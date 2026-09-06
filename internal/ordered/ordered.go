package ordered

import (
	"cmp"
	"encoding/json"
	"slices"
)

// Set is an insertion-ordered generic set with deterministic iteration.
//
// Elements are ordered by first insertion. Re-adding an existing element is a
// no-op and does not change its position; [Set.Delete] removes an element and
// closes the gap. Iteration order is therefore a pure function of the mutation
// history, independent of Go's randomized map iteration, of GOMAXPROCS and of
// the process (ADR-003).
//
// The zero value is an empty, ready-to-use Set. A Set must not be copied by
// value once used; use it through a pointer. It is not safe for concurrent
// mutation (see the package documentation).
type Set[K cmp.Ordered] struct {
	elems []K
	index map[K]int // element -> position; used only for lookup, never for iteration
}

// NewSet returns an empty [Set]. The capacity hint pre-allocates the backing
// storage and is never observable in iteration order.
func NewSet[K cmp.Ordered](capacity int) *Set[K] {
	if capacity < 0 {
		capacity = 0
	}
	return &Set[K]{
		elems: make([]K, 0, capacity),
		index: make(map[K]int, capacity),
	}
}

func (s *Set[K]) ensure() {
	if s.index == nil {
		s.index = make(map[K]int)
	}
}

// Add inserts element if it is not already present and reports whether it was
// newly added. A new element is appended after all existing elements.
func (s *Set[K]) Add(element K) bool {
	s.ensure()
	if _, ok := s.index[element]; ok {
		return false
	}
	s.index[element] = len(s.elems)
	s.elems = append(s.elems, element)
	return true
}

// Has reports whether element is present.
func (s *Set[K]) Has(element K) bool {
	_, ok := s.index[element]
	return ok
}

// Delete removes element if present and reports whether it was. Remaining
// elements keep their relative insertion order.
func (s *Set[K]) Delete(element K) bool {
	i, ok := s.index[element]
	if !ok {
		return false
	}
	s.elems = slices.Delete(s.elems, i, i+1)
	delete(s.index, element)
	for j := i; j < len(s.elems); j++ {
		s.index[s.elems[j]] = j
	}
	return true
}

// Len returns the number of elements.
func (s *Set[K]) Len() int {
	return len(s.elems)
}

// Elements returns a copy of the elements in insertion order. The returned
// slice is owned by the caller.
func (s *Set[K]) Elements() []K {
	return slices.Clone(s.elems)
}

// Sorted returns a copy of the elements sorted ascending by [cmp.Ordered].
func (s *Set[K]) Sorted() []K {
	out := slices.Clone(s.elems)
	slices.Sort(out)
	return out
}

// Range calls yield for each element in insertion order, stopping early if
// yield returns false. It matches the iter.Seq shape.
func (s *Set[K]) Range(yield func(K) bool) {
	for _, e := range s.elems {
		if !yield(e) {
			return
		}
	}
}

// MarshalJSON encodes the set as a JSON array of its elements in insertion
// order. The order is deterministic because it is fixed by the mutation
// history.
func (s *Set[K]) MarshalJSON() ([]byte, error) {
	if s.elems == nil {
		return []byte("[]"), nil
	}
	return json.Marshal(s.elems)
}
