package jsonrpc

// Standard JSON-RPC 2.0 error codes (§5.1).
//
// These are the codes JSON-RPC 2.0 itself defines and are stable independent of
// GAP-003: JSON-RPC 2.0 is a ratified standard, so unlike the MCP-specific
// codes (-32020/-32021/-32022, which live in internal/wire and are Phase 2)
// they are safe to name here as literals. The Code field of an [Error] is a
// plain int, never an enum, because MOCK-505 (Phase 9) must be able to emit
// arbitrary and retired codes from the reserved range; these constants name the
// codes mcpmock produces itself in Phase 1.
const (
	// CodeParseError (-32700): invalid JSON was received; the server could not
	// parse the request. JSON-RPC 2.0 §5.1.
	CodeParseError = -32700
	// CodeInvalidRequest (-32600): the JSON sent is not a valid Request object.
	// JSON-RPC 2.0 §5.1.
	CodeInvalidRequest = -32600
	// CodeMethodNotFound (-32601): the method does not exist or is not
	// available. JSON-RPC 2.0 §5.1.
	CodeMethodNotFound = -32601
	// CodeInvalidParams (-32602): invalid method parameters. JSON-RPC 2.0 §5.1;
	// also the code MOCK-203 uses for a missing _meta field.
	CodeInvalidParams = -32602
	// CodeInternalError (-32603): internal JSON-RPC error. JSON-RPC 2.0 §5.1;
	// the default code for a builtin fail(mode: protocolError).
	CodeInternalError = -32603
)

// ReservedServerErrorMin and ReservedServerErrorMax bound the range JSON-RPC
// 2.0 §5.1 reserves for implementation-defined server errors (-32000 to
// -32099). The MCP codes and MOCK-505's retired codes live in or near this
// range; the bounds are exported for callers that need to classify a code
// without importing the MCP wire layer.
const (
	// ReservedServerErrorMin is the inclusive lower bound of the
	// implementation-defined server-error range.
	ReservedServerErrorMin = -32099
	// ReservedServerErrorMax is the inclusive upper bound of the
	// implementation-defined server-error range.
	ReservedServerErrorMax = -32000
)
