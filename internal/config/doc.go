// Package config loads, schema-validates and composes mcpmock scenario
// documents. It is the internal engine behind the public scenario package:
// scenario owns the types, config owns the I/O, the embedded JSON Schema, the
// validation that turns a byte stream into a trusted [scenario.Document]
// (MOCK-701), and the extends composition that lets a fleet of mocks share a
// base without copy-paste (MOCK-703).
//
// # Validation on load (MOCK-701)
//
// Every load validates the document against the JSON Schema 2020-12 embedded
// from specification/contracts/scenario.schema.json. Validation is not
// optional and not deferred: a document that does not satisfy the schema —
// including one with an unknown key, because the schema is
// unevaluatedProperties: false throughout — is rejected before it is returned.
// This is what makes a typo in a user's fault name an error rather than a
// silently-ignored key, which ADR-008 identifies as the single most expensive
// failure mode for a tool like this.
//
// Because YAML and JSON decode to the same JSON tree (via sigs.k8s.io/yaml,
// ADR-013), one schema validates both input formats and the Go types carry
// json: tags only.
//
// # Actionable errors (the --validate contract)
//
// Validation failures are reported as a [ValidationError] whose Problems each
// carry the offending JSON Pointer, a human-readable message, and — for a
// file-based load — the source file path. This is a deliberate contract, not a
// convenience: mcpmock validate must exit non-zero with a message a user can
// act on (MOCK-701.5), and "validation failed" is not acceptable for a config
// contract users author by hand. The error's Error string leads with the file
// and the first problem's pointer.
//
// # Compile once (MOCK-107 startup budget)
//
// The schema is compiled exactly once per process via [sync.OnceValue] and the
// compiled *jsonschema.Schema is reused for every load. Schema compilation is
// the expensive step; doing it per-load would blow the 40 ms slice of the
// MOCK-107 startup budget when ≥200 instances each load a scenario. The
// compile step reads only embedded bytes, holds no process-global mutable
// state that a load can mutate, and is safe for concurrent use.
//
// # Composition (extends, MOCK-703)
//
// [ComposeFile] resolves a document's extends chain depth-first, left-to-right,
// later-wins, over decoded JSON trees, then validates the merged result — after
// composition, not before (ADR-008 / MOCK-703.8), so an intermediate document
// reached via extends may be incomplete. The merge algebra (scalars replace,
// objects merge, explicit null deletes, object lists merge by their
// schema-declared key, other lists replace, $patch overrides) lives in
// merge.go; the extends resolution, cycle and depth bounds live in compose.go;
// the --scenario-root path sandbox lives in root.go. [Compose] is the in-memory
// overlay entry point for programmatic composition.
//
// # Not in this package
//
// The CLI --validate wiring and its exit codes are TASK-024; the fleet
// generate: expansion (MOCK-904) is a later phase. This package stops at
// producing a single validated, composed [scenario.Document].
package config
