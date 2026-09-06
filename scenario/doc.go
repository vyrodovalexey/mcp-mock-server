// Package scenario carries the public, user-facing types for an mcpmock
// scenario document — the single declarative YAML/JSON file that configures all
// mock behavior (MOCK-701). It is a serialization boundary: external hub test
// suites import it to construct or inspect a scenario, so it imports nothing
// from internal/ (architecture.md §6.1 rule 4). Loading, composition and
// schema validation live in internal/config; this package is types only.
//
// # Version policy
//
// The document is versioned by its apiVersion and kind (ADR-008). The only
// apiVersion this release understands is [APIVersion] ("mcpmock.dev/v1alpha1")
// and the only kinds are [KindScenario] and [KindFleet]. The v1alpha1 suffix is
// deliberate and load-bearing: the scenario shape is explicitly UNSTABLE until
// the wire annex (ADR-019 / GAP-003) is ratified, because the protocol shape
// leaks into the scenario shape. An unknown or unsupported apiVersion is
// rejected by the loader with a message naming the accepted value, never
// silently coerced — a versioned document whose version is not understood is an
// error, not a best-effort parse (MOCK-701.2).
//
// # The absent-vs-zero idiom (the TASK-007 precondition)
//
// Composition (extends, TASK-007) merges a base document with overlays, and an
// overlay must be able to express three distinct states for any field:
//
//   - the field is ABSENT (the overlay says nothing; inherit the base),
//   - the field is SET to a value (the overlay replaces the base's value),
//   - the field is DELETED (the overlay sets it to JSON null; ADR-008's null
//     rule removes the key from the merged result).
//
// Plain Go structs collapse absent and zero-value into one state — a bool field
// that is false cannot be distinguished from a bool field that was never
// written — which would make an overlay that sets enabled: false
// indistinguishable from one that is silent. Every optional scalar in this
// package is therefore a POINTER, exactly as internal/jsonrpc modeled id
// presence (IDAbsent vs a present-but-null id) and internal/wire modeled the
// _meta envelope: a nil pointer records ABSENCE, and a non-nil pointer to the
// zero value records a deliberately-set zero. TASK-007's merge reads this
// nil-ness; the schema validates the pointed-to value. This distinction is what
// lets an overlay turn a base's boolean off, or set a count to 0, without being
// erased by the merge.
//
// Object-valued optional fields (nested config blocks) are likewise pointers so
// that "block absent" is distinct from "block present but all-default", and map
// and slice fields are left nil when absent.
//
// # Deliberately unvalidated fields
//
// Four fields on an authored catalog item — InputSchema, OutputSchema,
// Annotations and Icons — are typed [json.RawMessage] and are NEVER validated
// or normalised by mcpmock (MOCK-222.3 / MOCK-224 / MOCK-225, ADR-008). Their
// whole purpose is to carry deliberately-invalid JSON Schemas and annotations
// that must survive our validation untouched and be emitted verbatim, including
// key order (MOCK-701.7). mcpmock validates the scenario, not the payloads
// inside it; the schema documents this exemption explicitly.
package scenario
