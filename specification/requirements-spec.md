---
title: mcpmock — Requirements Specification with Acceptance Criteria
status: draft
version: 0.1.0
updated: 2026-09-04
normative-source: specification/requirements.md
---

# Requirements Specification

Every `MOCK-nnn` from `requirements.md` restated with acceptance criteria that an implementer and
a test author can both act on.

**Provenance labels.** `[stated]` = given in `requirements.md`. `[derived]` = logically forced by
something stated. `[assumed]` = supplied by this specification and requiring confirmation; every
`[assumed]` appears in `gap-analysis.md`.

**Testability verdicts.** `OK` = testable as written. `TIGHTENED` = written loosely; a measurable
form is proposed here. `NOT TESTABLE AS WRITTEN` = the requirement cannot be verified without a
decision; a testable replacement is proposed and the item is escalated.

**Verification levels.** `U` unit · `F` functional (in-process, independent client, ADR-017) ·
`I` integration (real process, TLS, control API) · `E` e2e (container + Helm on
`docker-desktop`/`mcpmock-test`) · `P` performance (k6 / Go benchmarks).

---

## §0 Design principles as verifiable properties

`requirements.md` §0 is prose, not numbered requirements. It is nonetheless testable and is the
root of several architectural decisions, so it is given handles here.

| ID | Property | Acceptance criteria | Level |
|---|---|---|---|
| **PRIN-1** `[stated §0.1]` | Same config + same seed ⇒ byte-identical responses. | For every scenario in `scenarios/`: two runs in the same process, and two runs at `GOMAXPROCS=1` and `GOMAXPROCS=8`, produce byte-identical response bodies for an identical request sequence, and identical journals after redaction of `wallTime`, `monoTime`, `peer`, `durationNs`. Test runs with `-count=20`. **Scope: response bytes, not wall-clock timing** — see GAP-008. | F |
| **PRIN-2** `[stated §0.1]` | Every nondeterminism source is seeded. | No call to `time.Now()`, `crypto/rand` or `math/rand` global functions occurs on a path producing a response body. Enforced by `make determinism-check` (AST scan of `internal/` excluding `obs/`, `journal/`, `certs/`) plus PRIN-1. | U |
| **PRIN-3** `[stated §0.2]` | Behaviour is mutable at runtime without restart. | Every §7 `MOCK-702` operation is exercised against a running instance and takes effect on the next request, with `Snapshot.Gen` incremented and journaled. | I |
| **PRIN-4** `[stated §0.3]` | The journal is complete. | For every request in a scenario run, a journal record exists whose `Seq` ordering matches send order for a serialised workload, containing all `MOCK-601` fields. | F |
| **PRIN-5** `[stated §0.4]` | Non-conformance is nameable. | Every deliberate violation is reachable only via a named, schema-validated scenario key or fault `id`; a run with an empty fault list and `switches` at defaults produces only self-check-passing responses (ADR-017 §5). | F |

---

## §1 Deployment and interfaces

### MOCK-101 — Single static binary, distroless image, Helm chart, manifests
`[stated]` · Testability: **OK**

| # | Acceptance criterion | Level |
|---|---|---|
| 101.1 | `CGO_ENABLED=0 go build -trimpath ./cmd/mcpmock` produces a binary for `linux/amd64`, `linux/arm64`, `darwin/arm64`. | U |
| 101.2 | On Linux, `file bin/mcpmock` reports `statically linked`, and `ldd bin/mcpmock` reports "not a dynamic executable". | I |
| 101.3 | The image is built `FROM gcr.io/distroless/static-debian12:nonroot`; `docker run --rm --entrypoint sh <img>` fails (no shell present). | E |
| 101.4 | Container runs as UID 65532, `readOnlyRootFilesystem: true`, all capabilities dropped, and starts with no writable volume other than an optional `emptyDir` for scenario files. | E |
| 101.5 | `helm lint helm/mcpmock` passes; `helm template` renders; `helm install --wait` into `docker-desktop`/`mcpmock-test` reaches `Ready`. | E |
| 101.6 | `deploy/manifests/*.yaml` apply with `kubectl apply -f` and reach `Ready` without Helm. | E |
| 101.7 | Approved dependency set matches ADR-013 exactly; `make deps-check` fails on any other module. | U |
| 101.8 | "No runtime dependencies" is verified as: the pod starts and serves with **no** other pod, service, ConfigMap-external-service or network egress (NetworkPolicy deny-all egress applied). | E |

### MOCK-102 — Both transports in one binary
`[stated]` · Testability: **OK**

| # | Acceptance criterion | Level |
|---|---|---|
| 102.1 | `mcpmock serve --transport stdio` reads newline-delimited JSON-RPC on stdin, writes frames on stdout, and writes **nothing** non-protocol to stdout (ADR-011). | F |
| 102.2 | `mcpmock serve --transport http --path /mcp` accepts `POST` at the configured path; path is configurable per instance and defaults to `/mcp`. | F |
| 102.3 | A single invocation may enable both (`--transport stdio,http`); the same scenario is served on both and produces identical response bodies for identical JSON-RPC payloads (modulo HTTP-only header behaviour). | F |
| 102.4 | The response-body equality in 102.3 is asserted byte-for-byte for `tools/list`, `tools/call`, `prompts/get`, `resources/read` at a fixed seed. | F |

### MOCK-103 — N independent logical servers in one process
`[stated]` · Testability: **OK**

| # | Acceptance criterion | Level |
|---|---|---|
| 103.1 | A `kind: Fleet` scenario with 3 instances at distinct `mountPath`s serves 3 distinct catalogues from one process. | F |
| 103.2 | Cross-instance isolation: mutating instance A's catalogue via the control API leaves B's `tools/list` byte-identical. | F |
| 103.3 | Two instances may serve tools with **identical names** without interference (feeds `MOCK-223`). | F |
| 103.4 | Instances may be added and removed at runtime (`POST /v1/instances`, `DELETE /v1/instances/{name}`) without dropping in-flight requests on other instances. | I |
| 103.5 | Each instance has its own journal, fault set, auth config, era and seed subtree. | F |

### MOCK-104 — Control API (HTTP, separate port) + CLI mirror
`[stated]` · Testability: **OK**

| # | Acceptance criterion | Level |
|---|---|---|
| 104.1 | Control API listens on a port distinct from every MCP listener; `contracts/control-api.openapi.yaml` is served at `GET /v1/openapi.yaml`. | I |
| 104.2 | **Every** operation on the `mcpmock.Control` Go interface has exactly one route, and every route maps to exactly one method. Asserted by `TestControlSurfaceParity`, which walks the route table and the interface method set. | U |
| 104.3 | `mcpmock ctl <verb>` exists for every `Control` method; `mcpmock ctl --help` output is generated from the same route table. | U |
| 104.4 | In stdio mode a unix-socket control listener is created at **the documented path — `${XDG_RUNTIME_DIR}/mcpmock-${pid}.sock`, falling back to `/tmp/mcpmock-${pid}.sock`; full rules, including override, stale-socket probing, permissions and cleanup, in `ADR-015 §"The documented path"` (AMEND-7)** — with mode `0600` verified after bind; the path is printed on stderr at startup under log key `controlSocket`; `mcpmock ctl --socket <path>` works against it. | I |
| 104.5 | A **stale** socket file at the default path (no listener behind it) is probed, unlinked and rebound, and the removal is logged at `WARN`; a **live** socket at the same path causes startup to fail with a collision error rather than stealing it. `[derived]` — AMEND-7. | I |
| 104.5 | Binding the control API to a non-loopback address without a configured token fails at startup with a non-zero exit and a clear message. | I |
| 104.6 | Every 4xx/5xx response conforms to the error object in `contracts/control-api.openapi.yaml` (`code`, `message`, `details`). | I |

### MOCK-105 — `/metrics` and structured JSON logs on stderr
`[stated]` · Testability: **OK**

| # | Acceptance criterion | Level |
|---|---|---|
| 105.1 | `GET /metrics` returns Prometheus text exposition; `promtool check metrics` passes on the output. | I |
| 105.2 | Every metric in `observability.md §2` is present after a scenario run that exercises it, with the declared type and label set. | F |
| 105.3 | All log output is valid JSON, one object per line, on **stderr**; `TestLogSchema` validates every line against `observability.md §4`'s schema. | F |
| 105.4 | stdout receives zero bytes in stdio mode other than protocol frames; `mcpmock_stdout_leak_bytes_total == 0` after a full scenario run. | F |
| 105.5 | No log record contains a raw credential; asserted by scanning all output for the fixture token values. | F |

### MOCK-106 — TLS and mTLS on the MCP listener
`[stated]` · Testability: **OK**

| # | Acceptance criterion | Level |
|---|---|---|
| 106.1 | TLS enabled from PEM file paths or from a runtime-generated ephemeral CA (`mcpmock gen certs`). No key material is committed or embedded. | I |
| 106.2 | `clientAuth` is configurable across all five `tls.ClientAuthType` values; `require-and-verify` rejects a connection with no client certificate at the TLS layer. | I |
| 106.3 | With mTLS on, the journal records client-certificate subject, issuer, serial, SHA-256 fingerprint and `notAfter`. | I |
| 106.4 | Minimum TLS version configurable, default TLS 1.2, with TLS 1.3 supported; cipher suites configurable. | I |
| 106.5 | A TLS handshake failure fault (`MOCK-502`) is injectable and is journaled with `phase: connection` even though no HTTP request results. | I |
| 106.6 | Per-instance differing client-cert policy works on a shared listener via SNI (`GetConfigForClient`) or via `listener: own`. | I |

### MOCK-107 — Startup < 200 ms; usable as a Go library
`[stated]` · Testability: **TIGHTENED** — "under 200 ms" needs a stated workload and percentile.

**Proposed testable form:** p95 wall time from process start to "all listeners accepting" is
< 200 ms, measured over 50 runs, for the reference scenario (200 instances, 5000 generated tools
each, journaling on, TLS off) on the CI runner; and < 100 ms for a single-instance scenario.

| # | Acceptance criterion | Level |
|---|---|---|
| 107.1 | p95 startup < 200 ms for the reference scenario, 50 runs. Recorded as a CI artifact and gated. | P |
| 107.2 | The library form starts in-process with no listener in < 20 ms p95 (`mcpmock.New` + `Start` with `WithStdio`). | U |
| 107.3 | `package mcpmock` is importable from an external module and satisfies `contracts/library-api.md`. Verified by an example module in `test/e2e/embed/`. | E |
| 107.4 | Two `mcpmock.Server` instances run concurrently in one test binary under `t.Parallel()` with no shared state; asserted by `TestTwoServersParallel`. | U |
| 107.5 | `make globals-check` finds zero mutable package-level variables outside the allowlist. | U |
| 107.6 | No `init()` in the module performs I/O, compiles a schema, generates a key or allocates > 4 KiB. | U |
| 107.7 | `mcpmock.StartTest(t)` registers `t.Cleanup` shutdown; a test that forgets to close leaks nothing (verified with `goleak`). | U |

---

## §2 Protocol emulation — modern era (`2026-07-28`)

> **All of §2 is gated on ADR-019 / GAP-003.** Where `requirements.md` names a field but not its
> type or nesting, the acceptance criterion below references `contracts/wire-2026-07-28.md`, whose
> `[PROPOSED]` entries require ratification. Criteria are written so that ratification changes the
> annex, not this document.

### MOCK-201 — `server/discover`
`[stated]` · Testability: **TIGHTENED** (field types unspecified)

| # | Acceptance criterion | Level |
|---|---|---|
| 201.1 | `server/discover` returns a result containing `supportedVersions`, `capabilities`, `instructions`, `serverInfo`, `ttlMs`, `cacheScope`, each independently configurable in `spec.discover`. | F |
| 201.2 | `capabilities.extensions` is configurable to arbitrary JSON, including unknown extension keys (§11 "Extensions" row). | F |
| 201.3 | Each field has an independent omission switch; omitting a field produces a result with that key **absent**, not `null`. | F |
| 201.4 | The result validates against the annex schema when `switches.selfCheck` is on. **The schema is `contracts/wire-2026-07-28.schema.json#/$defs/phase1Result`, created by AMEND-4; `selfCheck` is Phase 1 scope for the three Phase 1 methods.** A self-check failure is a server-side `-32603` plus an `ERROR` log naming the schema path — never a silently emitted invalid result. | F |
| 201.5 | `ttlMs` and `cacheScope` values include `0`, negative, absent, and `> 2^53` (shared with `MOCK-232`). | F |

### MOCK-202 — Method set with per-method enable/disable
`[stated]` · Testability: **OK**

| # | Acceptance criterion | Level |
|---|---|---|
| 202.1 | All nine named methods are implemented: `tools/list`, `tools/call`, `prompts/list`, `prompts/get`, `resources/list`, `resources/templates/list`, `resources/read`, `completion/complete`, `subscriptions/listen`. | F |
| 202.2 | `spec.switches.methods.<name>.enabled: false` causes the method to return `-32601` Method not found. | F |
| 202.3 | A disabled method is also absent from `server/discover` `capabilities` when `switches.methods.<name>.hideFromCapabilities` is true; independently controllable (so "advertised but disabled" is testable). | F |
| 202.4 | Each method's happy-path response validates against the annex schema. | F |

### MOCK-203 — `_meta` validation; reject missing `protocolVersion` / `clientCapabilities`
`[stated]` · Testability: **OK**

| # | Acceptance criterion | Level |
|---|---|---|
| 203.1 | A request whose `_meta.protocolVersion` is absent yields JSON-RPC `-32602` and HTTP `400`. | F |
| 203.2 | Same for absent `_meta.clientCapabilities`. | F |
| 203.3 | Same for `_meta` absent entirely. | F |
| 203.4 | The error `data` identifies which field was missing (`data.missing: ["protocolVersion"]`). `[assumed]` — GAP-011. | F |
| 203.5 | Validation strictness is switchable (`switches.validateMeta: strict\|lenient\|off`) so the hub's behaviour against a lax server is also testable. **The three modes are defined normatively in the table below (AMEND-6).** | F |
| 203.6 | The decoded `_meta` appears in the journal under `meta` with all keys preserved, including unknown ones. | F |
| 203.7 | In `lenient` mode a request with absent `_meta.protocolVersion` is **answered normally** (no error), and the journal record carries `metaValidation: {mode:"lenient", missing:["protocolVersion"], accepted:true}`. `[derived]` — AMEND-6. | F |
| 203.8 | In `off` mode the same request is answered normally and the journal record carries `metaValidation: {mode:"off", missing:[], accepted:true}` — **`missing` is not computed**, which is the observable difference from `lenient`. `[derived]` — AMEND-6. | F |

##### `switches.validateMeta` — the three modes  (AMEND-6, closes gap G-3)

`lenient` exists to answer one question: *does the hub cope with a server that is sloppy about
`_meta`?* A hub that silently depends on the server rejecting its malformed `_meta` will pass
against `strict` and fail in production against a lax server. `lenient` makes that latent bug
reproducible. It is therefore **"accept, but record everything a strict server would have
rejected"** — the deficiency must remain *visible to the test*, or the mode proves nothing.

| | `strict` (default) | `lenient` | `off` |
|---|---|---|---|
| Missing `_meta.protocolVersion` | **Reject** `-32602` / `400` | Accept, request proceeds | Accept, request proceeds |
| Missing `_meta.clientCapabilities` | **Reject** `-32602` / `400` | Accept, request proceeds | Accept, request proceeds |
| `_meta` absent entirely | **Reject** `-32602` / `400` | Accept, request proceeds | Accept, request proceeds |
| `_meta` present but **not a JSON object** (e.g. `"_meta": 4`) | **Reject** `-32602` / `400` | **Reject** `-32602` / `400` | Accept, `_meta` treated as absent |
| `params` present but not an object | **Reject** `-32600` / `400` | **Reject** `-32600` / `400` | **Reject** `-32600` / `400` |
| Is the missing-field set computed? | yes (for `data.missing`) | **yes** — this is the point of the mode | **no** |
| Journal `metaValidation.mode` | `"strict"` | `"lenient"` | `"off"` |
| Journal `metaValidation.missing` | the rejected fields | **the tolerated fields** | always `[]` |
| Journal `metaValidation.accepted` | `false` when rejecting | always `true` | always `true` |
| Journal `meta` (203.6) | decoded, all keys | decoded, all keys | decoded, all keys |
| `mcpmock_meta_validation_total{mode,outcome}` | `outcome="rejected"` | `outcome="tolerated"` | `outcome="skipped"` |
| Log line when a field is missing | — (the error is the signal) | **`WARN`**, once per request, naming the fields | none |

**What `lenient` still enforces.** `lenient` relaxes exactly two things: the **presence** of
`protocolVersion` and the **presence** of `clientCapabilities`. It relaxes nothing else. Structural
well-formedness — `_meta` being an object if present, `params` being an object, the JSON-RPC
envelope itself — is still enforced, because a structurally broken request cannot be decoded into
the journal, and `MOCK-601`/`MOCK-203.6` require it to be. `lenient` is a **semantic** relaxation,
never a **syntactic** one.

**Why `lenient` is retained rather than removed.** It has a coherent and distinct meaning
(tolerate-and-record) that neither `strict` nor `off` provides, and `MOCK-203.5` is a `[stated]`
requirement naming all three modes. `off` is the mode that skips the work; `lenient` is the mode
that does the work and declines to act on it. The difference is observable in the journal and in
the metric, which is what makes both testable.

**Downstream effect.** Whatever `_meta` fields are absent, later pipeline stages must not assume
they are present. In `lenient`/`off`, an absent `protocolVersion` means version negotiation
(`MOCK-205`) falls back to the configured `defaultProtocolVersion`, and an absent
`clientCapabilities` is treated as the **empty capability set** — which makes `MOCK-206`'s
`-32021` *more* likely to fire, not less. `lenient` does not suppress downstream errors; it only
declines to raise its own.

### MOCK-204 — Header/body mirroring; `-32020` HeaderMismatch
`[stated]` · Testability: **TIGHTENED** — sentinel format and `Mcp-Param-*` encoding unspecified (GAP-003).

| # | Acceptance criterion | Level |
|---|---|---|
| 204.1 | `MCP-Protocol-Version` disagreeing with `_meta.protocolVersion` ⇒ `-32020` / `400`. | F |
| 204.2 | `Mcp-Method` disagreeing with the JSON-RPC `method` ⇒ `-32020` / `400`. | F |
| 204.3 | `Mcp-Name` disagreeing with the body's primitive name ⇒ `-32020` / `400`. | F |
| 204.4 | `Mcp-Name` carrying a Base64 sentinel that **decodes** to the body's name is **accepted**; a sentinel decoding to a different name is rejected. Round-trip property test over ASCII, non-ASCII, 128-char and 129-char names. | U,F |
| 204.5 | `Mcp-Param-*` values are compared to the body by the annex's typed rules: integers compared **numerically** (`007` ≡ `7`), strings byte-wise, booleans by keyword. A string/number mismatch is a mismatch. | F |
| 204.6 | Header absent while the body has the field, and header present while the body lacks it, are both distinguishable outcomes with distinct `data.reason`. | F |
| 204.7 | Mismatch detection is switchable off (`switches.validateHeaders: false`) to test the hub against a non-validating server. | F |
| 204.8 | Every mismatch is journaled with the header name, the header value, the body value and the comparison mode. | F |

### MOCK-205 — `-32022` UnsupportedProtocolVersion with configurable `supported`
`[stated]` · Testability: **OK**

| # | Acceptance criterion | Level |
|---|---|---|
| 205.1 | A request whose `protocolVersion` is not in `spec.discover.supportedVersions` yields `-32022`. | F |
| 205.2 | The error `data.supported` equals the configured list, in configured order. | F |
| 205.3 | The supported list is mutable at runtime (`PATCH /v1/instances/{n}` ⇒ `Snapshot.Gen`+1) and takes effect on the next request. | I |
| 205.4 | An empty `supported` list is expressible (rejects everything). | F |

### MOCK-206 — `-32021` MissingRequiredClientCapability
`[stated]` · Testability: **OK**

| # | Acceptance criterion | Level |
|---|---|---|
| 206.1 | A tool configured with `requiredClientCapabilities: [x]` called without `x` in `_meta.clientCapabilities` yields `-32021`. | F |
| 206.2 | `data.requiredCapabilities` lists **all** missing capabilities, in configured order. | F |
| 206.3 | Requirement is expressible per tool, per prompt, per resource, and globally. | F |
| 206.4 | Nested capability paths (e.g. `sampling.createMessage`) are supported. `[assumed]` — GAP-012. | F |

### MOCK-207 — `405` on GET/DELETE; ignore session headers; mint no session ids
`[stated]` · Testability: **OK**

| # | Acceptance criterion | Level |
|---|---|---|
| 207.1 | In modern-only mode, `GET` and `DELETE` on the MCP path return HTTP `405` with an `Allow: POST` header. `[derived]` | F |
| 207.2 | A request carrying `Mcp-Session-Id` is processed normally and the header is **ignored**; the value is journaled. | F |
| 207.3 | A request carrying `Last-Event-ID` is processed normally and the header is ignored; the value is journaled. | F |
| 207.4 | **No** response in modern mode contains `Mcp-Session-Id`; asserted across every method by `assert.AssertNoSessionHeaders` (`MOCK-603`). | F |
| 207.5 | Each of 207.1–207.4 has a named non-conformant switch that reverses it, so the hub's defensive path is testable. | F |

### MOCK-208 — Response shape selection; `X-Accel-Buffering: no`
`[stated]` · Testability: **OK**

| # | Acceptance criterion | Level |
|---|---|---|
| 208.1 | A method can be configured to answer `application/json` or `text/event-stream`. | F |
| 208.2 | Shape is configurable globally, per method, and per tool, with per-tool winning. | F |
| 208.3 | `shape: {mode: random, sseProbability: p}` selects by seeded draw (ADR-002 domain `"shape"`); the same request bytes at the same seed always get the same shape. | F |
| 208.4 | Every SSE response carries `X-Accel-Buffering: no`, `Content-Type: text/event-stream`, `Cache-Control: no-cache`. | F |
| 208.5 | A `Content-Type: application/json` response is a single complete JSON object with `Content-Length` set. | F |

### MOCK-209 — `resultType` on every result; `serverInfo` in result `_meta`; omission switches
`[stated]` · Testability: **TIGHTENED** (`resultType` value set unspecified — GAP-003)

| # | Acceptance criterion | Level |
|---|---|---|
| 209.1 | Every result carries `resultType` with the annex-defined value for that method. | F |
| 209.2 | Every result's `_meta` carries `serverInfo` matching `spec.discover.serverInfo`. | F |
| 209.3 | `switches.omitResultType: true` removes the key entirely from all results. | F |
| 209.4 | `switches.omitServerInfoMeta: true` removes `_meta.serverInfo`. | F |
| 209.5 | Omission is expressible per method as well as globally. | F |

### MOCK-210 — `notifications/progress` on the originating stream
`[stated]` · Testability: **OK**

| # | Acceptance criterion | Level |
|---|---|---|
| 210.1 | Progress notifications are emitted **only** when `progressToken` was supplied on the request; zero otherwise, asserted over a full scenario. | F |
| 210.2 | Count and interval are configurable per method and per tool; `count: 0` emits none. | F |
| 210.3 | Notifications are emitted **on the originating request's response stream**, not on a listen stream or a separate connection. | F |
| 210.4 | Each notification echoes the supplied `progressToken` verbatim. | F |
| 210.5 | Intervals are driven by the shared timer wheel; no per-request `time.Ticker` exists (asserted by a goroutine-count test at 1000 concurrent progressing requests). | P |
| 210.6 | A non-conformant switch emits progress **without** a `progressToken` (defensive-path testing). | F |

### MOCK-211 — `notifications/message` honouring requested minimum level
`[stated]` · Testability: **OK**

| # | Acceptance criterion | Level |
|---|---|---|
| 211.1 | Log notifications are emitted only when `_meta["io.modelcontextprotocol/logLevel"]` is present. | F |
| 211.2 | Only messages at or above the requested level are emitted; the level ladder is the annex's. | F |
| 211.3 | Message count, level distribution and content are configurable per method. | F |
| 211.4 | A non-conformant switch emits below the requested level. | F |

### MOCK-212 — Stream closure is cancellation
`[stated]` · Testability: **OK**

| # | Acceptance criterion | Level |
|---|---|---|
| 212.1 | Closing the response stream mid-request cancels the request `context` within 50 ms. | F |
| 212.2 | No further frames are written after cancellation, and any in-progress fault sleep is aborted. | F |
| 212.3 | A journal record exists with `cancelled: true` and `elapsedNs` equal to the time from request receipt to cancellation observation (± 10 ms). | F |
| 212.4 | Applies to both transports: HTTP client disconnect, and stdio `notifications/cancelled` or stdin EOF. | F |
| 212.5 | `mcpmock_requests_total{outcome="cancelled"}` increments. | F |

### §2.1 Primitive catalogue generation

#### MOCK-221 — Configurable catalogue, ≥5000 tools, deterministic names
`[stated]` · Testability: **OK**

| # | Acceptance criterion | Level |
|---|---|---|
| 221.1 | `catalogue.{tools,prompts,resources,resourceTemplates}.count` generate N, M, K items. | F |
| 221.2 | A 5000-tool catalogue is servable and paginable; `tools/list` over all pages returns exactly 5000 distinct names. | F |
| 221.3 | Names are deterministic: at a fixed seed, item `i` has the same name across runs and processes. Golden-tested at `i ∈ {0, 1, 2499, 4999}`. | U |
| 221.4 | 200 instances × 5000 tools resident adds < 50 MiB RSS over a 200-instance × 0-tool baseline (ADR-004's virtual-catalogue claim, measured). | P |
| 221.5 | `IndexOf(At(i).Name) == i` for all `i` in `[0, count)`. | U |

#### MOCK-222 — Hand-written primitives with full field control
`[stated]` · Testability: **OK**

| # | Acceptance criterion | Level |
|---|---|---|
| 222.1 | Authored items support `name`, `title`, `description`, `icons`, `annotations`, `inputSchema`, `outputSchema`, each emitted verbatim. | F |
| 222.2 | Authored items coexist with generated ones; an authored item whose name matches a generated one **replaces** it. | F |
| 222.3 | `inputSchema` / `outputSchema` / `annotations` / `icons` are `json.RawMessage` in the scenario schema and are **never** validated or normalised by mcpmock (ADR-008). Byte-for-byte emission is asserted. | F |
| 222.4 | Key order within an authored object is preserved on the wire. `[derived from 222.3]` | F |

#### MOCK-223 — Name edge cases
`[stated]` · Testability: **OK**

| # | Acceptance criterion | Level |
|---|---|---|
| 223.1 | A name of exactly 128 characters is servable. | F |
| 223.2 | A name of exactly 129 characters is servable (deliberately over-limit). | F |
| 223.3 | Names containing `.` are servable. | F |
| 223.4 | Names containing non-ASCII (including astral-plane and combining characters) are servable and force `Mcp-Name` sentinel encoding; round-trip asserted. | F |
| 223.5 | A name colliding with another instance's name is expressible via `collideWith` and both instances serve it independently. | F |
| 223.6 | A name that **literally matches the sentinel pattern** is servable and is not mis-decoded (the encoder must escape it; asserted by round-trip). | U,F |
| 223.7 | Each case is reachable from a named scenario key, not by hand-editing a list. | F |

#### MOCK-224 — `x-mcp-header` annotations including invalid ones
`[stated]` · Testability: **OK**

| # | Acceptance criterion | Level |
|---|---|---|
| 224.1 | Valid `x-mcp-header` annotations are emitted. | F |
| 224.2 | Each of the six named invalid forms is expressible by a named key and emitted verbatim: empty; containing CR/LF; duplicated case-insensitively; applied to a `number`-typed field; applied under `items` / `oneOf` / `$ref` (non-statically-reachable); out-of-safe-range integer. | F |
| 224.3 | mcpmock does **not** reject or normalise these at scenario-validation time. | U |
| 224.4 | CR/LF-bearing annotations are emitted inside the JSON body only; mcpmock never places them in a real HTTP header (response splitting is a vulnerability, not a feature) — `security.md §8`. `[derived]` | F |

#### MOCK-225 — Schema edge cases
`[stated]` · Testability: **TIGHTENED** — "composition-keyword bombs sized to a configurable validation cost" needs a cost unit.

**Proposed testable form:** validation cost is expressed as `estimatedValidationOps` (a declared
integer) and verified by measuring `jsonschema` validation wall time against the generated schema;
the generator must hit a target within ±30%.

| # | Acceptance criterion | Level |
|---|---|---|
| 225.1 | `$schema` draft-07 and missing `$schema` are both expressible and emitted verbatim. | F |
| 225.2 | Network `$ref` to a **loopback** address resolves against mcpmock's own embedded `$ref` host; no external egress occurs. | F |
| 225.3 | Network `$ref` to a **public host** requires `hostile.allowExternalRefs` + an explicit host allowlist; refused otherwise with a clear error (ADR-018 §7). | F |
| 225.4 | Deep nesting to a configurable depth is generated; depth ≥ 200 is expressible. | F |
| 225.5 | `$defs` of a configurable count (≥ 1000) is generated. | F |
| 225.6 | Composition bombs (`allOf`/`anyOf`/`oneOf` cross products) hit a target `estimatedValidationOps` within ±30%, measured. | P |
| 225.7 | mcpmock's own validation of `tools/call` arguments (`MOCK-606`) against such a schema is bounded by `catalogue.validationTimeout` (default 100 ms) and records a timeout outcome rather than hanging. `[derived]` | F |

#### MOCK-226 — Deterministic and explicitly non-deterministic list ordering
`[stated]` · Testability: **OK**

| # | Acceptance criterion | Level |
|---|---|---|
| 226.1 | `ordering: deterministic` (default) returns identical order across calls and runs. | F |
| 226.2 | `ordering: shuffled` returns a **different** order on successive calls with high probability (asserted over 10 calls on a 100-item list: ≥ 8 distinct orderings). | F |
| 226.3 | `shuffled` order is nonetheless reproducible for a given seed **and call ordinal** — a replay of the same call sequence yields the same sequence of orderings (ADR-002). | F |
| 226.4 | Shuffling never comes from Go map iteration; asserted by PRIN-1 across `GOMAXPROCS` values. | F |
| 226.5 | `ordering: reversed` and `ordering: byName` are also available. `[assumed]` — convenience, GAP-013. | F |

#### MOCK-227 — Catalogue drift
`[stated]` · Testability: **OK**

| # | Acceptance criterion | Level |
|---|---|---|
| 227.1 | A drift patch changes a named tool's `inputSchema`, `description` or `annotations`. | F |
| 227.2 | Trigger by control API (`POST /v1/instances/{n}/catalogue:drift`) takes effect on the next request. | I |
| 227.3 | Trigger `afterCalls: N` applies after the Nth matching call. | F |
| 227.4 | Trigger `afterSeconds: N` applies via the timer wheel. | F |
| 227.5 | `Snapshot.CatalogueGen` increments and is journaled on every subsequent request, so a test can partition the journal by generation. | F |
| 227.6 | Drift optionally emits `notifications/tools/list_changed` (`emitNotification: true`) or deliberately does not (feeding `MOCK-233`). | F |

#### MOCK-228 — Authorization-dependent catalogues
`[stated]` · Testability: **OK**

| # | Acceptance criterion | Level |
|---|---|---|
| 228.1 | Items carry `requiredScopes`; `tools/list` returns only items the presented token satisfies. | F |
| 228.2 | Two different tokens against the same instance yield different, correct tool sets. | F |
| 228.3 | Pagination is consistent within a principal: total count and cursors reflect the filtered set. | F |
| 228.4 | `cacheScope` emitted for a scope-filtered list is configurable and defaults to `"private"`. `[assumed]` — GAP-014. | F |
| 228.5 | The scope-filtered index is memoised bounded (ADR-004); ≥ 64 distinct scope sets do not cause unbounded growth (RSS asserted). | P |

### §2.2 Pagination and caching

#### MOCK-231 — Pagination with configurable page size, opaque cursors, cursor faults
`[stated]` · Testability: **OK**

| # | Acceptance criterion | Level |
|---|---|---|
| 231.1 | All list methods paginate; page size configurable globally and per method. | F |
| 231.2 | Cursors are opaque (AEAD-wrapped index + generation, ADR-010's construction reused) and are not guessable index integers. | U |
| 231.3 | Valid cursor: concatenation of all pages equals the full list exactly once, in order. | F |
| 231.4 | Expired cursor: a cursor older than `paging.cursorTTL` yields a configurable error (default `-32602` with `data.reason: "cursor_expired"`). | F |
| 231.5 | Erroring cursor: a fault can make a specific page return an arbitrary configured error. | F |
| 231.6 | Overlapping pages: a fault makes page N+1 repeat the last `k` items of page N; the exact overlap is configurable and asserted. | F |
| 231.7 | Missing items: a fault makes page N+1 skip `k` items; asserted. | F |
| 231.8 | A cursor minted by instance A is rejected by instance B. `[derived]` | F |

#### MOCK-232 — Per-method and per-page `ttlMs` / `cacheScope`
`[stated]` · Testability: **OK**

| # | Acceptance criterion | Level |
|---|---|---|
| 232.1 | `ttlMs` and `cacheScope` are configurable per method. | F |
| 232.2 | They are configurable **per page**, with different values on different pages of the same list. | F |
| 232.3 | `ttlMs` values `0`, negative (`-1`), absent, and `9007199254740991` are all expressible and emitted as configured (absent means key omitted). | F |
| 232.4 | `cacheScope` accepts arbitrary strings including unknown values, to test the hub's handling. | F |

#### MOCK-233 — Silent catalogue change within the advertised TTL
`[stated]` · Testability: **OK**

| # | Acceptance criterion | Level |
|---|---|---|
| 233.1 | A scenario advertises `ttlMs: 60000` then changes the catalogue at `t+5s` with **no** `list_changed` notification. | F |
| 233.2 | The journal makes the change point unambiguous via `Snapshot.CatalogueGen`, so a test can prove the hub served stale data after it. | F |
| 233.3 | The advertised `ttlMs` on responses after the change is configurable to be unchanged (the deceptive case) or updated. | F |

### §2.3 MRTR

#### MOCK-241 — `InputRequiredResult` with configurable rounds
`[stated]` · Testability: **OK**

| # | Acceptance criterion | Level |
|---|---|---|
| 241.1 | `tools/call`, `resources/read` and `prompts/get` can each answer with `InputRequiredResult`. | F |
| 241.2 | `mrtr.rounds: 0` completes immediately; `rounds: N` requires N input exchanges; `rounds: -1` **never** completes. | F |
| 241.3 | Round count is configurable per method and per primitive name. | F |
| 241.4 | The final round returns the normal result with the correct `resultType`. | F |
| 241.5 | Every round is journaled with `mrtr.round` and a shared `mrtr.chain`. | F |

#### MOCK-242 — All three input request types, singly and combined
`[stated]` · Testability: **OK**

| # | Acceptance criterion | Level |
|---|---|---|
| 242.1 | `elicitation/create` in **form** mode and in **URL** mode are both expressible. | F |
| 242.2 | `sampling/createMessage` is expressible; the mock emits the request and accepts any canned result (§8 non-goal: no real LLM). | F |
| 242.3 | `roots/list` is expressible. | F |
| 242.4 | Two or three types can appear in **one** `inputRequests` map simultaneously. | F |
| 242.5 | Map keys are configurable strings, including duplicated-looking and non-ASCII keys. | F |
| 242.6 | Partial `inputResponses` (a subset of keys) is handled per `MOCK-246`. | F |

#### MOCK-243 — AEAD `requestState` with four rejection classes
`[stated]` · Testability: **OK**

| # | Acceptance criterion | Level |
|---|---|---|
| 243.1 | `requestState` is AES-256-GCM per ADR-010; a single-bit flip anywhere in the token yields `reason: "tampered"`. Property-tested over all bit positions of a sample token. | U |
| 243.2 | It binds principal, request digest and TTL; the plaintext claim set matches ADR-010. | U |
| 243.3 | An expired token yields `reason: "expired"`. | F |
| 243.4 | A token presented by a different principal yields `reason: "cross_principal"`. | F |
| 243.5 | A token presented on a different request yields `reason: "cross_request"`. | F |
| 243.6 | A token from another instance yields `reason: "cross_instance"`. `[derived]` | F |
| 243.7 | A replayed round yields `reason: "replayed"`. `[derived]` | F |
| 243.8 | Every reason is recorded in the journal (`mrtr.verifyReason`), exposed in `mcpmock_requeststate_verify_total{result}`, and (when `switches.exposeStateReason`) in `error.data.reason`. | F |
| 243.9 | Nonces never repeat, including across a simulated restart in pinned-key deterministic mode. | U |

#### MOCK-244 — `requestState`-only and `inputRequests`-only; self-check
`[stated]` · Testability: **OK**

| # | Acceptance criterion | Level |
|---|---|---|
| 244.1 | A response with `requestState` and no `inputRequests` is producible. | F |
| 244.2 | A response with `inputRequests` and no `requestState` is producible. | F |
| 244.3 | A configuration producing **neither** is rejected — at scenario-validation time where statically detectable, and at emit time by the self-check (ADR-017 §5), which fails the test loudly. | U,F |
| 244.4 | The self-check is suppressible only by an explicit fault rule, and the suppression is journaled with the rule id. | F |

#### MOCK-245 — Capability-gated input requests; named non-conformant override
`[stated]` · Testability: **OK**

| # | Acceptance criterion | Level |
|---|---|---|
| 245.1 | In conformant mode, an input request type absent from `_meta.clientCapabilities` is **not** emitted; the mock instead returns a configured fallback (default: the normal result, or `-32021`). | F |
| 245.2 | `switches.nonConformant.emitUnsupportedInputRequest: true` emits it anyway. | F |
| 245.3 | The switch has a stable, documented name so a hub test can cite it. | F |
| 245.4 | The journal records which capabilities were declared and which input types were emitted, so the violation is provable from the journal alone. | F |

#### MOCK-246 — Re-asking on incomplete `inputResponses`
`[stated]` · Testability: **OK**

| # | Acceptance criterion | Level |
|---|---|---|
| 246.1 | A retry with a strict subset of the requested keys yields a **new** `InputRequiredResult`, not an error. | F |
| 246.2 | The new result requests only the still-missing keys (configurable: `reask: missing-only \| all`). | F |
| 246.3 | The new `requestState` shares the `chain` and increments `round`. | F |
| 246.4 | `mrtr.maxReasks` bounds the loop; exceeding it returns a configured error. `[assumed]` — GAP-016. | F |

#### MOCK-247 — Retry id distinctness assertion and journaling
`[stated]` · Testability: **TIGHTENED** — "MUST assert" is ambiguous for a server.

**Proposed testable form:** mcpmock **records** the initial and retry JSON-RPC ids on the MRTR
chain, **exposes** a chain-level boolean `retryIdsDistinct`, and **optionally** (per
`mrtr.enforceDistinctRetryIds`, default `false`) rejects a retry reusing the initial id with a
configured error. The *assertion* is `assert.AssertRetryIDsDistinct()` (`MOCK-603`).

| # | Acceptance criterion | Level |
|---|---|---|
| 247.1 | Every MRTR chain record carries `initialId` and `retryId` as raw JSON. | F |
| 247.2 | `GET /v1/instances/{n}/journal/correlations` reports `retryIdsDistinct` per chain. | I |
| 247.3 | `assert.AssertRetryIDsDistinct()` fails when any chain reuses an id. | U |
| 247.4 | With `enforceDistinctRetryIds: true`, a reused id returns the configured error and is journaled. | F |

### §2.4 Subscriptions

#### MOCK-251 — `subscriptions/listen` with filter, ack-first, id on every message
`[stated]` · Testability: **OK**

| # | Acceptance criterion | Level |
|---|---|---|
| 251.1 | `subscriptions/listen` opens a stream and the **first** frame is `notifications/subscriptions/acknowledged`. | F |
| 251.2 | The requested filter is honoured: notification types outside the filter are not delivered. | F |
| 251.3 | **Every** message on the stream, including the ack, carries `_meta["io.modelcontextprotocol/subscriptionId"]`. | F |
| 251.4 | The subscription id is deterministically minted (ADR-002) and unique per stream. | F |
| 251.5 | A non-conformant switch omits the ack, or omits the subscription id, or sends the ack second. | F |

#### MOCK-252 — Partial acknowledgment and refusal
`[stated]` · Testability: **OK**

| # | Acceptance criterion | Level |
|---|---|---|
| 252.1 | The ack can agree to a configured **subset** of the requested filter, and the subset is stated in the ack payload. | F |
| 252.2 | Refusal of specific notification types is configurable per type. | F |
| 252.3 | Refusal of the **entire** filter is expressible (ack with an empty accepted set). | F |
| 252.4 | Refused types are never subsequently delivered on that stream. | F |

#### MOCK-253 — Notifications on demand, on a timer, or on catalogue change
`[stated]` · Testability: **OK**

| # | Acceptance criterion | Level |
|---|---|---|
| 253.1 | `POST /v1/instances/{n}/notifications` fires a specified notification to matching subscribers within 50 ms. | I |
| 253.2 | Timer-driven emission at a configurable interval, via the shared wheel. | F |
| 253.3 | Catalogue change (`MOCK-227`) optionally emits the corresponding `*_changed`. | F |
| 253.4 | All four types are supported: `tools/list_changed`, `prompts/list_changed`, `resources/list_changed`, `resources/updated` (with a specific URI). | F |
| 253.5 | `resources/updated` targets a configurable URI set, including URIs not in the catalogue. | F |

#### MOCK-254 — Graceful and abrupt closure, per subscription
`[stated]` · Testability: **OK**

| # | Acceptance criterion | Level |
|---|---|---|
| 254.1 | Graceful: a final response with `resultType: "complete"` then a clean stream close. | F |
| 254.2 | Abrupt (HTTP): TCP RST via hijack + `SetLinger(0)`; the client observes a connection reset, not EOF. Requires HTTP/1.1 (ADR-012). | I |
| 254.3 | Abrupt (stdio): process exit, covered by `MOCK-508`. | I |
| 254.4 | Closure mode is selectable **per subscription**, via scenario and via `POST /v1/instances/{n}/streams:close`. | I |
| 254.5 | Closure is journaled with mode and elapsed stream lifetime. | F |

#### MOCK-255 — SSE keep-alive comments, configurable and disableable
`[stated]` · Testability: **OK**

| # | Acceptance criterion | Level |
|---|---|---|
| 255.1 | Keep-alive comment lines (`: <text>\n\n`) are emitted at `sse.keepAliveInterval` (default 15 s). | F |
| 255.2 | `sse.keepAliveInterval: 0` disables them entirely. | F |
| 255.3 | Comment text is configurable. | F |
| 255.4 | Keep-alives come from the shared timer wheel; a test at 1000 concurrent streams asserts the goroutine count is `≈ 2×streams + constant`, with no per-stream timer. | P |
| 255.5 | When a stream's frame channel is full, the keep-alive is dropped and `mcpmock_sse_frames_dropped_total{reason="slow_consumer"}` increments — the wheel never blocks. | P |

#### MOCK-256 — Many concurrent subscriptions; correct stdio interleaving
`[stated]` · Testability: **OK**

| # | Acceptance criterion | Level |
|---|---|---|
| 256.1 | ≥ 1000 concurrent subscriptions on one instance deliver all notifications to all matching subscribers. | P |
| 256.2 | On stdio, N concurrent subscriptions multiplex onto the single stdout channel with every frame correctly tagged by `subscriptionId`, and **no frame is interleaved mid-line**. Asserted by a fuzz-ordered test that reassembles per-subscription streams and checks each is well-formed and complete. | F |
| 256.3 | stdio frame writes are serialised through exactly one writer goroutine (ADR-006). | U |
| 256.4 | Notification ordering **within** a subscription is preserved; ordering **across** subscriptions is not guaranteed and this is documented. `[derived]` | F |

#### MOCK-257 — Named non-conformant subscription modes
`[stated]` · Testability: **OK**

| # | Acceptance criterion | Level |
|---|---|---|
| 257.1 | `nonConformant.sendUnrequestedNotificationType` delivers a type outside the acknowledged filter. | F |
| 257.2 | `nonConformant.sendRequestScopedNotificationOnListenStream` delivers a notification belonging to a different request's stream on the listen stream. | F |
| 257.3 | Both switches have stable documented names and are journaled when they fire. | F |

---

## §3 Protocol emulation — legacy era

### MOCK-301 — `2025-11-25` (and optionally `2025-06-18`, `2025-03-26`) era
`[stated]` · Testability: **TIGHTENED** — nine distinct behaviours in one bullet; split below.

| # | Acceptance criterion | Level |
|---|---|---|
| 301.1 | `initialize` → result → `notifications/initialized` handshake completes; the negotiated version is configurable. | F |
| 301.2 | `Mcp-Session-Id` is issued on `initialize` (deterministically minted) and echoed on subsequent responses. | F |
| 301.3 | A request with a missing/unknown/expired session id yields a configurable error (default HTTP `404`). | F |
| 301.4 | `DELETE` on the MCP path terminates the session; subsequent requests with that id fail. | F |
| 301.5 | `GET` on the MCP path opens the server→client SSE stream. | F |
| 301.6 | Every SSE event carries a monotonically increasing, deterministically minted `id:`. | F |
| 301.7 | `Last-Event-ID` on reconnect **replays** events after that id from a bounded per-session buffer (size configurable, default 1000); replayed bytes are identical to the originals. | F |
| 301.8 | `ping` is answered. | F |
| 301.9 | `logging/setLevel` changes the level of subsequent `notifications/message`. | F |
| 301.10 | `resources/subscribe` / `resources/unsubscribe` register and deregister URI subscriptions; `resources/updated` is delivered on the GET stream. | F |
| 301.11 | `2025-06-18` and `2025-03-26` are selectable; behavioural differences between them are enumerated in `contracts/wire-legacy-2025.md`. `[assumed]` — GAP-018. | F |

### MOCK-302 — Genuine server-initiated JSON-RPC requests on the SSE stream
`[stated]` · Testability: **OK**

| # | Acceptance criterion | Level |
|---|---|---|
| 302.1 | The mock sends `sampling/createMessage`, `elicitation/create` and `roots/list` as **requests** (with an id) on the legacy SSE stream. | F |
| 302.2 | It awaits the client's response, correlating by id, with a configurable timeout; timeout behaviour is configurable (error, ignore, retry). | F |
| 302.3 | Emission is triggerable by control API, by timer, and on receipt of a configured method. | I |
| 302.4 | The client's response is journaled as an inbound record with `direction: inbound-response` and linked to the outbound request. | F |
| 302.5 | Ids for server-initiated requests are deterministically minted and never collide with client ids. | U |

### MOCK-303 — Dual-era on one endpoint; strict single-era modes
`[stated]` · Testability: **OK**

| # | Acceptance criterion | Level |
|---|---|---|
| 303.1 | `era: dual` serves `initialize` **and** modern per-request `_meta` on the same path concurrently. | F |
| 303.2 | Era discrimination rules are stated in `contracts/wire-legacy-2025.md §era-detection` and are deterministic for every input, including ambiguous ones. `[assumed]` — GAP-019. | F |
| 303.3 | `era: modern` rejects `initialize` with a configurable response; `era: legacy` rejects modern `_meta` requests likewise. | F |
| 303.4 | Era is switchable at runtime (`PUT /v1/instances/{n}/era`) and takes effect on the next request. | I |
| 303.5 | The journal records the resolved era for every request. | F |

### MOCK-304 — Era-probe behaviours
`[stated]` · Testability: **OK**

| # | Acceptance criterion | Level |
|---|---|---|
| 304.1 | Each of six probe responses is selectable by a named key: bare `400` empty body; `400` non-JSON body; modern JSON-RPC error; `404`; `405`; timeout (no response for a configurable duration). | F |
| 304.2 | Probe behaviour is selectable per method and per era-detection request specifically. | F |
| 304.3 | The non-JSON body content is configurable (HTML, plain text, binary). | F |
| 304.4 | Timeout mode holds the connection open without writing any byte, for a configurable duration, then closes in a configurable way. | F |

### MOCK-305 — Expire or invalidate a legacy session mid-flight
`[stated]` · Testability: **OK**

| # | Acceptance criterion | Level |
|---|---|---|
| 305.1 | `POST /v1/instances/{n}/sessions/{sid}:invalidate` invalidates immediately. | I |
| 305.2 | `sessions.ttl` and `sessions.invalidateAfterCalls` provide scenario-driven expiry. | F |
| 305.3 | Requests after invalidation receive the configured session-error response. | F |
| 305.4 | In-flight requests at the moment of invalidation complete or fail according to a configured policy (`complete` \| `fail`), and the choice is journaled. `[assumed]` — GAP-020. | F |
| 305.5 | The GET SSE stream of an invalidated session is closed in a configurable way. | F |

### MOCK-306 — Legacy `-32002` resource-not-found
`[stated]` · Testability: **OK**

| # | Acceptance criterion | Level |
|---|---|---|
| 306.1 | `resources/read` for a configured "missing" URI returns JSON-RPC `-32002`. | F |
| 306.2 | The error `message` and `data` are configurable. | F |
| 306.3 | Emission is selectable per URI and per URI glob. | F |
| 306.4 | The same condition can be made to return `-32602` instead, so both sides of the hub's remap are testable. | F |

---

## §4 Authorization emulation

### MOCK-401 — Modes `none`, `bearer-static`, `oauth-resource-server`
`[stated]` · Testability: **OK**

| # | Acceptance criterion | Level |
|---|---|---|
| 401.1 | `none`: all requests accepted; a presented `Authorization` header is still journaled (hashed). | F |
| 401.2 | `bearer-static`: a configured token list is accepted; anything else yields `401`. Comparison is constant-time. | F |
| 401.3 | `oauth-resource-server`: full RFC 9728 behaviour per `MOCK-402`. | F |
| 401.4 | Mode is per instance and switchable at runtime. | I |

### MOCK-402 — RFC 9728 protected-resource metadata and `WWW-Authenticate`
`[stated]` · Testability: **OK**

| # | Acceptance criterion | Level |
|---|---|---|
| 402.1 | `GET /.well-known/oauth-protected-resource` returns JSON with configurable `authorization_servers`, `scopes_supported`, `resource`. | F |
| 402.2 | The path is served relative to the instance mount so a 200-instance fleet works. `[derived]` | F |
| 402.3 | An unauthenticated request yields `401` with `WWW-Authenticate: Bearer resource_metadata="…", scope="…"`. | F |
| 402.4 | The challenge is a single, well-formed header value; multiple challenges are expressible as a named non-conformant mode. | F |
| 402.5 | Additional/absent/malformed metadata fields are expressible for defensive testing. | F |

### MOCK-403 — Audience validation
`[stated]` · Testability: **OK**

| # | Acceptance criterion | Level |
|---|---|---|
| 403.1 | A token whose `aud` does not include the instance's canonical resource URI yields `401` with `error="invalid_token"` and a journaled reason `aud_mismatch`. | F |
| 403.2 | `aud` as a string and as an array are both handled. | F |
| 403.3 | The canonical URI is configurable per instance and defaults to the instance's external base URL. | F |
| 403.4 | **Passthrough detection:** a test in which the hub forwards the client's own token produces a journal record with `aud_mismatch` and a distinct `credential.hash` from the expected upstream credential — asserted by `assert.AssertHeaderNotValue` + `credential.hash` inequality. | F |
| 403.5 | Audience checking is switchable off, to test the hub against a permissive server. | F |

### MOCK-404 — Per-tool required scopes; `403 insufficient_scope`
`[stated]` · Testability: **OK**

| # | Acceptance criterion | Level |
|---|---|---|
| 404.1 | Scopes are configurable per tool, per prompt, per resource and per method. | F |
| 404.2 | Insufficient scope yields HTTP `403` with `WWW-Authenticate: Bearer error="insufficient_scope", scope="<full required set>"`. | F |
| 404.3 | The **full** required set appears in **one** challenge, not several. | F |
| 404.4 | Scope matching semantics (exact / prefix / space-delimited set) are stated in `contracts/` and configurable. `[assumed]` — GAP-014. | F |

### MOCK-405 — Embedded mock authorization server
`[stated, SHOULD]` · Testability: **OK**

| # | Acceptance criterion | Level |
|---|---|---|
| 405.1 | `/.well-known/oauth-authorization-server` metadata is served with configurable content. | I |
| 405.2 | `/.well-known/jwks.json` publishes the current signing key(s). | I |
| 405.3 | `POST /token` issues ES256 (default) or RS256 JWTs with configurable `aud`, `scope`, `exp`, `iss`, `sub`, `kid`. | I |
| 405.4 | RFC 9207 `iss` in the authorization response is configurable: present / absent / mismatched. | I |
| 405.5 | Deliberate malformations from ADR-020's named enum are all producible. | I |
| 405.6 | Signing keys are generated at runtime; **no private key exists in the repository or image**. Asserted by a repository scan in CI. | U |
| 405.7 | An end-to-end auth test runs with **no external IdP** and no network egress. | E |

### MOCK-406 — Token expiry mid-session, scope escalation, credential recording
`[stated]` · Testability: **OK**

| # | Acceptance criterion | Level |
|---|---|---|
| 406.1 | A token can be made to expire at a configured wall time or after N requests; subsequent requests get `401` with `invalid_token` / `expired`. | F |
| 406.2 | A scope-escalation flow is expressible: first call `403 insufficient_scope`, then a new token with wider scope is accepted. | F |
| 406.3 | **Every** request's presented credential is journaled with its hash, scheme, and (when a JWT parses) `aud`, `iss`, `sub`, `scope`, `exp`, `kid`. Never the raw token. | F |
| 406.4 | A change of credential between two requests is detectable from the journal by hash inequality. | F |

### MOCK-407 — Hashed `Authorization` echo into the journal
`[stated]` · Testability: **OK**

| # | Acceptance criterion | Level |
|---|---|---|
| 407.1 | `auth.echoCredential: true` records `credential.hash = HMAC-SHA256(perRunKey, rawHeaderValue)` truncated to 128 bits, hex. | F |
| 407.2 | The raw header value appears **nowhere**: not in the journal, not in logs, not in metrics, not in traces, not in the control API. Asserted by scanning all outputs for fixture token values. | F |
| 407.3 | Hashes are comparable **within** a run (same input ⇒ same hash) and, in deterministic mode, across runs (key derived from the seed). The distinction is documented. | F |
| 407.4 | A test can prove the hub minted a distinct upstream credential by asserting `hash(clientToken) != journal.credential.hash`. | F |

---

## §5 Fault and chaos injection

**§5 preamble** `[stated]` — every fault addressable by (method, primitive name, instance) selector
and by probability / count / control-API trigger.

| # | Acceptance criterion | Level |
|---|---|---|
| 5.p1 | Every action kind is reachable with each of the three selector dimensions, singly and combined. Verified by a matrix test over the fault catalogue. | F |
| 5.p2 | Every action kind is reachable with each of the three trigger types. Same matrix. | F |
| 5.p3 | Probability triggers are seeded and reproducible (ADR-002, per rule id). | F |
| 5.p4 | Count triggers are exact under serialised load; under concurrency they are exact only with `determinism.strict` — GAP-007. | F |
| 5.p5 | Every fired fault is journaled with its rule id and increments `mcpmock_faults_injected_total{kind,rule}`. | F |
| 5.p6 | Rules compose in list order; `stopPropagation` halts. | F |

### MOCK-501 — Latency
`[stated]` · Testability: **TIGHTENED** — "per-percentile injection" needs a definition.

**Proposed testable form:** `mode: percentile` takes a list of `{p, latency}` points defining an
inverse-CDF; the injected delay for a request is the interpolated value at a seeded uniform draw.
Verified by injecting 10 000 requests and asserting the measured p50/p90/p99 fall within ±15% of
the configured points.

| # | Acceptance criterion | Level |
|---|---|---|
| 501.1 | `mode: fixed` delays by exactly the configured duration (± 5 ms). | F |
| 501.2 | `mode: distribution` supports `normal`, `lognormal`, `exponential` with configurable parameters; measured mean within ±10% over 10 000 samples. | P |
| 501.3 | `mode: percentile` as defined above. | P |
| 501.4 | `slowFirstByte` delays the first byte only; subsequent bytes are prompt. | F |
| 501.5 | `slowSSEEvents` delays each event by a configured interval. | F |
| 501.6 | `stallAfterEvents: N` stops writing after N events without closing the stream. | F |
| 501.7 | All latency waits `select` on `ctx.Done()` and abort on cancellation (`MOCK-212`). | F |

### MOCK-502 — Transport faults
`[stated]` · Testability: **`DNS failure simulation` is NOT TESTABLE AS WRITTEN** — see GAP-009.

| # | Acceptance criterion | Level |
|---|---|---|
| 502.1 | Connection reset mid-stream: client observes `ECONNRESET`. Requires HTTP/1.1 (ADR-012). | I |
| 502.2 | Half-close: server closes its write side, keeps reading. | I |
| 502.3 | Response truncation mid-JSON at a configurable byte offset. | F |
| 502.4 | TCP accept refusal: the listener stops accepting; client sees `ECONNREFUSED`. Requires `listener: own` (ADR-007). | I |
| 502.5 | TLS handshake failure: configurable modes (wrong cert, expired cert, unknown CA, alert during handshake). | I |
| 502.6 | **DNS failure simulation** — *the mock is a server; it performs no DNS resolution, so it cannot fail its own.* Proposed replacement: (a) `mcpmock gen endpoint --unresolvable` emits an endpoint URL with a guaranteed-unresolvable hostname (`*.invalid`, RFC 6761) for the hub's configuration; and (b) an optional `mcpmock dnsfail` helper serving a local DNS responder returning `SERVFAIL`/`NXDOMAIN` for a configured zone, which the hub can be pointed at. **Requires a decision — GAP-009.** | I |
| 502.7 | Every transport fault is journaled at `phase: connection` or `phase: stream`, even when no complete HTTP request exists. | F |

### MOCK-503 — Protocol faults
`[stated]` · Testability: **OK**

| # | Acceptance criterion | Level |
|---|---|---|
| 503.1 | Each of the eleven named faults is a distinct named action, emitted byte-exactly: invalid JSON; JSON-RPC id mismatch; duplicate id; `null` id; missing `jsonrpc`; response to a notification; unsolicited response; unknown method; wrong `resultType`; unknown `resultType`; `input_required` on an unsupported method. | F |
| 503.2 | Each is produced via `FrameRawBytes` (ADR-006) so the typed path is not compromised. | U |
| 503.3 | The self-check (ADR-017 §5) is suppressed only for the specific rule, and the suppression is journaled. | F |
| 503.4 | Invalid-JSON content is configurable (truncated, extra comma, NaN, duplicate keys, BOM, control chars). | F |

### MOCK-504 — Size faults
`[stated]` · Testability: **OK**

| # | Acceptance criterion | Level |
|---|---|---|
| 504.1 | Oversized single result of a configurable size, up to at least 100 MiB, generated streaming (never fully buffered in mcpmock). | P |
| 504.2 | Oversized single SSE event of a configurable size. | P |
| 504.3 | Unbounded content-block count to a configurable N (≥ 100 000). | P |
| 504.4 | Deeply nested `structuredContent` to a configurable depth (≥ 10 000). | F |
| 504.5 | Scenario validation **rejects** `journal.bodies: full` combined with a size fault exceeding `journal.maxBytes/16` (ADR-005). | U |
| 504.6 | mcpmock's own RSS stays within a configured ceiling while emitting a 100 MiB result (asserted). | P |

### MOCK-505 — Error-code faults
`[stated]` · Testability: **OK**

| # | Acceptance criterion | Level |
|---|---|---|
| 505.1 | All JSON-RPC standard codes emittable: `-32700`, `-32600`, `-32601`, `-32602`, `-32603`. | F |
| 505.2 | All MCP codes emittable: `-32020`, `-32021`, `-32022`. | F |
| 505.3 | Retired codes emittable: `-32002`, `-32042`. | F |
| 505.4 | Arbitrary codes in the JSON-RPC reserved range `-32768…-32000` that are not defined, emittable — including at both range boundaries. `[derived: "reserved range" = JSON-RPC 2.0 §5.1]` | F |
| 505.5 | Codes **outside** the reserved range (application codes, positive codes, `0`) emittable. `[derived]` | F |
| 505.6 | `error.message` and `error.data` configurable per code, including absent `message` and non-object `data`. | F |

### MOCK-506 — Content faults
`[stated]` · Testability: **OK**

| # | Acceptance criterion | Level |
|---|---|---|
| 506.1 | `isError: true` tool results with configurable content. | F |
| 506.2 | `structuredContent` deliberately violating the declared `outputSchema`; the violation kind is configurable (wrong type, missing required, extra property, wrong enum). | F |
| 506.3 | Resource links to foreign URI schemes (configurable list, e.g. `file:`, `smb:`, `data:`, `gopher:`). | F |
| 506.4 | Icons with `javascript:`, `file:` and cross-origin-redirect URLs. mcpmock never dereferences any of them. `[derived — security.md §8]` | F |
| 506.5 | All content faults are gated by `hostile.enabled` where they carry active content (ADR-018 §7). | F |

### MOCK-507 — Prompt-injection corpus
`[stated]` · Testability: **OK** for mechanism; corpus **content** requires a human decision (GAP-015).

| # | Acceptance criterion | Level |
|---|---|---|
| 507.1 | Payloads are injectable into tool `description`, `annotations` and `server/discover` `instructions`. | F |
| 507.2 | The corpus is bundled, encoded at rest, and accompanied by a plaintext manifest (ADR-018). | U |
| 507.3 | `make corpus-verify` fails on any hash mismatch or unmanifested entry. | U |
| 507.4 | All three gates (`hostile.enabled`, rule `kind: corpus`, not `--safe-mode`) are required; failing any yields the `withheld` placeholder and a `WARN`. | F |
| 507.5 | Hidden-Unicode categories are represented and enumerated in the manifest (zero-width, bidi override, homoglyph, tag characters). Note `.golangci.yml:24` enables `bidichk` — the corpus package is excluded from it by path, and this exclusion is deliberate and documented. | U |
| 507.6 | When armed: `mcpmock_hostile_mode == 1`, `X-Mcpmock-Hostile: 1` on responses, startup `WARN`, `corpusIDs` in the journal. | F |
| 507.7 | The Helm chart refuses to render hostile mode without `networkPolicy.enabled: true`. | E |

### MOCK-508 — Restart faults
`[stated]` · Testability: **OK**

| # | Acceptance criterion | Level |
|---|---|---|
| 508.1 | stdio: process exit with in-flight requests, after a configurable delay; in-flight requests are journaled before exit and the journal is optionally flushed to a file. | I |
| 508.2 | HTTP: rolling restart behind a Service is demonstrated with a Helm-based e2e test on `docker-desktop`/`mcpmock-test`. | E |
| 508.3 | Slow shutdown that never closes streams: `shutdown.mode: hang` keeps streams open indefinitely after SIGTERM until `terminationGracePeriodSeconds` kills the pod. | E |
| 508.4 | Process exit is **refused** in embedded-library mode unless `WithAllowProcessExit()` was passed (ADR-009). | U |
| 508.5 | Graceful shutdown (default) closes streams with `resultType: "complete"` and drains within a configurable timeout. | I |

---

## §6 Request journal and assertions

### MOCK-601 — Record every received request with the full field set
`[stated]` · Testability: **OK**

| # | Acceptance criterion | Level |
|---|---|---|
| 601.1 | Every field is present: `wallTime` (RFC 3339 nanos), `monoNs` (since instance start), `transport`, **all** HTTP headers in wire order including duplicates, full body, decoded `_meta`, hashed credential, peer address, and the produced response. | F |
| 601.2 | Header capture preserves original casing and duplicates — `http.Header`'s canonicalisation must not be relied on. Asserted with a raw-socket client sending `x-mcp-header` twice with different casing. | F |
| 601.3 | Body capture modes `full \| truncate \| digest \| off` behave as ADR-005 specifies. | U |
| 601.4 | The recorded response includes status code, response headers, body (or digest), the ordered SSE frame list, and the close reason. | F |
| 601.5 | Credentials are hashed, never raw (`MOCK-407`). | F |
| 601.6 | Records also exist for requests rejected before dispatch (bad `_meta`, auth failure, header mismatch) and for connection-phase faults. | F |

### MOCK-602 — Journal exposed through the control API with filters, JSON and NDJSON
`[stated]` · Testability: **OK**

| # | Acceptance criterion | Level |
|---|---|---|
| 602.1 | `GET /v1/instances/{n}/journal` returns a paged JSON array. | I |
| 602.2 | `Accept: application/x-ndjson` (or `?format=ndjson`) returns newline-delimited records suitable for streaming. | I |
| 602.3 | `?follow=true` tails new records until the client disconnects. | I |
| 602.4 | Filters: `method`, `name`, `since`/`until`, `correlationId`, `traceId`, `chain`, `transport`, `era`, `status`, `faultRule`. Combinable (AND). | I |
| 602.5 | Filtering 100 000 records returns in < 200 ms p95. | P |
| 602.6 | `DELETE …/journal` clears it; `mcpmock_journal_records` returns to 0. | I |
| 602.7 | Records are returned in `Seq` order. | I |

### MOCK-603 — Go assertion helper API
`[stated]` · Testability: **OK**

| # | Acceptance criterion | Level |
|---|---|---|
| 603.1 | All eight named assertions exist with the signatures in `contracts/assertions-api.md`. | U |
| 603.2 | Each has a positive test (passes on conforming journal) and a negative test (fails, with a message naming the offending record's `Seq`, method and field). | U |
| 603.3 | `assert` imports only `journalapi` + stdlib + `testing`; `make deps-check` enforces it. | U |
| 603.4 | Assertions work against a journal loaded from an NDJSON **file**, with no running server. | U |
| 603.5 | Both fatal (`Assert*`, calls `t.Fatalf`, `t.Helper()`) and non-fatal (`Check*`, returns `error`) forms exist. | U |
| 603.6 | `AssertHeaderMatchesBody` covers **all** recorded requests and reports every mismatch, not just the first. | U |
| 603.7 | `AssertCapabilitiesNotWidened` compares against a declared capability set and reports each widened path. | U |
| 603.8 | Failure messages include a compact rendering of the offending record (method, `Seq`, relevant field) — not a full JSON dump. | U |

### MOCK-604 — Snapshot / golden comparison with configurable redaction
`[stated]` · Testability: **OK**

| # | Acceptance criterion | Level |
|---|---|---|
| 604.1 | `assert.AssertGolden(name, opts…)` compares the redacted journal against a golden file and writes a reviewable unified diff on mismatch. | U |
| 604.2 | Default redaction covers `wallTime`, `monoNs`, `durationNs`, `peer`, `credential.hash`, `requestState`, `sessionId`, `subscriptionId`, `eventId`. | U |
| 604.3 | Redaction is configurable by JSON Pointer, with `Drop`, `Replace(const)` and `Normalize(fn)` modes. | U |
| 604.4 | Golden update is explicit (`MCPMOCK_UPDATE_GOLDEN=1`) and prints a warning naming the file. | U |
| 604.5 | Golden files are deterministic across `GOMAXPROCS` and across runs (PRIN-1). | U |
| 604.6 | Where golden files for the **hub's** tests live is the hub team's choice; mcpmock ships only the comparison library — GAP-018. | — |

### MOCK-605 — Correlation view grouping MRTR chains
`[stated]` · Testability: **OK**

| # | Acceptance criterion | Level |
|---|---|---|
| 605.1 | `GET /v1/instances/{n}/journal/correlations` returns one object per `chain` with the ordered member records, round count, elapsed time, terminal outcome and `retryIdsDistinct`. | I |
| 605.2 | Grouping uses the mock-side `requestState.chain` (ADR-010), not client-supplied ids. | U |
| 605.3 | Chains with a rejected `requestState` still appear, with the rejection reason. | I |
| 605.4 | `journalapi.Correlation` is a public type usable from `assert`. | U |

### MOCK-606 — Per-request schema-validation outcome for `tools/call` arguments
`[stated]` · Testability: **OK**

| # | Acceptance criterion | Level |
|---|---|---|
| 606.1 | Every `tools/call` record carries `validation: {valid, errors[], schemaRef, durationNs}`. | F |
| 606.2 | Errors carry a JSON Pointer to the offending value and the failing keyword. | F |
| 606.3 | Validation uses the tool's **declared** `inputSchema` as emitted, including deliberately invalid ones (`MOCK-224`/`MOCK-225`); an unusable schema is recorded as `validation.status: "schema_unusable"`, not as a request error. | F |
| 606.4 | Validation is bounded by `catalogue.validationTimeout`; a timeout records `status: "timeout"` and does not affect the response. | F |
| 606.5 | Validation is disableable (`journal.validateArguments: false`) — it is on the `MOCK-902` path and costs CPU. | P |
| 606.6 | A test can prove the hub did not mutate arguments by comparing the recorded argument bytes to what the test sent. | F |

---

## §7 Configuration and control

### MOCK-701 — Single declarative file, versioned, schema-validated, `--validate`
`[stated]` · Testability: **OK**

| # | Acceptance criterion | Level |
|---|---|---|
| 701.1 | YAML and JSON inputs are accepted and produce identical internal state (asserted by comparing composed JSON trees). | U |
| 701.2 | `apiVersion` and `kind` are required; an unknown `apiVersion` is rejected with a clear message. | U |
| 701.3 | The JSON Schema is `go:embed`ed and identical to `specification/contracts/scenario.schema.json` (asserted by hash). | U |
| 701.4 | `additionalProperties: false` throughout: an unknown key fails validation. | U |
| 701.5 | `mcpmock validate <file>` exits `0` on success, `1` on validation error, `2` on I/O error, and supports `--output json` with JSON Pointer + source file/line. | U |
| 701.6 | Cross-field semantic rules are validated and reported in the same format. | U |
| 701.7 | Fields typed as `json.RawMessage` (`MOCK-222`/`224`/`225`) are explicitly exempt and documented as such in the schema. | U |

### MOCK-702 — Runtime mutation without restart
`[stated]` · Testability: **OK**

| # | Acceptance criterion | Level |
|---|---|---|
| 702.1 | Catalogue mutation (patch, regenerate) takes effect on the next request; `Snapshot.Gen` increments. | I |
| 702.2 | Firing notifications delivers to matching subscribers within 50 ms. | I |
| 702.3 | Toggling fault injectors (enable/disable/arm/fire) takes effect on the next matching request. | I |
| 702.4 | Switching protocol era takes effect on the next request. | I |
| 702.5 | Rotating credentials invalidates the old token set and accepts the new one. | I |
| 702.6 | Closing streams closes the selected set with the selected mode. | I |
| 702.7 | Clearing the journal empties it without disturbing in-flight requests. | I |
| 702.8 | **No** operation requires a restart or drops an unrelated in-flight request. Asserted by running a steady 500 rps load while executing every operation. | P |
| 702.9 | Mutations are rate-limited (default 100/s) returning `429` above; reads are not limited. | I |

### MOCK-703 — Scenario composition (base + overlays)
`[stated]` · Testability: **OK**

| # | Acceptance criterion | Level |
|---|---|---|
| 703.1 | `extends` resolves depth-first, left-to-right, later wins; cycles are detected and reported. | U |
| 703.2 | Merge semantics match ADR-008's table exactly, with a table-driven test per row. | U |
| 703.3 | `null` deletes a key. | U |
| 703.4 | Lists of objects merge by `name`; other lists replace; `$patch` overrides both. | U |
| 703.5 | Every mergeable object-list in the schema declares `x-mcpmock-merge-key` or `x-mcpmock-merge: replace`; a schema test enforces it. | U |
| 703.6 | A 200-instance fleet is defined in ≤ 30 lines using `generate:`. | U |
| 703.7 | Paths outside `--scenario-root` are rejected. | U |
| 703.8 | Post-composition validation, not pre-composition (an incomplete intermediate is legal). | U |

### MOCK-704 — `--seed` controlling every pseudo-random decision; printed at startup
`[stated]` · Testability: **OK**

| # | Acceptance criterion | Level |
|---|---|---|
| 704.1 | `--seed <uint64>` sets the root seed; absent, a random one is generated. | U |
| 704.2 | The effective seed is printed on **stderr** at startup as a structured record, and exposed at `GET /v1/seed` and as `mcpmock_effective_seed`. | F |
| 704.3 | PRIN-1 holds for every scenario in the library. | F |
| 704.4 | `make determinism-check` finds no unseeded randomness on a response path. | U |
| 704.5 | Known exceptions (count triggers, injected latency timing) are enumerated in documentation and in GAP-007 / GAP-008. | — |

### MOCK-705 — Library of named scenarios
`[stated, SHOULD]` · Testability: **OK**

| # | Acceptance criterion | Level |
|---|---|---|
| 705.1 | All ten named scenarios exist in `scenarios/`: happy-path, legacy-upstream, elicitation-flow, sampling-flow, roots-flow, large-catalogue, flaky-upstream, hostile-upstream, auth-protected-upstream, dual-era-upstream. | F |
| 705.2 | Each validates, starts, and is exercised by at least one functional test through the independent client. | F |
| 705.3 | Each is documented in `docs/scenarios.md` with its purpose and the `MOCK-nnn` it exercises. | — |
| 705.4 | Scenarios share a common `base.yaml` via `extends`, demonstrating `MOCK-703`. | U |
| 705.5 | `hostile-upstream` requires the ADR-018 gates and is excluded from the default CI scenario sweep. | F |

### MOCK-706 — Record/replay proxy mode
`[stated, SHOULD]` · Testability: **OK** but **scope decision required — GAP-012**

| # | Acceptance criterion | Level |
|---|---|---|
| 706.1 | `mcpmock record --upstream <url> --out fixtures/` proxies MCP traffic to a real server and writes a replayable fixture set. | I |
| 706.2 | `mcpmock serve --replay fixtures/` serves recorded responses, matching on method + canonical params digest, with configurable behaviour on a miss (error / passthrough / nearest). | I |
| 706.3 | Recording **redacts** credentials by default; a raw-capture mode requires an explicit flag and prints a warning. `[derived — security.md]` | I |
| 706.4 | Recorded fixtures are a documented, versioned format. | — |
| 706.5 | This makes mcpmock an MCP **client**, roughly doubling the protocol surface. Recommended for a late phase — GAP-012. | — |

---

## §8 Non-goals — recorded as explicit exclusions

| Exclusion | Consequence for design |
|---|---|
| Not a useful MCP server for real work | Tool bodies are limited to `echo`, `sleep`, `fail`, and canned content. No file, network or process side effects. |
| No LLM behind `sampling/createMessage` | The mock emits the request; the result is canned or supplied by the test through `Initiator`. |
| No performance parity with a production server | §9 targets are ceilings, not floors; no optimisation work beyond them. |

---

## §9 Performance requirements

**Common conditions** `[assumed]` unless stated, all escalated in GAP-006: Linux, 4 vCPU,
container CPU limit 4, `GOMAXPROCS=4`, HTTP/1.1 **plaintext** with keep-alive, log level `INFO`
(per-request logging off), tracing off, self-check off, `k6` as load generator on a separate host
or with pinned CPUs, 60-second steady-state after a 30-second warm-up, error rate < 0.1%.

### MOCK-901 — ≥20 000 `tools/call` rps, trivial handler, journaling off
`[stated]` · Testability: **TIGHTENED** — needs the conditions above.

| # | Acceptance criterion | Level |
|---|---|---|
| 901.1 | ≥ 20 000 rps sustained for 60 s under the common conditions, with p99 < 25 ms. | P |
| 901.2 | Fault list empty; the fault stage costs one nil check (asserted by a microbenchmark). | P |
| 901.3 | A TLS variant is measured and **reported**, with no pass/fail threshold — GAP-006. | P |
| 901.4 | Allocations per request on this path ≤ 12 (`-benchmem`, gated). | P |
| 901.5 | The result is published as a CI artifact on every tag. | — |

### MOCK-902 — ≥5000 rps with full journaling, bounded ring, configurable overflow
`[stated]` · Testability: **OK**

| # | Acceptance criterion | Level |
|---|---|---|
| 902.1 | ≥ 5000 rps for 60 s with `journal.bodies: full`, `validateArguments: true`, p99 < 50 ms. | P |
| 902.2 | The ring is bounded: RSS stabilises and does not grow after the ring fills. | P |
| 902.3 | `drop-oldest` increments `mcpmock_journal_dropped_total` and never blocks a request. | P |
| 902.4 | `block` blocks at most `journal.blockTimeout` then degrades to drop-oldest, incrementing a distinct counter — GAP-017. | P |
| 902.5 | Journal writes take no lock: asserted by `go test -race` under 5000 rps plus a `sync.Mutex`-absence review of the write path. | P |

### MOCK-903 — ≥20 000 concurrent open SSE streams
`[stated]` · Testability: **NOT TESTABLE AS WRITTEN when combined with `MOCK-904`** — GAP-005.

**Proposed testable form:** ≥ 20 000 concurrent SSE streams **per process**, distributed across
instances in any way. The literal reading ("per instance", × 200 instances = 4 000 000 streams)
is not achievable on any reasonable host and is presumed not intended.

| # | Acceptance criterion | Level |
|---|---|---|
| 903.1 | 20 000 concurrent streams held open for 5 minutes with keep-alives at 15 s; zero dropped streams. | P |
| 903.2 | RSS ≤ 1.5 GiB at 20 000 streams (ADR-012's envelope, stated as a target to be confirmed by measurement). | P |
| 903.3 | Goroutine count ≈ `2 × streams + constant`; no per-stream timer. | P |
| 903.4 | A broadcast notification reaches all 20 000 within 2 s. | P |
| 903.5 | `RLIMIT_NOFILE` is checked at startup and a `WARN` is logged if below `2 × expected streams`. | I |

### MOCK-904 — ≥200 logical instances in one process
`[stated]` · Testability: **OK**

| # | Acceptance criterion | Level |
|---|---|---|
| 904.1 | 200 instances start within the `MOCK-107` budget and all serve correctly. | P |
| 904.2 | Idle RSS for 200 instances × 5000 virtual tools ≤ 512 MiB `[assumed target]`. | P |
| 904.3 | Zero background goroutines per idle instance (`architecture.md §7.1`). | P |
| 904.4 | Per-instance journal budgets are derived from a fleet-wide total by default (ADR-007). | U |
| 904.5 | 500 instances is exercised as a stretch case and its behaviour documented (not a pass/fail). | P |

---

## §10 Deliverables

| ID | Deliverable | Acceptance criteria | Level |
|---|---|---|---|
| **DEL-1** | `mcpmock` Go module, library + CLI, semver | Module tagged `v0.x`; `go install` works; API documented; ADR-001's four public packages exist. | E |
| **DEL-2** | Container image with SBOM and **Cosign signature**, in the existing pipeline | ci.yml gains a `cosign` step. **VERIFIED GAP:** the current pipeline generates an SBOM (`ci.yml:609`) but contains **no Cosign step** — §10.2 is unmet by the existing workflow and must be added (`deployment.md §7`). Trivy CRITICAL/HIGH gate already exists (`ci.yml:665`). | E |
| **DEL-3** | Scenario library + JSON Schema | `MOCK-705` + `MOCK-701.3`. | F |
| **DEL-4** | Go assertion helpers importable by the gateway suite | `MOCK-603` + an example external module in `test/e2e/embed/`. | E |
| **DEL-5** | Conformance suite for the hub, wired into CI, across every era | **BLOCKED — the `HUB-nnn` requirements document is not in this repository.** mcpmock delivers the extension points (`MOCK-603`, `MOCK-705`) and a documented harness shape; the suite itself is an external dependency. GAP-004. | — |
| **DEL-6** | Documentation: quick start, scenario reference, fault catalogue, hub-requirement→scenario mapping | First three delivered. The mapping table is **blocked with DEL-5** — the `HUB-nnn` ids are unknown, and inventing them would be fabrication. A generated, empty-but-structured table is delivered for the hub team to populate. | — |

---

## §11 Coverage matrix

`requirements.md` §11 maps hub areas to mock capabilities. Because the `HUB-nnn` document is
absent (GAP-004), the **hub-side column cannot be verified**. The mock-side column is fully
covered by `traceability.md`. The §11 table is reproduced there verbatim with a
`[unverified — external document]` marker on every `HUB-nnn` reference. No `HUB-nnn` semantics are
invented anywhere in this specification.
