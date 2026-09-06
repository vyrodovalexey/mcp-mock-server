// Package journal implements the storage engine behind the mcpmock request
// journal: a sharded, preallocated ring buffer with a per-slot seqlock and a
// single global sequence, exactly as ADR-005 specifies. It is the storage half
// of internal/journal; capture, query and NDJSON wiring (TASK-012) build on the
// primitives here.
//
// The journal is mcpmock's most valuable output (design principle §0.3), so the
// engine's overriding obligation is that a reader never observes a torn record:
// a record it hands back is either fully consistent with a single write, or is
// retried, or is skipped and counted. It is never a half-written mixture, which
// would silently corrupt the evidence the tool exists to provide.
//
// # Structure (ADR-005)
//
//	Ring
//	 ├── enabled   atomic.Bool     // MOCK-901 fast path
//	 ├── seq       atomic.Uint64   // global total order across shards
//	 ├── dropped   atomic.Uint64   // drop-oldest evictions (metric)
//	 ├── degraded  atomic.Uint64   // block-timeout degradations (distinct)
//	 ├── rejected  atomic.Uint64   // error-policy rejections
//	 ├── bytes     atomic.Int64    // retained-byte accounting
//	 └── shards[S] where S = next_pow2(GOMAXPROCS), capped at 64
//	        ├── cursor  atomic.Uint64   // monotonic write index
//	        ├── drained atomic.Uint64   // consumed index (block backpressure)
//	        └── ring    []slot          // preallocated, len = pow2, per shard
//	              └── (per slot) state atomic.Uint32 // seqlock: even stable, odd writing
//
// # Fast path (MOCK-901)
//
// [Ring.Write] begins with a single atomic load of enabled. When journaling is
// off it returns immediately: no lock, no branch-heavy work, and — because the
// [journalapi.Record] argument is taken by value and never escapes — no heap
// allocation. TestWriteDisabledZeroAllocs and BenchmarkWriteDisabled prove the
// zero-allocation property with testing.AllocsPerRun.
//
// # Seqlock protocol
//
// Each slot carries an atomic.Uint32 state. A writer does:
//
//	state.Add(1)   // even -> odd: "being written"
//	fill(slot)     // copy the record's fields into the slot
//	state.Add(1)   // odd -> even: "stable", state advanced by two
//
// A reader does:
//
//	s1 := state.Load(); if s1 is odd { retry or skip }
//	copy the record out of the slot
//	s2 := state.Load(); if s2 != s1 { retry or skip }  // a writer intervened
//
// A reader that loses the race a bounded number of times skips the slot and
// counts it rather than spinning forever, so a hot writer cannot starve a
// reader (ADR-005 Consequences). Callers that require a guaranteed-consistent
// view request one through [Ring.ConsistentView], which briefly disables writes
// so no writer can intervene during the copy.
//
// # Memory bound (MOCK-902)
//
// Two bounds are enforced, whichever binds first: MaxRecords (default 100 000,
// rounded up to a power of two and divided across shards) and MaxBytes (default
// 256 MiB). The ring is preallocated to MaxRecords slots at construction, so the
// steady-state write path allocates nothing for fixed fields; only the record's
// own byte slices (body, headers) are retained, and they are owned by the record
// and released to the garbage collector when their slot is overwritten.
//
// # No pooling (ADR-005)
//
// Record byte slices are never pooled or reused. Pooling a body while a reader
// may still hold a copy of the slot is a use-after-free class of bug that would
// corrupt test evidence — unacceptable in a tool whose purpose is evidence. The
// engine trades the resulting garbage-collector pressure for correctness; that
// pressure is measured, not assumed, by the package benchmarks.
//
// # Overflow (MOCK-902)
//
// When a bound would be exceeded the configured [journalapi.OverflowPolicy]
// decides the outcome:
//
//   - drop-oldest (default): overwrite the oldest slot, increment the dropped
//     counter, and set [journalapi.Record.Dropped] on the incoming record so a
//     reader sees that a gap precedes it.
//   - block: wait up to BlockTimeout for a reader to drain capacity, honoring
//     context cancellation; on timeout degrade to drop-oldest and increment a
//     distinct counter. An unbounded block would be a self-inflicted denial of
//     service in the system under test (GAP-017), so the wait is always bounded.
//   - error: reject the write, increment the rejected counter, and report the
//     rejection so the caller can emit a 503 with a journalled reason.
//
// # Concurrency contract
//
// [Ring.Write] is safe for concurrent use by any number of goroutines. The read
// methods ([Ring.View], [Ring.Snapshot], [Ring.ConsistentView], [Ring.Iter]) are
// safe to call concurrently with writes and with each other. The zero value of
// [Ring] is not usable; construct one with [New]. There is no process-global
// state (ADR-007): every [Ring] is reachable only from an explicit [New], so the
// ≥200 logical instances each own an independent journal.
package journal
