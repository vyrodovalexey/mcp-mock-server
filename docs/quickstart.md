# Quickstart

Audience: **user** — you want a running mock MCP server to point a client or
hub integration test at.

Verified from a clean clone on 2026-09-05 with Go 1.27.1 on macOS/arm64.

## Table of contents

- [Prerequisites](#prerequisites)
- [Build](#build)
- [Run over HTTP](#run-over-http)
- [Run over stdio](#run-over-stdio)
- [The `_meta` requirement](#the-_meta-requirement)
- [Inspect what happened](#inspect-what-happened)
- [Determinism](#determinism)
- [Troubleshooting](#troubleshooting)
- [Next steps](#next-steps)

## Prerequisites

- Go 1.27 or later (`go version`). No other tool is required to build and run
  the binary.
- `curl` for the examples below (any HTTP client works).
- Docker, only if you want the container path — see
  [`docs/deployment.md`](deployment.md).

## Build

```console
$ git clone https://github.com/vyrodovalexey/mcp-mock-server.git
$ cd mcp-mock-server
$ go build -o bin/mcpmock ./cmd/mcpmock
```

This produces a single binary, `bin/mcpmock`, with no runtime dependencies.

## Run over HTTP

```console
$ ./bin/mcpmock serve --listen 127.0.0.1:8080 --metrics-listen 127.0.0.1:9090
```

`serve` with no `--path` loads the built-in default scenario (equivalent to
[`scenarios/happy-path.yaml`](../scenarios/happy-path.yaml)): one HTTP
instance at `/mcp`, `switches.validateMeta: strict`, and the three built-in
tools (`echo`, `sleep`, `fail`) available.

Startup prints three structured JSON log lines to **stderr**:

```json
{"time":"...","level":"INFO","msg":"effective seed","seed":11983285343475008406}
{"time":"...","level":"INFO","msg":"started","seed":11983285343475008406,"instances":1,"controlUrl":"http://127.0.0.1:9091","controlSocket":""}
{"time":"...","level":"INFO","msg":"serving","event":"startup","mcpUrl":"http://127.0.0.1:8080/mcp","metricsUrl":"http://127.0.0.1:9090","controlUrl":"http://127.0.0.1:9091"}
```

The seed is random on every run unless you pass `--seed` (see
[Determinism](#determinism)). The control API bound to `127.0.0.1:9091` is
covered in [`docs/control-api.md`](control-api.md).

In a second terminal, list the built-in tools:

```console
$ curl -s -X POST http://127.0.0.1:8080/mcp \
    -H 'content-type: application/json' \
    -d '{"jsonrpc":"2.0","id":1,"method":"tools/list",
         "params":{"_meta":{"protocolVersion":"2026-07-28","clientCapabilities":{}}}}'
```

```json
{"jsonrpc":"2.0","id":1,"result":{"resultType":"toolList","tools":[
  {"name":"echo", "...": "..."},
  {"name":"sleep", "...": "..."},
  {"name":"fail", "...": "..."}
],"_meta":{"serverInfo":{"name":"mcpmock","version":"0.1.0"}}}}
```

Call `echo`:

```console
$ curl -s -X POST http://127.0.0.1:8080/mcp \
    -H 'content-type: application/json' \
    -d '{"jsonrpc":"2.0","id":2,"method":"tools/call",
         "params":{"name":"echo","arguments":{"message":"hello"},
                   "_meta":{"protocolVersion":"2026-07-28","clientCapabilities":{}}}}'
```

```json
{"jsonrpc":"2.0","id":2,"result":{"resultType":"toolResult","content":[{"type":"text","text":"{\"message\":\"hello\"}"}],"structuredContent":{"echoed":{"message":"hello"}},"_meta":{"serverInfo":{"name":"mcpmock","version":"0.1.0"}}}}
```

`GET` and `DELETE` on `/mcp` return `405` by design (no session negotiation
in Phase 1):

```console
$ curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8080/mcp
405
```

## Run over stdio

```console
$ ./bin/mcpmock serve --transport stdio --no-control
```

In stdio mode, **stdout carries protocol frames only** — one JSON-RPC message
per line — and every log line (the effective seed, startup diagnostics) goes
to stderr. A client writes a request line to the process's stdin and reads
the response line from stdout:

```console
$ echo '{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_meta":{"protocolVersion":"2026-07-28","clientCapabilities":{}}}}' \
    | ./bin/mcpmock serve --transport stdio --no-control
```

```json
{"jsonrpc":"2.0","id":1,"result":{"resultType":"toolList", "...": "..."}}
```

(The example above works with a single request piped through stdin; a real
client keeps the process running and writes one line per request.)

## The `_meta` requirement

By default (`switches.validateMeta: strict`) every request must carry
`params._meta.protocolVersion` and `params._meta.clientCapabilities`. Omit
either and the server rejects the call before it reaches a handler:

```console
$ curl -s -X POST http://127.0.0.1:8080/mcp \
    -H 'content-type: application/json' \
    -d '{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}'
```

```json
{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"invalid params: missing required _meta field(s)","data":{"missing":["protocolVersion","clientCapabilities"]}}}
```

HTTP status is `400` for this rejection. Two other modes exist —
`lenient` (accept, record the omission) and `off` (skip the check) — set via
`spec.switches.validateMeta` in a scenario file; see
[`docs/scenario-reference.md`](scenario-reference.md#specswitches).

## Inspect what happened

Every request is retained in the instance's journal. With the control API
enabled (the default), query it:

```console
$ curl -s http://127.0.0.1:9091/v1/instances/default/journal?limit=5
```

Or use the CLI front end, which talks the same route table:

```console
$ ./bin/mcpmock ctl journal default --url http://127.0.0.1:9091
```

See [`docs/control-api.md`](control-api.md) for the full route list.

## Determinism

Pass `--seed` for byte-identical output across runs:

```console
$ ./bin/mcpmock serve --seed 42 --listen 127.0.0.1:8080 &
$ curl -s -X POST http://127.0.0.1:8080/mcp -H 'content-type: application/json' \
    -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"echo","arguments":{"a":1},"_meta":{"protocolVersion":"2026-07-28","clientCapabilities":{}}}}'
```

Kill the server, start it again with the same `--seed 42`, and issue the same
request: the response body is byte-for-byte identical (verified directly for
this documentation set — two independent server processes, same seed, same
request, identical JSON). The effective seed is also readable at any time via
`mcpmock ctl seed` or `GET /v1/seed`.

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `-32602 ... missing required _meta field(s)` | `params._meta.protocolVersion` or `.clientCapabilities` absent | Add both, or set `switches.validateMeta: lenient`/`off` in your scenario |
| `405` on `GET`/`DELETE /mcp` | By design — MOCK-207, no session negotiation in Phase 1 | Use `POST` |
| `mcpmock: control bind: control: refusing to bind control API to non-loopback address ... without a token` | Control listener bound to a non-loopback address with no token configured | Bind to loopback, or set `--control-token-file` (see [`docs/control-api.md`](control-api.md#security) for what the token does and does not do in Phase 1) |
| `serve` exits 2 immediately | Usage/startup error — bad flag, bad scenario path, listener bind failure | Check stderr; exit code 2 is always usage/I-O, never a protocol error |
| `validate` exits 1 | The scenario file failed schema or semantic validation | Read the printed problem list; each line names the JSON Pointer and the failing keyword |

## Next steps

- [`docs/scenario-reference.md`](scenario-reference.md) — configure instances,
  switches, and the built-in tools beyond the defaults.
- [`docs/cli-reference.md`](cli-reference.md) — every flag and exit code.
- [`docs/library.md`](library.md) — embed mcpmock directly in a Go test binary
  instead of running the binary.
- [`docs/deployment.md`](deployment.md) — Docker and Helm.
