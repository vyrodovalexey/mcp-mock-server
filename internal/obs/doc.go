// Package obs is mcpmock's observability bundle: a structured stderr logger, a
// per-Server Prometheus registry with pre-resolved metric handles, a lazily
// initialized OpenTelemetry tracer provider, and the observability HTTP
// listener serving /metrics, /healthz and /readyz.
//
// It implements ADR-016 (observability), and is bound by ADR-007 (no process
// globals) and ADR-011 (stdout belongs to the stdio protocol channel). The
// authoritative metric names, types and labels, span names and attributes, and
// the log schema are defined in observability.md; this package registers those
// names verbatim and never invents its own.
//
// # Stderr only (ADR-011)
//
// In stdio transport mode, os.Stdout carries the MCP JSON-RPC frame stream. A
// single stray byte written to stdout corrupts that stream and the hub under
// test sees a parse error. This package therefore NEVER constructs a logger,
// a metrics handler or a trace exporter that writes to stdout. [NewLogger]
// takes an explicit io.Writer and cmd/mcpmock always passes os.Stderr; there is
// no code path that defaults to stdout. TestNoStdoutWrites proves the package
// writes nothing to fd 1 during normal operation.
//
// # No process globals (ADR-007)
//
// Prometheus's default registry and OpenTelemetry's global tracer provider are
// process-global singletons by design. With them, two [Bundle]s in one test
// binary — which MOCK-107 requires — would collide on duplicate metric
// registration or fight over the global provider. This package therefore:
//
//   - creates a fresh *prometheus.Registry per [Bundle]; it never touches
//     prometheus.DefaultRegisterer, prometheus.MustRegister (the default),
//     promauto, or expvar;
//   - holds an OTel *sdktrace.TracerProvider on the [Bundle]; it never calls
//     otel.SetTracerProvider or any other global setter (installing globally is
//     a cmd/-only choice, out of scope here);
//   - never calls slog.SetDefault; each [Bundle] owns its own *slog.Logger and
//     derives per-instance loggers with a bound instance attribute.
//
// There is no mutable package-level variable in this package. TestNoGlobals /
// the make globals-check AST scan enforce this. TestTwoBundlesNoCollision
// proves two bundles register metrics without a duplicate-registration panic.
//
// # Zero cost when off (ADR-016, MOCK-107, MOCK-901)
//
// The tracer provider is a no-op by default and dials no collector until the
// first span is exported, so a disabled or unreachable collector costs zero
// startup time and never fails Start — a test harness must run with no
// collector present (TestUsableWithoutCollector). Metric label handles are
// pre-resolved at instance construction ([Bundle.NewInstanceMetrics]); the
// request hot path records through cached handles and never calls
// WithLabelValues, which takes a lock and allocates (observability.md §2.3).
//
// # Never a credential (security.md §5)
//
// No credential-derived value is ever a metric label, a span attribute or a
// log field except a keyed hash. The redaction is structural: pass a
// [CredentialHash] (which implements slog.LogValuer to yield only its
// fingerprint) rather than a raw secret, so even a careless log call cannot
// leak. TestNoCredentialInLogs scans emitted output for a fixture token value
// and asserts zero matches.
package obs
