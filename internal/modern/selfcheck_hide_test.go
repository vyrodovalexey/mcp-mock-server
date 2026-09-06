package modern_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/vyrodovalexey/mcp-mock-server/internal/engine"
	"github.com/vyrodovalexey/mcp-mock-server/internal/jsonrpc"
	"github.com/vyrodovalexey/mcp-mock-server/internal/modern"
	"github.com/vyrodovalexey/mcp-mock-server/internal/wire"
	"github.com/vyrodovalexey/mcp-mock-server/scenario"
)

// selfcheck_hide_test.go covers the two Phase-1 switches DEF-010 and DEF-011
// wired end to end through the real modern handlers:
//
//   - switches.selfCheck (MOCK-201.4): every outgoing result is validated against
//     the AMEND-4 wire subset; a malformed one is rejected with -32603 and an
//     ERROR log naming the schema path — never silently emitted. The check costs
//     effectively nothing when off.
//   - switches.methods.<name>.hideFromCapabilities (MOCK-202.3): a hidden method's
//     capabilities key is removed from server/discover, independently of whether
//     the method is enabled.

// runPipelineWithLogger drives one request through a fresh pipeline whose handlers
// are registered with a self-check logger, so a test can both observe the wire
// response and inspect the ERROR log a self-check failure emits.
func runPipelineWithLogger(
	t *testing.T, snap engine.Snapshot, log modern.SelfCheckLogger, method, body string,
) []byte {
	t.Helper()
	reg := engine.NewRegistry()
	modern.RegisterHandlersWithLogger(reg, modern.BuiltinConfig{}, log)
	p := engine.NewPipeline(reg)

	inst := newPipelineInstance(snap)
	ex := &engine.Exchange{
		Ctx:       context.Background(),
		Instance:  inst,
		Transport: engine.KindHTTP,
		Raw:       []byte(body),
	}
	sink := engine.NewBufferedSink()
	if err := p.Handle(ex.Ctx, ex, sink); err != nil {
		t.Fatalf("pipeline Handle infra error: %v", err)
	}
	return append([]byte(nil), sink.Bytes()...)
}

// --- DEF-010: switches.selfCheck ------------------------------------------

// TestSelfCheckOffEmitsResult proves the default (self-check off) path: a normal
// result is emitted unchanged, no -32603.
func TestSelfCheckOffEmitsResult(t *testing.T) {
	snap := &handlerSnapshot{era: "modern"} // selfCheckOn defaults false
	body := runPipeline(t, snap, modern.BuiltinConfig{}, wire.MethodDiscover, discoverBody("1"))
	if e := envelopeError(t, body); e != nil {
		t.Fatalf("self-check off must not error a well-formed result: %+v (%s)", e, body)
	}
}

// TestSelfCheckOnPassesWellFormed proves self-check ON leaves a conformant result
// untouched: the handler emits the same body it would with the check off.
func TestSelfCheckOnPassesWellFormed(t *testing.T) {
	off := &handlerSnapshot{era: "modern", discover: fullDiscover()}
	on := &handlerSnapshot{era: "modern", discover: fullDiscover(), selfCheckOn: true}
	for _, method := range []struct{ m, b string }{
		{wire.MethodDiscover, discoverBody("1")},
		{wire.MethodToolsList, toolsListBody("1")},
		{wire.MethodToolsCall, toolsCallBody("1", modern.ToolEcho, `{"a":1}`)},
	} {
		t.Run(method.m, func(t *testing.T) {
			offBody := runPipeline(t, off, modern.BuiltinConfig{}, method.m, method.b)
			onBody := runPipeline(t, on, modern.BuiltinConfig{}, method.m, method.b)
			if string(offBody) != string(onBody) {
				t.Errorf("self-check altered a well-formed result:\n off=%s\n on =%s", offBody, onBody)
			}
			if e := envelopeError(t, onBody); e != nil {
				t.Errorf("self-check rejected a well-formed %s: %+v", method.m, e)
			}
		})
	}
}

// TestSelfCheckCatchesMalformedResult is the core DEF-010 proof: a scenario that
// turns on selfCheck AND omitResultType produces a result with no resultType
// discriminator — a genuine violation of the phase1Result subset — and the
// self-check REJECTS it with -32603 rather than emitting it, and logs an ERROR
// naming the schema path. This is a deliberately malformed result ACTUALLY CAUGHT
// (not merely logged): the invalid body never reaches the client.
func TestSelfCheckCatchesMalformedResult(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelError}))

	// omitResultType removes the resultType key; selfCheck then finds the result
	// has no discriminator and rejects it.
	snap := &handlerSnapshot{era: "modern", omitRT: true, selfCheckOn: true}
	body := runPipelineWithLogger(t, snap, log, wire.MethodDiscover, discoverBody("1"))

	// The client sees a -32603, NOT the malformed result.
	e := envelopeError(t, body)
	if e == nil {
		t.Fatalf("self-check did not reject a discriminator-less result: %s", body)
	}
	if e.Code != wire.ErrCodeInternal {
		t.Errorf("self-check failure code = %d, want %d (-32603)", e.Code, wire.ErrCodeInternal)
	}
	// The invalid body must NOT have been emitted as a result.
	if resultOf(t, body) != nil {
		t.Errorf("malformed result leaked to the client alongside the error: %s", body)
	}

	// The ERROR log names the schema path (MOCK-201.4) and marks it provisional.
	logLine := buf.String()
	if !strings.Contains(logLine, "wire-2026-07-28.schema.json") ||
		!strings.Contains(logLine, "phase1Result") {
		t.Errorf("ERROR log does not name the schema path: %s", logLine)
	}
	if !strings.Contains(logLine, "PROVISIONAL") || !strings.Contains(logLine, "GAP-003") {
		t.Errorf("ERROR log does not mark the oracle provisional (GAP-003): %s", logLine)
	}
	if !strings.Contains(logLine, `"level":"ERROR"`) {
		t.Errorf("self-check failure was not logged at ERROR level: %s", logLine)
	}
}

// TestSelfCheckNilLoggerStillRejects proves the rejection does not depend on a
// logger: with a nil logger the malformed result is still turned into -32603 (the
// log line is merely absent). A self-check that only rejects when a logger is
// present would be a silent failure in a logger-less embedding.
func TestSelfCheckNilLoggerStillRejects(t *testing.T) {
	snap := &handlerSnapshot{era: "modern", omitRT: true, selfCheckOn: true}
	body := runPipelineWithLogger(t, snap, nil, wire.MethodDiscover, discoverBody("1"))
	e := envelopeError(t, body)
	if e == nil || e.Code != wire.ErrCodeInternal {
		t.Fatalf("self-check with nil logger must still reject with -32603: %s", body)
	}
}

// resultOf returns the result member of a response envelope, or nil when the
// envelope carries an error instead. It is how a test asserts a malformed result
// did NOT leak alongside the error.
func resultOf(t *testing.T, body []byte) json.RawMessage {
	t.Helper()
	var env struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode envelope: %v (%s)", err, body)
	}
	return env.Result
}

// --- DEF-011: hideFromCapabilities ----------------------------------------

// capsDiscover is a discover config whose capabilities object advertises one key
// per Phase 1 method (named after the method), so a test can watch a hidden
// method's key disappear.
func capsDiscover() *scenario.Discover {
	caps := `{"` + wire.MethodDiscover + `":{"v":1},"` +
		wire.MethodToolsList + `":{"listChanged":true},"` +
		wire.MethodToolsCall + `":{"v":2},"extensions":{"x":1}}`
	return &scenario.Discover{Capabilities: json.RawMessage(caps)}
}

// discoverCapabilities runs server/discover and returns the raw capabilities
// member of the result, or nil when absent.
func discoverCapabilities(t *testing.T, snap engine.Snapshot) json.RawMessage {
	t.Helper()
	body := runPipeline(t, snap, modern.BuiltinConfig{}, wire.MethodDiscover, discoverBody("1"))
	var env struct {
		Result struct {
			Capabilities json.RawMessage `json:"capabilities"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode discover: %v (%s)", err, body)
	}
	return env.Result.Capabilities
}

// TestHideFromCapabilitiesRemovesKey proves MOCK-202.3: with tools/list hidden,
// its capabilities key is absent, while the other advertised keys remain — and
// with nothing hidden, every key is present (verbatim).
func TestHideFromCapabilitiesRemovesKey(t *testing.T) {
	// Nothing hidden: capabilities emitted verbatim, all keys present.
	none := &handlerSnapshot{era: "modern", discover: capsDiscover()}
	full := discoverCapabilities(t, none)
	for _, m := range wire.Phase1Methods() {
		if !capsHasKey(t, full, m) {
			t.Fatalf("unhidden capabilities missing %q: %s", m, full)
		}
	}

	// Hide tools/list: its key vanishes, the others survive.
	hide := &handlerSnapshot{era: "modern", discover: capsDiscover(), hidden: []string{wire.MethodToolsList}}
	filtered := discoverCapabilities(t, hide)
	if capsHasKey(t, filtered, wire.MethodToolsList) {
		t.Errorf("hidden method %q still advertised: %s", wire.MethodToolsList, filtered)
	}
	for _, m := range []string{wire.MethodDiscover, wire.MethodToolsCall} {
		if !capsHasKey(t, filtered, m) {
			t.Errorf("non-hidden method %q wrongly removed: %s", m, filtered)
		}
	}
	if !capsHasKey(t, filtered, "extensions") {
		t.Errorf("unrelated capability %q wrongly removed: %s", "extensions", filtered)
	}
}

// TestHideFromCapabilitiesVerbatimWhenNoneHidden proves the fast path is truly
// verbatim: with no method hidden, the capabilities bytes are IDENTICAL to the
// authored bytes (no re-encoding, no key reordering, MOCK-201.2).
func TestHideFromCapabilitiesVerbatimWhenNoneHidden(t *testing.T) {
	authored := capsDiscover().Capabilities
	none := &handlerSnapshot{era: "modern", discover: capsDiscover()}
	got := discoverCapabilities(t, none)
	if string(got) != string(authored) {
		t.Errorf("capabilities not verbatim with nothing hidden:\n authored=%s\n got     =%s", authored, got)
	}
}

// TestHideFromCapabilitiesPreservesOrder proves surviving keys keep their
// authored order (§0.1 determinism): hiding the FIRST advertised key leaves the
// remaining keys in their original relative order.
func TestHideFromCapabilitiesPreservesOrder(t *testing.T) {
	hide := &handlerSnapshot{era: "modern", discover: capsDiscover(), hidden: []string{wire.MethodDiscover}}
	got := string(discoverCapabilities(t, hide))
	// After removing server/discover, tools/list must precede tools/call must
	// precede extensions, matching the authored order.
	iList := strings.Index(got, wire.MethodToolsList)
	iCall := strings.Index(got, wire.MethodToolsCall)
	iExt := strings.Index(got, "extensions")
	if iList < 0 || iCall < 0 || iExt < 0 || !(iList < iCall && iCall < iExt) {
		t.Errorf("surviving capability order not preserved: %s", got)
	}
}

// TestHideAllCapabilitiesYieldsEmptyObject proves hiding every advertised method
// (and the unrelated extensions key too) yields an empty object {} — a scenario
// that advertises nothing, which is a valid, testable state.
func TestHideAllCapabilitiesYieldsEmptyObject(t *testing.T) {
	all := append(wire.Phase1Methods(), "extensions")
	hide := &handlerSnapshot{era: "modern", discover: capsDiscover(), hidden: all}
	got := string(discoverCapabilities(t, hide))
	if got != "{}" {
		t.Errorf("hiding every capability must yield {}, got %s", got)
	}
}

// TestHideFromCapabilitiesNonObjectSurvives proves a non-object capabilities
// value (authored as an array) is left verbatim even with a hidden method: there
// is no method key to remove from a non-object, and MOCK-201.2 requires it
// survive unaltered.
func TestHideFromCapabilitiesNonObjectSurvives(t *testing.T) {
	disc := &scenario.Discover{Capabilities: json.RawMessage(`["opaque",1]`)}
	hide := &handlerSnapshot{era: "modern", discover: disc, hidden: []string{wire.MethodToolsList}}
	got := string(discoverCapabilities(t, hide))
	if got != `["opaque",1]` {
		t.Errorf("non-object capabilities not verbatim under hide: %s", got)
	}
}

// TestHideIndependentOfDisableEndToEnd is the MOCK-202.3 independence proof at the
// wire level: a method may be hidden from capabilities yet still ENABLED (it
// answers normally), and advertised yet DISABLED (-32601). All four combinations
// are exercised.
func TestHideIndependentOfDisableEndToEnd(t *testing.T) {
	cases := []struct {
		name       string
		disabled   map[string]bool
		hidden     []string
		wantHidden bool // tools/list key absent from capabilities
		wantCode   int  // tools/list response code (0 = success)
	}{
		{"advertised-enabled", nil, nil, false, 0},
		{"advertised-disabled", map[string]bool{wire.MethodToolsList: true}, nil, false, wire.ErrCodeMethodNotFound},
		{"hidden-enabled", nil, []string{wire.MethodToolsList}, true, 0},
		{"hidden-disabled", map[string]bool{wire.MethodToolsList: true}, []string{wire.MethodToolsList}, true, wire.ErrCodeMethodNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			snap := &handlerSnapshot{
				era:      "modern",
				discover: capsDiscover(),
				disabled: tc.disabled,
				hidden:   tc.hidden,
			}
			// Advertisement: is tools/list in the discover capabilities?
			caps := discoverCapabilities(t, snap)
			if got := capsHasKey(t, caps, wire.MethodToolsList); got == tc.wantHidden {
				t.Errorf("tools/list advertised=%v, want advertised=%v: %s", got, !tc.wantHidden, caps)
			}
			// Availability: does tools/list answer or -32601?
			listBody := runPipeline(t, snap, modern.BuiltinConfig{}, wire.MethodToolsList, toolsListBody("1"))
			e := envelopeError(t, listBody)
			if tc.wantCode == 0 {
				if e != nil {
					t.Errorf("tools/list unexpectedly errored: %+v", e)
				}
			} else {
				if e == nil || e.Code != tc.wantCode {
					t.Errorf("tools/list code = %v, want %d", e, tc.wantCode)
				}
			}
		})
	}
}

// TestSwitchCombinationsByteDeterministic proves §0.1: for a fixed seed the wire
// output is byte-identical across independent renders in every combination of the
// two switches (and of hide vs. disable), so neither switch introduces
// nondeterminism.
func TestSwitchCombinationsByteDeterministic(t *testing.T) {
	combos := []struct {
		name string
		snap func() *handlerSnapshot
	}{
		{"none", func() *handlerSnapshot { return &handlerSnapshot{era: "modern", discover: capsDiscover()} }},
		{"selfcheck", func() *handlerSnapshot {
			return &handlerSnapshot{era: "modern", discover: capsDiscover(), selfCheckOn: true}
		}},
		{"hide-one", func() *handlerSnapshot {
			return &handlerSnapshot{era: "modern", discover: capsDiscover(), hidden: []string{wire.MethodToolsList}}
		}},
		{"hide-and-selfcheck", func() *handlerSnapshot {
			return &handlerSnapshot{era: "modern", discover: capsDiscover(), hidden: []string{wire.MethodDiscover}, selfCheckOn: true}
		}},
	}
	for _, c := range combos {
		t.Run(c.name, func(t *testing.T) {
			a := runPipeline(t, c.snap(), modern.BuiltinConfig{}, wire.MethodDiscover, discoverBody("9"))
			for i := 0; i < 5; i++ {
				b := runPipeline(t, c.snap(), modern.BuiltinConfig{}, wire.MethodDiscover, discoverBody("9"))
				if string(a) != string(b) {
					t.Fatalf("%s not byte-deterministic on run %d:\n a=%s\n b=%s", c.name, i, a, b)
				}
			}
		})
	}
}

// capsHasKey reports whether the raw capabilities object has a top-level member
// named key. It fails the test if capabilities is not a JSON object.
func capsHasKey(t *testing.T, caps json.RawMessage, key string) bool {
	t.Helper()
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(caps, &obj); err != nil {
		t.Fatalf("capabilities not an object: %v (%s)", err, caps)
	}
	_, ok := obj[key]
	return ok
}

// BenchmarkSelfCheckDisabled measures the disabled-path cost: with switches.
// selfCheck off, the check must add effectively nothing over emitting the result.
// It reports allocations so the "off costs nothing" claim is evidence-backed.
func BenchmarkSelfCheckDisabled(b *testing.B) {
	snap := &handlerSnapshot{era: "modern", discover: fullDiscover()}
	reg := engine.NewRegistry()
	modern.RegisterHandlers(reg, modern.BuiltinConfig{})
	h, _ := reg.Lookup(wire.MethodDiscover)
	req := decodeReqB(b, discoverBody("1"))
	ex := &engine.Exchange{Ctx: context.Background(), Snapshot: snap, Request: req, Raw: []byte(discoverBody("1"))}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, f := h.Handle(context.Background(), ex); f != nil {
			b.Fatalf("unexpected fault: %v", f)
		}
	}
}

// decodeReqB decodes a JSON-RPC request body for a benchmark, failing the bench
// on a decode error.
func decodeReqB(b *testing.B, body string) *jsonrpc.Request {
	b.Helper()
	req, err := jsonrpc.DecodeRequest([]byte(body))
	if err != nil {
		b.Fatalf("decode request %q: %v", body, err)
	}
	return req
}

// BenchmarkSelfCheckEnabled measures the enabled-path cost for contrast, so the
// disabled/enabled gap is visible in the report.
func BenchmarkSelfCheckEnabled(b *testing.B) {
	snap := &handlerSnapshot{era: "modern", discover: fullDiscover(), selfCheckOn: true}
	reg := engine.NewRegistry()
	modern.RegisterHandlers(reg, modern.BuiltinConfig{})
	h, _ := reg.Lookup(wire.MethodDiscover)
	req := decodeReqB(b, discoverBody("1"))
	ex := &engine.Exchange{Ctx: context.Background(), Snapshot: snap, Request: req, Raw: []byte(discoverBody("1"))}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, f := h.Handle(context.Background(), ex); f != nil {
			b.Fatalf("unexpected fault: %v", f)
		}
	}
}
