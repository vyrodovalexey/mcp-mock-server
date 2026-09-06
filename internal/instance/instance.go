package instance

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/vyrodovalexey/mcp-mock-server/internal/catalogue"
	"github.com/vyrodovalexey/mcp-mock-server/internal/determinism"
	"github.com/vyrodovalexey/mcp-mock-server/internal/engine"
	"github.com/vyrodovalexey/mcp-mock-server/internal/journal"
	"github.com/vyrodovalexey/mcp-mock-server/journalapi"
	"github.com/vyrodovalexey/mcp-mock-server/scenario"
)

// Metrics is the pre-resolved per-instance metric handle set an instance records
// through (ADR-007 §4). It is the intersection of what engine.Metrics needs and
// what obs.InstanceMetrics provides, named here so this package need not import
// internal/obs: an *obs.InstanceMetrics satisfies it, and it in turn satisfies
// engine.Metrics, so the pipeline records through the same handles. It is
// optional — an instance may be constructed with nil metrics — so every method
// is guarded by the caller.
type Metrics = engine.Metrics

// Instance is one logical MCP server: the tenancy boundary of ADR-007. It owns
// its immutable configuration behind an atomic.Pointer[Snapshot] (ADR-014), its
// journal ring and capturer, its determinism seed subtree, its credential hasher
// and its pre-resolved metric handles. It owns NO goroutine at rest
// (architecture.md §7.1) — that invariant is what makes ≥200 instances cheap
// (MOCK-904) and is asserted by TestZeroGoroutinesAtRest.
//
// An *Instance satisfies engine.Instance (the consumer-defined interface the
// pipeline reads through), so the engine dispatches a request against an
// instance without importing this package. Construct one with [New]; the zero
// value is not usable.
//
// Concurrency: [Instance.LoadSnapshot] and the read accessors are lock-free and
// safe for unbounded concurrent callers (the 20 000 rps read path, MOCK-901).
// [Instance.Mutate] and [Instance.ClearJournal] serialize on the per-instance
// writer mutex, which never blocks a reader.
type Instance struct {
	// name is the instance name; it participates in seed derivation and is
	// recorded on every journal record. Immutable after construction.
	name string
	// mountPath is the path prefix this instance is served under (ADR-007 §3),
	// used as the registry key. Immutable after construction.
	mountPath string
	// id is the instance's stable index within a Server (ADR-007 §2:
	// "index == id"). Immutable after construction.
	id int

	// snapshot is the current immutable configuration (ADR-014). The read path
	// is Load(); a mutation Store()s a fresh clone under mu.
	snapshot atomic.Pointer[Snapshot]
	// mu serializes writers ONLY (ADR-014). Readers never take it. It guards the
	// clone-mutate-store sequence so two concurrent mutations do not lose an
	// update, but it is off the request hot path entirely.
	mu sync.Mutex

	// ring and capturer are the journal storage and capture halves (ADR-005).
	// Both are non-nil for an instance with a journal; both may be nil for an
	// instance constructed without one, in which case stage 9 is a no-op.
	ring     *journal.Ring
	capturer *journal.Capturer

	// key is the instance seed subtree, root.Derive(DomainInstance, name) per
	// ADR-002. Immutable value; safe to share and to copy.
	key determinism.Key
	// epoch is the scenario epoch the virtual clock is anchored to (ADR-002
	// rule 3). Immutable after construction.
	epoch time.Time

	// metrics are the pre-resolved metric handles (ADR-007 §4), or nil when the
	// instance was constructed without metrics. Immutable after construction.
	metrics Metrics

	// start is the instance-start reference for the monotonic timestamp on
	// journal records (MOCK-601.1 MonoNs). Immutable after construction.
	start time.Time
}

// Compile-time assertion that an *Instance satisfies the engine's consumer-side
// Instance interface, so the pipeline can dispatch against it.
var _ engine.Instance = (*Instance)(nil)

// Config configures a [New] instance. Root is the process seed-tree root
// (determinism.Root(seed)); the instance derives its own subtree from it, so two
// instances with different names get independent derived keys from the same root
// (MOCK-103.5). Metrics is optional. Epoch defaults to the zero time when unset,
// which the deterministic clock treats as a valid anchor.
type Config struct {
	// Name is the instance name (required). It seeds the instance subtree and is
	// recorded on every journal record.
	Name string
	// MountPath is the registry key / path prefix. When empty it defaults to
	// Name so an instance is always registrable.
	MountPath string
	// ID is the instance's stable index within a Server.
	ID int
	// Root is the process determinism root (determinism.Root(seed)). The
	// instance derives key = Root.Derive(DomainInstance, name).
	Root determinism.Key
	// Spec is the validated scenario instance spec the snapshot is built from.
	Spec scenario.InstanceSpec
	// Epoch anchors the deterministic virtual clock (ADR-002 rule 3).
	Epoch time.Time
	// Metrics are the pre-resolved per-instance metric handles, or nil.
	Metrics Metrics
	// MetaValidator is the stage-4 _meta validator injected by the composition
	// root (the mcpmock facade), built from switches.validateMeta (MOCK-203,
	// AMEND-6). It is injected rather than constructed here so this package need
	// not import internal/modern and so the metric recorder (a concrete
	// *obs.InstanceMetrics, which is not part of the narrower engine.Metrics
	// surface this package holds) can be bound at the facade where it exists.
	// Nil is legal and means "accept all _meta": the snapshot falls back to
	// engine.AcceptAllMeta, which is what a directly-constructed test instance
	// gets. Immutable after construction; the validator is itself stateless and
	// safe for concurrent use (TASK-017).
	MetaValidator engine.MetaValidator
}

// New constructs an instance from cfg. It derives the instance seed subtree,
// builds the immutable generation-0 snapshot from the validated scenario spec,
// constructs the journal ring and capturer from the scenario's journal config,
// and publishes the snapshot. It starts NO goroutine (architecture.md §7.1),
// performs no I/O, and allocates a bounded amount independent of catalog size
// (ADR-004), so it stays inside the MOCK-107 startup budget and 200 of it stay
// cheap (MOCK-904).
//
// A nil-journal instance results when the scenario disables the journal
// (journal.enabled: false): ring and capturer are left nil and stage 9 becomes a
// no-op. Otherwise the ring is preallocated to its configured bound and enabled
// to match the config.
func New(cfg Config) *Instance {
	key := cfg.Root.Derive(determinism.DomainInstance, []byte(cfg.Name))
	mount := cfg.MountPath
	if mount == "" {
		mount = cfg.Name
	}
	cat := catalog.New(key, cfg.Spec.Catalog)

	inst := &Instance{
		name:      cfg.Name,
		mountPath: mount,
		id:        cfg.ID,
		key:       key,
		epoch:     cfg.Epoch,
		metrics:   cfg.Metrics,
		start:     time.Now(),
	}

	snap := buildSnapshot(cfg.Spec, cat, cfg.MetaValidator)
	if snap.journalEnabled {
		jcfg := journalConfig(cfg.Spec.Journal)
		inst.ring = journal.New(jcfg)
		// The credential-hash key is per-process crypto/rand, NEVER seed-derived
		// (ADR-002 named exception; security.md §3/§5; AMEND-8; REV-004): the seed
		// is public, so a seed-derived key would make a journal export a
		// dictionary-attack target for captured credentials.
		inst.capturer = journal.NewCapturer(jcfg, journal.NewCredHasher())
	}
	inst.snapshot.Store(snap)
	return inst
}

// Name returns the instance name.
func (i *Instance) Name() string { return i.name }

// MountPath returns the path prefix this instance is served under (its registry
// key).
func (i *Instance) MountPath() string { return i.mountPath }

// ID returns the instance's stable index within a Server.
func (i *Instance) ID() int { return i.id }

// LoadSnapshot returns the current immutable configuration snapshot with a
// single atomic pointer load and NO lock (ADR-014, MOCK-901). The pipeline calls
// it exactly once, at stage 1, and carries the result on the Exchange so a
// mid-request mutation cannot be observed. The returned pointer stays valid and
// consistent for as long as the caller holds it, regardless of concurrent
// mutations.
func (i *Instance) LoadSnapshot() engine.Snapshot {
	return i.snapshot.Load()
}

// Snapshot is the concrete-typed variant of [Instance.LoadSnapshot], for callers
// within this package and tests that need *Snapshot rather than the engine
// interface. It is the same single atomic load.
func (i *Instance) Snapshot() *Snapshot {
	return i.snapshot.Load()
}

// InstanceKey returns the instance seed subtree (ADR-002). Stage 1 derives the
// per-request key from it. The returned value is an immutable copy.
func (i *Instance) InstanceKey() determinism.Key { return i.key }

// Journal returns the storage ring and the capturer used at stage 9. Both are
// nil when the instance has no journal, in which case stage 9 is a no-op.
func (i *Instance) Journal() (*journal.Ring, *journal.Capturer) {
	return i.ring, i.capturer
}

// Metrics returns the pre-resolved per-instance metric handles, or nil when
// metrics are not configured. The pipeline records through them without ever
// calling WithLabelValues (ADR-007 §4).
func (i *Instance) Metrics() engine.Metrics {
	if i.metrics == nil {
		return nil
	}
	return i.metrics
}

// Epoch returns the scenario epoch the virtual clock is anchored to (ADR-002
// rule 3).
func (i *Instance) Epoch() time.Time { return i.epoch }

// Start returns the instance-start reference for the monotonic journal timestamp
// (MOCK-601.1 MonoNs). It satisfies the engine.Instance.Start seam: stage 9's
// buildInput records time.Since(Start()) on every record (REV-002), so this is a
// live accessor on the journal path, not a spare.
func (i *Instance) Start() time.Time { return i.start }

// Mutate applies fn to a fresh clone of the current snapshot, bumps the
// generation, publishes the result atomically, and returns the new generation
// (ADR-014 write path). It is the single entry point for a runtime configuration
// change (the control API, MOCK-702, drives it in a later phase). fn receives a
// mutable clone it may edit freely — replacing any slice or map it changes with
// a fresh one rather than editing in place — but MUST NOT retain the pointer
// after it returns, because the published snapshot is thereafter immutable.
//
// Mutate serializes writers on the per-instance mutex so two concurrent
// mutations both take effect (each sees the other's Gen), and it never blocks a
// reader: a reader concurrently holding the old snapshot keeps observing the
// old, consistent values (TestSnapshotImmutability). A publish is a single
// atomic pointer store, so a reader loading during a mutation gets either the
// whole old snapshot or the whole new one, never a half-applied mix.
func (i *Instance) Mutate(fn func(*Snapshot)) uint64 {
	i.mu.Lock()
	defer i.mu.Unlock()
	old := i.snapshot.Load()
	next := old.clone()
	fn(next)
	next.gen = old.gen + 1
	i.snapshot.Store(next)
	if i.metrics != nil {
		i.metrics.SetSnapshotGeneration(next.gen)
	}
	return next.gen
}

// ClearJournal empties the instance's journal without disturbing in-flight
// requests (MOCK-702.7). It is a no-op when the instance has no journal. The
// clear briefly disables writes inside the ring so a concurrent writer cannot
// observe a half-cleared ring; the global sequence counter is preserved so Seq
// stays monotone across a clear (MOCK-602.6). Clearing is a runtime mutation of
// per-entity state (the journal is deliberately NOT in the snapshot, ADR-014),
// so it does not bump the snapshot generation.
func (i *Instance) ClearJournal() {
	if i.ring == nil {
		return
	}
	i.ring.Clear()
}

// journalConfig maps the scenario journal block to a journalapi.Config for the
// ring and the capturer, applying the absent-vs-set pointer idiom: a nil field
// leaves the corresponding Config field zero so journalapi.Config.WithDefaults
// supplies the documented default. It is the single place scenario journal
// config becomes storage config.
func journalConfig(j *scenario.Journal) journalapi.Config {
	cfg := journalapi.Config{Enabled: true}
	if j == nil {
		return cfg
	}
	if j.Enabled != nil {
		cfg.Enabled = *j.Enabled
	}
	cfg.Mode = captureMode(j.Bodies)
	if j.TruncateBytes != nil {
		cfg.TruncateBytes = *j.TruncateBytes
	}
	if j.MaxRecords != nil {
		cfg.MaxRecords = *j.MaxRecords
	}
	if j.MaxBytes != nil {
		cfg.MaxBytes = int64(*j.MaxBytes)
	}
	cfg.Overflow = overflowPolicy(j.Overflow)
	if j.BlockTimeoutMs != nil {
		cfg.BlockTimeout = time.Duration(*j.BlockTimeoutMs) * time.Millisecond
	}
	return cfg
}

// captureMode maps the scenario bodies string to a journalapi.CaptureMode,
// defaulting an absent value to the empty mode so WithDefaults picks CaptureFull.
func captureMode(bodies *string) journalapi.CaptureMode {
	if bodies == nil {
		return ""
	}
	return journalapi.CaptureMode(*bodies)
}

// overflowPolicy maps the scenario overflow string to a journalapi.OverflowPolicy,
// defaulting an absent value to the empty policy so WithDefaults picks
// OverflowDropOldest.
func overflowPolicy(overflow *string) journalapi.OverflowPolicy {
	if overflow == nil {
		return ""
	}
	return journalapi.OverflowPolicy(*overflow)
}
