package journal_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vyrodovalexey/mcp-mock-server/internal/journal"
	"github.com/vyrodovalexey/mcp-mock-server/journalapi"
)

// mkRecord builds an internally-consistent record for a given marker n. Every
// field derived from n carries the same value, so a torn record that mixed two
// writes' fields would fail the invariant check in assertConsistent.
func mkRecord(n int) journalapi.Record {
	return journalapi.Record{
		Instance:   "inst",
		MonoNs:     int64(n),
		DurationNs: int64(n),
		Transport:  journalapi.TransportHTTP,
		JSONRPC: journalapi.JSONRPCPart{
			Method:     "tools/call",
			Name:       fmt.Sprintf("tool-%d", n),
			Body:       []byte(fmt.Sprintf("body-%d", n)),
			BodyLength: n,
		},
	}
}

// assertConsistent verifies a record's cross-field invariant: MonoNs, DurationNs
// and BodyLength all equal, and the body names the same marker. A seqlock defect
// that returned a torn record would break at least one of these.
func assertConsistent(t *testing.T, r journalapi.Record) {
	t.Helper()
	if r.MonoNs != r.DurationNs || int(r.MonoNs) != r.JSONRPC.BodyLength {
		t.Fatalf("torn record: MonoNs=%d DurationNs=%d BodyLength=%d",
			r.MonoNs, r.DurationNs, r.JSONRPC.BodyLength)
	}
	want := fmt.Sprintf("body-%d", r.MonoNs)
	if string(r.JSONRPC.Body) != want {
		t.Fatalf("torn record: body=%q want %q", r.JSONRPC.Body, want)
	}
}

func enabledConfig(maxRecords int) journalapi.Config {
	return journalapi.Config{Enabled: true, MaxRecords: maxRecords}
}

// testMaxShards mirrors the production shard-count cap (ring.go: maxShards).
// The black-box test package cannot see the unexported constant, so the value
// is restated here; the internal TestShardCountRounding pins the real cap.
const testMaxShards = 64

// shardSafeMaxRecords returns a MaxRecords large enough that a ring holds all n
// records regardless of how they distribute across shards, on any host
// regardless of GOMAXPROCS.
//
// The journal ring is sharded per ADR-005 into next_pow2(GOMAXPROCS) shards,
// capped at maxShards (64); the binding capacity is the per-shard ring
// (MaxRecords / shardCount), not MaxRecords itself. Two ways records concentrate
// on one shard: a single-goroutine writer's stable pool hint lands every write
// on one shard (DEF-002/DEF-003), and concurrent writers can, in the worst case,
// still pile onto a single shard (DEF-004). Both share the same worst case — all
// n records on one shard — so both share this bound. Sizing MaxRecords for the
// total instead silently loses records to drop-oldest once GOMAXPROCS is high
// enough: deterministic given GOMAXPROCS, so it reads as flakiness across
// environments. Sizing for n*maxShards makes even the worst-case single-shard
// concentration hold all n records: at 64 shards perShard = n*64/64 = n, rounded
// up to a power of two, which is ≥ n; at fewer shards perShard only grows. Named
// so future tests reach for it instead of guessing a literal.
func shardSafeMaxRecords(n int) int {
	return n * testMaxShards
}

// TestWriteAssignsSeqAndStores covers the basic accepted-write path: a record is
// stored, gets a global Seq, and is readable back with its fields intact.
func TestWriteAssignsSeqAndStores(t *testing.T) {
	t.Parallel()
	r := journal.New(enabledConfig(1024))
	seq, err := r.Write(context.Background(), mkRecord(1))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if seq != 1 {
		t.Fatalf("first Seq = %d, want 1", seq)
	}
	got := r.Snapshot()
	if len(got) != 1 {
		t.Fatalf("Snapshot len = %d, want 1", len(got))
	}
	assertConsistent(t, got[0])
	if got[0].SchemaVersion != journalapi.SchemaVersion {
		t.Fatalf("SchemaVersion = %d, want %d", got[0].SchemaVersion, journalapi.SchemaVersion)
	}
}

// TestSeqOrderUniqueGapless proves records come back in Seq order and that Seq
// values are unique and gapless for accepted writes (AC-3, MOCK-602.7).
//
// MaxRecords is sized so that all n writes are accepted rather than partly
// evicted by drop-oldest. A single goroutine's writes all land on one shard via
// its stable pool hint (see pickShard), so the binding bound is the per-shard
// capacity MaxRecords/shardCount, not MaxRecords itself; shardCount is
// next_pow2(GOMAXPROCS) capped at maxShards (64). 1<<16 keeps that per-shard
// capacity (≥ 1024 even at 64 shards) well above n, so the assertion holds on
// any host regardless of GOMAXPROCS. The drop-oldest overflow contract this
// test would otherwise trip is covered deterministically by the internal
// TestDropOldest/TestMaxRecordsBoundHolds tests, which pin the shard count.
func TestSeqOrderUniqueGapless(t *testing.T) {
	t.Parallel()
	const n = 500
	r := journal.New(enabledConfig(1 << 16))
	for i := range n {
		if _, err := r.Write(context.Background(), mkRecord(i)); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}
	got := r.Snapshot()
	if len(got) != n {
		t.Fatalf("len = %d, want %d", len(got), n)
	}
	for i, rec := range got {
		wantSeq := uint64(i + 1)
		if rec.Seq != wantSeq {
			t.Fatalf("record %d Seq = %d, want %d (order or gap defect)", i, rec.Seq, wantSeq)
		}
		assertConsistent(t, rec)
	}
}

// TestConcurrentSeqUniqueGapless writes from many goroutines and asserts the
// global Seq is still a unique, gapless total order across shards (AC-3). It is
// the integrity proof that Seq stays unique and gapless for ACCEPTED writes, so
// it must accept every write: MaxRecords is sized through shardSafeMaxRecords so
// no write is lost to drop-oldest even when concurrent writers concentrate on a
// single shard at high GOMAXPROCS (DEF-004). A total-sized MaxRecords (the prior
// 1<<16) is a per-shard capacity of only 1024 at 64 shards, below the 3200
// total, so records silently dropped and len<3200 at GOMAXPROCS≥32.
func TestConcurrentSeqUniqueGapless(t *testing.T) {
	t.Parallel()
	const (
		writers = 16
		each    = 200
		total   = writers * each
	)
	r := journal.New(enabledConfig(shardSafeMaxRecords(total)))
	var wg sync.WaitGroup
	for w := range writers {
		wg.Add(1)
		go func(base int) {
			defer wg.Done()
			for i := range each {
				if _, err := r.Write(context.Background(), mkRecord(base*each+i)); err != nil {
					t.Errorf("Write: %v", err)
					return
				}
			}
		}(w)
	}
	wg.Wait()

	got := r.Snapshot()
	if len(got) != total {
		t.Fatalf("len = %d, want %d", len(got), total)
	}
	seen := make(map[uint64]bool, total)
	for _, rec := range got {
		if seen[rec.Seq] {
			t.Fatalf("duplicate Seq %d", rec.Seq)
		}
		seen[rec.Seq] = true
		assertConsistent(t, rec)
	}
	for s := uint64(1); s <= uint64(total); s++ {
		if !seen[s] {
			t.Fatalf("gap: Seq %d missing", s)
		}
	}
}

// TestConcurrentReadDuringWrite is the torn-read guard (AC-2). Readers snapshot
// the ring continuously while writers hammer it; every record a reader observes
// must satisfy the cross-field invariant. Run under -race, a data race on the
// payload publication is also reported. It exercises the code path but — because
// publication is a single atomic pointer store — cannot itself tear; the direct
// seqlock-logic guard lives in the internal TestSlotSeqlock* tests.
func TestConcurrentReadDuringWrite(t *testing.T) {
	t.Parallel()
	// A small ring so writers wrap and overwrite slots readers are mid-read on.
	r := journal.New(enabledConfig(64))
	var stop atomic.Bool
	var wg sync.WaitGroup

	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; !stop.Load(); n++ {
				if _, err := r.Write(context.Background(), mkRecord(n)); err != nil {
					t.Errorf("Write: %v", err)
					return
				}
			}
		}()
	}
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 2000 {
				for _, rec := range r.Snapshot() {
					assertConsistent(t, rec)
				}
			}
		}()
	}
	time.Sleep(50 * time.Millisecond)
	stop.Store(true)
	wg.Wait()
}

// TestConsistentViewSnapshotsUnderWriteDisable covers AC-8: a consistent view
// reads a coherent snapshot even while writers race, because it briefly disables
// writes for the copy.
func TestConsistentViewSnapshotsUnderWriteDisable(t *testing.T) {
	t.Parallel()
	r := journal.New(enabledConfig(1 << 12))
	var stop atomic.Bool
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; !stop.Load(); n++ {
				_, _ = r.Write(context.Background(), mkRecord(n))
			}
		}()
	}
	for range 200 {
		v := r.ConsistentView()
		v.Iter(func(rec journalapi.Record) bool {
			assertConsistent(t, rec)
			return true
		})
	}
	stop.Store(true)
	wg.Wait()
}

// TestClearEmpties covers Clear: the journal empties and its byte budget resets,
// while Seq stays globally monotone across the clear (MOCK-602.6/702.7).
func TestClearEmpties(t *testing.T) {
	t.Parallel()
	r := journal.New(enabledConfig(1024))
	for i := range 100 {
		if _, err := r.Write(context.Background(), mkRecord(i)); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}
	r.Clear()
	if r.Len() != 0 {
		t.Fatalf("Len after Clear = %d, want 0", r.Len())
	}
	if r.Bytes() != 0 {
		t.Fatalf("Bytes after Clear = %d, want 0", r.Bytes())
	}
	// Seq continues, it does not reset.
	seq, err := r.Write(context.Background(), mkRecord(1))
	if err != nil {
		t.Fatalf("Write after Clear: %v", err)
	}
	if seq != 101 {
		t.Fatalf("Seq after Clear = %d, want 101 (Seq must stay monotone)", seq)
	}
}

// TestDisabledStoresNothing verifies the disabled path records nothing and
// returns a zero Seq without error (MOCK-901 behavior half; the allocation half
// is TestWriteDisabledZeroAllocs).
func TestDisabledStoresNothing(t *testing.T) {
	t.Parallel()
	r := journal.New(journalapi.Config{Enabled: false, MaxRecords: 1024})
	seq, err := r.Write(context.Background(), mkRecord(1))
	if err != nil {
		t.Fatalf("disabled Write err = %v", err)
	}
	if seq != 0 {
		t.Fatalf("disabled Write Seq = %d, want 0", seq)
	}
	if r.Len() != 0 {
		t.Fatalf("disabled ring Len = %d, want 0", r.Len())
	}
}

// TestSetEnabledToggles verifies a ring can be switched on and off at runtime.
func TestSetEnabledToggles(t *testing.T) {
	t.Parallel()
	r := journal.New(journalapi.Config{Enabled: false, MaxRecords: 1024})
	if _, err := r.Write(context.Background(), mkRecord(1)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if r.Len() != 0 {
		t.Fatalf("wrote while disabled")
	}
	r.SetEnabled(true)
	if !r.Enabled() {
		t.Fatalf("Enabled() = false after SetEnabled(true)")
	}
	if _, err := r.Write(context.Background(), mkRecord(2)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if r.Len() != 1 {
		t.Fatalf("Len = %d after enabling and one write, want 1", r.Len())
	}
	r.SetEnabled(false)
	if _, err := r.Write(context.Background(), mkRecord(3)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if r.Len() != 1 {
		t.Fatalf("Len = %d after disabling, want still 1", r.Len())
	}
}

// TestWriteDisabledZeroAllocs is the MOCK-901 allocation proof: with journaling
// off, Write must allocate nothing. testing.AllocsPerRun returns the average
// allocations per call; it must be exactly zero.
func TestWriteDisabledZeroAllocs(t *testing.T) {
	// No t.Parallel(): testing.AllocsPerRun panics if called during a parallel
	// test, and the allocation count must be measured without interference.
	r := journal.New(journalapi.Config{Enabled: false, MaxRecords: 1024})
	ctx := context.Background()
	rec := mkRecord(1)
	allocs := testing.AllocsPerRun(1000, func() {
		_, _ = r.Write(ctx, rec)
	})
	if allocs != 0 {
		t.Fatalf("disabled Write allocated %.2f objects/op, want 0 (MOCK-901)", allocs)
	}
}
