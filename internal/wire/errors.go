package wire

// Error vocabulary for the Phase 1 surface: the error codes Phase 1 can emit
// and the authored -32602 data.missing payload shape (annex §6, annex 2.12
// [P-11] / [R MOCK-203.4]).
//
// The five codes below are the standard JSON-RPC 2.0 codes, which are also
// named in internal/jsonrpc/codes.go. JSON-RPC 2.0 is a ratified standard, so
// these codes are [D] and stable independent of GAP-003. They are RESTATED here
// so that the ADR-019 containment rule holds for handlers uniformly: a handler
// names wire.ErrCode* rather than reaching for a bare literal or having to know
// whether a given code lives in jsonrpc or wire. The parity test asserts these
// equal their jsonrpc counterparts.
//
// The MCP-specific codes -32020 (HeaderMismatch), -32021
// (MissingRequiredClientCapability) and -32022 (UnsupportedProtocolVersion) are
// Phase 2 (MOCK-204/205/206) and are deliberately ABSENT: there is no constant
// for them here, so a Phase 1 handler cannot emit one.

// Error codes — Phase 1 subset. annex §6.
const (
	// ErrCodeParse (-32700): invalid JSON was received. annex §6.
	// [D JSON-RPC 2.0]. HTTP 400 [P-31].
	ErrCodeParse = -32700
	// ErrCodeInvalidRequest (-32600): the request is not a valid JSON-RPC
	// object (e.g. params is not an object, MOCK-203 mode table). annex §6.
	// [D JSON-RPC 2.0]. HTTP 400 [P-31].
	ErrCodeInvalidRequest = -32600
	// ErrCodeMethodNotFound (-32601): unknown method, or a method disabled by
	// switches.methods.<name>.enabled=false (MOCK-202.2). annex §6.
	// [D JSON-RPC 2.0] / [R MOCK-202]. Proposed HTTP 200 with a JSON-RPC error
	// body [P-31].
	ErrCodeMethodNotFound = -32601
	// ErrCodeInvalidParams (-32602): invalid params; the code MOCK-203 uses for
	// a missing or malformed _meta field, and builtin-tools.md uses for bad
	// arguments. annex §6 / 2.3 / 2.4. [R MOCK-203]. HTTP 400 [R].
	ErrCodeInvalidParams = -32602
	// ErrCodeInternal (-32603): internal error; the default code for a builtin
	// fail(mode: protocolError) (builtin-tools.md 4.3). annex §6.
	// [D JSON-RPC 2.0] / [P-51]. HTTP 500 [P-31] (or 200 as the fail seam).
	ErrCodeInternal = -32603
)

// Phase1ErrorCodes returns the closed set of error codes Phase 1 can produce,
// in ascending-severity presentation order. It is used by the annex-parity test
// and lets a caller enumerate the Phase 1 codes without hard-coding literals.
// The Phase 2 codes (-32020/-32021/-32022) are absent by design.
func Phase1ErrorCodes() []int {
	return []int{
		ErrCodeParse,
		ErrCodeInvalidRequest,
		ErrCodeMethodNotFound,
		ErrCodeInvalidParams,
		ErrCodeInternal,
	}
}

// error.data keys — Phase 1 subset.
const (
	// ErrDataKeyMissing is the -32602 error.data key naming the absent _meta
	// fields for a MOCK-203 violation. annex 2.12 [P-11] / [R MOCK-203.4].
	ErrDataKeyMissing = "missing"
)

// MetaMissingErrorData is the error.data payload for a -32602 raised because a
// required _meta field was absent (annex 2.12 [P-11], schema
// #/$defs/metaMissingErrorData). It names, in Missing, exactly the required
// _meta fields that were not present, so a client (and MOCK-203.4's assertion)
// can see which field failed.
//
// The value-generating logic — deciding WHICH fields are missing — is
// TASK-017's; this package provides only the vocabulary and the shape. The two
// legal member values are [MetaFieldProtocolVersion] and
// [MetaFieldClientCapabilities] (meta.go), matching the schema's enum; the
// schema also requires Missing to be non-empty (minItems: 1), which a caller
// satisfies by only constructing this payload when at least one field is
// actually missing.
//
// Missing carries no omitempty tag on purpose: a -32602 _meta violation always
// has at least one missing field, and the schema forbids an empty array, so the
// field is always emitted.
type MetaMissingErrorData struct {
	// Missing names the absent required _meta fields, in a caller-chosen order
	// (MOCK-203.4 asserts membership, not order). Each entry is one of the
	// MetaField* values. annex 2.12 [P-11].
	Missing []string `json:"missing"`
}
