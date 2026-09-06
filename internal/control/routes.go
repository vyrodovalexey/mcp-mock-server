package control

import (
	"context"
	"net/http"

	"github.com/vyrodovalexey/mcp-mock-server/journalapi"
)

// This file is the single route table (ADR-015): every control operation is one
// row here, and every front end — HTTP, unix socket and (TASK-024) the CLI — is
// driven by it. A route names the OpenAPI operationId it implements so
// TestControlSurfaceParity can assert a bijection between the contract's
// Phase 1 operations and this table. Adding an operation is one row plus one
// Backend method; the two grow together and cannot drift.

// route is one entry in the control route table. It binds an HTTP method and a
// path template (with a single optional {name} segment) to the operationId it
// implements and to a handler that adapts the request to a [Backend] call.
type route struct {
	// method is the HTTP method.
	method string
	// tmpl is the path template under the /v1 prefix, e.g.
	// "/instances/{name}/journal". A "{name}" segment binds the instance name.
	tmpl string
	// opID is the contracts/control-api.openapi.yaml operationId this route
	// implements. It is the parity key.
	opID string
	// handle produces the response value, HTTP status and error. A nil error
	// with a nil value and a status writes an empty-body response (e.g. 204).
	handle func(ctx context.Context, b Backend, rc *reqContext) (any, int, error)
}

// reqContext carries the parsed request into a route handler: the bound instance
// name (empty for process-scoped routes), the parsed journal query, and the
// negotiated output format.
type reqContext struct {
	// name is the {name} path segment, or "" for a process-scoped route.
	name string
	// query is the parsed journal query (only meaningful on journal routes).
	query journalapi.Query
	// ndjson reports whether the client asked for the NDJSON streaming form,
	// via Accept: application/x-ndjson or ?format=ndjson (MOCK-602.2).
	ndjson bool
}

// operationId values, one per Phase 1 route. Kept as constants so the route
// table and the parity test reference the same identifiers the OpenAPI document
// uses, with no stray string literals.
const (
	opGetOpenAPI      = "getOpenAPI"
	opGetSeed         = "getSeed"
	opGetHealth       = "getHealth"
	opListInstances   = "listInstances"
	opGetInstance     = "getInstance"
	opGetJournal      = "getJournal"
	opClearJournal    = "clearJournal"
	opGetCorrelations = "getCorrelations"
)

// phase1Routes returns the Phase 1 control route table. It is a function rather
// than a package var so the table is freshly built per [Handler] and this
// package keeps no package-level mutable state (ADR-007). The getOpenAPI route
// carries a nil handler because the [Handler] serves that document specially
// (raw YAML, not JSON).
func phase1Routes() []route {
	return []route{
		{
			method: http.MethodGet, tmpl: "/openapi.yaml", opID: opGetOpenAPI,
			handle: nil, // served specially by the Handler (raw YAML, not JSON)
		},
		{
			method: http.MethodGet, tmpl: "/seed", opID: opGetSeed,
			handle: func(ctx context.Context, b Backend, _ *reqContext) (any, int, error) {
				s, err := b.Seed(ctx)
				return s, http.StatusOK, err
			},
		},
		{
			method: http.MethodGet, tmpl: "/health", opID: opGetHealth,
			handle: func(ctx context.Context, b Backend, _ *reqContext) (any, int, error) {
				h, err := b.Health(ctx)
				return h, http.StatusOK, err
			},
		},
		{
			method: http.MethodGet, tmpl: "/instances", opID: opListInstances,
			handle: func(ctx context.Context, b Backend, _ *reqContext) (any, int, error) {
				list, err := b.Instances(ctx)
				return list, http.StatusOK, err
			},
		},
		{
			method: http.MethodGet, tmpl: "/instances/{name}", opID: opGetInstance,
			handle: func(ctx context.Context, b Backend, rc *reqContext) (any, int, error) {
				info, err := b.Instance(ctx, rc.name)
				return info, http.StatusOK, err
			},
		},
		{
			method: http.MethodGet, tmpl: "/instances/{name}/journal", opID: opGetJournal,
			handle: func(ctx context.Context, b Backend, rc *reqContext) (any, int, error) {
				page, err := b.For(rc.name).Journal(ctx, rc.query)
				return page, http.StatusOK, err
			},
		},
		{
			method: http.MethodDelete, tmpl: "/instances/{name}/journal", opID: opClearJournal,
			handle: func(ctx context.Context, b Backend, rc *reqContext) (any, int, error) {
				err := b.For(rc.name).ClearJournal(ctx)
				return nil, http.StatusNoContent, err
			},
		},
		{
			method: http.MethodGet, tmpl: "/instances/{name}/journal/correlations", opID: opGetCorrelations,
			handle: func(ctx context.Context, b Backend, rc *reqContext) (any, int, error) {
				cs, err := b.For(rc.name).Correlations(ctx, rc.query)
				return cs, http.StatusOK, err
			},
		},
	}
}

// matchRoute finds the route matching method and the /v1-stripped path segments,
// binding the {name} segment into rc. It returns the route, whether a path
// matched at all (for a 404 vs 405 distinction), and whether the method matched.
func matchRoute(routes []route, method string, segs []string, rc *reqContext) (r route, pathOK, methodOK bool) {
	for _, cand := range routes {
		tmplSegs := splitPath(cand.tmpl)
		name, ok := matchSegments(tmplSegs, segs)
		if !ok {
			continue
		}
		pathOK = true
		if cand.method != method {
			continue
		}
		rc.name = name
		return cand, true, true
	}
	return route{}, pathOK, false
}

// matchSegments reports whether tmplSegs matches segs, treating a "{name}"
// template segment as a wildcard and returning its bound value.
func matchSegments(tmplSegs, segs []string) (name string, ok bool) {
	if len(tmplSegs) != len(segs) {
		return "", false
	}
	for i, t := range tmplSegs {
		if t == "{name}" {
			name = segs[i]
			continue
		}
		if t != segs[i] {
			return "", false
		}
	}
	return name, true
}

// splitPath splits a "/a/b/c" path into its non-empty segments. A leading and
// trailing slash produce no empty segments, so "/instances/" and "/instances"
// match the same template.
func splitPath(p string) []string {
	out := make([]string, 0, 4)
	start := -1
	for i := 0; i < len(p); i++ {
		if p[i] == '/' {
			if start >= 0 {
				out = append(out, p[start:i])
				start = -1
			}
			continue
		}
		if start < 0 {
			start = i
		}
	}
	if start >= 0 {
		out = append(out, p[start:])
	}
	return out
}
