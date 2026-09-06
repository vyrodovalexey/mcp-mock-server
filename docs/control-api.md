# Control API reference

Audience: **operator** / **integrator**.

The control API is a separate HTTP surface (default `127.0.0.1:9091`) and/or
a unix domain socket for inspecting and, in later phases, mutating a running
mcpmock process. **Phase 1 implements 7 read operations plus the served
OpenAPI document — 8 routes total.** Everything else in
[`internal/control/control-api.openapi.yaml`](../internal/control/control-api.openapi.yaml)
(instance mutation, fault control, notifications, streams, sessions, auth,
catalogue drift) is the **full future contract**, not what this build serves;
a `TestControlSurfaceParity` test enforces that the served route table and
this document's Phase 1 operation IDs never drift apart, but it does not mean
the rest of the OpenAPI file is implemented.

## Table of contents

- [Routes implemented in Phase 1](#routes-implemented-in-phase-1)
- [Base URLs](#base-urls)
- [`mcpmock ctl` vs raw HTTP](#mcpmock-ctl-vs-raw-http)
- [Journal query parameters](#journal-query-parameters)
- [Response shapes](#response-shapes)
- [Security](#security)

## Routes implemented in Phase 1

Verified against [`internal/control/routes.go`](../internal/control/routes.go)
and live `curl` calls against a running instance:

| Method | Path | `operationId` | Returns |
|---|---|---|---|
| `GET` | `/v1/openapi.yaml` | `getOpenAPI` | This contract document, as raw YAML, served by the running process. |
| `GET` | `/v1/seed` | `getSeed` | `{"seed": <uint64>, "source": "flag"\|"random"}` |
| `GET` | `/v1/health` | `getHealth` | `{"status","instances","uptimeSeconds","seed","safeMode"}` |
| `GET` | `/v1/instances` | `listInstances` | Array of instance summaries. |
| `GET` | `/v1/instances/{name}` | `getInstance` | One instance summary, or 404. |
| `GET` | `/v1/instances/{name}/journal` | `getJournal` | A page of journal records (JSON or NDJSON). |
| `DELETE` | `/v1/instances/{name}/journal` | `clearJournal` | `204`, empties the instance's journal. |
| `GET` | `/v1/instances/{name}/journal/correlations` | `getCorrelations` | MRTR-shaped correlation chains grouped by request identity — populated in Phase 1 only for the trivial single-round case, since MRTR itself is Phase 5. |

There is no route to add, remove or mutate an instance, no fault control, no
credential rotation, and no session control in this build — those are the
`addInstance`, `setFaults`, `rotateCredentials`, `invalidateSession`, etc.
operations in the OpenAPI file, all scheduled for later phases per
[`docs/not-yet-implemented.md`](not-yet-implemented.md).

## Base URLs

By default (no `-control-listen` flag), the control API binds to an
ephemeral loopback TCP port; `serve`'s startup log line
(`"msg":"started"`) reports the bound URL as `controlUrl`. Pass
`-control-listen 127.0.0.1:9091` for a fixed port. `-control-socket` adds (or,
in stdio mode, is the only) unix-socket front end. `-no-control` disables
both front ends (the in-process API used by the library form still exists,
but is unreachable from outside the process).

## `mcpmock ctl` vs raw HTTP

`mcpmock ctl` is a thin CLI front end over the exact same route table — see
[`docs/cli-reference.md#ctl`](cli-reference.md#ctl). Anything shown here as a
`curl` call has an equivalent `ctl` invocation; use whichever is convenient.

```console
$ curl -s http://127.0.0.1:9091/v1/health
{"status":"ok","instances":1,"uptimeSeconds":19.59,"seed":11983285343475008406,"safeMode":false}

$ mcpmock ctl health --url http://127.0.0.1:9091
{
  "status": "ok",
  "instances": 1,
  "uptimeSeconds": 20.95,
  "seed": 11983285343475008406,
  "safeMode": false
}
```

## Journal query parameters

`GET /v1/instances/{name}/journal` accepts these query parameters (verified
against [`internal/control/http.go`](../internal/control/http.go) and the
OpenAPI document; not every parameter listed in the full contract has an
effect yet — the ones below do):

| Parameter | Type | Effect |
|---|---|---|
| `limit` | integer | Max records returned (default 100, max 10000). |
| `format` | `json`\|`ndjson` | `ndjson` streams one `JournalRecord` per line instead of a `{"records":[...]}` envelope. Verified live. |
| `method` | string glob | Filter by JSON-RPC method. |
| `name` | string glob | Filter by tool/prompt/resource name. |

Example, NDJSON form:

```console
$ curl -s "http://127.0.0.1:9091/v1/instances/default/journal?format=ndjson&limit=5"
{"schemaVersion":1,"seq":1,"instance":"default", ... }
```

A journal record's fields are documented in
[`specification/data-model.md`](../specification/data-model.md) §3
(source-owned by the architect); the shape you see in a live response is the
ground truth for this build.

## Response shapes

- **`/v1/seed`** — `source` is `"flag"` whenever a seed value was resolved
  before the process started serving, whether that came from an explicit
  `--seed` or from a value `mcpmock serve` generated itself; in practice
  every server started through the `serve` command reports `source: "flag"`.
  `"random"` is reserved for a library caller (`mcpmock.New`) that never
  called `WithSeed` — verified by reading `seedSourceFor` in `mcpmock.go`.
- **`/v1/instances`** — `journal.records`/`journal.dropped`/`journal.bytes`
  reflect the live ring state at request time; `hostile` is always `false` in
  Phase 1 (the hostile-corpus gate is Phase 9).
- **Error responses** — the OpenAPI document's error catalogue (`code`,
  `message`, `retryable`) is the target contract for the full API; Phase 1's
  8 routes return plain HTTP status codes (`404` for an unknown instance
  name) without necessarily matching that catalogue's JSON error body shape
  yet. If your tooling parses control API errors, treat this as unverified
  and check the raw response.

## Security

**Read this before exposing the control API beyond your own workstation.**

- The control API reads full request/response bodies from the journal and
  (in later phases) can forge credentials and inject faults. It binds to
  loopback or a unix socket by default.
- Binding it to a **non-loopback** address requires a token to be configured
  (`--control-token-file` or `MCPMOCK_CONTROL_TOKEN`); without one, startup
  is refused:

  ```console
  $ mcpmock serve -control-listen 0.0.0.0:9091
  mcpmock serve: mcpmock: control bind: control: refusing to bind control API
  to non-loopback address "0.0.0.0:9091" without a token (...)
  ```

- **Verified live and important: in this build, configuring a token only
  permits the non-loopback bind at startup — it is not checked on individual
  requests.** A server started with `-control-listen 0.0.0.0:9091
  -control-token-file <file>` answered `GET /v1/health` with `200` with no
  `Authorization` header at all, and also with an incorrect bearer token.
  This matches the source comment in `options.go`: *"Phase 1 wires the token
  only for the non-loopback-bind gate; per-request token enforcement is
  Phase 4."* **Treat a non-loopback control listener in this build as
  equivalent to no authentication** and keep it on loopback or a unix socket
  outside of a deliberately isolated test environment.
- The Helm chart reflects this: `control.enabled` defaults to `false`, and
  turning it on requires `control.tokenSecretName` — see
  [`docs/deployment.md`](deployment.md#control-api).
