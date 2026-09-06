package config

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

// specSchemaPath is the authoritative schema in the specification tree. The
// embedded copy must be byte-identical to it (MOCK-701.3). The path is relative
// to this test file: internal/config -> repo root is three levels up.
var specSchemaPath = filepath.Join("..", "..", "specification", "contracts", "scenario.schema.json")

// TestEmbeddedSchema_ByteIdenticalToSpec is the schema-check gate (MOCK-701.3 /
// ADR-008): the go:embed-ed schema must be SHA-256-identical to the
// specification source. If this fails, the Go binary is validating against a
// schema that has drifted from the published contract.
func TestEmbeddedSchema_ByteIdenticalToSpec(t *testing.T) {
	specBytes, err := os.ReadFile(specSchemaPath)
	if err != nil {
		t.Fatalf("read spec schema %s: %v", specSchemaPath, err)
	}
	specHash := sha256.Sum256(specBytes)
	embeddedHash := sha256.Sum256(EmbeddedSchemaBytes())
	if specHash != embeddedHash {
		t.Fatalf("embedded schema drifted from spec:\n embedded=%s\n spec    =%s\n"+
			"re-copy specification/contracts/scenario.schema.json into internal/config/schema/",
			hex.EncodeToString(embeddedHash[:]), hex.EncodeToString(specHash[:]))
	}
}

// TestSchema_CompilesOnce asserts the embedded schema is compiled at most once
// per process regardless of how many documents are loaded (acceptance
// criterion 7 / MOCK-107 startup budget). Because compilation is memoised by a
// single sync.OnceValue, the compile counter must read exactly 1 after any
// number of loads and validations.
func TestSchema_CompilesOnce(t *testing.T) {
	for i := 0; i < 5; i++ {
		if _, err := scenarioSchema(); err != nil {
			t.Fatalf("compile: %v", err)
		}
	}
	// A couple of real loads also go through the compiled schema.
	_, _ = Load([]byte("apiVersion: mcpmock.dev/v1alpha1\nkind: Scenario\nmetadata: {name: a}\nspec: {}"), "a")
	_, _ = Load([]byte("apiVersion: mcpmock.dev/v1alpha1\nkind: Scenario\nmetadata: {name: b}\nspec: {}"), "b")

	if got := compileCountForTest(); got != 1 {
		t.Fatalf("schema compiled %d times, want exactly 1", got)
	}
}

// TestSchema_Compiles is a smoke test that the embedded schema itself is a
// well-formed, compilable JSON Schema — a corrupt embed would fail here with a
// clear message rather than on the first user load.
func TestSchema_Compiles(t *testing.T) {
	sch, err := scenarioSchema()
	if err != nil {
		t.Fatalf("embedded schema failed to compile: %v", err)
	}
	if sch == nil {
		t.Fatal("compiled schema is nil")
	}
}
