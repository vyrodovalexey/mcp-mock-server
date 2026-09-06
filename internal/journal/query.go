package journal

// This file adds the query half of internal/journal (TASK-012): filter execution
// over the stored records through the public [journalapi.Selector] contract
// (MOCK-602). It reads the ring through the same lock-free snapshot the storage
// engine exposes, so a query never blocks a writer.

import (
	"github.com/vyrodovalexey/mcp-mock-server/journalapi"
)

// Query filters the journal by one or more selectors AND-combined and returns
// the matching records in global [journalapi.Record.Seq] order (MOCK-602.4,
// MOCK-602.7). A record is returned only if it matches every selector, so
// passing sel1 then sel2 selects the same set as sel2 then sel1 — matching is a
// pure conjunction ([journalapi.Selector.Matches]). Passing no selectors returns
// every record in Seq order.
//
// The MOCK-602.4 filter dimensions — method, name, since/until, correlationId,
// traceId, chain, transport, era, status, faultRule — are all fields of
// [journalapi.Selector]; this function does not reimplement matching, it applies
// the contract's own predicate, so a filter added to the selector is available
// here with no change.
//
// Query reads a non-consistent snapshot (it never disables writes), which is the
// intended trade for never blocking a hot writer; a caller that needs a
// guaranteed-coherent read under a hot writer uses [Ring.ConsistentView] and
// filters the returned view instead. The result slice is owned by the caller.
func (r *Ring) Query(selectors ...journalapi.Selector) []journalapi.Record {
	return filterView(r.View(), selectors)
}

// QueryConsistent is [Ring.Query] taken over a consistent snapshot: writes are
// briefly disabled for the copy so no writer intervenes and no torn slot is
// skipped (ADR-005 ?consistent=true). It is the read a caller uses when evidence
// completeness matters more than never briefly gating the hot path.
func (r *Ring) QueryConsistent(selectors ...journalapi.Selector) []journalapi.Record {
	return filterView(r.ConsistentView(), selectors)
}

// filterView applies every selector to v in turn and returns the surviving
// records' snapshot in Seq order. Applying selectors sequentially is exactly the
// AND-combination MOCK-602.4 requires, because each [journalapi.View.Filter]
// narrows the set and [journalapi.Selector.Matches] is a pure conjunction. The
// view is already in Seq order (a View sorts on construction), so the returned
// snapshot is too.
func filterView(v journalapi.View, selectors []journalapi.Selector) []journalapi.Record {
	for _, s := range selectors {
		v = v.Filter(s)
	}
	return v.Snapshot()
}
