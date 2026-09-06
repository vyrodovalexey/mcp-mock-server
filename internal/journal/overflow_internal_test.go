package journal

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/vyrodovalexey/mcp-mock-server/journalapi"
)

// These tests are white-box (package journal) because they pin the shard count
// to one via newRing, making the per-shard overflow bound deterministic and
// independent of the test host's GOMAXPROCS. The exported constructor New
// derives the shard count from GOMAXPROCS, so a single-goroutine overflow test
// through New could not know which shard its writes land on or its exact bound.

func record(n int) journalapi.Record {
	return journalapi.Record{
		Instance: "inst",
		MonoNs:   int64(n),
		JSONRPC: journalapi.JSONRPCPart{
			Method: "tools/call",
			Body:   []byte(fmt.Sprintf("body-%d", n)),
		},
	}
}

// singleShard builds a ring with exactly one shard so a single writer's fill and
// overflow are fully deterministic.
func singleShard(t *testing.T, cfg journalapi.Config) *Ring {
	t.Helper()
	r := newRing(cfg, 1)
	if got := len(r.shards); got != 1 {
		t.Fatalf("expected 1 shard, got %d", got)
	}
	return r
}

// TestDropOldestOverflow covers AC-4.
func TestDropOldestOverflow(t *testing.T) {
	t.Parallel()
	r := singleShard(t, journalapi.Config{
		Enabled:    true,
		MaxRecords: 4,
		Overflow:   journalapi.OverflowDropOldest,
	})
	capacity := r.Cap()
	for i := range capacity * 3 {
		if _, err := r.Write(context.Background(), record(i)); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}
	if r.Dropped() == 0 {
		t.Fatalf("dropped counter did not increment on overflow")
	}
	if r.Len() > capacity {
		t.Fatalf("Len = %d exceeds capacity %d: ring not bounded", r.Len(), capacity)
	}
	var sawMarker bool
	for _, rec := range r.Snapshot() {
		if rec.Dropped {
			sawMarker = true
		}
	}
	if !sawMarker {
		t.Fatalf("no retained record carries the Dropped marker (reader cannot see the gap)")
	}
	// The retained records are the most recent ones (oldest were dropped).
	got := r.Snapshot()
	last := got[len(got)-1]
	if last.MonoNs != int64(capacity*3-1) {
		t.Fatalf("newest retained MonoNs = %d, want %d", last.MonoNs, capacity*3-1)
	}
}

// TestBlockPolicyTimesOutAndDegrades covers AC-5 (timeout + distinct counter).
func TestBlockPolicyTimesOutAndDegrades(t *testing.T) {
	t.Parallel()
	r := singleShard(t, journalapi.Config{
		Enabled:      true,
		MaxRecords:   4,
		Overflow:     journalapi.OverflowBlock,
		BlockTimeout: 20 * time.Millisecond,
	})
	capacity := r.Cap()
	for i := range capacity {
		if _, err := r.Write(context.Background(), record(i)); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}
	start := time.Now()
	if _, err := r.Write(context.Background(), record(999)); err != nil {
		t.Fatalf("blocking Write returned error: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed < 15*time.Millisecond {
		t.Fatalf("block returned in %v, expected ~20ms wait then degrade", elapsed)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("block took %v — must be bounded, never indefinite (GAP-017)", elapsed)
	}
	if r.Degraded() == 0 {
		t.Fatalf("degraded counter did not increment after block timeout")
	}
}

// TestBlockPolicyProceedsAfterDrain covers AC-5 (drain releases the writer).
func TestBlockPolicyProceedsAfterDrain(t *testing.T) {
	t.Parallel()
	r := singleShard(t, journalapi.Config{
		Enabled:      true,
		MaxRecords:   4,
		Overflow:     journalapi.OverflowBlock,
		BlockTimeout: 2 * time.Second,
	})
	capacity := r.Cap()
	for i := range capacity {
		if _, err := r.Write(context.Background(), record(i)); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}
	go func() {
		time.Sleep(30 * time.Millisecond)
		r.Drain()
	}()
	start := time.Now()
	if _, err := r.Write(context.Background(), record(1000)); err != nil {
		t.Fatalf("Write after drain: %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatalf("writer did not proceed promptly after Drain")
	}
	if r.Degraded() != 0 {
		t.Fatalf("Degraded = %d, want 0 — writer should have proceeded via drain", r.Degraded())
	}
}

// TestBlockPolicyRespectsContext covers AC-5 (context cancellation unblocks).
func TestBlockPolicyRespectsContext(t *testing.T) {
	t.Parallel()
	r := singleShard(t, journalapi.Config{
		Enabled:      true,
		MaxRecords:   4,
		Overflow:     journalapi.OverflowBlock,
		BlockTimeout: 10 * time.Second, // long, so cancellation is what unblocks
	})
	capacity := r.Cap()
	for i := range capacity {
		if _, err := r.Write(context.Background(), record(i)); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	if _, err := r.Write(ctx, record(1000)); err != nil {
		t.Fatalf("Write under canceled ctx: %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("write ignored context cancellation: waited %v", elapsed)
	}
	if r.Degraded() == 0 {
		t.Fatalf("degraded counter did not move after ctx-canceled block")
	}
}

// TestErrorPolicyRejects covers AC-6.
func TestErrorPolicyRejects(t *testing.T) {
	t.Parallel()
	r := singleShard(t, journalapi.Config{
		Enabled:    true,
		MaxRecords: 4,
		Overflow:   journalapi.OverflowError,
	})
	capacity := r.Cap()
	for i := range capacity {
		if _, err := r.Write(context.Background(), record(i)); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}
	_, err := r.Write(context.Background(), record(999))
	if !errors.Is(err, ErrOverflow) {
		t.Fatalf("overflow write err = %v, want ErrOverflow", err)
	}
	if r.Rejected() == 0 {
		t.Fatalf("rejected counter did not increment")
	}
	if r.Len() > capacity {
		t.Fatalf("rejected record was stored: Len = %d, cap = %d", r.Len(), capacity)
	}
}

// TestMaxBytesBoundHolds covers AC-7 for the byte bound and the deliverable's
// "memory bound actually holds under sustained writes". With a byte budget far
// below the record-count capacity, retained bytes must stay bounded near the
// budget however long writing continues.
func TestMaxBytesBoundHolds(t *testing.T) {
	t.Parallel()
	const budget = 64 * 1024
	r := singleShard(t, journalapi.Config{
		Enabled:    true,
		MaxRecords: 1 << 16, // large, so MaxBytes is the binding bound
		MaxBytes:   budget,
		Overflow:   journalapi.OverflowDropOldest,
	})
	body := make([]byte, 1024)
	for i := range 5000 {
		rec := record(i)
		rec.JSONRPC.Body = body
		if _, err := r.Write(context.Background(), rec); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
		// Assert the bound holds at every step, not just at the end.
		if got := r.Bytes(); got > 2*budget {
			t.Fatalf("at write %d retained bytes = %d exceeds 2x budget %d", i, got, budget)
		}
	}
	if r.Dropped() == 0 {
		t.Fatalf("byte-bound overflow did not increment dropped counter")
	}
}

// TestMaxRecordsBoundHolds covers AC-7 for the count bound, independently of the
// byte bound.
func TestMaxRecordsBoundHolds(t *testing.T) {
	t.Parallel()
	r := singleShard(t, journalapi.Config{
		Enabled:    true,
		MaxRecords: 128,
		MaxBytes:   1 << 40, // effectively unbounded
		Overflow:   journalapi.OverflowDropOldest,
	})
	capacity := r.Cap()
	for i := range capacity * 4 {
		if _, err := r.Write(context.Background(), record(i)); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}
	if r.Len() > capacity {
		t.Fatalf("Len = %d exceeds capacity %d: count bound not enforced", r.Len(), capacity)
	}
	if r.Dropped() == 0 {
		t.Fatalf("count-bound overflow did not increment dropped counter")
	}
}

// TestShardCountRounding pins the ADR-005 shard-count rule: next power of two,
// at least one, capped at maxShards.
func TestShardCountRounding(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   int
		want uint64
	}{
		{in: 0, want: 1},
		{in: 1, want: 1},
		{in: 2, want: 2},
		{in: 3, want: 4},
		{in: 5, want: 8},
		{in: 8, want: 8},
		{in: 100, want: maxShards},
		{in: 1000, want: maxShards},
	}
	for _, c := range cases {
		if got := shardCount(c.in); got != c.want {
			t.Errorf("shardCount(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

// TestPerShardCapacityRounding pins the per-shard power-of-two rounding and the
// minimum-capacity floor.
func TestPerShardCapacityRounding(t *testing.T) {
	t.Parallel()
	cases := []struct {
		records int
		shards  uint64
		want    uint64
	}{
		{records: 0, shards: 1, want: minShardCapacity},
		{records: 1, shards: 1, want: minShardCapacity},
		{records: 100, shards: 1, want: 128},
		{records: 100_000, shards: 8, want: 16384},
		{records: 3, shards: 4, want: minShardCapacity},
	}
	for _, c := range cases {
		if got := perShardCapacity(c.records, c.shards); got != c.want {
			t.Errorf("perShardCapacity(%d,%d) = %d, want %d", c.records, c.shards, got, c.want)
		}
	}
}
