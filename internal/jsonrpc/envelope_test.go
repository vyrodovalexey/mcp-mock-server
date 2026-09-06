package jsonrpc_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/vyrodovalexey/mcp-mock-server/internal/jsonrpc"
)

// TestDecodeRequestWellFormed covers a normal request decode: id, method and
// params are recovered and params bytes are preserved verbatim.
func TestDecodeRequestWellFormed(t *testing.T) {
	t.Parallel()
	const in = `{"jsonrpc":"2.0","id":7,"method":"tools/list","params":{"b":1,"a":2}}`
	req, err := jsonrpc.DecodeRequest([]byte(in))
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	if !req.HasVersion || req.Version != "2.0" {
		t.Errorf("version = %q present=%v", req.Version, req.HasVersion)
	}
	if req.Method != "tools/list" || !req.HasMethod {
		t.Errorf("method = %q present=%v", req.Method, req.HasMethod)
	}
	if string(req.ID.Raw()) != "7" {
		t.Errorf("id = %q, want 7", req.ID.Raw())
	}
	// params preserved with authored key order (MOCK-222.4).
	if string(req.Params) != `{"b":1,"a":2}` {
		t.Errorf("params = %q, want authored order preserved", req.Params)
	}
	if verr := req.Validate(); verr != nil {
		t.Errorf("Validate() = %v, want nil", verr)
	}
	if req.IsNotification() {
		t.Error("request with id must not be a notification")
	}
}

// TestDecodeRequestPathologies covers acceptance criterion 5 and the MOCK-503
// requirement that the envelope can *represent* pathological cases: missing
// jsonrpc, null id, absent method, notification.
func TestDecodeRequestPathologies(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name           string
		in             string
		wantNotif      bool
		wantHasVersion bool
		wantValidCode  int // 0 means Validate() should pass
	}{
		{
			name:           "missing jsonrpc",
			in:             `{"id":1,"method":"tools/list"}`,
			wantHasVersion: false,
			wantValidCode:  jsonrpc.CodeInvalidRequest,
		},
		{
			name:           "null id present",
			in:             `{"jsonrpc":"2.0","id":null,"method":"tools/list"}`,
			wantHasVersion: true,
			wantValidCode:  0,
		},
		{
			name:           "notification (no id)",
			in:             `{"jsonrpc":"2.0","method":"tools/list"}`,
			wantNotif:      true,
			wantHasVersion: true,
			wantValidCode:  0,
		},
		{
			name:           "absent method",
			in:             `{"jsonrpc":"2.0","id":1}`,
			wantHasVersion: true,
			wantValidCode:  jsonrpc.CodeInvalidRequest,
		},
		{
			name:           "wrong jsonrpc version",
			in:             `{"jsonrpc":"1.0","id":1,"method":"tools/list"}`,
			wantHasVersion: true,
			wantValidCode:  jsonrpc.CodeInvalidRequest,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req, err := jsonrpc.DecodeRequest([]byte(tc.in))
			if err != nil {
				t.Fatalf("DecodeRequest(%q) parse error: %v", tc.in, err)
			}
			if req.IsNotification() != tc.wantNotif {
				t.Errorf("IsNotification() = %v, want %v", req.IsNotification(), tc.wantNotif)
			}
			if req.HasVersion != tc.wantHasVersion {
				t.Errorf("HasVersion = %v, want %v", req.HasVersion, tc.wantHasVersion)
			}
			verr := req.Validate()
			switch {
			case tc.wantValidCode == 0 && verr != nil:
				t.Errorf("Validate() = %v, want nil", verr)
			case tc.wantValidCode != 0 && (verr == nil || verr.Code != tc.wantValidCode):
				t.Errorf("Validate() = %v, want code %d", verr, tc.wantValidCode)
			}
		})
	}
}

// TestDecodeRequestNullIdVsNotification is the sharpest MOCK-503 distinction:
// an explicit null id is not a notification.
func TestDecodeRequestNullIdVsNotification(t *testing.T) {
	t.Parallel()
	nullID, err := jsonrpc.DecodeRequest([]byte(`{"jsonrpc":"2.0","id":null,"method":"m"}`))
	if err != nil {
		t.Fatal(err)
	}
	if nullID.IsNotification() {
		t.Error("null id must not be treated as a notification")
	}
	if !nullID.ID.IsNull() {
		t.Error("id kind must be null")
	}
}

// TestDecodeRequestMalformed covers acceptance criterion 5: malformed envelopes
// yield -32700 with no panic, over truncated and over-nested input.
func TestDecodeRequestMalformed(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"truncated":        `{"jsonrpc":"2.0","id":1,`,
		"empty":            ``,
		"garbage":          `not json`,
		"trailing content": `{"jsonrpc":"2.0"} extra`,
		"deep nesting":     "[" + strings.Repeat("[", 100000),
		"unterminated str": `{"method":"abc`,
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			// Must not panic.
			req, err := jsonrpc.DecodeRequest([]byte(in))
			if err == nil {
				t.Fatalf("DecodeRequest(%q) = %+v, want parse error", in, req)
			}
			var rpcErr *jsonrpc.Error
			if !asRPCError(err, &rpcErr) {
				t.Fatalf("error %v is not *jsonrpc.Error", err)
			}
			if rpcErr.Code != jsonrpc.CodeParseError {
				t.Errorf("code = %d, want %d (-32700)", rpcErr.Code, jsonrpc.CodeParseError)
			}
		})
	}
}

// TestResponseEchoesIDByteForByte covers acceptance criterion 1 on the response
// side: the id is echoed exactly, preserving its JSON type.
func TestResponseEchoesIDByteForByte(t *testing.T) {
	t.Parallel()
	cases := []string{`1`, `"1"`, `1.0`, `null`, `"abc-def"`}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			t.Parallel()
			id, err := jsonrpc.RawID([]byte(in))
			if err != nil {
				t.Fatal(err)
			}
			resp := jsonrpc.NewResultResponse(id, json.RawMessage(`{"ok":true}`))
			out, err := resp.Encode()
			if err != nil {
				t.Fatal(err)
			}
			want := `{"jsonrpc":"2.0","id":` + in + `,"result":{"ok":true}}`
			if string(out) != want {
				t.Errorf("response = %s\nwant       %s", out, want)
			}
		})
	}
}

// TestResponsePreservesAuthoredKeyOrder covers acceptance criterion 4: response
// emission preserves input key order rather than canonicalising it.
func TestResponsePreservesAuthoredKeyOrder(t *testing.T) {
	t.Parallel()
	// Non-alphabetical authored key order that a canonicaliser would reorder.
	authored := json.RawMessage(`{"zebra":1,"apple":2,"mango":3}`)
	resp := jsonrpc.NewResultResponse(jsonrpc.StringID("x"), authored)
	out, err := resp.Encode()
	if err != nil {
		t.Fatal(err)
	}
	want := `{"jsonrpc":"2.0","id":"x","result":{"zebra":1,"apple":2,"mango":3}}`
	if string(out) != want {
		t.Errorf("response reordered authored keys:\n got %s\nwant %s", out, want)
	}
}

// TestResponseNoHTMLEscaping covers that <, > and & in an authored body survive
// verbatim in a response (byte-stability).
func TestResponseNoHTMLEscaping(t *testing.T) {
	t.Parallel()
	authored := json.RawMessage(`{"html":"a<b>c&d"}`)
	resp := jsonrpc.NewResultResponse(jsonrpc.StringID("x"), authored)
	out, err := resp.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `a<b>c&d`) {
		t.Errorf("HTML characters were escaped: %s", out)
	}
	if strings.Contains(string(out), `\u003c`) {
		t.Errorf("response contains \\u003c escape: %s", out)
	}
}

// TestErrorResponse covers the error envelope shape and that data is preserved
// as raw JSON (feeding TASK-010's -32602 data.missing).
func TestErrorResponse(t *testing.T) {
	t.Parallel()
	e := &jsonrpc.Error{
		Code:    jsonrpc.CodeInvalidParams,
		Message: "invalid params",
		Data:    json.RawMessage(`{"missing":["protocolVersion"]}`),
	}
	resp := jsonrpc.NewErrorResponse(mustRawID(t, `5`), e)
	out, err := resp.Encode()
	if err != nil {
		t.Fatal(err)
	}
	want := `{"jsonrpc":"2.0","id":5,"error":{"code":-32602,"message":"invalid params","data":{"missing":["protocolVersion"]}}}`
	if string(out) != want {
		t.Errorf("error response =\n %s\nwant %s", out, want)
	}
	if e.Error() == "" {
		t.Error("Error() must be non-empty")
	}
}

// TestErrorImplementsError checks NewError and the error string contains code.
func TestErrorImplementsError(t *testing.T) {
	t.Parallel()
	e := jsonrpc.NewError(jsonrpc.CodeMethodNotFound, "nope")
	if !strings.Contains(e.Error(), "-32601") {
		t.Errorf("Error() = %q, want it to contain the code", e.Error())
	}
}
