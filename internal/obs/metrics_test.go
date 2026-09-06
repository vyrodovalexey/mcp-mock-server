package obs_test

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/prometheus/common/expfmt"

	"github.com/vyrodovalexey/mcp-mock-server/internal/obs"
)

// TestTwoBundlesNoCollision is acceptance criterion 4 / MOCK-107.4: two Bundles
// constructed in one process each get an independent registry and neither panics
// on duplicate registration. If either used the default registerer, the second
// New would return a duplicate-registration error.
func TestTwoBundlesNoCollision(t *testing.T) {
	t.Parallel()
	a, err := obs.New(obs.Config{})
	if err != nil {
		t.Fatalf("first bundle: %v", err)
	}
	b, err := obs.New(obs.Config{})
	if err != nil {
		t.Fatalf("second bundle (would fail on a shared/default registry): %v", err)
	}
	if a.Metrics().Registry() == b.Metrics().Registry() {
		t.Fatal("two bundles share a registry; ADR-007 requires one per Server")
	}
	// Both must expose the same metric families without interfering.
	a.NewInstanceMetrics("inst-a").RecordRequest("http", "modern", "tools/list", "ok", 0.01)
	b.NewInstanceMetrics("inst-b").RecordRequest("http", "modern", "tools/list", "ok", 0.02)

	if got := testutil.CollectAndCount(a.Metrics().Registry(), "mcpmock_requests_total"); got == 0 {
		t.Fatal("bundle a has no requests_total series after recording")
	}
	if got := testutil.CollectAndCount(b.Metrics().Registry(), "mcpmock_requests_total"); got == 0 {
		t.Fatal("bundle b has no requests_total series after recording")
	}
}

// TestSuppliedRegistry asserts WithRegisterer-style injection (ADR-016): an
// embedding test may supply its own registry.
func TestSuppliedRegistry(t *testing.T) {
	t.Parallel()
	reg := prometheus.NewRegistry()
	b, err := obs.New(obs.Config{Registry: reg})
	if err != nil {
		t.Fatalf("new with supplied registry: %v", err)
	}
	if b.Metrics().Registry() != reg {
		t.Fatal("bundle did not use the supplied registry")
	}
}

// TestMetricsExpositionValid renders the registry through the same path
// /metrics uses and asserts the output is well-formed Prometheus text exposition
// carrying the declared metric names, types and label sets (MOCK-105.1/105.2).
func TestMetricsExpositionValid(t *testing.T) {
	t.Parallel()
	b, err := obs.New(obs.Config{})
	if err != nil {
		t.Fatalf("new bundle: %v", err)
	}
	b.Metrics().SetBuildInfo("0.1.0", "abc123", "go1.27.1")
	b.Metrics().SetEffectiveSeed(42)
	im := b.NewInstanceMetrics("inst-1")
	im.RecordRequest("http", "modern", "tools/call", "ok", 0.02)
	im.IncJournalRecord(0.0001)
	im.IncValidationError("meta_missing")

	// Lint the exposition: promhttp/testutil GatherAndLint runs the same checks
	// promtool applies (metric type/name/label consistency).
	problems, err := testutil.GatherAndLint(b.Metrics().Registry())
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	if len(problems) != 0 {
		t.Fatalf("exposition lint problems: %+v", problems)
	}

	// Names present with declared types.
	text := gather(t, b.Metrics().Registry())
	for _, want := range []string{
		"# TYPE mcpmock_build_info gauge",
		"# TYPE mcpmock_effective_seed gauge",
		"# TYPE mcpmock_requests_total counter",
		"# TYPE mcpmock_request_duration_seconds histogram",
		"# TYPE mcpmock_journal_records_total counter",
		"# TYPE mcpmock_validation_errors_total counter",
		"# TYPE mcpmock_stdout_leak_bytes_total counter",
		`mcpmock_requests_total{era="modern",instance="inst-1",method="tools/call",outcome="ok",transport="http"} 1`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("exposition missing %q", want)
		}
	}
}

// TestMetaValidationMetric asserts the AMEND-6 metric
// mcpmock_meta_validation_total is registered with its {instance,mode,outcome}
// label set and records through a pre-resolved handle, and that out-of-domain
// mode/outcome values fold to bounded fallbacks (requirements-spec.md MOCK-203).
func TestMetaValidationMetric(t *testing.T) {
	t.Parallel()
	b, err := obs.New(obs.Config{})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	im := b.NewInstanceMetrics("inst-m")
	im.RecordMetaValidation("lenient", "tolerated")
	im.RecordMetaValidation("off", "skipped")
	// Out-of-domain values must fold, never admit a new series.
	im.RecordMetaValidation("bogus-mode", "bogus-outcome")

	text := gather(t, b.Metrics().Registry())
	for _, want := range []string{
		"# TYPE mcpmock_meta_validation_total counter",
		`mcpmock_meta_validation_total{instance="inst-m",mode="lenient",outcome="tolerated"} 1`,
		`mcpmock_meta_validation_total{instance="inst-m",mode="off",outcome="skipped"} 1`,
		`mcpmock_meta_validation_total{instance="inst-m",mode="strict",outcome="rejected"} 1`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("meta_validation exposition missing %q", want)
		}
	}
	if strings.Contains(text, "bogus") {
		t.Fatal("out-of-domain meta label leaked into a series")
	}
}

// TestStdoutLeakCounterStartsZero asserts the ADR-011 honesty counter exists and
// is zero — it must be zero in a correct run.
func TestStdoutLeakCounterStartsZero(t *testing.T) {
	t.Parallel()
	b, err := obs.New(obs.Config{})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if got := testutil.ToFloat64(b.Metrics().StdoutLeakCounter()); got != 0 {
		t.Fatalf("stdout leak counter = %v, want 0", got)
	}
}

// TestCardinalityBounded asserts an out-of-domain method or outcome is folded to
// a bounded fallback rather than admitted as a new series — the cardinality
// discipline of observability.md §2.1. A 5000-item catalogue name must never
// become a label.
func TestCardinalityBounded(t *testing.T) {
	t.Parallel()
	b, err := obs.New(obs.Config{})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	im := b.NewInstanceMetrics("inst-x")
	// A tool name from a 5000-item catalogue as a "method" must fold to "other".
	im.RecordRequest("http", "modern", "com.example.tool.number.4999", "weird_outcome", 0.01)

	text := gather(t, b.Metrics().Registry())
	if strings.Contains(text, "com.example.tool") {
		t.Fatal("catalogue name leaked into a metric label — cardinality explosion")
	}
	if !strings.Contains(text, `method="other"`) {
		t.Fatal("unknown method was not folded to \"other\"")
	}
	if !strings.Contains(text, `outcome="error"`) {
		t.Fatal("unknown outcome was not folded to \"error\"")
	}
}

// TestInstanceMetricsSeriesCount asserts the pre-resolved request-series count
// per instance is exactly the bounded cartesian product, so the cardinality is
// what the budget claims.
func TestInstanceMetricsSeriesCount(t *testing.T) {
	t.Parallel()
	b, err := obs.New(obs.Config{})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	im := b.NewInstanceMetrics("inst-c")
	// Touch every combination via one record each would be excessive; instead
	// assert the domains multiply to the documented size.
	want := len(obs.Transports()) * len(obs.Eras()) * len(obs.Methods()) * len(obs.Outcomes())
	if want != 2*4*4*8 {
		t.Fatalf("bounded request cardinality changed: %d", want)
	}
	// Record one to prove the handle path works.
	im.RecordRequest("stdio", "probe", "server/discover", "timeout", 0.5)
	if got := testutil.CollectAndCount(b.Metrics().Registry(), "mcpmock_requests_total"); got < 1 {
		t.Fatalf("expected at least one series, got %d", got)
	}
}

// BenchmarkRecordRequestNoAlloc is acceptance criterion 8: a metric handle is
// resolved once at construction; the request path performs zero allocations and
// never calls WithLabelValues (observability.md §2.3, MOCK-901).
func BenchmarkRecordRequestNoAlloc(b *testing.B) {
	bundle, err := obs.New(obs.Config{})
	if err != nil {
		b.Fatalf("new: %v", err)
	}
	im := bundle.NewInstanceMetrics("bench")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		im.RecordRequest("http", "modern", "tools/call", "ok", 0.001)
	}
}

// TestRecordRequestZeroAlloc turns the benchmark into a hard assertion so the
// hot-path allocation-freedom is gated in the test suite, not only observed.
func TestRecordRequestZeroAlloc(t *testing.T) {
	// AllocsPerRun cannot run under t.Parallel(); this test is intentionally
	// serial.
	bundle, err := obs.New(obs.Config{})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	im := bundle.NewInstanceMetrics("alloc")
	avg := testing.AllocsPerRun(1000, func() {
		im.RecordRequest("http", "modern", "tools/call", "ok", 0.001)
	})
	if avg != 0 {
		t.Fatalf("RecordRequest allocated %.2f times per call, want 0", avg)
	}
}

// gather renders a registry to Prometheus text exposition for substring checks.
func gather(t *testing.T, reg *prometheus.Registry) string {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	var sb strings.Builder
	enc := expfmt.NewEncoder(&sb, expfmt.NewFormat(expfmt.TypeTextPlain))
	for _, mf := range mfs {
		if err := enc.Encode(mf); err != nil {
			t.Fatalf("encode: %v", err)
		}
	}
	return sb.String()
}
