package journal_test

import (
	"context"
	"testing"

	"github.com/vyrodovalexey/mcp-mock-server/internal/journal"
	"github.com/vyrodovalexey/mcp-mock-server/journalapi"
)

// BenchmarkCaptureDisabled measures the MOCK-901 fast path through the full
// capture entry point: with journaling off, Commit does one atomic load and
// returns before building a record. It must report 0 allocs/op — the enabled
// check is paid, the capture cost is not.
func BenchmarkCaptureDisabled(b *testing.B) {
	c := newCapturerBench(journalapi.Config{Enabled: false})
	ring := journal.New(journalapi.Config{Enabled: false, MaxRecords: 1 << 16})
	ctx := context.Background()
	in := fullInput()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_, _ = c.Commit(ctx, ring, in)
	}
}

// BenchmarkCaptureEnabledFull measures the MOCK-902 capture hot path with a
// full-body capture: build the record, redact headers, hash the credential, and
// commit. Allocations here are expected (the record and its copied bytes are
// retained behind an atomic pointer) and are the ADR-005 GC pressure that must
// be measured, not assumed.
func BenchmarkCaptureEnabledFull(b *testing.B) {
	c := newCapturerBench(journalapi.Config{Enabled: true, Mode: journalapi.CaptureFull})
	ring := journal.New(journalapi.Config{Enabled: true, MaxRecords: 1 << 16})
	ctx := context.Background()
	in := fullInput()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_, _ = c.Commit(ctx, ring, in)
	}
}

// BenchmarkCaptureEnabledDigest measures capture in digest mode: bodies are
// hashed, not copied, bounding retained bytes for oversized results (MOCK-504).
func BenchmarkCaptureEnabledDigest(b *testing.B) {
	c := newCapturerBench(journalapi.Config{Enabled: true, Mode: journalapi.CaptureDigest})
	ring := journal.New(journalapi.Config{Enabled: true, MaxRecords: 1 << 16})
	ctx := context.Background()
	in := fullInput()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_, _ = c.Commit(ctx, ring, in)
	}
}

// newCapturerBench builds a capturer for a benchmark (no *testing.T available).
func newCapturerBench(cfg journalapi.Config) *journal.Capturer {
	return journal.NewCapturer(cfg, journal.NewCredHasher())
}
