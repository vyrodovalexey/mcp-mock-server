---
id: ADR-006
title: Transport abstraction — Exchange and ResponseSink, era-blind transports
status: accepted
date: 2026-09-04
reversibility: MEDIUM — internal interface, but every handler is written against it
requirements: MOCK-102, MOCK-208, MOCK-210, MOCK-212, MOCK-251, MOCK-256, MOCK-302, MOCK-502
---

# ADR-006 — Transport abstraction

## Context

`MOCK-102` requires `stdio` and `streamable-http` in one binary. They differ profoundly:

| | streamable-http | stdio |
|---|---|---|
| Framing | HTTP request/response; SSE for streams | newline-delimited JSON on one duplex channel |
| Correlation | one connection per exchange | **everything multiplexed on one channel** (`MOCK-256`) |
| Headers | real HTTP headers, validated (`MOCK-204`) | none |
| Cancellation | client disconnect | `notifications/cancelled` or EOF |
| Faults | connection reset, truncation, TLS failure (`MOCK-502`) | process exit (`MOCK-508`) |

Writing each protocol handler twice is unacceptable: the modern era alone has ~10 methods
× era-probe variants × fault variants. Any duplication guarantees divergence between the two
transports, and a mock that behaves differently over stdio than over HTTP is worse than useless
— it produces false hub bugs.

## Decision

Handlers are written **once**, against two interfaces owned by `internal/engine`.

```go
// Inbound, transport-neutral.
type Exchange struct {
    Ctx       context.Context
    Instance  *instance.Instance
    Snapshot  *instance.Snapshot     // captured once, immutable
    Transport Kind                   // stdio | http
    Envelope  jsonrpc.Envelope       // id (raw), method, params (RawMessage)
    Raw       []byte                 // exact received bytes
    HTTP      *HTTPContext           // nil for stdio: method, path, headers (ordered), TLS state
    Peer      string
    Principal authz.Principal
    Rand      func() *rand.Rand      // lazy, ADR-002
    Clock     determinism.Clock
}

// Outbound. The single point where a response becomes bytes.
type ResponseSink interface {
    // Shape is decided once, before the first Send. MOCK-208.
    Begin(shape Shape, hdr ResponseHeader) error   // Shape: JSONOnce | SSEStream
    Send(f Frame) error                            // result, error, notification, keepalive, raw
    Close(reason CloseReason) error                // Complete | ClientGone | Fault | Abrupt
    Flush() error
}
```

`Frame` is a small sum type: `FrameResult`, `FrameError`, `FrameNotification`, `FrameKeepAlive`,
`FrameRawBytes` (the escape hatch that makes §5 protocol faults expressible — invalid JSON,
truncated JSON, duplicate ids — without polluting the typed path).

**Transports are era-blind.** `internal/transport/*` must not import `internal/modern`,
`internal/legacy` or `internal/mrtr`; enforced by `make deps-check`. A transport's job is
exactly: decode bytes into `Exchange`, provide a `ResponseSink`, honour `Close`.

Per-transport sink implementations:

- **`httpx.jsonSink`** — buffers one result, writes `Content-Type: application/json`.
- **`httpx.sseSink`** — writes `Content-Type: text/event-stream`, `Cache-Control: no-cache`,
  `Connection: keep-alive`, and **`X-Accel-Buffering: no`** (`MOCK-208`, mandatory). Frames go
  through a buffered channel consumed by the handler goroutine; `http.ResponseController.Flush`
  after each event. Event ids are minted deterministically (ADR-002 `"eventid"` domain) so
  `Last-Event-ID` replay (`MOCK-301`) is reproducible.
- **`stdio.muxSink`** — every frame is tagged with the originating JSON-RPC id, and, for
  subscription traffic, with `io.modelcontextprotocol/subscriptionId` (`MOCK-251`). All sinks in
  the process feed **one** writer goroutine over a single channel, which is what guarantees the
  correct interleaving `MOCK-256` demands. Writes are never split across a newline boundary.

`Shape` selection (`MOCK-208`: single JSON vs SSE, per method / per tool / by seeded probability)
happens in `engine` before `Begin`, using the ADR-002 `"shape"` domain. Transports do not choose.

Server-initiated requests (`MOCK-302`: the legacy era sends real `sampling/createMessage` etc. and
awaits a client response) invert the direction. This is modelled as `engine.Initiator`:

```go
type Initiator interface {
    Call(ctx context.Context, method string, params any) (json.RawMessage, error)
}
```

`httpx` implements it over the legacy GET SSE stream + the client's POST response, correlating on
a deterministically minted id; `stdio` implements it over the mux. Handlers use `Initiator` and
never know which.

## Options considered

1. **Two handler trees** — rejected: guaranteed divergence, doubles §2/§3/§5 surface area.
2. **`http.ResponseWriter` as the universal abstraction, with a fake for stdio** — rejected: forces
   stdio to fabricate HTTP semantics (status codes, header canonicalisation) that `MOCK-204` then
   has to un-fabricate. It also makes `MOCK-207` (reject GET/DELETE with 405) meaningless in
   stdio.
3. **`io.Writer` + a codec** — rejected: too thin. Loses `Begin`/`Close(reason)`, which is where
   `MOCK-212` (closure = cancellation) and `MOCK-254` (graceful vs abrupt closure) live.
4. **`Exchange` / `ResponseSink` (chosen).**

## Consequences

**Positive.** One handler tree. `MOCK-254` graceful/abrupt closure and `MOCK-502` transport faults
are expressed as `CloseReason` and `FrameRawBytes` — transport-specific *implementations* of
transport-neutral *intent*. Adding a third transport later (WebSocket) touches no handler.

**Negative.** `FrameRawBytes` is a hole in the type system; it is exactly the hole §0.4 requires,
but it means a handler can emit garbage by accident. Mitigated by restricting `FrameRawBytes`
construction to `internal/fault` (unexported constructor + an `engine.faultOnly` marker type).

The abstraction cost on the hot path is one interface call per response. Measured, not assumed:
`BenchmarkSinkDispatch` gates it at < 5 ns.

**Forecloses.** Zero-copy `sendfile`-style response paths. Irrelevant here.
