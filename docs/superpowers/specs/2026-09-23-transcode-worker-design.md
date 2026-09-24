# Transcode worker pools: a NATS-only worker on per-profile pooled Jobs

**Status:** Proposed, 2026-09-23. **Supersedes on acceptance:** ADR-0005 (see
ADR-0009). **Changes:** design spec §5 (streams, buckets), §6.4 (squasharr),
§12 (scaling) and the `config/keda` overlay.

## 1. Goal

Transcoding moves out of the shared `media` image and out of Kubernetes'
reach of the worker:

1. A separate Dockerfile builds a separate worker binary, and only that image
   carries encoding libraries (ffmpeg with the Intel QSV/VAAPI runtime, and a
   CUDA variant).
2. The worker is a **pure consumer of the encoding work queue**: tasks arrive
   over NATS, progress and results leave over NATS, and it has no Kubernetes
   API access at all: no ServiceAccount token, no RBAC.
3. squasharr schedules the workers **directly**. There is no KEDA. Workers run
   in one long-lived `batch/v1` Job per TranscodeProfile (a *pool*), scaled by
   squasharr, suspended to zero when idle, re-shaped while suspended through
   Kubernetes 1.37's mutable scheduling directives and mutable pod resources,
   and gang-admitted through `spec.scheduling.schedulingPolicy.gang.minCount`
   where the cluster enables `WorkloadWithJob`.

Success: a TranscodeJob is planned, dispatched, encoded, verified, swapped and
recorded with the worker holding no Kubernetes credentials; a profile with no
work runs zero pods; `TranscodeJob.status` has exactly one writer.

## 2. Scope

**In:** `cmd/squasharr-worker`, `images/Dockerfile.transcoder`, the pool
lifecycle in squasharr, the NATS topology and schemas below, the API changes
in §11, the removals in §12, ADR-0009, and the spec sections named above.

**Out, each its own design:**

- **The scratch `media` image.** `images/Dockerfile.media` loses the Intel
  runtime here, and `Dockerfile.media-cuda` is deleted, but ffmpeg, ffprobe and
  par2 stay in it. Four paths outside squasharr still exec them: catalogarr's
  MediaFile controller and importarr's rescan and file import (`ffprobe`),
  captionarr's embedded subtitle provider (`ffmpeg`), and grabarr's usenet
  engine (`par2`, against node-local `/scratch`). Where they go decides whether
  the image can become a UPX-compressed binary on `scratch`. Verified here:
  the binary itself can go static with no code change. At `CGO_ENABLED=0`,
  anacrolix/torrent selects its pure-Go uTP (`utp_go.go`, `!cgo`) and bolt
  piece completion (`default-dir-piece-completion-boltdb.go`), and those two
  were the only reasons for cgo.
- **Artwork in a JetStream object store.** Nothing stores artwork today. The
  UI links `status.metadata.images` URLs directly
  (`ui/projection/library.go`).
- **The release index.** It stays SQLite FTS5. JetStream KV has no text
  search, secondary index or sort. Replacing it means an in-memory index
  rebuilt per pod from a stream, which is the alternative ADR-0003 rejected on
  memory. It does not block a static binary, because `modernc.org/sqlite` is
  pure Go.

## 3. Platform facts this design rests on

Verified against the envtest `kube-apiserver` 1.37.0 (`k8s.io/api` v0.37.0),
with `WorkloadWithJob=true,GenericWorkload=true` and
`runtime-config=scheduling.k8s.io/v1alpha3=true`, then again with the gate off.
The scheduler's gang behaviour is **not** verified. envtest runs no scheduler,
so that is Phase H's to prove on kind.

| Gate (1.37) | Stage | Default |
|---|---|---|
| `MutableSchedulingDirectivesForSuspendedJobs` | beta | on |
| `MutablePodResourcesForSuspendedJobs` | beta | on |
| `WorkloadWithJob` | alpha | off |
| `GenericWorkload` | beta | off |

"Not run" means the case was only tried with the gate on. The template rows depend on the two
beta gates, not on `WorkloadWithJob`, and Phase H's kind run covers them either way.

| Operation | Gate on | Gate off |
|---|---|---|
| Gang policy on a NonIndexed Job with `completions` unset | accepted | field dropped |
| `gang.minCount` < 1, or > `parallelism` (so `parallelism: 0` with a gang) | rejected | not run |
| `gang: {}` without `minCount` | stored as `{}`, not defaulted, does not follow `parallelism` | dropped |
| Server-side apply naming **only** `spec.scheduling.schedulingPolicy.gang.minCount`, on create, while suspended, while running | accepted; the manager owns exactly `f:minCount` | dropped silently, no error |
| A later apply that omits `minCount` once set | **rejected**: "field cannot be cleared once set" | accepted |
| Adding `minCount` to an existing Job that never had it | **rejected**: "field cannot be set once created" | dropped silently |
| Raising `parallelism`, changing `minCount`, while running | accepted | accepted (`minCount` dropped) |
| Resources, nodeSelector, tolerations, while suspended **and** `status.startTime` nil | accepted | not run |
| The same, while running or while suspended with `startTime` still set | rejected | not run |
| Container image or `runtimeClassName`, at any time | rejected | not run |

Consequences taken below. A pool's zero is `suspend: true`, never
`parallelism: 0`. The image and runtime class are fixed for a pool's life.
Template changes wait for the Job controller to clear `startTime` after a
suspend. `minCount` is sent on every apply and nothing else under
`.spec.scheduling` is ever set.

## 4. Architecture

```
TranscodeProfile ──owns──▶ Job squasharr-pool-<profile>   (suspended when idle)
       │                         │ pods: squasharr-worker (transcoder image)
       │ selects                 │   env NATS_URL, CLUSTARR_POOL_PROFILE_UID
       ▼                         ▼
TranscodeJob ──(squasharr publishes)──▶ clustarr.work.transcode.task.<profileUID>.<jobUID>
    ▲   status: sole writer squasharr                  │ consumer squasharr-transcode-<profileUID>
    │                                                  ▼
    ├── results consumer ◀── clustarr.work.transcode.result.<jobUID> ◀── worker
    ├── KV watch ◀── clustarr-progress   (transcode.<jobUID>, 1 Hz)  ◀── worker
    └── KV watch ◀── clustarr-transcode-leases (<jobUID>: held|cancelled) ◀─▶ worker
```

Admission is still squasharr's. The TranscodeJob controller's admission pass
publishes the highest-priority, oldest Planned jobs into free slots, the way
`Admit` unsuspends per-job Jobs today. It then sizes every pool from what it
dispatched. Slot accounting and pool sizing are one computation in one pass
(`MaxConcurrentReconciles` stays 1), so they can never disagree. The queue
only ever holds work squasharr has already admitted, so `spec.priority` keeps
its meaning despite JetStream's FIFO delivery.

## 5. NATS topology (pkg/events)

**Stream** `CLUSTARR_WORK_SQUASHARR`: subjects `clustarr.work.transcode.>`,
work-queue retention, file storage, `DiscardNew`, 64 MiB, duplicate window 1h.

| Subject | Payload | Msg-Id |
|---|---|---|
| `clustarr.work.transcode.task.<profileUID>.<jobUID>` | `schema.TranscodeTask` | `<jobUID>/<status.attempts>` |
| `clustarr.work.transcode.result.<jobUID>` | `schema.TranscodeResult` | `<jobUID>/<attempt>/<delivery>/<final>` |

Profile **UIDs**, not names, go in subjects and consumer names, because a
DNS-1123 name may contain `.`. The task Msg-Id carries the dispatch count
(`status.attempts`, incremented on every publish), so a job withdrawn and
re-dispatched inside the duplicate window is not absorbed as a duplicate. That
is the forced-subtitle-search class, avoided here up front.

**Consumers.**

| Durable | Filter | Tuning | Created by |
|---|---|---|---|
| `squasharr-transcode-<profileUID>` | `…task.<profileUID>.>` | AckWait 60s, heartbeat (InProgress) 20s, MaxAckPending 64, MaxDeliver 8, BackOff 1m/5m/15m/30m (the last repeats) | the pool reconciler, from `events.TranscodeTaskConsumer(profileUID)`; the worker subscribes with the same spec |

MaxDeliver has headroom because a nak for a lease that is still held (§9)
also counts as a delivery.
| `squasharr-transcode-results` | `…result.>` | standard | static topology; runs on the squasharr leader |

One function renders the per-profile `ConsumerSpec`, so controller and worker
cannot drift. The per-profile consumer is deleted with its pool.

**Buckets.**

- `clustarr-transcode-leases` (new): bucket TTL 90s, history 1. Expiry is
  judged by the server, so worker clock skew is irrelevant. Keys are
  `events.KVKeyToken(<jobUID>)`.
- `clustarr-progress` (existing): unchanged. `app/squash/worker.ProgressKey`.

**New bus operations**, in both `natsbus` and `membus` and under the contract
suite: ensure and delete a durable without subscribing, and purge one subject
of a stream.

## 6. Schemas (pkg/events/schema/transcode.go)

**`TranscodeTask` (`transcode.TranscodeTask.v1`)** is everything the worker
used to read from the apiserver, snapshotted at dispatch:

- `jobRef` (namespace, name, uid) and `attempt`;
- `profile`: name, `status.hash` (the `CLUSTARR_PROFILE=<name>@<hash>` tag)
  and the render spec `app/squash/worker.ProfileSpec`;
- `sourcePath`, `sourceProbeHash`, and `outputPath` resolved by
  `worker.OutputPath`;
- `rootFolder`: the one containing the source, with its recycle-bin setting.
  The controller resolves it; the worker still refuses any source or output
  outside it;
- `argsHash` from `status.plan` (a mismatch is logged, as today);
- `deadline`: the profile's `activeDeadline`, now enforced per task by the
  worker.

A snapshot is correct here, not merely convenient. A TranscodeJob's name
already pins its profile hash, and a profile change produces new
TranscodeJobs.

**`TranscodeResult` (`transcode.TranscodeResult.v1`):**

- `jobRef`, `attempt`, `delivery`, `pod`;
- `final` (bool) and `outcome`: `succeeded | skipped | failed | cancelled`;
- `reason`: for `failed`, one of `InvalidSource`, `VerifyFailed`,
  `DeadlineExceeded`, `RetriesExhausted`, or `Retriable` on a result that
  isn't final; for `skipped`, the plan's skip reason; for `cancelled`,
  `Cancelled`;
- the existing `Result` fields (output path, sizes, percent, mediaInfo);
- `stderrTail`, `startedAt`, `finishedAt`.

A result that isn't final reports a failed attempt that will be retried.

**Lease value** (JSON): `{state: held|cancelled, attempt, pod, node, since}`.
A `cancelled` lease applies only to its own `attempt` and earlier ones. A
claimant for a later attempt treats it as absent and replaces it by revision,
so a job withdrawn and re-dispatched within the lease TTL cannot find its own
new task cancelled. The controller also deletes the old marker before it
re-dispatches, but correctness does not depend on that.

## 7. The pool

**Identity.** One Job per TranscodeProfile, in squasharr's namespace, named
`squasharr-pool-<profile>`. When that would exceed 63 characters, the name is
truncated and given an 8-character hash of the profile UID. Owner reference:
the (cluster-scoped) TranscodeProfile. Labels:
`app.kubernetes.io/component=squasharr-worker`,
`transcode.clustarr.io/profile=<name>`, and
`squasharr.clustarr.io/template-hash=<hash of the immutable part>`.

**Spec.**

| Field | Value |
|---|---|
| `completionMode` / `completions` | NonIndexed / unset: the work-queue pattern |
| `parallelism` | ≥ 1 always; see sizing |
| `spec.scheduling.schedulingPolicy.gang.minCount` | = `parallelism`, on **every** apply; nothing else under `.spec.scheduling` |
| `suspend` | true when the pool has no dispatched work |
| `podReplacementPolicy` | `Failed` |
| `backoffLimit` | 6 (worker-level failures only; task failures never fail a pod) |
| `podFailurePolicy` | Ignore `DisruptionTarget`; Ignore exits `ExitDrained` and 137 (OOM-killed; final fix round, I1); FailJob on exit `ExitMisconfigured` |
| `activeDeadlineSeconds`, `ttlSecondsAfterFinished` | unset: a pool never finishes by design |

**Template.** Rendered from the profile and squasharr's flags, reusing today's
`job.go` pod shape: the securityContext held to the Deployments', `/data`
from `--data-claim`, `/scratch` and `/tmp` emptyDirs, UMASK, tracing flags,
`CLUSTARR_CPU_LIMIT` from the Downward API, `POD_NAME`, `NVIDIA_*` for nvidia,
and `--intel-render-groups` as supplementalGroups for intel. It adds
`automountServiceAccountToken: false`, `NATS_URL` and
`CLUSTARR_POOL_PROFILE_UID`, and has no ServiceAccount.

- **Immutable part** (hashed into `template-hash`): the image
  (`--worker-image` or `--worker-image-cuda`, by hardware class),
  `runtimeClassName`, securityContext, volumes including the scratch
  `sizeLimit`, env and args.
- **Mutable part**: container resources, nodeSelector and tolerations.

**Field manager.** `squasharr-pool`. One renderer produces the complete
declaration for every apply (CLAUDE.md: a manager that applies `spec` must
send every field it owns). While the pool is running, the renderer copies the
*stored* mutable template values rather than the desired ones, so an apply
never asks for a change the apiserver would reject.

**Sizing.** In the admission pass, per profile:

```
dispatched = count(TranscodeJobs of this profile in Queued or Running)
parallelism = min(dispatched, profile.maxConcurrent (0: no cap), the class's --slots left)
```

`dispatched` never exceeds that bound, because admission published within
it. So `parallelism = dispatched`, floored at 1 for the apiserver.

| Pool state | Condition | Action (one apply each) |
|---|---|---|
| suspended | `dispatched > 0`, no drift pending | `suspend: false`, `parallelism = minCount = dispatched`, with the desired mutable template (legal in the same update, because the stored Job is suspended with `startTime` nil; envtest proves it, and the fallback is a template apply then a resume apply) |
| running | `dispatched > parallelism` | raise `parallelism = minCount` |
| running | `dispatched == 0` | `suspend: true` (the workers are idle; see §9 for the race) |
| running | `0 < dispatched < parallelism` | nothing: v1 shrinks only to zero, and idle workers wait for the queue to drain |

**Drift.** A mutable change (profile resources, nodeSelector, tolerations)
sets the profile **draining**: admission publishes nothing new for it, the
pool finishes its dispatched work and suspends, the controller waits for
`startTime` to be nil, then applies the new template. An immutable change
(`template-hash` differs: a new image tag, a hardware class, a scratch size)
drains the same way, then deletes and recreates the Job. The consumer is keyed
by profile UID and survives.

**Gate changes.** Nothing to configure. On a cluster without
`WorkloadWithJob` the apiserver drops `minCount` and pods schedule one by one.
A cluster that enables the gate later rejects the apply against older pools
with "field cannot be set once created". The reconciler reads exactly that
error as immutable drift: drain, delete, recreate.

**A Failed pool** (backoffLimit spent on worker-level failures) is recreated
after an exponential backoff, and a Warning Event is recorded on the
TranscodeProfile. Its dispatched tasks stay queued or are naked back by the
dying pods.

## 8. TranscodeJob controller

**Phases** are unchanged in name. `Verifying` is never set under pools (the
worker verifies inside Running); it stays in the enum as legacy (final fix
round, D5).

| Phase | Meaning now |
|---|---|
| Planned | unchanged |
| Queued | the task is published |
| Running | a lease is held (from the lease watch); a lease that expires or is released without a final result returns the job to Queued |
| Succeeded / Failed / Skipped | a final result arrived, or (Failed, reason `DeadLettered`) the DLQ projector's `clustarr.io/dead-lettered` annotation appeared |

**Dispatch.** Order is ensure consumer, then publish, then status
`Queued` + `attempts++` + `jobRef = <pool>`. If NATS is down, requeue: nothing
is marked Queued that was not published.

**Status.** squasharr is the **sole writer** of `TranscodeJob.status`, under
`k8s.ManagerSquasharr`. It takes each field from one source:

- `progress`: the `clustarr-progress` watch, throttled to `ProgressInterval`;
- `result`, `stderrTail`, `message`, `finishedAt`: the results consumer
  (idempotent; the first final result wins, and a later one for a terminal job
  is acked and ignored);
- `workerPod`, `startedAt`: the lease watch.

Every path goes through one renderer seeded from the live object, after
`reassertKnownStatus`.

**Withdrawal.** `spec.suspend: true`, or deletion, does two things:

1. Put a `cancelled` lease. A worker holding the task sees its next renewal
   fail on revision, reads `cancelled`, stops ffmpeg, removes the `.part` and
   acks.
2. Purge the task subject, which removes a task no worker has taken yet.

Deletion runs through a finalizer, `squasharr.clustarr.io/task-withdrawal`,
that is removed once both steps succeed **or after 10 minutes**, so an outage
cannot pin an object (the R-6 pattern). A periodic sweep purges task subjects
and leases with no live TranscodeJob as a backstop.

## 9. The worker (`cmd/squasharr-worker`)

**Process.** Read the environment, connect to NATS (`natsbus`), subscribe to
`events.TranscodeTaskConsumer(CLUSTARR_POOL_PROFILE_UID)` with one message in
flight, and serve until SIGTERM. It **never exits 0**: in a work-queue Job one
successful pod ends the pool.

| Exit | Meaning | podFailurePolicy |
|---|---|---|
| 2 `ExitRetriable` | worker-level: NATS unreachable, ffmpeg missing or unusable | counts toward `backoffLimit` |
| 3 `ExitMisconfigured` | bad or missing environment | FailJob |
| 10 `ExitDrained` | SIGTERM (suspend, drain, eviction, pool deletion) | Ignore |

**Per task.** The handler reuses `app/squash/worker`'s sequence (re-probe
against `sourceProbeHash`, plan, free space, encode, verify, swap), fed by the
task instead of the apiserver. Its steps:

1. **Claim:** `Create` the lease `{held}`.
   - The key exists and is `held`: another worker has the task (a redelivery
     during a partition). Nak after 30s.
   - The key exists and is `cancelled` for this attempt or a later one: ack
     and drop. A `cancelled` lease for an earlier attempt is replaced by
     revision and the claim proceeds (§6).
2. **Encode** with renewals every 20s (lease `Update` by revision, and
   InProgress). If no renewal has succeeded for 60s, the worker
   **self-fences**: it stops ffmpeg and removes the `.part` before the 90s TTL
   could let another worker claim. The worker times its 60s from when the
   server acknowledged the renewal, which is always later than the server's
   own timestamp, so it stops at least 30s early.
3. **Settle.** The result is published and acknowledged by JetStream before
   the task is settled:

   | Outcome | Result | Task |
   |---|---|---|
   | succeeded, skipped | final | Ack |
   | InvalidSource, VerifyFailed, DeadlineExceeded | final, `failed` | Ack (the result carries the failure; nothing is dead-lettered) |
   | retriable | not final | Nak with the consumer's backoff |
   | retriable on delivery `MaxDeliver` | final, `failed` | Ack |
   | cancelled | final, `cancelled` | Ack |
   | undecodable task | none | Term (dead-lettered) |

4. **Release:** delete the lease by revision.

**SIGTERM.** While encoding: stop ffmpeg, remove the `.part`, release the
lease, Nak with no delay, exit 10. After the result is published: finish the
ack, then exit 10. If the ack is lost anyway, the redelivered task finds the
`CLUSTARR_PROFILE` tag, re-publishes its result and acks. The crash matrix in
`app/squash/worker/doc.go` is unchanged, because the swap order is unchanged.

**Idempotence.** The existing tag checks (`producedEarlier`,
`finishElsewhere`) are what make redelivery safe after a swap. They stay.

**Guard.** A test runs `go list -deps ./cmd/squasharr-worker` and fails on
`sigs.k8s.io/controller-runtime`, `k8s.io/client-go` or `pkg/k8s`.
`k8s.io/api` and `k8s.io/apimachinery` types are allowed.

## 10. Images and build

`images/Dockerfile.transcoder`:

- `build` stage: `CGO_ENABLED=0 go build ./cmd/squasharr-worker`.
- `tools` stage: ffmpeg and ffprobe from BtbN, moved verbatim from
  `Dockerfile.media`, with its verification notes.
- Two final targets:
  - `transcoder`: debian bookworm-slim, plus the Intel QSV/VAAPI runtime on
    amd64 and `LIBVA_MESSAGING_LEVEL=1`, moved verbatim from
    `Dockerfile.media`;
  - `transcoder-cuda`: `nvidia/cuda` base, with `NVIDIA_*`, moved from
    `Dockerfile.media-cuda`.
- Both run as uid/gid 1000.
- Neither contains par2.

**Images:** `ghcr.io/mediactl/clustarr/transcoder[-cuda]`. `--worker-image`
and `--worker-image-cuda` keep their names and point at them.

**`images/Dockerfile.media`** loses the Intel runtime and `LIBVA_*`, and keeps
ffmpeg, ffprobe and par2 until the scratch-image design (§2).
**`Dockerfile.media-cuda`** is deleted.

**Also updated:** the Makefile (`TRANSCODER_IMG`, `TRANSCODER_CUDA_IMG`,
`docker-build`), `.github/workflows/{ci,release}.yml`, the chart values and
`chart_images_test`, and `hack/kind.sh`. kind loads the transcoder image, and
its cluster config enables `WorkloadWithJob` and `GenericWorkload` (plus the
scheduler's gang gates) and `scheduling.k8s.io/v1alpha3`, for Phase H.

## 11. API changes

- `TranscodeJobStatus.workerPod` (string, optional, new): the pod holding the
  task, for `kubectl logs`.
- Changed meaning, same fields:
  - `jobRef` names the pool Job;
  - `attempts` counts dispatches;
  - `stderrTail`, `progress` and `result` are now written by squasharr.
- `TranscodeProfileSpec.ttlSecondsAfterFinished` no longer applies: pools
  never finish. It is documented as ignored, and deleted at the next API
  version.
- `activeDeadline` becomes the per-task deadline, enforced by the worker.

## 12. Removed

- `squasharr --role worker`, `squasharr.Options.Validate`'s `--job` path, and
  `--worker-service-account`.
- `app/squash/worker`'s client code and RBAC markers.
- `config/rbac/squasharr_worker_role.yaml` and the `squasharr-worker`
  ServiceAccount, Role and binding, in config and chart.
- `k8s.ManagerSquasharrWorker`, `app/squash/status.WorkerFields`, and `Patch`'s
  worker branch.
- The per-TranscodeJob Job path (`ensureJob`, `setSuspend`, and `job.go`'s
  per-task Job; the pod-shape helpers move to the pool renderer).
- `config/keda/transcode-scaledjob.yaml`, `TestKEDATranscodeScaledJobRenders`,
  and the README row. captionarr's ScaledObject is unrelated and stays.
- Tests that assert removed things are deleted or moved:
  - `TestSquasharrWorkerRoleMatchesTheWorkerMarkers` is deleted;
  - `TestSquasharrWorkerExitCodeReachesTheProcess` moves to
    `cmd/squasharr-worker`, over the new exit codes;
  - the RBAC-enforced start envtest's `squasharr-worker` identity is removed;
  - the registration guard's worker role is removed.

## 13. Failure handling

| Failure | Handling |
|---|---|
| NATS down at dispatch | requeue; nothing marked Queued |
| Worker partitioned mid-encode | self-fences at 60s; the task is redelivered after AckWait; the new claimant naks until the lease expires at 90s |
| Worker pod killed (OOM, node loss) | the lease expires, then redelivery; the Job replaces the pod only after the old one has fully terminated |
| Task fails verify or invalid source | final `failed` result, Ack; TranscodeJob Failed with the reason |
| Retriable failure | Nak with backoff; the final delivery is reported `failed`, reason `RetriesExhausted` |
| Worker dies on the last delivery (no result) | the existing MAX_DELIVERIES watcher dead-letters the task; the DLQ projector annotates the TranscodeJob; the controller marks it Failed, reason `DeadLettered` |
| Crash between swap and result | the retry finds the tag, re-publishes the result, acks |
| Result consumer behind or down | results are durable in the stream; the status catches up |
| TranscodeJob deleted | cancelled lease plus purge, then the finalizer is removed (≤ 10 min); the sweep is the backstop |
| Pool Failed | recreated with backoff; Warning Event on the profile |
| Gate off | `minCount` dropped by the apiserver; pods schedule one by one |
| Gate enabled later | "cannot be set once created" → drain and recreate |
| Suspend races a new dispatch | the worker naks (SIGTERM path), and the next pass resumes the pool |

## 14. Testing

- **Pure:** the pool renderer and sizing table (§7) as table tests, including
  running-state renders copying stored mutable values, `minCount = parallelism
  ≥ 1`, and drift classification.
- **Pool envtest against the real apiserver**, in two environments, with
  gates on and off. The Job controller does not run under envtest, so tests
  set `status.startTime` themselves, as the spike did. Each row of §3, through
  the real renderer, including:
  - resume together with the template change in one apply;
  - omitting `minCount` being impossible;
  - "cannot be set once created" read as drift.

  Plus field ownership: `managedFields` shows `squasharr-pool` owning
  `f:minCount` and nothing under `.spec.scheduling` besides it.
- **Contract (real embedded NATS and `membus`):**
  - the per-profile consumer and new bus operations;
  - one lease holder at a time;
  - self-fencing when renewals stop;
  - a redelivered task naked while its lease lives;
  - a cancelled lease stopping a running handler;
  - SIGTERM leading to a nak;
  - every row of the §9 settle table.
- **Worker:** embedded NATS and real ffmpeg (skips without it), end to end
  from a published task to a final result and the swapped file. Also: the
  never-exits-0 guard, the import guard, and the exit-code test through the
  real `main`.
- **Parity:** a task rendered by the controller's own renderer from real
  CRs, planned by the worker, gives an argv whose hash equals
  `status.plan.argsHash`. The fixture comes from the real producer
  (CLAUDE.md).
- **Controller envtest:**
  - dispatch order;
  - no Queued without a publish;
  - status from progress, lease and results;
  - a `managedFields` assertion that no manager other than `squasharr` owns
    any `TranscodeJob.status` leaf, against an object already in steady state;
  - withdrawal and the finalizer timeout.
- **Phase H (kind, gates on):**
  - scenario 12 rewritten for pools;
  - a pool scales from zero and back;
  - a profile edit re-shapes a suspended pool;
  - gang admission actually holds a burst until `minCount` pods fit.

## 15. Documents

- **ADR-0009** is Proposed now. On implementation it becomes Accepted, and
  ADR-0005 becomes "Superseded by ADR-0009" in the same commit, per
  `docs/adr/README.md`.
- **Design spec:** §5 (stream, consumers, buckets), §6.4 (rewritten as built),
  §12 (pools replace the KEDA note), §19 (ADR summary).
- **Other:** `config/keda/README.md`, `app/squash/worker/doc.go`, `README.md`
  (images), and CLAUDE.md's Phase E paragraph and squasharr invariants.

## 16. Deferred, named

- Shrinking a running pool below its dispatched maximum. v1 shrinks only to
  zero. A later version could lower `parallelism` and rely on the Job
  controller deleting not-ready pods first, with worker readiness meaning
  "busy". That needs kind to prove the ordering.
- Chunked transcoding (ruling R-1), unchanged.
- The scratch `media` image, artwork in the object store, and a JetStream
  release index (§2).

## 17. Errata from planning (2026-09-23)

Planning against the code found six places where this spec, as approved,
would not work. Where a section above disagrees with this list, **this list
wins.**

1. **Results travel in a KV bucket, not a stream.** A results consumer inside
   squasharr would be a second code path writing `TranscodeJob.status` under
   the `squasharr` manager, beside the reconciler. That is CLAUDE.md's
   lost-update and field-release hazard. So:
   - The worker `Put`s its `Result` to a new bucket
     `clustarr-transcode-results` (TTL 7 days, history 1) under
     `result.<KVKeyToken(jobUID)>`, before it settles the task.
   - The reconciler reads it with `Get` and is the only code that writes
     status. It applies a result only when `result.attempt ==
     status.attempts`, and deletes the key after a terminal apply.
   - The stream carries tasks only. There is no `…result.>` subject and no
     `squasharr-transcode-results` consumer.
2. **One pool per profile *and hardware class*.** A job's class comes from
   its plan's encoder (`hardwareForEncoder`): a remux takes a CPU slot today,
   and `spec.hardware` overrides per job. So one profile's jobs can need two
   classes, and one pool image cannot run a GPU task on a CPU node. The key
   is (profile, class) throughout:
   - Job `squasharr-pool-<profile>-<class>`;
   - subject `clustarr.work.transcode.task.<profileUID>.<class>.<jobUID>`;
   - consumer `squasharr-transcode-<profileUID>-<class>`;
   - pool env `CLUSTARR_POOL_CLASS` beside `CLUSTARR_POOL_PROFILE_UID`.
3. **The worker sends `InProgress` itself.** `natsbus` never extends an ack.
   `Subscription.Heartbeat` is the broker's idle heartbeat
   (`app/indexer/worker/rss/worker.go:57`) and is not used here. The worker's
   renewal loop sends `InProgress` every 20s beside the lease `Update`.
4. **Two optional bus interfaces, not new `Bus` methods,** so no existing
   fake breaks:
   - `events.PullSubscriber.Pull` returns a `Puller` whose `Next` fetches
     exactly one message when called. A worker never prefetches a second task
     that would outlive its ack window.
   - `events.StreamAdmin` has `DeleteSubscription(stream, durable)` (the
     durable and its dead-letter watcher) and `PurgeSubject`, and -- since the
     final fix round (M1) -- `Subscriptions(stream)`, which squasharr's sweep
     reads to delete the durables of profiles that no longer exist.
   - Both are implemented by `natsbus` and `membus`, under the contract
     suite.
   - Dispatch does not pre-create the consumer: work-queue retention keeps a
     task until a consumer exists, and the worker's `Pull` creates it.
5. **The task, result and lease types live in `app/squash/task`, not
   `pkg/events/schema`.** The schema package imports nothing from `api/`,
   and the task carries `TranscodeProfileSpec`.
   - `worker.BuildTask` is the one builder. The controller dispatches with
     it, so a source under no RootFolder now fails at dispatch as
     `InvalidSource` rather than in a pod.
   - Lease and result values carry the job's `schema.Ref`, so a KV watch
     entry maps back to its TranscodeJob.
   - `schema.TranscodeProgress` gains `bitrateKbps` (additive), so
     `status.progress` loses nothing now that the controller renders it from
     telemetry.
6. **Status sources.** The reconciler reads lease, result and progress with
   `Get`, and requeues a Queued or Running job every 10s
   (`worker.DefaultProgressInterval`). Watches on the lease and result
   buckets (a `source.Func` started with the controller) only wake it
   sooner. There is no in-memory state that a restart could lose.

## 18. Scope additions (2026-09-23, second round)

The owner added three requirements after §17. This section supersedes
§17.1 and §17.6. The rest of §17 stands.

### 18.1 The worker reports on a stream

- **Where events go.** The worker publishes `transcode.StatusEvent.v1`
  events on `clustarr.work.transcode.result.<jobUID>`, in
  `CLUSTARR_WORK_SQUASHARR`. The Msg-Id is
  `<jobUID>/<attempt>/<delivery>/<seq>`, where `seq` counts from 1 within one
  delivery.
- **The three kinds:**
  - `claimed` carries the pod and node.
  - `progress` is shaped like `status.progress` and published at most every
    `ProgressInterval` (10s). The 1 Hz UI telemetry in `clustarr-progress` is
    unchanged.
  - `finished` carries the outcome, reason, message, result and stderr tail.
- **Settling.** The worker acks its task only after JetStream has stored
  `finished`, for every outcome. The queue retries nothing itself except a
  crashed, drained or fenced delivery, which is redelivered for the same
  attempt.
- **Worker reasons:** `InvalidSource`, `SourceChanged` (the live file's probe
  hash no longer matches, so it is not the planned file), `VerifyFailed`,
  `DeadlineExceeded`, `Retriable`, `GPUUnavailable` (ffmpeg lacks the GPU
  encoder the plan wants), `GPUEncodeFailed` (ffmpeg failed while encoding on
  a GPU tier), and `Cancelled`.
- **What is removed.** The results KV bucket from §17.1 goes away. Leases stay
  in KV, because fencing needs compare-and-swap, and they are not status.

### 18.2 squasharr consumes `squasharr-transcode-results`

- **The consumer.** A durable in `Default()`, filtering
  `clustarr.work.transcode.result.>`, with AckWait 30s, MaxDeliver 10,
  BackOff 5s/30s/2m and MaxAckPending 1. It is subscribed by a runnable that
  runs only on the leader. It acks an event only after the status write the
  event causes has landed.
- **One write path.** The consumer and the reconciler both write
  `TranscodeJob.status` under `k8s.ManagerSquasharr`, through one function:
  1. read the object fresh, through the uncached reader;
  2. change a copy of its status;
  3. apply it with the read `resourceVersion` as a precondition. This is the
     compare-and-swap pattern in `app/catalog/worker/grab/kindops.go:181`.

  A write that races the other path fails with Conflict and is redone from a
  fresh read. It is never silently rolled back.
- **Stale and duplicate events are ignored.** An event whose `attempt` is not
  `status.attempts`, or that reaches a job that is terminal or not dispatched,
  is acked and changes nothing.
- **What the controller no longer reads.** It reads no lease and no progress
  from KV. `claimed` and `progress` events move a job to Running and set
  `workerPod`, `startedAt` and `progress`.

### 18.3 squasharr decides the next step

| `finished` | squasharr does |
|---|---|
| succeeded / skipped | Succeeded / Skipped |
| cancelled | nothing (the withdrawal already acted) |
| `GPUUnavailable`, `GPUEncodeFailed`, on an `auto` job running on a GPU class | set `status.fallbackReason`; back to Planned with no delay; the next dispatch is CPU (§18.5) |
| `Retriable`, or a GPU reason on a job pinned to a GPU class | while `attempts` < 5: back to Planned with `status.nextAttemptAt` = now + 1m, 5m, 15m, then 30m for each later attempt; after that, block with reason `RetriesExhausted` |
| `SourceChanged` | Failed, reason `SourceChanged`, **not** blocked: the TranscodeProfile controller deletes a Failed/SourceChanged job whose `spec.sourceProbeHash` differs from its MediaFile's current probe hash, and creates it again for the new file |
| `InvalidSource`, `VerifyFailed`, `DeadlineExceeded` | block |
| (a dead-lettered task, §13) | block, reason `DeadLettered` |

Admission skips a Planned job until its `nextAttemptAt` has passed.

### 18.4 Blocked

- **What a block leaves.** A blocked job is phase **Failed** plus a
  `Blocked=True` condition carrying the reason and message. There is no new
  phase.
- **Why that is enough.** The job's fixed name stops the TranscodeProfile
  controller from creating it again.
- **Retrying.** To retry, delete the TranscodeJob; the profile creates it
  again. A status condition is controller-owned and has no user edit path, so
  deletion is the supported retry.

### 18.5 GPU preferred, CPU fallback

- **GPU node labels.** A GPU node is a Ready, schedulable node that has both:
  - the label `nvidia.com/gpu.present=true` or
    `intel.feature.node.kubernetes.io/gpu=true`, overridable with
    `--gpu-node-label-nvidia` and `--gpu-node-label-intel`;
  - allocatable `nvidia.com/gpu` or `gpu.intel.com/i915` above 0.

- **What "send a task to a GPU" means.** The task goes to its profile's pool
  for that GPU class. That pool Job is **created** with
  `.spec.scheduling.schedulingConstraints.topology: [{key: <the class's GPU
  node label>}]`, which holds every pod in the pool to the label's `true`
  domain. A CPU pool has no constraint.
- **Facts from the 1.37 apiserver** (gates on; with `WorkloadWithJob` off the
  whole `.spec.scheduling` is silently dropped):
  - the constraint is accepted at create;
  - changing it, adding it later, or leaving it out of a later apply is
    rejected as immutable, even while the Job is suspended.

  So every pool apply re-sends the constraint the Job was created with. A
  changed label flag is immutable drift: the pool is recreated once idle
  (§7).
- **What stays on the pod.** Pool pods keep the GPU resource request, because
  only a device-plugin request gives a pod the device. They also keep required
  node affinity on the same label, which is the placement on clusters without
  `WorkloadWithJob`. Both use the configured label keys, replacing the
  constants in `job.go`.
- **kind for Phase H.** The cluster also enables
  `TopologyAwareWorkloadScheduling`, the 1.37 alpha gate the scheduler needs
  to honour topology constraints.
- **`hardware: auto`.** The value `auto` is added to `Hardware`, on the
  profile and on `TranscodeJob.spec.hardware`, and becomes the profile's CRD
  default.
- **Choosing a class at dispatch.** For an `auto` job, the class is chosen in
  priority order, `nvidia` then `intel` then `cpu`. A GPU class is eligible
  when all four hold:
  - a GPU node of that class exists;
  - the class has a free slot, counting this pass's own dispatches;
  - the job has no `fallbackReason`;
  - that (profile, class) pool is not marked unschedulable.
- **Planning for the chosen class.** Dispatch plans for the chosen class and
  writes `status.plan` and `status.hardware` in the same write.
- **Pinned classes.** `cpu`, `nvidia` and `intel` stay pinned and never fall
  back.
- **An unschedulable GPU pool.** If any pod of a GPU pool has been
  `PodScheduled=False, reason=Unschedulable` for more than 10 minutes, that
  pool is marked unschedulable for 30 minutes. Its dispatched jobs that are
  not yet claimed (Queued) are then withdrawn (§8). Each gets
  `fallbackReason` = "GPU pool unschedulable" and is dispatched again, which
  sends it to CPU.

### 18.6 API additions

- **`TranscodeJobStatus`:**
  - `workerPod` (§11);
  - `hardware`: the class of the current attempt;
  - `fallbackReason`, MaxLength 256;
  - `nextAttemptAt`.
- **Condition type `Blocked`.** A TranscodeJob now has seven condition types,
  within `MaxItems=8`.
- **`Hardware`.** The enum gains `auto`, and `TranscodeProfileSpec.hardware`
  defaults to `auto`.
- **RBAC.** squasharr gains `nodes: get,list,watch`, `pods: list` and
  `transcodejobs: delete`.
