package httpx_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
	"time"

	"github.com/vyrodovalexey/mcp-mock-server/internal/engine"
	"github.com/vyrodovalexey/mcp-mock-server/internal/transport/httpx"
	"github.com/vyrodovalexey/mcp-mock-server/internal/wire"
)

// BenchmarkRequestThroughput measures the request/response path (MOCK-901). It
// runs POSTs against a live server with keep-alive so the figure reflects the
// steady-state hot path, not connection setup. Report with -benchmem.
func BenchmarkRequestThroughput(b *testing.B) {
	mount := newMountB(1, "inst", httpx.DefaultPath, echoHandler(`{"ok":true}`))
	srv, err := httpx.New(httpx.Config{Addr: "127.0.0.1:0", Mounts: []*httpx.Mount{mount}})
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	go func() { _ = srv.Serve() }()
	b.Cleanup(func() { _ = srv.Close() })
	base := "http://" + srv.Addr().String()
	body := rawRequest("1", wire.MethodToolsCall)
	client := &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 64}}
	b.Cleanup(client.CloseIdleConnections)

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, base+httpx.DefaultPath, bytes.NewReader(body))
			resp, err := client.Do(req)
			if err != nil {
				b.Fatalf("do: %v", err)
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
	})
}

// BenchmarkPerStreamMemory measures the heap cost of holding N concurrent open
// SSE streams, to compare against ADR-012's ~25 KiB/stream ESTIMATE. It opens
// streams against httptest.ResponseRecorders (so the number is the Go-heap cost
// of the sink + wheel registration + stream loop, isolated from kernel socket
// buffers), holds them all open, and reports HeapAlloc delta / stream.
//
// This is a MEASURED figure for the user-space, Go-heap portion of ADR-012 §3's
// envelope. It excludes net/http's per-connection read goroutine, bufio buffers
// and kernel socket buffers, which a real listener adds and which TASK-029's
// end-to-end 20 000-stream spike measures against a live socket. The comparison
// against 25 KiB is therefore a lower bound on the real per-stream cost.
func BenchmarkPerStreamMemory(b *testing.B) {
	const streams = 2000
	srv, err := httpx.New(httpx.Config{Addr: "127.0.0.1:0", KeepAliveTick: 10 * time.Millisecond})
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	go func() { _ = srv.Serve() }()
	b.Cleanup(func() { _ = srv.Close() })

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		var m0, m1 runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&m0)

		sinks := make([]*httpx.SSESink, streams)
		ctx, cancel := context.WithCancel(context.Background())
		for s := 0; s < streams; s++ {
			rec := &benchFlusher{ResponseRecorder: httptest.NewRecorder()}
			sink := httpx.NewSSESink(rec, nil)
			_ = sink.Begin(engine.ShapeSSEStream, engine.ResponseHeader{})
			sinks[s] = sink
			go func() { _ = srv.Stream(ctx, sink, time.Minute) }()
		}
		// Let every stream register its keep-alive and block.
		time.Sleep(100 * time.Millisecond)
		runtime.GC()
		runtime.ReadMemStats(&m1)

		perStream := (int64(m1.HeapAlloc) - int64(m0.HeapAlloc)) / int64(streams)
		b.ReportMetric(float64(perStream), "B/stream")

		cancel()
		for _, sink := range sinks {
			_ = sink.Close(engine.CloseComplete)
		}
		// Give the stream goroutines time to exit before the next iteration so
		// goleak (in TestMain) does not see them.
		time.Sleep(50 * time.Millisecond)
		runtime.KeepAlive(sinks)
	}
}

// benchFlusher adds a no-op Flush to the recorder so the SSE ResponseController
// Flush succeeds under benchmark.
type benchFlusher struct{ *httptest.ResponseRecorder }

func (f *benchFlusher) Flush() {}

// newMountB builds a Mount for benchmarks (no *testing.T dependency).
func newMountB(seed uint64, name, path string, h engine.Handler) *httpx.Mount {
	return &httpx.Mount{
		Path:     path,
		Instance: newInstance(seed, name),
		Pipeline: newPipelineFor(newRegistryForMethods(h)),
	}
}
