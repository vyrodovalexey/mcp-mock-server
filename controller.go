package mcpmock

import (
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"time"

	"github.com/vyrodovalexey/mcp-mock-server/internal/control"
	"github.com/vyrodovalexey/mcp-mock-server/internal/instance"
	"github.com/vyrodovalexey/mcp-mock-server/internal/journal"
	"github.com/vyrodovalexey/mcp-mock-server/journalapi"
)

// controller is the in-process implementation of [Control] (ADR-015's first and
// truest front end: the real one, wrapped by the HTTP handler and reached
// directly by embedded MOCK-107 tests). It reads and mutates the Server's own
// instances with no HTTP round trip. It holds only a pointer back to the Server,
// so it carries no state of its own and two Servers' controllers never collide
// (ADR-007).
type controller struct {
	s *Server
}

// Control returns the in-process control operations for this Server (ADR-015,
// contracts/library-api.md §2). It performs no HTTP round trip; the HTTP and
// unix-socket front ends wrap this same implementation, which is what makes the
// three front ends behave identically. The returned value is safe for concurrent
// use.
func (s *Server) Control() Control { return &controller{s: s} }

// Instances implements [Control].
func (c *controller) Instances(_ context.Context) ([]InstanceInfo, error) {
	out := make([]InstanceInfo, 0, len(c.s.instances))
	for _, in := range c.s.instances {
		out = append(out, c.info(in))
	}
	return out, nil
}

// Instance implements [Control]. An unknown name is [ErrNotFound], which wraps
// the internal control sentinel so a facade caller matches it with errors.Is and
// the HTTP front end maps it to 404 (one identity, ADR-015).
func (c *controller) Instance(_ context.Context, name string) (InstanceInfo, error) {
	in, ok := c.s.Instance(name)
	if !ok {
		return InstanceInfo{}, fmt.Errorf("%w: instance %q", ErrNotFound, name)
	}
	return c.info(in), nil
}

// Seed implements [Control] (MOCK-704.2). The source is "flag" when the caller
// supplied an explicit seed and "random" otherwise, matching how [effectiveSeed]
// derived it.
func (c *controller) Seed(_ context.Context) (SeedInfo, error) {
	return SeedInfo{Seed: c.s.seed, Source: c.s.seedSource}, nil
}

// Health implements [Control].
func (c *controller) Health(_ context.Context) (Health, error) {
	return Health{
		Status:        "ok",
		Instances:     len(c.s.instances),
		UptimeSeconds: time.Since(c.s.startedAt).Seconds(),
		Seed:          c.s.seed,
		SafeMode:      false,
	}, nil
}

// For implements [Control], returning the per-instance operations.
func (c *controller) For(name string) InstanceControl {
	return &instanceController{s: c.s, name: name}
}

// info builds the read model for one instance.
func (c *controller) info(in *Instance) InstanceInfo {
	ring, _ := in.inst.Journal()
	var ji control.JournalInfo
	if ring != nil {
		ji = control.JournalInfo{Records: ring.Len(), Dropped: ring.Dropped(), Bytes: ring.Bytes()}
	}
	return InstanceInfo{
		Name:       in.inst.Name(),
		MountPath:  in.inst.MountPath(),
		URL:        in.url,
		Era:        in.inst.Snapshot().Era(),
		Generation: in.inst.Snapshot().Gen(),
		Journal:    ji,
		Hostile:    false,
	}
}

// instanceController is the in-process [InstanceControl]. It resolves the named
// instance per call, so a name removed between calls surfaces as a not-found on
// the individual operation rather than being cached.
type instanceController struct {
	s    *Server
	name string
}

// resolve returns the internal instance and its journal ring, or a not-found
// error. A nil ring means the instance disabled journalling.
func (ic *instanceController) resolve() (*instance.Instance, *journal.Ring, error) {
	in, ok := ic.s.Instance(ic.name)
	if !ok {
		return nil, nil, fmt.Errorf("%w: instance %q", ErrNotFound, ic.name)
	}
	ring, _ := in.inst.Journal()
	return in.inst, ring, nil
}

// Journal implements [InstanceControl] (MOCK-602.1/602.4/602.7). It reads the
// ring through the TASK-012 query surface — a consistent snapshot when the query
// asks for one — filters by the query's selector, applies the seq cursor and
// limit, and returns a page in Seq order.
func (ic *instanceController) Journal(_ context.Context, q journalapi.Query) (journalapi.Page, error) {
	_, ring, err := ic.resolve()
	if err != nil {
		return journalapi.Page{}, err
	}
	if ring == nil {
		return journalapi.Page{}, nil
	}
	sel := q.Selector
	var matched []journalapi.Record
	if q.Consistent {
		matched = ring.QueryConsistent(sel)
	} else {
		matched = ring.Query(sel)
	}
	return page(matched, q, ring.Dropped()), nil
}

// JournalStream implements [InstanceControl] (MOCK-602.2). It streams through
// [journal.Ring.ExportNDJSON] — the bounded k-way shard merge that holds at most
// ≤64 records in flight and never materializes the journal — piping its output
// into a decoder that yields one record at a time. The pipe writer runs in a
// goroutine bounded by ctx; iteration draining stops it. This is the streaming
// path MOCK-602.2 requires: memory stays constant beyond the shard heads.
func (ic *instanceController) JournalStream(
	ctx context.Context, q journalapi.Query,
) (iter.Seq2[journalapi.Record, error], error) {
	_, ring, err := ic.resolve()
	if err != nil {
		return nil, err
	}
	if ring == nil {
		return func(func(journalapi.Record, error) bool) {}, nil
	}
	return streamRing(ctx, ring, q), nil
}

// streamRing returns an iterator that drives ExportNDJSON through an io.Pipe and
// decodes records off the read side, so the journal is never materialized. The
// exporter goroutine is bounded by ctx and by the reader draining the pipe: if
// the consumer stops early, closing the read side makes the next write fail and
// the goroutine returns.
func streamRing(ctx context.Context, ring *journal.Ring, q journalapi.Query) iter.Seq2[journalapi.Record, error] {
	return func(yield func(journalapi.Record, error) bool) {
		pr, pw := io.Pipe()
		go func() {
			_, exErr := ring.ExportNDJSON(ctx, pw, journal.ExportOptions{Selectors: []journalapi.Selector{q.Selector}})
			_ = pw.CloseWithError(exErr)
		}()
		defer func() { _ = pr.Close() }()
		rd := journalapi.NewReader(pr)
		for {
			rec, rErr := rd.Read()
			if errors.Is(rErr, io.EOF) {
				return
			}
			if rErr != nil {
				yield(journalapi.Record{}, rErr)
				return
			}
			if !yield(rec, nil) {
				return
			}
		}
	}
}

// Correlations implements [InstanceControl] (MOCK-605). Phase 1 carries no MRTR
// traffic, so grouping a modern-only journal yields no chains; the operation
// exists so the route and the interface method stay paired for parity.
func (ic *instanceController) Correlations(_ context.Context, q journalapi.Query) ([]journalapi.Correlation, error) {
	_, ring, err := ic.resolve()
	if err != nil {
		return nil, err
	}
	if ring == nil {
		return nil, nil
	}
	view := ring.View().Filter(q.Selector)
	return journalapi.Correlations(view), nil
}

// ClearJournal implements [InstanceControl] (MOCK-602.6/702.7). It clears through
// [instance.Instance.ClearJournal], which briefly disables ring writes so a
// concurrent writer never observes a half-cleared ring, and preserves the global
// sequence counter so Seq stays monotone across the clear.
func (ic *instanceController) ClearJournal(_ context.Context) error {
	in, _, err := ic.resolve()
	if err != nil {
		return err
	}
	in.ClearJournal()
	return nil
}

// page assembles a [journalapi.Page] from the matched records: it applies the
// After seq cursor, caps to the query limit, and reports the total match count,
// the next cursor and the dropped count. The records arrive in Seq order from the
// query surface, so paging is a forward window over them.
func page(matched []journalapi.Record, q journalapi.Query, dropped uint64) journalapi.Page {
	total := 0
	out := make([]journalapi.Record, 0, len(matched))
	limit := q.Limit
	for _, r := range matched {
		if r.Seq <= q.After {
			continue
		}
		total++
		if limit > 0 && len(out) >= limit {
			continue
		}
		out = append(out, r)
	}
	var next uint64
	if limit > 0 && total > len(out) && len(out) > 0 {
		next = out[len(out)-1].Seq
	}
	return journalapi.Page{Records: out, NextAfter: next, Total: total, Dropped: dropped}
}
