// Command mcpmock is the mcpmock server binary.
//
// This package is the single, contained home of the ADR-011 stdout hijack
// (TASK-021): in stdio-transport mode stdout is the MCP frame channel, so a
// stray write to os.Stdout anywhere in the process would corrupt the protocol
// stream. To make that structurally impossible rather than a review obligation,
// [Install] captures the real fd 1, replaces os.Stdout process-wide with a pipe,
// and drains everything else to stderr as counted "stdout_leak" diagnostics.
//
// The hijack lives ONLY here, under cmd/, and never in the library: the library
// form of the stdio transport takes an explicit writer (internal/transport/stdio
// Config.Out) so that importing mcpmock as a library mutates no process global
// (ADR-007, MOCK-107). The hijack is installed by an explicit call in stdio mode
// only — never by an init() side effect, never on import — and is fully reversed
// by [Hijack.Restore], so an in-process test harness can install and uninstall
// it around a stdio run without corrupting its host's stdout.
//
// # The CLI (TASK-024)
//
// This package is the process-mode front end of ADR-015's "one interface, three
// front ends". It has four subcommands, dispatched by a ~90-line stdlib-flag
// dispatcher (ADR-013 rejected cobra for its init cost against the 200 ms
// startup budget, MOCK-107):
//
//   - serve     — run the mock server over stdio and/or http. In stdio mode it
//     installs the [Install] hijack FIRST, hands [Hijack.ProtoOut] to the stdio
//     transport, routes every diagnostic (including the effective-seed record)
//     to stderr, and restores the hijack on graceful shutdown.
//   - validate  — validate a scenario file without serving. Exit 0 valid, 1
//     validation error, 2 I/O error (MOCK-701.5); --output json emits the
//     JSON Pointer and source per problem.
//   - ctl       — call the control API of a running server over --url (TCP) or
//     --socket (UDS). Its verbs are generated from the shared control route
//     table via internal/controlclient (MOCK-104.3), so they cannot drift.
//   - version   — print the -ldflags build stamp (MOCK-101).
//
// The seed is resolved in the CLI (--seed, else a cryptographically random one),
// printed on stderr as a structured "effective seed" record (MOCK-704.2), and
// passed to the facade so the printed value equals the facade's effective seed.
//
// The control client lives in internal/controlclient, NOT in internal/control:
// co-locating an HTTP client with the control handler trips gosec's G704 (SSRF)
// through package-level taint aggregation. The sibling package dials only the
// operator-supplied endpoint, so the split is honest and needs no //nolint.
package main
