package wire

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// selfcheck_test.go proves the MOCK-201.4 outgoing-result self-check
// ([SelfCheckResult]) accepts every well-formed Phase 1 result and rejects a
// representative set of malformed ones, and that its accept/reject decision
// agrees with the AMEND-4 schema's phase1Result constraints (the schema is the
// oracle; TestSelfCheckAgreesWithSchema loads it live and cross-checks).

// wellFormed returns byte forms of conformant results, one per Phase 1 shape,
// built through the real emitters so the test exercises exactly what a handler
// would produce.
func wellFormedResults(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	si := &ServerInfo{Name: "mcpmock", Version: "0.1.0"}
	out := map[string]json.RawMessage{}
	for name, v := range map[string]any{
		"discover": DiscoverResult{
			ResultType:        strptr(ResultTypeDiscovery),
			SupportedVersions: []string{ProtocolRevision},
			Instructions:      strptr("hi"),
			Meta:              &ResultMeta{ServerInfo: si},
		},
		"discover-minimal": DiscoverResult{ResultType: strptr(ResultTypeDiscovery)},
		"toolList": ToolListResult{
			ResultType: strptr(ResultTypeToolList),
			Tools: []ToolDescriptor{
				{Name: "echo", InputSchema: json.RawMessage(`{"type":"object"}`)},
			},
			Meta: &ResultMeta{ServerInfo: si},
		},
		"toolList-empty": ToolListResult{
			ResultType: strptr(ResultTypeToolList),
			Tools:      []ToolDescriptor{},
			Meta:       &ResultMeta{ServerInfo: si},
		},
		"toolCall": ToolCallResult{
			ResultType: strptr(ResultTypeToolResult),
			Content:    []TextContentBlock{NewTextContentBlock("{}")},
			Meta:       &ResultMeta{ServerInfo: si},
		},
		"toolCall-error": ToolCallResult{
			ResultType: strptr(ResultTypeToolResult),
			Content:    []TextContentBlock{NewTextContentBlock("boom")},
			IsError:    boolptr(true),
		},
	} {
		b, err := MarshalResult(v)
		if err != nil {
			t.Fatalf("marshal %s: %v", name, err)
		}
		out[name] = b
	}
	return out
}

// TestSelfCheckAcceptsWellFormed proves every conformant Phase 1 result passes
// the self-check with no error.
func TestSelfCheckAcceptsWellFormed(t *testing.T) {
	for name, body := range wellFormedResults(t) {
		t.Run(name, func(t *testing.T) {
			if err := SelfCheckResult(body); err != nil {
				t.Errorf("well-formed %s rejected: %v\nbody: %s", name, err, body)
			}
		})
	}
}

// TestSelfCheckRejectsMalformed proves a deliberately malformed result is caught
// — the core DEF-010 guarantee that the self-check ACTUALLY REJECTS rather than
// merely logging. Each case violates a distinct phase1Result constraint.
func TestSelfCheckRejectsMalformed(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string // substring the reason must contain
	}{
		{"not-an-object", `[]`, "not a JSON object"},
		{"no-resultType", `{"tools":[]}`, "discriminator"},
		{"unknown-resultType", `{"resultType":"promptList"}`, "unknown resultType"},
		{
			"discover-extra-member (Phase 2 surface leak)",
			`{"resultType":"discovery","nextCursor":"x"}`,
			"additionalProperties:false",
		},
		{
			"toolList-missing-tools",
			`{"resultType":"toolList"}`,
			"missing required member",
		},
		{
			"toolList-nextCursor (premature pagination)",
			`{"resultType":"toolList","tools":[],"nextCursor":"abc"}`,
			"additionalProperties:false",
		},
		{
			"toolCall-empty-content",
			`{"resultType":"toolResult","content":[]}`,
			"content is empty",
		},
		{
			"toolCall-nontext-block (Phase 2 content type)",
			`{"resultType":"toolResult","content":[{"type":"image","data":"x"}]}`,
			"text",
		},
		{
			"toolCall-isError-false (const:true violation)",
			`{"resultType":"toolResult","content":[{"type":"text","text":"x"}],"isError":false}`,
			"isError must be true",
		},
		{
			"serverInfo-missing-version",
			`{"resultType":"discovery","_meta":{"serverInfo":{"name":"x"}}}`,
			"missing required member",
		},
		{
			"serverInfo-extra-member",
			`{"resultType":"discovery","_meta":{"serverInfo":{"name":"x","version":"1","extra":true}}}`,
			"additionalProperties:false",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := SelfCheckResult(json.RawMessage(tc.body))
			if err == nil {
				t.Fatalf("malformed result NOT rejected: %s", tc.body)
			}
			if !errors.Is(err, ErrSelfCheck) {
				t.Errorf("error does not wrap ErrSelfCheck: %v", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("reason %q does not mention %q", err.Error(), tc.want)
			}
		})
	}
}

// TestSelfCheckSchemaRefNamesProvisional proves the schema reference the failure
// mode names carries the GAP-003 provisional marker, so a reader of a self-check
// failure (log or journalled reason) never mistakes a passing check for a
// conformance claim.
func TestSelfCheckSchemaRefNamesProvisional(t *testing.T) {
	if !strings.Contains(SchemaRef, "PROVISIONAL") || !strings.Contains(SchemaRef, "GAP-003") {
		t.Errorf("SchemaRef %q must mark the oracle PROVISIONAL and cite GAP-003", SchemaRef)
	}
	if !strings.Contains(SchemaRef, "phase1Result") {
		t.Errorf("SchemaRef %q must name the phase1Result entry point", SchemaRef)
	}
}

// TestSelfCheckAgreesWithSchema is the oracle-parity proof: for each well-formed
// result and each malformed result, the self-check's accept/reject decision must
// match what the AMEND-4 schema's structural constraints say (loaded live via the
// same shallow schema reader schema_test uses). This is what keeps the in-Go
// validator honest against the authored annex it claims to enforce.
func TestSelfCheckAgreesWithSchema(t *testing.T) {
	defs := loadSchemaDefs(t) // skips when the specification tree is absent

	// Well-formed results must validate against their $def AND pass the self-check.
	for name, body := range wellFormedResults(t) {
		t.Run("accept/"+name, func(t *testing.T) {
			def := defForResult(t, body)
			validateAgainstDef(t, defs, def, body) // schema agrees it is valid
			if err := SelfCheckResult(body); err != nil {
				t.Errorf("self-check rejected a schema-valid result: %v", err)
			}
		})
	}

	// A leaked Phase 2 member (nextCursor) must be rejected by BOTH the schema
	// (additionalProperties:false) and the self-check — the "absent, not
	// permissive" discipline enforced identically at both layers.
	leak := json.RawMessage(`{"resultType":"toolList","tools":[],"nextCursor":"x"}`)
	if err := SelfCheckResult(leak); err == nil {
		t.Error("self-check accepted a Phase 2 nextCursor leak the schema forbids")
	}
	if schemaAcceptsClosedMember(t, defs, "toolListResult", leak) {
		t.Error("schema unexpectedly accepted nextCursor; oracle drift")
	}
}

// defForResult returns the schema $def name for a result body, keyed by its
// resultType discriminator, so the parity test validates each body against the
// right $def.
func defForResult(t *testing.T, body json.RawMessage) string {
	t.Helper()
	var env struct {
		ResultType string `json:"resultType"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode resultType: %v", err)
	}
	switch env.ResultType {
	case ResultTypeDiscovery:
		return "discoverResult"
	case ResultTypeToolList:
		return "toolListResult"
	case ResultTypeToolResult:
		return "toolCallResult"
	default:
		t.Fatalf("unknown resultType %q", env.ResultType)
		return ""
	}
}

// schemaAcceptsClosedMember reports whether the named $def would accept every
// member of data under its additionalProperties rule — used to confirm the schema
// itself rejects a leaked member, so the parity test is checking real drift, not
// a tautology.
func schemaAcceptsClosedMember(t *testing.T, defs map[string]json.RawMessage, defName string, data []byte) bool {
	t.Helper()
	var sch objectSchema
	if err := json.Unmarshal(defs[defName], &sch); err != nil {
		t.Fatalf("read $def %q: %v", defName, err)
	}
	if sch.AdditionalProperties != nil && !*sch.AdditionalProperties {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(data, &obj); err != nil {
			t.Fatalf("decode data: %v", err)
		}
		for k := range obj {
			if _, ok := sch.Properties[k]; !ok {
				return false // schema rejects this member
			}
		}
	}
	return true
}
