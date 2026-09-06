//go:build functional

package functional_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"testing"
)

// golden.go is the golden-fixture comparison machinery (MOCK-604). Golden files
// are HAND-AUTHORED from the wire annex / builtin-tools.md, never regenerated
// from server code (test-strategy.md §0: "make golden-update does not exist").
// They encode the PROVISIONAL 2026-07-28 behaviour (GAP-003) and are labelled as
// such in testdata/golden/README.md. There is deliberately no update flag here:
// a change to a golden is a reviewed edit, so a behaviour change is a readable
// diff rather than a silent overwrite (MOCK-604), and a disagreement between a
// golden and the server is a defect to escalate — never a golden to rewrite.

// goldenDir is the root under which fixtures live, directoried by MOCK-nnn.
const goldenDir = "testdata/golden"

// phase1RequirementDirs is the closed set of MOCK-nnn directories a Phase 1
// golden suite must cover. The coverage test fails if any is missing or empty,
// so a requirement cannot silently lose its fixture (acceptance criterion 2).
var phase1RequirementDirs = []string{
	"MOCK-201",
	"MOCK-202",
	"MOCK-209",
}

// volatileKeys are JSON object keys whose values are volatile (ports, ephemeral
// ids, timing) and therefore redacted to a stable placeholder before comparison,
// so a real behaviour change is visible and environment noise is not (MOCK-604).
// Phase 1 response bodies carry none of these at the top level — the built-in
// tool responses are pure functions of (arguments, config, seed) (builtin-tools
// §1.4) — but the redactor is applied uniformly so a future field that leaks a
// timestamp cannot destabilise the suite silently.
//
// "hash" covers credential.hash (CC-2 / AMEND-8): since the credential-hash key
// is per-process crypto/rand (never seed-derived), the recorded credential.hash
// CHANGES EVERY RUN, so a golden that pinned its literal value would fail
// nondeterministically. Redacting it here is the volatile-field handling
// security.md §5 and ADR-002's named exception mandate. This does NOT weaken the
// MOCK-407 distinctness claim: "the hub minted a distinct upstream credential" is
// an equality comparison BETWEEN records within one run, asserted through the
// journalapi selectors / assert package, not through golden byte-comparison — the
// golden only needs to show a credential WAS present, which the redacted-but-
// present placeholder preserves.
var volatileKeys = map[string]bool{
	"wallTime":       true,
	"monoNs":         true,
	"durationNs":     true,
	"elapsedNs":      true,
	"peer":           true,
	"port":           true,
	"eventId":        true,
	"requestState":   true,
	"hash":           true, // includes credential.hash (CC-2 / AMEND-8)
	"traceId":        true,
	"spanId":         true,
	"sessionId":      true,
	"subscriptionId": true,
}

// redactedPlaceholder is the deterministic value a volatile field is replaced
// with. It is a literal so a diff shows "<redacted>" rather than a changing
// value, and so the presence of the field is still asserted.
const redactedPlaceholder = "<redacted>"

// hashRedactRe collapses a redacted-credential header value
// (<redacted:sha256:HEX...>) to a stable placeholder: the hash suffix is
// per-record and volatile, but the redaction itself is the property under test.
var hashRedactRe = regexp.MustCompile(`<redacted:sha256:[0-9a-fA-F]+>`)

// canonicalize re-encodes raw JSON into a stable, sorted, indented form with
// volatile values redacted, so goldens are reviewable diffs. It sorts object
// keys recursively (the server already emits canonical JSON for tool content,
// but sorting here makes the golden independent of any field-order change that
// is not itself the subject of a test) and redacts volatile leaves.
func canonicalize(t *testing.T, raw []byte) []byte {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("golden canonicalize: input is not JSON: %v\nraw: %s", err, raw)
	}
	v = redact(v)
	out, err := marshalSorted(v)
	if err != nil {
		t.Fatalf("golden canonicalize: marshal: %v", err)
	}
	return out
}

// redact walks a decoded JSON value and replaces any volatile leaf with the
// stable placeholder, and any redacted-credential string with a stable form.
func redact(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			if volatileKeys[k] {
				t[k] = redactedPlaceholder
				continue
			}
			t[k] = redact(child)
		}
		return t
	case []any:
		for i := range t {
			t[i] = redact(t[i])
		}
		return t
	case string:
		return hashRedactRe.ReplaceAllString(t, "<redacted:sha256>")
	default:
		return v
	}
}

// marshalSorted marshals v with object keys sorted recursively and two-space
// indentation, producing a stable, reviewable byte form. It uses a manual
// encoder because encoding/json sorts map keys already, but only for map[string]
// values — which is exactly what we have after Unmarshal into any.
func marshalSorted(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(sortValue(v)); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// sortValue returns v with every nested map replaced by an order-stable form.
// encoding/json already emits map[string]any keys in sorted order, so this is a
// pass-through that exists to make the intent explicit and to allow a future
// slice-ordering rule to hook in without touching callers.
func sortValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make(map[string]any, len(t))
		for _, k := range keys {
			out[k] = sortValue(t[k])
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i := range t {
			out[i] = sortValue(t[i])
		}
		return out
	default:
		return v
	}
}

// assertGolden compares the canonicalized actual bytes against the golden file
// at goldenDir/relPath. On mismatch it fails with a readable diff of the two
// canonical forms. A missing golden is a hard failure (a fixture must exist for
// a requirement it claims to cover) — never auto-created.
func assertGolden(t *testing.T, relPath string, actual []byte) {
	t.Helper()
	path := filepath.Join(goldenDir, relPath)
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("golden %s: %v (goldens are hand-authored; create it with a reviewer, never auto-generate)", path, err)
	}
	gotCanon := canonicalize(t, actual)
	wantCanon := canonicalize(t, want)
	if !bytes.Equal(gotCanon, wantCanon) {
		t.Fatalf("golden %s mismatch (MOCK-604):\n--- want (canonical) ---\n%s\n--- got (canonical) ---\n%s\n"+
			"If this is an intended behaviour change, edit the golden by hand with a reviewer.\n"+
			"If it is NOT intended, this is a DEFECT — escalate; do not edit the golden.",
			path, wantCanon, gotCanon)
	}
}

// TestGoldenCoverage asserts that every Phase 1 requirement that owns a golden
// directory actually has at least one *.json fixture in it, so a requirement
// cannot silently lose its regression contract (acceptance criterion 2).
func TestFunctional_GoldenCoverage(t *testing.T) {
	for _, dir := range phase1RequirementDirs {
		full := filepath.Join(goldenDir, dir)
		entries, err := os.ReadDir(full)
		if err != nil {
			t.Errorf("golden directory %s missing: %v", full, err)
			continue
		}
		var jsons int
		for _, e := range entries {
			if !e.IsDir() && filepath.Ext(e.Name()) == ".json" {
				jsons++
			}
		}
		if jsons == 0 {
			t.Errorf("golden directory %s has no *.json fixture; every Phase 1 requirement must have one", full)
		}
	}
}
