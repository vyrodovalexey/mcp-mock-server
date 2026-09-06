package catalog_test

import (
	"testing"

	"github.com/vyrodovalexey/mcp-mock-server/internal/catalogue"
	"github.com/vyrodovalexey/mcp-mock-server/scenario"
)

// BenchmarkConstruct measures catalog.New's cost at 0 and 5000 generated items. ADR-004's
// central claim is that construction is O(overlay), independent of the generated
// count: the 5000-item construction must not allocate 5000 items. -benchmem
// reports bytes/op and allocs/op so the flat cost is visible.
func BenchmarkConstruct(b *testing.B) {
	for _, count := range []int{0, 5000} {
		count := count
		b.Run(name(count), func(b *testing.B) {
			cfg := &scenario.Catalog{
				Tools:             &scenario.Generated{Count: &count},
				Prompts:           &scenario.Generated{Count: &count},
				Resources:         &scenario.Generated{Count: &count},
				ResourceTemplates: &scenario.Generated{Count: &count},
			}
			key := goldenKey("bench")
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				sink = catalog.New(key, cfg)
			}
		})
	}
}

// BenchmarkAt measures per-item generation cost at 5000 items (one Derive plus
// an Item). This is the O(1)-per-item, zero-at-rest access ADR-004 promises.
func BenchmarkAt(b *testing.B) {
	c := generatedCatalogue(b, "bench", 5000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		itemSink = c.At(catalog.KindTool, i%5000)
	}
}

// BenchmarkIndexOf measures name-inversion lookup cost at 5000 items. It must be
// independent of the count (no scan): a pure string parse.
func BenchmarkIndexOf(b *testing.B) {
	c := generatedCatalogue(b, "bench", 5000)
	names := make([]string, 5000)
	for i := range names {
		names[i] = c.At(catalog.KindTool, i).Name
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		intSink, _ = c.IndexOf(catalog.KindTool, names[i%5000])
	}
}

// TestConstructDoesNotMaterialise asserts, as a hard test rather than a
// benchmark reading, that constructing a 5000-item catalogue allocates on the
// same order as a 0-item one — proving the generated range is not materialised.
// It compares per-op allocation between the two constructions with a generous
// margin so it is not flaky, but tight enough to fail if catalog.New ever built a
// 5000-element slice (which would be ~thousands of extra allocs).
func TestConstructDoesNotMaterialise(t *testing.T) {
	t.Parallel()
	key := goldenKey("materialise")
	build := func(count int) float64 {
		cfg := &scenario.Catalog{
			Tools:             &scenario.Generated{Count: &count},
			Prompts:           &scenario.Generated{Count: &count},
			Resources:         &scenario.Generated{Count: &count},
			ResourceTemplates: &scenario.Generated{Count: &count},
		}
		res := testing.Benchmark(func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				sink = catalog.New(key, cfg)
			}
		})
		return float64(res.AllocsPerOp())
	}
	empty := build(0)
	big := build(5000)
	// Materialising 5000×4 items would add tens of thousands of allocs. A
	// virtual catalogue's construction cost must be within a tiny constant of
	// the empty case. Allow generous slack for map/backing growth.
	if big > empty+64 {
		t.Fatalf("5000-item construction allocated %.0f allocs vs %.0f empty (>%.0f): catalogue is materialising",
			big, empty, empty+64)
	}
}

// sinks defeat dead-code elimination so the benchmarks measure real work.
var (
	sink     *catalog.Catalog
	itemSink catalog.Item
	intSink  int
)

func name(count int) string {
	if count == 0 {
		return "count=0"
	}
	return "count=5000"
}
