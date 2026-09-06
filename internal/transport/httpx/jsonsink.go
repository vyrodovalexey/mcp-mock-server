package httpx

import (
	"errors"
	"net/http"
	"strconv"
	"sync"

	"github.com/vyrodovalexey/mcp-mock-server/internal/engine"
)

// Content types and headers the transport writes. They are HTTP framing
// vocabulary, not MCP wire vocabulary, so they belong to the transport (this
// task is wire-insensitive: framing is ours, wire is internal/wire's).
const (
	contentTypeJSON = "application/json"
	contentTypeSSE  = "text/event-stream"

	headerContentType   = "Content-Type"
	headerContentLength = "Content-Length"
	headerCacheControl  = "Cache-Control"
	headerConnection    = "Connection"
	// headerAccelBuffering disables proxy buffering so SSE frames are delivered
	// as they are written (MOCK-208). It MUST be present on every SSE response.
	headerAccelBuffering = "X-Accel-Buffering"
)

// errStreamSinkClosed is returned by a streaming sink used after Close.
var errStreamSinkClosed = errors.New("httpx: stream sink closed")

// httpJSONSink is the buffered JSON [engine.Sink] for a non-streaming
// request/response (ShapeJSONOnce). The engine's stage 8 buffers the single
// frame in memory; this sink writes it to the http.ResponseWriter on Close with
// the correct status and Content-Length. It spawns NO goroutine — the whole
// request is handled on net/http's connection goroutine (architecture.md §7.1,
// acceptance criterion 3).
//
// It embeds the engine's own [engine.BufferedSink] for the Begin/Send/Flush
// state machine, so the transport reuses the engine's tested buffering rather
// than re-implementing it, and only overrides Close to perform the HTTP write.
type httpJSONSink struct {
	*engine.BufferedSink
	w       http.ResponseWriter
	written bool
}

// newHTTPJSONSink wraps w in a buffered JSON sink.
func newHTTPJSONSink(w http.ResponseWriter) *httpJSONSink {
	return &httpJSONSink{BufferedSink: engine.NewBufferedSink(), w: w}
}

// Close writes the buffered response to the client, then records the close
// reason on the embedded sink. It writes the body exactly once even if the
// engine's deferred Close fires after an explicit one, so the response is never
// double-written.
func (s *httpJSONSink) Close(reason engine.CloseReason) error {
	if s.written {
		return s.BufferedSink.Close(reason)
	}
	s.written = true
	// A client-gone closure means nobody will read the body; skip the write and
	// let the engine's journal record the cancellation (MOCK-212).
	if reason == engine.CloseClientGone {
		return s.BufferedSink.Close(reason)
	}
	body := s.BufferedSink.Bytes()
	hdr := s.BufferedSink.Header()
	h := s.w.Header()
	h.Set(headerContentType, contentTypeJSON)
	h.Set(headerContentLength, strconv.Itoa(len(body)))
	status := hdr.Status
	if status == 0 {
		status = http.StatusOK
	}
	s.w.WriteHeader(status)
	_, err := s.w.Write(body)
	_ = s.BufferedSink.Close(reason)
	return err
}

// Compile-time assertion that httpJSONSink satisfies engine.Sink, so a
// signature drift in the engine's interface fails this build rather than a
// caller (acceptance criterion 7).
var _ engine.Sink = (*httpJSONSink)(nil)

// SSESink is the Server-Sent-Events [engine.Sink] (ShapeSSEStream). Phase 1's
// engine never selects the SSE shape — full SSE content semantics are Phase 2
// (MOCK-208) — but the framing, the mandatory X-Accel-Buffering header, and the
// keep-alive plumbing exist now so Phase 2 fills content into a correct envelope
// rather than re-plumbing the transport. It is the exported Phase-2 seam this
// task delivers.
//
// The sink owns NO goroutine of its own. Frames arrive on a buffered channel;
// the [Stream] loop drains the channel and writes to the socket, and the shared
// [timerWheel] enqueues keep-alives onto the same channel (never writing the
// socket itself, ADR-012 §4). This is the ≈ two-goroutine, one-channel,
// zero-timer per-stream shape MOCK-903 requires.
//
// Concurrency: [SSESink.Send] is called from the wheel goroutine AND from a
// handler, while [SSESink.Close] is called from the [Stream] loop. A mutex
// guards the closed flag so a keep-alive enqueue never races Close, and Close
// signals completion by closing a separate done channel — it never closes the
// frame channel, so a concurrent Send can never send on a closed channel.
type SSESink struct {
	w       http.ResponseWriter
	rc      *http.ResponseController
	frames  chan engine.Frame
	done    chan struct{}
	begun   bool
	header  engine.ResponseHeader
	onClose func()

	mu     sync.Mutex
	closed bool
}

// sseChannelCap is the per-stream frame channel capacity. It is small and fixed
// (ADR-012 §3's "frame channel, cap 16 × pointer ≈ 256 B") so a stream's memory
// footprint stays measurable and small (MOCK-903); a full channel drops
// keep-alives rather than growing unbounded.
const sseChannelCap = 16

// NewSSESink builds an SSE sink over w. onClose (may be nil) is invoked once at
// Close so the caller can cancel the stream's keep-alive registration on the
// shared wheel.
func NewSSESink(w http.ResponseWriter, onClose func()) *SSESink {
	return &SSESink{
		w:       w,
		rc:      http.NewResponseController(w),
		frames:  make(chan engine.Frame, sseChannelCap),
		done:    make(chan struct{}),
		onClose: onClose,
	}
}

// Begin writes the SSE response head with the MOCK-208 headers, most importantly
// X-Accel-Buffering: no. It fixes the shape to ShapeSSEStream; any other shape
// is a programming error the engine would never make, rejected here so it fails
// loudly.
func (s *SSESink) Begin(shape engine.Shape, hdr engine.ResponseHeader) error {
	if s.begun {
		return errStreamSinkClosed
	}
	if shape != engine.ShapeSSEStream {
		return errors.New("httpx: SSESink supports only ShapeSSEStream")
	}
	s.begun = true
	s.header = hdr
	h := s.w.Header()
	h.Set(headerContentType, contentTypeSSE)
	h.Set(headerCacheControl, "no-cache")
	h.Set(headerConnection, "keep-alive")
	// MOCK-208: proxies (nginx) must not buffer the stream, or frames stall.
	h.Set(headerAccelBuffering, "no")
	status := hdr.Status
	if status == 0 {
		status = http.StatusOK
	}
	s.w.WriteHeader(status)
	return s.rc.Flush()
}

// Send enqueues one frame for the [Stream] loop to write. It never blocks the
// caller: a full channel drops the frame and reports it, matching ADR-012 §4's
// backpressure rule that one slow consumer must not stall the producer. It is
// safe to call from the wheel goroutine and a handler concurrently: the closed
// check and the enqueue happen under the mutex, so a Send never races Close.
func (s *SSESink) Send(f engine.Frame) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.begun || s.closed {
		return errStreamSinkClosed
	}
	select {
	case s.frames <- f:
		return nil
	default:
		return errSlowConsumer
	}
}

// errSlowConsumer signals a dropped frame because the stream's channel was full
// (ADR-012 §4). It is a sentinel so a caller can classify and count it.
var errSlowConsumer = errors.New("httpx: sse consumer too slow; frame dropped")

// Flush pushes buffered bytes toward the client via the ResponseController.
func (s *SSESink) Flush() error {
	return s.rc.Flush()
}

// Close ends the stream. It is idempotent; the second call is a no-op. It marks
// the sink closed under the mutex (so a concurrent [SSESink.Send] observes it),
// signals the [Stream] loop by closing done, and fires onClose so the shared
// wheel's keep-alive registration is canceled. It NEVER closes the frame
// channel, so a keep-alive enqueue in flight on another goroutine cannot panic.
func (s *SSESink) Close(_ engine.CloseReason) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	close(s.done)
	if s.onClose != nil {
		s.onClose()
	}
	return nil
}

// Frames exposes the sink's frame channel for the [Stream] loop to drain.
func (s *SSESink) Frames() <-chan engine.Frame { return s.frames }

// Done is closed when the sink is closed, signaling the [Stream] loop to stop
// draining. It is the completion signal that replaces closing the frame channel.
func (s *SSESink) Done() <-chan struct{} { return s.done }

// Compile-time assertion that SSESink satisfies engine.Sink.
var _ engine.Sink = (*SSESink)(nil)

// writeFrame serializes one frame as an SSE event. Result/error/notification
// frames carry their JSON bytes as the event data; a keep-alive is an SSE
// comment line (": keep-alive"), which is exactly what a per-stream ticker would
// have written but is instead driven by the shared wheel (ADR-012 §4).
func writeFrame(w http.ResponseWriter, f engine.Frame) error {
	if f.Kind == engine.FrameKeepAlive {
		_, err := w.Write([]byte(": keep-alive\n\n"))
		return err
	}
	// SSE data lines: "data: <json>\n\n". The JSON has no newlines (it is a
	// single JSON-RPC frame), so one data line suffices.
	if _, err := w.Write([]byte("data: ")); err != nil {
		return err
	}
	if _, err := w.Write(f.Bytes); err != nil {
		return err
	}
	_, err := w.Write([]byte("\n\n"))
	return err
}
