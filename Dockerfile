# mcpmock container image — TASK-030 (MOCK-101, deployment.md §2)
#
# Contract (deployment.md §2): a fully static, CGO-free Go binary on a distroless
# nonroot base, running as uid 65532 with a read-only-friendly layout, exposing
# the MCP (8080), control (9091) and metrics (9090) ports. Signed + SBOM'd in CI
# (deployment.md §7.6); this Dockerfile owns only the reproducible, minimal image.
#
# Deviations from the deployment.md §2 reference block, deliberately:
#   * Base images are pinned BY DIGEST, not by mutable tag — `:nonroot` and
#     `:1.27.1-alpine` are tags and are not pins. The tag is kept in a comment
#     next to each digest so a human can see what the digest resolves to.
#   * TARGETOS / TARGETARCH are declared as ARGs before use (the reference block
#     interpolates them without declaring them, which yields empty strings and a
#     host-arch build). Cross-compilation is driven from $BUILDPLATFORM so a pure
#     -Go static binary is produced without QEMU emulation.
#   * The runtime CMD uses `--path`, the real serve flag. The reference block's
#     `--config` does not exist in the Phase-1 CLI (TASK-024) and would exit 2.

# =============================================================================
# Build stage — cross-compiles the static binary for the requested platform.
# Pinned by digest; runs natively on the builder's arch ($BUILDPLATFORM) and
# emits for $TARGETOS/$TARGETARCH, so multi-arch needs no emulation.
# tag: golang:1.27.1-alpine
# =============================================================================
FROM --platform=$BUILDPLATFORM golang@sha256:cf6fca6641884b8433441b2b0652976f975e1d0fdd26d177eaaf8596087f3125 AS build

WORKDIR /src

# Dependency layer first: only re-runs when go.mod / go.sum change.
COPY go.mod go.sum ./
RUN go mod download

# Source layer.
COPY . .

# Cross-compilation targets, injected by buildx per requested platform.
ARG TARGETOS
ARG TARGETARCH

# Version stamp, injected by `make docker-build` (git describe / rev-parse / UTC).
ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_TIME=unknown

# CGO_ENABLED=0 + -trimpath -> a single statically linked binary with no runtime
# deps (MOCK-101.2), stamped via -ldflags -X main.{version,commit,date}
# (matches cmd/mcpmock/version.go). -s -w drops the symbol table and DWARF to
# keep the layer small. GOFLAGS=-mod=readonly fails the build on any go.sum drift.
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build \
      -mod=readonly \
      -trimpath \
      -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${BUILD_TIME}" \
      -o /out/mcpmock ./cmd/mcpmock

# =============================================================================
# Runtime stage — distroless static nonroot. No shell, no package manager, no
# libc beyond the static base (MOCK-101.3). Runs as uid 65532 (MOCK-101.4).
# tag: gcr.io/distroless/static-debian12:nonroot
# =============================================================================
FROM gcr.io/distroless/static-debian12@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab

# OCI provenance labels (source / revision / created), populated from build args.
ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_TIME=unknown
LABEL org.opencontainers.image.title="mcpmock" \
      org.opencontainers.image.description="Deterministic mock MCP server" \
      org.opencontainers.image.source="https://github.com/vyrodovalexey/mcp-mock-server" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.created="${BUILD_TIME}" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.base.name="gcr.io/distroless/static-debian12:nonroot"

COPY --from=build /out/mcpmock /usr/local/bin/mcpmock

# ADR-018 third gate: the hostile corpus is disabled by default in the image.
ENV MCPMOCK_SAFE_MODE=1

# 8080 mcp · 9090 metrics (/metrics,/healthz,/readyz) · 9091 control.
# EXPOSE is documentation only; the chart (TASK-031) maps the Services.
EXPOSE 8080 9090 9091

# The distroless :nonroot tag already defaults to 65532; state it explicitly so
# the image is non-root regardless of any future base change, and so admission
# policy in TASK-034 sees a numeric non-root USER (not a name).
USER 65532:65532

# No shell exists here, so there is no shell-form HEALTHCHECK to add. Kubernetes
# httpGet probes against /healthz and /readyz on :9090 own liveness/readiness
# (deployment.md §4, owned by TASK-031). A HEALTHCHECK is deliberately omitted.

ENTRYPOINT ["/usr/local/bin/mcpmock"]
# Default to serving the mounted scenario. `--path` (not `--config`) is the real
# flag; an empty --path would serve the built-in default scenario instead.
CMD ["serve", "--path", "/etc/mcpmock/scenario.yaml"]
