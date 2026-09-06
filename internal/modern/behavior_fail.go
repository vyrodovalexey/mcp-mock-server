package modern

import (
	"encoding/json"
	"net/http"

	"github.com/vyrodovalexey/mcp-mock-server/internal/engine"
	"github.com/vyrodovalexey/mcp-mock-server/internal/wire"
)

// behavior_fail.go implements the fail built-in (builtin-tools.md §4).
//
// # The two failure shapes (builtin-tools.md 4.1)
//
//   - toolError (DEFAULT, [P-49]): a SUCCESSFUL response carrying isError:true.
//     "The tool ran and failed" — a domain failure, HTTP 200. isError is emitted
//     ONLY when true (builtin-tools.md 4.2 [P-50]); a successful result omits it.
//   - protocolError (opt-in): a JSON-RPC error response with code (default
//     -32603, [P-51]) and the request id echoed with its original lexical form.
//     "The call itself failed" — the tool did not run.
//
// toolError is the default because isError:true is the MCP-native way for a tool
// to report failure and is the case hub authors most often mishandle
// (builtin-tools.md 4.1). fail's message and code are configuration, not
// generated, so the result is deterministic (builtin-tools.md 1.4).
//
// # Who selects the mode (builtin-tools.md 4.4 [P-52])
//
// The SCENARIO selects, not the caller. behavior.fail.allowClientOverride
// defaults to false; while false, arguments.mode/code/message are IGNORED (not
// rejected — ignored, so a shared fixture called by tests that do not know about
// the option cannot be flipped). Only allowClientOverride:true lets the caller
// win, per field, precedence arguments → behavior.fail → defaults.

// failParams is the fail configuration resolved for one call: the config values,
// possibly overridden by the caller's arguments when allowClientOverride is set.
type failParams struct {
	mode    FailMode
	message string
	code    int
	data    json.RawMessage
}

// fail builds the fail tool result in the resolved mode. In toolError mode it
// returns a successful result (nil fault); in protocolError mode it returns a
// *engine.Fault so the pipeline's single error encoder (emit.go) produces the
// JSON-RPC error response and echoes the request id with its original lexical
// form — the id echo is the engine's job, so fail needs no access to it here.
func (h *toolsCallHandler) fail(args callArgs, rc resultContext) (json.RawMessage, *engine.Fault) {
	fp := h.resolveFail(args)
	if fp.mode == FailProtocolError {
		return nil, protocolErrorFault(fp)
	}
	return toolErrorResult(fp, rc)
}

// resolveFail resolves the effective fail parameters. It starts from the
// scenario config and, ONLY when allowClientOverride is true, lets the caller's
// arguments.mode/code/message override per field (builtin-tools.md 4.4). While
// allowClientOverride is false the arguments are ignored entirely, so the
// scenario alone selects the mode.
func (h *toolsCallHandler) resolveFail(args callArgs) failParams {
	fp := failParams{
		mode:    h.cfg.Fail.Mode,
		message: h.cfg.Fail.Message,
		code:    h.cfg.Fail.Code,
		data:    h.cfg.Fail.Data,
	}
	if !h.cfg.Fail.AllowClientOverride {
		return fp
	}
	applyClientFailOverrides(&fp, args)
	return fp
}

// applyClientFailOverrides overlays the caller's arguments.mode/code/message
// onto fp, per field, when each is present and well-formed. A malformed override
// value is skipped (the config value stands) rather than rejected: this path is
// only reached when a scenario has explicitly opted into caller control, and a
// bad override should not fail a tool the scenario configured to fail anyway.
func applyClientFailOverrides(fp *failParams, args callArgs) {
	if raw, ok := args.obj["mode"]; ok {
		var m string
		if json.Unmarshal(raw, &m) == nil && (m == string(FailToolError) || m == string(FailProtocolError)) {
			fp.mode = FailMode(m)
		}
	}
	if raw, ok := args.obj["code"]; ok {
		var c int
		if json.Unmarshal(raw, &c) == nil {
			fp.code = c
		}
	}
	if raw, ok := args.obj["message"]; ok {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			fp.message = s
		}
	}
}

// toolErrorResult builds the toolError success result: a single text content
// block carrying the message, isError:true, and _meta.serverInfo.
// structuredContent is NOT emitted in toolError mode (builtin-tools.md 4.2).
func toolErrorResult(fp failParams, rc resultContext) (json.RawMessage, *engine.Fault) {
	isErr := true
	res := wire.ToolCallResult{
		ResultType: resultTypePtr(rc.method, rc.om),
		Content:    []wire.TextContentBlock{wire.NewTextContentBlock(fp.message)},
		IsError:    &isErr,
		Meta:       serverInfoMeta(rc.discover, rc.om),
	}
	return marshalToolResult(res)
}

// protocolErrorFault builds the *engine.Fault for a protocolError fail. The
// engine's single error encoder turns it into the JSON-RPC error response and
// echoes the request id with its original lexical form (builtin-tools.md 4.3).
// The code is the configured code (default -32603, sourced from wire), and any
// integer is permitted — the fail seam MOCK-505 (Phase 9) grows from here, so
// Phase 1 does not validate the code (builtin-tools.md 4.3 [P-51]).
//
// HTTP status is 200: a protocolError is a well-formed JSON-RPC error envelope,
// and 200 is the fail seam's status (builtin-tools.md 4.1 note; matches the
// engine's own -32601 [P-31] treatment). The Fault is built from its exported
// fields with the code from wire, so no wire literal appears here (ADR-019) and
// the wire error object is still produced only in engine/emit.go.
func protocolErrorFault(fp failParams) *engine.Fault {
	return &engine.Fault{
		Code:       fp.code,
		HTTPStatus: http.StatusOK,
		Message:    fp.message,
		Data:       fp.data,
		Reason:     "fail tool: protocolError mode",
	}
}
