package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/vyrodovalexey/mcp-mock-server/internal/controlclient"
	"github.com/vyrodovalexey/mcp-mock-server/journalapi"
)

// ctlFlags holds the parsed `ctl` endpoint and journal-filter flags. Endpoint
// precedence is flag > env > default (AMEND-7): an explicit --url or --socket
// wins, else MCPMOCK_CONTROL_URL / MCPMOCK_CONTROL_SOCKET, else nothing (the
// user must point ctl at a server).
type ctlFlags struct {
	url    string
	socket string
	method string
	limit  int
}

// envControlURL and envControlSocketCtl are the environment fallbacks for the
// ctl endpoint, below the explicit flags (flag > env > default).
const (
	envControlURL       = "MCPMOCK_CONTROL_URL"
	envControlSocketCtl = "MCPMOCK_CONTROL_SOCKET"
)

// runCtl implements `mcpmock ctl <verb> [name]`. The verbs are generated from
// [controlclient.Verbs] — the same route table the server exposes (ADR-015) — so
// a verb cannot drift from the control API. It returns exitOK on success,
// exitIO on a bad invocation or a control-plane failure.
func runCtl(ctx context.Context, out, errOut io.Writer, args []string) int {
	verbs := controlclient.Verbs()
	if len(args) == 0 || isHelp(args[0]) {
		ctlUsage(errOut, verbs)
		if len(args) == 0 {
			return exitIO
		}
		return exitOK
	}
	verbName, rest := args[0], args[1:]
	verb, ok := findVerb(verbs, verbName)
	if !ok {
		fmt.Fprintf(errOut, "mcpmock ctl: unknown verb %q\n\n", verbName)
		ctlUsage(errOut, verbs)
		return exitIO
	}

	cf, name, ok := parseCtlArgs(errOut, verb, rest)
	if !ok {
		return exitIO
	}
	client, err := buildClient(cf)
	if err != nil {
		fmt.Fprintf(errOut, "mcpmock ctl: %v\n", err)
		return exitIO
	}
	return invokeVerb(ctx, out, errOut, verb, client, name, ctlQuery(cf))
}

// parseCtlArgs parses the endpoint/filter flags and the optional instance-name
// positional argument for a verb. It reports a usage error when a name-bearing
// verb is missing its name.
func parseCtlArgs(errOut io.Writer, verb controlclient.Verb, args []string) (cf ctlFlags, name string, ok bool) {
	fs := flag.NewFlagSet("ctl "+verb.Name, flag.ContinueOnError)
	fs.SetOutput(errOut)
	fs.StringVar(&cf.url, "url", "", "control API base URL (e.g. http://127.0.0.1:8081)")
	fs.StringVar(&cf.socket, "socket", "", "control API unix socket path")
	fs.StringVar(&cf.method, "method", "", "journal filter: JSON-RPC method glob")
	fs.IntVar(&cf.limit, "limit", 0, "journal filter: max records")
	fs.Usage = func() {
		fmt.Fprintf(errOut, "Usage: mcpmock ctl %s%s [flags]\n", verb.Name, nameArg(verb))
		fmt.Fprintln(errOut, "\nFlags:")
		fs.PrintDefaults()
	}
	// Reorder so positional arguments follow flags: stdlib flag stops parsing at
	// the first non-flag token, so "ctl instance default --url X" would leave
	// --url unparsed. Moving positionals last lets the operator write the name
	// before or after the flags.
	if err := fs.Parse(reorderArgs(args)); err != nil {
		return ctlFlags{}, "", false
	}
	rest := fs.Args()
	if verb.NeedsName {
		if len(rest) < 1 {
			fmt.Fprintf(errOut, "mcpmock ctl %s: an instance name is required\n", verb.Name)
			fs.Usage()
			return ctlFlags{}, "", false
		}
		name = rest[0]
	}
	return cf, name, true
}

// buildClient resolves the endpoint with flag > env > default precedence and
// constructs a control client. It refuses ambiguity (both url and socket) and
// requires at least one endpoint.
func buildClient(cf ctlFlags) (*controlclient.Client, error) {
	url := firstSet(cf.url, os.Getenv(envControlURL))
	socket := firstSet(cf.socket, os.Getenv(envControlSocketCtl))
	if url != "" && socket != "" {
		return nil, errors.New("set exactly one of --url / --socket (or their env vars), not both")
	}
	if url == "" && socket == "" {
		return nil, errors.New("no control endpoint: pass --url or --socket (or set " +
			envControlURL + " / " + envControlSocketCtl + ")")
	}
	return controlclient.New(controlclient.Endpoint{URL: url, Socket: socket})
}

// invokeVerb runs the verb against the client and renders its result as indented
// JSON to out (a no-body verb prints nothing). A control-plane error is written
// to errOut and mapped to exitIO.
func invokeVerb(
	ctx context.Context, out, errOut io.Writer, verb controlclient.Verb,
	client *controlclient.Client, name string, q journalapi.Query,
) int {
	val, err := verb.Invoke(ctx, client, name, q)
	if err != nil {
		fmt.Fprintf(errOut, "mcpmock ctl %s: %v\n", verb.Name, err)
		return exitIO
	}
	if val == nil {
		return exitOK
	}
	enc := json.NewEncoder(out)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(val); err != nil {
		fmt.Fprintf(errOut, "mcpmock ctl %s: encode result: %v\n", verb.Name, err)
		return exitIO
	}
	return exitOK
}

// ctlQuery builds the journal query from the filter flags.
func ctlQuery(cf ctlFlags) journalapi.Query {
	var q journalapi.Query
	q.Method = cf.method
	q.Limit = cf.limit
	return q
}

// ctlValueFlags are the ctl flag names that take a value; used by reorderArgs to
// tell a flag's value apart from a positional argument. Every ctl flag takes a
// value (there are no boolean ctl flags), so a token immediately after one of
// these is its value, not the instance name.
var ctlValueFlags = map[string]bool{"url": true, "socket": true, "method": true, "limit": true}

// reorderArgs moves positional arguments after all flags so stdlib flag parses
// the full flag set regardless of where the operator put the instance name. It
// understands both "--flag value" and "--flag=value"; a "--" terminator passes
// everything after it through as positionals unchanged.
func reorderArgs(args []string) []string {
	var flags, positionals []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positionals = append(positionals, args[i+1:]...)
			break
		}
		name, hasEq := flagName(a)
		if name == "" {
			positionals = append(positionals, a)
			continue
		}
		flags = append(flags, a)
		// A value-taking flag in "--flag value" form consumes the next token.
		if !hasEq && ctlValueFlags[name] && i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	return append(flags, positionals...)
}

// flagName returns the flag name of a "-x"/"--x"/"--x=v" token and whether it
// used the "=value" form, or "" when the token is not a flag.
func flagName(a string) (name string, hasEq bool) {
	if len(a) < 2 || a[0] != '-' {
		return "", false
	}
	body := strings.TrimLeft(a, "-")
	if body == "" {
		return "", false
	}
	if i := strings.IndexByte(body, '='); i >= 0 {
		return body[:i], true
	}
	return body, false
}

// findVerb looks up a verb by name.
func findVerb(verbs []controlclient.Verb, name string) (controlclient.Verb, bool) {
	for _, v := range verbs {
		if v.Name == name {
			return v, true
		}
	}
	return controlclient.Verb{}, false
}

// firstSet returns the first non-empty argument, or "".
func firstSet(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// isHelp reports whether arg is a help request.
func isHelp(arg string) bool {
	return arg == "-h" || arg == "--help" || arg == "help"
}

// nameArg renders the " <name>" argument hint for a name-bearing verb, or "".
func nameArg(verb controlclient.Verb) string {
	if verb.NeedsName {
		return " <name>"
	}
	return ""
}

// ctlUsage prints the ctl help, generating one line per verb from the route
// table (MOCK-104.3: the CLI surface is generated, not hand-maintained, so it
// cannot drift from the control API).
func ctlUsage(w io.Writer, verbs []controlclient.Verb) {
	fmt.Fprintln(w, "Usage: mcpmock ctl <verb> [name] [--url URL | --socket PATH] [flags]")
	fmt.Fprintln(w, "\nCall the control API of a running mcpmock server. The endpoint is resolved")
	fmt.Fprintln(w, "flag > env (MCPMOCK_CONTROL_URL / MCPMOCK_CONTROL_SOCKET) > (none).")
	fmt.Fprintln(w, "\nVerbs (generated from the control route table):")
	for _, v := range verbs {
		fmt.Fprintf(w, "  %-14s %s%s\n", v.Name+nameArg(v), v.Summary, opIDNote(v))
	}
	fmt.Fprintln(w, "\nExit codes: 0 success, 2 usage error or control-plane failure.")
}

// opIDNote renders the operationId a verb mirrors, so the generated help shows
// the contract operation each verb maps to.
func opIDNote(v controlclient.Verb) string {
	return "  [" + v.OpID + "]"
}
