package catalog

import (
	"sort"

	"github.com/vyrodovalexey/mcp-mock-server/internal/determinism"
	"github.com/vyrodovalexey/mcp-mock-server/scenario"
)

// kindView resolves the three-way model for one kind: the generated range, the
// authored items that REPLACE a generated index by name, and the authored items
// that APPEND after the generated range. It is immutable after construction and
// safe for concurrent use.
//
// Index space:
//
//	[0, count)              generated, unless shadowed by a replacement at that i
//	[count, count+extra)    appended authored items, sorted by name
//
// A replacement authored item occupies the SAME index as the generated item it
// shadows, so it does not extend the length; an appended authored item occupies
// a new index. This is what makes IndexOf(At(i)) == i hold for every i.
type kindView struct {
	gen *generator
	// replace maps a generated index to the authored item that shadows it.
	replace map[int]Item
	// append is the sorted-by-name slice of authored items with no generated
	// counterpart; index count+j is appendItems[j].
	appendItems []Item
	// byName indexes every authored name (replacement or appended) to its final
	// catalog index, so IndexOf resolves an authored name in O(log n) without
	// consulting the generator.
	byName map[string]int
}

// newKindView builds the resolved view for one kind. It is O(overlay): it never
// enumerates the generated range. Authored names are matched against the
// generated name mapping by INVERSION (nameIndex), not by generating and
// scanning, so a 5000-item generated range with a handful of overlays stays
// cheap.
func newKindView(
	instanceKey determinism.Key,
	kind Kind,
	g *scenario.Generated,
	authored []scenario.AuthoredItem,
) *kindView {
	spec := resolveGenerated(kind, g)
	gen := newGenerator(instanceKey, kind, spec)

	v := &kindView{
		gen:     gen,
		replace: make(map[int]Item),
		byName:  make(map[string]int),
	}

	// Deduplicate authored items by name defensively (schema+merge already
	// enforce uniqueness); last occurrence wins, matching merge-by-name.
	deduped := dedupeByName(authored)

	var appends []Item
	for _, a := range deduped {
		item := authoredItem(kind, a)
		if idx, ok := gen.nameIndex(a.Name); ok {
			// REPLACE: authored name collides with generated name at idx.
			v.replace[idx] = item
			v.byName[a.Name] = idx
			continue
		}
		// APPEND: authored name is not a generated name.
		appends = append(appends, item)
	}

	// Sort appended items by name so their indices are deterministic and
	// independent of scenario declaration order.
	sort.Slice(appends, func(i, j int) bool { return appends[i].Name < appends[j].Name })
	v.appendItems = appends
	for j, item := range appends {
		v.byName[item.Name] = gen.count + j
	}
	return v
}

// length is count + number of appended authored items. Replacements do not add
// to the length (they occupy an existing generated index).
func (v *kindView) length() int {
	return v.gen.count + len(v.appendItems)
}

// at returns the item at index i, panicking on an out-of-range index.
func (v *kindView) at(i int) Item {
	if i < 0 || i >= v.length() {
		panic("catalog: index out of range")
	}
	if i < v.gen.count {
		if item, ok := v.replace[i]; ok {
			return item
		}
		return v.gen.generate(i)
	}
	return v.appendItems[i-v.gen.count]
}

// indexOf returns the index of a name and whether it exists. An authored name
// (replacement or appended) is found in byName; otherwise the name is inverted
// against the generator, but only accepted if that generated index is not
// shadowed by a replacement under a DIFFERENT name — which cannot happen,
// because a replacement shares the generated name, so a shadowed index's
// generated name is exactly the replacement's name and is already in byName.
func (v *kindView) indexOf(name string) (int, bool) {
	if idx, ok := v.byName[name]; ok {
		return idx, true
	}
	if idx, ok := v.gen.nameIndex(name); ok {
		// A generated name whose index was replaced would appear in byName
		// (same name), so reaching here means the index is a live generated
		// item.
		return idx, true
	}
	return 0, false
}

// authoredItem converts a scenario authored item into a catalog [Item],
// carrying its verbatim raw-JSON payloads untouched (MOCK-222.3) and
// dereferencing its optional scalar pointers to their zero value when absent.
func authoredItem(kind Kind, a scenario.AuthoredItem) Item {
	item := Item{
		Name:                       a.Name,
		Kind:                       kind,
		Authored:                   true,
		InputSchema:                a.InputSchema,
		OutputSchema:               a.OutputSchema,
		Annotations:                a.Annotations,
		Icons:                      a.Icons,
		RequiredScopes:             a.RequiredScopes,
		RequiredClientCapabilities: a.RequiredClientCapabilities,
	}
	if a.Title != nil {
		item.Title = *a.Title
	}
	if a.Description != nil {
		item.Description = *a.Description
	}
	if a.URI != nil {
		item.URI = *a.URI
	}
	return item
}

// dedupeByName returns the authored items with duplicate names collapsed to
// their last occurrence, preserving the order of last occurrences. It is a
// defensive guard: the schema and TASK-007 merge-by-name already guarantee
// uniqueness, but a duplicate must never produce two entries for one name.
func dedupeByName(items []scenario.AuthoredItem) []scenario.AuthoredItem {
	if len(items) < 2 {
		return items
	}
	lastAt := make(map[string]int, len(items))
	for i, it := range items {
		lastAt[it.Name] = i
	}
	out := make([]scenario.AuthoredItem, 0, len(items))
	for i, it := range items {
		if lastAt[it.Name] == i {
			out = append(out, it)
		}
	}
	return out
}
