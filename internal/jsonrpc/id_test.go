package jsonrpc_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/vyrodovalexey/mcp-mock-server/internal/jsonrpc"
)

// TestRawIDPreservesLexicalForm covers acceptance criterion 1: numeric, string
// and null ids are handled distinguishably and preserved byte-for-byte, and
// distinct lexical forms of the same numeric value stay distinct (MOCK-247).
func TestRawIDPreservesLexicalForm(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		kind jsonrpc.IDKind
	}{
		{"integer", `1`, jsonrpc.IDNumber},
		{"float", `1.0`, jsonrpc.IDNumber},
		{"exponent", `1e0`, jsonrpc.IDNumber},
		{"negative", `-42`, jsonrpc.IDNumber},
		{"string", `"1"`, jsonrpc.IDString},
		{"string-word", `"abc"`, jsonrpc.IDString},
		{"null", `null`, jsonrpc.IDNull},
		{"large-int", `100000000000000000000000`, jsonrpc.IDNumber},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			id, err := jsonrpc.RawID([]byte(tc.in))
			if err != nil {
				t.Fatalf("RawID(%q) unexpected error: %v", tc.in, err)
			}
			if id.Kind() != tc.kind {
				t.Errorf("Kind() = %v, want %v", id.Kind(), tc.kind)
			}
			if got := string(id.Raw()); got != tc.in {
				t.Errorf("Raw() = %q, want %q (must be byte-for-byte)", got, tc.in)
			}
			out, err := id.MarshalJSON()
			if err != nil {
				t.Fatalf("MarshalJSON: %v", err)
			}
			if string(out) != tc.in {
				t.Errorf("MarshalJSON echoed %q, want %q", out, tc.in)
			}
		})
	}
}

// TestRawIDDistinctness proves the values MOCK-247 must tell apart are not
// equal: 1 vs "1" vs 1.0.
func TestRawIDDistinctness(t *testing.T) {
	t.Parallel()
	num, _ := jsonrpc.RawID([]byte(`1`))
	str, _ := jsonrpc.RawID([]byte(`"1"`))
	flt, _ := jsonrpc.RawID([]byte(`1.0`))

	if num.Equal(str) {
		t.Error("numeric 1 must not equal string \"1\"")
	}
	if num.Equal(flt) {
		t.Error("1 must not equal 1.0 (distinct lexical form)")
	}
	if !num.Equal(num) {
		t.Error("id must equal itself")
	}
}

// TestRawIDRejectsNonID covers that a boolean, object or array is not a valid
// id and returns ErrInvalidID rather than being silently accepted.
func TestRawIDRejectsNonID(t *testing.T) {
	t.Parallel()
	for _, in := range []string{`true`, `false`, `{}`, `[1]`, ``, `  `, `1 2`, `1x`} {
		if _, err := jsonrpc.RawID([]byte(in)); !errors.Is(err, jsonrpc.ErrInvalidID) {
			t.Errorf("RawID(%q) error = %v, want ErrInvalidID", in, err)
		}
	}
}

// TestAbsentVsNull covers the MOCK-503 distinction between a notification
// (absent id) and an explicit null id.
func TestAbsentVsNull(t *testing.T) {
	t.Parallel()
	absent := jsonrpc.AbsentID
	null := jsonrpc.NullID()

	if !absent.IsAbsent() {
		t.Error("AbsentID.IsAbsent() = false")
	}
	if absent.IsNull() {
		t.Error("AbsentID.IsNull() = true")
	}
	if null.IsAbsent() {
		t.Error("NullID.IsAbsent() = true")
	}
	if !null.IsNull() {
		t.Error("NullID.IsNull() = false")
	}
	if absent.Equal(null) {
		t.Error("absent id must not equal explicit null id")
	}
	if absent.Kind() != jsonrpc.IDAbsent || null.Kind() != jsonrpc.IDNull {
		t.Errorf("kinds: absent=%v null=%v", absent.Kind(), null.Kind())
	}
}

// TestStringIDEscapes covers that StringID produces a valid JSON string token,
// escaping as needed, and round-trips.
func TestStringIDEscapes(t *testing.T) {
	t.Parallel()
	id := jsonrpc.StringID(`a"b`)
	raw := id.Raw()
	var decoded string
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("StringID produced invalid JSON %q: %v", raw, err)
	}
	if decoded != `a"b` {
		t.Errorf("round-trip = %q, want %q", decoded, `a"b`)
	}
}

// TestIDRawIsCopied ensures Raw returns a defensive copy so a caller cannot
// mutate the id's internal bytes.
func TestIDRawIsCopied(t *testing.T) {
	t.Parallel()
	id, _ := jsonrpc.RawID([]byte(`123`))
	b := id.Raw()
	b[0] = 'x'
	if !bytes.Equal(id.Raw(), []byte(`123`)) {
		t.Error("mutating Raw() result altered the id")
	}
}

// TestIDKindString exercises the human-readable kind names, including the
// out-of-range default branch, so it never panics.
func TestIDKindString(t *testing.T) {
	t.Parallel()
	cases := map[jsonrpc.IDKind]string{
		jsonrpc.IDAbsent: "absent",
		jsonrpc.IDNull:   "null",
		jsonrpc.IDString: "string",
		jsonrpc.IDNumber: "number",
	}
	for k, want := range cases {
		if got := k.String(); got != want {
			t.Errorf("IDKind(%d).String() = %q, want %q", k, got, want)
		}
	}
	if got := jsonrpc.IDKind(99).String(); got == "" {
		t.Error("out-of-range IDKind.String() must not be empty")
	}
}

// TestIDUnmarshalInStruct proves an ID field decodes from an embedding struct,
// preserving raw form, and rejects an invalid id.
func TestIDUnmarshalInStruct(t *testing.T) {
	t.Parallel()
	type holder struct {
		ID jsonrpc.ID `json:"id"`
	}
	var h holder
	if err := json.Unmarshal([]byte(`{"id":1.0}`), &h); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if string(h.ID.Raw()) != "1.0" {
		t.Errorf("decoded id = %q, want 1.0", h.ID.Raw())
	}
	var bad holder
	if err := json.Unmarshal([]byte(`{"id":true}`), &bad); !errors.Is(err, jsonrpc.ErrInvalidID) {
		t.Errorf("decoding boolean id error = %v, want ErrInvalidID", err)
	}
}
