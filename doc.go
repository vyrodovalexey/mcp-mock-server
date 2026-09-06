// Package mcpmock provides a programmable MCP (Model Context Protocol) server
// emulator usable as a test harness, either embedded directly as an in-process
// Go library or run as a standalone binary (cmd/mcpmock).
//
// It is the lifecycle facade of the mcpmock module (ADR-001): construct a
// [Server], reach its logical [Instance]s, drive them from a real MCP client,
// and assert against the request [journalapi.View] each instance records. The
// same facade backs both the embedded and the subprocess forms, so the two
// cannot diverge.
//
// # Import path
//
// This package is imported as:
//
//	import "github.com/vyrodovalexey/mcp-mock-server"     // package mcpmock
//
// Companion public packages a consumer may import without pulling in the full
// server graph are scenario, journalapi and assert (ADR-001 §6.2). A test that
// only inspects a captured journal imports journalapi and assert, which are
// standard-library-only.
//
// # Lifecycle
//
// [New], [NewFromFile] and [NewFromScenario] construct a Server. None of them
// binds a listener, starts a goroutine or performs I/O beyond reading a scenario
// file, so a Server may be constructed and never started (MOCK-107.6). [Server.Start]
// binds listeners and begins serving; [Server.Shutdown] and [Server.Close] stop,
// and Close returns only after every goroutine the Server started has exited
// (MOCK-107.7), so a host test suite never inherits a leaked goroutine.
//
// # Test ergonomics
//
// [StartTest] is the intended entry point for a hub test suite: it starts a
// Server on an ephemeral loopback port, registers Close with the test's cleanup,
// and fails the test on any startup error. It takes the minimal [TestingTB]
// interface, not a concrete *testing.T, so it works from tests, benchmarks and
// the non-test callers (the CLI and control plane) alike.
//
// # Isolation (ADR-007)
//
// A Server touches no process-global mutable state: no slog.SetDefault, no
// Prometheus default registerer, no otel global provider, no os.Stdout. Its
// registry, metrics, logger and listeners are all owned instance state. Two
// Servers therefore run simultaneously in one test binary without interfering —
// a t.Parallel() test that starts a fresh Server per subtest works — because
// they share nothing. Supply your own Prometheus registry with [WithRegisterer]
// and your own logger with [WithLogger].
//
// # stdio and the stdout hijack (ADR-011)
//
// The stdio transport takes its streams explicitly through [WithStdio]; the
// library never defaults them to os.Stdin/os.Stdout. That is what lets
// cmd/mcpmock capture the real fd 1 and hand it in as the protocol writer while
// replacing os.Stdout with a leak drainer. The library form writes protocol
// bytes only to the writer you pass and touches os.Stdout nowhere, preserving
// that hijack seam.
//
// # Journal memory (ADR-007)
//
// The journal ring is preallocated per instance, so the fleet-wide journal
// budget is a facade concern. This package defaults each instance to a
// 4 096-record ring bounded at 8 MiB rather than the journal package's own
// 100 000-record default. Measured across the MOCK-904 target of 200 instances,
// the facade default costs ~87 MiB total, against ~667 MiB for the 100 000-record
// ring and ~66 MiB with journalling off — so the default retains thousands of
// recent requests per instance while saving ~580 MiB over the naive full ring.
// A caller that wants the larger ring passes
// [WithJournal](journalapi.Config{Enabled: true, MaxRecords: 100000}); a caller
// measuring memory passes [WithoutJournal].
//
// # Errors
//
// Every error this package returns wraps one of the sentinels in
// contracts/library-api.md §5 ([ErrValidation], [ErrNotFound], [ErrAlreadyExists],
// [ErrNotStarted], [ErrClosed], [ErrUnsupported]), so a caller inspects an
// outcome with errors.Is. A scenario validation failure wraps [ErrValidation]
// while remaining errors.As-recoverable to the loader's detailed error, so a
// caller learns the JSON Pointer, file and line.
//
// # Stability
//
// This is a published contract, but the module is v0: any release may change
// this API until the wire annex (ADR-019) is ratified. mcpmock emulates
// requirements.md plus the authored 2026-07-28 annex and makes no conformance
// claim. See contracts/library-api.md for the full contract.
package mcpmock
