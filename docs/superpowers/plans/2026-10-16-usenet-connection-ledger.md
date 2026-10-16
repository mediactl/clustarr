# Usenet Connection Ledger Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Usenet downloads transfer only under a connection grant taken from a ledger every engine pod shares, park when no server has room, and chase missing articles through the other servers under grants of their own instead of failing.

**Architecture:** `pkg/download/usenet` gains a `Ledger` interface, a `LocalLedger` default and per-job grants that bound the fetch workers through a resizable limiter; the pool's fetches take a server scope; `pkg/download/usenet/kvledger` keeps the shared ledger in one NATS KV entry per provider account with compare-and-swap writes; the engine wires it; the grant reaches the transfer record.

**Tech Stack:** Go, NATS JetStream KV (`pkg/events`), the stub NNTP server in `pkg/download/usenet/stub_test.go`, embedded nats-server for the contract test.

**Spec:** `docs/superpowers/specs/2026-10-16-usenet-connection-ledger-design.md`

## Global Constraints

- GPL-3.0 header from `hack/boilerplate.go.txt` on every new Go file.
- `pkg/download/usenet` never imports `pkg/events`; the KV ledger is the subpackage `kvledger`.
- Every KV key goes through `events.KVKeyToken`; the key shape is proved on a real embedded server.
- Logging through `logging.FromContext`; no package-level logger.
- Commits are pathspec commits (`git commit -m ... -- <paths>`), new files `git add`ed first.
- Never `git stash`; never push (the owner pushes after review).
- Gate per task: `go build ./... && go vet ./pkg/download/... ./app/grab/... ./pkg/events/...` plus the task's tests. Lint once at the end: `golangci-lint-v2 run ./pkg/download/... ./app/grab/... ./pkg/events/...`.

## Review Focus

1. A job paused or outranked while parked must give back nothing it does not hold, and must not hold a grant while paused (Task 3's pause test).
2. Two takers on one account in different processes never sum over the limit (Task 6's contract test).
3. A grant that expired under a pod (renew missed) is re-taken, never assumed (Task 6's `Renew` returning `lost`, Task 3's drop test).
4. A missing article on the only configured server fails as missing without parking forever (Task 4's single-server test).
5. The stall watch and the manager's stall clock both stop while parked (Task 3: `WaitingTurn`; `startStallWatch` treats `waitingGrant` like paused).

---

### Task 1: Ledger interface and LocalLedger

**Files:**
- Create: `pkg/download/usenet/ledger.go`
- Test: `pkg/download/usenet/ledger_test.go`

**Interfaces (Produces):**
```go
// Account is the budget key: host:port:username.
func (p Provider) Account() string
type Grant struct {
    Holder      string    `json:"holder"`
    Pod         string    `json:"pod,omitempty"`
    Server      string    `json:"server"`
    Connections int       `json:"connections"`
    Expires     time.Time `json:"expires"`
}
type Ledger interface {
    // Take grants g.Holder up to want connections on account, bounded by
    // limit minus every other live grant; it takes nothing and returns 0
    // when fewer than min are free. A holder that already holds a grant on
    // account gets that grant's size back unchanged.
    Take(ctx context.Context, account string, limit int, g Grant, want, min int) (int, error)
    // Give returns holder's grant on account; absent is not an error.
    Give(ctx context.Context, account, holder string) error
    // Held is the live connections on account.
    Held(ctx context.Context, account string) (int, error)
    // Renew extends every grant this ledger's pod holds and names the
    // holders whose grants it found gone (expired and pruned by another
    // writer, with no room to take them back).
    Renew(ctx context.Context) (lost []string, err error)
}
// LocalLedger is the in-process Ledger: one pod, no expiry.
func NewLocalLedger() *LocalLedger
// share is a download's share of an account: max(1, limit/maxActive).
func share(limit, maxActive int) int
```

- [ ] **Step 1: Write the failing tests** (`ledger_test.go`): `TestLocalLedgerTakesAtMostTheFreeConnections` (limit 10: holder a wants 6 → 6; holder b wants 6 → 4; holder c wants 6 min 5 → 0; a gives; c → 6), `TestLocalLedgerTakeIsIdempotentPerHolder` (a takes 6 then asks 8 → 6), `TestShareIsAtLeastOne` (share(100,2)=50, share(1,4)=1, share(0,2)=1), `TestProviderAccountIsHostPortUser`.
- [ ] **Step 2: Run** `go test ./pkg/download/usenet/ -run 'LocalLedger|Share|ProviderAccount'` — Expected: FAIL, undefined symbols.
- [ ] **Step 3: Implement** `ledger.go` as above; `LocalLedger` is a mutex over `map[account]map[holder]Grant`; `Renew` returns nil, nil.
- [ ] **Step 4: Run** the tests — Expected: PASS.
- [ ] **Step 5: Commit** `git add pkg/download/usenet/ledger.go pkg/download/usenet/ledger_test.go && git commit -m 'feat(usenet): the connection Ledger interface and the in-process LocalLedger' -- pkg/download/usenet/ledger.go pkg/download/usenet/ledger_test.go`

### Task 2: Pool fetches take a server scope

**Files:**
- Modify: `pkg/download/usenet/pool.go`
- Test: `pkg/download/usenet/pool_test.go`

**Produces:**
```go
// Servers lists provider names in walk order, with their class.
func (p *Pool) Servers() []ServerStats   // existing Stats() already does this; add
func (p *Pool) Provider(name string) (Provider, bool)
// FetchBatchOn is FetchBatch over only the named servers, in the pool's order; nil means all.
func (p *Pool) FetchBatchOn(ctx context.Context, ids []string, servers []string) ([]Result, error)
func (p *Pool) ExistsOn(ctx context.Context, ids []string, servers []string) ([]string, error)
```
`FetchBatch`/`Exists` become `FetchBatchOn(ctx, ids, nil)`/`ExistsOn(ctx, ids, nil)`. A scoped walk iterates `p.servers` skipping names outside the scope. The sentinels are unchanged: with scope `[A]` an article A refuses is `ErrArticleMissing` (the caller's chase decides what "every server" means).

- [ ] **Step 1: Failing test** `TestFetchBatchOnAsksOnlyTheScopedServer`: two stub servers both holding `<a@x>`; scope `[second]` → `first.servedCount == 0`, result served by `second`. `TestFetchBatchOnScopedToARefusingServerIsMissing`: first refuses (430), second holds; scope `[first]` → `ErrArticleMissing`, `second.servedCount == 0`.
- [ ] **Step 2: Run** `go test ./pkg/download/usenet/ -run FetchBatchOn` — Expected: FAIL, undefined.
- [ ] **Step 3: Implement.** Step 4: run — PASS; `go test ./pkg/download/usenet/ -run 'Pool|FetchBatch|Exists'` still green.
- [ ] **Step 5: Commit** `-- pkg/download/usenet/pool.go pkg/download/usenet/pool_test.go` message `feat(usenet): FetchBatchOn and ExistsOn scope a fetch to named servers`.

### Task 3: Grants in the client

**Files:**
- Modify: `pkg/download/usenet/client.go` (Config, Client, job, New, item, Pause, Close, Remove), `pkg/download/usenet/priority.go` (waitForTurn), `pkg/download/usenet/segment.go` (transfer, fetchFirstArticles, runBatch, startStallWatch)
- Create: `pkg/download/usenet/grant.go` (takeGrant, giveGrant, connLimiter, renew loop)
- Test: `pkg/download/usenet/grant_test.go`

**Consumes:** Task 1's `Ledger`, `Grant`, `share`; Task 2's `FetchBatchOn`.

**Produces:**
```go
// Config additions
Ledger             Ledger        // nil → NewLocalLedger()
Holder             string        // "<engine>" prefix of every grant holder; "" → "usenet"
GrantRenewInterval time.Duration // 0 → defaultGrantRenewInterval (30s)
// download.Item (pkg/download/download.go) additions
type Grant struct{ Server string; Connections int32 }
Grant *Grant   // the usenet transfer's connection grant; nil when none
```

Design inside the client:
- `job.grant *Grant` (under mu) and `job.waitingGrant atomic.Bool`; `job.limiter connLimiter` (mutex+cond: `set(n)` bumps a generation and wakes waiters; `acquire(ctx) (ok bool)` blocks while in-use ≥ n, returns false when the generation changed or n == 0 so the worker goes back through `waitForTurn`; `release()`).
- `takeGrant(ctx, candidates []string, holderSuffix string, min int) (Grant, error)`: walks candidates in pool order; for each provider, `want = share(p.Connections, c.maxActive)`, `Take(account, p.Connections, Grant{Holder: holder+"/"+id+suffix, Server: name}, want, min)`; first n>0 wins. None: `waitingGrant=true`, message `waiting for usenet connections (<name> <held>/<limit> in use, ...)`, status Queued, poll every `ledgerPollInterval` (var, 5 s) honouring ctx and pause (`waitWhilePaused`). On success: `waitingGrant=false`, `lastProgress=now`, message `transferring on <name> (<n> connections)`.
- `giveGrant(ctx)`: Give + clear + `limiter.set(0)`.
- `waitForTurn`: the paused and outranked branches also `giveGrant`; after `admit` succeeds and `j.grant == nil`, `takeGrant(primaries, "", 1)` then `limiter.set(n)`.
- `transfer`: workers = `share(maxLimit, maxActive)` where maxLimit is the largest primary's Connections; each worker per batch: `waitForTurn` → `limiter.acquire` (false → continue the loop) → `runBatch` → `release`. `fetchFirstArticles` and `runBatch` fetch with scope `[j.grant.Server]` (read under mu).
- `startStallWatch`: `j.paused.Load() || j.waitingGrant.Load()` resets `start`.
- `item()`: `WaitingTurn` also `|| j.waitingGrant.Load()`; `Grant` copied from `j.grant`.
- `Pause`: nothing new (the workers' next `waitForTurn` gives the grant back); `Remove`/`Close`: after the job stops, `giveGrant` with a 5 s background context.
- `New`: default the ledger and holder; start `c.renewLoop()` (ticker `GrantRenewInterval`; `lost` holders → the matching jobs' grants cleared and `limiter.set(0)`, so their workers re-take); `Close` stops it.

- [ ] **Step 1: Failing tests** (`grant_test.go`, stub server, `newTestClient`):
  - `TestATransferParksUntilTheLedgerHasRoom`: a test `gateLedger` (wraps LocalLedger; `Take` returns 0 until `open()`); job reaches Queued + `WaitingTurn` with message containing `waiting for usenet connections`, serves nothing; `open()` → completes; `Grant.Server == "solo"`.
  - `TestTheWorkersMatchTheGrant`: provider 8 connections, MaxActive 2 → share 4; `srv.peakConns <= 4` over a 40-part job with `bodyDelay`.
  - `TestABackupServerNeverGetsTheFirstGrant`: primary gated closed, backup open; job parks, `backup.servedCount == 0` for every id.
  - `TestAPausedJobGivesItsGrantBack`: pause mid-transfer → `ledger.Held(account) == 0` eventually; resume → completes.
  - `TestALostGrantIsRetaken`: a test ledger whose `Renew` reports the holder lost once; `GrantRenewInterval` 20 ms; the job completes and `Take` was called at least twice for it.
- [ ] **Step 2: Run** `go test ./pkg/download/usenet/ -run 'Parks|MatchTheGrant|BackupServerNever|GivesItsGrantBack|LostGrant'` — Expected: FAIL (fields undefined).
- [ ] **Step 3: Implement** as designed. **Step 4: Run** the five, then `go test ./pkg/download/usenet/` — Expected: PASS, the slot and priority tests still green (they run on the LocalLedger).
- [ ] **Step 5: Commit** `-- pkg/download/usenet pkg/download/download.go` message `feat(usenet): a transfer runs under a connection grant from the ledger, parking Queued when no primary has room`.

### Task 4: The chase

**Files:**
- Modify: `pkg/download/usenet/segment.go` (`retryFailed` scoped to the grant server; new `chase`; `healthGate`; end of `transfer`), `pkg/download/usenet/client.go` (`preCheck` on the granted server), `job` gains `tried map[string]bool`.
- Test: `pkg/download/usenet/chase_test.go`

**Consumes:** Task 3's `takeGrant`/`giveGrant`, Task 2's scope.

Behaviour:
```go
// chase fetches the still-missing articles from every server not yet
// tried, in pool order, primaries then backups, each under a grant of its
// own (holder suffix "/chase"), parking until one has room. It marks a
// server tried once it answered; a server that could not be asked is not
// marked and is visited again in a second round. It returns nil when
// nothing is missing or every server was tried, and ErrProvidersUnavailable
// when a whole round asked nobody.
func (j *job) chase(ctx context.Context) error
```
- `retryFailed` keeps its one delayed pass but scoped to `j.grant.Server`; the grant's server is marked tried when the transfer on it ends.
- `healthGate`: breached → `retryFailed` → `chase` → judge again (`breached()`), then `breach` as today.
- End of `transfer`: `retryFailed` (if failed > 0) → mark tried → `giveGrant` → `chase` → the existing warning log; `unverifiableGate` unchanged.
- `preCheck`: `ExistsOn(ids, [j.grant.Server])` after a first grant (takeGrant moves ahead of preCheck in `run`); a shortfall logs at Info and returns nil — it is no longer a verdict (spec §4.2).

- [ ] **Step 1: Failing tests:**
  - `TestAMissingArticleIsChasedOnTheNextServerUnderAGrant`: first (priority 1) refuses `<p3>`, second (priority 2) holds all; second's account gated closed → job completes only after `open()`; `second.servedCount(p3) == 1` and second served nothing else.
  - `TestAJobFailsAsMissingOnlyAfterEveryServerWasTried`: both refuse `<p3..p9>` (7 > maxBadArticles) of 10 parts, `HealthAction: delete`, `AbortHealthPercent 90`; result Failed `DownloadFailureMissingArticles`; both servers were asked for p3.
  - `TestASingleServerJobFailsAsMissingWithoutParking`: one server refusing 7 of 10 → Failed within the test's deadline, never Queued on `waiting for usenet connections`.
  - `TestABackupFillsOnlyTheMissingArticles`: primary refuses one, backup holds all → `backup.servedCount` is 1 in total.
- [ ] **Step 2: Run** — Expected: FAIL (chase missing: the first test completes without waiting, or the servedCount asserts fail).
- [ ] **Step 3: Implement. Step 4: Run** the four, then the package — Expected: PASS.
- [ ] **Step 5: Commit** `-- pkg/download/usenet` message `feat(usenet): missing articles are chased through the other servers under grants, failing as missing only after every server was tried`.

### Task 5: The grant in the transfer record

**Files:**
- Modify: `pkg/events/schema/transfer.go` (`TransferGrant{Server string; Connections int32}`, `TransferRecord.Grant *TransferGrant`), `app/grab/engine/report.go` (map `it.Grant`; fingerprint adds `rec.Grant`).
- Test: `app/grab/engine/report_test.go`

- [ ] **Step 1: Failing test** `TestTheGrantReachesTheTransferRecordAndItsFingerprint`: an item with `Grant{"frugal", 20}` → record `Grant.Server == "frugal"`; the fingerprint of a record with and without the grant differ.
- [ ] **Step 2: Run** `go test ./app/grab/engine/ -run Grant` — FAIL. **Step 3: Implement. Step 4:** PASS, `go test ./app/grab/...` green.
- [ ] **Step 5: Commit** `-- pkg/events/schema/transfer.go app/grab/engine` message `feat(grab): the usenet connection grant rides the transfer record`.

### Task 6: The KV ledger

**Files:**
- Modify: `pkg/events/subjects.go` (`BucketUsenetConnections = "clustarr-usenet-connections"`, `UsenetAccountKey(account string) string { return "account." + KVKeyToken(account) }`), `pkg/events/topology.go` (`b(BucketUsenetConnections, 0, "Usenet connection grants per provider account; grants carry their own expiry.")`).
- Create: `pkg/download/usenet/kvledger/ledger.go`, `pkg/download/usenet/kvledger/ledger_test.go` (embedded NATS, copy `startServer` from `app/grab/engine/kvkey_contract_test.go`; apply the production topology with `natsbus` as that test does).

**Produces:**
```go
func New(kv events.KV, pod string, ttl time.Duration) *Ledger   // ttl 0 → 90s
```
`Take`: up to 8 tries of Get → decode `usenet.Account{Grants}` → prune expired → sum others → decide → Update(rev) (or Create when absent); `ErrRevisionMismatch`/`ErrKeyExists`/`ErrKeyNotFound` retry. `Give`: same loop removing the holder. `Held`: Get+sum. `Renew`: for every account the ledger touched (kept in memory), reload, extend own grants' `Expires`; an own grant that is absent is taken back only if room, else its holder is returned in `lost`.

- [ ] **Step 1: Failing tests:** `TestTwoPodsNeverExceedTheAccountLimit` (two `Ledger`s, limit 10, 20 concurrent takers of want 3 min 1 → held ≤ 10 at every point, `Held` ≤ 10 at the end), `TestAnExpiredGrantFreesItsConnections` (ttl 50 ms, pod A takes 10; after 60 ms pod B takes 10), `TestRenewReportsAGrantItLost` (A takes 10, ttl 50 ms; B takes 10 after expiry; A.Renew → lost contains A's holder), `TestTheAccountKeyIsValidOnARealServer` (an account with `:` and `@` round-trips).
- [ ] **Step 2: Run** `go test ./pkg/download/usenet/kvledger/` — FAIL (package missing). **Step 3: Implement. Step 4:** PASS; `go test ./pkg/events/...` (the single-node limits test) green.
- [ ] **Step 5: Commit** `-- pkg/events/subjects.go pkg/events/topology.go pkg/download/usenet/kvledger` message `feat(usenet): the shared connection ledger in NATS KV, one compare-and-swap entry per provider account`.

### Task 7: Engine wiring

**Files:**
- Modify: `app/grab/engine/usenet/config.go` (`BuildClient(..., ledger usenetclient.Ledger, holder string)` sets `cfg.Ledger`, `cfg.Holder`), `app/grab/agent/usenet/register.go` (passes `kvledger.New(bus.KV(events.BucketUsenetConnections), os.Getenv("POD_NAME"), 0)` and `o.Engine`).
- Test: `app/grab/engine/usenet/config_test.go` (BuildConfig unchanged; a test that `BuildClient` installs the ledger it is given — through a stub ledger whose `Take` records a call once a transfer is added, or simply that `cfg.Ledger != nil`).

- [ ] **Step 1: Failing test** `TestBuildClientInstallsTheLedger`. **Step 2:** FAIL (signature). **Step 3: Implement. Step 4:** `go build ./... && go test ./app/grab/...` PASS.
- [ ] **Step 5: Commit** `-- app/grab/engine/usenet app/grab/agent/usenet` message `feat(grab): the usenet engine takes its connection grants from the shared KV ledger`.

### Task 8: Docs and gate

- [ ] CLAUDE.md "Gotchas": one entry (usenet transfers run under grants; a parked job is Queued/WaitingTurn; the pre-check is no longer a verdict; two DownloadClients on one account share a budget).
- [ ] Spec: "## 10. As built" with deviations.
- [ ] `golangci-lint-v2 run ./pkg/download/... ./app/grab/... ./pkg/events/...`; `go test ./pkg/download/... ./app/grab/... ./pkg/events/...`.
- [ ] Commit `-- CLAUDE.md docs/superpowers/specs/2026-10-16-usenet-connection-ledger-design.md docs/superpowers/plans/2026-10-16-usenet-connection-ledger.md`.
