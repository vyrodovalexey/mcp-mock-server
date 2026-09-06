package engine_test

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/vyrodovalexey/mcp-mock-server/internal/engine"
	"github.com/vyrodovalexey/mcp-mock-server/internal/wire"
	"github.com/vyrodovalexey/mcp-mock-server/journalapi"
)

// newPipeline builds a pipeline whose registry serves the three Phase 1 method
// names from internal/wire (no literals, ADR-019). The dispatch handler is
// parameterised so a test can pick echo/random/sleep behaviour.
func newPipeline(h engine.Handler) *engine.Pipeline {
	reg := engine.NewRegistry()
	for _, m := range wire.Phase1Methods() {
		reg.Register(m, h)
	}
	return engine.NewPipeline(reg)
}

// runOnce drives one request through the pipeline and returns the buffered
// response bytes and the sink, failing the test on an infrastructure error.
func runOnce(t *testing.T, p *engine.Pipeline, ex *engine.Exchange) *engine.BufferedSink {
	t.Helper()
	sink := engine.NewBufferedSink()
	if err := p.Handle(ex.Ctx, ex, sink); err != nil {
		t.Fatalf("Handle returned infra error: %v", err)
	}
	return sink
}

// TestMonoNsWiredAndMonotonic is the REV-002 regression: every journal record
// must carry a non-zero monotonic timestamp (nanoseconds since instance start,
// MOCK-601.1), and the value must not go backwards across two requests. Before
// the fix buildInput never set MonoNs, so every record carried monoNs: 0 and
// MOCK-212's elapsed-time claims were untrustworthy across clock adjustments.
func TestMonoNsWiredAndMonotonic(t *testing.T) {
	inst := newInstance(1, "inst", true)
	p := newPipeline(echoHandler())

	_ = runOnce(t, p, buildExchange(
		context.Background(), inst, engine.KindHTTP, rawRequest("1", wire.MethodToolsCall)))
	// A brief pause so the second record's monotonic reading is strictly later,
	// which lets the test assert ordering rather than mere non-zero-ness.
	time.Sleep(time.Millisecond)
	_ = runOnce(t, p, buildExchange(
		context.Background(), inst, engine.KindHTTP, rawRequest("2", wire.MethodToolsCall)))

	recs := inst.ring.Snapshot()
	if len(recs) != 2 {
		t.Fatalf("want 2 journal records, got %d", len(recs))
	}
	if recs[0].MonoNs <= 0 {
		t.Fatalf("record 0 monoNs = %d, want > 0 (MonoNs never wired, REV-002)", recs[0].MonoNs)
	}
	if recs[1].MonoNs <= 0 {
		t.Fatalf("record 1 monoNs = %d, want > 0 (MonoNs never wired, REV-002)", recs[1].MonoNs)
	}
	if recs[1].MonoNs < recs[0].MonoNs {
		t.Errorf("monoNs went backwards across requests: rec0=%d rec1=%d", recs[0].MonoNs, recs[1].MonoNs)
	}
}

// TestAllNineStagesExecuteInOrder asserts every stage runs in the documented
// order for a normal request, including the pass-through stages (acceptance
// criterion 2).
func TestAllNineStagesExecuteInOrder(t *testing.T) {
	inst := newInstance(1, "inst", true)
	var seen []engine.StageID
	p := newPipeline(echoHandler()).WithObserverForTest(func(s engine.StageID) {
		seen = append(seen, s)
	})
	ex := buildExchange(context.Background(), inst, engine.KindHTTP, rawRequest("1", wire.MethodToolsCall))
	_ = runOnce(t, p, ex)

	want := []engine.StageID{
		engine.StageDecode, engine.StageEraResolve, engine.StageAuthorize,
		engine.StageValidate, engine.StageFaultPre, engine.StageDispatch,
		engine.StageFaultPost, engine.StageEncode, engine.StageJournal,
	}
	if len(seen) != len(want) {
		t.Fatalf("stage sequence = %v, want %v", seen, want)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("stage %d = %v, want %v (full: %v)", i, seen[i], want[i], seen)
		}
	}
}

// TestStageFailureMidPipelineHandledCorrectly asserts that a stage failing
// mid-pipeline (stage 4 validation reject, standing in for TASK-017) short-
// circuits to encode+journal, produces a well-formed error response and a
// complete record (MOCK-601.6), and does NOT run the dispatch stage.
func TestStageFailureMidPipelineHandledCorrectly(t *testing.T) {
	inst := newInstance(1, "inst", true)
	// A validator that rejects like strict mode would for a missing field.
	missing := mustJSON(t, wire.MetaMissingErrorData{Missing: wire.RequiredMetaFields()})
	snap := newSnapshot()
	snap.validator = engine.MetaValidatorFunc(
		func(_ context.Context, _ *engine.Exchange) (*engine.Fault, *journalapi.MetaValidationPart) {
			return engine.InvalidParamsFault("missing required _meta field", missing),
				&journalapi.MetaValidationPart{Mode: wire.MetaModeStrict, Outcome: "rejected"}
		})
	inst.setSnapshot(snap)

	var reached []engine.StageID
	p := newPipeline(echoHandler()).WithObserverForTest(func(s engine.StageID) {
		reached = append(reached, s)
	})
	ex := buildExchange(context.Background(), inst, engine.KindHTTP, rawRequest("7", wire.MethodToolsCall))
	sink := runOnce(t, p, ex)

	for _, s := range reached {
		if s == engine.StageDispatch {
			t.Fatalf("dispatch ran despite stage-4 rejection; stages: %v", reached)
		}
	}
	resp := decodeResponse(t, sink.Bytes())
	if resp.Error == nil || resp.Error.Code != wire.ErrCodeInvalidParams {
		t.Fatalf("want -32602 error response, got %+v", resp)
	}
	// The record must exist and carry the emitted error (MOCK-601.6).
	recs := inst.ring.Snapshot()
	if len(recs) != 1 {
		t.Fatalf("want 1 journal record for rejected request, got %d", len(recs))
	}
	if recs[0].Response == nil || recs[0].Response.Error == nil ||
		recs[0].Response.Error.Code != wire.ErrCodeInvalidParams {
		t.Fatalf("record missing emitted error: %+v", recs[0].Response)
	}
	if recs[0].MetaValidation == nil || recs[0].MetaValidation.Outcome != "rejected" {
		t.Fatalf("record missing metaValidation outcome: %+v", recs[0].MetaValidation)
	}
}

// TestMethodNotFoundForDisabledMethod asserts a disabled method returns -32601
// without reaching a handler (MOCK-202.2 mechanics), and the other methods are
// unaffected.
func TestMethodNotFoundForDisabledMethod(t *testing.T) {
	inst := newInstance(1, "inst", true)
	snap := newSnapshot()
	snap.disabled = map[string]bool{wire.MethodToolsCall: true}
	inst.setSnapshot(snap)

	p := newPipeline(echoHandler())
	sink := runOnce(t, p, buildExchange(
		context.Background(), inst, engine.KindHTTP, rawRequest("1", wire.MethodToolsCall)))
	resp := decodeResponse(t, sink.Bytes())
	if resp.Error == nil || resp.Error.Code != wire.ErrCodeMethodNotFound {
		t.Fatalf("disabled method: want -32601, got %+v", resp)
	}

	sink2 := runOnce(t, p, buildExchange(
		context.Background(), inst, engine.KindHTTP, rawRequest("2", wire.MethodToolsList)))
	if resp2 := decodeResponse(t, sink2.Bytes()); resp2.Error != nil {
		t.Fatalf("enabled method should succeed, got error %+v", resp2.Error)
	}
}

// TestParseErrorStillJournaled asserts a malformed request yields a -32700 and a
// complete journal record (MOCK-601.6), with no panic.
func TestParseErrorStillJournaled(t *testing.T) {
	inst := newInstance(1, "inst", true)
	p := newPipeline(echoHandler())
	sink := runOnce(t, p, buildExchange(
		context.Background(), inst, engine.KindHTTP, []byte(`{not json`)))
	resp := decodeResponse(t, sink.Bytes())
	if resp.Error == nil || resp.Error.Code != wire.ErrCodeParse {
		t.Fatalf("want -32700 parse error, got %+v", resp)
	}
	if len(inst.ring.Snapshot()) != 1 {
		t.Fatalf("parse error must still produce a journal record")
	}
}

// TestEmptyEraIsInternalFault covers the stage-2 misconfiguration guard: a
// snapshot with no resolved era is rejected with -32603 rather than journalling
// an empty era, and dispatch never runs.
func TestEmptyEraIsInternalFault(t *testing.T) {
	inst := newInstance(1, "inst", true)
	snap := newSnapshot()
	snap.era = "" // misconfigured
	inst.setSnapshot(snap)

	var reached []engine.StageID
	p := newPipeline(echoHandler()).WithObserverForTest(func(s engine.StageID) {
		reached = append(reached, s)
	})
	sink := runOnce(t, p, buildExchange(
		context.Background(), inst, engine.KindHTTP, rawRequest("1", wire.MethodToolsCall)))
	resp := decodeResponse(t, sink.Bytes())
	if resp.Error == nil || resp.Error.Code != wire.ErrCodeInternal {
		t.Fatalf("empty era: want -32603, got %+v", resp)
	}
	for _, s := range reached {
		if s == engine.StageDispatch {
			t.Fatalf("dispatch ran despite era misconfiguration: %v", reached)
		}
	}
}

// TestCancellationStopsWorkAndJournalsElapsed is the MOCK-212 test: closing the
// stream (cancelling the context) while a handler holds the request stops work,
// closes the sink as ClientGone, and journals the cancellation with a non-zero
// elapsed time.
func TestCancellationStopsWorkAndJournalsElapsed(t *testing.T) {
	inst := newInstance(1, "inst", true)
	p := newPipeline(sleepHandler(5 * time.Second))
	ctx, cancel := context.WithCancel(context.Background())
	ex := buildExchange(ctx, inst, engine.KindHTTP, rawRequest("1", wire.MethodToolsCall))
	sink := engine.NewBufferedSink()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = p.Handle(ctx, ex, sink)
	}()
	// Let the handler enter its select, then close the stream.
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("pipeline did not stop after cancellation")
	}

	if sink.CloseReason() != engine.CloseClientGone {
		t.Fatalf("sink close reason = %v, want CloseClientGone", sink.CloseReason())
	}
	if len(sink.Bytes()) != 0 {
		t.Fatalf("no response body should be emitted to a gone client, got %q", sink.Bytes())
	}
	recs := inst.ring.Snapshot()
	if len(recs) != 1 {
		t.Fatalf("cancellation must journal exactly one record, got %d", len(recs))
	}
	if recs[0].DurationNs <= 0 {
		t.Fatalf("cancellation record must carry elapsed time, got DurationNs=%d", recs[0].DurationNs)
	}
	if recs[0].Response == nil || recs[0].Response.CloseReason != engine.CloseClientGone.String() {
		t.Fatalf("record must note clientGone close reason: %+v", recs[0].Response)
	}
}

// TestByteIdenticalOutputForFixedSeed proves §0.1: same config + same seed ⇒
// byte-identical responses, across repeated runs, including a handler that draws
// from the seeded RNG.
func TestByteIdenticalOutputForFixedSeed(t *testing.T) {
	raw := rawRequest("42", wire.MethodToolsCall)
	first := runSeeded(t, raw)
	for i := 0; i < 50; i++ {
		got := runSeeded(t, raw)
		if !bytes.Equal(first, got) {
			t.Fatalf("run %d differs:\n first=%s\n got  =%s", i, first, got)
		}
	}
}

// runSeeded builds a fresh instance at a fixed seed and runs one random-handler
// request, returning the response bytes. Fresh instance each call proves the
// output depends only on seed+content, not on process state.
func runSeeded(t *testing.T, raw []byte) []byte {
	t.Helper()
	inst := newInstance(0xABCDEF, "orders", true)
	p := newPipeline(randomHandler())
	sink := runOnce(t, p, buildExchange(context.Background(), inst, engine.KindHTTP, raw))
	out := make([]byte, len(sink.Bytes()))
	copy(out, sink.Bytes())
	return out
}

// TestDifferentSeedDiffersOutput guards the flip side: a different seed produces
// different bytes for the same request, so the byte-identity above is a real
// determinism property and not a constant.
func TestDifferentSeedDiffersOutput(t *testing.T) {
	raw := rawRequest("42", wire.MethodToolsCall)
	a := runOnceSeed(t, raw, 1)
	b := runOnceSeed(t, raw, 2)
	if bytes.Equal(a, b) {
		t.Fatalf("different seeds produced identical bytes: %s", a)
	}
}

func runOnceSeed(t *testing.T, raw []byte, seed uint64) []byte {
	t.Helper()
	inst := newInstance(seed, "orders", true)
	p := newPipeline(randomHandler())
	sink := runOnce(t, p, buildExchange(context.Background(), inst, engine.KindHTTP, raw))
	return append([]byte(nil), sink.Bytes()...)
}

// TestConcurrentRequestsDoNotInterfere runs many requests concurrently on one
// pipeline+instance under -race, asserting each response is well-formed and the
// seeded output for a fixed request is stable regardless of interleaving
// (ADR-002 order-independence).
func TestConcurrentRequestsDoNotInterfere(t *testing.T) {
	inst := newInstance(99, "inst", true)
	p := newPipeline(randomHandler())
	fixed := rawRequest("fixed", wire.MethodToolsCall)
	want := runOnceInst(t, p, inst, fixed)

	const workers = 64
	var wg sync.WaitGroup
	errs := make(chan string, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			// Half the goroutines send the fixed request (byte-identity check),
			// half send unique requests (no cross-talk).
			if n%2 == 0 {
				if got := runOnceInst(t, p, inst, fixed); !bytes.Equal(got, want) {
					errs <- "fixed request produced varying bytes under concurrency"
				}
				return
			}
			uniq := rawRequest(itoa(n), wire.MethodToolsCall)
			sink := engine.NewBufferedSink()
			if err := p.Handle(context.Background(), buildExchange(
				context.Background(), inst, engine.KindHTTP, uniq), sink); err != nil {
				errs <- "handle error: " + err.Error()
				return
			}
			if r := decodeResponseNoFatal(sink.Bytes()); r == nil || r.Error != nil {
				errs <- "unique request produced error"
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatal(e)
	}
}

func runOnceInst(t *testing.T, p *engine.Pipeline, inst *fakeInstance, raw []byte) []byte {
	t.Helper()
	sink := runOnce(t, p, buildExchange(context.Background(), inst, engine.KindHTTP, raw))
	return append([]byte(nil), sink.Bytes()...)
}

// TestTransportNeutralIdenticalBytes asserts the same handler serves an Exchange
// from stdio and from HTTP and produces identical response bytes (acceptance
// criterion 1, feeds MOCK-102.3/.4).
func TestTransportNeutralIdenticalBytes(t *testing.T) {
	raw := rawRequest("1", wire.MethodToolsList)
	instHTTP := newInstance(7, "inst", true)
	instStdio := newInstance(7, "inst", true)
	p := newPipeline(randomHandler())

	httpSink := runOnce(t, p, buildExchange(context.Background(), instHTTP, engine.KindHTTP, raw))
	stdioSink := runOnce(t, p, buildExchange(context.Background(), instStdio, engine.KindStdio, raw))

	if !bytes.Equal(httpSink.Bytes(), stdioSink.Bytes()) {
		t.Fatalf("transport-dependent bytes:\n http =%s\n stdio=%s",
			httpSink.Bytes(), stdioSink.Bytes())
	}
}

// TestMetricsBalancedAndRecorded asserts the pipeline records a request and that
// in-flight is decremented for every increment (no leak), through the Metrics
// interface.
func TestMetricsBalancedAndRecorded(t *testing.T) {
	inst := newInstance(1, "inst", true)
	m := &countingMetrics{}
	inst.metrics = m
	p := newPipeline(echoHandler())
	for i := 0; i < 5; i++ {
		_ = runOnce(t, p, buildExchange(
			context.Background(), inst, engine.KindHTTP, rawRequest(itoa(i), wire.MethodToolsCall)))
	}
	if got := m.requests.Load(); got != 5 {
		t.Fatalf("recorded %d requests, want 5", got)
	}
	if got := m.inFlight.Load(); got != 0 {
		t.Fatalf("in-flight gauge unbalanced: %d", got)
	}
	if got := m.lastGen.Load(); got != 1 {
		t.Fatalf("snapshot generation not published, got %d", got)
	}
}

// mustJSON marshals v or fails the test.
func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// wireResponse is the minimal decoded response envelope the tests inspect.
type wireResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *wireErr        `json:"error"`
}

type wireErr struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func decodeResponse(t *testing.T, b []byte) wireResponse {
	t.Helper()
	var r wireResponse
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatalf("decode response %q: %v", b, err)
	}
	if r.JSONRPC != wire.JSONRPCVersion {
		t.Fatalf("response jsonrpc = %q, want %q", r.JSONRPC, wire.JSONRPCVersion)
	}
	return r
}

func decodeResponseNoFatal(b []byte) *wireResponse {
	var r wireResponse
	if json.Unmarshal(b, &r) != nil {
		return nil
	}
	return &r
}

// itoa is a tiny int-to-string without importing strconv into the test's hot
// concurrent path formatting (keeps allocations obvious).
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
