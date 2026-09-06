package journal_test

import (
	"context"
	"testing"
	"time"

	"github.com/vyrodovalexey/mcp-mock-server/internal/journal"
	"github.com/vyrodovalexey/mcp-mock-server/journalapi"
)

// seedQueryRing writes the fixed record set (see seedQueryRecords) into a
// shard-safe ring for the filter/export tests and returns it.
func seedQueryRing(t *testing.T) *journal.Ring {
	t.Helper()
	// These records are written from this single goroutine, so all land on one
	// shard (see pickShard). MaxRecords must therefore cover the per-shard
	// worst case (n*maxShards), not just the record count, or drop-oldest
	// silently evicts records once GOMAXPROCS raises the shard count. See
	// shardSafeMaxRecords for the full explanation of this shard-sizing trap.
	records := seedQueryRecords()
	ring := journal.New(enabledConfig(shardSafeMaxRecords(len(records))))
	for _, rec := range records {
		if _, err := ring.Write(context.Background(), rec); err != nil {
			t.Fatalf("seed write: %v", err)
		}
	}
	return ring
}

// seedQueryRecords returns the fixed, diverse record set the query/export tests
// filter over. Each record varies one dimension so a single-field selector
// picks a known subset. Kept separate from seedQueryRing so the sizing helper
// can bound MaxRecords by the actual record count.
func seedQueryRecords() []journalapi.Record {
	base := time.Date(2026, 9, 4, 9, 0, 0, 0, time.UTC)
	status200, status503 := 200, 503
	return []journalapi.Record{
		{Instance: "one", Transport: journalapi.TransportHTTP, Era: "modern",
			WallTime: base.Add(0 * time.Minute),
			JSONRPC:  journalapi.JSONRPCPart{Method: "tools/list", Name: ""},
			Response: &journalapi.ResponsePart{Status: status200}},
		{Instance: "one", Transport: journalapi.TransportHTTP, Era: "modern",
			WallTime: base.Add(1 * time.Minute),
			JSONRPC:  journalapi.JSONRPCPart{Method: "tools/call", Name: "search"},
			Response: &journalapi.ResponsePart{Status: status200}},
		{Instance: "two", Transport: journalapi.TransportStdio, Era: "modern",
			WallTime:    base.Add(2 * time.Minute),
			JSONRPC:     journalapi.JSONRPCPart{Method: "tools/call", Name: "fetch"},
			Response:    &journalapi.ResponsePart{Status: status503},
			Correlation: journalapi.CorrelationPart{TraceID: "trace-xyz"}},
		{Instance: "two", Transport: journalapi.TransportStdio, Era: "modern",
			WallTime: base.Add(3 * time.Minute),
			JSONRPC:  journalapi.JSONRPCPart{Method: "server/discover"},
			Response: &journalapi.ResponsePart{Status: status200},
			Faults:   []journalapi.FaultFired{{RuleID: "rule-1"}}},
	}
}

// TestQueryFilters covers 602.4/602.7: each filter dimension works individually
// and results are in Seq order.
func TestQueryFilters(t *testing.T) {
	t.Parallel()
	ring := seedQueryRing(t)
	status503 := 503

	tests := []struct {
		name     string
		selector journalapi.Selector
		wantSeqs []uint64
	}{
		{"method-glob", journalapi.Selector{Method: "tools/*"}, []uint64{1, 2, 3}},
		{"method-exact", journalapi.Selector{Method: "tools/call"}, []uint64{2, 3}},
		{"name", journalapi.Selector{Name: "search"}, []uint64{2}},
		{"instance", journalapi.Selector{Instance: "two"}, []uint64{3, 4}},
		{"transport", journalapi.Selector{Transport: "stdio"}, []uint64{3, 4}},
		{"traceId", journalapi.Selector{TraceID: "trace-xyz"}, []uint64{3}},
		{"status", journalapi.Selector{StatusCode: &status503}, []uint64{3}},
		{"faultRule", journalapi.Selector{FaultRule: "rule-1"}, []uint64{4}},
		{"empty-all", journalapi.Selector{}, []uint64{1, 2, 3, 4}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ring.Query(tc.selector)
			assertSeqs(t, got, tc.wantSeqs)
		})
	}
}

// TestQueryTimeRange covers the since/until filter dimension (602.4).
func TestQueryTimeRange(t *testing.T) {
	t.Parallel()
	ring := seedQueryRing(t)
	base := time.Date(2026, 9, 4, 9, 0, 0, 0, time.UTC)
	// [base+1min, base+3min): records 2 and 3.
	got := ring.Query(journalapi.Selector{
		Since: base.Add(1 * time.Minute),
		Until: base.Add(3 * time.Minute),
	})
	assertSeqs(t, got, []uint64{2, 3})
}

// TestQueryAndCombines covers 602.4 combination: multiple selectors AND, and the
// order of application does not change the result set.
func TestQueryAndCombines(t *testing.T) {
	t.Parallel()
	ring := seedQueryRing(t)
	a := journalapi.Selector{Method: "tools/call"}
	b := journalapi.Selector{Transport: "stdio"}
	// tools/call AND stdio => only record 3.
	ab := ring.Query(a, b)
	ba := ring.Query(b, a)
	assertSeqs(t, ab, []uint64{3})
	assertSeqs(t, ba, []uint64{3})
}

// TestQueryConsistent covers the consistent read path returning the same set.
func TestQueryConsistent(t *testing.T) {
	t.Parallel()
	ring := seedQueryRing(t)
	got := ring.QueryConsistent(journalapi.Selector{Instance: "one"})
	assertSeqs(t, got, []uint64{1, 2})
}

// assertSeqs checks the records' Seq values equal want, in order.
func assertSeqs(t *testing.T, got []journalapi.Record, want []uint64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d records, want %d (seqs %v)", len(got), len(want), recSeqs(got))
	}
	for i, w := range want {
		if got[i].Seq != w {
			t.Fatalf("record[%d] Seq = %d, want %d (full: %v)", i, got[i].Seq, w, recSeqs(got))
		}
	}
}

// recSeqs extracts the Seq list for a diagnostic message.
func recSeqs(recs []journalapi.Record) []uint64 {
	out := make([]uint64, len(recs))
	for i, r := range recs {
		out[i] = r.Seq
	}
	return out
}
