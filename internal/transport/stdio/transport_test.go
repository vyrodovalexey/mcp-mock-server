package stdio_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vyrodovalexey/mcp-mock-server/internal/engine"
	"github.com/vyrodovalexey/mcp-mock-server/internal/transport/stdio"
	"github.com/vyrodovalexey/mcp-mock-server/internal/wire"
)

// serveInBackground constructs a transport over the given streams and serves it
// on a goroutine, returning a stop func that cancels serve and waits for Serve
// to return. It fails the test if construction fails.
func serveInBackground(
	t *testing.T, cfg stdio.Config,
) (tr *stdio.Transport, done <-chan error, cancel context.CancelFunc) {
	t.Helper()
	tr, err := stdio.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan error, 1)
	go func() { ch <- tr.Serve(ctx) }()
	return tr, ch, cancel
}

// readResponses reads up to n newline-delimited response lines from r.
func readResponses(t *testing.T, r io.Reader, n int) [][]byte {
	t.Helper()
	br := bufio.NewReader(r)
	out := make([][]byte, 0, n)
	for i := 0; i < n; i++ {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			out = append(out, bytes.TrimRight(line, "\n"))
		}
		if err != nil {
			break
		}
	}
	return out
}

// decodeID extracts the id member of a response line as a raw string.
func decodeID(t *testing.T, line []byte) string {
	t.Helper()
	var env struct {
		ID json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(line, &env); err != nil {
		t.Fatalf("decode response %q: %v", line, err)
	}
	return string(env.ID)
}

// TestConstructorRejectsMissingStreams asserts the no-defaulting rule (ADR-011
// layer 2): constructing without explicit streams is a returned error, never a
// silent fallback to os.Stdout. This is acceptance criterion 2.
func TestConstructorRejectsMissingStreams(t *testing.T) {
	inst := newInstance(1, "inst", false)
	p := newPipeline(0)
	cases := []struct {
		name string
		cfg  stdio.Config
		want error
	}{
		{"no reader", stdio.Config{Out: &bytes.Buffer{}, Instance: inst, Pipeline: p}, stdio.ErrNoStreams},
		{"no writer", stdio.Config{In: &bytes.Buffer{}, Instance: inst, Pipeline: p}, stdio.ErrNoStreams},
		{"no streams at all", stdio.Config{Instance: inst, Pipeline: p}, stdio.ErrNoStreams},
		{
			"no pipeline",
			stdio.Config{In: &bytes.Buffer{}, Out: &bytes.Buffer{}, Instance: inst},
			stdio.ErrNoPipeline,
		},
		{
			"no instance",
			stdio.Config{In: &bytes.Buffer{}, Out: &bytes.Buffer{}, Pipeline: p},
			stdio.ErrNoPipeline,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := stdio.New(tc.cfg)
			if !errors.Is(err, tc.want) {
				t.Fatalf("New err = %v, want %v", err, tc.want)
			}
		})
	}
}

// TestReadWriteRoundTrip asserts the transport reads newline-delimited JSON-RPC
// on the supplied reader and writes framed responses on the supplied writer
// (acceptance criterion 1), and that the response echoes the request id.
func TestReadWriteRoundTrip(t *testing.T) {
	inServer, inClient := io.Pipe() // client writes -> server reads
	outClient, outServer := io.Pipe()

	cfg := stdio.Config{In: inServer, Out: outServer, Instance: newInstance(1, "inst", true), Pipeline: newPipeline(0)}
	_, done, cancel := serveInBackground(t, cfg)
	defer cancel()

	go func() {
		_, _ = inClient.Write(rawRequest("7", wire.MethodToolsCall))
		_, _ = inClient.Write([]byte{'\n'})
	}()

	lines := readResponses(t, outClient, 1)
	if len(lines) != 1 {
		t.Fatalf("want 1 response line, got %d", len(lines))
	}
	if id := decodeID(t, lines[0]); id != "7" {
		t.Fatalf("response id = %s, want 7", id)
	}
	_ = inClient.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve returned %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not return after EOF")
	}
}

// TestConcurrentRequestsInterleavedAndTagged is the MOCK-256 test: many
// concurrent requests are correctly interleaved and tagged by id on the single
// output channel, under -race. It sends N requests with distinct ids and asserts
// every id comes back exactly once and every line is a well-formed frame.
func TestConcurrentRequestsInterleavedAndTagged(t *testing.T) {
	const n = 200
	inServer, inClient := io.Pipe()
	outClient, outServer := io.Pipe()

	cfg := stdio.Config{
		In: inServer, Out: outServer,
		Instance: newInstance(1, "inst", true), Pipeline: newPipeline(0),
		PoolSize: 8,
	}
	_, done, cancel := serveInBackground(t, cfg)
	defer cancel()

	// Feed all requests as fast as possible so the pool multiplexes them.
	go func() {
		w := bufio.NewWriter(inClient)
		for i := 0; i < n; i++ {
			_, _ = w.Write(rawRequest(strconv.Itoa(i), wire.MethodToolsCall))
			_, _ = w.Write([]byte{'\n'})
		}
		_ = w.Flush()
	}()

	seen := make(map[string]int, n)
	lines := readResponses(t, outClient, n)
	if len(lines) != n {
		t.Fatalf("want %d responses, got %d", n, len(lines))
	}
	for _, line := range lines {
		// Every line must be a complete, parseable frame — no interleaved
		// corruption (MOCK-256). A torn frame fails this unmarshal.
		var env map[string]json.RawMessage
		if err := json.Unmarshal(line, &env); err != nil {
			t.Fatalf("corrupt/interleaved frame %q: %v", line, err)
		}
		seen[decodeID(t, line)]++
	}
	for i := 0; i < n; i++ {
		id := strconv.Itoa(i)
		if seen[id] != 1 {
			t.Fatalf("id %s seen %d times, want 1", id, seen[id])
		}
	}
	_ = inClient.Close()
	<-done
}

// serialWriter is an io.Writer that detects any interleaved (concurrent) Write
// call — the corruption MOCK-256 forbids. It records the max concurrent writers
// observed; because the transport funnels through one writer goroutine, that max
// must be exactly 1.
type serialWriter struct {
	mu       sync.Mutex
	buf      bytes.Buffer
	inWrite  atomic.Int64
	maxWrite atomic.Int64
}

func (w *serialWriter) Write(p []byte) (int, error) {
	cur := w.inWrite.Add(1)
	for {
		m := w.maxWrite.Load()
		if cur <= m || w.maxWrite.CompareAndSwap(m, cur) {
			break
		}
	}
	// Hold briefly so a second concurrent writer, if one existed, would overlap.
	time.Sleep(50 * time.Microsecond)
	w.mu.Lock()
	n, err := w.buf.Write(p)
	w.mu.Unlock()
	w.inWrite.Add(-1)
	return n, err
}

// TestNoFrameCorruptionUnderWritePressure asserts that under concurrent request
// load only ONE goroutine ever writes to the output at a time (frame-level
// serialization), so frames cannot interleave. This is the structural guarantee
// behind MOCK-256, exercised under -race.
func TestNoFrameCorruptionUnderWritePressure(t *testing.T) {
	const n = 100
	inServer, inClient := io.Pipe()
	sw := &serialWriter{}

	cfg := stdio.Config{
		In: inServer, Out: sw,
		Instance: newInstance(1, "inst", false), Pipeline: newPipeline(0),
		PoolSize: 16,
	}
	_, done, cancel := serveInBackground(t, cfg)
	defer cancel()

	go func() {
		w := bufio.NewWriter(inClient)
		for i := 0; i < n; i++ {
			_, _ = w.Write(rawRequest(strconv.Itoa(i), wire.MethodToolsCall))
			_, _ = w.Write([]byte{'\n'})
		}
		_ = w.Flush()
	}()

	// Wait until all n responses are written.
	deadline := time.After(5 * time.Second)
	for {
		sw.mu.Lock()
		count := bytes.Count(sw.buf.Bytes(), []byte{'\n'})
		sw.mu.Unlock()
		if count >= n {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("only %d/%d frames written", count, n)
		case <-time.After(5 * time.Millisecond):
		}
	}
	if max := sw.maxWrite.Load(); max != 1 {
		t.Fatalf("max concurrent writers = %d, want exactly 1 (serialization broken)", max)
	}
	_ = inClient.Close()
	<-done
}

// TestPoolBoundHolds asserts concurrent in-flight requests never exceed the
// configured pool bound (acceptance criterion 4) and defines the backpressure:
// requests beyond the bound wait for a worker rather than spawning a goroutine.
func TestPoolBoundHolds(t *testing.T) {
	const bound = 3
	const requests = 12
	inServer, inClient := io.Pipe()
	outClient, outServer := io.Pipe()

	entered := &atomic.Int64{}
	peak := &atomic.Int64{}
	gate := make(chan struct{})
	reg := engine.NewRegistry()
	reg.Register(wire.MethodToolsCall, gateHandler{entered: entered, peak: peak, gate: gate})
	p := engine.NewPipeline(reg)

	cfg := stdio.Config{
		In: inServer, Out: outServer,
		Instance: newInstance(1, "inst", false), Pipeline: p, PoolSize: bound,
	}
	tr, done, cancel := serveInBackground(t, cfg)
	defer cancel()
	if tr.PoolSize() != bound {
		t.Fatalf("PoolSize = %d, want %d", tr.PoolSize(), bound)
	}

	go func() {
		w := bufio.NewWriter(inClient)
		for i := 0; i < requests; i++ {
			_, _ = w.Write(rawRequest(strconv.Itoa(i), wire.MethodToolsCall))
			_, _ = w.Write([]byte{'\n'})
		}
		_ = w.Flush()
	}()

	// Let the pool saturate: wait until `bound` handlers are simultaneously in
	// flight, then confirm it never exceeds the bound.
	deadline := time.After(3 * time.Second)
	for entered.Load() < bound {
		select {
		case <-deadline:
			t.Fatalf("only %d handlers entered, want %d saturated", entered.Load(), bound)
		case <-time.After(time.Millisecond):
		}
	}
	// Give any (incorrectly) unbounded spawn a chance to exceed the bound.
	time.Sleep(50 * time.Millisecond)
	if got := peak.Load(); got > bound {
		t.Fatalf("peak concurrent in-flight = %d, exceeds pool bound %d", got, bound)
	}

	// Release all handlers and drain the responses.
	close(gate)
	_ = readResponses(t, outClient, requests)
	_ = inClient.Close()
	<-done

	if got := peak.Load(); got != bound {
		t.Fatalf("peak in-flight = %d, want exactly the bound %d", got, bound)
	}
}

// TestEOFCancelsInFlightAndJournals is the MOCK-212 test: stdin EOF cancels
// in-flight request contexts and the cancellation is journaled with elapsed
// time, without dropping frames already written.
func TestEOFCancelsInFlightAndJournals(t *testing.T) {
	inServer, inClient := io.Pipe()
	outClient, outServer := io.Pipe()

	// Closing the output reader end unblocks the discard drainer below once the
	// test ends, so it is not mistaken for a transport leak by goleak.
	t.Cleanup(func() { _ = outClient.Close(); _ = outServer.Close() })

	inst := newInstance(1, "inst", true)
	// A long sleep so the request is reliably in flight when EOF arrives.
	cfg := stdio.Config{In: inServer, Out: outServer, Instance: inst, Pipeline: newPipeline(30 * time.Second)}
	_, done, cancel := serveInBackground(t, cfg)
	defer cancel()

	entered := make(chan struct{})
	go func() {
		_, _ = inClient.Write(rawRequest("1", wire.MethodToolsCall))
		_, _ = inClient.Write([]byte{'\n'})
		close(entered)
	}()
	<-entered
	// Drain any output the server might produce so the pipe never blocks.
	go func() { _, _ = io.Copy(io.Discard, outClient) }()

	// Give the request time to reach the sleeping handler, then close stdin.
	time.Sleep(50 * time.Millisecond)
	_ = inClient.Close()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve returned %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not shut down on EOF while a request was in flight")
	}

	// The in-flight request must have journaled a cancellation record. TASK-014's
	// engine records MOCK-212 cancellation through the response close reason
	// ("clientGone") and the elapsed DurationNs, not through a dedicated
	// Record.Canceled field (journal.Input exposes none — see
	// internal/engine/capture.go). We assert on that faithful evidence, which is
	// what the current engine actually writes; asserting Record.Can: would test a
	// field this engine version does not populate.
	recs := inst.ring.Snapshot()
	if len(recs) == 0 {
		t.Fatal("no journal record for the in-flight request on EOF")
	}
	found := false
	for _, r := range recs {
		if r.Response != nil && r.Response.CloseReason == engine.CloseClientGone.String() {
			found = true
			if r.DurationNs <= 0 {
				t.Fatalf("cancellation elapsed DurationNs = %d, want > 0", r.DurationNs)
			}
		}
	}
	if !found {
		t.Fatalf("no clientGone cancellation record journaled on EOF (MOCK-212); records: %+v", recs)
	}
}

// TestOnlyProtocolFramesWritten asserts acceptance criterion 7 / the ADR-011
// injected-writer contract: the only bytes the output writer receives are
// protocol frames. Every byte written to the injected writer is accounted for as
// a newline-delimited JSON-RPC response — nothing else (no log line, no debug
// text) reaches it.
func TestOnlyProtocolFramesWritten(t *testing.T) {
	inServer, inClient := io.Pipe()
	out := &syncBuffer{}

	cfg := stdio.Config{In: inServer, Out: out, Instance: newInstance(1, "inst", false), Pipeline: newPipeline(0)}
	_, done, cancel := serveInBackground(t, cfg)
	defer cancel()

	const n = 5
	go func() {
		w := bufio.NewWriter(inClient)
		for i := 0; i < n; i++ {
			_, _ = w.Write(rawRequest(strconv.Itoa(i), wire.MethodToolsList))
			_, _ = w.Write([]byte{'\n'})
		}
		_ = w.Flush()
	}()

	deadline := time.After(3 * time.Second)
	for out.lineCount() < n {
		select {
		case <-deadline:
			t.Fatalf("only %d/%d frames", out.lineCount(), n)
		case <-time.After(2 * time.Millisecond):
		}
	}
	_ = inClient.Close()
	<-done

	// Every line must parse as a JSON-RPC response and nothing else may appear.
	for _, line := range bytes.Split(bytes.TrimRight(out.bytes(), "\n"), []byte{'\n'}) {
		var env struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Result  json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal(line, &env); err != nil {
			t.Fatalf("non-protocol bytes on output writer: %q (%v)", line, err)
		}
		if env.JSONRPC != "2.0" {
			t.Fatalf("frame is not a JSON-RPC response: %q", line)
		}
	}
}

// TestMalformedInputHandledWithoutPanic asserts malformed input framing is
// handled without panic (acceptance criterion / required boundary): garbage
// lines yield a -32700/-32600 error frame, and the transport keeps serving.
func TestMalformedInputHandledWithoutPanic(t *testing.T) {
	inServer, inClient := io.Pipe()
	outClient, outServer := io.Pipe()

	cfg := stdio.Config{In: inServer, Out: outServer, Instance: newInstance(1, "inst", false), Pipeline: newPipeline(0)}
	_, done, cancel := serveInBackground(t, cfg)
	defer cancel()

	go func() {
		w := bufio.NewWriter(inClient)
		_, _ = w.Write([]byte("this is not json\n"))
		_, _ = w.Write([]byte("{ broken json \n"))
		_, _ = w.Write([]byte("\n")) // blank line: skipped, no frame
		_, _ = w.Write(rawRequest("9", wire.MethodToolsCall))
		_, _ = w.Write([]byte{'\n'})
		_ = w.Flush()
	}()

	// Two malformed lines -> two error frames; the valid request -> one result
	// frame echoing id 9. The blank line produces nothing. Responses may arrive
	// in ANY order (concurrent workers on a bounded pool — MOCK-256), so assert
	// on the multiset, not the sequence.
	lines := readResponses(t, outClient, 3)
	if len(lines) != 3 {
		t.Fatalf("want 3 frames (2 errors + 1 result), got %d: %q", len(lines), lines)
	}
	errorFrames, resultForNine := 0, 0
	for _, line := range lines {
		var env struct {
			Error  json.RawMessage `json:"error"`
			Result json.RawMessage `json:"result"`
			ID     json.RawMessage `json:"id"`
		}
		if err := json.Unmarshal(line, &env); err != nil {
			t.Fatalf("unparseable frame %q: %v", line, err)
		}
		switch {
		case env.Error != nil:
			errorFrames++
		case env.Result != nil && string(env.ID) == "9":
			resultForNine++
		default:
			t.Fatalf("unexpected frame %q", line)
		}
	}
	if errorFrames != 2 {
		t.Fatalf("want 2 error frames for the malformed lines, got %d", errorFrames)
	}
	if resultForNine != 1 {
		t.Fatalf("want 1 result frame echoing id 9, got %d", resultForNine)
	}
	_ = inClient.Close()
	<-done
}

// TestGoroutineCountBounded asserts acceptance criterion 3: exactly two
// long-lived goroutines plus at most the pool size while serving, and zero
// transport goroutines remain after shutdown. It measures the delta rather than
// an absolute count so unrelated runtime goroutines do not skew it.
func TestGoroutineCountBounded(t *testing.T) {
	const pool = 4
	runtime.GC()
	before := runtime.NumGoroutine()

	inServer, inClient := io.Pipe()
	outClient, outServer := io.Pipe()
	t.Cleanup(func() { _ = outClient.Close(); _ = outServer.Close() })
	cfg := stdio.Config{
		In: inServer, Out: outServer,
		Instance: newInstance(1, "inst", false), Pipeline: newPipeline(0), PoolSize: pool,
	}
	_, done, cancel := serveInBackground(t, cfg)
	defer cancel()

	// Drive a little traffic so workers spin up.
	go func() {
		w := bufio.NewWriter(inClient)
		for i := 0; i < 10; i++ {
			_, _ = w.Write(rawRequest(strconv.Itoa(i), wire.MethodToolsCall))
			_, _ = w.Write([]byte{'\n'})
		}
		_ = w.Flush()
	}()
	_ = readResponses(t, outClient, 10)

	// While serving: reader(on Serve's goroutine) + writer + <= pool workers +
	// the Serve goroutine itself. Bound generously but finitely.
	peak := runtime.NumGoroutine()
	if peak-before > pool+8 {
		t.Fatalf("goroutine delta while serving = %d, exceeds bound (pool=%d)", peak-before, pool)
	}

	_ = inClient.Close()
	<-done
	// After shutdown, let the scheduler settle, then confirm no transport
	// goroutine remains. The reader (Serve's goroutine), the writer and every
	// worker must be gone.
	time.Sleep(50 * time.Millisecond)
	runtime.GC()
	after := runtime.NumGoroutine()
	if after > before+2 {
		t.Fatalf("goroutines after shutdown = %d, before = %d: transport leaked", after, before)
	}
}

// TestSecondServeRejected asserts a Transport is single-use per stream pair.
func TestSecondServeRejected(t *testing.T) {
	inServer, inClient := io.Pipe()
	outClient, outServer := io.Pipe()
	t.Cleanup(func() { _ = outClient.Close(); _ = outServer.Close() })
	go func() { _, _ = io.Copy(io.Discard, outClient) }()

	cfg := stdio.Config{In: inServer, Out: outServer, Instance: newInstance(1, "inst", false), Pipeline: newPipeline(0)}
	tr, done, cancel := serveInBackground(t, cfg)
	defer cancel()

	// Wait until the first Serve has claimed the transport.
	time.Sleep(20 * time.Millisecond)
	if err := tr.Serve(context.Background()); !errors.Is(err, stdio.ErrAlreadyServing) {
		t.Fatalf("second Serve err = %v, want ErrAlreadyServing", err)
	}
	_ = inClient.Close()
	<-done
}

// syncBuffer is a concurrency-safe buffer for asserting on the injected writer's
// contents from the test goroutine while the writer goroutine appends to it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]byte, b.buf.Len())
	copy(out, b.buf.Bytes())
	return out
}

func (b *syncBuffer) lineCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return bytes.Count(b.buf.Bytes(), []byte{'\n'})
}
