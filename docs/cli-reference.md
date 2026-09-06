# CLI reference

Audience: **user** / **operator**.

Every flag and exit code below was captured by running the built binary
(`mcpmock <cmd> --help` and representative invocations); none is transcribed
from the specification. Rebuild with `go build -o bin/mcpmock ./cmd/mcpmock`
to reproduce.

## Table of contents

- [Top-level usage and exit codes](#top-level-usage-and-exit-codes)
- [`serve`](#serve)
- [`validate`](#validate)
- [`ctl`](#ctl)
- [`version`](#version)

## Top-level usage and exit codes

```console
$ mcpmock
mcpmock — deterministic mock MCP server (Phase 1)

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
```

Running `mcpmock` with no arguments, or an unknown command, prints this usage
to stderr and exits `2`.

## `serve`

```console
$ mcpmock serve --help
Usage: mcpmock serve [flags]

Run the mock MCP server. In stdio mode stdout carries protocol frames only;
the effective seed and all diagnostics go to stderr.

Exit codes: 0 clean shutdown, 1 scenario validation error, 2 startup/usage error.

Flags:
  -control-listen string
    	control API TCP listen address (loopback by default)
  -control-socket string
    	control API unix socket path (overrides the default)
  -control-token-file string
    	file holding the control bearer token (never a flag value)
  -listen string
    	MCP HTTP listen address (default "127.0.0.1:0")
  -log-level string
    	log level: debug,info,warn,error (default "info")
  -metrics-listen string
    	observability listen address for /metrics,/healthz,/readyz (flag > MCPMOCK_METRICS_LISTEN > default :9090)
  -no-control
    	disable the control API front ends
  -no-validate
    	skip scenario schema validation
  -path string
    	scenario file to load (empty = built-in default scenario)
  -scenario-root string
    	bound extends resolution to this directory
  -seed string
    	root seed (uint64); absent = cryptographically random
  -transport string
    	comma-separated transports: stdio,http (default "http")
```

Notes, each verified by direct invocation:

- **`-listen`** defaults to `127.0.0.1:0` (an ephemeral port) if you never set
  it — the effective bound address is printed in the `serving` startup log
  line.
- **`-transport`** accepts `http`, `stdio`, or `stdio,http`. An unrecognized
  value fails fast: `mcpmock serve: unknown transport "bogus" (want stdio
  and/or http)`, exit `2`.
- **`-log-level`** rejects anything outside `debug,info,warn,error`:
  `mcpmock serve: --log-level must be one of debug,info,warn,error; got
  "bogus"`, exit `2`.
- **`-seed`** takes a `uint64` as a *string* (so it round-trips exactly at the
  top of the range); omit it for a cryptographically random seed, printed at
  startup as `{"msg":"effective seed","seed":<uint64>}` on stderr.
- **`-metrics-listen`** precedence is flag > `MCPMOCK_METRICS_LISTEN` env var >
  default `:9090`.
- **`-control-token-file`** is the only way to supply the control bearer
  token to the CLI — there is deliberately no `-control-token` flag value, so
  the token never appears in a process listing. The same token can be
  supplied via the `MCPMOCK_CONTROL_TOKEN` environment variable. **Read
  [`docs/control-api.md#security`](control-api.md#security) before relying on
  this for anything beyond permitting a non-loopback bind** — Phase 1 does
  not enforce the token on individual requests.
- **`-scenario-root`** bounds where `extends` targets in `-path`'s scenario
  may resolve; an `extends` entry that would escape this directory (via `..`
  or a symlink) is rejected before any file is read.
- Binding the control listener to a non-loopback address without a token
  configured is refused at startup:

  ```console
  $ mcpmock serve -control-listen 0.0.0.0:9091
  mcpmock serve: mcpmock: control bind: control: refusing to bind control API
  to non-loopback address "0.0.0.0:9091" without a token (set
  MCPMOCK_CONTROL_TOKEN or --control-token-file)
  ```

  Exit code `2`.

## `validate`

```console
$ mcpmock validate --help
Usage: mcpmock validate [flags] <scenario-file>

Validate a scenario file (schema + semantics) without serving it.

Exit codes: 0 valid, 1 validation error, 2 I/O or usage error.

Flags:
  -output string
    	report format: text or json (default "text")
  -scenario-root string
    	bound extends resolution to this directory
```

Behaviour verified directly:

| Case | Output | Exit |
|---|---|---|
| Valid scenario | `mcpmock validate: <path> is valid (kind Scenario)` | `0` |
| Schema violation | `mcpmock validate: <path>: scenario validation failed: N problem(s)`, one `- /json/pointer: reason (keyword)` line per problem | `1` |
| File does not exist | `mcpmock validate: config: read scenario "<path>": open <path>: no such file or directory` | `2` |
| `-output json`, valid | `{"ok":true,"source":"<path>","kind":"Scenario"}` | `0` |

Example of a real validation failure:

```console
$ mcpmock validate /tmp/bad.yaml
mcpmock validate: /tmp/bad.yaml: scenario validation failed: 1 problem
  - /spec/bogusField: unknown key "bogusField" is not permitted here (additionalProperties: false)
$ echo $?
1
```

## `ctl`

```console
$ mcpmock ctl --help
Usage: mcpmock ctl <verb> [name] [--url URL | --socket PATH] [flags]

Call the control API of a running mcpmock server. The endpoint is resolved
flag > env (MCPMOCK_CONTROL_URL / MCPMOCK_CONTROL_SOCKET) > (none).

Verbs (generated from the control route table):
  instances      list every logical instance  [listInstances]
  instance <name> show one instance by name  [getInstance]
  seed           show the effective root seed and its source  [getSeed]
  health         show process health  [getHealth]
  journal <name> read one instance's request journal  [getJournal]
  correlations <name> read one instance's request correlation chains  [getCorrelations]
  clear-journal <name> clear one instance's request journal  [clearJournal]

Exit codes: 0 success, 2 usage error or control-plane failure.
```

The verb list is generated from the same route table the HTTP control API
serves (`internal/control/routes.go`), so it cannot drift from
[`docs/control-api.md`](control-api.md) — there are exactly these 7 verbs
(plus the process's `/v1/openapi.yaml`, which `ctl` has no verb for) in
Phase 1.

Endpoint resolution, in order: `--url`/`--socket` flag > `MCPMOCK_CONTROL_URL`
/ `MCPMOCK_CONTROL_SOCKET` environment variable > none (an invocation with no
endpoint configured fails with a usage error).

Per-verb flags (shown for `ctl instance <name>`, identical shape for
`journal`/`correlations`):

```console
Usage: mcpmock ctl instance <name> [flags]

Flags:
  -limit int
    	journal filter: max records
  -method string
    	journal filter: JSON-RPC method glob
  -socket string
    	control API unix socket path
  -url string
    	control API base URL (e.g. http://127.0.0.1:8081)
```

`-limit` and `-method` are accepted by every verb's flag set but are only
meaningful for `journal` and `correlations`.

Example:

```console
$ MCPMOCK_CONTROL_URL=http://127.0.0.1:9091 mcpmock ctl instances
[
  {
    "name": "default",
    "mountPath": "/mcp",
    "url": "http://127.0.0.1:8080/mcp",
    "era": "modern",
    "generation": 0,
    "journal": { "records": 6, "dropped": 0, "bytes": 6472 },
    "hostile": false
  }
]
```

A verb missing its required instance name fails with a usage error and its
own usage block, exit `2`:

```console
$ mcpmock ctl instance --url http://127.0.0.1:9091
mcpmock ctl instance: an instance name is required
```

## `version`

```console
$ mcpmock version
mcpmock dev (commit none, built unknown)
```

`version`, `commit` and `date` are stamped at build time via `-ldflags`
(see the Dockerfile and `make docker-build`); an unstamped `go build` prints
the placeholders shown above. Exit code is `0`.
