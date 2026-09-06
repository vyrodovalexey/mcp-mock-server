package mcpmock

import (
	"errors"
	"fmt"

	"github.com/vyrodovalexey/mcp-mock-server/internal/control"
)

// This file declares the wrapped sentinel errors the facade returns
// (contracts/library-api.md §5). Every error this package returns wraps one of
// these, so a caller inspects an outcome with errors.Is rather than matching on
// a message string. The sentinels are a published contract: their identity is
// stable across v0, and callers branch on them.
//
// Phase 1 exposes the subset the lifecycle and construction paths can actually
// produce. ErrValidation is wrapped by the config loader (internal/config) and
// surfaces unchanged through NewFromFile/NewFromScenario. The remaining
// sentinels named in §5 that only the control plane (TASK-023) and later-phase
// features can raise (ErrRateLimited, ErrControlAuth, ErrTransport, …) are
// introduced by the tasks that can return them, so a caller never sees a
// declared-but-unreachable sentinel in Phase 1.

var (
	// ErrValidation reports that a scenario failed schema or semantic
	// validation (contracts/library-api.md §5). It is non-retryable: the
	// scenario must be fixed. NewFromFile, NewFromScenario and the WithOverlay
	// composition path wrap it; the wrapped value carries the JSON Pointer,
	// source file and line the internal loader produced, so errors.As to the
	// loader's *config.ValidationError still recovers the detail.
	ErrValidation = errors.New("mcpmock: scenario validation failed")

	// ErrNotFound reports that a named instance does not exist
	// (contracts/library-api.md §5). Non-retryable. Instance(name) reports
	// absence through its bool result rather than this error; ErrNotFound is
	// returned by the control plane's lookups (TASK-023). It wraps the internal
	// control sentinel so a single not-found error satisfies both
	// errors.Is(err, mcpmock.ErrNotFound) for a facade caller and the control
	// front end's own 404 mapping — one sentinel identity, two views (ADR-015).
	ErrNotFound = fmt.Errorf("mcpmock: not found: %w", control.ErrNotFound)

	// ErrAlreadyExists reports that an instance name or mount path collides
	// with one already registered (contracts/library-api.md §5). It is how the
	// facade refuses to silently overwrite an instance when a scenario or an
	// overlay defines the same name or mount path twice. Non-retryable.
	ErrAlreadyExists = errors.New("mcpmock: instance name or mount path already exists")

	// ErrNotStarted reports lifecycle misuse: an operation that requires a
	// started Server was called before Start (contracts/library-api.md §5).
	// Non-retryable; it indicates a programming error.
	ErrNotStarted = errors.New("mcpmock: server not started")

	// ErrClosed reports that the Server has been shut down: after Close or a
	// completed Shutdown, lifecycle methods return it rather than acting on a
	// torn-down Server (contracts/library-api.md §5, §6). Non-retryable.
	ErrClosed = errors.New("mcpmock: server closed")

	// ErrUnsupported reports that an operation is not available in the current
	// mode or Phase (contracts/library-api.md §5) — for example a facade
	// constructed with no listener has no MCP URL, or a control operation that
	// is Phase 2+. Non-retryable. It wraps the internal control sentinel so one
	// identity serves both the facade caller and the control front end's 501
	// mapping (ADR-015).
	ErrUnsupported = fmt.Errorf("mcpmock: operation not supported in this mode: %w", control.ErrUnsupported)
)
