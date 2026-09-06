package wire

import "encoding/json"

// The _meta envelope. annex §2. This file carries the _meta key constants and
// the MetaEnvelope type that models an incoming request's _meta so that
// TASK-017 can validate it (MOCK-203) and the journal can preserve it
// (MOCK-203.6 / MOCK-601).
//
// The central modeling requirement (MOCK-203, AMEND-6) is that ABSENCE is
// distinguishable from EMPTY, for each required field independently. This is
// the same presence-vs-value distinction internal/jsonrpc made for ids
// (IDAbsent vs a present-but-null id), and it is modeled the same way here:
// each optional member is a pointer whose nil-ness records "member not
// present". A present-but-empty clientCapabilities ({}) is a non-nil pointer to
// an empty map and is treated as the empty capability set — which is a
// different thing from an absent one (requirements-spec.md MOCK-203 downstream
// effect: absent falls back, empty is the empty set).

// _meta location and key constants. annex 2.5 / 2.3 / 2.4 / 2.6.
const (
	// MetaKey is the params member under which _meta is carried. annex 2.5
	// [P-06]: _meta lives INSIDE params, i.e. params._meta, matching the
	// established MCP convention. This is the single most consequential
	// proposed choice in the annex — it changes every request the hub builds —
	// so it is isolated here as one constant. [R MOCK-203] presence;
	// [P-06] location.
	MetaKey = "_meta"

	// MetaKeyProtocolVersion is the _meta member REQUIRED under
	// validateMeta=strict; its absence yields -32602/400. annex 2.3.
	// [R MOCK-203.1].
	MetaKeyProtocolVersion = "protocolVersion"

	// MetaKeyClientCapabilities is the _meta member REQUIRED under
	// validateMeta=strict; its absence yields -32602/400. annex 2.4.
	// [R MOCK-203.2] presence; [P-08] shape.
	MetaKeyClientCapabilities = "clientCapabilities"

	// MetaKeyClientInfo carries client identity in _meta. annex 2.6. [P-07]
	// shape {name, version, title?}.
	MetaKeyClientInfo = "clientInfo"
)

// MetaField* name the required _meta fields as the values that appear in a
// -32602 data.missing array (MetaMissingErrorData) and in the journal's
// metaValidation.missing set. They are the SAME strings as the corresponding
// MetaKey* constants; they exist as a distinct, named group because the
// missing-set vocabulary is a closed enumeration (the schema's
// metaMissingErrorData enum is exactly these two) whereas the MetaKey* group is
// the open set of decodable _meta members.
const (
	// MetaFieldProtocolVersion is the data.missing token for an absent
	// _meta.protocolVersion. annex 2.12 [P-11] / [R MOCK-203.4].
	MetaFieldProtocolVersion = MetaKeyProtocolVersion
	// MetaFieldClientCapabilities is the data.missing token for an absent
	// _meta.clientCapabilities. annex 2.12 [P-11] / [R MOCK-203.4].
	MetaFieldClientCapabilities = MetaKeyClientCapabilities
)

// RequiredMetaFields returns the closed, ordered set of _meta fields whose
// PRESENCE is required under validateMeta=strict (and whose absence lenient
// mode records rather than rejects). annex 2.3 / 2.4, AMEND-6. It is the set
// TASK-017 iterates to compute the missing set, and the annex-parity test
// asserts it matches the schema's metaMissingErrorData enum.
func RequiredMetaFields() []string {
	return []string{MetaFieldProtocolVersion, MetaFieldClientCapabilities}
}

// ClientInfo is the _meta.clientInfo shape. annex 2.6 [P-07]:
// {name, version, title?}. Name and version are always emitted; Title is
// omitted when empty.
type ClientInfo struct {
	// Name is the client's name. annex 2.6 [P-07].
	Name string `json:"name"`
	// Version is the client's version. annex 2.6 [P-07].
	Version string `json:"version"`
	// Title is the client's optional human-readable title. annex 2.6 [P-07].
	Title string `json:"title,omitempty"`
}

// MetaEnvelope is the decoded request _meta (params._meta, annex 2.5 [P-06]).
//
// It is the vocabulary and structure for MOCK-203 validation; the validation
// LOGIC belongs to TASK-017. Its whole design point is that absence is
// distinguishable from empty:
//
//   - ProtocolVersion is a *string. nil means the member was ABSENT (a -32602
//     under strict; recorded in the missing set under lenient). A non-nil
//     pointer to "" means the member was present with an empty string, which is
//     a different, structurally-valid case.
//   - ClientCapabilities is a *(map). nil means ABSENT; a non-nil pointer to an
//     empty map means present-and-empty, i.e. the empty capability set, which
//     MOCK-203's downstream note treats differently from absent.
//   - Extra preserves every unknown _meta key verbatim (annex 2.11 [D],
//     MOCK-203.6 / MOCK-601) so the journal can record all keys, including
//     unknown ones, exactly as received.
//
// The zero value is a valid, wholly-absent _meta: all pointers nil, Extra nil.
// That state is itself meaningful — it is what a request with no params._meta
// at all decodes to, and is distinct from a present-but-empty _meta ({}), which
// decodes to a non-nil Extra of length zero. Presence of the _meta member
// itself is tracked by the caller (the params decoder), the same way
// internal/jsonrpc tracks id presence separately from the ID value.
type MetaEnvelope struct {
	// ProtocolVersion is _meta.protocolVersion, or nil if that member was
	// absent. annex 2.3. [R MOCK-203.1].
	ProtocolVersion *string
	// ClientCapabilities is _meta.clientCapabilities, or nil if that member was
	// absent. A non-nil pointer to an empty map is the empty capability set.
	// Each value is preserved as raw JSON so the [P-08] capability-object shape
	// (which is not yet validated in Phase 1) survives verbatim for the
	// journal. annex 2.4. [R MOCK-203.2] presence; [P-08] shape.
	ClientCapabilities *map[string]json.RawMessage
	// ClientInfo is _meta.clientInfo, or nil if absent. annex 2.6. [P-07].
	ClientInfo *ClientInfo
	// Extra holds every _meta member not decoded into a typed field above,
	// preserved verbatim so unknown keys reach the journal unaltered. It is nil
	// when the _meta object carried no such members (and, in particular, nil
	// for a wholly-absent _meta). annex 2.11. [D] — MOCK-203.6 / MOCK-601.
	Extra map[string]json.RawMessage
}

// HasProtocolVersion reports whether the _meta.protocolVersion member was
// present (regardless of its value). It is the presence predicate MOCK-203
// strictness modes turn on: strict rejects when it is false, lenient records it
// in the missing set, off does not compute it at all.
func (m *MetaEnvelope) HasProtocolVersion() bool {
	return m.ProtocolVersion != nil
}

// HasClientCapabilities reports whether the _meta.clientCapabilities member was
// present (regardless of whether it was empty). A present-but-empty map returns
// true; only a wholly-absent member returns false.
func (m *MetaEnvelope) HasClientCapabilities() bool {
	return m.ClientCapabilities != nil
}

// MetaValidationResult is what a strictness mode RECORDS for the journal
// (metaValidation, MOCK-203.7 / 203.8, AMEND-6). This package provides the
// carrier; TASK-017 populates it. It is the type that makes the three modes'
// observable differences expressible:
//
//   - strict:  Mode=MetaModeStrict, Missing = the rejected fields, Accepted=false when rejecting.
//   - lenient: Mode=MetaModeLenient, Missing = the TOLERATED fields, Accepted=true always.
//   - off:     Mode=MetaModeOff, Missing = always empty (not computed), Accepted=true always.
//
// The load-bearing distinction lenient-vs-off is Missing: lenient computes it,
// off does not. Because Missing is a plain slice, a nil/empty Missing under off
// is the "not computed" signal, and a non-empty Missing under lenient is the
// "computed and tolerated" signal — the observable difference MOCK-203.8
// requires.
type MetaValidationResult struct {
	// Mode is the strictness mode in force. One of the MetaMode* constants.
	Mode string `json:"mode"`
	// Missing names the required _meta fields that were absent. Under lenient
	// these were tolerated; under strict these were rejected; under off this is
	// always empty because the set is not computed. Each entry is a MetaField*
	// value.
	Missing []string `json:"missing"`
	// Accepted reports whether the request was allowed to proceed: false only
	// when strict rejected it, true otherwise.
	Accepted bool `json:"accepted"`
}

// validateMeta strictness modes. annex / requirements-spec.md MOCK-203.5,
// AMEND-6. These are the journal metaValidation.mode values and the
// switches.validateMeta enum.
const (
	// MetaModeStrict rejects a missing required _meta field with -32602/400.
	// requirements-spec.md MOCK-203.5 (default). [stated].
	MetaModeStrict = "strict"
	// MetaModeLenient tolerates a missing required _meta field but still
	// COMPUTES and records the missing set (tolerate-and-record). It relaxes
	// exactly the presence of protocolVersion and clientCapabilities and
	// nothing structural. requirements-spec.md MOCK-203.5 / 203.7, AMEND-6.
	// [stated] mode; [derived] semantics.
	MetaModeLenient = "lenient"
	// MetaModeOff skips the _meta presence check entirely and does NOT compute
	// the missing set — the observable difference from lenient.
	// requirements-spec.md MOCK-203.5 / 203.8, AMEND-6. [stated] mode;
	// [derived] semantics.
	MetaModeOff = "off"
)
