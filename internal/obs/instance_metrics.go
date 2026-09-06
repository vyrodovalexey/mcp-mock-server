package obs

import (
	"github.com/prometheus/client_golang/prometheus"
)

// requestKey is the fully-bound label tuple for mcpmock_requests_total. It is a
// comparable value used as a map key so a fully-resolved counter handle can be
// fetched with a single map lookup on the request hot path — no WithLabelValues,
// no lock, no allocation (observability.md §2.3, ADR-016).
type requestKey struct {
	transport string
	era       string
	method    string
	outcome   string
}

// durationKey is the fully-bound label tuple for the per-request histograms that
// carry {method, outcome} (duration) — instance is already fixed per
// InstanceMetrics, so it is not part of the key.
type durationKey struct {
	method  string
	outcome string
}

// metaKey is the fully-bound label tuple for mcpmock_meta_validation_total,
// carrying {mode, outcome} — instance is fixed per InstanceMetrics
// (requirements-spec.md MOCK-203, AMEND-6).
type metaKey struct {
	mode    string
	outcome string
}

// InstanceMetrics holds metric handles pre-resolved for a single instance. All
// WithLabelValues resolution happens once, here, at instance construction; the
// request path only reads from these maps and calls the resolved Counter /
// Observer, so it never takes the label-registry lock and never allocates
// (MOCK-901, observability.md §2.3). Acceptance criterion 8 is proved by
// BenchmarkRecordRequestNoAlloc.
type InstanceMetrics struct {
	instance string

	// requests is the pre-resolved cartesian product of the bounded request
	// label domains for this instance. Its size is fixed:
	// len(Transports())*len(Eras())*len(Methods())*len(Outcomes()) = 2*4*4*8 = 256.
	requests map[requestKey]prometheus.Counter

	// duration/reqBody/respBody are pre-resolved histogram observers.
	duration map[durationKey]prometheus.Observer
	reqBody  map[string]prometheus.Observer // keyed by method
	respBody map[string]prometheus.Observer // keyed by method

	inFlight    prometheus.Gauge
	validation  map[string]prometheus.Counter // keyed by kind
	metaValid   map[metaKey]prometheus.Counter
	jRecords    prometheus.Gauge
	jRecTotal   prometheus.Counter
	jDropped    map[string]prometheus.Counter // keyed by policy
	jBytes      prometheus.Gauge
	jWriteDur   prometheus.Observer
	snapshotGen prometheus.Gauge

	// Membership sets for the bounded label domains, built once at
	// construction. They let the hot path fold an out-of-domain value to its
	// bounded fallback with a single map read and no allocation — the enum
	// functions (Methods() etc.) allocate a fresh slice and must never be called
	// on the request path.
	methodSet    map[string]struct{}
	transportSet map[string]struct{}
	eraSet       map[string]struct{}
	outcomeSet   map[string]struct{}
	metaModeSet  map[string]struct{}
	metaOutSet   map[string]struct{}
}

// newInstanceMetrics pre-resolves every label handle this instance can touch on
// the hot path. It is O(number of label combinations) once, at construction,
// and is why the request path is allocation-free.
func newInstanceMetrics(m *Metrics, instance string) *InstanceMetrics {
	transports, eras, methods, outcomes := Transports(), Eras(), Methods(), Outcomes()
	kinds, policies := ValidationKinds(), JournalDropPolicies()
	metaModes, metaOutcomes := MetaValidationModes(), MetaValidationOutcomes()

	im := &InstanceMetrics{
		instance:   instance,
		requests:   make(map[requestKey]prometheus.Counter, len(transports)*len(eras)*len(methods)*len(outcomes)),
		duration:   make(map[durationKey]prometheus.Observer, len(methods)*len(outcomes)),
		reqBody:    make(map[string]prometheus.Observer, len(methods)),
		respBody:   make(map[string]prometheus.Observer, len(methods)),
		validation: make(map[string]prometheus.Counter, len(kinds)),
		metaValid:  make(map[metaKey]prometheus.Counter, len(metaModes)*len(metaOutcomes)),
		jDropped:   make(map[string]prometheus.Counter, len(policies)),
	}

	for _, tr := range transports {
		for _, er := range eras {
			for _, me := range methods {
				for _, oc := range outcomes {
					k := requestKey{transport: tr, era: er, method: me, outcome: oc}
					im.requests[k] = m.requestsTotal.WithLabelValues(instance, tr, er, me, oc)
				}
			}
		}
	}
	for _, me := range methods {
		im.reqBody[me] = m.requestBodyBytes.WithLabelValues(instance, me)
		im.respBody[me] = m.respBodyBytes.WithLabelValues(instance, me)
		for _, oc := range outcomes {
			im.duration[durationKey{method: me, outcome: oc}] =
				m.requestDuration.WithLabelValues(instance, me, oc)
		}
	}
	for _, kind := range kinds {
		im.validation[kind] = m.validationErrors.WithLabelValues(instance, kind)
	}
	for _, mode := range metaModes {
		for _, oc := range metaOutcomes {
			im.metaValid[metaKey{mode: mode, outcome: oc}] =
				m.metaValidation.WithLabelValues(instance, mode, oc)
		}
	}
	for _, pol := range policies {
		im.jDropped[pol] = m.journalDropped.WithLabelValues(instance, pol)
	}

	im.inFlight = m.requestsInFlight.WithLabelValues(instance)
	im.jRecords = m.journalRecords.WithLabelValues(instance)
	im.jRecTotal = m.journalRecTotal.WithLabelValues(instance)
	im.jBytes = m.journalBytes.WithLabelValues(instance)
	im.jWriteDur = m.journalWriteDur.WithLabelValues(instance)
	im.snapshotGen = m.snapshotGen.WithLabelValues(instance)

	im.methodSet = toSet(methods)
	im.transportSet = toSet(transports)
	im.eraSet = toSet(eras)
	im.outcomeSet = toSet(outcomes)
	im.metaModeSet = toSet(metaModes)
	im.metaOutSet = toSet(metaOutcomes)
	return im
}

// toSet builds a membership set from a slice. Used once per instance at
// construction, never on the hot path.
func toSet(vals []string) map[string]struct{} {
	s := make(map[string]struct{}, len(vals))
	for _, v := range vals {
		s[v] = struct{}{}
	}
	return s
}

// Instance returns the instance name these handles are bound to.
func (im *InstanceMetrics) Instance() string { return im.instance }

// RecordRequest records one completed request against the pre-resolved counter
// and duration histogram. It performs two map lookups and no allocation. An
// out-of-domain method or outcome is normalized to a bounded fallback so a
// caller can never widen cardinality through this path.
func (im *InstanceMetrics) RecordRequest(transport, era, method, outcome string, seconds float64) {
	transport = fold(im.transportSet, transport, "http")
	era = fold(im.eraSet, era, "modern")
	method = fold(im.methodSet, method, "other")
	outcome = fold(im.outcomeSet, outcome, "error")

	if c, ok := im.requests[requestKey{transport: transport, era: era, method: method, outcome: outcome}]; ok {
		c.Inc()
	}
	if o, ok := im.duration[durationKey{method: method, outcome: outcome}]; ok {
		o.Observe(seconds)
	}
}

// ObserveRequestBody records a request body size for a method.
func (im *InstanceMetrics) ObserveRequestBody(method string, bytes float64) {
	if o, ok := im.reqBody[fold(im.methodSet, method, "other")]; ok {
		o.Observe(bytes)
	}
}

// ObserveResponseBody records a response body size for a method (MOCK-504).
func (im *InstanceMetrics) ObserveResponseBody(method string, bytes float64) {
	if o, ok := im.respBody[fold(im.methodSet, method, "other")]; ok {
		o.Observe(bytes)
	}
}

// IncInFlight and DecInFlight bracket a request's lifetime on the in-flight
// gauge.
func (im *InstanceMetrics) IncInFlight() { im.inFlight.Inc() }

// DecInFlight decrements the in-flight gauge.
func (im *InstanceMetrics) DecInFlight() { im.inFlight.Dec() }

// IncValidationError records a validation failure of a bounded kind
// (MOCK-203…206). An unknown kind is dropped rather than admitted as a new
// series, keeping the label bounded.
func (im *InstanceMetrics) IncValidationError(kind string) {
	if c, ok := im.validation[kind]; ok {
		c.Inc()
	}
}

// RecordMetaValidation records one _meta validation result under a bounded
// {mode, outcome} pair (requirements-spec.md MOCK-203, AMEND-6). Out-of-domain
// values are folded to their bounded fallbacks so this path can never widen
// cardinality. It is a single map read and allocation-free, safe on the request
// hot path. This handle is pre-resolved here for TASK-017's validator to record
// through; obs itself never fires it.
func (im *InstanceMetrics) RecordMetaValidation(mode, outcome string) {
	mode = fold(im.metaModeSet, mode, "strict")
	outcome = fold(im.metaOutSet, outcome, "rejected")
	if c, ok := im.metaValid[metaKey{mode: mode, outcome: outcome}]; ok {
		c.Inc()
	}
}

// SetJournalRecords publishes the current ring occupancy for this instance.
func (im *InstanceMetrics) SetJournalRecords(n int) { im.jRecords.Set(float64(n)) }

// IncJournalRecord records one written journal record and its write duration.
func (im *InstanceMetrics) IncJournalRecord(writeSeconds float64) {
	im.jRecTotal.Inc()
	im.jWriteDur.Observe(writeSeconds)
}

// IncJournalDropped records a dropped record under a bounded policy (the honesty
// metric). An unknown policy is dropped rather than admitted.
func (im *InstanceMetrics) IncJournalDropped(policy string) {
	if c, ok := im.jDropped[policy]; ok {
		c.Inc()
	}
}

// SetJournalBytes publishes the current journal byte occupancy.
func (im *InstanceMetrics) SetJournalBytes(n int64) { im.jBytes.Set(float64(n)) }

// SetSnapshotGeneration publishes the current Snapshot generation (ADR-014).
func (im *InstanceMetrics) SetSnapshotGeneration(gen uint64) { im.snapshotGen.Set(float64(gen)) }

// fold returns v if it is a member of the bounded domain set, else fallback. It
// keeps a label strictly within its declared domain by construction, so a
// 5000-item catalog name (observability.md §2.1) can never explode a label. It
// is a single map read and allocation-free, so it is safe on the request hot
// path.
func fold(set map[string]struct{}, v, fallback string) string {
	if _, ok := set[v]; ok {
		return v
	}
	return fallback
}
