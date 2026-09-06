package instance_test

import (
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vyrodovalexey/mcp-mock-server/internal/determinism"
	"github.com/vyrodovalexey/mcp-mock-server/internal/instance"
	"github.com/vyrodovalexey/mcp-mock-server/scenario"
)

// BenchmarkSnapshotRead measures the read-path cost: a single atomic pointer
// load and one field read, with no lock (acceptance criterion 1, MOCK-901). It
// runs in parallel to expose any hidden contention on the read path.
func BenchmarkSnapshotRead(b *testing.B) {
	inst := instance.New(instance.Config{
		Name: "read", Root: determinism.Root(1),
		Spec: scenario.InstanceSpec{Journal: &scenario.Journal{Enabled: boolp(true)}},
	})
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		var gen uint64
		for pb.Next() {
			gen += inst.Snapshot().Gen()
		}
		_ = gen
	})
}

// BenchmarkSnapshotReadUnderWriter measures the read path while a writer mutates
// occasionally, asserting the read stays lock-free (acceptance criterion 1). The
// writer runs in the background; readers must not slow to its cadence.
func BenchmarkSnapshotReadUnderWriter(b *testing.B) {
	inst := instance.New(instance.Config{
		Name: "rw", Root: determinism.Root(1),
		Spec: scenario.InstanceSpec{Journal: &scenario.Journal{Enabled: boolp(true)}},
	})
	var stop atomic.Bool
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for !stop.Load() {
			inst.Mutate(func(*instance.Snapshot) {})
			time.Sleep(time.Millisecond)
		}
	}()
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		var gen uint64
		for pb.Next() {
			gen += inst.Snapshot().Gen()
		}
		_ = gen
	})
	b.StopTimer()
	stop.Store(true)
	wg.Wait()
}

// BenchmarkSnapshotSwap measures the write-path cost: clone, edit, bump gen,
// atomic store, under the writer mutex (ADR-014). This is the control-API
// mutation cost, off the request hot path.
func BenchmarkSnapshotSwap(b *testing.B) {
	inst := instance.New(instance.Config{
		Name: "swap", Root: determinism.Root(1),
		Spec: scenario.InstanceSpec{Journal: &scenario.Journal{Enabled: boolp(true)}},
	})
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		inst.Mutate(func(*instance.Snapshot) {})
	}
}

// BenchmarkInstanceMemory200 measures per-instance heap memory at 200 instances
// (MOCK-904). It reports bytes/instance as a custom metric by diffing HeapAlloc
// around construction of 200 instances, each with a journal ring and a 100-tool
// virtual catalogue. Being a benchmark, it is measured, not asserted, so the
// number is reported rather than gated here.
func BenchmarkInstanceMemory200(b *testing.B) {
	const n = 200
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		var m0, m1 runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&m0)

		insts := make([]*instance.Instance, 0, n)
		for k := 0; k < n; k++ {
			insts = append(insts, instance.New(instance.Config{
				Name: "mem" + strconv.Itoa(k),
				ID:   k,
				Root: determinism.Root(uint64(k)),
				Spec: scenario.InstanceSpec{
					Journal: &scenario.Journal{Enabled: boolp(true)},
					Catalog: &scenario.Catalog{Tools: &scenario.Generated{Count: intp(100)}},
				},
			}))
		}

		runtime.ReadMemStats(&m1)
		perInstance := float64(m1.HeapAlloc-m0.HeapAlloc) / float64(n)
		b.ReportMetric(perInstance, "bytes/instance")
		runtime.KeepAlive(insts)
	}
}

// BenchmarkInstanceMemory200NoJournal isolates the non-journal per-instance
// memory (MOCK-904): the snapshot, the virtual catalogue and the metadata,
// without the journal ring's preallocated slots. It is the floor of per-instance
// cost; BenchmarkInstanceMemory200 adds the default 100k-record journal ring on
// top. Measured, not asserted.
func BenchmarkInstanceMemory200NoJournal(b *testing.B) {
	const n = 200
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		var m0, m1 runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&m0)

		insts := make([]*instance.Instance, 0, n)
		for k := 0; k < n; k++ {
			insts = append(insts, instance.New(instance.Config{
				Name: "mem" + strconv.Itoa(k),
				ID:   k,
				Root: determinism.Root(uint64(k)),
				Spec: scenario.InstanceSpec{
					Journal: &scenario.Journal{Enabled: boolp(false)},
					Catalog: &scenario.Catalog{Tools: &scenario.Generated{Count: intp(100)}},
				},
			}))
		}

		runtime.ReadMemStats(&m1)
		perInstance := float64(m1.HeapAlloc-m0.HeapAlloc) / float64(n)
		b.ReportMetric(perInstance, "bytes/instance")
		runtime.KeepAlive(insts)
	}
}

// BenchmarkRegistryLookup measures the registry read path: one atomic load and
// one map read, lock-free (acceptance criterion 5).
func BenchmarkRegistryLookup(b *testing.B) {
	reg := instance.NewRegistry()
	const n = 200
	paths := make([]string, n)
	for k := 0; k < n; k++ {
		name := "inst" + strconv.Itoa(k)
		paths[k] = "/mock/" + name + "/mcp"
		inst := instance.New(instance.Config{
			Name: name, MountPath: paths[k], ID: k,
			Root: determinism.Root(uint64(k)), Spec: scenario.InstanceSpec{},
		})
		reg.Add(inst)
	}
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			_, _ = reg.Lookup(paths[i%n])
			i++
		}
	})
}
