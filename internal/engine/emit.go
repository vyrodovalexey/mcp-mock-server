package engine

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/vyrodovalexey/mcp-mock-server/internal/jsonrpc"
	"github.com/vyrodovalexey/mcp-mock-server/internal/wire"
)

// emit.go is the SINGLE place in the module where a JSON-RPC error object is
// constructed and where a result or error becomes wire bytes (architecture.md
// §8, ADR-019). Every wire error — a genuine stage failure or, in Phase 9, a
// fault code — flows through [encodeError] here, so the two share one encoder
// and one code source (internal/wire). An AST check (TASK-026/033) asserts no
// other file constructs a jsonrpc error response.

// The Phase 1 faults the engine can raise, each built from an internal/wire
// code and its documented HTTP status (wire/errors.go). No error-code literal
// appears here; the codes come from internal/wire (ADR-019 containment).
var (
	// faultParse is a -32700 parse error (HTTP 400): the request bytes were not
	// valid JSON. It is raised at stage 1.
	faultParse = newFault(wire.ErrCodeParse, http.StatusBadRequest, "Parse error")
	// faultInvalidRequest is a -32600 invalid request (HTTP 400): the JSON was
	// not a well-formed JSON-RPC request object. Raised at stage 1.
	faultInvalidRequest = newFault(
		wire.ErrCodeInvalidRequest, http.StatusBadRequest, "Invalid Request")
	// faultMethodNotFound is a -32601 method-not-found (HTTP 200 per annex
	// [P-31]): the method is unknown or disabled by switches (MOCK-202.2).
	// Raised at stage 6.
	faultMethodNotFound = newFault(
		wire.ErrCodeMethodNotFound, http.StatusOK, "Method not found")
	// faultInternal is a -32603 internal error (HTTP 500): an unexpected engine
	// failure. It is the catch-all for a stage returning a non-Fault error.
	faultInternal = newFault(
		wire.ErrCodeInternal, http.StatusInternalServerError, "Internal error")
)

// InvalidParamsFault builds a -32602 invalid-params fault (HTTP 400) carrying an
// optional data payload. It is exported because it is the constructor TASK-017's
// stage-4 _meta validator (internal/modern) uses for a missing-_meta rejection:
// the validator lives in another package, hands the engine a fault, and never
// constructs a wire error itself (ADR-019 containment). The code and status come
// from internal/wire and the data payload (the -32602 data.missing shape) is
// built by the caller from internal/wire, so no wire literal appears at the call
// site either.
func InvalidParamsFault(message string, data json.RawMessage) *Fault {
	f := newFault(wire.ErrCodeInvalidParams, http.StatusBadRequest, message)
	f.Data = data
	return f
}

// encodeError turns a [*Fault] into the wire bytes of a JSON-RPC error response
// echoing id. It is the ONLY error-object constructor in the module: it builds a
// jsonrpc.Error from the fault's wire code, message and data, wraps it in an
// error response, and encodes it byte-stably (HTML escaping disabled, authored
// data preserved) through jsonrpc.Response.Encode. A nil fault is treated as an
// internal error rather than panicking, so a mis-wired stage cannot crash the
// process.
func encodeError(id jsonrpc.ID, f *Fault) ([]byte, error) {
	if f == nil {
		f = faultInternal
	}
	werr := &jsonrpc.Error{Code: f.Code, Message: f.Message, Data: f.Data}
	return jsonrpc.NewErrorResponse(id, werr).Encode()
}

// encodeResult turns a raw result body into the wire bytes of a JSON-RPC success
// response echoing id. The result bytes are emitted verbatim (authored key order
// preserved, MOCK-222.4); it is the caller's responsibility to have produced
// them in the intended order (handlers build results through internal/wire's
// MarshalResult, which does not re-sort keys).
func encodeResult(id jsonrpc.ID, result json.RawMessage) ([]byte, error) {
	return jsonrpc.NewResultResponse(id, result).Encode()
}

// BufferedSink is the Phase 1 buffered JSON [Sink]: it collects the single
// response frame in memory and exposes the bytes for the transport to write
// (ADR-006's httpx.jsonSink / the stdio single-frame case). It implements the
// full [Sink] state machine so the engine's stage 8 is transport-agnostic, and
// it satisfies the same interface the Phase 2 SSE sink will, so nothing in the
// engine changes when streaming arrives.
//
// A BufferedSink is used by one request on one goroutine. It is not safe for
// concurrent use; a transport that multiplexes (stdio) hands each request its
// own sink and serializes the resulting bytes onto its single writer
// (MOCK-256).
type BufferedSink struct {
	buf     bytes.Buffer
	shape   Shape
	header  ResponseHeader
	begun   bool
	closed  bool
	reason  CloseReason
	sent    bool
	frameID jsonrpc.ID
}

// NewBufferedSink returns an empty buffered JSON sink.
func NewBufferedSink() *BufferedSink { return &BufferedSink{} }

// errSinkState is returned when a [Sink] method is called out of the Begin →
// Send* → Close order. It wraps a sentinel so a transport can classify it.
var errSinkState = errors.New("engine: sink used out of order")

// Begin records the shape and header. Phase 1 accepts only [ShapeJSONOnce];
// [ShapeSSEStream] is rejected here until the Phase 2 SSE sink implements it, so
// a mis-selected shape fails loudly rather than emitting a malformed stream.
func (s *BufferedSink) Begin(shape Shape, hdr ResponseHeader) error {
	if s.begun {
		return errSinkState
	}
	if shape != ShapeJSONOnce {
		return errors.New("engine: BufferedSink supports only ShapeJSONOnce")
	}
	s.begun = true
	s.shape = shape
	s.header = hdr
	return nil
}

// Send buffers one frame's bytes. For the JSON-once shape exactly one
// result-or-error frame is expected; a second Send is a state error. The id the
// frame carries is retained so a caller can confirm the response echoes the
// request id.
func (s *BufferedSink) Send(f Frame) error {
	if !s.begun || s.closed || s.sent {
		return errSinkState
	}
	s.frameID = f.ID
	s.buf.Write(f.Bytes)
	s.sent = true
	return nil
}

// Close marks the sink closed with a reason. It is idempotent-tolerant: a second
// Close is a no-op returning nil, so the engine's deferred Close after an
// explicit one does not error.
func (s *BufferedSink) Close(reason CloseReason) error {
	if s.closed {
		return nil
	}
	s.closed = true
	s.reason = reason
	return nil
}

// Flush is a no-op for a buffered sink: bytes are already in memory and are read
// by [BufferedSink.Bytes]. It exists to satisfy [Sink].
func (s *BufferedSink) Flush() error { return nil }

// Bytes returns the buffered response bytes. It is called by the transport after
// [Pipeline.Handle] returns to write the response, and by tests to assert
// byte-identity. The returned slice aliases the sink's buffer; the caller must
// not mutate it.
func (s *BufferedSink) Bytes() []byte { return s.buf.Bytes() }

// Header returns the response header recorded at [BufferedSink.Begin], for the
// transport to translate into HTTP status/headers and for the journal record.
func (s *BufferedSink) Header() ResponseHeader { return s.header }

// CloseReason returns the reason the sink was closed with, for the journal
// record (MOCK-212 / MOCK-254).
func (s *BufferedSink) CloseReason() CloseReason { return s.reason }

// Compile-time assertion that BufferedSink satisfies Sink, so a signature drift
// fails the build rather than a caller (feeds TASK-019 acceptance criterion 7).
var _ Sink = (*BufferedSink)(nil)
