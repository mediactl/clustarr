# ADR-0020: NATS client resilience -- graceful drain, bounded subscription buffers, slow-consumer shedding, object store maintenance, reconnect backoff

**Status:** Accepted, 2026-10-07. Implementation is scheduled after the current plan's last
implementation wave (`docs/superpowers/plans/2026-10-06-manager-agent-split.md` and
`docs/superpowers/plans/2026-10-07-agents-report-over-nats.md`, through F9 and A9) and before
the test batch, so its tests join that batch.

## Context

An external review (2026-10-07) checked ADR-0019's manager/agent split and the NATS layer
(`pkg/events/natsbus`, `pkg/busconn`) against the NATS documentation on resilient clients,
drain and shutdown, slow consumers and object store chunking. Its overall reading of ADR-0019
agrees with the design: the manager is the only writer of custom resources, agents hold
read-only bindings, and `app/dispatch` admits no more than twice a durable's `MaxAckPending`
before a task waits on its CR (`admit.go`).

Each claim was checked against the branch at `ed4d1dcf`/`392d5c95` and against nats.go
v1.53.1 and nats-server v2.15.0 (`go.mod`). The verdicts:

| # | Claim | Verdict | Evidence |
|---|---|---|---|
| 1 | Core subscriptions (`Serve`, `SubscribeCore`) set no pending limits, so each can buffer nats.go's defaults | **Confirmed.** The defaults are 500,000 messages and 64 MiB per subscription (`DefaultSubPendingMsgsLimit`, `DefaultSubPendingBytesLimit`, nats.go:5765-5767), not 512,000 | `natsbus.go` `Serve` (`QueueSubscribe`, no `SetPendingLimits`); `core.go` `SubscribeCore` (`nc.Subscribe`) |
| 2 | A slow consumer is only counted and logged | **Partly.** `asyncerr.go` counts `clustarr_nats_async_errors_total{kind="slow_consumer"}` and logs once a minute per subject. But `Serve` already sheds load: a request that finds the `ServeGate` full is answered at once with `ServiceErrorBusy` (`natsbus.go:529-568`), so the callback itself never blocks on a slow handler. The suggested remedy, readiness, does not apply: a pod's readiness takes it out of a Kubernetes Service, not out of a NATS queue group, and no clustarr RPC goes through a Service | `asyncerr.go:25-75`, `natsbus.go:529-568` |
| 3 | Abandoned pull requests linger until the manager's `CONSUMER.INFO` prunes them | **Not confirmed as a risk.** Every `Fetch` carries a 30 s expiry (`fetchWait`, passed as `FetchContext` with a deadline, `subscription.go:36,269-271`), so the server drops an abandoned pull within 30 s even with no manager. The HPA metric is `NumPending + NumAckPending`, which an open pull does not inflate. Only `NumWaiting` can read an abandoned pull, for at most 30 s, inside A2's two-minute unattended window. The code comments (`subscription.go:102-104,265-267`, `deadletter.go:80-81`) overstate the reliance on `CONSUMER.INFO` and are corrected | `subscription.go`, `deadletter.go` |
| 4 | Object store chunk size is fixed at nats.go's default of 128 KiB | **Confirmed**, with smaller impact than stated. `events.ObjectMeta` has no chunk size and `objectHandle.Put` passes no `Opts`. But clustarr's servers run `max_payload` 8 MiB (CLAUDE.md, "The Helm chart's NATS ran the server's 1 MiB `max_payload`"), not 1 MiB. Fingerprints are about 20 KB each (`topology.go:1214-1216`), one chunk, not "tens of MB". Artwork originals and overlays, up to about 3 MB, are the only objects of many chunks | `objectstore.go:182-196`, `topology.go:1212-1224` |
| 5 | `clustarr-fingerprints` has no orphan-chunk purge | **Confirmed, but bounded.** `PurgeOrphanChunks` runs only from the artwork reaper over the artwork bucket (`app/catalog/artwork/reaper.go:273-290`). But the fingerprints store has `MaxAge` 90 days and a `MaxBytes` cap, so orphans expire rather than "leak permanently" | `reaper.go`, `topology.go:1219-1220` |
| 6 | Shutdown halts subscriptions and skips their drain | **Partly.** Pull subscriptions drain on SIGTERM: every runnable subscribes, then `defer stop()` after `<-ctx.Done()`, and `stop` waits up to `Subscription.Drain` (for example `app/catalog/agent/metadata/register.go:125-130`). controller-runtime waits for runnables up to `GracefulShutdownTimeout`, which the agent sets to `DrainTimeout + 15s`. `bus.Close`'s `halt()` therefore reaches only a subscription still running after that budget: the intended hard stop. **Confirmed for responders.** `Serve` returns no stop function. Responders end only in `Close`, which cancels every in-flight request handler's context (`serveCancel`) and `Unsubscribe`s, never `Drain`s, so requests buffered client-side or half-answered are dropped and their requesters time out | `natsbus.go:222-256,540-600` |
| 7 | `nc.Close()` without `nc.Drain()` drops unwritten acks, naks and KV records | **Not confirmed.** nats.go's `close` flushes the outbound buffer before closing the socket (`nc.bw.flush()`, nats.go:6099-6101). A handler's ack is `DoubleAck`, confirmed by the server (`message.go:93`). JetStream publishes and KV writes wait for their PubAck. What `nc.Drain()` would add is the orderly end of core subscriptions, which is item 6's responder gap | nats.go v1.53.1; `message.go` |
| 8 | Fixed reconnect wait, single URL, default reconnect buffer, no distinction between no-responders and timeout | **Partly.** `ReconnectWait` is a fixed 2 s (`busconn.go:47-48,108`), but nats.go already adds jitter: 100 ms, or 1 s with TLS (`DefaultReconnectJitter`, nats.go:57-58). `nats.Connect` takes a comma-separated server list and learns a cluster's other servers from `INFO`, so a pool needs only a list in `NATS_URL`. The reconnect buffer is the default 8 MiB. `natsbus.Request` already maps `nats.ErrNoResponders` to `events.ErrNoResponders` (`natsbus.go:705-706`), but callers do not act on the difference consistently | `busconn.go`, `natsbus.go` |

## Decision

**1. Graceful drain is the shutdown path; `Close` stays the hard abort.**

- `events.Bus` gains `Drain(ctx) error`, implemented by both buses:
  - It stops every pull subscription the way `stop` does: no more fetches, a nak for anything still arriving, and handlers keep their context for up to `Subscription.Drain`.
  - It drains every responder with nats.go's `Subscription.Drain()`: no new requests are taken, and requests already buffered client-side are still answered.
  - It waits for in-flight respond goroutines (the `ServeGate`'s holders) until `ctx` ends, and only then cancels the serve context.
- `Serve` returns a stop function, as `Subscribe` does, so a runnable can drain its own responder at `<-ctx.Done()`.
- `busconn` gains `Shutdown(ctx, bus, nc)`:
  - It calls `bus.Drain(ctx)`, then `nc.Drain()`, and waits for the connection's `ClosedHandler`, or `ctx`, before returning.
  - `nats.DrainTimeout` is set to the same budget, so nats.go never cuts its own drain short.
- `cmd/manager`, `cmd/agent`, `cmd/markers` and `cmd/transcode` replace `defer nc.Close()` / `defer bus.Close()` with `busconn.Shutdown` after the manager returns. The budget is the remainder of `GracefulShutdownTimeout`, with `Close` as the fallback once it is spent.
- The manager drains last among its runnables, so intake acks after a decision lands (ADR-0019 §8) still reach the server.

**2. Every core subscription has explicit pending limits.**

- `Serve`: `SetPendingLimits(ServeLimits.MaxPending, MaxPendingBytes)`. The defaults are 4 × the gate's concurrency in messages and 8 MiB, the server's own `max_payload`, so one oversized request still fits.
- `SubscribeCore` takes an `events.CoreLimits` with default 1,024 messages and 8 MiB.
- Memory then has a bound per subscription, and a burst past it becomes nats.go's slow-consumer drop. That drop is counted, and alerted on by decision 3.

**3. Slow consumers are shed and alerted on, not tied to readiness.**

- Responders already shed load through `ServeGate`'s immediate `ServiceErrorBusy`.
- `asyncerr` additionally records a per-subscription `clustarr_nats_dropped_messages` gauge from `sub.Dropped()`, labelled by the subscription's bounded name (durable or RPC verb), never by a subject carrying ids.
- The chart's PrometheusRule (where enabled) alerts on any increase of `clustarr_nats_async_errors_total{kind="slow_consumer"}`.
- Readiness is not coupled. A NATS queue group does not consult it, and an unready agent would still receive its pulls.
- A responder that keeps dropping, for `SlowConsumerLeave` (5 minutes of drops), drains its queue subscription and re-subscribes after `SlowConsumerRejoin` (1 minute). This leaves the queue group to its peers when a peer exists, and is logged. Where it is the only member, it stays subscribed.

**4. Request errors are classified at every caller.**

- `events.ErrNoResponders` means nothing serves the verb. It fails fast, and in the manager it is fed to `app/dispatch` as the destination's `NoAgent` reason.
- `context.DeadlineExceeded` means the responder is slow or busy, or its answer is lost.
- `events.ErrResponderBusy` means refused at once.
- An idempotent verb retries on its own backoff: `clustarr.rpc.indexarr.blocklist`, the metadata search and extras, `rpc.indexarr.query`. A non-idempotent one never retries blindly after a timeout: it re-reads state first (`rpc.indexarr.download`'s callers dedupe on `AddRequest.Name`, CLAUDE.md).
- A small `events.ClassifyRequestError` helper returns `noResponders | busy | timeout | other`, and the RPC callers switch on it.

**5. Object stores: chunk size per store, and every store is maintained.**

- `events.ObjectStoreSpec` gains `ChunkSize uint32` (0 = nats.go's 128 KiB), mapped to `jetstream.ObjectMetaOptions{ChunkSize}` on every `Put`. `events.ObjectMeta` gains an optional per-object override.
- `clustarr-artwork` uses 1 MiB, so an image of up to 3 MB is at most 3 chunks. `clustarr-fingerprints` keeps the default.
- `Topology.Validate` refuses a chunk size above 1 MiB, an eighth of the 8 MiB `max_payload`, so a chunk never approaches a server limit.
- The orphan-chunk purge runs over every object store in the topology, not only artwork. It runs from the manager's maintenance loop (ADR-0019: maintenance is the manager's), on the artwork reaper's cadence and grace. The artwork reaper keeps its own variant rules but calls the shared purge.

**6. Reconnect: exponential backoff with jitter, a server pool, a sized buffer.**

- `busconn` uses `nats.CustomReconnectDelay`: 250 ms doubling to 30 s, with full jitter.
- `NATS_URL` is documented and validated as a comma-separated pool. The chart renders the NATS Service, which already fronts every server, and an optional `nats.urls` list.
- `ReconnectBufSize` is set explicitly to 8 MiB, today's default, made a named constant.
- A publish refused with `nats.ErrReconnectBufExceeded` is classified transient, like a timeout: the caller's backoff retries it, and nothing drops it silently.

**7. Correct the comments.** The pull-abandonment comments in `subscription.go` and
`deadletter.go` say a pull's own 30 s expiry bounds it, with `CONSUMER.INFO` pruning it
sooner when it happens.

## Alternatives considered

**Tie slow-consumer state to pod readiness**, as the review proposed. Rejected: readiness
removes a pod from Kubernetes Service endpoints, while clustarr's RPC and work traffic is NATS
queue groups and pull consumers, which never read it. The pod would read unready and keep
receiving work.

**Call `nc.Drain()` alone at shutdown.** It drains core subscriptions but knows nothing of the
bus's pull loops, slots or handler contexts. `bus.Drain` must come first and `nc.Drain` second.

**Raise chunk sizes to several MiB for fewer messages.** Rejected: there are no objects of tens
of MiB, and a chunk near `max_payload` risks the 1 MiB-default class of failure the chart
already hit once (CLAUDE.md).

**A reaper per object store in its owning service.** Rejected: ADR-0019 puts maintenance in the
manager, and one purge over the topology's list cannot forget a store added later.

## Consequences

- **Rolling updates and scale-downs finish in-flight RPCs** instead of timing their callers
  out. This matters most for the index domain's `download` and `blocklist` verbs and the
  metadata gateway.
- **Memory per process is bounded** by explicit per-subscription limits. A burst past them is
  dropped, counted and alerted, rather than growing to 64 MiB per subscription.
- **Shutdown ordering is one function**, `busconn.Shutdown`, used by all four binaries, held by a
  guard: no `nc.Close()` or `bus.Close()` in a `cmd/` or `internal/cli` shutdown path outside it.
- **New knobs:**
  - `ServeLimits.MaxPending`/`MaxPendingBytes`, `CoreLimits`;
  - `SlowConsumerLeave`/`SlowConsumerRejoin`;
  - `ObjectStoreSpec.ChunkSize`;
  - the reconnect backoff.

  Each has a default and needs no installer change.
- **Tests (in the batch):**
  - `TestDrainAnswersEveryBufferedRequest`;
  - `TestShutdownDrainsPullsThenRespondersThenTheConnection`;
  - `TestCloseStillHaltsAtOnce`;
  - `TestEveryCoreSubscriptionHasPendingLimits`, an AST guard plus a runtime check of `PendingLimits()`;
  - `TestASlowResponderLeavesItsQueueGroupOnlyWithAPeer`;
  - `TestClassifyRequestError`;
  - `TestArtworkPutsUseTheStoresChunkSize`;
  - `TestValidateRefusesAChunkOverOneMiB`;
  - `TestEveryObjectStoreIsPurgedOfOrphans`;
  - `TestReconnectDelayBacksOffWithJitter`;
  - `TestNoShutdownPathClosesTheConnectionOutsideShutdown`.

## Implementation

Wave N, after the current plan's implementation waves and before the test batch:

| Task | Content |
|---|---|
| N1 | `Bus.Drain`, `Serve`'s stop function, `busconn.Shutdown`, `DrainTimeout`; the four binaries' shutdown paths |
| N2 | Pending limits on `Serve` and `SubscribeCore`; `CoreLimits` |
| N3 | Slow-consumer gauge and alert; the responder leave/rejoin |
| N4 | `ClassifyRequestError` and the callers' retry rules |
| N5 | `ObjectStoreSpec.ChunkSize`, `ObjectMeta` override, Validate's 1 MiB cap; the manager's orphan purge over every object store |
| N6 | Reconnect backoff with jitter, `NATS_URL` pool validation, the named reconnect buffer, `ErrReconnectBufExceeded` as transient; the comment corrections |
| N7, N8 | ADR-0021: fingerprint metadata through `pkg/segments.FingerprintMeta`, and the guards `TestNoObjectLinks` and `TestObjectMetadataComesFromItsStoresBuilder` |

## Revisit triggers

- NATS runs as a multi-node cluster with leaf nodes or gateways: then revisit server discovery and the pool.
- An object store gains objects over 3 MB routinely: then revisit `ChunkSize`.
- `clustarr_nats_dropped_messages` is non-zero in steady state: the limits are too small, or a callback is doing work it should hand to a goroutine.
