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

---

## Tasks

### D2-0 — dependencies, `pkg/download`, `grabarr/status` (SERIAL, blocks everything)
**Nothing else may run until this lands.** Adds every module the phase needs in one `go get` pass: `github.com/anacrolix/torrent`, and for usenet the set `docs/research/download.md` recommends after evaluation — verify each still resolves and record the chosen versions. Creates `pkg/download` (the `Client` interface from spec §7 plus `ApplyStatus`), and `grabarr/status` per R3. `hack/deps/deps.go` gets an entry per module not yet imported, with the task that retires it.

### D2-1 — `pkg/download` torrent client (anacrolix)
### D2-2 — `pkg/download` usenet client (NNTP, yEnc, PAR2 repair, unpack)
`pkg/fsops` already provides `Import`, `HardlinkOrCopy`, `MoveAtomic`, `AtomicWrite`, `Recycle`, `SafeRemove`, `EnsureFreeSpace` — use them; do not reimplement.

### D2-3 — `DownloadClient` controller (engine workload, DiskSpaceOK, blocklist sweep)
### D2-4 — `Download` controller (ClientRef pick, EngineReady wait, `status.engine` pin, finalizer)
### D2-5 — torrent engine (re-attach first, per-download dir, telemetry under `ManagerGrabarrEngine`)
### D2-6 — usenet engine (scratch, repair, extract, atomic rename into DataDir)
### D2-7 — `importarr` file-import worker (`ConsumerImportFile`, `Download.status.import`, MediaFile)
The only cross-group status write in the project. R6, R7, R8 all bind here.

### D2-8 — wiring, RBAC, readiness (SERIAL, after D2-1..D2-7)
The D1 equivalent found both of its phase's Criticals *outside* the package it scoped itself to. Registration is where inert code hides.

### D2-9 — fixtures: a BitTorrent seeder and an NNTP stub
### D2-10 — e2e scenarios 1 (through import), 2, 3, 4, 6 — **written, not run** while the deferral stands
### D2-11 — gate, CLAUDE.md Status, carried list

## Waves
0: D2-0 · 1: D2-1, D2-2, D2-3 · 2: D2-4, D2-7, D2-9 · 3: D2-5, D2-6 · 4: D2-8 · 5: D2-10 · 6: D2-11
