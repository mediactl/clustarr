# ADR-0005: Transcodes run as batch/v1 Jobs gated by suspend and slot budgets

**Status:** Accepted, 2026-09-18

## Context

A transcode breaks every assumption the rest of the system makes about work items. It runs for
minutes to a full day, needs a GPU on some nodes and a lot of CPU on others, produces gigabytes of
output, emits progress continuously, and fails in interesting ways — bad source streams,
out-of-space, a driver mismatch, an OOM at 90%. Users want to watch it, read its logs, kill it.

Clustarr already has a durable work queue (ADR-0001) with retries, backoff and dead-lettering, so
the obvious move is to make transcodes messages on it. The reason that is wrong is `AckWait`. A
consumer holding a message for 24 hours must heartbeat `InProgress` continuously; any pause,
restart or partition longer than the ack window causes redelivery, so a second worker starts the
same 24-hour encode while the first is still writing the same output path. That heartbeat is not a
strong enough guarantee to build on.

## Decision

Each transcode is a `batch/v1` Job, created by squasharr from a `TranscodeJob` custom resource
whose name includes a hash of the profile so that a profile change produces a distinct object
rather than mutating a running one. Jobs are created `suspend: true` and unsuspended only when a
slot budget allows, so admission is controlled by squasharr while scheduling, placement and
retries are controlled by Kubernetes.

The bus still carries the *request* — "this MediaFile wants transcoding" is a message — but the
execution is a Job. The queue does short work; the Job does long work.

## Alternatives considered

**Transcode as a JetStream work-queue message consumed by a long-lived worker.** Uniform with
every other workload and needs no Job plumbing. Rejected on the redelivery hazard above, and
because it would mean reimplementing GPU scheduling, node selection, backoff between attempts, log
retention and cancellation — all of which `batch/v1` already has.

**A bespoke worker pool with its own leases in NATS KV.** Solves duplication with a lease rather
than an ack window, but still leaves us writing a scheduler: GPU-aware placement, per-node
concurrency, failure classification. Kubernetes already is that scheduler.

**Chunked transcoding from the start** — split at scene-aligned keyframes, run an Indexed Job with
`completions=chunks` and `backoffLimitPerIndex`, concatenate and verify. Materially better for
wall-clock time on long files and for retrying only the failed chunk. Deferred, not rejected: it
requires RWX scratch space, seam verification to prove the concatenation is bit-correct at chunk
boundaries, and a success-policy-based concat Job. The shape is documented so the migration is
additive.

**Kueue for admission.** The right long-term answer for quota across job types, and listed as
deferred; suspend-plus-slot-budget needs no new operator dependency today.

## Consequences

We get GPU scheduling via resource requests and node selectors, `podFailurePolicy` to distinguish
a retryable pod eviction from a genuine encode failure, `kubectl logs` on a real pod, `ttlSecondsAfterFinished`
for cleanup, and cancellation by deleting an object. Progress reporting becomes our problem rather
than the queue's: the worker writes progress to the `TranscodeJob` status on an interval, which is
a status write budget we have to respect.

Slot budgeting is ours to get right — squasharr must not unsuspend more Jobs than the cluster can
run, or the scheduler queues them and the budget means nothing. And two execution models
(messages for short work, Jobs for long work) mean two failure-handling paths.

## Revisit triggers

Chunked transcoding moving into scope, which turns each transcode into an Indexed Job plus a
concat Job rather than a single Job; adoption of Kueue, which would replace the suspend/slot
budget with a proper queue and quota model; or a Job-level guarantee gap that forces admission
logic back into squasharr.
