package wire

// resultType — the value carried on every result identifying the shape of that
// result (MOCK-209.1). The value set is annex 4.2 [P-21], of which Phase 1
// carries three of the eventual eleven values; the other eight belong to
// methods that do not exist in Phase 1 and are added, as table entries, in
// Phase 2+ (implementation-plan.md split table).
//
// Per ADR-019 and TASK-010 acceptance criterion 2, resultType is looked up
// through a TABLE keyed by method, not a switch statement: adding a method is a
// table entry, not a code change.

// resultType values — Phase 1 subset of annex 4.2 [P-21].
const (
	// ResultTypeDiscovery is the resultType of a server/discover result.
	// annex 4.2. [P-21].
	ResultTypeDiscovery = "discovery"
	// ResultTypeToolList is the resultType of a tools/list result. annex 4.2.
	// [P-21].
	ResultTypeToolList = "toolList"
	// ResultTypeToolResult is the resultType of a tools/call result. annex 4.2
	// / builtin-tools.md 1.2. [P-21] / [P-39].
	ResultTypeToolResult = "toolResult"
)

// resultTypeByMethod is the annex 4.2 [P-21] table for the Phase 1 method
// surface. It is the single lookup that maps a method to the resultType its
// result must carry. It is package-private and read-only; the exported
// [ResultTypeForMethod] is the accessor, so no caller can mutate the table and
// no wire literal is copied out of this file.
//
// To add a method in Phase 2, add one entry here — that is the whole change on
// the wire-vocabulary side.
var resultTypeByMethod = map[string]string{
	MethodDiscover:  ResultTypeDiscovery,
	MethodToolsList: ResultTypeToolList,
	MethodToolsCall: ResultTypeToolResult,
}

// ResultTypeForMethod returns the resultType value that a result for the given
// method must carry, and whether the method is known to the Phase 1 table.
//
// A false second return is not an error condition in itself: it is how a
// dispatcher learns that a method is outside the Phase 1 vocabulary (for
// example one of the six Phase 2 methods, which have no entry here by design).
// Returning ("", false) rather than panicking keeps the Phase 2/absent surface
// failing quietly-at-lookup rather than crashing, while still being
// distinguishable from a legitimately empty value.
func ResultTypeForMethod(method string) (string, bool) {
	rt, ok := resultTypeByMethod[method]
	return rt, ok
}

// Phase1ResultTypes returns the closed set of resultType values reachable in
// Phase 1, in the table's method order. It is used by the annex-parity test and
// by any caller that needs to enumerate the known resultType values without
// hard-coding literals.
func Phase1ResultTypes() []string {
	return []string{
		resultTypeByMethod[MethodDiscover],
		resultTypeByMethod[MethodToolsList],
		resultTypeByMethod[MethodToolsCall],
	}
}
