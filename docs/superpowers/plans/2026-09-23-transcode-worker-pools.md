# Transcode Worker Pools Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Transcodes run in a separate `squasharr-worker` binary and image that hold no
Kubernetes credentials.
- **Pools.** Workers run in one long-lived Job per (TranscodeProfile, hardware class). squasharr
  sizes each pool, suspends it to zero when idle, and reshapes it while suspended.
- **Reporting.** Workers report on a NATS stream. squasharr consumes that stream to set status
  and decide each job's next step: requeue, fall back to CPU, or block.
- **`hardware: auto`.** This new default prefers GPU pools and falls back to CPU. A GPU pool is a
  Job created with `.spec.scheduling.schedulingConstraints.topology` set to the class's GPU node
  label.

**Architecture:**
- **Dispatch.** squasharr's TranscodeJob controller admits Planned jobs into slots. For each one
  it chooses a class (for `auto`: GPU when a labelled GPU node and a free GPU slot exist,
  otherwise CPU), plans for that class, and publishes the task on `CLUSTARR_WORK_SQUASHARR`.
- **Pools.** squasharr applies one Job per (profile, class) under the field manager
  `squasharr-pool`.
  - Every apply sends `gang.minCount = parallelism`.
  - A GPU pool also sends its immutable `schedulingConstraints` topology key.
  - A pool suspends when it has nothing dispatched.
  - A profile edit waits for the pool to drain, then applies while suspended.
- **Worker.** Pulls one task at a time, holds a KV lease that the server expires, and renews it
  with `InProgress`. It runs the encode, verify and swap sequence, and publishes `claimed`,
  `progress` and `finished` events to `clustarr.work.transcode.result.<jobUID>`. It acks the task
  only after `finished` is stored.
- **Status.** squasharr's `squasharr-transcode-results` consumer and its reconciler both write
  `TranscodeJob.status`, through one compare-and-swap function. The consumer applies the
  next-step table (spec §18.3).

**Tech Stack:**
- Go 1.27
- controller-runtime v0.25.1
- `k8s.io/api` v0.37.0: `batch/v1` `JobSpec.Scheduling`, `scheduling/v1alpha3`
- nats.go `jetstream`, and nats-server v2.15 (embedded in tests)
- `pkg/events` (`natsbus`, `membus`)
- envtest 1.37.0
- ffmpeg (tests that need it skip without it)

**Spec:** `docs/superpowers/specs/2026-09-23-transcode-worker-design.md`. Read §17 and §18 first:
§18 wins over §17, and both win over §1-§16. ADR:
`docs/adr/0009-transcode-worker-pools-over-jetstream.md`.

## Global Constraints

- Every new Go file starts with the GPL-3.0 header from `hack/boilerplate.go.txt`.
- **Status writes:**
  - All status writes go through `pkg/k8s.PatchStatus` (`squasharr/status.Patch` and
    `PatchCAS`).
  - `.Status().Update()` and `.Status().Patch()` are banned by forbidigo. The only exception is a
    test that simulates the Job controller or kubelet, with a `//nolint:forbidigo` reason.
- **One TranscodeJob status write path.** After Task 10, every write to `TranscodeJob.status` goes
  through `(*Reconciler).writeStatus` or `patchCAS`. That means a fresh read through the uncached
  reader, then an apply that carries the read `resourceVersion` (the
  `catalogarr/worker/grab/kindops.go:181` pattern), under `k8s.ManagerSquasharr`. A Conflict is
  redone from a fresh read, never ignored.
- **Server-side apply:**
  - Every apply is a complete declaration of everything its manager owns (CLAUDE.md
    "Server-side apply replaces…").
  - `WithConditions` appends, so set conditions exactly once per apply.
- **Pool applies:**
  - Every pool apply sends `spec.scheduling.schedulingPolicy.gang.minCount = parallelism` (≥ 1).
  - A GPU pool's applies also send the `schedulingConstraints.topology` key it was created with.
  - Both are immutable once set, so leaving either out of an apply is a rejected write (spec
    §3, §18.5).
  - Nothing else goes under `.spec.scheduling`.
  - `parallelism` is never 0. A pool's zero is `suspend: true`.
- **GPU node labels.** They default to `nvidia.com/gpu.present` and
  `intel.feature.node.kubernetes.io/gpu`, with value `true`. They are overridden only through
  `--gpu-node-label-nvidia` and `--gpu-node-label-intel` (`pool.Config`), never hard-coded
  anywhere else.
- **NATS names:**
  - Every NATS KV key is built through `events.KVKeyToken`.
  - Every subject token is built through `pkg/events`'s `tok`.
  - Subjects and consumer names carry profile **UIDs**, never names.
- **Worker binary imports.** `cmd/squasharr-worker` and its non-test dependencies must not
  import `k8s.io/client-go/...`, `sigs.k8s.io/controller-runtime/pkg/client`,
  `sigs.k8s.io/controller-runtime/pkg/manager`, `github.com/mediactl/clustarr/pkg/k8s` or
  `github.com/mediactl/clustarr/pkg/obs` (the top-level package). The `api/...` packages are
  allowed: they import only `controller-runtime/pkg/scheme`.
- **Worker exit codes.** The worker process never exits 0. Its exit codes are
  `WorkerExitRetriable = 2`, `WorkerExitMisconfigured = 3` and `WorkerExitDrained = 10`.
- **Logging.** Use `slog` through `context` (`pkg/obs/logging.FromContext`), with no
  package-level loggers. Metrics use the `clustarr_` prefix and never carry a title, path or name
  label.
- **Building.** Build with `make build`, never bare `go build` (it drops a binary in the repo
  root). Never run `go get` or `go mod tidy`; no new modules are needed (`clockwork`,
  `nats-server`, `jetstream` and `pflag` are already in `go.mod`).
- **Envtest.** Export the assets once per shell:
  `export KUBEBUILDER_ASSETS="$($(go env GOPATH)/bin/setup-envtest use 1.37.0 -p path)"`.
  A suite that finishes in milliseconds skipped.
- **Git.** Other sessions share this checkout.
  - Commit with a pathspec: `git commit -m '…' -- <paths>`.
  - Never `git stash`, `git reset --hard` or push.
  - Find commits by subject, never by SHA.
- **After touching generated inputs:**
  - API types: run `make generate manifests`.
  - RBAC markers: run `make manifests`, then copy the regenerated role into its BEGIN/END sentinel
    block in `charts/clustarr/templates/rbac.yaml`.

## Review Focus

The five input classes and failure modes most likely to bite a user that no task's happy path
covers. Each has a named test in the task that owns it.

1. **JSON drops a `false` policy pointer.** A task that round-trips JSON must keep
   `policy.replaceSource: false` and `policy.recycleBin: false`. These are pointer fields whose
   zero value means "true". Test: `TestTaskJSONKeepsFalsePolicyPointers` (Task 3).
2. **Pool template drift.** Two tests cover it:
   - Apiserver defaulting must not read as drift forever: `TestClassifyIgnoresApiserverDefaulting`
     (Task 8).
   - A profile edited while its pool runs must never produce a rejected apply:
     `TestProfileEditWhileRunningNeverRejectsAnApply` (Task 9).
3. **A re-dispatched job cancelled by its own stale marker.** A job withdrawn and re-dispatched
   inside the 90s lease TTL must not be dropped by its first attempt's `cancelled` marker.
   Test: `TestServeReplacesACancelledLeaseFromAnEarlierAttempt` (Task 6).
4. **SIGTERM right after a finished encode.** SIGTERM arriving after `Process` succeeded but
   before settlement must still publish `finished` and ack, not nak and redo the work.
   Test: `TestServeFinishesASucceededTaskDespiteDrain` (Task 6).
5. **A result event racing a reconcile.** This must never roll status back: the loser's
   compare-and-swap conflicts and is redone from a fresh read.
   Test: `TestAResultEventRacingAReconcileIsNotLost` (Task 10).

## Order and ownership

Run the tasks **in order**, one at a time. Several of them edit
`squasharr/controller/transcodejob/controller.go`, `squasharr/run.go` and `cmd/clustarr`, so no
two tasks run in parallel.

- **Tasks 10 and 11 land back to back.** After Task 10 no pool exists yet, so dispatched tasks
  wait in the queue. That is fine inside an unreleased branch, and nothing is pushed between them.
- **Task 13 uses Task 12's `withdraw`.**

---

### Task 1: The transcode stream, subjects, consumers and lease bucket in `pkg/events`

**Files:**
- Modify: `pkg/events/subjects.go`: the constants and builders below.
- Modify: `pkg/events/topology.go`: the stream in `defaultStreams()`,
  `squasharr-transcode-results` in `defaultConsumers()`, the lease bucket in `defaultBuckets()`,
  and `TranscodeTaskConsumer`.
- Test: `pkg/events/transcode_topology_test.go` (package `events`, internal).

**Interfaces:**
- Produces (every later task uses these):
  ```go
  const StreamWorkSquasharr      = "CLUSTARR_WORK_SQUASHARR"
  const FilterWorkSquasharr      = "clustarr.work.transcode.>"
  const FilterTranscodeResults   = "clustarr.work.transcode.result.>"
  const ConsumerSquasharrResults = "squasharr-transcode-results"
  const BucketTranscodeLeases    = "clustarr-transcode-leases"
  const TranscodeLeaseTTL        = 90 * time.Second
  func WorkTranscodeTaskSubject(profileUID, class, jobUID string) string
  func WorkTranscodeResultSubject(jobUID string) string
  func FilterTranscodeTasks(profileUID, class string) string
  func TranscodeTaskConsumerName(profileUID, class string) string
  func TranscodeTaskConsumer(profileUID, class string) ConsumerSpec
  func TranscodeLeaseKey(jobUID string) string
  func MsgIDForTranscodeTask(jobUID string, attempt int32) string
  func MsgIDForTranscodeEvent(jobUID string, attempt int32, delivery, seq uint64) string
  ```

- [ ] **Step 1: Write the failing test**

```go
package events

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// matches is NATS subject matching: "*" is one token, a trailing ">" the rest.
func matches(filter, subject string) bool {
	f, s := strings.Split(filter, "."), strings.Split(subject, ".")
	for i, tok := range f {
		if tok == ">" {
			return len(s) > i
		}
		if i >= len(s) || (tok != "*" && tok != s[i]) {
			return false
		}
	}
	return len(f) == len(s)
}

func TestTranscodeTopology(t *testing.T) {
	top := Default()
	require.NoError(t, top.Validate())

	st, ok := top.Stream(StreamWorkSquasharr)
	require.True(t, ok, "CLUSTARR_WORK_SQUASHARR is missing from Default()")
	assert.Equal(t, RetentionWorkQueue, st.Retention)
	assert.Equal(t, DiscardNew, st.Discard, "a full queue must refuse a task, not drop an admitted one")
	assert.False(t, st.AllowMsgSchedules, "DiscardNew cannot be combined with schedules")

	task := WorkTranscodeTaskSubject("6f1c-uid", "nvidia", "a1b2-uid")
	result := WorkTranscodeResultSubject("a1b2-uid")
	assert.Len(t, strings.Split(task, "."), 7, "UIDs only: a profile name with dots cannot change the shape")
	for _, subj := range []string{task, result} {
		got, ok := top.StreamForSubject(subj)
		require.True(t, ok, subj)
		assert.Equal(t, StreamWorkSquasharr, got.Name)
	}

	c := TranscodeTaskConsumer("6f1c-uid", "nvidia")
	require.NoError(t, c.Subscription().Validate())
	assert.Regexp(t, `^[A-Za-z0-9_-]+$`, c.Name)
	assert.True(t, matches(c.Filters[0], task))
	assert.False(t, matches(c.Filters[0], WorkTranscodeTaskSubject("6f1c-uid", "cpu", "a1b2-uid")),
		"one class's pool must never receive another class's task")
	assert.False(t, matches(c.Filters[0], result))

	rc, ok := top.Consumer(ConsumerSquasharrResults)
	require.True(t, ok, "squasharr-transcode-results is missing from Default()")
	assert.Equal(t, StreamWorkSquasharr, rc.Stream)
	assert.Equal(t, 1, rc.MaxAckPending, "one event at a time: status writes stay ordered")
	assert.True(t, matches(rc.Filters[0], result))
	assert.False(t, matches(rc.Filters[0], task), "work-queue filters must not overlap")

	var leases *BucketSpec
	for i := range top.Buckets {
		if top.Buckets[i].Name == BucketTranscodeLeases {
			leases = &top.Buckets[i]
		}
	}
	require.NotNil(t, leases)
	assert.Equal(t, TranscodeLeaseTTL, leases.TTL)
	assert.Equal(t, uint8(1), leases.History)

	assert.True(t, ValidKVKey(TranscodeLeaseKey("a1b2-uid")))
	assert.Equal(t, "a1b2-uid/3", MsgIDForTranscodeTask("a1b2-uid", 3))
	assert.Equal(t, "a1b2-uid/2/3/4", MsgIDForTranscodeEvent("a1b2-uid", 2, 3, 4))
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./pkg/events/ -run TestTranscodeTopology`
Expected: FAIL to compile, "undefined: StreamWorkSquasharr".

- [ ] **Step 3: Add the constants and builders to `pkg/events/subjects.go`**

Add each constant to its block:

| Constant | Block |
|---|---|
| `StreamWorkSquasharr = "CLUSTARR_WORK_SQUASHARR"` | Stream, line 28 |
| `FilterWorkSquasharr = "clustarr.work.transcode.>"` | Filter, line 83 |
| `FilterTranscodeResults = "clustarr.work.transcode.result.>"` | Filter, line 83 |
| `ConsumerSquasharrResults = "squasharr-transcode-results"` | Consumer, line 103 |
| `BucketTranscodeLeases = "clustarr-transcode-leases"` | Bucket, line 120 |

Then append:

```go
// TranscodeLeaseTTL is how long a transcode task lease lives after its
// holder's last renewal. It is the lease bucket's TTL, so the server, not a
// worker's clock, decides when a lease has lapsed.
const TranscodeLeaseTTL = 90 * time.Second

// WorkTranscodeTaskSubject is where squasharr publishes one admitted
// TranscodeJob's task for the pool of its profile and hardware class.
// Profile and job are UIDs because a profile name may contain ".".
func WorkTranscodeTaskSubject(profileUID, class, jobUID string) string {
	return fmt.Sprintf("clustarr.work.transcode.task.%s.%s.%s", tok(profileUID), tok(class), tok(jobUID))
}

// WorkTranscodeResultSubject is where a worker publishes a job's status
// events; squasharr-transcode-results consumes them.
func WorkTranscodeResultSubject(jobUID string) string {
	return "clustarr.work.transcode.result." + tok(jobUID)
}

// FilterTranscodeTasks is one pool's share of CLUSTARR_WORK_SQUASHARR.
func FilterTranscodeTasks(profileUID, class string) string {
	return fmt.Sprintf("clustarr.work.transcode.task.%s.%s.>", tok(profileUID), tok(class))
}

// TranscodeTaskConsumerName is the durable one pool's workers share.
func TranscodeTaskConsumerName(profileUID, class string) string {
	return "squasharr-transcode-" + KVKeyToken(profileUID) + "-" + KVKeyToken(class)
}

// TranscodeLeaseKey is a TranscodeJob's lease in clustarr-transcode-leases.
func TranscodeLeaseKey(jobUID string) string { return "lease." + KVKeyToken(jobUID) }

// MsgIDForTranscodeTask carries the dispatch count, so a job dispatched again
// inside the duplicate window is not absorbed as a duplicate.
func MsgIDForTranscodeTask(jobUID string, attempt int32) string {
	return jobUID + "/" + strconv.Itoa(int(attempt))
}

// MsgIDForTranscodeEvent makes a re-published status event a duplicate;
// delivery separates two workers' runs of one attempt.
func MsgIDForTranscodeEvent(jobUID string, attempt int32, delivery, seq uint64) string {
	return fmt.Sprintf("%s/%d/%d/%d", jobUID, attempt, delivery, seq)
}
```

Add the `strconv` and `time` imports if they are missing.

- [ ] **Step 4: Add the stream, the results consumer, the bucket and the pool consumer to `topology.go`**

In `defaultStreams()`, next to the `work(...)` streams, add this stream. It is written out in
full because the `work` helper sets `DiscardOld` and `AllowMsgSchedules`:

```go
		{
			Name:        StreamWorkSquasharr,
			Description: "Transcode tasks squasharr admitted, and the workers' status events.",
			Subjects:    []string{FilterWorkSquasharr},
			Retention:   RetentionWorkQueue,
			Storage:     StorageFile,
			Discard:     DiscardNew,
			MaxBytes:    64 * MiB,
			Duplicates:  time.Hour,
			Replicas:    3,
		},
```

In `defaultConsumers()`, beside the captionarr consumers (same `s`/`m` unit constants):

```go
		{
			Name: ConsumerSquasharrResults, Stream: StreamWorkSquasharr,
			Description: "Worker status events: squasharr sets TranscodeJob status and decides the next step.",
			Filters: []string{FilterTranscodeResults},
			AckWait: 30 * s, MaxDeliver: 10,
			BackOff:       []time.Duration{5 * s, 30 * s, 2 * m},
			MaxAckPending: 1,
		},
```

In `defaultBuckets()`, beside `b(BucketProgress, …)`:

```go
		b(BucketTranscodeLeases, TranscodeLeaseTTL,
			"Transcode task leases: created by the claiming worker, renewed with Update, expired by the server; squasharr writes cancel markers."),
```

After `ConsumerSpec.Subscription()`:

```go
// TranscodeTaskConsumer is one pool's durable. It is not in Default(): pools
// come and go with profiles, so the worker's Pull creates it and squasharr's
// StreamAdmin deletes it. There is no Heartbeat: the worker sends InProgress
// itself while it renews its lease (spec §17.3). Workers settle every task
// once its finished event is stored; redelivery covers only a crashed,
// drained or fenced worker, so MaxDeliver is a safety net, not a retry policy
// (squasharr decides retries, spec §18.3).
func TranscodeTaskConsumer(profileUID, class string) ConsumerSpec {
	return ConsumerSpec{
		Name:          TranscodeTaskConsumerName(profileUID, class),
		Stream:        StreamWorkSquasharr,
		Description:   "One transcode pool's tasks.",
		Filters:       []string{FilterTranscodeTasks(profileUID, class)},
		AckWait:       60 * time.Second,
		MaxDeliver:    8,
		BackOff:       []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, 30 * time.Minute},
		MaxAckPending: 64,
	}
}
```

- [ ] **Step 5: Run the package tests**

Run: `go test ./pkg/events/... && grep -rln 'Default().Consumers' --include='*_test.go' .`
Expected: PASS.
- **`ForSingleNode`.** A test that pins exact per-stream byte sizes may fail, because one more
  stream now shares the 64 MiB single-node total. Update only its expected numbers; the rules it
  tests do not change.
- **Consumer inventories.** If the grep finds a test that holds every `Default()` consumer to a
  subscriber or to a KEDA inventory, register `squasharr-transcode-results` there as squasharr's.
  Task 10 subscribes it.

- [ ] **Step 6: Commit**

```bash
git add pkg/events/subjects.go pkg/events/topology.go pkg/events/transcode_topology_test.go
git commit -m 'feat(events): transcode task stream, results consumer, per-pool consumer and lease bucket' -- pkg/events/subjects.go pkg/events/topology.go pkg/events/transcode_topology_test.go
```
(Include any test file you updated in Step 5 in both commands.)

---

### Task 2: One-at-a-time pulls and queue cleanup on both buses

**Files:**
- Modify: `pkg/events/bus.go`: the `Puller`, `PullSubscriber` and `StreamAdmin` interfaces.
- Create: `pkg/events/natsbus/pull.go`, `pkg/events/natsbus/admin.go`.
- Modify: `pkg/events/natsbus/natsbus.go`: factor message wrapping out of `handle` (line 342).
- Modify: `pkg/events/natsbus/deadletter.go`: factor out `dlqWatchName`.
- Create: `pkg/events/membus/pull.go`, `pkg/events/membus/admin.go`.
- Create: `pkg/events/contracttest/pull.go`.
- Modify: `pkg/events/membus/membus_test.go:31` and `pkg/events/natsbus/natsbus_test.go:75`, to run
  the new contract.

**Interfaces:**
- Consumes: `events.TranscodeTaskConsumer`, `events.WorkTranscodeTaskSubject` (Task 1).
- Produces:
  ```go
  type Puller interface {
  	Next(ctx context.Context) (context.Context, Message, error)
  	Stop()
  }
  type PullSubscriber interface {
  	Pull(ctx context.Context, s Subscription) (Puller, error)
  }
  type StreamAdmin interface {
  	DeleteSubscription(ctx context.Context, stream, durable string) error
  	PurgeSubject(ctx context.Context, stream, subject string) error
  	Subjects(ctx context.Context, stream, filter string) ([]string, error)
  }
  ```
  Both buses satisfy them: `var _ events.PullSubscriber = (*Bus)(nil)` and
  `var _ events.StreamAdmin = (*Bus)(nil)` in each package.

- [ ] **Step 1: Declare the interfaces in `pkg/events/bus.go`**

Add them after `Subscriber`. They are optional interfaces rather than `Bus` methods, so no
existing fake breaks.

```go
// Puller hands out one message per Next call from a durable pull consumer.
// Nothing is fetched ahead, so a caller that works on a message for hours
// never holds a second, prefetched one past its ack window. The caller
// settles each message itself (Ack, Nak, Term); nothing settles it for them.
type Puller interface {
	// Next blocks until the consumer delivers a message to this caller or
	// ctx ends. The returned context carries Hooks.AfterReceive's result.
	Next(ctx context.Context) (context.Context, Message, error)
	// Stop releases the puller. It never deletes the durable.
	Stop()
}

// PullSubscriber is a bus that can pull one message at a time. Pull creates
// or updates the durable s describes; s.MaxInFlight is the durable's
// MaxAckPending across every puller that shares it.
type PullSubscriber interface {
	Pull(ctx context.Context, s Subscription) (Puller, error)
}

// StreamAdmin removes queue state whose owner is gone.
type StreamAdmin interface {
	// DeleteSubscription deletes the durable and its dead-letter watcher.
	// A missing one is not an error.
	DeleteSubscription(ctx context.Context, stream, durable string) error
	// PurgeSubject removes every stored message on subject.
	PurgeSubject(ctx context.Context, stream, subject string) error
	// Subjects lists the subjects under filter that hold stored messages.
	Subjects(ctx context.Context, stream, filter string) ([]string, error)
}
```

- [ ] **Step 2: Write the failing contract in `pkg/events/contracttest/pull.go`**

This follows `contracttest.go`'s style: stdlib `testing`, `setup`, `envelope`.

```go
package contracttest

import (
	"context"
	"testing"
	"time"

	"github.com/mediactl/clustarr/pkg/events"
)

// RunPullContract holds a bus's PullSubscriber and StreamAdmin to the
// behaviour squasharr's worker pools depend on.
func RunPullContract(t *testing.T, newBus func() events.Bus) {
	t.Run("PullHandsOutOneMessagePerNext", func(t *testing.T) { testPullOnePerNext(t, newBus) })
	t.Run("PullRedeliversANakedMessage", func(t *testing.T) { testPullRedelivers(t, newBus) })
	t.Run("InProgressHoldsAPulledMessage", func(t *testing.T) { testPullInProgress(t, newBus) })
	t.Run("PurgeSubjectRemovesOnlyThatSubject", func(t *testing.T) { testPurgeSubject(t, newBus) })
	t.Run("DeleteSubscriptionIsIdempotentAndKeepsQueuedWork", func(t *testing.T) { testDeleteSubscription(t, newBus) })
}

func pullBus(t *testing.T, bus events.Bus) (events.PullSubscriber, events.StreamAdmin) {
	t.Helper()
	ps, ok := bus.(events.PullSubscriber)
	if !ok {
		t.Fatalf("%T does not implement events.PullSubscriber", bus)
	}
	sa, ok := bus.(events.StreamAdmin)
	if !ok {
		t.Fatalf("%T does not implement events.StreamAdmin", bus)
	}
	return ps, sa
}

func publishTask(ctx context.Context, t *testing.T, bus events.Bus, profile, job string) {
	t.Helper()
	subj := events.WorkTranscodeTaskSubject(profile, "cpu", job)
	if _, err := bus.Publish(ctx, subj, envelope(job, "transcode.Task.v1", job)); err != nil {
		t.Fatalf("Publish %s: %v", subj, err)
	}
}

func next(ctx context.Context, t *testing.T, p events.Puller, within time.Duration) events.Message {
	t.Helper()
	c, cancel := context.WithTimeout(ctx, within)
	defer cancel()
	_, m, err := p.Next(c)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	return m
}

func nothingWithin(ctx context.Context, t *testing.T, p events.Puller, within time.Duration) {
	t.Helper()
	c, cancel := context.WithTimeout(ctx, within)
	defer cancel()
	if _, m, err := p.Next(c); err == nil {
		t.Fatalf("Next returned %s when nothing was deliverable", m.Envelope().ID)
	}
}

func testPullOnePerNext(t *testing.T, newBus func() events.Bus) {
	ctx, bus := setup(t, newBus)
	ps, _ := pullBus(t, bus)
	sub := events.TranscodeTaskConsumer("prof", "cpu").Subscription()
	publishTask(ctx, t, bus, "prof", "j1")
	publishTask(ctx, t, bus, "prof", "j2")

	a, err := ps.Pull(ctx, sub)
	if err != nil {
		t.Fatalf("Pull a: %v", err)
	}
	defer a.Stop()
	b, err := ps.Pull(ctx, sub)
	if err != nil {
		t.Fatalf("Pull b: %v", err)
	}
	defer b.Stop()

	m1, m2 := next(ctx, t, a, 5*time.Second), next(ctx, t, b, 5*time.Second)
	if m1.Envelope().ID == m2.Envelope().ID {
		t.Fatalf("two pullers on one durable both got %s", m1.Envelope().ID)
	}
	nothingWithin(ctx, t, a, 500*time.Millisecond)
	_ = m1.Ack(ctx)
	_ = m2.Ack(ctx)
}

func testPullRedelivers(t *testing.T, newBus func() events.Bus) {
	ctx, bus := setup(t, newBus)
	ps, _ := pullBus(t, bus)
	p, err := ps.Pull(ctx, events.TranscodeTaskConsumer("prof", "cpu").Subscription())
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	defer p.Stop()
	publishTask(ctx, t, bus, "prof", "j1")

	m := next(ctx, t, p, 5*time.Second)
	if err := m.Nak(ctx, 0); err != nil {
		t.Fatalf("Nak: %v", err)
	}
	again := next(ctx, t, p, 5*time.Second)
	if again.Envelope().ID != "j1" || again.Attempt() != 2 {
		t.Fatalf("redelivery = %s attempt %d, want j1 attempt 2", again.Envelope().ID, again.Attempt())
	}
	_ = again.Ack(ctx)
}

func testPullInProgress(t *testing.T, newBus func() events.Bus) {
	ctx, bus := setup(t, newBus)
	ps, _ := pullBus(t, bus)
	sub := events.TranscodeTaskConsumer("prof", "cpu").Subscription()
	sub.AckWait = 2 * time.Second
	p, err := ps.Pull(ctx, sub)
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	defer p.Stop()
	publishTask(ctx, t, bus, "prof", "j1")

	m := next(ctx, t, p, 5*time.Second)
	for i := 0; i < 8; i++ { // 4s, twice the ack window
		time.Sleep(500 * time.Millisecond)
		if err := m.InProgress(ctx); err != nil {
			t.Fatalf("InProgress: %v", err)
		}
	}
	nothingWithin(ctx, t, p, 500*time.Millisecond)
	// Stop renewing: the message must come back after one ack window.
	again := next(ctx, t, p, 6*time.Second)
	if again.Envelope().ID != "j1" {
		t.Fatalf("redelivered %s, want j1", again.Envelope().ID)
	}
	_ = again.Ack(ctx)
}

func testPurgeSubject(t *testing.T, newBus func() events.Bus) {
	ctx, bus := setup(t, newBus)
	ps, sa := pullBus(t, bus)
	publishTask(ctx, t, bus, "prof", "gone")
	publishTask(ctx, t, bus, "prof", "kept")

	subjects, err := sa.Subjects(ctx, events.StreamWorkSquasharr, events.FilterTranscodeTasks("prof", "cpu"))
	if err != nil || len(subjects) != 2 {
		t.Fatalf("Subjects = %v, %v; want two", subjects, err)
	}
	if err := sa.PurgeSubject(ctx, events.StreamWorkSquasharr,
		events.WorkTranscodeTaskSubject("prof", "cpu", "gone")); err != nil {
		t.Fatalf("PurgeSubject: %v", err)
	}
	p, err := ps.Pull(ctx, events.TranscodeTaskConsumer("prof", "cpu").Subscription())
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	defer p.Stop()
	if m := next(ctx, t, p, 5*time.Second); m.Envelope().ID != "kept" {
		t.Fatalf("got %s after purging gone, want kept", m.Envelope().ID)
	}
	nothingWithin(ctx, t, p, 500*time.Millisecond)
}

func testDeleteSubscription(t *testing.T, newBus func() events.Bus) {
	ctx, bus := setup(t, newBus)
	ps, sa := pullBus(t, bus)
	sub := events.TranscodeTaskConsumer("prof", "cpu").Subscription()
	if err := sa.DeleteSubscription(ctx, sub.Stream, sub.Durable); err != nil {
		t.Fatalf("deleting a durable that never existed: %v", err)
	}
	p, err := ps.Pull(ctx, sub)
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	p.Stop()
	publishTask(ctx, t, bus, "prof", "queued")
	if err := sa.DeleteSubscription(ctx, sub.Stream, sub.Durable); err != nil {
		t.Fatalf("DeleteSubscription: %v", err)
	}
	p, err = ps.Pull(ctx, sub) // a new durable on a work queue still sees the stored task
	if err != nil {
		t.Fatalf("Pull after delete: %v", err)
	}
	defer p.Stop()
	if m := next(ctx, t, p, 5*time.Second); m.Envelope().ID != "queued" {
		t.Fatalf("got %s, want the task published before the delete", m.Envelope().ID)
	}
}
```

Wire the contract in `membus_test.go` and `natsbus_test.go` beside `RunBusContract`, with the
same constructor each already passes:

```go
func TestPullContract(t *testing.T) {
	contracttest.RunPullContract(t, func() events.Bus { return membus.New(nil) })
}
```

(In `natsbus_test.go`, pass the same closure that `RunBusContract` receives at line 75.)

- [ ] **Step 3: Run the contract to verify it fails**

Run: `go test ./pkg/events/... -run TestPullContract`
Expected: FAIL: "*membus.Bus does not implement events.PullSubscriber" (natsbus fails the same
way).

- [ ] **Step 4: Implement the natsbus side**

`pkg/events/natsbus/deadletter.go`: replace the inline `"clustarr-dlq-watch-" + sub.Stream + "-" + sub.Durable`
with a call to:

```go
func dlqWatchName(stream, durable string) string { return "clustarr-dlq-watch-" + stream + "-" + durable }
```

`pkg/events/natsbus/natsbus.go`: extract the code in `handle` (line 342) that turns a
`jetstream.Msg` into an `events.Message` into one method, so `Subscribe` and `Pull` wrap messages
identically. That code decodes the envelope, runs `Hooks.AfterReceive` and builds `*message`:

```go
// receive wraps one delivery the way every consumer path must: decode the
// envelope, run Hooks.AfterReceive, and bind the message to its subscription.
func (b *Bus) receive(ctx context.Context, jm jetstream.Msg, sub events.Subscription) (context.Context, *message, error)
```

`handle` calls `receive`, then settles as before. Behaviour is unchanged: `RunBusContract` must
still pass.

`pkg/events/natsbus/pull.go`:

```go
package natsbus

// (GPL header)

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/mediactl/clustarr/pkg/events"
)

var _ events.PullSubscriber = (*Bus)(nil)

type puller struct {
	bus   *Bus
	cons  jetstream.Consumer
	sub   events.Subscription
	watch jetstream.ConsumeContext
}

// Pull implements events.PullSubscriber.
func (b *Bus) Pull(ctx context.Context, s events.Subscription) (events.Puller, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	spec := events.ConsumerSpec{
		Name: s.Durable, Stream: s.Stream, Filters: s.Filters, AckWait: s.AckWait,
		MaxDeliver: s.MaxDeliver, BackOff: s.Backoff, MaxAckPending: max(s.MaxInFlight, 1),
	}
	cons, err := b.js.CreateOrUpdateConsumer(ctx, s.Stream, events.ConsumerConfig(spec))
	if err != nil {
		return nil, fmt.Errorf("natsbus: pull %s/%s: %w", s.Stream, s.Durable, err)
	}
	watch, err := b.watchMaxDeliveries(ctx, s)
	if err != nil {
		return nil, err
	}
	return &puller{bus: b, cons: cons, sub: s, watch: watch}, nil
}

// Next implements events.Puller. It fetches exactly one message per call and
// wakes every second to notice a cancelled ctx.
func (p *puller) Next(ctx context.Context) (context.Context, events.Message, error) {
	for {
		if err := ctx.Err(); err != nil {
			return ctx, nil, err
		}
		jm, err := p.cons.Next(jetstream.FetchMaxWait(time.Second))
		if errors.Is(err, jetstream.ErrNoMessages) || errors.Is(err, nats.ErrTimeout) {
			continue
		}
		if err != nil {
			return ctx, nil, fmt.Errorf("natsbus: next %s/%s: %w", p.sub.Stream, p.sub.Durable, err)
		}
		mctx, m, err := p.bus.receive(ctx, jm, p.sub)
		if err != nil {
			return ctx, nil, err
		}
		return mctx, m, nil
	}
}

// Stop implements events.Puller.
func (p *puller) Stop() {
	if p.watch != nil {
		p.watch.Stop()
	}
}
```

`pkg/events/natsbus/admin.go`:

```go
var _ events.StreamAdmin = (*Bus)(nil)

// DeleteSubscription implements events.StreamAdmin.
func (b *Bus) DeleteSubscription(ctx context.Context, stream, durable string) error {
	for _, c := range [][2]string{{stream, durable}, {events.StreamAdvisories, dlqWatchName(stream, durable)}} {
		err := b.js.DeleteConsumer(ctx, c[0], c[1])
		if err != nil && !errors.Is(err, jetstream.ErrConsumerNotFound) && !errors.Is(err, jetstream.ErrStreamNotFound) {
			return fmt.Errorf("natsbus: delete consumer %s/%s: %w", c[0], c[1], err)
		}
	}
	return nil
}

// PurgeSubject implements events.StreamAdmin.
func (b *Bus) PurgeSubject(ctx context.Context, stream, subject string) error {
	st, err := b.js.Stream(ctx, stream)
	if err != nil {
		return fmt.Errorf("natsbus: stream %s: %w", stream, err)
	}
	if err := st.Purge(ctx, jetstream.WithPurgeSubject(subject)); err != nil {
		return fmt.Errorf("natsbus: purge %s on %s: %w", subject, stream, err)
	}
	return nil
}

// Subjects implements events.StreamAdmin.
func (b *Bus) Subjects(ctx context.Context, stream, filter string) ([]string, error) {
	st, err := b.js.Stream(ctx, stream)
	if err != nil {
		return nil, fmt.Errorf("natsbus: stream %s: %w", stream, err)
	}
	info, err := st.Info(ctx, jetstream.WithSubjectFilter(filter))
	if err != nil {
		return nil, fmt.Errorf("natsbus: stream %s subjects %s: %w", stream, filter, err)
	}
	out := make([]string, 0, len(info.State.Subjects))
	for s := range info.State.Subjects {
		out = append(out, s)
	}
	sort.Strings(out)
	return out, nil
}
```

- [ ] **Step 5: Implement the membus side**

`pkg/events/membus/pull.go`: `Pull` validates `s`. It registers the durable the same way
`Subscribe` does (`membus.go:248-296`), with the same claim state, dedup and ack-deadline sweep,
but runs no delivery goroutine. `Next(ctx)`:
1. claims the next unclaimed message on that durable, using the claim step of `Subscribe`'s
   delivery loop;
2. waits on the bus's notify channel until one appears or `ctx` ends;
3. runs `Hooks.AfterReceive` like `Subscribe` does and returns its context.

`Stop` releases the puller's slot and leaves the durable. Factor the claim step out of
`Subscribe` into one function the two paths share. Do not copy it: CLAUDE.md, "grep for the
other copies".

`pkg/events/membus/admin.go`:
- `DeleteSubscription` drops the durable's claim state (a missing durable is nil).
- `PurgeSubject` removes stored messages whose subject equals `subject`.
- `Subjects` returns the sorted distinct stored subjects that match `filter`, using the matcher
  `StreamForSubject` already uses.

- [ ] **Step 6: Run both contracts**

Run: `go test ./pkg/events/... -run 'TestPullContract|TestBusContract|TestHooks'`
Expected: PASS for membus and natsbus. `InProgressHoldsAPulledMessage` takes about 8s per bus.

- [ ] **Step 7: Commit**

```bash
git add pkg/events/bus.go pkg/events/natsbus pkg/events/membus pkg/events/contracttest
git commit -m 'feat(events): PullSubscriber and StreamAdmin on natsbus and membus, under one contract' -- pkg/events/bus.go pkg/events/natsbus pkg/events/membus pkg/events/contracttest
```

---

### Task 3: Task, status-event and lease types; `worker.BuildTask`

**Files:**
- Create: `squasharr/task/task.go`, `squasharr/task/task_test.go`.
- Create: `squasharr/worker/buildtask.go`, `squasharr/worker/buildtask_test.go`.
- Modify: `squasharr/worker/profile.go`: add `DefaultActiveDeadline` and `ActiveDeadline`.

**Interfaces:**
- Produces:
  ```go
  // package task
  type Profile struct { Name, Hash string; Spec transcodev1alpha1.TranscodeProfileSpec; Hardware *transcodev1alpha1.Hardware }
  type RootFolder struct { Path, RecycleBin string }
  type Task struct { Job schema.Ref; Attempt int32; Class string; Profile Profile; SourcePath, SourceProbeHash string;
  	SourceSizeBytes int64; SourceModifier, OutputPath string; Root RootFolder; OutputRoot, ArgsHash string; Deadline metav1.Duration }
  type Outcome string   // OutcomeSucceeded, OutcomeSkipped, OutcomeFailed, OutcomeCancelled
  type Reason string    // ReasonInvalidSource, ReasonSourceChanged, ReasonVerifyFailed, ReasonDeadlineExceeded, ReasonRetriable,
                        // ReasonGPUUnavailable, ReasonGPUEncodeFailed, ReasonCancelled; squasharr-only: ReasonRetriesExhausted, ReasonDeadLettered
  type EventKind string // EventClaimed, EventProgress, EventFinished
  type StatusEvent struct { Job schema.Ref; Attempt int32; Delivery, Seq uint64; Kind EventKind; Pod, Node string;
  	Progress *transcodev1alpha1.Progress; Outcome Outcome; Reason Reason; Message string;
  	Result *transcodev1alpha1.Result; StderrTail string; At time.Time }
  type LeaseState string // LeaseHeld, LeaseCancelled
  type Lease struct { Job schema.Ref; Attempt int32; State LeaseState; Pod, Node string; Since time.Time }
  // package worker
  var ErrNoRootFolder, ErrInvalidOutput error
  func BuildTask(tj *transcodev1alpha1.TranscodeJob, tp *transcodev1alpha1.TranscodeProfile, mf *catalogv1alpha1.MediaFile,
  	folders []catalogv1alpha1.RootFolder, attempt int32, class transcodev1alpha1.Hardware) (task.Task, error)
  const DefaultActiveDeadline = 48 * time.Hour
  func ActiveDeadline(p transcodev1alpha1.TranscodeProfileSpec) time.Duration
  ```

- [ ] **Step 1: Write the failing tests**

`squasharr/task/task_test.go`:

```go
package task_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"

	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/squasharr/task"
	"github.com/mediactl/clustarr/squasharr/worker"
)

// Review Focus 1: a pointer false is a value; losing it in transit silently
// turns "keep the source" into "replace the source".
func TestTaskJSONKeepsFalsePolicyPointers(t *testing.T) {
	in := task.Task{
		Job:     schema.Ref{Namespace: "media", Name: "tj", UID: "u1"},
		Attempt: 2,
		Profile: task.Profile{Name: "p", Hash: "h", Spec: transcodev1alpha1.TranscodeProfileSpec{
			Policy: transcodev1alpha1.PolicySpec{ReplaceSource: ptr.To(false), RecycleBin: ptr.To(false)},
		}},
	}
	_, data, err := schema.Encode(in)
	require.NoError(t, err)
	var out task.Task
	require.NoError(t, schema.Decode(in.Schema(), data, &out))
	assert.False(t, worker.ReplaceSource(out.Profile.Spec.Policy))
	assert.False(t, worker.RecycleBin(out.Profile.Spec.Policy))
	assert.Equal(t, in.Attempt, out.Attempt)
}

func TestSchemasAreVersioned(t *testing.T) {
	assert.Equal(t, "transcode.Task.v1", task.Task{}.Schema())
	assert.Equal(t, "transcode.StatusEvent.v1", task.StatusEvent{}.Schema())
	assert.Equal(t, "transcode.Lease.v1", task.Lease{}.Schema())
	b, err := json.Marshal(task.StatusEvent{Kind: task.EventFinished, Outcome: task.OutcomeFailed, Reason: task.ReasonGPUEncodeFailed})
	require.NoError(t, err)
	assert.JSONEq(t, `{"job":{"name":""},"attempt":0,"delivery":0,"seq":0,"kind":"finished","outcome":"failed","reason":"GPUEncodeFailed","at":"0001-01-01T00:00:00Z"}`, string(b))
}
```

`squasharr/worker/buildtask_test.go` is a pure table test over in-memory objects:

```go
package worker

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
)

func folder(name, path, bin string) catalogv1alpha1.RootFolder {
	rf := catalogv1alpha1.RootFolder{ObjectMeta: metav1.ObjectMeta{Name: name}}
	rf.Spec.Path = path
	rf.Spec.RecycleBin.Path = bin
	return rf
}

func TestBuildTask(t *testing.T) {
	folders := []catalogv1alpha1.RootFolder{
		folder("all", "/data/media", ""),
		folder("movies", "/data/media/movies", "/data/media/.bin"),
	}
	tp := &transcodev1alpha1.TranscodeProfile{ObjectMeta: metav1.ObjectMeta{Name: "hevc", UID: "puid"}}
	tp.Status.Hash = "abc123"
	tp.Spec.Container = transcodev1alpha1.Container("mkv")
	tp.Spec.ActiveDeadline = metav1.Duration{Duration: 3 * time.Hour}
	mf := &catalogv1alpha1.MediaFile{}
	mf.Spec.Path = "/data/media/movies/Heat (1995)/Heat.mkv"
	mf.Spec.SizeBytes = 42
	tj := &transcodev1alpha1.TranscodeJob{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "tj", UID: "juid"}}
	tj.Spec.SourceProbeHash = "ph"
	tj.Status.Plan = &transcodev1alpha1.Plan{ArgsHash: "ah"}

	got, err := BuildTask(tj, tp, mf, folders, 3, transcodev1alpha1.HardwareNVIDIA)
	require.NoError(t, err)
	assert.Equal(t, "/data/media/movies", got.Root.Path, "the deepest containing folder wins")
	assert.Equal(t, "/data/media/.bin", got.Root.RecycleBin)
	assert.Equal(t, mf.Spec.Path, got.SourcePath, "an empty spec.sourcePath falls back to the MediaFile")
	assert.Equal(t, mf.Spec.Path, got.OutputPath, "same container, replaceSource defaulted: in place")
	assert.Empty(t, got.OutputRoot)
	assert.Equal(t, int32(3), got.Attempt)
	assert.Equal(t, "nvidia", got.Class)
	assert.Equal(t, "ah", got.ArgsHash)
	assert.Equal(t, int64(42), got.SourceSizeBytes)
	assert.Equal(t, 3*time.Hour, got.Deadline.Duration)
	assert.Equal(t, "juid", got.Job.UID)

	folders[1].Spec.RecycleBin.Path = ""
	got, err = BuildTask(tj, tp, mf, folders, 1, transcodev1alpha1.HardwareCPU)
	require.NoError(t, err)
	assert.Equal(t, defaultRecycleBin, got.Root.RecycleBin)

	elsewhere := tj.DeepCopy()
	elsewhere.Spec.OutputPath = ptr.To("/data/media/other/Heat.mkv")
	got, err = BuildTask(elsewhere, tp, mf, folders, 1, transcodev1alpha1.HardwareCPU)
	require.NoError(t, err)
	assert.Equal(t, "/data/media", got.OutputRoot)

	outside := tj.DeepCopy()
	outside.Spec.OutputPath = ptr.To("/tmp/Heat.mkv")
	_, err = BuildTask(outside, tp, mf, folders, 1, transcodev1alpha1.HardwareCPU)
	assert.ErrorIs(t, err, ErrNoRootFolder)

	stray := mf.DeepCopy()
	stray.Spec.Path = "/srv/Heat.mkv"
	_, err = BuildTask(tj, tp, stray, folders, 1, transcodev1alpha1.HardwareCPU)
	assert.ErrorIs(t, err, ErrNoRootFolder)

	tp.Spec.ActiveDeadline = metav1.Duration{}
	got, err = BuildTask(tj, tp, mf, folders, 1, transcodev1alpha1.HardwareCPU)
	require.NoError(t, err)
	assert.Equal(t, DefaultActiveDeadline, got.Deadline.Duration)
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./squasharr/task/ ./squasharr/worker/ -run 'TestTask|TestSchemas|TestBuildTask'`
Expected: FAIL to compile, "package squasharr/task is not in std".

- [ ] **Step 3: Write `squasharr/task/task.go`**

```go
// Package task holds what crosses NATS between squasharr and its transcode
// pools (spec §6, §17.5, §18.1): the Task squasharr publishes, the
// StatusEvents a worker publishes on clustarr.work.transcode.result.<uid>,
// and the Lease a worker holds in clustarr-transcode-leases. It imports no
// Kubernetes client.
package task

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// Profile is the TranscodeProfile snapshot a task was planned under.
type Profile struct {
	Name     string                                 `json:"name"`
	Hash     string                                 `json:"hash"`
	Spec     transcodev1alpha1.TranscodeProfileSpec `json:"spec"`
	Hardware *transcodev1alpha1.Hardware            `json:"hardware,omitempty"`
}

// RootFolder is the library root the source lives under, resolved by squasharr.
type RootFolder struct {
	Path       string `json:"path"`
	RecycleBin string `json:"recycleBin"`
}

// Task is one dispatch of one TranscodeJob.
type Task struct {
	Job             schema.Ref      `json:"job"`
	Attempt         int32           `json:"attempt"`
	Class           string          `json:"class"`
	Profile         Profile         `json:"profile"`
	SourcePath      string          `json:"sourcePath"`
	SourceProbeHash string          `json:"sourceProbeHash"`
	SourceSizeBytes int64           `json:"sourceSizeBytes"`
	SourceModifier  string          `json:"sourceModifier,omitempty"`
	OutputPath      string          `json:"outputPath"`
	Root            RootFolder      `json:"root"`
	OutputRoot      string          `json:"outputRoot,omitempty"`
	ArgsHash        string          `json:"argsHash,omitempty"`
	Deadline        metav1.Duration `json:"deadline"`
}

// Schema implements schema.Payload.
func (Task) Schema() string { return "transcode.Task.v1" }

// Outcome is how an attempt ended.
type Outcome string

const (
	OutcomeSucceeded Outcome = "succeeded"
	OutcomeSkipped   Outcome = "skipped"
	OutcomeFailed    Outcome = "failed"
	OutcomeCancelled Outcome = "cancelled"
)

// Reason qualifies an Outcome (spec §18.1, §18.3).
type Reason string

const (
	ReasonInvalidSource    Reason = "InvalidSource"
	ReasonSourceChanged    Reason = "SourceChanged"
	ReasonVerifyFailed     Reason = "VerifyFailed"
	ReasonDeadlineExceeded Reason = "DeadlineExceeded"
	ReasonRetriable        Reason = "Retriable"
	ReasonGPUUnavailable   Reason = "GPUUnavailable"
	ReasonGPUEncodeFailed  Reason = "GPUEncodeFailed"
	ReasonCancelled        Reason = "Cancelled"

	// Decided by squasharr, never reported by a worker.
	ReasonRetriesExhausted Reason = "RetriesExhausted"
	ReasonDeadLettered     Reason = "DeadLettered"
)

// EventKind is what a StatusEvent reports.
type EventKind string

const (
	EventClaimed  EventKind = "claimed"
	EventProgress EventKind = "progress"
	EventFinished EventKind = "finished"
)

// StatusEvent is one report from the worker running a delivery of an
// attempt. Seq counts from 1 within one delivery; the Msg-Id
// <uid>/<attempt>/<delivery>/<seq> makes a re-publish a duplicate.
type StatusEvent struct {
	Job        schema.Ref                  `json:"job"`
	Attempt    int32                       `json:"attempt"`
	Delivery   uint64                      `json:"delivery"`
	Seq        uint64                      `json:"seq"`
	Kind       EventKind                   `json:"kind"`
	Pod        string                      `json:"pod,omitempty"`
	Node       string                      `json:"node,omitempty"`
	Progress   *transcodev1alpha1.Progress `json:"progress,omitempty"`
	Outcome    Outcome                     `json:"outcome,omitempty"`
	Reason     Reason                      `json:"reason,omitempty"`
	Message    string                      `json:"message,omitempty"`
	Result     *transcodev1alpha1.Result   `json:"result,omitempty"`
	StderrTail string                      `json:"stderrTail,omitempty"`
	At         time.Time                   `json:"at"`
}

// Schema implements schema.Payload.
func (StatusEvent) Schema() string { return "transcode.StatusEvent.v1" }

// LeaseState is whether a lease is held by a worker or marks a withdrawal.
type LeaseState string

const (
	LeaseHeld      LeaseState = "held"
	LeaseCancelled LeaseState = "cancelled"
)

// Lease is one TranscodeJob's claim. A cancelled lease applies to its own
// Attempt and earlier ones only (spec §6).
type Lease struct {
	Job     schema.Ref `json:"job"`
	Attempt int32      `json:"attempt"`
	State   LeaseState `json:"state"`
	Pod     string     `json:"pod,omitempty"`
	Node    string     `json:"node,omitempty"`
	Since   time.Time  `json:"since"`
}

// Schema implements schema.Payload.
func (Lease) Schema() string { return "transcode.Lease.v1" }
```

- [ ] **Step 4: Write `squasharr/worker/buildtask.go` and the deadline accessor**

In `profile.go`, add the deadline accessor beside `MinDuration`:

```go
// DefaultActiveDeadline is the per-task deadline when the profile sets none.
// TestFlooredDefaultsMatchTheGeneratedCRD holds it to the CRD default.
const DefaultActiveDeadline = 48 * time.Hour

// ActiveDeadline is a profile's per-task deadline, enforced by the worker.
func ActiveDeadline(p transcodev1alpha1.TranscodeProfileSpec) time.Duration {
	if p.ActiveDeadline.Duration > 0 {
		return p.ActiveDeadline.Duration
	}
	return DefaultActiveDeadline
}
```

`buildtask.go`:

```go
// ErrNoRootFolder is a source, or an output that is not in place, lying under
// no RootFolder. The worker never touches a file outside one.
var ErrNoRootFolder = errors.New("not under any RootFolder")

// ErrInvalidOutput is an output path OutputPath refuses.
var ErrInvalidOutput = errors.New("invalid output path")

// BuildTask renders one dispatch of tj for the class it goes to: everything
// the worker used to read from the apiserver, resolved by the one function
// the controller and the worker's tests share (spec §6, §17.5).
func BuildTask(tj *transcodev1alpha1.TranscodeJob, tp *transcodev1alpha1.TranscodeProfile,
	mf *catalogv1alpha1.MediaFile, folders []catalogv1alpha1.RootFolder,
	attempt int32, class transcodev1alpha1.Hardware,
) (task.Task, error) {
	source := tj.Spec.SourcePath
	if source == "" {
		source = mf.Spec.Path
	}
	source = filepath.Clean(source)
	rf := rootFolderFor(folders, source)
	if rf == nil {
		return task.Task{}, fmt.Errorf("source %s: %w", source, ErrNoRootFolder)
	}
	out, err := OutputPath(tj.Spec, tp.Name, tp.Spec.Container, ReplaceSource(tp.Spec.Policy))
	if err != nil {
		return task.Task{}, fmt.Errorf("%w: %v", ErrInvalidOutput, err)
	}
	bin := rf.Spec.RecycleBin.Path
	if bin == "" {
		bin = defaultRecycleBin
	}
	t := task.Task{
		Job:     schema.Ref{Namespace: tj.Namespace, Name: tj.Name, UID: string(tj.UID)},
		Attempt: attempt,
		Class:   string(class),
		Profile: task.Profile{
			Name: tp.Name, Hash: tp.Status.Hash, Spec: *tp.Spec.DeepCopy(), Hardware: ptr.To(class),
		},
		SourcePath:      source,
		SourceProbeHash: tj.Spec.SourceProbeHash,
		SourceSizeBytes: mf.Spec.SizeBytes,
		SourceModifier:  string(mf.Spec.Quality.Modifier),
		OutputPath:      out,
		Root:            task.RootFolder{Path: rf.Spec.Path, RecycleBin: bin},
		Deadline:        metav1.Duration{Duration: ActiveDeadline(tp.Spec)},
	}
	if tj.Status.Plan != nil {
		t.ArgsHash = tj.Status.Plan.ArgsHash
	}
	if filepath.Clean(out) != source {
		orf := rootFolderFor(folders, out)
		if orf == nil {
			return task.Task{}, fmt.Errorf("output %s: %w", out, ErrNoRootFolder)
		}
		t.OutputRoot = orf.Spec.Path
	}
	return t, nil
}
```

`Profile.Hardware` is the chosen **class**, so the worker plans with exactly the class squasharr
planned with (`ProfileSpec(spec, &class)`). Add this assertion to `TestBuildTask`:
`assert.Equal(t, transcodev1alpha1.HardwareNVIDIA, *got.Profile.Hardware)`.

- [ ] **Step 5: Run the tests**

Run: `go test ./squasharr/task/ ./squasharr/worker/`
Expected: PASS. Envtest and ffmpeg suites skip without their assets; that is fine for this step.

- [ ] **Step 6: Commit**

```bash
git add squasharr/task squasharr/worker/buildtask.go squasharr/worker/buildtask_test.go squasharr/worker/profile.go
git commit -m 'feat(squasharr): task, status-event and lease types; BuildTask resolves what the worker used to read' -- squasharr/task squasharr/worker/buildtask.go squasharr/worker/buildtask_test.go squasharr/worker/profile.go
```

---

### Task 4: API: the new status fields, the `Blocked` condition, and `hardware: auto`

**Files:**
- Modify: `api/transcode/v1alpha1/transcodejob_types.go`: status fields, the condition type,
  print columns, and docs.
- Modify: `api/transcode/v1alpha1/transcodeprofile_types.go`: the `Hardware` enum and default,
  plus docs for `ActiveDeadline` and `TTLSecondsAfterFinished`.
- Modify: `squasharr/worker/profile.go`, so `ProfileSpec` resolves `auto`.
- Test: `squasharr/worker/unit_test.go`.
- Generated: `zz_generated.deepcopy.go`, `api/applyconfiguration/transcode/v1alpha1/*`, and
  `config/crd/bases/transcode.clustarr.io_transcode{jobs,profiles}.yaml`.

**Interfaces:**
- Produces:
  ```go
  const HardwareAuto Hardware = "auto"        // Hardware's enum: cpu;nvidia;intel;auto
  const ConditionBlocked = "Blocked"          // beside the other TranscodeJob condition types (lines 68-79)
  // TranscodeJobStatus gains:
  WorkerPod      string       `json:"workerPod,omitempty"`
  Hardware       Hardware     `json:"hardware,omitempty"`
  FallbackReason string       `json:"fallbackReason,omitempty"`
  NextAttemptAt  *metav1.Time `json:"nextAttemptAt,omitempty"`
  ```
  `worker.ProfileSpec(spec, hardware)` plans for CPU when the effective hardware is `auto`.

- [ ] **Step 1: Write the failing test**

In `squasharr/worker/unit_test.go`:

```go
// An auto profile with no class chosen yet plans for CPU. Its profile hash is
// therefore the one a cpu profile had, so changing the CRD default from cpu to
// auto re-transcodes nothing.
func TestProfileSpecResolvesAutoToCPU(t *testing.T) {
	auto := transcodev1alpha1.TranscodeProfileSpec{Hardware: transcodev1alpha1.HardwareAuto}
	cpu := transcodev1alpha1.TranscodeProfileSpec{Hardware: transcodev1alpha1.HardwareCPU}
	assert.Equal(t, ProfileSpec(cpu, nil), ProfileSpec(auto, nil))
	nv := transcodev1alpha1.HardwareNVIDIA
	assert.Equal(t, ProfileSpec(transcodev1alpha1.TranscodeProfileSpec{Hardware: nv}, nil), ProfileSpec(auto, &nv),
		"a chosen class overrides auto")
	autoOverride := transcodev1alpha1.HardwareAuto
	assert.Equal(t, ProfileSpec(cpu, nil), ProfileSpec(cpu, &autoOverride), "an auto override of a pinned profile keeps cpu")
}
```

Run: `go test ./squasharr/worker/ -run TestProfileSpecResolvesAutoToCPU`
Expected: FAIL to compile, "undefined: transcodev1alpha1.HardwareAuto".

- [ ] **Step 2: Edit the types**

`transcodeprofile_types.go`:
- Change the `Hardware` type's marker to `// +kubebuilder:validation:Enum=cpu;nvidia;intel;auto`.
- Add the constant:

  ```go
	// HardwareAuto prefers a GPU class with a labelled GPU node and a free slot,
	// else cpu, chosen per task at dispatch (spec §18.5).
	HardwareAuto Hardware = "auto"
  ```

- Change `TranscodeProfileSpec.Hardware`'s default marker to `// +kubebuilder:default="auto"`, and
  document both values: auto is chosen per task, with CPU fallback; cpu, nvidia and intel are
  pinned and never fall back.
- `ActiveDeadline`: "ActiveDeadline is each task's deadline, enforced by the worker; a task past it is blocked as DeadlineExceeded."
- `TTLSecondsAfterFinished`: "Deprecated: ignored. Transcode pools never finish; this is removed at the next API version."

`transcodejob_types.go`:
- Add `ConditionBlocked = "Blocked"` to the condition-type constants, with the comment "a
  terminal Failed job squasharr will not retry; delete the TranscodeJob to retry (spec §18.4)".
- In `TranscodeJobStatus`, after `StderrTail`:

```go
	// WorkerPod is the pool pod running this job's current attempt, so
	// `kubectl logs` can find it. Empty when no worker has claimed it.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	WorkerPod string `json:"workerPod,omitempty"`

	// Hardware is the class the current attempt was dispatched to.
	// +optional
	Hardware Hardware `json:"hardware,omitempty"`

	// FallbackReason, once set, keeps an auto job on CPU: why its GPU attempt
	// was abandoned (spec §18.5).
	// +optional
	// +kubebuilder:validation:MaxLength=256
	FallbackReason string `json:"fallbackReason,omitempty"`

	// NextAttemptAt holds a requeued job back from dispatch until then.
	// +optional
	NextAttemptAt *metav1.Time `json:"nextAttemptAt,omitempty"`
```

Doc changes, each replacing the field's existing first sentence:
- `JobRef`: "JobRef names the pool Job whose workers take this job's task (one per profile and hardware class)."
- `Attempts`: "Attempts counts dispatches: each publish of this job's task increments it."
- The `TranscodeJobStatus` type comment at line 249, which says the worker (squasharr-worker)
  owns progress and result: replace that sentence with "squasharr writes every field, from its
  reconciler and from the worker's status events (spec §18.2)."
- `TranscodeJobSpec.Hardware`: "overrides the profile's hardware for this job; auto is allowed."

Add two print columns to the `TranscodeJob` type's markers (lines 314-320):

```go
// +kubebuilder:printcolumn:name="Hardware",type=string,JSONPath=".status.hardware"
// +kubebuilder:printcolumn:name="Worker",type=string,JSONPath=".status.workerPod",priority=1
```

- [ ] **Step 3: Resolve `auto` in `worker.ProfileSpec`**

In `squasharr/worker/profile.go:46`, where the effective hardware is computed (an override, else
`spec.Hardware`), resolve `auto` before converting:

```go
	hw := spec.Hardware
	if hardware != nil && *hardware != "" && *hardware != transcodev1alpha1.HardwareAuto {
		hw = *hardware
	}
	if hw == transcodev1alpha1.HardwareAuto || hw == "" {
		hw = transcodev1alpha1.HardwareCPU // auto with no class chosen yet plans for CPU
	}
```

Use `hw` wherever the function used the override or the spec value.

- [ ] **Step 4: Regenerate and verify**

Run: `make generate manifests && go test ./squasharr/worker/ ./pkg/crdcheck/... ./api/... ./squasharr/controller/transcodeprofile/...`
Expected: PASS, with `KUBEBUILDER_ASSETS` exported. `TestNoCRDDefaultIsUnreachableFromGo` holds:
`hardware` is an `omitempty` string, so a Go zero gets `auto`, and an explicit `cpu` survives.

- [ ] **Step 5: Commit**

```bash
git add api/transcode config/crd api/applyconfiguration squasharr/worker/profile.go squasharr/worker/unit_test.go
git commit -m 'feat(api): TranscodeJob workerPod, hardware, fallbackReason, nextAttemptAt and Blocked; hardware auto is the default' -- api/transcode config/crd api/applyconfiguration squasharr/worker/profile.go squasharr/worker/unit_test.go
```

---

### Task 5: The worker package stops talking to Kubernetes

The encode, verify and swap sequence becomes `worker.Process(ctx, task.Task, Options) Outcome`.
The old `worker.Run(ctx, client, Options)` moves to `squasharr/run.go` as a short transitional
adapter; Task 10 deletes it. The envtests are rewritten to call `Process` on a task built by the
real `BuildTask` from real, apiserver-defaulted CRs, so their inputs come from the real producer.

**Files:**
- Create: `squasharr/worker/process.go`.
- Modify: `squasharr/worker/run.go`: `runner` works from a `task.Task`, and `Run` is removed from
  this package.
- Modify: `squasharr/run.go`: `runWorker` does its own Gets and status apply around `Process`.
- Modify: `squasharr/worker/worker_envtest_test.go`, `squasharr/worker/telemetry_test.go`: call
  `Process`.
- Test: `squasharr/worker/imports_test.go`.

**Interfaces:**
- Consumes: `task.Task`, `worker.BuildTask` (Task 3).
- Produces:
  ```go
  type Outcome struct {
  	Code       int                       // ExitOK, ExitRetriable, ExitInvalidSource, ExitVerifyFailed
  	Err        error
  	Result     *transcodev1alpha1.Result // set when Code == ExitOK
  	StderrTail string
  	Reason     task.Reason               // SourceChanged, GPUUnavailable or GPUEncodeFailed when Process can tell; else empty
  }
  func Process(ctx context.Context, t task.Task, o Options) Outcome
  // Options loses JobName and Namespace and gains:
  //   OnProgress func(context.Context, transcodev1alpha1.Progress) error // nil drops status-shaped progress
  ```

- [ ] **Step 1: Write the failing import guard**

`squasharr/worker/imports_test.go`:

```go
package worker

import (
	"os/exec"
	"strings"
	"testing"
)

// forbiddenForWorker is what the NATS-only worker must never link: spec §9.
var forbiddenForWorker = []string{
	"k8s.io/client-go/",
	"sigs.k8s.io/controller-runtime/pkg/client",
	"sigs.k8s.io/controller-runtime/pkg/manager",
	"github.com/mediactl/clustarr/pkg/k8s",
}

func TestWorkerPackageImportsNoKubernetesClient(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, out)
	}
	for _, dep := range strings.Fields(string(out)) {
		for _, bad := range forbiddenForWorker {
			if dep == strings.TrimSuffix(bad, "/") || strings.HasPrefix(dep, bad) {
				t.Errorf("squasharr/worker depends on %s", dep)
			}
		}
		if dep == "github.com/mediactl/clustarr/pkg/obs" {
			t.Errorf("squasharr/worker depends on pkg/obs, which links controller-runtime; use pkg/obs/logging or tracing")
		}
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./squasharr/worker/ -run TestWorkerPackageImportsNoKubernetesClient`
Expected: FAIL, with "squasharr/worker depends on sigs.k8s.io/controller-runtime/pkg/client" and
"…pkg/k8s".

- [ ] **Step 3: Convert `runner` to a task**

In `squasharr/worker/run.go`:
- Replace the `runner` fields `c client.Client`, `key types.NamespacedName` and
  `jobUID types.UID` with `t task.Task` and `out Outcome`.
- Delete `Run`, `getErr` and `applyWorkerStatus`.
- Delete the `JobName` and `Namespace` fields from `Options`. Add `OnProgress` (see Interfaces).
- Change `run(ctx)` step by step. Every other line stays as it is.

| Current lines | Becomes |
|---|---|
| 277 `r.key = …` | removed |
| 289-307 three `Get`s | removed |
| 309 `source := filepath.Clean(tj.Spec.SourcePath)` | `source := r.t.SourcePath` |
| 314-321 `List` + `rootFolderFor` | `if !within(r.t.Root.Path, source) { return invalidSource("source %s is outside root folder %s", source, r.t.Root.Path) }` |
| 322-329 recycle bin | `binLogical := r.t.Root.RecycleBin` (then `localPath` as today) |
| 334-347 `swap{…}`, `OutputPath(…)`, output root check | `replace: ReplaceSource(r.t.Profile.Spec.Policy)`, `recycle: RecycleBin(r.t.Profile.Spec.Policy)`, `sw.out = r.t.OutputPath`; when not in place: `if r.t.OutputRoot == "" \|\| !within(r.t.OutputRoot, sw.out) { return invalidSource(…) }` |
| 348 tag | `tag := r.t.Profile.Name + "@" + r.t.Profile.Hash` |
| 349 `tj.Spec.SourceProbeHash == ""` | `r.t.SourceProbeHash == ""` |
| 354-362 `finishElsewhere(ctx, sw, tj.Spec.SourceProbeHash, mf.Spec.SizeBytes)` | `finishElsewhere(ctx, sw, r.t.SourceProbeHash, r.t.SourceSizeBytes)` |
| 373-379 `alreadySwappedOrChanged(ctx, &mf, …)` | change its first parameter from `mf *catalogv1alpha1.MediaFile` to `sourceSize int64`, and pass `r.t.SourceSizeBytes` |
| 391 `info.Modifier = string(mf.Spec.Quality.Modifier)` | `info.Modifier = r.t.SourceModifier` |
| 400 `ProfileSpec(tp.Spec, tj.Spec.Hardware)` | `ProfileSpec(r.t.Profile.Spec, r.t.Profile.Hardware)` |
| 413 `PlanMeta{ProfileName: tp.Name, ProfileHash: tp.Status.Hash, …}` | `PlanMeta{ProfileName: r.t.Profile.Name, ProfileHash: r.t.Profile.Hash, …}` |
| 419 `compareWithRecordedPlan(ctx, tj.Status.Plan, plan)` | change the parameter to `recorded string` and pass `r.t.ArgsHash` (an empty string skips the comparison) |
| 458 `MaxOutputToSourcePercent(tp.Spec.Policy)` | `MaxOutputToSourcePercent(r.t.Profile.Spec.Policy)` |

- `finish(ctx, out, local, sourceSize)` keeps its body. Instead of applying status, it stores
  `r.out.Result = &res`: the `Result{OutputPath, OutputSizeBytes, OutputToSourcePercent, MediaInfo}`
  it builds today. It returns nil, or the output-stat error classified as `retriable`.
- `applyProgress` becomes: call `r.o.OnProgress(ctx, p)` when it is non-nil, else return nil.
- `applyStderrTail` becomes `r.out.StderrTail = tail`.
- `encode` builds its telemetry `schema.Ref` as `r.t.Job`.

- [ ] **Step 4: Write `squasharr/worker/process.go`**

```go
// Outcome is how one Process call ended. Code classifies it the way the Job
// exit codes used to: ExitOK, ExitRetriable, ExitInvalidSource or
// ExitVerifyFailed (run.go).
type Outcome struct {
	Code       int
	Err        error
	Result     *transcodev1alpha1.Result
	StderrTail string
	Reason     task.Reason
}

// Process transcodes one task: re-probe against SourceProbeHash, plan, check
// free space, encode, verify, swap. It reads and writes files only; what the
// result means for the TranscodeJob is the caller's to report. The crash
// matrix in doc.go is unchanged because the swap order is.
func Process(ctx context.Context, t task.Task, o Options) Outcome {
	o = o.withDefaults()
	ctx, span := tracing.Start(ctx, "squasharr.worker.process") // same span helper Run used
	defer span.End()
	r := &runner{o: o, t: t, started: o.Now()}
	err := r.run(ctx)
	r.out.Code, r.out.Err = ExitCode(err), err
	var f *failure
	if errors.As(err, &f) && f.reason != "" {
		r.out.Reason = f.reason
	}
	r.observeOutcome(r.out.Code)
	return r.out
}
```

(Use the span call `Run` used, at `run.go:240-258`.)

**Name the causes squasharr decides on (spec §18.1, §18.3).**
- Give run.go's `failure` type (line 191) a `reason task.Reason` field, and add three
  constructors beside `retriable`, `invalidSource` and `verifyFailed` (lines 199-207):

```go
// sourceChanged: the live file is not the one that was planned. squasharr
// fails it unblocked, and the profile replaces the job for the new file.
func sourceChanged(format string, a ...any) error {
	return &failure{code: ExitInvalidSource, reason: task.ReasonSourceChanged, err: fmt.Errorf(format, a...)}
}

// gpuUnavailable: ffmpeg lacks the GPU encoder the plan wants.
func gpuUnavailable(format string, a ...any) error {
	return &failure{code: ExitRetriable, reason: task.ReasonGPUUnavailable, err: fmt.Errorf(format, a...)}
}

// gpuEncodeFailed: ffmpeg failed while encoding on a GPU tier.
func gpuEncodeFailed(err error) error {
	return &failure{code: ExitRetriable, reason: task.ReasonGPUEncodeFailed, err: err}
}
```

- Use them at:
  - **The probe-hash mismatches.** `run.go:373-379`, where the not-in-place mismatch and the
    in-place mismatch without this profile's tag currently return `invalidSource`, and
    `run.go:466-469`, where the source changed during the encode. All three become
    `sourceChanged`.
  - **`FallbackTier` not ok** (`run.go:401-412`), when the wanted tier is a GPU tier: this becomes
    `gpuUnavailable`.
  - **An ffmpeg `RunError` in `encode`** (`run.go:610-649`), when `plan.Tier` is a GPU tier: this
    becomes `gpuEncodeFailed`.

  "A GPU tier" is any `transcode.Tier` other than the libx265 software tier (see
  `pkg/transcode`'s tier constants).
- Add to `worker_envtest_test.go`: a test that edits the source after planning (the existing
  "ExitsThreeBeforeRunningFFmpeg" fixture) and asserts
  `out.Reason == task.ReasonSourceChanged`. Add a unit test that `ExitCode` still maps each new
  constructor to its code.

- [ ] **Step 5: Move the adapter into `squasharr/run.go`**

`runWorker(ctx, o)` keeps the `ctrl.GetConfig` and `client.New`, the trace parent and the
telemetry bus exactly as today (`run.go:501-540`). It replaces `worker.Run(ctx, c, wo)` with this
function, which Task 10 deletes:

```go
// runWorkerJob is the transitional in-cluster adapter (removed in Task 10 of
// docs/superpowers/plans/2026-09-23-transcode-worker-pools.md): it reads what
// BuildTask needs, runs worker.Process, and writes the worker's status.
func runWorkerJob(ctx context.Context, c client.Client, o Options, wo worker.Options) (int, error) {
	key := types.NamespacedName{Namespace: o.Namespace, Name: o.JobName}
	var tj transcodev1alpha1.TranscodeJob
	if err := c.Get(ctx, key, &tj); err != nil {
		return exitForGet(err), err
	}
	var tp transcodev1alpha1.TranscodeProfile
	if err := c.Get(ctx, types.NamespacedName{Name: tj.Spec.ProfileRef}, &tp); err != nil {
		return exitForGet(err), err
	}
	if tp.Status.Hash == "" {
		return worker.ExitRetriable, fmt.Errorf("TranscodeProfile %s has no status.hash yet", tp.Name)
	}
	var mf catalogv1alpha1.MediaFile
	if err := c.Get(ctx, types.NamespacedName{Namespace: tj.Namespace, Name: tj.Spec.MediaFileRef}, &mf); err != nil {
		return exitForGet(err), err
	}
	var folders catalogv1alpha1.RootFolderList
	if err := c.List(ctx, &folders, client.InNamespace(tj.Namespace)); err != nil {
		return worker.ExitRetriable, err
	}
	t, err := worker.BuildTask(&tj, &tp, &mf, folders.Items, tj.Status.Attempts, tp.Spec.Hardware)
	if err != nil {
		return worker.ExitInvalidSource, err
	}
	apply := func(change func(*transcodev1alpha1.TranscodeJobStatus)) error {
		var fresh transcodev1alpha1.TranscodeJob
		if err := c.Get(ctx, key, &fresh); err != nil {
			return err
		}
		change(&fresh.Status)
		return status.Patch(ctx, c, k8s.ManagerSquasharrWorker, &fresh, nil)
	}
	wo.OnProgress = func(_ context.Context, p transcodev1alpha1.Progress) error {
		return apply(func(s *transcodev1alpha1.TranscodeJobStatus) { s.Progress = &p })
	}
	out := worker.Process(ctx, t, wo)
	if out.StderrTail != "" {
		_ = apply(func(s *transcodev1alpha1.TranscodeJobStatus) { s.StderrTail = out.StderrTail })
	}
	if out.Code == worker.ExitOK && out.Result != nil {
		if err := apply(func(s *transcodev1alpha1.TranscodeJobStatus) {
			s.Result = out.Result
			if s.Progress != nil {
				s.Progress.Percent, s.Progress.UpdatedAt = 100, metav1.Now()
			}
		}); err != nil {
			return worker.ExitRetriable, err
		}
	}
	return out.Code, out.Err
}

func exitForGet(err error) int {
	if apierrors.IsNotFound(err) {
		return worker.ExitInvalidSource
	}
	return worker.ExitRetriable
}
```

- [ ] **Step 6: Port the worker envtests to `Process`**

In `squasharr/worker/worker_envtest_test.go`, add this helper and replace every
`Run(ctx, testClient, f.options())` call with `f.process(t, testClient)`. Keep every filesystem,
exit-code and result assertion, reading `out.Result` where a test read `status.result`:

```go
// process runs Process on the task BuildTask renders from the fixture's real,
// apiserver-defaulted objects: the same producer squasharr dispatches with.
func (f *fixture) process(t *testing.T, c client.Client) Outcome {
	t.Helper()
	ctx := context.Background()
	tj := f.get(t, c)
	var tp transcodev1alpha1.TranscodeProfile
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: tj.Spec.ProfileRef}, &tp))
	var mf catalogv1alpha1.MediaFile
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: tj.Namespace, Name: tj.Spec.MediaFileRef}, &mf))
	var folders catalogv1alpha1.RootFolderList
	require.NoError(t, c.List(ctx, &folders, client.InNamespace(tj.Namespace)))
	tk, err := BuildTask(tj, &tp, &mf, folders.Items, 1, tp.Spec.Hardware)
	if err != nil {
		return Outcome{Code: ExitInvalidSource, Err: err}
	}
	return Process(ctx, tk, f.options())
}
```

- Delete the tests that assert managed-fields ownership by `squasharr-worker`: every caller of
  `statusFieldsOwnedBy` (line 646) and the assertions at lines 374 and 464. Task 10 replaces them
  with the controller's single-owner test.
- Delete `f.options()`'s `JobName` and `Namespace` fields.
- In `telemetry_test.go:178`, make the same `Run` → `f.process` replacement.

- [ ] **Step 7: Run everything touched**

Run: `go test ./squasharr/... ./cmd/clustarr/ -run 'Worker|Process|Telemetry|Squasharr'`
Expected: PASS, including `TestWorkerPackageImportsNoKubernetesClient`. The ffmpeg envtests need
`KUBEBUILDER_ASSETS` and `/usr/bin/ffmpeg`. `TestSquasharrWorkerExitCodeReachesTheProcess` still
passes, because `runWorker` keeps its exit codes.

- [ ] **Step 8: Commit**

```bash
git add squasharr/worker squasharr/run.go
git commit -m 'refactor(squasharr): worker.Process runs one task from files alone; the kube adapter moves to runWorker' -- squasharr/worker squasharr/run.go
```

---

### Task 6: The worker loop: pull, lease, renew, report on the stream, settle

**Files:**
- Create: `squasharr/worker/serve.go`, `squasharr/worker/lease.go`, `squasharr/worker/report.go`.
- Test: `squasharr/worker/serve_test.go` (membus and a clockwork fake clock, with a stub
  `Process`).
- Test: `squasharr/worker/lease_nats_test.go` (an embedded nats-server; checks that bucket-TTL
  expiry is extended by `Update`).

**Interfaces:**
- Consumes:
  - Tasks 1-2: `events.PullSubscriber`, `events.TranscodeTaskConsumer`,
    `events.TranscodeLeaseKey`, `events.WorkTranscodeResultSubject`,
    `events.MsgIDForTranscodeEvent`, `events.ConsumerSquasharrResults`
  - Task 3: `task.*`
  - Task 5: `Process`, `Outcome`, and the `Outcome.Reason` Task 5 adds
- Produces:
  ```go
  type ServeOptions struct {
  	Options                          // handed to Process for every task; Options.PodName names this worker
  	ProfileUID, Class, Node string
  	Leases  events.KV
  	Renew, FenceAfter, HeldRetry time.Duration // 20s, 60s, 30s when zero
  	Clock   clockwork.Clock                    // nil: real
  	Process func(context.Context, task.Task, Options) Outcome // nil: Process
  }
  func Serve(ctx context.Context, bus events.Bus, o ServeOptions) error // returns ctx.Err() once drained
  ```
- **Settlement rules (spec §18.1):**
  - Every attempt that finishes publishes `finished`, then acks, whatever the outcome.
  - A drained or fenced delivery publishes no `finished` and naks.
  - A task whose lease someone else holds is naked after `HeldRetry`.
  - A task cancelled for this attempt or a later one is acked with no events.
  - An undecodable task is terminated and dead-lettered.

- [ ] **Step 1: Write the failing tests**

`squasharr/worker/serve_test.go`. The harness runs `Serve` against `membus.New(clock)` with the
default single-node topology, a stub `Process` the test controls, and a results subscription
standing in for squasharr:

```go
package worker

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/squasharr/task"
)

type harness struct {
	t       *testing.T
	ctx     context.Context
	cancel  context.CancelFunc
	clock   *clockwork.FakeClock
	bus     events.Bus
	leases  events.KV
	calls   atomic.Int32
	release chan Outcome // unbuffered: a send means the stub took it
	started chan struct{}
	ended   chan struct{}
	events  chan task.StatusEvent
	done    chan error
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{t: t, clock: clockwork.NewFakeClock(), release: make(chan Outcome),
		started: make(chan struct{}, 8), ended: make(chan struct{}, 8),
		events: make(chan task.StatusEvent, 64), done: make(chan error, 1)}
	h.ctx, h.cancel = context.WithCancel(context.Background())
	t.Cleanup(h.cancel)
	h.bus = membus.New(h.clock)
	require.NoError(t, h.bus.Ensure(h.ctx, events.Default().ForSingleNode()))
	h.leases = h.bus.KV(events.BucketTranscodeLeases)
	rc, ok := events.Default().Consumer(events.ConsumerSquasharrResults)
	require.True(t, ok)
	stop, err := h.bus.Subscribe(context.Background(), rc.Subscription(), func(_ context.Context, m events.Message) error {
		var ev task.StatusEvent
		require.NoError(t, schema.Decode(m.Envelope().Schema, m.Envelope().Data, &ev))
		h.events <- ev
		return nil
	})
	require.NoError(t, err)
	t.Cleanup(stop)
	return h
}

func (h *harness) serve() {
	go func() {
		h.done <- Serve(h.ctx, h.bus, ServeOptions{
			ProfileUID: "puid", Class: "cpu", Node: "n1",
			Options: Options{PodName: "pool-abc"},
			Leases:  h.leases, Clock: h.clock,
			Process: func(ctx context.Context, _ task.Task, o Options) Outcome {
				h.calls.Add(1)
				h.started <- struct{}{}
				defer func() { h.ended <- struct{}{} }()
				if o.OnProgress != nil {
					_ = o.OnProgress(ctx, transcodev1alpha1.Progress{Percent: 40})
				}
				select {
				case out := <-h.release:
					return out
				case <-ctx.Done():
					return Outcome{Code: ExitRetriable, Err: ctx.Err()}
				}
			},
		})
	}()
}

func (h *harness) publish(attempt int32, deadline time.Duration) {
	h.t.Helper()
	tk := task.Task{Job: schema.Ref{Namespace: "media", Name: "tj", UID: "juid"}, Attempt: attempt, Class: "cpu"}
	tk.Deadline.Duration = deadline
	sch, data, err := schema.Encode(tk)
	require.NoError(h.t, err)
	id := events.MsgIDForTranscodeTask("juid", attempt)
	_, err = h.bus.Publish(h.ctx, events.WorkTranscodeTaskSubject("puid", "cpu", "juid"),
		&events.Envelope{ID: id, Schema: sch, Data: data}, events.WithMsgID(id))
	require.NoError(h.t, err)
}

// next waits for the next event of kind, skipping others.
func (h *harness) next(kind task.EventKind) task.StatusEvent {
	h.t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-h.events:
			if ev.Kind == kind {
				return ev
			}
		case <-deadline:
			h.t.Fatalf("no %s event within 5s", kind)
		}
	}
}

func (h *harness) noEvent(kind task.EventKind, within time.Duration) {
	h.t.Helper()
	deadline := time.After(within)
	for {
		select {
		case ev := <-h.events:
			if ev.Kind == kind {
				h.t.Fatalf("unexpected %s event: %+v", kind, ev)
			}
		case <-deadline:
			return
		}
	}
}

func (h *harness) putLease(l task.Lease) {
	h.t.Helper()
	b, _ := json.Marshal(l)
	_, err := h.leases.Put(h.ctx, events.TranscodeLeaseKey("juid"), b)
	require.NoError(h.t, err)
}

func (h *harness) leaseGone() bool {
	_, err := h.leases.Get(context.Background(), events.TranscodeLeaseKey("juid"))
	return err != nil
}

func TestServeReportsClaimedProgressFinishedAndAcks(t *testing.T) {
	h := newHarness(t)
	h.serve()
	h.publish(1, 0)
	claimed := h.next(task.EventClaimed)
	assert.Equal(t, uint64(1), claimed.Seq)
	assert.Equal(t, "pool-abc", claimed.Pod)
	assert.Equal(t, "n1", claimed.Node)
	assert.Equal(t, int32(1), claimed.Attempt)
	assert.False(t, h.leaseGone(), "the lease is held while Process runs")
	progress := h.next(task.EventProgress)
	assert.Equal(t, uint64(2), progress.Seq)
	assert.Equal(t, int32(40), progress.Progress.Percent)

	h.release <- Outcome{Code: ExitOK, Result: &transcodev1alpha1.Result{OutputPath: "/data/x.mkv"}}
	fin := h.next(task.EventFinished)
	assert.Equal(t, task.OutcomeSucceeded, fin.Outcome)
	assert.Equal(t, "/data/x.mkv", fin.Result.OutputPath)
	assert.Equal(t, uint64(3), fin.Seq)
	require.Eventually(t, h.leaseGone, 5*time.Second, 10*time.Millisecond, "the lease is released after finished")
	h.clock.Advance(2 * time.Minute) // past AckWait: an acked task never returns
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, int32(1), h.calls.Load())
}

// The queue retries nothing: every outcome is reported and acked, and
// squasharr decides what happens next (spec §18.3).
func TestServeReportsEveryOutcomeAndAcks(t *testing.T) {
	for _, tc := range []struct {
		out    Outcome
		reason task.Reason
	}{
		{Outcome{Code: ExitRetriable}, task.ReasonRetriable},
		{Outcome{Code: ExitInvalidSource}, task.ReasonInvalidSource},
		{Outcome{Code: ExitVerifyFailed}, task.ReasonVerifyFailed},
		{Outcome{Code: ExitInvalidSource, Reason: task.ReasonSourceChanged}, task.ReasonSourceChanged},
		{Outcome{Code: ExitRetriable, Reason: task.ReasonGPUUnavailable}, task.ReasonGPUUnavailable},
		{Outcome{Code: ExitRetriable, Reason: task.ReasonGPUEncodeFailed, StderrTail: "nvenc: no device"}, task.ReasonGPUEncodeFailed},
	} {
		t.Run(string(tc.reason), func(t *testing.T) {
			h := newHarness(t)
			h.serve()
			h.publish(1, 0)
			<-h.started
			h.release <- tc.out
			fin := h.next(task.EventFinished)
			assert.Equal(t, task.OutcomeFailed, fin.Outcome)
			assert.Equal(t, tc.reason, fin.Reason)
			assert.Equal(t, tc.out.StderrTail, fin.StderrTail)
			h.clock.Advance(time.Hour)
			time.Sleep(100 * time.Millisecond)
			assert.Equal(t, int32(1), h.calls.Load(), "a reported task is acked, never redelivered")
		})
	}
}

func TestServeNaksATaskAnotherWorkerHolds(t *testing.T) {
	h := newHarness(t)
	h.putLease(task.Lease{Job: schema.Ref{UID: "juid"}, Attempt: 1, State: task.LeaseHeld, Pod: "other"})
	h.serve()
	h.publish(1, 0)
	h.noEvent(task.EventClaimed, 300*time.Millisecond)
	assert.Zero(t, h.calls.Load(), "Process must not run while another worker's lease lives")
}

// Review Focus 3.
func TestServeReplacesACancelledLeaseFromAnEarlierAttempt(t *testing.T) {
	h := newHarness(t)
	h.putLease(task.Lease{Job: schema.Ref{UID: "juid"}, Attempt: 1, State: task.LeaseCancelled})
	h.serve()
	h.publish(2, 0)
	assert.Equal(t, int32(2), h.next(task.EventClaimed).Attempt, "attempt 2 was dropped by attempt 1's cancel marker")
	<-h.started
	h.release <- Outcome{Code: ExitOK, Result: &transcodev1alpha1.Result{}}
	assert.Equal(t, int32(2), h.next(task.EventFinished).Attempt)
}

func TestServeDropsATaskCancelledForItsOwnAttempt(t *testing.T) {
	h := newHarness(t)
	h.putLease(task.Lease{Job: schema.Ref{UID: "juid"}, Attempt: 2, State: task.LeaseCancelled})
	h.serve()
	h.publish(2, 0)
	h.noEvent(task.EventClaimed, 300*time.Millisecond)
	assert.Zero(t, h.calls.Load())
}

func TestServeCancelsRunningWorkWhenTheLeaseIsCancelled(t *testing.T) {
	h := newHarness(t)
	h.serve()
	h.publish(1, 0)
	<-h.started
	h.putLease(task.Lease{Job: schema.Ref{UID: "juid"}, Attempt: 1, State: task.LeaseCancelled})
	h.clock.Advance(20 * time.Second) // the next renewal sees the revision change
	fin := h.next(task.EventFinished)
	assert.Equal(t, task.OutcomeCancelled, fin.Outcome)
	assert.Equal(t, task.ReasonCancelled, fin.Reason)
}

func TestServeSelfFencesWhenRenewalsFail(t *testing.T) {
	h := newHarness(t)
	failing := &failingUpdates{KV: h.leases}
	h.leases = failing
	h.serve()
	h.publish(1, 0)
	<-h.started
	failing.fail.Store(true)
	for i := 0; i < 3; i++ { // three missed renewals: 60s
		h.clock.Advance(20 * time.Second)
		time.Sleep(20 * time.Millisecond)
	}
	select {
	case <-h.ended: // Process saw its context cancelled: the encode was stopped
	case <-time.After(5 * time.Second):
		t.Fatal("a worker that cannot renew its lease kept encoding past FenceAfter")
	}
	h.noEvent(task.EventFinished, 300*time.Millisecond) // a fenced worker reports nothing
}

// Review Focus 4.
func TestServeFinishesASucceededTaskDespiteDrain(t *testing.T) {
	h := newHarness(t)
	h.serve()
	h.publish(1, 0)
	<-h.started
	h.release <- Outcome{Code: ExitOK, Result: &transcodev1alpha1.Result{OutputPath: "/data/x.mkv"}}
	h.cancel() // SIGTERM lands as Process returns
	assert.Equal(t, task.OutcomeSucceeded, h.next(task.EventFinished).Outcome)
	assert.ErrorIs(t, <-h.done, context.Canceled)
}

func TestServeDrainNaksUnfinishedWork(t *testing.T) {
	h := newHarness(t)
	h.serve()
	h.publish(1, 0)
	<-h.started
	h.cancel()
	assert.ErrorIs(t, <-h.done, context.Canceled)
	h.noEvent(task.EventFinished, 300*time.Millisecond)
	assert.True(t, h.leaseGone(), "drained work releases its lease so the next pod can start at once")
}

func TestServeEnforcesTheTaskDeadline(t *testing.T) {
	h := newHarness(t)
	h.serve()
	h.publish(1, time.Minute)
	<-h.started
	h.clock.Advance(61 * time.Second)
	fin := h.next(task.EventFinished)
	assert.Equal(t, task.OutcomeFailed, fin.Outcome)
	assert.Equal(t, task.ReasonDeadlineExceeded, fin.Reason)
}

type failingUpdates struct {
	events.KV
	fail atomic.Bool
}

func (f *failingUpdates) Update(ctx context.Context, key string, val []byte, rev uint64) (uint64, error) {
	if f.fail.Load() {
		return 0, events.ErrClosed
	}
	return f.KV.Update(ctx, key, val, rev)
}
```

`squasharr/worker/lease_nats_test.go` starts an embedded server the way `telemetry_test.go:62`
`progressKV` does, with a lease bucket TTL of 2s:

```go
func TestLeaseBucketTTLIsExtendedByUpdate(t *testing.T) {
	kv := leaseKVWithTTL(t, 2*time.Second) // embedded nats-server; BucketSpec{Name: events.BucketTranscodeLeases, TTL: 2s, History: 1, LimitMarkerTTL: time.Minute}
	ctx := context.Background()
	rev, err := kv.Create(ctx, "lease.j", []byte("held"))
	require.NoError(t, err)
	for i := 0; i < 4; i++ { // 4s of renewals, twice the TTL
		time.Sleep(time.Second)
		rev, err = kv.Update(ctx, "lease.j", []byte("held"), rev)
		require.NoError(t, err, "renewal %d", i)
	}
	_, err = kv.Create(ctx, "lease.j", []byte("other"))
	assert.ErrorIs(t, err, events.ErrKeyExists, "a renewed lease must still be held")
	time.Sleep(3 * time.Second) // no renewals past the TTL
	_, err = kv.Create(ctx, "lease.j", []byte("other"))
	assert.NoError(t, err, "a lapsed lease must be claimable: the server expires it")
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./squasharr/worker/ -run 'TestServe|TestLease'`
Expected: FAIL to compile, "undefined: Serve".

- [ ] **Step 3: Write `squasharr/worker/lease.go`**

```go
var (
	errCancelled = errors.New("task withdrawn by squasharr")
	errFenced    = errors.New("lease could not be renewed; stopped before it could lapse")
	errDeadline  = errors.New("task deadline exceeded")
)

// claim takes t's lease. It reports the lease it found when it could not.
func (s *server) claim(ctx context.Context, t task.Task) (cur task.Lease, rev uint64, ok bool, err error) {
	key := events.TranscodeLeaseKey(t.Job.UID)
	val, _ := json.Marshal(s.held(t))
	for try := 0; try < 2; try++ {
		rev, err = s.o.Leases.Create(ctx, key, val)
		if err == nil {
			return task.Lease{}, rev, true, nil
		}
		if !errors.Is(err, events.ErrKeyExists) {
			return task.Lease{}, 0, false, err
		}
		e, err := s.o.Leases.Get(ctx, key)
		if errors.Is(err, events.ErrKeyNotFound) {
			continue // lapsed between Create and Get: try again once
		}
		if err != nil {
			return task.Lease{}, 0, false, err
		}
		if err := json.Unmarshal(e.Value, &cur); err != nil {
			return task.Lease{}, 0, false, err
		}
		if cur.State == task.LeaseCancelled && cur.Attempt < t.Attempt {
			rev, err = s.o.Leases.Update(ctx, key, val, e.Revision)
			return cur, rev, err == nil, nil
		}
		return cur, 0, false, nil
	}
	return task.Lease{}, 0, false, events.ErrKeyExists
}

func (s *server) held(t task.Task) task.Lease {
	return task.Lease{Job: t.Job, Attempt: t.Attempt, State: task.LeaseHeld,
		Pod: s.o.PodName, Node: s.o.Node, Since: s.clock.Now().UTC()}
}

// renew keeps the lease and the ack window alive until ctx ends. A revision
// it did not write means squasharr cancelled the task, or the lease lapsed
// and someone else took it: stop at once. Plain errors are tolerated until
// FenceAfter since the last good renewal, then the work is stopped: FenceAfter
// is 30s short of the lease TTL, so the work ends before anyone can claim it.
func (s *server) renew(ctx context.Context, stop context.CancelCauseFunc, m events.Message, t task.Task, rev *uint64) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		key := events.TranscodeLeaseKey(t.Job.UID)
		val, _ := json.Marshal(s.held(t))
		tick := s.clock.NewTicker(s.o.Renew)
		defer tick.Stop()
		lastOK := s.clock.Now()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.Chan():
			}
			_ = m.InProgress(ctx)
			next, err := s.o.Leases.Update(ctx, key, val, *rev)
			switch {
			case err == nil:
				*rev, lastOK = next, s.clock.Now()
			case errors.Is(err, events.ErrRevisionMismatch) || errors.Is(err, events.ErrKeyNotFound):
				if e, gerr := s.o.Leases.Get(ctx, key); gerr == nil {
					var cur task.Lease
					if json.Unmarshal(e.Value, &cur) == nil && cur.State == task.LeaseCancelled && cur.Attempt >= t.Attempt {
						stop(errCancelled)
						return
					}
				}
				stop(errFenced)
				return
			default:
				if s.clock.Since(lastOK) >= s.o.FenceAfter {
					stop(errFenced)
					return
				}
			}
		}
	}()
	return done
}
```

- [ ] **Step 4: Write `squasharr/worker/report.go`**

```go
// reporter publishes one delivery's status events in order. Progress arrives
// from Process's reporter goroutine, so publishes are serialised.
type reporter struct {
	mu       sync.Mutex
	s        *server
	t        task.Task
	delivery uint64
	seq      uint64
}

func (p *reporter) publish(ctx context.Context, ev task.StatusEvent) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seq++
	ev.Job, ev.Attempt, ev.Delivery, ev.Seq = p.t.Job, p.t.Attempt, p.delivery, p.seq
	ev.Pod, ev.Node, ev.At = p.s.o.PodName, p.s.o.Node, p.s.clock.Now().UTC()
	sch, data, err := schema.Encode(ev)
	if err != nil {
		return err
	}
	id := events.MsgIDForTranscodeEvent(p.t.Job.UID, p.t.Attempt, p.delivery, p.seq)
	env := &events.Envelope{ID: id, Type: "transcode.StatusEvent", Schema: sch,
		Source: "squasharr-worker@" + version.Version, Key: p.t.Job.Namespace + "/" + p.t.Job.Name,
		Time: ev.At, Data: data}
	_, err = p.s.bus.Publish(ctx, events.WorkTranscodeResultSubject(p.t.Job.UID), env,
		events.WithMsgID(id), events.WithExpectStream(events.StreamWorkSquasharr))
	return err
}

// reasonFor names a Process outcome: Process's own reason when it has one
// (SourceChanged, GPUUnavailable, GPUEncodeFailed), else the code's.
func reasonFor(out Outcome) task.Reason {
	if out.Reason != "" {
		return out.Reason
	}
	switch out.Code {
	case ExitInvalidSource:
		return task.ReasonInvalidSource
	case ExitVerifyFailed:
		return task.ReasonVerifyFailed
	default:
		return task.ReasonRetriable
	}
}
```

- [ ] **Step 5: Write `squasharr/worker/serve.go`**

```go
// Serve pulls this pool's tasks one at a time and runs each to a settled
// message (spec §9, §18.1). It never returns on its own except for a
// worker-level failure; a cancelled ctx (SIGTERM) returns ctx.Err() once
// in-flight work is drained, and the binary maps that to WorkerExitDrained.
func Serve(ctx context.Context, bus events.Bus, o ServeOptions) error {
	s := &server{o: o.withDefaults()}
	s.clock = s.o.Clock
	ps, ok := bus.(events.PullSubscriber)
	if !ok {
		return fmt.Errorf("squasharr worker: %T cannot pull one message at a time", bus)
	}
	s.bus, s.sub = bus, events.TranscodeTaskConsumer(o.ProfileUID, o.Class).Subscription()
	p, err := ps.Pull(ctx, s.sub)
	if err != nil {
		return fmt.Errorf("squasharr worker: pull %s: %w", s.sub.Durable, err)
	}
	defer p.Stop()
	for {
		mctx, m, err := p.Next(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("squasharr worker: next task: %w", err)
		}
		s.handle(mctx, m)
	}
}

type server struct {
	o     ServeOptions
	clock clockwork.Clock
	bus   events.Bus
	sub   events.Subscription
}

func (o ServeOptions) withDefaults() ServeOptions {
	if o.Renew == 0 {
		o.Renew = 20 * time.Second
	}
	if o.FenceAfter == 0 {
		o.FenceAfter = 60 * time.Second
	}
	if o.HeldRetry == 0 {
		o.HeldRetry = 30 * time.Second
	}
	if o.Clock == nil {
		o.Clock = clockwork.NewRealClock()
	}
	if o.Process == nil {
		o.Process = Process
	}
	return o
}

func (s *server) handle(ctx context.Context, m events.Message) {
	settle := func() (context.Context, context.CancelFunc) { // survives a drain
		return context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	}
	log := logging.FromContext(ctx)
	var t task.Task
	if err := schema.Decode(m.Envelope().Schema, m.Envelope().Data, &t); err != nil {
		sctx, cancel := settle()
		defer cancel()
		subj, dl := events.DeadLetter(m, s.sub.Durable, "undecodable task: "+err.Error())
		_, _ = s.bus.Publish(sctx, subj, dl)
		_ = m.Term(sctx, "undecodable task")
		return
	}
	cur, rev, ok, err := s.claim(ctx, t)
	switch {
	case err != nil || (!ok && cur.State == task.LeaseHeld):
		sctx, cancel := settle()
		defer cancel()
		_ = m.Nak(sctx, s.o.HeldRetry)
		return
	case !ok: // cancelled for this attempt or a later one
		sctx, cancel := settle()
		defer cancel()
		_ = m.Ack(sctx)
		return
	}

	rep := &reporter{s: s, t: t, delivery: m.Attempt()}
	if err := rep.publish(ctx, task.StatusEvent{Kind: task.EventClaimed}); err != nil {
		log.WarnContext(ctx, "squasharr worker: claimed event not published", "error", err)
	}
	opts := s.o.Options
	opts.OnProgress = func(pctx context.Context, p transcodev1alpha1.Progress) error {
		return rep.publish(pctx, task.StatusEvent{Kind: task.EventProgress, Progress: &p})
	}

	work, stop := context.WithCancelCause(ctx)
	defer stop(nil)
	if d := t.Deadline.Duration; d > 0 {
		timer := s.clock.AfterFunc(d, func() { stop(errDeadline) })
		defer timer.Stop()
	}
	renewed := s.renew(work, stop, m, t, &rev)
	out := s.o.Process(work, t, opts)
	cause := context.Cause(work)
	stop(nil)
	<-renewed

	fin := task.StatusEvent{Kind: task.EventFinished, StderrTail: out.StderrTail}
	if out.Err != nil {
		fin.Message = out.Err.Error()
	}
	switch {
	case errors.Is(cause, errFenced):
		sctx, cancel := settle()
		defer cancel()
		_ = m.Nak(sctx, 0) // the lease is left to lapse; nothing else is safe
		return
	case errors.Is(cause, errCancelled):
		fin.Outcome, fin.Reason = task.OutcomeCancelled, task.ReasonCancelled
	case errors.Is(cause, errDeadline):
		fin.Outcome, fin.Reason = task.OutcomeFailed, task.ReasonDeadlineExceeded
	case out.Code == ExitRetriable && ctx.Err() != nil: // drained mid-encode
		sctx, cancel := settle()
		defer cancel()
		_ = s.o.Leases.DeleteRevision(sctx, events.TranscodeLeaseKey(t.Job.UID), rev)
		_ = m.Nak(sctx, 0)
		return
	case out.Code == ExitOK:
		fin.Outcome, fin.Result = task.OutcomeSucceeded, out.Result
	default:
		fin.Outcome, fin.Reason = task.OutcomeFailed, reasonFor(out)
	}

	// finished is stored before the task can disappear.
	sctx, cancel := settle()
	defer cancel()
	if err := rep.publish(sctx, fin); err != nil {
		log.WarnContext(ctx, "squasharr worker: finished event not published; redelivery will redo it", "error", err)
		_ = m.Nak(sctx, 0)
		return
	}
	_ = s.o.Leases.DeleteRevision(sctx, events.TranscodeLeaseKey(t.Job.UID), rev)
	_ = m.Ack(sctx)
}
```

A redelivery after an unpublished `finished` is safe. On success, Process's tag checks
(`producedEarlier`, `finishElsewhere`) find the swap already done and report success again. On
failure the attempt simply runs again.

- [ ] **Step 6: Run the tests**

Run: `go test ./squasharr/worker/ -run 'TestServe|TestLease' -race`
Expected: PASS. `TestLeaseBucketTTLIsExtendedByUpdate` takes about 7s against the embedded server.

- [ ] **Step 7: Commit**

```bash
git add squasharr/worker/serve.go squasharr/worker/lease.go squasharr/worker/report.go squasharr/worker/serve_test.go squasharr/worker/lease_nats_test.go
git commit -m 'feat(squasharr): the pool worker loop: pull one task, lease, renew, report claimed/progress/finished, ack after finished' -- squasharr/worker/serve.go squasharr/worker/lease.go squasharr/worker/report.go squasharr/worker/serve_test.go squasharr/worker/lease_nats_test.go
```

---

### Task 7: The `squasharr-worker` binary

**Files:**
- Create: `cmd/squasharr-worker/main.go`, `cmd/squasharr-worker/main_test.go`.
- Create: `squasharr/worker/exit.go`.
- Create: `pkg/obs/obsflags/obsflags.go`. Move `bindObservabilityFlags` here from
  `cmd/clustarr/flags.go:198`.
- Create: `pkg/fsops/umask.go` and `pkg/fsops/umask_test.go`. Move `parseUmask` and
  `applyUmaskFromEnv` here from `cmd/clustarr/umask.go`, together with their tests.
- Modify: `cmd/clustarr/flags.go`, `cmd/clustarr/root.go` and `cmd/clustarr/umask.go`, to call
  the moved functions.
- Modify: `Makefile`, so the `build` target also builds `bin/squasharr-worker`.

**Interfaces:**
- Consumes: `worker.Serve` and `ServeOptions` (Task 6).
- Produces:
  ```go
  // squasharr/worker/exit.go
  const (
  	WorkerExitRetriable     = 2  // worker-level: NATS unreachable, ffmpeg missing
  	WorkerExitMisconfigured = 3  // bad or missing environment: the pool Job fails outright
  	WorkerExitDrained       = 10 // SIGTERM: podFailurePolicy ignores it
  )
  // pkg/obs/obsflags
  func Bind(fs *pflag.FlagSet) (*logging.Options, *tracing.Options)
  // pkg/fsops
  func ApplyUmaskFromEnv() error
  ```
- Worker environment: `NATS_URL`, `CLUSTARR_POOL_PROFILE_UID` and `CLUSTARR_POOL_CLASS`
  (required), `POD_NAME` (required), `NODE_NAME` and `CLUSTARR_CPU_LIMIT`. Flag: `--data-dir`,
  plus the observability flags.

- [ ] **Step 1: Move the shared flag and umask code, with no behaviour change**

- Create `pkg/obs/obsflags/obsflags.go` holding `func Bind(fs *pflag.FlagSet) (*logging.Options, *tracing.Options)`.
  Its body is exactly `cmd/clustarr/flags.go:198`'s `bindObservabilityFlags`. Make
  `bindObservabilityFlags` a one-line call to `obsflags.Bind`.
- Move `parseUmask`, `applyUmaskFromEnv` and the `umaskEnv` constant to `pkg/fsops/umask.go` as
  `ParseUmask`, `ApplyUmaskFromEnv` and `UmaskEnv`. The root command calls
  `fsops.ApplyUmaskFromEnv()`.
- Move `cmd/clustarr`'s umask tests to `pkg/fsops/umask_test.go`, renamed to match.

Run: `go test ./cmd/clustarr/ ./pkg/fsops/ ./pkg/obs/...`
Expected: PASS.

- [ ] **Step 2: Write the failing binary tests**

`cmd/squasharr-worker/main_test.go`:

```go
package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/squasharr/worker"
)

const reexecEnv = "SQUASHARR_WORKER_TEST_REEXEC"

func TestMain(m *testing.M) {
	if os.Getenv(reexecEnv) == "1" {
		os.Exit(run(nil, os.Getenv))
	}
	os.Exit(m.Run())
}

func env(kv map[string]string) func(string) string { return func(k string) string { return kv[k] } }

func fakeTools(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, b := range []string{"ffmpeg", "ffprobe"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, b), []byte("#!/bin/sh\nexit 0\n"), 0o755))
	}
	return dir
}

func TestMissingEnvironmentIsMisconfigured(t *testing.T) {
	assert.Equal(t, worker.WorkerExitMisconfigured, run(nil, env(nil)))
}

func TestUnreachableNATSIsRetriable(t *testing.T) {
	t.Setenv("PATH", fakeTools(t))
	assert.Equal(t, worker.WorkerExitRetriable, run(nil, env(map[string]string{
		"NATS_URL": "nats://127.0.0.1:1", "CLUSTARR_POOL_PROFILE_UID": "p", "CLUSTARR_POOL_CLASS": "cpu", "POD_NAME": "w",
	})))
}

func TestSIGTERMIsDrained(t *testing.T) {
	srv, err := natsserver.NewServer(&natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true,
		StoreDir: t.TempDir(), NoLog: true, NoSigs: true})
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(20*time.Second))
	t.Cleanup(srv.Shutdown)
	ensureTopology(t, srv.ClientURL()) // natsbus.New + bus.Ensure(events.Default().ForSingleNode())

	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), reexecEnv+"=1", "PATH="+fakeTools(t), "NATS_URL="+srv.ClientURL(),
		"CLUSTARR_POOL_PROFILE_UID=p", "CLUSTARR_POOL_CLASS=cpu", "POD_NAME=w")
	require.NoError(t, cmd.Start())
	time.Sleep(2 * time.Second) // connected and pulling
	require.NoError(t, cmd.Process.Signal(syscall.SIGTERM))
	err = cmd.Wait()
	var ee *exec.ExitError
	require.ErrorAs(t, err, &ee)
	assert.Equal(t, worker.WorkerExitDrained, ee.ExitCode())
}

// A work-queue Job ends the whole pool when one pod exits 0 (spec §9).
func TestRunNeverReturnsZero(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	require.NoError(t, err)
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "run" {
			return true
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if r, ok := n.(*ast.ReturnStmt); ok && len(r.Results) == 1 {
				if lit, ok := r.Results[0].(*ast.BasicLit); ok && lit.Value == "0" {
					t.Errorf("run returns 0 at offset %d", lit.Pos())
				}
				if sel, ok := r.Results[0].(*ast.SelectorExpr); ok && sel.Sel.Name == "ExitOK" {
					t.Error("run returns worker.ExitOK")
				}
			}
			return true
		})
		return false
	})
}

func TestBinaryImportsNoKubernetesClient(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").CombinedOutput()
	require.NoError(t, err, string(out))
	for _, dep := range strings.Fields(string(out)) {
		for _, bad := range []string{"k8s.io/client-go", "sigs.k8s.io/controller-runtime/pkg/client",
			"sigs.k8s.io/controller-runtime/pkg/manager", "github.com/mediactl/clustarr/pkg/k8s"} {
			if dep == bad || strings.HasPrefix(dep, bad+"/") {
				t.Errorf("cmd/squasharr-worker depends on %s", dep)
			}
		}
		if dep == "github.com/mediactl/clustarr/pkg/obs" {
			t.Error("cmd/squasharr-worker depends on pkg/obs (links controller-runtime)")
		}
	}
}
```

`ensureTopology` connects with `nats.Connect(url)`, builds `natsbus.New(nc)` and calls
`bus.Ensure(context.Background(), events.Default().ForSingleNode())`.

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./cmd/squasharr-worker/`
Expected: FAIL to compile, "undefined: run".

- [ ] **Step 4: Write `squasharr/worker/exit.go` and `cmd/squasharr-worker/main.go`**

`exit.go` holds the three constants from Interfaces, with this comment: "Process-level exit codes
of cmd/squasharr-worker, distinct from the task classifications in run.go: those end up in a
task.Result, never in a pod's exit code."

`main.go`:

```go
// Command squasharr-worker is a transcode pool's pod: a pure consumer of the
// squasharr work queue (spec §9). It holds no Kubernetes credentials; tasks
// arrive over NATS and results leave over NATS.
package main

func main() { os.Exit(run(os.Args[1:], os.Getenv)) }

// run never returns 0: a work-queue Job ends when any pod succeeds.
func run(args []string, getenv func(string) string) int {
	fs := pflag.NewFlagSet("squasharr-worker", pflag.ContinueOnError)
	dataDir := fs.String("data-dir", worker.LogicalDataRoot, "Where the RWX /data volume is mounted.")
	lo, to := obsflags.Bind(fs)
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, "squasharr-worker:", err)
		return worker.WorkerExitMisconfigured
	}
	if err := fsops.ApplyUmaskFromEnv(); err != nil {
		fmt.Fprintln(os.Stderr, "squasharr-worker:", err)
		return worker.WorkerExitMisconfigured
	}
	need := map[string]string{}
	for _, k := range []string{"NATS_URL", "CLUSTARR_POOL_PROFILE_UID", "CLUSTARR_POOL_CLASS", "POD_NAME"} {
		if need[k] = getenv(k); need[k] == "" {
			fmt.Fprintf(os.Stderr, "squasharr-worker: $%s is required\n", k)
			return worker.WorkerExitMisconfigured
		}
	}

	ctx, stopSignals := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stopSignals()
	ctx = logging.NewContext(ctx, logging.New(*lo))
	log := logging.FromContext(ctx)
	to.ServiceName = "squasharr-worker"
	shutdown, err := tracing.Setup(ctx, *to)
	if err != nil {
		log.ErrorContext(ctx, "tracing", "error", err)
		return worker.WorkerExitMisconfigured
	}
	defer func() {
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = shutdown(sctx)
	}()

	for _, bin := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(bin); err != nil {
			log.ErrorContext(ctx, "not available", "binary", bin, "error", err)
			return worker.WorkerExitRetriable
		}
	}
	nc, err := nats.Connect(need["NATS_URL"], nats.Name("squasharr-worker/"+need["POD_NAME"]))
	if err != nil {
		log.ErrorContext(ctx, "nats connect", "error", err)
		return worker.WorkerExitRetriable
	}
	defer nc.Close()
	bus, err := natsbus.New(nc, natsbus.WithHooks(events.Hooks{
		BeforePublish: tracing.Inject, AfterReceive: tracing.Extract, // obs.BusHooks, without pkg/obs
	}))
	if err != nil {
		log.ErrorContext(ctx, "bus", "error", err)
		return worker.WorkerExitRetriable
	}
	err = worker.Serve(ctx, bus, worker.ServeOptions{
		Options: worker.Options{
			DataDir: *dataDir, Threads: worker.ThreadsFromEnv(), PodName: need["POD_NAME"],
			Telemetry: bus.KV(events.BucketProgress),
		},
		ProfileUID: need["CLUSTARR_POOL_PROFILE_UID"], Class: need["CLUSTARR_POOL_CLASS"], Node: getenv("NODE_NAME"),
		Leases: bus.KV(events.BucketTranscodeLeases), // status events go to the stream through bus
	})
	if ctx.Err() != nil {
		return worker.WorkerExitDrained
	}
	log.ErrorContext(ctx, "serve", "error", err)
	return worker.WorkerExitRetriable
}
```

In the Makefile `build` target, add a second line mirroring the first:

```make
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X github.com/mediactl/clustarr/pkg/version.Version=$(shell git describe --tags --always --dirty 2>/dev/null || echo dev)" -o bin/squasharr-worker ./cmd/squasharr-worker
```

- [ ] **Step 5: Run the tests and the build**

Run: `go test ./cmd/squasharr-worker/ ./squasharr/worker/ && make build`
Expected: PASS, and `bin/squasharr-worker` exists.

- [ ] **Step 6: Commit**

```bash
git add cmd/squasharr-worker squasharr/worker/exit.go pkg/obs/obsflags pkg/fsops/umask.go pkg/fsops/umask_test.go cmd/clustarr/flags.go cmd/clustarr/root.go cmd/clustarr/umask.go cmd/clustarr/umask_test.go Makefile
git commit -m 'feat(squasharr-worker): the NATS-only pool binary; exits 2, 3 or 10, never 0' -- cmd/squasharr-worker squasharr/worker/exit.go pkg/obs/obsflags pkg/fsops/umask.go pkg/fsops/umask_test.go cmd/clustarr/flags.go cmd/clustarr/root.go cmd/clustarr/umask.go cmd/clustarr/umask_test.go Makefile
```

---

### Task 8: The pool renderer (pure)

**Files:**
- Create: `squasharr/controller/pool/doc.go`, `template.go`, `render.go`, `next.go`.
- Test: `squasharr/controller/pool/template_test.go`, `render_test.go`, `next_test.go`.
- Modify: `squasharr/controller/transcodejob/job.go`. Move out the pod-shape helpers and
  constants listed below; `buildJob` calls the moved copies until Task 10 deletes it.
- Modify: `squasharr/controller/transcodejob/plan.go`: `threadsFor` becomes `pool.Threads`.
- Move tests: `TestBuildJobPodSecurity`, `TestJobPodSecurityMatchesTheDeployments`,
  `TestFlooredDefaultsMatchTheGeneratedCRD`, `TestBuildJobIntelGetsTheRenderGroups`,
  `TestBuildJobPassesUmask` and `TestJobCPULimitEnvIsThePlannedThreads` move from `job_test.go`
  to `pool/template_test.go`, rewritten against `pool.Template`.

**Interfaces:**
- Consumes: `worker.CPULimitEnv`, `worker.ActiveDeadline`, and `worker.WorkerExit*` (Tasks 3 and 7).
- Produces:
  ```go
  type Config struct { Namespace, Image, ImageCUDA, DataClaimName, DataDir, Umask, NATSURL string
  	IntelRenderGroups []int64; ExtraArgs []string
  	NodeLabelNVIDIA, NodeLabelIntel string } // empty: DefaultNodeLabelNVIDIA / DefaultNodeLabelIntel
  func (c Config) NodeLabel(class transcodev1alpha1.Hardware) string // the GPU node label key; "" for cpu
  const DefaultNodeLabelNVIDIA = "nvidia.com/gpu.present", DefaultNodeLabelIntel = "intel.feature.node.kubernetes.io/gpu"
  var GPUResource = map[transcodev1alpha1.Hardware]corev1.ResourceName{"nvidia": "nvidia.com/gpu", "intel": "gpu.intel.com/i915"}
  type Spec struct { Template corev1.PodTemplateSpec; Constraint string } // Constraint: the schedulingConstraints topology key; "" for cpu
  func Want(tp *transcodev1alpha1.TranscodeProfile, class transcodev1alpha1.Hardware, cfg Config) Spec
  type Key struct { Profile string; ProfileUID types.UID; Class transcodev1alpha1.Hardware }
  func Name(k Key) string
  func Threads(p *transcodev1alpha1.TranscodeProfile) int32
  func Template(tp *transcodev1alpha1.TranscodeProfile, class transcodev1alpha1.Hardware, cfg Config) corev1.PodTemplateSpec
  func Hash(s Spec) string // the immutable part: everything but resources, nodeSelector, tolerations
  type Drift int // DriftNone, DriftReshape, DriftRecreate
  func Classify(stored *batchv1.Job, want Spec) Drift
  func Mutable(j *batchv1.Job) bool
  type Desired struct { Parallelism int32; Suspend bool }
  func Render(k Key, tp *transcodev1alpha1.TranscodeProfile, want Spec, d Desired, stored *batchv1.Job, cfg Config) (*batchv1ac.JobApplyConfiguration, error)
  type Action int // ActionNone, ActionApply, ActionDelete
  func Next(stored *batchv1.Job, dispatched int32, drift Drift) (Desired, Action)
  const LabelProfile = "transcode.clustarr.io/profile", LabelHardware = "transcode.clustarr.io/hardware",
  	LabelTemplateHash = "squasharr.clustarr.io/template-hash", AnnotationAppliedTemplate = "squasharr.clustarr.io/applied-template",
  	LabelManagedBy = "app.kubernetes.io/managed-by", ManagedByValue = "squasharr", ContainerName = "transcode",
  	EnvProfileUID = "CLUSTARR_POOL_PROFILE_UID", EnvClass = "CLUSTARR_POOL_CLASS", DefaultDataClaimName = "clustarr-data",
  	UmaskEnv = "UMASK", BackoffLimit = int32(6)
  ```

**Moved out of `job.go` into `pool/template.go`:**
- Constants: `dataVolumeName`, `scratchVolumeName`, `scratchMountPath`, `tmpVolumeName`,
  `tmpMountPath`, `podUID`, `podGID`, `resourceNVIDIAGPU`, `resourceIntelGPU`, `nodeLabelNVIDIA`,
  `nodeLabelIntel`, `DefaultDataClaimName`, `DefaultDataDir`, `UmaskEnv`, `defaultScratch`.
- Functions: `defaultResources`, `wholeCores`, `threadsFromResources`, `resourcesFor`,
  `scratchFor`, `scratchSource`, `addGPU`, `requireNodeLabel`, `podSecurityContext`,
  `containerSecurityContext`.
- `job.go` keeps its own names as aliases, e.g. `var podSecurityContext = pool.PodSecurityContext`,
  until Task 10 deletes the file's per-task Job code. Export exactly what `job.go` needs; keep the
  rest unexported.

- [ ] **Step 1: Write the failing tests**

`squasharr/controller/pool/render_test.go`:

```go
package pool

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/squasharr/worker"
)

var cfg = Config{Namespace: "clustarr-system", Image: "transcoder:t", ImageCUDA: "transcoder-cuda:t",
	DataClaimName: "clustarr-data", DataDir: "/data", NATSURL: "nats://nats:4222", Umask: "002"}

func profile() *transcodev1alpha1.TranscodeProfile {
	tp := &transcodev1alpha1.TranscodeProfile{ObjectMeta: metav1.ObjectMeta{Name: "hevc.uhd", UID: "puid"}}
	tp.Spec.Resources = corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4")}}
	return tp
}

// rendered decodes an apply configuration back into a typed Job for assertions.
func rendered(t *testing.T, k Key, tp *transcodev1alpha1.TranscodeProfile, d Desired, stored *batchv1.Job) batchv1.Job {
	t.Helper()
	ac, err := Render(k, tp, Want(tp, k.Class, cfg), d, stored, cfg)
	require.NoError(t, err)
	b, err := json.Marshal(ac)
	require.NoError(t, err)
	var j batchv1.Job
	require.NoError(t, json.Unmarshal(b, &j))
	return j
}

func TestRenderIsACompletePoolDeclaration(t *testing.T) {
	k := Key{Profile: "hevc.uhd", ProfileUID: "puid", Class: transcodev1alpha1.HardwareNVIDIA}
	j := rendered(t, k, profile(), Desired{Parallelism: 3}, nil)

	assert.Equal(t, "batch/v1", j.APIVersion)
	assert.LessOrEqual(t, len(j.Name), 63)
	assert.True(t, strings.HasPrefix(j.Name, "squasharr-pool-"))
	assert.Equal(t, "clustarr-system", j.Namespace)
	require.Len(t, j.OwnerReferences, 1)
	assert.Equal(t, "TranscodeProfile", j.OwnerReferences[0].Kind)
	assert.True(t, *j.OwnerReferences[0].Controller)

	assert.Equal(t, int32(3), *j.Spec.Parallelism)
	assert.Nil(t, j.Spec.Completions, "work-queue pattern: completions unset")
	assert.Equal(t, batchv1.NonIndexedCompletion, *j.Spec.CompletionMode)
	assert.Equal(t, batchv1.Failed, *j.Spec.PodReplacementPolicy)
	assert.Equal(t, BackoffLimit, *j.Spec.BackoffLimit)
	assert.Nil(t, j.Spec.ActiveDeadlineSeconds)
	assert.Nil(t, j.Spec.TTLSecondsAfterFinished)
	require.NotNil(t, j.Spec.Scheduling)
	assert.Equal(t, int32(3), *j.Spec.Scheduling.SchedulingPolicy.Gang.MinCount)
	require.NotNil(t, j.Spec.Scheduling.SchedulingConstraints, "a GPU pool is created with its GPU label as a topology constraint (spec §18.5)")
	assert.Equal(t, "nvidia.com/gpu.present", j.Spec.Scheduling.SchedulingConstraints.Topology[0].Key)
	assert.Nil(t, j.Spec.Scheduling.DisruptionMode)
	assert.Empty(t, j.Spec.Scheduling.ResourceClaims)

	rules := j.Spec.PodFailurePolicy.Rules
	require.Len(t, rules, 3)
	assert.Equal(t, batchv1.PodFailurePolicyActionIgnore, rules[0].Action)
	assert.Equal(t, []int32{worker.WorkerExitDrained}, rules[1].OnExitCodes.Values)
	assert.Equal(t, batchv1.PodFailurePolicyActionFailJob, rules[2].Action)
	assert.Equal(t, []int32{worker.WorkerExitMisconfigured}, rules[2].OnExitCodes.Values)

	pod := j.Spec.Template.Spec
	assert.False(t, *pod.AutomountServiceAccountToken, "the worker holds no Kubernetes credentials")
	assert.Empty(t, pod.ServiceAccountName)
	assert.Equal(t, "transcoder-cuda:t", pod.Containers[0].Image)
	assert.Equal(t, "nvidia", *pod.RuntimeClassName)
	envs := map[string]string{}
	for _, e := range pod.Containers[0].Env {
		envs[e.Name] = e.Value
	}
	assert.Equal(t, "puid", envs[EnvProfileUID])
	assert.Equal(t, "nvidia", envs[EnvClass])
	assert.Equal(t, "nats://nats:4222", envs["NATS_URL"])
	assert.NotEmpty(t, j.Labels[LabelTemplateHash])
	assert.NotEmpty(t, j.Annotations[AnnotationAppliedTemplate])
}

func TestRenderNeverZeroesParallelism(t *testing.T) {
	j := rendered(t, Key{Profile: "p", ProfileUID: "u", Class: "cpu"}, profile(), Desired{Suspend: true}, nil)
	assert.Equal(t, int32(1), *j.Spec.Parallelism)
	assert.Equal(t, int32(1), *j.Spec.Scheduling.SchedulingPolicy.Gang.MinCount)
	assert.True(t, *j.Spec.Suspend)
	assert.Nil(t, j.Spec.Scheduling.SchedulingConstraints, "a CPU pool has no constraint")
}

func TestGPUPoolsCarryTheirNodeLabelAsASchedulingConstraint(t *testing.T) {
	intel := rendered(t, Key{Profile: "p", ProfileUID: "u", Class: "intel"}, profile(), Desired{Parallelism: 1}, nil)
	assert.Equal(t, "intel.feature.node.kubernetes.io/gpu", intel.Spec.Scheduling.SchedulingConstraints.Topology[0].Key)
	aff := intel.Spec.Template.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
	assert.Equal(t, "intel.feature.node.kubernetes.io/gpu", aff.NodeSelectorTerms[0].MatchExpressions[0].Key,
		"pod affinity keeps the same label for clusters without WorkloadWithJob")

	k := Key{Profile: "p", ProfileUID: "u", Class: "nvidia"}
	stored := rendered(t, k, profile(), Desired{Parallelism: 1}, nil)
	relabelled := cfg
	relabelled.NodeLabelNVIDIA = "example.com/gpu"
	assert.Equal(t, DriftRecreate, Classify(&stored, Want(profile(), k.Class, relabelled)),
		"the constraint is immutable: a new label key recreates the pool")

	// While the pool exists, the constraint it was created with is re-sent, never the new one.
	ac, err := Render(k, profile(), Want(profile(), k.Class, relabelled), Desired{Parallelism: 1, Suspend: true}, &stored, relabelled)
	require.NoError(t, err)
	assert.Equal(t, "nvidia.com/gpu.present", *ac.Spec.Scheduling.SchedulingConstraints.Topology[0].Key)
}

// A running pool is rendered with the template it was last applied with, so
// no apply ever asks the apiserver for a template change it would reject.
func TestRenderKeepsTheAppliedTemplateWhileRunning(t *testing.T) {
	k := Key{Profile: "p", ProfileUID: "u", Class: "cpu"}
	first := rendered(t, k, profile(), Desired{Parallelism: 1}, nil)
	stored := first.DeepCopy()
	stored.Spec.Suspend = ptr.To(false)
	stored.Status.StartTime = &metav1.Time{}

	edited := profile()
	edited.Spec.Resources.Limits[corev1.ResourceCPU] = resource.MustParse("8")
	next := rendered(t, k, edited, Desired{Parallelism: 2}, stored)
	assert.Equal(t, first.Spec.Template.Spec.Containers[0].Resources, next.Spec.Template.Spec.Containers[0].Resources)
	assert.Equal(t, int32(2), *next.Spec.Parallelism)
}
```

`squasharr/controller/pool/next_test.go`:

```go
func TestClassifyIgnoresApiserverDefaulting(t *testing.T) { // Review Focus 2
	k := Key{Profile: "p", ProfileUID: "u", Class: "cpu"}
	tp := profile()
	stored := rendered(t, k, tp, Desired{Parallelism: 1, Suspend: true}, nil)
	// What the apiserver does on create: requests copied from limits, fields defaulted.
	c := &stored.Spec.Template.Spec.Containers[0]
	c.Resources.Requests = c.Resources.Limits.DeepCopy()
	c.TerminationMessagePath, c.ImagePullPolicy = "/dev/termination-log", corev1.PullIfNotPresent
	assert.Equal(t, DriftNone, Classify(&stored, Want(tp, k.Class, cfg)))
}

func TestClassify(t *testing.T) {
	k := Key{Profile: "p", ProfileUID: "u", Class: "cpu"}
	stored := rendered(t, k, profile(), Desired{Parallelism: 1}, nil)
	reshaped := profile()
	reshaped.Spec.Resources.Limits[corev1.ResourceCPU] = resource.MustParse("8")
	assert.Equal(t, DriftReshape, Classify(&stored, Want(reshaped, k.Class, cfg)))
	other := cfg
	other.Image = "transcoder:new"
	assert.Equal(t, DriftRecreate, Classify(&stored, Want(profile(), k.Class, other)))
	delete(stored.Annotations, AnnotationAppliedTemplate)
	assert.Equal(t, DriftRecreate, Classify(&stored, Want(profile(), k.Class, cfg)), "an unknown applied template is recreated")
}

func job(suspend bool, par int32, started bool, active int32, failed bool) *batchv1.Job {
	j := &batchv1.Job{Spec: batchv1.JobSpec{Suspend: ptr.To(suspend), Parallelism: ptr.To(par)}}
	if started {
		j.Status.StartTime = &metav1.Time{}
	}
	j.Status.Active = active
	if failed {
		j.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue}}
	}
	return j
}

func TestNext(t *testing.T) {
	for _, tc := range []struct {
		name       string
		stored     *batchv1.Job
		dispatched int32
		drift      Drift
		want       Desired
		act        Action
	}{
		{"no pool, no work", nil, 0, DriftNone, Desired{}, ActionNone},
		{"no pool, work", nil, 2, DriftNone, Desired{Parallelism: 2}, ActionApply},
		{"idle and suspended", job(true, 2, false, 0, false), 0, DriftNone, Desired{Parallelism: 2, Suspend: true}, ActionNone},
		{"work drained: suspend", job(false, 2, true, 2, false), 0, DriftNone, Desired{Parallelism: 2, Suspend: true}, ActionApply},
		{"work arrives: resume", job(true, 2, false, 0, false), 3, DriftNone, Desired{Parallelism: 3}, ActionApply},
		{"more work: scale up", job(false, 2, true, 2, false), 4, DriftNone, Desired{Parallelism: 4}, ActionApply},
		{"less work: never shrink below zero", job(false, 3, true, 3, false), 1, DriftNone, Desired{Parallelism: 3}, ActionNone},
		{"failed pool is recreated", job(false, 2, true, 0, true), 2, DriftNone, Desired{}, ActionDelete},
		{"drift, busy: hold", job(false, 2, true, 2, false), 2, DriftReshape, Desired{Parallelism: 2}, ActionNone},
		{"drift, drained: suspend", job(false, 2, true, 2, false), 0, DriftReshape, Desired{Parallelism: 2, Suspend: true}, ActionApply},
		{"drift, suspending: wait for startTime", job(true, 2, true, 1, false), 0, DriftReshape, Desired{Parallelism: 2, Suspend: true}, ActionNone},
		{"reshape when mutable", job(true, 2, false, 0, false), 0, DriftReshape, Desired{Parallelism: 1, Suspend: true}, ActionApply},
		{"recreate when mutable", job(true, 2, false, 0, false), 0, DriftRecreate, Desired{}, ActionDelete},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, a := Next(tc.stored, tc.dispatched, tc.drift)
			assert.Equal(t, tc.act, a)
			if a != ActionDelete {
				assert.Equal(t, tc.want, d)
			}
		})
	}
}
```

`template_test.go` holds the six moved tests. Each builds its pod with `Template(tp, class, cfg)`
instead of `buildJob(tj, profile, hardware, cfg)` and keeps its assertions, plus:

```go
func TestTemplatePodsRunWithoutAServiceAccountToken(t *testing.T) {
	for _, class := range []transcodev1alpha1.Hardware{"cpu", "nvidia", "intel"} {
		pod := Template(profile(), class, cfg).Spec
		assert.False(t, *pod.AutomountServiceAccountToken, class)
		assert.Empty(t, pod.ServiceAccountName, class)
		assert.Equal(t, []string{"--data-dir", "/data"}, pod.Containers[0].Args[:2])
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./squasharr/controller/pool/`
Expected: FAIL to compile, "undefined: Render".

- [ ] **Step 3: Write `template.go`**

Move the helpers listed above. Then write `Template`, reusing `buildJob`'s pod shape from
`job.go:250-392`, with these differences:

```go
// Template is the pod template a (profile, class) pool asks for. The image,
// runtimeClassName, securityContext, volumes, env and args are immutable for
// the pool's life; resources, nodeSelector and tolerations can be changed
// while it is suspended (spec §3, §7).
func Template(tp *transcodev1alpha1.TranscodeProfile, class transcodev1alpha1.Hardware, cfg Config) corev1.PodTemplateSpec {
	dataDir := cmp.Or(cfg.DataDir, DefaultDataDir)
	image := cfg.Image
	if class == transcodev1alpha1.HardwareNVIDIA && cfg.ImageCUDA != "" {
		image = cfg.ImageCUDA
	}
	res := resourcesFor(tp)
	threads, fromLimit := threadsFromResources(res)
	env := []corev1.EnvVar{
		{Name: "POD_NAME", ValueFrom: fieldRef("metadata.name")},
		{Name: "POD_NAMESPACE", ValueFrom: fieldRef("metadata.namespace")},
		{Name: "NODE_NAME", ValueFrom: fieldRef("spec.nodeName")},
		{Name: EnvProfileUID, Value: string(tp.UID)},
		{Name: EnvClass, Value: string(class)},
		{Name: "NATS_URL", Value: cfg.NATSURL},
		cpuLimitEnv(threads, fromLimit), // the worker.CPULimitEnv entry, built exactly as job.go:289-297 builds it
	}
	if cfg.Umask != "" {
		env = append(env, corev1.EnvVar{Name: UmaskEnv, Value: cfg.Umask})
	}
	pod := corev1.PodSpec{
		RestartPolicy:                corev1.RestartPolicyNever,
		AutomountServiceAccountToken: ptr.To(false),
		SecurityContext:              podSecurityContext(),
		Containers: []corev1.Container{{
			Name: ContainerName, Image: image,
			Args:            append([]string{"--data-dir", dataDir}, cfg.ExtraArgs...),
			Env:             env,
			Resources:       res,
			SecurityContext: containerSecurityContext(),
			VolumeMounts: []corev1.VolumeMount{
				{Name: dataVolumeName, MountPath: dataDir},
				{Name: scratchVolumeName, MountPath: scratchMountPath},
				{Name: tmpVolumeName, MountPath: tmpMountPath},
			},
		}},
		Volumes: []corev1.Volume{
			{Name: dataVolumeName, VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: cmp.Or(cfg.DataClaimName, DefaultDataClaimName)}}},
			{Name: scratchVolumeName, VolumeSource: corev1.VolumeSource{EmptyDir: scratchSource(tp)}},
			{Name: tmpVolumeName, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		},
	}
	applyHardware(&pod, tp, class, cfg) // job.go:338-366: GPU resource (GPUResource[class]), NVIDIA_DRIVER_CAPABILITIES,
	// runtimeClassName, required node affinity on cfg.NodeLabel(class) (not the old nodeLabel* constants),
	// intel supplementalGroups, GPU nodeSelector and tolerations
	return corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{
			"app.kubernetes.io/name": "clustarr", "app.kubernetes.io/component": "squasharr-worker",
			LabelManagedBy: ManagedByValue, LabelHardware: string(class),
		}},
		Spec: pod,
	}
}

// Threads is the x265 pool size a profile's pods get: plan.go plans with it.
func Threads(p *transcodev1alpha1.TranscodeProfile) int32 {
	n, _ := threadsFromResources(resourcesFor(p))
	return n
}
```

- [ ] **Step 4: Write `render.go`**

```go
const AnnotationAppliedTemplate = "squasharr.clustarr.io/applied-template"

// Name is a pool's Job name: readable where it fits, hashed where it does not.
func Name(k Key) string {
	return k8s.LabelSafeName("squasharr-pool-"+k.Profile+"-"+string(k.Class), string(k.ProfileUID), string(k.Class))
}

// Mutable reports whether the apiserver will accept a template change: the
// Job is suspended and the Job controller has cleared startTime (spec §3).
func Mutable(j *batchv1.Job) bool {
	return ptr.Deref(j.Spec.Suspend, false) && j.Status.StartTime == nil && j.Status.Active == 0
}

// NodeLabel is the GPU node label key a class's pools are held to; "" for cpu.
func (c Config) NodeLabel(class transcodev1alpha1.Hardware) string {
	switch class {
	case transcodev1alpha1.HardwareNVIDIA:
		return cmp.Or(c.NodeLabelNVIDIA, DefaultNodeLabelNVIDIA)
	case transcodev1alpha1.HardwareIntel:
		return cmp.Or(c.NodeLabelIntel, DefaultNodeLabelIntel)
	}
	return ""
}

// Spec is everything about a pool that the profile and the flags decide: the
// pod template and, for a GPU class, the topology key its Job's
// .spec.scheduling.schedulingConstraints carries (spec §18.5).
type Spec struct {
	Template   corev1.PodTemplateSpec `json:"template"`
	Constraint string                 `json:"constraint,omitempty"`
}

// Want is the Spec a (profile, class) pool asks for.
func Want(tp *transcodev1alpha1.TranscodeProfile, class transcodev1alpha1.Hardware, cfg Config) Spec {
	return Spec{Template: Template(tp, class, cfg), Constraint: cfg.NodeLabel(class)}
}

// Hash identifies a Spec's immutable part. The constraint is in it: the
// apiserver never lets a Job's schedulingConstraints change.
func Hash(s Spec) string {
	c := s.Template.DeepCopy()
	c.Spec.NodeSelector, c.Spec.Tolerations = nil, nil
	for i := range c.Spec.Containers {
		c.Spec.Containers[i].Resources = corev1.ResourceRequirements{}
	}
	b, _ := json.Marshal(struct {
		T *corev1.PodTemplateSpec
		C string
	}{c, s.Constraint})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

// applied is the Spec squasharr last applied to j, from its annotation.
// Judging drift against it, not j.Spec.Template, ignores apiserver defaulting.
func applied(j *batchv1.Job) (Spec, bool) {
	var s Spec
	raw, ok := j.Annotations[AnnotationAppliedTemplate]
	if !ok || json.Unmarshal([]byte(raw), &s) != nil {
		return s, false
	}
	return s, true
}

// Classify compares what the profile and flags now ask for with what was applied.
func Classify(stored *batchv1.Job, want Spec) Drift {
	prev, ok := applied(stored)
	if !ok || Hash(prev) != Hash(want) {
		return DriftRecreate
	}
	if !equality.Semantic.DeepEqual(mutablePart(prev.Template), mutablePart(want.Template)) {
		return DriftReshape
	}
	return DriftNone
}

type mutable struct {
	NodeSelector map[string]string
	Tolerations  []corev1.Toleration
	Resources    []corev1.ResourceRequirements
}

func mutablePart(t corev1.PodTemplateSpec) mutable {
	m := mutable{NodeSelector: t.Spec.NodeSelector, Tolerations: t.Spec.Tolerations}
	for _, c := range t.Spec.Containers {
		m.Resources = append(m.Resources, c.Resources)
	}
	return m
}

// Render is the one complete declaration squasharr-pool makes for a pool.
// An existing Job is always re-sent the constraint it was created with (it
// is immutable even while suspended). While the Job is not Mutable, its
// applied template is re-sent unchanged too: a template field this manager
// stops sending would be released, and a released template field on a
// running Job is a rejected write.
func Render(k Key, tp *transcodev1alpha1.TranscodeProfile, want Spec, d Desired,
	stored *batchv1.Job, cfg Config,
) (*batchv1ac.JobApplyConfiguration, error) {
	d.Parallelism = max(d.Parallelism, 1)
	spec := want
	if stored != nil {
		prev, ok := applied(stored)
		if !ok {
			return nil, fmt.Errorf("pool %s: no applied spec to keep", stored.Name)
		}
		spec.Constraint = prev.Constraint
		if !Mutable(stored) {
			spec.Template = prev.Template
		}
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		return nil, err
	}
	sched := &batchv1.JobSchedulingConfiguration{
		SchedulingPolicy: &schedulingv1alpha3.WorkloadPodGroupSchedulingPolicy{
			Gang: &schedulingv1alpha3.WorkloadPodGroupGangSchedulingPolicy{MinCount: ptr.To(d.Parallelism)},
		},
	}
	if spec.Constraint != "" {
		sched.SchedulingConstraints = &schedulingv1alpha3.WorkloadPodGroupSchedulingConstraints{
			Topology: []schedulingv1alpha3.TopologyConstraint{{Key: spec.Constraint}},
		}
	}
	job := &batchv1.Job{
		TypeMeta: metav1.TypeMeta{APIVersion: "batch/v1", Kind: "Job"},
		ObjectMeta: metav1.ObjectMeta{
			Name: Name(k), Namespace: cfg.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/name": "clustarr", "app.kubernetes.io/component": "squasharr-worker",
				LabelManagedBy: ManagedByValue, LabelHardware: string(k.Class), LabelProfile: k.Profile,
				LabelTemplateHash: Hash(spec),
			},
			Annotations: map[string]string{AnnotationAppliedTemplate: string(raw)},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: transcodev1alpha1.GroupVersion.String(), Kind: "TranscodeProfile",
				Name: tp.Name, UID: tp.UID, Controller: ptr.To(true), BlockOwnerDeletion: ptr.To(true),
			}},
		},
		Spec: batchv1.JobSpec{
			Parallelism:          ptr.To(d.Parallelism),
			Suspend:              ptr.To(d.Suspend),
			CompletionMode:       ptr.To(batchv1.NonIndexedCompletion),
			BackoffLimit:         ptr.To(BackoffLimit),
			PodReplacementPolicy: ptr.To(batchv1.Failed),
			PodFailurePolicy:     podFailurePolicy(),
			Scheduling:           sched,
			Template:             spec.Template,
		},
	}
	return toApply(job)
}

// toApply turns a typed Job into its apply configuration through JSON; the
// two share field names by construction.
func toApply(job *batchv1.Job) (*batchv1ac.JobApplyConfiguration, error) {
	b, err := json.Marshal(job)
	if err != nil {
		return nil, err
	}
	ac := batchv1ac.Job(job.Name, job.Namespace)
	if err := json.Unmarshal(b, ac); err != nil {
		return nil, err
	}
	ac.Status = nil
	return ac, nil
}

func podFailurePolicy() *batchv1.PodFailurePolicy {
	return &batchv1.PodFailurePolicy{Rules: []batchv1.PodFailurePolicyRule{
		{Action: batchv1.PodFailurePolicyActionIgnore, OnPodConditions: []batchv1.PodFailurePolicyOnPodConditionsPattern{
			{Type: corev1.DisruptionTarget, Status: corev1.ConditionTrue}}},
		{Action: batchv1.PodFailurePolicyActionIgnore, OnExitCodes: &batchv1.PodFailurePolicyOnExitCodesRequirement{
			ContainerName: ptr.To(ContainerName), Operator: batchv1.PodFailurePolicyOnExitCodesOpIn,
			Values: []int32{worker.WorkerExitDrained}}},
		{Action: batchv1.PodFailurePolicyActionFailJob, OnExitCodes: &batchv1.PodFailurePolicyOnExitCodesRequirement{
			ContainerName: ptr.To(ContainerName), Operator: batchv1.PodFailurePolicyOnExitCodesOpIn,
			Values: []int32{worker.WorkerExitMisconfigured}}},
	}}
}
```

- [ ] **Step 5: Write `next.go`**

```go
// Next decides one admission pass for one pool. dispatched counts the pool's
// Queued and Running TranscodeJobs; admission never dispatches past the
// class's slots or the profile's maxConcurrent, so it is already the size.
func Next(stored *batchv1.Job, dispatched int32, drift Drift) (Desired, Action) {
	if stored == nil {
		if dispatched == 0 {
			return Desired{}, ActionNone
		}
		return Desired{Parallelism: dispatched}, ActionApply
	}
	if failed(stored) {
		return Desired{}, ActionDelete
	}
	par := ptr.Deref(stored.Spec.Parallelism, 1)
	suspended := ptr.Deref(stored.Spec.Suspend, false)
	if drift != DriftNone {
		switch {
		case !suspended && dispatched > 0:
			return Desired{Parallelism: par}, ActionNone // draining: admission holds new work
		case !suspended:
			return Desired{Parallelism: par, Suspend: true}, ActionApply
		case !Mutable(stored):
			return Desired{Parallelism: par, Suspend: true}, ActionNone
		case drift == DriftRecreate:
			return Desired{}, ActionDelete
		default:
			return Desired{Parallelism: max(dispatched, 1), Suspend: dispatched == 0}, ActionApply
		}
	}
	switch {
	case dispatched == 0 && suspended:
		return Desired{Parallelism: par, Suspend: true}, ActionNone
	case dispatched == 0:
		return Desired{Parallelism: par, Suspend: true}, ActionApply
	case suspended:
		return Desired{Parallelism: dispatched}, ActionApply
	case dispatched > par:
		return Desired{Parallelism: dispatched}, ActionApply
	default:
		return Desired{Parallelism: par}, ActionNone
	}
}

func failed(j *batchv1.Job) bool {
	for _, c := range j.Status.Conditions {
		if c.Type == batchv1.JobFailed && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}
```

- [ ] **Step 6: Run the tests**

Run: `go test ./squasharr/controller/...`
Expected: PASS: the new `pool` tests, and `transcodejob`'s remaining `job_test.go` through the
moved helpers.

- [ ] **Step 7: Commit**

```bash
git add squasharr/controller/pool squasharr/controller/transcodejob/job.go squasharr/controller/transcodejob/job_test.go squasharr/controller/transcodejob/plan.go
git commit -m 'feat(squasharr): pool renderer: one complete declaration, applied-template drift, gang minCount = parallelism' -- squasharr/controller/pool squasharr/controller/transcodejob/job.go squasharr/controller/transcodejob/job_test.go squasharr/controller/transcodejob/plan.go
```

---

### Task 9: Pools against a real apiserver, gates on and off

**Files:**
- Modify: `pkg/k8s/fieldmanager.go`: add `ManagerSquasharrPool FieldManager = "squasharr-pool"`
  to the const block and to `FieldManagers()`.
- Modify: `pkg/k8s/fieldmanager_test.go`: add `"squasharr-pool"` to the expected list.
- Create: `squasharr/controller/pool/pool_envtest_test.go`.
- Create: `squasharr/controller/pool/errors.go`.

**Interfaces:**
- Consumes: `Render`, `Template`, `Classify`, `Next` (Task 8).
- Produces:
  ```go
  const k8s.ManagerSquasharrPool
  func IsSchedulingImmutable(err error) bool // the apiserver refused to add .spec.scheduling to an existing Job
  ```

- [ ] **Step 1: Write the failing envtests**

`squasharr/controller/pool/pool_envtest_test.go` (package `pool`):

```go
func startEnv(t *testing.T, gates bool) client.Client {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is not set")
	}
	env := &envtest.Environment{CRDDirectoryPaths: []string{"../../../config/crd/bases"}, ErrorIfCRDPathMissing: true}
	if gates {
		env.ControlPlane.GetAPIServer().Configure().
			Append("feature-gates", "WorkloadWithJob=true,GenericWorkload=true").
			Append("runtime-config", "scheduling.k8s.io/v1alpha3=true")
	}
	restCfg, err := env.Start() // not `cfg`: that is the package's pool.Config
	require.NoError(t, err)
	t.Cleanup(func() { _ = env.Stop() })
	c, err := client.New(restCfg, client.Options{Scheme: k8s.MustNewScheme()})
	require.NoError(t, err)
	require.NoError(t, c.Create(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: cfg.Namespace}}))
	return c
}

// newProfile creates a real TranscodeProfile so the owner reference resolves.
func newProfile(t *testing.T, c client.Client) *transcodev1alpha1.TranscodeProfile {
	tp := &transcodev1alpha1.TranscodeProfile{ObjectMeta: metav1.ObjectMeta{Name: "hevc.uhd"}}
	tp.Spec.Resources = corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4")}}
	require.NoError(t, c.Create(context.Background(), tp))
	return tp
}

func apply(t *testing.T, c client.Client, tp *transcodev1alpha1.TranscodeProfile, d Desired, stored *batchv1.Job) error {
	t.Helper()
	k := Key{Profile: tp.Name, ProfileUID: tp.UID, Class: "cpu"}
	ac, err := Render(k, tp, Want(tp, "cpu", cfg), d, stored, cfg)
	require.NoError(t, err)
	_, err = k8s.Apply(context.Background(), c, k8s.ManagerSquasharrPool, ac)
	return err
}

func get(t *testing.T, c client.Client, tp *transcodev1alpha1.TranscodeProfile) *batchv1.Job {
	t.Helper()
	var j batchv1.Job
	require.NoError(t, c.Get(context.Background(),
		types.NamespacedName{Namespace: cfg.Namespace, Name: Name(Key{Profile: tp.Name, ProfileUID: tp.UID, Class: "cpu"})}, &j))
	return &j
}

// As the Job controller would: running pods, then a suspend that clears startTime.
func setRunning(t *testing.T, c client.Client, j *batchv1.Job, active int32) {
	j.Status.StartTime, j.Status.Active = &metav1.Time{Time: time.Now()}, active
	require.NoError(t, c.Status().Update(context.Background(), j)) //nolint:forbidigo // simulating the Job controller
}
func setStopped(t *testing.T, c client.Client, j *batchv1.Job) {
	j.Status.StartTime, j.Status.Active = nil, 0
	require.NoError(t, c.Status().Update(context.Background(), j)) //nolint:forbidigo // simulating the Job controller
}

func TestPoolLifecycleWithGangScheduling(t *testing.T) {
	c := startEnv(t, true)
	tp := newProfile(t, c)

	require.NoError(t, apply(t, c, tp, Desired{Parallelism: 2}, nil))
	j := get(t, c, tp)
	assert.Equal(t, int32(2), *j.Spec.Scheduling.SchedulingPolicy.Gang.MinCount)
	setRunning(t, c, j, 2)

	require.NoError(t, apply(t, c, tp, Desired{Parallelism: 4}, get(t, c, tp)), "scale up while running")
	assert.Equal(t, int32(4), *get(t, c, tp).Spec.Scheduling.SchedulingPolicy.Gang.MinCount)

	require.NoError(t, apply(t, c, tp, Desired{Parallelism: 4, Suspend: true}, get(t, c, tp)), "suspend to zero")
	setStopped(t, c, get(t, c, tp))

	tp.Spec.Resources.Limits[corev1.ResourceCPU] = resource.MustParse("8")
	stored := get(t, c, tp)
	require.Equal(t, DriftReshape, Classify(stored, Want(tp, "cpu", cfg)))
	require.NoError(t, apply(t, c, tp, Desired{Parallelism: 1}, stored), "reshape and resume in one apply")
	got := get(t, c, tp)
	assert.Equal(t, "8", got.Spec.Template.Spec.Containers[0].Resources.Limits.Cpu().String())
	assert.False(t, *got.Spec.Suspend)
	assert.Equal(t, DriftNone, Classify(got, Want(tp, "cpu", cfg)))

	for _, mf := range got.ManagedFields {
		if mf.Manager != string(k8s.ManagerSquasharrPool) {
			continue
		}
		raw := string(mf.FieldsV1.Raw)
		assert.Contains(t, raw, `"f:minCount"`)
		for _, other := range []string{`"f:schedulingConstraints"`, `"f:disruptionMode"`, `"f:resourceClaims"`} {
			assert.NotContains(t, raw, other)
		}
	}
}

// Review Focus 5.
func TestProfileEditWhileRunningNeverRejectsAnApply(t *testing.T) {
	c := startEnv(t, true)
	tp := newProfile(t, c)
	require.NoError(t, apply(t, c, tp, Desired{Parallelism: 1}, nil))
	setRunning(t, c, get(t, c, tp), 1)

	tp.Spec.Resources.Limits[corev1.ResourceCPU] = resource.MustParse("8")
	stored := get(t, c, tp)
	assert.Equal(t, DriftReshape, Classify(stored, Want(tp, "cpu", cfg)))
	d, act := Next(stored, 1, DriftReshape)
	assert.Equal(t, ActionNone, act, "a busy pool holds while draining")
	require.NoError(t, apply(t, c, tp, Desired{Parallelism: d.Parallelism}, stored),
		"rendering a running pool re-sends its applied template, so the apply is accepted")
	assert.Equal(t, "4", get(t, c, tp).Spec.Template.Spec.Containers[0].Resources.Limits.Cpu().String())
}

func TestPoolWithoutTheGate(t *testing.T) {
	c := startEnv(t, false)
	tp := newProfile(t, c)
	require.NoError(t, apply(t, c, tp, Desired{Parallelism: 2}, nil))
	assert.Nil(t, get(t, c, tp).Spec.Scheduling, "the apiserver drops minCount without WorkloadWithJob")
	require.NoError(t, apply(t, c, tp, Desired{Parallelism: 3}, get(t, c, tp)))
	assert.Equal(t, int32(3), *get(t, c, tp).Spec.Parallelism)
}

func TestGPUPoolConstraintAgainstTheApiserver(t *testing.T) {
	c := startEnv(t, true)
	tp := newProfile(t, c)
	k := Key{Profile: tp.Name, ProfileUID: tp.UID, Class: "nvidia"}
	get := func() *batchv1.Job {
		var j batchv1.Job
		require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: cfg.Namespace, Name: Name(k)}, &j))
		return &j
	}
	put := func(d Desired, stored *batchv1.Job) error {
		ac, err := Render(k, tp, Want(tp, k.Class, cfg), d, stored, cfg)
		require.NoError(t, err)
		_, err = k8s.Apply(context.Background(), c, k8s.ManagerSquasharrPool, ac)
		return err
	}
	require.NoError(t, put(Desired{Parallelism: 1, Suspend: true}, nil))
	assert.Equal(t, "nvidia.com/gpu.present", get().Spec.Scheduling.SchedulingConstraints.Topology[0].Key)
	require.NoError(t, put(Desired{Parallelism: 2}, get()), "every later apply re-sends the constraint it was created with")
	relabelled := cfg
	relabelled.NodeLabelNVIDIA = "example.com/gpu"
	assert.Equal(t, DriftRecreate, Classify(get(), Want(tp, k.Class, relabelled)), "never applied in place")
}

func TestAGateEnabledLaterReadsAsRecreate(t *testing.T) {
	c := startEnv(t, true)
	tp := newProfile(t, c)
	// A pool created before the gate existed: no .spec.scheduling.
	ac, err := Render(Key{Profile: tp.Name, ProfileUID: tp.UID, Class: "cpu"}, tp, Want(tp, "cpu", cfg), Desired{Parallelism: 1}, nil, cfg)
	require.NoError(t, err)
	ac.Spec.Scheduling = nil
	_, err = k8s.Apply(context.Background(), c, k8s.ManagerSquasharrPool, ac)
	require.NoError(t, err)

	err = apply(t, c, tp, Desired{Parallelism: 1}, get(t, c, tp))
	require.Error(t, err)
	assert.True(t, IsSchedulingImmutable(err), "%v", err)
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./squasharr/controller/pool/ -run 'TestPool|TestProfileEdit|TestAGate'`
Expected: FAIL to compile, "undefined: k8s.ManagerSquasharrPool".

- [ ] **Step 3: Add the manager and `IsSchedulingImmutable`**

In `pkg/k8s/fieldmanager.go`, after `ManagerSquasharrWorker`:

```go
	// ManagerSquasharrPool is squasharr's transcode pool Jobs: the sole
	// writer of their spec, including spec.scheduling.schedulingPolicy.gang.minCount.
	ManagerSquasharrPool FieldManager = "squasharr-pool"
```

Add it to `FieldManagers()` and to `fieldmanager_test.go`'s expected list.

`squasharr/controller/pool/errors.go`:

```go
// IsSchedulingImmutable reports the apiserver refusing to add
// .spec.scheduling to a Job created before WorkloadWithJob was enabled
// ("field cannot be set once created", spec §7): that pool is recreated.
func IsSchedulingImmutable(err error) bool {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	return apierrors.IsInvalid(err) && strings.Contains(msg, "spec.scheduling") &&
		(strings.Contains(msg, "cannot be set once created") || strings.Contains(msg, "field is immutable"))
}
```

- [ ] **Step 4: Run the envtests**

Run: `go test ./squasharr/controller/pool/ ./pkg/k8s/ -v -run 'TestPool|TestProfileEdit|TestAGate|TestFieldManagers'`
Expected: PASS in seconds, not milliseconds; a millisecond run means it skipped.

- [ ] **Step 5: Commit**

```bash
git add pkg/k8s/fieldmanager.go pkg/k8s/fieldmanager_test.go squasharr/controller/pool/errors.go squasharr/controller/pool/pool_envtest_test.go
git commit -m 'test(squasharr): pools against the 1.37 apiserver with and without WorkloadWithJob' -- pkg/k8s/fieldmanager.go pkg/k8s/fieldmanager_test.go squasharr/controller/pool/errors.go squasharr/controller/pool/pool_envtest_test.go
```

---

### Task 10: squasharr dispatches over NATS, consumes the results stream, and decides the next step

This is the switch-over. After it:
- the controller no longer creates per-task Jobs;
- it publishes tasks and consumes `squasharr-transcode-results`;
- both its write paths go through one compare-and-swap function;
- the next-step table (spec §18.3) decides requeue, block or fallback;
- the in-process worker role is gone.

**Files:**
- Modify: `squasharr/controller/transcodejob/controller.go`: the new `Reconciler` fields; the
  `Reconcile`, `advance` and `admit` rewiring; `afterWrite`.
- Create: in `squasharr/controller/transcodejob/`:
  - `write.go`: `writeStatus`, `patchCAS`
  - `dispatch.go`: `dispatch`, `classFor`, `poolKeyFor`
  - `results.go`: the results consumer runnable and `handleEvent`
  - `decide.go`: the pure `Decide` and `applyDecision`
  - `decide_test.go`
- Modify: `squasharr/controller/transcodejob/events.go`: the queued edge becomes "attempts
  increased".
- Modify: `squasharr/controller/transcodejob/metrics.go`: `setActive` over TranscodeJob slots.
- Delete: in `job.go`, `buildJob`, `jobName`, `jobFinished`, `jobSuspended`, the `JobConfig`
  struct and the leftover aliases; delete `job_test.go`'s per-task Job tests.
- Modify: `squasharr/status/status.go`, `squasharr/status/split_test.go` and
  `squasharr/status/status_envtest_test.go`.
- Modify: `pkg/k8s/fieldmanager.go` and `pkg/k8s/fieldmanager_test.go`: delete
  `ManagerSquasharrWorker`.
- Modify: `squasharr/controller/transcodeprofile/controller.go`: replace stale SourceChanged jobs;
  its RBAC marker gains `delete` on `transcodejobs`.
- Modify: `catalogarr/history/target.go` and its test: resolve schema `transcode.Task.v1`.
  `pkg/k8s/deadletter.go:58`: list `transcode.Task`.
- Modify: `squasharr/run.go`:
  - delete `RoleWorker`, `runWorker`, `runWorkerJob`, `exitForGet`, `ExitError`, `JobName` and
    the worker branch of `Validate`;
  - `jobConfig` becomes `poolConfig`;
  - register the results consumer with `mgr.Add`;
  - set `Leases: bus.KV(events.BucketTranscodeLeases)`.
- Modify: `cmd/clustarr`:
  - remove `--job` (`services.go:255-256`) and the `--role worker` wording (`services.go:244`);
  - `main.go`'s `exitCode` returns 1 for every error;
  - move `TestMain`, `runMainEnv`, `runClustarr`, `unreachableKubeconfig` and the RBAC helpers from
    `squasharr_worker_test.go` into a new `helpers_test.go`;
  - delete `TestSquasharrWorkerExitCodeReachesTheProcess` and
    `TestExitCodeOnlyHonoursTheSquasharrWorker`.
- Modify:
  - `squasharr/controller/transcodejob/controller_envtest_test.go`, `events_envtest_test.go` and
    `parity_envtest_test.go`;
  - `squasharr/observability_test.go`;
  - `importarr/worker/rescan/transcodeoutput_envtest_test.go:50`, where
    `ManagerSquasharrWorker` becomes `k8s.ManagerSquasharr`.

**Interfaces:**
- Consumes:
  - Task 1: `events.WorkTranscodeTaskSubject`, `MsgIDForTranscodeTask`, `ConsumerSquasharrResults`
  - Task 3: `task.*`, `worker.BuildTask`
  - Task 4: the new status fields, `ConditionBlocked`, `HardwareAuto`
  - Task 8: `pool.Config`, `pool.Key`, `pool.Name`, `pool.Threads`
- Produces:
  ```go
  type Reconciler struct {
  	Client   client.Client
  	Reader   client.Reader        // uncached: writeStatus reads through it
  	Slots    map[string]int32
  	Pool     pool.Config
  	Recorder k8sevents.EventRecorder
  	Bus      events.Bus           // publishes tasks and lifecycle events; subscribes the results
  	Leases   events.KV            // cancel markers (Task 12)
  	Now      func() time.Time
  }
  func (r *Reconciler) writeStatus(ctx context.Context, key types.NamespacedName,
  	change func(tj *transcodev1alpha1.TranscodeJob, st *transcodev1alpha1.TranscodeJobStatus) bool,
  ) (before transcodev1alpha1.TranscodeJobStatus, after *transcodev1alpha1.TranscodeJob, err error)
  func (r *Reconciler) patchCAS(ctx context.Context, tj *transcodev1alpha1.TranscodeJob, st *transcodev1alpha1.TranscodeJobStatus) error
  func (r *Reconciler) ResultsConsumer() manager.Runnable
  func (r *Reconciler) classFor(tj *transcodev1alpha1.TranscodeJob, tp *transcodev1alpha1.TranscodeProfile) transcodev1alpha1.Hardware // Task 13 extends
  func poolKeyFor(tp *transcodev1alpha1.TranscodeProfile, class transcodev1alpha1.Hardware) pool.Key
  const MaxAttempts = 5
  var RequeueBackoff = []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, 30 * time.Minute}
  type Decision struct { Phase transcodev1alpha1.TranscodeJobPhase; Reason, Message string
  	Block, Requeue, FallbackCPU, NoOp bool; After time.Duration }
  func Decide(ev task.StatusEvent, st transcodev1alpha1.TranscodeJobStatus, auto bool) Decision
  // squasharr/status
  func PatchCAS(ctx context.Context, c client.Client, job *transcodev1alpha1.TranscodeJob,
  	mutate func(*transcodeac.TranscodeJobStatusApplyConfiguration)) error
  ```

- [ ] **Step 1: Write the failing decision table test**

`squasharr/controller/transcodejob/decide_test.go`:

```go
package transcodejob

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/squasharr/task"
)

func TestDecide(t *testing.T) {
	gpu := transcodev1alpha1.TranscodeJobStatus{Attempts: 1, Hardware: transcodev1alpha1.HardwareNVIDIA}
	cpu := transcodev1alpha1.TranscodeJobStatus{Attempts: 1, Hardware: transcodev1alpha1.HardwareCPU}
	fin := func(o task.Outcome, r task.Reason) task.StatusEvent {
		return task.StatusEvent{Kind: task.EventFinished, Outcome: o, Reason: r, Message: "m"}
	}
	for _, tc := range []struct {
		name string
		ev   task.StatusEvent
		st   transcodev1alpha1.TranscodeJobStatus
		auto bool
		want Decision
	}{
		{"succeeded", fin(task.OutcomeSucceeded, ""), cpu, false, Decision{Phase: transcodev1alpha1.TranscodeJobPhaseSucceeded}},
		{"skipped", fin(task.OutcomeSkipped, "compliant"), cpu, false,
			Decision{Phase: transcodev1alpha1.TranscodeJobPhaseSkipped, Reason: "compliant", Message: "m"}},
		{"cancelled is left to the withdrawal", fin(task.OutcomeCancelled, task.ReasonCancelled), cpu, false, Decision{NoOp: true}},
		{"retriable requeues after 1m", fin(task.OutcomeFailed, task.ReasonRetriable), cpu, false,
			Decision{Phase: transcodev1alpha1.TranscodeJobPhasePlanned, Requeue: true, After: time.Minute, Reason: "Retriable", Message: "m"}},
		{"the 4th retry waits 30m", fin(task.OutcomeFailed, task.ReasonRetriable),
			transcodev1alpha1.TranscodeJobStatus{Attempts: 4, Hardware: "cpu"}, false,
			Decision{Phase: transcodev1alpha1.TranscodeJobPhasePlanned, Requeue: true, After: 30 * time.Minute, Reason: "Retriable", Message: "m"}},
		{"retries exhausted block", fin(task.OutcomeFailed, task.ReasonRetriable),
			transcodev1alpha1.TranscodeJobStatus{Attempts: MaxAttempts, Hardware: "cpu"}, false,
			Decision{Phase: transcodev1alpha1.TranscodeJobPhaseFailed, Block: true, Reason: "RetriesExhausted"}},
		{"auto GPU failure falls back to CPU at once", fin(task.OutcomeFailed, task.ReasonGPUEncodeFailed), gpu, true,
			Decision{Phase: transcodev1alpha1.TranscodeJobPhasePlanned, Requeue: true, FallbackCPU: true, Reason: "GPUEncodeFailed", Message: "m"}},
		{"auto GPU unavailable falls back too", fin(task.OutcomeFailed, task.ReasonGPUUnavailable), gpu, true,
			Decision{Phase: transcodev1alpha1.TranscodeJobPhasePlanned, Requeue: true, FallbackCPU: true, Reason: "GPUUnavailable", Message: "m"}},
		{"a pinned GPU job retries, never falls back", fin(task.OutcomeFailed, task.ReasonGPUEncodeFailed), gpu, false,
			Decision{Phase: transcodev1alpha1.TranscodeJobPhasePlanned, Requeue: true, After: time.Minute, Reason: "GPUEncodeFailed", Message: "m"}},
		{"source changed fails unblocked", fin(task.OutcomeFailed, task.ReasonSourceChanged), cpu, false,
			Decision{Phase: transcodev1alpha1.TranscodeJobPhaseFailed, Reason: "SourceChanged", Message: "m"}},
		{"verify failed blocks", fin(task.OutcomeFailed, task.ReasonVerifyFailed), cpu, false,
			Decision{Phase: transcodev1alpha1.TranscodeJobPhaseFailed, Block: true, Reason: "VerifyFailed", Message: "m"}},
		{"invalid source blocks", fin(task.OutcomeFailed, task.ReasonInvalidSource), cpu, false,
			Decision{Phase: transcodev1alpha1.TranscodeJobPhaseFailed, Block: true, Reason: "InvalidSource", Message: "m"}},
		{"deadline blocks", fin(task.OutcomeFailed, task.ReasonDeadlineExceeded), cpu, false,
			Decision{Phase: transcodev1alpha1.TranscodeJobPhaseFailed, Block: true, Reason: "DeadlineExceeded", Message: "m"}},
		{"an unknown reason blocks rather than loops", fin(task.OutcomeFailed, "Surprise"), cpu, false,
			Decision{Phase: transcodev1alpha1.TranscodeJobPhaseFailed, Block: true, Reason: "Surprise", Message: "m"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Decide(tc.ev, tc.st, tc.auto)
			if tc.want.Reason == "RetriesExhausted" {
				assert.Contains(t, got.Message, "5 attempts")
				got.Message = ""
			}
			assert.Equal(t, tc.want, got)
		})
	}
}
```

Run: `go test ./squasharr/controller/transcodejob/ -run TestDecide`
Expected: FAIL to compile, "undefined: Decide".

- [ ] **Step 2: Write `decide.go`**

```go
// MaxAttempts is how many dispatches a retriable failure gets before it is blocked.
const MaxAttempts = 5

// RequeueBackoff is the wait before dispatch n+1 after attempt n failed
// retriably; the last entry repeats.
var RequeueBackoff = []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, 30 * time.Minute}

// Decision is squasharr's next step for a finished attempt (spec §18.3).
type Decision struct {
	Phase       transcodev1alpha1.TranscodeJobPhase
	Reason      string
	Message     string
	Block       bool          // Failed plus Blocked=True: not retried until the TranscodeJob is deleted
	Requeue     bool          // back to Planned for another dispatch
	After       time.Duration // with Requeue: status.nextAttemptAt = now + After
	FallbackCPU bool          // with Requeue: set status.fallbackReason, so the next dispatch is CPU
	NoOp        bool
}

// Decide is the next-step table. It is pure: the status it reads is the
// one the write is about to change.
func Decide(ev task.StatusEvent, st transcodev1alpha1.TranscodeJobStatus, auto bool) Decision {
	switch ev.Outcome {
	case task.OutcomeSucceeded:
		return Decision{Phase: transcodev1alpha1.TranscodeJobPhaseSucceeded}
	case task.OutcomeSkipped:
		return Decision{Phase: transcodev1alpha1.TranscodeJobPhaseSkipped, Reason: string(ev.Reason), Message: ev.Message}
	case task.OutcomeCancelled:
		return Decision{NoOp: true}
	}
	switch ev.Reason {
	case task.ReasonGPUUnavailable, task.ReasonGPUEncodeFailed:
		if auto && st.Hardware != transcodev1alpha1.HardwareCPU {
			return Decision{Phase: transcodev1alpha1.TranscodeJobPhasePlanned, Requeue: true, FallbackCPU: true,
				Reason: string(ev.Reason), Message: ev.Message}
		}
		return retry(ev, st)
	case task.ReasonRetriable:
		return retry(ev, st)
	case task.ReasonSourceChanged:
		return Decision{Phase: transcodev1alpha1.TranscodeJobPhaseFailed, Reason: string(ev.Reason), Message: ev.Message}
	default: // InvalidSource, VerifyFailed, DeadlineExceeded, and anything unknown
		return Decision{Phase: transcodev1alpha1.TranscodeJobPhaseFailed, Block: true, Reason: string(ev.Reason), Message: ev.Message}
	}
}

func retry(ev task.StatusEvent, st transcodev1alpha1.TranscodeJobStatus) Decision {
	if st.Attempts >= MaxAttempts {
		return Decision{Phase: transcodev1alpha1.TranscodeJobPhaseFailed, Block: true,
			Reason:  string(task.ReasonRetriesExhausted),
			Message: fmt.Sprintf("%d attempts; the last failed with %s: %s", st.Attempts, ev.Reason, ev.Message)}
	}
	i := min(max(int(st.Attempts)-1, 0), len(RequeueBackoff)-1)
	return Decision{Phase: transcodev1alpha1.TranscodeJobPhasePlanned, Requeue: true, After: RequeueBackoff[i],
		Reason: string(ev.Reason), Message: ev.Message}
}

// applyDecision writes d onto st. Conditions are set once, here, for this
// write (CLAUDE.md: WithConditions appends).
func applyDecision(tj *transcodev1alpha1.TranscodeJob, st *transcodev1alpha1.TranscodeJobStatus,
	ev task.StatusEvent, d Decision, now time.Time,
) {
	if ev.StderrTail != "" {
		st.StderrTail = ev.StderrTail
	}
	cond := func(typ string, status metav1.ConditionStatus, reason, msg string) {
		meta.SetStatusCondition(&st.Conditions, metav1.Condition{Type: typ, Status: status,
			Reason: cmp.Or(reason, "Unknown"), Message: msg, ObservedGeneration: tj.Generation})
	}
	switch {
	case d.NoOp:
	case d.Requeue:
		st.Phase, st.WorkerPod, st.Progress = transcodev1alpha1.TranscodeJobPhasePlanned, "", nil
		st.NextAttemptAt = nil
		if d.After > 0 {
			st.NextAttemptAt = &metav1.Time{Time: now.Add(d.After)}
		}
		if d.FallbackCPU {
			st.FallbackReason = truncate(fmt.Sprintf("GPU attempt %d on %s: %s: %s", st.Attempts, st.Hardware, d.Reason, d.Message), 256)
		}
		st.Message = truncate(fmt.Sprintf("attempt %d failed (%s): %s; requeued", st.Attempts, d.Reason, d.Message), 1024)
		cond(transcodev1alpha1.ConditionJobCreated, metav1.ConditionFalse, "Requeued", st.Message)
	case d.Phase == transcodev1alpha1.TranscodeJobPhaseSucceeded:
		st.Phase, st.Result, st.FinishedAt = d.Phase, ev.Result, &metav1.Time{Time: ev.At}
		cond(transcodev1alpha1.ConditionVerified, metav1.ConditionTrue, ReasonWorkerVerified, "")
		cond(transcodev1alpha1.ConditionSucceeded, metav1.ConditionTrue, ReasonJobSucceeded, "")
	case d.Phase == transcodev1alpha1.TranscodeJobPhaseSkipped:
		st.Phase, st.FinishedAt, st.Message = d.Phase, &metav1.Time{Time: ev.At}, d.Message
	default: // Failed
		st.Phase, st.FinishedAt, st.Message = transcodev1alpha1.TranscodeJobPhaseFailed, &metav1.Time{Time: ev.At}, d.Message
		cond(transcodev1alpha1.ConditionFailed, metav1.ConditionTrue, d.Reason, d.Message)
		if d.Block {
			cond(transcodev1alpha1.ConditionBlocked, metav1.ConditionTrue, d.Reason,
				d.Message+" (delete the TranscodeJob to retry)")
		}
	}
}
```

Use the condition-type constants `transcodev1alpha1` declares (`transcodejob_types.go:68-79`,
plus Task 4's `ConditionBlocked`). If a constant's Go name differs from the one used here, use the
declared name. Add `truncate(s string, n int) string` if the package lacks one.

Run: `go test ./squasharr/controller/transcodejob/ -run TestDecide`
Expected: PASS.

- [ ] **Step 3: Make status single-writer and add `PatchCAS`**

In `squasharr/status/status.go`:
- `ControllerFields` also always sends `StderrTail`, `WorkerPod`, `Hardware` and `FallbackReason`.
  It sends `Progress` (through `progressAC`), `Result` (through `resultAC`) and `NextAttemptAt`
  when they are non-nil.
- Delete `WorkerFields` and the `ManagerSquasharrWorker` branch of `Patch`, so `Patch` accepts only
  `k8s.ManagerSquasharr`.
- Add `PatchCAS`, following `catalogarr/worker/grab/kindops.go:181`:

```go
// PatchCAS applies squasharr's complete status for job, conditional on
// job.ResourceVersion: a write that raced another returns a Conflict instead
// of silently rolling that other write back (spec §18.2).
func PatchCAS(ctx context.Context, c client.Client, job *transcodev1alpha1.TranscodeJob,
	mutate func(*transcodeac.TranscodeJobStatusApplyConfiguration),
) error {
	ac := ControllerFields(job.Status)
	if mutate != nil {
		mutate(ac)
	}
	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerSquasharr,
		transcodeac.TranscodeJob(job.Name, job.Namespace).WithResourceVersion(job.ResourceVersion).WithStatus(ac))
	return err
}
```

- Rewrite the file comment's two-manager paragraph (lines 41-80): squasharr owns all of
  `TranscodeJob.status`, written by its reconciler and its results consumer through one CAS path.

In `split_test.go`:
- delete `TestWorkerFieldsDeclaresExactlyItsOwnSet` and the disjointness test;
- extend the controller field-accounting test to list `stderrTail`, `workerPod`, `hardware`,
  `fallbackReason`, `nextAttemptAt`, `progress.*` and `result.*`.

In `status_envtest_test.go`, delete the worker-manager cases (lines 131, 168, 270 and 427), then
add:

```go
func TestPatchCASRejectsAStaleResourceVersion(t *testing.T) {
	c := newTestClient(t)
	job := newJob(t, c) // the file's existing TranscodeJob fixture helper
	stale := job.DeepCopy()
	job.Status.Message = "first"
	require.NoError(t, status.PatchCAS(context.Background(), c, job, nil))
	stale.Status.Message = "second"
	err := status.PatchCAS(context.Background(), c, stale, nil)
	require.True(t, apierrors.IsConflict(err), "a stale write must conflict, got %v", err)
}
```

Delete `ManagerSquasharrWorker` from `pkg/k8s/fieldmanager.go`, from `FieldManagers()` and from
`fieldmanager_test.go`.

- [ ] **Step 4: Write `write.go`**

```go
// patchCAS is the reconciler's and the results consumer's one status write:
// the dead-letter fold, sorted conditions, and an apply conditional on the
// resourceVersion st was read with.
func (r *Reconciler) patchCAS(ctx context.Context, tj *transcodev1alpha1.TranscodeJob, st *transcodev1alpha1.TranscodeJobStatus) error {
	seed := tj.DeepCopy()
	seed.Status = *st.DeepCopy()
	k8s.MarkDeadLettered(seed, &seed.Status.Conditions) // exactly as apply() did at controller.go:661-675
	sortConditions(seed.Status.Conditions)
	return squasharrstatus.PatchCAS(ctx, r.Client, seed, func(ac *transcodeac.TranscodeJobStatusApplyConfiguration) {
		ac.WithConditions(k8s.ConditionACs(seed.Status.Conditions)...)
	})
}

// writeStatus reads key fresh through the uncached reader, lets change edit
// a copy of its status, and applies it with the read resourceVersion. A
// Conflict (the other write path got there first) is redone from a new read
// up to three times. change returns false for nothing to write.
func (r *Reconciler) writeStatus(ctx context.Context, key types.NamespacedName,
	change func(*transcodev1alpha1.TranscodeJob, *transcodev1alpha1.TranscodeJobStatus) bool,
) (transcodev1alpha1.TranscodeJobStatus, *transcodev1alpha1.TranscodeJob, error) {
	var err error
	for try := 0; try < 3; try++ {
		var tj transcodev1alpha1.TranscodeJob
		if err = r.reader().Get(ctx, key, &tj); err != nil {
			return transcodev1alpha1.TranscodeJobStatus{}, nil, err
		}
		before := *tj.Status.DeepCopy()
		st := tj.Status.DeepCopy()
		if !change(&tj, st) {
			return before, nil, nil
		}
		if err = r.patchCAS(ctx, &tj, st); apierrors.IsConflict(err) {
			continue
		}
		if err != nil {
			return before, nil, err
		}
		tj.Status = *st
		return before, &tj, nil
	}
	return transcodev1alpha1.TranscodeJobStatus{}, nil, err
}
```

(Use the dead-letter fold and the condition sort that `apply` performs today, at
`controller.go:661-675`, under their existing names.)

- [ ] **Step 5: Write `dispatch.go`**

```go
// classFor is the class a Planned job dispatches to. Task 13 makes auto
// capacity-aware; here it is the plan's encoder's class (a remux takes a CPU
// slot), and CPU once a fallback reason is recorded.
func (r *Reconciler) classFor(tj *transcodev1alpha1.TranscodeJob, _ *transcodev1alpha1.TranscodeProfile) transcodev1alpha1.Hardware {
	if tj.Status.FallbackReason != "" || tj.Status.Plan == nil {
		return transcodev1alpha1.HardwareCPU
	}
	return hardwareForEncoder(tj.Status.Plan.Encoder)
}

func poolKeyFor(tp *transcodev1alpha1.TranscodeProfile, class transcodev1alpha1.Hardware) pool.Key {
	return pool.Key{Profile: tp.Name, ProfileUID: tp.UID, Class: class}
}

// dispatch publishes one admitted job's task, then records it: a job is never
// Queued without a task on the queue. The status write is conditional on the
// attempt count the task was built from, so a job cannot be dispatched twice.
func (r *Reconciler) dispatch(ctx context.Context, key types.NamespacedName, class transcodev1alpha1.Hardware) error {
	var tj transcodev1alpha1.TranscodeJob
	if err := r.reader().Get(ctx, key, &tj); err != nil {
		return client.IgnoreNotFound(err)
	}
	if tj.Status.Phase != transcodev1alpha1.TranscodeJobPhasePlanned || tj.DeletionTimestamp != nil {
		return nil
	}
	tp, ok, err := r.profile(ctx, &tj)
	if err != nil || !ok {
		return err
	}
	var mf catalogv1alpha1.MediaFile
	if err := r.Client.Get(ctx, types.NamespacedName{Namespace: tj.Namespace, Name: tj.Spec.MediaFileRef}, &mf); err != nil {
		return err
	}
	var folders catalogv1alpha1.RootFolderList
	if err := r.Client.List(ctx, &folders, client.InNamespace(tj.Namespace)); err != nil {
		return err
	}
	attempt := tj.Status.Attempts + 1
	t, buildErr := worker.BuildTask(&tj, tp, &mf, folders.Items, attempt, class)
	if errors.Is(buildErr, worker.ErrNoRootFolder) || errors.Is(buildErr, worker.ErrInvalidOutput) {
		_, _, err := r.writeStatus(ctx, key, func(tj *transcodev1alpha1.TranscodeJob, st *transcodev1alpha1.TranscodeJobStatus) bool {
			applyDecision(tj, st, task.StatusEvent{At: r.now().Time},
				Decision{Phase: transcodev1alpha1.TranscodeJobPhaseFailed, Block: true,
					Reason: string(task.ReasonInvalidSource), Message: buildErr.Error()}, r.now().Time)
			return true
		})
		return err
	}
	if buildErr != nil {
		return buildErr
	}
	sch, data, err := schema.Encode(t)
	if err != nil {
		return err
	}
	id := events.MsgIDForTranscodeTask(string(tj.UID), attempt)
	env := &events.Envelope{ID: id, Type: "transcode.Task", Schema: sch, Source: "squasharr-controller@" + version.Version,
		Key: tj.Namespace + "/" + tj.Name, Time: r.now().Time, Data: data}
	k := poolKeyFor(tp, class)
	if _, err := r.Bus.Publish(ctx, events.WorkTranscodeTaskSubject(string(tp.UID), string(class), string(tj.UID)), env,
		events.WithMsgID(id), events.WithExpectStream(events.StreamWorkSquasharr)); err != nil {
		_, _, _ = r.writeStatus(ctx, key, func(_ *transcodev1alpha1.TranscodeJob, st *transcodev1alpha1.TranscodeJobStatus) bool {
			st.Message = fmt.Sprintf("dispatch: %v", err)
			return true
		})
		return err
	}
	_, after, err := r.writeStatus(ctx, key, func(tj *transcodev1alpha1.TranscodeJob, st *transcodev1alpha1.TranscodeJobStatus) bool {
		if st.Phase != transcodev1alpha1.TranscodeJobPhasePlanned || st.Attempts != attempt-1 {
			return false // someone else moved it; the published task is a duplicate the Msg-Id absorbs
		}
		st.Phase, st.Attempts, st.Hardware = transcodev1alpha1.TranscodeJobPhaseQueued, attempt, class
		st.JobRef, st.WorkerPod, st.NextAttemptAt = ptr.To(pool.Name(k)), "", nil
		st.Message = fmt.Sprintf("queued for pool %s", pool.Name(k))
		meta.SetStatusCondition(&st.Conditions, metav1.Condition{Type: transcodev1alpha1.ConditionJobCreated,
			Status: metav1.ConditionTrue, Reason: "Dispatched", Message: st.Message, ObservedGeneration: tj.Generation})
		return true
	})
	if after != nil {
		r.afterWrite(ctx, after, &tj.Status)
	}
	return err
}
```

- [ ] **Step 6: Write `results.go`**

```go
// ResultsConsumer subscribes squasharr-transcode-results on the leader only:
// one writer of the status those events describe (spec §18.2).
func (r *Reconciler) ResultsConsumer() manager.Runnable { return resultsConsumer{r} }

type resultsConsumer struct{ r *Reconciler }

func (c resultsConsumer) Start(ctx context.Context) error {
	spec, ok := events.Default().Consumer(events.ConsumerSquasharrResults)
	if !ok {
		return errors.New("squasharr: squasharr-transcode-results is missing from the topology")
	}
	stop, err := c.r.Bus.Subscribe(ctx, spec.Subscription(), c.r.handleEvent)
	if err != nil {
		return fmt.Errorf("squasharr: subscribe %s: %w", spec.Name, err)
	}
	<-ctx.Done()
	stop()
	return nil
}

// NeedLeaderElection implements manager.LeaderElectionRunnable.
func (resultsConsumer) NeedLeaderElection() bool { return true }

// handleEvent turns one worker status event into status and, for finished,
// the next step. It is acked (nil) once the write landed or when the event
// no longer applies; a returned error naks it with the consumer's backoff.
func (r *Reconciler) handleEvent(ctx context.Context, m events.Message) error {
	var ev task.StatusEvent
	if err := schema.Decode(m.Envelope().Schema, m.Envelope().Data, &ev); err != nil {
		return events.Discard("undecodable transcode status event", err)
	}
	key := types.NamespacedName{Namespace: ev.Job.Namespace, Name: ev.Job.Name}
	before, after, err := r.writeStatus(ctx, key, func(tj *transcodev1alpha1.TranscodeJob, st *transcodev1alpha1.TranscodeJobStatus) bool {
		if string(tj.UID) != ev.Job.UID || tj.DeletionTimestamp != nil || st.Attempts != ev.Attempt ||
			(st.Phase != transcodev1alpha1.TranscodeJobPhaseQueued && st.Phase != transcodev1alpha1.TranscodeJobPhaseRunning) {
			return false // stale, duplicate, or for a job that moved on
		}
		switch ev.Kind {
		case task.EventClaimed, task.EventProgress:
			st.Phase, st.WorkerPod = transcodev1alpha1.TranscodeJobPhaseRunning, ev.Pod
			if st.StartedAt == nil {
				st.StartedAt = &metav1.Time{Time: ev.At}
			}
			if ev.Progress != nil && (st.Progress == nil || !ev.Progress.UpdatedAt.Before(&st.Progress.UpdatedAt)) {
				p := *ev.Progress
				if p.UpdatedAt.IsZero() {
					p.UpdatedAt = metav1.Time{Time: ev.At}
				}
				st.Progress = &p
			}
			return true
		case task.EventFinished:
			d := Decide(ev, *st, r.isAuto(ctx, tj))
			applyDecision(tj, st, ev, d, r.now().Time)
			return !d.NoOp || ev.StderrTail != ""
		}
		return false
	})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if after != nil {
		r.afterWrite(ctx, after, &before)
	}
	return nil
}

// isAuto reports whether tj chooses its class per dispatch (spec §18.5).
func (r *Reconciler) isAuto(ctx context.Context, tj *transcodev1alpha1.TranscodeJob) bool {
	if tj.Spec.Hardware != nil && *tj.Spec.Hardware != "" {
		return *tj.Spec.Hardware == transcodev1alpha1.HardwareAuto
	}
	tp, ok, err := r.profile(ctx, tj)
	return err == nil && ok && (tp.Spec.Hardware == transcodev1alpha1.HardwareAuto || tp.Spec.Hardware == "")
}
```

- [ ] **Step 7: Rewire `controller.go`**

- **`afterWrite(ctx, after *TranscodeJob, before *TranscodeJobStatus)`.** Factor out of `Reconcile`
  the steps after its apply: `transitions`, `publishJobEvents`, `recordEvents` and
  `observeFinished`, including the "already counted" guard. `Reconcile`, `dispatch` and
  `handleEvent` all call it after a write lands.
- **`Reconcile`.** The terminal re-apply and the normal path both write with
  `r.patchCAS(ctx, &tj, desired)` on the `tj` read at the top, so they carry its
  `resourceVersion`. On a Conflict, return the error; controller-runtime requeues and the next
  pass reads fresh. Delete `apply` and `controllerView`.
- **`advance`:**
  - Pending: `plan` (unchanged).
  - Planned: return `RequeueAfter` until `NextAttemptAt`, or `requeueQueued`; admission
    dispatches it.
  - Queued or Running, carrying the dead-lettered annotation that `k8s.MarkDeadLettered` reads:
    `applyDecision(…, Decision{Phase: Failed, Block: true, Reason: "DeadLettered", Message: "the task was dead-lettered after its last delivery"}, …)`.
  - Queued or Running otherwise: return `RequeueAfter: time.Minute`. Events drive these jobs.
  - Delete `ensureJob`, `observe`, `setSuspend` and `jobSignal`.
- **`admit(ctx)`:**
  - List TranscodeJobs through `r.reader()`.
  - `running`: jobs in Queued or Running, as `Slot{Key: ns/name, Hardware: string(st.Hardware), Profile: spec.ProfileRef}`.
  - `queued`: jobs that are Planned, not suspended, not deleting, and not waiting (`NextAttemptAt`
    nil or ≤ now), with `Hardware: string(r.classFor(tj, tp))`, and priority and `Created` as
    today.
  - Call `Admit(queued, running, Budget{…})`, then `setActive`, then
    `r.dispatch(ctx, key, Hardware(slot.Hardware))` for each admitted slot.
- **`SetupWithManager`.** Delete `Owns(&batchv1.Job{}, …)`. There is no KV source: status events
  arrive through the results consumer, which is a separate runnable.
- **`events.go`.** `transitions` emits `queued` when `st.Attempts > old.Attempts`, not on a
  `JobRef` nil→set edge.

- [ ] **Step 8: Replace stale SourceChanged jobs in the TranscodeProfile controller**

In `transcodeprofile/controller.go`'s loop over matching files, before `ensureTranscodeJob`, add
the following. Also add `delete` to its `transcodejobs` RBAC marker.

```go
		// A job that failed because its source changed can never succeed: its
		// sourceProbeHash is immutable. Once the MediaFile has a new probe,
		// replace the job so the next pass plans the new file (spec §18.3).
		if old, ok := jobsByFile[mf.Name]; ok && old.Status.Phase == transcodev1alpha1.TranscodeJobPhaseFailed &&
			failedReason(old) == string(task.ReasonSourceChanged) && old.Spec.SourceProbeHash != mf.Status.ProbeHash {
			if err := r.Delete(ctx, old); client.IgnoreNotFound(err) != nil {
				return ctrl.Result{}, err
			}
			continue
		}
```

`jobsByFile` indexes the TranscodeJobs the reconciler already lists for `countJobs`, by
`spec.mediaFileRef`, for this profile. `failedReason` returns the reason of the job's `Failed`
condition.

Test, in `transcodeprofile`'s envtest:

```go
func TestASourceChangedJobIsReplacedForTheNewFile(t *testing.T) {
	// Arrange a profile, a probed MediaFile (probeHash "p1") and its TranscodeJob, then mark the
	// job Failed with a Failed condition of reason SourceChanged, through status.PatchCAS.
	// Set the MediaFile's probeHash to "p2" and reconcile the profile.
	// The old job is gone. After a second reconcile a job for the file exists with sourceProbeHash "p2".
}
```

Write it out fully with the package's existing fixture helpers (`newTestClient`, and its profile
and MediaFile helpers).

- [ ] **Step 9: Resolve dead-lettered tasks to their TranscodeJob**

- In `catalogarr/history/target.go`, add a resolver for payload schema `transcode.Task.v1`. It
  decodes only `struct{ Job schema.Ref \`json:"job"\` }` and names the TranscodeJob
  `(Job.Namespace, Job.Name)`, so catalogarr does not import `squasharr/task`.
- Add the schema to that package's resolver table test.
- Add `transcode.Task` beside `transcode.JobEvent` in `pkg/k8s/deadletter.go:58`.

- [ ] **Step 10: Remove the worker role from `squasharr/run.go` and `cmd/clustarr`**

In `squasharr/run.go`:
- delete `RoleWorker` (`Roles()` returns `[]Role{RoleController}`), `runWorker`, `runWorkerJob`,
  `exitForGet`, `ExitError`, `JobName` and the worker `Validate` branch;
- `jobConfig` becomes:

```go
func poolConfig(o Options) pool.Config {
	return pool.Config{
		Namespace: o.Namespace, Image: o.WorkerImage, ImageCUDA: o.WorkerImageCUDA,
		DataClaimName: o.DataClaimName, DataDir: o.DataDir, IntelRenderGroups: o.IntelRenderGroups,
		Umask: os.Getenv(pool.UmaskEnv), NATSURL: o.NATSURL,
		ExtraArgs: workerObservabilityArgs(o.Logging, o.Tracing),
	}
}
```

`setupControllers` builds the reconciler with `Pool: poolConfig(o)` and
`Leases: bus.KV(events.BucketTranscodeLeases)`, then
`if err := mgr.Add(rec.ResultsConsumer()); err != nil { return … }`. Keep `WorkerServiceAccount`
for now: Task 14 removes it with its RBAC.

Run `go test ./cmd/clustarr/ -run 'Registration|Runnable'`. The registration guard derives what
each service registers; make it accept the leader-only results consumer, following whatever rule
it applies to other `NeedLeaderElection` runnables.

In `cmd/clustarr`:
- delete the `--job` flag and `jobName`;
- `exitCode` becomes `return 1`;
- move the shared test helpers into `helpers_test.go`, then delete the two worker exit-code tests;
- `squasharr/observability_test.go`'s `TestJobConfigCarriesTheControllerOptions` becomes
  `TestPoolConfigCarriesTheControllerOptions`, over `poolConfig`.

- [ ] **Step 11: Write the controller envtests**

In `controller_envtest_test.go`:
- `newReconciler(c, slots)` builds `Pool: pool.Config{Namespace: "default", Image: "transcoder:test", ImageCUDA: "transcoder-cuda:test"}`.
- It wires `Bus` and `Leases` from a `membus.New(nil)` ensured with `events.Default().ForSingleNode()`.
- A test worker delivers events straight to `handleEvent` through a fake message:

```go
type fakeMsg struct{ env *events.Envelope }

func (f fakeMsg) Ack(context.Context) error                  { return nil }
func (f fakeMsg) Nak(context.Context, time.Duration) error   { return nil }
func (f fakeMsg) Term(context.Context, string) error         { return nil }
func (f fakeMsg) InProgress(context.Context) error           { return nil }
func (f fakeMsg) Envelope() *events.Envelope                 { return f.env }
func (f fakeMsg) Subject() string                            { return "" }
func (f fakeMsg) Attempt() uint64                            { return 1 }

func deliver(t *testing.T, r *transcodejob.Reconciler, tj *transcodev1alpha1.TranscodeJob, ev task.StatusEvent) error {
	t.Helper()
	ev.Job = schema.Ref{Namespace: tj.Namespace, Name: tj.Name, UID: string(tj.UID)}
	if ev.At.IsZero() {
		ev.At = time.Now()
	}
	sch, data, err := schema.Encode(ev)
	require.NoError(t, err)
	return transcodejob.HandleEventForTest(r, context.Background(), fakeMsg{&events.Envelope{Schema: sch, Data: data}})
}
```

Export `HandleEventForTest` from a `export_test.go` in the package:
`var HandleEventForTest = (*Reconciler).handleEvent`.

The tests:

```go
func TestDispatchPublishesTheTaskThenQueues(t *testing.T) {
	_, c := startEnv(t)
	ns := newNamespace(t, c)
	r := newReconciler(c, map[string]int32{"cpu": 1})
	newRootFolder(t, c, ns, "/data/media") // add this helper if the file lacks one
	tp := newProfile(t, c, "hevc", "hash1", nil)
	mf := newMediaFile(t, c, ns, "mf", "/data/media/Heat.mkv", h264Probe())
	tj := newTJ(t, c, ns, mf, tp)

	reconcileTJ(t, r, tj) // Pending -> Planned -> admitted -> Queued
	got := getTJ(t, c, tj)
	require.Equal(t, transcodev1alpha1.TranscodeJobPhaseQueued, got.Status.Phase)
	assert.Equal(t, int32(1), got.Status.Attempts)
	assert.Equal(t, transcodev1alpha1.HardwareCPU, got.Status.Hardware)
	assert.Equal(t, pool.Name(pool.Key{Profile: tp.Name, ProfileUID: tp.UID, Class: "cpu"}), *got.Status.JobRef)

	p, err := r.Bus.(events.PullSubscriber).Pull(context.Background(), events.TranscodeTaskConsumer(string(tp.UID), "cpu").Subscription())
	require.NoError(t, err)
	defer p.Stop()
	_, m, err := p.Next(ctxWithin(t, 5*time.Second))
	require.NoError(t, err, "Queued means the task is on the queue")
	var tk task.Task
	require.NoError(t, schema.Decode(m.Envelope().Schema, m.Envelope().Data, &tk))
	assert.Equal(t, got.Status.Plan.ArgsHash, tk.ArgsHash)
	assert.Equal(t, int32(1), tk.Attempt)
}

func TestStatusEventsDriveStatusAndOneManagerOwnsIt(t *testing.T) {
	// Arrange a job as in TestDispatchPublishesTheTaskThenQueues, reconciled to Queued, then:
	//   - deliver claimed{Attempt 1, Pod "pool-xyz"}: Running, workerPod pool-xyz, startedAt set;
	//   - deliver progress{Attempt 1, Progress{Percent: 42}}: status.progress.percent 42;
	//   - deliver finished{Attempt 1, succeeded, Result{OutputPath, OutputSizeBytes: 10}, StderrTail "ok"}:
	//     Succeeded, result.outputSizeBytes 10, stderrTail "ok", and the Succeeded condition True.
	// Then walk got.ManagedFields: every entry touching f:status has Manager == "squasharr".
	// Use the file's statusFieldsOf helper (line 799), adapted to its return shape.
}

func TestRetriableRequeuesWithBackoffThenBlocks(t *testing.T) {
	// A Queued job at attempt 1. Deliver finished{failed, Retriable}: Planned, nextAttemptAt = now+1m, message names the attempt.
	// Reconcile with r.Now = now: stays Planned, nothing published.
	// Reconcile with r.Now = now+61s: Queued, attempts 2.
	// Repeat to attempt 5. After the fifth Retriable: Failed, with Blocked=True reason RetriesExhausted.
}

func TestVerifyFailedBlocks(t *testing.T) {
	// Queued at attempt 1; deliver finished{failed, VerifyFailed}: Failed, with Failed and Blocked conditions of reason VerifyFailed.
	// A further reconcile publishes nothing.
}

func TestAStaleEventChangesNothing(t *testing.T) {
	// Queued at attempt 2 (dispatch, deliver Retriable, advance r.Now, dispatch again).
	// Deliver finished{Attempt 1, succeeded}: it returns nil (acked) and the job is still Queued at attempt 2.
}

// Review Focus 5.
func TestAResultEventRacingAReconcileIsNotLost(t *testing.T) {
	// Queued at attempt 1. Read the job into `stale`. Deliver claimed{Pod "pool-xyz"}: Running.
	// Call r.PatchCASForTest(ctx, stale, &stale.Status) (export via export_test.go): it returns a Conflict.
	// Reconcile once more: the job is still Running with workerPod pool-xyz. The reconciler's write
	// was redone from a fresh read, not applied over the event's.
}

func TestNoQueuedWithoutAPublish(t *testing.T) {
	// Wrap r.Bus in failingPublisher{Bus: r.Bus}, whose Publish returns events.ErrQueueFull.
	// Reconcile a Planned job: it stays Planned at attempts 0, and the message names the error.
}

func TestASourceUnderNoRootFolderBlocksAtDispatch(t *testing.T) {
	// No RootFolder in the namespace. Reconcile: Failed with Blocked=True reason InvalidSource, and nothing published.
}

func TestADeadLetteredTaskBlocksTheJob(t *testing.T) {
	// Queued at attempt 1. Set the dead-lettered annotation on the job (the key k8s.MarkDeadLettered reads).
	// Reconcile: Failed with Blocked=True reason DeadLettered.
}
```

Write each outlined test out fully, in the style of `TestDispatchPublishesTheTaskThenQueues`. The
comments give the exact sequence and assertions. `failingPublisher` embeds `events.Bus` and
overrides `Publish`.

Delete the Job-driven tests that no longer describe the system:
- `TestPlanQueueAdmitRun`
- `TestJobFromATypedClientProfileGetsTheCRDDefaults`
- `TestUserSuspendPausesAndResumes`, which Task 12 rewrites
- `completeJob`, `failJob` and `getJob`

Drive `TestTransientFailureDoesNotReleaseStatus` and `TestTerminalMetricsObservedOnce` with
`deliver(… finished …)`. Keep `TestAdmissionHonoursTheBudget` and
`TestAdmissionHonoursProfileMaxConcurrent`, asserting on dispatched attempts. In
`events_envtest_test.go`, drive the lifecycle events with `deliver`.

Update `parity_envtest_test.go`'s `TestStatusPlanIsTheArgvTheWorkerRenders`. It builds the worker's
inputs with `worker.BuildTask(tj, tp, mf, folders, 1, class)`, where `class` is the job's class,
and reads the planned threads from `pool.Template(tp, class, cfg)`'s env through `cpuLimitEnv`. It
still asserts `transcode.ArgsHash(workerPlan) == status.plan.argsHash` for HDR10 in place, a
container change and a kept source.

- [ ] **Step 12: Run everything squasharr and cmd touch**

Run: `go test ./squasharr/... ./pkg/k8s/... ./cmd/clustarr/... ./catalogarr/history/... ./importarr/worker/rescan/...`
Expected: PASS with `KUBEBUILDER_ASSETS` set. Then run `make manifests`, copy the regenerated
squasharr role into the chart's sentinel block, and run `make lint`: no new findings and no
forbidigo hits.

- [ ] **Step 13: Commit**

```bash
git add squasharr pkg/k8s catalogarr/history cmd/clustarr importarr/worker/rescan/transcodeoutput_envtest_test.go config/rbac/squasharr_role.yaml charts/clustarr/templates/rbac.yaml
git commit -m 'feat(squasharr): dispatch over NATS; consume squasharr-transcode-results; one CAS status writer; requeue, fall back or block' -- squasharr pkg/k8s catalogarr/history cmd/clustarr importarr/worker/rescan/transcodeoutput_envtest_test.go config/rbac/squasharr_role.yaml charts/clustarr/templates/rbac.yaml
```

---

### Task 11: Pools in the admission pass

**Files:**
- Modify: `squasharr/controller/transcodejob/controller.go`: a `pools` step after dispatch, a
  watch on pool Jobs, and holding admission for draining pools.
- Create: `squasharr/controller/transcodejob/pools.go`.
- Modify: `squasharr/controller/transcodejob/doc.go:112`: the RBAC marker verbs become
  `get;list;watch;create;patch;delete`.
- Generated: `config/rbac/squasharr_role.yaml`; copy it into the sentinel block in
  `charts/clustarr/templates/rbac.yaml`.
- Test: `squasharr/controller/transcodejob/pools_envtest_test.go`.

**Interfaces:**
- Consumes: `pool.*` (Tasks 8-9), `dispatch`, `classFor` and `poolKeyFor` (Task 10), `k8s.ManagerSquasharrPool`.
- Produces: `(*Reconciler).pools(ctx context.Context, dispatched map[pool.Key]int32) (draining map[pool.Key]bool, err error)`.

- [ ] **Step 1: Write the failing envtest**

This uses the default envtest (gates off), so no minCount assertions; Task 9 covers the gate.

```go
func TestPoolFollowsDispatch(t *testing.T) {
	_, c := startEnv(t)
	ns := newNamespace(t, c)
	r := newReconciler(c, map[string]int32{"cpu": 2})
	newRootFolder(t, c, ns, "/data/media")
	tp := newProfile(t, c, "hevc", "hash1", nil)
	a := newTJ(t, c, ns, newMediaFile(t, c, ns, "a", "/data/media/A.mkv", h264Probe()), tp)
	b := newTJ(t, c, ns, newMediaFile(t, c, ns, "b", "/data/media/B.mkv", h264Probe()), tp)
	reconcileTJ(t, r, a)
	reconcileTJ(t, r, b)

	name := pool.Name(pool.Key{Profile: tp.Name, ProfileUID: tp.UID, Class: "cpu"})
	var j batchv1.Job
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: name}, &j))
	assert.Equal(t, int32(2), *j.Spec.Parallelism)
	assert.False(t, *j.Spec.Suspend)
	assert.Equal(t, tp.UID, j.OwnerReferences[0].UID)

	for _, tj := range []*transcodev1alpha1.TranscodeJob{a, b} {
		require.NoError(t, deliver(t, r, tj, task.StatusEvent{Kind: task.EventFinished, Attempt: 1,
			Outcome: task.OutcomeSucceeded, Result: &transcodev1alpha1.Result{}}))
		reconcileTJ(t, r, tj)
	}
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: name}, &j))
	assert.True(t, *j.Spec.Suspend, "no dispatched work: the pool suspends to zero")
	assert.Equal(t, int32(2), *j.Spec.Parallelism, "parallelism is never zeroed")
}

func TestDrainingPoolHoldsNewWorkThenReshapes(t *testing.T) {
	// Pool running with one job. Edit the profile's resources.limits.cpu, then create a second job.
	// A reconcile of the second job leaves it Planned ("draining" in its message).
	// Finish the first job: the pool suspends.
	// Clear the Job's status.startTime and active the way the Job controller would.
	// A reconcile reshapes the template and dispatches the held job.
}

func TestImageChangeRecreatesTheIdlePool(t *testing.T) {
	// A suspended pool with startTime nil. Change r.Pool.Image; reconcile any job of the profile.
	// The Job is deleted. The next reconcile with work creates it again with the new image.
}

func TestFailedPoolIsRecreatedWithBackoff(t *testing.T) {
	// Mark the pool Job Failed=True (status update). A reconcile deletes it and records a
	// Warning Event on the TranscodeProfile. A reconcile inside the backoff window creates nothing.
}
```

Write the three outlined tests out fully in the style of `TestPoolFollowsDispatch`. Each comment
is the exact sequence and the exact assertions.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./squasharr/controller/transcodejob/ -run 'TestPool|TestDraining|TestImageChange|TestFailedPool'`
Expected: FAIL, "jobs.batch … not found".

- [ ] **Step 3: Write `pools.go`**

```go
// pools applies every pool this pass needs, from what admission dispatched.
// It returns the pools that are draining so admission holds their new work.
func (r *Reconciler) pools(ctx context.Context, dispatched map[pool.Key]int32) (map[pool.Key]bool, error) {
	var jobs batchv1.JobList
	if err := r.reader().List(ctx, &jobs, client.InNamespace(r.Pool.Namespace),
		client.MatchingLabels{pool.LabelManagedBy: pool.ManagedByValue}, client.HasLabels{pool.LabelProfile}); err != nil {
		return nil, err
	}
	stored := map[string]*batchv1.Job{}
	for i := range jobs.Items {
		stored[jobs.Items[i].Name] = &jobs.Items[i]
	}
	keys := maps.Clone(dispatched)
	for _, j := range jobs.Items { // pools with no dispatched work still need their suspend
		if uid := ownerProfileUID(&j); uid != "" {
			keys[pool.Key{Profile: j.Labels[pool.LabelProfile], ProfileUID: uid,
				Class: transcodev1alpha1.Hardware(j.Labels[pool.LabelHardware])}] += 0
		}
	}
	draining := map[pool.Key]bool{}
	var errs []error
	for k, n := range keys {
		var tp transcodev1alpha1.TranscodeProfile
		if err := r.Client.Get(ctx, types.NamespacedName{Name: k.Profile}, &tp); err != nil {
			errs = append(errs, client.IgnoreNotFound(err)) // a deleted profile's pool goes with it (owner ref)
			continue
		}
		want := pool.Want(&tp, k.Class, r.Pool)
		cur := stored[pool.Name(k)]
		drift := pool.DriftNone
		if cur != nil {
			drift = pool.Classify(cur, want)
			if r.recreate[pool.Name(k)] {
				drift = pool.DriftRecreate
			}
			draining[k] = drift != pool.DriftNone
		}
		d, act := pool.Next(cur, n, drift)
		switch act {
		case pool.ActionDelete:
			errs = append(errs, r.deletePool(ctx, &tp, cur))
		case pool.ActionApply:
			if cur == nil && !r.poolBackoffOver(pool.Name(k)) {
				continue
			}
			ac, err := pool.Render(k, &tp, want, d, cur, r.Pool)
			if err == nil {
				_, err = k8s.Apply(ctx, r.Client, k8s.ManagerSquasharrPool, ac)
			}
			if pool.IsSchedulingImmutable(err) {
				r.recreate[pool.Name(k)] = true // gate enabled after this pool was made: drain and recreate
				draining[k], err = true, nil
			}
			errs = append(errs, err)
		}
	}
	return draining, errors.Join(errs...)
}
```

- **`deletePool`:**
  - Deletes with `client.PropagationPolicy(metav1.DeletePropagationBackground)` and clears
    `r.recreate[name]`.
  - For a Failed pool, it also records a Warning Event `PoolFailed` on the profile through
    `r.Recorder` and sets a backoff.
- **Backoff:** `r.poolBackoff[name]` is the next time a create is allowed. It starts at 1m, doubles
  up to 30m, and is kept in memory; a restart forgets it, which only shortens one wait.
  `poolBackoffOver` reads it.
- **`ownerProfileUID`:** returns the UID of the Job's controller owner reference of kind
  TranscodeProfile.
- **New `Reconciler` fields:** `recreate map[string]bool` and `poolBackoff map[string]time.Time`.
  Initialise them lazily in `admit`, which is safe because `MaxConcurrentReconciles` is 1.

In `admit`:
1. Before calling `Admit`, filter `queued` down to jobs whose `poolKeyFor(tp, r.classFor(tj, tp))`
   is not in the previous pass's `draining` set. Held jobs keep `Phase: Planned`, and their message becomes "waiting for
   pool X to drain before its profile change applies".
2. After dispatching, recount `dispatched` per `poolKeyFor(tp, st.Hardware)` over Queued and Running
   jobs, call
   `r.pools(ctx, dispatched)` and store its result as the next pass's `draining`.

In `SetupWithManager`, watch pool Jobs:

```go
		Watches(&batchv1.Job{},
			handler.EnqueueRequestsFromMapFunc(r.mapPoolToJobs),
			builder.WithPredicates(k8s.StatusFieldChanged(poolSignal))).
```

- `poolSignal` renders `suspend`, `active`, `startTime != nil`, failed conditions and the
  `template-hash` label.
- `mapPoolToJobs` lists non-terminal TranscodeJobs through the `indexProfileRef` field index,
  keyed by the Job's `LabelProfile`.

- [ ] **Step 4: Regenerate RBAC and copy it into the chart**

Run: `make manifests`. Then replace the text between the squasharr role's BEGIN and END sentinels
in `charts/clustarr/templates/rbac.yaml` with `config/rbac/squasharr_role.yaml`'s rules.

Run: `go test ./cmd/clustarr/ -run 'TestEveryGeneratedRoleMatchesItsMarkers|TestChartRBACMatchesTheGeneratedRoles|TestChartAndKustomizeAgreePerComponent'`
Expected: PASS. Run `helm dependency build charts/clustarr` first in a fresh worktree (CLAUDE.md).

- [ ] **Step 5: Run the squasharr suites**

Run: `go test ./squasharr/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add squasharr/controller/transcodejob config/rbac/squasharr_role.yaml charts/clustarr/templates/rbac.yaml
git commit -m 'feat(squasharr): pools follow dispatch: resume, scale up, suspend to zero, drain then reshape or recreate' -- squasharr/controller/transcodejob config/rbac/squasharr_role.yaml charts/clustarr/templates/rbac.yaml
```

---

### Task 12: Withdrawal: finalizer, cancelled lease, purge, sweep

**Files:**
- Modify: `squasharr/controller/transcodejob/dispatch.go`: add the finalizer before publishing.
- Create: `squasharr/controller/transcodejob/withdraw.go`.
- Modify: `squasharr/controller/transcodejob/controller.go`: handle deletion; handle
  `spec.suspend` on Queued and Running jobs; the `Admin events.StreamAdmin` field; a sweep every
  5m in `admit`.
- Modify: `squasharr/run.go`: set `Admin: bus` (natsbus implements `events.StreamAdmin`).
- Test: `squasharr/controller/transcodejob/withdraw_envtest_test.go`.

**Interfaces:**
- Consumes: `events.StreamAdmin` (Task 2), `k8s.EnsureFinalizer` and `k8s.RemoveFinalizer`
  (`pkg/k8s/finalizers.go:81,101`).
- Produces:
  ```go
  const FinalizerTaskWithdrawal = "squasharr.clustarr.io/task-withdrawal"
  const withdrawalTimeout = 10 * time.Minute
  func (r *Reconciler) withdraw(ctx context.Context, tj *transcodev1alpha1.TranscodeJob, tp *transcodev1alpha1.TranscodeProfile) error
  ```

- [ ] **Step 1: Write the failing envtests**

```go
func TestSuspendWithdrawsAQueuedTaskAndRedispatchesAsANewAttempt(t *testing.T) {
	// Dispatch a job (attempts 1). Set spec.suspend=true and reconcile:
	//   - the job is Planned with message "paused by spec.suspend";
	//   - the lease key holds {state: cancelled, attempt: 1};
	//   - StreamAdmin.Subjects under the pool filter lists nothing.
	// Set spec.suspend=false and reconcile: Queued, attempts 2, and the pulled task has Attempt 2.
}

func TestDeleteWithdrawsThenReleasesTheFinalizer(t *testing.T) {
	// Dispatch, then delete the job. Reconcile:
	//   - the lease is cancelled and the task purged;
	//   - the finalizer is removed and the object is gone.
}

func TestDeleteReleasesTheFinalizerAfterTheTimeoutWhenNATSIsDown(t *testing.T) {
	// Dispatch; swap r.Admin for one whose calls return events.ErrClosed; delete the job.
	// Reconcile with r.Now = deletionTimestamp + 5m: the finalizer stays.
	// Reconcile with r.Now = deletionTimestamp + 11m: the finalizer is gone, and a Warning Event names the timeout.
}

func TestSweepPurgesTasksOfDeletedJobs(t *testing.T) {
	// Publish a task for a UID with no TranscodeJob. Run admit with the sweep due: the subject is purged.
}
```

Write each one out fully. The comments give the exact sequence and assertions.

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./squasharr/controller/transcodejob/ -run 'TestSuspendWithdraws|TestDeleteWithdraws|TestDeleteReleases|TestSweep'`
Expected: FAIL.

- [ ] **Step 3: Write `withdraw.go`**

```go
// withdraw takes a dispatched job's task back (spec §8): a cancelled lease
// reaches a worker at its next renewal; the purge removes a task no worker
// has taken yet. Order matters: marker first, so a worker that fetched the
// task just before the purge finds it cancelled when it claims.
func (r *Reconciler) withdraw(ctx context.Context, tj *transcodev1alpha1.TranscodeJob, tp *transcodev1alpha1.TranscodeProfile) error {
	uid := string(tj.UID)
	b, _ := json.Marshal(task.Lease{Job: schema.Ref{Namespace: tj.Namespace, Name: tj.Name, UID: uid},
		Attempt: tj.Status.Attempts, State: task.LeaseCancelled, Since: r.now().UTC()})
	if _, err := r.Leases.Put(ctx, events.TranscodeLeaseKey(uid), b); err != nil {
		return fmt.Errorf("cancel lease: %w", err)
	}
	if tj.Status.Hardware == "" { // never dispatched
		return nil
	}
	return r.Admin.PurgeSubject(ctx, events.StreamWorkSquasharr,
		events.WorkTranscodeTaskSubject(string(tp.UID), string(tj.Status.Hardware), uid))
}
```

In `Reconcile`:
- **Before the terminal check:** if `tj.DeletionTimestamp != nil` and the finalizer is present:
  1. withdraw (the profile may be gone; then only cancel the lease);
  2. on success, or once `r.now().Sub(tj.DeletionTimestamp.Time) > withdrawalTimeout`, call
     `k8s.RemoveFinalizer`;
  3. on a timeout release, record a Warning Event "withdrawal timed out; the task may still run".

  Return in every case, and requeue after 30s while it keeps failing.
- **In `advance`:** a Queued or Running job with `spec.suspend=true` calls `withdraw`, then
  becomes Planned with message "paused by spec.suspend" and `WorkerPod` cleared. Admission
  already skips user-suspended Planned jobs.
- **In `dispatch`:** before publishing, call `k8s.EnsureFinalizer(ctx, r.Client, &tj, FinalizerTaskWithdrawal)`.
  For `attempt > 1`, delete a stale cancelled lease first:
  `_ = r.Leases.Delete(ctx, events.TranscodeLeaseKey(uid))`. The worker's attempt rule makes this
  an optimisation, not a requirement.
- **Terminal jobs:** in `afterWrite`, when a write makes the job terminal (a `finished` event, a
  block, or a plan-time skip), remove the finalizer, so a finished job deletes instantly.
- **Sweep, in `admit`:** when `r.now()` is past `r.nextSweep`, call
  `r.Admin.Subjects(ctx, events.StreamWorkSquasharr, "clustarr.work.transcode.task.>")`. Take each
  subject's last token (the escaped job UID), purge subjects whose UID matches no TranscodeJob in
  the list, and set `nextSweep = now + 5m`. Match by comparing against each live job's
  `events.WorkTranscodeTaskSubject(...)` suffix, so escaping is handled by the one builder.

- [ ] **Step 4: Run the tests**

Run: `go test ./squasharr/controller/transcodejob/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add squasharr/controller/transcodejob squasharr/run.go
git commit -m 'feat(squasharr): withdraw a dispatched task on suspend or delete, with a bounded finalizer and a sweep' -- squasharr/controller/transcodejob squasharr/run.go
```

---

### Task 13: GPU preferred, CPU fallback

`hardware: auto` jobs go to a GPU pool when a labelled GPU node and a free GPU slot exist, and to
CPU otherwise. A GPU failure, or a GPU pool that stays unschedulable, moves a job to CPU (spec
§18.5). "Sending a task to a GPU" means sending it to a pool whose Job was created with
`.spec.scheduling.schedulingConstraints.topology` set to that class's GPU label (Task 8).

**Files:**
- Create: `squasharr/controller/transcodejob/class.go`, `class_test.go` (pure),
  `capacity.go` and `class_envtest_test.go`.
- Modify: `squasharr/controller/transcodejob/dispatch.go`. `classFor` becomes capacity-aware for
  `auto`, and `dispatch` re-plans when the chosen class differs from the plan's.
- Modify: `squasharr/controller/transcodejob/controller.go`, so `admit` assigns classes in
  priority order before `Admit`.
- Modify: `squasharr/controller/transcodejob/pools.go`, for unschedulable GPU-pool detection and
  rerouting.
- Modify: `squasharr/controller/transcodejob/doc.go`. RBAC markers gain `nodes` (get, list, watch)
  and `pods` (list).
- Generated: `config/rbac/squasharr_role.yaml`, then copied into the chart's sentinel block.
- Modify: `squasharr/run.go` (`Options.NodeLabelNVIDIA` and `Options.NodeLabelIntel`, passed to
  `pool.Config`), and `cmd/clustarr/services.go` and `flags.go` (`--gpu-node-label-nvidia` and
  `--gpu-node-label-intel`, defaulting to `pool.DefaultNodeLabelNVIDIA` and
  `pool.DefaultNodeLabelIntel`).
- Modify: `cmd/clustarr/cli_test.go` (the flags parse into Options).

**Interfaces:**
- Consumes:
  - Task 8: `pool.Config.NodeLabel`, `pool.DefaultNodeLabel*`
  - Task 10: `classFor`, `dispatch`, `writeStatus`, `isAuto`
  - Task 11: `pools`
  - Task 12: `withdraw`
- Produces:
  ```go
  func ChooseClass(gpuNodes, unschedulable map[transcodev1alpha1.Hardware]bool,
  	free map[transcodev1alpha1.Hardware]int32, fallback bool) transcodev1alpha1.Hardware
  func (r *Reconciler) gpuNodes(ctx context.Context) (map[transcodev1alpha1.Hardware]bool, error)
  const unschedulableAfter = 10 * time.Minute
  const unschedulableFor = 30 * time.Minute
  ```

- [ ] **Step 1: Write the failing pure test**

`class_test.go`:

```go
func TestChooseClass(t *testing.T) {
	nv, in, cpu := transcodev1alpha1.HardwareNVIDIA, transcodev1alpha1.HardwareIntel, transcodev1alpha1.HardwareCPU
	both := map[transcodev1alpha1.Hardware]bool{nv: true, in: true}
	slots := map[transcodev1alpha1.Hardware]int32{nv: 1, in: 1, cpu: 2}
	for _, tc := range []struct {
		name          string
		nodes, unsched map[transcodev1alpha1.Hardware]bool
		free          map[transcodev1alpha1.Hardware]int32
		fallback      bool
		want          transcodev1alpha1.Hardware
	}{
		{"nvidia first", both, nil, slots, false, nv},
		{"intel when nvidia is full", both, nil, map[transcodev1alpha1.Hardware]int32{nv: 0, in: 1, cpu: 2}, false, in},
		{"cpu when every GPU slot is full", both, nil, map[transcodev1alpha1.Hardware]int32{cpu: 2}, false, cpu},
		{"cpu without a GPU node", nil, nil, slots, false, cpu},
		{"skip an unschedulable pool", both, map[transcodev1alpha1.Hardware]bool{nv: true}, slots, false, in},
		{"a fallback reason pins cpu", both, nil, slots, true, cpu},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, ChooseClass(tc.nodes, tc.unsched, tc.free, tc.fallback))
		})
	}
}
```

Run: `go test ./squasharr/controller/transcodejob/ -run TestChooseClass`
Expected: FAIL to compile, "undefined: ChooseClass".

- [ ] **Step 2: Write `class.go` and `capacity.go`**

```go
// ChooseClass picks an auto job's class (spec §18.5): the first GPU class, in
// priority order, with a labelled GPU node, a free slot and a schedulable
// pool; else cpu. A recorded fallback reason pins cpu.
func ChooseClass(gpuNodes, unschedulable map[transcodev1alpha1.Hardware]bool,
	free map[transcodev1alpha1.Hardware]int32, fallback bool,
) transcodev1alpha1.Hardware {
	if !fallback {
		for _, c := range []transcodev1alpha1.Hardware{transcodev1alpha1.HardwareNVIDIA, transcodev1alpha1.HardwareIntel} {
			if gpuNodes[c] && free[c] > 0 && !unschedulable[c] {
				return c
			}
		}
	}
	return transcodev1alpha1.HardwareCPU
}
```

```go
// gpuNodes reports which GPU classes have a Ready, schedulable node that
// carries the class's GPU label as "true" and allocatable GPUs: the labels
// the GPU operators set, confirmed by the device plugins that make the GPU
// requestable (spec §18.5).
func (r *Reconciler) gpuNodes(ctx context.Context) (map[transcodev1alpha1.Hardware]bool, error) {
	var nodes corev1.NodeList
	if err := r.Client.List(ctx, &nodes); err != nil {
		return nil, err
	}
	out := map[transcodev1alpha1.Hardware]bool{}
	for i := range nodes.Items {
		n := &nodes.Items[i]
		if n.Spec.Unschedulable || !nodeReady(n) {
			continue
		}
		for class, res := range pool.GPUResource { // what pool pods request: one map, no drift
			if n.Labels[r.Pool.NodeLabel(class)] != "true" {
				continue
			}
			if q, ok := n.Status.Allocatable[res]; ok && !q.IsZero() {
				out[class] = true
			}
		}
	}
	return out, nil
}

func nodeReady(n *corev1.Node) bool {
	for _, c := range n.Status.Conditions {
		if c.Type == corev1.NodeReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}
```

Run: `go test ./squasharr/controller/transcodejob/ -run TestChooseClass`
Expected: PASS.

- [ ] **Step 3: Assign classes in `admit` and re-plan in `dispatch`**

In `admit`:
1. Compute `gpu, _ := r.gpuNodes(ctx)`, logging a List error and treating it as no GPU nodes.
2. Compute `free[class] = r.Slots[class] - running[class]` from the running slots.
3. Sort the Planned candidates the way `Admit` orders them (priority descending, then `Created`,
   then `Key`).
4. For each candidate:
   - an `auto` job (`r.isAuto`) gets
     `class := ChooseClass(gpu, r.unschedulableFor(profile), free, tj.Status.FallbackReason != "")`
     and decrements `free[class]`;
   - a pinned job keeps `r.classFor(tj, tp)`, whose Task 10 behaviour is unchanged.
5. Pass the candidates, with those classes as `Slot.Hardware`, to `Admit`. `Admit` still enforces
   the budgets and `maxConcurrent`.

`r.unschedulableFor(profile string) map[Hardware]bool` reads the pools marked in Step 4.

In `dispatch`, when `class` differs from `hardwareForEncoder(tj.Status.Plan.Encoder)`, plan again
for `class` before building the task. Use the same code `plan` uses (`controller.go:315-334`),
factored into `planFor(tj, tp, mf, class) (*transcode.PlanResult, error)`: it calls
`worker.ProfileSpec(tp.Spec, &class)`, `pool.Threads(tp)` and `worker.OutputPath`.
- Put the new `statusPlan(result)` on the `tj` copy before `worker.BuildTask`, so the task
  carries the new `argsHash`.
- Write it in the Queued write (`st.Plan = …`).
- A skip or reject decision for the new class is written the way `plan` writes it, and nothing is
  published.

- [ ] **Step 4: Detect an unschedulable GPU pool in `pools`**

In `pools()`, for each running GPU-class pool (not suspended), list its pods through `r.reader()`:
`client.InNamespace(r.Pool.Namespace), client.MatchingLabels{"batch.kubernetes.io/job-name": name}`.
If any pod has `PodScheduled=False` with reason `Unschedulable`, and its `LastTransitionTime` is
more than `unschedulableAfter` ago:
1. Set `r.unschedulable[k] = r.now().Add(unschedulableFor)`. It is in memory; a restart forgets
   it and re-detects within 10 minutes.
2. For every TranscodeJob of that profile with `status.hardware == class` in phase **Queued**
   (dispatched, not claimed):
   - call `r.withdraw(ctx, tj, tp)`;
   - then `r.writeStatus`: Planned, `FallbackReason = "GPU pool <name> unschedulable for 10m"`,
     `WorkerPod = ""`, `NextAttemptAt = nil`, and a message naming the pool.
3. Record a Warning Event `PoolUnschedulable` on the profile.

The pool then has nothing dispatched and suspends through `Next`. The rerouted jobs dispatch to
CPU on the next pass, because `ChooseClass` sees the fallback reason.

- [ ] **Step 5: Flags, RBAC and wiring**

- Add `NodeLabelNVIDIA` and `NodeLabelIntel` to `squasharr.Options`.
- Add `--gpu-node-label-nvidia` and `--gpu-node-label-intel` to `newSquasharrCommand`, with
  defaults `pool.DefaultNodeLabelNVIDIA` and `pool.DefaultNodeLabelIntel` and a help text naming
  the GPU operator that sets each.
- `poolConfig` copies both into `pool.Config`.
- Extend `TestSquasharrManagerOptionsAndSlots` (`cli_test.go:374`) to parse both flags.
- In `squasharr/controller/transcodejob/doc.go`, add
  `// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch` and
  `// +kubebuilder:rbac:groups="",resources=pods,verbs=list`.
- Run `make manifests` and copy the role into the chart's sentinel block.

- [ ] **Step 6: Write the envtests**

`class_envtest_test.go`. Nodes are created directly. Their allocatable and Ready condition are set
with `c.Status().Update(…) //nolint:forbidigo // simulating the kubelet`:

```go
func gpuNode(t *testing.T, c client.Client, name, label string, gpus string) {
	t.Helper()
	n := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{label: "true"}}}
	require.NoError(t, c.Create(context.Background(), n))
	n.Status.Allocatable = corev1.ResourceList{"nvidia.com/gpu": resource.MustParse(gpus), corev1.ResourceCPU: resource.MustParse("8")}
	n.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}
	require.NoError(t, c.Status().Update(context.Background(), n)) //nolint:forbidigo // simulating the kubelet
}

func TestAutoGoesToTheGPUPoolWhenOneIsFree(t *testing.T) {
	// slots nvidia=1 cpu=2; gpuNode(nvidia.com/gpu.present, "1"); auto profile; one job.
	// Reconcile: Queued with status.hardware nvidia and plan.encoder hevc_nvenc.
	// The task is on the nvidia pool's subject, and the nvidia pool Job carries
	// schedulingConstraints.topology[0].key nvidia.com/gpu.present. The envtest runs without
	// WorkloadWithJob, so read the constraint from the applied-spec annotation (Task 8).
}

func TestAutoFallsBackToCPUWithoutAGPUNodeOrSlot(t *testing.T) {
	// (a) No GPU node: status.hardware cpu, encoder libx265.
	// (b) A GPU node, nvidia=1, two jobs: the first goes to nvidia and the second to cpu.
}

func TestAGPUEncodeFailureMovesAnAutoJobToCPU(t *testing.T) {
	// An auto job Queued on nvidia at attempt 1. Deliver finished{failed, GPUEncodeFailed}:
	// Planned, fallbackReason set, no nextAttemptAt.
	// Reconcile: Queued on cpu at attempt 2. It never returns to nvidia, even with a free slot.
}

func TestAPinnedGPUJobNeverFallsBack(t *testing.T) {
	// Profile hardware nvidia; deliver GPUEncodeFailed: Planned with nextAttemptAt (a retry), fallbackReason empty.
	// The next dispatch is nvidia again.
}

func TestAnUnschedulableGPUPoolReroutesItsQueuedJobs(t *testing.T) {
	// An auto job Queued on nvidia; its pool Job exists and is running.
	// Create a Pod labelled batch.kubernetes.io/job-name=<pool>, with status condition
	// PodScheduled=False, reason Unschedulable, LastTransitionTime 11m ago.
	// Reconcile: the job's lease holds a cancelled marker, the task subject is purged, and the job is
	// Planned with fallbackReason naming the pool. The next reconcile dispatches it to cpu.
}
```

Write each outlined test out fully, in the style of Task 10's envtests (`deliver`, `reconcileTJ`,
`getTJ`). The comments give the exact sequence and assertions.

- [ ] **Step 7: Run and commit**

Run: `go test ./squasharr/... ./cmd/clustarr/...`
Expected: PASS.

```bash
git add squasharr cmd/clustarr config/rbac/squasharr_role.yaml charts/clustarr/templates/rbac.yaml
git commit -m 'feat(squasharr): hardware auto prefers a labelled GPU pool with a free slot; GPU failure or an unschedulable pool falls back to CPU' -- squasharr cmd/clustarr config/rbac/squasharr_role.yaml charts/clustarr/templates/rbac.yaml
```

---

### Task 14: Retire the `squasharr-worker` identity

**Files:**
- `Makefile:30-49`:
  - delete `squasharr-worker` from `RBAC_ROLES`;
  - delete `RBAC_PATHS_squasharr-worker`;
  - rewrite the comment block (lines 15-41) so the only per-pod sub-identity is `grabarr-engine`.
- Delete: `config/rbac/squasharr_worker_role.yaml` and `config/rbac/squasharr_worker_role_binding.yaml`.
- Modify: `config/rbac/kustomization.yaml:32-36` and the comment at `config/rbac/role_binding.yaml:11-12`.
- Modify: `config/manager/squasharr.yaml`:
  - delete the `squasharr-worker` ServiceAccount (lines 16-23);
  - rewrite the header comment (lines 6-7) to say pools are Jobs squasharr manages;
  - rewrite the image comment (lines 106-109).
- Modify: `charts/clustarr/templates/rbac.yaml:896-969`: delete the worker's ServiceAccount,
  ClusterRole and binding block.
- Modify: `charts/clustarr/templates/deployments.yaml:134-139`: delete the
  `CLUSTARR_WORKER_SERVICE_ACCOUNT` entry.
- Modify: `squasharr/run.go`: delete `WorkerServiceAccount`, `DefaultWorkerServiceAccount` and its
  `Validate` check.
- Modify: `cmd/clustarr/services.go` and `flags.go`: delete `--worker-service-account` and
  `workerServiceAccountEnv`.
- Modify: `squasharr/worker/doc.go`:
  - replace the "Status" and "RBAC" sections (lines 129-157, including the five
    `+kubebuilder:rbac` markers) with a "Reporting" section: the worker writes a lease, a result
    and progress to three KV buckets, and squasharr turns them into status (spec §9, §17);
  - rewrite the package's first paragraph for the pool binary.
- Modify tests:
  - `cmd/clustarr/rbac_split_test.go`: delete the `squasharr-worker` pair at line 83, and lower
    the minimum identity count at line 91 by one.
  - Delete `TestTranscodeJobServiceAccountHoldsTheWorkerRole` from `cmd/clustarr/helpers_test.go`
    (moved there in Task 10), or wherever it ended up.
  - `cmd/clustarr/cli_test.go:374-445`: delete the service-account assertions.
  - `squasharr/observability_test.go:81-91`: drop `rel-squasharr-worker`.
  - `squasharr/controller/transcodejob/controller_envtest_test.go`: drop `ServiceAccountName`.
- Modify: `test/e2e/transcode_test.go:20-37`, the header comment on the worker's ServiceAccount and
  ClusterRole.

- [ ] **Step 1: Write the failing guard**

In `cmd/clustarr/rbac_split_test.go`:

```go
func TestNoInstallerShipsASquasharrWorkerIdentity(t *testing.T) {
	for name, docs := range map[string][]*unstructured.Unstructured{
		"kustomize": renderedKustomize(t), // the helper TestEachServiceAccountHoldsExactlyItsOwnRole uses
		"helm":      renderedHelm(t, "clustarr"),
	} {
		for _, d := range docs {
			if strings.Contains(d.GetName(), "squasharr-worker") {
				t.Errorf("%s still renders %s/%s: pool pods run with no ServiceAccount token", name, d.GetKind(), d.GetName())
			}
		}
	}
}
```

Use the two render helpers the existing RBAC tests use, keeping their exact names.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./cmd/clustarr/ -run TestNoInstallerShipsASquasharrWorkerIdentity`
Expected: FAIL, listing the ServiceAccount, ClusterRole and binding.

- [ ] **Step 3: Make the deletions listed under Files, then regenerate**

Run: `make manifests`
Expected: `config/rbac/squasharr_worker_role.yaml` is not recreated.

- [ ] **Step 4: Run the installer and RBAC suites**

Run: `helm dependency build charts/clustarr && go test ./cmd/clustarr/... ./squasharr/...`
Expected: PASS, including `TestChartAndKustomizeAgreePerComponent`, `TestEveryGeneratedRoleMatchesItsMarkers`,
`TestEachServiceAccountHoldsExactlyItsOwnRole` and the start envtest that runs every service under
its own role.

- [ ] **Step 5: Commit**

```bash
git add -A Makefile config/rbac config/manager/squasharr.yaml charts/clustarr/templates squasharr cmd/clustarr test/e2e/transcode_test.go
git commit -m 'refactor: retire the squasharr-worker ServiceAccount, role and flag; pool pods carry no token' -- Makefile config/rbac config/manager/squasharr.yaml charts/clustarr/templates squasharr cmd/clustarr test/e2e/transcode_test.go
```

---

### Task 15: The transcoder image; encoding libraries leave the media image

**Files:**
- Create: `images/Dockerfile.transcoder`.
- Delete: `images/Dockerfile.media-cuda`.
- Modify: `images/Dockerfile.media`. Delete the Intel QSV/VAAPI runtime: the header section, the
  `non-free` sed and grep, the `intel` packages and `LIBVA_MESSAGING_LEVEL`. Keep ffmpeg, ffprobe
  and par2; the scratch-image design decides their future. Rewrite the header's first paragraph:
  "encoding runtimes live in Dockerfile.transcoder".
- Modify: `images/Dockerfile.controller:5`: its comment should name `Dockerfile.transcoder`, not
  `media-cuda`.
- Modify: `Makefile`: add `TRANSCODER_IMG` and `TRANSCODER_CUDA_IMG`, and extend `docker-build`.
- Modify: `.github/workflows/release.yml`:
  - the header comment (lines 4-7);
  - the images matrix (lines 54-60): add a `target` key;
  - the build step (lines 101-114): `target: ${{ matrix.target }}`;
  - the merge matrix (lines 147-150).
- Modify: `hack/kind.sh`: load `TRANSCODER_IMG`, and enable the gates in the cluster config.
- Modify: `charts/clustarr/values.yaml` (lines 11-15 and 26-28), `values.schema.json:14,20`,
  `templates/_helpers.tpl:59`, `templates/deployments.yaml:135-136` and `charts/clustarr/README.md:227`:
  `image.mediaCuda` becomes `image.transcoder` and `image.transcoderCuda`.
- Modify: `config/manager/squasharr.yaml:110-113`: `CLUSTARR_WORKER_IMAGE` becomes
  `ghcr.io/mediactl/clustarr/transcoder:dev`, and `…_CUDA` becomes `…/transcoder-cuda:dev`.
- Modify tests:
  - `cmd/clustarr/cli_test.go:382-383,399` and `start_envtest_test.go:410`, where
    `media-cuda`/`media` images become `transcoder-cuda`/`transcoder`;
  - `grabarr/controller/downloadclient/workload.go:77`'s comment.
- Modify: `README.md:110`.

- [ ] **Step 1: Write the failing check**

In `cmd/clustarr/chart_images_test.go`, extend `TestChartImagesMatchConfig` with:

```go
func TestTranscoderImagesAreWhatSquasharrStampsOntoPools(t *testing.T) {
	values := readChartValues(t) // the helper TestChartImagesMatchConfig already uses
	assert.Equal(t, "mediactl/clustarr/transcoder", values.Image.Transcoder.Repository)
	assert.Equal(t, "mediactl/clustarr/transcoder-cuda", values.Image.TranscoderCuda.Repository)
	manifest := readFile(t, "../../config/manager/squasharr.yaml")
	assert.Contains(t, manifest, "ghcr.io/mediactl/clustarr/transcoder:dev")
	assert.Contains(t, manifest, "ghcr.io/mediactl/clustarr/transcoder-cuda:dev")
	assert.NotContains(t, manifest, "media-cuda")
	_, err := os.Stat("../../images/Dockerfile.media-cuda")
	assert.True(t, os.IsNotExist(err), "Dockerfile.media-cuda is replaced by Dockerfile.transcoder's transcoder-cuda target")
}
```

If the existing helpers have different names or shapes, adapt to them. Keep the five assertions.

Run: `go test ./cmd/clustarr/ -run TestTranscoderImages`
Expected: FAIL.

- [ ] **Step 2: Write `images/Dockerfile.transcoder`**

```dockerfile
# syntax=docker/dockerfile:1.7
#
# ghcr.io/mediactl/clustarr/transcoder[-cuda] -- the squasharr-worker binary
# and the only encoding runtime in the project (spec
# docs/superpowers/specs/2026-09-23-transcode-worker-design.md §10). Two
# targets from one file:
#
#   docker build -f images/Dockerfile.transcoder --target transcoder      -t ghcr.io/mediactl/clustarr/transcoder:dev .
#   docker build -f images/Dockerfile.transcoder --target transcoder-cuda -t ghcr.io/mediactl/clustarr/transcoder-cuda:dev .
#
# The worker is static (CGO_ENABLED=0) and holds no Kubernetes credentials:
# pool pods run with automountServiceAccountToken=false.
#
# * ffmpeg: BtbN GPL build, exactly as Dockerfile.media fetches it (see its
#   header for why not the distro package). FFMPEG_BRANCH / FFMPEG_URL /
#   FFMPEG_SHA256 as there.
#
# * Intel QSV/VAAPI runtime (transcoder, amd64 only): moved verbatim from
#   Dockerfile.media's gap-fix X10 section. <<Paste that header section here
#   unchanged: the libva gen-implib note, the oneVPL dispatcher note, the
#   package list and why non-free, the Comet Lake verification, and what the
#   operator still provides.>>
#
# * CUDA (transcoder-cuda): the nvidia/cuda -base- flavour, as
#   Dockerfile.media-cuda had it. Pools set runtimeClassName and request
#   nvidia.com/gpu (§6.4).

ARG GO_VERSION=1.27
ARG FFMPEG_BRANCH=9.0
ARG CUDA_BASE=nvidia/cuda:12.8.1-base-ubuntu24.04

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION} AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
WORKDIR /src
ENV CGO_ENABLED=0 GOFLAGS=-trimpath
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -ldflags "-s -w -X github.com/mediactl/clustarr/pkg/version.Version=${VERSION}" \
      -o /out/squasharr-worker ./cmd/squasharr-worker

FROM debian:bookworm-slim AS tools
ARG TARGETARCH
ARG FFMPEG_BRANCH
ARG FFMPEG_URL=""
ARG FFMPEG_SHA256=""
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl xz-utils \
 && rm -rf /var/lib/apt/lists/*
# <<The "ffmpeg + ffprobe" RUN from Dockerfile.media, byte for byte.>>

FROM debian:bookworm-slim AS transcoder
ARG TARGETARCH
LABEL org.opencontainers.image.source="https://github.com/mediactl/clustarr" \
      org.opencontainers.image.licenses="GPL-3.0-only" \
      org.opencontainers.image.title="clustarr-transcoder"
# <<The Intel runtime RUN from Dockerfile.media's final stage, byte for byte:
#   the non-free sed + grep, the intel package list on amd64, ca-certificates
#   tzdata libstdc++6, the uid/gid 1000 user, /data and /scratch owned by 1000.>>
COPY --from=tools /out/bin/ffmpeg /out/bin/ffprobe /usr/local/bin/
COPY --from=build /out/squasharr-worker /usr/local/bin/squasharr-worker
ENV LIBVA_MESSAGING_LEVEL=1
VOLUME ["/data"]
USER 1000:1000
ENTRYPOINT ["/usr/local/bin/squasharr-worker"]

FROM ${CUDA_BASE} AS transcoder-cuda
LABEL org.opencontainers.image.source="https://github.com/mediactl/clustarr" \
      org.opencontainers.image.licenses="GPL-3.0-only" \
      org.opencontainers.image.title="clustarr-transcoder-cuda"
# <<The user/dirs RUN from Dockerfile.media-cuda's final stage, byte for byte
#   (it replaces ubuntu:24.04's uid-1000 `ubuntu` user).>>
COPY --from=tools /out/bin/ffmpeg /out/bin/ffprobe /usr/local/bin/
COPY --from=build /out/squasharr-worker /usr/local/bin/squasharr-worker
ENV NVIDIA_VISIBLE_DEVICES=all \
    NVIDIA_DRIVER_CAPABILITIES=video,compute,utility
VOLUME ["/data"]
USER 1000:1000
ENTRYPOINT ["/usr/local/bin/squasharr-worker"]
```

Each `<<…>>` marks a block to copy verbatim from the named file **before** deleting or editing that
file. The copied text is the content; do not paraphrase it, because the verification notes are
records. The `tools` stage drops par2, which the worker never runs.

- [ ] **Step 3: Makefile, release matrix and kind**

Makefile:

```make
TRANSCODER_IMG ?= ghcr.io/mediactl/clustarr/transcoder:dev
TRANSCODER_CUDA_IMG ?= ghcr.io/mediactl/clustarr/transcoder-cuda:dev
...
docker-build: ## Build controller, media and transcoder images.
	docker build -f images/Dockerfile.controller -t $(IMG) .
	docker build -f images/Dockerfile.media -t $(MEDIA_IMG) .
	docker build -f images/Dockerfile.transcoder --target transcoder -t $(TRANSCODER_IMG) .

.PHONY: docker-build-cuda
docker-build-cuda: ## Build the CUDA transcoder image (amd64).
	docker build -f images/Dockerfile.transcoder --target transcoder-cuda -t $(TRANSCODER_CUDA_IMG) .
```

`release.yml`:
- In the images matrix, add `target: ""` to the existing rows.
- Replace the `media-cuda` row with:

```yaml
- { image: transcoder, suffix: /transcoder, dockerfile: images/Dockerfile.transcoder, target: transcoder, arch: amd64, runner: ubuntu-24.04 }
- { image: transcoder, suffix: /transcoder, dockerfile: images/Dockerfile.transcoder, target: transcoder, arch: arm64, runner: ubuntu-24.04-arm }
- { image: transcoder-cuda, suffix: /transcoder-cuda, dockerfile: images/Dockerfile.transcoder, target: transcoder-cuda, arch: amd64, runner: ubuntu-24.04 }
```

- In the build step's `with:`, add `target: ${{ matrix.target }}`.
- In the merge matrix, replace `media-cuda` with the rows `transcoder` and `transcoder-cuda`.
- Update the header comment to list both images.

`hack/kind.sh`:
- Add `TRANSCODER_IMG="${TRANSCODER_IMG:-ghcr.io/mediactl/clustarr/transcoder:dev}"` beside
  `MEDIA_IMG`, and to the `cmd_load` loop.
- In the kind config heredoc, add the following. All four gates are shared-registry names the
  1.37 apiserver lists, so every component accepts them. The scheduler's gang plugin rides
  `GenericWorkload` in 1.37.

```yaml
featureGates:
  WorkloadWithJob: true
  GenericWorkload: true
  TopologyAwareWorkloadScheduling: true   # honours a GPU pool's schedulingConstraints (spec §18.5)
runtimeConfig:
  "scheduling.k8s.io/v1alpha3": "true"
```

- [ ] **Step 4: Chart and manifests**

- In `values.yaml`, replace `mediaCuda` with:

```yaml
  transcoder:
    repository: mediactl/clustarr/transcoder
    tag: ""
  transcoderCuda:
    repository: mediactl/clustarr/transcoder-cuda
    tag: ""
```

  and rewrite the images comment (lines 11-15): four images, plus the transcoder pair used only
  by squasharr's pools.
- `values.schema.json`: `required` lists `transcoder` and `transcoderCuda`, not `mediaCuda`; add a
  `$ref: imageRef` for each.
- `deployments.yaml`: `CLUSTARR_WORKER_IMAGE` renders `(dict "root" $ "which" "transcoder")`, and
  `…_CUDA` renders `"transcoderCuda"`.
- `_helpers.tpl:59` and `README.md:227`: rename accordingly.

- [ ] **Step 5: Verify**

Run:
```bash
go test ./cmd/clustarr/... && helm template charts/clustarr >/dev/null
docker build -f images/Dockerfile.transcoder --target transcoder -t transcoder:check .
docker run --rm --entrypoint /usr/local/bin/ffmpeg transcoder:check -hide_banner -encoders | grep -E 'libx265|hevc_qsv|hevc_vaapi'
docker run --rm -e NATS_URL= transcoder:check; echo "exit=$?"
```
Expected:
- the tests pass;
- the grep prints the three encoders on amd64;
- the last command prints "squasharr-worker: $NATS_URL is required" and `exit=3`.

- [ ] **Step 6: Commit**

```bash
git add -A images Makefile .github/workflows/release.yml hack/kind.sh charts/clustarr config/manager/squasharr.yaml cmd/clustarr grabarr/controller/downloadclient/workload.go README.md
git commit -m 'build: Dockerfile.transcoder (transcoder, transcoder-cuda) carries the encoding runtime; media drops it' -- images Makefile .github/workflows/release.yml hack/kind.sh charts/clustarr config/manager/squasharr.yaml cmd/clustarr grabarr/controller/downloadclient/workload.go README.md
```

---

### Task 16: Remove the KEDA transcode example; rewrite scenario 12 for pools

**Files:**
- Delete: `config/keda/transcode-scaledjob.yaml`.
- Modify: `config/keda/kustomization.yaml` (header lines 7-8 and resources at line 17),
  `config/keda/README.md` (lines 5-6 and 21), `config/default/kustomization.yaml:11-12` and
  `config/prometheus/README.md:52`.
- Modify: `test/e2e/parity_test.go`:
  - delete `TestKEDATranscodeScaledJobRenders` (lines 110-156);
  - delete the `kustomizeBuild` helper if nothing else uses it (line 81);
  - delete the package-doc paragraph about it (lines 20-56).
- Modify: `test/e2e/transcode_test.go`, scenario 12 `TestTranscodeMediaFileThroughTranscodeJob`
  (line 531).

- [ ] **Step 1: Add the pool assertions to scenario 12**

After the "`waitForTranscodeJobPhaseAtLeast(Planned)`" step, add:

```go
	// §7: the job's task went to its profile's pool, which scaled up from zero.
	poolName := *waitForTranscodeJobField(t, tj, func(s transcodev1alpha1.TranscodeJobStatus) *string { return s.JobRef })
	var poolJob batchv1.Job
	require.Eventually(t, func() bool {
		return k8sClient.Get(ctx, types.NamespacedName{Namespace: clustarrNamespace, Name: poolName}, &poolJob) == nil &&
			!ptr.Deref(poolJob.Spec.Suspend, true)
	}, 2*time.Minute, 2*time.Second, "pool %s never resumed", poolName)
	assert.False(t, ptr.Deref(poolJob.Spec.Template.Spec.AutomountServiceAccountToken, true))
	running := waitForTranscodeJobPhaseAtLeast(t, tj, transcodev1alpha1.TranscodeJobPhaseRunning)
	assert.NotEmpty(t, running.Status.WorkerPod, "a running job names its worker pod")
```

After the `Succeeded` assertions:

```go
	require.Eventually(t, func() bool {
		return k8sClient.Get(ctx, types.NamespacedName{Namespace: clustarrNamespace, Name: poolName}, &poolJob) == nil &&
			ptr.Deref(poolJob.Spec.Suspend, false)
	}, 2*time.Minute, 2*time.Second, "pool %s did not suspend to zero after its only job finished", poolName)
```

`waitForTranscodeJobField` is a small generic poller beside `waitForTranscodeJobPhase` (line 380):
it polls `describeTranscodeJob` until the accessor returns non-nil, and times out with the job's
description. Use the package's existing namespace and client names.

- [ ] **Step 2: Verify**

Run: `kustomize build config/keda >/dev/null && go vet -tags e2e ./test/... && go test ./cmd/clustarr/ -run Keda`
Expected: PASS. The scenario itself stays deferred to Phase H by owner instruction; it is written,
not executed.

- [ ] **Step 3: Commit**

```bash
git add -A config/keda config/default/kustomization.yaml config/prometheus/README.md test/e2e
git commit -m 'test(e2e): scenario 12 follows the pool from zero and back; drop the KEDA transcode example' -- config/keda config/default/kustomization.yaml config/prometheus/README.md test/e2e
```

---

### Task 17: Documents: ADR-0009 accepted, design spec as built, CLAUDE.md

**Files:**
- `docs/adr/0009-transcode-worker-pools-over-jetstream.md`: Status becomes `Accepted, <date>`.
- `docs/adr/0005-transcodes-as-batch-jobs.md`: Status becomes `Superseded by ADR-0009, <date>`.
  The body is untouched.
- `docs/adr/README.md`: both index rows.
- `docs/superpowers/specs/2026-09-18-clustarr-design.md`:
  - §5 (lines 566-654): the stream row, the subjects row for tasks, and the two bucket rows. Say
    in one line that the pool consumer is per (profile, class) and outside `Default()`.
  - §6.4 (lines 672-676): rewrite as built. Pools per (profile, class), GPU pools'
    `schedulingConstraints`, `hardware: auto` with CPU fallback, dispatch, the lease protocol,
    the status stream and `squasharr-transcode-results`, the next-step table and Blocked, one CAS
    status writer, the two images, and the exit codes. Cite spec 2026-09-23 §17 and §18.
  - §12 (lines 873-876): pools replace the KEDA note.
  - §19 (line 946): the ADR-0005 line points at ADR-0009.
- `CLAUDE.md`:
  - The Transcoding section: unchanged.
  - The Phase E paragraph: add one sentence after "(The gap fixes reversed…)" saying that
    worker pools replaced per-job Jobs, with a pointer to the plan.
  - Invariants: under "One controller-writer per resource", note that `TranscodeJob.status` is
    squasharr's alone.
  - Gotchas: add two entries. The first:

    > **Two write paths under one manager need compare-and-swap.** squasharr writes
    > `TranscodeJob.status` from its reconciler and from its results consumer. Both go through
    > `writeStatus`/`patchCAS`, which carry the read `resourceVersion`, so a race is a Conflict and
    > is redone from a fresh read, not a silent rollback. Any new status writer must use the same
    > path.

    The second:

    > **A running Job's template is re-sent, not re-rendered.** An SSA manager that stops sending
    > a template field releases it, and a released field on a running Job is a rejected write. So
    > `squasharr/controller/pool.Render` re-sends the template recorded in
    > `squasharr.clustarr.io/applied-template`, and judges drift against that annotation rather
    > than the stored template, which carries apiserver defaults.
- `config/keda/README.md`, if Task 16 left anything citing the slot scheduler as an alternative.

- [ ] **Step 1: Make the edits**

For the edits above, follow `docs/adr/README.md` "To supersede an ADR" steps 1-3 exactly. Use
today's date.

- [ ] **Step 2: Check for stale references**

Run: `grep -rn 'media-cuda\|squasharr-worker ServiceAccount\|--role worker\|transcode-scaledjob\|ManagerSquasharrWorker\|WorkerFields' --include='*.go' --include='*.md' --include='*.yaml' . | grep -v '^./.superpowers\|^./docs/superpowers/plans\|^./docs/adr/0005\|^./docs/superpowers/specs/2026-09-23'`
Expected: no output, except `captionarr`/`indexarr` `*WorkerFields` identifiers, which are
unrelated packages.

- [ ] **Step 3: Final gate**

Run: `make generate manifests && git diff --exit-code -- api config && make lint && make test`
Expected: no generated drift, lint clean, and every suite passing. The envtest suites must take
seconds, not milliseconds.

- [ ] **Step 4: Commit**

```bash
git add docs/adr docs/superpowers/specs/2026-09-18-clustarr-design.md CLAUDE.md config/keda/README.md
git commit -m 'docs: ADR-0009 accepted, ADR-0005 superseded; spec §5/§6.4/§12/§19 as built' -- docs/adr docs/superpowers/specs/2026-09-18-clustarr-design.md CLAUDE.md config/keda/README.md
```
The design spec had uncommitted edits from another session when this plan was written. Before
committing, run `git diff docs/superpowers/specs/2026-09-18-clustarr-design.md` and confirm every
hunk is yours; commit that file only once its other owner has committed theirs.
