package journal_test

import (
	"context"
	"testing"

	"github.com/vyrodovalexey/mcp-mock-server/internal/journal"
	"github.com/vyrodovalexey/mcp-mock-server/journalapi"
)

// benchRecord is a representative ~2 KB request record: the ADR-005 sizing used
// for the MOCK-902 garbage estimate. It is built once, outside the benchmarked
// loop, so the benchmark measures the storage write path, not record assembly.
func benchRecord() journalapi.Record {
	body := make([]byte, 2048)
	for i := range body {
		body[i] = byte(i)
	}
	return journalapi.Record{
		Instance:  "bench-instance",
		Transport: journalapi.TransportHTTP,
		MonoNs:    123456789,
		HTTP: &journalapi.HTTPPart{
			Method: "POST",
			Path:   "/mcp",
			Headers: [][2]string{
				{"Content-Type", "application/json"},
				{"x-mcp-header", "a"},
				{"x-mcp-header", "b"},
			},
		},
		JSONRPC: journalapi.JSONRPCPart{
			Method:     "tools/call",
			Name:       "search",
			Body:       body,
			BodyLength: len(body),
		},
	}
}

// BenchmarkWriteDisabled measures the MOCK-901 fast path: journaling off. It
// must report 0 allocs/op and a handful of nanoseconds — a single atomic load.
func BenchmarkWriteDisabled(b *testing.B) {
	r := journal.New(journalapi.Config{Enabled: false, MaxRecords: 1 << 16})
	ctx := context.Background()
	rec := benchRecord()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_, _ = r.Write(ctx, rec)
	}
}

// BenchmarkWriteEnabled measures the MOCK-902 hot path: journaling on with a
// full ~2 KB body retained. Allocations here are expected (the record is
// published behind an atomic pointer and its body is retained) and are the
// GC-pressure ADR-005 says must be measured, not assumed.
func BenchmarkWriteEnabled(b *testing.B) {
	r := journal.New(journalapi.Config{Enabled: true, MaxRecords: 1 << 16})
	ctx := context.Background()
	rec := benchRecord()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_, _ = r.Write(ctx, rec)
	}
}

// BenchmarkWriteEnabledParallel measures the contended hot path: many goroutines
// writing at once, which is the realistic MOCK-902 shape. Throughput here is the
// basis for the rps estimate; ns/op inverts to writes/second per core-set.
func BenchmarkWriteEnabledParallel(b *testing.B) {
	r := journal.New(journalapi.Config{Enabled: true, MaxRecords: 1 << 18})
	ctx := context.Background()
	rec := benchRecord()
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = r.Write(ctx, rec)
		}
	})
}

// BenchmarkWriteDisabledParallel measures the MOCK-901 disabled fast path under
// contention: the single atomic load must stay cheap and allocation-free with
// many goroutines.
func BenchmarkWriteDisabledParallel(b *testing.B) {
	r := journal.New(journalapi.Config{Enabled: false, MaxRecords: 1 << 16})
	ctx := context.Background()
	rec := benchRecord()
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = r.Write(ctx, rec)
		}
	})
}

// BenchmarkSnapshot measures the read/query path building a Seq-ordered snapshot
// of a full ring — the cost the control API pays per journal read.
func BenchmarkSnapshot(b *testing.B) {
	r := journal.New(journalapi.Config{Enabled: true, MaxRecords: 4096})
	ctx := context.Background()
	rec := benchRecord()
	for range 4096 {
		_, _ = r.Write(ctx, rec)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_ = r.Snapshot()
	}
}
