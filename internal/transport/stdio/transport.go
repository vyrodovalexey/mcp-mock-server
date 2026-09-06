package stdio

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"

	"github.com/vyrodovalexey/mcp-mock-server/internal/engine"
)

// ErrNoStreams reports that a [Transport] was constructed without both an input
// reader and an output writer. There is deliberately no default to
// os.Stdin/os.Stdout (ADR-011 layer 2): defaulting would let the library form of
// the transport write protocol bytes to the real fd 1, defeating the cmd/-only
// stdout hijack. Constructing without explicit streams is an error, never a
// silent fallback.
var ErrNoStreams = errors.New("stdio: WithStdio requires a non-nil reader and writer")

// ErrNoPipeline reports that a [Transport] was constructed without an engine
// pipeline and instance to drive. A transport with no pipeline can decode frames
// but has nothing to answer them, which is a construction bug, not a runtime
// condition.
var ErrNoPipeline = errors.New("stdio: a pipeline and instance are required")

// ErrAlreadyServing reports a second [Transport.Serve] call on a Transport that
// is already (or was already) serving. A Transport is single-use per stream
// pair.
var ErrAlreadyServing = errors.New("stdio: transport already serving")

// Config configures a stdio [Transport]. The reader and writer are supplied
// explicitly by the caller — cmd/mcpmock passes os.Stdin and the captured
// protoOut handle (the real fd 1, before the hijack replaces os.Stdout); an
// embedded test passes an io.Pipe. The transport writes protocol frames through
// Out and NOTHING ELSE writes to Out, which is precisely what lets TASK-021
// hijack os.Stdout without touching this package (ADR-011).
type Config struct {
	// In is the request stream (the peer's stdout → our stdin). Required.
	In io.Reader
	// Out is the response stream (our stdout → the peer's stdin). It receives
	// protocol frames only. Required. The transport never references os.Stdout;
	// the caller injects the real handle so the hijack can substitute a pipe.
	Out io.Writer
	// Instance is the single engine instance this stdio transport serves.
	// Phase 1 is single-instance (the registry that maps many mounts is HTTP's
	// concern and a later phase); stdio serves one instance's pipeline.
	// Required.
	Instance engine.Instance
	// Pipeline drives the nine-stage engine for each decoded request. Required.
	Pipeline *engine.Pipeline
	// PoolSize bounds concurrent in-flight requests (the worker-pool bound). A
	// value <= 0 selects GOMAXPROCS. The bound is what keeps MOCK-256
	// interleaving and the MOCK-901 goroutine budget bounded: at most PoolSize
	// requests run at once regardless of how fast the peer sends.
	PoolSize int
	// MaxLineBytes bounds one inbound frame. A value <= 0 selects 16 MiB. A
	// larger frame is rejected as [ErrLineTooLong] and the reader stops, so a
	// peer cannot exhaust memory with an unterminated stream.
	MaxLineBytes int
	// Peer is the peer label recorded on journal records. Optional; defaults to
	// "stdio".
	Peer string
}

// Transport is the newline-delimited JSON-RPC stdio transport with a bounded
// worker pool (MOCK-102 stdio, MOCK-256 interleaving). It multiplexes many
// concurrent requests over a single duplex channel, tagging each response frame
// with its originating JSON-RPC id so interleaved frames on the one output
// channel are correctly attributed.
//
// # Goroutine budget (architecture.md §7.1)
//
// A running Transport owns exactly two long-lived goroutines — one reader
// demultiplexing In, one writer serializing frames to Out — plus at most
// PoolSize worker goroutines. After [Transport.Serve] returns, zero remain. The
// reader runs on Serve's own goroutine, so the "two goroutines" are that caller
// goroutine acting as reader plus the single writer goroutine.
//
// # Framing and serialization guarantee (MOCK-256)
//
// Every response frame is handed to a SINGLE writer goroutine over one channel
// and written with writeFrame in one Write call (payload + '\n'). Because there
// is exactly one writer and each frame is one atomic Write, two concurrent
// requests can never interleave their bytes: a frame is emitted whole or not at
// all. Frames carry their JSON-RPC id (jsonrpc.ID, preserved byte-for-byte) so a
// peer demultiplexes responses by id.
//
// # Injected writer (ADR-011)
//
// Protocol output goes through Config.Out, an injected io.Writer — the transport
// NEVER references os.Stdout. cmd/mcpmock (TASK-021) captures the real fd 1,
// hands it here as Out, then replaces os.Stdout with a stderr-tagging drain, so
// a stray fmt.Println anywhere in the process becomes a counted diagnostic
// instead of frame-stream corruption. If this transport hardcoded os.Stdout the
// hijack could not do its job; it does not.
//
// # Cancellation (MOCK-212)
//
// Each in-flight request holds a context in a request table. When In reaches EOF
// (the peer closed the pipe / the parent process exited), the reader cancels
// every in-flight request context; the engine observes the cancellation at
// stages 8-9 and journals a record with elapsed time rather than emitting a
// response nobody will read. Phase 1 wires cancellation on EOF only;
// per-id cancel-notification cancellation is a Phase 2 use of the same
// table.
type Transport struct {
	cfg     Config
	peer    string
	writeCh chan []byte

	// reqTable holds the cancel func of every in-flight request, keyed by a
	// monotonic token, so EOF (and, in Phase 2, a cancel notification) can
	// cancel in-flight work. It is guarded by mu because the reader and the
	// workers mutate it concurrently.
	mu       sync.Mutex
	reqTable map[uint64]context.CancelFunc
	nextTok  uint64

	// started guards against a second Serve on the same Transport.
	started atomic.Bool
}

// New validates cfg and returns a ready [Transport]. It binds no stream and
// starts no goroutine (that is [Transport.Serve]'s job), so constructing a
// Transport is cheap and side-effect-free. It returns [ErrNoStreams] when a
// stream is missing and [ErrNoPipeline] when the pipeline or instance is missing
// — the no-defaulting rule (ADR-011 layer 2) enforced as a returned error.
func New(cfg Config) (*Transport, error) {
	if cfg.In == nil || cfg.Out == nil {
		return nil, ErrNoStreams
	}
	if cfg.Pipeline == nil || cfg.Instance == nil {
		return nil, ErrNoPipeline
	}
	if cfg.PoolSize <= 0 {
		cfg.PoolSize = defaultPoolSize()
	}
	peer := cfg.Peer
	if peer == "" {
		peer = "stdio"
	}
	return &Transport{
		cfg:      cfg,
		peer:     peer,
		reqTable: make(map[uint64]context.CancelFunc),
	}, nil
}

// PoolSize reports the effective worker-pool bound after defaulting, for a test
// or a caller that needs to assert the bound.
func (t *Transport) PoolSize() int { return t.cfg.PoolSize }

// Serve runs the stdio transport until In reaches EOF, ctx is canceled, or a
// fatal output-write error occurs, then shuts down cleanly: it stops accepting
// new requests, cancels in-flight ones, drains the writer so already-produced
// frames are not dropped, and returns once the writer goroutine and every worker
// have exited. It returns nil on a clean EOF/context-cancel shutdown and a
// non-nil error only for an input read failure or an output write failure the
// caller should surface.
//
// Serve may be called once per Transport; a second call returns
// [ErrAlreadyServing].
func (t *Transport) Serve(ctx context.Context) error {
	if !t.started.CompareAndSwap(false, true) {
		return ErrAlreadyServing
	}

	// serveCtx bounds the reader, the pool and the writer. Canceling it (on EOF,
	// on the caller's ctx, or on a write error) is the single shutdown signal.
	serveCtx, cancelServe := context.WithCancel(ctx)
	defer cancelServe()

	// The writer channel is unbuffered so a submitted frame is handed directly
	// to the writer goroutine; one writer goroutine is the serialization point
	// (MOCK-256).
	t.writeCh = make(chan []byte)

	workers := newPool(serveCtx, t.cfg.PoolSize)

	writeErrCh := make(chan error, 1)
	var writerWG sync.WaitGroup
	writerWG.Add(1)
	go func() {
		defer writerWG.Done()
		// A write failure cancels the whole transport so the reader stops
		// pulling from a peer whose output side is broken.
		err := t.writeLoop()
		if err != nil {
			cancelServe()
		}
		writeErrCh <- err
	}()

	// The reader runs on THIS goroutine (Serve's caller), so the two-goroutine
	// budget is this reader + the writer, plus the pool.
	readErr := t.readLoop(serveCtx, workers, cancelServe)

	// Reader has stopped (EOF, ctx cancel, or shutdown). Cancel in-flight work
	// so it journals cancellation (MOCK-212), then drain the pool.
	t.cancelAllInFlight()
	workers.stop()

	// All frame producers (workers) are gone; close the writer channel so the
	// writer drains remaining frames and exits without dropping them.
	close(t.writeCh)
	writerWG.Wait()

	return firstErr(readErr, <-writeErrCh)
}

// readLoop reads frames from In and submits each to the worker pool until EOF,
// context cancellation, or a submit the pool rejects (shutdown). On EOF it
// cancels the serve context so the whole transport shuts down. It returns nil on
// a clean EOF/cancel and a wrapped error on a non-EOF read failure (including a
// bounded-line overflow).
func (t *Transport) readLoop(
	ctx context.Context, workers *pool, cancelServe context.CancelFunc,
) error {
	fr := newFrameReader(t.cfg.In, t.cfg.MaxLineBytes)
	for {
		// Poll for shutdown without inspecting an error value: a canceled serve
		// context ends the loop cleanly (it is not a failure to surface).
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		frame, err := fr.readFrame()
		if err != nil {
			// EOF, a bounded-line overflow, or a genuine read error all end the
			// read loop. EOF is a clean shutdown that cancels in-flight work
			// (MOCK-212); the others are surfaced. In every case cancelServe
			// stops the rest.
			cancelServe()
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if !t.dispatch(ctx, workers, frame) {
			// The pool refused the task: the transport is shutting down.
			return nil
		}
	}
}

// dispatch submits one request to the pool for handling. It returns false when
// the pool is shutting down (so the reader stops). Submission BLOCKS when the
// pool is saturated, which is the backpressure that bounds in-flight concurrency
// to the pool size and propagates flow control to the peer through the pipe.
func (t *Transport) dispatch(ctx context.Context, workers *pool, raw []byte) bool {
	return workers.submit(func(workerCtx context.Context) {
		t.handleOne(ctx, workerCtx, raw)
	})
}

// handleOne handles a single decoded request frame end to end: it registers a
// per-request cancelable context in the request table (so EOF can cancel it),
// builds a transport-neutral Exchange, and drives the engine pipeline through a
// muxSink that forwards the response frame to the single writer. The per-request
// context derives from the SERVE context — not the worker context — so returning
// to the pool does not cancel the request, but shutdown/EOF does.
func (t *Transport) handleOne(serveCtx, workerCtx context.Context, raw []byte) {
	if workerCtx.Err() != nil {
		return
	}
	reqCtx, cancel := context.WithCancel(serveCtx)
	tok := t.register(cancel)
	defer t.deregister(tok, cancel)

	ex := &engine.Exchange{
		Ctx:       reqCtx,
		Instance:  t.cfg.Instance,
		Transport: engine.KindStdio,
		Peer:      t.peer,
		Raw:       raw,
	}
	sink := newMuxSink(reqCtx, t.writeCh)
	// A pipeline error is a sink-forward failure caused by shutdown (the writer
	// channel stopped accepting because serve was canceled). It is not a wire
	// error — wire faults are encoded into a response frame by the engine and
	// forwarded like any other. There is nothing to surface here: shutdown is
	// already in progress, so drop the shutdown-induced sink error.
	_ = t.cfg.Pipeline.Handle(reqCtx, ex, sink)
}

// register adds cancel to the request table under a fresh token and returns it.
func (t *Transport) register(cancel context.CancelFunc) uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.nextTok++
	tok := t.nextTok
	t.reqTable[tok] = cancel
	return tok
}

// deregister removes tok from the request table and cancels its context to
// release resources. Canceling an already-completed request is a harmless no-op.
func (t *Transport) deregister(tok uint64, cancel context.CancelFunc) {
	t.mu.Lock()
	delete(t.reqTable, tok)
	t.mu.Unlock()
	cancel()
}

// cancelAllInFlight cancels every request context currently in the table. It is
// the EOF/shutdown path (MOCK-212): in-flight requests observe cancellation,
// stop work, and journal a record with elapsed time. It does not clear the table
// — each request deregisters itself as it unwinds.
func (t *Transport) cancelAllInFlight() {
	t.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(t.reqTable))
	for _, c := range t.reqTable {
		cancels = append(cancels, c)
	}
	t.mu.Unlock()
	for _, c := range cancels {
		c()
	}
}

// writeLoop is the single writer goroutine (MOCK-256 serialization point). It
// drains the writer channel, writing each frame to Config.Out with writeFrame in
// one atomic Write, until the channel is closed (all producers gone). It returns
// the first output-write error, which tears the transport down.
func (t *Transport) writeLoop() error {
	var firstWriteErr error
	for message := range t.writeCh {
		if firstWriteErr != nil {
			// After a write failure, keep draining so producers do not block on
			// a full channel during shutdown, but do not attempt more writes.
			continue
		}
		if err := writeFrame(t.cfg.Out, message); err != nil {
			firstWriteErr = err
		}
	}
	return firstWriteErr
}

// firstErr returns the first non-nil error of a, b, treating a clean shutdown
// (nil) as no error. It lets Serve prefer the read-side cause over the write-side
// one when both fire during teardown.
func firstErr(a, b error) error {
	if a != nil {
		return a
	}
	return b
}
