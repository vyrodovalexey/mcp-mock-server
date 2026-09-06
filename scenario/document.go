package scenario

import "encoding/json"

// APIVersion is the only scenario document apiVersion this release understands
// (ADR-008 document shape). The v1alpha1 suffix marks the format as explicitly
// unstable pending GAP-003 ratification; see the package doc's version policy.
const APIVersion = "mcpmock.dev/v1alpha1"

// Document kinds. A Scenario is a single mock instance's configuration; a Fleet
// wraps N instances (MOCK-103 / MOCK-703). Both are carried by [Document].
const (
	// KindScenario is a single-instance scenario document.
	KindScenario = "Scenario"
	// KindFleet is a multi-instance fleet document (MOCK-103 / MOCK-904).
	KindFleet = "Fleet"
)

// Document is the top level of a scenario file: apiVersion, kind, metadata and
// spec (ADR-008). It is the type a loaded, schema-valid YAML or JSON document
// decodes into.
//
// The document is versioned by [Document.APIVersion] and [Document.Kind]; the
// loader rejects any apiVersion other than [APIVersion] and any kind other than
// [KindScenario] or [KindFleet] before this type is meaningfully consumed.
//
// Spec is a [json.RawMessage] rather than a decoded struct because its shape
// depends on Kind (a Scenario's spec is an [InstanceSpec]; a Fleet's spec is a
// [FleetSpec]). Keeping it raw lets the loader validate the whole document
// against the JSON Schema first — which enforces the kind→spec correspondence
// via the schema's allOf/if-then — and lets a caller decode the spec into the
// correct type afterward with [Document.ScenarioSpec] or [Document.FleetSpec].
// It also preserves the spec bytes verbatim for composition (TASK-007), which
// merges decoded JSON trees, not Go structs.
type Document struct {
	// APIVersion identifies the document schema version. It must equal
	// [APIVersion]; the loader rejects any other value (MOCK-701.2).
	APIVersion string `json:"apiVersion"`
	// Kind selects the document shape: [KindScenario] or [KindFleet].
	Kind string `json:"kind"`
	// Extends names documents merged left to right before this one during
	// composition (TASK-007). It is nil when absent. Paths are relative to the
	// including file and are sandboxed within --scenario-root by the composer.
	Extends []string `json:"extends,omitempty"`
	// Metadata carries the document's name and descriptive labels. It is a
	// pointer so an overlay that omits metadata entirely is distinguishable
	// from one that supplies an empty metadata block (the absent-vs-zero idiom;
	// see the package doc).
	Metadata *Metadata `json:"metadata,omitempty"`
	// Spec is the kind-dependent body, preserved as raw JSON. Decode it with
	// [Document.ScenarioSpec] or [Document.FleetSpec] according to Kind.
	Spec json.RawMessage `json:"spec,omitempty"`
}

// ScenarioSpec decodes the document's spec as an [InstanceSpec]. It is valid
// only when Kind is [KindScenario]; callers should check Kind first. A nil Spec
// decodes to the zero InstanceSpec (all fields absent).
func (d *Document) ScenarioSpec() (InstanceSpec, error) {
	var s InstanceSpec
	if len(d.Spec) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(d.Spec, &s); err != nil {
		return s, err
	}
	return s, nil
}

// FleetSpec decodes the document's spec as a [FleetSpec]. It is valid only when
// Kind is [KindFleet]; callers should check Kind first.
func (d *Document) FleetSpec() (FleetSpec, error) {
	var s FleetSpec
	if len(d.Spec) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(d.Spec, &s); err != nil {
		return s, err
	}
	return s, nil
}

// Metadata is the document's identifying block (schema #/$defs/metadata). Name
// is required by the schema; the other fields are optional and left at their
// zero value when absent.
type Metadata struct {
	// Name is the scenario or fleet name. The schema constrains it to
	// ^[a-zA-Z0-9][a-zA-Z0-9._-]{0,62}$.
	Name string `json:"name"`
	// Description is free-form documentation. Empty when absent.
	Description string `json:"description,omitempty"`
	// Labels is an arbitrary string→string map for grouping. Nil when absent.
	Labels map[string]string `json:"labels,omitempty"`
	// CoversRequirements lists MOCK-nnn ids this scenario documents; used only
	// to generate the scenario/requirement map (MOCK-705.3). Nil when absent.
	CoversRequirements []string `json:"coversRequirements,omitempty"`
}

// FleetSpec is the body of a [KindFleet] document (schema #/$defs/fleetSpec):
// N instances defined explicitly or generated. Phase 1 models only the fields
// needed to load and re-encode a fleet document without loss; per-instance
// generation logic (MOCK-904) is a later phase. Every field is optional and
// nil/absent when omitted, preserving the absent-vs-zero distinction that
// composition (TASK-007) requires.
type FleetSpec struct {
	// Defaults is the base instanceRef applied to every instance. Nil when
	// absent.
	Defaults *InstanceRef `json:"defaults,omitempty"`
	// Instances lists explicitly-named instances. Nil when absent. This is a
	// merge-by-name list under composition (schema x-mcpmock-merge-key: name).
	Instances []FleetInstance `json:"instances,omitempty"`
	// Process carries process-wide settings meaningful only on the top-level
	// document. Nil when absent. Preserved verbatim so later phases can decode
	// the full process block without this package foreclosing its shape.
	Process json.RawMessage `json:"process,omitempty"`
	// Generate defines instances programmatically (MOCK-904). Preserved
	// verbatim in Phase 1; nil when absent.
	Generate json.RawMessage `json:"generate,omitempty"`
}

// InstanceRef is a reference to an instance configuration, either by extends
// overlays or an inline spec (schema #/$defs/instanceRef).
type InstanceRef struct {
	// Extends names documents merged to form this instance. Nil when absent.
	Extends []string `json:"extends,omitempty"`
	// Spec is an inline [InstanceSpec] overlay, highest precedence. Nil when
	// absent.
	Spec *InstanceSpec `json:"spec,omitempty"`
}

// FleetInstance is one entry in a fleet's instances list (schema
// #/$defs/fleetSpec/instances/items).
type FleetInstance struct {
	// Name is the instance name; required by the schema and the merge key.
	Name string `json:"name"`
	// MountPath is the HTTP mount path for this instance. Empty when absent.
	MountPath string `json:"mountPath,omitempty"`
	// Extends names documents merged to form this instance. Nil when absent.
	Extends []string `json:"extends,omitempty"`
	// Spec is an inline [InstanceSpec] overlay. Nil when absent.
	Spec *InstanceSpec `json:"spec,omitempty"`
}
