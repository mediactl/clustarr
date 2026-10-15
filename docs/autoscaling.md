# Autoscaling

The queue-driven agents scale on the depth of their JetStream work queues, from zero, with no
KEDA and no Prometheus in the path: the manager serves the one metric itself, over the
Kubernetes External Metrics API, and owns the HorizontalPodAutoscalers that read it (ADR-0018;
`docs/superpowers/specs/2026-10-06-manager-agent-split-design.md` §9). This page is the
operator's view of how that works, what it costs, and how to turn it off.

## What scales

| Deployment | Scaling | Range | Why |
| --- | --- | --- | --- |
| `agent-catalog` | HPA | 0..1 | Its search consumers are throttled downstream (below). |
| `agent-events` | HPA | 1..N | The RSS matcher reads `CLUSTARR_RELEASES`, a limits stream that is memory with discard-old on single-node NATS, so it cannot buffer through zero. |
| `agent-import` | HPA | 0..N | Scans, file imports, list syncs, the recycle sweep and both probe lanes; a `ProbeVersion` raise's backlog scales it out. |
| `agent-caption` | HPA | 0..1 | Its fetch consumers are throttled downstream (below). |
| `markers` | HPA | 0..N, one per node | Segment analysis (`markers-analyze`), bound by a node's CPUs and its reads from `/data`. |
| `manager` | fixed | 1 | Leader-elected; it is the autoscaler. |
| `agent-metadata` | fixed | 1, `Recreate` | RPC responders and in-process provider limiters (ADR-0007). |
| `agent-index` | fixed | 1, `Recreate` | The release index, the RPC responders, the facade and the per-host limiter are one process. |
| `ui` | fixed | `ui.replicas` | Request-driven. |
| `<client>-engine` | fixed | `DownloadClient.spec.replicas` | Rendered by the manager's DownloadClient controller. |
| transcode pools | the manager | per profile and class | Sized by the transcode admission ledger, not by an HPA. |

N is the number of Ready, schedulable nodes the Deployment's pod template can run on
(`app/autoscale/controller.MatchingNodes`: node selector, required affinity, tolerations and
any extended resource the pod requests), clamped by each consumer's `MaxAckPending` over its
per-pod slots.

**Why `catalog` and `caption` stop at 1.** Their throttled consumers
(`pkg/agentdomain.Throttled`: `catalog-search-high`, `catalog-search-normal`,
`caption-fetch-high`, `caption-fetch-normal`) keep `MaxAckPending` at one pod's slots, because
the work's real limit is downstream: the index agent's per-host limiter (every query waits its
indexer's `requestDelay` inside a 45 s budget) and the shared subtitle-provider token bucket
(`clustarr-provider-throttle`). More replicas would add pods the limiter cannot feed, and for
search would turn a sweep's queue into `paced` skips that cost an item its live search (OD46).

## The metric

`clustarr_consumer_lag{stream,consumer}` = `NumPending + NumAckPending`, read per durable from
JetStream's `$JS.API.CONSUMER.INFO.<stream>.<durable>` (`events.StreamAdmin.ConsumerState`):

- `NumPending` is the messages matching the durable's filters after its delivered sequence. A
  scheduled hold (`WithScheduleAt`, on a `.sched.` subject no filter matches) counts only once
  it fires, so a delay-profile grab wakes its domain when it is due, not when it is queued.
- `NumAckPending` is delivered and unsettled: running in a handler, waiting out a delayed nak,
  or lapsed and waiting for redelivery. That is what wakes a domain at zero for a retry, and
  what keeps the last pod while it holds work. It also means a domain whose work sits in a long
  backoff stays at one replica until the backoff ends (OD27); turning long retries into
  scheduled republishes that are not lag (S4) is deferred.
- Neither counts a message past `MaxDeliver` that waits for its dead-letter copy, nor
  `NumRedelivered` (a subset) or `NumWaiting` (idle pull requests, the inverse of demand).

Only the consumer's leader answers `CONSUMER.INFO`, so the reading is right on a 3-node R3
cluster by construction; it rides the manager's NATS connection, needs no monitoring port, and
costs 897 bytes and 0.23-0.83 ms per durable (measured on kind-cluster-plex, 2026-10-07). A
member that answers with a placement-less config, or a leader election in progress, reads as
unavailable (that series answers 503 and the HPA holds its scale-down), never as zero.

**Why not `/jsz`.** Its `num_pending` is authoritative only on the consumer leader, so a
follower reads 0: on R3, a read that lands on a follower with no pods running would leave a
domain asleep while its leader holds thousands of tasks (kind runs one server, the leader of
everything, so every e2e would pass). Port 8222 is unauthenticated. An `acc` that names no
account answers HTTP 200 with no data at all. And it is a whole-account dump: 45,913 bytes and
about 6 ms for 26 streams and 45 consumers, against 897 bytes per `CONSUMER.INFO`.

**Why not a stream's message count.** It counts future and dead work and, on a limits stream,
retention. Measured the same day: the markers work stream held 18,897 messages against a lag
of 16,563, the gap being exactly the 2,334 TheIntroDB tasks held on scheduled subjects;
`CLUSTARR_RELEASES` held 10,137 against a lag of 0, its 72 h retention window. And one stream
can feed two domains.

No NATS-native metrics adapter exists: nothing in nats-io serves `external.metrics.k8s.io` or
`custom.metrics.k8s.io`, and nats-server has no `/metrics` route. Every published route to an
HPA goes through Prometheus or KEDA, which is why the manager serves the API itself.

The manager's leader also exports the readings as Prometheus gauges every 30 s
(`app/autoscale/extmetrics.QueueGauge`): `clustarr_work_queue_pending` (the lag),
`clustarr_consumer_pending`, `clustarr_consumer_ack_pending`, `clustarr_consumer_waiting`,
`clustarr_consumer_max_ack_pending` and `clustarr_stream_fill_ratio`
(`docs/observability.md`). One 5 s cache with a singleflight serves the gauge, the API and the
dispatch ledger.

## Dispatch admission is not a scaling input

The manager admits every task it publishes to an agent (`app/dispatch`, ADR-0019): a durable
takes at most a budget of 2 × `MaxAckPending` outstanding dispatches -- one full fleet in flight
and one queued behind it -- and a planner whose dispatch is refused renders
`delivery.state: waiting` with a reason on its CR (`Budget`, `Paced`, `NoAgent`,
`NoCapableAgent`, `EngineNotReady`, `Rebuilding`). The work the ledger holds back is counted in
`clustarr_dispatch_waiting{consumer}`, and `clustarr_dispatch_unattended{consumer}` is 1 while a
durable has work and no agent taking it (no pull open and no progress for two minutes, or no
agent of its domain present). Neither is an HPA metric: the HPA sees only work an agent can
take, which is already on the stream. Waiting work becomes lag as the budget frees, and the
lag then scales the domain. A durable that reads unattended at zero replicas is waiting for its
HPA to wake it, which the lag already on the stream does.

## The External Metrics API

The manager serves `external.metrics.k8s.io/v1beta1` itself (`app/autoscale/extmetrics`,
hand-rolled on `net/http` and `crypto/tls`), on `:6443` on every manager replica, started
before the caches sync, behind the Service `external-metrics` (port 443,
`publishNotReadyAddresses: true`), so the API does not wait out a cache sync after every
manager restart. It serves one metric, `clustarr_consumer_lag`, selected by its `stream` and
`consumer` labels.

Authentication is front-proxy mTLS checked against
`kube-system/extension-apiserver-authentication`: a request without a verified client
certificate and identity is 401, one whose certificate CN is not an allowed front-proxy name is
403. There is no SubjectAccessReview, so `system:auth-delegator` is not bound. The manager
generates its own CA and serving certificate and keeps them in the Secret named by
`--external-metrics-secret` (`CLUSTARR_EXTERNAL_METRICS_SECRET`; the chart's
`<release>-external-metrics-tls`, kustomize's `external-metrics-tls`).

## Who owns what

| Object | Owner |
| --- | --- |
| APIService `v1beta1.external.metrics.k8s.io` | the installers render it without `caBundle`; the manager writes only `spec.caBundle`, as `clustarr-autoscale` (`extmetrics.CertManager`) |
| Service `external-metrics` | the installers |
| RoleBinding in `kube-system` to `extension-apiserver-authentication-reader` | the installers |
| The TLS Secret | the manager (create-only, renewed by a resourceVersion-carrying update) |
| One HPA per Deployment labelled `autoscale.clustarr.io/domain` | the manager's autoscale reconciler, as `clustarr-autoscale`, owned by its Deployment |
| The autoscaled Deployments' `replicas` | the HPA: the installers render no `replicas` and no HPA, since Helm's server-side apply would fight the reconciler |

## The HPA

- `minReplicas`: 0, or 1 for `events`; the annotation `autoscale.clustarr.io/min-replicas`
  raises it, never lowers it.
- `maxReplicas`: `max(minReplicas, max(1, min(matching nodes, floor(MaxAckPending / slots) over
  the domain's consumers)))`.
- One External metric per consumer of the domain (`pkg/agentdomain.Domains()`), at
  `AverageValue` = that consumer's per-pod slots.
- A per-node domain (`agentdomain.Domain.PerNode`: markers) targets `AverageValue` 1 instead,
  whatever its slots: a backlog of at least as many tasks as matching nodes runs a pod on every
  node, a smaller one that many pods, and none none. Its durables' `MaxAckPending` is
  `AutoscaleReplicaCeiling × PerNodeSlotsCeiling` (64), so the clamp above does not bind below
  8 nodes at 8 slots and `maxReplicas` is the matching nodes; when it does bind, the Event
  `PerNodeClamped` says so. Its installers spread its pods by hostname with `DoNotSchedule`
  over the nodes they tolerate (`nodeTaintsPolicy: Honor`), so each node runs one; `maxSkew` 1
  still fits a rollout's surge pod. Until 2026-10-15 markers targeted its slots (3 a pod) with
  a `MaxAckPending` of 8, which held three amd64 nodes to two pods, both often on one node.
  A per-node pod without a slot override sizes its own slots from its node (Slots, below),
  which the reconciler cannot see, so its clamp counts such a pod at `PerNodeSlotsCeiling` (8).
- Scale-up immediately, by up to `maxReplicas` pods per 15 s; scale-down after
  `max(300 s, the longest AckWait of the domain's consumers)`.
- `autoscale.clustarr.io/paused: "true"` on a Deployment makes the reconciler delete its HPA and
  leave its replicas alone: the supported way to pause an agent.
- A Deployment someone scaled to zero by hand, which its HPA would never wake (an HPA wakes only
  a workload it scaled to zero itself), is scaled back to one through its scale subresource.

## Slots

A consumer's per-pod slots -- how many handlers one pod runs -- come from its
`events.ConsumerSpec.Slots` in the topology. `CLUSTARR_CONSUMER_SLOTS` overrides them per
Deployment, for example `CLUSTARR_CONSUMER_SLOTS=markers-analyze=3,import-fileimport=8` (chart:
`agents.<domain>.consumerSlots`, `markers.consumerSlots`), and the reconciler reads the same
variable from the pod template as the HPA's per-pod target, so the pods and the HPA agree. A
consumer's `MaxAckPending` is the topology's, sized for the domain's ceiling times its slots,
and only the manager writes it.

A per-node domain's consumer that the variable does not name has automatic slots
(`agentdomain.Domain.AutoSlots`, since 2026-10-15): each pod runs
`min(max(1, GOMAXPROCS / coresPerSlot), PerNodeSlotsCeiling)`, where GOMAXPROCS is the CPUs the
pod can use (the cgroup's CPU limit, rounded up, else the node's) and `coresPerSlot` is markers'
`--cores-per-slot` (chart `markers.coresPerSlot`, default 2). At 2 a 4-CPU node runs two
analyses and a 16-CPU node eight; at 1, for a cluster whose reads from `/data` bind first, four
and eight. The installers set markers no CPU limit, so the slots follow the node, and a memory
limit that holds eight slots (4Gi: 256 MiB a pod plus 448 MiB a slot, measured on 4K HEVC
Main 10 under the 128 KiB mmap threshold the pod's `GLIBC_TUNABLES` fixes). Each pod reports the slots it runs in its presence record; nothing in the manager
reads them as capacity (dispatch budgets by `MaxAckPending`), so pods of one domain may differ.

## Turning it off

`autoscaling.enabled: false` (Helm) renders fixed replicas (`agents.<domain>.replicas`,
`markers.replicas`), passes `--autoscale=false` to the manager and renders no APIService; the
manager's leader then deletes every HPA a previous release labelled. Use it where KEDA or
prometheus-adapter already holds `v1beta1.external.metrics.k8s.io`: only one APIService may hold
the group, this chart does not run beside another, and Helm refuses to adopt an APIService it
does not own, so the conflict is a loud install error.

## Failure modes

| Failure | Effect | Handling |
| --- | --- | --- |
| Manager down or crash-looping | HPAs read `FailedGetExternalMetric` and hold their scale; agents at 0 stay at 0. The APIService is unavailable, so namespace deletion anywhere in the cluster stalls until it returns | the reconcilers are down too; restore the manager |
| The release namespace is deleted | the manager and its Service go and the cluster-scoped APIService stays: without handling, a deadlock | `NamespaceGuard` deletes the APIService on the manager's way out; see [A namespace stuck Terminating](#a-namespace-stuck-terminating) |
| Manager restart | the API answers as soon as it listens, before the caches sync | -- |
| NATS down | 503, HPAs hold | agents could not work anyway |
| NATS restarted (single node: memory-backed streams, consumers and buckets gone) | `ConsumerState` is not-found, the API answers 503 and every HPA holds; running subscriptions stop fetching; an agent that restarts meanwhile waits for the topology | `busconn.KeepTopology` re-ensures it on reconnect and every 60 s while anything is missing; subscriptions re-bind; queued work in memory streams is lost |
| Another external-metrics provider | one APIService per group per cluster | Helm refuses to adopt it; set `autoscaling.enabled=false`. A kustomize overwrite is caught by CertManager's `spec.service` check (`ExternalMetricsAPIServiceForeign` Event) |
| `HPAScaleToZero` off | `minReplicas: 0` refused | re-applied with 1 and the Event `ScaleToZeroUnavailable` |
| No node matches the pod template | `maxReplicas` 1, the pod Pending | the Event `NoMatchingNodes` |
| Slots above `MaxAckPending` | no second replica could be fed | the Event `SlotsExceedMaxAckPending` |
| A per-node domain on more nodes than its `MaxAckPending` feeds at its slots | `maxReplicas` below the matching nodes | the Event `PerNodeClamped` |
| A consumer leader election (R3) | `CONSUMER.INFO` times out or answers `JSClusterNotAvail` for the election's seconds | that series is a 503: the HPA scales up on the others and holds any scale-down |
| Consumer assigned, Raft node not yet up | a member answers with the config and zero state | read as unavailable, not zero; the scale-down window absorbs it anyway |
| NATS monitoring port closed | none | nothing reads it |
| A durable expires while its domain is at zero | its metric would be not-found for good | no consumer sets `InactiveThreshold` |
| A durable stalls at `MaxAckPending` behind long delayed naks | lag at the cap, `waiting > 0`, nothing delivered; the HPA keeps pods that receive nothing | a diagnostic: `clustarr_consumer_ack_pending == clustarr_consumer_max_ack_pending` |
| Every slot of a pod held by handlers deaf to their context | the pod fetches nothing; queued work stays pending for other replicas | the `bus` liveness check restarts the pod once it has sat at the lapsed cap past the handler budget |
| A single-node memory stream fills and discards its oldest | silent; lag cannot show it | `clustarr_stream_fill_ratio{stream}`; alert at 0.8 |

## A namespace stuck Terminating

If the release namespace was deleted while the manager was down, the APIService is left
pointing at a Service that no longer exists, and every namespace deletion in the cluster stalls
on the aggregated discovery error. Delete it by hand:

```sh
kubectl delete apiservice v1beta1.external.metrics.k8s.io
```

While the manager runs, its `NamespaceGuard` deletes the APIService itself when it sees its own
namespace terminating.
