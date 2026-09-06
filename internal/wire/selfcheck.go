package wire

import (
	"encoding/json"
	"errors"
	"fmt"
)

// selfcheck.go is the outgoing-result self-check (MOCK-201.4, switches.selfCheck,
// AMEND-3). It validates a fully-marshaled Phase 1 result against the AMEND-4
// wire subset — contracts/wire-2026-07-28.schema.json#/$defs/phase1Result — so a
// scenario running with switches.selfCheck=true never emits a result that
// violates the annex the mock itself publishes. This is the machine-checkable
// realization of MOCK-244's principle that the mock must reject its OWN malformed
// responses.
//
// # What "validates against phase1Result" means here (and its limits — GAP-003)
//
// The oracle is the AMEND-4 wire subset, which is PROVISIONAL and UNRATIFIED
// (GAP-003: the schema is x-status "PROVISIONAL — NOT RATIFIED", v0,
// x-conformance-claim NONE). A passing self-check therefore proves ONE thing:
// that the result is self-consistent with mcpmock's OWN authored annex. It
// proves NOTHING about interoperability with any real MCP client or server. Do
// not read a green self-check as a conformance claim.
//
// This validator does NOT embed or read the schema file at run time (the schema
// lives under specification/** which only that tree owns, and shipping a JSON
// Schema evaluator would add a forbidden dependency). Instead it enforces, in
// Go, exactly the structural constraints the schema's phase1Result branch
// pins — required members, additionalProperties:false closed objects, the
// resultType const discriminator, and the nested serverInfo / content / isError
// constraints — over the same wire vocabulary this package owns (ADR-019
// containment). The offline TestPhase1ResultsValidateAgainstSchema proves this
// package's result TYPES agree with the schema $defs, so the two stay in lock
// step: if the schema changes shape, that test fails until the wire types (and
// therefore this validator's constraints) are brought back into agreement.
//
// # Cost
//
// SelfCheckResult parses the result bytes once with encoding/json and walks a
// bounded, fixed set of members. It is called ONLY when a caller has already
// decided switches.selfCheck is on, so it never runs on the default hot path
// (MOCK-901); a disabled self-check pays a single boolean check at the call site
// and does not enter this file at all. It is a pure function holding no state and
// is safe for concurrent use.

// SchemaRef is the human-readable identifier of the oracle a self-check failure
// names, for the MOCK-201.4 ERROR log and the -32603 reason. It is the schema
// path plus the phase1Result entry point, so an operator reading the log knows
// exactly what the result was checked against — and, via the provisional marker,
// that it is the authored annex, not ratified protocol.
const SchemaRef = "contracts/wire-2026-07-28.schema.json#/$defs/phase1Result (PROVISIONAL, GAP-003)"

// ErrSelfCheck is the sentinel every self-check failure wraps, so a caller can
// classify a self-check rejection with errors.Is without string matching. The
// wrapped message names the specific structural violation (for the journalled
// reason and the ERROR log); the sentinel identifies the class.
var ErrSelfCheck = errors.New("wire: self-check failed")

// SelfCheckResult validates raw — a fully-marshaled Phase 1 result body — against
// the phase1Result subset (MOCK-201.4). It returns nil when the result is
// well-formed, or an error wrapping [ErrSelfCheck] and naming the violation when
// it is not. The returned error's message is safe to log and to journal: it names
// the structural fault and the resultType, never leaks the whole body.
//
// resultType is the discriminator: the result must be a JSON object carrying a
// resultType whose value selects one of the three Phase 1 shapes. A result with
// no resultType, or an unknown one, fails — which is exactly the MOCK-209.3 note
// that omitResultType and selfCheck are mutually exclusive expectations: a
// scenario that omits resultType has removed the discriminator the self-check
// needs, so turning both on is a configuration the self-check correctly rejects.
func SelfCheckResult(raw json.RawMessage) error {
	obj, err := decodeObject(raw)
	if err != nil {
		return fmt.Errorf("%w: result is not a JSON object: %w", ErrSelfCheck, err)
	}
	rt, err := discriminator(obj)
	if err != nil {
		return err
	}
	switch rt {
	case ResultTypeDiscovery:
		return checkDiscoverResult(obj)
	case ResultTypeToolList:
		return checkToolListResult(obj)
	case ResultTypeToolResult:
		return checkToolCallResult(obj)
	default:
		return fmt.Errorf("%w: unknown resultType %q (not one of %v)",
			ErrSelfCheck, rt, Phase1ResultTypes())
	}
}

// discriminator extracts the result's resultType value, which selects the shape
// to validate. A missing resultType is a violation: phase1Result's three
// branches each require resultType, so a result without it matches none.
func discriminator(obj map[string]json.RawMessage) (string, error) {
	raw, ok := obj[fieldResultType]
	if !ok {
		return "", fmt.Errorf("%w: result has no %q discriminator", ErrSelfCheck, fieldResultType)
	}
	var rt string
	if err := json.Unmarshal(raw, &rt); err != nil {
		return "", fmt.Errorf("%w: %q is not a string", ErrSelfCheck, fieldResultType)
	}
	return rt, nil
}

// Result member keys the self-check reasons about. They are the JSON tags of the
// result structs in result.go, named here so the validator references the same
// vocabulary the emitters do (ADR-019) rather than repeating bare literals.
const (
	fieldResultType        = "resultType"
	fieldSupportedVersions = "supportedVersions"
	fieldCapabilities      = "capabilities"
	fieldInstructions      = "instructions"
	fieldTTLMs             = "ttlMs"
	fieldCacheScope        = "cacheScope"
	fieldMeta              = "_meta"
	fieldTools             = "tools"
	fieldName              = "name"
	fieldTitle             = "title"
	fieldDescription       = "description"
	fieldInputSchema       = "inputSchema"
	fieldOutputSchema      = "outputSchema"
	fieldAnnotations       = "annotations"
	fieldIcons             = "icons"
	fieldContent           = "content"
	fieldStructuredContent = "structuredContent"
	fieldIsError           = "isError"
	fieldType              = "type"
	fieldText              = "text"
	fieldServerInfo        = "serverInfo"
	fieldVersion           = "version"
)

// checkDiscoverResult enforces #/$defs/discoverResult: resultType const
// "discovery", additionalProperties:false over the declared members, and — when
// present — a well-formed _meta. The other members are independently omissible
// (MOCK-201.3), so only their PRESENCE-when-permitted is checked, not their
// requiredness.
func checkDiscoverResult(obj map[string]json.RawMessage) error {
	allowed := map[string]struct{}{
		fieldResultType: {}, fieldSupportedVersions: {}, fieldCapabilities: {},
		fieldInstructions: {}, fieldTTLMs: {}, fieldCacheScope: {}, fieldMeta: {},
	}
	if err := closedObject("discoverResult", obj, allowed); err != nil {
		return err
	}
	if err := constString("discoverResult", obj, fieldResultType, ResultTypeDiscovery); err != nil {
		return err
	}
	return checkOptionalMeta("discoverResult", obj)
}

// checkToolListResult enforces #/$defs/toolListResult: required members
// resultType and tools, resultType const "toolList", additionalProperties:false,
// each tool a well-formed toolDescriptor, and a well-formed _meta when present.
func checkToolListResult(obj map[string]json.RawMessage) error {
	allowed := map[string]struct{}{
		fieldResultType: {}, fieldTools: {}, fieldTTLMs: {}, fieldCacheScope: {}, fieldMeta: {},
	}
	if err := requireMembers("toolListResult", obj, fieldResultType, fieldTools); err != nil {
		return err
	}
	if err := closedObject("toolListResult", obj, allowed); err != nil {
		return err
	}
	if err := constString("toolListResult", obj, fieldResultType, ResultTypeToolList); err != nil {
		return err
	}
	if err := checkTools(obj[fieldTools]); err != nil {
		return err
	}
	return checkOptionalMeta("toolListResult", obj)
}

// checkToolCallResult enforces #/$defs/toolCallResult: required members
// resultType and content, resultType const "toolResult",
// additionalProperties:false, a non-empty text-only content array (minItems:1),
// isError const true when present, and a well-formed _meta when present.
func checkToolCallResult(obj map[string]json.RawMessage) error {
	allowed := map[string]struct{}{
		fieldResultType: {}, fieldContent: {}, fieldStructuredContent: {},
		fieldIsError: {}, fieldMeta: {},
	}
	if err := requireMembers("toolCallResult", obj, fieldResultType, fieldContent); err != nil {
		return err
	}
	if err := closedObject("toolCallResult", obj, allowed); err != nil {
		return err
	}
	if err := constString("toolCallResult", obj, fieldResultType, ResultTypeToolResult); err != nil {
		return err
	}
	if err := checkContent(obj[fieldContent]); err != nil {
		return err
	}
	return checkIsError(obj)
}

// checkTools validates the tools array: every entry must be a well-formed
// toolDescriptor. An absent or non-array tools was already ruled out by the
// required-member and structural checks in the caller for the required case; here
// it is decoded as an array of raw descriptors.
func checkTools(raw json.RawMessage) error {
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		return fmt.Errorf("%w: toolListResult.tools is not an array: %w", ErrSelfCheck, err)
	}
	for i, d := range arr {
		if err := checkToolDescriptor(d); err != nil {
			return fmt.Errorf("%w: toolListResult.tools[%d]: %v", ErrSelfCheck, i, unwrap(err))
		}
	}
	return nil
}

// checkToolDescriptor enforces #/$defs/toolDescriptor: required member name, and
// additionalProperties:false over the declared members. The schema deliberately
// accepts ANY value for inputSchema/outputSchema/annotations/icons (MOCK-222.3
// verbatim-and-unvalidated), so those members are permitted but their shapes are
// not inspected — the self-check must not reject an authored hostile schema.
func checkToolDescriptor(raw json.RawMessage) error {
	obj, err := decodeObject(raw)
	if err != nil {
		return fmt.Errorf("%w: toolDescriptor is not an object: %w", ErrSelfCheck, err)
	}
	if err := requireMembers("toolDescriptor", obj, fieldName); err != nil {
		return err
	}
	allowed := map[string]struct{}{
		fieldName: {}, fieldTitle: {}, fieldDescription: {},
		fieldInputSchema: {}, fieldOutputSchema: {}, fieldAnnotations: {}, fieldIcons: {},
	}
	return closedObject("toolDescriptor", obj, allowed)
}

// checkContent enforces the toolCallResult.content constraint: a JSON array with
// at least one item (minItems:1), every item a well-formed textContentBlock. An
// empty content array is a violation — builtin-tools.md 1.2 pins content
// non-empty — so a bug that emitted [] is caught here.
func checkContent(raw json.RawMessage) error {
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		return fmt.Errorf("%w: toolCallResult.content is not an array: %w", ErrSelfCheck, err)
	}
	if len(arr) == 0 {
		return fmt.Errorf("%w: toolCallResult.content is empty (minItems:1)", ErrSelfCheck)
	}
	for i, b := range arr {
		if err := checkTextContentBlock(b); err != nil {
			return fmt.Errorf("%w: toolCallResult.content[%d]: %v", ErrSelfCheck, i, unwrap(err))
		}
	}
	return nil
}

// checkTextContentBlock enforces #/$defs/textContentBlock: required members type
// and text, type const "text", text a string, additionalProperties:false. This
// is the block that a non-text Phase 2 content block would violate, which is the
// "absent, not permissive" guarantee made checkable.
func checkTextContentBlock(raw json.RawMessage) error {
	obj, err := decodeObject(raw)
	if err != nil {
		return fmt.Errorf("%w: textContentBlock is not an object: %w", ErrSelfCheck, err)
	}
	if err := requireMembers("textContentBlock", obj, fieldType, fieldText); err != nil {
		return err
	}
	allowed := map[string]struct{}{fieldType: {}, fieldText: {}}
	if err := closedObject("textContentBlock", obj, allowed); err != nil {
		return err
	}
	if err := constString("textContentBlock", obj, fieldType, ContentTypeText); err != nil {
		return err
	}
	return stringMember("textContentBlock", obj, fieldText)
}

// checkIsError enforces the toolCallResult.isError const:true constraint: when
// present, isError MUST be the boolean true (builtin-tools.md 4.2 [P-50] — an
// accidental "isError": false is a schema violation, not a silent inconsistency).
// An absent isError is fine (success omits the key).
func checkIsError(obj map[string]json.RawMessage) error {
	raw, ok := obj[fieldIsError]
	if !ok {
		return nil
	}
	var v bool
	if err := json.Unmarshal(raw, &v); err != nil {
		return fmt.Errorf("%w: toolCallResult.isError is not a boolean", ErrSelfCheck)
	}
	if !v {
		return fmt.Errorf("%w: toolCallResult.isError must be true when present (const:true)", ErrSelfCheck)
	}
	return nil
}

// checkOptionalMeta validates the result-level _meta when present: it must be a
// JSON object, and its serverInfo (when present) must be a well-formed serverInfo.
// _meta itself is additionalProperties:true in the schema (resultMeta carries the
// builtin sleep clamp fields and future members), so unknown _meta members are
// permitted; only serverInfo's shape is pinned.
func checkOptionalMeta(where string, obj map[string]json.RawMessage) error {
	raw, ok := obj[fieldMeta]
	if !ok {
		return nil
	}
	meta, err := decodeObject(raw)
	if err != nil {
		return fmt.Errorf("%w: %s._meta is not an object: %w", ErrSelfCheck, where, err)
	}
	si, ok := meta[fieldServerInfo]
	if !ok {
		return nil
	}
	return checkServerInfo(where, si)
}

// checkServerInfo enforces #/$defs/serverInfo: required members name and version
// (both strings), optional title (a string), additionalProperties:false. This is
// the MOCK-209.2 serverInfo shape carried on every non-omitted result.
func checkServerInfo(where string, raw json.RawMessage) error {
	obj, err := decodeObject(raw)
	if err != nil {
		return fmt.Errorf("%w: %s._meta.serverInfo is not an object: %w", ErrSelfCheck, where, err)
	}
	if err := requireMembers(where+"._meta.serverInfo", obj, fieldName, fieldVersion); err != nil {
		return err
	}
	allowed := map[string]struct{}{fieldName: {}, fieldVersion: {}, fieldTitle: {}}
	if err := closedObject(where+"._meta.serverInfo", obj, allowed); err != nil {
		return err
	}
	for _, f := range []string{fieldName, fieldVersion} {
		if err := stringMember(where+"._meta.serverInfo", obj, f); err != nil {
			return err
		}
	}
	return nil
}

// decodeObject unmarshals raw into a member map, failing for a non-object. It is
// the single "is this a JSON object" gate the shape checks share.
func decodeObject(raw json.RawMessage) (map[string]json.RawMessage, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, err
	}
	if obj == nil {
		return nil, errors.New("value is JSON null, not an object")
	}
	return obj, nil
}

// requireMembers checks that every named member is present in obj, naming the
// first absent one. It is the schema "required" keyword for the closed Phase 1
// shapes.
func requireMembers(where string, obj map[string]json.RawMessage, names ...string) error {
	for _, n := range names {
		if _, ok := obj[n]; !ok {
			return fmt.Errorf("%w: %s is missing required member %q", ErrSelfCheck, where, n)
		}
	}
	return nil
}

// closedObject enforces additionalProperties:false: every member of obj must be
// in allowed, naming the first offender. It is what makes a premature Phase 2
// surface (a nextCursor on tools/list, a non-text content block field) fail
// loudly rather than slip through — the AMEND-4 "absent, not permissive"
// discipline realized at emit time.
func closedObject(where string, obj map[string]json.RawMessage, allowed map[string]struct{}) error {
	for k := range obj {
		if _, ok := allowed[k]; !ok {
			return fmt.Errorf("%w: %s carries member %q that the Phase 1 subset does not permit "+
				"(additionalProperties:false)", ErrSelfCheck, where, k)
		}
	}
	return nil
}

// constString enforces a const string constraint (the resultType and content
// type discriminators): the member must equal want.
func constString(where string, obj map[string]json.RawMessage, field, want string) error {
	var got string
	if err := json.Unmarshal(obj[field], &got); err != nil {
		return fmt.Errorf("%w: %s.%s is not a string", ErrSelfCheck, where, field)
	}
	if got != want {
		return fmt.Errorf("%w: %s.%s = %q, want const %q", ErrSelfCheck, where, field, got, want)
	}
	return nil
}

// stringMember checks that a present member decodes to a JSON string, naming the
// offender otherwise.
func stringMember(where string, obj map[string]json.RawMessage, field string) error {
	var s string
	if err := json.Unmarshal(obj[field], &s); err != nil {
		return fmt.Errorf("%w: %s.%s is not a string", ErrSelfCheck, where, field)
	}
	return nil
}

// unwrap strips the ErrSelfCheck sentinel prefix from a nested error's message so
// a wrapping caller does not repeat "self-check failed:" at every array level,
// keeping the final reason a single readable sentence.
func unwrap(err error) string {
	msg := err.Error()
	const prefix = "wire: self-check failed: "
	if len(msg) > len(prefix) && msg[:len(prefix)] == prefix {
		return msg[len(prefix):]
	}
	return msg
}
