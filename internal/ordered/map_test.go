package ordered_test

import (
	"encoding/json"
	"fmt"
	"strconv"
	"testing"

	"github.com/vyrodovalexey/mcp-mock-server/internal/ordered"
)

func TestMapSetGetHasLen(t *testing.T) {
	t.Parallel()
	m := ordered.NewMap[string, int](0)
	if m.Len() != 0 {
		t.Fatalf("new map Len = %d, want 0", m.Len())
	}
	m.Set("a", 1)
	m.Set("b", 2)
	m.Set("a", 3) // update, must not move position or grow

	if got := m.Len(); got != 2 {
		t.Fatalf("Len after update = %d, want 2", got)
	}
	if v, ok := m.Get("a"); !ok || v != 3 {
		t.Fatalf("Get(a) = %d,%v; want 3,true", v, ok)
	}
	if !m.Has("b") {
		t.Fatal("Has(b) = false, want true")
	}
	if _, ok := m.Get("missing"); ok {
		t.Fatal("Get(missing) ok = true, want false")
	}
	if want := []string{"a", "b"}; !equalSlices(m.Keys(), want) {
		t.Fatalf("Keys after update = %v, want %v", m.Keys(), want)
	}
}

func TestMapZeroValueUsable(t *testing.T) {
	t.Parallel()
	var m ordered.Map[string, int]
	m.Set("x", 1)
	if v, ok := m.Get("x"); !ok || v != 1 {
		t.Fatalf("zero-value Map Get(x) = %d,%v; want 1,true", v, ok)
	}
}

func TestMapDeletePreservesOrderAndReinsertAppends(t *testing.T) {
	t.Parallel()
	m := ordered.NewMap[string, int](0)
	for i, k := range []string{"a", "b", "c", "d"} {
		m.Set(k, i)
	}
	if !m.Delete("b") {
		t.Fatal("Delete(b) = false, want true")
	}
	if m.Delete("b") {
		t.Fatal("second Delete(b) = true, want false")
	}
	if want := []string{"a", "c", "d"}; !equalSlices(m.Keys(), want) {
		t.Fatalf("Keys after delete = %v, want %v", m.Keys(), want)
	}
	// Values must still resolve correctly after the index was repaired.
	for k, want := range map[string]int{"a": 0, "c": 2, "d": 3} {
		if v, ok := m.Get(k); !ok || v != want {
			t.Fatalf("Get(%s) = %d,%v; want %d,true", k, v, ok, want)
		}
	}
	// Re-inserting a previously deleted key appends at the end.
	m.Set("b", 9)
	if want := []string{"a", "c", "d", "b"}; !equalSlices(m.Keys(), want) {
		t.Fatalf("Keys after reinsert = %v, want %v", m.Keys(), want)
	}
}

func TestMapSortedKeys(t *testing.T) {
	t.Parallel()
	m := ordered.NewMap[string, int](0)
	for _, k := range []string{"banana", "apple", "cherry"} {
		m.Set(k, 0)
	}
	if want := []string{"banana", "apple", "cherry"}; !equalSlices(m.Keys(), want) {
		t.Fatalf("Keys = %v, want insertion order %v", m.Keys(), want)
	}
	if want := []string{"apple", "banana", "cherry"}; !equalSlices(m.SortedKeys(), want) {
		t.Fatalf("SortedKeys = %v, want %v", m.SortedKeys(), want)
	}
	// SortedKeys must not mutate insertion order.
	if want := []string{"banana", "apple", "cherry"}; !equalSlices(m.Keys(), want) {
		t.Fatalf("Keys mutated by SortedKeys: %v", m.Keys())
	}
}

func TestMapRangeInsertionOrderAndEarlyStop(t *testing.T) {
	t.Parallel()
	m := ordered.NewMap[string, int](0)
	order := []string{"z", "y", "x", "w"}
	for i, k := range order {
		m.Set(k, i)
	}
	var seen []string
	m.Range(func(k string, _ int) bool {
		seen = append(seen, k)
		return true
	})
	if !equalSlices(seen, order) {
		t.Fatalf("Range order = %v, want %v", seen, order)
	}

	seen = nil
	m.Range(func(k string, _ int) bool {
		seen = append(seen, k)
		return k != "y" // stop after visiting y
	})
	if want := []string{"z", "y"}; !equalSlices(seen, want) {
		t.Fatalf("Range early-stop = %v, want %v", seen, want)
	}
}

// TestMapIterationDeterminism is the load-bearing determinism assertion for
// this task (acceptance criterion 1). A plain Go map ranges in randomized
// order; a leak of that randomness into ordered.Map would show up as a Keys()
// sequence that differs between two independently built maps or between
// repeated iterations. With 128 keys the chance of two random permutations
// coinciding is 1/128!, so any leak fails this within a single run — unlike a
// two-key map, which matches by luck half the time.
//
// This test is written to be meaningful under TASK-026's -count=20 across
// varying GOMAXPROCS: iteration order here is a pure function of insertion
// order and depends on neither.
func TestMapIterationDeterminism(t *testing.T) {
	t.Parallel()
	const n = 128
	const repeats = 200

	// A non-sorted, non-trivial insertion order so a leak cannot accidentally
	// match a sorted or reversed traversal.
	insertionOrder := make([]string, n)
	for i := range insertionOrder {
		insertionOrder[i] = fmt.Sprintf("key-%03d", (i*37+11)%n)
	}

	build := func() *ordered.Map[string, int] {
		m := ordered.NewMap[string, int](n)
		for i, k := range insertionOrder {
			m.Set(k, i)
		}
		return m
	}

	// The canonical expected order is the de-duplicated first-seen sequence.
	want := build().Keys()
	if len(want) != n {
		t.Fatalf("expected %d distinct keys, got %d", n, len(want))
	}

	for r := 0; r < repeats; r++ {
		// Freshly built map: order must be identical every time.
		if got := build().Keys(); !equalSlices(got, want) {
			t.Fatalf("repeat %d: Keys diverged from first build", r)
		}
		// Range on the same map twice must agree, and agree with Keys().
		m := build()
		var a, b []string
		m.Range(func(k string, _ int) bool { a = append(a, k); return true })
		m.Range(func(k string, _ int) bool { b = append(b, k); return true })
		if !equalSlices(a, b) || !equalSlices(a, want) {
			t.Fatalf("repeat %d: Range order not stable", r)
		}
	}
}

func TestMapMarshalJSONPreservesInsertionOrder(t *testing.T) {
	t.Parallel()
	m := ordered.NewMap[string, int](0)
	// Deliberately non-alphabetical: encoding/json would sort a plain map here.
	for i, k := range []string{"zebra", "alpha", "mike"} {
		m.Set(k, i)
	}
	got, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	want := `{"zebra":0,"alpha":1,"mike":2}`
	if string(got) != want {
		t.Fatalf("Marshal = %s, want %s", got, want)
	}

	empty, err := json.Marshal(ordered.NewMap[string, int](0))
	if err != nil {
		t.Fatalf("Marshal empty: %v", err)
	}
	if string(empty) != "{}" {
		t.Fatalf("empty Marshal = %s, want {}", empty)
	}
}

func TestMapJSONRoundTripPreservesOrder(t *testing.T) {
	t.Parallel()
	src := ordered.NewMap[string, int](0)
	for i, k := range []string{"gamma", "beta", "alpha", "delta"} {
		src.Set(k, i*10)
	}
	data, err := json.Marshal(src)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var dst ordered.Map[string, int]
	if err := json.Unmarshal(data, &dst); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !equalSlices(dst.Keys(), src.Keys()) {
		t.Fatalf("round-trip Keys = %v, want %v", dst.Keys(), src.Keys())
	}
	re, err := json.Marshal(&dst)
	if err != nil {
		t.Fatalf("re-Marshal: %v", err)
	}
	if string(re) != string(data) {
		t.Fatalf("round-trip bytes = %s, want %s", re, data)
	}
}

func TestMapUnmarshalIntKeys(t *testing.T) {
	t.Parallel()
	// Integer-keyed maps are encoded with quoted numeric member names by
	// encoding/json and by Map.MarshalJSON; the round trip must decode them.
	src := ordered.NewMap[int, string](0)
	for _, k := range []int{30, 10, 20} {
		src.Set(k, strconv.Itoa(k))
	}
	data, err := json.Marshal(src)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if string(data) != `{"30":"30","10":"10","20":"20"}` {
		t.Fatalf("Marshal int keys = %s", data)
	}
	var dst ordered.Map[int, string]
	if err := json.Unmarshal(data, &dst); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !equalSlices(dst.Keys(), []int{30, 10, 20}) {
		t.Fatalf("int-key round-trip order = %v", dst.Keys())
	}
}

func TestMapUnmarshalErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{name: "null yields empty", input: `null`, wantErr: false},
		{name: "empty object", input: `{}`, wantErr: false},
		{name: "array is not an object", input: `[1,2]`, wantErr: true},
		{name: "truncated object", input: `{"a":1`, wantErr: true},
		{name: "value type mismatch", input: `{"a":"not-an-int"}`, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var m ordered.Map[string, int]
			err := json.Unmarshal([]byte(tc.input), &m)
			if (err != nil) != tc.wantErr {
				t.Fatalf("Unmarshal(%s) err = %v, wantErr = %v", tc.input, err, tc.wantErr)
			}
		})
	}
}

func TestMapNewNegativeCapacity(t *testing.T) {
	t.Parallel()
	m := ordered.NewMap[string, int](-5) // must not panic
	m.Set("a", 1)
	if v, ok := m.Get("a"); !ok || v != 1 {
		t.Fatalf("Get(a) = %d,%v", v, ok)
	}
}
