package engine

import (
	"encoding/json"

	"github.com/vyrodovalexey/mcp-mock-server/internal/jsonrpc"
)

// Shape is the response shape chosen ONCE, before the first [Sink.Send]
// (MOCK-208). The engine decides it in stage 8 and the transport obeys;
// transports never choose the shape (ADR-006).
type Shape uint8

const (
	// ShapeJSONOnce is a single buffered JSON-RPC response. It is the only shape
	// Phase 1 emits; the SSE shape is a Phase 2 [Sink] implementation of this
	// same interface (ADR-006).
	ShapeJSONOnce Shape = iota
	// ShapeSSEStream is a Server-Sent-Events stream of frames. Declared now so
	// the interface accommodates it without change; no Phase 1 code selects it.
	ShapeSSEStream
)

// FrameKind classifies a [Frame] in the small sum type ADR-006 defines. Keeping
// frames a tagged union (rather than an interface per frame) keeps the hot path
// allocation-free: a [Frame] is a value, not a heap-boxed interface.
type FrameKind uint8

const (
	// FrameResult carries a JSON-RPC result. Its Bytes are the raw result JSON,
	// emitted with the request id by the sink.
	FrameResult FrameKind = iota
	// FrameError carries a JSON-RPC error object. Its Bytes are the encoded
	// error, produced solely by emit.go.
	FrameError
	// FrameNotification carries a server-initiated notification (later phases).
	FrameNotification
	// FrameKeepAlive is an SSE keep-alive with no JSON-RPC payload (Phase 2).
	FrameKeepAlive
	// FrameRawBytes is the escape hatch that makes §5 protocol faults
	// expressible — invalid JSON, truncated JSON, duplicate ids — without
	// polluting the typed path. Phase 1 never constructs one on the request
	// path; ADR-006 restricts its construction to internal/fault in later
	// phases.
	FrameRawBytes
)

// Frame is one unit a [Sink] emits: a result, an error, a notification, a
// keep-alive, or raw bytes. It is a value type carrying the originating
// JSON-RPC id so a stdio [Sink] can tag interleaved frames from many concurrent
// requests onto its single channel (MOCK-256).
//
// Exactly one of the byte-bearing fields is meaningful per FrameKind: Bytes for
// result/error/notification/raw, empty for keep-alive. The id is echoed
// byte-for-byte (MOCK-203).
type Frame struct {
	// Kind selects which payload interpretation applies.
	Kind FrameKind
	// ID is the JSON-RPC id this frame answers, preserved byte-for-byte so it
	// echoes exactly and so a mux sink can correlate it (MOCK-256). AbsentID for
	// a notification or keep-alive.
	ID jsonrpc.ID
	// Bytes is the raw payload: result JSON, encoded error, notification JSON,
	// or arbitrary bytes for FrameRawBytes. Nil for a keep-alive.
	Bytes json.RawMessage
}

// CloseReason states why a response stream closed. It is where MOCK-212
// (closure = cancellation) and MOCK-254 (graceful vs abrupt) live: the reason is
// transport-neutral intent, and each transport implements it concretely
// (ADR-006).
type CloseReason uint8

const (
	// CloseComplete is a normal, graceful completion — every intended frame was
	// sent.
	CloseComplete CloseReason = iota
	// CloseClientGone is client-initiated cancellation: the HTTP client
	// disconnected, stdin reached EOF, or a cancel notification arrived. The
	// engine records it with elapsed time (MOCK-212).
	CloseClientGone
	// CloseFault is a deliberately-injected fault closure (Phase 9).
	CloseFault
	// CloseAbrupt is an abnormal closure that is neither complete nor a
	// recognized cancellation.
	CloseAbrupt
)

// String returns a stable label for the reason, used as the journal record's
// close reason. It never panics on an out-of-range value.
func (r CloseReason) String() string {
	switch r {
	case CloseComplete:
		return "complete"
	case CloseClientGone:
		return "clientGone"
	case CloseFault:
		return "fault"
	case CloseAbrupt:
		return "abrupt"
	default:
		return unknownLabel
	}
}

// ResponseHeader carries the shape-level response metadata a [Sink] needs before
// the first frame. Phase 1's buffered JSON sink uses only Status; the SSE sink
// (Phase 2) will read the header fields it needs (Content-Type, X-Accel-Buffering)
// without this type changing shape.
type ResponseHeader struct {
	// Status is the HTTP status code the response carries (200 for a JSON-RPC
	// error in a well-formed envelope). A stdio sink ignores it; it is recorded
	// on the journal record regardless.
	Status int
	// ResultType is the MCP resultType of the response, when known, for the
	// journal record.
	ResultType string
}

// Sink is the transport-neutral outbound interface every response is written
// through (ADR-006's ResponseSink). Phase 1 implements the buffered JSON sink
// ([BufferedSink]); the SSE sink is a Phase 2 implementation of THIS interface,
// which must accommodate it without change.
//
// The contract is a small state machine: exactly one [Sink.Begin] first (which
// fixes the [Shape]), then zero or more [Sink.Send], then exactly one
// [Sink.Close]. [Sink.Flush] may be called between sends by a streaming sink.
// A [Sink] is used by ONE request's pipeline on ONE goroutine; a stdio
// implementation funnels every sink's frames onto a single shared writer
// goroutine (MOCK-256), but the Send/Close calls for one request are serial.
type Sink interface {
	// Begin fixes the response shape and header before any frame is sent
	// (MOCK-208). It returns an error if called more than once or after a
	// frame; the engine calls it exactly once at stage 8.
	Begin(shape Shape, hdr ResponseHeader) error
	// Send emits one [Frame]. For ShapeJSONOnce exactly one result-or-error
	// frame is sent; for a streaming shape many frames are sent in order.
	Send(f Frame) error
	// Close ends the response with a reason (MOCK-212 / MOCK-254). It is
	// idempotent-safe to the engine: the pipeline calls it exactly once, in a
	// defer, so a mid-pipeline error still closes the sink.
	Close(reason CloseReason) error
	// Flush pushes buffered bytes toward the client. The buffered JSON sink
	// flushes on Close; a streaming sink flushes after each event.
	Flush() error
}
