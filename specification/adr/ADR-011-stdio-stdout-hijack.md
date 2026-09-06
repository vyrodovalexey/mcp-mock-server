---
id: ADR-011
title: In stdio mode stdout is protocol-only; os.Stdout is hijacked and logs go to stderr
status: accepted
date: 2026-09-04
reversibility: EASY — a startup behaviour in cmd/, removable in one function
requirements: MOCK-102, MOCK-105, MOCK-107
---

# ADR-011 — stdout/stderr conflict in stdio mode

## Context

`MOCK-102` requires a `stdio` transport, in which **stdout is the MCP frame channel**. `MOCK-105`
requires structured JSON logs — and specifies **stderr**, which resolves the requirement-level
conflict. But requirements do not stop libraries.

The real hazard is that a single stray write to `os.Stdout` anywhere in the process corrupts the
frame stream and the hub sees a JSON parse error. Sources of such writes are not hypothetical:

- a `fmt.Println` left in during debugging;
- `log` package defaults (Go's `log` writes to **stderr** by default, so that one is safe, but
  `log.New(os.Stdout, …)` is a common idiom);
- third-party libraries that print warnings — `prometheus/client_golang`, `otel` and JSON Schema
  libraries all have historically printed to stdout under some conditions;
- Go runtime output on `panic` (goes to stderr — safe);
- `testing` output when embedded (`MOCK-107`).

Detecting this in review is unreliable. It must be structurally impossible.

## Decision

Three layers.

### 1. The transport does not use `os.Stdout` — it uses a captured handle

`cmd/mcpmock`, in stdio mode, **before** any other initialisation:

```go
protoOut := os.Stdout                    // the real fd 1, handed to the transport
r, w, err := os.Pipe()
os.Stdout = w                            // everything else in the process writes here
go drainToStderr(r, logger)              // tags each line: {"level":"warn","msg":"stdout leak","text":...}
```

The stdio transport writes frames to `protoOut` only. Anything else that writes to `os.Stdout`
lands in the pipe, is drained by one goroutine, and is re-emitted **on stderr** as a structured
log record with `event: "stdout_leak"`. Leaks become loud, attributable diagnostics instead of
silent protocol corruption.

A counter `mcpmock_stdout_leak_bytes_total` is incremented. A leak in CI fails
`test/functional/TestNoStdoutLeak`, which asserts the counter is zero after a full scenario run.

Note `os.Stdout` is a package-level `*os.File` variable in the standard library; reassigning it is
legal and is exactly how this is done in practice. It does **not** affect code that captured
`os.Stdout` earlier — hence "before any other initialisation".

### 2. The library never touches `os.Stdout`

`MOCK-107` forbids a library from mutating process globals (ADR-007). The library form of the
stdio transport therefore takes explicit streams:

```go
mcpmock.WithStdio(in io.Reader, out io.Writer)     // no defaulting to os.Stdin/os.Stdout
```

`cmd/mcpmock` passes `os.Stdin` and the captured `protoOut`. An embedded test passes an
`io.Pipe`. The hijack lives in `cmd/`, and only there. This split is why the hijack is safe to do
at all.

### 3. Logging is stderr-only, structurally

`obs.NewLogger` accepts a writer and `cmd/mcpmock` always passes `os.Stderr`. There is no code
path that constructs a logger over stdout. `slog.SetDefault` is never called (ADR-007), so a
library that reaches for `slog.Default()` gets the stdlib default — which writes to **stderr**.
Same for `log.Default()`.

In HTTP-only mode the hijack is **not** installed (stdout is free), but logs still go to stderr,
so behaviour is uniform and log-capture tooling does not have to care which transport is running.

## Options considered

1. **Documentation and code review only** — rejected: one missed `fmt.Println` produces an
   intermittent hub test failure that will be blamed on the hub. The cost of being wrong is
   asymmetric.
2. **Set `os.Stdout` to `io.Discard`-backed file** — rejected: silently swallows the diagnostic.
   Turning a bug into silence is worse than turning it into noise.
3. **Route the protocol to a different fd (fd 3) and leave stdout alone** — rejected: breaks the
   MCP stdio convention that the hub, as a client spawning a child process, relies on. Not
   negotiable.
4. **Hijack `os.Stdout` into a stderr-tagging drain (chosen).**

## Consequences

**Positive.** Frame-stream corruption from in-process writes becomes structurally impossible;
leaks become attributable log lines and a CI assertion. Uniform stderr logging across transports.

**Negative.** One extra goroutine and one pipe in stdio mode (counted in the `architecture.md §7.1`
budget). A library that writes megabytes to stdout will now block on a full pipe if the drainer
stalls — the drainer is a tight `bufio.Scanner` loop with a bounded line length (64 KiB, longer
lines truncated and counted), so this is a non-issue in practice but is worth knowing.

Debugging with `dlv` or a `println` during development now goes to stderr, which occasionally
confuses people. Documented in `CONTRIBUTING`.

**Forecloses.** Any future "print the journal to stdout" CLI behaviour *while the stdio transport
is running*. `mcpmock ctl journal` writes to stdout only in non-serving invocations, which is a
separate process anyway.
