package mcpmock_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	mcpmock "github.com/vyrodovalexey/mcp-mock-server"
	"github.com/vyrodovalexey/mcp-mock-server/test/mcpclient"
)

// selfcheck_hide_e2e_test.go is the DEF-010/DEF-011 end-to-end proof that both
// switches are settable FROM A SCENARIO FILE and take effect through the
// assembled facade — the same shape as the DEF-005 meta_e2e_test.go proof for
// switches.validateMeta. Unit coverage of the resolvers lives in
// internal/instance; unit coverage of the handler behaviour lives in
// internal/modern. What these tests add is the file→switch→wire path.

// codeInternalError is the JSON-RPC -32603 a self-check rejection carries
// (MOCK-201.4). It is transcribed here rather than imported, keeping this a
// black-box facade test.
const codeInternalError = -32603

// startFromFile boots a server from a scenario file and returns the named
// instance's URL, cleaning up on test end.
func startFromFile(t *testing.T, file, instance string) string {
	t.Helper()
	s, err := mcpmock.NewFromFile(file, mcpmock.WithSeed(7), mcpmock.WithAddr("127.0.0.1:0"))
	if err != nil {
		t.Fatalf("NewFromFile(%s): %v", file, err)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start(%s): %v", file, err)
	}
	t.Cleanup(func() { _ = s.Close() })
	inst, ok := s.Instance(instance)
	if !ok {
		t.Fatalf("instance %q not found in %s", instance, file)
	}
	return inst.URL()
}

// TestHideFromCapabilitiesFromScenarioFile proves DEF-011 end to end: a scenario
// file setting switches.methods.tools/list.hideFromCapabilities removes exactly
// the tools/list key from the advertised capabilities, while the other advertised
// capabilities survive verbatim — and tools/list still ANSWERS (it is enabled),
// proving hide is independent of disable.
func TestHideFromCapabilitiesFromScenarioFile(t *testing.T) {
	url := startFromFile(t, "testdata/selfcheck-hide.yaml", "selfcheck-hide")

	// server/discover: inspect the advertised capabilities.
	_, env := postThroughFacade(t, url, mcpclient.DiscoverRequest(mcpclient.IntID(1)))
	if env.Error != nil {
		t.Fatalf("discover errored: %+v", env.Error)
	}
	caps := capabilitiesOf(t, env.Result)

	if _, ok := caps["tools/list"]; ok {
		t.Errorf("tools/list should be hidden from capabilities: %s", env.Result)
	}
	for _, k := range []string{"server/discover", "tools/call", "extensions"} {
		if _, ok := caps[k]; !ok {
			t.Errorf("capability %q wrongly removed: %s", k, env.Result)
		}
	}

	// Independence: tools/list is hidden but ENABLED, so it still answers.
	statusList, listEnv := postThroughFacade(t, url, mcpclient.ToolsListRequest(mcpclient.IntID(2)))
	if statusList != http.StatusOK || listEnv.Error != nil {
		t.Errorf("hidden-but-enabled tools/list must still answer, got status=%d err=%+v",
			statusList, listEnv.Error)
	}
}

// TestSelfCheckFromScenarioFilePassesWellFormed proves selfCheck is settable from
// a file and does not disturb a conformant result: with selfCheck on, a normal
// discover still returns 200 with a well-formed result.
func TestSelfCheckFromScenarioFilePassesWellFormed(t *testing.T) {
	url := startFromFile(t, "testdata/selfcheck-hide.yaml", "selfcheck-hide")
	status, env := postThroughFacade(t, url, mcpclient.DiscoverRequest(mcpclient.IntID(1)))
	if status != http.StatusOK || env.Error != nil {
		t.Fatalf("selfCheck-on well-formed discover must be 200/no-error, got status=%d err=%+v",
			status, env.Error)
	}
	if len(env.Result) == 0 {
		t.Fatalf("expected a result, got none: %+v", env)
	}
}

// TestSelfCheckFromScenarioFileRejectsMalformed proves DEF-010 end to end and
// from a file: a scenario setting selfCheck AND omitResultType produces a result
// with no resultType discriminator, and the self-check rejects it with -32603 —
// the mock refusing to emit its own malformed response (MOCK-244 principle,
// MOCK-201.4 mechanism). The client sees the error, never the malformed result.
func TestSelfCheckFromScenarioFileRejectsMalformed(t *testing.T) {
	url := startFromFile(t, "testdata/selfcheck-catch.yaml", "selfcheck-catch")
	status, env := postThroughFacade(t, url, mcpclient.DiscoverRequest(mcpclient.IntID(1)))

	if env.Error == nil {
		t.Fatalf("self-check must reject a discriminator-less result, got result: %s", env.Result)
	}
	if env.Error.Code != codeInternalError {
		t.Errorf("self-check rejection code = %d, want %d (-32603)", env.Error.Code, codeInternalError)
	}
	if status != http.StatusInternalServerError {
		t.Errorf("self-check rejection HTTP status = %d, want 500", status)
	}
	if len(env.Result) != 0 {
		t.Errorf("malformed result must not be emitted alongside the error: %s", env.Result)
	}
}

// capabilitiesOf decodes the capabilities object from a discover result body.
func capabilitiesOf(t *testing.T, result json.RawMessage) map[string]json.RawMessage {
	t.Helper()
	var res struct {
		Capabilities json.RawMessage `json:"capabilities"`
	}
	if err := json.Unmarshal(result, &res); err != nil {
		t.Fatalf("decode discover result: %v (%s)", err, result)
	}
	var caps map[string]json.RawMessage
	if len(res.Capabilities) > 0 {
		if err := json.Unmarshal(res.Capabilities, &caps); err != nil {
			t.Fatalf("capabilities not an object: %v (%s)", err, res.Capabilities)
		}
	}
	return caps
}
