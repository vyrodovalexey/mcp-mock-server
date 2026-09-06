package control

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestEmbeddedOpenAPIMatchesSpec asserts the embedded control-api.openapi.yaml
// is byte-identical to the authoritative specification copy (MOCK-104.1). This
// is the same drift-guard TASK-006 uses for scenario.schema.json: the served
// document must be the contract, not a stale copy. If this fails, re-copy
// specification/contracts/control-api.openapi.yaml into internal/control.
func TestEmbeddedOpenAPIMatchesSpec(t *testing.T) {
	// Walk up to the module root (this test runs from internal/control).
	specPath := filepath.Join("..", "..", "specification", "contracts", "control-api.openapi.yaml")
	want, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("read spec: %v", err)
	}
	if !bytes.Equal(OpenAPISpec, want) {
		t.Errorf("embedded OpenAPI document differs from %s; re-copy it into internal/control", specPath)
	}
}

// TestOpenAPINonEmpty guards against an empty embed.
func TestOpenAPINonEmpty(t *testing.T) {
	if len(OpenAPISpec) == 0 {
		t.Fatal("embedded OpenAPI document is empty")
	}
}
