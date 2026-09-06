---
title: mcpmock — Observability Design
status: draft
version: 0.1.0
updated: 2026-09-04
requirements: MOCK-105, MOCK-603, MOCK-901, MOCK-902, MOCK-904
adr: ADR-016, ADR-011, ADR-007
---

# Observability

Three signals with three different jobs:

| Signal | Job | Consumer |
|---|---|---|
| **Journal** | Evidence about the *hub*. The product (§0.3). | The test author. |
| **Metrics** | Health and load of *mcpmock*. | The operator, and the perf gates. |
| **Traces** | Causality within a request, and `traceparent` continuity evidence. | The test author (via the journal) and the operator. |
| **Logs** | Lifecycle, mutation, and things that went wrong. | Both. |

The journal is specified in `data-model.md §3`. This document covers the other three.

---

## 1. Principles

1. **Nothing global** (ADR-007): one registry, one logger, one tracer provider per `Server`.
2. **Zero cost when off** (ADR-016): no-op tracer by default; no exporter dialled until the first
   span; `DEBUG` logging off by default.
3. **Bounded cardinality** — enforced at scenario-validation time, not discovered in production.
4. **Stderr only** (ADR-011): stdout belongs to the stdio transport.
5. **Never a credential** (`security.md §5`): structurally, not by convention.

---

## 2. Metrics

Prometheus text exposition at `GET /metrics` on the observability listener (default `:9090`).
All names are prefixed `mcpmock_`.

### 2.1 Cardinality budget

| Label | Domain size | Notes |
|---|---|---|
| `instance` | ≤ 200 (`MOCK-904`) | |
| `transport` | 2 | `http`, `stdio` |
| `era` | 4 | `modern`, `legacy`, `dual`, `probe` |
| `method` | ~14 | fixed enum; an unknown method maps to `other` |
| `outcome` | 8 | `ok`, `error`, `cancelled`, `fault`, `auth_denied`, `validation_failed`, `timeout`, `panic` |
| `rule` | ≤ 200 | fault rule ids, bounded by scenario validation |
| `result` | ≤ 8 | requestState verify reasons |
| **`name` (primitive name)** | **NEVER A LABEL** | 5000 tools × 200 instances = 1 M series. Use the journal. |

Worst case for the main histogram: `200 × 2 × 4 × 14 × 8 ≈ 179 200` series — above the default
ceiling. Resolution: `mcpmock_request_duration_seconds` carries `{instance, method, outcome}`
only (200 × 14 × 8 = 22 400), and `transport`/`era` live on the cheaper counter.
`scenario.Validate()` computes the worst case and **fails** above
`observability.maxMetricSeries` (default 100 000), naming the responsible label.

### 2.2 Catalogue

#### Process

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `mcpmock_build_info` | Gauge (=1) | `version`, `commit`, `goversion` | Build identification |
| `mcpmock_effective_seed` | Gauge | — | `MOCK-704`; the seed as a float64 (documented as lossy above 2^53; the authoritative value is `GET /v1/seed` and the startup log) |
| `mcpmock_startup_duration_seconds` | Gauge | `phase` | `config`, `validate`, `instances`, `listeners`, `total` — directly gates `MOCK-107` |
| `mcpmock_instances` | Gauge | — | `MOCK-904` |
| `mcpmock_safe_mode` | Gauge (0/1) | — | ADR-018 |
| `mcpmock_hostile_mode` | Gauge (0/1) | `instance` | ADR-018 §4 |
| `mcpmock_stdout_leak_bytes_total` | Counter | — | ADR-011; **must be 0** |
| `mcpmock_goroutines` | Gauge | — | supports the `MOCK-903`/`MOCK-255.4` goroutine assertions |

#### Requests

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `mcpmock_requests_total` | Counter | `instance`, `transport`, `era`, `method`, `outcome` | `MOCK-901` |
| `mcpmock_request_duration_seconds` | Histogram | `instance`, `method`, `outcome` | buckets `.001 .0025 .005 .01 .025 .05 .1 .25 .5 1 2.5 5 10`; native histograms when available |
| `mcpmock_request_body_bytes` | Histogram | `instance`, `method` | |
| `mcpmock_response_body_bytes` | Histogram | `instance`, `method` | `MOCK-504` |
| `mcpmock_requests_in_flight` | Gauge | `instance` | |
| `mcpmock_validation_errors_total` | Counter | `instance`, `kind` | `kind`: `meta_missing`, `header_mismatch`, `unsupported_version`, `missing_capability` — `MOCK-203`…`206` |

#### Streams and subscriptions

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `mcpmock_sse_streams_open` | Gauge | `instance`, `kind` | `kind`: `request`, `listen`, `legacy_get`. **The `MOCK-903` gate.** |
| `mcpmock_sse_streams_total` | Counter | `instance`, `kind`, `close_reason` | `close_reason`: `complete`, `client_gone`, `fault`, `abrupt`, `shutdown` |
| `mcpmock_sse_events_total` | Counter | `instance`, `type` | `type`: `result`, `notification`, `keepalive`, `error` |
| `mcpmock_sse_frames_dropped_total` | Counter | `instance`, `reason` | `reason`: `slow_consumer`, `closed`. **Non-zero means evidence was lost.** |
| `mcpmock_sse_stream_duration_seconds` | Histogram | `instance`, `kind` | |
| `mcpmock_subscriptions_active` | Gauge | `instance` | `MOCK-256` |
| `mcpmock_notifications_sent_total` | Counter | `instance`, `type` | `MOCK-253` |

#### Journal (`MOCK-902`)

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `mcpmock_journal_records` | Gauge | `instance` | current ring occupancy |
| `mcpmock_journal_records_total` | Counter | `instance` | written |
| `mcpmock_journal_dropped_total` | Counter | `instance`, `policy` | `policy`: `drop_oldest`, `block_timeout`. **The honesty metric.** |
| `mcpmock_journal_bytes` | Gauge | `instance` | |
| `mcpmock_journal_write_duration_seconds` | Histogram | `instance` | proves the `MOCK-902` claim that writes are cheap |
| `mcpmock_journal_block_seconds_total` | Counter | `instance` | time spent blocked under `overflow: block` |
| `mcpmock_journal_query_duration_seconds` | Histogram | `instance` | `MOCK-602.5` |
| `mcpmock_argument_validation_duration_seconds` | Histogram | `instance`, `status` | `MOCK-606` |

#### Faults, auth, MRTR, catalogue

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `mcpmock_faults_matched_total` | Counter | `instance`, `rule` | selector matched |
| `mcpmock_faults_injected_total` | Counter | `instance`, `rule`, `kind` | trigger fired — §5 |
| `mcpmock_faults_armed` | Gauge | `instance`, `rule` | remaining armed count |
| `mcpmock_auth_decisions_total` | Counter | `instance`, `mode`, `decision` | `decision` from ADR-020's reason vocabulary — `MOCK-403`, `MOCK-404` |
| `mcpmock_tokens_issued_total` | Counter | `instance`, `alg`, `malformed` | `MOCK-405` |
| `mcpmock_requeststate_issued_total` | Counter | `instance` | `MOCK-243` |
| `mcpmock_requeststate_verify_total` | Counter | `instance`, `result` | the eight ADR-010 reasons |
| `mcpmock_requeststate_replay_lru_evictions_total` | Counter | `instance` | weakened replay detection — must be visible |
| `mcpmock_mrtr_rounds_total` | Counter | `instance`, `outcome` | `MOCK-241` |
| `mcpmock_catalogue_generation` | Gauge | `instance` | `MOCK-227`/`MOCK-233` |
| `mcpmock_catalogue_items` | Gauge | `instance`, `kind` | virtual count |
| `mcpmock_catalogue_scope_index_entries` | Gauge | `instance` | ADR-004 memo occupancy — `MOCK-228.5` |
| `mcpmock_snapshot_generation` | Gauge | `instance` | ADR-014 |
| `mcpmock_control_mutations_total` | Counter | `operation`, `outcome` | `MOCK-702` |
| `mcpmock_otel_export_failures_total` | Counter | — | never fatal (ADR-016) |
| `mcpmock_sessions_active` | Gauge | `instance` | `MOCK-301` |

### 2.3 Hot-path rule

All handles are pre-resolved at instance construction (`obs.InstanceMetrics`). `WithLabelValues`
is **never** called on the request path — it takes a lock and allocates, and at 20 000 rps that is
measurable. A microbenchmark gates metric recording at < 50 ns/request.

---

## 3. Tracing

OTel, OTLP/**HTTP** exporter only (ADR-013), no-op by default (ADR-016).

### 3.1 Spans

| Span | Parent | When |
|---|---|---|
| `mcpmock.request` | extracted `traceparent`, else root | every inbound JSON-RPC request |
| `mcpmock.authorize` | `mcpmock.request` | when auth mode ≠ `none` |
| `mcpmock.validate` | `mcpmock.request` | `_meta` + header validation |
| `mcpmock.dispatch` | `mcpmock.request` | handler execution |
| `mcpmock.catalogue.page` | `mcpmock.dispatch` | list methods |
| `mcpmock.argument_validation` | `mcpmock.dispatch` | `MOCK-606` |
| `mcpmock.fault` | `mcpmock.request` | one per fired rule |
| `mcpmock.stream` | `mcpmock.request` | SSE stream lifetime (long-lived; ends at close) |
| `mcpmock.mrtr.verify` | `mcpmock.request` | `requestState` verification |
| `mcpmock.journal.write` | `mcpmock.request` | only at `DEBUG`-equivalent sampling |
| `mcpmock.control.<operation>` | root | control API mutations |
| `mcpmock.startup` | root | with child spans per `startup_duration` phase |

### 3.2 Attributes

Stable, prefixed, and deliberately not high-cardinality except where a trace justifies it (a trace
is already sampled, so `mcp.name` is acceptable here even though it is forbidden as a metric
label).

| Attribute | Type | On |
|---|---|---|
| `mcpmock.instance` | string | all |
| `mcpmock.seed` | int | `mcpmock.startup` |
| `mcpmock.snapshot_generation` | int | `mcpmock.request` |
| `mcpmock.journal_seq` | int | `mcpmock.request` — **the join key between a trace and the journal** |
| `mcp.method` | string | request, dispatch |
| `mcp.name` | string | dispatch |
| `mcp.era` | string | request |
| `mcp.protocol_version` | string | request |
| `mcp.result_type` | string | dispatch |
| `mcp.transport` | string | request |
| `mcp.response_shape` | string | request |
| `mcpmock.fault.rule` / `.kind` | string | `mcpmock.fault` |
| `mcpmock.auth.mode` / `.decision` | string | authorize |
| `mcpmock.mrtr.chain` / `.round` / `.verify_reason` | string/int | mrtr.verify |
| `mcpmock.stream.kind` / `.frames_sent` / `.close_reason` | string/int | stream |
| `error.type` | string | any span that fails |

Standard `http.*` and `server.*` semantic-convention attributes are set on `mcpmock.request` for
HTTP transport. **`http.request.header.authorization` is never set** (`security.md §5`).

### 3.3 Context propagation is independent of export

The single most important point in this section. `traceparent` / `tracestate` are extracted from
every inbound request **unconditionally**, and `Record.Correlation` is populated regardless of
whether tracing is enabled or a collector exists. `assert.AssertTraceContextPropagated`
(`MOCK-603`) reads the journal, so it works in a plain `go test` with no infrastructure.

A **malformed** `traceparent` is recorded verbatim with `traceparentValid: false` and is a
distinct assertion failure class — a hub emitting a malformed header is precisely the defect this
is for, and treating it as "missing" would hide it.

Outbound: mcpmock does not call the hub, so there is no outbound injection except on OTLP export.

---

## 4. Log schema

`log/slog` JSON handler, **stderr**, one object per line. A `TestLogSchema` validates every
emitted line against this schema, so log consumers have a contract.

### 4.1 Fields

| Field | Type | Always? | Notes |
|---|---|---|---|
| `time` | RFC 3339 nanos | yes | real clock |
| `level` | `DEBUG`\|`INFO`\|`WARN`\|`ERROR` | yes | |
| `msg` | string | yes | **stable, lowercase, no interpolation** — variables go in fields, so `msg` is groupable |
| `event` | string | yes | machine-readable event id, e.g. `request.completed`, `control.mutation`, `fault.fired`, `stdout_leak`, `journal.dropped`, `hostile.armed` |
| `instance` | string | when applicable | pre-bound on the instance logger |
| `seq` | uint64 | request events | joins to the journal |
| `trace_id` / `span_id` | string | when present | joins to traces |
| `method`, `name`, `era`, `transport` | string | request events | |
| `status`, `duration_ms`, `outcome` | int/float/string | request events | |
| `fault_rule`, `fault_kind` | string | fault events | |
| `auth_decision` | string | auth events | |
| `generation` | uint64 | mutation events | resulting `Snapshot.Gen` |
| `actor` | string | control events | token **fingerprint**, never the token |
| `error` | string | ERROR/WARN | |
| `hint` | string | optional | requirement id or remedy |

### 4.2 Levels

| Level | Content | Default |
|---|---|---|
| `DEBUG` | per-request records | **off** — 20 000 rps × a JSON line is ~40 MB/s and would dominate the `MOCK-901` measurement |
| `INFO` | lifecycle, effective seed, listener addresses, control mutations, auth denials, fault arming, hostile arming | **on** |
| `WARN` | stdout leaks, journal drops, slow consumers, OTLP export failures, replay-LRU evictions, deterministic-key mode, `withheld` corpus placeholders | on |
| `ERROR` | conditions that make mcpmock itself wrong (self-check failure, panic recovery) | on |

High-frequency `WARN`s are rate-limited to one per event type per 10 s, with an aggregated count,
so a slow consumer cannot produce more log volume than protocol traffic.

### 4.3 Startup record

Emitted once, at `INFO`, and deliberately rich because it is what someone pastes into a bug report:

```json
{"time":"…","level":"INFO","msg":"mcpmock started","event":"startup",
 "version":"0.1.0","commit":"…","go":"go1.27.1",
 "seed":42,"seed_source":"flag",
 "instances":200,"transports":["http"],
 "mcp_addr":"0.0.0.0:8080","control_addr":"127.0.0.1:9091",
 "control_socket":"","metrics_addr":"127.0.0.1:9090",
 "safe_mode":true,"hostile_instances":[],
 "journal":{"enabled":true,"bodies":"full","max_records":100000,"overflow":"drop-oldest"},
 "startup_ms":143}
```

`MOCK-704` is satisfied by `seed` + `seed_source` here.

---

## 5. Health and readiness

| Endpoint | Semantics |
|---|---|
| `GET /healthz` | Liveness: the process is running and the timer wheel has ticked within 5 s. Never fails because of load or a fault rule — a fault-injecting test must not restart the pod. |
| `GET /readyz` | Readiness: every configured listener is accepting **and** every instance's `Snapshot` is published. Returns `503` during startup and during shutdown drain. |
| `GET /v1/health` | Rich JSON (control API): instance count, open streams, uptime, version, seed, safe mode, hostile instances. |

Deliberate choice: `/healthz` ignores injected faults entirely. Otherwise `MOCK-508`'s
"slow shutdown that never closes streams" would be indistinguishable from a crash-looping pod,
and Kubernetes would helpfully destroy the test.

---

## 6. SLIs and SLOs

These describe mcpmock as a **test dependency**. A flaky harness is worse than a slow one, so
availability and determinism outrank latency.

| SLI | Definition | SLO | Derived from |
|---|---|---|---|
| Determinism | fraction of repeated identical runs producing byte-identical responses | **100%** — any failure is a bug, not a budget | §0.1, PRIN-1 |
| Request success | `1 - requests_total{outcome=~"panic|timeout"} / requests_total` | ≥ 99.99% excluding deliberately injected faults | `MOCK-901` |
| Evidence completeness | `1 - journal_dropped_total / journal_records_total` | ≥ 99.9% at the `MOCK-902` load; **100%** below it | `MOCK-902` |
| Request latency | p99 `request_duration_seconds`, trivial handler, no faults | < 25 ms at 20 000 rps | `MOCK-901` |
| Journaled latency | p99 at 5000 rps with full journaling | < 50 ms | `MOCK-902` |
| Stream capacity | `sse_streams_open` sustainable without drops | ≥ 20 000 per process | `MOCK-903` |
| Frame delivery | `1 - sse_frames_dropped_total / sse_events_total` | ≥ 99.9% at 20 000 streams | `MOCK-903` |
| Startup | p95 `startup_duration_seconds{phase="total"}` | < 200 ms, reference scenario | `MOCK-107` |
| Control mutation latency | p99 `control_mutations_total` duration | < 50 ms | `MOCK-702` |
| Stdout cleanliness | `stdout_leak_bytes_total` | **exactly 0** | `MOCK-105` |

### 6.1 Alerting (for a long-lived shared deployment)

Only three are worth paging on, because everything else is a test result rather than an incident:

- `mcpmock_stdout_leak_bytes_total > 0` — the stdio channel is corrupted; every result downstream
  is suspect.
- `rate(mcpmock_journal_dropped_total[5m]) > 0` while `journal.overflow != "drop-oldest"` —
  evidence is being lost in a configuration that said it would not be.
- `mcpmock_hostile_mode == 1` on any instance in a namespace other than the authorised test
  namespace — the containment boundary of ADR-018 has been crossed.
