//go:build e2e

package embed_test

import (
	"io"
	"os"
	"testing"
	"time"
)

// readAllWithDeadline drains r to EOF but never blocks the test indefinitely: if
// the read does not complete within a bounded window it returns what it has with
// a timeout error. This matters precisely in the failure case TC-028.5 is
// written to catch — if mcpmock DID stream to os.Stdout, a naive io.ReadAll on
// an unclosed pipe would hang CI instead of failing the test.
func readAllWithDeadline(t *testing.T, r *os.File) ([]byte, error) {
	t.Helper()
	type result struct {
		data []byte
		err  error
	}
	done := make(chan result, 1)
	go func() {
		data, err := io.ReadAll(r)
		done <- result{data: data, err: err}
	}()
	select {
	case res := <-done:
		_ = r.Close()
		return res.data, res.err
	case <-time.After(5 * time.Second):
		_ = r.Close() // unblock the reader goroutine so it can exit.
		<-done
		return nil, io.ErrNoProgress
	}
}
