package obs

import (
	"context"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// Span names, transcribed verbatim from observability.md §3.1. This package
// registers these names; it does not invent them.
const (
	// SpanRequest is the root span for every inbound JSON-RPC request.
	SpanRequest = "mcpmock.request"
	// SpanAuthorize covers authorization when the auth mode is not "none".
	SpanAuthorize = "mcpmock.authorize"
	// SpanValidate covers _meta and header validation.
	SpanValidate = "mcpmock.validate"
	// SpanDispatch covers handler execution.
	SpanDispatch = "mcpmock.dispatch"
	// SpanStartup covers process startup, with child spans per phase.
	SpanStartup = "mcpmock.startup"
)

// Tracing attribute keys, transcribed verbatim from observability.md §3.2. Only
// the Phase-1-reachable subset is declared here; later phases append.
const (
	// AttrInstance is set on all spans.
	AttrInstance = "mcpmock.instance"
	// AttrMethod is the MCP method, set on request and dispatch spans.
	AttrMethod = "mcp.method"
	// AttrTransport is the transport, set on the request span.
	AttrTransport = "mcp.transport"
	// AttrEra is the protocol era, set on the request span.
	AttrEra = "mcp.era"
	// AttrProtocolVersion is the negotiated protocol version, on the request span.
	AttrProtocolVersion = "mcp.protocol_version"
	// AttrSnapshotGeneration joins a request span to its Snapshot (ADR-014).
	AttrSnapshotGeneration = "mcpmock.snapshot_generation"
	// AttrJournalSeq is the join key between a trace and the journal.
	AttrJournalSeq = "mcpmock.journal_seq"
	// AttrErrorType is set on any span that fails.
	AttrErrorType = "error.type"
)

// tracerName is the instrumentation scope name reported on every tracer.
const tracerName = "github.com/vyrodovalexey/mcp-mock-server/internal/obs"

// TracingConfig configures the per-Bundle tracer. The zero value is valid and
// yields a no-op tracer with no exporter — the default (ADR-016): a disabled or
// unreachable collector must cost zero startup time and never fail Start.
type TracingConfig struct {
	// Enabled turns tracing on. When false, the Bundle uses a no-op tracer
	// provider and dials no collector (ADR-016). Context propagation is
	// unaffected — traceparent is still extracted regardless (see [Propagator]).
	Enabled bool
	// Endpoint is the OTLP/HTTP collector endpoint (host:port or URL). When
	// empty and Enabled is true, the OTel SDK falls back to its own
	// OTEL_EXPORTER_OTLP_ENDPOINT default. It is never dialed until the first
	// span export (lazy), so an absent collector never blocks startup.
	Endpoint string
	// Insecure selects plain HTTP for the OTLP endpoint.
	Insecure bool
	// SampleRatio is the ParentBased(TraceIDRatioBased) sample ratio; default
	// 1.0 when Enabled (a test tool wants every trace), 0.0 otherwise.
	SampleRatio float64
	// ServiceName tags the resource; defaults to "mcpmock".
	ServiceName string
	// ExportTimeout bounds a single export attempt. Defaults to 10s.
	ExportTimeout time.Duration
}

// Tracer is the per-[Bundle] tracing facility. It holds its provider on the
// struct and NEVER installs it via otel.SetTracerProvider (ADR-007) — installing
// globally is a cmd/-only decision, out of scope for this package. When tracing
// is disabled the provider is a no-op and no exporter is constructed.
type Tracer struct {
	provider   trace.TracerProvider
	tracer     trace.Tracer
	propagator propagation.TextMapPropagator

	// shutdown flushes and stops the SDK provider, if any. It is a no-op when
	// tracing is disabled.
	shutdown func(context.Context) error
}

// newTracer builds a Tracer from cfg. It performs no network I/O: exporter
// construction is deferred so that no collector is dialed until the first span
// export. When cfg.Enabled is false it returns a no-op tracer whose Shutdown is
// a no-op. It never returns an error for an absent collector — that is the point
// (TestUsableWithoutCollector).
func newTracer(cfg TracingConfig) *Tracer {
	prop := propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	)
	if !cfg.Enabled {
		np := noop.NewTracerProvider()
		return &Tracer{
			provider:   np,
			tracer:     np.Tracer(tracerName),
			propagator: prop,
			shutdown:   func(context.Context) error { return nil },
		}
	}

	tp := newSDKProvider(cfg)
	return &Tracer{
		provider:   tp,
		tracer:     tp.Tracer(tracerName),
		propagator: prop,
		shutdown:   tp.Shutdown,
	}
}

// newSDKProvider builds an SDK tracer provider whose OTLP/HTTP exporter is
// constructed lazily on first export. The lazyExporter wrapper defers the
// otlptracehttp client build (and therefore any connection) until the batch
// processor first flushes, keeping the MOCK-107 startup budget clean and
// ensuring an unreachable collector never blocks Start.
func newSDKProvider(cfg TracingConfig) *sdktrace.TracerProvider {
	ratio := cfg.SampleRatio
	if ratio == 0 {
		ratio = 1.0
	}
	service := cfg.ServiceName
	if service == "" {
		service = "mcpmock"
	}
	timeout := cfg.ExportTimeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}

	exp := &lazyExporter{cfg: cfg, timeout: timeout}
	res := resource.NewSchemaless(
		attribute.String("service.name", service),
	)
	return sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(ratio))),
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
	)
}

// Tracer returns the OTel tracer for creating spans. When tracing is disabled it
// is a no-op tracer, so callers create spans unconditionally without a nil check
// and pay nothing when off.
func (t *Tracer) Tracer() trace.Tracer { return t.tracer }

// Provider returns the tracer provider held by this Bundle. It is exposed so a
// caller that must pass a provider (never the global) can do so explicitly.
func (t *Tracer) Provider() trace.TracerProvider { return t.provider }

// Propagator returns the text-map propagator used to extract inbound
// traceparent/tracestate. Extraction happens unconditionally on every request,
// independent of whether tracing is exported (ADR-016 §"Context propagation is
// separate from export", MOCK-603).
func (t *Tracer) Propagator() propagation.TextMapPropagator { return t.propagator }

// Extract reads trace context from an inbound carrier (e.g. HTTP headers) into a
// derived context. It works with tracing disabled and with no collector present,
// because AssertTraceContextPropagated (MOCK-603) reads the journal, not a span
// exporter.
func (t *Tracer) Extract(ctx context.Context, carrier propagation.TextMapCarrier) context.Context {
	return t.propagator.Extract(ctx, carrier)
}

// Shutdown flushes and stops the tracer provider. It is safe to call when
// tracing is disabled (it is then a no-op) and is bounded by ctx.
func (t *Tracer) Shutdown(ctx context.Context) error { return t.shutdown(ctx) }

// lazyExporter defers construction of the OTLP/HTTP exporter until the first
// ExportSpans call, so nothing is dialed at startup (ADR-016). A failure to
// build or export never panics and never propagates fatally to the caller — the
// batch span processor logs and drops, and the Bundle counts the failure via
// mcpmock_otel_export_failures_total.
type lazyExporter struct {
	cfg     TracingConfig
	timeout time.Duration

	once  sync.Once
	inner sdktrace.SpanExporter
	err   error
}

// resolve constructs the real exporter exactly once. otlptracehttp.New with a
// context that is already done (or a bounded one) does not block on a missing
// collector at construction — the client connects lazily per export — so this is
// safe to call from the export path.
func (e *lazyExporter) resolve(ctx context.Context) (sdktrace.SpanExporter, error) {
	e.once.Do(func() {
		opts := []otlptracehttp.Option{}
		if e.cfg.Endpoint != "" {
			opts = append(opts, otlptracehttp.WithEndpointURL(e.cfg.Endpoint))
		}
		if e.cfg.Insecure {
			opts = append(opts, otlptracehttp.WithInsecure())
		}
		opts = append(opts, otlptracehttp.WithTimeout(e.timeout))
		e.inner, e.err = otlptracehttp.New(ctx, opts...)
	})
	return e.inner, e.err
}

// ExportSpans builds the real exporter on first use and delegates. Any error is
// returned to the batch processor, which handles it non-fatally; it is never a
// panic and never blocks Start.
func (e *lazyExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	exp, err := e.resolve(ctx)
	if err != nil {
		return err
	}
	return exp.ExportSpans(ctx, spans)
}

// Shutdown resolves nothing if the exporter was never built, and otherwise
// delegates. It is bounded by ctx.
func (e *lazyExporter) Shutdown(ctx context.Context) error {
	if e.inner == nil {
		return nil
	}
	return e.inner.Shutdown(ctx)
}
