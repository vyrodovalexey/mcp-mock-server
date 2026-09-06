package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/vyrodovalexey/mcp-mock-server/internal/obs"
)

// TestMain runs every test under goleak so the drainer goroutine leaking past
// Restore — the failure mode the "no deadlock / clean shutdown" criteria guard
// against — fails the suite rather than passing silently. A correct Restore
// joins the drainer, so a clean run leaks nothing.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// stdoutAtImport records os.Stdout as observed by an init() in this test binary,
// BEFORE any Install runs. It is the probe for acceptance criterion 4 and 6: if
// merely importing/initialising cmd/mcpmock installed the hijack, this would be
// a pipe rather than the real fd 1. It is the real fd 1, proving the hijack is
// never an init()/import side effect.
var stdoutAtImport *os.File

func init() { stdoutAtImport = os.Stdout }

// countingCounter is a trivial concurrency-safe LeakCounter for tests that want
// to assert the counted total independently of the atomic inside Hijack.
type countingCounter struct{ n atomic.Uint64 }

func (c *countingCounter) Add(v float64) { c.n.Add(uint64(v)) }
func (c *countingCounter) total() uint64 { return c.n.Load() }

// newTestLogger returns a stderr-style logger writing JSON to buf, matching the
// module's obs logger so the emitted "stdout_leak" records are exactly what
// cmd/mcpmock would emit. The buffer stands in for stderr.
func newTestLogger(buf io.Writer) *slog.Logger {
	return obs.NewLogger(buf, slog.LevelDebug)
}

// withRedirectedFd1 replaces the real os.Stdout with a pipe for the duration of
// fn, returning everything written to that pipe. It lets a test capture the
// protocol channel (Hijack.ProtoOut, which is the real fd 1 as it stood at
// Install time) without polluting the test runner's own stdout. It is the same
// technique obs.TestNoStdoutWrites uses. It must not run in parallel: it mutates
// process-global os.Stdout.
func withRedirectedFd1(t *testing.T, fn func()) []byte {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	captured := make(chan []byte, 1)
	go func() {
		data, _ := io.ReadAll(r)
		captured <- data
	}()

	fn()

	os.Stdout = orig
	_ = w.Close()
	data := <-captured
	_ = r.Close()
	return data
}

// installForTest installs a hijack with a buffer-backed logger and a counting
// counter, and registers a Restore with a generous timeout so a test failure can
// never hang the suite. It returns the hijack, the log buffer and the counter.
func installForTest(t *testing.T) (*Hijack, *bytes.Buffer, *countingCounter) {
	t.Helper()
	logBuf := &bytes.Buffer{}
	counter := &countingCounter{}
	h, err := Install(newTestLogger(logBuf), counter)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = h.Restore(ctx)
	})
	return h, logBuf, counter
}

// waitForLeakBytes polls h.LeakBytes until it reaches at least want or the
// deadline passes. The drainer is asynchronous, so a test that writes then reads
// immediately would race the drain; this waits deterministically without a fixed
// sleep.
func waitForLeakBytes(t *testing.T, h *Hijack, want uint64) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if h.LeakBytes() >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for LeakBytes >= %d, got %d", want, h.LeakBytes())
}

// TestNotInstalledByImport asserts the library-embedding safety property
// (acceptance criteria 4 and 6): importing and initialising cmd/mcpmock does not
// install the hijack. The init()-captured stdoutAtImport is the real fd 1, not a
// pipe, so an embedding host's stdout is never ambushed on import.
func TestNotInstalledByImport(t *testing.T) {
	if stdoutAtImport == nil {
		t.Fatal("init probe did not run")
	}
	// The process-global os.Stdout at the start of a test (no Install in effect)
	// is the same handle the init() saw: nothing hijacked it on import.
	if os.Stdout != stdoutAtImport {
		t.Fatalf("os.Stdout changed without an explicit Install: import had a side effect")
	}
}

// TestInstallRejectsNilLogger covers the error path: Install requires a stderr
// logger and refuses nil rather than silently discarding leak diagnostics.
func TestInstallRejectsNilLogger(t *testing.T) {
	h, err := Install(nil, &countingCounter{})
	if err == nil {
		t.Fatal("expected error for nil logger")
	}
	if h != nil {
		t.Fatalf("expected nil hijack on error, got %v", h)
	}
}

// TestProtoOutIsCapturedFd1 asserts the capture-before-replace ordering
// (acceptance criterion 3 mechanics): Hijack.ProtoOut is the real fd 1 as it
// stood at Install, and os.Stdout has been replaced by a different handle (the
// drain pipe). If the order were wrong, ProtoOut would be the pipe and the mock
// would be mute.
func TestProtoOutIsCapturedFd1(t *testing.T) {
	fd1Before := os.Stdout
	h, _, _ := installForTest(t)

	if h.ProtoOut() != fd1Before {
		t.Fatal("ProtoOut is not the captured real fd 1 (capture-before-replace violated)")
	}
	if os.Stdout == fd1Before {
		t.Fatal("os.Stdout was not replaced by Install")
	}
	if os.Stdout != h.w {
		t.Fatal("os.Stdout is not the hijack pipe write end")
	}
}

// TestStrayWriteCapturedTaggedCountedNotOnProto is the central acceptance test
// (criteria 1, 2, 3). With fd 1 redirected so we can read the protocol channel,
// it installs the hijack, writes a stray fmt.Println (which goes to the hijacked
// os.Stdout) and a protocol frame to ProtoOut, then asserts:
//   - the stray line appears on stderr as a JSON "stdout_leak" record,
//   - its bytes are counted (both the internal atomic and the Prometheus-facing
//     counter),
//   - the stray bytes do NOT appear on the protocol channel,
//   - the protocol bytes DO appear on the protocol channel (real fd 1).
func TestStrayWriteCapturedTaggedCountedNotOnProto(t *testing.T) {
	const stray = "stray debug line"
	const frame = `{"jsonrpc":"2.0","id":1,"result":{}}`

	logBuf := &bytes.Buffer{}
	counter := &countingCounter{}
	var h *Hijack

	protoBytes := withRedirectedFd1(t, func() {
		var err error
		h, err = Install(newTestLogger(logBuf), counter)
		if err != nil {
			t.Fatalf("Install: %v", err)
		}

		// A stray write from "anywhere in the process" lands in the drain.
		fmt.Println(stray)
		// The transport writes a protocol frame to the captured fd 1.
		if _, err := fmt.Fprintln(h.ProtoOut(), frame); err != nil {
			t.Fatalf("write proto: %v", err)
		}

		// stray line is "len+newline" bytes.
		waitForLeakBytes(t, h, uint64(len(stray)+1))

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := h.Restore(ctx); err != nil {
			t.Fatalf("Restore: %v", err)
		}
	})

	// The protocol channel carries the frame and ONLY the frame.
	got := strings.TrimSpace(string(protoBytes))
	if got != frame {
		t.Fatalf("protocol channel = %q, want exactly the frame %q", got, frame)
	}
	if strings.Contains(string(protoBytes), stray) {
		t.Fatal("stray text leaked onto the protocol channel — frame corruption")
	}

	// The stray line surfaced on stderr as a structured stdout_leak record.
	rec := findLeakRecord(t, logBuf.Bytes(), stray)
	if rec[obs.FieldEvent] != obs.EventStdoutLeak {
		t.Fatalf("event = %v, want %q", rec[obs.FieldEvent], obs.EventStdoutLeak)
	}
	if rec["msg"] != "stdout leak" {
		t.Fatalf("msg = %v, want %q", rec["msg"], "stdout leak")
	}

	// Bytes are counted, both internally and toward the Prometheus counter.
	want := uint64(len(stray) + 1)
	if h.LeakBytes() != want {
		t.Fatalf("LeakBytes = %d, want %d", h.LeakBytes(), want)
	}
	if counter.total() != want {
		t.Fatalf("counter = %d, want %d", counter.total(), want)
	}
}

// findLeakRecord scans NDJSON log output for the first "stdout_leak" record
// whose text field contains want, returning the decoded object. It fails the
// test if no such record exists.
func findLeakRecord(t *testing.T, logJSON []byte, want string) map[string]any {
	t.Helper()
	sc := bufio.NewScanner(bytes.NewReader(logJSON))
	sc.Buffer(make([]byte, 0, 128*1024), 1024*1024)
	for sc.Scan() {
		var rec map[string]any
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			continue
		}
		if rec[obs.FieldEvent] != obs.EventStdoutLeak {
			continue
		}
		if text, ok := rec["text"].(string); ok && strings.Contains(text, want) {
			return rec
		}
	}
	t.Fatalf("no stdout_leak record containing %q in log output:\n%s", want, logJSON)
	return nil
}

// TestRestoreLeavesStdoutExactlyAsBefore covers the reversibility property
// (install → restore is a no-op on os.Stdout): after Restore, os.Stdout is the
// exact handle it was before Install, so an in-process host is undisturbed.
func TestRestoreLeavesStdoutExactlyAsBefore(t *testing.T) {
	before := os.Stdout

	h, _, _ := installForTest(t)
	if os.Stdout == before {
		t.Fatal("Install did not replace os.Stdout")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.Restore(ctx); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if os.Stdout != before {
		t.Fatal("Restore did not put os.Stdout back exactly as it was")
	}
}

// TestRestoreIsIdempotent asserts a second Restore is a harmless no-op, so a
// deferred Restore plus an explicit shutdown Restore does not double-close the
// pipe or panic.
func TestRestoreIsIdempotent(t *testing.T) {
	h, _, _ := installForTest(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	first := h.Restore(ctx)
	second := h.Restore(ctx)
	if first != nil {
		t.Fatalf("first Restore: %v", first)
	}
	if second != nil {
		t.Fatalf("second Restore should be a no-op, got %v", second)
	}
}

// TestNoDeadlockOnLargeVolume writes far more than any OS pipe buffer through the
// hijacked stdout. If the drainer ever stopped reading, the writer would block
// forever and the test would time out; passing proves the drainer always drains
// (the ADR-011 deadlock hazard). Every byte is counted.
func TestNoDeadlockOnLargeVolume(t *testing.T) {
	h, _, counter := installForTest(t)

	const lines = 20000
	line := strings.Repeat("x", 200)
	var want uint64
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range lines {
			n, _ := fmt.Fprintln(os.Stdout, line)
			atomic.AddUint64(&want, uint64(n))
		}
	}()

	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("writing to hijacked stdout blocked: drainer stalled (deadlock)")
	}

	total := atomic.LoadUint64(&want)
	waitForLeakBytes(t, h, total)
	if counter.total() != total {
		t.Fatalf("counter = %d, want %d bytes drained", counter.total(), total)
	}
}

// TestOversizedLineTruncatedCountedNoBlock covers acceptance criterion 5: a
// single line longer than the 64 KiB bound is drained without blocking, every
// byte is counted, and at least one emitted record is marked truncated.
func TestOversizedLineTruncatedCountedNoBlock(t *testing.T) {
	h, logBuf, counter := installForTest(t)

	// One line of 200 KiB with no interior newline, then a terminator.
	const size = 200 * 1024
	big := strings.Repeat("A", size)

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = fmt.Fprintln(os.Stdout, big)
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("oversized-line write blocked: drainer did not keep draining")
	}

	want := uint64(size + 1) // + newline
	waitForLeakBytes(t, h, want)
	if counter.total() != want {
		t.Fatalf("counter = %d, want %d", counter.total(), want)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.Restore(ctx); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	if !hasTruncatedRecord(logBuf.Bytes()) {
		t.Fatal("expected at least one truncated stdout_leak record for the oversized line")
	}
}

// hasTruncatedRecord reports whether any stdout_leak log record carries
// truncated=true.
func hasTruncatedRecord(logJSON []byte) bool {
	sc := bufio.NewScanner(bytes.NewReader(logJSON))
	sc.Buffer(make([]byte, 0, 128*1024), 1024*1024)
	for sc.Scan() {
		var rec map[string]any
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			continue
		}
		if rec[obs.FieldEvent] != obs.EventStdoutLeak {
			continue
		}
		if tr, ok := rec["truncated"].(bool); ok && tr {
			return true
		}
	}
	return false
}

// TestConcurrentStrayWrites drives stray writes from many goroutines at once and
// asserts every byte is accounted for. Run under -race, it proves the drain and
// counters are safe against writes arriving from any goroutine at any time
// (ADR-011 concurrency guarantee).
func TestConcurrentStrayWrites(t *testing.T) {
	h, _, counter := installForTest(t)

	const (
		writers        = 16
		linesPerWriter = 500
	)
	var want uint64
	var wg sync.WaitGroup
	wg.Add(writers)
	for w := range writers {
		go func(id int) {
			defer wg.Done()
			line := fmt.Sprintf("goroutine-%d-leak", id)
			for range linesPerWriter {
				n, _ := fmt.Fprintln(os.Stdout, line)
				atomic.AddUint64(&want, uint64(n))
			}
		}(w)
	}
	wg.Wait()

	total := atomic.LoadUint64(&want)
	waitForLeakBytes(t, h, total)
	if counter.total() != total {
		t.Fatalf("counter = %d, want %d bytes", counter.total(), total)
	}
}

// TestCleanRunLeaksZero asserts the CI-assertable property (acceptance criterion
// 2 / MOCK-105.4): with no stray write, LeakBytes and the counter are zero after
// install and restore. This is the assertion test/functional/TestNoStdoutLeak
// makes after a full scenario.
func TestCleanRunLeaksZero(t *testing.T) {
	h, _, counter := installForTest(t)

	// Protocol writes go to fd 1, not the drain, so they never count as leaks.
	_, _ = fmt.Fprintln(h.ProtoOut(), `{"jsonrpc":"2.0","id":1,"result":{}}`)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.Restore(ctx); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	if h.LeakBytes() != 0 {
		t.Fatalf("LeakBytes = %d, want 0 on a clean run", h.LeakBytes())
	}
	if counter.total() != 0 {
		t.Fatalf("counter = %d, want 0 on a clean run", counter.total())
	}
}

// TestRestoreBoundedByContext asserts shutdown cannot hang: even if the drainer
// were wedged, a canceled context makes Restore return. Here the drain is
// healthy, so Restore returns nil promptly; the test's value is that Restore
// honours ctx and always joins the goroutine (goleak verifies no leak).
func TestRestoreBoundedByContext(t *testing.T) {
	h, _, _ := installForTest(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.Restore(ctx); err != nil {
		t.Fatalf("Restore: %v", err)
	}
}

// TestMetricsLeakCounterAdapter exercises the obs.Metrics adapter so the wiring
// cmd/ uses in production is covered: bytes fed through MetricsLeakCounter reach
// the Prometheus counter.
func TestMetricsLeakCounterAdapter(t *testing.T) {
	b, err := obs.New(obs.Config{LogWriter: io.Discard, LogLevel: slog.LevelDebug})
	if err != nil {
		t.Fatalf("obs.New: %v", err)
	}
	lc := MetricsLeakCounter{Metrics: b.Metrics()}
	lc.Add(42)
	// The counter is exposed via StdoutLeakCounter; gather and check its value.
	if got := counterValue(t, b); got != 42 {
		t.Fatalf("mcpmock_stdout_leak_bytes_total = %v, want 42", got)
	}
}

// counterValue gathers the stdout-leak counter value from the bundle's registry.
func counterValue(t *testing.T, b *obs.Bundle) float64 {
	t.Helper()
	mfs, err := b.Metrics().Registry().Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() != "mcpmock_stdout_leak_bytes_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			return m.GetCounter().GetValue()
		}
	}
	t.Fatal("mcpmock_stdout_leak_bytes_total not found in registry")
	return 0
}
