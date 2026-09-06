package httpx_test

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/vyrodovalexey/mcp-mock-server/internal/determinism"
	"github.com/vyrodovalexey/mcp-mock-server/internal/engine"
	"github.com/vyrodovalexey/mcp-mock-server/internal/journal"
	"github.com/vyrodovalexey/mcp-mock-server/internal/transport/httpx"
	"github.com/vyrodovalexey/mcp-mock-server/internal/wire"
	"github.com/vyrodovalexey/mcp-mock-server/journalapi"
)

// TestMain runs the suite under goleak so a leaked timer-wheel goroutine, a
// leaked connection goroutine, or an un-drained stream fails the suite rather
// than passing silently — the failure mode acceptance criterion 3 guards
// against. Each server test shuts the server down (Shutdown or Close) in
// cleanup, so a clean run leaks nothing the transport owns.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// fixtures_test.go provides in-package test doubles for the engine's
// consumer-defined interfaces so the transport can be exercised end to end
// against a real pipeline without depending on TASK-015's *Instance (which is
// parallel work). They mirror internal/engine's own fixtures.

var testEpoch = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

// fakeSnapshot is a minimal immutable engine.Snapshot.
type fakeSnapshot struct {
	gen      uint64
	era      string
	disabled map[string]bool
	jEnabled bool
}

func (s *fakeSnapshot) Gen() uint64          { return s.gen }
func (s *fakeSnapshot) Era() string          { return s.era }
func (s *fakeSnapshot) JournalEnabled() bool { return s.jEnabled }

func (s *fakeSnapshot) MetaValidator() engine.MetaValidator { return engine.AcceptAllMeta }

func (s *fakeSnapshot) MethodEnabled(method string) bool { return !s.disabled[method] }

// fakeInstance is a test engine.Instance backed by a real journal ring so the
// transport's journal-capture path (MOCK-601.2 header fidelity) is genuinely
// exercised.
type fakeInstance struct {
	name     string
	key      determinism.Key
	snap     atomic.Pointer[fakeSnapshot]
	ring     *journal.Ring
	capturer *journal.Capturer
	epoch    time.Time
	start    time.Time
}

func newInstance(seed uint64, name string) *fakeInstance {
	root := determinism.Root(seed)
	key := root.Derive(determinism.DomainInstance, []byte(name))
	cfg := journalapi.Config{Enabled: true, Mode: journalapi.CaptureFull}
	inst := &fakeInstance{
		name:     name,
		key:      key,
		ring:     journal.New(cfg),
		capturer: journal.NewCapturer(cfg, journal.NewCredHasher()),
		epoch:    testEpoch,
		start:    time.Now(),
	}
	inst.snap.Store(&fakeSnapshot{gen: 1, era: "modern", jEnabled: true})
	return inst
}

func (i *fakeInstance) Name() string                  { return i.name }
func (i *fakeInstance) InstanceKey() determinism.Key  { return i.key }
func (i *fakeInstance) Epoch() time.Time              { return i.epoch }
func (i *fakeInstance) Start() time.Time              { return i.start }
func (i *fakeInstance) Metrics() engine.Metrics       { return nil }
func (i *fakeInstance) LoadSnapshot() engine.Snapshot { return i.snap.Load() }

func (i *fakeInstance) Journal() (*journal.Ring, *journal.Capturer) {
	return i.ring, i.capturer
}

// newMount builds a Mount for name at path serving handler h through a real
// pipeline registered with the three Phase 1 method names (no wire literals,
// ADR-019).
func newMount(seed uint64, name, path string, h engine.Handler) *httpx.Mount {
	return &httpx.Mount{
		Path:     path,
		Instance: newInstance(seed, name),
		Pipeline: newPipelineFor(newRegistryForMethods(h)),
	}
}

// newRegistryForMethods builds a registry serving the three Phase 1 methods
// with h. It lets a test construct a Mount around a specific instance.
func newRegistryForMethods(h engine.Handler) *engine.Registry {
	reg := engine.NewRegistry()
	for _, m := range wire.Phase1Methods() {
		reg.Register(m, h)
	}
	return reg
}

// newPipelineFor wraps a registry in a pipeline.
func newPipelineFor(reg *engine.Registry) *engine.Pipeline { return engine.NewPipeline(reg) }

// echoHandler returns a fixed result and draws no RNG.
func echoHandler(result string) engine.Handler {
	return engine.HandlerFunc(func(_ context.Context, _ *engine.Exchange) (json.RawMessage, *engine.Fault) {
		return json.RawMessage(result), nil
	})
}

// gatedHandler blocks until release is closed, then returns result — a
// controllable in-flight request for shutdown tests.
func gatedHandler(release <-chan struct{}, result string) engine.Handler {
	return engine.HandlerFunc(func(ctx context.Context, _ *engine.Exchange) (json.RawMessage, *engine.Fault) {
		select {
		case <-release:
			return json.RawMessage(result), nil
		case <-ctx.Done():
			return nil, nil
		}
	})
}

// releaseOnCancel signals started when the handler begins, blocks on ctx.Done(),
// and closes released once the context is canceled — the MOCK-212 disconnect
// observation point. A handler that never saw cancellation would leave released
// open and hang the test.
func releaseOnCancel(started chan<- struct{}, released chan<- struct{}) engine.Handler {
	return engine.HandlerFunc(func(ctx context.Context, _ *engine.Exchange) (json.RawMessage, *engine.Fault) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-ctx.Done()
		close(released)
		return nil, nil
	})
}

// rawRequest builds a valid JSON-RPC request body for method with a complete
// _meta so the pipeline reaches dispatch.
func rawRequest(id, method string) []byte {
	return []byte(`{"jsonrpc":"2.0","id":` + id + `,"method":"` + method +
		`","params":{"_meta":{"protocolVersion":"2026-07-28","clientCapabilities":{}}}}`)
}

// rawExchange dials base, writes the literal request head+body, reads the whole
// response and discards it. It exists so a test can transmit headers with exact
// casing and duplicates on the wire (MOCK-601.2) without http.Header
// canonicalisation intervening.
func rawExchange(base, head string) error {
	addr := strings.TrimPrefix(base, "http://")
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Write([]byte(head)); err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, conn)
	return nil
}
