package obs

import (
	"io"
	"net/http"
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Health endpoint paths (observability.md §5).
const (
	// PathMetrics serves Prometheus text exposition.
	PathMetrics = "/metrics"
	// PathHealthz is liveness: the process is up.
	PathHealthz = "/healthz"
	// PathReadyz is readiness: every listener is accepting and every Snapshot
	// is published.
	PathReadyz = "/readyz"
)

// HealthState is the readiness/liveness state the observability handlers report.
// Its fields are atomic so a listener goroutine and a scrape can touch them
// without a lock, and so the state can flip during startup and shutdown drain
// without racing (observability.md §5).
type HealthState struct {
	live  atomic.Bool
	ready atomic.Bool
}

// NewHealthState returns a HealthState that reports live and not-ready — the
// correct posture during startup, when /readyz must return 503 until listeners
// are accepting (observability.md §5).
func NewHealthState() *HealthState {
	h := &HealthState{}
	h.live.Store(true)
	return h
}

// SetLive sets liveness. /healthz reports 200 while live; it deliberately
// ignores injected faults and load (observability.md §5) so a fault-injecting
// test does not get its pod restarted.
func (h *HealthState) SetLive(live bool) { h.live.Store(live) }

// SetReady sets readiness. /readyz returns 200 only once this is true — after
// every configured listener is accepting and every Snapshot is published — and
// returns 503 during startup and shutdown drain.
func (h *HealthState) SetReady(ready bool) { h.ready.Store(ready) }

// Live reports the current liveness state.
func (h *HealthState) Live() bool { return h.live.Load() }

// Ready reports the current readiness state.
func (h *HealthState) Ready() bool { return h.ready.Load() }

// Handler returns the http.Handler for the observability listener. It serves the
// Bundle's private registry at /metrics (never the default registry, ADR-007)
// and the liveness/readiness endpoints backed by health. The returned handler
// owns no goroutines and writes nothing to stdout.
func (b *Bundle) Handler(health *HealthState) http.Handler {
	mux := http.NewServeMux()
	mux.Handle(PathMetrics, promhttp.HandlerFor(
		b.metrics.Registry(),
		promhttp.HandlerOpts{Registry: b.metrics.Registry()},
	))
	mux.HandleFunc(PathHealthz, func(w http.ResponseWriter, _ *http.Request) {
		writeHealth(w, health.Live())
	})
	mux.HandleFunc(PathReadyz, func(w http.ResponseWriter, _ *http.Request) {
		writeHealth(w, health.Ready())
	})
	return mux
}

// writeHealth writes a minimal, allocation-light health response: 200 "ok" when
// up, 503 "unavailable" otherwise. The body is plain text so a probe with no
// JSON parser still works.
func writeHealth(w http.ResponseWriter, up bool) {
	if up {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok\n")
		return
	}
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = io.WriteString(w, "unavailable\n")
}
