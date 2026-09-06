package control

import (
	"errors"
	"net/http"
)

// This file carries the control API's error object and its code↔HTTP-status
// mapping, exactly as the contract's error catalogue specifies
// (contracts/control-api.openapi.yaml #/components/schemas/Error and the ERROR
// CATALOGUE table). Every 4xx/5xx response is one [errorObject] so a client
// branches on the stable machine-readable code, and a message never carries a
// credential (security.md §5).

// Stable machine-readable error codes (contracts/control-api.openapi.yaml
// Error.code enum). Phase 1 produces a subset; the full set is declared so the
// mapping table is complete and a later phase adds no new code silently.
const (
	codeNotFound         = "not_found"
	codeAlreadyExists    = "already_exists"
	codeConflict         = "conflict"
	codeValidationFailed = "validation_failed"
	codeRateLimited      = "rate_limited"
	codeUnauthorized     = "unauthorized"
	codeForbidden        = "forbidden"
	codeUnsupported      = "unsupported"
	codeClosed           = "closed"
	codeInternal         = "internal"
)

// Sentinel errors a [Backend] returns; the front end maps each to its contract
// code and HTTP status through [statusFor]. They are wrapped, so a caller uses
// errors.Is. The root package maps ErrNotFound / ErrUnsupported onto its own
// published sentinels.
var (
	// ErrNotFound reports that a named instance does not exist (404 not_found).
	ErrNotFound = errors.New("control: not found")
	// ErrUnsupported reports that an operation is not available in this
	// mode/phase (501 unsupported).
	ErrUnsupported = errors.New("control: operation not supported")
	// ErrValidation reports a malformed request body or query (422
	// validation_failed).
	ErrValidation = errors.New("control: validation failed")
	// ErrUnauthorized reports a missing or invalid bearer token (401
	// unauthorized). It is returned by [requireBearer] on a non-loopback TCP
	// control listener when the Authorization header is absent or does not match
	// the configured token (security.md §2.1, REV-001).
	ErrUnauthorized = errors.New("control: unauthorized")
)

// errorObject is the wire error the contract requires: a stable code, a
// human-readable message that never contains credentials, and optional
// structured details (contracts/control-api.openapi.yaml #/components/schemas/Error).
type errorObject struct {
	// Code is the stable machine-readable code from the catalogue.
	Code string `json:"code"`
	// Message is human-readable and never contains a credential.
	Message string `json:"message"`
	// Details carries optional per-field diagnostics.
	Details []errorDetail `json:"details,omitempty"`
	// Retryable reports whether retrying may succeed.
	Retryable bool `json:"retryable,omitempty"`
}

// errorDetail is one structured diagnostic within an [errorObject].
type errorDetail struct {
	Pointer string `json:"pointer,omitempty"`
	Keyword string `json:"keyword,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// codeToStatus is the contract's error catalogue as a table: code → HTTP status.
// It is the single mapping both the handler and a client's status→code inference
// read, so the two cannot drift.
var codeToStatus = map[string]int{
	codeNotFound:         http.StatusNotFound,
	codeAlreadyExists:    http.StatusConflict,
	codeConflict:         http.StatusConflict,
	codeValidationFailed: http.StatusUnprocessableEntity,
	codeRateLimited:      http.StatusTooManyRequests,
	codeUnauthorized:     http.StatusUnauthorized,
	codeForbidden:        http.StatusForbidden,
	codeUnsupported:      http.StatusNotImplemented,
	codeClosed:           http.StatusServiceUnavailable,
	codeInternal:         http.StatusInternalServerError,
}

// statusFor maps a Go error to its contract error code, HTTP status and a safe
// message. The status is looked up from [codeToStatus] so the catalogue table is
// the single source of the code→status mapping. An unrecognized error is
// codeInternal with no detail leaked (the contract's "internal: the response
// includes no detail").
func statusFor(err error) (code string, status int, message string) {
	switch {
	case errors.Is(err, ErrNotFound):
		code, message = codeNotFound, err.Error()
	case errors.Is(err, ErrUnsupported):
		code, message = codeUnsupported, err.Error()
	case errors.Is(err, ErrValidation):
		code, message = codeValidationFailed, err.Error()
	case errors.Is(err, ErrUnauthorized):
		code, message = codeUnauthorized, "unauthorized"
	default:
		// Never surface an internal error's text: it may name internal state.
		code, message = codeInternal, "internal error"
	}
	return code, statusForCode(code), message
}

// statusForCode returns the HTTP status for a contract error code from the
// catalogue table, defaulting to 500 for an unknown code.
func statusForCode(code string) int {
	if s, ok := codeToStatus[code]; ok {
		return s
	}
	return http.StatusInternalServerError
}

// retryable reports whether a code invites a retry, per the catalogue's retry
// column. It backs [errorObject.Retryable].
func retryable(code string) bool {
	return code == codeRateLimited || code == codeClosed
}
