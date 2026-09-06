package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// Fault is the structured error every pipeline stage and handler returns
// (architecture.md §8). It carries a JSON-RPC code, an HTTP status and a
// journalable reason string, so a stage failure maps cleanly to a wire error
// through the single encoder in emit.go — no fmt.Errorf string ever reaches the
// client where a JSON-RPC error is required.
//
// A Fault is NOT itself the wire error object: emit.go turns it into one. This
// separation is the ADR-019 / MOCK-505 discipline — Phase 9 fault codes and
// genuine stage errors share one encoder, and the code/status/data come from
// internal/wire, never from a literal here.
//
// Fault implements error and wraps an optional cause, so a call site can match a
// sentinel with errors.Is and inspect a typed cause with errors.As while still
// presenting a clean wire error to the client.
type Fault struct {
	// Code is the JSON-RPC error code, sourced from internal/wire (ADR-019).
	Code int
	// HTTPStatus is the HTTP status the response carries. For stdio it is
	// recorded on the journal record but not sent.
	HTTPStatus int
	// Message is the short wire error message (JSON-RPC error.message).
	Message string
	// Data is the optional wire error.data payload, preserved as raw JSON so an
	// authored shape (for example the -32602 data.missing payload from
	// internal/wire) survives byte-for-byte. Nil when absent.
	Data json.RawMessage
	// Reason is the journalable, human-readable cause. It is recorded on the
	// journal record (MOCK-601.6) and is NEVER sent to the client, so it may be
	// more specific than Message without leaking internals onto the wire.
	Reason string
	// cause is an optional wrapped error for errors.Is/As at the call site. It
	// is not serialized.
	cause error
}

// Error implements the error interface with a stable rendering including the
// code, so a Fault can be returned as a plain error and logged.
func (f *Fault) Error() string {
	if f.Reason != "" {
		return fmt.Sprintf("engine: fault code %d: %s (%s)", f.Code, f.Message, f.Reason)
	}
	return fmt.Sprintf("engine: fault code %d: %s", f.Code, f.Message)
}

// Unwrap returns the wrapped cause, enabling errors.Is and errors.As at the call
// site (architecture.md §8: errors.Is/As, not string matching).
func (f *Fault) Unwrap() error { return f.cause }

// WithReason returns a copy of f with the journalable reason set. It is used
// when a stage wants the same wire error but a more specific journalled cause.
func (f *Fault) WithReason(reason string) *Fault {
	c := *f
	c.Reason = reason
	return &c
}

// WithCause returns a copy of f wrapping cause for errors.Is/As, without
// changing the wire-visible fields.
func (f *Fault) WithCause(cause error) *Fault {
	c := *f
	c.cause = cause
	return &c
}

// newFault builds a Fault from a wire code, HTTP status and message. It is the
// single internal constructor the engine uses so every Fault's code and status
// trace back to internal/wire (ADR-019); the exported convenience faults in
// emit.go call it.
func newFault(code, httpStatus int, message string) *Fault {
	return &Fault{Code: code, HTTPStatus: httpStatus, Message: message}
}

// ErrPipeline is the sentinel a pipeline-internal failure wraps, so a caller can
// distinguish an engine fault from a transport or context error with errors.Is.
var ErrPipeline = errors.New("engine: pipeline")

// faultPreHook is the stage-5 request-phase fault hook. In Phase 1 it is a
// deliberate no-op: fault injection is Phase 9 (MOCK-501…508). It exists as a
// named, ordered stage so Phase 9 fills it by APPENDING request-phase actions
// here rather than inserting a new stage and shifting the ADR-002 draw order.
//
// It draws zero RNG values in Phase 1. The function is intentionally empty; the
// comment is its rationale, satisfying revive's empty-block rule.
func faultPreHook(_ context.Context, _ *Exchange) *Fault {
	// Phase 1 no-op: no request-phase faults exist yet (MOCK-501 is Phase 9).
	return nil
}

// faultPostHook is the stage-7 response/stream-phase fault hook. Like
// [faultPreHook] it is a Phase 1 no-op seam for Phase 9's response-phase and
// stream-phase actions, kept as an ordered stage so later work appends rather
// than inserts. It draws zero RNG values in Phase 1.
func faultPostHook(_ context.Context, _ *Exchange, _ json.RawMessage) *Fault {
	// Phase 1 no-op: no response-phase faults exist yet (MOCK-505 is Phase 9).
	return nil
}
