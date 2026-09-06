# Requirements: MCP Mock Server (test harness for the MCP hub)

**Working name:** `mcpmock`
**Purpose:** a programmable MCP server emulator used to test the gateway's hub mode
**Target protocol revisions:** `2026-07-28` (primary), `2025-11-25`, `2025-06-18`,
`2025-03-26` (legacy-era emulation)
**Keywords:** MUST / SHOULD / MAY per RFC 2119 / RFC 8174

---

## 0. Design principles

1. **Deterministic by default.** Same config + same seed ⇒ byte-identical responses. Every
   source of nondeterminism (ordering, ids, timestamps, jitter) MUST be seeded and
   reproducible.
2. **Programmable, not scripted-once.** Behavior is driven by a declarative scenario file and
   mutable at runtime through a control API, so a single test binary covers hundreds of cases.
3. **Observable from the test side.** The mock's most valuable output is not its responses but
   its **request journal** — the full record of what the hub actually sent. Most hub defects
   (token passthrough, header/body mismatch, capability widening, lost trace context) are only
   visible there.
4. **Deliberately non-conformant on demand.** The mock MUST be able to violate the spec in
   specific, named ways, so the hub's defensive paths are exercised.

---

## 1. Deployment and interfaces

- **MOCK-101** MUST be a single statically linked Go binary, no runtime dependencies, plus a
  distroless container image and a Helm chart / plain manifests.
- **MOCK-102** MUST support both transports in one binary:
  - `stdio` — for testing the hub's child-process upstreams;
  - `streamable-http` — POST MCP endpoint at a configurable path.
- **MOCK-103** MUST support running as N independent logical servers in one process
  (distinct endpoints/paths, distinct scenarios) so a large fleet can be emulated cheaply.
- **MOCK-104** MUST expose a **control API** (HTTP, separate port) for runtime mutation and
  inspection, and MUST expose the same operations through a CLI for stdio mode.
- **MOCK-105** MUST expose `/metrics` (Prometheus) and structured JSON logs on stderr.
- **MOCK-106** MUST support TLS and mTLS on the MCP listener, with configurable client-cert
  requirements, to test the hub's Vault-PKI upstream mode.
- **MOCK-107** MUST start in under 200 ms and MUST be usable as a Go library (`package mcpmock`)
  embedded directly in the hub's test suite, not only as a separate process.

---

## 2. Protocol emulation — modern era (`2026-07-28`)

- **MOCK-201** MUST implement `server/discover` returning configurable `supportedVersions`,
  `capabilities` (including `extensions`), `instructions`, `serverInfo`, `ttlMs`, `cacheScope`.
- **MOCK-202** MUST implement `tools/list`, `tools/call`, `prompts/list`, `prompts/get`,
  `resources/list`, `resources/templates/list`, `resources/read`, `completion/complete`,
  `subscriptions/listen`, with per-method enable/disable switches.
- **MOCK-203** MUST validate incoming `_meta` exactly as a conformant server would, and MUST
  reject with `-32602` / `400` when `protocolVersion` or `clientCapabilities` is missing —
  this is how the hub's upstream-side `_meta` construction gets verified.
- **MOCK-204** MUST validate `MCP-Protocol-Version`, `Mcp-Method`, `Mcp-Name` and
  `Mcp-Param-*` against the body and MUST return `-32020` (`HeaderMismatch`) / `400` on any
  mismatch, including Base64-sentinel decoding and numeric comparison of integers.
- **MOCK-205** MUST return `-32022` (`UnsupportedProtocolVersion`) with a configurable
  `supported` list when the requested version is not in its configured set.
- **MOCK-206** MUST return `-32021` (`MissingRequiredClientCapability`) with
  `data.requiredCapabilities` when a configured tool requires a capability absent from the
  request's `clientCapabilities`.
- **MOCK-207** MUST reject `GET`/`DELETE` on the MCP endpoint with `405`, MUST ignore
  `Mcp-Session-Id` and `Last-Event-ID`, and MUST NOT mint session ids in modern mode.
- **MOCK-208** MUST support both response shapes per request — single `application/json` and
  `text/event-stream` — selectable per method, per tool, or randomly under a seeded
  probability, and MUST send `X-Accel-Buffering: no` on SSE responses.
- **MOCK-209** MUST include `resultType` on every result and `serverInfo` in result `_meta`,
  with switches to omit either (backward-compatibility and defensiveness testing).
- **MOCK-210** MUST emit `notifications/progress` on the originating request's response
  stream, at a configurable count and interval, only when `progressToken` was supplied.
- **MOCK-211** MUST emit `notifications/message` only when `io.modelcontextprotocol/logLevel`
  was present on the request, honoring the requested minimum level.
- **MOCK-212** MUST treat closure of a response stream as cancellation, MUST stop work, and
  MUST record the cancellation in the journal with elapsed time.

### 2.1 Primitive catalogue generation

- **MOCK-221** MUST generate a configurable catalogue: `N` tools, `M` prompts, `K` resources
  and templates, with deterministic names, and MUST support catalogues of ≥5000 tools for the
  hub's aggregation and pagination limits.
- **MOCK-222** MUST support hand-written primitives alongside generated ones, defined in the
  scenario file with full control over `name`, `title`, `description`, `icons`,
  `annotations`, `inputSchema`, `outputSchema`.
- **MOCK-223** MUST support name edge cases on demand: maximum-length (128) names, names at
  129 characters, names containing dots, names with non-ASCII characters (forcing `Mcp-Name`
  Base64 sentinel encoding), names colliding with another mock instance's names, and names
  matching the sentinel pattern literally.
- **MOCK-224** MUST support `x-mcp-header` annotations, including deliberately invalid ones:
  empty, containing CR/LF, duplicated case-insensitively, applied to `number`, applied under
  `items` / `oneOf` / `$ref` (non-statically-reachable), and out-of-safe-range integers.
- **MOCK-225** MUST support schema edge cases: `$schema` draft-07, missing `$schema`,
  network `$ref` (to a loopback address, to a public host), deep nesting, large `$defs`, and
  composition-keyword bombs sized to a configurable validation cost.
- **MOCK-226** MUST support deterministic and explicitly non-deterministic ordering of list
  results (the latter to prove the hub's own ordering guarantee is independent of upstreams).
- **MOCK-227** MUST support **catalogue drift**: change a tool's schema, description or
  annotations at a controlled moment (control API or after N calls) to exercise the hub's
  drift detection and cache invalidation.
- **MOCK-228** MUST support authorization-dependent catalogues: return different tool sets per
  presented token/scope, to verify the hub's `cacheScope: "private"` handling.

### 2.2 Pagination and caching

- **MOCK-231** MUST paginate all list methods with configurable page size and opaque cursors,
  and MUST support: valid cursors, expired cursors, cursors that error, and cursors that
  return overlapping or missing items between pages.
- **MOCK-232** MUST emit `ttlMs` and `cacheScope` per method — including per page, with
  different values on different pages — and MUST support `ttlMs` of `0`, negative, absent, and
  very large.
- **MOCK-233** MUST support a mode where the catalogue silently changes within the advertised
  TTL, so tests can detect the hub serving stale data past an invalidation signal.

### 2.3 MRTR

- **MOCK-241** MUST be able to answer `tools/call`, `resources/read` and `prompts/get` with
  `InputRequiredResult`, with a configurable number of rounds before completion (0..N,
  including "never completes").
- **MOCK-242** MUST support all three input request types — `elicitation/create` (form and URL
  modes), `sampling/createMessage`, `roots/list` — singly and in combination in one
  `inputRequests` map, with configurable keys.
- **MOCK-243** MUST mint `requestState` as an AEAD-protected blob binding principal, request
  digest and TTL, and MUST verify it on retry: tampered, expired, cross-principal and
  cross-request state MUST be rejected with a recorded, inspectable reason.
- **MOCK-244** MUST support `requestState`-only responses (no `inputRequests`) and
  `inputRequests`-only responses, and MUST reject its own responses that contain neither
  (self-check).
- **MOCK-245** MUST refuse to emit an input request type absent from the request's
  `clientCapabilities` in conformant mode, and MUST emit it anyway in a named non-conformant
  mode (to verify the hub blocks it per HUB-206).
- **MOCK-246** MUST support re-asking: return a new `InputRequiredResult` when `inputResponses`
  are incomplete, rather than erroring.
- **MOCK-247** MUST assert and journal that the retry's JSON-RPC id differs from the initial
  request's id.

### 2.4 Subscriptions

- **MOCK-251** MUST implement `subscriptions/listen`, honoring the filter, acknowledging with
  `notifications/subscriptions/acknowledged` first and carrying
  `io.modelcontextprotocol/subscriptionId` on every message.
- **MOCK-252** MUST support partial acknowledgment (agreeing to a subset of the requested
  filter) and refusal of specific notification types.
- **MOCK-253** MUST emit notifications on demand (control API), on a timer, or on catalogue
  change: `tools/list_changed`, `prompts/list_changed`, `resources/list_changed`,
  `resources/updated` for specific URIs.
- **MOCK-254** MUST support graceful closure (final `resultType: "complete"` response then
  close) and abrupt closure (TCP reset / process exit), selectable per subscription.
- **MOCK-255** MUST emit SSE keep-alive comment lines at a configurable interval, and MUST
  support disabling them to test the hub's idle-timeout behavior.
- **MOCK-256** MUST support many concurrent subscriptions and MUST correctly interleave and
  tag them on stdio's single channel.
- **MOCK-257** MUST support a named non-conformant mode that sends an unrequested notification
  type, and one that sends a request-scoped notification on the listen stream.

---

## 3. Protocol emulation — legacy era

- **MOCK-301** MUST emulate the `2025-11-25` (and optionally `2025-06-18`, `2025-03-26`) era:
  `initialize` / `notifications/initialized` handshake, `Mcp-Session-Id` issuance and
  validation, `DELETE` session termination, GET SSE stream, `Last-Event-ID` resumption with
  replay, `ping`, `logging/setLevel`, `resources/subscribe` / `unsubscribe`.
- **MOCK-302** MUST be able to send genuine server-initiated JSON-RPC requests
  (`sampling/createMessage`, `elicitation/create`, `roots/list`) on the SSE stream and await
  the client's response — this is what exercises the hub's legacy→MRTR conversion.
- **MOCK-303** MUST support dual-era mode on one endpoint (serving `initialize` and modern
  per-request `_meta` concurrently) and MUST support strictly single-era modes.
- **MOCK-304** MUST support era-probe behaviors: reply to a modern request with a bare `400`
  and empty body, with a `400` and a non-JSON body, with a modern JSON-RPC error, with `404`,
  with `405`, or with a timeout — covering every branch of the hub's era-detection logic.
- **MOCK-305** MUST be able to expire or invalidate a legacy session mid-flight, forcing the
  hub to re-initialize without losing downstream requests.
- **MOCK-306** MUST emit legacy `-32002` resource-not-found so the hub's `-32602` remapping is
  testable.

---

## 4. Authorization emulation

- **MOCK-401** MUST support modes: `none`, `bearer-static`, `oauth-resource-server`.
- **MOCK-402** In `oauth-resource-server` mode MUST publish
  `/.well-known/oauth-protected-resource` (RFC 9728) with configurable
  `authorization_servers`, `scopes_supported` and `resource`, and MUST return `401` with a
  `WWW-Authenticate` challenge containing `resource_metadata` and `scope`.
- **MOCK-403** MUST validate token audience and MUST reject tokens whose audience is not the
  mock's canonical URI — this is the primary detector for hub token passthrough.
- **MOCK-404** MUST support per-tool required scopes and MUST return `403` with
  `error="insufficient_scope"` and the full required scope set in one challenge.
- **MOCK-405** SHOULD embed a minimal mock authorization server (metadata document, token
  endpoint issuing signed JWTs with configurable `aud`, `scope`, `exp`, and RFC 9207 `iss`
  behavior) for end-to-end tests without external dependencies.
- **MOCK-406** MUST support token expiry mid-session and scope escalation flows, and MUST
  record which credential the hub presented on each request.
- **MOCK-407** MUST support a mode that echoes back the received `Authorization` header value
  (hashed) into the journal so tests can assert the hub minted a distinct upstream credential.

---

## 5. Fault and chaos injection

Every fault MUST be addressable by (method, primitive name, upstream instance) selector and by
probability, count, or explicit control-API trigger.

- **MOCK-501** Latency: fixed, distribution-based, or per-percentile injection; slow first
  byte; slow SSE events; stall after N events.
- **MOCK-502** Transport faults: connection reset mid-stream, half-close, response truncation
  mid-JSON, TCP accept refusal, DNS failure simulation, TLS handshake failure.
- **MOCK-503** Protocol faults: invalid JSON, JSON-RPC id mismatch, duplicate id, `null` id,
  missing `jsonrpc`, response to a notification, unsolicited response, unknown method,
  wrong `resultType`, unknown `resultType`, `input_required` on an unsupported method.
- **MOCK-504** Size faults: oversized single result, oversized SSE event, unbounded content
  block count, deeply nested `structuredContent`.
- **MOCK-505** Error faults: every JSON-RPC standard code, every MCP code
  (`-32020`, `-32021`, `-32022`), retired codes (`-32002`, `-32042`), and codes from the
  reserved range that the spec does not define — verifying the hub does not propagate them.
- **MOCK-506** Content faults: `isError: true` tool results, `structuredContent` violating the
  declared `outputSchema`, resource links to foreign URI schemes, icons with
  `javascript:` / `file:` / cross-origin-redirect URLs.
- **MOCK-507** Prompt-injection payloads: tool descriptions, `annotations` and `instructions`
  containing instruction-like text and hidden Unicode, drawn from a bundled corpus, for
  testing the hub's untrusted-content handling and any sanitization layer.
- **MOCK-508** Restart faults: process exit with in-flight requests (stdio), rolling restart
  behind a service (HTTP), and slow shutdown that never closes streams.

---

## 6. Request journal and assertions

- **MOCK-601** MUST record every received request with: wall-clock and monotonic timestamps,
  transport, all HTTP headers, full body, decoded `_meta`, presented credential (hashed),
  peer address, and the response the mock produced.
- **MOCK-602** MUST expose the journal through the control API with filtering (by method,
  name, time range, correlation id) in JSON and as newline-delimited JSON for streaming.
- **MOCK-603** MUST provide a Go assertion helper API over the journal, at minimum:
  - `AssertNoHeader(name)` / `AssertHeaderNotValue(name, value)` — token passthrough checks;
  - `AssertHeaderMatchesBody()` — mirrored-header integrity over all recorded requests;
  - `AssertMetaField(path, matcher)` — protocol version, client capabilities, client info;
  - `AssertTraceContextPropagated(traceID)` — `traceparent` continuity;
  - `AssertCapabilitiesNotWidened(declared)` — hub did not advertise more than the client did;
  - `AssertNoSessionHeaders()` — modern mode cleanliness;
  - `AssertRequestCount(selector, n)` — de-duplication and cache-hit verification;
  - `AssertRetryIDsDistinct()` — MRTR id discipline.
- **MOCK-604** MUST support snapshot/golden-file comparison of the journal with configurable
  redaction of volatile fields, so a hub behavior change surfaces as a reviewable diff.
- **MOCK-605** MUST expose a correlation view that groups an initial request with its MRTR
  retries via the mock-side `requestState` identity.
- **MOCK-606** MUST record and expose per-request the schema-validation outcome for
  `tools/call` arguments, so tests can verify the hub did not mutate arguments.

---

## 7. Configuration and control

- **MOCK-701** Scenario configuration MUST be a single declarative YAML/JSON file, versioned,
  schema-validated on load, with a `--validate` mode that exits non-zero on error.
- **MOCK-702** The control API MUST allow, at runtime, without restart: mutating the
  catalogue, firing notifications, toggling fault injectors, switching protocol era, rotating
  credentials, closing streams, and clearing the journal.
- **MOCK-703** MUST support scenario composition (base + overlays) so a fleet of N mocks is
  defined without copy-paste.
- **MOCK-704** MUST accept a `--seed` flag controlling every pseudo-random decision, and MUST
  print the effective seed at startup.
- **MOCK-705** SHOULD ship a library of named scenarios covering, at minimum: happy path,
  legacy upstream, elicitation flow, sampling flow, roots flow, large catalogue, flaky
  upstream, hostile upstream (MOCK-507), auth-protected upstream, and dual-era upstream.
- **MOCK-706** SHOULD support recording a real MCP server's responses and replaying them
  (record/replay proxy mode) to build regression fixtures from production servers.

---

## 8. Non-goals

- Being a useful MCP server for real work (no real tool side effects beyond echo/sleep/fail).
- Implementing the LLM side of `sampling/createMessage` (it emits the request; a canned or
  test-supplied result is enough).
- Performance parity with a production server; the mock only needs to sustain the hub's
  target load (§9).

---

## 9. Performance requirements

- **MOCK-901** MUST sustain ≥20 000 `tools/call` requests/s per instance on 4 vCPU with a
  trivial handler and journaling disabled.
- **MOCK-902** MUST sustain ≥5000 requests/s with full journaling to memory, with a bounded
  ring buffer and configurable overflow policy (drop-oldest or block).
- **MOCK-903** MUST hold ≥20 000 concurrent open SSE streams per instance.
- **MOCK-904** MUST support ≥200 logical server instances in one process for fleet emulation.

---

## 10. Deliverables

1. `mcpmock` Go module: library + CLI, published with semantic versioning.
2. Container image with SBOM and Cosign signature, built in the existing GitHub Actions
   pipeline (Trivy, golangci-lint, SonarCloud).
3. Scenario library (§MOCK-705) and JSON Schema for the scenario file.
4. Go assertion helpers (§MOCK-603) importable by the gateway's test suite.
5. Conformance test suite for the hub, wired into CI, running the acceptance criteria of the
   hub requirements document against the scenario library across every supported protocol era.
6. Documentation: quick start, scenario reference, fault catalogue, and a mapping table from
   each hub requirement ID to the scenarios that cover it.

---

## 11. Coverage matrix (hub requirement → mock capability)

| Hub area | Hub reqs | Mock capabilities |
| --- | --- | --- |
| Statelessness / no sessions | HUB-103…109, 501 | MOCK-207, 301, 903 |
| Per-request `_meta` | HUB-121…128 | MOCK-203, 205, 206, 603 |
| Header mirroring | HUB-141…148 | MOCK-204, 223, 224, 603 |
| Aggregation & namespacing | HUB-161…169 | MOCK-221…228, 103, 904 |
| Caching | HUB-181…186 | MOCK-231…233 |
| MRTR | HUB-201…209 | MOCK-241…247, 605 |
| Subscriptions | HUB-221…229 | MOCK-251…257 |
| Cancellation & timeouts | HUB-241…244 | MOCK-210, 212, 501, 502 |
| Authorization | HUB-301…310 | MOCK-401…407 |
| Security / untrusted content | HUB-401…410 | MOCK-225, 505, 506, 507 |
| Degraded operation | HUB-167, 504 | MOCK-501, 502, 508 |
| Era bridging | HUB-701…724 | MOCK-301…306 |
| Extensions | HUB-801…804 | MOCK-201 (`extensions`), scenario library |
