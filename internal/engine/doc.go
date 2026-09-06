// Package engine is the transport-neutral spine of mcpmock: the [Exchange] /
// [Sink] abstraction (ADR-006) and the nine-stage request pipeline
// (architecture.md §5) that every request from every transport flows through.
//
// It owns the two interfaces every handler is written against — an inbound
// [Exchange] and an outbound [Sink] — so a method handler is written once and
// serves stdio and streamable-http identically (MOCK-102). Transports
// (TASK-019/020) decode bytes into an [Exchange], supply a [Sink], and honor
// [Sink.Close]; they never interpret wire content and never see a
// transport-specific type reach a handler.
//
// # The nine stages (architecture.md §5)
//
// [Pipeline.Handle] runs these stages in this fixed order for every request,
// including the pass-through stages Phase 1 has not yet filled. The order is
// load-bearing: ADR-002 consumes the per-request RNG in stage order, so a new
// random decision is APPENDED to a stage, never inserted, or every downstream
// golden file shifts (ADR-002 rule 2). Each stage documents its RNG draw count.
//
//  1. Decode        parse the JSON-RPC envelope, capture the immutable Snapshot
//     once (ADR-014), and install the LAZY per-request key / RNG /
//     clock accessors. params/_meta are decoded only when the
//     journal needs them. Draws: 0.
//  2. EraResolve    resolve the protocol era. Phase 1 is modern-only, a
//     pass-through recording era "modern". Draws: 0.
//  3. Authorize     authorization. Phase 1 is `none`, a pass-through. Draws: 0.
//  4. Validate      _meta / header / version / capability validation. This is
//     the SEAM TASK-017 fills: the pipeline calls the Snapshot's
//     MetaValidator (see [Snapshot.MetaValidator]); Phase 1's
//     default validator accepts every request. Draws: 0.
//  5. FaultPre      request-phase fault hook. Phase 1 no-op. Draws: 0.
//  6. Dispatch      the method handler from the [Registry] produces a result or
//     an error. Draws: handler-defined, appended after stages 1-5.
//  7. FaultPost     response/stream-phase fault hook. Phase 1 no-op. Draws: 0.
//  8. Encode        turn the result or error into frames and emit them through
//     the [Sink]. The wire error object is produced in exactly one
//     place, [encodeError] in emit.go, so Phase 9 fault codes and
//     genuine errors share one encoder. Draws: 0.
//  9. Journal       commit a fully-faithful record (MOCK-601), including a
//     cancellation record with elapsed time (MOCK-212). Draws: 0.
//
// Stage 4 is deliberately a distinct, ordered stage rather than logic inlined
// into decode or dispatch. TASK-017 inserts its _meta validation by supplying a
// [MetaValidator] on the Snapshot; it does not restructure this pipeline. If a
// later task has to move a stage boundary to insert itself, this task is not
// done.
//
// # Concurrency model (architecture.md §7)
//
// The pipeline adds ZERO goroutines per request: a request is handled entirely
// on the caller's goroutine (net/http's connection goroutine for HTTP, a stdio
// worker for stdio). There is no `go func()` on the request path and no
// per-request mutex on shared state — the architect warns a
// per-request-goroutine-plus-mutex design will not reach MOCK-901's 20 000 rps.
// The only per-request allocation is the one the journal makes when journaling
// is ON (ADR-005); with journaling OFF the hot path allocates nothing it can
// avoid. The engine holds no process-global mutable state (ADR-007); every
// instance owns its own [Registry] handle, journal ring and metrics.
//
// The configuration a request reads is the immutable [Snapshot] captured once
// at stage 1 and carried on the [Exchange] through every stage and handler
// (ADR-014). A control-API mutation that lands mid-request therefore cannot
// produce a half-old/half-new response: stages 2-9 read the stage-1 snapshot,
// never a fresh Load.
//
// # Context and cancellation (MOCK-212)
//
// context.Context enters exactly once, at the transport boundary, as
// [Exchange.Ctx], and is threaded through every stage and every handler as the
// first parameter (contextcheck, .golangci.yml:50). It is never recreated
// mid-pipeline and never substituted with context.Background.
//
// Closure of a response stream is cancellation: a transport cancels
// [Exchange.Ctx] when the client disconnects (HTTP) or on stdin EOF / a cancel
// notification (stdio). The pipeline observes ctx.Err() at stages 8 and 9 and,
// when it is non-nil, stops work, closes the [Sink] with [CloseClientGone], and
// commits a journal record whose CancelPart carries the elapsed time from
// request start (MOCK-212). A latency-holding handler (TASK-018's sleep tool)
// selects on ctx.Done() rather than sleeping through cancellation.
//
// # Determinism (§0.1, ADR-002)
//
// Same config + same seed ⇒ byte-identical responses. The per-request key uses
// the exact ADR-002 call shape —
// instanceKey.Derive(DomainRequest, method, rawID, sha256(canonicalBody)) — but
// is derived LAZILY: stage 1 installs a sync.OnceValue accessor, so a request
// that draws no randomness and reads no clock pays neither the canonical body
// hash nor the HMAC (MOCK-901, ADR-002 option 4). Response bytes are produced
// through internal/jsonrpc's byte-stable encoder ([jsonrpc.Response.Encode]);
// the pipeline never calls time.Now() or iterates a map on the response path.
//
// # Wire containment (ADR-019)
//
// No wire literal — method name, error code, resultType value — appears in this
// package; every such value comes from internal/wire. The single error encoder
// in emit.go is the only place a JSON-RPC error object is constructed.
package engine
