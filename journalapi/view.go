package journalapi

import (
	"encoding/json"
	"sort"
)

// View is a read-only handle over a set of journal records in [Record.Seq]
// order (ADR-005). It is the query surface external test suites consume; both a
// live journal and a journal loaded from an NDJSON file expose it (MOCK-602,
// MOCK-603.4).
//
// Records yielded by a View are always copies (data-model.md §3.2): the backing
// ring may overwrite a slot at any time, so handing out a pointer into it would
// corrupt evidence.
//
// Stability: v0. The method set is the ADR-005 query contract.
type View interface {
	// Filter returns a View over the subset of records matching s. It never
	// mutates the receiver; composing filters in either order yields the same
	// record set because [Selector.Matches] is a pure conjunction.
	Filter(s Selector) View
	// Iter calls fn for each record in Seq order, stopping early if fn returns
	// false (matching the range-over-func convention).
	Iter(fn func(Record) bool)
	// Len reports the number of records in the View.
	Len() int
	// Snapshot returns a copy of the records in Seq order. Mutating the result
	// does not affect the View.
	Snapshot() []Record
}

// NewView returns a [View] over a copy of records, sorted into [Record.Seq]
// order. It is the constructor a file reader and a test use to obtain a View
// with no server present (MOCK-603.4). The input slice is not retained.
func NewView(records []Record) View {
	cp := make([]Record, len(records))
	copy(cp, records)
	sort.SliceStable(cp, func(i, j int) bool { return cp[i].Seq < cp[j].Seq })
	return sliceView{records: cp}
}

// sliceView is the immutable in-memory [View] implementation. Its records slice
// is owned by the view and never shared with a caller uncopied.
type sliceView struct {
	records []Record
}

// Filter implements [View.Filter].
func (v sliceView) Filter(s Selector) View {
	out := make([]Record, 0, len(v.records))
	for _, r := range v.records {
		if s.Matches(r) {
			out = append(out, r)
		}
	}
	return sliceView{records: out}
}

// Iter implements [View.Iter].
func (v sliceView) Iter(fn func(Record) bool) {
	for _, r := range v.records {
		if !fn(r) {
			return
		}
	}
}

// Len implements [View.Len].
func (v sliceView) Len() int { return len(v.records) }

// Snapshot implements [View.Snapshot].
func (v sliceView) Snapshot() []Record {
	out := make([]Record, len(v.records))
	copy(out, v.records)
	return out
}

// Correlation is a per-chain summary grouping journal records by
// [MRTRPart.ChainID] (MOCK-605). It is what the assert package's chain helpers
// and AssertRetryIDsDistinct consume.
//
// Stability: v0.
type Correlation struct {
	// ChainID is the grouping key.
	ChainID string `json:"chainId"`
	// Rounds is the number of rounds observed in the chain.
	Rounds int `json:"rounds"`
	// Seqs lists the [Record.Seq] of every record in the chain, in order.
	Seqs []uint64 `json:"seqs"`
	// InitialID is the raw JSON-RPC id of the first round.
	InitialID json.RawMessage `json:"initialId,omitempty"`
	// RetryIDs lists the raw JSON-RPC ids of the retry rounds, in order.
	RetryIDs []json.RawMessage `json:"retryIds,omitempty"`
	// RetryIDsDistinct is true when every id in the chain is byte-distinct
	// (MOCK-247). Compared as raw JSON, so 1, "1" and 1.0 are distinct.
	RetryIDsDistinct bool `json:"retryIdsDistinct"`
	// Outcome is the chain's terminal outcome (e.g. "completed", "rejected",
	// "abandoned").
	Outcome string `json:"outcome,omitempty"`
	// RejectionReason is the ADR-010 reason when the chain was rejected.
	RejectionReason string `json:"rejectionReason,omitempty"`
	// ElapsedMs is the chain's wall-clock span in milliseconds. Volatile.
	ElapsedMs float64 `json:"elapsedMs,omitempty"`
}

// Correlations groups the records of v by [MRTRPart.ChainID] and returns one
// [Correlation] per chain, in ascending order of each chain's first
// [Record.Seq]. Records with no MRTR part are ignored. This makes MOCK-605
// grouping expressible over the public contract with no server present.
func Correlations(v View) []Correlation {
	byChain := make(map[string]*Correlation)
	var order []string
	v.Iter(func(r Record) bool {
		if r.MRTR == nil || r.MRTR.ChainID == "" {
			return true
		}
		id := r.MRTR.ChainID
		c, ok := byChain[id]
		if !ok {
			c = &Correlation{ChainID: id, RetryIDsDistinct: true}
			byChain[id] = c
			order = append(order, id)
		}
		accumulateChain(c, r)
		return true
	})
	out := make([]Correlation, 0, len(order))
	for _, id := range order {
		out = append(out, *byChain[id])
	}
	return out
}

// accumulateChain folds one record into its chain summary.
func accumulateChain(c *Correlation, r Record) {
	c.Seqs = append(c.Seqs, r.Seq)
	if c.Rounds < r.MRTR.Round {
		c.Rounds = r.MRTR.Round
	}
	if r.MRTR.Round <= 1 {
		c.InitialID = append(json.RawMessage(nil), r.JSONRPC.ID...)
		return
	}
	id := append(json.RawMessage(nil), r.JSONRPC.ID...)
	if idSeen(c, id) {
		c.RetryIDsDistinct = false
	}
	c.RetryIDs = append(c.RetryIDs, id)
}

// idSeen reports whether id is byte-equal to the initial id or any retry id
// already recorded for the chain.
func idSeen(c *Correlation, id json.RawMessage) bool {
	if len(c.InitialID) > 0 && rawEqual(c.InitialID, id) {
		return true
	}
	for _, seen := range c.RetryIDs {
		if rawEqual(seen, id) {
			return true
		}
	}
	return false
}

// rawEqual reports byte equality of two raw JSON values.
func rawEqual(a, b json.RawMessage) bool {
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
