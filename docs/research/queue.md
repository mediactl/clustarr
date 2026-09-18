# Clustarr research note: event bus + distributed work queue

Date: 2026-09-18. Scope: choose the transport for (a) domain events (MediaAdded, ReleaseGrabbed, DownloadCompleted, ImportCompleted, TranscodeRequested, ...) and (b) durable work (transcode jobs, download tasks, subtitle fetches, indexer searches) in a Kubernetes-native Go media stack, homelab scale first.

Everything marked **[verified]** was checked with `go doc`, `go list -m -versions`, `gh release list`, raw GitHub source, or vendor docs during this session. Unmarked claims are engineering judgement.

---

## 0. TL;DR

**Recommendation: NATS JetStream for both the event bus and the work queues, Kubernetes CRDs as the source of truth for desired state and for human-visible long-running jobs, KEDA for scaling workers on JetStream consumer lag.** No Redis, no Kafka, no Postgres queue.

Why in one paragraph: JetStream is the only candidate that natively gives per-message `Ack/Nak(delay)/Term/InProgress`, `MaxDeliver` + `BackOff`, server-side publish dedup (`Nats-Msg-Id`), a durable event log (Limits/Interest retention) *and* a true work queue (WorkQueue retention) in one 20-50 MB-RSS binary with a first-party Helm chart, a first-party Kubernetes operator (NACK, CRDs `jetstream.nats.io/v1beta2`) and a first-party KEDA scaler. It also ships KV (leases, dedup markers, progress) and an object store. Kafka/Redpanda are heavy for a homelab and Go clients still lack share-group (per-message ack) support; Redis Streams need hand-rolled PEL/XAUTOCLAIM retry logic and asynq is a job queue only; Postgres/River is an excellent job queue but not an event bus; CRDs-as-queue is the wrong tool for high-churn short tasks (etcd, no ack semantics), but the right tool for desired state and for a small number of long-lived jobs.

---

## 1. Workload profile (from the brief)

| Workload | Kind | Volume (homelab) | Duration | Needs |
|---|---|---|---|---|
| Domain events (MediaAdded, ReleaseGrabbed, DownloadCompleted, ImportCompleted, TranscodeRequested, SubtitleMissing...) | pub/sub, fan-out to N services, replayable | 10s-1000s/day, bursts on library scans | n/a | durable log, multiple independent consumers, replay for new services, ordering per entity |
| Transcode job | work queue | 1-100/day, burst 1000s on a re-encode campaign | minutes-hours per job | exactly-one-worker, long ack window / heartbeat, retry with backoff, dead-letter, priority tiers, progress reporting, scale 0..N workers |
| Download task (usenet/torrent) | work queue + long-lived state | 1-100/day | minutes-days | one owner at a time (lease), resumable, progress, cancel |
| Subtitle fetch | work queue | 100s/day | seconds | retry with backoff, provider rate limits, dedup |
| Indexer search | scatter/gather request-reply, and cached results | 10s-1000s/day, RSS sync every 15 min | 1-30 s | fan-out to N indexers, timeouts, partial results, rate limits; *not* a durable queue |
| Metadata refresh | work queue, periodic | 1000s/day on scans | seconds | dedup, scheduled/delayed, low priority |

Design constraints that fall out: one system must do both durable pub/sub and durable work queue well; per-message ack with retry and dead-letter is mandatory (transcodes fail in interesting ways); long-running jobs need "still working" heartbeats; homelab footprint matters (12 CPU / 62 GB dev box, but target clusters may be 3 small nodes); Go client quality matters (all services are Go); the whole thing must be installable with Helm on kind.

---

## 2. Candidates

### 2.1 NATS JetStream

**Versions [verified]:** nats-server v2.15.0 (released 2026-09-17), v2.14.7 (2026-09-15); nats.go v1.53.1 (2026-08-11); Helm chart `nats` 2.14.6 (appVersion 2.14.6, 2026-08-28); NACK controller v0.24.0 (chart `nack` 0.35.0, 2026-08-19); jsm.go v0.5.0; KEDA v2.20.2 scaler `nats-jetstream`.

**Server-side semantics [verified via nats-server source/docs]:**
- Retention: `LimitsPolicy` (keep until MaxMsgs/MaxBytes/MaxAge), `InterestPolicy` (delete once every consumer whose filter covers it has acked; zero consumers means immediate drop; slow consumer holds everything), `WorkQueuePolicy` (delete on first ack). Limits (MaxMsgs/MaxBytes/MaxAge) still apply as a backstop under Interest/WorkQueue.
- WorkQueue constraints (server error strings): "workqueue stream requires explicit ack", "multiple non-filtered consumers not allowed on workqueue stream", "filtered consumer not unique on workqueue stream" (filters must not overlap), "consumer must be deliver all on workqueue stream". Retention cannot be changed to/from WorkQueue after creation.
- Ack model per consumer: `AckExplicit`; `AckWait` (server default 30s); `MaxDeliver` (default -1 unlimited); `BackOff []time.Duration` ("max deliver is required to be > length of backoff values"; **BackOff applies only to AckWait timeouts, not to NAK'd messages** - for NAK use `NakWithDelay`); `MaxAckPending` (default 1000); `InactiveThreshold`; `FilterSubjects []string`; `PauseUntil`; priority groups (`PriorityPolicyOverflow`, `PriorityPolicyPrioritized`, pinned client) - note these prioritise *pull requests/clients*, not messages.
- Redelivery: NAK puts message back (optionally with delay), Term stops redelivery regardless of MaxDeliver and emits advisory `$JS.EVENT.ADVISORY.CONSUMER.MSG_TERMINATED.<stream>.<consumer>`, InProgress resets AckWait. When MaxDeliver is exceeded, the server publishes `$JS.EVENT.ADVISORY.CONSUMER.MAX_DELIVERIES.<stream>.<consumer>` and stops redelivering; **there is no built-in DLQ** - the message remains in the stream (on a WorkQueue stream it just sits unacked until limits purge it). We must build the DLQ (design in section 5.5).
- Exactly-once building blocks: publisher dedup via `Nats-Msg-Id` inside the stream's `Duplicates` window (default 2m); `msg.DoubleAck(ctx)` waits for the server to confirm the ack. Combine with idempotent consumers keyed by message ID.
- Extras: `Nats-Expected-Last-Subject-Sequence` optimistic concurrency, per-message TTL (`AllowMsgTTL`), subject transforms, `RePublish`, `AllowDirect`, delayed/scheduled publish (`Nats-Schedule` header with `@at <RFC3339>`, `@every`, cron; needs `AllowMsgSchedules` and server >= 2.12 / 2.14 for recurring), atomic batch publish (2.12+), consumer `PauseUntil` (2.11+). 2.15 defaults streams to a 1000-consumer cap.
- KV store (`KeyValueConfig{Bucket, History<=64, TTL, MaxBytes, Replicas}`; `Create` fails if key exists; `Update(key, val, revision)` CAS; `Watch/WatchAll` with a nil marker after initial values). Object store (`ObjectStoreConfig`, chunked, `Put(io.Reader)`, `Watch`).

**Go client API (nats.go v1.53.1 `github.com/nats-io/nats.go/jetstream`) [verified with `go doc`]:**
```go
js, err := jetstream.New(nc)                                   // JetStream interface
s,  err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{...})
c,  err := s.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{...}) // also js.CreateOrUpdateConsumer(ctx, stream, cfg)
oc, err := s.OrderedConsumer(ctx, jetstream.OrderedConsumerConfig{FilterSubjects: ...})
ack, err := js.Publish(ctx, subj, data, jetstream.WithMsgID(id), jetstream.WithExpectStream("CLUSTARR_EVENTS"))
fut, err := js.PublishAsync(subj, data, ...); <-js.PublishAsyncComplete()
cc, err := c.Consume(func(m jetstream.Msg){...}, jetstream.PullMaxMessages(8), jetstream.PullExpiry(30*time.Second),
              jetstream.PullHeartbeat(5*time.Second), jetstream.ConsumeErrHandler(func(cc jetstream.ConsumeContext, err error){...}))
cc.Stop(); cc.Drain(); <-cc.Closed()
it, err := c.Messages(jetstream.PullMaxMessages(1)); m, err := it.Next(); it.Stop()
b,  err := c.Fetch(10, jetstream.FetchMaxWait(5*time.Second)); for m := range b.Messages() {...}; b.Error()
m.Ack(); m.DoubleAck(ctx); m.Nak(); m.NakWithDelay(d); m.InProgress(); m.Term(); m.TermWithReason("poison")
md, _ := m.Metadata()   // Sequence{Stream,Consumer}, NumDelivered, NumPending, Timestamp, Stream, Consumer
kv, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{Bucket:"clustarr-leases", TTL: 10*time.Minute})
rev, err := kv.Create(ctx, key, val); rev, err = kv.Update(ctx, key, val, rev); w, _ := kv.Watch(ctx, "job.>"); for e := range w.Updates() {...}
s.PauseConsumer(ctx, name, until); s.ResumeConsumer(ctx, name); s.GetMsg(ctx, seq); s.DeleteMsg(ctx, seq); s.Purge(ctx, jetstream.WithPurgeSubject(...))
```
Option types are plain types: `PullMaxMessages int`, `PullMaxBytes int`, `PullExpiry time.Duration`, `PullHeartbeat time.Duration`, `PullThresholdMessages int`, `StopAfter int`, `PullPriorityGroup string`, `PullMinPending int`, `PullMinAckPending int`; `ConsumeErrHandler` wraps `func(ConsumeContext, error)`. `ErrConsumerDeleted` is terminal for `Consume`; `ErrNoHeartbeat` is recoverable (client re-pulls). Fetch options: `FetchMaxWait`, `FetchHeartbeat`, `FetchPriorityGroup`, `FetchMinPending`, `FetchMinAckPending`, `FetchPrioritized`. `micro` package exists for request/reply services (useful for indexer scatter/gather).

**Kubernetes ops [verified]:**
- Helm chart `nats/nats` 2.14.6: keys `config.cluster.enabled`, `config.cluster.replicas` (3), `config.jetstream.enabled`, `config.jetstream.fileStore.pvc.enabled/size` (10Gi default) / `storageClassName`, `config.jetstream.memoryStore.enabled/maxSize`, `config.monitor.enabled/port` (8222), `reloader`, `natsBox`, `promExporter` (prometheus-nats-exporter sidecar, metrics `jetstream_consumer_num_pending`, `jetstream_consumer_num_ack_pending`, `jetstream_consumer_num_redelivered`, `jetstream_consumer_num_waiting`, `jetstream_stream_total_messages`, labels include `stream_name`, `consumer_name`). No default resources set; homelab examples run 3 replicas at 100m/128Mi requests, 500m/512Mi limits; NATS docs recommend 2 CPU / 8Gi for "production" JetStream.
- NACK (`jetstream.nats.io/v1beta2` kinds `Stream`, `Consumer`, `KeyValue`, `ObjectStore`, `Account`): install `helm upgrade --install nack nats/nack --set jetstream.nats.url=nats://nats.nats.svc:4222 --set 'jetstream.additionalArgs={--control-loop}'`. KeyValue/ObjectStore/Account only reconcile in `--control-loop` mode. Resources are exclusively owned by NACK (drift is corrected). Released cadence 2026: v0.22.4 (Mar), v0.23.0 (May, NATS 2.14 fields), v0.24.0 (Aug). Alternative: create streams from Go with `CreateOrUpdateStream` at startup (idempotent); we can do both (NACK in cluster, Go for tests/kind).
- KEDA `nats-jetstream` scaler (v2.20.2): metadata `natsServerMonitoringEndpoint` (host:8222), `account` ("$G"), `accountID`, `stream`, `consumer`, `lagThreshold` (default 10), `activationLagThreshold` (0), `useHttps`. Lag = `num_pending + num_ack_pending` from `/jsz`; supports ScaledObject and ScaledJob; errors if consumer missing. **Open bug #8165 (2026-09-10): with nats-server >= 2.12.7 the scaler omits `accounts=true` on `/jsz` and never finds consumers; fix PR #8166 is still OPEN as of 2026-09-18.** Workaround until merged: KEDA `prometheus` trigger on `jetstream_consumer_num_pending{stream_name="CLUSTARR_WORK_TRANSCODE",consumer_name="transcodarr-workers"} + jetstream_consumer_num_ack_pending{...}` from the exporter sidecar, or pin nats-server 2.12.x/2.11.x (not recommended).

**Footprint:** single static Go binary; tens of MB RSS on restart per issue reports; memory grows with dedup-window cardinality and file-store cache. R3 streams need 3 servers; R1 is fine on a single-node homelab (data loss on disk loss only).

**Go client quality:** first-party, actively released (3 releases in 2026), context-aware, generics-free, good docs, `jetstream` package is the recommended API (legacy `nats.JetStreamContext` is maintained but not where new features land).

**Observability:** `/jsz`, `/varz`, `/connz` monitoring on 8222; prometheus-nats-exporter sidecar in chart; `nats` CLI (`nats stream info`, `nats consumer report`, `nats stream view`) via nats-box; server advisories on `$JS.EVENT.ADVISORY.>` are themselves subscribable.

### 2.2 Redis Streams (+ asynq, machinery)

**Versions [verified]:** go-redis v9.22.0 stable (v9.23.0-beta.1 exists); asynq v0.26.0 (2026-02-03; before that v0.25.1 Dec 2024 - slow cadence); machinery v2.0.16 (not actively developed); OT-CONTAINER-KIT redis-operator v0.26.0 (2026-07-15); spotahome operator forked to freshworks-oss/redis-operator (Redis or Valkey via `spec.engine`).

**Semantics:** Streams give an append-only log with consumer groups (`XADD`, `XREADGROUP`, `XACK`, `XPENDING`, `XAUTOCLAIM` with delivery counter, `XACKDEL` in newer servers). go-redis exposes `XAdd/XReadGroup/XAck/XAckDel/XAutoClaim/XAutoClaimJustID/XPending/XPendingExt/XGroupCreateMkStream` [verified]. There is no server-side AckWait/MaxDeliver/BackOff: you must run a reclaimer loop (`XAUTOCLAIM` with min-idle), inspect the delivery counter, and move poison entries to a DLQ stream yourself. No Term. Ordering per stream. Dedup must be hand-built (`SET NX` with TTL). Redis 8 is tri-licensed (RSALv2/SSPLv1/AGPLv3); Valkey (BSD) is the drop-in fork and go-redis works with both.

**asynq** (Redis-only job queue): `Client.Enqueue(task, asynq.Queue, MaxRetry, Timeout, Deadline, Unique(ttl), ProcessAt/ProcessIn, TaskID, Retention, Group)` [verified]; `Server{Config{Concurrency, Queues map[string]int (weighted priority), StrictPriority, RetryDelayFunc, IsFailure, Group* aggregation}}`. Retries, scheduled tasks, archived (dead) tasks, `asynqmon` UI, CLI. Lua scripts may not be Redis-Cluster-safe. It is a job queue, not an event bus - you'd still need Redis Streams or Pub/Sub (non-durable) for events and there is no long-running-job heartbeat (you set `Timeout`).

**Fit:** two mechanisms, hand-rolled redelivery, in-memory dataset bounded by RAM, and a licensing story that pushes you to Valkey. Operators exist but are community-grade. Not chosen.

### 2.3 Apache Kafka / Redpanda

**Versions [verified]:** Strimzi 1.2.0 (2026-08-20, KRaft-only since 0.46); Redpanda operator v25.3.9 / charts v26.1.11 (2026-09); franz-go v1.22.0, sarama v1.60.2, kafka-go v0.4.51.

**Semantics:** partitioned log, consumer groups, offset commits - no per-message ack; a slow/failed record blocks the partition; retries/DLQ are application patterns (retry topics). KIP-932 "Queues for Kafka" (share groups, per-message acknowledge/release/reject, delivery-count) is GA in Apache Kafka 4.1 with Java clients only; non-Java client support is "targeted for H2 2026" - I found no share-group support in franz-go v1.22.0 [unverified: not in `go doc` scan]. Exactly-once is producer idempotence + transactions (Kafka) and not something the Go clients make easy.

**Ops/footprint:** Redpanda requires >= 2 GiB memory per core (docs say request 2.22-2.5 GiB/core) per broker; Kafka brokers are JVMs (1-2 GiB heap realistic) plus controllers. Strimzi is mature (CNCF) and heavy; Redpanda operator is fine but single-vendor. For a homelab media stack this is the wrong weight class. Not chosen.

### 2.4 RabbitMQ

**Versions [verified]:** rabbitmq/cluster-operator v2.23.0 (2026-09-09, needs cert-manager >= 2.20, deploys RabbitMQ 4.2.x by default); messaging-topology-operator manages queues/exchanges; amqp091-go v1.15.0.

**Semantics:** quorum queues: per-message ack/nack/reject, `delivery-limit` default 20 (then dropped or dead-lettered to a DLX), `dead-letter-strategy: at-least-once` (requires `overflow: reject-publish`), consumer priorities, single-active-consumer. Streams (RabbitMQ Streams) give a replayable log but through a different protocol/client. Quorum needs 3 nodes (majority). Erlang VM footprint ~150-300 MB per node idle.

**Fit:** good work queue, but pub/sub with replay needs RabbitMQ Streams (another client), and it's a second ecosystem to run (Erlang, cert-manager dependency). Go client is a maintained but bare AMQP 0-9-1 driver. Not chosen.

### 2.5 Postgres-backed queues (River, neoq, gue) + CloudNativePG

**Versions [verified]:** river v0.47.0 (2026-08-31; very active), neoq v0.72.1 (`github.com/acaloiaro/neoq`), gue v5.9.0; CloudNativePG v1.30.0 (2026-06-29).

**River:** `river.NewClient(riverpgxv5.New(pool), &river.Config{Queues, Workers, JobTimeout, MaxAttempts (default 25), RetryPolicy, PeriodicJobs, RescueStuckJobsAfter, ...})`; `Client.Insert/InsertTx/InsertMany`; `river.Worker[T]` with `Work(ctx, *river.Job[T]) error`; `river.JobSnooze(d)`, `river.JobCancel(err)`; states `available, scheduled, pending, running, retryable, completed, cancelled, discarded` (discarded = dead letter, retained `DiscardedJobRetentionPeriod`); `InsertOpts{MaxAttempts, Priority, Queue, ScheduledAt, Tags, UniqueOpts{ByArgs, ByPeriod, ByQueue, ByState}}`; LISTEN/NOTIFY with poll-only fallback; River UI; workflows are River Pro (paid).

**Fit:** the best pure job queue in Go, transactional with your own tables. But Clustarr's system of record is Kubernetes (CRDs), not Postgres, so "transactional enqueue with the domain write" - River's main selling point - does not apply. It is not an event bus (LISTEN/NOTIFY is fire-and-forget; you'd build an outbox + fan-out tables per subscriber). Running Postgres just for a queue adds CNPG + backups. Not chosen as the transport; keep in mind if a service later needs relational storage.

### 2.6 "Kubernetes-native": CRDs as queue, informers as pub/sub, batch/v1 Jobs, KEDA

**Facts [verified/known]:** etcd request limit 1.5 MiB, objects effectively <= 1 MiB; high-churn CRs and events stress etcd (noisy-neighbour); controller-runtime workqueue rate limiter `workqueue.DefaultTypedControllerRateLimiter` (per-item exponential 5 ms -> 1000 s plus 10 qps/100 burst bucket) [verified]; `source.Channel(<-chan event.TypedGenericEvent[T], handler, opts...)` in controller-runtime v0.25.1 for injecting external events [verified]; KEDA `ScaledJob` (`jobTargetRef`, `pollingInterval` 30s, `maxReplicaCount` 100, `scalingStrategy.strategy: default|custom|accurate|eager`, `successfulJobsHistoryLimit`/`failedJobsHistoryLimit` 100) creates one Job per queue item with `accurate` [verified].

**As a queue:** a CR per task gives you persistence, `kubectl` visibility, RBAC and owner-reference GC for free, and reconciliation gives "at-least-once, idempotent, level-triggered" semantics. It does *not* give ordering, per-message ack, backoff arrays, fan-out to multiple consumers (every controller sees every object; you must filter), or cheap high-throughput enqueue. Informer caches are a *state* sync, not an event log: a subscriber that was down sees only the final state, not each transition (fine for reconcilers, wrong for "audit every ReleaseGrabbed"). Kubernetes `Events` are best-effort and TTL 1h. Volume guide: hundreds of CRs/day with a TTL-after-finished controller is fine; thousands of sub-minute tasks/day (subtitle fetches, metadata refresh, searches) should not be CRs.

**As workers:** `batch/v1` Jobs are a good execution unit for hour-long transcodes (pod per job, resource requests for CPU/GPU, `activeDeadlineSeconds`, `ttlSecondsAfterFinished`), and KEDA `ScaledJob` with the `nats-jetstream` trigger creates Jobs from consumer lag. A Deployment + `ScaledObject` (min 0) with each pod running a `Consume` loop is simpler and keeps ffmpeg processes warm; either works with the same NATS consumer.

---

## 3. Comparison matrix

Scores: A = native/first-class, B = works with app-side logic, C = awkward or missing.

| Criterion | NATS JetStream | Redis Streams / asynq | Kafka / Redpanda | RabbitMQ (quorum + streams) | Postgres / River | CRDs + controllers |
|---|---|---|---|---|---|---|
| At-least-once | A (explicit ack, AckWait redelivery) | B (PEL + XAUTOCLAIM loop) / A (asynq) | A (offset commit) | A | A | A (reconcile) |
| Exactly-once-ish | A (Nats-Msg-Id dedup + DoubleAck + idempotent handler) | C (SET NX yourself) | B (idempotent producer + txns, Java-centric) | B (publisher confirms + dedup plugin) | A (transactional insert/complete, but only for DB-side work) | B (idempotent reconcile, resourceVersion CAS) |
| Ordering | A per subject/stream; strict with MaxAckPending=1 or ordered consumer | A per stream | A per partition | B per queue (single active consumer) | B (priority + created_at) | C (none) |
| Per-message ack / retry / DLQ | A ack/nak(delay)/term/inprogress, MaxDeliver, BackOff; DLQ via advisory + copy (we build) | B/A asynq archived | C (retry topics), A* share groups Java-only | A delivery-limit + DLX at-least-once | A retryable/discarded | C (rate-limited requeue only) |
| Fan-out pub/sub with replay | A (Limits/Interest streams, N durable consumers, DeliverByStartTime) | B (streams) | A | B (RabbitMQ Streams, different client) | C (outbox + fan-out tables) | C (state sync, not log) |
| Work queue | A (WorkQueue retention, shared pull consumer) | A (asynq) | B/C (partition-bound) | A | A | C |
| Long-running jobs (hours) | A InProgress heartbeat | B asynq Timeout only | C (max.poll.interval) | B (ack timeout 30 min default) | A (JobTimeout, RescueStuckJobsAfter) | A (Job pod) |
| Horizontal consumer scaling | A (any number of pull clients on one consumer; KEDA scaler) | A (consumer group; KEDA redis-streams scaler) | B (bounded by partitions) | A (KEDA rabbitmq scaler) | A (many clients; KEDA postgresql scaler) | A (KEDA kubernetes-workload) |
| K8s operator maturity | A Helm 2.14.6 + NACK 0.24 CRDs | B OT-CONTAINER-KIT 0.26 / freshworks fork | A Strimzi 1.2 / Redpanda op 25.3 | A cluster-operator 2.23 + topology op | A CNPG 1.30 | A (it *is* K8s) |
| Homelab footprint | A ~50-200 MiB/node, single binary | A ~50 MiB | C >= 2 GiB/core/broker | B ~300 MiB/node x3 | B Postgres ~256 MiB + operator | A |
| Go client | A first-party nats.go | A go-redis / asynq | A franz-go | B amqp091-go | A river + pgx | A controller-runtime |
| Observability | A /jsz, exporter, advisories, nats CLI | B redis-exporter, asynqmon | A JMX/exporters, Console | A management UI | A River UI | A kubectl |
| Both event bus AND work queue | **A** | B (two mechanisms) | B (queue is weak in Go) | B (two protocols) | C | C |

---

## 4. Recommendation and rationale

Use **NATS JetStream** as the single messaging substrate:

1. Event bus: one Limits-retention stream `CLUSTARR_EVENTS` (7-day MaxAge) with durable pull consumers per service. New services replay from a start time. Per-entity ordering by putting the entity UID as the last subject token.
2. Work queues: one WorkQueue-retention stream per service (`CLUSTARR_WORK_TRANSCODE`, `CLUSTARR_WORK_DOWNLOAD`, `CLUSTARR_WORK_SUBTITLE`, `CLUSTARR_WORK_METADATA`). Priority tiers are subject partitions with separate non-overlapping consumers and worker pools (WorkQueue rule). Delayed work uses `WithScheduleAt` or `NakWithDelay`.
3. KV buckets for leases (download-client ownership), dedup markers, job progress; the Object Store is *not* used for media (media stays on root-folder PVC/NFS) - at most for small artefacts like subtitle files or ffprobe JSON.
4. CRDs remain the source of truth for desired state (Movie/Series/Album/Book, QualityProfile, RootFolder, Indexer, DownloadClient, ImportList, TranscodeProfile) and for user-visible jobs (`TranscodeJob`, `DownloadTask`). Controllers reconcile CRs, emit events, enqueue work, and update `.status` from events/progress via `source.Channel`.
5. Indexer search is *request/reply* (`micro` service per indexer or core NATS request with a wildcard reply and a deadline), not a queue; results are cached in KV with TTL. Only RSS sync and "search for missing" campaigns are queued work.
6. KEDA scales worker Deployments (and optionally Jobs) from consumer lag.

Why not the others (short): Kafka/Redpanda - footprint and no Go per-message ack; Redis - two mechanisms plus hand-rolled redelivery and licence churn; RabbitMQ - second ecosystem, streams need another client; Postgres/River - superb queue, no bus, and K8s is our database; CRDs-only - no ack/ordering/backoff and etcd churn for the short tasks.

Costs we accept: NATS has no built-in DLQ (we build a 60-line advisory watcher); WorkQueue retention is immutable per stream (create it right); dedup window is memory-resident (keep `Duplicates` <= 10 m); the KEDA NATS scaler bug #8165 forces the Prometheus-trigger workaround until PR #8166 ships; NACK legacy mode does not reconcile KV/ObjectStore (run `--control-loop`).

---

## 5. Hybrid design

### 5.1 Topology

```
            +-----------------------------+   watch/reconcile   +--------------------------+
 kubectl -> |  CRDs (desired state)       | <------------------> | service controllers      |
            |  Movie, Series, Album, Book |                      | (controller-runtime)     |
            |  QualityProfile, Indexer,   |                      |  inventarr, downloadarr, |
            |  DownloadClient, ImportList |                      |  indexarr, transcodarr,  |
            |  TranscodeJob, DownloadTask |                      |  subtitlarr, metadatarr  |
            +-----------------------------+                      +-----+---------------+----+
                                                                       | publish        | source.Channel
                                                                       v                |
   +------------------------------------- NATS JetStream (3x nats, R3) ------------------------------+
   |  CLUSTARR_EVENTS   (Limits, 7d)   clustarr.evt.<domain>.<entity>.<event>.<uid>                   |
   |  CLUSTARR_WORK_*   (WorkQueue)    clustarr.work.<service>.<task>.<tier>.<id>                     |
   |  CLUSTARR_DLQ      (Limits, 30d)  clustarr.dlq.<service>.<task>.<id>                             |
   |  KV: clustarr-leases (TTL), clustarr-progress, clustarr-dedup (TTL), clustarr-search-cache (TTL)|
   +---------------------------+-----------------------------------------+--------------------------+
                               | pull consumers (Consume)                | /jsz or exporter metrics
                               v                                         v
                 +---------------------------+                    +-------------+
                 | workers (Deployment, min 0)|  <-- scales ---    | KEDA 2.20   |
                 | ffmpeg / torrent / usenet / |                    +-------------+
                 | subtitle providers          |
                 +---------------------------+
```

### 5.2 Stream definitions (Go, applied idempotently at startup; NACK YAML equivalent in 5.3)

```go
var Streams = []jetstream.StreamConfig{
  {
    Name: "CLUSTARR_EVENTS", Description: "domain events (append-only, replayable)",
    Subjects: []string{"clustarr.evt.>"},
    Retention: jetstream.LimitsPolicy, Discard: jetstream.DiscardOld,
    MaxAge: 7 * 24 * time.Hour, MaxBytes: 2 << 30, // 2 GiB backstop
    Storage: jetstream.FileStorage, Replicas: replicas(), // 3 in-cluster, 1 on kind
    Duplicates: 10 * time.Minute, Compression: jetstream.S2Compression,
    AllowDirect: true, DenyDelete: true,
    Metadata: map[string]string{"clustarr.io/kind": "events"},
  },
  {
    Name: "CLUSTARR_WORK_TRANSCODE", Subjects: []string{"clustarr.work.transcode.>"},
    Retention: jetstream.WorkQueuePolicy, Discard: jetstream.DiscardNew, // backpressure, not silent loss
    MaxMsgs: 100_000, MaxAge: 30 * 24 * time.Hour,
    Storage: jetstream.FileStorage, Replicas: replicas(),
    Duplicates: 10 * time.Minute, AllowMsgSchedules: true, // delayed re-try / off-peak scheduling
  },
  // CLUSTARR_WORK_DOWNLOAD, CLUSTARR_WORK_SUBTITLE, CLUSTARR_WORK_METADATA: same shape, own subjects.
  {
    Name: "CLUSTARR_DLQ", Subjects: []string{"clustarr.dlq.>"},
    Retention: jetstream.LimitsPolicy, MaxAge: 30 * 24 * time.Hour, Storage: jetstream.FileStorage,
    Replicas: replicas(), AllowDirect: true,
  },
}
```

Consumers (all durable pull, `AckExplicit`):

| Consumer (durable name) | Stream | FilterSubjects | AckWait | MaxDeliver | BackOff | MaxAckPending | Notes |
|---|---|---|---|---|---|---|---|
| `inventarr-events` | CLUSTARR_EVENTS | `clustarr.evt.download.>`, `clustarr.evt.transcode.>`, `clustarr.evt.subtitle.>` | 30s | 10 | 1s,5s,30s,2m,10m | 256 | each service owns ONE durable on EVENTS, filtered to what it cares about |
| `transcodarr-events` | CLUSTARR_EVENTS | `clustarr.evt.download.task.imported.*`, `clustarr.evt.inventory.*.profilechanged.*` | 30s | 10 | same | 256 | |
| `audit-tap` | CLUSTARR_EVENTS | `clustarr.evt.>` | 30s | -1 | | 1000 | optional; ordered consumer for UI/event log |
| `transcodarr-workers-high` | CLUSTARR_WORK_TRANSCODE | `clustarr.work.transcode.*.high.*` | 5m | 5 | 1m,5m,15m,1h | 1 per pod x pods (set 64) | workers call InProgress every 60s |
| `transcodarr-workers-normal` | CLUSTARR_WORK_TRANSCODE | `clustarr.work.transcode.*.normal.*`, `clustarr.work.transcode.*.low.*` | 5m | 5 | same | 64 | non-overlapping with `high` (WorkQueue rule) |
| `downloadarr-workers` | CLUSTARR_WORK_DOWNLOAD | `clustarr.work.download.>` | 2m | 20 | 10s,1m,5m | 32 | worker takes a KV lease per task before starting |
| `subtitlarr-workers` | CLUSTARR_WORK_SUBTITLE | `clustarr.work.subtitle.>` | 60s | 8 | 30s,2m,10m,1h,6h | 16 | provider rate limits => NakWithDelay(retryAfter) |
| `metadatarr-workers` | CLUSTARR_WORK_METADATA | `clustarr.work.metadata.>` | 60s | 8 | same | 32 | |
| `dlq-controller` | CLUSTARR_DLQ | `clustarr.dlq.>` | 30s | -1 | | 64 | surfaces DLQ entries as CR status conditions / UI |

Rules: MaxDeliver must exceed len(BackOff) [verified server rule]; BackOff only covers AckWait expiry - handlers that fail fast must call `NakWithDelay` themselves (the library does this from the attempt number, see 6.2). `InactiveThreshold` unset for durables (never auto-delete).

### 5.3 NACK YAML equivalent (for GitOps installs)

```yaml
apiVersion: jetstream.nats.io/v1beta2
kind: Stream
metadata: { name: clustarr-work-transcode, namespace: clustarr }
spec:
  name: CLUSTARR_WORK_TRANSCODE
  subjects: ["clustarr.work.transcode.>"]
  retention: workqueue
  discard: new
  storage: file
  replicas: 3
  maxMsgs: 100000
  maxAge: 720h
  duplicates: 10m
  allowMsgSchedules: true   # NACK 0.23+ carries 2.14 fields
---
apiVersion: jetstream.nats.io/v1beta2
kind: Consumer
metadata: { name: transcodarr-workers-normal, namespace: clustarr }
spec:
  streamName: CLUSTARR_WORK_TRANSCODE
  durableName: transcodarr-workers-normal
  deliverPolicy: all
  ackPolicy: explicit
  ackWait: 5m
  maxDeliver: 5
  backOff: ["1m", "5m", "15m", "1h"]
  maxAckPending: 64
  filterSubjects: ["clustarr.work.transcode.*.normal.*", "clustarr.work.transcode.*.low.*"]
```
(Field names follow the NACK CRD; verify against `kubectl explain consumer.spec` for the installed chart version - the README example uses `filterSubject`, `maxDeliver`, `ackPolicy`, `deliverPolicy`, `durableName`, `streamName` [verified].)

### 5.4 Subject grammar

```
clustarr.evt.<domain>.<entity>.<event>.<uid>        events   (uid = K8s metadata.uid or ULID; last token => per-entity filter & DeliverLastPerSubject)
clustarr.work.<service>.<task>.<tier>.<id>          work     (tier in {high,normal,low}; id = ULID / CR uid)
clustarr.dlq.<service>.<task>.<id>                  dead letters
clustarr.rpc.<service>.<method>                     request/reply (core NATS / micro), never JetStream
clustarr.progress.<service>.<id>                    high-frequency progress (core NATS, not persisted) - persisted summary goes to KV
```
Examples: `clustarr.evt.inventory.movie.added.9f2c...`, `clustarr.evt.download.task.completed.01J...`, `clustarr.evt.transcode.job.succeeded.01J...`, `clustarr.work.transcode.video.normal.01J...`, `clustarr.work.subtitle.fetch.normal.01J...`, `clustarr.rpc.indexarr.search`.

Naming rules: lowercase, no dots inside tokens (dots are separators), `<event>` is past tense (`added`, `grabbed`, `completed`, `failed`, `requested` for commands), version the *payload* with a header (`Clustarr-Schema: inventory.MovieAdded.v1`) rather than the subject so filters stay stable. Stream names are `CLUSTARR_<KIND>[_<SERVICE>]` uppercase (JetStream convention). Durable consumer names `<service>-<role>[-<tier>]`. KV buckets `clustarr-<purpose>` (bucket names cannot contain dots).

Headers on every message:
```
Nats-Msg-Id:        <envelope id>            # server dedup within Duplicates window
Clustarr-Type:      inventory.movie.added    # = subject minus prefix/uid, handy for routing without parsing the subject
Clustarr-Schema:    inventory.MovieAdded.v1
Clustarr-Source:    inventarr/controller
Clustarr-Key:       <entity uid>
Clustarr-Time:      RFC3339Nano
Clustarr-Trace:     W3C traceparent
Content-Type:       application/json         # protobuf later: application/protobuf; schema in Clustarr-Schema
```
Optional: emit CloudEvents binary-mode headers (`ce-id`, `ce-type`, `ce-source`, `ce-specversion`) via `github.com/cloudevents/sdk-go/v2` v2.16.2 so external tools can consume the bus; not required for v1.

### 5.5 Dead-letter design (NATS has none built in)

1. Handler returns `events.Discard(err)` -> library publishes a copy to `clustarr.dlq.<service>.<task>.<id>` with headers `Clustarr-DLQ-Reason`, `Clustarr-DLQ-Attempts`, `Clustarr-DLQ-Consumer`, original subject, then `TermWithReason(reason)`.
2. Crash loops that never return: a tiny `dlq-sweeper` subscribes (core NATS) to `$JS.EVENT.ADVISORY.CONSUMER.MAX_DELIVERIES.<stream>.<consumer>`, reads `stream_seq` from the advisory, `stream.GetMsg(ctx, seq)`, publishes the copy to the DLQ subject, then `stream.DeleteMsg(ctx, seq)` (WorkQueue streams keep the message otherwise). Also logs `$JS.EVENT.ADVISORY.CONSUMER.MSG_TERMINATED.>` for audit.
3. `dlq-controller` reconciles DLQ entries into `.status.conditions[type=Failed]` on the owning CR (`TranscodeJob`, `DownloadTask`) or a `clustarr-dlq` KV entry when there is no CR; a `kubectl clustarr replay <id>` / API republishes to the original subject with a new `Nats-Msg-Id`.

### 5.6 CRD <-> NATS bridging patterns

- **Enqueue from reconcile:** controller sets `status.phase=Queued` and publishes `clustarr.work.transcode.video.normal.<uid>` with `Nats-Msg-Id = <uid>:<generation>` inside the same reconcile; publish failure -> requeue with error; duplicate publish on retry is absorbed by dedup (window 10m) and by the worker checking the KV lease/`status.phase`. This is the "outbox" without a table.
- **Events into reconcile:** each controller runs a `Subscriber` on its EVENTS durable and pushes `event.TypedGenericEvent[*v1alpha1.TranscodeJob]{Object: &TranscodeJob{ObjectMeta{Name, Namespace}}}` into a channel wired with `source.Channel(ch, &handler.TypedEnqueueRequestForObject[*v1alpha1.TranscodeJob]{})` [signature verified]. Ack the NATS message *after* the channel send; reconcile is idempotent so at-least-once is fine.
- **Progress:** workers publish high-frequency progress on core NATS (`clustarr.progress.transcode.<id>`, not persisted) and write a summarised record to KV `clustarr-progress` every N seconds (`Update` with revision, CAS); the controller watches the KV bucket (`kv.Watch(ctx, "transcode.>")`) and patches `.status.progress` at most every 10 s.
- **Leases:** `kv.Create(ctx, "download."+id, workerID)` on bucket with `TTL: 2m`; refreshed by `Update(key, val, rev)` on each heartbeat; `Create` failing with key-exists means another worker owns it -> `NakWithDelay(30s)`.
- **CR lifecycle:** finished jobs get `ttlSecondsAfterFinished`-style cleanup by their controller (default 24 h) to keep etcd small; DownloadTask/TranscodeJob CRs carry ownerReferences to the Movie/Series CR for GC.

### 5.7 Autoscaling

```yaml
apiVersion: keda.sh/v1alpha1
kind: ScaledObject
metadata: { name: transcodarr-workers, namespace: clustarr }
spec:
  scaleTargetRef: { name: transcodarr-worker }     # Deployment; each pod runs Consume with PullMaxMessages(1)
  minReplicaCount: 0
  maxReplicaCount: 6                               # ~ CPU-bound ffmpeg slots on the cluster
  cooldownPeriod: 600                              # let a long transcode finish (also set terminationGracePeriodSeconds high + InProgress)
  triggers:
  - type: nats-jetstream                           # preferred once kedacore/keda PR #8166 is released
    metadata:
      natsServerMonitoringEndpoint: "nats.nats.svc.cluster.local:8222"
      account: "$G"
      stream: "CLUSTARR_WORK_TRANSCODE"
      consumer: "transcodarr-workers-normal"
      lagThreshold: "1"
      activationLagThreshold: "0"
  # workaround while #8165 is open on nats-server >= 2.12.7:
  # - type: prometheus
  #   metadata:
  #     serverAddress: http://prometheus.monitoring.svc:9090
  #     query: sum(jetstream_consumer_num_pending{stream_name="CLUSTARR_WORK_TRANSCODE",consumer_name="transcodarr-workers-normal"}) + sum(jetstream_consumer_num_ack_pending{...})
  #     threshold: "1"
```
Alternative for GPU/heavy jobs: `ScaledJob` with `scalingStrategy.strategy: accurate`, `jobTargetRef.template` running `clustarr-transcode-worker --one-shot` (`Consume` with `StopAfter(1)` or `Fetch(1)`), `maxReplicaCount` = GPU count. Scale-down safety: workers handle SIGTERM by finishing the current job (grace period >= AckWait; keep calling `InProgress`) or by `Nak`-ing early if `--preempt`.

### 5.8 Homelab sizing (defaults to ship in the Helm umbrella)

- NATS: 3 replicas (kind: 1), `config.jetstream.fileStore.pvc.size: 20Gi`, memoryStore disabled, requests 100m/256Mi, limits 1 CPU/1Gi, `GOMEMLIMIT` via limits. Streams R3 in-cluster, R1 on kind. Enable `promExporter`, `natsBox`, `reloader`.
- NACK: 1 replica, `--control-loop`, 50m/64Mi.
- KEDA: standard chart (operator + metrics-apiserver ~ 200 MiB total).
- Total messaging overhead well under 1 GiB RAM; compare >= 6 GiB for a 3-broker Redpanda/Kafka.

---

## 6. `pkg/events` Go interface sketch

Design goals: one small interface used by every service; NATS JetStream implementation first; in-memory implementation for unit tests with identical semantics (ack/nak/term/redelivery/max-deliver/DLQ); no leaking of `jetstream.*` types above the adapter.

### 6.1 Types

```go
// pkg/events/events.go
package events

import (
    "context"
    "errors"
    "time"
)

// Envelope is the transport-neutral message. Data is the encoded payload (JSON in v1).
type Envelope struct {
    ID          string            // ULID; doubles as Nats-Msg-Id for dedup. Deterministic for CR-driven publishes: "<uid>:<generation>:<type>".
    Type        string            // dotted type, e.g. "inventory.movie.added" or "transcode.video"
    Schema      string            // "inventory.MovieAdded.v1"
    Source      string            // "inventarr/controller"
    Key         string            // entity/job identity, last subject token
    Time        time.Time
    ContentType string            // "application/json"
    Headers     map[string]string // extra headers (trace, tenant, tier)
    Data        []byte
}

// Subject builds the canonical subject for a namespace prefix ("clustarr.evt" or "clustarr.work.<svc>").
func Subject(prefix string, e *Envelope, extra ...string) string

// Message is a received Envelope with acknowledgement controls. Exactly one of Ack/Nak/Term must be called
// unless the handler returns and the Subscriber auto-acks (see Options.AutoAck).
type Message interface {
    Envelope() *Envelope
    Subject() string
    Attempt() uint64             // 1-based delivery count (NATS NumDelivered)
    Pending() uint64             // messages still queued behind this one (NumPending), 0 if unknown
    Ack(ctx context.Context) error
    Nak(ctx context.Context, delay time.Duration) error // redeliver later; delay 0 = ASAP
    Term(ctx context.Context, reason string) error      // never redeliver (caller has already dead-lettered)
    Heartbeat(ctx context.Context) error                // extend ack window (NATS InProgress); no-op in memory
}

// Handler processes one message. Return nil to Ack. Return Retry(...) to Nak with a delay, Discard(...) to
// dead-letter + Term, any other error to Nak using the subscription's backoff schedule for Attempt().
type Handler func(ctx context.Context, m Message) error

type RetryError struct{ After time.Duration; Err error }
func (e *RetryError) Error() string { return e.Err.Error() }
func (e *RetryError) Unwrap() error { return e.Err }
func Retry(after time.Duration, err error) error { return &RetryError{After: after, Err: err} }

type DiscardError struct{ Reason string; Err error }
func (e *DiscardError) Error() string { return e.Reason + ": " + e.Err.Error() }
func (e *DiscardError) Unwrap() error { return e.Err }
func Discard(reason string, err error) error { return &DiscardError{Reason: reason, Err: err} }

var ErrDuplicate = errors.New("events: duplicate message id")

type PublishOption func(*PublishOptions)
type PublishOptions struct {
    ExpectStream string    // reject if routed to a different stream (safety)
    ScheduleAt   time.Time // delayed delivery (JetStream Nats-Schedule "@at"); memory impl uses a timer
    TTL          time.Duration
    IdempotencyKey string  // overrides Envelope.ID for dedup
}
func WithSchedule(t time.Time) PublishOption
func WithExpectStream(name string) PublishOption
func WithTTL(d time.Duration) PublishOption

// Publisher publishes durably: returns after the broker persisted the message (JetStream PubAck).
type Publisher interface {
    Publish(ctx context.Context, subject string, e *Envelope, opts ...PublishOption) (*Receipt, error)
}
type Receipt struct{ Stream string; Sequence uint64; Duplicate bool }

// Subscription describes a durable consumer. Both the event bus and work queues are Subscriptions;
// the difference is the Stream (Limits vs WorkQueue retention) and MaxInFlight.
type Subscription struct {
    Stream        string          // "CLUSTARR_EVENTS" or "CLUSTARR_WORK_TRANSCODE"
    Durable       string          // "transcodarr-workers-normal"
    Filter        []string        // subject filters (non-overlapping across consumers on WorkQueue streams)
    StartFrom     StartPosition   // All | New | Time(t)  (events only; WorkQueue must be All)
    AckWait       time.Duration   // e.g. 5m for transcodes
    MaxDeliver    int             // -1 unlimited
    Backoff       []time.Duration // len < MaxDeliver; used for AckWait expiry (server) AND for generic errors (client Nak delay)
    MaxInFlight   int             // NATS MaxAckPending
    Heartbeat     time.Duration   // if > 0 the Subscriber calls Heartbeat() on this cadence while Handler runs
    Concurrency   int             // handler goroutines per process (PullMaxMessages)
    DeadLetter    *DeadLetter     // nil = no DLQ copy on Discard
}
type DeadLetter struct{ SubjectPrefix string } // "clustarr.dlq.transcodarr"
type StartPosition struct{ Kind StartKind; Time time.Time }
type StartKind int
const ( StartAll StartKind = iota; StartNew; StartTime )

// Subscriber runs Handler for a Subscription until ctx is cancelled. Returns after drain.
type Subscriber interface {
    Subscribe(ctx context.Context, sub Subscription, h Handler) error
}

// WorkQueue is a thin convenience over Publisher/Subscriber for task semantics.
type WorkQueue interface {
    Enqueue(ctx context.Context, task *Envelope, opts ...EnqueueOption) (*Receipt, error) // tier/delay/dedup key
    Work(ctx context.Context, sub Subscription, h Handler) error                            // = Subscribe with WorkQueue defaults
    Depth(ctx context.Context, durable string) (pending, inFlight uint64, err error)         // for tests/ops; KEDA reads the server directly
}
type EnqueueOption func(*EnqueueOptions)
type EnqueueOptions struct{ Tier string; Delay time.Duration; ScheduleAt time.Time; DedupKey string }
func Tier(t string) EnqueueOption          // "high" | "normal" | "low" -> subject token
func Delay(d time.Duration) EnqueueOption
func DedupKey(k string) EnqueueOption

// Bus bundles what a service needs; constructed once in cmd/<service>.
type Bus interface {
    Publisher
    Subscriber
    Queue(service string) WorkQueue
    KV(bucket string) (KV, error)
    Close(ctx context.Context) error
}

// KV is the subset of jetstream.KeyValue we use (leases, progress, dedup, caches).
type KV interface {
    Get(ctx context.Context, key string) (Entry, error)
    Create(ctx context.Context, key string, val []byte) (rev uint64, err error)              // fails if exists
    Update(ctx context.Context, key string, val []byte, rev uint64) (uint64, error)          // CAS
    Put(ctx context.Context, key string, val []byte) (uint64, error)
    Delete(ctx context.Context, key string) error
    Watch(ctx context.Context, pattern string) (<-chan Entry, func(), error)                  // pattern "transcode.>"
}
type Entry struct{ Key string; Value []byte; Revision uint64; Created time.Time; Deleted bool }
```

### 6.2 NATS implementation notes (`pkg/events/natsbus`)

- `New(nc *nats.Conn, cfg Config) (*Bus, error)`: `js, _ := jetstream.New(nc, jetstream.WithPublishAsyncMaxPending(256))`; `EnsureStreams(ctx)` calls `js.CreateOrUpdateStream` for every `StreamConfig` in `Streams` (skips if NACK owns them: `cfg.ManagedExternally`).
- `Publish`: `js.Publish(ctx, subject, e.Data, jetstream.WithMsgID(id), jetstream.WithExpectStream(...), jetstream.WithScheduleAt(t))`; map `PubAck.Duplicate` to `Receipt.Duplicate` (not an error - callers treat it as success). Headers from Envelope -> `nats.Header` via `PublishMsg`.
- `Subscribe`: `s := js.Stream(ctx, sub.Stream)`; `c := s.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{Durable, FilterSubjects, DeliverPolicy, OptStartTime, AckPolicy: AckExplicitPolicy, AckWait, MaxDeliver, BackOff, MaxAckPending})`; `cc, _ := c.Consume(handler, jetstream.PullMaxMessages(sub.Concurrency), jetstream.PullExpiry(30*time.Second), jetstream.PullHeartbeat(5*time.Second), jetstream.ConsumeErrHandler(...))`; on ctx done -> `cc.Drain()` then wait `<-cc.Closed()`. Per message: start heartbeat ticker (`m.InProgress()`), run Handler with a context that carries `Attempt`, then: nil -> `Ack()` (or `DoubleAck(ctx)` when `sub.DoubleAck`); `*RetryError` -> `NakWithDelay(e.After)`; `*DiscardError` -> publish copy to `sub.DeadLetter.SubjectPrefix + "." + type + "." + key` then `TermWithReason(reason)`; other error -> `NakWithDelay(backoffFor(attempt, sub.Backoff))`. If `attempt >= MaxDeliver` and err != nil -> treat as Discard (so the advisory sweeper is only a fallback).
- Ordering knob: `MaxInFlight: 1` + `Concurrency: 1` gives strict per-consumer order (for the audit tap / UI projector); services that need per-entity order with parallelism should shard by `Key` onto N durables with `FilterSubjects` (`clustarr.evt.>` can't be hashed server-side; use N deterministic subject suffix buckets `.<uid>` -> bucket in the publisher, e.g. `clustarr.evt.inventory.movie.added.b3.<uid>`; decide before v1 since filters are part of the subject grammar).
- Errors to surface: `jetstream.ErrConsumerDeleted` (recreate consumer, restart Consume), `jetstream.ErrNoHeartbeat` (log, client re-pulls), `nats.ErrConnectionClosed`.
- Tests: `natsserver.RunServer` from `github.com/nats-io/nats-server/v2/test` (v2.15.0) boots an embedded JetStream server with a temp dir in ~50 ms; use it for integration tests of the adapter, and the in-memory bus for everything else.

### 6.3 In-memory implementation (`pkg/events/membus`)

- Streams are `[]stored` with per-subject sequence; subscriptions hold a cursor and a `pending map[seq]deliverState{attempts, notBefore}`; a ticker drives redelivery when `now > deliveredAt + AckWait` or `notBefore`; `MaxDeliver` exceeded -> copy to DLQ subject and mark terminated; WorkQueue streams delete on Ack; dedup map `id -> expiry` honours `Duplicates`; `Heartbeat` resets `deliveredAt`; `Depth` counts cursor lag + pending. Use a fake clock (`github.com/jonboulle/clockwork` or a `Clock` interface) so tests assert backoff schedules without sleeping.
- Behavioural contract tests live in `pkg/events/eventstest` and run against both implementations: at-least-once redelivery on missing ack; Nak delay honoured; Term stops redelivery; MaxDeliver -> DLQ; dedup within window; WorkQueue delete-on-ack; filter routing; ordered delivery with MaxInFlight=1; graceful drain finishes in-flight handler.

### 6.4 Payload model (JSON v1, protobuf-ready)

```go
// pkg/events/schema/inventory.go
type MovieAdded struct {   // Clustarr-Schema: inventory.MovieAdded.v1
    UID        string `json:"uid"`        // Movie CR metadata.uid
    Namespace  string `json:"namespace"`
    Name       string `json:"name"`
    TMDBID     int64  `json:"tmdbId"`
    Monitored  bool   `json:"monitored"`
    ProfileRef string `json:"profileRef"` // QualityProfile name
    RootFolder string `json:"rootFolder"`
}
type ReleaseGrabbed struct { UID, MediaUID, IndexerRef, DownloadClientRef, Title, GUID string; Size int64; Quality Quality; Protocol string /*usenet|torrent*/ }
type DownloadCompleted struct { TaskUID, MediaUID, ClientRef, Path string; Files []string; Size int64 }
type ImportCompleted struct { MediaUID, ReleaseUID, Path string; Quality Quality; MediaInfo MediaInfo }
type TranscodeRequested struct { JobUID, MediaUID, InputPath, ProfileRef string; Tier string }
type TranscodeTask struct { JobUID, MediaUID, InputPath, OutputPath string; Profile TranscodeProfileSpec; Attempt int } // work payload
type SubtitleFetchTask struct { MediaUID, FilePath string; Languages []string; Providers []string; HearingImpaired bool }
type Quality struct { Name string; Resolution int; Source string /*bluray|webdl|hdtv*/ ; Codec string; Revision int }
```
Payloads carry K8s identities (`uid`, `namespace/name`) not whole objects; consumers `Get` the CR if they need more, keeping messages small (< 64 KiB, far below the 1 MiB default max).

---

## 7. ADR draft

```
# ADR-0001: Messaging substrate for events and work queues

Status: Proposed (2026-09-18)
Deciders: Clustarr maintainers
Technical story: Clustarr is an event-driven, Kubernetes-native media stack (inventory, indexers, downloads,
transcoding, subtitles). Services must exchange durable domain events and distribute long-running work with
retry/dead-letter semantics, scale workers to zero, and run on a homelab cluster.

## Context
- System of record is Kubernetes (CRDs + controllers via controller-runtime v0.25.1).
- Work mix: hour-long transcodes, minute-to-day downloads, second-scale subtitle/metadata tasks, request/reply searches.
- Requirements: at-least-once with idempotent handlers, per-message ack/nak/term, retry backoff, dead-letter,
  replayable event log, KEDA scaling, single Helm install on kind, small footprint, first-class Go client.
- Candidates evaluated: NATS JetStream, Redis Streams/asynq, Kafka/Redpanda, RabbitMQ, Postgres/River, CRDs-as-queue.
  (Comparison matrix in research/queue.md section 3.)

## Decision
1. NATS JetStream (nats-server >= 2.14, nats.go v1.53 `jetstream` API) is the only message transport.
   - CLUSTARR_EVENTS: Limits retention, 7d, file, R3 (R1 on kind), subjects clustarr.evt.>
   - CLUSTARR_WORK_<SERVICE>: WorkQueue retention, DiscardNew, subjects clustarr.work.<service>.>
   - CLUSTARR_DLQ: Limits retention, 30d, subjects clustarr.dlq.>
   - KV buckets clustarr-leases (TTL), clustarr-progress, clustarr-dedup (TTL), clustarr-search-cache (TTL)
2. Kubernetes CRDs hold desired state and user-visible jobs (TranscodeJob, DownloadTask); controllers enqueue
   work and consume events; per-task sub-minute work is NATS-only.
3. pkg/events defines Publisher/Subscriber/WorkQueue/KV interfaces; natsbus and membus implementations share
   one contract test suite.
4. Streams/consumers are declared in Go (CreateOrUpdate at startup) and optionally as NACK CRDs
   (jetstream.nats.io/v1beta2) for GitOps; NACK runs with --control-loop.
5. Workers scale with KEDA ScaledObject (min 0) on JetStream consumer lag; ScaledJob for GPU one-shots.
6. Indexer search is core-NATS request/reply (micro), not queued.

## Consequences
+ One substrate for bus + queue; per-message ack/nak/term/inprogress, MaxDeliver/BackOff, publish dedup, KV
  leases, replay, all first-party; ~200 MiB RAM for 3 nodes.
+ First-party Helm chart, operator (NACK), KEDA scaler, Prometheus exporter, CLI.
- No built-in DLQ: we implement Discard->DLQ copy + advisory sweeper (section 5.5).
- WorkQueue retention is immutable per stream and forbids overlapping consumers: priority tiers are subject
  partitions; stream definitions must be right the first time (rename = new stream + drain).
- Dedup window is memory-resident; keep <= 10 min and message IDs short.
- KEDA nats-jetstream scaler bug #8165 on nats-server >= 2.12.7; use the Prometheus trigger until PR #8166 ships.
- Exactly-once is "dedup on publish + idempotent handler keyed by Envelope.ID"; handlers must be written that way.

## Alternatives rejected
- Kafka/Redpanda: >= 2 GiB/core per broker; no per-message ack in Go clients (KIP-932 share groups Java-only).
- Redis Streams + asynq: two mechanisms; redelivery/DLQ hand-rolled on PEL/XAUTOCLAIM; licence churn (Redis 8 AGPL/SSPL) -> Valkey.
- RabbitMQ: strong queue but replayable pub/sub needs RabbitMQ Streams + another client; Erlang + cert-manager ops.
- Postgres/River: best Go job queue, but not an event bus and our system of record is Kubernetes, not Postgres.
- CRDs as queue: no ordering/ack/backoff, etcd churn for thousands of short tasks; kept only for desired state and long jobs.

## Revisit when
- Go Kafka clients get share groups and the cluster has >= 3 nodes with 8 GiB free each.
- KEDA PR #8166 lands (switch trigger back to nats-jetstream).
- A service needs relational queries over history (consider River/CNPG for that service only, still publishing to NATS).
```

---

## 8. Open questions for the architects

1. Per-entity ordering with parallel consumers: adopt subject bucket sharding (`.b<N>.`) from day one, or accept unordered fan-out and rely on CR `resourceVersion` CAS? (Recommendation: accept unordered for v1; every handler re-reads the CR.)
2. Should `TranscodeJob`/`DownloadTask` be CRs (visibility, RBAC, GC) or KV records with a read-only aggregated API? (Recommendation: CRs, with 24 h TTL cleanup.)
3. JSON vs protobuf payloads: JSON v1 with `Clustarr-Schema` header; protobuf when a second language client appears.
4. Multi-tenancy/accounts: single NATS account `$G` for v1; NACK `Account` CRD and per-namespace accounts later.
5. Media-file transport: never through NATS (1 MiB default max, object store is chunked but not for multi-GB video); shared PVC/NFS root folders as in the *arr stack.

---

## 9. Verified versions (go list -m -versions / gh release list, 2026-09-18)

| Module / component | Version | Role |
|---|---|---|
| github.com/nats-io/nats.go | v1.53.1 | client (`jetstream`, `micro` packages) |
| github.com/nats-io/nats-server/v2 | v2.15.0 | server; embeddable for tests |
| github.com/nats-io/jsm.go | v0.5.0 | admin/CLI-style management (optional) |
| github.com/nats-io/nack | v0.24.0 (chart nack 0.35.0) | JetStream CRDs |
| nats Helm chart | 2.14.6 | server deploy |
| github.com/kedacore/keda/v2 | v2.20.2 | autoscaling (`nats-jetstream`, `prometheus` triggers) |
| sigs.k8s.io/controller-runtime | v0.25.1 | controllers; `source.Channel` bridge |
| github.com/cloudevents/sdk-go/v2 | v2.16.2 | optional CloudEvents headers |
| github.com/ThreeDotsLabs/watermill (+ watermill-nats/v2) | v1.5.3 / v2.2.0 | evaluated; not adopted (its JetStream adapter wraps the same API; we want our own ack model) |
| github.com/redis/go-redis/v9 | v9.22.0 | rejected candidate |
| github.com/hibiken/asynq | v0.26.0 | rejected candidate |
| github.com/RichardKnop/machinery/v2 | v2.0.16 | rejected candidate (stale) |
| github.com/twmb/franz-go | v1.22.0 | rejected candidate |
| github.com/IBM/sarama / segmentio/kafka-go | v1.60.2 / v0.4.51 | rejected candidates |
| github.com/rabbitmq/amqp091-go | v1.15.0 | rejected candidate |
| github.com/riverqueue/river | v0.47.0 | rejected candidate (keep for relational needs) |
| github.com/acaloiaro/neoq | v0.72.1 | rejected candidate |
| github.com/vgarvardt/gue/v5 | v5.9.0 | rejected candidate |
| Strimzi / Redpanda operator | 1.2.0 / v25.3.9 | rejected candidates |
| rabbitmq/cluster-operator | v2.23.0 | rejected candidate |
| CloudNativePG | v1.30.0 | rejected (for queue purposes) |
| OT-CONTAINER-KIT/redis-operator | v0.26.0 | rejected candidate |

## 10. Sources

- nats.go v1.53.1 `go doc github.com/nats-io/nats.go/jetstream` (local module cache)
- nats-server errors.json: https://raw.githubusercontent.com/nats-io/nats-server/main/server/errors.json
- NATS 2.15 release: https://nats.io/blog/nats-server-2.15-release/ ; 2.14: https://nats.io/blog/nats-server-2.14-release/ ; 2.12: https://nats.io/blog/nats-server-2.12-release/
- NATS retention policies: https://docs.nats.io/learn/jetstream/retention-policies ; streams: https://docs.nats.io/nats-concepts/jetstream/streams ; pull consumers: https://docs.nats.io/nats-concepts/jetstream/consumers
- NACK README: https://github.com/nats-io/nack/blob/main/README.md ; releases: https://github.com/nats-io/nack/releases
- NATS Helm chart values: https://github.com/nats-io/k8s/blob/main/helm/charts/nats/values.yaml
- prometheus-nats-exporter jsz collector: https://github.com/nats-io/prometheus-nats-exporter/blob/main/collector/jsz.go
- KEDA nats-jetstream scaler: https://keda.sh/docs/2.20/scalers/nats-jetstream/ ; ScaledJob spec: https://keda.sh/docs/2.20/reference/scaledjob-spec/ ; bug: https://github.com/kedacore/keda/issues/8165 ; https://github.com/kedacore/keda/issues/7657 ; scaler source: https://github.com/kedacore/keda/blob/main/pkg/scalers/nats_jetstream_scaler.go
- River: https://riverqueue.com/docs ; https://github.com/riverqueue/river
- asynq: https://github.com/hibiken/asynq ; Redis Cluster wiki: https://github.com/hibiken/asynq/wiki/Redis-Cluster
- neoq: https://github.com/acaloiaro/neoq
- RabbitMQ quorum queues: https://www.rabbitmq.com/docs/quorum-queues ; cluster operator: https://www.rabbitmq.com/kubernetes/operator/operator-overview
- KIP-932 Queues for Kafka: https://cwiki.apache.org/confluence/display/KAFKA/KIP-932%3A+Queues+for+Kafka ; Confluent GA note: https://www.confluent.io/blog/kafka-queue-semantics-share-consumer-ga/
- Redpanda K8s requirements: https://docs.redpanda.com/current/deploy/redpanda/kubernetes/k-requirements/ ; Strimzi docs: https://strimzi.io/docs/operators/latest/deploying
- Redis licensing: https://redis.io/legal/licenses/ ; XAUTOCLAIM pattern: https://redis.antirez.com/fundamental/streams-consumer-patterns.html
- etcd limits: https://etcd.io/docs/v3.3/dev-guide/limit/ ; https://learnkube.com/etcd-breaks-at-scale
- CloudNativePG releases: https://github.com/cloudnative-pg/cloudnative-pg/releases
- Redis operators: https://github.com/ot-container-kit/redis-operator ; https://github.com/freshworks-oss/redis-operator
- JetStream memory behaviour: https://www.synadia.com/blog/nats-jetstream-high-ram-usage ; https://github.com/nats-io/nats-server/issues/5870
- DeepWiki Q&A on nats-io/nats-server, nats-io/nats.go, kedacore/keda, riverqueue/river (2026-09-18)
