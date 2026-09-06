package jsonrpc_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vyrodovalexey/mcp-mock-server/internal/jsonrpc"
)

// TestCanonicalKeyOrderIndependence covers acceptance criterion 2: two objects
// differing only in key order canonicalise identically, and objects differing
// in a value canonicalise differently.
func TestCanonicalKeyOrderIndependence(t *testing.T) {
	t.Parallel()
	a := json.RawMessage(`{"b":1,"a":2,"c":3}`)
	b := json.RawMessage(`{"c":3,"a":2,"b":1}`)
	ca, err := jsonrpc.Canonical(a)
	if err != nil {
		t.Fatal(err)
	}
	cb, err := jsonrpc.Canonical(b)
	if err != nil {
		t.Fatal(err)
	}
	if string(ca) != string(cb) {
		t.Errorf("key order changed canonical output:\n %s\n %s", ca, cb)
	}
	if string(ca) != `{"a":2,"b":1,"c":3}` {
		t.Errorf("canonical = %s, want sorted keys", ca)
	}

	// Differing value must differ.
	diff, err := jsonrpc.Canonical(json.RawMessage(`{"a":2,"b":9,"c":3}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(diff) == string(ca) {
		t.Error("objects differing in a value must canonicalise differently")
	}
}

// TestCanonicalNestedKeyOrder proves sorting recurses into nested objects and
// leaves array order intact.
func TestCanonicalNestedKeyOrder(t *testing.T) {
	t.Parallel()
	in := json.RawMessage(`{"z":{"y":1,"x":2},"a":[3,2,1]}`)
	out, err := jsonrpc.Canonical(in)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"a":[3,2,1],"z":{"x":2,"y":1}}`
	if string(out) != want {
		t.Errorf("canonical = %s, want %s", out, want)
	}
}

// TestCanonicalNumberFormatting covers the deliverable's number-formatting
// stability requirement: integral values normalise, and equivalent lexical
// forms collapse.
func TestCanonicalNumberFormatting(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want string
	}{
		{`1`, `1`},
		{`1.0`, `1`},
		{`1e0`, `1`},
		{`1E2`, `100`},
		{`-0`, `0`},
		{`1.5`, `1.5`},
		{`100`, `100`},
		{`0`, `0`},
		{`-42`, `-42`},
		{`3.14159`, `3.14159`},
		{`100000000000000000000000`, `100000000000000000000000`},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			// Wrap in an object so the value is a JSON document member.
			out, err := jsonrpc.CanonicalJSON([]byte(`{"n":` + tc.in + `}`))
			if err != nil {
				t.Fatalf("CanonicalJSON: %v", err)
			}
			want := `{"n":` + tc.want + `}`
			if string(out) != want {
				t.Errorf("canonical(%s) = %s, want %s", tc.in, out, want)
			}
		})
	}
}

// TestCanonicalEquivalentNumbersHashSame proves 1, 1.0 and 1e0 produce the same
// canonical bytes — the property ADR-002's request-key derivation relies on for
// bodies, while ids (which are NOT canonicalised) stay distinct.
func TestCanonicalEquivalentNumbersHashSame(t *testing.T) {
	t.Parallel()
	forms := []string{`{"v":1}`, `{"v":1.0}`, `{"v":1e0}`, `{"v":1.00}`}
	var first string
	for i, f := range forms {
		out, err := jsonrpc.CanonicalJSON([]byte(f))
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = string(out)
			continue
		}
		if string(out) != first {
			t.Errorf("form %q canonicalised to %s, want %s", f, out, first)
		}
	}
}

// TestCanonicalNoHTMLEscape covers the deliverable's HTML-escaping stability
// requirement: <, > and & are emitted literally.
func TestCanonicalNoHTMLEscape(t *testing.T) {
	t.Parallel()
	out, err := jsonrpc.Canonical(json.RawMessage(`{"s":"a<b>c&d"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `a<b>c&d`) {
		t.Errorf("HTML characters escaped: %s", out)
	}
	if strings.Contains(string(out), `\u003c`) {
		t.Errorf("canonical contains \\u003c: %s", out)
	}
}

// TestCanonicalStringEscaping covers minimal RFC 8785 string escaping: control
// characters use short forms, and unicode passes through as UTF-8.
func TestCanonicalStringEscaping(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want string
	}{
		{"tab\there", `"tab\there"`},
		{"new\nline", `"new\nline"`},
		{"quote\"x", `"quote\"x"`},
		{"back\\slash", `"back\\slash"`},
		{"unit\u0001sep", `"unit\u0001sep"`},
		{"héllo", `"héllo"`},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			t.Parallel()
			out, err := jsonrpc.Canonical(tc.in)
			if err != nil {
				t.Fatal(err)
			}
			if string(out) != tc.want {
				t.Errorf("canonical(%q) = %s, want %s", tc.in, out, tc.want)
			}
		})
	}
}

// TestCanonicalRoundTripByteIdentity covers the deliverable's repeated-encode
// byte-identity requirement: canonicalising the same document many times yields
// identical bytes every time.
func TestCanonicalRoundTripByteIdentity(t *testing.T) {
	t.Parallel()
	doc := json.RawMessage(`{"m":"tools/call","p":{"z":1,"a":[1,2,{"k":true,"j":null}]},"id":7}`)
	first, err := jsonrpc.Canonical(doc)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 1000; i++ {
		again, err := jsonrpc.Canonical(doc)
		if err != nil {
			t.Fatal(err)
		}
		if string(again) != string(first) {
			t.Fatalf("iteration %d differs:\n %s\n %s", i, again, first)
		}
	}
}

// TestCanonicalGoldenVector covers acceptance criterion 3: canonical output is
// stable against a committed golden vector. Running with GOMAXPROCS varied is
// done by the -race -count=10 gate and by go test env; the committed file makes
// cross-process stability an explicit assertion. Regenerate with -update only
// when the derivation intentionally changes (a breaking change per ADR-002).
func TestCanonicalGoldenVector(t *testing.T) {
	t.Parallel()
	input := json.RawMessage(`{
		"method":"tools/call",
		"params":{"name":"echo","arguments":{"z":1,"a":"x<y>&z"},"_meta":{"protocolVersion":"1","clientCapabilities":{}}},
		"id":1,
		"nums":[1,1.0,1e0,-0,1.5,100000000000000000000000],
		"jsonrpc":"2.0"
	}`)
	got, err := jsonrpc.Canonical(input)
	if err != nil {
		t.Fatal(err)
	}
	golden := filepath.Join("testdata", "canonical_vector.json")
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read golden %s: %v (run TestGenerateGolden manually if intentionally changing)", golden, err)
	}
	if string(got) != strings.TrimRight(string(want), "\n") {
		t.Errorf("canonical golden mismatch:\n got: %s\nwant: %s", got, strings.TrimRight(string(want), "\n"))
	}
}

// TestCanonicalMalformedInput covers panic-freedom on the canonical path for
// invalid or over-nested JSON.
func TestCanonicalMalformedInput(t *testing.T) {
	t.Parallel()
	for _, in := range []string{`{`, `not json`, ``, `{"a":1} trailing`, "[" + strings.Repeat("[", 200000)} {
		if _, err := jsonrpc.CanonicalJSON([]byte(in)); err == nil {
			t.Errorf("CanonicalJSON(%q) = nil error, want error", in)
		}
	}
}

// TestCanonicalArbitraryValueInput proves Canonical accepts a Go value (not just
// raw bytes) and produces sorted output from a map.
func TestCanonicalArbitraryValueInput(t *testing.T) {
	t.Parallel()
	v := map[string]any{"b": 1, "a": 2}
	out, err := jsonrpc.Canonical(v)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"a":2,"b":1}` {
		t.Errorf("canonical(map) = %s, want sorted", out)
	}
}
