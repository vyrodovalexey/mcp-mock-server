package config

import (
	"bytes"
	_ "embed"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// schemaURL is the in-memory URL the embedded schema is registered under. It
// matches the schema's own $id so intra-document $ref resolution
// (#/$defs/...) works without network access — no $ref in scenario.schema.json
// ever leaves this compiled resource.
const schemaURL = "https://mcpmock.dev/schema/v1alpha1/scenario.schema.json"

// embeddedSchema is the canonical scenario JSON Schema, embedded byte-for-byte
// from specification/contracts/scenario.schema.json (MOCK-701.3). A unit test
// asserts this copy is SHA-256-identical to the specification source; that
// assertion is the schema-check gate (ADR-008). The schema is the authoritative
// contract — if the Go types in package scenario and this schema disagree, the
// schema wins.
//
//go:embed schema/scenario.schema.json
var embeddedSchema []byte

// EmbeddedSchemaBytes returns a copy of the embedded scenario JSON Schema. It
// exists so the schema-check test (and any tooling) can hash the exact bytes
// this binary validates against, rather than re-reading the file from disk.
func EmbeddedSchemaBytes() []byte {
	out := make([]byte, len(embeddedSchema))
	copy(out, embeddedSchema)
	return out
}

// compiledSchema is the process-wide, compiled scenario schema, built at most
// once by [scenarioSchema]. compileCount records how many times the underlying
// compile actually ran, so the compile-once test can assert it is exactly one
// across many loads (acceptance criterion 7). It is an atomic counter because,
// although the OnceValue body runs once, the test reads it concurrently with
// nothing in particular yet an atomic keeps it race-clean by construction. The
// accessor lives in export_test.go so this instrumentation has no production
// reader.
var (
	compileCount   atomic.Int64
	compiledSchema = sync.OnceValue(func() schemaResult {
		compileCount.Add(1)
		sch, err := compileEmbeddedSchema()
		return schemaResult{schema: sch, err: err}
	})
)

// schemaResult pairs the compiled schema with any compile error so both are
// memoised by the single OnceValue; a compile failure is a programming error
// (the embedded schema is a build artifact) but is surfaced rather than
// panicked so callers can report it.
type schemaResult struct {
	schema *jsonschema.Schema
	err    error
}

// scenarioSchema returns the compiled scenario schema, compiling it on first
// call and reusing the result thereafter. It is safe for concurrent use.
func scenarioSchema() (*jsonschema.Schema, error) {
	r := compiledSchema()
	return r.schema, r.err
}

// compileEmbeddedSchema parses and compiles the embedded schema into a
// validator. The custom x-mcpmock-merge / x-mcpmock-merge-key annotations are
// unknown keywords under draft 2020-12 and are ignored by the validator, which
// is exactly what ADR-008 intends: they document merge behavior for TASK-007
// without affecting validation.
func compileEmbeddedSchema() (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(embeddedSchema))
	if err != nil {
		return nil, fmt.Errorf("config: parse embedded scenario schema: %w", err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource(schemaURL, doc); err != nil {
		return nil, fmt.Errorf("config: register embedded scenario schema: %w", err)
	}
	sch, err := c.Compile(schemaURL)
	if err != nil {
		return nil, fmt.Errorf("config: compile embedded scenario schema: %w", err)
	}
	return sch, nil
}
