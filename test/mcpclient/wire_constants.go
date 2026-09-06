package mcpclient

// This file is the SINGLE place in this package where wire constants live.
// Every constant is transcribed BY HAND from the wire annex
// (specification/contracts/wire-2026-07-28.md) and the built-in-tools contract
// (specification/contracts/builtin-tools.md), never from any mcpmock server
// package. See doc.go for the anti-circularity rule (ADR-017).
//
// Each constant is annotated with:
//   - its annex clause number (e.g. "annex 2.5"), and
//   - its provenance label:
//       [R]     restated from requirements.md, cited in the annex
//       [D]     derived from an [R] item or from JSON-RPC 2.0 / RFC 9110 / etc.
//       [P-nn]  PROPOSED in the annex to fill a GAP-003 gap; may change on
//               ratification. A [P-nn] label marks a localised ratification cost.
//
// TestAnnexParity in wire_constants_test.go asserts that the set of constants
// here matches the annex tables for the Phase 1 surface, so a drift between
// this file and the annex fails loudly.

// Protocol identity. The 2026-07-28 revision is authored and unratified
// (GAP-003); it carries no conformance claim.
const (
	// ProtocolRevision is the modern protocol revision string this client
	// speaks. annex §0 / preamble. [R] (revision named in requirements.md);
	// its wire semantics are [P] pending GAP-003 ratification.
	ProtocolRevision = "2026-07-28"

	// JSONRPCVersion is the JSON-RPC envelope version. annex 2.1. [D JSON-RPC 2.0].
	JSONRPCVersion = "2.0"
)

// Method names — annex §4/§5 and the Phase 1 method set (task-breakdown G-1,
// AMEND-3). These three are the entire Phase 1 surface; adding a method here
// when the annex grows is a one-line edit.
const (
	// MethodDiscover is the discovery call. annex §5. [R MOCK-201].
	MethodDiscover = "server/discover"
	// MethodToolsList lists the tool catalogue. annex §4 / MOCK-202. [R MOCK-202].
	MethodToolsList = "tools/list"
	// MethodToolsCall invokes a tool. annex §4 / MOCK-202. [R MOCK-202].
	MethodToolsCall = "tools/call"
)

// _meta envelope. annex §2.
const (
	// MetaKeyParams is the params object key on a JSON-RPC request. annex 2.5.
	// [D JSON-RPC 2.0].
	MetaKeyParams = "params"

	// MetaKey is the key under which _meta is carried. annex 2.5 [P-06]:
	// _meta lives inside params, i.e. params._meta. This is the single most
	// consequential proposed choice in the annex — it changes every request the
	// hub builds — so it is isolated here and depended on by newRequestBody.
	MetaKey = "_meta"

	// MetaKeyProtocolVersion is REQUIRED under validateMeta=strict; absence
	// yields -32602/400. annex 2.3. [R MOCK-203.1].
	MetaKeyProtocolVersion = "protocolVersion"

	// MetaKeyClientCapabilities is REQUIRED under validateMeta=strict; absence
	// yields -32602/400. annex 2.4. [R MOCK-203.2] presence; [P-08] shape.
	MetaKeyClientCapabilities = "clientCapabilities"

	// MetaKeyClientInfo carries client identity. annex 2.6. [P-07] shape
	// {name, version, title?}.
	MetaKeyClientInfo = "clientInfo"
)

// tools/call request param keys. annex §4 / builtin-tools.md §1.3.
const (
	// ParamsKeyName names the tool to call. annex 3.3 / builtin-tools.md 1.3.
	// [R MOCK-204] + [P-12].
	ParamsKeyName = "name"
	// ParamsKeyArguments carries the tool arguments object. builtin-tools.md 1.3.
	// [D].
	ParamsKeyArguments = "arguments"
)

// resultType values — Phase 1 subset of annex 4.2 [P-21]. The full enum has 11
// values; the other 8 belong to methods absent from Phase 1 and are appended in
// Phase 2+. This client only asserts against the three it can drive.
const (
	// ResultTypeDiscovery is the resultType of a server/discover result.
	// annex 4.2. [P-21].
	ResultTypeDiscovery = "discovery"
	// ResultTypeToolList is the resultType of a tools/list result. annex 4.2.
	// [P-21].
	ResultTypeToolList = "toolList"
	// ResultTypeToolResult is the resultType of a tools/call result. annex 4.2 /
	// builtin-tools.md 1.2. [P-21] / [P-39].
	ResultTypeToolResult = "toolResult"
)

// JSON-RPC / MCP error codes — Phase 1 subset. annex §6. The MCP-specific
// -32020/-32021/-32022 are Phase 2 and are deliberately absent here.
const (
	// ErrCodeParse is a JSON parse error. annex §6. [D JSON-RPC 2.0]. HTTP 400.
	ErrCodeParse = -32700
	// ErrCodeInvalidRequest is a malformed JSON-RPC request. annex §6. [D]. HTTP 400.
	ErrCodeInvalidRequest = -32600
	// ErrCodeMethodNotFound is an unknown or disabled method. annex §6.
	// [D] / [R MOCK-202]. Proposed HTTP 200 with a JSON-RPC error body [P-31].
	ErrCodeMethodNotFound = -32601
	// ErrCodeInvalidParams covers a missing/invalid _meta field and bad
	// arguments. annex §6 / 2.3 / 2.4. [R MOCK-203]. HTTP 400.
	ErrCodeInvalidParams = -32602
	// ErrCodeInternal is the default fail protocolError code. annex §6 /
	// builtin-tools.md 4.3. [D] / [P-51]. HTTP 500 (or 200 for a fail seam).
	ErrCodeInternal = -32603
)

// -32602 _meta-violation error.data payload. annex 2.12 [P-11] / [R MOCK-203.4]:
// data.missing names the absent _meta fields.
const (
	// ErrDataKeyMissing is the error.data key naming absent _meta fields.
	// annex 2.12. [P-11].
	ErrDataKeyMissing = "missing"
)

// HTTP framing. annex §1.
const (
	// ContentTypeJSON is the required request Content-Type and the JSON
	// response Content-Type. annex 1.4 / 1.10. [R MOCK-208] / [P-05].
	ContentTypeJSON = "application/json"
	// ContentTypeEventStream is the SSE response Content-Type. annex 1.4.
	// [R MOCK-208]. Phase 1 emits JSON, but the client must recognise this
	// without assuming it (doc.go: no response-shape assumption).
	ContentTypeEventStream = "text/event-stream"

	// HeaderContentType is the HTTP Content-Type header name. [D RFC 9110].
	HeaderContentType = "Content-Type"
	// HeaderMCPProtocolVersion mirrors _meta.protocolVersion on the wire.
	// annex 3.1. [R MOCK-204]. Phase 1 does not validate it server-side
	// (mirrored-header validation is Phase 2), but the client can send it.
	HeaderMCPProtocolVersion = "MCP-Protocol-Version"
)

// phase1Methods is the closed set of methods this client drives in Phase 1.
// Used by TestAnnexParity to assert parity with the annex Phase 1 surface.
func phase1Methods() []string {
	return []string{MethodDiscover, MethodToolsList, MethodToolsCall}
}

// phase1ResultTypes is the closed set of resultType values reachable in Phase 1.
// Used by TestAnnexParity.
func phase1ResultTypes() []string {
	return []string{ResultTypeDiscovery, ResultTypeToolList, ResultTypeToolResult}
}

// phase1ErrorCodes is the closed set of error codes Phase 1 can produce.
// Used by TestAnnexParity.
func phase1ErrorCodes() []int {
	return []int{
		ErrCodeParse,
		ErrCodeInvalidRequest,
		ErrCodeMethodNotFound,
		ErrCodeInvalidParams,
		ErrCodeInternal,
	}
}
