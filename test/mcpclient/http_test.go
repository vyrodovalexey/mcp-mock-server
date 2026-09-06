package mcpclient

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// echoServer returns the request body prefixed so the test can confirm the
// round trip. It is a stand-in for the mock; it shares no code with the client.
func echoServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", ContentTypeJSON)
		_, _ = w.Write(body)
	}))
}

func TestHTTPRoundTripRawBytes(t *testing.T) {
	srv := echoServer(t)
	defer srv.Close()

	c, err := NewHTTP(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	resp, err := c.Discover(context.Background(), IntID(1))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.GotResponse {
		t.Fatal("expected a response")
	}
	if resp.ParseErr != nil {
		t.Fatalf("parse error: %v", resp.ParseErr)
	}
	// The echo server returns the request; the raw body must be exactly what we
	// sent, and it must be non-empty for a wire-level assertion.
	if len(resp.Body) == 0 {
		t.Error("empty raw body")
	}
	sent, _ := c.bindDefault(DiscoverRequest(IntID(1))).Marshal()
	if string(resp.Body) != string(sent) {
		t.Errorf("raw body mismatch:\n got %s\nwant %s", resp.Body, sent)
	}
}

// TestRawSocketDuplicateMixedCaseHeaders is TASK-008 acceptance criterion 5 /
// MOCK-601.2: the same header name is transmitted twice with differing casing,
// and both occurrences must reach the server in wire order with original casing,
// WITHOUT http.Header canonicalisation. To observe the raw casing we run a bare
// TCP server that reads the literal request head.
func TestRawSocketDuplicateMixedCaseHeaders(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	headLines := make(chan []string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			headLines <- nil
			return
		}
		defer func() { _ = conn.Close() }()
		buf := make([]byte, 4096)
		n, _ := conn.Read(buf)
		head := string(buf[:n])
		if i := strings.Index(head, "\r\n\r\n"); i >= 0 {
			head = head[:i]
		}
		headLines <- strings.Split(head, "\r\n")
		// Minimal valid response so the client's ReadResponse succeeds.
		_, _ = conn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\n{}"))
	}()

	c, err := NewHTTP("http://"+ln.Addr().String()+"/mcp", WithRawSocket())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	r := DiscoverRequest(IntID(1))
	r.Headers = []Header{
		{Name: "x-mcp-header", Value: "lower"},
		{Name: "X-MCP-Header", Value: "upper"},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := c.Do(ctx, r); err != nil {
		t.Fatalf("raw-socket Do: %v", err)
	}

	lines := <-headLines
	if lines == nil {
		t.Fatal("server did not accept")
	}
	var sawLower, sawUpper bool
	for _, l := range lines {
		if l == "x-mcp-header: lower" {
			sawLower = true
		}
		if l == "X-MCP-Header: upper" {
			sawUpper = true
		}
	}
	if !sawLower {
		t.Errorf("lowercase header not transmitted verbatim; head=%q", lines)
	}
	if !sawUpper {
		t.Errorf("mixed-case duplicate header not transmitted verbatim; head=%q", lines)
	}
}

func TestHTTPRejectsBadScheme(t *testing.T) {
	if _, err := NewHTTP("ftp://example/mcp"); err == nil {
		t.Error("expected error for non-http scheme")
	}
}

func TestRawSocketRejectsHTTPS(t *testing.T) {
	if _, err := NewHTTP("https://example/mcp", WithRawSocket()); err == nil {
		t.Error("expected raw-socket mode to reject https")
	}
}

func TestHTTPContextCancellation(t *testing.T) {
	// A server that blocks; the client's context should cancel the call.
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
	}))
	defer srv.Close()
	defer close(block)

	c, err := NewHTTP(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := c.Discover(ctx, IntID(1)); err == nil {
		t.Error("expected a cancellation/timeout error")
	}
}
