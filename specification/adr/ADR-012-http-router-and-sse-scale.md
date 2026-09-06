---
id: ADR-012
title: Stdlib net/http with a custom prefix router, shared timer wheel, and channel-fed SSE sinks
status: accepted
date: 2026-09-04
reversibility: MEDIUM — swapping the HTTP stack later is a contained but real rewrite of internal/transport/httpx
requirements: MOCK-102, MOCK-103, MOCK-208, MOCK-255, MOCK-901, MOCK-903, MOCK-904
---

# ADR-012 — HTTP router and SSE strategy at 20 000 streams

## Context

Two numbers drive this:

- `MOCK-901`: ≥20 000 `tools/call` rps on 4 vCPU, trivial handler, journaling off.
- `MOCK-903`: ≥20 000 concurrent open SSE streams per instance.
- plus `MOCK-255`: SSE keep-alive comment lines **at a configurable interval**, disableable.

The naive SSE implementation is:

```go
tick := time.NewTicker(keepAlive)      // per stream
defer tick.Stop()
for { select { case f := <-frames: ...; case <-tick.C: writeComment(); case <-ctx.Done(): return } }
```

At 20 000 streams that is 20 000 `time.Ticker`s. Each is a runtime timer heap entry; Go's timer
implementation is per-P and good, but 20 000 timers firing every 15 s means a steady wake-up
storm, 20 000 timer allocations, and — more importantly — it makes the *idle* cost of a stream
non-zero, which is exactly what `MOCK-903` measures.

The second question is whether `net/http` can hold 20 000 streams at all, and whether it can do
20 000 rps on 4 vCPU.

## Decision

### 1. Stdlib `net/http`. No third-party HTTP framework.

Justification against the alternatives is in "Options considered". The short version: the
bottleneck at 20 000 rps for a trivial handler is not the router, and every alternative costs a
dependency in the hub's module graph (`MOCK-107`).

### 2. Custom prefix router, not `ServeMux`

`internal/transport/httpx.Router` is an immutable radix trie of mount paths, held in
`atomic.Pointer[routeTable]`. Reasons: instances are added and removed at runtime by the control
API (`MOCK-702`, `MOCK-103`), and `http.ServeMux` cannot be mutated after `ListenAndServe` without
racing. Lookup is O(path length), allocation-free, and does not sort or lock. ~120 lines.

Go 1.22 `ServeMux` patterns are used **only** for the control and observability listeners, which
are static.

### 3. Goroutine and memory budget for 20 000 SSE streams

Per stream, with this design:

| Cost | Amount |
|---|---|
| `net/http` connection read goroutine | 1 goroutine, ~8 KiB initial stack (grows to ~16 KiB typical) |
| our handler goroutine | 1 goroutine, ~8 KiB |
| `bufio.Reader` on the conn | 4 KiB (`http.Server` default) |
| `bufio.Writer` on the conn | 4 KiB |
| socket buffers (kernel) | ~4–16 KiB, not Go heap |
| frame channel, cap 16 × pointer | ~256 B |
| stream record (ids, filter, snapshot ptr) | ~512 B |
| **total, user space** | **≈ 25 KiB/stream → ≈ 500 MiB at 20 000** |

Plus ~40 000 goroutines. Go handles 40 000 goroutines comfortably; the scheduler cost is
negligible when they are all blocked on I/O or channel receive.

Tuning applied:
- `http.Server{ ReadHeaderTimeout: 10s, IdleTimeout: 120s, WriteTimeout: 0, MaxHeaderBytes: 1<<16 }`.
  `WriteTimeout` **must** be 0 for streams; per-frame deadlines are set with
  `http.ResponseController.SetWriteDeadline` instead, which is the correct tool and exists since
  Go 1.20.
- `ReadBufferSize`/`WriteBufferSize` are not directly settable on `http.Server`; where the
  measured memory matters, the listener wraps conns to reduce buffer pressure. **This is measured
  in Phase 6, not assumed.**
- `GOMEMLIMIT` set from the container limit in `cmd/mcpmock` (`deployment.md`).
- File descriptors: 20 000 streams needs `nofile` ≥ 24 576. Set in the Helm chart's guidance and
  asserted at startup with a warning if `RLIMIT_NOFILE` is below `2 × expected streams`.

The **resource envelope is stated honestly**: `MOCK-903` at 20 000 streams needs ≈ 1 GiB memory
limit and ≈ 2 vCPU idle. This is in `deployment.md §5` and is not hidden.

### 4. No per-stream timers — one hierarchical timer wheel

`internal/sched.Wheel`: single goroutine, 10 ms tick, hierarchical buckets (10 ms / 1 s / 1 min).
Registering a keep-alive is inserting into a bucket: O(1), no allocation after warm-up. On fire,
the wheel **enqueues a `FrameKeepAlive` onto the stream's channel** and re-arms. It never writes
to a socket, so one slow client cannot stall the wheel.

Same wheel serves: SSE keep-alives (`MOCK-255`), timed notifications (`MOCK-253`), progress
notification intervals (`MOCK-210`), catalogue drift "after N seconds" (`MOCK-227`), MRTR state
expiry sweeps, and cursor expiry (`MOCK-231`).

Backpressure: if a stream's channel is full, the wheel **drops** the keep-alive and increments
`mcpmock_sse_frames_dropped_total{reason="slow_consumer"}`. Blocking would let one hub bug freeze
every stream in the process.

### 5. Hot path for `MOCK-901`

`tools/call` with a trivial echo handler and journaling off must not allocate meaningfully:

- Response is assembled from precomputed byte fragments held in `Snapshot` (the constant prefix
  `{"jsonrpc":"2.0","id":` , the constant result envelope, the `resultType`), with only the id and
  the echoed argument copied in. No `json.Marshal` of a struct on this path.
- `sync.Pool` of `[]byte` write buffers, sized 4 KiB.
- Pre-resolved Prometheus handles (ADR-007).
- Histogram observation is one `atomic` bucket increment (`prometheus.Histogram` native path).
- No `fmt.Sprintf`, no `time.Now()` beyond one per request for the duration histogram.

Estimated per-request budget on 4 vCPU: ~10 µs of user CPU → theoretical ~400 k rps single-core,
leaving generous headroom for `net/http`'s own ~15–25 µs/request. 20 000 rps on 4 vCPU is a
comfortable target for stdlib `net/http` with keep-alive; the risk is not CPU but connection
handling, so the k6 profile pins `--vus` with keep-alive on and reports both.

**This is an estimate, not a measurement.** `test/perf/` gates it, and `implementation-plan.md`
Phase 1 includes an early throughput smoke test specifically so the assumption is falsified early
rather than at the end.

## Options considered

| Option | Verdict |
|---|---|
| **stdlib `net/http` (chosen)** | Zero dependency, HTTP/1.1 + HTTP/2 + TLS + mTLS for free, `http.ResponseController` covers flush/hijack/deadlines. |
| `fasthttp` | Rejected: no HTTP/2, non-standard `Request` type that would infect the whole codebase, and a hard incompatibility with `crypto/tls` client-cert plumbing (`MOCK-106`). Its throughput advantage is irrelevant at 20 000 rps. |
| `gnet` / `evio` (epoll event loop) | Rejected: would remove the per-stream goroutine and cut memory ~4×, which is genuinely attractive for `MOCK-903`. But we would then have to implement HTTP/1.1 parsing, chunked encoding, TLS, and mTLS ourselves — for a *test tool whose correctness is the product*. Wrong trade. Revisit only if measurement shows 20 000 streams is unreachable, which the budget above says it is not. |
| `chi` / `gorilla/mux` / `echo` / `gin` | Rejected: a dependency in the hub's module graph to solve a routing problem that is 120 lines here, and none of them support atomic route-table replacement well. |
| `ServeMux` (Go 1.22 patterns) | Rejected for the MCP listener (immutable after serve); **used** for control/obs listeners. |

## Consequences

**Positive.** No HTTP dependency. TLS/mTLS (`MOCK-106`) is `crypto/tls` configuration.
`MOCK-903` idle cost is one channel + two goroutines, with zero timers. The timer wheel is reused
by five other requirements, so its cost is amortised.

**Negative.** ≈ 500 MiB–1 GiB for 20 000 streams is a real number that must appear in the Helm
chart's default `resources` guidance and in the `MOCK-903` acceptance criterion. We do not get to
pretend it is small. `http.ResponseController.Hijack` (needed for `MOCK-502` connection reset) is
unavailable under HTTP/2 — so RST-style faults require the listener to be HTTP/1.1, which the
scenario schema enforces (`transport.http.forceHTTP1: true` is implied by any `transport` fault
with `kind: reset`, checked at validation time).

**Forecloses.** Sub-100 MiB memory at 20 000 streams. Accepted.
