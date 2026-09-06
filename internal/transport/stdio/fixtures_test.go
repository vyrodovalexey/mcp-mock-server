package stdio_test

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/vyrodovalexey/mcp-mock-server/internal/determinism"
	"github.com/vyrodovalexey/mcp-mock-server/internal/engine"
	"github.com/vyrodovalexey/mcp-mock-server/internal/journal"
	"github.com/vyrodovalexey/mcp-mock-server/internal/wire"
	"github.com/vyrodovalexey/mcp-mock-server/journalapi"
)

// TestMain runs the package's tests under goleak so a leaked reader, writer or
// worker goroutine — the failure mode acceptance criterion 3 guards against —
// fails the suite rather than passing silently. Each test closes its input to
// reach EOF and waits for Serve to return, so a clean run leaks nothing.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// fixtures_test.go provides the in-package test doubles the stdio transport
// needs to drive a real engine.Pipeline: a fake Instance/Snapshot standing in
// for TASK-015's *Instance, and a small registry of Phase-1-shaped handlers.
// They mirror the engine package's own test doubles so the transport is
// exercised against the genuine pipeline, not a stub.

// testEpoch is a fixed scenario epoch so the virtual clock is reproducible.
var testEpoch = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

// fakeSnapshot is a minimal immutable engine.Snapshot for tests.
type fakeSnapshot struct {
	disabled map[string]bool
	jEnabled bool
}

func (s *fakeSnapshot) Gen() uint64                         { return 1 }
func (s *fakeSnapshot) Era() string                         { return "modern" }
func (s *fakeSnapshot) JournalEnabled() bool                { return s.jEnabled }
func (s *fakeSnapshot) MetaValidator() engine.MetaValidator { return engine.AcceptAllMeta }
func (s *fakeSnapshot) MethodEnabled(method string) bool    { return !s.disabled[method] }

// fakeInstance is a test engine.Instance backed by a real journal ring and
// capturer so stage 9 exercises the genuine capture path.
type fakeInstance struct {
	name     string
	key      determinism.Key
	ring     *journal.Ring
	capturer *journal.Capturer
	snap     *fakeSnapshot
	start    time.Time
}

func newInstance(seed uint64, name string, journalOn bool) *fakeInstance {
	root := determinism.Root(seed)
	key := root.Derive(determinism.DomainInstance, []byte(name))
	cfg := journalapi.Config{Enabled: journalOn, Mode: journalapi.CaptureFull}
	return &fakeInstance{
		name:     name,
		key:      key,
		ring:     journal.New(cfg),
		capturer: journal.NewCapturer(cfg, journal.NewCredHasher()),
		snap:     &fakeSnapshot{jEnabled: journalOn},
		start:    time.Now(),
	}
}

func (i *fakeInstance) Name() string                  { return i.name }
func (i *fakeInstance) InstanceKey() determinism.Key  { return i.key }
func (i *fakeInstance) Epoch() time.Time              { return testEpoch }
func (i *fakeInstance) Start() time.Time              { return i.start }
func (i *fakeInstance) Metrics() engine.Metrics       { return nil }
func (i *fakeInstance) LoadSnapshot() engine.Snapshot { return i.snap }

func (i *fakeInstance) Journal() (*journal.Ring, *journal.Capturer) {
	return i.ring, i.capturer
}

// echoResult is a fixed result body so byte-identity assertions are concrete.
const echoResult = `{"echo":true}`

// newPipeline builds a pipeline whose registry answers the three Phase 1 methods
// with simple test handlers: tools/call echoes, tools/list returns a fixed body,
// server/discover returns a fixed body. The sleepHandler variant is installed by
// tests that need cancellation.
func newPipeline(sleep time.Duration) *engine.Pipeline {
	reg := engine.NewRegistry()
	reg.Register(wire.MethodToolsCall, echoHandler())
	reg.Register(wire.MethodToolsList, fixedHandler(`{"tools":[]}`))
	reg.Register(wire.MethodDiscover, fixedHandler(`{"serverInfo":{}}`))
	if sleep > 0 {
		reg.Register(wire.MethodToolsCall, sleepHandler(sleep))
	}
	return engine.NewPipeline(reg)
}

func echoHandler() engine.Handler {
	return engine.HandlerFunc(
		func(_ context.Context, _ *engine.Exchange) (json.RawMessage, *engine.Fault) {
			return json.RawMessage(echoResult), nil
		})
}

func fixedHandler(body string) engine.Handler {
	return engine.HandlerFunc(
		func(_ context.Context, _ *engine.Exchange) (json.RawMessage, *engine.Fault) {
			return json.RawMessage(body), nil
		})
}

// sleepHandler blocks until the context is canceled or d elapses, standing in
// for TASK-018's sleep tool: it selects on ctx.Done() so cancellation is
// observed rather than slept through (MOCK-212).
func sleepHandler(d time.Duration) engine.Handler {
	return engine.HandlerFunc(
		func(ctx context.Context, _ *engine.Exchange) (json.RawMessage, *engine.Fault) {
			select {
			case <-ctx.Done():
				return nil, nil
			case <-time.After(d):
				return json.RawMessage(echoResult), nil
			}
		})
}

// gateHandler blocks on a shared gate channel so a test can hold requests
// in-flight and observe the pool bound. entered is incremented on entry so a
// test can count how many handlers ran concurrently.
type gateHandler struct {
	entered *atomic.Int64
	peak    *atomic.Int64
	gate    <-chan struct{}
}

func (h gateHandler) Handle(ctx context.Context, _ *engine.Exchange) (json.RawMessage, *engine.Fault) {
	cur := h.entered.Add(1)
	for {
		p := h.peak.Load()
		if cur <= p || h.peak.CompareAndSwap(p, cur) {
			break
		}
	}
	defer h.entered.Add(-1)
	select {
	case <-ctx.Done():
		return nil, nil
	case <-h.gate:
		return json.RawMessage(echoResult), nil
	}
}

// rawRequest builds a JSON-RPC request body with the given id and method.
func rawRequest(id, method string) []byte {
	return []byte(`{"jsonrpc":"2.0","id":` + id + `,"method":"` + method +
		`","params":{"_meta":{"protocolVersion":"2026-07-28"}}}`)
}
