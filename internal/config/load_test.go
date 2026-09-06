package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vyrodovalexey/mcp-mock-server/scenario"
)

// mustReadTestdata returns the bytes of a file under testdata/, failing the
// test on error.
func mustReadTestdata(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read testdata/%s: %v", name, err)
	}
	return b
}

// TestLoad_ValidScenario proves a valid document loads and decodes into the
// typed shape (acceptance criterion: a valid scenario loads correctly).
func TestLoad_ValidScenario(t *testing.T) {
	doc, err := Load(mustReadTestdata(t, "valid-scenario.yaml"), "valid-scenario.yaml")
	if err != nil {
		t.Fatalf("unexpected error loading valid scenario: %v", err)
	}
	if doc.APIVersion != scenario.APIVersion {
		t.Errorf("apiVersion = %q, want %q", doc.APIVersion, scenario.APIVersion)
	}
	if doc.Kind != scenario.KindScenario {
		t.Errorf("kind = %q, want %q", doc.Kind, scenario.KindScenario)
	}
	if doc.Metadata == nil || doc.Metadata.Name != "happy-path" {
		t.Fatalf("metadata.name not decoded: %+v", doc.Metadata)
	}
	spec, err := doc.ScenarioSpec()
	if err != nil {
		t.Fatalf("decode scenario spec: %v", err)
	}
	if spec.Catalog == nil || spec.Catalog.Tools == nil || spec.Catalog.Tools.Count == nil {
		t.Fatalf("catalogue.tools.count not decoded: %+v", spec.Catalog)
	}
	if *spec.Catalog.Tools.Count != 10 {
		t.Errorf("catalogue.tools.count = %d, want 10", *spec.Catalog.Tools.Count)
	}
}

// TestLoad_YAMLAndJSONParity proves the same document as YAML and as JSON
// produces an identical decoded document (MOCK-701.1).
func TestLoad_YAMLAndJSONParity(t *testing.T) {
	yamlDoc, err := Load(mustReadTestdata(t, "valid-scenario.yaml"), "y.yaml")
	if err != nil {
		t.Fatalf("load yaml: %v", err)
	}
	jsonDoc, err := Load(mustReadTestdata(t, "valid-scenario.json"), "j.json")
	if err != nil {
		t.Fatalf("load json: %v", err)
	}
	// The spec is preserved as canonical JSON bytes by YAMLToJSON in both
	// paths, so the two must be byte-identical — the strongest form of
	// "identical composed JSON trees".
	if string(yamlDoc.Spec) != string(jsonDoc.Spec) {
		t.Errorf("YAML and JSON specs differ:\n yaml=%s\n json=%s", yamlDoc.Spec, jsonDoc.Spec)
	}
	if yamlDoc.APIVersion != jsonDoc.APIVersion || yamlDoc.Kind != jsonDoc.Kind {
		t.Errorf("YAML/JSON header mismatch: %+v vs %+v", yamlDoc, jsonDoc)
	}
}

// TestLoad_UnknownKeyRejected proves additionalProperties/unevaluatedProperties
// false actually rejects a typo at every level, and that the error names the
// offending JSON path (acceptance criteria: prove the typo case; error names
// the JSON path). This is the single most valuable property of the contract.
func TestLoad_UnknownKeyRejected(t *testing.T) {
	cases := []struct {
		name        string
		doc         string
		wantPointer string
		wantInMsg   string
	}{
		{
			name: "unknown key at document root",
			doc: `apiVersion: mcpmock.dev/v1alpha1
kind: Scenario
metadata: { name: x }
spec: {}
bogusRoot: true`,
			wantPointer: "/bogusRoot",
			wantInMsg:   "bogusRoot",
		},
		{
			name: "unknown key under spec",
			doc: `apiVersion: mcpmock.dev/v1alpha1
kind: Scenario
metadata: { name: x }
spec:
  toolz: {}`,
			wantPointer: "/spec/toolz",
			wantInMsg:   "toolz",
		},
		{
			name: "unknown key under a nested block",
			doc: `apiVersion: mcpmock.dev/v1alpha1
kind: Scenario
metadata: { name: x }
spec:
  journal:
    enabld: true`,
			wantPointer: "/spec/journal/enabld",
			wantInMsg:   "enabld",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load([]byte(tc.doc), "typo.yaml")
			if err == nil {
				t.Fatalf("expected rejection, got nil")
			}
			if !errors.Is(err, ErrValidation) {
				t.Fatalf("error is not ErrValidation: %v", err)
			}
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("error is not *ValidationError: %v", err)
			}
			if !hasProblemAt(ve, tc.wantPointer, tc.wantInMsg) {
				t.Errorf("no problem naming pointer %q containing %q; got problems: %v",
					tc.wantPointer, tc.wantInMsg, ve.Problems)
			}
		})
	}
}

// TestLoad_UnsupportedVersionRejected proves an unknown or missing apiVersion,
// and a missing kind, are rejected clearly, naming the field (MOCK-701.2).
func TestLoad_UnsupportedVersionRejected(t *testing.T) {
	cases := []struct {
		name      string
		doc       string
		wantInMsg string
	}{
		{
			name:      "unsupported apiVersion",
			doc:       "apiVersion: mcpmock.dev/v2\nkind: Scenario\nmetadata: { name: x }\nspec: {}",
			wantInMsg: "mcpmock.dev/v1alpha1",
		},
		{
			name:      "missing apiVersion",
			doc:       "kind: Scenario\nmetadata: { name: x }\nspec: {}",
			wantInMsg: "apiVersion",
		},
		{
			name:      "missing kind",
			doc:       "apiVersion: mcpmock.dev/v1alpha1\nmetadata: { name: x }\nspec: {}",
			wantInMsg: "kind",
		},
		{
			name:      "unknown kind value",
			doc:       "apiVersion: mcpmock.dev/v1alpha1\nkind: Herd\nmetadata: { name: x }\nspec: {}",
			wantInMsg: "kind",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load([]byte(tc.doc), "ver.yaml")
			if err == nil {
				t.Fatalf("expected rejection, got nil")
			}
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("error is not *ValidationError: %v", err)
			}
			joined := allMessages(ve)
			if !strings.Contains(joined, tc.wantInMsg) {
				t.Errorf("error does not mention %q; full error:\n%s", tc.wantInMsg, err)
			}
		})
	}
}

// TestLoad_ErrorNamesJSONPath asserts the reported pointer is a real JSON
// Pointer into the document for a value-level violation (metadata.name pattern).
func TestLoad_ErrorNamesJSONPath(t *testing.T) {
	doc := `apiVersion: mcpmock.dev/v1alpha1
kind: Scenario
metadata:
  name: "has spaces and !!! illegal chars"
spec: {}`
	_, err := Load([]byte(doc), "path.yaml")
	if err == nil {
		t.Fatalf("expected rejection for bad metadata.name")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error is not *ValidationError: %v", err)
	}
	found := false
	for _, p := range ve.Problems {
		if p.Pointer == "/metadata/name" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a problem at /metadata/name; got %v", ve.Problems)
	}
	// The rendered message must lead with the source and be actionable.
	if !strings.HasPrefix(err.Error(), "path.yaml:") {
		t.Errorf("error message does not lead with source: %q", err.Error())
	}
}

// TestLoad_SemanticSizeFaultRule proves the ADR-005 cross-field rule
// (MOCK-701.6): bodies:full + a size fault above maxBytes/16 is rejected, in
// the same Problem shape as a schema error, and accepted otherwise.
func TestLoad_SemanticSizeFaultRule(t *testing.T) {
	// maxBytes 1600 => budget 100; a 200-byte size fault must be rejected.
	reject := `apiVersion: mcpmock.dev/v1alpha1
kind: Scenario
metadata: { name: x }
spec:
  journal:
    bodies: full
    maxBytes: 1600
  faults:
    - id: big-body
      trigger: { armed: true }
      action:
        kind: size
        size:
          kind: oversizedResult
          bytes: 200`
	_, err := Load([]byte(reject), "sem.yaml")
	if err == nil {
		t.Fatalf("expected ADR-005 semantic rejection")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error is not *ValidationError: %v", err)
	}
	if !hasProblemAt(ve, "/spec/faults/0/action/size/bytes", "ADR-005") {
		t.Errorf("expected ADR-005 problem at size fault pointer; got %v", ve.Problems)
	}

	// Same document with bodies:digest is accepted (the rule only fires for
	// bodies:full).
	accept := strings.Replace(reject, "bodies: full", "bodies: digest", 1)
	if _, err := Load([]byte(accept), "sem.yaml"); err != nil {
		t.Errorf("bodies:digest with a large size fault must be accepted; got %v", err)
	}
}

// TestLoadFile_IOErrorNotValidationError proves a missing file is an I/O error,
// NOT a ValidationError — the distinction the CLI needs for exit code 2 vs 1
// (MOCK-701.5).
func TestLoadFile_IOErrorNotValidationError(t *testing.T) {
	_, err := LoadFile(filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	if err == nil {
		t.Fatalf("expected an I/O error")
	}
	if errors.Is(err, ErrValidation) {
		t.Errorf("missing-file error must not be a validation error: %v", err)
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expected os.ErrNotExist, got %v", err)
	}
}

// TestLoadFile_ValidFromDisk exercises the file path end to end.
func TestLoadFile_ValidFromDisk(t *testing.T) {
	doc, err := LoadFile(filepath.Join("testdata", "valid-scenario.yaml"))
	if err != nil {
		t.Fatalf("LoadFile valid: %v", err)
	}
	if doc.Metadata == nil || doc.Metadata.Name != "happy-path" {
		t.Fatalf("unexpected decoded doc: %+v", doc)
	}
}

// hasProblemAt reports whether ve has a problem at the exact pointer whose
// message contains substr.
func hasProblemAt(ve *ValidationError, pointer, substr string) bool {
	for _, p := range ve.Problems {
		if p.Pointer == pointer && strings.Contains(p.Message, substr) {
			return true
		}
	}
	return false
}

// allMessages joins every problem's rendered form for substring assertions.
func allMessages(ve *ValidationError) string {
	var b strings.Builder
	for _, p := range ve.Problems {
		b.WriteString(p.String())
		b.WriteByte('\n')
	}
	return b.String()
}
