package config

import (
	"encoding/json"
	"fmt"

	"github.com/vyrodovalexey/mcp-mock-server/scenario"
)

// Phase 1 cross-field semantic rules (MOCK-701.6). These are the checks that
// JSON Schema cannot express because they relate two independent fields.
// They are reported in the SAME [Problem] shape (JSON Pointer + message) as
// schema failures, so a caller — and mcpmock validate --output json — cannot
// tell a semantic failure from a schema failure by shape (ADR-008).
//
// The only rule Phase 1 must carry is the ADR-005 rule: journal.bodies: "full"
// together with a size fault whose emitted body exceeds journal.maxBytes/16
// would let one oversized-result fault (MOCK-504) evict the journal ring, so it
// is rejected at load time even though size faults themselves arrive in a later
// phase. The rule is declared now so a scenario authored against a future phase
// still fails loudly today rather than silently mis-sizing the ring.

// defaultJournalMaxBytes mirrors the schema default for journal.maxBytes
// (#/$defs/journal). When a document does not set maxBytes, the ADR-005 budget
// is computed against this default, matching the value the running server would
// use.
const defaultJournalMaxBytes = 268435456

// sizeFaultBudgetDivisor is the ADR-005 factor: a size fault's body may not
// exceed maxBytes/16 while bodies == "full".
const sizeFaultBudgetDivisor = 16

// semanticProblems runs the Phase 1 cross-field rules against a decoded JSON
// tree. It re-encodes the tree to the typed [scenario.Document] to inspect
// fields; a tree that reaches here has already passed the schema, so this
// re-decode cannot fail on shape and any error is reported defensively rather
// than ignored.
func semanticProblems(inst any) []Problem {
	raw, err := json.Marshal(inst)
	if err != nil {
		return []Problem{{Pointer: "", Message: "internal: re-encode for semantic checks: " + err.Error()}}
	}
	var doc scenario.Document
	if err := json.Unmarshal(raw, &doc); err != nil {
		return []Problem{{Pointer: "", Message: "internal: re-decode for semantic checks: " + err.Error()}}
	}
	// Semantic rules apply to a single instance spec; a Fleet's per-instance
	// specs are checked when each is composed (TASK-007).
	if doc.Kind != scenario.KindScenario {
		return nil
	}
	spec, err := doc.ScenarioSpec()
	if err != nil {
		return nil
	}
	return sizeFaultJournalProblems(&spec)
}

// sizeFaultJournalProblems implements the ADR-005 rule. It fires only when
// journal.bodies is "full" (the default) AND a fault rule carries a size action
// whose bytes exceed maxBytes/16.
func sizeFaultJournalProblems(spec *scenario.InstanceSpec) []Problem {
	if !bodiesIsFull(spec.Journal) {
		return nil
	}
	budget := journalMaxBytes(spec.Journal) / sizeFaultBudgetDivisor

	var problems []Problem
	for i := range spec.Faults {
		rule := spec.Faults[i]
		bytesVal, ok := sizeActionBytes(rule.Action)
		if !ok || bytesVal <= budget {
			continue
		}
		problems = append(problems, Problem{
			Pointer: fmt.Sprintf("/spec/faults/%d/action/size/bytes", i),
			Message: fmt.Sprintf(
				"size fault body of %d bytes exceeds journal.maxBytes/16 (%d) while journal.bodies is \"full\"; "+
					"set journal.bodies to \"digest\", \"truncate\" or \"off\", or reduce bytes (ADR-005)",
				bytesVal, budget),
		})
	}
	return problems
}

// bodiesIsFull reports whether journal.bodies resolves to "full" — either set
// explicitly or left at its schema default. A nil Journal or nil Bodies both
// mean the default, which is "full".
func bodiesIsFull(j *scenario.Journal) bool {
	if j == nil || j.Bodies == nil {
		return true
	}
	return *j.Bodies == "full"
}

// journalMaxBytes returns the effective journal.maxBytes: the set value, or the
// schema default when absent.
func journalMaxBytes(j *scenario.Journal) int {
	if j != nil && j.MaxBytes != nil {
		return *j.MaxBytes
	}
	return defaultJournalMaxBytes
}

// sizeActionBytes extracts the bytes field of a size action from a raw fault
// action body, reporting whether the action is a size action carrying a bytes
// value. It decodes only the two fields it needs, leaving the rest of the
// action (a later-phase shape) untouched.
func sizeActionBytes(action json.RawMessage) (int, bool) {
	if len(action) == 0 {
		return 0, false
	}
	var a struct {
		Kind string `json:"kind"`
		Size *struct {
			Bytes *int `json:"bytes"`
		} `json:"size"`
	}
	if err := json.Unmarshal(action, &a); err != nil {
		return 0, false
	}
	if a.Kind != "size" || a.Size == nil || a.Size.Bytes == nil {
		return 0, false
	}
	return *a.Size.Bytes, true
}
