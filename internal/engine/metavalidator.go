package engine

import (
	"context"

	"github.com/vyrodovalexey/mcp-mock-server/journalapi"
)

// metavalidator.go defines the stage-4 seam (architecture.md §5 stage 4). The
// pipeline calls a [MetaValidator] at stage 4; TASK-017 supplies one that
// implements MOCK-203 (_meta presence validation with strict/lenient/off
// strictness), and Phase 1 uses the permissive [AcceptAllMeta] default. The
// validation LOGIC is TASK-017's; the engine owns only the seam and its
// contract, so TASK-017 inserts itself by providing a validator through the
// snapshot rather than by restructuring this pipeline.

// MetaValidator is the stage-4 validation hook. It inspects the decoded request
// (the envelope and its params._meta, both reachable from ex) and returns either
// nil to accept, or a [*Fault] to reject — the -32602 / HTTP 400 the strict mode
// raises for a missing required _meta field (MOCK-203.1-.4). It also returns the
// journalable validation outcome so stage 9 records what a strict server would
// have required, which is how lenient mode stays observable (MOCK-203.5-.8).
//
// It is called with ex.Ctx as ctx (contextcheck) and MUST NOT mutate ex beyond
// what a validator legitimately reads. It draws no RNG values (ADR-002: stage 4
// is a zero-draw stage), so appending validation here does not shift the draw
// order of later stages.
type MetaValidator interface {
	// ValidateMeta returns a fault to reject the request, or nil to accept it,
	// together with the outcome to journal (nil when nothing is recorded). A
	// non-nil fault and a non-nil part may be returned together: strict mode
	// rejects AND records what was missing.
	ValidateMeta(ctx context.Context, ex *Exchange) (*Fault, *journalapi.MetaValidationPart)
}

// MetaValidatorFunc adapts a function to a [MetaValidator], for validators that
// need no receiver state.
type MetaValidatorFunc func(
	ctx context.Context, ex *Exchange,
) (*Fault, *journalapi.MetaValidationPart)

// ValidateMeta calls f. It makes a MetaValidatorFunc a [MetaValidator].
func (f MetaValidatorFunc) ValidateMeta(
	ctx context.Context, ex *Exchange,
) (*Fault, *journalapi.MetaValidationPart) {
	return f(ctx, ex)
}

// AcceptAllMeta is the Phase 1 default stage-4 validator: it accepts every
// request and records no validation outcome. A snapshot with no configured
// validator returns it, so the pipeline can call the seam unconditionally
// without a nil check. TASK-017 replaces it with a validator that honors
// switches.validateMeta; until then, stage 4 is a genuine pass-through, which is
// the "pass-through stage still executes in order" property the stage-order test
// asserts.
var AcceptAllMeta MetaValidator = MetaValidatorFunc(
	func(_ context.Context, _ *Exchange) (*Fault, *journalapi.MetaValidationPart) {
		return nil, nil
	},
)
