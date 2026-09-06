package mcpclient

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// newStdioEcho wires a Client to an in-process echo "server" over two pipes and
// returns the client plus a stop func. The server reads one line and writes it
// back, mimicking a newline-delimited JSON-RPC endpoint. It shares no framing
// code with the client.
func newStdioEcho(t *testing.T) (*Client, func()) {
	t.Helper()
	// client writes -> serverIn ; server writes -> clientIn (client reads).
	serverInR, serverInW := io.Pipe()
	clientInR, clientInW := io.Pipe()

	done := make(chan struct{})
	go func() {
		defer close(done)
		br := bufio.NewReader(serverInR)
		for {
			line, err := br.ReadBytes('\n')
			if len(line) > 0 {
				if _, werr := clientInW.Write(line); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	c, err := NewStdio(StdioConfig{
		Reader: clientInR,
		Writer: serverInW,
		Closer: serverInW,
	})
	if err != nil {
		t.Fatal(err)
	}
	stop := func() {
		_ = c.Close()
		_ = serverInR.Close()
		_ = clientInW.Close()
		<-done
	}
	return c, stop
}

func TestStdioRoundTrip(t *testing.T) {
	c, stop := newStdioEcho(t)
	defer stop()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := c.CallTool(ctx, IntID(1), "echo", map[string]any{"message": "ping"})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.GotResponse {
		t.Fatal("expected a response")
	}
	sent, _ := c.bindDefault(ToolsCallRequest(IntID(1), "echo", map[string]any{"message": "ping"})).Marshal()
	if string(resp.Body) != string(sent) {
		t.Errorf("stdio raw body mismatch:\n got %s\nwant %s", resp.Body, sent)
	}
}

// TestCrossTransportByteIdentical is TASK-008 acceptance criterion 4: the same
// JSON-RPC payload sent over stdio and over HTTP yields byte-identical response
// bodies from an echo server. This is the wire-level determinism check the
// client exists to enable.
func TestCrossTransportByteIdentical(t *testing.T) {
	// Build one request and reuse its exact bytes on both transports.
	req := (&Client{defaultClientInfo: defaultClientInfo()}).
		bindDefault(ToolsListRequest(IntID(42)))
	payload, err := req.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	rawReq := &Request{RawBody: payload}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// HTTP echo.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	hc, err := NewHTTP(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = hc.Close() }()
	httpResp, err := hc.Do(ctx, rawReq)
	if err != nil {
		t.Fatal(err)
	}

	// stdio echo.
	sc, stop := newStdioEcho(t)
	defer stop()
	stdioResp, err := sc.Do(ctx, &Request{RawBody: payload})
	if err != nil {
		t.Fatal(err)
	}

	// stdio echo appends the framing newline the transport added; compare the
	// JSON-RPC payload the server saw, which is identical modulo that framing.
	got := stdioResp.Body
	if n := len(got); n > 0 && got[n-1] == '\n' {
		got = got[:n-1]
	}
	if string(httpResp.Body) != string(got) {
		t.Errorf("cross-transport bodies differ:\n http : %s\n stdio: %s", httpResp.Body, got)
	}
}

func TestStdioContextCancellation(t *testing.T) {
	// A server that never replies: client read must abandon on ctx timeout.
	serverInR, serverInW := io.Pipe()
	clientInR, clientInW := io.Pipe()
	defer func() { _ = serverInR.Close() }()
	defer func() { _ = clientInW.Close() }()

	c, err := NewStdio(StdioConfig{Reader: clientInR, Writer: serverInW})
	if err != nil {
		t.Fatal(err)
	}
	// Drain writes so the client's write does not block.
	go func() { _, _ = io.Copy(io.Discard, serverInR) }()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err = c.Discover(ctx, IntID(1))
	if err == nil {
		t.Error("expected a cancellation error when no response arrives")
	}
}

func TestStdioRequiresBothStreams(t *testing.T) {
	if _, err := NewStdio(StdioConfig{Reader: nil, Writer: io.Discard}); err == nil {
		t.Error("expected error for missing reader")
	}
}
