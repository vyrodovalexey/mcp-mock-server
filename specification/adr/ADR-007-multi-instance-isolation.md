---
id: ADR-007
title: Multi-instance isolation — no global state, shared listener, prefix router
status: accepted
date: 2026-09-04
reversibility: HARD — "no globals" is a property of every file; retrofitting it is a rewrite
requirements: MOCK-103, MOCK-107, MOCK-904, MOCK-223, MOCK-502
---

# ADR-007 — Multi-instance isolation

## Context

`MOCK-103`: N independent logical servers in one process, with distinct endpoints/paths and
distinct scenarios. `MOCK-904`: ≥200 of them. `MOCK-107`: the whole thing embeddable in someone
else's test binary — which means **two `mcpmock.Server`s must be able to run in one test binary
simultaneously**, e.g. a parallel test that starts a fresh fleet per subtest.

That last point is what kills the usual Go shortcuts: a package-level `var registry`, a
`prometheus.MustRegister` on `DefaultRegisterer`, a `log.SetOutput`, a global `rand` seed, or a
`sync.Once` that configures OTel globally. Each of them turns two concurrent tests into one
broken test.

`MOCK-223` further requires *deliberate* name collisions across instances — so isolation must be
real, not accidental (two instances must be able to serve a tool with the same name without
interfering).

## Decision

### 1. Zero process-global mutable state

- No package-level `var` that is written after init, anywhere, including `internal/`.
- Prometheus: each `mcpmock.Server` owns a `*prometheus.Registry` created in `New()`. The
  `/metrics` handler serves that registry. `promauto` and `DefaultRegisterer` are banned.
  `WithRegisterer(r)` lets an embedding test supply its own.
- OTel: a `TracerProvider` is held on the `Server`, never installed with `otel.SetTracerProvider`
  unless `WithGlobalOTel()` is explicitly passed by the top-level binary.
- `slog`: a `*slog.Logger` per `Server`, never `slog.SetDefault`.
- Signals, `GOMAXPROCS`, `os.Stdout`: touched **only** by `cmd/mcpmock`, never by the library.
  This is the sharpest line in the design — ADR-011's stdout hijack is a `cmd/` behaviour, and the
  library form of stdio transport takes explicit `io.Reader`/`io.Writer`.

### 2. Instance ownership

```
Server
 ├── registry   atomic.Pointer[routeTable]     // path prefix -> *Instance
 ├── instances  []*Instance                    // stable order, index == id
 ├── listeners  []net.Listener
 ├── control    *control.Server
 └── obs        *obs.Bundle                    // registry, logger, tracer

Instance                       // the tenancy boundary
 ├── name, mountPath, id
 ├── snapshot  atomic.Pointer[Snapshot]        // ADR-014
 ├── mu        sync.Mutex                      // writers only
 ├── journal   *journal.Journal
 ├── sessions, subscriptions, cursors, mrtrRounds   // sharded maps
 ├── key       determinism.Key                 // ADR-002 instance subtree
 ├── metrics   *obs.InstanceMetrics            // pre-resolved label handles
 └── (no goroutines)
```

Instances share exactly three things and nothing else: the process's listeners, the timer wheel,
and the observability bundle. All three are read-mostly and explicitly passed in, never reached
for.

### 3. Listener strategy

Default: **one HTTP listener, path-prefix routing.** 200 instances mounted at `/mock/{name}/mcp`
cost 200 map entries, not 200 sockets, 200 TLS configs and 200 accept loops.

The router is a custom immutable prefix trie in `internal/transport/httpx`, swapped atomically so
the control API can add or remove an instance without restarting the listener. `net/http.ServeMux`
was considered — its Go 1.22 pattern matching is adequate — but it cannot be mutated after serving
begins without building a new mux and racing, and the trie is ~120 lines.

**Exception, `listener: own`.** `MOCK-502` requires "TCP accept refusal" and TLS handshake
failure, and `MOCK-106` allows per-instance client-certificate requirements. Both are
listener-scoped, not request-scoped. An instance may therefore declare `listener: own` and get a
dedicated `net.Listener` (and `tls.Config`). This is opt-in because it does not scale to 200.
Validated at load: `own` listeners ≤ 32 by default, configurable.

Per-instance TLS on the *shared* listener is handled where possible by
`tls.Config.GetConfigForClient` keyed on SNI — which covers differing client-cert policies when
instances have distinct SNI names, and is documented as the recommended pattern.

### 4. Instance identity in observability

Every metric, log line, span and journal record carries `instance`. `obs.InstanceMetrics` holds
**pre-resolved** `prometheus.Counter`/`Observer` handles bound at instance construction, so the
hot path never calls `WithLabelValues` (which takes a lock and allocates).

### 5. Name collisions (`MOCK-223`)

Tool names are scoped to an instance by construction. A scenario may declare
`collideWith: <otherInstanceName>` which copies that instance's generated name sequence into this
one's overlay — an explicit, configured collision, produced by shared *configuration*, never by
shared *state*.

## Options considered

1. **One process per logical server** — rejected: 200 processes, and `MOCK-103` says "in one
   process". Also defeats `MOCK-107`.
2. **Goroutine-per-instance supervisor** — rejected: 200 idle goroutines is not expensive in
   itself, but each one that owns a `time.Ticker` is, and it invites per-instance background work
   that then has to be shut down deterministically. The timer wheel (ADR-012) removes the need.
3. **`context.Context` value-passing of the instance** — rejected: `context` values are the wrong
   tool for a mandatory dependency, and `contextcheck`/`revive` in `.golangci.yml` discourage it.
   The instance is an explicit field on `Exchange`.
4. **Shared listener + prefix trie, with an `own` escape hatch (chosen).**

## Consequences

**Positive.** Two `Server`s coexist in one test binary; `t.Parallel()` works. 200 instances cost
~200 × (Snapshot + journal ring + metric handles). With the default 100 000-record shared budget
divided per instance, journal rings must be sized *per instance* — see the negative below.
Instance addition/removal at runtime is a pointer swap.

**Negative.** Journal memory is now per-instance: 200 × default ring is 200 × the default budget.
The scenario schema therefore expresses journal bounds as a **fleet-wide budget** divided across
instances by default (`journal.maxBytesTotal`, default 256 MiB), with per-instance override. This
is easy to get wrong and is called out in `data-model.md`.

The "no globals" rule is a discipline with no linter behind it. Enforcement: a CI check
(`make globals-check`) using `go vet`-style AST inspection for package-level `var` declarations of
mutable types outside an allowlist. Cheap to write (~80 lines) and catches regressions.

**Forecloses.** Using any third-party library that requires global registration
(`promauto`, `expvar`-style APIs, `otel` global-only exporters). ADR-013's dependency screen
includes "does it demand a global?" as a rejection criterion.
