---
id: ADR-016
title: Prometheus registry per Server, OTLP/HTTP tracing lazily initialised, slog JSON on stderr
status: accepted
date: 2026-09-04
reversibility: EASY for metric names before first release; MEDIUM after (dashboards depend on them)
requirements: MOCK-105, MOCK-107, MOCK-603, MOCK-901
---

# ADR-016 — Observability

## Context

`MOCK-105` mandates `/metrics` (Prometheus) and structured JSON logs on stderr. The user
additionally mandates **OpenTelemetry OTLP tracing** — this is a stated requirement beyond
`requirements.md`, and is recorded as such (`[stated — user, not in requirements.md]`).

Three constraints shape it:

- `MOCK-107` startup budget: an OTLP exporter that dials a collector at construction costs
  connection setup time and fails noisily when no collector exists — which is the normal case in a
  unit test.
- `MOCK-901`: 20 000 rps. Metric recording must be allocation-free.
- `MOCK-603` `AssertTraceContextPropagated(traceID)`: we must **extract and record** the hub's
  `traceparent` even when we are not exporting anything.
- ADR-007: no process globals — two `Server`s in one test binary must not fight over a registry.

## Decision

### Metrics

- One `*prometheus.Registry` per `mcpmock.Server`, created in `New()`. `promauto` and
  `prometheus.DefaultRegisterer` are banned. `WithRegisterer(prometheus.Registerer)` lets an
  embedding test supply its own.
- `/metrics` is served on the **observability listener** (default `:9090`), separate from both the
  MCP listener and the control listener, so metrics can be scraped without exposing control.
  It may be co-hosted on the control listener with `observability.shareControlListener: true`.
- **Cardinality budget is a design constraint, not an afterthought.** Allowed labels:
  `instance` (≤ 200, `MOCK-904`), `transport` (2), `era` (4), `method` (~14), `outcome` (~8),
  `fault_rule` (bounded by scenario, validated ≤ 200), `result` (small enums). **Primitive name is
  never a label** — 5000 tools (`MOCK-221`) × 200 instances would be 1 M series.
  `scenario.Validate()` computes the worst-case series count and **fails validation above a
  configurable ceiling** (default 100 000). This turns a classic production incident into a config
  error.
- Hot path: `obs.InstanceMetrics` holds pre-resolved `prometheus.Counter` / `prometheus.Observer`
  handles bound at instance construction (ADR-007). No `WithLabelValues` per request.
- Native histograms enabled where the client library supports them; classic buckets declared
  explicitly for latency (`.001 .0025 .005 .01 .025 .05 .1 .25 .5 1 2.5 5 10`).

Full metric catalogue in `observability.md`.

### Tracing

- OTel `TracerProvider` held on the `Server`, **never** installed via `otel.SetTracerProvider`
  unless `WithGlobalOTel()` is passed by `cmd/mcpmock`.
- **Default is a no-op tracer.** Enabled by `observability.tracing.enabled: true` or
  `OTEL_EXPORTER_OTLP_ENDPOINT` being set.
- Exporter: **OTLP/HTTP only** (`otlptracehttp`). ADR-013 rejects the gRPC exporter on dependency
  grounds.
- Exporter construction is `sync.OnceValue`-lazy: nothing dials until the first span is exported,
  so a disabled or unreachable collector costs zero startup time and never blocks `Start()`.
  Export failures are logged at `WARN`, rate-limited, and counted
  (`mcpmock_otel_export_failures_total`) — never fatal. A test harness must not fail because a
  collector is missing.
- Sampling: `ParentBased(TraceIDRatioBased(observability.tracing.sampleRatio))`, default `0.0`
  when tracing is off, `1.0` when explicitly enabled (a test tool wants every trace, and volumes
  are small in tests). At `MOCK-901` load the ratio must be lowered; documented.

### Context propagation is separate from export

**This is the important nuance.** `traceparent` / `tracestate` extraction from the inbound request
happens in the pipeline **unconditionally**, using
`propagation.TraceContext{}.Extract`, and the extracted `trace_id` / `span_id` / `sampled` are
written into `Record.Correlation` regardless of whether tracing is exported. `MOCK-603`'s
`AssertTraceContextPropagated(traceID)` reads the journal, not a span exporter. Coupling the
assertion to an OTLP collector would make it unusable in a unit test.

Malformed `traceparent` is recorded verbatim in the journal with a `traceparent_invalid` flag —
the hub emitting a malformed header is exactly the kind of defect the journal exists to catch,
and a silently-dropped bad header would hide it.

### Logging

- `log/slog` with `slog.NewJSONHandler`, **stderr, always** (ADR-011).
- One `*slog.Logger` per `Server`; per-instance loggers derived with `With("instance", name)` at
  construction. `slog.SetDefault` is never called.
- Levels: `INFO` = lifecycle, config mutation, fault arming, auth denials, effective seed,
  listener addresses. `DEBUG` = per-request. `WARN` = stdout leaks, export failures, journal drops,
  slow consumers. `ERROR` = only conditions that make the mock itself wrong.
- **At `MOCK-901` load, per-request logging is off by design** (`DEBUG` disabled): 20 000 JSON log
  lines/s is ~40 MB/s and would dominate the measurement. The `INFO` default is what the perf
  profile uses, and this is stated in the acceptance criterion so the number is honest.
- Log schema (fields, types) in `observability.md §4`; a `TestLogSchema` asserts every emitted
  record validates against an embedded JSON Schema, so log consumers have a contract.
- **Redaction:** the logger has no access to credentials. Credential material never reaches a log
  field; only the keyed hash (`security.md §5`). Enforced by making `authz.Credential`'s
  `LogValue()` return the hash — `slog`'s `LogValuer` makes this structural rather than a rule.

## Options considered

1. **OTel metrics instead of Prometheus** — rejected: `MOCK-105` names Prometheus, and the OTel
   metrics SDK adds weight for no requirement.
2. **Both OTel metrics and Prometheus** — rejected: two metric pipelines, double cost, no
   requirement.
3. **Eager OTLP exporter at startup** — rejected on the 200 ms budget and on test ergonomics.
4. **`otlptracegrpc`** — rejected, ADR-013.
5. **`zap` / `zerolog`** — rejected: `log/slog` is stdlib, structured, and has `LogValuer`, which
   is what makes credential redaction structural.
6. **Log to stdout with a flag** — rejected, ADR-011.

## Consequences

**Positive.** Zero-cost when disabled; no globals so parallel embedded tests work; the
cardinality ceiling is enforced at config-validation time; trace-context assertions work with no
collector.

**Negative.** Prometheus and OTel are the two heaviest entries in the dependency graph
(ADR-013) and land in the hub's `go.mod` for anyone importing `mcpmock` (not `assert`). The
cardinality ceiling can reject a legitimate large-fleet scenario; it is configurable, and the
error message says exactly which label is responsible.

**Forecloses.** Per-tool metrics. If someone needs per-tool counts, that is what the journal is
for — and the journal is the better answer anyway.
