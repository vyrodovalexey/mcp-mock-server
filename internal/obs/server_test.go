package obs_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vyrodovalexey/mcp-mock-server/internal/obs"
)

// TestHealthReadinessLifecycle asserts /healthz reports live once the process is
// up and /readyz returns 503 until every listener is accepting, then 200
// (observability.md §5, MOCK-105 acceptance criterion 7).
func TestHealthReadinessLifecycle(t *testing.T) {
	t.Parallel()
	b, err := obs.New(obs.Config{})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	health := obs.NewHealthState()
	h := b.Handler(health)

	// At startup: live, not ready.
	if code := get(t, h, obs.PathHealthz); code != http.StatusOK {
		t.Fatalf("/healthz at startup = %d, want 200", code)
	}
	if code := get(t, h, obs.PathReadyz); code != http.StatusServiceUnavailable {
		t.Fatalf("/readyz before ready = %d, want 503", code)
	}

	// Listeners up: ready.
	health.SetReady(true)
	if code := get(t, h, obs.PathReadyz); code != http.StatusOK {
		t.Fatalf("/readyz after ready = %d, want 200", code)
	}

	// Shutdown drain: not ready, still live.
	health.SetReady(false)
	if code := get(t, h, obs.PathReadyz); code != http.StatusServiceUnavailable {
		t.Fatalf("/readyz during drain = %d, want 503", code)
	}
	if code := get(t, h, obs.PathHealthz); code != http.StatusOK {
		t.Fatalf("/healthz during drain = %d, want 200 (liveness ignores load)", code)
	}
}

// TestMetricsEndpointServesRegistry asserts GET /metrics returns Prometheus text
// exposition from the Bundle's own registry (MOCK-105.1).
func TestMetricsEndpointServesRegistry(t *testing.T) {
	t.Parallel()
	b, err := obs.New(obs.Config{})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	b.Metrics().SetInstances(3)
	h := b.Handler(obs.NewHealthState())

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, obs.PathMetrics, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/metrics = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "mcpmock_instances 3") {
		t.Fatalf("/metrics missing expected series; body:\n%s", body)
	}
	if !strings.Contains(body, "# TYPE mcpmock_instances gauge") {
		t.Fatalf("/metrics missing TYPE line; body:\n%s", body)
	}
}

// get performs an in-memory GET against a handler and returns the status code,
// draining the body.
func get(t *testing.T, h http.Handler, path string) int {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	_, _ = io.Copy(io.Discard, rec.Body)
	return rec.Code
}
