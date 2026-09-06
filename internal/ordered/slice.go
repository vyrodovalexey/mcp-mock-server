package ordered

import (
	"encoding/json"
	"slices"
)

// Slice is an ordered sequence that preserves every element in the order it
// was appended, including duplicates.
//
// Unlike [Map] and [Set] it performs no de-duplication and holds no index: it
// is a thin, order- and duplicate-preserving wrapper over a backing slice.
// The journal uses Slice[[2]string] to capture HTTP headers in wire order with
// duplicates and original casing intact (ADR-005), because http.Header's
// canonicalization and map ordering would otherwise destroy exactly the
// evidence the journal exists to provide.
//
// The zero value is an empty, ready-to-use Slice. It is not safe for
// concurrent mutation (see the package documentation). Any reordering is
// explicit via [Slice.Sort]; there is no implicit or randomized ordering.
type Slice[T any] struct {
	items []T
}

// NewSlice returns an empty [Slice]. The capacity hint pre-allocates the
// backing storage and is never observable in iteration order.
func NewSlice[T any](capacity int) *Slice[T] {
	if capacity < 0 {
		capacity = 0
	}
	return &Slice[T]{items: make([]T, 0, capacity)}
}

// SliceOf returns a [Slice] containing the given items in order. The items are
// copied, so the caller's backing array is not retained.
func SliceOf[T any](items ...T) *Slice[T] {
	return &Slice[T]{items: slices.Clone(items)}
}

// Append adds items to the end of the sequence, preserving order and any
// duplicates.
func (s *Slice[T]) Append(items ...T) {
	s.items = append(s.items, items...)
}

// Len returns the number of elements.
func (s *Slice[T]) Len() int {
	return len(s.items)
}

// At returns the element at index i. It panics if i is out of range, matching
// the behavior of a native slice index.
func (s *Slice[T]) At(i int) T {
	return s.items[i]
}

// Items returns a copy of the elements in order. The returned slice is owned
// by the caller and may be modified freely without affecting this Slice.
func (s *Slice[T]) Items() []T {
	return slices.Clone(s.items)
}

// Sort reorders the elements in place using the given comparison, which must
// return true when a should sort before b. The sort is stable: elements that
// compare equal keep their existing relative order, so the result is fully
// deterministic even in the presence of duplicates.
func (s *Slice[T]) Sort(less func(a, b T) bool) {
	slices.SortStableFunc(s.items, func(a, b T) int {
		switch {
		case less(a, b):
			return -1
		case less(b, a):
			return 1
		default:
			return 0
		}
	})
}

// Range calls yield for each element in order, stopping early if yield returns
// false. It matches the iter.Seq shape.
func (s *Slice[T]) Range(yield func(T) bool) {
	for _, item := range s.items {
		if !yield(item) {
			return
		}
	}
}

// MarshalJSON encodes the slice as a JSON array in element order. A nil or
// empty Slice encodes as "[]" rather than "null".
func (s *Slice[T]) MarshalJSON() ([]byte, error) {
	if s.items == nil {
		return []byte("[]"), nil
	}
	return json.Marshal(s.items)
}

// UnmarshalJSON decodes a JSON array into the slice, replacing its contents
// and preserving element order and duplicates.
func (s *Slice[T]) UnmarshalJSON(data []byte) error {
	var items []T
	if err := json.Unmarshal(data, &items); err != nil {
		return err
	}
	s.items = items
	return nil
}
