# Changelog

All notable changes to this project are documented in this file.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project intends to use [Semantic Versioning](https://semver.org/)
once a first version is tagged. **No version has been tagged yet** — the
work below is recorded under `Unreleased` and no release date or version
number is invented for it.

## [Unreleased]

Phase 1 of a 12-phase implementation plan (a "walking skeleton"): one
transport pair, one method family, three built-in tools, a working
determinism/journal/control/observability spine, and packaging. See
[`docs/not-yet-implemented.md`](docs/not-yet-implemented.md) for what this
excludes.

### Added

- `mcpmock serve`: run the mock server over `streamable-http` (`POST` only,
  default `/mcp`) and/or `stdio`, with `--seed`, `--path`, `--transport`,
  `--listen`, `--metrics-listen`, `--control-listen`, `--control-socket`,
  `--control-token-file`, `--no-control`, `--no-validate`, `--scenario-root`
  and `--log-level` flags. `GET`/`DELETE` on the MCP path return `405` by
  design (`MOCK-207`).
- `server/discover`, `tools/list`, `tools/call` methods, with three built-in
  tool behaviors: `echo` (canonical-JSON echo with type-preserving
  `structuredContent`), `sleep` (millisecond delay, bounded, cancellable,
  reject-or-clamp on overrun), `fail` (scenario-selected `toolError` or
  `protocolError`, not caller-selected by default).
- `switches.validateMeta` — `strict` (reject a request missing
  `_meta.protocolVersion`/`_meta.clientCapabilities` with `-32602`/HTTP 400
  and `error.data.missing`), `lenient` (accept and record the omission),
  `off`.
- `switches.omitResultType`, `switches.omitServerInfoMeta`,
  `switches.methods.<name>.hideFromCapabilities`, `switches.selfCheck`
  (reject the mock's own malformed outgoing result before it is sent).
- Scenario configuration: a single declarative YAML/JSON document, validated
  against a JSON Schema before serving, with `extends` composition
  (base + overlays, keyed strategic merge, `null` deletes) bounded by
  `--scenario-root`.
- `mcpmock validate [--output text|json] <file>`: schema and semantic
  validation with exit codes `0` valid / `1` validation error / `2` I/O or
  usage error.
- Request journal: a bounded, sharded, seqlocked ring per instance, with
  configurable overflow policy, body-capture modes (`full`/`truncate`/
  `digest`/`off`), and filtering by method, name, transport, era, time range
  and correlation id. Credentials are recorded as a hash and non-sensitive
  claims only — the raw value is unrepresentable in the record type.
- Control API: 8 routes (`getOpenAPI`, `getSeed`, `getHealth`,
  `listInstances`, `getInstance`, `getJournal`, `clearJournal`,
  `getCorrelations`) served over HTTP and a unix domain socket, loopback by
  default. Binding to a non-loopback address without a configured token is
  refused at startup.
- `mcpmock ctl <verb>`: a CLI front end over the same control route table
  (`instances`, `instance`, `seed`, `health`, `journal`, `correlations`,
  `clear-journal`).
- Go library API: `mcpmock.New`/`NewFromFile`/`NewFromScenario`, the full
  `With*` option set, and `mcpmock.StartTest(tb, opts...)` for in-process
  hub test suites.
- `assert` package: three journal assertion families
  (`AssertNoHeader`/`CheckNoHeader`, `AssertHeaderNotValue`/
  `CheckHeaderNotValue`, `AssertRequestCount`/`CheckRequestCount` with
  `AtMost`/`AtLeast` variants), each in a fatal (`TB`) and non-fatal
  (`Asserts`) form, plus `FromFile`/`FromReader` to load an NDJSON journal
  export with no server running.
- Observability: Prometheus `/metrics` (897 series observed on a
  single-instance run with one request served), `/healthz` (liveness),
  `/readyz` (readiness) on a dedicated listener (default `:9090`,
  overridable via `--metrics-listen` or `MCPMOCK_METRICS_LISTEN`);
  structured JSON logs on stderr; OTLP trace export wiring.
- Determinism: `--seed` (`uint64`); the effective seed is printed at startup
  and readable via `GET /v1/seed`; verified byte-identical `tools/call`
  responses across two independent processes given the same seed and
  request.
- Packaging: a distroless, non-root (uid/gid `65532`), `CGO_ENABLED=0`
  static-binary container image (`mcpmock:dev-local`, 18.5 MB), a Helm chart
  (`helm/mcpmock`) with a default-deny `NetworkPolicy`, plain Kubernetes
  manifests, and a GitHub Actions CI workflow.

### Fixed

- `internal/journal`: an intermittently failing sequence-ordering test
  caused by a shard-sizing assumption (DEF-002, DEF-003).
- `mcpmock serve` did not start the observability listener at all — `/metrics`,
  `/healthz`, `/readyz` were unreachable regardless of flags (DEF-008).
- `switches.validateMeta` was not wired from a loaded scenario file into the
  running handler pipeline (DEF-005).
- `switches.omitResultType`/`omitServerInfoMeta`/`hideFromCapabilities` were
  accepted by the schema but silently ignored at runtime (DEF-209, DEF-010,
  DEF-011).
- The Helm chart's default inline scenario used `spec.transport` as a bare
  string, which fails schema validation (`transport` is an object); a bare
  `helm install` never reached `Ready` (DEF-012). The chart's default is now
  the object form and validates cleanly.

### Known limitations

- The control API's bearer token gates only the *non-loopback bind* at
  startup; it is **not enforced on individual requests** in this build — see
  [`docs/control-api.md#security`](docs/control-api.md#security). Do not
  expose the control port off-host expecting authentication.
- Performance figures (`MOCK-901`–`903`) are measured on one development
  laptop, not the specified 4-vCPU target; `MOCK-901`'s throughput/latency
  gate and `MOCK-903`'s 20,000-stream count are **indeterminate on this
  host** — see [`docs/deployment.md#measured-performance`](docs/deployment.md#measured-performance).
- The modern-era wire format (`server/discover`, `resultType`, error codes
  `-32020`–`-32022`) is this project's own authored, unratified annex, not a
  confirmed public MCP specification — see the
  [README's GAP-003 note](README.md#-the-wire-format-is-not-a-public-standard).
- Everything under [`docs/not-yet-implemented.md`](docs/not-yet-implemented.md)
  (pagination, the full catalogue engine, MRTR, subscriptions, legacy-era
  emulation, authorization, fault/chaos injection, TLS, the remaining
  scenario library and assertion helpers) does not exist in this build.

[Unreleased]: https://github.com/vyrodovalexey/mcp-mock-server/compare/HEAD
