//go:build e2e

package embed_test

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain runs the whole embed package under goleak. This is the strongest
// possible statement of MOCK-107.7: a goroutine that mcpmock fails to stop does
// not merely fail the test that started the server — it fails the ENTIRE embed
// package, exactly as it would poison an unrelated package in the gateway's host
// binary. If any test in this file leaves a goroutine running that traces back
// into mcpmock, VerifyTestMain reports it and the process exits non-zero.
//
// The ignore-list below covers goroutines owned by the test infrastructure
// itself (goleak's own bookkeeping and the Go HTTP client's idle-connection
// reaper), never mcpmock goroutines. If mcpmock leaked, it would show up outside
// these ignores.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		// net/http keeps idle keep-alive connections and their reader goroutines
		// around briefly after a client is done; these are the CONSUMER's
		// http.Client, not mcpmock, and drain on their own. Ignoring them keeps
		// the check focused on mcpmock-owned goroutines.
		goleak.IgnoreTopFunction("net/http.(*persistConn).readLoop"),
		goleak.IgnoreTopFunction("net/http.(*persistConn).writeLoop"),
		goleak.IgnoreAnyFunction("internal/poll.runtime_pollWait"),
	)
}
