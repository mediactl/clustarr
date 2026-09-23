# Phase F — M5 subtitles (captionarr)

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development.

**Goal:** every video MediaFile gets a SubtitleRequest; missing wanted languages are searched across providers under a shared throttle, scored Bazarr-style, post-processed, written as sidecars, and surfaced on the MediaFile.

**Architecture:** captionarr's controller role owns SubtitleProfile, SubtitleProvider and SubtitleRequest; its worker role consumes fetch tasks from JetStream. **captionarr's only output is `SubtitleRequest.status.items`** — catalogarr already turns those into `MediaFile.status.sidecars`. Provider throttle state lives in NATS KV, shared by every worker.

**Spec:** `docs/superpowers/specs/2026-09-18-clustarr-design.md` §4.6, §6.5, §16 M5. Amendment-1 is silent on subtitles, so the base spec governs unchanged.

**Prior art, verified 2026-09-23 against source.** Where this plan says a thing does not exist, it does not exist.

---

## Global Constraints

- Go 1.27, controller-runtime v0.25.1, k8s.io/* v0.37.0. GPL-3.0 header on every file.
- **All status writes go through `pkg/k8s.PatchStatus`**, each a **complete declaration** of its manager's owned set. Every variant in CLAUDE.md's "Gotchas" applies.
- **An over-claim is silent** — assert manager splits on `metadata.managedFields`.
- **Re-`Get` before applying** on any read-slow-work-apply path. The fetch worker searches several providers over the network: that is exactly the shape.
- **NATS KV keys go through `events.KVKeyToken`**, and any new key shape gets a contract test against a **real embedded server**, not the in-memory bus — the in-memory bus has no key grammar and has let an illegal key ship twice.
- **The caller owns rate limiting**: a library accepts an injected limiter and never defaults one on.
- **Every HTTP response body is read through a cap** (`io.LimitReader(body, max+1)` + `ErrResponseTooLarge`).
- `+kubebuilder:rbac` markers are **package-level comments**. envtest does not enforce RBAC.
- **No new Go dependencies** — confirmed none are needed. Never `go get`/`go mod tidy`.
- **No `-race`, no e2e runs, no kind clusters** (user instruction). e2e is **written, not run**.
- Commit with a pathspec: `git commit -m '…' -- <paths>`. Never `git add -A`/`commit -a`, never `git stash`. **Do not push.**

## What already exists — consume it, do not rebuild it

- `pkg/subtitles`: the `Provider` interface (`provider.go:38-44`), `Registry` with `For(kind)`, Bazarr `Score`/`MinScore`/`CandidateMatches`/`GuessMatches` with the weight tables verbatim, `PostProcess` (cue-scoped mods, SRT/ASS), `FixMojibake`, `RemoveHI`, `ThrottleFor(provider, err)` with Bazarr's duration table, `ProviderError` and sentinels, `Writer.Write` (atomic via `fsops.AtomicWrite`), `ParseLangKey`/`FormatLangKey`.
- Providers: `providers/opensubtitlescom`, `providers/gestdown`, `providers/embedded`.
- **catalogarr's half is done.** `catalogarr/controller/mediafile/mediafile_controller.go:468-503` `scanSidecars` lists SubtitleRequests for the file and `sidecars.go:36-57` turns items with state `downloaded|upgradable` and a non-empty `path` into `MediaFile.status.sidecars`, inside catalogarr's single status apply. `items[].path` is **relative to the media file's directory**.
- `pkg/pipeline` derives all four subtitle stages from `SubtitleRequest.status`, and `ui/projection` already indexes SubtitleRequests by owner. The pipeline page will light up as soon as requests exist.
- Plumbing: `ManagerCaptionarr`, `ManagerCaptionarrWorker`; `StreamWorkCaptionarr`, consumers `captionarr-fetch-high`/`-normal`, `FetchTaskSubject`, `MsgIDForSubtitle(requestUID, langKey, probeHash)`; `schema.FetchTask`, `schema.SubtitleEvent`; KV bucket `BucketProviderThrottle` = `clustarr-provider-throttle` (provisioned, **unused**).
- Secret-reading precedent: `catalogarr/controller/metadataprovider/registry.go:87,105`.

## Rulings

**R1 — captionarr writes `SubtitleRequest.status` and nothing on MediaFile.** catalogarr is the sole writer of `MediaFileStatus` and its sidecar projection already exists. A captionarr write to MediaFile would be a second writer on a resource whose one sanctioned exception is spec-vs-status, not status-vs-status.

**R2 — throttle state lives in the KV bucket, and the SubtitleProvider controller is the only writer of `SubtitleProvider.status`.** Workers record throttles, quota and auth failures into `clustarr-provider-throttle`; the provider controller projects KV into `status.throttledUntil/throttleReason/quota/errorsLast120s` and the conditions. A worker writing SubtitleProvider status directly would make every worker replica a writer of one object.

**R3 — fix the OpenSubtitles limiter default; give Gestdown an injectable one.** `providers/opensubtitlescom/client.go:86-87` defaults `rate.NewLimiter(5,5)` when none is passed, contradicting CLAUDE.md's rule — which *names OpenSubtitles* as a client that follows it. Remove the default and add a test mirroring `pkg/torznab/client_test.go:220`'s caller-owned-limiter test. Gestdown has no limiter at all; add the injection point, still defaulting to none.

**R4 — `status.items` is split per leaf between two managers, which is legal and fragile.** `subtitlerequest_types.go:127-129` gives the controller `nextSearchAt` and `attempts` and the worker every other leaf of each item, keyed `listType=map` by `langKey`. SSA tracks ownership per leaf, so this works — but each manager must re-send every key it owns on every apply, and **generated `With*` methods append**, so build one struct and render it once rather than seeding and then calling `WithItems`. `MediaFileStatus.Sidecars` hit exactly this.

**R5 — sync and Whisper are out of scope.** Both carry CEL forcing `enabled: false` in v1alpha1 (`subtitleprofile_types.go:241,280`). Do not implement them. Likewise `subdl`, `subsource` and `whisper` are `SubtitleProviderType` values with no client: a SubtitleProvider of those types reports `Ready=False` with a clear reason, not an error loop.

**R6 — fetch tasks are deduplicated by `MsgIDForSubtitle`.** The request controller publishes on every eligible reconcile; the deterministic MsgID lets the JetStream dedup window absorb repeats, as D2-8a did for import tasks.

---

## Tasks

### F-0 — `captionarr/status` (SERIAL, blocks everything)

**Files:** create `captionarr/status/`. Mirror `grabarr/status` — read it first.

Declarations: `ControllerFields` for SubtitleRequest (`phase`, `profileGeneration`, `probeHash`, `fileFingerprint`, `existing`, `conditions`, `observedGeneration`, and per item **only** `nextSearchAt`/`attempts`); `WorkerFields` for SubtitleRequest (every other item leaf); SubtitleProfile's and SubtitleProvider's status sets. `Patch` refuses any other manager. Test the split on `managedFields`, **including the per-leaf item split under R4** against an object whose items were written by both managers.

### F-1 — the shared provider throttle, and the limiter fixes

**Files:** create `captionarr/throttle/`; modify `pkg/subtitles/providers/opensubtitlescom/`, `pkg/subtitles/providers/gestdown/`.

A KV-backed throttle over `clustarr-provider-throttle`: a token bucket per provider so N workers together never exceed the provider's `requestsPerSecondMilli`, plus the throttle table (`ThrottleFor` durations, "5 errors in 120s") and OpenSubtitles' JWT/quota. Keys via `events.KVKeyToken`. **A contract test against a real embedded NATS server**, per the Global Constraints. Then **R3**.

### F-2 — the planner, and sidecar naming

**Files:** `pkg/subtitles/planner.go`, `pkg/subtitles/sidecarname.go`. Pure, no Kubernetes.

`SidecarName`/`ParseSidecar` (the package doc defers them to this phase) and Bazarr's missing-subtitle planner: existing = embedded text streams plus sidecars parsed right-to-left; wanted = the profile's languages minus existing, respecting cutoff, HI, forced, `audioExclude`/`audioOnlyInclude`. Table-driven — CLAUDE.md asks for tricky logic as pure functions.

### F-3 — SubtitleProfile and SubtitleProvider controllers

**Files:** `captionarr/controller/subtitleprofile/`, `captionarr/controller/subtitleprovider/`.

Profile: watch video-kind MediaFiles, ensure **one SubtitleRequest per file** (deterministic name, owner = MediaFile), keep `wantedKeys`/`matchingFiles`. Trigger on `status.probeHash` changes via `k8s.StatusFieldChanged` (`captionarr/run.go:207-208` names it). Provider: read the Secret by `secretRef` (keys `apiKey`/`username`/`password`, constants already declared), validate, and project KV throttle state into status per **R2**.

### F-4 — SubtitleRequest controller

**Files:** `captionarr/controller/subtitlerequest/`.

Replan when `profileGeneration` or `probeHash` is stale, using F-2's planner. For items with `nextSearchAt <= now`, publish a `schema.FetchTask` with `MsgIDForSubtitle` (**R6**) and move to `Searching`. Adaptive gate per §6.5 (`initial+3w > now` full cadence, else `latest+1w <= now`), and the **12h upgrade pass** (`score < outOf−3`, `minScore = score+1`). Owns `ControllerFields` only, including the two item leaves.

### F-5 — the fetch worker

**Files:** `captionarr/worker/fetch/`; fill `setupWorkers`.

Consume the two fetch consumers. For a task: build a `subtitles.Query` from the MediaFile, ask each provider from `Registry.For(kind)` in priority order **through F-1's throttle**, score with `CandidateMatches`/`Score`, reject below `MinScore`, download the winner, `PostProcess`, `Writer.Write` the sidecar next to the media file, then patch the item under `ManagerCaptionarrWorker` — **re-`Get` first**, since provider searches are slow. Map provider errors through `ThrottleFor` into the KV throttle, never into SubtitleProvider status (**R2**). Publish a `schema.SubtitleEvent`. Workers are **never leader-elected** (§6.5).

### F-6 — wiring, RBAC, readiness (SERIAL, after F-0..F-5)

Register every reconciler and both workers. RBAC markers for the three subtitle kinds incl. `/status`, Secrets read, MediaFiles read; `make manifests`; **sync the chart's RBAC between its BEGIN/END sentinels**. Add captionarr to `cmd/clustarr/runnable_registration_test.go` and prove each role reaches `/readyz` in `start_envtest_test.go` — the D2 equivalent found three components registered nowhere.

### F-7 — fixtures and e2e scenario 13 (written, not run)

Mock OpenSubtitles and Gestdown in `test/fixtures/`, following `torznabstub`'s pattern, deployed from `config/e2e`. Scenario 13, and scenario 1 extended through the SubtitleRequest. **Do not run.**

### F-8 — gate, CLAUDE.md Status, carried list

Mirror D1-10. Phase F paragraph with every identifier grepped; scenario 13 stated as **never executed**. File `subdl`/`subsource`/`whisper` having no client.

## Waves
0: F-0 · 1: F-1, F-2 · 2: F-3, F-4, F-5 · 3: F-6 · 4: F-7 · 5: F-8
