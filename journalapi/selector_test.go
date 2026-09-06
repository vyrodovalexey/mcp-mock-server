package journalapi_test

import (
	"encoding/json"
	"testing"
	"time"

	japi "github.com/vyrodovalexey/mcp-mock-server/journalapi"
)

func intPtr(n int) *int { return &n }

// records builds a small fixed corpus spanning methods, transports, times,
// chains, trace ids, faults and statuses, so selector cases can be table-driven.
func records() []japi.Record {
	base := time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC)
	return []japi.Record{
		{
			Seq: 1, Instance: "a", Transport: japi.TransportHTTP, Era: "modern",
			WallTime:    base,
			JSONRPC:     japi.JSONRPCPart{Method: "tools/call", Name: "search"},
			Response:    &japi.ResponsePart{Status: 200},
			Correlation: japi.CorrelationPart{TraceID: "trace-1"},
			MRTR:        &japi.MRTRPart{ChainID: "chain-1", Round: 1},
		},
		{
			Seq: 2, Instance: "a", Transport: japi.TransportStdio, Era: "modern",
			WallTime:    base.Add(time.Minute),
			JSONRPC:     japi.JSONRPCPart{Method: "tools/list"},
			Response:    &japi.ResponsePart{Status: 200},
			Correlation: japi.CorrelationPart{TraceID: "trace-2"},
		},
		{
			Seq: 3, Instance: "b", Transport: japi.TransportHTTP, Era: "modern",
			WallTime:    base.Add(2 * time.Minute),
			JSONRPC:     japi.JSONRPCPart{Method: "server/discover"},
			Response:    &japi.ResponsePart{Status: 500},
			Faults:      []japi.FaultFired{{RuleID: "fault-x"}},
			Correlation: japi.CorrelationPart{TraceID: "trace-1"},
			MRTR:        &japi.MRTRPart{ChainID: "chain-1", Round: 2, InitialID: json.RawMessage(`1`)},
		},
	}
}

func TestSelectorMatches(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		sel     japi.Selector
		wantSeq []uint64
	}{
		{"empty matches all", japi.Selector{}, []uint64{1, 2, 3}},
		{"instance", japi.Selector{Instance: "a"}, []uint64{1, 2}},
		{"method glob", japi.Selector{Method: "tools/*"}, []uint64{1, 2}},
		{"method exact", japi.Selector{Method: "tools/call"}, []uint64{1}},
		{"name", japi.Selector{Name: "search"}, []uint64{1}},
		{"transport http", japi.Selector{Transport: "http"}, []uint64{1, 3}},
		{"transport stdio", japi.Selector{Transport: "stdio"}, []uint64{2}},
		{"era", japi.Selector{Era: "modern"}, []uint64{1, 2, 3}},
		{"since", japi.Selector{Since: base.Add(time.Minute)}, []uint64{2, 3}},
		{"until", japi.Selector{Until: base.Add(2 * time.Minute)}, []uint64{1, 2}},
		{"time window", japi.Selector{Since: base.Add(time.Minute), Until: base.Add(2 * time.Minute)}, []uint64{2}},
		{"chain id", japi.Selector{ChainID: "chain-1"}, []uint64{1, 3}},
		{"trace id", japi.Selector{TraceID: "trace-1"}, []uint64{1, 3}},
		{"fault rule", japi.Selector{FaultRule: "fault-x"}, []uint64{3}},
		{"status 200", japi.Selector{StatusCode: intPtr(200)}, []uint64{1, 2}},
		{"status 500", japi.Selector{StatusCode: intPtr(500)}, []uint64{3}},
		{"and-combined narrows", japi.Selector{Instance: "a", Method: "tools/call"}, []uint64{1}},
		{"no match", japi.Selector{Method: "prompts/*"}, nil},
		{"malformed glob matches nothing", japi.Selector{Method: "[bad"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var got []uint64
			for _, r := range records() {
				if tc.sel.Matches(r) {
					got = append(got, r.Seq)
				}
			}
			if !equalSeq(got, tc.wantSeq) {
				t.Errorf("selector %+v matched %v, want %v", tc.sel, got, tc.wantSeq)
			}
		})
	}
}

func TestSelectorEmpty(t *testing.T) {
	t.Parallel()
	if !(japi.Selector{}).Empty() {
		t.Error("zero Selector should report Empty")
	}
	if (japi.Selector{Method: "x"}).Empty() {
		t.Error("Selector with a field set should not report Empty")
	}
	if (japi.Selector{StatusCode: intPtr(0)}).Empty() {
		t.Error("Selector with StatusCode set (even to 0) should not report Empty")
	}
}

func TestViewFilterComposesOrderIndependent(t *testing.T) {
	t.Parallel()
	v := japi.NewView(records())
	selA := japi.Selector{Instance: "a"}
	selHTTP := japi.Selector{Transport: "http"}

	ab := v.Filter(selA).Filter(selHTTP).Snapshot()
	ba := v.Filter(selHTTP).Filter(selA).Snapshot()

	if !recordsEqual(ab, ba) {
		t.Fatalf("filter composition is order-dependent: A∘HTTP=%v HTTP∘A=%v",
			seqs(ab), seqs(ba))
	}
	if len(ab) != 1 || ab[0].Seq != 1 {
		t.Fatalf("composed filter selected %v, want [1]", seqs(ab))
	}
}

func TestViewFilterDoesNotMutateReceiver(t *testing.T) {
	t.Parallel()
	v := japi.NewView(records())
	before := v.Len()
	_ = v.Filter(japi.Selector{Instance: "a"})
	if v.Len() != before {
		t.Fatalf("Filter mutated the receiver: len %d -> %d", before, v.Len())
	}
}

func TestViewSnapshotIsCopy(t *testing.T) {
	t.Parallel()
	v := japi.NewView(records())
	snap := v.Snapshot()
	snap[0].Instance = "mutated"
	if v.Snapshot()[0].Instance == "mutated" {
		t.Fatal("Snapshot must return copies; mutating the result affected the View")
	}
}

func TestViewIterEarlyStop(t *testing.T) {
	t.Parallel()
	v := japi.NewView(records())
	count := 0
	v.Iter(func(japi.Record) bool {
		count++
		return count < 2 // stop after visiting the second record
	})
	if count != 2 {
		t.Fatalf("Iter visited %d records, want 2 (early stop honoured)", count)
	}
}

func TestNewViewSortsBySeq(t *testing.T) {
	t.Parallel()
	unsorted := []japi.Record{{Seq: 3}, {Seq: 1}, {Seq: 2}}
	got := seqs(japi.NewView(unsorted).Snapshot())
	want := []uint64{1, 2, 3}
	if !equalSeq(got, want) {
		t.Fatalf("NewView produced %v, want %v", got, want)
	}
}

func TestCorrelations(t *testing.T) {
	t.Parallel()
	v := japi.NewView(records())
	cs := japi.Correlations(v)
	if len(cs) != 1 {
		t.Fatalf("got %d chains, want 1", len(cs))
	}
	c := cs[0]
	if c.ChainID != "chain-1" {
		t.Errorf("ChainID = %q, want chain-1", c.ChainID)
	}
	if c.Rounds != 2 {
		t.Errorf("Rounds = %d, want 2", c.Rounds)
	}
	if !equalSeq(c.Seqs, []uint64{1, 3}) {
		t.Errorf("Seqs = %v, want [1 3]", c.Seqs)
	}
	if !c.RetryIDsDistinct {
		t.Errorf("RetryIDsDistinct = false, want true (initial \"1\" vs retry 1 are byte-distinct)")
	}
}

func TestCorrelationsDetectsDuplicateID(t *testing.T) {
	t.Parallel()
	recs := []japi.Record{
		{Seq: 1, JSONRPC: japi.JSONRPCPart{ID: json.RawMessage(`5`)}, MRTR: &japi.MRTRPart{ChainID: "c", Round: 1}},
		{Seq: 2, JSONRPC: japi.JSONRPCPart{ID: json.RawMessage(`5`)}, MRTR: &japi.MRTRPart{ChainID: "c", Round: 2}},
	}
	cs := japi.Correlations(japi.NewView(recs))
	if len(cs) != 1 {
		t.Fatalf("got %d chains, want 1", len(cs))
	}
	if cs[0].RetryIDsDistinct {
		t.Error("RetryIDsDistinct = true, want false (id 5 reused across rounds)")
	}
}

// ---- helpers ----

func equalSeq(a, b []uint64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func seqs(rs []japi.Record) []uint64 {
	out := make([]uint64, len(rs))
	for i, r := range rs {
		out[i] = r.Seq
	}
	return out
}

func recordsEqual(a, b []japi.Record) bool {
	return equalSeq(seqs(a), seqs(b))
}
