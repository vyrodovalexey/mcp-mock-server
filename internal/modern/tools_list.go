package modern

import (
	"context"
	"encoding/json"

	"github.com/vyrodovalexey/mcp-mock-server/internal/catalogue"
	"github.com/vyrodovalexey/mcp-mock-server/internal/engine"
	"github.com/vyrodovalexey/mcp-mock-server/internal/wire"
)

// tools_list.go implements the tools/list handler (MOCK-202 method, serving
// MOCK-221/222 through the catalog). Phase 1 is UNPAGINATED — the whole catalog
// is returned in one response; cursors are MOCK-231, Phase 2, and their absence
// here is deliberate (wire.ToolListResult carries no nextCursor field).
//
// Wire clauses it depends on (provisional, GAP-003): annex 4.2 [P-21]
// (resultType "toolList"), the toolListResult / toolDescriptor shapes, annex
// 4.3/4.4 (_meta.serverInfo). All sourced from [wire] (ADR-019).
//
// # Large catalogs without materialization (MOCK-221, ADR-004)
//
// The catalog is virtual: catalog.At(kind, i) computes item i on demand from the
// instance seed, and catalog.Len(kind) reports the count without touching any
// generated item (ADR-004). This handler walks [0, Len) with At, so a 5000-tool
// catalog costs one Derive per item at REQUEST time and O(1) at construction —
// it never builds or holds a 5000-element source slice. The one allocation
// proportional to the catalog is the response descriptor slice itself, which is
// unavoidable: a tools/list response by definition carries every tool. It is
// preallocated to Len so the slice grows exactly once.
//
// # Built-in default catalog (builtin-tools.md 1.1)
//
// When the scenario configures NO catalog, the instance's catalog is empty, so
// this handler falls back to the three built-in tools — echo, sleep, fail — in
// that fixed order (builtin-tools.md 1.1 [D], deterministic for golden
// stability). When the scenario DOES author a catalog, the built-ins are
// suppressed so they never pollute an authored fixture (MOCK-202.20 /
// builtin-tools.md 1.1).
//
// # Determinism (§0.1)
//
// The handler draws no RNG and reads no clock. Generated names are seed-derived
// by the catalog (deterministic across runs and processes at a fixed seed), and
// authored raw-JSON schemas are emitted verbatim (MOCK-222.4), so two identical
// requests at a fixed seed produce byte-identical bodies.

// handleToolsList is the tools/list [engine.Handler]. It builds a
// wire.ToolListResult over the whole tool catalog — the authored/generated items
// when the scenario configured a catalog, or the three built-in descriptors when
// it did not — and marshals it byte-stably.
func handleToolsList(_ context.Context, ex *engine.Exchange) (json.RawMessage, *engine.Fault) {
	om := omissionsFor(ex.Snapshot)
	cfg := discoverConfig(ex.Snapshot)

	res := wire.ToolListResult{
		ResultType: resultTypePtr(wire.MethodToolsList, om),
		Tools:      listTools(ex.Snapshot),
		Meta:       serverInfoMeta(cfg, om),
	}

	body, err := wire.MarshalResult(res)
	if err != nil {
		return nil, engine.InvalidParamsFault("mcpmock: encoding tools/list result", nil).
			WithReason("marshal tools/list result: " + err.Error())
	}
	return body, nil
}

// listTools returns the tool descriptors for the response. It reads the virtual
// catalog through At/Len (never materializing the generated range) and, only
// when that catalog contains no tools, falls back to the built-in descriptors.
// The returned slice is never nil — an empty catalog with no built-ins would be
// an empty (non-nil) slice, which wire emits as [] — but Phase 1's fallback
// guarantees at least the three built-ins in that case.
func listTools(snap engine.Snapshot) []wire.ToolDescriptor {
	cs, ok := configOf(snap)
	if !ok {
		return builtinDescriptors()
	}
	cat := cs.Catalog()
	if cat == nil || cat.Len(catalog.KindTool) == 0 {
		return builtinDescriptors()
	}
	return catalogDescriptors(cat)
}

// catalogDescriptors walks the tool kind of the virtual catalog with At over
// [0, Len) and maps each catalog.Item to a wire.ToolDescriptor. It preallocates
// to Len so the append loop never reallocates, and it touches each item exactly
// once via At — the ADR-004 on-demand access that keeps a 5000-item catalog from
// being stored anywhere.
func catalogDescriptors(cat *catalog.Catalog) []wire.ToolDescriptor {
	n := cat.Len(catalog.KindTool)
	out := make([]wire.ToolDescriptor, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, descriptorFromItem(cat.At(catalog.KindTool, i)))
	}
	return out
}

// descriptorFromItem maps a catalog.Item to the wire tools/list descriptor. The
// raw-JSON schema members (InputSchema, OutputSchema, Annotations, Icons) are
// carried through byte-for-byte (MOCK-222.3): they are never parsed, so an
// authored or deliberately-malformed schema survives verbatim including key
// order (MOCK-222.4).
func descriptorFromItem(item catalog.Item) wire.ToolDescriptor {
	return wire.ToolDescriptor{
		Name:         item.Name,
		Title:        item.Title,
		Description:  item.Description,
		InputSchema:  item.InputSchema,
		OutputSchema: item.OutputSchema,
		Annotations:  item.Annotations,
		Icons:        item.Icons,
	}
}
