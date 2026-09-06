package engine_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/vyrodovalexey/mcp-mock-server/internal/engine"
	"github.com/vyrodovalexey/mcp-mock-server/internal/jsonrpc"
	"github.com/vyrodovalexey/mcp-mock-server/internal/wire"
)

// TestBufferedSinkStateMachine asserts the Begin → Send → Close contract: Begin
// once, one Send, Close, and that out-of-order use is rejected.
func TestBufferedSinkStateMachine(t *testing.T) {
	s := engine.NewBufferedSink()
	if err := s.Send(engine.Frame{Kind: engine.FrameResult, Bytes: []byte("x")}); err == nil {
		t.Fatal("Send before Begin must error")
	}
	if err := s.Begin(engine.ShapeJSONOnce, engine.ResponseHeader{Status: 200}); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := s.Begin(engine.ShapeJSONOnce, engine.ResponseHeader{}); err == nil {
		t.Fatal("second Begin must error")
	}
	if err := s.Send(engine.Frame{Kind: engine.FrameResult, Bytes: []byte(`{"ok":1}`)}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if err := s.Send(engine.Frame{Kind: engine.FrameResult, Bytes: []byte("y")}); err == nil {
		t.Fatal("second Send on JSON-once must error")
	}
	if err := s.Close(engine.CloseComplete); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := s.Close(engine.CloseComplete); err != nil {
		t.Fatalf("second Close must be a tolerant no-op, got %v", err)
	}
	if !bytes.Equal(s.Bytes(), []byte(`{"ok":1}`)) {
		t.Fatalf("buffered bytes = %q", s.Bytes())
	}
}

// TestBufferedSinkRejectsStreamingShape asserts the Phase 1 buffered sink refuses
// the SSE shape, so a mis-selected shape fails loudly rather than emitting a
// malformed stream. This is the seam the Phase 2 SSE sink fills.
func TestBufferedSinkRejectsStreamingShape(t *testing.T) {
	s := engine.NewBufferedSink()
	if err := s.Begin(engine.ShapeSSEStream, engine.ResponseHeader{}); err == nil {
		t.Fatal("BufferedSink must reject ShapeSSEStream in Phase 1")
	}
}

// streamSink is a test Sink that implements the STREAMING shape, proving the
// Sink interface is usable for a streaming response without change (acceptance
// criterion 5, ADR-006). It also tags each frame with its JSON-RPC id, which is
// exactly what a stdio mux sink does to interleave many requests on one channel
// (MOCK-256).
type streamSink struct {
	shape  engine.Shape
	frames []engine.Frame
	closed engine.CloseReason
	flushN int
}

func (s *streamSink) Begin(shape engine.Shape, _ engine.ResponseHeader) error {
	s.shape = shape
	return nil
}
func (s *streamSink) Send(f engine.Frame) error {
	s.frames = append(s.frames, f)
	return nil
}
func (s *streamSink) Close(r engine.CloseReason) error {
	s.closed = r
	return nil
}
func (s *streamSink) Flush() error {
	s.flushN++
	return nil
}

var _ engine.Sink = (*streamSink)(nil)

// TestSinkUsableForRequestResponseAndStreaming exercises the SAME Sink interface
// in a request/response shape (BufferedSink) and a streaming shape (streamSink),
// showing one abstraction serves both (ADR-006, acceptance criterion 5).
func TestSinkUsableForRequestResponseAndStreaming(t *testing.T) {
	// Request/response shape.
	buffered := engine.NewBufferedSink()
	emitOne(t, buffered, engine.ShapeJSONOnce, jsonrpc.StringID("a"), `{"r":1}`)
	if got := string(buffered.Bytes()); got != `{"r":1}` {
		t.Fatalf("buffered shape bytes = %q", got)
	}

	// Streaming shape: three interleaved frames tagged with different ids, as a
	// stdio mux would carry them on one channel (MOCK-256).
	stream := &streamSink{}
	if err := stream.Begin(engine.ShapeSSEStream, engine.ResponseHeader{}); err != nil {
		t.Fatalf("stream Begin: %v", err)
	}
	ids := []jsonrpc.ID{jsonrpc.StringID("req-1"), jsonrpc.StringID("req-2"), jsonrpc.StringID("req-1")}
	for i, id := range ids {
		b, _ := json.Marshal(map[string]int{"frame": i})
		if err := stream.Send(engine.Frame{Kind: engine.FrameResult, ID: id, Bytes: b}); err != nil {
			t.Fatalf("stream Send: %v", err)
		}
		_ = stream.Flush()
	}
	_ = stream.Close(engine.CloseComplete)

	if stream.shape != engine.ShapeSSEStream {
		t.Fatalf("stream shape not recorded")
	}
	if len(stream.frames) != 3 {
		t.Fatalf("want 3 interleaved frames, got %d", len(stream.frames))
	}
	// The mux can demultiplex by id: frames 0 and 2 belong to req-1, frame 1 to
	// req-2 — the interleaving MOCK-256 requires is expressible.
	if !stream.frames[0].ID.Equal(stream.frames[2].ID) {
		t.Fatal("frames 0 and 2 should carry the same id (req-1)")
	}
	if stream.frames[0].ID.Equal(stream.frames[1].ID) {
		t.Fatal("frame 1 should carry a different id (req-2)")
	}
	if stream.flushN != 3 {
		t.Fatalf("streaming sink should flush per frame, got %d flushes", stream.flushN)
	}
}

// emitOne drives a single frame through a sink via the same Begin/Send/Flush/
// Close sequence the pipeline uses.
func emitOne(t *testing.T, s engine.Sink, shape engine.Shape, id jsonrpc.ID, body string) {
	t.Helper()
	if err := s.Begin(shape, engine.ResponseHeader{Status: 200}); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := s.Send(engine.Frame{Kind: engine.FrameResult, ID: id, Bytes: json.RawMessage(body)}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if err := s.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if err := s.Close(engine.CloseComplete); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestResponseEchoesIDByteForByte asserts the response id matches the request id
// exactly, distinguishing numeric 1 from string "1" (MOCK-203, MOCK-247).
func TestResponseEchoesIDByteForByte(t *testing.T) {
	cases := []struct {
		name string
		id   string
	}{
		{"numeric", "1"},
		{"string", `"1"`},
		{"large", "123456789012345678901234567890"},
	}
	inst := newInstance(1, "inst", false)
	p := newPipeline(echoHandler())
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := []byte(`{"jsonrpc":"2.0","id":` + tc.id + `,"method":"` + wire.MethodToolsCall + `","params":{}}`)
			sink := runOnce(t, p, buildExchange(context.Background(), inst, engine.KindHTTP, raw))
			resp := decodeResponse(t, sink.Bytes())
			if string(resp.ID) != tc.id {
				t.Fatalf("response id = %s, want %s", resp.ID, tc.id)
			}
		})
	}
}

// TestFaultErrorsAreStructured asserts a Fault maps to a wire error and matches
// with errors.Is/As at the call site (architecture.md §8), and that its wire
// fields come from internal/wire.
func TestFaultErrorsAreStructured(t *testing.T) {
	base := errors.New("root cause")
	f := engine.InvalidParamsFault("bad", nil).WithReason("detail").WithCause(base)
	if !errors.Is(f, base) {
		t.Fatal("Fault must wrap its cause for errors.Is")
	}
	var target *engine.Fault
	if !errors.As(error(f), &target) {
		t.Fatal("Fault must be matchable with errors.As")
	}
	if target.Code != wire.ErrCodeInvalidParams {
		t.Fatalf("fault code = %d, want %d", target.Code, wire.ErrCodeInvalidParams)
	}
}
