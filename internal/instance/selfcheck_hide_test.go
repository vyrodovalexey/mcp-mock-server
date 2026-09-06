package instance_test

import (
	"testing"

	"github.com/vyrodovalexey/mcp-mock-server/internal/determinism"
	"github.com/vyrodovalexey/mcp-mock-server/internal/instance"
	"github.com/vyrodovalexey/mcp-mock-server/internal/wire"
	"github.com/vyrodovalexey/mcp-mock-server/scenario"
)

// selfcheck_hide_test.go is the DEF-010/DEF-011 plumbing regression suite, the
// twin of omission_test.go (DEF-209). Both defects were the same shape: a switch
// carried by the scenario schema but never reaching the consumer, because the
// instance.Snapshot neither carried it nor exposed the consumer-side surface
// internal/modern reads through. These tests prove the snapshot now carries and
// exposes switches.selfCheck (MOCK-201.4, DEF-010) and the per-method
// switches.methods.<name>.hideFromCapabilities set (MOCK-202.3, DEF-011), at the
// unit level: the end-to-end handler behaviour is proven in internal/modern.

// selfCheckSpec builds a spec whose switches carry the given selfCheck flag. A
// nil pointer leaves it absent, so a caller distinguishes "unset" from
// "explicitly false".
func selfCheckSpec(selfCheck *bool) scenario.InstanceSpec {
	return scenario.InstanceSpec{Switches: &scenario.Switches{SelfCheck: selfCheck}}
}

// TestSnapshotCarriesSelfCheck proves the snapshot resolves switches.selfCheck to
// SelfCheck() with the absent-vs-explicit-false idiom and an ON default
// (AMEND-10): an absent switch resolves to true, and only an explicit false
// disables it, so switches-at-defaults produce self-check-passing responses
// (PRIN-5).
func TestSnapshotCarriesSelfCheck(t *testing.T) {
	cases := []struct {
		name string
		flag *bool
		want bool
	}{
		{"absent", nil, true},
		{"explicit-false", boolp(false), false},
		{"explicit-true", boolp(true), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inst := instance.New(instance.Config{
				Name: "selfcheck-" + tc.name,
				Root: determinism.Root(200),
				Spec: selfCheckSpec(tc.flag),
			})
			if got := inst.Snapshot().SelfCheck(); got != tc.want {
				t.Errorf("SelfCheck() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestSnapshotSelfCheckAbsentSwitchesBlock proves a wholly-absent switches block
// resolves SelfCheck() to true — the AMEND-10 ON default (PRIN-5), not a panic.
func TestSnapshotSelfCheckAbsentSwitchesBlock(t *testing.T) {
	inst := instance.New(instance.Config{
		Name: "no-switches-selfcheck",
		Root: determinism.Root(201),
		Spec: scenario.InstanceSpec{},
	})
	if !inst.Snapshot().SelfCheck() {
		t.Fatal("absent switches block must default SelfCheck() to true (AMEND-10)")
	}
}

// TestSnapshotSelfCheckSurvivesMutation proves a COW mutation carries the
// resolved selfCheck flag onto the new generation (ADR-014), so an unrelated
// mutation cannot silently disable the self-check.
func TestSnapshotSelfCheckSurvivesMutation(t *testing.T) {
	inst := instance.New(instance.Config{
		Name: "mutate-selfcheck",
		Root: determinism.Root(202),
		Spec: selfCheckSpec(boolp(true)),
	})
	inst.Mutate(func(*instance.Snapshot) {})
	if !inst.Snapshot().SelfCheck() {
		t.Fatal("mutation dropped the selfCheck flag")
	}
}

// hideSpec builds a spec whose per-method switches set enabled and
// hideFromCapabilities for the named method independently, so a test can express
// every combination of the two.
func hideSpec(method string, enabled, hide *bool) scenario.InstanceSpec {
	return scenario.InstanceSpec{
		Switches: &scenario.Switches{
			Methods: map[string]scenario.MethodSwitch{
				method: {Enabled: enabled, HideFromCapabilities: hide},
			},
		},
	}
}

// TestSnapshotCarriesHiddenCapabilities proves the snapshot resolves the sorted
// hidden-capabilities set from switches.methods.<name>.hideFromCapabilities
// (MOCK-202.3), applying the absent/false idiom: only an explicit true adds a
// method to the hidden set.
func TestSnapshotCarriesHiddenCapabilities(t *testing.T) {
	inst := instance.New(instance.Config{
		Name: "hide-basic",
		Root: determinism.Root(210),
		Spec: hideSpec(wire.MethodToolsList, nil, boolp(true)),
	})
	got := inst.Snapshot().HiddenCapabilities()
	if len(got) != 1 || got[0] != wire.MethodToolsList {
		t.Fatalf("HiddenCapabilities() = %v, want [%q]", got, wire.MethodToolsList)
	}
}

// TestSnapshotHiddenCapabilitiesAbsentOrFalse proves a method with an absent or
// explicitly-false hideFromCapabilities contributes nothing to the hidden set,
// so the default is "advertise everything".
func TestSnapshotHiddenCapabilitiesAbsentOrFalse(t *testing.T) {
	for _, tc := range []struct {
		name string
		hide *bool
	}{
		{"absent", nil},
		{"explicit-false", boolp(false)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inst := instance.New(instance.Config{
				Name: "hide-none-" + tc.name,
				Root: determinism.Root(211),
				Spec: hideSpec(wire.MethodToolsList, nil, tc.hide),
			})
			if got := inst.Snapshot().HiddenCapabilities(); got != nil {
				t.Fatalf("HiddenCapabilities() = %v, want nil", got)
			}
		})
	}
}

// TestSnapshotHiddenCapabilitiesSorted proves the hidden set is returned sorted,
// which is what keeps the discover handler's capabilities filtering deterministic
// (ADR-003) regardless of Go's randomized map iteration over the switches.
func TestSnapshotHiddenCapabilitiesSorted(t *testing.T) {
	inst := instance.New(instance.Config{
		Name: "hide-sorted",
		Root: determinism.Root(212),
		Spec: scenario.InstanceSpec{
			Switches: &scenario.Switches{
				Methods: map[string]scenario.MethodSwitch{
					wire.MethodToolsList: {HideFromCapabilities: boolp(true)},
					wire.MethodDiscover:  {HideFromCapabilities: boolp(true)},
					wire.MethodToolsCall: {HideFromCapabilities: boolp(true)},
				},
			},
		},
	})
	got := inst.Snapshot().HiddenCapabilities()
	// server/discover < tools/call < tools/list lexicographically.
	want := []string{wire.MethodDiscover, wire.MethodToolsCall, wire.MethodToolsList}
	if len(got) != len(want) {
		t.Fatalf("HiddenCapabilities() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("HiddenCapabilities() = %v, want sorted %v", got, want)
		}
	}
}

// TestHideAndEnableAreIndependent is the MOCK-202.3 independence proof at the
// snapshot level: hiding a method from capabilities and disabling it are resolved
// from separate fields, so all four combinations — advertised+enabled,
// advertised+disabled, hidden+enabled, hidden+disabled — are expressible.
func TestHideAndEnableAreIndependent(t *testing.T) {
	cases := []struct {
		name        string
		enabled     *bool
		hide        *bool
		wantEnabled bool
		wantHidden  bool
	}{
		{"advertised-enabled", nil, nil, true, false},
		{"advertised-disabled", boolp(false), nil, false, false},
		{"hidden-enabled", nil, boolp(true), true, true},
		{"hidden-disabled", boolp(false), boolp(true), false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inst := instance.New(instance.Config{
				Name: "indep-" + tc.name,
				Root: determinism.Root(213),
				Spec: hideSpec(wire.MethodToolsList, tc.enabled, tc.hide),
			})
			s := inst.Snapshot()
			if got := s.MethodEnabled(wire.MethodToolsList); got != tc.wantEnabled {
				t.Errorf("MethodEnabled = %v, want %v", got, tc.wantEnabled)
			}
			hidden := len(s.HiddenCapabilities()) == 1 && s.HiddenCapabilities()[0] == wire.MethodToolsList
			if hidden != tc.wantHidden {
				t.Errorf("hidden = %v, want %v (set=%v)", hidden, tc.wantHidden, s.HiddenCapabilities())
			}
		})
	}
}
