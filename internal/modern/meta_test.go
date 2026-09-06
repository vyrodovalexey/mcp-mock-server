package modern_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/vyrodovalexey/mcp-mock-server/internal/engine"
	"github.com/vyrodovalexey/mcp-mock-server/internal/jsonrpc"
	"github.com/vyrodovalexey/mcp-mock-server/internal/modern"
	"github.com/vyrodovalexey/mcp-mock-server/internal/wire"
	"github.com/vyrodovalexey/mcp-mock-server/journalapi"
)

// meta_test.go covers the stage-4 _meta validator (MOCK-203, AMEND-6): the
// -32602/400 mapping for a missing required field, the data.missing payload, the
// absent-vs-present-empty distinction, all three switches.validateMeta modes and
// specifically the observable lenient-vs-off difference (journal + metric), the
// structural cases, and the clean valid case. Requests are decoded through the
// real jsonrpc envelope decoder so the validator sees exactly what the pipeline
// would hand it.

// recordedMetric is one RecordMetaValidation call. It is the observable metric
// side the lenient-vs-off distinction is asserted through.
type recordedMetric struct {
	mode    string
	outcome string
}

// fakeRecorder captures RecordMetaValidation calls so a test can assert the
// bounded {mode, outcome} label pair the validator fires (AMEND-6). It stands in
// for *obs.InstanceMetrics without importing internal/obs.
type fakeRecorder struct {
	calls []recordedMetric
}

func (r *fakeRecorder) RecordMetaValidation(mode, outcome string) {
	r.calls = append(r.calls, recordedMetric{mode: mode, outcome: outcome})
}

// exchangeFor decodes body into a real jsonrpc request and wraps it in the
// minimal Exchange the validator reads (only ex.Request.Params is consulted).
func exchangeFor(t *testing.T, body string) *engine.Exchange {
	t.Helper()
	req, err := jsonrpc.DecodeRequest([]byte(body))
	if err != nil {
		t.Fatalf("decode request %q: %v", body, err)
	}
	return &engine.Exchange{Request: req}
}

// The three modes and the sentinel used to prove an unrecognized mode folds to
// strict. Kept as named locals so the table below reads cleanly.
const (
	modeStrict  = wire.MetaModeStrict
	modeLenient = wire.MetaModeLenient
	modeOff     = wire.MetaModeOff
)

// bodyWith builds a tools/call request body whose params._meta carries exactly
// the members named in present. It is used to construct the missing-field and
// present-empty cases precisely.
func bodyWith(members map[string]string) string {
	meta := ""
	first := true
	for k, v := range members {
		if !first {
			meta += ","
		}
		meta += `"` + k + `":` + v
		first = false
	}
	return `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"_meta":{` + meta + `}}}`
}

// fullMeta is a request whose _meta carries both required fields — the valid
// case every strict/lenient run must accept cleanly.
func fullMeta() string {
	return bodyWith(map[string]string{
		wire.MetaKeyProtocolVersion:    `"2026-07-28"`,
		wire.MetaKeyClientCapabilities: `{}`,
	})
}

// TestValidateMetaTable is the primary table: it exercises every mode against
// every _meta shape and asserts the fault (code + HTTP status), the journal
// part (mode, outcome, missing set) and the metric emission together, so a
// change to any one of the three observable channels is caught.
func TestValidateMetaTable(t *testing.T) {
	t.Parallel()

	// A request whose _meta has only protocolVersion (clientCapabilities
	// absent), and vice versa, and one with neither.
	onlyPV := bodyWith(map[string]string{wire.MetaKeyProtocolVersion: `"2026-07-28"`})
	onlyCC := bodyWith(map[string]string{wire.MetaKeyClientCapabilities: `{}`})
	neither := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"_meta":{}}}`
	noMeta := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{}}`
	noParams := `{"jsonrpc":"2.0","id":1,"method":"tools/call"}`

	type want struct {
		reject         bool
		code           int
		httpStatus     int
		outcome        string
		missing        []string
		hasDataMissing bool
		metricFired    bool
	}
	cases := []struct {
		name string
		mode string
		body string
		want want
	}{
		// ---- strict: rejects every absence with -32602 / 400 (MOCK-203.1-.4) ----
		{
			name: "strict/valid accepts cleanly",
			mode: modeStrict, body: fullMeta(),
			want: want{reject: false, outcome: "ok", missing: nil, metricFired: false},
		},
		{
			name: "strict/protocolVersion absent rejects",
			mode: modeStrict, body: onlyCC,
			want: want{
				reject: true, code: wire.ErrCodeInvalidParams, httpStatus: http.StatusBadRequest,
				outcome: "rejected", missing: []string{wire.MetaFieldProtocolVersion},
				hasDataMissing: true, metricFired: true,
			},
		},
		{
			name: "strict/clientCapabilities absent rejects",
			mode: modeStrict, body: onlyPV,
			want: want{
				reject: true, code: wire.ErrCodeInvalidParams, httpStatus: http.StatusBadRequest,
				outcome: "rejected", missing: []string{wire.MetaFieldClientCapabilities},
				hasDataMissing: true, metricFired: true,
			},
		},
		{
			name: "strict/both absent (empty _meta) rejects naming both",
			mode: modeStrict, body: neither,
			want: want{
				reject: true, code: wire.ErrCodeInvalidParams, httpStatus: http.StatusBadRequest,
				outcome:        "rejected",
				missing:        []string{wire.MetaFieldProtocolVersion, wire.MetaFieldClientCapabilities},
				hasDataMissing: true, metricFired: true,
			},
		},
		{
			name: "strict/_meta absent entirely rejects naming both",
			mode: modeStrict, body: noMeta,
			want: want{
				reject: true, code: wire.ErrCodeInvalidParams, httpStatus: http.StatusBadRequest,
				outcome:        "rejected",
				missing:        []string{wire.MetaFieldProtocolVersion, wire.MetaFieldClientCapabilities},
				hasDataMissing: true, metricFired: true,
			},
		},
		{
			name: "strict/params absent rejects naming both",
			mode: modeStrict, body: noParams,
			want: want{
				reject: true, code: wire.ErrCodeInvalidParams, httpStatus: http.StatusBadRequest,
				outcome:        "rejected",
				missing:        []string{wire.MetaFieldProtocolVersion, wire.MetaFieldClientCapabilities},
				hasDataMissing: true, metricFired: true,
			},
		},

		// ---- lenient: accepts, but records the tolerated missing set (203.7) ----
		{
			name: "lenient/valid accepts cleanly, no metric",
			mode: modeLenient, body: fullMeta(),
			want: want{reject: false, outcome: "ok", missing: nil, metricFired: false},
		},
		{
			name: "lenient/protocolVersion absent tolerated",
			mode: modeLenient, body: onlyCC,
			want: want{
				reject: false, outcome: "tolerated",
				missing: []string{wire.MetaFieldProtocolVersion}, metricFired: true,
			},
		},
		{
			name: "lenient/both absent tolerated naming both",
			mode: modeLenient, body: neither,
			want: want{
				reject: false, outcome: "tolerated",
				missing:     []string{wire.MetaFieldProtocolVersion, wire.MetaFieldClientCapabilities},
				metricFired: true,
			},
		},

		// ---- off: no check, no computed missing set (203.8) ----
		{
			name: "off/valid accepts, outcome skipped, missing empty",
			mode: modeOff, body: fullMeta(),
			want: want{reject: false, outcome: "skipped", missing: nil, metricFired: true},
		},
		{
			name: "off/both absent still accepts, missing NOT computed",
			mode: modeOff, body: neither,
			want: want{reject: false, outcome: "skipped", missing: nil, metricFired: true},
		},
		{
			name: "off/_meta absent still accepts, missing NOT computed",
			mode: modeOff, body: noMeta,
			want: want{reject: false, outcome: "skipped", missing: nil, metricFired: true},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			rec := &fakeRecorder{}
			v := modern.NewMetaValidator(c.mode, rec)
			fault, part := v.ValidateMeta(context.Background(), exchangeFor(t, c.body))

			assertFault(t, fault, c.want.reject, c.want.code, c.want.httpStatus)
			assertPart(t, part, c.mode, c.want.outcome, c.want.missing)
			assertDataMissing(t, fault, c.want.hasDataMissing, c.want.missing)
			assertMetric(t, rec, c.mode, c.want.outcome, c.want.metricFired)
		})
	}
}

func assertFault(t *testing.T, fault *engine.Fault, reject bool, code, status int) {
	t.Helper()
	if reject {
		if fault == nil {
			t.Fatalf("expected a rejecting fault, got nil")
		}
		if fault.Code != code {
			t.Errorf("fault code = %d, want %d", fault.Code, code)
		}
		if fault.HTTPStatus != status {
			t.Errorf("fault HTTP status = %d, want %d", fault.HTTPStatus, status)
		}
		return
	}
	if fault != nil {
		t.Fatalf("expected acceptance (nil fault), got %v", fault)
	}
}

func assertPart(t *testing.T, part *journalapi.MetaValidationPart, mode, outcome string, missing []string) {
	t.Helper()
	if part == nil {
		t.Fatalf("expected a journal metaValidation part, got nil")
	}
	if part.Mode != mode {
		t.Errorf("part.Mode = %q, want %q", part.Mode, mode)
	}
	if part.Outcome != outcome {
		t.Errorf("part.Outcome = %q, want %q", part.Outcome, outcome)
	}
	if !equalStrings(part.Missing, missing) {
		t.Errorf("part.Missing = %v, want %v", part.Missing, missing)
	}
}

// assertDataMissing checks the -32602 error.data carries the annex 2.12 [P-11]
// data.missing payload naming exactly the absent fields (MOCK-203.4).
func assertDataMissing(t *testing.T, fault *engine.Fault, expect bool, missing []string) {
	t.Helper()
	if !expect {
		if fault != nil && len(fault.Data) != 0 {
			t.Errorf("did not expect data payload, got %s", fault.Data)
		}
		return
	}
	var data wire.MetaMissingErrorData
	if err := json.Unmarshal(fault.Data, &data); err != nil {
		t.Fatalf("error.data is not a metaMissingErrorData payload: %v (%s)", err, fault.Data)
	}
	if !equalStrings(data.Missing, missing) {
		t.Errorf("data.missing = %v, want %v", data.Missing, missing)
	}
}

func assertMetric(t *testing.T, rec *fakeRecorder, mode, outcome string, fired bool) {
	t.Helper()
	if !fired {
		if len(rec.calls) != 0 {
			t.Errorf("expected no metric call, got %v", rec.calls)
		}
		return
	}
	if len(rec.calls) != 1 {
		t.Fatalf("expected exactly one metric call, got %v", rec.calls)
	}
	got := rec.calls[0]
	if got.mode != mode || got.outcome != outcome {
		t.Errorf("metric = {mode:%q outcome:%q}, want {mode:%q outcome:%q}",
			got.mode, got.outcome, mode, outcome)
	}
}

// TestLenientAndOffAreDistinguishable is the AMEND-6 acceptance criterion
// (MOCK-203.7 / 203.8) as its own focused test: given the SAME request missing a
// required field, lenient and off produce observably different journal parts and
// different metric outcomes. This is the property that makes both modes testable.
func TestLenientAndOffAreDistinguishable(t *testing.T) {
	t.Parallel()
	body := bodyWith(map[string]string{wire.MetaKeyClientCapabilities: `{}`}) // protocolVersion absent

	lenRec := &fakeRecorder{}
	_, lenPart := modern.NewMetaValidator(modeLenient, lenRec).
		ValidateMeta(context.Background(), exchangeFor(t, body))

	offRec := &fakeRecorder{}
	_, offPart := modern.NewMetaValidator(modeOff, offRec).
		ValidateMeta(context.Background(), exchangeFor(t, body))

	// Journal: lenient populates missing; off leaves it empty (the load-bearing
	// difference of MOCK-203.8).
	if len(lenPart.Missing) == 0 {
		t.Error("lenient must populate metaValidation.missing")
	}
	if len(offPart.Missing) != 0 {
		t.Errorf("off must leave metaValidation.missing empty, got %v", offPart.Missing)
	}
	if lenPart.Outcome == offPart.Outcome {
		t.Errorf("lenient and off must record different outcomes, both = %q", lenPart.Outcome)
	}

	// Metric: tolerated vs skipped, both under the correct mode label.
	if len(lenRec.calls) != 1 || lenRec.calls[0].outcome != "tolerated" {
		t.Errorf("lenient metric = %v, want one tolerated", lenRec.calls)
	}
	if len(offRec.calls) != 1 || offRec.calls[0].outcome != "skipped" {
		t.Errorf("off metric = %v, want one skipped", offRec.calls)
	}
}

// TestAbsentVsPresentEmpty proves the validator honors the absent-vs-present-
// empty distinction (the core MetaEnvelope property): a present-but-empty-string
// protocolVersion and a present-but-empty-object clientCapabilities are PRESENT
// (validation passes), whereas absent members fail. This is why "absent" and
// "explicitly empty" must not be conflated.
func TestAbsentVsPresentEmpty(t *testing.T) {
	t.Parallel()

	// Present but empty: protocolVersion is "", clientCapabilities is {}. Both
	// members are present, so strict must ACCEPT — empty is not absent.
	presentEmpty := bodyWith(map[string]string{
		wire.MetaKeyProtocolVersion:    `""`,
		wire.MetaKeyClientCapabilities: `{}`,
	})
	fault, part := modern.NewMetaValidator(modeStrict, nil).
		ValidateMeta(context.Background(), exchangeFor(t, presentEmpty))
	if fault != nil {
		t.Fatalf("present-but-empty required fields must be accepted, got fault %v", fault)
	}
	if part.Outcome != "ok" {
		t.Errorf("present-empty outcome = %q, want ok", part.Outcome)
	}

	// Absent: no protocolVersion member at all — strict must reject and name it.
	absent := bodyWith(map[string]string{wire.MetaKeyClientCapabilities: `{}`})
	fault2, _ := modern.NewMetaValidator(modeStrict, nil).
		ValidateMeta(context.Background(), exchangeFor(t, absent))
	if fault2 == nil {
		t.Fatal("absent protocolVersion must be rejected")
	}
}

// TestStructuralCases covers the AMEND-6 mode-table rows that are structural,
// not semantic: a present-but-non-object params is -32600 in EVERY mode
// (including off), and a present-but-non-object _meta is -32602 in strict and
// lenient but treated as absent in off. lenient does not relax anything
// syntactic.
func TestStructuralCases(t *testing.T) {
	t.Parallel()

	nonObjectParams := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":4}`
	nonObjectMeta := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"_meta":4}}`

	t.Run("params not an object is -32600 in every mode", func(t *testing.T) {
		t.Parallel()
		for _, mode := range []string{modeStrict, modeLenient, modeOff} {
			rec := &fakeRecorder{}
			fault, part := modern.NewMetaValidator(mode, rec).
				ValidateMeta(context.Background(), exchangeFor(t, nonObjectParams))
			if fault == nil {
				t.Fatalf("%s: non-object params must be rejected", mode)
			}
			if fault.Code != wire.ErrCodeInvalidRequest {
				t.Errorf("%s: code = %d, want -32600", mode, fault.Code)
			}
			if fault.HTTPStatus != http.StatusBadRequest {
				t.Errorf("%s: HTTP status = %d, want 400", mode, fault.HTTPStatus)
			}
			// A structural JSON-RPC error is not a MOCK-203 outcome: no
			// metaValidation part and no meta metric.
			if part != nil {
				t.Errorf("%s: expected no metaValidation part for a structural error, got %v", mode, part)
			}
			if len(rec.calls) != 0 {
				t.Errorf("%s: structural error must not fire the meta metric, got %v", mode, rec.calls)
			}
		}
	})

	t.Run("non-object _meta is -32602 in strict and lenient", func(t *testing.T) {
		t.Parallel()
		for _, mode := range []string{modeStrict, modeLenient} {
			rec := &fakeRecorder{}
			fault, part := modern.NewMetaValidator(mode, rec).
				ValidateMeta(context.Background(), exchangeFor(t, nonObjectMeta))
			if fault == nil {
				t.Fatalf("%s: non-object _meta must be rejected (syntactic, not relaxed)", mode)
			}
			if fault.Code != wire.ErrCodeInvalidParams {
				t.Errorf("%s: code = %d, want -32602", mode, fault.Code)
			}
			if fault.HTTPStatus != http.StatusBadRequest {
				t.Errorf("%s: HTTP status = %d, want 400", mode, fault.HTTPStatus)
			}
			if part == nil || part.Outcome != "rejected" {
				t.Errorf("%s: expected a rejected metaValidation part, got %v", mode, part)
			}
		}
	})

	t.Run("non-object _meta is treated as absent in off", func(t *testing.T) {
		t.Parallel()
		rec := &fakeRecorder{}
		fault, part := modern.NewMetaValidator(modeOff, rec).
			ValidateMeta(context.Background(), exchangeFor(t, nonObjectMeta))
		if fault != nil {
			t.Fatalf("off: non-object _meta must be accepted (treated as absent), got %v", fault)
		}
		if part.Outcome != "skipped" || len(part.Missing) != 0 {
			t.Errorf("off: expected skipped/empty, got %v", part)
		}
	})
}

// TestUnknownModeFoldsToStrict proves an unrecognized switches.validateMeta
// value is treated as strict — the conformant-server default — rather than
// silently permissive.
func TestUnknownModeFoldsToStrict(t *testing.T) {
	t.Parallel()
	body := bodyWith(map[string]string{wire.MetaKeyClientCapabilities: `{}`}) // protocolVersion absent
	rec := &fakeRecorder{}
	fault, part := modern.NewMetaValidator("bogus", rec).
		ValidateMeta(context.Background(), exchangeFor(t, body))
	if fault == nil || fault.Code != wire.ErrCodeInvalidParams {
		t.Fatalf("unknown mode must behave as strict (reject -32602), got %v", fault)
	}
	if part.Mode != wire.MetaModeStrict {
		t.Errorf("folded mode = %q, want strict", part.Mode)
	}
	if len(rec.calls) != 1 || rec.calls[0].mode != wire.MetaModeStrict {
		t.Errorf("metric mode = %v, want strict", rec.calls)
	}
}

// TestNilRecorderIsSafe proves a validator built with no metric recorder still
// validates and journals correctly — metrics are optional to correctness.
func TestNilRecorderIsSafe(t *testing.T) {
	t.Parallel()
	body := bodyWith(map[string]string{wire.MetaKeyClientCapabilities: `{}`})
	fault, part := modern.NewMetaValidator(modeStrict, nil).
		ValidateMeta(context.Background(), exchangeFor(t, body))
	if fault == nil {
		t.Fatal("nil recorder must not disable validation")
	}
	if part == nil || part.Outcome != "rejected" {
		t.Errorf("nil recorder must still journal the outcome, got %v", part)
	}
}

// TestUnknownMetaKeysDoNotAffectPresence proves the presence check keys on the
// required members only: an _meta carrying an unrecognised key plus both
// required fields still validates cleanly (the unknown key is preserved by the
// journal, MOCK-203.6, which is the engine's decode responsibility — here we
// only assert the unknown key does not perturb the missing-set computation).
func TestUnknownMetaKeysDoNotAffectPresence(t *testing.T) {
	t.Parallel()
	body := bodyWith(map[string]string{
		wire.MetaKeyProtocolVersion:    `"2026-07-28"`,
		wire.MetaKeyClientCapabilities: `{}`,
		"vendorExtension":              `{"x":1}`,
	})
	fault, part := modern.NewMetaValidator(modeStrict, nil).
		ValidateMeta(context.Background(), exchangeFor(t, body))
	if fault != nil {
		t.Fatalf("unknown _meta key must not cause rejection, got %v", fault)
	}
	if part.Outcome != "ok" {
		t.Errorf("outcome = %q, want ok", part.Outcome)
	}
}

// equalStrings compares two string slices for equality, treating nil and empty
// as equal (the validator returns nil for "nothing missing" and callers pass
// nil in that case).
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
