# TC-028 — External-module embeddability of `mcpmock`

| | |
|---|---|
| **Task** | `TASK-028` |
| **Requirements** | `MOCK-107` (107.3, 107.4, 107.5, 107.6, 107.7), `MOCK-603` (603.3, 603.4 in situ) |
| **Level** | e2e (out-of-module consumer) |
| **Harness** | separate Go module `test/e2e/embed/` with a `replace` directive to the parent working tree; no cluster, local only |
| **Build tag** | `e2e` on the driving tests (`//go:build e2e`) |

## Why this case exists

Every other test in the repository runs *inside* the parent module and therefore
cannot detect the failure modes that only bite an external consumer: an
unexported-but-needed symbol, an `internal/` type leaking into a public
signature, an import-time process-global side effect, a goroutine leaking into
the host's unrelated packages, an implicit `os.Stdout` hijack, or a
dependency-version conflict on a type in the public API (`prometheus.Registerer`).
This case imports `mcpmock`, `journalapi`, `assert` and `scenario` as a genuinely
separate module and exercises the public surface as the gateway's test suite
would.

---

## TC-028.1 — Drive a real request and assert over the journal (`107.3`, `603.3/603.4`)

**Preconditions.** Embed module builds against the parent via `replace`.

**Given** a `Server` started with `StartTest` on an ephemeral loopback port
(`WithAddr("127.0.0.1:0")`, seed fixed for reproducibility).
**When** the consumer POSTs a valid `tools/list` JSON-RPC request (with a valid
`params._meta.{protocolVersion,clientCapabilities}`) to the default instance's
`URL()` using plain `net/http` — nothing beyond the four public packages + stdlib.
**Then**
- the HTTP round trip succeeds (status 200, non-error JSON-RPC envelope), and
- `assert.T(t, inst.Journal()).WhereMethod("tools/list").AssertRequestCount(sel, 1)`
  passes — i.e. exactly one `tools/list` record is present, and
- `assert.T(...).AssertNoHeader("X-Nonexistent-Token")` passes (credential-safety
  form, `603.3`), and
- `assert.FromReader` over an NDJSON export loads a `View` with no server running
  (`603.4`).

**Observable outcome.** All assertions pass; the journal contains the driven
request. **Cleanup:** `StartTest` registers `Close` via `t.Cleanup`; ephemeral
port, no leak. **Runtime:** < 1 s.

---

## TC-028.2 — Startup within the `MOCK-107` 200 ms budget, measured from outside (`107` budget)

**Given** a warmed process (one throwaway `New`/`Close` to amortise the
once-per-process schema compile).
**When** the consumer measures wall-clock `New(WithAddr("127.0.0.1:0")) + Start`
across N runs from outside the module.
**Then** the worst observed start is `< 200 ms`.
**Observable outcome.** Logged `mean`/`worst` against budget; fails if worst
exceeds 200 ms. **Cleanup:** each server closed in-loop. **Runtime:** < 1 s.

---

## TC-028.3 — Two independent servers coexist under `t.Parallel()` (`107.4`)

**Given** two `Server`s started in separate parallel subtests, each with its own
ephemeral port, seed, logger and Prometheus registry.
**When** both serve a request concurrently.
**Then** neither panics (no duplicate Prometheus registration), each journals
only its own request, and their URLs differ.
**Observable outcome.** Both subtests green; no `duplicate metrics collector
registration` panic. **Cleanup:** per-subtest `Close`. **Runtime:** < 1 s.

---

## TC-028.4 — No goroutine leaks into the host (`107.7`)

**Given** `goleak` installed as the embed module's `TestMain` verifier and a test
that starts a server via `StartTest`.
**When** the test starts a server, drives a request, and returns (letting
`t.Cleanup` run `Close`).
**Then** `goleak.VerifyTestMain` reports no leaked goroutine attributable to
`mcpmock`.
**Observable outcome.** `TestMain` exits 0. A leak would fail the *whole* embed
package, exactly as it would poison a host suite. **Runtime:** < 1 s.

---

## TC-028.5 — Nothing written to `os.Stdout` on import or normal operation (`107` / ADR-011)

**Given** the embed module imports the root `mcpmock` package (which transitively
loads its `init()`s) but never imports `cmd/mcpmock`.
**When** the consumer redirects the process `os.Stdout` to a pipe, then
constructs, starts, drives and closes a `Server`.
**Then** zero bytes are read from the captured pipe — the stdout hijack is not
installed implicitly, and normal operation writes only to the caller's own
writers.
**Observable outcome.** Captured stdout is empty. **Cleanup:** `os.Stdout`
restored in `t.Cleanup`. **Runtime:** < 1 s.

---

## TC-028.6 — No `internal/` type appears in any public signature the consumer must name (`107.3`)

**Given** the embed module (compiled outside the parent).
**When** the consumer writes code that names every public type it must use —
`mcpmock.Control`, `mcpmock.InstanceControl`, `mcpmock.InstanceInfo`,
`mcpmock.SeedInfo`, `mcpmock.Health`, `mcpmock.JournalInfo` — and calls every
method on `Control`/`InstanceControl`, binding each return value to a
consumer-named variable.
**Then** the embed module **compiles**. If any public method forced the consumer
to name an `internal/` type, compilation from outside the module would fail with
"use of internal package not allowed".
**Observable outcome.** Compilation is the assertion (`107.3`). Reinforced by a
mechanical `go list` transitive-import check on the built test binary that no
consumer *needs* to import an `internal/` path directly (they may appear
transitively, which is legal — the property is that none is *nameable*).
**Runtime:** compile-time.

---

## TC-028.7 — `assert`+`journalapi`-only graph pulls no server/Prometheus/OTel (ADR-001 §6.2, RISK-07)

**Given** a source file in the embed module that imports **only** `journalapi`
and `assert`.
**When** `go list -deps` is run over it.
**Then** the transitive module/package graph contains **no** internal server
package, **no** `github.com/prometheus/client_golang`, and **no**
`go.opentelemetry.io/otel`.
**Observable outcome.** The graph is recorded as an artifact
(`.opencode/output/test-artifacts/`) rather than asserted only in prose; the test
fails if a forbidden module appears. **Runtime:** < 2 s (runs `go list`).

---

## TC-028.8 — `Server.Registerer()` satisfiable by an external Prometheus consumer

**Given** the embed module declaring its own `require github.com/prometheus/client_golang`.
**When** the consumer calls `srv.Registerer()` and registers its own collector,
and passes its own `*prometheus.Registry` via `WithRegisterer`.
**Then** the module resolves a single Prometheus version (no `replace`/version
conflict) and the calls type-check and run.
**Observable outcome.** Build + run succeed; `go mod graph` shows one
`prometheus/client_golang` version. **Runtime:** < 1 s.
