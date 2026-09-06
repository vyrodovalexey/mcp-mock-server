//go:build functional

package functional_test

import (
	"bytes"
	"testing"

	mcpmock "github.com/vyrodovalexey/mcp-mock-server"
	"github.com/vyrodovalexey/mcp-mock-server/test/mcpclient"
)

// mock_704_determinism_test.go — determinism (MOCK-704, §0.1). Same scenario +
// same seed ⇒ BYTE-IDENTICAL responses; a different seed differs. Asserted on
// raw bytes (Response.Body), which is what makes this a real wire-level test and
// not a decode-then-compare tautology.

// requestScript is the fixed sequence of requests the determinism test replays
// against each server. It deliberately includes the seed-sensitive tools/list
// (generated names would differ by seed) and the seed-independent echo (which is
// pure), so the same-seed comparison and the different-seed comparison both have
// something to bite on.
func requestScript(t *testing.T, c *mcpclient.Client) [][]byte {
	t.Helper()
	bodies := make([][]byte, 0, 4)
	add := func(resp *mcpclient.Response, err error, what string) {
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		if !resp.GotResponse {
			t.Fatalf("%s: no response", what)
		}
		bodies = append(bodies, resp.Body)
	}
	dResp, err := c.Discover(ctx(t), mcpclient.IntID(1))
	add(dResp, err, "discover")
	lResp, err := c.ListTools(ctx(t), mcpclient.IntID(2))
	add(lResp, err, "tools/list")
	eResp, err := c.CallTool(ctx(t), mcpclient.IntID(3), "echo", map[string]any{"a": 1, "b": "two"})
	add(eResp, err, "echo")
	return bodies
}

// runScript starts a fresh server at seed and returns the raw response bytes of
// the fixed request script.
func runScript(t *testing.T, seed uint64) [][]byte {
	t.Helper()
	s := mcpmock.StartTest(t, mcpmock.WithSeed(seed))
	inst := s.Instances()[0]
	c := newHTTPClient(t, inst)
	return requestScript(t, c)
}

// TestMOCK704_SameSeedByteIdentical asserts two independent servers built with
// the SAME seed produce byte-identical responses for the same request script
// (MOCK-704, §0.1). Two fresh servers, not one server twice, so a hidden
// per-server RNG state would surface.
func TestMOCK704_SameSeedByteIdentical(t *testing.T) {
	t.Parallel()
	const seed = 0xA5A5A5A5
	a := runScript(t, seed)
	b := runScript(t, seed)
	if len(a) != len(b) {
		t.Fatalf("script lengths differ: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if !bytes.Equal(a[i], b[i]) {
			t.Fatalf("same-seed response %d differs (MOCK-704 / §0.1):\nA: %s\nB: %s", i, a[i], b[i])
		}
	}
}

// TestMOCK704_DifferentSeedDiffers asserts a different seed yields a different
// response on at least one seed-dependent call (tools/list generated names). If
// two different seeds produced identical output on every call, the seed would be
// doing nothing — the opposite failure MOCK-704 must also guard.
//
// The built-in catalogue is fixed regardless of seed, so this test configures a
// GENERATED catalogue whose names ARE seed-derived, giving the seed something to
// affect. It then compares tools/list bytes across two seeds.
func TestMOCK704_DifferentSeedDiffers(t *testing.T) {
	t.Parallel()
	genCat := generatedCatalogueOverlay(t, 8)

	sA := mcpmock.StartTest(t, mcpmock.WithSeed(1), mcpmock.WithOverlay(genCat))
	sB := mcpmock.StartTest(t, mcpmock.WithSeed(2), mcpmock.WithOverlay(genCat))
	cA := newHTTPClient(t, sA.Instances()[0])
	cB := newHTTPClient(t, sB.Instances()[0])

	rA, err := cA.ListTools(ctx(t), mcpclient.IntID(1))
	if err != nil {
		t.Fatalf("seed 1 tools/list: %v", err)
	}
	rB, err := cB.ListTools(ctx(t), mcpclient.IntID(1))
	if err != nil {
		t.Fatalf("seed 2 tools/list: %v", err)
	}
	if bytes.Equal(rA.Body, rB.Body) {
		t.Fatalf("different seeds produced identical generated tools/list — seed has no effect (MOCK-704)\nbody: %s", rA.Body)
	}

	// And same-seed still identical with the generated catalogue.
	sB2 := mcpmock.StartTest(t, mcpmock.WithSeed(1), mcpmock.WithOverlay(genCat))
	cB2 := newHTTPClient(t, sB2.Instances()[0])
	rB2, err := cB2.ListTools(ctx(t), mcpclient.IntID(1))
	if err != nil {
		t.Fatalf("seed 1 (again) tools/list: %v", err)
	}
	if !bytes.Equal(rA.Body, rB2.Body) {
		t.Fatalf("same seed produced different generated tools/list (MOCK-704 / §0.1)\nA:  %s\nB2: %s", rA.Body, rB2.Body)
	}
}
