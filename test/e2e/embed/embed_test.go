//go:build e2e

// Package embed_test is the out-of-module consumer of mcpmock required by
// TASK-028. It lives in its own module (see go.mod) with a replace directive to
// the parent working tree, and exercises the public surface (mcpmock,
// journalapi, assert, scenario) exactly as the gateway's own test suite would.
package embed_test

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"

	mcpmock "github.com/vyrodovalexey/mcp-mock-server"
	"github.com/vyrodovalexey/mcp-mock-server/assert"
	"github.com/vyrodovalexey/mcp-mock-server/journalapi"

	"github.com/prometheus/client_golang/prometheus"
)

// startupBudget is the MOCK-107 whole-binary startup budget.
const startupBudget = 200 * time.Millisecond

// TestE2E_EmbedDriveAndAssert is TC-028.1: start a server via StartTest, drive a
// real tools/list request with plain net/http, and assert over the journal with
// the assert helpers — the canonical hub-style usage MOCK-107 promises
// (107.3, 603.3, 603.4).
func TestE2E_EmbedDriveAndAssert(t *testing.T) {
	srv := mcpmock.StartTest(t,
		mcpmock.WithSeed(42),
		mcpmock.WithAddr("127.0.0.1:0"),
	)

	inst, ok := srv.Instance("default")
	if !ok {
		t.Fatalf("default instance missing")
	}
	if inst.URL() == "" {
		t.Fatalf("instance URL is empty; expected an ephemeral loopback address")
	}

	resp := drive(t, inst.URL(), toolsListBody(1), nil)
	if resp.Error != nil {
		t.Fatalf("tools/list returned JSON-RPC error %d: %s", resp.Error.Code, resp.Error.Message)
	}
	if len(resp.Result) == 0 {
		t.Fatalf("tools/list returned no result")
	}

	// Assert over the journal using the public assert package, exactly as the
	// gateway's suite would. This is the MOCK-603 assertion surface driven from
	// an external module.
	view := inst.Journal()
	if view.Len() < 1 {
		t.Fatalf("journal empty after driving a request")
	}

	a := assert.T(t, view)
	// 603.3-style count assertion, scoped by method.
	a.WhereMethod("tools/list").AssertRequestCount(journalapi.Selector{Method: "tools/list"}, 1)
	// 603.3 credential-safety form: a header we never sent must be absent.
	a.AssertNoHeader("X-Nonexistent-Token")

	// 603.4: assertions work against a recorded NDJSON journal with no server
	// running. Export via the journalapi NDJSON writer, then load with
	// assert.FromReader.
	var buf bytes.Buffer
	w := journalapi.NewWriter(&buf)
	view.Iter(func(r journalapi.Record) bool {
		if err := w.Write(r); err != nil {
			t.Fatalf("write ndjson record: %v", err)
		}
		return true
	})
	if err := w.Close(); err != nil {
		t.Fatalf("close ndjson writer: %v", err)
	}
	fileView, err := assert.FromReader(&buf)
	if err != nil {
		t.Fatalf("assert.FromReader over exported journal: %v", err)
	}
	if err := assert.New(fileView).
		WhereMethod("tools/list").
		CheckRequestCount(journalapi.Selector{Method: "tools/list"}, 1); err != nil {
		t.Fatalf("recorded-journal assertion failed: %v", err)
	}
}

// TestE2E_StartupUnderBudget is TC-028.2: measure New+Start from OUTSIDE the
// module and assert it lands within the MOCK-107 200 ms budget.
func TestE2E_StartupUnderBudget(t *testing.T) {
	// Warm the once-per-process schema compile so it is not charged to a
	// measured start (MOCK-701.7); this mirrors what a real binary amortises.
	warm, err := mcpmock.New(mcpmock.WithSeed(1))
	if err != nil {
		t.Fatalf("warm New: %v", err)
	}
	_ = warm.Close()

	const runs = 20
	var worst, total time.Duration
	for i := 0; i < runs; i++ {
		start := time.Now()
		s, err := mcpmock.New(mcpmock.WithSeed(uint64(i)), mcpmock.WithAddr("127.0.0.1:0"))
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if err := s.Start(context.Background()); err != nil {
			t.Fatalf("Start: %v", err)
		}
		elapsed := time.Since(start)
		_ = s.Close()
		total += elapsed
		if elapsed > worst {
			worst = elapsed
		}
	}
	mean := total / runs
	t.Logf("MOCK-107 startup from external module over %d runs: mean=%v worst=%v budget=%v",
		runs, mean, worst, startupBudget)
	if worst > startupBudget {
		t.Errorf("worst startup %v exceeds MOCK-107 budget %v", worst, startupBudget)
	}
}

// TestE2E_TwoServersParallel is TC-028.3: two independent Servers run under
// t.Parallel() with their own registry, logger, seed and port, proving they
// share no process-global state and do not collide on Prometheus registration
// (107.4).
func TestE2E_TwoServersParallel(t *testing.T) {
	names := []string{"alpha", "beta"}
	urls := make([]string, len(names))
	var mu = make(chan struct{}, 1)
	mu <- struct{}{}

	for i, name := range names {
		i, name := i, name
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			// Each server gets its OWN Prometheus registry: if the facade
			// touched the process-global default registerer, two of these in one
			// binary would panic with a duplicate-registration error.
			reg := prometheus.NewRegistry()
			srv := mcpmock.StartTest(t,
				mcpmock.WithSeed(uint64(100+i)),
				mcpmock.WithAddr("127.0.0.1:0"),
				mcpmock.WithRegisterer(reg),
			)
			inst, ok := srv.Instance("default")
			if !ok {
				t.Fatalf("[%s] default instance missing", name)
			}
			resp := drive(t, inst.URL(), toolsListBody(1), nil)
			if resp.Error != nil {
				t.Fatalf("[%s] tools/list error: %d %s", name, resp.Error.Code, resp.Error.Message)
			}
			// Each instance journals only its own request.
			if got := inst.Journal().Len(); got != 1 {
				t.Fatalf("[%s] journal len = %d, want 1 (cross-server leakage?)", name, got)
			}
			<-mu
			urls[i] = inst.URL()
			mu <- struct{}{}
		})
	}
	t.Cleanup(func() {
		if urls[0] != "" && urls[0] == urls[1] {
			t.Errorf("two servers shared a URL %q; they are not independent", urls[0])
		}
	})
}

// TestE2E_StartTestNoGoroutineLeak is TC-028.4: a StartTest server leaks no
// goroutine into the host. The assertion is enforced process-wide by
// goleak.VerifyTestMain in main_test.go; this test provides the workload
// (start, drive, let t.Cleanup close). A leak here fails the whole package.
func TestE2E_StartTestNoGoroutineLeak(t *testing.T) {
	srv := mcpmock.StartTest(t,
		mcpmock.WithSeed(7),
		mcpmock.WithAddr("127.0.0.1:0"),
	)
	inst, ok := srv.Instance("default")
	if !ok {
		t.Fatalf("default instance missing")
	}
	resp := drive(t, inst.URL(), toolsListBody(1), nil)
	if resp.Error != nil {
		t.Fatalf("tools/list error: %d %s", resp.Error.Code, resp.Error.Message)
	}
	// Intentionally do NOT call Close here: StartTest registered it with
	// t.Cleanup. goleak (TestMain) verifies that after Cleanup runs, no mcpmock
	// goroutine survives — the property MOCK-107.7 promises a host suite.
}

// TestE2E_NoStdoutOnImportOrOperation is TC-028.5: importing the root package
// (which loads its init()s) and running a full server lifecycle writes NOTHING
// to os.Stdout. The stdout hijack lives in cmd/mcpmock and is installed only by
// an explicit Install call; a library consumer must never see it (ADR-011).
func TestE2E_NoStdoutOnImportOrOperation(t *testing.T) {
	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = origStdout })

	// Full lifecycle under the redirected stdout.
	srv, err := mcpmock.New(mcpmock.WithSeed(3), mcpmock.WithAddr("127.0.0.1:0"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := srv.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	inst, ok := srv.Instance("default")
	if !ok {
		t.Fatalf("default instance missing")
	}
	resp := drive(t, inst.URL(), toolsListBody(1), nil)
	if resp.Error != nil {
		t.Fatalf("tools/list error: %d %s", resp.Error.Code, resp.Error.Message)
	}
	if err := srv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Restore stdout and close the write end so the read side sees EOF.
	os.Stdout = origStdout
	if err := w.Close(); err != nil {
		t.Fatalf("close pipe writer: %v", err)
	}
	captured, err := readAllWithDeadline(t, r)
	if err != nil {
		t.Fatalf("read captured stdout: %v", err)
	}
	if len(captured) != 0 {
		t.Errorf("mcpmock wrote %d bytes to os.Stdout during normal operation; "+
			"an implicit hijack or stray write corrupts a host binary: %q",
			len(captured), captured)
	}
}

// TestE2E_ControlSurfaceUsesNoInternalType is TC-028.6: exercise every public
// method on mcpmock.Control / mcpmock.InstanceControl, binding each return value
// to a consumer-named variable. This file COMPILING from outside the parent
// module is the proof that no internal/ type appears in a public signature the
// consumer must name (107.3). If it did, the build would fail with "use of
// internal package not allowed".
func TestE2E_ControlSurfaceUsesNoInternalType(t *testing.T) {
	srv := mcpmock.StartTest(t,
		mcpmock.WithSeed(5),
		mcpmock.WithAddr("127.0.0.1:0"),
	)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Every type named below is either a public-package type or an mcpmock
	// re-export alias — never an internal/ path.
	var ctrl mcpmock.Control = srv.Control()

	var infos []mcpmock.InstanceInfo
	infos, err := ctrl.Instances(ctx)
	if err != nil {
		t.Fatalf("Control.Instances: %v", err)
	}
	if len(infos) == 0 {
		t.Fatalf("no instances reported")
	}

	var info mcpmock.InstanceInfo
	info, err = ctrl.Instance(ctx, "default")
	if err != nil {
		t.Fatalf("Control.Instance: %v", err)
	}
	var ji mcpmock.JournalInfo = info.Journal
	_ = ji.Records

	var seed mcpmock.SeedInfo
	seed, err = ctrl.Seed(ctx)
	if err != nil {
		t.Fatalf("Control.Seed: %v", err)
	}
	if seed.Seed != 5 {
		t.Errorf("effective seed = %d, want 5", seed.Seed)
	}

	var health mcpmock.Health
	health, err = ctrl.Health(ctx)
	if err != nil {
		t.Fatalf("Control.Health: %v", err)
	}
	if health.Instances < 1 {
		t.Errorf("health reports %d instances, want >=1", health.Instances)
	}

	var ic mcpmock.InstanceControl = ctrl.For("default")
	page, err := ic.Journal(ctx, journalapi.Query{})
	if err != nil {
		t.Fatalf("InstanceControl.Journal: %v", err)
	}
	_ = page

	// Also exercise the Instance facade's InstanceControl.
	inst, ok := srv.Instance("default")
	if !ok {
		t.Fatalf("default instance missing")
	}
	var ic2 mcpmock.InstanceControl = inst.Control()
	if err := ic2.ClearJournal(ctx); err != nil {
		t.Fatalf("InstanceControl.ClearJournal: %v", err)
	}
}

// TestE2E_RegistererSatisfiableByExternalPrometheus is TC-028.8: an external
// consumer with its own require on prometheus/client_golang can use
// Server.Registerer() and WithRegisterer without a version conflict.
func TestE2E_RegistererSatisfiableByExternalPrometheus(t *testing.T) {
	reg := prometheus.NewRegistry()
	srv := mcpmock.StartTest(t,
		mcpmock.WithSeed(9),
		mcpmock.WithAddr("127.0.0.1:0"),
		mcpmock.WithRegisterer(reg),
	)

	// The public return type is prometheus.Registerer, an interface from the
	// consumer's OWN prometheus module. If the versions conflicted, this
	// assignment would not type-check.
	var r prometheus.Registerer = srv.Registerer()
	c := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "embed_consumer_probe_total",
		Help: "probe that an external consumer can register on the server's registerer",
	})
	if err := r.Register(c); err != nil {
		t.Fatalf("register consumer collector on Server.Registerer(): %v", err)
	}
	c.Inc()
}
