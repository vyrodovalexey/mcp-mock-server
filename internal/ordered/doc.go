// Package ordered provides deterministic, insertion-ordered container types
// that never leak Go's randomized map iteration order onto any output path.
//
// # Why this package exists
//
// Go randomizes map iteration order by design. Any code that builds a JSON
// object, a header set, a list result or a log line by ranging over a plain
// map produces different bytes on different runs. That defeats the mcpmock
// determinism guarantee (design principle §0.1) and makes golden-file journal
// comparison (MOCK-604) unusable, because every diff would be noise. See
// ADR-003 for the governing decision.
//
// The types here are the only permitted containers for anything that reaches
// output:
//
//   - [Map] — an insertion-ordered map with deterministic iteration,
//     SortedKeys and a Range that visits pairs in insertion order.
//   - [Set] — an insertion-ordered set with the same guarantees.
//   - [Slice] — an ordered sequence that preserves every element, including
//     duplicates, in the order appended, with an explicit Sort. It is used by
//     the journal (ADR-005) to hold HTTP headers in wire order — duplicates
//     and original casing intact — because http.Header canonicalization and
//     map ordering would otherwise destroy exactly that evidence.
//
// These are thin wrappers over a slice plus, for [Map] and [Set], an index map
// used only for O(1) lookup and never for iteration. Iteration order derives
// solely from the backing slice, so it is a pure function of the sequence of
// mutations — independent of GOMAXPROCS, of scheduling, and of the process.
//
// # Import rule
//
// This package is the base of the module dependency graph
// (architecture.md §6.1 rule 2): it imports nothing from the module and only
// cmp, encoding/json and slices from the standard library. Nothing internal
// may be added to its import list; make deps-check enforces this.
//
// # Concurrency contract
//
// The types in this package are NOT safe for concurrent mutation. A single
// value may be mutated by at most one goroutine at a time; concurrent
// mutation, or mutation concurrent with any read, is a data race and must be
// externally synchronized.
//
// Concurrent reads of a value that is not being mutated are safe: reads never
// modify internal state, and iteration order is fixed by the mutation history
// alone. In mcpmock these containers live inside copy-on-write snapshots
// (ADR-014), so a published value is effectively immutable and freely shared
// across the ~200 concurrent logical instances without a lock. That is the
// intended usage; this package deliberately holds no process-global mutable
// state (ADR-007).
//
// Deliberate, seeded disorder (MOCK-226) is never produced here. It is a
// separate mechanism — ordering.Shuffle over a seeded RNG (ADR-002/ADR-003) —
// so accidental disorder and requested disorder can never share a code path.
package ordered
