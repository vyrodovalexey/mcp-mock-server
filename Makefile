# mcpmock — build system and verification gates (TASK-001)
#
# This Makefile is the contract every subsequent development and devops task's
# verification gate runs against. Target names are stable; do not rename them.
#
# Module: github.com/vyrodovalexey/mcp-mock-server   (go.mod)
# Refs:   ADR-013 (dependency policy, build flags), deployment.md §7.7 (CI targets),
#         task-breakdown-phase1.md TASK-001.

# ---- shell / make hygiene -------------------------------------------------
SHELL := /bin/bash
.SHELLFLAGS := -eu -o pipefail -c
.DEFAULT_GOAL := help

# ---- pinned tool versions -------------------------------------------------
# golangci-lint is pinned by the user and confirmed as the current latest.
# `lint` fails loudly if the invoked binary does not match this pin.
GOLANGCI_LINT_VERSION := v2.13.2
GOVULNCHECK_VERSION   := latest

# ---- paths ----------------------------------------------------------------
BIN_DIR   := bin
BINARY    := mcpmock
MAIN_PKG  := ./cmd/mcpmock
COVER_DIR := coverage

# ---- version stamping (ldflags) -------------------------------------------
VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT     ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
BUILD_TIME ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

# ---- build flags (ADR-013 §"Build flags") ---------------------------------
# CGO_ENABLED=0 + -trimpath -> single statically linked binary, no runtime deps.
CGO_ENABLED := 0
GO_LDFLAGS  := -s -w \
	-X main.version=$(VERSION) \
	-X main.commit=$(COMMIT) \
	-X main.date=$(BUILD_TIME)
GO_BUILDFLAGS := -trimpath -ldflags="$(GO_LDFLAGS)"

# Release matrix (task-breakdown TASK-001 criterion 2 / deployment.md §2).
PLATFORMS := linux/amd64 linux/arm64 darwin/arm64

# ---- container image (TASK-030, deployment.md §2) -------------------------
# IMAGE_REPO:IMAGE_TAG is the local tag `make docker-build` loads into the
# Docker Desktop daemon. That daemon is shared with the docker-desktop
# Kubernetes cluster (the only authorised deploy target), so a locally-built
# tag with imagePullPolicy: IfNotPresent needs no registry — this is the tag
# TASK-031's chart should default to for a local deploy. It is intentionally
# NOT a registry path; CI (TASK-032) retags to ghcr.io on push.
IMAGE_REPO ?= mcpmock
IMAGE_TAG  ?= dev-local
IMAGE      := $(IMAGE_REPO):$(IMAGE_TAG)
# Linux platforms the multi-arch image targets. The single-arch `docker-build`
# defaults to the host arch (buildx --load takes exactly one platform).
IMAGE_PLATFORMS := linux/amd64,linux/arm64
# Build args shared by every image target: the same version stamp `make build`
# uses, so the binary in the image reports the same identity as a local build.
IMAGE_BUILD_ARGS := \
	--build-arg VERSION=$(VERSION) \
	--build-arg COMMIT=$(COMMIT) \
	--build-arg BUILD_TIME=$(BUILD_TIME)

# ---- Helm chart (TASK-031, deployment.md §4) ------------------------------
# CHART is the chart directory CI (TASK-032) references as helm/mcpmock; RELEASE
# and NAMESPACE match ci.yml's HELM_RELEASE_NAME / HELM_NAMESPACE so a local
# `helm template` renders the same names CI does. kubeconform validates the
# rendered manifests against the Kubernetes API schemas (strict: unknown fields
# fail). KUBECONFORM_KVER pins the schema set to the target cluster's minor.
CHART            := helm/mcpmock
HELM_RELEASE     := mcpmock
HELM_NAMESPACE   := mcpmock-test
KUBECONFORM_KVER := 1.35.0

# A main package only exists from TASK-024 onward. Until then `build` compiles
# the whole module (which may be zero packages — a valid pass) and skips binary
# emission with a loud, honest notice rather than faking a green artifact.
HAVE_MAIN := $(shell test -d cmd/mcpmock && echo yes || echo no)

# =========================================================================
# Help
# =========================================================================
.PHONY: help
help: ## Show this help
	@echo "mcpmock — make targets"
	@echo ""
	@grep -hE '^[a-zA-Z0-9_-]+:.*?## ' $(MAKEFILE_LIST) \
		| sort \
		| awk 'BEGIN {FS = ":.*?## "} {printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}'

# =========================================================================
# Build
# =========================================================================
.PHONY: build
build: ## Compile the module; emit bin/mcpmock for the host platform when cmd/mcpmock exists
	@go build ./...
	@# `go vet ./...` exits non-zero on a zero-package tree ("no packages to vet").
	@# Until the first package lands, that is a valid pass — only vet when packages exist.
	@if [ -n "$$(go list ./... 2>/dev/null)" ]; then \
		go vet ./...; \
	else \
		echo "NOTE: no Go packages yet — skipping 'go vet' (empty-tree valid pass)."; \
	fi
ifeq ($(HAVE_MAIN),yes)
	@mkdir -p $(BIN_DIR)
	CGO_ENABLED=$(CGO_ENABLED) go build $(GO_BUILDFLAGS) -o $(BIN_DIR)/$(BINARY) $(MAIN_PKG)
	@echo "built $(BIN_DIR)/$(BINARY) ($(VERSION) $(COMMIT))"
else
	@echo "NOTE: $(MAIN_PKG) does not exist yet (arrives in TASK-024);"
	@echo "      'go build ./...' and 'go vet ./...' passed over the current package set."
endif

.PHONY: build-all
build-all: ## Cross-compile static binaries for every release platform (needs cmd/mcpmock)
ifeq ($(HAVE_MAIN),yes)
	@mkdir -p $(BIN_DIR)
	@for platform in $(PLATFORMS); do \
		os=$${platform%/*}; arch=$${platform#*/}; \
		out=$(BIN_DIR)/$(BINARY)-$${os}-$${arch}; \
		echo "building $${out}"; \
		CGO_ENABLED=$(CGO_ENABLED) GOOS=$${os} GOARCH=$${arch} \
			go build $(GO_BUILDFLAGS) -o $${out} $(MAIN_PKG); \
	done
else
	@echo "ERROR: $(MAIN_PKG) does not exist yet (arrives in TASK-024); nothing to cross-compile." >&2
	@exit 1
endif

# =========================================================================
# Test
# =========================================================================
.PHONY: test
test: test-unit ## Alias for the unit-test suite

.PHONY: test-unit
test-unit: ## Run unit tests with the race detector and coverage
	@mkdir -p $(COVER_DIR)
	go test -race -covermode=atomic -coverprofile=$(COVER_DIR)/unit.out ./...

.PHONY: test-functional
test-functional: ## Run the -tags=functional suite (ci.yml:126)
	@mkdir -p $(COVER_DIR)
	go test -race -covermode=atomic -coverprofile=$(COVER_DIR)/functional.out -tags=functional ./test/functional/...

.PHONY: test-integration
test-integration: ## Run the -tags=integration suite (ci.yml:222)
	@mkdir -p $(COVER_DIR)
	go test -race -covermode=atomic -coverprofile=$(COVER_DIR)/integration.out -tags=integration -timeout=5m ./test/integration/...

.PHONY: test-e2e
test-e2e: ## Run the -tags=e2e suite (ci.yml:281)
	@mkdir -p $(COVER_DIR)
	go test -race -covermode=atomic -coverprofile=$(COVER_DIR)/e2e.out -tags=e2e -timeout=5m ./test/e2e/...

.PHONY: test-perf
test-perf: ## Run the -tags=perf Go benchmarks
	go test -run=^$$ -bench=. -tags=perf ./test/perf/...

# =========================================================================
# Lint / format / modules
# =========================================================================
.PHONY: lint
lint: ## Run golangci-lint (v2.13.2 pin) with the repository .golangci.yml
	@command -v golangci-lint >/dev/null 2>&1 || { \
		echo "ERROR: golangci-lint not found. Run 'make tools' to install $(GOLANGCI_LINT_VERSION)." >&2; \
		exit 1; }
	@have="v$$(golangci-lint version --short 2>/dev/null)"; \
	want="$(GOLANGCI_LINT_VERSION)"; \
	if [ "$${have}" != "$${want}" ]; then \
		echo "ERROR: golangci-lint version mismatch." >&2; \
		echo "       installed: $${have}" >&2; \
		echo "       required : $${want} (pinned in Makefile and ci.yml)" >&2; \
		echo "       Run 'make tools' to install the pinned version." >&2; \
		exit 1; \
	fi; \
	echo "golangci-lint $${have} (pinned match)"; \
	if [ -z "$$(go list ./... 2>/dev/null)" ]; then \
		echo "NOTE: no Go packages yet — nothing for golangci-lint to analyze (empty-tree valid pass)."; \
		exit 0; \
	fi; \
	golangci-lint run

.PHONY: fmt
fmt: ## Format Go source (gofmt) and tidy imports
	gofmt -s -w .
	go fmt ./...

.PHONY: fmt-check
fmt-check: ## Fail if any Go file is not gofmt-clean
	@out="$$(gofmt -s -l . 2>/dev/null)"; \
	if [ -n "$${out}" ]; then \
		echo "ERROR: the following files are not gofmt-clean:" >&2; \
		echo "$${out}" >&2; \
		exit 1; \
	fi; \
	echo "gofmt: clean"

.PHONY: tidy
tidy: ## Reconcile go.mod / go.sum
	go mod tidy
	go mod verify

# =========================================================================
# Security / vulnerability
# =========================================================================
.PHONY: vuln
vuln: ## Scan for known vulnerabilities (govulncheck — the baseline scanner)
	@command -v govulncheck >/dev/null 2>&1 || { \
		echo "ERROR: govulncheck not found. Run 'make tools' to install it." >&2; \
		exit 1; }
	@if [ -z "$$(go list ./... 2>/dev/null)" ]; then \
		echo "NOTE: no Go packages yet — nothing for govulncheck to scan (empty-tree valid pass)."; \
	else \
		govulncheck ./...; \
	fi

# =========================================================================
# Policy gates (TASK-033).
#
# TASK-001 created these as deliberately fail-closed stubs so downstream CI
# gates could invoke stable target names before the owning tasks landed. The
# underlying rules were then implemented by development agents as Go tests or
# runnable programs, but those agents could not wire them because the Makefile
# was outside their scope. TASK-033 does the wiring — NOT the re-implementation.
#
# Each target below REUSES an existing implementation so there is one source of
# truth: duplicating the rule logic in shell would let the two copies drift.
# Every target is fail-closed: no `|| true`, no `-` prefix. A gate that cannot
# run does not pass — it fails loudly or skips loudly, and says which.
#
# determinism-check is OWNED BY TASK-026 (task-breakdown-phase1.md:1385) and is
# left as its stub below so TASK-026 can land it without a merge collision. It
# is intentionally NOT part of the `policy-gates` aggregate here for the same
# reason; TASK-026 adds it when its scanner is wired.
# =========================================================================
define NOT_IMPLEMENTED
	@echo "NOT-IMPLEMENTED: '$(1)' is a fail-closed stub (owner: $(2))." >&2
	@echo "                 It exits non-zero by design until that task lands it." >&2
	@exit 1
endef

# deps-check invokes the hack/deps-check oracle, which enforces ADR-013's
# two-tier (runtime vs test-only) policy using `go list -deps` — the mechanical
# oracle ADR-013 §Enforcement names (AMEND-5). It admits go.uber.org/goleak (a
# spec-mandated MOCK-107.7 test-only dep) and fails on a test-only leak into the
# runtime graph or a forbidden direct dependency. The approved sets live in that
# one Go file so the check and the ADR cannot drift.
.PHONY: deps-check
deps-check: ## Enforce ADR-013's two-tier dependency policy (runtime vs test-only) via go list -deps
	@go run ./hack/deps-check

# globals-check invokes TASK-028's AST-based auditor. It lives in the EXTERNAL
# embed module (test/e2e/embed) because only an out-of-module consumer can see
# the public surface the way a host test would; it must therefore run with that
# module as the working directory. -root points it back at this checkout. It
# fails on any mutable package-level var (MOCK-107.5) or side-effecting init()
# (MOCK-107.6) in the four public packages, and its own tests include the proven
# negative case (an injected `var Leaked` is detected).
.PHONY: globals-check
globals-check: ## Assert no mutable package-level state / side-effecting init() in the public packages (TASK-028 AST audit)
	@go -C test/e2e/embed run ./globalscheck/cmd/globalscheck -root ../../..

# schema-check invokes TASK-006's Go test, which asserts the go:embed-ed scenario
# schema is SHA-256-identical to specification/contracts/scenario.schema.json
# (ADR-008 / MOCK-701.3). Reusing the test keeps one source of truth for the
# drift guard rather than re-hashing in shell.
.PHONY: schema-check
schema-check: ## Assert the embedded scenario schema is SHA-256-identical to the spec (TASK-006 test)
	@go test -run '^TestEmbeddedSchema_ByteIdenticalToSpec$$' -count=1 ./internal/config/

# wire-literal-check invokes TASK-010's AST-based containment test (ADR-019): no
# wire method name or resultType value may appear as a string literal outside
# internal/wire and test/mcpclient. That test carries the documented allowlist
# for internal/obs/metrics.go (the three method names are Prometheus label-domain
# values, not wire emission — observability.md §2.1); running the test PRESERVES
# that allowlist rather than re-encoding it in shell.
.PHONY: wire-literal-check
wire-literal-check: ## Assert no wire literals escape internal/wire, preserving the internal/obs allowlist (TASK-010 test, ADR-019)
	@go test -run '^TestNoWireLiteralsEscape$$|^TestPackageIsSoleDefinitionSite$$' -count=1 ./internal/wire/

# secrets-check scans the working tree for committed credentials (security.md
# §3). gitleaks/trufflehog are NOT installed on this workstation, so — exactly
# like image-scan (TASK-030) — this target FAILS CLOSED locally: an unrun scan is
# not a clean scan. In CI the pinned gitleaks action supplies the binary and this
# gate runs for real. No `|| true`, no `-` prefix.
.PHONY: secrets-check
secrets-check: ## Scan the tree for committed secrets (gitleaks); FAILS CLOSED if no scanner is present
	@if command -v gitleaks >/dev/null 2>&1; then \
		echo "secrets-check: scanning with gitleaks (fail-closed on findings)"; \
		gitleaks detect --source . --no-banner --redact --exit-code 1; \
	elif command -v trufflehog >/dev/null 2>&1; then \
		echo "secrets-check: scanning with trufflehog (fail-closed on findings)"; \
		trufflehog filesystem . --fail --no-update; \
	else \
		echo "ERROR: no secret scanner (gitleaks/trufflehog) on PATH — cannot scan the tree." >&2; \
		echo "       This target FAILS CLOSED: an unrun scan is not a clean scan (image-scan precedent)." >&2; \
		echo "       Install gitleaks, or run this in CI where the pinned gitleaks action supplies it." >&2; \
		exit 1; \
	fi

# corpus-verify will assert the hostile-corpus manifest hashes. That corpus is
# Phase 9 work (MOCK-507 / ADR-018); it does NOT exist in the Phase 1 tree, so
# there is nothing to verify. Rather than fake a green tick, this gate FAILS
# CLOSED with an honest explanation — until the corpus lands it cannot pass. It
# is intentionally NOT in the `policy-gates` aggregate below so the Phase 1 CI
# gate is not blocked on a Phase 9 deliverable.
.PHONY: corpus-verify
corpus-verify: ## Assert hostile-corpus manifest hashes (Phase 9 / MOCK-507); FAILS CLOSED — nothing to verify in Phase 1
	@if [ -f specification/corpus/manifest.sha256 ] || [ -d testdata/corpus ]; then \
		echo "ERROR: a corpus manifest appeared but corpus-verify has no verifier wired yet (owner: MOCK-507 / ADR-018)." >&2; \
		echo "       Escalate to the corpus owner; do not bypass." >&2; \
		exit 1; \
	fi; \
	echo "ERROR: corpus-verify has nothing to verify — the hostile corpus is Phase 9 (MOCK-507 / ADR-018)," >&2; \
	echo "       not present in the Phase 1 tree. This gate FAILS CLOSED rather than emit a false pass." >&2; \
	echo "       It will be implemented when the corpus lands. Not wired into 'make policy-gates'." >&2; \
	exit 1

.PHONY: determinism-check
determinism-check: ## [stub] AST scan for unseeded randomness on response paths (owner: TASK-026)
	$(call NOT_IMPLEMENTED,determinism-check,PRIN-2 / TASK-026)

# policy-gates runs every Phase-1-ready gate TASK-033 owns, in one command, so
# local and CI verdicts match. It excludes:
#   - determinism-check — owned by TASK-026, wired when its scanner lands.
#   - corpus-verify      — Phase 9 (MOCK-507); fails closed, not a Phase 1 gate.
#   - secrets-check      — runs here but FAILS CLOSED without a scanner locally;
#                          CI runs it for real via the pinned gitleaks action, so
#                          it is gated in CI as a separate job, not in this local
#                          aggregate (which must be runnable on a clean laptop).
.PHONY: policy-gates
policy-gates: deps-check globals-check schema-check wire-literal-check ## Run the Phase-1 policy gates TASK-033 owns (deps/globals/schema/wire-literal)
	@echo "policy-gates: all Phase-1 gates passed (deps-check, globals-check, schema-check, wire-literal-check)."

# =========================================================================
# Packaging — container image (TASK-030). Chart targets remain stubs (TASK-031).
# =========================================================================

# docker-build builds the distroless image for the HOST architecture and loads
# it into the local Docker Desktop daemon under $(IMAGE). One platform only:
# buildx --load cannot load a multi-arch manifest into the daemon. Use
# `make docker-buildx` to build (and verify) the full linux/amd64,linux/arm64
# matrix. `docker` is kept as an alias because deployment.md §7.7 names it.
.PHONY: docker docker-build
docker docker-build: ## Build the distroless image for the host arch and load it as $(IMAGE)
	@command -v docker >/dev/null 2>&1 || { \
		echo "ERROR: docker not found on PATH." >&2; exit 1; }
	@echo "building $(IMAGE) (host arch) — VERSION=$(VERSION) COMMIT=$(COMMIT)"
	docker buildx build \
		--load \
		--tag $(IMAGE) \
		$(IMAGE_BUILD_ARGS) \
		.
	@echo "loaded $(IMAGE) into the local daemon"
	@docker images $(IMAGE_REPO) --format 'table {{.Repository}}\t{{.Tag}}\t{{.Size}}'

# docker-buildx builds the full multi-arch matrix to verify both platforms
# compile and assemble. buildx cannot --load a multi-arch result into the
# daemon and this task must not push to a registry, so the result is validated
# in the builder cache only (no --load, no --push). This proves arm64 + amd64
# build; CI (TASK-032) owns the push+sign of the multi-arch manifest.
#
# Multi-platform needs a docker-container (or containerd-store) builder — the
# default "docker" driver cannot do it. BUILDER selects one; override with
# `make docker-buildx BUILDER=<name>`. Empty BUILDER uses the current builder
# and will fail loudly if that is the default driver, which is the correct
# fail-closed behaviour (better an explicit error than a silent host-arch build).
BUILDER ?=
.PHONY: docker-buildx
docker-buildx: ## Build (verify only) the linux/amd64,linux/arm64 matrix — no load, no push (needs a container builder)
	@command -v docker >/dev/null 2>&1 || { \
		echo "ERROR: docker not found on PATH." >&2; exit 1; }
	@echo "building $(IMAGE_PLATFORMS) (verify only, not loaded/pushed)"
	docker buildx build \
		$(if $(BUILDER),--builder $(BUILDER),) \
		--platform $(IMAGE_PLATFORMS) \
		--tag $(IMAGE) \
		$(IMAGE_BUILD_ARGS) \
		.

# image-scan runs Trivy against the locally-built image and FAILS CLOSED on any
# HIGH/CRITICAL. Trivy is NOT installed on this workstation (deployment.md §0),
# so locally this target fails closed with an explicit message rather than
# silently passing — a scan that cannot run is not a clean scan. In CI the
# pinned aquasecurity/trivy action provides the binary and this target runs for
# real. No `|| true`, no `-` prefix: an absent scanner blocks completion.
.PHONY: image-scan
image-scan: ## Trivy-scan $(IMAGE) for HIGH/CRITICAL; FAILS CLOSED if trivy is absent
	@if ! command -v trivy >/dev/null 2>&1; then \
		echo "ERROR: trivy not found on PATH — cannot scan $(IMAGE)." >&2; \
		echo "       This target FAILS CLOSED: an unrun scan is not a passing scan." >&2; \
		echo "       Install trivy ('make tools' in a later task) or run this in CI," >&2; \
		echo "       where the pinned aquasecurity/trivy-action supplies the binary." >&2; \
		exit 1; \
	fi; \
	echo "scanning $(IMAGE) for HIGH,CRITICAL (fail-closed on findings)"; \
	trivy image --severity HIGH,CRITICAL --exit-code 1 --no-progress $(IMAGE)

# helm-lint lints the chart against its values.schema.json and Helm's chart
# conventions. Fail-closed: an absent helm binary blocks the gate (no `|| true`).
.PHONY: helm-lint
helm-lint: ## helm lint the chart (TASK-031); FAILS CLOSED if helm is absent
	@command -v helm >/dev/null 2>&1 || { \
		echo "ERROR: helm not found on PATH — cannot lint $(CHART)." >&2; \
		echo "       This gate FAILS CLOSED: install helm 3.19.1 (deployment.md §0)." >&2; \
		exit 1; }
	@echo "helm lint $(CHART)"
	helm lint $(CHART)

# helm-template renders the chart and validates every rendered manifest with
# kubeconform in strict mode (unknown fields fail) against the target cluster's
# API schema set. It renders BOTH the default profile and the large-fleet profile
# so a profile-specific templating error cannot hide. It also asserts the §4.1
# control-token guard fires. Fail-closed: absent helm or kubeconform blocks the
# gate. No `|| true`, no `-` prefix.
.PHONY: helm-template
helm-template: ## helm template + kubeconform (strict) the chart (TASK-031); FAILS CLOSED if a tool is absent
	@command -v helm >/dev/null 2>&1 || { \
		echo "ERROR: helm not found on PATH — cannot render $(CHART)." >&2; \
		echo "       This gate FAILS CLOSED: install helm 3.19.1 (deployment.md §0)." >&2; \
		exit 1; }
	@command -v kubeconform >/dev/null 2>&1 || { \
		echo "ERROR: kubeconform not found on PATH — cannot validate rendered manifests." >&2; \
		echo "       This gate FAILS CLOSED: a render that is not schema-validated is not a pass." >&2; \
		echo "       Install: go install github.com/yannh/kubeconform/cmd/kubeconform@latest" >&2; \
		exit 1; }
	@echo "helm template $(HELM_RELEASE) $(CHART) (default profile) | kubeconform -strict"
	helm template $(HELM_RELEASE) $(CHART) --namespace $(HELM_NAMESPACE) \
		| kubeconform -strict -summary -kubernetes-version $(KUBECONFORM_KVER) -verbose
	@echo "helm template (large-fleet profile) | kubeconform -strict"
	helm template $(HELM_RELEASE) $(CHART) --namespace $(HELM_NAMESPACE) \
		--set profile=large-fleet \
		| kubeconform -strict -summary -kubernetes-version $(KUBECONFORM_KVER)
	@echo "asserting the control-token guard (§4.1.2) fails closed on control.enabled without a token"
	@if helm template $(HELM_RELEASE) $(CHART) --set control.enabled=true >/dev/null 2>&1; then \
		echo "ERROR: control.enabled=true without a token secret rendered successfully — the guard is broken." >&2; \
		exit 1; \
	fi
	@echo "helm-template: chart renders and validates for default + large-fleet, and the control-token guard fires."

# helm-manifests-sync asserts the hand-checked plain manifests (deploy/manifests,
# deployment.md §8) stay in sync with the chart's default-profile render for the
# fields that must match (image, args, ports, probes, securityContext). It is a
# lightweight drift guard, not a byte-diff (the plain manifests carry a namespace
# and manual labels the chart parameterises). Fail-closed on a missing renderer.
.PHONY: helm-manifests-sync
helm-manifests-sync: ## Assert deploy/manifests stays in sync with the chart's default render (deployment.md §8)
	@command -v helm >/dev/null 2>&1 || { \
		echo "ERROR: helm not found — cannot check manifest sync." >&2; exit 1; }
	@render="$$(helm template $(HELM_RELEASE) $(CHART) --namespace $(HELM_NAMESPACE))"; \
	for needle in \
		'mcpmock:dev-local' \
		'0.0.0.0:8080' \
		'path: /healthz' \
		'path: /readyz' \
		'readOnlyRootFilesystem: true' \
		'runAsUser: 65532'; do \
		if ! printf '%s' "$$render" | grep -qF "$$needle"; then \
			echo "ERROR: chart render is missing expected content: $$needle" >&2; exit 1; \
		fi; \
		if ! grep -rqF "$$needle" deploy/manifests/ 2>/dev/null; then \
			echo "ERROR: deploy/manifests drifted from the chart: missing $$needle" >&2; exit 1; \
		fi; \
	done; \
	echo "helm-manifests-sync: plain manifests match the chart's default render on the load-bearing fields."

# =========================================================================
# Tooling
# =========================================================================
.PHONY: tools
tools: ## Install pinned dev tools (golangci-lint, govulncheck) into GOBIN
	@echo "installing golangci-lint $(GOLANGCI_LINT_VERSION) ..."
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	@echo "installing govulncheck $(GOVULNCHECK_VERSION) ..."
	go install golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION)
	@echo "done. Ensure \$$(go env GOBIN) or \$$(go env GOPATH)/bin precedes other golangci-lint on PATH."

# =========================================================================
# Clean
# =========================================================================
.PHONY: clean
clean: ## Remove build and coverage artifacts
	rm -rf $(BIN_DIR) $(COVER_DIR)
	@echo "cleaned $(BIN_DIR)/ $(COVER_DIR)/"
