# Usenet engine fleet: shared connection pools, bandwidth budgets, late binding and scaling

**Date:** 2026-11-27
**Status:** Built 2026-11-28 on the owner's word ("Begin the phases,
forgetting about the blocknews setup for now"), phases 1 to 4 and the
grant-sizing half of 5; **§13 is what was built and where it differs from
§§4-11**. Not deployed, not proven on kind.
**Supersedes:** §6 ("Several pods, phase 2") of
`2026-10-16-usenet-connection-ledger-design.md`, which it carries out.

The owner, 2026-11-27: "Per pod, we will be limited to the node's NIC
speed. Too many concurrent downloads + streams on one node will just
throttle the other downloads on that node. We should support a total
connection pool on frugal of 100 - split between the us and eu servers -
which can be configured as fallbacks sharing a single block of connections
which must be shared among all usenet-engine pods. ... When scheduling, we
will ideally schedule based on a 'bandwidth' request/limit of sorts per
node and we should also include queue based autoscaling. ... nats.io should
lock a download to the initial pod so that it can re-access the required
local storage in the event of a crash, items get distributed evenly to the
pods. ... our network's bandwidth limit (public internet) is 5Gi so we
should not schedule higher expected download streams than say 80% of 5G."

## 1. Where we are

One usenet engine pod, by four separate pins: a CEL rule
(`replicas == 1`), a `Recreate` Deployment that ignores `spec.replicas`,
`lifecycle.ChooseOrdinal` forcing ordinal 0, and the fixed argument
`--engine=<client>-0` (`app/grab/controller/downloadclient/workload.go`).
On talos that pod sits on talos-88 (2.5 Gbit, NVMe scratch) and moves
about 245 MB/s (about 2 Gbit/s) on 114 workers: about 2.1 MB/s, 17 Mbit/s,
a connection.

What already exists and is kept:

- **The torrent engine is already a fleet.** A StatefulSet, one durable
  consumer per ordinal on the work-queue stream `CLUSTARR_WORK_ENGINE`
  (`grab-engine-<client>-<ordinal>`, filter
  `clustarr.work.engine.<client>.<ordinal>.>`), the entry pinned to its
  engine (`status.downloads[].engine`, `dlindex.PinnedTo`), per-ordinal
  scratch claims (`scratchPerReplica`), durables swept only when nothing
  is pinned to them. A download's commands can reach no other pod, and a
  pod that comes back with the same ordinal and the same volume finds its
  unacked commands and its manifests. **That is the lock the owner asks
  NATS for; usenet joins it rather than getting a second mechanism.**
- **The connection ledger** (`clustarr-usenet-connections`, one
  compare-and-swap entry per account, grants renewed every 30 s) already
  makes every pod on an account share its cap.
- **Engine records** (`clustarr-engines`) and **transfer records**
  (`clustarr-transfers`) are how an engine answers; the manager is the
  only router (ADR-0019).

What is missing:

1. A budget shared by two **hosts** (the ledger's key is
   `host:port:username`, so `news.` and `eunews.` are two budgets).
2. Any division of a budget **between pods**: a share is
   `limit / maxActive` of the pod's own `maxActive`, so the first pod to
   start eight transfers holds every connection and the second parks.
3. Any notion of **bandwidth**: nothing measures a pod's rate
   (`EngineRecord` has none, the five `clustarr_download_*` metrics are
   registered and never recorded), nothing limits it
   (`downloadLimitBps` is declared and not plumbed), and the manager reads
   no network fact from a Node.
4. **Capacity-aware assignment**: `ChooseOrdinal` assigns every entry at
   once to the engine with the fewest active + queued. Five hundred grabs
   become five hundred pinned transfers, divided by count, and one pinned
   to a slow pod cannot move.
5. **Durable quotas**: `quotaBytes` is counted per process and forgotten
   at a restart, which is useless for a 300 GB block.

## 2. What was measured (2026-11-27, talos, one pod on talos-88)

- **Frugal enforces no connection cap** that 300 connections reach: 260
  on `news.` alone, and 150 on `news.` with 150 on `eunews.`, every one
  fetching eight articles at once, were all served (2,400 `222`s, no
  refusal). Frugal's public pages say only that every regional server
  takes the same login; its newsreader guide is behind the billing login
  and was not read. **The cap is a term of the plan, so the ledger is the
  only thing that holds us to it.**
- **`eunews.` has not held what `news.` lacks.** A download that failed
  as missing: 0 of 200 sampled articles on each; `bonus.` had 147 of 200.
  One post only. Both hosts are one operator's networks
  (`2607:bc40:1:119::/64` and `2607:bc40:2:119::/64`).
- **`block.blocknews.net` does not resolve.** Blocknews answers on
  `usnews.blocknews.net` and `eunews.blocknews.net`, in the same two
  networks as Frugal's own hosts: more connections and more data on the
  same store, probably no different articles. No login yet.
- **Capacity.** Primaries: frugal 100, easynews 30, newsdemon 50: 180
  connections, about 3.1 Gbit/s at 17 Mbit/s each. The public link's
  budget is 4 Gbit/s (80% of 5). Schedulable amd64 nodes: talos-88
  (2.5 Gbit) and talos-b8 (1 Gbit, a 1 TB local disk); talos-9c is
  unreachable; the three control planes are Pi 5s and stay out.
  **The nodes are the ceiling, not the providers**, and at the owner's
  example fractions (limit = half the link) the fleet would be allowed
  1.75 Gbit/s, less than the one pod moves today (§5.2).

## 3. Shape

```
                       manager (only router, only writer)
   Node annotations ─▶ fleet controller ──▶ StatefulSet <client>-engine (replicas from backlog)
                        │        │
                        │        └─▶ KV clustarr-engine-budgets   budget.<client>.<ordinal>
                        ▼
   Pending entries ─▶ transfer admission (class, age) ─▶ engine with room
                        │  clustarr.work.engine.<client>.<ordinal>.<entry>   (pinned)
                        ▼
   engine pod 0 (talos-88)        engine pod 1 (talos-b8)        ...
     own scratch volume             own scratch volume
     token bucket = its budget      token bucket = its budget
         └──────── KV clustarr-usenet-connections ────────┘
                   pool.<name>: grants[], members[], usage
```

Five parts, each useful without the next, in the order they would ship
(§10).

## 4. Connection pools

### 4.1 API

```yaml
spec:
  usenet:
    connectionPools:            # MaxItems 8
      - name: frugal
        connections: 100
    providers:
      - {name: frugal,       host: news.frugalusenet.com,   pool: frugal, priority: 1}
      - {name: easynews,     host: news.easynews.com,       connections: 30, priority: 2}
      - {name: newsdemon,    host: news.newsdemon.com,      connections: 50, priority: 3}
      - {name: frugal-eu,    host: eunews.frugalusenet.com, pool: frugal, connections: 25,
         backup: true, priority: 4}
      - {name: frugal-bonus, host: bonus.frugalusenet.com,  connections: 20, backup: true,
         priority: 5, quotaBytes: 1000G, quotaPeriod: Month}
      - {name: blocknews,    host: usnews.blocknews.net,    connections: 20, backup: true,
         priority: 6, quotaBytes: 300G, quotaPeriod: Never}
```

- `NNTPProvider.pool` names an entry of `connectionPools`. The pool's
  `connections` is the cap on its members together; a member's own
  `connections`, when set, is the most that one host may hold of it
  (`frugal-eu`: 25 of the 100), and unset means the whole pool.
- CEL: a named pool exists; a pool's members share one `secretRef`
  (a pool is one account).
- `quotaPeriod`: `Month` (the calendar month, UTC; today's meaning of
  `quotaBytes`) or `Never` (a block: counted until it is gone).

### 4.2 Ledger

- `Provider.Account()` for a pooled provider is
  `pool:<pool name>:<username>`, so every member, every pod and every
  DownloadClient naming that pool on that login shares one entry. An
  unpooled provider keeps `host:port:username`.
- `Grant` gains `Host`. `Take` is given two limits, the pool's and the
  host's, and grants `min(want, pool − held, host − held on that host)`.
  `Held` and the "frugal 100/100 in use" message read the pool.
- **Members** (§6.2) and **usage** (§4.4) live in the same entry, written
  by the same compare-and-swap at the same 30 s renewal: no new bucket,
  no new writer.

### 4.3 What the EU host is for

A backup in the pool, with one new rule: **a backup that shares a pool
with a primary stands in for that primary while it cannot be reached**
(connect or login failures, the pool's existing penalty), as a primary
does, for whole transfers. Otherwise it is asked, as backups are, for the
few articles the primaries lack, under the 2026-11-27 rule that a backup
never supplies the bulk of a download.

Not proposed: making `eunews.` a second primary. It would split the same
100 connections across a slower path (41 MB/s against 94 MB/s on 110
connections from talos-88) for the same articles.

### 4.4 Quotas that survive a restart

The entry carries `usage: {periodStart, bytes}`. Each pod adds the wire
bytes it read since its last renewal; a `Month` quota resets when the
month turns; a provider whose usage has reached its quota is skipped by
every pod. `DownloadClient.status` reports each metered provider's used
and remaining bytes, and a condition warns at 90%.

## 5. Bandwidth budgets

### 5.1 Inputs

| Input | Where | Default |
| --- | --- | --- |
| A node's link speed | Node annotation `download.clustarr.io/link-speed` (bits/s, a quantity: `2500M`) | `bandwidth.defaultLinkSpeed`, `1G` |
| A node's own request / limit | Node annotations `download.clustarr.io/bandwidth-request`, `.../bandwidth-limit` | the percentages below |
| Request, as a share of the link | `spec.usenet.bandwidth.requestPercent` | 25 |
| Limit, as a share of the link | `spec.usenet.bandwidth.limitPercent` | 50 |
| The fleet's total | `spec.usenet.bandwidth.total` | unset: no cap. talos: `4G` |
| What one transfer is worth | `spec.usenet.bandwidth.perTransfer` | `250M` |

Rates are `resource.Quantity` in bits per second (no floats in `api/`).
The manager already holds `nodes` get/list/watch. An engine learns its
node from the downward API (`NODE_NAME`) and says so in its engine record.

### 5.2 The budget

For each engine the manager computes, every 15 s and on any change:

- `request` and `limit` from its node (annotation, else percentage).
- `budget` = `request`, plus an equal-proportion share of what is left of
  `total` after every engaged engine's request, capped at `limit`.
  Engines with nothing to do count for nothing, so their share goes to
  the busy ones.
- `transfers` = `clamp(round(budget / perTransfer), 1, maxActive)`.
- `weight` = `budget` (the pod's claim on every connection pool, §6.2).

It writes `{budget, limit, transfers, weight, seq}` to
`clustarr-engine-budgets` under `budget.<client>.<ordinal>` (a new bucket,
manager-written, no TTL). The engine reads it at start and watches it; an
engine with no budget record runs as it does today, on `maxActive`.

On talos with the example fractions: talos-88 625M / 1.25G, 5 transfers;
talos-b8 250M / 500M, 2 transfers; total 1.75 Gbit/s. **That is less than
today's 2 Gbit/s from talos-88 alone.** Recommended for talos: annotate
talos-88 with `bandwidth-limit: 2G` (what it already carries without
harm) and leave talos-b8 on the percentages: 2.5 Gbit/s, 10 transfers.
Reaching the providers' 3.1 Gbit/s needs a third node.

### 5.3 Enforcement

A token bucket in the engine on article reads (`Config.Limiter`, injected
per the `pkg/` convention; `golang.org/x/time/rate`), set to `budget`.
Reading slower is TCP back-pressure: no CNI feature, no shaping on the
node. The engine reports its measured rate (bytes/s over 10 s) and its
node in the engine record, and the five registered `clustarr_download_*`
metrics start being recorded, plus `clustarr_usenet_connections_held` and
`clustarr_usenet_engine_budget_bits_per_second`, labelled by client and
engine only.

### 5.4 Other traffic on the node (later, optional)

The owner's example subtracts what the node already carries ("using an
average of say 100Mb/s"). A pod cannot see its host's counters, and the
routes that can (`nodes/proxy` to the kubelet, a hostPath) are privileges
clustarr should not hold. Two honest options, neither in the first build:

- **Back off on evidence.** The engine knows its throughput per
  connection. While that holds within a fifth of its recent best it climbs
  from `request` toward `budget`; when adding rate lowers it, the path is
  contended (NIC, public link or NAS) and it steps back. No dependency.
- **Ask a metrics store.** A manager flag naming a Prometheus-compatible
  query for a node's receive rate; the budget is then also capped at
  `0.9 × link − other traffic`. talos has VictoriaMetrics; most installs
  have nothing.

## 6. The fleet

### 6.1 Workload

The usenet engine becomes what the torrent engine is: a StatefulSet,
`podManagementPolicy: Parallel`, required pod anti-affinity on
`kubernetes.io/hostname` (one engine a node), identity from `POD_NAME`
(`EngineIdentity`), the per-ordinal durable that already exists. The CEL
rule, the hard-coded `1`s and `ChooseOrdinal`'s usenet case go.

Scratch is per engine:

- `storageClassName` (a node-local class): `volumeClaimTemplates`, as
  torrent's `scratchPerReplica`. The claim's node affinity keeps ordinal
  *k* on its node for good, which is what brings a crashed pod back to
  its transfers.
- `path` on the shared volume: `<path>/<engine>`, and `reattach` reads
  only its own directory.
- `existingClaim` / `volumeName`: valid only with one replica (CEL).

Publishing from a local volume to `publishDir` is already a copy
(`MaxPublish`); nothing changes.

talos cutover (a runbook, no migration code): drain the queue, bind the
NVMe PersistentVolume to `scratch-usenet-engine-0` and a local-path
volume on talos-b8 to `scratch-usenet-engine-1`, so the big node is
ordinal 0 and the last to be scaled away.

### 6.2 Dividing a pool between pods

Each pod writes itself into every pool entry it uses:
`members[pod] = {weight, expires}`, `weight` from its budget record and
zero while it has no transfer running or waiting. A pod's entitlement on
a pool is `connections × weight / Σ live weights`, at least one, and its
shares are computed from the entitlement instead of the whole limit
(`transferBudgets`, `primaryShares`, with `transfers` in place of
`maxActive`). Two busy pods of 2G and 500M hold 80 and 20 of frugal's
100; when one goes idle the other's entitlement is the whole pool at its
next renewal.

Agents couple through NATS only: the division needs no manager in the
path and survives a manager restart.

### 6.3 Losing a pod or a node

- Pod crash, node reboot: the same ordinal returns to the same volume;
  `reattach` resumes; unacked commands redeliver to its durable. Nothing
  moves. Its grants lapse after 90 s and are taken again.
- Node gone for good: after `EngineTeardownTimeout` the operator scales
  the ordinal away (or the autoscaler does, §8); `engineGone` re-adds its
  entries elsewhere, as it does for torrents today. The work on the lost
  disk is lost; nothing can be done about that.

## 7. Assignment: late binding

An entry is assigned when an engine has room for it, not when it is
grabbed.

- An engine has room while the entries pinned to it and not finished
  number fewer than its `transfers` plus a look-ahead of 2 (so its next
  .nzb is fetched and parsed before a slot frees).
- Among engines with room the entry goes to the one with the lowest
  `pinned / transfers`; ties to the lowest ordinal. Equal fill by
  fraction: the 1 Gbit pods are full after 2 each, the rest go to the
  2.5 Gbit pod, as the owner describes.
- With no room it stays `Pending`, message "waiting for a free transfer
  slot", and is woken when one frees.
- Order across items is the engine's own today (`jobAhead`: priority
  class, then oldest). The waiters are kept in that order in a slots book
  in `app/dispatch` beside the command ledger, rebuilt from
  `status.downloads[]` at start like it, counted from the manager's own
  assignments (`dlindex.PinnedTo`), so a stale engine record cannot
  over-assign.

`ChooseOrdinal` stays a pure function of that view. Torrent clients keep
today's rule (their `maxActive` is 200; they are not bandwidth-bound in
the same way).

**A consequence to accept:** a waiting entry's .nzb is fetched when it is
assigned, not when it is grabbed. With a long queue that is hours later.
It spreads indexer grabs out, which the indexers prefer, but a release
taken down in between fails at assignment instead of at grab.

## 8. Scaling

The DownloadClient controller sets the StatefulSet's replicas itself;
**no HPA**. An HPA cuts replicas on a metric and would delete an ordinal
that holds transfers on its own disk; the transcode pools are sized by
their admission ledger for the same reason.

```yaml
spec:
  usenet:
    autoscale: {minReplicas: 1, maxReplicas: 4}   # unset: spec.replicas, fixed
```

- **Up:** while entries wait for a slot and every engaged engine is full,
  add one, up to `maxReplicas` and to the nodes that match the pod
  (`autoscale.MatchingNodes` already answers that).
- **Down:** the highest ordinal is first given nothing new (draining),
  and removed once nothing is pinned to it for 10 minutes.
- `minReplicas: 0` is allowed: an idle fleet is no pods.

## 9. What NATS gives, and what is used

| Feature (server 2.15, client 1.56) | Fit | Used |
| --- | --- | --- |
| Work-queue stream, one filtered durable per pod | Pins a download's commands to one pod across crashes | Yes, already (torrent); usenet joins |
| KV compare-and-swap, per-key TTL | Shared budgets, membership, quotas | Yes (the ledger, extended) |
| KV watch | Budgets pushed to engines | Yes (new bucket) |
| Message schedules | Delayed re-offers | Already used for holds; nothing new |
| Priority group `pinned_client` | One active consumer with standbys | No: it pins a *consumer*, not a message; a StatefulSet ordinal is already single |
| Priority groups `overflow`, `prioritized` | Pods pull from one shared queue, the spare ones only when it backs up | No, see below |
| Counter streams | Durable shared counters (quota bytes) | No: the ledger entry already is one, with one writer path |

**The pull model, considered and not chosen.** Publish every add to one
shared subject; each pod pulls when it has a free slot, with `prioritized`
pulls so the pod with the most spare bandwidth is served first and
`overflow` so small nodes pull only past a backlog; the puller claims the
entry in its transfer record. It balances by itself and needs no slots
book. It loses three things: order (a work queue is first-in first-out, so
priority class and age would need a queue each); the single router
(ADR-0019: every entry's `engine`, sequence numbers, dedup ids and
republishing are addressed to an engine the manager chose; a claim would
be a second protocol beside it); and determinism in tests (`ChooseOrdinal`
is a pure function; a race between pullers is not). The manager already
knows every pod's budget and every assignment, so it can do the same
balancing exactly.

## 10. Build order

Each phase ends proven on kind (`test/e2e`), then on talos.

1. **Pools and quotas.** `connectionPools`, `pool`, `quotaPeriod`; the
   ledger's pool key, host limit and usage; the stand-in rule. One pod.
   talos gets the EU host, the pool of 100 and the block.
2. **Fleet.** StatefulSet, identity, per-engine scratch, members and
   entitlements, the pins removed. talos: two engines, fixed replicas.
3. **Budgets.** Node annotations, the budget bucket, the token bucket,
   rates in the engine record, the metrics.
4. **Late binding and scaling.** The slots book, `ChooseOrdinal` on
   fill, controller-set replicas with draining.
5. **Work-conserving grants** (the ledger spec's deferred "growing a
   running transfer's grant"): at each renewal a pod re-divides its
   entitlement among the transfers it is actually running, shrinking
   first, so three transfers use what eight would; and a transfer may
   hold grants on several primaries at once. This is what makes a short
   queue as fast as a long one.

## 11. Tests

- `ledger_test.go`, `kvledger`: a pool caps two hosts together; a host
  limit holds inside it; two members' entitlements sum to the pool and
  follow their weights; an idle member's weight is zero; usage adds
  across pods, resets on the month, never resets for `Never`.
- Stub NNTP server: the EU host carries a transfer only while the US one
  refuses connections; a metered provider is skipped by a second client
  after the first spends the quota.
- `lifecycle`: assignment by fill, ties, no room leaves `Pending`, order
  by class then age across items, the book rebuilt from status.
- Fleet controller (envtest, `librarytest.NewManager`): budgets from
  annotations, the total's cap, replicas up on waiters, draining before
  down, durables swept only when unpinned.
- Engine: the token bucket holds a transfer to its budget within 5% over
  30 s; a budget change is applied without a restart.
- e2e: two engines on two kind nodes, one killed mid-transfer, the
  download resumes on the same ordinal and completes.

Every test builds its input through the real producer and is falsified
once (the rule reverted, the test seen to fail).

## 12. Open questions for the owner

1. **talos-88's limit.** Half its link is 1.25 Gbit/s, below today's
   2 Gbit/s. Recommended: annotate it `bandwidth-limit: 2G`.
2. **Blocknews.** Which host (`usnews.` or `eunews.`), and its login:
   Frugal's FAQ says a redemption code arrives by e-mail after the yearly
   payment. It needs its own Secret through 1Password.
3. **Frugal's newsreader guide** (behind the billing login): if it states
   a number per host, `frugal-eu`'s 25 follows it.
4. **The .nzb is fetched at assignment** (§7). Acceptable?
5. **Other traffic on the node** (§5.4): back-off on evidence, the
   metrics store, or neither for now.
6. **The Pi 5 control planes** stay out of the fleet. Assumed.

## 13. As built (2026-11-28)

The owner, starting the build: "at first, we shouldn't expect much of a
change in our workloads. We can likely keep just the single pod on the
nuc15pro." So everything a fleet needs is opt-in, and one engine on the
volume it has stays what it is.

**What switches the new behaviour on.** A usenet client is a *fleet* when
`spec.replicas` is above one or `usenet.autoscale` is set; it *binds late*
when it is a fleet or sets `usenet.bandwidth`. A client that is neither is
assigned and queued exactly as before. What changes for such a client all
the same:

- Its engine is a StatefulSet of one (pod `<client>-engine-0`), not a
  Deployment. It is still told `--engine=<client>-0`, so an engine image a
  release behind the manager starts; only a fleet's engines read their
  ordinal from `$POD_NAME` (`--download-client`). The controller deletes
  the Deployment first, in the foreground, and applies the StatefulSet
  only once it is gone (`retireDeployment`), so the two never run on one
  working area. Its scratch claim, emptyDir or path is unchanged.
- A transfer's grant is work-conserving (phase 5, `Config.GrowGrants`,
  which the engine always sets): it asks for its server's whole transfer
  budget divided among the transfers running there and is resized at each
  renewal and whenever a transfer parks for connections.
- A quota is counted in the ledger per account and survives restarts.

**Differences from the design.**

| Design | Built | Why |
| --- | --- | --- |
| §4.2 usage in the pool's ledger entry | its own bucket, `clustarr-usenet-usage`, durable | `clustarr-usenet-connections` is a memory bucket: a block's count would not survive a NATS restart, and a bucket's storage cannot be changed in place |
| §4.1 CEL: a pool's members share one `secretRef` | the controller refuses it (`validatePools`, `InvalidSpec`) | `LocalObjectReference.name` has no length bound, so the rule's estimated cost over 32 x 32 providers passes the apiserver's budget |
| §4.1 `connections` default 8 | no API default; the engine reads unset as 8, or as the whole pool for a pooled provider | a defaulted 8 could not be told from "unset" for a pool's member |
| §5.2 `budget` share "equal-proportion" | in proportion to each engine's room between request and limit; requests alone over the total are scaled down together | needs no iteration and never passes a limit |
| §5.3 token bucket on article reads | paid after each pipelined exchange, with the provider's connection slot held | waiting inside an exchange would run down its I/O deadline |
| §5.3 rate "bytes/s over 10 s" | the average since the engine's last record (60 s) | the record's own cadence |
| §6.1 per-engine scratch for every usenet client | only for a fleet: `storageClassName` gives a claim per ordinal, `path` gives `<path>/$(POD_NAME)`; one engine keeps `<client>-scratch` or its path as it was | the owner's single pod must not move |
| §6.2 weight zero "while it has no transfer running or waiting" | zero while no job holds or waits for a transfer slot | the client's own notion of work |
| §7 slots book in `app/dispatch` | no book: the queue is status. A waiter is an entry Pending on its client with delivery reason `NoSlot`; its place is its priority class and its `grabbedAt`, set when it first waits. Both the stage and the controller read it through `dlindex` (`SlotWaiters`, `SlotHolders`). Only this leader's last minute of assignments is kept in memory | the architect's review (§15): a book seeded by every owner's first pass and kept alive by rechecks does not survive ADR-0025, which removes the pass of every owner at start |
| §7 a waiter "is woken when one frees" | a grab that stops holding a slot wakes the next four waiters; every engine record wakes them too; a waiter sets no timer | the review: a recheck per waiter grows with the queue |
| §5.2 budgets "every 15 s and on any change" | on change only: the engines' records, a node's three annotations, and a signal from the downloads stage when a client's waiters or holders change (`downloadclient.Wake`). A drain alone has a due time | the review: no timer does work that a change can announce |
| §4.4 status reports used bytes | in steps of a hundredth of the quota, at least 1 GiB; `QuotaOK` is judged on the exact count | the review: the live figure is a metric, and status should not be rewritten at every reconcile |
| §8 scale-down "nothing pinned for 10 minutes" | the highest ordinal drains when the engines below it have slots for everything pinned anywhere, and goes once *no entry at all* is pinned to it for 10 minutes | a grab past its transfer still unpacks and publishes from that engine's disk |
| §10.5 a transfer holds grants on several primaries | **not built** | the fetch path scopes a transfer to one server; a limiter per server is its own piece of work |
| §5.4 other traffic on the node | **not built** | the owner has not chosen between the two options |
| Blocknews | **not configured** | the owner: "forgetting about the blocknews setup for now" |

**Where things are.**

- API: `usenet.connectionPools`, `providers[].pool`, `providers[].quotaPeriod`,
  `usenet.bandwidth`, `usenet.autoscale`; status `engine.desiredReplicas`,
  `engine.draining`, `engine.waiting`, `quotas[]`, `budgets[]`, condition
  `QuotaOK` (False from 90% of a quota). Node annotations
  `download.clustarr.io/link-speed`, `/bandwidth-request`, `/bandwidth-limit`.
- Ledger: `pkg/download/usenet/ledger.go` (`GrantRoom`: account, host and pod
  limits; `Entitlement`; `UsageAfter`), `kvledger` (`Join`, `Resize`, `Use`),
  `pkg/download/usenetaccount` (the account key and the usage read the
  manager shares without linking the client).
- Client: `pkg/download/usenet/fleet.go` (membership, `SetBudget`, `rebalance`,
  `standIns`, `syncUsage`), `Config.Limiter`, `Config.GrowGrants`.
- Engine: `app/grab/engine/usenet/budget.go` (`BudgetApplier` watches
  `clustarr-engine-budgets`); the engine record carries `node`,
  `downRateBytesPerSec`, `budgetSeq`.
- Controller: `app/grab/controller/downloadclient/fleet.go` (`nodeLink`,
  `computeBudgets`, `scale`, `quotas`, `retireDeployment`).
- Assignment: `lifecycle.ChooseSlot`, `DeliveryReasonNoSlot`,
  `app/remediation/downloads/slots.go`.
- Metrics: `clustarr_usenet_connections_held`,
  `clustarr_usenet_engine_budget_bits_per_second`; the usenet engine now
  records `clustarr_download_bytes_total`,
  `clustarr_download_speed_bytes_per_second` and `clustarr_downloads_active`.

**Owed.** An e2e scenario (two engines on two kind nodes, one killed
mid-transfer); the talos runbook for a second engine; phase 5's second half;
the settings form's new groups checked in a browser.

## 14. Keeping the .nzb from selection (reviewed and decided, not built)

The owner, 2026-11-28, answering §12's question 4: "When a release is
selected and we will download the NZB, the NZB should be stored somewhere
with persistence in case crashes happen or bugs prevent the first attempt
from succeeding when we suspect everything is fine with the release."

**Today.**

- The .nzb is fetched when the engine handles the add command: an
  `indexerDownload` source through `rpc.index.download`, an `nzbURL` source
  by the engine's own HTTP GET (`app/grab/engine/usenet/payload.go`).
- The index agent keeps what it fetched in the object store
  `clustarr-payloads` (file storage, 3 replicas, 1 GiB, a cache evicted
  oldest first, `MaxAge` 3 days; key `nzb/<indexer uid>/<sha256(guid)[:32]>`;
  `app/indexer/download/kept.go`). A direct `nzbURL` fetch is kept nowhere.
- The engine writes it to `<scratch>/<category>/<id>/source.nzb`; that copy
  goes with the job when the job is removed, and with the disk when the
  disk is lost.
- With late binding (§7) the fetch moves from the grab to the assignment,
  possibly hours later.

**The design, as the architect's review left it (§15).**

1. **Fetch when the entry is added**, not while a delay profile still
   holds the release: the manager owes a prefetch beside the entry; the
   index agent fetches (the indexer's limiter and keys stay with it; a
   plain GET for an `nzbURL`) and answers in a record with the digest and
   size, no body on the bus. A late-binding entry is not assigned before
   its payload is stored; a prefetch the indexer refuses fails the entry
   at once.
2. **One file per entry on the shared data volume**:
   `payloads/<entry uid>.nzb`, written tmp, fsync, rename, its digest
   checked on read. Named by the entry, not by content: one writer, one
   reader, one lifetime. Not the engine's scratch, so it survives a lost
   engine disk. `clustarr-payloads` is not widened: a store evicted oldest
   first cannot hold a pin.
3. **The entry is the pin and the index.** `status.downloads[]` gains
   `payload {digest, size, fetchedAt}` on the entry type all six owner
   kinds share; the manager writes it when the fetcher's record reports
   it. Dropping the entry deletes the file, as an effect of that removal;
   one rare, paced audit of the directory catches what was missed. A key
   derived from indexer and guid is not enough: a newznab .nzb is not
   byte-stable across fetches, so the entry says which bytes it took.
4. **The engine reads the file.** A pinned file that is missing is a
   counted fault, not a quiet second fetch from the indexer.
5. **A retry that spends no grab**, for a failure that is not the
   release's: an engine crash, a lost working area, `writeError`,
   `diskFull`, a connection or login failure, a spent quota, a panic.
   Articles missing on every provider is the release's. Retries are
   capped, with the attempt count and the next try's due time kept on the
   entry, never in memory.
6. **Torrents: no.** A .torrent carries its tracker's passkey.

**Decided by the architect, at the owner's word ("Ask the architect all 3
of those", 2026-11-28).**

*Who writes the file: the import agent.* The index agent parses untrusted
indexer content and is the one replica serving every search and feed; a
mount of the NFS data volume there would give it write access beside the
library and put a hard-mount stall inside the search path.

- The manager publishes one new work task on the import domain's existing
  work stream (`payload`, keyed by entry uid).
- The import agent asks `rpc.index.download` for an indexer source, so the
  indexer's keys, limiter and proxy stay with the index agent; that call
  already leaves the payload in `clustarr-payloads` and answers with its
  key, so it needs no new flag. For an `nzbURL` source the import agent
  does the capped GET itself, as the engine does today.
- It reads the object from `clustarr-payloads`, writes the file through
  `pkg/fsops` under the squashed uid it already runs as, and answers with
  a record `{digest, size, fetchedAt}` in `clustarr-imports`, under its own
  key per entry uid beside the inspect and execute records: same writer,
  same reader, and the record source that already wakes the owner.
- `clustarr-payloads` stays a cache evicted oldest first; it holds the
  object only for the seconds the copy takes.

*Retries from the stored .nzb.*

| Question | Decision |
| --- | --- |
| Which failures | today's local faults (`diskFull`, `writeError`), and an engine crash or a lost working area with the release not at fault |
| Not a retry case | a connection failure, a login failure or a spent quota should not fail an entry at all: the transfer waits. Where that ends today as `stalled` or `timeout`, which blocklist, the engine needs a reason that is not the release's (`providersUnavailable`): a separate fix |
| Free retries | 3: one for a crash or an OOM, one for a recurrence after the cause was cleared, one of margin. A fault that recurs on three fresh attempts is deterministic |
| Back-off | 10 minutes, 30 minutes, 2 hours |
| After the third | `Failed` with the local reason, as today: no blocklist, visible to a person |
| A different engine | preferred, never required: `ChooseSlot` breaks ties away from the engine that failed and never waits for another |
| Where the state lives | `status.downloads[].retry {attempts (max 3), nextAttemptAt, lastReason}`; `nextAttemptAt` is also the owner's due time, so a restart keeps it |
| Phase between attempts | `Pending` with the reason `RetryWait`, not `Failed`, which would start the search for another release |

`SlotWaiters` leaves a `RetryWait` entry out until its `nextAttemptAt` has
passed; it then rejoins the queue at its original `grabbedAt`. The stored
file stays until the entry is dropped.

## 15. The architect's review (2026-11-28)

The owner asked for the design to be reviewed by the principal architect
agent (a subagent of session clustarr-10), measured against ADR-0025's
(`docs/adr/0025-manager-work-proportional-to-change.md`, Proposed at the
time of the review and Accepted by the owner later the same day): every
pass resumable, each key with its own due time, no
timer doing whole-library work, derived state kept up to date rather than
rebuilt by scans.

- **§14:** the shape is right; its answers are §14 as it now stands.
- **§13, stands as built:** the StatefulSet, `retireDeployment`, the
  ledger, the budgets and the limiter; the two small KV buckets; the
  membership writes (they grow with engines times accounts, not with the
  library).
- **§13, changed before deploy** (done, after 0584ff27): the 15 s fleet
  interval, the quota figure in status, and the slot waiters' book and
  rechecks, as the table in §13 now says. Also: an idle engine writes no
  membership, and a quota count is written only when bytes were read.
- **Its second round, on the follow-up:** fit to commit as it stood. Then,
  before deploy: wake the waiters on an engine record only when it shows
  room that was not there (done: `slotBook.roomChanged`); skip a status
  apply that changes nothing (done: `statusUnchanged`, and `freeBytes` in
  status moves by the gibibyte); the queue tests seen to fail with their
  rule reverted (done: the order by class, and the freed slot's wake);
  **and a proof on kind, which does not exist yet**: two engines with one
  killed mid-transfer, and a waiter woken when a slot frees.
- **Left for ADR-0025's wave 5** (clustarr-10, 2026-11-28: not owed
  now): `dlindex.SlotHolders` and `SlotWaiters` read the cache's index.
  Under that ADR the library kinds
  (Album, Book, Audiobook) have no in-memory informer, so those owners'
  counts need a SQL path; Movie and Series keep theirs. Both functions
  are the one place that would change.
