package jsonrpc_test

import (
	"errors"
	"testing"

	"github.com/vyrodovalexey/mcp-mock-server/internal/jsonrpc"
)

// asRPCError reports whether err is (or wraps) a *jsonrpc.Error and, if so,
// stores it in target. It is the test-side equivalent of errors.As specialised
// to the concrete error type this package returns.
func asRPCError(err error, target **jsonrpc.Error) bool {
	return errors.As(err, target)
}

// mustRawID builds a raw id from s, failing the test if it is not a valid id.
func mustRawID(t *testing.T, s string) jsonrpc.ID {
	t.Helper()
	id, err := jsonrpc.RawID([]byte(s))
	if err != nil {
		t.Fatalf("mustRawID(%q): %v", s, err)
	}
	return id
}
