package ordered_test

import (
	"encoding/json"
	"testing"

	"github.com/vyrodovalexey/mcp-mock-server/internal/ordered"
)

// equalSlices reports whether two comparable slices are element-wise equal.
func equalSlices[T comparable](a, b []T) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestSetAddHasLen(t *testing.T) {
	t.Parallel()
	s := ordered.NewSet[string](0)
	if s.Len() != 0 {
		t.Fatalf("new set Len = %d, want 0", s.Len())
	}
	if !s.Add("a") {
		t.Fatal("Add(a) = false, want true (newly added)")
	}
	if s.Add("a") {
		t.Fatal("Add(a) again = true, want false (already present)")
	}
	s.Add("b")
	if s.Len() != 2 {
		t.Fatalf("Len = %d, want 2", s.Len())
	}
	if !s.Has("a") || s.Has("missing") {
		t.Fatal("Has semantics wrong")
	}
}

func TestSetZeroValueUsable(t *testing.T) {
	t.Parallel()
	var s ordered.Set[int]
	if !s.Add(7) {
		t.Fatal("zero-value Set Add(7) = false")
	}
	if !s.Has(7) {
		t.Fatal("zero-value Set Has(7) = false")
	}
}

func TestSetDeletePreservesOrder(t *testing.T) {
	t.Parallel()
	s := ordered.NewSet[string](0)
	for _, e := range []string{"a", "b", "c", "d"} {
		s.Add(e)
	}
	if !s.Delete("b") {
		t.Fatal("Delete(b) = false")
	}
	if s.Delete("b") {
		t.Fatal("second Delete(b) = true")
	}
	if want := []string{"a", "c", "d"}; !equalSlices(s.Elements(), want) {
		t.Fatalf("Elements after delete = %v, want %v", s.Elements(), want)
	}
}

func TestSetSortedAndInsertionOrder(t *testing.T) {
	t.Parallel()
	s := ordered.NewSet[string](0)
	for _, e := range []string{"delta", "alpha", "charlie"} {
		s.Add(e)
	}
	if want := []string{"delta", "alpha", "charlie"}; !equalSlices(s.Elements(), want) {
		t.Fatalf("Elements = %v, want insertion order %v", s.Elements(), want)
	}
	if want := []string{"alpha", "charlie", "delta"}; !equalSlices(s.Sorted(), want) {
		t.Fatalf("Sorted = %v, want %v", s.Sorted(), want)
	}
}

func TestSetRangeEarlyStop(t *testing.T) {
	t.Parallel()
	s := ordered.NewSet[int](0)
	for _, e := range []int{5, 4, 3, 2, 1} {
		s.Add(e)
	}
	var seen []int
	s.Range(func(e int) bool {
		seen = append(seen, e)
		return e != 3
	})
	if want := []int{5, 4, 3}; !equalSlices(seen, want) {
		t.Fatalf("Range early-stop = %v, want %v", seen, want)
	}
}

// TestSetIterationDeterminism mirrors the Map determinism assertion: with many
// elements a leak of Go map randomness into Set would produce a different
// Elements() order between independent builds.
func TestSetIterationDeterminism(t *testing.T) {
	t.Parallel()
	const n = 128
	const repeats = 200

	order := make([]int, n)
	for i := range order {
		order[i] = (i*53 + 7) % (n * 2) // distinct, non-monotonic values
	}
	build := func() *ordered.Set[int] {
		s := ordered.NewSet[int](n)
		for _, e := range order {
			s.Add(e)
		}
		return s
	}
	want := build().Elements()
	for r := 0; r < repeats; r++ {
		if got := build().Elements(); !equalSlices(got, want) {
			t.Fatalf("repeat %d: Elements diverged", r)
		}
	}
}

func TestSetMarshalJSON(t *testing.T) {
	t.Parallel()
	s := ordered.NewSet[string](0)
	for _, e := range []string{"z", "a", "m"} {
		s.Add(e)
	}
	got, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if want := `["z","a","m"]`; string(got) != want {
		t.Fatalf("Marshal = %s, want %s", got, want)
	}
	empty, err := json.Marshal(ordered.NewSet[string](0))
	if err != nil {
		t.Fatalf("Marshal empty: %v", err)
	}
	if string(empty) != "[]" {
		t.Fatalf("empty Set Marshal = %s, want []", empty)
	}
}

func TestSetNewNegativeCapacity(t *testing.T) {
	t.Parallel()
	s := ordered.NewSet[int](-3)
	s.Add(1)
	if !s.Has(1) {
		t.Fatal("Has(1) = false after Add on negative-cap Set")
	}
}

// Compile-time confirmation that Map/Set/Slice satisfy json.Marshaler.
var (
	_ json.Marshaler = (*ordered.Map[string, int])(nil)
	_ json.Marshaler = (*ordered.Set[string])(nil)
	_ json.Marshaler = (*ordered.Slice[int])(nil)
)

// exampleHeader is used by the slice tests to model a wire header pair.
func exampleHeader(name, value string) [2]string { return [2]string{name, value} }
