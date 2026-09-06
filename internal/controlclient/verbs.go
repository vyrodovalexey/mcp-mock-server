package controlclient

import (
	"context"
	"net/url"
	"strconv"

	"github.com/vyrodovalexey/mcp-mock-server/internal/control"
	"github.com/vyrodovalexey/mcp-mock-server/journalapi"
)

// This file is the client's projection of the ADR-015 control route table: one
// [Verb] per Phase 1 operation the CLI exposes. `mcpmock ctl` iterates [Verbs]
// to dispatch and to generate its --help, so a CLI verb cannot exist that the
// route table lacks, and a route the CLI omits shows up as a missing verb —
// TestRouteParity asserts the correspondence with internal/control's table.

// Verb is one CLI-facing control operation. It names the operationId it mirrors
// (the parity key against the server route table), a one-line help string, and
// whether it binds an instance {name} argument, plus the invoker that issues the
// request through a [Client] and returns a JSON-encodable result.
type Verb struct {
	// Name is the CLI verb, e.g. "instances" or "journal".
	Name string
	// OpID is the contracts/control-api.openapi.yaml operationId this verb
	// mirrors; it is the parity key against internal/control's route table.
	OpID string
	// Summary is the one-line help string shown by `mcpmock ctl --help`.
	Summary string
	// NeedsName reports whether the verb requires an instance name argument.
	NeedsName bool
	// invoke issues the request. name is the bound instance name (empty when
	// NeedsName is false); q carries parsed journal filters where relevant. It
	// returns a value to render as JSON, or nil for a no-body result.
	invoke func(ctx context.Context, c *Client, name string, q journalapi.Query) (any, error)
}

// Verbs returns the Phase 1 CLI verb table, freshly built (no package-level
// mutable state, ADR-007). The order is the display order for --help.
func Verbs() []Verb {
	return []Verb{
		{
			Name: "instances", OpID: "listInstances",
			Summary: "list every logical instance",
			invoke: func(ctx context.Context, c *Client, _ string, _ journalapi.Query) (any, error) {
				return c.Instances(ctx)
			},
		},
		{
			Name: "instance", OpID: "getInstance", NeedsName: true,
			Summary: "show one instance by name",
			invoke: func(ctx context.Context, c *Client, name string, _ journalapi.Query) (any, error) {
				return c.Instance(ctx, name)
			},
		},
		{
			Name: "seed", OpID: "getSeed",
			Summary: "show the effective root seed and its source",
			invoke: func(ctx context.Context, c *Client, _ string, _ journalapi.Query) (any, error) {
				return c.Seed(ctx)
			},
		},
		{
			Name: "health", OpID: "getHealth",
			Summary: "show process health",
			invoke: func(ctx context.Context, c *Client, _ string, _ journalapi.Query) (any, error) {
				return c.Health(ctx)
			},
		},
		{
			Name: "journal", OpID: "getJournal", NeedsName: true,
			Summary: "read one instance's request journal",
			invoke: func(ctx context.Context, c *Client, name string, q journalapi.Query) (any, error) {
				return c.Journal(ctx, name, q)
			},
		},
		{
			Name: "correlations", OpID: "getCorrelations", NeedsName: true,
			Summary: "read one instance's request correlation chains",
			invoke: func(ctx context.Context, c *Client, name string, q journalapi.Query) (any, error) {
				return c.Correlations(ctx, name, q)
			},
		},
		{
			Name: "clear-journal", OpID: "clearJournal", NeedsName: true,
			Summary: "clear one instance's request journal",
			invoke: func(ctx context.Context, c *Client, name string, _ journalapi.Query) (any, error) {
				return nil, c.ClearJournal(ctx, name)
			},
		},
	}
}

// Invoke runs the verb against c, returning the value to render (nil for a
// no-body result) or an error.
func (v Verb) Invoke(ctx context.Context, c *Client, name string, q journalapi.Query) (any, error) {
	return v.invoke(ctx, c, name, q)
}

// --- typed operations, one per verb ---

// Instances lists every logical instance in stable configuration order.
func (c *Client) Instances(ctx context.Context) ([]control.InstanceInfo, error) {
	var out []control.InstanceInfo
	if err := c.get(ctx, "/instances", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Instance returns the named instance, or a wrapped control.ErrNotFound.
func (c *Client) Instance(ctx context.Context, name string) (control.InstanceInfo, error) {
	var out control.InstanceInfo
	if err := c.get(ctx, "/instances/"+url.PathEscape(name), nil, &out); err != nil {
		return control.InstanceInfo{}, err
	}
	return out, nil
}

// Seed returns the effective root seed and its source (MOCK-704.2).
func (c *Client) Seed(ctx context.Context) (control.SeedInfo, error) {
	var out control.SeedInfo
	if err := c.get(ctx, "/seed", nil, &out); err != nil {
		return control.SeedInfo{}, err
	}
	return out, nil
}

// Health returns process health.
func (c *Client) Health(ctx context.Context) (control.Health, error) {
	var out control.Health
	if err := c.get(ctx, "/health", nil, &out); err != nil {
		return control.Health{}, err
	}
	return out, nil
}

// Journal reads one page of the named instance's journal matching q.
func (c *Client) Journal(ctx context.Context, name string, q journalapi.Query) (journalapi.Page, error) {
	var out journalapi.Page
	if err := c.get(ctx, "/instances/"+url.PathEscape(name)+"/journal", journalQuery(q), &out); err != nil {
		return journalapi.Page{}, err
	}
	return out, nil
}

// Correlations reads the named instance's correlation chains matching q.
func (c *Client) Correlations(ctx context.Context, name string, q journalapi.Query) ([]journalapi.Correlation, error) {
	var out []journalapi.Correlation
	path := "/instances/" + url.PathEscape(name) + "/journal/correlations"
	if err := c.get(ctx, path, journalQuery(q), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ClearJournal empties the named instance's journal (DELETE, 204).
func (c *Client) ClearJournal(ctx context.Context, name string) error {
	return c.del(ctx, "/instances/"+url.PathEscape(name)+"/journal", nil)
}

// journalQuery renders the subset of journal filters the CLI exposes into query
// parameters the server's parseJournalQuery understands. Only non-zero fields
// are emitted, so an unfiltered read sends no parameters.
func journalQuery(q journalapi.Query) url.Values {
	v := url.Values{}
	if q.Method != "" {
		v.Set("method", q.Method)
	}
	if q.Transport != "" {
		v.Set("transport", q.Transport)
	}
	if q.Limit > 0 {
		v.Set("limit", strconv.Itoa(q.Limit))
	}
	if q.After > 0 {
		v.Set("after", strconv.FormatUint(q.After, 10))
	}
	return v
}
