# mcpmock

A deterministic mock server for the Model Context Protocol (MCP), built to sit
in front of a hub under test: same seed in, byte-identical responses out,
every request recorded in an inspectable journal.

> **Status: Phase 1 of a 12-phase delivery plan.** This is a walking skeleton —
> the transport, the core JSON-RPC pipeline, three built-in tools, the control
> plane, the journal and the Go embedding API are real and tested. Fault
> injection, subscriptions, multi-round tool results (MRTR), legacy-era
> emulation and authorization **do not exist yet**. See
> [`docs/not-yet-implemented.md`](docs/not-yet-implemented.md) for the full
> list mapped to requirement IDs and phases. Do not point a hub integration at
> this build expecting anything beyond what is listed under "Implemented"
> below.

## ⚠ The wire format is not a public standard

mcpmock's modern-era protocol revision (`2026-07-28`) — its methods
(`server/discover`), its error codes (`-32020`–`-32022`), the `resultType`
discriminator and MRTR — comes from this project's own requirements document,
not from any Model Context Protocol specification text available to the
people who built this server. **No claim is made, one way or the other, about
whether this wire format matches a real MCP revision.**

The formal definition lives in
[`specification/contracts/wire-2026-07-28.md`](specification/contracts/wire-2026-07-28.md)
and its companion
[`wire-2026-07-28.schema.json`](specification/contracts/wire-2026-07-28.schema.json),
both marked `status: PROPOSED — REQUIRES RATIFICATION`, versioned `v0`/`v1alpha1`,
and carrying `x-conformance-claim: NONE`. Roughly 52 of the details in that
annex are the authors' own proposals to fill gaps in the requirements, not
restatements of anything external. This is tracked as an open, unresolved gap
(`GAP-003` in [`specification/gap-analysis.md`](specification/gap-analysis.md))
that blocks later phases.

**If you are building a hub against this server, do not assume its wire shapes
are MCP-standard.** Verify independently against whatever specification your
hub actually targets.

## What mcpmock is

- A single static Go binary (`mcpmock`) or an embeddable Go library
  (`import mcpmock "github.com/vyrodovalexey/mcp-mock-server"`).
- Speaks MCP-shaped JSON-RPC over **streamable HTTP** (`POST` only) and
  **stdio**.
- Configured by one declarative YAML/JSON **scenario** document, validated
  against a JSON Schema before it serves a single request.
- Deterministic: pass `--seed`, get byte-identical responses on every run.
- Everything a client sent and got back is retained in a bounded, queryable
  **journal**, inspectable live over a **control API**.

## What is implemented (Phase 1)

| Area | What you get |
|---|---|
| Transports | `streamable-http` (`POST /mcp`, default port 8080) and `stdio`. `GET`/`DELETE` on the MCP path return `405` by design. |
| Methods | `server/discover`, `tools/list`, `tools/call`, with three built-in tools: `echo`, `sleep`, `fail`. |
| `_meta` validation | `strict` (reject missing `protocolVersion`/`clientCapabilities` with `-32602`/HTTP 400), `lenient` (accept + record), `off`. |
| Switches | `omitResultType`, `omitServerInfoMeta`, `hideFromCapabilities`, `selfCheck` (reject the mock's own malformed output before it leaves the process). |
| Scenario config | Single YAML/JSON document, JSON-Schema validated, `extends` composition, `mcpmock validate --output json\|text`. |
| Journal | Bounded ring per instance, overflow policy, filter by method/name/time/correlation id, JSON and NDJSON streaming. Credentials are stored **hashed**, never in clear. |
| Control API | HTTP + Unix socket, loopback by default, `mcpmock ctl` CLI front end. See the caveat in [`docs/control-api.md`](docs/control-api.md#security). |
| Go library | `mcpmock.StartTest(tb, opts...)` for in-process hub tests; the `assert` package adds three journal assertions (fatal `TB` form + non-fatal `Asserts` form). |
| Observability | Prometheus `/metrics`, `/healthz`, `/readyz` on `:9090`; structured JSON logs on stderr; OTLP tracing. |
| Determinism | `--seed` (uint64); same seed → byte-identical responses; effective seed printed at startup and readable from `mcpmock ctl seed`. |
| Packaging | Distroless container image, Helm chart, plain manifests. |

Everything else in the requirements document — pagination, the full virtual
catalogue, MRTR, subscriptions, legacy-era emulation, authorization, fault and
chaos injection, TLS — is **not built yet**. See
[`docs/not-yet-implemented.md`](docs/not-yet-implemented.md).

## Quickstart

Requires Go 1.27 or later. Verified from a clean clone on 2026-09-05.

```console
$ git clone https://github.com/vyrodovalexey/mcp-mock-server.git
$ cd mcp-mock-server
$ go build -o bin/mcpmock ./cmd/mcpmock
$ ./bin/mcpmock serve --listen 127.0.0.1:8080 --metrics-listen 127.0.0.1:9090
{"time":"...","level":"INFO","msg":"effective seed","seed":<random uint64>}
{"time":"...","level":"INFO","msg":"started","seed":<...>,"instances":1,"controlUrl":"http://127.0.0.1:9091","controlSocket":""}
{"time":"...","level":"INFO","msg":"serving","event":"startup","mcpUrl":"http://127.0.0.1:8080/mcp","metricsUrl":"http://127.0.0.1:9090","controlUrl":"http://127.0.0.1:9091"}
```

In a second terminal, call it:

```console
$ curl -s -X POST http://127.0.0.1:8080/mcp \
    -H 'content-type: application/json' \
    -d '{"jsonrpc":"2.0","id":1,"method":"tools/call",
         "params":{"name":"echo","arguments":{"message":"hello"},
                   "_meta":{"protocolVersion":"2026-07-28","clientCapabilities":{}}}}'
{"jsonrpc":"2.0","id":1,"result":{"resultType":"toolResult","content":[{"type":"text","text":"{\"message\":\"hello\"}"}],"structuredContent":{"echoed":{"message":"hello"}},"_meta":{"serverInfo":{"name":"mcpmock","version":"0.1.0"}}}}
```

The `_meta` object with `protocolVersion` and `clientCapabilities` is
required by default (`switches.validateMeta: strict`); omit it and you get a
`-32602` naming the missing fields instead. See
[`docs/quickstart.md`](docs/quickstart.md) for the full walkthrough, including
`stdio` mode, the Go library form and Docker/Helm.

## Documentation

| Document | Audience | Covers |
|---|---|---|
| [`docs/quickstart.md`](docs/quickstart.md) | User | Install, first request, `stdio`, troubleshooting |
| [`docs/scenario-reference.md`](docs/scenario-reference.md) | User / Operator | Every scenario key implemented in Phase 1, schema-derived |
| [`docs/cli-reference.md`](docs/cli-reference.md) | User / Operator | Every flag of `serve`, `validate`, `ctl`, `version`, exit codes |
| [`docs/library.md`](docs/library.md) | Contributor / Integrator | Embedding mcpmock in a Go test binary, `StartTest`, the `assert` package |
| [`docs/control-api.md`](docs/control-api.md) | Operator / Integrator | The 8 routes actually implemented, security posture |
| [`docs/observability.md`](docs/observability.md) | Operator | Metrics, health/readiness, logs, tracing |
| [`docs/deployment.md`](docs/deployment.md) | Operator | Docker image, Helm chart, resource sizing, measured performance |
| [`docs/not-yet-implemented.md`](docs/not-yet-implemented.md) | Everyone | `MOCK-nnn` → phase mapping for everything not yet built |
| [`CHANGELOG.md`](CHANGELOG.md) | Everyone | What shipped, in Keep a Changelog form |

For *why* things were decided — architecture, ADRs, the requirements text
itself — see [`specification/`](specification/). That directory is the
architect's; this documentation set describes how to use what was actually
built, not what was planned.

## Building from source

```console
$ go build ./...          # 0 issues
$ go vet ./...             # 0 issues
$ go test ./... -race      # green
$ golangci-lint run ./...  # 0 issues (v2.13.2)
```

See `make help` for the full target list (`test-unit`, `test-functional`,
`test-e2e`, `test-perf`, `docker-build`, `helm-lint`, `helm-template`, and
more).

## License

No `LICENSE` file is present in this repository at the time of writing. The
container image and Helm chart metadata reference Apache-2.0, but that is not
confirmed by a license file in the source tree. Treat licensing as
unresolved until a `LICENSE` file is added — do not rely on this README for a
license grant.
