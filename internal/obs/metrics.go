package obs

import (
	"github.com/prometheus/client_golang/prometheus"
)

// Metric and label names are transcribed verbatim from observability.md §2.
// They are the contract a scrape depends on; this package never invents a name.
const (
	metricBuildInfo         = "mcpmock_build_info"
	metricEffectiveSeed     = "mcpmock_effective_seed"
	metricStartupDuration   = "mcpmock_startup_duration_seconds"
	metricInstances         = "mcpmock_instances"
	metricGoroutines        = "mcpmock_goroutines"
	metricStdoutLeakBytes   = "mcpmock_stdout_leak_bytes_total"
	metricRequestsTotal     = "mcpmock_requests_total"
	metricRequestDuration   = "mcpmock_request_duration_seconds"
	metricRequestBodyBytes  = "mcpmock_request_body_bytes"
	metricResponseBodyBytes = "mcpmock_response_body_bytes"
	metricRequestsInFlight  = "mcpmock_requests_in_flight"
	metricValidationErrors  = "mcpmock_validation_errors_total"
	metricJournalRecords    = "mcpmock_journal_records"
	metricJournalRecTotal   = "mcpmock_journal_records_total"
	metricJournalDropped    = "mcpmock_journal_dropped_total"
	metricJournalBytes      = "mcpmock_journal_bytes"
	metricJournalWriteDur   = "mcpmock_journal_write_duration_seconds"
	metricOTELExportFailure = "mcpmock_otel_export_failures_total"
	metricSnapshotGen       = "mcpmock_snapshot_generation"
	metricMetaValidation    = "mcpmock_meta_validation_total"
)

// Label names, transcribed from observability.md §2.1. The cardinality budget
// there is authoritative; see the package-level cardinality analysis in the
// task report. Notably the primitive "name" is NEVER a label — 5000 tools × 200
// instances would be 1M series; per-name evidence lives in the journal.
const (
	labelInstance  = "instance"
	labelTransport = "transport"
	labelEra       = "era"
	labelMethod    = "method"
	labelOutcome   = "outcome"
	labelKind      = "kind"
	labelPolicy    = "policy"
	labelMode      = "mode"
	labelVersion   = "version"
	labelCommit    = "commit"
	labelGoversion = "goversion"
	labelPhase     = "phase"
)

// latencyBuckets returns the classic histogram buckets for request duration,
// declared explicitly by observability.md §2.2 / ADR-016. It returns a fresh
// slice each call so there is no shared mutable package-level state (ADR-007).
func latencyBuckets() []float64 {
	return []float64{.001, .0025, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10}
}

// The bounded enum domains below (observability.md §2.1) fix the cardinality of
// each label and are the sets over which per-instance handles are pre-resolved,
// so the request hot path never calls WithLabelValues. They are functions
// returning fresh slices rather than package-level vars, so no caller can mutate
// a shared domain and the package holds zero mutable global state (ADR-007).

// Transports enumerates the transport label domain (size 2).
func Transports() []string { return []string{"http", "stdio"} }

// Eras enumerates the era label domain (size 4).
func Eras() []string { return []string{"modern", "legacy", "dual", "probe"} }

// Methods enumerates the method label domain; an unknown method maps to
// "other", keeping the domain fixed at ~14 (observability.md §2.1).
func Methods() []string {
	return []string{"server/discover", "tools/list", "tools/call", "other"}
}

// Outcomes enumerates the outcome label domain (size 8). "canceled" uses US
// spelling to match the misspell locale and the requirement vocabulary.
func Outcomes() []string {
	return []string{
		"ok", "error", "canceled", "fault",
		"auth_denied", "validation_failed", "timeout", "panic",
	}
}

// ValidationKinds enumerates the validation-error kind label
// (observability.md §2.2, MOCK-203…206).
func ValidationKinds() []string {
	return []string{"meta_missing", "header_mismatch", "unsupported_version", "missing_capability"}
}

// JournalDropPolicies enumerates the journal drop policy label
// (observability.md §2.2).
func JournalDropPolicies() []string { return []string{"drop_oldest", "block_timeout"} }

// MetaValidationModes enumerates the _meta validation mode label
// (requirements-spec.md MOCK-203, AMEND-6). Bounded at 3.
func MetaValidationModes() []string { return []string{"strict", "lenient", "off"} }

// MetaValidationOutcomes enumerates the _meta validation outcome label
// (requirements-spec.md MOCK-203, AMEND-6). Bounded at 3: strict rejects,
// lenient tolerates-and-records, off skips.
func MetaValidationOutcomes() []string { return []string{"rejected", "tolerated", "skipped"} }

// Metrics is the per-[Bundle] Prometheus metric set. It owns a private
// *prometheus.Registry — never the process-global default registerer (ADR-007) —
// and holds every collector registered against it. Vectors are registered once
// here; fully-bound per-instance handles are pre-resolved by
// [Bundle.NewInstanceMetrics] so WithLabelValues never appears on the hot path.
type Metrics struct {
	registry *prometheus.Registry

	buildInfo     *prometheus.GaugeVec
	effectiveSeed prometheus.Gauge
	startupDur    *prometheus.GaugeVec
	instances     prometheus.Gauge
	goroutines    prometheus.Gauge
	stdoutLeak    prometheus.Counter
	otelFailures  prometheus.Counter

	requestsTotal    *prometheus.CounterVec
	requestDuration  *prometheus.HistogramVec
	requestBodyBytes *prometheus.HistogramVec
	respBodyBytes    *prometheus.HistogramVec
	requestsInFlight *prometheus.GaugeVec
	validationErrors *prometheus.CounterVec
	metaValidation   *prometheus.CounterVec

	journalRecords  *prometheus.GaugeVec
	journalRecTotal *prometheus.CounterVec
	journalDropped  *prometheus.CounterVec
	journalBytes    *prometheus.GaugeVec
	journalWriteDur *prometheus.HistogramVec
	snapshotGen     *prometheus.GaugeVec
}

// newMetrics constructs a Metrics bound to its own registry and registers every
// Phase-1-reachable collector from observability.md §2. Registration uses the
// explicit registry, so two Bundles in one process never collide on the default
// registerer (the duplicate-registration panic MOCK-107.4 guards against).
func newMetrics(reg *prometheus.Registry) (*Metrics, error) {
	m := &Metrics{registry: reg}

	m.buildInfo = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: metricBuildInfo, Help: "Build identification (always 1).",
	}, []string{labelVersion, labelCommit, labelGoversion})
	m.effectiveSeed = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: metricEffectiveSeed,
		Help: "Effective seed as float64 (lossy above 2^53; authoritative value is GET /v1/seed).",
	})
	m.startupDur = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: metricStartupDuration, Help: "Startup duration per phase in seconds.",
	}, []string{labelPhase})
	m.instances = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: metricInstances, Help: "Number of logical instances.",
	})
	m.goroutines = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: metricGoroutines, Help: "Current goroutine count.",
	})
	m.stdoutLeak = prometheus.NewCounter(prometheus.CounterOpts{
		Name: metricStdoutLeakBytes, Help: "Bytes leaked to the hijacked stdout (ADR-011); must be 0.",
	})
	m.otelFailures = prometheus.NewCounter(prometheus.CounterOpts{
		Name: metricOTELExportFailure, Help: "OTLP export failures; never fatal (ADR-016).",
	})

	m.requestsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: metricRequestsTotal, Help: "Total JSON-RPC requests.",
	}, []string{labelInstance, labelTransport, labelEra, labelMethod, labelOutcome})
	m.requestDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: metricRequestDuration, Help: "Request duration in seconds.", Buckets: latencyBuckets(),
	}, []string{labelInstance, labelMethod, labelOutcome})
	m.requestBodyBytes = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    metricRequestBodyBytes,
		Help:    "Request body size in bytes.",
		Buckets: prometheus.ExponentialBuckets(64, 4, 8),
	}, []string{labelInstance, labelMethod})
	m.respBodyBytes = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    metricResponseBodyBytes,
		Help:    "Response body size in bytes.",
		Buckets: prometheus.ExponentialBuckets(64, 4, 8),
	}, []string{labelInstance, labelMethod})
	m.requestsInFlight = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: metricRequestsInFlight, Help: "In-flight requests.",
	}, []string{labelInstance})
	m.validationErrors = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: metricValidationErrors, Help: "Validation errors by kind.",
	}, []string{labelInstance, labelKind})
	m.metaValidation = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: metricMetaValidation,
		Help: "_meta validation results by mode and outcome (MOCK-203, AMEND-6).",
	}, []string{labelInstance, labelMode, labelOutcome})

	m.journalRecords = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: metricJournalRecords, Help: "Current journal ring occupancy.",
	}, []string{labelInstance})
	m.journalRecTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: metricJournalRecTotal, Help: "Total journal records written.",
	}, []string{labelInstance})
	m.journalDropped = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: metricJournalDropped, Help: "Dropped journal records by policy (the honesty metric).",
	}, []string{labelInstance, labelPolicy})
	m.journalBytes = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: metricJournalBytes, Help: "Current journal size in bytes.",
	}, []string{labelInstance})
	m.journalWriteDur = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: metricJournalWriteDur, Help: "Journal write duration in seconds.", Buckets: latencyBuckets(),
	}, []string{labelInstance})
	m.snapshotGen = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: metricSnapshotGen, Help: "Current Snapshot generation (ADR-014).",
	}, []string{labelInstance})

	for _, c := range m.collectors() {
		if err := reg.Register(c); err != nil {
			return nil, err
		}
	}
	return m, nil
}

// collectors returns every collector owned by this Metrics, in a stable order,
// so registration and any future unregistration share one source of truth.
func (m *Metrics) collectors() []prometheus.Collector {
	return []prometheus.Collector{
		m.buildInfo, m.effectiveSeed, m.startupDur, m.instances, m.goroutines,
		m.stdoutLeak, m.otelFailures,
		m.requestsTotal, m.requestDuration, m.requestBodyBytes, m.respBodyBytes,
		m.requestsInFlight, m.validationErrors, m.metaValidation,
		m.journalRecords, m.journalRecTotal, m.journalDropped, m.journalBytes,
		m.journalWriteDur, m.snapshotGen,
	}
}

// Registry returns the private Prometheus registry this metric set is bound to,
// so the observability listener can serve exactly it (never the default one).
func (m *Metrics) Registry() *prometheus.Registry { return m.registry }

// SetBuildInfo publishes the build identification gauge (=1). Called once at
// startup; its labels are constant for the process lifetime.
func (m *Metrics) SetBuildInfo(version, commit, goversion string) {
	m.buildInfo.WithLabelValues(version, commit, goversion).Set(1)
}

// SetEffectiveSeed publishes the effective seed gauge (MOCK-704). The value is
// lossy above 2^53; the authoritative value is the startup log and GET /v1/seed.
func (m *Metrics) SetEffectiveSeed(seed uint64) {
	m.effectiveSeed.Set(float64(seed))
}

// SetStartupPhase records the duration of a named startup phase (MOCK-107).
func (m *Metrics) SetStartupPhase(phase string, seconds float64) {
	m.startupDur.WithLabelValues(phase).Set(seconds)
}

// SetInstances publishes the instance-count gauge (MOCK-904).
func (m *Metrics) SetInstances(n int) { m.instances.Set(float64(n)) }

// SetGoroutines publishes the goroutine-count gauge.
func (m *Metrics) SetGoroutines(n int) { m.goroutines.Set(float64(n)) }

// AddStdoutLeakBytes records bytes that leaked to the hijacked stdout (ADR-011).
// It is exported so cmd/mcpmock's stdout drainer (TASK-021) can feed it; the
// value must remain 0 in a correct run.
func (m *Metrics) AddStdoutLeakBytes(n float64) { m.stdoutLeak.Add(n) }

// IncOTELExportFailure records a single OTLP export failure. Export failures are
// never fatal (ADR-016); this counter makes them visible.
func (m *Metrics) IncOTELExportFailure() { m.otelFailures.Inc() }

// StdoutLeakCounter exposes the raw counter so a drain that must Add on a hot
// path can cache the handle rather than re-resolving it.
func (m *Metrics) StdoutLeakCounter() prometheus.Counter { return m.stdoutLeak }
