# Clustarr build tooling. Targets follow kubebuilder conventions.

GOBIN ?= $(shell go env GOPATH)/bin
CONTROLLER_GEN ?= $(GOBIN)/controller-gen
SETUP_ENVTEST ?= $(GOBIN)/setup-envtest
KUSTOMIZE ?= $(GOBIN)/kustomize
GOLANGCI_LINT ?= $(GOBIN)/golangci-lint-v2
ENVTEST_K8S_VERSION ?= 1.37.0
# PG_ASSETS is a cache directory embedded-postgres downloads its Postgres
# binaries into (spec §A.2), the way SETUP_ENVTEST populates
# KUBEBUILDER_ASSETS. `make pg-assets` populates it once; `test` and
# test-race export it as CLUSTARR_PG_ASSETS so pkg/relindex's
# TestPostgresStoreContract runs instead of skipping.
PG_ASSETS ?= $(GOBIN)/pg-assets
# NATIVE_ASSETS holds what the native tests load and compare against
# (hack/native-assets.sh, spec §10.1.6): FFmpeg 9's libraries and libffshim,
# ONNX Runtime, libpar2shim, and the test-only ffmpeg, ffprobe and par2
# CLIs. ffgo finds libavutil, libavcodec and libavformat only through
# LD_LIBRARY_PATH and fixed system directories, at package init, so the
# variable must be in the test process's environment from the start;
# FFGO_SHIM_DIR locates only the shim. CLUSTARR_REQUIRE_TOOLS=1 turns a
# missing tool (FFmpeg, the shims, ONNX Runtime, helm, kustomize,
# KUBEBUILDER_ASSETS) into a failure instead of a skip (OD44).
NATIVE_ASSETS ?= $(GOBIN)/native-assets
NATIVE_ENV = LD_LIBRARY_PATH="$(NATIVE_ASSETS)/lib" \
	CLUSTARR_NATIVE_ASSETS="$(NATIVE_ASSETS)" \
	FFGO_SHIM_DIR="$(NATIVE_ASSETS)/lib" \
	ORT_LIB_PATH="$(NATIVE_ASSETS)/lib/libonnxruntime.so" \
	PAR2GO_LIB="$(NATIVE_ASSETS)/lib/libpar2shim.so" \
	PAR2GO_REQUIRE=1 \
	PATH="$(NATIVE_ASSETS)/bin:$$PATH" \
	CLUSTARR_REQUIRE_TOOLS=1

IMG ?= ghcr.io/mediactl/clustarr:dev
NATIVE_IMG ?= ghcr.io/mediactl/clustarr/native:dev
NATIVE_DEBUG_IMG ?= ghcr.io/mediactl/clustarr/native-debug:dev
IMAGE_VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
FIXTURES_IMG ?= ghcr.io/mediactl/clustarr/e2e-fixtures:dev
# The ffgo fork, which go.mod replaces with a local directory until its tag is
# published (spec §7.6, R12; Wave 0 of
# docs/superpowers/plans/2026-10-06-manager-agent-split.md names the
# directory). Image builds never read that working tree: `make contexts`
# exports $(FFGO_REF) with git archive, and every docker build passes it as
# the named context the Dockerfiles' empty ffgo stage stands in for.
# FFGO_LOCAL is the directory go.mod names (empty once go.mod replaces with a
# published version). par2go is a published module with no replace (upgrade
# guide U1): PAR2GO_VERSION is go.mod's version of it, which native-assets
# and the native image's par2 stage download the release asset of.
FFGO_LOCAL := $(shell sed -nE 's#^replace github.com/obinnaokechukwu/ffgo => \.\./(.+)$$#\1#p' go.mod)
FFGO_DIR ?= ../$(or $(FFGO_LOCAL),ffgo)
FFGO_REF ?= v0.0.0-clustarr.13
CONTEXTS ?= $(or $(TMPDIR),/tmp)/clustarr-contexts-$(USER)
LOCAL_CONTEXTS := $(if $(FFGO_LOCAL),--build-context ffgo=$(CONTEXTS)/ffgo --build-arg FFGO_COMMIT=$(shell git -C $(FFGO_DIR) rev-parse $(FFGO_REF)^{commit} 2>/dev/null))
PAR2GO_VERSION := $(shell go list -m -f '{{.Version}}' github.com/mediactl/par2go 2>/dev/null)

API_PATHS := ./api/...
CRD_DIR := config/crd/bases

# The RBAC ClusterRoles `make manifests` generates, one per identity that runs
# under its own ServiceAccount (spec 2026-10-06 §4.6, §10.2.4): the manager,
# each agent domain's Deployment, and grab-engine, the identity of the engine
# pods the manager's DownloadClient controller renders. ui's role is
# hand-written (config/rbac/ui_role.yaml); markers and the transcode pool and
# graft Jobs run with no ServiceAccount token and hold no role.
#
# Each role is generated from exactly the packages in RBAC_PATHS_<role>,
# written to config/rbac/<role>_role.yaml (hyphens as underscores) as
# ClusterRole clustarr-<role>-role. The paths are explicit: a glob such as
# ./app/catalog/... cannot separate the manager's controllers from the
# agents' workers, and a role holds only what its process runs. The manager
# role alone grants writes: no package an agent links carries a write marker
# (ADR-0019, A7.2), so every agent role and the engine role hold get, list
# and watch only, secrets get only. Each RBAC_PATHS_* stays on one line: the
# guards read this file line by line.
#
# test/guards reads RBAC_ROLES and RBAC_PATHS_* from here rather than
# restating them: TestEveryPackageWithRBACMarkersIsInARole fails when a
# package with markers is in no role, TestEachRoleCoversTheMarkedPackagesItsHomeLinks
# when a role's paths and what its process links disagree,
# TestConfigRBACHoldsOnlyKnownRoleFiles when config/rbac keeps a retired role,
# TestNoAgentRoleGrantsAWriteVerb when an agent role grants a write, and
# TestChartRBACMatchesTheGeneratedRoles holds the chart's copies to these
# files byte for byte.
RBAC_ROLES := manager agent-catalog agent-events agent-metadata agent-import agent-index agent-caption grab-engine
RBAC_PATHS_manager := ./app/catalog/controller/... ./app/catalog/history/replay ./app/catalog/worker/history ./app/catalog/segmentplan ./app/catalog/artwork ./app/remediation/... ./app/intake/... ./app/dispatch/... ./app/import/controller/... ./app/import/manager/... ./app/indexer/controller/... ./app/indexer/manager ./app/indexer/bundle/... ./app/indexer/clients ./app/indexer/proxy ./app/indexer/status/... ./app/grab/controller/... ./app/transcode/controller/... ./app/transcode/status ./app/transcode/admission ./app/caption/controller/... ./app/caption/providerset ./app/autoscale/... ./app/livetv/...
RBAC_PATHS_agent-catalog := ./app/catalog/worker/search ./app/catalog/worker/artwork
RBAC_PATHS_agent-events := ./app/catalog/worker/rssmatcher
RBAC_PATHS_agent-metadata := ./app/catalog/metadata/... ./app/catalog/worker/markers
RBAC_PATHS_agent-import := ./app/import/worker/...
RBAC_PATHS_agent-index := ./app/indexer/agent ./app/indexer/clients ./app/indexer/clientcache ./app/indexer/proxy ./app/indexer/search ./app/indexer/query ./app/indexer/download ./app/indexer/facade ./app/indexer/worker/...
RBAC_PATHS_agent-caption := ./app/caption/worker/... ./app/caption/providerset/...
RBAC_PATHS_grab-engine := ./app/grab/engine/... ./app/grab/agent/...

.PHONY: all
all: generate manifests build

##@ Development

# controller-gen v0.22.0's applyconfiguration generator ignores output rules: it
# writes straight to disk at <pkg dir>/<kubebuilder:ac:output:package>, so no
# output:applyconfiguration:dir flag is passed below (it is silently discarded).
# Generation is opt-in per package via +kubebuilder:ac:generate=true and the
# destination is set per group by +kubebuilder:ac:output:package, both in each
# groupversion_info.go. The generator nests each group under its own name
# (api/applyconfiguration/<group>/<group>/<version>); hack/flatten-applyconfiguration.sh
# moves every version up to api/applyconfiguration/<group>/<version> and rewrites
# the imports. A shared output root is no fix: the groups would share one
# internal/internal.go and the last group's schema would overwrite the rest.
.PHONY: generate
generate: ## Generate DeepCopy and apply-configuration code.
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="$(API_PATHS)"
	$(CONTROLLER_GEN) applyconfiguration:headerFile="hack/boilerplate.go.txt" paths="$(API_PATHS)"
	hack/flatten-applyconfiguration.sh

# No templ binary is installed; `go run` against the version go.mod already
# pins (v0.3.1020) generates the same *_templ.go it would, without adding a
# tool dependency. The generated files are committed, so a generator change
# can never break a build unattended -- this target is for regenerating them
# after editing a .templ file, not a build-time step.
.PHONY: brand-gate
brand-gate: ## Fail if a retired *arr brand name appears outside the records.
	hack/brand-gate.sh

.PHONY: templ
templ: ## Regenerate ui/views/*_templ.go and ui/components/*/*_templ.go from their .templ sources.
	go run github.com/a-h/templ/cmd/templ@v0.3.1020 generate

# Tailwind's standalone CLI is a platform binary, not a Go module -- Phase G
# ruling R4 (docs/superpowers/plans/2026-09-23-phase-g-parity.md) is no Node
# and no new Go dependency for a CSS build step. Pinned to v4.3.3 (the
# standalone CLI's latest release as of 2026-09-23; there is no older LTS
# line to track) with a sha256 recorded per asset below, verified against
# that release's sha256sums.txt, so a corrupted or substituted download
# fails loudly instead of silently changing the CSS that ui/static.go later
# embeds. Like `templ`, this is a standalone target: `make build` never
# invokes it, and the output (ui/static/app.css) is committed so a build
# never needs the tailwindcss binary at all.
TAILWINDCSS_VERSION ?= 4.3.3
TAILWINDCSS ?= $(GOBIN)/tailwindcss-$(TAILWINDCSS_VERSION)
TAILWINDCSS_OS := $(shell go env GOOS)
TAILWINDCSS_ARCH := $(shell go env GOARCH)

# Maps GOOS/GOARCH to the asset name tailwindlabs/tailwindcss publishes at
# https://github.com/tailwindlabs/tailwindcss/releases/download/v$(TAILWINDCSS_VERSION)/<asset>
# and to that asset's sha256. Only the platforms this project is built and
# developed on are listed; an unlisted GOOS/GOARCH fails the target rather
# than fetching an unverified binary.
ifeq ($(TAILWINDCSS_OS)_$(TAILWINDCSS_ARCH),linux_amd64)
TAILWINDCSS_ASSET := tailwindcss-linux-x64
TAILWINDCSS_SHA256 := dc61b3ac6b8c9ca874c0cc4c57b2409791a64c5540404ca5f5367360babc313a
else ifeq ($(TAILWINDCSS_OS)_$(TAILWINDCSS_ARCH),linux_arm64)
TAILWINDCSS_ASSET := tailwindcss-linux-arm64
TAILWINDCSS_SHA256 := 55fd0b241214eff3de1e8ee4f22796662f2d2e7a49bcfca7477cfd0bac398195
else ifeq ($(TAILWINDCSS_OS)_$(TAILWINDCSS_ARCH),darwin_amd64)
TAILWINDCSS_ASSET := tailwindcss-macos-x64
TAILWINDCSS_SHA256 := 7922e0953f2110c05976e3bf58f14e643d90427575e766b7d433f5f80cbee7e1
else ifeq ($(TAILWINDCSS_OS)_$(TAILWINDCSS_ARCH),darwin_arm64)
TAILWINDCSS_ASSET := tailwindcss-macos-arm64
TAILWINDCSS_SHA256 := cdf646702987a743464dff4d9c60fd4480d1c1e73dd819a9a67f1078815dce9d
endif

.PHONY: css
css: ## Build ui/static/app.css with the standalone Tailwind CLI (downloads it into GOBIN if absent).
	@if [ -z "$(TAILWINDCSS_ASSET)" ]; then \
		echo "no pinned tailwindcss release asset for GOOS=$(TAILWINDCSS_OS) GOARCH=$(TAILWINDCSS_ARCH); add one to the Makefile"; \
		exit 1; \
	fi
	@if [ ! -x "$(TAILWINDCSS)" ]; then \
		echo "downloading tailwindcss v$(TAILWINDCSS_VERSION) ($(TAILWINDCSS_ASSET)) into $(TAILWINDCSS)"; \
		curl -fsSL -o "$(TAILWINDCSS).tmp" "https://github.com/tailwindlabs/tailwindcss/releases/download/v$(TAILWINDCSS_VERSION)/$(TAILWINDCSS_ASSET)"; \
		echo "$(TAILWINDCSS_SHA256)  $(TAILWINDCSS).tmp" | sha256sum -c -; \
		chmod +x "$(TAILWINDCSS).tmp"; \
		mv "$(TAILWINDCSS).tmp" "$(TAILWINDCSS)"; \
	fi
	$(TAILWINDCSS) -i ui/static/input.css -o ui/static/app.css -m

.PHONY: manifests
manifests: ## Generate CRDs and one RBAC ClusterRole per identity (RBAC_ROLES).
	$(CONTROLLER_GEN) crd paths="$(API_PATHS)" output:crd:artifacts:config=$(CRD_DIR)
	@mkdir -p config/rbac
	$(foreach role,$(RBAC_ROLES),$(CONTROLLER_GEN) rbac:roleName=clustarr-$(role)-role $(foreach p,$(RBAC_PATHS_$(role)),paths=$(p)) output:rbac:stdout > config/rbac/$(subst -,_,$(role))_role.yaml &&) true

.PHONY: fmt
fmt: ## Run gofmt.
	go fmt ./...

.PHONY: vet
vet: ## Run go vet.
	go vet ./...

.PHONY: lint
lint: ## Run golangci-lint.
	$(GOLANGCI_LINT) run ./...

.PHONY: tidy
tidy: ## Tidy go.mod.
	go mod tidy

##@ Build

.PHONY: cardigann-bundle
cardigann-bundle: ## Re-pack .data/Definitions into the embedded Cardigann corpus (app/indexer/bundle/embedded).
	go run ./hack/pack-cardigann -src .data/Definitions -out app/indexer/bundle/embedded/definitions.zip

.PHONY: contexts
contexts: ## Export a clean tree of $(FFGO_REF) for the image builds (spec §7.6); refuses a dirty or moved ffgo.
	@if [ -n "$(FFGO_LOCAL)" ]; then hack/contexts.sh $(CONTEXTS) $(FFGO_DIR) $(FFGO_REF); fi

# make build and make test read the fork's working tree through go.mod;
# developing the fork additions needs a dirty tree, so this only warns.
.PHONY: fork-status
fork-status:
	@if [ -n "$(FFGO_LOCAL)" ] && [ "$$(git -C $(FFGO_DIR) describe --tags --exact-match --dirty 2>/dev/null)" != "$(FFGO_REF)" ]; then \
	  echo "WARNING: $(FFGO_DIR) is not a clean $(FFGO_REF); go.mod builds against it as it is" >&2; fi

LDFLAGS := -s -w -X github.com/mediactl/clustarr/pkg/version.Version=$(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

# grpcnotrace drops gRPC's golang.org/x/net/trace, which every binary links
# through OpenTelemetry's OTLP exporters (the HTTP one too): its init keeps
# html/template reachable, and a reachable text/template turns off the
# linker's method dead-code elimination for the whole binary (2026-10-07:
# markers 26.6 -> 21.6 MB, transcode 27.0 -> 20.7 MB). The images build with
# the same tag, and test/deadcode holds all three places to it.
GOTAGS ?= grpcnotrace

.PHONY: build
build: fork-status ## Build bin/manager, bin/ui, bin/agent, bin/markers and bin/transcode, each as its image does (spec §3.9).
	CGO_ENABLED=0 go build -trimpath -tags "$(GOTAGS)" -ldflags "$(LDFLAGS)" -o bin/manager ./cmd/manager
	CGO_ENABLED=0 go build -trimpath -tags "$(GOTAGS)" -ldflags "$(LDFLAGS)" -o bin/ui ./cmd/ui
	CGO_ENABLED=1 go build -trimpath -tags "$(GOTAGS)" -ldflags "$(LDFLAGS)" -o bin/agent ./cmd/agent
	CGO_ENABLED=0 go build -trimpath -tags "$(GOTAGS)" -ldflags "$(LDFLAGS)" -o bin/markers ./cmd/markers
	CGO_ENABLED=0 go build -trimpath -tags "$(GOTAGS)" -ldflags "$(LDFLAGS)" -o bin/transcode ./cmd/transcode

.PHONY: docker-build
docker-build: contexts ## Build the clustarr (manager, ui) and native (agent, markers, transcode) images.
	docker build $(LOCAL_CONTEXTS) --build-arg VERSION=$(IMAGE_VERSION) -f images/Dockerfile.clustarr --target clustarr -t $(IMG) .
	docker build $(LOCAL_CONTEXTS) --build-arg VERSION=$(IMAGE_VERSION) --build-arg PAR2GO_VERSION=$(PAR2GO_VERSION) -f images/Dockerfile.native --target native -t $(NATIVE_IMG) .

# The clustarr image (manager, ui) for amd64 and arm64: the control-plane
# placement design (2026-10-21, §6) runs both on the arm64 control-plane nodes.
# A two-platform push needs a docker-container buildx builder; for a local
# check, BUILDX_PLATFORMS=linux/arm64 BUILDX_OUTPUT=--load. UPX_ARM64=0 leaves
# the arm64 binaries unpacked.
BUILDX_PLATFORMS ?= linux/amd64,linux/arm64
BUILDX_OUTPUT ?= --push
UPX_ARM64 ?= 1

.PHONY: docker-buildx-clustarr
docker-buildx-clustarr: contexts ## Build (and push) the clustarr image for amd64 and arm64.
	docker buildx build $(LOCAL_CONTEXTS) --platform $(BUILDX_PLATFORMS) --build-arg VERSION=$(IMAGE_VERSION) --build-arg UPX_ARM64=$(UPX_ARM64) -f images/Dockerfile.clustarr --target clustarr -t $(IMG) $(BUILDX_OUTPUT) .

# There is no CUDA image (docs/adr/0015-no-cuda-image.md): nvidia pools run
# the native image, the NVIDIA container runtime injecting the driver.
.PHONY: docker-build-native
docker-build-native: contexts ## Build the native image and its -debug twin.
	docker build $(LOCAL_CONTEXTS) --build-arg VERSION=$(IMAGE_VERSION) --build-arg PAR2GO_VERSION=$(PAR2GO_VERSION) -f images/Dockerfile.native --target native -t $(NATIVE_IMG) .
	docker build $(LOCAL_CONTEXTS) --build-arg VERSION=$(IMAGE_VERSION) --build-arg PAR2GO_VERSION=$(PAR2GO_VERSION) -f images/Dockerfile.native --target native-debug -t $(NATIVE_DEBUG_IMG) .

# The classes the native image is self-checked for: it serves every pool,
# cpu, nvidia and intel (ADR 0015). NATIVE_CHECKS is the native image's list
# for hack/image-checks.sh, self-check:par2-child included (the usenet
# engine's repair child, spec §8.7). The debug image's list adds
# ffmpeg-libraries (upgrade guide U3).
NATIVE_CLASSES ?= cpu cuda intel
NATIVE_CHECKS ?= $(foreach c,$(NATIVE_CLASSES),self-check:transcode:$(c)) self-check:agent self-check:markers self-check:par2-child version:agent version:markers version:transcode no-shell

.PHONY: docker-build-fixtures
docker-build-fixtures: contexts ## Build the e2e fixture image.
	docker build $(LOCAL_CONTEXTS) -f images/Dockerfile.e2e-fixtures -t $(FIXTURES_IMG) .

.PHONY: docker-selfcheck
docker-selfcheck: ## Check the built images as CI and the release do (hack/image-checks.sh).
	hack/image-checks.sh $(IMG) version:manager version:ui no-shell
	hack/image-checks.sh $(NATIVE_IMG) $(NATIVE_CHECKS)
	hack/image-checks.sh $(NATIVE_DEBUG_IMG) notices no-media-executables ffmpeg-libraries

##@ Test

.PHONY: test
# TEST_PARALLEL caps how many packages run at once. Around fifteen packages
# each stand up their own envtest control plane, and running them all together
# starves the apiservers: suites fail in a DIFFERENT package on each run, pass
# 3/3 in isolation, and read as a mystery flake rather than as contention.
# Raise it on a bigger machine; lower it if the flake reappears.
# Lower this if envtest suites start failing with "timeout waiting for process
# kube-apiserver to start". Each suite runs its own control plane, so the limit
# is the machine's, not the code's -- and the failure lands in a DIFFERENT
# package on each run, which reads as a mystery flake rather than as contention.
# 4 is comfortable on an idle machine; a box already running other clusters
# wants 2.
TEST_PARALLEL ?= 4

.PHONY: test-race
# -timeout 60m: the transcode envtests took some 460 s without the race
# detector, which multiplies that several times; go's 10m default panics.
test-race: envtest pg-assets native-assets chart-deps fork-status ## Run the suites under the race detector.
	@mkdir -p "$${CLUSTARR_TEST_MEDIA_ROOT:-/data/media}" 2>/dev/null || true
	KUBEBUILDER_ASSETS="$(shell $(SETUP_ENVTEST) use $(ENVTEST_K8S_VERSION) -p path)" \
	CLUSTARR_PG_ASSETS="$(PG_ASSETS)" \
	$(NATIVE_ENV) \
	go test ./... -race -p $(TEST_PARALLEL) -timeout 60m

# -timeout 20m: the transcode envtests took some 460 s alone and passed
# go's 10m default under the whole suite's load (2026-10-06).
test: envtest pg-assets native-assets chart-deps fork-status ## Run unit and envtest suites; a missing tool fails.
	@mkdir -p "$${CLUSTARR_TEST_MEDIA_ROOT:-/data/media}" 2>/dev/null || echo "warning: could not create $${CLUSTARR_TEST_MEDIA_ROOT:-/data/media}; the import scan suites will skip"
	KUBEBUILDER_ASSETS="$(shell $(SETUP_ENVTEST) use $(ENVTEST_K8S_VERSION) -p path)" \
	CLUSTARR_PG_ASSETS="$(PG_ASSETS)" \
	$(NATIVE_ENV) \
	go test ./... -p $(TEST_PARALLEL) -timeout 20m -coverprofile cover.out

.PHONY: test-unit
test-unit: ## Run unit tests only (no envtest, no pg-assets).
	go test -short ./...

.PHONY: envtest
envtest: ## Download envtest binaries.
	$(SETUP_ENVTEST) use $(ENVTEST_K8S_VERSION) -p path >/dev/null

.PHONY: pg-assets
pg-assets: ## Download the embedded Postgres binary for pkg/relindex's Postgres tests.
	go run ./hack/pgassets $(PG_ASSETS)

.PHONY: native-assets
native-assets: ## FFmpeg 9 shared libs + libffshim, ONNX Runtime, libpar2shim and the test-only CLIs (hack/native-assets.sh).
	hack/native-assets.sh $(NATIVE_ASSETS)

# A fresh clone or worktree has no charts/clustarr/charts (gitignored), and
# helm template refuses a chart whose dependencies are missing, which reads
# as a chart/kustomize disagreement rather than a missing artifact.
.PHONY: chart-deps
chart-deps: ## Vendor the chart's nats and cloudnative-pg archives when charts/clustarr/charts lacks them.
	@ls charts/clustarr/charts/nats-*.tgz charts/clustarr/charts/cloudnative-pg-*.tgz >/dev/null 2>&1 || { \
	  helm repo add nats https://nats-io.github.io/k8s/helm/charts/ --force-update && \
	  helm repo add cloudnative-pg https://cloudnative-pg.github.io/charts --force-update && \
	  helm dependency build charts/clustarr; }

# Extra arguments appended to the e2e `go test` invocation, e.g.
#   make e2e E2E_ARGS="-run TestLibraryRescan -v"
# hack/e2e.sh forwards its own E2E_ARGS through here rather than restating the
# go test line, so the script and this target cannot drift apart.
E2E_ARGS ?=

.PHONY: e2e
e2e: ## Run kind-based end-to-end tests. E2E_ARGS="-run ..." appends go test flags.
	go test ./test/e2e/... -tags e2e -timeout 60m $(E2E_ARGS)

##@ Deploy

.PHONY: install
install: manifests ## Install CRDs into the current cluster.
	$(KUSTOMIZE) build config/crd | kubectl apply --server-side -f -

.PHONY: uninstall
uninstall: manifests ## Remove CRDs from the current cluster.
	$(KUSTOMIZE) build config/crd | kubectl delete --ignore-not-found -f -

.PHONY: deploy
deploy: manifests ## Deploy controllers into the current cluster.
	$(KUSTOMIZE) build config/default | kubectl apply --server-side -f -

.PHONY: kind-up
kind-up: ## Create a local kind cluster with NATS.
	hack/kind.sh up

.PHONY: kind-load
kind-load: ## Load the clustarr and native images into kind, skipping any the node already holds.
	IMG=$(IMG) NATIVE_IMG=$(NATIVE_IMG) hack/kind.sh load

.PHONY: run-dev
run-dev: build native-assets ## Run the manager, six agents and the ui locally against the current kube context (hack/dev-run.sh, spec §3.10).
	NATIVE_ASSETS=$(NATIVE_ASSETS) NATIVE_IMG=$(NATIVE_IMG) hack/dev-run.sh

.PHONY: kind-down
kind-down:
	hack/kind.sh down

.PHONY: help
help:
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)
