package modern

import (
	"context"
	"encoding/json"

	"github.com/vyrodovalexey/mcp-mock-server/internal/engine"
	"github.com/vyrodovalexey/mcp-mock-server/internal/wire"
	"github.com/vyrodovalexey/mcp-mock-server/scenario"
)

// tools_call.go implements the tools/call handler and the three built-in
// behaviors echo / sleep / fail (MOCK-202, MOCK-212, builtin-tools.md).
//
// Wire clauses it depends on (provisional, GAP-003): annex 4.2 [P-21] /
// builtin-tools.md 1.2 [P-39] (resultType "toolResult" and the toolResult
// envelope), builtin-tools.md 1.2 [P-40] (text-only content blocks),
// [P-42]/[P-43] (echo channels / non-coercion), [P-45]..[P-48] (sleep),
// [P-49]..[P-52] (fail). Every wire value comes from [wire] (ADR-019).

// toolsCallHandler is the tools/call [engine.Handler]. Unlike the other two
// handlers it carries state — the resolved [BuiltinConfig] — so it is a struct
// implementing engine.Handler rather than a bare func. The config is the
// scenario-derived behavior (builtin-tools.md 4.4 [P-52]: the scenario, not the
// caller, selects the mode), captured once at registration and read-only
// thereafter, so the handler stays safe for unbounded concurrent use.
type toolsCallHandler struct {
	// cfg is the fully-defaulted built-in behavior configuration.
	cfg BuiltinConfig
}

// newToolsCallHandler builds the tools/call handler from an already-defaulted
// config (RegisterHandlers applies withDefaults before calling this).
func newToolsCallHandler(cfg BuiltinConfig) engine.Handler {
	return &toolsCallHandler{cfg: cfg}
}

// Handle dispatches a tools/call to the named built-in behavior. It first
// validates the arguments shape uniformly (builtin-tools.md 1.3), then routes on
// the tool name. An unknown tool is a -32602 rather than a -32601: the METHOD
// (tools/call) exists and is enabled, so the failure is an invalid params.name,
// not a missing method (the engine reserves -32601 for a missing/disabled
// method at dispatch).
func (h *toolsCallHandler) Handle(ctx context.Context, ex *engine.Exchange) (json.RawMessage, *engine.Fault) {
	name, args, fault := decodeCall(ex)
	if fault != nil {
		return nil, fault
	}
	om := omissionsFor(ex.Snapshot)
	cfg := discoverConfig(ex.Snapshot)
	rc := resultContext{method: wire.MethodToolsCall, om: om, discover: cfg}

	switch name {
	case ToolEcho:
		return h.echo(args, rc)
	case ToolSleep:
		return h.sleep(ctx, args, rc)
	case ToolFail:
		return h.fail(args, rc)
	default:
		return nil, unknownToolFault(name)
	}
}

// resultContext carries the per-request values every behavior's result builder
// needs: the method (for the resultType lookup), the MOCK-209 omission switches,
// and the discover config (for the default serverInfo). It is a small value
// threaded through so each behavior does not re-resolve the snapshot.
type resultContext struct {
	method   string
	om       omissions
	discover *scenario.Discover
}

// callArgs is the decoded, validated tools/call arguments. present reports
// whether params.arguments was supplied at all (absent ⇒ treated as {}); obj is
// the decoded object (nil when absent). The raw bytes are retained so echo can
// re-canonicalize them without a second decode of the whole params.
type callArgs struct {
	present bool
	obj     map[string]json.RawMessage
	raw     json.RawMessage
}

// decodeCall extracts params.name and params.arguments from the request and
// enforces the uniform argument rule (builtin-tools.md 1.3): arguments is an
// object or absent; a present non-object arguments is a -32602 with
// data.reason "arguments_not_object", raised for every tool before any behavior
// runs. A missing params.name is a -32602 (a tools/call must name a tool).
func decodeCall(ex *engine.Exchange) (name string, args callArgs, fault *engine.Fault) {
	params := requestParams(ex)
	if len(params) == 0 {
		return "", callArgs{}, invalidParamsReason("mcpmock: tools/call requires params", "missing_params")
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(params, &obj); err != nil {
		return "", callArgs{}, invalidParamsReason("mcpmock: params must be an object", "params_not_object")
	}
	if raw, ok := obj[wire.ParamsKeyName]; !ok || json.Unmarshal(raw, &name) != nil || name == "" {
		return "", callArgs{}, invalidParamsReason("mcpmock: tools/call requires a tool name", "missing_name")
	}
	args, fault = decodeArguments(obj[wire.ParamsKeyArguments])
	return name, args, fault
}

// decodeArguments validates and decodes params.arguments. Absent arguments is
// legal and equivalent to {} (builtin-tools.md 1.3). A present arguments that is
// not a JSON object is the uniform -32602 "arguments_not_object".
func decodeArguments(raw json.RawMessage) (callArgs, *engine.Fault) {
	if len(raw) == 0 {
		return callArgs{present: false, obj: nil, raw: nil}, nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return callArgs{}, invalidParamsReason(
			"mcpmock: arguments must be an object", "arguments_not_object")
	}
	return callArgs{present: true, obj: obj, raw: raw}, nil
}

// marshalToolResult marshals a wire.ToolCallResult to byte-stable result JSON.
// It is the single emission point for a successful (toolResult) tools/call
// result, so every behavior's success path shares one encoder and one error
// mapping.
func marshalToolResult(res wire.ToolCallResult) (json.RawMessage, *engine.Fault) {
	body, err := wire.MarshalResult(res)
	if err != nil {
		return nil, engine.InvalidParamsFault("mcpmock: encoding tools/call result", nil).
			WithReason("marshal tools/call result: " + err.Error())
	}
	return body, nil
}

// unknownToolFault is the -32602 for a tools/call naming a tool the instance
// does not provide. It is invalid params (a bad params.name), distinct from the
// engine's -32601 for a missing/disabled METHOD.
func unknownToolFault(name string) *engine.Fault {
	data := mustMarshal(map[string]string{"tool": name})
	return engine.InvalidParamsFault("mcpmock: unknown tool", data).
		WithReason("unknown tool: " + name)
}

// invalidParamsReason builds a -32602 whose error.data carries a single
// {"reason": <reason>} member — the uniform argument-error shape builtin-tools.md
// uses (1.3 "arguments_not_object", 3.3 "negative_duration", …). The message is
// human text; the machine-readable cause is in data.reason.
func invalidParamsReason(message, reason string) *engine.Fault {
	data := mustMarshal(map[string]string{"reason": reason})
	return engine.InvalidParamsFault(message, data).WithReason(reason)
}

// echoedContentBlock builds the single text content block from an already-
// serialized canonical-JSON string, applying the configured prefix. It exists so
// the prefix concatenation happens in exactly one place.
func echoedContentBlock(prefix, canonical string) wire.TextContentBlock {
	return wire.NewTextContentBlock(prefix + canonical)
}
