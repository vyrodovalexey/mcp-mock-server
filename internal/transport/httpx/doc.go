// Package httpx is the streamable-http MCP transport and prefix router
// (MOCK-102/MOCK-103, governed by ADR-012). It binds one stdlib net/http
// listener that fans out to many instances through a lock-free prefix router, so
// ≥200 instances share one port, and it drives each request through the
// era-blind engine pipeline without interpreting any wire content.
//
// # Wire-blindness (architecture.md §6.1 rule 3)
//
// The transport imports internal/engine but NOT internal/modern, internal/legacy
// or internal/mrtr. Framing (HTTP methods, status codes, SSE events, headers) is
// this package's vocabulary; MCP wire vocabulary (method names, error codes,
// resultType) belongs to internal/wire and never appears here. This keeps the
// transport reusable across eras and keeps GAP-003 ratification a one-package
// change.
//
// # Concurrency model (architecture.md §7.1, ADR-012)
//
// A non-streaming POST spawns ZERO extra goroutines: it is decoded, driven
// through the pipeline, and written back on net/http's own connection goroutine.
// The only goroutine this package owns process-wide is ONE shared timer wheel
// (see [timerWheel]); there is deliberately no per-stream time.Ticker, because
// MOCK-903's ≥20 000 concurrent SSE streams would make 20 000 tickers a wake-up
// storm and give every idle stream a non-zero cost. The wheel enqueues
// keep-alives onto each stream's small buffered channel and never writes a
// socket itself, so one slow client cannot stall it.
//
// # Per-stream cost (MOCK-903)
//
// An SSE stream costs, per ADR-012 §3's envelope: net/http's read goroutine plus
// one handler goroutine (two ~8 KiB stacks), the connection's bufio reader and
// writer, one buffered frame channel (cap 16, ≈ 256 B) and a small stream
// record — ≈ 25 KiB/stream, ≈ 500 MiB at 20 000. ADR-012 states this is an
// ESTIMATE, not a measurement, and rates MOCK-903 the #2 technical risk; the
// package benchmark reports the MEASURED per-stream figure so TASK-029 can
// falsify the estimate. Phase 1 emits only buffered JSON; the SSE framing and
// the mandatory X-Accel-Buffering header (MOCK-208) exist now so Phase 2 fills
// content into an already-measured, already-correct envelope.
//
// # Statelessness (MOCK-207)
//
// GET and DELETE on an MCP endpoint return 405. Mcp-Session-Id and Last-Event-ID
// are ignored — never read into a decision — and no session id is minted in
// modern mode. These are correctness controls: a mock that honored a session
// header would hide a real hub statelessness defect.
//
// # Cancellation (MOCK-212)
//
// The request context is r.Context() threaded verbatim as [engine.Exchange.Ctx];
// it is never recreated. A client disconnect cancels it, the engine observes the
// cancellation at stages 8-9, closes the sink as client-gone and journals the
// elapsed time.
//
// # Header fidelity (MOCK-601.2)
//
// Headers reach the journal in wire order with original casing and duplicates.
// Because net/http canonicalises header names, a per-connection wrapper tees the
// raw request head and the handler parses it (see rawheaders.go), so the journal
// records exactly what was sent rather than net/http's rewrite.
//
// # Shutdown contract (MOCK-508)
//
// [Server.Shutdown] stops accepting, drains in-flight requests bounded by its
// context, stops the timer wheel, and returns; it does not hang past the
// deadline on an open stream. [Server.Close] is the immediate escape hatch.
package httpx
