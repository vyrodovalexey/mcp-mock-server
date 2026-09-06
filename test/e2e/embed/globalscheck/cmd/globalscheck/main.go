// Command globalscheck runs the mechanical MOCK-107.5/107.6 audit over the
// mcpmock public packages and exits non-zero if any finding is reported. It is a
// RUNNABLE program (not wired into the root Makefile — that is TASK-033's scope;
// see the TASK-028 report) so it can be invoked directly:
//
//	go run ./globalscheck/cmd/globalscheck -root ../../..
//
// from the embed module, or with -root pointing at any mcpmock checkout.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/vyrodovalexey/mcp-mock-server-embedtest/globalscheck"
)

func main() {
	root := flag.String("root", "../../..", "path to the mcpmock module root to scan")
	flag.Parse()

	findings, err := globalscheck.RunReport(*root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "globalscheck: %v\n", err)
		os.Exit(2)
	}
	if len(findings) == 0 {
		fmt.Printf("globalscheck: OK — no mutable package-level state and no forbidden init() side effect in the public packages under %s\n", *root)
		return
	}
	fmt.Fprintf(os.Stderr, "globalscheck: %d finding(s) (MOCK-107.5/107.6):\n", len(findings))
	for _, f := range findings {
		fmt.Fprintf(os.Stderr, "  %s\n", f)
	}
	os.Exit(1)
}
