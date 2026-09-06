package obs

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/prometheus/client_golang/prometheus"
)

// Config configures a [Bundle]. The zero value is usable: it yields an
// info-level logger to the supplied writer, a fresh private registry, and a
// no-op tracer with no collector.
type Config struct {
	// LogWriter is where structured JSON logs are written. It MUST NOT be
	// os.Stdout — stdout is the stdio transport's frame channel (ADR-011).
	// cmd/mcpmock passes os.Stderr. A nil writer discards logs.
	LogWriter io.Writer
	// LogLevel is the minimum log level. Defaults to INFO.
	LogLevel slog.Leveler
	// Registry lets an embedding test supply its own Prometheus registry
	// (ADR-016 WithRegisterer). When nil, the Bundle creates a fresh private
	// registry — never the process-global default registerer (ADR-007).
	Registry *prometheus.Registry
	// Tracing configures the tracer. The zero value is a no-op tracer.
	Tracing TracingConfig
}

// Bundle is the per-Server observability facility: one logger, one Prometheus
// registry with pre-resolved metric handles, and one tracer provider — none of
// them process-global (ADR-007). It is the object ADR-007 §2 lists as one of the
// three things ≥200 instances share; it is read-mostly and passed explicitly,
// never reached for.
type Bundle struct {
	logger  *slog.Logger
	metrics *Metrics
	tracer  *Tracer
}

// New constructs a Bundle from cfg. It performs no network I/O and touches no
// process global: no slog.SetDefault, no prometheus.DefaultRegisterer, no
// otel.SetTracerProvider. It returns an error only if metric registration fails
// against the (fresh or supplied) registry — never because a collector is
// absent.
func New(cfg Config) (*Bundle, error) {
	level := cfg.LogLevel
	if level == nil {
		level = slog.LevelInfo
	}
	logger := NewLogger(cfg.LogWriter, level)

	reg := cfg.Registry
	if reg == nil {
		reg = prometheus.NewRegistry()
	}
	metrics, err := newMetrics(reg)
	if err != nil {
		return nil, fmt.Errorf("obs: register metrics: %w", err)
	}

	return &Bundle{
		logger:  logger,
		metrics: metrics,
		tracer:  newTracer(cfg.Tracing),
	}, nil
}

// Logger returns the Bundle's base logger. Per-instance loggers are derived with
// [InstanceLogger].
func (b *Bundle) Logger() *slog.Logger { return b.logger }

// InstanceLogger derives a logger with the instance name pre-bound.
func (b *Bundle) InstanceLogger(instance string) *slog.Logger {
	return InstanceLogger(b.logger, instance)
}

// Metrics returns the Bundle's metric set, bound to its private registry.
func (b *Bundle) Metrics() *Metrics { return b.metrics }

// Tracer returns the Bundle's tracer.
func (b *Bundle) Tracer() *Tracer { return b.tracer }

// NewInstanceMetrics pre-resolves every hot-path metric handle for one instance
// (observability.md §2.3). Call it once per instance at construction; the
// request path then records through the returned handles without ever calling
// WithLabelValues.
func (b *Bundle) NewInstanceMetrics(instance string) *InstanceMetrics {
	return newInstanceMetrics(b.metrics, instance)
}

// Shutdown flushes and stops the tracer provider, bounded by ctx. It is safe to
// call when tracing is disabled.
func (b *Bundle) Shutdown(ctx context.Context) error {
	return b.tracer.Shutdown(ctx)
}
