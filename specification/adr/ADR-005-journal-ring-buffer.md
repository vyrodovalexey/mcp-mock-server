---
id: ADR-005
title: Journal storage — sharded preallocated ring buffer with a global sequence
status: accepted
date: 2026-09-04
reversibility: MEDIUM — internal storage is replaceable; journalapi.Record is a public contract (HARD)
requirements: MOCK-601, MOCK-602, MOCK-604, MOCK-605, MOCK-606, MOCK-902, MOCK-901
---

# ADR-005 — Journal storage and overflow

## Context

The journal is the product (§0.3). It must record, per `MOCK-601`, wall + monotonic timestamps,
transport, **all HTTP headers**, **full body**, decoded `_meta`, hashed credential, peer address
and the produced response.

`MOCK-902` requires ≥5000 rps *with* full journaling, a **bounded ring buffer** and a
**configurable overflow policy (drop-oldest or block)**. `MOCK-901` requires ≥20 000 rps with
journaling disabled — so the disabled path must be close to free, not merely cheap.

A single `[]Record` behind a `sync.Mutex` serialises every request in the process. At 5000 rps
with ~2 KB of copying under the lock that is survivable, but it collapses the moment the control
API reads the journal concurrently, and it puts a global lock on the `MOCK-901` path too (the
`enabled` check would be behind it).

## Decision

### Structure

```
Journal
 ├── enabled   atomic.Bool                 // MOCK-901 fast path
 ├── seq       atomic.Uint64               // global total order
 ├── dropped   atomic.Uint64               // metric + MOCK-902 visibility
 ├── bytes     atomic.Int64                // byte-budget accounting
 └── shards[S] where S = next_pow2(GOMAXPROCS), capped at 64
        ├── cursor atomic.Uint64           // monotonically increasing write index
        ├── ring   []slot                  // preallocated, len = capacity/S, power of two
        └── (per slot) state atomic.Uint32 // seqlock: even = stable, odd = being written
```

Write path:

```
if !j.enabled.Load() { return }            // one atomic load; branch-predicted, ~1 ns
sh   := &j.shards[shardIndex()]            // runtime_procPin-free: FNV of goroutine-local hint
i    := sh.cursor.Add(1) - 1
slot := &sh.ring[i & mask]
slot.state.Add(1)                          // -> odd
fill(slot, rec)                            // no allocation for fixed fields
slot.seq = j.seq.Add(1)
slot.state.Add(1)                          // -> even
```

There is no lock. Readers use the seqlock: read `state`, copy, re-read `state`; retry if it
changed or is odd. A slot overwritten during a read is simply retried or skipped (and counted).

Shard selection uses a cheap per-goroutine-stable hint (`runtime.NumGoroutine`-free: a
`sync.Pool`-held small struct carrying a shard index assigned on first use). This gives
contention-free writes without `runtime_procPin`, which is unavailable outside the runtime.

### Ordering

`Seq` from a single global `atomic.Uint64` gives a **total order** across shards. Query merges
shards by `Seq`. One shared atomic increment per record is the only global contention point:
`atomic.Uint64.Add` on modern x86/arm64 sustains far more than 5000 ops/s even fully contended.

### Bodies and headers

The handler already holds the request body bytes (it must parse them). Journaling therefore adds
a **copy**, not an extra read. Capture modes, per instance:

| `journal.bodies` | Behaviour |
|---|---|
| `full` (default) | Body retained verbatim. |
| `truncate: N` | First N bytes + `sha256` of the whole + original length. |
| `digest` | `sha256` + length only. |
| `off` | Neither. |

Headers are captured as an `ordered.Slice[[2]string]` preserving wire order and duplicates —
required by `MOCK-204` (mirrored-header integrity) and `MOCK-603 AssertHeaderMatchesBody`, both of
which can be defeated by `http.Header`'s canonicalisation and map ordering.

Record byte slices are **owned by the record**, not pooled. When drop-oldest overwrites a slot the
old slices become garbage. This trades GC pressure for correctness; pooling bodies while a reader
may hold them is a use-after-free class of bug that would corrupt test evidence — unacceptable in
a tool whose purpose is evidence.

### Bounds and overflow (`MOCK-902`)

Two bounds, both enforced: `maxRecords` (default 100 000, rounded to a power of two per shard) and
`maxBytes` (default 256 MiB). Whichever binds first triggers the overflow policy:

| Policy | Behaviour |
|---|---|
| `drop-oldest` (default) | Overwrite. `mcpmock_journal_dropped_total` increments. `Record.Dropped` markers let a reader see that a gap exists. |
| `block` | Writer waits on a bounded condition until a reader drains. **Bounded by `journal.blockTimeout` (default 100 ms), after which it degrades to drop-oldest and increments a distinct counter.** An unbounded block is a self-inflicted denial of service in the system under test — see GAP-017. |
| `error` (added) | Reject the request with a `503` and a journalled reason. Useful for tests that must never lose evidence. |

### Query (`MOCK-602`)

`View` is a read-only handle: `Filter(Selector) View`, `Iter(func(Record) bool)`, `Len()`,
`Snapshot() []Record`. Filters supported: method, primitive name, time range, correlation id,
instance, transport, era, HTTP status, fault rule id, `requestState` identity (`MOCK-605`).
Output: JSON array or NDJSON stream. NDJSON streaming reads shard-by-shard in `Seq` order with a
follow mode (`?follow=true`) that tails new records.

## Options considered

1. **`sync.Mutex` + slice** — rejected (contention; couples the disabled path to a lock).
2. **Buffered channel + single writer goroutine** — rejected: adds a goroutine per instance
   (breaks the `MOCK-904` "0 goroutines at rest" budget ×200), and channel send is slower than an
   atomic add under contention. Also reorders relative to response emission, which breaks
   `MOCK-212`'s "record the cancellation with elapsed time" ordering guarantees.
3. **Write-ahead to disk / mmap** — rejected: `MOCK-902` says "journaling to memory". Disk adds an
   fsync question we do not need. Export to file is offered as a *reader* feature instead.
4. **Sharded ring + seqlock (chosen).**

## Consequences

**Positive.** Journaling-off is a single atomic load. Journaling-on has no lock and no goroutine.
The total-order `Seq` makes `MOCK-605` correlation and `MOCK-604` golden files well-defined.

**Negative.** Seqlock readers can observe torn slots and must retry; a reader racing a hot writer
on a small ring may starve. Mitigated by making reads take a `Snapshot()` under a brief
`disableWrites` flag when `?consistent=true` is requested. Records are GC garbage on overwrite —
sustained 5000 rps × 2 KB = 10 MB/s of garbage. That is well within Go's comfort zone but appears
in the `MOCK-902` benchmark and must be measured, not assumed.

**Forecloses.** Journal records larger than the configured `maxBytes` budget; a single 100 MB
oversized-result fault (`MOCK-504`) must be journaled in `digest` mode or it evicts the ring.
The scenario schema therefore forbids `bodies: full` together with size faults above
`maxBytes/16`, validated at load time.
