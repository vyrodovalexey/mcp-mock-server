package modern

import (
	"encoding/json"
	"net/http"

	"github.com/vyrodovalexey/mcp-mock-server/internal/engine"
	"github.com/vyrodovalexey/mcp-mock-server/internal/wire"
)

// selfcheck.go wires the outgoing-result self-check (MOCK-201.4,
// switches.selfCheck, AMEND-3) into the three Phase 1 handlers. The VALIDATION
// logic lives in internal/wire ([wire.SelfCheckResult]) — wire owns the result
// shapes and the AMEND-4 subset it validates against (ADR-019) — and this file is
// the era-side wiring: it reads the per-instance switch off the snapshot, decides
// whether to run the check at all, and turns a failure into the MOCK-201.4
// failure mode.
//
// # Failure mode (MOCK-201.4, decided and documented)
//
// A self-check FAILURE is a server-side JSON-RPC -32603 (Internal error) PLUS an
// ERROR log naming the schema path — NEVER a silently emitted invalid result.
// Both halves matter and both are here:
//
//   - the -32603 fault replaces the result, so the malformed body is never
//     written to the client (the check "actually rejects", not merely logs), and
//     its Reason names the wire.SchemaRef and the specific violation, so the
//     journal record (MOCK-601.6) carries the cause;
//   - the ERROR log (through the injected [SelfCheckLogger]) names the schema
//     path and the violation, so a failure is visible to an operator even when
//     the journal is off. A self-check whose failure nobody sees is not a
//     self-check.
//
// # Provisional oracle (GAP-003)
//
// The check validates against the AMEND-4 wire subset, which is PROVISIONAL and
// UNRATIFIED (GAP-003, x-conformance-claim NONE): a green self-check proves the
// result is self-consistent with mcpmock's OWN authored annex, NOT that it
// interoperates with any real MCP peer. wire.SchemaRef carries this marker into
// every log line and reason so nobody reads a passing self-check as conformance.
//
// # Off-by-default cost (MOCK-901)
//
// The self-check sits on the ≥20 000 rps hot path but is a development aid, so it
// must cost effectively nothing when disabled. [selfChecker.check] reads a single
// immutable bool off the snapshot (Snapshot.SelfCheck) and returns before any
// work when it is false — no parse, no allocation — matching the one-boolean-
// check discipline the journal capture path uses. The validator in internal/wire
// is entered only after that guard passes.

// SelfCheckLogger is the CONSUMER-DEFINED surface the self-check logs a failure
// through. It is exactly the ERROR-level slice of *slog.Logger, so the facade
// injects a per-instance *slog.Logger (obs.InstanceLogger) directly, WITHOUT
// internal/modern importing internal/obs (architecture.md §6.1: modern stays a
// wire consumer below the obs layer). A nil logger is legal and disables the log
// line; the -32603 rejection and its journalled reason are unaffected, so the
// check still rejects observably in a logger-less test or embedding.
type SelfCheckLogger interface {
	// Error logs at ERROR level with structured key/value args, matching
	// (*slog.Logger).Error. The self-check calls it with the schema ref and the
	// violation so the failure is attributable.
	Error(msg string, args ...any)
}

// selfCheckSnapshot is the CONSUMER-DEFINED surface carrying the MOCK-201.4
// switch. It is separate from the other handler surfaces because it is optional:
// a snapshot that does not expose it reports self-check OFF, which is the Phase 1
// default when a scenario sets no switch, so the check is opt-in and the hot path
// is untouched for a bare test double.
type selfCheckSnapshot interface {
	// SelfCheck reports whether the outgoing-result self-check is on
	// (switches.selfCheck, MOCK-201.4). Default false ⇒ no check.
	SelfCheck() bool
}

// selfChecker holds the self-check wiring captured once at registration: the
// optional failure logger. It is immutable after construction and safe for
// unbounded concurrent use — check reads only the per-request snapshot and body,
// and the logger is itself concurrency-safe (*slog.Logger is). One selfChecker
// is shared by all three handlers of an instance.
type selfChecker struct {
	// log is the ERROR-level failure logger, or nil to disable the log line.
	log SelfCheckLogger
}

// selfCheckLogMessage is the ERROR log message a self-check failure emits. It is
// a constant so the log stream is greppable; the schema ref and the specific
// violation are structured fields, not baked into the message.
const selfCheckLogMessage = "mcpmock: self-check rejected an outgoing result"

// Structured log/field keys for a self-check failure. They are observability
// labels, not wire vocabulary, so they are named here (the same class as the
// engine's own outcome constants) rather than sourced from internal/wire.
const (
	selfCheckLogKeySchema    = "schema"
	selfCheckLogKeyMethod    = "method"
	selfCheckLogKeyViolation = "violation"
)

// check runs the self-check for one outgoing result and returns a non-nil
// [*engine.Fault] to REJECT it (replacing the body with a -32603) or nil to allow
// it. It is the single call the handlers make after building a result body.
//
// The disabled path is the first and cheapest: when switches.selfCheck is off (or
// the snapshot does not expose the switch), it returns nil immediately, having
// touched only one bool — so a production instance with the check off pays a
// single predictable branch and no allocation on the hot path (MOCK-901). Only
// when the switch is on does it enter wire.SelfCheckResult.
func (s *selfChecker) check(snap engine.Snapshot, method string, body json.RawMessage) *engine.Fault {
	scs, ok := snap.(selfCheckSnapshot)
	if !ok || !scs.SelfCheck() {
		return nil
	}
	if err := wire.SelfCheckResult(body); err != nil {
		return s.fail(method, err)
	}
	return nil
}

// fail turns a self-check violation into the MOCK-201.4 failure mode: it emits
// the ERROR log naming the schema path and the violation (when a logger is
// injected) and returns a -32603 whose Reason carries the same detail for the
// journal. The malformed body is discarded by the caller returning this fault, so
// it is never written to the client.
func (s *selfChecker) fail(method string, err error) *engine.Fault {
	if s.log != nil {
		s.log.Error(selfCheckLogMessage,
			selfCheckLogKeySchema, wire.SchemaRef,
			selfCheckLogKeyMethod, method,
			selfCheckLogKeyViolation, unwrapSelfCheck(err),
		)
	}
	return &engine.Fault{
		Code:       wire.ErrCodeInternal,
		HTTPStatus: http.StatusInternalServerError,
		Message:    selfCheckErrorMessage,
		Reason:     "self-check failed against " + wire.SchemaRef + ": " + unwrapSelfCheck(err),
	}
}

// selfCheckErrorMessage is the -32603 wire error.message a self-check rejection
// carries. Like the engine's other error messages it is human-readable text, not
// wire vocabulary, and it deliberately does NOT name the specific violation — the
// detail lives in the journalled Reason and the ERROR log, so the client sees a
// stable, non-leaking internal-error message.
const selfCheckErrorMessage = "Internal error"

// unwrapSelfCheck renders a wire self-check error as a plain reason string,
// stripping the wire sentinel prefix so the ERROR log and the journalled reason
// read as one sentence rather than repeating "self-check failed".
func unwrapSelfCheck(err error) string {
	msg := err.Error()
	const prefix = "wire: self-check failed: "
	if len(msg) > len(prefix) && msg[:len(prefix)] == prefix {
		return msg[len(prefix):]
	}
	return msg
}
