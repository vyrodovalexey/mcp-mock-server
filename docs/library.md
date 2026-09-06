# Library / embedding guide

Audience: **contributor** / **integrator** — you want a mock MCP server
running inside your own Go test binary, not as a separate process.

```go
import mcpmock "github.com/vyrodovalexey/mcp-mock-server"
```

## Table of contents

- [Quick example](#quick-example)
- [`StartTest` — the intended entry point](#starttest--the-intended-entry-point)
- [`New` / `NewFromFile` / `NewFromScenario`](#new--newfromfile--newfromscenario)
- [Options reference](#options-reference)
- [Startup cost](#startup-cost)
- [The `assert` package](#the-assert-package)
- [Reading a journal with no server running](#reading-a-journal-with-no-server-running)

## Quick example

This is a real, running example from the module
([`example_test.go`](../example_test.go)), verified with `go test -run
Example_embedded .`:

```go
package mcpmock_test

import (
	"context"
	"fmt"

	mcpmock "github.com/vyrodovalexey/mcp-mock-server"
	"github.com/vyrodovalexey/mcp-mock-server/test/mcpclient"
)

func Example_embedded() {
	srv, err := mcpmock.New(
		mcpmock.WithSeed(42),          // deterministic
		mcpmock.WithAddr("127.0.0.1:0"), // ephemeral loopback port
	)
	if err != nil {
		panic(err)
	}
	if err := srv.Start(context.Background()); err != nil {
		panic(err)
	}
	defer func() { _ = srv.Close() }()

	inst, _ := srv.Instance("default")

	client, _ := mcpclient.NewHTTP(inst.URL())
	defer func() { _ = client.Close() }()

	resp, _ := client.ListTools(context.Background(), mcpclient.IntID(1))

	fmt.Println("got response:", resp.GotResponse)
	fmt.Println("is error:", resp.Envelope.Error != nil)
	fmt.Println("journal records:", inst.Journal().Len())

	// Output:
	// got response: true
	// is error: false
	// journal records: 1
}
```

## `StartTest` — the intended entry point

For a hub test suite, `mcpmock.StartTest` is the entry point: it constructs
and starts a `Server` on an ephemeral loopback port, registers cleanup with
`tb.Cleanup`, and fails the test on any construction or startup error.

```go
func TestMyHub(t *testing.T) {
	srv := mcpmock.StartTest(t, mcpmock.WithSeed(1))
	inst, _ := srv.Instance("default")
	// drive your hub against inst.URL() ...
	// no explicit Close() needed — StartTest registered it with t.Cleanup.
}
```

`StartTest` accepts `*testing.T`, `*testing.B`, or anything satisfying the
minimal `mcpmock.TestingTB` interface (`Helper`, `Fatalf`, `Cleanup`) — so it
also works from a fuzz target or a non-standard test harness.

If you pass no transport option, `StartTest` defaults to an ephemeral HTTP
listener so `Instance.URL()` is dialable. Passing `WithStdio` or
`WithAddr`/`WithListener` explicitly overrides that default.

## `New` / `NewFromFile` / `NewFromScenario`

| Function | Signature | Use |
|---|---|---|
| `New` | `New(opts ...Option) (*Server, error)` | Programmatic configuration only (options), no scenario file. |
| `NewFromFile` | `NewFromFile(path string, opts ...Option) (*Server, error)` | Load a scenario document from disk, then apply `opts` (and any `WithOverlay` documents) on top. |
| `NewFromScenario` | `NewFromScenario(s *scenario.Document, opts ...Option) (*Server, error)` | Start from an in-memory `*scenario.Document` you built or parsed yourself. |

None of these starts the server — call `srv.Start(ctx)` explicitly, or use
`StartTest`, which does both.

## Options reference

Every `With*` function in the module, verified against
[`options.go`](../options.go):

| Option | Effect |
|---|---|
| `WithSeed(uint64)` | Sets the effective root seed (`MOCK-704`). Without it, `New` generates a cryptographically random seed. |
| `WithScenarioRoot(dir string)` | Bounds `extends` path resolution for `NewFromFile`. |
| `WithoutValidation()` | Skips JSON Schema validation on the programmatic construction paths (not the file path, which always validates). |
| `WithOverlay(docs ...*scenario.Document)` | Programmatic scenario overlays applied last, highest precedence. |
| `WithAddr(addr string)` | Sets the MCP HTTP listener address and enables HTTP. `":0"`/`"127.0.0.1:0"` for ephemeral. |
| `WithListener(l net.Listener)` | Supplies a pre-bound listener for the MCP HTTP transport instead of an address. |
| `WithStdio(in io.Reader, out io.Writer)` | Enables stdio transport with explicit streams — the library **never** defaults to `os.Stdin`/`os.Stdout`. |
| `WithObservabilityAddr(addr string)` | Serves `/metrics`, `/healthz`, `/readyz` on `addr`, a listener distinct from the MCP listener. |
| `WithControlAddr(addr string)` | Binds the HTTP control API to `addr`. Binding non-loopback without a token is a startup error. |
| `WithControlSocket(path string)` | Sets the control API's unix-socket path explicitly. |
| `WithoutControl()` | Starts no control front end (HTTP/socket); the in-process `Server.Control()` still works. |
| `WithControlToken(token string)` | Sets the control bearer token — see [`docs/control-api.md#security`](control-api.md#security) for what it does and does not enforce in Phase 1. |
| `WithLogger(*slog.Logger)` | Sets the facade's lifecycle logger. Default is a discard logger — the library is silent unless you opt in, and must never write to stdout in stdio mode. |
| `WithRegisterer(*prometheus.Registry)` | Supplies your own Prometheus registry instead of a fresh private one. |
| `WithJournal(journalapi.Config)` | Overrides the per-instance journal config, replacing the library's fleet-friendly default. |
| `WithoutJournal()` | Disables journalling for every instance. |

**Library journal default differs from the CLI.** The CLI/scenario-file
default is a 100,000-record ring; the library defaults each instance to a
4,096-record ring capped at 8 MiB, sized so embedding dozens of instances in
one test binary does not preallocate hundreds of MiB. Pass
`WithJournal(journalapi.Config{Enabled: true, MaxRecords: 100000})` to
restore the larger ring.

## Startup cost

Measured directly on this host (`go test -run
'TestStartupUnderBudget|TestInProcessStartUnderBudget' .`, 2026-09-05):

| Test | Runs | Mean | Worst | Budget |
|---|---|---|---|---|
| Full `New`+`Start` with an ephemeral HTTP listener | 20 | 437 µs | 783 µs | 200 ms |
| In-process start, no network listener (stdio over pipes) | 50 | best 289 µs | worst 890 µs | 20 ms (backstop 200 ms) |

Both comfortably inside budget on this host (an Apple M1 Max laptop); no
claim is made about the CI/production target hardware.

## The `assert` package

```go
import "github.com/vyrodovalexey/mcp-mock-server/assert"
```

Binds to an instance's journal (`journalapi.View`) and offers three
assertion families, each in a fatal (`TB`, stops the test) and a non-fatal
(`Asserts`, returns an `error`) form:

| Assertion | Fatal method | Non-fatal method | Checks |
|---|---|---|---|
| No forbidden header | `TB.AssertNoHeader(name)` | `Asserts.CheckNoHeader(name)` | No record in scope carries a header matching `name` (case-insensitive). |
| Header not a forbidden value | `TB.AssertHeaderNotValue(name, value)` | `Asserts.CheckHeaderNotValue(name, value)` | No record carries `name` with exactly `value` (constant-time compare — not a timing oracle for a token). |
| Request count | `TB.AssertRequestCount(sel, n)` (+ `...AtMost`/`...AtLeast`) | `Asserts.CheckRequestCount(sel, n)` (+ `...AtMost`/`...AtLeast`) | Exactly / at most / at least `n` records match a `journalapi.Selector`. |

```go
tb := assert.T(t, inst.Journal())
tb.WhereMethod("tools/*").
	AssertNoHeader("Authorization").
	AssertRequestCount(journalapi.Selector{Name: "echo"}, 1)
```

`Where`, `WhereMethod`, `WhereInstance` narrow scope and return a new,
immutable value — the receiver is never mutated, so a base `TB`/`Asserts` can
be reused for several independent scoped assertions.

## Reading a journal with no server running

`assert.FromFile(path)` and `assert.FromReader(r)` load an NDJSON journal
export (as produced by `?format=ndjson` on the control API, or
`journalapi.Writer`) into a `journalapi.View` with no server process at all —
useful for asserting against a journal captured in a previous run.

```go
view, err := assert.FromFile("journal.ndjson")
if err != nil { /* ... */ }
if err := assert.New(view).CheckRequestCount(journalapi.Selector{Method: "tools/call"}, 3); err != nil {
	t.Error(err)
}
```
