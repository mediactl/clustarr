# ADR-0009: Transcodes run on per-profile worker-pool Jobs fed by a JetStream work queue

**Status:** Accepted, 2026-09-24

## Context

ADR-0005 made each transcode its own `batch/v1` Job. squasharr created each Job suspended and
unsuspended it against a slot budget. The worker in that Job pod read its TranscodeJob, profile,
MediaFile and RootFolders from the apiserver and wrote its own progress and result to
`TranscodeJob.status`. ADR-0005 rejected a queue-fed worker because a message held for hours
outlives any ack window: a pause longer than `AckWait` redelivers the message, and a second
worker then encodes the same file into the same output path.

Four things changed:

- **The owner set new requirements.** Encoding libraries leave the shared `media` image for an
  image of their own. The worker is a pure consumer of the encoding queue with no Kubernetes API
  access. squasharr schedules the workers directly, without KEDA. A Job per queue item is not
  wanted.
- **Kubernetes 1.37 lets a controller reshape a suspended Job** (`MutableSchedulingDirectivesForSuspendedJobs`
  and `MutablePodResourcesForSuspendedJobs`, both beta and on by default). The apiserver allows
  resources, nodeSelector and tolerations to change once `status.startTime` is nil. It never
  allows the image or `runtimeClassName` to change.
- **Jobs gained gang admission through `spec.scheduling`**, behind the alpha `WorkloadWithJob`
  gate. `schedulingPolicy.gang.minCount` is the one mutable member. It must be at least 1 and at
  most `parallelism`, and the apiserver silently drops it when the gate is off.
- **KEDA's native NATS scaler counts only undelivered messages**, which is why
  `config/keda/transcode-scaledjob.yaml` never became more than an example.

The facts above were verified against the 1.37 apiserver (see the design,
`docs/superpowers/specs/2026-09-23-transcode-worker-design.md` §3). The scheduler's gang
behaviour was not, and is Phase H's to prove.

## Decision

- **One long-lived Job per TranscodeProfile** (a pool): NonIndexed with `completions` unset, and
  pods that run `squasharr-worker` from a dedicated `transcoder` or `transcoder-cuda` image.
- **squasharr still admits the work.** It publishes admitted TranscodeJobs as tasks on
  `CLUSTARR_WORK_SQUASHARR`, one durable consumer per profile, and sets each pool's
  `parallelism`, and `gang.minCount` to the same value, from what it dispatched.
- **A pool with no work is suspended.** A Job with a gang policy can never reach
  `parallelism: 0`, so suspension is how a pool reaches zero.
- **A profile change is applied only while the pool is suspended**, after the pool drains. A
  change to anything the apiserver never allows to change recreates the Job.
- **The worker talks only to NATS.** It claims each task with a server-expired lease in
  `clustarr-transcode-leases`, renews the lease while it encodes, and stops itself before the
  lease can expire. It reports progress and results over NATS. squasharr is the only writer of
  `TranscodeJob.status`.

The redelivery hazard ADR-0005 named is closed by the lease, not the ack window. A redelivered
task whose lease is still live goes back on the queue. A worker that cannot renew its lease stops
at least 30 seconds before the lease expires.

## Alternatives considered

**Keep one Job per TranscodeJob, with the worker made NATS-only.** Approved in the first design
round. Its strength: the Job's own guarantees prevent duplicates, because a replacement pod starts
only after the old one has stopped. Rejected by the owner: a Job per item is unnecessary, and it
cannot use the suspend-and-reshape and gang capabilities that act on a Job's pods as a group.

**A KEDA ScaledJob.** Dropped. It adds an operator. Its NATS trigger sees only undelivered
messages, so it cannot size a pool of in-flight work. And KEDA cannot hand an identical pod a
particular task, so the worker would still need its own dispatch.

**A Deployment of workers.** No suspend, no gang admission, no `podFailurePolicy`, and
scale-to-zero only through HPA or KEDA.

**Hold the message with InProgress heartbeats and no lease.** This is the hazard ADR-0005 rejected
queues for: a partition longer than `AckWait` puts two encoders on one output.

**Let the gang default its own `minCount`.** A stored `gang: {}` is not defaulted and does not
follow `parallelism`, so squasharr sets `minCount` explicitly on every apply, and sets nothing
else under `.spec.scheduling`.

## Consequences

**Gains:**

- The worker holds no Kubernetes credentials, and its image is the only one with encoding
  libraries.
- `TranscodeJob.status` gets one writer, and the `squasharr-worker` field manager, role and
  ServiceAccount go away.
- An idle profile runs zero pods.
- A burst is admitted all at once where the cluster enables `WorkloadWithJob`, and is scheduled
  pod by pod where it doesn't, with no configuration either way.

**Costs:**

- Duplicate prevention now depends on the lease protocol and on self-fencing timings, not on
  Kubernetes.
- In v1 a pool shrinks only to zero: once its queue thins, idle workers hold their CPU or GPU
  until the queue drains.
- A profile edit waits for its pool to drain, and new work for that profile is held until then.
- Task failures no longer fail pods: retries are the queue's (`MaxDeliver` and backoff), and the
  task deadline is the worker's.
- `kubectl logs` for a task goes through `status.workerPod` rather than the Job's name.
- Gang admission depends on an alpha gate.

## Revisit triggers

- `WorkloadWithJob` graduates, changes shape or is withdrawn.
- Holding idle workers during partial drains becomes costly enough to need partial scale-down,
  which needs proof on kind that the Job controller deletes idle pods first.
- Chunked transcoding comes into scope. It needs Indexed pools plus a concat stage.
- The lease protocol is ever observed to let two workers encode one task.
