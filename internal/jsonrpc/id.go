package jsonrpc

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// IDKind classifies the JSON type of a JSON-RPC 2.0 id.
//
// JSON-RPC 2.0 (§4) permits an id to be a String, a Number, or Null; a request
// object with no id member is a Notification and carries no id at all. These
// cases are distinguishable and must stay distinguishable: MOCK-247
// (AssertRetryIDsDistinct) treats a numeric 1 and a string "1" as different id
// values, and MOCK-503 deliberately emits null and mismatched ids. Collapsing
// them into a single Go type would make those cases unrepresentable.
type IDKind uint8

const (
	// IDAbsent means no id member was present. This is how a Notification is
	// distinguished from a Request whose id happens to be null (MOCK-503).
	IDAbsent IDKind = iota
	// IDNull means the id member was present with the JSON value null.
	IDNull
	// IDString means the id was a JSON string.
	IDString
	// IDNumber means the id was a JSON number (integer or floating point).
	IDNumber
)

// String returns a stable, lowercase name for the kind, for logs and test
// diagnostics. It never panics on an out-of-range value.
func (k IDKind) String() string {
	switch k {
	case IDAbsent:
		return "absent"
	case IDNull:
		return "null"
	case IDString:
		return "string"
	case IDNumber:
		return "number"
	default:
		return fmt.Sprintf("IDKind(%d)", uint8(k))
	}
}

// ErrInvalidID reports that a byte sequence is not a well-formed JSON-RPC id.
// Per JSON-RPC 2.0 an id must be a string, a number, or null; a boolean,
// object, or array is rejected. It is returned wrapped, so call sites match it
// with errors.Is.
var ErrInvalidID = errors.New("jsonrpc: id must be a string, number, or null")

// ID is a JSON-RPC 2.0 request/response id preserved as the exact bytes in
// which it was received.
//
// # Why raw bytes
//
// ADR-002 derives the per-request key from canonicalJSONRPCID — "raw bytes as
// received" — so a numeric id 1 and a string id "1" produce different request
// keys and therefore different seeded behavior. The id is also echoed into the
// response byte-for-byte (MOCK-203, builtin-tools.md §4.3 [D]): a hub that
// rewrites 1 to "1" must be caught, which is only possible if mcpmock never
// itself normalises the id. ID therefore stores the raw token and reports its
// kind, rather than decoding into an int64/string that would discard the
// original lexical form (for example 1 vs 1.0 vs 1e0, or leading-zero-free vs
// not).
//
// The zero value is a valid absent id (kind IDAbsent), which represents a
// Notification. ID is immutable once constructed and is safe for concurrent
// reads; it holds no process-global state (ADR-007).
type ID struct {
	// raw holds the exact received bytes of the id token for a present id, or
	// nil when the id is absent. For IDNull it holds the four bytes "null".
	raw  []byte
	kind IDKind
}

// AbsentID is the id of a Notification: no id member was present. It is the
// zero value of ID and is provided as a named constant for readable call sites.
var AbsentID = ID{}

// NullID is an explicit JSON null id. JSON-RPC 2.0 permits it, and MOCK-503
// emits it deliberately; it is distinct from AbsentID, which carries no id at
// all.
func NullID() ID {
	return ID{raw: []byte("null"), kind: IDNull}
}

// StringID returns an id holding the given JSON string value. The value is
// JSON-encoded (quoted and escaped) so the stored raw bytes are a valid JSON
// string token, matching what would have been received on the wire.
func StringID(s string) ID {
	// json.Marshal of a string cannot fail; the result is a quoted token.
	b, _ := json.Marshal(s)
	return ID{raw: b, kind: IDString}
}

// RawID wraps bytes that are already a JSON id token, validating that they form
// a string, number, or null and nothing else. It preserves the bytes verbatim,
// so 1, 1.0 and 1e0 remain distinct. Surrounding insignificant whitespace is
// trimmed; the token itself is never reformatted. It returns ErrInvalidID for
// a boolean, object, array, empty input, or trailing garbage.
func RawID(b []byte) (ID, error) {
	trimmed := bytes.TrimSpace(b)
	if len(trimmed) == 0 {
		return ID{}, fmt.Errorf("%w: empty", ErrInvalidID)
	}
	kind, err := classifyID(trimmed)
	if err != nil {
		return ID{}, err
	}
	// Copy so a later mutation of the caller's buffer cannot alter the id.
	stored := make([]byte, len(trimmed))
	copy(stored, trimmed)
	return ID{raw: stored, kind: kind}, nil
}

// classifyID validates that trimmed is a single JSON string, number, or null
// token and returns its kind. It rejects any other JSON value.
func classifyID(trimmed []byte) (IDKind, error) {
	// Confirm the token is well-formed JSON and consumes the whole slice, so
	// "1 2" or "1x" is rejected rather than silently truncated.
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return 0, fmt.Errorf("%w: %w", ErrInvalidID, err)
	}
	if dec.More() {
		return 0, fmt.Errorf("%w: trailing tokens after id", ErrInvalidID)
	}
	switch tok.(type) {
	case nil:
		return IDNull, nil
	case string:
		return IDString, nil
	case json.Number:
		return IDNumber, nil
	default:
		return 0, fmt.Errorf("%w: got %T", ErrInvalidID, tok)
	}
}

// Kind reports the JSON kind of the id.
func (id ID) Kind() IDKind { return id.kind }

// IsAbsent reports whether no id member was present (a Notification).
func (id ID) IsAbsent() bool { return id.kind == IDAbsent }

// IsNull reports whether the id member was present with the value null.
func (id ID) IsNull() bool { return id.kind == IDNull }

// Raw returns a copy of the exact id bytes as received, or nil for an absent
// id. The copy protects the internal representation from mutation by the
// caller. For a null id it returns the bytes "null".
func (id ID) Raw() []byte {
	if id.raw == nil {
		return nil
	}
	out := make([]byte, len(id.raw))
	copy(out, id.raw)
	return out
}

// Equal reports whether two ids are byte-identical and of the same kind. It is
// the comparison MOCK-247 (AssertRetryIDsDistinct) needs: numeric 1 and string
// "1" are not equal because their raw tokens differ, and 1 and 1.0 are not
// equal because their raw tokens differ. Two absent ids are equal.
func (id ID) Equal(other ID) bool {
	if id.kind != other.kind {
		return false
	}
	return bytes.Equal(id.raw, other.raw)
}

// MarshalJSON emits the id exactly as stored, so a response echoes the received
// id byte-for-byte (MOCK-203). An absent id marshals to null; a caller that
// must distinguish absent from explicit-null for response emission should
// consult IsAbsent before marshaling, because JSON has no representation for
// "member not present" at the value level.
func (id ID) MarshalJSON() ([]byte, error) {
	if id.raw == nil {
		return []byte("null"), nil
	}
	out := make([]byte, len(id.raw))
	copy(out, id.raw)
	return out, nil
}

// UnmarshalJSON stores the incoming id token verbatim after validating that it
// is a string, number, or null. Because encoding/json only calls UnmarshalJSON
// when the member is present, an absent id is represented by leaving the field
// at its zero value; envelope decoding tracks presence separately.
func (id *ID) UnmarshalJSON(data []byte) error {
	parsed, err := RawID(data)
	if err != nil {
		return err
	}
	*id = parsed
	return nil
}
