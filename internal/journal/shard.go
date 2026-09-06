package journal

import (
	"sync/atomic"

	"github.com/vyrodovalexey/mcp-mock-server/journalapi"
)

// slot is one preallocated cell of a shard ring. It publishes a record through a
// seqlock built on two atomics, so a reader never observes a torn record and the
// implementation is race-clean under the Go race detector (ADR-005).
//
// The seqlock has two parts working together:
//
//   - state (atomic.Uint32) is the classic seqlock version counter: even when
//     the slot holds a stable record, odd while a writer is publishing. A reader
//     that sees an odd state, or a state that changed across its read, knows a
//     writer intervened and retries or skips-and-counts.
//   - rec (atomic.Pointer[journalapi.Record]) carries the payload itself. It is
//
// published with a single atomic store and read with a single atomic load,
// so the record bytes are transferred whole under the Go memory model — no
// word-tearing is even representable, which is what keeps the design correct
// under -race where a hand-rolled multi-word memcpy seqlock would be a
// reported data race.
//
// The published *Record is immutable once stored: a writer allocates a fresh one
// each time rather than mutating in place, so a reader holding the pointer keeps
// a stable, whole record even as the slot is overwritten. Records are never
// pooled or reused (ADR-005): the previous *Record simply becomes garbage. size
// is the byte weight the current record contributed to the budget; it is atomic
// because an eviction on one goroutine may touch a slot that a writer on another
// wraps onto, so both accesses must be race-free by construction.
type slot struct {
	state atomic.Uint32
	rec   atomic.Pointer[journalapi.Record]
	size  atomic.Int64
}

// write publishes rec into the slot under the seqlock. It advances state to odd,
// atomically stores the freshly-allocated record pointer, then advances state
// back to even. A concurrent reader sees either the pre-write stable state, the
// post-write stable state, or an odd state it must retry — never a torn record,
// because the payload transfer is a single atomic pointer store. It returns the
// byte weight of the record it overwrote (zero if the slot was empty) so the
// caller can adjust the byte budget. write is invoked under the ring's write
// gate held for reading, so two writers never target the same slot concurrently
// (distinct cursor indices) and size is safe to mutate here.
func (s *slot) write(rec *journalapi.Record, size int64) (evicted int64) {
	if s.rec.Load() != nil {
		evicted = s.size.Load()
	}
	s.state.Add(1) // even -> odd: publishing
	s.size.Store(size)
	s.rec.Store(rec)
	s.state.Add(1) // odd -> even: stable
	return evicted
}

// reset returns the slot to its empty state, releasing the retained record to
// the garbage collector (ADR-005: never pooled) and advancing state so any
// reader mid-read observes a changed state and skips. Callers hold the write
// gate exclusively, so no writer races this reset.
func (s *slot) reset() {
	s.state.Add(1) // even -> odd: mutating
	s.size.Store(0)
	s.rec.Store(nil)
	s.state.Add(1) // odd -> even: stable (empty)
}

// maxReadRetries bounds how many times a reader re-attempts a slot whose writer
// kept intervening before it gives up and skips it. A bounded retry guarantees a
// reader cannot be starved by a hot writer on a small ring (ADR-005
// Consequences); the skipped slot is counted by the caller so the loss is
// visible, never silent.
const maxReadRetries = 8

// read returns the slot's current record using the seqlock. It reports ok=false
// when the slot is empty, or when a writer kept intervening past maxReadRetries
// so the read was abandoned (to be skipped-and-counted by the caller). The
// returned *Record is immutable and safe to dereference indefinitely; the ring
// may overwrite this slot immediately afterward without affecting it.
func (s *slot) read() (rec *journalapi.Record, ok bool) {
	for range maxReadRetries {
		before := s.state.Load()
		if before&1 != 0 {
			continue // a writer is publishing; retry
		}
		candidate := s.rec.Load()
		after := s.state.Load()
		if before != after {
			continue // a writer intervened during the load; retry
		}
		if candidate == nil {
			return nil, false // stably empty
		}
		return candidate, true
	}
	return nil, false
}

// shard is one lane of the sharded ring. Writers hash to a shard to avoid
// contending on a single cursor; a global sequence still totally orders records
// across shards (ADR-005). cursor is the monotonically increasing write index;
// drained is the highest index a reader has consumed via [Ring.Drain], used only
// by the block overflow policy as backpressure. mask is len(ring)-1, so the ring
// length is always a power of two and index &= mask is a cheap wrap.
type shard struct {
	cursor  atomic.Uint64
	drained atomic.Uint64
	ring    []slot
	mask    uint64
}

// newShard preallocates a shard ring of the given power-of-two capacity. The
// slots are zero-valued and unoccupied until first written, so preallocation
// costs the slot headers but retains no record bytes (ADR-005 preallocation).
func newShard(capacity uint64) *shard {
	return &shard{
		ring: make([]slot, capacity),
		mask: capacity - 1,
	}
}

// slotAt returns the slot for write index i, wrapping within the ring.
func (sh *shard) slotAt(i uint64) *slot {
	return &sh.ring[i&sh.mask]
}

// capacity returns the number of preallocated slots in the shard ring.
func (sh *shard) capacity() int { return len(sh.ring) }

// pending reports how many records this shard holds that a reader has not yet
// drained. It is the block policy's backpressure signal: when pending reaches
// the ring capacity a further write would overwrite an undrained record. cursor
// only ever advances, and drained never passes cursor, so the difference is a
// safe, monotone-bounded gauge.
func (sh *shard) pending() uint64 {
	cur := sh.cursor.Load()
	drained := sh.drained.Load()
	if cur < drained {
		return 0
	}
	return cur - drained
}

// capacityFull reports whether the next write to this shard would overwrite a
// record the reader has not drained. It is true exactly when the shard is
// holding a full ring of undrained records.
func (sh *shard) capacityFull() bool {
	return sh.pending() >= uint64(len(sh.ring))
}

// heldCount returns how many records the shard currently holds, capped to the
// ring capacity and returned as an int (always in range because it never
// exceeds len(sh.ring)). It is the count [Ring.Len] sums.
func (sh *shard) heldCount() int {
	p := sh.pending()
	if n := len(sh.ring); p >= uint64(n) {
		return n
	}
	return int(p) //nolint:gosec // p < len(sh.ring), an int, so this never overflows
}
