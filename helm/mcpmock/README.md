# mcpmock Helm chart

Deploys **mcpmock**, a deterministic mock MCP server, as a single-replica
Deployment with three Services (MCP / control / metrics), a scenario ConfigMap,
a default-deny NetworkPolicy and an optional ServiceMonitor.

`specification/deployment.md` is authoritative for the values contract.

## TL;DR (local, no registry)

The image `mcpmock:dev-local` is built into the Docker Desktop daemon (`make
docker-build`), which the `docker-desktop` Kubernetes cluster shares. With
`imagePullPolicy: IfNotPresent` and that tag, a bare install needs no registry:

```console
$ helm install mcpmock helm/mcpmock --namespace mcpmock-test --create-namespace --wait
$ kubectl -n mcpmock-test port-forward svc/mcpmock-metrics 9090:9090
$ curl -sf http://127.0.0.1:9090/readyz && echo ok
```

> The only authorised deploy target is context `docker-desktop`, namespace
> `mcpmock-test`. The actual cluster deploy is owned by TASK-034.

## Ports

| Port | Name | Serves | Exposure |
|---|---|---|---|
| 8080 | `mcp` | MCP endpoint at `/mcp` | to the hub |
| 9090 | `metrics` | `/metrics`, `/healthz`, `/readyz` | to Prometheus / the kubelet |
| 9091 | `control` | control API (token-protected) | disabled by default |

## Key values

See `values.yaml` for the full, documented contract. The most important:

| Value | Default | Notes |
|---|---|---|
| `image.repository` / `image.tag` | `mcpmock` / `dev-local` | local, offline. Override for a registry. |
| `image.pullPolicy` | `IfNotPresent` | resolves the local tag without a pull |
| `replicaCount` | `1` | **>1 splits the per-process journal** — see NOTES |
| `scenario.inline` | minimal doc | rendered into a ConfigMap; set exactly one of `inline`/`configMapName` |
| `scenario.validateOnStart` | `true` | init container runs `mcpmock validate` |
| `control.enabled` | `false` | loopback-only; enabling requires `control.tokenSecretName` |
| `metrics.enabled` | `true` | `/metrics`,`/healthz`,`/readyz` always on |
| `networkPolicy.enabled` | `true` | default-deny egress + narrow DNS allowance |
| `profile` | `default` | `default` \| `large-fleet` \| `many-streams` \| `throughput` |
| `resources` | 100m/128Mi → 1/256Mi | the `default` profile envelope |

## Control API

The control API mutates the mock and reads its journal, so it is **disabled by
default**. The binary refuses a non-loopback bind without a token (TASK-023), and
a token is never put in `values.yaml`. To expose it in-cluster:

1. Create a Secret out of band holding the token under key `token`.
2. Set `control.enabled=true` and `control.tokenSecretName=<that secret>`.

The token is mounted as a file and passed via `--control-token-file`, so it never
appears in the pod args or a process listing.

## Template guards

The chart fails `helm template`/`helm install` with a named reason when:

1. `hostile.enabled` without `networkPolicy.enabled` (ADR-018).
2. `control.enabled` without `control.tokenSecretName` (off-loopback token).
3. `mcp.tls.enabled` (Phase 11; terminate TLS at an ingress instead).
4. both or neither of `scenario.inline` / `scenario.configMapName`.

## Plain manifests

`deploy/manifests/` carries the hand-checked equivalent for the `default`
profile, for readers who do not want to run Helm.
