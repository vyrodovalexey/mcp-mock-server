package ordered

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
)

// Map is an insertion-ordered generic map with deterministic iteration.
//
// Keys are ordered by first insertion. Re-assigning an existing key updates
// its value in place and does not change its position; [Map.Delete] removes a
// key and closes the gap, so a subsequent re-insert appends at the end. This
// makes iteration order a pure function of the mutation history and therefore
// independent of Go's randomized map iteration, of GOMAXPROCS and of the
// process — the property ADR-003 requires.
//
// The zero value is an empty, ready-to-use Map. A Map must not be copied by
// value once used; use it through a pointer. It is not safe for concurrent
// mutation (see the package documentation for the full concurrency contract).
//
// K is constrained to [cmp.Ordered] so [Map.SortedKeys] can produce a stable
// sorted key order for callers that need canonical rather than insertion
// order.
type Map[K cmp.Ordered, V any] struct {
	keys  []K
	index map[K]int // key -> position in keys/vals; used only for lookup, never for iteration
	vals  []V
}

// NewMap returns an empty [Map]. The capacity hint pre-allocates the backing
// storage; it is advisory and never observable in iteration order.
func NewMap[K cmp.Ordered, V any](capacity int) *Map[K, V] {
	if capacity < 0 {
		capacity = 0
	}
	return &Map[K, V]{
		keys:  make([]K, 0, capacity),
		index: make(map[K]int, capacity),
		vals:  make([]V, 0, capacity),
	}
}

// ensure lazily initializes the index for a zero-value Map.
func (m *Map[K, V]) ensure() {
	if m.index == nil {
		m.index = make(map[K]int)
	}
}

// Set inserts or updates key with value. A new key is appended after all
// existing keys; an existing key keeps its position and only its value
// changes.
func (m *Map[K, V]) Set(key K, value V) {
	m.ensure()
	if i, ok := m.index[key]; ok {
		m.vals[i] = value
		return
	}
	m.index[key] = len(m.keys)
	m.keys = append(m.keys, key)
	m.vals = append(m.vals, value)
}

// Get returns the value for key and whether it was present.
func (m *Map[K, V]) Get(key K) (V, bool) {
	if i, ok := m.index[key]; ok {
		return m.vals[i], true
	}
	var zero V
	return zero, false
}

// Has reports whether key is present.
func (m *Map[K, V]) Has(key K) bool {
	_, ok := m.index[key]
	return ok
}

// Delete removes key if present and reports whether it was. Remaining keys
// keep their relative insertion order; the removed key's slot is closed so a
// later re-insertion appends at the end.
func (m *Map[K, V]) Delete(key K) bool {
	i, ok := m.index[key]
	if !ok {
		return false
	}
	m.keys = slices.Delete(m.keys, i, i+1)
	m.vals = slices.Delete(m.vals, i, i+1)
	delete(m.index, key)
	// Positions after i shifted left by one; repair the index.
	for j := i; j < len(m.keys); j++ {
		m.index[m.keys[j]] = j
	}
	return true
}

// Len returns the number of keys.
func (m *Map[K, V]) Len() int {
	return len(m.keys)
}

// Keys returns a copy of the keys in insertion order. The returned slice is
// owned by the caller and may be modified freely.
func (m *Map[K, V]) Keys() []K {
	return slices.Clone(m.keys)
}

// SortedKeys returns a copy of the keys sorted ascending by [cmp.Ordered].
// Use this where canonical rather than insertion order is required, so a
// hand-rolled map range never appears on an output path (ADR-003).
func (m *Map[K, V]) SortedKeys() []K {
	out := slices.Clone(m.keys)
	slices.Sort(out)
	return out
}

// Range calls yield for each pair in insertion order, stopping early if yield
// returns false. It matches the iter.Seq2 shape so a caller may write
// "for k, v := range m.Range" under a range-over-func loop.
func (m *Map[K, V]) Range(yield func(K, V) bool) {
	for i, k := range m.keys {
		if !yield(k, m.vals[i]) {
			return
		}
	}
}

// MarshalJSON encodes the map as a JSON object with keys in insertion order.
//
// This deliberately differs from encoding/json's treatment of a plain
// map[string]V, which sorts keys: here the emitted order is the caller's
// insertion order, which is what an authored, order-sensitive response body
// requires. Because the order is fixed by mutation history it is still fully
// deterministic. Keys are rendered via their default JSON encoding.
func (m *Map[K, V]) MarshalJSON() ([]byte, error) {
	if m.keys == nil {
		return []byte("{}"), nil
	}
	buf := make([]byte, 0, len(m.keys)*8)
	buf = append(buf, '{')
	for i, k := range m.keys {
		if i > 0 {
			buf = append(buf, ',')
		}
		kb, err := marshalKey(k)
		if err != nil {
			return nil, err
		}
		buf = append(buf, kb...)
		buf = append(buf, ':')
		vb, err := json.Marshal(m.vals[i])
		if err != nil {
			return nil, err
		}
		buf = append(buf, vb...)
	}
	buf = append(buf, '}')
	return buf, nil
}

// UnmarshalJSON decodes a JSON object into the map, replacing its contents and
// preserving the key order in which members appear in the input. A round trip
// through [Map.MarshalJSON] and UnmarshalJSON therefore preserves order, which
// [Map]'s use on order-sensitive output paths requires (ADR-003).
//
// It decodes the member name stream directly rather than through a plain
// map[K]V, because that would discard order. Duplicate member names resolve
// last-wins, matching encoding/json, and the surviving entry keeps the
// position of the first occurrence.
func (m *Map[K, V]) UnmarshalJSON(data []byte) error {
	m.keys = m.keys[:0]
	m.vals = m.vals[:0]
	m.index = make(map[K]int)

	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		if tok == nil {
			return nil // JSON null decodes to an empty map.
		}
		return fmt.Errorf("ordered.Map: expected JSON object, got %v", tok)
	}

	for dec.More() {
		keyTok, keyErr := dec.Token()
		if keyErr != nil {
			return keyErr
		}
		name, ok := keyTok.(string)
		if !ok {
			return fmt.Errorf("ordered.Map: object key is not a string: %v", keyTok)
		}
		key, keyErr := decodeKey[K](name)
		if keyErr != nil {
			return keyErr
		}
		var value V
		if valErr := dec.Decode(&value); valErr != nil {
			return valErr
		}
		m.Set(key, value)
	}
	// Consume the closing '}'.
	if _, err = dec.Token(); err != nil {
		return err
	}
	return nil
}

// decodeKey converts a JSON object member name back into a key of type K. For
// string-kinded keys it is the identity; for other ordered kinds (numbers) it
// re-parses through json.Unmarshal, mirroring marshalKey's quoting.
func decodeKey[K cmp.Ordered](name string) (K, error) {
	var key K
	if _, isString := any(key).(string); isString {
		if s, ok := any(name).(K); ok {
			return s, nil
		}
	}
	if err := json.Unmarshal([]byte(name), &key); err != nil {
		return key, fmt.Errorf("ordered.Map: cannot decode key %q: %w", name, err)
	}
	return key, nil
}

// marshalKey renders a map key as a JSON object member name. json.Marshal of
// an ordered key type yields a JSON string for string-kinded keys and a JSON
// number for numeric keys; the latter must be quoted to be a valid member
// name, matching encoding/json's own behavior for integer-keyed maps.
func marshalKey[K cmp.Ordered](k K) ([]byte, error) {
	kb, err := json.Marshal(k)
	if err != nil {
		return nil, err
	}
	if len(kb) > 0 && kb[0] == '"' {
		return kb, nil
	}
	out := make([]byte, 0, len(kb)+2)
	out = append(out, '"')
	out = append(out, kb...)
	out = append(out, '"')
	return out, nil
}
