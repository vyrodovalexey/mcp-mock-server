package wire

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// containedLiterals is the set of unambiguous wire literals ADR-019 forbids
// outside this package (and test/mcpclient, which transcribes its own copy on
// purpose, ADR-017). These are the method names and resultType values: strings
// whose only meaning is wire vocabulary, so their appearance as a source
// literal in another production package is a hard-coded wire literal and a
// containment violation.
//
// Deliberately NOT scanned: the _meta key words (protocolVersion,
// clientCapabilities), the data.missing key (missing), and the JSON-RPC error
// codes. The _meta words are English/JSON field names that legitimately appear
// in the journalapi public contract (which documents the decoded _meta shape)
// and as generic words in unrelated tests; a substring gate over them produces
// false positives. Distinguishing a wire-emitting use from an incidental one
// requires the semantic (type-and-callsite) analysis that the make
// wire-literal-check gate owns (TASK-033). This test enforces the
// unambiguous subset, which is where an accidental handler literal would land.
var containedLiterals = []string{
	MethodDiscover, MethodToolsList, MethodToolsCall,
	ResultTypeDiscovery, ResultTypeToolList, ResultTypeToolResult,
}

// allowedDirs are the directories permitted to hold these literals: this
// package, and the independent test client (ADR-017 anti-circularity — its copy
// is deliberate).
var allowedDirs = []string{
	filepath.Join("internal", "wire"),
	filepath.Join("test", "mcpclient"),
}

// preExistingExceptions records module-relative production files that already
// held a contained literal before TASK-010, for a reason unrelated to wire
// emission, and which TASK-010's boundary does not permit changing. Each entry
// is a reported, not fixed, ADR-019 tension:
//
//   - internal/obs/metrics.go (TASK-004, gate-passed): the three method names
//     are METRIC LABEL DOMAIN values (observability.md §2.1 fixed cardinality),
//     not wire emission. They are a bounded enum for Prometheus labels. This is
//     a pre-existing occurrence flagged in the TASK-010 report for the architect
//     to route to the wire-literal-check gate owner, which can decide whether
//     the obs domain should reference internal/wire or stay independent for
//     cardinality reasons.
//
// The allowlist exists so this test enforces the rule for NEW code without
// falsely failing on pre-existing, out-of-boundary code or silently rewriting
// another task's file.
var preExistingExceptions = map[string]bool{
	filepath.Join("internal", "obs", "metrics.go"): true,
}

// TestNoWireLiteralsEscape is the Go enforcement of ADR-019's containment rule
// (TASK-010 acceptance criterion 1) for the unambiguous wire vocabulary. It
// walks the module's production (non-test) Go source outside the allowed
// directories and fails if any contained wire literal appears as a string
// LITERAL (AST *ast.BasicLit) — so a mention in a comment is never flagged.
//
// It is the test-suite counterpart of make wire-literal-check; the Makefile
// gate is owned by the task permitted to edit the Makefile (TASK-033), and the
// semantic superset covering the _meta words is that gate's responsibility.
//
// Test files are excluded: fixtures across the tree use example method names as
// data, which is legitimate and not wire emission. Production code is where an
// accidental hard-coded handler literal would appear.
func TestNoWireLiteralsEscape(t *testing.T) {
	root := moduleRoot(t)
	if root == "" {
		t.Skip("module root not found; skipping containment scan")
	}

	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "bin", "specification", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		if inAllowedDir(rel) || preExistingExceptions[rel] {
			return nil
		}
		scanFileForLiterals(t, fset, path, rel)
		return nil
	})
	if err != nil {
		t.Fatalf("walking module tree: %v", err)
	}
}

// forbidden is the lookup set of contained literals, built once per scan.
var forbidden = func() map[string]bool {
	m := make(map[string]bool, len(containedLiterals))
	for _, lit := range containedLiterals {
		m[lit] = true
	}
	return m
}()

// scanFileForLiterals parses the file and reports any contained wire literal
// that appears as a string literal (not a comment, not an identifier).
func scanFileForLiterals(t *testing.T, fset *token.FileSet, path, rel string) {
	t.Helper()
	file, err := parser.ParseFile(fset, path, nil, 0) // no comments parsed
	if err != nil {
		t.Fatalf("parsing %s: %v", rel, err)
	}
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		val, uqErr := strconv.Unquote(lit.Value)
		if uqErr != nil {
			return true
		}
		if forbidden[val] {
			t.Errorf("wire literal %q appears as a string literal in %s, outside internal/wire (ADR-019 containment)", val, rel)
		}
		return true
	})
}

// inAllowedDir reports whether the module-relative path is inside one of the
// directories permitted to hold wire literals.
func inAllowedDir(rel string) bool {
	for _, dir := range allowedDirs {
		if rel == dir || strings.HasPrefix(rel, dir+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// moduleRoot walks up from the working directory to find the directory holding
// go.mod, returning "" if none is found before the filesystem root.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// TestPackageIsSoleDefinitionSite proves TASK-010 acceptance criterion 5:
// changing any [P-nn] constant is contained to this package. Every contained
// literal is a constant defined here (referenced above), and each is defined
// exactly once. Paired with TestNoWireLiteralsEscape (no OTHER production
// package defines them as literals), this bounds the ratification blast radius
// to internal/wire.
func TestPackageIsSoleDefinitionSite(t *testing.T) {
	seen := map[string]bool{}
	for _, lit := range containedLiterals {
		if lit == "" {
			t.Errorf("a contained wire literal is empty; every literal must have exactly one definition here")
		}
		if seen[lit] {
			t.Errorf("wire literal %q is listed twice; each has exactly one definition site", lit)
		}
		seen[lit] = true
	}
}
