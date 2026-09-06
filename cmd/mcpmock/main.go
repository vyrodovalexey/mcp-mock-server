package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
)

// Exit codes are a contract: CI and scripts branch on them, so they are defined
// once here and documented in --help (MOCK-701.5). They must never change
// meaning across a release.
const (
	// exitOK is a successful run: `serve` shut down gracefully, `validate`
	// found no problems, `ctl` succeeded.
	exitOK = 0
	// exitValidation is a scenario validation failure (`validate` on an invalid
	// document, or `serve` refusing an invalid scenario). MOCK-701 requires
	// `validate` to exit non-zero on error; this is that non-zero code.
	exitValidation = 1
	// exitIO is an I/O or usage failure: a missing/unreadable file, an unknown
	// subcommand or flag, a bad endpoint, a control-plane error. Distinct from
	// exitValidation so CI can tell "the scenario is wrong" from "I could not
	// read it / you invoked me wrong" (MOCK-701.5).
	exitIO = 2
)

// usage is the top-level help text. It is written to stderr and describes the
// subcommands and the exit-code contract, so an operator sees the codes CI
// depends on without reading the source.
const usage = `mcpmock — deterministic mock MCP server (Phase 1)

Usage:
  mcpmock <command> [flags]

Commands:
  serve       run the mock server (stdio and/or http transports)
  validate    validate a scenario file without serving it
  ctl         call the control API of a running server
  version     print the build version, commit and date

Run "mcpmock <command> --help" for command-specific flags.

Exit codes:
  0  success
  1  scenario validation error
  2  I/O error, unknown command, or unknown flag
`

// main is the process entry point. It resolves the subcommand and delegates,
// converting the chosen command's integer exit code into the process status.
// The context is created once here and propagated into serve; it is never
// recreated mid-flight (ADR-007).
func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is main's testable core: it dispatches args[0] to a subcommand and returns
// its exit code, writing normal output to out and diagnostics to errOut. An
// unknown or missing subcommand prints usage to errOut and returns exitIO
// (criterion 7: never a panic).
func run(args []string, out, errOut io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(errOut, usage)
		return exitIO
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "serve":
		// serve writes protocol frames only to stdout (via the stdio transport /
		// hijack), so it takes no general-purpose stdout writer; all diagnostics
		// go to errOut.
		return runServe(errOut, rest)
	case "validate":
		return runValidate(out, errOut, rest)
	case "ctl":
		return runCtl(context.Background(), out, errOut, rest)
	case "version":
		return runVersion(out, rest)
	case "-h", "--help", "help":
		fmt.Fprint(errOut, usage)
		return exitOK
	default:
		fmt.Fprintf(errOut, "mcpmock: unknown command %q\n\n", cmd)
		fmt.Fprint(errOut, usage)
		return exitIO
	}
}

// signalContext returns a context canceled on SIGINT or SIGTERM plus its stop
// func, so `serve` performs a graceful shutdown on either signal (MOCK-212). It
// is created once — after the facade is constructed, so the context-free
// constructor is not in a ctx-carrying call chain — and threaded into the run
// loop; it is never recreated mid-flight (ADR-007). The caller calls stop to
// release the signal handler.
func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
}
