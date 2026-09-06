package mcpmock

import "context"

// TestingTB is the minimal testing interface [StartTest] reports through. Any
// *testing.T, *testing.B or testing.TB satisfies it, so StartTest(t, …) works
// unchanged in a normal test, benchmark or fuzz target. It is deliberately a
// small, exported interface rather than a hard *testing.T dependency, matching
// the idiom the assert package already established (assert.TestingT): a hard
// *testing.T would make the facade unusable from the CLI and the control API,
// and would drag the testing package into every consumer's non-test build.
//
// The methods mirror the subset of testing.TB StartTest needs:
//
//   - Helper marks the caller a test helper so a failure points at the test.
//   - Fatalf reports a fatal failure and stops the test.
//   - Cleanup registers a function to run when the test finishes.
//
// Stability: v0.
type TestingTB interface {
	// Helper marks the calling function as a test helper.
	Helper()
	// Fatalf reports a formatted fatal failure and stops the test.
	Fatalf(format string, args ...any)
	// Cleanup registers fn to run when the test and its subtests complete.
	Cleanup(fn func())
}

// StartTest constructs and starts a Server bound to an ephemeral loopback port,
// registers Close with tb.Cleanup, and fails the test on any construction or
// startup error (contracts/library-api.md §1). It is the intended entry point
// for hub test suites (MOCK-107): a test that only calls StartTest and never
// closes the Server still leaks nothing, because cleanup tears it down.
//
// StartTest defaults to an ephemeral HTTP listener on 127.0.0.1 so
// [Instance.URL] returns a dialable address; a caller wanting a different
// transport (stdio over pipes, an own listener) passes the matching option,
// which wins over the default. StartTest performs the full New+Start sequence
// inside the MOCK-107 startup budget.
func StartTest(tb TestingTB, opts ...Option) *Server {
	tb.Helper()

	// Default to an ephemeral loopback HTTP listener unless the caller opted
	// into a transport explicitly (WithAddr, WithListener or WithStdio). We
	// detect that by pre-applying the options into a probe and checking whether
	// any transport was enabled; if not, we prepend an ephemeral WithAddr.
	effective := opts
	if !anyTransport(opts) {
		effective = append([]Option{WithAddr("127.0.0.1:0")}, opts...)
	}

	s, err := New(effective...)
	if err != nil {
		tb.Fatalf("mcpmock.StartTest: construct server: %v", err)
		return nil // unreachable when tb.Fatalf stops the goroutine
	}
	tb.Cleanup(func() { _ = s.Close() })

	if err := s.Start(context.Background()); err != nil {
		tb.Fatalf("mcpmock.StartTest: start server: %v", err)
		return nil
	}
	return s
}

// anyTransport reports whether opts enable any transport, so StartTest only
// supplies its ephemeral-HTTP default when the caller chose none. It applies the
// options to a throwaway probe, which is side-effect-free (options only mutate
// the struct they are given).
func anyTransport(opts []Option) bool {
	probe := &options{}
	for _, o := range opts {
		o.apply(probe)
	}
	return probe.httpEnabled || probe.stdioEnabled
}
