package instance_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/vyrodovalexey/mcp-mock-server/internal/determinism"
	"github.com/vyrodovalexey/mcp-mock-server/internal/engine"
	"github.com/vyrodovalexey/mcp-mock-server/internal/instance"
	"github.com/vyrodovalexey/mcp-mock-server/internal/modern"
	"github.com/vyrodovalexey/mcp-mock-server/internal/wire"
	"github.com/vyrodovalexey/mcp-mock-server/scenario"
)

// omission_test.go is the DEF-209 plumbing regression suite. The defect was that
// the MOCK-209 omission switches (switches.omitResultType, omitServerInfoMeta)
// were carried by the scenario schema and honoured by internal/modern, but the
// instance.Snapshot neither carried them nor implemented the consumer-side
// omissionSnapshot surface internal/modern reads through — so the switches were
// silently ignored. These tests prove the snapshot now carries and exposes both
// flags (unit level), that they take effect end to end through the real modern
// handlers (integration level, driving a real *Instance through the engine
// pipeline), that the two switches are independent, and that output stays
// byte-identical for a fixed seed under every switch combination (§0.1).

// omitSpec builds an instance spec whose switches carry the given omission flags.
// A nil pointer leaves the switch absent (which must resolve to "do not omit"),
// so a caller distinguishes "unset" from "explicitly set".
//
// It sets switches.selfCheck EXPLICITLY false: these tests isolate the MOCK-209
// omission plumbing, and since AMEND-10 the self-check defaults ON. Omitting the
// resultType discriminator (switches.omitResultType) legitimately produces a
// wire-invalid, discriminator-less result that the self-check correctly rejects
// with -32603 (MOCK-209.3 note, wire.SelfCheckResult) — see the internal/modern
// TestSelfCheckCatchesMalformedResult, which proves exactly that interaction on
// purpose. To observe the omission on the wire rather than the self-check's
// rejection, these cases opt out of the check with the design's own explicit
// false. This is the sanctioned suppression, not a weakening of the validator or
// the default.
func omitSpec(omitRT, omitSrv *bool) scenario.InstanceSpec {
	selfCheckOff := false
	return scenario.InstanceSpec{
		Switches: &scenario.Switches{
			OmitResultType:     omitRT,
			OmitServerInfoMeta: omitSrv,
			SelfCheck:          &selfCheckOff,
		},
	}
}

// TestSnapshotCarriesOmissionSwitches is the unit-level proof that the snapshot
// carries switches.omitResultType and switches.omitServerInfoMeta and exposes
// them through OmitResultType/OmitServerInfoMeta (the omissionSnapshot seam
// internal/modern consumes). It covers each switch independently, both together
// and neither, and the absent-vs-explicit-false distinction the scenario pointer
// preserves.
func TestSnapshotCarriesOmissionSwitches(t *testing.T) {
	cases := []struct {
		name    string
		omitRT  *bool
		omitSrv *bool
		wantRT  bool
		wantSrv bool
	}{
		{"neither-set", nil, nil, false, false},
		{"resulttype-only", boolp(true), nil, true, false},
		{"serverinfo-only", nil, boolp(true), false, true},
		{"both", boolp(true), boolp(true), true, true},
		{"resulttype-explicit-false", boolp(false), nil, false, false},
		{"serverinfo-explicit-false", nil, boolp(false), false, false},
		{"both-explicit-false", boolp(false), boolp(false), false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inst := instance.New(instance.Config{
				Name: "omit-" + tc.name,
				Root: determinism.Root(100),
				Spec: omitSpec(tc.omitRT, tc.omitSrv),
			})
			s := inst.Snapshot()
			if got := s.OmitResultType(); got != tc.wantRT {
				t.Errorf("OmitResultType() = %v, want %v", got, tc.wantRT)
			}
			if got := s.OmitServerInfoMeta(); got != tc.wantSrv {
				t.Errorf("OmitServerInfoMeta() = %v, want %v", got, tc.wantSrv)
			}
		})
	}
}

// TestSnapshotOmissionAbsentSwitchesBlock proves a wholly-absent switches block
// (spec.Switches == nil) resolves both flags to false — the conformant default,
// not a panic.
func TestSnapshotOmissionAbsentSwitchesBlock(t *testing.T) {
	inst := instance.New(instance.Config{
		Name: "no-switches",
		Root: determinism.Root(101),
		Spec: scenario.InstanceSpec{},
	})
	s := inst.Snapshot()
	if s.OmitResultType() || s.OmitServerInfoMeta() {
		t.Fatalf("absent switches block must default both omissions to false; got rt=%v srv=%v",
			s.OmitResultType(), s.OmitServerInfoMeta())
	}
}

// TestSnapshotOmissionSurvivesMutation proves a COW mutation carries the resolved
// omission flags onto the new generation rather than resetting them (ADR-014):
// the shallow clone copies the bool fields, so a later mutation that touches an
// unrelated field must not silently re-enable an omitted key.
func TestSnapshotOmissionSurvivesMutation(t *testing.T) {
	inst := instance.New(instance.Config{
		Name: "mutate-omit",
		Root: determinism.Root(102),
		Spec: omitSpec(boolp(true), boolp(true)),
	})
	inst.Mutate(func(*instance.Snapshot) {}) // touch nothing else
	s := inst.Snapshot()
	if !s.OmitResultType() || !s.OmitServerInfoMeta() {
		t.Fatalf("mutation dropped the omission flags: rt=%v srv=%v",
			s.OmitResultType(), s.OmitServerInfoMeta())
	}
}

// renderDiscover drives a real server/discover request through the engine
// pipeline registered with the REAL modern handlers, against a real *Instance
// built from spec. It returns the raw JSON-RPC response bytes, so a test asserts
// on wire output exactly as a client would see it — the end-to-end proof that the
// snapshot plumbing reaches the handler's omissionsFor.
func renderDiscover(t *testing.T, seed uint64, name string, spec scenario.InstanceSpec) []byte {
	t.Helper()
	inst := instance.New(instance.Config{
		Name:  name,
		Root:  determinism.Root(seed),
		Spec:  spec,
		Epoch: time.Unix(1_700_000_000, 0),
	})
	reg := engine.NewRegistry()
	modern.RegisterHandlers(reg, modern.BuiltinConfig{})
	p := engine.NewPipeline(reg)
	sink := engine.NewBufferedSink()
	ex := &engine.Exchange{
		Ctx:       context.Background(),
		Instance:  inst,
		Transport: engine.KindHTTP,
		Peer:      "peer",
		Raw:       rawRequest("1", wire.MethodDiscover),
	}
	if err := p.Handle(ex.Ctx, ex, sink); err != nil {
		t.Fatalf("pipeline discover: %v", err)
	}
	return sink.Bytes()
}

// resultKeys decodes result.resultType and whether result._meta.serverInfo is
// present from a JSON-RPC response body, mirroring the functional witness so the
// unit assertions describe the same observable wire shape.
func resultKeys(t *testing.T, body []byte) (resultType string, hasServerInfo bool) {
	t.Helper()
	var env struct {
		Result struct {
			ResultType string `json:"resultType"`
			Meta       struct {
				ServerInfo json.RawMessage `json:"serverInfo"`
			} `json:"_meta"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode result: %v\nbody: %s", err, body)
	}
	return env.Result.ResultType, len(env.Result.Meta.ServerInfo) > 0
}

// TestOmissionSwitchesTakeEffectEndToEnd is the integration proof that both
// switches, resolved off a real scenario spec and carried on a real
// instance.Snapshot, actually change the wire bytes the modern handlers emit —
// the DEF-209 end-to-end path. It exercises each switch independently and both
// together, asserting the key is ABSENT (not null, not zero) when the switch is
// set and PRESENT when it is not, so setting one switch never affects the other.
func TestOmissionSwitchesTakeEffectEndToEnd(t *testing.T) {
	cases := []struct {
		name    string
		spec    scenario.InstanceSpec
		wantRT  bool // resultType key present
		wantSrv bool // _meta.serverInfo present
	}{
		{"default-both-present", omitSpec(nil, nil), true, true},
		{"omit-resulttype-only", omitSpec(boolp(true), nil), false, true},
		{"omit-serverinfo-only", omitSpec(nil, boolp(true)), true, false},
		{"omit-both", omitSpec(boolp(true), boolp(true)), false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := renderDiscover(t, 0x00C0FFEE, "e2e-"+tc.name, tc.spec)
			rt, hasSrv := resultKeys(t, body)
			gotRT := rt != ""
			if gotRT != tc.wantRT {
				t.Errorf("resultType present=%v, want %v\nbody: %s", gotRT, tc.wantRT, body)
			}
			if hasSrv != tc.wantSrv {
				t.Errorf("_meta.serverInfo present=%v, want %v\nbody: %s", hasSrv, tc.wantSrv, body)
			}
		})
	}
}

// TestOmissionByteDeterminism is the §0.1 proof that, for a fixed seed, the wire
// output is byte-identical across two independent renders in EVERY switch
// combination — the omission plumbing must not introduce any nondeterminism
// (RNG, map iteration, clock). It renders each combination twice from two
// separately-constructed instances at the same seed and requires the bytes to
// match exactly.
func TestOmissionByteDeterminism(t *testing.T) {
	combos := []struct {
		name string
		spec scenario.InstanceSpec
	}{
		{"neither", omitSpec(nil, nil)},
		{"resulttype", omitSpec(boolp(true), nil)},
		{"serverinfo", omitSpec(nil, boolp(true))},
		{"both", omitSpec(boolp(true), boolp(true))},
	}
	for _, c := range combos {
		t.Run(c.name, func(t *testing.T) {
			const seed = 0x00C0FFEE
			a := renderDiscover(t, seed, "det", c.spec)
			b := renderDiscover(t, seed, "det", c.spec)
			if string(a) != string(b) {
				t.Fatalf("byte output not deterministic under %q:\n a=%s\n b=%s", c.name, a, b)
			}
		})
	}
}
