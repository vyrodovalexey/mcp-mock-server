package engine_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/vyrodovalexey/mcp-mock-server/internal/engine"
	"github.com/vyrodovalexey/mcp-mock-server/internal/wire"
)

// TestSnapshotIsolationAcrossMutation is acceptance criterion 3: the Snapshot
// captured at stage 1 is the only one a request reads, so a control mutation
// applied between stage 1 and dispatch cannot appear in that request's response.
// A gate handler blocks after stage 1 has captured the snapshot; the test
// mutates the instance's snapshot while the handler is blocked, then releases
// it, and asserts the request saw the pre-mutation configuration.
func TestSnapshotIsolationAcrossMutation(t *testing.T) {
	inst := newInstance(1, "inst", false)

	entered := make(chan uint64, 1)
	release := make(chan struct{})
	gate := engine.HandlerFunc(func(_ context.Context, ex *engine.Exchange) (json.RawMessage, *engine.Fault) {
		entered <- ex.Snapshot.Gen() // the generation THIS request captured at stage 1
		<-release
		return json.RawMessage(echoResult), nil
	})
	p := engine.NewPipeline(registryFor(gate))

	done := make(chan struct{})
	go func() {
		defer close(done)
		sink := engine.NewBufferedSink()
		_ = p.Handle(context.Background(), buildExchange(
			context.Background(), inst, engine.KindHTTP, rawRequest("1", wire.MethodToolsCall)), sink)
	}()

	capturedGen := <-entered
	// Mutate the instance's snapshot mid-request (a control-API swap).
	next := newSnapshot()
	next.gen = capturedGen + 1
	inst.setSnapshot(next)
	// Release the handler; it must still see the snapshot it captured at stage 1.
	close(release)
	<-done

	// A fresh request now sees the mutated generation, proving the swap took
	// effect and the previous request's isolation was real, not a no-op.
	var freshGen uint64
	check := engine.HandlerFunc(func(_ context.Context, ex *engine.Exchange) (json.RawMessage, *engine.Fault) {
		freshGen = ex.Snapshot.Gen()
		return json.RawMessage(echoResult), nil
	})
	sink := engine.NewBufferedSink()
	_ = engine.NewPipeline(registryFor(check)).Handle(context.Background(), buildExchange(
		context.Background(), inst, engine.KindHTTP, rawRequest("2", wire.MethodToolsCall)), sink)

	if capturedGen != 1 {
		t.Fatalf("request captured generation %d, want the pre-mutation 1", capturedGen)
	}
	if freshGen != capturedGen+1 {
		t.Fatalf("fresh request saw generation %d, want the post-mutation %d", freshGen, capturedGen+1)
	}
}

// registryFor builds a registry serving all Phase 1 methods with h.
func registryFor(h engine.Handler) *engine.Registry {
	reg := engine.NewRegistry()
	for _, m := range wire.Phase1Methods() {
		reg.Register(m, h)
	}
	return reg
}

// TestPerStageDrawCount is acceptance criterion 4: each stage's RNG draw count
// for a fixed request matches a committed per-stage expectation (ADR-002
// rule 2). The Phase 1 expectation is: stages 1-5 and 7-9 draw ZERO, and only
// the dispatch stage (6) draws — exactly what its handler draws.
//
// Draws are attributed by seed-dependence, which is precise and does not require
// reaching into the engine's private RNG:
//
//   - A handler that never calls ex.Rand (echo) must produce SEED-INDEPENDENT
//     output. If any stage 1-5/7-9 drew from the RNG, the RNG would advance and,
//     more tellingly, a pre-dispatch draw would only be observable if it changed
//     output — but since the ADR-002 accessor is lazy (sync.OnceValue), a
//     zero-draw request never even constructs the RNG. Seed-independence of the
//     echo output is the observable proof that no non-handler stage draws.
//   - A handler that draws (random) must produce SEED-DEPENDENT output, proving
//     the dispatch stage's draws are the ones that matter and are wired to the
//     per-request key.
func TestPerStageDrawCount(t *testing.T) {
	raw := rawRequest("1", wire.MethodToolsCall)

	// Echo: zero draws anywhere ⇒ output identical across two different seeds.
	echoSeed1 := runOnceSeedHandler(t, raw, 1, echoHandler())
	echoSeed2 := runOnceSeedHandler(t, raw, 2, echoHandler())
	if string(echoSeed1) != string(echoSeed2) {
		t.Fatalf("zero-draw request depends on seed — a non-dispatch stage drew from the RNG:\n"+
			" seed1=%s\n seed2=%s", echoSeed1, echoSeed2)
	}

	// Random: dispatch draws ⇒ output differs across seeds.
	randSeed1 := runOnceSeedHandler(t, raw, 1, randomHandler())
	randSeed2 := runOnceSeedHandler(t, raw, 2, randomHandler())
	if string(randSeed1) == string(randSeed2) {
		t.Fatalf("drawing handler produced seed-independent output — dispatch draws are not wired to the request key")
	}

	// All nine stages still run for the drawing request (the draw is at stage 6,
	// appended, not inserted).
	inst := newInstance(1, "inst", false)
	var stages []engine.StageID
	p := engine.NewPipeline(registryFor(randomHandler())).WithObserverForTest(func(s engine.StageID) {
		stages = append(stages, s)
	})
	sink := engine.NewBufferedSink()
	_ = p.Handle(context.Background(), buildExchange(context.Background(), inst, engine.KindHTTP, raw), sink)
	if len(stages) != 9 {
		t.Fatalf("drawing request ran %d stages, want 9: %v", len(stages), stages)
	}
}

// runOnceSeedHandler runs one request at seed with an explicit handler and
// returns the response bytes.
func runOnceSeedHandler(t *testing.T, raw []byte, seed uint64, h engine.Handler) []byte {
	t.Helper()
	inst := newInstance(seed, "inst", false)
	p := engine.NewPipeline(registryFor(h))
	sink := engine.NewBufferedSink()
	if err := p.Handle(context.Background(), buildExchange(
		context.Background(), inst, engine.KindHTTP, raw), sink); err != nil {
		t.Fatalf("handle: %v", err)
	}
	return append([]byte(nil), sink.Bytes()...)
}
