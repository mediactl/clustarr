# Usenet connection ledger design

**Date:** 2026-10-16
**Status:** Draft, for the owner's review
**Branch:** `usenet-ledger` off `unify-manager-agent`

## 1. Problem

Each usenet engine pod caps connections only for itself: a `serverPool`
semaphore per provider (`pkg/download/usenet/pool.go`). No pod knows what
another uses, and a download's workers are the sum of every provider's
allowance, so one transfer alone can hold every connection of every
server. Two consequences the owner named on 2026-10-14:

1. Two pods on the same account exceed the plan. A provider answers that
   with 502s or a throttled account, so usenet is pinned to one pod
   (`replicas == 1` CEL rule, a `Recreate` Deployment, `ChooseOrdinal`).
2. A download that got a lower-priority server, because the higher one
   was full, fails on articles that server lacks. It should park and retry
   them on a higher-priority server once that one has room.

The owner's decisions (2026-10-14): budget keyed by **provider account**;
**park and retry on a higher-priority server** instead of failing; the
per-pod slot cap shipped first (`2d5fc0aa`) and stays.

## 2. Shape

A **ledger** in NATS KV, one entry per provider account, holds every
grant of connections any engine pod has taken. A download transfers only
under a grant, on the one server the grant names, with exactly as many
workers as it was granted. Articles that server lacks are chased through
the other servers in configured order, each under its own short grant,
parking until one is free. The ledger is the only thing that changes;
the pool's semaphore stays as the hard per-pod floor beneath it.

```
manager ──commands──▶ engine pod A ──┐
                      engine pod B ──┼──▶ KV clustarr-usenet-connections
                                     │     account.<host:port:user> → {grants[]}
                      provider ◀─────┘  (each pod opens ≤ its grants)
```

## 3. The ledger

### 3.1 Bucket and key

- Bucket `clustarr-usenet-connections` (`events.BucketUsenetConnections`),
  in `pkg/events/topology.go` beside `clustarr-transcode-leases`: no TTL
  (grants carry their own expiry), one value per account, well under the
  single-node memory budget.
- Key `account.<events.KVKeyToken(host:port:username)>`
  (`events.UsenetAccountKey`). The account is the budget: two
  DownloadClients, or two pods, configured with the same host, port and
  username share one.

### 3.2 Value

```go
// pkg/download/usenet/ledger.go
type Account struct {
    Grants []Grant `json:"grants"`
}
type Grant struct {
    Holder      string    `json:"holder"`      // "<engine>/<transfer id>"
    Pod         string    `json:"pod"`
    Server      string    `json:"server"`      // provider name, for reading
    Connections int       `json:"connections"`
    Expires     time.Time `json:"expires"`
}
```

A grant older than its `Expires` is ignored by every reader and pruned by
the next writer. The engine renews every grant it holds every
`GrantRenewInterval` (30 s) with `GrantTTL` (90 s), so a dead pod's
connections return within 90 s without anyone cleaning up.

### 3.3 Operations

`pkg/download/usenet` defines the interface; the client never imports
`pkg/events`:

```go
type Ledger interface {
    // Take grants holder up to want connections on account, at most the
    // free ones under limit; it returns 0 when none are free. It never
    // takes a partial grant below min.
    Take(ctx context.Context, account string, limit int, g Grant, want, min int) (int, error)
    // Give returns holder's grant on account.
    Give(ctx context.Context, account, holder string) error
    // Renew extends every grant pod holds, on every account it touched.
    Renew(ctx context.Context, pod string, ttl time.Duration) error
}
```

- `pkg/download/usenet.LocalLedger`: in-memory, the default when
  `Config.Ledger` is nil, so the library, its tests and a one-pod install
  behave exactly as a one-pod ledger.
- `pkg/download/usenet/kvledger`: the NATS implementation. Every write is
  a compare-and-swap on the account key (`Get`, decide, `Update` with the
  read revision; `Create` when absent), retried on
  `ErrRevisionMismatch`, bounded at 8 tries, the loop
  `app/transcode/worker/lease.go`'s `claim` already uses. A real embedded
  NATS contract test (as `kvkey_contract_test.go`) proves two takers never
  exceed the limit and that an expired grant frees its connections.

`limit` is the caller's own configured `Connections` for the account. Two
DownloadClients that disagree on the plan's size each stay under their own
number; the ledger does not arbitrate that misconfiguration.

## 4. Grants in the client

### 4.1 Taking the first grant

After the per-pod slot (`waitForTurn`), before any article is fetched:

1. Walk the **primary** servers in configured order (`NewPool`'s order).
   On the first with free connections, take
   `min(free, max(1, limit / MaxActive))`: a per-download share so a pod's
   `MaxActive` transfers together never exceed one account's limit, and
   the free count bounds what several pods take together.
2. No primary has room: the job **parks**. `download.Item` reads Queued,
   `WaitingTurn` true (the manager's stall clock stops, as for a slot),
   message `waiting for usenet connections (frugal 100/100 in use, ...)`.
   It polls the ledger every `ledgerPollInterval` (5 s), holding its slot;
   a pause or a higher-priority arrival releases both, as today.
3. Backup servers never receive a first grant: a backup fills, it does not
   carry a download.

The job records `grant{server, connections, account, holder}`; `transfer`
starts exactly `grant.connections` workers instead of `client.workers`,
and `fetchFirstArticles`, the batches and the pre-check all fetch through
`Pool.FetchBatchOn(ctx, ids, []string{grant.server})` /
`Pool.ExistsOn`: the pool's `FetchBatch` gains a server scope; an empty
scope is every server, which keeps the pool's own tests and behaviour.

### 4.2 The chase: park and retry on the next server

When the transfer on the granted server ends with missing articles, or
the health gate trips mid-transfer, the job gives its grant back and runs
the **chase** over the missing set:

```
for server in configured order (primaries by priority, then backups),
                                   skipping servers already tried:
    take a grant on server: min(free, share), at least 1 — else PARK
        (Queued, WaitingTurn, "waiting for connections on <server> to
        retry N missing articles"), poll every 5 s
    FetchBatchOn(missing, [server]) with that many workers
    give the grant back
    stop when nothing is missing
```

The job fails with `ErrArticleMissing` (blocklisted, as today) only when
**every** configured server has been tried for the articles still
missing. A server that could not be asked (refused, auth, quota) counts as
not tried and the chase comes back to it after the others, up to one full
round; a second full round with no server askable is
`ErrProvidersUnavailable` (the job waits `ProviderRetryDelay` and resumes,
as today).

`retryFailed`'s one delayed pass (`ArticleRetryDelay`, SABnzbd's retry)
runs once, on the server the job transferred on, before the chase starts;
the chase itself is SABnzbd's and NZBGet's "next server" rule made to wait
for room instead of running at once.

The pre-check (`PreCheck`) STATs on the granted server only. A shortfall
below the floor there is not a verdict any more: the job proceeds and the
chase decides, so a release only one server lacks is no longer refused.

### 4.3 Giving grants back

A grant is returned when the transfer on its server ends (success, chase
step done, failure), on pause, on `Remove`, on `Close`, and when the job
is outranked and leaves its slot. A grant is never resized while held: a
connection freed elsewhere goes to the next taker, not to a running
transfer (deferred, §8).

### 4.4 Reporting

`download.Item.Grant *download.Grant{Server string; Connections int32}`
→ `schema.TransferRecord.Grant` (part of the report fingerprint, so a
change is written) → the UI's download row message shows
`frugal ×20`. The message strings above carry the parked state; no new
UI column.

## 5. Engine wiring

`app/grab/engine/usenet.BuildClient` takes the bus's
`KV(events.BucketUsenetConnections)` and sets
`Config.Ledger = kvledger.New(kv, pod)`; `Config.GrantTTL` and
`Config.GrantRenewInterval` default to 90 s and 30 s. The client runs one
renew goroutine per process (started in `New`, stopped in `Close`); a
renew that fails logs and tries again next tick, and a grant that cannot
be renewed for a whole TTL is treated as lost: the transfer on it is
parked and re-takes before its next batch.

## 6. Several pods (phase 2, separate plan)

With the ledger in place the one-pod pin can go. It needs four changes
that touch the installers and the live cluster, so they are a plan of
their own after this one proves out on talos with one pod:

1. Per-engine scratch: `<scratch>/<engine>` so two pods on the shared
   volume (ADR-0014) each re-attach only their own manifests.
2. The usenet workload becomes a StatefulSet like torrent's, so engines
   have stable ordinals.
3. `ChooseOrdinal` stops forcing usenet to ordinal 0.
4. The `replicas == 1` CEL rule on `DownloadClient` is removed.

## 7. Tests

- `ledger_test.go`: `LocalLedger` take/give/renew; share arithmetic;
  no partial grant below `min`.
- `kvledger` contract test on embedded NATS: two concurrent takers sum to
  at most the limit; an expired grant frees its connections; a
  revision race retries and converges.
- `client_test.go`/`slots_test.go` style, against the stub NNTP server:
  a job parks (Queued, WaitingTurn) when the ledger has no room and
  starts when room appears; its workers equal the grant; a missing
  article on the granted server is fetched from the next server only
  after a grant there; a job fails `ErrArticleMissing` only after every
  server was tried; a backup server never receives a first grant; a
  paused or removed job returns its grant; the stall watch ignores parked
  time (`startStallWatch` on `WaitingTurn`).
- `pool_test.go`: `FetchBatchOn` scope honoured; empty scope unchanged.
- `app/grab/engine/report_test.go`: the grant reaches the record and the
  fingerprint.
- Lint, `go test ./pkg/download/... ./app/grab/... ./pkg/events/...`;
  the topology single-node test.

## 8. Deferred

- Growing a running transfer's grant when connections free up.
- A configurable per-download share (`UsenetSpec` field) — derived from
  `MaxActive` for now.
- Phase 2 (§6).

## 9. Risks

- **Fewer workers per download than today** (a share, not the sum of
  every server). With one pod and `MaxActive` 2 on a 100-connection plan a
  transfer gets 50; today it got 160 across both accounts, most of which
  the provider refused. Throughput per transfer may drop on an idle
  system; the aggregate does not.
- **KV contention** on a busy account key: one CAS per take, give and
  30-second renew per pod; with a handful of pods this is a few writes a
  minute.
- **A parked job holds a slot and scratch.** Deliberate: nothing else
  would fare better without connections, and the owner sees it as Queued
  with the reason.

## 10. As built (2026-10-16, branch `usenet-ledger`)

Built as designed, with these differences:

- **The pre-check stays a verdict** (§4.2 said it would be log-only). It
  STATs the granted server and then only its shortfall on each other
  server under a one-connection chase grant, so "missing everywhere" is
  still decided before any article moves, under the ledger's discipline.
- **An unavailable server never blocks a verdict.** The chase skips a
  server that cannot be asked (penalised, over quota) and revisits it in
  one more pass; it does not wait for it, matching the pool's rule that a
  verdict stands once any server answered. `ErrProvidersUnavailable` is
  raised only when the transfer itself could ask nobody, as before.
- **`Renew` reports `lost` holders** rather than a lost-grant TTL rule
  (§5): the KV ledger remembers every grant the pod holds, re-adds one
  another writer pruned when the account still has room, and names it
  lost otherwise; the client clears that job's grant so its workers take
  a new one before their next batch. A renewal that fails outright logs
  and tries again next tick, since a bus nobody can reach lets no other
  pod take either.
- **Workers are goroutines, the grant is the bound.** A transfer starts
  `share` workers and a per-job limiter (`connLimiter`) admits as many
  fetches as the grant allows; a grant given back closes the limiter and
  sends every worker back through `waitForTurn`, which takes a new one.
- **`Grant.Limit`** carries the taker's account limit so a renewal can
  judge room for a re-add without the provider config.
- The grant is on `download.Item.Grant`, `schema.TransferRecord.Grant`
  (fingerprinted) and the transfer's message, `transferring on frugal (50
  connections)`; no UI change.
- `pkg/events/schema` has four test failures that predate this branch
  (schema names not naming their structs); they are untouched.

### 10.1 After the review (same day)

- **The chase's tried set is the chase's own.** A later breach finds
  other articles missing, and every server is asked for those again; the
  first build kept one set per transfer and never chased a server twice.
- **One chase at a time** (`chaseMu`): workers that breach together run
  one between them. A mid-transfer chase keeps the transfer grant, since
  the other workers are still fetching on it; the end-of-transfer chase
  gives it back first.
- **A primary that cannot be asked is never granted**; when no candidate
  can be, the take is `ErrProvidersUnavailable` and the job waits as it
  always did, then takes the next primary.
- **A parked job yields**: paused or outranked while waiting for
  connections it gives its slot back (`errTurnLost`), and a health pause
  raised by the pre-check holds neither slot nor connections.
- **A ledger that cannot be reached parks the job**, it never fails it;
  lost holders reported beside a renewal error are still dropped; `Renew`
  re-checks what the pod holds before putting a grant back, so a `Give`
  that lands mid-swap is never undone.
- **No fetch outside a grant**: the grant and its limiter change together
  under the job lock, and a fetch that finds no grant goes back through
  `waitForTurn`. Post-processing judges health (`healthVerdict`) without
  retrying or chasing.
