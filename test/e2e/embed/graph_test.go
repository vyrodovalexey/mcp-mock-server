//go:build e2e

package embed_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// forbiddenDepSubstrings are import paths that MUST NOT appear in the transitive
// dependency graph of a journalapi+assert-only consumer (ADR-001 §6.2,
// RISK-07). An internal server package, Prometheus or OTel appearing here would
// mean a hub that only inspects a recorded journal inherits the full server
// dependency graph — the exact weight RISK-07 warns about.
var forbiddenDepSubstrings = []string{
	"github.com/vyrodovalexey/mcp-mock-server/internal/",
	"github.com/prometheus/client_golang",
	"go.opentelemetry.io/otel",
}

// TestE2E_AssertOnlyGraphHasNoServerDeps is TC-028.7. It runs `go list -deps`
// over the assertonly witness package and asserts the transitive package graph
// contains none of the forbidden paths. It records the full graph as an artifact
// so the property is evidence, not prose (RISK-07 explicitly asks for the graph
// as an artifact rather than an in-prose assertion).
func TestE2E_AssertOnlyGraphHasNoServerDeps(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("go toolchain not on PATH: %v", err)
	}

	// -deps lists every package the target transitively imports; the template
	// prints one import path per line.
	cmd := exec.Command(goBin, "list", "-deps", "-f", "{{.ImportPath}}",
		"github.com/vyrodovalexey/mcp-mock-server-embedtest/assertonly")
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps failed: %v\n%s", err, out)
	}

	deps := strings.Split(strings.TrimSpace(string(out)), "\n")

	// Record the graph as an artifact. The path is configurable via
	// EMBED_ARTIFACT_DIR (used in CI); it defaults to the repository's
	// test-artifacts directory relative to this module.
	artifactDir := os.Getenv("EMBED_ARTIFACT_DIR")
	if artifactDir == "" {
		// This module sits at test/e2e/embed; the artifacts live at
		// .opencode/output/test-artifacts from the repo root (../../../..).
		artifactDir = filepath.Join("..", "..", "..", ".opencode", "output", "test-artifacts")
	}
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		t.Fatalf("create artifact dir %s: %v", artifactDir, err)
	}
	artifact := filepath.Join(artifactDir, "assert-journalapi-transitive-graph.txt")
	header := "# Transitive package graph of a journalapi+assert-only consumer\n" +
		"# (ADR-001 §6.2 / RISK-07). Root: assertonly witness package.\n" +
		"# Recorded by TASK-028 TestE2E_AssertOnlyGraphHasNoServerDeps.\n\n"
	if err := os.WriteFile(artifact, []byte(header+string(out)), 0o644); err != nil {
		t.Fatalf("write artifact %s: %v", artifact, err)
	}
	t.Logf("recorded transitive graph (%d packages) to %s", len(deps), artifact)

	for _, dep := range deps {
		for _, bad := range forbiddenDepSubstrings {
			if strings.Contains(dep, bad) {
				t.Errorf("forbidden dependency %q (matches %q) in the assert+journalapi-only graph; "+
					"a journal-only consumer must not inherit the server graph (ADR-001 §6.2)", dep, bad)
			}
		}
	}
}
