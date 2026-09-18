# Clustarr build tooling. Targets follow kubebuilder conventions.

GOBIN ?= $(shell go env GOPATH)/bin
CONTROLLER_GEN ?= $(GOBIN)/controller-gen
SETUP_ENVTEST ?= $(GOBIN)/setup-envtest
KUSTOMIZE ?= $(GOBIN)/kustomize
GOLANGCI_LINT ?= $(GOBIN)/golangci-lint-v2
ENVTEST_K8S_VERSION ?= 1.37.0
IMG ?= ghcr.io/mediactl/clustarr:dev
MEDIA_IMG ?= ghcr.io/mediactl/clustarr-media:dev

API_PATHS := ./api/...
CRD_DIR := config/crd/bases

.PHONY: all
all: generate manifests build

##@ Development

.PHONY: generate
generate: ## Generate DeepCopy and apply-configuration code.
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="$(API_PATHS)"
	$(CONTROLLER_GEN) applyconfiguration:headerFile="hack/boilerplate.go.txt" paths="$(API_PATHS)" output:applyconfiguration:dir=./api/applyconfiguration

.PHONY: manifests
manifests: ## Generate CRDs and RBAC.
	$(CONTROLLER_GEN) crd paths="$(API_PATHS)" output:crd:artifacts:config=$(CRD_DIR)
	$(CONTROLLER_GEN) rbac:roleName=clustarr-manager paths="./catalogarr/... ./indexarr/... ./grabarr/... ./squasharr/... ./captionarr/..." output:rbac:artifacts:config=config/rbac

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

.PHONY: build
build: ## Build the clustarr binary.
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X github.com/mediactl/clustarr/pkg/version.Version=$(shell git describe --tags --always --dirty 2>/dev/null || echo dev)" -o bin/clustarr ./cmd/clustarr

.PHONY: docker-build
docker-build: ## Build controller and media images.
	docker build -f images/Dockerfile.controller -t $(IMG) .
	docker build -f images/Dockerfile.media -t $(MEDIA_IMG) .

##@ Test

.PHONY: test
test: envtest ## Run unit and envtest suites.
	KUBEBUILDER_ASSETS="$(shell $(SETUP_ENVTEST) use $(ENVTEST_K8S_VERSION) -p path)" go test ./... -coverprofile cover.out

.PHONY: test-unit
test-unit: ## Run unit tests only (no envtest).
	go test -short ./...

.PHONY: envtest
envtest: ## Download envtest binaries.
	$(SETUP_ENVTEST) use $(ENVTEST_K8S_VERSION) -p path >/dev/null

.PHONY: e2e
e2e: ## Run kind-based end-to-end tests.
	go test ./test/e2e/... -tags e2e -timeout 30m

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

.PHONY: kind-down
kind-down:
	hack/kind.sh down

.PHONY: help
help:
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)
