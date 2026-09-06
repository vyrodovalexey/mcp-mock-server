package httpx_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vyrodovalexey/mcp-mock-server/internal/engine"
	"github.com/vyrodovalexey/mcp-mock-server/internal/transport/httpx"
)

// TestSSEAccelBufferingHeader asserts MOCK-208: an SSE response carries
// X-Accel-Buffering: no, plus the Content-Type: text/event-stream framing
// header. The SSE sink writes these at Begin.
func TestSSEAccelBufferingHeader(t *testing.T) {
	rec := httptest.NewRecorder()
	sink := httpx.NewSSESink(rec, nil)
	if err := sink.Begin(engine.ShapeSSEStream, engine.ResponseHeader{Status: http.StatusOK}); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if got := rec.Header().Get("X-Accel-Buffering"); got != "no" {
		t.Fatalf("X-Accel-Buffering = %q, want \"no\" (MOCK-208)", got)
	}
	if got := rec.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", got)
	}
	if err := sink.Close(engine.CloseComplete); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestSSESinkRejectsWrongShape asserts the SSE sink refuses a non-SSE shape,
// failing loudly rather than emitting a malformed stream.
func TestSSESinkRejectsWrongShape(t *testing.T) {
	rec := httptest.NewRecorder()
	sink := httpx.NewSSESink(rec, nil)
	if err := sink.Begin(engine.ShapeJSONOnce, engine.ResponseHeader{}); err == nil {
		t.Fatal("SSESink accepted ShapeJSONOnce; must reject a non-SSE shape")
	}
}

// TestSSESinkOnCloseCancelsKeepAlive asserts the onClose hook fires exactly once
// so the shared wheel's keep-alive registration is canceled when the stream ends
// (no per-stream timer outlives its stream, ADR-012 §4).
func TestSSESinkOnCloseCancelsKeepAlive(t *testing.T) {
	rec := httptest.NewRecorder()
	fired := 0
	sink := httpx.NewSSESink(rec, func() { fired++ })
	_ = sink.Begin(engine.ShapeSSEStream, engine.ResponseHeader{})
	_ = sink.Close(engine.CloseComplete)
	_ = sink.Close(engine.CloseComplete) // idempotent
	if fired != 1 {
		t.Fatalf("onClose fired %d times, want exactly 1", fired)
	}
}

// TestStreamWritesFramesAndKeepAlive drives the exported Stream loop with a
// short keep-alive interval and asserts both a data frame and a keep-alive
// comment reach the socket, proving the shared wheel enqueues keep-alives
// without a per-stream ticker.
func TestStreamWritesFramesAndKeepAlive(t *testing.T) {
	srv, err := httpx.New(httpx.Config{Addr: "127.0.0.1:0", KeepAliveTick: time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	go func() { _ = srv.Serve() }()
	t.Cleanup(func() { _ = srv.Close() })

	rec := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
	sink := httpx.NewSSESink(rec, nil)

	done := make(chan error, 1)
	go func() {
		done <- srv.Stream(context.Background(), sink, 5*time.Millisecond)
	}()

	if err := sink.Begin(engine.ShapeSSEStream, engine.ResponseHeader{}); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if err := sink.Send(engine.Frame{Kind: engine.FrameResult, Bytes: []byte(`{"n":1}`)}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	// Give the shared wheel time to fire at least one keep-alive.
	time.Sleep(40 * time.Millisecond)
	_ = sink.Close(engine.CloseComplete)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Stream returned %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Stream did not return after Close")
	}

	out := rec.body()
	if !strings.Contains(out, "data: {\"n\":1}") {
		t.Fatalf("stream missing data frame; got:\n%s", out)
	}
	if !strings.Contains(out, ": keep-alive") {
		t.Fatalf("stream missing keep-alive comment (shared wheel did not fire); got:\n%s", out)
	}
}

// TestStreamStopsOnContextCancel asserts the Stream loop returns promptly when
// its context is canceled (client disconnect), releasing the handler goroutine.
func TestStreamStopsOnContextCancel(t *testing.T) {
	srv, err := httpx.New(httpx.Config{Addr: "127.0.0.1:0", KeepAliveTick: time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	go func() { _ = srv.Serve() }()
	t.Cleanup(func() { _ = srv.Close() })

	rec := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
	sink := httpx.NewSSESink(rec, nil)
	_ = sink.Begin(engine.ShapeSSEStream, engine.ResponseHeader{})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Stream(ctx, sink, time.Second) }()
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Stream returned nil on cancel; want ctx.Err()")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Stream did not return on context cancel")
	}
}

// flushRecorder adds Flush() to httptest.ResponseRecorder so the SSE
// ResponseController's Flush succeeds. It records the written body.
type flushRecorder struct {
	*httptest.ResponseRecorder
}

func (f *flushRecorder) Flush() {}

func (f *flushRecorder) body() string { return f.ResponseRecorder.Body.String() }
