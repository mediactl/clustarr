# Clustarr research note: controller-runtime v0.25 / kubebuilder v4 conventions (2026-09)

Everything marked **verified** below was checked on this machine with `go doc`, `go list -m -versions`,
`controller-gen v0.22.0`, an actual `envtest` (kube-apiserver 1.37.0) run, or by reading the
kubebuilder `test/data/project-v4` scaffold at `master`. Sample code in section 13 compiles against
`sigs.k8s.io/controller-runtime v0.25.1` + `k8s.io/* v0.37.0` on Go 1.27 and the integration test passes.
Probe project lives at `<scratchpad>/k8sprobe` (not in the repo).

## 0. TL;DR for the architects

1. **Toolchain (verified):** Go 1.27, `sigs.k8s.io/controller-runtime v0.25.1`, `sigs.k8s.io/controller-tools v0.22.0`
   (`controller-gen`), `k8s.io/{api,apimachinery,client-go} v0.37.0`, kubebuilder CLI `v4.16.0` (released 2026-09-10; scaffolds
   controller-runtime v0.24.1 / controller-tools v0.22.0 / Go 1.26 — bump go.mod to v0.25.1 after `kubebuilder init`),
   kustomize v5.8.1, golangci-lint v2.13.x, envtest assets `1.37.0` (setup-envtest index has 1.37.0 for linux/amd64).
2. **Layout:** use the kubebuilder go/v4 layout in **multi-group** mode (`kubebuilder init --multigroup`), one Go module, one
   API group per service (`api/<group>/v1alpha1`), controllers in `internal/controller/<group>/`, and **one image with cobra
   subcommands** (`clustarr transcodarr|downloadarr|indexarr|inventorri|subtitlarr|all`). Each subcommand builds its own
   `ctrl.Manager` with its own `LeaderElectionID`; `all` runs every controller in a single manager for the small-cluster case.
3. **CRDs:** `+kubebuilder:object:root=true`, `+kubebuilder:subresource:status`, `+kubebuilder:resource:scope=...,shortName=...,categories=clustarr`,
   `+kubebuilder:printcolumn`, `+kubebuilder:default`, `+required`/`+optional` (the long forms are deprecated in controller-tools v0.22),
   CEL via `+kubebuilder:validation:XValidation:rule=...,message=...` (field-level `self == oldSelf` immutability and type-level
   cross-field rules both verified against the 1.37 apiserver). Conditions are `[]metav1.Condition` with `+listType=map`/`+listMapKey=type`.
4. **Controller pattern:** `client.Object` reconcile, finalizer named `<group>/<kind>` via `controllerutil.AddFinalizer`, children with
   `controllerutil.SetControllerReference` + `Owns()`, cross-object fan-out with a **field index + `Watches(..., handler.EnqueueRequestsFromMapFunc)`**,
   status written once per reconcile with `Status().Patch(MergeFromWithOptimisticLock)` (or SSA via generated apply-configurations),
   `RequeueAfter` (never `Requeue: true`, it is deprecated), `reconcile.TerminalError` for non-retryable failures. **Priority queue is
   on by default in v0.25** and `reconcile.Result.Priority` exists.
5. **Batch workers:** the controller creates `batch/v1` Jobs with ownerRefs, `ttlSecondsAfterFinished`, `backoffLimit`, `podFailurePolicy`
   (ignore `DisruptionTarget`, fail fast on ffmpeg usage exit codes), `podReplacementPolicy: Failed`, and translates `spec.hardware` into
   `nvidia.com/gpu` + `runtimeClassName: nvidia` + `NVIDIA_DRIVER_CAPABILITIES=video,compute,utility`, or `gpu.intel.com/i915|xe`, plus
   required node affinity on the device-plugin/NFD labels. Use **KEDA ScaledJob + `nats-jetstream` scaler** only for the *stateless* worker
   pools driven purely by a JetStream consumer (lag = `num_pending + num_ack_pending`); do **not** import KEDA's Go module (it pins k8s v0.35 / controller-runtime v0.23.3).
6. **Testing:** envtest via `setup-envtest use 1.37.x -p path` (Makefile derives `ENVTEST_K8S_VERSION` from `k8s.io/api` in go.mod), plain
   `testing` or Ginkgo v2.33/Gomega v1.43. Gotcha verified: Kubernetes 1.37 rejects a Job status `Complete=True` unless `SuccessCriteriaMet=True`
   and `startTime` are set (matters for fakes/simulations).
7. **Packaging:** kustomize `config/` is the source of truth; generate the Helm chart with `kubebuilder edit --plugins=helm/v2-alpha`
   (CRDs land in `templates/crd/` so they upgrade; `crd.keep=true` adds `helm.sh/resource-policy: keep`). Dev box has **Helm v4.2.2**, kind v0.30.0, kubectl 1.36.4.

---

## 1. Verified versions and environment

| Thing | Verified value | How |
|---|---|---|
| Go | `go1.27.0` | `go version` |
| sigs.k8s.io/controller-runtime | **v0.25.1** (v0.25.0 = k8s 1.37; v0.25.1 fixes priorityqueue data race + subresource create RV parse under read-your-writes) | `go list -m -versions`, GitHub releases |
| sigs.k8s.io/controller-tools | **v0.22.0** (k8s v0.37; deprecates `+kubebuilder:validation:Required/Optional` for `+required/+optional`; adds `k8s:listType`/`k8s:listMapKey`; envtest 1.36.2 and 1.37.0 published) | `go list`, releases page, `controller-gen --version` |
| k8s.io/api, apimachinery, client-go | **v0.37.0** (v0.38.0-alpha.0 exists; ignore) | `go list -m -versions` |
| k8s.io/utils | pseudo-version `v0.0.0-20260707023825-cf1189d6abe3` (no tags) | `go get k8s.io/utils@latest` |
| kubebuilder CLI | **v4.16.0**, published 2026-09-10 (`sigs.k8s.io/kubebuilder/v4`); not installed locally (`go install sigs.k8s.io/kubebuilder/v4@v4.16.0`) | GitHub API, `go list` |
| kustomize | v5.8.1 installed locally; Makefile pins `KUSTOMIZE_VERSION ?= v5.8.1` | `kustomize version` |
| golangci-lint | v2.13.2 installed; scaffold pins `v2.13.1` | `golangci-lint --version` |
| envtest assets | 1.37.0 (linux/amd64) installed at `~/.local/share/kubebuilder-envtest/k8s/1.37.0-linux-amd64` (`etcd`, `kube-apiserver`, `kubectl`) | `setup-envtest list` |
| github.com/spf13/cobra | v1.10.2 | `go list` |
| github.com/prometheus/client_golang | v1.24.1 (already an indirect dep of controller-runtime) | `go list` |
| github.com/onsi/ginkgo/v2 / gomega | v2.33.0 / v1.43.1 | `go list` |
| github.com/kedacore/keda/v2 | v2.20.2 — **its go.mod replaces k8s.io/api => v0.35.5 and controller-runtime => v0.23.3** | `go get` + read module go.mod |
| cert-manager | v1.21.2 (only needed if you add admission webhooks) | `go list` |
| helm.sh/helm/v3 module | v3.22.0; local `helm` binary is **v4.2.2** | `go list`, `helm version` |
| kind / kubectl / docker | kind v0.30.0, kubectl client v1.36.4, docker present | CLI |
| ffmpeg | 9.0.1 + ffprobe (given) | env facts |

controller-runtime v0.25.0 highlights (from the release page): k8s.io/* v1.37; experimental **read-your-writes** cache-backed client
(`Client.Cache.EnableReadYourWritesConsistency`); **new `events.k8s.io` EventRecorder** (`mgr.GetEventRecorder(name)`; `GetEventRecorderFor` is deprecated);
opt-in **client-go REST client metrics** (`metrics.RegisterRESTClientMetrics()`); `source.TypedInformer`; **webhook server can be disabled with `Port: -1`**;
fake client scale-subresource Apply support. v0.24 brought k8s 1.36, typed ApplyConfiguration round-trips, base-context wiring for HTTP servers.

`manager.Options`, `controller.TypedOptions`, `config.Controller`, `cache.Options`, `client.Options`, `client.CacheOptions`, `metricsserver.Options`,
`webhook.Options`, `envtest.Environment` field lists were dumped with `go doc` and are reproduced where relevant below.

---

## 2. Kubebuilder v4 project layout (go/v4 plugin, verified from `test/data/project-v4`)

`kubebuilder init --domain clustarr.io --repo github.com/<org>/clustarr --multigroup` scaffolds:

```
PROJECT                      # plugin bookkeeping: layout: [go.kubebuilder.io/v4], multigroup: true, resources[]
go.mod / go.sum
Makefile                     # see section 3
Dockerfile                   # golang:1.26 builder -> gcr.io/distroless/static:nonroot, USER 65532:65532, ENTRYPOINT ["/manager"]
.golangci.yml                # v2 config; linters: copyloopvar depguard dupl errcheck ginkgolinter goconst gocyclo govet ineffassign lll modernize misspell nakedret prealloc revive staticcheck unconvert unparam unused logcheck(custom)
.gitignore .dockerignore README.md
.devcontainer/  .github/workflows/{test,lint,e2e-test}.yml
cmd/main.go                  # the manager binary (section 6)
hack/boilerplate.go.txt
api/<group>/<version>/       # multigroup: groupversion_info.go, <kind>_types.go, zz_generated.deepcopy.go
internal/controller/<group>/ # <kind>_controller.go, suite_test.go (envtest), <kind>_controller_test.go
internal/webhook/<group>/<version>/   # only if you add webhooks (--legacy location flag was removed in v4.16)
config/
  crd/{kustomization.yaml,kustomizeconfig.yaml,bases/<group>_<plural>.yaml,patches/}
  rbac/{kustomization.yaml,service_account.yaml,role.yaml(generated),role_binding.yaml,
        leader_election_role.yaml,leader_election_role_binding.yaml,
        metrics_auth_role.yaml,metrics_auth_role_binding.yaml,metrics_reader_role.yaml,
        <kind>_{admin,editor,viewer}_role.yaml}
  manager/{kustomization.yaml,manager.yaml}         # Namespace + Deployment
  default/{kustomization.yaml,manager_metrics_patch.yaml,metrics_service.yaml,
           cert_metrics_manager_patch.yaml,manager_webhook_patch.yaml}
  prometheus/monitor.yaml                            # ServiceMonitor (commented out in default/kustomization)
  network-policy/allow-metrics-traffic.yaml          # NetworkPolicy (commented out by default)
  certmanager/ webhook/                              # only with webhooks
  samples/kustomization.yaml + <group>_<version>_<kind>.yaml
test/e2e/{e2e_suite_test.go,e2e_test.go}            # kind-based, `go test -tags=e2e`
test/utils/utils.go
dist/install.yaml                                    # from `make build-installer`
```

`kubebuilder create api --group transcode --version v1alpha1 --kind TranscodeJob --resource --controller` adds
`api/transcode/v1alpha1/transcodejob_types.go`, `internal/controller/transcode/transcodejob_controller.go`, updates `config/crd/kustomization.yaml`,
`config/rbac/kustomization.yaml` (+ admin/editor/viewer roles), `config/samples`, `cmd/main.go` registration, and the `PROJECT` file.
v4.14 added `--namespaced` for namespace-scoped managers and support for multiple controllers per GVK; v4.16 renamed webhook types to
`<Kind>Defaulter`/`<Kind>Validator` and fixed the webhook NetworkPolicy to port 9443.

Scaffolded Deployment (`config/manager/manager.yaml`, verified): `replicas: 1`, `securityContext.runAsNonRoot: true`, `seccompProfile: RuntimeDefault`,
container `command: [/manager]`, `args: [--leader-elect, --health-probe-bind-address=:8081]`, `readOnlyRootFilesystem: true`, `allowPrivilegeEscalation: false`,
`capabilities.drop: [ALL]`, liveness `GET /healthz:8081` (15s/20s), readiness `GET /readyz:8081` (5s/10s), resources `requests 10m/64Mi, limits 500m/128Mi`,
`terminationGracePeriodSeconds: 10`, `serviceAccountName: controller-manager`. `config/default` adds `--metrics-bind-address=:8443` via
`manager_metrics_patch.yaml` and a `controller-manager-metrics-service` on `https:8443`. `config/default/kustomization.yaml` = `namespace: <project>-system`,
`namePrefix: <project>-`, `resources: [../crd, ../rbac, ../manager, (../webhook), (../certmanager), (../prometheus), (../network-policy), metrics_service.yaml]`
plus commented `replacements:` that wire cert-manager Certificate DNS names into webhook configs and CRD conversion.

### Proposed Clustarr tree (kubebuilder-compatible, brief-compatible)

```
clustarr/
  PROJECT  Makefile  Dockerfile  go.mod
  cmd/clustarr/main.go            # cobra root; subcommands below build managers (section 7)
  api/
    inventory/v1alpha1/           # Movie, Series, Album, Book, RootFolder, QualityProfile, ImportList ...
    download/v1alpha1/            # DownloadClient, Download(Task)
    indexer/v1alpha1/             # Indexer, IndexerDefinition, SearchRequest(?)
    transcode/v1alpha1/           # TranscodeProfile (cluster), TranscodeJob (ns)
    subtitle/v1alpha1/            # SubtitleProfile, SubtitleJob
  internal/
    controller/{inventory,download,indexer,transcode,subtitle}/
    manager/                      # shared: build-a-manager helper (flags, metrics, probes, LE id)
    downloadarr/ app/indexer/ inventorri/ transcodarr/ subtitlarr/   # service internals (non-k8s logic)
  pkg/                            # importable shared libs: quality, metadata clients, nats, ffmpeg, release parsing
  config/{crd,rbac,manager,default,prometheus,network-policy,samples}
  charts/clustarr/                # generated by helm/v2-alpha into dist/chart, then moved/curated
  test/e2e/
```

Keep the brief's top-level names as *cobra subcommands and internal packages*, not as separate Go modules; controller-gen `paths="./..."`
then sees all groups, RBAC is aggregated into one `manager-role`, and the Dockerfile builds one static binary.

---

## 3. Makefile (verified verbatim from kubebuilder v4.16 scaffold) and controller-gen invocations

Key variables:

```make
CONTROLLER_TOOLS_VERSION ?= v0.22.0
KUSTOMIZE_VERSION ?= v5.8.1
GOLANGCI_LINT_VERSION ?= v2.13.1
ENVTEST_VERSION ?= $(shell v='$(call gomodver,sigs.k8s.io/controller-runtime)'; [ -n "$$v" ] || { echo "Set ENVTEST_VERSION manually" >&2; exit 1; }; printf '%s\n' "$$v")
ENVTEST_K8S_VERSION ?= $(shell v='$(call gomodver,k8s.io/api)'; [ -n "$$v" ] || { ...; exit 1; }; printf '%s\n' "$$v" | sed -E 's/^v?[0-9]+\.([0-9]+).*/1.\1/')   # -> "1.37"
define gomodver
$(shell go list -m -f '{{if .Replace}}{{.Replace.Version}}{{else}}{{.Version}}{{end}}' $(1) 2>/dev/null)
endef
LOCALBIN ?= $(shell pwd)/bin
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen ; ENVTEST ?= $(LOCALBIN)/setup-envtest ; KUSTOMIZE ?= $(LOCALBIN)/kustomize ; GOLANGCI_LINT = $(LOCALBIN)/golangci-lint
```

Targets and exact recipes:

```make
manifests: controller-gen   ## CRDs, RBAC, webhooks, apply configurations
	"$(CONTROLLER_GEN)" rbac:roleName=manager-role crd webhook applyconfiguration:headerFile="hack/boilerplate.go.txt" paths="./..." output:crd:artifacts:config=config/crd/bases
generate: controller-gen    ## deepcopy
	"$(CONTROLLER_GEN)" object:headerFile="hack/boilerplate.go.txt",year=$(YEAR) paths="./..."
fmt: go fmt ./...      vet: go vet ./...
test: manifests generate fmt vet setup-envtest
	KUBEBUILDER_ASSETS="$(shell "$(ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path)" go test $$(go list ./... | grep -v /e2e) -coverprofile cover.out
setup-envtest: envtest
	@"$(ENVTEST)" use $(ENVTEST_K8S_VERSION) --bin-dir "$(LOCALBIN)" -p path || { echo "Error: ..."; exit 1; }
test-e2e: setup-test-e2e manifests generate fmt vet      # kind cluster "$(KIND_CLUSTER)"
	KIND=$(KIND) KIND_CLUSTER=$(KIND_CLUSTER) go test -tags=e2e ./test/e2e/ -v -ginkgo.v ; $(MAKE) cleanup-test-e2e
lint / lint-fix / lint-config:  "$(GOLANGCI_LINT)" run | run --fix | config verify
build: manifests generate fmt vet ; go build -o bin/manager cmd/main.go
run:   manifests generate fmt vet ; go run ./cmd/main.go
docker-build: $(CONTAINER_TOOL) build $(if $(BASE_IMAGE),--build-arg BASE_IMAGE=$(BASE_IMAGE)) -t ${IMG} .
docker-buildx: buildx create/use/build --push --platform=$(PLATFORMS) -f Dockerfile.cross   # PLATFORMS ?= linux/arm64,linux/amd64,linux/s390x,linux/ppc64le
build-installer: manifests generate kustomize
	mkdir -p dist ; cd config/manager && "$(KUSTOMIZE)" edit set image controller=${IMG} ; "$(KUSTOMIZE)" build config/default > dist/install.yaml
install:   "$(KUSTOMIZE)" build config/crd | "$(KUBECTL)" apply -f -        (skips when no CRDs)
uninstall: "$(KUSTOMIZE)" build config/crd | "$(KUBECTL)" delete --ignore-not-found=$(ignore-not-found) -f -
deploy:    cd config/manager && "$(KUSTOMIZE)" edit set image controller=${IMG} ; "$(KUSTOMIZE)" build config/default | "$(KUBECTL)" apply -f -
undeploy:  "$(KUSTOMIZE)" build config/default | "$(KUBECTL)" delete --ignore-not-found=$(ignore-not-found) -f -
kustomize / controller-gen / envtest / golangci-lint:  $(call go-install-tool,<bin>,<module path>,<version>)   # installs to LOCALBIN as <bin>-<version> + symlink
#   modules: sigs.k8s.io/kustomize/kustomize/v5, sigs.k8s.io/controller-tools/cmd/controller-gen,
#            sigs.k8s.io/controller-runtime/tools/setup-envtest, github.com/golangci/golangci-lint/v2/cmd/golangci-lint
```

`controller-gen` generator/flag grammar (from `controller-gen -h`, v0.22.0):

```
controller-gen object[:headerFile=<f>][,year=<y>] paths=./api/...
controller-gen crd[:allowDangerousTypes=<bool>][,crdVersions=<[]string>][,generateEmbeddedObjectMeta=<bool>][,ignoreUnexportedFields=<bool>][,maxDescLen=<int>] paths=./... output:crd:artifacts:config=config/crd/bases
controller-gen rbac:roleName=manager-role[,fileName=<f>] paths=./...  output:rbac:artifacts:config=config/rbac
controller-gen webhook paths=./...
controller-gen applyconfiguration[:externalApplyConfigurations=<[]string>][,headerFile=<f>] paths=./api/...
controller-gen schemapatch:manifests=./manifests paths=./api/...   # patch existing CRDs
output rules: output:stdout | output:none | output:dir=<d> | output:artifacts[:code=<d>],config=<d> | output:<generator>:...
markers help: controller-gen crd -w / -ww / -www / -wwww (json)
```

Verified quirks:
* `applyconfiguration` generates nothing unless the API package carries `// +kubebuilder:ac:generate=true` (or per-type). Output goes to
  `api/<ver>/applyconfiguration/{api/<ver>/*.go,internal/internal.go,utils.go}` (override with `+kubebuilder:ac:output:package=`).
  The generated `utils.go` references **`v1alpha1.SchemeGroupVersion`**, which the kubebuilder scaffold does not define (it defines `GroupVersion`);
  add `var SchemeGroupVersion = GroupVersion` in `groupversion_info.go` or the package will not compile.
* Generated code uses `k8s.io/client-go/applyconfigurations/meta/v1` builders for conditions (`metav1ac.Condition().WithType(...)`), not `metav1.Condition`.

---

## 4. CRD authoring: markers that matter (all names verified with `controller-gen crd -w`)

```go
// package-level (groupversion_info.go)
// +kubebuilder:object:generate=true
// +kubebuilder:ac:generate=true            // apply configurations (SSA)
// +groupName=transcode.clustarr.io

// type-level
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:subresource:scale:specpath=.spec.replicas,statuspath=.status.replicas[,selectorpath=.status.selector]
// +kubebuilder:resource:scope=Namespaced|Cluster,shortName=tj,categories=clustarr[,path=transcodejobs,singular=transcodejob]
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`[,priority=1][,format=date-time][,description=...]
// +kubebuilder:storageversion  +kubebuilder:unservedversion  +kubebuilder:deprecatedversion:warning="..."  +kubebuilder:skipversion
// +kubebuilder:selectablefield:JSONPath=`.spec.profileRef.name`      // enables field selectors on CRs (k8s >=1.31)
// +kubebuilder:metadata:labels="app.kubernetes.io/part-of=clustarr",annotations="..."
// +kubebuilder:validation:XValidation:rule="...",message="...",[messageExpression=...,fieldPath=...,reason=FieldValueForbidden|...,optionalOldSelf=true]
// +kubebuilder:validation:ExactlyOneOf=a;b   +kubebuilder:validation:AtMostOneOf=a;b   +kubebuilder:validation:AtLeastOneOf=a;b   (type-level, emit CEL)

// field-level
// +required / +optional                     // preferred; +kubebuilder:validation:Required|Optional deprecated in v0.22
// +kubebuilder:default=50   +kubebuilder:example=...   +kubebuilder:title=...
// +kubebuilder:validation:Enum=a;b;c   Minimum/Maximum/ExclusiveMinimum/ExclusiveMaximum/MultipleOf
// +kubebuilder:validation:MinLength/MaxLength/Pattern/Format   MinItems/MaxItems/UniqueItems   MinProperties/MaxProperties
// +kubebuilder:validation:Type=string   +kubebuilder:validation:Schemaless   +kubebuilder:validation:EmbeddedResource
// +kubebuilder:validation:XIntOrString   +kubebuilder:validation:XPreserveUnknownFields (or +kubebuilder:pruning:PreserveUnknownFields)
// +kubebuilder:validation:items:<any of the above>   // applies to array items
// +listType=map +listMapKey=type +patchStrategy=merge +patchMergeKey=type   // for []metav1.Condition (SSA-friendly)
// +mapType=atomic|granular   +structType=atomic|granular
// +k8s:enum (type)   +k8s:listType/+k8s:listMapKey   +k8s:required/+k8s:optional/+k8s:immutable (declarative validation markers; controller-tools honours listType/listMapKey/enum)
```

CEL notes (verified against apiserver 1.37):
* Field-level `rule="self == oldSelf"` produces `spec.source: Invalid value: "other.mkv": spec.source is immutable` on update. Transition
  rules (`oldSelf`) are skipped on create automatically.
* Type-level rules see the whole object (`self.spec...`, `has(self.status)`); the error path is `<nil>` unless `fieldPath=` is given.
* CEL cost budget: keep rules over bounded fields; `MaxLength`/`MaxItems` on strings/lists you reference in rules lowers estimated cost.
* Defaults from `+kubebuilder:default` are applied by the apiserver at admission (verified: Priority=50, TTL=3600, BackoffLimit=2 came back on `Create`).
* `resource.Quantity` maps get the standard quantity `pattern` + `x-kubernetes-int-or-string` automatically.

### Status conditions (k8s api-conventions, verified text)

* `Conditions []metav1.Condition` at `.status.conditions`, `+listType=map`, `+listMapKey=type`. Fields: `Type` (PascalCase, adjective or past-tense verb:
  `Ready`, `Succeeded`, `Failed`, never `Deploying`), `Status` (`True|False|Unknown`), `ObservedGeneration`, `LastTransitionTime`, `Reason`
  (one-word CamelCase, machine readable), `Message`.
* Polarity is per-condition; absent == `Unknown`. Controllers should set their conditions on the first visit even if `Unknown`.
* Summary condition: `Ready` for long-running objects, `Succeeded` for bounded work (jobs). Keep `status.observedGeneration` == `metadata.generation` when
  status reflects the latest spec.
* Helpers (apimachinery `meta` package, verified): `meta.SetStatusCondition(&conds, c) (changed bool)`, `FindStatusCondition`, `IsStatusConditionTrue/False`,
  `IsStatusConditionPresentAndEqual`, `RemoveStatusCondition`. `SetStatusCondition` preserves `LastTransitionTime` when status is unchanged.
* `batch/v1` Job conditions to mirror: `Complete`, `Failed`, `Suspended`, `FailureTarget`, `SuccessCriteriaMet`; failure reasons `PodFailurePolicy`,
  `BackoffLimitExceeded`, `DeadlineExceeded`.

---

## 5. Controller pattern (controller-runtime v0.25.1, API surface verified with `go doc`)

```go
type Reconciler = TypedReconciler[Request]          // Reconcile(ctx, reconcile.Request) (Result, error)
type ObjectReconciler[T client.Object] interface { Reconcile(context.Context, T) (Result, error) }   // use with reconcile.AsReconciler(client, r)
type Result struct { Requeue bool /*deprecated*/; RequeueAfter time.Duration; Priority *int /* only with priority queue */ }
func TerminalError(wrapped error) error            // logged + counted, never retried
```

Builder (`sigs.k8s.io/controller-runtime/pkg/builder`):

```go
ctrl.NewControllerManagedBy(mgr).
    Named("transcodejob").                                   // required when two controllers share a For() type
    For(&v1alpha1.TranscodeJob{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
    Owns(&batchv1.Job{}[, builder.MatchEveryOwner]).         // handler.EnqueueRequestForOwner(..., handler.OnlyControllerOwner()) by default
    Watches(&v1alpha1.TranscodeProfile{}, handler.EnqueueRequestsFromMapFunc(mapFn)[, builder.WithPredicates(...)]).
    WatchesMetadata(&corev1.Secret{}, h)                      // PartialObjectMetadata informer (cheap)
    WatchesRawSource(source.Channel(ch, h, source.WithBufferSize[...](1024)))   // external events (NATS!) -> GenericEvent
    WithEventFilter(p).WithLogConstructor(f).
    WithOptions(controller.Options{MaxConcurrentReconciles: 4, RecoverPanic: ptr.To(true), UsePriorityQueue: ptr.To(true),
                                   ReconciliationTimeout: 2*time.Minute, RateLimiter: ..., NeedLeaderElection: ptr.To(true), EnableWarmup: ptr.To(true)}).
    Complete(r)   // or Build(r) to keep the controller.TypedController handle
```

`controller.TypedOptions` fields (verified): `SkipNameValidation`, `MaxConcurrentReconciles`, `CacheSyncTimeout` (2m default), `RecoverPanic` (default true),
`NeedLeaderElection`, `Reconciler`, `RateLimiter`, `NewQueue`, `Logger`, `LogConstructor`, `UsePriorityQueue` (**enabled by default**), `EnableWarmup`
(beta; starts sources before winning leader election), `ReconciliationTimeout` (context deadline per Reconcile; no default).
Manager-wide defaults live in `config.Controller{GroupKindConcurrency map[string]int /* "TranscodeJob.transcode.clustarr.io": 8 */, MaxConcurrentReconciles, ...}`.

Handlers/predicates/sources (verified): `handler.EnqueueRequestForObject`, `EnqueueRequestForOwner(scheme, mapper, ownerType, OnlyControllerOwner())`,
`EnqueueRequestsFromMapFunc(func(ctx, client.Object) []reconcile.Request)`, `handler.Funcs{CreateFunc,UpdateFunc,DeleteFunc,GenericFunc}`,
`handler.WithLowPriorityWhenUnchanged(h)` (initial-list/resync events get priority -100); `predicate.GenerationChangedPredicate`, `ResourceVersionChangedPredicate`,
`LabelChangedPredicate`, `AnnotationChangedPredicate`, `predicate.Funcs`, `predicate.And/Or/Not`, `predicate.NewPredicateFuncs`; `source.Kind(cache, obj, handler, predicates...)`,
`source.Channel(<-chan event.TypedGenericEvent[T], handler, opts...)`, `source.TypedInformer`, `source.Func`. `event.TypedCreateEvent.IsInInitialList` is available to handlers.

Priority queue (`pkg/controller/priorityqueue`, verified): `PriorityQueue[T]` = `workqueue.TypedRateLimitingInterface[T]` + `AddWithOpts(AddOpts{After, RateLimited, Priority *int}, items...)`
+ `GetWithPriority()`. De-duplicates; keeps max priority / min delay; FIFO within a priority level. To map `spec.priority` onto queue priority, write a custom
`handler.TypedEventHandler` that type-asserts the queue to `priorityqueue.PriorityQueue[reconcile.Request]` and calls `AddWithOpts`; `reconcile.Result.Priority`
only affects requeues.

### Reconcile skeleton rules (all exercised by the envtest run in section 12)

1. `Get` the object; `client.IgnoreNotFound(err)` on miss.
2. If `DeletionTimestamp` set: do external cleanup, `controllerutil.RemoveFinalizer`, `Update`, return. Children are GC'd through ownerRefs.
3. `controllerutil.AddFinalizer` + `Update` **and keep going** — a finalizer add is a metadata-only update that does not bump `metadata.generation`,
   so with `GenerationChangedPredicate` on `For()` there will be no follow-up event; returning early stalls until resync.
4. Do the work idempotently: `Get` child; on NotFound build it, `controllerutil.SetControllerReference(owner, child, scheme)`, `Create` (treat `AlreadyExists` as cache lag and requeue in ~1s).
   Alternatives: `controllerutil.CreateOrUpdate/CreateOrPatch(ctx, c, obj, mutateFn)` or `client.Apply(ctx, applyConfig, client.FieldOwner("x"), client.ForceOwnership)` (SSA, v0.22+).
5. Mirror child state into conditions with `meta.SetStatusCondition`, set `status.observedGeneration = metadata.generation`.
6. Write status **once** at the end: `r.Status().Patch(ctx, obj, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}))`; on `IsConflict` requeue shortly.
   Or SSA: `r.Status().Apply(ctx, ac, client.FieldOwner("transcodarr"), client.ForceOwnership)` with generated apply configurations (compiles, see `ssa_probe.go`).
7. Return `ctrl.Result{}` when a watch will wake you up; `RequeueAfter` for time-based polling of external systems; `err` for retry with backoff
   (default rate limiter = per-item exponential 5ms..1000s + overall 10qps/100 bucket); `reconcile.TerminalError(err)` when retrying cannot help.
8. Cross-object fan-out: register a field index in `SetupWithManager` (`mgr.GetFieldIndexer().IndexField(ctx, &TranscodeJob{}, ".spec.profileRef.name", fn)`)
   and `List(ctx, &l, client.MatchingFields{key: name})` inside the map func. Verified: creating the profile after the job re-enqueued the job.
9. Events: `mgr.GetEventRecorder("transcodejob-controller")` returns the **new** `recorder.EventRecorder` (`events.k8s.io/v1`; `Eventf(regarding, related runtime.Object, type, reason, action, note, args...)`).
   Needs RBAC `events.k8s.io/events: create,patch`; `GetEventRecorderFor` (core/v1 events) is deprecated. The name becomes `reportingController` and must be a qualified name; `len(name)+1+len(hostname) <= 128`.

### Cache / client scoping (verified fields)

* `cache.Options{DefaultNamespaces: map[string]cache.Config{"media": {}}, DefaultLabelSelector, DefaultFieldSelector, DefaultTransform, ByObject: map[client.Object]cache.ByObject{...},
  SyncPeriod, ReaderFailOnMissingInformer, DefaultUnsafeDisableDeepCopy, DefaultEnableWatchBookmarks}` — use `ByObject` to restrict Secrets to a label selector
  and `cache.TransformStripManagedFields()` to cut memory.
* `client.Options{Cache: &client.CacheOptions{DisableFor: []client.Object{&corev1.Secret{}}, Unstructured: false, EnableReadYourWritesConsistency: ptr.To(true) /* experimental */}, FieldOwner: "transcodarr", FieldValidation: "Strict", DryRun}`.
* `mgr.GetAPIReader()` bypasses the cache for one-off strongly consistent reads (e.g. Secrets, or right after Create).

---

## 6. Manager: leader election, metrics, probes, webhooks, logging (verified against `manager.Options`)

```go
mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
    Scheme:                        scheme,
    Metrics:                       metricsserver.Options{BindAddress: ":8443", SecureServing: true, FilterProvider: filters.WithAuthenticationAndAuthorization, TLSOpts: tlsOpts, CertDir/CertName/KeyName: ..., ExtraHandlers: map[string]http.Handler{}},
    HealthProbeBindAddress:        ":8081",                    // ReadinessEndpointName "readyz", LivenessEndpointName "healthz"
    PprofBindAddress:              "",                         // enable per-service behind auth only
    LeaderElection:                true,
    LeaderElectionID:              "transcodarr.clustarr.io",  // one Lease per service; LeaderElectionResourceLock defaults to "leases"
    LeaderElectionNamespace:       "",                         // defaults to pod namespace (in-cluster) — set when running out-of-cluster
    LeaderElectionReleaseOnCancel: true,                       // fast hand-off; only safe if the process exits right after Start returns
    LeaseDuration/RenewDeadline/RetryPeriod: 15s/10s/2s defaults,
    Cache: cache.Options{...}, Client: client.Options{...},
    WebhookServer: webhook.NewServer(webhook.Options{Port: -1}),   // -1 disables the webhook server (v0.25); default 9443, CertDir /tmp/k8s-webhook-server/serving-certs
    Controller: config.Controller{RecoverPanic: ptr.To(true), UsePriorityQueue: ptr.To(true), EnableWarmup: ptr.To(true), GroupKindConcurrency: map[string]int{"TranscodeJob.transcode.clustarr.io": 8}},
    GracefulShutdownTimeout: ptr.To(30*time.Second),
    BaseContext: func() context.Context { ... },
})
mgr.AddHealthzCheck("healthz", healthz.Ping); mgr.AddReadyzCheck("readyz", healthz.Ping)   // add a NATS/JetStream ping for readiness
mgr.AddMetricsServerExtraHandler("/debug/queues", h)
mgr.Add(manager.RunnableFunc(func(ctx context.Context) error { ... }))   // implement LeaderElectionRunnable{NeedLeaderElection() bool} to run on every replica (e.g. NATS consumers)
<-mgr.Elected()
mgr.Start(ctrl.SetupSignalHandler())
```

Metrics: everything goes through `metrics.Registry` (a `prometheus.Registry`; `metrics.Registry.MustRegister(myCounterVec)` in `init()`), served at `/metrics`.
Built-in series (verified names): `controller_runtime_reconcile_total{controller,result}`, `controller_runtime_reconcile_errors_total`, `controller_runtime_terminal_reconcile_errors_total`,
`controller_runtime_reconcile_panics_total`, `controller_runtime_reconcile_timeouts_total`, `controller_runtime_reconcile_time_seconds`, `controller_runtime_max_concurrent_reconciles`,
`controller_runtime_active_workers`, `leader_election_master_status`, `leader_election_slowpath_total`, workqueue `workqueue_depth/adds_total/queue_duration_seconds/work_duration_seconds/unfinished_work_seconds/longest_running_processor_seconds/retries_total`.
client-go REST metrics are **opt-in** in v0.25: `metrics.RegisterRESTClientMetrics()` or `RegisterRESTClientMetricsWithOptions(metrics.RESTClientMetricsOptions{DurationBuckets: ...}, ...)`.
Secure metrics require the scaffolded `metrics_auth_role` (`authentication.k8s.io/tokenreviews create`, `authorization.k8s.io/subjectaccessreviews create`) and the Prometheus `ServiceMonitor`
uses `scheme: https`, `bearerTokenFile: /var/run/secrets/kubernetes.io/serviceaccount/token`, `tlsConfig.insecureSkipVerify: true` (or `cert_metrics_manager_patch.yaml` + cert-manager).

Logging: `zap.Options{Development: true}; opts.BindFlags(flag.CommandLine); ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))` adds `--zap-devel --zap-encoder --zap-log-level --zap-stacktrace-level --zap-time-encoding`;
inside reconcilers use `logf.FromContext(ctx)` (`sigs.k8s.io/controller-runtime/pkg/log`). Scaffold's HTTP/2 default is **off** (`--enable-http2=false` sets `tls.Config.NextProtos = ["http/1.1"]`).

RBAC for leader election (scaffold `leader_election_role.yaml`, verified): `coordination.k8s.io/leases` full CRUD, `""/configmaps` full CRUD (legacy), `""/events create,patch`.
Add `events.k8s.io/events create,patch` for the new recorder.

---

## 7. One binary vs many, one manager vs many

Options considered:

| Option | Pros | Cons |
|---|---|---|
| A. One image, one process, one manager, all controllers | simplest ops; one Lease; kubebuilder default | one crash/leak kills every service; all services scale as one; RBAC is a superset |
| B. One image, **cobra subcommands**, one manager per subcommand (`clustarr transcodarr`, `clustarr all`) | single build/release; per-service Deployments, replicas, RBAC, LE lease, resource limits; `all` mode for homelabs | slightly larger image; must keep scheme registration per subcommand |
| C. One image per service (separate `cmd/<svc>/main.go`) | strict isolation; cluster-api style | 5x CI/CD/artifacts; shared-code drift is not an issue in a monorepo but release coordination is |
| D. Multiple managers in one process | none over B | two Leases, two caches, two metrics registries to reconcile; no upside |

**Recommendation: B.** Kueue (single binary, `--config` ComponentConfig, `SetupControllers` per group) and the kubebuilder scaffold both show
"many controllers in one manager" is the norm; cobra (`v1.10.2`) gives per-service processes from the same image. Each subcommand:
`internal/manager.New(opts)` -> registers only its group's scheme + controllers -> `LeaderElectionID: "<svc>.clustarr.io"`. Keep a *shared* `pkg/clustarrapi`
scheme builder so `all` can register every group. Per-service RBAC: run `controller-gen rbac:roleName=<svc>-role paths=./internal/controller/<svc>/...` per service
in addition to the aggregate `manager-role` (the rbac generator accepts `roleName=` per marker in v0.21+, so markers can also carry `roleName=transcodarr`).
Non-leader work (NATS consumers, HTTP APIs) is added with `mgr.Add(r)` where `r` implements `NeedLeaderElection() bool { return false }`.

ComponentConfig: controller-runtime's `pkg/config` file loading was removed; kueue-style `--config` files are hand-rolled (`sigs.k8s.io/yaml` v1.6.0 into a versioned struct). Prefer flags + env for v1.

---

## 8. Testing with envtest (verified run)

* `sigs.k8s.io/controller-runtime/tools/setup-envtest@release-0.25` (installed here): `setup-envtest use 1.37.x -p path` -> `~/.local/share/kubebuilder-envtest/k8s/1.37.0-linux-amd64`;
  `--bin-dir`, `--index` (default `https://raw.githubusercontent.com/kubernetes-sigs/controller-tools/HEAD/envtest-releases.yaml`), `list`, `cleanup <1.36`, `-i` offline, `--use-env`.
  Available (linux/amd64): 1.37.0, 1.36.2, 1.36.0, 1.35.0, 1.34.1, 1.34.0, 1.33.0, ...
* Alternative without the tool: `envtest.Environment{DownloadBinaryAssets: true, DownloadBinaryAssetsVersion: "v1.37.0", BinaryAssetsDirectory: "bin/k8s"}` (fields verified).
* `envtest.Environment{CRDDirectoryPaths: []string{"../../config/crd/bases"}, ErrorIfCRDPathMissing: true, UseExistingCluster, ControlPlaneStartTimeout, AttachControlPlaneOutput, CRDInstallOptions{CleanUpAfterUse, MaxTime, PollInterval}}`;
  `cfg, _ := env.Start(); defer env.Stop()`. No kube-controller-manager/scheduler: Jobs never run, GC never cascades, you simulate child status yourself.
* **Verified gotcha (apiserver 1.37):** `Job.batch "x" is invalid: status.conditions: cannot set Complete=True condition without the SuccessCriteriaMet=true condition, status.startTime: Required value: startTime is required for finished job`.
  Set `startTime`, then `SuccessCriteriaMet=True`, then `Complete=True` (two status updates) when faking completion.
* Kubebuilder scaffolds Ginkgo v2 suites (`suite_test.go`: `BeforeSuite` starts envtest, `AfterSuite` stops it; v4.16 dropped the `Stop()` retry). Plain `testing` + `wait.PollUntilContextTimeout` works fine (section 13.4).
* e2e: `test/e2e` uses kind (`KIND_CLUSTER`), `go test -tags=e2e`, and `test/utils` helpers (`LoadImageToKindClusterWithName`, `Run`, cert-manager/prometheus installers).

---

## 9. Packaging: kustomize, Helm, CRD lifecycle

* `make build-installer` -> `dist/install.yaml` (CRDs + RBAC + Deployment, namespace `<project>-system`, prefix `<project>-`).
* `kubebuilder edit --plugins=helm/v2-alpha` (kubebuilder >= v4.11; v1-alpha users should migrate) generates `dist/chart/{Chart.yaml,values.yaml,templates/{crd,rbac,manager,metrics,cert-manager,webhook,prometheus,network-policy,extras}}`.
  CRDs are in `templates/crd/` (not `crds/`) so `helm upgrade` updates them; `values.yaml`: `crd.enable: true`, `crd.keep: true` (adds `helm.sh/resource-policy: keep`),
  `manager.args/envOverrides/healthProbe.port`, `serviceAccount.name`, `rbac.helpers`, `tolerations/nodeSelector/affinity`, optional NetworkPolicy. Re-run after `make build-installer`; `--force` regenerates everything but `Chart.yaml`.
  Caveat: charts with CR version conversion cannot set `webhook.enabled=false`.
* Alternative that avoids the plugin's churn: a hand-written `charts/clustarr` chart whose `templates/crds/*.yaml` are copied from `config/crd/bases` by a Makefile target (`cp config/crd/bases/*.yaml charts/clustarr/crds/` for classic `crds/`, or into `templates/` with `{{- if .Values.crds.install }}` to get upgrades). Helm 4.2.2 is on the dev box; keep charts Helm 3/4 compatible (no v4-only features yet).
* kustomize consumers: point `resources:` at `github.com/<org>/clustarr/config/default?ref=v0.1.0`; `config/crd` alone for CRD-only install.

---

## 10. Batch worker scheduler design inputs

### 10.1 `batch/v1` Job fields to use (verified in k8s.io/api v0.37.0)

`Parallelism`, `Completions`, `CompletionMode` (`NonIndexed|Indexed`), `ActiveDeadlineSeconds`, `BackoffLimit` (default 6), `BackoffLimitPerIndex`, `MaxFailedIndexes`,
`PodFailurePolicy{Rules[]{Action: FailJob|FailIndex|Ignore|Count, OnExitCodes{ContainerName, Operator: In|NotIn, Values}, OnPodConditions[]{Type, Status}}}`,
`SuccessPolicy{Rules[]{SucceededIndexes, SucceededCount}}`, `TTLSecondsAfterFinished`, `Suspend` (create suspended; flip to false to admit -> Kueue-style gating),
`PodReplacementPolicy: TerminatingOrFailed|Failed`, `ManagedBy` (delegate to an external controller; must be `kubernetes.io/job-controller` otherwise), `Scheduling` (new `JobSchedulingConfiguration`
with `schedulingv1alpha3` workload pod-group policy/constraints/resourceClaims — alpha, gang scheduling), `Template corev1.PodTemplateSpec`.
Status: `Conditions[]JobCondition`, `StartTime`, `CompletionTime`, `Active`, `Succeeded`, `Failed`, `Terminating`, `Ready`, `CompletedIndexes`, `FailedIndexes`, `UncountedTerminatedPods`.
Labels the Job controller stamps on pods: `batch.kubernetes.io/job-name`, `batch.kubernetes.io/controller-uid`, annotation `batch.kubernetes.io/job-completion-index`.

### 10.2 Hardware transcoding placement

| Vendor | Extended resource (limits) | Runtime / env | Node labels for affinity | Notes |
|---|---|---|---|---|
| NVIDIA (k8s-device-plugin v0.17.1, helm `nvdp/nvidia-device-plugin`) | `nvidia.com/gpu: 1` | `runtimeClassName: nvidia` (RuntimeClass handler `nvidia`, from nvidia-container-toolkit >= 1.7); env `NVIDIA_DRIVER_CAPABILITIES=video,compute,utility` for NVENC/NVDEC; `NVIDIA_VISIBLE_DEVICES` set by the runtime | GFD: `nvidia.com/gpu.present=true`, `nvidia.com/gpu.product`, `nvidia.com/gpu.count`, `nvidia.com/gpu.replicas`, `nvidia.com/gpu.sharing-strategy` | Time-slicing config `sharing.timeSlicing.resources[{name: nvidia.com/gpu, replicas: N}]` lets N ffmpeg pods share one GPU (NVENC session limit still applies); MPS is the alternative. Typical taint `nvidia.com/gpu:NoSchedule` -> add toleration |
| Intel (intel-device-plugins-for-kubernetes gpu_plugin) | `gpu.intel.com/i915: 1` (legacy i915 KMD) or `gpu.intel.com/xe: 1` (xe KMD); `gpu.intel.com/monitoring` | plugin bind-mounts `/dev/dri/renderD128` etc.; no special runtime; VAAPI/QSV via `-hwaccel vaapi -vaapi_device /dev/dri/renderD128` | NFD: `intel.feature.node.kubernetes.io/gpu=true`, `gpu.intel.com/family`, `gpu.intel.com/device-id`, `gpu.intel.com/millicores` (GAS) | `-shared-dev-num N` allows N containers per GPU; `by-path` symlink strategies `single|none|all`; install via kustomize overlays w/ NFD or the Intel Device Plugin Operator |
| AMD | `amd.com/gpu: 1` (AMD GPU device plugin) | ROCm/VAAPI via `/dev/dri` + `/dev/kfd` | `amd.com/gpu.*` labels from amd node labeller | not verified in this pass |
| Any / no device plugin | hostPath `/dev/dri` + `securityContext.privileged` or `supplementalGroups: [render gid]` | — | `feature.node.kubernetes.io/pci-0300_8086.present=true` (NFD PCI class 0300 vendor 8086/10de/1002) | fallback for homelabs; avoid privileged in the default chart |

Pod template essentials for the worker (verified compile in `buildJob`): `restartPolicy: Never`, resource requests for CPU/memory (ffmpeg software HEVC 10-bit
is CPU-bound: request 4-8 CPU, limit memory ~2-4Gi per 1080p job), `nodeAffinity.requiredDuringSchedulingIgnoredDuringExecution` on the vendor label,
`tolerations` for GPU taints, `volumeMounts` for the media PVC (`ReadWriteMany`), `securityContext` drop-all, `topologySpreadConstraints` optional,
`priorityClassName` mapped from `spec.priority`, `schedulingGates` if you want the controller to admit pods explicitly, `activeDeadlineSeconds` as a hard cap.

### 10.3 KEDA (v2.20.2) — `ScaledJob` + `nats-jetstream` scaler (docs + source verified)

```yaml
apiVersion: keda.sh/v1alpha1
kind: ScaledJob
metadata: {name: transcode-workers, namespace: clustarr}
spec:
  jobTargetRef:                # batch/v1 JobSpec
    parallelism: 1
    completions: 1
    backoffLimit: 2
    activeDeadlineSeconds: 21600
    ttlSecondsAfterFinished: 3600
    template: { spec: { restartPolicy: Never, containers: [{name: worker, image: ghcr.io/clustarr/transcodarr-worker, args: ["consume","--stream","TRANSCODE","--consumer","workers"], resources: {limits: {nvidia.com/gpu: 1}}}] } }
  pollingInterval: 15          # default 30
  successfulJobsHistoryLimit: 5   # default 100
  failedJobsHistoryLimit: 20      # default 100
  minReplicaCount: 0
  maxReplicaCount: 20          # jobs created per polling period cap
  rollout: {strategy: gradual, propagationPolicy: background}   # gradual keeps running jobs on spec change
  scalingStrategy:
    strategy: accurate         # default|custom|accurate|eager; accurate = queueLength excludes in-flight (JetStream ack-pending counts as lag, so "default" may over-create; measure)
    multipleScalersCalculation: max
  triggers:
  - type: nats-jetstream
    metadata:
      natsServerMonitoringEndpoint: "nats.nats.svc.cluster.local:8222"   # nats-server --http_port 8222 ; headless svc OK (scaler resolves cluster members via /varz and follows the consumer leader, v2.19+)
      account: "$G"                       # or accountID; can come from TriggerAuthentication
      stream: "TRANSCODE"
      consumer: "workers"                 # durable pull or push consumer; missing consumer/stream => error, no scale (fixed v2.20 to not scale to max)
      lagThreshold: "1"                   # target average value per job: lag = num_pending + num_ack_pending
      activationLagThreshold: "0"
      useHttps: "false"
```

Jobs created per poll: `maxScale = min(maxReplicaCount, ceil(queueLength / lagThreshold)); jobsToCreate = maxScale - runningJobCount` (default strategy).
Go types (verified): `kedav1alpha1.ScaledJobSpec{JobTargetRef *batchv1.JobSpec, PollingInterval, SuccessfulJobsHistoryLimit, FailedJobsHistoryLimit, RolloutStrategy, Rollout, EnvSourceContainerName, MinReplicaCount, MaxReplicaCount, ScalingStrategy{Strategy, CustomScalingQueueLengthDeduction, CustomScalingRunningJobPercentage, PendingPodConditions, MultipleScalersCalculation}, Triggers []ScaleTriggers{Type, Name, UseCachedMetrics, Metadata map[string]string, AuthenticationRef, MetricType autoscalingv2.MetricTargetType}}`.
**Do not import `github.com/kedacore/keda/v2`** into the operator: its go.mod pins `k8s.io/api v0.35.5` and `controller-runtime v0.23.3` via `replace` (consumers get MVS conflicts/behaviour skew). Emit ScaledJob/ScaledObject as `unstructured.Unstructured` (or ship them in the Helm chart) if the operator must manage them.

### 10.4 Recommended split for Transcodarr

* **Controller-created Jobs (primary):** `TranscodeJob` CR -> reconciler builds a Job with ownerRef/TTL/failure policy/GPU placement (section 13). Gives per-item status, retries, priority, GPU vs CPU
  routing, and `kubectl get tj` visibility. Concurrency is throttled by `MaxConcurrentReconciles` + a `TranscodeQueue`/quota object or by creating Jobs `suspend: true` and un-suspending N at a time (Kueue-style; Kueue itself is an option later).
* **KEDA ScaledJob (optional add-on):** for a pure "drain the JetStream consumer" worker pool (e.g. subtitle fetches, ffprobe scans) where per-item CRs are overkill. It scales the *pool*; message ownership stays in JetStream (ack/nak, `AckWait`, `MaxDeliver`).
* Both need the NATS monitoring port (8222) exposed cluster-internally for KEDA and JetStream `MaxAckPending` tuned to the pool size.

---

## 11. Multi-tenancy: cluster-scoped vs namespaced CRDs

* **Cluster-scoped** (`+kubebuilder:resource:scope=Cluster`): shared, admin-owned definitions with no owner namespace — `TranscodeProfile`, `QualityProfile`/`QualityDefinition`,
  `IndexerDefinition` (Cardigann/YAML defs), `DownloadClient` (if shared), `RootFolder` (maps to PVs/PVCs? keep namespaced if it references a PVC). Referenced from namespaced objects via `corev1.LocalObjectReference{Name}`.
  Cluster-scoped objects cannot own namespaced objects for GC purposes the other way round: a namespaced object may not have a cluster-scoped *dependent*, but a cluster-scoped owner of namespaced dependents is fine.
* **Namespaced**: per-library/tenant state — `Movie`, `Series`, `Album`, `Book`, `ImportList`, `Download`, `TranscodeJob`, `SubtitleJob`, `Indexer` (credentialed instance referencing a Secret in the same namespace).
  Tenancy = one namespace per library/household; RBAC via the scaffolded `<kind>_{admin,editor,viewer}_role.yaml` aggregated ClusterRoles bound per namespace.
* Manager scoping: cluster-wide cache by default; `--watch-namespace` -> `cache.Options.DefaultNamespaces`; kubebuilder `--namespaced` init flag scaffolds Role/RoleBinding instead of ClusterRole.
  Cluster-scoped kinds still require a ClusterRole even for a namespaced manager.
* Cross-namespace ownerReferences are invalid; use labels + field indexes + `Watches` for cross-namespace relations (e.g. cluster-scoped `TranscodeProfile` -> namespaced jobs, verified pattern).

---

## 12. What the envtest run proved (kube-apiserver 1.37.0, controller-runtime v0.25.1)

```
=== RUN   TestTranscodeJobReconciler
  phase=Pending conditions=[{Type:Ready Status:False ObservedGeneration:1 Reason:ProfileNotFound ...}] finalizers=[clustarr.io/transcodejob]
  CEL immutability OK: TranscodeJob.clustarr.io "movie-1" is invalid: spec.source: Invalid value: "other.mkv": spec.source is immutable
  CEL cross-field OK: TranscodeJob.clustarr.io "bad" is invalid: <nil>: Invalid value: do not set nvidia.com/gpu limits directly; the controller injects them from spec.hardware
  Job OK: owner=movie-1 ttl=3600 runtimeClass=nvidia limits=map[nvidia.com/gpu:1]
  final conditions: [{Ready True Succeeded} {JobCreated True Created} {Succeeded True JobComplete}]
  event: JobCreated created Job transcode-movie-1 regarding=TranscodeJob/movie-1      # events.k8s.io/v1, reportingController=transcodejob-controller
--- PASS: TestTranscodeJobReconciler (7.91s)
```

Covered: CRD install from `config/crd/bases`; `+kubebuilder:default` applied on create; field-level and type-level CEL; finalizer add without early return;
`Watches`+field-index fan-out when the profile appears; `Owns(Job)` propagating `Complete` back to the parent; new events API; finalizer removal on delete.

---

## 13. Verified code (compiles against v0.25.1; probe module `example.com/k8sprobe`, replace with `github.com/<org>/clustarr`)

### 13.1 `api/v1alpha1/groupversion_info.go`

```go
// Package v1alpha1 contains API Schema definitions for the clustarr v1alpha1 API group.
// +kubebuilder:object:generate=true
// +kubebuilder:ac:generate=true
// +groupName=clustarr.io
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	// GroupVersion is group version used to register these objects.
	GroupVersion = schema.GroupVersion{Group: "clustarr.io", Version: "v1alpha1"}

	// SchemeBuilder is used to add go types to the GroupVersionKind scheme.
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}

	// AddToScheme adds the types in this group-version to the given scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)

// SchemeGroupVersion is required by controller-gen's applyconfiguration generator (utils.go references it).
var SchemeGroupVersion = GroupVersion
```

### 13.2 `api/v1alpha1/transcodeprofile_types.go` (cluster-scoped) and `transcodejob_types.go` (namespaced)

```go
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// VideoCodec is the target video codec.
// +kubebuilder:validation:Enum=hevc;av1;h264
type VideoCodec string

// TranscodeProfileSpec is the opinionated target format for a transcode.
type TranscodeProfileSpec struct {
	// +kubebuilder:default=hevc
	// +optional
	VideoCodec VideoCodec `json:"videoCodec,omitempty"`

	// CRF quality (lower is better). x265 default 28; trash-style "optimized" ~22-24.
	// +kubebuilder:default=23
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=51
	// +optional
	CRF int32 `json:"crf,omitempty"`

	// +kubebuilder:validation:Enum=ultrafast;superfast;veryfast;faster;fast;medium;slow;slower;veryslow
	// +kubebuilder:default=medium
	// +optional
	Preset string `json:"preset,omitempty"`

	// +kubebuilder:default=true
	// +optional
	TenBit *bool `json:"tenBit,omitempty"`

	// +kubebuilder:validation:Enum=aac;opus;copy
	// +kubebuilder:default=aac
	// +optional
	AudioCodec string `json:"audioCodec,omitempty"`

	// +kubebuilder:validation:Pattern=`^[0-9]+k$`
	// +kubebuilder:default="160k"
	// +optional
	AudioBitrate string `json:"audioBitrate,omitempty"`
}

// TranscodeProfile is a cluster-scoped, reusable encode target.
//
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName=tp,categories=clustarr
// +kubebuilder:printcolumn:name="Codec",type=string,JSONPath=`.spec.videoCodec`
// +kubebuilder:printcolumn:name="CRF",type=integer,JSONPath=`.spec.crf`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type TranscodeProfile struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec TranscodeProfileSpec `json:"spec"`
}

// +kubebuilder:object:root=true
type TranscodeProfileList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TranscodeProfile `json:"items"`
}

func init() {
	SchemeBuilder.Register(&TranscodeProfile{}, &TranscodeProfileList{})
}
```

```go
package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// HardwareAccel selects the hardware encoder family a worker Pod needs.
// +kubebuilder:validation:Enum=none;nvidia;intel;amd
type HardwareAccel string

const (
	HardwareNone   HardwareAccel = "none"
	HardwareNvidia HardwareAccel = "nvidia"
	HardwareIntel  HardwareAccel = "intel"
	HardwareAMD    HardwareAccel = "amd"
)

// Condition types follow the k8s api-conventions: adjectives / past-tense verbs, positive polarity.
const (
	ConditionReady      = "Ready"      // summary condition
	ConditionJobCreated = "JobCreated" // batch/v1 Job exists
	ConditionSucceeded  = "Succeeded"  // terminal success
)

// TranscodeJobSpec is the desired state of a single transcode.
type TranscodeJobSpec struct {
	// Source is the media path (relative to the media PVC mount) to transcode. Immutable.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=4096
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec.source is immutable"
	Source string `json:"source"`

	// ProfileRef names a cluster-scoped TranscodeProfile.
	// +required
	ProfileRef corev1.LocalObjectReference `json:"profileRef"`

	// Priority 0-100, higher is scheduled first (mapped onto the controller-runtime priority queue).
	// +kubebuilder:default=50
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	// +optional
	Priority int32 `json:"priority,omitempty"`

	// +kubebuilder:default=none
	// +optional
	Hardware HardwareAccel `json:"hardware,omitempty"`

	// Resources for the worker container (CPU/memory). GPU limits are injected by the controller from Hardware.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// +kubebuilder:default=3600
	// +kubebuilder:validation:Minimum=0
	// +optional
	TTLSecondsAfterFinished *int32 `json:"ttlSecondsAfterFinished,omitempty"`

	// +kubebuilder:default=2
	// +kubebuilder:validation:Minimum=0
	// +optional
	BackoffLimit *int32 `json:"backoffLimit,omitempty"`
}

// TranscodePhase is a coarse, human-facing summary (conditions are the source of truth).
// +kubebuilder:validation:Enum=Pending;Running;Succeeded;Failed
type TranscodePhase string

const (
	PhasePending   TranscodePhase = "Pending"
	PhaseRunning   TranscodePhase = "Running"
	PhaseSucceeded TranscodePhase = "Succeeded"
	PhaseFailed    TranscodePhase = "Failed"
)

// TranscodeJobStatus is the observed state.
type TranscodeJobStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// +optional
	Phase TranscodePhase `json:"phase,omitempty"`

	// JobName is the batch/v1 Job created for this transcode.
	// +optional
	JobName string `json:"jobName,omitempty"`

	// +optional
	StartTime *metav1.Time `json:"startTime,omitempty"`
	// +optional
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`

	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// TranscodeJob requests one transcode of one source file into a TranscodeProfile.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=tj,categories=clustarr
// +kubebuilder:printcolumn:name="Profile",type=string,JSONPath=`.spec.profileRef.name`
// +kubebuilder:printcolumn:name="HW",type=string,JSONPath=`.spec.hardware`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Job",type=string,JSONPath=`.status.jobName`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:validation:XValidation:rule="self.spec.hardware != 'nvidia' || !has(self.spec.resources.limits) || !('nvidia.com/gpu' in self.spec.resources.limits)",message="do not set nvidia.com/gpu limits directly; the controller injects them from spec.hardware"
type TranscodeJob struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +required
	Spec TranscodeJobSpec `json:"spec"`
	// +optional
	Status TranscodeJobStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type TranscodeJobList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TranscodeJob `json:"items"`
}

func init() {
	SchemeBuilder.Register(&TranscodeJob{}, &TranscodeJobList{})
}
```

### 13.3 `internal/controller/transcodejob_controller.go`

```go
package controller

import (
	"context"
	"fmt"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/recorder"

	clustarrv1alpha1 "example.com/k8sprobe/api/v1alpha1"
)

const (
	// Finalizer is namespaced with the API group, per api-conventions.
	Finalizer = "clustarr.io/transcodejob"
	// profileIndexKey is a cache field index so profile changes fan out to jobs.
	profileIndexKey = ".spec.profileRef.name"
)

// TranscodeJobReconciler turns a TranscodeJob into a batch/v1 Job and mirrors its outcome.
type TranscodeJobReconciler struct {
	client.Client
	Scheme      *runtime.Scheme
	Recorder    recorder.EventRecorder // new events.k8s.io API (mgr.GetEventRecorder)
	WorkerImage string
	MediaPVC    string
}

// RBAC markers: `controller-gen rbac:roleName=manager-role paths=./...` collects these into config/rbac/role.yaml.
// +kubebuilder:rbac:groups=clustarr.io,resources=transcodejobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=clustarr.io,resources=transcodejobs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=clustarr.io,resources=transcodejobs/finalizers,verbs=update
// +kubebuilder:rbac:groups=clustarr.io,resources=transcodeprofiles,verbs=get;list;watch
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// Reconcile is idempotent: it observes the TranscodeJob + its child Job and converges status.
func (r *TranscodeJobReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	tj := &clustarrv1alpha1.TranscodeJob{}
	if err := r.Get(ctx, req.NamespacedName, tj); err != nil {
		// NotFound after delete: nothing to do (finalizer already ran or never set).
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// ---- deletion path -------------------------------------------------------------------
	if !tj.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(tj, Finalizer) {
			// External cleanup goes here (e.g. NAK the NATS message, delete scratch files).
			// The child Job is garbage collected by the API server via ownerReferences.
			controllerutil.RemoveFinalizer(tj, Finalizer)
			if err := r.Update(ctx, tj); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	// ---- ensure finalizer (metadata-only update: does NOT bump generation) ----------------
	if controllerutil.AddFinalizer(tj, Finalizer) {
		if err := r.Update(ctx, tj); err != nil {
			return ctrl.Result{}, err
		}
		// Do not return here: with GenerationChangedPredicate on For(), the resulting update
		// event is filtered and we would stall until resync. Continue with the fresh object.
	}

	// ---- main path; status is patched once at the end ------------------------------------
	before := tj.DeepCopy()
	res, err := r.reconcileNormal(ctx, tj)
	tj.Status.ObservedGeneration = tj.Generation
	if perr := r.Status().Patch(ctx, tj, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})); perr != nil {
		if apierrors.IsConflict(perr) {
			log.V(1).Info("status conflict, requeueing")
			return ctrl.Result{RequeueAfter: time.Second}, nil
		}
		if err == nil {
			err = perr
		}
	}
	return res, err
}

func (r *TranscodeJobReconciler) reconcileNormal(ctx context.Context, tj *clustarrv1alpha1.TranscodeJob) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	profile := &clustarrv1alpha1.TranscodeProfile{}
	if err := r.Get(ctx, types.NamespacedName{Name: tj.Spec.ProfileRef.Name}, profile); err != nil {
		if apierrors.IsNotFound(err) {
			setCondition(tj, clustarrv1alpha1.ConditionReady, metav1.ConditionFalse, "ProfileNotFound",
				fmt.Sprintf("TranscodeProfile %q does not exist", tj.Spec.ProfileRef.Name))
			tj.Status.Phase = clustarrv1alpha1.PhasePending
			// No requeue needed: Watches(TranscodeProfile) + field index re-enqueues us when it appears.
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	job := &batchv1.Job{}
	err := r.Get(ctx, types.NamespacedName{Namespace: tj.Namespace, Name: jobNameFor(tj)}, job)
	switch {
	case apierrors.IsNotFound(err):
		if tj.Status.Phase == clustarrv1alpha1.PhaseSucceeded || tj.Status.Phase == clustarrv1alpha1.PhaseFailed {
			// Job was TTL-collected after completion; keep terminal status, do not recreate.
			return ctrl.Result{}, nil
		}
		job = r.buildJob(tj, profile)
		if err := controllerutil.SetControllerReference(tj, job, r.Scheme); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.Create(ctx, job); err != nil {
			if apierrors.IsAlreadyExists(err) { // cache lag: someone (we) created it already
				return ctrl.Result{RequeueAfter: time.Second}, nil
			}
			return ctrl.Result{}, err
		}
		r.Recorder.Eventf(tj, nil, corev1.EventTypeNormal, "JobCreated", "Reconcile", "created Job %s", job.Name)
		tj.Status.JobName = job.Name
		tj.Status.StartTime = ptr.To(metav1.Now())
		tj.Status.Phase = clustarrv1alpha1.PhaseRunning
		setCondition(tj, clustarrv1alpha1.ConditionJobCreated, metav1.ConditionTrue, "Created", "batch/v1 Job "+job.Name)
		setCondition(tj, clustarrv1alpha1.ConditionReady, metav1.ConditionFalse, "Running", "transcode in progress")
		log.Info("created worker Job", "job", job.Name, "hardware", tj.Spec.Hardware)
		return ctrl.Result{}, nil
	case err != nil:
		return ctrl.Result{}, err
	}

	// Mirror the Job's terminal conditions into ours.
	switch {
	case jobConditionTrue(job, batchv1.JobComplete):
		tj.Status.Phase = clustarrv1alpha1.PhaseSucceeded
		tj.Status.CompletionTime = job.Status.CompletionTime
		setCondition(tj, clustarrv1alpha1.ConditionSucceeded, metav1.ConditionTrue, "JobComplete", "worker exited 0")
		setCondition(tj, clustarrv1alpha1.ConditionReady, metav1.ConditionTrue, "Succeeded", "transcode finished")
	case jobConditionTrue(job, batchv1.JobFailed):
		c := findJobCondition(job, batchv1.JobFailed)
		tj.Status.Phase = clustarrv1alpha1.PhaseFailed
		setCondition(tj, clustarrv1alpha1.ConditionSucceeded, metav1.ConditionFalse, c.Reason, c.Message)
		setCondition(tj, clustarrv1alpha1.ConditionReady, metav1.ConditionFalse, "Failed", c.Message)
		r.Recorder.Eventf(tj, job, corev1.EventTypeWarning, "JobFailed", "Reconcile", "%s: %s", c.Reason, c.Message)
	default:
		tj.Status.Phase = clustarrv1alpha1.PhaseRunning
		setCondition(tj, clustarrv1alpha1.ConditionReady, metav1.ConditionFalse, "Running",
			fmt.Sprintf("active=%d succeeded=%d failed=%d", job.Status.Active, job.Status.Succeeded, job.Status.Failed))
	}
	return ctrl.Result{}, nil
}

// buildJob is the "scheduler": it translates spec.hardware into device requests + node affinity.
func (r *TranscodeJobReconciler) buildJob(tj *clustarrv1alpha1.TranscodeJob, p *clustarrv1alpha1.TranscodeProfile) *batchv1.Job {
	labels := map[string]string{
		"app.kubernetes.io/name":       "transcodarr-worker",
		"app.kubernetes.io/managed-by": "transcodarr",
		"clustarr.io/transcodejob":     tj.Name,
		"clustarr.io/profile":          p.Name,
	}
	res := tj.Spec.Resources.DeepCopy()
	if res.Limits == nil {
		res.Limits = corev1.ResourceList{}
	}
	podSpec := corev1.PodSpec{
		RestartPolicy: corev1.RestartPolicyNever,
		Containers: []corev1.Container{{
			Name:  "ffmpeg",
			Image: r.WorkerImage,
			Args: []string{
				"transcode",
				"--source", tj.Spec.Source,
				"--video-codec", string(p.Spec.VideoCodec),
				"--crf", fmt.Sprint(p.Spec.CRF),
				"--preset", p.Spec.Preset,
				"--audio-codec", p.Spec.AudioCodec,
				"--audio-bitrate", p.Spec.AudioBitrate,
				"--hw", string(tj.Spec.Hardware),
			},
			Resources:    *res,
			VolumeMounts: []corev1.VolumeMount{{Name: "media", MountPath: "/media"}},
			SecurityContext: &corev1.SecurityContext{
				AllowPrivilegeEscalation: ptr.To(false),
				Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
			},
		}},
		Volumes: []corev1.Volume{{
			Name:         "media",
			VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: r.MediaPVC}},
		}},
	}

	switch tj.Spec.Hardware {
	case clustarrv1alpha1.HardwareNvidia:
		// Device plugin extended resource + RuntimeClass from nvidia-container-toolkit.
		res.Limits["nvidia.com/gpu"] = resource.MustParse("1")
		podSpec.RuntimeClassName = ptr.To("nvidia")
		podSpec.Containers[0].Env = append(podSpec.Containers[0].Env,
			corev1.EnvVar{Name: "NVIDIA_DRIVER_CAPABILITIES", Value: "video,compute,utility"})
		podSpec.Affinity = nodeAffinityIn("nvidia.com/gpu.present", "true")
		podSpec.Tolerations = []corev1.Toleration{{Key: "nvidia.com/gpu", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule}}
	case clustarrv1alpha1.HardwareIntel:
		// Intel GPU device plugin exposes /dev/dri render nodes through this extended resource.
		res.Limits["gpu.intel.com/i915"] = resource.MustParse("1")
		podSpec.Affinity = nodeAffinityIn("intel.feature.node.kubernetes.io/gpu", "true")
	case clustarrv1alpha1.HardwareAMD:
		res.Limits["amd.com/gpu"] = resource.MustParse("1")
	}
	podSpec.Containers[0].Resources = *res

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobNameFor(tj),
			Namespace: tj.Namespace,
			Labels:    labels,
		},
		Spec: batchv1.JobSpec{
			Parallelism:             ptr.To[int32](1),
			Completions:             ptr.To[int32](1),
			BackoffLimit:            tj.Spec.BackoffLimit,
			TTLSecondsAfterFinished: tj.Spec.TTLSecondsAfterFinished,
			ActiveDeadlineSeconds:   ptr.To[int64](6 * 3600),
			PodReplacementPolicy:    ptr.To(batchv1.Failed),
			PodFailurePolicy: &batchv1.PodFailurePolicy{Rules: []batchv1.PodFailurePolicyRule{
				{ // preemption / node drain: do not count against backoffLimit
					Action:          batchv1.PodFailurePolicyActionIgnore,
					OnPodConditions: []batchv1.PodFailurePolicyOnPodConditionsPattern{{Type: corev1.DisruptionTarget, Status: corev1.ConditionTrue}},
				},
				{ // ffmpeg "bad input" exit code: fail fast, no retry
					Action:      batchv1.PodFailurePolicyActionFailJob,
					OnExitCodes: &batchv1.PodFailurePolicyOnExitCodesRequirement{ContainerName: ptr.To("ffmpeg"), Operator: batchv1.PodFailurePolicyOnExitCodesOpIn, Values: []int32{64, 65, 66}},
				},
			}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec:       podSpec,
			},
		},
	}
}

func nodeAffinityIn(key string, values ...string) *corev1.Affinity {
	return &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
			NodeSelectorTerms: []corev1.NodeSelectorTerm{{
				MatchExpressions: []corev1.NodeSelectorRequirement{{Key: key, Operator: corev1.NodeSelectorOpIn, Values: values}},
			}},
		},
	}}
}

func jobNameFor(tj *clustarrv1alpha1.TranscodeJob) string { return "transcode-" + tj.Name }

func setCondition(tj *clustarrv1alpha1.TranscodeJob, t string, s metav1.ConditionStatus, reason, msg string) {
	meta.SetStatusCondition(&tj.Status.Conditions, metav1.Condition{
		Type: t, Status: s, Reason: reason, Message: msg, ObservedGeneration: tj.Generation,
	})
}

func findJobCondition(job *batchv1.Job, t batchv1.JobConditionType) batchv1.JobCondition {
	for _, c := range job.Status.Conditions {
		if c.Type == t {
			return c
		}
	}
	return batchv1.JobCondition{Type: t, Reason: "Unknown"}
}

func jobConditionTrue(job *batchv1.Job, t batchv1.JobConditionType) bool {
	return findJobCondition(job, t).Status == corev1.ConditionTrue
}

// SetupWithManager wires watches: own CR, owned Jobs, and a fan-out from TranscodeProfile.
func (r *TranscodeJobReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &clustarrv1alpha1.TranscodeJob{}, profileIndexKey,
		func(o client.Object) []string {
			return []string{o.(*clustarrv1alpha1.TranscodeJob).Spec.ProfileRef.Name}
		}); err != nil {
		return err
	}
	return ctrl.NewControllerManagedBy(mgr).
		Named("transcodejob").
		For(&clustarrv1alpha1.TranscodeJob{}, builder.WithPredicates(predicate.Or(
			predicate.GenerationChangedPredicate{}, // spec changes
			predicate.AnnotationChangedPredicate{}, // allow "kick" via annotation
		))).
		Owns(&batchv1.Job{}). // EnqueueRequestForOwner(OnlyControllerOwner) under the hood
		Watches(&clustarrv1alpha1.TranscodeProfile{}, handler.EnqueueRequestsFromMapFunc(r.jobsForProfile)).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 4,
			RecoverPanic:            ptr.To(true),
			UsePriorityQueue:        ptr.To(true), // default in v0.25 anyway
			ReconciliationTimeout:   2 * time.Minute,
		}).
		Complete(r)
}

func (r *TranscodeJobReconciler) jobsForProfile(ctx context.Context, o client.Object) []reconcile.Request {
	var list clustarrv1alpha1.TranscodeJobList
	if err := r.List(ctx, &list, client.MatchingFields{profileIndexKey: o.GetName()}); err != nil {
		logf.FromContext(ctx).Error(err, "listing TranscodeJobs for profile", "profile", o.GetName())
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(list.Items))
	for i := range list.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&list.Items[i])})
	}
	return reqs
}
```

SSA variant for status (`internal/controller/ssa_probe.go`, compiles with generated apply configurations):

```go
package controller

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	metav1ac "k8s.io/client-go/applyconfigurations/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	clustarrv1alpha1 "example.com/k8sprobe/api/v1alpha1"
	acv1alpha1 "example.com/k8sprobe/api/v1alpha1/applyconfiguration/api/v1alpha1"
)

// applyStatusSSA shows the server-side-apply way to write status (only fields we own, no conflicts).
func applyStatusSSA(ctx context.Context, c client.Client, tj *clustarrv1alpha1.TranscodeJob) error {
	ac := acv1alpha1.TranscodeJob(tj.Name, tj.Namespace).
		WithStatus(acv1alpha1.TranscodeJobStatus().
			WithPhase(clustarrv1alpha1.PhaseRunning).
			WithObservedGeneration(tj.Generation).
			WithConditions(metav1ac.Condition().
				WithType(clustarrv1alpha1.ConditionReady).
				WithStatus(metav1.ConditionFalse).
				WithReason("Running").
				WithObservedGeneration(tj.Generation).
				WithLastTransitionTime(metav1.Now())))
	return c.Status().Apply(ctx, ac, client.FieldOwner("transcodarr"), client.ForceOwnership)
}
```

### 13.4 `internal/controller/transcodejob_controller_test.go` (envtest, plain `testing`)

```go
package controller

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	clustarrv1alpha1 "example.com/k8sprobe/api/v1alpha1"
)

func TestTranscodeJobReconciler(t *testing.T) {
	logf.SetLogger(zap.New(zap.UseDevMode(true), zap.WriteTo(os.Stderr)))
	testEnv := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := testEnv.Start()
	if err != nil {
		t.Fatalf("envtest start: %v", err)
	}
	t.Cleanup(func() { _ = testEnv.Stop() })
	if err := clustarrv1alpha1.AddToScheme(scheme.Scheme); err != nil {
		t.Fatal(err)
	}

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 scheme.Scheme,
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
	})
	if err != nil {
		t.Fatal(err)
	}
	r := &TranscodeJobReconciler{
		Client: mgr.GetClient(), Scheme: mgr.GetScheme(),
		Recorder: mgr.GetEventRecorder("transcodejob-controller"),
		WorkerImage: "ghcr.io/clustarr/worker:test", MediaPVC: "media",
	}
	if err := r.SetupWithManager(mgr); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = mgr.Start(ctx) }()
	if !mgr.GetCache().WaitForCacheSync(ctx) {
		t.Fatal("cache sync")
	}
	k8s := mgr.GetClient()

	// --- 1. create TranscodeJob before its profile exists -> Ready=False/ProfileNotFound
	tj := &clustarrv1alpha1.TranscodeJob{
		ObjectMeta: metav1.ObjectMeta{Name: "movie-1", Namespace: "default"},
		Spec: clustarrv1alpha1.TranscodeJobSpec{
			Source: "movies/Example (2020)/Example.mkv", ProfileRef: corev1.LocalObjectReference{Name: "hevc-opt"}, Hardware: clustarrv1alpha1.HardwareNvidia,
		},
	}
	if err := k8s.Create(ctx, tj); err != nil {
		t.Fatal(err)
	}
	// API-server defaulting from +kubebuilder:default markers
	if tj.Spec.Priority != 50 || tj.Spec.TTLSecondsAfterFinished == nil || *tj.Spec.TTLSecondsAfterFinished != 3600 || tj.Spec.BackoffLimit == nil || *tj.Spec.BackoffLimit != 2 {
		t.Fatalf("defaults not applied: %+v", tj.Spec)
	}
	key := types.NamespacedName{Namespace: "default", Name: "movie-1"}
	waitFor(t, ctx, func() bool {
		_ = k8s.Get(ctx, key, tj)
		c := meta.FindStatusCondition(tj.Status.Conditions, clustarrv1alpha1.ConditionReady)
		return c != nil && c.Reason == "ProfileNotFound" && len(tj.Finalizers) == 1
	}, "Ready=False/ProfileNotFound + finalizer")
	t.Logf("phase=%s conditions=%+v finalizers=%v", tj.Status.Phase, tj.Status.Conditions, tj.Finalizers)

	// --- 2. CEL: spec.source immutable
	mut := tj.DeepCopy()
	mut.Spec.Source = "other.mkv"
	err = k8s.Update(ctx, mut)
	if !apierrors.IsInvalid(err) || !strings.Contains(err.Error(), "spec.source is immutable") {
		t.Fatalf("expected CEL immutability error, got: %v", err)
	}
	t.Logf("CEL immutability OK: %v", err)

	// --- 3. CEL: object-level cross-field rule
	bad := &clustarrv1alpha1.TranscodeJob{
		ObjectMeta: metav1.ObjectMeta{Name: "bad", Namespace: "default"},
		Spec: clustarrv1alpha1.TranscodeJobSpec{Source: "x.mkv", ProfileRef: corev1.LocalObjectReference{Name: "hevc-opt"}, Hardware: clustarrv1alpha1.HardwareNvidia,
			Resources: corev1.ResourceRequirements{Limits: corev1.ResourceList{"nvidia.com/gpu": resourceMustParse("1")}}},
	}
	err = k8s.Create(ctx, bad)
	if !apierrors.IsInvalid(err) || !strings.Contains(err.Error(), "do not set nvidia.com/gpu limits directly") {
		t.Fatalf("expected object-level CEL error, got: %v", err)
	}
	t.Logf("CEL cross-field OK: %v", err)

	// --- 4. create the (cluster-scoped) profile -> Watches()+index fan-out re-enqueues the job
	profile := &clustarrv1alpha1.TranscodeProfile{ObjectMeta: metav1.ObjectMeta{Name: "hevc-opt"}}
	if err := k8s.Create(ctx, profile); err != nil {
		t.Fatal(err)
	}
	if profile.Spec.CRF != 23 || profile.Spec.VideoCodec != "hevc" || profile.Spec.AudioBitrate != "160k" {
		t.Fatalf("profile defaults not applied: %+v", profile.Spec)
	}
	job := &batchv1.Job{}
	waitFor(t, ctx, func() bool {
		return k8s.Get(ctx, types.NamespacedName{Namespace: "default", Name: "transcode-movie-1"}, job) == nil
	}, "batch/v1 Job created")
	if !metav1.IsControlledBy(job, tj) {
		t.Fatalf("job not controlled by TranscodeJob: %+v", job.OwnerReferences)
	}
	ps := job.Spec.Template.Spec
	if ps.RuntimeClassName == nil || *ps.RuntimeClassName != "nvidia" || ps.Containers[0].Resources.Limits.Name("nvidia.com/gpu", "").String() != "1" || ps.Affinity == nil {
		t.Fatalf("gpu scheduling not applied: %+v", ps)
	}
	if job.Spec.TTLSecondsAfterFinished == nil || *job.Spec.TTLSecondsAfterFinished != 3600 || job.Spec.PodFailurePolicy == nil {
		t.Fatalf("job spec: %+v", job.Spec)
	}
	t.Logf("Job OK: owner=%s ttl=%d runtimeClass=%s limits=%v", job.OwnerReferences[0].Name, *job.Spec.TTLSecondsAfterFinished, *ps.RuntimeClassName, ps.Containers[0].Resources.Limits)
	waitFor(t, ctx, func() bool {
		_ = k8s.Get(ctx, key, tj)
		return tj.Status.Phase == clustarrv1alpha1.PhaseRunning && tj.Status.JobName == job.Name && tj.Status.ObservedGeneration == tj.Generation
	}, "phase Running")

	// --- 5. simulate the Job controller marking completion (no kube-controller-manager in envtest)
	// k8s >=1.33 Job status validation: finished Jobs need startTime, and Complete=True requires
	// SuccessCriteriaMet=True to be set first (KEP-3998 success policy, GA).
	now := metav1.Now()
	job.Status.StartTime = &now
	job.Status.Conditions = append(job.Status.Conditions, batchv1.JobCondition{Type: batchv1.JobSuccessCriteriaMet, Status: corev1.ConditionTrue, LastTransitionTime: now})
	if err := k8s.Status().Update(ctx, job); err != nil {
		t.Fatal(err)
	}
	job.Status.Conditions = append(job.Status.Conditions, batchv1.JobCondition{Type: batchv1.JobComplete, Status: corev1.ConditionTrue, LastTransitionTime: now})
	job.Status.Succeeded = 1
	job.Status.CompletionTime = &now
	if err := k8s.Status().Update(ctx, job); err != nil {
		t.Fatal(err)
	}
	waitFor(t, ctx, func() bool {
		_ = k8s.Get(ctx, key, tj)
		return tj.Status.Phase == clustarrv1alpha1.PhaseSucceeded && meta.IsStatusConditionTrue(tj.Status.Conditions, clustarrv1alpha1.ConditionReady)
	}, "phase Succeeded via Owns(Job)")
	t.Logf("final conditions: %+v", tj.Status.Conditions)

	// --- 6. events.k8s.io/v1 Event recorded through the new recorder API
	evs := &eventsv1.EventList{}
	waitFor(t, ctx, func() bool {
		_ = k8s.List(ctx, evs, client.InNamespace("default"))
		for _, e := range evs.Items {
			if e.Reason == "JobCreated" && e.ReportingController == "transcodejob-controller" {
				return true
			}
		}
		return false
	}, "events.k8s.io event")
	t.Logf("event: %s %s regarding=%s/%s", evs.Items[0].Reason, evs.Items[0].Note, evs.Items[0].Regarding.Kind, evs.Items[0].Regarding.Name)

	// --- 7. delete -> finalizer removed -> object gone
	if err := k8s.Delete(ctx, tj); err != nil {
		t.Fatal(err)
	}
	waitFor(t, ctx, func() bool { return apierrors.IsNotFound(k8s.Get(ctx, key, tj)) }, "deleted after finalizer")
}

func waitFor(t *testing.T, ctx context.Context, cond func() bool, what string) {
	t.Helper()
	if err := wait.PollUntilContextTimeout(ctx, 100*time.Millisecond, 30*time.Second, true, func(context.Context) (bool, error) { return cond(), nil }); err != nil {
		t.Fatalf("timeout waiting for %s: %v", what, err)
	}
}
```

### 13.5 `cmd/main.go` (single-manager; for cobra wrap this body in a `RunE` per subcommand)

```go
package main

import (
	"crypto/tls"
	"flag"
	"os"

	"github.com/prometheus/client_golang/prometheus"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth" // cloud auth providers
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	clustarrv1alpha1 "example.com/k8sprobe/api/v1alpha1"
	"example.com/k8sprobe/internal/controller"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")

	// Custom metric registered on controller-runtime's registry -> served on /metrics with the built-ins.
	transcodeJobsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "clustarr_transcodejobs_total", Help: "TranscodeJobs by terminal phase.",
	}, []string{"phase", "hardware"})
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(clustarrv1alpha1.AddToScheme(scheme))
	metrics.Registry.MustRegister(transcodeJobsTotal)
}

func main() {
	var (
		metricsAddr, probeAddr, watchNamespace, workerImage, mediaPVC string
		enableLeaderElection, secureMetrics, enableHTTP2         bool
	)
	flag.StringVar(&metricsAddr, "metrics-bind-address", "0", "metrics endpoint; \"0\" disables, \":8443\" secure, \":8080\" plain")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "health probe endpoint")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false, "enable leader election (one active replica)")
	flag.BoolVar(&secureMetrics, "metrics-secure", true, "serve metrics over HTTPS with authn/authz")
	flag.BoolVar(&enableHTTP2, "enable-http2", false, "enable HTTP/2 for metrics and webhook servers")
	flag.StringVar(&watchNamespace, "watch-namespace", "", "restrict cache to one namespace (empty = cluster-wide)")
	flag.StringVar(&workerImage, "worker-image", "ghcr.io/clustarr/transcodarr-worker:latest", "ffmpeg worker image")
	flag.StringVar(&mediaPVC, "media-pvc", "media", "PVC holding the media library")
	opts := zap.Options{Development: true}
	opts.BindFlags(flag.CommandLine) // adds --zap-devel, --zap-encoder, --zap-log-level, --zap-stacktrace-level, --zap-time-encoding
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	// HTTP/2 off by default (CVE-2023-44487 / -39325 mitigations), same as kubebuilder scaffold.
	tlsOpts := []func(*tls.Config){}
	if !enableHTTP2 {
		tlsOpts = append(tlsOpts, func(c *tls.Config) { c.NextProtos = []string{"http/1.1"} })
	}

	metricsOpts := metricsserver.Options{BindAddress: metricsAddr, SecureServing: secureMetrics, TLSOpts: tlsOpts}
	if secureMetrics {
		// TokenReview/SubjectAccessReview-protected /metrics (needs the metrics_auth_role RBAC).
		metricsOpts.FilterProvider = filters.WithAuthenticationAndAuthorization
	}

	cacheOpts := cache.Options{}
	if watchNamespace != "" {
		cacheOpts.DefaultNamespaces = map[string]cache.Config{watchNamespace: {}}
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                        scheme,
		Metrics:                       metricsOpts,
		HealthProbeBindAddress:        probeAddr,
		LeaderElection:                enableLeaderElection,
		LeaderElectionID:              "transcodarr.clustarr.io",
		LeaderElectionReleaseOnCancel: true,
		Cache:                         cacheOpts,
		// No admission webhooks in this binary: -1 disables the webhook server entirely (new in v0.25).
		WebhookServer: webhook.NewServer(webhook.Options{Port: -1, TLSOpts: tlsOpts}),
		Controller: config.Controller{
			RecoverPanic:     ptr.To(true),
			UsePriorityQueue: ptr.To(true),
			// Per-kind concurrency without touching each controller: key is "Kind.group".
			GroupKindConcurrency: map[string]int{"TranscodeJob.clustarr.io": 8},
		},
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	if err := (&controller.TranscodeJobReconciler{
		Client:      mgr.GetClient(),
		Scheme:      mgr.GetScheme(),
		Recorder:    mgr.GetEventRecorder("transcodejob-controller"),
		WorkerImage: workerImage,
		MediaPVC:    mediaPVC,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "TranscodeJob")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
```

### 13.6 Generated RBAC (`controller-gen rbac:roleName=manager-role ... output:rbac:artifacts:config=config/rbac`)

```yaml
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: manager-role
rules:
- apiGroups:
  - batch
  resources:
  - jobs
  verbs:
  - create
  - delete
  - get
  - list
  - patch
  - update
  - watch
- apiGroups:
  - clustarr.io
  resources:
  - transcodejobs
  verbs:
  - create
  - delete
  - get
  - list
  - patch
  - update
  - watch
- apiGroups:
  - clustarr.io
  resources:
  - transcodejobs/finalizers
  verbs:
  - update
- apiGroups:
  - clustarr.io
  resources:
  - transcodejobs/status
  verbs:
  - get
  - patch
  - update
- apiGroups:
  - clustarr.io
  resources:
  - transcodeprofiles
  verbs:
  - get
  - list
  - watch
- apiGroups:
  - events.k8s.io
  resources:
  - events
  verbs:
  - create
  - patch
```

### 13.7 Cobra multi-service sketch (not compiled; cobra v1.10.2 API)

```go
// cmd/clustarr/main.go
func main() {
    root := &cobra.Command{Use: "clustarr"}
    root.AddCommand(
        service("transcodarr", "transcodarr.clustarr.io", transcode.AddToScheme, transcodecontroller.Setup),
        service("downloadarr", "downloadarr.clustarr.io", download.AddToScheme, downloadcontroller.Setup),
        service("indexarr",    "indexarr.clustarr.io",    indexer.AddToScheme,  indexercontroller.Setup),
        service("inventorri",  "inventorri.clustarr.io",  inventory.AddToScheme, inventorycontroller.Setup),
        service("subtitlarr",  "subtitlarr.clustarr.io",  subtitle.AddToScheme, subtitlecontroller.Setup),
        allCommand(), // registers every AddToScheme + Setup in ONE manager, LeaderElectionID "clustarr.clustarr.io"
    )
    cobra.CheckErr(root.Execute())
}

type setupFn func(mgr ctrl.Manager, deps Deps) error

func service(name, leID string, add func(*runtime.Scheme) error, setup setupFn) *cobra.Command {
    o := internalmanager.NewOptions() // metrics/probe/leader-elect/zap flags shared across services
    cmd := &cobra.Command{Use: name, RunE: func(cmd *cobra.Command, _ []string) error {
        scheme := runtime.NewScheme()
        utilruntime.Must(clientgoscheme.AddToScheme(scheme)); utilruntime.Must(add(scheme))
        mgr, err := internalmanager.New(scheme, leID, o) // wraps ctrl.NewManager with the options from section 6
        if err != nil { return err }
        if err := setup(mgr, o.Deps()); err != nil { return err }
        return mgr.Start(ctrl.SetupSignalHandler())
    }}
    o.BindFlags(cmd.Flags())
    return cmd
}
```

---

## 14. Data model to borrow (Go-oriented, for the design team)

```go
// ---- shared (pkg/apis/common or api/<group> shared file) ----
type ObjectRef struct {            // cross-kind ref; namespaced-to-cluster refs use Name only
    Name      string `json:"name"`
    Namespace string `json:"namespace,omitempty"`
}
// Conditions everywhere:
//   Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"` with +listType=map +listMapKey=type
// Status everywhere: ObservedGeneration int64; Phase <KindPhase> enum for humans; conditions for machines.
// Condition vocabulary: Ready (summary), Progressing, Degraded, Succeeded (bounded work), Available (services), plus kind-specific (JobCreated, Downloaded, Imported, Indexed, Transcoded, SubtitlesFetched).
// Reasons: CamelCase single word; e.g. ProfileNotFound, Running, JobComplete, BackoffLimitExceeded, DeadlineExceeded, PodFailurePolicy.

// ---- transcode.clustarr.io (verified compile) ----
type HardwareAccel string  // none|nvidia|intel|amd  (+kubebuilder:validation:Enum)
type TranscodeProfileSpec struct { VideoCodec VideoCodec /*hevc|av1|h264, default hevc*/; CRF int32 /*0..51, default 23*/; Preset string /*x265 presets, default medium*/; TenBit *bool /*default true*/; AudioCodec string /*aac|opus|copy, default aac*/; AudioBitrate string /*^[0-9]+k$, default 160k*/ }
type TranscodeJobSpec struct { Source string /*immutable via CEL*/; ProfileRef corev1.LocalObjectReference; Priority int32 /*0..100 default 50*/; Hardware HardwareAccel /*default none*/; Resources corev1.ResourceRequirements; TTLSecondsAfterFinished *int32 /*3600*/; BackoffLimit *int32 /*2*/ }
type TranscodeJobStatus struct { ObservedGeneration int64; Phase TranscodePhase /*Pending|Running|Succeeded|Failed*/; JobName string; StartTime, CompletionTime *metav1.Time; Conditions []metav1.Condition }
// Child: batch/v1 Job "transcode-<name>", controller ownerRef, labels app.kubernetes.io/name=transcodarr-worker, clustarr.io/transcodejob=<name>, clustarr.io/profile=<profile>.

// ---- worker scheduling knobs to expose on every *Job kind ----
type WorkerPlacement struct {
    Hardware      HardwareAccel                 `json:"hardware,omitempty"`
    Resources     corev1.ResourceRequirements   `json:"resources,omitempty"`
    NodeSelector  map[string]string             `json:"nodeSelector,omitempty"`
    Tolerations   []corev1.Toleration           `json:"tolerations,omitempty"`
    PriorityClass string                        `json:"priorityClassName,omitempty"`
    RuntimeClass  *string                       `json:"runtimeClassName,omitempty"`
}
type WorkerPolicy struct {
    TTLSecondsAfterFinished *int32 `json:"ttlSecondsAfterFinished,omitempty"`  // default 3600
    BackoffLimit            *int32 `json:"backoffLimit,omitempty"`             // default 2
    ActiveDeadlineSeconds   *int64 `json:"activeDeadlineSeconds,omitempty"`    // default 6h
    Suspend                 *bool  `json:"suspend,omitempty"`                  // admission gating
}

// ---- manager/service wiring (internal/manager) ----
type Options struct { MetricsAddr, ProbeAddr, WatchNamespace string; LeaderElect, SecureMetrics, EnableHTTP2 bool; LeaderElectionID string; Zap zap.Options }
// LeaderElectionID per service: "<svc>.clustarr.io"; Lease lives in the manager namespace.
```

---

## 15. Recommendations (condensed)

1. `kubebuilder init --domain clustarr.io --multigroup --repo github.com/<org>/clustarr` then bump to controller-runtime v0.25.1 / k8s v0.37.0; keep `controller-gen v0.22.0`, kustomize v5.8.1, golangci-lint v2.13.x, envtest 1.37.
2. One Go module, one image, cobra subcommands per service + `all`; one manager per process; per-service `LeaderElectionID`; non-leader runnables for NATS consumers/HTTP.
3. API groups per service under `api/<group>/v1alpha1`; cluster-scoped for shared definitions (profiles, indexer definitions, quality profiles), namespaced for library state and jobs; `LocalObjectReference` + field index + `Watches` for cross-object links.
4. Status = conditions (`Ready`/`Succeeded` + specific) with `observedGeneration`; patch once per reconcile with optimistic lock, or SSA via generated apply configurations (`+kubebuilder:ac:generate=true`, add `SchemeGroupVersion`).
5. Enable `+kubebuilder:ac:generate`, `+kubebuilder:selectablefield` where users will filter (`spec.profileRef.name`, `status.phase`), CEL immutability on identity fields (`source`, `tmdbId`, `rootFolder`).
6. Workers: controller-owned `batch/v1` Jobs with ownerRef, TTL, `podFailurePolicy`, `podReplacementPolicy: Failed`, GPU translation in the controller (never by users; enforce with CEL), affinity on `nvidia.com/gpu.present` / `intel.feature.node.kubernetes.io/gpu`. Add KEDA `ScaledJob` + `nats-jetstream` for CR-less pools; never import KEDA's Go module.
7. Metrics: register custom collectors on `metrics.Registry`; enable REST client metrics; secure metrics with `filters.WithAuthenticationAndAuthorization` and the scaffolded `metrics_auth_role`; readiness check that pings NATS.
8. Tests: envtest per controller package (plain `testing` is fine), simulate Job completion with `startTime` + `SuccessCriteriaMet` + `Complete`; kind e2e for the GPU-less path.
9. Packaging: kustomize `config/` is canonical; generate Helm with `helm/v2-alpha` (CRDs in `templates/crd`, `crd.keep=true`); publish `dist/install.yaml` per release.
10. Keep `UsePriorityQueue` on (default); when the queue backlog matters (thousands of `Movie`s), wrap handlers with `handler.WithLowPriorityWhenUnchanged` and set `Result.Priority` for hot paths; consider `EnableWarmup` for fast failover of the inventory service.

## 16. Sources

* `go doc` / `go list -m -versions` on this machine for sigs.k8s.io/controller-runtime v0.25.1, k8s.io/* v0.37.0, controller-tools v0.22.0, keda v2.20.2 (2026-09-18)
* `controller-gen -h`, `controller-gen crd|rbac|webhook|object|applyconfiguration -w` (v0.22.0)
* `setup-envtest --help`, `setup-envtest list`, envtest run with kube-apiserver 1.37.0
* https://github.com/kubernetes-sigs/controller-runtime/releases (v0.25.0, v0.25.1, v0.24.0)
* controller-runtime module cache: `designs/priorityqueue.md`, `designs/warmreplicas.md`, `examples/priorityqueue/main.go`, `pkg/recorder/recorder.go`
* https://github.com/kubernetes-sigs/controller-tools/releases (v0.22.0, v0.21.0, v0.20.0)
* https://github.com/kubernetes-sigs/kubebuilder/releases (v4.16.0 ... v4.11.0) and https://api.github.com/repos/kubernetes-sigs/kubebuilder/releases/latest
* https://raw.githubusercontent.com/kubernetes-sigs/kubebuilder/master/testdata/project-v4/{Makefile,cmd/main.go,Dockerfile,PROJECT,.golangci.yml,config/**}
* https://book.kubebuilder.io/plugins/available/helm-v2-alpha ; https://book.kubebuilder.io/migration/multi-group
* DeepWiki: kubernetes-sigs/kubebuilder (layout + Makefile), kedacore/keda (nats-jetstream scaler internals), kubernetes-sigs/kueue (single-binary multi-controller structure)
* https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md (conditions)
* https://keda.sh/docs/2.20/scalers/nats-jetstream/ ; https://keda.sh/docs/2.20/reference/scaledjob-spec/
* https://github.com/NVIDIA/k8s-device-plugin/blob/main/README.md ; https://github.com/intel/intel-device-plugins-for-kubernetes/blob/main/cmd/gpu_plugin/README.md
