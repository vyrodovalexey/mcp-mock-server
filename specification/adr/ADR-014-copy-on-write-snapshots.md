---
id: ADR-014
title: Runtime mutation via immutable copy-on-write snapshots behind atomic.Pointer
status: accepted
date: 2026-09-04
reversibility: MEDIUM — internal, but every handler assumes snapshot immutability
requirements: MOCK-702, MOCK-227, MOCK-233, MOCK-901, §0.2
---

# ADR-014 — Copy-on-write configuration snapshots

## Context

§0.2 ("programmable, not scripted-once") and `MOCK-702` require mutation **at runtime, without
restart**, of: the catalogue, notifications, fault injectors, protocol era, credentials, streams
and the journal. `MOCK-227` (catalogue drift at a controlled moment) and `MOCK-233` (catalogue
changes silently within the advertised TTL) are timing-sensitive mutations.

Meanwhile `MOCK-901` demands 20 000 rps, so the read path cannot take a lock, and correctness
demands that a request never observe a **half-applied** mutation. If a control call swaps the era
and the fault list, a request must see either both or neither — otherwise the test that observes
the resulting response cannot attribute it.

## Decision

Each instance holds `snapshot atomic.Pointer[Snapshot]`. `Snapshot` is **deeply immutable** after
publication:

```go
type Snapshot struct {
    Gen               uint64          // monotonically increasing, journaled per request
    Era               era.Mode
    Switches          Switches        // per-method enable/disable (MOCK-202), omissions (MOCK-209)
    Catalogue         catalogue.Params
    Overlay           []catalogue.Item        // sorted, never mutated
    Drift             []catalogue.Patch       // ordered, never mutated
    CatalogueGen      uint64
    Paging            paging.Params
    MRTR              mrtr.Params
    Subscriptions     subs.Params
    Auth              authz.Config
    Faults            *fault.Set              // compiled selectors + bloom
    Journal           journal.Params
    Precomputed       *hotBytes               // ADR-012 hot-path fragments
}
```

Write path (control API, in-process `Control`, or a timer-wheel drift event):

```go
inst.mu.Lock()
old := inst.snapshot.Load()
next := old.Clone()          // shallow struct copy; slices replaced, never appended in place
mutate(next)
next.Gen = old.Gen + 1
next.Precomputed = buildHotBytes(next)      // recompute derived caches here, once
inst.snapshot.Store(next)
inst.mu.Unlock()
```

Read path: `snap := inst.snapshot.Load()` — **once**, at pipeline stage 1 — and that pointer is
carried on `Exchange` through every stage and every handler. One atomic load per request.

Rules:

1. **A handler never calls `snapshot.Load()`.** It uses `ex.Snapshot`. Enforced by making the
   field unexported on `Instance` with only an internal accessor used by the pipeline entry.
2. **Nothing in a `Snapshot` is mutated after `Store`.** Slices are copy-on-write; `fault.Set`
   counters live *outside* the snapshot (on the `Instance`, keyed by rule id) precisely because
   they are mutable.
3. **Derived caches are rebuilt in the writer**, not lazily by readers. `Precomputed`, compiled
   glob selectors, the fault method bloom, and the scope-filtered catalogue index memo are all
   built under `mu`. This keeps readers allocation-free and removes double-checked-locking
   entirely.
4. `Gen` is recorded on every journal record. A test that mutates config mid-run can partition its
   journal by `Gen` and know exactly which requests saw which configuration — this turns a race
   into an observable fact, which is worth more than the mechanism itself.

State that is **not** in the snapshot because it is inherently mutable and per-entity: sessions,
subscriptions, cursors, MRTR rounds, replay LRU, fault counters, journal. Each has its own
sharded structure (`architecture.md §7.2`).

`MOCK-233` ("catalogue silently changes within the advertised TTL") is a drift patch scheduled on
the timer wheel that bumps `CatalogueGen` **without** changing the `ttlMs` previously emitted —
which is exactly the bug class the hub must detect.

## Options considered

1. **`sync.RWMutex` around a mutable config** — rejected: `RWMutex.RLock` is not free under high
   core counts (cache-line contention on the reader count), and it does not by itself give
   atomicity across a multi-field mutation without holding the write lock across the whole
   handler.
2. **`sync.Map` per config key** — rejected: no cross-key atomicity; a mutation of era + faults
   would be observable half-applied.
3. **Event-sourced config with a version vector** — rejected: over-engineered for a single-writer,
   low-frequency mutation pattern.
4. **Immutable snapshot + `atomic.Pointer` (chosen).** This is the standard Go answer for
   read-mostly configuration and it composes perfectly with `MOCK-901`.

## Consequences

**Positive.** Zero read contention. Multi-field mutations are atomic by construction. `Gen`
turns "did this request see the new config?" from a guess into a journal field. Drift
(`MOCK-227`) and silent drift (`MOCK-233`) need no cache invalidation because ADR-004's catalogue
is a pure function of the snapshot.

**Negative.** Every mutation allocates a new `Snapshot` and rebuilds derived caches. For a
5000-tool catalogue the rebuilt caches are small (ADR-004 keeps items virtual), but the compiled
fault selector set is rebuilt on every fault toggle — O(#rules), negligible. A control API driven
in a tight loop (thousands of mutations/second) would churn; the control API is rate-limited to a
configurable default of 100 mutations/second, returning `429` above it.

Snapshot clones are shallow. A future contributor appending to `next.Overlay` in place would
corrupt a live snapshot. Mitigated by: `Overlay` typed as a distinct `immutableItems` type whose
only mutator returns a new value; and a `TestSnapshotImmutability` that holds an old snapshot
across a mutation and asserts it is unchanged.

**Forecloses.** Per-request configuration overrides that mutate the instance (they must instead be
expressed as selector-scoped rules, which is what §5 already does).
