---
id: ADR-015
title: One Control interface, three front-ends (HTTP, unix socket, in-process); loopback + token auth
status: accepted
date: 2026-09-04
reversibility: MEDIUM — the Control Go interface is public (HARD); the HTTP shape is versioned (MEDIUM)
requirements: MOCK-104, MOCK-107, MOCK-602, MOCK-702
---

# ADR-015 — Control plane

## Context

`MOCK-104`:

> MUST expose a **control API** (HTTP, separate port) for runtime mutation and inspection, and
> MUST expose **the same operations** through a CLI for stdio mode.

"The same operations" is the load-bearing phrase. Two hand-written surfaces drift within weeks.

`MOCK-107` adds a third consumer: an embedded hub test needs to mutate the mock **without an HTTP
round trip** (and possibly without any listener at all, e.g. in a sandboxed test environment).

And in stdio mode there is a bootstrapping problem: the CLI must reach a *running* mock whose
stdin/stdout are owned by the hub. It cannot use them.

## Decision

### One interface, defined once

```go
// package mcpmock
type Control interface {
    Instances(ctx context.Context) ([]InstanceInfo, error)
    Instance(ctx context.Context, name string) (InstanceInfo, error)
    AddInstance(ctx context.Context, spec scenario.InstanceSpec) (InstanceInfo, error)
    RemoveInstance(ctx context.Context, name string) error

    SetEra(ctx context.Context, name string, era string) error
    PatchCatalogue(ctx context.Context, name string, p []scenario.CataloguePatch) error
    RegenerateCatalogue(ctx context.Context, name string, params scenario.CatalogueParams) error

    Faults(ctx context.Context, name string) ([]FaultStatus, error)
    SetFaults(ctx context.Context, name string, rules []scenario.FaultRule) error
    PatchFault(ctx context.Context, name, ruleID string, p scenario.FaultPatch) error
    ArmFault(ctx context.Context, name, ruleID string, times int) error

    Notify(ctx context.Context, name string, n scenario.NotificationSpec) error
    CloseStreams(ctx context.Context, name string, sel StreamSelector, mode CloseMode) error
    RotateCredentials(ctx context.Context, name string, spec scenario.CredentialSpec) error

    Journal(ctx context.Context, name string, q journalapi.Query) (journalapi.Page, error)
    JournalStream(ctx context.Context, name string, q journalapi.Query) (iter.Seq2[journalapi.Record, error], error)
    Correlations(ctx context.Context, name string, q journalapi.Query) ([]journalapi.Correlation, error)
    ClearJournal(ctx context.Context, name string) error

    Seed(ctx context.Context) (uint64, error)
    Health(ctx context.Context) (Health, error)
}
```

Three implementations, one of them trivial:

| Front-end | Implementation | Used by |
|---|---|---|
| **in-process** | `*server.controller` — the real one. | `MOCK-107` embedded tests; `Server.Control()`. |
| **HTTP** | `internal/control.Handler` wraps the in-process controller; `internal/control.Client` implements `Control` over HTTP. | `MOCK-104` HTTP control API; `mcpmock ctl --url`. |
| **unix socket** | The same HTTP handler and client over `net.Listen("unix", …)`. | `MOCK-104` "CLI for stdio mode". |

`mcpmock ctl` is a thin CLI over `Control`. Because the CLI holds a `Control` *interface*, the
same code path serves `--url https://…`, `--socket /run/mcpmock.sock`, and (in tests) an
in-process controller. **Adding an operation means adding one method and one route; the CLI and
the HTTP client are generated from the same route table**, so they cannot drift.

### Solving stdio bootstrapping

In stdio mode a control listener is started by default on a **unix domain socket**.

#### The documented path  (`MOCK-104.4`; specified in full by AMEND-7)

```
${XDG_RUNTIME_DIR}/mcpmock-${pid}.sock     # if XDG_RUNTIME_DIR is set, non-empty and absolute
/tmp/mcpmock-${pid}.sock                   # otherwise
```

**This is the "documented path" that `MOCK-104.4` refers to.** `${pid}` is the decimal process id
of the mcpmock process. The path is deterministic given the environment and the pid, so a harness
that knows the pid can compute it without parsing stderr — but parsing stderr is the supported
route, because the fallbacks below can change the answer.

| Concern | Rule |
|---|---|
| **Override** | `--control-socket <path>` (highest precedence), else `MCPMOCK_CONTROL_SOCKET` env var, else the default above. An explicit override is used **verbatim**: no pid is appended, and the length/fallback rules below are *not* applied — an over-long or unwritable explicit path is a **startup error**, never silently relocated. Surprising the operator is worse than failing. |
| **Disable** | `--no-control` starts no control listener at all. `--control-socket ""` is an error, not a synonym for disable. |
| **Directory** | The parent directory must already exist. mcpmock creates the *socket*, never the directory — creating directories under `$XDG_RUNTIME_DIR` invites permission surprises. Missing parent ⇒ startup error naming the path. |
| **Permissions** | The socket is created with mode `0600`, owner-only. `umask` is neutralised: mcpmock `chmod`s the socket to `0600` immediately after `net.Listen` and **before** it accepts, so a permissive umask cannot widen it. Verification of the mode after bind is part of `MOCK-104.4`. |
| **Length limit** | `sun_path` is 104 bytes on macOS/BSD, 108 on Linux. If the computed default exceeds the platform limit, mcpmock falls back to `/tmp/mcpmock-${pid}.sock` and logs the fallback at `WARN` with both paths. If **that** also exceeds the limit, startup fails. |
| **Stale socket** | On bind failure with `EADDRINUSE`, mcpmock **probes** the existing socket by dialling it. If the dial **succeeds**, a live process owns it ⇒ startup fails with a collision error naming the path. If the dial fails with `ECONNREFUSED` (nothing listening), the socket is stale ⇒ mcpmock unlinks it, logs the removal at `WARN`, and retries the bind **once**. A second failure is fatal. Never unlink without probing first: an unconditional unlink silently steals the socket of a healthy process. |
| **Collision** | Defaults embed the pid, so a collision means either a stale file (handled above) or pid reuse after an unclean kill (also handled: the probe fails, the file is stale). Two *live* instances cannot collide on the default path. |
| **Cleanup** | The socket is unlinked on graceful shutdown, via the same `defer`/signal path that flushes the journal. On `SIGKILL` or panic no cleanup runs — hence the stale-socket rule above, which is the actual mechanism that makes cleanup failures survivable. mcpmock deliberately does **not** sweep other `mcpmock-*.sock` files; that would race with concurrent instances (ADR-007). |
| **Announcement** | The chosen path is printed on **stderr** at startup alongside the effective seed, in the ADR-016 structured-log startup record, under key `controlSocket`. |

In HTTP mode a TCP control listener is started too (both may run simultaneously).

### Contract shape

REST-ish, `/v1` prefix, resources under `/v1/instances/{name}/…`, action verbs as
`POST /…:verb` (Google AIP style) so mutating actions are never confused with resource writes.
Full contract with error catalogue in `contracts/control-api.openapi.yaml`.

Journal reads support `Accept: application/json` (paged) and `application/x-ndjson`
(streaming, with `?follow=true`) — `MOCK-602`.

### Authentication and exposure

The control API can regenerate catalogues, forge credentials and read a journal containing full
request bodies. It is the highest-value target in the process.

| Binding | Default | Auth |
|---|---|---|
| unix socket | enabled in stdio mode | filesystem perms `0600` |
| `127.0.0.1:<port>` | enabled in HTTP mode | token **optional** (warn-only) |
| any non-loopback address | **refused at startup** unless a token is configured | `Authorization: Bearer <token>`, constant-time compare |

Token source: `MCPMOCK_CONTROL_TOKEN` env var or `--control-token-file`. **Never** a CLI flag
value (visible in `ps`) — the flag `--control-token` is deliberately not implemented; only
`--control-token-file`. Never in the scenario file. In Kubernetes, a `Secret` mounted as a file
(`deployment.md §4`).

Optional mTLS on the control listener reuses `MOCK-106`'s plumbing.

The control API is **not** rate-limited for reads and is limited to 100 mutations/s (ADR-014).

## Options considered

1. **Separate CLI implementation calling the HTTP API by hand** — rejected: the drift `MOCK-104`
   is implicitly guarding against.
2. **CLI writes to the scenario file and signals a reload** — rejected: reload is not "without
   restart" for stream state, and it makes journal reads impossible.
3. **gRPC control plane** — rejected: dependency (ADR-013), and `MOCK-104` says HTTP.
4. **Named pipe / signal-based control for stdio** — rejected: cannot carry a journal query
   response.
5. **One `Control` interface with three front-ends (chosen).**

## Consequences

**Positive.** `MOCK-104`'s "same operations" is guaranteed structurally, not by discipline.
Embedded tests skip HTTP entirely, which also makes them faster and free of port allocation.
The OpenAPI document and the Go interface are checked against each other by a test that walks the
route table.

**Negative.** The `Control` interface is public and therefore a compatibility surface — adding a
method breaks third-party implementers. Mitigated by documenting that `Control` is
**consumer-only**: users call it, they do not implement it, and we reserve the right to add
methods (stated in the doc comment). If that proves untenable, the escape is an embedded
`unimplementedControl` struct, which is the standard Go remedy.

Unix socket paths on macOS are limited to 104 bytes — `$XDG_RUNTIME_DIR` on some systems plus a
long pid can approach it. The fallback to `/tmp` and the logging of the chosen path are now
specified normatively in *The documented path*, above (AMEND-7), together with stale-socket
probing, permission hardening and cleanup — the parts `MOCK-104.4` depends on and that were
previously left to the implementer.

**Forecloses.** A control plane that survives process restart (state is in memory by design).
