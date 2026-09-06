package scenario

import "encoding/json"

// This file carries the Phase 1 subset of a Scenario document's spec
// (#/$defs/instanceSpec) as public Go types. Per the package doc's absent-vs-
// zero idiom, every optional scalar is a pointer whose nil-ness records
// absence, every nested config block is a pointer, and maps/slices are nil when
// absent — so composition (TASK-007) can tell "field not present" (inherit)
// from "field set to its zero value" (override).
//
// Fields whose shape belongs to a later phase (mrtr, subscriptions, sessions,
// auth, hostile, paging, process, and the action bodies of a fault rule) are
// carried as [json.RawMessage] rather than decoded structs. That is a
// deliberate choice: the JSON Schema (which validates the raw document tree,
// not these structs) already enforces those shapes with
// unevaluatedProperties: false, so preserving them verbatim neither weakens
// validation nor forecloses TASK-007's merge, while keeping this Phase 1
// package to the surface the task scopes.

// InstanceSpec is a single instance's configuration (schema
// #/$defs/instanceSpec) — the body of a [KindScenario] document's spec.
type InstanceSpec struct {
	// Era selects the protocol era: "modern", "legacy", "dual" or "probe"
	// (MOCK-303). Nil when absent; the schema default is "modern".
	Era *string `json:"era,omitempty"`
	// LegacyVersion pins the legacy protocol revision. Nil when absent.
	LegacyVersion *string `json:"legacyVersion,omitempty"`
	// Transport configures the HTTP/stdio transports (MOCK-102/106). Nil when
	// absent.
	Transport *Transport `json:"transport,omitempty"`
	// Discover configures the server/discover response (MOCK-201). Nil when
	// absent.
	Discover *Discover `json:"discover,omitempty"`
	// Switches configures per-method enable/disable and omission switches
	// (MOCK-202/203/209). Nil when absent.
	Switches *Switches `json:"switches,omitempty"`
	// Catalog configures generated and authored primitives (MOCK-221..228).
	// Nil when absent. The JSON key MUST match the authoritative schema's
	// property name, which is the British spelling; misspell (locale: US) is
	// suppressed on this one tag because the schema wins over the linter here
	// (ADR-008, HARD-to-reverse contract). The Go identifier avoids the word so
	// only the wire-mandated tag is affected.
	Catalog *Catalog `json:"catalogue,omitempty"`
	// Journal configures the journal ring (MOCK-601/602, ADR-005). Nil when
	// absent.
	Journal *Journal `json:"journal,omitempty"`
	// Faults lists fault rules (§5). Nil when absent. This is a merge-by-id
	// list under composition. In Phase 1 the rule envelope is typed but action
	// bodies are preserved verbatim; see [FaultRule].
	Faults []FaultRule `json:"faults,omitempty"`

	// The following blocks belong to later phases. They are preserved verbatim
	// so a full document loads and re-encodes without loss and the schema still
	// validates them, without this Phase 1 package fixing their Go shape.

	// Paging is the pagination config (MOCK-231/232). Nil when absent.
	Paging json.RawMessage `json:"paging,omitempty"`
	// Mrtr is the multi-round tool result config (MOCK-241..247). Nil when
	// absent.
	Mrtr json.RawMessage `json:"mrtr,omitempty"`
	// Subscriptions is the subscription config (MOCK-251..257). Nil when
	// absent.
	Subscriptions json.RawMessage `json:"subscriptions,omitempty"`
	// Sessions is the session config (MOCK-301/305). Nil when absent.
	Sessions json.RawMessage `json:"sessions,omitempty"`
	// Auth is the authorization config (MOCK-401..407). Nil when absent.
	Auth json.RawMessage `json:"auth,omitempty"`
	// Hostile gates the hostile corpus (ADR-018). Nil when absent.
	Hostile json.RawMessage `json:"hostile,omitempty"`
	// Process carries instance-scoped process settings. Nil when absent.
	Process json.RawMessage `json:"process,omitempty"`
}

// Transport configures the instance's transports (schema #/$defs/transport,
// MOCK-102/106).
type Transport struct {
	// Kinds lists enabled transport kinds ("http", "stdio"). Nil when absent;
	// the schema default is ["http"]. It is a replace-on-merge list.
	Kinds []string `json:"kinds,omitempty"`
	// HTTP configures the HTTP transport. Nil when absent.
	HTTP *TransportHTTP `json:"http,omitempty"`
}

// TransportHTTP configures the streamable-HTTP transport (schema
// #/$defs/transport/http).
type TransportHTTP struct {
	// Path is the MCP HTTP path. Nil when absent; schema default "/mcp".
	Path *string `json:"path,omitempty"`
	// Listener selects "shared" or "own" listener. Nil when absent.
	Listener *string `json:"listener,omitempty"`
	// Addr binds an own listener. Nil when absent.
	Addr *string `json:"addr,omitempty"`
	// ForceHTTP1 forces HTTP/1.1 (required by reset/halfClose faults). Nil when
	// absent; a non-nil pointer to false is a deliberate override.
	ForceHTTP1 *bool `json:"forceHTTP1,omitempty"`
	// TLS configures transport TLS. Preserved verbatim (later-phase shape); nil
	// when absent.
	TLS json.RawMessage `json:"tls,omitempty"`
}

// Discover configures the server/discover response (schema #/$defs/discover,
// MOCK-201). Capabilities and ServerInfo are emitted verbatim, including
// unknown extension keys, so they are [json.RawMessage].
type Discover struct {
	// SupportedVersions lists advertised protocol versions. Nil when absent.
	SupportedVersions []string `json:"supportedVersions,omitempty"`
	// Capabilities is emitted verbatim, including unknown extension keys
	// (MOCK-201.2). Nil when absent.
	Capabilities json.RawMessage `json:"capabilities,omitempty"`
	// Instructions is the server instructions string. Nil when absent.
	Instructions *string `json:"instructions,omitempty"`
	// ServerInfo is emitted verbatim. Nil when absent.
	ServerInfo json.RawMessage `json:"serverInfo,omitempty"`
	// TTLMs is the discover cache TTL in ms, or an explicit JSON null. Preserved
	// as raw JSON so absent (nil), a number, and null stay distinct. Nil when
	// absent.
	TTLMs json.RawMessage `json:"ttlMs,omitempty"`
	// CacheScope is the discover cache scope, or an explicit JSON null. Nil when
	// absent.
	CacheScope json.RawMessage `json:"cacheScope,omitempty"`
}

// Switches configures per-method behavior and named omission/non-conformance
// switches (schema #/$defs/switches, MOCK-202/203/209).
type Switches struct {
	// Methods maps method name → per-method switch block. Nil when absent. Its
	// iteration order does not affect any output; where a deterministic order
	// is needed downstream, callers sort the keys.
	Methods map[string]MethodSwitch `json:"methods,omitempty"`
	// ValidateMeta selects the _meta validation strictness: "strict",
	// "lenient" or "off" (MOCK-203.5, AMEND-6). Nil when absent; schema default
	// "strict".
	ValidateMeta *string `json:"validateMeta,omitempty"`
	// ValidateHeaders toggles header validation. Nil when absent.
	ValidateHeaders *bool `json:"validateHeaders,omitempty"`
	// OmitResultType omits the resultType field (MOCK-209). Nil when absent.
	OmitResultType *bool `json:"omitResultType,omitempty"`
	// OmitServerInfoMeta omits serverInfo _meta. Nil when absent.
	OmitServerInfoMeta *bool `json:"omitServerInfoMeta,omitempty"`
	// ExposeStateReason puts the requestState rejection reason in error.data
	// (ADR-010). Nil when absent.
	ExposeStateReason *bool `json:"exposeStateReason,omitempty"`
	// SelfCheck toggles the self-check (ADR-017). Nil when absent.
	SelfCheck *bool `json:"selfCheck,omitempty"`
	// NonConformant carries named spec-violation switches. Preserved verbatim
	// (later-phase shape); nil when absent.
	NonConformant json.RawMessage `json:"nonConformant,omitempty"`
}

// MethodSwitch is a per-method switch block (schema
// #/$defs/switches/methods/additionalProperties).
type MethodSwitch struct {
	// Enabled toggles the method. Nil when absent; schema default true. A
	// non-nil pointer to false disables the method (MOCK-202.2).
	Enabled *bool `json:"enabled,omitempty"`
	// HideFromCapabilities hides an enabled method from capabilities. Nil when
	// absent.
	HideFromCapabilities *bool `json:"hideFromCapabilities,omitempty"`
	// Shape selects the response shape. Preserved verbatim; nil when absent.
	Shape json.RawMessage `json:"shape,omitempty"`
	// TTLMs is the per-method cache TTL or explicit null. Nil when absent.
	TTLMs json.RawMessage `json:"ttlMs,omitempty"`
	// CacheScope is the per-method cache scope or explicit null. Nil when
	// absent.
	CacheScope json.RawMessage `json:"cacheScope,omitempty"`
}

// Catalog configures generated and authored primitives (schema $defs entry,
// MOCK-221..228). Generated items are virtual (ADR-004); only authored
// overrides cost memory. Its JSON key is spelled the British way in the schema;
// the Go identifier is spelled Catalog for tooling reasons only.
type Catalog struct {
	// Tools generates virtual tools. Nil when absent.
	Tools *Generated `json:"tools,omitempty"`
	// Prompts generates virtual prompts. Nil when absent.
	Prompts *Generated `json:"prompts,omitempty"`
	// Resources generates virtual resources. Nil when absent.
	Resources *Generated `json:"resources,omitempty"`
	// ResourceTemplates generates virtual resource templates. Nil when absent.
	ResourceTemplates *Generated `json:"resourceTemplates,omitempty"`
	// Ordering selects catalog ordering. Nil when absent; schema default
	// "deterministic".
	Ordering *string `json:"ordering,omitempty"`
	// CollideWith copies another instance's generated names (MOCK-223.5). Nil
	// when absent.
	CollideWith *string `json:"collideWith,omitempty"`
	// ValidationTimeoutMs bounds schema validation (MOCK-225.7). Nil when
	// absent.
	ValidationTimeoutMs *int `json:"validationTimeoutMs,omitempty"`
	// Items are authored primitives (MOCK-222); a merge-by-name list. Nil when
	// absent.
	Items []AuthoredItem `json:"items,omitempty"`
	// EdgeCases configures named edge-case generators. Preserved verbatim
	// (later-phase shape); nil when absent.
	EdgeCases json.RawMessage `json:"edgeCases,omitempty"`
	// Drift configures catalog drift (MOCK-227/233). Preserved verbatim; nil
	// when absent.
	Drift json.RawMessage `json:"drift,omitempty"`
}

// Generated parametrises virtual primitive generation (schema
// #/$defs/generated). Count-controlled generation is O(1) memory (ADR-004).
type Generated struct {
	// Count is how many primitives to generate. Nil when absent; schema
	// default 0. A non-nil pointer to 0 is a deliberate "generate none".
	Count *int `json:"count,omitempty"`
	// NameTemplate templates generated names. Nil when absent.
	NameTemplate *string `json:"nameTemplate,omitempty"`
	// DescriptionWords sizes generated descriptions. Nil when absent.
	DescriptionWords *int `json:"descriptionWords,omitempty"`
	// SchemaShape selects the generated input-schema size. Nil when absent.
	SchemaShape *string `json:"schemaShape,omitempty"`
	// RequiredScopes lists scopes required to see these items (MOCK-228). Nil
	// when absent; replace-on-merge.
	RequiredScopes []string `json:"requiredScopes,omitempty"`
	// RequiredClientCapabilities lists required client capabilities (MOCK-206).
	// Nil when absent; replace-on-merge.
	RequiredClientCapabilities []string `json:"requiredClientCapabilities,omitempty"`
}

// AuthoredItem is an authored catalog primitive (schema #/$defs/authoredItem,
// MOCK-222). The InputSchema, OutputSchema, Annotations and Icons fields are
// emitted VERBATIM and are deliberately never validated or normalised by
// mcpmock (MOCK-222.3 / MOCK-701.7); they are [json.RawMessage] so an authored,
// intentionally-invalid schema is retained byte-for-byte, including key order.
type AuthoredItem struct {
	// Name is the primitive name; required by the schema and the merge key.
	Name string `json:"name"`
	// Kind is "tool", "prompt", "resource" or "resourceTemplate". Nil when
	// absent; schema default "tool".
	Kind *string `json:"kind,omitempty"`
	// Title is the display title. Nil when absent.
	Title *string `json:"title,omitempty"`
	// Description is the primitive description. Nil when absent.
	Description *string `json:"description,omitempty"`
	// URI addresses resources and templates. Nil when absent.
	URI *string `json:"uri,omitempty"`
	// Icons is emitted verbatim and never validated (MOCK-224). Nil when
	// absent.
	Icons json.RawMessage `json:"icons,omitempty"`
	// Annotations is emitted verbatim and never validated (MOCK-224). Nil when
	// absent.
	Annotations json.RawMessage `json:"annotations,omitempty"`
	// InputSchema is emitted verbatim and never validated (MOCK-222.3 /
	// MOCK-225). Nil when absent.
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
	// OutputSchema is emitted verbatim and never validated. Nil when absent.
	OutputSchema json.RawMessage `json:"outputSchema,omitempty"`
	// RequiredScopes lists scopes required to see this item. Nil when absent.
	RequiredScopes []string `json:"requiredScopes,omitempty"`
	// RequiredClientCapabilities lists required client capabilities. Nil when
	// absent.
	RequiredClientCapabilities []string `json:"requiredClientCapabilities,omitempty"`
	// Behavior configures the builtin tool behavior (echo/sleep/fail/canned).
	// Preserved verbatim (contracts/builtin-tools.md owns its shape); nil when
	// absent.
	Behavior json.RawMessage `json:"behavior,omitempty"`
}

// Journal configures the journal ring buffer (schema #/$defs/journal,
// MOCK-601/602, ADR-005).
type Journal struct {
	// Enabled toggles journalling. Nil when absent; schema default true.
	Enabled *bool `json:"enabled,omitempty"`
	// Bodies selects body capture mode: "full", "truncate", "digest" or "off".
	// Nil when absent; schema default "full". This is the field the ADR-005
	// cross-field rule reads.
	Bodies *string `json:"bodies,omitempty"`
	// TruncateBytes bounds captured bodies in truncate mode. Nil when absent.
	TruncateBytes *int `json:"truncateBytes,omitempty"`
	// MaxRecords bounds the ring by record count. Nil when absent.
	MaxRecords *int `json:"maxRecords,omitempty"`
	// MaxBytes bounds the ring by total bytes; the ADR-005 size-fault rule is
	// stated against maxBytes/16. Nil when absent; schema default 268435456.
	MaxBytes *int `json:"maxBytes,omitempty"`
	// MaxBytesTotal is the fleet-wide byte budget (ADR-007). Nil when absent.
	MaxBytesTotal *int `json:"maxBytesTotal,omitempty"`
	// Overflow selects overflow behavior. Nil when absent.
	Overflow *string `json:"overflow,omitempty"`
	// BlockTimeoutMs bounds block-mode overflow (GAP-017). Nil when absent.
	BlockTimeoutMs *int `json:"blockTimeoutMs,omitempty"`
	// ValidateArguments toggles tools/call argument validation (MOCK-606). Nil
	// when absent.
	ValidateArguments *bool `json:"validateArguments,omitempty"`
	// Preallocate eagerly allocates the ring. Nil when absent.
	Preallocate *bool `json:"preallocate,omitempty"`
}

// FaultRule is a single fault rule envelope (schema #/$defs/faultRule, §5).
// Phase 1 types the identifying and gating fields — which composition needs to
// merge rules by id — and preserves the trigger, selector and action bodies
// verbatim, because the full §5 action vocabulary lands in later phases and the
// JSON Schema already validates those bodies. The Action field is preserved so
// the ADR-005 cross-field rule can inspect size-fault bodies without this
// package fixing every action shape.
type FaultRule struct {
	// ID is the rule id; required by the schema and the merge key
	// (^[a-z0-9][a-z0-9-]{0,62}$).
	ID string `json:"id"`
	// Enabled toggles the rule. Nil when absent; schema default true.
	Enabled *bool `json:"enabled,omitempty"`
	// When is the selector. Preserved verbatim; nil when absent.
	When json.RawMessage `json:"when,omitempty"`
	// Trigger is the trigger (probability/count/armed). Preserved verbatim; nil
	// when absent.
	Trigger json.RawMessage `json:"trigger,omitempty"`
	// Once fires the rule at most once. Nil when absent.
	Once *bool `json:"once,omitempty"`
	// StopPropagation stops later rules once this fires. Nil when absent.
	StopPropagation *bool `json:"stopPropagation,omitempty"`
	// Action is the action body; required by the schema. Preserved verbatim so
	// later phases decode the full action vocabulary and Phase 1 semantic
	// checks (ADR-005) can inspect a size action. Nil only for an
	// action-less rule, which the schema rejects.
	Action json.RawMessage `json:"action,omitempty"`
}
