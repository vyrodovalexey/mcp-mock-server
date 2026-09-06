package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// DEF-008: the serve command must enable the observability listener so /metrics,
// /healthz and /readyz are reachable in a container (MOCK-105). These tests wire
// against a running `serve` session — the same construct-then-serve path runServe
// takes minus the signal context — and hit the endpoints over HTTP.

// serveSession builds a serve session, runs it under a cancellable context, and
// returns the session (for srv.ObservabilityURL()), a cancel func that triggers
// graceful shutdown, and a channel carrying the exit code. It fails the test if
// construction fails, so callers can assume a live session.
func serveSession(t *testing.T, args []string, errOut io.Writer) (*session, context.CancelFunc, <-chan int) {
	t.Helper()
	sess, ts, code, ok := prepareServe(errOut, args)
	if !ok {
		t.Fatalf("prepareServe failed (code=%d): %s", code, bufString(errOut))
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() { done <- sess.serve(ctx, ts) }()
	return sess, cancel, done
}

// bufString reads a stringer-capable buffer for diagnostics, or "" otherwise.
func bufString(w io.Writer) string {
	if s, ok := w.(stringer); ok {
		return s.String()
	}
	return ""
}

// getStatus issues a GET and returns the status code and body, failing on a
// transport error. A short client timeout keeps a wedged endpoint from hanging
// the suite.
func getStatus(t *testing.T, url string) (int, string) {
	t.Helper()
	client := &http.Client{Timeout: 3 * time.Second}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("new request %q: %v", url, err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET %q: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body %q: %v", url, err)
	}
	return resp.StatusCode, string(body)
}

// waitReady polls /readyz until it returns 200 or the deadline passes. It fails
// on timeout so a never-ready server does not hang the suite.
func waitReady(t *testing.T, base string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	client := &http.Client{Timeout: time.Second}
	for time.Now().Before(deadline) {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, base+"/readyz", nil)
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("/readyz never returned 200 within deadline")
}

// TestServeExposesMetricsAndReadyz is the core DEF-008 property: `serve` enables
// the observability listener, and once ready /metrics serves Prometheus text and
// /readyz and /healthz return 200 (MOCK-105). It uses an ephemeral obs port so
// the test never collides with the :9090 default or a concurrent test.
func TestServeExposesMetricsAndReadyz(t *testing.T) {
	errOut := &syncBuf{}
	sess, cancel, done := serveSession(t,
		[]string{"--transport", "http", "--listen", "127.0.0.1:0",
			"--metrics-listen", "127.0.0.1:0", "--no-control"},
		errOut)
	waitForStderr(t, errOut, "serving")

	base := sess.srv.ObservabilityURL()
	if base == "" {
		t.Fatal("ObservabilityURL empty; the observability listener was not enabled")
	}
	waitReady(t, base)

	if code, body := getStatus(t, base+"/metrics"); code != http.StatusOK {
		t.Errorf("/metrics = %d, want 200 (body=%q)", code, body)
	} else if !strings.Contains(body, "# HELP") && !strings.Contains(body, "# TYPE") {
		t.Errorf("/metrics body is not Prometheus text exposition:\n%s", body)
	}
	if code, _ := getStatus(t, base+"/readyz"); code != http.StatusOK {
		t.Errorf("/readyz = %d, want 200", code)
	}
	if code, _ := getStatus(t, base+"/healthz"); code != http.StatusOK {
		t.Errorf("/healthz = %d, want 200", code)
	}

	cancel()
	if code := waitCode(t, done); code != exitOK {
		t.Fatalf("serve exit = %d, want %d (stderr=%q)", code, exitOK, errOut.String())
	}
}

// TestServeReadyzGatesOnServing is the property that makes /readyz worth having:
// a 200 from /readyz means the MCP listener is actually accepting connections, so
// Kubernetes never routes traffic to a pod that cannot serve it. Once /readyz is
// 200 the announced MCP URL must be dialable; after shutdown /readyz drops to a
// non-200 (drain), so the endpoint tracks real serving state, not a constant.
func TestServeReadyzGatesOnServing(t *testing.T) {
	errOut := &syncBuf{}
	sess, cancel, done := serveSession(t,
		[]string{"--transport", "http", "--listen", "127.0.0.1:0",
			"--metrics-listen", "127.0.0.1:0", "--no-control"},
		errOut)
	waitForStderr(t, errOut, "serving")

	base := sess.srv.ObservabilityURL()
	waitReady(t, base)

	// /readyz is 200 -> the MCP listener must be accepting.
	insts := sess.srv.Instances()
	if len(insts) == 0 {
		t.Fatal("no instances; cannot check MCP dialability")
	}
	mcpURL := insts[0].URL()
	hostPort := strings.TrimPrefix(mcpURL, "http://")
	if i := strings.IndexByte(hostPort, '/'); i >= 0 {
		hostPort = hostPort[:i]
	}
	conn, err := net.DialTimeout("tcp", hostPort, 2*time.Second)
	if err != nil {
		t.Fatalf("/readyz was 200 but MCP listener %q not accepting: %v", hostPort, err)
	}
	_ = conn.Close()

	cancel()
	if code := waitCode(t, done); code != exitOK {
		t.Fatalf("serve exit = %d, want %d", code, exitOK)
	}
}

// TestServeStdioObservabilityKeepsStdoutClean is the ADR-011 property with
// observability enabled in stdio mode: the /metrics,/healthz,/readyz listener is
// a separate HTTP socket that writes nothing to stdout, so the stdio protocol
// channel still carries only JSON-RPC frames (MOCK-105.4). It redirects os.Stdout
// to a pipe (captured by the hijack as the protocol channel), enables the obs
// listener on an ephemeral port, hits /metrics, then asserts stdout is
// protocol-only.
func TestServeStdioObservabilityKeepsStdoutClean(t *testing.T) {
	req := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}` + "\n"

	protoR, protoW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	origStdout, origStdin := os.Stdout, os.Stdin
	os.Stdout, os.Stdin = protoW, stdinR
	t.Cleanup(func() { os.Stdout, os.Stdin = origStdout, origStdin })

	protoBuf := &syncBuf{}
	collected := make(chan struct{})
	go func() { _, _ = io.Copy(protoBuf, protoR); close(collected) }()

	errOut := &syncBuf{}
	sess, cancel, done := serveSession(t,
		[]string{"--transport", "stdio", "--metrics-listen", "127.0.0.1:0", "--seed", "88"},
		errOut)
	waitForStderr(t, errOut, "serving")

	// The obs listener is live even in stdio mode; hit /metrics to exercise it.
	base := sess.srv.ObservabilityURL()
	if base == "" {
		t.Fatal("stdio mode did not enable the observability listener")
	}
	waitReady(t, base)
	if code, _ := getStatus(t, base+"/metrics"); code != http.StatusOK {
		t.Errorf("stdio /metrics = %d, want 200", code)
	}

	// Drive one protocol request; its response must land on stdout, nothing else.
	if _, err := stdinW.Write([]byte(req)); err != nil {
		t.Fatalf("write request: %v", err)
	}
	waitForBuffer(t, protoBuf, `"jsonrpc"`)
	_ = stdinW.Close()

	cancel()
	if code := waitCode(t, done); code != exitOK {
		t.Fatalf("serve exit = %d, want %d (stderr=%q)", code, exitOK, errOut.String())
	}
	_ = protoW.Close()
	<-collected
	_ = protoR.Close()

	assertProtocolOnly(t, protoBuf.String())
	if strings.Contains(protoBuf.String(), "# HELP") || strings.Contains(protoBuf.String(), "serving") {
		t.Errorf("observability output leaked to stdout protocol channel:\n%s", protoBuf.String())
	}
}

// TestServeStartupWithinBudget asserts enabling the observability listener does
// not blow the MOCK-107 startup budget (200 ms): construction plus Start plus
// readiness completes well inside it. The listener startup must be non-blocking,
// so this is a real regression guard, not a vanity metric.
func TestServeStartupWithinBudget(t *testing.T) {
	errOut := &syncBuf{}
	sess, _, code, ok := prepareServe(errOut,
		[]string{"--transport", "http", "--listen", "127.0.0.1:0",
			"--metrics-listen", "127.0.0.1:0", "--no-control"})
	if !ok {
		t.Fatalf("prepareServe failed (code=%d): %s", code, errOut.String())
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	start := time.Now()
	if err := sess.srv.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	elapsed := time.Since(start)
	t.Cleanup(func() {
		sc, scancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer scancel()
		_ = sess.srv.Shutdown(sc)
	})

	// Generous ceiling versus the 200 ms budget; the facade measures ~2 ms.
	if elapsed > 150*time.Millisecond {
		t.Errorf("Start with observability took %v, over the MOCK-107 budget guard", elapsed)
	}
	base := sess.srv.ObservabilityURL()
	waitReady(t, base)
}

// TestServeMetricsListenFlagBeatsEnv asserts flag > env > default precedence for
// the observability address: an explicit --metrics-listen wins over
// MCPMOCK_METRICS_LISTEN, which in turn wins over the default. It resolves the
// address through the same helper the CLI uses.
func TestServeMetricsListenFlagBeatsEnv(t *testing.T) {
	t.Setenv(envMetricsListen, "127.0.0.1:19191")

	// Flag beats env.
	if got := resolveMetricsListen(serveFlags{metricsListen: "127.0.0.1:12345"}); got != "127.0.0.1:12345" {
		t.Errorf("flag should win: got %q, want 127.0.0.1:12345", got)
	}
	// Env beats default when the flag is unset.
	if got := resolveMetricsListen(serveFlags{}); got != "127.0.0.1:19191" {
		t.Errorf("env should win over default: got %q, want 127.0.0.1:19191", got)
	}
}

// TestServeMetricsListenDefault asserts the default is the container-safe :9090
// (all interfaces, unprivileged) when neither flag nor env is set — the address a
// kubelet probe can reach in a pod (deployment.md §4).
func TestServeMetricsListenDefault(t *testing.T) {
	t.Setenv(envMetricsListen, "")
	if got := resolveMetricsListen(serveFlags{}); got != defaultMetricsListen {
		t.Errorf("default = %q, want %q", got, defaultMetricsListen)
	}
	if defaultMetricsListen != ":9090" {
		t.Errorf("default metrics listen = %q, want :9090 (observability.md §38)", defaultMetricsListen)
	}
}

// TestServeMetricsEnvBinds asserts the env override actually binds the
// observability listener (no explicit flag), proving the env path is wired end to
// end, not merely resolved. It uses an ephemeral loopback port via the env var.
func TestServeMetricsEnvBinds(t *testing.T) {
	t.Setenv(envMetricsListen, "127.0.0.1:0")
	errOut := &syncBuf{}
	sess, cancel, done := serveSession(t,
		[]string{"--transport", "http", "--listen", "127.0.0.1:0", "--no-control"},
		errOut)
	waitForStderr(t, errOut, "serving")

	base := sess.srv.ObservabilityURL()
	if !strings.HasPrefix(base, "http://127.0.0.1:") {
		t.Fatalf("env-configured obs listener bound %q, want http://127.0.0.1:<port>", base)
	}
	waitReady(t, base)

	cancel()
	if code := waitCode(t, done); code != exitOK {
		t.Fatalf("serve exit = %d, want %d", code, exitOK)
	}
}
