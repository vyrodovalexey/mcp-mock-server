package httpx_test

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/vyrodovalexey/mcp-mock-server/internal/transport/httpx"
	"github.com/vyrodovalexey/mcp-mock-server/internal/wire"
)

// newServer starts a server on an ephemeral loopback port with the given mounts,
// registers cleanup, and returns it plus its base URL.
func newServer(t *testing.T, mounts ...*httpx.Mount) (*httpx.Server, string) {
	t.Helper()
	srv, err := httpx.New(httpx.Config{Addr: "127.0.0.1:0", Mounts: mounts})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	go func() {
		if err := srv.Serve(); err != nil && err != http.ErrServerClosed {
			t.Errorf("Serve: %v", err)
		}
	}()
	t.Cleanup(func() {
		http.DefaultClient.CloseIdleConnections()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
	return srv, "http://" + srv.Addr().String()
}

// post issues a raw POST and returns status and body.
func post(t *testing.T, url string, body []byte) (int, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// TestPostAcceptedAtConfiguredPath asserts POST is accepted at the configured
// path and the response echoes the handler result (MOCK-102.2). It also proves
// the path is configurable — /mcp here, a custom path in the routing test.
func TestPostAcceptedAtConfiguredPath(t *testing.T) {
	mount := newMount(1, "inst", httpx.DefaultPath, echoHandler(`{"ok":true}`))
	_, base := newServer(t, mount)

	status, body := post(t, base+httpx.DefaultPath, rawRequest("1", wire.MethodToolsCall))
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", status, body)
	}
	var env struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("unmarshal response: %v; body=%s", err, body)
	}
	if string(env.Result) != `{"ok":true}` {
		t.Fatalf("result = %s, want {\"ok\":true}", env.Result)
	}
}

// TestNonStreamingRequestAddsNoGoroutine asserts acceptance criterion 3: a
// non-streaming request is handled on net/http's connection goroutine and adds
// none of its own. It compares the goroutine count after warm-up to the count
// after a burst of requests, allowing only net/http's own per-connection
// goroutines (which the burst reuses via keep-alive).
func TestNonStreamingRequestAddsNoGoroutine(t *testing.T) {
	mount := newMount(1, "inst", httpx.DefaultPath, echoHandler(`{"ok":true}`))
	_, base := newServer(t, mount)

	client := &http.Client{}
	t.Cleanup(client.CloseIdleConnections)
	// Warm up one connection so net/http's goroutines exist before we measure.
	for i := 0; i < 5; i++ {
		if status, _ := postWith(t, client, base+httpx.DefaultPath, rawRequest("1", wire.MethodToolsCall)); status != http.StatusOK {
			t.Fatalf("warmup status = %d", status)
		}
	}
	runtime.GC()
	time.Sleep(20 * time.Millisecond)
	before := runtime.NumGoroutine()

	for i := 0; i < 200; i++ {
		if status, _ := postWith(t, client, base+httpx.DefaultPath, rawRequest("1", wire.MethodToolsCall)); status != http.StatusOK {
			t.Fatalf("request %d status = %d", i, status)
		}
	}
	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	after := runtime.NumGoroutine()

	// The engine adds zero goroutines per request; the only growth allowed is
	// net/http's fixed per-connection overhead, well under this slack.
	if after-before > 8 {
		t.Fatalf("goroutine count grew by %d over 200 requests (before=%d after=%d); the request path must add no goroutine",
			after-before, before, after)
	}
}

// postWith issues a POST on a specific client (keep-alive reuse) and returns
// status and body.
func postWith(t *testing.T, client *http.Client, url string, body []byte) (int, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// TestMethodPolicy is the MOCK-207 method table: GET and DELETE (and any
// non-POST) MUST return 405; POST is accepted.
func TestMethodPolicy(t *testing.T) {
	mount := newMount(1, "inst", httpx.DefaultPath, echoHandler(`{"ok":true}`))
	_, base := newServer(t, mount)

	cases := []struct {
		method string
		want   int
	}{
		{http.MethodGet, http.StatusMethodNotAllowed},
		{http.MethodDelete, http.StatusMethodNotAllowed},
		{http.MethodPut, http.StatusMethodNotAllowed},
		{http.MethodPatch, http.StatusMethodNotAllowed},
		{http.MethodPost, http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.method, func(t *testing.T) {
			req, err := http.NewRequestWithContext(context.Background(), tc.method,
				base+httpx.DefaultPath, strings.NewReader(string(rawRequest("1", wire.MethodToolsCall))))
			if err != nil {
				t.Fatalf("build request: %v", err)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("do: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != tc.want {
				t.Fatalf("%s status = %d, want %d", tc.method, resp.StatusCode, tc.want)
			}
			if tc.want == http.StatusMethodNotAllowed {
				if got := resp.Header.Get("Allow"); got != http.MethodPost {
					t.Fatalf("Allow = %q, want POST", got)
				}
			}
		})
	}
}

// TestSessionHeadersIgnoredAndNoSessionMinted asserts MOCK-207 statelessness:
// Mcp-Session-Id and Last-Event-ID are ignored (the request is served exactly as
// if they were absent) and NO Mcp-Session-Id is minted on the response.
func TestSessionHeadersIgnoredAndNoSessionMinted(t *testing.T) {
	mount := newMount(1, "inst", httpx.DefaultPath, echoHandler(`{"ok":true}`))
	_, base := newServer(t, mount)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		base+httpx.DefaultPath, strings.NewReader(string(rawRequest("1", wire.MethodToolsCall))))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Mcp-Session-Id", "should-be-ignored")
	req.Header.Set("Last-Event-ID", "42")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (session headers must be ignored, not rejected)", resp.StatusCode)
	}
	// No session id minted in modern mode: the response carries no
	// Mcp-Session-Id header of any casing.
	for name := range resp.Header {
		if strings.EqualFold(name, "Mcp-Session-Id") {
			t.Fatalf("response minted a session id header %q; modern mode is stateless (MOCK-207)", name)
		}
	}
}

// TestPrefixRoutingToDistinctInstances asserts MOCK-103: distinct paths route to
// distinct instances on one listener, each producing its own instance's result.
func TestPrefixRoutingToDistinctInstances(t *testing.T) {
	a := newMount(1, "alpha", "/alpha/mcp", echoHandler(`{"who":"alpha"}`))
	b := newMount(2, "beta", "/beta/mcp", echoHandler(`{"who":"beta"}`))
	_, base := newServer(t, a, b)

	cases := []struct {
		path string
		want string
	}{
		{"/alpha/mcp", `{"who":"alpha"}`},
		{"/beta/mcp", `{"who":"beta"}`},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			status, body := post(t, base+tc.path, rawRequest("1", wire.MethodToolsList))
			if status != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%s", status, body)
			}
			var env struct {
				Result json.RawMessage `json:"result"`
			}
			if err := json.Unmarshal(body, &env); err != nil {
				t.Fatalf("unmarshal: %v; body=%s", err, body)
			}
			if string(env.Result) != tc.want {
				t.Fatalf("result = %s, want %s", env.Result, tc.want)
			}
		})
	}
}

// TestUnmountedPathIsWellFormedError asserts an unmounted path returns a
// well-formed JSON error, not a panic or a bare 404 page (acceptance criterion 2).
func TestUnmountedPathIsWellFormedError(t *testing.T) {
	mount := newMount(1, "inst", httpx.DefaultPath, echoHandler(`{"ok":true}`))
	_, base := newServer(t, mount)

	status, body := post(t, base+"/nowhere", rawRequest("1", wire.MethodToolsCall))
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", status)
	}
	var env map[string]any
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("404 body is not well-formed JSON: %v; body=%s", err, body)
	}
	if _, ok := env["error"]; !ok {
		t.Fatalf("404 body missing error field: %s", body)
	}
}

// TestMalformedBodyNoPanic asserts malformed request bodies are handled without
// panic and produce a well-formed JSON-RPC error response.
func TestMalformedBodyNoPanic(t *testing.T) {
	mount := newMount(1, "inst", httpx.DefaultPath, echoHandler(`{"ok":true}`))
	_, base := newServer(t, mount)

	bodies := map[string][]byte{
		"truncated":     []byte(`{"jsonrpc":"2.0","id":1,"method":`),
		"not-json":      []byte(`}{not json at all`),
		"empty":         {},
		"wrong-version": []byte(`{"jsonrpc":"1.0","id":1,"method":"tools/call"}`),
		"non-object":    []byte(`[1,2,3]`),
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			status, resp := post(t, base+httpx.DefaultPath, body)
			// A wire-level error is a 200 or a 4xx with a JSON-RPC error object;
			// the point is: no panic, a parseable response, no hang.
			if status == 0 {
				t.Fatalf("no status returned")
			}
			if len(resp) == 0 {
				return // an error status with empty body is acceptable
			}
			var env map[string]any
			if err := json.Unmarshal(resp, &env); err != nil {
				t.Fatalf("response not JSON: %v; body=%s", err, resp)
			}
		})
	}
}

// TestClientDisconnectCancelsContext asserts MOCK-212: when the client
// disconnects mid-request, the request context is canceled and the handler
// (blocked on ctx.Done()) is released. A handler that never observed
// cancellation would hang and the test would time out.
func TestClientDisconnectCancelsContext(t *testing.T) {
	started := make(chan struct{}, 1)
	released := make(chan struct{})
	h := releaseOnCancel(started, released)
	mount := newMount(1, "inst", httpx.DefaultPath, h)
	_, base := newServer(t, mount)

	// Dial raw so we can hang up mid-request deterministically.
	u := strings.TrimPrefix(base, "http://")
	conn, err := net.Dial("tcp", u)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	body := rawRequest("1", wire.MethodToolsCall)
	req := "POST " + httpx.DefaultPath + " HTTP/1.1\r\n" +
		"Host: " + u + "\r\n" +
		"Content-Type: application/json\r\n" +
		"Content-Length: " + itoa(len(body)) + "\r\n\r\n" + string(body)
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatalf("write: %v", err)
	}

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		_ = conn.Close()
		t.Fatal("handler never started")
	}
	// Client disconnects before the response is read.
	_ = conn.Close()

	select {
	case <-released:
		// Handler observed ctx cancellation — MOCK-212 satisfied.
	case <-time.After(3 * time.Second):
		t.Fatal("handler was not released by client disconnect; context not canceled (MOCK-212)")
	}
}

// itoa is a tiny local int-to-string to avoid strconv noise in the test above.
func itoa(n int) string {
	return strings.TrimSpace(func() string {
		b, _ := json.Marshal(n)
		return string(b)
	}())
}
