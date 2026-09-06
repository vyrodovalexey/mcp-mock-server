package wire

import "encoding/json"

// Result shapes for the Phase 1 methods (annex §4/§5, schema
// #/$defs/discoverResult, #/$defs/toolListResult, #/$defs/toolCallResult) and
// the result-level _meta that carries serverInfo (MOCK-209).
//
// Two MOCK-209 omission switches must be EXPRESSIBLE in these types, in both
// their conformant and deliberately-omitted forms, for backward-compatibility
// and defensiveness testing:
//
//   - omitResultType (209.3): the resultType key is removed entirely.
//   - omitServerInfoMeta (209.4): _meta.serverInfo is removed.
//
// Both are modeled as ABSENCE, not as a zero value, so an omitted field is a
// key that is not present rather than a key with a null/empty value —
// consistent with MOCK-201.3's absent-not-null rule. resultType is a pointer so
// nil emits no key; the result _meta is a pointer so nil emits no _meta object,
// and its ServerInfo is a pointer so nil emits _meta without serverInfo.
//
// # Emission and byte-stability
//
// These structs are serialized through the canonical/HTML-safe path so results
// are byte-identical under a fixed seed (design principle §0.1). A caller builds
// a result value, marshals it to the raw result JSON, and hands that to
// jsonrpc.NewResultResponse(id, raw).Encode(). MarshalResult is the helper that
// does the marshal with HTML escaping disabled (matching jsonrpc.Response.Encode
// so a result carrying <, > or & is byte-stable).

// ServerInfo is the result _meta.serverInfo shape. annex 4.4 [P-22]:
// {name, version, title?}. It is present on every result unless
// switches.omitServerInfoMeta (MOCK-209.4). Its shape is identical to
// [ClientInfo] but it is a distinct type because the two are independently
// owned by the annex (2.6 vs 4.4) and may diverge on ratification.
type ServerInfo struct {
	// Name is the server's name. annex 4.4 [P-22].
	Name string `json:"name"`
	// Version is the server's version. annex 4.4 [P-22].
	Version string `json:"version"`
	// Title is the server's optional human-readable title. annex 4.4 [P-22].
	Title string `json:"title,omitempty"`
}

// ResultMeta is the result-level _meta object (schema #/$defs/resultMeta). Its
// primary member is ServerInfo (MOCK-209.2); it also carries the builtin sleep
// clamp fields (builtin-tools.md 3.3).
//
// ServerInfo is a pointer so that switches.omitServerInfoMeta (MOCK-209.4) is
// expressible as absence: nil ServerInfo emits a _meta object with no
// serverInfo key. A caller that wants to omit the entire _meta object sets the
// result's Meta field itself to nil (see the result structs below).
type ResultMeta struct {
	// ServerInfo is _meta.serverInfo, present on every result unless
	// omitServerInfoMeta removed it. nil ⇒ the serverInfo key is absent.
	// annex 4.3/4.4. [R MOCK-209] presence; [P-22] shape.
	ServerInfo *ServerInfo `json:"serverInfo,omitempty"`
	// Clamped is emitted only when a builtin sleep with onExceedMaxSleep=clamp
	// reduced the delay (builtin-tools.md 3.3). A *bool so it is absent unless
	// explicitly set; when set it is true. [P-47].
	Clamped *bool `json:"clamped,omitempty"`
	// RequestedMs accompanies Clamped, naming the originally requested delay.
	// builtin-tools.md 3.3. *int so it is absent unless set. [P-47].
	RequestedMs *int `json:"requestedMs,omitempty"`
}

// CacheHints are the result-level ttlMs and cacheScope fields on server/discover
// and the list methods (annex 4.5/4.6, schema #/$defs/cacheHints). They are
// embedded into the result structs rather than nested, matching the schema,
// which places ttlMs and cacheScope at the result top level.
//
// Both are pointers so each is independently omissible as ABSENCE (MOCK-201.3):
// a nil TTLMs emits no ttlMs key at all. MOCK-201.5 / MOCK-232 require the
// values 0, negative and > 2^53 to be expressible, so TTLMs is a *int64 (not a
// smaller or unsigned type) and no clamping is applied here. CacheScope is a
// *string, deliberately not an enum, because MOCK-232.4 requires arbitrary
// strings to be emittable even though the proposed set is "public"|"private"
// ([P-23]).
type CacheHints struct {
	// TTLMs is the result cache time-to-live in milliseconds, or nil to omit.
	// annex 4.5. [R MOCK-201, MOCK-232]. Values 0, negative and > 2^53 are all
	// legal (MOCK-201.5).
	TTLMs *int64 `json:"ttlMs,omitempty"`
	// CacheScope is the cache scope; proposed "public"|"private" but any string
	// is emittable (MOCK-232.4), or nil to omit. annex 4.6. [P-23].
	CacheScope *string `json:"cacheScope,omitempty"`
}

// DiscoverResult is the server/discover result (annex §5, schema
// #/$defs/discoverResult). Every field except ResultType is independently
// omissible, and an omitted field is ABSENT rather than null (MOCK-201.3): that
// is why each optional field is a pointer, slice or map with omitempty.
//
// ResultType is a *string so switches.omitResultType (MOCK-209.3) is expressible
// as absence; when present it is [ResultTypeDiscovery].
type DiscoverResult struct {
	// ResultType is "discovery" (or absent under omitResultType). annex 4.2.
	// [P-21]. Pointer so MOCK-209.3 omission is absence, not "".
	ResultType *string `json:"resultType,omitempty"`
	// SupportedVersions is the array of revision strings, most-preferred first,
	// or omitted. annex 5.2. [P-27].
	SupportedVersions []string `json:"supportedVersions,omitempty"`
	// Capabilities is the capability object, including capabilities.extensions,
	// preserved as raw JSON so authored/arbitrary capability shapes survive
	// (MOCK-201.2), or omitted. annex 5.3. [P-28].
	Capabilities json.RawMessage `json:"capabilities,omitempty"`
	// Instructions is a human-readable instruction string, or omitted.
	// annex 5.4. [P-29].
	Instructions *string `json:"instructions,omitempty"`
	// TTLMs and CacheScope are the result cache hints. annex 4.5/4.6.
	CacheHints
	// Meta is the result _meta carrying serverInfo, or nil to omit the whole
	// _meta object. annex 4.3. [R MOCK-209].
	Meta *ResultMeta `json:"_meta,omitempty"`
}

// ToolDescriptor is one tools/list entry (annex, schema #/$defs/toolDescriptor).
// MOCK-222.3 / MOCK-224.3: inputSchema, outputSchema, annotations and icons are
// emitted VERBATIM and are deliberately NOT validated — the whole point is that
// mcpmock can serve a hostile or malformed schema — so they are carried as raw
// JSON and pass through unaltered.
type ToolDescriptor struct {
	// Name is the tool name. annex. [R MOCK-221, MOCK-222].
	Name string `json:"name"`
	// Title is the optional human-readable title, or omitted.
	Title string `json:"title,omitempty"`
	// Description is the optional description, or omitted.
	Description string `json:"description,omitempty"`
	// InputSchema is advertised verbatim, unvalidated (MOCK-222.3), or omitted.
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
	// OutputSchema is advertised verbatim, unvalidated, or omitted.
	OutputSchema json.RawMessage `json:"outputSchema,omitempty"`
	// Annotations are advertised verbatim, unvalidated, or omitted.
	Annotations json.RawMessage `json:"annotations,omitempty"`
	// Icons are advertised verbatim, unvalidated, or omitted.
	Icons json.RawMessage `json:"icons,omitempty"`
}

// ToolListResult is the tools/list result (annex, schema
// #/$defs/toolListResult). Phase 1 is UNPAGINATED: there is no nextCursor field,
// which is a Phase 2 addition (MOCK-231) — its absence here is the AMEND-4
// "absent, not permissive" discipline made concrete.
type ToolListResult struct {
	// ResultType is "toolList" (or absent under omitResultType). annex 4.2.
	// [P-21].
	ResultType *string `json:"resultType,omitempty"`
	// Tools is the catalog, in deterministic order (ADR-003). Never nil for a
	// well-formed result; an empty catalog is an empty (non-nil) slice, which
	// emits []. annex. [R MOCK-202, MOCK-221].
	Tools []ToolDescriptor `json:"tools"`
	// TTLMs and CacheScope are the result cache hints. annex 4.5/4.6.
	CacheHints
	// Meta is the result _meta carrying serverInfo, or nil to omit. annex 4.3.
	// [R MOCK-209].
	Meta *ResultMeta `json:"_meta,omitempty"`
}

// TextContentBlock is the ONLY content block Phase 1 emits (builtin-tools.md 1.2
// [P-40], schema #/$defs/textContentBlock): {"type":"text","text":<string>}.
// Non-text blocks are Phase 2+/Phase 9 and are absent by design.
type TextContentBlock struct {
	// Type is always [ContentTypeText]. builtin-tools.md 1.2. [P-40].
	Type string `json:"type"`
	// Text is the block's text payload. builtin-tools.md 1.2. [P-40].
	Text string `json:"text"`
}

// ContentTypeText is the content-block discriminator for the only block Phase 1
// emits. builtin-tools.md 1.2. [P-40].
const ContentTypeText = "text"

// NewTextContentBlock builds the sole Phase 1 content block with its type fixed,
// so no caller writes the "text" discriminator literal (ADR-019 containment).
func NewTextContentBlock(text string) TextContentBlock {
	return TextContentBlock{Type: ContentTypeText, Text: text}
}

// ToolCallResult is the tools/call result (annex 4.2 [P-39] / builtin-tools.md
// 1.2, schema #/$defs/toolCallResult).
//
// IsError is a *bool because builtin-tools.md 4.2 [P-50] requires it to be
// emitted ONLY when true: a successful result omits the key entirely rather
// than carrying "isError": false (the schema pins it to const:true for exactly
// this reason). A nil IsError therefore emits no isError key; a non-nil pointer
// must point to true. StructuredContent is optional and, per builtin-tools.md
// 4.2, is not emitted in toolError mode.
type ToolCallResult struct {
	// ResultType is "toolResult" (or absent under omitResultType). annex 4.2.
	// [P-21] / [P-39].
	ResultType *string `json:"resultType,omitempty"`
	// Content is the content-block array, never empty for a well-formed result
	// (builtin-tools.md 1.2, schema minItems:1). Phase 1 blocks are all text.
	Content []TextContentBlock `json:"content"`
	// StructuredContent is the optional structured channel, or omitted (and
	// always omitted in toolError mode, builtin-tools.md 4.2). Raw JSON so an
	// arbitrary echoed value survives verbatim ([P-42]/[P-43]).
	StructuredContent json.RawMessage `json:"structuredContent,omitempty"`
	// IsError is emitted only when true (builtin-tools.md 4.2 [P-50]); nil ⇒
	// the key is absent. A non-nil pointer must be true.
	IsError *bool `json:"isError,omitempty"`
	// Meta is the result _meta carrying serverInfo, or nil to omit. annex 4.3.
	// [R MOCK-209].
	Meta *ResultMeta `json:"_meta,omitempty"`
}

// MarshalResult serializes a Phase 1 result value to its raw result JSON with
// HTML escaping disabled, matching jsonrpc.Response.Encode so that a result
// carrying <, > or & is byte-stable (design principle §0.1). The returned bytes
// are handed to jsonrpc.NewResultResponse(id, raw).Encode() for emission.
//
// It does NOT canonicalise (re-sort) keys: struct field order is fixed and
// deterministic already, and the authored raw-JSON members (Capabilities,
// StructuredContent, tool schemas) must reach the client verbatim (MOCK-222.4),
// exactly as the jsonrpc response path requires. It is a pure function and
// holds no state.
func MarshalResult(v any) ([]byte, error) {
	return marshalNoHTMLEscape(v)
}
