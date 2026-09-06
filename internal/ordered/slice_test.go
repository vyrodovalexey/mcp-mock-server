package ordered_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/vyrodovalexey/mcp-mock-server/internal/ordered"
)

func TestSliceAppendLenAt(t *testing.T) {
	t.Parallel()
	s := ordered.NewSlice[int](0)
	if s.Len() != 0 {
		t.Fatalf("new slice Len = %d, want 0", s.Len())
	}
	s.Append(1, 2)
	s.Append(3)
	if s.Len() != 3 {
		t.Fatalf("Len = %d, want 3", s.Len())
	}
	if s.At(0) != 1 || s.At(2) != 3 {
		t.Fatalf("At mismatch: [0]=%d [2]=%d", s.At(0), s.At(2))
	}
}

func TestSliceOf(t *testing.T) {
	t.Parallel()
	s := ordered.SliceOf("a", "b", "a")
	if want := []string{"a", "b", "a"}; !equalSlices(s.Items(), want) {
		t.Fatalf("SliceOf items = %v, want %v", s.Items(), want)
	}
}

// TestSlicePreservesDuplicateHeadersAndCasing is acceptance criterion 2:
// ordered.Slice[[2]string] preserves both original casing and duplicate
// entries. The input contains the same header name twice with different casing
// (as a raw socket can send), which http.Header would canonicalize and merge.
func TestSlicePreservesDuplicateHeadersAndCasing(t *testing.T) {
	t.Parallel()
	headers := ordered.NewSlice[[2]string](0)
	input := [][2]string{
		exampleHeader("X-Mcp-Header", "first"),
		exampleHeader("content-type", "application/json"),
		exampleHeader("x-mcp-header", "second"), // same name, different casing + duplicate
		exampleHeader("X-MCP-HEADER", "third"),  // yet another casing
	}
	for _, h := range input {
		headers.Append(h)
	}

	if headers.Len() != len(input) {
		t.Fatalf("Len = %d, want %d (no de-duplication allowed)", headers.Len(), len(input))
	}

	got := headers.Items()
	for i, want := range input {
		if got[i] != want {
			t.Fatalf("header[%d] = %v, want %v (casing/order/dupes must survive)", i, got[i], want)
		}
	}

	// Explicitly assert the three distinct casings of the same logical name all
	// survived verbatim and in wire order.
	var names []string
	headers.Range(func(h [2]string) bool {
		if strings.EqualFold(h[0], "x-mcp-header") {
			names = append(names, h[0])
		}
		return true
	})
	if want := []string{"X-Mcp-Header", "x-mcp-header", "X-MCP-HEADER"}; !equalSlices(names, want) {
		t.Fatalf("preserved casings = %v, want %v", names, want)
	}
}

func TestSliceRangeEarlyStop(t *testing.T) {
	t.Parallel()
	s := ordered.SliceOf(10, 20, 30, 40)
	var seen []int
	s.Range(func(v int) bool {
		seen = append(seen, v)
		return v != 20
	})
	if want := []int{10, 20}; !equalSlices(seen, want) {
		t.Fatalf("Range early-stop = %v, want %v", seen, want)
	}
}

func TestSliceSortIsStable(t *testing.T) {
	t.Parallel()
	// Sort header pairs by name only. Pairs with equal names must keep their
	// original relative order (stability), so duplicate-header ordering stays
	// deterministic rather than being reshuffled by an unstable sort.
	s := ordered.SliceOf(
		exampleHeader("b", "1"),
		exampleHeader("a", "first"),
		exampleHeader("a", "second"),
		exampleHeader("a", "third"),
		exampleHeader("b", "2"),
	)
	s.Sort(func(x, y [2]string) bool { return x[0] < y[0] })

	want := [][2]string{
		exampleHeader("a", "first"),
		exampleHeader("a", "second"),
		exampleHeader("a", "third"),
		exampleHeader("b", "1"),
		exampleHeader("b", "2"),
	}
	got := s.Items()
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Sort[%d] = %v, want %v (stable order violated)", i, got[i], want[i])
		}
	}
}

// TestSliceSortDeterminism proves the explicit Sort is deterministic across
// many independent runs — the "requested ordering" path must be stable, unlike
// map randomness (which this package forbids entirely).
func TestSliceSortDeterminism(t *testing.T) {
	t.Parallel()
	const repeats = 200
	build := func() *ordered.Slice[[2]string] {
		return ordered.SliceOf(
			exampleHeader("c", "1"),
			exampleHeader("a", "x"),
			exampleHeader("a", "y"),
			exampleHeader("b", "z"),
			exampleHeader("a", "w"),
		)
	}
	first := build()
	first.Sort(func(x, y [2]string) bool { return x[0] < y[0] })
	want := first.Items()

	for r := 0; r < repeats; r++ {
		s := build()
		s.Sort(func(x, y [2]string) bool { return x[0] < y[0] })
		got := s.Items()
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("repeat %d: sort result diverged at %d", r, i)
			}
		}
	}
}

func TestSliceJSONRoundTrip(t *testing.T) {
	t.Parallel()
	src := ordered.SliceOf(
		exampleHeader("X-A", "1"),
		exampleHeader("x-a", "2"), // duplicate name, different casing
		exampleHeader("X-B", "3"),
	)
	data, err := json.Marshal(src)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if want := `[["X-A","1"],["x-a","2"],["X-B","3"]]`; string(data) != want {
		t.Fatalf("Marshal = %s, want %s", data, want)
	}

	var dst ordered.Slice[[2]string]
	if err := json.Unmarshal(data, &dst); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if dst.Len() != src.Len() {
		t.Fatalf("round-trip Len = %d, want %d", dst.Len(), src.Len())
	}
	got := dst.Items()
	for i, want := range src.Items() {
		if got[i] != want {
			t.Fatalf("round-trip[%d] = %v, want %v", i, got[i], want)
		}
	}
	re, err := json.Marshal(&dst)
	if err != nil {
		t.Fatalf("re-Marshal: %v", err)
	}
	if string(re) != string(data) {
		t.Fatalf("round-trip bytes = %s, want %s", re, data)
	}
}

func TestSliceEmptyMarshalsAsArray(t *testing.T) {
	t.Parallel()
	got, err := json.Marshal(ordered.NewSlice[int](0))
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(got) != "[]" {
		t.Fatalf("empty Slice Marshal = %s, want []", got)
	}
}

func TestSliceNewNegativeCapacity(t *testing.T) {
	t.Parallel()
	s := ordered.NewSlice[int](-4)
	s.Append(9)
	if s.At(0) != 9 {
		t.Fatalf("At(0) = %d, want 9", s.At(0))
	}
}
