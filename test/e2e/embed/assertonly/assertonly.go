// Package assertonly is the RISK-07 witness: it imports ONLY the two
// standard-library-only public packages a journal-consuming test needs —
// journalapi and assert — and nothing else from mcpmock. ADR-001 §6.2 promises
// that this import set pulls in NO internal server package, NO
// prometheus/client_golang and NO go.opentelemetry.io/otel, so a hub test that
// merely inspects a recorded journal does not inherit the full server
// dependency graph.
//
// The transitive-graph test in the parent embed package runs `go list -deps`
// over THIS package and asserts the promise, recording the graph as an
// artifact. This file exists so the graph has a concrete, minimal root to
// measure; it deliberately does something trivial with each import so neither is
// elided.
//
// It carries NO wire vocabulary as source literals (no method names) so it does
// not trip the ADR-019 containment scan the parent module runs over the whole
// tree; the method to filter by is supplied by the caller, keeping this witness
// pure plumbing.
package assertonly

import (
	"github.com/vyrodovalexey/mcp-mock-server/assert"
	"github.com/vyrodovalexey/mcp-mock-server/journalapi"
)

// CountMethod loads an NDJSON journal from path and returns the number of
// records whose JSON-RPC method matches the caller-supplied glob — a minimal use
// of both public packages, using nothing else from mcpmock and embedding no wire
// literal of its own.
func CountMethod(path, method string) (int, error) {
	view, err := assert.FromFile(path)
	if err != nil {
		return 0, err
	}
	return view.Filter(journalapi.Selector{Method: method}).Len(), nil
}
