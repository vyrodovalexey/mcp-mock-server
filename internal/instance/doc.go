// Package instance implements the tenancy boundary of mcpmock: a single logical
// MCP server (an [Instance]), the immutable copy-on-write configuration
// [Snapshot] it serves requests from, and the atomic [Registry] that maps a
// mount path to an instance so one process hosts ≥200 independent servers
// (MOCK-103, MOCK-904). It is the concrete side of the consumer-defined
// interfaces internal/engine names — an *Instance satisfies engine.Instance and
// a *Snapshot satisfies engine.Snapshot — which is why the dependency arrow
// points instance → engine and never the reverse (architecture.md §6.1).
//
// # The isolation guarantee (ADR-007)
//
// An instance IS the tenant. Two instances share exactly three things and
// nothing else: the process listeners, the timer wheel, and the observability
// bundle — all read-mostly and passed in explicitly, never reached for. Every
// piece of per-tenant state — the snapshot, the journal ring, the seed subtree,
// the credential hasher and the pre-resolved metric handles — lives on the
// [Instance] itself. There is no package-level mutable variable anywhere in this
// package (ADR-007's "no globals" rule), so two instances with different
// scenarios cannot interfere through config, catalog, journal or metrics, and
// two mcpmock.Servers coexist in one test binary. TestInstanceIsolation proves
// the four axes of non-interference under -race.
//
// Because tool names are scoped to an instance by construction, two instances
// may serve a tool with the same name without collision (MOCK-223); a
// deliberate collision is expressed as shared *configuration*, never shared
// state.
//
// # The copy-on-write contract (ADR-014)
//
// An instance holds its configuration behind an atomic.Pointer[Snapshot]. A
// [Snapshot] is DEEPLY IMMUTABLE after publication: no field of a published
// snapshot is ever mutated. The read path is a single atomic pointer load and
// takes no lock — mandatory at 20 000 rps (MOCK-901). The pipeline loads the
// pointer exactly once, at stage 1, and carries it on the Exchange through every
// stage, so a mutation that lands mid-request cannot produce a half-old /
// half-new response.
//
// A write clones, edits the clone, bumps Gen, and swaps the pointer under a
// per-instance writer mutex that serializes writers only (never readers):
//
//	inst.mu.Lock()
//	old := inst.snapshot.Load()
//	next := old.clone()   // struct copy; slices/maps replaced, never mutated in place
//	mutate(next)
//	next.gen = old.gen + 1
//	inst.snapshot.Store(next)
//	inst.mu.Unlock()
//
// A reader holding a *Snapshot obtained before a mutation observes the
// pre-mutation values indefinitely; the writer allocates a new snapshot rather
// than editing the live one. TestSnapshotImmutability and TestCOWNoTornRead
// prove that a reader mid-iteration never observes a half-applied mutation,
// under -race.
//
// State that is inherently mutable and per-entity — the journal ring, and in
// later phases sessions, subscriptions, cursors and fault counters — is NOT in
// the snapshot; it lives beside it on the instance with its own concurrency
// discipline, precisely because a snapshot must never be mutated after Store.
//
// # The zero-goroutines-at-rest invariant (architecture.md §7.1)
//
// An instance owns NO goroutine. Nothing here starts a background goroutine at
// construction, on mutation, or at rest. This is the property that makes 200
// instances cheap: 200 × (a snapshot + a journal ring + metric handles), with no
// 200 idle goroutines and no 200 tickers. Anything periodic a later phase needs
// is a timer-wheel entry, owned by the process, not the instance. Construction
// is allocation-bounded and does no I/O, keeping instance construction inside
// the MOCK-107 startup budget; BenchmarkInstanceMemory200 measures per-instance
// memory at 200 instances (MOCK-904).
//
// # Concurrency model
//
//   - [Instance.LoadSnapshot] and every read accessor: lock-free, safe for
//     unbounded concurrent callers.
//   - [Instance.Mutate], [Instance.ClearJournal]: serialized on the per-instance
//     writer mutex; safe to call concurrently with readers and with each other.
//   - [Registry] lookup: lock-free (an atomic.Pointer[routeTable] load);
//     [Registry.Add] and [Registry.Remove] serialize on the registry's writer
//     mutex and publish a new immutable table, so adding or removing an instance
//     never disturbs an in-flight lookup on another instance.
//
// # Out of scope (later phases / other tasks)
//
// The public facade (TASK-022), transports (TASK-019/020), the control API that
// drives [Instance.Mutate] over the wire (TASK-023), the CLI (TASK-024) and the
// stage-4 _meta validator (TASK-017) are not here. Fleets of instances
// (MOCK-103 multi-instance, Phase 4) build on this registry without changing it;
// Phase 1 constructs one instance but the registry is built for N so retrofitting
// it would not touch every call site.
package instance
