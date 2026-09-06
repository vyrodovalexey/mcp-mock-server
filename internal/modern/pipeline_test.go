package modern_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/vyrodovalexey/mcp-mock-server/internal/determinism"
	"github.com/vyrodovalexey/mcp-mock-server/internal/engine"
	"github.com/vyrodovalexey/mcp-mock-server/internal/journal"
	"github.com/vyrodovalexey/mcp-mock-server/internal/modern"
	"github.com/vyrodovalexey/mcp-mock-server/internal/wire"
)

// pipeline_test.go drives the handlers through the REAL engine pipeline so the
// crit-5 per-method disable (a stageDispatch behavior, not a handler behavior)
// and the crit-8 byte-identity of the full JSON-RPC envelope can be asserted end
// to end. It also exercises the MOCK-209 omission switches through the pipeline.

// pipelineInstance is a minimal engine.Instance for driving the pipeline in this
// package. It owns no journal (stage 9 is a no-op) and returns a configurable
// snapshot, so a test controls MethodEnabled and the discover/omission surface.
type pipelineInstance struct {
	key  determinism.Key
	snap engine.Snapshot
}

func newPipelineInstance(snap engine.Snapshot) *pipelineInstance {
	key := determinism.Root(testSeed).Derive(determinism.DomainInstance, []byte("pipe"))
	return &pipelineInstance{key: key, snap: snap}
}

func (i *pipelineInstance) Name() string                  { return "pipe" }
func (i *pipelineInstance) LoadSnapshot() engine.Snapshot { return i.snap }
func (i *pipelineInstance) InstanceKey() determinism.Key  { return i.key }
func (i *pipelineInstance) Metrics() engine.Metrics       { return nil }
func (i *pipelineInstance) Epoch() time.Time              { return time.Unix(0, 0).UTC() }
func (i *pipelineInstance) Start() time.Time              { return time.Now() }

// Journal returns nil ring and capturer: this instance owns no journal, so
// pipeline stage 9 is a no-op (the handler behavior under test does not depend
// on journalling).
func (i *pipelineInstance) Journal() (*journal.Ring, *journal.Capturer) {
	return nil, nil
}

// Compile-time assertion that *pipelineInstance satisfies the engine seam.
var _ engine.Instance = (*pipelineInstance)(nil)

// runPipeline drives one request through a fresh pipeline registered with the
// TASK-018 handlers and returns the response bytes.
func runPipeline(
	t *testing.T, snap engine.Snapshot, bcfg modern.BuiltinConfig, method, body string,
) []byte {
	t.Helper()
	reg := engine.NewRegistry()
	modern.RegisterHandlers(reg, bcfg)
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

// envelopeError decodes a JSON-RPC error object from a full response envelope,
// or nil when the envelope carries a result.
func envelopeError(t *testing.T, body []byte) *struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
} {
	t.Helper()
	var env struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode envelope: %v (body=%s)", err, body)
	}
	return env.Error
}

// TestMethodDisabledReturns32601 proves crit 5 / MOCK-202.2: a method with
// switches.methods.<name>.enabled:false returns -32601, while the other two
// methods are unaffected. The disable is enforced at the engine dispatch stage,
// so this must run through the pipeline.
func TestMethodDisabledReturns32601(t *testing.T) {
	// Disable tools/list; leave discover and tools/call enabled.
	snap := &handlerSnapshot{
		disabled: map[string]bool{wire.MethodToolsList: true},
		era:      "modern",
	}

	// Disabled method → -32601.
	body := runPipeline(t, snap, modern.BuiltinConfig{}, wire.MethodToolsList, toolsListBody("1"))
	e := envelopeError(t, body)
	if e == nil {
		t.Fatalf("disabled tools/list did not return an error: %s", body)
	}
	if e.Code != wire.ErrCodeMethodNotFound {
		t.Errorf("disabled method code = %d, want %d (-32601)", e.Code, wire.ErrCodeMethodNotFound)
	}

	// Other methods unaffected: discover succeeds.
	discBody := runPipeline(t, snap, modern.BuiltinConfig{}, wire.MethodDiscover, discoverBody("2"))
	if envelopeError(t, discBody) != nil {
		t.Errorf("discover affected by tools/list disable: %s", discBody)
	}
	// tools/call succeeds.
	callBody := runPipeline(t, snap, modern.BuiltinConfig{}, wire.MethodToolsCall,
		toolsCallBody("3", modern.ToolEcho, `{"a":1}`))
	if envelopeError(t, callBody) != nil {
		t.Errorf("tools/call affected by tools/list disable: %s", callBody)
	}
}

// TestOmitResultType proves MOCK-209.3 (crit "resultType omission switch"): with
// switches.omitResultType, the resultType key is ABSENT from every result — and
// present again when the switch is off. Tested across all three methods.
func TestOmitResultType(t *testing.T) {
	for _, tc := range []struct {
		method string
		body   string
	}{
		{wire.MethodDiscover, discoverBody("1")},
		{wire.MethodToolsList, toolsListBody("1")},
		{wire.MethodToolsCall, toolsCallBody("1", modern.ToolEcho, `{"a":1}`)},
	} {
		t.Run(tc.method, func(t *testing.T) {
			// Omitted.
			omit := &handlerSnapshot{omitRT: true, era: "modern"}
			body := runPipeline(t, omit, modern.BuiltinConfig{}, tc.method, tc.body)
			if resultHasKey(t, body, "resultType") {
				t.Errorf("resultType present despite omitResultType: %s", body)
			}
			// Present.
			keep := &handlerSnapshot{era: "modern"}
			body2 := runPipeline(t, keep, modern.BuiltinConfig{}, tc.method, tc.body)
			if !resultHasKey(t, body2, "resultType") {
				t.Errorf("resultType absent without omit switch: %s", body2)
			}
		})
	}
}

// TestOmitServerInfoMeta proves MOCK-209.4 (crit "serverInfo omission switch"):
// with switches.omitServerInfoMeta, _meta is absent (no serverInfo to carry),
// and present again when the switch is off.
func TestOmitServerInfoMeta(t *testing.T) {
	// Omitted: the whole _meta object is absent for discover/list/echo (no other
	// _meta members in these cases).
	omit := &handlerSnapshot{omitSrvInfo: true, era: "modern"}
	body := runPipeline(t, omit, modern.BuiltinConfig{}, wire.MethodDiscover, discoverBody("1"))
	if resultHasKey(t, body, "_meta") {
		t.Errorf("_meta present despite omitServerInfoMeta: %s", body)
	}
	// Present.
	keep := &handlerSnapshot{era: "modern"}
	body2 := runPipeline(t, keep, modern.BuiltinConfig{}, wire.MethodDiscover, discoverBody("1"))
	if !resultHasKey(t, body2, "_meta") {
		t.Errorf("_meta absent without omit switch: %s", body2)
	}
}

// TestFullEnvelopeByteIdentical proves crit 8 end to end: two identical requests
// through the whole pipeline (envelope included) at the same seed produce
// byte-identical response bytes, for each method.
func TestFullEnvelopeByteIdentical(t *testing.T) {
	snap := &handlerSnapshot{discover: fullDiscover(), era: "modern"}
	cases := map[string]string{
		wire.MethodDiscover:  discoverBody("7"),
		wire.MethodToolsList: toolsListBody("7"),
		wire.MethodToolsCall: toolsCallBody("7", modern.ToolEcho, `{"z":1,"a":2}`),
	}
	for method, body := range cases {
		first := runPipeline(t, snap, modern.BuiltinConfig{}, method, body)
		for i := 0; i < 5; i++ {
			next := runPipeline(t, snap, modern.BuiltinConfig{}, method, body)
			if string(next) != string(first) {
				t.Fatalf("%s envelope not byte-identical on run %d:\n first=%s\n next =%s",
					method, i, first, next)
			}
		}
	}
}

// resultHasKey reports whether the result object inside a success envelope has
// key. It fails the test if the envelope is not a success response.
func resultHasKey(t *testing.T, body []byte, key string) bool {
	t.Helper()
	var env struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode envelope: %v (body=%s)", err, body)
	}
	if len(env.Result) == 0 {
		t.Fatalf("envelope carries no result: %s", body)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(env.Result, &obj); err != nil {
		t.Fatalf("result not an object: %v", err)
	}
	_, ok := obj[key]
	return ok
}
