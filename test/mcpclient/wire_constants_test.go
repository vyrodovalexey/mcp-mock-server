package mcpclient

import "testing"

// TestAnnexParity asserts that the Phase 1 constant set transcribed in
// wire_constants.go matches the annex Phase 1 surface exactly. It is the parity
// gate required by TASK-008 acceptance criterion 2: if a constant drifts from
// the annex tables, or an annex Phase 1 item loses its constant, this fails.
//
// The expected values below are transcribed a SECOND time, independently, from
// the annex prose — deliberately not referencing the package constants — so a
// single typo does not agree with itself.
func TestAnnexParity(t *testing.T) {
	t.Run("methods", func(t *testing.T) {
		// annex §4/§5, Phase 1 subset (AMEND-3).
		want := map[string]bool{
			"server/discover": true, // annex §5 [R MOCK-201]
			"tools/list":      true, // annex §4 [R MOCK-202]
			"tools/call":      true, // annex §4 [R MOCK-202]
		}
		got := phase1Methods()
		if len(got) != len(want) {
			t.Fatalf("method count: got %d, want %d", len(got), len(want))
		}
		for _, m := range got {
			if !want[m] {
				t.Errorf("method %q is not in the annex Phase 1 set", m)
			}
			delete(want, m)
		}
		for m := range want {
			t.Errorf("annex Phase 1 method %q has no constant", m)
		}
	})

	t.Run("resultTypes", func(t *testing.T) {
		// annex 4.2 [P-21], Phase 1 subset (3 of 11).
		want := map[string]bool{"discovery": true, "toolList": true, "toolResult": true}
		got := phase1ResultTypes()
		if len(got) != len(want) {
			t.Fatalf("resultType count: got %d, want %d", len(got), len(want))
		}
		for _, r := range got {
			if !want[r] {
				t.Errorf("resultType %q is not in the annex Phase 1 set", r)
			}
		}
	})

	t.Run("errorCodes", func(t *testing.T) {
		// annex §6, Phase 1 subset. -32020/-32021/-32022 are Phase 2 and MUST
		// be absent here.
		want := map[int]string{
			-32700: "parse",
			-32600: "invalidRequest",
			-32601: "methodNotFound",
			-32602: "invalidParams",
			-32603: "internal",
		}
		got := phase1ErrorCodes()
		if len(got) != len(want) {
			t.Fatalf("error-code count: got %d, want %d", len(got), len(want))
		}
		for _, c := range got {
			if _, ok := want[c]; !ok {
				t.Errorf("error code %d is not in the annex Phase 1 set", c)
			}
		}
		// Guard against a Phase 2 code leaking into Phase 1.
		for _, forbidden := range []int{-32020, -32021, -32022} {
			for _, c := range got {
				if c == forbidden {
					t.Errorf("Phase 2 error code %d must not appear in the Phase 1 set", forbidden)
				}
			}
		}
	})

	t.Run("scalarConstants", func(t *testing.T) {
		cases := []struct{ name, got, want string }{
			{"ProtocolRevision", ProtocolRevision, "2026-07-28"}, // annex §0
			{"JSONRPCVersion", JSONRPCVersion, "2.0"},            // annex 2.1
			{"MetaKey", MetaKey, "_meta"},                        // annex 2.5 [P-06]
			{"MetaKeyProtocolVersion", MetaKeyProtocolVersion, "protocolVersion"},
			{"MetaKeyClientCapabilities", MetaKeyClientCapabilities, "clientCapabilities"},
			{"MetaKeyParams", MetaKeyParams, "params"},
			{"ContentTypeJSON", ContentTypeJSON, "application/json"},
			{"HeaderMCPProtocolVersion", HeaderMCPProtocolVersion, "MCP-Protocol-Version"},
		}
		for _, c := range cases {
			if c.got != c.want {
				t.Errorf("%s: got %q, want %q (annex drift)", c.name, c.got, c.want)
			}
		}
	})
}
