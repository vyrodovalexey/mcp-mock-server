package controlclient_test

import (
	"regexp"
	"testing"

	"github.com/vyrodovalexey/mcp-mock-server/internal/control"
	"github.com/vyrodovalexey/mcp-mock-server/internal/controlclient"
)

// operationIDRE extracts operationId values from the embedded OpenAPI document,
// so the parity check reads the contract rather than a hand-copied list.
var operationIDRE = regexp.MustCompile(`operationId:\s*(\w+)`)

// TestVerbOpIDsExistInContract asserts every CLI verb mirrors an operationId
// that actually exists in the control API contract (MOCK-104.3: the CLI surface
// is generated from — and cannot drift from — the control route table). It reads
// the operationIds from control.OpenAPISpec, the authoritative contract copy.
func TestVerbOpIDsExistInContract(t *testing.T) {
	contractOps := map[string]bool{}
	for _, m := range operationIDRE.FindAllSubmatch(control.OpenAPISpec, -1) {
		contractOps[string(m[1])] = true
	}
	if len(contractOps) == 0 {
		t.Fatal("no operationIds found in embedded OpenAPI spec")
	}
	for _, v := range controlclient.Verbs() {
		if !contractOps[v.OpID] {
			t.Errorf("verb %q maps to opID %q which is not in the control contract", v.Name, v.OpID)
		}
	}
}

// TestVerbsCoverPhase1Reads asserts the CLI exposes every Phase 1 read/clear
// operation, so a verb is not silently dropped. getOpenAPI is served specially
// (raw YAML) and is not a CLI verb, so it is excluded.
func TestVerbsCoverPhase1Reads(t *testing.T) {
	want := map[string]bool{
		"listInstances":   false,
		"getInstance":     false,
		"getSeed":         false,
		"getHealth":       false,
		"getJournal":      false,
		"getCorrelations": false,
		"clearJournal":    false,
	}
	for _, v := range controlclient.Verbs() {
		if _, ok := want[v.OpID]; ok {
			want[v.OpID] = true
		}
	}
	for op, covered := range want {
		if !covered {
			t.Errorf("no CLI verb covers Phase 1 operation %q", op)
		}
	}
}

// TestVerbNamesUnique asserts the verb table has no duplicate names, since the
// CLI dispatcher looks a verb up by name.
func TestVerbNamesUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, v := range controlclient.Verbs() {
		if seen[v.Name] {
			t.Errorf("duplicate verb name %q", v.Name)
		}
		seen[v.Name] = true
	}
}
