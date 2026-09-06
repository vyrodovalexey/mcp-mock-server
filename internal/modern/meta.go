package modern

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/vyrodovalexey/mcp-mock-server/internal/engine"
	"github.com/vyrodovalexey/mcp-mock-server/internal/wire"
	"github.com/vyrodovalexey/mcp-mock-server/journalapi"
)

// meta.go is the stage-4 _meta validation logic (MOCK-203, AMEND-6). It plugs
// into internal/engine's pipeline through the [engine.MetaValidator] seam
// (architecture.md §5 stage 4): an *Instance's snapshot returns a [*MetaValidator]
// and the pipeline invokes it at stage 4, so this validation is wired in WITHOUT
// restructuring the pipeline (the engine's own doc.go states this is how
// TASK-017 inserts itself).
//
// Wire containment (ADR-019): every _meta key, error code and data-payload shape
// this file names comes from internal/wire — no wire literal appears here. The
// -32602 fault is built through [engine.InvalidParamsFault], the single exported
// constructor the engine provides for this exact purpose; the -32600 fault is
// built from wire.ErrCodeInvalidRequest. The validation LOGIC is this package's;
// the vocabulary and the encoder are internal/wire's and internal/engine's.

// Outcome labels for the metaValidation journal part and the
// mcpmock_meta_validation_total{mode,outcome} metric (AMEND-6, closes G-3).
// These are OBSERVABILITY labels, not wire values, so — like the engine's own
// request-outcome constants — they are named here rather than sourced from
// internal/wire (they are not subject to ADR-019 wire containment). The three
// anomaly outcomes match internal/obs's bounded metric enum exactly
// (obs.MetaValidationOutcomes); outcomeOK is the journal-only "validation ran
// and passed" outcome, which fires no metric because the metric has no ok
// series.
const (
	// outcomeOK marks a request whose _meta satisfied strict validation. It is
	// recorded in the journal (so a reader sees validation ran) but fires no
	// metric: mcpmock_meta_validation_total counts only the three anomalies.
	outcomeOK = "ok"
	// outcomeRejected marks a strict-mode rejection (-32602 / 400). It is the
	// metric outcome for the strict column of the AMEND-6 mode table.
	outcomeRejected = "rejected"
	// outcomeTolerated marks a lenient-mode toleration: a strict server would
	// have rejected, but lenient accepts AND records the missing set so the
	// latent hub bug stays visible.
	outcomeTolerated = "tolerated"
	// outcomeSkipped marks off mode: the presence check is not run and the
	// missing set is NOT computed — the observable difference from lenient.
	outcomeSkipped = "skipped"
)

// Wire error MESSAGE strings. These are the short JSON-RPC error.message values
// the client sees; like the engine's own error messages in emit.go ("Parse
// error", "Invalid Request") they are human-readable message text, NOT wire
// vocabulary (method names, error codes, _meta keys) — so they are not subject
// to ADR-019 containment and are named here. The specific missing fields are
// carried in error.data.missing, never spelled into the message, so the message
// stays constant across every _meta rejection and the golden bytes are stable.
const (
	// metaMissingMessage is the -32602 message for a missing required _meta
	// field (MOCK-203.1-.4). The absent fields are named in data.missing.
	metaMissingMessage = "invalid params: missing required _meta field(s)"
	// metaNotObjectMessage is the -32602 message for a present-but-non-object
	// params._meta (mode table: rejected in strict and lenient).
	metaNotObjectMessage = "invalid params: _meta must be an object"
	// invalidRequestMessage is the -32600 message for a present-but-non-object
	// params (mode table: rejected in every mode).
	invalidRequestMessage = "invalid request: params must be an object"
)

// MetaMetricRecorder is the minimal, consumer-defined surface [MetaValidator]
// needs to record the mcpmock_meta_validation_total{mode,outcome} counter
// (AMEND-6). It is exactly the shape of *obs.InstanceMetrics.RecordMetaValidation,
// which the owning instance (TASK-015) injects at construction.
//
// Defining it here — rather than importing internal/obs — keeps internal/modern
// off internal/obs in the dependency graph (architecture.md §6.1: modern
// depends only on internal/wire below the engine spine), so the era package
// stays a pure wire consumer. A nil recorder is legal and disables metric
// recording; the journal outcome is unaffected, so validation stays fully
// observable in a metrics-less test or deployment.
type MetaMetricRecorder interface {
	// RecordMetaValidation records one validation result under the bounded
	// {mode, outcome} label pair. It must be allocation-free and fold
	// out-of-domain values, which *obs.InstanceMetrics guarantees.
	RecordMetaValidation(mode, outcome string)
}

// MetaValidator validates a request's params._meta at pipeline stage 4
// (MOCK-203). It implements [engine.MetaValidator]. It is immutable after
// construction and safe for concurrent use by every request on an instance: it
// holds only the configured mode and an optional metric recorder, and reads the
// per-request _meta off the [engine.Exchange] passed to each call.
//
// # The three modes (switches.validateMeta, requirements-spec.md MOCK-203, AMEND-6)
//
//   - strict (default): a missing required _meta field, or a present-but-non-object
//     _meta, is REJECTED with JSON-RPC -32602 and HTTP 400; error.data.missing
//     names exactly the absent fields. The journal records the rejected fields
//     with accepted=false, outcome="rejected".
//   - lenient: the PRESENCE of protocolVersion and clientCapabilities is relaxed
//     — a request missing either (or _meta entirely) is ACCEPTED, but the missing
//     set is still COMPUTED and recorded (accepted=true, outcome="tolerated"), so
//     a hub that silently relies on the server rejecting its malformed _meta has
//     a reproducible, visible deficiency. lenient relaxes nothing STRUCTURAL: a
//     present-but-non-object _meta is still -32602. It is a semantic relaxation,
//     never a syntactic one.
//   - off: the presence check is NOT run and the missing set is NOT computed
//     (outcome="skipped", missing always empty) — the observable difference from
//     lenient. A present-but-non-object _meta is treated as absent rather than
//     rejected, because off computes nothing to reject on.
//
// # Structural well-formedness (all modes)
//
// A params value that is present but NOT a JSON object is a JSON-RPC structural
// error the envelope decoder does not catch (params is opaque there), so it is
// rejected with -32600 / 400 in every mode, including off — a structurally
// broken request cannot be faithfully decoded into the journal, which
// MOCK-601 / MOCK-203.6 require.
//
// # Determinism and cost (MOCK-901)
//
// ValidateMeta draws no RNG and reads no clock (ADR-002: stage 4 is a zero-draw
// stage). Off mode does a single length check and returns; strict/lenient parse
// params once with encoding/json and, only when a field is actually missing,
// allocate the small missing slice and the data payload. The valid strict path
// allocates only the journal part.
type MetaValidator struct {
	// mode is one of wire.MetaModeStrict / MetaModeLenient / MetaModeOff.
	mode string
	// rec records the metric; nil disables metric recording.
	rec MetaMetricRecorder
}

// Compile-time assertion that *MetaValidator satisfies the engine seam, so a
// signature drift fails the build here rather than at the instance wiring site.
var _ engine.MetaValidator = (*MetaValidator)(nil)

// NewMetaValidator returns a stage-4 _meta validator for the given
// switches.validateMeta mode, recording the mcpmock_meta_validation_total metric
// through rec (which may be nil to disable metric recording).
//
// An unrecognized mode folds to strict — the default and the safe choice: an
// operator who fat-fingers the mode gets the conformant-server behavior, not a
// silently permissive one. The mode string is compared against the wire.MetaMode*
// constants, so the accepted set is exactly the switches.validateMeta enum.
func NewMetaValidator(mode string, rec MetaMetricRecorder) *MetaValidator {
	switch mode {
	case wire.MetaModeStrict, wire.MetaModeLenient, wire.MetaModeOff:
		// A recognized mode is used as given.
	default:
		mode = wire.MetaModeStrict
	}
	return &MetaValidator{mode: mode, rec: rec}
}

// ValidateMeta implements [engine.MetaValidator]. It returns a non-nil
// [*engine.Fault] to reject the request (with the journal part describing the
// rejection), or a nil fault to accept it (with the journal part describing what
// a strict server would have required). It records the bounded metric as a side
// effect. ctx is accepted to satisfy the seam and to keep one context threaded
// through the pipeline (contextcheck); the validation itself is pure and does
// not block, so it does not consult ctx.
func (v *MetaValidator) ValidateMeta(
	_ context.Context, ex *engine.Exchange,
) (*engine.Fault, *journalapi.MetaValidationPart) {
	params := requestParams(ex)

	// Structural check first, in EVERY mode: params present but not an object is
	// -32600 (requirements-spec.md MOCK-203 mode table, "params present but not
	// an object" row). Off does not relax this — it relaxes only _meta presence.
	if !paramsIsObjectOrAbsent(params) {
		// No metaValidation part: the request never reached _meta validation,
		// and the fault is a JSON-RPC structural error, not a MOCK-203 outcome.
		return invalidRequestFault(), nil
	}

	if v.mode == wire.MetaModeOff {
		return v.validateOff()
	}
	return v.validateComputing(params)
}

// validateOff is the off-mode path: it computes nothing and records
// outcome="skipped" with an always-empty missing set (MOCK-203.8). It accepts
// unconditionally. A present-but-non-object _meta was already ruled non-fatal by
// ValidateMeta calling here only after the params-object structural check, and
// off treats such an _meta as absent (it computes no missing set to reject on).
func (v *MetaValidator) validateOff() (*engine.Fault, *journalapi.MetaValidationPart) {
	v.record(outcomeSkipped)
	return nil, &journalapi.MetaValidationPart{
		Mode:    wire.MetaModeOff,
		Outcome: outcomeSkipped,
		Missing: nil,
	}
}

// validateComputing is the strict/lenient path: it decodes _meta, enforces the
// structural rule that a present _meta must be an object (-32602 in BOTH strict
// and lenient — a syntactic error lenient does not relax), computes the missing
// required-field set, and then either rejects (strict) or tolerates-and-records
// (lenient). It is shared because the two modes differ only in what they DO with
// a non-empty missing set, not in how they compute it.
func (v *MetaValidator) validateComputing(
	params json.RawMessage,
) (*engine.Fault, *journalapi.MetaValidationPart) {
	metaRaw, present, isObject := extractMeta(params)
	if present && !isObject {
		// A present-but-non-object _meta (e.g. "_meta": 4) is a STRUCTURAL error
		// rejected in strict and lenient alike (mode table row "_meta present
		// but not a JSON object"). It has no computable missing set, so
		// error.data carries no data.missing payload; the journal records the
		// rejection with an empty missing set.
		part := &journalapi.MetaValidationPart{Mode: v.mode, Outcome: outcomeRejected}
		v.record(outcomeRejected)
		return metaNotObjectFault(), part
	}

	missing := computeMissing(metaRaw)
	if len(missing) == 0 {
		return v.acceptClean()
	}
	if v.mode == wire.MetaModeLenient {
		return v.tolerate(missing)
	}
	return v.reject(missing)
}

// acceptClean is the valid path in strict/lenient: every required field was
// present, so the request is accepted and the journal records outcome="ok" with
// an empty missing set. No metric fires — the metric counts only anomalies.
func (v *MetaValidator) acceptClean() (*engine.Fault, *journalapi.MetaValidationPart) {
	return nil, &journalapi.MetaValidationPart{
		Mode:    v.mode,
		Outcome: outcomeOK,
		Missing: nil,
	}
}

// tolerate is the lenient anomaly path: the request is ACCEPTED (nil fault) but
// the tolerated missing set is recorded (accepted:true, outcome="tolerated"),
// which is what keeps the hub's latent dependency on server rejection visible
// (MOCK-203.7). The metric fires outcome="tolerated".
func (v *MetaValidator) tolerate(missing []string) (*engine.Fault, *journalapi.MetaValidationPart) {
	v.record(outcomeTolerated)
	return nil, &journalapi.MetaValidationPart{
		Mode:    wire.MetaModeLenient,
		Outcome: outcomeTolerated,
		Missing: missing,
	}
}

// reject is the strict anomaly path: the request is REJECTED with -32602 / 400
// (MOCK-203.1-.4). error.data.missing names exactly the absent fields; the
// journal records the rejected fields with outcome="rejected". The metric fires
// outcome="rejected".
func (v *MetaValidator) reject(missing []string) (*engine.Fault, *journalapi.MetaValidationPart) {
	v.record(outcomeRejected)
	part := &journalapi.MetaValidationPart{
		Mode:    wire.MetaModeStrict,
		Outcome: outcomeRejected,
		Missing: missing,
	}
	return missingFieldsFault(missing), part
}

// record fires the metric for one validation outcome, tagged with this
// validator's mode. It is a no-op when no recorder was injected. It is
// allocation-free (the recorder folds labels into pre-resolved handles).
func (v *MetaValidator) record(outcome string) {
	if v.rec == nil {
		return
	}
	v.rec.RecordMetaValidation(v.mode, outcome)
}

// computeMissing returns, in wire.RequiredMetaFields order, the required _meta
// fields absent from metaRaw. metaRaw is nil for a wholly-absent or empty _meta,
// in which case every required field is missing. It returns nil (not an empty
// slice) when nothing is missing, so a caller can test len()==0 and never
// allocates on the valid path.
func computeMissing(metaRaw map[string]json.RawMessage) []string {
	var missing []string
	for _, field := range wire.RequiredMetaFields() {
		if _, ok := metaRaw[field]; !ok {
			missing = append(missing, field)
		}
	}
	return missing
}

// extractMeta pulls the raw params._meta member from a params object. It reports
// present=true when the _meta member exists at all (even if null or non-object),
// so absence stays distinguishable from a present-but-malformed value; isObject
// reports whether _meta decoded to a JSON object. metaRaw is the decoded object
// (nil unless isObject). params is assumed to be an object or absent — the
// caller checks that first.
func extractMeta(params json.RawMessage) (metaRaw map[string]json.RawMessage, present, isObject bool) {
	if len(params) == 0 {
		return nil, false, false
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(params, &obj); err != nil {
		// params was not an object after all; the structural caller handles it.
		return nil, false, false
	}
	raw, ok := obj[wire.MetaKey]
	if !ok {
		return nil, false, false
	}
	var meta map[string]json.RawMessage
	if err := json.Unmarshal(raw, &meta); err != nil {
		// _meta is present but not a JSON object (e.g. a number or string).
		return nil, true, false
	}
	return meta, true, true
}

// paramsIsObjectOrAbsent reports whether params is absent, or present and a JSON
// object. A present-but-non-object params (array, scalar, null) returns false
// and is a -32600 in every mode. An absent params is legal here: a request with
// no params has no _meta, which strict/lenient treat as every required field
// missing.
func paramsIsObjectOrAbsent(params json.RawMessage) bool {
	if len(params) == 0 {
		return true
	}
	var obj map[string]json.RawMessage
	return json.Unmarshal(params, &obj) == nil
}

// requestParams returns the raw params of the decoded request, or nil when the
// request did not decode far enough to have any. Stage 1 has already run and
// populated ex.Request, so this is a plain field read on the happy path.
func requestParams(ex *engine.Exchange) json.RawMessage {
	if ex == nil || ex.Request == nil {
		return nil
	}
	return ex.Request.Params
}

// missingFieldsFault builds the -32602 / HTTP 400 fault for a strict-mode
// rejection, carrying the annex 2.12 [P-11] data.missing payload naming the
// absent fields (MOCK-203.4). The fault is constructed through the engine's
// single exported -32602 constructor and the wire data shape, so no wire literal
// and no error-object construction happens here (ADR-019 containment; the wire
// error object is built once, in engine/emit.go).
func missingFieldsFault(missing []string) *engine.Fault {
	data := mustMarshal(wire.MetaMissingErrorData{Missing: missing})
	return engine.InvalidParamsFault(metaMissingMessage, data).
		WithReason("missing required _meta field(s): " + join(missing))
}

// metaNotObjectFault builds the -32602 / HTTP 400 fault for a present-but-non-
// object _meta. It carries no data.missing payload (there is no computable
// missing set for a structurally broken _meta); the journalled reason names the
// structural cause.
func metaNotObjectFault() *engine.Fault {
	return engine.InvalidParamsFault(metaNotObjectMessage, nil).
		WithReason("params._meta is present but is not a JSON object")
}

// invalidRequestFault builds the -32600 / HTTP 400 fault for a present-but-non-
// object params. It is constructed directly from the wire code and the
// documented HTTP status (wire/errors.go): the engine exposes no exported -32600
// constructor, but engine.Fault's fields are exported, and the code and status
// come from internal/wire, so the ADR-019 containment rule (no bare wire
// literal) still holds. The wire error object is still produced in one place
// (engine/emit.go) from this fault.
func invalidRequestFault() *engine.Fault {
	return &engine.Fault{
		Code:       wire.ErrCodeInvalidRequest,
		HTTPStatus: http.StatusBadRequest,
		Message:    invalidRequestMessage,
		Reason:     "params is present but is not a JSON object",
	}
}

// join concatenates fields with ", " for a journalled reason string. It is a
// tiny local helper so the reason does not depend on a formatting import; the
// reason is journal-only and never reaches the wire.
func join(fields []string) string {
	out := ""
	for i, f := range fields {
		if i > 0 {
			out += ", "
		}
		out += f
	}
	return out
}

// mustMarshal marshals the small, fixed-shape data payload. The value is a
// wire.MetaMissingErrorData built from a string slice, which cannot fail to
// marshal, so an error would be a programming error; it returns nil data in that
// impossible case rather than panicking on the request hot path.
func mustMarshal(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}
