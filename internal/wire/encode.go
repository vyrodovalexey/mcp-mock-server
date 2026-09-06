package wire

import (
	"bytes"
	"encoding/json"
)

// marshalNoHTMLEscape encodes v as JSON with HTML escaping disabled and without
// the trailing newline json.Encoder appends. It is the byte-stability primitive
// behind [MarshalResult]: encoding/json's default escapes <, > and & to
// \u003c, \u003e and \u0026, which would silently change the bytes of a result
// carrying those characters and break golden-file comparison (design principle
// §0.1).
//
// It mirrors the equivalent unexported helper in internal/jsonrpc so that a
// result marshaled here and a response encoded there share the same escaping
// discipline. It is a pure function with no package-global state and is safe for
// concurrent use.
func marshalNoHTMLEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	out := buf.Bytes()
	if n := len(out); n > 0 && out[n-1] == '\n' {
		out = out[:n-1]
	}
	return out, nil
}
