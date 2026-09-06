package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vyrodovalexey/mcp-mock-server/scenario"
)

// composeRoot returns a ScenarioRoot bounding testdata/compose, the sandbox
// under which the fixture chain resolves.
func composeRoot(t *testing.T) *ScenarioRoot {
	t.Helper()
	root, err := NewScenarioRoot(filepath.Join("testdata", "compose"))
	if err != nil {
		t.Fatalf("scenario root: %v", err)
	}
	return root
}

func fixture(name string) string {
	return filepath.Join("testdata", "compose", name)
}

// TestCompose_SingleOverlay proves base + one overlay merges: scalar replace,
// keyed-list merge and null-delete all in one document (703.1, 703.3, 703.4).
func TestCompose_SingleOverlay(t *testing.T) {
	doc, err := ComposeFile(composeRoot(t), fixture("single-overlay.yaml"))
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	spec, err := doc.ScenarioSpec()
	if err != nil {
		t.Fatalf("decode spec: %v", err)
	}

	// scalar replace: count 10 (base) -> 50 (overlay).
	if spec.Catalog == nil || spec.Catalog.Tools == nil || spec.Catalog.Tools.Count == nil {
		t.Fatalf("tools.count missing: %+v", spec.Catalog)
	}
	if *spec.Catalog.Tools.Count != 50 {
		t.Errorf("tools.count = %d, want 50", *spec.Catalog.Tools.Count)
	}

	// keyed-list merge: alpha updated in place, beta inherited, gamma appended.
	names := make([]string, len(spec.Catalog.Items))
	descByName := map[string]string{}
	for i, it := range spec.Catalog.Items {
		names[i] = it.Name
		if it.Description != nil {
			descByName[it.Name] = *it.Description
		}
	}
	wantOrder := []string{"alpha", "beta", "gamma"}
	if strings.Join(names, ",") != strings.Join(wantOrder, ",") {
		t.Errorf("item order = %v, want %v", names, wantOrder)
	}
	if descByName["alpha"] != "overlay alpha" {
		t.Errorf("alpha description = %q, want overlay alpha", descByName["alpha"])
	}
	if descByName["beta"] != "base beta" {
		t.Errorf("beta description = %q, want base beta (inherited)", descByName["beta"])
	}

	// null-delete: base set discover.ttlMs; overlay set it to null -> absent.
	if spec.Discover == nil {
		t.Fatalf("discover missing")
	}
	if spec.Discover.TTLMs != nil {
		t.Errorf("discover.ttlMs = %s, want absent after null-delete", spec.Discover.TTLMs)
	}
	if spec.Discover.Instructions == nil || *spec.Discover.Instructions != "from overlay" {
		t.Errorf("discover.instructions not overridden: %+v", spec.Discover.Instructions)
	}
}

// TestCompose_NullDeleteVsAbsentVsZero pins the three-state distinction the
// whole algebra rests on (703.3): a base value, then an overlay that either
// deletes it (null), leaves it absent (inherit), or sets it to a zero value.
func TestCompose_NullDeleteVsAbsentVsZero(t *testing.T) {
	dir := t.TempDir()
	root, err := NewScenarioRoot(dir)
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	write("base.yaml", `apiVersion: mcpmock.dev/v1alpha1
kind: Scenario
metadata: { name: base }
spec:
  journal:
    enabled: true
    maxRecords: 5000
  discover:
    ttlMs: 1000
`)

	cases := []struct {
		name         string
		overlayField string
		// assertion on the composed spec
		check func(t *testing.T, spec scenario.InstanceSpec)
	}{
		{
			name:         "null deletes maxRecords",
			overlayField: "    maxRecords: null",
			check: func(t *testing.T, spec scenario.InstanceSpec) {
				if spec.Journal == nil {
					t.Fatalf("journal missing")
				}
				if spec.Journal.MaxRecords != nil {
					t.Errorf("maxRecords = %v, want nil (deleted)", *spec.Journal.MaxRecords)
				}
			},
		},
		{
			name:         "absent inherits maxRecords",
			overlayField: "    truncateBytes: 42",
			check: func(t *testing.T, spec scenario.InstanceSpec) {
				if spec.Journal == nil || spec.Journal.MaxRecords == nil {
					t.Fatalf("maxRecords should be inherited, got %+v", spec.Journal)
				}
				if *spec.Journal.MaxRecords != 5000 {
					t.Errorf("maxRecords = %d, want inherited 5000", *spec.Journal.MaxRecords)
				}
			},
		},
		{
			name:         "zero value sets maxRecords to 0",
			overlayField: "    maxRecords: 0",
			check: func(t *testing.T, spec scenario.InstanceSpec) {
				if spec.Journal == nil || spec.Journal.MaxRecords == nil {
					t.Fatalf("maxRecords should be present (set to 0), got %+v", spec.Journal)
				}
				if *spec.Journal.MaxRecords != 0 {
					t.Errorf("maxRecords = %d, want 0 (explicit zero)", *spec.Journal.MaxRecords)
				}
			},
		},
		{
			name:         "zero value sets enabled to false",
			overlayField: "    enabled: false",
			check: func(t *testing.T, spec scenario.InstanceSpec) {
				if spec.Journal == nil || spec.Journal.Enabled == nil {
					t.Fatalf("enabled should be present (set false), got %+v", spec.Journal)
				}
				if *spec.Journal.Enabled {
					t.Errorf("enabled = true, want explicit false")
				}
			},
		},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			overlayName := fmt.Sprintf("overlay-%d.yaml", i)
			write(overlayName, fmt.Sprintf(`apiVersion: mcpmock.dev/v1alpha1
kind: Scenario
metadata: { name: overlay }
extends: [ base.yaml ]
spec:
  journal:
%s
`, tc.overlayField))
			doc, err := ComposeFile(root, filepath.Join(dir, overlayName))
			if err != nil {
				t.Fatalf("compose: %v", err)
			}
			spec, err := doc.ScenarioSpec()
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			tc.check(t, spec)
		})
	}
}

// TestCompose_DiamondPrecedence proves depth-first, left-to-right, later-wins
// resolution on a three-level chain with a diamond (703.1). top extends
// [mid-a, mid-b]; both extend leaf.
//
//   - count: leaf=1, mid-a=2, mid-b=3. mid-b is rightmost (higher precedence),
//     so the shared field resolves to 3.
//   - nameTemplate: only mid-a sets it; no branch overrides it, so mid-a's
//     contribution survives — proving the left branch is not simply discarded.
//   - instructions: leaf="from leaf", mid-a="from mid-a", mid-b does not set it.
//     compose(mid-b) inherits "from leaf"; mid-b wins over mid-a as the rightmost
//     branch, so the composed value is "from leaf". This is the subtle,
//     correct diamond outcome: a field the winning branch inherits beats a
//     field a losing branch set.
func TestCompose_DiamondPrecedence(t *testing.T) {
	doc, err := ComposeFile(composeRoot(t), fixture("diamond-top.yaml"))
	if err != nil {
		t.Fatalf("compose diamond: %v", err)
	}
	spec, err := doc.ScenarioSpec()
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if spec.Catalog == nil || spec.Catalog.Tools == nil || spec.Catalog.Tools.Count == nil {
		t.Fatalf("count missing: %+v", spec.Catalog)
	}
	if *spec.Catalog.Tools.Count != 3 {
		t.Errorf("count = %d, want 3 (rightmost branch mid-b wins)", *spec.Catalog.Tools.Count)
	}
	if spec.Catalog.Tools.NameTemplate == nil || *spec.Catalog.Tools.NameTemplate != "from-mid-a-{{i}}" {
		t.Errorf("nameTemplate = %v, want mid-a's value surviving", spec.Catalog.Tools.NameTemplate)
	}
	if spec.Discover == nil || spec.Discover.Instructions == nil || *spec.Discover.Instructions != "from leaf" {
		t.Errorf("instructions = %+v, want 'from leaf' (mid-b inherits leaf and wins)", spec.Discover)
	}
	// leaf's shared item survives; top's item is appended by keyed merge.
	names := make([]string, len(spec.Catalog.Items))
	for i, it := range spec.Catalog.Items {
		names[i] = it.Name
	}
	if strings.Join(names, ",") != "shared,top-item" {
		t.Errorf("item order = %v, want [shared top-item]", names)
	}
}

// TestCompose_CycleDetected proves an a->b->a cycle is reported as *CycleError
// naming the chain, not a stack overflow (703.1).
func TestCompose_CycleDetected(t *testing.T) {
	root, err := NewScenarioRoot(filepath.Join("testdata", "compose", "cycle"))
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	_, err = ComposeFile(root, filepath.Join("testdata", "compose", "cycle", "a.yaml"))
	if err == nil {
		t.Fatalf("expected a cycle error")
	}
	var ce *CycleError
	if !errors.As(err, &ce) {
		t.Fatalf("error is not *CycleError: %v", err)
	}
	if !errors.Is(err, ErrValidation) {
		t.Errorf("cycle error should wrap ErrValidation: %v", err)
	}
	msg := ce.Error()
	// The chain must name both files and close the loop back to a.yaml.
	if !strings.Contains(msg, "a.yaml") || !strings.Contains(msg, "b.yaml") {
		t.Errorf("cycle message does not name the chain: %q", msg)
	}
	if strings.Count(msg, "a.yaml") < 2 {
		t.Errorf("cycle message should show the loop closing on a.yaml: %q", msg)
	}
	if len(ce.Chain) < 3 {
		t.Errorf("cycle chain too short to name the loop: %v", ce.Chain)
	}
	t.Logf("cycle message: %s", msg)
}

// TestCompose_DepthLimit proves a chain deeper than maxExtendsDepth is rejected
// as *DepthError rather than hanging or overflowing (bounded loader).
func TestCompose_DepthLimit(t *testing.T) {
	dir := t.TempDir()
	root, err := NewScenarioRoot(dir)
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	// Build a linear chain longer than the limit: n0 extends n1 ... extends nN.
	total := maxExtendsDepth + 5
	for i := 0; i <= total; i++ {
		var body string
		if i == total {
			body = "apiVersion: mcpmock.dev/v1alpha1\nkind: Scenario\nmetadata: { name: leaf }\nspec: { era: modern }\n"
		} else {
			body = fmt.Sprintf("apiVersion: mcpmock.dev/v1alpha1\nkind: Scenario\nmetadata: { name: n%d }\nextends: [ n%d.yaml ]\nspec: {}\n", i, i+1)
		}
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("n%d.yaml", i)), []byte(body), 0o600); err != nil {
			t.Fatalf("write n%d: %v", i, err)
		}
	}
	_, err = ComposeFile(root, filepath.Join(dir, "n0.yaml"))
	if err == nil {
		t.Fatalf("expected a depth-limit error")
	}
	var de *DepthError
	if !errors.As(err, &de) {
		t.Fatalf("error is not *DepthError: %v", err)
	}
	if de.Limit != maxExtendsDepth {
		t.Errorf("depth limit reported %d, want %d", de.Limit, maxExtendsDepth)
	}
	if !errors.Is(err, ErrValidation) {
		t.Errorf("depth error should wrap ErrValidation: %v", err)
	}
}

// TestCompose_PostMergeValidationRejectsUnknownKey proves validation runs AFTER
// composition and still rejects an unknown key introduced through the overlay
// path, naming the JSON pointer with the same quality as the loader (703.8).
func TestCompose_PostMergeValidationRejectsUnknownKey(t *testing.T) {
	_, err := ComposeFile(composeRoot(t), fixture("typo-overlay.yaml"))
	if err == nil {
		t.Fatalf("expected post-merge validation to reject the typo")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error is not *ValidationError: %v", err)
	}
	if !hasProblemAt(ve, "/spec/catalogyo", "catalogyo") {
		t.Errorf("expected a problem naming /spec/catalogyo; got %v", ve.Problems)
	}
}

// TestCompose_IncompleteIntermediateComposes proves an intermediate document
// that would fail validation on its own (no metadata, no spec) composes
// successfully when the final result is valid (703.8).
func TestCompose_IncompleteIntermediateComposes(t *testing.T) {
	// The incomplete base alone must fail schema validation.
	if _, err := LoadFile(fixture("incomplete-base.yaml")); err == nil {
		t.Fatalf("incomplete-base.yaml should be invalid on its own")
	}
	// But composing a document that completes it must succeed.
	doc, err := ComposeFile(composeRoot(t), fixture("completes-incomplete.yaml"))
	if err != nil {
		t.Fatalf("composing a completed document failed: %v", err)
	}
	if doc.Metadata == nil || doc.Metadata.Name != "completed" {
		t.Errorf("composed metadata wrong: %+v", doc.Metadata)
	}
}

// TestCompose_ByteIdenticalAcrossRuns proves the composed tree is byte-identical
// across repeated runs, independent of Go map iteration order (ADR-008 §0.1
// determinism). It composes the diamond many times and compares canonical bytes.
func TestCompose_ByteIdenticalAcrossRuns(t *testing.T) {
	root := composeRoot(t)
	first, err := composeTreeForTest(root, fixture("diamond-top.yaml"))
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	for i := 0; i < 100; i++ {
		got, err := composeTreeForTest(root, fixture("diamond-top.yaml"))
		if err != nil {
			t.Fatalf("compose run %d: %v", i, err)
		}
		if !bytes.Equal(first, got) {
			t.Fatalf("composed bytes differ at run %d:\n first %s\n got   %s", i, first, got)
		}
	}
	t.Logf("deterministic composed tree: %s", first)
}

// TestCompose_NoResidualExtends proves the composed output carries no extends
// key — it is a self-contained scenario, not a still-extending one.
func TestCompose_NoResidualExtends(t *testing.T) {
	bytesOut, err := composeTreeForTest(composeRoot(t), fixture("single-overlay.yaml"))
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	if bytes.Contains(bytesOut, []byte(`"extends"`)) {
		t.Errorf("composed output still contains an extends key: %s", bytesOut)
	}
}

// TestCompose_ProgrammaticOverlays proves the in-memory Compose entry point
// applies overlays left to right, later winning (library WithOverlay path).
func TestCompose_ProgrammaticOverlays(t *testing.T) {
	base := []byte(`apiVersion: mcpmock.dev/v1alpha1
kind: Scenario
metadata: { name: base }
spec: { catalogue: { tools: { count: 1 } } }`)
	o1 := []byte(`{"spec":{"catalogue":{"tools":{"count":2}}}}`)
	o2 := []byte(`{"spec":{"catalogue":{"tools":{"count":3}}}}`)
	doc, err := Compose("prog", base, o1, o2)
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	spec, err := doc.ScenarioSpec()
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if spec.Catalog == nil || spec.Catalog.Tools == nil || spec.Catalog.Tools.Count == nil || *spec.Catalog.Tools.Count != 3 {
		t.Errorf("count not 3 after later-wins overlays: %+v", spec.Catalog)
	}
}
