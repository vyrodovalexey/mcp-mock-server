package main

import (
	"fmt"
	"io"
)

// Build-stamp variables, set at link time by the Makefile's -ldflags
// (-X main.version / main.commit / main.date, ADR-013 build flags). They are
// the ONLY package-level mutable state in the command, and they are written
// exactly once by the linker before main runs, so they are not the process
// globals ADR-007 forbids (they are effectively constants). Their zero values
// are the honest "built without stamping" defaults a `go run` produces.
var (
	// version is the release version (git describe), or "dev" when unstamped.
	version = "dev"
	// commit is the short git commit, or "none" when unstamped.
	commit = "none"
	// date is the RFC 3339 build time, or "unknown" when unstamped.
	date = "unknown"
)

// versionInfo is the stamped build identity, rendered by the version command.
type versionInfo struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Date    string `json:"date"`
}

// buildInfo returns the linker-stamped build identity.
func buildInfo() versionInfo {
	return versionInfo{Version: version, Commit: commit, Date: date}
}

// runVersion implements `mcpmock version`: it prints the stamped build identity
// (criterion 8) and returns exitOK. It writes to out (os.Stdout for the real
// command) so a test can capture it; version is not a stdio-protocol command, so
// stdout is the correct channel here.
func runVersion(out io.Writer, _ []string) int {
	bi := buildInfo()
	fmt.Fprintf(out, "mcpmock %s (commit %s, built %s)\n", bi.Version, bi.Commit, bi.Date)
	return exitOK
}
