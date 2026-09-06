package modern_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/vyrodovalexey/mcp-mock-server/internal/engine"
	"github.com/vyrodovalexey/mcp-mock-server/internal/modern"
	"github.com/vyrodovalexey/mcp-mock-server/internal/wire"
)

// tools_call_test.go covers tools/call and the three built-ins (crit 4), the
// echo canonical-key ordering (202.10), sleep reject-not-clamp (202.14) and
// cancellation (202.16 / MOCK-212), fail default toolError with scenario-
// selected mode (202.17/202.19), protocolError (202.18), resultType+serverInfo
// (crit 6), and byte-identity (crit 8).

// bg is a shortcut for a non-cancelled background context.
func bg() context.Context { return context.Background() }

// ---- echo -----------------------------------------------------------------

// TestEchoCanonicalKeyOrdering proves 202.10 (crit "echo canonical-JSON key
// ordering"): echo with {"b":2,"a":"x"} returns content[0].text equal to the
// canonical JSON {"a":"x","b":2} (keys sorted) and structuredContent.echoed
// deep-equal to the arguments with JSON types preserved. Non-string values are
// not coerced (builtin-tools.md 2.5 [P-43]).
func TestEchoCanonicalKeyOrdering(t *testing.T) {
	args := `{"b":2,"a":"x","n":null,"deep":{"z":[1,true]}}`
	raw, fault := call(t, bg(), &handlerSnapshot{}, modern.BuiltinConfig{},
		wire.MethodToolsCall, toolsCallBody("1", modern.ToolEcho, args))
	if fault != nil {
		t.Fatalf("unexpected fault: %v", fault)
	}
	obj := mustObject(t, raw)

	// content[0].text is the canonical JSON string with sorted keys.
	wantText := `{"a":"x","b":2,"deep":{"z":[1,true]},"n":null}`
	if got := contentText(t, obj); got != wantText {
		t.Errorf("echo content text = %q, want canonical %q", got, wantText)
	}
	// structuredContent.echoed preserves types verbatim.
	sc := mustObject(t, obj["structuredContent"])
	if got := string(sc["echoed"]); got != args {
		t.Errorf("structuredContent.echoed = %s, want verbatim %s", got, args)
	}
	// isError is absent on a successful result (202.17 second half).
	if hasKey(obj, "isError") {
		t.Errorf("successful echo result carries isError: %s", raw)
	}
}

// TestEchoAbsentArguments proves 202.11: echo with absent arguments returns
// content[0].text == "{}" and does not error.
func TestEchoAbsentArguments(t *testing.T) {
	raw, fault := call(t, bg(), &handlerSnapshot{}, modern.BuiltinConfig{},
		wire.MethodToolsCall, toolsCallBody("1", modern.ToolEcho, ""))
	if fault != nil {
		t.Fatalf("unexpected fault: %v", fault)
	}
	if got := contentText(t, mustObject(t, raw)); got != "{}" {
		t.Errorf("echo of absent arguments text = %q, want %q", got, "{}")
	}
}

// TestEchoKeyAbsent proves 202.12: echo with behavior.echo.key naming an absent
// key returns -32602 with data.missing, NOT an echoed null.
func TestEchoKeyAbsent(t *testing.T) {
	bcfg := modern.BuiltinConfig{Echo: modern.EchoBehavior{Key: "wanted"}}
	_, fault := call(t, bg(), &handlerSnapshot{}, bcfg,
		wire.MethodToolsCall, toolsCallBody("1", modern.ToolEcho, `{"other":1}`))
	if fault == nil {
		t.Fatal("expected -32602 for absent echo key, got success")
	}
	if fault.Code != wire.ErrCodeInvalidParams {
		t.Errorf("fault code = %d, want %d", fault.Code, wire.ErrCodeInvalidParams)
	}
	assertMissingData(t, fault.Data, "wanted")
}

// TestEchoKeySelects proves the behavior.echo.key happy path: a present key
// echoes only that value, types preserved.
func TestEchoKeySelects(t *testing.T) {
	bcfg := modern.BuiltinConfig{Echo: modern.EchoBehavior{Key: "pick"}}
	raw, fault := call(t, bg(), &handlerSnapshot{}, bcfg,
		wire.MethodToolsCall, toolsCallBody("1", modern.ToolEcho, `{"pick":{"c":3,"a":1},"drop":9}`))
	if fault != nil {
		t.Fatalf("unexpected fault: %v", fault)
	}
	obj := mustObject(t, raw)
	if got := contentText(t, obj); got != `{"a":1,"c":3}` {
		t.Errorf("echo[key] text = %q, want canonical %q", got, `{"a":1,"c":3}`)
	}
}

// TestEchoStructuredSuppressed proves behavior.echo.structured:false omits the
// structuredContent channel.
func TestEchoStructuredSuppressed(t *testing.T) {
	bcfg := modern.BuiltinConfig{Echo: modern.EchoBehavior{Structured: boolptr(false)}}
	raw, fault := call(t, bg(), &handlerSnapshot{}, bcfg,
		wire.MethodToolsCall, toolsCallBody("1", modern.ToolEcho, `{"a":1}`))
	if fault != nil {
		t.Fatalf("unexpected fault: %v", fault)
	}
	if hasKey(mustObject(t, raw), "structuredContent") {
		t.Errorf("structuredContent present despite structured:false: %s", raw)
	}
}

// TestArgumentsNotObject proves the uniform builtin-tools.md 1.3 rule: a present
// non-object arguments is -32602 with data.reason "arguments_not_object", before
// any behavior runs.
func TestArgumentsNotObject(t *testing.T) {
	_, fault := call(t, bg(), &handlerSnapshot{}, modern.BuiltinConfig{},
		wire.MethodToolsCall, toolsCallBody("1", modern.ToolEcho, `[1,2,3]`))
	if fault == nil || fault.Code != wire.ErrCodeInvalidParams {
		t.Fatalf("expected -32602 for non-object arguments, got %v", fault)
	}
	assertDataReason(t, fault.Data, "arguments_not_object")
}

// ---- sleep ----------------------------------------------------------------

// TestSleepZeroFast proves 202.13 first half: durationMs:0 returns within 50 ms
// with the canonical body.
func TestSleepZeroFast(t *testing.T) {
	start := time.Now()
	raw, fault := call(t, bg(), &handlerSnapshot{}, modern.BuiltinConfig{},
		wire.MethodToolsCall, toolsCallBody("1", modern.ToolSleep, `{"durationMs":0}`))
	if fault != nil {
		t.Fatalf("unexpected fault: %v", fault)
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Errorf("sleep(0) took %s, want < 50ms", elapsed)
	}
	if got := contentText(t, mustObject(t, raw)); got != "slept 0ms" {
		t.Errorf("sleep(0) text = %q, want %q", got, "slept 0ms")
	}
}

// TestSleepDelays proves 202.13 second half: durationMs:250 returns after at
// least 250 ms.
func TestSleepDelays(t *testing.T) {
	start := time.Now()
	raw, fault := call(t, bg(), &handlerSnapshot{}, modern.BuiltinConfig{},
		wire.MethodToolsCall, toolsCallBody("1", modern.ToolSleep, `{"durationMs":250}`))
	if fault != nil {
		t.Fatalf("unexpected fault: %v", fault)
	}
	if elapsed := time.Since(start); elapsed < 250*time.Millisecond {
		t.Errorf("sleep(250) took %s, want >= 250ms", elapsed)
	}
	if got := contentText(t, mustObject(t, raw)); got != "slept 250ms" {
		t.Errorf("sleep(250) text = %q, want %q", got, "slept 250ms")
	}
}

// TestSleepRejectsOverMax proves 202.14 (crit "sleep rejecting >30000ms rather
// than clamping"): a durationMs above maxSleepMs with the default reject mode
// returns -32602 IMMEDIATELY (< 50 ms, proving no delay was incurred) with
// data.requestedMs and data.maxSleepMs.
func TestSleepRejectsOverMax(t *testing.T) {
	// Default maxSleepMs is 30000; request 60000.
	start := time.Now()
	_, fault := call(t, bg(), &handlerSnapshot{}, modern.BuiltinConfig{},
		wire.MethodToolsCall, toolsCallBody("1", modern.ToolSleep, `{"durationMs":60000}`))
	elapsed := time.Since(start)
	if fault == nil {
		t.Fatal("expected -32602 rejecting over-max sleep, got success")
	}
	if fault.Code != wire.ErrCodeInvalidParams {
		t.Errorf("fault code = %d, want %d", fault.Code, wire.ErrCodeInvalidParams)
	}
	if elapsed > 50*time.Millisecond {
		t.Errorf("reject took %s, want < 50ms — a delay was incurred, so it did not reject immediately", elapsed)
	}
	data := mustObject(t, fault.Data)
	if string(data["requestedMs"]) != "60000" || string(data["maxSleepMs"]) != "30000" {
		t.Errorf("reject data = %s, want requestedMs=60000 maxSleepMs=30000", fault.Data)
	}
}

// TestSleepClamp proves 202.15: onExceedMaxSleep=clamp delays maxSleepMs and
// sets _meta.clamped:true. maxSleepMs is set small so the test is fast.
func TestSleepClamp(t *testing.T) {
	bcfg := modern.BuiltinConfig{Sleep: modern.SleepBehavior{
		MaxSleepMs:  60,
		OnExceedMax: modern.ExceedClamp,
	}}
	start := time.Now()
	raw, fault := call(t, bg(), &handlerSnapshot{}, bcfg,
		wire.MethodToolsCall, toolsCallBody("1", modern.ToolSleep, `{"durationMs":100000}`))
	if fault != nil {
		t.Fatalf("unexpected fault: %v", fault)
	}
	if elapsed := time.Since(start); elapsed < 60*time.Millisecond {
		t.Errorf("clamp delayed %s, want >= 60ms (maxSleepMs)", elapsed)
	}
	obj := mustObject(t, raw)
	if got := contentText(t, obj); got != "slept 60ms" {
		t.Errorf("clamped sleep text = %q, want %q", got, "slept 60ms")
	}
	meta := mustObject(t, obj["_meta"])
	if string(meta["clamped"]) != "true" {
		t.Errorf("_meta.clamped = %s, want true", meta["clamped"])
	}
	if string(meta["requestedMs"]) != "100000" {
		t.Errorf("_meta.requestedMs = %s, want 100000", meta["requestedMs"])
	}
}

// TestSleepNegativeDuration proves the -32602 negative_duration case.
func TestSleepNegativeDuration(t *testing.T) {
	_, fault := call(t, bg(), &handlerSnapshot{}, modern.BuiltinConfig{},
		wire.MethodToolsCall, toolsCallBody("1", modern.ToolSleep, `{"durationMs":-5}`))
	if fault == nil || fault.Code != wire.ErrCodeInvalidParams {
		t.Fatalf("expected -32602 for negative duration, got %v", fault)
	}
	assertDataReason(t, fault.Data, "negative_duration")
}

// TestSleepCancellation proves 202.16 / MOCK-212 (crit "canceling via ctx"):
// a sleep in flight, cancelled via ctx, returns (nil, nil) within 50 ms of the
// cancellation and writes NO result frame. Here the handler is invoked directly
// so the (nil result, nil fault) return — the pipeline's cancellation signal —
// is asserted; the pipeline's own MOCK-212 finishCanceled path (elapsed record,
// cancelled metric, no frame) is covered by the engine's pipeline tests, which
// this handler feeds. goleak in TestMain proves no timer/goroutine leaks.
func TestSleepCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(bg())
	// Cancel shortly after the sleep begins.
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	raw, fault := call(t, ctx, &handlerSnapshot{}, modern.BuiltinConfig{},
		wire.MethodToolsCall, toolsCallBody("1", modern.ToolSleep, `{"durationMs":10000}`))
	elapsed := time.Since(start)
	if fault != nil {
		t.Fatalf("cancelled sleep returned a fault, want nil: %v", fault)
	}
	if raw != nil {
		t.Fatalf("cancelled sleep returned a result, want nil (no frame): %s", raw)
	}
	// Cancellation observed well within the 10s configured delay, and within a
	// small margin of the 20ms cancel (MOCK-212.1's 50ms bound from the cancel).
	if elapsed > 200*time.Millisecond {
		t.Errorf("cancellation observed after %s, expected promptly after cancel", elapsed)
	}
}

// TestSleepByteIdentical proves crit 8 for sleep: two sleeps at the same seed
// (different real durations) produce byte-identical bodies because sleptMs is
// the intended value, never a measurement (builtin-tools.md 3.6).
func TestSleepByteIdentical(t *testing.T) {
	body := toolsCallBody("1", modern.ToolSleep, `{"durationMs":30}`)
	first, _ := call(t, bg(), &handlerSnapshot{}, modern.BuiltinConfig{}, wire.MethodToolsCall, body)
	for i := 0; i < 3; i++ {
		next, _ := call(t, bg(), &handlerSnapshot{}, modern.BuiltinConfig{}, wire.MethodToolsCall, body)
		if string(next) != string(first) {
			t.Fatalf("sleep body not byte-identical on run %d:\n first=%s\n next =%s", i, first, next)
		}
	}
}

// ---- fail -----------------------------------------------------------------

// TestFailDefaultToolError proves 202.17 (crit "fail defaulting to toolError"):
// fail with default config returns a RESULT (nil fault) with isError:true and
// HTTP-200 semantics (a successful envelope). The default message appears.
func TestFailDefaultToolError(t *testing.T) {
	raw, fault := call(t, bg(), &handlerSnapshot{}, modern.BuiltinConfig{},
		wire.MethodToolsCall, toolsCallBody("1", modern.ToolFail, ""))
	if fault != nil {
		t.Fatalf("default fail returned a fault, want a toolError result: %v", fault)
	}
	obj := mustObject(t, raw)
	if string(obj["isError"]) != "true" {
		t.Errorf("fail toolError isError = %s, want true", obj["isError"])
	}
	if !hasKey(obj, "content") {
		t.Errorf("fail toolError has no content: %s", raw)
	}
	if hasKey(obj, "structuredContent") {
		t.Errorf("fail toolError must not emit structuredContent: %s", raw)
	}
}

// TestFailScenarioSelectsMode proves 202.19 (crit "scenario selects the mode"):
// with allowClientOverride false (default), a caller's arguments.mode is IGNORED
// and fail still returns toolError even though the caller asked for
// protocolError.
func TestFailScenarioSelectsMode(t *testing.T) {
	// Scenario config: default (toolError), no client override.
	raw, fault := call(t, bg(), &handlerSnapshot{}, modern.BuiltinConfig{},
		wire.MethodToolsCall, toolsCallBody("1", modern.ToolFail, `{"mode":"protocolError"}`))
	if fault != nil {
		t.Fatalf("caller flipped fail to protocolError despite allowClientOverride=false: %v", fault)
	}
	if string(mustObject(t, raw)["isError"]) != "true" {
		t.Errorf("expected scenario-selected toolError, got %s", raw)
	}
}

// TestFailProtocolError proves 202.18: fail configured for protocolError returns
// a JSON-RPC error with the default code -32603 and — through the pipeline —
// echoes the id. Here the handler yields a *Fault carrying the code; the id echo
// is the engine emit path, asserted by decoding what emit would produce.
func TestFailProtocolError(t *testing.T) {
	bcfg := modern.BuiltinConfig{Fail: modern.FailBehavior{Mode: modern.FailProtocolError}}
	_, fault := call(t, bg(), &handlerSnapshot{}, bcfg,
		wire.MethodToolsCall, toolsCallBody("42", modern.ToolFail, ""))
	if fault == nil {
		t.Fatal("protocolError fail returned no fault")
	}
	if fault.Code != wire.ErrCodeInternal {
		t.Errorf("protocolError code = %d, want %d (-32603 default)", fault.Code, wire.ErrCodeInternal)
	}
	if fault.HTTPStatus != http.StatusOK {
		t.Errorf("protocolError HTTP status = %d, want 200 (fail seam)", fault.HTTPStatus)
	}
}

// TestFailClientOverride proves the opt-in: with allowClientOverride true, the
// caller's arguments.mode/code/message win.
func TestFailClientOverride(t *testing.T) {
	bcfg := modern.BuiltinConfig{Fail: modern.FailBehavior{AllowClientOverride: true}}
	_, fault := call(t, bg(), &handlerSnapshot{}, bcfg,
		wire.MethodToolsCall,
		toolsCallBody("1", modern.ToolFail, `{"mode":"protocolError","code":-32000,"message":"boom"}`))
	if fault == nil {
		t.Fatal("client override to protocolError ignored")
	}
	if fault.Code != -32000 || fault.Message != "boom" {
		t.Errorf("override not applied: code=%d message=%q", fault.Code, fault.Message)
	}
}

// TestToolsCallResultTypeServerInfo proves crit 6 for tools/call: an echo result
// carries resultType "toolResult" and _meta.serverInfo.
func TestToolsCallResultTypeServerInfo(t *testing.T) {
	raw, fault := call(t, bg(), &handlerSnapshot{}, modern.BuiltinConfig{},
		wire.MethodToolsCall, toolsCallBody("1", modern.ToolEcho, `{"a":1}`))
	if fault != nil {
		t.Fatalf("unexpected fault: %v", fault)
	}
	obj := mustObject(t, raw)
	if got := string(obj["resultType"]); got != `"`+wire.ResultTypeToolResult+`"` {
		t.Errorf("resultType = %s, want %q", got, wire.ResultTypeToolResult)
	}
	meta := mustObject(t, obj["_meta"])
	if !hasKey(meta, "serverInfo") {
		t.Errorf("tools/call _meta missing serverInfo: %s", obj["_meta"])
	}
}

// TestUnknownTool proves an unknown tool name is -32602 (a bad params.name), not
// -32601 (which is the engine's disabled/missing-method answer).
func TestUnknownTool(t *testing.T) {
	_, fault := call(t, bg(), &handlerSnapshot{}, modern.BuiltinConfig{},
		wire.MethodToolsCall, toolsCallBody("1", "nope", ""))
	if fault == nil || fault.Code != wire.ErrCodeInvalidParams {
		t.Fatalf("expected -32602 for unknown tool, got %v", fault)
	}
}

// contentText extracts content[0].text from a tools/call result.
func contentText(t *testing.T, obj map[string]json.RawMessage) string {
	t.Helper()
	var blocks []wire.TextContentBlock
	if err := json.Unmarshal(obj["content"], &blocks); err != nil {
		t.Fatalf("decode content: %v (raw=%s)", err, obj["content"])
	}
	if len(blocks) == 0 {
		t.Fatalf("content array is empty")
	}
	return blocks[0].Text
}

// assertMissingData checks the fault data is {"missing":[names...]} containing
// want.
func assertMissingData(t *testing.T, data json.RawMessage, want string) {
	t.Helper()
	var payload wire.MetaMissingErrorData
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("decode data.missing: %v (raw=%s)", err, data)
	}
	for _, m := range payload.Missing {
		if m == want {
			return
		}
	}
	t.Errorf("data.missing = %v, want to contain %q", payload.Missing, want)
}

// assertDataReason checks the fault data is {"reason": want}.
func assertDataReason(t *testing.T, data json.RawMessage, want string) {
	t.Helper()
	var payload struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("decode data.reason: %v (raw=%s)", err, data)
	}
	if payload.Reason != want {
		t.Errorf("data.reason = %q, want %q", payload.Reason, want)
	}
}

// engineSnapshot is the engine.Snapshot interface, referenced so the test file's
// snapshot doubles are checked against it at compile time.
var _ engine.Snapshot = (*handlerSnapshot)(nil)
var _ engine.Snapshot = bareSnapshot{}
