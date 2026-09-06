# TC-027 — Phase 1 functional suite and golden fixtures

| | |
|---|---|
| **Task** | `TASK-027` |
| **Requirements** | `MOCK-102`, `MOCK-201`, `MOCK-202`, `MOCK-203`, `MOCK-207`, `MOCK-209`, `MOCK-212` (sleep subset), `MOCK-601`, `MOCK-602`, `MOCK-701`, `MOCK-704`, `§0.1` determinism; built-in-tools AMEND-2 (202.10–202.20); `MOCK-105.5` credential scan |
| **Level** | functional (in-process `mcpmock.Server` driven **only** through `test/mcpclient` and the public facade/control surface) |
| **Harness** | `mcpmock.StartTest` / `New` + `Start` on an ephemeral loopback port; `test/mcpclient` `NewHTTP`/`NewStdio`; no cluster, local only |
| **Build tag** | `functional` (`//go:build functional`), path `test/functional` (CI `functional-tests`, `ci.yml:126`) |

## Why this case exists

The unit tests prove the internal algorithms; the embed e2e proves external
import. Neither proves that the **assembled** server answers the Phase 1
protocol correctly *at the wire byte level*, driven by a client that shares no
code with it (ADR-017). This suite is that proof. Its golden files encode the
**provisional** `2026-07-28` behaviour (GAP-003 open): they are the Phase 1
regression contract, not a conformance claim.

## Anti-circularity discipline (ADR-017, test-strategy §0)

- The server is driven **only** through `test/mcpclient`, which hand-transcribes
  its own wire constants from the annex and imports no `internal/` package. No
  test in this suite imports `internal/`.
- Golden files are **authored by hand** from `builtin-tools.md` / the wire annex,
  redacted deterministically, and reviewed. There is no `-update` flag: a golden
  change is a deliberate edit, so a behaviour change surfaces as a reviewable diff
  (`MOCK-604`). A disagreement between a golden and the server is reported as a
  defect, never silently rewritten.

## Boundary respected (test-strategy §1, §4)

Socket-level facts (TLS, connection reset, `ctl`-over-a-real-socket CLI parity,
control-plane HTTP routing) are **integration**-level and are *not* proven here.
`MOCK-602` journal filters are exercised through the in-process `Server.Control()`
— ADR-015's truest front end, the same implementation the `ctl` CLI and the HTTP
control API wrap — so the filter semantics are proven functionally while the CLI
transport parity remains integration-level.

---

## TC-027.1 — Three handlers, both transports (`MOCK-102`, `MOCK-201`, `MOCK-202`)

**Given** a server at a fixed seed with the default (built-in) catalogue, reached
over HTTP and over stdio.
**When** the client issues `server/discover`, `tools/list`, and `tools/call` for
each built-in (`echo`, `sleep`, `fail`) with a complete `_meta`.
**Then** each response is a non-error JSON-RPC envelope carrying the
annex-defined `resultType`, and the HTTP and stdio response bodies are
**byte-identical** for the same JSON-RPC payload (`MOCK-102.3/.4`).
**Cleanup:** `t.Cleanup` closes client and server; ephemeral port; stdio pipes
closed before `Close`. **Runtime:** < 1 s.

## TC-027.2 — `_meta` strict rejection and mode distinction (`MOCK-203`)

**Given** a strict-default server.
**When** the client omits `_meta.protocolVersion`, then `_meta.clientCapabilities`,
then `_meta` entirely.
**Then** each yields JSON-RPC `-32602` **and** HTTP `400`, with
`error.data.missing` naming exactly the absent field(s) (`203.1`–`203.4`).
**And** under `lenient` the same request is **answered normally** and the journal
records `metaValidation{mode:"lenient", outcome:"tolerated", missing:[…]}`; under
`off` it is answered normally and records `metaValidation{mode:"off",
outcome:"skipped"}` with **no** `missing` — the two modes are observably distinct
in the journal (`203.7`, `203.8`; DEF-005 guard). **Runtime:** < 1 s.

## TC-027.3 — `MOCK-207` GET/DELETE 405, session headers ignored, no session id

**Given** a modern-only HTTP server.
**When** the client sends `GET` and `DELETE` to the MCP path; and a normal `POST`
carrying `Mcp-Session-Id` and `Last-Event-ID`.
**Then** GET/DELETE return HTTP `405` with `Allow: POST` (`207.1`); the POST is
processed normally, the two headers are **ignored** but journaled verbatim
(`207.2/.3`); and **no** response carries `Mcp-Session-Id` (`207.4`).
**Runtime:** < 1 s.

## TC-027.4 — `MOCK-209` resultType / serverInfo and omission switches

**Given** servers with the omission switches off and on.
**When** the client calls each Phase 1 method.
**Then** each result carries `resultType` and `_meta.serverInfo` (`209.1/.2`);
with `switches.omitResultType` the `resultType` key is absent from every result
(`209.3`); with `switches.omitServerInfoMeta` the `_meta.serverInfo` key is
absent (`209.4`). **Runtime:** < 1 s.

## TC-027.5 — Built-in tool semantics (`MOCK-202` AMEND-2: 202.10–202.20)

**Given** a fixed-seed server.
**When/Then**
- `echo {"b":2,"a":"x"}` → `content[0].text` is canonical JSON with **keys
  sorted**, `structuredContent.echoed` preserves JSON types (202.10); absent
  arguments → `content[0].text == "{}"` (202.11).
- `sleep durationMs:0` returns quickly with `content[0].text=="slept 0ms"`;
  `sleep durationMs > 30000` with default `reject` returns `-32602`
  **immediately** with `data.requestedMs`/`data.maxSleepMs` (202.14); negative
  duration → `-32602 reason:"negative_duration"`.
- `fail` default → HTTP `200` **result** with `isError:true`; a successful `echo`
  result omits the `isError` key (202.17).
- `tools/list` lists `echo`, `sleep`, `fail` in that fixed order (202.20).
**Runtime:** < 1 s.

## TC-027.6 — `sleep` cancellation on client disconnect (`MOCK-212` subset)

**Given** a server and an HTTP request for `sleep` with a long duration.
**When** the client cancels its context (disconnect) mid-flight.
**Then** the request `ctx` is observed cancelled, **no** response frame is read,
and the journal shows a record with `canceled: true` for that request
(`212.2/.3`), and `mcpmock_requests_total{outcome="cancelled"}` is observable.
The test asserts on the *condition* (cancellation recorded), never on an absolute
wall-clock ceiling. **Runtime:** < 2 s.

## TC-027.7 — Determinism: same seed ⇒ byte-identical, different seed ⇒ different (`MOCK-704`, `§0.1`)

**Given** two servers built with the **same** seed, and a third with a different
seed, each run under a fixed request script.
**When** the same requests are issued.
**Then** the raw response bytes are **identical** across the same-seed servers
and across `GOMAXPROCS` values; the different-seed server differs on at least one
seed-dependent response. Asserted on raw bytes via `Response.Body`.
**Runtime:** < 1 s.

## TC-027.8 — Journal completeness and filters (`MOCK-601`, `MOCK-602`)

**Given** a server that has served a mix of methods.
**When** the suite queries the journal through `Server.Control().For(name).Journal`
with `journalapi.Query` selectors (method glob, name glob, status code, limit),
and exports NDJSON via `assert.FromReader`.
**Then** each request is captured (`601`), and each filter returns exactly the
expected subset (`602.4`); `ClearJournal` empties it (`602.6/702.7); the NDJSON
round-trips into a server-less `View` (`603.4`).
**Runtime:** < 1 s.

## TC-027.9 — Credential-safety sentinel scan (`MOCK-105.5`)

**Given** a request carrying a sentinel bearer credential
`Authorization: Bearer test-only-SENTINEL-<random>`.
**When** the server journals it and the suite serialises the whole journal to
JSON.
**Then** the raw sentinel value appears **nowhere** in the serialised journal;
the `Authorization` value is redacted (`<redacted:sha256:…`). Zero matches.
**Runtime:** < 1 s.

## TC-027.10 — `validate` exit-code contract (`MOCK-701.5`)

**Given** the built `mcpmock` binary.
**When** `validate` is run on a valid scenario, an invalid (schema-violating)
scenario, and a missing file.
**Then** exit codes are `0`, `1`, `2` respectively, with an actionable message,
and `--output json` emits the JSON Pointer + source. **Runtime:** < 3 s.

## TC-027.11 — Golden-file comparison with redaction (`MOCK-604`)

**Given** hand-authored golden files under `testdata/golden/MOCK-*/` labelled
provisional (GAP-003).
**When** the server produces the corresponding responses at a fixed seed.
**Then** the redacted actual bytes equal the golden bytes; a directory-coverage
test fails if a Phase 1 requirement directory has no golden. **Runtime:** < 1 s.

## TC-027.12 — `scenarios/happy-path.yaml` validates and serves (`705.2` Phase 1 subset)

**Given** the Phase 1 delivered scenario `scenarios/happy-path.yaml`.
**When** it is loaded via `NewFromFile` and exercised.
**Then** it validates, starts, and answers `tools/list`. **Runtime:** < 1 s.
