package stdio

import (
	"context"
	"errors"

	"github.com/vyrodovalexey/mcp-mock-server/internal/engine"
)

// muxSink is the stdio implementation of engine.Sink (ADR-006's stdio.muxSink).
// It is created per request, used by that one request's pipeline on one worker
// goroutine, and forwards each response frame to the transport's SINGLE writer
// channel — never writing to Config.Out itself. That funnel is the MOCK-256
// guarantee: many concurrent muxSinks feed one writer, which serializes frames
// so interleaved responses on the single output channel are never corrupted.
//
// Each frame carries its originating JSON-RPC id (preserved byte-for-byte), so a
// peer demultiplexes responses by id. Phase 1 emits exactly one frame per
// request (ShapeJSONOnce); the same forward-to-one-writer structure carries the
// many-frame SSE/subscription case in later phases without change.
type muxSink struct {
	// ctx is the request context, so a channel send unblocks on shutdown/EOF
	// instead of deadlocking when the writer has stopped.
	ctx context.Context
	// out is the transport's shared writer channel — the one serialization
	// point. The sink sends complete frame payloads; the writer appends the
	// newline in one atomic Write (writeFrame).
	out chan<- []byte

	shape  engine.Shape
	begun  bool
	closed bool
	sent   bool
}

// newMuxSink returns a muxSink forwarding to out, bounded by ctx. ctx is the
// per-request context so a Send during shutdown returns promptly rather than
// blocking on a writer that is draining to exit.
func newMuxSink(ctx context.Context, out chan<- []byte) *muxSink {
	return &muxSink{ctx: ctx, out: out}
}

// errSinkClosed reports a Send/Close race with shutdown: the request context was
// canceled before the frame could be handed to the writer. It is wrapped so the
// caller can classify it; the transport treats it as a benign shutdown signal,
// not a wire error.
var errSinkClosed = errors.New("stdio: sink forward canceled by shutdown")

// errSinkState reports a Sink method called out of the Begin → Send → Close
// order. The engine drives the state machine correctly; this guards against a
// future miswiring rather than a runtime input.
var errSinkState = errors.New("stdio: sink used out of order")

// Begin fixes the response shape. Phase 1 accepts only ShapeJSONOnce; the SSE
// shape is a later phase's implementation of this same interface, so a
// mis-selected shape fails loudly here rather than emitting a malformed stream.
// The header is unused by stdio (there are no HTTP status/headers on the wire);
// it is recorded on the journal record by the engine, not by the sink.
func (s *muxSink) Begin(shape engine.Shape, _ engine.ResponseHeader) error {
	if s.begun {
		return errSinkState
	}
	if shape != engine.ShapeJSONOnce {
		return errors.New("stdio: muxSink supports only ShapeJSONOnce in Phase 1")
	}
	s.begun = true
	s.shape = shape
	return nil
}

// Send forwards one frame's bytes to the single writer, tagged (by the engine's
// encoder) with the request id. For ShapeJSONOnce exactly one result-or-error
// frame is expected. The send respects the request context so shutdown does not
// deadlock on the unbuffered writer channel; on cancellation it returns
// errSinkClosed, which the transport treats as a benign shutdown outcome.
func (s *muxSink) Send(f engine.Frame) error {
	if !s.begun || s.closed || s.sent {
		return errSinkState
	}
	// A keep-alive carries no bytes and is not emitted in Phase 1; guard against
	// forwarding an empty frame that would write a bare newline.
	if len(f.Bytes) == 0 {
		s.sent = true
		return nil
	}
	// Copy the frame bytes: the writer goroutine receives them asynchronously,
	// so the sink must not alias a buffer the engine may reuse. In Phase 1 the
	// engine hands freshly-encoded bytes, but copying keeps the contract robust
	// against a future pooled encoder.
	message := make([]byte, len(f.Bytes))
	copy(message, f.Bytes)
	select {
	case <-s.ctx.Done():
		return errSinkClosed
	case s.out <- message:
		s.sent = true
		return nil
	}
}

// Close marks the sink closed. It is idempotent-tolerant so the pipeline's
// deferred Close after an explicit one is a no-op. stdio has no per-response
// close frame on the wire (the newline already terminated the frame), so Close
// only updates state.
func (s *muxSink) Close(_ engine.CloseReason) error {
	s.closed = true
	return nil
}

// Flush is a no-op: a frame is fully written by the writer goroutine as soon as
// it is received, so there is nothing buffered in the sink to push. It exists to
// satisfy engine.Sink.
func (s *muxSink) Flush() error { return nil }

// Compile-time assertion that muxSink satisfies engine.Sink, so a signature
// drift fails the build rather than a caller.
var _ engine.Sink = (*muxSink)(nil)
