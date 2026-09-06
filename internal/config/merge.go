package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// This file implements the ADR-008 keyed strategic-merge algebra over decoded
// JSON trees (map[string]any / []any / scalars / nil), NOT over Go structs.
// Merging structs would collapse the three states the whole algebra rests on —
// absent (inherit), explicit null (delete) and zero value (set) — into two.
// Composition therefore merges the raw trees produced by yaml.YAMLToJSON and
// only decodes into scenario types after the final result is validated.
//
// # The merge table (ADR-008), one row per node type
//
//   - Object (map): recursive merge, later document's keys win; keys only in
//     the base survive.
//   - Scalar (string/number/bool): replace wholesale.
//   - Explicit JSON null in the overlay: DELETE the key from the result. This
//     is the row that distinguishes "turn a base's fault list off" from both
//     "inherit it" (absent) and "set it to []" (a zero-ish value). See the
//     null-delete note below.
//   - Array of objects carrying the schema's declared merge key: MERGE BY KEY.
//     Base order is preserved; overlay items with a new key are appended;
//     overlay items with an existing key are merged into the base item at its
//     original position. This is keyed, not positional, so inserting one
//     element into a base list never misaligns an overlay.
//   - Array of scalars, and array of objects the schema marks replace: REPLACE
//     wholesale.
//   - Any array: overridable per-node by the sibling directive
//     "$patch": "replace" | "merge" | "append", which takes precedence over the
//     schema directive (ADR-008 last table row).
//
// # The key used for each keyed list
//
// The key is NOT hard-coded per list here; it is read from the JSON Schema's
// x-mcpmock-merge-key annotation for the array at that location, so the merge
// behavior is discoverable from the published contract rather than buried in
// code (ADR-008). In the Phase 1 schema the declared keys are: instances→name,
// generate.vary→path, faults→id, catalogue.items→name, catalogue.drift→id,
// paging.perPage→page, mrtr.inputRequests→key, subscriptions.timers→id. Every
// other object-bearing array declares x-mcpmock-merge: replace; a schema-lint
// test (703.5) fails the build if a future array omits both.
//
// # null-delete vs absent vs zero (the TASK-006 precondition)
//
// Because merge runs over the raw JSON tree, the three states are physically
// distinct values in an overlay object:
//   - the key is missing        → absent  → the base value is inherited;
//   - the key maps to JSON null  → delete  → the key is removed from the result;
//   - the key maps to a zero-ish → set     → the base value is replaced by it
//     (0, false, "", [], {} are ordinary scalars/containers, not deletion).
//
// The typed scenario structs then decode this result with their pointer idiom
// (a non-nil pointer to the zero value records "explicitly set"), so the
// distinction survives all the way to the consumer. A deletion simply leaves
// the key absent, which decodes to a nil pointer (inherit-the-default), exactly
// as if the base had never set it.

// mergeNullSentinel is the JSON null literal. An overlay value equal to JSON
// null (decoded as a Go nil interface) means "delete this key".
//
// jsonNull is recognized structurally rather than by a sentinel type: after
// json.Unmarshal into any, a JSON null is the untyped nil interface, which is
// distinguishable from an absent key (the key is simply not in the map).

// patchDirectiveKey is the sibling key that overrides array merge behavior for
// one array node (ADR-008 last row). It is consumed during merge and never
// appears in the composed output.
const patchDirectiveKey = "$patch"

// patch directive values.
const (
	patchReplace = "replace"
	patchMerge   = "merge"
	patchAppend  = "append"
)

// mergeArrayMode is the resolved merge behavior for a single array node.
type mergeArrayMode struct {
	// keyed is true when the array merges by key; key names the object field.
	keyed bool
	key   string
	// replace is true when the array replaces wholesale.
	replace bool
	// append is true only via a $patch: append directive.
	appendMode bool
}

// mergeTrees merges overlay onto base and returns the composed tree. base is
// the lower-precedence (earlier) document; overlay wins on conflict. path is the
// instance JSON Pointer of the node being merged (""/root at the top), used to
// consult the schema directive resolver for array behavior. Neither input is
// mutated; the result shares no mutable structure with either input.
func mergeTrees(base, overlay any, path string, dir *mergeDirectives) any {
	// A null overlay deletes; at the top of a value merge it means "the caller
	// removed this whole subtree", which the object path handles before
	// recursing. Reaching here with a null overlay at the root replaces base
	// with nothing, i.e. an empty result — but the root is always an object in
	// practice, so this is defensive.
	baseObj, baseIsObj := base.(map[string]any)
	overlayObj, overlayIsObj := overlay.(map[string]any)
	if baseIsObj && overlayIsObj {
		return mergeObjects(baseObj, overlayObj, path, dir)
	}

	baseArr, baseIsArr := base.([]any)
	overlayArr, overlayIsArr := overlay.([]any)
	if baseIsArr && overlayIsArr {
		return mergeArrays(baseArr, overlayArr, path, dir)
	}

	// Mixed or scalar: the overlay replaces the base wholesale (scalar rule,
	// and the "types differ" case such as object-over-scalar).
	return cloneTree(overlay)
}

// mergeObjects recursively merges two JSON objects. A key present in the overlay
// with a JSON null value is deleted from the result; a key present with any
// other value is merged (recursively for containers, replaced for scalars); a
// key present only in the base is inherited unchanged.
func mergeObjects(base, overlay map[string]any, path string, dir *mergeDirectives) map[string]any {
	out := make(map[string]any, len(base)+len(overlay))
	for k, v := range base {
		out[k] = cloneTree(v)
	}
	for k, ov := range overlay {
		if ov == nil {
			// Explicit null deletes (ADR-008). Distinguished from absent because
			// the key is physically present in the overlay map.
			delete(out, k)
			continue
		}
		childPath := path + "/" + escapePointerToken(k)
		if bv, ok := out[k]; ok {
			out[k] = mergeTrees(bv, ov, childPath, dir)
		} else {
			out[k] = cloneTree(ov)
		}
	}
	return out
}

// mergeArrays merges two JSON arrays according to the array's resolved mode:
// keyed merge, wholesale replace, or (via $patch: append) concatenation. The
// mode comes from the $patch sibling directive if present, else from the schema
// annotation for this path, else defaults to replace (the safe default that
// never silently misaligns).
func mergeArrays(base, overlay []any, path string, dir *mergeDirectives) []any {
	mode := resolveArrayMode(overlay, path, dir)
	switch {
	case mode.appendMode:
		out := make([]any, 0, len(base)+len(overlay))
		out = append(out, cloneSlice(base)...)
		out = append(out, cloneSlice(stripPatchDirectives(overlay))...)
		return out
	case mode.keyed:
		return mergeKeyedArray(base, stripPatchDirectives(overlay), mode.key, path, dir)
	default:
		// replace
		return cloneSlice(stripPatchDirectives(overlay))
	}
}

// mergeKeyedArray merges overlay onto base by the object field named key. Base
// order is preserved; an overlay item whose key matches a base item is merged
// into that item at its original index; an overlay item with a new key is
// appended in overlay order. An overlay item that lacks the key, or is not an
// object, is appended verbatim (the schema forbids this shape, but merge must
// not panic on a tree that has not yet been validated — validation runs after
// composition, 703.8).
func mergeKeyedArray(base, overlay []any, key, path string, dir *mergeDirectives) []any {
	out := cloneSlice(base)
	// index maps a key value to its position in out. Only string-valued keys
	// participate; every declared merge key in the schema is a string.
	index := make(map[string]int, len(out))
	for i, item := range out {
		if kv, ok := objectKey(item, key); ok {
			index[kv] = i
		}
	}
	itemPath := path + "/*"
	for _, item := range overlay {
		kv, ok := objectKey(item, key)
		if !ok {
			out = append(out, cloneTree(item))
			continue
		}
		if pos, found := index[kv]; found {
			out[pos] = mergeTrees(out[pos], item, itemPath, dir)
			continue
		}
		index[kv] = len(out)
		out = append(out, cloneTree(item))
	}
	return out
}

// resolveArrayMode determines how an array node merges. A $patch sibling in the
// overlay's first element wins; ADR-008 places $patch at the array level, and
// yaml/json arrays have no place for a sibling except inside their elements, so
// the directive is read from an element carrying only "$patch" (a common
// strategic-merge convention) OR from a directive element removed before merge.
// Absent a $patch directive, the schema annotation for path decides; absent
// that, the array replaces.
func resolveArrayMode(overlay []any, path string, dir *mergeDirectives) mergeArrayMode {
	if p, ok := patchDirectiveOf(overlay); ok {
		switch p {
		case patchReplace:
			return mergeArrayMode{replace: true}
		case patchAppend:
			return mergeArrayMode{appendMode: true}
		case patchMerge:
			if key, keyed := dir.arrayKey(path); keyed {
				return mergeArrayMode{keyed: true, key: key}
			}
			// $patch: merge on a non-keyed array degrades to append, the only
			// element-wise "merge" meaning for a keyless list.
			return mergeArrayMode{appendMode: true}
		}
	}
	if key, keyed := dir.arrayKey(path); keyed {
		return mergeArrayMode{keyed: true, key: key}
	}
	return mergeArrayMode{replace: true}
}

// patchDirectiveOf returns the $patch directive value carried by a directive-
// only element of the array, if any. A directive element is an object with
// exactly the single key "$patch"; this is how a per-array override is encoded
// in a pure JSON/YAML array (ADR-008).
func patchDirectiveOf(arr []any) (string, bool) {
	for _, el := range arr {
		obj, ok := el.(map[string]any)
		if !ok || len(obj) != 1 {
			continue
		}
		if v, has := obj[patchDirectiveKey]; has {
			if s, isStr := v.(string); isStr {
				return s, true
			}
		}
	}
	return "", false
}

// stripPatchDirectives returns arr without any directive-only ($patch) element,
// so the directive never leaks into the composed output.
func stripPatchDirectives(arr []any) []any {
	out := make([]any, 0, len(arr))
	for _, el := range arr {
		if obj, ok := el.(map[string]any); ok && len(obj) == 1 {
			if _, has := obj[patchDirectiveKey]; has {
				continue
			}
		}
		out = append(out, el)
	}
	return out
}

// objectKey returns the string value of field key in item when item is an
// object carrying that field as a string, reporting success.
func objectKey(item any, key string) (string, bool) {
	obj, ok := item.(map[string]any)
	if !ok {
		return "", false
	}
	v, has := obj[key]
	if !has {
		return "", false
	}
	s, isStr := v.(string)
	return s, isStr
}

// cloneTree deep-copies a decoded JSON value so the composed result shares no
// mutable map or slice with either input document. Scalars are returned as-is
// (they are immutable), objects and arrays are cloned recursively.
func cloneTree(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, cv := range t {
			out[k] = cloneTree(cv)
		}
		return out
	case []any:
		return cloneSlice(t)
	default:
		return v
	}
}

// cloneSlice deep-copies a JSON array.
func cloneSlice(s []any) []any {
	out := make([]any, len(s))
	for i, v := range s {
		out[i] = cloneTree(v)
	}
	return out
}

// escapePointerToken escapes a map key for use as a JSON Pointer reference token
// (RFC 6901): "~" -> "~0", "/" -> "~1". Order matters: "~" first.
func escapePointerToken(tok string) string {
	if !bytes.ContainsAny([]byte(tok), "~/") {
		return tok
	}
	var b bytes.Buffer
	for _, r := range tok {
		switch r {
		case '~':
			b.WriteString("~0")
		case '/':
			b.WriteString("~1")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// canonicalJSON re-encodes a decoded JSON tree to bytes with encoding/json,
// which sorts object keys lexically. Because the composed tree is built from
// plain map[string]any, re-encoding is a pure function of the tree's content
// and independent of Go map iteration order — this is what makes the composed
// document byte-identical across runs and across GOMAXPROCS settings
// (ADR-008 §0.1 determinism). It is used both to hand a canonical spec to the
// scenario decoder and, in tests, to prove determinism.
func canonicalJSON(tree any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(tree); err != nil {
		return nil, fmt.Errorf("config: canonical encode composed document: %w", err)
	}
	// Encoder.Encode appends a trailing newline; trim it so the bytes are a
	// bare JSON value suitable for hashing and re-decoding.
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// mergeDirectives resolves, for an instance JSON Pointer path, whether the array
// at that path merges by key (and which key) or replaces, by reading the
// x-mcpmock-merge-key / x-mcpmock-merge annotations from the compiled schema
// source. It is built once from the embedded schema and is immutable and safe
// for concurrent use thereafter.
type mergeDirectives struct {
	// byPattern maps a schema instance-path pattern (segments separated by "/",
	// with "*" matching any single array-index or additionalProperties segment)
	// to the array merge directive discovered at that location.
	byPattern map[string]arrayDirective
}

// arrayDirective is one array node's declared merge behavior.
type arrayDirective struct {
	key     string // non-empty ⇒ merge by this key
	replace bool   // true ⇒ replace wholesale
}

// arrayKey reports the merge key for the array at instance path, and whether the
// array is keyed. A path with no matching directive, or a directive that says
// replace, returns keyed=false so the caller replaces — the safe default.
func (d *mergeDirectives) arrayKey(path string) (string, bool) {
	if d == nil {
		return "", false
	}
	if dir, ok := d.byPattern[path]; ok && dir.key != "" {
		return dir.key, true
	}
	return "", false
}

// directivesOnce memoizes the merge-directive table built from the embedded
// schema, mirroring the compile-once discipline of scenarioSchema so
// composition adds nothing to the per-load startup budget (MOCK-107).
var directivesOnce = sync.OnceValues(func() (*mergeDirectives, error) {
	return buildMergeDirectives(embeddedSchema)
})

// mergeDirectivesFor returns the process-wide merge-directive table.
func mergeDirectivesFor() (*mergeDirectives, error) {
	return directivesOnce()
}

// buildMergeDirectives parses the schema JSON and walks it, recording the array
// merge directive for every array node it can reach, keyed by the instance path
// pattern that reaches it. It follows properties, items, additionalProperties,
// $ref (intra-document only), and the allOf/if-then branches the scenario schema
// uses to bind kind→spec. The walk is bounded by a visited-$ref set so a
// self-referential schema cannot loop.
func buildMergeDirectives(schemaBytes []byte) (*mergeDirectives, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaBytes))
	if err != nil {
		return nil, fmt.Errorf("config: parse schema for merge directives: %w", err)
	}
	root, ok := doc.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("config: schema root is not an object")
	}
	w := &directiveWalk{root: root, out: make(map[string]arrayDirective)}
	w.walk(root, "", map[string]bool{})
	return &mergeDirectives{byPattern: w.out}, nil
}

// directiveWalk carries the walk state: the schema root (for $ref resolution)
// and the accumulating pattern→directive table.
type directiveWalk struct {
	root map[string]any
	out  map[string]arrayDirective
}

// walk descends a schema node, emitting array directives at every array node it
// finds and recursing into properties/items/additionalProperties/$ref/allOf.
// path is the instance path pattern that reaches this schema node. seen guards
// against $ref cycles within a single descent.
func (w *directiveWalk) walk(node map[string]any, path string, seen map[string]bool) {
	if ref, ok := node["$ref"].(string); ok {
		w.walkRef(ref, path, seen)
		return
	}
	w.walkArray(node, path, seen)
	w.walkObject(node, path, seen)
	w.walkCombinators(node, path, seen)
}

// walkRef resolves an intra-document "#/$defs/..." reference and continues the
// walk at the referenced node, unless this ref is already on the current
// descent path (a cycle), in which case it stops.
func (w *directiveWalk) walkRef(ref, path string, seen map[string]bool) {
	if seen[ref] {
		return
	}
	target := resolveRef(w.root, ref)
	if target == nil {
		return
	}
	next := make(map[string]bool, len(seen)+1)
	for k, v := range seen {
		next[k] = v
	}
	next[ref] = true
	w.walk(target, path, next)
}

// walkArray records the directive for an array node and recurses into its item
// schema under a "*" path segment (one element position).
func (w *directiveWalk) walkArray(node map[string]any, path string, seen map[string]bool) {
	if t, _ := node["type"].(string); t != "array" {
		return
	}
	if dir, ok := arrayDirectiveOf(node); ok {
		w.out[path] = dir
	}
	if items, ok := node["items"].(map[string]any); ok {
		w.walk(items, path+"/*", seen)
	}
}

// walkObject recurses into an object node's declared properties and its
// additionalProperties schema (the latter reached in the instance via a "*"
// segment, matching any dynamic key such as switches.methods.<name>).
func (w *directiveWalk) walkObject(node map[string]any, path string, seen map[string]bool) {
	if props, ok := node["properties"].(map[string]any); ok {
		for name, sub := range props {
			if subObj, isObj := sub.(map[string]any); isObj {
				w.walk(subObj, path+"/"+escapePointerToken(name), seen)
			}
		}
	}
	if ap, ok := node["additionalProperties"].(map[string]any); ok {
		w.walk(ap, path+"/*", seen)
	}
}

// walkCombinators recurses into the allOf / then branches the scenario schema
// uses (kind→spec binding). Only allOf and the then of an if/then pair carry
// property definitions in this schema; anyOf/oneOf here gate scalar shapes and
// carry no arrays, but are walked defensively.
func (w *directiveWalk) walkCombinators(node map[string]any, path string, seen map[string]bool) {
	for _, kw := range []string{"allOf", "anyOf", "oneOf"} {
		if branches, ok := node[kw].([]any); ok {
			for _, b := range branches {
				if bObj, isObj := b.(map[string]any); isObj {
					w.walkBranch(bObj, path, seen)
				}
			}
		}
	}
	if then, ok := node["then"].(map[string]any); ok {
		w.walk(then, path, seen)
	}
}

// walkBranch walks a single combinator branch, descending into its own then
// clause (an if/then inside an allOf entry, as the scenario schema uses for the
// kind→spec and action-kind bindings).
func (w *directiveWalk) walkBranch(branch map[string]any, path string, seen map[string]bool) {
	w.walk(branch, path, seen)
}

// arrayDirectiveOf reads the x-mcpmock-merge-key / x-mcpmock-merge annotations
// from an array schema node. A node with x-mcpmock-merge-key merges by that key;
// a node with x-mcpmock-merge: "replace" replaces; a node with neither returns
// ok=false, which the 703.5 schema-lint test forbids for object-bearing arrays.
func arrayDirectiveOf(node map[string]any) (arrayDirective, bool) {
	if key, ok := node["x-mcpmock-merge-key"].(string); ok && key != "" {
		return arrayDirective{key: key}, true
	}
	if mode, ok := node["x-mcpmock-merge"].(string); ok {
		return arrayDirective{replace: mode == patchReplace}, true
	}
	return arrayDirective{}, false
}

// resolveRef resolves an intra-document JSON reference of the form
// "#/$defs/name" against the schema root, returning the referenced object or
// nil if it cannot be resolved. Only the "#/$defs/..." shape the scenario schema
// uses is supported; external refs are not (the schema has none).
func resolveRef(root map[string]any, ref string) map[string]any {
	const prefix = "#/$defs/"
	if len(ref) <= len(prefix) || ref[:len(prefix)] != prefix {
		return nil
	}
	defs, ok := root["$defs"].(map[string]any)
	if !ok {
		return nil
	}
	name := ref[len(prefix):]
	target, ok := defs[name].(map[string]any)
	if !ok {
		return nil
	}
	return target
}
