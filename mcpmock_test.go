package mcpmock_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"

	mcpmock "github.com/vyrodovalexey/mcp-mock-server"
	"github.com/vyrodovalexey/mcp-mock-server/internal/config"
	"github.com/vyrodovalexey/mcp-mock-server/scenario"
	"github.com/vyrodovalexey/mcp-mock-server/test/mcpclient"
)

// TestMain runs the whole facade suite under goleak so any goroutine a Server
// starts and fails to reap on Close fails the suite rather than silently
// leaking into a host test binary — the MOCK-107.7 property the facade must
// hold for the hub's test suite (contracts/library-api.md §6).
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// --- construction and lifecycle ---

// TestNewDoesNoIO asserts New binds no listener and starts no goroutine
// (MOCK-107.6, acceptance criterion 2): a Server constructed and never started
// exposes no URL and leaks nothing. goleak (TestMain) covers the goroutine half.
func TestNewDoesNoIO(t *testing.T) {
	s, err := mcpmock.New(mcpmock.WithSeed(1))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	inst := s.Instances()
	if len(inst) != 1 {
		t.Fatalf("Instances: got %d, want 1", len(inst))
	}
	if url := inst[0].URL(); url != "" {
		t.Errorf("URL before Start: got %q, want empty (no listener bound)", url)
	}
	// A Server never started still closes cleanly.
	if err := s.Close(); err != nil {
		t.Errorf("Close of unstarted server: %v", err)
	}
}

// TestStartTestUsableAddressAndCleanup verifies StartTest returns a Server whose
// instance URL is dialable and that cleanup tears it down (acceptance
// criterion 4). The registered cleanup runs at test end; goleak then confirms
// nothing leaked.
func TestStartTestUsableAddressAndCleanup(t *testing.T) {
	s := mcpmock.StartTest(t, mcpmock.WithSeed(7))
	inst, ok := s.Instance("default")
	if !ok {
		t.Fatalf("Instance(default): not found")
	}
	if !strings.HasPrefix(inst.URL(), "http://127.0.0.1:") {
		t.Fatalf("URL: got %q, want an ephemeral loopback address", inst.URL())
	}
	// Drive one request end to end so the address is proven usable, not merely
	// well-formed.
	cl, err := mcpclient.NewHTTP(inst.URL())
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	defer func() { _ = cl.Close() }()
	resp, err := cl.ListTools(context.Background(), mcpclient.IntID(1))
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if !resp.GotResponse || resp.Envelope.Error != nil {
		t.Fatalf("ListTools: unexpected response: got=%v err=%v", resp.GotResponse, resp.Envelope.Error)
	}
	// No explicit Close: t.Cleanup(Close) registered by StartTest must reap it.
}

// TestStartTestNoCloseLeaksNothing is the MOCK-107.7 case: a test that starts a
// Server via StartTest and never closes it leaks no goroutine, because the
// registered cleanup closes it. It runs as a subtest so its cleanup fires and
// goleak (TestMain) sees a clean process at the end.
func TestStartTestNoCloseLeaksNothing(t *testing.T) {
	t.Run("started-never-closed", func(t *testing.T) {
		s := mcpmock.StartTest(t, mcpmock.WithSeed(11))
		if s.Seed() != 11 {
			t.Fatalf("Seed: got %d, want 11", s.Seed())
		}
		// deliberately no Close; cleanup must handle it.
	})
}

// TestStartStopCyclesLeakFree runs several start/stop cycles and relies on
// goleak (TestMain) to assert none of them leaks a goroutine — the clean
// shutdown contract of §6.
func TestStartStopCyclesLeakFree(t *testing.T) {
	for i := 0; i < 5; i++ {
		s, err := mcpmock.New(mcpmock.WithSeed(uint64(i)), mcpmock.WithAddr("127.0.0.1:0"))
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if err := s.Start(context.Background()); err != nil {
			t.Fatalf("Start: %v", err)
		}
		if err := s.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}
}

// TestLifecycleSentinels checks the lifecycle guards: Start after Close returns
// ErrClosed, and a double Start is a no-op (idempotent), and double Close is
// harmless — all matchable with errors.Is (acceptance criterion 8).
func TestLifecycleSentinels(t *testing.T) {
	s, err := mcpmock.New(mcpmock.WithSeed(1), mcpmock.WithAddr("127.0.0.1:0"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Errorf("second Start should be a no-op, got %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("second Close should be harmless, got %v", err)
	}
	if err := s.Start(context.Background()); !errors.Is(err, mcpmock.ErrClosed) {
		t.Errorf("Start after Close: got %v, want ErrClosed", err)
	}
}

// --- seed / determinism ---

// TestSeedRandomWhenAbsent asserts two Servers built without WithSeed get
// different seeds (MOCK-704.1), and WithSeed pins the seed.
func TestSeedRandomWhenAbsent(t *testing.T) {
	a, err := mcpmock.New()
	if err != nil {
		t.Fatalf("New a: %v", err)
	}
	defer func() { _ = a.Close() }()
	b, err := mcpmock.New()
	if err != nil {
		t.Fatalf("New b: %v", err)
	}
	defer func() { _ = b.Close() }()
	if a.Seed() == b.Seed() {
		t.Errorf("two seedless Servers share a seed %d; expected distinct random seeds", a.Seed())
	}

	c, err := mcpmock.New(mcpmock.WithSeed(0xABCDEF))
	if err != nil {
		t.Fatalf("New c: %v", err)
	}
	defer func() { _ = c.Close() }()
	if c.Seed() != 0xABCDEF {
		t.Errorf("WithSeed: got %d, want %d", c.Seed(), 0xABCDEF)
	}
}

// --- two facades in one process (ADR-007, MOCK-107.4) ---

// TestTwoServersParallel runs two independent Servers under t.Parallel() and
// asserts they share no state: each has its own registerer, its own seed, and
// each answers on its own address without interfering (acceptance criterion 3).
func TestTwoServersParallel(t *testing.T) {
	run := func(name string, seed uint64) func(t *testing.T) {
		return func(t *testing.T) {
			t.Parallel()
			s := mcpmock.StartTest(t, mcpmock.WithSeed(seed))
			inst, _ := s.Instance("default")
			cl, err := mcpclient.NewHTTP(inst.URL())
			if err != nil {
				t.Fatalf("%s client: %v", name, err)
			}
			defer func() { _ = cl.Close() }()
			for i := 0; i < 20; i++ {
				resp, err := cl.ListTools(context.Background(), mcpclient.IntID(int64(i)))
				if err != nil {
					t.Fatalf("%s ListTools: %v", name, err)
				}
				if !resp.GotResponse {
					t.Fatalf("%s ListTools: no response", name)
				}
			}
			if s.Seed() != seed {
				t.Errorf("%s seed: got %d, want %d", name, s.Seed(), seed)
			}
		}
	}
	t.Run("server-a", run("a", 100))
	t.Run("server-b", run("b", 200))
}

// TestTwoServersDistinctRegisterers proves the no-globals property directly: two
// Servers built in one process each get their own Prometheus registerer, and
// neither panics on construction (the duplicate-registration failure mode ADR-007
// guards against).
func TestTwoServersDistinctRegisterers(t *testing.T) {
	a, err := mcpmock.New(mcpmock.WithSeed(1))
	if err != nil {
		t.Fatalf("New a: %v", err)
	}
	defer func() { _ = a.Close() }()
	b, err := mcpmock.New(mcpmock.WithSeed(2))
	if err != nil {
		t.Fatalf("New b: %v", err)
	}
	defer func() { _ = b.Close() }()
	if a.Registerer() == b.Registerer() {
		t.Errorf("two Servers share a registerer; ADR-007 requires a private registry each")
	}
}

// --- multiple logical instances on distinct paths (MOCK-103) ---

// TestMultipleInstancesDistinctPaths builds a Fleet with three instances on
// distinct mount paths and drives each independently through test/mcpclient,
// asserting each answers on its own URL (MOCK-103). It exercises the shared
// listener + prefix router (ADR-007 §3) through the facade.
func TestMultipleInstancesDistinctPaths(t *testing.T) {
	doc := fleetDocument(t, []fleetEntry{
		{name: "alpha", mount: "/mock/alpha/mcp"},
		{name: "bravo", mount: "/mock/bravo/mcp"},
		{name: "charlie", mount: "/mock/charlie/mcp"},
	})
	s, err := mcpmock.NewFromScenario(doc, mcpmock.WithSeed(42), mcpmock.WithAddr("127.0.0.1:0"))
	if err != nil {
		t.Fatalf("NewFromScenario: %v", err)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if got := len(s.Instances()); got != 3 {
		t.Fatalf("Instances: got %d, want 3", got)
	}

	for _, name := range []string{"alpha", "bravo", "charlie"} {
		inst, ok := s.Instance(name)
		if !ok {
			t.Fatalf("Instance(%s): not found", name)
		}
		if inst.URL() == "" {
			t.Fatalf("Instance(%s): empty URL", name)
		}
		cl, err := mcpclient.NewHTTP(inst.URL())
		if err != nil {
			t.Fatalf("%s client: %v", name, err)
		}
		resp, err := cl.ListTools(context.Background(), mcpclient.IntID(1))
		_ = cl.Close()
		if err != nil {
			t.Fatalf("%s ListTools: %v", name, err)
		}
		if !resp.GotResponse || resp.Envelope.Error != nil {
			t.Fatalf("%s ListTools: bad response err=%v", name, resp.Envelope.Error)
		}
		// The request was journalled by exactly this instance.
		if n := inst.Journal().Len(); n == 0 {
			t.Errorf("%s journal empty after a request", name)
		}
	}

	// Cross-instance isolation: bravo's journal did not capture alpha's traffic
	// beyond what bravo itself served.
	alpha, _ := s.Instance("alpha")
	bravo, _ := s.Instance("bravo")
	if alpha.Journal().Len() != bravo.Journal().Len() {
		// Each got exactly one request above, so counts match; a mismatch means
		// routing crossed instances.
		t.Errorf("journal counts differ across isolated instances: alpha=%d bravo=%d",
			alpha.Journal().Len(), bravo.Journal().Len())
	}
}

// --- stdio mode does not touch os.Stdout (ADR-011 hijack seam) ---

// TestStdioUsesInjectedWriterOnly proves the stdio transport writes protocol
// frames only to the injected writer and never to os.Stdout, preserving the
// cmd/mcpmock hijack seam (deliverable item 8). It drives a request over an
// in-memory pipe pair and asserts the response arrives on the injected writer.
func TestStdioUsesInjectedWriterOnly(t *testing.T) {
	// clientToServer carries the client's requests to the server's reader;
	// serverToClient carries the server's frames to the client's reader.
	crIn, cwIn := io.Pipe()   // server reads crIn, client writes cwIn
	crOut, cwOut := io.Pipe() // client reads crOut, server writes cwOut

	s, err := mcpmock.New(mcpmock.WithSeed(5), mcpmock.WithStdio(crIn, cwOut))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		_ = cwIn.Close()
		_ = s.Close()
		_ = crOut.Close()
	})

	cl, err := mcpclient.NewStdio(mcpclient.StdioConfig{Reader: crOut, Writer: cwIn})
	if err != nil {
		t.Fatalf("stdio client: %v", err)
	}
	t.Cleanup(func() { _ = cl.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := cl.ListTools(ctx, mcpclient.IntID(1))
	if err != nil {
		t.Fatalf("stdio ListTools: %v", err)
	}
	if !resp.GotResponse {
		t.Fatalf("stdio ListTools: no response on the injected writer")
	}
	if resp.Envelope.Error != nil {
		t.Fatalf("stdio ListTools: error response: %v", resp.Envelope.Error)
	}
	// The response body arrived on the injected pipe (crOut), which is not
	// os.Stdout: the facade never handed os.Stdout to the transport. The
	// AST-level guarantee (no os.Stdout assignment outside cmd/) is TASK-021's
	// scan; here we prove the behavioural half — frames flow through the
	// injected writer.
}

// --- graceful shutdown with an in-flight request ---

// TestGracefulShutdownInFlight starts a request that blocks in the sleep tool,
// then shuts down with a deadline. The in-flight request drains (its context is
// canceled) and Close returns without leaking a goroutine (goleak, TestMain).
func TestGracefulShutdownInFlight(t *testing.T) {
	s := mcpmock.StartTest(t, mcpmock.WithSeed(9))
	inst, _ := s.Instance("default")
	cl, err := mcpclient.NewHTTP(inst.URL())
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	defer func() { _ = cl.Close() }()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// A short sleep that is in flight when Shutdown is called: graceful
		// shutdown drains it within the deadline rather than dropping it.
		_, _ = cl.CallTool(ctx, mcpclient.IntID(1), "sleep", map[string]any{"durationMs": 300})
	}()

	// Give the request time to reach the handler, then shut down gracefully.
	time.Sleep(100 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Errorf("Shutdown with in-flight request: %v", err)
	}
	wg.Wait()
}

// --- validation error semantics ---

// TestValidationErrorWrapsSentinel checks that a bad scenario surfaces as
// ErrValidation (errors.Is) while remaining errors.As-recoverable to the
// loader's detailed error (acceptance criterion 8, contracts/library-api.md §5).
func TestValidationErrorWrapsSentinel(t *testing.T) {
	bad := &scenario.Document{
		APIVersion: "wrong/version",
		Kind:       scenario.KindScenario,
		Metadata:   &scenario.Metadata{Name: "x"},
	}
	_, err := mcpmock.NewFromScenario(bad)
	if err == nil {
		t.Fatalf("expected a validation error for a bad apiVersion")
	}
	if !errors.Is(err, mcpmock.ErrValidation) {
		t.Errorf("error does not wrap ErrValidation: %v", err)
	}
	var ve *config.ValidationError
	if !errors.As(err, &ve) {
		t.Errorf("error not recoverable to *config.ValidationError: %v", err)
	}
}

// TestWithoutValidationSkips confirms WithoutValidation lets an otherwise-invalid
// document through construction (the startup-budget escape hatch). We use a
// document the schema would reject (missing spec is tolerated by the Go decode
// path) but that still builds a usable instance.
func TestWithoutValidationSkips(t *testing.T) {
	// A minimal doc with no spec: schema requires spec for a Scenario, so
	// validation would reject it; WithoutValidation skips that.
	doc := &scenario.Document{
		APIVersion: scenario.APIVersion,
		Kind:       scenario.KindScenario,
		Metadata:   &scenario.Metadata{Name: "novalidate"},
	}
	s, err := mcpmock.NewFromScenario(doc, mcpmock.WithoutValidation(), mcpmock.WithSeed(1))
	if err != nil {
		t.Fatalf("NewFromScenario with WithoutValidation: %v", err)
	}
	defer func() { _ = s.Close() }()
	if _, ok := s.Instance("novalidate"); !ok {
		t.Errorf("instance not built under WithoutValidation")
	}
}

// --- journal option: WithoutJournal ---

// TestWithoutJournalDisablesCapture asserts WithoutJournal yields an empty
// journal even after a request (MOCK-901 measurement mode).
func TestWithoutJournalDisablesCapture(t *testing.T) {
	s := mcpmock.StartTest(t, mcpmock.WithSeed(3), mcpmock.WithoutJournal())
	inst, _ := s.Instance("default")
	cl, err := mcpclient.NewHTTP(inst.URL())
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	defer func() { _ = cl.Close() }()
	if _, err := cl.ListTools(context.Background(), mcpclient.IntID(1)); err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if n := inst.Journal().Len(); n != 0 {
		t.Errorf("journal should be empty with WithoutJournal, got %d records", n)
	}
}

// fleetEntry names one instance in a test Fleet document.
type fleetEntry struct {
	name  string
	mount string
}

// fleetDocument builds a valid Fleet scenario document with the given instances.
// It marshals a JSON spec so the mountPath and names round-trip through the
// scenario types exactly as the loader would decode them.
func fleetDocument(t *testing.T, entries []fleetEntry) *scenario.Document {
	t.Helper()
	insts := make([]scenario.FleetInstance, 0, len(entries))
	for _, e := range entries {
		insts = append(insts, scenario.FleetInstance{Name: e.name, MountPath: e.mount})
	}
	specBytes, err := json.Marshal(scenario.FleetSpec{Instances: insts})
	if err != nil {
		t.Fatalf("marshal fleet spec: %v", err)
	}
	return &scenario.Document{
		APIVersion: scenario.APIVersion,
		Kind:       scenario.KindFleet,
		Metadata:   &scenario.Metadata{Name: "fleet"},
		Spec:       specBytes,
	}
}
