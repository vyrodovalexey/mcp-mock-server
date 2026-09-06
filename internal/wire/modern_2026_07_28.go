package wire

// This file holds the modern-era (revision 2026-07-28) scalar vocabulary:
// protocol identity, the Phase 1 method-name set, and the request param keys.
// It is the file that ratification of GAP-003 edits for these items; per
// ADR-019 no copy of these literals exists in any handler.

// Protocol identity. The 2026-07-28 revision is authored and UNRATIFIED
// (GAP-003); these constants carry no conformance claim (see doc.go).
const (
	// ProtocolRevision is the modern protocol revision string this package's
	// vocabulary describes. annex §0 / preamble. [R] — the revision is named
	// in requirements.md; its wire semantics are provisional pending GAP-003.
	ProtocolRevision = "2026-07-28"

	// JSONRPCVersion is the JSON-RPC envelope version every request and
	// response carries. annex 2.1. [D JSON-RPC 2.0]. It equals
	// jsonrpc.Version; it is restated here only so a reader of the wire
	// vocabulary sees the whole surface in one place, and the parity test
	// asserts the two agree.
	JSONRPCVersion = "2.0"
)

// Method names — annex §5 (server/discover) and §4 (tools/*), the Phase 1
// method set (task-breakdown G-1, AMEND-3). These three are the ENTIRE Phase 1
// method surface. The remaining six methods of MOCK-202 are Phase 2 and are
// deliberately absent: there is no constant for them, so a Phase 1 dispatcher
// cannot name one.
const (
	// MethodDiscover is the discovery call. annex §5. [R MOCK-201].
	MethodDiscover = "server/discover"
	// MethodToolsList lists the tool catalog. annex §4 / MOCK-202.
	// [R MOCK-202].
	MethodToolsList = "tools/list"
	// MethodToolsCall invokes a tool. annex §4 / MOCK-202. [R MOCK-202].
	MethodToolsCall = "tools/call"
)

// Request param keys. annex §2 / §4 / builtin-tools.md §1.3.
const (
	// ParamsKey is the JSON-RPC params object key. annex 2.5. [D JSON-RPC 2.0].
	ParamsKey = "params"

	// ParamsKeyName names the tool to call in a tools/call request. annex 3.3 /
	// builtin-tools.md 1.3. [R MOCK-204] (that a primitive name exists) +
	// [P-12] (that it is params.name for tools).
	ParamsKeyName = "name"

	// ParamsKeyArguments carries the tool arguments object in a tools/call
	// request. builtin-tools.md 1.3. [D] — absent is equivalent to {}.
	ParamsKeyArguments = "arguments"
)

// Phase1Methods returns the closed, ordered set of methods this package's
// vocabulary covers in Phase 1. It is used by the annex-parity test to assert
// that the constant set matches the annex Phase 1 surface, and by a dispatcher
// that wants to enumerate the known methods without hard-coding literals.
//
// The order is the annex's presentation order (discover, list, call) and is not
// otherwise significant.
func Phase1Methods() []string {
	return []string{MethodDiscover, MethodToolsList, MethodToolsCall}
}
