//go:build functional

package functional_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/vyrodovalexey/mcp-mock-server/test/mcpclient"
)

// mock_102_handlers_test.go — the three Phase 1 handlers over both transports,
// and their byte-equality (MOCK-102.1–102.4, MOCK-201, MOCK-202). Driven only
// through test/mcpclient (ADR-017).

// TestMOCK201_Discover_ResultShape drives server/discover over HTTP and asserts
// the result is a non-error envelope carrying resultType "discovery" and
// _meta.serverInfo, against the hand-authored golden (MOCK-201, MOCK-209.1/.2).
func TestMOCK201_Discover_ResultShape(t *testing.T) {
	t.Parallel()
	_, inst := newHTTPServer(t)
	c := newHTTPClient(t, inst)
	resp, err := c.Discover(ctx(t), mcpclient.IntID(1))
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	requireResult(t, resp)
	assertGolden(t, "MOCK-201/discover.json", resp.Body)
}

// TestMOCK202_ToolsList_BuiltinOrder proves tools/list returns the three
// built-ins in the fixed order echo, sleep, fail with their advertised
// inputSchemas (MOCK-202, builtin-tools 202.20), against the golden.
func TestMOCK202_ToolsList_BuiltinOrder(t *testing.T) {
	t.Parallel()
	_, inst := newHTTPServer(t)
	c := newHTTPClient(t, inst)
	resp, err := c.ListTools(ctx(t), mcpclient.IntID(1))
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	requireResult(t, resp)
	assertGolden(t, "MOCK-202/tools_list.json", resp.Body)

	// Independently of the golden, assert the ORDER explicitly so a reordering
	// is named as such (builtin-tools 1.1 [D]).
	names := toolNames(t, resp.Body)
	want := []string{"echo", "sleep", "fail"}
	if len(names) != len(want) {
		t.Fatalf("tools/list: got %d tools %v, want %v", len(names), names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("tools/list order: got %v, want %v", names, want)
		}
	}
}

// TestMOCK202_ToolsCall_Echo proves the echo happy path (MOCK-202, 202.10).
func TestMOCK202_ToolsCall_Echo(t *testing.T) {
	t.Parallel()
	_, inst := newHTTPServer(t)
	c := newHTTPClient(t, inst)
	resp, err := c.CallTool(ctx(t), mcpclient.IntID(1), "echo", map[string]any{"message": "hi"})
	if err != nil {
		t.Fatalf("echo: %v", err)
	}
	requireResult(t, resp)
	assertGolden(t, "MOCK-202/echo_message.json", resp.Body)
}

// TestMOCK202_ToolsCall_Sleep proves the sleep happy path at durationMs 0
// (MOCK-202, 202.13).
func TestMOCK202_ToolsCall_Sleep(t *testing.T) {
	t.Parallel()
	_, inst := newHTTPServer(t)
	c := newHTTPClient(t, inst)
	resp, err := c.CallTool(ctx(t), mcpclient.IntID(1), "sleep", map[string]any{"durationMs": 0})
	if err != nil {
		t.Fatalf("sleep: %v", err)
	}
	requireResult(t, resp)
	assertGolden(t, "MOCK-202/sleep_zero.json", resp.Body)
}

// TestMOCK202_ToolsCall_Fail proves the fail default toolError path: HTTP 200, a
// RESULT (not error) with isError:true (MOCK-202, 202.17).
func TestMOCK202_ToolsCall_Fail(t *testing.T) {
	t.Parallel()
	_, inst := newHTTPServer(t)
	c := newHTTPClient(t, inst)
	resp, err := c.CallTool(ctx(t), mcpclient.IntID(1), "fail", nil)
	if err != nil {
		t.Fatalf("fail: %v", err)
	}
	requireResult(t, resp) // a toolError is a SUCCESSFUL envelope (result present)
	assertGolden(t, "MOCK-202/fail_default.json", resp.Body)
}

// TestMOCK102_TransportByteEquality is the load-bearing MOCK-102.3/102.4 test:
// the SAME JSON-RPC payload produces BYTE-IDENTICAL response bodies over HTTP and
// stdio at a fixed seed. It compares raw bytes (Response.Body), which is exactly
// what test/mcpclient exposes for this purpose.
func TestMOCK102_TransportByteEquality(t *testing.T) {
	t.Parallel()
	_, httpInst := newHTTPServer(t)
	httpC := newHTTPClient(t, httpInst)
	_, stdioC := stdioPair(t)

	cases := []struct {
		name string
		call func(c *mcpclient.Client) (*mcpclient.Response, error)
	}{
		{"discover", func(c *mcpclient.Client) (*mcpclient.Response, error) {
			return c.Discover(ctx(t), mcpclient.IntID(1))
		}},
		{"tools/list", func(c *mcpclient.Client) (*mcpclient.Response, error) {
			return c.ListTools(ctx(t), mcpclient.IntID(1))
		}},
		{"echo", func(c *mcpclient.Client) (*mcpclient.Response, error) {
			return c.CallTool(ctx(t), mcpclient.IntID(1), "echo", map[string]any{"message": "hi"})
		}},
		{"sleep", func(c *mcpclient.Client) (*mcpclient.Response, error) {
			return c.CallTool(ctx(t), mcpclient.IntID(1), "sleep", map[string]any{"durationMs": 0})
		}},
		{"fail", func(c *mcpclient.Client) (*mcpclient.Response, error) {
			return c.CallTool(ctx(t), mcpclient.IntID(1), "fail", nil)
		}},
	}
	for _, tc := range cases {
		hResp, err := tc.call(httpC)
		if err != nil {
			t.Fatalf("http %s: %v", tc.name, err)
		}
		sResp, err := tc.call(stdioC)
		if err != nil {
			t.Fatalf("stdio %s: %v", tc.name, err)
		}
		if !bytes.Equal(hResp.Body, sResp.Body) {
			t.Fatalf("%s: HTTP and stdio bodies differ (MOCK-102.4):\nhttp:  %s\nstdio: %s",
				tc.name, hResp.Body, sResp.Body)
		}
	}
}

// TestMOCK202_MethodNotFound proves an unknown method returns -32601 (MOCK-202).
func TestMOCK202_MethodNotFound(t *testing.T) {
	t.Parallel()
	_, inst := newHTTPServer(t)
	c := newHTTPClient(t, inst)
	req := completeCallInfo(mcpclient.NewRequest(mcpclient.IntID(1), "nonsense/method", nil))
	resp, err := c.Do(ctx(t), req)
	if err != nil {
		t.Fatalf("unknown method: %v", err)
	}
	requireRPCError(t, resp, mcpclient.ErrCodeMethodNotFound)
}

// --- shared response helpers ---

// requireResult fails unless resp is a non-error JSON-RPC result envelope.
func requireResult(t *testing.T, resp *mcpclient.Response) {
	t.Helper()
	if !resp.GotResponse {
		t.Fatalf("no response produced")
	}
	if resp.ParseErr != nil {
		t.Fatalf("response is not valid JSON: %v\nbody: %s", resp.ParseErr, resp.Body)
	}
	if resp.Envelope.Error != nil {
		t.Fatalf("expected result, got error %d %q\nbody: %s",
			resp.Envelope.Error.Code, resp.Envelope.Error.Message, resp.Body)
	}
	if len(resp.Envelope.Result) == 0 {
		t.Fatalf("expected a result member, got none\nbody: %s", resp.Body)
	}
}

// requireRPCError fails unless resp is a JSON-RPC error with the given code.
func requireRPCError(t *testing.T, resp *mcpclient.Response, code int) {
	t.Helper()
	if !resp.GotResponse || resp.ParseErr != nil {
		t.Fatalf("expected error envelope, got no/invalid response: %s", resp.Body)
	}
	if resp.Envelope.Error == nil {
		t.Fatalf("expected error code %d, got result\nbody: %s", code, resp.Body)
	}
	if resp.Envelope.Error.Code != code {
		t.Fatalf("expected error code %d, got %d (%q)\nbody: %s",
			code, resp.Envelope.Error.Code, resp.Envelope.Error.Message, resp.Body)
	}
}

// toolNames extracts result.tools[].name from a tools/list body in wire order.
func toolNames(t *testing.T, body []byte) []string {
	t.Helper()
	var env struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode tools/list: %v\nbody: %s", err, body)
	}
	out := make([]string, 0, len(env.Result.Tools))
	for _, tool := range env.Result.Tools {
		out = append(out, tool.Name)
	}
	return out
}
