package config

import (
	"bytes"
	"sort"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// TestSchema_EveryObjectArrayDeclaresMergeDirective is the 703.5 schema-lint
// gate: every array in scenario.schema.json whose items are (or resolve to) an
// object MUST declare either x-mcpmock-merge-key (merge by that key) or
// x-mcpmock-merge (replace). A future schema addition that adds an object-list
// without a directive fails here loudly, preventing the "we forgot how this list
// merges" failure mode ADR-008 warns about.
//
// It is wired into make schema-check via the config package's test run.
func TestSchema_EveryObjectArrayDeclaresMergeDirective(t *testing.T) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(EmbeddedSchemaBytes()))
	if err != nil {
		t.Fatalf("parse embedded schema: %v", err)
	}
	root, ok := doc.(map[string]any)
	if !ok {
		t.Fatalf("schema root is not an object")
	}

	var offenders []string
	lint := &mergeLint{root: root, offenders: &offenders}
	lint.walk(root, "#", map[string]bool{})

	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Fatalf("object-bearing array(s) missing a merge directive "+
			"(add x-mcpmock-merge-key or x-mcpmock-merge to each):\n  %v", offenders)
	}
}

// mergeLint walks a schema, recording every object-item array that lacks a merge
// directive.
type mergeLint struct {
	root      map[string]any
	offenders *[]string
}

func (l *mergeLint) walk(node map[string]any, loc string, seen map[string]bool) {
	if ref, ok := node["$ref"].(string); ok {
		if seen[ref] {
			return
		}
		target := resolveRef(l.root, ref)
		if target == nil {
			return
		}
		next := cloneSeen(seen)
		next[ref] = true
		l.walk(target, loc, next)
		return
	}

	if t, _ := node["type"].(string); t == "array" {
		l.checkArray(node, loc, seen)
	}
	if props, ok := node["properties"].(map[string]any); ok {
		for name, sub := range props {
			if subObj, isObj := sub.(map[string]any); isObj {
				l.walk(subObj, loc+"/properties/"+name, seen)
			}
		}
	}
	if ap, ok := node["additionalProperties"].(map[string]any); ok {
		l.walk(ap, loc+"/additionalProperties", seen)
	}
	for _, kw := range []string{"allOf", "anyOf", "oneOf"} {
		if branches, ok := node[kw].([]any); ok {
			for i, b := range branches {
				if bObj, isObj := b.(map[string]any); isObj {
					l.walk(bObj, loc+"/"+kw+"/"+itoa(i), seen)
				}
			}
		}
	}
	if then, ok := node["then"].(map[string]any); ok {
		l.walk(then, loc+"/then", seen)
	}
}

// checkArray verifies an array node: if its items resolve to an object schema it
// must carry a merge directive. Scalar-item arrays that carry a directive are
// fine (they declare replace); a scalar-item array without one is allowed by
// 703.5 (the requirement targets object lists), but the scenario schema declares
// replace on those too, which this test does not forbid.
func (l *mergeLint) checkArray(node map[string]any, loc string, seen map[string]bool) {
	items, ok := node["items"].(map[string]any)
	if !ok {
		return
	}
	if !l.itemsAreObject(items, seen) {
		// Still recurse into scalar items for completeness (they contain no
		// arrays in this schema, but the walk stays general).
		l.walk(items, loc+"/items", seen)
		return
	}
	if _, hasDirective := arrayDirectiveOf(node); !hasDirective {
		*l.offenders = append(*l.offenders, loc)
	}
	l.walk(items, loc+"/items", seen)
}

// itemsAreObject reports whether an items schema is (or via $ref resolves to) an
// object-typed schema.
func (l *mergeLint) itemsAreObject(items map[string]any, seen map[string]bool) bool {
	if ref, ok := items["$ref"].(string); ok {
		if seen[ref] {
			return false
		}
		target := resolveRef(l.root, ref)
		if target == nil {
			return false
		}
		next := cloneSeen(seen)
		next[ref] = true
		return l.itemsAreObject(target, next)
	}
	if t, ok := items["type"].(string); ok {
		return t == "object"
	}
	// An items schema with properties but no explicit type is object-shaped.
	_, hasProps := items["properties"]
	return hasProps
}

func cloneSeen(seen map[string]bool) map[string]bool {
	out := make(map[string]bool, len(seen)+1)
	for k, v := range seen {
		out[k] = v
	}
	return out
}

// itoa is a tiny int→string without importing strconv for one call site.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(b[pos:])
}
