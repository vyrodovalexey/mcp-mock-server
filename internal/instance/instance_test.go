package instance_test

import (
	"context"
	"encoding/json"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/vyrodovalexey/mcp-mock-server/internal/determinism"
	"github.com/vyrodovalexey/mcp-mock-server/internal/engine"
	"github.com/vyrodovalexey/mcp-mock-server/internal/instance"
	"github.com/vyrodovalexey/mcp-mock-server/internal/wire"
	"github.com/vyrodovalexey/mcp-mock-server/journalapi"
	"github.com/vyrodovalexey/mcp-mock-server/scenario"
)

// TestMain installs goleak so any test that leaves a goroutine behind fails
// loudly. It is the primary enforcement of ADR-007's zero-goroutines-at-rest
// invariant for instances: constructing, mutating and dropping an instance must
// leak nothing.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// --- helpers ---------------------------------------------------------------

func boolp(b bool) *bool    { return &b }
func strp(s string) *string { return &s }
func intp(n int) *int       { return &n }

// newInstance builds an instance from a minimal spec. journalOn toggles the
// journal; methods disables the named methods via switches.
func newInstance(t *testing.T, seed uint64, name string, journalOn bool) *instance.Instance {
	t.Helper()
	spec := scenario.InstanceSpec{
		Journal: &scenario.Journal{Enabled: boolp(journalOn)},
	}
	return instance.New(instance.Config{
		Name:  name,
		ID:    int(seed),
		Root:  determinism.Root(seed),
		Spec:  spec,
		Epoch: time.Unix(1_700_000_000, 0),
	})
}

// rawRequest builds a JSON-RPC request body for a method.
func rawRequest(id, method string) []byte {
	return []byte(`{"jsonrpc":"2.0","id":` + id + `,"method":"` + method +
		`","params":{"_meta":{"protocolVersion":"2026-07-28"}}}`)
}

// okHandler returns a handler that always answers with a fixed result, so a
// behavioural test can exercise the pipeline against a real *Instance.
func okHandler() engine.Handler {
	return engine.HandlerFunc(func(_ context.Context, _ *engine.Exchange) (json.RawMessage, *engine.Fault) {
		return json.RawMessage(`{"ok":true}`), nil
	})
}

func pipelineFor() *engine.Pipeline {
	reg := engine.NewRegistry()
	for _, m := range wire.Phase1Methods() {
		reg.Register(m, okHandler())
	}
	return engine.NewPipeline(reg)
}

// --- interface satisfaction ------------------------------------------------

// TestSatisfiesEngineInterfaces is the compile-plus-behaviour proof that
// *Instance and *Snapshot satisfy the engine's consumer-defined interfaces. The
// compile-time assertions live in the package (var _ engine.Instance ...); this
// exercises them through the real pipeline so the interface is proven USED, not
// merely declared.
func TestSatisfiesEngineInterfaces(t *testing.T) {
	var _ engine.Instance = (*instance.Instance)(nil)
	var _ engine.Snapshot = (*instance.Snapshot)(nil)

	inst := newInstance(t, 1, "iface", true)
	p := pipelineFor()
	sink := engine.NewBufferedSink()
	ex := &engine.Exchange{
		Ctx:       context.Background(),
		Instance:  inst,
		Transport: engine.KindHTTP,
		Peer:      "peer",
		Raw:       rawRequest("1", wire.MethodToolsCall),
	}
	if err := p.Handle(ex.Ctx, ex, sink); err != nil {
		t.Fatalf("pipeline against real *Instance: %v", err)
	}
	if len(sink.Bytes()) == 0 {
		t.Fatal("expected a response emitted through the pipeline")
	}
	// The snapshot the pipeline captured at stage 1 is the instance's snapshot.
	if ex.Snapshot == nil {
		t.Fatal("stage 1 did not capture a snapshot from the instance")
	}
	if ex.Snapshot.Era() == "" {
		t.Fatal("snapshot era must be resolved (modern in Phase 1)")
	}
}

// --- COW correctness -------------------------------------------------------

// TestSnapshotImmutability holds a snapshot obtained before a mutation and
// asserts it observes the pre-mutation values indefinitely after the mutation
// (acceptance criterion 2, ADR-014).
func TestSnapshotImmutability(t *testing.T) {
	inst := newInstance(t, 2, "immut", true)
	before := inst.Snapshot()
	genBefore := before.Gen()
	methodBefore := before.MethodEnabled(wire.MethodToolsCall)

	gen := inst.Mutate(func(s *instance.Snapshot) {
		s.SetMethodEnabledForTest(wire.MethodToolsCall, false)
	})
	if gen != genBefore+1 {
		t.Fatalf("Mutate returned gen %d, want %d", gen, genBefore+1)
	}

	// The pointer captured before the mutation is unchanged.
	if before.Gen() != genBefore {
		t.Fatalf("stale snapshot Gen changed: got %d want %d", before.Gen(), genBefore)
	}
	if before.MethodEnabled(wire.MethodToolsCall) != methodBefore {
		t.Fatal("stale snapshot observed a half-applied mutation")
	}
	// The instance's current snapshot reflects the mutation.
	if inst.Snapshot().MethodEnabled(wire.MethodToolsCall) {
		t.Fatal("current snapshot did not observe the mutation")
	}
	if inst.Snapshot().Gen() != genBefore+1 {
		t.Fatal("current snapshot Gen did not increment")
	}
}

// TestGenIncrementsMonotonically asserts Gen increases by one on every mutation
// (acceptance criterion 3, MOCK-702 mechanics).
func TestGenIncrementsMonotonically(t *testing.T) {
	inst := newInstance(t, 3, "gen", true)
	prev := inst.Snapshot().Gen()
	for i := 0; i < 10; i++ {
		got := inst.Mutate(func(*instance.Snapshot) {})
		if got != prev+1 {
			t.Fatalf("mutation %d: gen %d, want %d", i, got, prev+1)
		}
		prev = got
	}
}

// TestCOWNoTornRead is the -race proof that a reader mid-iteration never observes
// a half-applied mutation. A reader repeatedly loads a snapshot and reads several
// fields; a writer concurrently mutates. Because each snapshot is immutable and
// published by a single atomic store, every read the reader takes must be
// internally consistent: the era is always non-empty and the gen it reads
// matches the snapshot it holds, never a mix.
func TestCOWNoTornRead(t *testing.T) {
	inst := newInstance(t, 4, "torn", true)
	var stop atomic.Bool
	var wg sync.WaitGroup

	// Writer: flips a method switch and bumps gen repeatedly.
	wg.Add(1)
	go func() {
		defer wg.Done()
		flip := false
		for !stop.Load() {
			flip = !flip
			f := flip
			inst.Mutate(func(s *instance.Snapshot) {
				s.SetMethodEnabledForTest(wire.MethodToolsCall, f)
			})
		}
	}()

	// Readers: each takes a snapshot and reads several fields, asserting the
	// snapshot is internally consistent. A single snapshot pointer must never
	// change under it (immutability), so reading the same field twice yields the
	// same answer.
	for r := 0; r < runtime.GOMAXPROCS(0)+2; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 5000; i++ {
				snap := inst.Snapshot()
				gen := snap.Gen()
				enabled := snap.MethodEnabled(wire.MethodToolsCall)
				// Re-read: an immutable snapshot must not change under us.
				if snap.Gen() != gen {
					t.Errorf("torn read: Gen changed within one snapshot")
					return
				}
				if snap.MethodEnabled(wire.MethodToolsCall) != enabled {
					t.Errorf("torn read: MethodEnabled changed within one snapshot")
					return
				}
				if snap.Era() == "" {
					t.Errorf("torn read: era empty on a published snapshot")
					return
				}
			}
		}()
	}
	time.Sleep(20 * time.Millisecond)
	stop.Store(true)
	wg.Wait()
}

// --- instance isolation ----------------------------------------------------

// TestInstanceIsolation asserts two instances with different scenarios do not
// interfere across the four axes ADR-007 names: config (snapshot), catalogue,
// journal and metrics/seed derivation.
func TestInstanceIsolation(t *testing.T) {
	// a: journal on, tools count 5, tools/call disabled.
	specA := scenario.InstanceSpec{
		Journal: &scenario.Journal{Enabled: boolp(true)},
		Switches: &scenario.Switches{Methods: map[string]scenario.MethodSwitch{
			wire.MethodToolsCall: {Enabled: boolp(false)},
		}},
		Catalog: &scenario.Catalog{Tools: &scenario.Generated{Count: intp(5)}},
	}
	// b: journal off, tools count 9, all methods enabled.
	specB := scenario.InstanceSpec{
		Journal: &scenario.Journal{Enabled: boolp(false)},
		Catalog: &scenario.Catalog{Tools: &scenario.Generated{Count: intp(9)}},
	}
	a := instance.New(instance.Config{Name: "alpha", Root: determinism.Root(10), Spec: specA})
	b := instance.New(instance.Config{Name: "beta", Root: determinism.Root(10), Spec: specB})

	// Config isolation: switches differ.
	if a.Snapshot().MethodEnabled(wire.MethodToolsCall) {
		t.Error("alpha tools/call should be disabled")
	}
	if !b.Snapshot().MethodEnabled(wire.MethodToolsCall) {
		t.Error("beta tools/call should be enabled")
	}

	// Catalogue isolation: counts differ and generated names differ despite the
	// same root seed, because each derives its own instance subtree by name.
	catA := a.Snapshot().Catalog()
	catB := b.Snapshot().Catalog()
	if catA.Len("tool") != 5 || catB.Len("tool") != 9 {
		t.Fatalf("catalogue lengths not isolated: a=%d b=%d", catA.Len("tool"), catB.Len("tool"))
	}
	// Generated names are index-based by design (invertible), so they match
	// across instances; the seed-dependent CONTENT (description) must differ,
	// proving each catalogue derives from its own instance subtree.
	if catA.At("tool", 0).Description == catB.At("tool", 0).Description {
		t.Error("two instances with different names produced identical generated content from the same root")
	}

	// Journal isolation: a has a ring, b does not.
	ringA, capA := a.Journal()
	ringB, _ := b.Journal()
	if ringA == nil || capA == nil {
		t.Error("alpha must have a journal ring and capturer")
	}
	if ringB != nil {
		t.Error("beta disabled its journal; ring must be nil")
	}

	// Seed isolation: derived instance keys differ.
	if a.InstanceKey() == b.InstanceKey() {
		t.Error("two instances derived identical seed subtrees from the same root")
	}
}

// TestSeedSubtreePerInstance asserts each instance derives its own seed subtree,
// and that the derivation is exactly root.Derive(DomainInstance, name)
// (acceptance criterion 6).
func TestSeedSubtreePerInstance(t *testing.T) {
	root := determinism.Root(42)
	inst := instance.New(instance.Config{Name: "named", Root: root, Spec: scenario.InstanceSpec{}})
	want := root.Derive(determinism.DomainInstance, []byte("named"))
	if inst.InstanceKey() != want {
		t.Fatal("instance key is not root.Derive(DomainInstance, name)")
	}
	other := instance.New(instance.Config{Name: "other", Root: root, Spec: scenario.InstanceSpec{}})
	if inst.InstanceKey() == other.InstanceKey() {
		t.Fatal("different names must derive different keys")
	}
}

// --- journal clear ---------------------------------------------------------

// TestClearJournal asserts clearing empties the journal (MOCK-702.7) and is a
// safe no-op on a journal-less instance.
func TestClearJournal(t *testing.T) {
	inst := newInstance(t, 5, "clear", true)
	ring, _ := inst.Journal()
	if ring == nil {
		t.Fatal("expected a ring")
	}
	// Drive a request so the ring holds a record.
	p := pipelineFor()
	sink := engine.NewBufferedSink()
	ex := &engine.Exchange{
		Ctx: context.Background(), Instance: inst, Transport: engine.KindHTTP,
		Peer: "p", Raw: rawRequest("1", wire.MethodToolsCall),
	}
	if err := p.Handle(ex.Ctx, ex, sink); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if ring.Len() == 0 {
		t.Fatal("expected a journalled record before clear")
	}
	inst.ClearJournal()
	if ring.Len() != 0 {
		t.Fatalf("journal not cleared: len=%d", ring.Len())
	}

	// No-op on a journal-less instance.
	jless := newInstance(t, 6, "jless", false)
	jless.ClearJournal() // must not panic
}

// --- zero goroutines at rest ----------------------------------------------

// TestZeroGoroutinesAtRest asserts an instance owns no goroutine at construction
// or after idleness (acceptance criterion 4). goleak in TestMain is the backstop;
// this test makes the assertion explicit and local by counting goroutines around
// construction of many instances.
func TestZeroGoroutinesAtRest(t *testing.T) {
	runtime.GC()
	before := runtime.NumGoroutine()
	insts := make([]*instance.Instance, 0, 50)
	for i := 0; i < 50; i++ {
		insts = append(insts, newInstance(t, uint64(100+i), "rest"+strconv.Itoa(i), true))
	}
	time.Sleep(10 * time.Millisecond) // idleness: a background goroutine would show now
	after := runtime.NumGoroutine()
	if after > before {
		t.Fatalf("instances started goroutines: before=%d after=%d", before, after)
	}
	runtime.KeepAlive(insts)
}

// Test200InstancesInOneProcess constructs ≥200 instances in one process and
// asserts each is independent and correctly registered (MOCK-904 mechanics). The
// per-instance MEMORY number is measured by BenchmarkInstanceMemory200; this test
// proves the count and the isolation at that count.
func Test200InstancesInOneProcess(t *testing.T) {
	const n = 200
	reg := instance.NewRegistry()
	insts := make([]*instance.Instance, 0, n)
	for i := 0; i < n; i++ {
		name := "svc" + strconv.Itoa(i)
		inst := instance.New(instance.Config{
			Name:      name,
			MountPath: "/mock/" + name + "/mcp",
			ID:        i,
			Root:      determinism.Root(1), // same root: names must still separate them
			Spec: scenario.InstanceSpec{
				Journal: &scenario.Journal{Enabled: boolp(true)},
				Catalog: &scenario.Catalog{Tools: &scenario.Generated{Count: intp(50)}},
			},
		})
		if !reg.Add(inst) {
			t.Fatalf("instance %d failed to register", i)
		}
		insts = append(insts, inst)
	}
	if reg.Len() != n {
		t.Fatalf("registered %d instances, want %d", reg.Len(), n)
	}
	// Every derived key is distinct: 200 distinct instance subtrees from one root.
	seen := make(map[determinism.Key]struct{}, n)
	for _, inst := range insts {
		k := inst.InstanceKey()
		if _, dup := seen[k]; dup {
			t.Fatal("two of 200 instances share a seed subtree")
		}
		seen[k] = struct{}{}
	}
	// Each has its own journal ring.
	for i, inst := range insts {
		ring, _ := inst.Journal()
		if ring == nil {
			t.Fatalf("instance %d has no ring", i)
		}
	}
	runtime.KeepAlive(insts)
}

// --- discover / journalEnabled surface ------------------------------------

// TestSnapshotSurface exercises the remaining engine.Snapshot surface and the
// scenario-to-snapshot mapping.
func TestSnapshotSurface(t *testing.T) {
	spec := scenario.InstanceSpec{
		Discover: &scenario.Discover{Instructions: strp("hi")},
		Journal:  &scenario.Journal{Enabled: boolp(false)},
	}
	inst := instance.New(instance.Config{Name: "surf", Root: determinism.Root(7), Spec: spec})
	s := inst.Snapshot()
	if s.JournalEnabled() {
		t.Error("journal disabled in spec but snapshot reports enabled")
	}
	if s.MetaValidator() == nil {
		t.Error("MetaValidator must never be nil")
	}
	if s.Discover() == nil || s.Discover().Instructions == nil {
		t.Error("discover config not carried onto snapshot")
	}
	// Default (absent journal block) is enabled.
	def := instance.New(instance.Config{Name: "def", Root: determinism.Root(8), Spec: scenario.InstanceSpec{}})
	if !def.Snapshot().JournalEnabled() {
		t.Error("absent journal block must default to enabled")
	}
}

// TestMetaValidatorInjection proves the DEF-005 injection path: a validator
// supplied on Config is exactly what the snapshot returns (so the facade can
// wire modern.NewMetaValidator through to the pipeline's stage-4 seam), that an
// absent validator falls back to the permissive engine.AcceptAllMeta (preserving
// the never-nil invariant for directly-constructed instances), and that a COW
// mutation carries the injected validator onto the new generation rather than
// resetting it.
func TestMetaValidatorInjection(t *testing.T) {
	// A sentinel validator whose identity we can recover: it rejects with a
	// distinctive fault so a caller can prove it, not AcceptAllMeta, is in force.
	var called atomic.Bool
	sentinel := engine.MetaValidatorFunc(
		func(_ context.Context, _ *engine.Exchange) (*engine.Fault, *journalapi.MetaValidationPart) {
			called.Store(true)
			return &engine.Fault{Code: -32602, HTTPStatus: 400, Message: "sentinel"}, nil
		},
	)

	inst := instance.New(instance.Config{
		Name:          "injected",
		Root:          determinism.Root(21),
		Spec:          scenario.InstanceSpec{},
		MetaValidator: sentinel,
	})
	got := inst.Snapshot().MetaValidator()
	if got == nil {
		t.Fatal("injected validator must never surface as nil")
	}
	fault, _ := got.ValidateMeta(context.Background(), &engine.Exchange{})
	if fault == nil || fault.Message != "sentinel" {
		t.Fatalf("snapshot returned a different validator than the one injected: fault=%v", fault)
	}
	if !called.Load() {
		t.Fatal("injected validator was not the one invoked")
	}

	// A COW mutation must carry the injected validator forward.
	inst.Mutate(func(_ *instance.Snapshot) {})
	fault2, _ := inst.Snapshot().MetaValidator().ValidateMeta(context.Background(), &engine.Exchange{})
	if fault2 == nil || fault2.Message != "sentinel" {
		t.Fatalf("mutation dropped the injected validator: fault=%v", fault2)
	}

	// No validator injected: the snapshot must fall back to AcceptAllMeta, which
	// accepts (nil fault) — the never-nil, permissive default.
	bare := instance.New(instance.Config{Name: "bare", Root: determinism.Root(22), Spec: scenario.InstanceSpec{}})
	bareV := bare.Snapshot().MetaValidator()
	if bareV == nil {
		t.Fatal("absent validator must fall back to a non-nil default")
	}
	if f, _ := bareV.ValidateMeta(context.Background(), &engine.Exchange{}); f != nil {
		t.Fatalf("default validator must accept, got fault %v", f)
	}
}
