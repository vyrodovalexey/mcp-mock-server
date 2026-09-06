package mcpmock

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path/filepath"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/vyrodovalexey/mcp-mock-server/internal/config"
	"github.com/vyrodovalexey/mcp-mock-server/internal/instance"
	"github.com/vyrodovalexey/mcp-mock-server/internal/transport/httpx"
	"github.com/vyrodovalexey/mcp-mock-server/scenario"
)

// build.go holds the pure construction helpers the facade uses: turning a
// scenario document into instance specs, wrapping the loader's validation error
// under the facade's sentinel, and resolving URLs and mount paths. None of them
// touch process-global state or perform lifecycle work.

// defaultScenarioName is the instance name used when a document carries no
// metadata name. It keeps a bare New() Server usable without a scenario file.
const defaultScenarioName = "default"

// defaultScenarioDocument returns the empty single-instance scenario a bare
// New() builds from: one instance, default mount path, no catalogue, journal at
// the facade default. It is a valid document that composes and validates.
func defaultScenarioDocument() scenario.Document {
	return scenario.Document{
		APIVersion: scenario.APIVersion,
		Kind:       scenario.KindScenario,
		Metadata:   &scenario.Metadata{Name: defaultScenarioName},
		// An empty spec object satisfies the schema (instanceSpec has no
		// required fields) and yields an all-defaults single instance.
		Spec: json.RawMessage(`{}`),
	}
}

// instanceSpecs expands a validated document into the per-instance specs the
// Server builds. A Scenario yields exactly one instance (Phase 1 single-instance
// lifecycle); a Fleet yields one per listed instance (MOCK-103), each with its
// name, mount path and inline spec overlay. Fleet generate blocks are a later
// phase and are ignored here.
func instanceSpecs(doc *scenario.Document) ([]instanceSpec, error) {
	switch doc.Kind {
	case scenario.KindFleet:
		return fleetInstanceSpecs(doc)
	case scenario.KindScenario, "":
		return scenarioInstanceSpec(doc)
	default:
		return nil, fmt.Errorf("%w: unknown document kind %q", ErrValidation, doc.Kind)
	}
}

// scenarioInstanceSpec decodes a single-instance Scenario document into one
// spec, naming it from metadata and mounting it at its configured HTTP path (or
// the default).
func scenarioInstanceSpec(doc *scenario.Document) ([]instanceSpec, error) {
	spec, err := doc.ScenarioSpec()
	if err != nil {
		return nil, fmt.Errorf("%w: decode scenario spec: %w", ErrValidation, err)
	}
	name := documentName(doc)
	return []instanceSpec{{
		name:      name,
		mountPath: mountPathFromSpec(spec, name),
		spec:      spec,
	}}, nil
}

// fleetInstanceSpecs decodes a Fleet document into one spec per listed instance.
// Each instance's inline spec overlay (FleetInstance.Spec) is the instance's
// configuration; the fleet defaults block is a later-phase merge and is applied
// by composition, not here.
func fleetInstanceSpecs(doc *scenario.Document) ([]instanceSpec, error) {
	fleet, err := doc.FleetSpec()
	if err != nil {
		return nil, fmt.Errorf("%w: decode fleet spec: %w", ErrValidation, err)
	}
	if len(fleet.Instances) == 0 {
		return nil, fmt.Errorf("%w: fleet has no instances", ErrValidation)
	}
	out := make([]instanceSpec, 0, len(fleet.Instances))
	for _, fi := range fleet.Instances {
		var spec scenario.InstanceSpec
		if fi.Spec != nil {
			spec = *fi.Spec
		}
		mount := fi.MountPath
		if mount == "" {
			mount = mountPathFromSpec(spec, fi.Name)
		}
		out = append(out, instanceSpec{name: fi.Name, mountPath: mount, spec: spec})
	}
	return out, nil
}

// documentName returns the document's metadata name, or the default name when
// metadata is absent.
func documentName(doc *scenario.Document) string {
	if doc.Metadata != nil && doc.Metadata.Name != "" {
		return doc.Metadata.Name
	}
	return defaultScenarioName
}

// mountPathFromSpec returns the instance's HTTP mount path: the scenario's
// configured transport.http.path when present, else the default "/mcp". The
// instance name is not folded into the path here — the scenario controls the
// path, and a fleet gives distinct paths per instance (MOCK-103).
func mountPathFromSpec(spec scenario.InstanceSpec, _ string) string {
	if spec.Transport != nil && spec.Transport.HTTP != nil && spec.Transport.HTTP.Path != nil {
		if p := *spec.Transport.HTTP.Path; p != "" {
			return p
		}
	}
	return httpx.DefaultPath
}

// mountPathFor returns the mount path an already-constructed instance is
// registered under (its registry key).
func mountPathFor(in *instance.Instance) string { return in.MountPath() }

// httpBaseURL renders the scheme+host+port prefix a client dials, from the
// listener's resolved address. Phase 1 is plaintext HTTP (TLS is MOCK-106, a
// later phase), so the scheme is always http.
func httpBaseURL(addr net.Addr) string {
	return "http://" + addr.String()
}

// scenarioRootFor resolves the extends sandbox for a file load: the explicit
// WithScenarioRoot when set, else the scenario file's own directory. Bounding to
// the file's directory by default keeps a bare NewFromFile safe (an extends
// target cannot escape the scenario's directory) without forcing every caller to
// pass a root.
func scenarioRootFor(root, path string) (*config.ScenarioRoot, error) {
	if root == "" {
		root = filepath.Dir(path)
	}
	sr, err := config.NewScenarioRoot(root)
	if err != nil {
		return nil, fmt.Errorf("mcpmock: scenario root: %w", err)
	}
	return sr, nil
}

// validateDoc validates an in-memory document by marshaling it to JSON and
// running the same embedded schema + semantic rules the loader uses, so a
// programmatically-built scenario is held to the identical contract as a file.
func validateDoc(doc *scenario.Document) error {
	raw, err := marshalDoc(doc)
	if err != nil {
		return err
	}
	tree, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("%w: %w", ErrValidation, err)
	}
	if verr := config.Validate(tree); verr != nil {
		verr.Source = "<scenario>"
		return wrapValidation(verr)
	}
	return nil
}

// marshalDoc renders a scenario document to JSON for validation or composition.
func marshalDoc(doc *scenario.Document) ([]byte, error) {
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("mcpmock: marshal scenario: %w", err)
	}
	return raw, nil
}

// wrapValidation maps the internal loader's validation error onto the facade's
// [ErrValidation] sentinel while preserving the original for errors.As, so a
// caller can both branch with errors.Is(err, ErrValidation) and recover the
// JSON Pointer / file / line via errors.As to *config.ValidationError. A
// non-validation error (an I/O failure) passes through unchanged so exit-code
// distinction survives.
func wrapValidation(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, config.ErrValidation) {
		return &validationError{err: err}
	}
	return err
}

// validationError adapts an internal config validation error to the facade's
// ErrValidation sentinel. It wraps both, so errors.Is matches ErrValidation and
// errors.As still reaches the underlying *config.ValidationError detail.
type validationError struct {
	err error
}

// Error renders the underlying loader message unchanged.
func (e *validationError) Error() string { return e.err.Error() }

// Unwrap exposes the facade sentinel first, then the loader chain, so
// errors.Is(err, ErrValidation) and errors.As(err, &*config.ValidationError)
// both succeed.
func (e *validationError) Unwrap() []error { return []error{ErrValidation, e.err} }
