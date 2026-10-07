# Usenet Connection Budget Phase A Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** One usenet engine pod runs at most as many downloads as its primary servers' connections allow, at `connectionsPerDownload` connections each. An optional per-pod (per-node) byte rate caps it, and each server's traffic and penalties become visible.

**Architecture:** All of this lives in `pkg/download/usenet` and the CRD types, as clustarr-c3's unify branch asked (it dissolves `app/grab/run.go`). The changes:

- **Admission:** `Client` admits a job's transfer stage through a slot gate placed beside the existing priority gate (`waitForTurn`). A job waiting for a slot reads `Queued`.
- **Connections per download:** the per-job worker count becomes `connectionsPerDownload`.
- **Bandwidth:** one injected `rate.Limiter` in the `Pool` meters bytes read from every connection.
- **Visibility:** `ServerStats`, today internal, are published as metrics. A penalty is logged once with its NNTP code.

The engine only maps the two new fields in `BuildConfig`. All of `spec.usenet` is already in `engineConfigHash`, so an edit rolls the engine.

**Tech Stack:** Go, `golang.org/x/time/rate` (already a dependency: `go list -m golang.org/x/time` must show it, or the plan stops before Task 1), Prometheus client, envtest for the CRD.

**Spec:** `docs/superpowers/specs/2026-10-06-usenet-connection-budget-design.md`. It is approved with the recommended answers: `connectionsPerDownload` defaults to 10, there is no bandwidth cap until one is set, and phase B (several pods) comes later.

## Global Constraints

- GPL-3.0 header on every new Go file.
- No `go get`, no `go mod tidy`, and pathspec commits with `git add` for each new file.
- CRD typed-client rule (CLAUDE.md): optional fields are pointers with an `…OrDefault` accessor. No `+kubebuilder:default` that a Go zero would override.
- The caller owns rate limiting: `Config.Limiter` is injected and never defaulted on inside `pkg/download/usenet`.
- Metrics use the `clustarr_` prefix and base units, labelled by `client` and `server` (bounded configuration names), never a release name or title.
- `slots = Σ over non-backup providers ⌊connections / connectionsPerDownload⌋`, at least 1. Backup providers add no slots.
- Only the transfer stage holds a slot. Propagation delay, pre-check, repair, unpack and publish do not.

## Review Focus

1. **A job paused or outranked while it holds a slot** gives the slot back, so a paused download never blocks the queue.
2. **A job removed or cancelled while waiting** for a slot never leaks a slot.
3. **On restart (re-attach), every in-flight job competes for slots again.** They must not all start at once.
4. **`connectionsPerDownload` above a server's connection count** gives one slot, not zero.
5. **The bandwidth bound never drives slots to 0,** and is inactive until a rate has been measured.

---

### Task 1: The two CRD fields and the engine's mapping

**Files:**

- Modify: `api/download/v1alpha1/downloadclient_types.go` (`UsenetSpec`, plus accessors beside the type)
- Modify: `pkg/download/usenet/client.go` (`Config.ConnectionsPerDownload`, `Config.Limiter`)
- Modify: `app/grab/engine/usenet/config.go` (`BuildConfig`)
- Test: `app/grab/engine/usenet/config_test.go` (or the existing BuildConfig test file), `pkg/crdcheck` (runs as is)

**Interfaces:**

- Produces:
  - `UsenetSpec.ConnectionsPerDownload *int32` (1-256) and `UsenetSpec.MaxBytesPerSecond *resource.Quantity`;
  - `func (u UsenetSpec) ConnectionsPerDownloadOrDefault() int32` (10);
  - `Config.ConnectionsPerDownload int` and `Config.Limiter *rate.Limiter` (bytes per second, burst one article or 1 MiB).

- [ ] **Step 1: Write the failing test.** `BuildConfig` maps unset fields to 10 and a nil limiter, and maps `connectionsPerDownload: 4, maxBytesPerSecond: 50Mi` to 4 and a limiter at 52428800 B/s.

```go
func TestBuildConfigMapsTheConnectionBudget(t *testing.T) {
 dc := usenetClient(t) // the test file's existing builder of a usenet DownloadClient with one provider Secret
 cfg, err := BuildConfig(ctx, c, dc, "/data", "/scratch", "/publish")
 require.NoError(t, err)
 assert.Equal(t, 10, cfg.ConnectionsPerDownload)
 assert.Nil(t, cfg.Limiter)

 dc.Spec.Usenet.ConnectionsPerDownload = new(int32(4))
 q := resource.MustParse("50Mi")
 dc.Spec.Usenet.MaxBytesPerSecond = &q
 cfg, err = BuildConfig(ctx, c, dc, "/data", "/scratch", "/publish")
 require.NoError(t, err)
 assert.Equal(t, 4, cfg.ConnectionsPerDownload)
 require.NotNil(t, cfg.Limiter)
 assert.Equal(t, rate.Limit(52428800), cfg.Limiter.Limit())
}
```

- [ ] **Step 2: Run it.** Expected: FAIL to compile on `ConnectionsPerDownload`.
- [ ] **Step 3: Implement.**
  - Add the fields, each doc comment quoting the spec's §3.1.
  - Add `ConnectionsPerDownloadOrDefault`.
  - Run `make generate manifests`.
  - In `BuildConfig`: `cfg.ConnectionsPerDownload = int(us.ConnectionsPerDownloadOrDefault())`; if `us.MaxBytesPerSecond != nil && us.MaxBytesPerSecond.Value() > 0`, then `cfg.Limiter = rate.NewLimiter(rate.Limit(v), max(int(v)/10, 1<<20))`.
- [ ] **Step 4: Run.** `go test ./app/grab/engine/usenet/ && KUBEBUILDER_ASSETS=... go test ./pkg/crdcheck/`. Expected: PASS.
- [ ] **Step 5: Commit** the API, the generated files, `config/crd/bases` and the engine config.

### Task 2: Admission by slots and connections per download

**Files:**

- Create: `pkg/download/usenet/admission.go`
- Modify: `pkg/download/usenet/client.go` (`New`: workers from `ConnectionsPerDownload`; `Client.admission`), `pkg/download/usenet/segment.go` (`transfer` acquires a slot and releases it on return)
- Test: `pkg/download/usenet/admission_test.go`, `pkg/download/usenet/client_test.go` (with the package's stub NNTP server, `stub_test.go`)

**Interfaces:**

- Produces:
  - `func connectionSlots(providers []Provider, perDownload int) int`;
  - `type admission struct{ … }` with `acquire(ctx, j) error`, `release(j)` and `limit() int`.
- Consumes: Task 1's `Config.ConnectionsPerDownload`.

- [ ] **Step 1: Write the failing unit tests.**

```go
func TestConnectionSlots(t *testing.T) {
 p := func(conn int, backup bool) Provider { return Provider{Connections: conn, Backup: backup} }
 assert.Equal(t, 3, connectionSlots([]Provider{p(30, false)}, 10))
 assert.Equal(t, 10, connectionSlots([]Provider{p(30, false), p(70, false)}, 10))
 assert.Equal(t, 10, connectionSlots([]Provider{p(100, false), p(60, true)}, 10), "a backup adds no slots")
 assert.Equal(t, 1, connectionSlots([]Provider{p(8, false)}, 10), "never zero")
}
```

Then the client-level tests, using the stub server the client tests already run (read `client_test.go`'s helpers first):

- **Holding back the third download:** with one provider of 20 connections and `ConnectionsPerDownload: 10`, adding three jobs leaves the third in `download.StatusQueued` while two transfer. The third starts when one finishes.
- **A pause gives its slot back:** a paused transferring job lets a queued one start.
- **Removing a queued job** frees nothing it never held, and every slot comes back.

- [ ] **Step 2: Run them.** Expected: FAIL. `connectionSlots` is undefined, and all three jobs transfer at once.
- [ ] **Step 3: Implement** `admission` as a counter under a mutex with a `sync.Cond`-free poll at `turnPollInterval`, the same cadence as `waitForTurn`:
  - `acquire` sets the job's status to `StatusQueued` while it waits;
  - it takes the slot when `active < limit()`;
  - it returns `ctx.Err()` on cancel;
  - `release` decrements.

  `transfer` calls `acquire` after `waitForTurn` and defers `release`. Inside the worker loop:
  - a job that becomes paused or outranked releases its slot, and acquires one again before its next batch;
  - a `held` flag on the job keeps release idempotent.

  `New` sets `workers = cfg.ConnectionsPerDownload` (falling back to today's sum of every provider's connections when it is 0, for the package's own tests).
- [ ] **Step 4: Run.** `go test -race ./pkg/download/usenet/`. Expected: PASS.
- [ ] **Step 5: Commit.**

### Task 3: The bandwidth limiter and the bandwidth bound on slots

**Files:**

- Modify: `pkg/download/usenet/pool.go` (`NewPool` takes the limiter), `pkg/download/usenet/conn.go` (the article body read waits on the limiter), `pkg/download/usenet/admission.go` (`limit()` applies the bound)
- Test: `pkg/download/usenet/pool_test.go`, `pkg/download/usenet/admission_test.go`

- [ ] **Step 1: Write the failing tests.**
  - With a limiter of 1 MiB/s and a stub serving 4 MiB of articles, a transfer takes at least about 3 s.
  - `limit()` is `min(connectionSlots, max(1, ⌊limiter rate / measured per-download rate⌋))` once a rate is measured, and `connectionSlots` before.
  - A measured rate of 0 never divides.
- [ ] **Step 2: Run.** Expected: FAIL.
- [ ] **Step 3: Implement.**
  - The connection wraps its body reader so that every `Read` of n bytes calls `limiter.WaitN(ctx, n)`, chunked to the burst.
  - The admission's per-download rate is the moving average of active jobs' `rate.bps()`, refreshed at checkpoint.
- [ ] **Step 4: Run.** `go test -race ./pkg/download/usenet/`. Expected: PASS.
- [ ] **Step 5: Commit.**

### Task 4: Per-server traffic and penalties, visible

**Files:**

- Modify: `pkg/download/usenet/pool.go` (count bytes and penalties per server, log a penalty once with its code)
- Create: `app/grab/engine/usenet/metrics.go` (collectors reading `Client.Info`'s `ServerStats`)
- Test: `pkg/download/usenet/pool_test.go`, `app/grab/engine/usenet/metrics_test.go`

**Interfaces:** the metrics `clustarr_usenet_server_bytes_total{client,server}` and `clustarr_usenet_server_penalties_total{client,server,reason}`, where reason is `refused`, `auth` or `quota`.

- [ ] **Step 1: Write the failing tests.**
  - A stub server that answers 502 raises `penalties_total{reason="refused"}` by 1 and logs one line naming the server and the code.
  - Bytes served rise by the article bytes.
- [ ] **Step 2: Run.** Expected: FAIL.
- [ ] **Step 3: Implement.** `ServerStats` gains `Penalties map[string]int64`. The collector reads it on scrape, so it holds no state of its own.
- [ ] **Step 4: Run.** Expected: PASS.
- [ ] **Step 5: Commit.**

### Task 5: Docs, gate, deploy

- [ ] **Step 1: Docs.**
  - The spec gets an "As built (phase A)" section.
  - CLAUDE.md's downloads gotchas get one paragraph: slots, `connectionsPerDownload`, `maxBytesPerSecond` and the server metrics.
- [ ] **Step 2: Gate.** `make test` in a clean worktree (chart dependencies copied). Expected: exit 0.
- [ ] **Step 3: Push and tell clustarr-c3.** It rebases its unify branch onto this.
- [ ] **Step 4: Deploy and check.**
  - Deploy as the deploy toolkit does: refresh the values first, apply the CRDs server-side, then `helm upgrade`.
  - Read `clustarr_usenet_server_*` for frugal and easynews. A frugal `refused` count shows whether its 100 connections are over the plan.
