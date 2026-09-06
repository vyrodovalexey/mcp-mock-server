package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// runServeAsync builds a serve session and runs it in a goroutine under a
// cancellable context, returning the cancel func and a channel that receives the
// exit code. The caller cancels to trigger graceful shutdown (MOCK-212) and
// reads the code. It uses prepareServe + session.serve, the same path runServe
// takes minus the signal context, so a test can cancel without signalling the
// whole test binary.
func runServeAsync(t *testing.T, args []string, errOut io.Writer) (context.CancelFunc, <-chan int) {
	t.Helper()
	sess, ts, code, ok := prepareServe(errOut, args)
	done := make(chan int, 1)
	if !ok {
		done <- code
		return func() {}, done
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { done <- sess.serve(ctx, ts) }()
	return cancel, done
}

// waitCode waits for the serve goroutine to return, failing on timeout so a
// wedged shutdown does not hang the suite.
func waitCode(t *testing.T, done <-chan int) int {
	t.Helper()
	select {
	case code := <-done:
		return code
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not shut down within 10s")
		return -1
	}
}

// stringer is the read side of a buffer the serve tests poll; both bytes.Buffer
// and *syncBuf satisfy it.
type stringer interface{ String() string }

// seedFromStderr extracts the seed from the structured "effective seed" record
// the serve command prints on stderr (MOCK-704.2). It fails if no such record is
// present, proving the record is emitted and on the stderr channel.
func seedFromStderr(t *testing.T, stderr string) uint64 {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(stderr), "\n") {
		var rec struct {
			Msg  string `json:"msg"`
			Seed uint64 `json:"seed"`
		}
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if rec.Msg == "effective seed" {
			return rec.Seed
		}
	}
	t.Fatalf(`no {"msg":"effective seed","seed":...} record on stderr:\n%s`, stderr)
	return 0
}

// TestServeHTTPPrintsEffectiveSeedToStderr asserts the effective seed is printed
// as a structured record on stderr at startup, matching the supplied --seed
// (MOCK-704.2). It also exercises the SIGINT-equivalent graceful shutdown path
// (ctx cancellation, MOCK-212).
func TestServeHTTPPrintsEffectiveSeedToStderr(t *testing.T) {
	errOut := &syncBuf{}
	cancel, done := runServeAsync(t,
		[]string{"--transport", "http", "--listen", "127.0.0.1:0", "--no-control", "--seed", "424242"},
		errOut)

	// Give startup a moment to emit the seed record, then shut down.
	waitForStderr(t, errOut, "effective seed")
	cancel()
	if code := waitCode(t, done); code != exitOK {
		t.Fatalf("serve exit = %d, want %d (stderr=%q)", code, exitOK, errOut.String())
	}
	if got := seedFromStderr(t, errOut.String()); got != 424242 {
		t.Errorf("printed seed = %d, want 424242", got)
	}
}

// TestServeRandomSeedDiffers asserts two serve runs without --seed print
// different seeds (MOCK-704.1: two runs without a seed differ).
func TestServeRandomSeedDiffers(t *testing.T) {
	run := func() uint64 {
		errOut := &syncBuf{}
		cancel, done := runServeAsync(t,
			[]string{"--transport", "http", "--listen", "127.0.0.1:0", "--no-control"},
			errOut)
		waitForStderr(t, errOut, "effective seed")
		cancel()
		_ = waitCode(t, done)
		return seedFromStderr(t, errOut.String())
	}
	if run() == run() {
		t.Error("two seedless serve runs printed the same seed")
	}
}

// TestServeInvalidScenarioExitsValidation asserts serve on an invalid scenario
// exits with the validation code (MOCK-701), not a crash.
func TestServeInvalidScenarioExitsValidation(t *testing.T) {
	invalid := writeScenario(t, "invalid.yaml", invalidScenario)
	var errOut bytes.Buffer
	code := runServe(&errOut,
		[]string{"--transport", "http", "--path", invalid, "--no-control"})
	if code != exitValidation {
		t.Fatalf("exit = %d, want %d (stderr=%q)", code, exitValidation, errOut.String())
	}
}

// TestServeControlSocketFlagWins asserts an explicit --control-socket is used
// verbatim (flag beats env and default), by pointing the flag at a chosen path
// and confirming the socket is created there — the CLI-side of the AMEND-7
// precedence (flag > env > default). The env is set to a different path to prove
// the flag wins.
func TestServeControlSocketFlagWins(t *testing.T) {
	flagPath := filepath.Join(shortSockDir(t), "flag.sock")
	envPath := filepath.Join(shortSockDir(t), "env.sock")
	t.Setenv("MCPMOCK_CONTROL_SOCKET", envPath)

	errOut := &syncBuf{}
	cancel, done := runServeAsync(t,
		[]string{"--transport", "http", "--listen", "127.0.0.1:0", "--control-socket", flagPath},
		errOut)
	waitForStderr(t, errOut, "serving")

	if _, err := os.Stat(flagPath); err != nil {
		t.Errorf("flag socket %q not created: %v", flagPath, err)
	}
	if _, err := os.Stat(envPath); err == nil {
		t.Errorf("env socket %q was created; flag should have won", envPath)
	}
	cancel()
	if code := waitCode(t, done); code != exitOK {
		t.Fatalf("serve exit = %d, want %d", code, exitOK)
	}
}

// TestServeDualTransportBothServe asserts serve --transport stdio,http enables
// both transports on the same scenario (criterion 5 / MOCK-102.3): the HTTP
// instance reports a bound URL and the announce line records stdio=true. It runs
// with os.Stdin/os.Stdout redirected so the stdio hijack captures a test pipe,
// not the test binary's real stdout.
func TestServeDualTransportBothServe(t *testing.T) {
	protoR, protoW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	// Drain the protocol channel so the hijack/transport never block on a full
	// pipe, and so the drainer joins cleanly on shutdown.
	go func() { _, _ = io.Copy(io.Discard, protoR) }()

	origStdout, origStdin := os.Stdout, os.Stdin
	os.Stdout, os.Stdin = protoW, stdinR
	t.Cleanup(func() { os.Stdout, os.Stdin = origStdout, origStdin })

	errOut := &syncBuf{}
	cancel, done := runServeAsync(t,
		[]string{"--transport", "stdio,http", "--listen", "127.0.0.1:0", "--no-control"},
		errOut)
	waitForStderr(t, errOut, "serving")

	logs := errOut.String()
	if !strings.Contains(logs, `"mcpUrl":"http://127.0.0.1:`) {
		t.Errorf("http transport did not bind a URL:\n%s", logs)
	}
	if !strings.Contains(logs, `"stdio":true`) {
		t.Errorf("stdio transport not announced:\n%s", logs)
	}

	// Close stdin to give the stdio read loop EOF, then cancel; shutdown then
	// completes without waiting on a blocked read.
	_ = stdinW.Close()
	cancel()
	if code := waitCode(t, done); code != exitOK {
		t.Fatalf("serve exit = %d, want %d", code, exitOK)
	}
	_ = protoW.Close()
}

// TestServeStdioKeepsStdoutClean is the ADR-011 property at the CLI level: in
// stdio mode nothing but protocol frames reach stdout, the hijack is installed,
// and a clean run leaks zero bytes (MOCK-105.4). It redirects os.Stdout to a
// pipe (which the hijack captures as the protocol channel), feeds one request on
// os.Stdin, and asserts stdout holds exactly one JSON-RPC response and no
// diagnostics; the effective-seed record is on stderr instead.
func TestServeStdioKeepsStdoutClean(t *testing.T) {
	// Feed exactly one tools/list request, then EOF so the transport's read loop
	// ends and shutdown is clean.
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
	os.Stdout = protoW // the hijack captures this as ProtoOut (fd 1 stand-in)
	os.Stdin = stdinR
	t.Cleanup(func() { os.Stdout, os.Stdin = origStdout, origStdin })

	// Collect everything written to the protocol channel.
	protoBuf := &syncBuf{}
	collected := make(chan struct{})
	go func() { _, _ = io.Copy(protoBuf, protoR); close(collected) }()

	errOut := &syncBuf{}
	sess, ts, _, ok := prepareServe(errOut, []string{"--transport", "stdio", "--seed", "77"})
	if !ok {
		t.Fatalf("prepareServe failed: %s", errOut.String())
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() { done <- sess.serve(ctx, ts) }()

	// Send the request and close stdin to signal EOF.
	if _, err := stdinW.Write([]byte(req)); err != nil {
		t.Fatalf("write request: %v", err)
	}
	waitForBuffer(t, protoBuf, `"jsonrpc"`)
	_ = stdinW.Close()

	cancel()
	if code := waitCode(t, done); code != exitOK {
		t.Fatalf("serve exit = %d, want %d (stderr=%q)", code, exitOK, errOut.String())
	}

	// Close the write end and drain the collector so protoBuf is complete.
	_ = protoW.Close()
	<-collected
	_ = protoR.Close()

	assertProtocolOnly(t, protoBuf.String())

	// The effective seed must be on stderr, never on the protocol channel.
	if strings.Contains(protoBuf.String(), "effective seed") {
		t.Errorf("seed record leaked to stdout protocol channel: %q", protoBuf.String())
	}
	if got := seedFromStderr(t, errOut.String()); got != 77 {
		t.Errorf("stderr seed = %d, want 77", got)
	}
}

// TestServeSameSeedByteIdentical is the determinism criterion at the CLI level:
// two stdio serve runs with the same --seed produce byte-identical protocol
// output for the same request (MOCK-704 / PRIN-1). It runs the same single
// request twice and compares the raw response bytes.
func TestServeSameSeedByteIdentical(t *testing.T) {
	req := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}` + "\n"
	first := stdioResponse(t, req, "31337")
	second := stdioResponse(t, req, "31337")
	if first != second {
		t.Errorf("same-seed responses differ:\n first=%q\n second=%q", first, second)
	}
	if first == "" {
		t.Fatal("empty response; the run produced no protocol frame")
	}
}

// stdioResponse runs one stdio serve for a single request at the given seed and
// returns the raw bytes written to the protocol channel. It fully restores
// os.Stdin/os.Stdout, so it is safe to call repeatedly and leaks no goroutine.
func stdioResponse(t *testing.T, req, seed string) string {
	t.Helper()
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
	sess, ts, _, ok := prepareServe(errOut, []string{"--transport", "stdio", "--seed", seed})
	if !ok {
		t.Fatalf("prepareServe failed: %s", errOut.String())
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() { done <- sess.serve(ctx, ts) }()

	if _, err := stdinW.Write([]byte(req)); err != nil {
		t.Fatalf("write request: %v", err)
	}
	waitForBuffer(t, protoBuf, `"jsonrpc"`)
	_ = stdinW.Close()
	cancel()
	_ = waitCode(t, done)

	_ = protoW.Close()
	<-collected
	_ = protoR.Close()
	return protoBuf.String()
}

// assertProtocolOnly asserts every non-empty line of the protocol channel is a
// JSON object (a protocol frame), i.e. no plain-text diagnostic leaked to
// stdout.
func assertProtocolOnly(t *testing.T, out string) {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) == 0 || lines[0] == "" {
		t.Fatal("protocol channel is empty; expected one response frame")
	}
	for i, line := range lines {
		if line == "" {
			continue
		}
		var obj map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			t.Errorf("stdout line %d is not a JSON protocol frame: %q", i, line)
			continue
		}
		if _, ok := obj["jsonrpc"]; !ok {
			t.Errorf("stdout line %d is JSON but not a JSON-RPC frame: %q", i, line)
		}
	}
}

// waitForStderr polls buf until it contains sub or the deadline passes.
func waitForStderr(t *testing.T, buf stringer, sub string) {
	t.Helper()
	waitForBuffer(t, buf, sub)
}

// waitForBuffer polls buf until it contains sub or a 5s deadline passes. It is a
// simple readiness wait for the async serve goroutine's output.
func waitForBuffer(t *testing.T, buf stringer, sub string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(buf.String(), sub) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %q in output:\n%s", sub, buf.String())
}
