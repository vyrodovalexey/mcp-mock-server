package httpx

import (
	"context"
	"time"

	"github.com/vyrodovalexey/mcp-mock-server/internal/engine"
)

// defaultKeepAliveInterval is the SSE keep-alive comment cadence when a caller
// does not specify one (MOCK-255 default). It is deliberately coarse: the shared
// wheel rounds it to whole ticks and a keep-alive's only job is to keep an idle
// connection and its proxies from timing out.
const defaultKeepAliveInterval = 15 * time.Second

// Stream drives one SSE response to completion on the CURRENT goroutine (the one
// net/http already gave the handler), adding no goroutine of its own — the
// per-stream budget stays at net/http's read goroutine plus this handler
// goroutine, with zero timers (architecture.md §7.1, ADR-012 §3). It registers a
// keep-alive on the SHARED [timerWheel] (not a per-stream time.Ticker), drains
// the sink's frame channel writing each frame to the socket, and returns when
// the sink closes or the context is canceled (client disconnect, MOCK-212).
//
// Phase 1 does not select the SSE shape, so this is the exported Phase-2 seam:
// it and [SSESink] are the complete, keep-alive-correct, X-Accel-Buffering-
// correct envelope Phase 2 fills with content. keepAlive ≤ 0 uses the default;
// keepAlive disabled is expressed by passing a very large interval or by the
// caller not opening a stream at all (MOCK-255 disableable).
func (s *Server) Stream(ctx context.Context, sink *SSESink, keepAlive time.Duration) error {
	if keepAlive <= 0 {
		keepAlive = defaultKeepAliveInterval
	}
	// One shared-wheel registration per stream — O(1), no per-stream ticker
	// (ADR-012 §4). On fire the wheel enqueues a keep-alive onto the sink's
	// channel; a full channel drops it (slow consumer) rather than stalling the
	// wheel.
	cancelKA := s.wheel.register(keepAlive, func() {
		_ = sink.Send(engine.Frame{Kind: engine.FrameKeepAlive})
	})
	defer cancelKA()

	for {
		select {
		case <-ctx.Done():
			// Client gone: stop the stream, let the caller journal cancellation.
			return ctx.Err()
		case <-sink.Done():
			// Sink closed: drain any frames already buffered, then complete. The
			// drain is bounded by the channel capacity, so it cannot spin.
			return drain(sink)
		case f := <-sink.Frames():
			if err := writeFrame(sink.w, f); err != nil {
				return err
			}
			if err := sink.Flush(); err != nil {
				return err
			}
		}
	}
}

// drain writes any frames still buffered in the sink after it closed, so a final
// result enqueued just before Close is not lost. It never blocks: it stops as
// soon as the buffered channel is empty.
func drain(sink *SSESink) error {
	for {
		select {
		case f := <-sink.Frames():
			if err := writeFrame(sink.w, f); err != nil {
				return err
			}
			if err := sink.Flush(); err != nil {
				return err
			}
		default:
			return nil
		}
	}
}
