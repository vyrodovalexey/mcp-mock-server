---
id: ADR-004
title: Virtual (procedurally generated) catalogue with an overlay of authored items
status: accepted
date: 2026-09-04
reversibility: MEDIUM — internal, but the generation function's naming scheme becomes a fixture contract
requirements: MOCK-221, MOCK-222, MOCK-223, MOCK-227, MOCK-228, MOCK-231, MOCK-904, MOCK-107
---

# ADR-004 — Virtual catalogue

## Context

`MOCK-221` requires catalogues of ≥5000 tools. `MOCK-904` requires ≥200 logical instances in one
process. `MOCK-107` requires startup under 200 ms.

Materialising the cross product is not viable. 5000 tools × 200 instances = 1 000 000 tool
objects. Each tool carries `name`, `title`, `description`, `icons`, `annotations`,
`inputSchema`, `outputSchema` — realistically 1–4 KB serialised, more as parsed Go structs. That
is multiple gigabytes of heap and seconds of allocation at startup, and it makes GC pauses a
factor in the `MOCK-901` throughput measurement.

`MOCK-227` (catalogue drift at a controlled moment) and `MOCK-228` (different tool sets per
token/scope) additionally mean the catalogue is not a single fixed list — it is a *function of*
(instance, time/call-count, principal).

## Decision

The catalogue is a **pure function**, not a data structure.

```
type Catalogue interface {
    Len(kind Kind, view View) int
    At(kind Kind, view View, i int) Item          // O(1), allocates one Item
    Page(kind Kind, view View, cur Cursor, n int) ([]Item, Cursor, error)
    IndexOf(kind Kind, view View, name string) (int, bool)
}

type View struct {
    Snapshot  *Snapshot   // generation params, overlays, drift generation number
    Principal Principal   // scopes/subject — MOCK-228
}
```

Three layers, resolved at `At()`:

1. **Generator** — `item(i)` is computed from `instanceKey.Derive("catalogue", kind, i)`. Names
   follow a configured template (`nameTemplate: "tool_{{i:05d}}"`), descriptions and schemas are
   drawn from a small set of shapes selected by the derived RNG. Cost: one HMAC + one struct
   allocation. Zero memory at rest.
2. **Overlay** — authored items from the scenario file (`MOCK-222`) and edge-case items
   (`MOCK-223`, `MOCK-224`, `MOCK-225`). Stored as a sorted, immutable slice inside `Snapshot`.
   Overlay items either *append* to the generated range or *replace* a generated index by name.
   Bounded by the scenario size, typically tens of items.
3. **Drift patches** (`MOCK-227`) — an immutable, ordered list of `{targetName, field, newValue,
   effectiveAtGeneration}` in `Snapshot`. A drift event bumps `Snapshot.CatalogueGeneration` via
   a copy-on-write swap (ADR-014); items recomputed afterwards observe the patch. Because
   generation is a pure function of the snapshot, drift needs no invalidation logic anywhere else.

Authorisation-dependent catalogues (`MOCK-228`) are a `View` predicate, not a separate catalogue:
each item carries `requiredScopes`, and `Len`/`At` operate over a **scope-filtered index**. The
filtered index is computed lazily and memoised per distinct scope-set hash in a small bounded LRU
(`≤64` entries per instance) so that pagination over 5000 items is not O(N) per page.

Ordering (`MOCK-226`) is applied as an index permutation on top: identity (deterministic) or
`ordering.Shuffle` (deliberate disorder). Never a re-sort of materialised items.

## Options considered

1. **Materialise everything at load** — rejected on memory and startup grounds above.
2. **Materialise lazily with a full cache** — rejected: under 200 instances a test that lists all
   tools on every instance ends up materialising everything anyway, just later and less
   predictably. Worst case is identical; the failure is just deferred into the middle of a test.
3. **Materialise only when an overlay/drift touches the item (chosen, layer 2/3).**
4. **Generate into a memory-mapped file** — rejected: complexity, breaks `MOCK-107` embedding,
   platform-specific.

## Consequences

**Positive.** A 5000-tool, 200-instance fleet costs O(overlay) memory — kilobytes. Startup stays
in budget. Drift is trivially correct because there is no cache to invalidate. Pagination cursors
can encode an *index*, which makes `MOCK-231`'s "overlapping or missing items between pages"
faults easy to construct precisely (ADR-008 / `paging`).

**Negative.** `At()` recomputes on every access; `tools/list` of a 100-item page costs 100 HMACs
(~100 µs). Acceptable: `tools/list` is not the `MOCK-901` hot path (`tools/call` is), and the
result of a page can be cached per (view, cursor) in the same bounded LRU if measurement demands.
Generated content is necessarily formulaic — realistic-looking catalogues need the overlay.

**Forecloses.** A control-API operation that mutates a single generated item *in place* by index
without a name. All mutation is expressed as a drift patch keyed by name, which is also what
makes it reproducible from the scenario file.

**Testing note.** Because generation is pure, `catalogue` is exhaustively unit-testable without a
server: `TestGeneratedCatalogueStable` fixes a seed and golden-files items 0, 1, 4999.
