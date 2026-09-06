//go:build functional

// Package functional_test is the Phase 1 functional suite (TASK-027). It drives
// an in-process mcpmock.Server ONLY through test/mcpclient (the ADR-017
// independent client) and the public facade/control surface, and asserts on raw
// wire bytes. No test in this package imports any internal/ package — that is
// the anti-circularity rule (test-strategy.md §0, ADR-017), and it is what makes
// a green run evidence rather than a tautology.
//
// The suite runs against the PROVISIONAL 2026-07-28 wire behaviour (GAP-003
// open): its golden files encode [P]-labelled annex items and are marked as such
// (see testdata/golden/README.md). A green suite is a regression contract, NOT a
// protocol-conformance claim.
package functional_test

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	mcpmock "github.com/vyrodovalexey/mcp-mock-server"
	"github.com/vyrodovalexey/mcp-mock-server/scenario"
	"github.com/vyrodovalexey/mcp-mock-server/test/mcpclient"
)

// sharedBinPath is the path to the mcpmock CLI binary built once for the whole
// package by TestMain. It is empty until TestMain builds it. Building once and
// removing it in TestMain guarantees no leaked binary or temp dir, which the
// per-test sync.Once form could not (it had no place to register cleanup).
var sharedBinPath string

// TestMain builds the mcpmock CLI once (for the CLI exit-code tests) and removes
// it afterward, so the suite leaks no process, temp file or directory (TASK-027
// resource-lifecycle rule). It fails the whole package if the build fails — a
// broken CLI is a defect the CLI tests must not silently skip over.
func TestMain(m *testing.M) {
	code := runWithSharedBin(m)
	os.Exit(code)
}

// runWithSharedBin builds the CLI into a temp dir, runs the package, and cleans
// up the temp dir unconditionally (even if a test panics, the deferred RemoveAll
// runs). It returns the test exit code.
func runWithSharedBin(m *testing.M) int {
	dir, err := os.MkdirTemp("", "mcpmock-func-bin-*")
	if err != nil {
		panic("functional TestMain: mkdir temp: " + err.Error())
	}
	defer func() { _ = os.RemoveAll(dir) }()

	out := filepath.Join(dir, "mcpmock")
	build := exec.Command("go", "build", "-o", out, "../../cmd/mcpmock")
	if b, bErr := build.CombinedOutput(); bErr != nil {
		panic("functional TestMain: build mcpmock CLI: " + bErr.Error() + "\n" + string(b))
	}
	sharedBinPath = out
	return m.Run()
}

// fixedSeed is the seed every determinism-sensitive test pins so responses are
// byte-stable (MOCK-704, §0.1). A single shared value keeps goldens comparable
// across tests that expect the same seed-derived output.
const fixedSeed = 0x00C0FFEE

// opTimeout bounds every client operation. It is a generous ceiling that fails a
// hung server without ever being used as a timing assertion — this suite asserts
// on CONDITIONS, never on elapsed wall-clock (the five prior flaky defects came
// from timing/parallelism assumptions; this suite adds no sixth).
const opTimeout = 10 * time.Second

// newHTTPServer starts a fixed-seed HTTP server on an ephemeral loopback port and
// returns it with its single default instance. Cleanup is registered on tb.
func newHTTPServer(tb testing.TB, opts ...mcpmock.Option) (*mcpmock.Server, *mcpmock.Instance) {
	tb.Helper()
	all := append([]mcpmock.Option{mcpmock.WithSeed(fixedSeed)}, opts...)
	s := mcpmock.StartTest(tb, all...)
	insts := s.Instances()
	if len(insts) == 0 {
		tb.Fatalf("server has no instances")
	}
	return s, insts[0]
}

// newHTTPClient builds an mcpclient over the instance URL with cleanup
// registered. It is the ADR-017 independent client; the whole suite drives the
// server through it.
func newHTTPClient(tb testing.TB, inst *mcpmock.Instance) *mcpclient.Client {
	tb.Helper()
	c, err := mcpclient.NewHTTP(inst.URL())
	if err != nil {
		tb.Fatalf("mcpclient.NewHTTP(%q): %v", inst.URL(), err)
	}
	tb.Cleanup(func() { _ = c.Close() })
	return c
}

// ctx returns a bounded context with cleanup-registered cancel. Every operation
// in the suite is deadline-bounded so a hang fails the test rather than hanging
// CI.
func ctx(tb testing.TB) context.Context {
	tb.Helper()
	c, cancel := context.WithTimeout(context.Background(), opTimeout)
	tb.Cleanup(cancel)
	return c
}

// stdioPair wires an in-process stdio client to a server over two io.Pipe pairs,
// following the ADR-011 seam: the server reads the client's writes and writes to
// the client's reader, and the facade never touches os.Stdout. It returns the
// started server and a connected stdio client. Cleanup closes the client's write
// pipe FIRST (so the server's Serve loop sees EOF and shutdown does not block on
// a blocked read), then the server, then the remaining pipe ends.
func stdioPair(tb testing.TB, opts ...mcpmock.Option) (*mcpmock.Server, *mcpclient.Client) {
	tb.Helper()
	srvIn, cliW := io.Pipe()  // server reads srvIn; client writes cliW
	cliR, srvOut := io.Pipe() // client reads cliR; server writes srvOut

	all := append([]mcpmock.Option{mcpmock.WithSeed(fixedSeed), mcpmock.WithStdio(srvIn, srvOut)}, opts...)
	s, err := mcpmock.New(all...)
	if err != nil {
		tb.Fatalf("New(stdio): %v", err)
	}
	if err := s.Start(context.Background()); err != nil {
		tb.Fatalf("Start(stdio): %v", err)
	}
	c, err := mcpclient.NewStdio(mcpclient.StdioConfig{Reader: cliR, Writer: cliW})
	if err != nil {
		tb.Fatalf("mcpclient.NewStdio: %v", err)
	}
	tb.Cleanup(func() {
		_ = cliW.Close() // signal EOF to the server's stdin so Serve returns
		_ = s.Close()
		_ = c.Close()
		_ = cliR.Close()
		_ = srvIn.Close()
		_ = srvOut.Close()
	})
	return s, c
}

// completeCallInfo attaches a stable clientInfo to a request built by hand so its
// _meta is complete (the method helpers do this automatically; a raw Request does
// not). A hand-built request that keeps a stable clientInfo is required for
// byte-stable goldens.
func completeCallInfo(r *mcpclient.Request) *mcpclient.Request {
	if r.Meta.ClientInfo == nil && !r.Meta.OmitClientInfo {
		r.Meta.ClientInfo = &mcpclient.ClientInfo{Name: "mcpmock-test-client", Version: "0"}
	}
	return r
}

// switchesOverlay builds a single-instance scenario overlay whose spec carries
// the given switches, so a test can turn on omitResultType / omitServerInfoMeta
// / validateMeta programmatically without an on-disk file. It is composed over
// the default scenario by WithOverlay. Switches is a pointer field on the typed
// InstanceSpec, so the overlay merges cleanly onto the default document.
func switchesOverlay(tb testing.TB, sw scenario.Switches) *scenario.Document {
	tb.Helper()
	specBytes, err := json.Marshal(scenario.InstanceSpec{Switches: &sw})
	if err != nil {
		tb.Fatalf("marshal overlay spec: %v", err)
	}
	return &scenario.Document{
		APIVersion: "mcpmock.dev/v1alpha1",
		Kind:       "Scenario",
		Spec:       specBytes,
	}
}

// boolPtr / strPtr / intPtr are the absent-vs-zero helpers the scenario config
// uses.
func boolPtr(b bool) *bool    { return &b }
func strPtr(s string) *string { return &s }
func intPtr(n int) *int       { return &n }

// generatedCatalogueOverlay builds an overlay whose catalogue generates n tools,
// so the tool names are seed-derived (ADR-004) and therefore differ across seeds
// — giving the different-seed determinism test something the seed actually
// affects. It suppresses the built-ins (an authored/generated catalogue does).
func generatedCatalogueOverlay(tb testing.TB, n int) *scenario.Document {
	tb.Helper()
	spec := scenario.InstanceSpec{
		Catalog: &scenario.Catalog{
			Tools: &scenario.Generated{Count: intPtr(n)},
		},
	}
	specBytes, err := json.Marshal(spec)
	if err != nil {
		tb.Fatalf("marshal catalogue overlay: %v", err)
	}
	return &scenario.Document{
		APIVersion: "mcpmock.dev/v1alpha1",
		Kind:       "Scenario",
		Spec:       specBytes,
	}
}
