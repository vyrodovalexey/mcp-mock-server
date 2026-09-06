package catalog

import (
	"encoding/json"

	"github.com/vyrodovalexey/mcp-mock-server/internal/determinism"
	"github.com/vyrodovalexey/mcp-mock-server/scenario"
)

// Kind names one of the four primitive families a catalog generates. Its
// string value matches the scenario schema's authored-item "kind" discriminator
// and the scenario.Catalog field it draws generation parameters from, so a Kind
// is the single key that ties a generation count, a name template and an
// overlay together.
type Kind string

// The four primitive kinds (scenario schema authoredItem.kind). The default for
// an authored item with no explicit kind is [KindTool], matching the schema.
const (
	// KindTool is a tools/list primitive (MOCK-221/222).
	KindTool Kind = "tool"
	// KindPrompt is a prompts/list primitive.
	KindPrompt Kind = "prompt"
	// KindResource is a resources/list primitive.
	KindResource Kind = "resource"
	// KindResourceTemplate is a resources/templates/list primitive.
	KindResourceTemplate Kind = "resourceTemplate"
)

// Kinds returns the four kinds in a fixed, deterministic order. Callers that
// enumerate every kind (construction, tests) use this rather than a
// hand-maintained literal so a future kind is added in exactly one place.
func Kinds() []Kind {
	return []Kind{KindTool, KindPrompt, KindResource, KindResourceTemplate}
}

// KindFromString maps an authored item's raw "kind" string to a [Kind]. An
// empty string maps to [KindTool] (the scenario schema default). An unrecognized
// value maps to [KindTool] as well and reports ok=false, so a caller can decide
// whether to treat it as a defaulted tool or reject it; this package defaults it
// defensively rather than panicking, because the schema already constrains the
// value before composition hands it here.
func KindFromString(s string) (Kind, bool) {
	switch Kind(s) {
	case KindTool:
		return KindTool, true
	case KindPrompt:
		return KindPrompt, true
	case KindResource:
		return KindResource, true
	case KindResourceTemplate:
		return KindResourceTemplate, true
	case "":
		return KindTool, true
	default:
		return KindTool, false
	}
}

// Item is one catalog primitive as DATA. It is transport- and wire-agnostic:
// internal/modern maps an Item to the appropriate wire descriptor
// (wire.ToolDescriptor and its siblings). Every field is either a plain scalar
// or a [json.RawMessage] carried verbatim; there are no typed sub-objects, so an
// authored, deliberately-malformed payload survives untouched (MOCK-222.3).
//
// An Item is a value returned fresh by [Catalog.At]; it shares no mutable
// state with the catalog. Its raw-JSON fields alias the immutable overlay's
// bytes for an authored item (never copied, never mutated) and are nil for a
// generated item except InputSchema, which a generated item carries as a small
// generated shape.
type Item struct {
	// Name is the primitive name, unique within its kind. For a generated item
	// it is the deterministic name from the kind's template; for an authored
	// item it is the authored name.
	Name string
	// Kind is the primitive family this item belongs to.
	Kind Kind
	// Authored reports whether this item came from the scenario overlay (true)
	// or was procedurally generated (false). It exists so callers and tests can
	// assert the overlay precedence rule without inspecting content.
	Authored bool
	// Title is the optional human-readable title, or "" when absent.
	Title string
	// Description is the optional description, or "" when absent.
	Description string
	// URI addresses a resource or resource template, or "" when absent (and
	// meaningless for a tool or prompt).
	URI string
	// InputSchema is emitted verbatim and never validated (MOCK-222.3). Nil when
	// absent.
	InputSchema json.RawMessage
	// OutputSchema is emitted verbatim and never validated. Nil when absent.
	OutputSchema json.RawMessage
	// Annotations is emitted verbatim and never validated. Nil when absent.
	Annotations json.RawMessage
	// Icons is emitted verbatim and never validated. Nil when absent.
	Icons json.RawMessage
	// RequiredScopes are the scopes a future MOCK-228 view predicate will filter
	// on. Carried, not applied, in Phase 1. Nil when absent.
	RequiredScopes []string
	// RequiredClientCapabilities are the client capabilities a future predicate
	// will filter on. Carried, not applied, in Phase 1. Nil when absent.
	RequiredClientCapabilities []string
}

// Catalog is an immutable, virtual catalog over all four kinds. It is a pure
// function of the instance key and the scenario catalog configuration:
// generated items are computed on demand and never stored, so a 5000-item
// catalog costs O(overlay) memory (ADR-004). It holds no process-global state
// and is safe for concurrent use.
//
// The exported identifier is spelled Catalog (US locale) so the spell checker
// passes without a suppression; scenario.Catalog resolves the identical Go-
// identifier conflict the same way. The two are distinct types in distinct
// packages: scenario.Catalog is configuration, this one is the resolved engine.
type Catalog struct {
	kinds map[Kind]*kindView
}

// New builds a [Catalog] from an instance key and the scenario catalog
// configuration. instanceKey is the per-instance determinism subtree
// (root.Derive(DomainInstance, name)); every generated name for this catalog
// derives from instanceKey.Derive(DomainCatalogue, …), so two instances with
// different keys generate different names from the same configuration
// (MOCK-103.5 mechanics).
//
// A nil cfg, or a cfg with no generated counts and no authored items, yields an
// empty-but-valid catalog whose every Len is 0. New allocates only for the
// authored overlay (O(overlay)); it does not touch the generated range, so its
// cost is independent of the configured counts.
func New(instanceKey determinism.Key, cfg *scenario.Catalog) *Catalog {
	c := &Catalog{kinds: make(map[Kind]*kindView, len(Kinds()))}
	for _, k := range Kinds() {
		c.kinds[k] = newKindView(instanceKey, k, generatedFor(cfg, k), authoredFor(cfg, k))
	}
	return c
}

// Len returns the number of items of the given kind: the generated count plus
// the number of authored items appended after the generated range (authored
// items that replace a generated index do not add to the length). An unknown
// kind returns 0.
func (c *Catalog) Len(kind Kind) int {
	v, ok := c.kinds[kind]
	if !ok {
		return 0
	}
	return v.length()
}

// At returns the item at index i within the given kind, computing a generated
// item on demand or returning an authored overlay item. i must be in [0,
// Len(kind)); [At] panics on an out-of-range index, matching a native slice
// index, because callers iterate over [0, Len) and an out-of-range access is a
// programming error, not a runtime condition. At allocates one [Item]; for a
// generated item that is one Derive plus the returned value, with no per-catalog
// storage touched (ADR-004).
func (c *Catalog) At(kind Kind, i int) Item {
	v, ok := c.kinds[kind]
	if !ok {
		panic("catalog: At on unknown kind " + string(kind))
	}
	return v.at(i)
}

// IndexOf returns the index of the item with the given name within the kind and
// whether such an item exists. It is the inverse of [At] restricted to names:
// for every i in [0, Len(kind)), IndexOf(kind, At(kind, i).Name) == (i, true).
// Lookup is O(log overlay) for the overlay plus O(1) to invert a generated
// name; it never scans the generated range.
func (c *Catalog) IndexOf(kind Kind, name string) (int, bool) {
	v, ok := c.kinds[kind]
	if !ok {
		return 0, false
	}
	return v.indexOf(name)
}

// generatedFor extracts the generation parameters for a kind from cfg, or a
// zero-count spec when cfg or the kind's block is absent.
func generatedFor(cfg *scenario.Catalog, kind Kind) *scenario.Generated {
	if cfg == nil {
		return nil
	}
	switch kind {
	case KindTool:
		return cfg.Tools
	case KindPrompt:
		return cfg.Prompts
	case KindResource:
		return cfg.Resources
	case KindResourceTemplate:
		return cfg.ResourceTemplates
	default:
		return nil
	}
}

// authoredFor returns the authored items belonging to a kind, defaulting a
// kind-less item to [KindTool] per the schema. It preserves the scenario order
// so a defensive last-wins duplicate resolution in the kind view is stable.
func authoredFor(cfg *scenario.Catalog, kind Kind) []scenario.AuthoredItem {
	if cfg == nil || len(cfg.Items) == 0 {
		return nil
	}
	var out []scenario.AuthoredItem
	for _, it := range cfg.Items {
		itemKind := KindTool
		if it.Kind != nil {
			itemKind, _ = KindFromString(*it.Kind)
		}
		if itemKind == kind {
			out = append(out, it)
		}
	}
	return out
}
