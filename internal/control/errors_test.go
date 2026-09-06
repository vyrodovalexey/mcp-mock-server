package control

import (
	"errors"
	"fmt"
	"net/http"
	"testing"
)

// TestStatusFor maps each Phase 1 sentinel to its contract code and status, and
// confirms an unknown error is internal/500 with no leaked detail.
func TestStatusFor(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantCode   string
		wantStatus int
		wantMsg    string // "" means "don't care about exact text"
	}{
		{"not found", fmt.Errorf("%w: x", ErrNotFound), codeNotFound, http.StatusNotFound, ""},
		{"unsupported", ErrUnsupported, codeUnsupported, http.StatusNotImplemented, ""},
		{"validation", ErrValidation, codeValidationFailed, http.StatusUnprocessableEntity, ""},
		{"unauthorized", ErrUnauthorized, codeUnauthorized, http.StatusUnauthorized, "unauthorized"},
		{"unknown is internal", errors.New("some internal detail"), codeInternal, http.StatusInternalServerError, "internal error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, status, msg := statusFor(tt.err)
			if code != tt.wantCode {
				t.Errorf("code = %q, want %q", code, tt.wantCode)
			}
			if status != tt.wantStatus {
				t.Errorf("status = %d, want %d", status, tt.wantStatus)
			}
			if tt.wantMsg != "" && msg != tt.wantMsg {
				t.Errorf("msg = %q, want %q", msg, tt.wantMsg)
			}
			// The internal case must never leak the underlying detail.
			if tt.wantCode == codeInternal && msg == tt.err.Error() {
				t.Errorf("internal error leaked its detail: %q", msg)
			}
		})
	}
}

// TestCodeToStatusCoversCatalogue asserts every catalogue code has a status, so
// a new code cannot be added without a mapping.
func TestCodeToStatusCoversCatalogue(t *testing.T) {
	codes := []string{
		codeNotFound, codeAlreadyExists, codeConflict, codeValidationFailed,
		codeRateLimited, codeUnauthorized, codeForbidden, codeUnsupported,
		codeClosed, codeInternal,
	}
	for _, c := range codes {
		if _, ok := codeToStatus[c]; !ok {
			t.Errorf("code %q missing from codeToStatus table", c)
		}
	}
	if len(codeToStatus) != len(codes) {
		t.Errorf("codeToStatus has %d entries, expected %d", len(codeToStatus), len(codes))
	}
}

// TestRetryable checks the retry column of the catalogue.
func TestRetryable(t *testing.T) {
	if !retryable(codeRateLimited) || !retryable(codeClosed) {
		t.Error("rate_limited and closed must be retryable")
	}
	if retryable(codeNotFound) || retryable(codeValidationFailed) {
		t.Error("not_found and validation_failed must not be retryable")
	}
}
