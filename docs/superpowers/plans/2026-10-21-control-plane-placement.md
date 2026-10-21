# Control-Plane Placement Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Run the manager, the ui, NATS and the Plex database as three replicas on the three control-plane nodes (one per node), every other clustarr pod on workers, and bring the manager's memory down.

**Architecture:** One placement model is used three ways:

- **Helm:** two named profiles (`placement.profiles.controlPlane`, `placement.profiles.workers`) rendered by one helper.
- **Manager-built pods:** one Go type (`pkg/placement.Profile`) that the manager applies to every pod it builds.
- **Literal subchart values** (NATS, cluster-plex), checked at render time.

The Live TV tuner stays leader-only, and the leader publishes itself as the `livetv` Service's only endpoint.

**Tech Stack:** Go 1.27, controller-runtime v0.25.1, Helm 3/4 chart `charts/clustarr`, kustomize `config/`, CloudNativePG 1.30, nats chart 2.14.6.

**Spec:** `docs/superpowers/specs/2026-10-21-control-plane-placement-design.md`. Read §3 (profiles), §5.1 (CNPG settings), §5.4 (leader endpoint), §5.4.1 (memory) and §9 (rollout) before any task.

## Global Constraints

**Repository rules:**

- Every Go file carries the GPL-3.0 header from `hack/boilerplate.go.txt`.
- Logging is `slog` through `pkg/obs/logging.FromContext(ctx)`, never a package logger.
- No *arr brand names, held by `make brand-gate`.

**Writes and leadership:**

- Kubernetes writes from the manager go through `pkg/k8s.Apply` (a complete declaration, `ForceOwnership`) under a named field manager.
- Never strip fields in a cache transform: a server-side-apply reassert from the cache would release them. §5.4.1 item 5 allows only value-preserving transforms.
- A leader-only runnable implements `NeedLeaderElection() bool { return true }`, or is wrapped in `k8s.LeaderOnly`. An every-replica one uses `k8s.EveryReplica` (`pkg/k8s/manager.go:414-434`).

**Placement defaults:**

- With `placement.enabled: false` (the chart default), every rendered object is unchanged. Kustomize keeps one manager and one ui replica.
- Profile values are verbatim from spec §3:
  - `controlPlane`: `replicas: 3`, `onePerNode: true`, `nodeAffinity` `[{key: node-role.kubernetes.io/control-plane, operator: Exists}]`, `tolerations` `[{key: node-role.kubernetes.io/control-plane, operator: Exists, effect: NoSchedule}]`.
  - `workers`: `onePerNode: false`, `nodeAffinity` `[{key: node-role.kubernetes.io/control-plane, operator: DoesNotExist}, {key: kubernetes.io/arch, operator: In, values: [amd64]}]`, `tolerations: []`.
- A `onePerNode` Deployment gets `strategy.rollingUpdate: {maxSurge: 0, maxUnavailable: 1}` and a PodDisruptionBudget `maxUnavailable: 1`.

**Plex database (CNPG):**

- `postgresql.synchronous: {method: any, number: 1, dataDurability: preferred}`, no `failoverQuorum` key.
- `postgresql.parameters.max_slot_wal_keep_size: "2GB"`.
- `primaryUpdateStrategy: unsupervised`, `primaryUpdateMethod: switchover`.
- `affinity: {enablePodAntiAffinity: true, podAntiAffinityType: required, topologyKey: kubernetes.io/hostname}`.

**Manager memory (talos values, spec §5.4.1):** `GOGC=50`, `GOMEMLIMIT=1GiB`, pprof on `127.0.0.1:6060`.

**Gate for every task**, per the owner's 2026-10-08 rule (no envtest runs until the end):

- `go build ./...`
- `go vet` of the touched packages
- `make brand-gate`
- plain `go test` of the touched packages, without `KUBEBUILDER_ASSETS` (envtest suites skip)
- chart tasks also: `helm lint --strict charts/clustarr` and `make chart-deps` once per checkout
- RBAC-marker tasks also: `make manifests` leaves a clean diff after committing its output

Envtest tests this plan names are written and committed. They run in the single final `make test`.

**Git:**

- Work on `main` in `/home/appkins/src/mediactl/clustarr`.
- Commit with a pathspec: `git commit -m '…' -- <paths>`, after `git add` of every new file.
- Never `git stash`, never push from a subagent, never `go mod tidy`. No new dependency is needed.

**The talos tasks (§D)** are run only by the controlling session, with the owner's OK. They upgrade from the saved `talos-values.yaml` with the `pin-amd64` post-renderer until Task D8.

## Review Focus

1. **A component's own required node affinity with several terms.** Terms OR, so the profile must be appended to every term, never added as a new term. A new term would let a pod skip the profile. The tests are in Task A1 and Task B2.
2. **The leader endpoint with no `POD_IP`** (`make run-dev`, or a kustomize overlay that forgot the env) must not publish an empty or wrong address. It logs once and publishes nothing. Test in Task A4.
3. **The tuner disabled** (`--livetv-bind-address=0`) registers no endpoint runnable and leaves the Service without endpoints, rather than a slice pointing at a port nobody listens on. Test in Task A4.
4. **A `--worker-placement` value that is not valid JSON** stops the manager at start with the flag named. It must never start with no placement. Test in Task A2.
5. **More replicas than the profile allows** (`manager.replicas: 4` with three control-plane nodes) fails the render with a message naming the key, instead of rendering a pod that stays Pending forever. Test in Task B2.

---

## Part A: Go code

### Task 1 (A1): `pkg/placement`, the profile type

**Files:**

- Create: `pkg/placement/placement.go`, `pkg/placement/placement_test.go`

**Interfaces:**

- Produces:

```go
package placement

// Profile is a placement profile (spec §3): expressions every required
// node-affinity term must carry, and tolerations to add.
type Profile struct {
	NodeAffinity []corev1.NodeSelectorRequirement `json:"nodeAffinity,omitempty"`
	Tolerations  []corev1.Toleration              `json:"tolerations,omitempty"`
}
func Parse(s string) (Profile, error)                          // "" → zero Profile
func (p Profile) IsZero() bool
func (p Profile) Apply(spec *corev1.PodSpec)                   // mutates spec
func (p Profile) ApplyAC(spec *corev1ac.PodSpecApplyConfiguration)
```

- [ ] **Step 1: Write the failing tests** in `placement_test.go`. Table-driven, testify, with these assertions:
  - `TestApplyANDsIntoEveryRequiredTerm`: a spec with two required terms (`a In [x]`) OR (`b Exists`), after `Apply` of the workers profile, has exactly two terms, each ending with the profile's two expressions in order, and its own first expression kept.
  - `TestApplyCreatesTheTermWhenNone`: nil `Affinity` → one term equal to the profile's expressions.
  - `TestApplyKeepsPreferredAndPodAffinity`: `Preferred…` node terms and `PodAffinity` / `PodAntiAffinity` are unchanged.
  - `TestApplyAppendsTolerationsOnce`: applying twice yields the profile's tolerations once (no duplicate by key, operator and effect).
  - `TestZeroProfileChangesNothing`: `reflect.DeepEqual` before and after.
  - `TestParseRejectsInvalidJSON`: `Parse("{")` returns an error whose text contains `worker placement`.
  - `TestParseEmptyIsZero`.
  - `TestApplyACMatchesApply`: building a spec through `ApplyAC` then `json.Marshal` equals `Apply` on the same starting spec.

- [ ] **Step 2: Run** `go test ./pkg/placement/` and expect a build failure (package missing).

- [ ] **Step 3: Implement** `placement.go`.
  - `Parse` uses `json.Unmarshal` and wraps errors as `fmt.Errorf("worker placement: %w", err)`.
  - `Apply` appends to each existing term's `MatchExpressions`, or creates one term.
  - Toleration dedupe compares `Key`, `Operator`, `Value` and `Effect`.

- [ ] **Step 4: Run** `go test ./pkg/placement/` and expect PASS.

- [ ] **Step 5: Commit:** `git add pkg/placement && git commit -m 'feat(placement): a placement profile ANDed into every required node-affinity term' -- pkg/placement`

### Task 2 (A2): `--worker-placement` reaches every pod the manager builds

**Files:**

- Modify:
  - `internal/cli/cli.go` (add `EnvWorkerPlacement = "CLUSTARR_WORKER_PLACEMENT"` beside `EnvNativeImage`)
  - `internal/cli/manager/options.go` (`Options.WorkerPlacement placement.Profile`)
  - `internal/cli/manager/command.go` (flag)
  - `app/transcode/controller/pool/template.go` (`Config.WorkerPlacement`, applied at the end of `Template` for every class)
  - `app/transcode/manager/register.go:130` (`poolConfig` copies it)
  - `app/grab/controller/downloadclient` (`EngineRuntime.WorkerPlacement`, applied with `ApplyAC` after `workload.go:396-401`)
  - `app/grab/manager/register.go` (`engineRuntime(o)` copies it)
- Test:
  - `app/transcode/controller/pool/template_test.go`
  - `app/remediation/graft/job_test.go`
  - `app/grab/controller/downloadclient/workload_test.go`
  - `internal/cli/manager/options_test.go`

**Interfaces:**

- Consumes: `placement.Profile`, `placement.Parse`, `Profile.Apply`, `Profile.ApplyAC` (Task A1).
- Produces: flag `--worker-placement` (JSON; default from `$CLUSTARR_WORKER_PLACEMENT`; empty = no placement), `pool.Config.WorkerPlacement placement.Profile`, `downloadclient.EngineRuntime.WorkerPlacement placement.Profile`.

- [ ] **Step 1: Write the failing tests.**
  - `TestEveryPoolClassCarriesTheWorkerPlacement`: for `HardwareCPU`, `HardwareIntel` and `HardwareNVIDIA`, `pool.Template(tp, class, cfg)` with `cfg.WorkerPlacement` set to the workers profile has every required term containing both workers expressions. Intel and NVIDIA keep their GPU label expression in the same term.
  - `TestGraftJobCarriesTheWorkerPlacement`: `graft.Job(cfg, mf, task, "g")` has the same.
  - `TestEnginePodCarriesTheWorkerPlacement`: a DownloadClient with `spec.nodeSelector {kubernetes.io/hostname: talos-b8-ae-ed-76-79-61}` renders an engine pod that keeps that nodeSelector and has the workers expressions in its required affinity.
  - `TestWorkerPlacementFlagReachesPoolAndEngines`: parsing `--worker-placement='{"nodeAffinity":[{"key":"k","operator":"Exists"}]}'` yields `poolConfig(o).WorkerPlacement` and `engineRuntime(o).WorkerPlacement` equal to that profile. Extend the existing `TestPoolConfigCarriesTheControllerOptions` pattern.
  - `TestInvalidWorkerPlacementStopsTheManager`: the command's `PreRunE` (or `Options.Validate`) returns an error containing `--worker-placement` for `'{'`.

- [ ] **Step 2: Run** `go test ./pkg/placement/ ./app/transcode/controller/pool/ ./app/remediation/graft/ ./app/grab/controller/downloadclient/ ./internal/cli/manager/` and expect FAIL: the fields are undefined.

- [ ] **Step 3: Implement** the fields and the flag. Parse the flag with `placement.Parse` where the manager validates options, and return its error.

- [ ] **Step 4: Run** the same `go test` and expect PASS.

- [ ] **Step 5: Commit** the listed files with `feat(manager): --worker-placement holds the engines, the transcode pools and graft Jobs to the workers`.

### Task 3 (A3): the tuner says when it is listening

**Files:**

- Modify: `app/livetv/tuner/tuner.go` (`Tuner` gains a `listening chan struct{}` made in `New`, closed once after `net.Listen` succeeds in `Start`)
- Test: `app/livetv/tuner/tuner_test.go`

**Interfaces:**

- Produces: `func (t *Tuner) Listening() <-chan struct{}`. It closes after the listener is bound, and never closes when `BindAddress` is empty or `Start` failed to listen.

- [ ] **Step 1: Write the failing tests.**
  - `TestListeningClosesOnceTheListenerIsBound`: `New(Options{BindAddress: "127.0.0.1:0", …})`, `go Start(ctx)`, then `<-t.Listening()` within 2 s.
  - `TestListeningNeverClosesWhenDisabled`: `BindAddress: ""`, and `Listening()` is still open after `Start` has run 200 ms.

- [ ] **Step 2: Run** `go test ./app/livetv/tuner/ -run Listening` and expect FAIL.

- [ ] **Step 3: Implement** it: close with `sync.Once` right after `net.Listen` returns nil.

- [ ] **Step 4: Run** the tests and expect PASS.

- [ ] **Step 5: Commit** with `feat(livetv): the tuner reports when it is listening`.

### Task 4 (A4): the leader publishes itself as the `livetv` endpoint

**Files:**

- Create:
  - `app/livetv/leaderendpoint/endpoint.go` (with `doc.go` holding the RBAC marker: `// +kubebuilder:rbac:groups=discovery.k8s.io,resources=endpointslices,verbs=get;create;update;patch`)
  - `app/livetv/leaderendpoint/endpoint_test.go`
  - `app/livetv/leaderendpoint/envtest_test.go` (written, not run)
- Modify:
  - `pkg/k8s/fieldmanager.go` (add `ManagerLiveTVEndpoint FieldManager = "clustarr-livetv-endpoint"`)
  - `pkg/k8s/fieldmanager_test.go` (its list)
  - `app/livetv/manager/register.go` (register the publisher after the tuner, only when the tuner is registered)
  - regenerated `config/rbac/manager_role.yaml` (`RBAC_PATHS_manager` already covers `./app/livetv/...`), and the chart's held copy in `charts/clustarr/templates/rbac.yaml`

**Interfaces:**

- Consumes: `tuner.Tuner.Listening()` (Task A3), `livetv/manager.TargetPort(bind string) (int32, error)`, `k8s.Apply`.
- Produces:

```go
package leaderendpoint

type Publisher struct {
	Client    client.Client
	Tuner     interface{ Listening() <-chan struct{} }
	Service   string // the livetv Service's name (--livetv-service)
	Namespace string
	PodName   string // $POD_NAME
	PodIP     string // $POD_IP
	Port      int32  // the tuner's port, from TargetPort
}
func (p *Publisher) Start(ctx context.Context) error
func (p *Publisher) NeedLeaderElection() bool // true
```

- [ ] **Step 1: Write the failing unit tests** (controller-runtime fake client with SSA).
  - `TestPublishesTheLeadersAddressAfterTheTunerListens`: before `Listening` closes, there's no EndpointSlice. After it, there's one named `<Service>`, with:
    - labels `kubernetes.io/service-name=<Service>` and `endpointslice.kubernetes.io/managed-by=clustarr.io/manager`;
    - `addressType: IPv4`, one endpoint with address `PodIP`, `conditions.ready=true`, and `targetRef {kind: Pod, name: PodName, namespace}`;
    - one port named `http`, number `Port`, protocol TCP;
    - managed fields owned by `clustarr-livetv-endpoint`.
  - `TestANewLeaderReplacesTheAddress`: a second `Publisher` with another `PodIP` overwrites the single endpoint.
  - `TestSkipsWithoutAPodIP`: `PodIP: ""` produces no slice and `Start` returns nil at ctx end.
  - `TestNeedsLeaderElection`.
  - `TestNoPublisherWhenTheTunerIsDisabled` (in `app/livetv/manager`): `Register` with `BindAddress: "0"` adds no `*leaderendpoint.Publisher` to the manager. Use a fake `manager.Manager` recording `Add`.

- [ ] **Step 2: Run** `go test ./app/livetv/... ./pkg/k8s/` and expect FAIL.

- [ ] **Step 3: Implement** it.
  - `Start` waits on `Listening()` or `ctx.Done()`, applies the slice once through `k8s.Apply(ctx, c, k8s.ManagerLiveTVEndpoint, ac)`, then blocks until `ctx.Done()`.
  - It retries a failed apply with `wait.ExponentialBackoff` (1 s doubling to 30 s) and logs each failure.
  - Register reads `POD_NAME` and `POD_IP` with `os.Getenv`.

- [ ] **Step 4: Write** the envtest `TestLiveTVServiceRoutesToTheLeader`: two managers on one envtest, the second taking the lease after the first's context ends, with the slice's address following. **Do not run it** (the 2026-10-08 rule); it runs in Task C1.

- [ ] **Step 5: Run** the unit tests, then `make manifests`. Expect PASS, and a `manager_role.yaml` diff with only `endpointslices`.

- [ ] **Step 6: Commit** the listed files with `feat(livetv): the leader publishes itself as the livetv Service's endpoint`.

### Task 5 (A5): standby warmth and a leader gauge

**Files:**

- Modify:
  - `internal/cli/manager/run.go` (add the warmer and the gauge where the other `EveryReplica` runnables are added, `run.go:134-138`)
  - `pkg/k8s/manager.go` (add `WarmInformers`)
- Create: `pkg/k8s/warm_test.go`, `cmd/manager/standby_envtest_test.go` (written, not run)

**Interfaces:**

- Produces:
  - `func WarmInformers(mgr ctrl.Manager, objs ...client.Object) error`: an `EveryReplica` runnable calling `mgr.GetCache().GetInformer(ctx, obj)` for each.
  - The metric `clustarr_manager_leader` (gauge, 1 after `<-mgr.Elected()`, else 0), registered on the controller-runtime metrics registry.

- [ ] **Step 1: Write the failing tests.**
  - `TestWarmInformersAsksForEachKind`: a fake cache records `GetInformer` calls for `MediaFile`, `Episode`, `Movie` and `Series`, and the runnable reports `NeedLeaderElection() == false`.
  - The envtest `TestStandbyWarmsTheFailoverKinds` (spec §8): written, not run.

- [ ] **Step 2: Run** `go test ./pkg/k8s/ -run Warm` and expect FAIL.

- [ ] **Step 3: Implement** it. `run.go` calls `k8s.WarmInformers(mgr, &catalogv1alpha1.MediaFile{}, &catalogv1alpha1.Episode{}, &catalogv1alpha1.Movie{}, &catalogv1alpha1.Series{})`. The gauge runnable is `EveryReplica` and sets the gauge on `<-mgr.Elected()`.

- [ ] **Step 4: Run** the tests and expect PASS.

- [ ] **Step 5: Commit** with `feat(manager): a standby keeps the failover kinds warm; clustarr_manager_leader`.

### Task 6 (A6): intern condition strings in the Episode and MediaFile caches

**Files:**

- Modify: `pkg/k8s/manager.go:244` (`cache.Options.ByObject` for Episode and MediaFile with `Transform: InternConditions`, chained after `TransformStripManagedFields`)
- Create: `pkg/k8s/intern.go`, `pkg/k8s/intern_test.go`

**Interfaces:**

- Produces: `func InternConditions(obj any) (any, error)`. For an object with `Status.Conditions []metav1.Condition`, it replaces `Type`, `Status`, `Reason` and `Message` with `unique.Make(s).Value()`. Other objects pass through unchanged.

- [ ] **Step 1: Write the failing tests.**
  - `TestInternConditionsPreservesValues`: the object is `reflect.DeepEqual` before and after.
  - `TestInternConditionsSharesStorage`: two Episodes whose conditions carry the same message share one string pointer (`unsafe.StringData` equal).
  - `TestInternConditionsPassesOtherObjectsThrough`.

- [ ] **Step 2: Run** `go test ./pkg/k8s/ -run Intern` and expect FAIL.

- [ ] **Step 3: Implement** it with a type switch on `*catalogv1alpha1.Episode` and `*catalogv1alpha1.MediaFile`; no reflection.

- [ ] **Step 4: Run** the tests and expect PASS.

- [ ] **Step 5: Commit** with `perf(manager): the Episode and MediaFile caches share repeated condition strings`.

### Task 7 (A7): stop copying read-only whole-set lists (profile-driven)

Runs **after Task D2** has produced the talos `alloc_space` profile (`/tmp/claude-…/scratchpad/pprof/alloc.pb.gz` from the controlling session).

**Files:** only the call sites the profile ranks. The spec §5.4.1 candidates are:

- `app/transcode/admission/sweep.go` and `rebuild.go`
- `app/transcode/controller/transcodeprofile/counts.go` and `controller.go`
- `app/remediation/transcode/sources.go`, `app/remediation/probe/sources.go` and `app/remediation/downloads/gather.go`
- `app/import/manager/scanapply/orphans.go`

**Rule, per call site:**

- Convert a `List` only if it is a MediaFile or Episode `List` with no label or field selector, or with an index selector.
- Convert only if the profile attributes at least 1 MiB/s of `alloc_space` to it.
- Convert only if every object it returns is only read: never passed to `Patch`, `Update`, `Apply` or `DeepCopyInto` a write, and never stored past the reconcile.

Add `client.UnsafeDisableDeepCopy` as `app/remediation/subtitles/sources.go:85` does. Above each converted call, add the comment: `// Read-only: these are the cache's own objects (UnsafeDisableDeepCopy).`

- [ ] **Step 1:** List the qualifying sites with their MiB/s from `go tool pprof -top -sample_index=alloc_space`, and paste the table into the commit message.

- [ ] **Step 2:** Convert them. For each, read its package's existing tests; any test that mutates a listed object fails under the rule and shows a site that must stay copied.

- [ ] **Step 3: Run** `go test` of each touched package and expect PASS.

- [ ] **Step 4: Commit** with `perf(manager): read-only whole-set lists skip the deep copy` plus the table.

---

## Part B: installers

### Task 8 (B1): selector-less `livetv` Service and `POD_IP`, both installers

**Files:**

- Modify:
  - `charts/clustarr/templates/livetv-service.yaml` (drop `selector`)
  - `charts/clustarr/templates/_helpers.tpl:353` (add `POD_IP` from `status.podIP` beside `POD_NAME`, manager only)
  - `config/manager/livetv-service.yaml` (drop `selector`)
  - `config/manager/manager.yaml:99` (add `POD_IP`)
- Test: `test/guards/livetv/` (a new `TestLiveTVServiceIsSelectorLessInBothInstallers`)

- [ ] **Step 1: Write the failing test.** It renders `installtest.RenderChart(t)` and `kustomize build config/default`, and asserts:
  - the `-livetv` / `livetv` Service has no `spec.selector`, and its port is named `http` (80 → `livetv`);
  - the manager container in both has env `POD_IP` with `valueFrom.fieldRef.fieldPath: status.podIP`.

- [ ] **Step 2: Run** `go test ./test/guards/livetv/` and expect FAIL.

- [ ] **Step 3: Implement** the four edits. Keep `livetv.networkPolicy` unchanged.

- [ ] **Step 4: Run** `go test ./test/guards/livetv/ ./test/guards/ -run 'LiveTV|ChartAndKustomizeAgree'` and expect PASS.

- [ ] **Step 5: Commit** with `feat(installers): the livetv Service has no selector; the manager knows its pod IP`.

### Task 9 (B2): placement profiles in the chart

**Files:**

- Modify:
  - `charts/clustarr/values.yaml` (add `placement` at top level with §3's values and `enabled: false`; `placement: workers` on each agent block and markers; `placement: controlPlane` on manager and ui)
  - `charts/clustarr/values.schema.json`: add the `placement` object, `placement` (enum `controlPlane`, `workers`) on manager, ui, markers, agentBlock, `plexDatabase` and `postgres`; add `manager.replicas` (integer ≥ 1); and keep `additionalProperties: false`
  - `charts/clustarr/templates/_helpers.tpl`: the new `clustarr.placement`, used inside `clustarr.workload` in place of the three `with .values.nodeSelector|tolerations|affinity` blocks; `clustarr.validate` gains the replicas check
  - `charts/clustarr/templates/deployments.yaml`: manager `replicas` and `strategy` from the profile; ui the same; `--worker-placement` arg from `placement.profiles.workers` when enabled
  - `charts/clustarr/templates/markers.yaml` (use the helper)
- Create:
  - `charts/clustarr/templates/pdb.yaml` (one PDB per `onePerNode` Deployment, `maxUnavailable: 1`)
  - `test/guards/placement_test.go`

**Interfaces:**

- Produces:
  - Helm helper `clustarr.placement`, called with `(dict "root" $ "values" <component values> "component" <name>)`. It emits `nodeSelector`, `affinity` and `tolerations` at pod-spec indentation 6.
  - `clustarr.placementReplicas`, with the same dict, returns the replicas: the component's `replicas`, or else the profile's when the profile is `onePerNode`, or else the caller's default.

- [ ] **Step 1: Write the failing tests** (`installtest.RenderChart`).
  - `TestPlacementOffLeavesTheDefaultRenderAlone`: the default render has no `node-role.kubernetes.io/control-plane` string, no PodDisruptionBudget, manager `replicas: 1` with `maxSurge: 1` / `maxUnavailable: 0`, and no `--worker-placement`.
  - `TestPlacementProfilesRenderPerComponent`: with `--set placement.enabled=true`:
    - The manager and ui Deployments have `replicas: 3`, `maxSurge: 0`, `maxUnavailable: 1`.
    - They have required `podAntiAffinity` on `kubernetes.io/hostname` selecting their own `clustarr.selectorLabels`.
    - Every required node term contains `node-role.kubernetes.io/control-plane Exists`, and they carry the control-plane toleration.
    - Each has a PDB with `maxUnavailable: 1`.
    - Every agent Deployment's terms contain both workers expressions.
    - markers' single term is `[<its own expressions>…, control-plane DoesNotExist, arch In [amd64]]`. Set `markers.affinity` to the talos value with `intel.feature.node.kubernetes.io/gpu DoesNotExist` to prove the AND.
    - The manager args contain `--worker-placement=` whose JSON equals `placement.profiles.workers` minus `onePerNode`.
  - `TestReplicasAboveTheProfileFailTheRender`: `--set placement.enabled=true --set manager.replicas=4` fails with stderr containing `manager.replicas`.
  - `TestMultiTermAffinityGetsTheProfileInEveryTerm`: `ui.affinity` with two required terms gives two terms, each with the control-plane expression.

- [ ] **Step 2: Run** `go test ./test/guards/ -run 'Placement|Replicas|MultiTerm'` and expect FAIL.

- [ ] **Step 3: Implement** the helper, the values, the schema, the PDB template and the `validate` check. Update `test/guards/installers_topology_test.go:70,78` only if the default render changed. It must not.

- [ ] **Step 4: Run** `helm lint --strict charts/clustarr`, then `go test ./test/guards/`. Expect lint clean and the guards PASS: `installers_topology`, `installers_workloads`, `chart_rbac`, `ui_rbac`.

- [ ] **Step 5: Commit** with `feat(chart): control-plane and workers placement profiles`.

### Task 10 (B3): CNPG Clusters take placement and the §5.1 replication settings

**Files:**

- Modify:
  - `charts/clustarr/templates/plex-db.yaml`
  - `charts/clustarr/templates/postgres-cluster.yaml`
  - `charts/clustarr/values.yaml`:
    - `plexDatabase.placement: controlPlane`
    - `plexDatabase.synchronous: {method: any, number: 1, dataDurability: preferred}`
    - `plexDatabase.maxSlotWalKeepSize: 2GB`
    - `plexDatabase.primaryUpdateMethod: switchover`
    - `postgres.cluster.placement: workers`
  - `charts/clustarr/values.schema.json`
- Test: `test/guards/postgres_test.go` (extend)

- [ ] **Step 1: Write the failing tests.**
  - `TestPlexDatabaseReplicationSettings`: with `cluster-plex.enabled=true` and `placement.enabled=true`, the `-plex-db` Cluster has:
    - `instances: 3`;
    - `affinity.enablePodAntiAffinity: true`, `podAntiAffinityType: required`, `topologyKey: kubernetes.io/hostname`;
    - `affinity.nodeAffinity` required term with control-plane `Exists`, and `affinity.tolerations` with the control-plane toleration;
    - `postgresql.synchronous == {method: any, number: 1, dataDurability: preferred}` and no `failoverQuorum`;
    - `postgresql.parameters.max_slot_wal_keep_size == "2GB"`, with the existing `jit` parameter kept;
    - `primaryUpdateStrategy: unsupervised`, `primaryUpdateMethod: switchover`.
  - `TestPlexDatabaseWithPlacementOffIsUnchanged`: `placement.enabled=false` renders `instances` from `plexDatabase.instances` (1) and no `affinity` or `synchronous`.
  - `TestReleaseIndexPostgresOnWorkers`: `postgres.enabled=true`, `placement.enabled=true` gives a `-postgres` Cluster whose `affinity.nodeAffinity` has both workers expressions, and `podAntiAffinityType` left unset (preferred).

- [ ] **Step 2: Run** `go test ./test/guards/ -run 'PlexDatabase|ReleaseIndex'` and expect FAIL.

- [ ] **Step 3: Implement.** Render CNPG's `affinity` from the profile with a small helper, `clustarr.cnpgAffinity`, beside `clustarr.placement`. CNPG takes a full `nodeAffinity`, not expressions. `synchronous` and the update method render only when placement is enabled.

- [ ] **Step 4: Run** `helm lint --strict charts/clustarr` and the tests, and expect PASS.

- [ ] **Step 5: Commit** with `feat(chart): the Plex database runs three quorum-synchronous instances on the control plane`.

### Task 11 (B4): render-time checks for the literal subchart placements

**Files:**

- Modify: `charts/clustarr/templates/_helpers.tpl` (`clustarr.validate`)
- Test: `test/guards/placement_test.go` (extend)

- [ ] **Step 1: Write the failing tests.**
  - `TestNATSPlacementMatchesTheControlPlaneProfile`: with `placement.enabled=true` and `nats.config.cluster.enabled=true`, a render without `nats.podTemplate.merge.spec.affinity` containing the control-plane `Exists` expression fails, with stderr naming `nats.podTemplate.merge.spec.affinity`. With it set, the render succeeds.
  - `TestLoadBalancerWorkloadsStayOnTheControlPlane`: with `placement.enabled=true` and `cluster-plex.enabled=true`, a render whose `cluster-plex.plex.affinity` lacks that expression fails, naming `cluster-plex.plex.affinity`. cluster-plex's Service is always a LoadBalancer.
  - `TestNatsBoxOnWorkers`: same shape for `nats.natsBox.podTemplate.merge.spec.affinity` and the workers expressions.

- [ ] **Step 2: Run** them and expect FAIL.

- [ ] **Step 3: Implement** the three `fail` checks. They walk `.nodeAffinity.requiredDuringSchedulingIgnoredDuringExecution.nodeSelectorTerms[*].matchExpressions` with `range`. Every term must contain the expression.

- [ ] **Step 4: Run** the tests and `helm lint --strict charts/clustarr`, and expect PASS.

- [ ] **Step 5: Commit** with `feat(chart): NATS and Plex placements are checked at render time`.

### Task 12 (B5): manager memory knobs in the chart

**Files:**

- Modify:
  - `charts/clustarr/values.yaml` / `values.schema.json`: `manager.pprofBindAddress` (string, default `""`), `manager.gogc` (integer, default unset), `manager.goMemLimit` (string; overrides the computed `GOMEMLIMIT` when set), `manager.watchReleaseNamespace` (bool, default `false`)
  - `charts/clustarr/templates/deployments.yaml` (args `--pprof-bind-address`, `--watch-namespace={{ .Release.Namespace }}`; env `GOGC`)
  - `charts/clustarr/templates/_helpers.tpl` (`clustarr.gomemlimit` honours `goMemLimit`)
- Test: `test/guards/manager_values_test.go` (extend)

- [ ] **Step 1: Write the failing tests.**
  - `TestManagerMemoryKnobs`: `--set manager.pprofBindAddress=127.0.0.1:6060,manager.gogc=50,manager.goMemLimit=1GiB,manager.watchReleaseNamespace=true` renders:
    - args `--pprof-bind-address=127.0.0.1:6060` and `--watch-namespace=<release namespace>`;
    - env `GOGC=50` and `GOMEMLIMIT=1073741824`.
  - The default render has none of these, and `GOMEMLIMIT` is unchanged (80% of the limit).

- [ ] **Step 2: Run** them and expect FAIL.

- [ ] **Step 3: Implement** it.

- [ ] **Step 4: Run** `go test ./test/guards/ -run Manager` and `helm lint --strict charts/clustarr`, and expect PASS.

- [ ] **Step 5: Commit** with `feat(chart): manager pprof, GOGC, GOMEMLIMIT and namespace scoping values`.

### Task 13 (B6): cluster-plex placement keys (cluster-plex repo)

**Files** (in `/home/appkins/src/mediactl/cluster-plex`):

- Modify: `charts/cluster-plex/values.yaml`: `maintenance.nodeSelector|affinity|tolerations`, `remux.…`, `proxy.…` (each `{}` / `[]` by default)
- Modify: the maintenance CronJob, remux and proxy templates, which render them the way `plex` does (`templates/plex.yaml`)
- Test: `test/manifests` (that repo's chart test, extended) and `helm lint --strict charts/cluster-plex`

- [ ] **Step 1:** Write a failing test: setting each block's `affinity` reaches its pod template, and the default render is unchanged.
- [ ] **Step 2:** Run that repo's chart test and expect FAIL.
- [ ] **Step 3:** Implement it.
- [ ] **Step 4:** Run it and `helm lint --strict charts/cluster-plex`, and expect PASS.
- [ ] **Step 5:** Commit in cluster-plex with `feat(chart): placement keys for maintenance, remux and proxy`.

Publishing the chart and bumping clustarr's `Chart.yaml` dependency is Task D7. It belongs to the controlling session, with the owner's OK.

### Task 14 (B7): multi-arch clustarr image

**Files:**

- Modify: `Makefile`. Add `docker-buildx-clustarr`:

  ```sh
  docker buildx build $(LOCAL_CONTEXTS) --platform linux/amd64,linux/arm64 --build-arg VERSION=$(IMAGE_VERSION) -f images/Dockerfile.clustarr --target clustarr -t $(IMG) --push .
  ```

  Add `UPX_ARM64 ?= 1`, passed as `--build-arg UPX_ARM64`.
- Modify: `images/Dockerfile.clustarr`. Skip UPX when `TARGETARCH=arm64` and `UPX_ARM64=0`.

- [ ] **Step 1:** Run `make -n docker-buildx-clustarr` and expect the command above, with `--platform linux/amd64,linux/arm64`.
- [ ] **Step 2:** Run `docker buildx build --platform linux/arm64 -f images/Dockerfile.clustarr --target clustarr .` (no push). Expect it builds, and `docker run --rm --platform linux/arm64 <id> /usr/bin/manager --version` prints the version under qemu.
- [ ] **Step 3:** Commit with `build: docker-buildx-clustarr builds the clustarr image for amd64 and arm64`.

---

## Part C: the deferred test run

### Task 15 (C1): the single envtest run

- [ ] **Step 1:** Run `make test` (it sets `KUBEBUILDER_ASSETS` and the rest). Expect PASS, including:
  - `TestLiveTVServiceRoutesToTheLeader`
  - `TestStandbyWarmsTheFailoverKinds`
  - every guard
- [ ] **Step 2:** Bring any failure to the owner with its output. Never merge around it.

---

## Part D: talos rollout

**Controlling session only, with the owner's OK for each step.** Each step is spec §9's row:

- Upgrade with `kubectl --context admin@talos apply --server-side -f <chart commit>/config/crd/bases/`, then `HELM_PLUGINS=…/talos/helm-plugins helm --kube-context admin@talos -n clustarr-system upgrade clustarr <chart dir> -f …/talos/talos-values.yaml --post-renderer pin-amd64`.
- Update `talos-values.yaml` after each step, and note the revision in the `clustarr-on-talos` memory.
- **Gate for D3, D4 and D6: the control-plane NICs' TX stalls** (spec §10's first risk). These steps wait until the Pi 5 `macb` stalls are fixed, or until etcd is retuned and its leader changes rise by no more than a few an hour. Count the stalls on each node before and after each step (`talosctl -n <node> dmesg | grep -c 'TX stall'`); revert a step that raises them. D1, D2, D5 and D7 move nothing onto the control planes and may go ahead.

| Task | Change (talos values) | Verify | Revert |
| --- | --- | --- | --- |
| D1 | Images from Parts A and B; the clustarr image via `make docker-buildx-clustarr`. Then on each of .32, .33 and .34 run `kubectl run arm64-check --rm -it --restart=Never --overrides '{"spec":{"nodeName":"<node>"}}' --image <clustarr image> -- /usr/bin/manager --version`, and the same for `/usr/bin/ui` | Exits 0 on all three; else rebuild with `UPX_ARM64=0` | n/a |
| D2 | `placement.enabled: true`, everything on `workers` (manager and ui `placement: workers` for now; no database placement). Every pod template gains affinity, so every Deployment rolls and every transcode pool drains and is recreated; no pod changes node. Workers expressions in `nats.natsBox`. `manager.pprofBindAddress: 127.0.0.1:6060`, `gogc: 50`, `goMemLimit: 1GiB`, `watchReleaseNamespace: true` | No pod moves. Graft and pool pods carry the workers expressions. After 1 h, `kubectl port-forward` 6060 to save `heap` and `allocs` profiles into the session scratchpad; 24 h later compare RSS and GC CPU with 716 MiB / 56m | Restore the values |
| D3 | (Task A7 lands; image bump.) `plexDatabase.placement: controlPlane` (instances from the profile; the primary moves by switchover), cluster-plex's Plex affinity `cnpg.io/instanceRole: primary` | 3 instances Ready; `psql -c 'show synchronous_standby_names'` lists both standbys; lag about 0; `kubectl cnpg promote` drill while browsing Plex | `placement.enabled` back on D2's values with `plexDatabase.instances: 1` |
| D4 | Literal NATS placement (§5.2), then one server at a time: nats-2, nats-1, nats-0 (claim to 30 GiB) | Per server: `/healthz` ok on all three, no stream without a leader (`/jsz?streams=true`), manager logs `topology ensured` | Revert values; moved servers stay |
| D5 | Release-index Postgres to workers: `postgres.cluster.placement: workers`, `primaryUpdateMethod: switchover`, `instances: 2`; once switched over, `cnpg destroy … 1`, then `instances: 1` | Blocklist and release row counts equal before and after; index RPCs answer | Promote back |
| D6 | `ui.placement: controlPlane`, then `manager.placement: controlPlane`; drop their `kubernetes.io/arch: amd64` nodeSelectors from talos-values (the control planes are arm64) | ui 3 Ready, `/art/search` URLs work from any replica; the `livetv` EndpointSlice names the leader; tune a channel, delete the leader pod, tune again within a minute; standby RSS about 300–400 MiB | `placement: workers` |
| D7 | Publish cluster-plex's chart (Task B6) with `make chart-push`; bump `charts/clustarr/Chart.yaml`'s cluster-plex version; set maintenance and remux to the workers profile and the proxy (behind its own LoadBalancer) to the control-plane profile | Their pods carry the workers expressions | Previous chart version |
| D8 | Upgrade once without `--post-renderer` | `helm template` with and without the post-renderer is byte-identical; every pod is where it was | Re-add `--post-renderer pin-amd64` |

During D3, D4 and D6, watch `etcd_disk_wal_fsync_duration_seconds` p99 under 10 ms and `etcd_disk_backend_commit_duration_seconds` p99 under 25 ms. If they breach, revert NATS first (spec §10).
