package control

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// getVia issues GET target through client and returns the status and body. It is
// a bare test HTTP client, not the production control client, so this test
// exercises the Handler over each transport without depending on any client
// code.
func getVia(t *testing.T, client *http.Client, target, accept string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, target, http.NoBody)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("do %s: %v", target, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// TestHTTPUDSParity is the ADR-015 property: the identical Handler served over
// TCP and over a unix socket produces byte-identical results for the same
// operation. The two front ends share one Handler, so this proves they agree.
func TestHTTPUDSParity(t *testing.T) {
	h, _ := newTestHandler()

	// TCP front end.
	tcp := httptest.NewServer(h)
	defer tcp.Close()

	// Unix front end serving the SAME handler.
	sockPath := filepath.Join(shortSockDir(t), "ctl.sock")
	ln, err := ListenUDS(sockPath)
	if err != nil {
		t.Fatalf("ListenUDS: %v", err)
	}
	s := New(Config{Handler: h, UnixListener: ln, SocketPath: sockPath})
	s.Serve()
	defer func() { _ = s.Shutdown(context.Background()) }()

	tcpClient := tcp.Client()
	unixClient := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", sockPath)
			},
		},
	}

	// Every read operation must agree byte-for-byte across the two transports.
	targets := []struct{ name, path, accept string }{
		{"instances", "/v1/instances", ""},
		{"instance", "/v1/instances/a", ""},
		{"seed", "/v1/seed", ""},
		{"health", "/v1/health", ""},
		{"journal json", "/v1/instances/a/journal", ""},
		{"journal filtered", "/v1/instances/a/journal?transport=stdio", ""},
		{"journal ndjson", "/v1/instances/a/journal", contentTypeNDJSON},
		{"not found", "/v1/instances/zzz", ""},
	}
	for _, tt := range targets {
		t.Run(tt.name, func(t *testing.T) {
			tcpStatus, tcpBody := getVia(t, tcpClient, tcp.URL+tt.path, tt.accept)
			unixStatus, unixBody := getVia(t, unixClient, "http://unix"+tt.path, tt.accept)
			if tcpStatus != unixStatus {
				t.Errorf("status differs: tcp=%d unix=%d", tcpStatus, unixStatus)
			}
			if tcpBody != unixBody {
				t.Errorf("body differs:\n tcp=%q\n unix=%q", tcpBody, unixBody)
			}
		})
	}
}

// TestServerEndpoints asserts the Server reports its TCP and socket endpoints,
// and unlinks the socket on shutdown (AMEND-7 cleanup).
func TestServerEndpoints(t *testing.T) {
	h, _ := newTestHandler()
	tcpLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("tcp listen: %v", err)
	}
	sockPath := filepath.Join(shortSockDir(t), "ep.sock")
	unixLn, err := ListenUDS(sockPath)
	if err != nil {
		t.Fatalf("ListenUDS: %v", err)
	}
	s := New(Config{Handler: h, TCPListener: tcpLn, UnixListener: unixLn, SocketPath: sockPath})
	s.Serve()

	if s.TCPAddr() == "" {
		t.Error("TCPAddr should be non-empty")
	}
	if s.SocketPath() != sockPath {
		t.Errorf("SocketPath = %q, want %q", s.SocketPath(), sockPath)
	}
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	// The socket must be unlinked after graceful shutdown.
	if _, statErr := os.Stat(sockPath); statErr == nil {
		t.Errorf("socket %q should be unlinked after shutdown", sockPath)
	}
}
