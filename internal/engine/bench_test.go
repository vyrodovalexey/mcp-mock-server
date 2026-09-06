package engine_test

import (
	"context"
	"testing"

	"github.com/vyrodovalexey/mcp-mock-server/internal/engine"
	"github.com/vyrodovalexey/mcp-mock-server/internal/wire"
)

// BenchmarkPipelineJournalOff measures the hot path with journaling OFF — the
// MOCK-901 ≥20 000 rps path. It reuses one instance and one pipeline and issues
// the same trivial echo request repeatedly, so it measures pipeline overhead
// (decode, derive-lazily, dispatch, encode) and not journal capture. The
// zero-draw echo handler exercises the no-random-decision path that pays nothing
// for RNG construction (ADR-002 option 4).
func BenchmarkPipelineJournalOff(b *testing.B) {
	inst := newInstance(1, "inst", false) // journal ring disabled
	p := newPipeline(echoHandler())
	raw := rawRequest("1", wire.MethodToolsCall)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sink := engine.NewBufferedSink()
		if err := p.Handle(ctx, buildExchange(ctx, inst, engine.KindHTTP, raw), sink); err != nil {
			b.Fatalf("handle: %v", err)
		}
	}
}

// BenchmarkPipelineJournalOn measures the same hot path with journaling ON, so
// the delta against BenchmarkPipelineJournalOff is the per-request cost of
// capture and commit (ADR-005 accepts one allocation per record on this path).
func BenchmarkPipelineJournalOn(b *testing.B) {
	inst := newInstance(1, "inst", true) // journal ring enabled
	p := newPipeline(echoHandler())
	raw := rawRequest("1", wire.MethodToolsCall)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sink := engine.NewBufferedSink()
		if err := p.Handle(ctx, buildExchange(ctx, inst, engine.KindHTTP, raw), sink); err != nil {
			b.Fatalf("handle: %v", err)
		}
	}
}

// BenchmarkPipelineParallel measures the pipeline under GOMAXPROCS-way
// concurrency with journaling ON, the shape closest to the 20 000 rps target: no
// per-request goroutine, no shared mutex on the request path, so throughput
// should scale with cores (architecture.md §7.1). Each goroutine sends a
// distinct id so requests do not collapse to one seeded stream.
func BenchmarkPipelineParallel(b *testing.B) {
	inst := newInstance(1, "inst", true)
	p := newPipeline(echoHandler())
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		n := 0
		for pb.Next() {
			n++
			raw := rawRequest(itoa(n), wire.MethodToolsCall)
			sink := engine.NewBufferedSink()
			if err := p.Handle(ctx, buildExchange(ctx, inst, engine.KindHTTP, raw), sink); err != nil {
				b.Fatalf("handle: %v", err)
			}
		}
	})
}
