package engine_test

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"time"

	"github.com/vyrodovalexey/mcp-mock-server/internal/determinism"
	"github.com/vyrodovalexey/mcp-mock-server/internal/engine"
	"github.com/vyrodovalexey/mcp-mock-server/internal/journal"
	"github.com/vyrodovalexey/mcp-mock-server/journalapi"
)

// fixtures_test.go provides in-package test doubles for the engine's
// consumer-defined interfaces (Instance, Snapshot, Metrics). They stand in for
// TASK-015's *Instance and *Snapshot and TASK-013's *obs.InstanceMetrics, so
// the engine can be exercised in isolation before those packages exist.

// testEpoch is a fixed scenario epoch so the virtual clock is reproducible in
// tests. Its exact value is irrelevant; its fixedness is the point.
var testEpoch = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

// fakeSnapshot is a minimal immutable Snapshot for tests. Its fields are set at
// construction and never mutated, matching the ADR-014 immutability contract.
type fakeSnapshot struct {
	gen       uint64
	era       string
	validator engine.MetaValidator
	disabled  map[string]bool
	jEnabled  bool
}

func newSnapshot() *fakeSnapshot {
	return &fakeSnapshot{gen: 1, era: "modern", jEnabled: true}
}

func (s *fakeSnapshot) Gen() uint64          { return s.gen }
func (s *fakeSnapshot) Era() string          { return s.era }
func (s *fakeSnapshot) JournalEnabled() bool { return s.jEnabled }

func (s *fakeSnapshot) MetaValidator() engine.MetaValidator { return s.validator }

func (s *fakeSnapshot) MethodEnabled(method string) bool {
	return !s.disabled[method]
}

// fakeInstance is a test Instance backed by a real journal ring and capturer so
// stage 9 exercises the genuine capture path, plus a swappable snapshot pointer
// so a test can mutate configuration mid-request (ADR-014 isolation test).
type fakeInstance struct {
	name     string
	key      determinism.Key
	snap     atomic.Pointer[fakeSnapshot]
	ring     *journal.Ring
	capturer *journal.Capturer
	metrics  engine.Metrics
	epoch    time.Time
	start    time.Time
}

// newInstance builds a fake instance with a working journal at the given seed
// and name. journalOn selects whether the ring starts enabled.
func newInstance(seed uint64, name string, journalOn bool) *fakeInstance {
	root := determinism.Root(seed)
	key := root.Derive(determinism.DomainInstance, []byte(name))
	cfg := journalapi.Config{Enabled: journalOn, Mode: journalapi.CaptureFull}
	ring := journal.New(cfg)
	capturer := journal.NewCapturer(cfg, journal.NewCredHasher())
	inst := &fakeInstance{
		name:     name,
		key:      key,
		ring:     ring,
		capturer: capturer,
		epoch:    testEpoch,
		start:    time.Now(),
	}
	snap := newSnapshot()
	snap.jEnabled = journalOn
	inst.snap.Store(snap)
	return inst
}

func (i *fakeInstance) Name() string                  { return i.name }
func (i *fakeInstance) InstanceKey() determinism.Key  { return i.key }
func (i *fakeInstance) Epoch() time.Time              { return i.epoch }
func (i *fakeInstance) Start() time.Time              { return i.start }
func (i *fakeInstance) Metrics() engine.Metrics       { return i.metrics }
func (i *fakeInstance) LoadSnapshot() engine.Snapshot { return i.snap.Load() }

func (i *fakeInstance) Journal() (*journal.Ring, *journal.Capturer) {
	return i.ring, i.capturer
}

// setSnapshot swaps the instance's snapshot, simulating a control-API mutation.
func (i *fakeInstance) setSnapshot(s *fakeSnapshot) { i.snap.Store(s) }

// countingMetrics is a test Metrics that counts each call, so a test can assert
// the pipeline records metrics and that DecInFlight always balances IncInFlight.
type countingMetrics struct {
	inFlight    atomic.Int64
	maxInFlight atomic.Int64
	requests    atomic.Int64
	journalRecs atomic.Int64
	lastGen     atomic.Uint64
	lastOutcome atomic.Value // string
}

func (m *countingMetrics) IncInFlight() {
	v := m.inFlight.Add(1)
	for {
		max := m.maxInFlight.Load()
		if v <= max || m.maxInFlight.CompareAndSwap(max, v) {
			break
		}
	}
}
func (m *countingMetrics) DecInFlight() { m.inFlight.Add(-1) }

func (m *countingMetrics) RecordRequest(_, _, _, outcome string, _ float64) {
	m.requests.Add(1)
	m.lastOutcome.Store(outcome)
}
func (m *countingMetrics) IncJournalRecord(_ float64)     { m.journalRecs.Add(1) }
func (m *countingMetrics) SetSnapshotGeneration(g uint64) { m.lastGen.Store(g) }

// echoResult is the raw result an echo handler returns; a fixed byte string so
// byte-identity assertions have something concrete to compare.
const echoResult = `{"echo":true}`

// echoHandler is a trivial handler returning a fixed result and drawing no RNG,
// standing in for a Phase 1 handler on the MOCK-901 no-draw path.
func echoHandler() engine.Handler {
	return engine.HandlerFunc(func(_ context.Context, _ *engine.Exchange) (json.RawMessage, *engine.Fault) {
		return json.RawMessage(echoResult), nil
	})
}

// randomHandler draws one RNG value and encodes it, so a byte-identity test can
// prove the seeded stream is reproducible and a draw-count test can observe a
// draw at the dispatch stage.
func randomHandler() engine.Handler {
	return engine.HandlerFunc(func(_ context.Context, ex *engine.Exchange) (json.RawMessage, *engine.Fault) {
		n := ex.Rand().IntN(1_000_000)
		b, _ := json.Marshal(map[string]int{"n": n})
		return b, nil
	})
}

// sleepHandler blocks until the context is cancelled or the timeout elapses,
// standing in for TASK-018's sleep tool: it selects on ctx.Done() rather than
// sleeping through cancellation (MOCK-212).
func sleepHandler(d time.Duration) engine.Handler {
	return engine.HandlerFunc(func(ctx context.Context, _ *engine.Exchange) (json.RawMessage, *engine.Fault) {
		select {
		case <-ctx.Done():
			return nil, nil // cancelled; finishResult sees canceled(ctx) and records it
		case <-time.After(d):
			return json.RawMessage(echoResult), nil
		}
	})
}

// buildExchange assembles an Exchange for method with the given raw body over
// the given transport, using ctx as the request context.
func buildExchange(ctx context.Context, inst *fakeInstance, kind engine.Kind, raw []byte) *engine.Exchange {
	return &engine.Exchange{
		Ctx:       ctx,
		Instance:  inst,
		Transport: kind,
		Peer:      "test-peer",
		Raw:       raw,
	}
}

// rawRequest builds a JSON-RPC request body with the given id and method and an
// empty params object carrying a _meta so the journal has something to preserve.
func rawRequest(id, method string) []byte {
	return []byte(`{"jsonrpc":"2.0","id":` + id + `,"method":"` + method +
		`","params":{"_meta":{"protocolVersion":"2026-07-28"}}}`)
}
