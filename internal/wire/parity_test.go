package wire

import "testing"

// TestAnnexParity asserts that the Phase 1 vocabulary defined in this package
// matches the wire annex's Phase 1 surface exactly (TASK-010 acceptance
// criterion 3): every [P-nn]/[R]/[D] item the annex tables assign to Phase 1
// has a corresponding constant here, and no constant drifts from its annex
// value.
//
// The expected values below are transcribed a SECOND time, independently, from
// the annex prose (wire-2026-07-28.md) — deliberately NOT referencing the
// package constants — so a single typo cannot agree with itself. This mirrors
// the independent transcription in test/mcpclient (ADR-017): if the two
// transcriptions disagree, that disagreement is a feature that catches a bug,
// and it is never resolved by making either import the other.
func TestAnnexParity(t *testing.T) {
	t.Run("methods", func(t *testing.T) {
		// annex §5 (discover) / §4 (tools), Phase 1 subset (AMEND-3 split table).
		want := map[string]bool{
			"server/discover": true, // annex §5 [R MOCK-201]
			"tools/list":      true, // annex §4 [R MOCK-202]
			"tools/call":      true, // annex §4 [R MOCK-202]
		}
		got := Phase1Methods()
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
		got := Phase1ResultTypes()
		if len(got) != len(want) {
			t.Fatalf("resultType count: got %d, want %d", len(got), len(want))
		}
		for _, r := range got {
			if !want[r] {
				t.Errorf("resultType %q is not in the annex Phase 1 set", r)
			}
		}
		// The eight Phase 2 resultType values MUST NOT be reachable in Phase 1.
		phase2 := []string{
			"promptList", "promptResult", "resourceList", "resourceTemplateList",
			"resourceContents", "completion", "inputRequired", "complete",
		}
		for _, forbidden := range phase2 {
			for _, r := range got {
				if r == forbidden {
					t.Errorf("Phase 2 resultType %q must not appear in the Phase 1 set", forbidden)
				}
			}
		}
	})

	t.Run("errorCodes", func(t *testing.T) {
		// annex §6, Phase 1 subset. -32020/-32021/-32022 are Phase 2 and MUST
		// be absent.
		want := map[int]string{
			-32700: "parse",
			-32600: "invalidRequest",
			-32601: "methodNotFound",
			-32602: "invalidParams",
			-32603: "internal",
		}
		got := Phase1ErrorCodes()
		if len(got) != len(want) {
			t.Fatalf("error-code count: got %d, want %d", len(got), len(want))
		}
		for _, c := range got {
			if _, ok := want[c]; !ok {
				t.Errorf("error code %d is not in the annex Phase 1 set", c)
			}
		}
		for _, forbidden := range []int{-32020, -32021, -32022} {
			for _, c := range got {
				if c == forbidden {
					t.Errorf("Phase 2 error code %d must not appear in the Phase 1 set", forbidden)
				}
			}
		}
	})

	t.Run("requiredMetaFields", func(t *testing.T) {
		// annex 2.3 / 2.4, the two fields whose presence is required (strict) or
		// recorded (lenient). Matches the schema metaMissingErrorData enum.
		want := map[string]bool{"protocolVersion": true, "clientCapabilities": true}
		got := RequiredMetaFields()
		if len(got) != len(want) {
			t.Fatalf("required _meta field count: got %d, want %d", len(got), len(want))
		}
		for _, f := range got {
			if !want[f] {
				t.Errorf("required _meta field %q is not in the annex Phase 1 set", f)
			}
		}
	})

	t.Run("scalarConstants", func(t *testing.T) {
		cases := []struct{ name, got, want string }{
			{"ProtocolRevision", ProtocolRevision, "2026-07-28"},                           // annex §0 [R]
			{"JSONRPCVersion", JSONRPCVersion, "2.0"},                                      // annex 2.1 [D]
			{"ParamsKey", ParamsKey, "params"},                                             // annex 2.5 [D]
			{"MetaKey", MetaKey, "_meta"},                                                  // annex 2.5 [P-06]
			{"MetaKeyProtocolVersion", MetaKeyProtocolVersion, "protocolVersion"},          // 2.3 [R]
			{"MetaKeyClientCapabilities", MetaKeyClientCapabilities, "clientCapabilities"}, // 2.4 [R]
			{"MetaKeyClientInfo", MetaKeyClientInfo, "clientInfo"},                         // 2.6 [P-07]
			{"ParamsKeyName", ParamsKeyName, "name"},                                       // 3.3 [P-12]
			{"ParamsKeyArguments", ParamsKeyArguments, "arguments"},                        // builtin 1.3 [D]
			{"ResultTypeDiscovery", ResultTypeDiscovery, "discovery"},                      // 4.2 [P-21]
			{"ResultTypeToolList", ResultTypeToolList, "toolList"},                         // 4.2 [P-21]
			{"ResultTypeToolResult", ResultTypeToolResult, "toolResult"},                   // 4.2 [P-21]
			{"ErrDataKeyMissing", ErrDataKeyMissing, "missing"},                            // 2.12 [P-11]
			{"ContentTypeText", ContentTypeText, "text"},                                   // builtin 1.2 [P-40]
			{"MetaModeStrict", MetaModeStrict, "strict"},                                   // MOCK-203.5
			{"MetaModeLenient", MetaModeLenient, "lenient"},                                // MOCK-203.5
			{"MetaModeOff", MetaModeOff, "off"},                                            // MOCK-203.5
		}
		for _, c := range cases {
			if c.got != c.want {
				t.Errorf("%s: got %q, want %q (annex drift)", c.name, c.got, c.want)
			}
		}
	})

	t.Run("errorCodesAgreeWithJSONRPC", func(t *testing.T) {
		// The five Phase 1 codes are the standard JSON-RPC codes, restated here
		// for ADR-019 containment. They must equal their well-known values (and
		// therefore internal/jsonrpc's), or a handler naming wire.ErrCode* and
		// one naming jsonrpc.Code* would disagree.
		if ErrCodeParse != -32700 || ErrCodeInvalidRequest != -32600 ||
			ErrCodeMethodNotFound != -32601 || ErrCodeInvalidParams != -32602 ||
			ErrCodeInternal != -32603 {
			t.Errorf("Phase 1 error codes drifted from the JSON-RPC 2.0 standard values")
		}
	})
}

// TestResultTypeTableIsData asserts acceptance criterion 2: resultType is looked
// up through a table keyed by method, so adding a method is a table entry rather
// than a code change. It proves the lookup, the unknown-method signal, and that
// the Phase 2 methods are absent from the table (not merely mapped to "").
func TestResultTypeTableIsData(t *testing.T) {
	cases := []struct {
		method   string
		wantType string
		wantOK   bool
	}{
		{MethodDiscover, ResultTypeDiscovery, true},
		{MethodToolsList, ResultTypeToolList, true},
		{MethodToolsCall, ResultTypeToolResult, true},
		// Phase 2 methods have no entry: absent, not permissive.
		{"prompts/list", "", false},
		{"prompts/get", "", false},
		{"resources/list", "", false},
		{"resources/read", "", false},
		{"completion/complete", "", false},
		{"subscriptions/listen", "", false},
		{"", "", false},
		{"totally/unknown", "", false},
	}
	for _, c := range cases {
		got, ok := ResultTypeForMethod(c.method)
		if got != c.wantType || ok != c.wantOK {
			t.Errorf("ResultTypeForMethod(%q) = (%q, %v), want (%q, %v)",
				c.method, got, ok, c.wantType, c.wantOK)
		}
	}
}
