package engine

// Metrics is the engine's view of an instance's pre-resolved metric handles
// (observability.md §2.3). It is CONSUMER-DEFINED — the engine names only the
// handful of hot-path recordings it makes, and internal/obs's *InstanceMetrics
// satisfies it — so internal/engine need not import internal/obs and the
// request path never calls WithLabelValues. Every method is allocation-free and
// safe on the request hot path (MOCK-901).
//
// A nil Metrics is legal: [Instance.Metrics] may return nil when metrics are not
// configured, and the pipeline guards every call, so metrics are strictly
// optional to the engine's correctness.
type Metrics interface {
	// IncInFlight and DecInFlight bracket a request's lifetime on the in-flight
	// gauge; the pipeline defers DecInFlight immediately after IncInFlight.
	IncInFlight()
	// DecInFlight decrements the in-flight gauge.
	DecInFlight()
	// RecordRequest records one completed request under bounded labels
	// (transport, era, method, outcome) and its duration in seconds. Out-of-
	// domain label values are folded to a bounded fallback by the implementation.
	RecordRequest(transport, era, method, outcome string, seconds float64)
	// IncJournalRecord records one written journal record and its write
	// duration in seconds. The pipeline calls it at stage 9 only when a record
	// was actually written.
	IncJournalRecord(writeSeconds float64)
	// SetSnapshotGeneration publishes the snapshot generation observed for this
	// request (ADR-014), so the current generation is visible in metrics.
	SetSnapshotGeneration(gen uint64)
}

// outcome labels for [Metrics.RecordRequest]. They are bounded strings the obs
// package already knows (observability.md §2.1: the outcome domain), named here
// as constants so the pipeline passes no bare literal and the label domain stays
// closed. They are metric labels, not wire values, so they are not subject to
// ADR-019 wire containment.
const (
	// outcomeOK marks a request that produced a successful result.
	outcomeOK = "ok"
	// outcomeError marks a request that produced a JSON-RPC error response.
	outcomeError = "error"
	// outcomeCanceled marks a request canceled by client disconnect / EOF
	// (MOCK-212). US spelling: misspell is locale:US.
	outcomeCanceled = "canceled"
)
