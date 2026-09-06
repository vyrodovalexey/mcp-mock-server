package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"sigs.k8s.io/yaml"

	"github.com/vyrodovalexey/mcp-mock-server/scenario"
)

// ErrValidation is the sentinel wrapped by every [ValidationError], so a caller
// (mcpmock validate, TASK-024) can branch on validation failure with
// errors.Is without depending on the concrete type. A validation failure maps
// to exit code 1; an I/O failure (a plain os error) maps to exit code 2
// (MOCK-701.5).
var ErrValidation = errors.New("scenario validation failed")

// Problem is one actionable validation failure: where it occurred (a JSON
// Pointer into the document, RFC 6901) and what was wrong. It is the unit the
// --validate --output json mode (TASK-024) serializes.
type Problem struct {
	// Pointer is the JSON Pointer to the offending value, e.g.
	// "/spec/faults/0/id" for an unknown key or "" for a document-root
	// problem. It is empty (the document root) when the failure is not
	// localized to a child value.
	Pointer string `json:"pointer"`
	// Message is a human-readable description of what was expected, e.g.
	// "additional properties 'toolz' not allowed" or
	// "value must be 'mcpmock.dev/v1alpha1'".
	Message string `json:"message"`
}

// String renders the problem as "<pointer>: <message>", using "(document root)"
// for the empty pointer so a message is never left dangling on a bare colon.
func (p Problem) String() string {
	loc := p.Pointer
	if loc == "" {
		loc = "(document root)"
	}
	return loc + ": " + p.Message
}

// ValidationError reports that a scenario document is invalid. It names the
// source (a file path, or "<input>" for an in-memory load) and carries every
// problem found, each with a JSON Pointer. It wraps [ErrValidation].
//
// The error message is deliberately actionable: it leads with the source and
// the first problem, and appends the rest, so a user pasting the message into
// an issue sees which file, which JSON path, and what was expected — never a
// bare "validation failed" (ADR-008, MOCK-701.5).
type ValidationError struct {
	// Source is the file path the document came from, or "<input>" for a
	// []byte load.
	Source string
	// Problems is the non-empty list of failures, each with a JSON Pointer.
	Problems []Problem
}

// Error renders all problems, source-qualified, one per line after a summary.
func (e *ValidationError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s: %d problem", e.Source, ErrValidation.Error(), len(e.Problems))
	if len(e.Problems) != 1 {
		b.WriteByte('s')
	}
	for _, p := range e.Problems {
		b.WriteString("\n  - ")
		b.WriteString(p.String())
	}
	return b.String()
}

// Unwrap ties ValidationError to the ErrValidation sentinel for errors.Is.
func (e *ValidationError) Unwrap() error { return ErrValidation }

// LoadFile reads, YAML/JSON-decodes and schema-validates the scenario document
// at path. On success it returns the decoded [scenario.Document].
//
// An I/O failure (path missing, unreadable) is returned as a plain wrapped os
// error — NOT a [ValidationError] — so the CLI can distinguish exit code 2
// (I/O) from exit code 1 (validation) per MOCK-701.5. A decode or schema
// failure is returned as a *[ValidationError] naming path.
func LoadFile(path string) (scenario.Document, error) {
	// filepath.Clean normalises the operator-supplied path; --scenario-root
	// sandboxing of extends targets is TASK-007's job, not this direct-load
	// entry point's. Cleaning also satisfies gosec G304 without suppressing it.
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return scenario.Document{}, fmt.Errorf("config: read scenario: %w", err)
	}
	return load(raw, path)
}

// Load decodes and schema-validates a scenario document from raw YAML or JSON
// bytes, attributing problems to source (a display name such as a file path).
// It is the in-memory entry point; LoadFile is the file wrapper.
func Load(raw []byte, source string) (scenario.Document, error) {
	if source == "" {
		source = "<input>"
	}
	return load(raw, source)
}

// load is the shared body of LoadFile and Load. It converts YAML→JSON,
// validates the resulting JSON tree against the embedded schema, runs the
// Phase 1 cross-field semantic rules, and only then decodes into the typed
// document.
func load(raw []byte, source string) (scenario.Document, error) {
	// YAML→JSON so one schema validates both formats (ADR-013). JSON input is a
	// subset of YAML, so this is the identity for JSON.
	jsonBytes, err := yaml.YAMLToJSON(raw)
	if err != nil {
		return scenario.Document{}, &ValidationError{
			Source:   source,
			Problems: []Problem{{Pointer: "", Message: "not valid YAML or JSON: " + err.Error()}},
		}
	}

	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(jsonBytes))
	if err != nil {
		return scenario.Document{}, &ValidationError{
			Source:   source,
			Problems: []Problem{{Pointer: "", Message: "not a JSON value: " + err.Error()}},
		}
	}

	if verr := Validate(inst); verr != nil {
		verr.Source = source
		return scenario.Document{}, verr
	}

	var doc scenario.Document
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		// The tree already passed the schema, so a typed-decode failure here is
		// a shape the schema permits but the Go types cannot hold — a
		// schema/struct drift defect, surfaced as a validation problem rather
		// than hidden.
		return scenario.Document{}, &ValidationError{
			Source:   source,
			Problems: []Problem{{Pointer: "", Message: "document decode: " + err.Error()}},
		}
	}
	return doc, nil
}

// Validate runs the embedded JSON Schema plus the Phase 1 cross-field semantic
// rules against an already-decoded JSON tree (as produced by
// jsonschema.UnmarshalJSON). It is exported so TASK-007 can validate a document
// AFTER composition (ADR-008: validation runs post-merge), reusing the exact
// same rules the load path uses. It returns nil when the document is valid, or
// a *[ValidationError] whose Source the caller fills in.
func Validate(inst any) *ValidationError {
	sch, err := scenarioSchema()
	if err != nil {
		return &ValidationError{Problems: []Problem{{Pointer: "", Message: err.Error()}}}
	}

	var problems []Problem
	if verr := sch.Validate(inst); verr != nil {
		var ve *jsonschema.ValidationError
		if errors.As(verr, &ve) {
			problems = append(problems, schemaProblems(ve)...)
		} else {
			problems = append(problems, Problem{Pointer: "", Message: verr.Error()})
		}
	}

	problems = append(problems, semanticProblems(inst)...)

	if len(problems) == 0 {
		return nil
	}
	return &ValidationError{Problems: problems}
}

// schemaProblems flattens a jsonschema ValidationError tree into a stable,
// deduplicated list of leaf problems, each carrying the instance-location JSON
// Pointer and the keyword's message. It uses the library's BasicOutput, which
// already flattens the causal tree to leaves, then keeps only units that name a
// concrete failure. The result is sorted by (pointer, message) so the reported
// order is deterministic regardless of validator internals — a config contract
// must not report the same file's errors in a different order run to run.
func schemaProblems(ve *jsonschema.ValidationError) []Problem {
	basic := ve.BasicOutput()
	specific, concrete := surveyUnits(basic.Errors)

	out := selectProblems(basic.Errors, specific, concrete)

	// If every unit was a cascade (or none carried a message), fall back to the
	// top-level unit so a problem is never dropped silently.
	if len(out) == 0 {
		msg := ve.Error()
		if basic.Error != nil {
			msg = basic.Error.String()
		}
		out = append(out, Problem{Pointer: basic.InstanceLocation, Message: msg})
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Pointer != out[j].Pointer {
			return out[i].Pointer < out[j].Pointer
		}
		return out[i].Message < out[j].Message
	})
	return out
}

// surveyUnits scans the flattened output units once, returning the set of
// instance locations that carry a specific unknown-key failure and the count of
// units that name a concrete (non-cascade) problem. Both feed the cascade
// filtering in selectProblems.
func surveyUnits(units []jsonschema.OutputUnit) (specific map[string]bool, concrete int) {
	specific = make(map[string]bool)
	for i := range units {
		u := units[i]
		if isUnknownKeyKeyword(u.KeywordLocation) {
			specific[u.InstanceLocation] = true
		}
		if u.Error != nil && isConcreteProblem(u) {
			concrete++
		}
	}
	return specific, concrete
}

// selectProblems turns the flattened output units into a deduplicated list of
// actionable problems, dropping cascade units (a parent that merely reports a
// descendant failed) whenever a concrete problem remains to carry the signal.
func selectProblems(units []jsonschema.OutputUnit, specific map[string]bool, concrete int) []Problem {
	seen := make(map[Problem]struct{})
	var out []Problem
	for i := range units {
		u := units[i]
		if u.Error == nil || isCascadeUnit(u, specific) {
			continue
		}
		// Drop a bare combinator cascade ('allOf'/'oneOf'/'anyOf' failed,
		// "validation failed") whenever a concrete problem is also reported;
		// keep it only if it is the sole signal.
		if !isConcreteProblem(u) && concrete > 0 {
			continue
		}
		p := Problem{
			Pointer: u.InstanceLocation,
			Message: renderSchemaMessage(u.KeywordLocation, u.InstanceLocation, u.Error.String()),
		}
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out
}

// isConcreteProblem reports whether a unit names a specific, actionable
// failure rather than a bare combinator/group cascade. An unknown-key keyword
// (unevaluatedProperties/additionalProperties) is always concrete even though
// the library labels it "validation failed", because renderSchemaMessage turns
// it into a named-key message.
func isConcreteProblem(u jsonschema.OutputUnit) bool {
	if u.Error == nil {
		return false
	}
	if isUnknownKeyKeyword(u.KeywordLocation) {
		return true
	}
	switch u.Error.String() {
	case "'allOf' failed", "'oneOf' failed", "'anyOf' failed", "validation failed":
		return false
	default:
		return true
	}
}

// isUnknownKeyKeyword reports whether a keyword location is an
// unevaluatedProperties or additionalProperties assertion — the schema's
// mechanism for rejecting unknown keys.
func isUnknownKeyKeyword(keywordLocation string) bool {
	return strings.HasSuffix(keywordLocation, "/unevaluatedProperties") ||
		strings.HasSuffix(keywordLocation, "/additionalProperties") ||
		keywordLocation == "/unevaluatedProperties" ||
		keywordLocation == "/additionalProperties"
}

// isCascadeUnit reports whether a unit is a generic parent cascade that a more
// specific unit already explains, so it can be dropped from the reported
// problems. A "false schema" unevaluatedProperties failure at some parent
// location is a cascade when a specific unknown-key failure exists strictly
// below it; the specific child names the actual offending key.
func isCascadeUnit(u jsonschema.OutputUnit, specific map[string]bool) bool {
	if u.Error == nil {
		return false
	}
	if u.Error.String() != "false schema" {
		return false
	}
	if !isUnknownKeyKeyword(u.KeywordLocation) {
		return false
	}
	for loc := range specific {
		if loc != u.InstanceLocation && strings.HasPrefix(loc, u.InstanceLocation+"/") {
			return true
		}
	}
	return false
}

// renderSchemaMessage turns the validator's terse keyword messages into ones a
// scenario author can act on. The important case is the schema's pervasive
// unevaluatedProperties:false / additionalProperties:false: an unknown key is
// reported by the library as a bare "false schema" or "validation failed" whose
// only reliable clue is that the pointer's LAST segment is the offending key
// name and the keyword is (un)evaluatedProperties. That is the single most
// valuable rejection in the whole contract (a silently-ignored key means a
// fault that does not fire), so it must name the key explicitly. Other messages
// are already actionable and pass through unchanged.
func renderSchemaMessage(keywordLocation, pointer, msg string) string {
	if !isUnknownKeyKeyword(keywordLocation) {
		return msg
	}
	key := lastPointerToken(pointer)
	if key == "" {
		return "unexpected value not permitted by the schema"
	}
	return fmt.Sprintf("unknown key %q is not permitted here (additionalProperties: false)", key)
}

// lastPointerToken returns the final, JSON-Pointer-unescaped segment of a JSON
// Pointer, or "" for the root. It reverses RFC 6901 escaping (~1 -> /, ~0 -> ~).
func lastPointerToken(pointer string) string {
	if pointer == "" {
		return ""
	}
	idx := strings.LastIndexByte(pointer, '/')
	if idx < 0 {
		return pointer
	}
	tok := pointer[idx+1:]
	tok = strings.ReplaceAll(tok, "~1", "/")
	tok = strings.ReplaceAll(tok, "~0", "~")
	return tok
}
