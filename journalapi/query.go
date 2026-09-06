package journalapi

// Query is a journal read request: a [Selector] plus paging and read-mode
// controls (data-model.md §3.2). It is the single argument the control-plane
// journal operations take, so the same value describes a query issued
// in-process, over HTTP, or from the CLI (ADR-015 one-interface-three-frontends).
//
// A Query embeds [Selector], so every MOCK-602.4 filter dimension — method,
// name, transport, era, since/until, correlationId (chain), traceId, status,
// faultRule — is expressed directly on the Query. The paging fields bound the
// returned window without materializing the whole journal.
//
// Stability: v0. Fields are additive.
type Query struct {
	// Selector is the AND-combined filter (MOCK-602.4). The zero Selector
	// matches every record.
	Selector
	// After is a [Record.Seq] cursor: only records with Seq strictly greater
	// than After are returned. Zero means "from the beginning". It pairs with
	// [Page.NextAfter] to page forward through a large journal.
	After uint64
	// Limit bounds the number of records returned in one page; zero means the
	// server's default. A negative value is treated as zero.
	Limit int
	// Follow requests a long-lived stream that keeps emitting new records as
	// they arrive (MOCK-602.5). Phase 1 does not implement following; a Follow
	// query returns the currently-held records and completes. It is carried on
	// the contract now so a follower does not change the query shape later.
	Follow bool
	// Consistent requests a torn-read-free snapshot taken with writers briefly
	// quiesced (ADR-005 ?consistent=true), at the cost of a brief write pause.
	// When false the read is a lock-free snapshot that never blocks a writer.
	Consistent bool
}

// Page is one page of a journal query result (contracts/control-api.openapi.yaml
// #/components/schemas/JournalPage). It carries the matched records in
// [Record.Seq] order plus the cursor and counters a paging caller needs.
//
// Stability: v0.
type Page struct {
	// Records are the matched records in ascending [Record.Seq] order.
	Records []Record `json:"records"`
	// NextAfter is the cursor to pass as [Query.After] to fetch the next page;
	// zero when this page is the last. It is the highest Seq in Records when the
	// page filled to the limit.
	NextAfter uint64 `json:"nextAfter,omitempty"`
	// Total is the number of records this query matched across the whole
	// journal, independent of the page limit.
	Total int `json:"total,omitempty"`
	// Dropped is the number of records lost to ring overflow before this page,
	// so a reader can see that evidence is missing (ADR-005).
	Dropped uint64 `json:"dropped,omitempty"`
}
