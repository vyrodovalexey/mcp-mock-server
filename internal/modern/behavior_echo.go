package modern

import (
	"encoding/json"

	"github.com/vyrodovalexey/mcp-mock-server/internal/engine"
	"github.com/vyrodovalexey/mcp-mock-server/internal/jsonrpc"
	"github.com/vyrodovalexey/mcp-mock-server/internal/wire"
)

// behavior_echo.go implements the echo built-in (builtin-tools.md §2).
//
// echo returns the whole params.arguments object (or arguments[key] when
// behavior.echo.key is set) in two channels:
//
//   - content[0].text: the echoed value as CANONICAL JSON (keys sorted, no
//     insignificant whitespace — RFC 8785 via jsonrpc.CanonicalJSON), prefixed
//     by behavior.echo.prefix. Canonical key ordering is what makes echo
//     golden-stable regardless of the order the client serialized its arguments
//     (builtin-tools.md 2.5 [D]).
//   - structuredContent.echoed: the echoed value verbatim as JSON, TYPES
//     PRESERVED — a number stays a number, null stays null, a nested object
//     stays an object (builtin-tools.md 2.5 [P-43]). Omitted when
//     behavior.echo.structured is false.
//
// Non-string arguments are neither coerced nor rejected (builtin-tools.md 2.5
// [P-43]). echo draws no RNG and reads no clock — it is deterministic by
// construction, independent of seed (builtin-tools.md 1.4).

// echoObject is the canonical JSON for an empty arguments object. When arguments
// is absent or {}, content[0].text is "{}" (builtin-tools.md 2.4).
const echoObject = "{}"

// echo builds the echo tool result. It resolves the value to echo (the whole
// arguments object, or arguments[key]), canonicalizes it for the text channel,
// and — unless suppressed — carries it verbatim in structuredContent.
func (h *toolsCallHandler) echo(args callArgs, rc resultContext) (json.RawMessage, *engine.Fault) {
	value, fault := h.echoValue(args)
	if fault != nil {
		return nil, fault
	}

	canonical, err := canonicalString(value)
	if err != nil {
		return nil, engine.InvalidParamsFault("mcpmock: echo canonicalisation failed", nil).
			WithReason("canonicalize echo value: " + err.Error())
	}

	res := wire.ToolCallResult{
		ResultType: resultTypePtr(rc.method, rc.om),
		Content:    []wire.TextContentBlock{echoedContentBlock(h.cfg.Echo.Prefix, canonical)},
		Meta:       serverInfoMeta(rc.discover, rc.om),
	}
	if h.echoStructured() {
		res.StructuredContent = structuredEchoed(value)
	}
	return marshalToolResult(res)
}

// echoValue returns the JSON value echo should echo. With no configured key it
// is the whole arguments object (or "{}" when arguments is absent). With a
// configured behavior.echo.key it is arguments[key]; an ABSENT key is a -32602
// with data.missing naming the key, NOT an echoed null, so a test can tell
// "absent" from "explicitly null" (builtin-tools.md 2.5 [P-44]).
func (h *toolsCallHandler) echoValue(args callArgs) (json.RawMessage, *engine.Fault) {
	if h.cfg.Echo.Key == "" {
		if !args.present || len(args.obj) == 0 {
			return json.RawMessage(echoObject), nil
		}
		return args.raw, nil
	}
	raw, ok := args.obj[h.cfg.Echo.Key]
	if !ok {
		data := mustMarshal(wire.MetaMissingErrorData{Missing: []string{h.cfg.Echo.Key}})
		return nil, engine.InvalidParamsFault(
			"mcpmock: echo key not present in arguments", data).
			WithReason("echo key not present: " + h.cfg.Echo.Key)
	}
	return raw, nil
}

// echoStructured reports whether the structuredContent channel is emitted. It is
// on by default; behavior.echo.structured:false suppresses it. The *bool nil
// value means "use the default (true)".
func (h *toolsCallHandler) echoStructured() bool {
	if h.cfg.Echo.Structured == nil {
		return true
	}
	return *h.cfg.Echo.Structured
}

// structuredEchoed wraps the echoed value as {"echoed": <value>} for the
// structured channel, preserving the value's JSON verbatim (types intact). The
// wrapper is built with a canonical single-key object so its bytes are stable;
// the inner value is carried as raw JSON.
func structuredEchoed(value json.RawMessage) json.RawMessage {
	// A single-key object cannot vary in key order, so a plain marshal of the
	// wrapper with the raw value embedded is byte-stable. Using RawMessage keeps
	// the inner value verbatim (no re-canonicalisation, so nested authored order
	// is preserved for the structured channel per builtin-tools.md 2.5).
	wrapper := struct {
		Echoed json.RawMessage `json:"echoed"`
	}{Echoed: value}
	return mustMarshal(wrapper)
}

// canonicalString returns the RFC 8785 canonical JSON encoding of value as a Go
// string, so it can be placed inside content[0].text. It routes through
// internal/jsonrpc.CanonicalJSON — the same canonicalizer ADR-002's derivation
// path uses — so key sorting is identical to the rest of the module and the text
// block is golden-stable (builtin-tools.md 2.5 [D]).
func canonicalString(value json.RawMessage) (string, error) {
	canon, err := jsonrpc.CanonicalJSON(value)
	if err != nil {
		return "", err
	}
	return string(canon), nil
}
