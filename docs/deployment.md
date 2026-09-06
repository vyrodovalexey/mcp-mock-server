# Deployment guide

Audience: **operator**.

## Table of contents

- [Container image](#container-image)
- [Running with Docker](#running-with-docker)
- [Helm chart](#helm-chart)
- [Resource sizing](#resource-sizing)
- [Measured performance](#measured-performance)
- [Upgrade / rollback](#upgrade--rollback)

## Container image

Built from [`Dockerfile`](../Dockerfile): a `CGO_ENABLED=0` static Go binary
on `gcr.io/distroless/static-debian12:nonroot`, base images pinned by digest.
Verified locally:

```console
$ docker build -t mcpmock:dev-local .
$ docker image inspect mcpmock:dev-local --format='{{.Size}}'
18498138
```

18.5 MB, running as uid/gid `65532` (non-root), no shell. `EXPOSE 8080 9090
9091` documents the three ports (MCP / metrics / control); nothing is opened
automatically beyond what you publish. `MCPMOCK_SAFE_MODE=1` is baked into
the image (the hostile-corpus gate — inert in Phase 1, since the corpus
itself does not exist yet).

Default entrypoint: `mcpmock serve --path /etc/mcpmock/scenario.yaml` — the
image expects a scenario mounted at that path (the Helm chart does this via
a ConfigMap).

## Running with Docker

Verified end to end:

```console
$ docker run -d --name mcpmock -p 8080:8080 -p 9090:9090 \
    mcpmock:dev-local serve --listen 0.0.0.0:8080 --metrics-listen 0.0.0.0:9090 --no-control
$ docker logs mcpmock
{"time":"...","level":"INFO","msg":"effective seed","seed":5286026103321589191}
{"time":"...","level":"INFO","msg":"started", ...}
{"time":"...","level":"INFO","msg":"serving","event":"startup","mcpUrl":"http://[::]:8080/mcp","metricsUrl":"http://[::]:9090"}

$ curl -s -X POST http://127.0.0.1:8080/mcp -H 'content-type: application/json' \
    -d '{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"_meta":{"protocolVersion":"2026-07-28","clientCapabilities":{}}}}'
{"jsonrpc":"2.0","id":1,"result":{"resultType":"discovery","_meta":{"serverInfo":{"name":"mcpmock","version":"0.1.0"}}}}

$ curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:9090/healthz
200
```

`--no-control` is used above because the container's default entrypoint has
no scenario mounted; to run with a mounted scenario, bind-mount a file to
`/etc/mcpmock/scenario.yaml` and drop the `CMD` override.

## Helm chart

Chart: [`helm/mcpmock`](../helm/mcpmock), `Chart.yaml` version `0.1.0`,
`appVersion "0.1.0"`. Verified locally with `helm v3.19.1`:

```console
$ helm lint helm/mcpmock
==> Linting helm/mcpmock
[INFO] Chart.yaml: icon is recommended
1 chart(s) linted, 0 chart(s) failed

$ helm template my-release helm/mcpmock | kubectl apply --dry-run=client -f - 2>&1 | head
# renders and dry-run-applies cleanly (NetworkPolicy, ServiceAccount, Deployment, three Services, ConfigMap)
```

**The default inline scenario in `values.yaml` uses the object form of
`transport`** (`transport.kinds: [http]` / `transport.http.path: /mcp`), not
a bare string. An earlier draft of this default used a string and failed
schema validation (tracked as `DEF-012`); the shape currently in the chart
validates cleanly:

```console
$ mcpmock validate <(helm template x helm/mcpmock -s templates/configmap-scenario.yaml | ... extract the scenario.yaml key ...)
# equivalent standalone check:
$ mcpmock validate scenario-with-object-transport.yaml
scenario-with-object-transport.yaml is valid (kind Scenario)
```

A bare `helm install` with no value overrides installs one replica, an
inline default scenario, metrics always on, the control API **disabled**,
and a default-deny `NetworkPolicy` with narrow allowances (DNS egress, MCP
and metrics ingress from the same namespace).

### Values you are likely to set

| Value | Default | Notes |
|---|---|---|
| `image.repository` / `image.tag` | `mcpmock` / `dev-local` | `dev-local` targets a locally built image already in the daemon (e.g. `docker-desktop`); override both for a registry image. |
| `replicaCount` | `1` | The journal is per-process and per-replica; scaling above 1 splits a test's evidence across pods. Only supported for rolling-restart testing. |
| `scenario.inline` / `scenario.configMapName` | inline default scenario | Exactly one of the two must be set. |
| `scenario.validateOnStart` | `true` | Runs `mcpmock validate` in an init container so a bad scenario fails the rollout with a readable message instead of crash-looping. |
| `seed` | `""` (random) | Set a fixed value for reproducible runs. |
| `control.enabled` | `false` | Enabling it **requires** `control.tokenSecretName` pointing at an existing Secret — see [Control API](#control-api) below for what that token does and does not enforce in this build. |
| `metrics.serviceMonitor.enabled` | `false` | Enable only if the Prometheus Operator CRDs are installed. |
| `profile` | `default` | One of `default`, `large-fleet`, `many-streams`, `throughput` — selects a resources block. |
| `networkPolicy.enabled` | `true` | Default-deny egress with a DNS allowance; disable only if your cluster policy conflicts. |

### Control API

`control.enabled` is `false` by default — the chart's posture is
loopback-only, matching the binary's own default. Setting it to `true`
creates a Service for port `9091` and **requires**
`control.tokenSecretName` (an existing Secret; the chart refuses to render
with the control API enabled and no token reference). See
[`docs/control-api.md#security`](control-api.md#security) for what that
token does — and does not — enforce in this build before relying on it.

### Probes

Verified against `values.yaml` and the running binary — **`/healthz` is
liveness, `/readyz` is readiness, both on the metrics port (`9090`), not
`/livez`**:

```yaml
livenessProbe:
  httpGet: { path: /healthz, port: metrics }
readinessProbe:
  httpGet: { path: /readyz, port: metrics }
```

## Resource sizing

The chart's `default` profile requests `100m`/`128Mi` and limits
`1`/`256Mi`. Larger profiles (`large-fleet`, `many-streams`, `throughput`)
scale both cpu and memory — see `helm/mcpmock/values.yaml` for the exact
numbers, which are sized from the measured figures below, not from a
theoretical estimate.

## Measured performance

**All figures below are measured on one Apple M1 Max laptop (`TASK-029`,
2026-09-04), not on the 4-vCPU Linux target the requirements specify
(`MOCK-901`).** Read every number as "measured on this host" — none of them
is a target-hardware verdict.

| Requirement | Target | Measured on this host | Verdict |
|---|---|---|---|
| `MOCK-901` (throughput, journaling off) | ≥20,000 `tools/call` rps, p99 < 25 ms | ~19,857 rps achieved; p99 27–50 ms in 2 of 3 runs; **server CPU never exceeded ~1.3 of 4 cores** while the k6 load generator itself consumed 2–3 cores | **INDETERMINATE on this host** — the load generator saturated before the server did; the p99 breach is generator-induced, not server-side. Neither a pass nor a fail of the server can be claimed here. |
| `MOCK-902` (throughput, full journaling) | ≥5,000 rps, p99 < 50 ms | ~4,917 rps (−1.6% generator-side drop), p99 3.0–5.5 ms, RSS stabilises at ~614 MiB, 0 errors, generator provably unsaturated | **PASS — measured on this host.** High confidence; not a claim about the 4-vCPU target. |
| `MOCK-903` (concurrent SSE streams, RSS) | ≥20,000 streams/process, RSS ≤ 1.5 GiB | Server-only per-stream RSS ≈ 27.5 KiB (range 23–34 KiB); maximum **15,000** live streams reached before **loopback ephemeral-port exhaustion** stopped the run — not memory, not file descriptors, not the server. Projected RSS at 20,000 streams: ~545–617 MiB. | **Memory envelope: PASS (extrapolated)**, ~2.5× headroom under 1.5 GiB. **Stream count: INDETERMINATE on this host** — 20,000 concurrent loopback connections cannot be dialed from a single-host generator; this needs a two-host or multi-loopback-alias setup. |

None of these numbers should be read as a certification against the
production target; re-running `MOCK-901` and the `MOCK-903` stream count on
the actual 4-vCPU Linux target with a separate load-generator host is the
explicit next step recorded in the perf report
(`.opencode/output/task-029-perf-report_manager-development_2026-09-04_091847.md`,
not part of this documentation set).

## Upgrade / rollback

Not yet exercised or documented for Phase 1 — no versioned release has
shipped, so there is no upgrade path to describe yet beyond Helm's own
`helm upgrade`/`helm rollback` mechanics, which apply generically to any
chart and are not mcpmock-specific in this build. Revisit once a first
tagged release exists.
