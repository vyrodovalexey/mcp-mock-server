package journal

import (
	"context"
	"math/bits"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vyrodovalexey/mcp-mock-server/journalapi"
)

// maxShards caps the shard count regardless of GOMAXPROCS (ADR-005). More shards
// reduce cursor contention but cost preallocated slots and lengthen a merge; 64
// is well past the point of diminishing returns for the 5000 rps target.
const maxShards = 64

// minShardCapacity is the smallest per-shard ring length. A power of two, it
// keeps a small MaxRecords configuration from producing degenerate single-slot
// shards where every write evicts the previous one.
const minShardCapacity = 2

// Ring is the sharded, preallocated journal storage engine (ADR-005). It records
// [journalapi.Record] values in a total order given by a single global sequence,
// with a per-slot seqlock guarding each write against concurrent readers.
//
// Construct a Ring with [New]; the zero value is not usable. A Ring holds no
// process-global state and owns no goroutine at rest (ADR-007, architecture.md
// §7.1), so ≥200 instances each own one cheaply. See the package documentation
// for the concurrency contract, the memory bound and the overflow semantics.
type Ring struct {
	enabled  atomic.Bool
	seq      atomic.Uint64
	dropped  atomic.Uint64
	degraded atomic.Uint64
	rejected atomic.Uint64
	bytes    atomic.Int64

	shards   []*shard
	shardIdx uint64 // len(shards)-1, a mask; shard count is a power of two

	maxBytes int64
	overflow journalapi.OverflowPolicy
	blockTO  time.Duration

	// writeGate serializes the brief write-disable window a ConsistentView
	// takes. Writers take it in read mode (concurrently); a consistent read
	// takes it in write mode to exclude all writers for the copy. It is never
	// held on the hot path except as an uncontended RLock, so it does not
	// reintroduce the global lock ADR-005 rejects.
	writeGate sync.RWMutex

	// hints hands each writing goroutine a stable shard index so writes spread
	// across shards without runtime_procPin (unavailable outside the runtime).
	hints sync.Pool
}

// hint is the per-goroutine shard selector held in the pool. Its value is
// assigned once from a rotating counter and reused, giving contention-free,
// stable shard affinity for a goroutine's writes (ADR-005).
type hint struct{ shard uint64 }

// New constructs a Ring from a [journalapi.Config]. Zero-valued config fields
// take their documented defaults via [journalapi.Config.WithDefaults]. The ring
// is preallocated to the rounded record bound immediately, so first-write
// latency carries no allocation of slot storage (ADR-005 preallocation). Enabled
// mirrors cfg.Enabled; toggle it later with [Ring.SetEnabled].
func New(cfg journalapi.Config) *Ring {
	return newRing(cfg, shardCount(runtime.GOMAXPROCS(0)))
}

// newRing is the shared constructor. It takes an explicit shard count so tests
// can pin it for deterministic overflow behavior; New derives it from
// GOMAXPROCS. nShards is rounded to a power of two and capped at [maxShards].
func newRing(cfg journalapi.Config, nShards uint64) *Ring {
	cfg = cfg.WithDefaults()
	nShards = normalizeShards(nShards)
	perShard := perShardCapacity(cfg.MaxRecords, nShards)

	r := &Ring{
		shards:   make([]*shard, nShards),
		shardIdx: nShards - 1,
		maxBytes: cfg.MaxBytes,
		overflow: cfg.Overflow,
		blockTO:  cfg.BlockTimeout,
	}
	for i := range r.shards {
		r.shards[i] = newShard(perShard)
	}
	var ctr atomic.Uint64
	r.hints.New = func() any {
		return &hint{shard: ctr.Add(1) - 1}
	}
	r.enabled.Store(cfg.Enabled)
	return r
}

// shardCount returns next_pow2(gomaxprocs) capped at [maxShards], at least one.
// It works and returns in uint64 so the shard-index mask needs no int conversion.
func shardCount(gomaxprocs int) uint64 {
	if gomaxprocs < 1 {
		gomaxprocs = 1
	}
	return normalizeShards(uint64(gomaxprocs))
}

// normalizeShards rounds n up to a power of two, at least one, capped at
// [maxShards]. It is the single place shard-count bounds are enforced, shared by
// [shardCount] and the explicit-count constructor path.
func normalizeShards(n uint64) uint64 {
	if n < 1 {
		return 1
	}
	p := uint64(1) << bits.Len(uint(n-1)) // next power of two >= n
	if p < 1 {
		p = 1
	}
	if p > maxShards {
		p = maxShards
	}
	return p
}

// perShardCapacity divides the record bound across shards, rounding each shard's
// ring up to a power of two at least [minShardCapacity]. The realized total may
// exceed the requested MaxRecords by the rounding, which is safe: the bound is a
// floor on capacity, and the byte budget still enforces the memory ceiling.
func perShardCapacity(maxRecords int, nShards uint64) uint64 {
	per := uint64(1)
	if maxRecords > 0 {
		per = uint64(maxRecords) / nShards
	}
	if per < minShardCapacity {
		per = minShardCapacity
	}
	return uint64(1) << bits.Len(uint(per-1))
}

// SetEnabled turns journaling on or off. When off, [Ring.Write] returns after a
// single atomic load with no allocation and no lock (MOCK-901). It is safe to
// call concurrently with writes; a write in flight is unaffected.
func (r *Ring) SetEnabled(on bool) { r.enabled.Store(on) }

// Enabled reports whether journaling is currently on.
func (r *Ring) Enabled() bool { return r.enabled.Load() }

// Write stores rec in the journal and returns its assigned global sequence, or
// an error under the error overflow policy. When journaling is disabled it does
// nothing and returns (0, nil) after exactly one atomic load — the MOCK-901 fast
// path, which allocates nothing because rec is taken by value and never escapes.
//
// ctx bounds the block overflow policy's wait; it is never stored. Under
// drop-oldest (the default) Write never blocks and never errors. Under block it
// waits at most BlockTimeout for drained capacity, honoring ctx, then degrades
// to drop-oldest. Under error it returns [ErrOverflow] when a bound is reached.
func (r *Ring) Write(ctx context.Context, rec journalapi.Record) (uint64, error) {
	if !r.enabled.Load() {
		return 0, nil // MOCK-901: one atomic load, no allocation, no lock
	}
	return r.writeEnabled(ctx, rec)
}

// writeEnabled is the slow path taken only when journaling is on. It is a
// separate function so the disabled fast path in [Ring.Write] stays trivially
// inlineable and allocation-free.
func (r *Ring) writeEnabled(ctx context.Context, rec journalapi.Record) (uint64, error) {
	sh := r.pickShard()
	size := recordSize(rec)

	dropped, overBytes, err := r.applyOverflow(ctx, sh, size)
	if err != nil {
		return 0, err
	}
	if overBytes {
		// Free bytes before writing so the MaxBytes ceiling actually holds
		// under body-heavy load, not merely marks records dropped (MOCK-902).
		r.evictForBytes(sh, size)
	}

	r.writeGate.RLock()
	seqNum := r.seq.Add(1)
	rec.Seq = seqNum
	rec.SchemaVersion = journalapi.SchemaVersion
	rec.Dropped = dropped
	// The record is published behind an atomic pointer, so it must be heap
	// allocated and immutable once stored. This per-write allocation is on the
	// journaling-ON path only; MOCK-901's zero-allocation guarantee is the
	// disabled path in Write, which returns before reaching here. ADR-005
	// accepts this record as GC garbage on overwrite.
	stored := rec
	idx := sh.cursor.Add(1) - 1
	evicted := sh.slotAt(idx).write(&stored, size)
	r.writeGate.RUnlock()

	r.bytes.Add(size - evicted)
	return seqNum, nil
}

// evictForBytes reclaims byte budget before a write by clearing the oldest
// occupied slots in the writer's shard until the incoming record fits under
// MaxBytes or the shard is empty. Eviction is local to one shard to avoid
// cross-shard coordination on the hot path; because writes spread evenly across
// shards this keeps the global byte total bounded near MaxBytes. Each cleared
// slot's bytes are subtracted from the budget and its record becomes garbage
// (ADR-005: never pooled).
func (r *Ring) evictForBytes(sh *shard, incoming int64) {
	cur := sh.cursor.Load()
	drained := sh.drained.Load()
	for r.bytes.Load()+incoming > r.maxBytes && drained < cur {
		s := sh.slotAt(drained)
		if s.rec.Load() != nil {
			freed := s.size.Load()
			r.writeGate.RLock()
			s.reset()
			r.writeGate.RUnlock()
			r.bytes.Add(-freed)
			r.dropped.Add(1)
		}
		drained++
		sh.drained.Store(drained)
	}
}

// applyOverflow enforces the record-count and byte bounds before a write and
// applies the configured policy when a bound would be exceeded. It returns
// whether the incoming record must carry a Dropped marker, whether the byte
// bound bound (so the caller frees bytes), and an error only under the error
// policy. The count bound is the per-shard ring capacity itself; the byte bound
// is the shared budget.
func (r *Ring) applyOverflow(
	ctx context.Context, sh *shard, size int64,
) (dropped, overBytes bool, err error) {
	overCount := sh.capacityFull()
	overBytes = r.bytes.Load()+size > r.maxBytes
	if !overCount && !overBytes {
		return false, false, nil
	}
	switch r.overflow {
	case journalapi.OverflowError:
		r.rejected.Add(1)
		return false, false, ErrOverflow
	case journalapi.OverflowBlock:
		if !overCount || waitForCapacity(ctx, sh, r.blockTO) {
			// Count bound satisfied (or not the binding one); any byte overflow
			// is handled by eviction, signaled via overBytes.
			return overBytes, overBytes, nil
		}
		r.degraded.Add(1)
		r.dropped.Add(1)
		return true, overBytes, nil
	default: // OverflowDropOldest
		if overCount {
			// The ring wrap evicts one record; count it here. Byte-bound
			// evictions are counted separately in evictForBytes.
			r.dropped.Add(1)
		}
		return overCount, overBytes, nil
	}
}

// pickShard returns the shard this goroutine writes to, using a stable per-
// goroutine hint from the pool. The hint is put back immediately so a pooled
// object is reused rather than allocated per write; the modulo keeps the index
// in range even as the assigning counter grows past the shard count.
func (r *Ring) pickShard() *shard {
	h := r.hints.Get().(*hint)
	idx := h.shard & r.shardIdx
	r.hints.Put(h)
	return r.shards[idx]
}

// Drain advances every shard's drained cursor to its current write cursor,
// signaling that a reader has consumed the records written so far. It is the
// backpressure release the block overflow policy waits on: after a Drain a
// blocked writer finds capacity and proceeds. Reads themselves are non-consuming
// snapshots; Drain is the explicit "I have taken these" acknowledgement a
// draining consumer (for example an NDJSON export) issues.
func (r *Ring) Drain() {
	for _, sh := range r.shards {
		sh.drained.Store(sh.cursor.Load())
	}
}

// Dropped returns the number of records evicted by the drop-oldest policy,
// including block-policy degradations. It backs mcpmock_journal_dropped_total.
func (r *Ring) Dropped() uint64 { return r.dropped.Load() }

// Degraded returns the number of times the block policy timed out and degraded
// to drop-oldest. It backs a counter distinct from [Ring.Dropped] (MOCK-902).
func (r *Ring) Degraded() uint64 { return r.degraded.Load() }

// Rejected returns the number of writes the error policy rejected.
func (r *Ring) Rejected() uint64 { return r.rejected.Load() }

// Bytes returns the current retained-byte estimate against MaxBytes.
func (r *Ring) Bytes() int64 { return r.bytes.Load() }

// Cap returns the realized record capacity: the total number of preallocated
// slots across all shards. It may exceed the configured MaxRecords because each
// shard's ring is rounded up to a power of two (a floor on capacity, never a
// smaller ceiling). It is the count bound the drop-oldest and block policies
// enforce.
func (r *Ring) Cap() int {
	var n int
	for _, sh := range r.shards {
		n += sh.capacity()
	}
	return n
}

// Len returns the number of records currently held across all shards. It is a
// best-effort count taken without stopping writers, so it may momentarily race a
// concurrent write; it is exact when writes are quiescent.
func (r *Ring) Len() int {
	var n int
	for _, sh := range r.shards {
		n += sh.heldCount()
	}
	return n
}

// Snapshot returns every currently-held record in global [journalapi.Record.Seq]
// order. Each record is a value copy read under the seqlock, so a concurrent
// overwrite cannot tear it; a slot the reader could not read consistently after
// the bounded retry is skipped. Snapshot takes no write-disable, so it never
// blocks a writer (compare [Ring.ConsistentView]).
func (r *Ring) Snapshot() []journalapi.Record {
	return r.collect()
}

// ConsistentView returns a [journalapi.View] over a snapshot taken with writes
// briefly disabled, so no writer can intervene mid-copy and no slot is skipped
// (ADR-005 Consequences: the ?consistent=true read). The write-disable is held
// only for the duration of the copy, keeping the hot path lock-free in the
// common case.
func (r *Ring) ConsistentView() journalapi.View {
	r.writeGate.Lock()
	records := r.collect()
	r.writeGate.Unlock()
	return journalapi.NewView(records)
}

// View returns a [journalapi.View] over a lock-free snapshot of the journal in
// Seq order. Unlike [Ring.ConsistentView] it does not disable writes, so under a
// hot writer a torn slot may be skipped; that is the intended trade for never
// blocking a writer.
func (r *Ring) View() journalapi.View {
	return journalapi.NewView(r.collect())
}

// Iter calls fn for each held record in Seq order, stopping early if fn returns
// false. It ranges over a snapshot, so fn may run arbitrarily long without
// holding any lock or blocking writers.
func (r *Ring) Iter(fn func(journalapi.Record) bool) {
	for _, rec := range r.collect() {
		if !fn(rec) {
			return
		}
	}
}

// collect gathers every readable record across all shards and sorts it into
// global Seq order. It reads each occupied slot under the seqlock and skips any
// it cannot read consistently after the bounded retry, so the result is always
// composed of whole, untorn records.
func (r *Ring) collect() []journalapi.Record {
	out := make([]journalapi.Record, 0, r.Len())
	for _, sh := range r.shards {
		for i := range sh.ring {
			if rec, ok := sh.ring[i].read(); ok {
				out = append(out, *rec)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out
}

// Clear empties the journal: it resets every shard's cursors and slots and zeroes
// the byte budget, without disturbing the sequence counter (so Seq stays globally
// monotone across a clear, per MOCK-602.6/702.7). It briefly disables writes so a
// concurrent writer cannot observe a half-cleared ring.
func (r *Ring) Clear() {
	r.writeGate.Lock()
	defer r.writeGate.Unlock()
	for _, sh := range r.shards {
		for i := range sh.ring {
			sh.ring[i].reset()
		}
		sh.cursor.Store(0)
		sh.drained.Store(0)
	}
	r.bytes.Store(0)
}
