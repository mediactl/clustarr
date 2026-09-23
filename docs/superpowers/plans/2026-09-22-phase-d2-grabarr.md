# Phase D2 — grabarr and the file-import worker (M3)

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development.

**Goal:** a release chosen by catalogarr is downloaded by grabarr — over BitTorrent or Usenet — and imported into a root folder as a `MediaFile`.

**Architecture:** `grabarr` is a CRD watcher, not a bus subscriber: catalogarr already hands off by *applying a `Download` object*. A controller role owns `DownloadClient` and `Download`; engine roles (one StatefulSet per torrent client, a Deployment for usenet) do the transfers and write only telemetry. `importarr` gains the completed-download import worker on a consumer that is already tuned and waiting.

**Spec:** `docs/superpowers/specs/2026-09-18-clustarr-design.md` §6.3, §7, §16 M3; amendment-1 wins on conflict. Scope: `docs/superpowers/plans/2026-09-18-remaining-work.md:894-898`.

**Prior art that is verified, not assumed:** every fact below was checked against source on 2026-09-22. Where a plan says a thing does not exist, it does not exist.

---

## Global Constraints

- Go 1.27, controller-runtime v0.25.1, k8s.io/* v0.37.0. GPL-3.0 header on every file.
- **All status writes go through `pkg/k8s.PatchStatus`.** `.Status().Update()/.Patch()` are banned; forbidigo enforces it.
- **`+kubebuilder:rbac` markers must be package-level comments.** envtest does not enforce RBAC, so a misplaced marker passes every test and fails only on a real cluster.
- **No `float32`/`float64` in `api/`.** Cap every status list with `MaxItems`.
- **NATS KV keys** must match `^[-/_=\.a-zA-Z0-9]+$` plus no leading/trailing `.` and no `..` — build through `events.KVKeyToken`.
- **`Envelope.Key` is `<namespace>/<name>`.** Consumers `strings.Cut` on `/` and dead-letter on failure.
- **Never `go get`/`go mod tidy` outside D2-0.** They corrupt `go.mod` when run in parallel.
- **No `-race` runs; no e2e runs; no kind clusters** until D1–D3 implementation is complete (user instruction, 2026-09-22).
- Commit path-scoped. Never `git add -A`. Do not push, do not merge.

## Rulings made before implementation

**R1 — `DownloadPhase` transitions are a pinned contract, not a free choice.**
`catalogarr/controller/rollup/downloadoverlay.go:61` already maps every phase onto a catalog overlay, and its own comment says only the first two rows are exercised today and the rest are "wired for when grabarr lands". D2's transitions must match that switch exactly; a divergence silently corrupts catalogarr's phase computation. Any change belongs in both files in one commit.

**R2 — the grab handoff is object creation.** `catalogarr/worker/grab/perform.go:246` server-side-applies a `Download` under `k8s.ManagerCatalogarrGrab`. No NATS subject mentions grabarr. grabarr watches; it does not subscribe for this.

**R3 — two field managers, two disjoint owned sets, one declaration each.**
`ManagerGrabarr` owns phase and conditions; `ManagerGrabarrEngine` owns *only* the telemetry fields. Both are declared and neither has a writer yet. **D2-0 creates `grabarr/status` holding both declarations plus a `Patch` that refuses any other manager**, exactly as `indexarr/status` does — that package exists because D1 proved that two writers hand-building applies for one object is how each deletes the other's fields.

**R4 — an engine must not report ready before re-attach completes.** `grabarr/run.go:216` already says so: reporting ready early lets the controller hand an engine work it would double-download. Readiness gates on re-attach, and a test proves it with leader election on.

**R5 — creating a `DownloadClient` changes existing behaviour.** `catalogarr/worker/search/worker.go:498` already lists live `DownloadClient` objects and fails *closed* for protocols with no enabled client. Automatic search is inert today for that reason; the first enabled client turns it on. Expect e2e behaviour to change the moment D2-3 lands.

**R6 — the import consumer is pre-tuned.** `ConsumerImportFile` ("importarr-fileimport") exists with AckWait 60s, MaxDeliver 5, BackOff 30s/2m/10m/1h, MaxAckPending 4, Heartbeat 30s. Read it from the topology; do not restate or retune it.

**R7 — mirror `importarr/worker/rescan`, do not invent.** It already solves heartbeat-to-extend-AckWait, progress checkpointing into `BucketProgress`, `finalAttempt` against MaxDeliver, and terminal-vs-retry. File import is the same shape.

**R8 — these payload types exist with zero producers: `schema.DownloadEvent`, `schema.DownloadProgress`, `schema.ImportTask`.** Use them as defined; if a field is wrong, change it deliberately and say so. `events.BucketDedup` ("import fingerprints for re-import no-ops") is likewise declared and unused — D2-7 is its first user.

## Hazards carried from D1 — every one of these shipped at least once

- **A lost update is not an SSA release**, and no release-regression test can see one. Any path that reads an object, does slow work, then applies must re-`Get` immediately before the apply. This bit three tasks in D1; an engine writing telemetry after a long transfer is the same shape.
- **SSA tracks ownership per leaf inside a struct.** Declaring a parent while omitting its fields releases the siblings.
- **Two managers can co-own a field**, so a release test that does not drop the co-owner reports a false pass.
- **A typed Go client is never defaulted** — `omitempty` does nothing on a struct field, so every envtest fixture gets `0s` for durations. Floor what has no meaningful zero; do not floor what does.
- **Verify every identifier against source.** This plan's predecessor produced nine phantom identifiers and four phantom behaviours, all in prose written without a `go doc` check.

**R9 — the usenet stack departs from the research note, and D2-2 is bigger for it.**
`javi11/nntppool/v4` is **cgo-only** — re-verified rather than assumed: `CGO_ENABLED=0` fails on `rapidyenc.Format/State/Encoder`, `CGO_ENABLED=1` succeeds. `cmd/clustarr` is one binary, and `make build` and `images/Dockerfile.controller` are `CGO_ENABLED=0` onto distroless/static. That is the same constraint ADR-0003 R8 already settled for the SQLite driver, which is why `pkg/relindex` runs on pure-Go `modernc.org/sqlite`. Taking nntppool would mean a build-tag split or a second binary, for one client. So D2-0 took the research note's own fallback — `Tensai75/nntp` plus pure-Go `javi11/rapidyenc` — and **grabarr therefore writes its own pool, 430 failover, pipelining and quota accounting**, which nntppool would have supplied.

**Correction, from D2-2's implementation (dbdaf1b): the ruling stands but this paragraph's premise was wrong.** `Tensai75/nntp` cannot carry the connection at all, for three independent reasons found by building on it: its `*Conn.Body` canonicalises CRLF→LF, un-stuffs dots and swallows the terminating `.\r\n`, while `javi11/rapidyenc` is a **raw** NNTP decoder that un-stuffs itself and locates the article end by exactly that byte sequence — so the two libraries the plan paired cannot be composed; its `cmd()` cannot pipeline; and it exposes neither the underlying `net.Conn` nor a context, so there are no deadlines and no cancellation. `pkg/download/usenet/conn.go` therefore implements the RFC 3977 subset directly, importing the module only for `nntp.Error` so protocol failures keep the ecosystem's error shape. The cgo constraint that drove the ruling is unchanged and the ruling is unchanged; what was wrong was the assumption that a usable NNTP transport existed in pure Go off the shelf. Affirmed rather than revisited: consistency with ADR-0003 and a single static binary are worth more than the pool code. Cost if wrong: D2-2 carries connection-pool bugs that a mature library would not have, so its tests must cover the pool directly and not only the happy path.

---

## Tasks

### D2-0 — dependencies, `pkg/download`, `grabarr/status` (SERIAL, blocks everything)
**Nothing else may run until this lands.** Adds every module the phase needs in one `go get` pass: `github.com/anacrolix/torrent`, and for usenet the set `docs/research/download.md` recommends after evaluation — verify each still resolves and record the chosen versions. Creates `pkg/download` (the `Client` interface from spec §7 plus `ApplyStatus`), and `grabarr/status` per R3. `hack/deps/deps.go` gets an entry per module not yet imported, with the task that retires it.

Landed: `4ee4d76` (`pkg/download`), `6b2cdbd` (`grabarr/status`), `c0043e9` (six modules), `458ec3c` (RBAC). See R9 for the NNTP departure it forced.

### D2-1 — `pkg/download` torrent client (anacrolix)

**Files:** create `pkg/download/torrent/client.go`, `session.go`, `client_test.go`. **Produces:** `torrent.New(cfg Config) (download.Client, error)`.

Implement the full `download.Client` interface (`pkg/download/download.go:381`) over `github.com/anacrolix/torrent v1.61.0`. Every method's contract is written on the interface — read those doc comments as the spec, because several encode decisions that are not obvious:

- **`Add` is idempotent on the returned id.** Adding the same payload twice returns the same id and does not restart the transfer. This is what makes re-attach safe, so it is a test, not a hope. Use the infohash as the id; `AddRequest.ExpectedInfoHash`, when set, must be verified against the metainfo and mismatches rejected rather than silently accepted.
- **`Resume` on a non-paused transfer is a no-op returning nil**, not an error — the controller calls it from a level-driven reconcile that cannot know current state. Same for `SetSeedCriteria` after the goal is met.
- **`Item.CanMoveFiles` and `CanBeRemoved` drive the import and cleanup paths**, so they must reflect real torrent state, not optimism.
- Map anacrolix state onto `download.Status` (`download.go:98-120`) and `DownloadStage`. A torrent that is checking or fetching metadata is not `StatusDownloading`.
- Populate `Seeders`/`Peers`/`RatioMilli`/`SeedTime`. `RatioMilli` is thousandths — **no floats reach `api/`**.

Tests run against a real in-process anacrolix instance seeding to itself over loopback — no network, no fixture server. Prove idempotent `Add`, the infohash mismatch rejection, and that a paused transfer reports `StatusPaused` and transfers nothing.

### D2-2 — `pkg/download` usenet client (NNTP, yEnc, PAR2 repair, unpack)

**Files:** create `pkg/download/usenet/client.go`, `pool.go`, `nzb.go`, `segment.go`, `repair.go`, plus tests. **Produces:** `usenet.New(cfg Config) (download.Client, error)`.

**This is the largest task in the phase, and larger than the plan first assumed — read R9 before starting.** The chosen stack is `github.com/Tensai75/nntp v0.1.5` plus pure-Go `github.com/javi11/rapidyenc`, which means **this task writes the connection pool itself**: `javi11/nntppool` would have provided it but is cgo-only and cannot be used (R9). Budget for it.

What the pool owes the rest of the system:
- **A bounded connection pool per server**, honouring `UsenetSpec`'s connection limit. Connections are expensive and providers enforce the cap by disconnecting.
- **`430 No Such Article` failover**: an article missing on one server is fetched from the next configured server, in priority order, before the segment is declared lost. This is the single most important behaviour for real-world completion rates and it is the one a naive implementation omits.
- **Pipelining** — request the next article before the current body finishes — because round-trip latency, not bandwidth, is the limit on a high-latency provider.
- **Quota accounting** so `Info.FreeBytes` and the client's own counters mean something.

Then: parse the NZB, fetch and yEnc-decode segments into a scratch area, verify and repair with PAR2, and unpack. **PAR2 adds no Go module** — `hack/deps/deps.go:56-58` records the decision: `images/Dockerfile.media` already ships `par2cmdline-turbo` v1.5.0 and it is invoked as a binary. RAR unpacking is `github.com/nwaples/rardecode/v2`.

`Item.Health` (`*UsenetHealth`) is this client's to populate — article completion and repair state — and it is the only protocol-specific status block in `DownloadStatus`.

**`pkg/fsops` already provides `Import`, `HardlinkOrCopy`, `MoveAtomic`, `AtomicWrite`, `Recycle`, `SafeRemove`, `EnsureFreeSpace` — use them; do not reimplement.** Anything that must survive a crash goes through `fsops.AtomicWrite`, which fsyncs the file and its parent.

Tests use an in-process NNTP stub over loopback. **Prove the 430 failover by having the stub refuse an article on server one and serve it on server two**, and prove the pool never exceeds its connection cap under concurrent demand. An `IsEncrypted` release must be reported, not silently unpacked into garbage.

### D2-3 — `DownloadClient` controller (engine workload, DiskSpaceOK, blocklist sweep)

**Files:** create `grabarr/controller/downloadclient/`. **Field manager:** `ManagerGrabarr` only — the engine's set is disjoint and belongs to D2-5/D2-6.

Reconciles `DownloadClient` into the engine workload it describes: a StatefulSet per torrent client (stable identity, because a torrent client owns on-disk state it must re-attach to) and a Deployment for usenet (stateless fetchers). Owns `status.conditions` including `DiskSpaceOK`, and the blocklist sweep.

**R5 binds here and changes live behaviour the moment this lands:** `catalogarr/worker/search/worker.go:498` already lists `DownloadClient` objects and fails *closed* for protocols with no enabled client. Automatic search is inert today for exactly that reason, so the first enabled client switches it on. Say so in the report.

Status writes go through `grabarr/status.Patch` (R3) — **every apply is a complete declaration of `ControllerFields`**. Note that a double-claim against the engine's manager will NOT surface as a conflict: `pkg/k8s` forces ownership unconditionally, so an over-claim is silent and visible only in `metadata.managedFields`. Assert there, not on values.
### D2-4 — `Download` controller (ClientRef pick, EngineReady wait, `status.engine` pin, finalizer)

**Files:** create `grabarr/controller/download/`. **Field manager:** `ManagerGrabarr`, `ControllerFields` only.

Owns the `Download` lifecycle: pick a `DownloadClient` matching `spec.protocol` and category, wait for that client's engine to report ready, pin the choice into `status.engine` so a later reconcile cannot silently migrate a running transfer, and run a finalizer honouring `spec.removeDataOnDelete`.

**The phase set is closed and pinned (R1).** The eleven values are `Pending`, `Assigned`, `Queued`, `Downloading`, `Paused`, `Completed`, `Seeding`, `Imported`, `Failed`, `Blocklisted`, `Removing` (`api/download/v1alpha1/download_types.go:75-98`). `catalogarr/controller/rollup/downloadoverlay.go:66-76` already switches on all of them and its `default` branch silently means "no overlay" — so an unhandled or invented phase does not error, it makes the movie's rollup quietly wrong. Your transitions must match that switch exactly, and any change to either file belongs in **both, in one commit**.

**R4 binds here:** wait for `EngineReady` before handing work over. An engine that reports ready before re-attach completes gets handed a transfer it is already running, and downloads it twice.

Test against envtest with a fake `download.Client`. Prove: the engine pin survives a reconcile that would otherwise pick differently; a Download whose client disappears does not lose `status.engine`; and the finalizer path both with and without `removeDataOnDelete`.

### D2-5 — torrent engine (re-attach first, per-download dir, telemetry under `ManagerGrabarrEngine`)

**Files:** create `grabarr/engine/torrent/`. **Field manager:** `ManagerGrabarrEngine` — **telemetry fields only**, and nothing the controller owns.

Runs as the StatefulSet workload D2-3 creates. **Re-attach is the first thing it does and readiness gates on it** (R4). Consumes `pkg/download/torrent` from D2-1.

Three traps, all already paid for once:
- **`Files` and `Conditions` append.** `EngineFields` seeds `Files`, so a `mutate` needing a different list must **assign `ac.Files`**, never call `WithFiles` — D2-0 documented this on `Patch` after `MediaFileStatus` hit the identical bug in Phase C.
- **Re-`Get` immediately before applying.** Any path that reads an object, does slow work, then applies a status seeded from that read will silently roll back whatever another writer did meanwhile. This is a *lost update*, not an SSA release: every field is declared, just with stale values, and **no release-regression test in this tree can see it**. `indexarr/worker/rss/worker.go` does the re-Get with the comment "the poll closes the window".
- **An over-claim is silent**, because `pkg/k8s` forces ownership. Assert the controller/engine split on `metadata.managedFields`, never on object values — values catch under-declaration only.

D2-1's carried notes land here: seed-criteria and `CanBeRemoved` are implemented but untested until a real controller loop drives them, and the `DownloadPriority` → anacrolix connection-budget mapping is an unsourced judgement call to confirm or correct.

### D2-6 — usenet engine (scratch, repair, extract, atomic rename into DataDir)

**Files:** create `grabarr/engine/usenet/`. **Field manager:** `ManagerGrabarrEngine`, telemetry only. Consumes `pkg/download/usenet` from D2-2.

Runs as the Deployment D2-3 creates — stateless fetchers, unlike torrent's StatefulSet, because a usenet transfer owns no long-lived on-disk identity. Downloads into a scratch area, PAR2-verifies and repairs, extracts, then **renames atomically into `DataDir`** so a partially-extracted release is never visible to the import worker. Use `pkg/fsops` (`MoveAtomic`, `AtomicWrite`, `EnsureFreeSpace`) — do not reimplement.

The same three traps as D2-5 apply verbatim; re-read them there. `status.health` (`*UsenetHealth`) is this engine's to report.

### D2-7 — `importarr` file-import worker (`ConsumerImportFile`, `Download.status.import`, MediaFile)

**Files:** create `importarr/worker/fileimport/`. **The only cross-group status write in the project** — R6, R7 and R8 all bind here.

Consumes `ConsumerImportFile` (`"importarr-fileimport"`, `pkg/events/subjects.go:84`, durable pull consumer on `StreamWorkImportarr`, `topology.go:526`). Imports a completed download into its root folder and creates the `MediaFile`.

**The MediaFile spec/status split is an invariant, not a convention** (CLAUDE.md): importarr creates the resource and owns `MediaFileSpec` — observed path, size, fingerprint, plus quality, revision, formatScore, matchedFormats and releaseType **frozen at import**; `catalogarr` is the sole writer of all of `MediaFileStatus`. Both use their own field manager so the apiserver enforces the split. Do not write any part of `MediaFileStatus`.

**Settle the `status.import` ownership contradiction as part of this task.** Three comments disagree today: `ManagerImportarr`'s comment and this task say importarr; `ManagerCatalogarr`'s comment and `DownloadStatus`' own field doc say catalogarr. No writer exists, so nothing has decided it. Pick one, fix **all three** comments in one commit, and re-run `make manifests` — the field comment is CRD description text, so leaving it stale ships a lie in `kubectl explain`.

`Envelope.Key` is `<namespace>/<name>`: `strings.Cut` on `/`, dead-letter on failure. **Never guess** — an unattributable file goes to `LibraryScan.status.unmatched` with a reason, never a speculative item.

### D2-8 — wiring, RBAC, readiness (SERIAL, after D2-1..D2-7)

**Files:** `grabarr/run.go`, `cmd/clustarr/services.go`, `cmd/clustarr/all.go`, `Makefile` (`RBAC_DIRS`), `config/`, `charts/`.

Registers every controller, worker and engine behind its role flag; adds `grabarr` to `RBAC_DIRS` and regenerates; per-service readiness that runs on **every replica, not only the leader**; `k8s.WithBusHooks(obs.BusHooks())` on `k8s.ConnectBus` (AST-guarded — the guard will tell you if you miss it).

**The D1 equivalent of this task found both of its phase's Criticals *outside* the package it scoped itself to.** Registration is where inert code hides: a controller nobody registers passes every unit test it has. Explicitly verify each new runnable is actually reachable from `clustarr all` and from its own subcommand.

D2-3 deliberately left its markers unregenerated and reported them; collect those. envtest does **not** enforce RBAC, so a missing marker passes every suite and fails only on a real cluster.

### D2-9 — fixtures: a BitTorrent seeder and an NNTP stub

**Files:** `test/fixtures/seeder/`, `test/fixtures/nntpstub/`, `images/Dockerfile.fixture`.

Neither exists. Both run in-cluster with no Internet, mirroring `test/fixtures/torznabstub/`, which is the working pattern to copy. The seeder serves a known torrent over loopback/cluster networking; the NNTP stub serves a known NZB's articles and **must be able to refuse an article on one server and serve it on another**, so D2-10 can exercise 430 failover end to end rather than only in D2-2's unit tests.

### D2-10 — e2e scenarios 1 (through import), 2, 3, 4, 6 — **written, not run**

**Files:** `test/e2e/download_test.go`, `test/e2e/import_test.go`, build tag `e2e`.

The phase gate: a wanted movie searched, a release grabbed, downloaded against a local fixture, imported into a root folder, with a `MediaFile` created.

**Do not run these.** e2e execution is deferred by explicit user instruction until D1-D3 implementation is complete. Write them to be correct on first execution and state plainly in the report that they are unexecuted.

Note that `test/e2e/helpers_test.go`'s `newRankedQualityProfile` is now back on CRD defaults (9f344d6) after its language workaround was retired — releases must genuinely pass the language checks rather than stepping around them.

### D2-11 — gate, CLAUDE.md Status, carried list

Mirror what D1-10 did (commit 3038552): `make generate`, `make manifests`, `make build`, `make lint`, scoped `go test` with `KUBEBUILDER_ASSETS` exported — **a suite finishing in milliseconds skipped**. Then a Phase D2 paragraph in CLAUDE.md's `## Status`, in the same voice as the Phase B/C/D1 paragraphs: name the packages and what was proven, not adjectives, and **check every claim against source before writing it** — D1 caught nine phantom identifiers and four phantom behaviours in its own briefs.

State plainly that the e2e scenarios are written and **never executed**, and do not write "proven end to end" for anything that has not run on kind.

## Waves
0: D2-0 · 1: D2-1, D2-2, D2-3 · 2: D2-4, D2-7, D2-9 · 3: D2-5, D2-6 · 4: D2-8 · 5: D2-10 · 6: D2-11
