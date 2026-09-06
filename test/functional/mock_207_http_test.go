//go:build functional

package functional_test

import (
	"bytes"
	"io"
	"net/http"
	"testing"

	"github.com/vyrodovalexey/mcp-mock-server/journalapi"
	"github.com/vyrodovalexey/mcp-mock-server/test/mcpclient"
)

// mock_207_http_test.go — HTTP-method and session-header behaviour (MOCK-207).
// These need direct net/http control (the status code, the method, response
// headers), so they use the standard library directly rather than the mcpclient
// convenience wrappers — still no internal/ import.

// TestMOCK207_GetDelete405 asserts GET and DELETE on the MCP path return HTTP 405
// with Allow: POST (207.1).
func TestMOCK207_GetDelete405(t *testing.T) {
	t.Parallel()
	_, inst := newHTTPServer(t)
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		req, err := http.NewRequestWithContext(ctx(t), method, inst.URL(), nil)
		if err != nil {
			t.Fatalf("%s build: %v", method, err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s do: %v", method, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("%s: status %d, want 405", method, resp.StatusCode)
		}
		if allow := resp.Header.Get("Allow"); allow != http.MethodPost {
			t.Fatalf("%s: Allow=%q, want POST", method, allow)
		}
	}
}

// TestMOCK207_SessionHeadersIgnoredAndJournaled sends a normal POST carrying
// Mcp-Session-Id and Last-Event-ID and asserts: the request is processed
// normally (207.2/.3), the two header values are journaled verbatim, and NO
// response carries an Mcp-Session-Id (207.4 — no session id minted).
func TestMOCK207_SessionHeadersIgnoredAndJournaled(t *testing.T) {
	t.Parallel()
	_, inst := newHTTPServer(t)
	c := newHTTPClient(t, inst)

	r := mcpclient.ToolsListRequest(mcpclient.IntID(1))
	completeCallInfo(r)
	r.Headers = []mcpclient.Header{
		{Name: "Mcp-Session-Id", Value: "sess-should-be-ignored"},
		{Name: "Last-Event-ID", Value: "42"},
	}
	resp, err := c.Do(ctx(t), r)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	requireResult(t, resp) // processed normally despite the session headers

	// The value is journaled (207.2/.3).
	if !journalHasHeaderValue(inst.Journal(), "Mcp-Session-Id", "sess-should-be-ignored") {
		t.Fatalf("Mcp-Session-Id not journaled (207.2)")
	}
	if !journalHasHeaderValue(inst.Journal(), "Last-Event-Id", "42") {
		t.Fatalf("Last-Event-ID not journaled (207.3)")
	}

	// No session id minted on the response (207.4). Re-issue over raw net/http
	// so we can read the response headers.
	assertNoResponseSessionHeader(t, inst.URL(), r)
}

// assertNoResponseSessionHeader POSTs the request over raw net/http and asserts
// the response carries no Mcp-Session-Id header (207.4).
func assertNoResponseSessionHeader(t *testing.T, url string, r *mcpclient.Request) {
	t.Helper()
	body, err := r.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req, err := http.NewRequestWithContext(ctx(t), http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	req.Header.Set(mcpclient.HeaderContentType, mcpclient.ContentTypeJSON)
	for _, h := range r.Headers {
		req.Header.Add(h.Name, h.Value)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	if got := resp.Header.Get("Mcp-Session-Id"); got != "" {
		t.Fatalf("response minted Mcp-Session-Id=%q (207.4 forbids it)", got)
	}
}

// assertHTTPStatus POSTs the request's raw bytes and asserts the HTTP status
// equals want. It is the transport-layer half of MOCK-203.1 (the -32602 body is
// asserted separately via mcpclient).
func assertHTTPStatus(t *testing.T, url string, r *mcpclient.Request, want int) {
	t.Helper()
	body, err := r.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req, err := http.NewRequestWithContext(ctx(t), http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	req.Header.Set(mcpclient.HeaderContentType, mcpclient.ContentTypeJSON)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != want {
		t.Fatalf("HTTP status = %d, want %d", resp.StatusCode, want)
	}
}

// journalHasHeaderValue reports whether any journal record carries a request
// header whose name matches (case-insensitively via the stored casing) and whose
// value equals value.
func journalHasHeaderValue(v journalapi.View, name, value string) bool {
	var found bool
	v.Iter(func(rec journalapi.Record) bool {
		if rec.HTTP == nil {
			return true
		}
		for _, h := range rec.HTTP.Headers {
			if eqFold(h[0], name) && h[1] == value {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

// eqFold is a tiny ASCII case-insensitive compare, avoiding a strings import
// churn for one call site.
func eqFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}
