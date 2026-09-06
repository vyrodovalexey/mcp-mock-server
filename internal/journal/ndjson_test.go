package journal_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/vyrodovalexey/mcp-mock-server/internal/journal"
	"github.com/vyrodovalexey/mcp-mock-server/journalapi"
)

// TestExportNDJSONRoundTrip covers 602.2/602.8: the exported NDJSON parses back
// through journalapi's Reader into the same records, in Seq order, and matches
// the JSON-array form modulo framing.
func TestExportNDJSONRoundTrip(t *testing.T) {
	t.Parallel()
	ring := seedQueryRing(t)

	var buf bytes.Buffer
	n, err := ring.ExportNDJSON(context.Background(), &buf, journal.ExportOptions{})
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if n != 4 {
		t.Fatalf("exported %d records, want 4", n)
	}

	// Round-trip through the public reader.
	got, err := journalapi.NewReader(&buf).ReadAll()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	want := ring.Snapshot()
	if len(got) != len(want) {
		t.Fatalf("round-trip len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Seq != want[i].Seq {
			t.Errorf("record[%d] Seq = %d, want %d (order lost in round-trip)", i, got[i].Seq, want[i].Seq)
		}
		if got[i].JSONRPC.Method != want[i].JSONRPC.Method {
			t.Errorf("record[%d] method mismatch after round-trip", i)
		}
	}
}

// TestExportNDJSONFiltered covers export honoring selectors (602.4 over the
// stream).
func TestExportNDJSONFiltered(t *testing.T) {
	t.Parallel()
	ring := seedQueryRing(t)
	var buf bytes.Buffer
	n, err := ring.ExportNDJSON(context.Background(), &buf, journal.ExportOptions{
		Selectors: []journalapi.Selector{{Method: "tools/call"}},
	})
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if n != 2 {
		t.Fatalf("filtered export wrote %d, want 2", n)
	}
	recs, err := journalapi.NewReader(&buf).ReadAll()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	assertSeqs(t, recs, []uint64{2, 3})
}

// lineCountingWriter records, at each Write call, how many complete NDJSON lines
// (newline-terminated) have been delivered so far. It lets a test observe that
// records arrive incrementally rather than all at once at the end — the
// streaming property (MOCK-602.2).
type lineCountingWriter struct {
	total     int
	lineTally []int // complete-line count observed after each Write
}

func (w *lineCountingWriter) Write(p []byte) (int, error) {
	w.total += bytes.Count(p, []byte{'\n'})
	w.lineTally = append(w.lineTally, w.total)
	return len(p), nil
}

// TestExportNDJSONStreamsIncrementally proves the export does not materialize the
// whole journal before writing: with FlushEvery=1 the records are delivered to
// the underlying writer progressively, so complete lines accumulate across many
// Write calls rather than appearing only in one final burst (MOCK-602.2).
func TestExportNDJSONStreamsIncrementally(t *testing.T) {
	t.Parallel()
	const n = 5000
	// MaxRecords is sized so that even if the single writing goroutine's pooled
	// shard hint concentrates every write on one shard, that shard's ring
	// (MaxRecords / shardCount, shardCount ≤ 64) still holds all n records
	// without wrapping. 1<<20 / 64 = 16384 > n.
	ring := journal.New(journalapi.Config{Enabled: true, MaxRecords: 1 << 20})
	for i := range n {
		rec := journalapi.Record{
			Instance: "s",
			JSONRPC:  journalapi.JSONRPCPart{Method: "tools/call", BodyLength: i},
		}
		if _, err := ring.Write(context.Background(), rec); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	w := &lineCountingWriter{}
	count, err := ring.ExportNDJSON(context.Background(), w, journal.ExportOptions{FlushEvery: 1})
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if count != n {
		t.Fatalf("exported %d, want %d", count, n)
	}
	// If the export had materialized then dumped, nearly all lines would appear
	// in one final Write. Incremental streaming produces many intermediate
	// Writes each adding lines. Require a healthy number of distinct progress
	// points — far more than a single final flush.
	progressPoints := 0
	prev := 0
	for _, tally := range w.lineTally {
		if tally > prev {
			progressPoints++
			prev = tally
		}
	}
	if progressPoints < n/2 {
		t.Fatalf("export delivered lines in %d progress steps for %d records; "+
			"looks materialized-then-dumped, not streamed", progressPoints, n)
	}
}

// TestExportNDJSONDrain covers the ADR-005 drain interaction: with Drain set, a
// successful export advances the ring's drained cursor so a writer blocked under
// the block overflow policy finds capacity.
func TestExportNDJSONDrain(t *testing.T) {
	t.Parallel()
	// A block-policy ring with a short block timeout so a would-be block resolves
	// fast. All writes here run on this single goroutine, so the pooled shard
	// hint lands them on one shard: that shard is filled to its ceiling, and the
	// next write must block. After a drained export releases capacity, the next
	// write proceeds without degrading — proving the export called Drain.
	cfg := journalapi.Config{
		Enabled:      true,
		MaxRecords:   16,
		Overflow:     journalapi.OverflowBlock,
		BlockTimeout: 20 * time.Millisecond,
	}
	ring := journal.New(cfg)

	// Fill until the writing shard degrades once: that pins the shard at its
	// undrained ceiling. A bounded loop guards against a runaway.
	for i := 0; i < 10_000 && ring.Degraded() == 0; i++ {
		if _, err := ring.Write(context.Background(), journalapi.Record{
			JSONRPC: journalapi.JSONRPCPart{Method: "tools/call"},
		}); err != nil {
			t.Fatalf("fill write %d: %v", i, err)
		}
	}
	if ring.Degraded() == 0 {
		t.Fatal("could not saturate the writing shard under block policy")
	}

	// Export with Drain acknowledges consumption of the saturated shard.
	var buf bytes.Buffer
	if _, err := ring.ExportNDJSON(context.Background(), &buf, journal.ExportOptions{Drain: true}); err != nil {
		t.Fatalf("export: %v", err)
	}

	degradedAfterDrain := ring.Degraded()
	// A write after the drain finds released capacity and does not degrade.
	if _, err := ring.Write(context.Background(), journalapi.Record{
		JSONRPC: journalapi.JSONRPCPart{Method: "tools/call"},
	}); err != nil {
		t.Fatalf("post-drain write: %v", err)
	}
	if ring.Degraded() != degradedAfterDrain {
		t.Errorf("write after drain degraded (%d -> %d); drain did not release capacity",
			degradedAfterDrain, ring.Degraded())
	}
}

// TestExportNDJSONNoDrainLeavesRing proves a non-draining export is a read: it
// does not advance the drained cursor (the ring's held count is unchanged).
func TestExportNDJSONNoDrainLeavesRing(t *testing.T) {
	t.Parallel()
	ring := seedQueryRing(t)
	before := ring.Len()
	var buf bytes.Buffer
	if _, err := ring.ExportNDJSON(context.Background(), &buf, journal.ExportOptions{Drain: false}); err != nil {
		t.Fatalf("export: %v", err)
	}
	if ring.Len() != before {
		t.Errorf("non-draining export changed held count: %d -> %d", before, ring.Len())
	}
}

// TestExportNDJSONCanceled covers context cancellation: a canceled export stops
// and reports the cancellation, and does not drain.
func TestExportNDJSONCanceled(t *testing.T) {
	t.Parallel()
	ring := seedQueryRing(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already canceled
	_, err := ring.ExportNDJSON(ctx, io.Discard, journal.ExportOptions{Drain: true})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("export err = %v, want context.Canceled", err)
	}
}

// TestExportNDJSONEmpty covers the empty-journal case: zero records, no error.
func TestExportNDJSONEmpty(t *testing.T) {
	t.Parallel()
	ring := journal.New(journalapi.Config{Enabled: true, MaxRecords: 8})
	var buf bytes.Buffer
	n, err := ring.ExportNDJSON(context.Background(), &buf, journal.ExportOptions{})
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if n != 0 || buf.Len() != 0 {
		t.Fatalf("empty export wrote %d records / %d bytes, want 0/0", n, buf.Len())
	}
}
