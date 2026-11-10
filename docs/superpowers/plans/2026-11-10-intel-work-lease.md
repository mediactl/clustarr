# Intel Work Lease Implementation Plan (clustarr half of the CPU video route)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** clustarr's transcode admission publishes how much intel-class work it holds in a Lease, `clustarr-intel-work`, so cluster-plex can send Plex's conversions to its CPU pool while clustarr has encodes waiting or running.

**Architecture:** The admission pass already knows the intel class's slots in use and the Planned files assigned to intel; it now counts them into `PassResult.IntelWork`. A small writer owned by the admission reconciler publishes the count into a Lease in the manager's own namespace whenever it changes, and a leader-only runnable renews it every 10 s while it is above zero, so a dead manager reads as idle within 30 s. No new RBAC: the chart's leader-election Role already grants every verb on Leases in the release namespace.

**Tech Stack:** Go, controller-runtime (the manager's client, `retry.RetryOnConflict`), Helm.

**Spec:** `../cluster-plex/docs/superpowers/specs/2026-11-10-cpu-video-route-design.md`, §1 "The signal" (approved 2026-11-10). The cluster-plex half is `cluster-plex/docs/superpowers/plans/2026-11-10-cpu-video-route.md`.

## Global Constraints

- The manager is the only writer of every Kubernetes object; this Lease is its own, in its own namespace (`POD_NAMESPACE`), written through `mgr.GetClient()`; agents and workers never touch it (ADR-0019).
- Spec values: Lease name `clustarr-intel-work` (the flag names it; the chart's `manager.intelWorkLease` is `<namespace>/<name>`, empty for none), annotation `clustarr.io/intel-work: "<n>"`, renewed every 10 s while n > 0, `leaseDurationSeconds` 30, holder the pod's name (`POD_NAME`); a Lease not renewed for 30 s reads as 0 on the other side.
- n counts the intel class's files that hold or want a slot: Queued, Running and Swapping (the ledger's `slotPhase`) files of class intel, plus the pass's Planned candidates assigned to intel (admitted or not) and the holds whose message is `WaitingForGPU` (a `gpu` file waiting class-less); never grafts, never cpu or nvidia.
- Admission never waits on the write: a failed write is logged and retried at the next renewal.
- Commits on `main` by pathspec; `git add` new files first; no `make test` (envtests) until the final run per the owner's standing rule, so `go test ./app/transcode/admission/... ./internal/cli/manager/...` is the gate here.

## Review Focus

- The flag names a namespace other than the manager's: the write would be refused by RBAC; refuse the flag at start instead (Task 3, `TestTheIntelWorkLeaseMustBeInTheManagersNamespace`).
- A write that fails (RBAC, a conflict storm) never fails or delays a pass (Task 2's `TestAFailedPublishIsLoggedNotReturned`, Task 3's reconcile path).
- A leader change: the new leader's first pass publishes under its own holder identity and the old entry is overwritten, not summed (Task 2, `TestANewHolderReplacesTheOld`).
- The count dropping to zero is published at once, not at the next renewal, so cluster-plex stops routing to the CPU within a pass (Task 2, `TestZeroIsPublishedAtOnce`).
- An intel budget of zero (`--slots intel=0`) or no Intel node: the count is zero, never the held files (Task 1, `TestNoIntelBudgetIsNoIntelWork`).

---

### Task 1: Counting the intel class's work in a pass

**Files:**
- Create: `app/transcode/admission/intelwork.go`
- Modify: `app/transcode/admission/pass.go` (`PassResult`, `Pass`)
- Test: `app/transcode/admission/intelwork_test.go`

**Interfaces:**
- Produces: `PassResult.IntelWork int`; `func intelWork(queued []candidate, running []Slot, holds []hold, budget map[string]int32) int` (unexported; 0 when `budget[intel]` is 0 or absent).

- [ ] **Step 1: Write the failing test** in `app/transcode/admission/intelwork_test.go` (package `admission`, the fixtures of `videolease_test.go` at hand):

```go
func TestIntelWorkCountsTheIntelClassInHandAndWaiting(t *testing.T) {
	intelSlot := func(k string) Slot { return Slot{Key: k, Hardware: string(intel)} }
	running := []Slot{intelSlot("a"), intelSlot("b"), {Key: "c", Hardware: "cpu"}, {Key: "d", Hardware: "nvidia"}}
	queued := []candidate{{slot: intelSlot("e")}, {slot: Slot{Key: "f", Hardware: "cpu"}}}
	holds := []hold{{msg: WaitingForGPU}, {msg: "waiting: the pool is unhealthy"}}
	budget := map[string]int32{"intel": 2, "cpu": 2}
	assert.Equal(t, 4, intelWork(queued, running, holds, budget), "two running, one queued, one held gpu file")
	assert.Equal(t, 0, intelWork(queued, running, holds, map[string]int32{"cpu": 2}), "no intel budget is no intel work")
	assert.Equal(t, 0, intelWork(queued, running, holds, map[string]int32{"intel": 0}))
}

func TestNoIntelBudgetIsNoIntelWork(t *testing.T) {
	in := passIn() // the file's pass fixture, with no Intel node
	res := ledgerWith(t, in).Pass(in) // the helper the pass tests use to build a rebuilt ledger with files
	assert.Zero(t, res.IntelWork)
}

func TestAPassReportsItsIntelWork(t *testing.T) {
	// as TestRunningIntelEncodesKeepRunningBesideAStream (videolease_test.go) seeds a ledger with
	// running intel encodes and Planned intel files: assert res.IntelWork equals running + Planned-assigned-to-intel.
}
```

- [ ] **Step 2: Run it to make sure it fails**: `go test ./app/transcode/admission/ -run 'IntelWork|NoIntelBudget'` Expected: FAIL, `undefined: intelWork`.

- [ ] **Step 3: Implement** `intelWork` in `intelwork.go`, and in `Pass` (after `admitted := Admit(...)`), set `res.IntelWork = intelWork(queued, running, holds, budget)` where `budget` is the per-class slots the pass computed (the yield-adjusted copy counts as a budget above zero while `l.o.Slots[intel] > 0`: pass `l.o.Slots`, not the yielded copy, so a yield does not read as "no intel").

- [ ] **Step 4: Run the admission suite**: `go test ./app/transcode/admission/...` Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add app/transcode/admission/intelwork.go app/transcode/admission/intelwork_test.go
git commit -m "transcode/admission: a pass reports the intel class's work in hand and waiting (PassResult.IntelWork): running, queued and Planned intel files and the yield's held gpu files" -- app/transcode/admission/intelwork.go app/transcode/admission/intelwork_test.go app/transcode/admission/pass.go
```

---

### Task 2: The Lease writer and its renewer

**Files:**
- Modify: `app/transcode/admission/intelwork.go`
- Test: `app/transcode/admission/intelwork_test.go`

**Interfaces:**
- Produces: `const IntelWorkAnnotation = "clustarr.io/intel-work"`, `IntelWorkRenew = 10 * time.Second`, `IntelWorkStale = 30 * time.Second`; `type IntelWorkLease struct { Client client.Client; Key types.NamespacedName; Identity string; Now func() time.Time; Every time.Duration /* 0: IntelWorkRenew */; Log *slog.Logger; mu sync.Mutex; n int; known bool }`; `func (l *IntelWorkLease) Set(ctx context.Context, n int)` (publishes when n differs from the last published, or on the first call; a failure is logged at Warn and the value kept, so the renewer retries); `func (l *IntelWorkLease) Publish(ctx context.Context) error` (Get, then Create or Update under `retry.RetryOnConflict`: `annotations[IntelWorkAnnotation] = strconv.Itoa(n)`, `spec.holderIdentity = Identity`, `spec.renewTime = now`, `spec.leaseDurationSeconds = 30`); `func (l *IntelWorkLease) Start(ctx context.Context) error` (a ticker of `Every`; publishes while `n > 0`; returns on ctx); `func (l *IntelWorkLease) NeedLeaderElection() bool { return true }`.

- [ ] **Step 1: Write the failing tests** with controller-runtime's fake client (`sigs.k8s.io/controller-runtime/pkg/client/fake`, scheme with `coordinationv1`), a fixed clock `now`:

```go
func TestTheIntelWorkLeaseIsWrittenWhenTheCountMoves(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	l := &IntelWorkLease{Client: c, Key: types.NamespacedName{Namespace: "clustarr-system", Name: "clustarr-intel-work"}, Identity: "clustarr-manager-1", Now: clock.Now}
	l.Set(ctx, 3)
	got := read(t, c, l.Key)
	assert.Equal(t, "3", got.Annotations[IntelWorkAnnotation])
	assert.Equal(t, "clustarr-manager-1", *got.Spec.HolderIdentity)
	assert.EqualValues(t, 30, *got.Spec.LeaseDurationSeconds)
	assert.True(t, got.Spec.RenewTime.Time.Equal(clock.Now()))
	clock.Advance(time.Second)
	l.Set(ctx, 3)
	assert.True(t, read(t, c, l.Key).Spec.RenewTime.Time.Equal(clock.Now().Add(-time.Second)), "an unchanged count writes nothing")
	l.Set(ctx, 5)
	assert.Equal(t, "5", read(t, c, l.Key).Annotations[IntelWorkAnnotation])
}

func TestZeroIsPublishedAtOnce(t *testing.T) { // Set(3) then Set(0): the annotation reads "0" with the new renewTime }

func TestANewHolderReplacesTheOld(t *testing.T) { // two IntelWorkLease values with different Identity on one fake client: the second's Set(2) leaves holder = second, annotation "2", no sum }

func TestAFailedPublishIsLoggedNotReturned(t *testing.T) {
	// a client whose Update returns an error (fake.NewClientBuilder().WithInterceptorFuncs): Set does not panic or block; the next Publish retries with the kept value
}

func TestTheRenewerRenewsWhileWorkIsHeld(t *testing.T) {
	l.Every = 20 * time.Millisecond
	ctx, cancel := context.WithCancel(t.Context()); go func() { _ = l.Start(ctx) }()
	l.Set(ctx, 1)
	time.Sleep(70 * time.Millisecond)
	assert.GreaterOrEqual(t, writes(c), 3, "renewed about every Every")  // count Updates through an interceptor
	l.Set(ctx, 0); n := writes(c); time.Sleep(50 * time.Millisecond)
	assert.Equal(t, n, writes(c), "nothing renews at zero")
	cancel()
	assert.True(t, l.NeedLeaderElection())
}
```

- [ ] **Step 2: Run them to make sure they fail**: `go test ./app/transcode/admission/ -run 'IntelWorkLease|ZeroIsPublished|NewHolder|FailedPublish|Renewer'` Expected: FAIL, `undefined: IntelWorkLease`.

- [ ] **Step 3: Implement** the type and methods in `intelwork.go`; `Publish` mirrors cluster-plex's `lease.Counter.Renew` (`pkg/video/lease/lease.go`) without the per-manager entries: one annotation, one holder.

- [ ] **Step 4: Run**: `go test ./app/transcode/admission/...` Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git commit -m "transcode/admission: IntelWorkLease publishes the pass's intel work into a Lease when it moves and renews it every 10 s while above zero (leader-only runnable)" -- app/transcode/admission/intelwork.go app/transcode/admission/intelwork_test.go
```

---

### Task 3: Wiring: the reconciler, the flag, the chart

**Files:**
- Modify: `app/transcode/admission/controller.go` (`Reconciler.IntelWork`, `Reconcile`, `SetupWithManager`)
- Modify: `app/transcode/manager/register.go` (`Options.IntelWorkLease`, the reconciler's construction)
- Modify: `internal/cli/manager/options.go`, `internal/cli/manager/command.go`, `internal/cli/manager/run.go` (the `--intel-work-lease` flag beside `--video-transcode-lease`)
- Modify: `charts/clustarr/values.yaml` (`manager.intelWorkLease: ""` with a comment), `charts/clustarr/templates/deployments.yaml` (the arg, as `videoTranscodeLease` renders)
- Modify: `CLAUDE.md` (the Live TV conversions paragraph in "Transcoding")
- Test: `app/transcode/admission/intelwork_test.go`, the `internal/cli/manager` test that covers `--video-transcode-lease` parsing

**Interfaces:**
- Consumes: `PassResult.IntelWork` (Task 1), `IntelWorkLease` (Task 2).
- Produces: `Reconciler.IntelWork *IntelWorkLease` (nil: nothing published); `Options.IntelWorkLease types.NamespacedName` on `app/transcode/manager`; `internal/cli/manager` `Options.IntelWorkLease types.NamespacedName` from `--intel-work-lease` (parsed by `parseLeaseRef`, as `--video-transcode-lease` is); a start-time refusal `"--intel-work-lease: the Lease must be in the manager's namespace <POD_NAMESPACE> (the leader-election Role grants its writes there)"` when the namespace differs; chart value `manager.intelWorkLease` rendered as `--intel-work-lease=<value>`.

- [ ] **Step 1: Write the failing tests**: in `intelwork_test.go`, `TestAPassPublishesItsIntelWork` (a `Reconciler{Ledger: ..., IntelWork: &IntelWorkLease{Client: fake...}}` run through one `Reconcile` with the `RebuildGate` closed as `pass_cost_test.go` does; assert the Lease exists with the pass's count); in the `internal/cli/manager` flag test, `TestTheIntelWorkLeaseMustBeInTheManagersNamespace` (`--intel-work-lease=other/clustarr-intel-work` with `POD_NAMESPACE=clustarr-system` fails with the message above; `clustarr-system/clustarr-intel-work` parses).

- [ ] **Step 2: Run them to make sure they fail**.

- [ ] **Step 3: Implement**: in `Reconcile`, after `res := r.Ledger.Pass(in)`: `if r.IntelWork != nil { r.IntelWork.Set(ctx, res.IntelWork) }`; in `SetupWithManager`, `if r.IntelWork != nil { if err := mgr.Add(r.IntelWork); err != nil { return err } }`; in `register.go`, build it with `Identity: os.Getenv("POD_NAME")` when `o.IntelWorkLease.Name != ""`; the flag, option and namespace check in `internal/cli/manager`; the chart value and arg.

- [ ] **Step 4: Run**: `go test ./app/transcode/admission/... ./app/transcode/manager/... ./internal/cli/manager/...` Expected: PASS. `helm lint --strict charts/clustarr` Expected: clean; `helm template clustarr charts/clustarr --set manager.intelWorkLease=clustarr-system/clustarr-intel-work | grep -- '--intel-work-lease='` Expected: the arg.

- [ ] **Step 5: CLAUDE.md**: in the Transcoding section's Live TV paragraph (after the `VideoAllowance` sentences), one sentence: the manager publishes the intel class's work in hand and waiting in Lease `clustarr-intel-work` (`clustarr.io/intel-work`, `--intel-work-lease`, chart `manager.intelWorkLease`), which cluster-plex reads to route Plex's conversions to its CPU pool (cluster-plex's 2026-11-10 CPU video route design); off by default.

- [ ] **Step 6: Lint** the touched packages: `golangci-lint-v2 run ./app/transcode/... ./internal/cli/manager/...` Expected: 0 issues.

- [ ] **Step 7: Commit**

```bash
git commit -m "transcode admission, manager cli, chart: --intel-work-lease / manager.intelWorkLease publishes the intel class's work into a Lease in the manager's namespace for cluster-plex's CPU video route; refused outside the manager's namespace" -- app/transcode/admission/controller.go app/transcode/admission/intelwork_test.go app/transcode/manager/register.go internal/cli/manager/options.go internal/cli/manager/command.go internal/cli/manager/run.go charts/clustarr/values.yaml charts/clustarr/templates/deployments.yaml CLAUDE.md
```

---

### Task 4: Proof on talos (with the cluster-plex half)

Not code: set `manager.intelWorkLease: clustarr-system/clustarr-intel-work` in cluster-bootstrap's values with the clustarr release that carries this; `kubectl -n clustarr-system get lease clustarr-intel-work -o yaml` shows the annotation moving with `clustarr_transcode_jobs_active{class="intel"}` and the Planned intel files, renewed every 10 s while above zero and reading `"0"` within a pass of the last encode ending; cluster-plex's `clusterplex_video_conversions_total{pool="cpu"}` rises while it is above zero (the cluster-plex plan's Task 7).
