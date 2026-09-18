# ADR-0001: NATS JetStream is the event bus and the distributed work queue

**Status:** Accepted, 2026-09-18

## Context

Clustarr has two messaging shapes and one budget. The first shape is a durable, replayable
domain-event log — `MediaAdded`, `ReleaseGrabbed`, `DownloadCompleted`, `ImportCompleted`,
`TranscodeRequested`, `SubtitleMissing` — fanned out to several services, where a service added
later must be able to replay history to build its own view. The second shape is durable work:
indexer searches and RSS sync, grab attempts, subtitle fetches, metadata refreshes. Work items
need per-message acknowledgement, retry with backoff, dead-lettering, delayed delivery (the
delay-profile in ADR-0008 is literally "deliver this in 30 minutes"), and dedup so a double
publish does not become a double grab.

The budget is a homelab: three small nodes, not a Kafka cluster. Everything is Go, and the whole
thing has to install on kind with Helm.

## Decision

One NATS JetStream cluster carries both. Limits/Interest-retention streams
(`CLUSTARR_EVENTS`, `CLUSTARR_RELEASES`) carry the log; WorkQueue-retention streams
(`CLUSTARR_WORK_CATALOGARR`, `CLUSTARR_WORK_INDEXARR`, `CLUSTARR_WORK_CAPTIONARR`) carry work,
with priority tiers as subject partitions served by non-overlapping consumers. `CLUSTARR_DLQ`
holds terminal failures. KV buckets hold leases, dedup markers, pending-grab state and progress.
Indexer search is micro request/reply with a deadline, not a queue. Everything is reachable through `pkg/events.Bus`, which has a NATS
implementation and an in-memory one, both run against the same contract suite, so no service
imports `nats.go` directly.

## Alternatives considered

**Redis Streams (go-redis v9.22, asynq v0.26).** No server-side `AckWait`/`MaxDeliver`/`BackOff`/
DLQ: redelivery is a hand-rolled `XAUTOCLAIM` reclaimer over the PEL, and exactly-once-ish means
your own `SET NX` markers. asynq fixes the queue half but is a job queue only, so the event log
stays a second mechanism. Redis 8's tri-licence and the Valkey fork add drift risk.

**Kafka / Redpanda (franz-go v1.22).** ≥2 GiB per core per broker is out of budget on its own,
and work-queue semantics are weak by design: consumption is partition-bound, so concurrency is
capped by partition count, and the per-message ack that share groups provide is still Java-only in
practice. Retry topics are a workaround, not a feature.

**RabbitMQ.** Quorum queues are a genuinely good work queue with `delivery-limit` and DLX. The
problem is the other half: replayable pub/sub needs RabbitMQ Streams, which is a second protocol
and a second client, plus Erlang and cert-manager in the dependency set.

**Postgres / River v0.47 (+ CNPG).** The best Go job queue of the lot — `JobTimeout`,
`RescueStuckJobsAfter`, transactional enqueue, a usable UI. But it is not a bus: fan-out with
replay means an outbox and per-consumer tables. And the transactional-insert advantage evaporates
here, because etcd, not Postgres, is the system of record; there is no local transaction to join.

**Custom resources as the queue.** No ordering, no ack, no backoff, and etcd churn for
sub-minute tasks with a write amplification the apiserver should not be asked to absorb. Kept for
what it is good at: desired state, and a small number of human-visible long-running jobs.

## Consequences

JetStream gives `Ack`/`Nak(delay)`/`Term`/`InProgress`, `MaxDeliver` with per-attempt `BackOff`,
`Nats-Msg-Id` publish dedup, scheduled messages, KV with CAS/TTL/watch, request/reply, a
first-party Helm chart (nats 2.14.6), an operator (NACK 0.35.0) and a KEDA scaler, in roughly
100 MiB. The costs we accept: NATS has no built-in DLQ, so we build the advisory watcher;
WorkQueue retention is immutable per stream, so the topology has to be right at creation; the
dedup window is memory-resident, so `Duplicates` stays ≤ 10 m; and the KEDA NATS scaler bug
(#8165) forces a Prometheus-trigger workaround.

## Revisit triggers

Go Kafka clients ship GA share groups (per-message ack) and the footprint argument changes;
KEDA #8166 lands and the scaler workaround can be dropped; or a single service turns out to need
relational history and joins over it — that service gets River plus CNPG for itself, without
moving the bus.
