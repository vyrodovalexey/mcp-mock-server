package modern

import (
	"encoding/json"
	"testing"

	"github.com/vyrodovalexey/mcp-mock-server/internal/engine"
)

// selfcheck_guard_bench_test.go is a white-box benchmark of the self-check
// disabled-path GUARD in isolation (MOCK-901): it measures exactly what a
// production instance with switches.selfCheck off pays per request for the
// self-check to exist. It is in package modern (not modern_test) so it can call
// the unexported selfChecker.check directly, isolating the guard cost from the
// handler cost the black-box benchmarks include.

// guardSnapshot is the minimal engine.Snapshot that also reports self-check OFF,
// so check takes its earliest return.
type guardSnapshot struct{}

func (guardSnapshot) Gen() uint64                         { return 1 }
func (guardSnapshot) Era() string                         { return "modern" }
func (guardSnapshot) JournalEnabled() bool                { return false }
func (guardSnapshot) MetaValidator() engine.MetaValidator { return engine.AcceptAllMeta }
func (guardSnapshot) MethodEnabled(string) bool           { return true }
func (guardSnapshot) SelfCheck() bool                     { return false }

// BenchmarkSelfCheckGuardDisabled measures the cost of the disabled self-check
// guard: a type assertion and one bool read, returning before any validation.
// The expectation (and the MOCK-901 requirement) is 0 allocs and a handful of
// nanoseconds, matching the journal-off discipline.
func BenchmarkSelfCheckGuardDisabled(b *testing.B) {
	sc := &selfChecker{log: nil}
	var snap engine.Snapshot = guardSnapshot{}
	body := json.RawMessage(`{"resultType":"discovery"}`)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if f := sc.check(snap, "server/discover", body); f != nil {
			b.Fatalf("disabled guard returned a fault: %v", f)
		}
	}
}
