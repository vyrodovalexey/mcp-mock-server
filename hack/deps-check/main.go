// Command deps-check is the mechanical enforcement of ADR-013's two-tier
// dependency policy (AMEND-5). It is the runnable oracle behind
// `make deps-check`.
//
// It is deliberately a hack/ tool, NOT application source: TASK-033 owns the
// wiring of the policy gates, and ADR-013 §Enforcement asks that "both lists
// live in one file so the check and this ADR cannot drift". This file is that
// one file. It is not imported by internal/, cmd/, scenario/, journalapi/ or
// assert/ — it is invoked as a standalone program.
//
// # The policy, as implemented
//
// ADR-013 (AMEND-5) splits dependencies into two tiers. The `go list -deps`
// command is the ADR's named mechanical oracle:
//
//   - RUNTIME tier — `go list -deps ./...` (which EXCLUDES test imports). Every
//     module here enters a consumer's go.mod and carries the MOCK-107 cost, so
//     it is screened strictly. A test-only module appearing here is a FAILURE:
//     it means a _test.go-only dependency leaked into production code.
//
//   - TEST-ONLY tier — `go list -deps -test ./...` MINUS the runtime graph.
//     These never reach a consumer's graph, so ADR criteria 1–3 are waived; the
//     module must simply be one of the approved test-only modules (or a
//     documented transitive test-support dependency of an approved module).
//
// # Why this design rather than a hand-transcribed allow-list of 28 modules
//
// ADR-013's approved RUNTIME table names the DIRECT modules and explicitly
// states "Its own transitive tree is inspected and listed below" — i.e.
// approving prometheus/client_golang approves its transitive tree
// (client_model, perks, xxhash, …) as a consequence. The reviewed go.mod IS
// that inspected transitive closure. Transcribing all 28 resolved modules by
// hand into this file would immediately drift from go.mod on the next patch
// bump and produce false positives that make the gate untrustworthy.
//
// So the runtime tier is enforced by two load-bearing, non-drifting rules that
// capture exactly what ADR-013 exists to prevent:
//
//  1. NO test-only module (goleak, testify, mock) may appear in the runtime
//     graph — the leak the two-tier split was created to catch.
//  2. NO explicitly-rejected module (ADR-013 "Not taken": cobra/pflag, the
//     web frameworks, x/crypto, the JWT libraries, google/uuid as a DIRECT
//     dep, …) may appear as a DIRECT runtime dependency — the "took whatever
//     was convenient" failure mode.
//  3. Every runtime-graph module must be present in the reviewed go.mod
//     (direct or indirect). A module in the build graph but absent from the
//     require blocks would mean an unreviewed, unpinned dependency.
//
// The test-only tier is enforced strictly against the approved set, because
// that set is small and directly imported.
//
// This is exactly why `goleak` — a spec-MANDATED (MOCK-107.7) test-only
// dependency — passes: it is in the approved TEST-ONLY set and is verified to
// be absent from the RUNTIME graph. A naive "reject anything not in the runtime
// table" check would false-positive on it; this one does not.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
)

// approvedTestOnly is the ADR-013 (AMEND-5) "Approved test-only set", plus the
// transitive test-support dependencies those modules legitimately pull. A
// module here is exempt from ADR criteria 1–3 but must be unreachable from the
// runtime graph.
var approvedTestOnly = map[string]string{
	// Directly named in ADR-013's "Approved test-only set".
	"github.com/stretchr/testify": "assertion ergonomics in _test.go (ADR-013)",
	"go.uber.org/goleak":          "MOCK-107.7 mandates goleak by name (ADR-013)",
	"go.uber.org/mock":            "mockgen test doubles, build-tagged tools.go (ADR-013)",

	// Transitive test-support dependencies of an APPROVED RUNTIME module. These
	// are reachable ONLY from _test.go via that module's testutil/test helper,
	// never from production code, so they cost a consumer's go.mod nothing.
	// Reported to the architect (TASK-033) as a deviation from the literal
	// ADR-013 test-only table, which enumerates only the direct set.
	//   - godebug/diff is pulled by prometheus/client_golang/prometheus/testutil,
	//     used in internal/obs/metrics_test.go (exposition linting).
	"github.com/kylelemons/godebug": "transitive: prometheus testutil (internal/obs/metrics_test.go); ADR-013 deviation, reported",
}

// forbiddenDirect is the ADR-013 "Not taken, deliberately" set: modules whose
// appearance as a DIRECT runtime dependency is a policy violation. Each would
// have served a requirement ADR-013 chose to meet with the standard library or
// an internal package instead.
var forbiddenDirect = map[string]string{
	"github.com/spf13/cobra":          "ADR-013: CLI is stdlib flag + a small dispatcher (MOCK-104)",
	"github.com/spf13/pflag":          "ADR-013: CLI is stdlib flag (MOCK-104)",
	"github.com/go-chi/chi":           "ADR-012 / ADR-013: HTTP is net/http (MOCK-102)",
	"github.com/gin-gonic/gin":        "ADR-012 / ADR-013: HTTP is net/http (MOCK-102)",
	"github.com/labstack/echo":        "ADR-012 / ADR-013: HTTP is net/http (MOCK-102)",
	"github.com/valyala/fasthttp":     "ADR-012 / ADR-013: HTTP is net/http (MOCK-102)",
	"github.com/golang-jwt/jwt":       "ADR-013 / ADR-020: JWTs via internal/jwtmini (MOCK-405)",
	"github.com/go-jose/go-jose":      "ADR-013 / ADR-020: JWTs via internal/jwtmini (MOCK-405)",
	"github.com/square/go-jose":       "ADR-013 / ADR-020: JWTs via internal/jwtmini (MOCK-405)",
	"golang.org/x/crypto":             "ADR-013 / ADR-010: AEAD via crypto/aes+cipher+hkdf (MOCK-243)",
	"github.com/google/uuid":          "ADR-013 / ADR-002: ids minted deterministically; uuid may only be an INDIRECT dep",
	"gopkg.in/yaml.v3":                "ADR-013: YAML via sigs.k8s.io/yaml so one JSON Schema validates both (MOCK-701)",
	"github.com/goccy/go-yaml":        "ADR-013: YAML via sigs.k8s.io/yaml (MOCK-701)",
	"github.com/xeipuuv/gojsonschema": "ADR-013: schema via santhosh-tekuri/jsonschema/v6 (MOCK-701/225)",
}

func main() {
	verbose := flag.Bool("v", false, "print the resolved runtime and test-only module sets")
	flag.Parse()

	runtime, err := moduleSet("go", "list", "-deps",
		"-f", "{{if .Module}}{{.Module.Path}}{{end}}", "./...")
	if err != nil {
		fatalf("resolving runtime graph (go list -deps ./...): %v", err)
	}
	withTest, err := moduleSet("go", "list", "-deps", "-test",
		"-f", "{{if .Module}}{{.Module.Path}}{{end}}", "./...")
	if err != nil {
		fatalf("resolving test graph (go list -deps -test ./...): %v", err)
	}
	direct, err := moduleSet("go", "list", "-m",
		"-f", "{{if not .Indirect}}{{.Path}}{{end}}", "all")
	if err != nil {
		fatalf("resolving direct modules (go list -m all): %v", err)
	}
	inGoMod, err := moduleSet("go", "list", "-m", "-f", "{{.Path}}", "all")
	if err != nil {
		fatalf("resolving go.mod module set (go list -m all): %v", err)
	}

	self := "github.com/vyrodovalexey/mcp-mock-server"
	delete(runtime, self)
	delete(withTest, self)
	delete(direct, self)
	delete(inGoMod, self)

	// The test-only tier is the test graph minus the runtime graph.
	testOnly := map[string]bool{}
	for m := range withTest {
		if !runtime[m] {
			testOnly[m] = true
		}
	}

	if *verbose {
		printSet("RUNTIME graph (go list -deps ./...)", runtime)
		printSet("TEST-ONLY graph (go list -deps -test ./... \\ runtime)", testOnly)
	}

	var violations []string

	// Rule 1: no approved TEST-ONLY module may appear in the RUNTIME graph.
	for m := range runtime {
		if _, isTestOnly := approvedTestOnly[m]; isTestOnly {
			violations = append(violations, fmt.Sprintf(
				"RUNTIME LEAK: test-only module %q appears in the runtime graph "+
					"(go list -deps ./...). A _test.go-only dependency has leaked into "+
					"production code — ADR-013 two-tier violation.", m))
		}
	}

	// Rule 2: no explicitly-rejected module may be a DIRECT runtime dependency.
	for m := range direct {
		if !runtime[m] {
			continue // rejected list concerns the runtime tier only.
		}
		if why, forbidden := matchForbidden(m); forbidden {
			violations = append(violations, fmt.Sprintf(
				"FORBIDDEN DIRECT DEP: %q is a direct runtime dependency but is "+
					"rejected by ADR-013 (%s). A new dependency requires an ADR "+
					"amendment, not a go get.", m, why))
		}
	}

	// Rule 3: every runtime-graph module must be present in the reviewed go.mod.
	for m := range runtime {
		if !inGoMod[m] {
			violations = append(violations, fmt.Sprintf(
				"UNPINNED: runtime module %q is in the build graph but not in the "+
					"reviewed go.mod require set. Run `go mod tidy` and review.", m))
		}
	}

	// Rule 4: every module in the TEST-ONLY tier must be approved.
	for m := range testOnly {
		if _, ok := approvedTestOnly[m]; !ok {
			violations = append(violations, fmt.Sprintf(
				"UNAPPROVED TEST DEP: %q is in the test-only graph but is not in "+
					"ADR-013's approved test-only set. Admit it via an ADR amendment "+
					"or remove it.", m))
		}
	}

	if len(violations) > 0 {
		sort.Strings(violations)
		fmt.Fprintf(os.Stderr, "deps-check: %d ADR-013 violation(s):\n", len(violations))
		for _, v := range violations {
			fmt.Fprintf(os.Stderr, "  - %s\n", v)
		}
		os.Exit(1)
	}

	fmt.Printf("deps-check: OK — ADR-013 two-tier policy satisfied.\n")
	fmt.Printf("  runtime modules:   %d (all present in go.mod; no test-only leak; no forbidden direct dep)\n", len(runtime))
	fmt.Printf("  test-only modules: %d (all in the approved test-only set)\n", len(testOnly))
	for m := range testOnly {
		fmt.Printf("    - %s  (%s)\n", m, approvedTestOnly[m])
	}
}

// matchForbidden reports whether module path m is (or is a versioned suffix of)
// a rejected module, e.g. "github.com/golang-jwt/jwt/v5" matches
// "github.com/golang-jwt/jwt".
func matchForbidden(m string) (string, bool) {
	for prefix, why := range forbiddenDirect {
		if m == prefix || strings.HasPrefix(m, prefix+"/") {
			return why, true
		}
	}
	return "", false
}

// moduleSet runs a go command and returns the unique non-empty output lines as
// a set.
func moduleSet(name string, args ...string) (map[string]bool, error) {
	cmd := exec.Command(name, args...)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	set := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			set[line] = true
		}
	}
	return set, nil
}

func printSet(title string, set map[string]bool) {
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Printf("%s: %d\n", title, len(keys))
	for _, k := range keys {
		fmt.Printf("  %s\n", k)
	}
}

func fatalf(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "deps-check: "+format+"\n", args...)
	os.Exit(2)
}
