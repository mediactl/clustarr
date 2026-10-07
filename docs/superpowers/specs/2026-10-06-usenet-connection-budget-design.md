# Usenet connection budgets, a bandwidth cap and server order

Status: design, for the owner's approval (2026-10-06).

## 1. What the owner asked

1. Cap the usenet downloader's bandwidth per node.
2. Limit concurrent downloads by the providers' connections. A server
   allowing 30 connections at 10 per download runs at most 3 downloads; a
   second server of 70 adds 7. When the expected speed would exceed the
   node's cap, the remaining concurrency is reserved for another pod.
3. Try frugal (100 connections) first, with easynews (60) as the backup
   that fetches what frugal cannot. Today every job appears to land on
   easynews.
4. Name the client `usenet`, since it holds several servers.

## 2. What the code does today

| Area | Today |
|---|---|
| Server order | `NewPool` sorts primaries, then backups, each by `priority` (lower first); `FetchBatch` tries them in order. An article goes to the next server on a 430 or a corrupt body. A server that answers 400/502 is benched for 2 min, one that fails auth for 10 min (`pkg/download/usenet/pool.go`). |
| Connections | Each server has one semaphore of `connections` per pod. A download starts `Workers` goroutines, which default to the sum of every server's connections (160 here). A busy frugal makes work wait for frugal, and never spills to easynews. |
| Concurrent downloads | Unlimited: `Client.Add` starts every job at once. Only the priority classes hold lower classes back. |
| Bandwidth | No limiter. `j.rate` only measures. |
| Pods | A usenet client is one Deployment with one replica. CEL forbids more. |
| Visibility | Nothing records which server served a download. Per-server stats are internal. |

**The live cluster (2026-10-07):**
- **Client:** the `usenet` client exists (created 02:11Z). frugal is primary, priority 1, 100 connections; easynews is `backup: true`, priority 2, 60 connections. So wants 3 and 4 are already met in config.
- **Engine:** `clustarr-grabarr` is scaled to 0, so nothing downloads. The "all on easynews" report can't be checked: nothing records which server served what. The likely cause is frugal on its 2- or 10-minute penalty. Its 100 connections may be more than the plan allows, so it answers 502, and every article then fails over to easynews.

## 3. Design

### 3.1 Two new fields on the usenet spec

```go
// ConnectionsPerDownload is how many connections one download uses at once
// (its fetch workers). Default 10.
// +kubebuilder:validation:Minimum=1
// +kubebuilder:validation:Maximum=256
ConnectionsPerDownload *int32 `json:"connectionsPerDownload,omitempty"`

// MaxBytesPerSecond caps one engine pod's total download rate; an engine
// pod runs alone on its node (3.4), so it is the node's cap. Unset means
// uncapped.
MaxBytesPerSecond *resource.Quantity `json:"maxBytesPerSecond,omitempty"`
```

Both are pointers with accessors (`ConnectionsPerDownloadOrDefault`), as
the CLAUDE.md typed-client gotcha requires.

### 3.2 Admission: how many downloads run at once

- **The slot count:** `slots = Σ over primary servers ⌊connections / connectionsPerDownload⌋`. That is 10 for frugal at 100/10. Backups add no slots: they only fill.
- **A slot and its connections:** a running download holds one slot and starts `connectionsPerDownload` workers, not 160. Each worker holds one connection at a time, primary first, then backup, as today.
- **`Client.Add`:**
  - queues a job past the slot count, FIFO within its priority class, under the existing outranked rule;
  - starts the job when a slot frees;
  - reports the queued state as `Queued`, which the engine already maps.
- **The bandwidth bound:** with a cap set, slots are also limited to `⌊cap / expected per-download rate⌋`, never below 1.
  - The expected rate is the moving average of measured per-download throughput on this pod.
  - Until one download has run long enough to measure, only the connection bound applies.
  - Slots this bound leaves unused are the ones another pod can take (3.4).

### 3.3 The bandwidth cap

One `rate.Limiter` per pool, in bytes per second, at `MaxBytesPerSecond`, shared by every connection. A connection takes tokens for each chunk it reads from the socket, so the whole pod stays under the cap. The caller-owns-rate-limiting convention holds: `Config.Limiter` is injected, and the engine builds it from the CRD.

### 3.4 More than one pod (phase B)

"Reserve the remaining concurrency for another pod" needs:

- **More replicas:** the CEL rule that pins usenet to one replica is relaxed. Pods are spread one per node by pod anti-affinity, so the bandwidth cap is per node.
- **A connection budget shared across pods:** a provider's connections belong to the account, not the pod.
  - The budget is held as leases in a NATS KV bucket `clustarr-usenet-connections`, one key per provider through `events.KVKeyToken`.
  - Writes are revision-checked; a lease is renewed with the engine's heartbeat, and a dead pod's lease lapses.
  - A pod leases only the slots its bandwidth bound lets it use. The rest stay free for the next pod.
- **Assigning Downloads by capacity:** a Download goes to the pod with a free slot, not by `HashOrdinal`.
  - `status.engine` is still set once, when the Download is assigned.
  - A Download no pod can take waits `Queued`.

Phase B touches the grab controller and engine, which the
`unify-manager-agent` branch is moving (clustarr-c3). So its logic lives
in `pkg/download/usenet` and the CRD, as that session asked.

### 3.5 Seeing which server served what

- **Status:** `Download.status.usenet.servers[]` holds `{name, bytes, articles, missing}`, capped at 32 by `MaxItems`. It is engine-owned, under the engine's field manager.
- **Metrics:** `clustarr_usenet_server_bytes_total{client,server}` and `clustarr_usenet_server_penalties_total{client,server,reason}`. Server and client names are bounded config names, never titles.
- **Benched servers:** a benched server is logged once per penalty with its NNTP code, so a 502 from too many connections is visible at once.

## 4. Phases

- **A** (one pod; wants 1 and 2 on this cluster):
  - the two fields;
  - admission by slots;
  - the per-download worker count;
  - the bandwidth limiter;
  - per-server stats and metrics.
- **B** (several pods): replicas above 1, KV connection leases, and assignment by capacity.

Wants 3 and 4 need no code. They are config, already applied. Phase A's
per-server stats show whether frugal is being benched.

## 5. Open questions for the owner

1. **The default `connectionsPerDownload`:** 10, as in your example, or another number?
2. **The node cap:** what `maxBytesPerSecond` should this cluster's node take, for example `100Mi`, about 800 Mbit/s?
3. **Phase B now, or later?** This cluster has one usenet node.
4. **The old `frugal` client:** is it gone? Is `grabarr` at 0 replicas your pause, to lift when phase A is deployed?
