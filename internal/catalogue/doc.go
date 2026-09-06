// Package catalog implements mcpmock's virtual, procedurally generated
// primitive catalog with an overlay of hand-authored items (ADR-004,
// MOCK-221/MOCK-222). It produces catalog DATA — names, titles, descriptions
// and the four verbatim raw-JSON payloads of a primitive — and deliberately
// does NOT speak the wire: turning an [Item] into a tools/list descriptor is
// owned by internal/wire and internal/modern. This package therefore imports no
// wire vocabulary and no serialization contract.
//
// # The catalog is a pure function, not a data structure (ADR-004)
//
// The central claim of ADR-004 is that a catalog of N items costs O(overlay)
// memory, not O(N): a 5000-tool catalog is kilobytes, not gigabytes, and 200
// logical instances (MOCK-904) each holding one costs nothing per generated
// item. This is achieved by never materializing the generated range. [At]
// computes item i on demand from instanceKey.Derive(DomainCatalogue, …); [Len]
// returns the configured count; nothing generated is stored. Constructing a
// [Kind] view is O(overlay), independent of the generated count, and this is
// asserted by an allocation benchmark (BenchmarkConstruct) and by
// TestConstructDoesNotMaterialise.
//
// # Deterministic names (MOCK-221.3)
//
// Every generated name is a pure function of the instance key, the kind and the
// index: the same seed and configuration yield byte-identical names on every
// process and at every GOMAXPROCS setting. Names follow the configured
// nameTemplate (default "tool_{{i:05d}}" and its per-kind analogs); the "i"
// placeholder is the zero-padded index. Because the mapping index→name is a
// pure, invertible function, [IndexOf] recovers the index from a name without a
// stored index, and the round-trip invariant IndexOf(At(i).Name) == i holds for
// every generated index (ADR-004's load-bearing invariant, MOCK-221.5).
//
// # Overlay precedence rule (MOCK-222.2)
//
// Authored items from the scenario (scenario.Catalog.Items) overlay the
// generated range with a single, explicit precedence rule:
//
//   - REPLACE: an authored item whose name equals the generated name at some
//     index i takes that index i. [At](i) then returns the authored item, and
//     [IndexOf] of that name returns i. The generated item at i is shadowed
//     entirely; it does not also appear elsewhere.
//   - APPEND: an authored item whose name is not any generated name is appended
//     after the generated range, occupying an index in [count, count+extra).
//     Appended authored items are ordered by name (lexicographic on the raw
//     bytes) so their indices are deterministic and independent of the order
//     they appear in the scenario file.
//
// Two authored items that share a name within one kind are a configuration
// error the schema and composition (merge-by-name, TASK-007) reject before this
// package sees them; the constructor treats a duplicate defensively as
// last-wins and never panics. An authored item's Kind selects which kind's
// overlay it joins; an item with no explicit kind defaults to "tool" per the
// scenario schema, matched here by [KindFromString].
//
// # Verbatim payloads (MOCK-222.3 / MOCK-222.4)
//
// An authored item's Icons, Annotations, InputSchema and OutputSchema are
// [json.RawMessage] carried through UNTOUCHED: never validated, never
// normalized, never re-serialized, so key order and byte content survive
// exactly as authored. Generated items carry a generated inputSchema shape and
// no authored payloads.
//
// # Ordering guarantee (MOCK-226 Phase-1 subset)
//
// Iteration by ascending index is the only order Phase 1 exposes and it is
// fully deterministic: [At](0), [At](1), … [At](Len-1) never depends on Go map
// iteration order, on GOMAXPROCS or on the process. The overlay is stored in a
// sorted, immutable slice; lookup uses a sorted-key index. MOCK-226's second
// mode (deliberate, reproducible disorder) is a Phase-3 index permutation
// layered on top and is out of scope here — but the deterministic guarantee it
// will contrast against is made real now.
//
// # Out of scope (later phases)
//
// Pagination and cursors (MOCK-231, Phase 2), catalog drift (MOCK-227, Phase
// 3), authorization-scoped views (MOCK-228, Phase 3), name edge cases
// (MOCK-223, Phase 3), x-mcp-header annotations (MOCK-224, Phase 3) and schema
// edge cases (MOCK-225, Phase 3) are deliberately absent. RequiredScopes and
// RequiredClientCapabilities on generated and authored items are carried into
// the [Item] so a later View predicate can filter on them without regenerating,
// but Phase 1 applies no filter.
//
// # Concurrency and globals (ADR-007)
//
// A [Catalog] is immutable after [New] returns and holds no process-global
// state: the ~200 instances that each own one share this code, not its state.
// All methods are safe for concurrent use.
package catalog
