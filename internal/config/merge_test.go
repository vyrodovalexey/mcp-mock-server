package config

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
)

// decodeJSON turns a JSON literal into a decoded tree for merge-table cases.
func decodeJSON(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("decode %q: %v", s, err)
	}
	return v
}

// TestMergeTable exercises one case per ADR-008 merge-table row against the raw
// tree merge, with no file I/O. The instance path is chosen so the schema
// directive resolver classifies the arrays correctly (703.2, 703.3, 703.4).
func TestMergeTable(t *testing.T) {
	dir, err := mergeDirectivesFor()
	if err != nil {
		t.Fatalf("merge directives: %v", err)
	}

	cases := []struct {
		name    string
		path    string
		base    string
		overlay string
		want    string
	}{
		{
			name:    "object recursive merge",
			path:    "",
			base:    `{"a":{"x":1},"b":2}`,
			overlay: `{"a":{"y":3}}`,
			want:    `{"a":{"x":1,"y":3},"b":2}`,
		},
		{
			name:    "scalar replace",
			path:    "",
			base:    `{"a":1}`,
			overlay: `{"a":2}`,
			want:    `{"a":2}`,
		},
		{
			name:    "scalar replace zero value is a set not a delete",
			path:    "",
			base:    `{"enabled":true}`,
			overlay: `{"enabled":false}`,
			want:    `{"enabled":false}`,
		},
		{
			name:    "null deletes the key",
			path:    "",
			base:    `{"a":1,"b":2}`,
			overlay: `{"b":null}`,
			want:    `{"a":1}`,
		},
		{
			name:    "null delete distinguished from absent (absent inherits)",
			path:    "",
			base:    `{"a":1,"b":2}`,
			overlay: `{"a":9}`,
			want:    `{"a":9,"b":2}`,
		},
		{
			// /spec/catalogue/items is x-mcpmock-merge-key: name.
			name:    "keyed list merges by name preserving base order",
			path:    "/spec/catalogue/items",
			base:    `[{"name":"a","v":1},{"name":"b","v":2}]`,
			overlay: `[{"name":"a","v":9},{"name":"c","v":3}]`,
			want:    `[{"name":"a","v":9},{"name":"b","v":2},{"name":"c","v":3}]`,
		},
		{
			// /spec/faults is x-mcpmock-merge-key: id.
			name:    "keyed list by id",
			path:    "/spec/faults",
			base:    `[{"id":"x","on":true},{"id":"y"}]`,
			overlay: `[{"id":"y","on":false}]`,
			want:    `[{"id":"x","on":true},{"id":"y","on":false}]`,
		},
		{
			// /spec/transport/kinds is x-mcpmock-merge: replace (scalar array).
			name:    "scalar array replaces wholesale",
			path:    "/spec/transport/kinds",
			base:    `["http","stdio"]`,
			overlay: `["http"]`,
			want:    `["http"]`,
		},
		{
			name:    "unknown-path array defaults to replace",
			path:    "/spec/unknownArray",
			base:    `[1,2,3]`,
			overlay: `[4]`,
			want:    `[4]`,
		},
		{
			name:    "patch replace overrides a keyed list",
			path:    "/spec/catalogue/items",
			base:    `[{"name":"a"},{"name":"b"}]`,
			overlay: `[{"$patch":"replace"},{"name":"c"}]`,
			want:    `[{"name":"c"}]`,
		},
		{
			name:    "patch append concatenates",
			path:    "/spec/catalogue/items",
			base:    `[{"name":"a"}]`,
			overlay: `[{"$patch":"append"},{"name":"a"},{"name":"b"}]`,
			want:    `[{"name":"a"},{"name":"a"},{"name":"b"}]`,
		},
		{
			name:    "patch merge on keyed list merges by key",
			path:    "/spec/catalogue/items",
			base:    `[{"name":"a","v":1}]`,
			overlay: `[{"$patch":"merge"},{"name":"a","v":2}]`,
			want:    `[{"name":"a","v":2}]`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := decodeJSON(t, tc.base)
			overlay := decodeJSON(t, tc.overlay)
			got := mergeTrees(base, overlay, tc.path, dir)
			want := decodeJSON(t, tc.want)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("merge mismatch:\n got  %v\n want %v", got, want)
			}
		})
	}
}

// TestMerge_KeyedListInsertionDoesNotMisalignOverlay proves the keyed-list rule
// is by-key, not positional (703.4): inserting an element at the FRONT of the
// base list must not cause an overlay update to hit the wrong element.
func TestMerge_KeyedListInsertionDoesNotMisalignOverlay(t *testing.T) {
	dir, err := mergeDirectivesFor()
	if err != nil {
		t.Fatalf("merge directives: %v", err)
	}
	// Base originally had [beta]; someone inserts 'alpha' at the front.
	base := decodeJSON(t, `[{"name":"alpha","v":"a"},{"name":"beta","v":"b"}]`)
	// The overlay only means to change 'beta'. Positionally it is index 0 in the
	// overlay, but by key it must land on 'beta' regardless of base order.
	overlay := decodeJSON(t, `[{"name":"beta","v":"B"}]`)
	got := mergeTrees(base, overlay, "/spec/catalogue/items", dir)
	want := decodeJSON(t, `[{"name":"alpha","v":"a"},{"name":"beta","v":"B"}]`)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("keyed merge misaligned after base insertion:\n got  %v\n want %v", got, want)
	}
}

// TestMerge_DoesNotMutateInputs proves the merge shares no mutable structure
// with either input, so composing the same base under two overlays cannot let
// one overlay corrupt the other's result.
func TestMerge_DoesNotMutateInputs(t *testing.T) {
	dir, err := mergeDirectivesFor()
	if err != nil {
		t.Fatalf("merge directives: %v", err)
	}
	base := decodeJSON(t, `{"a":{"x":1},"list":[{"name":"n","v":1}]}`)
	baseCopy := decodeJSON(t, `{"a":{"x":1},"list":[{"name":"n","v":1}]}`)
	overlay := decodeJSON(t, `{"a":{"y":2}}`)
	_ = mergeTrees(base, overlay, "", dir)
	if !reflect.DeepEqual(base, baseCopy) {
		t.Errorf("merge mutated its base input:\n got  %v\n want %v", base, baseCopy)
	}
}

// TestArrayDirectiveResolver asserts the schema-driven directive resolver reads
// the declared merge key for keyed lists and does not invent one for replace
// lists (703.4, 703.5 machinery).
func TestArrayDirectiveResolver(t *testing.T) {
	dir, err := mergeDirectivesFor()
	if err != nil {
		t.Fatalf("merge directives: %v", err)
	}
	keyed := map[string]string{
		"/spec/catalogue/items":      "name",
		"/spec/faults":               "id",
		"/spec/catalogue/drift":      "id",
		"/spec/paging/perPage":       "page",
		"/spec/mrtr/inputRequests":   "key",
		"/spec/subscriptions/timers": "id",
	}
	for path, wantKey := range keyed {
		gotKey, ok := dir.arrayKey(path)
		if !ok || gotKey != wantKey {
			t.Errorf("arrayKey(%q) = (%q,%v), want (%q,true)", path, gotKey, ok, wantKey)
		}
	}
	replaceLists := []string{
		"/spec/transport/kinds",
		"/spec/discover/supportedVersions",
		"/extends",
	}
	for _, path := range replaceLists {
		if _, ok := dir.arrayKey(path); ok {
			t.Errorf("arrayKey(%q) reported keyed; want replace (not keyed)", path)
		}
	}
}

// TestCanonicalJSON_Deterministic proves the canonical encoding of a decoded
// tree is byte-identical regardless of Go map ordering, by encoding many times
// and comparing (ADR-008 §0.1 determinism, the property the composed output
// relies on).
func TestCanonicalJSON_Deterministic(t *testing.T) {
	tree := decodeJSON(t, `{"z":1,"a":{"q":2,"b":3},"m":[{"name":"y"},{"name":"x"}]}`)
	first, err := canonicalJSON(tree)
	if err != nil {
		t.Fatalf("canonical: %v", err)
	}
	for i := 0; i < 200; i++ {
		got, err := canonicalJSON(tree)
		if err != nil {
			t.Fatalf("canonical run %d: %v", i, err)
		}
		if !bytes.Equal(first, got) {
			t.Fatalf("canonical output not stable at run %d:\n first %s\n got   %s", i, first, got)
		}
	}
}
