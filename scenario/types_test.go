package scenario_test

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/vyrodovalexey/mcp-mock-server/scenario"
)

// TestAbsentVsZero_Scalar proves the TASK-007 precondition: a field that is
// absent (nil pointer) is distinguishable from a field deliberately set to its
// zero value (non-nil pointer to the zero). This is what lets an overlay turn a
// base's boolean off, or set a count to 0, without the merge treating it as
// "unset" and re-inheriting the base.
func TestAbsentVsZero_Scalar(t *testing.T) {
	// journal.enabled absent.
	var absent scenario.Journal
	if err := json.Unmarshal([]byte(`{}`), &absent); err != nil {
		t.Fatalf("unmarshal absent: %v", err)
	}
	if absent.Enabled != nil {
		t.Errorf("absent journal.enabled should be nil, got %v", *absent.Enabled)
	}

	// journal.enabled explicitly false — must be a non-nil pointer to false,
	// NOT nil. If these two collapsed, an overlay could not turn journalling
	// off.
	var zero scenario.Journal
	if err := json.Unmarshal([]byte(`{"enabled": false}`), &zero); err != nil {
		t.Fatalf("unmarshal zero: %v", err)
	}
	if zero.Enabled == nil {
		t.Fatal("explicit journal.enabled:false decoded to nil — absent and zero collapsed")
	}
	if *zero.Enabled != false {
		t.Errorf("journal.enabled = %v, want false", *zero.Enabled)
	}

	// generated.count set to 0 explicitly must be distinguishable from absent.
	var g scenario.Generated
	if err := json.Unmarshal([]byte(`{"count": 0}`), &g); err != nil {
		t.Fatalf("unmarshal count:0: %v", err)
	}
	if g.Count == nil {
		t.Fatal("explicit count:0 decoded to nil — absent and zero collapsed")
	}
	if *g.Count != 0 {
		t.Errorf("count = %d, want 0", *g.Count)
	}
}

// TestAbsentVsZero_Block proves an absent nested block (nil pointer) is
// distinguishable from a present-but-empty block.
func TestAbsentVsZero_Block(t *testing.T) {
	var absent scenario.InstanceSpec
	if err := json.Unmarshal([]byte(`{}`), &absent); err != nil {
		t.Fatalf("unmarshal absent spec: %v", err)
	}
	if absent.Journal != nil {
		t.Error("absent journal block should be nil")
	}

	var present scenario.InstanceSpec
	if err := json.Unmarshal([]byte(`{"journal": {}}`), &present); err != nil {
		t.Fatalf("unmarshal present spec: %v", err)
	}
	if present.Journal == nil {
		t.Fatal("present-but-empty journal block should be non-nil")
	}
}

// TestRawMessage_RetainedVerbatim proves an authored inputSchema is retained
// byte-for-byte including key order (MOCK-701.7 / MOCK-222.3). A round trip
// through the typed struct must not reorder or normalise the raw payload.
func TestRawMessage_RetainedVerbatim(t *testing.T) {
	// Key order zeta-before-alpha is deliberately non-alphabetical; if the
	// loader normalised object keys, this would come back sorted.
	in := `{"name":"t","inputSchema":{"zeta":1,"alpha":2,"nested":{"y":true,"x":false}}}`
	var item scenario.AuthoredItem
	if err := json.Unmarshal([]byte(in), &item); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	want := `{"zeta":1,"alpha":2,"nested":{"y":true,"x":false}}`
	if string(item.InputSchema) != want {
		t.Errorf("inputSchema not retained verbatim:\n got %s\n want %s", item.InputSchema, want)
	}
}

// TestDeterministicIteration proves that iterating a loaded switches.methods map
// in sorted-key order is stable and does not leak Go's randomised map
// iteration into output (§0.1). Callers that need order sort the keys; this
// test documents and locks that contract.
func TestDeterministicIteration(t *testing.T) {
	in := `{"methods":{"tools/call":{"enabled":true},"tools/list":{"enabled":false},"server/discover":{}}}`
	var sw scenario.Switches
	if err := json.Unmarshal([]byte(in), &sw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	order := func() []string {
		keys := make([]string, 0, len(sw.Methods))
		for k := range sw.Methods {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return keys
	}

	first := order()
	// Repeat many times; a hand-rolled range would vary run to run, sorted
	// keys never do.
	for i := 0; i < 100; i++ {
		got := order()
		if len(got) != len(first) {
			t.Fatalf("iteration length changed: %d vs %d", len(got), len(first))
		}
		for j := range got {
			if got[j] != first[j] {
				t.Fatalf("iteration order not deterministic at %d: %v vs %v", j, got, first)
			}
		}
	}
	want := []string{"server/discover", "tools/call", "tools/list"}
	for i := range want {
		if first[i] != want[i] {
			t.Fatalf("sorted order = %v, want %v", first, want)
		}
	}
}

// TestDocument_SpecDispatch proves ScenarioSpec / FleetSpec decode the raw spec
// according to kind, and that a nil spec is the zero value rather than an error.
func TestDocument_SpecDispatch(t *testing.T) {
	scen := scenario.Document{
		APIVersion: scenario.APIVersion,
		Kind:       scenario.KindScenario,
		Spec:       json.RawMessage(`{"era":"legacy"}`),
	}
	spec, err := scen.ScenarioSpec()
	if err != nil {
		t.Fatalf("ScenarioSpec: %v", err)
	}
	if spec.Era == nil || *spec.Era != "legacy" {
		t.Errorf("era not decoded: %+v", spec.Era)
	}

	empty := scenario.Document{Kind: scenario.KindScenario}
	if _, err := empty.ScenarioSpec(); err != nil {
		t.Errorf("nil spec should decode to zero value, got %v", err)
	}
}
