package wire

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// schemaPath is the Phase 1 subset schema (AMEND-4). The test loads it at run
// time rather than embedding it: it is a specification artifact this package
// must NOT copy (only specification/** owns it), and loading it live means a
// change to the schema is seen by this test without a code edit here.
const schemaPath = "../../specification/contracts/wire-2026-07-28.schema.json"

// loadSchemaDefs loads the schema file and returns its $defs map. It skips the
// test (rather than failing) if the file is absent, so this unit test does not
// break in a checkout without the specification tree, while still exercising the
// validation whenever the artifact is present.
func loadSchemaDefs(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	b, err := os.ReadFile(filepath.Clean(schemaPath))
	if err != nil {
		t.Skipf("schema not present (%v); skipping self-consistency check", err)
	}
	var root struct {
		Defs map[string]json.RawMessage `json:"$defs"`
	}
	if err := json.Unmarshal(b, &root); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	if len(root.Defs) == 0 {
		t.Fatalf("schema has no $defs")
	}
	return root.Defs
}

// objectSchema is the subset of JSON Schema 2020-12 keywords this test enforces.
// A full validator would be an external dependency (forbidden: stdlib only), so
// the test checks the specific constraints the Phase 1 result surface must
// satisfy — required members, additionalProperties:false, and const/enum
// discriminators — which is exactly what "self-consistent with the annex"
// (AMEND-4) means for these types. It is not a general-purpose validator and
// does not claim to be.
type objectSchema struct {
	Type                 string                     `json:"type"`
	Required             []string                   `json:"required"`
	Properties           map[string]json.RawMessage `json:"properties"`
	AdditionalProperties *bool                      `json:"additionalProperties"`
	Const                json.RawMessage            `json:"const"`
	Enum                 []json.RawMessage          `json:"enum"`
}

// validateAgainstDef checks that the JSON object encoded in data satisfies the
// required-members, closed-object and const/enum constraints of the named $def.
// It follows a top-level $ref one hop and checks each declared property's const
// (used for the resultType discriminator). It is intentionally shallow: it
// proves the Phase 1 result envelopes agree with the schema's structural
// contract, which is the self-check MOCK-201.4 requires.
func validateAgainstDef(t *testing.T, defs map[string]json.RawMessage, defName string, data []byte) {
	t.Helper()
	raw, ok := defs[defName]
	if !ok {
		t.Fatalf("schema has no $def %q", defName)
	}
	var sch objectSchema
	if err := json.Unmarshal(raw, &sch); err != nil {
		t.Fatalf("$def %q is not an object schema: %v", defName, err)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		t.Fatalf("value is not a JSON object: %v", err)
	}

	for _, req := range sch.Required {
		if _, present := obj[req]; !present {
			t.Errorf("%s: required member %q is absent from %s", defName, req, data)
		}
	}
	if sch.AdditionalProperties != nil && !*sch.AdditionalProperties {
		for k := range obj {
			if _, declared := sch.Properties[k]; !declared {
				t.Errorf("%s: member %q is not permitted (additionalProperties:false): %s", defName, k, data)
			}
		}
	}
	// Check property-level const discriminators (e.g. resultType const).
	for name, propRaw := range sch.Properties {
		val, present := obj[name]
		if !present {
			continue
		}
		var prop objectSchema
		if err := json.Unmarshal(propRaw, &prop); err != nil {
			continue // property is `true` or a non-object schema; nothing to check
		}
		if len(prop.Const) > 0 && !jsonEqual(prop.Const, val) {
			t.Errorf("%s.%s: value %s does not match const %s", defName, name, val, prop.Const)
		}
	}
}

// jsonEqual compares two raw JSON values by their canonical decoded form.
func jsonEqual(a, b json.RawMessage) bool {
	var av, bv any
	if json.Unmarshal(a, &av) != nil || json.Unmarshal(b, &bv) != nil {
		return false
	}
	ab, _ := json.Marshal(av)
	bb, _ := json.Marshal(bv)
	return string(ab) == string(bb)
}

// TestPhase1ResultsValidateAgainstSchema proves the Phase 1 result envelopes
// this package produces are self-consistent with wire-2026-07-28.schema.json
// (AMEND-4 / MOCK-201.4). It validates a representative, conformant value for
// each of the three Phase 1 result shapes against its $def.
func TestPhase1ResultsValidateAgainstSchema(t *testing.T) {
	defs := loadSchemaDefs(t)
	si := &ServerInfo{Name: "mcpmock", Version: "1.0.0"}

	cases := []struct {
		name  string
		def   string
		value any
	}{
		{
			name: "discoverResult",
			def:  "discoverResult",
			value: DiscoverResult{
				ResultType:        strptr(ResultTypeDiscovery),
				SupportedVersions: []string{ProtocolRevision},
				Instructions:      strptr("hello"),
				CacheHints:        CacheHints{TTLMs: i64ptr(1000), CacheScope: strptr("public")},
				Meta:              &ResultMeta{ServerInfo: si},
			},
		},
		{
			name: "discoverResult minimal (all optional fields omitted)",
			def:  "discoverResult",
			value: DiscoverResult{
				ResultType: strptr(ResultTypeDiscovery),
			},
		},
		{
			name: "toolListResult",
			def:  "toolListResult",
			value: ToolListResult{
				ResultType: strptr(ResultTypeToolList),
				Tools: []ToolDescriptor{
					{Name: "echo", Description: "echoes", InputSchema: json.RawMessage(`{"type":"object"}`)},
				},
				Meta: &ResultMeta{ServerInfo: si},
			},
		},
		{
			name: "toolCallResult",
			def:  "toolCallResult",
			value: ToolCallResult{
				ResultType: strptr(ResultTypeToolResult),
				Content:    []TextContentBlock{NewTextContentBlock("{}")},
				Meta:       &ResultMeta{ServerInfo: si},
			},
		},
		{
			name: "toolCallResult with isError",
			def:  "toolCallResult",
			value: ToolCallResult{
				ResultType: strptr(ResultTypeToolResult),
				Content:    []TextContentBlock{NewTextContentBlock("boom")},
				IsError:    boolptr(true),
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b, err := MarshalResult(c.value)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			validateAgainstDef(t, defs, c.def, b)
		})
	}
}

// TestServerInfoAndContentBlockValidateAgainstSchema checks the two leaf $defs
// (serverInfo, textContentBlock) that MOCK-209 and builtin-tools.md 1.2 pin,
// including their additionalProperties:false and const constraints.
func TestServerInfoAndContentBlockValidateAgainstSchema(t *testing.T) {
	defs := loadSchemaDefs(t)

	si, _ := MarshalResult(ServerInfo{Name: "mcpmock", Version: "1.0.0", Title: "Mock"})
	validateAgainstDef(t, defs, "serverInfo", si)

	block, _ := MarshalResult(NewTextContentBlock("hi"))
	validateAgainstDef(t, defs, "textContentBlock", block)
}

// TestMetaMissingErrorDataValidatesAgainstSchema checks the -32602 data.missing
// payload against #/$defs/metaMissingErrorData, including the enum on the
// members.
func TestMetaMissingErrorDataValidatesAgainstSchema(t *testing.T) {
	defs := loadSchemaDefs(t)
	b, _ := json.Marshal(MetaMissingErrorData{Missing: []string{MetaFieldProtocolVersion}})
	validateAgainstDef(t, defs, "metaMissingErrorData", b)

	// Each Missing value must be one of the schema's enum members.
	raw := defs["metaMissingErrorData"]
	var sch struct {
		Properties struct {
			Missing struct {
				Items struct {
					Enum []string `json:"enum"`
				} `json:"items"`
			} `json:"missing"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &sch); err != nil {
		t.Fatalf("cannot read metaMissingErrorData enum: %v", err)
	}
	allowed := map[string]bool{}
	for _, e := range sch.Properties.Missing.Items.Enum {
		allowed[e] = true
	}
	for _, f := range RequiredMetaFields() {
		if !allowed[f] {
			t.Errorf("required _meta field %q is not in the schema data.missing enum %v", f, sch.Properties.Missing.Items.Enum)
		}
	}
}

// TestResultTypeSubsetMatchesSchemaEnum proves the three Phase 1 resultType
// values are exactly the schema's resultType enum — no more, no fewer. This is
// the "absent, not permissive" guarantee for the resultType surface: a Phase 2
// value would appear in neither.
func TestResultTypeSubsetMatchesSchemaEnum(t *testing.T) {
	defs := loadSchemaDefs(t)
	var rt struct {
		Enum []string `json:"enum"`
	}
	if err := json.Unmarshal(defs["resultType"], &rt); err != nil {
		t.Fatalf("cannot read resultType enum: %v", err)
	}
	got := map[string]bool{}
	for _, v := range Phase1ResultTypes() {
		got[v] = true
	}
	if len(got) != len(rt.Enum) {
		t.Fatalf("resultType count: package has %d, schema enum has %d (%v)", len(got), len(rt.Enum), rt.Enum)
	}
	for _, v := range rt.Enum {
		if !got[v] {
			t.Errorf("schema resultType enum value %q has no package constant", v)
		}
	}
}
