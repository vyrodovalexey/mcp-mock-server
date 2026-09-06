package httpx_test

import (
	"bufio"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/vyrodovalexey/mcp-mock-server/internal/transport/httpx"
	"github.com/vyrodovalexey/mcp-mock-server/internal/wire"
)

// TestKeepAliveHeadersNotMisattributed is the REV-003 regression: on a reused
// (keep-alive) connection, every journal record must carry ITS OWN request head
// — its headers AND its Authorization credential — not request #1's. Before the
// fix the raw-head tee captured only the first request's head per connection and
// re-served it for every later request, so a second request presenting a
// different token would be journalled with the first request's credential hash,
// making any token-passthrough assertion (assert.AssertHeaderNotValue) unsound.
//
// The test sends TWO requests with DIFFERENT x-mcp-req headers and DIFFERENT
// Authorization values on ONE TCP connection, then asserts the two records carry
// distinct, correctly-attributed evidence.
func TestKeepAliveHeadersNotMisattributed(t *testing.T) {
	inst := newInstance(1, "keepalive")
	reg := newRegistryForMethods(echoHandler(`{"ok":true}`))
	mount := &httpx.Mount{Path: httpx.DefaultPath, Instance: inst, Pipeline: newPipelineFor(reg)}
	srv, err := httpx.New(httpx.Config{Addr: "127.0.0.1:0", Mounts: []*httpx.Mount{mount}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	go func() { _ = srv.Serve() }()
	t.Cleanup(func() { _ = srv.Close() })
	base := srv.Addr().String()

	conn, err := net.Dial("tcp", base)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	br := bufio.NewReader(conn)

	// Two requests, same connection (no "Connection: close"), each with its own
	// distinctive header value and its own bearer token.
	reqs := []struct{ tag, token string }{
		{"alpha", "Bearer token-ALPHA"},
		{"bravo", "Bearer token-BRAVO"},
	}
	for i, rq := range reqs {
		body := rawRequest("1", wire.MethodToolsCall)
		head := "POST " + httpx.DefaultPath + " HTTP/1.1\r\n" +
			"Host: " + base + "\r\n" +
			"Content-Type: application/json\r\n" +
			"x-mcp-req: " + rq.tag + "\r\n" +
			"Authorization: " + rq.token + "\r\n" +
			"Content-Length: " + itoa(len(body)) + "\r\n\r\n" + string(body)
		if _, werr := conn.Write([]byte(head)); werr != nil {
			t.Fatalf("write request %d: %v", i, werr)
		}
		// Read the full response before sending the next request: HTTP/1.1
		// serializes requests on a connection, and reading the response ensures
		// the handler (and thus the journal write and the tee re-arm) completed.
		httpReq, _ := http.NewRequest(http.MethodPost, "http://"+base+httpx.DefaultPath, http.NoBody)
		resp, rerr := http.ReadResponse(br, httpReq)
		if rerr != nil {
			t.Fatalf("read response %d: %v", i, rerr)
		}
		drainAndClose(t, resp)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d status = %d, want 200", i, resp.StatusCode)
		}
	}

	recs := inst.ring.Query()
	if len(recs) != 2 {
		t.Fatalf("journal has %d records, want 2 (one per keep-alive request)", len(recs))
	}

	// Each record must carry its own x-mcp-req value in wire order.
	for i, rec := range recs {
		want := reqs[i].tag
		got := headerValue(rec.HTTP.Headers, "x-mcp-req")
		if got != want {
			t.Errorf("record %d x-mcp-req = %q, want %q (headers misattributed across keep-alive requests, REV-003)\nheaders: %+v",
				i, got, want, rec.HTTP.Headers)
		}
	}

	// Each record's credential hash must differ, because the two requests
	// presented different tokens. Equal hashes would mean request #1's
	// Authorization was reused for request #2 — the exact misattribution.
	if recs[0].Credential == nil || recs[1].Credential == nil {
		t.Fatalf("both records must carry a credential part; got %+v and %+v", recs[0].Credential, recs[1].Credential)
	}
	if recs[0].Credential.Hash == recs[1].Credential.Hash {
		t.Errorf("SECURITY: both records share credential hash %q despite different Authorization values; "+
			"request #1's credential was misattributed to request #2 (REV-003)", recs[0].Credential.Hash)
	}
}

// headerValue returns the value of the first header whose name matches name
// exactly (case-sensitive, preserving the wire fidelity the journal records),
// or "" when absent.
func headerValue(headers [][2]string, name string) string {
	for _, h := range headers {
		if h[0] == name {
			return h[1]
		}
	}
	return ""
}

// drainAndClose reads and discards the response body and closes it, so the
// bufio.Reader is positioned at the start of the next response on the connection.
func drainAndClose(t *testing.T, resp *http.Response) {
	t.Helper()
	buf := make([]byte, 512)
	for {
		if _, err := resp.Body.Read(buf); err != nil {
			break
		}
	}
	_ = resp.Body.Close()
}
