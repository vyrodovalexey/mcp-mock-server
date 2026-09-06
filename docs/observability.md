# Observability reference

Audience: **operator**.

mcpmock serves Prometheus metrics, liveness/readiness endpoints and
structured logs, and can export OTLP traces. All observability endpoints
live on **one listener, separate from the MCP port** — default `:9090`.

## Table of contents

- [Endpoints](#endpoints)
- [Metrics](#metrics)
- [Logs](#logs)
- [Tracing](#tracing)
- [Kubernetes probe wiring](#kubernetes-probe-wiring)

## Endpoints

| Path | Purpose | Verified response |
|---|---|---|
| `/metrics` | Prometheus exposition format | `text/plain`, verified: 897 `mcpmock_*` series on a fresh instance with one request served. |
| `/healthz` | Liveness | `200`, body `ok`. This is the liveness path — not `/livez`; an earlier draft of the deployment spec assumed `/livez`, but the shipped binary and Helm chart both use `/healthz`. |
| `/readyz` | Readiness | `200`, body `ok`. Returns non-200 during startup and shutdown drain. |

Default bind address: `:9090`. Override with `mcpmock serve
--metrics-listen <addr>` (flag) or the `MCPMOCK_METRICS_LISTEN` environment
variable — flag wins over env, env wins over the `:9090` default.

## Metrics

Metric names verified live against a running instance
(`curl http://127.0.0.1:9090/metrics`). This is not an exhaustive list of
every label combination — it is every distinct metric family observed:

| Metric | Type | Meaning |
|---|---|---|
| `mcpmock_requests_total` | counter | Total JSON-RPC requests. |
| `mcpmock_requests_in_flight` | gauge | Requests currently being handled. |
| `mcpmock_request_duration_seconds` | histogram | Request duration. |
| `mcpmock_request_body_bytes` | histogram | Inbound request body size. |
| `mcpmock_response_body_bytes` | histogram | Outbound response body size. |
| `mcpmock_meta_validation_total` | counter | `_meta` validation outcomes by mode and result (`MOCK-203`). |
| `mcpmock_validation_errors_total` | counter | Schema/semantic validation errors. |
| `mcpmock_journal_records` | gauge | Current journal ring occupancy. |
| `mcpmock_journal_records_total` | counter | Cumulative journal records written. |
| `mcpmock_journal_dropped_total` | counter | Records lost to ring overflow. |
| `mcpmock_journal_bytes` | gauge | Retained journal bytes against the configured budget. |
| `mcpmock_journal_write_duration_seconds` | histogram | Journal write latency. |
| `mcpmock_instances` | gauge | Number of logical instances. |
| `mcpmock_snapshot_generation` | gauge | Current configuration snapshot generation. |
| `mcpmock_goroutines` | gauge | Current goroutine count. |
| `mcpmock_effective_seed` | gauge | Effective seed as a `float64` — **lossy above 2^53**; use `GET /v1/seed` on the control API (or `mcpmock ctl seed`) for the authoritative `uint64` value. |
| `mcpmock_otel_export_failures_total` | counter | OTLP export failures. |
| `mcpmock_stdout_leak_bytes_total` | counter | Bytes accidentally leaked to stdout in stdio mode (ADR-011); **must always read 0** — any non-zero value means a bug corrupted the protocol stream. |

Each metric is self-documented via its Prometheus `HELP` line; read it
directly from a running instance for the authoritative wording.

## Logs

- Structured JSON, written to **stderr only** — in stdio mode, stdout is
  reserved for protocol frames (ADR-011), so logs never appear there.
- `--log-level` accepts `debug`, `info`, `warn`, `error` (default `info`).
- Startup always emits, at `info`, an `"effective seed"` line before any
  other output, followed by `"started"` and `"serving"` lines naming every
  bound URL (MCP, metrics, control).

## Tracing

OTLP/HTTP tracing is wired but off by default. It is configured only via a
scenario document's `process.observability.tracing` block (not yet exposed
as a `serve` CLI flag) — see
[`docs/scenario-reference.md`](scenario-reference.md) for other scenario
keys and the schema file for the exact tracing keys
(`internal/config/schema/scenario.schema.json`,
`$defs.process.observability.tracing`). Trace context is recorded on every
journal record's `correlation` field regardless of whether export is
enabled, so trace-based assertions work even in a plain unit test with no
collector running.

## Kubernetes probe wiring

The Helm chart's `livenessProbe` and `readinessProbe` (see
[`docs/deployment.md`](deployment.md)) point at `:9090/healthz` and
`:9090/readyz` respectively — the published contract, verified against the
running binary, not `/livez`.
