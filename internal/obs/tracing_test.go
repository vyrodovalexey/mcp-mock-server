package obs_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/vyrodovalexey/mcp-mock-server/internal/obs"
)

// TestUsableWithoutCollector is a core acceptance criterion: a Bundle with
// tracing enabled but no OTLP collector reachable must construct, produce spans,
// and shut down without error (ADR-016 — a test harness must not fail because a
// collector is missing). The endpoint points at a black hole; nothing is dialed
// until first export, and export failures are non-fatal.
func TestUsableWithoutCollector(t *testing.T) {
	t.Parallel()
	b, err := obs.New(obs.Config{
		Tracing: obs.TracingConfig{
			Enabled:       true,
			Endpoint:      "http://127.0.0.1:1", // unreachable on purpose
			Insecure:      true,
			ExportTimeout: 200 * time.Millisecond,
		},
	})
	if err != nil {
		t.Fatalf("New must not fail when a collector is absent: %v", err)
	}

	// Creating and ending a span must not block or panic even though export
	// will fail against the black-hole endpoint.
	_, span := b.Tracer().Tracer().Start(context.Background(), obs.SpanRequest)
	span.End()

	// Shutdown flushes; it may surface an export error but must return promptly.
	shCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = b.Shutdown(shCtx) // non-fatal by contract; absence of a hang is the test
}

// TestDisabledTracerIsNoop asserts the default (disabled) tracer is a genuine
// no-op provider and dials nothing — the MOCK-107 zero-cost-when-off property.
func TestDisabledTracerIsNoop(t *testing.T) {
	t.Parallel()
	b, err := obs.New(obs.Config{})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if _, ok := b.Tracer().Provider().(noop.TracerProvider); !ok {
		t.Fatalf("disabled tracer provider is %T, want noop.TracerProvider", b.Tracer().Provider())
	}
	if err := b.Shutdown(context.Background()); err != nil {
		t.Fatalf("disabled Shutdown returned %v, want nil", err)
	}
}

// TestPropagationIndependentOfExport asserts inbound traceparent extraction
// works with tracing disabled and no collector — the ADR-016 nuance that
// context propagation is separate from export (MOCK-603).
func TestPropagationIndependentOfExport(t *testing.T) {
	t.Parallel()
	b, err := obs.New(obs.Config{}) // tracing disabled
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	h := http.Header{}
	const traceID = "0af7651916cd43dd8448eb211c80319c"
	h.Set("traceparent", "00-"+traceID+"-b7ad6b7169203331-01")

	ctx := b.Tracer().Extract(context.Background(), propagation.HeaderCarrier(h))
	sc := trace.SpanContextFromContext(ctx)
	if !sc.HasTraceID() {
		t.Fatal("traceparent was not extracted with tracing disabled")
	}
	if got := sc.TraceID().String(); got != traceID {
		t.Fatalf("extracted trace id = %q, want %q", got, traceID)
	}
}
