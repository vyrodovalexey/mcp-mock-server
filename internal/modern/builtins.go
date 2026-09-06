package modern

import (
	"encoding/json"

	"github.com/vyrodovalexey/mcp-mock-server/internal/wire"
)

// builtins.go carries the three Phase 1 built-in tools' identity: their names,
// their advertised inputSchema (emitted through tools/list), and the scenario-
// derived behavior configuration the tools/call handler resolves by name.
//
// Every shape here is builtin-tools.md's, and every item it depends on is
// provisional (GAP-003): [P-41]/[P-42] (echo), [P-45]/[P-46]/[P-47] (sleep),
// [P-49]/[P-51]/[P-52] (fail). mcpmock makes no conformance claim.

// Built-in tool names (builtin-tools.md 1.1). They are NOT wire vocabulary
// (method names / resultType values / error codes are the ADR-019-contained
// set); they are tool identities the scenario author types, the same class as an
// authored catalog item name, so they are named here rather than in internal/wire.
const (
	// ToolEcho is the zero-surprise round-trip tool (builtin-tools.md §2).
	ToolEcho = "echo"
	// ToolSleep is the cancellable-delay tool (builtin-tools.md §3, MOCK-212).
	ToolSleep = "sleep"
	// ToolFail is the always-fails tool (builtin-tools.md §4).
	ToolFail = "fail"
)

// builtinNames is the fixed, ordered set of built-in tool names
// (builtin-tools.md 1.1 [D]: echo, sleep, fail). The order is load-bearing —
// tools/list golden fixtures depend on it (MOCK-202.20) — so it is a single
// ordered source both the descriptor list and any enumerating caller use.
func builtinNames() []string {
	return []string{ToolEcho, ToolSleep, ToolFail}
}

// FailMode selects which of the two failure shapes a fail behavior produces
// (builtin-tools.md 4.1). It is scenario configuration, not a wire value.
type FailMode string

const (
	// FailToolError is the DEFAULT fail mode (builtin-tools.md 4.1 [P-49]): a
	// SUCCESSFUL response carrying isError:true. It models a domain failure —
	// "the tool ran and failed" — and is HTTP 200.
	FailToolError FailMode = "toolError"
	// FailProtocolError is the opt-in fail mode: a JSON-RPC error response. It
	// models "the call itself failed"; the tool did not run.
	FailProtocolError FailMode = "protocolError"
)

// EchoBehavior configures the echo tool (builtin-tools.md §2). Its zero value is
// the default behavior: echo the whole arguments object in both channels.
type EchoBehavior struct {
	// Key, when non-empty, selects arguments[Key] to echo instead of the whole
	// arguments object (builtin-tools.md 2.4 [P-42]). An absent key at call time
	// is a -32602 (builtin-tools.md 2.5 [P-44]).
	Key string
	// Prefix is prepended to content[0].text (builtin-tools.md 2.4). Empty by
	// default.
	Prefix string
	// Structured controls whether structuredContent is emitted. The default is
	// true; a scenario sets it false to suppress the structured channel
	// (builtin-tools.md 2.4). Modeled as a *bool so the zero value (nil) means
	// "use the default (true)".
	Structured *bool
}

// SleepBehavior configures the sleep tool (builtin-tools.md §3). Its zero value,
// after defaulting, is a 0 ms sleep bounded at 30 000 ms with reject-on-exceed.
type SleepBehavior struct {
	// SleepMs is the configured delay used when the client does not supply
	// arguments.durationMs (or is not allowed to). builtin-tools.md 3.3.
	SleepMs int
	// MaxSleepMs bounds the effective delay (builtin-tools.md 3.3 [P-46],
	// default 30 000). A request exceeding it is rejected or clamped per
	// OnExceedMax.
	MaxSleepMs int
	// AllowClientDuration lets arguments.durationMs override SleepMs
	// (builtin-tools.md 3.3, default true). *bool so nil means default-true.
	AllowClientDuration *bool
	// OnExceedMax selects reject (default) or clamp when the effective delay
	// exceeds MaxSleepMs (builtin-tools.md 3.3 [P-47]).
	OnExceedMax ExceedMode
}

// ExceedMode selects the sleep behavior when the requested delay exceeds the
// maximum (builtin-tools.md 3.3 [P-47]).
type ExceedMode string

const (
	// ExceedReject is the DEFAULT (builtin-tools.md 3.3 [P-47]): the call fails
	// with -32602 IMMEDIATELY, incurring no delay, so a hub timeout test cannot
	// pass for the wrong reason via silent clamping.
	ExceedReject ExceedMode = "reject"
	// ExceedClamp reduces the delay to MaxSleepMs and marks the result
	// _meta.clamped:true so a deliberate bound-probing test can observe it.
	ExceedClamp ExceedMode = "clamp"
)

// FailBehavior configures the fail tool (builtin-tools.md §4). Its zero value,
// after defaulting, is mode=toolError with the default message and code -32603.
type FailBehavior struct {
	// Mode selects toolError (default) or protocolError (builtin-tools.md 4.1
	// [P-49]).
	Mode FailMode
	// Message is the failure message placed in the toolError content block or
	// the protocolError error.message (builtin-tools.md 4.2/4.3).
	Message string
	// Code is the protocolError JSON-RPC error code (builtin-tools.md 4.3
	// [P-51], default -32603). Ignored in toolError mode.
	Code int
	// Data is the optional protocolError error.data, emitted verbatim when set.
	Data json.RawMessage
	// AllowClientOverride lets the CALLER's arguments.mode/code/message win over
	// this scenario config (builtin-tools.md 4.4 [P-52], default false). While
	// false the scenario selects the mode, not the caller.
	AllowClientOverride bool
}

// BuiltinConfig is the scenario-derived behavior configuration for the three
// built-in tools, plus the default serverInfo the results carry. It is passed to
// [RegisterHandlers] once at instance construction and captured by the tools/call
// handler; the scenario — never the client — sets it (builtin-tools.md 4.4
// [P-52]). Its zero value is the all-defaults configuration.
type BuiltinConfig struct {
	// Echo configures the echo tool. Zero value ⇒ whole-arguments echo.
	Echo EchoBehavior
	// Sleep configures the sleep tool. Zero value ⇒ 0 ms, max 30 000, reject.
	Sleep SleepBehavior
	// Fail configures the fail tool. Zero value ⇒ toolError, code -32603.
	Fail FailBehavior
}

// defaultMaxSleepMs is builtin-tools.md 3.3 [P-46]'s maxSleepMs default: 30 000
// ms (30 s), under the Go test timeout with wide margin while still long enough
// to exercise a hub's own 1–30 s timeout.
const defaultMaxSleepMs = 30000

// defaultFailCode is builtin-tools.md 4.3 [P-51]'s default protocolError code:
// -32603 Internal error, the honest JSON-RPC code for a server-side failure. It
// is sourced from [wire] (ADR-019) rather than written as a literal.
var defaultFailCode = wire.ErrCodeInternal

// defaultFailMessage is the fail message emitted when the scenario configures
// none. It is human-readable error text (like the engine's own error messages),
// not wire vocabulary, so it is named here.
const defaultFailMessage = "mcpmock: fail tool invoked"

// withDefaults returns a copy of the config with every absent field replaced by
// its builtin-tools.md default. It is applied once at registration so the
// tools/call handler reads fully-resolved values and never re-defaults on the
// hot path. The pointer fields (Structured, AllowClientDuration) are left as-is
// because their nil-means-default semantics are resolved at use.
func (c BuiltinConfig) withDefaults() BuiltinConfig {
	if c.Sleep.MaxSleepMs == 0 {
		c.Sleep.MaxSleepMs = defaultMaxSleepMs
	}
	if c.Sleep.OnExceedMax == "" {
		c.Sleep.OnExceedMax = ExceedReject
	}
	if c.Fail.Mode == "" {
		c.Fail.Mode = FailToolError
	}
	if c.Fail.Code == 0 {
		c.Fail.Code = defaultFailCode
	}
	if c.Fail.Message == "" {
		c.Fail.Message = defaultFailMessage
	}
	return c
}

// builtinDescriptors returns the three built-in tools as tools/list descriptors,
// in the fixed builtin-tools.md 1.1 order, each carrying its advertised
// inputSchema verbatim (builtin-tools.md §2.2 / §3.5 / §4.5). The schemas are
// advertised, NOT enforced in Phase 1 (argument validation is MOCK-606, Phase 3,
// builtin-tools.md 1.3), so they are informational for the client.
func builtinDescriptors() []wire.ToolDescriptor {
	out := make([]wire.ToolDescriptor, 0, len(builtinNames()))
	for _, name := range builtinNames() {
		out = append(out, wire.ToolDescriptor{
			Name:        name,
			Description: builtinDescription(name),
			InputSchema: builtinInputSchema(name),
		})
	}
	return out
}

// builtinDescription returns the short description advertised for a built-in
// tool. Descriptions are human text, not wire vocabulary.
func builtinDescription(name string) string {
	switch name {
	case ToolEcho:
		return "Echoes its arguments back. Any JSON object is accepted."
	case ToolSleep:
		return "Delays for a bounded number of milliseconds, observing cancellation."
	case ToolFail:
		return "Always fails. Shape is scenario-controlled unless overridden."
	default:
		return ""
	}
}

// builtinInputSchema returns the advertised inputSchema for a built-in tool,
// verbatim from builtin-tools.md §2.2 / §3.5 / §4.5. It is returned as raw JSON
// with sorted, fixed key order so the tools/list bytes are golden-stable, and it
// is emitted but never enforced in Phase 1 (builtin-tools.md 1.3). An unknown
// name yields nil (no inputSchema key).
func builtinInputSchema(name string) json.RawMessage {
	switch name {
	case ToolEcho:
		return json.RawMessage(echoInputSchema)
	case ToolSleep:
		return json.RawMessage(sleepInputSchema)
	case ToolFail:
		return json.RawMessage(failInputSchema)
	default:
		return nil
	}
}

// The advertised inputSchemas, transcribed verbatim from builtin-tools.md and
// compacted to a single stable byte form (no insignificant whitespace) so the
// tools/list response is byte-identical across runs.
const (
	echoInputSchema = `{"$schema":"https://json-schema.org/draft/2020-12/schema",` +
		`"type":"object",` +
		`"description":"Echoes its arguments back. Any JSON object is accepted.",` +
		`"properties":{"message":{"type":"string",` +
		`"description":"Conventional field for the common case. Not required."}},` +
		`"additionalProperties":true}`

	sleepInputSchema = `{"$schema":"https://json-schema.org/draft/2020-12/schema",` +
		`"type":"object",` +
		`"description":"Delays for a bounded number of milliseconds, observing cancellation.",` +
		`"properties":{"durationMs":{"type":"integer","minimum":0,` +
		`"description":"Delay in MILLISECONDS. Bounded by behavior.maxSleepMs (default 30000). ` +
		`Ignored when behavior.allowClientDuration is false."}},` +
		`"additionalProperties":false}`

	failInputSchema = `{"$schema":"https://json-schema.org/draft/2020-12/schema",` +
		`"type":"object",` +
		`"description":"Always fails. Shape is scenario-controlled unless ` +
		`behavior.fail.allowClientOverride is true.",` +
		`"properties":{` +
		`"mode":{"enum":["toolError","protocolError"],` +
		`"description":"Honored only when behavior.fail.allowClientOverride is true."},` +
		`"code":{"type":"integer",` +
		`"description":"protocolError only; honored only when allowClientOverride is true."},` +
		`"message":{"type":"string",` +
		`"description":"Honored only when allowClientOverride is true."}},` +
		`"additionalProperties":false}`
)
