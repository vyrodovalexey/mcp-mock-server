package jsonrpc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strconv"
	"unicode/utf16"
)

// Canonical produces an RFC 8785-style canonical JSON encoding of v.
//
// # Purpose
//
// This encoder exists for one job: hashing. ADR-002 derives the per-request key
// from sha256 over a canonical JSON form of the request body, so two byte
// sequences that denote the same JSON value must canonicalise to identical
// bytes, and any semantic difference must survive. ADR-003 §4 additionally
// names it for request digests, requestState plaintext, golden files and
// AssertHeaderMatchesBody.
//
// It is emphatically NOT used for response emission: MOCK-222.4 requires
// hand-authored key order and deliberately malformed structures to be emitted
// verbatim, which is what [Response] and ordered.Map do. Applying Canonical to
// a response would destroy authored order. The two paths are kept separate on
// purpose.
//
// # Byte-stability guarantees
//
// For any two inputs that parse to the same JSON value, Canonical returns
// identical bytes; for inputs that parse to different values, it returns
// different bytes. Concretely:
//
//   - Object keys are emitted in ascending order by their UTF-16 code-unit
//     sequence (RFC 8785 §3.2.3), independent of input order and of Go map
//     iteration order.
//   - No insignificant whitespace is emitted: no spaces, no newlines, no
//     indentation.
//   - HTML escaping is disabled: <, > and & are emitted literally, never as
//     \u003c/\u003e/\u0026, so canonical bytes do not depend on encoding/json's
//     default HTML-escaping behavior.
//   - Numbers are rendered by the ECMAScript Number-to-String algorithm (RFC
//     8785 §3.2.2.3): an integral value carries no fraction or exponent, so 1,
//     1.0 and 1e0 all canonicalise to "1"; non-integral values use the shortest
//     round-tripping decimal. Integers outside float64's exact range keep their
//     exact digits.
//   - Strings are minimally escaped per RFC 8785 §3.2.2.2 (only ", \\ and the
//     C0 controls, with the two-character forms for the named controls), so two
//     encodings of the same string produce identical bytes.
//   - A JSON null is emitted as null; there is no nil-versus-empty ambiguity
//     because Canonical operates on parsed JSON, where a member is either
//     present with a value or absent entirely.
//
// Canonical is a pure function with no package-global state and is safe for
// concurrent use.
func Canonical(v any) ([]byte, error) {
	raw, err := toRawJSON(v)
	if err != nil {
		return nil, err
	}
	node, err := parseCanonical(raw)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := writeCanonical(&buf, node); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// CanonicalJSON is a convenience wrapper that canonicalises already-serialized
// JSON bytes. It is the form ADR-002's derivation path uses: sha256 over
// CanonicalJSON(body). It returns a -32700-style wrapped error for input that
// is not valid JSON.
func CanonicalJSON(data []byte) ([]byte, error) {
	node, err := parseCanonical(data)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := writeCanonical(&buf, node); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// toRawJSON reduces an arbitrary value to its JSON bytes. When v is already
// json.RawMessage or []byte it is used directly (and validated during parsing);
// otherwise it is marshaled with HTML escaping disabled so the intermediate
// form does not itself introduce escapes.
func toRawJSON(v any) ([]byte, error) {
	switch t := v.(type) {
	case json.RawMessage:
		return t, nil
	case []byte:
		return t, nil
	default:
		return marshalNoHTMLEscape(v)
	}
}

// parseCanonical decodes JSON into a tree of *ordered* nodes. Numbers are kept
// as json.Number so no float64 rounding happens before we choose their
// canonical form. Object member order from the input is discarded here because
// writeCanonical re-sorts, but decoding through json.Decoder bounds nesting and
// rejects malformed input without panicking.
func parseCanonical(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	node, err := decodeValue(dec)
	if err != nil {
		return nil, fmt.Errorf("jsonrpc: canonical parse: %w", err)
	}
	if dec.More() {
		return nil, fmt.Errorf("jsonrpc: canonical parse: trailing content after JSON value")
	}
	return node, nil
}

// canonMember is a decoded object member awaiting canonical key sorting.
type canonMember struct {
	key string
	val any
}

// decodeValue reads one JSON value from dec, recursing into objects and arrays.
// It relies on json.Decoder for depth bounding, so pathologically nested input
// returns an error rather than overflowing the stack.
func decodeValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if delim, ok := tok.(json.Delim); ok {
		switch delim {
		case '{':
			return decodeObject(dec)
		case '[':
			return decodeArray(dec)
		}
	}
	// Scalar: nil, bool, string, or json.Number (UseNumber is set).
	return tok, nil
}

// decodeObject reads members until the closing brace, preserving decoded values
// but not order (writeCanonical sorts).
func decodeObject(dec *json.Decoder) (any, error) {
	members := make([]canonMember, 0, 8)
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, fmt.Errorf("object key is not a string: %v", keyTok)
		}
		val, err := decodeValue(dec)
		if err != nil {
			return nil, err
		}
		members = append(members, canonMember{key: key, val: val})
	}
	if _, err := dec.Token(); err != nil { // consume '}'
		return nil, err
	}
	return members, nil
}

// decodeArray reads elements until the closing bracket, preserving order, which
// is significant for arrays.
func decodeArray(dec *json.Decoder) (any, error) {
	elems := make([]any, 0, 8)
	for dec.More() {
		val, err := decodeValue(dec)
		if err != nil {
			return nil, err
		}
		elems = append(elems, val)
	}
	if _, err := dec.Token(); err != nil { // consume ']'
		return nil, err
	}
	return elems, nil
}

// writeCanonical emits node in canonical form. It dispatches on the node types
// produced by decodeValue.
func writeCanonical(buf *bytes.Buffer, node any) error {
	switch t := node.(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		if t {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case string:
		writeCanonicalString(buf, t)
	case json.Number:
		s, err := canonicalNumber(t)
		if err != nil {
			return err
		}
		buf.WriteString(s)
	case []canonMember:
		return writeCanonicalObject(buf, t)
	case []any:
		return writeCanonicalArray(buf, t)
	default:
		return fmt.Errorf("jsonrpc: canonical: unexpected node type %T", node)
	}
	return nil
}

// writeCanonicalObject sorts members by RFC 8785 key order and emits them with
// no whitespace.
func writeCanonicalObject(buf *bytes.Buffer, members []canonMember) error {
	sorted := slices.Clone(members)
	slices.SortStableFunc(sorted, func(a, b canonMember) int {
		return compareUTF16(a.key, b.key)
	})
	buf.WriteByte('{')
	for i := range sorted {
		if i > 0 {
			buf.WriteByte(',')
		}
		writeCanonicalString(buf, sorted[i].key)
		buf.WriteByte(':')
		if err := writeCanonical(buf, sorted[i].val); err != nil {
			return err
		}
	}
	buf.WriteByte('}')
	return nil
}

// writeCanonicalArray emits elements in order with no whitespace.
func writeCanonicalArray(buf *bytes.Buffer, elems []any) error {
	buf.WriteByte('[')
	for i, e := range elems {
		if i > 0 {
			buf.WriteByte(',')
		}
		if err := writeCanonical(buf, e); err != nil {
			return err
		}
	}
	buf.WriteByte(']')
	return nil
}

// canonicalNumber renders n per RFC 8785 §3.2.2.3 (the ECMAScript
// Number::toString form). Integral values within float64's exact-integer range
// are emitted without a fraction or exponent, so 1, 1.0 and 1e0 all become "1".
// Very large integers that json.Number holds exactly but float64 cannot
// represent are emitted from their exact digits rather than being rounded.
func canonicalNumber(n json.Number) (string, error) {
	s := n.String()
	// Fast path: a plain integer literal with no fraction or exponent is
	// already canonical, and this preserves exact large integers.
	if isPlainInteger(s) {
		return normalizeIntegerLiteral(s), nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return "", fmt.Errorf("jsonrpc: canonical number %q: %w", s, err)
	}
	if math.IsInf(f, 0) || math.IsNaN(f) {
		return "", fmt.Errorf("jsonrpc: canonical number %q is not finite", s)
	}
	// An integral float renders without a fractional part (RFC 8785).
	if f == math.Trunc(f) && math.Abs(f) < 1e21 {
		return strconv.FormatFloat(f, 'f', -1, 64), nil
	}
	// Shortest round-tripping form; strconv already emits ES6-compatible
	// output for the finite non-integral range Canonical accepts.
	return strconv.FormatFloat(f, 'g', -1, 64), nil
}

// isPlainInteger reports whether s is a JSON integer literal with no fraction
// and no exponent (optionally signed).
func isPlainInteger(s string) bool {
	if s == "" {
		return false
	}
	i := 0
	if s[0] == '-' {
		i = 1
	}
	if i == len(s) {
		return false
	}
	for ; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// normalizeIntegerLiteral removes a redundant leading "-" on zero so "-0"
// canonicalises to "0", matching RFC 8785's treatment of negative zero. JSON
// does not permit leading zeros, so no other normalisation is needed.
func normalizeIntegerLiteral(s string) string {
	if s == "-0" {
		return "0"
	}
	return s
}

// compareUTF16 orders two strings by their UTF-16 code-unit sequences, which is
// the ordering RFC 8785 §3.2.3 mandates for object keys. For strings in the
// Basic Multilingual Plane this coincides with a rune comparison; the
// distinction matters only for supplementary-plane characters, where UTF-16
// surrogate pairs sort differently from raw code points. It uses the standard
// library utf16.Encode so surrogate splitting is handled correctly.
func compareUTF16(a, b string) int {
	ar := utf16.Encode([]rune(a))
	br := utf16.Encode([]rune(b))
	return slices.Compare(ar, br)
}

// writeCanonicalString emits s as a JSON string with RFC 8785 §3.2.2.2 minimal
// escaping: quote, backslash and the C0 control characters are escaped (using
// the short \b \t \n \f \r forms where defined, \u00XX otherwise), and every
// other character — including <, >, & and all non-ASCII — is emitted literally
// as UTF-8. This is intentionally narrower than encoding/json's default, which
// also escapes HTML-significant bytes.
func writeCanonicalString(buf *bytes.Buffer, s string) {
	buf.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			buf.WriteString(`\"`)
		case '\\':
			buf.WriteString(`\\`)
		case '\b':
			buf.WriteString(`\b`)
		case '\t':
			buf.WriteString(`\t`)
		case '\n':
			buf.WriteString(`\n`)
		case '\f':
			buf.WriteString(`\f`)
		case '\r':
			buf.WriteString(`\r`)
		default:
			if r < 0x20 {
				buf.WriteString(`\u00`)
				const hexDigits = "0123456789abcdef"
				buf.WriteByte(hexDigits[(r>>4)&0xF])
				buf.WriteByte(hexDigits[r&0xF])
				continue
			}
			buf.WriteRune(r)
		}
	}
	buf.WriteByte('"')
}
