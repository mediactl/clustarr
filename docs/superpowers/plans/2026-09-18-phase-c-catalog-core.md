# Clustarr Phase C — Catalog Core and Library Rescan Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make Clustarr reconcile for the first time. Bring the catalog to life — Movie, Series, Episode, MediaFile and the five configuration kinds, the metadata gateway, the decision engine, the search, grab, RSS and cron workers — and bring an existing library under management through `importarr`'s rescan, without ever guessing. Wire trace propagation through the bus, and stand up the end-to-end harness on kind with this milestone's three scenarios.

**Architecture:** Controllers own custom resources one writer apiece (the sole exception is `MediaFile`, split by field manager between what `importarr` observed and what `catalogarr` decided). Kubernetes watches are the default coupling; NATS carries only rate-limited or long-running work, the release firehose, RPC and the KV leases. Every decision that is hard to get right — availability, the release evaluation, delay-profile resolution, ranking, file classification — is a pure function with table tests, wrapped in a thin reconcile. Phase B's libraries do the domain work; this phase is the wiring, the lifecycle and the concurrency.

**Tech Stack:** Go 1.27, controller-runtime v0.25.1, k8s.io/* v0.37.0, envtest against a real apiserver (Kubernetes 1.37.0), NATS JetStream, `github.com/robfig/cron/v3` v3.0.1, testify, kind for the end-to-end suite.

**Spec:** `docs/superpowers/specs/2026-09-18-clustarr-design.md` — §4 (CRD fields and CEL), §5 (events, streams, KV), §6.1 (catalogarr), §8.1 (want), §8.2 (search → decide → delay → grab), §8.4 (import), §8.7 (RSS, lists, cron), §8.8 (failure handling), §9 (quality), §14 (testing). The amendment `…-amendment-1.md` **wins where they disagree**: §A1 is `importarr` (ownership, the never-guess rule, `LibraryScan`), §A2 observability. Phase context and the Phase H end-to-end design: `docs/superpowers/plans/2026-09-18-remaining-work.md`.

## Global Constraints

Copied from the spec and `CLAUDE.md`. Every task's requirements implicitly include this section.

- Module `github.com/mediactl/clustarr`. Go 1.27. Licence **GPL-3.0**; every Go file starts with the header in `hack/boilerplate.go.txt`.
- **One controller-writer per resource.** Sole exception: `MediaFile` — `importarr` owns `status.file` and `status.probe`, `catalogarr` owns `status.quality`, `status.formatScore` and the conditions, split by field manager on disjoint fields.
- **Every status write goes through `pkg/k8s.PatchStatus`** (server-side apply, named field manager). `.Status().Update()` and `.Status().Patch()` are banned outside `pkg/k8s`; golangci-lint's forbidigo rule fails the build on them.
- **No `float32`/`float64` under `api/`.** `resource.Quantity` for decimals a human types; scaled integers (`Milli`, `Centis`, `Percent`) for telemetry.
- **Cap every status list** with `+kubebuilder:validation:MaxItems`. Unbounded status lists are how operators melt etcd.
- **Kubernetes watches are the default coupling**; NATS is the exception, for rate-limited or long-running work, RPC, the release firehose and KV leases.
- **The scanner never guesses.** An unattributable file goes to `LibraryScan.status.unmatched` with a reason, never a speculative item.
- Controllers: `RequeueAfter` only, `reconcile.TerminalError` for an invalid spec, `RecoverPanic`, conditions carrying `observedGeneration`, Events through the manager's recorder, a 5-minute reconciliation timeout. Workers: `events.Retry(after)` → nak with delay, a generic error → backoff nak, `events.Discard` → DLQ, heartbeats on long tasks. (Spec §8.8.)
- Logging is `slog` through `context` (`pkg/obs/logging.FromContext`); no package-level logger, no logger struct field. Spans wrap every `Reconcile`, work handler and outbound provider call. Metrics use the `clustarr_` prefix and base units and are **never labelled by title, path or release name**.
- Tests are table-driven with testify; fixtures under `testdata/`. **No network in tests.** envtest suites need `KUBEBUILDER_ASSETS`; a suite finishing in milliseconds **skipped**, which is not a pass. Anything that shells out to `ffprobe`/`ffmpeg` skips when the binary is absent AND has a fixture-driven path that runs without it.
- Generated code stays clean: `make generate && make manifests` must leave no diff.

### Rules for parallel agents

1. **Never run `go get` or `go mod tidy` from a worker.** Task C0 adds every dependency serially, up front.
2. **Each task owns disjoint paths**, listed per task. `go.mod`, `go.sum`, `hack/deps/`, `api/` and the service `run.go` files belong to the controller.
3. **Commit path-scoped:** `git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "<msg>" -- <your paths>`. Never `git add -A`, never `git commit -a`. On `.git/index.lock`, wait five seconds and retry; never delete it.
4. **Never `go build` without `-o`** — a bare `go build` drops a binary in the repo root. Use `go build ./<pkg>/...`.
5. **Verify, do not trust.** The controller re-runs every gate itself after workers report.

## Execution order: four waves

| Wave | Tasks | Starts when |
| --- | --- | --- |
| 0 | C0 — dependencies, the carried library debt, and the `api/common` `ChannelLayout` field | first; controller runs it |
| 1 | C1 trace, C2 `pkg/decision`, C4 configuration controllers, C5 metadata gateway, C6 Movie/Series/Episode, C7 MediaFile | C0 committed |
| 2 | C8 Search, C9 grab/delay/cron/RSS, C10 importarr rescan | every wave-1 task has passed its review |
| 3 | C11 end-to-end harness and scenarios 5, 7 and 8 | every wave-2 task has passed its review |
| 4 | C12 — wiring, RBAC from markers, readiness, the phase gate | C11 passed |

Wave 1's tasks are decoupled by design: C6 publishes metadata work and C5 consumes it, but they meet at a bus subject rather than a Go symbol, so both can be built at once against the contract each states. Wave 2's tasks consume `pkg/decision` and the delay resolution as real code.

## File structure

| Task | Owns | Primary source |
| --- | --- | --- |
| C0 (serial) | `go.mod`, `go.sum`, `hack/deps/`, `hack/gen-catalogue/`, `pkg/quality`, `pkg/release`, `api/common/v1alpha1` | this plan |
| C1 | `pkg/events/`, `pkg/obs/bootstrap.go`, `docs/observability.md` | work order, §A2.2 |
| C2 | `pkg/decision/`, `testdata/decision/` | spec §7, §8.2, §9 |
| C4 | `catalogarr/controller/{rootfolder,qualityprofile,delayprofile,metadataprovider,importexclusion}/` | spec §4, §6.1 |
| C5 | `catalogarr/metadata/` | spec §6.1, §8.1 |
| C6 | `catalogarr/controller/{movie,series,episode}/` | spec §8.1 |
| C7 | `catalogarr/controller/mediafile/` | spec §8.4–8.6, §A1.3 |
| C8 | `catalogarr/controller/search/`, `catalogarr/worker/search/` | spec §8.2 |
| C9 | `catalogarr/worker/{grab,rssmatcher}/`, `catalogarr/controller/wantedcron/` | spec §8.2, §8.7 |
| C10 | `importarr/controller/{libraryscan,rootfolderschedule}/`, `importarr/worker/rescan/` | amendment §A1 |
| C11 | `test/e2e/`, `test/fixtures/`, `config/e2e/`, `images/Dockerfile.e2e-fixtures`, `hack/e2e.sh` | Phase H in the remaining-work plan |
| C12 (serial) | `catalogarr/run.go`, `importarr/run.go`, `config/rbac/`, `charts/`, `CLAUDE.md` | this plan |

**The search RPC is a seam, not a dependency.** `indexarr` does not exist until Phase D. Task C8 defines the search interface it needs, ships a fake for tests, and states the contract Phase D must satisfy. No Phase C task builds an indexarr client.

---
### Task C0: Serial dependency pre-add and carried library debt (controller runs this; not dispatched)

**Files:**
- Modify: `go.mod`, `go.sum`, `hack/deps/deps.go`
- Create: `hack/gen-catalogue/main.go`
- Modify: `pkg/quality/catalogue/catalogue.go` (ExceptLanguage), `pkg/release/language.go` (Chinese)
- Test: `hack/gen-catalogue/main_test.go`, plus additions to the existing catalogue and release suites

**Path ownership:** controller only. Runs to completion and is committed before any of C1–C11 is dispatched, because it touches `pkg/quality`, `pkg/release` and `go.mod`, which no other Phase C task owns.

**Interfaces — Produces:** `github.com/robfig/cron/v3` present in `go.mod` at the pinned version for Tasks C9 and C10; `quality.Condition.ExceptLanguage` evaluated rather than ignored; `release.parseLanguages` able to detect Chinese; `hack/gen-catalogue` able to regenerate the embedded catalogue from the vendored corpus.

- [ ] **Step 1: Pre-add the one new dependency, pinned**

```bash
cd /home/appkins/src/mediactl/clustarr
go get github.com/robfig/cron/v3@v3.0.1
```
Version verified 2026-09-18 with `cd /tmp && go list -m -versions github.com/robfig/cron/v3`. Add `_ "github.com/robfig/cron/v3"` to `hack/deps/deps.go` so `go mod tidy` cannot strip it before C9/C10 import it, then `go mod tidy && go mod tidy -diff` and confirm it survived.

- [ ] **Step 2: `hack/gen-catalogue` (carried out of Phase B)**

Spec §7 and §9 say `pkg/quality/catalogue`'s embedded data is *generated* from `testdata/trash/docs/json/{radarr,sonarr}/cf`; Phase B hand-authored it instead and proved equivalence with a parity test. Write the generator so the data has a reproducible source: read the vendored corpus at the commit in `testdata/trash/COMMIT`, emit the same JSON shape the loader already consumes, and write it into `pkg/quality/catalogue/data/`. The existing `parity_test.go` is the acceptance test — after generating, it must still pass unchanged, and `git status` must show no diff against the hand-authored files. If a file does differ, the generator is wrong (or found a real transcription error); investigate before overwriting, and record which it was.

- [ ] **Step 3: Evaluate `Condition.ExceptLanguage`**

`pkg/quality/catalogue/catalogue.go` decodes `ExceptLanguage` but never reads it (a `TODO(generator sync)` marks the spot). TRaSH uses it for "this language, except when …" formats. Implement it in `evalCondition` with the semantics the corpus's own use of the field implies — read the corpus entries that set it before choosing — and add table cases covering set, unset and negated.

- [ ] **Step 4: Chinese in `release.parseLanguages`**

Phase B's language table carries Chinese (Radarr id 10) but `pkg/release/language.go` cannot detect it, so the `anime-dual-audio` custom format's language group can never fire for a Chinese release. Add the detection (the usual scene tokens: `CHS`, `CHT`, `GB`, `BIG5`, `Chinese`, `中文`, plus the dual-audio combinations already handled for Japanese), with table cases including a negative that must not match.

- [ ] **Step 5: Declare the metric series Phase C adds**

`pkg/obs/metrics` is a shared path that no wave task can own safely — `newCounterVec` is
unexported, so a task needing a new series would have to edit the shared file while five
other agents commit around it. Declare them here instead, exactly as dependencies are
pre-added here.

Phase C needs one series that the Phase A catalogue does not already carry, from spec §13:

```go
// MetadataCacheHitsTotal counts gateway cache outcomes by tier. Never labelled
// by title or id -- the tier is the whole cardinality budget.
var MetadataCacheHitsTotal = newCounterVec(
    "clustarr_metadata_cache_hits_total",
    "Total metadata cache lookups, by tier and outcome.",
    "tier", "outcome",
)
```

Add it to the `all` collector slice so `Register` picks it up, extend the cardinality-guard
test, and update the metric table in `docs/observability.md` so the documented catalogue and
the code still agree. Every other series Phase C touches (`WorkQueuePending`,
`ReconcileErrorsTotal`, `SearchDecisionsTotal`, `ImportFilesTotal`) already exists — read
`go doc ./pkg/obs/metrics` and reuse them rather than declaring near-duplicates.

- [ ] **Step 6: Correct the MediaFile ownership documentation**

CLAUDE.md's invariant, amendment §A1.3 and `pkg/k8s/fieldmanager.go`'s doc comments all
describe `importarr` owning `status.file`/`status.probe` and `catalogarr` owning
`status.quality`/`status.formatScore` on MediaFile. **None of those four fields exist.**
`MediaFileStatus` is flat (`ObservedGeneration`, `Conditions`, `ProbeHash`, `ProbedAt`,
`MediaInfo`, `Sidecars`, `Transcode`) and all six "decided" fields live in `MediaFileSpec`,
frozen at import — which is exactly what spec §8.4 says. The documented split described an
API that was never shipped, and survived three phases because nothing reconciled yet.

Fix the documentation to match the code:
- **CLAUDE.md's invariant:** importarr creates the MediaFile and owns `MediaFileSpec`;
  catalogarr is sole writer of all of `MediaFileStatus` and takes over `sizeBytes`,
  `modTime` and `original` only after it incorporates a transcode swap. The two-writer
  discipline and its mandatory envtest remain — the split is spec-versus-status plus that
  three-field handover.
- **`pkg/k8s/fieldmanager.go`:** correct `ManagerImportarr`/`ManagerImportarrWorker` and
  `ManagerCatalogarr`'s doc comments the same way.
- **The amendment:** add a dated erratum note under §A1.3 pointing at the real type rather
  than silently rewriting a design of record. Say what was believed, what is true, and why
  the discipline still holds.

- [ ] **Step 7: The `api/` changes and the shared `pkg/events` names**

Every `api/` change in Phase C happens here, once, so no two parallel agents race on
regenerated files. Two changes:

1. **`api/common/v1alpha1.AudioStream` gains `ChannelLayout string`.** `pkg/mediainfo` reads
   ffprobe's real channel layout and gets `2.1` and `6.1` right, while `pkg/naming`'s
   channel-count lookup cannot — it has no layout to read. Add the field and populate it
   where MediaFile spec is written. `pkg/naming` consuming it is Phase G's follow-up.
2. **`api/catalog/v1alpha1.MovieAddMethod` gains a `scan` value.** A movie discovered by
   importarr's library scan is not `manual`, and labelling it so is a small lie that costs
   somebody an hour later. Add the enum value, and have Task C10's scanner use it.

Then `make generate && make manifests` and confirm `git status` is clean afterwards.

Also declare the KV names Task C10's ImportExclusion lookup needs, in `pkg/events`,
mirroring the existing `LeaseKey`/`PendingKey` pattern — `ExclusionKey`, `ExclusionEntry`
and the `BucketImportExclusions` bucket spec. They live here rather than in Task C1 or C10
because `pkg/events` is Task C1's path and two wave tasks must not collide on it.

- [ ] **Step 8: Gate and commit**

```bash
go build ./... && go vet ./...
export KUBEBUILDER_ASSETS=$(/home/appkins/go/bin/setup-envtest use 1.37.0 -p path)
make generate && make manifests && git status --short   # must be clean
go test -count=1 -race ./pkg/quality/... ./pkg/release/... ./pkg/events/... ./pkg/obs/... ./hack/...
make lint
go mod tidy -diff
git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "chore(deps): pre-add cron for Phase C; close the Phase B library carries" -- go.mod go.sum hack/deps/deps.go hack/gen-catalogue pkg/quality pkg/release pkg/events pkg/obs pkg/k8s/fieldmanager.go api config docs CLAUDE.md
```

**Done when:** the two `api/` changes are in with a clean regeneration; the ImportExclusion KV names exist in `pkg/events`; the MediaFile ownership documentation matches the generated type in all three places; the metadata cache series is declared, registered and documented; cron is in `go.mod` and kept alive by the keeper file; `hack/gen-catalogue` reproduces the embedded data with the parity test green and no diff; `ExceptLanguage` is evaluated and tested; Chinese is detected and tested; lint and the touched suites are green.

---

## Wave 1 — independent (parallel, after C0)

### Task C1: Trace propagation across the bus

**Files:**
- Modify: `pkg/events/bus.go` (or wherever the `Bus` interface and its options live — read the package first), `pkg/events/natsbus/natsbus.go`, `pkg/events/membus/membus.go`
- Modify: `pkg/events/contracttest/` (one new contract case, so both buses are held to it)
- Modify: `pkg/obs/bootstrap.go`
- Modify: `docs/observability.md` (the Status banner)
- Test: `pkg/events/contracttest/`'s new case, `pkg/obs/bootstrap_test.go`

**Path ownership:** `pkg/events/`, `pkg/obs/bootstrap.go`, `docs/observability.md`. No other Phase C task owns these.

**Read first:** the Phase C work order's "Also in this phase" paragraph in `docs/superpowers/plans/2026-09-18-remaining-work.md`; amendment §A2.2 (tracing) and §A4; `docs/observability.md`'s current Status banner; `go doc ./pkg/events` and `go doc ./pkg/obs/tracing`; `pkg/events/envelope.go` lines 27, 52-53, 98 and 138, where `HeaderTrace` (`Clustarr-Trace`) and `Envelope.Trace` already exist and already round-trip through the wire headers.

**Dependencies:** none new. Everything needed is in the tree.

**Interfaces — Consumes:** already present and exactly the right shape, which is why this task is small:
```go
// pkg/obs/tracing
func Inject(ctx context.Context, e *events.Envelope)
func Extract(ctx context.Context, e *events.Envelope) context.Context
```

**Interfaces — Produces:**
```go
// pkg/events -- hooks the bus calls, supplied by the service wiring.
// pkg/events MUST NOT import pkg/obs; these are plain function fields, so the
// dependency points the other way and tracing needs no change at all.
type Hooks struct {
    // BeforePublish runs on every outbound envelope, before it is encoded.
    // pkg/obs/tracing.Inject satisfies it as written.
    BeforePublish func(ctx context.Context, e *Envelope)
    // AfterReceive runs on every inbound envelope, before the handler, and
    // returns the context the handler is given.
    // pkg/obs/tracing.Extract satisfies it as written.
    AfterReceive  func(ctx context.Context, e *Envelope) context.Context
}
```
Plus the option that installs them on each bus (match the package's existing option style — read it rather than inventing one).

**Why hooks and not call sites.** The work order settles this: wire it *inside* `pkg/events` so no publisher or consumer can forget. A nil hook is a no-op, so the buses stay usable in tests without observability.

- [ ] **Step 1: Failing contract case**

Add to `pkg/events/contracttest/` a case that both `membus` and `natsbus` must pass: publish with a `BeforePublish` hook that stamps `e.Trace`, consume with an `AfterReceive` hook that reads it, and assert the handler's context carries what the publisher put there — and that with no hooks installed nothing is stamped and nothing breaks. Run it: both buses fail, because neither calls a hook yet.

- [ ] **Step 2: Implement the hooks in both buses**

`BeforePublish` runs on the envelope before encoding, so the header is on the wire. `AfterReceive` runs before the handler and its returned context is what the handler receives; a nil return must be treated as "unchanged" rather than a nil context. Run the contract suite green for both buses.

- [ ] **Step 3: Wire them in `pkg/obs.Bootstrap`**

`Bootstrap` is the one call each service's `Run` makes to stand up logging, tracing and metrics; it is the natural place to hand the bus its hooks, and it already owns the tracing lifecycle. Export the hook set from `pkg/obs` (for example `obs.BusHooks() events.Hooks` returning `{BeforePublish: tracing.Inject, AfterReceive: tracing.Extract}`) and have each service pass it when it connects the bus. Note: `pkg/obs` importing `pkg/events` is fine and is the direction the tracing helpers already take.

- [ ] **Step 4: Prove one trace crosses a process boundary**

A test that starts a span, publishes through the in-memory bus with the hooks installed, and asserts the consumer's span has the publisher's trace id and a parent-child link — the first real exercise of `Inject`/`Extract`, which until now had no production call sites.

- [ ] **Step 5: Correct the documentation**

`docs/observability.md`'s Status banner currently says the propagation leg is not wired. Rewrite that paragraph to say what is now true, and say which call sites remain unwired if any. Amendment §A4 assigns the first end-to-end trace to M1, so this is the phase that makes the claim honest.

- [ ] **Step 6: Commit**

```bash
go build ./... && go vet ./...
go test -count=1 -race ./pkg/events/... ./pkg/obs/...
make lint
git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "feat(events): propagate the trace across the bus through publish and receive hooks" -- pkg/events pkg/obs docs/observability.md
```

**Verification:** `go test -count=1 -race ./pkg/events/... ./pkg/obs/...` green, including the new contract case on BOTH buses; `make lint` clean; `pkg/events` still does not import `pkg/obs` (`go list -deps ./pkg/events/... | grep mediactl/clustarr/pkg/obs` must be empty).

**Done when:** both buses call the hooks; a nil hook is a no-op; `obs.Bootstrap` supplies tracing's existing `Inject`/`Extract` unchanged; one trace demonstrably spans a publish and a consume; `pkg/events` imports no `pkg/obs`; the observability doc no longer claims the leg is unwired.

---

---

### Task C2: pkg/decision

**Files:**
- Create:
  - `pkg/decision/doc.go`
  - `pkg/decision/types.go` (`Target`, `Current`, `Queued`, `Options`, `Decision`, `RankKey`)
  - `pkg/decision/reasons.go` (`Reason`, every `Reason*` value, `newRejection`, the `quality.Verdict` → `Reason` table)
  - `pkg/decision/size.go` (`targetRuntimeMinutes`, `sizeRejections`)
  - `pkg/decision/checks.go` (every other per-release check: protocol, availability, quality/MinFormatScore, language, sample, blocklist/already-imported, queue, upgrade-table)
  - `pkg/decision/evaluate.go` (`Evaluate`, `evaluateOne`, `buildRankKey`, small helpers)
  - `pkg/decision/rank.go` (`Rank`, the comparator chain)
- Test:
  - `pkg/decision/reasons_test.go`
  - `pkg/decision/size_test.go`
  - `pkg/decision/checks_test.go`
  - `pkg/decision/evaluate_test.go`
  - `pkg/decision/rank_test.go`
- Fixture:
  - `testdata/decision/releases.json`

**Path ownership:** `pkg/decision/` and `testdata/decision/` only. No other Phase C task, and no file under `api/`, `pkg/quality/`, `pkg/release/` or `hack/`, may be touched by this task.

**Read first:**
- `/home/appkins/src/mediactl/clustarr/CLAUDE.md` in full — the float ban does not apply here (this package emits nothing under `api/`), but the GPL header, the no-package-level-logger rule and "pure functions where the logic is tricky" apply directly; this whole package *is* that pure-function layer.
- Spec `docs/superpowers/specs/2026-09-18-clustarr-design.md`:
  - §7's `pkg/decision` block, lines 674-679 — the literal declared shapes (`Target`, `Current`, `Options`, `Decision`, `Evaluate`, `Rank`, `Upgradable`). Read the disagreements below before trusting these verbatim; two of the seven lines are superseded.
  - §8.2, line 745, in full — the checklist this task implements ("protocol enabled, availability unless `UserInvoked`, size MB/min×runtime with 110/45-min fallbacks and summed episode runtimes for packs, quality in profile, MinFormatScore, language, sample, blocklist, already imported by hash/name, queue higher/equal preference, `Upgradable` table") and the Search-CR paragraph two sentences later ("Search-CR grabs: user sets `spec.grab=[guid]`; controller creates Downloads (`Override` required for Permanent rejections)").
  - §9, lines 759-772 — quality model context; line 769 in particular ("`pkg/decision` ports the specification list and `UpgradableSpecification` verbatim... size checks, ranking").
- Research `docs/research/quality.md` (`grep -n '^#'` first):
  - §2.1-2.2 (lines 98-144) — size semantics and the exact TRaSH MB/min tables. You do not re-derive these; `pkg/quality.MovieSizeTable`/`SeriesSizeTable`/`AnimeSizeTable` already embed them (Task B2). Read this only to understand what `quality.SizeLimits` is doing under the hood.
  - §6.1 (lines 339-360) — `UpgradableSpecification.IsUpgradable`; already ported as `quality.Profile.UpgradeDecision` (Task B2). Read it to understand the `Verdict` values you are mapping to `Reason`s, not to reimplement it.
  - §6.2 (lines 361-369) — `DownloadDecisionComparer`'s stated order: "quality (profile index, then revision) → custom format score → protocol (delay-profile preferred) → indexer priority (reverse) → indexer flags → peers if torrent → age if usenet → size (closest to preferred size × runtime, else largest)" for Radarr, and Sonarr's variant which inserts "**episode count** (prefer packs when searching seasons)" between protocol and indexer priority. This is `Rank`'s exact key order; §14's "Read first" note below records where this task independently re-verified it against the real C# source.
- Research `docs/research/naming.md`:
  - §A6 "Decision engine: rejections, delay profiles, pending releases" (lines 154-163) — the verified `DownloadRejectionReason` taxonomy this task's `Reason` values are ported from, and the load-bearing rule: *"RSS sync obeys **all** specs; user-triggered searches skip 'monitored'-type checks"* — this is `Options.UserInvoked`'s effect on the availability check.
  - §A7 "Queue, history, blocklist" (lines 169-172) — *"Blocklist... Matching is by infohash (torrent) or title (usenet)"*, confirming `Target.Blocklist func(infohash, title string) bool`'s two keys are exactly what a caller can supply.
- Generated types, already read in full below — copy them, do not re-derive:
  - `api/common/v1alpha1/quality_types.go` (`Quality`, `Revision`).
  - `api/common/v1alpha1/release_types.go` (`ReleaseInfo`, `ReleaseType`, `Protocol`, the `IndexerFlag*` constants).
  - `api/common/v1alpha1/download_types.go` (`Rejection`, `RejectionType`, `RejectionPermanent`/`RejectionTemporary`).
  - `api/common/v1alpha1/media_types.go` (`MediaKind`).
  - `api/catalog/v1alpha1/qualityprofile_types.go` and `api/catalog/v1alpha1/delayprofile_types.go` — **for context only**. `Evaluate` takes an already-resolved `quality.Profile`, never `QualityProfileSpec` (see Disagreement 1). `DelayProfileSpec`'s `BypassIfHighestQuality`/`BypassIfAboveFormatScore`/`MinimumFormatScore`/delay-minutes fields belong to the controller task that resolves a `DelayProfile` after calling `Rank` (§8.2's "DelayProfile resolution" paragraph) — that resolution is **not** part of this task; do not add a `DelayProfile`-consuming function here.
  - `api/catalog/v1alpha1/search_types.go` — `ReleaseDecision{ commonv1.ReleaseInfo; Approved bool; TemporarilyRejected bool; Rejections []commonv1.Rejection (max 20); Rank int32 }` and `SearchSpec.Override bool`. This is what a later Phase C controller task builds from this package's `[]Decision` (`ReleaseDecision.Rank = int32(i)` from the post-`Rank()` slice position — see Interfaces — Produces). You are not implementing that controller; read this file only so `Decision`'s shape lines up with what it will feed.
- `go doc -all ./pkg/quality` and `go doc -all ./pkg/quality/catalogue` and `go doc ./pkg/release` — run these yourself before writing code; the exact signatures quoted under Interfaces — Consumes below were captured from these commands on 2026-09-18 against `main`, but Phase C tasks run in parallel and a sibling task cannot have touched these paths (they are Phase B, already merged), so no re-verification wave applies — just don't hand-transcribe from the spec's one-liners when the real package disagrees (it does, twice — see below).

**Disagreements (spec / task brief vs. the real merged code) — read before writing types.go:**

1. **`Evaluate` takes `quality.Profile`, not `QualityProfileSpec`.** The task brief that generated this document says "it takes a `QualityProfileSpec` and a `ReleaseInfo`." Spec §7's own literal signature (`func Evaluate(ctx, t Target, p quality.Profile, cat *catalogue.Catalogue, rels []common.ReleaseInfo, o Options) []Decision`) and the real, merged `pkg/quality` package agree with each other and disagree with the brief's prose: `quality.Profile` is Task B2's *already-resolved* type (`FromCRD` has already looked up every tier's qualities, flattened custom-format scores, resolved the cutoff index and the language/protocol strings); `QualityProfileSpec` is the raw CRD field bag that still needs a `*catalogue.Catalogue` to resolve. Redoing that resolution inside `pkg/decision` on every search would duplicate `quality.FromCRD` and silently diverge from it. This task follows the spec's literal signature and the real `pkg/quality` shape: **`Evaluate` takes `quality.Profile`**. Consequence: `pkg/decision` does not import `api/catalog/v1alpha1` at all — only `api/common/v1alpha1`, `pkg/quality`, `pkg/quality/catalogue`, `pkg/release` and `pkg/obs/logging`.
2. **`Upgradable` is not reimplemented; `quality.Profile.UpgradeDecision` is called instead.** Spec §7 declares `func Upgradable(p quality.Profile, cur *Current, cand Current, proper string) (ok bool, reason string)` in `pkg/decision`. Task B2 already shipped this exact table (research §6.1, `UpgradableSpecification.IsUpgradable`, verbatim) as `func (p quality.Profile) UpgradeDecision(current, candidate quality.Candidate) Verdict` — confirmed by `go doc -all ./pkg/quality`'s doc comment on `Candidate`: *"the full multi-candidate ranking machinery (availability, blocklist, protocol preference, queue) belongs to a later phase's `pkg/decision`, which will very likely call through to this function rather than reimplement it."* That prediction is this task. `pkg/decision` has **no** `Upgradable` function; `checks.go`'s `upgradeRejection` and `queueRejection` both call `p.UpgradeDecision` directly and map its `Verdict` to a `Reason` (table in `reasons.go`). Do not write a second copy of the decision table.
3. **`Current` gains two fields spec's one-line struct doesn't show: `SourceHash` and `SourceTitle`.** Spec §7: `type Current struct { Quality common.Quality; Revision common.Revision; FormatScore int; Formats []string }`. The §8.2 checklist requires "already imported (by hash and by name)," which needs something to compare the candidate release's `InfoHash`/`Title` against — and nothing in the one-line `Current` carries that. The real `MediaFileSpec.ImportedFrom` (`api/catalog/v1alpha1/mediafile_types.go`) has `ReleaseTitle string` but no hash field at all (a torrent's info hash isn't stored on `MediaFile`, only on the `Download` that produced it, via `Download.spec.release.InfoHash`, which is `common.ReleaseInfo.InfoHash`). So: `Current.SourceTitle` maps directly to the real `MediaFileSpec.ImportedFrom.ReleaseTitle`; `Current.SourceHash` has no MediaFile-side source and the caller (a later controller task) is expected to resolve it from the `Download` named by `MediaFileSpec.ImportedFrom.DownloadRef` when one exists. This is additive — one struct, two new fields — not a shape change, same precedent Task B2 used for `Match`/`Score`'s added `ctx` parameter. State this in `types.go`'s doc comment on `Current` so the controller task that populates it doesn't have to rediscover it.
4. **`Queued` is a type alias for `quality.Candidate`, not a new struct.** Spec §7 writes `Queue []Queued` without ever defining `Queued`. `quality.Candidate{Quality, Revision, FormatScore}` is exactly the shape a queued download's decision-relevant state needs (see `queueRejection`, which calls `p.UpgradeDecision` against each entry) and reusing it avoids a duplicate struct: `type Queued = quality.Candidate`. This satisfies spec's literal field name (`Target.Queue []Queued`) while being backed by the type Task B2 already ships.
5. **`Target.FreeBytes` is carried on the struct (spec's exact declared field) but this task does not add a free-space rejection.** The task brief's explicit checklist ("protocol enabled; availability...; size...; quality in profile; MinFormatScore; language; sample rejection; blocklist; already-imported...; queue preference...; and the `Upgradable` table") does not mention free space, even though `docs/research/naming.md` §A6's rejection taxonomy lists `FreeSpace: MinimumFreeSpace` as a real Radarr spec. `Target.FreeBytes` is kept on the struct because spec §7 declares it and a later task may wire it up, but no check reads it in this task — do not add one; it is explicitly out of scope, not an oversight. (§8.4's import flow already statfs-guards free space at import time via `RootFolder.minFreeBytes` per spec §8.4, so nothing is unguarded in the meantime.)
6. **Every `Reason` this task emits is `common.RejectionPermanent`.** The task brief says rejections "carry whether they are Permanent... or transient" and points at the Search-CR paragraph. Having read it: in the real Radarr/Sonarr source (`RejectionType.cs`, and every one of `ProtocolSpecification.cs`, `AcceptableSizeSpecification.cs`, `QualityAllowedByProfileSpecification.cs`, `CustomFormatAllowedByProfileSpecification.cs`, `LanguageSpecification.cs`, `NotSampleSpecification.cs`, `BlocklistSpecification.cs`, `AlreadyImportedSpecification.cs`, `QueueSpecification.cs`, `UpgradeDiskSpecification.cs` — all inspected directly, not inferred), **every single specification on the §8.2 checklist is `RejectionType.Permanent`**. The only `Temporary` specifications in the real source (`MinimumAgeSpecification` — usenet retention window, `BlockedIndexerSpecification`, and three RSS-sync-only specs) are all outside this task's checklist. So `common.RejectionTemporary` is a value this package's `Reason.Type` field can hold (and `Decision.TemporarilyRejected`'s formula, below, is written generally) but no `Reason` constant in `reasons.go` uses it. This is a documented finding, not a gap: it means every rejection this package produces requires `Search.spec.override` to force-grab, and `TemporarilyRejected` is always `false` for decisions this task's checks alone can produce — the field exists for forward compatibility (a future minimum-age/indexer-availability check) and because the formula computing it is correct regardless of whether any `Temporary` reason currently exists.
7. **`Rank`'s comparator order was independently re-verified against the real source, resolving a research-note "unverified."** `docs/research/naming.md` §A6 flags its own summary of `DownloadDecisionComparer`'s order as "**unverified order**": *"quality (+ revision/proper/repack) -> custom format score -> preferred protocol from the delay profile -> indexer priority -> seeders/leechers (torrent) or age (usenet) -> size."* That list omits Sonarr's episode-count step and Radarr's indexer-flags step, both real. This task's `rank.go` was written against the actual vendored `NzbDrone.Core/DecisionEngine/DownloadDecisionComparer.cs` (both apps) still present at `$SCRATCHPAD/src/{Radarr,Sonarr}/src/NzbDrone.Core/DecisionEngine/DownloadDecisionComparer.cs` at plan-writing time, which gives the exact key order this task uses (matching spec §7's own one-line order exactly): quality index + revision (bundled — revision only breaks a tie when `ProperPolicy != "doNotPrefer"`) → custom-format score → preferred-protocol match → episode count → indexer priority (ascending; `Priority` "1 (highest) … 50, default 25" per `docs/research/indexers.md` line 35/709 — the CRD doc comment `api/index/v1alpha1/indexer_types.go:104` confirms "lower wins") → indexer-flag score (weights below, from `DownloadDecisionComparer.ScoreFlags`) → seeders (torrent, `round(log10(seeders))`) or age bucket (usenet, `hours<1→1000, hours<=24→100, days<=7→10, else round(log10(days))·-1`) → size (closest to the quality's preferred MB/min×runtime, rounded to 200 MiB buckets, unless the quality's preferred size is the TRaSH "biggest" sentinel — see Step 13 — in which case largest wins). If the implementer's own `go doc`/source read of the vendored clone disagrees with this document at the time of implementation, trust the source over this document and say so in the final report, same as any other disagreement.

**Dependencies:** none new. `pkg/decision` imports only in-module packages already on `main`: `api/common/v1alpha1`, `pkg/quality`, `pkg/quality/catalogue`, `pkg/release`, `pkg/obs/logging`, plus the stdlib (`context`, `fmt`, `math`, `sort`, `strings`, `time`). Tests additionally import `github.com/stretchr/testify` (`require`), already at `v1.12.1` in `go.mod` (used by every other `pkg/*` test in the repo — no version check needed, nothing to add). Do not run `go get` or `go mod tidy` for this task.

**Interfaces — Consumes:**

```go
// api/common/v1alpha1/quality_types.go -- reuse verbatim.
type Quality struct { Name string; Source Source; Resolution int32; Modifier Modifier }
type Revision struct { Version int32; Real int32; Repack bool }

// api/common/v1alpha1/media_types.go
type MediaKind string
const (
	MediaKindMovie MediaKind = "movie"; MediaKindSeries MediaKind = "series"; MediaKindEpisode MediaKind = "episode"
	// ...artist, album, author, book, audiobook, comic, issue -- non-video, out of scope (Disagreement-adjacent: size.go
	// only handles movie/episode/series; every other Kind's size check is a documented no-op, see Step 2).
)

// api/common/v1alpha1/release_types.go -- reuse verbatim, do not redefine.
type ReleaseType string // single, multi, seasonPack, album, book, issue
type Protocol string
const ( ProtocolTorrent Protocol = "torrent"; ProtocolUsenet Protocol = "usenet" )
const (
	IndexerFlagFreeleech = "freeleech"; IndexerFlagHalfleech = "halfleech"; IndexerFlagNeutralleech = "neutralleech"
	IndexerFlagDoubleUpload = "doubleupload"; IndexerFlagInternal = "internal"; IndexerFlagExclusive = "exclusive"; IndexerFlagScene = "scene"
)
type ReleaseInfo struct {
	GUID, IndexerRef, IndexerName, Title string
	Protocol      Protocol
	SizeBytes     int64
	PublishedAt   metav1.Time
	DownloadURL, MagnetURL, InfoHash, InfoURL string
	Seeders, Leechers *int32
	IndexerFlags  []string
	Categories    []int32
	Quality       Quality
	Revision      Revision
	ReleaseGroup, Edition string
	Languages     []string
	ReleaseType   ReleaseType
	FormatScore   int32
	MatchedFormats []string
	IDs           map[string]string
}

// api/common/v1alpha1/download_types.go -- reuse verbatim.
type RejectionType string
const ( RejectionPermanent RejectionType = "Permanent"; RejectionTemporary RejectionType = "Temporary" )
type Rejection struct { Reason string; Type RejectionType }

// pkg/obs/logging (Phase A).
func FromContext(ctx context.Context) *slog.Logger

// pkg/release (Task B1, merged) -- captured via `go doc ./pkg/release`, 2026-09-18.
type Options struct { Kind commonv1.MediaKind; SeriesType string } // SeriesType "" -> "standard"
func Parse(title string, o Options) (*ParsedRelease, error)
type ParsedRelease struct {
	Title string; Titles []string; Year int
	Quality commonv1.Quality; Revision commonv1.Revision
	Languages []string; Group, Hash, Edition string
	Seasons, Episodes, Absolute []int
	AirDate *time.Time
	FullSeason, Partial, MultiSeason, Special bool
	ReleaseType commonv1.ReleaseType
	Music *MusicInfo; Book *BookInfo; Comic *ComicInfo
	Hints Hints
	IDs map[string]string
}
func (p *ParsedRelease) ApplyTo(ri *commonv1.ReleaseInfo) // copies parsed fields onto ri; leaves indexer-sourced fields untouched
func Age(publishedAt, now time.Time) (days int32, hours int32, minutes int64) // total elapsed, not remainders

// pkg/quality (Task B2, merged) -- captured via `go doc -all ./pkg/quality`, 2026-09-18.
type Definition struct { Quality common.Quality; Name string; Aliases []string; Weight int; MinMBPerMin, PrefMBPerMin, MaxMBPerMin float64; Group string }
func Lookup(kind, name string) (Definition, bool)
type SizeLimit struct { MinMBPerMin, PrefMBPerMin, MaxMBPerMin float64 } // MaxMBPerMin 0 = unlimited
func MovieSizeTable() map[string]SizeLimit
func SeriesSizeTable() map[string]SizeLimit
func AnimeSizeTable() map[string]SizeLimit
func SizeLimits(p Profile, q common.Quality, runtimeMin int) (minBytes, maxBytes int64) // caller does the runtime-0 fallback
type Profile struct {
	Tiers [][]Definition; CutoffIndex int; UpgradeAllowed bool
	MinFormatScore, CutoffFormatScore, MinUpgradeFormatScore int
	Scores map[string]int
	Language, LanguageName string // LanguageName: "original"/"any" pass through, else a Radarr English name, "" = no constraint
	ProperPolicy string           // "preferAndUpgrade" | "doNotUpgrade" | "doNotPrefer"
	Sizes map[string]SizeLimit
	PreferredProtocol string      // "usenet" | "torrent" | "any" -- "This package does not evaluate it; a later phase's release ranking does" (go doc)
	Hash string
}
func (p Profile) Index(q common.Quality) (idx int, ok bool)       // 0 = best
func (p Profile) Allowed(q common.Quality) bool
func (p Profile) CutoffMet(q common.Quality) bool
func (p Profile) Score(ctx context.Context, cat *catalogue.Catalogue, r *release.ParsedRelease, ic catalogue.ItemContext) (score int, matched []string)
type Candidate struct { Quality common.Quality; Revision common.Revision; FormatScore int }
type Verdict uint8
const (
	Upgrade Verdict = iota; ExistingBetterQuality; UpgradesNotAllowed; ExistingBetterRevision
	QualityCutoffMet; FormatScoreNotHigher; FormatCutoffMet; FormatIncrementTooSmall
)
func (p Profile) UpgradeDecision(current, candidate Candidate) Verdict

// pkg/quality/catalogue (Task B2, merged) -- captured via `go doc -all ./pkg/quality/catalogue`, 2026-09-18.
type ItemContext struct { OriginalLanguage string; IndexerFlags []string; ReleaseType common.ReleaseType }
type Catalogue struct { Version string; Formats map[string]*Format; Conflicts [][2]string }
```

**Interfaces — Produces:**

```go
package decision // github.com/mediactl/clustarr/pkg/decision

import (
	"context"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
	"github.com/mediactl/clustarr/pkg/release"
)

// Current is the quality-model state of the file already imported for a
// Target, when one exists. SourceTitle mirrors the real
// catalogv1alpha1.MediaFileSpec.ImportedFrom.ReleaseTitle field verbatim;
// SourceHash has no MediaFile-side source (a torrent's info hash lives on
// the Download that produced the file, not on MediaFile itself) and is the
// caller's responsibility to resolve from that Download when one still
// exists -- see Disagreement 3.
type Current struct {
	Quality     common.Quality
	Revision    common.Revision
	FormatScore int
	Formats     []string
	SourceHash  string
	SourceTitle string
}

// Queued is a release already downloading for a Target. It is a type alias
// for quality.Candidate (Disagreement 4): spec §7 names the field
// Target.Queue []Queued without ever defining Queued, and quality.Candidate
// is exactly the shape queueRejection needs to call p.UpgradeDecision.
type Queued = quality.Candidate

// Target is everything about one catalog item Evaluate needs that isn't
// carried on a candidate release itself. Blocklist matches on infohash
// (torrent) or title (usenet) -- docs/research/naming.md §A7.
type Target struct {
	Kind             common.MediaKind
	Key              string
	Monitored        bool
	Available        bool
	RuntimeMinutes   int
	EpisodeRuntimes  []int
	OriginalLanguage string
	Current          *Current
	Queue            []Queued
	Blocklist        func(infohash, title string) bool
	FreeBytes        int64 // carried per spec §7; unused by this task, see Disagreement 5
}

// Options carries the parts of a decision that come from something other
// than the release or the quality profile: whether this is a user-invoked
// (interactive) search, which protocols are currently enabled, indexer
// priority, and the protocol Evaluate/Rank should prefer when the profile
// itself doesn't say (Profile.PreferredProtocol is the fallback -- see
// buildRankKey).
type Options struct {
	UserInvoked       bool
	ProtocolsEnabled  map[string]bool // missing key = disabled (fail closed); caller populates both "torrent" and "usenet"
	IndexerPriority   map[string]int  // missing key defaults to 25, docs/research/indexers.md's documented default
	PreferredProtocol string
}

// RankKey is the part of a Decision's rank Evaluate can compute (it alone
// has Profile and Target); Rank combines it with the parts that only need
// Options and the Decision's own Release/Parsed at sort time. Only
// populated when Approved is true.
type RankKey struct {
	QualityIndex           int
	PreferRevision         bool // false when Profile.ProperPolicy == "doNotPrefer"
	Revision                common.Revision
	FormatScore             int
	PreferredProtocolMatch bool
	EpisodeCount            int  // 1 for a single release; len(Parsed.Episodes) for a partial multi-episode release; a season-pack sentinel for FullSeason
	PreferLargestSize       bool // true when the quality's preferred size is TRaSH's "biggest" sentinel (Step 13)
	SizeDeltaBucket          int64 // |release.SizeBytes - preferredBytes|, rounded to 200 MiB; meaningful only when !PreferLargestSize
	SizeBytes                int64 // meaningful only when PreferLargestSize
}

// Decision is one release's verdict against one Target. Its shape matches
// spec §7's literal Decision struct field-for-field.
type Decision struct {
	Release             common.ReleaseInfo
	Parsed              *release.ParsedRelease // nil only when Release.Title failed to parse (ReasonUnableToParse)
	Approved            bool                   // len(Rejections) == 0
	TemporarilyRejected bool                   // len(Rejections) > 0 && every Rejection is common.RejectionTemporary
	Rejections          []common.Rejection
	Score               int
	Matched             []string
	Rank                RankKey
}

// Evaluate runs the §8.2 checklist against every release in rels for one
// Target, in the order: protocol enabled, availability (skipped when
// o.UserInvoked), size, quality-in-profile + MinFormatScore, language,
// sample, blocklist + already-imported, queue preference, then the
// UpgradableSpecification table via p.UpgradeDecision. Every applicable
// check runs (nothing short-circuits except an unparseable title), so a
// release can carry more than one Rejection -- matching the real
// DownloadDecision's Rejections list, which Approved/TemporarilyRejected
// are then derived from exactly as Radarr's DownloadDecision does
// (Disagreement 6).
func Evaluate(ctx context.Context, t Target, p quality.Profile, cat *catalogue.Catalogue, rels []common.ReleaseInfo, o Options) []Decision

// Rank stable-sorts a copy of ds best-first: QualityIndex asc, Revision
// desc (only if PreferRevision), FormatScore desc, PreferredProtocolMatch
// desc, EpisodeCount desc, indexer priority asc (from o.IndexerPriority),
// indexer-flag score desc, seeders/age desc, then size (Disagreement 7).
// Callers typically pass only the Approved decisions; ds with a zero
// RankKey (never Evaluated as Approved) sort by that zero value like any
// other -- Rank does not filter Rejected decisions itself.
func Rank(ds []Decision, o Options) []Decision

// Reason is a typed, non-string rejection reason: a stable Code ported from
// Radarr/Sonarr's verified DownloadRejectionReason enum
// (docs/research/naming.md §A6) plus the RejectionType every use of it
// carries, so a call site cannot forget to mark whether Search.spec.override
// is required. See reasons.go for the full list; every one of this
// package's Reason values is common.RejectionPermanent (Disagreement 6).
type Reason struct {
	Code string
	Type common.RejectionType
}
```

- [ ] **Step 1: Failing test — `Reason` values are exhaustively typed, and every `quality.Verdict` except `Upgrade` maps to one**

`pkg/decision/reasons_test.go`:
```go
/* GPL header from hack/boilerplate.go.txt */

package decision_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/quality"
)

func TestEveryReasonIsPermanent(t *testing.T) {
	// Disagreement 6: every real *arr specification on the §8.2 checklist is
	// RejectionType.Permanent; this package emits no Temporary reason.
	for _, r := range []decision.Reason{
		decision.ReasonUnableToParse, decision.ReasonProtocolDisabled, decision.ReasonUnavailable,
		decision.ReasonBelowMinimumSize, decision.ReasonAboveMaximumSize, decision.ReasonQualityNotWanted,
		decision.ReasonCustomFormatMinimumScore, decision.ReasonWantedLanguage, decision.ReasonSample,
		decision.ReasonBlocklisted, decision.ReasonAlreadyImportedSameHash, decision.ReasonAlreadyImportedSameName,
		decision.ReasonQueueHigherPreference, decision.ReasonExistingHigherPreference, decision.ReasonUpgradesNotAllowed,
		decision.ReasonExistingHigherRevision, decision.ReasonExistingCutoffMet, decision.ReasonExistingFormatScore,
		decision.ReasonExistingFormatCutoffMet, decision.ReasonExistingFormatScoreIncrement,
	} {
		require.Equal(t, common.RejectionPermanent, r.Type, "reason %s", r.Code)
	}
}

func TestVerdictReasonTableIsExhaustive(t *testing.T) {
	// Every Verdict quality.UpgradeDecision can return, other than Upgrade
	// itself (which means "approve, don't reject"), must have a mapped Reason.
	for _, v := range []quality.Verdict{
		quality.ExistingBetterQuality, quality.UpgradesNotAllowed, quality.ExistingBetterRevision,
		quality.QualityCutoffMet, quality.FormatScoreNotHigher, quality.FormatCutoffMet, quality.FormatIncrementTooSmall,
	} {
		r, ok := decision.VerdictReason(v)
		require.True(t, ok, "verdict %v has no mapped Reason", v)
		require.NotEmpty(t, r.Code)
	}
	_, ok := decision.VerdictReason(quality.Upgrade)
	require.False(t, ok, "Upgrade must never map to a Reason -- it means approve")
}
```

```bash
cd /home/appkins/src/mediactl/clustarr && go test ./pkg/decision/... -run 'TestEveryReasonIsPermanent|TestVerdictReasonTableIsExhaustive' -v
# expect: FAIL -- package pkg/decision does not exist yet
```

Implement `pkg/decision/types.go` (the `Target`/`Current`/`Queued`/`Options`/`Decision`/`RankKey` block from Interfaces — Produces above, verbatim) and `pkg/decision/reasons.go`:
```go
/* GPL header */

package decision

import (
	"fmt"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/quality"
)

// Reason.Code values, ported 1:1 from the verified Radarr/Sonarr
// DownloadRejectionReason enum (docs/research/naming.md §A6; cross-checked
// against DecisionEngine/DownloadRejectionReason.cs in the vendored clone).
// "Existing*" corresponds to *arr's "Disk*" family (UpgradeDiskSpecification)
// -- renamed because Clustarr's Target.Current is the analogous concept, not
// a literal file on disk.
var (
	ReasonUnableToParse                = Reason{"UnableToParse", common.RejectionPermanent}
	ReasonProtocolDisabled              = Reason{"ProtocolDisabled", common.RejectionPermanent}
	ReasonUnavailable                   = Reason{"Availability", common.RejectionPermanent}
	ReasonBelowMinimumSize              = Reason{"BelowMinimumSize", common.RejectionPermanent}
	ReasonAboveMaximumSize              = Reason{"AboveMaximumSize", common.RejectionPermanent}
	ReasonQualityNotWanted              = Reason{"QualityNotWanted", common.RejectionPermanent}
	ReasonCustomFormatMinimumScore      = Reason{"CustomFormatMinimumScore", common.RejectionPermanent}
	ReasonWantedLanguage                = Reason{"WantedLanguage", common.RejectionPermanent}
	ReasonSample                        = Reason{"Sample", common.RejectionPermanent}
	ReasonBlocklisted                   = Reason{"Blocklisted", common.RejectionPermanent}
	ReasonAlreadyImportedSameHash       = Reason{"AlreadyImportedSameHash", common.RejectionPermanent}
	ReasonAlreadyImportedSameName       = Reason{"AlreadyImportedSameName", common.RejectionPermanent}
	ReasonQueueHigherPreference         = Reason{"QueueHigherPreference", common.RejectionPermanent}
	ReasonExistingHigherPreference      = Reason{"ExistingHigherPreference", common.RejectionPermanent}
	ReasonUpgradesNotAllowed            = Reason{"UpgradesNotAllowed", common.RejectionPermanent}
	ReasonExistingHigherRevision        = Reason{"ExistingHigherRevision", common.RejectionPermanent}
	ReasonExistingCutoffMet             = Reason{"ExistingCutoffMet", common.RejectionPermanent}
	ReasonExistingFormatScore           = Reason{"ExistingFormatScore", common.RejectionPermanent}
	ReasonExistingFormatCutoffMet       = Reason{"ExistingFormatCutoffMet", common.RejectionPermanent}
	ReasonExistingFormatScoreIncrement  = Reason{"ExistingFormatScoreIncrement", common.RejectionPermanent}
)

// verdictReasons maps every non-Upgrade quality.Verdict to the Reason
// upgradeRejection and queueRejection report it as (Disagreement 2: this
// table exists because pkg/decision calls quality.Profile.UpgradeDecision
// rather than reimplementing UpgradableSpecification).
var verdictReasons = map[quality.Verdict]Reason{
	quality.ExistingBetterQuality:   ReasonExistingHigherPreference,
	quality.UpgradesNotAllowed:      ReasonUpgradesNotAllowed,
	quality.ExistingBetterRevision:  ReasonExistingHigherRevision,
	quality.QualityCutoffMet:        ReasonExistingCutoffMet,
	quality.FormatScoreNotHigher:    ReasonExistingFormatScore,
	quality.FormatCutoffMet:         ReasonExistingFormatCutoffMet,
	quality.FormatIncrementTooSmall: ReasonExistingFormatScoreIncrement,
}

// VerdictReason exposes verdictReasons to tests in the decision_test
// package; production callers use it only through upgradeRejection/queueRejection.
func VerdictReason(v quality.Verdict) (Reason, bool) {
	r, ok := verdictReasons[v]
	return r, ok
}

// newRejection renders r as a common.Rejection: "<Code>: <formatted detail>",
// mirroring *arr's own Rejection.ToString() ("[Type] Message") closely enough
// that a reviewer can find the matching *arr specification by the Code alone.
func newRejection(r Reason, format string, args ...any) common.Rejection {
	return common.Rejection{
		Reason: r.Code + ": " + fmt.Sprintf(format, args...),
		Type:   r.Type,
	}
}
```

```bash
cd /home/appkins/src/mediactl/clustarr && go test ./pkg/decision/... -run 'TestEveryReasonIsPermanent|TestVerdictReasonTableIsExhaustive' -v
# expect: PASS
git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "feat(decision): typed Reason values and the core Target/Decision shapes" -- pkg/decision/types.go pkg/decision/reasons.go pkg/decision/reasons_test.go
```

- [ ] **Step 2: Failing test — `targetRuntimeMinutes`, the 110/45-minute fallbacks and summed episode runtimes for packs**

`pkg/decision/size_test.go`:
```go
/* GPL header */

package decision

import (
	"testing"

	"github.com/stretchr/testify/require"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/release"
)

func TestTargetRuntimeMinutes(t *testing.T) {
	t.Run("movie with known runtime", func(t *testing.T) {
		m, ok := targetRuntimeMinutes(Target{Kind: common.MediaKindMovie, RuntimeMinutes: 95}, &release.ParsedRelease{})
		require.True(t, ok)
		require.Equal(t, 95, m)
	})

	t.Run("movie with unknown runtime falls back to 110", func(t *testing.T) {
		m, ok := targetRuntimeMinutes(Target{Kind: common.MediaKindMovie, RuntimeMinutes: 0}, &release.ParsedRelease{})
		require.True(t, ok)
		require.Equal(t, 110, m)
	})

	t.Run("single episode with known runtime", func(t *testing.T) {
		tg := Target{Kind: common.MediaKindEpisode, EpisodeRuntimes: []int{42}}
		m, ok := targetRuntimeMinutes(tg, &release.ParsedRelease{Episodes: []int{5}})
		require.True(t, ok)
		require.Equal(t, 42, m)
	})

	t.Run("single episode with unknown runtime falls back to 45", func(t *testing.T) {
		tg := Target{Kind: common.MediaKindEpisode, EpisodeRuntimes: []int{0}}
		m, ok := targetRuntimeMinutes(tg, &release.ParsedRelease{Episodes: []int{5}})
		require.True(t, ok)
		require.Equal(t, 45, m)
	})

	t.Run("season pack sums per-episode runtimes, falling back per episode", func(t *testing.T) {
		tg := Target{Kind: common.MediaKindEpisode, EpisodeRuntimes: []int{42, 42, 0, 42}}
		m, ok := targetRuntimeMinutes(tg, &release.ParsedRelease{Episodes: []int{1, 2, 3, 4}})
		require.True(t, ok)
		require.Equal(t, 171, m) // 42+42+45+42, the third episode's unknown runtime falls back to 45
	})

	t.Run("full-season release with no per-episode runtime data falls back to one 45-minute episode", func(t *testing.T) {
		tg := Target{Kind: common.MediaKindEpisode}
		m, ok := targetRuntimeMinutes(tg, &release.ParsedRelease{FullSeason: true})
		require.True(t, ok)
		require.Equal(t, 45, m)
	})

	t.Run("non-video kind has no runtime/size model", func(t *testing.T) {
		_, ok := targetRuntimeMinutes(Target{Kind: common.MediaKindBook}, &release.ParsedRelease{})
		require.False(t, ok)
	})
}
```

```bash
cd /home/appkins/src/mediactl/clustarr && go test ./pkg/decision/... -run TestTargetRuntimeMinutes -v
# expect: FAIL -- undefined: targetRuntimeMinutes
```

Implement `pkg/decision/size.go`:
```go
/* GPL header */

package decision

import (
	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/release"
)

// Fallback runtimes, docs/superpowers/specs/2026-09-18-clustarr-design.md
// §8.2: "size MB/min×runtime with 110/45-min fallbacks and summed episode
// runtimes for packs" (docs/research/quality.md §2.1 sources the two
// numbers from AcceptableSizeSpecification.cs / Sonarr's episode-runtime
// fallback).
const (
	fallbackMovieRuntimeMinutes   = 110
	fallbackEpisodeRuntimeMinutes = 45
)

// targetRuntimeMinutes resolves the runtime the size check uses. For a
// multi-episode release (FullSeason, or more than one parsed Episode) it
// sums Target.EpisodeRuntimes, applying the 45-minute fallback to each
// zero/missing entry before summing -- t.EpisodeRuntimes is read as "one
// entry per episode this specific release covers," so it takes the first
// min(len(parsed.Episodes), len(t.EpisodeRuntimes)) entries; a FullSeason
// release with no episode list at all falls back to exactly one 45-minute
// episode rather than reporting an unknown runtime (spec's fallback rule has
// no "unknown pack size" case). ok is false for a MediaKind this package has
// no size model for (music/book/audiobook/comic -- spec §9's "non-video
// pipelines... implemented after M6").
func targetRuntimeMinutes(t Target, parsed *release.ParsedRelease) (minutes int, ok bool) {
	switch t.Kind {
	case common.MediaKindMovie:
		m := t.RuntimeMinutes
		if m <= 0 {
			m = fallbackMovieRuntimeMinutes
		}
		return m, true

	case common.MediaKindEpisode, common.MediaKindSeries:
		multi := parsed.FullSeason || len(parsed.Episodes) > 1
		if !multi {
			m := 0
			if len(t.EpisodeRuntimes) > 0 {
				m = t.EpisodeRuntimes[0]
			}
			if m <= 0 {
				m = fallbackEpisodeRuntimeMinutes
			}
			return m, true
		}

		n := len(parsed.Episodes)
		if n == 0 || n > len(t.EpisodeRuntimes) {
			n = len(t.EpisodeRuntimes)
		}
		if n == 0 {
			return fallbackEpisodeRuntimeMinutes, true
		}
		total := 0
		for i := 0; i < n; i++ {
			m := t.EpisodeRuntimes[i]
			if m <= 0 {
				m = fallbackEpisodeRuntimeMinutes
			}
			total += m
		}
		return total, true

	default:
		return 0, false
	}
}
```

```bash
cd /home/appkins/src/mediactl/clustarr && go test ./pkg/decision/... -run TestTargetRuntimeMinutes -v
# expect: PASS
git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "feat(decision): runtime resolution with the 110/45-minute fallbacks" -- pkg/decision/size.go pkg/decision/size_test.go
```

- [ ] **Step 3: Failing test — `sizeRejections` against the real TRaSH movie size table**

Add to `pkg/decision/size_test.go`:
```go
func TestSizeRejections(t *testing.T) {
	bluray1080, _ := quality.Lookup("video", "Bluray-1080p")
	p := quality.Profile{Sizes: quality.MovieSizeTable()}
	tg := Target{Kind: common.MediaKindMovie} // RuntimeMinutes 0 -> falls back to 110

	t.Run("within bounds", func(t *testing.T) {
		rel := common.ReleaseInfo{Quality: bluray1080.Quality, SizeBytes: 10_200_000_000} // ~9.5 GiB
		require.Empty(t, sizeRejections(tg, p, &release.ParsedRelease{}, rel))
	})

	t.Run("below the 50.8 MB/min floor at 110 minutes", func(t *testing.T) {
		// 50.8 MB/min * 110 min * 1024 * 1024 = 5,859,442,688 bytes exactly.
		rel := common.ReleaseInfo{Quality: bluray1080.Quality, SizeBytes: 5_000_000_000}
		got := sizeRejections(tg, p, &release.ParsedRelease{}, rel)
		require.Len(t, got, 1)
		require.Equal(t, common.RejectionPermanent, got[0].Type)
		require.Contains(t, got[0].Reason, ReasonBelowMinimumSize.Code)
	})

	t.Run("zero size is unknown, not rejected -- ported from AcceptableSizeSpecification", func(t *testing.T) {
		rel := common.ReleaseInfo{Quality: bluray1080.Quality, SizeBytes: 0}
		require.Empty(t, sizeRejections(tg, p, &release.ParsedRelease{}, rel))
	})

	t.Run("quality absent from the size table is not checked", func(t *testing.T) {
		rel := common.ReleaseInfo{Quality: common.Quality{Name: "Unknown-Table-Entry"}, SizeBytes: 1}
		require.Empty(t, sizeRejections(tg, p, &release.ParsedRelease{}, rel))
	})
}
```

```bash
cd /home/appkins/src/mediactl/clustarr && go test ./pkg/decision/... -run TestSizeRejections -v
# expect: FAIL -- undefined: sizeRejections
```

Add to `pkg/decision/size.go`:
```go
// sizeRejections is AcceptableSizeSpecification, ported: a release with
// unknown size (0) is never rejected; a quality absent from p.Sizes (e.g.
// SizeTable "none") is never checked; otherwise both bounds are enforced,
// with MaxMBPerMin 0 meaning unlimited (quality.SizeLimits' own convention).
func sizeRejections(t Target, p quality.Profile, parsed *release.ParsedRelease, rel common.ReleaseInfo) []common.Rejection {
	if rel.SizeBytes <= 0 {
		return nil
	}
	if _, hasLimits := p.Sizes[rel.Quality.Name]; !hasLimits {
		return nil
	}
	minutes, ok := targetRuntimeMinutes(t, parsed)
	if !ok {
		return nil
	}
	minBytes, maxBytes := quality.SizeLimits(p, rel.Quality, minutes)

	var out []common.Rejection
	if minBytes > 0 && rel.SizeBytes < minBytes {
		out = append(out, newRejection(ReasonBelowMinimumSize,
			"%d bytes is below the %s minimum of %d bytes at %d minutes", rel.SizeBytes, rel.Quality.Name, minBytes, minutes))
	}
	if maxBytes > 0 && rel.SizeBytes > maxBytes {
		out = append(out, newRejection(ReasonAboveMaximumSize,
			"%d bytes is above the %s maximum of %d bytes at %d minutes", rel.SizeBytes, rel.Quality.Name, maxBytes, minutes))
	}
	return out
}
```

Add the needed imports (`"github.com/mediactl/clustarr/pkg/quality"`) to `size.go` and `size_test.go` (`common`, `release`, `quality`, `require`, `testing`).

```bash
cd /home/appkins/src/mediactl/clustarr && go test ./pkg/decision/... -run 'TestTargetRuntimeMinutes|TestSizeRejections' -v
# expect: PASS
git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "feat(decision): size-vs-runtime rejection against the TRaSH tables" -- pkg/decision/size.go pkg/decision/size_test.go
```

- [ ] **Step 4: Failing test — protocol-enabled and availability checks**

`pkg/decision/checks_test.go`:
```go
/* GPL header */

package decision

import (
	"testing"

	"github.com/stretchr/testify/require"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
)

func TestProtocolRejection(t *testing.T) {
	t.Run("enabled protocol passes", func(t *testing.T) {
		o := Options{ProtocolsEnabled: map[string]bool{"torrent": true}}
		require.Nil(t, protocolRejection(common.ReleaseInfo{Protocol: common.ProtocolTorrent}, o))
	})
	t.Run("disabled protocol rejects", func(t *testing.T) {
		o := Options{ProtocolsEnabled: map[string]bool{"torrent": false, "usenet": true}}
		got := protocolRejection(common.ReleaseInfo{Protocol: common.ProtocolTorrent}, o)
		require.NotNil(t, got)
		require.Equal(t, common.RejectionPermanent, got.Type)
	})
	t.Run("a protocol missing from the map is disabled (fail closed)", func(t *testing.T) {
		o := Options{ProtocolsEnabled: map[string]bool{}}
		require.NotNil(t, protocolRejection(common.ReleaseInfo{Protocol: common.ProtocolUsenet}, o))
	})
}

func TestAvailabilityRejection(t *testing.T) {
	t.Run("unavailable and not user-invoked rejects", func(t *testing.T) {
		got := availabilityRejection(Target{Available: false}, Options{UserInvoked: false})
		require.NotNil(t, got)
	})
	t.Run("unavailable but user-invoked is skipped -- naming.md §A6: interactive search skips monitored-type checks", func(t *testing.T) {
		require.Nil(t, availabilityRejection(Target{Available: false}, Options{UserInvoked: true}))
	})
	t.Run("available passes regardless", func(t *testing.T) {
		require.Nil(t, availabilityRejection(Target{Available: true}, Options{UserInvoked: false}))
	})
}
```

```bash
cd /home/appkins/src/mediactl/clustarr && go test ./pkg/decision/... -run 'TestProtocolRejection|TestAvailabilityRejection' -v
# expect: FAIL -- undefined: protocolRejection, availabilityRejection
```

Implement `pkg/decision/checks.go` (this step only; more functions land in Steps 5-10):
```go
/* GPL header */

package decision

import (
	"strings"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/quality"
)

// protocolRejection is ProtocolSpecification: a protocol absent from
// o.ProtocolsEnabled is treated as disabled. The caller populates both
// "torrent" and "usenet" from the resolved DelayProfile's
// EnableUsenet/EnableTorrent (default true on the CRD), so an absent key in
// practice means "no DelayProfile resolved yet," which should fail closed.
func protocolRejection(rel common.ReleaseInfo, o Options) *common.Rejection {
	if o.ProtocolsEnabled[string(rel.Protocol)] {
		return nil
	}
	r := newRejection(ReasonProtocolDisabled, "%s is not enabled", rel.Protocol)
	return &r
}

// availabilityRejection is RssSync/AvailabilitySpecification: skipped
// entirely for a user-invoked (interactive) search.
func availabilityRejection(t Target, o Options) *common.Rejection {
	if o.UserInvoked || t.Available {
		return nil
	}
	r := newRejection(ReasonUnavailable, "item is not yet available")
	return &r
}
```

```bash
cd /home/appkins/src/mediactl/clustarr && go test ./pkg/decision/... -run 'TestProtocolRejection|TestAvailabilityRejection' -v
# expect: PASS
git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "feat(decision): protocol-enabled and availability checks" -- pkg/decision/checks.go pkg/decision/checks_test.go
```

- [ ] **Step 5: Failing test — quality-in-profile and `MinFormatScore`**

Add to `checks_test.go`:
```go
func TestQualityRejections(t *testing.T) {
	bluray1080, _ := quality.Lookup("video", "Bluray-1080p")
	webdl720, _ := quality.Lookup("video", "WEBDL-720p")
	p := quality.Profile{Tiers: [][]quality.Definition{{bluray1080}}, MinFormatScore: 10}

	t.Run("allowed quality, score above minimum", func(t *testing.T) {
		require.Empty(t, qualityRejections(p, common.ReleaseInfo{Quality: bluray1080.Quality}, 10))
	})
	t.Run("quality not in any tier", func(t *testing.T) {
		got := qualityRejections(p, common.ReleaseInfo{Quality: webdl720.Quality}, 10)
		require.Len(t, got, 1)
		require.Contains(t, got[0].Reason, ReasonQualityNotWanted.Code)
	})
	t.Run("score below MinFormatScore", func(t *testing.T) {
		got := qualityRejections(p, common.ReleaseInfo{Quality: bluray1080.Quality}, 9)
		require.Len(t, got, 1)
		require.Contains(t, got[0].Reason, ReasonCustomFormatMinimumScore.Code)
	})
	t.Run("both fail at once", func(t *testing.T) {
		got := qualityRejections(p, common.ReleaseInfo{Quality: webdl720.Quality}, 0)
		require.Len(t, got, 2)
	})
}
```

```bash
cd /home/appkins/src/mediactl/clustarr && go test ./pkg/decision/... -run TestQualityRejections -v
# expect: FAIL -- undefined: qualityRejections
```

Add to `checks.go`:
```go
// qualityRejections is QualityAllowedByProfileSpecification +
// CustomFormatAllowedByProfileSpecification: both run unconditionally (a
// release can fail either or both at once, matching the real
// DownloadDecision's accumulate-every-rejection behavior -- Disagreement 6).
func qualityRejections(p quality.Profile, rel common.ReleaseInfo, score int) []common.Rejection {
	var out []common.Rejection
	if !p.Allowed(rel.Quality) {
		out = append(out, newRejection(ReasonQualityNotWanted, "%s is not wanted in this profile", rel.Quality.Name))
	}
	if score < p.MinFormatScore {
		out = append(out, newRejection(ReasonCustomFormatMinimumScore,
			"custom format score %d is below the profile minimum %d", score, p.MinFormatScore))
	}
	return out
}
```

```bash
cd /home/appkins/src/mediactl/clustarr && go test ./pkg/decision/... -run TestQualityRejections -v
# expect: PASS
git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "feat(decision): quality-in-profile and MinFormatScore checks" -- pkg/decision/checks.go pkg/decision/checks_test.go
```

- [ ] **Step 6: Failing test — language check (`any`/`original`/a specific language)**

Add to `checks_test.go`:
```go
func TestLanguageRejection(t *testing.T) {
	t.Run("any accepts everything", func(t *testing.T) {
		p := quality.Profile{LanguageName: "any"}
		require.Nil(t, languageRejection(Target{}, p, &release.ParsedRelease{Languages: []string{"French"}}))
	})
	t.Run("original language present", func(t *testing.T) {
		p := quality.Profile{LanguageName: "original"}
		tg := Target{OriginalLanguage: "Japanese"}
		require.Nil(t, languageRejection(tg, p, &release.ParsedRelease{Languages: []string{"Japanese", "English"}}))
	})
	t.Run("original language missing rejects", func(t *testing.T) {
		p := quality.Profile{LanguageName: "original"}
		tg := Target{OriginalLanguage: "Japanese"}
		got := languageRejection(tg, p, &release.ParsedRelease{Languages: []string{"English"}})
		require.NotNil(t, got)
		require.Contains(t, got.Reason, ReasonWantedLanguage.Code)
	})
	t.Run("specific wanted language missing rejects", func(t *testing.T) {
		p := quality.Profile{LanguageName: "German"}
		require.NotNil(t, languageRejection(Target{}, p, &release.ParsedRelease{Languages: []string{"English"}}))
	})
	t.Run("empty LanguageName means no constraint", func(t *testing.T) {
		p := quality.Profile{LanguageName: ""}
		require.Nil(t, languageRejection(Target{}, p, &release.ParsedRelease{Languages: nil}))
	})
}
```

```bash
cd /home/appkins/src/mediactl/clustarr && go test ./pkg/decision/... -run TestLanguageRejection -v
# expect: FAIL -- undefined: languageRejection
```

Add to `checks.go`:
```go
// languageRejection is LanguageSpecification, using Profile.LanguageName --
// go doc ./pkg/quality: "'original' and 'any' pass through unchanged, an
// empty Language stays empty (no constraint)".
func languageRejection(t Target, p quality.Profile, parsed *release.ParsedRelease) *common.Rejection {
	switch p.LanguageName {
	case "", "any":
		return nil
	case "original":
		if containsFold(parsed.Languages, t.OriginalLanguage) {
			return nil
		}
		r := newRejection(ReasonWantedLanguage, "original language %s is wanted, but found %v", t.OriginalLanguage, parsed.Languages)
		return &r
	default:
		if containsFold(parsed.Languages, p.LanguageName) {
			return nil
		}
		r := newRejection(ReasonWantedLanguage, "%s is wanted, but found %v", p.LanguageName, parsed.Languages)
		return &r
	}
}

func containsFold(haystack []string, needle string) bool {
	for _, s := range haystack {
		if strings.EqualFold(s, needle) {
			return true
		}
	}
	return false
}
```

Add `"github.com/mediactl/clustarr/pkg/release"` to `checks.go`'s imports (needed for `*release.ParsedRelease` parameters from this step on).

```bash
cd /home/appkins/src/mediactl/clustarr && go test ./pkg/decision/... -run TestLanguageRejection -v
# expect: PASS
git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "feat(decision): language check" -- pkg/decision/checks.go pkg/decision/checks_test.go
```

- [ ] **Step 7: Failing test — sample rejection**

Add to `checks_test.go`:
```go
func TestSampleRejection(t *testing.T) {
	t.Run("small file with sample in the title rejects", func(t *testing.T) {
		got := sampleRejection(common.ReleaseInfo{Title: "Arrival.2016.1080p.BluRay.x264-GROUP.sample", SizeBytes: 41_943_040}) // 40 MiB
		require.NotNil(t, got)
		require.Contains(t, got.Reason, ReasonSample.Code)
	})
	t.Run("sample in the title but large enough to be a real file", func(t *testing.T) {
		require.Nil(t, sampleRejection(common.ReleaseInfo{Title: "Arrival.2016.sample.pack.1080p.BluRay-GROUP", SizeBytes: 10_000_000_000}))
	})
	t.Run("no sample token", func(t *testing.T) {
		require.Nil(t, sampleRejection(common.ReleaseInfo{Title: "Arrival.2016.1080p.BluRay.x264-GROUP", SizeBytes: 1000}))
	})
	t.Run("case-insensitive", func(t *testing.T) {
		require.NotNil(t, sampleRejection(common.ReleaseInfo{Title: "Arrival.2016.SAMPLE.mkv", SizeBytes: 1}))
	})
}
```

```bash
cd /home/appkins/src/mediactl/clustarr && go test ./pkg/decision/... -run TestSampleRejection -v
# expect: FAIL -- undefined: sampleRejection
```

Add to `checks.go`:
```go
// sampleMaxBytes is NotSampleSpecification's exact threshold: 70 MB (decimal,
// as .NET's `70.Megabytes()` extension resolves it).
const sampleMaxBytes = 70 * 1_000_000

func sampleRejection(rel common.ReleaseInfo) *common.Rejection {
	if !strings.Contains(strings.ToLower(rel.Title), "sample") {
		return nil
	}
	if rel.SizeBytes <= 0 || rel.SizeBytes >= sampleMaxBytes {
		return nil
	}
	r := newRejection(ReasonSample, "title contains \"sample\" and is under 70 MB")
	return &r
}
```

```bash
cd /home/appkins/src/mediactl/clustarr && go test ./pkg/decision/... -run TestSampleRejection -v
# expect: PASS
git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "feat(decision): sample rejection" -- pkg/decision/checks.go pkg/decision/checks_test.go
```

- [ ] **Step 8: Failing test — blocklist and already-imported (by hash, by name)**

Add to `checks_test.go`:
```go
func TestBlocklistAndAlreadyImportedRejections(t *testing.T) {
	t.Run("blocklisted", func(t *testing.T) {
		tg := Target{Blocklist: func(hash, title string) bool { return hash == "deadbeef" }}
		got := blocklistAndHistoryRejections(tg, common.ReleaseInfo{InfoHash: "deadbeef", Title: "x"})
		require.Len(t, got, 1)
		require.Contains(t, got[0].Reason, ReasonBlocklisted.Code)
	})
	t.Run("nil Blocklist func never rejects", func(t *testing.T) {
		require.Empty(t, blocklistAndHistoryRejections(Target{}, common.ReleaseInfo{InfoHash: "x"}))
	})
	t.Run("same hash as the currently-imported file's source", func(t *testing.T) {
		tg := Target{Current: &Current{SourceHash: "ABCDEF", SourceTitle: "Some.Other.Title"}}
		got := blocklistAndHistoryRejections(tg, common.ReleaseInfo{InfoHash: "abcdef", Title: "Different.Title"})
		require.Len(t, got, 1)
		require.Contains(t, got[0].Reason, ReasonAlreadyImportedSameHash.Code)
	})
	t.Run("same title as the currently-imported file's source (usenet, no hash)", func(t *testing.T) {
		tg := Target{Current: &Current{SourceTitle: "Arrival.2016.1080p.BluRay.x264-GROUP"}}
		got := blocklistAndHistoryRejections(tg, common.ReleaseInfo{Title: "arrival.2016.1080p.bluray.x264-group"})
		require.Len(t, got, 1)
		require.Contains(t, got[0].Reason, ReasonAlreadyImportedSameName.Code)
	})
	t.Run("no Current means never already-imported", func(t *testing.T) {
		require.Empty(t, blocklistAndHistoryRejections(Target{}, common.ReleaseInfo{Title: "anything"}))
	})
	t.Run("hash takes priority over a simultaneous name match, one rejection only", func(t *testing.T) {
		tg := Target{Current: &Current{SourceHash: "AAAA", SourceTitle: "Same.Title"}}
		got := blocklistAndHistoryRejections(tg, common.ReleaseInfo{InfoHash: "aaaa", Title: "Same.Title"})
		require.Len(t, got, 1)
		require.Contains(t, got[0].Reason, ReasonAlreadyImportedSameHash.Code)
	})
}
```

```bash
cd /home/appkins/src/mediactl/clustarr && go test ./pkg/decision/... -run TestBlocklistAndAlreadyImportedRejections -v
# expect: FAIL -- undefined: blocklistAndHistoryRejections
```

Add to `checks.go`:
```go
// blocklistAndHistoryRejections is BlocklistSpecification +
// AlreadyImportedSpecification, simplified to a direct hash/title compare
// against Target.Current (Disagreement 3): the real AlreadyImportedSpecification
// also skips the check when the last grab and the last import were the same
// quality, which needs a separate "last grabbed" record this task's Target
// does not carry; omitted, documented here rather than silently dropped.
func blocklistAndHistoryRejections(t Target, rel common.ReleaseInfo) []common.Rejection {
	var out []common.Rejection
	if t.Blocklist != nil && t.Blocklist(rel.InfoHash, rel.Title) {
		out = append(out, newRejection(ReasonBlocklisted, "release is blocklisted"))
	}
	if cur := t.Current; cur != nil {
		switch {
		case cur.SourceHash != "" && rel.InfoHash != "" && strings.EqualFold(cur.SourceHash, rel.InfoHash):
			out = append(out, newRejection(ReasonAlreadyImportedSameHash, "has the same hash as a grabbed and imported release"))
		case cur.SourceTitle != "" && strings.EqualFold(cur.SourceTitle, rel.Title):
			out = append(out, newRejection(ReasonAlreadyImportedSameName, "has the same title as a grabbed and imported release"))
		}
	}
	return out
}
```

```bash
cd /home/appkins/src/mediactl/clustarr && go test ./pkg/decision/... -run TestBlocklistAndAlreadyImportedRejections -v
# expect: PASS
git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "feat(decision): blocklist and already-imported-by-hash/name checks" -- pkg/decision/checks.go pkg/decision/checks_test.go
```

- [ ] **Step 9: Failing test — queue preference (a higher-or-equal release already downloading)**

Add to `checks_test.go`:
```go
func TestQueueRejection(t *testing.T) {
	bluray1080, _ := quality.Lookup("video", "Bluray-1080p")
	webdl720, _ := quality.Lookup("video", "WEBDL-720p")
	p := quality.Profile{
		Tiers: [][]quality.Definition{{bluray1080}, {webdl720}}, CutoffIndex: 0,
		UpgradeAllowed: true, CutoffFormatScore: 10000, MinUpgradeFormatScore: 1, ProperPolicy: "preferAndUpgrade",
	}

	t.Run("nothing queued", func(t *testing.T) {
		require.Nil(t, queueRejection(p, Target{}, quality.Candidate{Quality: bluray1080.Quality}))
	})
	t.Run("queued release is equal-or-better, candidate rejected", func(t *testing.T) {
		tg := Target{Queue: []Queued{{Quality: bluray1080.Quality, Revision: common.Revision{Version: 1}}}}
		candidate := quality.Candidate{Quality: bluray1080.Quality, Revision: common.Revision{Version: 1}}
		got := queueRejection(p, tg, candidate)
		require.NotNil(t, got)
		require.Contains(t, got.Reason, ReasonQueueHigherPreference.Code)
	})
	t.Run("candidate is strictly better than what's queued, not rejected", func(t *testing.T) {
		tg := Target{Queue: []Queued{{Quality: webdl720.Quality}}}
		candidate := quality.Candidate{Quality: bluray1080.Quality}
		require.Nil(t, queueRejection(p, tg, candidate))
	})
}
```

```bash
cd /home/appkins/src/mediactl/clustarr && go test ./pkg/decision/... -run TestQueueRejection -v
# expect: FAIL -- undefined: queueRejection
```

Add to `checks.go`:
```go
// queueRejection is QueueSpecification, simplified per the task brief to a
// single check (Disagreement 2 / the task brief's own "queue preference (a
// higher-or-equal release already downloading)", not Radarr's seven-way
// QueueCutoffMet/QueueHigherPreference/... split): reject as soon as any
// queued entry is not upgraded-over by the candidate.
func queueRejection(p quality.Profile, t Target, candidate quality.Candidate) *common.Rejection {
	for _, q := range t.Queue {
		if p.UpgradeDecision(q, candidate) != quality.Upgrade {
			r := newRejection(ReasonQueueHigherPreference, "a release of equal or higher preference is already downloading")
			return &r
		}
	}
	return nil
}
```

```bash
cd /home/appkins/src/mediactl/clustarr && go test ./pkg/decision/... -run TestQueueRejection -v
# expect: PASS
git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "feat(decision): queue-preference check" -- pkg/decision/checks.go pkg/decision/checks_test.go
```

- [ ] **Step 10: Failing test — the Upgradable-table integration (`upgradeRejection`)**

Add to `checks_test.go`:
```go
func TestUpgradeRejection(t *testing.T) {
	bluray1080, _ := quality.Lookup("video", "Bluray-1080p")
	webdl720, _ := quality.Lookup("video", "WEBDL-720p")
	p := quality.Profile{
		Tiers: [][]quality.Definition{{bluray1080}, {webdl720}}, CutoffIndex: 0,
		UpgradeAllowed: true, CutoffFormatScore: 10000, MinUpgradeFormatScore: 1, ProperPolicy: "preferAndUpgrade",
	}

	t.Run("no current file, nothing to upgrade over", func(t *testing.T) {
		require.Nil(t, upgradeRejection(p, Target{}, quality.Candidate{Quality: webdl720.Quality}))
	})
	t.Run("candidate is a real upgrade, not rejected", func(t *testing.T) {
		tg := Target{Current: &Current{Quality: webdl720.Quality, Revision: common.Revision{Version: 1}}}
		got := upgradeRejection(p, tg, quality.Candidate{Quality: bluray1080.Quality, Revision: common.Revision{Version: 1}})
		require.Nil(t, got)
	})
	t.Run("candidate is worse quality than current, rejected as ExistingHigherPreference", func(t *testing.T) {
		tg := Target{Current: &Current{Quality: bluray1080.Quality}}
		got := upgradeRejection(p, tg, quality.Candidate{Quality: webdl720.Quality})
		require.NotNil(t, got)
		require.Contains(t, got.Reason, ReasonExistingHigherPreference.Code)
	})
	t.Run("upgrades not allowed on the profile", func(t *testing.T) {
		np := p
		np.UpgradeAllowed = false
		tg := Target{Current: &Current{Quality: bluray1080.Quality, Revision: common.Revision{Version: 1}}}
		got := upgradeRejection(np, tg, quality.Candidate{Quality: bluray1080.Quality, Revision: common.Revision{Version: 1}})
		require.NotNil(t, got)
		require.Contains(t, got.Reason, ReasonUpgradesNotAllowed.Code)
	})
}
```

```bash
cd /home/appkins/src/mediactl/clustarr && go test ./pkg/decision/... -run TestUpgradeRejection -v
# expect: FAIL -- undefined: upgradeRejection
```

Add to `checks.go`:
```go
// upgradeRejection is the UpgradableSpecification table (Disagreement 2):
// called against Target.Current via quality.Profile.UpgradeDecision, never
// reimplemented here. Returns nil (no rejection) both when there is no
// current file and when the candidate is a genuine Upgrade.
func upgradeRejection(p quality.Profile, t Target, candidate quality.Candidate) *common.Rejection {
	if t.Current == nil {
		return nil
	}
	current := quality.Candidate{Quality: t.Current.Quality, Revision: t.Current.Revision, FormatScore: t.Current.FormatScore}
	v := p.UpgradeDecision(current, candidate)
	if v == quality.Upgrade {
		return nil
	}
	reason, ok := VerdictReason(v)
	if !ok {
		// Defensive only: TestVerdictReasonTableIsExhaustive (Step 1) proves
		// every non-Upgrade Verdict has an entry, so this never runs in practice.
		reason = ReasonUpgradesNotAllowed
	}
	r := newRejection(reason, "current file or queue entry is preferred over %s", candidate.Quality.Name)
	return &r
}
```

```bash
cd /home/appkins/src/mediactl/clustarr && go test ./pkg/decision/... -run TestUpgradeRejection -v
# expect: PASS
git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "feat(decision): wire the UpgradableSpecification table via quality.Profile.UpgradeDecision" -- pkg/decision/checks.go pkg/decision/checks_test.go
```

- [ ] **Step 11: Failing test — `Evaluate` wires every check together and populates `RankKey` for approved decisions**

Create `testdata/decision/releases.json`:
```json
[
  {
    "guid": "hd-tracker:12345",
    "indexerRef": "hd-tracker",
    "indexerName": "HD Tracker",
    "title": "Arrival.2016.1080p.BluRay.x264-GROUP",
    "protocol": "torrent",
    "sizeBytes": 10200000000,
    "infoHash": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
    "seeders": 42,
    "leechers": 3,
    "indexerFlags": ["freeleech"]
  },
  {
    "guid": "hd-tracker:12346",
    "indexerRef": "hd-tracker",
    "indexerName": "HD Tracker",
    "title": "Arrival.2016.720p.WEB-DL.x264-GROUP",
    "protocol": "torrent",
    "sizeBytes": 3500000000,
    "infoHash": "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB",
    "seeders": 10,
    "leechers": 1
  },
  {
    "guid": "usenet-1:98765",
    "indexerRef": "usenet-1",
    "indexerName": "Usenet One",
    "title": "Arrival.2016.1080p.BluRay.x264-GROUP.sample",
    "protocol": "usenet",
    "sizeBytes": 41943040
  },
  {
    "guid": "hd-tracker:12347",
    "indexerRef": "hd-tracker",
    "indexerName": "HD Tracker",
    "title": "Arrival.2016.1080p.WEBRip.x264-BADGRP",
    "protocol": "torrent",
    "sizeBytes": 419430400,
    "infoHash": "CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC",
    "seeders": 1
  }
]
```
(Every `title`/quality pair was verified against the real parser while this plan was written: `release.Parse` resolves entry 1 to `Bluray-1080p`, entry 2 to `WEBDL-720p`, entry 3 to `Bluray-1080p` with an empty `ReleaseGroup` — the trailing `.sample` breaks the release-group regex, a real and harmless parser quirk, not a bug to fix here — and entry 4 to `WEBRip-1080p`.)

`pkg/decision/evaluate_test.go`:
```go
/* GPL header */

package decision_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

// loadReleases reads testdata/decision/releases.json the same way
// pkg/quality/trash_corpus_test.go reads testdata/trash: os.ReadFile with a
// filepath.Join("..", "..", "testdata", ...) relative path, not go:embed --
// an embed pattern cannot contain ".." (it may only reach files inside its
// own package directory), so it cannot see a repo-root testdata/ directory
// from pkg/decision/.
func loadReleases(t *testing.T) []common.ReleaseInfo {
	t.Helper()
	doc, err := os.ReadFile(filepath.Join("..", "..", "testdata", "decision", "releases.json"))
	require.NoError(t, err)
	var rels []common.ReleaseInfo
	require.NoError(t, json.Unmarshal(doc, &rels))
	return rels
}

func TestEvaluateFullPipeline(t *testing.T) {
	bluray1080, _ := quality.Lookup("video", "Bluray-1080p")
	webdl720, _ := quality.Lookup("video", "WEBDL-720p")
	p := quality.Profile{
		Tiers:             [][]quality.Definition{{bluray1080}, {webdl720}},
		CutoffIndex:       0,
		UpgradeAllowed:    true,
		CutoffFormatScore: 10000,
		MinUpgradeFormatScore: 1,
		ProperPolicy:      "preferAndUpgrade",
		LanguageName:      "any",
		Sizes:             quality.MovieSizeTable(),
		PreferredProtocol: "torrent",
	}
	tg := decision.Target{
		Kind:             common.MediaKindMovie,
		Available:        true,
		OriginalLanguage: "English",
	}
	o := decision.Options{
		UserInvoked:       true,
		ProtocolsEnabled:  map[string]bool{"torrent": true, "usenet": true},
		IndexerPriority:   map[string]int{"hd-tracker": 5},
		PreferredProtocol: "torrent",
	}

	ds := decision.Evaluate(context.Background(), tg, p, &catalogue.Catalogue{}, loadReleases(t), o)
	require.Len(t, ds, 4)

	require.True(t, ds[0].Approved, "Bluray-1080p, well within size bounds")
	require.Empty(t, ds[0].Rejections)
	require.Equal(t, 0, ds[0].Rank.QualityIndex)

	require.True(t, ds[1].Approved, "WEBDL-720p, lower tier but still in the profile")
	require.Equal(t, 1, ds[1].Rank.QualityIndex)

	require.False(t, ds[2].Approved, "sample release")
	require.False(t, ds[2].TemporarilyRejected, "every reason this package emits is Permanent")
	found := false
	for _, r := range ds[2].Rejections {
		if r.Reason[:len(decision.ReasonSample.Code)] == decision.ReasonSample.Code {
			found = true
		}
	}
	require.True(t, found, "expected a Sample rejection among %+v", ds[2].Rejections)

	require.False(t, ds[3].Approved, "WEBRip-1080p, 400 MB is under the 1.44 GB floor at 110 minutes")
}
```

```bash
cd /home/appkins/src/mediactl/clustarr && go test ./pkg/decision/... -run TestEvaluateFullPipeline -v
# expect: FAIL -- undefined: decision.Evaluate
```

Implement `pkg/decision/evaluate.go`:
```go
/* GPL header */

package decision

import (
	"context"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
	"github.com/mediactl/clustarr/pkg/release"
)

// Evaluate -- see the doc comment on the declaration under Interfaces —
// Produces above.
func Evaluate(ctx context.Context, t Target, p quality.Profile, cat *catalogue.Catalogue, rels []common.ReleaseInfo, o Options) []Decision {
	out := make([]Decision, 0, len(rels))
	for _, rel := range rels {
		out = append(out, evaluateOne(ctx, t, p, cat, rel, o))
	}
	return out
}

func evaluateOne(ctx context.Context, t Target, p quality.Profile, cat *catalogue.Catalogue, rel common.ReleaseInfo, o Options) Decision {
	parsed, err := release.Parse(rel.Title, release.Options{Kind: t.Kind})
	if err != nil {
		logging.FromContext(ctx).Debug("decision: release title did not parse", "title", rel.Title, "err", err)
		return Decision{
			Release:    rel,
			Rejections: []common.Rejection{newRejection(ReasonUnableToParse, "%v", err)},
		}
	}
	parsed.ApplyTo(&rel)

	ic := catalogue.ItemContext{OriginalLanguage: t.OriginalLanguage, IndexerFlags: rel.IndexerFlags, ReleaseType: parsed.ReleaseType}
	score, matched := p.Score(ctx, cat, parsed, ic)
	rel.FormatScore = int32(score)
	rel.MatchedFormats = matched

	var rejections []common.Rejection
	add := func(r *common.Rejection) {
		if r != nil {
			rejections = append(rejections, *r)
		}
	}

	add(protocolRejection(rel, o))
	add(availabilityRejection(t, o))
	rejections = append(rejections, sizeRejections(t, p, parsed, rel)...)
	rejections = append(rejections, qualityRejections(p, rel, score)...)
	add(languageRejection(t, p, parsed))
	add(sampleRejection(rel))
	rejections = append(rejections, blocklistAndHistoryRejections(t, rel)...)

	candidate := quality.Candidate{Quality: rel.Quality, Revision: rel.Revision, FormatScore: score}
	add(queueRejection(p, t, candidate))
	add(upgradeRejection(p, t, candidate))

	d := Decision{
		Release:             rel,
		Parsed:              parsed,
		Approved:            len(rejections) == 0,
		TemporarilyRejected: len(rejections) > 0 && allTemporary(rejections),
		Rejections:          rejections,
		Score:               score,
		Matched:             matched,
	}
	if d.Approved {
		d.Rank = buildRankKey(p, o, t, parsed, rel, score)
	}
	return d
}

func allTemporary(rejections []common.Rejection) bool {
	for _, r := range rejections {
		if r.Type != common.RejectionTemporary {
			return false
		}
	}
	return true
}
```

```bash
cd /home/appkins/src/mediactl/clustarr && go test ./pkg/decision/... -run TestEvaluateFullPipeline -v
# expect: FAIL -- undefined: buildRankKey (Step 12 supplies it)
```

Add a placeholder `buildRankKey` to `evaluate.go` so this step compiles and the assertions above (which only touch `Rank.QualityIndex`) pass; Step 12 replaces the body:
```go
// buildRankKey is completed in Step 12 (primary keys) and Step 13
// (secondary keys); this step only needs QualityIndex to exist.
func buildRankKey(p quality.Profile, o Options, t Target, parsed *release.ParsedRelease, rel common.ReleaseInfo, score int) RankKey {
	idx, _ := p.Index(rel.Quality)
	return RankKey{QualityIndex: idx}
}
```

```bash
cd /home/appkins/src/mediactl/clustarr && go test ./pkg/decision/... -run TestEvaluateFullPipeline -v
# expect: PASS
git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "feat(decision): wire Evaluate end to end over the full §8.2 checklist" -- pkg/decision/evaluate.go pkg/decision/evaluate_test.go testdata/decision/releases.json
```

- [ ] **Step 12: Failing test — `Rank`'s primary key (quality index, revision, custom-format score)**

`pkg/decision/rank_test.go`:
```go
/* GPL header */

package decision_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/decision"
)

func TestRankPrimaryKey(t *testing.T) {
	t.Run("lower QualityIndex (better quality) ranks first", func(t *testing.T) {
		worse := decision.Decision{Release: common.ReleaseInfo{GUID: "worse"}, Rank: decision.RankKey{QualityIndex: 1}}
		better := decision.Decision{Release: common.ReleaseInfo{GUID: "better"}, Rank: decision.RankKey{QualityIndex: 0}}
		ranked := decision.Rank([]decision.Decision{worse, better}, decision.Options{})
		require.Equal(t, "better", ranked[0].Release.GUID)
	})

	t.Run("same quality: higher revision (proper/repack) ranks first when PreferRevision", func(t *testing.T) {
		v1 := decision.Decision{Release: common.ReleaseInfo{GUID: "v1"}, Rank: decision.RankKey{PreferRevision: true, Revision: common.Revision{Version: 1}}}
		v2 := decision.Decision{Release: common.ReleaseInfo{GUID: "v2-proper"}, Rank: decision.RankKey{PreferRevision: true, Revision: common.Revision{Version: 2}}}
		ranked := decision.Rank([]decision.Decision{v1, v2}, decision.Options{})
		require.Equal(t, "v2-proper", ranked[0].Release.GUID)
	})

	t.Run("PreferRevision false: revision is not a tiebreak (doNotPrefer)", func(t *testing.T) {
		v1 := decision.Decision{Release: common.ReleaseInfo{GUID: "v1"}, Rank: decision.RankKey{PreferRevision: false, Revision: common.Revision{Version: 1}}}
		v2 := decision.Decision{Release: common.ReleaseInfo{GUID: "v2"}, Rank: decision.RankKey{PreferRevision: false, Revision: common.Revision{Version: 2}}}
		ranked := decision.Rank([]decision.Decision{v2, v1}, decision.Options{}) // stable: original order preserved
		require.Equal(t, "v2", ranked[0].Release.GUID)
	})

	t.Run("same quality and revision: higher custom-format score ranks first", func(t *testing.T) {
		low := decision.Decision{Release: common.ReleaseInfo{GUID: "low"}, Rank: decision.RankKey{FormatScore: 10}}
		high := decision.Decision{Release: common.ReleaseInfo{GUID: "high"}, Rank: decision.RankKey{FormatScore: 50}}
		ranked := decision.Rank([]decision.Decision{low, high}, decision.Options{})
		require.Equal(t, "high", ranked[0].Release.GUID)
	})
}
```

```bash
cd /home/appkins/src/mediactl/clustarr && go test ./pkg/decision/... -run TestRankPrimaryKey -v
# expect: FAIL -- undefined: decision.Rank
```

Implement `pkg/decision/rank.go` (primary key only; Step 13 adds the rest of the chain) and complete `buildRankKey` in `evaluate.go`:
```go
/* GPL header */

package decision

import (
	"sort"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// Rank -- see the doc comment on the declaration under Interfaces —
// Produces above.
func Rank(ds []Decision, o Options) []Decision {
	out := make([]Decision, len(ds))
	copy(out, ds)
	sort.SliceStable(out, func(i, j int) bool { return less(out[i], out[j], o) })
	return out
}

func less(a, b Decision, o Options) bool {
	if a.Rank.QualityIndex != b.Rank.QualityIndex {
		return a.Rank.QualityIndex < b.Rank.QualityIndex
	}
	if a.Rank.PreferRevision {
		if c := compareRevision(a.Rank.Revision, b.Rank.Revision); c != 0 {
			return c > 0 // higher revision (Real, then Version) wins
		}
	}
	if a.Rank.FormatScore != b.Rank.FormatScore {
		return a.Rank.FormatScore > b.Rank.FormatScore
	}
	return false // Step 13 appends the remaining keys here
}

// compareRevision orders by Real, then Version -- Revision.CompareTo,
// docs/research/quality.md §7.1.
func compareRevision(x, y common.Revision) int {
	if x.Real != y.Real {
		if x.Real > y.Real {
			return 1
		}
		return -1
	}
	if x.Version != y.Version {
		if x.Version > y.Version {
			return 1
		}
		return -1
	}
	return 0
}
```

```bash
cd /home/appkins/src/mediactl/clustarr && go test ./pkg/decision/... -run TestRankPrimaryKey -v
# expect: PASS
git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "feat(decision): Rank's primary key -- quality index, revision, custom-format score" -- pkg/decision/rank.go pkg/decision/rank_test.go
```

- [ ] **Step 13: Failing test — `Rank`'s secondary keys (protocol, episode count, indexer priority, indexer flags, seeders/age, size) and the full pipeline's Rank ordering**

Add to `rank_test.go`:
```go
func TestRankSecondaryKeys(t *testing.T) {
	t.Run("preferred protocol match ranks first at equal quality", func(t *testing.T) {
		torrentPref := decision.Decision{Release: common.ReleaseInfo{GUID: "torrent"}, Rank: decision.RankKey{PreferredProtocolMatch: true}}
		usenetOther := decision.Decision{Release: common.ReleaseInfo{GUID: "usenet"}, Rank: decision.RankKey{PreferredProtocolMatch: false}}
		ranked := decision.Rank([]decision.Decision{usenetOther, torrentPref}, decision.Options{})
		require.Equal(t, "torrent", ranked[0].Release.GUID)
	})

	t.Run("higher episode count (season pack) ranks above a single episode", func(t *testing.T) {
		single := decision.Decision{Release: common.ReleaseInfo{GUID: "single"}, Rank: decision.RankKey{EpisodeCount: 1}}
		pack := decision.Decision{Release: common.ReleaseInfo{GUID: "pack"}, Rank: decision.RankKey{EpisodeCount: 8}}
		ranked := decision.Rank([]decision.Decision{single, pack}, decision.Options{})
		require.Equal(t, "pack", ranked[0].Release.GUID)
	})

	t.Run("lower indexer-priority number wins -- indexers.md: Priority 1 (highest) .. 50, default 25", func(t *testing.T) {
		a := decision.Decision{Release: common.ReleaseInfo{GUID: "a", IndexerRef: "prio-5"}}
		b := decision.Decision{Release: common.ReleaseInfo{GUID: "b", IndexerRef: "prio-30"}}
		o := decision.Options{IndexerPriority: map[string]int{"prio-5": 5, "prio-30": 30}}
		ranked := decision.Rank([]decision.Decision{b, a}, o)
		require.Equal(t, "a", ranked[0].Release.GUID)
	})

	t.Run("an indexer absent from IndexerPriority defaults to 25", func(t *testing.T) {
		known := decision.Decision{Release: common.ReleaseInfo{GUID: "known", IndexerRef: "prio-10"}}
		unknown := decision.Decision{Release: common.ReleaseInfo{GUID: "unknown", IndexerRef: "no-entry"}}
		o := decision.Options{IndexerPriority: map[string]int{"prio-10": 10}}
		ranked := decision.Rank([]decision.Decision{unknown, known}, o) // 10 < 25
		require.Equal(t, "known", ranked[0].Release.GUID)
	})

	t.Run("freeleech (+2) outranks halfleech (+1) outranks no flags", func(t *testing.T) {
		free := decision.Decision{Release: common.ReleaseInfo{GUID: "free", IndexerFlags: []string{common.IndexerFlagFreeleech}}}
		half := decision.Decision{Release: common.ReleaseInfo{GUID: "half", IndexerFlags: []string{common.IndexerFlagHalfleech}}}
		none := decision.Decision{Release: common.ReleaseInfo{GUID: "none"}}
		ranked := decision.Rank([]decision.Decision{none, half, free}, decision.Options{})
		require.Equal(t, []string{"free", "half", "none"}, []string{ranked[0].Release.GUID, ranked[1].Release.GUID, ranked[2].Release.GUID})
	})

	t.Run("more seeders wins for torrent", func(t *testing.T) {
		many := seeders(200)
		few := seeders(2)
		a := decision.Decision{Release: common.ReleaseInfo{GUID: "many", Protocol: common.ProtocolTorrent, Seeders: &many}}
		b := decision.Decision{Release: common.ReleaseInfo{GUID: "few", Protocol: common.ProtocolTorrent, Seeders: &few}}
		ranked := decision.Rank([]decision.Decision{b, a}, decision.Options{})
		require.Equal(t, "many", ranked[0].Release.GUID)
	})

	t.Run("size: closest to the profile's preferred size wins when the quality has a real preference", func(t *testing.T) {
		closer := decision.Decision{Release: common.ReleaseInfo{GUID: "closer"}, Rank: decision.RankKey{SizeDeltaBucket: 200 * 1024 * 1024}}
		farther := decision.Decision{Release: common.ReleaseInfo{GUID: "farther"}, Rank: decision.RankKey{SizeDeltaBucket: 800 * 1024 * 1024}}
		ranked := decision.Rank([]decision.Decision{farther, closer}, decision.Options{})
		require.Equal(t, "closer", ranked[0].Release.GUID)
	})

	t.Run("size: largest wins when the quality's preferred size is the TRaSH \"biggest\" sentinel", func(t *testing.T) {
		bigger := decision.Decision{Release: common.ReleaseInfo{GUID: "bigger"}, Rank: decision.RankKey{PreferLargestSize: true, SizeBytes: 9_000_000_000}}
		smaller := decision.Decision{Release: common.ReleaseInfo{GUID: "smaller"}, Rank: decision.RankKey{PreferLargestSize: true, SizeBytes: 4_000_000_000}}
		ranked := decision.Rank([]decision.Decision{smaller, bigger}, decision.Options{})
		require.Equal(t, "bigger", ranked[0].Release.GUID)
	})
}

func seeders(n int32) int32 { return n }
```

Also add to `evaluate_test.go`'s `TestEvaluateFullPipeline`, after the existing assertions:
```go
	approved := []decision.Decision{ds[0], ds[1]}
	ranked := decision.Rank(approved, o)
	require.Equal(t, "hd-tracker:12345", ranked[0].Release.GUID, "Bluray-1080p outranks WEBDL-720p")
	require.Equal(t, "hd-tracker:12346", ranked[1].Release.GUID)
```

```bash
cd /home/appkins/src/mediactl/clustarr && go test ./pkg/decision/... -run 'TestRankSecondaryKeys|TestEvaluateFullPipeline' -v
# expect: FAIL -- secondary-key subtests fail (less() falls through to false, a stable no-op past FormatScore); size-related RankKey fields (PreferLargestSize etc.) and buildRankKey's real body do not exist yet
```

Complete `buildRankKey` in `evaluate.go` (replacing Step 11's placeholder) and extend `less` plus its helpers in `rank.go`:
```go
// evaluate.go -- replaces the Step 11 placeholder.

import "math" // add to evaluate.go's import block

func buildRankKey(p quality.Profile, o Options, t Target, parsed *release.ParsedRelease, rel common.ReleaseInfo, score int) RankKey {
	idx, _ := p.Index(rel.Quality)

	preferredProtocol := o.PreferredProtocol
	if preferredProtocol == "" {
		preferredProtocol = p.PreferredProtocol
	}
	protocolMatch := preferredProtocol == "any" || string(rel.Protocol) == preferredProtocol

	episodeCount := 1
	switch {
	case parsed.FullSeason:
		episodeCount = math.MaxInt32
	case len(parsed.Episodes) > 1:
		episodeCount = len(parsed.Episodes)
	}

	sl := p.Sizes[rel.Quality.Name]
	key := RankKey{
		QualityIndex:           idx,
		PreferRevision:         p.ProperPolicy != "doNotPrefer",
		Revision:                rel.Revision,
		FormatScore:             score,
		PreferredProtocolMatch: protocolMatch,
		EpisodeCount:            episodeCount,
	}
	if preferLargest(sl) {
		key.PreferLargestSize = true
		key.SizeBytes = rel.SizeBytes
		return key
	}
	if minutes, ok := targetRuntimeMinutes(t, parsed); ok {
		prefBytes := int64(sl.PrefMBPerMin * float64(minutes) * 1024 * 1024)
		key.SizeDeltaBucket = roundTo200MiB(abs64(rel.SizeBytes - prefBytes))
	}
	return key
}

// preferLargest reports whether q's preferred size is TRaSH's own "biggest"
// sentinel rather than a real target: the shipped tables set PrefMBPerMin
// one below MaxMBPerMin (1999/2000 movies, 1999/2000 movies and anime, 995/1000 series) to mean
// exactly that (docs/research/quality.md §2.2: `"2000" is the UI value for
// unlimited, 1999 preferred = "biggest"`). A profile's SizeLimits override
// that sets a materially lower preferred value is a real target and takes
// the closest-to-preferred branch instead.
// preferLargestRatio is how close PrefMBPerMin must sit to MaxMBPerMin before
// the pair is read as TRaSH's "no effective ceiling, take the biggest" sentinel
// rather than a real target. The real tables sit at 0.9995 (movies and anime,
// 1999/2000) and 0.9950 (series, 995/1000), while an ordinary profile's
// preferred size is far below its max, so 0.99 separates them with room to
// spare. An absolute tolerance does NOT work here: `Pref >= Max-1` matches
// 1999/2000 but misses 995/1000, silently breaking the size branch for every
// standard TV series.
const preferLargestRatio = 0.99

func preferLargest(sl quality.SizeLimit) bool {
	if sl.MaxMBPerMin == 0 {
		return true // unlimited
	}
	return sl.PrefMBPerMin >= sl.MaxMBPerMin*preferLargestRatio
}

func abs64(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}

const sizeBucket = 200 * 1024 * 1024 // 200 MiB, DownloadDecisionComparer.CompareSize's own bucket

func roundTo200MiB(n int64) int64 {
	return (n + sizeBucket/2) / sizeBucket * sizeBucket
}
```

```go
// rank.go -- extends less() and adds the remaining comparators.

func less(a, b Decision, o Options) bool {
	if a.Rank.QualityIndex != b.Rank.QualityIndex {
		return a.Rank.QualityIndex < b.Rank.QualityIndex
	}
	if a.Rank.PreferRevision {
		if c := compareRevision(a.Rank.Revision, b.Rank.Revision); c != 0 {
			return c > 0
		}
	}
	if a.Rank.FormatScore != b.Rank.FormatScore {
		return a.Rank.FormatScore > b.Rank.FormatScore
	}
	if a.Rank.PreferredProtocolMatch != b.Rank.PreferredProtocolMatch {
		return a.Rank.PreferredProtocolMatch
	}
	if a.Rank.EpisodeCount != b.Rank.EpisodeCount {
		return a.Rank.EpisodeCount > b.Rank.EpisodeCount
	}
	if pa, pb := indexerPriority(o, a.Release.IndexerRef), indexerPriority(o, b.Release.IndexerRef); pa != pb {
		return pa < pb
	}
	if fa, fb := indexerFlagScore(a.Release.IndexerFlags), indexerFlagScore(b.Release.IndexerFlags); fa != fb {
		return fa > fb
	}
	if sa, sb := seedersOrAgeScore(a.Release), seedersOrAgeScore(b.Release); sa != sb {
		return sa > sb
	}
	if a.Rank.PreferLargestSize {
		return a.Rank.SizeBytes > b.Rank.SizeBytes
	}
	return a.Rank.SizeDeltaBucket < b.Rank.SizeDeltaBucket
}

// defaultIndexerPriority is indexers.md's documented default: "Priority int
// -- 1 (highest) … 50; used as the dedup tiebreaker ... default 25".
const defaultIndexerPriority = 25

func indexerPriority(o Options, ref string) int {
	if p, ok := o.IndexerPriority[ref]; ok {
		return p
	}
	return defaultIndexerPriority
}

// indexerFlagScore ports DownloadDecisionComparer.ScoreFlags's weights
// verbatim (verified against the vendored Radarr source, Disagreement 7):
// freeleech/doubleupload/internal +2, halfleech +1. neutralleech/exclusive/
// scene have no *arr equivalent and score 0.
func indexerFlagScore(flags []string) int {
	score := 0
	for _, f := range flags {
		switch f {
		case common.IndexerFlagFreeleech, common.IndexerFlagDoubleUpload, common.IndexerFlagInternal:
			score += 2
		case common.IndexerFlagHalfleech:
			score++
		}
	}
	return score
}

// seedersOrAgeScore ports ComparePeersIfTorrent/CompareAgeIfUsenet: torrent
// releases rank by log10(seeders); usenet releases rank by an age bucket
// (fresher wins), both verified against the vendored Radarr source.
func seedersOrAgeScore(rel common.ReleaseInfo) float64 {
	switch rel.Protocol {
	case common.ProtocolTorrent:
		if rel.Seeders == nil || *rel.Seeders <= 0 {
			return 0
		}
		return math.Round(math.Log10(float64(*rel.Seeders)))
	case common.ProtocolUsenet:
		days, hours, _ := release.Age(rel.PublishedAt.Time, time.Now())
		switch {
		case hours < 1:
			return 1000
		case hours <= 24:
			return 100
		case days <= 7:
			return 10
		default:
			return math.Round(math.Log10(float64(days))) * -1
		}
	default:
		return 0
	}
}
```
Add `"math"`, `"time"`, and `"github.com/mediactl/clustarr/pkg/release"` to `rank.go`'s imports.

```bash
cd /home/appkins/src/mediactl/clustarr && go test ./pkg/decision/... -run 'TestRankPrimaryKey|TestRankSecondaryKeys|TestEvaluateFullPipeline' -v
# expect: PASS
git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "feat(decision): Rank's secondary keys -- protocol, episode count, indexer priority/flags, seeders/age, size" -- pkg/decision/rank.go pkg/decision/rank_test.go pkg/decision/evaluate.go pkg/decision/evaluate_test.go
```

- [ ] **Step 14: `doc.go` and a final adversarial pass**

Write `pkg/decision/doc.go`:
```go
/* GPL header */

// Package decision is the pure release-decision engine every search, RSS and
// grab path runs (spec §7, §8.2, §9). Evaluate runs the full §8.2 checklist
// (protocol, availability, size, quality, MinFormatScore, language, sample,
// blocklist, already-imported, queue preference, and the
// UpgradableSpecification table via pkg/quality.Profile.UpgradeDecision)
// against every candidate release for one Target, and Rank orders the
// approved ones. Every rejection is a typed Reason, not a bare string
// (reasons.go), and every one this package emits is common.RejectionPermanent
// -- see the package's task plan for why. This package has no Kubernetes
// client and does no I/O or network access; a controller calls Evaluate/Rank
// and stores the result (CLAUDE.md's "pure functions where the logic is
// tricky").
package decision
```

Add to `checks_test.go` (adversarial cases a reviewer will otherwise ask for):
```go
func TestEvaluateAdversarial(t *testing.T) {
	p := quality.Profile{LanguageName: "any", Sizes: quality.MovieSizeTable()}
	tg := decision.Target{Kind: common.MediaKindMovie, Available: true}
	o := decision.Options{UserInvoked: true, ProtocolsEnabled: map[string]bool{"torrent": true, "usenet": true}}

	t.Run("unparseable title rejects with exactly one reason and Parsed is nil", func(t *testing.T) {
		rel := common.ReleaseInfo{Title: "", Protocol: common.ProtocolTorrent}
		ds := decision.Evaluate(context.Background(), tg, p, &catalogue.Catalogue{}, []common.ReleaseInfo{rel}, o)
		require.Len(t, ds, 1)
		require.False(t, ds[0].Approved)
		require.Nil(t, ds[0].Parsed)
	})

	t.Run("empty release slice returns an empty, non-nil slice", func(t *testing.T) {
		ds := decision.Evaluate(context.Background(), tg, p, &catalogue.Catalogue{}, nil, o)
		require.NotNil(t, ds)
		require.Empty(t, ds)
	})

	t.Run("Rank on an empty slice does not panic", func(t *testing.T) {
		require.Empty(t, decision.Rank(nil, o))
	})
}
```
(Uses `release.Parse`'s real error path: confirm with `go doc ./pkg/release` that an empty title returns an error before relying on it -- if it instead returns a zero-value `ParsedRelease` with no error, change the fixture to a title guaranteed to fail every kind's regex family, e.g. a string of only punctuation, and note which behavior was real in the final report.)

```bash
cd /home/appkins/src/mediactl/clustarr && go test ./pkg/decision/... -run TestEvaluateAdversarial -v
# expect: PASS (or: adjust the empty-title fixture per the note above, then PASS)
git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "docs(decision): package doc and adversarial edge-case coverage" -- pkg/decision/doc.go pkg/decision/checks_test.go
```

**Verification:**
```bash
cd /home/appkins/src/mediactl/clustarr

# Builds and vets clean.
go build ./pkg/decision/...
go vet ./pkg/decision/...

# Full suite, race detector on.
go test -race -count=1 ./pkg/decision/... -v

# Every new .go file carries the GPL header.
for f in $(git ls-files pkg/decision '*.go'); do
  head -1 "$f" | grep -q '^/\*$' || echo "MISSING GPL HEADER: $f"
done

# Pure package: no Kubernetes client, no network, no api/catalog import
# (Disagreement 1 -- Evaluate takes quality.Profile, never QualityProfileSpec).
grep -rn 'sigs.k8s.io/controller-runtime\|k8s.io/client-go' pkg/decision/ && echo "FAIL: k8s client import found" || echo "OK: no k8s client"
grep -rn 'api/catalog/v1alpha1' pkg/decision/*.go && echo "FAIL: api/catalog import found (Disagreement 1)" || echo "OK: no api/catalog import"
grep -rn 'http.Get\|http.Post\|net.Dial\|os.Open' pkg/decision/*.go | grep -v _test.go && echo "FAIL: I/O found outside tests" || echo "OK: no I/O outside tests"

# forbidigo / no Status().Update its own way N/A here (no Kubernetes types at
# all), but run the project linter scoped to this package anyway.
golangci-lint-v2 run ./pkg/decision/...

# Nothing outside this task's two owned paths changed.
git diff --stat HEAD~15 -- . ':!pkg/decision' ':!testdata/decision'   # expect: empty; adjust ~15 to this task's actual commit count once merged
```

**Done when:**
- [ ] `go build ./pkg/decision/...` and `go vet ./pkg/decision/...` are clean.
- [ ] `go test -race -count=1 ./pkg/decision/... -v` passes; no test is a silent no-op (every table-driven `t.Run` above has a real assertion, not just a call).
- [ ] `TestEveryReasonIsPermanent` and `TestVerdictReasonTableIsExhaustive` (Step 1) pass -- every `Reason` is typed and every non-`Upgrade` `quality.Verdict` maps to one.
- [ ] `TestTargetRuntimeMinutes` and `TestSizeRejections` (Steps 2-3) pass, including the season-pack summed-runtime case (171 minutes) and the real 50.8 MB/min Bluray-1080p floor.
- [ ] Every individual check in the §8.2 list has its own passing test: protocol (Step 4), availability (Step 4), size (Step 3), quality-in-profile + MinFormatScore (Step 5), language (Step 6), sample (Step 7), blocklist + already-imported by hash/name (Step 8), queue preference (Step 9), and the Upgradable table via `quality.Profile.UpgradeDecision` (Step 10).
- [ ] `TestEvaluateFullPipeline` (Step 11) passes against the real `testdata/decision/releases.json` fixture, proving `Evaluate` composes every check and that `Rank` (Step 13's addition to the same test) orders the two approved releases correctly.
- [ ] `TestRankPrimaryKey` and `TestRankSecondaryKeys` (Steps 12-13) pass, covering every key in the comparator chain: quality index, revision (gated by `PreferRevision`), custom-format score, preferred-protocol match, episode count, indexer priority (including the documented default of 25), indexer-flag score (freeleech/doubleupload/internal +2, halfleech +1), seeders (torrent), and both size branches (closest-to-preferred and prefer-largest).
- [ ] `TestEvaluateAdversarial` (Step 14) passes: an unparseable title produces exactly one rejection and a nil `Parsed`; `Evaluate` never returns a nil slice; `Rank` never panics on an empty or nil input.
- [ ] No test performs network I/O or depends on wall-clock time (the `seedersOrAgeScore` usenet-age test, if added beyond what's specified above, must inject a fixed `PublishedAt` rather than relying on `time.Now()` drift across the assertion).
- [ ] `pkg/decision` imports no Kubernetes client package and no `api/catalog/v1alpha1` (Disagreement 1) -- confirmed by the two `grep` checks above.
- [ ] Every new `.go` file starts with the GPL-3.0 header from `hack/boilerplate.go.txt`.
- [ ] No file outside `pkg/decision/` or `testdata/decision/` was created or modified; `git diff --stat` against this task's first commit confirms it.
- [ ] `go.mod`/`go.sum` are untouched -- this task added no dependency.
- [ ] Every commit in this task's range was made with `-c user.name=appkins -c user.email=nbatkins@gmail.com` and touches only the paths listed in its own step.
- [ ] Each of the seven numbered disagreements above is reflected in a code comment at its point of impact: `types.go`'s doc comment on `Current` (3) and on `Queued` (4) and on `Target.FreeBytes` (5); `evaluate.go`'s package-level absence of any `api/catalog/v1alpha1` import (1); `checks.go`'s doc comments on `upgradeRejection` and `queueRejection` (2); `reasons.go`'s doc comment on the `Reason` var block (6); `rank.go`'s doc comments on `indexerFlagScore` and `seedersOrAgeScore` (7) -- a reviewer should not have to trust this plan document once the code exists, the code should say it too.

---

### Task C4: catalogarr configuration controllers

**SCOPE CORRECTION — read this before anything else.** The task brief this
section was commissioned from lists five controllers including
**ImportExclusion**. That is wrong and this section does not implement it.
Amendment §A1.3's ownership table is explicit:

> `ImportExclusion` | `catalog.clustarr.io` | `importarr` (was `catalogarr`)

`pkg/k8s`'s own `ManagerImportarr` doc comment agrees ("It owns ImportList,
ImportExclusion and LibraryScan status"), and `catalogarr/run.go`'s
`setupControllers` doc comment is a third, independent confirmation: *"The
importer, importlist and importexclusion controllers are NOT here: amendment
§A1.2/§A1.3 moved them to importarr... Building them here would break the
MediaFile single-writer split."* `importarr/controller/doc.go` lists
`ImportExclusion` among what it will hold. Three independent sources in the
merged codebase agree with the amendment against the brief; per the shared
context's own rule ("Amendment... WINS where they disagree") and "generated
types/real code win over guessed pseudocode", this section builds **four**
controllers — RootFolder, QualityProfile, DelayProfile, MetadataProvider —
and leaves ImportExclusion to importarr's Phase C task. If that task's section
doesn't already cover it, flag the gap to whoever assembles the final plan;
do not build it here regardless.

A second brief inaccuracy, smaller: the brief asks RootFolder to "enforce the
built-in-protection... rules the CRD's CEL already encodes." `RootFolderSpec`
(`api/catalog/v1alpha1/rootfolder_types.go`) has no `builtIn`/protected field
at all — only `QualityProfile` does. This section treats "built-in-protection"
as a mixup with QualityProfile's `builtIn` immutability rule and, for
RootFolder, implements only what the real type has: the `path`/`kind`
immutability CEL and the `/data/media/` prefix CEL, both already enforced by
the apiserver at admission — there is nothing left for the controller to
enforce beyond not fighting them.

---

**Files:**
- Create: `catalogarr/controller/rootfolder/controller.go`
- Create: `catalogarr/controller/rootfolder/probe.go`
- Create: `catalogarr/controller/rootfolder/probe_test.go`
- Create: `catalogarr/controller/rootfolder/controller_envtest_test.go`
- Create: `catalogarr/controller/qualityprofile/controller.go`
- Create: `catalogarr/controller/qualityprofile/bootstrap.go`
- Create: `catalogarr/controller/qualityprofile/controller_envtest_test.go`
- Create: `catalogarr/controller/qualityprofile/bootstrap_envtest_test.go`
- Create: `catalogarr/controller/delayprofile/resolve.go`
- Create: `catalogarr/controller/delayprofile/resolve_test.go`
- Create: `catalogarr/controller/delayprofile/controller.go`
- Create: `catalogarr/controller/delayprofile/controller_envtest_test.go`
- Create: `catalogarr/controller/metadataprovider/prober.go`
- Create: `catalogarr/controller/metadataprovider/prober_test.go`
- Create: `catalogarr/controller/metadataprovider/registry.go`
- Create: `catalogarr/controller/metadataprovider/registry_test.go`
- Create: `catalogarr/controller/metadataprovider/controller.go`
- Create: `catalogarr/controller/metadataprovider/controller_envtest_test.go`
- Modify: none. `catalogarr/run.go` is out of this task's path ownership —
  see "For the wiring task" at the end.

**Path ownership:** `catalogarr/controller/rootfolder/`,
`catalogarr/controller/qualityprofile/`, `catalogarr/controller/delayprofile/`,
`catalogarr/controller/metadataprovider/`. No file outside these four
directories. In particular, do not create `catalogarr/controller/doc.go` — a
package-overview file at that level is a shared resource another Phase C
catalogarr task (movie/series/episode/mediafile/search) may also want, and it
is outside this task's disjoint ownership.

**Read first:**
- Spec `docs/superpowers/specs/2026-09-18-clustarr-design.md` §4.2 lines
  124–160 (RootFolder, QualityProfile, DelayProfile field lists — the prose;
  the generated types below are the actual contract), §8.2 lines ~743 (the
  "item ref → tag match → lowest order" delay resolution, in context of the
  grab flow), §8.8 line ~757 (failure handling), §9 lines 759–772 (quality
  model, the 13 built-ins, custom formats are not a CRD), §10 line 773
  (cross-service RBAC table — none of the rows in it are this task's, which
  is itself useful: nothing else watches these four kinds yet).
- Amendment `docs/superpowers/specs/2026-09-18-clustarr-design-amendment-1.md`
  §A1.3 lines 51–78 (the ownership table — see the scope correction above).
- `docs/research/k8s.md` §4 (CEL/marker reference), §5 lines 274–341
  (reconcile skeleton, `PatchStatus`/SSA, predicates, events recorder — the
  section this task's controllers follow almost verbatim, minus the finalizer
  steps, which none of these four need: none of them create or own an
  external resource that needs cleanup on delete), §8 lines 407–421 (envtest
  setup — mirrored exactly by `pkg/k8s/patch_envtest_test.go`, read that file
  directly, not just the note).
- Generated types, read in full, they are already open above in this
  research pass but re-read before writing code: `api/catalog/v1alpha1/{root
  folder,qualityprofile,delayprofile,metadataprovider}_types.go`. Also read
  `api/applyconfiguration/catalog/catalog/v1alpha1/{rootfolder,qualityprofile,
  delayprofile,metadataprovider}status.go` for the exact `With*` builders
  `PatchStatus` needs — note the doubled `catalog/catalog` path segment, a
  known controller-gen v0.22.0 quirk CLAUDE.md documents; it is correct, not a
  typo to fix.
- `pkg/k8s/patch_envtest_test.go` in full — it is the one working, committed
  example of `PatchStatus`, an envtest client, and the `KUBEBUILDER_ASSETS`
  skip guard in this codebase. Copy its `newTestClient` shape into each of
  this task's four `*_envtest_test.go` files (no shared test-helper package
  exists yet outside `pkg/k8s`; creating one is out of this task's path
  ownership, so each package gets its own small copy, same as `pkg/k8s`'s is
  the only copy that exists today).
- `pkg/k8s/conditions.go` in full (already read; `MarkTrue`/`MarkFalse`/
  `MarkReady`/`ConditionACs` are the only condition API this task uses).
- `go doc ./pkg/quality`, `go doc ./pkg/quality/catalogue`, `go doc
  ./pkg/fsops`, `go doc ./pkg/metadata`, `go doc ./pkg/metadata/clients/{tmdb,
  tvdb,musicbrainz,openlibrary,comicvine,audnexus}` — every signature this
  section's steps use was read from these with `go doc`, not guessed.
- `pkg/quality/builtins.go` (`BuiltinProfiles`'s implementation) — read the
  actual loop, not just the doc comment; the bootstrap step in this task
  re-derives the same `ProfileFS()` → `DecodeProfileSeeds` → `FromCRD` chain
  by hand because `BuiltinProfiles` returns resolved `Profile` values, not the
  `QualityProfileSpec`s a controller needs to `Create` real CRs.

**Dependencies:** none new. Everything this task uses is already in `go.mod`
(verified by `grep` against the checked-out `go.mod`, not `go list -m
-versions`, since nothing is being added):
`sigs.k8s.io/controller-runtime v0.25.1`, `k8s.io/api v0.37.0`,
`k8s.io/apimachinery v0.37.0`, `k8s.io/client-go v0.37.0`,
`github.com/stretchr/testify v1.12.1`, `golang.org/x/time v0.16.0`,
`k8s.io/utils v0.0.0-20260626114624-be93311217bd` (for `k8s.io/utils/ptr`).
`k8s.io/client-go/tools/events` (the `events.k8s.io/v1` recorder interface,
`events.EventRecorder` / `events.NewFakeRecorder`) ships inside
`k8s.io/client-go v0.37.0`, already present — confirmed present at
`$(go env GOMODCACHE)/k8s.io/client-go@v0.37.0` on this machine, no fetch
needed. Do not run `go get` or `go mod tidy`.

**Interfaces — Consumes** (exact signatures, from `go doc`):
```go
// pkg/k8s
func PatchStatus[T ApplyConfiguration](ctx context.Context, c client.Client, fm FieldManager, ac T, opts ...client.SubResourceApplyOption) (T, error)
func MarkTrue(obj client.Object, conditions *[]metav1.Condition, condType, reason, message string, args ...any) bool
func MarkFalse(obj client.Object, conditions *[]metav1.Condition, condType, reason, message string, args ...any) bool
func MarkReady(obj client.Object, conditions *[]metav1.Condition, ready bool, reason, message string, args ...any) bool
func ConditionACs(conditions []metav1.Condition) []*metav1ac.ConditionApplyConfiguration
func GenerationChanged() predicate.Predicate
const ManagerCatalogarr FieldManager = "catalogarr"

// pkg/quality
func FromCRD(p *catalogv1alpha1.QualityProfile, cat *catalogue.Catalogue) (Profile, []error)
func BuiltinProfiles(cat *catalogue.Catalogue) (map[string]Profile, []error) // read, not called directly (see bootstrap.go step)
func DecodeProfileSeeds(doc []byte) ([]ProfileSeed, error)
type ProfileSeed struct { Name string; Spec catalogv1alpha1.QualityProfileSpec }
type Profile struct { /* ...; Hash string */ }

// pkg/quality/catalogue
func LoadedCatalogue() *Catalogue
func ProfileFS() embed.FS // "data/profiles/*.json", each a JSON array of ProfileSeed
type Catalogue struct { Version string; Formats map[string]*Format; Conflicts [][2]string }

// pkg/fsops
func DiskUsage(path string) (Usage, error)          // type Usage struct{ Total, Free, Available int64 }
func EnsureFreeSpace(path string, needed int64) error // needed<=0 always passes

// pkg/metadata
func NewLimiter(l rate.Limit, burst int) *rate.Limiter
func DefaultLimits() Limits
var ErrAuth, ErrRateLimited, ErrNotFound, ErrUnsupported, ErrDecode error
type RateLimitedError struct { Provider string; RetryAfter time.Duration }
type Registry struct { Movies []MovieProvider; Series []SeriesProvider; Artists []ArtistProvider; Books []BookProvider; Audiobooks []AudiobookProvider; Comics []ComicProvider; Artwork []ArtworkProvider; Resolvers []IDResolver }

// pkg/metadata/clients/{tmdb,tvdb,musicbrainz,openlibrary,comicvine,audnexus}
func tmdb.New(apiKey string, httpClient *http.Client, baseURL string, limiter *rate.Limiter) (*tmdb.Client, error)
func tvdb.New(apiKey, pin string, httpClient *http.Client, baseURL string, limiter *rate.Limiter) *tvdb.Client
func musicbrainz.New(userAgent string, httpClient *http.Client, baseURL string, limiter *rate.Limiter) (*musicbrainz.Client, error)
func openlibrary.New(userAgent string, httpClient *http.Client, baseURL string, limiter *rate.Limiter) *openlibrary.Client
func comicvine.New(apiKey string, httpClient *http.Client, baseURL string, limiter *rate.Limiter) *comicvine.Client
func audnexus.New(httpClient *http.Client, baseURL string, limiter *rate.Limiter) *audnexus.Client
// All six default their own baseURL internally when baseURL=="" (verified in
// each package's source); tests pass an httptest.Server URL to override.

// k8s.io/client-go/tools/events
type EventRecorder interface { Eventf(regarding, related runtime.Object, eventtype, reason, action, note string, args ...interface{}) }
func NewFakeRecorder(bufferSize int) *FakeRecorder
```

**Interfaces — Produces** (what later tasks import):
```go
// catalogarr/controller/delayprofile
// Task C9 (the grab worker) imports this directly. Pure, no client.
func Resolve(ref *string, itemTags []string, profiles []catalogv1alpha1.DelayProfile) (*catalogv1alpha1.DelayProfile, error)
var ErrNoProfiles = errors.New("delayprofile: no profiles given")
var ErrNoMatch = errors.New("delayprofile: no profile matches and no catch-all is configured")

// catalogarr/controller/metadataprovider
// Task C5 (the metadata gateway, a SEPARATE process/Deployment --
// `catalogarr-metadata`, §3/§12, pinned to 1 replica -- from the `catalogarr`
// Deployment this task's controller runs in) imports this and calls it with
// its OWN client, in its OWN process. An in-memory registry cache built by
// this task's controller would not be reachable from that other process, so
// this task exports the pure construction logic, not a shared cache; Task
// C5 owns its own refresh cadence (watch, poll, whatever it needs).
func BuildRegistry(ctx context.Context, c client.Client, namespace string, httpClient *http.Client) (*metadata.Registry, error)
func NewProber(spec catalogv1alpha1.MetadataProviderSpec, secret map[string][]byte, httpClient *http.Client) (Prober, error)
type Prober interface { Probe(ctx context.Context) (ProbeResult, error) }
type ProbeResult struct { QuotaRemaining *int32 }

// catalogarr/controller/{rootfolder,qualityprofile,delayprofile,metadataprovider}
// One constructor + SetupWithManager per package, for the wiring task:
func NewReconciler(c client.Client, recorder events.EventRecorder) *Reconciler                              // rootfolder, delayprofile
func NewReconciler(c client.Client, cat *catalogue.Catalogue, recorder events.EventRecorder) *Reconciler     // qualityprofile
func NewReconciler(c client.Client, recorder events.EventRecorder, httpClient *http.Client) *Reconciler      // metadataprovider
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error                                                // all four

// catalogarr/controller/qualityprofile
// A one-shot startup bootstrap, registered via mgr.Add, not part of the
// watch-driven Reconciler.
func SeedBuiltins(ctx context.Context, c client.Client, cat *catalogue.Catalogue) error
type Bootstrap struct { Client client.Client; Catalogue *catalogue.Catalogue } // implements manager.Runnable + manager.LeaderElectionRunnable
```

---

#### RootFolder

**Design note.** `RootFolderSpec.Path` carries a CEL rule requiring the
literal prefix `/data/media/`. An envtest apiserver will accept any string
matching that prefix at admission — it never touches a real filesystem — but
this controller's own accessibility check does real `os` calls. A test cannot
and must not create a real `/data/media/...` directory on the machine running
`go test` (no such mount exists outside a real pod, and creating one would be
invasive). So the filesystem probe is injected: `Reconciler.CheckPath` is a
`func(path string) (accessible bool, freeBytes, totalBytes int64, err error)`
field, defaulting to the real, `os`/`fsops`-backed implementation. Envtest
tests create a `RootFolder` whose `spec.path` satisfies the CEL prefix
(`/data/media/movies`, never touched on disk) and override `CheckPath` with a
fake that returns canned answers; a separate, non-envtest unit test exercises
the real implementation against `t.TempDir()` (which does not need to satisfy
the CEL prefix, because no CRD is involved in that test at all).

- [ ] **Step 1: `checkPath`'s real implementation, unit-tested against a real
      temp directory.**

  Test first (`catalogarr/controller/rootfolder/probe_test.go`):
  ```go
  package rootfolder

  import (
  	"os"
  	"path/filepath"
  	"testing"
  )

  func TestCheckPathAccessibleDirectory(t *testing.T) {
  	dir := t.TempDir()
  	accessible, free, total, err := checkPath(dir)
  	if err != nil {
  		t.Fatalf("checkPath: %v", err)
  	}
  	if !accessible {
  		t.Error("a writable temp dir reported not accessible")
  	}
  	if total <= 0 || free <= 0 || free > total {
  		t.Errorf("free=%d total=%d, want 0 < free <= total", free, total)
  	}
  }

  func TestCheckPathMissingDirectory(t *testing.T) {
  	accessible, _, _, err := checkPath(filepath.Join(t.TempDir(), "does-not-exist"))
  	if err == nil {
  		t.Fatal("a missing directory returned no error")
  	}
  	if accessible {
  		t.Error("a missing directory reported accessible")
  	}
  }

  func TestCheckPathReadOnlyDirectory(t *testing.T) {
  	if os.Geteuid() == 0 {
  		t.Skip("root ignores directory permissions")
  	}
  	dir := t.TempDir()
  	if err := os.Chmod(dir, 0o555); err != nil {
  		t.Fatalf("chmod: %v", err)
  	}
  	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) }) // let t.TempDir clean up
  	accessible, _, _, err := checkPath(dir)
  	if err == nil {
  		t.Fatal("a read-only directory returned no error")
  	}
  	if accessible {
  		t.Error("a read-only directory reported accessible")
  	}
  }
  ```
  Run: `go test ./catalogarr/controller/rootfolder/... -run TestCheckPath -v`
  — expect a compile failure (`checkPath` undefined).

  Implement (`catalogarr/controller/rootfolder/probe.go`):
  ```go
  package rootfolder

  import (
  	"fmt"
  	"os"

  	"github.com/mediactl/clustarr/pkg/fsops"
  )

  // checkPath is the real, os-backed filesystem probe: the path must exist,
  // be a directory, and accept a temp-file write (existence alone is not
  // enough -- a directory owned by another uid with no write bit exists and
  // is not accessible). It never creates or removes anything except its own
  // probe file.
  func checkPath(path string) (accessible bool, freeBytes, totalBytes int64, err error) {
  	info, err := os.Stat(path)
  	if err != nil {
  		return false, 0, 0, fmt.Errorf("stat %s: %w", path, err)
  	}
  	if !info.IsDir() {
  		return false, 0, 0, fmt.Errorf("%s is not a directory", path)
  	}

  	probe, err := os.CreateTemp(path, ".clustarr-rootfolder-check-*")
  	if err != nil {
  		return false, 0, 0, fmt.Errorf("%s is not writable: %w", path, err)
  	}
  	name := probe.Name()
  	_ = probe.Close()
  	if rmErr := os.Remove(name); rmErr != nil {
  		return false, 0, 0, fmt.Errorf("remove probe file: %w", rmErr)
  	}

  	usage, err := fsops.DiskUsage(path)
  	if err != nil {
  		return true, 0, 0, fmt.Errorf("disk usage %s: %w", path, err)
  	}
  	return true, usage.Free, usage.Total, nil
  }
  ```
  Run: `go test ./catalogarr/controller/rootfolder/... -run TestCheckPath -v`
  — expect PASS on all three.

  Commit (path-scoped):
  ```bash
  git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m \
    "feat(catalogarr): rootfolder filesystem probe" -- \
    catalogarr/controller/rootfolder/probe.go catalogarr/controller/rootfolder/probe_test.go
  ```

- [ ] **Step 2: the Reconciler, envtest-verified end to end.**

  Test first (`catalogarr/controller/rootfolder/controller_envtest_test.go`):
  ```go
  package rootfolder_test

  import (
  	"context"
  	"os"
  	"testing"

  	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
  	"k8s.io/apimachinery/pkg/types"
  	"k8s.io/client-go/tools/events"
  	"sigs.k8s.io/controller-runtime/pkg/client"
  	"sigs.k8s.io/controller-runtime/pkg/envtest"
  	"sigs.k8s.io/controller-runtime/pkg/reconcile"

  	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
  	"github.com/mediactl/clustarr/catalogarr/controller/rootfolder"
  	"github.com/mediactl/clustarr/pkg/k8s"
  )

  func newTestClient(t *testing.T) client.Client {
  	t.Helper()
  	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
  		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
  	}
  	env := &envtest.Environment{
  		CRDDirectoryPaths:     []string{"../../../config/crd/bases"},
  		ErrorIfCRDPathMissing: true,
  	}
  	cfg, err := env.Start()
  	if err != nil {
  		t.Fatalf("start envtest: %v", err)
  	}
  	t.Cleanup(func() {
  		if err := env.Stop(); err != nil {
  			t.Errorf("stop envtest: %v", err)
  		}
  	})
  	c, err := client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
  	if err != nil {
  		t.Fatalf("build client: %v", err)
  	}
  	return c
  }

  func TestReconcileAccessiblePathIsReady(t *testing.T) {
  	ctx := context.Background()
  	c := newTestClient(t)
  	rf := &catalogv1alpha1.RootFolder{
  		ObjectMeta: metav1.ObjectMeta{Name: "movies", Namespace: "default"},
  		Spec: catalogv1alpha1.RootFolderSpec{
  			Path: "/data/media/movies",
  			Kind: catalogv1alpha1.RootFolderKindMovie,
  		},
  	}
  	if err := c.Create(ctx, rf); err != nil {
  		t.Fatalf("create RootFolder: %v", err)
  	}

  	r := rootfolder.NewReconciler(c, events.NewFakeRecorder(10))
  	r.CheckPath = func(path string) (bool, int64, int64, error) {
  		if path != "/data/media/movies" {
  			t.Errorf("checkPath called with %q, want /data/media/movies", path)
  		}
  		return true, 500_000_000_000, 1_000_000_000_000, nil
  	}

  	if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "movies"}}); err != nil {
  		t.Fatalf("Reconcile: %v", err)
  	}

  	var got catalogv1alpha1.RootFolder
  	if err := c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "movies"}, &got); err != nil {
  		t.Fatalf("get: %v", err)
  	}
  	if !got.Status.Accessible {
  		t.Error("status.accessible = false, want true")
  	}
  	if got.Status.FreeBytes != 500_000_000_000 || got.Status.TotalBytes != 1_000_000_000_000 {
  		t.Errorf("freeBytes=%d totalBytes=%d, want 500e9/1e12", got.Status.FreeBytes, got.Status.TotalBytes)
  	}
  	if !k8s.IsConditionTrue(got.Status.Conditions, catalogv1alpha1.RootFolderConditionReady) {
  		t.Error("Ready is not True")
  	}
  	if !k8s.IsConditionTrue(got.Status.Conditions, catalogv1alpha1.RootFolderConditionDiskSpaceOK) {
  		t.Error("DiskSpaceOK is not True")
  	}
  }

  func TestReconcileMissingPathIsNotReady(t *testing.T) {
  	ctx := context.Background()
  	c := newTestClient(t)
  	rf := &catalogv1alpha1.RootFolder{
  		ObjectMeta: metav1.ObjectMeta{Name: "tv", Namespace: "default"},
  		Spec: catalogv1alpha1.RootFolderSpec{
  			Path: "/data/media/tv",
  			Kind: catalogv1alpha1.RootFolderKindSeries,
  		},
  	}
  	if err := c.Create(ctx, rf); err != nil {
  		t.Fatalf("create RootFolder: %v", err)
  	}

  	r := rootfolder.NewReconciler(c, events.NewFakeRecorder(10))
  	r.CheckPath = func(string) (bool, int64, int64, error) {
  		return false, 0, 0, os.ErrNotExist
  	}
  	if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "tv"}}); err != nil {
  		t.Fatalf("Reconcile: %v", err)
  	}

  	var got catalogv1alpha1.RootFolder
  	if err := c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "tv"}, &got); err != nil {
  		t.Fatalf("get: %v", err)
  	}
  	if got.Status.Accessible {
  		t.Error("status.accessible = true, want false")
  	}
  	if k8s.IsConditionTrue(got.Status.Conditions, catalogv1alpha1.RootFolderConditionReady) {
  		t.Error("Ready is True for a missing path")
  	}
  	cond := k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.RootFolderConditionReady)
  	if cond == nil || cond.Reason != "PathNotFound" {
  		t.Errorf("Ready reason = %+v, want PathNotFound", cond)
  	}
  }

  func TestReconcileBelowMinFreeBytesFailsDiskSpaceOK(t *testing.T) {
  	ctx := context.Background()
  	c := newTestClient(t)
  	rf := &catalogv1alpha1.RootFolder{
  		ObjectMeta: metav1.ObjectMeta{Name: "music", Namespace: "default"},
  		Spec: catalogv1alpha1.RootFolderSpec{
  			Path:         "/data/media/music",
  			Kind:         catalogv1alpha1.RootFolderKindMusic,
  			MinFreeBytes: 100_000_000_000,
  		},
  	}
  	if err := c.Create(ctx, rf); err != nil {
  		t.Fatalf("create RootFolder: %v", err)
  	}

  	r := rootfolder.NewReconciler(c, events.NewFakeRecorder(10))
  	r.CheckPath = func(string) (bool, int64, int64, error) {
  		return true, 10_000_000_000, 1_000_000_000_000, nil // 10GB free, need 100GB
  	}
  	if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "music"}}); err != nil {
  		t.Fatalf("Reconcile: %v", err)
  	}

  	var got catalogv1alpha1.RootFolder
  	if err := c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "music"}, &got); err != nil {
  		t.Fatalf("get: %v", err)
  	}
  	if k8s.IsConditionTrue(got.Status.Conditions, catalogv1alpha1.RootFolderConditionDiskSpaceOK) {
  		t.Error("DiskSpaceOK is True with free below minFreeBytes")
  	}
  	// Accessible is still true -- DiskSpaceOK is a separate condition from
  	// Ready per the CRD's own condition pair; Ready follows DiskSpaceOK too
  	// (an inaccessible-but-technically-writable folder that is about to fill
  	// up is not "ready" either).
  	if k8s.IsConditionTrue(got.Status.Conditions, catalogv1alpha1.RootFolderConditionReady) {
  		t.Error("Ready is True while DiskSpaceOK is False")
  	}
  }
  ```
  Run: `KUBEBUILDER_ASSETS="$(setup-envtest use 1.37.0 -p path)" go test
  ./catalogarr/controller/rootfolder/... -run TestReconcile -v` — expect a
  compile failure (`rootfolder.NewReconciler` undefined).

  Implement (`catalogarr/controller/rootfolder/controller.go`):
  ```go
  package rootfolder

  import (
  	"context"
  	"time"

  	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
  	"k8s.io/apimachinery/pkg/types"
  	"k8s.io/client-go/tools/events"
  	"sigs.k8s.io/controller-runtime/pkg/builder"
  	ctrl "sigs.k8s.io/controller-runtime"
  	"sigs.k8s.io/controller-runtime/pkg/client"
  	"sigs.k8s.io/controller-runtime/pkg/controller"
  	"sigs.k8s.io/controller-runtime/pkg/reconcile"

  	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
  	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
  	"github.com/mediactl/clustarr/pkg/fsops"
  	"github.com/mediactl/clustarr/pkg/k8s"
  	"github.com/mediactl/clustarr/pkg/obs/logging"
  	"github.com/mediactl/clustarr/pkg/obs/tracing"
  )

  // recheckInterval is how often an already-Ready RootFolder is re-probed for
  // free space between spec changes. Not given by the spec; invented here as
  // a cheap-enough, frequent-enough default -- one statfs call every 5
  // minutes catches a filling disk long before it is disastrous. Revisit if
  // a later task adds a cheaper signal (e.g. a kubelet volume metric).
  const recheckInterval = 5 * time.Minute

  // Reason tokens local to RootFolder; see pkg/k8s.Reason* for the shared set
  // this reuses (ReasonReconciled, ReasonReconcileError).
  const (
  	ReasonPathNotFound  = "PathNotFound"
  	ReasonNotWritable   = "NotWritable"
  	ReasonBelowMinFree  = "BelowMinFreeBytes"
  )

  // Reconciler resolves a RootFolder's accessibility and free space.
  type Reconciler struct {
  	Client   client.Client
  	Recorder events.EventRecorder

  	// CheckPath is the filesystem probe. Production uses checkPath (probe.go);
  	// tests inject a fake so they never need a real /data/media/... path.
  	CheckPath func(path string) (accessible bool, freeBytes, totalBytes int64, err error)
  }

  // NewReconciler builds a Reconciler with the real filesystem probe.
  func NewReconciler(c client.Client, recorder events.EventRecorder) *Reconciler {
  	return &Reconciler{Client: c, Recorder: recorder, CheckPath: checkPath}
  }

  func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (ctrl.Result, error) {
  	ctx, span := tracing.Start(ctx, "rootfolder.Reconcile")
  	defer span.End()
  	log := logging.FromContext(ctx).With("rootfolder", req.NamespacedName)

  	var rf catalogv1alpha1.RootFolder
  	if err := r.Client.Get(ctx, req.NamespacedName, &rf); err != nil {
  		return ctrl.Result{}, client.IgnoreNotFound(err)
  	}

  	conditions := append([]metav1.Condition(nil), rf.Status.Conditions...)
  	accessible, free, total, err := r.CheckPath(rf.Spec.Path)

  	var reason, message string
  	switch {
  	case err != nil:
  		reason, message = ReasonPathNotFound, err.Error()
  		if accessible {
  			// checkPath found the path but a later step (DiskUsage) failed;
  			// keep the specific reason but do not claim NotWritable.
  			reason = ReasonNotWritable
  		}
  	case !accessible:
  		reason, message = ReasonNotWritable, "path exists but is not writable"
  	}

  	k8s.MarkFalse(&rf, &conditions, catalogv1alpha1.RootFolderConditionReady, k8s.ReasonReconciling, "not yet evaluated")
  	diskOK := accessible && err == nil && fsops.EnsureFreeSpace(rf.Spec.Path, rf.Spec.MinFreeBytes) == nil
  	if accessible && err == nil {
  		if diskOK {
  			k8s.MarkTrue(&rf, &conditions, catalogv1alpha1.RootFolderConditionDiskSpaceOK, k8s.ReasonReconciled, "free space above minFreeBytes")
  		} else {
  			k8s.MarkFalse(&rf, &conditions, catalogv1alpha1.RootFolderConditionDiskSpaceOK, ReasonBelowMinFree, "free=%d minFreeBytes=%d", free, rf.Spec.MinFreeBytes)
  		}
  	} else {
  		k8s.MarkFalse(&rf, &conditions, catalogv1alpha1.RootFolderConditionDiskSpaceOK, reason, "%s", message)
  	}

  	ready := accessible && err == nil && diskOK
  	if ready {
  		k8s.MarkReady(&rf, &conditions, true, k8s.ReasonReconciled, "path is accessible with sufficient free space")
  	} else {
  		k8s.MarkReady(&rf, &conditions, false, reason, "%s", message)
  		if r.Recorder != nil {
  			r.Recorder.Eventf(&rf, nil, "Warning", reason, "Reconcile", message)
  		}
  	}

  	ac := catalogac.RootFolder(rf.Name, rf.Namespace).WithStatus(
  		catalogac.RootFolderStatus().
  			WithObservedGeneration(rf.Generation).
  			WithAccessible(accessible && err == nil).
  			WithFreeBytes(free).
  			WithTotalBytes(total).
  			WithConditions(k8s.ConditionACs(conditions)...),
  	)
  	if _, err := k8s.PatchStatus(ctx, r.Client, k8s.ManagerCatalogarr, ac); err != nil {
  		log.Error("patch status", "error", err)
  		return ctrl.Result{}, err
  	}

  	return ctrl.Result{RequeueAfter: recheckInterval}, nil
  }

  // +kubebuilder:rbac:groups=catalog.clustarr.io,resources=rootfolders,verbs=get;list;watch
  // +kubebuilder:rbac:groups=catalog.clustarr.io,resources=rootfolders/status,verbs=get;update;patch
  // +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch
  func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
  	return ctrl.NewControllerManagedBy(mgr).
  		Named("rootfolder").
  		For(&catalogv1alpha1.RootFolder{}, builder.WithPredicates(k8s.GenerationChanged())).
  		WithOptions(controller.Options{ReconciliationTimeout: 5 * time.Minute}).
  		Complete(r)
  }

  var _ = types.NamespacedName{} // silence unused import if trimmed during edits; remove once req use is added above
  ```
  (Drop the trailing `var _` line — it is a placeholder against a copy/paste
  edit removing the only other use of `types`; `req.NamespacedName` in
  `Reconcile` already uses it, so the real file does not need it. Do not
  ship the placeholder line.)

  Run: `KUBEBUILDER_ASSETS="$(setup-envtest use 1.37.0 -p path)" go test
  ./catalogarr/controller/rootfolder/... -v` — expect all tests PASS, and the
  three `TestReconcile*` tests to take on the order of a second each (real
  apiserver round trips), never single-digit milliseconds — a millisecond
  runtime means `KUBEBUILDER_ASSETS` was unset and the test skipped silently.

  Commit:
  ```bash
  git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m \
    "feat(catalogarr): rootfolder controller" -- \
    catalogarr/controller/rootfolder/controller.go catalogarr/controller/rootfolder/controller_envtest_test.go
  ```

---

#### QualityProfile

**Design note.** `QualityProfileSpec` has a type-level CEL rule
(`!oldSelf.builtIn || self == oldSelf`) making a `builtIn: true` object's spec
fully immutable once created. Spec §9 says built-ins are "re-seeded on
version bump" — with that CEL rule, "re-seed" cannot mean "update in place"
(the apiserver would reject it); it can only mean delete-then-recreate. The
bootstrap therefore does not use `k8s.Apply` (SSA converges a *steady-state*
field set; there is no steady state to converge to once the object is
immutable) — it uses plain `client.Create`/`client.Delete`, detecting drift
via an annotation (`catalog.clustarr.io/builtin-seed-hash`, metadata, not
spec, so the CEL rule does not apply to it) set to `quality.Profile.Hash`,
which `FromCRD` already computes deterministically from the resolved spec
*and* the loaded `catalogue.Catalogue` — exactly the two things a version
bump can change, so it is the one comparison this needs.

- [ ] **Step 1: the reconciler resolves a profile and reports `Invalid` for
      what CEL cannot check (an unknown quality name inside a tier).**

  Test first (`catalogarr/controller/qualityprofile/controller_envtest_test.go`):
  ```go
  package qualityprofile_test

  import (
  	"context"
  	"os"
  	"testing"

  	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
  	"k8s.io/apimachinery/pkg/types"
  	"k8s.io/client-go/tools/events"
  	"sigs.k8s.io/controller-runtime/pkg/client"
  	"sigs.k8s.io/controller-runtime/pkg/envtest"
  	"sigs.k8s.io/controller-runtime/pkg/reconcile"

  	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
  	"github.com/mediactl/clustarr/catalogarr/controller/qualityprofile"
  	"github.com/mediactl/clustarr/pkg/k8s"
  	"github.com/mediactl/clustarr/pkg/quality/catalogue"
  )

  func newTestClient(t *testing.T) client.Client {
  	t.Helper()
  	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
  		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
  	}
  	env := &envtest.Environment{CRDDirectoryPaths: []string{"../../../config/crd/bases"}, ErrorIfCRDPathMissing: true}
  	cfg, err := env.Start()
  	if err != nil {
  		t.Fatalf("start envtest: %v", err)
  	}
  	t.Cleanup(func() {
  		if err := env.Stop(); err != nil {
  			t.Errorf("stop envtest: %v", err)
  		}
  	})
  	c, err := client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
  	if err != nil {
  		t.Fatalf("build client: %v", err)
  	}
  	return c
  }

  // Real tier/quality names, copied verbatim from
  // pkg/quality/catalogue/data/profiles/hd-bluray-web.json (the "hd-bluray-web"
  // built-in) rather than invented, so a resolution failure in this test can
  // only be the deliberately-broken tier below, not a typo in a made-up name.
  func validVideoSpec(cutoff string) catalogv1alpha1.QualityProfileSpec {
  	return catalogv1alpha1.QualityProfileSpec{
  		MediaKind: catalogv1alpha1.ProfileMediaKindVideo,
  		Tiers: []catalogv1alpha1.Tier{
  			{Name: "Bluray-1080p", Qualities: []string{"Bluray-1080p"}},
  			{Name: "WEB 1080p", Qualities: []string{"WEBRip-1080p", "WEBDL-1080p"}},
  			{Name: "Bluray-720p", Qualities: []string{"Bluray-720p"}},
  		},
  		Cutoff: cutoff,
  	}
  }

  func TestReconcileValidProfileIsReady(t *testing.T) {
  	ctx := context.Background()
  	c := newTestClient(t)
  	qp := &catalogv1alpha1.QualityProfile{
  		ObjectMeta: metav1.ObjectMeta{Name: "test-hd"},
  		Spec:       validVideoSpec("Bluray-1080p"),
  	}
  	if err := c.Create(ctx, qp); err != nil {
  		t.Fatalf("create QualityProfile: %v", err)
  	}

  	r := qualityprofile.NewReconciler(c, catalogue.LoadedCatalogue(), events.NewFakeRecorder(10))
  	if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: "test-hd"}}); err != nil {
  		t.Fatalf("Reconcile: %v", err)
  	}

  	var got catalogv1alpha1.QualityProfile
  	if err := c.Get(ctx, types.NamespacedName{Name: "test-hd"}, &got); err != nil {
  		t.Fatalf("get: %v", err)
  	}
  	if !k8s.IsConditionTrue(got.Status.Conditions, catalogv1alpha1.QualityProfileConditionReady) {
  		t.Errorf("Ready is not True: %+v", got.Status.Conditions)
  	}
  	if k8s.IsConditionTrue(got.Status.Conditions, catalogv1alpha1.QualityProfileConditionInvalid) {
  		t.Error("Invalid is True for a valid profile")
  	}
  	if got.Status.Hash == "" {
  		t.Error("status.hash is empty")
  	}
  	if len(got.Status.QualityOrder) != 3 {
  		t.Errorf("qualityOrder has %d entries, want 3", len(got.Status.QualityOrder))
  	}
  	if got.Status.CatalogueVersion != catalogue.LoadedCatalogue().Version {
  		t.Errorf("catalogueVersion = %q, want %q", got.Status.CatalogueVersion, catalogue.LoadedCatalogue().Version)
  	}
  }

  func TestReconcileUnknownQualityNameIsInvalid(t *testing.T) {
  	ctx := context.Background()
  	c := newTestClient(t)
  	spec := validVideoSpec("Bogus")
  	spec.Tiers = append(spec.Tiers, catalogv1alpha1.Tier{Name: "Bogus", Qualities: []string{"NotARealQuality"}})
  	qp := &catalogv1alpha1.QualityProfile{ObjectMeta: metav1.ObjectMeta{Name: "test-broken"}, Spec: spec}
  	if err := c.Create(ctx, qp); err != nil {
  		t.Fatalf("create QualityProfile: %v", err)
  	}

  	r := qualityprofile.NewReconciler(c, catalogue.LoadedCatalogue(), events.NewFakeRecorder(10))
  	if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: "test-broken"}}); err != nil {
  		t.Fatalf("Reconcile: %v", err)
  	}

  	var got catalogv1alpha1.QualityProfile
  	if err := c.Get(ctx, types.NamespacedName{Name: "test-broken"}, &got); err != nil {
  		t.Fatalf("get: %v", err)
  	}
  	if !k8s.IsConditionTrue(got.Status.Conditions, catalogv1alpha1.QualityProfileConditionInvalid) {
  		t.Error("Invalid is not True for a profile with an unknown quality name")
  	}
  	if k8s.IsConditionTrue(got.Status.Conditions, catalogv1alpha1.QualityProfileConditionReady) {
  		t.Error("Ready is True for an invalid profile")
  	}
  }
  ```
  Run: `KUBEBUILDER_ASSETS="$(setup-envtest use 1.37.0 -p path)" go test
  ./catalogarr/controller/qualityprofile/... -run TestReconcile -v` — expect
  a compile failure (`qualityprofile.NewReconciler` undefined).

  Implement (`catalogarr/controller/qualityprofile/controller.go`):
  ```go
  package qualityprofile

  import (
  	"context"
  	"errors"
  	"strings"
  	"time"

  	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
  	"k8s.io/client-go/tools/events"
  	ctrl "sigs.k8s.io/controller-runtime"
  	"sigs.k8s.io/controller-runtime/pkg/builder"
  	"sigs.k8s.io/controller-runtime/pkg/client"
  	"sigs.k8s.io/controller-runtime/pkg/controller"
  	"sigs.k8s.io/controller-runtime/pkg/reconcile"

  	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
  	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
  	"github.com/mediactl/clustarr/pkg/k8s"
  	"github.com/mediactl/clustarr/pkg/obs/logging"
  	"github.com/mediactl/clustarr/pkg/obs/tracing"
  	"github.com/mediactl/clustarr/pkg/quality"
  	"github.com/mediactl/clustarr/pkg/quality/catalogue"
  )

  const ReasonResolutionFailed = "ResolutionFailed"

  type Reconciler struct {
  	Client    client.Client
  	Catalogue *catalogue.Catalogue
  	Recorder  events.EventRecorder
  }

  func NewReconciler(c client.Client, cat *catalogue.Catalogue, recorder events.EventRecorder) *Reconciler {
  	return &Reconciler{Client: c, Catalogue: cat, Recorder: recorder}
  }

  func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (ctrl.Result, error) {
  	ctx, span := tracing.Start(ctx, "qualityprofile.Reconcile")
  	defer span.End()
  	log := logging.FromContext(ctx).With("qualityprofile", req.Name)

  	var qp catalogv1alpha1.QualityProfile
  	if err := r.Client.Get(ctx, req.NamespacedName, &qp); err != nil {
  		return ctrl.Result{}, client.IgnoreNotFound(err)
  	}

  	profile, errs := quality.FromCRD(&qp, r.Catalogue)
  	conditions := append([]metav1.Condition(nil), qp.Status.Conditions...)

  	if len(errs) > 0 {
  		msgs := make([]string, len(errs))
  		for i, e := range errs {
  			msgs[i] = e.Error()
  		}
  		message := strings.Join(msgs, "; ")
  		k8s.MarkTrue(&qp, &conditions, catalogv1alpha1.QualityProfileConditionInvalid, ReasonResolutionFailed, "%s", message)
  		k8s.MarkReady(&qp, &conditions, false, ReasonResolutionFailed, "%s", message)
  		if r.Recorder != nil {
  			r.Recorder.Eventf(&qp, nil, "Warning", ReasonResolutionFailed, "Reconcile", message)
  		}
  	} else {
  		k8s.MarkFalse(&qp, &conditions, catalogv1alpha1.QualityProfileConditionInvalid, k8s.ReasonReconciled, "resolved cleanly")
  		k8s.MarkReady(&qp, &conditions, true, k8s.ReasonReconciled, "resolved against catalogue %s", r.Catalogue.Version)
  	}

  	order := make([]string, 0, len(profile.Tiers)*1)
  	for _, tier := range profile.Tiers {
  		for _, def := range tier {
  			order = append(order, def.Name)
  		}
  	}

  	ac := catalogac.QualityProfile(qp.Name, "").WithStatus(
  		catalogac.QualityProfileStatus().
  			WithObservedGeneration(qp.Generation).
  			WithCatalogueVersion(r.Catalogue.Version).
  			WithResolvedFormats(int32(len(profile.Scores))).
  			WithQualityOrder(order...).
  			WithHash(profile.Hash).
  			WithConditions(k8s.ConditionACs(conditions)...),
  	)
  	if _, err := k8s.PatchStatus(ctx, r.Client, k8s.ManagerCatalogarr, ac); err != nil {
  		log.Error("patch status", "error", err)
  		return ctrl.Result{}, err
  	}
  	return ctrl.Result{}, nil
  }

  // +kubebuilder:rbac:groups=catalog.clustarr.io,resources=qualityprofiles,verbs=get;list;watch;create;delete
  // +kubebuilder:rbac:groups=catalog.clustarr.io,resources=qualityprofiles/status,verbs=get;update;patch
  // +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch
  func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
  	return ctrl.NewControllerManagedBy(mgr).
  		Named("qualityprofile").
  		For(&catalogv1alpha1.QualityProfile{}, builder.WithPredicates(k8s.GenerationChanged())).
  		WithOptions(controller.Options{ReconciliationTimeout: 5 * time.Minute}).
  		Complete(r)
  }

  var errNotReached = errors.New("unreachable") // remove; placeholder only if an unused-import linter complains during drafting
  ```
  (Drop the `errNotReached` line — `errors` is not otherwise imported by this
  file; if a draft doesn't need it, remove the import too. It exists in this
  plan only as a reminder to double check imports once written for real, not
  to ship.)

  Run the envtest command from Step 1 again — expect both tests PASS.

  Commit:
  ```bash
  git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m \
    "feat(catalogarr): qualityprofile controller" -- \
    catalogarr/controller/qualityprofile/controller.go catalogarr/controller/qualityprofile/controller_envtest_test.go
  ```

- [ ] **Step 2: the built-in bootstrap — creates the 13 profiles once, is a
      no-op when nothing changed, and deletes+recreates a profile whose seed
      content drifted.**

  Test first (`catalogarr/controller/qualityprofile/bootstrap_envtest_test.go`,
  reuses `newTestClient` from Step 1's file, same package):
  ```go
  package qualityprofile_test

  import (
  	"context"
  	"testing"

  	"k8s.io/apimachinery/pkg/types"

  	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
  	"github.com/mediactl/clustarr/catalogarr/controller/qualityprofile"
  	"github.com/mediactl/clustarr/pkg/quality/catalogue"
  )

  func TestSeedBuiltinsCreatesAllThirteen(t *testing.T) {
  	ctx := context.Background()
  	c := newTestClient(t)
  	cat := catalogue.LoadedCatalogue()

  	if err := qualityprofile.SeedBuiltins(ctx, c, cat); err != nil {
  		t.Fatalf("SeedBuiltins: %v", err)
  	}

  	var list catalogv1alpha1.QualityProfileList
  	if err := c.List(ctx, &list); err != nil {
  		t.Fatalf("list: %v", err)
  	}
  	if len(list.Items) != 13 {
  		t.Fatalf("got %d QualityProfiles, want 13 (pkg/quality.BuiltinProfiles's own count)", len(list.Items))
  	}
  	for _, p := range list.Items {
  		if !p.Spec.BuiltIn {
  			t.Errorf("%s: spec.builtIn = false, want true", p.Name)
  		}
  	}

  	var hd catalogv1alpha1.QualityProfile
  	if err := c.Get(ctx, types.NamespacedName{Name: "hd-bluray-web"}, &hd); err != nil {
  		t.Fatalf("get hd-bluray-web: %v", err)
  	}
  	if hd.Spec.Cutoff != "Bluray-1080p" {
  		t.Errorf("hd-bluray-web cutoff = %q, want Bluray-1080p", hd.Spec.Cutoff)
  	}
  }

  func TestSeedBuiltinsIsIdempotent(t *testing.T) {
  	ctx := context.Background()
  	c := newTestClient(t)
  	cat := catalogue.LoadedCatalogue()

  	if err := qualityprofile.SeedBuiltins(ctx, c, cat); err != nil {
  		t.Fatalf("first SeedBuiltins: %v", err)
  	}
  	var before catalogv1alpha1.QualityProfile
  	if err := c.Get(ctx, types.NamespacedName{Name: "hd-bluray-web"}, &before); err != nil {
  		t.Fatalf("get: %v", err)
  	}

  	if err := qualityprofile.SeedBuiltins(ctx, c, cat); err != nil {
  		t.Fatalf("second SeedBuiltins: %v", err)
  	}
  	var after catalogv1alpha1.QualityProfile
  	if err := c.Get(ctx, types.NamespacedName{Name: "hd-bluray-web"}, &after); err != nil {
  		t.Fatalf("get: %v", err)
  	}
  	if before.UID != after.UID {
  		t.Error("a no-op re-seed deleted and recreated the object (UID changed)")
  	}
  	if before.ResourceVersion != after.ResourceVersion {
  		t.Error("a no-op re-seed still wrote the object (resourceVersion changed)")
  	}
  }

  func TestSeedBuiltinsRecreatesOnDrift(t *testing.T) {
  	ctx := context.Background()
  	c := newTestClient(t)
  	cat := catalogue.LoadedCatalogue()

  	if err := qualityprofile.SeedBuiltins(ctx, c, cat); err != nil {
  		t.Fatalf("first SeedBuiltins: %v", err)
  	}
  	var before catalogv1alpha1.QualityProfile
  	if err := c.Get(ctx, types.NamespacedName{Name: "hd-bluray-web"}, &before); err != nil {
  		t.Fatalf("get: %v", err)
  	}

  	// Simulate a catalogue/seed content change the only way this test can:
  	// corrupt the drift-detection annotation directly, the same way a real
  	// version bump would make the freshly computed hash disagree with what
  	// is stored.
  	before.Annotations["catalog.clustarr.io/builtin-seed-hash"] = "stale-hash-simulating-a-version-bump"
  	if err := c.Update(ctx, &before); err != nil {
  		t.Fatalf("corrupt annotation: %v", err)
  	}

  	if err := qualityprofile.SeedBuiltins(ctx, c, cat); err != nil {
  		t.Fatalf("second SeedBuiltins: %v", err)
  	}
  	var after catalogv1alpha1.QualityProfile
  	if err := c.Get(ctx, types.NamespacedName{Name: "hd-bluray-web"}, &after); err != nil {
  		t.Fatalf("get after re-seed: %v", err)
  	}
  	if before.UID == after.UID {
  		t.Error("a drifted profile was not recreated (UID unchanged)")
  	}
  	if after.Spec.Cutoff != "Bluray-1080p" {
  		t.Errorf("recreated profile cutoff = %q, want Bluray-1080p", after.Spec.Cutoff)
  	}
  }
  ```
  Run: `KUBEBUILDER_ASSETS="$(setup-envtest use 1.37.0 -p path)" go test
  ./catalogarr/controller/qualityprofile/... -run TestSeedBuiltins -v` —
  expect a compile failure (`qualityprofile.SeedBuiltins` undefined).

  Implement (`catalogarr/controller/qualityprofile/bootstrap.go`):
  ```go
  package qualityprofile

  import (
  	"context"
  	"fmt"

  	apierrors "k8s.io/apimachinery/pkg/api/errors"
  	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
  	"k8s.io/apimachinery/pkg/types"
  	"sigs.k8s.io/controller-runtime/pkg/client"

  	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
  	"github.com/mediactl/clustarr/pkg/obs/logging"
  	"github.com/mediactl/clustarr/pkg/quality"
  	"github.com/mediactl/clustarr/pkg/quality/catalogue"
  )

  // seedHashAnnotation records the resolved Profile.Hash a built-in was last
  // created with, so a re-run can tell "nothing changed" from "the catalogue
  // or the seed JSON moved on, re-seed this one" without ever comparing a
  // builtIn:true spec for update (the CEL rule forbids that outright).
  const seedHashAnnotation = "catalog.clustarr.io/builtin-seed-hash"

  // SeedBuiltins ensures every embedded profile seed
  // (pkg/quality/catalogue.ProfileFS, "data/profiles/*.json") exists as a
  // builtIn:true QualityProfile, creating it if absent and replacing it if
  // its resolved content drifted from what is stored. It is idempotent and
  // side-effect free when nothing changed.
  func SeedBuiltins(ctx context.Context, c client.Client, cat *catalogue.Catalogue) error {
  	entries, err := catalogue.ProfileFS().ReadDir("data/profiles")
  	if err != nil {
  		return fmt.Errorf("qualityprofile: read profile seeds: %w", err)
  	}

  	for _, e := range entries {
  		doc, err := catalogue.ProfileFS().ReadFile("data/profiles/" + e.Name())
  		if err != nil {
  			return fmt.Errorf("qualityprofile: read %s: %w", e.Name(), err)
  		}
  		seeds, err := quality.DecodeProfileSeeds(doc)
  		if err != nil {
  			return fmt.Errorf("qualityprofile: decode %s: %w", e.Name(), err)
  		}
  		for _, seed := range seeds {
  			if err := seedOne(ctx, c, cat, seed); err != nil {
  				return err
  			}
  		}
  	}
  	return nil
  }

  func seedOne(ctx context.Context, c client.Client, cat *catalogue.Catalogue, seed quality.ProfileSeed) error {
  	log := logging.FromContext(ctx).With("builtin", seed.Name)

  	spec := seed.Spec
  	spec.BuiltIn = true // the seed JSON never sets this; it is a property of
  	// how the object was created, not of the resolved profile content.

  	want := &catalogv1alpha1.QualityProfile{ObjectMeta: metav1.ObjectMeta{Name: seed.Name}, Spec: spec}
  	profile, errs := quality.FromCRD(want, cat)
  	if len(errs) > 0 {
  		return fmt.Errorf("qualityprofile: built-in %s does not resolve: %v", seed.Name, errs)
  	}
  	want.Annotations = map[string]string{seedHashAnnotation: profile.Hash}

  	var existing catalogv1alpha1.QualityProfile
  	err := c.Get(ctx, types.NamespacedName{Name: seed.Name}, &existing)
  	switch {
  	case apierrors.IsNotFound(err):
  		log.Info("creating built-in QualityProfile")
  		return client.IgnoreAlreadyExists(c.Create(ctx, want))
  	case err != nil:
  		return fmt.Errorf("qualityprofile: get %s: %w", seed.Name, err)
  	case existing.Annotations[seedHashAnnotation] == profile.Hash:
  		return nil // up to date, nothing to do
  	default:
  		log.Info("built-in content drifted, recreating", "oldHash", existing.Annotations[seedHashAnnotation], "newHash", profile.Hash)
  		if err := c.Delete(ctx, &existing); err != nil && !apierrors.IsNotFound(err) {
  			return fmt.Errorf("qualityprofile: delete stale %s: %w", seed.Name, err)
  		}
  		return client.IgnoreAlreadyExists(c.Create(ctx, want))
  	}
  }

  // Bootstrap is a one-shot manager.Runnable that calls SeedBuiltins once the
  // leader is elected. It is added via mgr.Add in the wiring task, not
  // started as part of the watch-driven Reconciler.
  type Bootstrap struct {
  	Client    client.Client
  	Catalogue *catalogue.Catalogue
  }

  func (b *Bootstrap) Start(ctx context.Context) error {
  	return SeedBuiltins(ctx, b.Client, b.Catalogue)
  }

  func (b *Bootstrap) NeedLeaderElection() bool { return true }
  ```
  Run the envtest command above again — expect all three `TestSeedBuiltins*`
  tests PASS.

  Commit:
  ```bash
  git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m \
    "feat(catalogarr): seed the 13 built-in QualityProfiles at startup" -- \
    catalogarr/controller/qualityprofile/bootstrap.go catalogarr/controller/qualityprofile/bootstrap_envtest_test.go
  ```

---

#### DelayProfile

- [ ] **Step 1: `Resolve` — pure, no client, table-driven, unit-tested
      without envtest.**

  Test first (`catalogarr/controller/delayprofile/resolve_test.go`):
  ```go
  package delayprofile

  import (
  	"testing"

  	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

  	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
  )

  func profile(name string, order int32, tags ...string) catalogv1alpha1.DelayProfile {
  	return catalogv1alpha1.DelayProfile{
  		ObjectMeta: metav1.ObjectMeta{Name: name},
  		Spec:       catalogv1alpha1.DelayProfileSpec{Order: order, Tags: tags},
  	}
  }

  func TestResolveExplicitRefWinsOutright(t *testing.T) {
  	profiles := []catalogv1alpha1.DelayProfile{
  		profile("catchall", 1000),
  		profile("anime", 50, "anime"),
  		profile("chosen", 500, "4k"),
  	}
  	ref := "chosen"
  	got, err := Resolve(&ref, []string{"anime"}, profiles) // tags would otherwise match "anime"
  	if err != nil {
  		t.Fatalf("Resolve: %v", err)
  	}
  	if got.Name != "chosen" {
  		t.Errorf("got %q, want chosen (explicit ref must win over a tag match)", got.Name)
  	}
  }

  func TestResolveTagMatchBeatsCatchallEvenWithHigherOrder(t *testing.T) {
  	profiles := []catalogv1alpha1.DelayProfile{
  		profile("catchall", 1000),      // no tags: matches everything, order 1000
  		profile("anime", 5000, "anime"), // matches "anime" but a WORSE (higher) order
  	}
  	got, err := Resolve(nil, []string{"anime"}, profiles)
  	if err != nil {
  		t.Fatalf("Resolve: %v", err)
  	}
  	// Lowest order wins among everything that matches (tag overlap OR
  	// catch-all); "anime" has 5000 > "catchall"'s 1000, so catchall wins even
  	// though "anime" is the more specific tag match. This mirrors the CRD
  	// doc's literal "lowest order" rule -- Order is the sole tie-breaker
  	// once a profile is in the candidate set, tag specificity is not a
  	// second-order signal.
  	if got.Name != "catchall" {
  		t.Errorf("got %q, want catchall (lower order wins regardless of tag specificity)", got.Name)
  	}
  }

  func TestResolveLowestOrderAmongTagMatches(t *testing.T) {
  	profiles := []catalogv1alpha1.DelayProfile{
  		profile("catchall", 1000),
  		profile("anime-strict", 50, "anime", "strict"),
  		profile("anime-loose", 100, "anime"),
  	}
  	got, err := Resolve(nil, []string{"anime"}, profiles)
  	if err != nil {
  		t.Fatalf("Resolve: %v", err)
  	}
  	if got.Name != "anime-strict" {
  		t.Errorf("got %q, want anime-strict (order 50 < 100 < 1000)", got.Name)
  	}
  }

  func TestResolveNoTagMatchFallsBackToCatchall(t *testing.T) {
  	profiles := []catalogv1alpha1.DelayProfile{
  		profile("catchall", 1000),
  		profile("anime", 50, "anime"),
  	}
  	got, err := Resolve(nil, []string{"documentary"}, profiles)
  	if err != nil {
  		t.Fatalf("Resolve: %v", err)
  	}
  	if got.Name != "catchall" {
  		t.Errorf("got %q, want catchall", got.Name)
  	}
  }

  func TestResolveDanglingRefFallsThroughToTagMatch(t *testing.T) {
  	// The CRD does not say what happens when spec.delayProfileRef names a
  	// profile that no longer exists. Treated here as "no ref" rather than an
  	// error, so a deleted profile does not wedge the grab pipeline for every
  	// item that referenced it; flagged as an inferred rule, not a spec fact.
  	profiles := []catalogv1alpha1.DelayProfile{
  		profile("catchall", 1000),
  		profile("anime", 50, "anime"),
  	}
  	ref := "deleted-profile"
  	got, err := Resolve(&ref, []string{"anime"}, profiles)
  	if err != nil {
  		t.Fatalf("Resolve: %v", err)
  	}
  	if got.Name != "anime" {
  		t.Errorf("got %q, want anime (dangling ref falls back to tag match)", got.Name)
  	}
  }

  func TestResolveTiesBreakByName(t *testing.T) {
  	profiles := []catalogv1alpha1.DelayProfile{
  		profile("zzz", 100, "anime"),
  		profile("aaa", 100, "anime"),
  	}
  	got, err := Resolve(nil, []string{"anime"}, profiles)
  	if err != nil {
  		t.Fatalf("Resolve: %v", err)
  	}
  	if got.Name != "aaa" {
  		t.Errorf("got %q, want aaa (lexicographically first on an order tie)", got.Name)
  	}
  }

  func TestResolveNoProfilesIsAnError(t *testing.T) {
  	if _, err := Resolve(nil, nil, nil); err != ErrNoProfiles {
  		t.Errorf("err = %v, want ErrNoProfiles", err)
  	}
  }

  func TestResolveNoMatchAndNoCatchallIsAnError(t *testing.T) {
  	profiles := []catalogv1alpha1.DelayProfile{profile("anime", 50, "anime")}
  	if _, err := Resolve(nil, []string{"documentary"}, profiles); err != ErrNoMatch {
  		t.Errorf("err = %v, want ErrNoMatch", err)
  	}
  }
  ```
  Run: `go test ./catalogarr/controller/delayprofile/... -run TestResolve -v`
  — expect a compile failure (`Resolve` undefined).

  Implement (`catalogarr/controller/delayprofile/resolve.go`):
  ```go
  package delayprofile

  import (
  	"errors"
  	"slices"

  	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
  )

  var (
  	ErrNoProfiles = errors.New("delayprofile: no profiles given")
  	ErrNoMatch    = errors.New("delayprofile: no profile matches and no catch-all is configured")
  )

  // Resolve implements spec §8.2's delay-profile resolution order: item ref
  // → tag match → lowest order.
  //
  //  1. If ref names a profile present in profiles, that profile wins
  //     outright, no matter its Tags or Order. A ref naming an absent
  //     profile is treated as no ref (an inferred rule -- the spec does not
  //     say what a dangling ref does -- chosen so a deleted DelayProfile
  //     cannot wedge every item that referenced it).
  //  2. Otherwise, the candidate set is every profile whose Tags intersects
  //     itemTags, plus every profile whose Tags is empty (a catch-all, "the
  //     chart installs `default` with order 1000 and no tags"). The
  //     candidate with the lowest Order wins; ties break by Name for
  //     determinism. Tag specificity is not a tie-breaker: Order alone
  //     decides once a profile is a candidate, matching the CRD field
  //     doc's literal "lowest order" rule.
  //  3. An empty candidate set (no tag match and no catch-all configured)
  //     is ErrNoMatch. An empty profiles slice is ErrNoProfiles.
  func Resolve(ref *string, itemTags []string, profiles []catalogv1alpha1.DelayProfile) (*catalogv1alpha1.DelayProfile, error) {
  	if len(profiles) == 0 {
  		return nil, ErrNoProfiles
  	}

  	if ref != nil && *ref != "" {
  		for i := range profiles {
  			if profiles[i].Name == *ref {
  				return &profiles[i], nil
  			}
  		}
  		// dangling ref: fall through to tag match.
  	}

  	var best *catalogv1alpha1.DelayProfile
  	for i := range profiles {
  		p := &profiles[i]
  		if len(p.Spec.Tags) > 0 && !tagsIntersect(p.Spec.Tags, itemTags) {
  			continue
  		}
  		if best == nil || p.Spec.Order < best.Spec.Order ||
  			(p.Spec.Order == best.Spec.Order && p.Name < best.Name) {
  			best = p
  		}
  	}
  	if best == nil {
  		return nil, ErrNoMatch
  	}
  	return best, nil
  }

  func tagsIntersect(a, b []string) bool {
  	for _, t := range a {
  		if slices.Contains(b, t) {
  			return true
  		}
  	}
  	return false
  }
  ```
  Run: `go test ./catalogarr/controller/delayprofile/... -run TestResolve -v`
  — expect all PASS.

  Commit:
  ```bash
  git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m \
    "feat(catalogarr): delay-profile resolution order" -- \
    catalogarr/controller/delayprofile/resolve.go catalogarr/controller/delayprofile/resolve_test.go
  ```

- [ ] **Step 2: the reconciler — thin, since DelayProfile carries no CEL
      beyond field-level bounds the apiserver already enforces.**

  `status.pendingCount` is explicitly **not** written by this controller:
  computing it needs the `clustarr-pending` KV bucket's grab leases, which
  belong to the grab worker (Task C9), not this task. It is left at its zero
  value here. Whether Task C9 becomes a second field manager on
  `DelayProfileStatus` (à la the `MediaFile` split) or asks this controller to
  recompute it via a watch is Task C9's decision to make and document, not
  this task's — flag it to whoever writes that task.

  Test first (`catalogarr/controller/delayprofile/controller_envtest_test.go`):
  ```go
  package delayprofile_test

  import (
  	"context"
  	"os"
  	"testing"

  	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
  	"k8s.io/apimachinery/pkg/types"
  	"k8s.io/client-go/tools/events"
  	"sigs.k8s.io/controller-runtime/pkg/client"
  	"sigs.k8s.io/controller-runtime/pkg/envtest"
  	"sigs.k8s.io/controller-runtime/pkg/reconcile"

  	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
  	"github.com/mediactl/clustarr/catalogarr/controller/delayprofile"
  	"github.com/mediactl/clustarr/pkg/k8s"
  )

  func newTestClient(t *testing.T) client.Client {
  	t.Helper()
  	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
  		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
  	}
  	env := &envtest.Environment{CRDDirectoryPaths: []string{"../../../config/crd/bases"}, ErrorIfCRDPathMissing: true}
  	cfg, err := env.Start()
  	if err != nil {
  		t.Fatalf("start envtest: %v", err)
  	}
  	t.Cleanup(func() {
  		if err := env.Stop(); err != nil {
  			t.Errorf("stop envtest: %v", err)
  		}
  	})
  	c, err := client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
  	if err != nil {
  		t.Fatalf("build client: %v", err)
  	}
  	return c
  }

  func TestReconcileMarksReady(t *testing.T) {
  	ctx := context.Background()
  	c := newTestClient(t)
  	if err := c.Create(ctx, &corev1Namespace("default")); err != nil {
  		t.Fatalf("create namespace: %v", err)
  	}
  	dp := &catalogv1alpha1.DelayProfile{ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: "default"}}
  	if err := c.Create(ctx, dp); err != nil {
  		t.Fatalf("create DelayProfile: %v", err)
  	}

  	r := delayprofile.NewReconciler(c, events.NewFakeRecorder(10))
  	if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "default"}}); err != nil {
  		t.Fatalf("Reconcile: %v", err)
  	}

  	var got catalogv1alpha1.DelayProfile
  	if err := c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "default"}, &got); err != nil {
  		t.Fatalf("get: %v", err)
  	}
  	if !k8s.IsConditionTrue(got.Status.Conditions, catalogv1alpha1.DelayProfileConditionReady) {
  		t.Error("Ready is not True")
  	}
  	if got.Status.ObservedGeneration != got.Generation {
  		t.Errorf("observedGeneration = %d, want %d", got.Status.ObservedGeneration, got.Generation)
  	}
  }
  ```
  (`corev1Namespace` is a tiny local helper — `func corev1Namespace(name
  string) corev1.Namespace { return corev1.Namespace{ObjectMeta:
  metav1.ObjectMeta{Name: name}} }` — add it next to the test; `default`
  namespace already exists on most clusters but envtest starts clean.)

  Run: `KUBEBUILDER_ASSETS="$(setup-envtest use 1.37.0 -p path)" go test
  ./catalogarr/controller/delayprofile/... -run TestReconcile -v` — expect a
  compile failure (`delayprofile.NewReconciler` undefined).

  Implement (`catalogarr/controller/delayprofile/controller.go`):
  ```go
  package delayprofile

  import (
  	"context"
  	"time"

  	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
  	"k8s.io/client-go/tools/events"
  	ctrl "sigs.k8s.io/controller-runtime"
  	"sigs.k8s.io/controller-runtime/pkg/builder"
  	"sigs.k8s.io/controller-runtime/pkg/client"
  	"sigs.k8s.io/controller-runtime/pkg/controller"
  	"sigs.k8s.io/controller-runtime/pkg/reconcile"

  	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
  	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
  	"github.com/mediactl/clustarr/pkg/k8s"
  	"github.com/mediactl/clustarr/pkg/obs/logging"
  	"github.com/mediactl/clustarr/pkg/obs/tracing"
  )

  type Reconciler struct {
  	Client   client.Client
  	Recorder events.EventRecorder
  }

  func NewReconciler(c client.Client, recorder events.EventRecorder) *Reconciler {
  	return &Reconciler{Client: c, Recorder: recorder}
  }

  func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (ctrl.Result, error) {
  	ctx, span := tracing.Start(ctx, "delayprofile.Reconcile")
  	defer span.End()
  	log := logging.FromContext(ctx).With("delayprofile", req.NamespacedName)

  	var dp catalogv1alpha1.DelayProfile
  	if err := r.Client.Get(ctx, req.NamespacedName, &dp); err != nil {
  		return ctrl.Result{}, client.IgnoreNotFound(err)
  	}

  	conditions := append([]metav1.Condition(nil), dp.Status.Conditions...)
  	k8s.MarkReady(&dp, &conditions, true, k8s.ReasonReconciled, "field-level validation only; CRD schema covers everything else")

  	ac := catalogac.DelayProfile(dp.Name, dp.Namespace).WithStatus(
  		catalogac.DelayProfileStatus().
  			WithObservedGeneration(dp.Generation).
  			WithConditions(k8s.ConditionACs(conditions)...),
  	)
  	if _, err := k8s.PatchStatus(ctx, r.Client, k8s.ManagerCatalogarr, ac); err != nil {
  		log.Error("patch status", "error", err)
  		return ctrl.Result{}, err
  	}
  	return ctrl.Result{}, nil
  }

  // +kubebuilder:rbac:groups=catalog.clustarr.io,resources=delayprofiles,verbs=get;list;watch
  // +kubebuilder:rbac:groups=catalog.clustarr.io,resources=delayprofiles/status,verbs=get;update;patch
  func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
  	return ctrl.NewControllerManagedBy(mgr).
  		Named("delayprofile").
  		For(&catalogv1alpha1.DelayProfile{}, builder.WithPredicates(k8s.GenerationChanged())).
  		WithOptions(controller.Options{ReconciliationTimeout: 5 * time.Minute}).
  		Complete(r)
  }
  ```
  Run the envtest command above again — expect PASS.

  Commit:
  ```bash
  git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m \
    "feat(catalogarr): delayprofile controller" -- \
    catalogarr/controller/delayprofile/controller.go catalogarr/controller/delayprofile/controller_envtest_test.go
  ```

---

#### MetadataProvider

**Scope gap, flagged rather than silently worked around.** The CRD's `type`
enum has 14 values (`tmdb tvdb musicbrainz coverart fanart openlibrary
hardcover audnexus comicvine metron mangadex anilist kitsu animelists`).
Phase B (`pkg/metadata/clients/`) shipped concrete clients for exactly six:
`tmdb tvdb musicbrainz openlibrary comicvine audnexus`. The other eight have
no Go client at all yet. This controller probes and registers the six it can,
and for the other eight sets `Ready=Unknown`, reason `ProviderNotImplemented`,
and does not error the reconcile (a user creating a `coverart`
`MetadataProvider` today gets an honest, static status, not a crash loop or a
silent lie).

- [ ] **Step 1: the `Prober` interface and two representative adapters
      (`tmdb`, keyed; `openlibrary`, unauthenticated) against a local
      `httptest.Server` — no real network, per the testing constraint.**

  Test first (`catalogarr/controller/metadataprovider/prober_test.go`):
  ```go
  package metadataprovider

  import (
  	"context"
  	"encoding/json"
  	"net/http"
  	"net/http/httptest"
  	"testing"

  	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
  	"github.com/mediactl/clustarr/pkg/metadata"
  )

  func TestTMDBProberSucceeds(t *testing.T) {
  	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
  		_ = json.NewEncoder(w).Encode(map[string]any{
  			"page": 1, "results": []any{}, "total_pages": 1, "total_results": 0,
  		})
  	}))
  	defer srv.Close()

  	p, err := NewProber(
  		catalogv1alpha1.MetadataProviderSpec{Type: catalogv1alpha1.MetadataProviderTMDB, BaseURL: &srv.URL},
  		map[string][]byte{"apiKey": []byte("test-key")},
  		srv.Client(),
  	)
  	if err != nil {
  		t.Fatalf("NewProber: %v", err)
  	}
  	if _, err := p.Probe(context.Background()); err != nil {
  		t.Errorf("Probe: %v", err)
  	}
  }

  func TestTMDBProberMissingAPIKeyFailsFast(t *testing.T) {
  	if _, err := NewProber(
  		catalogv1alpha1.MetadataProviderSpec{Type: catalogv1alpha1.MetadataProviderTMDB},
  		nil, http.DefaultClient,
  	); err == nil {
  		t.Error("no apiKey secret data accepted for tmdb")
  	}
  }

  func TestOpenLibraryProberSucceedsWithNoCredentials(t *testing.T) {
  	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
  		_ = json.NewEncoder(w).Encode(map[string]any{"docs": []any{}, "numFound": 0})
  	}))
  	defer srv.Close()

  	p, err := NewProber(
  		catalogv1alpha1.MetadataProviderSpec{
  			Type:             catalogv1alpha1.MetadataProviderOpenLibrary,
  			BaseURL:          &srv.URL,
  			ContactUserAgent: "clustarr-test/0.0 (test@example.invalid)",
  		},
  		nil, srv.Client(),
  	)
  	if err != nil {
  		t.Fatalf("NewProber: %v", err)
  	}
  	if _, err := p.Probe(context.Background()); err != nil {
  		t.Errorf("Probe: %v", err)
  	}
  }

  func TestUnimplementedProviderTypeIsExplicit(t *testing.T) {
  	_, err := NewProber(catalogv1alpha1.MetadataProviderSpec{Type: catalogv1alpha1.MetadataProviderCoverArt}, nil, http.DefaultClient)
  	if err != ErrProviderNotImplemented {
  		t.Errorf("err = %v, want ErrProviderNotImplemented", err)
  	}
  }

  func TestTMDBProberMapsAuthFailure(t *testing.T) {
  	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
  		w.WriteHeader(http.StatusUnauthorized)
  		_ = json.NewEncoder(w).Encode(map[string]any{"status_code": 7, "status_message": "Invalid API key"})
  	}))
  	defer srv.Close()

  	p, err := NewProber(
  		catalogv1alpha1.MetadataProviderSpec{Type: catalogv1alpha1.MetadataProviderTMDB, BaseURL: &srv.URL},
  		map[string][]byte{"apiKey": []byte("bad-key")},
  		srv.Client(),
  	)
  	if err != nil {
  		t.Fatalf("NewProber: %v", err)
  	}
  	if _, err := p.Probe(context.Background()); !metadata.IsAuthError(err) {
  		t.Errorf("Probe err = %v, want an auth error (wraps metadata.ErrAuth)", err)
  	}
  }
  ```
  Run: `go test ./catalogarr/controller/metadataprovider/... -run
  'TestTMDBProber|TestOpenLibraryProber|TestUnimplementedProviderType' -v` —
  expect a compile failure (`NewProber` undefined; also
  `metadata.IsAuthError` — check `go doc ./pkg/metadata` for the exact
  spelling before writing this: if Phase B did not expose a helper like this,
  replace the assertion with `errors.Is(err, metadata.ErrAuth) ||
  errors.As(err, new(*metadata.RateLimitedError))`-style checks against the
  sentinels this task already verified exist, and drop the invented helper
  name).

  Implement (`catalogarr/controller/metadataprovider/prober.go`):
  ```go
  package metadataprovider

  import (
  	"context"
  	"errors"
  	"fmt"
  	"net/http"
  	"time"

  	"golang.org/x/time/rate"

  	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
  	"github.com/mediactl/clustarr/pkg/metadata"
  	"github.com/mediactl/clustarr/pkg/metadata/clients/audnexus"
  	"github.com/mediactl/clustarr/pkg/metadata/clients/comicvine"
  	"github.com/mediactl/clustarr/pkg/metadata/clients/musicbrainz"
  	"github.com/mediactl/clustarr/pkg/metadata/clients/openlibrary"
  	"github.com/mediactl/clustarr/pkg/metadata/clients/tmdb"
  	"github.com/mediactl/clustarr/pkg/metadata/clients/tvdb"
  )

  var ErrProviderNotImplemented = errors.New("metadataprovider: no Phase B client exists for this provider type yet")

  // ProbeResult carries whatever the probe learned that is worth writing to
  // status beyond reachability itself.
  type ProbeResult struct {
  	QuotaRemaining *int32
  }

  // Prober checks that a configured provider is reachable and, where the
  // provider takes credentials, that they were accepted. It performs exactly
  // one cheap, read-only call -- a search or a zero-argument "what changed"
  // call where the client offers one, never a call keyed by a guessed real
  // entity id.
  type Prober interface {
  	Probe(ctx context.Context) (ProbeResult, error)
  }

  func baseURL(spec catalogv1alpha1.MetadataProviderSpec) string {
  	if spec.BaseURL != nil {
  		return *spec.BaseURL
  	}
  	return "" // every client in pkg/metadata/clients defaults its own baseURL on ""
  }

  func limiterFor(spec catalogv1alpha1.MetadataProviderSpec, def rate.Limit, defBurst int) *rate.Limiter {
  	if spec.RateLimit == nil {
  		return metadata.NewLimiter(def, defBurst)
  	}
  	rps := def
  	if spec.RateLimit.RequestsPerSecond != nil {
  		rps = rate.Limit(spec.RateLimit.RequestsPerSecond.AsApproximateFloat64())
  	}
  	burst := defBurst
  	if spec.RateLimit.Burst > 0 {
  		burst = int(spec.RateLimit.Burst)
  	}
  	return metadata.NewLimiter(rps, burst)
  }

  // NewProber builds the Prober for spec.Type, or ErrProviderNotImplemented
  // for one of the eight types Phase B did not ship a client for
  // (coverart, fanart, hardcover, metron, mangadex, anilist, kitsu,
  // animelists).
  func NewProber(spec catalogv1alpha1.MetadataProviderSpec, secret map[string][]byte, httpClient *http.Client) (Prober, error) {
  	limits := metadata.DefaultLimits()

  	switch spec.Type {
  	case catalogv1alpha1.MetadataProviderTMDB:
  		apiKey := string(secret["apiKey"])
  		if apiKey == "" {
  			return nil, fmt.Errorf("metadataprovider: tmdb requires secretRef key apiKey")
  		}
  		c, err := tmdb.New(apiKey, httpClient, baseURL(spec), limiterFor(spec, limits.TMDB, limits.TMDBBurst))
  		if err != nil {
  			return nil, fmt.Errorf("metadataprovider: build tmdb client: %w", err)
  		}
  		return tmdbProber{c}, nil

  	case catalogv1alpha1.MetadataProviderTVDB:
  		apiKey, pin := string(secret["apiKey"]), string(secret["pin"])
  		if apiKey == "" {
  			return nil, fmt.Errorf("metadataprovider: tvdb requires secretRef key apiKey")
  		}
  		c := tvdb.New(apiKey, pin, httpClient, baseURL(spec), limiterFor(spec, limits.TVDB, limits.TVDBBurst))
  		return tvdbProber{c}, nil

  	case catalogv1alpha1.MetadataProviderMusicBrainz:
  		c, err := musicbrainz.New(spec.ContactUserAgent, httpClient, baseURL(spec), limiterFor(spec, limits.MusicBrainz, limits.MusicBrainzBurst))
  		if err != nil {
  			return nil, fmt.Errorf("metadataprovider: build musicbrainz client: %w", err)
  		}
  		return musicbrainzProber{c}, nil

  	case catalogv1alpha1.MetadataProviderOpenLibrary:
  		if spec.ContactUserAgent == "" {
  			return nil, fmt.Errorf("metadataprovider: openlibrary requires contactUserAgent")
  		}
  		c := openlibrary.New(spec.ContactUserAgent, httpClient, baseURL(spec), limiterFor(spec, limits.OpenLibrary, limits.OpenLibraryBurst))
  		return openlibraryProber{c}, nil

  	case catalogv1alpha1.MetadataProviderComicVine:
  		apiKey := string(secret["apiKey"])
  		if apiKey == "" {
  			return nil, fmt.Errorf("metadataprovider: comicvine requires secretRef key apiKey")
  		}
  		c := comicvine.New(apiKey, httpClient, baseURL(spec), limiterFor(spec, limits.ComicVine, limits.ComicVineBurst))
  		return comicvineProber{c}, nil

  	case catalogv1alpha1.MetadataProviderAudnexus:
  		c := audnexus.New(httpClient, baseURL(spec), limiterFor(spec, limits.Audnexus, limits.AudnexusBurst))
  		return audnexusProber{c}, nil

  	default:
  		return nil, ErrProviderNotImplemented
  	}
  }

  type tmdbProber struct{ c *tmdb.Client }

  func (p tmdbProber) Probe(ctx context.Context) (ProbeResult, error) {
  	// A search, not a specific movie id: proves reachability and that the
  	// api key was accepted without asserting any particular title exists.
  	_, err := p.c.SearchMovies(ctx, "star wars", 1)
  	return ProbeResult{}, err
  }

  type openlibraryProber struct{ c *openlibrary.Client }

  func (p openlibraryProber) Probe(ctx context.Context) (ProbeResult, error) {
  	_, err := p.c.SearchBooks(ctx, "the lord of the rings")
  	return ProbeResult{}, err
  }

  // tvdbProber, musicbrainzProber, comicvineProber and audnexusProber follow
  // in Step 2, same shape.
  ```
  Run: `go test ./catalogarr/controller/metadataprovider/... -run
  'TestTMDBProber|TestOpenLibraryProber|TestUnimplementedProviderType' -v` —
  expect all PASS. Note: this step deliberately leaves `tvdbProber` etc.
  referenced by nothing yet but declared in Step 2 — `go build` of the
  package will fail until Step 2 lands the remaining four types in the same
  `switch`; do Steps 1 and 2 as one commit if your toolchain does not like an
  intermediate non-building state, or stub the four remaining `case`s with
  `return nil, ErrProviderNotImplemented` temporarily and remove the stub in
  Step 2.

  Commit: fold into Step 2's commit (see below) rather than committing a
  package that does not build.

- [ ] **Step 2: the remaining four adapters (`tvdb`, `musicbrainz`,
      `comicvine`, `audnexus`) — same `Prober` shape, one real, guess-free
      call each.**

  Extend `prober_test.go` with:
  ```go
  func TestTVDBProberSucceeds(t *testing.T) {
  	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
  		switch r.URL.Path {
  		case "/login":
  			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]string{"token": "fake-jwt"}})
  		default:
  			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
  		}
  	}))
  	defer srv.Close()

  	p, err := NewProber(
  		catalogv1alpha1.MetadataProviderSpec{Type: catalogv1alpha1.MetadataProviderTVDB, BaseURL: &srv.URL},
  		map[string][]byte{"apiKey": []byte("k"), "pin": []byte("0000")},
  		srv.Client(),
  	)
  	if err != nil {
  		t.Fatalf("NewProber: %v", err)
  	}
  	if _, err := p.Probe(context.Background()); err != nil {
  		t.Errorf("Probe: %v", err)
  	}
  }

  func TestMusicBrainzProberSucceeds(t *testing.T) {
  	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
  		_ = json.NewEncoder(w).Encode(map[string]any{"artists": []any{}, "count": 0})
  	}))
  	defer srv.Close()

  	p, err := NewProber(
  		catalogv1alpha1.MetadataProviderSpec{
  			Type: catalogv1alpha1.MetadataProviderMusicBrainz, BaseURL: &srv.URL,
  			ContactUserAgent: "clustarr-test/0.0 (test@example.invalid)",
  		},
  		nil, srv.Client(),
  	)
  	if err != nil {
  		t.Fatalf("NewProber: %v", err)
  	}
  	if _, err := p.Probe(context.Background()); err != nil {
  		t.Errorf("Probe: %v", err)
  	}
  }

  func TestComicVineProberSucceeds(t *testing.T) {
  	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
  		_ = json.NewEncoder(w).Encode(map[string]any{"status_code": 1, "results": []any{}})
  	}))
  	defer srv.Close()

  	p, err := NewProber(
  		catalogv1alpha1.MetadataProviderSpec{Type: catalogv1alpha1.MetadataProviderComicVine, BaseURL: &srv.URL},
  		map[string][]byte{"apiKey": []byte("k")},
  		srv.Client(),
  	)
  	if err != nil {
  		t.Fatalf("NewProber: %v", err)
  	}
  	if _, err := p.Probe(context.Background()); err != nil {
  		t.Errorf("Probe: %v", err)
  	}
  }

  func TestAudnexusProberSucceeds(t *testing.T) {
  	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
  		_ = json.NewEncoder(w).Encode(map[string]any{"chapters": []any{}, "runtimeLengthMs": 0})
  	}))
  	defer srv.Close()

  	p, err := NewProber(
  		catalogv1alpha1.MetadataProviderSpec{Type: catalogv1alpha1.MetadataProviderAudnexus, BaseURL: &srv.URL},
  		nil, srv.Client(),
  	)
  	if err != nil {
  		t.Fatalf("NewProber: %v", err)
  	}
  	// B08G9PRS1K (Project Hail Mary, an Audible catalog entry, chosen only
  	// for being a stable, syntactically valid ASIN) -- the fake server above
  	// answers any path, so the specific value only has to be well-formed.
  	if _, err := p.Probe(context.Background()); err != nil {
  		t.Errorf("Probe: %v", err)
  	}
  }
  ```
  Run: `go test ./catalogarr/controller/metadataprovider/... -run
  'TestTVDBProber|TestMusicBrainzProber|TestComicVineProber|TestAudnexusProber' -v`
  — expect a compile failure (the four `case`s in `NewProber` are missing
  their prober types).

  Extend `prober.go`: replace the `default:` fallthrough comment from Step 1
  with the four real `case`s (already wired into the `switch` above — move
  them out of "Step 1 leaves them declared but unreferenced" into the actual
  switch), and add:
  ```go
  type tvdbProber struct{ c *tvdb.Client }

  func (p tvdbProber) Probe(ctx context.Context) (ProbeResult, error) {
  	// Updates(since) takes no id at all -- the cleanest possible reachability
  	// probe, since there is nothing to get wrong about what it asks for.
  	_, err := p.c.Updates(ctx, time.Now().Add(-1*time.Hour))
  	return ProbeResult{}, err
  }

  type musicbrainzProber struct{ c *musicbrainz.Client }

  func (p musicbrainzProber) Probe(ctx context.Context) (ProbeResult, error) {
  	_, err := p.c.SearchArtists(ctx, "radiohead")
  	return ProbeResult{}, err
  }

  type comicvineProber struct{ c *comicvine.Client }

  func (p comicvineProber) Probe(ctx context.Context) (ProbeResult, error) {
  	_, err := p.c.SearchVolumes(ctx, "batman")
  	return ProbeResult{}, err
  }

  type audnexusProber struct{ c *audnexus.Client }

  func (p audnexusProber) Probe(ctx context.Context) (ProbeResult, error) {
  	_, err := p.c.Chapters(ctx, "B08G9PRS1K", "us")
  	return ProbeResult{}, err
  }
  ```
  Run: full `go test ./catalogarr/controller/metadataprovider/... -run
  Prober -v` — expect all PASS.

  Commit (both prober steps together, since the package does not build until
  both land):
  ```bash
  git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m \
    "feat(catalogarr): metadata provider credential/reachability probes" -- \
    catalogarr/controller/metadataprovider/prober.go catalogarr/controller/metadataprovider/prober_test.go
  ```

- [ ] **Step 3: `BuildRegistry` — the function Task C5's gateway process
      calls with its own client.**

  Test first (`catalogarr/controller/metadataprovider/registry_test.go`,
  envtest — building a real `metadata.Registry` needs real `MetadataProvider`
  and `Secret` objects, not fakes, since ordering-by-priority is the thing
  under test):
  ```go
  package metadataprovider_test

  import (
  	"context"
  	"os"
  	"testing"

  	corev1 "k8s.io/api/core/v1"
  	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
  	"sigs.k8s.io/controller-runtime/pkg/client"
  	"sigs.k8s.io/controller-runtime/pkg/envtest"

  	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
  	"github.com/mediactl/clustarr/catalogarr/controller/metadataprovider"
  	"github.com/mediactl/clustarr/pkg/k8s"
  )

  func newTestClient(t *testing.T) client.Client {
  	t.Helper()
  	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
  		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
  	}
  	env := &envtest.Environment{CRDDirectoryPaths: []string{"../../../config/crd/bases"}, ErrorIfCRDPathMissing: true}
  	cfg, err := env.Start()
  	if err != nil {
  		t.Fatalf("start envtest: %v", err)
  	}
  	t.Cleanup(func() {
  		if err := env.Stop(); err != nil {
  			t.Errorf("stop envtest: %v", err)
  		}
  	})
  	c, err := client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
  	if err != nil {
  		t.Fatalf("build client: %v", err)
  	}
  	return c
  }

  func TestBuildRegistryOrdersByPriorityAndSkipsUnimplemented(t *testing.T) {
  	ctx := context.Background()
  	c := newTestClient(t)
  	ns := "registry-test"
  	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}); err != nil {
  		t.Fatalf("create namespace: %v", err)
  	}
  	if err := c.Create(ctx, &corev1.Secret{
  		ObjectMeta: metav1.ObjectMeta{Name: "tmdb-creds", Namespace: ns},
  		Data:       map[string][]byte{"apiKey": []byte("k")},
  	}); err != nil {
  		t.Fatalf("create secret: %v", err)
  	}

  	providers := []catalogv1alpha1.MetadataProvider{
  		{ObjectMeta: metav1.ObjectMeta{Name: "tmdb-low", Namespace: ns}, Spec: catalogv1alpha1.MetadataProviderSpec{
  			Type: catalogv1alpha1.MetadataProviderTMDB, Priority: 90,
  			SecretRef: &corev1.LocalObjectReference{Name: "tmdb-creds"},
  		}},
  		{ObjectMeta: metav1.ObjectMeta{Name: "tmdb-high", Namespace: ns}, Spec: catalogv1alpha1.MetadataProviderSpec{
  			Type: catalogv1alpha1.MetadataProviderTMDB, Priority: 10,
  			SecretRef: &corev1.LocalObjectReference{Name: "tmdb-creds"},
  		}},
  		{ObjectMeta: metav1.ObjectMeta{Name: "disabled-tmdb", Namespace: ns}, Spec: catalogv1alpha1.MetadataProviderSpec{
  			Type: catalogv1alpha1.MetadataProviderTMDB, Enabled: boolPtr(false),
  			SecretRef: &corev1.LocalObjectReference{Name: "tmdb-creds"},
  		}},
  		{ObjectMeta: metav1.ObjectMeta{Name: "not-implemented", Namespace: ns}, Spec: catalogv1alpha1.MetadataProviderSpec{
  			Type: catalogv1alpha1.MetadataProviderCoverArt,
  		}},
  	}
  	for i := range providers {
  		if err := c.Create(ctx, &providers[i]); err != nil {
  			t.Fatalf("create %s: %v", providers[i].Name, err)
  		}
  	}

  	reg, err := metadataprovider.BuildRegistry(ctx, c, ns, nil)
  	if err != nil {
  		t.Fatalf("BuildRegistry: %v", err)
  	}
  	if len(reg.Movies) != 2 {
  		t.Fatalf("got %d movie providers, want 2 (disabled and unimplemented excluded)", len(reg.Movies))
  	}
  	if reg.Movies[0].Name() != "tmdb" || reg.Movies[1].Name() != "tmdb" {
  		t.Errorf("both entries should be tmdb clients (only Priority differs), got %s / %s", reg.Movies[0].Name(), reg.Movies[1].Name())
  	}
  	// Priority order cannot be asserted by Name() alone since both are
  	// "tmdb" -- this proves BuildRegistry sorted by ascending Priority via
  	// its own internal bookkeeping; if that assertion is impossible without
  	// exposing more of Registry's internals than pkg/metadata does today,
  	// replace it with a test-only wrapper that records construction order,
  	// or add a Registry.MoviesWithNames() test seam in this task's package
  	// (not in pkg/metadata, which this task does not own) -- do not weaken
  	// the assertion to "len == 2" alone; ordering is the point of the test.
  }

  func boolPtr(b bool) *bool { return &b }
  ```
  Run: `KUBEBUILDER_ASSETS="$(setup-envtest use 1.37.0 -p path)" go test
  ./catalogarr/controller/metadataprovider/... -run TestBuildRegistry -v` —
  expect a compile failure (`BuildRegistry` undefined). Before implementing,
  resolve the ordering-assertion note in the test above: check `go doc
  ./pkg/metadata Registry` for anything that exposes provider identity beyond
  `Name()` (a `Provider.Capabilities()` may be enough to distinguish two
  same-name entries by e.g. an injected marker in a test double) — if
  nothing suffices, add a tiny local test-only `MovieProvider` wrapper in
  this test file that embeds the real `tmdb.Client` and overrides nothing,
  purely to make `reg.Movies[i]` identifiable in assertions; that wrapper
  lives in `_test.go`, never in `registry.go`.

  Implement (`catalogarr/controller/metadataprovider/registry.go`):
  ```go
  package metadataprovider

  import (
  	"context"
  	"fmt"
  	"net/http"
  	"slices"

  	apierrors "k8s.io/apimachinery/pkg/api/errors"
  	corev1 "k8s.io/api/core/v1"
  	"k8s.io/apimachinery/pkg/types"
  	"sigs.k8s.io/controller-runtime/pkg/client"

  	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
  	"github.com/mediactl/clustarr/pkg/metadata"
  	"github.com/mediactl/clustarr/pkg/metadata/clients/audnexus"
  	"github.com/mediactl/clustarr/pkg/metadata/clients/comicvine"
  	"github.com/mediactl/clustarr/pkg/metadata/clients/musicbrainz"
  	"github.com/mediactl/clustarr/pkg/metadata/clients/openlibrary"
  	"github.com/mediactl/clustarr/pkg/metadata/clients/tmdb"
  	"github.com/mediactl/clustarr/pkg/metadata/clients/tvdb"
  )

  // BuildRegistry lists every enabled MetadataProvider in namespace, sorted
  // ascending by Priority within each kind (ties by Name for determinism),
  // and constructs a metadata.Registry from them. It skips a disabled
  // provider and a provider of a type Phase B did not ship a client for
  // (ErrProviderNotImplemented) rather than failing the whole build -- one
  // bad or unimplemented provider must not take every other one down.
  //
  // It includes every enabled provider regardless of its last observed
  // Ready/Authenticated/Throttled status: reachability is a per-call concern
  // for metadata.Registry.Lookup's caller (a throttled provider fails its own
  // calls; that is not a reason to leave it out of the registry), and this
  // avoids the caller depending on this task's controller having reconciled
  // recently.
  func BuildRegistry(ctx context.Context, c client.Client, namespace string, httpClient *http.Client) (*metadata.Registry, error) {
  	var list catalogv1alpha1.MetadataProviderList
  	if err := c.List(ctx, &list, client.InNamespace(namespace)); err != nil {
  		return nil, fmt.Errorf("metadataprovider: list: %w", err)
  	}

  	items := slices.Clone(list.Items)
  	slices.SortFunc(items, func(a, b catalogv1alpha1.MetadataProvider) int {
  		if a.Spec.Priority != b.Spec.Priority {
  			return int(a.Spec.Priority) - int(b.Spec.Priority)
  		}
  		if a.Name < b.Name {
  			return -1
  		}
  		if a.Name > b.Name {
  			return 1
  		}
  		return 0
  	})

  	reg := &metadata.Registry{}
  	for _, p := range items {
  		if p.Spec.Enabled != nil && !*p.Spec.Enabled {
  			continue
  		}
  		secretData, err := readSecret(ctx, c, p.Namespace, p.Spec.SecretRef)
  		if err != nil {
  			return nil, err
  		}
  		if err := addToRegistry(reg, p.Spec, secretData, httpClient); err != nil {
  			if err == ErrProviderNotImplemented {
  				continue
  			}
  			return nil, fmt.Errorf("metadataprovider: build client for %s: %w", p.Name, err)
  		}
  	}
  	return reg, nil
  }

  func readSecret(ctx context.Context, c client.Client, ns string, ref *corev1.LocalObjectReference) (map[string][]byte, error) {
  	if ref == nil {
  		return nil, nil
  	}
  	var s corev1.Secret
  	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: ref.Name}, &s); err != nil {
  		if apierrors.IsNotFound(err) {
  			return nil, fmt.Errorf("metadataprovider: secret %s/%s not found", ns, ref.Name)
  		}
  		return nil, err
  	}
  	return s.Data, nil
  }

  // addToRegistry mirrors NewProber's switch, but builds the raw client and
  // appends it to the right Registry slice instead of wrapping it in Prober.
  // The two switches share the same construction calls; kept separate rather
  // than factored together because Prober's job (one cheap probe call) and
  // Registry's job (the real, capability-typed client) return different
  // things and unifying them would need an interface neither pkg/metadata
  // nor this task's Prober actually needs.
  func addToRegistry(reg *metadata.Registry, spec catalogv1alpha1.MetadataProviderSpec, secret map[string][]byte, httpClient *http.Client) error {
  	limits := metadata.DefaultLimits()
  	switch spec.Type {
  	case catalogv1alpha1.MetadataProviderTMDB:
  		c, err := tmdb.New(string(secret["apiKey"]), httpClient, baseURL(spec), limiterFor(spec, limits.TMDB, limits.TMDBBurst))
  		if err != nil {
  			return err
  		}
  		reg.Movies = append(reg.Movies, c)
  	case catalogv1alpha1.MetadataProviderTVDB:
  		reg.Series = append(reg.Series, tvdb.New(string(secret["apiKey"]), string(secret["pin"]), httpClient, baseURL(spec), limiterFor(spec, limits.TVDB, limits.TVDBBurst)))
  	case catalogv1alpha1.MetadataProviderMusicBrainz:
  		c, err := musicbrainz.New(spec.ContactUserAgent, httpClient, baseURL(spec), limiterFor(spec, limits.MusicBrainz, limits.MusicBrainzBurst))
  		if err != nil {
  			return err
  		}
  		reg.Artists = append(reg.Artists, c)
  	case catalogv1alpha1.MetadataProviderOpenLibrary:
  		reg.Books = append(reg.Books, openlibrary.New(spec.ContactUserAgent, httpClient, baseURL(spec), limiterFor(spec, limits.OpenLibrary, limits.OpenLibraryBurst)))
  	case catalogv1alpha1.MetadataProviderComicVine:
  		reg.Comics = append(reg.Comics, comicvine.New(string(secret["apiKey"]), httpClient, baseURL(spec), limiterFor(spec, limits.ComicVine, limits.ComicVineBurst)))
  	case catalogv1alpha1.MetadataProviderAudnexus:
  		reg.Audiobooks = append(reg.Audiobooks, audnexus.New(httpClient, baseURL(spec), limiterFor(spec, limits.Audnexus, limits.AudnexusBurst)))
  	default:
  		return ErrProviderNotImplemented
  	}
  	return nil
  }
  ```
  Run the envtest command above again — expect PASS (after resolving the
  ordering-assertion note).

  Commit:
  ```bash
  git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m \
    "feat(catalogarr): metadata provider registry construction for the gateway" -- \
    catalogarr/controller/metadataprovider/registry.go catalogarr/controller/metadataprovider/registry_test.go
  ```

- [ ] **Step 4: the reconciler — probes on a timer, reports
      Ready/Authenticated/Throttled, RBAC for Secrets.**

  Test first (`catalogarr/controller/metadataprovider/controller_envtest_test.go`,
  reuses `newTestClient` from Step 3's file, same package
  `metadataprovider_test`):
  ```go
  package metadataprovider_test

  import (
  	"context"
  	"net/http"
  	"net/http/httptest"
  	"testing"

  	corev1 "k8s.io/api/core/v1"
  	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
  	"k8s.io/apimachinery/pkg/types"
  	"k8s.io/client-go/tools/events"
  	"sigs.k8s.io/controller-runtime/pkg/reconcile"

  	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
  	"github.com/mediactl/clustarr/catalogarr/controller/metadataprovider"
  	"github.com/mediactl/clustarr/pkg/k8s"
  )

  func TestReconcileReachableProviderIsReadyAndAuthenticated(t *testing.T) {
  	ctx := context.Background()
  	c := newTestClient(t)
  	ns := "mdp-reachable"
  	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}); err != nil {
  		t.Fatalf("create namespace: %v", err)
  	}
  	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
  		w.Write([]byte(`{"page":1,"results":[],"total_pages":1,"total_results":0}`))
  	}))
  	defer srv.Close()
  	if err := c.Create(ctx, &corev1.Secret{
  		ObjectMeta: metav1.ObjectMeta{Name: "creds", Namespace: ns}, Data: map[string][]byte{"apiKey": []byte("k")},
  	}); err != nil {
  		t.Fatalf("create secret: %v", err)
  	}
  	mp := &catalogv1alpha1.MetadataProvider{
  		ObjectMeta: metav1.ObjectMeta{Name: "tmdb", Namespace: ns},
  		Spec: catalogv1alpha1.MetadataProviderSpec{
  			Type: catalogv1alpha1.MetadataProviderTMDB, BaseURL: &srv.URL,
  			SecretRef: &corev1.LocalObjectReference{Name: "creds"},
  		},
  	}
  	if err := c.Create(ctx, mp); err != nil {
  		t.Fatalf("create MetadataProvider: %v", err)
  	}

  	r := metadataprovider.NewReconciler(c, events.NewFakeRecorder(10), srv.Client())
  	if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: "tmdb"}}); err != nil {
  		t.Fatalf("Reconcile: %v", err)
  	}

  	var got catalogv1alpha1.MetadataProvider
  	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "tmdb"}, &got); err != nil {
  		t.Fatalf("get: %v", err)
  	}
  	if !k8s.IsConditionTrue(got.Status.Conditions, catalogv1alpha1.MetadataProviderConditionReady) {
  		t.Errorf("Ready is not True: %+v", got.Status.Conditions)
  	}
  	if !k8s.IsConditionTrue(got.Status.Conditions, catalogv1alpha1.MetadataProviderConditionAuthenticated) {
  		t.Error("Authenticated is not True")
  	}
  	if k8s.IsConditionTrue(got.Status.Conditions, catalogv1alpha1.MetadataProviderConditionThrottled) {
  		t.Error("Throttled is True for a healthy server")
  	}
  }

  func TestReconcileDisabledProviderSkipsProbe(t *testing.T) {
  	ctx := context.Background()
  	c := newTestClient(t)
  	ns := "mdp-disabled"
  	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}); err != nil {
  		t.Fatalf("create namespace: %v", err)
  	}
  	disabled := false
  	mp := &catalogv1alpha1.MetadataProvider{
  		ObjectMeta: metav1.ObjectMeta{Name: "tmdb", Namespace: ns},
  		Spec:       catalogv1alpha1.MetadataProviderSpec{Type: catalogv1alpha1.MetadataProviderTMDB, Enabled: &disabled},
  	}
  	if err := c.Create(ctx, mp); err != nil {
  		t.Fatalf("create MetadataProvider: %v", err)
  	}

  	r := metadataprovider.NewReconciler(c, events.NewFakeRecorder(10), http.DefaultClient)
  	if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: "tmdb"}}); err != nil {
  		t.Fatalf("Reconcile: %v", err) // must not attempt a real network call against a fake key
  	}

  	var got catalogv1alpha1.MetadataProvider
  	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "tmdb"}, &got); err != nil {
  		t.Fatalf("get: %v", err)
  	}
  	cond := k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.MetadataProviderConditionReady)
  	if cond == nil || cond.Reason != k8s.ReasonDisabled {
  		t.Errorf("Ready reason = %+v, want %s", cond, k8s.ReasonDisabled)
  	}
  }

  func TestReconcileUnimplementedTypeIsUnknownNotError(t *testing.T) {
  	ctx := context.Background()
  	c := newTestClient(t)
  	ns := "mdp-unimplemented"
  	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}); err != nil {
  		t.Fatalf("create namespace: %v", err)
  	}
  	mp := &catalogv1alpha1.MetadataProvider{
  		ObjectMeta: metav1.ObjectMeta{Name: "coverart", Namespace: ns},
  		Spec:       catalogv1alpha1.MetadataProviderSpec{Type: catalogv1alpha1.MetadataProviderCoverArt},
  	}
  	if err := c.Create(ctx, mp); err != nil {
  		t.Fatalf("create MetadataProvider: %v", err)
  	}

  	r := metadataprovider.NewReconciler(c, events.NewFakeRecorder(10), http.DefaultClient)
  	if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: "coverart"}}); err != nil {
  		t.Fatalf("Reconcile returned an error for an unimplemented type: %v", err)
  	}

  	var got catalogv1alpha1.MetadataProvider
  	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "coverart"}, &got); err != nil {
  		t.Fatalf("get: %v", err)
  	}
  	cond := k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.MetadataProviderConditionReady)
  	if cond == nil || cond.Status != metav1.ConditionUnknown || cond.Reason != "ProviderNotImplemented" {
  		t.Errorf("Ready = %+v, want Unknown/ProviderNotImplemented", cond)
  	}
  }
  ```
  Run: `KUBEBUILDER_ASSETS="$(setup-envtest use 1.37.0 -p path)" go test
  ./catalogarr/controller/metadataprovider/... -run TestReconcile -v` —
  expect a compile failure (`metadataprovider.NewReconciler` undefined).

  Implement (`catalogarr/controller/metadataprovider/controller.go`):
  ```go
  package metadataprovider

  import (
  	"context"
  	"net/http"
  	"time"

  	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
  	"k8s.io/client-go/tools/events"
  	ctrl "sigs.k8s.io/controller-runtime"
  	"sigs.k8s.io/controller-runtime/pkg/builder"
  	"sigs.k8s.io/controller-runtime/pkg/client"
  	"sigs.k8s.io/controller-runtime/pkg/controller"
  	"sigs.k8s.io/controller-runtime/pkg/reconcile"

  	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
  	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
  	"github.com/mediactl/clustarr/pkg/k8s"
  	"github.com/mediactl/clustarr/pkg/metadata"
  	"github.com/mediactl/clustarr/pkg/obs/logging"
  	"github.com/mediactl/clustarr/pkg/obs/tracing"
  )

  // reprobeInterval is how often a healthy provider is re-probed. Not given
  // by the spec; invented -- frequent enough to notice a key rotation or an
  // outage within the hour, cheap enough (one search call) not to threaten
  // any provider's own rate limit even at the lowest configured RPS.
  const reprobeInterval = 15 * time.Minute

  const ReasonProviderNotImplemented = "ProviderNotImplemented"

  type Reconciler struct {
  	Client     client.Client
  	Recorder   events.EventRecorder
  	HTTPClient *http.Client
  }

  func NewReconciler(c client.Client, recorder events.EventRecorder, httpClient *http.Client) *Reconciler {
  	return &Reconciler{Client: c, Recorder: recorder, HTTPClient: httpClient}
  }

  func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (ctrl.Result, error) {
  	ctx, span := tracing.Start(ctx, "metadataprovider.Reconcile")
  	defer span.End()
  	log := logging.FromContext(ctx).With("metadataprovider", req.NamespacedName)

  	var mp catalogv1alpha1.MetadataProvider
  	if err := r.Client.Get(ctx, req.NamespacedName, &mp); err != nil {
  		return ctrl.Result{}, client.IgnoreNotFound(err)
  	}
  	conditions := append([]metav1.Condition(nil), mp.Status.Conditions...)

  	if mp.Spec.Enabled != nil && !*mp.Spec.Enabled {
  		k8s.MarkReady(&mp, &conditions, false, k8s.ReasonDisabled, "spec.enabled is false")
  		return r.patch(ctx, &mp, conditions, nil, ctrl.Result{})
  	}

  	secretData, err := readSecret(ctx, r.Client, mp.Namespace, mp.Spec.SecretRef)
  	if err != nil {
  		k8s.MarkReady(&mp, &conditions, false, k8s.ReasonDependencyNotReady, "%s", err.Error())
  		return r.patch(ctx, &mp, conditions, nil, ctrl.Result{RequeueAfter: reprobeInterval})
  	}

  	prober, err := NewProber(mp.Spec, secretData, r.HTTPClient)
  	if err == ErrProviderNotImplemented {
  		k8s.SetCondition(&mp, &conditions, k8s.NewCondition(catalogv1alpha1.MetadataProviderConditionReady, metav1.ConditionUnknown, ReasonProviderNotImplemented, "no client exists for provider type %s yet", mp.Spec.Type))
  		return r.patch(ctx, &mp, conditions, nil, ctrl.Result{})
  	}
  	if err != nil {
  		k8s.MarkReady(&mp, &conditions, false, k8s.ReasonInvalidSpec, "%s", err.Error())
  		return r.patch(ctx, &mp, conditions, nil, ctrl.Result{})
  	}

  	result, probeErr := prober.Probe(ctx)
  	switch {
  	case probeErr == nil:
  		k8s.MarkTrue(&mp, &conditions, catalogv1alpha1.MetadataProviderConditionAuthenticated, k8s.ReasonReconciled, "credentials accepted")
  		k8s.MarkFalse(&mp, &conditions, catalogv1alpha1.MetadataProviderConditionThrottled, k8s.ReasonReconciled, "not throttled")
  		k8s.MarkReady(&mp, &conditions, true, k8s.ReasonReconciled, "reachable")
  	case isRateLimited(probeErr):
  		k8s.MarkTrue(&mp, &conditions, catalogv1alpha1.MetadataProviderConditionAuthenticated, k8s.ReasonReconciled, "credentials previously accepted")
  		k8s.MarkTrue(&mp, &conditions, catalogv1alpha1.MetadataProviderConditionThrottled, k8s.ReasonThrottled, "%s", probeErr.Error())
  		k8s.MarkReady(&mp, &conditions, false, k8s.ReasonThrottled, "%s", probeErr.Error())
  	case isAuthError(probeErr):
  		k8s.MarkFalse(&mp, &conditions, catalogv1alpha1.MetadataProviderConditionAuthenticated, "CredentialsRejected", "%s", probeErr.Error())
  		k8s.MarkReady(&mp, &conditions, false, "CredentialsRejected", "%s", probeErr.Error())
  		if r.Recorder != nil {
  			r.Recorder.Eventf(&mp, nil, "Warning", "CredentialsRejected", "Reconcile", probeErr.Error())
  		}
  	default:
  		k8s.MarkReady(&mp, &conditions, false, k8s.ReasonReconcileError, "%s", probeErr.Error())
  	}

  	_ = log // logging is used at the patch step below
  	return r.patch(ctx, &mp, conditions, result.QuotaRemaining, ctrl.Result{RequeueAfter: reprobeInterval})
  }

  func (r *Reconciler) patch(ctx context.Context, mp *catalogv1alpha1.MetadataProvider, conditions []metav1.Condition, quotaRemaining *int32, result ctrl.Result) (ctrl.Result, error) {
  	statusAC := catalogac.MetadataProviderStatus().
  		WithObservedGeneration(mp.Generation).
  		WithConditions(k8s.ConditionACs(conditions)...)
  	if quotaRemaining != nil {
  		statusAC = statusAC.WithQuotaRemaining(*quotaRemaining)
  	}
  	ac := catalogac.MetadataProvider(mp.Name, mp.Namespace).WithStatus(statusAC)
  	if _, err := k8s.PatchStatus(ctx, r.Client, k8s.ManagerCatalogarr, ac); err != nil {
  		logging.FromContext(ctx).Error("patch status", "error", err)
  		return ctrl.Result{}, err
  	}
  	return result, nil
  }

  // isRateLimited and isAuthError check against the pkg/metadata sentinels
  // this task verified exist (metadata.ErrRateLimited, metadata.RateLimitedError,
  // metadata.ErrAuth) -- write these with errors.Is/errors.As, not a helper
  // this task invented sight unseen; confirm the exact spelling with
  // `go doc ./pkg/metadata` before writing, same as prober_test.go's note.
  func isRateLimited(err error) bool { /* errors.Is(err, metadata.ErrRateLimited) || errors.As(err, new(*metadata.RateLimitedError)) */ return false }
  func isAuthError(err error) bool   { /* errors.Is(err, metadata.ErrAuth) */ return false }

  // +kubebuilder:rbac:groups=catalog.clustarr.io,resources=metadataproviders,verbs=get;list;watch
  // +kubebuilder:rbac:groups=catalog.clustarr.io,resources=metadataproviders/status,verbs=get;update;patch
  // +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
  // +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch
  func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
  	return ctrl.NewControllerManagedBy(mgr).
  		Named("metadataprovider").
  		For(&catalogv1alpha1.MetadataProvider{}, builder.WithPredicates(k8s.GenerationChanged())).
  		WithOptions(controller.Options{ReconciliationTimeout: 5 * time.Minute}).
  		Complete(r)
  }
  ```
  The two `isRateLimited`/`isAuthError` stubs above are written as stubs
  **in this plan only** because this task's research read the sentinel
  *names* (`metadata.ErrAuth`, `metadata.ErrRateLimited`,
  `metadata.RateLimitedError`) via `go doc` but did not read every client's
  actual wrapping call sites to confirm each one reliably wraps with
  `%w`. The implementer must replace both stub bodies with real
  `errors.Is`/`errors.As` checks before this step is done — ship code, not
  these stubs; the "expect compile failure" step below will not catch a
  logic bug hidden by a stub that always returns `false`, so also add:
  ```go
  func TestIsAuthErrorMatchesWrappedErrAuth(t *testing.T) {
  	wrapped := fmt.Errorf("tmdb: %w", metadata.ErrAuth)
  	if !isAuthError(wrapped) {
  		t.Error("isAuthError did not match a wrapped metadata.ErrAuth")
  	}
  }
  func TestIsRateLimitedMatchesRateLimitedError(t *testing.T) {
  	wrapped := fmt.Errorf("tmdb: %w", &metadata.RateLimitedError{Provider: "tmdb", RetryAfter: time.Minute})
  	if !isRateLimited(wrapped) {
  		t.Error("isRateLimited did not match a wrapped *metadata.RateLimitedError")
  	}
  }
  ```
  in `prober_test.go` (package `metadataprovider`, not `metadataprovider_test`,
  since `isRateLimited`/`isAuthError` are unexported) before this step's
  commit, and make them pass for real.

  Run: `KUBEBUILDER_ASSETS="$(setup-envtest use 1.37.0 -p path)" go test
  ./catalogarr/controller/metadataprovider/... -v` — expect every test in the
  package PASS, including the two new `isAuthError`/`isRateLimited` unit
  tests.

  Commit:
  ```bash
  git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m \
    "feat(catalogarr): metadataprovider controller" -- \
    catalogarr/controller/metadataprovider/controller.go catalogarr/controller/metadataprovider/prober_test.go catalogarr/controller/metadataprovider/controller_envtest_test.go
  ```

---

**For the wiring task (not this one — `catalogarr/run.go` is out of this
task's path ownership).** `setupControllers(mgr ctrl.Manager, o Options)
error` needs, once this task's packages exist:
```go
if err := rootfolder.NewReconciler(mgr.GetClient(), mgr.GetEventRecorderFor("rootfolder-controller")).SetupWithManager(mgr); err != nil {
    return fmt.Errorf("catalogarr: rootfolder controller: %w", err)
}
cat := catalogue.LoadedCatalogue()
if err := qualityprofile.NewReconciler(mgr.GetClient(), cat, mgr.GetEventRecorderFor("qualityprofile-controller")).SetupWithManager(mgr); err != nil {
    return fmt.Errorf("catalogarr: qualityprofile controller: %w", err)
}
if err := mgr.Add(&qualityprofile.Bootstrap{Client: mgr.GetClient(), Catalogue: cat}); err != nil {
    return fmt.Errorf("catalogarr: qualityprofile bootstrap: %w", err)
}
if err := delayprofile.NewReconciler(mgr.GetClient(), mgr.GetEventRecorderFor("delayprofile-controller")).SetupWithManager(mgr); err != nil {
    return fmt.Errorf("catalogarr: delayprofile controller: %w", err)
}
if err := metadataprovider.NewReconciler(mgr.GetClient(), mgr.GetEventRecorderFor("metadataprovider-controller"), http.DefaultClient).SetupWithManager(mgr); err != nil {
    return fmt.Errorf("catalogarr: metadataprovider controller: %w", err)
}
```
Two corrections needed to that snippet before it can be trusted verbatim: (1)
`mgr.GetEventRecorderFor` is the *deprecated* core-v1 recorder per
`docs/research/k8s.md` §5's verified note ("`GetEventRecorderFor` (core/v1
events) is deprecated") — this task's controllers all take
`k8s.io/client-go/tools/events.EventRecorder`, which `mgr.GetEventRecorder
(name string) recorder.EventRecorder` (no `For`, no object argument) returns,
not `GetEventRecorderFor`; the wiring task must call `mgr.GetEventRecorder
("rootfolder-controller")` etc., and (2) `RoleMetadata` runs in the
**separate** `catalogarr-metadata` Deployment (§3/§12) — the wiring task
decides there, not here, how that process gets its own `client.Client` and
calls `metadataprovider.BuildRegistry`; this task's `metadataprovider`
package registers a *controller* (which patches `MetadataProvider.status`)
in the `catalogarr` Deployment's `RoleController`, and separately exports
`BuildRegistry` for whoever wires `RoleMetadata` — the two are not the same
call site and must not be collapsed into one. After this task's four
`SetupWithManager` calls land, `setupControllers`'s `TODO(M1)` comment listing
`rootfolder, qualityprofile, delayprofile, metadataprovider` should drop those
four names (movie/series/episode/mediafile/search stay, they are other Phase
C tasks).

**Verification:**
```bash
go build ./catalogarr/...
KUBEBUILDER_ASSETS="$(setup-envtest use 1.37.0 -p path)" go test ./catalogarr/controller/... -v
go vet ./catalogarr/...
golangci-lint-v2 run ./catalogarr/...
```
Every `*_envtest_test.go` needs `KUBEBUILDER_ASSETS` set (via `setup-envtest
use 1.37.0 -p path`, matching `Makefile`'s `ENVTEST_K8S_VERSION`) or it skips
silently — confirm by checking the `go test -v` output names each
`TestReconcile*`/`TestSeedBuiltins*`/`TestBuildRegistry*` test as `PASS` with
a non-trivial duration (envtest starts a real, if minimal, apiserver — each
such test takes on the order of hundreds of milliseconds to low seconds, not
microseconds); a `--- SKIP` line or an entire suite finishing in under 50ms
means the assets were not found and nothing was proved.
`go test ./catalogarr/...` with `KUBEBUILDER_ASSETS` unset must still pass
(every envtest file skips cleanly rather than failing) per the shared
context's rule 4.

**Done when:**
- [ ] `catalogarr/controller/{rootfolder,qualityprofile,delayprofile,
      metadataprovider}/` exist with a `Reconciler`, `NewReconciler` and
      `SetupWithManager` each; `qualityprofile` additionally has
      `SeedBuiltins`/`Bootstrap`; `metadataprovider` additionally has
      `Prober`/`NewProber` and `BuildRegistry`; `delayprofile` additionally
      has the pure, exported `Resolve`.
- [ ] No `catalogarr/controller/importexclusion/` directory exists anywhere
      in the tree this task produced.
- [ ] Every status write in all four packages goes through
      `k8s.PatchStatus(..., k8s.ManagerCatalogarr, ...)` — `grep -rn
      "Status().Update\|Status().Patch" catalogarr/controller/` returns
      nothing (and `golangci-lint-v2 run` would fail the build if it did,
      per `.golangci.yml`'s forbidigo rule).
- [ ] `go test ./catalogarr/controller/... -v` with `KUBEBUILDER_ASSETS` set
      passes every test, each envtest test taking a real, non-trivial
      duration; the same command with `KUBEBUILDER_ASSETS` unset passes with
      every envtest test reported skipped.
- [ ] `delayprofile.Resolve`'s eight test cases (Step 1) all pass without
      `KUBEBUILDER_ASSETS` — it is a pure function, proven by the fact its
      tests do not need envtest at all.
- [ ] `qualityprofile.SeedBuiltins` creates exactly 13 `QualityProfile`
      objects, is a no-op on a second call with nothing changed, and
      delete-recreates one whose stored seed-hash annotation disagrees with
      the freshly resolved hash.
- [ ] `metadataprovider.NewProber`/`BuildRegistry` handle all six Phase-B-
      implemented provider types (`tmdb tvdb musicbrainz openlibrary
      comicvine audnexus`) and return `ErrProviderNotImplemented` (never a
      crash, never a silent success) for the other eight.
- [ ] `make manifests` (from repo root, after this task's code exists) picks
      up the new `+kubebuilder:rbac` markers under `catalogarr/...` without
      any change to `Makefile` — `RBAC_DIRS` already includes `catalogarr`.
- [ ] `go build ./catalogarr/...`, `go vet ./catalogarr/...` and
      `golangci-lint-v2 run ./catalogarr/...` are clean.
- [ ] This document's "For the wiring task" section is enough, on its own,
      for someone editing `catalogarr/run.go` to add the four
      `SetupWithManager` calls and the `qualityprofile.Bootstrap` `mgr.Add`
      correctly, without re-deriving `mgr.GetEventRecorder`'s correct name
      or re-discovering the `RoleMetadata`/`RoleController` process split.

---

### Task C5: the catalogarr metadata gateway

**Files:**
- Create: `catalogarr/metadata/settle.go`
- Create: `catalogarr/metadata/settle_test.go`
- Create: `catalogarr/metadata/cache.go`
- Create: `catalogarr/metadata/cache_test.go`
- Create: `catalogarr/metadata/tieredcache.go`
- Create: `catalogarr/metadata/tieredcache_test.go`
- Create: `catalogarr/metadata/limiter.go`
- Create: `catalogarr/metadata/limiter_test.go`
- Create: `catalogarr/metadata/target.go`
- Create: `catalogarr/metadata/target_test.go`
- Create: `catalogarr/metadata/patch.go`
- Create: `catalogarr/metadata/patch_test.go`
- Create: `catalogarr/metadata/registry.go`
- Create: `catalogarr/metadata/registry_test.go`
- Create: `catalogarr/metadata/worker.go`
- Create: `catalogarr/metadata/worker_envtest_test.go`
- Create: `catalogarr/metadata/rpc.go`
- Create: `catalogarr/metadata/rpc_test.go`
- Create: `catalogarr/metadata/gateway.go`
- Create: `catalogarr/metadata/gateway_envtest_test.go`
- Modify: `pkg/obs/metrics/domain.go` (append one new var block; see "Judgment calls" below — this is the one file outside this task's path ownership it must touch, because `newCounterVec` is unexported to `pkg/obs/metrics`)

**Path ownership:** `catalogarr/metadata/` — every file under it, this task only. The single exception is the append-only addition to `pkg/obs/metrics/domain.go` noted above and justified below; nothing else outside `catalogarr/metadata/` is touched, and `catalogarr/run.go` (another Phase C task's path — it currently has an empty `setupWorkers` with a `TODO(M1): ... the metadata gateway (RoleMetadata) ...` comment) is left alone. This task produces `metadata.Setup`, the function that task is expected to call.

**Read first:**
- Spec §5 (`docs/superpowers/specs/2026-09-18-clustarr-design.md:531-613`): streams/subjects/consumers/KV buckets table. In particular the `catalogarr-metadata` consumer row (line 589: `AckWait 60s, MaxDeliver 8, BackOff 30s,2m,10m,1h,6h, MaxAckPending 32`) and the `clustarr-metadata-cache` KV bucket row (line 609: `<provider>.<kind>.<id>` JSON with `expiresAt`, 30d bucket, "per-entry expiry checked by gateway").
- Spec §6.1 (line 617-618): catalogarr's roles, in particular "**Metadata gateway (1 replica):** `pkg/metadata` registry with all clients, `rate.Limiter` per provider, otter L1 + KV L2, TTL by entity state (Radarr/Sonarr refresh heuristics), serves `rpc.catalogarr.metadata.*`."
- Spec §8.1 (line 743): the Want flow — read this twice. The Movie/Series controller (a *different* task) publishes the task and sets `MetadataReady=False`; this task's gateway "patches `status.metadata` (SSA `catalogarr-worker`, only `status.metadata`)". You never touch `status.phase` or `status.conditions`.
- Spec §8.8 (line 757): failure handling vocabulary — `events.Retry(after)` → NakWithDelay, generic error → backoff nak, `events.Discard` → DLQ.
- Spec §10 (line 773-783): the cross-service contract table; note it does *not* list a metadata-gateway row, because the gateway has no watches, only the work-queue consumer and the RPC responder.
- ADR-0007 (`docs/adr/0007-single-replica-metadata-gateway.md`, read in full): why exactly one replica, why it owns every credential, cache and limiter, and why callers treat it as an asynchronous, failable dependency. This is the design rationale behind everything in this task.
- `docs/research/metadata.md` §4.5 "Aggregation, caching, rate limiting" (`grep -n '^#' docs/research/metadata.md` first) — background only; where it names a concrete Go shape (`Registry`, `Cache`, rate defaults), the **real merged Phase B code in `pkg/metadata` wins**, not the research note's earlier draft. See "Judgment calls" for the one place they diverge in a way that matters.
- `docs/research/queue.md` §5.2/5.5/5.6 for the consumer/DLQ/bridging *patterns* only — its concrete stream and consumer names (`CLUSTARR_WORK_METADATA`, `metadatarr-workers`) are an earlier draft superseded by the real `pkg/events` package (`StreamWorkCatalogarr`, `ConsumerCatalogMetadata`); use the real constants, not the note's names.
- Generated types, read in full: `api/catalog/v1alpha1/movie_types.go` (`MovieMetadata`, `MovieStatus`, `MovieSpec.TmdbID`), `api/catalog/v1alpha1/series_types.go` (`SeriesMetadata`, `SeriesStatus`, `SeriesSpec.TvdbID`), `api/catalog/v1alpha1/metadataprovider_types.go` (`MetadataProviderSpec`, `RateLimit`, `CacheTTL`, the three `MetadataSecretKey*` constants), `api/catalog/v1alpha1/shared_types.go` (`Image`, `ImageType` — only `poster`/`fanart`/`logo`, `AltTitle`).
- Generated apply configurations, read in full: `api/applyconfiguration/catalog/catalog/v1alpha1/moviestatus.go`, `moviemetadata.go`, `seriesstatus.go`, `seriesmetadata.go` — note the double `catalog/catalog` path segment (`CLAUDE.md`'s controller-gen v0.22.0 workaround). Every field has exactly one `WithX` builder; build through them, never a struct literal.
- `go doc -all ./pkg/metadata` in full (it is dense — this is the actual Phase B contract, not the research note). In particular: `Registry`, `Registry.Lookup`, `Cache`, `LRUCache`, `DefaultLimits`, `NewLimiter`, `RefreshTTL` + the `RefreshState*` constants, the `Err*` sentinels and `RateLimitedError`, `Movie`, `Series`, `ExternalIDs`, `Image`/`ImageType`, `Collection`.
- `go doc -all ./pkg/events` for `Bus`, `Subscription`, `Handler`, `Retry`, `Discard`, `KV`, `Requester`, the `Consumer*`/`Stream*`/`Filter*`/`RPCMetadata*`/`QueueGroupCatalogar` constants, `Topology.Consumer`, `ConsumerSpec.Subscription`.
- `go doc -all ./pkg/events/schema` for `MetadataTask`, `MetadataRequest`, `MetadataResponse`, `Ref`, `Decode`/`Encode`.
- `go doc ./pkg/metadata/clients/{tmdb,tvdb,musicbrainz,openlibrary,audnexus,comicvine}` for the six `New(...)` constructors — these are the only providers Phase B built; the other seven `MetadataProviderType` values have no client yet.
- `pkg/k8s/patch.go` in full (`PatchStatus`, `ApplyConfiguration`) and `pkg/k8s/fieldmanager.go` (`ManagerCatalogarrWorker = "catalogarr-worker"` already exists — you do not add it).
- `pkg/k8s/patch_envtest_test.go` in full — this is the exact envtest pattern (fixture builder, `newTestClient`, `managesField`/`fieldManagerNames` helpers) to mirror for this task's envtest files. Copy its `newTestClient`-style helper rather than inventing a new one.
- `pkg/events/contracttest/contracttest.go` lines 1-95 and 150-230 for the membus test pattern (`events.Default().ForSingleNode()`, `bus.Ensure`, `bus.Subscribe`, `envelope(...)` helper).
- `pkg/metadata/clients/tmdb/tmdb_test.go` in full, and `testdata/metadata/tmdb/movie_27205.json` — reuse this exact fixture (do not invent new TMDB JSON; golang-tmdb's response shape is easy to get subtly wrong, and this fixture is already verified against the real client). Known-good values from it: `Title` "Inception", `Runtime` 148, `IDs[metadata.KeyTMDB]` "27205", `IDs[metadata.KeyIMDb]` "tt1375666", `InCinemas` 2010-07-16 UTC, `Status` "released".
- `cmd/clustarr/start_envtest_test.go` only if you need another example of the `KUBEBUILDER_ASSETS`-skip pattern; `pkg/k8s/patch_envtest_test.go` already has it.

**Dependencies:** none new. Every package this task imports is already in `go.mod`: `github.com/hashicorp/golang-lru/v2 v2.0.7`, `github.com/jonboulle/clockwork v0.5.0`, `github.com/prometheus/client_golang v1.24.1`, `golang.org/x/time v0.16.0`, `go.opentelemetry.io/otel v1.46.0` (via `pkg/obs/tracing`), `k8s.io/apimachinery v0.37.0`, `k8s.io/client-go v0.37.0`, `sigs.k8s.io/controller-runtime v0.25.1`, `github.com/stretchr/testify v1.12.1`. Do not run `go get` or `go mod tidy`.

**Interfaces — Consumes** (verified with `go doc`, not guessed):
```go
// pkg/metadata
type Registry struct {
    Movies []MovieProvider; Series []SeriesProvider; Artists []ArtistProvider
    Books []BookProvider; Audiobooks []AudiobookProvider; Comics []ComicProvider
    Artwork []ArtworkProvider; Resolvers []IDResolver
}
func (r *Registry) Lookup(ctx context.Context, kind commonv1.MediaKind, ids ExternalIDs) (any, error)
type Cache interface {
    Get(ctx context.Context, key string, out any) (bool, error)
    Set(ctx context.Context, key string, v any, ttl time.Duration) error
}
func NewLRUCache(size int, clock clockwork.Clock) (*LRUCache, error)
func DefaultLimits() Limits
func NewLimiter(l rate.Limit, burst int) *rate.Limiter
func RefreshTTL(kind commonv1.MediaKind, state string, lastRefreshed time.Time) time.Duration
var ErrNotFound, ErrRateLimited, ErrAuth, ErrUnsupported, ErrDecode error
type RateLimitedError struct { Provider string; RetryAfter time.Duration }

// pkg/events
type Subscription struct { Stream, Durable string; Filters []string; AckWait time.Duration; MaxDeliver int; Backoff []time.Duration; MaxInFlight int; Heartbeat time.Duration }
type Handler func(ctx context.Context, m Message) error
func Retry(after time.Duration, err error) error
func Discard(reason string, err error) error
type Bus interface { Publisher; Subscriber; Requester; KV(bucket string) KV; Ensure(...) error; Close() error }
type Requester interface {
    Request(ctx context.Context, subject string, in, out any) error
    Serve(subject, queue string, h func(ctx context.Context, data []byte) ([]byte, error)) error
}
func Default() Topology  // ConsumerCatalogMetadata: AckWait 60s, MaxDeliver 8, BackOff [30s,2m,10m,1h,6h], MaxAckPending 32
func (t Topology) Consumer(name string) (ConsumerSpec, bool)
func (c ConsumerSpec) Subscription() Subscription
const StreamWorkCatalogarr, FilterCatalogMetadata, ConsumerCatalogMetadata = "CLUSTARR_WORK_CATALOGARR", "clustarr.work.catalogarr.metadata.>", "catalogarr-metadata"
const RPCMetadataLookup, RPCMetadataSearch, RPCMetadataResolve, QueueGroupCatalogar = "clustarr.rpc.catalogarr.metadata.lookup", "...search", "...resolve", "catalogarr"
const BucketMetadataCache = "clustarr-metadata-cache"

// pkg/events/schema
type MetadataTask struct { MediaRef commonv1.MediaRef; RefreshEpoch int64 }
type MetadataRequest struct { Kind commonv1.MediaKind; IDs map[string]string; Text string; Year int32; Region, Language string }
type MetadataResponse struct { Kind commonv1.MediaKind; Provider string; IDs map[string]string; Result []byte; Results [][]byte; CachedAt *time.Time; Error string }
func Decode(schema string, data []byte, out Payload) error

// pkg/k8s
func PatchStatus[T ApplyConfiguration](ctx, c client.Client, fm FieldManager, ac T, opts ...client.SubResourceApplyOption) (T, error)
const ManagerCatalogarrWorker FieldManager = "catalogarr-worker"  // already exists
```

**Interfaces — Produces** (what a later task — the movie/series controllers, and whichever task fills in `catalogarr/run.go`'s `setupWorkers` — relies on):
```go
package metadata // catalogarr/metadata

// Setup is RoleMetadata's entire job. catalogarr/run.go's setupWorkers,
// guarded by o.Role.Has(catalogarr.RoleMetadata), calls this once and keeps
// stop for shutdown. (reconciled by controller: the caller in
// catalogarr/run.go is a different Phase C task; this signature is the
// assumed shape it must match.)
type Options struct {
    Client     client.Client   // required
    Bus        events.Bus      // required
    HTTPClient *http.Client    // default http.DefaultClient
    L1Size     int             // default 4096
    Clock      clockwork.Clock // default clockwork.NewRealClock()
}
func Setup(ctx context.Context, o Options) (stop func(), err error)

// BuildRegistry and ServeRPC are exported separately so a test (or a future
// CLI diagnostic) can exercise them without the work-queue subscription.
func BuildRegistry(ctx context.Context, c client.Client, providers []catalogv1alpha1.MetadataProvider, httpClient *http.Client) (*pkgmetadata.Registry, error)
func ServeRPC(bus events.Requester, reg *pkgmetadata.Registry) error

type Handler struct {
    Client   client.Client
    Registry *pkgmetadata.Registry
    Cache    pkgmetadata.Cache
    Now      func() time.Time // nil = time.Now
}
func (h *Handler) Handle(ctx context.Context, m events.Message) error // implements events.Handler
```

**Interfaces — Produces, coordinated with Task C6 (episode listing).** Task C6 (Movie/Series/Episode
controllers) needs to list a series' episodes and found no dedicated subject for it — only the
generic lookup RPC. Rather than add a subject (out of both tasks' path ownership: `pkg/events`
subjects are Phase A/B, already merged), it rides the existing `clustarr.rpc.catalogarr.metadata.lookup`
subject with a reserved `Kind`. This task implements that verb; the two request/response shapes below
are the exact contract Task C6's section must also state, symbol for symbol:
```go
// Request: Kind selects the verb; IDs carries both the lookup key and the
// one extra parameter Episodes needs. There is no dedicated "order" key in
// pkg/metadata's Key* vocabulary (go doc ./pkg/metadata: KeyIMDb, KeyTMDB,
// KeyTVDB, ... — no season-order key, because order is not a crosswalk id).
// This reuses the same generic map MetadataRequest already carries rather
// than widening the schema for one caller.
schema.MetadataRequest{
    Kind: commonv1.MediaKindEpisode,
    IDs:  map[string]string{"tvdb": "<tvdbID>", "order": "<official|dvd|absolute|...>"},
    // "order" is a plain string, matching SeriesProvider.Episodes' own
    // parameter type (go doc ./pkg/metadata: Episodes(ctx, tvdbID, order
    // string) ([]Episode, error)) — not pkg/metadata's typed SeasonOrder,
    // which that method signature does not take either.
}

// Response: Results (not Result) holds one JSON-encoded pkg/metadata.Episode
// per entry, exactly like a search response's hits — MetadataResponse's own
// doc comment says Results "holds the hits of a search request", which this
// slightly repurposes (a listing, not a search) rather than adding a field;
// flagged here, not a blocking issue, since the type is structurally
// identical either way.
schema.MetadataResponse{
    Kind:     commonv1.MediaKindEpisode,
    Provider: "<name of the SeriesProvider that answered, e.g. \"tvdb\">",
    Results:  [][]byte{ /* one json.Marshal(pkgmetadata.Episode) per entry */ },
    Error:    "<set instead of Results on failure>",
}
```
Routing: `commonv1.MediaKindEpisode` is **not** one of `Registry.Lookup`'s cases (`go doc ./pkg/metadata`
Lookup's switch covers movie/series/artist/author/audiobook/comic only — "the first entity from the
first provider that succeeds" is the wrong shape for a list). This task's `lookup()` in `rpc.go`
special-cases `MediaKindEpisode` before calling `Registry.Lookup`, and calls
`SeriesProvider.Episodes(ctx, tvdbID, order)` directly over `reg.Series`, first-provider-that-succeeds
— see Step 15. No alternative shape was more correct once `Registry.Lookup`'s real signature was
read: the coordinator's proposed shape is adopted as-is, with the one caveat about `Results`' doc
comment noted above.

**Assumption about the producer side (a different task):** `MetadataTask.MediaRef` (`{Kind, Name, Keys}`, see `api/common/v1alpha1/media_types.go:41`) has no `Namespace` field. This task's `Handler.Handle` gets the namespace from `events.Envelope.Key`, documented in `pkg/events` as `"<namespace>/<name>"` of the owning CR, and the name from `MediaRef.Name` (trusting the payload over re-parsing `Key`, since `Key` is generic infrastructure and `MediaRef.Name` is the typed field). The producer (the Movie/Series controller task) must therefore publish with `Envelope.Key` set to the item's `"<namespace>/<name>"`, exactly as `schema.Ref.String()` already renders it (`pkg/events/schema/catalog.go`) — state this explicitly if that task's plan doesn't already.

**Judgment calls (spec/research-note/generated-type disagreements, resolved):**
1. **pkg/obs/metrics touch.** The task brief and spec §13 require `clustarr_metadata_cache_hits_total{tier}`. `pkg/obs/metrics`'s `newCounterVec`/`newGaugeVec`/`newHistogramVec` helpers are unexported to that package (`pkg/obs/metrics/metrics.go`), so there is no way to add a new series from `catalogarr/metadata/` without either bypassing the package's registration/cardinality-guard convention or editing `pkg/obs/metrics/domain.go`. This task edits it, appending one new `var (...)` block at the end of the file, touching no existing line. Flag this to whoever assembles the full Phase C plan: other tasks (search, transcode, subtitle workers) may hit the same wall for their own §13 series and also need to append to this file — a real cross-task collision point on one shared file that this document cannot resolve by itself.
2. **Cache key drops the provider segment.** §5's KV table documents the `clustarr-metadata-cache` key as `<provider>.<kind>.<id>`. But `Registry.Lookup` (the real, merged `pkg/metadata` code) is a first-provider-that-succeeds call: the caller does not know which provider will answer *before* calling it, so a pre-fetch cache lookup cannot be keyed by provider. This task uses `<kind>:<key>=<id>[,<key>=<id>...]` (built by `cacheKey`, sorted by `ExternalIDs` key) instead — an item-level key, not a provider-level one. This is what `RefreshTTL`'s "a fresh item is not refetched" actually needs; a provider-qualified key would make a request that failed on provider A and later succeeds on provider B miss the cache for no benefit.
3. **tmdb is Movies-only, not Series-too.** `docs/research/metadata.md` §4.5 lists Series providers as `[tvdb, tmdb]` ("tmdb for backdrops/keywords"). The real, already-merged `pkg/metadata/clients/tmdb` package doc says it "wraps ... as a metadata.MovieProvider" and implements no `SeriesProvider` method. `BuildRegistry` follows the merged code: `tmdb.Client` only ever populates `Registry.Movies`.
4. **Image type is narrowed, not passed through.** `pkg/metadata.ImageType` has nine values (poster, fanart, banner, logo, clearart, thumb, screenshot, disc, headshot); the generated `catalogv1alpha1.ImageType` CRD enum allows only `poster;fanart;logo` (`api/catalog/v1alpha1/shared_types.go:51`). Per "the generated type wins", `mapImageType` passes through the three that match and drops the rest — a wrong label is worse than a missing image.
5. **M1 kind scope.** `Handler`'s CR-fetch/patch path (`newTarget`, `externalIDs`, `buildMovieMetadataAC`/`buildSeriesMetadataAC`) only wires `Movie` and `Series`. This matches `catalogarr/run.go`'s own `setupControllers` comment (`TODO(M1): movie, series, ...` / `TODO(M6): artist, album, author, book, audiobook, comic, issue.`) and CLAUDE.md's milestone plan. The **RPC** path (`lookup`/`search`/`resolve` in `rpc.go`) is not CR-shaped at all — it returns opaque JSON — so it supports every kind `Registry` already supports (movie, series, artist, album, book, audiobook, comic) with no extra work; only the work-queue/CR side is scoped to M1.
6. **Rate-limited vs not-found vs auth.** The task brief says "handles the rate-limited and not-found cases as retry-with-delay versus terminal" but doesn't name every `pkg/metadata` sentinel. This task's `settlement` maps: `RateLimitedError`/`ErrRateLimited` → `events.Retry` (honouring `RetryAfter` when present, else a 30s default); `ErrNotFound` → `events.Discard` (the item genuinely has no such external id; retrying every attempt wastes the provider's quota for nothing); `ErrAuth` → `events.Discard` (a bad credential does not fix itself by retrying; the `MetadataProvider` controller, a different task, is expected to reflect `Authenticated=False` from the same failure independently); anything else (`ErrDecode`, `ErrUnsupported`, a bare transport error) → returned unwrapped, so the bus applies the subscription's own backoff schedule.

---

- [ ] **Step 1: error settlement is a pure function.** Write the failing test first.

  `catalogarr/metadata/settle_test.go`:
  ```go
  package metadata

  import (
      "errors"
      "testing"
      "time"

      "github.com/stretchr/testify/require"

      "github.com/mediactl/clustarr/pkg/events"
      pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
  )

  func TestSettlementMapsProviderErrorsToBusActions(t *testing.T) {
      t.Run("nil is nil", func(t *testing.T) {
          require.NoError(t, settlement(nil))
      })
      t.Run("rate limited with a Retry-After becomes Retry honouring it", func(t *testing.T) {
          err := settlement(&pkgmetadata.RateLimitedError{Provider: "tmdb", RetryAfter: 90 * time.Second})
          var re *events.RetryError
          require.ErrorAs(t, err, &re)
          require.Equal(t, 90*time.Second, re.After)
      })
      t.Run("bare ErrRateLimited becomes Retry with the default delay", func(t *testing.T) {
          err := settlement(pkgmetadata.ErrRateLimited)
          var re *events.RetryError
          require.ErrorAs(t, err, &re)
          require.Equal(t, 30*time.Second, re.After)
      })
      t.Run("not found is terminal", func(t *testing.T) {
          err := settlement(pkgmetadata.ErrNotFound)
          var de *events.DiscardError
          require.ErrorAs(t, err, &de)
      })
      t.Run("auth failure is terminal", func(t *testing.T) {
          err := settlement(pkgmetadata.ErrAuth)
          var de *events.DiscardError
          require.ErrorAs(t, err, &de)
      })
      t.Run("an unmapped error passes through for the subscription backoff", func(t *testing.T) {
          want := errors.New("boom")
          require.Same(t, want, settlement(want))
      })
  }
  ```
  Run `go test ./catalogarr/metadata/... -run TestSettlement` — expect a build failure (`settlement` undefined). Implement `catalogarr/metadata/settle.go`:
  ```go
  package metadata

  import (
      "errors"
      "time"

      "github.com/mediactl/clustarr/pkg/events"
      pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
  )

  // defaultRetryAfter is used when a provider's rate-limit error carries no
  // Retry-After hint (ErrRateLimited bare, not wrapped in RateLimitedError).
  const defaultRetryAfter = 30 * time.Second

  // settlement maps a pkg/metadata provider error onto the events.Handler
  // vocabulary §8.8 defines: Retry for a transient, provider-told-us-when
  // condition; Discard for one no amount of retrying fixes; anything else
  // passes through so the subscription's own backoff schedule
  // (catalogarr-metadata: 30s,2m,10m,1h,6h) applies.
  func settlement(err error) error {
      if err == nil {
          return nil
      }
      var rl *pkgmetadata.RateLimitedError
      if errors.As(err, &rl) {
          after := rl.RetryAfter
          if after <= 0 {
              after = defaultRetryAfter
          }
          return events.Retry(after, err)
      }
      if errors.Is(err, pkgmetadata.ErrRateLimited) {
          return events.Retry(defaultRetryAfter, err)
      }
      if errors.Is(err, pkgmetadata.ErrNotFound) {
          return events.Discard("metadata: no such record at the provider", err)
      }
      if errors.Is(err, pkgmetadata.ErrAuth) {
          return events.Discard("metadata: provider rejected credentials", err)
      }
      return err
  }
  ```
  Run `go test ./catalogarr/metadata/... -run TestSettlement -v` — expect PASS.
  Commit: `git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "feat(catalogarr/metadata): map provider errors to bus settlement" -- catalogarr/metadata/settle.go catalogarr/metadata/settle_test.go`

- [ ] **Step 2: the KV-backed L2 cache.** Failing test first, against `membus` (no network, no apiserver).

  `catalogarr/metadata/cache_test.go`:
  ```go
  package metadata

  import (
      "context"
      "testing"
      "time"

      "github.com/jonboulle/clockwork"
      "github.com/stretchr/testify/require"

      "github.com/mediactl/clustarr/pkg/events"
      "github.com/mediactl/clustarr/pkg/events/membus"
  )

  type cacheFixture struct {
      Title string `json:"title"`
  }

  func newTestKV(t *testing.T, clock clockwork.Clock) events.KV {
      t.Helper()
      bus := membus.New(clock)
      t.Cleanup(func() { _ = bus.Close() })
      ctx := context.Background()
      topo := events.Topology{Buckets: []events.BucketSpec{{Name: events.BucketMetadataCache, TTL: 30 * 24 * time.Hour}}}
      require.NoError(t, bus.Ensure(ctx, topo))
      return bus.KV(events.BucketMetadataCache)
  }

  func TestKVCacheMissThenHitThenExpiry(t *testing.T) {
      clock := clockwork.NewFakeClock()
      c := newKVCache(newTestKV(t, clock), clock)
      ctx := context.Background()

      var out cacheFixture
      hit, err := c.Get(ctx, "movie:tmdb=27205", &out)
      require.NoError(t, err)
      require.False(t, hit, "empty bucket must miss")

      require.NoError(t, c.Set(ctx, "movie:tmdb=27205", cacheFixture{Title: "Inception"}, time.Hour))

      hit, err = c.Get(ctx, "movie:tmdb=27205", &out)
      require.NoError(t, err)
      require.True(t, hit)
      require.Equal(t, "Inception", out.Title)

      clock.Advance(2 * time.Hour)
      hit, err = c.Get(ctx, "movie:tmdb=27205", &out)
      require.NoError(t, err)
      require.False(t, hit, "an entry past its own expiresAt must miss even though the bucket TTL (30d) has not elapsed")
  }
  ```
  Run `go test ./catalogarr/metadata/... -run TestKVCache` — expect a build failure (`newKVCache` undefined). Implement `catalogarr/metadata/cache.go`:
  ```go
  package metadata

  import (
      "context"
      "encoding/json"
      "errors"
      "fmt"
      "time"

      "github.com/jonboulle/clockwork"

      "github.com/mediactl/clustarr/pkg/events"
      pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
  )

  // kvEntry is what kvCache stores in the bucket. The bucket's own 30d TTL
  // (events.Default()'s BucketMetadataCache) is a hard backstop; per-entry
  // freshness is decided here by ExpiresAt, exactly as §5 documents ("per-entry
  // expiry checked by gateway").
  type kvEntry struct {
      Value     json.RawMessage `json:"value"`
      ExpiresAt time.Time       `json:"expiresAt"`
  }

  // kvCache is the L2 tier: a metadata.Cache backed by the shared
  // clustarr-metadata-cache KV bucket, so a restarted gateway (there is only
  // ever one replica, ADR-0007) does not lose every cached answer.
  type kvCache struct {
      kv    events.KV
      clock clockwork.Clock
  }

  func newKVCache(kv events.KV, clock clockwork.Clock) *kvCache {
      return &kvCache{kv: kv, clock: clock}
  }

  func (c *kvCache) Get(ctx context.Context, key string, out any) (bool, error) {
      entry, err := c.kv.Get(ctx, key)
      if err != nil {
          if errors.Is(err, events.ErrKeyNotFound) {
              return false, nil
          }
          return false, fmt.Errorf("metadata: get cache key %q: %w", key, err)
      }
      var e kvEntry
      if err := json.Unmarshal(entry.Value, &e); err != nil {
          return false, fmt.Errorf("metadata: decode cache entry %q: %w", key, err)
      }
      if !c.clock.Now().Before(e.ExpiresAt) {
          return false, nil
      }
      if err := json.Unmarshal(e.Value, out); err != nil {
          return false, fmt.Errorf("metadata: decode cached value %q: %w", key, err)
      }
      return true, nil
  }

  func (c *kvCache) Set(ctx context.Context, key string, v any, ttl time.Duration) error {
      raw, err := json.Marshal(v)
      if err != nil {
          return fmt.Errorf("metadata: encode %q for cache: %w", key, err)
      }
      data, err := json.Marshal(kvEntry{Value: raw, ExpiresAt: c.clock.Now().Add(ttl)})
      if err != nil {
          return fmt.Errorf("metadata: encode cache entry %q: %w", key, err)
      }
      _, err = c.kv.Put(ctx, key, data)
      return err
  }

  var _ pkgmetadata.Cache = (*kvCache)(nil)
  ```
  Run `go test ./catalogarr/metadata/... -run TestKVCache -v` — expect PASS.
  Commit: `git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "feat(catalogarr/metadata): KV-backed L2 metadata cache" -- catalogarr/metadata/cache.go catalogarr/metadata/cache_test.go`

- [ ] **Step 3: the two-tier cache and its metric.** First add the metric series (append-only) to `pkg/obs/metrics/domain.go`, at the end of the file:
  ```go
  // Metadata telemetry, used by catalogarr's metadata gateway (ADR-0007).
  var (
      // MetadataCacheHitsTotal counts metadata cache lookups, by tier
      // (l1, l2, miss). §13.
      MetadataCacheHitsTotal = newCounterVec(
          "clustarr_metadata_cache_hits_total",
          "Total metadata cache lookups, by tier (l1, l2, miss).",
          "tier",
      )
  )
  ```
  Run `go test ./pkg/obs/metrics/...` — expect PASS already (this alone is not yet exercised by a failing test; `TestNoMetricIsLabelledByAnUnboundedDimension` in that package must still pass with the new series, confirming `tier` is an accepted bounded label).

  Now the failing test for the cache itself, `catalogarr/metadata/tieredcache_test.go`:
  ```go
  package metadata

  import (
      "context"
      "testing"
      "time"

      "github.com/jonboulle/clockwork"
      "github.com/prometheus/client_golang/prometheus/testutil"
      "github.com/stretchr/testify/require"

      "github.com/mediactl/clustarr/pkg/obs/metrics"
  )

  func TestTieredCachePrefersL1ThenBackfillsFromL2(t *testing.T) {
      clock := clockwork.NewFakeClock()
      ctx := context.Background()

      l1, err := newTestLRU(t, clock)
      require.NoError(t, err)
      l2 := newKVCache(newTestKV(t, clock), clock)
      tc := newTieredCache(l1, l2)

      before := testutil.ToFloat64(metrics.MetadataCacheHitsTotal.WithLabelValues("miss"))
      var out cacheFixture
      hit, err := tc.Get(ctx, "movie:tmdb=27205", &out)
      require.NoError(t, err)
      require.False(t, hit)
      require.Equal(t, before+1, testutil.ToFloat64(metrics.MetadataCacheHitsTotal.WithLabelValues("miss")))

      require.NoError(t, tc.Set(ctx, "movie:tmdb=27205", cacheFixture{Title: "Inception"}, time.Hour))

      // L1 hit: direct write above put it in both tiers.
      beforeL1 := testutil.ToFloat64(metrics.MetadataCacheHitsTotal.WithLabelValues("l1"))
      hit, err = tc.Get(ctx, "movie:tmdb=27205", &out)
      require.NoError(t, err)
      require.True(t, hit)
      require.Equal(t, "Inception", out.Title)
      require.Equal(t, beforeL1+1, testutil.ToFloat64(metrics.MetadataCacheHitsTotal.WithLabelValues("l1")))

      // Simulate an L1 eviction (process restart): only L2 has it. Get must
      // still succeed, be counted as l2, and backfill L1.
      l1Only, err := newTestLRU(t, clock)
      require.NoError(t, err)
      tc2 := newTieredCache(l1Only, l2)
      beforeL2 := testutil.ToFloat64(metrics.MetadataCacheHitsTotal.WithLabelValues("l2"))
      hit, err = tc2.Get(ctx, "movie:tmdb=27205", &out)
      require.NoError(t, err)
      require.True(t, hit)
      require.Equal(t, beforeL2+1, testutil.ToFloat64(metrics.MetadataCacheHitsTotal.WithLabelValues("l2")))
  }
  ```
  Add a small `newTestLRU` helper alongside `newTestKV` in `cache_test.go` (`pkgmetadata.NewLRUCache(64, clock)`, `require.NoError`). Run `go test ./catalogarr/metadata/... -run TestTieredCache` — expect a build failure (`newTieredCache` undefined). Implement `catalogarr/metadata/tieredcache.go`:
  ```go
  package metadata

  import (
      "context"
      "time"

      pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
      "github.com/mediactl/clustarr/pkg/obs/metrics"
  )

  // tieredCache is metadata.Cache over the two tiers §6.1 describes: an
  // in-process LRU in front of the shared KV bucket. ADR-0007 pins the
  // gateway to one replica, so "L1" and "the process cache" are the same
  // thing; L2 is what survives a restart.
  type tieredCache struct {
      l1 pkgmetadata.Cache
      l2 pkgmetadata.Cache
  }

  func newTieredCache(l1, l2 pkgmetadata.Cache) *tieredCache {
      return &tieredCache{l1: l1, l2: l2}
  }

  func (c *tieredCache) Get(ctx context.Context, key string, out any) (bool, error) {
      hit, err := c.l1.Get(ctx, key, out)
      if err != nil {
          return false, err
      }
      if hit {
          metrics.MetadataCacheHitsTotal.WithLabelValues("l1").Inc()
          return true, nil
      }
      hit, err = c.l2.Get(ctx, key, out)
      if err != nil {
          return false, err
      }
      if !hit {
          metrics.MetadataCacheHitsTotal.WithLabelValues("miss").Inc()
          return false, nil
      }
      metrics.MetadataCacheHitsTotal.WithLabelValues("l2").Inc()
      // Backfill L1 so the next request in this process skips the KV round
      // trip. A backfill failure must not fail the read that just succeeded.
      _ = c.l1.Set(ctx, key, out, time.Hour)
      return true, nil
  }

  func (c *tieredCache) Set(ctx context.Context, key string, v any, ttl time.Duration) error {
      if err := c.l2.Set(ctx, key, v, ttl); err != nil {
          return err
      }
      return c.l1.Set(ctx, key, v, ttl)
  }

  var _ pkgmetadata.Cache = (*tieredCache)(nil)
  ```
  Run `go test ./catalogarr/metadata/... -run TestTieredCache -v` and `go test ./pkg/obs/metrics/...` — expect both PASS.
  Commit: `git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "feat(catalogarr/metadata): two-tier cache with the cache-hit metric" -- catalogarr/metadata/tieredcache.go catalogarr/metadata/tieredcache_test.go catalogarr/metadata/cache_test.go pkg/obs/metrics/domain.go`

- [ ] **Step 4: per-provider rate limiter resolution.** Failing test first.

  `catalogarr/metadata/limiter_test.go`:
  ```go
  package metadata

  import (
      "testing"

      "github.com/stretchr/testify/require"
      "k8s.io/apimachinery/pkg/api/resource"

      catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
      pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
  )

  func TestResolveLimiterUsesThePackageFloorByDefault(t *testing.T) {
      l := resolveLimiter(catalogv1alpha1.MetadataProviderTMDB, nil)
      d := pkgmetadata.DefaultLimits()
      require.Equal(t, d.TMDB, l.Limit())
      require.Equal(t, d.TMDBBurst, l.Burst())
  }

  func TestResolveLimiterHonoursTheCRDOverride(t *testing.T) {
      q := resource.MustParse("2.5")
      l := resolveLimiter(catalogv1alpha1.MetadataProviderTMDB, &catalogv1alpha1.RateLimit{
          RequestsPerSecond: &q,
          Burst:             10,
      })
      require.InDelta(t, 2.5, float64(l.Limit()), 0.001)
      require.Equal(t, 10, l.Burst())
  }

  func TestResolveLimiterFallsBackForAnUnimplementedType(t *testing.T) {
      l := resolveLimiter(catalogv1alpha1.MetadataProviderFanart, nil)
      require.Equal(t, 1, l.Burst())
  }
  ```
  Run `go test ./catalogarr/metadata/... -run TestResolveLimiter` — expect a build failure. Implement `catalogarr/metadata/limiter.go`:
  ```go
  package metadata

  import (
      "golang.org/x/time/rate"

      catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
      pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
  )

  // resolveLimiter builds the token-bucket limiter for one provider: the
  // CRD's spec.rateLimit when set, else pkg/metadata's documented floor
  // (DefaultLimits, docs/research/metadata.md §4.5). CLAUDE.md: RateLimit
  // uses resource.Quantity, never a float, in the CRD.
  func resolveLimiter(t catalogv1alpha1.MetadataProviderType, spec *catalogv1alpha1.RateLimit) *rate.Limiter {
      if spec != nil && spec.RequestsPerSecond != nil {
          q := spec.RequestsPerSecond.DeepCopy()
          burst := int(spec.Burst)
          if burst <= 0 {
              burst = 1
          }
          return pkgmetadata.NewLimiter(rate.Limit(q.AsApproximateFloat64()), burst)
      }
      d := pkgmetadata.DefaultLimits()
      switch t {
      case catalogv1alpha1.MetadataProviderTMDB:
          return pkgmetadata.NewLimiter(d.TMDB, d.TMDBBurst)
      case catalogv1alpha1.MetadataProviderTVDB:
          return pkgmetadata.NewLimiter(d.TVDB, d.TVDBBurst)
      case catalogv1alpha1.MetadataProviderMusicBrainz:
          return pkgmetadata.NewLimiter(d.MusicBrainz, d.MusicBrainzBurst)
      case catalogv1alpha1.MetadataProviderOpenLibrary:
          return pkgmetadata.NewLimiter(d.OpenLibrary, d.OpenLibraryBurst)
      case catalogv1alpha1.MetadataProviderComicVine:
          return pkgmetadata.NewLimiter(d.ComicVine, d.ComicVineBurst)
      case catalogv1alpha1.MetadataProviderAudnexus:
          return pkgmetadata.NewLimiter(d.Audnexus, d.AudnexusBurst)
      default:
          // fanart, hardcover, metron, mangadex, anilist, kitsu, animelists:
          // no Phase B client yet (see registry.go), so no documented floor
          // either. One request per second is a deliberately conservative
          // placeholder for the day a client lands.
          return pkgmetadata.NewLimiter(rate.Limit(1), 1)
      }
  }
  ```
  Run `go test ./catalogarr/metadata/... -run TestResolveLimiter -v` — expect PASS.
  Commit: `git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "feat(catalogarr/metadata): per-provider rate limiter resolution" -- catalogarr/metadata/limiter.go catalogarr/metadata/limiter_test.go`

- [ ] **Step 5: target dispatch — which CR, which external id.** Failing test first, using the controller-runtime fake client (no apiserver needed for a plain `Get`).

  `catalogarr/metadata/target_test.go`:
  ```go
  package metadata

  import (
      "errors"
      "testing"
      "time"

      "github.com/stretchr/testify/require"
      metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

      catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
      commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
      pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
  )

  func TestNewTargetDispatchesMovieAndSeriesOnly(t *testing.T) {
      m, err := newTarget(commonv1.MediaKindMovie)
      require.NoError(t, err)
      require.IsType(t, &catalogv1alpha1.Movie{}, m)

      s, err := newTarget(commonv1.MediaKindSeries)
      require.NoError(t, err)
      require.IsType(t, &catalogv1alpha1.Series{}, s)

      _, err = newTarget(commonv1.MediaKindArtist)
      require.True(t, errors.Is(err, errUnsupportedKind), "artist is M6 scope, see run.go's own TODO(M6)")
  }

  func TestExternalIDsReadsTheSpecID(t *testing.T) {
      movie := &catalogv1alpha1.Movie{Spec: catalogv1alpha1.MovieSpec{TmdbID: 27205}}
      ids, err := externalIDs(movie)
      require.NoError(t, err)
      require.Equal(t, pkgmetadata.ExternalIDs{pkgmetadata.KeyTMDB: "27205"}, ids)

      series := &catalogv1alpha1.Series{Spec: catalogv1alpha1.SeriesSpec{TvdbID: 121361}}
      ids, err = externalIDs(series)
      require.NoError(t, err)
      require.Equal(t, pkgmetadata.ExternalIDs{pkgmetadata.KeyTVDB: "121361"}, ids)
  }

  func TestRefreshedAtIsZeroBeforeTheFirstFetch(t *testing.T) {
      require.True(t, refreshedAt(&catalogv1alpha1.Movie{}).IsZero())

      when := metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
      m := &catalogv1alpha1.Movie{Status: catalogv1alpha1.MovieStatus{
          Metadata: &catalogv1alpha1.MovieMetadata{RefreshedAt: when},
      }}
      require.True(t, refreshedAt(m).Equal(when.Time))
  }
  ```
  Run `go test ./catalogarr/metadata/... -run 'TestNewTarget|TestExternalIDs|TestRefreshedAt'` — expect a build failure. Implement `catalogarr/metadata/target.go`:
  ```go
  package metadata

  import (
      "errors"
      "fmt"
      "strconv"
      "time"

      "sigs.k8s.io/controller-runtime/pkg/client"

      catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
      commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
      pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
  )

  // errUnsupportedKind marks a MediaKind this M1 worker does not yet fetch
  // metadata for. catalogarr/run.go's own setupControllers comment carries
  // the matching TODO(M6): artist, album, author, book, audiobook, comic,
  // issue — this switch grows a case, not a redesign, when that lands.
  var errUnsupportedKind = errors.New("metadata: unsupported media kind for the metadata worker")

  // newTarget returns a zero-value object of the concrete type kind names,
  // ready for client.Get.
  func newTarget(kind commonv1.MediaKind) (client.Object, error) {
      switch kind {
      case commonv1.MediaKindMovie:
          return &catalogv1alpha1.Movie{}, nil
      case commonv1.MediaKindSeries:
          return &catalogv1alpha1.Series{}, nil
      default:
          return nil, fmt.Errorf("%w: %q", errUnsupportedKind, kind)
      }
  }

  // externalIDs extracts the id the registry looks a target up by, from its
  // spec (§4.2: Movie carries a TMDB id, Series a TVDB id).
  func externalIDs(obj client.Object) (pkgmetadata.ExternalIDs, error) {
      switch o := obj.(type) {
      case *catalogv1alpha1.Movie:
          return pkgmetadata.ExternalIDs{pkgmetadata.KeyTMDB: strconv.FormatInt(o.Spec.TmdbID, 10)}, nil
      case *catalogv1alpha1.Series:
          return pkgmetadata.ExternalIDs{pkgmetadata.KeyTVDB: strconv.FormatInt(o.Spec.TvdbID, 10)}, nil
      default:
          return nil, fmt.Errorf("%w: %T", errUnsupportedKind, obj)
      }
  }

  // refreshedAt returns the target's previous status.metadata.refreshedAt, or
  // the zero time when it has never been fetched — the "lastRefreshed"
  // pkg/metadata.RefreshTTL needs.
  func refreshedAt(obj client.Object) time.Time {
      switch o := obj.(type) {
      case *catalogv1alpha1.Movie:
          if o.Status.Metadata != nil {
              return o.Status.Metadata.RefreshedAt.Time
          }
      case *catalogv1alpha1.Series:
          if o.Status.Metadata != nil {
              return o.Status.Metadata.RefreshedAt.Time
          }
      }
      return time.Time{}
  }
  ```
  Run `go test ./catalogarr/metadata/... -run 'TestNewTarget|TestExternalIDs|TestRefreshedAt' -v` — expect PASS.
  Commit: `git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "feat(catalogarr/metadata): Movie/Series target dispatch" -- catalogarr/metadata/target.go catalogarr/metadata/target_test.go`

- [ ] **Step 6: refresh-state derivation.** Failing test first (pure functions, no I/O).

  Append to `catalogarr/metadata/target_test.go`:
  ```go
  func TestMovieRefreshState(t *testing.T) {
      now := time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)
      recent := now.Add(-10 * 24 * time.Hour)
      old := now.Add(-400 * 24 * time.Hour)

      require.Equal(t, pkgmetadata.RefreshStateAnnounced, movieRefreshState(&pkgmetadata.Movie{Status: pkgmetadata.MovieStatusAnnounced}, now))
      require.Equal(t, pkgmetadata.RefreshStateInCinemas, movieRefreshState(&pkgmetadata.Movie{Status: pkgmetadata.MovieStatusInCinemas}, now))
      require.Equal(t, pkgmetadata.RefreshStateReleasedRecent,
          movieRefreshState(&pkgmetadata.Movie{Status: pkgmetadata.MovieStatusReleased, DigitalRelease: &recent}, now))
      require.Equal(t, pkgmetadata.RefreshStateReleasedOld,
          movieRefreshState(&pkgmetadata.Movie{Status: pkgmetadata.MovieStatusReleased, DigitalRelease: &old}, now))
  }

  func TestSeriesRefreshState(t *testing.T) {
      now := time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)
      recent := now.Add(-10 * 24 * time.Hour)
      old := now.Add(-400 * 24 * time.Hour)

      require.Equal(t, pkgmetadata.RefreshStateContinuing, seriesRefreshState(&pkgmetadata.Series{Status: pkgmetadata.SeriesStatusContinuing}, now))
      require.Equal(t, pkgmetadata.RefreshStateEndedRecent,
          seriesRefreshState(&pkgmetadata.Series{Status: pkgmetadata.SeriesStatusEnded, LastAired: &recent}, now))
      require.Equal(t, pkgmetadata.RefreshStateEndedOld,
          seriesRefreshState(&pkgmetadata.Series{Status: pkgmetadata.SeriesStatusEnded, LastAired: &old}, now))
      require.Equal(t, pkgmetadata.RefreshStateAnnounced, seriesRefreshState(&pkgmetadata.Series{Status: pkgmetadata.SeriesStatusUpcoming}, now))
  }
  ```
  Run `go test ./catalogarr/metadata/... -run 'TestMovieRefreshState|TestSeriesRefreshState'` — expect a build failure. Implement in `catalogarr/metadata/target.go` (append):
  ```go
  // movieRefreshState derives the RefreshTTL state bucket from a fetched
  // Movie's own fields — RefreshTTL does not know about CRDs or providers,
  // only the state string the caller hands it.
  func movieRefreshState(m *pkgmetadata.Movie, now time.Time) string {
      switch m.Status {
      case pkgmetadata.MovieStatusAnnounced:
          return pkgmetadata.RefreshStateAnnounced
      case pkgmetadata.MovieStatusInCinemas:
          return pkgmetadata.RefreshStateInCinemas
      case pkgmetadata.MovieStatusReleased:
          if latest := latestOf(m.DigitalRelease, m.PhysicalRelease); latest != nil && now.Sub(*latest) < 30*24*time.Hour {
              return pkgmetadata.RefreshStateReleasedRecent
          }
          return pkgmetadata.RefreshStateReleasedOld
      default:
          return pkgmetadata.RefreshStateReleasedOld
      }
  }

  func latestOf(a, b *time.Time) *time.Time {
      switch {
      case a == nil:
          return b
      case b == nil:
          return a
      case b.After(*a):
          return b
      default:
          return a
      }
  }

  func seriesRefreshState(s *pkgmetadata.Series, now time.Time) string {
      switch s.Status {
      case pkgmetadata.SeriesStatusContinuing:
          return pkgmetadata.RefreshStateContinuing
      case pkgmetadata.SeriesStatusEnded:
          if s.LastAired != nil && now.Sub(*s.LastAired) < 30*24*time.Hour {
              return pkgmetadata.RefreshStateEndedRecent
          }
          return pkgmetadata.RefreshStateEndedOld
      default: // upcoming
          return pkgmetadata.RefreshStateAnnounced
      }
  }
  ```
  Run `go test ./catalogarr/metadata/... -run 'TestMovieRefreshState|TestSeriesRefreshState' -v` — expect PASS.
  Commit: `git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "feat(catalogarr/metadata): refresh-state derivation for RefreshTTL" -- catalogarr/metadata/target.go catalogarr/metadata/target_test.go`

- [ ] **Step 7: build `MovieMetadataApplyConfiguration` from a fetched `*pkgmetadata.Movie`.** Failing test first, with the real values from `testdata/metadata/tmdb/movie_27205.json` (via the already-recorded fixture, not invented ones) plus enough extra fields to exercise the cap/filter logic.

  `catalogarr/metadata/patch_test.go`:
  ```go
  package metadata

  import (
      "testing"
      "time"

      "github.com/stretchr/testify/require"
      metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

      catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
      pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
  )

  func TestBuildMovieMetadataACMapsFieldsAndFiltersImageTypes(t *testing.T) {
      inCinemas := time.Date(2010, 7, 16, 0, 0, 0, 0, time.UTC)
      now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)

      m := &pkgmetadata.Movie{
          IDs:              pkgmetadata.ExternalIDs{pkgmetadata.KeyTMDB: "27205", pkgmetadata.KeyIMDb: "tt1375666"},
          Title:            "Inception",
          OriginalTitle:    "Inception",
          OriginalLanguage: "en",
          Runtime:          148,
          Genres:           []string{"Action", "Science Fiction", "Adventure"},
          Status:           pkgmetadata.MovieStatusReleased,
          InCinemas:        &inCinemas,
          Images: []pkgmetadata.Image{
              {Type: pkgmetadata.ImageTypePoster, URL: "https://image.tmdb.org/poster.jpg"},
              {Type: pkgmetadata.ImageTypeBanner, URL: "https://image.tmdb.org/banner.jpg"}, // no CRD equivalent
              {Type: pkgmetadata.ImageTypeFanart, URL: "https://image.tmdb.org/fanart.jpg"},
          },
          AlternateTitles: []pkgmetadata.AltTitle{{Title: "Origen"}, {Title: "Inception: Le Origini"}},
      }

      ac := buildMovieMetadataAC(m, now)

      require.Equal(t, "Inception", *ac.Title)
      require.EqualValues(t, 148, *ac.RuntimeMinutes)
      require.Equal(t, catalogv1alpha1.MovieReleaseStatus("released"), *ac.Status)
      require.True(t, ac.InCinemas.Equal(&metav1.Time{Time: inCinemas}))
      require.ElementsMatch(t, []string{"Action", "Science Fiction", "Adventure"}, ac.Genres)
      require.Equal(t, map[string]string{"tmdb": "27205", "imdb": "tt1375666"}, ac.ExternalIDs)
      require.True(t, ac.RefreshedAt.Equal(&metav1.Time{Time: now}))

      require.Len(t, ac.Images, 2, "banner has no CRD ImageType and must be dropped, not mis-labelled")
      for _, img := range ac.Images {
          require.Contains(t, []catalogv1alpha1.ImageType{catalogv1alpha1.ImageTypePoster, catalogv1alpha1.ImageTypeFanart}, *img.Type)
      }

      require.Equal(t, []string{"Origen", "Inception: Le Origini"}, ac.AlternateTitles)
  }

  func TestBuildMovieMetadataACCapsListsAtTheCRDsMaxItems(t *testing.T) {
      m := &pkgmetadata.Movie{Title: "Padded"}
      for i := 0; i < 80; i++ {
          m.AlternateTitles = append(m.AlternateTitles, pkgmetadata.AltTitle{Title: "Alt"})
          m.Images = append(m.Images, pkgmetadata.Image{Type: pkgmetadata.ImageTypePoster, URL: "https://x/1.jpg"})
      }
      for i := 0; i < 70; i++ {
          m.ReleaseDates = append(m.ReleaseDates, pkgmetadata.ReleaseDate{Country: "US", Type: pkgmetadata.ReleaseTypeTheatrical, Date: time.Now()})
      }

      ac := buildMovieMetadataAC(m, time.Now())
      require.Len(t, ac.AlternateTitles, 50, "MovieMetadata.AlternateTitles: +kubebuilder:validation:MaxItems=50")
      require.Len(t, ac.Images, 50, "MovieMetadata.Images: +kubebuilder:validation:MaxItems=50")
      require.Len(t, ac.ReleaseDates, 60, "MovieMetadata.ReleaseDates: +kubebuilder:validation:MaxItems=60")
  }

  func TestBuildMovieMetadataACOmitsCollectionWithoutATMDBID(t *testing.T) {
      withTMDB := &pkgmetadata.Movie{Collection: &pkgmetadata.Collection{
          IDs: pkgmetadata.ExternalIDs{pkgmetadata.KeyTMDB: "1241"}, Title: "The Mummy Collection",
      }}
      ac := buildMovieMetadataAC(withTMDB, time.Now())
      require.NotNil(t, ac.Collection)
      require.EqualValues(t, 1241, *ac.Collection.TmdbID)
      require.Equal(t, "The Mummy Collection", *ac.Collection.Name)

      withoutTMDB := &pkgmetadata.Movie{Collection: &pkgmetadata.Collection{Title: "Untethered"}}
      ac = buildMovieMetadataAC(withoutTMDB, time.Now())
      require.Nil(t, ac.Collection, "CollectionRef.TmdbID is +required; without one, omit the collection rather than send a zero id")
  }
  ```
  Run `go test ./catalogarr/metadata/... -run TestBuildMovieMetadataAC` — expect a build failure (`buildMovieMetadataAC`, `mapImageType` undefined). Implement `catalogarr/metadata/patch.go`:
  ```go
  package metadata

  import (
      "strconv"
      "time"

      metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

      catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
      catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
      pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
  )

  // mapImageType narrows pkg/metadata's nine image roles to the three
  // catalog.clustarr.io's CRD accepts (poster, fanart, logo — §4.2,
  // api/catalog/v1alpha1/shared_types.go:51). An image with no CRD
  // equivalent (banner, clearart, thumb, screenshot, disc, headshot) is
  // dropped, not mis-labelled: a wrong type is worse than a missing image.
  func mapImageType(t pkgmetadata.ImageType) (catalogv1alpha1.ImageType, bool) {
      switch t {
      case pkgmetadata.ImageTypePoster:
          return catalogv1alpha1.ImageTypePoster, true
      case pkgmetadata.ImageTypeFanart:
          return catalogv1alpha1.ImageTypeFanart, true
      case pkgmetadata.ImageTypeLogo:
          return catalogv1alpha1.ImageTypeLogo, true
      default:
          return "", false
      }
  }

  // buildMovieMetadataAC maps a fetched provider Movie onto
  // MovieStatus.metadata, truncating every list to the CRD's own
  // +kubebuilder:validation:MaxItems cap (CLAUDE.md: "cap every status
  // list").
  func buildMovieMetadataAC(m *pkgmetadata.Movie, now time.Time) *catalogac.MovieMetadataApplyConfiguration {
      ac := catalogac.MovieMetadata().
          WithTitle(m.Title).
          WithOriginalTitle(m.OriginalTitle).
          WithSortTitle(m.SortTitle).
          WithOriginalLanguage(m.OriginalLanguage).
          WithOverview(m.Overview).
          WithCertification(m.Certification).
          WithYear(m.Year).
          WithRuntimeMinutes(m.Runtime).
          WithStatus(catalogv1alpha1.MovieReleaseStatus(m.Status)).
          WithExternalIDs(m.IDs).
          WithRefreshedAt(metav1.NewTime(now))

      if len(m.Genres) > 0 {
          ac.WithGenres(m.Genres...)
      }
      if m.InCinemas != nil {
          ac.WithInCinemas(metav1.NewTime(*m.InCinemas))
      }
      if m.DigitalRelease != nil {
          ac.WithDigitalRelease(metav1.NewTime(*m.DigitalRelease))
      }
      if m.PhysicalRelease != nil {
          ac.WithPhysicalRelease(metav1.NewTime(*m.PhysicalRelease))
      }
      for _, rd := range m.ReleaseDates {
          if len(ac.ReleaseDates) >= 60 {
              break
          }
          ac.WithReleaseDates(catalogac.ReleaseDate().
              WithCountry(rd.Country).WithType(int32(rd.Type)).WithDate(metav1.NewTime(rd.Date)))
      }
      for _, img := range m.Images {
          if len(ac.Images) >= 50 {
              break
          }
          t, ok := mapImageType(img.Type)
          if !ok {
              continue
          }
          ac.WithImages(catalogac.Image().WithType(t).WithURL(img.URL))
      }
      for _, at := range m.AlternateTitles {
          if len(ac.AlternateTitles) >= 50 {
              break
          }
          ac.WithAlternateTitles(at.Title)
      }
      if m.Collection != nil {
          if tmdbID, ok := m.Collection.IDs[pkgmetadata.KeyTMDB]; ok {
              if id, err := strconv.ParseInt(tmdbID, 10, 64); err == nil {
                  ac.WithCollection(catalogac.CollectionRef().WithTmdbID(id).WithName(m.Collection.Title))
              }
          }
      }
      return ac
  }
  ```
  Run `go test ./catalogarr/metadata/... -run TestBuildMovieMetadataAC -v` — expect PASS.
  Commit: `git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "feat(catalogarr/metadata): map fetched Movie into MovieMetadata's apply configuration" -- catalogarr/metadata/patch.go catalogarr/metadata/patch_test.go`

- [ ] **Step 8: the same for Series.** Failing test first — Series' CRD `AlternateTitles` is `[]AltTitle` (not `[]string` like Movie's), so this is a genuinely different mapping, not a copy-paste.

  Append to `catalogarr/metadata/patch_test.go`:
  ```go
  func TestBuildSeriesMetadataACMapsAlternateTitlesAsStructsNotStrings(t *testing.T) {
      scene := int32(1)
      s := &pkgmetadata.Series{
          IDs:     pkgmetadata.ExternalIDs{pkgmetadata.KeyTVDB: "121361"},
          Title:   "Game of Thrones",
          Status:  pkgmetadata.SeriesStatusEnded,
          Genres:  []string{"Drama", "Fantasy"},
          AlternateTitles: []pkgmetadata.AltTitle{
              {Title: "GoT", SceneSeason: &scene},
              {Title: "Le Trône de Fer"},
          },
      }
      ac := buildSeriesMetadataAC(s, time.Now())

      require.Equal(t, "Game of Thrones", *ac.Title)
      require.Equal(t, catalogv1alpha1.SeriesRunStatus("ended"), *ac.Status)
      require.Len(t, ac.AlternateTitles, 2)
      require.Equal(t, "GoT", *ac.AlternateTitles[0].Title)
      require.EqualValues(t, 1, *ac.AlternateTitles[0].SceneSeason)
      require.Nil(t, ac.AlternateTitles[1].SceneSeason)
  }

  func TestBuildSeriesMetadataACCapsAlternateTitlesAt100(t *testing.T) {
      s := &pkgmetadata.Series{Title: "Padded"}
      for i := 0; i < 150; i++ {
          s.AlternateTitles = append(s.AlternateTitles, pkgmetadata.AltTitle{Title: "Alt"})
      }
      ac := buildSeriesMetadataAC(s, time.Now())
      require.Len(t, ac.AlternateTitles, 100, "SeriesMetadata.AlternateTitles: +kubebuilder:validation:MaxItems=100")
  }
  ```
  Run `go test ./catalogarr/metadata/... -run TestBuildSeriesMetadataAC` — expect a build failure. Implement in `catalogarr/metadata/patch.go` (append):
  ```go
  // buildSeriesMetadataAC maps a fetched provider Series onto
  // SeriesStatus.metadata. Unlike Movie, Series' CRD AlternateTitles is
  // []AltTitle{Title, SceneSeason}, not []string — map the struct, not just
  // the title.
  func buildSeriesMetadataAC(s *pkgmetadata.Series, now time.Time) *catalogac.SeriesMetadataApplyConfiguration {
      ac := catalogac.SeriesMetadata().
          WithTitle(s.Title).
          WithSortTitle(s.SortTitle).
          WithNetwork(s.Network).
          WithAirTime(s.AirTime).
          WithOverview(s.Overview).
          WithCertification(s.Certification).
          WithOriginalLanguage(s.OriginalLanguage).
          WithYear(s.Year).
          WithRuntimeMinutes(s.Runtime).
          WithStatus(catalogv1alpha1.SeriesRunStatus(s.Status)).
          WithExternalIDs(s.IDs).
          WithRefreshedAt(metav1.NewTime(now))

      if len(s.Genres) > 0 {
          ac.WithGenres(s.Genres...)
      }
      for _, img := range s.Images {
          if len(ac.Images) >= 50 {
              break
          }
          t, ok := mapImageType(img.Type)
          if !ok {
              continue
          }
          ac.WithImages(catalogac.Image().WithType(t).WithURL(img.URL))
      }
      for _, at := range s.AlternateTitles {
          if len(ac.AlternateTitles) >= 100 {
              break
          }
          alt := catalogac.AltTitle().WithTitle(at.Title)
          if at.SceneSeason != nil {
              alt.WithSceneSeason(*at.SceneSeason)
          }
          ac.WithAlternateTitles(alt)
      }
      return ac
  }
  ```
  Run `go test ./catalogarr/metadata/... -run TestBuildSeriesMetadataAC -v` — expect PASS.
  Commit: `git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "feat(catalogarr/metadata): map fetched Series into SeriesMetadata's apply configuration" -- catalogarr/metadata/patch.go catalogarr/metadata/patch_test.go`

- [ ] **Step 9: build the `Registry` from `MetadataProvider` CRs.** Failing test first, using the controller-runtime fake client for `Get` (secrets) — no network, since provider construction never calls out.

  `catalogarr/metadata/registry_test.go`:
  ```go
  package metadata

  import (
      "context"
      "net/http"
      "testing"

      "github.com/stretchr/testify/require"
      corev1 "k8s.io/api/core/v1"
      metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
      "sigs.k8s.io/controller-runtime/pkg/client/fake"

      catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
      "github.com/mediactl/clustarr/pkg/k8s"
  )

  func enabled() *bool { b := true; return &b }

  func TestBuildRegistryWiresOnlyTheImplementedTypesInPriorityOrder(t *testing.T) {
      secret := &corev1.Secret{
          ObjectMeta: metav1.ObjectMeta{Name: "tmdb-key", Namespace: "clustarr"},
          Data:       map[string][]byte{catalogv1alpha1.MetadataSecretKeyAPIKey: []byte("test-key")},
      }
      providers := []catalogv1alpha1.MetadataProvider{
          {
              ObjectMeta: metav1.ObjectMeta{Name: "tmdb-backup", Namespace: "clustarr"},
              Spec: catalogv1alpha1.MetadataProviderSpec{
                  Type: catalogv1alpha1.MetadataProviderTMDB, Enabled: enabled(), Priority: 90,
                  SecretRef: &corev1.LocalObjectReference{Name: "tmdb-key"},
              },
          },
          {
              ObjectMeta: metav1.ObjectMeta{Name: "tmdb-primary", Namespace: "clustarr"},
              Spec: catalogv1alpha1.MetadataProviderSpec{
                  Type: catalogv1alpha1.MetadataProviderTMDB, Enabled: enabled(), Priority: 10,
                  SecretRef: &corev1.LocalObjectReference{Name: "tmdb-key"},
              },
          },
          {
              ObjectMeta: metav1.ObjectMeta{Name: "off", Namespace: "clustarr"},
              Spec: catalogv1alpha1.MetadataProviderSpec{
                  Type: catalogv1alpha1.MetadataProviderTVDB, Enabled: func() *bool { b := false; return &b }(),
              },
          },
          {
              ObjectMeta: metav1.ObjectMeta{Name: "no-client-yet", Namespace: "clustarr"},
              Spec: catalogv1alpha1.MetadataProviderSpec{Type: catalogv1alpha1.MetadataProviderFanart, Enabled: enabled()},
          },
      }

      c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(secret).Build()
      reg, err := BuildRegistry(context.Background(), c, providers, http.DefaultClient)
      require.NoError(t, err)

      require.Len(t, reg.Movies, 2, "both tmdb providers wire a MovieProvider; the disabled tvdb and unimplemented fanart do not")
      require.Equal(t, "tmdb", reg.Movies[0].Name())
      require.Empty(t, reg.Series, "tvdb was disabled; tmdb implements MovieProvider only (see Judgment call 3)")
  }

  func TestBuildRegistryErrorsOnAMissingSecret(t *testing.T) {
      providers := []catalogv1alpha1.MetadataProvider{{
          ObjectMeta: metav1.ObjectMeta{Name: "tmdb", Namespace: "clustarr"},
          Spec: catalogv1alpha1.MetadataProviderSpec{
              Type: catalogv1alpha1.MetadataProviderTMDB, Enabled: enabled(),
              SecretRef: &corev1.LocalObjectReference{Name: "does-not-exist"},
          },
      }}
      c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).Build()
      _, err := BuildRegistry(context.Background(), c, providers, http.DefaultClient)
      require.Error(t, err)
  }
  ```
  Run `go test ./catalogarr/metadata/... -run TestBuildRegistry` — expect a build failure. Implement `catalogarr/metadata/registry.go`:
  ```go
  package metadata

  import (
      "context"
      "fmt"
      "net/http"
      "sort"

      corev1 "k8s.io/api/core/v1"
      "k8s.io/apimachinery/pkg/types"
      "sigs.k8s.io/controller-runtime/pkg/client"

      catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
      pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
      "github.com/mediactl/clustarr/pkg/metadata/clients/audnexus"
      "github.com/mediactl/clustarr/pkg/metadata/clients/comicvine"
      "github.com/mediactl/clustarr/pkg/metadata/clients/musicbrainz"
      "github.com/mediactl/clustarr/pkg/metadata/clients/openlibrary"
      "github.com/mediactl/clustarr/pkg/metadata/clients/tmdb"
      "github.com/mediactl/clustarr/pkg/metadata/clients/tvdb"
  )

  // BuildRegistry constructs a pkg/metadata.Registry from the cluster's
  // MetadataProvider objects, in ascending spec.priority order (lower wins,
  // matching Registry.Lookup's "first in the slice, first tried"). Only the
  // six types with a Phase B client are wired (see Judgment call 5 in this
  // task's plan); an enabled provider of any other type is skipped, not an
  // error, so an operator can create the CR ahead of the client landing.
  func BuildRegistry(ctx context.Context, c client.Client, providers []catalogv1alpha1.MetadataProvider, httpClient *http.Client) (*pkgmetadata.Registry, error) {
      sorted := make([]catalogv1alpha1.MetadataProvider, len(providers))
      copy(sorted, providers)
      sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Spec.Priority < sorted[j].Spec.Priority })

      reg := &pkgmetadata.Registry{}
      for _, p := range sorted {
          if p.Spec.Enabled != nil && !*p.Spec.Enabled {
              continue
          }
          limiter := resolveLimiter(p.Spec.Type, p.Spec.RateLimit)
          switch p.Spec.Type {
          case catalogv1alpha1.MetadataProviderTMDB:
              key, err := secretValue(ctx, c, p, catalogv1alpha1.MetadataSecretKeyAPIKey)
              if err != nil {
                  return nil, err
              }
              cl, err := tmdb.New(key, httpClient, baseURL(p, "https://api.themoviedb.org/3"), limiter)
              if err != nil {
                  return nil, fmt.Errorf("metadata: build tmdb client for %s/%s: %w", p.Namespace, p.Name, err)
              }
              reg.Movies = append(reg.Movies, cl)
          case catalogv1alpha1.MetadataProviderTVDB:
              key, err := secretValue(ctx, c, p, catalogv1alpha1.MetadataSecretKeyAPIKey)
              if err != nil {
                  return nil, err
              }
              pin, _ := secretValue(ctx, c, p, catalogv1alpha1.MetadataSecretKeyPin)
              reg.Series = append(reg.Series, tvdb.New(key, pin, httpClient, baseURL(p, "https://api4.thetvdb.com/v4"), limiter))
          case catalogv1alpha1.MetadataProviderMusicBrainz:
              cl, err := musicbrainz.New(p.Spec.ContactUserAgent, httpClient, baseURL(p, "https://musicbrainz.org/ws/2"), limiter)
              if err != nil {
                  return nil, fmt.Errorf("metadata: build musicbrainz client for %s/%s: %w", p.Namespace, p.Name, err)
              }
              reg.Artists = append(reg.Artists, cl)
          case catalogv1alpha1.MetadataProviderOpenLibrary:
              reg.Books = append(reg.Books, openlibrary.New(p.Spec.ContactUserAgent, httpClient, baseURL(p, "https://openlibrary.org"), limiter))
          case catalogv1alpha1.MetadataProviderAudnexus:
              reg.Audiobooks = append(reg.Audiobooks, audnexus.New(httpClient, baseURL(p, "https://api.audnex.us"), limiter))
          case catalogv1alpha1.MetadataProviderComicVine:
              key, err := secretValue(ctx, c, p, catalogv1alpha1.MetadataSecretKeyAPIKey)
              if err != nil {
                  return nil, err
              }
              reg.Comics = append(reg.Comics, comicvine.New(key, httpClient, baseURL(p, "https://comicvine.gamespot.com/api"), limiter))
          default:
              continue
          }
      }
      return reg, nil
  }

  func baseURL(p catalogv1alpha1.MetadataProvider, def string) string {
      if p.Spec.BaseURL != nil && *p.Spec.BaseURL != "" {
          return *p.Spec.BaseURL
      }
      return def
  }

  func secretValue(ctx context.Context, c client.Client, p catalogv1alpha1.MetadataProvider, key string) (string, error) {
      if p.Spec.SecretRef == nil {
          return "", fmt.Errorf("metadata: %s/%s (%s) has no secretRef", p.Namespace, p.Name, p.Spec.Type)
      }
      var s corev1.Secret
      if err := c.Get(ctx, types.NamespacedName{Namespace: p.Namespace, Name: p.Spec.SecretRef.Name}, &s); err != nil {
          return "", fmt.Errorf("metadata: get secret %s/%s: %w", p.Namespace, p.Spec.SecretRef.Name, err)
      }
      v, ok := s.Data[key]
      if !ok {
          return "", fmt.Errorf("metadata: secret %s/%s has no %q key", p.Namespace, p.Spec.SecretRef.Name, key)
      }
      return string(v), nil
  }
  ```
  Run `go test ./catalogarr/metadata/... -run TestBuildRegistry -v` — expect PASS.
  Commit: `git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "feat(catalogarr/metadata): build the provider Registry from MetadataProvider CRs" -- catalogarr/metadata/registry.go catalogarr/metadata/registry_test.go`

- [ ] **Step 10: the work-queue `Handler`, happy path.** This needs a real apiserver for `k8s.PatchStatus`'s SSA semantics, so it is an envtest file (mirror `pkg/k8s/patch_envtest_test.go`'s `newTestClient` helper) combined with an `httptest` TMDB stub reusing the verified fixture. Failing test first.

  `catalogarr/metadata/worker_envtest_test.go`:
  ```go
  package metadata_test

  import (
      "context"
      "net/http"
      "net/http/httptest"
      "os"
      "testing"
      "time"

      "github.com/stretchr/testify/require"
      corev1 "k8s.io/api/core/v1"
      metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
      "k8s.io/apimachinery/pkg/types"
      "sigs.k8s.io/controller-runtime/pkg/client"
      "sigs.k8s.io/controller-runtime/pkg/envtest"

      catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
      commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
      "github.com/mediactl/clustarr/catalogarr/metadata"
      "github.com/mediactl/clustarr/pkg/events"
      "github.com/mediactl/clustarr/pkg/events/schema"
      "github.com/mediactl/clustarr/pkg/k8s"
      pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
      "github.com/mediactl/clustarr/pkg/metadata/clients/tmdb"
  )

  // newTestClient mirrors pkg/k8s/patch_envtest_test.go's helper: an
  // apiserver with the real CRDs, skipped when KUBEBUILDER_ASSETS is unset.
  func newTestClient(t *testing.T) client.Client {
      t.Helper()
      if os.Getenv("KUBEBUILDER_ASSETS") == "" {
          t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
      }
      env := &envtest.Environment{CRDDirectoryPaths: []string{"../../config/crd/bases"}, ErrorIfCRDPathMissing: true}
      cfg, err := env.Start()
      require.NoError(t, err)
      t.Cleanup(func() { require.NoError(t, env.Stop()) })
      c, err := client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
      require.NoError(t, err)
      return c
  }

  func newMovie(t *testing.T, ctx context.Context, c client.Client, ns, name string, tmdbID int64) *catalogv1alpha1.Movie {
      t.Helper()
      if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}); err != nil && client.IgnoreAlreadyExists(err) != nil {
          t.Fatalf("create namespace: %v", err)
      }
      m := &catalogv1alpha1.Movie{
          ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
          Spec: catalogv1alpha1.MovieSpec{
              TmdbID: tmdbID, QualityProfileRef: "hd-1080p", RootFolderRef: "movies",
          },
      }
      require.NoError(t, c.Create(ctx, m))
      return m
  }

  func TestHandlerFetchesFromTheProviderAndPatchesOnlyStatusMetadata(t *testing.T) {
      ctx := context.Background()
      c := newTestClient(t)
      const ns, name = "hworker", "inception"
      newMovie(t, ctx, c, ns, name, 27205)

      body, err := os.ReadFile("../../testdata/metadata/tmdb/movie_27205.json")
      require.NoError(t, err)
      srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
          require.Equal(t, "/movie/27205", r.URL.Path)
          w.Header().Set("Content-Type", "application/json")
          _, _ = w.Write(body)
      }))
      t.Cleanup(srv.Close)
      cl, err := tmdb.New("test-key", srv.Client(), srv.URL, pkgmetadata.NewLimiter(1000, 1))
      require.NoError(t, err)

      h := &metadata.Handler{
          Client:   c,
          Registry: &pkgmetadata.Registry{Movies: []pkgmetadata.MovieProvider{cl}},
          Cache:    noopCache{},
      }

      env := &events.Envelope{
          Key:    ns + "/" + name,
          Schema: schema.MetadataTask{}.Schema(),
      }
      task := schema.MetadataTask{MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: name}}
      _, env.Data, err = schema.Encode(task)
      require.NoError(t, err)

      require.NoError(t, h.Handle(ctx, testMessage{env: env}))

      var got catalogv1alpha1.Movie
      require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got))
      require.NotNil(t, got.Status.Metadata)
      require.Equal(t, "Inception", got.Status.Metadata.Title)
      require.EqualValues(t, 148, got.Status.Metadata.RuntimeMinutes)
      require.Equal(t, "tt1375666", got.Status.Metadata.ExternalIDs["imdb"])
      require.Empty(t, got.Status.Phase, "the worker must never set phase; that is the movie controller's field")
      require.Empty(t, got.Status.Conditions, "the worker must never set conditions")
  }
  ```
  This references two small test doubles (`noopCache`, `testMessage`) and `schema.Encode`/`.Schema()` — check `schema.Decode`'s exact companion `Encode` signature with `go doc ./pkg/events/schema Encode` before writing them; add them at the top of this file:
  ```go
  type noopCache struct{}

  func (noopCache) Get(context.Context, string, any) (bool, error) { return false, nil }
  func (noopCache) Set(context.Context, string, any, time.Duration) error { return nil }

  // testMessage is the minimal events.Message this test needs: Handle only
  // calls Envelope(). A later step (envtest via membus directly) exercises
  // the real Ack/Nak/Term/InProgress wiring; this one isolates the fetch +
  // patch behaviour from the bus.
  type testMessage struct{ env *events.Envelope }

  func (m testMessage) Envelope() *events.Envelope     { return m.env }
  func (m testMessage) Subject() string                { return "" }
  func (m testMessage) Attempt() uint64                { return 1 }
  func (m testMessage) Ack(context.Context) error      { return nil }
  func (m testMessage) Nak(context.Context, time.Duration) error { return nil }
  func (m testMessage) Term(context.Context, string) error       { return nil }
  func (m testMessage) InProgress(context.Context) error         { return nil }
  ```
  Run `go test ./catalogarr/metadata/... -run TestHandlerFetchesFromTheProvider` with `KUBEBUILDER_ASSETS` unset — expect **SKIP** (confirms the guard works) and a build failure on `metadata.Handler`/`Handle` (not yet implemented) once you fix the skip by running under `make test`. Implement `catalogarr/metadata/worker.go`:
  ```go
  package metadata

  import (
      "context"
      "fmt"
      "strings"
      "time"

      apierrors "k8s.io/apimachinery/pkg/api/errors"
      "sigs.k8s.io/controller-runtime/pkg/client"

      catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
      catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
      commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
      "github.com/mediactl/clustarr/pkg/events"
      "github.com/mediactl/clustarr/pkg/events/schema"
      "github.com/mediactl/clustarr/pkg/k8s"
      pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
      "github.com/mediactl/clustarr/pkg/obs/tracing"
  )

  // Handler is the clustarr.work.catalogarr.metadata.<tier>.<mediaKey>
  // consumer: the only place that reads a MetadataTask, calls a provider
  // through the Registry, and patches status.metadata back — and ONLY
  // status.metadata (§3's single-writer rule; §8.1).
  type Handler struct {
      Client   client.Client
      Registry *pkgmetadata.Registry
      Cache    pkgmetadata.Cache
      Now      func() time.Time // nil = time.Now
  }

  // Handle implements events.Handler.
  func (h *Handler) Handle(ctx context.Context, m events.Message) error {
      now := time.Now
      if h.Now != nil {
          now = h.Now
      }

      env := m.Envelope()
      var task schema.MetadataTask
      if err := schema.Decode(env.Schema, env.Data, &task); err != nil {
          return events.Discard("undecodable MetadataTask", err)
      }
      ns, _, ok := strings.Cut(env.Key, "/")
      if !ok || ns == "" {
          return events.Discard("envelope key is not <namespace>/<name>", fmt.Errorf("key=%q", env.Key))
      }

      target, err := newTarget(task.MediaRef.Kind)
      if err != nil {
          return events.Discard("unsupported media kind", err)
      }
      key := client.ObjectKey{Namespace: ns, Name: task.MediaRef.Name}

      ctx, span := tracing.Start(ctx, "metadata.Handler.Handle")
      defer span.End()

      if err := h.Client.Get(ctx, key, target); err != nil {
          if apierrors.IsNotFound(err) {
              return events.Discard("target object no longer exists", err)
          }
          tracing.RecordError(span, err)
          return fmt.Errorf("metadata: get %s: %w", key, err)
      }

      ids, err := externalIDs(target)
      if err != nil {
          return events.Discard("cannot derive external ids", err)
      }
      ck := cacheKey(task.MediaRef.Kind, ids)

      var cached any
      switch target.(type) {
      case *catalogv1alpha1.Movie:
          var v pkgmetadata.Movie
          if hit, err := h.Cache.Get(ctx, ck, &v); err != nil {
              return fmt.Errorf("metadata: cache get: %w", err)
          } else if hit {
              cached = &v
          }
      case *catalogv1alpha1.Series:
          var v pkgmetadata.Series
          if hit, err := h.Cache.Get(ctx, ck, &v); err != nil {
              return fmt.Errorf("metadata: cache get: %w", err)
          } else if hit {
              cached = &v
          }
      }

      result := cached
      if result == nil {
          fetchCtx, fetchSpan := tracing.Start(ctx, "metadata.Registry.Lookup")
          v, err := h.Registry.Lookup(fetchCtx, task.MediaRef.Kind, ids)
          if err != nil {
              tracing.RecordError(fetchSpan, err)
              fetchSpan.End()
              return settlement(err)
          }
          fetchSpan.End()
          result = v
      }

      var ac k8s.ApplyConfiguration
      var ttl time.Duration
      switch v := result.(type) {
      case *pkgmetadata.Movie:
          ttl = pkgmetadata.RefreshTTL(commonv1.MediaKindMovie, movieRefreshState(v, now()), refreshedAt(target))
          ac = catalogac.Movie(key.Name, key.Namespace).WithStatus(
              catalogac.MovieStatus().WithMetadata(buildMovieMetadataAC(v, now())))
      case *pkgmetadata.Series:
          ttl = pkgmetadata.RefreshTTL(commonv1.MediaKindSeries, seriesRefreshState(v, now()), refreshedAt(target))
          ac = catalogac.Series(key.Name, key.Namespace).WithStatus(
              catalogac.SeriesStatus().WithMetadata(buildSeriesMetadataAC(v, now())))
      default:
          return events.Discard("registry returned an unexpected type", fmt.Errorf("%T", result))
      }

      if cached == nil {
          if err := h.Cache.Set(ctx, ck, result, ttl); err != nil {
              // A cache-write failure must not fail the task: the status
              // write below is what §8.1 actually depends on.
              tracing.RecordError(span, fmt.Errorf("metadata: cache set (non-fatal): %w", err))
          }
      }

      if _, err := k8s.PatchStatus(ctx, h.Client, k8s.ManagerCatalogarrWorker, ac); err != nil {
          tracing.RecordError(span, err)
          return fmt.Errorf("metadata: patch status.metadata: %w", err)
      }
      return nil
  }
  ```
  Add the `cacheKey` helper to `catalogarr/metadata/cache.go` (append):
  ```go
  // cacheKey builds an item-level cache key: <kind>:<sorted k=v external ids>.
  // See this task's "Judgment calls" for why this deliberately drops the
  // <provider> segment the spec's KV table literally shows.
  func cacheKey(kind commonv1.MediaKind, ids pkgmetadata.ExternalIDs) string {
      keys := make([]string, 0, len(ids))
      for k := range ids {
          keys = append(keys, k)
      }
      sort.Strings(keys)
      parts := make([]string, 0, len(keys))
      for _, k := range keys {
          parts = append(parts, k+"="+ids[k])
      }
      return string(kind) + ":" + strings.Join(parts, ",")
  }
  ```
  (add `"sort"`, `"strings"` and `commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"` to `cache.go`'s imports). Add a small `cacheKey` unit test to `cache_test.go` asserting a deterministic, sorted key for a multi-key `ExternalIDs` map. Run `KUBEBUILDER_ASSETS=$(setup-envtest use 1.37.0 -p path) go test ./catalogarr/metadata/... -run TestHandlerFetchesFromTheProvider -v` — expect PASS.
  Commit: `git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "feat(catalogarr/metadata): work-queue Handler happy path" -- catalogarr/metadata/worker.go catalogarr/metadata/worker_envtest_test.go catalogarr/metadata/cache.go catalogarr/metadata/cache_test.go`

- [ ] **Step 11: `Handler` cache-hit path skips the provider entirely.** Failing test first, using a fake `MovieProvider` that fails the test if called.

  Append to `worker_envtest_test.go`:
  ```go
  type failIfCalledMovieProvider struct{ t *testing.T }

  func (p failIfCalledMovieProvider) Name() string { return "fail-if-called" }
  func (p failIfCalledMovieProvider) Capabilities() pkgmetadata.Capabilities { return pkgmetadata.Capabilities{} }
  func (p failIfCalledMovieProvider) Movie(context.Context, string, string) (*pkgmetadata.Movie, error) {
      p.t.Fatal("Movie called despite a cache hit")
      return nil, nil
  }
  func (p failIfCalledMovieProvider) FindMovie(context.Context, pkgmetadata.ExternalIDs) (*pkgmetadata.Movie, error) {
      p.t.Fatal("FindMovie called despite a cache hit")
      return nil, nil
  }
  func (p failIfCalledMovieProvider) SearchMovies(context.Context, string, int) ([]pkgmetadata.MovieHit, error) {
      return nil, nil
  }

  type fakeCache struct{ movie *pkgmetadata.Movie }

  func (c *fakeCache) Get(_ context.Context, _ string, out any) (bool, error) {
      if c.movie == nil {
          return false, nil
      }
      *out.(*pkgmetadata.Movie) = *c.movie
      return true, nil
  }
  func (c *fakeCache) Set(context.Context, string, any, time.Duration) error { return nil }

  func TestHandlerSkipsTheProviderOnACacheHit(t *testing.T) {
      ctx := context.Background()
      c := newTestClient(t)
      const ns, name = "hcache", "inception"
      newMovie(t, ctx, c, ns, name, 27205)

      h := &metadata.Handler{
          Client:   c,
          Registry: &pkgmetadata.Registry{Movies: []pkgmetadata.MovieProvider{failIfCalledMovieProvider{t: t}}},
          Cache:    &fakeCache{movie: &pkgmetadata.Movie{Title: "Inception (cached)", Runtime: 148}},
      }

      env := &events.Envelope{Key: ns + "/" + name, Schema: schema.MetadataTask{}.Schema()}
      task := schema.MetadataTask{MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: name}}
      var err error
      _, env.Data, err = schema.Encode(task)
      require.NoError(t, err)

      require.NoError(t, h.Handle(ctx, testMessage{env: env}))

      var got catalogv1alpha1.Movie
      require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got))
      require.Equal(t, "Inception (cached)", got.Status.Metadata.Title)
  }
  ```
  Run it — it should already PASS against Step 10's implementation (this step is a regression guard, not new production code; if it fails, the cache-check-before-Lookup ordering in `Handle` is wrong and needs fixing). Run `KUBEBUILDER_ASSETS=... go test ./catalogarr/metadata/... -run TestHandlerSkipsTheProviderOnACacheHit -v` — expect PASS with no changes to `worker.go`.
  Commit: `git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "test(catalogarr/metadata): cache hit must not call the provider" -- catalogarr/metadata/worker_envtest_test.go`

- [ ] **Step 12: `Handler` settlement and unsupported-kind paths.** Failing test first, using a fake provider returning `pkgmetadata` sentinels.

  Append to `worker_envtest_test.go`:
  ```go
  type erroringMovieProvider struct{ err error }

  func (p erroringMovieProvider) Name() string { return "erroring" }
  func (p erroringMovieProvider) Capabilities() pkgmetadata.Capabilities { return pkgmetadata.Capabilities{} }
  func (p erroringMovieProvider) Movie(context.Context, string, string) (*pkgmetadata.Movie, error) {
      return nil, p.err
  }
  func (p erroringMovieProvider) FindMovie(context.Context, pkgmetadata.ExternalIDs) (*pkgmetadata.Movie, error) {
      return nil, p.err
  }
  func (p erroringMovieProvider) SearchMovies(context.Context, string, int) ([]pkgmetadata.MovieHit, error) {
      return nil, p.err
  }

  func TestHandlerMapsRateLimitedToRetry(t *testing.T) {
      ctx := context.Background()
      c := newTestClient(t)
      const ns, name = "hratelimit", "inception"
      newMovie(t, ctx, c, ns, name, 27205)

      h := &metadata.Handler{
          Client:   c,
          Registry: &pkgmetadata.Registry{Movies: []pkgmetadata.MovieProvider{
              erroringMovieProvider{err: &pkgmetadata.RateLimitedError{Provider: "tmdb", RetryAfter: 45 * time.Second}},
          }},
          Cache: noopCache{},
      }
      env := &events.Envelope{Key: ns + "/" + name, Schema: schema.MetadataTask{}.Schema()}
      task := schema.MetadataTask{MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: name}}
      var err error
      _, env.Data, err = schema.Encode(task)
      require.NoError(t, err)

      err = h.Handle(ctx, testMessage{env: env})
      var re *events.RetryError
      require.ErrorAs(t, err, &re)
      require.Equal(t, 45*time.Second, re.After)
  }

  func TestHandlerMapsNotFoundToDiscard(t *testing.T) {
      ctx := context.Background()
      c := newTestClient(t)
      const ns, name = "hnotfound", "inception"
      newMovie(t, ctx, c, ns, name, 27205)

      h := &metadata.Handler{
          Client:   c,
          Registry: &pkgmetadata.Registry{Movies: []pkgmetadata.MovieProvider{erroringMovieProvider{err: pkgmetadata.ErrNotFound}}},
          Cache:    noopCache{},
      }
      env := &events.Envelope{Key: ns + "/" + name, Schema: schema.MetadataTask{}.Schema()}
      task := schema.MetadataTask{MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: name}}
      var err error
      _, env.Data, err = schema.Encode(task)
      require.NoError(t, err)

      err = h.Handle(ctx, testMessage{env: env})
      var de *events.DiscardError
      require.ErrorAs(t, err, &de)
  }

  func TestHandlerDiscardsAnUnsupportedKind(t *testing.T) {
      ctx := context.Background()
      c := newTestClient(t)
      h := &metadata.Handler{Client: c, Registry: &pkgmetadata.Registry{}, Cache: noopCache{}}

      env := &events.Envelope{Key: "ns/artist-1", Schema: schema.MetadataTask{}.Schema()}
      task := schema.MetadataTask{MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindArtist, Name: "artist-1"}}
      var err error
      _, env.Data, err = schema.Encode(task)
      require.NoError(t, err)

      err = h.Handle(ctx, testMessage{env: env})
      var de *events.DiscardError
      require.ErrorAs(t, err, &de)
  }
  ```
  These should already PASS given Steps 1 and 10's implementations (settlement mapping is already wired into `Handle`, and `newTarget` already returns an error for `MediaKindArtist` that `Handle` turns into `events.Discard`). Run `KUBEBUILDER_ASSETS=... go test ./catalogarr/metadata/... -run 'TestHandlerMaps|TestHandlerDiscards' -v` — expect PASS with no `worker.go` changes; if any fails, fix the specific ordering bug in `Handle` it exposes (do not weaken the test).
  Commit: `git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "test(catalogarr/metadata): Handler settlement and unsupported-kind coverage" -- catalogarr/metadata/worker_envtest_test.go`

- [ ] **Step 13: the RPC `lookup` responder.** Failing test first, over `membus` (no CRs involved — this path never touches Kubernetes).

  `catalogarr/metadata/rpc_test.go`:
  ```go
  package metadata

  import (
      "context"
      "net/http"
      "net/http/httptest"
      "os"
      "testing"

      "github.com/jonboulle/clockwork"
      "github.com/stretchr/testify/require"

      commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
      "github.com/mediactl/clustarr/pkg/events"
      "github.com/mediactl/clustarr/pkg/events/membus"
      "github.com/mediactl/clustarr/pkg/events/schema"
      pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
      "github.com/mediactl/clustarr/pkg/metadata/clients/tmdb"
  )

  func newTestBus(t *testing.T) events.Bus {
      t.Helper()
      bus := membus.New(clockwork.NewRealClock())
      t.Cleanup(func() { _ = bus.Close() })
      require.NoError(t, bus.Ensure(context.Background(), events.Default().ForSingleNode()))
      return bus
  }

  func TestServeRPCLookupReturnsTheProviderDocument(t *testing.T) {
      body, err := os.ReadFile("../../testdata/metadata/tmdb/movie_27205.json")
      require.NoError(t, err)
      srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
          w.Header().Set("Content-Type", "application/json")
          _, _ = w.Write(body)
      }))
      t.Cleanup(srv.Close)
      cl, err := tmdb.New("test-key", srv.Client(), srv.URL, pkgmetadata.NewLimiter(1000, 1))
      require.NoError(t, err)

      reg := &pkgmetadata.Registry{Movies: []pkgmetadata.MovieProvider{cl}}
      bus := newTestBus(t)
      require.NoError(t, ServeRPC(bus, reg))

      var resp schema.MetadataResponse
      req := schema.MetadataRequest{Kind: commonv1.MediaKindMovie, IDs: map[string]string{"tmdb": "27205"}}
      require.NoError(t, bus.Request(context.Background(), events.RPCMetadataLookup, req, &resp))

      require.Empty(t, resp.Error)
      var got pkgmetadata.Movie
      require.NoError(t, json.Unmarshal(resp.Result, &got))
      require.Equal(t, "Inception", got.Title)
  }

  func TestServeRPCLookupReturnsAnErrorStringOnFailure(t *testing.T) {
      reg := &pkgmetadata.Registry{} // no providers configured
      bus := newTestBus(t)
      require.NoError(t, ServeRPC(bus, reg))

      var resp schema.MetadataResponse
      req := schema.MetadataRequest{Kind: commonv1.MediaKindMovie, IDs: map[string]string{"tmdb": "1"}}
      require.NoError(t, bus.Request(context.Background(), events.RPCMetadataLookup, req, &resp))
      require.NotEmpty(t, resp.Error)
  }
  ```
  (add `"encoding/json"` to the imports.) Run `go test ./catalogarr/metadata/... -run TestServeRPCLookup` — expect a build failure (`ServeRPC` undefined). Implement `catalogarr/metadata/rpc.go` (lookup only for this step):
  ```go
  package metadata

  import (
      "context"
      "encoding/json"
      "fmt"

      "github.com/mediactl/clustarr/pkg/events"
      "github.com/mediactl/clustarr/pkg/events/schema"
      pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
  )

  // ServeRPC registers the gateway's three request/reply methods —
  // rpc.catalogarr.metadata.{lookup,search,resolve} — in the catalogarr
  // queue group. Unlike Handler, these never touch a Kubernetes object: the
  // caller (an import list, an interactive lookup) supplies ids and gets a
  // provider document back.
  func ServeRPC(bus events.Requester, reg *pkgmetadata.Registry) error {
      handlers := map[string]func(context.Context, schema.MetadataRequest) schema.MetadataResponse{
          events.RPCMetadataLookup: func(ctx context.Context, req schema.MetadataRequest) schema.MetadataResponse {
              return lookup(ctx, reg, req)
          },
      }
      for subject, h := range handlers {
          h := h
          err := bus.Serve(subject, events.QueueGroupCatalogar, func(ctx context.Context, data []byte) ([]byte, error) {
              var req schema.MetadataRequest
              if err := json.Unmarshal(data, &req); err != nil {
                  return nil, fmt.Errorf("metadata: decode MetadataRequest: %w", err)
              }
              return json.Marshal(h(ctx, req))
          })
          if err != nil {
              return fmt.Errorf("metadata: serve %s: %w", subject, err)
          }
      }
      return nil
  }

  func lookup(ctx context.Context, reg *pkgmetadata.Registry, req schema.MetadataRequest) schema.MetadataResponse {
      v, err := reg.Lookup(ctx, req.Kind, req.IDs)
      if err != nil {
          return schema.MetadataResponse{Kind: req.Kind, Error: err.Error()}
      }
      result, err := json.Marshal(v)
      if err != nil {
          return schema.MetadataResponse{Kind: req.Kind, Error: err.Error()}
      }
      return schema.MetadataResponse{Kind: req.Kind, IDs: idsOf(v), Result: result}
  }

  func idsOf(v any) map[string]string {
      switch e := v.(type) {
      case *pkgmetadata.Movie:
          return e.IDs
      case *pkgmetadata.Series:
          return e.IDs
      case *pkgmetadata.Artist:
          return e.IDs
      case *pkgmetadata.Author:
          return e.IDs
      case *pkgmetadata.Audiobook:
          return e.IDs
      case *pkgmetadata.ComicVolume:
          return e.IDs
      default:
          return nil
      }
  }
  ```
  Run `go test ./catalogarr/metadata/... -run TestServeRPCLookup -v` — expect PASS.
  Commit: `git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "feat(catalogarr/metadata): RPC lookup responder" -- catalogarr/metadata/rpc.go catalogarr/metadata/rpc_test.go`

- [ ] **Step 14: RPC `search` and `resolve`.** Failing test first.

  Append to `rpc_test.go`:
  ```go
  type stubArtistProvider struct{ hit pkgmetadata.SearchHit }

  func (p stubArtistProvider) Name() string { return "musicbrainz" }
  func (p stubArtistProvider) Capabilities() pkgmetadata.Capabilities { return pkgmetadata.Capabilities{} }
  func (p stubArtistProvider) SearchArtists(context.Context, string) ([]pkgmetadata.SearchHit, error) {
      return []pkgmetadata.SearchHit{p.hit}, nil
  }
  func (p stubArtistProvider) Artist(context.Context, string) (*pkgmetadata.Artist, error) { return nil, pkgmetadata.ErrNotFound }
  func (p stubArtistProvider) Albums(context.Context, string) ([]pkgmetadata.Album, error) { return nil, nil }
  func (p stubArtistProvider) Album(context.Context, string) (*pkgmetadata.Album, error)   { return nil, pkgmetadata.ErrNotFound }

  func TestServeRPCSearchDispatchesByKind(t *testing.T) {
      reg := &pkgmetadata.Registry{
          Artists: []pkgmetadata.ArtistProvider{stubArtistProvider{hit: pkgmetadata.SearchHit{Title: "Radiohead"}}},
      }
      bus := newTestBus(t)
      require.NoError(t, ServeRPC(bus, reg))

      var resp schema.MetadataResponse
      req := schema.MetadataRequest{Kind: commonv1.MediaKindArtist, Text: "Radiohead"}
      require.NoError(t, bus.Request(context.Background(), events.RPCMetadataSearch, req, &resp))
      require.Len(t, resp.Results, 1)
      var hit pkgmetadata.SearchHit
      require.NoError(t, json.Unmarshal(resp.Results[0], &hit))
      require.Equal(t, "Radiohead", hit.Title)
  }

  func TestServeRPCSearchReportsUnsupportedKinds(t *testing.T) {
      reg := &pkgmetadata.Registry{}
      bus := newTestBus(t)
      require.NoError(t, ServeRPC(bus, reg))

      var resp schema.MetadataResponse
      req := schema.MetadataRequest{Kind: commonv1.MediaKindSeries, Text: "anything"}
      require.NoError(t, bus.Request(context.Background(), events.RPCMetadataSearch, req, &resp))
      require.NotEmpty(t, resp.Error, "SeriesProvider has no search method (spec-pinned; see pkg/metadata go doc)")
  }

  type stubResolver struct{ add pkgmetadata.ExternalIDs }

  func (p stubResolver) Name() string { return "wikidata" }
  func (p stubResolver) Capabilities() pkgmetadata.Capabilities { return pkgmetadata.Capabilities{} }
  func (p stubResolver) Resolve(context.Context, commonv1.MediaKind, pkgmetadata.ExternalIDs) (pkgmetadata.ExternalIDs, error) {
      return p.add, nil
  }

  func TestServeRPCResolveMergesEveryResolver(t *testing.T) {
      reg := &pkgmetadata.Registry{Resolvers: []pkgmetadata.IDResolver{
          stubResolver{add: pkgmetadata.ExternalIDs{"imdb": "tt1375666"}},
      }}
      bus := newTestBus(t)
      require.NoError(t, ServeRPC(bus, reg))

      var resp schema.MetadataResponse
      req := schema.MetadataRequest{Kind: commonv1.MediaKindMovie, IDs: map[string]string{"tmdb": "27205"}}
      require.NoError(t, bus.Request(context.Background(), events.RPCMetadataResolve, req, &resp))
      require.Equal(t, "27205", resp.IDs["tmdb"])
      require.Equal(t, "tt1375666", resp.IDs["imdb"])
  }
  ```
  Run `go test ./catalogarr/metadata/... -run 'TestServeRPCSearch|TestServeRPCResolve'` — expect a build failure (only `lookup` is registered). Implement in `catalogarr/metadata/rpc.go` (extend the `handlers` map and add the two functions):
  ```go
  // add to the handlers map in ServeRPC:
  events.RPCMetadataSearch:  func(ctx context.Context, req schema.MetadataRequest) schema.MetadataResponse { return search(ctx, reg, req) },
  events.RPCMetadataResolve: func(ctx context.Context, req schema.MetadataRequest) schema.MetadataResponse { return resolve(ctx, reg, req) },
  ```
  ```go
  // search dispatches by kind. Only the kinds whose Provider interface (§7,
  // go doc ./pkg/metadata) has a search method are supported: movie, artist,
  // album, book, comic. series has none (spec-pinned); audiobook, author
  // have none either (Audnexus/Open Library search is out of scope).
  func search(ctx context.Context, reg *pkgmetadata.Registry, req schema.MetadataRequest) schema.MetadataResponse {
      switch req.Kind {
      case commonv1.MediaKindMovie:
          for _, p := range reg.Movies {
              if hits, err := p.SearchMovies(ctx, req.Text, int(req.Year)); err == nil {
                  return schema.MetadataResponse{Kind: req.Kind, Provider: p.Name(), Results: marshalAll(hits)}
              }
          }
      case commonv1.MediaKindArtist:
          for _, p := range reg.Artists {
              if hits, err := p.SearchArtists(ctx, req.Text); err == nil {
                  return schema.MetadataResponse{Kind: req.Kind, Provider: p.Name(), Results: marshalAll(hits)}
              }
          }
      case commonv1.MediaKindAlbum:
          for _, p := range reg.Artists {
              if hits, err := p.SearchAlbums(ctx, req.Text); err == nil {
                  return schema.MetadataResponse{Kind: req.Kind, Provider: p.Name(), Results: marshalAll(hits)}
              }
          }
      case commonv1.MediaKindBook:
          for _, p := range reg.Books {
              if hits, err := p.SearchBooks(ctx, req.Text); err == nil {
                  return schema.MetadataResponse{Kind: req.Kind, Provider: p.Name(), Results: marshalAll(hits)}
              }
          }
      case commonv1.MediaKindComic:
          for _, p := range reg.Comics {
              if hits, err := p.SearchVolumes(ctx, req.Text); err == nil {
                  return schema.MetadataResponse{Kind: req.Kind, Provider: p.Name(), Results: marshalAll(hits)}
              }
          }
      }
      return schema.MetadataResponse{Kind: req.Kind, Error: fmt.Sprintf("metadata: search does not support kind %q", req.Kind)}
  }

  func resolve(ctx context.Context, reg *pkgmetadata.Registry, req schema.MetadataRequest) schema.MetadataResponse {
      ids := req.IDs
      for _, r := range reg.Resolvers {
          if resolved, err := r.Resolve(ctx, req.Kind, req.IDs); err == nil {
              ids = pkgmetadata.ExternalIDs(ids).Merge(resolved)
          }
      }
      return schema.MetadataResponse{Kind: req.Kind, IDs: ids}
  }

  func marshalAll[T any](items []T) [][]byte {
      out := make([][]byte, 0, len(items))
      for _, it := range items {
          if b, err := json.Marshal(it); err == nil {
              out = append(out, b)
          }
      }
      return out
  }
  ```
  Run `go test ./catalogarr/metadata/... -run 'TestServeRPCSearch|TestServeRPCResolve' -v` — expect PASS.
  Commit: `git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "feat(catalogarr/metadata): RPC search and resolve responders" -- catalogarr/metadata/rpc.go catalogarr/metadata/rpc_test.go`

- [ ] **Step 15: episode listing on the `lookup` subject — the Task C6 contract.** Failing test first.

  Append to `rpc_test.go`:
  ```go
  type stubSeriesProvider struct {
      episodes []pkgmetadata.Episode
      err      error
      wantOrder string
  }

  func (p stubSeriesProvider) Name() string { return "tvdb" }
  func (p stubSeriesProvider) Capabilities() pkgmetadata.Capabilities { return pkgmetadata.Capabilities{} }
  func (p stubSeriesProvider) Series(context.Context, string) (*pkgmetadata.Series, error) {
      return nil, pkgmetadata.ErrNotFound
  }
  func (p stubSeriesProvider) Episodes(_ context.Context, tvdbID, order string) ([]pkgmetadata.Episode, error) {
      if p.wantOrder != "" && order != p.wantOrder {
          return nil, fmt.Errorf("unexpected order %q", order)
      }
      return p.episodes, p.err
  }
  func (p stubSeriesProvider) Updates(context.Context, time.Time) ([]string, error) { return nil, nil }

  func TestServeRPCLookupListsEpisodesForTaskC6(t *testing.T) {
      reg := &pkgmetadata.Registry{Series: []pkgmetadata.SeriesProvider{stubSeriesProvider{
          wantOrder: "absolute",
          episodes: []pkgmetadata.Episode{
              {SeasonNumber: 1, EpisodeNumber: 1, Title: "Pilot"},
              {SeasonNumber: 1, EpisodeNumber: 2, Title: "Two"},
          },
      }}}
      bus := newTestBus(t)
      require.NoError(t, ServeRPC(bus, reg))

      var resp schema.MetadataResponse
      req := schema.MetadataRequest{
          Kind: commonv1.MediaKindEpisode,
          IDs:  map[string]string{"tvdb": "121361", "order": "absolute"},
      }
      require.NoError(t, bus.Request(context.Background(), events.RPCMetadataLookup, req, &resp))

      require.Empty(t, resp.Error)
      require.Equal(t, "tvdb", resp.Provider)
      require.Len(t, resp.Results, 2)
      var ep pkgmetadata.Episode
      require.NoError(t, json.Unmarshal(resp.Results[0], &ep))
      require.Equal(t, "Pilot", ep.Title)
  }

  func TestServeRPCLookupEpisodesRequiresATVDBID(t *testing.T) {
      reg := &pkgmetadata.Registry{}
      bus := newTestBus(t)
      require.NoError(t, ServeRPC(bus, reg))

      var resp schema.MetadataResponse
      req := schema.MetadataRequest{Kind: commonv1.MediaKindEpisode, IDs: map[string]string{"order": "official"}}
      require.NoError(t, bus.Request(context.Background(), events.RPCMetadataLookup, req, &resp))
      require.NotEmpty(t, resp.Error)
  }
  ```
  (add `"fmt"` and `"time"` to `rpc_test.go`'s imports.) Run `go test ./catalogarr/metadata/... -run TestServeRPCLookupListsEpisodes -v` and `-run TestServeRPCLookupEpisodesRequiresATVDBID` — expect a build failure (`lookupEpisodes` undefined; the existing `lookup()` sends `MediaKindEpisode` into `Registry.Lookup`, which has no such case and returns a generic "does not support kind" error, so the first test fails on `resp.Provider` being empty rather than on a build error — either way, red). Implement in `catalogarr/metadata/rpc.go`:
  ```go
  // lookup answers rpc.catalogarr.metadata.lookup. MediaKindEpisode is a
  // verb Task C6 needs and pkg/metadata.Registry.Lookup does not support
  // (its switch covers movie/series/artist/author/audiobook/comic only,
  // because "first entity from the first provider that succeeds" is the
  // wrong shape for a list) — dispatch it to lookupEpisodes before falling
  // through to Registry.Lookup for every other kind.
  func lookup(ctx context.Context, reg *pkgmetadata.Registry, req schema.MetadataRequest) schema.MetadataResponse {
      if req.Kind == commonv1.MediaKindEpisode {
          return lookupEpisodes(ctx, reg, req)
      }
      v, err := reg.Lookup(ctx, req.Kind, req.IDs)
      if err != nil {
          return schema.MetadataResponse{Kind: req.Kind, Error: err.Error()}
      }
      result, err := json.Marshal(v)
      if err != nil {
          return schema.MetadataResponse{Kind: req.Kind, Error: err.Error()}
      }
      return schema.MetadataResponse{Kind: req.Kind, IDs: idsOf(v), Result: result}
  }

  // lookupEpisodes is the Task C6 contract: Kind=MediaKindEpisode,
  // IDs={"tvdb": tvdbID, "order": order}, order a plain string matching
  // SeriesProvider.Episodes' own parameter (not pkg/metadata's typed
  // SeasonOrder, which that method does not take). Answers with Results,
  // one JSON-encoded pkg/metadata.Episode per entry, first
  // SeriesProvider-that-succeeds over reg.Series.
  func lookupEpisodes(ctx context.Context, reg *pkgmetadata.Registry, req schema.MetadataRequest) schema.MetadataResponse {
      tvdbID, ok := req.IDs[pkgmetadata.KeyTVDB]
      if !ok {
          return schema.MetadataResponse{Kind: req.Kind, Error: fmt.Sprintf("metadata: episode lookup requires %q in ids", pkgmetadata.KeyTVDB)}
      }
      order := req.IDs["order"]
      var lastErr error
      for _, p := range reg.Series {
          episodes, err := p.Episodes(ctx, tvdbID, order)
          if err != nil {
              lastErr = err
              continue
          }
          return schema.MetadataResponse{Kind: req.Kind, Provider: p.Name(), Results: marshalAll(episodes)}
      }
      if lastErr == nil {
          lastErr = pkgmetadata.ErrNotFound
      }
      return schema.MetadataResponse{Kind: req.Kind, Error: lastErr.Error()}
  }
  ```
  This replaces the earlier, simpler `lookup` from Step 13 in place — the new version wraps the old body unchanged except for the `MediaKindEpisode` branch at the top. Run `go test ./catalogarr/metadata/... -run 'TestServeRPCLookup' -v` (covers both the Step 13 fixture-based test and these two) — expect PASS.
  Commit: `git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "feat(catalogarr/metadata): episode listing on rpc.catalogarr.metadata.lookup (Task C6 contract)" -- catalogarr/metadata/rpc.go catalogarr/metadata/rpc_test.go`

- [ ] **Step 16: `Setup` wiring.** Failing test first — a smoke test that `Setup` builds a registry, subscribes and serves RPC without error, using membus + envtest client (this is the one place both are needed together).

  `catalogarr/metadata/gateway_envtest_test.go` (part 1):
  ```go
  package metadata_test

  import (
      "context"
      "net/http"
      "testing"

      "github.com/jonboulle/clockwork"
      "github.com/stretchr/testify/require"

      "github.com/mediactl/clustarr/catalogarr/metadata"
      "github.com/mediactl/clustarr/pkg/events"
      "github.com/mediactl/clustarr/pkg/events/membus"
  )

  func TestSetupBuildsAndStartsTheGatewayWithNoProvidersConfigured(t *testing.T) {
      ctx := context.Background()
      c := newTestClient(t)
      bus := membus.New(clockwork.NewRealClock())
      t.Cleanup(func() { _ = bus.Close() })
      require.NoError(t, bus.Ensure(ctx, events.Default().ForSingleNode()))

      stop, err := metadata.Setup(ctx, metadata.Options{Client: c, Bus: bus, HTTPClient: http.DefaultClient})
      require.NoError(t, err)
      require.NotNil(t, stop)
      stop()
  }

  func TestSetupRejectsMissingRequiredOptions(t *testing.T) {
      _, err := metadata.Setup(context.Background(), metadata.Options{})
      require.Error(t, err)
  }
  ```
  Run `go test ./catalogarr/metadata/... -run TestSetup` — expect a build failure (`metadata.Setup` undefined). Implement `catalogarr/metadata/gateway.go`:
  ```go
  package metadata

  import (
      "context"
      "fmt"
      "net/http"

      "github.com/jonboulle/clockwork"

      catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
      "github.com/mediactl/clustarr/pkg/events"
      pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
  )

  // Options configures Setup. Client and Bus are required; everything else
  // defaults.
  type Options struct {
      Client     client.Client
      Bus        events.Bus
      HTTPClient *http.Client
      L1Size     int
      Clock      clockwork.Clock
  }

  // Setup builds the metadata gateway (the Registry from every enabled
  // MetadataProvider, the two-tier cache, the work-queue Handler and the RPC
  // responders) and starts consuming. It is RoleMetadata's entire job;
  // catalogarr/run.go's setupWorkers is expected to call this once, guarded
  // by o.Role.Has(catalogarr.RoleMetadata) (a different Phase C task's path
  // — see "Interfaces — Produces").
  func Setup(ctx context.Context, o Options) (stop func(), err error) {
      if o.Client == nil || o.Bus == nil {
          return nil, fmt.Errorf("metadata: Client and Bus are required")
      }
      httpClient := o.HTTPClient
      if httpClient == nil {
          httpClient = http.DefaultClient
      }
      clock := o.Clock
      if clock == nil {
          clock = clockwork.NewRealClock()
      }
      l1Size := o.L1Size
      if l1Size <= 0 {
          l1Size = 4096 // a homelab-sized working set; tune via Options.L1Size
      }

      var providers catalogv1alpha1.MetadataProviderList
      if err := o.Client.List(ctx, &providers); err != nil {
          return nil, fmt.Errorf("metadata: list MetadataProviders: %w", err)
      }
      reg, err := BuildRegistry(ctx, o.Client, providers.Items, httpClient)
      if err != nil {
          return nil, err
      }

      l1, err := pkgmetadata.NewLRUCache(l1Size, clock)
      if err != nil {
          return nil, fmt.Errorf("metadata: build L1 cache: %w", err)
      }
      cache := newTieredCache(l1, newKVCache(o.Bus.KV(events.BucketMetadataCache), clock))

      spec, ok := events.Default().Consumer(events.ConsumerCatalogMetadata)
      if !ok {
          return nil, fmt.Errorf("metadata: consumer %q missing from the default topology", events.ConsumerCatalogMetadata)
      }
      h := &Handler{Client: o.Client, Registry: reg, Cache: cache}
      stopSub, err := o.Bus.Subscribe(ctx, spec.Subscription(), h.Handle)
      if err != nil {
          return nil, fmt.Errorf("metadata: subscribe: %w", err)
      }

      if err := ServeRPC(o.Bus, reg); err != nil {
          stopSub()
          return nil, err
      }
      return stopSub, nil
  }
  ```
  (add `"sigs.k8s.io/controller-runtime/pkg/client"` to the imports). Run `KUBEBUILDER_ASSETS=... go test ./catalogarr/metadata/... -run TestSetup -v` — expect PASS.
  Commit: `git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "feat(catalogarr/metadata): Setup wires the registry, cache, worker and RPC" -- catalogarr/metadata/gateway.go catalogarr/metadata/gateway_envtest_test.go`

- [ ] **Step 17: the two-manager SSA no-clobber proof.** This is the specific envtest case the task brief calls for. Mirror `pkg/k8s/patch_envtest_test.go`'s `TestPatchStatusTwoManagersDoNotClobber` exactly, but for `Movie`: `catalogarr` (a different task's controller) owns `status.phase`/`status.conditions`; this task's `catalogarr-worker` owns `status.metadata`; applying one must never erase the other. Write the test first (it should fail only if `k8s.PatchStatus`'s SSA behaviour or the two apply configurations overlap a field — which they must not, by construction — so this step is a proof, and failure here means a real bug to fix, not a test to weaken).

  Append to `gateway_envtest_test.go`:
  ```go
  func TestTwoManagerSSASplitDoesNotClobberEitherSide(t *testing.T) {
      ctx := context.Background()
      c := newTestClient(t)
      const ns, name = "noclobber", "inception"
      newMovie(t, ctx, c, ns, name, 27205)

      // The item controller (a different task) sets phase and conditions.
      controllerAC := catalogac.Movie(name, ns).WithStatus(
          catalogac.MovieStatus().
              WithPhase(catalogv1alpha1.MoviePhaseUnavailable).
              WithConditions(metav1ac.Condition().
                  WithType(catalogv1alpha1.MovieConditionMetadataReady).
                  WithStatus(metav1.ConditionFalse).
                  WithReason("Pending").
                  WithMessage("metadata not yet fetched").
                  WithLastTransitionTime(metav1.Now())),
      )
      _, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarr, controllerAC)
      require.NoError(t, err)

      // This task's worker patches status.metadata only.
      workerAC := catalogac.Movie(name, ns).WithStatus(
          catalogac.MovieStatus().WithMetadata(
              catalogac.MovieMetadata().WithTitle("Inception").WithRuntimeMinutes(148)),
      )
      _, err = k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrWorker, workerAC)
      require.NoError(t, err)

      var got catalogv1alpha1.Movie
      require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got))
      require.Equal(t, catalogv1alpha1.MoviePhaseUnavailable, got.Status.Phase, "the worker's apply erased the controller's phase")
      require.Len(t, got.Status.Conditions, 1, "the worker's apply erased the controller's conditions")
      require.NotNil(t, got.Status.Metadata)
      require.Equal(t, "Inception", got.Status.Metadata.Title)

      // The controller re-applies (e.g. it observed status.metadata changed
      // and flips MetadataReady) — the worker's metadata must survive.
      controllerAC2 := catalogac.Movie(name, ns).WithStatus(
          catalogac.MovieStatus().
              WithPhase(catalogv1alpha1.MoviePhaseWanted).
              WithConditions(metav1ac.Condition().
                  WithType(catalogv1alpha1.MovieConditionMetadataReady).
                  WithStatus(metav1.ConditionTrue).
                  WithReason("Fetched").
                  WithMessage("metadata fetched").
                  WithLastTransitionTime(metav1.Now())),
      )
      _, err = k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarr, controllerAC2)
      require.NoError(t, err)

      require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got))
      require.Equal(t, catalogv1alpha1.MoviePhaseWanted, got.Status.Phase)
      require.NotNil(t, got.Status.Metadata, "the controller's re-apply erased the worker's metadata")
      require.Equal(t, "Inception", got.Status.Metadata.Title)

      var sawCatalogarr, sawWorker bool
      for _, e := range got.ManagedFields {
          if e.Subresource != "status" {
              continue
          }
          sawCatalogarr = sawCatalogarr || e.Manager == string(k8s.ManagerCatalogarr)
          sawWorker = sawWorker || e.Manager == string(k8s.ManagerCatalogarrWorker)
      }
      require.True(t, sawCatalogarr && sawWorker, "both field managers must own a status entry")
  }
  ```
  Add `catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"` and `metav1ac "k8s.io/client-go/applyconfigurations/meta/v1"` (matching `pkg/k8s/patch.go`'s own import of that package) to the imports. Run `KUBEBUILDER_ASSETS=... go test ./catalogarr/metadata/... -run TestTwoManagerSSASplitDoesNotClobberEitherSide -v` — expect PASS (SSA field-level ownership makes this pass by construction once `buildMovieMetadataAC`/`Handle` never touch `phase`/`conditions`, which Step 7/10 already established; if it fails, the bug is in this task's code, not the test).
  Commit: `git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "test(catalogarr/metadata): prove the catalogarr/catalogarr-worker SSA split on Movie" -- catalogarr/metadata/gateway_envtest_test.go`

---

**Verification:**
```bash
go build ./catalogarr/...
go vet ./catalogarr/...
go test ./catalogarr/metadata/...                              # envtest suites SKIP here — confirm the SKIP lines appear, don't mistake them for a pass
KUBEBUILDER_ASSETS="$(setup-envtest use 1.37.0 -p path)" go test ./catalogarr/metadata/... -v   # every test, including the envtest ones, must run and PASS
go test ./pkg/obs/metrics/...                                  # the new series must not break the cardinality guard
golangci-lint-v2 run ./catalogarr/metadata/... ./pkg/obs/metrics/...   # forbidigo must show zero Status().Update/Patch calls
```

**Done when:**
- [ ] `catalogarr/metadata/` builds and every unit test (`settle`, `cache`, `tieredcache`, `limiter`, `target`, `patch`, `registry`, `rpc`) passes with no `KUBEBUILDER_ASSETS` set, and the file-level `_test.go`s that don't need an apiserver actually run rather than skip.
- [ ] Every `*_envtest_test.go` (`worker_envtest_test.go`, `gateway_envtest_test.go`) passes under `make test` / `KUBEBUILDER_ASSETS` set — not silently skipped.
- [ ] `Handler.Handle` never calls `k8s.PatchStatus` with anything but a `status.metadata`-only apply configuration under `k8s.ManagerCatalogarrWorker` — verified by `TestTwoManagerSSASplitDoesNotClobberEitherSide` and by inspection of `worker.go`.
- [ ] A rate-limited provider error becomes `events.Retry` honouring the provider's `RetryAfter`; a not-found error becomes `events.Discard`; an unsupported `MediaKind` becomes `events.Discard`; any other error passes through for the subscription's own backoff.
- [ ] `clustarr_metadata_cache_hits_total{tier}` is registered, incremented on every `tieredCache.Get`, labelled only by `tier` (l1/l2/miss) — never by title, provider or id — and `pkg/obs/metrics`'s cardinality guard test still passes.
- [ ] `BuildRegistry` wires exactly the six implemented provider types (tmdb, tvdb, musicbrainz, openlibrary, audnexus, comicvine), skips disabled providers and unimplemented types, and errors clearly on a missing/short secret.
- [ ] `ServeRPC` answers `lookup`, `search` and `resolve` on `clustarr.rpc.catalogarr.metadata.*` without touching any Kubernetes object.
- [ ] `lookup` answers the Task C6 episode-listing contract: `Kind: MediaKindEpisode, IDs: {"tvdb": ..., "order": ...}` → `Results` of JSON-encoded `pkg/metadata.Episode`, routed to `SeriesProvider.Episodes`, not through `Registry.Lookup` (which has no `MediaKindEpisode` case) — confirmed by `TestServeRPCLookupListsEpisodesForTaskC6`.
- [ ] `Setup` is the single exported entry point another task's `catalogarr/run.go` needs to wire `RoleMetadata` — confirmed by `TestSetupBuildsAndStartsTheGatewayWithNoProvidersConfigured`.
- [ ] `pkg/obs/metrics/domain.go`'s diff is exactly one appended `var (...)` block — no existing line touched.
- [ ] Every file carries the GPL-3.0 header from `hack/boilerplate.go.txt`.
- [ ] `golangci-lint-v2 run ./catalogarr/metadata/...` is clean, in particular forbidigo (no `Status().Update`/`Status().Patch` outside `pkg/k8s`).

---

### Task C6: the Movie, Series and Episode controllers

> **Controller amendments (binding; they override the text below where they differ).**
>
> 1. **Do not duplicate `FileState`/`DownloadOverlay` between `movie/` and `episode/`.** The section flags this as the one deliberate duplication because the task owns no shared directory — so this amendment gives it one. Task C6 additionally owns **`catalogarr/controller/rollup/`**; put both pure functions there, with their table tests, and import them from both controllers. Verbatim duplication of a logic block is a named review defect in this project (Phase B's reviews raised it twice), and a shared package of pure functions costs nothing.
> 2. Everything else in the amended section stands, including: Series takes `Owns(&Episode{})` guarded by `StatusFieldChanged` on `HasFile` rather than a direct watch (its status has no `HasFile`/`ActiveDownloadRef` fields); the `Download` leg is built and unit-tested here but only exercised end to end in Phase D; `MonitorSpecials`/`UnmonitorSpecials` touch season 0 only; and the recency windows are named exported constants documented as chosen defaults.


**Files:**
- Create: `catalogarr/controller/movie/doc.go`, `catalogarr/controller/movie/availability.go`, `catalogarr/controller/movie/availability_test.go`, `catalogarr/controller/movie/path.go`, `catalogarr/controller/movie/path_test.go`, `catalogarr/controller/movie/phase.go`, `catalogarr/controller/movie/phase_test.go`, `catalogarr/controller/movie/filestate.go`, `catalogarr/controller/movie/filestate_test.go`, `catalogarr/controller/movie/downloadoverlay.go`, `catalogarr/controller/movie/downloadoverlay_test.go`, `catalogarr/controller/movie/reconciler.go`, `catalogarr/controller/movie/reconciler_test.go`
- Create: `catalogarr/controller/series/doc.go`, `catalogarr/controller/series/order.go`, `catalogarr/controller/series/order_test.go`, `catalogarr/controller/series/fanout.go`, `catalogarr/controller/series/fanout_test.go`, `catalogarr/controller/series/path.go`, `catalogarr/controller/series/path_test.go`, `catalogarr/controller/series/rollup.go`, `catalogarr/controller/series/rollup_test.go`, `catalogarr/controller/series/phase.go`, `catalogarr/controller/series/phase_test.go`, `catalogarr/controller/series/reconciler.go`, `catalogarr/controller/series/reconciler_test.go`
- Create: `catalogarr/controller/episode/doc.go`, `catalogarr/controller/episode/phase.go`, `catalogarr/controller/episode/phase_test.go`, `catalogarr/controller/episode/filestate.go`, `catalogarr/controller/episode/filestate_test.go`, `catalogarr/controller/episode/downloadoverlay.go`, `catalogarr/controller/episode/downloadoverlay_test.go`, `catalogarr/controller/episode/reconciler.go`, `catalogarr/controller/episode/reconciler_test.go`
- Do NOT modify: `catalogarr/run.go` (see "Registration lines for C12" at the end), `api/**`, `pkg/**`.

**Path ownership:** `catalogarr/controller/movie/`, `catalogarr/controller/series/`, `catalogarr/controller/episode/`, and nothing else. These three packages are disjoint from every other Phase C task's paths.

**Read first:**
- Spec §8.1 (Want) in full — it is quoted and walked step by step below, so read it once for the shape, then follow this section's breakdown rather than re-deriving it.
- Spec §4.2 — `Movie`, `Series`, `Episode` field lists (also read the *generated* types below; where they disagree, the generated type wins and each disagreement is called out inline).
- Spec §8.8 (failure handling): `RequeueAfter` only, `reconcile.TerminalError` for an invalid spec, `RecoverPanic`, `ReconciliationTimeout 5m`, conditions carry `observedGeneration`, Events via `mgr.GetEventRecorder`.
- `docs/research/quality.md` §9 "Radarr minimum availability & monitoring" (the `IsAvailable` algorithm — this is where it actually lives, **not** `metadata.md` as the assigning brief says; see "Where the brief was wrong" below) and the `Add-time monitoring` / `MonitorTypes` paragraph a few lines above it (`EpisodeMonitoredService` semantics for `AddOptions.Monitor`).
- `pkg/metadata/refresh.go` in full (already read for you below — `RefreshTTL`'s hard-refresh-after-180d branch means "missing" and "stale" are the same check once you pass a zero `time.Time`).
- `docs/research/k8s.md` §5 "Reconcile skeleton rules" and §8; `pkg/k8s/patch_envtest_test.go` in full — this is the *only* real envtest example in the tree today and is the pattern to copy for `newTestClient`. Section 13's `TranscodeJobReconciler` example patches status with `client.MergeFromWithOptions` — **do not follow that part**; it predates `pkg/k8s.PatchStatus` and `.Status().Patch()` is forbidigo-banned outside `pkg/k8s` (`.golangci.yml`). Use `k8s.PatchStatus` everywhere in this task.
- The real types: `api/catalog/v1alpha1/movie_types.go`, `series_types.go`, `episode_types.go`, `shared_types.go`, `rootfolder_types.go`; `api/common/v1alpha1/media_types.go`.
- `go doc ./pkg/k8s`, `go doc ./pkg/naming`, `go doc -all ./pkg/naming` (read `pkg/naming/series.go`'s `SeriesFolder`/`EpisodeFile` and `pkg/naming/render.go`'s token table directly — the `{Series ...}` tokens read `Context.SeriesTitle`, not `Context.Title`; verified by `grep -n 'SeriesTitle' pkg/naming/*.go`), `go doc ./pkg/metadata`, `go doc ./pkg/events`, `go doc ./pkg/events/schema`.
- `api/applyconfiguration/catalog/catalog/v1alpha1/{movie,series,episode}*.go` — yes, `catalog/catalog` is doubled; that is CLAUDE.md's documented controller-gen quirk, not a typo.

**Where the brief was wrong:** the assigning brief points at `docs/research/metadata.md`'s "availability/TTL section" for both availability and refresh TTL. Only the TTL half is there (§4 general shape; the concrete function is the real `pkg/metadata.RefreshTTL`, already merged). The `IsAvailable` algorithm — the thing this task actually needs for `Phase=Unavailable`/`Wanted` — is in `docs/research/quality.md` §9, not `metadata.md`. Followed `quality.md` §9, quoted verbatim below.

**Dependencies:** none new. Everything used here is already in `go.mod`: `sigs.k8s.io/controller-runtime v0.25.1`, `k8s.io/api v0.37.0`, `k8s.io/apimachinery v0.37.0`, `k8s.io/client-go v0.37.0`, `k8s.io/utils v0.0.0-20260626114624-be93311217bd` (for `ptr.Deref`), `github.com/stretchr/testify v1.12.1`.

---

## Interfaces — Consumes

Real, already-merged signatures this task builds on:

```go
// pkg/k8s
func PatchStatus[T ApplyConfiguration](ctx, c client.Client, fm FieldManager, ac T, opts ...client.SubResourceApplyOption) (T, error)
func EnsureFinalizer(ctx, c client.Client, obj client.Object, name string) (bool, error)
func RemoveFinalizer(ctx, c client.Client, obj client.Object, name string) (bool, error)
func IsDeleting(obj client.Object) bool
func FinalizerFor(obj runtime.Object, scheme *runtime.Scheme) (string, error)
func GenerationChanged() predicate.Predicate
func StatusFieldChanged[T comparable](extract func(client.Object) T) predicate.Predicate
func Or(ps ...predicate.Predicate) predicate.Predicate
func MarkTrue/MarkFalse(obj client.Object, conditions *[]metav1.Condition, condType, reason, message string, args ...any) bool
func MustNewScheme() *runtime.Scheme
const ManagerCatalogarr FieldManager = "catalogarr"   // this task's field manager for phase/conditions/path/etc.
                                                        // NOT ManagerCatalogarrWorker -- that is the gateway's (Task C5), status.metadata only.

// pkg/metadata
func RefreshTTL(kind commonv1.MediaKind, state string, lastRefreshed time.Time) time.Duration
const RefreshStateAnnounced, RefreshStateInCinemas, RefreshStateReleasedRecent, RefreshStateReleasedOld = "announced", "inCinemas", "releasedRecent", "releasedOld"
const RefreshStateContinuing, RefreshStateEndedRecent = "continuing", "endedRecent"
type Episode struct {
    IDs ExternalIDs
    SeasonNumber, EpisodeNumber int32
    AbsoluteNumber *int32
    Title, Overview string
    AirDate *time.Time
    Runtime int32
    FinaleType string
}

// pkg/events
const RPCMetadataLookup = "clustarr.rpc.catalogarr.metadata.lookup"
func WorkMetadataSubject(p Priority, mediaKey string) string   // "clustarr.work.catalogarr.metadata.<p>.<mediaKey>"
func MsgIDForObject(uid string, generation int64, task string) string  // "<uid>:<generation>:<task>"
const PriorityNormal Priority = "normal"
var ErrQueueFull error
type Publisher interface { Publish(ctx, subject string, e *Envelope, opts ...PublishOption) (Receipt, error) }
type Requester interface { Request(ctx, subject string, in, out any) error; Serve(...) error }
type Bus interface { Publisher; Subscriber; Requester; KV(bucket string) KV; Ensure(ctx, Topology) error; Close() error }

// pkg/events/schema
type MetadataTask struct { MediaRef commonv1.MediaRef; RefreshEpoch int64 }  // Schema() "catalog.MetadataTask.v1"
type MetadataRequest struct { Kind commonv1.MediaKind; IDs map[string]string; Text string; Year int32; Region, Language string }
type MetadataResponse struct { Kind commonv1.MediaKind; Provider string; IDs map[string]string; Result []byte; Results [][]byte; CachedAt *time.Time; Error string }

// pkg/naming
func NewEngine(cfg Config) Engine
func (e Engine) MovieFolder(c Context) (string, error)
func (e Engine) SeriesFolder(c Context) (string, error)   // reads c.SeriesTitle / c.SeriesYear / c.TvdbID -- NOT c.Title
type Context struct { Kind commonv1.MediaKind; Title, OriginalTitle string; Year int; TmdbID, TvdbID, ImdbID string; SeriesTitle string; SeriesYear int; /* ...+more, unused here */ }
type Config struct { Dialect Dialect; ColonReplacement ColonReplacement; MultiEpisodeStyle MultiEpisodeStyle; Overrides map[string]string; ReplaceSpaces bool; Separator, Case string }

// pkg/quality and pkg/quality/catalogue -- added in review for the MediaFile
// rollup (Steps 20-25); both are Phase B, already merged, pure Go (no client).
func quality.FromCRD(p *catalogv1alpha1.QualityProfile, cat *catalogue.Catalogue) (quality.Profile, []error)
func catalogue.LoadedCatalogue() *catalogue.Catalogue   // cheap singleton accessor, call it directly, no need to cache it on the Reconciler
func (p quality.Profile) CutoffMet(q commonv1.Quality) bool  // idx <= p.CutoffIndex; false if q is not Allowed at all

// api/download/v1alpha1 -- read-only from this task; grabarr (Phase D) is
// the sole writer under field manager "grabarr"/"grabarr-engine".
type DownloadPhase string // Pending Assigned Queued Downloading Paused Completed Seeding Imported Failed Blocklisted Removing
type DownloadStatus struct { Phase DownloadPhase; /* ...+more, unused here */ }
type Download struct { /* ObjectMeta */ Status DownloadStatus }
```

Generated apply configurations (import `catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"`):

```go
func catalogac.Movie(name, namespace string) *MovieApplyConfiguration
func catalogac.MovieStatus() *MovieStatusApplyConfiguration
// .WithObservedGeneration/.WithPhase/.WithConditions/.WithAddOptionsApplied/.WithAvailable/.WithAvailableAt/.WithPath(...)
// deliberately NOT .WithMetadata(...) -- that field belongs to ManagerCatalogarrWorker (Task C5). Never set it here.
func catalogac.Series(name, namespace string) *SeriesApplyConfiguration
func catalogac.SeriesStatus() *SeriesStatusApplyConfiguration
// .WithPhase/.WithConditions/.WithAddOptionsApplied/.WithPath/.WithSeasons(...)/.WithEpisodeCount/.WithEpisodeFileCount(...)
func catalogac.Episode(name, namespace string) *EpisodeApplyConfiguration
func catalogac.EpisodeStatus() *EpisodeStatusApplyConfiguration
// .WithPhase/.WithConditions/.WithAired-condition-via-Conditions/.WithTvdbID/.WithTitle/.WithOverview/.WithAirDate/.WithRuntimeMinutes/.WithAbsoluteNumber(...)
```

**ASSUMED CONTRACT — Task C5 must implement this exactly, or C6's episode fan-out fails at integration.** There is no dedicated "list a series' episodes" RPC verb in the real `pkg/events/schema`; `clustarr.rpc.catalogarr.metadata.lookup` is generic (one opaque `Result`/`Results` blob per `MetadataRequest`). Per the wave note in `shared-context.md` ("C6 publishes metadata work and C5 consumes it, but they meet at a bus subject rather than a Go symbol"), this task defines the shape it needs on that existing subject, using only real, already-merged schema types:

- Request: `schema.MetadataRequest{Kind: commonv1.MediaKindEpisode, IDs: map[string]string{"tvdb": strconv.FormatInt(series.Spec.TvdbID, 10), "order": string(effectiveOrder)}}` over `bus.Request(ctx, events.RPCMetadataLookup, req, &resp)`.
- Response: `schema.MetadataResponse{Kind: commonv1.MediaKindEpisode, Results: [][]byte}`, each element `json.Unmarshal`-able into `metadata.Episode`. A non-empty `Error` field or a non-nil `err` from `Request` is a normal failure (see Step 10).

If Task C5 lands a different shape, this is the seam to reconcile — flag it in that task's review, do not silently reinterpret this one.

## Interfaces — Produces

```go
// catalogarr/controller/movie
func Availability(min catalogv1alpha1.MinimumAvailability, meta *catalogv1alpha1.MovieMetadata, delayDays int32, now time.Time) (available bool, availableAt time.Time)
// Phase gained hasFile/cutoffMet in review (Steps 20-22): a file already
// imported outranks availability entirely, so the hasFile checks are
// evaluated before !available.
func Phase(monitored, metadataReady, available, hasFile, cutoffMet bool) catalogv1alpha1.MoviePhase
func Path(rootPath string, folderOverride *string, engine naming.Engine, ctx naming.Context) (string, error)
// FileState and DownloadOverlay: new in review, Steps 20-21.
func FileState(mf *catalogv1alpha1.MediaFile, profile *quality.Profile) (hasFile bool, fileRef *string, fileQuality *commonv1.Quality, fileFormatScore int32, cutoffMet bool)
func DownloadOverlay(dl *downloadv1alpha1.Download) (phase catalogv1alpha1.MoviePhase, active bool)
type Reconciler struct { client.Client; Scheme *runtime.Scheme; Recorder record.EventRecorder; Bus events.Publisher }
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error

// catalogarr/controller/series
func EffectiveEpisodeOrder(seriesType catalogv1alpha1.SeriesType, order catalogv1alpha1.EpisodeOrder) catalogv1alpha1.EpisodeOrder
func EpisodeName(seriesName string, seriesType catalogv1alpha1.SeriesType, season, episode int32, airDate *time.Time) string
func InitialEpisodeMonitored(mode catalogv1alpha1.SeriesMonitorMode, ep EpisodeCandidate, all []EpisodeCandidate, runStatus catalogv1alpha1.SeriesRunStatus, now time.Time) bool
func DesiredEpisodes(series *catalogv1alpha1.Series, addOptionsApplied bool, existingNames map[string]bool, episodes []metadata.Episode, now time.Time) []DesiredEpisode
func Rollup(episodes []catalogv1alpha1.Episode) (seasons []catalogv1alpha1.SeasonStatus, episodeCount, episodeFileCount int32)
func Phase(monitored, metadataReady, episodesSynced bool) catalogv1alpha1.SeriesPhase
type Reconciler struct { client.Client; Scheme *runtime.Scheme; Recorder record.EventRecorder; Bus events.Bus }
// SetupWithManager gained Owns(&catalogv1alpha1.Episode{}, ...) in review
// (Step 23) so an owned Episode's own HasFile write (from the Episode
// controller's own MediaFile watch, Steps 24-25) re-triggers Series's Rollup.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error

// catalogarr/controller/episode
// Phase gained hasFile/cutoffMet in review (Steps 20, 25), same reasoning as Movie's.
func Phase(monitored bool, airDate *metav1.Time, hasFile, cutoffMet bool, now time.Time) catalogv1alpha1.EpisodePhase
// FileState and DownloadOverlay: new in review, Step 24 -- same shape as
// Movie's, duplicated rather than shared (see "why duplicated" below Step 25).
func FileState(mf *catalogv1alpha1.MediaFile, profile *quality.Profile) (hasFile bool, fileRef *string, fileQuality *commonv1.Quality, fileFormatScore int32, cutoffMet bool)
func DownloadOverlay(dl *downloadv1alpha1.Download) (phase catalogv1alpha1.EpisodePhase, active bool)
type Reconciler struct { client.Client; Scheme *runtime.Scheme; Recorder record.EventRecorder }
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error
```

**Scope boundary — partly closed in review.** §3's single-writer rule means only *this* controller may ever write `Movie.status.phase`/`HasFile`/`FileRef`/`ActiveDownloadRef`/`CutoffMet` (Episode: same fields), so no other task in C1–C12 could ever add the `Download`/`MediaFile` watches those fields need — they have to live here. Ruling from review: **add the `MediaFile` watch now** (Steps 20-22 for Movie, 24-25 for Episode) — importarr's rescan (Task C10) creates `MediaFile`s, and Phase C's own end-to-end scenario 7 asserts the catalog item reflects them, so this cannot wait for a later phase. **Add the `Download` watch too** (same steps), mapped by `status.activeDownloadRef`, setting `Downloading`/`Delayed` and clearing the ref on a terminal phase — but nothing in Phase C ever advances a `Download` past `Pending`/`Assigned` (grabarr, the only writer of `Download.status.phase` beyond that point, is Phase D), so this leg is built and unit-tested against synthetic `Download` fixtures now and is only exercised end to end once grabarr exists. It is better to have the watch in place and idle than to retrofit it into a single-writer reconciler later.

Series is the one case where this stays indirect: `SeriesStatus` (the generated type) has no `HasFile`/`FileRef`/`CutoffMet`/`ActiveDownloadRef` fields at all — only the existing `EpisodeCount`/`EpisodeFileCount`/`Seasons[]` rollup, and no `Downloading` value in `SeriesPhase`'s enum. So Series gets no direct `Download`/`MediaFile` watch; it watches its owned `Episode`s (`Owns`, Step 23) and its existing `Rollup` (Step 14) picks up an Episode's `HasFile` flip automatically, because that flip is exactly what Episode's own new `MediaFile` watch (Step 25) now produces.

Still open, and still nobody's in the current breakdown: `PendingGrab`/`Phase=Delayed` driven by an actual `DelayProfile` decision (§8.2, owned by Task C8/C9's search/grab worker) — this review only wired `Delayed` as `DownloadOverlay`'s best-fit label for a `Download` that exists but has no engine yet (see Step 21), not the pre-grab delay wait, which has no `Download` object at all and needs its own mechanism (most likely the Movie/Series/Episode controller polling the `clustarr-pending` KV bucket during reconcile). Flag that one too.

**mediaKey convention (movie and series only need it):** `mediaKey := movie.Namespace + "/" + movie.Name`, matching `events.Envelope.Key`'s documented shape ("`<namespace>/<name>` of the owning custom resource") exactly — not invented. No kind prefix; this repo has no cross-kind mediaKey collision guard yet (a Movie and a Series sharing a name+namespace would share a `clustarr-leases`/`clustarr-pending` KV key). Flag this too: it should probably move into `pkg/events` as an exported helper once a second task (C8/C9) needs the identical string.

---

## Movie: the pure functions

**`Availability`** ports `docs/research/quality.md` §9's `Movie.IsAvailable(delayDays)` verbatim:

```
if MinimumAvailability in {TBA, Announced}: date = MinValue   (always available)
elif MinimumAvailability == InCinemas && meta.InCinemas != null: date = meta.InCinemas
else:  # Released, or InCinemas with no known InCinemas date
    date = min(PhysicalRelease, DigitalRelease) if both
         | whichever exists
         | InCinemas + 90 days if only InCinemas
         | MaxValue (never)
if date is MinValue or MaxValue: return now >= date
return now >= date + delayDays
```

Note the delay is applied to *every* known date, including the `InCinemas`-known branch — it is not restricted to the `Released` branch. Read the pseudocode that way before writing the table.

- [ ] **Step 1: `Availability` — failing test, real cases**

`catalogarr/controller/movie/availability_test.go`:
```go
package movie_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/controller/movie"
)

func mt(y int, m time.Month, d int) *metav1.Time {
	t := metav1.NewTime(time.Date(y, m, d, 0, 0, 0, 0, time.UTC))
	return &t
}

func TestAvailability(t *testing.T) {
	now := time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)

	cases := []struct {
		name         string
		min          catalogv1alpha1.MinimumAvailability
		meta         *catalogv1alpha1.MovieMetadata
		delayDays    int32
		wantAvail    bool
		wantAt       time.Time
	}{
		{"tba always available, nil metadata", catalogv1alpha1.MinimumAvailabilityTBA, nil, 0, true, time.Time{}},
		{"announced always available", catalogv1alpha1.MinimumAvailabilityAnnounced, &catalogv1alpha1.MovieMetadata{}, 30, true, time.Time{}},
		{"inCinemas known, past date, no delay", catalogv1alpha1.MinimumAvailabilityInCinemas,
			&catalogv1alpha1.MovieMetadata{InCinemas: mt(2026, 8, 1)}, 0, true, time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)},
		{"inCinemas known, future date", catalogv1alpha1.MinimumAvailabilityInCinemas,
			&catalogv1alpha1.MovieMetadata{InCinemas: mt(2026, 10, 1)}, 0, false, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)},
		{"inCinemas known, delay pushes it past now", catalogv1alpha1.MinimumAvailabilityInCinemas,
			&catalogv1alpha1.MovieMetadata{InCinemas: mt(2026, 9, 10)}, 14, false, time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)},
		{"inCinemas requested but unknown falls through to released-style logic",
			catalogv1alpha1.MinimumAvailabilityInCinemas,
			&catalogv1alpha1.MovieMetadata{PhysicalRelease: mt(2026, 9, 1)}, 0, true, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)},
		{"released, both dates known, picks the earlier", catalogv1alpha1.MinimumAvailabilityReleased,
			&catalogv1alpha1.MovieMetadata{PhysicalRelease: mt(2026, 10, 1), DigitalRelease: mt(2026, 9, 1)}, 0, true, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)},
		{"released, only digital known", catalogv1alpha1.MinimumAvailabilityReleased,
			&catalogv1alpha1.MovieMetadata{DigitalRelease: mt(2026, 9, 20)}, 0, false, time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)},
		{"released, only inCinemas known, +90 days", catalogv1alpha1.MinimumAvailabilityReleased,
			&catalogv1alpha1.MovieMetadata{InCinemas: mt(2026, 1, 1)}, 0, true, time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)},
		{"released, nothing known, never available", catalogv1alpha1.MinimumAvailabilityReleased,
			&catalogv1alpha1.MovieMetadata{}, 0, false, time.Time{}},
		{"released, nil metadata, never available", catalogv1alpha1.MinimumAvailabilityReleased, nil, 0, false, time.Time{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			avail, at := movie.Availability(c.min, c.meta, c.delayDays, now)
			assert.Equal(t, c.wantAvail, avail)
			assert.True(t, c.wantAt.Equal(at), "availableAt = %v, want %v", at, c.wantAt)
		})
	}
}
```
Run: `go test ./catalogarr/controller/movie/... -run TestAvailability` — fails, package does not exist yet.

Implement `availability.go` to the pseudocode above (delay as `time.Duration(delayDays) * 24 * time.Hour`; "known" tracks whether a date was found at all, distinct from the zero `time.Time{}` returned for TBA/Announced/never so callers can tell "always" from "never" — a `RequeueAfter` computed from a zero `availableAt` must never be used; see Step 5). Run again: green.
`git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "feat(catalogarr): port Radarr's Movie.IsAvailable as a pure function" -- catalogarr/controller/movie/availability.go catalogarr/controller/movie/availability_test.go`

- [ ] **Step 2: `Phase` (Movie) — failing test, then implementation**

Written directly to the 5-argument form `Steps 20-22` add `hasFile`/`cutoffMet` for — no point writing the 3-arg version first and widening it later in the same task.

`phase_test.go`:
```go
func TestPhase(t *testing.T) {
	cases := []struct {
		name                                     string
		monitored, metaReady, available          bool
		hasFile, cutoffMet                       bool
		want                                     catalogv1alpha1.MoviePhase
	}{
		{"unmonitored wins over everything", false, true, true, false, false, catalogv1alpha1.MoviePhaseUnmonitored},
		{"monitored, no metadata yet", true, false, false, false, false, catalogv1alpha1.MoviePhasePending},
		{"monitored, metadata ready, not available", true, true, false, false, false, catalogv1alpha1.MoviePhaseUnavailable},
		{"monitored, metadata ready, available", true, true, true, false, false, catalogv1alpha1.MoviePhaseWanted},
		{"a file already imported outranks availability", true, true, false, true, true, catalogv1alpha1.MoviePhaseImported},
		{"an imported file below cutoff", true, true, true, true, false, catalogv1alpha1.MoviePhaseCutoffUnmet},
		{"hasFile with stale metadata still reports pending -- a file cannot exist before the item is monitored/known", true, false, false, true, true, catalogv1alpha1.MoviePhasePending},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, movie.Phase(c.monitored, c.metaReady, c.available, c.hasFile, c.cutoffMet))
		})
	}
}
```
Implement in this order: `!monitored` → `Unmonitored`; else `!metadataReady` → `Pending` (even if `hasFile` — the last test case is deliberately testing that a stale-metadata reconcile does not race ahead of the `Pending` gate); else `hasFile && !cutoffMet` → `CutoffUnmet`; else `hasFile && cutoffMet` → `Imported`; else `!available` → `Unavailable`; else `Wanted`. Commit path-scoped as in Step 1. `hasFile`/`cutoffMet` are always `false` until Step 22 wires the real `MediaFile` fetch — Step 7 below calls this with hardcoded `false, false` until then; say so at that call site rather than leaving it looking finished.

- [ ] **Step 3: `Path` (Movie) — failing test, then implementation**

```go
func TestPath(t *testing.T) {
	eng := naming.NewEngine(naming.Config{Dialect: naming.DialectJellyfin})
	ctx := naming.Context{Kind: commonv1.MediaKindMovie, Title: "Inception", Year: 2010, TmdbID: "27205"}

	got, err := movie.Path("/data/media/movies", nil, eng, ctx)
	require.NoError(t, err)
	assert.Equal(t, "/data/media/movies/Inception (2010) {tmdb-27205}", got) // Jellyfin preset -- confirm the exact
	                                                                          // literal against pkg/naming's own
	                                                                          // preset test fixtures before asserting;
	                                                                          // do not guess the separator/brace style.

	override := "Inception (Director's Cut)"
	got, err = movie.Path("/data/media/movies", &override, eng, ctx)
	require.NoError(t, err)
	assert.Equal(t, "/data/media/movies/Inception (Director's Cut)", got)
}
```
Before asserting the exact literal, run `go doc -all ./pkg/naming` is not enough — read `pkg/naming/preset_test.go` (or wherever `movieFolderTemplate(DialectJellyfin)`'s golden output lives) and copy the real expected string; do not invent Jellyfin's bracket/paren convention. Implement `Path` as `folderOverride != nil && *folderOverride != ""` ? use it : `engine.MovieFolder(ctx)`, then `path.Join(rootPath, folder)` (POSIX join — `/data/media/...` is always POSIX, this runs in-cluster on Linux only). Commit.

## Movie: the reconciler

- [ ] **Step 4: envtest harness + finalizer, no early return**

`reconciler_test.go` — copy `newTestClient` verbatim from `pkg/k8s/patch_envtest_test.go` (adjust `CRDDirectoryPaths` to `"../../../config/crd/bases"` — three levels up from `catalogarr/controller/movie/`), but this suite also needs a *running manager* (not just a client) because two of this task's required assertions are about watch predicates, which only fire through the real informer/workqueue path:

```go
func startManager(t *testing.T, ctx context.Context, cfg *rest.Config, bus events.Publisher) (client.Client, *reconcileCounter) {
	t.Helper()
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{Scheme: k8s.MustNewScheme(), Metrics: metricsserver.Options{BindAddress: "0"}, HealthProbeBindAddress: "0"})
	require.NoError(t, err)
	counter := &reconcileCounter{}
	r := &movie.Reconciler{Client: mgr.GetClient(), Scheme: mgr.GetScheme(), Recorder: mgr.GetEventRecorderFor("movie"), Bus: bus, OnReconcile: counter.inc}
	require.NoError(t, r.SetupWithManager(mgr))
	go func() { _ = mgr.Start(ctx) }()
	require.True(t, mgr.GetCache().WaitForCacheSync(ctx))
	return mgr.GetClient(), counter
}
```
(`OnReconcile func()` is a test-only hook field on `Reconciler`, called at the top of `Reconcile`; nil-checked so production callers never set it. This is how Step 6 counts reconciles without racing on side effects.) Write `reconcileCounter` as an `atomic.Int64`-backed helper with a `count()` method.

Test: create a `Movie` with a finalizer NOT yet present, `require.Eventually` that `k8s.FinalizerFor(&catalogv1alpha1.Movie{}, mgr.GetScheme())` (== `"catalog.clustarr.io/movie"`) is in `ObjectMeta.Finalizers`, AND that `status.phase` is already `Pending` in that *same* reconcile pass (proving the finalizer add did not early-return — assert both post-conditions from one `require.Eventually` poll, since if the finalizer add early-returned, `status.phase` would still be empty on the first sync and only appear after the *next* watch event, which a `GenerationChanged`-only predicate would never deliver).

Implement `reconciler.go`'s `Reconcile`:
```go
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	if r.OnReconcile != nil { r.OnReconcile() }
	var m catalogv1alpha1.Movie
	if err := r.Get(ctx, req.NamespacedName, &m); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if k8s.IsDeleting(&m) {
		return r.reconcileDelete(ctx, &m)
	}
	name, err := k8s.FinalizerFor(&m, r.Scheme)
	if err != nil {
		return ctrl.Result{}, reconcile.TerminalError(err)
	}
	if _, err := k8s.EnsureFinalizer(ctx, r.Client, &m, name); err != nil {
		return ctrl.Result{}, err
	}
	// NO early return here -- continue into the rest of this reconcile with
	// the same in-memory `m`.
	return r.reconcileNormal(ctx, &m)
}
```
`reconcileDelete` for now: best-effort remove the finalizer (`k8s.RemoveFinalizer`) with no other cleanup — there is nothing owned outside Kubernetes at this phase (see the mediaKey/KV note below; do not add KV cleanup you cannot test against a real bus without over-scoping this step). Commit.

- [ ] **Step 5: `addOptions` applied exactly once**

Test: create a `Movie` with `spec.addOptions.monitor = movieAndCollection`; reconcile; assert `status.addOptionsApplied == true` and (for now, since `Collection` handling is out of scope — Movie's `AddOptions.Monitor` only affects the *collection* fan-out, which has no owning task in this wave either; flag it alongside the Download/MediaFile gap) that a **second** spec change (bump `spec.tags`) does NOT re-trigger whatever `addOptions` would have done — assert this by round-tripping `status.addOptionsApplied` and confirming reconcile is idempotent (calling `Reconcile` twice back to back must produce the same status, not an error and not a second "apply").

Implement: in `reconcileNormal`, `if !m.Status.AddOptionsApplied { /* nothing to actually apply yet, since Collection fan-out is unowned; just record it */ }` — set `AddOptionsApplied: true` unconditionally in the very first status patch once metadata handling begins, guarded so a later reconcile with `addOptionsApplied` already `true` in the live object skips re-deciding it (read `m.Status.AddOptionsApplied` before building the apply configuration; if already true, omit `WithAddOptionsApplied` entirely from that call's `MovieStatusApplyConfiguration` — SSA leaves it as the previously-applied `true` either way, but omitting it is the honest way to express "not deciding this again", and it also means a stale test that patches `false` under a different simulated caller cannot silently be “fixed” by this controller). Commit.

- [ ] **Step 6: metadata missing/stale → publish `MetadataTask`, `MetadataReady=False`; QueueFull on `ErrQueueFull`**

Two tests:

1. *Happy path* — `Bus` is a `membus.New(clockwork.NewFakeClock())` (`go doc ./pkg/events/membus`), `bus.Ensure(ctx, events.Default())` called once in `TestMain`/suite setup so `CLUSTARR_WORK_CATALOGARR` exists. Reconcile a fresh `Movie` (no `status.metadata`). Assert: a message was published to `events.WorkMetadataSubject(events.PriorityNormal, ns+"/"+name)` whose `Envelope.ID == events.MsgIDForObject(string(m.UID), 0, "metadata")` (`RefreshEpoch` is always `0` in this task — see "Interfaces — Produces" note above the pure functions; a forced-refresh feature that increments it is out of scope) and whose `Data` unmarshals (via `schema.Decode`) into `schema.MetadataTask{MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: m.Name}}`; assert `status.conditions[MetadataReady].status == False` and `status.phase == Pending`.

2. *Queue full* — a tiny local fake:
```go
type fakePublisher struct{ err error }
func (f fakePublisher) Publish(ctx context.Context, subject string, e *events.Envelope, opts ...events.PublishOption) (events.Receipt, error) {
	return events.Receipt{}, f.err
}
```
wired with `err: events.ErrQueueFull`. Reconcile; assert `status.conditions[QueueFull].status == True` and the returned `ctrl.Result.RequeueAfter == time.Minute` (spec §8.1: "requeue 1m"). Assert this does NOT also flip `MetadataReady` to some other bogus state — leave it `Unknown`/absent, since the task never got to decide.

Implement the metadata-decision block in `reconcileNormal`:
```go
stale := m.Status.Metadata == nil
if !stale {
	state := movieRefreshState(m.Status.Metadata) // small unexported helper: TBA/Announced->RefreshStateAnnounced,
	                                                // InCinemas->RefreshStateInCinemas, Released within
	                                                // movie.ReleasedRecentWindow of DigitalRelease/PhysicalRelease
	                                                // ->RefreshStateReleasedRecent, else RefreshStateReleasedOld
	                                                // (docs/research/quality.md's "recent" 90-day window is for
	                                                // Sonarr's episode Recent monitor mode, NOT this --
	                                                // pkg/metadata/refresh.go's own bucket names are the authority
	                                                // on what exists, not on the cutoff between them).
	ttl := metadata.RefreshTTL(commonv1.MediaKindMovie, state, m.Status.Metadata.RefreshedAt.Time)
	stale = now.Sub(m.Status.Metadata.RefreshedAt.Time) >= ttl
}
metaReady := !stale
if stale {
	mediaKey := m.Namespace + "/" + m.Name
	env := &events.Envelope{
		ID:     events.MsgIDForObject(string(m.UID), 0, "metadata"),
		Type:   "catalog.MetadataTask",
		Schema: schema.MetadataTask{}.Schema(),
		Source: "catalogarr@" + version.Version, // match whatever pkg/version actually exports; read it first
		Key:    mediaKey,
		Time:   now,
	}
	_, encErr := schema.Encode(schema.MetadataTask{MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: m.Name}})
	// ^ Encode returns (schema, data, error); set env.Schema/env.Data from it, do not hand-roll json.Marshal.
	if _, err := r.Bus.Publish(ctx, events.WorkMetadataSubject(events.PriorityNormal, mediaKey), env); err != nil {
		if errors.Is(err, events.ErrQueueFull) {
			// set QueueFull=True, requeue 1m, return early from the *metadata*
			// decision only -- availability/phase below still runs against
			// whatever metadata is already cached, per §8.1's literal ordering.
		}
		return ctrl.Result{}, err // non-QueueFull publish errors are retried normally
	}
}
```
Declare the window as a named, exported, documented constant rather than an inline literal — ruling from review: no verified *arr value exists anywhere in this repo for it, so it must not be presented as ported:
```go
// ReleasedRecentWindow is how long after a movie's digital/physical release
// date it is still treated as RefreshStateReleasedRecent (a shorter refresh
// TTL) rather than RefreshStateReleasedOld. This is a Clustarr-chosen
// default, not a value taken from Radarr -- no verified source pins an exact
// cutoff here (docs/research/quality.md's 90-day "Recent" window is Sonarr's
// unrelated episode-monitoring mode). Tune it in one place if it turns out
// wrong.
const ReleasedRecentWindow = 30 * 24 * time.Hour
```
Run both tests green. Commit.

- [ ] **Step 7: availability, path, phase — end to end**

Test: seed a `Movie` whose `status.metadata` is already fresh (`RefreshedAt: now`, `Status: released`, `DigitalRelease: <yesterday>`) and a `RootFolder` (`spec.path: /data/media/movies`, default `Naming`). Reconcile; assert `status.available == true`, `status.path` equals what `movie.Path` computes for the same inputs, `status.phase == Wanted`, `conditions[Available].status == True`. A second case with `DigitalRelease` in the future: assert `status.phase == Unavailable` and `ctrl.Result.RequeueAfter` is within a few seconds of `availableAt.Sub(now)` (use `assert.InDelta` on `.Seconds()`, not exact equality — the reconcile takes real wall-clock time between computing `now` and returning).

Guard the zero-`availableAt` case explicitly: when `Availability` returns `(false, time.Time{})` ("never" — nothing in `status.metadata` gives a date), do **not** call `RequeueAfter(availableAt.Sub(now))` (that would be a large negative-then-wrapped duration); leave `ctrl.Result{}` and rely on the next metadata refresh (bounded by `RefreshTTL`) to re-trigger via Step 8's predicate. Add a test case for this too.

Implement: fetch the `RootFolder` named `m.Spec.RootFolderRef` in the same namespace (`client.IgnoreNotFound` is wrong here — a missing RootFolder is a spec problem the object cannot recover from on its own; set a `Ready=False` condition reason `RootFolderNotFound` and `ctrl.Result{RequeueAfter: time.Minute}`, since the RootFolder might not exist *yet* rather than never — do not `TerminalError` this one). Build `naming.Config` from `rf.Spec.Naming` (`naming.Dialect(rf.Spec.Naming.Dialect)` etc. — 1:1 string casts, confirmed by `pkg/naming`'s own package doc). Build `naming.Context{Kind: commonv1.MediaKindMovie, Title: m.Status.Metadata.Title, Year: int(m.Status.Metadata.Year), TmdbID: strconv.FormatInt(m.Spec.TmdbID, 10), ImdbID: m.Status.Metadata.ExternalIDs["imdb"]}`. Call `movie.Availability`, `movie.Path`, then `movie.Phase(monitored, metaReady, available, false, false)` — `hasFile`/`cutoffMet` are hardcoded `false` here on purpose; Step 22 replaces those two literals with the real `MediaFile`-derived values, nothing else in this call changes. Commit.

- [ ] **Step 8: metadata refresh reconciles this controller; the controller's own status write does not re-loop it**

This is the envtest the brief calls out explicitly. Build the `For(&catalogv1alpha1.Movie{})` predicate as:
```go
k8s.Or(
	k8s.GenerationChanged(),
	k8s.StatusFieldChanged(func(o client.Object) metav1.Time {
		mv, ok := o.(*catalogv1alpha1.Movie)
		if !ok || mv.Status.Metadata == nil {
			return metav1.Time{}
		}
		return mv.Status.Metadata.RefreshedAt
	}),
)
```
Test (uses the `reconcileCounter` from Step 4):
1. Create the `Movie`, `require.Eventually` the counter is `>= 1` (initial create always passes both predicates), let it settle (`require.Never` further growth for e.g. 300ms).
2. Record the counter value `n`. Patch `status.metadata` via `k8s.PatchStatus` under `k8s.ManagerCatalogarrWorker` (simulating the gateway — this task must NOT import `catalogarr/metadata`, Task C5's package; build the apply configuration directly with `catalogac.Movie(...).WithStatus(catalogac.MovieStatus().WithMetadata(...))`). `require.Eventually` the counter grows past `n` — the gateway's write must wake this controller.
3. Record the new counter value `n2`. Reconcile naturally settles and this controller's own `PatchStatus` call (Step 6/7, under `ManagerCatalogarr`, touching `Phase`/`Conditions`/`AddOptionsApplied`/`Available`/`AvailableAt`/`Path` — never `Metadata`) fires. `require.Never` the counter grows past `n2` for e.g. 500ms — the controller's own write, which does not touch `status.metadata.refreshedAt`, must not pass the predicate and cause another reconcile. (If this flakes because the controller's *first* reconcile after the gateway's write itself bumps the counter once as part of step 2's expected growth, account for that — the counter growth from step 2 to step 3 should be exactly 1, not more; assert that precisely rather than just "some growth happened".)

This is the concrete proof that `StatusFieldChanged` scoped to `Metadata.RefreshedAt` (not a blanket "any status changed") is what avoids the self-loop — a naïve `predicate.ResourceVersionChangedPredicate` here would fail this test by looping forever. Wire this predicate into `SetupWithManager`'s `For()` call. Commit.

- [ ] **Step 9: RBAC markers and manager wiring**

Add, directly above the `Reconciler` struct in `reconciler.go`:
```go
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=movies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=movies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=movies/finalizers,verbs=update
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=rootfolders,verbs=get;list;watch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch
```
`SetupWithManager`:
```go
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("movie").
		For(&catalogv1alpha1.Movie{}, builder.WithPredicates(moviePredicate())).
		WithOptions(controller.Options{RecoverPanic: ptr.To(true), ReconciliationTimeout: 5 * time.Minute}).
		Complete(r)
}
```
`go build ./catalogarr/...` (do not modify `run.go` — it will not compile-reference this package until C12; that's fine, `go build ./catalogarr/controller/...` is what you verify). Commit.

---

## Series: the pure functions

- [ ] **Step 10: `EffectiveEpisodeOrder` — failing test, then implementation**

```go
func TestEffectiveEpisodeOrder(t *testing.T) {
	cases := []struct{ name string; seriesType catalogv1alpha1.SeriesType; order catalogv1alpha1.EpisodeOrder; want catalogv1alpha1.EpisodeOrder }{
		{"anime forces absolute regardless of spec", catalogv1alpha1.SeriesTypeAnime, catalogv1alpha1.EpisodeOrderOfficial, catalogv1alpha1.EpisodeOrderAbsolute},
		{"anime forces absolute even if user set dvd", catalogv1alpha1.SeriesTypeAnime, catalogv1alpha1.EpisodeOrderDVD, catalogv1alpha1.EpisodeOrderAbsolute},
		{"standard keeps the spec value", catalogv1alpha1.SeriesTypeStandard, catalogv1alpha1.EpisodeOrderDVD, catalogv1alpha1.EpisodeOrderDVD},
		{"daily keeps official default", catalogv1alpha1.SeriesTypeDaily, catalogv1alpha1.EpisodeOrderOfficial, catalogv1alpha1.EpisodeOrderOfficial},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, series.EffectiveEpisodeOrder(c.seriesType, c.order))
		})
	}
}
```
Note: `SeriesSpec.EpisodeOrder` has `+kubebuilder:default=official`, so it is never the Go zero value in a real object; the function still handles `""` defensively (falls back to `official`) since this is a pure function tested in isolation. Implement, run green, commit.

- [ ] **Step 11: `EpisodeName` — failing test, then implementation**

```go
func TestEpisodeName(t *testing.T) {
	airDate := time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC)
	cases := []struct{ name string; seriesName string; seriesType catalogv1alpha1.SeriesType; season, episode int32; airDate *time.Time; want string }{
		{"standard", "the-expanse", catalogv1alpha1.SeriesTypeStandard, 2, 5, nil, "the-expanse-s02e05"},
		{"anime uses season/episode too, not absolute", "one-piece", catalogv1alpha1.SeriesTypeAnime, 1, 1092, nil, "one-piece-s01e1092"},
		{"specials season is s00", "the-expanse", catalogv1alpha1.SeriesTypeStandard, 0, 1, nil, "the-expanse-s00e01"},
		{"daily uses the air date", "the-daily-show", catalogv1alpha1.SeriesTypeDaily, 0, 0, &airDate, "the-daily-show-2026-03-04"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, series.EpisodeName(c.seriesName, c.seriesType, c.season, c.episode, c.airDate))
		})
	}
}
```
(Episode numbers can exceed 99 for long-running anime — `%02d` on a 4-digit number just prints all 4 digits in Go, no truncation, so `s01e1092` is correct, not a bug; assert it explicitly as above so a future "helpful" fix to zero-pad-to-exactly-2 doesn't regress it silently.) Implement per spec §4.2's literal naming (`<series>-s<NN>e<NN>`, daily `<series>-<yyyy-mm-dd>`), commit.

- [ ] **Step 12: `InitialEpisodeMonitored` — failing test (representative subset), then implementation**

Full semantics (`docs/research/quality.md`, the `Add-time monitoring` paragraph, `EpisodeMonitoredService`):
`All` all; `Future` unaired-only (and only when series is `continuing`/`upcoming`); `Missing` aired (no file exists yet at add time, so this collapses to "aired"); `Existing` always false at add time (no file exists yet); `FirstSeason`/`LastSeason` one season (numerically lowest/highest season number present); `Pilot` S01E01 only; `Recent` aired within the last 90 days; `None` always false; `Skip` always false (nothing to inherit from — see rationale in the doc comment).

`MonitorSpecials`/`UnmonitorSpecials` — **ruling from review**, settling what was flagged as a low-confidence reading: the research note's "toggle season 0 only" is the semantics, literally. These two modes set the monitored flag *only* for season-0 episodes and leave every other season's decision untouched by this branch. Since `InitialEpisodeMonitored` returns a plain `bool` (not the `*bool` `DesiredEpisode.Monitored` wraps it in) and a brand-new `Episode` has no prior value to leave "untouched" *toward*, "untouched" resolves to the same value the episode would get with no special-casing at all — the schema default `+kubebuilder:default=true` on `EpisodeSpec.Monitored`, i.e. `true` — which happens to be observationally identical to what `All` would produce for that episode. Concretely: `MonitorSpecials` returns `true` for a season-0 episode and `true` for every other season (unchanged from the default); `UnmonitorSpecials` returns `false` for a season-0 episode and `true` for every other season (unchanged from the default). The test below proves the "unchanged" half explicitly for both modes, not just the season-0 half.

```go
func TestInitialEpisodeMonitored(t *testing.T) {
	now := time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)
	aired := now.AddDate(0, 0, -10)
	old := now.AddDate(0, 0, -200)
	unaired := now.AddDate(0, 0, 10)
	all := []series.EpisodeCandidate{
		{SeasonNumber: 0, EpisodeNumber: 1, AirDate: &old},
		{SeasonNumber: 1, EpisodeNumber: 1, AirDate: &old},
		{SeasonNumber: 1, EpisodeNumber: 2, AirDate: &aired},
		{SeasonNumber: 2, EpisodeNumber: 1, AirDate: &unaired},
	}
	cases := []struct {
		name string
		mode catalogv1alpha1.SeriesMonitorMode
		ep   series.EpisodeCandidate
		run  catalogv1alpha1.SeriesRunStatus
		want bool
	}{
		{"all monitors everything incl specials", catalogv1alpha1.SeriesMonitorAll, all[0], catalogv1alpha1.SeriesRunStatusContinuing, true},
		{"future only unaired, continuing series", catalogv1alpha1.SeriesMonitorFuture, all[3], catalogv1alpha1.SeriesRunStatusContinuing, true},
		{"future skips aired", catalogv1alpha1.SeriesMonitorFuture, all[1], catalogv1alpha1.SeriesRunStatusContinuing, false},
		{"future is false for an ended series even if unaired", catalogv1alpha1.SeriesMonitorFuture, all[3], catalogv1alpha1.SeriesRunStatusEnded, false},
		{"missing monitors aired episodes", catalogv1alpha1.SeriesMonitorMissing, all[1], catalogv1alpha1.SeriesRunStatusContinuing, true},
		{"missing skips unaired", catalogv1alpha1.SeriesMonitorMissing, all[3], catalogv1alpha1.SeriesRunStatusContinuing, false},
		{"existing is always false at add time", catalogv1alpha1.SeriesMonitorExisting, all[1], catalogv1alpha1.SeriesRunStatusContinuing, false},
		{"firstSeason matches the lowest positive season", catalogv1alpha1.SeriesMonitorFirstSeason, all[1], catalogv1alpha1.SeriesRunStatusContinuing, true},
		{"firstSeason excludes specials", catalogv1alpha1.SeriesMonitorFirstSeason, all[0], catalogv1alpha1.SeriesRunStatusContinuing, false},
		{"firstSeason excludes later seasons", catalogv1alpha1.SeriesMonitorFirstSeason, all[3], catalogv1alpha1.SeriesRunStatusContinuing, false},
		{"lastSeason matches the highest season", catalogv1alpha1.SeriesMonitorLastSeason, all[3], catalogv1alpha1.SeriesRunStatusContinuing, true},
		{"pilot is s01e01 only", catalogv1alpha1.SeriesMonitorPilot, all[1], catalogv1alpha1.SeriesRunStatusContinuing, true},
		{"pilot excludes s01e02", catalogv1alpha1.SeriesMonitorPilot, all[2], catalogv1alpha1.SeriesRunStatusContinuing, false},
		{"recent within 90 days", catalogv1alpha1.SeriesMonitorRecent, all[1], catalogv1alpha1.SeriesRunStatusContinuing, true},
		{"recent excludes a 200-day-old episode", catalogv1alpha1.SeriesMonitorRecent, all[0], catalogv1alpha1.SeriesRunStatusContinuing, false},
		{"monitorSpecials sets season 0 true", catalogv1alpha1.SeriesMonitorMonitorSpecials, all[0], catalogv1alpha1.SeriesRunStatusContinuing, true},
		{"monitorSpecials leaves season 1 unmodified (same as the default/All)", catalogv1alpha1.SeriesMonitorMonitorSpecials, all[1], catalogv1alpha1.SeriesRunStatusContinuing, true},
		{"unmonitorSpecials sets season 0 false", catalogv1alpha1.SeriesMonitorUnmonitorSpecials, all[0], catalogv1alpha1.SeriesRunStatusContinuing, false},
		{"unmonitorSpecials leaves season 1 unmodified (same as the default/All)", catalogv1alpha1.SeriesMonitorUnmonitorSpecials, all[1], catalogv1alpha1.SeriesRunStatusContinuing, true},
		{"none is always false", catalogv1alpha1.SeriesMonitorNone, all[1], catalogv1alpha1.SeriesRunStatusContinuing, false},
		{"skip is always false at add time", catalogv1alpha1.SeriesMonitorSkip, all[1], catalogv1alpha1.SeriesRunStatusContinuing, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, series.InitialEpisodeMonitored(c.mode, c.ep, all, c.run, now))
		})
	}
}
```
Implement, run green, commit.

- [ ] **Step 13: `DesiredEpisodes` — failing test including anime absolute ordering, then implementation**

```go
func TestDesiredEpisodesAnimeAbsoluteOrdering(t *testing.T) {
	now := time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)
	s := &catalogv1alpha1.Series{
		ObjectMeta: metav1.ObjectMeta{Name: "one-piece"},
		Spec: catalogv1alpha1.SeriesSpec{
			SeriesType: catalogv1alpha1.SeriesTypeAnime,
			AddOptions: catalogv1alpha1.SeriesAddOptions{Monitor: catalogv1alpha1.SeriesMonitorAll},
		},
	}
	abs1, abs2 := int32(1091), int32(1092)
	provided := []metadata.Episode{
		{SeasonNumber: 1, EpisodeNumber: 1091, AbsoluteNumber: &abs1, Title: "Episode 1091"},
		{SeasonNumber: 1, EpisodeNumber: 1092, AbsoluteNumber: &abs2, Title: "Episode 1092"},
	}
	got := series.DesiredEpisodes(s, false, provided, now)
	require.Len(t, got, 2)
	assert.Equal(t, "one-piece-s01e1091", got[0].Name)
	require.NotNil(t, got[0].AbsoluteNumber)
	assert.EqualValues(t, 1091, *got[0].AbsoluteNumber)
	assert.True(t, got[0].Monitored != nil && *got[0].Monitored) // AddOptions.Monitor=All, first fan-out
	assert.Equal(t, "one-piece-s01e1092", got[1].Name)
}

func TestDesiredEpisodesAfterAddOptionsAppliedUsesMonitorNewItems(t *testing.T) {
	now := time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)
	s := &catalogv1alpha1.Series{
		ObjectMeta: metav1.ObjectMeta{Name: "the-expanse"},
		Spec: catalogv1alpha1.SeriesSpec{
			SeriesType:      catalogv1alpha1.SeriesTypeStandard,
			MonitorNewItems: catalogv1alpha1.MonitorNewChildrenNone,
		},
	}
	provided := []metadata.Episode{{SeasonNumber: 7, EpisodeNumber: 1, Title: "New Season"}}
	got := series.DesiredEpisodes(s, true, provided, now) // addOptionsApplied already true: this is a later refresh
	require.Len(t, got, 1)
	assert.True(t, got[0].Monitored != nil && !*got[0].Monitored) // MonitorNewItems=none
}
```
`DesiredEpisode.Monitored` is `*bool`: non-nil on the first fan-out (`addOptionsApplied == false`, decided by `InitialEpisodeMonitored`) and on brand-new episodes appearing after add (decided by `spec.MonitorNewItems == all`); **nil** for episodes that already exist as `Episode` objects on a later refresh — the reconciler (Step 16) must read `nil` as "do not touch `spec.monitored`", per `EpisodeSpec.Monitored`'s own doc comment ("the Series controller sets it only at creation and per `monitorNewItems`; after that it belongs to the user"). Add a third test case proving this: call `DesiredEpisodes` with `addOptionsApplied: true` and an episode whose name is passed in a `existingNames` set — no such parameter exists yet in the signature above; correct it to `DesiredEpisodes(series *catalogv1alpha1.Series, addOptionsApplied bool, existingNames map[string]bool, episodes []metadata.Episode, now time.Time) []DesiredEpisode` and thread `existingNames` from the reconciler's `List` call (Step 16) so this distinction is actually decidable — the two tests above pass either way since neither has a pre-existing episode, so this signature correction must land before Step 16, not after.

Implement (dedupe by `(SeasonNumber, EpisodeNumber)`, first occurrence wins — the provider is assumed not to send duplicates, but defend anyway; use `EffectiveEpisodeOrder` upstream of this function, not inside it — the fetched `[]metadata.Episode` already reflects whatever order was requested, this function does not need to know which). Run green. Commit.

- [ ] **Step 14: `Rollup` — failing test, then implementation**

```go
func TestRollup(t *testing.T) {
	eps := []catalogv1alpha1.Episode{
		{Spec: catalogv1alpha1.EpisodeSpec{SeasonNumber: 1, EpisodeNumber: 1}, Status: catalogv1alpha1.EpisodeStatus{HasFile: true}},
		{Spec: catalogv1alpha1.EpisodeSpec{SeasonNumber: 1, EpisodeNumber: 2}, Status: catalogv1alpha1.EpisodeStatus{HasFile: false}},
		{Spec: catalogv1alpha1.EpisodeSpec{SeasonNumber: 2, EpisodeNumber: 1}, Status: catalogv1alpha1.EpisodeStatus{HasFile: true}},
	}
	seasons, count, fileCount := series.Rollup(eps)
	require.Len(t, seasons, 2)
	assert.EqualValues(t, 3, count)
	assert.EqualValues(t, 2, fileCount)
	// seasons sorted ascending by number, per +listMapKey=number's implied order
	assert.EqualValues(t, 1, seasons[0].Number)
	assert.EqualValues(t, 2, seasons[0].EpisodeCount)
	assert.EqualValues(t, 1, seasons[0].EpisodeFileCount)
	assert.EqualValues(t, 2, seasons[1].Number)
}
```
Note this rollup is honest-but-currently-inert: nothing in this task's own reconcilers ever sets `Episode.Status.HasFile = true` (that field is populated once a future task wires a MediaFile watch into the episode controller — see the scope-boundary note above), so in production `fileCount` will read `0` until that lands. That is correct, not a bug; the test above exercises the arithmetic with synthetic data regardless. Implement, run green, commit.

- [ ] **Step 15: `Phase` (Series) — failing test, then implementation**

Same shape as Movie's, three inputs, mirrors §4.2's `SeriesPhase` enum and `EpisodesSynced` condition:
```go
func TestPhase(t *testing.T) {
	cases := []struct{ name string; monitored, metaReady, episodesSynced bool; want catalogv1alpha1.SeriesPhase }{
		{"unmonitored wins", false, true, true, catalogv1alpha1.SeriesPhaseUnmonitored},
		{"pending until metadata ready", true, false, false, catalogv1alpha1.SeriesPhasePending},
		{"pending until episodes synced", true, true, false, catalogv1alpha1.SeriesPhasePending},
		{"ready once both are true", true, true, true, catalogv1alpha1.SeriesPhaseReady},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { assert.Equal(t, c.want, series.Phase(c.monitored, c.metaReady, c.episodesSynced)) }
	}
}
```
Implement, run green, commit.

## Series: the reconciler

- [ ] **Step 16: finalizer, addOptions-once, metadata publish, path/phase — mirrors Movie Steps 4–9**

Follow the same structure as the Movie reconciler (finalizer with no early return; `addOptionsApplied` recorded once; stale-metadata check using `metadata.RefreshTTL(commonv1.MediaKindSeries, ...)` with states derived from `SeriesMetadata.Status` — `continuing` → `RefreshStateContinuing`, `ended` and recently ended (within `series.EndedRecentWindow`, a named exported constant with the same "chosen default, not ported" doc comment as `movie.ReleasedRecentWindow` in Step 6 — declare it here, do not import `movie`'s) → `RefreshStateEndedRecent` else `RefreshStateEndedOld`, `upcoming` → treat as `RefreshStateContinuing` for TTL purposes since `pkg/metadata/refresh.go` has no `upcoming` bucket — flag this too; `Path` via `naming.Context{Kind: commonv1.MediaKindSeries, SeriesTitle: s.Status.Metadata.Title, SeriesYear: int(s.Status.Metadata.Year), TvdbID: strconv.FormatInt(s.Spec.TvdbID, 10)}` and `engine.SeriesFolder(ctx)` — **not** `ctx.Title`, confirmed by the `pkg/naming/render.go` grep in "Read first"). Publish target is `events.WorkMetadataSubject(events.PriorityNormal, ns+"/"+name)` with `MediaRef.Kind: commonv1.MediaKindSeries`. QueueFull handling identical to Movie Step 6. `SeriesReconciler.Bus` is `events.Bus` (not just `Publisher`), since Step 17 also needs `Request`.

Write the same three envtest cases as Movie Steps 4, 5, 6, 8 (finalizer-no-early-return, addOptions-once, metadata-refresh-does-not-reloop — the predicate is the same `Or(GenerationChanged, StatusFieldChanged(extractMetadataRefreshedAt))` shape, just against `Series`). Commit.

- [ ] **Step 17: episode-list RPC fetch and Episode fan-out — the envtest the brief names explicitly**

Test (envtest, real manager as in Movie Step 4, but `Bus` here is a fake implementing only `Request` — do not pull in a real `membus` RPC round-trip for this, it adds a queue-group `Serve` side you do not need to test):
```go
type fakeEpisodeRPC struct{ episodes []metadata.Episode; err error }

func (f fakeEpisodeRPC) Request(ctx context.Context, subject string, in, out any) error {
	if f.err != nil {
		return f.err
	}
	resp, ok := out.(*schema.MetadataResponse)
	require.True(t, ok)
	resp.Kind = commonv1.MediaKindEpisode
	for _, ep := range f.episodes {
		b, err := json.Marshal(ep)
		require.NoError(t, err)
		resp.Results = append(resp.Results, b)
	}
	return nil
}
// embed a no-op Publish alongside it to satisfy events.Bus's Publisher half, or
// narrow SeriesReconciler.Bus to a small local interface
// { events.Publisher; events.Requester } instead of the full events.Bus --
// prefer the narrower interface, it is less to fake and is exactly what this
// reconciler uses (it never calls Subscribe/KV/Ensure/Close).
```
Seed a `Series` with `spec.seriesType: anime`, `status.metadata` already fresh (so the reconcile reaches episode sync), `status.addOptionsApplied: false`, `spec.addOptions.monitor: all`. Fake RPC returns two anime episodes as in Step 13's test data. `require.Eventually`: two `Episode` objects exist, named `<series>-s01e1091` / `...e1092`, each `ownerReferences` contains a controller ref to the `Series` (`k8s.ControllerRef` / `k8s.SetControllerReference`), `spec.monitored == true`, `status.absoluteNumber` set, `status.title` matches. Also assert `status.addOptionsApplied == true` and `conditions[EpisodesSynced].status == True` on the `Series` afterward.

Second case: reconcile again with the *same* fake (still returning the same 2 episodes) — assert episode count is still 2, not 4 (idempotent create-if-absent), and that `spec.monitored` on the existing episodes was NOT rewritten (change it to `false` on one out-of-band between the two reconciles via `k8s.PatchStatus`... no, `spec` is not a status field — use a plain `r.Update` in the test to flip `spec.monitored` on one Episode between reconciles, then assert the second reconcile left it `false`, proving the reconciler skips `spec.monitored` for already-existing episodes per Step 13's `nil`-means-do-not-touch contract).

Third case (RPC error): fake returns `err: errors.New("timeout")`. Assert the reconcile does not fail catastrophically (`ctrl.Result{RequeueAfter: ...}` with a short backoff, e.g. 30s — pick a concrete value and assert it exactly, do not leave it "some backoff"), `conditions[EpisodesSynced]` stays `False`/`Unknown` with a reason surfacing the RPC failure, and — critically — that this failure does NOT block the Series' own `Phase`/`Path`/`MetadataReady` from having been set correctly in the same reconcile (episode sync is the last step, per the ordering this task inherited from §8.1's sentence structure).

Implement: build the request as specified in "ASSUMED CONTRACT" above using `series.EffectiveEpisodeOrder(s.Spec.SeriesType, s.Spec.EpisodeOrder)` for `IDs["order"]`; `bus.Request(ctx, events.RPCMetadataLookup, req, &resp)` with a bounded `context.WithTimeout(ctx, 30*time.Second)`; unmarshal `resp.Results` into `[]metadata.Episode`; `r.List(ctx, &episodeList, client.InNamespace(s.Namespace), client.MatchingFields{ownerIndexKey: s.Name})` — add a field index in `SetupWithManager` (`mgr.GetFieldIndexer().IndexField(ctx, &catalogv1alpha1.Episode{}, ownerIndexKey, func(o client.Object) []string { ep := o.(*catalogv1alpha1.Episode); return []string{ep.Spec.SeriesRef} })`, per `docs/research/k8s.md` §5 rule 8) rather than listing every Episode in the namespace and filtering client-side; feed the existing names into `series.DesiredEpisodes`; for each desired row, `Get`-or-`Create` (set `Spec.SeriesRef/SeasonNumber/EpisodeNumber`, `Spec.Monitored` only when the row's `Monitored *bool != nil`, `k8s.SetControllerReference(s, &ep, r.Scheme)`); after create-or-confirm, `k8s.PatchStatus` each Episode's provider-sourced status fields (`TvdbID`, `Title`, `Overview`, `AirDate`, `RuntimeMinutes`, `AbsoluteNumber`) under `k8s.ManagerCatalogarr` — this is legal even though Episode has its *own* controller/package, because it is still the same conceptual "catalogarr" writer and the same field manager string; the Episode reconciler (Step 19) must never write these same fields itself, only `Phase`/`conditions[Aired]`, so the two stay on disjoint fields the same way grabarr/grabarr-engine do on `Download`. State this split explicitly in the Episode reconciler's own doc comment so it is not rediscovered by accident. Then call `series.Rollup` over the fetched `Episode` list and `PatchStatus` the `Series`'s `Seasons`/`EpisodeCount`/`EpisodeFileCount`. Commit.

---

## Episode: pure function and reconciler

- [ ] **Step 18: `Phase` (Episode) — failing test, then implementation**

Written directly to the 5-argument form Steps 24-25 need, same reasoning as Movie's Step 2.

```go
func TestPhase(t *testing.T) {
	now := time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)
	past := metav1.NewTime(now.AddDate(0, 0, -1))
	future := metav1.NewTime(now.AddDate(0, 0, 1))
	cases := []struct {
		name               string
		monitored          bool
		airDate            *metav1.Time
		hasFile, cutoffMet bool
		want               catalogv1alpha1.EpisodePhase
	}{
		{"unmonitored wins", false, &past, false, false, catalogv1alpha1.EpisodePhaseUnmonitored},
		{"no air date yet is unaired", true, nil, false, false, catalogv1alpha1.EpisodePhaseUnaired},
		{"future air date is unaired", true, &future, false, false, catalogv1alpha1.EpisodePhaseUnaired},
		{"past air date is wanted", true, &past, false, false, catalogv1alpha1.EpisodePhaseWanted},
		{"a file already imported outranks air date entirely", true, &future, true, true, catalogv1alpha1.EpisodePhaseImported},
		{"an imported file below cutoff", true, &past, true, false, catalogv1alpha1.EpisodePhaseCutoffUnmet},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, episode.Phase(c.monitored, c.airDate, c.hasFile, c.cutoffMet, now))
		})
	}
}
```
Implement in this order: `!monitored` → `Unmonitored`; else `hasFile && !cutoffMet` → `CutoffUnmet`; else `hasFile && cutoffMet` → `Imported`; else `airDate == nil || now.Before(airDate.Time)` → `Unaired`; else `Wanted` (a file already imported for a future-dated episode is a real, if unusual, case — a scene leak or a re-air — and outranks the air-date gate entirely, unlike Movie's `Pending` gate, because Episode has no metadata-readiness precondition of its own to race against). Run green, commit.

- [ ] **Step 19: Episode reconciler — finalizer, Phase, Aired condition**

envtest: create an `Episode` directly (owned by a hand-created `Series`, or standalone in the test — this reconciler does not require the owner to exist to compute its own phase, only `spec.monitored`/`status.airDate`, both already on the object). Assert finalizer present with no early return (same pattern as Movie Step 4). Reconcile with `status.airDate` unset: `phase == Unaired`, `conditions[Aired].status == False`. `k8s.PatchStatus` (test, simulating Step 17's write) `status.airDate` to yesterday; reconcile again: `phase == Wanted`, `conditions[Aired].status == True`. Assert the reconciler's own `PatchStatus` call only ever sets `Phase`/`Conditions`/`ObservedGeneration` — never touches `Title`/`Overview`/`AirDate`/`TvdbID`/`AbsoluteNumber`/`RuntimeMinutes` (those are Step 17's, under the same field manager but a disjoint field set) — assert this by patching those fields via a raw `k8s.PatchStatus` call from the test itself, reconciling, and confirming they survive unchanged.

Implement `reconciler.go` mirroring Movie's finalizer skeleton (no `Bus` field — this reconciler does no publishing); RBAC markers:
```go
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=episodes,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=episodes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=episodes/finalizers,verbs=update
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch
```
`For(&catalogv1alpha1.Episode{}, builder.WithPredicates(k8s.GenerationChanged()))` is sufficient here — this controller reacts to `spec.monitored` edits (a generation bump) and to its own periodic re-check of `airDate` against `now`, which needs a time-based `RequeueAfter(airDate.Sub(now))` when `Unaired`, not a watch; add that `RequeueAfter` in `Reconcile` explicitly (§8.8: `RequeueAfter` only, never `sleep`). It does *not* need `StatusFieldChanged` on its own `status.airDate` (Step 17 writes that under the same reconciler-family umbrella but via `Series`'s reconcile loop, which calls `PatchStatus` on the `Episode` object directly — that write **does** need to wake this controller, since otherwise a newly-discovered air date only gets picked up on the next `RequeueAfter` poll, not immediately. Add `k8s.Or(k8s.GenerationChanged(), k8s.StatusFieldChanged(extractAirDate))` here too, matching the exact self-loop-avoidance shape from Movie Step 8 — Series's write touches `AirDate`/`Title`/etc, never `Phase`/`Conditions`, so it passes the predicate without the reverse write looping back.) Call `episode.Phase(monitored, airDate, false, false, now)` for now — Step 25 replaces the two literals, same deal as Movie Step 7. Commit.

---

## Added in review: the MediaFile and Download watches (closes most of gap 6)

Ruling: add these now, in this task, rather than leaving them for an unassigned future one — see "Scope boundary" above for why single-writer forces it here. Movie and Episode each get both watches; Series gets neither directly (it has no `HasFile`/`ActiveDownloadRef` fields) and instead gets `Owns(&Episode{})` so its existing `Rollup` (Step 14) stays live.

- [ ] **Step 20: `FileState` — failing test, then implementation (write it once, land it twice: `catalogarr/controller/movie/filestate.go` and `catalogarr/controller/episode/filestate.go`)**

Identical logic in both packages — this is the one deliberate duplication in this task. `movie` and `episode` are disjoint path-owned packages with no third, shared directory in this task's ownership to put a common helper in; ~20 lines duplicated once is cheaper than inventing a new shared package outside the three this task owns. Flag it as a candidate for a future `pkg/` extraction if a third consumer ever needs it.

```go
// FileState derives a catalog item's file-related status fields from the
// MediaFile currently backing it. mf is nil when none does (a fresh item, or
// one whose file was just removed) -- every return value is then the zero
// value. profile is nil when the owning QualityProfile could not be
// resolved; cutoffMet is conservatively false in that case rather than
// panicking or guessing.
func FileState(mf *catalogv1alpha1.MediaFile, profile *quality.Profile) (hasFile bool, fileRef *string, fileQuality *commonv1.Quality, fileFormatScore int32, cutoffMet bool) {
	if mf == nil {
		return false, nil, nil, 0, false
	}
	name := mf.Name
	cutoffMet = profile != nil && profile.CutoffMet(mf.Spec.Quality)
	return true, &name, &mf.Spec.Quality, mf.Spec.FormatScore, cutoffMet
}
```

Test (`filestate_test.go`, same in both packages, substitute the package name):
```go
func TestFileState(t *testing.T) {
	q := commonv1.Quality{Name: "Bluray-1080p", Resolution: 1080}
	mf := &catalogv1alpha1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Name: "inception-abc1234567"},
		Spec:       catalogv1alpha1.MediaFileSpec{Quality: q, FormatScore: 40},
	}

	t.Run("no file", func(t *testing.T) {
		hasFile, ref, fq, score, cutoffMet := movie.FileState(nil, nil)
		assert.False(t, hasFile)
		assert.Nil(t, ref)
		assert.Nil(t, fq)
		assert.Zero(t, score)
		assert.False(t, cutoffMet)
	})

	t.Run("file present, no profile resolved yet", func(t *testing.T) {
		hasFile, ref, fq, score, cutoffMet := movie.FileState(mf, nil)
		assert.True(t, hasFile)
		require.NotNil(t, ref)
		assert.Equal(t, "inception-abc1234567", *ref)
		require.NotNil(t, fq)
		assert.Equal(t, q, *fq)
		assert.EqualValues(t, 40, score)
		assert.False(t, cutoffMet, "no profile means conservatively not met, never a guessed true")
	})

	t.Run("file present, profile resolved, meets cutoff", func(t *testing.T) {
		p := quality.Profile{Tiers: [][]quality.Definition{{{Quality: q}}}, CutoffIndex: 0}
		_, _, _, _, cutoffMet := movie.FileState(mf, &p)
		assert.True(t, cutoffMet)
	})
}
```
(Build the `quality.Profile` fixture by hand rather than through `quality.FromCRD` here — this test is about `FileState`'s wiring, not `Profile.CutoffMet`'s own correctness, which is Phase B's already-tested territory.) Run red, implement, run green, commit each package path-scoped separately (`-- catalogarr/controller/movie/filestate*.go` and `-- catalogarr/controller/episode/filestate*.go`).

- [ ] **Step 21: `DownloadOverlay` — failing test, then implementation (same duplication rule as Step 20)**

```go
// DownloadOverlay reads dl's phase and decides whether the item's Phase
// should be overridden and whether ActiveDownloadRef should stay set. dl is
// nil once the ref has been cleared or was never set; phase == "" means
// "no opinion, let the ordinary availability/file computation decide".
//
// The mapping is a judgment call, flagged in review: Movie/Episode's phase
// enum was designed around the *arr search/grab/import lifecycle, not
// Download's own phases, and the two do not line up one-to-one.
// DownloadPhasePending has no engine assigned yet, so the closest existing
// value is Delayed; Assigned/Queued/Downloading/Paused are all "actively
// working on it" and map to Downloading; every terminal phase
// (Completed/Seeding/Imported/Failed/Blocklisted/Removing) clears the ref
// and returns no opinion, deferring to the MediaFile watch (which produces
// Imported once the file actually lands) or the ordinary Wanted/Unavailable
// computation. Nothing in Phase C ever drives a Download past
// Pending/Assigned -- grabarr, the only writer past that point, is Phase D
// -- so only the first two rows are exercised by anything real before then;
// the rest are unit-tested against synthetic fixtures now and wired for
// when grabarr lands.
func DownloadOverlay(dl *downloadv1alpha1.Download) (phase catalogv1alpha1.MoviePhase, active bool) {
	if dl == nil {
		return "", false
	}
	switch dl.Status.Phase {
	case downloadv1alpha1.DownloadPhasePending:
		return catalogv1alpha1.MoviePhaseDelayed, true
	case downloadv1alpha1.DownloadPhaseAssigned, downloadv1alpha1.DownloadPhaseQueued,
		downloadv1alpha1.DownloadPhaseDownloading, downloadv1alpha1.DownloadPhasePaused:
		return catalogv1alpha1.MoviePhaseDownloading, true
	default: // Completed, Seeding, Imported, Failed, Blocklisted, Removing
		return "", false
	}
}
```
Test:
```go
func TestDownloadOverlay(t *testing.T) {
	dl := func(p downloadv1alpha1.DownloadPhase) *downloadv1alpha1.Download {
		return &downloadv1alpha1.Download{Status: downloadv1alpha1.DownloadStatus{Phase: p}}
	}
	cases := []struct {
		name       string
		dl         *downloadv1alpha1.Download
		wantPhase  catalogv1alpha1.MoviePhase
		wantActive bool
	}{
		{"nil download, nothing to say", nil, "", false},
		{"pending has no engine yet, closest fit is delayed", dl(downloadv1alpha1.DownloadPhasePending), catalogv1alpha1.MoviePhaseDelayed, true},
		{"assigned is actively working", dl(downloadv1alpha1.DownloadPhaseAssigned), catalogv1alpha1.MoviePhaseDownloading, true},
		{"downloading", dl(downloadv1alpha1.DownloadPhaseDownloading), catalogv1alpha1.MoviePhaseDownloading, true},
		{"paused still counts as downloading", dl(downloadv1alpha1.DownloadPhasePaused), catalogv1alpha1.MoviePhaseDownloading, true},
		{"completed clears the ref and defers", dl(downloadv1alpha1.DownloadPhaseCompleted), "", false},
		{"failed clears the ref and defers", dl(downloadv1alpha1.DownloadPhaseFailed), "", false},
		{"blocklisted clears the ref and defers", dl(downloadv1alpha1.DownloadPhaseBlocklisted), "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			phase, active := movie.DownloadOverlay(c.dl)
			assert.Equal(t, c.wantPhase, phase)
			assert.Equal(t, c.wantActive, active)
		})
	}
}
```
Run red, implement, run green, commit path-scoped as in Step 20. (Episode's copy returns `catalogv1alpha1.EpisodePhase` instead of `MoviePhase` — same switch, different return type, same test shape.)

- [ ] **Step 22: wire both watches into the Movie reconciler**

Register two field indices in `SetupWithManager` (pattern already used for Series→Episode in Step 17):
```go
const mediaFileByMovieIndexKey = ".spec.mediaRef.movie"
const movieByActiveDownloadIndexKey = ".status.activeDownloadRef"

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &catalogv1alpha1.MediaFile{}, mediaFileByMovieIndexKey,
		func(o client.Object) []string {
			mf := o.(*catalogv1alpha1.MediaFile)
			if mf.Spec.MediaRef.Kind != commonv1.MediaKindMovie {
				return nil
			}
			return []string{mf.Spec.MediaRef.Name}
		}); err != nil {
		return err
	}
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &catalogv1alpha1.Movie{}, movieByActiveDownloadIndexKey,
		func(o client.Object) []string {
			m := o.(*catalogv1alpha1.Movie)
			if m.Status.ActiveDownloadRef == nil {
				return nil
			}
			return []string{*m.Status.ActiveDownloadRef}
		}); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		Named("movie").
		For(&catalogv1alpha1.Movie{}, builder.WithPredicates(moviePredicate())).
		Watches(&catalogv1alpha1.MediaFile{}, handler.EnqueueRequestsFromMapFunc(r.mapMediaFile), builder.WithPredicates(k8s.GenerationChanged())).
		Watches(&downloadv1alpha1.Download{}, handler.EnqueueRequestsFromMapFunc(r.mapDownload), builder.WithPredicates(k8s.GenerationChanged())).
		WithOptions(controller.Options{RecoverPanic: ptr.To(true), ReconciliationTimeout: 5 * time.Minute}).
		Complete(r)
}

// mapMediaFile needs no List/index -- MediaFile already carries the target's
// identity directly in spec.mediaRef, so this is the cheap direction.
func (r *Reconciler) mapMediaFile(ctx context.Context, o client.Object) []reconcile.Request {
	mf := o.(*catalogv1alpha1.MediaFile)
	if mf.Spec.MediaRef.Kind != commonv1.MediaKindMovie {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: mf.Namespace, Name: mf.Spec.MediaRef.Name}}}
}

// mapDownload is the reverse direction -- Movie is the side holding the
// reference, so this needs the movieByActiveDownloadIndexKey index.
func (r *Reconciler) mapDownload(ctx context.Context, o client.Object) []reconcile.Request {
	dl := o.(*downloadv1alpha1.Download)
	var movies catalogv1alpha1.MovieList
	if err := r.List(ctx, &movies, client.InNamespace(dl.Namespace), client.MatchingFields{movieByActiveDownloadIndexKey: dl.Name}); err != nil {
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(movies.Items))
	for _, m := range movies.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: m.Namespace, Name: m.Name}})
	}
	return reqs
}
```
In `reconcileNormal`, after the availability/path block (Step 7): list `MediaFile`s via the same index (`client.MatchingFields{mediaFileByMovieIndexKey: m.Name}`), pick the one with `Spec.Original == true`, else the most recent by `CreationTimestamp`, else `nil`; resolve the `QualityProfile` named `m.Spec.QualityProfileRef` (`client.IgnoreNotFound` is fine here — an unresolved profile degrades `FileState` to `cutoffMet=false` rather than blocking the rest of the reconcile) via `quality.FromCRD(&qp, catalogue.LoadedCatalogue())`; call `movie.FileState(mf, profile)`; if `m.Status.ActiveDownloadRef != nil`, `Get` that `Download` and call `movie.DownloadOverlay(dl)`. Recompute `Phase` as: if the `DownloadOverlay` returned a non-empty phase, use it (and keep `ActiveDownloadRef` set); else use `movie.Phase(monitored, metaReady, available, hasFile, cutoffMet)` (and clear `ActiveDownloadRef` in the same `PatchStatus` call when `DownloadOverlay` returned `active == false` and a ref was set). Extend the `MovieStatusApplyConfiguration` built for `PatchStatus` (still `ManagerCatalogarr`, still never touching `Metadata`) with `.WithHasFile(...)`, `.WithFileRef(...)`, `.WithFileQuality(...)`, `.WithFileFormatScore(...)`, `.WithCutoffMet(...)`, and `.WithActiveDownloadRef(...)` only when it changed.

Add RBAC (additive to Step 9's block, same file):
```go
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=mediafiles,verbs=get;list;watch
// +kubebuilder:rbac:groups=download.clustarr.io,resources=downloads,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=qualityprofiles,verbs=get;list;watch
```
envtest: create a `Movie` already `Wanted` (as in Step 7); create a `MediaFile` with `spec.mediaRef = {kind: movie, name: <movie>}` and a `Quality`/`FormatScore`; `require.Eventually` `status.hasFile == true`, `status.fileRef` set, `status.phase` becomes `Imported` or `CutoffUnmet` depending on whether a seeded `QualityProfile` marks that quality as meeting cutoff (seed one that does, for the `Imported` case; assert the `CutoffUnmet` case in a second test with a profile whose cutoff is a better tier). Separately: set `status.activeDownloadRef` to a `Download` name via a raw `k8s.PatchStatus` call from the test, create that `Download` with `status.phase: Assigned`; `require.Eventually` `status.phase == Downloading`; flip the `Download`'s phase to `Completed` via `k8s.PatchStatus` under `k8s.ManagerGrabarr`; `require.Eventually` `status.activeDownloadRef` is cleared and `status.phase` falls back to whatever the availability computation gives (not `Downloading`). Commit.

- [ ] **Step 23: Series — `Owns(&catalogv1alpha1.Episode{})` so `Rollup` stays live**

Add to the Series `SetupWithManager` builder chain (after the existing `For(&catalogv1alpha1.Series{}, ...)`):
```go
Owns(&catalogv1alpha1.Episode{}, builder.WithPredicates(k8s.StatusFieldChanged(func(o client.Object) bool {
	ep, ok := o.(*catalogv1alpha1.Episode)
	return ok && ep.Status.HasFile
}))).
```
This is the same self-loop-avoidance technique as Step 8, applied to a child instead of self: Series's own writes to an owned `Episode` (Step 17 — `Title`/`Overview`/`AirDate`/`TvdbID`/`AbsoluteNumber`/`RuntimeMinutes`) never touch `HasFile`, so they do not re-trigger Series; only the Episode controller's own write of `HasFile` (Step 25, below) does. Without this predicate, `Owns()` would fire the Series reconcile on every `Episode` status write from *either* side, including its own, in a loop `StatusFieldChanged` scoped this narrowly avoids.

envtest: reuse Step 17's fan-out fixture; after two `Episode`s exist, `k8s.PatchStatus` one of them to `status.hasFile = true` under `k8s.ManagerCatalogarr` (simulating Step 25's write — this task must not literally run the Episode controller inside the Series test, just prove the watch reacts); `require.Eventually` the `Series`'s `status.episodeFileCount == 1` and the matching `status.seasons[].episodeFileCount` updated. Commit.

- [ ] **Step 24: Episode's own `FileState`/`DownloadOverlay`**

Same as Steps 20-21, landed in `catalogarr/controller/episode/`, with `DownloadOverlay` returning `catalogv1alpha1.EpisodePhase` (`EpisodePhaseDelayed`/`EpisodePhaseDownloading` instead of Movie's `MoviePhase` equivalents — same switch). Commit path-scoped.

- [ ] **Step 25: wire both watches into the Episode reconciler**

Same shape as Step 22: two field indices (`mediaFileByEpisodeIndexKey` filtering `MediaRef.Kind == commonv1.MediaKindEpisode`, `episodeByActiveDownloadIndexKey` on `ep.Status.ActiveDownloadRef`), the same two `Watches(...)` calls, the same `mapMediaFile`/`mapDownload` shape. The one real difference: Episode's `QualityProfile` is not on its own spec — `EpisodeSpec` has no `QualityProfileRef` (confirmed against the generated type; episodes are ranked against the *Series*'s profile, per `SeriesSpec.QualityProfileRef`'s own doc comment, "the QualityProfile episodes are ranked against"). So resolving the profile here means an extra hop: `Get` the owning `Series` by `ep.Spec.SeriesRef` first, then its `QualityProfileRef`, then the `QualityProfile` itself. Add RBAC for `series` (get only — this controller does not list or watch them) alongside `mediafiles`/`downloads`/`qualityprofiles`:
```go
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=series,verbs=get
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=mediafiles,verbs=get;list;watch
// +kubebuilder:rbac:groups=download.clustarr.io,resources=downloads,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=qualityprofiles,verbs=get;list;watch
```
Same `Phase`/`ActiveDownloadRef`/`HasFile`/`FileRef`/`FileQuality`/`FileFormatScore`/`CutoffMet` extension to the `PatchStatus` call as Step 22, using `episode.FileState`/`episode.DownloadOverlay`/the extended `episode.Phase` from Step 18. envtest: same two scenarios as Step 22 (`MediaFile` → `Imported`/`CutoffUnmet`, `Download` → `Downloading` → cleared on terminal), applied to a standalone `Episode`, plus one more: assert that this `Episode`'s `HasFile` flip is what makes Step 23's Series-level test pass when the two are run together (a single combined envtest is fine here, no need to duplicate the fixture). Commit.

---

## Registration lines for C12 (`catalogarr/run.go`) — you do not own this file, do not edit it

`setupControllers`'s current signature is `func setupControllers(mgr ctrl.Manager, o Options) error` and its call site in `Run` is `setupControllers(mgr, o)`. Both this task's Movie/Series controllers need the bus (`events.Publisher`/`events.Bus`), which today only `setupWorkers` receives. C12 must:

1. Change the signature to `func setupControllers(mgr ctrl.Manager, bus events.Bus, o Options) error` and its call site to `setupControllers(mgr, bus, o)` (`bus` is already constructed earlier in `Run`, right above the current `setupControllers` call — just move it above, or reorder so `bus` exists before both calls).
2. Inside `setupControllers`, add:
```go
if err := (&moviecontroller.Reconciler{
	Client: mgr.GetClient(), Scheme: mgr.GetScheme(),
	Recorder: mgr.GetEventRecorderFor("movie"), Bus: bus,
}).SetupWithManager(mgr); err != nil {
	return fmt.Errorf("catalogarr: movie controller: %w", err)
}
if err := (&seriescontroller.Reconciler{
	Client: mgr.GetClient(), Scheme: mgr.GetScheme(),
	Recorder: mgr.GetEventRecorderFor("series"), Bus: bus,
}).SetupWithManager(mgr); err != nil {
	return fmt.Errorf("catalogarr: series controller: %w", err)
}
if err := (&episodecontroller.Reconciler{
	Client: mgr.GetClient(), Scheme: mgr.GetScheme(),
	Recorder: mgr.GetEventRecorderFor("episode"),
}).SetupWithManager(mgr); err != nil {
	return fmt.Errorf("catalogarr: episode controller: %w", err)
}
```
with imports `moviecontroller "github.com/mediactl/clustarr/catalogarr/controller/movie"`, `seriescontroller ".../catalogarr/controller/series"`, `episodecontroller ".../catalogarr/controller/episode"`.
3. Update the `setupControllers` doc comment's `TODO(M1): movie, series, episode, ...` line to drop the three this task lands.

---

**Verification:**
```bash
go build ./catalogarr/... && go vet ./catalogarr/...
go test -count=1 -race ./catalogarr/controller/movie/... ./catalogarr/controller/series/... ./catalogarr/controller/episode/... -run 'TestAvailability|TestPhase|TestPath|TestEffectiveEpisodeOrder|TestEpisodeName|TestInitialEpisodeMonitored|TestDesiredEpisodes|TestRollup|TestFileState|TestDownloadOverlay'
export KUBEBUILDER_ASSETS=$(/home/appkins/go/bin/setup-envtest use 1.37.0 -p path)
go test -count=1 -race ./catalogarr/controller/movie/... ./catalogarr/controller/series/... ./catalogarr/controller/episode/...
make lint
```
Every envtest (`TestMovieReconciler...`, `TestSeriesReconciler...`, `TestEpisodeReconciler...` — name them so `go test -v` output makes the finalizer/addOptions/fanout/predicate/MediaFile-watch/Download-watch cases individually visible) must take real wall-clock time (multiple seconds, from spinning up `envtest.Environment` and a real manager) — a suite finishing in milliseconds means `KUBEBUILDER_ASSETS` was not picked up and it skipped silently; check the `go test -v` output for `--- SKIP`, not just exit code 0.

**Done when:**
- [ ] `Availability`, `Phase` (all three kinds, now 5-argument for Movie/Episode), `Path` (both kinds), `EffectiveEpisodeOrder`, `EpisodeName`, `InitialEpisodeMonitored`, `DesiredEpisodes`, `Rollup`, `FileState` (movie and episode), `DownloadOverlay` (movie and episode) are pure, table-tested, and pass without `KUBEBUILDER_ASSETS`.
- [ ] Movie, Series and Episode each have exactly one controller-writer (`ManagerCatalogarr`); none ever builds a `MovieStatusApplyConfiguration`/`SeriesStatusApplyConfiguration` that calls `.WithMetadata(...)`.
- [ ] Finalizer add is proven not to early-return (status progresses in the same reconcile the finalizer was added in).
- [ ] `addOptionsApplied` is proven idempotent (set once, second reconcile does not re-decide it).
- [ ] Series→Episode fan-out is proven, including anime absolute-numbering naming (`s01e1091` style) and idempotent re-fan-out that does not clobber a user's `spec.monitored` edit.
- [ ] A gateway-style `status.metadata` patch (`ManagerCatalogarrWorker`) demonstrably re-triggers this controller's own reconcile; this controller's own status patch (`ManagerCatalogarr`) demonstrably does not re-trigger itself. The same self-loop guard is proven for Series's `Owns(&Episode{})` watch (Step 23) and (implicitly, by using the same `GenerationChanged` predicate) for the Movie/Episode `MediaFile`/`Download` watches.
- [ ] `ErrQueueFull` from a publish sets `QueueFull=True` and requeues after exactly 1 minute.
- [ ] Movie and Episode both roll up `HasFile`/`FileRef`/`FileQuality`/`FileFormatScore`/`CutoffMet` from a watched `MediaFile` and reach `Phase=Imported`/`CutoffUnmet`; Series's `EpisodeFileCount`/`Seasons[].EpisodeFileCount` reflect an owned Episode's `HasFile` without a direct watch of its own. Movie and Episode both roll up `Phase=Downloading` and clear `ActiveDownloadRef` from a watched `Download`, proven against synthetic fixtures (no real grabarr exists yet to exercise this end to end — that is expected, not a gap in this task).
- [ ] `go build ./catalogarr/...`, `go vet ./catalogarr/...`, `make lint` clean; every envtest suite genuinely runs (multi-second wall time, no `--- SKIP`) under `KUBEBUILDER_ASSETS`.
- [ ] `movie.ReleasedRecentWindow` and `series.EndedRecentWindow` are named, exported, documented constants (not inline literals), each explicit that they are chosen defaults rather than values ported from Radarr/Sonarr.
- [ ] `MonitorSpecials`/`UnmonitorSpecials` set only the season-0 monitored flag and leave every other season at its default; the table test proves a season-1 episode is unaffected by either mode.
- [ ] Still-open items carried into the review rather than silently resolved: the episode-list RPC's assumed request/response shape (Task C5 must match it or this seam breaks); `PendingGrab`/`Phase=Delayed` driven by an actual `DelayProfile` decision (§8.2, still unowned by any task); the `DownloadOverlay` phase-mapping judgment call (Pending→Delayed, Assigned/Queued/Downloading/Paused→Downloading).

---

### Task C7: the MediaFile controller and the two-writer split

> **Controller amendment (binding).** Do NOT make the `api/common/v1alpha1.AudioStream.ChannelLayout` change in this task — Task C0 makes every `api/` change once, serially, so two parallel agents never race on regenerated files. By the time you run, the field exists; populate it where you write MediaFile spec and move on. Your path ownership is `catalogarr/controller/mediafile/` only.
>
> Your finding that the documented `status.file`/`status.probe` split does not exist is accepted and is now the plan's position: importarr owns `MediaFileSpec`, catalogarr is sole writer of all `MediaFileStatus` and takes over `sizeBytes`/`modTime`/`original` after a transcode swap. C0 corrects CLAUDE.md, the field-manager doc comments and the amendment. The two-writer envtest stays, testing that real split.


MediaFile is the only resource in Clustarr with two controller-writers. This task
builds catalogarr's half of that split: probing, label mirroring, the frozen
release-time fields' guard, the Movie/Episode status rollup, and the watches that
give squasharr and captionarr somewhere to land their results. It does **not**
build importarr's half (MediaFile creation) — that is a separate Phase C task on
`importarr/`. This task's envtest simulates importarr's writes by hand so the
split can be proven without depending on that task landing first.

**Files:**
- Create: `catalogarr/controller/mediafile/probe.go`
- Create: `catalogarr/controller/mediafile/probe_test.go`
- Create: `catalogarr/controller/mediafile/labels.go`
- Create: `catalogarr/controller/mediafile/labels_test.go`
- Create: `catalogarr/controller/mediafile/rollup.go`
- Create: `catalogarr/controller/mediafile/rollup_test.go`
- Create: `catalogarr/controller/mediafile/sidecars.go`
- Create: `catalogarr/controller/mediafile/sidecars_test.go`
- Create: `catalogarr/controller/mediafile/watch.go`
- Create: `catalogarr/controller/mediafile/watch_test.go`
- Create: `catalogarr/controller/mediafile/mediafile_controller.go`
- Create: `catalogarr/controller/mediafile/mediafile_envtest_test.go`
- Modify: `api/common/v1alpha1/media_types.go` (`AudioStream` gains `ChannelLayout`)
- Modify: `pkg/mediainfo/map.go` (`toAudioStream` populates it)
- Modify: `pkg/mediainfo/map_test.go` (asserts it)
- Regenerate: `config/crd/bases/catalog.clustarr.io_mediafiles.yaml`
- Regenerate: `config/crd/bases/transcode.clustarr.io_transcodejobs.yaml`

Not modified: `api/catalog/v1alpha1/mediafile_types.go` (the shape is already
generated and correct for this task — see "Resolving the field-manager split"
below), `catalogarr/run.go`. Every Phase C controller task under `catalogarr/`
wants to add one line to `setupControllers`; wiring `(&mediafile.Reconciler{...}).
SetupWithManager(mgr)` in there is deliberately left to whoever lands last or to
a separate integration task, not to this one, so this task's commits never touch
a file every other catalogarr controller task also touches.

**Path ownership:** `catalogarr/controller/mediafile/` entirely, plus the narrow,
named `api/common/v1alpha1/media_types.go` change and its regenerated output. The
media_types.go change makes this task **serial** against any other Phase C task
that touches `api/common/v1alpha1` (none are known to as of this writing, but
check before starting) — `make generate`/`make manifests` regenerate the whole
`api/...` tree, so a second uncommitted change there while this task is in
flight will show up as unrelated diff in this task's regeneration step. Do not
touch `pkg/mediainfo/*` beyond the one named line in `map.go` (its test package
is Phase B's and already reviewed) and do not touch `catalogarr/run.go`.

## Read first

- Spec §8.4's last two sentences (import tail, line 749): "`pkg/mediainfo.Probe`
  ... create `MediaFile` (quality/revision/formatScore/matchedFormats/releaseType
  frozen); MediaFile reconciler probes, sets labels, `probeHash`, `Probed`; item
  `HasFile`, `CutoffMet`, `Phase=Imported|CutoffUnmet` ...".
- Spec §8.5 (line 751, transcode ordering — why quality/formatScore never
  change) and §8.6 (line 753, subtitle sidecar feedback path) in full.
- Spec §10's cross-service contract table (line 779): catalogarr's `mediafile`
  controller watches transcode `TranscodeJob` and subtitle `SubtitleRequest`,
  predicates `status.phase==Succeeded` / `status.items changed`.
- Spec §4.2's `MediaFile` block (line 309 onward) and §4.1's common types —
  **superseded by the real generated types below; the spec's field list predates
  the amendment's ownership split and does not match what actually generated.**
- Amendment §A1.3 "Ownership" (lines 51–79), especially the `MediaFile` row and
  the two bullets on `status.file`/`status.probe` vs `status.quality`/
  `status.formatScore` — **also superseded; see below.**
- `docs/research/k8s.md` §4–5 (controller pattern, `Watches(...,
  EnqueueRequestsFromMapFunc(...))`, field index + `List(client.MatchingFields)`,
  line ~290–330) and the worked `SetupWithManager`/mapping-function/SSA-status
  example at lines 1068–1131. `pkg/k8s/patch_envtest_test.go` (already in the
  tree, Phase A) is the actual verified two-manager-no-clobber pattern this
  task's mandatory gate is modelled on — read
  `TestPatchStatusTwoManagersDoNotClobber` (line 148) and its `managesField`/
  `fieldManagerNames` helpers (line 283) before writing the envtest.
- `api/catalog/v1alpha1/mediafile_types.go` in full (already read for this
  section — every field name below is copied from it, not invented).
- `api/common/v1alpha1/media_types.go` (`AudioStream`, `MediaInfo`).
- `api/catalog/v1alpha1/shared_types.go` — the mirrored label key constants
  (`LabelKind`, `LabelResolution`, `LabelSource`, `LabelModifier`,
  `LabelVideoCodec`, `LabelHdr`, `LabelOriginal`) already exist; do not
  redeclare them.
- `pkg/mediainfo/map.go` and `pkg/mediainfo/map_test.go` (the `toAudioStream`
  function and its test's fixture style).
- `pkg/mediainfo/classify.go`'s `AudioChannelsString` doc comment — it already
  names the exact bug this task's carried item fixes.
- `go doc ./pkg/k8s` (`Apply`, `PatchStatus`, `FieldManager`,
  `ManagerCatalogarr`, `StatusFieldChanged`, `StatusFieldIn`, `MarkTrue`,
  `MarkFalse`, `NewCondition`, `ConditionACs`) — `PatchStatus` and `Apply` both
  hard-code `client.ForceOwnership`; there is no SSA 409 to catch a field-set
  mistake, which is exactly why this task's managedFields envtest is mandatory
  rather than a nice-to-have.
- `go doc ./pkg/mediainfo` (`Probe`, `ProbeHash`, `AudioChannelsString`) and
  `go doc ./pkg/quality` (`Profile`, `Profile.CutoffMet`, `FromCRD`) and
  `go doc ./pkg/quality/catalogue` (`LoadedCatalogue`).
- `go doc ./pkg/subtitles` — its own doc comment says sidecar-path naming and
  the missing-subtitle planner are captionarr's (Phase F), not this task's;
  this task only mirrors `SubtitleRequest.status.items` into
  `MediaFile.status.sidecars`, it does not scan the filesystem.

## Resolving the field-manager split against the real generated type

CLAUDE.md's invariant and amendment §A1.3 both describe the split as:
"`importarr` owns `status.file` (path, size, fingerprint) and `status.probe`
(the ffprobe result); `catalogarr` owns `status.quality`, `status.formatScore`
and the conditions." `pkg/k8s/fieldmanager.go`'s `ManagerImportarrWorker` doc
comment repeats the same shape ("applies `status.file` and `status.probe`
only"). **None of `status.file`, `status.probe`, `status.quality` or
`status.formatScore` exist on the real `MediaFileStatus`.** The real type
(`api/catalog/v1alpha1/mediafile_types.go`) is:

- `MediaFileSpec`: `mediaRef`, `path`, `sizeBytes`, `modTime`, `quality`,
  `revision`, `releaseType`, `releaseGroup`, `edition`, `languages`,
  `formatScore`, `matchedFormats`, `profileHash`, `importedFrom`, `original`.
  Quality/formatScore/revision/releaseType/matchedFormats/profileHash — the
  "decided" fields the amendment assigns to catalogarr — are **spec fields**,
  frozen at import per §8.4's own text ("create `MediaFile`
  (quality/revision/formatScore/matchedFormats/releaseType frozen)"), which is
  importarr's action, not catalogarr's.
- `MediaFileStatus`: `probeHash`, `probedAt`, `mediaInfo`, `sidecars`,
  `transcode`, `conditions`, `observedGeneration` — no split point inside it
  matches "file vs quality" at all; every one of these is exactly what this
  task's brief asks catalogarr to produce ("probes ... sets ... probeHash ...
  marks Probed").

Per shared-context's rule, the generated type wins. This task's controller is
the **sole writer of 100% of `MediaFileStatus`**, field manager
`k8s.ManagerCatalogarr`, via `k8s.PatchStatus` only — this satisfies the task's
own constraint ("status writes via `pkg/k8s.PatchStatus` with the catalogarr
manager only") exactly, because there is no importarr-owned status field to
collide with. `importarr` (a separate task, field manager
`k8s.ManagerImportarrWorker` per its own doc comment — the file-import
*worker*, not the controller, does the create) owns every `MediaFileSpec`
field **at creation**, via `k8s.Apply` on the main resource. The one place
catalogarr also touches `spec` is the three fields the type's own doc comments
already single out as catalogarr's: `Path`'s comment says "mutable only by
catalogarr (transcode replacement / rename)"; §8.5's text says the reconciler
"sets `spec.sizeBytes/modTime`, `Original=false`". So catalogarr's controller,
**only when it detects an unincorporated successful transcode** (Step 8 below),
issues a second `k8s.Apply` under `k8s.ManagerCatalogarr` naming exactly
`spec.sizeBytes`, `spec.modTime` and `spec.original` — never `path`, never any
of the frozen release-time fields. Both `k8s.Apply` and `k8s.PatchStatus`
force ownership unconditionally (see `pkg/k8s/patch.go`), so this is a clean
ownership *transfer* of those three fields from importarr to catalogarr after
the first transcode, not a conflict — and it is exactly what the mandatory
envtest in Step 9 proves happened correctly, field by field.

This makes the "freezing" §8.4/CLAUDE.md/the task brief all describe simpler
than it first looks: catalogarr's reconciler never has an opinion on
`spec.quality`/`spec.revision`/`spec.releaseType`/`spec.releaseGroup`/
`spec.edition`/`spec.languages`/`spec.formatScore`/`spec.matchedFormats`/
`spec.profileHash` — it simply never includes them in any `ApplyConfiguration`
it builds. There is nothing to actively "guard" beyond that omission; the
envtest asserts the omission holds.

**A second, narrower disagreement:** the amendment's ownership table lists
`ImportList`/`ImportExclusion`/`LibraryScan`/`Download.status.import` as
`importarr`'s and only `MediaFile` as split — consistent with everything
above, no action needed, noted only so the next reader does not think it was
missed.

**Scope decision (not specified by any source, settled here):** the "rolls its
state up to the owning item" rollup is implemented for `Movie` and `Episode`
only. `Album`/`Book`/`Audiobook`/`Issue` rollup is M6 scope ("non-video
inventory" per CLAUDE.md's Status section) and is out of this task; `Series`
itself has no `HasFile`/`CutoffMet` fields to roll up to (only its Episodes
do). `Artist`/`Author`/`Comic` never own a `MediaFile` directly either. If a
later task adds music/book/audiobook/comic `MediaFile` rollup, it is additive
to `rollup.go`, not a rewrite.

## Dependencies

No new dependency. Everything this task imports is already in `go.mod` from
Phases A/B: `sigs.k8s.io/controller-runtime v0.25.1`, `k8s.io/api v0.37.0`,
`k8s.io/apimachinery v0.37.0`, `k8s.io/client-go v0.37.0`,
`gopkg.in/vansante/go-ffprobe.v2 v2.3.1`, `github.com/stretchr/testify
v1.12.1`. Confirmed by reading `go.mod` directly (no `go list -m -versions`
needed — nothing new is being added).

## Interfaces — Consumes

```go
// pkg/mediainfo
func Probe(ctx context.Context, path string) (*commonv1.MediaInfo, *mediainfo.Raw, error)
func ProbeHash(path string, size int64, mtime time.Time) string

// pkg/quality
func FromCRD(p *catalogv1alpha1.QualityProfile, cat *catalogue.Catalogue) (quality.Profile, []error)
func (p quality.Profile) CutoffMet(q commonv1.Quality) bool

// pkg/quality/catalogue
func LoadedCatalogue() *catalogue.Catalogue

// pkg/subtitles
func ParseLangKey(k subtitles.LangKey) (lang string, forced, hi bool, err error)

// pkg/k8s
func Apply[T ApplyConfiguration](ctx, c client.Client, fm FieldManager, ac T, opts ...client.ApplyOption) (T, error)
func PatchStatus[T ApplyConfiguration](ctx, c client.Client, fm FieldManager, ac T, opts ...client.SubResourceApplyOption) (T, error)
const ManagerCatalogarr FieldManager = "catalogarr"
const ManagerImportarrWorker FieldManager = "importarr-worker" // simulated by hand in this task's envtest
func StatusFieldIn[T comparable](extract func(client.Object) T, want ...T) predicate.Predicate
func StatusFieldChanged[T comparable](extract func(client.Object) T) predicate.Predicate
func MarkTrue(obj client.Object, conditions *[]metav1.Condition, condType, reason, message string, args ...any) bool
func MarkFalse(obj client.Object, conditions *[]metav1.Condition, condType, reason, message string, args ...any) bool
func ConditionACs(conditions []metav1.Condition) []*metav1ac.ConditionApplyConfiguration

// api/applyconfiguration/catalog/catalog/v1alpha1 (generated, already exist)
func MediaFile(name, namespace string) *MediaFileApplyConfiguration
func MediaFileSpec() *MediaFileSpecApplyConfiguration    // WithSizeBytes, WithModTime, WithOriginal — nothing else used by this task
func MediaFileStatus() *MediaFileStatusApplyConfiguration // WithObservedGeneration, WithConditions, WithProbeHash, WithProbedAt, WithMediaInfo, WithSidecars, WithTranscode
func Sidecar() *SidecarApplyConfiguration                 // WithPath, WithLanguage, WithForced, WithHI
func TranscodeState() *TranscodeStateApplyConfiguration   // WithCompliant, WithProfileTag, WithLastResult
func Movie(name, namespace string) *MovieApplyConfiguration
func MovieStatus() *MovieStatusApplyConfiguration          // WithHasFile, WithFileRef, WithFileQuality, WithFileFormatScore, WithCutoffMet, WithPhase, WithConditions
func Episode(name, namespace string) *EpisodeApplyConfiguration
func EpisodeStatus() *EpisodeStatusApplyConfiguration       // same six methods, EpisodePhase instead of MoviePhase

// (reconciled by another Phase C task — importarr's file-import worker)
// Assumed shape: creates MediaFile via k8s.Apply(ctx, c, k8s.ManagerImportarrWorker, ac)
// with an ApplyConfiguration built from catalogac.MediaFile(name, ns).WithSpec(...)
// setting every MediaFileSpec field above except sizeBytes/modTime/original after
// the first transcode. This task's envtest builds that same shape by hand — it does
// not import an importarr package (none is a dependency of catalogarr).

// (reconciled by another Phase C/E/F task — squasharr's TranscodeJob worker,
// captionarr's SubtitleRequest worker)
// Assumed shape: TranscodeJob.status.phase transitions to "Succeeded" (real
// enum TranscodeJobPhaseSucceeded) with status.result.outputPath/outputSizeBytes
// set; SubtitleRequest.status.items entries reach state "downloaded" or
// "upgradable" with a non-empty relative status.items[].path. Both are real,
// already-generated types this task imports directly
// (api/transcode/v1alpha1, api/subtitle/v1alpha1); only the producing
// controllers are someone else's task.
```

## Interfaces — Produces

```go
// catalogarr/controller/mediafile
type Reconciler struct {
    Client    client.Client
    Scheme    *runtime.Scheme
    Recorder  record.EventRecorder
    Probe     ProbeFunc                // defaults to mediainfo.Probe via NewReconciler
    Catalogue *catalogue.Catalogue      // defaults to catalogue.LoadedCatalogue()
    Clock     func() time.Time          // defaults to time.Now
}
type ProbeFunc func(ctx context.Context, path string) (*commonv1.MediaInfo, *mediainfo.Raw, error)
func NewReconciler(c client.Client, scheme *runtime.Scheme, recorder record.EventRecorder) *Reconciler
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error)
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error
```

`SetupWithManager` is what the eventual `catalogarr/run.go` integration calls:
`return mediafile.NewReconciler(mgr.GetClient(), mgr.GetScheme(),
mgr.GetEventRecorderFor("mediafile-controller")).SetupWithManager(mgr)`. Not
wired in this task (see "Path ownership").

`AudioStream.ChannelLayout` (`api/common/v1alpha1/media_types.go`) and
`pkg/mediainfo`'s populated value are produced for **Phase G's `pkg/naming`
follow-up**, not consumed here: `pkg/naming/render.go`'s `audioChannelLayout`
map (line 238) still only handles 1/2/6/8 channels after this task; wiring
`{MediaInfo AudioChannels}` through `mediainfo.AudioChannelsString(stream.
ChannelLayout, stream.Channels)` instead of the static map is explicitly out
of scope here and is not this task's to fix.

---

- [ ] **Step 1: `AudioStream.ChannelLayout` — the carried Phase B item.**

  Failing test first, in `pkg/mediainfo/map_test.go` (append to
  `TestToMediaInfo`, right after the existing `mi.Audio[0]` assertions):

  ```go
  assert.Equal(t, "5.1(side)", mi.Audio[0].ChannelLayout)
  ```

  and add `ChannelLayout: "5.1(side)"` to the `Streams[1]` (index 1, the
  `eac3`/Atmos stream) literal in the same test's `raw` fixture.

  Run: `go test ./pkg/mediainfo/... -run TestToMediaInfo` — fails,
  `mi.Audio[0].ChannelLayout` does not compile (`AudioStream` has no such
  field yet).

  Implementation:
  1. In `api/common/v1alpha1/media_types.go`, add to `AudioStream` (after
     `Channels`, before `BitrateKbps`):
     ```go
     // ChannelLayout is ffprobe's channel_layout string ("5.1", "5.1(side)",
     // "stereo"), before AudioChannelsString normalizes it. Empty when
     // ffprobe did not report one; AudioChannelsString falls back to
     // "<channels>.0" in that case.
     // +optional
     ChannelLayout string `json:"channelLayout,omitempty"`
     ```
  2. `make generate && make manifests`. Expect: `git diff --stat` shows only
     `config/crd/bases/catalog.clustarr.io_mediafiles.yaml` and
     `config/crd/bases/transcode.clustarr.io_transcodejobs.yaml` (both embed
     `commonv1.MediaInfo`). Confirm
     `api/common/v1alpha1/zz_generated.deepcopy.go` has **no** diff —
     `AudioStream.DeepCopyInto` is `*out = *in`, a plain value copy, so a new
     scalar field needs no generated deepcopy change; if it does show a diff,
     something else was uncommitted before this step started and must be
     investigated, not committed. Confirm there is no diff under
     `api/applyconfiguration/` — `AudioStream` has no `+kubebuilder:ac:generate`
     apply-configuration of its own; it is embedded atomically inside
     `MediaFileStatusApplyConfiguration.MediaInfo *commonv1alpha1.MediaInfo`
     (verified: no `api/applyconfiguration/common/` directory exists and no
     `AudioStreamApplyConfiguration` type exists anywhere in the tree today).
  3. In `pkg/mediainfo/map.go`'s `toAudioStream`, add one field to the
     literal:
     ```go
     ChannelLayout: s.ChannelLayout,
     ```
     (`ffprobe.Stream.ChannelLayout string` already exists in
     `gopkg.in/vansante/go-ffprobe.v2@v2.3.1`, `json:"channel_layout"` —
     confirmed present, just unused until this line.)

  Run: `go test ./pkg/mediainfo/...` — passes.

  Commit (path-scoped):
  ```
  git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m \
    "feat(mediainfo): carry ffprobe's channel_layout onto AudioStream" -- \
    api/common/v1alpha1/media_types.go pkg/mediainfo/map.go pkg/mediainfo/map_test.go \
    config/crd/bases/catalog.clustarr.io_mediafiles.yaml \
    config/crd/bases/transcode.clustarr.io_transcodejobs.yaml
  ```

- [ ] **Step 2: probe staleness — pure function.**

  `catalogarr/controller/mediafile/probe_test.go`:
  ```go
  package mediafile

  import (
      "testing"
      "time"

      "github.com/stretchr/testify/assert"
  )

  func TestEvaluateProbe(t *testing.T) {
      path := "/data/media/movies/Inception (2010)/Inception (2010).mkv"
      mt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

      fresh := evaluateProbe(path, 1024, mt, "")
      assert.True(t, fresh.Stale, "empty status hash is always stale")
      assert.NotEmpty(t, fresh.Hash)

      same := evaluateProbe(path, 1024, mt, fresh.Hash)
      assert.False(t, same.Stale, "matching hash is not stale")

      changed := evaluateProbe(path, 2048, mt, fresh.Hash)
      assert.True(t, changed.Stale, "size change makes the hash stale")

      assert.True(t, fresh.ModTime.Time.Equal(mt))
      assert.Equal(t, int64(1024), fresh.SizeBytes)
  }
  ```

  Run: `go test ./catalogarr/controller/mediafile/... -run TestEvaluateProbe`
  — fails to compile (`evaluateProbe` undefined).

  `catalogarr/controller/mediafile/probe.go`:
  ```go
  package mediafile

  import (
      "time"

      metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

      "github.com/mediactl/clustarr/pkg/mediainfo"
  )

  // probeState is what a reconcile learns about the file on disk before
  // deciding whether to re-run ffprobe. Pure and cluster-free so the
  // staleness rule is unit-testable without envtest.
  type probeState struct {
      SizeBytes int64
      ModTime   metav1.Time
      Hash      string
      Stale     bool
  }

  // evaluateProbe hashes the file's current stat against currentHash
  // (MediaFile.status.probeHash as last observed). An empty currentHash --
  // never probed -- is always stale.
  func evaluateProbe(path string, statSize int64, statMod time.Time, currentHash string) probeState {
      h := mediainfo.ProbeHash(path, statSize, statMod)
      return probeState{
          SizeBytes: statSize,
          ModTime:   metav1.NewTime(statMod),
          Hash:      h,
          Stale:     h != currentHash,
      }
  }
  ```

  Run: `go test ./catalogarr/controller/mediafile/... -run TestEvaluateProbe`
  — passes.

  Commit: `... -m "feat(catalogarr): MediaFile probe staleness check" -- catalogarr/controller/mediafile/probe.go catalogarr/controller/mediafile/probe_test.go`

- [ ] **Step 3: label mirroring — pure function.**

  `catalogarr/controller/mediafile/labels_test.go`:
  ```go
  package mediafile

  import (
      "testing"

      "github.com/stretchr/testify/assert"

      catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
      commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
  )

  func TestMirrorLabels(t *testing.T) {
      q := commonv1.Quality{Name: "Bluray-1080p", Source: commonv1.SourceBluray, Resolution: commonv1.Resolution1080p, Modifier: commonv1.ModifierNone}
      mi := &commonv1.MediaInfo{VideoCodec: "hevc", Hdr: commonv1.HdrFormatHDR10}

      got := mirrorLabels(commonv1.MediaKindMovie, q, mi, true)

      assert.Equal(t, "movie", got[catalogv1alpha1.LabelKind])
      assert.Equal(t, "1080", got[catalogv1alpha1.LabelResolution])
      assert.Equal(t, "bluray", got[catalogv1alpha1.LabelSource])
      assert.Equal(t, "none", got[catalogv1alpha1.LabelModifier])
      assert.Equal(t, "hevc", got[catalogv1alpha1.LabelVideoCodec])
      assert.Equal(t, "hdr10", got[catalogv1alpha1.LabelHdr])
      assert.Equal(t, "true", got[catalogv1alpha1.LabelOriginal])
  }

  func TestMirrorLabelsNilMediaInfoOmitsCodecAndHdr(t *testing.T) {
      got := mirrorLabels(commonv1.MediaKindEpisode, commonv1.Quality{}, nil, false)
      assert.Equal(t, "episode", got[catalogv1alpha1.LabelKind])
      assert.Equal(t, "false", got[catalogv1alpha1.LabelOriginal])
      _, ok := got[catalogv1alpha1.LabelVideoCodec]
      assert.False(t, ok)
  }
  ```

  Run: fails (`mirrorLabels` undefined).

  `catalogarr/controller/mediafile/labels.go`:
  ```go
  package mediafile

  import (
      "strconv"

      catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
      commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
  )

  // mirrorLabels builds the catalog.clustarr.io/* label set MediaFile.status's
  // doc comment promises ("controller-mirrored labels: kind, resolution,
  // source, modifier, video-codec, hdr, original"). q is spec.quality (frozen
  // at import, never this controller's to change); mi is the current probe,
  // nil before the first successful probe.
  func mirrorLabels(kind commonv1.MediaKind, q commonv1.Quality, mi *commonv1.MediaInfo, original bool) map[string]string {
      labels := map[string]string{
          catalogv1alpha1.LabelKind:     string(kind),
          catalogv1alpha1.LabelOriginal: strconv.FormatBool(original),
      }
      if q.Resolution != 0 {
          labels[catalogv1alpha1.LabelResolution] = strconv.Itoa(int(q.Resolution))
      }
      if q.Source != "" {
          labels[catalogv1alpha1.LabelSource] = string(q.Source)
      }
      if q.Modifier != "" {
          labels[catalogv1alpha1.LabelModifier] = string(q.Modifier)
      }
      if mi != nil {
          if mi.VideoCodec != "" {
              labels[catalogv1alpha1.LabelVideoCodec] = mi.VideoCodec
          }
          labels[catalogv1alpha1.LabelHdr] = string(mi.Hdr)
      }
      return labels
  }
  ```

  Run: passes.

  Commit: `... -m "feat(catalogarr): MediaFile label mirroring" -- catalogarr/controller/mediafile/labels.go catalogarr/controller/mediafile/labels_test.go`

- [ ] **Step 4: Movie/Episode rollup — pure functions.**

  `catalogarr/controller/mediafile/rollup_test.go`:
  ```go
  package mediafile

  import (
      "testing"

      "github.com/stretchr/testify/assert"
      "github.com/stretchr/testify/require"

      catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
      commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
      "github.com/mediactl/clustarr/pkg/quality"
      "github.com/mediactl/clustarr/pkg/quality/catalogue"
  )

  func testProfile(t *testing.T) quality.Profile {
      t.Helper()
      cat := catalogue.LoadedCatalogue()
      crd := &catalogv1alpha1.QualityProfile{
          Spec: catalogv1alpha1.QualityProfileSpec{
              MediaKind: catalogv1alpha1.ProfileMediaKindVideo,
              Tiers: []catalogv1alpha1.Tier{
                  {Name: "Bluray-1080p", Qualities: []string{"Bluray-1080p"}},
                  {Name: "WEB 1080p", Qualities: []string{"WEBDL-1080p"}},
              },
              Cutoff: "Bluray-1080p",
          },
      }
      p, errs := quality.FromCRD(crd, cat)
      require.Empty(t, errs)
      return p
  }

  func TestComputeRollupCutoffMet(t *testing.T) {
      p := testProfile(t)
      bluray := commonv1.Quality{Name: "Bluray-1080p", Source: commonv1.SourceBluray, Resolution: commonv1.Resolution1080p, Modifier: commonv1.ModifierNone}

      got := computeRollup(rollupInput{FileRef: "inception-abc123", Quality: bluray, FormatScore: 40, Profile: p, HasProfile: true})

      assert.True(t, got.HasFile)
      assert.Equal(t, "inception-abc123", got.FileRef)
      assert.Equal(t, bluray, got.FileQuality)
      assert.Equal(t, int32(40), got.FileFormatScore)
      assert.True(t, got.CutoffMet)
  }

  func TestComputeRollupCutoffUnmet(t *testing.T) {
      p := testProfile(t)
      web := commonv1.Quality{Name: "WEBDL-1080p", Source: commonv1.SourceWebDL, Resolution: commonv1.Resolution1080p, Modifier: commonv1.ModifierNone}

      got := computeRollup(rollupInput{FileRef: "inception-abc123", Quality: web, FormatScore: 0, Profile: p, HasProfile: true})

      assert.False(t, got.CutoffMet)
  }

  func TestMoviePhaseForFile(t *testing.T) {
      assert.Equal(t, catalogv1alpha1.MoviePhaseImported, moviePhaseForFile(true))
      assert.Equal(t, catalogv1alpha1.MoviePhaseCutoffUnmet, moviePhaseForFile(false))
  }

  func TestEpisodePhaseForFile(t *testing.T) {
      assert.Equal(t, catalogv1alpha1.EpisodePhaseImported, episodePhaseForFile(true))
      assert.Equal(t, catalogv1alpha1.EpisodePhaseCutoffUnmet, episodePhaseForFile(false))
  }
  ```

  Run: fails to compile (`rollupInput`, `computeRollup`,
  `moviePhaseForFile`, `episodePhaseForFile` undefined).

  `catalogarr/controller/mediafile/rollup.go`:
  ```go
  package mediafile

  import (
      catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
      commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
      "github.com/mediactl/clustarr/pkg/quality"
  )

  // rollupInput is the MediaFile-derived half of what an owning Movie or
  // Episode's status rollup needs; the other half (which item, which
  // condition helper) is the reconciler's, not this pure function's.
  type rollupInput struct {
      FileRef     string
      Quality     commonv1.Quality
      FormatScore int32
      Profile     quality.Profile
      HasProfile  bool // false when the item's QualityProfileRef did not resolve
  }

  type rollupResult struct {
      HasFile         bool
      FileRef         string
      FileQuality     commonv1.Quality
      FileFormatScore int32
      CutoffMet       bool
  }

  // computeRollup mirrors a MediaFile onto the four fields spec §4.2 lists as
  // MediaFile-derived on Movie/Episode. An unresolved profile reports
  // CutoffMet=false rather than guessing -- amendment §A1.5's never-guess rule
  // extended to a missing reference, not just an unmatched file.
  func computeRollup(in rollupInput) rollupResult {
      cutoffMet := false
      if in.HasProfile {
          cutoffMet = in.Profile.CutoffMet(in.Quality)
      }
      return rollupResult{
          HasFile:         true,
          FileRef:         in.FileRef,
          FileQuality:     in.Quality,
          FileFormatScore: in.FormatScore,
          CutoffMet:       cutoffMet,
      }
  }

  // moviePhaseForFile and episodePhaseForFile pick the terminal phase once a
  // file exists; the reconciler only calls these on the has-a-file edge, never
  // overwriting Pending/Unavailable/Wanted/Delayed/Downloading phases that
  // belong to the item's own controller.
  func moviePhaseForFile(cutoffMet bool) catalogv1alpha1.MoviePhase {
      if cutoffMet {
          return catalogv1alpha1.MoviePhaseImported
      }
      return catalogv1alpha1.MoviePhaseCutoffUnmet
  }

  func episodePhaseForFile(cutoffMet bool) catalogv1alpha1.EpisodePhase {
      if cutoffMet {
          return catalogv1alpha1.EpisodePhaseImported
      }
      return catalogv1alpha1.EpisodePhaseCutoffUnmet
  }
  ```

  Run: `go test ./catalogarr/controller/mediafile/... -run 'TestComputeRollup|TestMoviePhaseForFile|TestEpisodePhaseForFile'` — passes.

  Commit: `... -m "feat(catalogarr): MediaFile->Movie/Episode status rollup" -- catalogarr/controller/mediafile/rollup.go catalogarr/controller/mediafile/rollup_test.go`

- [ ] **Step 5: sidecar feedback — pure function.**

  `catalogarr/controller/mediafile/sidecars_test.go`:
  ```go
  package mediafile

  import (
      "testing"

      "github.com/stretchr/testify/assert"
      "github.com/stretchr/testify/require"

      subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
  )

  func TestSidecarsFromSubtitleRequest(t *testing.T) {
      items := []subtitlev1alpha1.SubtitleItem{
          {LangKey: "en", State: subtitlev1alpha1.SubtitleItemDownloaded, Path: "Inception (2010).en.srt"},
          {LangKey: "en:forced", State: subtitlev1alpha1.SubtitleItemDownloaded, Path: "Inception (2010).en.forced.srt"},
          {LangKey: "pt-BR:hi", State: subtitlev1alpha1.SubtitleItemUpgradable, Path: "Inception (2010).pt-BR.sdh.srt"},
          {LangKey: "fr", State: subtitlev1alpha1.SubtitleItemSearching}, // no path yet -- excluded
      }
      dir := "/data/media/movies/Inception (2010)"

      got, err := sidecarsFromSubtitleRequest(dir, items)
      require.NoError(t, err)
      require.Len(t, got, 3)

      assert.Equal(t, "/data/media/movies/Inception (2010)/Inception (2010).en.srt", got[0].Path)
      assert.Equal(t, "en", got[0].Language)
      assert.False(t, got[0].Forced)
      assert.False(t, got[0].HI)

      assert.True(t, got[1].Forced)

      assert.Equal(t, "pt-BR", got[2].Language)
      assert.True(t, got[2].HI)
  }
  ```

  Run: fails (`sidecarsFromSubtitleRequest` undefined).

  `catalogarr/controller/mediafile/sidecars.go`:
  ```go
  package mediafile

  import (
      "fmt"
      "path/filepath"

      catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
      subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
      "github.com/mediactl/clustarr/pkg/subtitles"
  )

  // sidecarsFromSubtitleRequest is §8.6's "sidecar feedback path": it mirrors
  // a SubtitleRequest's downloaded/upgradable items onto MediaFile.status.
  // sidecars. It never walks the filesystem itself -- pkg/subtitles' own doc
  // comment says sidecar-path naming belongs to captionarr (Phase F); this
  // function only trusts what captionarr already wrote to status.items.path,
  // which spec §4.6 documents as relative to the media file's directory,
  // where MediaFileStatus.Sidecars.Path is documented absolute.
  func sidecarsFromSubtitleRequest(mediaFileDir string, items []subtitlev1alpha1.SubtitleItem) ([]catalogv1alpha1.Sidecar, error) {
      out := make([]catalogv1alpha1.Sidecar, 0, len(items))
      for _, it := range items {
          if it.Path == "" {
              continue
          }
          if it.State != subtitlev1alpha1.SubtitleItemDownloaded && it.State != subtitlev1alpha1.SubtitleItemUpgradable {
              continue
          }
          lang, forced, hi, err := subtitles.ParseLangKey(subtitles.LangKey(it.LangKey))
          if err != nil {
              return nil, fmt.Errorf("mediafile: sidecar langKey %q: %w", it.LangKey, err)
          }
          out = append(out, catalogv1alpha1.Sidecar{
              Path:     filepath.Join(mediaFileDir, it.Path),
              Language: lang,
              Forced:   forced,
              HI:       hi,
          })
      }
      return out, nil
  }
  ```

  Run: passes.

  Commit: `... -m "feat(catalogarr): MediaFile sidecar feedback from SubtitleRequest" -- catalogarr/controller/mediafile/sidecars.go catalogarr/controller/mediafile/sidecars_test.go`

- [ ] **Step 6: watch predicates and mapping functions.**

  `catalogarr/controller/mediafile/watch_test.go`:
  ```go
  package mediafile

  import (
      "testing"

      "github.com/stretchr/testify/assert"
      metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
      "sigs.k8s.io/controller-runtime/pkg/client/fake"
      "sigs.k8s.io/controller-runtime/pkg/reconcile"

      subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
      transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
      "github.com/mediactl/clustarr/pkg/k8s"
  )

  func TestExtractTranscodeJobPhase(t *testing.T) {
      tj := &transcodev1alpha1.TranscodeJob{Status: transcodev1alpha1.TranscodeJobStatus{Phase: transcodev1alpha1.TranscodeJobPhaseSucceeded}}
      assert.Equal(t, "Succeeded", extractTranscodeJobPhase(tj))

      other := &subtitlev1alpha1.SubtitleRequest{}
      assert.Equal(t, "", extractTranscodeJobPhase(other), "wrong type returns the zero value, never panics")
  }

  func TestMediaFileForTranscodeJob(t *testing.T) {
      c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).Build()
      tj := &transcodev1alpha1.TranscodeJob{
          ObjectMeta: metav1.ObjectMeta{Name: "inception-abc12345", Namespace: "media"},
          Spec:       transcodev1alpha1.TranscodeJobSpec{MediaFileRef: "inception-abc1234567"},
      }

      reqs := (&Reconciler{Client: c}).mediaFileForTranscodeJob(t.Context(), tj)

      want := []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: "media", Name: "inception-abc1234567"}}}
      assert.Equal(t, want, reqs)
  }
  ```

  (imports: `"k8s.io/apimachinery/pkg/types"`, `"sigs.k8s.io/controller-runtime/
  pkg/client/fake"`, `"sigs.k8s.io/controller-runtime/pkg/reconcile"`, plus
  `transcodev1alpha1`, `k8s` and `t.Context()` from Go 1.24's `testing.T`,
  already the toolchain's `go 1.27`.)

  Run: fails to compile (`extractTranscodeJobPhase`, `mediaFileForTranscodeJob`
  undefined).

  `catalogarr/controller/mediafile/watch.go`:
  ```go
  package mediafile

  import (
      "context"

      "sigs.k8s.io/controller-runtime/pkg/client"
      "sigs.k8s.io/controller-runtime/pkg/reconcile"
      "k8s.io/apimachinery/pkg/types"

      subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
      transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
  )

  // Field index keys. Registered against TranscodeJob and SubtitleRequest so
  // Reconcile can List "every {TranscodeJob,SubtitleRequest} for this
  // MediaFile" -- the direction the mapping functions below don't need,
  // because both spec types name their MediaFile directly.
  const (
      transcodeJobMediaFileRefIndex    = ".spec.mediaFileRef"
      subtitleRequestMediaFileRefIndex = ".spec.mediaFileRef"
  )

  func indexTranscodeJobByMediaFileRef(o client.Object) []string {
      tj, ok := o.(*transcodev1alpha1.TranscodeJob)
      if !ok || tj.Spec.MediaFileRef == "" {
          return nil
      }
      return []string{tj.Spec.MediaFileRef}
  }

  func indexSubtitleRequestByMediaFileRef(o client.Object) []string {
      sr, ok := o.(*subtitlev1alpha1.SubtitleRequest)
      if !ok || sr.Spec.MediaFileRef == "" {
          return nil
      }
      return []string{sr.Spec.MediaFileRef}
  }

  // extractTranscodeJobPhase is §10's predicate: catalogarr's mediafile
  // controller only reacts to a TranscodeJob reaching Succeeded, via
  // k8s.StatusFieldIn. A wrong-typed object (never happens in practice --
  // Watches only calls this for TranscodeJobs -- but StatusFieldChanged's own
  // doc comment models exactly this defensive shape) returns "".
  func extractTranscodeJobPhase(o client.Object) string {
      tj, ok := o.(*transcodev1alpha1.TranscodeJob)
      if !ok {
          return ""
      }
      return string(tj.Status.Phase)
  }

  // extractSubtitleItemsSignature is §10's other predicate:
  // "status.items changed", via k8s.StatusFieldChanged. Items is a slice, not
  // comparable, so this renders a comparable projection of the fields the
  // sidecar feedback path cares about (langKey/state/path), not the whole
  // struct -- a score or attempts-count change alone should not re-trigger
  // this controller.
  func extractSubtitleItemsSignature(o client.Object) string {
      sr, ok := o.(*subtitlev1alpha1.SubtitleRequest)
      if !ok {
          return ""
      }
      sig := ""
      for _, it := range sr.Status.Items {
          sig += string(it.LangKey) + "=" + string(it.State) + ":" + it.Path + ";"
      }
      return sig
  }

  // mediaFileForTranscodeJob and mediaFileForSubtitleRequest map the watched
  // object straight to its named MediaFile -- both spec types carry the ref
  // directly, so no List/field-index round trip is needed here (the index
  // above is for the reverse direction, used inside Reconcile).
  func (r *Reconciler) mediaFileForTranscodeJob(_ context.Context, o client.Object) []reconcile.Request {
      tj, ok := o.(*transcodev1alpha1.TranscodeJob)
      if !ok || tj.Spec.MediaFileRef == "" {
          return nil
      }
      return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: tj.Namespace, Name: tj.Spec.MediaFileRef}}}
  }

  func (r *Reconciler) mediaFileForSubtitleRequest(_ context.Context, o client.Object) []reconcile.Request {
      sr, ok := o.(*subtitlev1alpha1.SubtitleRequest)
      if !ok || sr.Spec.MediaFileRef == "" {
          return nil
      }
      return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: sr.Namespace, Name: sr.Spec.MediaFileRef}}}
  }
  ```

  Run: `go test ./catalogarr/controller/mediafile/... -run 'TestExtractTranscodeJobPhase|TestMediaFileForTranscodeJob'` — passes.

  Commit: `... -m "feat(catalogarr): MediaFile watch predicates and mapping funcs" -- catalogarr/controller/mediafile/watch.go catalogarr/controller/mediafile/watch_test.go`

- [ ] **Step 7: the Reconciler and its Reconcile loop.**

  No new failing unit test here — Steps 2–6's pure functions are already
  covered; this step wires them into the controller and is proven by Step 9's
  envtest, TDD'd there instead of here (an envtest that doesn't compile is
  still a legitimate "red" state to start Step 9 from).

  `catalogarr/controller/mediafile/mediafile_controller.go`:
  ```go
  package mediafile

  import (
      "context"
      "fmt"
      "os"
      "path/filepath"
      "time"

      apierrors "k8s.io/apimachinery/pkg/api/errors"
      metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
      "k8s.io/apimachinery/pkg/runtime"
      "k8s.io/apimachinery/pkg/types"
      "k8s.io/client-go/tools/record"
      ctrl "sigs.k8s.io/controller-runtime"
      "sigs.k8s.io/controller-runtime/pkg/builder"
      "sigs.k8s.io/controller-runtime/pkg/client"
      "sigs.k8s.io/controller-runtime/pkg/controller"
      "sigs.k8s.io/controller-runtime/pkg/handler"
      "sigs.k8s.io/controller-runtime/pkg/reconcile"
      "k8s.io/utils/ptr"

      catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
      catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
      commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
      subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
      transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
      "github.com/mediactl/clustarr/pkg/k8s"
      "github.com/mediactl/clustarr/pkg/mediainfo"
      "github.com/mediactl/clustarr/pkg/obs/logging"
      "github.com/mediactl/clustarr/pkg/obs/tracing"
      "github.com/mediactl/clustarr/pkg/quality"
      "github.com/mediactl/clustarr/pkg/quality/catalogue"
  )

  // Reconciler owns 100% of MediaFile.status (see this section's "Resolving
  // the field-manager split") plus, narrowly, spec.sizeBytes/modTime/original
  // after a transcode swap, plus the HasFile/FileRef/FileQuality/
  // FileFormatScore/CutoffMet/Phase rollup on the owning Movie/Episode.
  type Reconciler struct {
      client.Client
      Scheme    *runtime.Scheme
      Recorder  record.EventRecorder
      Probe     ProbeFunc
      Catalogue *catalogue.Catalogue
      Clock     func() time.Time
  }

  type ProbeFunc func(ctx context.Context, path string) (*commonv1.MediaInfo, *mediainfo.Raw, error)

  func NewReconciler(c client.Client, scheme *runtime.Scheme, recorder record.EventRecorder) *Reconciler {
      return &Reconciler{
          Client:    c,
          Scheme:    scheme,
          Recorder:  recorder,
          Probe:     mediainfo.Probe,
          Catalogue: catalogue.LoadedCatalogue(),
          Clock:     time.Now,
      }
  }

  func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
      ctx, span := tracing.Start(ctx, "mediafile.Reconcile")
      defer span.End()
      ctx = logging.With(ctx, "mediafile", req.Name, "namespace", req.Namespace)
      log := logging.FromContext(ctx)

      var mf catalogv1alpha1.MediaFile
      if err := r.Get(ctx, req.NamespacedName, &mf); err != nil {
          return ctrl.Result{}, client.IgnoreNotFound(err)
      }
      if k8s.IsDeleting(&mf) {
          return ctrl.Result{}, nil
      }

      conditions := append([]metav1.Condition(nil), mf.Status.Conditions...)
      now := metav1.NewTime(r.Clock())

      info, statErr := os.Stat(mf.Spec.Path)
      if statErr != nil {
          k8s.MarkFalse(&mf, &conditions, catalogv1alpha1.MediaFileConditionReady, "FileMissing", "stat %s: %s", mf.Spec.Path, statErr)
          if _, err := k8s.PatchStatus(ctx, r.Client, k8s.ManagerCatalogarr,
              catalogac.MediaFile(mf.Name, mf.Namespace).WithStatus(
                  catalogac.MediaFileStatus().
                      WithObservedGeneration(mf.Generation).
                      WithConditions(k8s.ConditionACs(conditions)...),
              )); err != nil {
              return ctrl.Result{}, err
          }
          return ctrl.Result{RequeueAfter: time.Minute}, nil
      }

      ps := evaluateProbe(mf.Spec.Path, info.Size(), info.ModTime(), mf.Status.ProbeHash)

      // Detect an unincorporated successful transcode: the newest Succeeded
      // TranscodeJob for this MediaFile whose result landed after the last
      // probe. §8.5: "worker swaps file at the same path" -- spec.path never
      // changes here, only sizeBytes/modTime/original.
      swap, err := r.latestUnincorporatedTranscode(ctx, &mf)
      if err != nil {
          return ctrl.Result{}, err
      }

      if swap != nil || ps.Stale || mf.Status.ProbeHash == "" {
          mi, _, probeErr := r.Probe(ctx, mf.Spec.Path)
          if probeErr != nil {
              k8s.MarkFalse(&mf, &conditions, catalogv1alpha1.MediaFileConditionProbed, "ProbeFailed", "%s", probeErr)
              k8s.MarkFalse(&mf, &conditions, catalogv1alpha1.MediaFileConditionReady, "ProbeFailed", "probe failed: %s", probeErr)
              if _, serr := k8s.PatchStatus(ctx, r.Client, k8s.ManagerCatalogarr,
                  catalogac.MediaFile(mf.Name, mf.Namespace).WithStatus(
                      catalogac.MediaFileStatus().
                          WithObservedGeneration(mf.Generation).
                          WithConditions(k8s.ConditionACs(conditions)...),
                  )); serr != nil {
                  return ctrl.Result{}, serr
              }
              return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
          }

          original := mf.Spec.Original == nil || *mf.Spec.Original
          if swap != nil {
              original = false
              specAC := catalogac.MediaFileSpec().
                  WithSizeBytes(ps.SizeBytes).
                  WithModTime(ps.ModTime).
                  WithOriginal(false)
              if _, err := k8s.Apply(ctx, r.Client, k8s.ManagerCatalogarr,
                  catalogac.MediaFile(mf.Name, mf.Namespace).WithSpec(specAC)); err != nil {
                  return ctrl.Result{}, err
              }
              log.Info("incorporated transcode swap", "transcodeJob", swap.Name)
          }

          labels := mirrorLabels(mf.Spec.MediaRef.Kind, mf.Spec.Quality, mi, original)
          if _, err := k8s.Apply(ctx, r.Client, k8s.ManagerCatalogarr,
              catalogac.MediaFile(mf.Name, mf.Namespace).WithLabels(labels)); err != nil {
              return ctrl.Result{}, err
          }

          k8s.MarkTrue(&mf, &conditions, catalogv1alpha1.MediaFileConditionProbed, "Probed", "probed at %s", now.Time)
          k8s.MarkTrue(&mf, &conditions, catalogv1alpha1.MediaFileConditionReady, "Ready", "file present and probed")

          statusAC := catalogac.MediaFileStatus().
              WithObservedGeneration(mf.Generation).
              WithProbeHash(ps.Hash).
              WithProbedAt(now).
              WithMediaInfo(*mi).
              WithConditions(k8s.ConditionACs(conditions)...)

          if swap != nil {
              profileTag, terr := r.transcodeProfileTag(ctx, swap)
              if terr != nil {
                  return ctrl.Result{}, terr
              }
              statusAC = statusAC.WithTranscode(catalogac.TranscodeState().
                  WithCompliant(true).
                  WithProfileTag(profileTag).
                  WithLastResult(catalogv1alpha1.TranscodeResultSucceeded))
          }

          if _, err := k8s.PatchStatus(ctx, r.Client, k8s.ManagerCatalogarr,
              catalogac.MediaFile(mf.Name, mf.Namespace).WithStatus(statusAC)); err != nil {
              return ctrl.Result{}, err
          }

          if r.Recorder != nil {
              r.Recorder.Eventf(&mf, nil, "Normal", "Probed", "Probing", "probed %s", mf.Spec.Path)
          }
      }

      if err := r.rescanSidecars(ctx, &mf); err != nil {
          return ctrl.Result{}, err
      }
      if err := r.rollupToOwner(ctx, &mf); err != nil {
          return ctrl.Result{}, err
      }

      return ctrl.Result{}, nil
  }

  func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
      ctx := context.Background()
      if err := mgr.GetFieldIndexer().IndexField(ctx, &transcodev1alpha1.TranscodeJob{}, transcodeJobMediaFileRefIndex, indexTranscodeJobByMediaFileRef); err != nil {
          return err
      }
      if err := mgr.GetFieldIndexer().IndexField(ctx, &subtitlev1alpha1.SubtitleRequest{}, subtitleRequestMediaFileRefIndex, indexSubtitleRequestByMediaFileRef); err != nil {
          return err
      }
      return ctrl.NewControllerManagedBy(mgr).
          Named("mediafile").
          For(&catalogv1alpha1.MediaFile{}, builder.WithPredicates(k8s.GenerationChanged())).
          Watches(&transcodev1alpha1.TranscodeJob{}, handler.EnqueueRequestsFromMapFunc(r.mediaFileForTranscodeJob),
              builder.WithPredicates(k8s.StatusFieldIn(extractTranscodeJobPhase, string(transcodev1alpha1.TranscodeJobPhaseSucceeded)))).
          Watches(&subtitlev1alpha1.SubtitleRequest{}, handler.EnqueueRequestsFromMapFunc(r.mediaFileForSubtitleRequest),
              builder.WithPredicates(k8s.StatusFieldChanged(extractSubtitleItemsSignature))).
          WithOptions(controller.Options{
              RecoverPanic:          ptr.To(true),
              ReconciliationTimeout: 5 * time.Minute,
          }).
          Complete(r)
  }
  ```

  This step also needs `r.latestUnincorporatedTranscode`, `r.transcodeProfileTag`,
  `r.rescanSidecars` and `r.rollupToOwner` — write these in the same file:

  ```go
  // latestUnincorporatedTranscode lists TranscodeJobs for mf via the field
  // index and returns the most recently finished Succeeded one whose
  // FinishedAt is after mf.Status.ProbedAt, or nil if none. This is what
  // tells Reconcile "a transcode swap happened and I have not folded it in
  // yet" without needing to know which watch woke it up.
  func (r *Reconciler) latestUnincorporatedTranscode(ctx context.Context, mf *catalogv1alpha1.MediaFile) (*transcodev1alpha1.TranscodeJob, error) {
      var list transcodev1alpha1.TranscodeJobList
      if err := r.List(ctx, &list, client.InNamespace(mf.Namespace), client.MatchingFields{transcodeJobMediaFileRefIndex: mf.Name}); err != nil {
          return nil, fmt.Errorf("mediafile: list TranscodeJobs: %w", err)
      }
      var latest *transcodev1alpha1.TranscodeJob
      for i := range list.Items {
          tj := &list.Items[i]
          if tj.Status.Phase != transcodev1alpha1.TranscodeJobPhaseSucceeded || tj.Status.FinishedAt == nil {
              continue
          }
          if mf.Status.ProbedAt != nil && !tj.Status.FinishedAt.After(mf.Status.ProbedAt.Time) {
              continue
          }
          if latest == nil || tj.Status.FinishedAt.After(latest.Status.FinishedAt.Time) {
              latest = tj
          }
      }
      return latest, nil
  }

  // transcodeProfileTag renders §4.5's "CLUSTARR_PROFILE=<name>@<hash>"
  // convention from the TranscodeProfile the job ran against.
  func (r *Reconciler) transcodeProfileTag(ctx context.Context, tj *transcodev1alpha1.TranscodeJob) (string, error) {
      var tp transcodev1alpha1.TranscodeProfile
      if err := r.Get(ctx, types.NamespacedName{Name: tj.Spec.ProfileRef}, &tp); err != nil {
          return "", fmt.Errorf("mediafile: get TranscodeProfile %s: %w", tj.Spec.ProfileRef, err)
      }
      return fmt.Sprintf("%s@%s", tj.Spec.ProfileRef, tp.Status.Hash), nil
  }

  // rescanSidecars folds every SubtitleRequest for mf into status.sidecars.
  // One SubtitleRequest per video MediaFile by convention (spec §4.6), but
  // this lists rather than Gets-by-name so a missing or renamed request never
  // errors the reconcile.
  func (r *Reconciler) rescanSidecars(ctx context.Context, mf *catalogv1alpha1.MediaFile) error {
      var list subtitlev1alpha1.SubtitleRequestList
      if err := r.List(ctx, &list, client.InNamespace(mf.Namespace), client.MatchingFields{subtitleRequestMediaFileRefIndex: mf.Name}); err != nil {
          return fmt.Errorf("mediafile: list SubtitleRequests: %w", err)
      }
      if len(list.Items) == 0 {
          return nil
      }
      dir := filepath.Dir(mf.Spec.Path)
      sidecars, err := sidecarsFromSubtitleRequest(dir, list.Items[0].Status.Items)
      if err != nil {
          return err
      }
      acs := make([]*catalogac.SidecarApplyConfiguration, 0, len(sidecars))
      for _, s := range sidecars {
          acs = append(acs, catalogac.Sidecar().WithPath(s.Path).WithLanguage(s.Language).WithForced(s.Forced).WithHI(s.HI))
      }
      _, err = k8s.PatchStatus(ctx, r.Client, k8s.ManagerCatalogarr,
          catalogac.MediaFile(mf.Name, mf.Namespace).WithStatus(
              catalogac.MediaFileStatus().WithObservedGeneration(mf.Generation).WithSidecars(acs...),
          ))
      return err
  }

  // rollupToOwner mirrors mf onto its owning Movie or Episode's
  // HasFile/FileRef/FileQuality/FileFormatScore/CutoffMet/Phase, scoped to
  // those two kinds for this task (see "Scope decision" above). Both are
  // catalog.clustarr.io kinds catalogarr's controller manager already owns
  // phase/conditions on (pkg/k8s's ManagerCatalogarr doc comment), so this
  // reuses the same field manager rather than inventing a third.
  func (r *Reconciler) rollupToOwner(ctx context.Context, mf *catalogv1alpha1.MediaFile) error {
      profileRef, err := r.ownerQualityProfileRef(ctx, mf)
      if err != nil && !apierrors.IsNotFound(err) {
          return err
      }
      var profile quality.Profile
      hasProfile := false
      if profileRef != "" {
          var qp catalogv1alpha1.QualityProfile
          if gerr := r.Get(ctx, types.NamespacedName{Name: profileRef}, &qp); gerr == nil {
              p, errs := quality.FromCRD(&qp, r.Catalogue)
              if len(errs) == 0 {
                  profile, hasProfile = p, true
              }
          }
      }

      res := computeRollup(rollupInput{
          FileRef:     mf.Name,
          Quality:     mf.Spec.Quality,
          FormatScore: mf.Spec.FormatScore,
          Profile:     profile,
          HasProfile:  hasProfile,
      })

      switch mf.Spec.MediaRef.Kind {
      case commonv1.MediaKindMovie:
          return r.applyMovieRollup(ctx, mf.Namespace, mf.Spec.MediaRef.Name, res)
      case commonv1.MediaKindEpisode:
          return r.applyEpisodeRollup(ctx, mf.Namespace, mf.Spec.MediaRef.Name, res)
      default:
          return nil // out of scope for this task -- see "Scope decision"
      }
  }
  ```

  `ownerQualityProfileRef`, `applyMovieRollup` and `applyEpisodeRollup` follow
  the same pattern (`Get` the owner or, for Episode, the owner's Series;
  `PatchStatus` under `k8s.ManagerCatalogarr` with exactly `HasFile`,
  `FileRef`, `FileQuality`, `FileFormatScore`, `CutoffMet`, `Phase` and the
  `HasFile`/`CutoffMet` conditions via `k8s.MarkTrue`/`k8s.MarkFalse` — write
  them the same way Step 4's `rollupResult` and Step 3's condition helpers
  already demonstrate; omitted here for length, not because they differ in
  kind from what is already fully specified above. `ownerQualityProfileRef`'s
  contract: `func (r *Reconciler) ownerQualityProfileRef(ctx context.Context,
  mf *catalogv1alpha1.MediaFile) (string, error)` — for `MediaKindMovie` it
  `Get`s the `Movie` named `mf.Spec.MediaRef.Name` and returns its
  `Spec.QualityProfileRef`; for `MediaKindEpisode` it `Get`s the `Episode`,
  then `Get`s the `Series` named by the `Episode`'s `Spec.SeriesRef`, and
  returns the `Series`' `Spec.QualityProfileRef` (Episode carries no
  `QualityProfileRef` of its own — confirmed absent from
  `api/catalog/v1alpha1/episode_types.go`); any `Get` returning a
  `k8s.io/apimachinery/pkg/api/errors.IsNotFound` error is returned as-is
  (`rollupToOwner` above already tolerates it and proceeds with
  `HasProfile: false`), any other `Get` error is returned as a hard failure.
  `rescanSidecars` above uses `filepath.Dir` from the `"path/filepath"`
  import already listed at the top of this file.

  Run: `go build ./catalogarr/...` — compiles clean (the package now has
  everything Step 9's envtest needs; `go vet ./catalogarr/...` too).

  Commit: `... -m "feat(catalogarr): MediaFile Reconciler wiring" -- catalogarr/controller/mediafile/mediafile_controller.go`

- [ ] **Step 8: shared envtest scaffolding.**

  `catalogarr/controller/mediafile/mediafile_envtest_test.go`, the shared
  helpers every test in this file and the next three steps use:
  ```go
  package mediafile_test

  import (
      "context"
      "os"
      "path/filepath"
      "testing"
      "time"

      corev1 "k8s.io/api/core/v1"
      metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
      "k8s.io/apimachinery/pkg/types"
      "k8s.io/client-go/rest"
      ctrl "sigs.k8s.io/controller-runtime"
      "sigs.k8s.io/controller-runtime/pkg/client"
      "sigs.k8s.io/controller-runtime/pkg/envtest"

      catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
      catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
      commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
      "github.com/mediactl/clustarr/catalogarr/controller/mediafile"
      "github.com/mediactl/clustarr/pkg/k8s"
  )

  func startEnv(t *testing.T) (client.Client, *rest.Config) {
      t.Helper()
      if os.Getenv("KUBEBUILDER_ASSETS") == "" {
          t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
      }
      env := &envtest.Environment{CRDDirectoryPaths: []string{"../../../config/crd/bases"}, ErrorIfCRDPathMissing: true}
      cfg, err := env.Start()
      if err != nil {
          t.Fatalf("start envtest: %v", err)
      }
      t.Cleanup(func() { _ = env.Stop() })
      c, err := client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
      if err != nil {
          t.Fatalf("build client: %v", err)
      }
      return c, cfg
  }

  func mustNamespace(t *testing.T, ctx context.Context, c client.Client, ns string) {
      t.Helper()
      if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}); err != nil && client.IgnoreAlreadyExists(err) != nil {
          t.Fatalf("create namespace %s: %v", ns, err)
      }
  }

  // importarrCreatesMediaFile simulates the file-import worker task this task
  // does not own: it Applies the full MediaFileSpec under
  // k8s.ManagerImportarrWorker, exactly the fields "Resolving the
  // field-manager split" assigns importarr, none of the three catalogarr may
  // later take over.
  func importarrCreatesMediaFile(t *testing.T, ctx context.Context, c client.Client, ns, name, path string, size int64, modTime time.Time, q commonv1.Quality) {
      t.Helper()
      ac := catalogac.MediaFile(name, ns).WithSpec(
          catalogac.MediaFileSpec().
              WithMediaRef(commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "inception"}).
              WithPath(path).
              WithSizeBytes(size).
              WithModTime(metav1.NewTime(modTime)).
              WithQuality(q).
              WithRevision(commonv1.Revision{Version: 1}).
              WithReleaseType(commonv1.ReleaseTypeSingle).
              WithFormatScore(40).
              WithProfileHash("profile-hash-abc").
              WithOriginal(true),
      )
      if _, err := k8s.Apply(ctx, c, k8s.ManagerImportarrWorker, ac); err != nil {
          t.Fatalf("simulate importarr create: %v", err)
      }
  }

  func mustQualityProfile(t *testing.T, ctx context.Context, c client.Client, name string) {
      t.Helper()
      qp := &catalogv1alpha1.QualityProfile{
          ObjectMeta: metav1.ObjectMeta{Name: name},
          Spec: catalogv1alpha1.QualityProfileSpec{
              MediaKind: catalogv1alpha1.ProfileMediaKindVideo,
              Tiers: []catalogv1alpha1.Tier{
                  {Name: "Bluray-1080p", Qualities: []string{"Bluray-1080p"}},
                  {Name: "WEB 1080p", Qualities: []string{"WEBDL-1080p"}},
              },
              Cutoff: "Bluray-1080p",
          },
      }
      if err := c.Create(ctx, qp); err != nil && client.IgnoreAlreadyExists(err) != nil {
          t.Fatalf("create QualityProfile: %v", err)
      }
  }

  func mustMovie(t *testing.T, ctx context.Context, c client.Client, ns, name, qualityProfile string) {
      t.Helper()
      m := &catalogv1alpha1.Movie{
          ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
          Spec: catalogv1alpha1.MovieSpec{
              TmdbID:            27205,
              QualityProfileRef: qualityProfile,
              RootFolderRef:     "movies",
          },
      }
      if err := c.Create(ctx, m); err != nil && client.IgnoreAlreadyExists(err) != nil {
          t.Fatalf("create Movie: %v", err)
      }
  }

  func writeFile(t *testing.T, dir, name string, contents []byte) string {
      t.Helper()
      path := filepath.Join(dir, name)
      if err := os.WriteFile(path, contents, 0o644); err != nil {
          t.Fatalf("write %s: %v", path, err)
      }
      return path
  }
  ```

  This step
  produces no new passing test on its own — it is scaffolding Steps 9–13
  consume — but must compile: `go vet ./catalogarr/controller/mediafile/...`
  with `KUBEBUILDER_ASSETS` unset still type-checks the file even though every
  test that calls `startEnv` will skip.

  Commit: bundled with Step 9 (same file, first meaningful commit lands once a
  test passes).

  Steps 9–13 append more functions to this one file and each pulls in a few
  more imports beyond Step 8's list: Step 9 needs
  `"github.com/stretchr/testify/assert"`, `"github.com/stretchr/testify/
  require"`, `"github.com/mediactl/clustarr/pkg/mediainfo"`,
  `"github.com/mediactl/clustarr/pkg/quality/catalogue"` and
  `transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"`;
  Step 13 additionally needs `subtitlev1alpha1
  "github.com/mediactl/clustarr/api/subtitle/v1alpha1"`; Steps 12–13
  additionally need `"k8s.io/client-go/tools/record"` and
  `metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"`. Add
  each to the single shared import block at the top of the file as its step
  introduces it — `goimports`/`make fmt` will flag anything missed.

- [ ] **Step 9 (mandatory gate): the two-writer envtest.**

  Append to `mediafile_envtest_test.go`:
  ```go
  func TestMediaFileFieldManagersStayDisjoint(t *testing.T) {
      c, _ := startEnv(t)
      ctx := t.Context()
      const ns, name = "twowriter", "inception-abc1234567"
      dir := t.TempDir()
      mustNamespace(t, ctx, c, ns)
      mustQualityProfile(t, ctx, c, "hd-bluray-web-test")
      mustMovie(t, ctx, c, ns, "inception", "hd-bluray-web-test")

      path := writeFile(t, dir, "Inception (2010).mkv", []byte("stand-in bytes"))
      stat, err := os.Stat(path)
      if err != nil {
          t.Fatal(err)
      }
      importarrCreatesMediaFile(t, ctx, c, ns, name, path, stat.Size(), stat.ModTime(),
          commonv1.Quality{Name: "WEBDL-1080p", Source: commonv1.SourceWebDL, Resolution: commonv1.Resolution1080p, Modifier: commonv1.ModifierNone})

      r := &mediafile.Reconciler{Client: c, Probe: fakeProbe, Catalogue: catalogue.LoadedCatalogue(), Clock: time.Now}
      if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: name}}); err != nil {
          t.Fatalf("reconcile: %v", err)
      }

      var got catalogv1alpha1.MediaFile
      if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got); err != nil {
          t.Fatalf("get: %v", err)
      }

      // importarr's fields survived catalogarr's writes.
      assert.Equal(t, "WEBDL-1080p", got.Spec.Quality.Name)
      assert.Equal(t, path, got.Spec.Path)
      assert.Equal(t, "profile-hash-abc", got.Spec.ProfileHash)

      // catalogarr produced status without ever claiming a spec field
      // importarr owns (no transcode happened yet, so it claims none).
      require.NotEmpty(t, got.Status.ProbeHash)
      require.NotNil(t, got.Status.MediaInfo)

      for _, want := range []struct{ manager, subresource string }{
          {"importarr-worker", ""},
          {"catalogarr", "status"},
      } {
          if !managesField(got.ManagedFields, want.manager, want.subresource) {
              t.Errorf("no %q/%q entry in managedFields: %+v", want.manager, want.subresource, fieldManagerNames(got.ManagedFields))
          }
      }
      if managesField(got.ManagedFields, "catalogarr", "") {
          t.Errorf("catalogarr claimed a main-resource field before any transcode: %+v", fieldManagerNames(got.ManagedFields))
      }

      // Now simulate squasharr: a Succeeded TranscodeJob at the same path.
      newContents := []byte("re-encoded stand-in bytes, longer")
      if err := os.WriteFile(path, newContents, 0o644); err != nil {
          t.Fatal(err)
      }
      newStat, err := os.Stat(path)
      if err != nil {
          t.Fatal(err)
      }
      finished := metav1.NewTime(newStat.ModTime().Add(time.Second))
      tj := &transcodev1alpha1.TranscodeJob{
          ObjectMeta: metav1.ObjectMeta{Name: name + "-abcd1234", Namespace: ns},
          Spec:       transcodev1alpha1.TranscodeJobSpec{MediaFileRef: name, ProfileRef: "hevc-main10", SourcePath: path, SourceProbeHash: got.Status.ProbeHash},
      }
      if err := c.Create(ctx, tj); err != nil {
          t.Fatalf("create TranscodeJob: %v", err)
      }
      tj.Status = transcodev1alpha1.TranscodeJobStatus{
          Phase:      transcodev1alpha1.TranscodeJobPhaseSucceeded,
          FinishedAt: &finished,
          Result:     &transcodev1alpha1.Result{OutputPath: path, OutputSizeBytes: int64(len(newContents))},
      }
      if err := c.Status().Update(ctx, tj); err != nil { // test-only direct status write against a hand-built fixture; production code never does this (forbidigo)
          t.Fatalf("set TranscodeJob status: %v", err)
      }

      if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: name}}); err != nil {
          t.Fatalf("second reconcile: %v", err)
      }

      if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got); err != nil {
          t.Fatalf("get: %v", err)
      }
      assert.Equal(t, int64(len(newContents)), got.Spec.SizeBytes)
      assert.False(t, got.Spec.Original != nil && *got.Spec.Original)
      // Quality/revision/formatScore/matchedFormats/releaseType are untouched --
      // the freeze this task's whole design exists to prove.
      assert.Equal(t, "WEBDL-1080p", got.Spec.Quality.Name)
      assert.Equal(t, int32(40), got.Spec.FormatScore)

      for _, want := range []struct{ manager, subresource string }{
          {"importarr-worker", ""}, // still owns path/quality/... (untouched fields)
          {"catalogarr", ""},       // now owns sizeBytes/modTime/original
          {"catalogarr", "status"},
      } {
          if !managesField(got.ManagedFields, want.manager, want.subresource) {
              t.Errorf("no %q/%q entry in managedFields after transcode: %+v", want.manager, want.subresource, fieldManagerNames(got.ManagedFields))
          }
      }
      require.NotNil(t, got.Status.Transcode)
      assert.True(t, got.Status.Transcode.Compliant)
  }

  func fakeProbe(_ context.Context, path string) (*commonv1.MediaInfo, *mediainfo.Raw, error) {
      if _, err := os.Stat(path); err != nil {
          return nil, nil, err // same failure shape as the real mediainfo.Probe on a missing file
      }
      return &commonv1.MediaInfo{
          Container:     "mkv",
          VideoCodec:    "h264",
          Width:         1920,
          Height:        1080,
          RuntimeMillis: 1000,
          Audio:         []commonv1.AudioStream{{Codec: "aac", Channels: 2, ChannelLayout: "stereo"}},
      }, nil, nil
  }

  func managesField(entries []metav1.ManagedFieldsEntry, manager, subresource string) bool {
      for _, e := range entries {
          if e.Manager == manager && e.Subresource == subresource {
              return true
          }
      }
      return false
  }

  func fieldManagerNames(entries []metav1.ManagedFieldsEntry) []string {
      out := make([]string, 0, len(entries))
      for _, e := range entries {
          out = append(out, e.Manager+"/"+e.Subresource)
      }
      return out
  }
  ```

  (`fakeProbe` must not shell out: it stats the fixture file only to fail the
  same way the real `mediainfo.Probe` would on a missing file, then returns a
  hand-built `*commonv1.MediaInfo` instead of calling ffprobe.
  `c.Status().Update` inside the test is the one place in this task a
  forbidigo-banned call is legitimate: it is simulating squasharr's worker
  writing TranscodeJob status directly against a hand-built fixture in a test,
  not production MediaFile code; golangci's forbidigo rule is scoped to
  `pkg/k8s` callers in production packages — confirm the exact scope in
  `.golangci.yml` before relying on this, and if the rule does fire on test
  files too, replace this line with `k8s.PatchStatus(ctx, c,
  k8s.ManagerSquasharrWorker, ...)` using the generated
  `transcodeac.TranscodeJob(...).WithStatus(...)` instead — same effect,
  still not this task's production code path.)

  Run: `KUBEBUILDER_ASSETS=$(setup-envtest use 1.37.0 -p path) go test
  ./catalogarr/controller/mediafile/... -run
  TestMediaFileFieldManagersStayDisjoint -v` — passes. Without
  `KUBEBUILDER_ASSETS` set, the same command SKIPs (not passes) — confirm the
  skip message names `KUBEBUILDER_ASSETS`, matching the mandatory-gate
  requirement that this is visibly a skip, never a silent green.

  Commit: `... -m "test(catalogarr): MediaFile two-writer field-manager gate" -- catalogarr/controller/mediafile/mediafile_envtest_test.go`

- [ ] **Step 10: fixture-driven full reconcile, Movie and Episode rollup, no ffprobe required.**

  Append `TestReconcileFixtureDrivenMovieRollup` and
  `TestReconcileFixtureDrivenEpisodeRollup` to the same file: each creates a
  namespace, a `QualityProfile` (reuse `mustQualityProfile`), a `Movie` (or a
  `Series` + `Episode` — `Series` needs only `TvdbID`, `QualityProfileRef`,
  `RootFolderRef`; `Episode` needs `SeriesRef`, `SeasonNumber`,
  `EpisodeNumber`), a `MediaFile` via `importarrCreatesMediaFile` with a
  Bluray-1080p quality (cutoff-met case) and a second with WEBDL-1080p
  (cutoff-unmet case), reconciles with `Probe: fakeProbe`, then asserts on
  the owner: `require.Eventually`-free since this calls `Reconcile` directly
  rather than starting a manager — a single direct call is enough because
  nothing here depends on the watch machinery, only on `rollupToOwner`.
  Assert `got.Status.HasFile`, `got.Status.FileQuality.Name`,
  `got.Status.CutoffMet` (true for Bluray-1080p, false for WEBDL-1080p) and
  `got.Status.Phase` (`MoviePhaseImported`/`MoviePhaseCutoffUnmet`,
  `EpisodePhaseImported`/`EpisodePhaseCutoffUnmet`) for both kinds. This is
  the test that exercises `computeRollup`/`moviePhaseForFile`/
  `episodePhaseForFile` end-to-end against a real apiserver rather than in
  isolation the way Step 4 already did in memory.

  Run: `KUBEBUILDER_ASSETS=... go test ./catalogarr/controller/mediafile/... -run TestReconcileFixtureDriven -v` — passes; unset `KUBEBUILDER_ASSETS`, same command SKIPs.

  Commit: `... -m "test(catalogarr): MediaFile fixture-driven rollup, no ffprobe" -- catalogarr/controller/mediafile/mediafile_envtest_test.go`

- [ ] **Step 11: real-ffprobe reconcile, skipped when the binary is absent.**

  Append `TestReconcileRealFFprobe`: copies
  `../../../testdata/mediainfo/sample_h264_8bit.mp4` (already in the tree from
  Phase B) into `t.TempDir()`, builds a `Reconciler` with `Probe:
  mediainfo.Probe` (the real default, not `fakeProbe`), skips via
  `skipIfNoFFprobe(t)` (copy the same three-line helper `pkg/mediainfo/
  probe_test.go` already uses — `exec.LookPath("ffprobe")`, `t.Skip` — into
  this package; do not import `pkg/mediainfo`'s unexported test helper, it is
  unexported and in a different package), runs one `Reconcile`, and asserts
  `got.Status.MediaInfo.VideoCodec == "h264"` and
  `got.Status.MediaInfo.Audio[0].ChannelLayout` is set to whatever the
  fixture's real ffprobe output reports (assert non-empty rather than a
  specific string, since Step 1 only proved the plumbing on a synthetic
  fixture, not this file's real layout — this test is the one place the two
  connect end to end).

  Run twice: with `ffprobe` on `PATH` and `KUBEBUILDER_ASSETS` set — passes;
  with either environment variable absent — SKIPs with a message naming which
  one.

  Commit: `... -m "test(catalogarr): MediaFile reconcile against real ffprobe" -- catalogarr/controller/mediafile/mediafile_envtest_test.go`

- [ ] **Step 12: TranscodeJob watch wiring, hand-created resource, full manager.**

  Append `TestTranscodeJobWatchTriggersReconcile`: starts a real
  `ctrl.Manager` against the envtest `cfg` (`ctrl.NewManager(cfg,
  ctrl.Options{Scheme: k8s.MustNewScheme(), Metrics: metricsserver.Options{
  BindAddress: "0"}})`), then, in this order: `r :=
  mediafile.NewReconciler(mgr.GetClient(), mgr.GetScheme(),
  record.NewFakeRecorder(32))`, `r.Probe = fakeProbe` (`NewReconciler`'s
  result is a `*Reconciler`, `Probe` is exported, safe to override before
  `mgr.Start` — `SetupWithManager` closes over `r` by pointer via
  `Complete(r)`, and `Reconcile` reads `r.Probe` at call time, so this order
  is what matters, not whether the swap happens before or after
  `SetupWithManager` — just before `mgr.Start`), `r.SetupWithManager(mgr)`,
  starts the manager in a goroutine with a cancellable context, creates the
  namespace/QualityProfile/Movie/MediaFile exactly as Step 9, waits (`require.
  Eventually`, 5s/100ms) for `got.Status.ProbeHash != ""` (the initial
  reconcile, proving the manager and `For()` watch work at all), then creates
  a `TranscodeJob` with `status.phase=Succeeded` the same way Step 9 did, and
  `require.Eventually`s for `got.Spec.Original` to flip false and
  `got.Status.Transcode.Compliant` to flip true — this time driven entirely by
  the `Watches(&TranscodeJob{}, ...)` wiring from Step 7, not a direct
  `Reconcile` call, proving the mapping function and predicate actually fire
  end to end.

  Run: `KUBEBUILDER_ASSETS=... go test ./catalogarr/controller/mediafile/... -run TestTranscodeJobWatchTriggersReconcile -v` — passes.

  Commit: `... -m "test(catalogarr): MediaFile TranscodeJob watch, hand-created resource" -- catalogarr/controller/mediafile/mediafile_envtest_test.go`

- [ ] **Step 13: SubtitleRequest watch wiring, hand-created resource.**

  Append `TestSubtitleRequestWatchTriggersReconcile`, same manager-driven
  shape as Step 12: after the initial MediaFile is probed, create a
  `SubtitleRequest{ObjectMeta: {Name: <mediafile-name>, Namespace: ns},
  Spec: {MediaFileRef: <mediafile-name>}}`, then patch its status with one
  `Items` entry (`LangKey: "en"`, `State:
  subtitlev1alpha1.SubtitleItemDownloaded`, `Path: "Inception (2010).en.srt"`)
  the same way Step 9 patches TranscodeJob status, and `require.Eventually`
  for `len(got.Status.Sidecars) == 1` with `got.Status.Sidecars[0].Language ==
  "en"`. This is the "status.items changed" predicate and the
  `mediaFileForSubtitleRequest` mapping proven end to end, and is the last
  piece §8.6 asks for: "this is the sidecar feedback path."

  Run: `KUBEBUILDER_ASSETS=... go test ./catalogarr/controller/mediafile/... -run TestSubtitleRequestWatchTriggersReconcile -v` — passes.

  Commit: `... -m "test(catalogarr): MediaFile SubtitleRequest watch, hand-created resource" -- catalogarr/controller/mediafile/mediafile_envtest_test.go`

**Verification:**
```
go build ./catalogarr/... ./api/...
go vet ./catalogarr/... ./pkg/mediainfo/...
go test ./catalogarr/controller/mediafile/... ./pkg/mediainfo/...             # envtest suites SKIP here, not pass
KUBEBUILDER_ASSETS=$(setup-envtest use 1.37.0 -p path) go test ./catalogarr/controller/mediafile/... -v   # must show 0 skips among the *_envtest_test.go tests except the deliberate ffprobe-absent one if ffprobe is not installed on this machine
make generate && make manifests && git status --porcelain              # must be empty except this task's own staged/committed files
make lint     # golangci-lint v2, including forbidigo on Status().Update/Status().Patch outside pkg/k8s and outside _test.go files per Step 9's note
```

**Done when:**
- [ ] `AudioStream.ChannelLayout` exists, is populated by `pkg/mediainfo`, and
      `pkg/mediainfo`'s own tests assert it; `pkg/naming` is untouched and a
      one-line note says why (Phase G's job).
- [ ] `make generate && make manifests` leaves a clean tree except the two
      named CRD YAMLs.
- [ ] `evaluateProbe`, `mirrorLabels`, `computeRollup`, `moviePhaseForFile`,
      `episodePhaseForFile`, `sidecarsFromSubtitleRequest` are pure, unit
      tested without a cluster, and are what `Reconcile` calls (not
      duplicated logic).
- [ ] `TestMediaFileFieldManagersStayDisjoint` passes under `make test` and
      proves, by reading `managedFields` directly: importarr's simulated
      write owns every `MediaFileSpec` field except `sizeBytes`/`modTime`/
      `original`; catalogarr owns 100% of `MediaFileStatus` and claims no
      main-resource field until a transcode swap is incorporated, at which
      point it owns exactly `sizeBytes`/`modTime`/`original` and nothing
      importarr still holds is lost.
      `quality`/`revision`/`formatScore`/`matchedFormats`/`releaseType` are
      byte-identical before and after the simulated transcode.
- [ ] The real-ffprobe test (Step 11) `t.Skip`s cleanly with `ffprobe`
      absent from `PATH`, and a separate fixture-driven test (Steps 9–10, 12–13)
      exercises the same reconcile logic without it.
- [ ] `TestTranscodeJobWatchTriggersReconcile` and
      `TestSubtitleRequestWatchTriggersReconcile` drive a real manager against
      hand-created `TranscodeJob`/`SubtitleRequest` fixtures and observe the
      watch (not a direct `Reconcile` call) produce the effect.
- [ ] `catalogarr/run.go` is untouched; `Reconciler.SetupWithManager`'s exact
      call shape is documented in "Interfaces — Produces" for whoever wires it.
- [ ] Every new file carries the GPL-3.0 header from `hack/boilerplate.go.txt`.

---

## Wave 2 — consume wave 1's real code (parallel, after every wave-1 task passed review)

### Task C8: the Search controller and the search worker

**Files:**
- Create: `catalogarr/controller/search/applyconfiguration.go`
- Create: `catalogarr/controller/search/applyconfiguration_envtest_test.go`
- Create: `catalogarr/controller/search/grab.go`
- Create: `catalogarr/controller/search/grab_test.go`
- Create: `catalogarr/controller/search/reconciler.go`
- Create: `catalogarr/controller/search/reconciler_envtest_test.go`
- Create: `catalogarr/worker/search/rpc.go`
- Create: `catalogarr/worker/search/rpc_test.go`
- Create: `catalogarr/worker/search/request.go`
- Create: `catalogarr/worker/search/request_test.go`
- Create: `catalogarr/worker/search/rank.go`
- Create: `catalogarr/worker/search/rank_test.go`
- Create: `catalogarr/worker/search/snapshot.go`
- Create: `catalogarr/worker/search/blocklist.go`
- Create: `catalogarr/worker/search/worker.go`
- Create: `catalogarr/worker/search/worker_envtest_test.go`
- Test: (all `_test.go`/`_envtest_test.go` files above)

**Path ownership:** `catalogarr/controller/search/` and `catalogarr/worker/search/`, and nothing else. In particular this task does **not** modify `catalogarr/run.go` — `setupControllers`/`setupWorkers` are shared registration points every Phase C controller/worker task touches, so wiring `search.Reconciler` and `search.Worker` into them is left to a later integration pass outside any single task's path ownership. This task instead exposes `search.NewReconciler(...).SetupWithManager(mgr)` and `search.NewWorker(...).SetupWithManager(mgr, bus)` as the two calls that integration step will make.

## Scope note: which trigger this task wires

Spec §8.2 lists six triggers for a search: `searchOnAdd`, an `Available` transition, the
`catalog.clustarr.io/search=now` annotation, the wanted cron, a redownload, and a `Search` CR.
This task wires exactly **one** of them end to end — the `Search` CR — plus the worker that
every trigger's task ultimately lands on:

- `searchOnAdd`, the `Available` transition, the annotation and the redownload trigger are all
  published by the Movie/Series/Episode reconcilers watching their own object's spec/status —
  **Task C6**, not here. This task does not touch Movie, Series or Episode.
- The wanted cron (`clustarr.work.catalogarr.wantedscan.low.<namespace>`, `schema.WantedScan`)
  is **Task C9**. This task does not schedule anything.
- The `Search` CR's own reconciler — validate, publish, TTL-delete, handle `spec.grab` — is
  **this task**.
- The **worker** (`catalogarr/worker/search`) is shared infrastructure: it consumes every
  `catalog.SearchTask.v1` on `catalogarr-search-high`/`catalogarr-search-normal` regardless of
  which task published it, so it is built once, here, for all six triggers. What differs by
  trigger is the *sink* for its ranked output (see "Interfaces — Produces" below): for an
  interactive search (`SearchTask.SearchRef` set) the worker itself writes `Search.status`
  (this task's job — only the worker holds the ranked list). For every other reason
  (`add`, `missing`, `cutoffUnmet`, `redownload`) the worker hands the ranked list to an
  injected `Sink`, because turning "best approved release" into a grab is spec §8.2's
  **second half** — DelayProfile resolution, the `clustarr-pending` CAS, the KV grab lease,
  and the grab worker itself — which is explicitly **out of scope** for this task (the task
  brief scopes this task to "§8.2's first half"). This task ships a `NopSink` so the worker
  compiles, subscribes and is fully tested today; a later task supplies the real one.

## A compile-time dependency this task cannot avoid: `pkg/decision`

Unlike the indexer search RPC (below), `pkg/decision` is a plain Go library, not a
cross-service boundary, so the worker imports it directly rather than through a narrow local
interface — that is the normal shape for a Phase B/C-style pure-function dependency. But
`pkg/decision` is Task C2, running in parallel with this one, so it may not exist yet in the
tree this task executes against.

**If `pkg/decision` is not present when you start this task**, create a minimal placeholder at
`pkg/decision/decision.go` with exactly the shape in "Interfaces — Consumes" below, a package
comment `// Package decision is a placeholder for Task C2; delete this file once C2 lands.`,
and touch no other file under `pkg/decision/` — do not invent evaluation rules, the
placeholder's `Evaluate` only needs to compile and return a zero `Decision`. Task C2 will
overwrite it. Every call site in this task goes through the single seam
`Worker.Evaluate func(context.Context, decision.Input) decision.Decision` (defaulted to
`decision.Evaluate` in `NewWorker`, overridable in tests), so if C2's real signature ends up
different, the fix is a one-line change to that field's default and the `Input`/`Decision`
literals in `worker.go` — nothing else in this task depends on `pkg/decision`'s internals.

**Read first:**
- Spec `docs/superpowers/specs/2026-09-18-clustarr-design.md`:
  - §8.2 in full (search → decide → delay → grab) — the paragraph beginning "Search → decide →
    delay → grab" and the "Grab (worker or scheduled consumer)"/"Search-CR grabs" sentences at
    its end.
  - §4.2's `Search` block (`SearchSpec`/`SearchStatus`) and the `Movie`/`Episode` status blocks
    (`PendingGrab`, `ActiveDownloadRef`, `LastSearchedAt`, `SearchAttempts`, `FileQuality`,
    `FileFormatScore`) — read-only inputs for this task, owned and written by Task C6.
  - §5's tables: the `clustarr.rpc.indexarr.search` row (payload schema, "single reply at
    min(deadline, 45s)"), the `catalogarr-search-high`/`catalogarr-search-normal` consumer rows
    (AckWait 120s, MaxDeliver 5, BackOff), and the `clustarr-leases`/`clustarr-pending` KV rows
    (context only — this task does not touch either bucket).
  - §8.8 (failure handling) — `events.Retry`/`events.Discard`/backoff-nak mapping for the
    worker; `reconcile.TerminalError`/`RequeueAfter` for the controller.
  - §9's `pkg/decision` paragraph (`UpgradableSpecification` rule list) — what `Evaluate` is
    assumed to port.
- Amendment `docs/superpowers/specs/2026-09-18-clustarr-design-amendment-1.md`: skim for
  "search" (nine hits, all incidental to the UI/observability pipeline projection, not to this
  task's behaviour) — nothing here overrides §8.2.
- `docs/research/queue.md` §5.4 (subject grammar), §6.1 (Go interface sketch, for the shape
  `pkg/events` actually took) — the concrete package already diverges from this sketch in
  places; the real `pkg/events` (below) wins.
- `docs/research/indexers.md` §5 ("Aggregated search in Prowlarr and what Clustarr should do
  instead") for the fan-out/dedup/outcome-reporting rationale, and §"pkg/indexer/search.go"
  sketch — **superseded** by the real `pkg/events/schema` types (below), which already exist
  and differ from the sketch (no `Offset`, no free `Genre`/`Artist` fields, etc.); use the real
  types, not the sketch.
- `docs/research/quality.md` §6.1 (`UpgradableSpecification.IsUpgradable`, ported by
  `quality.Profile.UpgradeDecision` and assumed by `pkg/decision.Evaluate`) and §6.2
  (`DownloadDecisionComparer` — the source for this task's `RankAndCap` comparator).
- Generated types, read in full: `api/catalog/v1alpha1/search_types.go` (note its doc comment:
  apply-configuration generation is **disabled** for `Search`/`SearchList` — see Step 1),
  `api/catalog/v1alpha1/movie_types.go` (`MovieSpec`/`MovieStatus`), `episode_types.go`
  (`EpisodeSpec`/`EpisodeStatus`), `mediafile_types.go` (`MediaFileSpec`), `qualityprofile_types.go`.
  `api/common/v1alpha1/release_types.go` (`ReleaseInfo`), `download_types.go` (`Rejection`,
  `RejectionType`, `Attempts`), `media_types.go` (`MediaRef`). `api/download/v1alpha1/download_types.go`
  (`DownloadSpec`, `DownloadSource`, `IndexerDownload`, `GrabSource`).
- `pkg/events/schema/index.go` and `pkg/events/schema/catalog.go` in full — `SearchTask`,
  `SearchRequest`, `SearchResponse`, `Release`, `SearchOutcome`, `SearchReason` are **already
  implemented** (Phase A). Do not redefine them; import and use them as-is.
- `go doc ./pkg/events`, `go doc ./pkg/k8s`, `go doc ./pkg/quality`, `go doc ./pkg/quality/catalogue`,
  `go doc ./pkg/release`, `go doc ./pkg/newznab` — run these; the exact signatures below were
  read from their output, not guessed.

**Dependencies:** none new. Every package this task imports is already in `go.mod`:
`sigs.k8s.io/controller-runtime` v0.25.1, `k8s.io/api`/`k8s.io/apimachinery` v0.37.0,
`github.com/stretchr/testify` v1.12.1, `github.com/jonboulle/clockwork` v0.5.0 (membus' clock),
plus the in-repo `pkg/events`, `pkg/events/schema`, `pkg/events/membus`, `pkg/k8s`,
`pkg/quality`, `pkg/quality/catalogue`, `pkg/release`, `pkg/newznab`, `api/catalog/v1alpha1`,
`api/common/v1alpha1`, `api/download/v1alpha1`, `api/applyconfiguration/download/download/v1alpha1`.
Do not run `go get`/`go mod tidy`.

## Interfaces — Consumes

Exact signatures read from `go doc` and the generated types (verified 2026-09-18):

```go
// pkg/k8s
func PatchStatus[T ApplyConfiguration](ctx context.Context, c client.Client, fm FieldManager, ac T, opts ...client.SubResourceApplyOption) (T, error)
func Apply[T ApplyConfiguration](ctx context.Context, c client.Client, fm FieldManager, ac T, opts ...client.ApplyOption) (T, error)
type ApplyConfiguration interface {
    runtime.ApplyConfiguration // marker method IsApplyConfiguration()
    GetName() *string; GetNamespace() *string; GetKind() *string; GetAPIVersion() *string
}
const ManagerCatalogarr FieldManager = "catalogarr"
func ConditionACs(conditions []metav1.Condition) []*metav1ac.ConditionApplyConfiguration
func MarkTrue(obj client.Object, conditions *[]metav1.Condition, condType, reason, message string, args ...any) bool
func MarkFalse(obj client.Object, conditions *[]metav1.Condition, condType, reason, message string, args ...any) bool
func ChildName(target string, parts ...string) string // "<target>-<sha1(parts joined by |)[:10]>"

// pkg/events
const RPCIndexSearch = "clustarr.rpc.indexarr.search"
type Requester interface {
    Request(ctx context.Context, subject string, in, out any) error
    Serve(subject, queue string, h func(ctx context.Context, data []byte) ([]byte, error)) error
}
var ErrNoResponders = errors.New("events: no responders")
func Retry(after time.Duration, err error) error
func Discard(reason string, err error) error
func WorkSearchSubject(p Priority, mediaKey string) string // clustarr.work.catalogarr.search.<priority>.<mediaKey>
const PriorityHigh Priority = "high"; PriorityNormal Priority = "normal"; PriorityLow Priority = "low"
func MsgIDForObject(uid string, generation int64, task string) string // "<uid>:<generation>:<task>"
type Envelope struct { ID, Type, Schema, Source, Key string; Time time.Time; Trace string; Headers map[string]string; Data []byte }
type Message interface { WorkQueue; Envelope() *Envelope; Subject() string; Attempt() uint64 }
func (t Topology) Consumer(name string) (ConsumerSpec, bool)
func Default() Topology
const ConsumerCatalogSearchHigh = "catalogarr-search-high" // exact name verified in pkg/events/topology.go
const ConsumerCatalogSearchNorm = "catalogarr-search-normal"

// pkg/events/schema (already implemented — Phase A)
type SearchReason string
const SearchReasonAdd, SearchReasonMissing, SearchReasonCutoffUnmet, SearchReasonInteractive, SearchReasonRedownload SearchReason
type SearchTask struct { MediaRef commonv1.MediaRef; Keys []string; Reason SearchReason; SearchRef *Ref; UserInvoked bool }
func (SearchTask) Schema() string // "catalog.SearchTask.v1"
type SearchRequest struct { Kind commonv1.MediaKind; Text string; IDs map[string]string; Season, Episode *int32; Year int32; Categories []int32; IndexerRefs []Ref; Protocols []commonv1.Protocol; Limit int32; DeadlineMillis int64; UserInvoked bool }
type SearchResponse struct { Releases []Release; Outcomes []SearchOutcome; Truncated bool }
type Release struct { Info commonv1.ReleaseInfo; ParsedTitle string; Year int32; Seasons, Episodes, Absolute []int32; AirDate *time.Time; FullSeason, MultiSeason, Special bool; Kind commonv1.MediaKind; Hints map[string][]string; FetchedAt time.Time }
type SearchOutcome struct { IndexerRef Ref; IndexerName string; Status SearchOutcomeStatus; Releases int32; ElapsedMillis int64; Error string }
const MaxSearchReleases = 500
func Encode(p Payload) (schema string, data []byte, err error)

// pkg/quality
func FromCRD(p *catalogv1alpha1.QualityProfile, cat *catalogue.Catalogue) (Profile, []error)
type Profile struct { Tiers [][]Definition; CutoffIndex int; UpgradeAllowed bool; MinFormatScore, CutoffFormatScore, MinUpgradeFormatScore int; Scores map[string]int; Language, LanguageName, ProperPolicy string; Sizes map[string]SizeLimit; PreferredProtocol string; Hash string }
func (p Profile) Score(ctx context.Context, cat *catalogue.Catalogue, r *release.ParsedRelease, ic catalogue.ItemContext) (score int, matched []string)
func (p Profile) Index(q common.Quality) (idx int, ok bool)
func (p Profile) UpgradeDecision(current, candidate Candidate) Verdict
type Candidate struct { Quality common.Quality; Revision common.Revision; FormatScore int }

// pkg/quality/catalogue
func LoadedCatalogue() *Catalogue
type ItemContext struct { OriginalLanguage string; IndexerFlags []string; ReleaseType common.ReleaseType }

// pkg/release
func Parse(title string, o Options) (*ParsedRelease, error)
type Options struct { Kind commonv1.MediaKind; SeriesType string }
type ParsedRelease struct { Title string; Quality commonv1.Quality; Revision commonv1.Revision; Languages []string; Group string; Seasons, Episodes, Absolute []int; ReleaseType commonv1.ReleaseType; Hints Hints /* ... */ }
func (p *ParsedRelease) ApplyTo(ri *commonv1.ReleaseInfo)

// pkg/newznab
func ByKind(kind commonv1.MediaKind) []CategoryID // movie->2000, series/episode->5000, ...

// pkg/decision (Task C2, ASSUMED — reconciled by controller)
package decision
type Input struct {
    Release               commonv1.ReleaseInfo  // fully populated: Quality/Revision/FormatScore/MatchedFormats/Languages/ReleaseType already set by the worker via release.Parse + profile.Score before Evaluate is called
    Profile               quality.Profile
    Current               *quality.Candidate    // nil when the item has no file yet
    RuntimeMinutes        int32                 // item runtime, or summed pack runtime for a season/discography pack
    Available             bool                  // item's availability (movie: status.available; episode: aired)
    UserInvoked            bool                  // Search CR / interactive: bypasses the availability check
    AlreadyImported        bool                  // this exact release is already the current MediaFile, by hash or by normalized title
    Blocklisted             bool                  // this release's infoHash or normalized title is on the live blocklist
    QueueHasHigherOrEqual   bool                  // an active Download for this media key is already at >= this release's quality/score
}
type Decision struct {
    Approved             bool
    TemporarilyRejected  bool
    Rejections           []commonv1.Rejection
    Quality              commonv1.Quality
    Revision              commonv1.Revision
    FormatScore           int32
}
func Evaluate(ctx context.Context, in Input) Decision
```

## Interfaces — Produces

```go
// catalogarr/worker/search — the indexer search RPC boundary. indexarr (Phase D) MUST serve
// clustarr.rpc.indexarr.search with exactly the schema.SearchRequest -> schema.SearchResponse
// contract already pinned in pkg/events/schema/index.go (this task adds no new payload type,
// only a thin client and a fake for tests):
type SearchRPC interface {
    Search(ctx context.Context, req schema.SearchRequest) (schema.SearchResponse, error)
}
func NewBusSearchRPC(r events.Requester) SearchRPC // wraps bus.Request(ctx, events.RPCIndexSearch, req, &resp); events.ErrNoResponders -> events.Retry(15*time.Second, err)
type FakeSearchRPC struct { Response schema.SearchResponse; Err error; Requests []schema.SearchRequest } // records every call; exported for C9's and the grab task's tests too

func BuildSearchRequest(kind commonv1.MediaKind, ids TargetIDs, limit int32, userInvoked bool, indexerRefs []schema.Ref, categories []int32) schema.SearchRequest
type TargetIDs struct { TmdbID int64; ImdbID string; TvdbID int64; Season, Episode *int32; Anime bool; Year int32; OriginalLanguage string }

type Scored struct { Decision catalogv1alpha1.ReleaseDecision; TierIndex int }
func RankAndCap(items []Scored, limit int, preferredProtocol commonv1.Protocol) []catalogv1alpha1.ReleaseDecision
const MaxResults = 200

// Sink is what the worker hands ranked, non-interactive results to. The delay-profile/grab
// task (out of scope here) supplies the real implementation; this task ships NopSink.
type Sink interface {
    Deliver(ctx context.Context, task schema.SearchTask, ranked []catalogv1alpha1.ReleaseDecision) error
}
type NopSink struct{ Log *slog.Logger }

type Worker struct {
    Client    client.Client
    RPC       SearchRPC
    Catalogue *catalogue.Catalogue
    Evaluate  func(context.Context, decision.Input) decision.Decision // defaults to decision.Evaluate
    Sink      Sink                                                    // defaults to NopSink
    Clock     clockwork.Clock
}
func NewWorker(c client.Client, rpc SearchRPC, cat *catalogue.Catalogue) *Worker
func (w *Worker) Handle(ctx context.Context, m events.Message) error
func (w *Worker) SetupWithManager(mgr ctrl.Manager, bus events.Bus) error // registers the 3 Download field indexes (blocklist.infoHash, blocklist.title, target) and subscribes catalogarr-search-high/-normal

// catalogarr/controller/search
type Reconciler struct { Client client.Client; Bus events.Bus; Recorder record.EventRecorder; Clock clockwork.Clock }
func NewReconciler(c client.Client, bus events.Bus, rec record.EventRecorder) *Reconciler
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error)
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error

func BuildDownloadSource(rel commonv1.ReleaseInfo) downloadv1alpha1.DownloadSource // exported: the automatic-grab task needs the identical mapping

// SearchApplyConfiguration satisfies k8s.ApplyConfiguration by hand, since Search/SearchList
// have +kubebuilder:ac:generate=false (search_types.go's doc comment explains why:
// controller-tools v0.22.0 panics on ReleaseDecision's inlined common.ReleaseInfo). Any later
// task that needs to patch Search.status can reuse this type; do not wait for codegen to grow
// one.
func Search(name, namespace string) *SearchApplyConfiguration
func SearchStatus() *SearchStatusApplyConfiguration
// ... With* builders, GetName/GetNamespace/GetKind/GetAPIVersion/IsApplyConfiguration — see Step 1.
```

- [ ] **Step 1: hand-write `SearchApplyConfiguration` and prove it round-trips through `k8s.PatchStatus` against a real apiserver.**

  `Search`/`SearchList` have `+kubebuilder:ac:generate=false` (see the doc comment on `Search`
  in `api/catalog/v1alpha1/search_types.go`), so there is no generated
  `catalogac.SearchApplyConfiguration`. Every other status write in this task depends on one
  existing, so build it first, by hand, mirroring the shape controller-gen produces for
  `Download` (`api/applyconfiguration/download/download/v1alpha1/download.go` — read it before
  writing this) but trimmed to only the status fields this task needs. Nested value types
  (`IndexerOutcome`, `ReleaseDecision`, `GrabResult`) are embedded as plain API-package values,
  not as further apply-configuration wrappers, exactly like `DownloadSpecApplyConfiguration`
  embeds `*commonv1alpha1.ReleaseInfo` directly — because this task always writes the *entire*
  list in one patch (never a partial update to one element), a plain value slice is correct and
  simpler than reproducing controller-tools' generator for three more types.

  Test first (`catalogarr/controller/search/applyconfiguration_envtest_test.go`, modelled
  exactly on `pkg/k8s/patch_envtest_test.go`'s `TestPatchStatusAppliesToTheStatusSubresource` —
  read that file first):

  ```go
  package search_test

  import (
      "context"
      "os"
      "testing"

      corev1 "k8s.io/api/core/v1"
      metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
      "sigs.k8s.io/controller-runtime/pkg/client"
      "sigs.k8s.io/controller-runtime/pkg/envtest"

      catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
      commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
      "github.com/mediactl/clustarr/catalogarr/controller/search"
      "github.com/mediactl/clustarr/pkg/k8s"
  )

  func newTestClient(t *testing.T) client.Client {
      t.Helper()
      if os.Getenv("KUBEBUILDER_ASSETS") == "" {
          t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
      }
      env := &envtest.Environment{CRDDirectoryPaths: []string{"../../../config/crd/bases"}, ErrorIfCRDPathMissing: true}
      cfg, err := env.Start()
      if err != nil {
          t.Fatalf("start envtest: %v", err)
      }
      t.Cleanup(func() { _ = env.Stop() })
      c, err := client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
      if err != nil {
          t.Fatalf("build client: %v", err)
      }
      return c
  }

  func TestSearchApplyConfigurationRoundTripsThroughPatchStatus(t *testing.T) {
      ctx := context.Background()
      c := newTestClient(t)
      const ns, name = "search-ac", "srch-1"

      if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}); err != nil {
          t.Fatalf("create namespace: %v", err)
      }
      s := &catalogv1alpha1.Search{
          ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
          Spec:       catalogv1alpha1.SearchSpec{MediaRef: &commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "the-matrix"}},
      }
      if err := c.Create(ctx, s); err != nil {
          t.Fatalf("create Search: %v", err)
      }

      seeders := int32(42)
      ac := search.Search(name, ns).WithStatus(
          search.SearchStatus().
              WithPhase(catalogv1alpha1.SearchPhaseCompleted).
              WithObservedGeneration(1).
              WithResults(catalogv1alpha1.ReleaseDecision{
                  ReleaseInfo: commonv1.ReleaseInfo{GUID: "g1", Title: "The.Matrix.1999.1080p.BluRay.x264-GROUP", Seeders: &seeders},
                  Approved:    true,
                  Rank:        1,
              }),
      )
      applied, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarr, ac)
      if err != nil {
          t.Fatalf("PatchStatus: %v", err)
      }
      if applied.Status == nil || applied.Status.Phase == nil || *applied.Status.Phase != catalogv1alpha1.SearchPhaseCompleted {
          t.Fatalf("round-tripped ac = %+v", applied)
      }

      got := &catalogv1alpha1.Search{}
      if err := c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, got); err != nil {
          t.Fatalf("get: %v", err)
      }
      if got.Status.Phase != catalogv1alpha1.SearchPhaseCompleted {
          t.Errorf("status.phase = %q", got.Status.Phase)
      }
      if len(got.Status.Results) != 1 || got.Status.Results[0].GUID != "g1" || got.Status.Results[0].Rank != 1 {
          t.Errorf("status.results = %+v", got.Status.Results)
      }
      if got.Spec.MediaRef == nil || got.Spec.MediaRef.Name != "the-matrix" {
          t.Errorf("spec was touched: %+v", got.Spec)
      }
  }
  ```

  Run it — it fails to compile (`search.Search`/`search.SearchStatus` do not exist yet):
  `KUBEBUILDER_ASSETS=$(go env GOPATH)/bin/../../.local/share/kubebuilder-envtest/k8s/1.37.0-linux-amd64 go test ./catalogarr/controller/search/... -run TestSearchApplyConfigurationRoundTripsThroughPatchStatus` (or just `make test` once the Makefile's envtest setup runs — either way this needs `KUBEBUILDER_ASSETS`; without it the test **skips**, which is not a pass — do not conclude success from a skip).

  Implement `catalogarr/controller/search/applyconfiguration.go`:

  ```go
  package search

  import (
      metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
      metav1ac "k8s.io/client-go/applyconfigurations/meta/v1"

      catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
  )

  // SearchApplyConfiguration is a hand-written stand-in for the generated
  // apply configuration controller-tools cannot produce for Search (see the
  // doc comment on catalogv1alpha1.Search). It carries only what this task's
  // controller and worker ever patch: name/namespace/kind/apiVersion and
  // status. Nested lists (Results, IndexerOutcomes, Grabbed) are plain
  // catalogv1alpha1 values because every write replaces the whole list.
  type SearchApplyConfiguration struct {
      metav1ac.TypeMetaApplyConfiguration    `json:""`
      *metav1ac.ObjectMetaApplyConfiguration `json:"metadata,omitempty"`
      Status                                 *SearchStatusApplyConfiguration `json:"status,omitempty"`
  }

  type SearchStatusApplyConfiguration struct {
      ObservedGeneration *int64                                    `json:"observedGeneration,omitempty"`
      Conditions         []*metav1ac.ConditionApplyConfiguration    `json:"conditions,omitempty"`
      Phase              *catalogv1alpha1.SearchPhase                `json:"phase,omitempty"`
      StartedAt          *metav1.Time                                `json:"startedAt,omitempty"`
      FinishedAt         *metav1.Time                                `json:"finishedAt,omitempty"`
      IndexerOutcomes    []catalogv1alpha1.IndexerOutcome            `json:"indexerOutcomes,omitempty"`
      Results            []catalogv1alpha1.ReleaseDecision           `json:"results,omitempty"`
      Grabbed            []catalogv1alpha1.GrabResult                 `json:"grabbed,omitempty"`
  }

  func Search(name, namespace string) *SearchApplyConfiguration {
      b := &SearchApplyConfiguration{}
      b.WithName(name)
      b.WithNamespace(namespace)
      b.WithKind("Search")
      b.WithAPIVersion("catalog.clustarr.io/v1alpha1")
      return b
  }

  func SearchStatus() *SearchStatusApplyConfiguration { return &SearchStatusApplyConfiguration{} }

  func (b SearchApplyConfiguration) IsApplyConfiguration() {}

  func (b *SearchApplyConfiguration) ensureObjectMeta() {
      if b.ObjectMetaApplyConfiguration == nil {
          b.ObjectMetaApplyConfiguration = &metav1ac.ObjectMetaApplyConfiguration{}
      }
  }
  func (b *SearchApplyConfiguration) WithName(v string) *SearchApplyConfiguration {
      b.ensureObjectMeta(); b.Name = &v; return b
  }
  func (b *SearchApplyConfiguration) WithNamespace(v string) *SearchApplyConfiguration {
      b.ensureObjectMeta(); b.Namespace = &v; return b
  }
  func (b *SearchApplyConfiguration) WithKind(v string) *SearchApplyConfiguration {
      b.TypeMetaApplyConfiguration.Kind = &v; return b
  }
  func (b *SearchApplyConfiguration) WithAPIVersion(v string) *SearchApplyConfiguration {
      b.TypeMetaApplyConfiguration.APIVersion = &v; return b
  }
  func (b *SearchApplyConfiguration) WithStatus(v *SearchStatusApplyConfiguration) *SearchApplyConfiguration {
      b.Status = v; return b
  }
  func (b *SearchApplyConfiguration) GetName() *string      { b.ensureObjectMeta(); return b.Name }
  func (b *SearchApplyConfiguration) GetNamespace() *string { b.ensureObjectMeta(); return b.Namespace }
  func (b *SearchApplyConfiguration) GetKind() *string      { return b.TypeMetaApplyConfiguration.Kind }
  func (b *SearchApplyConfiguration) GetAPIVersion() *string { return b.TypeMetaApplyConfiguration.APIVersion }

  func (b *SearchStatusApplyConfiguration) WithObservedGeneration(v int64) *SearchStatusApplyConfiguration {
      b.ObservedGeneration = &v; return b
  }
  func (b *SearchStatusApplyConfiguration) WithConditions(v ...*metav1ac.ConditionApplyConfiguration) *SearchStatusApplyConfiguration {
      b.Conditions = append(b.Conditions, v...); return b
  }
  func (b *SearchStatusApplyConfiguration) WithPhase(v catalogv1alpha1.SearchPhase) *SearchStatusApplyConfiguration {
      b.Phase = &v; return b
  }
  func (b *SearchStatusApplyConfiguration) WithStartedAt(v metav1.Time) *SearchStatusApplyConfiguration {
      b.StartedAt = &v; return b
  }
  func (b *SearchStatusApplyConfiguration) WithFinishedAt(v metav1.Time) *SearchStatusApplyConfiguration {
      b.FinishedAt = &v; return b
  }
  func (b *SearchStatusApplyConfiguration) WithIndexerOutcomes(v ...catalogv1alpha1.IndexerOutcome) *SearchStatusApplyConfiguration {
      b.IndexerOutcomes = append(b.IndexerOutcomes, v...); return b
  }
  func (b *SearchStatusApplyConfiguration) WithResults(v ...catalogv1alpha1.ReleaseDecision) *SearchStatusApplyConfiguration {
      b.Results = append(b.Results, v...); return b
  }
  func (b *SearchStatusApplyConfiguration) WithGrabbed(v ...catalogv1alpha1.GrabResult) *SearchStatusApplyConfiguration {
      b.Grabbed = append(b.Grabbed, v...); return b
  }
  ```

  Run the test again — it must pass under `make test` (envtest assets set). Commit:
  `git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "feat(catalogarr): hand-written Search apply configuration" -- catalogarr/controller/search/applyconfiguration.go catalogarr/controller/search/applyconfiguration_envtest_test.go`.

- [ ] **Step 2: `RankAndCap` — the pure ranking/capping function, table-tested.**

  Test first (`catalogarr/worker/search/rank_test.go`):

  ```go
  package search

  import (
      "fmt"
      "testing"

      catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
      commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
  )

  func seedersPtr(n int32) *int32 { return &n }

  func TestRankAndCapOrdersApprovedByTierThenFormatScoreThenSeeders(t *testing.T) {
      items := []Scored{
          {TierIndex: 0, Decision: catalogv1alpha1.ReleaseDecision{
              ReleaseInfo: commonv1.ReleaseInfo{GUID: "low-seeders", Protocol: commonv1.ProtocolTorrent, Seeders: seedersPtr(5), FormatScore: 100},
              Approved:    true,
          }},
          {TierIndex: 0, Decision: catalogv1alpha1.ReleaseDecision{
              ReleaseInfo: commonv1.ReleaseInfo{GUID: "high-seeders", Protocol: commonv1.ProtocolTorrent, Seeders: seedersPtr(50), FormatScore: 100},
              Approved:    true,
          }},
          {TierIndex: 1, Decision: catalogv1alpha1.ReleaseDecision{
              ReleaseInfo: commonv1.ReleaseInfo{GUID: "worse-tier"},
              Approved:    true,
          }},
          {Decision: catalogv1alpha1.ReleaseDecision{
              ReleaseInfo: commonv1.ReleaseInfo{GUID: "rejected"},
              Approved:    false,
              Rejections:  []commonv1.Rejection{{Reason: "quality below cutoff", Type: commonv1.RejectionTemporary}},
          }},
      }

      got := RankAndCap(items, 100, commonv1.ProtocolTorrent)

      want := []string{"high-seeders", "low-seeders", "worse-tier", "rejected"}
      if len(got) != len(want) {
          t.Fatalf("len = %d, want %d", len(got), len(want))
      }
      for i, guid := range want {
          if got[i].GUID != guid {
              t.Errorf("position %d = %q, want %q", i, got[i].GUID, guid)
          }
          if got[i].Rank != int32(i+1) {
              t.Errorf("position %d Rank = %d, want %d", i, got[i].Rank, i+1)
          }
      }
  }

  func TestRankAndCapTruncatesToLimit(t *testing.T) {
      items := make([]Scored, 5)
      for i := range items {
          items[i] = Scored{Decision: catalogv1alpha1.ReleaseDecision{
              ReleaseInfo: commonv1.ReleaseInfo{GUID: fmt.Sprintf("r%d", i)}, Approved: true,
          }}
      }
      if got := RankAndCap(items, 2, ""); len(got) != 2 {
          t.Fatalf("len = %d, want 2", len(got))
      }
  }

  func TestRankAndCapClampsLimitToMaxResults(t *testing.T) {
      items := make([]Scored, 3)
      for i := range items {
          items[i] = Scored{Decision: catalogv1alpha1.ReleaseDecision{
              ReleaseInfo: commonv1.ReleaseInfo{GUID: fmt.Sprintf("r%d", i)}, Approved: true,
          }}
      }
      if got := RankAndCap(items, 10_000, ""); len(got) != 3 {
          t.Fatalf("len = %d, want 3 (limit clamps to available count, not to 10000)", len(got))
      }
      if got := RankAndCap(items, 0, ""); len(got) != 3 {
          t.Fatalf("limit=0 should default to MaxResults, got len = %d", len(got))
      }
  }
  ```

  Run: `go test ./catalogarr/worker/search/... -run TestRankAndCap` — fails to compile
  (`RankAndCap`/`Scored`/`MaxResults` undefined).

  Implement `catalogarr/worker/search/rank.go`:

  ```go
  package search

  import (
      "sort"

      catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
      commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
  )

  // MaxResults is the hard cap on Search.status.results
  // (+kubebuilder:validation:MaxItems=200, api/catalog/v1alpha1/search_types.go).
  const MaxResults = 200

  // Scored pairs one evaluated release with the profile-tier index its verdict
  // was computed against. TierIndex is a runtime-only ranking input
  // (quality.Profile.Index result); it is not persisted on
  // catalogv1alpha1.ReleaseDecision, so it travels alongside the decision
  // rather than on it.
  type Scored struct {
      Decision  catalogv1alpha1.ReleaseDecision
      TierIndex int
  }

  // RankAndCap orders items best first and truncates to at most limit entries
  // (limit <= 0 or > MaxResults is treated as MaxResults), assigning Rank as
  // the 1-based position in the final list.
  //
  // Approved releases sort ahead of every rejected one. Among approved
  // releases the order is docs/research/quality.md §6.2's
  // DownloadDecisionComparer, restricted to what this task has available:
  // quality tier -> revision (Real, then Version, higher first) -> custom-
  // format score (higher first) -> protocol match with preferredProtocol ->
  // seeders (torrent) or PublishedAt (usenet) -> size (larger first).
  // Indexer priority, indexer flags and Sonarr's prefer-packs-by-episode-
  // count rule are omitted: none is available without indexarr's Indexer
  // objects or a DelayProfile, both out of this task's scope. Rejected
  // releases keep their arrival order (stable sort).
  func RankAndCap(items []Scored, limit int, preferredProtocol commonv1.Protocol) []catalogv1alpha1.ReleaseDecision {
      ranked := make([]Scored, len(items))
      copy(ranked, items)

      sort.SliceStable(ranked, func(i, j int) bool {
          a, b := ranked[i], ranked[j]
          if a.Decision.Approved != b.Decision.Approved {
              return a.Decision.Approved
          }
          if !a.Decision.Approved {
              return false
          }
          if a.TierIndex != b.TierIndex {
              return a.TierIndex < b.TierIndex
          }
          if a.Decision.Revision.Real != b.Decision.Revision.Real {
              return a.Decision.Revision.Real > b.Decision.Revision.Real
          }
          if a.Decision.Revision.Version != b.Decision.Revision.Version {
              return a.Decision.Revision.Version > b.Decision.Revision.Version
          }
          if a.Decision.FormatScore != b.Decision.FormatScore {
              return a.Decision.FormatScore > b.Decision.FormatScore
          }
          if preferredProtocol != "" && preferredProtocol != "any" {
              pa, pb := a.Decision.Protocol == preferredProtocol, b.Decision.Protocol == preferredProtocol
              if pa != pb {
                  return pa
              }
          }
          if a.Decision.Protocol == commonv1.ProtocolTorrent && b.Decision.Protocol == commonv1.ProtocolTorrent {
              if sa, sb := seedersOf(a.Decision), seedersOf(b.Decision); sa != sb {
                  return sa > sb
              }
          }
          if a.Decision.Protocol == commonv1.ProtocolUsenet && b.Decision.Protocol == commonv1.ProtocolUsenet {
              if !a.Decision.PublishedAt.Time.Equal(b.Decision.PublishedAt.Time) {
                  return a.Decision.PublishedAt.Time.After(b.Decision.PublishedAt.Time)
              }
          }
          return a.Decision.SizeBytes > b.Decision.SizeBytes
      })

      n := limit
      if n <= 0 || n > MaxResults {
          n = MaxResults
      }
      if n > len(ranked) {
          n = len(ranked)
      }
      out := make([]catalogv1alpha1.ReleaseDecision, n)
      for i := range out {
          out[i] = ranked[i].Decision
          out[i].Rank = int32(i + 1)
      }
      return out
  }

  func seedersOf(d catalogv1alpha1.ReleaseDecision) int32 {
      if d.Seeders == nil {
          return 0
      }
      return *d.Seeders
  }
  ```

  Run again — must pass: `go test ./catalogarr/worker/search/... -run TestRankAndCap -v`. Commit:
  `git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "feat(catalogarr): pure release ranking and result capping" -- catalogarr/worker/search/rank.go catalogarr/worker/search/rank_test.go`.

- [ ] **Step 3: `BuildSearchRequest` — the item-identity-to-RPC-request pure function, table-tested.**

  Test first (`catalogarr/worker/search/request_test.go`):

  ```go
  package search

  import (
      "reflect"
      "testing"

      commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
  )

  func TestBuildSearchRequestMovie(t *testing.T) {
      got := BuildSearchRequest(commonv1.MediaKindMovie,
          TargetIDs{TmdbID: 603, ImdbID: "tt0133093", Year: 1999}, 100, true, nil, nil)

      if got.Kind != commonv1.MediaKindMovie {
          t.Errorf("Kind = %v", got.Kind)
      }
      if got.IDs["tmdb"] != "603" || got.IDs["imdb"] != "tt0133093" {
          t.Errorf("IDs = %v", got.IDs)
      }
      if got.DeadlineMillis != SearchDeadline.Milliseconds() {
          t.Errorf("DeadlineMillis = %d, want %d", got.DeadlineMillis, SearchDeadline.Milliseconds())
      }
      if !got.UserInvoked {
          t.Error("UserInvoked = false")
      }
      if want := []int32{2000}; !reflect.DeepEqual(got.Categories, want) {
          t.Errorf("Categories = %v, want %v", got.Categories, want)
      }
      if got.Season != nil || got.Episode != nil {
          t.Errorf("Season/Episode set for a movie: %+v/%+v", got.Season, got.Episode)
      }
  }

  func TestBuildSearchRequestStandardEpisode(t *testing.T) {
      season, episode := int32(1), int32(1)
      got := BuildSearchRequest(commonv1.MediaKindEpisode,
          TargetIDs{TvdbID: 279121, Season: &season, Episode: &episode}, 100, false, nil, nil)

      if got.IDs["tvdb"] != "279121" {
          t.Errorf("IDs = %v", got.IDs)
      }
      if got.Season == nil || *got.Season != 1 || got.Episode == nil || *got.Episode != 1 {
          t.Errorf("Season/Episode = %v/%v", got.Season, got.Episode)
      }
      if want := []int32{5000}; !reflect.DeepEqual(got.Categories, want) {
          t.Errorf("Categories = %v, want %v", got.Categories, want)
      }
  }

  func TestBuildSearchRequestAnimeEpisodeFoldsAbsoluteIntoEpisode(t *testing.T) {
      season, absolute := int32(1), int32(37)
      got := BuildSearchRequest(commonv1.MediaKindEpisode,
          TargetIDs{TvdbID: 279121, Season: &season, Episode: &absolute, Anime: true}, 100, false, nil, nil)

      if got.Season != nil {
          t.Errorf("Season = %v, want nil for anime (absolute numbering has no season token)", got.Season)
      }
      if got.Episode == nil || *got.Episode != 37 {
          t.Errorf("Episode = %v, want 37", got.Episode)
      }
  }

  func TestBuildSearchRequestOverridesCategoriesAndIndexerRefs(t *testing.T) {
      got := BuildSearchRequest(commonv1.MediaKindMovie, TargetIDs{TmdbID: 603}, 50, true,
          []schemaRef{{Name: "indexer-a"}}, []int32{2040})

      if len(got.IndexerRefs) != 1 || got.IndexerRefs[0].Name != "indexer-a" {
          t.Errorf("IndexerRefs = %v", got.IndexerRefs)
      }
      if want := []int32{2040}; !reflect.DeepEqual(got.Categories, want) {
          t.Errorf("Categories = %v, want %v", got.Categories, want)
      }
  }
  ```

  (`schemaRef` here is a tiny local alias `type schemaRef = schema.Ref` at the top of the test
  file, just to keep the test line short — spell it out with the real
  `github.com/mediactl/clustarr/pkg/events/schema` import.)

  Run: `go test ./catalogarr/worker/search/... -run TestBuildSearchRequest` — fails to compile.

  Implement `catalogarr/worker/search/request.go`:

  ```go
  package search

  import (
      "strconv"
      "time"

      commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
      "github.com/mediactl/clustarr/pkg/events/schema"
      "github.com/mediactl/clustarr/pkg/newznab"
  )

  // SearchDeadline is the RPC deadline every federated search carries. Spec §5's
  // clustarr.rpc.indexarr.search row: "single reply at min(deadline, 45s)".
  const SearchDeadline = 45 * time.Second

  // TargetIDs is the identity BuildSearchRequest needs for one catalog item.
  type TargetIDs struct {
      TmdbID           int64
      ImdbID           string
      TvdbID           int64
      Season, Episode  *int32
      Anime            bool // SeriesType == anime: Episode carries the absolute number
      Year             int32
      OriginalLanguage string
  }

  // BuildSearchRequest renders one catalog item's identity into the federated
  // search RPC request. indexerRefs/categories, when non-empty, override the
  // newznab.ByKind(kind) default -- Search.spec.indexerRefs/categories on an
  // interactive search.
  //
  // schema.SearchRequest (pkg/events/schema/index.go) has Season and Episode
  // but no Absolute field, unlike spec §8.2's "tvdb plus season/episode/
  // absolute". For an anime episode this function puts the absolute number
  // in Episode and leaves Season nil -- how *arr indexers key anime releases
  // (absolute numbering, no season token). Generated-type-wins deviation
  // from the spec's literal wording; the design doc's own §5 table already
  // shows this Request shape.
  func BuildSearchRequest(kind commonv1.MediaKind, ids TargetIDs, limit int32, userInvoked bool, indexerRefs []schema.Ref, categories []int32) schema.SearchRequest {
      req := schema.SearchRequest{
          Kind:           kind,
          Limit:          limit,
          DeadlineMillis: SearchDeadline.Milliseconds(),
          UserInvoked:    userInvoked,
          Year:           ids.Year,
          IndexerRefs:    indexerRefs,
      }
      if len(categories) > 0 {
          req.Categories = categories
      } else {
          req.Categories = expandCategoryIDs(newznab.ByKind(kind))
      }

      idmap := map[string]string{}
      switch kind {
      case commonv1.MediaKindMovie:
          if ids.TmdbID != 0 {
              idmap[commonv1.IDKeyTMDB] = strconv.FormatInt(ids.TmdbID, 10)
          }
          if ids.ImdbID != "" {
              idmap[commonv1.IDKeyIMDB] = ids.ImdbID
          }
      case commonv1.MediaKindEpisode:
          if ids.TvdbID != 0 {
              idmap[commonv1.IDKeyTVDB] = strconv.FormatInt(ids.TvdbID, 10)
          }
          req.Episode = ids.Episode
          if !ids.Anime {
              req.Season = ids.Season
          }
      }
      if len(idmap) > 0 {
          req.IDs = idmap
      }
      return req
  }

  func expandCategoryIDs(cats []newznab.CategoryID) []int32 {
      out := make([]int32, len(cats))
      for i, c := range cats {
          out[i] = int32(c)
      }
      return out
  }
  ```

  Run again — must pass. Commit:
  `git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "feat(catalogarr): build the federated search RPC request from an item's identity" -- catalogarr/worker/search/request.go catalogarr/worker/search/request_test.go`.

- [ ] **Step 4: `BuildDownloadSource` — the release-to-DownloadSource pure function, table-tested.**

  Test first (`catalogarr/controller/search/grab_test.go`):

  ```go
  package search

  import (
      "testing"

      commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
  )

  func TestBuildDownloadSourcePrefersMagnet(t *testing.T) {
      rel := commonv1.ReleaseInfo{
          GUID: "g1", IndexerRef: "idx", MagnetURL: "magnet:?xt=urn:btih:abc",
          DownloadURL: "https://idx.example/dl/g1",
      }
      got := BuildDownloadSource(rel)
      if got.MagnetURL == nil || *got.MagnetURL != rel.MagnetURL {
          t.Fatalf("got %+v", got)
      }
      if got.IndexerDownload != nil {
          t.Errorf("IndexerDownload set when MagnetURL present: %+v", got.IndexerDownload)
      }
  }

  func TestBuildDownloadSourceFallsBackToIndexerDownload(t *testing.T) {
      rel := commonv1.ReleaseInfo{GUID: "g2", IndexerRef: "idx", DownloadURL: "https://idx.example/dl/g2"}
      got := BuildDownloadSource(rel)
      if got.MagnetURL != nil {
          t.Fatalf("MagnetURL set: %+v", got)
      }
      if got.IndexerDownload == nil || got.IndexerDownload.GUID != "g2" ||
          got.IndexerDownload.IndexerRef != "idx" || got.IndexerDownload.URL != rel.DownloadURL {
          t.Fatalf("got %+v", got.IndexerDownload)
      }
  }
  ```

  Also add, in the same file, the grab-resolution pure function's tests:

  ```go
  import (
      catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
  )

  func TestResolveGrabApprovedNeedsNoOverride(t *testing.T) {
      results := []catalogv1alpha1.ReleaseDecision{{ReleaseInfo: commonv1.ReleaseInfo{GUID: "g1"}, Approved: true}}
      got := resolveGrab("g1", results, false)
      if !got.Allowed || got.Error != "" {
          t.Fatalf("got %+v", got)
      }
  }

  func TestResolveGrabPermanentRejectionNeedsOverride(t *testing.T) {
      results := []catalogv1alpha1.ReleaseDecision{{
          ReleaseInfo: commonv1.ReleaseInfo{GUID: "g2"}, Approved: false,
          Rejections: []commonv1.Rejection{{Reason: "release group is unwanted", Type: commonv1.RejectionPermanent}},
      }}
      if got := resolveGrab("g2", results, false); got.Allowed {
          t.Fatalf("Allowed without override: %+v", got)
      }
      if got := resolveGrab("g2", results, true); !got.Allowed {
          t.Fatalf("still blocked with override: %+v", got)
      }
  }

  func TestResolveGrabTemporaryRejectionNeedsNoOverride(t *testing.T) {
      results := []catalogv1alpha1.ReleaseDecision{{
          ReleaseInfo: commonv1.ReleaseInfo{GUID: "g3"}, Approved: false, TemporarilyRejected: true,
          Rejections: []commonv1.Rejection{{Reason: "queue already has an equal-or-better candidate", Type: commonv1.RejectionTemporary}},
      }}
      if got := resolveGrab("g3", results, false); !got.Allowed {
          t.Fatalf("got %+v, want allowed (only Permanent rejections require override)", got)
      }
  }

  func TestResolveGrabGuidNotInResults(t *testing.T) {
      got := resolveGrab("missing", nil, true)
      if got.Allowed || got.Error == "" {
          t.Fatalf("got %+v, want a not-found error", got)
      }
  }
  ```

  Run: `go test ./catalogarr/controller/search/... -run 'TestBuildDownloadSource|TestResolveGrab'` — fails to compile.

  Implement `catalogarr/controller/search/grab.go`:

  ```go
  package search

  import (
      catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
      commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
      downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
  )

  // BuildDownloadSource maps a release to a Download's spec.source. A magnet
  // link needs no indexer round-trip and is preferred when present; otherwise
  // the release is resolved through indexarr (rpc.indexarr.download) via
  // IndexerDownload, which applies the indexer's own auth, rate limits and
  // proxying -- spec §8.2's "source (indexerDownload when the indexer is
  // authenticated)". Exported because the automatic-grab task (out of scope
  // here) needs the identical mapping.
  func BuildDownloadSource(rel commonv1.ReleaseInfo) downloadv1alpha1.DownloadSource {
      if rel.MagnetURL != "" {
          m := rel.MagnetURL
          return downloadv1alpha1.DownloadSource{MagnetURL: &m}
      }
      return downloadv1alpha1.DownloadSource{
          IndexerDownload: &downloadv1alpha1.IndexerDownload{
              IndexerRef: rel.IndexerRef,
              GUID:       rel.GUID,
              URL:        rel.DownloadURL,
          },
      }
  }

  // grabDecision is resolveGrab's verdict for one requested GUID.
  type grabDecision struct {
      Release commonv1.ReleaseInfo
      Allowed bool
      Error   string
  }

  // resolveGrab looks guid up in results and applies spec §8.2's "Override
  // required for Permanently-rejected release" rule: an Approved release, or
  // a rejected one whose Rejections carry no Permanent entry, grabs freely; a
  // Permanent rejection requires override.
  func resolveGrab(guid string, results []catalogv1alpha1.ReleaseDecision, override bool) grabDecision {
      for _, r := range results {
          if r.GUID != guid {
              continue
          }
          if r.Approved {
              return grabDecision{Release: r.ReleaseInfo, Allowed: true}
          }
          for _, rej := range r.Rejections {
              if rej.Type == commonv1.RejectionPermanent && !override {
                  return grabDecision{
                      Release: r.ReleaseInfo,
                      Error:   "release was permanently rejected: " + rej.Reason + "; set spec.override to grab it anyway",
                  }
              }
          }
          return grabDecision{Release: r.ReleaseInfo, Allowed: true}
      }
      return grabDecision{Error: "guid " + guid + " is not in status.results"}
  }
  ```

  Run again — must pass. Commit:
  `git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "feat(catalogarr): Download source mapping and Search-CR grab resolution" -- catalogarr/controller/search/grab.go catalogarr/controller/search/grab_test.go`.

- [ ] **Step 5: the search RPC client (`SearchRPC`, `NewBusSearchRPC`, `FakeSearchRPC`) against `membus`.**

  Test first (`catalogarr/worker/search/rpc_test.go`):

  ```go
  package search

  import (
      "context"
      "encoding/json"
      "errors"
      "testing"
      "time"

      "github.com/mediactl/clustarr/pkg/events"
      "github.com/mediactl/clustarr/pkg/events/membus"
      "github.com/mediactl/clustarr/pkg/events/schema"
  )

  func TestBusSearchRPCRoundTrips(t *testing.T) {
      bus := membus.New(nil)
      ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
      defer cancel()

      if err := bus.Serve(events.RPCIndexSearch, "indexarr", func(_ context.Context, data []byte) ([]byte, error) {
          var req schema.SearchRequest
          if err := json.Unmarshal(data, &req); err != nil {
              return nil, err
          }
          if req.Kind != "movie" {
              t.Errorf("Kind = %q", req.Kind)
          }
          return json.Marshal(schema.SearchResponse{Releases: []schema.Release{{ParsedTitle: "The Matrix"}}})
      }); err != nil {
          t.Fatalf("Serve: %v", err)
      }

      rpc := NewBusSearchRPC(bus)
      resp, err := rpc.Search(ctx, schema.SearchRequest{Kind: "movie"})
      if err != nil {
          t.Fatalf("Search: %v", err)
      }
      if len(resp.Releases) != 1 || resp.Releases[0].ParsedTitle != "The Matrix" {
          t.Fatalf("resp = %+v", resp)
      }
  }

  func TestBusSearchRPCNoRespondersIsRetryable(t *testing.T) {
      bus := membus.New(nil)
      ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
      defer cancel()

      rpc := NewBusSearchRPC(bus)
      _, err := rpc.Search(ctx, schema.SearchRequest{Kind: "movie"})
      if err == nil {
          t.Fatal("want an error when nothing serves the subject")
      }
      var re *events.RetryError
      if !errors.As(err, &re) {
          t.Fatalf("err = %v, want an events.RetryError so the worker naks instead of dead-lettering", err)
      }
  }

  func TestFakeSearchRPCRecordsRequestsAndReturnsCannedResponse(t *testing.T) {
      fake := &FakeSearchRPC{Response: schema.SearchResponse{Releases: []schema.Release{{ParsedTitle: "canned"}}}}
      resp, err := fake.Search(context.Background(), schema.SearchRequest{Kind: "movie"})
      if err != nil {
          t.Fatalf("Search: %v", err)
      }
      if len(resp.Releases) != 1 || resp.Releases[0].ParsedTitle != "canned" {
          t.Fatalf("resp = %+v", resp)
      }
      if len(fake.Requests) != 1 || fake.Requests[0].Kind != "movie" {
          t.Fatalf("Requests = %+v", fake.Requests)
      }
  }
  ```

  Run: `go test ./catalogarr/worker/search/... -run 'TestBusSearchRPC|TestFakeSearchRPC'` — fails to compile.

  Implement `catalogarr/worker/search/rpc.go`:

  ```go
  package search

  import (
      "context"
      "errors"
      "fmt"
      "time"

      "github.com/mediactl/clustarr/pkg/events"
      "github.com/mediactl/clustarr/pkg/events/schema"
  )

  // SearchRPC is the federated-search half of clustarr.rpc.indexarr.search
  // that this package depends on. indexarr (Phase D) MUST serve this subject
  // with exactly schema.SearchRequest -> schema.SearchResponse, already
  // pinned in pkg/events/schema/index.go -- this package adds no new payload
  // type, only this narrow client interface plus a fake for tests.
  type SearchRPC interface {
      Search(ctx context.Context, req schema.SearchRequest) (schema.SearchResponse, error)
  }

  type busSearchRPC struct{ r events.Requester }

  // NewBusSearchRPC wraps a bus's request/reply half as a SearchRPC. Spec §5:
  // "micro, queue group indexarr, single reply at min(deadline, 45s)" --
  // events.Requester.Request already implements the single-reply semantics;
  // this wrapper only translates events.ErrNoResponders (indexarr is down or
  // not built yet, Phase D) into a retryable error instead of a hard failure,
  // per §8.8's worker error handling.
  func NewBusSearchRPC(r events.Requester) SearchRPC { return &busSearchRPC{r: r} }

  func (b *busSearchRPC) Search(ctx context.Context, req schema.SearchRequest) (schema.SearchResponse, error) {
      var resp schema.SearchResponse
      err := b.r.Request(ctx, events.RPCIndexSearch, req, &resp)
      if err != nil {
          if errors.Is(err, events.ErrNoResponders) {
              return schema.SearchResponse{}, events.Retry(15*time.Second, fmt.Errorf("search RPC: %w", err))
          }
          return schema.SearchResponse{}, fmt.Errorf("search RPC: %w", err)
      }
      return resp, nil
  }

  // FakeSearchRPC is a SearchRPC test double: it returns Response/Err
  // unconditionally and records every request it saw. Exported so other
  // tasks that consume this package (the wanted cron, the delay/grab task)
  // can reuse it instead of writing their own.
  type FakeSearchRPC struct {
      Response schema.SearchResponse
      Err      error
      Requests []schema.SearchRequest
  }

  func (f *FakeSearchRPC) Search(_ context.Context, req schema.SearchRequest) (schema.SearchResponse, error) {
      f.Requests = append(f.Requests, req)
      if f.Err != nil {
          return schema.SearchResponse{}, f.Err
      }
      return f.Response, nil
  }
  ```

  Run again — must pass: `go test ./catalogarr/worker/search/... -run 'TestBusSearchRPC|TestFakeSearchRPC' -v`. Commit:
  `git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "feat(catalogarr): federated search RPC client and fake" -- catalogarr/worker/search/rpc.go catalogarr/worker/search/rpc_test.go`.

- [ ] **Step 6: Download field indexes for the blocklist and queue informers, proven against envtest.**

  Spec §8.2's worker snapshots "the live Downloads (queue and blocklist informers)"; the
  `Download` type's own doc comment (`api/download/v1alpha1/download_types.go`,
  `LabelBlocklisted`) says the decision engine reads the live blocklisted set "through a
  catalogarr informer indexed by infohash and normalized title" — build that index here, plus
  one for "is there already an active Download for this exact target" (the queue).

  Test first (`catalogarr/worker/search/blocklist_test.go`, envtest — this needs a real
  apiserver because field indexes are a controller-runtime cache feature, not something
  `client.Client.List` alone can fake):

  ```go
  package search_test

  import (
      "context"
      "os"
      "testing"
      "time"

      corev1 "k8s.io/api/core/v1"
      metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
      ctrl "sigs.k8s.io/controller-runtime"
      "sigs.k8s.io/controller-runtime/pkg/client"
      "sigs.k8s.io/controller-runtime/pkg/envtest"

      commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
      downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
      "github.com/mediactl/clustarr/catalogarr/worker/search"
      "github.com/mediactl/clustarr/pkg/k8s"
  )

  func TestBlocklistIndexesFindByInfoHashAndTitle(t *testing.T) {
      if os.Getenv("KUBEBUILDER_ASSETS") == "" {
          t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
      }
      env := &envtest.Environment{CRDDirectoryPaths: []string{"../../../config/crd/bases"}, ErrorIfCRDPathMissing: true}
      cfg, err := env.Start()
      if err != nil {
          t.Fatalf("start envtest: %v", err)
      }
      t.Cleanup(func() { _ = env.Stop() })

      mgr, err := ctrl.NewManager(cfg, ctrl.Options{
          Scheme:                 k8s.MustNewScheme(),
          Metrics:                metricsserver.Options{BindAddress: k8s.DisabledBindAddress},
          HealthProbeBindAddress: k8s.DisabledBindAddress,
      })
      if err != nil {
          t.Fatalf("manager: %v", err)
      }
      if err := search.RegisterDownloadIndexes(context.Background(), mgr.GetFieldIndexer()); err != nil {
          t.Fatalf("RegisterDownloadIndexes: %v", err)
      }
      ctx, cancel := context.WithCancel(context.Background())
      defer cancel()
      go func() { _ = mgr.Start(ctx) }()
      if !mgr.GetCache().WaitForCacheSync(ctx) {
          t.Fatal("cache did not sync")
      }

      c := mgr.GetClient()
      const ns = "blocklist-idx"
      if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}); err != nil {
          t.Fatalf("create namespace: %v", err)
      }
      until := metav1.NewTime(time.Now().Add(24 * time.Hour))
      blocked := &downloadv1alpha1.Download{
          ObjectMeta: metav1.ObjectMeta{Name: "blocked-1", Namespace: ns, Labels: map[string]string{downloadv1alpha1.LabelBlocklisted: downloadv1alpha1.LabelBlocklistedValue}},
          Spec: downloadv1alpha1.DownloadSpec{
              Protocol: commonv1.ProtocolTorrent,
              Source:   downloadv1alpha1.DownloadSource{MagnetURL: strPtr("magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567")},
              Release:  commonv1.ReleaseInfo{GUID: "g1", Title: "The.Matrix.1999.720p.BluRay.x264-BAD", InfoHash: "0123456789abcdef0123456789abcdef01234567"},
              Target:   commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "the-matrix"},
          },
          Status: downloadv1alpha1.DownloadStatus{BlocklistedUntil: &until},
      }
      if err := c.Create(ctx, blocked); err != nil {
          t.Fatalf("create blocked Download: %v", err)
      }

      k8s.EventuallyConsistent(t, func() bool {
          var list downloadv1alpha1.DownloadList
          if err := c.List(ctx, &list, client.MatchingFields{search.IndexBlocklistInfoHash: "0123456789abcdef0123456789abcdef01234567"}); err != nil {
              t.Fatalf("list by infoHash: %v", err)
          }
          return len(list.Items) == 1 && list.Items[0].Name == "blocked-1"
      })
  }

  func strPtr(s string) *string { return &s }
  ```

  (If `k8s.EventuallyConsistent` does not exist, write the poll inline with
  `wait.PollUntilContextTimeout` instead — the point is only that the cache needs a moment to
  sync after `Create`, not a specific helper name.)

  Run — fails to compile (`search.RegisterDownloadIndexes`, `search.IndexBlocklistInfoHash`
  undefined).

  Implement `catalogarr/worker/search/blocklist.go`:

  ```go
  package search

  import (
      "context"

      "sigs.k8s.io/controller-runtime/pkg/client"

      downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
      "github.com/mediactl/clustarr/pkg/release"
  )

  // Field index names, used both by RegisterDownloadIndexes and by the
  // worker's own List calls.
  const (
      IndexBlocklistInfoHash = "search.clustarr.io/blocklist-infohash"
      IndexBlocklistTitle    = "search.clustarr.io/blocklist-title"
      IndexDownloadTarget    = "search.clustarr.io/download-target"
  )

  // RegisterDownloadIndexes adds the three field indexes the search worker
  // needs on Download: two for the live blocklist (spec.release.infoHash and
  // a normalized spec.release.title, restricted to Downloads carrying
  // download.clustarr.io/blocklisted -- see that label's doc comment on
  // Download for why the blocklist has no CRD of its own), one for "is there
  // already an active Download for this exact target" (the queue). Call it
  // once per manager, before the cache starts.
  func RegisterDownloadIndexes(ctx context.Context, idx client.FieldIndexer) error {
      if err := idx.IndexField(ctx, &downloadv1alpha1.Download{}, IndexBlocklistInfoHash, func(o client.Object) []string {
          d := o.(*downloadv1alpha1.Download)
          if !isBlocklisted(d) || d.Spec.Release.InfoHash == "" {
              return nil
          }
          return []string{d.Spec.Release.InfoHash}
      }); err != nil {
          return err
      }
      if err := idx.IndexField(ctx, &downloadv1alpha1.Download{}, IndexBlocklistTitle, func(o client.Object) []string {
          d := o.(*downloadv1alpha1.Download)
          if !isBlocklisted(d) {
              return nil
          }
          return []string{release.CleanTitle(d.Spec.Release.Title)}
      }); err != nil {
          return err
      }
      return idx.IndexField(ctx, &downloadv1alpha1.Download{}, IndexDownloadTarget, func(o client.Object) []string {
          d := o.(*downloadv1alpha1.Download)
          if isTerminal(d.Status.Phase) {
              return nil
          }
          return []string{string(d.Spec.Target.Kind) + "/" + d.Spec.Target.Name}
      })
  }

  func isBlocklisted(d *downloadv1alpha1.Download) bool {
      return d.Labels[downloadv1alpha1.LabelBlocklisted] == downloadv1alpha1.LabelBlocklistedValue
  }

  func isTerminal(p downloadv1alpha1.DownloadPhase) bool {
      switch p {
      case downloadv1alpha1.DownloadPhaseImported, downloadv1alpha1.DownloadPhaseFailed, downloadv1alpha1.DownloadPhaseBlocklisted, downloadv1alpha1.DownloadPhaseRemoving:
          return true
      default:
          return false
      }
  }
  ```

  Run again — must pass under `make test`. Commit:
  `git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "feat(catalogarr): Download field indexes for the blocklist and queue informers" -- catalogarr/worker/search/blocklist.go catalogarr/worker/search/blocklist_test.go`.

- [ ] **Step 7: the item snapshot (Movie and Episode) — fetch, resolved profile, current `MediaFile`.**

  This step has no pure function to TDD in isolation (it is a handful of `client.Get` calls
  keyed by `commonv1.MediaRef`); write it test-first against envtest instead, in
  `catalogarr/worker/search/snapshot_envtest_test.go`, asserting on two fixtures: a Movie with
  no file yet (current == nil) and an Episode with a MediaFile (current != nil, revision/quality
  come from the MediaFile, not just the item status). Create both fixtures with `c.Create`
  (Movie needs a `RootFolder` + `QualityProfile` it references only by name string, so no
  further fixture objects are required — `MovieSpec.QualityProfileRef`/`RootFolderRef` are
  plain strings, not enforced references). Follow the `newDownload`-style helper pattern from
  `pkg/k8s/patch_envtest_test.go`.

  Implement `catalogarr/worker/search/snapshot.go` with:

  ```go
  // itemSnapshot is what the worker needs from the catalog item and its
  // current file, independent of media kind.
  type itemSnapshot struct {
      IDs               TargetIDs
      QualityProfileRef string
      RuntimeMinutes    int32
      Available         bool
      Current           *quality.Candidate // nil when HasFile is false
  }

  // snapshot fetches the target named by ref (in namespace ns) and its
  // current MediaFile, if any. Only movie and episode are implemented --
  // §16 M1 scopes catalogarr's non-video kinds (artist, album, author, book,
  // audiobook, comic, issue) to M6; a SearchTask for any other kind is
  // events.Discard'd by the caller, not handled here.
  func (w *Worker) snapshot(ctx context.Context, ns string, ref commonv1.MediaRef) (itemSnapshot, error)
  ```

  Movie: `IDs.TmdbID = movie.Spec.TmdbID`, `IDs.Year = movie.Status.Metadata.Year` (nil-safe),
  `QualityProfileRef = movie.Spec.QualityProfileRef`, `RuntimeMinutes =
  movie.Status.Metadata.RuntimeMinutes`, `Available = movie.Status.Available`. Episode:
  `IDs.TvdbID = episode.Status.TvdbID`, `IDs.Season/Episode = episode.Spec.SeasonNumber/
  EpisodeNumber`, `IDs.Anime` and `QualityProfileRef` come from the owning `Series`
  (`episode.Spec.SeriesRef`, one extra `client.Get`) — `Series.Spec.SeriesType == "anime"` and
  `Series.Spec.QualityProfileRef`. `RuntimeMinutes = episode.Status.RuntimeMinutes`, `Available
  = episode.Status.Phase != catalogv1alpha1.EpisodePhaseUnaired`. Both kinds: when
  `status.hasFile`, `Get` the `MediaFile` named `*status.fileRef` and set `Current =
  &quality.Candidate{Quality: mf.Spec.Quality, Revision: mf.Spec.Revision, FormatScore:
  int(mf.Status... )}` — **use `mf.Spec.FormatScore`, not a status field**: `MediaFileSpec` (not
  `MediaFileStatus`) carries `FormatScore int32` per `api/catalog/v1alpha1/mediafile_types.go`,
  frozen at import.

  Run `make test` (envtest) — must pass. Commit:
  `git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "feat(catalogarr): search worker item snapshot (Movie and Episode)" -- catalogarr/worker/search/snapshot.go catalogarr/worker/search/snapshot_envtest_test.go`.

- [ ] **Step 8: `Worker.Handle` — wire RPC, parse+score, `Evaluate`, `RankAndCap`, and the two sinks.**

  No new pure function here; this step assembles Steps 2–7 plus `pkg/decision` (real or the
  placeholder from the scope note above) into one handler, test-first against `membus` +
  `FakeSearchRPC` in `catalogarr/worker/search/worker_envtest_test.go` (envtest for the item
  fixtures, membus for the bus — no NATS needed, matching "Tests use the in-memory bus and the
  fake search RPC"). Two cases: (1) `SearchTask.SearchRef` set → after `Handle` returns nil,
  `Search.status.phase == Completed` and `status.results` is capped/ranked; (2) `SearchRef` nil
  → a recording `Sink` (a tiny test double implementing `Sink`) received the ranked list instead
  of any status write happening.

  Implement `catalogarr/worker/search/worker.go`:

  ```go
  func NewWorker(c client.Client, rpc SearchRPC, cat *catalogue.Catalogue) *Worker {
      return &Worker{Client: c, RPC: rpc, Catalogue: cat, Evaluate: decision.Evaluate, Sink: NopSink{}, Clock: clockwork.NewRealClock()}
  }

  func (w *Worker) Handle(ctx context.Context, m events.Message) error {
      var task schema.SearchTask
      if err := schema.Decode(m.Envelope().Schema, m.Envelope().Data, &task); err != nil {
          return events.Discard("undecodable SearchTask", err)
      }
      ns, _, ok := strings.Cut(m.Envelope().Key, "/")
      if !ok {
          return events.Discard("Clustarr-Key is not <namespace>/<name>", fmt.Errorf("key=%q", m.Envelope().Key))
      }

      switch task.MediaRef.Kind {
      case commonv1.MediaKindMovie, commonv1.MediaKindEpisode:
      default:
          return events.Discard("non-video search is M6 scope", fmt.Errorf("kind=%s", task.MediaRef.Kind))
      }

      snap, err := w.snapshot(ctx, ns, task.MediaRef)
      if apierrors.IsNotFound(err) {
          return events.Discard("target no longer exists", err)
      } else if err != nil {
          return fmt.Errorf("snapshot: %w", err)
      }

      qp := &catalogv1alpha1.QualityProfile{}
      if err := w.Client.Get(ctx, client.ObjectKey{Name: snap.QualityProfileRef}, qp); err != nil {
          return fmt.Errorf("get QualityProfile %s: %w", snap.QualityProfileRef, err)
      }
      profile, ferrs := quality.FromCRD(qp, w.Catalogue)
      if len(ferrs) > 0 {
          return events.Discard("invalid QualityProfile", errors.Join(ferrs...))
      }

      req := BuildSearchRequest(task.MediaRef.Kind, snap.IDs, schema.MaxSearchReleases, task.UserInvoked, nil, nil)
      resp, err := w.RPC.Search(ctx, req)
      if err != nil {
          return err // already an events.RetryError from busSearchRPC, or a plain error -> backoff nak
      }

      items := make([]Scored, 0, len(resp.Releases))
      for _, rel := range resp.Releases {
          parsed, perr := release.Parse(rel.Info.Title, release.Options{Kind: task.MediaRef.Kind})
          if perr != nil {
              continue // never guess a quality for an unparseable title
          }
          info := rel.Info
          parsed.ApplyTo(&info)
          score, matched := profile.Score(ctx, w.Catalogue, parsed, catalogue.ItemContext{
              OriginalLanguage: snap.IDs.OriginalLanguage,
              IndexerFlags:     info.IndexerFlags,
              ReleaseType:      info.ReleaseType,
          })
          info.FormatScore, info.MatchedFormats = int32(score), matched

          d := w.Evaluate(ctx, decision.Input{
              Release:               info,
              Profile:               profile,
              Current:               snap.Current,
              RuntimeMinutes:        snap.RuntimeMinutes,
              Available:             snap.Available,
              UserInvoked:           task.UserInvoked,
              AlreadyImported:       w.alreadyImported(snap.Current, info),
              Blocklisted:           w.blocklisted(ctx, info),
              QueueHasHigherOrEqual: w.queueHasHigherOrEqual(ctx, task.MediaRef, profile, info),
          })
          tier, _ := profile.Index(d.Quality)
          items = append(items, Scored{TierIndex: tier, Decision: catalogv1alpha1.ReleaseDecision{
              ReleaseInfo: info, Approved: d.Approved, TemporarilyRejected: d.TemporarilyRejected, Rejections: d.Rejections,
          }})
      }
      ranked := RankAndCap(items, int(req.Limit), commonv1.Protocol(profile.PreferredProtocol))

      if task.SearchRef != nil {
          return w.writeSearchStatus(ctx, *task.SearchRef, resp.Outcomes, ranked)
      }
      return w.Sink.Deliver(ctx, task, ranked)
  }
  ```

  `alreadyImported`/`blocklisted`/`queueHasHigherOrEqual` are small helpers: `alreadyImported`
  compares `info.InfoHash`/`release.CleanTitle(info.Title)` against `snap.Current` via the
  `ImportedFrom` fingerprint already frozen on the `MediaFile` (fetched in Step 7 — thread it
  through `itemSnapshot`, or re-fetch by name; either is fine, note the choice in the code
  comment); `blocklisted` lists `IndexBlocklistInfoHash`/`IndexBlocklistTitle`;
  `queueHasHigherOrEqual` lists `IndexDownloadTarget` for `task.MediaRef` and compares each
  active Download's `spec.release.quality`/`formatScore` against `info` via
  `profile.UpgradeDecision`. `writeSearchStatus` fetches the live `Search` (needed for
  `ObservedGeneration` and because a Query-mode Search must never reach here — see Step 10) and
  calls `k8s.PatchStatus` with the Step 1 apply configuration, `Phase: Completed`,
  `FinishedAt: now`, the mapped `IndexerOutcomes`, and `Results: ranked`.

  `type NopSink struct{ Log *slog.Logger }` and its `Deliver` just logs at `warn` level that the
  automatic delay/grab path is not wired yet (§16: "Nothing reconciles yet") and returns nil —
  the task must still ack the message, not retry forever for a gap this task documents as
  intentional.

  Run `make test` — must pass. Commit:
  `git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "feat(catalogarr): search worker Handle -- snapshot, score, decide, rank, dispatch" -- catalogarr/worker/search/worker.go catalogarr/worker/search/worker_envtest_test.go`.

- [ ] **Step 9: `Worker.SetupWithManager` — register indexes and subscribe both search consumers.**

  ```go
  func (w *Worker) SetupWithManager(mgr ctrl.Manager, bus events.Bus) error {
      if err := RegisterDownloadIndexes(context.Background(), mgr.GetFieldIndexer()); err != nil {
          return fmt.Errorf("register Download indexes: %w", err)
      }
      topo := events.Default()
      for _, name := range []string{events.ConsumerCatalogSearchHigh, events.ConsumerCatalogSearchNorm} {
          spec, ok := topo.Consumer(name)
          if !ok {
              return fmt.Errorf("no consumer spec named %s in the default topology", name)
          }
          sub := spec.Subscription() // capture per iteration, each name gets its own Runnable
          if err := mgr.Add(runnableFunc(func(ctx context.Context) error {
              stop, err := bus.Subscribe(ctx, sub, w.Handle)
              if err != nil {
                  return err
              }
              <-ctx.Done()
              stop()
              return nil
          })); err != nil {
              return fmt.Errorf("add %s consumer: %w", name, err)
          }
      }
      return nil
  }

  // runnableFunc implements manager.Runnable + manager.LeaderElectionRunnable
  // (NeedLeaderElection() bool { return false }) so both consumers run on
  // every replica, per docs/research/k8s.md §6/§7: "Non-leader work (NATS
  // consumers, HTTP APIs) is added with mgr.Add(r)".
  type runnableFunc func(ctx context.Context) error

  func (f runnableFunc) Start(ctx context.Context) error { return f(ctx) }
  func (runnableFunc) NeedLeaderElection() bool          { return false }
  ```

  Test (`worker_envtest_test.go`, extending Step 8's suite): start a real manager against
  envtest with a real `membus.Bus`, call `SetupWithManager`, publish a `SearchTask` onto
  `events.WorkSearchSubject(events.PriorityHigh, ...)` with `bus.Ensure`'d topology
  (`events.Default()`), and assert `Handle` fired (e.g. via the recording `Sink` from Step 8)
  within a short poll window.

  Run `make test`. Commit:
  `git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "feat(catalogarr): wire the search worker's consumers and field indexes into a manager" -- catalogarr/worker/search/worker.go catalogarr/worker/search/worker_envtest_test.go`.

- [ ] **Step 10: the `Search` controller — validate, publish, handle Query-mode, set `Running`.**

  Test first (`catalogarr/controller/search/reconciler_envtest_test.go`): create a `Search`
  with `spec.mediaRef` set (movie), start a manager with `Reconciler.SetupWithManager` and a
  `membus.Bus`, assert that within a short poll window `status.phase == Running`,
  `status.startedAt` is set, and exactly one message landed on
  `clustarr.work.catalogarr.search.high.*` (subscribe a throwaway consumer in the test to
  observe it, or use `bus.(*membus.Bus)`'s test-visible state if membus exposes one — otherwise
  register a second `Serve`-style probe). A second test creates a `Search` with `spec.query` set
  instead and asserts `status.phase == Failed` with a condition explaining query-mode search
  needs indexarr's release index, not yet available.

  Implement `catalogarr/controller/search/reconciler.go`'s first-reconcile branch:

  ```go
  func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
      s := &catalogv1alpha1.Search{}
      if err := r.Client.Get(ctx, req.NamespacedName, s); err != nil {
          return ctrl.Result{}, client.IgnoreNotFound(err)
      }

      if s.Spec.Query != nil {
          return r.failQueryMode(ctx, s)
      }
      if s.Status.Phase == "" {
          return r.startSearch(ctx, s)
      }
      if r.ttlExpired(s) {
          return ctrl.Result{}, client.IgnoreNotFound(r.Client.Delete(ctx, s))
      }
      if s.Status.Phase == catalogv1alpha1.SearchPhaseRunning && r.stuck(s) {
          return r.failStuck(ctx, s)
      }
      if len(s.Spec.Grab) > len(s.Status.Grabbed) {
          return r.handleGrabs(ctx, s)
      }
      return ctrl.Result{RequeueAfter: r.ttlRequeue(s)}, nil
  }
  ```

  `startSearch` builds `schema.SearchTask{MediaRef: *s.Spec.MediaRef, Reason:
  schema.SearchReasonInteractive, SearchRef: &schema.Ref{Namespace: s.Namespace, Name: s.Name,
  UID: string(s.UID)}, UserInvoked: true}`, encodes it with `schema.Encode`, builds the
  `events.Envelope{ID: events.MsgIDForObject(string(s.UID), s.Generation, "search"), Type:
  "work", Schema: schemaName, Source: "catalogarr@" + version.Version, Key: s.Namespace + "/" +
  s.Name, Time: time.Now().UTC(), Data: data}`, and publishes to
  `events.WorkSearchSubject(events.PriorityHigh, s.Namespace+"/"+s.Name)`. On
  `errors.Is(err, events.ErrQueueFull)`: `k8s.PatchStatus` a `Ready=False` condition (reason
  `"QueueFull"`) via the Step 1 AC, leave `Phase` untouched, `return ctrl.Result{RequeueAfter:
  time.Minute}, nil` — mirrors spec §8.1's `ErrQueueFull` handling; `Search`'s generated
  condition consts (`SearchConditionReady`/`Completed`/`Failed`) have no `QueueFull` type of
  their own, unlike `Movie`, so this task reuses `Ready` rather than inventing an
  ungenerated condition type. On success: `PatchStatus{Phase: Running, StartedAt: now,
  ObservedGeneration: s.Generation}`.

  `failQueryMode` sets `Phase: Failed` and a `Failed=True` condition, reason `"NotImplemented"`,
  message `"query-mode search requires indexarr's release index (rpc.indexarr.query), not yet
  available before Phase D"` — a deliberate, documented scope cut: `SearchSpec`'s CEL already
  requires exactly one of `query`/`mediaRef`, so a Query-mode `Search` is a legal object this
  task cannot serve yet.

  `stuck` and `ttlExpired`/`ttlRequeue` are small pure-ish helpers taking `(s
  *catalogv1alpha1.Search, now time.Time)`:
  `SearchRunningTimeout = 5 * time.Minute` (documented as this task's own safety margin: RPC
  deadline 45s + parse/score/evaluate for up to `schema.MaxSearchReleases` releases) for
  `stuck`; `ttlExpired` compares `now` against `FinishedAt.Add(s.Spec.TTL.Duration)`.

  Run `make test`. Commit:
  `git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "feat(catalogarr): Search controller -- validate, publish, query-mode cutover, TTL" -- catalogarr/controller/search/reconciler.go catalogarr/controller/search/reconciler_envtest_test.go`.

- [ ] **Step 11: the controller's `handleGrabs` — `spec.grab` to `Download` objects.**

  Test (extend `reconciler_envtest_test.go`): create a `Search` already `Completed` with two
  `status.results` entries (one `Approved`, one `Approved: false` with a `Permanent`
  `Rejection`), then patch `spec.grab` to name both GUIDs with `spec.override = false`; assert
  after reconcile that `status.grabbed` has one `DownloadRef` set and one `Error` naming the
  missing override, and that exactly one `Download` object exists (named
  `k8s.ChildName(mediaRef.Name, guid)`, per spec §8.2's `<target>-<sha1(guid)[:10]>`). Then set
  `spec.override = true` and assert the second `Download` now exists too and its
  `Error` cleared to `""` on the next reconcile.

  Implement `handleGrabs`:

  ```go
  func (r *Reconciler) handleGrabs(ctx context.Context, s *catalogv1alpha1.Search) (ctrl.Result, error) {
      grabbed := make(map[string]catalogv1alpha1.GrabResult, len(s.Status.Grabbed))
      for _, g := range s.Status.Grabbed {
          grabbed[g.GUID] = g
      }
      targetName := s.Spec.MediaRef.Name
      for _, guid := range s.Spec.Grab {
          if existing, done := grabbed[guid]; done && existing.Error == "" {
              continue
          }
          got := resolveGrab(guid, s.Status.Results, s.Spec.Override)
          if !got.Allowed {
              grabbed[guid] = catalogv1alpha1.GrabResult{GUID: guid, Error: got.Error}
              continue
          }
          name := k8s.ChildName(targetName, guid)
          dl := downloadac.Download(name, s.Namespace).WithSpec(
              downloadac.DownloadSpec().
                  WithProtocol(got.Release.Protocol).
                  WithSource(toDownloadSourceAC(BuildDownloadSource(got.Release))).
                  WithRelease(got.Release).
                  WithTarget(*s.Spec.MediaRef).
                  WithGrabbedBy(downloadv1alpha1.GrabSourceInteractive),
          )
          if _, err := k8s.Apply(ctx, r.Client, k8s.ManagerCatalogarr, dl); err != nil {
              grabbed[guid] = catalogv1alpha1.GrabResult{GUID: guid, Error: err.Error()}
              continue
          }
          grabbed[guid] = catalogv1alpha1.GrabResult{GUID: guid, DownloadRef: name}
      }
      out := make([]catalogv1alpha1.GrabResult, 0, len(grabbed))
      for _, guid := range s.Spec.Grab {
          out = append(out, grabbed[guid])
      }
      ac := search.Search(s.Name, s.Namespace).WithStatus(search.SearchStatus().WithGrabbed(out...))
      _, err := k8s.PatchStatus(ctx, r.Client, k8s.ManagerCatalogarr, ac)
      return ctrl.Result{}, err
  }
  ```

  `toDownloadSourceAC` is a tiny adapter from the plain `downloadv1alpha1.DownloadSource` Step 4
  returns to `*downloadac.DownloadSourceApplyConfiguration` — read
  `api/applyconfiguration/download/download/v1alpha1/downloadsource.go` for its `With*` methods
  (`WithMagnetURL`, `WithIndexerDownload`, etc.) and write the obvious field-by-field copy; it
  is a few lines, not worth a pure-function TDD cycle of its own, but give it a short unit test
  (`grab_test.go`) checking both branches (magnet vs. indexerDownload) map straight through.

  Note the idempotency argument explicitly in a code comment: `k8s.Apply` is SSA, `Download`
  names are deterministic, and `DownloadSpec`'s CEL rules are all `self == oldSelf`/exact-match
  on the fields this task ever sets, so re-running `handleGrabs` after a requeue re-applies the
  same values and is a no-op, not a conflict.

  Run `make test`. Commit:
  `git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "feat(catalogarr): Search controller spec.grab handling" -- catalogarr/controller/search/reconciler.go catalogarr/controller/search/reconciler_envtest_test.go catalogarr/controller/search/grab.go catalogarr/controller/search/grab_test.go`.

- [ ] **Step 12: `Reconciler.SetupWithManager` and `NewReconciler`.**

  ```go
  func NewReconciler(c client.Client, bus events.Bus, rec record.EventRecorder) *Reconciler {
      return &Reconciler{Client: c, Bus: bus, Recorder: rec, Clock: clockwork.NewRealClock()}
  }

  func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
      return ctrl.NewControllerManagedBy(mgr).
          For(&catalogv1alpha1.Search{}).
          WithOptions(controller.Options{RecoverPanic: ptr.To(true)}).
          Complete(r)
  }
  ```

  No finalizer: unlike `Download`, a deleted `Search` leaves nothing else to clean up (it owns
  no child resources other than the `Download`s created by `handleGrabs`, which are meant to
  outlive it — deleting a `Search` must never cascade-delete the grabs it made). Say this in a
  code comment so a later reviewer does not "fix" it by adding one.

  `reconcile.TerminalError` is not needed for this reconciler either: `SearchSpec`'s only
  cross-field invariant (`ExactlyOneOf(query, mediaRef)`) is enforced by CEL at admission, so
  `Reconcile` never observes an invalid spec to terminally reject — note this explicitly rather
  than silently omitting the binding constraint's `reconcile.TerminalError` requirement.

  Run `make test` (full package). Commit:
  `git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "feat(catalogarr): Search reconciler manager wiring" -- catalogarr/controller/search/reconciler.go`.

**Verification:**
- `go build ./catalogarr/...` (never bare `go build`).
- `go vet ./catalogarr/controller/search/... ./catalogarr/worker/search/...`.
- `go test ./catalogarr/worker/search/... ./catalogarr/controller/search/...` — the pure-function
  tests (Steps 2–5) and `TestBusSearchRPCNoRespondersIsRetryable`/`TestFakeSearchRPC...` run and
  pass with **no** `KUBEBUILDER_ASSETS`; the envtest suites (Steps 1, 6, 7, 8, 9, 10, 11) **skip**
  without it — confirm they are not silently skipped by re-running with assets set:
  `KUBEBUILDER_ASSETS="$(setup-envtest use 1.37.0 -p path)" go test ./catalogarr/worker/search/... ./catalogarr/controller/search/... -v | grep -c '^=== RUN'` should match the count of `func Test` in the package (i.e. nothing silently skipped), or just run `make test` from the repo root, which sets `KUBEBUILDER_ASSETS` itself.
- `golangci-lint run ./catalogarr/controller/search/... ./catalogarr/worker/search/...` — confirms
  no `client.Status().Patch`/`.Update()` call slipped in outside `pkg/k8s` (forbidigo) and no
  stray `float32`/`float64`.
- Race detector on the worker (it runs concurrently with the manager's controllers once wired):
  `go test -race ./catalogarr/worker/search/...`.

**Done when:**
- [ ] `SearchApplyConfiguration` compiles, satisfies `k8s.ApplyConfiguration`, and
      `TestSearchApplyConfigurationRoundTripsThroughPatchStatus` passes under `make test`.
- [ ] `RankAndCap`, `BuildSearchRequest`, `BuildDownloadSource` and `resolveGrab` are pure,
      exported, and table-tested with real values (no fakes needed) — verified failing-then-passing.
- [ ] `SearchRPC`/`NewBusSearchRPC`/`FakeSearchRPC` exist; a no-responder request surfaces an
      `*events.RetryError`, not a bare error or a panic.
- [ ] The three Download field indexes exist and are proven against a real apiserver cache.
- [ ] `Worker.Handle` runs the full snapshot → RPC → parse/score → `Evaluate` → `RankAndCap` →
      dispatch pipeline for Movie and Episode; interactive tasks (`SearchRef` set) end in
      `Search.status.phase == Completed` with a capped, ranked `status.results`; every other
      reason reaches `Sink.Deliver` instead, with `NopSink` as the wired default.
- [ ] `Worker.SetupWithManager` subscribes both `catalogarr-search-high` and
      `catalogarr-search-normal` as non-leader-elected `manager.Runnable`s.
- [ ] The `Search` reconciler: publishes on first reconcile at `PriorityHigh`; rejects
      Query-mode with `Phase=Failed` and an explanatory condition; self-heals a stuck `Running`
      phase; TTL-deletes once `spec.ttl` has elapsed past `status.finishedAt`; and handles
      `spec.grab`, creating exactly one `Download` per approved/overridden GUID, filling
      `status.grabbed`, and requiring `spec.override` only for a `Permanent` rejection.
- [ ] No file outside `catalogarr/controller/search/` and `catalogarr/worker/search/` is
      modified; `catalogarr/run.go` is untouched.
- [ ] `make lint` and `make test` (with `KUBEBUILDER_ASSETS` set) are green for these two
      packages.

---

### Task C9: the grab path, delay profiles, the wanted cron and the RSS matcher

**Files:**
- Create: `catalogarr/worker/grab/mediakey.go`
- Create: `catalogarr/worker/grab/mediakey_test.go`
- Create: `catalogarr/worker/grab/delay.go`
- Create: `catalogarr/worker/grab/delay_test.go`
- Create: `catalogarr/worker/grab/pending.go`
- Create: `catalogarr/worker/grab/pending_test.go`
- Create: `catalogarr/worker/grab/lease.go`
- Create: `catalogarr/worker/grab/lease_test.go`
- Create: `catalogarr/worker/grab/kindops.go`
- Create: `catalogarr/worker/grab/perform.go`
- Create: `catalogarr/worker/grab/perform_envtest_test.go`
- Create: `catalogarr/worker/grab/decide.go`
- Create: `catalogarr/worker/grab/decide_envtest_test.go`
- Create: `catalogarr/worker/grab/handler.go`
- Create: `catalogarr/worker/grab/handler_envtest_test.go`
- Create: `catalogarr/controller/wantedcron/backoff.go`
- Create: `catalogarr/controller/wantedcron/backoff_test.go`
- Create: `catalogarr/controller/wantedcron/scan.go`
- Create: `catalogarr/controller/wantedcron/scan_test.go`
- Create: `catalogarr/controller/wantedcron/runnable.go`
- Create: `catalogarr/controller/wantedcron/runnable_envtest_test.go`
- Create: `catalogarr/worker/rssmatcher/index.go`
- Create: `catalogarr/worker/rssmatcher/index_envtest_test.go`
- Create: `catalogarr/worker/rssmatcher/match.go`
- Create: `catalogarr/worker/rssmatcher/match_test.go`
- Create: `catalogarr/worker/rssmatcher/handler.go`
- Create: `catalogarr/worker/rssmatcher/handler_envtest_test.go`
- Modify: `catalogarr/run.go:267-285` (`setupControllers`, `setupWorkers`)

**Path ownership:** `catalogarr/worker/grab/`, `catalogarr/worker/rssmatcher/`,
`catalogarr/controller/wantedcron/`, plus the two named regions of
`catalogarr/run.go`. Do not touch `catalogarr/worker/search/` (Task C8),
`catalogarr/controller/delayprofile/` or any other `catalogarr/controller/<kind>/`
(Task C4 / C6), or `pkg/decision` (Task C2) — you consume their exported API,
never their files.

## Design decisions (read before coding)

The spec's prose in §8.2/§8.7/§6.1 under-specifies four boundaries this task sits
on. Each is resolved below with the evidence that settled it; do not relitigate
them mid-implementation.

1. **"Set PendingGrab and Phase=Delayed" splits across two field managers, not
   one write.** `pkg/k8s`'s `ManagerCatalogarrWorker` doc comment
   (`pkg/k8s/fieldmanager.go:39`) says it "covers the search, grab, import,
   importlist and rss-matcher consumers" but is silent on which *fields*; the
   binding invariant is CLAUDE.md's "one controller-writer per resource" and
   `ManagerCatalogarr`'s own doc comment says it "owns phase and conditions on
   every catalog.clustarr.io kind." Server-side apply only claims fields present
   in the applied configuration, so this task's code calls
   `k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrWorker, ...)` with **only**
   `WithActiveDownloadRef`, `WithPendingGrab`, `WithLastSearchedAt` and
   `WithSearchAttempts` set — never `WithPhase`. Recomputing `Phase` (e.g.
   `Wanted`→`Delayed`→`Downloading`) from those fields is the Movie/Episode
   controller's job (Task C6), reacting to a `StatusFieldChanged` predicate on
   `status.pendingGrab`/`status.activeDownloadRef`. Mark every `Phase=` mention
   below `(reconciled by controller — Task C6)`.
2. **`WantedScan` is the real handoff; the per-item backoff logic lives here but
   runs there.** §5's subject table pins
   `clustarr.work.catalogarr.wantedscan.low.<namespace>` → `catalog.WantedScan.v1`
   → "cron → search workers", and the `catalogarr-search-normal` consumer (already
   defined in `pkg/events.Default()`) filters `…wantedscan.>` — i.e. the search
   worker's own consumer receives it, not a consumer of wantedcron's. That
   is already-shipped, reviewed Phase A infrastructure; it wins over the looser
   §6.1 sentence that attributes "per-item ≥6h gap and Attempts backoff 6h·2^n
   capped 7d" directly to wantedcron. This task's wantedcron is therefore a
   `manager.Runnable` (confirmed by the comment already in
   `catalogarr/run.go:265-266`: *"wantedcron ... lands with M1 as a manager
   Runnable rather than a reconciler"*) that, on a 12h schedule, lists eligible
   items only to decide **which namespaces** to fire `WantedScan` for, and
   publishes one `WantedScan{Namespace, Kinds:[movie,episode], CutoffUnmet:true,
   Epoch}` per eligible namespace at low priority. The backoff *function*
   (`Backoff`/`NextEligible`, Step 8) is authored under this task's path
   ownership — satisfying the spec's attribution — and exported for Task C8's
   search worker to call when it expands a `WantedScan` into per-item searches.
   `(reconciled by controller — Task C8 imports `wantedcron.NextEligible`)`.
3. **The grab-or-delay decision (`grab.Decide`) is a pure entry point the
   caller resolves a `DelayProfile` for, not something this package resolves
   itself.** `Decide` takes an already-resolved `catalogv1alpha1.DelayProfileSpec`
   and a resolved `quality.Profile`. The only place *this* task calls a
   DelayProfile resolver is the RSS matcher (Step 15), which
   `(reconciled by controller — Task C4)` is assumed to export
   `func Resolve(ctx context.Context, c client.Client, namespace string, pinnedRef *string, itemTags []string) (*catalogv1alpha1.DelayProfile, error)`
   from a package this task cannot name for certain — run `go doc
   ./catalogarr/controller/delayprofile` once C4 has landed and fix the import
   in `rssmatcher/handler.go` if the name differs; the call shape (context,
   client, namespace, `*string`, `[]string`, returns `(*DelayProfile, error)`)
   is what to preserve.
4. **The "informer-backed in-memory map" for RSS matching is a controller-runtime
   field index, not a hand-rolled informer.** A field index (`
   mgr.GetFieldIndexer().IndexField`) *is* informer-backed — the cache's
   informers maintain it — and is the idiomatic, already-available primitive
   (no parallel task owns it) versus writing a second cache layer by hand. Step
   13 builds three indices on `Movie`/`Series`: `spec.tmdbID`, `spec.tvdbID`,
   and a multi-valued normalized-title+year key using `pkg/release.Normalize`
   (already merged, Phase B).

**Read first:**
- Spec §8.2 (`docs/superpowers/specs/2026-09-18-clustarr-design.md:745`, the
  "Search → decide → delay → grab" paragraph) and §8.7 (line 755, "RSS, lists,
  upgrades") in full — every clause maps to a step below.
- §5 (lines 531-615): the KV bucket table (`clustarr-leases`, `clustarr-pending`
  — exact TTLs, line 604-605), the stream/consumer table rows for
  `catalogarr-grab` and `catalogarr-rss-matcher` (AckWait/MaxDeliver/BackOff/
  MaxAckPending — copy these verbatim into your `Subscription`), and the
  `Nats-Msg-Id`/scheduled-delivery conventions in the "Message headers" bullet.
- §8.8 (line 757): `Retry`→`NakWithDelay`, generic error→backoff nak,
  `Discard`→DLQ — this is what every `Handle` return value must map to via
  `events.Retry`/`events.Discard`/`nil`.
- §6.1 (line 618) for what `wantedcron`, the `search`/`grab`/`rss-matcher`
  workers are each responsible for, read together with Design decision 2 above.
- §9 (lines 759-772) for `hd-bluray-web`'s exact tier composition (used in
  tests) and for `pkg/quality.Profile.UpgradeDecision`'s semantics.
- `api/catalog/v1alpha1/delayprofile_types.go` (full file, already read for you
  below) — `DelayProfileSpec` field names and defaults.
- `api/download/v1alpha1/download_types.go` — `DownloadSpec`, `DownloadSource`'s
  `ExactlyOneOf` CEL rule, `GrabSource`, the deterministic-name doc comment on
  `Download` itself.
- `api/catalog/v1alpha1/shared_types.go` — `PendingGrab` (only `ReleaseTitle`,
  `Protocol`, `GrabAt`; no GUID or full release).
- `api/catalog/v1alpha1/movie_types.go` and `episode_types.go` — `MovieStatus`/
  `EpisodeStatus`'s `ActiveDownloadRef`/`PendingGrab`/`LastSearchedAt`/
  `SearchAttempts` fields, and note `EpisodeSpec` has **no**
  `QualityProfileRef`/`DelayProfileRef`/`Tags` — read them off the owning
  `Series` via `EpisodeSpec.SeriesRef`.
- `api/index/v1alpha1/indexer_types.go:176-182` — `IndexerSpec.SeedCriteria`
  ("overrides the download client's seeding limits") and `SecretRef`.
- `go doc ./pkg/events` — `KV`, `Bus`, `Subscription`, `PublishOptions`,
  `WithScheduleAt`, `WithMsgID`, `ErrKeyNotFound`/`ErrKeyExists`/
  `ErrRevisionMismatch`, `LeaseKey`, `PendingKey`, `WorkGrabSubject`,
  `WorkWantedScanSubject`, `Default()`, `ConsumerSpec.Subscription()`.
- `go doc ./pkg/events/schema` — `GrabTask`, `WantedScan`, `ReleaseEvent`, `Ref`,
  `Encode`/`Decode`; `pkg/events/schema/index.go`'s `Release` struct (what
  indexarr publishes on `clustarr.rel.>` — already parsed: `Info
  commonv1.ReleaseInfo`, `ParsedTitle`, `Year`, `Seasons`, `Episodes`, `Kind`).
- `go doc ./pkg/k8s` — `PatchStatus`, `Apply`, `ChildName` (and its doc comment,
  which names this exact use case), `FieldManager`/`ManagerCatalogarrWorker`,
  `OwnerReferenceAC`, `MarkTrue`/`NewCondition`, the envtest test pattern in
  `pkg/k8s/patch_envtest_test.go` (copy its `newTestClient` shape).
- `go doc ./pkg/quality` — `Profile.Index`, `Profile.UpgradeDecision`,
  `Candidate`, `Verdict`, `BuiltinProfiles`; `go doc ./pkg/quality/catalogue` —
  `LoadedCatalogue()`.
- `go doc ./pkg/release` — `Normalize`.
- `go doc ./pkg/decision` **once Task C2 has landed** — this task assumes
  `decision.Evaluate`/`decision.Rank` exist with roughly the shape in Design
  decision 3's sibling note below; reconcile the RSS matcher's call site
  (`rssmatcher/handler.go`) against whatever C2 actually exports before wiring
  it, and record what you found in your final report.
- `catalogarr/run.go:1-30, 260-285` — service identity, `Role`, the two
  registration points you extend, and the comment already there about
  wantedcron being a `Runnable`.

**Dependencies:** `github.com/robfig/cron/v3` at **v3.0.1** — already added to
`go.mod` by Task C0 (`hack/deps/deps.go` keeps it alive); do **not** `go get`
it. `github.com/jonboulle/clockwork v0.5.0` is already in `go.mod` (used by
`pkg/events/membus`) — use it directly for fake-clock tests, do not add it
again. No other new dependency is needed: `k8s.io/apimachinery`,
`sigs.k8s.io/controller-runtime` and the project's own `pkg/*`/`api/*` cover
everything else.

**Interfaces — Consumes:**
```go
// pkg/events (already merged)
func events.WorkGrabSubject(mediaKey string) string
func events.WorkWantedScanSubject(namespace string) string
func events.LeaseKey(mediaKey string) string
func events.PendingKey(mediaKey string) string
func events.MsgIDForObject(uid string, generation int64, task string) string
const events.BucketLeases, events.BucketPending = "clustarr-leases", "clustarr-pending"
const events.ConsumerCatalogGrab, events.ConsumerCatalogRSSMatcher, events.FilterCatalogGrab, events.FilterAllReleases
func events.Default() events.Topology
func (t events.Topology) Consumer(name string) (events.ConsumerSpec, bool)
func (c events.ConsumerSpec) Subscription() events.Subscription
func events.WithScheduleAt(t time.Time) events.PublishOption
func events.WithMsgID(id string) events.PublishOption
func events.Retry(after time.Duration, err error) error
func events.Discard(reason string, err error) error
var events.ErrKeyNotFound, events.ErrKeyExists, events.ErrRevisionMismatch error

// pkg/events/schema (already merged)
type schema.GrabTask struct{ MediaRef commonv1.MediaRef; Keys []string }
type schema.WantedScan struct{ Namespace string; Kinds []commonv1.MediaKind; CutoffUnmet bool; Epoch int64 }
type schema.ReleaseEvent struct{ Media commonv1.MediaRef; Action string; GUID string; Indexer string; IndexerRef *schema.Ref; ReleaseGroup string; DownloadRef *schema.Ref; Rejections []commonv1.Rejection; Quality commonv1.Quality; FormatScore int32; At time.Time }
type schema.Release struct{ Info commonv1.ReleaseInfo; ParsedTitle string; Year int32; Seasons, Episodes, Absolute []int32; AirDate *time.Time; FullSeason, MultiSeason, Special bool; Kind commonv1.MediaKind; FetchedAt time.Time }
func schema.Encode(p schema.Payload) (schema string, data []byte, err error)
func schema.Decode(schema string, data []byte, out schema.Payload) error

// pkg/k8s (already merged)
func k8s.PatchStatus[T k8s.ApplyConfiguration](ctx, c client.Client, fm k8s.FieldManager, ac T, opts ...client.SubResourceApplyOption) (T, error)
func k8s.Apply[T k8s.ApplyConfiguration](ctx, c client.Client, fm k8s.FieldManager, ac T, opts ...client.ApplyOption) (T, error)
func k8s.ChildName(target string, parts ...string) string   // "<target>-<sha1(parts...)[:10]>"
func k8s.OwnerReferenceAC(obj client.Object, scheme *runtime.Scheme) (*metav1ac.OwnerReferenceApplyConfiguration, error)
const k8s.ManagerCatalogarrWorker k8s.FieldManager = "catalogarr-worker"

// pkg/quality, pkg/quality/catalogue, pkg/release (already merged)
func quality.FromCRD(p *catalogv1alpha1.QualityProfile, cat *catalogue.Catalogue) (quality.Profile, []error)
func quality.BuiltinProfiles(cat *catalogue.Catalogue) (map[string]quality.Profile, []error)
func (p quality.Profile) Index(q commonv1.Quality) (idx int, ok bool)          // 0 == top tier
func (p quality.Profile) UpgradeDecision(current, candidate quality.Candidate) quality.Verdict
func catalogue.LoadedCatalogue() *catalogue.Catalogue
func release.Normalize(title string) string

// pkg/events/membus (already merged, tests only)
func membus.New(clock clockwork.Clock) *membus.Bus   // clockwork.Clock from github.com/jonboulle/clockwork

// pkg/decision — (reconciled by controller, Task C2, not yet built; verify
// with `go doc ./pkg/decision` before wiring rssmatcher/handler.go)
// type decision.Input struct{ Item commonv1.MediaRef; Profile quality.Profile; UserInvoked bool; Current *quality.Candidate; ... }
// type decision.Result struct{ Approved *commonv1.ReleaseInfo; Rejections []commonv1.Rejection }
// func decision.Evaluate(ctx context.Context, in decision.Input, release schema.Release) decision.Result

// catalogarr/controller/delayprofile — (reconciled by controller, Task C4, not
// yet built; verify with `go doc ./catalogarr/controller/delayprofile`)
// func delayprofile.Resolve(ctx context.Context, c client.Client, namespace string, pinnedRef *string, itemTags []string) (*catalogv1alpha1.DelayProfile, error)
```

**Interfaces — Produces:**
```go
// catalogarr/worker/grab
package grab

type Approved struct {
	Namespace string
	Target    commonv1.MediaRef          // Kind+Name of the singleton or pack parent (movie, episode, or series)
	Keys      []string                   // episode names narrowed within Target; empty for a singleton
	Release   commonv1.ReleaseInfo
	GrabbedBy downloadv1alpha1.GrabSource
}
type Deps struct {
	Client client.Client
	Bus    events.Bus
	Now    func() time.Time              // defaults to time.Now
}
func MediaKey(namespace string, ref commonv1.MediaRef) string
func StatusTargets(target commonv1.MediaRef, keys []string) ([]commonv1.MediaRef, error)
func Bypasses(spec catalogv1alpha1.DelayProfileSpec, atTopTier bool, formatScore int32) bool
func DelayFor(spec catalogv1alpha1.DelayProfileSpec, protocol commonv1.Protocol) time.Duration
func Decide(ctx context.Context, d Deps, profile quality.Profile, delay catalogv1alpha1.DelayProfileSpec, a Approved) error
func NewHandler(d Deps) *Handler
func (h *Handler) Subscription() events.Subscription
func (h *Handler) Handle(ctx context.Context, m events.Message) error
var ErrDuplicateGrab error
var ErrUnsupportedKind error

// catalogarr/controller/wantedcron
package wantedcron

func Backoff(attempts commonv1.Attempts) time.Duration
func NextEligible(attempts commonv1.Attempts) time.Time  // zero Time == eligible now
type Runnable struct{ Client client.Client; Bus events.Bus; Schedule cron.Schedule; Now func() time.Time; Namespaces []string }
func (r *Runnable) Start(ctx context.Context) error
func (r *Runnable) NeedLeaderElection() bool

// catalogarr/worker/rssmatcher
package rssmatcher

func IndexFields(ctx context.Context, mgr ctrl.Manager) error
func Match(ctx context.Context, c client.Client, namespace string, rel schema.Release) ([]commonv1.MediaRef, error)
func NewHandler(d Deps) *Handler   // Deps mirrors grab.Deps plus a DelayProfile resolver func field
func (h *Handler) Subscription() events.Subscription
func (h *Handler) Handle(ctx context.Context, m events.Message) error
```

---

- [ ] **Step 1: `MediaKey` and `StatusTargets` — the shared key vocabulary.**

  Test first (`catalogarr/worker/grab/mediakey_test.go`, package `grab`):
  ```go
  package grab

  import (
  	"errors"
  	"testing"

  	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
  )

  func TestMediaKey(t *testing.T) {
  	got := MediaKey("media", commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "the-thing-1982"})
  	want := "movie/media/the-thing-1982"
  	if got != want {
  		t.Fatalf("MediaKey() = %q, want %q", got, want)
  	}
  }

  func TestStatusTargets(t *testing.T) {
  	cases := []struct {
  		name   string
  		target commonv1.MediaRef
  		keys   []string
  		want   []commonv1.MediaRef
  	}{
  		{
  			name:   "movie singleton",
  			target: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "the-thing-1982"},
  			want:   []commonv1.MediaRef{{Kind: commonv1.MediaKindMovie, Name: "the-thing-1982"}},
  		},
  		{
  			name:   "episode singleton",
  			target: commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: "the-wire-s01e01"},
  			want:   []commonv1.MediaRef{{Kind: commonv1.MediaKindEpisode, Name: "the-wire-s01e01"}},
  		},
  		{
  			name:   "season pack expands to one MediaRef per key",
  			target: commonv1.MediaRef{Kind: commonv1.MediaKindSeries, Name: "the-wire"},
  			keys:   []string{"the-wire-s01e01", "the-wire-s01e02"},
  			want: []commonv1.MediaRef{
  				{Kind: commonv1.MediaKindEpisode, Name: "the-wire-s01e01"},
  				{Kind: commonv1.MediaKindEpisode, Name: "the-wire-s01e02"},
  			},
  		},
  	}
  	for _, c := range cases {
  		t.Run(c.name, func(t *testing.T) {
  			got, err := StatusTargets(c.target, c.keys)
  			if err != nil {
  				t.Fatalf("StatusTargets: %v", err)
  			}
  			if len(got) != len(c.want) {
  				t.Fatalf("got %d targets, want %d: %+v", len(got), len(c.want), got)
  			}
  			for i := range got {
  				if got[i] != c.want[i] {
  					t.Errorf("target %d = %+v, want %+v", i, got[i], c.want[i])
  				}
  			}
  		})
  	}

  	if _, err := StatusTargets(commonv1.MediaRef{Kind: commonv1.MediaKindIssue, Name: "x"}, nil); !errors.Is(err, ErrUnsupportedKind) {
  		t.Errorf("issue target: got err %v, want ErrUnsupportedKind (comic/issue delay-grab is out of scope for M1 — IssueStatus has no PendingGrab field)", err)
  	}
  }
  ```
  Run: `go test ./catalogarr/worker/grab/... -run 'TestMediaKey|TestStatusTargets' -v` — fails (package does not exist).

  Implement `catalogarr/worker/grab/mediakey.go`: `MediaKey` lower-cases `ref.Kind` and joins `kind/namespace/name`. `StatusTargets` switches on `target.Kind`: `MediaKindMovie` or `MediaKindEpisode` with `len(keys)==0` returns `[]commonv1.MediaRef{target}`; `MediaKindSeries` with `keys` returns one `commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: k}` per key; anything else returns `nil, ErrUnsupportedKind` (M1 scope is movie + episode/series only — Design decision context: `IssueStatus` has no `PendingGrab` field, so a delayed grab cannot be represented for comics yet).

  Run again — passes. Commit:
  `git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "feat(grab): media key and pack status-target expansion" -- catalogarr/worker/grab/mediakey.go catalogarr/worker/grab/mediakey_test.go`

- [ ] **Step 2: `Bypasses` and `DelayFor` — the pure DelayProfile evaluation.**

  Test first (`catalogarr/worker/grab/delay_test.go`, package `grab`):
  ```go
  package grab

  import (
  	"testing"
  	"time"

  	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
  	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
  )

  func boolPtr(b bool) *bool { return &b }

  func TestBypasses(t *testing.T) {
  	cases := []struct {
  		name        string
  		spec        catalogv1alpha1.DelayProfileSpec
  		atTopTier   bool
  		formatScore int32
  		want        bool
  	}{
  		{"default profile, top tier bypasses", catalogv1alpha1.DelayProfileSpec{}, true, 0, true},
  		{"default profile, not top tier does not bypass", catalogv1alpha1.DelayProfileSpec{}, false, 999, false},
  		{"BypassIfHighestQuality explicitly off", catalogv1alpha1.DelayProfileSpec{BypassIfHighestQuality: boolPtr(false)}, true, 0, false},
  		{"BypassIfAboveFormatScore at the minimum bypasses", catalogv1alpha1.DelayProfileSpec{
  			BypassIfHighestQuality: boolPtr(false), BypassIfAboveFormatScore: boolPtr(true), MinimumFormatScore: 100,
  		}, false, 100, true},
  		{"BypassIfAboveFormatScore below the minimum does not bypass", catalogv1alpha1.DelayProfileSpec{
  			BypassIfHighestQuality: boolPtr(false), BypassIfAboveFormatScore: boolPtr(true), MinimumFormatScore: 100,
  		}, false, 99, false},
  	}
  	for _, c := range cases {
  		t.Run(c.name, func(t *testing.T) {
  			if got := Bypasses(c.spec, c.atTopTier, c.formatScore); got != c.want {
  				t.Errorf("Bypasses() = %v, want %v", got, c.want)
  			}
  		})
  	}
  }

  func TestDelayFor(t *testing.T) {
  	spec := catalogv1alpha1.DelayProfileSpec{UsenetDelayMinutes: 15, TorrentDelayMinutes: 45}
  	if got := DelayFor(spec, commonv1.ProtocolUsenet); got != 15*time.Minute {
  		t.Errorf("usenet delay = %v, want 15m", got)
  	}
  	if got := DelayFor(spec, commonv1.ProtocolTorrent); got != 45*time.Minute {
  		t.Errorf("torrent delay = %v, want 45m", got)
  	}
  }
  ```
  Run: fails (no `delay.go`).

  Implement `catalogarr/worker/grab/delay.go`. `Bypasses` treats a nil
  `*bool` as its CRD default (`BypassIfHighestQuality` defaults `true`,
  `BypassIfAboveFormatScore` defaults `false` — `catalog/v1alpha1/delayprofile_types.go`'s
  `+kubebuilder:default` markers). `DelayFor` switches on `protocol`, minutes to
  `time.Duration`, unknown protocol returns `0`.

  Run — passes. Commit (path-scoped, `delay.go` + `delay_test.go`).

- [ ] **Step 3: pending-candidate CAS keep-best.**

  Test first (`catalogarr/worker/grab/pending_test.go`, package `grab`):
  ```go
  package grab

  import (
  	"context"
  	"testing"
  	"time"

  	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
  	"github.com/mediactl/clustarr/pkg/events"
  	"github.com/mediactl/clustarr/pkg/events/membus"
  	"github.com/mediactl/clustarr/pkg/quality"
  	"github.com/mediactl/clustarr/pkg/quality/catalogue"
  )

  func hdBlurayWebProfile(t *testing.T) quality.Profile {
  	t.Helper()
  	profiles, errs := quality.BuiltinProfiles(catalogue.LoadedCatalogue())
  	if len(errs) != 0 {
  		t.Fatalf("BuiltinProfiles: %v", errs)
  	}
  	p, ok := profiles["hd-bluray-web"]
  	if !ok || len(p.Tiers) < 3 {
  		t.Fatalf("hd-bluray-web profile missing or has too few tiers: %+v", p)
  	}
  	return p
  }

  func newPendingBus(t *testing.T) events.Bus {
  	t.Helper()
  	bus := membus.New(nil)
  	if err := bus.Ensure(context.Background(), events.Topology{
  		Buckets: []events.BucketSpec{{Name: events.BucketPending, TTL: 7 * 24 * time.Hour}},
  	}); err != nil {
  		t.Fatalf("Ensure: %v", err)
  	}
  	return bus
  }

  func TestCasKeepBest_FirstCandidateCreatesAndStampsFirstSeen(t *testing.T) {
  	ctx := context.Background()
  	bus := newPendingBus(t)
  	profile := hdBlurayWebProfile(t)
  	kv := bus.KV(events.BucketPending)
  	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)

  	candidate := pendingValue{
  		Target:  commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "the-thing-1982"},
  		Release: commonv1.ReleaseInfo{GUID: "guid-1", Quality: profile.Tiers[len(profile.Tiers)-1][0].Quality, FormatScore: 10},
  	}
  	kept, err := casKeepBest(ctx, kv, "grab.movie/media/the-thing-1982", profile, candidate, now)
  	if err != nil {
  		t.Fatalf("casKeepBest: %v", err)
  	}
  	if !kept.FirstSeen.Equal(now) {
  		t.Fatalf("FirstSeen = %v, want %v", kept.FirstSeen, now)
  	}
  }

  func TestCasKeepBest_BetterCandidateReplacesButKeepsFirstSeen(t *testing.T) {
  	ctx := context.Background()
  	bus := newPendingBus(t)
  	profile := hdBlurayWebProfile(t)
  	kv := bus.KV(events.BucketPending)
  	key := "grab.movie/media/the-thing-1982"
  	firstSeen := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)

  	worse := pendingValue{Release: commonv1.ReleaseInfo{GUID: "guid-worse", Quality: profile.Tiers[len(profile.Tiers)-1][0].Quality, FormatScore: 0}}
  	if _, err := casKeepBest(ctx, kv, key, profile, worse, firstSeen); err != nil {
  		t.Fatalf("seed casKeepBest: %v", err)
  	}

  	better := pendingValue{Release: commonv1.ReleaseInfo{GUID: "guid-better", Quality: profile.Tiers[0][0].Quality, FormatScore: 50}}
  	later := firstSeen.Add(10 * time.Minute)
  	kept, err := casKeepBest(ctx, kv, key, profile, better, later)
  	if err != nil {
  		t.Fatalf("casKeepBest: %v", err)
  	}
  	if kept.Release.GUID != "guid-better" {
  		t.Errorf("kept GUID = %q, want guid-better", kept.Release.GUID)
  	}
  	if !kept.FirstSeen.Equal(firstSeen) {
  		t.Errorf("FirstSeen = %v, want the original %v (the delay clock must not reset)", kept.FirstSeen, firstSeen)
  	}
  }

  func TestCasKeepBest_WorseCandidateIsIgnored(t *testing.T) {
  	ctx := context.Background()
  	bus := newPendingBus(t)
  	profile := hdBlurayWebProfile(t)
  	kv := bus.KV(events.BucketPending)
  	key := "grab.movie/media/the-thing-1982"
  	firstSeen := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)

  	best := pendingValue{Release: commonv1.ReleaseInfo{GUID: "guid-best", Quality: profile.Tiers[0][0].Quality, FormatScore: 50}}
  	if _, err := casKeepBest(ctx, kv, key, profile, best, firstSeen); err != nil {
  		t.Fatalf("seed casKeepBest: %v", err)
  	}

  	worse := pendingValue{Release: commonv1.ReleaseInfo{GUID: "guid-worse", Quality: profile.Tiers[len(profile.Tiers)-1][0].Quality, FormatScore: 0}}
  	kept, err := casKeepBest(ctx, kv, key, profile, worse, firstSeen.Add(time.Minute))
  	if err != nil {
  		t.Fatalf("casKeepBest: %v", err)
  	}
  	if kept.Release.GUID != "guid-best" {
  		t.Errorf("kept GUID = %q, want the original guid-best to survive", kept.Release.GUID)
  	}
  }
  ```
  Run: fails (`pendingValue`/`casKeepBest` undefined).

  Implement `catalogarr/worker/grab/pending.go`: `pendingValue{Target
  commonv1.MediaRef; Keys []string; Release commonv1.ReleaseInfo; FirstSeen
  time.Time}` (JSON tags `target`, `keys,omitempty`, `release`, `firstSeen`).
  `casKeepBest(ctx, kv events.KV, key string, profile quality.Profile,
  candidate pendingValue, now time.Time) (pendingValue, error)` loops: `kv.Get`;
  `errors.Is(err, events.ErrKeyNotFound)` → stamp `candidate.FirstSeen = now`,
  `kv.Create` (on `errors.Is(err, events.ErrKeyExists)` loop and retry against
  the winner — do not treat it as a hard error, another goroutine just won the
  same race); on a hit, unmarshal, call `profile.UpgradeDecision(quality.Candidate{...existing}, quality.Candidate{...candidate})`,
  and only on `quality.Upgrade` write `kv.Update` with the **existing**
  `FirstSeen` copied onto the merged value before marshaling (this is what
  keeps the delay's anchor stable — copy it *after* the verdict check, from
  `existing.FirstSeen`, not `candidate.FirstSeen`); on `events.ErrRevisionMismatch`
  loop and retry. Do not pass `events.WithTTL` — `events.Default()`'s
  `BucketPending` spec already carries a 7-day bucket TTL
  (`pkg/events/topology.go:550`).

  Run — passes. Commit.

- [ ] **Step 4: all-or-nothing lease acquisition — the double-grab guard.**

  Test first (`catalogarr/worker/grab/lease_test.go`, package `grab`) — this is
  the "real concurrency test, not a happy-path one" the shared context requires
  for this task (spec §14's "grab lease race, two workers, one Download"):
  ```go
  package grab

  import (
  	"context"
  	"errors"
  	"sync"
  	"testing"

  	"github.com/mediactl/clustarr/pkg/events"
  	"github.com/mediactl/clustarr/pkg/events/membus"
  )

  func newLeaseBus(t *testing.T) events.Bus {
  	t.Helper()
  	bus := membus.New(nil)
  	if err := bus.Ensure(context.Background(), events.Topology{
  		Buckets: []events.BucketSpec{{Name: events.BucketLeases}},
  	}); err != nil {
  		t.Fatalf("Ensure: %v", err)
  	}
  	return bus
  }

  func TestAcquireLeases_AllOrNothingAcrossPackEpisodes(t *testing.T) {
  	ctx := context.Background()
  	kv := newLeaseBus(t).KV(events.BucketLeases)
  	keys := []string{
  		events.LeaseKey("episode/media/the-wire-s01e01"),
  		events.LeaseKey("episode/media/the-wire-s01e02"),
  		events.LeaseKey("episode/media/the-wire-s01e03"),
  	}
  	// Simulate an earlier, unrelated grab already holding the middle episode.
  	if _, err := kv.Create(ctx, keys[1], []byte("some-other-download")); err != nil {
  		t.Fatalf("seed: %v", err)
  	}

  	acquired, err := acquireLeases(ctx, kv, keys, "download-a")
  	if !errors.Is(err, ErrDuplicateGrab) {
  		t.Fatalf("err = %v, want ErrDuplicateGrab", err)
  	}
  	if len(acquired) != 0 {
  		t.Fatalf("acquireLeases must return nothing on partial failure, got %v", acquired)
  	}
  	// The lease it DID manage to take (episode 1, before hitting episode 2)
  	// must have been released, not left dangling.
  	if _, err := kv.Get(ctx, keys[0]); !errors.Is(err, events.ErrKeyNotFound) {
  		t.Fatalf("episode 1's lease should have been rolled back, got err=%v", err)
  	}
  }

  // TestAcquireLeases_TwoWorkersOneDownload is the two-worker race spec §14
  // asks for: two goroutines race to grab the same media key's leases
  // concurrently, and exactly one must win.
  func TestAcquireLeases_TwoWorkersOneDownload(t *testing.T) {
  	ctx := context.Background()
  	kv := newLeaseBus(t).KV(events.BucketLeases)
  	keys := []string{
  		events.LeaseKey("episode/media/the-wire-s01e01"),
  		events.LeaseKey("episode/media/the-wire-s01e02"),
  	}

  	var wg sync.WaitGroup
  	start := make(chan struct{})
  	results := make(chan error, 2)
  	for _, name := range []string{"download-a", "download-b"} {
  		wg.Add(1)
  		go func(downloadName string) {
  			defer wg.Done()
  			<-start
  			_, err := acquireLeases(ctx, kv, keys, downloadName)
  			results <- err
  		}(name)
  	}
  	close(start)
  	wg.Wait()
  	close(results)

  	var wins, dups int
  	for err := range results {
  		switch {
  		case err == nil:
  			wins++
  		case errors.Is(err, ErrDuplicateGrab):
  			dups++
  		default:
  			t.Fatalf("unexpected error: %v", err)
  		}
  	}
  	if wins != 1 || dups != 1 {
  		t.Fatalf("want exactly one winner and one duplicate, got wins=%d dups=%d", wins, dups)
  	}
  	for _, k := range keys {
  		if _, err := kv.Get(ctx, k); err != nil {
  			t.Errorf("lease %s missing after the race: %v", k, err)
  		}
  	}
  }
  ```
  Run: fails (`acquireLeases`/`releaseLeases`/`ErrDuplicateGrab` undefined).

  Implement `catalogarr/worker/grab/lease.go`: `ErrDuplicateGrab =
  errors.New("grab: lease already held")`. `acquireLeases(ctx, kv events.KV,
  keys []string, downloadName string) (acquired []string, err error)` iterates
  `keys`, `kv.Create(ctx, key, []byte(downloadName))`; on
  `errors.Is(err, events.ErrKeyExists)` call `releaseLeases(ctx, kv, acquired)`
  and return `nil, ErrDuplicateGrab`; on any other error, same rollback and
  return the wrapped error (caller retries via `events.Retry`); on success
  append to `acquired`. `releaseLeases` best-effort-deletes every key in
  `acquired`, logging (not failing) individual delete errors.

  Run — passes, including the race test under `-race`:
  `go test ./catalogarr/worker/grab/... -run TestAcquireLeases -race -v`.
  Commit.

- [ ] **Step 5: `kindOps` — the Movie/Episode glue `Decide` and the grab
      handler share.**

  No test yet (pure plumbing exercised by Steps 6-7's envtest suites).
  Implement `catalogarr/worker/grab/kindops.go`:
  ```go
  package grab

  type kindOps interface {
  	get(ctx context.Context, c client.Client, ns, name string) (client.Object, error)
  	activeDownloadRef(obj client.Object) *string
  	qualityProfileRef(ctx context.Context, c client.Client, obj client.Object) (string, error)
  	tags(ctx context.Context, c client.Client, obj client.Object) ([]string, error)
  	delayProfileRef(ctx context.Context, c client.Client, obj client.Object) (*string, error)
  	patchPendingGrab(ctx context.Context, c client.Client, ns, name string, pg *catalogac.PendingGrabApplyConfiguration) error
  	patchActiveDownloadRef(ctx context.Context, c client.Client, ns, name, downloadName string) error
  }
  func kindOpsFor(kind commonv1.MediaKind) (kindOps, error)  // movie, episode; else ErrUnsupportedKind
  ```
  `movieOps` reads `MovieSpec.QualityProfileRef`/`.Tags`/`.DelayProfileRef`
  directly off the fetched `*catalogv1alpha1.Movie`. `episodeOps` fetches the
  `Episode`, then fetches its owning `Series` via `EpisodeSpec.SeriesRef` (same
  namespace) and reads `SeriesSpec.QualityProfileRef`/`.Tags`/`.DelayProfileRef`
  from *that* — Episode's own spec has none of these fields (confirmed in
  `episode_types.go`). Both `patch*` methods call
  `k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrWorker, catalogac.Movie(name,
  ns).WithStatus(catalogac.MovieStatus().With...))` / the `Episode` equivalent,
  setting **only** the one field named by the method (never `WithPhase`, per
  Design decision 1).

- [ ] **Step 6: `performGrab` — leases, optimistic re-read, Download creation,
      status patch, `release.grabbed`.**

  Test first (`catalogarr/worker/grab/perform_envtest_test.go`, package
  `grab_test`, mirroring `pkg/k8s/patch_envtest_test.go`'s `newTestClient`
  helper — skips without `KUBEBUILDER_ASSETS`):
  ```go
  package grab_test

  import (
  	"context"
  	"errors"
  	"os"
  	"testing"
  	"time"

  	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
  	"sigs.k8s.io/controller-runtime/pkg/client"
  	"sigs.k8s.io/controller-runtime/pkg/envtest"

  	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
  	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
  	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
  	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
  	"github.com/mediactl/clustarr/catalogarr/worker/grab"
  	"github.com/mediactl/clustarr/pkg/events"
  	"github.com/mediactl/clustarr/pkg/events/membus"
  	"github.com/mediactl/clustarr/pkg/k8s"
  )

  func newTestClient(t *testing.T) client.Client {
  	t.Helper()
  	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
  		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
  	}
  	env := &envtest.Environment{CRDDirectoryPaths: []string{"../../../config/crd/bases"}, ErrorIfCRDPathMissing: true}
  	cfg, err := env.Start()
  	if err != nil {
  		t.Fatalf("start envtest: %v", err)
  	}
  	t.Cleanup(func() { _ = env.Stop() })
  	c, err := client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
  	if err != nil {
  		t.Fatalf("build client: %v", err)
  	}
  	return c
  }

  // TestPerformGrab_DuplicateIsAckAndStop proves the spec's "on exists -> ack
  // and stop" duplicate-grab guard end to end: seed the lease as if a previous
  // delivery already grabbed, then run performGrab again for the same movie
  // and assert it returns grab.ErrDuplicateGrab (which Handle must turn into a
  // nil/ack, not a retry) and creates no second Download.
  func TestPerformGrab_DuplicateIsAckAndStop(t *testing.T) {
  	ctx := context.Background()
  	c := newTestClient(t)
  	ns := "media"
  	mustCreateNamespace(t, ctx, c, ns)

  	movie := &catalogv1alpha1.Movie{
  		ObjectMeta: metav1.ObjectMeta{Name: "the-thing-1982", Namespace: ns},
  		Spec: catalogv1alpha1.MovieSpec{TmdbID: 1, QualityProfileRef: "hd-bluray-web", RootFolderRef: "movies"},
  	}
  	if err := c.Create(ctx, movie); err != nil {
  		t.Fatalf("create movie: %v", err)
  	}

  	release := commonv1.ReleaseInfo{
  		GUID: "guid-1", IndexerRef: "my-indexer", Protocol: commonv1.ProtocolTorrent,
  		MagnetURL: "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567",
  		Title:     "The.Thing.1982.1080p.BluRay.x264-GROUP",
  	}
  	target := commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: movie.Name}

  	bus := membus.New(nil)
  	if err := bus.Ensure(ctx, events.Topology{
  		Buckets: []events.BucketSpec{{Name: events.BucketLeases}},
  	}); err != nil {
  		t.Fatalf("Ensure: %v", err)
  	}
  	downloadName := "the-thing-1982-preexisting"
  	if _, err := bus.KV(events.BucketLeases).Create(ctx, events.LeaseKey(grab.MediaKey(ns, target)), []byte(downloadName)); err != nil {
  		t.Fatalf("seed lease: %v", err)
  	}

  	deps := grab.Deps{Client: c, Bus: bus, Now: func() time.Time { return time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC) }}
  	err := grab.PerformGrabForTest(ctx, deps, ns, target, nil, release, downloadv1alpha1.GrabSourceSearch)
  	if !errors.Is(err, grab.ErrDuplicateGrab) {
  		t.Fatalf("err = %v, want ErrDuplicateGrab", err)
  	}

  	var downloads downloadv1alpha1.DownloadList
  	if err := c.List(ctx, &downloads, client.InNamespace(ns)); err != nil {
  		t.Fatalf("list downloads: %v", err)
  	}
  	if len(downloads.Items) != 0 {
  		t.Fatalf("performGrab must not create a Download on a duplicate, got %d", len(downloads.Items))
  	}
  }

  // TestPerformGrab_CreatesDownloadAndPatchesStatus is the happy path: a
  // successful grab creates exactly one deterministically-named Download,
  // owned by the target Movie, with source/release/target/qualityProfileRef
  // set, and patches status.activeDownloadRef via ManagerCatalogarrWorker only
  // (never status.phase).
  func TestPerformGrab_CreatesDownloadAndPatchesStatus(t *testing.T) {
  	ctx := context.Background()
  	c := newTestClient(t)
  	ns := "media"
  	mustCreateNamespace(t, ctx, c, ns)

  	movie := &catalogv1alpha1.Movie{
  		ObjectMeta: metav1.ObjectMeta{Name: "the-thing-1982", Namespace: ns},
  		Spec: catalogv1alpha1.MovieSpec{TmdbID: 1, QualityProfileRef: "hd-bluray-web", RootFolderRef: "movies"},
  	}
  	if err := c.Create(ctx, movie); err != nil {
  		t.Fatalf("create movie: %v", err)
  	}
  	indexer := &indexv1alpha1.Indexer{
  		ObjectMeta: metav1.ObjectMeta{Name: "my-indexer", Namespace: ns},
  		Spec:       indexv1alpha1.IndexerSpec{BaseURL: "https://example.invalid"},
  	}
  	if err := c.Create(ctx, indexer); err != nil {
  		t.Fatalf("create indexer: %v", err)
  	}

  	release := commonv1.ReleaseInfo{
  		GUID: "guid-1", IndexerRef: "my-indexer", Protocol: commonv1.ProtocolTorrent,
  		MagnetURL: "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567",
  		Title:     "The.Thing.1982.1080p.BluRay.x264-GROUP",
  	}
  	target := commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: movie.Name}

  	bus := membus.New(nil)
  	if err := bus.Ensure(ctx, events.Default().ForSingleNode()); err != nil {
  		t.Fatalf("Ensure: %v", err)
  	}
  	deps := grab.Deps{Client: c, Bus: bus, Now: time.Now}
  	if err := grab.PerformGrabForTest(ctx, deps, ns, target, nil, release, downloadv1alpha1.GrabSourceSearch); err != nil {
  		t.Fatalf("performGrab: %v", err)
  	}

  	var downloads downloadv1alpha1.DownloadList
  	if err := c.List(ctx, &downloads, client.InNamespace(ns)); err != nil {
  		t.Fatalf("list downloads: %v", err)
  	}
  	if len(downloads.Items) != 1 {
  		t.Fatalf("got %d downloads, want 1: %+v", len(downloads.Items), downloads.Items)
  	}
  	dl := downloads.Items[0]
  	if dl.Spec.Release.GUID != "guid-1" || dl.Spec.QualityProfileRef != "hd-bluray-web" {
  		t.Errorf("download spec = %+v", dl.Spec)
  	}
  	if len(dl.OwnerReferences) != 1 || dl.OwnerReferences[0].Name != movie.Name {
  		t.Errorf("owner references = %+v, want one pointing at %s", dl.OwnerReferences, movie.Name)
  	}

  	var got catalogv1alpha1.Movie
  	if err := c.Get(ctx, client.ObjectKeyFromObject(movie), &got); err != nil {
  		t.Fatalf("get movie: %v", err)
  	}
  	if got.Status.ActiveDownloadRef == nil || *got.Status.ActiveDownloadRef != dl.Name {
  		t.Errorf("status.activeDownloadRef = %v, want %s", got.Status.ActiveDownloadRef, dl.Name)
  	}
  	if got.Status.Phase != "" {
  		t.Errorf("status.phase = %q, want unset — performGrab must never set Phase (Task C6's job)", got.Status.Phase)
  	}
  }

  func mustCreateNamespace(t *testing.T, ctx context.Context, c client.Client, ns string) {
  	t.Helper()
  	// ... create the corev1.Namespace, ignore AlreadyExists, as in pkg/k8s/patch_envtest_test.go
  }
  ```
  (`PerformGrabForTest` is a thin exported wrapper the implementation adds
  purely so this black-box `_test` package can call the unexported
  `performGrab`; keep `performGrab` itself unexported and package-private —
  add the wrapper in a small `export_test.go`-style file, or fold the tests
  into package `grab` directly if that reads cleaner. Either is fine; keep
  `Decide`/`Handle` as the only genuinely public entry points.)

  Run: fails (package/function undefined). Implement
  `catalogarr/worker/grab/perform.go`:
  - `chooseSource(release commonv1.ReleaseInfo, indexerRequiresAuth bool)
    downloadv1alpha1.DownloadSource`: magnet first (never needs indexer auth);
    else, when the indexer needs no auth (`Indexer.Spec.SecretRef == nil`) and a
    `DownloadURL` is set, a direct `TorrentURL`/`NZBURL` by protocol; otherwise
    `IndexerDownload{IndexerRef, GUID, URL}` — matching §8.2's "source
    (indexerDownload when the indexer is authenticated)". When protocol is
    torrent and `release.InfoHash != ""`, also set `ExpectedInfoHash`
    regardless of which branch fired.
  - `performGrab(ctx, d Deps, ns string, target commonv1.MediaRef, keys
    []string, release commonv1.ReleaseInfo, grabbedBy
    downloadv1alpha1.GrabSource) error`:
    1. `statusTargets, err := StatusTargets(target, keys)`.
    2. `downloadName := k8s.ChildName(target.Name, release.GUID)`.
    3. Build `leaseKeys` from `events.LeaseKey(MediaKey(ns, st))` for each
       `st` in `statusTargets`; `acquired, err := acquireLeases(ctx,
       d.Bus.KV(events.BucketLeases), leaseKeys, downloadName)` — on
       `ErrDuplicateGrab` return it unchanged (caller acks and stops).
    4. **Optimistic-lock re-read:** for each `st`, fetch the live object via
       its `kindOps.get`; if `activeDownloadRef(obj)` is non-nil and differs
       from `downloadName`, `releaseLeases` and return `ErrDuplicateGrab` — a
       second, non-lease-mediated path (e.g. a Search-CR manual grab) already
       claimed it.
    5. Fetch the `Indexer` named `release.IndexerRef` in `ns`; on
       `apierrors.IsNotFound` treat as "requires auth" (fail safe, route
       through indexarr) rather than erroring the whole grab.
    6. `src := chooseSource(release, indexer.Spec.SecretRef != nil)`.
    7. Resolve the owner object (the object named by `target` itself —
       `movie`, `series`, or `episode`, via a small `getOwner` switch on
       `target.Kind`) and its `qualityProfileRef` via the first
       `statusTargets[0]`'s `kindOps` (Movie: itself; Episode/pack: the
       Series).
    8. `seed *commonv1.SeedCriteria` from `indexer.Spec.SeedCriteria` when
       non-nil, else leave nil (DownloadClient default applies).
    9. Build `downloadac.Download(downloadName, ns).WithSpec(...)`, owner
       reference via `k8s.OwnerReferenceAC`, and `k8s.Apply(ctx, d.Client,
       k8s.ManagerCatalogarrWorker, ac)` (SSA create-or-update — safe against
       a redelivered work message re-running this step after a Download
       already exists with the same deterministic name).
    10. On any failure from steps 5-9, `releaseLeases(ctx, kv, acquired)` and
        return the error (caller `events.Retry`s).
    11. For each `st`, `kindOps.patchActiveDownloadRef(ctx, d.Client, ns,
        st.Name, downloadName)`. Leases are **not** released on success or on
        a failure past this point — they live until the Download reaches a
        terminal phase (grabarr's job) or the 10-minute sweeper reclaims a
        stale one, per the `clustarr-leases` bucket's documented TTL/lifetime
        (§5).
    12. Publish `schema.ReleaseEvent{Media: target, Action: "grabbed", GUID:
        release.GUID, Indexer: release.IndexerName, DownloadRef: &schema.Ref{
        Namespace: ns, Name: downloadName}, Quality: release.Quality,
        FormatScore: release.FormatScore, At: d.now()}` on
        `events.CatalogReleaseSubject(events.ActionGrabbed,
        string(ownerObj.GetUID()))`, `Envelope.Key = ns + "/" + target.Name`.

  Run — passes:
  `KUBEBUILDER_ASSETS="$(setup-envtest use 1.37.0 -p path)" go test ./catalogarr/worker/grab/... -run TestPerformGrab -v`.
  Commit.

- [ ] **Step 7: `Decide` — bypass-or-delay, wired to Steps 1-6.**

  Test first (`catalogarr/worker/grab/decide_envtest_test.go`, package
  `grab_test`, reusing `newTestClient`): two cases against the same envtest
  apiserver plus a membus with a real `clockwork.NewFakeClock()`:
  1. `TestDecide_BypassGrabsImmediately`: a `DelayProfileSpec{TorrentDelayMinutes:
     45}` with the release at the profile's top tier (`profile.Tiers[0][0].Quality`)
     — `Decide` must create a Download directly (assert via `client.List`) and
     must **not** write `clustarr-pending`.
  2. `TestDecide_DelayedGrabSchedulesAndSetsPendingGrab`: the same profile with
     a release at the bottom tier — `Decide` must not create a Download, must
     PatchStatus `status.pendingGrab` (`ReleaseTitle`, `Protocol`, `GrabAt ==
     now.Add(45*time.Minute)`) via `k8s.ManagerCatalogarrWorker`, and must
     publish a `schema.GrabTask` to `events.WorkGrabSubject(mediaKey)` with
     `WithScheduleAt(now.Add(45m))` — subscribe on the same subject/filter
     `Handler.Subscription()` will use, advance the fake clock by 45 minutes
     from a second goroutine (per `membus.New`'s doc: "a fake clock must be
     advanced from another goroutine" because the subscription loop polls),
     and assert delivery of a `GrabTask` whose `MediaRef == target` on the
     *target* subject, not the scheduling/holding one.
  Both assert the created/patched Movie never has `status.phase` set.

  Run: fails. Implement `catalogarr/worker/grab/decide.go`:
  ```go
  func Decide(ctx context.Context, d Deps, profile quality.Profile, delay catalogv1alpha1.DelayProfileSpec, a Approved) error {
  	now := d.now()
  	idx, ok := profile.Index(a.Release.Quality)
  	atTopTier := ok && idx == 0
  	wait := DelayFor(delay, a.Release.Protocol)
  	if wait <= 0 || Bypasses(delay, atTopTier, a.Release.FormatScore) {
  		return performGrab(ctx, d, a.Namespace, a.Target, a.Keys, a.Release, a.GrabbedBy)
  	}
  	mediaKey := MediaKey(a.Namespace, a.Target)
  	kept, err := casKeepBest(ctx, d.Bus.KV(events.BucketPending), events.PendingKey(mediaKey), profile,
  		pendingValue{Target: a.Target, Keys: a.Keys, Release: a.Release}, now)
  	if err != nil {
  		return fmt.Errorf("grab: pending CAS: %w", err)
  	}
  	grabAt := kept.FirstSeen.Add(wait)
  	task := schema.GrabTask{MediaRef: a.Target, Keys: a.Keys}
  	schemaName, data, err := schema.Encode(task)
  	if err != nil {
  		return err
  	}
  	env := &events.Envelope{
  		Type: "catalog.grab", Schema: schemaName, Source: "catalogarr-worker@v1alpha1",
  		Key: a.Namespace + "/" + a.Target.Name, Time: now, Data: data,
  	}
  	msgID := fmt.Sprintf("%s:%d", mediaKey, kept.FirstSeen.Unix())
  	if _, err := d.Bus.Publish(ctx, events.WorkGrabSubject(mediaKey), env,
  		events.WithScheduleAt(grabAt), events.WithMsgID(msgID)); err != nil {
  		return fmt.Errorf("grab: publish scheduled grab: %w", err)
  	}
  	statusTargets, err := StatusTargets(a.Target, a.Keys)
  	if err != nil {
  		return err
  	}
  	for _, st := range statusTargets {
  		ops, err := kindOpsFor(st.Kind)
  		if err != nil {
  			return err
  		}
  		pg := catalogac.PendingGrab().
  			WithReleaseTitle(kept.Release.Title).
  			WithProtocol(kept.Release.Protocol).
  			WithGrabAt(metav1.NewTime(grabAt))
  		if err := ops.patchPendingGrab(ctx, d.Client, a.Namespace, st.Name, pg); err != nil {
  			return err
  		}
  	}
  	return nil
  }
  ```
  The `<mediaKey>:<firstSeen>` Msg-Id format follows §8.2's literal wording;
  `firstSeen` is serialized as Unix seconds because the spec names the anchor
  without specifying its string form and Unix-seconds is what every other
  `MsgIDFor*` helper in `pkg/events` does with timestamps-as-strings.

  Run — passes. Commit.

- [ ] **Step 8: the grab work-queue handler.**

  Test first (`catalogarr/worker/grab/handler_envtest_test.go`, package
  `grab_test`): publish a `schema.GrabTask` directly to
  `events.WorkGrabSubject(mediaKey)` after seeding `clustarr-pending` with a
  `pendingValue` (simulating Step 7's delayed path having already run), start
  `Handler.Handle` via `bus.Subscribe(ctx, h.Subscription(), h.Handle)`, and
  assert: (a) a Download is created and `status.activeDownloadRef` patched,
  matching Step 6's assertions; (b) the pending KV entry is deleted afterward;
  (c) a second delivery of the identical `GrabTask` (simulating an at-least-once
  redelivery) after the pending entry has been consumed returns `nil` (ack) and
  creates no second Download, because `kv.Get(PendingKey(...))` now returns
  `events.ErrKeyNotFound`.

  Run: fails. Implement `catalogarr/worker/grab/handler.go`:
  ```go
  type Handler struct{ Deps Deps }
  func NewHandler(d Deps) *Handler { return &Handler{Deps: d} }
  func (h *Handler) Subscription() events.Subscription {
  	spec, _ := events.Default().Consumer(events.ConsumerCatalogGrab) // AckWait 60s, MaxDeliver 5, BackOff 10s/1m/5m, MaxAckPending 16 — §5
  	return spec.Subscription()
  }
  func (h *Handler) Handle(ctx context.Context, m events.Message) error {
  	var task schema.GrabTask
  	if err := schema.Decode(m.Envelope().Schema, m.Envelope().Data, &task); err != nil {
  		return events.Discard("grab: malformed GrabTask", err)
  	}
  	ns, _, ok := strings.Cut(m.Envelope().Key, "/")
  	if !ok || ns == "" {
  		return events.Discard("grab: envelope Key missing a namespace", fmt.Errorf("key=%q", m.Envelope().Key))
  	}
  	mediaKey := MediaKey(ns, task.MediaRef)
  	entry, err := h.Deps.Bus.KV(events.BucketPending).Get(ctx, events.PendingKey(mediaKey))
  	if errors.Is(err, events.ErrKeyNotFound) {
  		return nil // already grabbed by an earlier delivery, or bypassed and never went through pending
  	}
  	if err != nil {
  		return events.Retry(10*time.Second, fmt.Errorf("grab: read pending: %w", err))
  	}
  	var pv pendingValue
  	if err := json.Unmarshal(entry.Value, &pv); err != nil {
  		return events.Discard("grab: corrupt pending entry", err)
  	}
  	if err := performGrab(ctx, h.Deps, ns, pv.Target, pv.Keys, pv.Release, downloadv1alpha1.GrabSourceSearch); err != nil {
  		if errors.Is(err, ErrDuplicateGrab) {
  			return nil
  		}
  		return events.Retry(30*time.Second, err)
  	}
  	_ = h.Deps.Bus.KV(events.BucketPending).Delete(ctx, events.PendingKey(mediaKey))
  	return nil
  }
  ```
  Note: `pv.Target`'s `Keys` narrows a pack; the `GrabbedBy` on the redelivery
  path is fixed to `search` here because the RSS matcher's own approved
  candidates go through the *same* `Decide`→pending→`Handle` path with no way
  for `Handle` to distinguish origin from the KV entry alone — if that
  distinction turns out to matter for `evt.catalog.release.grabbed` consumers,
  extend `pendingValue` with a `GrabbedBy` field in a follow-up rather than
  guessing now.

  Run — passes. Commit
  (`catalogarr/worker/grab/handler.go`, `handler_envtest_test.go`).

- [ ] **Step 9: `+kubebuilder:rbac` markers and `catalogarr/run.go` wiring for
      the grab worker.**

  Add above `NewHandler` in `handler.go`:
  ```go
  // +kubebuilder:rbac:groups=catalog.clustarr.io,resources=movies;episodes;series,verbs=get;list;watch
  // +kubebuilder:rbac:groups=catalog.clustarr.io,resources=movies/status;episodes/status,verbs=get;patch
  // +kubebuilder:rbac:groups=download.clustarr.io,resources=downloads,verbs=get;list;watch;create;patch
  // +kubebuilder:rbac:groups=index.clustarr.io,resources=indexers,verbs=get;list;watch
  ```
  In `catalogarr/run.go`'s `setupWorkers` (line 284), inside the existing
  `RoleWorker`-implied path, add:
  ```go
  grabHandler := grab.NewHandler(grab.Deps{Client: mgr.GetClient(), Bus: bus})
  if _, err := bus.Subscribe(context.Background(), grabHandler.Subscription(), grabHandler.Handle); err != nil {
  	return fmt.Errorf("catalogarr: subscribe grab: %w", err)
  }
  ```
  (Match whatever `context.Background()`/manager-lifetime pattern the function
  already uses elsewhere once C8's search worker registration exists beside
  it — do not invent a second convention.) Update the `setupWorkers` doc
  comment's `TODO(M1)` line to drop "grab" from the list.

  Verify: `go build ./catalogarr/...`. Commit (path-scoped to
  `catalogarr/run.go` plus the two files this step touched).

- [ ] **Step 10: wantedcron `Backoff`/`NextEligible` — pure, exported for
      Task C8.**

  Test first (`catalogarr/controller/wantedcron/backoff_test.go`, package
  `wantedcron`):
  ```go
  package wantedcron

  import (
  	"testing"
  	"time"

  	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

  	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
  )

  func TestBackoff(t *testing.T) {
  	cases := []struct {
  		count int32
  		want  time.Duration
  	}{
  		{0, 6 * time.Hour},
  		{1, 6 * time.Hour},
  		{2, 12 * time.Hour},
  		{3, 24 * time.Hour},
  		{4, 48 * time.Hour},
  		{10, 7 * 24 * time.Hour}, // capped
  	}
  	for _, c := range cases {
  		if got := Backoff(commonv1.Attempts{Count: c.count}); got != c.want {
  			t.Errorf("Backoff(count=%d) = %v, want %v", c.count, got, c.want)
  		}
  	}
  }

  func TestNextEligible(t *testing.T) {
  	if got := (NextEligible(commonv1.Attempts{})); !got.IsZero() {
  		t.Errorf("no prior attempt: NextEligible() = %v, want zero (eligible now)", got)
  	}
  	latest := metav1.NewTime(time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC))
  	want := latest.Add(6 * time.Hour)
  	got := NextEligible(commonv1.Attempts{Latest: &latest, Count: 1})
  	if !got.Equal(want) {
  		t.Errorf("NextEligible() = %v, want %v", got, want)
  	}
  }
  ```
  Run: fails. Implement `catalogarr/controller/wantedcron/backoff.go`:
  `Backoff(a commonv1.Attempts)`: `n := a.Count; if n < 1 { n = 1 }`, `d := 6 *
  time.Hour * time.Duration(1<<(n-1))`, cap at `7*24*time.Hour`. `NextEligible`
  returns the zero `time.Time` when `a.Latest == nil` (always eligible), else
  `a.Latest.Add(Backoff(a))`. This is this task's own reading of "6h·2^n capped
  7d" with `n = Count-1` and a `Count ≤ 1` floor at the minimum 6h gap — the
  spec gives the formula, not the base case, so record this choice here rather
  than silently picking one.

  Run — passes. Commit.

- [ ] **Step 11: eligible-namespace sweep — pure function.**

  Test first (`catalogarr/controller/wantedcron/scan_test.go`, package
  `wantedcron`): build a small slice of `catalogv1alpha1.Movie` and
  `catalogv1alpha1.Episode` fixtures across two namespaces with varying
  `Phase`, `Status.LastSearchedAt`, `Status.SearchAttempts`, and assert
  `eligibleNamespaces(movies, episodes, now)` returns exactly the namespaces
  containing at least one item whose `Phase ∈ {Wanted, CutoffUnmet}` and whose
  `NextEligible(item.Status.SearchAttempts)` (falling back to
  `item.Status.LastSearchedAt` when `SearchAttempts.Latest` is nil, so a never-
  searched item is always eligible) is at or before `now`, sorted for
  deterministic output.

  Implement `catalogarr/controller/wantedcron/scan.go`:
  `func eligibleNamespaces(movies []catalogv1alpha1.Movie, episodes
  []catalogv1alpha1.Episode, now time.Time) []string`.

  Run — passes. Commit.

- [ ] **Step 12: the `Runnable` — cron schedule, list, publish `WantedScan`.**

  Test first (`catalogarr/controller/wantedcron/runnable_envtest_test.go`,
  package `wantedcron_test`, using `newTestClient` per Step 6's pattern plus a
  fresh `Movie` in `Phase: Wanted`): call the unexported tick body directly
  (export a small `RunOnceForTest(ctx, r *Runnable) error` or keep `runOnce`
  package-private and test from `package wantedcron` instead — prefer the
  latter to avoid growing the public surface) and assert a `WantedScan` with
  `Namespace == "media"`, `Kinds == [movie, episode]`, `CutoffUnmet == true`
  was published to `events.WorkWantedScanSubject("media")`. Separately, a
  schedule test: `cron.ParseStandard("0 */12 * * *")` (verify via
  `sched.Next(t)` for a couple of known instants — no live cron.New()/goroutine
  needed for this assertion) confirms the parsed schedule fires at hour 0 and
  12 daily.

  Implement `catalogarr/controller/wantedcron/runnable.go`:
  ```go
  type Runnable struct {
  	Client     client.Client
  	Bus        events.Bus
  	Schedule   cron.Schedule // cron.ParseStandard("0 */12 * * *")
  	Now        func() time.Time
  	Namespaces []string // nil = cluster-wide List
  }
  func (r *Runnable) NeedLeaderElection() bool { return true }
  func (r *Runnable) Start(ctx context.Context) error {
  	now := r.now()
  	next := r.Schedule.Next(now)
  	timer := time.NewTimer(next.Sub(now))
  	defer timer.Stop()
  	for {
  		select {
  		case <-ctx.Done():
  			return nil
  		case tick := <-timer.C:
  			func() {
  				defer func() {
  					if rec := recover(); rec != nil {
  						logging.FromContext(ctx).Error("wantedcron: recovered", "panic", rec)
  					}
  				}()
  				if err := r.runOnce(ctx, tick); err != nil {
  					logging.FromContext(ctx).Error("wantedcron: sweep failed", "error", err)
  				}
  			}()
  			timer.Reset(r.Schedule.Next(tick).Sub(tick))
  		}
  	}
  }
  ```
  `runOnce` lists `catalogv1alpha1.MovieList`/`EpisodeList` (namespace-scoped
  per `r.Namespaces` when set), calls `eligibleNamespaces`, and for each
  publishes `schema.WantedScan{Namespace: ns, Kinds: []commonv1.MediaKind{
  commonv1.MediaKindMovie, commonv1.MediaKindEpisode}, CutoffUnmet: true, Epoch:
  tick.Unix()}` to `events.WorkWantedScanSubject(ns)` at low priority, Msg-Id
  `fmt.Sprintf("wantedscan:%s:%d", ns, tick.Unix())`.

  Run — passes. Commit.

- [ ] **Step 13: register `wantedcron` in `catalogarr/run.go`.**

  In `setupControllers` (line 267), replace the stub body:
  ```go
  sched, err := cron.ParseStandard("0 */12 * * *")
  if err != nil {
  	return fmt.Errorf("catalogarr: parse wantedcron schedule: %w", err)
  }
  if err := mgr.Add(&wantedcron.Runnable{Client: mgr.GetClient(), Bus: /* bus is not in scope here today — thread it through Options or move this registration into Run() alongside setupWorkers, whichever keeps setupControllers' existing signature-only-takes-(mgr, o) contract least disrupted */, Schedule: sched, Namespaces: o.WatchNamespaces}); err != nil {
  	return err
  }
  ```
  `setupControllers(mgr, o)` today does not receive `bus` (only `setupWorkers`
  does — see `catalogarr/run.go:284`). Since `wantedcron` needs `events.Bus`,
  either (a) change `setupControllers`' signature to also take `bus
  events.Bus` and update its one call site in `Run` (line ~236), or (b) move
  the `mgr.Add(&wantedcron.Runnable{...})` call into `Run` directly, after both
  `setupControllers` and `setupWorkers`. Prefer (a): it keeps every controller
  registration in one function, matches the doc comment already on
  `setupControllers` ("wantedcron ... lands with M1 as a manager Runnable"),
  and is a two-line change at the single call site. Update `run.go`'s
  `setupControllers` doc comment to remove the forward-reference to this not-
  yet-built code.

  Verify: `go build ./catalogarr/...`. Commit
  (`catalogarr/run.go` scoped to this step's lines).

- [ ] **Step 14: rss-matcher field indices.**

  Test first (`catalogarr/worker/rssmatcher/index_envtest_test.go`, package
  `rssmatcher_test`): build a real `ctrl.NewManager` against the envtest
  config (mirroring `cmd/clustarr/start_envtest_test.go`'s setup — read that
  file for the exact manager-bring-up shape before writing this), call
  `rssmatcher.IndexFields(ctx, mgr)`, start the manager's cache, create a Movie
  with `Spec.TmdbID: 12345` and `Status.Metadata: &MovieMetadata{Title: "The
  Thing", Year: 1982}`, and assert `mgr.GetClient().List(ctx, &list,
  client.MatchingFields{"spec.tmdbID": "12345"})` and `client.MatchingFields{
  "status.normalizedTitleYear": release.Normalize("The Thing") + "|1982"}` each
  return exactly that Movie.

  Implement `catalogarr/worker/rssmatcher/index.go`:
  ```go
  func IndexFields(ctx context.Context, mgr ctrl.Manager) error {
  	idx := mgr.GetFieldIndexer()
  	if err := idx.IndexField(ctx, &catalogv1alpha1.Movie{}, "spec.tmdbID", func(o client.Object) []string {
  		m := o.(*catalogv1alpha1.Movie)
  		return []string{strconv.FormatInt(m.Spec.TmdbID, 10)}
  	}); err != nil {
  		return err
  	}
  	if err := idx.IndexField(ctx, &catalogv1alpha1.Series{}, "spec.tvdbID", func(o client.Object) []string {
  		s := o.(*catalogv1alpha1.Series)
  		return []string{strconv.FormatInt(s.Spec.TvdbID, 10)}
  	}); err != nil {
  		return err
  	}
  	if err := idx.IndexField(ctx, &catalogv1alpha1.Movie{}, "status.normalizedTitleYear", movieTitleYearKeys); err != nil {
  		return err
  	}
  	return idx.IndexField(ctx, &catalogv1alpha1.Series{}, "status.normalizedTitleYear", seriesTitleYearKeys)
  }
  ```
  `movieTitleYearKeys`/`seriesTitleYearKeys` return `nil` when `Status.Metadata
  == nil`, else one `release.Normalize(title) + "|" + strconv.Itoa(int(year))`
  key per non-empty `Title`/`OriginalTitle` (Movie) or `Title` (Series).

  Run — passes. Commit.

- [ ] **Step 15: `Match` — release → monitored items.**

  Test first (`catalogarr/worker/rssmatcher/match_test.go`, package
  `rssmatcher`, using a `sigs.k8s.io/controller-runtime/pkg/client/fake` client
  seeded with a couple of Movies — field-index matching by IDs needs a real
  indexed cache (Step 14's envtest concern), but the *selection logic* given a
  candidate list is pure, so split it: a `matchCandidates(rel schema.Release,
  candidates []catalogv1alpha1.Movie) []commonv1.MediaRef` pure function
  (table-tested here with plain slices, no client at all) for "prefer an
  IDs-based match over a title/year one when both are supplied", called by...
  ):

  Implement `catalogarr/worker/rssmatcher/match.go`:
  ```go
  func Match(ctx context.Context, c client.Client, namespace string, rel schema.Release) ([]commonv1.MediaRef, error) {
  	if id, ok := rel.Info.IDs[commonv1.IDKeyTMDB]; ok && rel.Kind == commonv1.MediaKindMovie {
  		var list catalogv1alpha1.MovieList
  		if err := c.List(ctx, &list, client.InNamespace(namespace), client.MatchingFields{"spec.tmdbID": id}); err != nil {
  			return nil, err
  		}
  		if len(list.Items) > 0 {
  			return refsFromMovies(list.Items), nil
  		}
  	}
  	if id, ok := rel.Info.IDs[commonv1.IDKeyTVDB]; ok && (rel.Kind == commonv1.MediaKindEpisode || rel.Kind == commonv1.MediaKindSeries) {
  		var list catalogv1alpha1.SeriesList
  		if err := c.List(ctx, &list, client.InNamespace(namespace), client.MatchingFields{"spec.tvdbID": id}); err != nil {
  			return nil, err
  		}
  		if len(list.Items) > 0 {
  			return refsFromSeries(list.Items, rel), nil
  		}
  	}
  	// Fallback: normalized title + year.
  	key := release.Normalize(rel.ParsedTitle) + "|" + strconv.Itoa(int(rel.Year))
  	switch rel.Kind {
  	case commonv1.MediaKindMovie:
  		var list catalogv1alpha1.MovieList
  		if err := c.List(ctx, &list, client.InNamespace(namespace), client.MatchingFields{"status.normalizedTitleYear": key}); err != nil {
  			return nil, err
  		}
  		return refsFromMovies(list.Items), nil
  	case commonv1.MediaKindEpisode, commonv1.MediaKindSeries:
  		var list catalogv1alpha1.SeriesList
  		if err := c.List(ctx, &list, client.InNamespace(namespace), client.MatchingFields{"status.normalizedTitleYear": key}); err != nil {
  			return nil, err
  		}
  		return refsFromSeries(list.Items, rel), nil
  	default:
  		return nil, nil // non-video kinds are out of scope for M1
  	}
  }
  ```
  `refsFromSeries` turns a matched `Series` plus `rel.Seasons`/`rel.Episodes`
  into a single `commonv1.MediaRef{Kind: series, Name: series.Name, Keys:
  [...]}` — resolving season/episode numbers to Episode CR names needs the
  naming convention Task C6's Series/Episode controller establishes (Episode
  names are almost certainly `k8s.ChildName`/a deterministic scheme off
  `SeriesRef`+season+episode); `(reconciled by controller — Task C6)`: until
  that naming function is confirmed via `go doc
  ./catalogarr/controller/episode` (or wherever it lands), list Episodes by
  `client.MatchingFields` on `spec.seriesRef`+`spec.seasonNumber`+
  `spec.episodeNumber` instead of guessing a name string — add the matching
  three-field composite index alongside Step 14's if Task C6 has not exported
  a name-building helper by the time you implement this step, and note in your
  own commit message which one you used.

- [ ] **Step 16: the rss-matcher work-queue handler.**

  Test first (`catalogarr/worker/rssmatcher/handler_envtest_test.go`, package
  `rssmatcher_test`): publish a `schema.Release` (payload `Schema() ==
  "index.Release.v1"`) to `events.ReleaseSubject(...)` for a release matching a
  seeded Movie, with the DelayProfile resolver stub
  (`Deps.ResolveDelayProfile`) returning a zero-delay `DelayProfileSpec{}`, and
  assert a Download is created (reusing Step 6's grab-path assertions) — this
  proves the "runs the same evaluation for that single release ... approved one
  takes the same delay/lease/grab path" clause end to end, modulo `decision.Evaluate`
  itself being stubbed (see below).

  Implement `catalogarr/worker/rssmatcher/handler.go`:
  ```go
  type Deps struct {
  	Client             client.Client
  	Bus                events.Bus
  	ResolveDelayProfile func(ctx context.Context, c client.Client, namespace string, pinnedRef *string, itemTags []string) (*catalogv1alpha1.DelayProfile, error)
  	Now                func() time.Time
  }
  type Handler struct{ Deps Deps }
  func NewHandler(d Deps) *Handler { return &Handler{Deps: d} }
  func (h *Handler) Subscription() events.Subscription {
  	spec, _ := events.Default().Consumer(events.ConsumerCatalogRSSMatcher) // AckWait 30s, MaxDeliver 6, BackOff 1s/5s/30s/2m/10m, MaxAckPending 256 — §5
  	return spec.Subscription()
  }
  func (h *Handler) Handle(ctx context.Context, m events.Message) error {
  	var rel schema.Release
  	if err := schema.Decode(m.Envelope().Schema, m.Envelope().Data, &rel); err != nil {
  		return events.Discard("rssmatcher: malformed Release", err)
  	}
  	ns, _, ok := strings.Cut(m.Envelope().Key, "/")
  	if !ok || ns == "" {
  		ns = "default" // (reconciled by controller — indexarr/Task C4/C6): confirm what Key indexarr actually sets on clustarr.rel.> envelopes and drop this fallback if it always carries a namespace.
  	}
  	targets, err := Match(ctx, h.Deps.Client, ns, rel)
  	if err != nil {
  		return events.Retry(5*time.Second, err)
  	}
  	for _, target := range targets {
  		ops, err := grabKindOpsFor(target.Kind) // (reconciled by controller — Task C2/C6): resolve the item's quality.Profile + tags for decision.Evaluate and Decide's profile argument
  		if err != nil {
  			continue
  		}
  		_ = ops
  		// result := decision.Evaluate(ctx, decision.Input{...}, rel)
  		// if result.Approved == nil { continue }
  		// dp, err := h.Deps.ResolveDelayProfile(ctx, h.Deps.Client, ns, delayProfileRef, tags)
  		// if err != nil { return events.Retry(5*time.Second, err) }
  		// if err := grab.Decide(ctx, grab.Deps{Client: h.Deps.Client, Bus: h.Deps.Bus, Now: h.Deps.Now}, profile, dp.Spec, grab.Approved{
  		// 	Namespace: ns, Target: target, Release: *result.Approved, GrabbedBy: downloadv1alpha1.GrabSourceRSS,
  		// }); err != nil { return events.Retry(10*time.Second, err) }
  	}
  	return nil
  }
  ```
  The commented body is deliberate: `decision.Evaluate` (Task C2) is not
  buildable against yet. Before marking this step done, run `go doc
  ./pkg/decision`, replace the comments with real calls against whatever it
  actually exports, and get `TestHandler_MatchedReleaseGrabs` (Step 16's own
  envtest, using a fake/zero-reject `decision.Evaluate` stand-in if C2 truly
  is not merged yet — do not block this task's completion on C2 landing; land
  with the wiring commented and both TODOs pointing at `go doc ./pkg/decision`
  and `go doc ./catalogarr/controller/delayprofile`) green.

  Run — the parts that do not depend on C2/C4 pass; note in your final report
  exactly which assertions you could and could not exercise.

- [ ] **Step 17: register rss-matcher in `catalogarr/run.go`.**

  In `setupWorkers` (line 284), alongside Step 9's grab registration:
  ```go
  if err := rssmatcher.IndexFields(context.Background(), mgr); err != nil {
  	return err
  }
  rssHandler := rssmatcher.NewHandler(rssmatcher.Deps{
  	Client: mgr.GetClient(), Bus: bus, ResolveDelayProfile: delayprofile.Resolve, // fix the import if Task C4's package name differs
  })
  if _, err := bus.Subscribe(context.Background(), rssHandler.Subscription(), rssHandler.Handle); err != nil {
  	return fmt.Errorf("catalogarr: subscribe rss-matcher: %w", err)
  }
  ```
  Update the `setupWorkers` doc comment to drop "rss-matcher" from its
  `TODO(M1)` line.

  Verify: `go build ./catalogarr/...` (this will fail to compile against
  `catalogarr/controller/delayprofile.Resolve` until Task C4 lands — that
  import is exactly the cross-task dependency Design decision 3 calls out; if
  C4 has not merged when you reach this step, leave the wiring in place but
  note the expected compile failure and the exact function it is waiting on
  in your final report rather than stubbing it out silently). Commit
  (`catalogarr/run.go` scoped to this step's lines, plus
  `catalogarr/worker/rssmatcher/handler.go`).

---

**Verification:**
```bash
cd /home/appkins/src/mediactl/clustarr
go build ./catalogarr/... ./pkg/...
go vet ./catalogarr/worker/grab/... ./catalogarr/worker/rssmatcher/... ./catalogarr/controller/wantedcron/...
go test ./catalogarr/worker/grab/... ./catalogarr/controller/wantedcron/... -race -v
KUBEBUILDER_ASSETS="$(setup-envtest use 1.37.0 -p path)" \
  go test ./catalogarr/worker/grab/... ./catalogarr/worker/rssmatcher/... ./catalogarr/controller/wantedcron/... -race -v
make lint
```
The second `go test` run is not optional: every `*_envtest_test.go` file in
this task (Steps 6-9, 14-17) skips without `KUBEBUILDER_ASSETS`, and a run
that finishes in milliseconds is a skip, not a pass — confirm the `-v` output
shows `--- PASS`, not `--- SKIP`, for each of those tests before treating this
task as done.

**Done when:**
- [ ] `MediaKey`, `StatusTargets`, `Bypasses`, `DelayFor` are pure, table-tested,
      and `StatusTargets` rejects non-movie/episode kinds with `ErrUnsupportedKind`.
- [ ] `casKeepBest` preserves `FirstSeen` across a keep-best replacement and
      never regresses to a worse candidate; proven with a real `quality.Profile`
      (`hd-bluray-web`), not hand-picked fake quality values.
- [ ] `acquireLeases`/`releaseLeases` are all-or-nothing and
      `TestAcquireLeases_TwoWorkersOneDownload` — two real goroutines racing
      over the same lease keys — passes under `-race` with exactly one winner.
- [ ] `performGrab` creates a deterministically-named, owner-referenced Download,
      patches only `status.activeDownloadRef`/`status.pendingGrab` (never
      `status.phase`) via `k8s.ManagerCatalogarrWorker`, and the duplicate-grab
      path both from a pre-held lease (`performGrab` itself) and from a
      redelivered work message after the pending entry is already consumed
      (`Handler.Handle`) is ack-and-stop, not a retry or a second Download.
- [ ] `Decide` bypasses correctly at the top tier / above the minimum format
      score, and otherwise CAS-keep-bests into `clustarr-pending`, schedules a
      `GrabTask` at `firstSeen + delay` with Msg-Id `<mediaKey>:<firstSeen-unix>`,
      and patches `status.pendingGrab` — verified against `membus`'s fake clock,
      not a real 45-minute sleep.
- [ ] `wantedcron.Backoff`/`NextEligible` are pure and table-tested against the
      documented `6h·2^n capped 7d` cases; the `Runnable` is registered as a
      leader-elected `manager.Runnable` (not a `reconcile.Reconciler`) and fires
      `WantedScan` only for namespaces with an eligible item.
- [ ] rss-matcher's field indices resolve a release by `tmdb`/`tvdb` id first,
      falling back to normalized title+year, over a real indexed
      controller-runtime cache (envtest), and `Match`'s pure candidate-selection
      logic is separately unit-tested.
- [ ] `catalogarr/run.go`'s `setupControllers`/`setupWorkers` register
      wantedcron, the grab handler and the rss-matcher handler; `go build
      ./catalogarr/...` succeeds except for the one documented cross-task
      dependency on Task C4's `delayprofile.Resolve` if C4 has not yet landed —
      in which case the exact failing import is named in the final report, not
      silently worked around.
- [ ] Every place this task assumed another Phase C task's API
      (`pkg/decision.Evaluate`/`Rank`, `catalogarr/controller/delayprofile.Resolve`,
      Episode naming for season-pack matching) is marked
      `(reconciled by controller — Task Cn)` in code comments at the call site,
      not just in this plan.
- [ ] `go test ./catalogarr/worker/grab/... ./catalogarr/worker/rssmatcher/...
      ./catalogarr/controller/wantedcron/... -race` is green both with and
      without `KUBEBUILDER_ASSETS`, and `make lint` passes (no `forbidigo`
      hits — every status write goes through `k8s.PatchStatus`/`k8s.Apply`).

---

### Task C10: importarr's LibraryScan controller, the RootFolder scan schedule, the rescan worker, and the ImportExclusion controller

**Scope corrections from the plan controller (mid-task, both addressed below):**

1. `ImportExclusion` belongs to `importarr`, not `catalogarr` — the catalogarr configuration-controllers task's brief wrongly assigned it. Amendment §A1.3's ownership table, `pkg/k8s.ManagerImportarr`'s own doc comment ("owns ImportList, ImportExclusion and LibraryScan status") and `catalogarr/run.go`'s `setupControllers` comment agree. It is added here as a fourth, self-contained component.
2. **`MediaFile.status.file` and `status.probe` do not exist.** The original brief (and CLAUDE.md's invariant paragraph, and amendment §A1.3) describe importarr owning those two status groups. `api/catalog/v1alpha1/mediafile_types.go` has no such fields: `MediaFileStatus` is flat (`ObservedGeneration`, `Conditions`, `ProbeHash`, `ProbedAt`, `MediaInfo`, `Sidecars`, `Transcode`), and the six "decided" fields (`Quality`, `Revision`, `ReleaseType`, `ReleaseGroup`... `FormatScore`, `MatchedFormats`, `ProfileHash`) are all on **`MediaFileSpec`**, frozen at import — matching spec §8.4's own words ("quality/revision/formatScore/matchedFormats/releaseType frozen"). The real split, confirmed against the generated type: **importarr creates the `MediaFile` and owns all of `MediaFileSpec`** (the observed facts plus the release identity frozen from what `pkg/release` parsed); **catalogarr (Task C7) is the sole writer of all of `MediaFileStatus`** (it probes, sets `probeHash`/`mediaInfo`, mirrors sidecars and transcode state), and it additionally takes over three spec fields — `sizeBytes`, `modTime`, `original` — but only *after* it incorporates a transcode swap (spec §8.5). This task is rewritten below to write spec only, never status, and never call `pkg/mediainfo.Probe` (there is nowhere on `MediaFileSpec` for a probe result to go — probing is entirely catalogarr's job now). The field manager for every rescan-worker write to `Movie` and `MediaFile` is `k8s.ManagerImportarr`, per the controller's explicit instruction; `k8s.ManagerImportarrWorker`'s doc comment in `pkg/k8s/fieldmanager.go` ("applies `status.file` and `status.probe` only") describes the same non-existent fields and is stale — this task does not use it, and flags the comment for correction outside this task's path ownership.

**Files:**
- Create: `importarr/controller/libraryscan/libraryscan_controller.go`
- Create: `importarr/controller/libraryscan/libraryscan_controller_envtest_test.go`
- Create: `importarr/controller/rootfolderschedule/rootfolderschedule_controller.go`
- Create: `importarr/controller/rootfolderschedule/rootfolderschedule_controller_envtest_test.go`
- Create: `importarr/controller/importexclusion/importexclusion_controller.go`
- Create: `importarr/controller/importexclusion/importexclusion_controller_envtest_test.go`
- Create: `importarr/worker/rescan/match.go`
- Create: `importarr/worker/rescan/match_test.go`
- Create: `importarr/worker/rescan/progress.go`
- Create: `importarr/worker/rescan/progress_test.go`
- Create: `importarr/worker/rescan/worker.go`
- Create: `importarr/worker/rescan/mediafile.go`
- Create: `importarr/worker/rescan/index.go`
- Create: `importarr/worker/rescan/worker_envtest_test.go`
- Create: `pkg/events/schema/importarr.go`
- Create: `pkg/events/exclusion.go`
- Modify: `pkg/events/schema/schema_test.go` (append `schema.ScanTask{}` to `allPayloads()`)
- Modify: `pkg/events/subjects.go` (add `BucketImportExclusions` to the bucket-name const block)
- Modify: `pkg/events/topology.go` (register `BucketImportExclusions` in `defaultBuckets()`)
- Modify: `importarr/run.go` (`setupControllers`, `setupWorkers`, and the one call site of `setupControllers` in `Run`)

**Path ownership:** `importarr/controller/libraryscan/`, `importarr/controller/rootfolderschedule/`, `importarr/controller/importexclusion/`, `importarr/worker/rescan/`, plus `importarr/run.go` and four small, additive touches to shared bus infrastructure: the new files `pkg/events/schema/importarr.go` and `pkg/events/exclusion.go`, one appended line in `pkg/events/schema/schema_test.go`, one constant in `pkg/events/subjects.go`, and one entry in `pkg/events/topology.go`'s `defaultBuckets()`. No other Phase C task (C0–C2, C4–C9, C11) works inside `importarr/` or touches `pkg/events`'s payload/bucket vocabulary — the eight sibling tasks are catalogarr/indexarr/e2e scoped and every schema type or bucket they need already exists from Phase A. This is the only Phase C task that reaches into `pkg/events`.

**Read first:**
- Amendment §A1 in full (`docs/superpowers/specs/2026-09-18-clustarr-design-amendment-1.md` lines 19–163): §A1.3 the ownership table, §A1.4 the `LibraryScan` kind, §A1.5 matching files to items (the never-guess rule), §A1.6 process topology (work-queue subjects, the "chunked by directory... one message is not a multi-hour unit of work" line).
- Spec §8.4 (import: "quality/revision/formatScore/matchedFormats/releaseType frozen" — the exact words that ground correction 2 above), §8.5 (transcode: "catalogarr MediaFile reconciler... sets `spec.sizeBytes/modTime`, `Original=false`, re-probes... bumps `probeHash`" — the exact words that ground the post-transcode field handoff this task's worker must not fight), §11 (the `/data` layout, for vocabulary), §8.8 (failure handling: `RequeueAfter` only, `Retry`/`Discard`, heartbeats).
- `docs/research/naming.md` §A2 (Radarr movie folder/file tokens) and §A3 (the Jellyfin/Plex/Emby provider-id bracket/brace table `pkg/release.ParsePath` already implements).
- `docs/research/k8s.md` §5 (Reconcile skeleton rules, `RequeueAfter`, `reconcile.TerminalError`, `mgr.GetEventRecorder`) and §8 (envtest pattern).
- The real generated types, which is where this task's design corrects both the amendment's pseudocode and the original brief: `api/catalog/v1alpha1/libraryscan_types.go`, `api/catalog/v1alpha1/rootfolder_types.go`, `api/catalog/v1alpha1/mediafile_types.go` (read this one especially carefully — see correction 2), `api/catalog/v1alpha1/movie_types.go`, `api/catalog/v1alpha1/importexclusion_types.go`, `api/common/v1alpha1/media_types.go`.
- `pkg/k8s/fieldmanager.go` in full — `ManagerImportarr`'s doc comment ("owns ImportList, ImportExclusion and LibraryScan status") is accurate and this task follows it for every controller AND for the rescan worker's writes (see correction 2); `ManagerImportarrWorker`'s doc comment is stale (references the non-existent status split) and is not used by this task.
- `pkg/events/topology.go` lines ~460–495 (the `importarr` consumer block: `ConsumerImportScan` AckWait 60s/MaxDeliver 4/BackOff 30s,2m,10m/MaxAckPending 4/Heartbeat 30s, and the comment above it about in-progress acks) and ~538–560 (`defaultBuckets()`, the `b(name, ttl, desc)` helper this task's new `BucketImportExclusions` entry follows) and `pkg/events/subjects.go` (`WorkScanSubject`, `StreamWorkImportarr`, `FilterImportScan`, `LeaseKey`/`PendingKey` — the existing precedent this task's `ExclusionKey` follows).
- `go doc ./pkg/fsops`, `go doc ./pkg/release`, `go doc ./pkg/events`, `go doc ./pkg/k8s` — all read in full for this task; signatures used below are exact. (`pkg/mediainfo` is **not** consumed by this task per correction 2 — do not add it as a dependency of `importarr/worker/rescan`.)
- `pkg/k8s/patch_envtest_test.go` — the `newTestClient(t)` helper (envtest + `KUBEBUILDER_ASSETS` skip + `config/crd/bases`) is the established pattern every envtest suite in this repo follows; mirror it rather than inventing a new one.
- `importarr/run.go` in full, especially the `setupControllers`/`setupWorkers` TODO comments.

**Dependencies:** `github.com/robfig/cron/v3` at **v3.0.1** — already verified and pre-added to `go.mod` by Task C0 (`/tmp/.../phase-c/deps-verified.txt`: `github.com/robfig/cron/v3@v3.0.1`), which runs and commits before any of C1–C11 are dispatched. **Do not `go get` it.** No other new dependency — everything else (`sigs.k8s.io/controller-runtime`, `k8s.io/apimachinery`, the `api/applyconfiguration/...` packages, `pkg/fsops`, `pkg/release`, `pkg/events`, `pkg/k8s`) is already in `go.mod` from Phases A/B. `pkg/mediainfo` is explicitly **not** a dependency of this task (correction 2).

**Interfaces — Consumes** (exact signatures, from `go doc` and the generated types):

```go
// pkg/fsops
func Walk(ctx context.Context, root string, fn func(path string, info os.FileInfo, class FileClass) error) error
type FileClass int
const ( ClassMedia FileClass = iota; ClassSample; ClassExtra; ClassPart; ClassOther )

// pkg/release
func ParsePath(path string, o Options) (*ParsedRelease, error)
func CleanTitle(title string) string
type Options struct { Kind commonv1.MediaKind; SeriesType string }
type ParsedRelease struct {
    Title string; Year int; IDs map[string]string // keys: "tmdb", "imdb", "tvdb"
    Quality commonv1.Quality; Revision commonv1.Revision; ReleaseType commonv1.ReleaseType
    Group string; Edition string; Languages []string
    // ... (Music/Book/Comic/Hints — not used by this task)
}

// pkg/events
const StreamWorkImportarr = "CLUSTARR_WORK_IMPORTARR"
const ConsumerImportScan  = "importarr-scan"
const RPCMetadataResolve  = "clustarr.rpc.catalogarr.metadata.resolve"
func WorkScanSubject(rootFolder string) string
func MsgIDForObject(uid string, generation int64, task string) string
func Default() Topology
func (t Topology) Consumer(name string) (ConsumerSpec, bool)
func (c ConsumerSpec) Subscription() Subscription
type Bus interface {
    Publish(ctx context.Context, subject string, e *Envelope, opts ...PublishOption) (Receipt, error)
    Subscribe(ctx context.Context, s Subscription, h Handler) (stop func(), err error)
    Request(ctx context.Context, subject string, in, out any) error
    KV(bucket string) KV
    Ensure(ctx context.Context, t Topology) error
}
type KV interface {
    Get(ctx context.Context, key string) (Entry, error)
    Put(ctx context.Context, key string, val []byte) (uint64, error)
    Delete(ctx context.Context, key string) error
    // Create/Update/Watch also exist; not needed here
}
var ErrKeyNotFound = errors.New("events: key not found")
type WorkQueue interface {
    Ack(ctx context.Context) error
    Nak(ctx context.Context, delay time.Duration) error
    Term(ctx context.Context, reason string) error
    InProgress(ctx context.Context) error // the heartbeat topology.go's comment requires for a long walk
}
type Message interface { WorkQueue; Envelope() *Envelope; Subject() string; Attempt() uint64 }
type Handler func(ctx context.Context, m Message) error
func Retry(after time.Duration, err error) error
func Discard(reason string, err error) error

// pkg/events/schema
type Payload interface { Schema() string }
type Ref struct { Namespace, Name, UID string }
func Encode(p Payload) (schema string, data []byte, err error)
func Decode(schema string, data []byte, out Payload) error
type MetadataRequest struct { Kind commonv1.MediaKind; IDs map[string]string; Text string; Year int32; Region, Language string }
type MetadataResponse struct { Kind commonv1.MediaKind; Provider string; IDs map[string]string; Result []byte; Results [][]byte; CachedAt *time.Time; Error string }

// pkg/k8s
func Apply[T ApplyConfiguration](ctx context.Context, c client.Client, fm FieldManager, ac T, opts ...client.ApplyOption) (T, error)
func PatchStatus[T ApplyConfiguration](ctx context.Context, c client.Client, fm FieldManager, ac T, opts ...client.SubResourceApplyOption) (T, error)
const ManagerImportarr FieldManager = "importarr" // every controller AND the rescan worker's Movie/MediaFile writes (correction 2)
func ChildName(target string, parts ...string) string
func DeterministicName(target string, maxLen int, parts ...string) string
const MaxNameLength = 253
func ConditionAC(c metav1.Condition) *metav1ac.ConditionApplyConfiguration
func ConditionACs(conditions []metav1.Condition) []*metav1ac.ConditionApplyConfiguration
func MarkFalse/MarkTrue/MarkReady(...) bool
func IsConditionTrue(conditions []metav1.Condition, condType string) bool
func IsDeleting(obj client.Object) bool
func HasFinalizer(obj client.Object, name string) bool
func EnsureFinalizer(ctx context.Context, c client.Client, obj client.Object, name string) (bool, error)
func RemoveFinalizer(ctx context.Context, c client.Client, obj client.Object, name string) (bool, error)

// api/applyconfiguration/catalog/catalog/v1alpha1 (import alias catalogac; NOTE the doubled
// "catalog/catalog" path segment — controller-gen v0.22.0's applyconfiguration generator
// ignores output:dir, CLAUDE.md's documented gotcha)
func LibraryScan(name, namespace string) *LibraryScanApplyConfiguration
func LibraryScanSpec() *LibraryScanSpecApplyConfiguration
func LibraryScanStatus() *LibraryScanStatusApplyConfiguration
func Movie(name, namespace string) *MovieApplyConfiguration
func MovieSpec() *MovieSpecApplyConfiguration
func MovieAddOptions() *MovieAddOptionsApplyConfiguration
func MediaFile(name, namespace string) *MediaFileApplyConfiguration
func MediaFileSpec() *MediaFileSpecApplyConfiguration
func UnmatchedFile() *UnmatchedFileApplyConfiguration
func ImportExclusion(name, namespace string) *ImportExclusionApplyConfiguration
func ImportExclusionStatus() *ImportExclusionStatusApplyConfiguration
// (b *LibraryScanStatusApplyConfiguration) WithPhase/WithStartedAt/WithFinishedAt/
//   WithFilesSeen/WithFilesMatched/WithItemsCreated/WithItemsUpdated/WithFilesSkipped/
//   WithUnmatched(...*UnmatchedFileApplyConfiguration)/WithConditions(...*metav1.ConditionApplyConfiguration)
// (b *MovieSpecApplyConfiguration) WithTmdbID(int64)/WithQualityProfileRef(string)/
//   WithRootFolderRef(string)/WithAddOptions(*MovieAddOptionsApplyConfiguration)
// (b *MovieAddOptionsApplyConfiguration) WithSearchForMovie(bool)/WithAddMethod(catalogv1alpha1.MovieAddMethod)
// (b *MediaFileSpecApplyConfiguration) WithMediaRef(commonv1alpha1.MediaRef)/WithPath(string)/
//   WithSizeBytes(int64)/WithModTime(metav1.Time)/WithQuality(commonv1alpha1.Quality)/
//   WithRevision(commonv1alpha1.Revision)/WithReleaseType(commonv1alpha1.ReleaseType)/
//   WithReleaseGroup(string)/WithEdition(string)/WithLanguages(...string)
//   (FormatScore/MatchedFormats/ProfileHash setters exist but are deliberately not called — see Step 10)
// (b *ImportExclusionStatusApplyConfiguration) WithObservedGeneration(int64)/WithConditions(...)/
//   WithMatchCount(int32)/WithLastMatchedAt(metav1.Time)

// api/catalog/v1alpha1 (import alias catalogv1alpha1)
type LibraryScan struct { Spec LibraryScanSpec; Status LibraryScanStatus }
type LibraryScanSpec struct {
    RootFolderRef string; Mode ScanMode; Subpath string; DryRun bool
    TTLSecondsAfterFinished *int32
}
type LibraryScanStatus struct {
    Phase ScanPhase; StartedAt, FinishedAt *metav1.Time
    FilesSeen, FilesMatched, ItemsCreated, ItemsUpdated, FilesSkipped int64
    Unmatched []UnmatchedFile // MaxItems=200, plain list (NOT listType=map)
    Conditions []metav1.Condition
}
type UnmatchedFile struct { Path, Reason string; Candidates []string; SeenAt metav1.Time }
const ScanPhasePending, ScanPhaseRunning, ScanPhaseCompleted, ScanPhaseFailed ScanPhase
const ScanModeFull, ScanModeIncremental ScanMode
type RootFolder struct { Spec RootFolderSpec; Status RootFolderStatus }
type RootFolderSpec struct {
    Path string; Kind RootFolderKind; Defaults RootDefaults; ScanSchedule string // cron expression
}
type RootDefaults struct { QualityProfileRef string /* +optional */; ... }
const RootFolderConditionReady = "Ready"
const RootFolderKindMovie RootFolderKind = "movie"
type MediaFile struct { Spec MediaFileSpec; Status MediaFileStatus }
type MediaFileSpec struct { // ALL of this is importarr's, per correction 2
    MediaRef commonv1.MediaRef; Path string; SizeBytes int64; ModTime metav1.Time
    Quality commonv1.Quality; Revision commonv1.Revision; ReleaseType commonv1.ReleaseType
    ReleaseGroup, Edition string; Languages []string
    FormatScore int32; MatchedFormats []string; ProfileHash string // left zero by this task, see Step 10
    ImportedFrom *ImportSource // nil for a rescan-created file — no Download behind it
    Original *bool // +kubebuilder:default=true; catalogarr takes this over post-transcode
}
type MediaFileStatus struct { // ALL of this is catalogarr's (Task C7), never written here
    ObservedGeneration int64; Conditions []metav1.Condition
    ProbeHash string; ProbedAt *metav1.Time; MediaInfo *commonv1.MediaInfo
    Sidecars []Sidecar; Transcode *TranscodeState
}
type MovieSpec struct { TmdbID int64 /* required, immutable */; QualityProfileRef, RootFolderRef string /* required */; AddOptions MovieAddOptions; ... }
type MovieAddOptions struct { SearchForMovie *bool; AddMethod MovieAddMethod }
const MovieAddMethodManual MovieAddMethod = "manual" // closest fit — see disagreement note
type MovieStatus struct { Metadata *MovieMetadata; ... }
type MovieMetadata struct { Title string; Year int32; ... }
type ImportExclusion struct { Spec ImportExclusionSpec; Status ImportExclusionStatus }
type ImportExclusionSpec struct {
    Kind ExclusionKind; ExternalIDs map[string]string /* +required, MaxProperties=16, CEL size(self)>0 */
    Title string; Year int32; Reason string
}
type ImportExclusionStatus struct {
    ObservedGeneration int64; Conditions []metav1.Condition
    MatchCount int32; LastMatchedAt *metav1.Time
}
const ImportExclusionConditionReady = "Ready"
const ( ExclusionIDKeyTMDB = "tmdb"; ExclusionIDKeyTVDB = "tvdb"; ExclusionIDKeyIMDB = "imdb"
        ExclusionIDKeyMusicBrainz = "musicbrainz"; ExclusionIDKeyOpenLibrary = "openlibrary"
        ExclusionIDKeyASIN = "asin"; ExclusionIDKeyComicVine = "comicvine"; ExclusionIDKeyMangaDex = "mangadex" )

// api/common/v1alpha1
type MediaRef struct { Kind MediaKind; Name string; Keys []string }
const MediaKindMovie MediaKind = "movie"
```

**Interfaces — Produces:**

```go
// pkg/events/schema
type ScanTask struct {
    LibraryScanRef Ref    `json:"libraryScanRef"`
    RootFolderRef  Ref    `json:"rootFolderRef"`
    Path           string `json:"path"`   // absolute: RootFolder.Spec.Path joined with LibraryScan.Spec.Subpath
    Mode           string `json:"mode"`   // "full" | "incremental" — plain string, not catalogv1alpha1.ScanMode:
                                           // schema payloads "never embed custom resources" (schema.go's own doc)
    DryRun         bool   `json:"dryRun,omitempty"`
}
func (ScanTask) Schema() string // "importarr.ScanTask.v1"

// pkg/events (new file exclusion.go — the cross-service lookup contract Phase D's
// import-list worker and Phase G's search path build against; both consume this
// directly with no dependency on importarr's controller package)
const BucketImportExclusions = "clustarr-import-exclusions"
func ExclusionKey(idKey, idValue string) string // "tmdb:949" — matches LeaseKey/PendingKey's convention
type ExclusionEntry struct {
    Namespace, Name string // the ImportExclusion object, for a caller that wants to report which one matched
    Kind            string // catalogv1alpha1.ExclusionKind as a string — plain data, no CRD import
    Reason          string
}
func (e ExclusionEntry) Encode() ([]byte, error)
func DecodeExclusionEntry(data []byte) (ExclusionEntry, error)
// Contract for (reconciled by a later task) consumers: for each external id a
// candidate carries (tmdb, imdb, tvdb, ...), call
// bus.KV(events.BucketImportExclusions).Get(ctx, events.ExclusionKey(key, value));
// events.ErrKeyNotFound means not excluded; any other successful Get means excluded,
// and DecodeExclusionEntry(entry.Value) gives the reason to surface to the user.

// importarr/worker/rescan
type Progress struct {
    Done bool; Error string
    FilesSeen, FilesMatched, ItemsCreated, ItemsUpdated, FilesSkipped int64
    Unmatched []UnmatchedFile
}
type UnmatchedFile struct { Path, Reason string; Candidates []string; SeenAt time.Time }
func (p Progress) Encode() ([]byte, error)
func DecodeProgress(data []byte) (Progress, error)
func ProgressKey(scanUID string) string // "scan.<uid>" inside events.BucketProgress

type MovieCandidate struct { Name string; TmdbID int64; Title string; Year int }
type ResolveIMDb func(imdbID string) (tmdbID int64, err error)
type MatchResult struct {
    TmdbID       int64  // resolved identity, set whenever IDs/title matching succeeded
    ExistingName string // non-empty: matched an existing Movie; empty + !Unmatched: caller must create one
    Unmatched    bool
    Reason       string
    Candidates   []string
}
func MatchMovie(parsed *release.ParsedRelease, existing []MovieCandidate, resolve ResolveIMDb) MatchResult // pure

type Worker struct { /* Client client.Client; Bus events.Bus; Clock func() time.Time; MetadataTimeout time.Duration */ }
func NewWorker(c client.Client, bus events.Bus) *Worker
func (w *Worker) Handle(ctx context.Context, m events.Message) error // satisfies events.Handler

const MediaFilePathIndexKey = "spec.path"
func IndexMediaFileByPath(ctx context.Context, mgr ctrl.Manager) error

// importarr/controller/libraryscan
type Reconciler struct { /* Client client.Client; Bus events.Bus; Clock func() time.Time */ }
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error

// importarr/controller/rootfolderschedule
const LabelRootFolder = "catalog.clustarr.io/root-folder"
type Reconciler struct { /* Client client.Client; Recorder record.EventRecorder; Clock func() time.Time */ }
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error

// importarr/controller/importexclusion
type Reconciler struct { /* Client client.Client; Bus events.Bus; Clock func() time.Time */ }
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error
```

Nothing here is consumed by another Phase C task (C2/C4–C9/C11 are catalogarr/indexarr/e2e scoped). `pkg/events.BucketImportExclusions`/`ExclusionKey`/`ExclusionEntry` are the explicit handoff for Phase D (import-list sync) and Phase G (search) — both `(reconciled by a later task)`, consuming the KV contract directly, not this task's controller package. The deferred completed-download-import worker (`importarr/worker/fileimport`, amendment §A1.6, M3, not in this Phase C batch) will also need to write `MediaFileSpec` — it is `(reconciled by a later task)` and must follow the same "spec only, `k8s.ManagerImportarr`" rule this task establishes.

---

### Where this disagrees with the brief, the amendment, or a research note — and what this task follows

1. **`MediaFile.status.file`/`status.probe` do not exist — see the scope-correction box at the top.** This is the load-bearing correction for the whole rescan worker: it writes `MediaFileSpec` only, through `k8s.Apply` under `k8s.ManagerImportarr`, and never calls `PatchStatus` or `pkg/mediainfo.Probe` for a `MediaFile`.

2. **`LibraryScanSpec.RootFolderRef` is a plain `string`, not `commonv1.LocalRef`.** The amendment's §A1.4 pseudocode says `commonv1.LocalRef`; the generated type's own doc comment overrides this explicitly ("reserved... for references to CORE objects"). This task follows the generated `string` field.

3. **`CLUSTARR_WORK_IMPORTARR` is not in spec §5's stream table.** The base design's §5 table (pre-amendment) lists only `WORK_CATALOGARR`/`WORK_INDEXARR`/`WORK_CAPTIONARR`. `pkg/events` (already merged, Phase A) already defines `StreamWorkImportarr`, `FilterImportScan/List/File`, `ConsumerImportScan/List/File` and a fully-tuned `ConsumerSpec` for each in `topology.go`'s `Default()`. This task follows the generated/implemented topology and uses `events.Default().Consumer(events.ConsumerImportScan).Subscription()` rather than inventing AckWait/MaxDeliver/BackOff numbers.

4. **No literal per-directory chunking of one `LibraryScan` into many `ScanTask` messages.** §A1.6 says "a scan of a large library is chunked by directory," and `ConsumerImportScan`'s `MaxAckPending: 4` is sized for concurrency. This task publishes **exactly one `ScanTask` per `LibraryScan`** instead. Reasons: (a) `LibraryScanStatus.Unmatched` has no `+listType=map`/`+listMapKey` — concurrent chunk-writers would clobber each other's unmatched entries, and there is no `MediaFile`-style multi-manager exception for `LibraryScan`; (b) only the controller may touch `LibraryScan.status`, so true chunking needs a new capped per-chunk CRD field (this task cannot edit `libraryscan_types.go`) or a `KV.Watch`-based aggregation goroutine — a much larger build than the milestone needs; (c) `LibraryScanSpec.Mode=incremental` (the kubebuilder default) already makes a *redelivered* single message cheap — the worker's `spec.path`-indexed fingerprint check (now comparing `spec.sizeBytes`/`spec.modTime` directly, see Step 10) skips every file it already matched. The worker instead sends `Message.InProgress(ctx)` roughly every 20s (well under the 60s `AckWait`) while it walks — the mechanism the topology comment names — and checkpoints counts to a `clustarr-progress` KV key every ~3s so the controller can poll and aggregate without waiting for the whole walk. `MaxAckPending: 4` still does useful work under this design: up to 4 different `LibraryScan`s can be in flight across the 2 worker replicas at once. True intra-scan directory fan-out is flagged as follow-up work, not built.

5. **Item creation is scoped to `RootFolderKind: movie` only.** §A1.5's "upsert the item" applies to every media kind the spec lists; spec §16's milestone table puts non-video kinds in M6, and this task is framed as M1 ("library rescan"). A file under a non-movie root folder with no existing catalog match is recorded in `status.unmatched` with reason `"root folder kind %q is not supported by library rescan yet"` — an honest scope limit, not a guess. Extending to series/music/books is explicit follow-up.

6. **A bare, id-less title is never resolved by calling out to the metadata gateway's search.** §A1.5 step 3 reads "resolve by normalized title plus year against existing catalog items, **then against the metadata gateway**." This task implements only the first half: exact `CleanTitle`(+year when both sides know it) against *existing* `Movie` objects. It never calls `clustarr.rpc.catalogarr.metadata.search` to turn a bare filename into a brand-new catalog item — that is the single riskiest form of guessing the never-guess rule forbids. A deliberate, conservative reading of an ambiguous passage, stated here per the brief's instruction.

7. **`MovieAddMethod`'s enum (`manual;list;collection`) has no "discovered by library scan" value.** This task uses `manual` as the closest fit when creating a `Movie` from a scanned file and flags the gap.

8. **`FormatScore`/`MatchedFormats`/`ProfileHash` are left zero-valued by this task.** Per correction 2, these are `MediaFileSpec` fields and therefore importarr's to write — but scoring them requires the item's `QualityProfile` plus `pkg/quality/catalogue`'s TRaSH custom-format evaluation, a separate subsystem this task does not integrate (it is not "a few minutes" of work and belongs with the quality/decision tasks, e.g. C2). A file discovered by library scan gets `Quality`/`Revision`/`ReleaseType`/`ReleaseGroup`/`Edition`/`Languages` (cheap: already produced by `release.ParsePath`, which this task already calls) but an unscored custom-format profile (`formatScore: 0`) until a follow-up task wires `pkg/quality/catalogue` into this path. This does not block M1: format score only affects upgrade *decisions*, not whether the file is tracked at all.

9. **`pkg/k8s.ManagerImportarrWorker` is not used by this task.** Its doc comment in `pkg/k8s/fieldmanager.go` references the non-existent `status.file`/`status.probe` split (the same error correction 2 fixes). Every write this task makes — `LibraryScan.status`, `ImportExclusion.status`, the rescan worker's `Movie` and `MediaFile` creates/updates — uses `k8s.ManagerImportarr`, per the plan controller's explicit instruction. Correcting `ManagerImportarrWorker`'s comment is outside this task's path ownership (`pkg/k8s/fieldmanager.go`) and is flagged here for whoever picks it up.

10. **The post-transcode field handoff on `MediaFile` is a real hazard this task must guard against, not just document.** `k8s.Apply`'s force-ownership is "always on" (its own doc comment): if the rescan worker re-applies `spec.sizeBytes`/`spec.modTime` on a file that catalogarr has already taken over after a transcode swap (spec §8.5), it silently reclaims those fields from catalogarr and the split breaks. Step 10 makes the worker check the existing `MediaFile.Spec.Original` before touching anything: `Original == false` means catalogarr owns this file's `sizeBytes`/`modTime`/`original` now, and the worker skips it entirely (counted as `FilesSkipped`, not re-applied).

---

- [ ] **Step 1: `ScanTask` bus payload, failing round-trip test first**

Add to `pkg/events/schema/schema_test.go`'s `allPayloads()` (append `schema.ScanTask{}` to the slice). Run it:

```bash
go test ./pkg/events/schema/... -run TestSchemaNamesAreUniqueAndVersioned
```

Fails: `schema.ScanTask` is undefined. Implement `pkg/events/schema/importarr.go`:

```go
package schema

// ScanTask asks a rescan worker to walk one RootFolder (or LibraryScan's
// subpath of it) on behalf of a LibraryScan. Subject:
// clustarr.work.importarr.scan.<rootfolder>. Amendment §A1.4, §A1.6.
type ScanTask struct {
	LibraryScanRef Ref    `json:"libraryScanRef"`
	RootFolderRef  Ref    `json:"rootFolderRef"`
	Path           string `json:"path"`
	Mode           string `json:"mode"`
	DryRun         bool   `json:"dryRun,omitempty"`
}

// Schema implements Payload.
func (ScanTask) Schema() string { return "importarr.ScanTask.v1" }
```

Add a round-trip test in a new `pkg/events/schema/importarr_test.go` mirroring `TestEncodeDecodeRoundTrip`:

```go
func TestScanTaskEncodeDecodeRoundTrip(t *testing.T) {
	in := schema.ScanTask{
		LibraryScanRef: schema.Ref{Namespace: "media", Name: "movies-2026-09-18t00-00-00z", UID: "u1"},
		RootFolderRef:  schema.Ref{Namespace: "media", Name: "movies"},
		Path:           "/data/media/movies",
		Mode:           "incremental",
	}
	name, data, err := schema.Encode(in)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if name != "importarr.ScanTask.v1" {
		t.Errorf("schema = %q", name)
	}
	var out schema.ScanTask
	if err := schema.Decode(name, data, &out); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Errorf("round trip changed the payload:\n got %+v\nwant %+v", out, in)
	}
}
```

```bash
go test ./pkg/events/schema/...
git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "feat(events): add the importarr.ScanTask work payload" -- pkg/events/schema/importarr.go pkg/events/schema/importarr_test.go pkg/events/schema/schema_test.go
```

- [ ] **Step 2: `rescan.Progress` checkpoint type, failing test first**

`importarr/worker/rescan/progress_test.go`:

```go
func TestProgressEncodeDecodeRoundTrip(t *testing.T) {
	seenAt := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	in := rescan.Progress{
		Done: true, FilesSeen: 12, FilesMatched: 10, ItemsCreated: 2, ItemsUpdated: 8, FilesSkipped: 2,
		Unmatched: []rescan.UnmatchedFile{{Path: "a.mkv", Reason: "ambiguous", Candidates: []string{"movie-a", "movie-b"}, SeenAt: seenAt}},
	}
	data, err := in.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	out, err := rescan.DecodeProgress(data)
	if err != nil {
		t.Fatalf("DecodeProgress: %v", err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Errorf("got %+v want %+v", out, in)
	}
}

func TestProgressKeyIsNamespacedUnderScan(t *testing.T) {
	if got, want := rescan.ProgressKey("abc-123"), "scan.abc-123"; got != want {
		t.Errorf("ProgressKey = %q, want %q", got, want)
	}
}
```

```bash
go test ./importarr/worker/rescan/...
```

Fails: package does not exist. Implement `importarr/worker/rescan/progress.go`:

```go
package rescan

import (
	"encoding/json"
	"fmt"
	"time"
)

// UnmatchedFile is one file the walk could not attribute to a catalog item,
// mirroring catalogv1alpha1.UnmatchedFile without importing the CRD package —
// Progress travels through a KV value, not the bus, but keeps the same
// "plain data only" discipline schema payloads use.
type UnmatchedFile struct {
	Path       string    `json:"path"`
	Reason     string    `json:"reason"`
	Candidates []string  `json:"candidates,omitempty"`
	SeenAt     time.Time `json:"seenAt"`
}

// Progress is the worker's running (and, once Done, final) tally for one
// LibraryScan, checkpointed to a clustarr-progress KV key roughly every 3s and
// polled by the LibraryScan controller, which is the only writer of
// LibraryScan.status (the single-writer rule has no worker exception here).
type Progress struct {
	Done  bool   `json:"done"`
	Error string `json:"error,omitempty"`

	FilesSeen    int64 `json:"filesSeen"`
	FilesMatched int64 `json:"filesMatched"`
	ItemsCreated int64 `json:"itemsCreated"`
	ItemsUpdated int64 `json:"itemsUpdated"`
	FilesSkipped int64 `json:"filesSkipped"`

	Unmatched []UnmatchedFile `json:"unmatched,omitempty"`
}

// Encode marshals p for a KV Put.
func (p Progress) Encode() ([]byte, error) {
	data, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("rescan: encode progress: %w", err)
	}
	return data, nil
}

// DecodeProgress unmarshals a KV entry written by Encode.
func DecodeProgress(data []byte) (Progress, error) {
	var p Progress
	if err := json.Unmarshal(data, &p); err != nil {
		return Progress{}, fmt.Errorf("rescan: decode progress: %w", err)
	}
	return p, nil
}

// ProgressKey is the clustarr-progress bucket key one LibraryScan's worker
// checkpoints to and its controller polls, following the bucket's existing
// "<kind>.<uid>" convention (spec §5's clustarr-progress row).
func ProgressKey(scanUID string) string { return "scan." + scanUID }
```

```bash
go test ./importarr/worker/rescan/...
git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "feat(importarr): rescan progress checkpoint type" -- importarr/worker/rescan/progress.go importarr/worker/rescan/progress_test.go
```

- [ ] **Step 3: `MatchMovie`, the pure never-guess matcher — failing tests first**

This is the function the "must NOT match" cases live in. `importarr/worker/rescan/match_test.go`:

```go
func TestMatchMovieUsesEmbeddedTmdbIDAgainstExistingMovie(t *testing.T) {
	parsed, err := release.ParsePath("/data/media/movies/Heat (1995) [tmdbid-949]/Heat (1995) [tmdbid-949] - Bluray-1080p.mkv", release.Options{Kind: commonv1.MediaKindMovie})
	if err != nil {
		t.Fatalf("ParsePath: %v", err)
	}
	existing := []rescan.MovieCandidate{{Name: "heat-1995", TmdbID: 949, Title: "Heat", Year: 1995}}
	got := rescan.MatchMovie(parsed, existing, failResolve(t))
	want := rescan.MatchResult{TmdbID: 949, ExistingName: "heat-1995"}
	if got != want {
		t.Errorf("got %+v want %+v", got, want)
	}
}

func TestMatchMovieCreatesFromEmbeddedTmdbIDWhenNoExistingMovie(t *testing.T) {
	parsed, err := release.ParsePath("/data/media/movies/Heat (1995) [tmdbid-949]/Heat (1995) [tmdbid-949] - Bluray-1080p.mkv", release.Options{Kind: commonv1.MediaKindMovie})
	if err != nil {
		t.Fatalf("ParsePath: %v", err)
	}
	got := rescan.MatchMovie(parsed, nil, failResolve(t))
	want := rescan.MatchResult{TmdbID: 949}
	if got != want {
		t.Errorf("got %+v want %+v", got, want)
	}
}

func TestMatchMovieResolvesEmbeddedImdbIDThroughGateway(t *testing.T) {
	parsed, err := release.ParsePath("/data/media/movies/Heat (1995) [imdbid-tt0113277]/Heat (1995) [imdbid-tt0113277].mkv", release.Options{Kind: commonv1.MediaKindMovie})
	if err != nil {
		t.Fatalf("ParsePath: %v", err)
	}
	resolve := func(imdbID string) (int64, error) {
		if imdbID == "tt0113277" {
			return 949, nil
		}
		t.Fatalf("unexpected imdb id %q", imdbID)
		return 0, nil
	}
	got := rescan.MatchMovie(parsed, nil, resolve)
	want := rescan.MatchResult{TmdbID: 949}
	if got != want {
		t.Errorf("got %+v want %+v", got, want)
	}
}

// "a file whose ids point at nothing" must NOT match.
func TestMatchMovieRejectsAnImdbIDTheGatewayCannotResolve(t *testing.T) {
	parsed, err := release.ParsePath("/data/media/movies/Nonexistent (2020) [imdbid-tt9999999]/Nonexistent (2020) [imdbid-tt9999999].mkv", release.Options{Kind: commonv1.MediaKindMovie})
	if err != nil {
		t.Fatalf("ParsePath: %v", err)
	}
	resolve := func(imdbID string) (int64, error) { return 0, errors.New("not found") }
	got := rescan.MatchMovie(parsed, nil, resolve)
	if !got.Unmatched {
		t.Fatalf("got matched: %+v", got)
	}
	if !strings.Contains(got.Reason, "tt9999999") {
		t.Errorf("reason %q does not name the unresolved id", got.Reason)
	}
}

func TestMatchMovieMatchesExistingByExactTitleAndYearWhenNoID(t *testing.T) {
	parsed, err := release.ParsePath("/data/media/movies/The Matrix (1999)/The Matrix (1999) - Bluray-1080p.mkv", release.Options{Kind: commonv1.MediaKindMovie})
	if err != nil {
		t.Fatalf("ParsePath: %v", err)
	}
	existing := []rescan.MovieCandidate{{Name: "the-matrix-1999", TmdbID: 603, Title: "The Matrix", Year: 1999}}
	got := rescan.MatchMovie(parsed, existing, failResolve(t))
	want := rescan.MatchResult{TmdbID: 603, ExistingName: "the-matrix-1999"}
	if got != want {
		t.Errorf("got %+v want %+v", got, want)
	}
}

// An ambiguous title (two existing movies share it) must NOT match.
func TestMatchMovieRejectsAmbiguousTitleWithNoID(t *testing.T) {
	parsed, err := release.ParsePath("/data/media/movies/Poltergeist (1982)/Poltergeist (1982).mkv", release.Options{Kind: commonv1.MediaKindMovie})
	if err != nil {
		t.Fatalf("ParsePath: %v", err)
	}
	existing := []rescan.MovieCandidate{
		{Name: "poltergeist-1982-a", TmdbID: 11663, Title: "Poltergeist", Year: 1982},
		{Name: "poltergeist-1982-b", TmdbID: 99999, Title: "Poltergeist", Year: 1982},
	}
	got := rescan.MatchMovie(parsed, existing, failResolve(t))
	if !got.Unmatched {
		t.Fatalf("got matched: %+v", got)
	}
	sort.Strings(got.Candidates)
	if want := []string{"poltergeist-1982-a", "poltergeist-1982-b"}; !reflect.DeepEqual(got.Candidates, want) {
		t.Errorf("candidates = %v, want %v", got.Candidates, want)
	}
}

func TestMatchMovieRejectsWhenNoIDAndNoTitleMatch(t *testing.T) {
	parsed, err := release.ParsePath("/data/media/movies/Some Unknown Film (2024)/Some Unknown Film (2024).mkv", release.Options{Kind: commonv1.MediaKindMovie})
	if err != nil {
		t.Fatalf("ParsePath: %v", err)
	}
	existing := []rescan.MovieCandidate{{Name: "the-matrix-1999", TmdbID: 603, Title: "The Matrix", Year: 1999}}
	got := rescan.MatchMovie(parsed, existing, failResolve(t))
	if !got.Unmatched {
		t.Fatalf("got matched: %+v", got)
	}
	if !strings.Contains(got.Reason, "1 existing movies") {
		t.Errorf("reason = %q", got.Reason)
	}
}

func failResolve(t *testing.T) rescan.ResolveIMDb {
	return func(imdbID string) (int64, error) {
		t.Fatalf("ResolveIMDb must not be called: %q", imdbID)
		return 0, nil
	}
}
```

Run: `go test ./importarr/worker/rescan/...` — fails, `rescan.MatchMovie` undefined. Implement `importarr/worker/rescan/match.go` per the exact logic given in the "Interfaces — Produces" block and disagreement note 6: embedded `tmdb` id used directly; embedded `imdb` id resolved via `resolve`; otherwise exact `release.CleanTitle` (+year when both sides have one) against `existing`, zero hits → unmatched "no embedded provider id and no confident title match among N existing movies", 2+ hits → unmatched "ambiguous: N existing movies share title %q" with `Candidates` set, exactly one hit → matched. Run the tests green.

```bash
go test ./importarr/worker/rescan/... -run TestMatchMovie
git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "feat(importarr): pure MatchMovie — the rescan never-guess matcher" -- importarr/worker/rescan/match.go importarr/worker/rescan/match_test.go
```

- [ ] **Step 4: RootFolder scan-schedule controller — first tick, failing envtest first**

`importarr/controller/rootfolderschedule/rootfolderschedule_controller_envtest_test.go`, following `pkg/k8s/patch_envtest_test.go`'s `newTestClient(t)` pattern (`CRDDirectoryPaths: []string{"../../../config/crd/bases"}`, skip when `KUBEBUILDER_ASSETS` is unset):

```go
func TestReconcileCreatesLibraryScanOnFirstTick(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	ns := createNamespace(t, ctx, c, "rfs-first-tick")

	rf := &catalogv1alpha1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Name: "movies", Namespace: ns},
		Spec: catalogv1alpha1.RootFolderSpec{
			Path: "/data/media/movies", Kind: catalogv1alpha1.RootFolderKindMovie,
			ScanSchedule: "0 3 * * *",
		},
	}
	if err := c.Create(ctx, rf); err != nil {
		t.Fatalf("create root folder: %v", err)
	}

	r := &rootfolderschedule.Reconciler{Client: c, Recorder: record.NewFakeRecorder(10), Clock: time.Now}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: "movies"}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	var scans catalogv1alpha1.LibraryScanList
	if err := c.List(ctx, &scans, client.InNamespace(ns), client.MatchingLabels{rootfolderschedule.LabelRootFolder: "movies"}); err != nil {
		t.Fatalf("list library scans: %v", err)
	}
	if len(scans.Items) != 1 {
		t.Fatalf("got %d library scans, want 1", len(scans.Items))
	}
	if got := scans.Items[0].Spec.RootFolderRef; got != "movies" {
		t.Errorf("rootFolderRef = %q", got)
	}

	// importarr must never write RootFolder status (Task C4 owns it — the
	// one-writer rule holds because two controllers watch RootFolder but only
	// one, catalogarr's, touches its status subresource).
	var after catalogv1alpha1.RootFolder
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "movies"}, &after); err != nil {
		t.Fatalf("get root folder: %v", err)
	}
	if !reflect.DeepEqual(after.Status, catalogv1alpha1.RootFolderStatus{}) {
		t.Errorf("RootFolder.status was written by importarr: %+v", after.Status)
	}
}
```

Fails: `rootfolderschedule` package does not exist. Implement `importarr/controller/rootfolderschedule/rootfolderschedule_controller.go` per the Reconcile design above (deterministic `k8s.ChildName(rf.Name, "scan", tick.UTC().Format(time.RFC3339))` name; `cron.ParseStandard`; List existing scans by `LabelRootFolder`; `last` = max `CreationTimestamp` or zero value; `next := sched.Next(last)`; create via `k8s.Apply` with `k8s.ManagerImportarr` when `!next.After(now)`, else `RequeueAfter: next.Sub(now)`). `SetupWithManager` uses `.Named("rootfolderschedule").For(&catalogv1alpha1.RootFolder{}, builder.WithPredicates(k8s.GenerationChanged()))` — no `Owns()`/`Watches()` on `LibraryScan`: the schedule is entirely self-timed via `RequeueAfter`, and no owner reference is set (a `LibraryScan`'s lifecycle is independent of the `RootFolder` that triggered it — it deletes itself on its own TTL, not on `RootFolder` deletion).

```bash
KUBEBUILDER_ASSETS=$(go run sigs.k8s.io/controller-runtime/tools/setup-envtest use -p path 1.37.0) \
  go test ./importarr/controller/rootfolderschedule/... -run TestReconcileCreatesLibraryScanOnFirstTick
git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "feat(importarr): RootFolder scan-schedule controller creates the first LibraryScan tick" -- importarr/controller/rootfolderschedule/rootfolderschedule_controller.go importarr/controller/rootfolderschedule/rootfolderschedule_controller_envtest_test.go
```

- [ ] **Step 5: schedule idempotency and invalid-cron handling**

Add to the same envtest file:

```go
func TestReconcileDoesNotDuplicateTheSameTick(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	ns := createNamespace(t, ctx, c, "rfs-dedup")
	rf := &catalogv1alpha1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Name: "movies", Namespace: ns},
		Spec: catalogv1alpha1.RootFolderSpec{Path: "/data/media/movies", Kind: catalogv1alpha1.RootFolderKindMovie, ScanSchedule: "0 3 * * *"},
	}
	if err := c.Create(ctx, rf); err != nil {
		t.Fatalf("create root folder: %v", err)
	}
	r := &rootfolderschedule.Reconciler{Client: c, Recorder: record.NewFakeRecorder(10), Clock: time.Now}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: "movies"}}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	var scans catalogv1alpha1.LibraryScanList
	if err := c.List(ctx, &scans, client.InNamespace(ns), client.MatchingLabels{rootfolderschedule.LabelRootFolder: "movies"}); err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(scans.Items) != 1 {
		t.Fatalf("got %d library scans after two reconciles, want 1", len(scans.Items))
	}
}

func TestReconcileEmitsEventAndTerminalErrorOnInvalidCron(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	ns := createNamespace(t, ctx, c, "rfs-bad-cron")
	rf := &catalogv1alpha1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Name: "movies", Namespace: ns},
		Spec: catalogv1alpha1.RootFolderSpec{Path: "/data/media/movies", Kind: catalogv1alpha1.RootFolderKindMovie, ScanSchedule: "not a cron expression"},
	}
	if err := c.Create(ctx, rf); err != nil {
		t.Fatalf("create root folder: %v", err)
	}
	rec := record.NewFakeRecorder(10)
	r := &rootfolderschedule.Reconciler{Client: c, Recorder: rec, Clock: time.Now}
	_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: "movies"}})
	if err == nil {
		t.Fatal("expected an error")
	}
	var terminal reconcile.TerminalError
	if !errors.As(err, &terminal) {
		t.Errorf("error %v is not a reconcile.TerminalError", err)
	}
	select {
	case msg := <-rec.Events:
		if !strings.Contains(msg, "InvalidScanSchedule") {
			t.Errorf("event = %q", msg)
		}
	default:
		t.Error("no event recorded")
	}
}
```

Implement the cron-parse-failure branch: `r.Recorder.Eventf(rf, nil, corev1.EventTypeWarning, "InvalidScanSchedule", "TriggerScan", "cron expression %q is invalid: %v", rf.Spec.ScanSchedule, err)` then `return ctrl.Result{}, reconcile.TerminalError(fmt.Errorf(...))`.

```bash
KUBEBUILDER_ASSETS=... go test ./importarr/controller/rootfolderschedule/...
git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "feat(importarr): schedule idempotency and invalid-cron event handling" -- importarr/controller/rootfolderschedule
```

- [ ] **Step 6: LibraryScan controller — start phase, failing envtest first**

`importarr/controller/libraryscan/libraryscan_controller_envtest_test.go`:

```go
func TestReconcileStartPublishesScanTaskAndSetsRunning(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	ns := createNamespace(t, ctx, c, "ls-start")
	rf := &catalogv1alpha1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Name: "movies", Namespace: ns},
		Spec:       catalogv1alpha1.RootFolderSpec{Path: "/data/media/movies", Kind: catalogv1alpha1.RootFolderKindMovie},
		Status:     catalogv1alpha1.RootFolderStatus{Conditions: []metav1.Condition{{Type: catalogv1alpha1.RootFolderConditionReady, Status: metav1.ConditionTrue, Reason: "Ready", LastTransitionTime: metav1.Now()}}},
	}
	if err := c.Create(ctx, rf); err != nil {
		t.Fatalf("create root folder: %v", err)
	}
	if err := c.Status().Update(ctx, rf); err != nil { // plain test fixture write, not production code
		t.Fatalf("seed root folder status: %v", err)
	}
	scan := &catalogv1alpha1.LibraryScan{
		ObjectMeta: metav1.ObjectMeta{Name: "movies-tick", Namespace: ns},
		Spec:       catalogv1alpha1.LibraryScanSpec{RootFolderRef: "movies", Mode: catalogv1alpha1.ScanModeIncremental},
	}
	if err := c.Create(ctx, scan); err != nil {
		t.Fatalf("create library scan: %v", err)
	}

	bus := membus.New(clockwork.NewRealClock())
	if err := bus.Ensure(ctx, events.Default()); err != nil {
		t.Fatalf("ensure topology: %v", err)
	}
	got := newCollector(t, ctx, bus)

	r := &libraryscan.Reconciler{Client: c, Bus: bus, Clock: time.Now}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: "movies-tick"}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	var after catalogv1alpha1.LibraryScan
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "movies-tick"}, &after); err != nil {
		t.Fatalf("get: %v", err)
	}
	if after.Status.Phase != catalogv1alpha1.ScanPhaseRunning {
		t.Errorf("phase = %q, want Running", after.Status.Phase)
	}
	if after.Status.StartedAt == nil {
		t.Error("startedAt not set")
	}

	task := got.waitOne(t)
	if task.RootFolderRef.Name != "movies" || task.Path != "/data/media/movies" || task.Mode != "incremental" {
		t.Errorf("task = %+v", task)
	}
}
```

(`newCollector` is a small test helper subscribing to `events.ConsumerImportScan`'s subscription and decoding the first `ScanTask` it receives — write it alongside the test, mirroring `pkg/events/contracttest`'s collector pattern.)

Fails: package does not exist. Implement `importarr/controller/libraryscan/libraryscan_controller.go`:

- `Reconcile`: `Get` the scan, `client.IgnoreNotFound`; `k8s.IsDeleting` → no-op (no finalizer: the only side effects — one bus publish and one KV key — are dedup-protected and TTL-bounded respectively, so nothing leaks on delete); dispatch on `Status.Phase` (`""`/`Pending` → `start`, `Running` → `poll`, else → `maybeExpire`).
- `start`: `Get` the `RootFolder` named by `Spec.RootFolderRef`; if missing, `MarkFalse` `Ready`/`ReasonDependencyNotReady`, `PatchStatus`, `RequeueAfter(15s)`; if present but not `RootFolderConditionReady`, same; else build `Path = filepath.Join(rf.Spec.Path, scan.Spec.Subpath)`, publish via `bus.Publish` on `events.WorkScanSubject(rf.Name)` with `&events.Envelope{ID: events.MsgIDForObject(string(scan.UID), scan.Generation, "scan"), Type: "importarr.scan", Schema: schemaName, Source: "importarr@" + version.String(), Key: scan.Namespace + "/" + scan.Name, Data: data}` (schemaName/data from `schema.Encode(schema.ScanTask{...})`), then `PatchStatus` `Phase: Running, StartedAt: now` under `k8s.ManagerImportarr`.
- `poll`: `bus.KV(events.BucketProgress).Get(ctx, rescan.ProgressKey(string(scan.UID)))`; `events.ErrKeyNotFound` → `RequeueAfter(3s)` (worker has not checkpointed yet); else `rescan.DecodeProgress`, `PatchStatus` the five counters plus `Unmatched` (sorted by `SeenAt` descending, capped to 200 — see Step 7) under `k8s.ManagerImportarr`; if `!Done` → `RequeueAfter(3s)`; if `Done && Error == ""` → `Phase: Completed, FinishedAt: now`, `MarkTrue Ready`; if `Done && Error != ""` → `Phase: Failed, FinishedAt: now`, `MarkFalse Ready`.
- `maybeExpire`: `ttl := 3600; if scan.Spec.TTLSecondsAfterFinished != nil { ttl = int(*scan.Spec.TTLSecondsAfterFinished) }`; if `now.After(scan.Status.FinishedAt.Add(time.Duration(ttl)*time.Second))` → `r.Delete(ctx, scan)`; else `RequeueAfter` the remaining time.
- `SetupWithManager`: `.Named("libraryscan").For(&catalogv1alpha1.LibraryScan{}, builder.WithPredicates(k8s.GenerationChanged())).Complete(r)`.

```bash
KUBEBUILDER_ASSETS=... go test ./importarr/controller/libraryscan/... -run TestReconcileStartPublishesScanTaskAndSetsRunning
git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "feat(importarr): LibraryScan controller — start phase publishes the scan task" -- importarr/controller/libraryscan
```

- [ ] **Step 7: LibraryScan controller — poll aggregation and the 200-cap, failing test first**

Add:

```go
func TestReconcilePollAggregatesProgressAndCapsUnmatchedAt200(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	ns := createNamespace(t, ctx, c, "ls-poll")
	scan := &catalogv1alpha1.LibraryScan{
		ObjectMeta: metav1.ObjectMeta{Name: "movies-tick", Namespace: ns},
		Spec:       catalogv1alpha1.LibraryScanSpec{RootFolderRef: "movies"},
		Status:     catalogv1alpha1.LibraryScanStatus{Phase: catalogv1alpha1.ScanPhaseRunning, StartedAt: ptrTime(time.Now())},
	}
	if err := c.Create(ctx, scan); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := c.Status().Update(ctx, scan); err != nil {
		t.Fatalf("seed status: %v", err)
	}

	bus := membus.New(clockwork.NewRealClock())
	if err := bus.Ensure(ctx, events.Default()); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	var unmatched []rescan.UnmatchedFile
	for i := range 250 {
		unmatched = append(unmatched, rescan.UnmatchedFile{
			Path: fmt.Sprintf("file-%03d.mkv", i), Reason: "no id, no title match",
			SeenAt: time.Now().Add(time.Duration(i) * time.Second), // strictly increasing
		})
	}
	progress := rescan.Progress{Done: true, FilesSeen: 250, FilesMatched: 0, FilesSkipped: 0, Unmatched: unmatched}
	data, err := progress.Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if _, err := bus.KV(events.BucketProgress).Put(ctx, rescan.ProgressKey(string(scan.UID)), data); err != nil {
		t.Fatalf("put progress: %v", err)
	}

	r := &libraryscan.Reconciler{Client: c, Bus: bus, Clock: time.Now}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: "movies-tick"}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	var after catalogv1alpha1.LibraryScan
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "movies-tick"}, &after); err != nil {
		t.Fatalf("get: %v", err)
	}
	if after.Status.Phase != catalogv1alpha1.ScanPhaseCompleted {
		t.Errorf("phase = %q", after.Status.Phase)
	}
	if got := len(after.Status.Unmatched); got != 200 {
		t.Fatalf("unmatched len = %d, want 200 (CRD MaxItems)", got)
	}
	if after.Status.Unmatched[0].Path != "file-249.mkv" {
		t.Errorf("unmatched[0] = %q, want file-249.mkv (newest first)", after.Status.Unmatched[0].Path)
	}
	for _, u := range after.Status.Unmatched {
		if u.Path == "file-000.mkv" {
			t.Error("file-000.mkv (oldest) should have been dropped by the 200 cap")
		}
	}
}
```

Implement the sort-and-cap in `poll`: `slices.SortFunc(unmatched, func(a, b rescan.UnmatchedFile) int { return b.SeenAt.Compare(a.SeenAt) })`, then `if len(unmatched) > 200 { unmatched = unmatched[:200] }`, converting each to `catalogac.UnmatchedFile().WithPath(...).WithReason(...).WithCandidates(...).WithSeenAt(metav1.NewTime(...))` for `LibraryScanStatusApplyConfiguration.WithUnmatched(...)`.

```bash
KUBEBUILDER_ASSETS=... go test ./importarr/controller/libraryscan/...
git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "feat(importarr): LibraryScan controller — poll aggregation and the 200-item unmatched cap" -- importarr/controller/libraryscan
```

- [ ] **Step 8: LibraryScan controller — TTL expiry, failing test first**

```go
func TestReconcileDeletesAfterTTL(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	ns := createNamespace(t, ctx, c, "ls-ttl")
	past := metav1.NewTime(time.Now().Add(-2 * time.Hour))
	ttl := int32(60) // 1 minute, long expired
	scan := &catalogv1alpha1.LibraryScan{
		ObjectMeta: metav1.ObjectMeta{Name: "movies-tick", Namespace: ns},
		Spec:       catalogv1alpha1.LibraryScanSpec{RootFolderRef: "movies", TTLSecondsAfterFinished: &ttl},
		Status:     catalogv1alpha1.LibraryScanStatus{Phase: catalogv1alpha1.ScanPhaseCompleted, FinishedAt: &past},
	}
	if err := c.Create(ctx, scan); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := c.Status().Update(ctx, scan); err != nil {
		t.Fatalf("seed status: %v", err)
	}
	r := &libraryscan.Reconciler{Client: c, Bus: membus.New(clockwork.NewRealClock()), Clock: time.Now}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: "movies-tick"}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "movies-tick"}, &catalogv1alpha1.LibraryScan{})
	if !apierrors.IsNotFound(err) {
		t.Errorf("expected NotFound after TTL, got %v", err)
	}
}
```

```bash
KUBEBUILDER_ASSETS=... go test ./importarr/controller/libraryscan/...
git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "feat(importarr): LibraryScan controller — delete once ttlSecondsAfterFinished elapses" -- importarr/controller/libraryscan
```

- [ ] **Step 9: rescan worker — fsops classification, failing test first**

`importarr/worker/rescan/worker_envtest_test.go` (needs a real client for `k8s.Apply`, per `pkg/k8s/patch_envtest_test.go`'s precedent — the fake client does not exercise SSA apply the way these tests need):

```go
func TestHandleSkipsPartExtraAndSampleFiles(t *testing.T) {
	c, mgr := newTestClientAndManager(t) // wraps newTestClient(t)'s cfg into a ctrl.Manager too, see Step 11
	ctx := context.Background()
	ns := createNamespace(t, ctx, c, "rw-skip")
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "Movie (2020) [tmdbid-1]", "Movie (2020) [tmdbid-1].mkv.part"), 1<<20)
	mustWriteFile(t, filepath.Join(root, "Movie (2020) [tmdbid-1]", "featurettes", "behind-the-scenes.mkv"), 1<<20)
	mustWriteFile(t, filepath.Join(root, "Movie (2020) [tmdbid-1]", "Movie.Sample.mkv"), 1<<20)

	rf := &catalogv1alpha1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Name: "movies", Namespace: ns},
		Spec:       catalogv1alpha1.RootFolderSpec{Path: root, Kind: catalogv1alpha1.RootFolderKindMovie, Defaults: catalogv1alpha1.RootDefaults{QualityProfileRef: "hd-bluray-web"}},
	}
	if err := c.Create(ctx, rf); err != nil {
		t.Fatalf("create root folder: %v", err)
	}
	scan := &catalogv1alpha1.LibraryScan{ObjectMeta: metav1.ObjectMeta{Name: "tick", Namespace: ns}, Spec: catalogv1alpha1.LibraryScanSpec{RootFolderRef: "movies", Mode: catalogv1alpha1.ScanModeFull}}
	if err := c.Create(ctx, scan); err != nil {
		t.Fatalf("create scan: %v", err)
	}
	if err := rescan.IndexMediaFileByPath(ctx, mgr); err != nil {
		t.Fatalf("index: %v", err)
	}

	bus := membus.New(clockwork.NewRealClock())
	if err := bus.Ensure(ctx, events.Default()); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	w := rescan.NewWorker(c, bus)
	task := schema.ScanTask{
		LibraryScanRef: schema.Ref{Namespace: ns, Name: "tick", UID: string(scan.UID)},
		RootFolderRef:  schema.Ref{Namespace: ns, Name: "movies"},
		Path:           root, Mode: "full",
	}
	msg := newFakeMessage(t, task) // small test double implementing events.Message over a schema.Encode'd envelope
	if err := w.Handle(ctx, msg); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	final, ok := msg.progress(t, bus, ctx, string(scan.UID))
	if !ok {
		t.Fatal("no progress checkpoint written")
	}
	if final.FilesSkipped != 3 {
		t.Errorf("filesSkipped = %d, want 3 (.part, featurettes/, Sample)", final.FilesSkipped)
	}
	if final.FilesSeen != 0 {
		t.Errorf("filesSeen = %d, want 0 (nothing classified ClassMedia)", final.FilesSeen)
	}
}
```

(`mustWriteFile`, `newFakeMessage` are small local test helpers: `mustWriteFile` writes N zero bytes via `os.WriteFile`; `newFakeMessage` wraps a `schema.Encode`d envelope and no-ops `Ack`/`Nak`/`Term`/`InProgress` so `Handle` can be called directly without a live bus subscription.)

Fails: package/`Handle` do not exist. Implement `importarr/worker/rescan/worker.go`'s `Handle` skeleton (decode `ScanTask`, get `RootFolder` + `LibraryScan`, `fsops.Walk` with the `ClassPart/ClassExtra/ClassSample/ClassOther → FilesSkipped++` / `ClassMedia → FilesSeen++`, dispatch) and `importarr/worker/rescan/index.go`'s `IndexMediaFileByPath` (registers a field indexer on `MediaFilePathIndexKey = "spec.path"`, extracting `mf.Spec.Path`). Get this test green with `handleMediaFile` still a stub that always appends to `Unmatched` with reason `"not yet implemented"` — Step 10 replaces the stub.

```bash
KUBEBUILDER_ASSETS=... go test ./importarr/worker/rescan/... -run TestHandleSkipsPartExtraAndSampleFiles
git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "feat(importarr): rescan worker walk/classify skeleton" -- importarr/worker/rescan/worker.go importarr/worker/rescan/index.go importarr/worker/rescan/worker_envtest_test.go
```

- [ ] **Step 10: rescan worker — Movie upsert and `MediaFileSpec` (spec only, no probe), failing test first**

Add to the envtest file:

```go
func TestHandleCreatesMovieAndMediaFileSpecForAConfidentMatch(t *testing.T) {
	c, mgr := newTestClientAndManager(t)
	ctx := context.Background()
	ns := createNamespace(t, ctx, c, "rw-create")
	root := t.TempDir()
	path := filepath.Join(root, "Heat (1995) [tmdbid-949]", "Heat (1995) [tmdbid-949] - Bluray-1080p.mkv")
	mustWriteFile(t, path, 2<<20)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	rf := &catalogv1alpha1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Name: "movies", Namespace: ns},
		Spec:       catalogv1alpha1.RootFolderSpec{Path: root, Kind: catalogv1alpha1.RootFolderKindMovie, Defaults: catalogv1alpha1.RootDefaults{QualityProfileRef: "hd-bluray-web"}},
	}
	if err := c.Create(ctx, rf); err != nil {
		t.Fatalf("create root folder: %v", err)
	}
	scan := &catalogv1alpha1.LibraryScan{ObjectMeta: metav1.ObjectMeta{Name: "tick", Namespace: ns}, Spec: catalogv1alpha1.LibraryScanSpec{RootFolderRef: "movies", Mode: catalogv1alpha1.ScanModeFull}}
	if err := c.Create(ctx, scan); err != nil {
		t.Fatalf("create scan: %v", err)
	}
	if err := rescan.IndexMediaFileByPath(ctx, mgr); err != nil {
		t.Fatalf("index: %v", err)
	}

	bus := membus.New(clockwork.NewRealClock())
	if err := bus.Ensure(ctx, events.Default()); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	w := rescan.NewWorker(c, bus)
	task := schema.ScanTask{LibraryScanRef: schema.Ref{Namespace: ns, Name: "tick", UID: string(scan.UID)}, RootFolderRef: schema.Ref{Namespace: ns, Name: "movies"}, Path: root, Mode: "full"}
	if err := w.Handle(ctx, newFakeMessage(t, task)); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	var movies catalogv1alpha1.MovieList
	if err := c.List(ctx, &movies, client.InNamespace(ns)); err != nil {
		t.Fatalf("list movies: %v", err)
	}
	if len(movies.Items) != 1 {
		t.Fatalf("got %d movies, want 1", len(movies.Items))
	}
	movie := movies.Items[0]
	if movie.Spec.TmdbID != 949 {
		t.Errorf("tmdbID = %d, want 949", movie.Spec.TmdbID)
	}
	if movie.Spec.QualityProfileRef != "hd-bluray-web" || movie.Spec.RootFolderRef != "movies" {
		t.Errorf("spec = %+v", movie.Spec)
	}
	if fm := findManager(t, movie.ManagedFields, "spec.tmdbID"); fm != string(k8s.ManagerImportarr) {
		t.Errorf("movie spec.tmdbID field manager = %q, want %q", fm, k8s.ManagerImportarr)
	}

	var files catalogv1alpha1.MediaFileList
	if err := c.List(ctx, &files, client.InNamespace(ns)); err != nil {
		t.Fatalf("list media files: %v", err)
	}
	if len(files.Items) != 1 {
		t.Fatalf("got %d media files, want 1", len(files.Items))
	}
	mf := files.Items[0]
	if mf.Spec.MediaRef.Kind != commonv1.MediaKindMovie || mf.Spec.MediaRef.Name != movie.Name {
		t.Errorf("mediaRef = %+v", mf.Spec.MediaRef)
	}
	if mf.Spec.Path != path || mf.Spec.SizeBytes != info.Size() {
		t.Errorf("spec = %+v", mf.Spec)
	}
	if mf.Spec.Quality.Name == "" {
		t.Error("spec.quality not populated from the parsed release")
	}
	// MediaFileStatus is entirely catalogarr's (Task C7) — importarr must never
	// have written it (no manager entry at all for the status subresource).
	if fm := findStatusManager(t, mf.ManagedFields, "status.probeHash"); fm != "" {
		t.Errorf("status.probeHash has a manager (%q); importarr must never write MediaFile status", fm)
	}
	if mf.Status.ProbeHash != "" || mf.Status.MediaInfo != nil {
		t.Error("MediaFile.status was populated; that is catalogarr's job (Task C7), not this task's")
	}
	if fm := findManager(t, mf.ManagedFields, "spec.path"); fm != string(k8s.ManagerImportarr) {
		t.Errorf("mediafile spec.path field manager = %q, want %q", fm, k8s.ManagerImportarr)
	}
}

// The post-transcode field handoff (disagreement note 10): once catalogarr has
// flipped Original=false, the rescan worker must not re-apply sizeBytes/modTime
// even if it walks the file again.
func TestHandleDoesNotReclaimFieldsCatalogarrOwnsPostTranscode(t *testing.T) {
	c, mgr := newTestClientAndManager(t)
	ctx := context.Background()
	ns := createNamespace(t, ctx, c, "rw-post-transcode")
	root := t.TempDir()
	path := filepath.Join(root, "Heat (1995) [tmdbid-949]", "Heat (1995) [tmdbid-949] - Bluray-1080p.mkv")
	mustWriteFile(t, path, 2<<20)

	rf := &catalogv1alpha1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Name: "movies", Namespace: ns},
		Spec:       catalogv1alpha1.RootFolderSpec{Path: root, Kind: catalogv1alpha1.RootFolderKindMovie, Defaults: catalogv1alpha1.RootDefaults{QualityProfileRef: "hd-bluray-web"}},
	}
	if err := c.Create(ctx, rf); err != nil {
		t.Fatalf("create root folder: %v", err)
	}
	movie := &catalogv1alpha1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: "heat-949", Namespace: ns},
		Spec:       catalogv1alpha1.MovieSpec{TmdbID: 949, QualityProfileRef: "hd-bluray-web", RootFolderRef: "movies"},
	}
	if err := c.Create(ctx, movie); err != nil {
		t.Fatalf("create movie: %v", err)
	}
	notOriginal := false
	mf := &catalogv1alpha1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Name: k8s.ChildName(movie.Name, "mediafile", path), Namespace: ns},
		Spec: catalogv1alpha1.MediaFileSpec{
			MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: movie.Name},
			Path:     path, SizeBytes: 999, // deliberately stale vs the real file, to prove it is left alone
			Original: &notOriginal,         // catalogarr already took this file over after a transcode
		},
	}
	if err := c.Create(ctx, mf); err != nil {
		t.Fatalf("create media file: %v", err)
	}

	scan := &catalogv1alpha1.LibraryScan{ObjectMeta: metav1.ObjectMeta{Name: "tick", Namespace: ns}, Spec: catalogv1alpha1.LibraryScanSpec{RootFolderRef: "movies", Mode: catalogv1alpha1.ScanModeFull}}
	if err := c.Create(ctx, scan); err != nil {
		t.Fatalf("create scan: %v", err)
	}
	if err := rescan.IndexMediaFileByPath(ctx, mgr); err != nil {
		t.Fatalf("index: %v", err)
	}
	bus := membus.New(clockwork.NewRealClock())
	if err := bus.Ensure(ctx, events.Default()); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	w := rescan.NewWorker(c, bus)
	task := schema.ScanTask{LibraryScanRef: schema.Ref{Namespace: ns, Name: "tick", UID: string(scan.UID)}, RootFolderRef: schema.Ref{Namespace: ns, Name: "movies"}, Path: root, Mode: "full"}
	if err := w.Handle(ctx, newFakeMessage(t, task)); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	var after catalogv1alpha1.MediaFile
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: mf.Name}, &after); err != nil {
		t.Fatalf("get: %v", err)
	}
	if after.Spec.SizeBytes != 999 {
		t.Errorf("sizeBytes = %d, want 999 (untouched — catalogarr owns it post-transcode)", after.Spec.SizeBytes)
	}
}
```

Implement `importarr/worker/rescan/mediafile.go`'s `applyMovieAndMediaFile` (the `handleMediaFile` logic from the design above):

1. Look up an existing `MediaFile` by the `spec.path` index. If found and `Spec.Original != nil && !*Spec.Original` → `FilesSkipped++`, return without touching anything (disagreement note 10).
2. If found, `mode == "incremental"`, and `Spec.SizeBytes == info.Size() && Spec.ModTime.Time.Equal(info.ModTime())` → `FilesSkipped++`, return (the incremental fingerprint is `spec.sizeBytes`+`spec.modTime` directly — no separate hash needed now that probing is not this task's job).
3. `release.ParsePath(path, release.Options{Kind: commonv1.MediaKindMovie})`; parse failure → `Unmatched{Reason: "could not parse: %v"}`.
4. `MatchMovie(parsed, existing, w.resolveIMDb(ctx))` (the real `resolveIMDb` closure wraps `w.Bus.Request(ctx, events.RPCMetadataResolve, schema.MetadataRequest{Kind: commonv1.MediaKindMovie, IDs: map[string]string{"imdb": imdbID}}, &resp)` under `w.MetadataTimeout`); `result.Unmatched` → append to `p.Unmatched`.
5. `movieName := result.ExistingName`; if empty: guard `rootFolder.Spec.Defaults.QualityProfileRef == ""` → unmatched "root folder %s has no default quality profile"; else `movieName = k8s.DeterministicName(release.CleanTitle(parsed.Title), k8s.MaxNameLength, "movie", strconv.FormatInt(result.TmdbID, 10))`, `k8s.Apply` a `catalogac.Movie(movieName, ns).WithSpec(catalogac.MovieSpec().WithTmdbID(result.TmdbID).WithQualityProfileRef(...).WithRootFolderRef(rf.Name).WithAddOptions(catalogac.MovieAddOptions().WithSearchForMovie(false).WithAddMethod(catalogv1alpha1.MovieAddMethodManual)))` under `k8s.ManagerImportarr`; `p.ItemsCreated++`; else `p.ItemsUpdated++`.
6. `name := k8s.ChildName(movieName, "mediafile", path)`; `k8s.Apply` a `catalogac.MediaFile(name, ns).WithSpec(catalogac.MediaFileSpec().WithMediaRef(commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: movieName}).WithPath(path).WithSizeBytes(info.Size()).WithModTime(metav1.NewTime(info.ModTime())).WithQuality(parsed.Quality).WithRevision(parsed.Revision).WithReleaseType(parsed.ReleaseType).WithReleaseGroup(parsed.Group).WithEdition(parsed.Edition).WithLanguages(parsed.Languages...))` under `k8s.ManagerImportarr` — **no `PatchStatus` call, no `pkg/mediainfo` import** (disagreement notes 1, 8: `FormatScore`/`MatchedFormats`/`ProfileHash` setters are deliberately not called, left at the CRD zero value). `p.FilesMatched++`.

```bash
KUBEBUILDER_ASSETS=... go test ./importarr/worker/rescan/...
git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "feat(importarr): rescan worker — Movie upsert and MediaFileSpec under k8s.ManagerImportarr" -- importarr/worker/rescan/mediafile.go importarr/worker/rescan/worker.go importarr/worker/rescan/worker_envtest_test.go
```

- [ ] **Step 11: wire the rescan components into `importarr/run.go`**

Modify `setupControllers`'s signature to `func setupControllers(mgr ctrl.Manager, bus events.Bus, o Options) error` (it currently only takes `mgr, o` — the `LibraryScan` and `ImportExclusion` controllers need the bus) and update its one call site in `Run` from `setupControllers(mgr, o)` to `setupControllers(mgr, bus, o)` (the call already happens after `bus, nc, err := k8s.ConnectBus(...)`, so `bus` is in scope). Add a `newTestClientAndManager(t) (client.Client, ctrl.Manager)` helper next to `newTestClient` in `importarr/worker/rescan`'s test file, building a `ctrl.Manager` from the same envtest `cfg` (`ctrl.NewManager(cfg, ctrl.Options{Scheme: k8s.MustNewScheme(), Metrics: metricsserver.Options{BindAddress: "0"}})`) so Steps 9–10's `rescan.IndexMediaFileByPath(ctx, mgr)` calls have a real manager to index against; return `mgr.GetClient()` so tests share one cache-backed client. Replace the two TODO bodies (ImportExclusion wiring included, per the scope correction):

```go
func setupControllers(mgr ctrl.Manager, bus events.Bus, o Options) error {
	if err := (&rootfolderschedule.Reconciler{Client: mgr.GetClient(), Recorder: mgr.GetEventRecorder("rootfolderschedule"), Clock: time.Now}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("importarr: rootfolderschedule: %w", err)
	}
	if err := (&libraryscan.Reconciler{Client: mgr.GetClient(), Bus: bus, Clock: time.Now}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("importarr: libraryscan: %w", err)
	}
	if err := (&importexclusion.Reconciler{Client: mgr.GetClient(), Bus: bus, Clock: time.Now}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("importarr: importexclusion: %w", err)
	}
	return nil
}

func setupWorkers(mgr ctrl.Manager, bus events.Bus, o Options) error {
	if err := rescan.IndexMediaFileByPath(context.Background(), mgr); err != nil {
		return fmt.Errorf("importarr: index mediafile path: %w", err)
	}
	scanSpec, ok := o.BusTopology().Consumer(events.ConsumerImportScan)
	if !ok {
		return fmt.Errorf("importarr: consumer %s missing from topology", events.ConsumerImportScan)
	}
	worker := rescan.NewWorker(mgr.GetClient(), bus)
	return mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
		stop, err := bus.Subscribe(ctx, scanSpec.Subscription(), worker.Handle)
		if err != nil {
			return fmt.Errorf("importarr: subscribe %s: %w", events.ConsumerImportScan, err)
		}
		<-ctx.Done()
		stop()
		return nil
	}))
}
```

```bash
go build ./importarr/...
KUBEBUILDER_ASSETS=... go test ./importarr/...
git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "feat(importarr): wire the rescan controllers and worker into setupControllers/setupWorkers" -- importarr/run.go importarr/worker/rescan/worker_envtest_test.go
```

- [ ] **Step 12: combined envtest — plant a file, prove the CRD-visible outcome the brief asks for**

`importarr/controller/libraryscan/libraryscan_controller_envtest_test.go`, one more test that drives the worker and the controller together end-to-end:

```go
func TestScanEndToEndCreatesMediaFileAndRecordsUnmatched(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	ns := createNamespace(t, ctx, c, "ls-e2e")
	root := t.TempDir()
	good := filepath.Join(root, "Heat (1995) [tmdbid-949]", "Heat (1995) [tmdbid-949] - Bluray-1080p.mkv")
	bad := filepath.Join(root, "Some Unknown Film (2024)", "Some Unknown Film (2024).mkv")
	mustWriteFile(t, good, 2<<20)
	mustWriteFile(t, bad, 2<<20)

	rf := &catalogv1alpha1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Name: "movies", Namespace: ns},
		Spec:       catalogv1alpha1.RootFolderSpec{Path: root, Kind: catalogv1alpha1.RootFolderKindMovie, Defaults: catalogv1alpha1.RootDefaults{QualityProfileRef: "hd-bluray-web"}},
		Status:     catalogv1alpha1.RootFolderStatus{Conditions: []metav1.Condition{{Type: catalogv1alpha1.RootFolderConditionReady, Status: metav1.ConditionTrue, Reason: "Ready", LastTransitionTime: metav1.Now()}}},
	}
	if err := c.Create(ctx, rf); err != nil {
		t.Fatalf("create root folder: %v", err)
	}
	if err := c.Status().Update(ctx, rf); err != nil {
		t.Fatalf("seed status: %v", err)
	}
	scan := &catalogv1alpha1.LibraryScan{ObjectMeta: metav1.ObjectMeta{Name: "tick", Namespace: ns}, Spec: catalogv1alpha1.LibraryScanSpec{RootFolderRef: "movies", Mode: catalogv1alpha1.ScanModeFull}}
	if err := c.Create(ctx, scan); err != nil {
		t.Fatalf("create scan: %v", err)
	}

	bus := membus.New(clockwork.NewRealClock())
	if err := bus.Ensure(ctx, events.Default()); err != nil {
		t.Fatalf("ensure: %v", err)
	}

	// Drive the LibraryScan controller's start phase (publishes the task), then
	// run the rescan worker against that exact message, then poll the
	// controller until it reports Completed -- the same three actors run.go
	// wires together in production, called directly so the test needs no live
	// NATS process (binding constraint: tests use no network).
	ctrlr := &libraryscan.Reconciler{Client: c, Bus: bus, Clock: time.Now}
	if _, err := ctrlr.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: "tick"}}); err != nil {
		t.Fatalf("start: %v", err)
	}

	msg := drainOneScanTask(t, ctx, bus) // subscribes with events.ConsumerImportScan's Subscription, returns the first delivery
	worker := rescan.NewWorker(c, bus)
	if err := worker.Handle(ctx, msg); err != nil {
		t.Fatalf("worker.Handle: %v", err)
	}

	var after catalogv1alpha1.LibraryScan
	waitFor(t, 5*time.Second, func() bool {
		if _, err := ctrlr.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: "tick"}}); err != nil {
			t.Fatalf("poll: %v", err)
		}
		if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "tick"}, &after); err != nil {
			t.Fatalf("get: %v", err)
		}
		return after.Status.Phase == catalogv1alpha1.ScanPhaseCompleted
	})

	if after.Status.FilesMatched != 1 || after.Status.ItemsCreated != 1 {
		t.Errorf("status = %+v", after.Status)
	}
	if len(after.Status.Unmatched) != 1 || after.Status.Unmatched[0].Path != bad {
		t.Fatalf("unmatched = %+v", after.Status.Unmatched)
	}
	if after.Status.Unmatched[0].Reason == "" {
		t.Error("unmatched entry has no reason")
	}

	var files catalogv1alpha1.MediaFileList
	if err := c.List(ctx, &files, client.InNamespace(ns)); err != nil {
		t.Fatalf("list media files: %v", err)
	}
	if len(files.Items) != 1 {
		t.Fatalf("got %d media files, want 1", len(files.Items))
	}
	mf := files.Items[0]
	if fm := findManager(t, mf.ManagedFields, "spec.path"); fm != string(k8s.ManagerImportarr) {
		t.Errorf("mediafile spec.path manager = %q", fm)
	}
	var movies catalogv1alpha1.MovieList
	if err := c.List(ctx, &movies, client.InNamespace(ns)); err != nil {
		t.Fatalf("list movies: %v", err)
	}
	if len(movies.Items) != 1 || mf.Spec.MediaRef.Name != movies.Items[0].Name {
		t.Fatalf("mediafile's owning movie mismatch: movies=%+v mediaRef=%+v", movies.Items, mf.Spec.MediaRef)
	}
}
```

`waitFor`, `drainOneScanTask`, `findManager`, `findStatusManager` are small local helpers (`waitFor` polls a `func() bool` with a short sleep and `t.Fatal`s on timeout, mirroring `pkg/events/contracttest`'s `waitUntil`; `findManager`/`findStatusManager` walk `metav1.ManagedFieldsEntry` for the entry whose `FieldsV1` mentions the given JSON path — `findStatusManager` filters to `Subresource == "status"` — returning its `Manager`, or `""` if none).

```bash
KUBEBUILDER_ASSETS=... go test ./importarr/... -run TestScanEndToEnd -v
git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "test(importarr): combined LibraryScan+rescan-worker envtest — MediaFile spec owner, unmatched with reason" -- importarr/controller/libraryscan/libraryscan_controller_envtest_test.go
```

- [ ] **Step 13: `pkg/events`'s exclusion-index contract, failing test first**

This is the cross-service lookup Phase D and Phase G build against, so it belongs in `pkg/events` next to `LeaseKey`/`PendingKey`, not inside the controller package. `pkg/events/exclusion_test.go`:

```go
func TestExclusionKeyFormat(t *testing.T) {
	if got, want := events.ExclusionKey("tmdb", "949"), "tmdb:949"; got != want {
		t.Errorf("ExclusionKey = %q, want %q", got, want)
	}
}

func TestExclusionEntryEncodeDecodeRoundTrip(t *testing.T) {
	in := events.ExclusionEntry{Namespace: "media", Name: "no-heat-1995", Kind: "movie", Reason: "already have a better cut"}
	data, err := in.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	out, err := events.DecodeExclusionEntry(data)
	if err != nil {
		t.Fatalf("DecodeExclusionEntry: %v", err)
	}
	if out != in {
		t.Errorf("got %+v want %+v", out, in)
	}
}
```

```bash
go test ./pkg/events/...
```

Fails: `events.ExclusionKey` undefined. Implement `pkg/events/exclusion.go`:

```go
package events

import (
	"encoding/json"
	"fmt"
)

// ExclusionKey builds the clustarr-import-exclusions key for one external id
// pair, e.g. ExclusionKey("tmdb", "949") -> "tmdb:949". The provider id alone
// is the key -- two ImportExclusions can only legitimately target the same
// centrally-assigned id (tmdb/imdb/tvdb/...) if they mean the same real-world
// item, so no ImportExclusion identity needs to be part of the key.
func ExclusionKey(idKey, idValue string) string { return idKey + ":" + idValue }

// ExclusionEntry is the value an ImportExclusion controller puts at an
// ExclusionKey, and what an import-list or search path decodes after a
// successful KV.Get to learn why the id is blocked.
type ExclusionEntry struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Reason    string `json:"reason,omitempty"`
}

// Encode marshals e for a KV Put.
func (e ExclusionEntry) Encode() ([]byte, error) {
	data, err := json.Marshal(e)
	if err != nil {
		return nil, fmt.Errorf("events: encode exclusion entry: %w", err)
	}
	return data, nil
}

// DecodeExclusionEntry unmarshals a KV entry written by Encode.
func DecodeExclusionEntry(data []byte) (ExclusionEntry, error) {
	var e ExclusionEntry
	if err := json.Unmarshal(data, &e); err != nil {
		return ExclusionEntry{}, fmt.Errorf("events: decode exclusion entry: %w", err)
	}
	return e, nil
}
```

Add `BucketImportExclusions = "clustarr-import-exclusions"` to `pkg/events/subjects.go`'s bucket-name const block (next to `BucketDedup`), and register it in `pkg/events/topology.go`'s `defaultBuckets()`:

```go
b(BucketImportExclusions, 0, "External-id exclusion index; put and deleted by the ImportExclusion controller."),
```

(`ttl=0` matches `BucketLeases`'s "durable, never expires" convention — an exclusion is a standing block, not ephemeral progress.)

```bash
go test ./pkg/events/...
git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "feat(events): ImportExclusion KV lookup contract (ExclusionKey/ExclusionEntry/BucketImportExclusions)" -- pkg/events/exclusion.go pkg/events/exclusion_test.go pkg/events/subjects.go pkg/events/topology.go
```

- [ ] **Step 14: ImportExclusion controller — validate and index, failing envtest first**

`importarr/controller/importexclusion/importexclusion_controller_envtest_test.go`:

```go
func TestReconcilePutsAnIndexKeyPerRecognizedExternalID(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	ns := createNamespace(t, ctx, c, "iex-index")
	ex := &catalogv1alpha1.ImportExclusion{
		ObjectMeta: metav1.ObjectMeta{Name: "no-heat", Namespace: ns},
		Spec: catalogv1alpha1.ImportExclusionSpec{
			Kind:        catalogv1alpha1.ExclusionKindMovie,
			ExternalIDs: map[string]string{catalogv1alpha1.ExclusionIDKeyTMDB: "949", catalogv1alpha1.ExclusionIDKeyIMDB: "tt0113277"},
			Title:       "Heat", Year: 1995, Reason: "duplicate of the extended cut we already track",
		},
	}
	if err := c.Create(ctx, ex); err != nil {
		t.Fatalf("create: %v", err)
	}

	bus := membus.New(clockwork.NewRealClock())
	if err := bus.Ensure(ctx, events.Default()); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	r := &importexclusion.Reconciler{Client: c, Bus: bus, Clock: time.Now}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: "no-heat"}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	for _, key := range []string{events.ExclusionKey("tmdb", "949"), events.ExclusionKey("imdb", "tt0113277")} {
		entry, err := bus.KV(events.BucketImportExclusions).Get(ctx, key)
		if err != nil {
			t.Fatalf("get %q: %v", key, err)
		}
		got, err := events.DecodeExclusionEntry(entry.Value)
		if err != nil {
			t.Fatalf("decode %q: %v", key, err)
		}
		if got.Reason != ex.Spec.Reason || got.Kind != "movie" {
			t.Errorf("entry for %q = %+v", key, got)
		}
	}

	var after catalogv1alpha1.ImportExclusion
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "no-heat"}, &after); err != nil {
		t.Fatalf("get: %v", err)
	}
	if !k8s.IsConditionTrue(after.Status.Conditions, catalogv1alpha1.ImportExclusionConditionReady) {
		t.Errorf("conditions = %+v, want Ready=True", after.Status.Conditions)
	}
}
```

Fails: package does not exist. Implement `importarr/controller/importexclusion/importexclusion_controller.go`: `Reconcile` gets the object, `client.IgnoreNotFound`; on delete with the finalizer present, removes every previously-applied index key (Step 15) then `RemoveFinalizer`; otherwise `EnsureFinalizer`, validates at least one `ExternalIDs` key is recognized (`ExclusionIDKeyTMDB/TVDB/IMDB/MusicBrainz/OpenLibrary/ASIN/ComicVine/MangaDex`) — none recognized → `PatchStatus` `Ready=False/ReasonInvalidSpec` then `reconcile.TerminalError`; else `Put`s `events.ExclusionEntry{Namespace, Name, Kind: string(spec.Kind), Reason: spec.Reason}` under `events.ExclusionKey(key, value)` for every recognized pair, `PatchStatus` `Ready=True/ReasonReconciled` under `k8s.ManagerImportarr`.

```bash
KUBEBUILDER_ASSETS=... go test ./importarr/controller/importexclusion/... -run TestReconcilePutsAnIndexKeyPerRecognizedExternalID
git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "feat(importarr): ImportExclusion controller — validate and index recognized external ids" -- importarr/controller/importexclusion/importexclusion_controller.go importarr/controller/importexclusion/importexclusion_controller_envtest_test.go
```

- [ ] **Step 15: ImportExclusion controller — stale-key cleanup on spec edit and on delete, failing test first**

```go
func TestReconcileRemovesStaleIndexKeysWhenExternalIDsChange(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	ns := createNamespace(t, ctx, c, "iex-stale")
	ex := &catalogv1alpha1.ImportExclusion{
		ObjectMeta: metav1.ObjectMeta{Name: "no-heat", Namespace: ns},
		Spec: catalogv1alpha1.ImportExclusionSpec{Kind: catalogv1alpha1.ExclusionKindMovie, ExternalIDs: map[string]string{"tmdb": "949"}},
	}
	if err := c.Create(ctx, ex); err != nil {
		t.Fatalf("create: %v", err)
	}
	bus := membus.New(clockwork.NewRealClock())
	if err := bus.Ensure(ctx, events.Default()); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	r := &importexclusion.Reconciler{Client: c, Bus: bus, Clock: time.Now}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: "no-heat"}}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}

	var current catalogv1alpha1.ImportExclusion
	if err := c.Get(ctx, req.NamespacedName, &current); err != nil {
		t.Fatalf("get: %v", err)
	}
	current.Spec.ExternalIDs = map[string]string{"imdb": "tt0113277"} // tmdb dropped, imdb added
	if err := c.Update(ctx, &current); err != nil {
		t.Fatalf("update: %v", err)
	}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}

	if _, err := bus.KV(events.BucketImportExclusions).Get(ctx, events.ExclusionKey("tmdb", "949")); !errors.Is(err, events.ErrKeyNotFound) {
		t.Errorf("stale tmdb key still present: err=%v", err)
	}
	if _, err := bus.KV(events.BucketImportExclusions).Get(ctx, events.ExclusionKey("imdb", "tt0113277")); err != nil {
		t.Errorf("new imdb key missing: %v", err)
	}
}

func TestReconcileRemovesIndexKeysOnDelete(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	ns := createNamespace(t, ctx, c, "iex-delete")
	ex := &catalogv1alpha1.ImportExclusion{
		ObjectMeta: metav1.ObjectMeta{Name: "no-heat", Namespace: ns},
		Spec:       catalogv1alpha1.ImportExclusionSpec{Kind: catalogv1alpha1.ExclusionKindMovie, ExternalIDs: map[string]string{"tmdb": "949"}},
	}
	if err := c.Create(ctx, ex); err != nil {
		t.Fatalf("create: %v", err)
	}
	bus := membus.New(clockwork.NewRealClock())
	if err := bus.Ensure(ctx, events.Default()); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	r := &importexclusion.Reconciler{Client: c, Bus: bus, Clock: time.Now}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: "no-heat"}}
	if _, err := r.Reconcile(ctx, req); err != nil { // adds the finalizer, puts the key
		t.Fatalf("first reconcile: %v", err)
	}
	if err := c.Delete(ctx, ex); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := r.Reconcile(ctx, req); err != nil { // runs the finalizer
		t.Fatalf("second reconcile: %v", err)
	}
	if _, err := bus.KV(events.BucketImportExclusions).Get(ctx, events.ExclusionKey("tmdb", "949")); !errors.Is(err, events.ErrKeyNotFound) {
		t.Errorf("index key survived delete: err=%v", err)
	}
	if err := c.Get(ctx, req.NamespacedName, &catalogv1alpha1.ImportExclusion{}); !apierrors.IsNotFound(err) {
		t.Errorf("object survived finalizer removal: err=%v", err)
	}
}
```

Implement the stale-key tracking: the controller records the keys it last applied in an annotation (`catalog.clustarr.io/exclusion-keys`, a comma-joined, sorted `key:value` list — metadata, not status, so writing it does not touch the single-writer-restricted subresource) via `k8s.Apply` on the main resource; on each reconcile it diffs the new key set against the annotation's, `Delete`s anything dropped, `Put`s anything added or changed, then re-applies the annotation. On delete (with the finalizer present), it `Delete`s every key named in the annotation before removing the finalizer. A finalizer is required here (unlike `LibraryScan`): these KV entries are durable, not TTL'd, so skipping cleanup would leave an exclusion silently in force forever after the user deleted it — a real correctness bug, not a cosmetic one.

```bash
KUBEBUILDER_ASSETS=... go test ./importarr/controller/importexclusion/...
git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "feat(importarr): ImportExclusion controller — stale-key cleanup on edit and finalizer cleanup on delete" -- importarr/controller/importexclusion
```

- [ ] **Step 16: wire ImportExclusion into `setupControllers` (already added in Step 11's snippet) — verify the build**

Step 11 already added the `importexclusion.Reconciler` registration to `setupControllers`. This step is the checkpoint that it actually compiles and starts cleanly once `importexclusion` exists (Step 11 was written before Steps 13–15 existed, so this is where the wiring first has a real package to point at):

```bash
go build ./importarr/...
go vet ./importarr/...
KUBEBUILDER_ASSETS=... go test ./importarr/...
git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "chore(importarr): confirm ImportExclusion wiring in setupControllers builds and passes" -- importarr/run.go
```

(If Step 11's commit already included a working `importexclusion` import — it will, since these steps are executed in order by the same implementer — this step is a no-op verification; commit only if `git status` shows a diff.)

- [ ] **Step 17: full gate**

```bash
go build ./importarr/... ./pkg/events/...
go vet ./importarr/... ./pkg/events/...
go test -count=1 -race ./importarr/... ./pkg/events/...
KUBEBUILDER_ASSETS=$(go run sigs.k8s.io/controller-runtime/tools/setup-envtest use -p path 1.37.0) \
  go test -count=1 ./importarr/... ./pkg/events/... -v 2>&1 | grep -c SKIP   # must print 0 envtest skips for this task's packages
make lint
```

Final path-scoped commit for anything left uncommitted from the steps above (there should be none if each step committed its own slice; this step is the safety net):

```bash
git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "chore(importarr): rescan and importexclusion gate green" -- importarr pkg/events/schema/importarr.go pkg/events/schema/schema_test.go pkg/events/exclusion.go pkg/events/subjects.go pkg/events/topology.go
```

**Verification:**
- `go build ./importarr/... ./pkg/events/...` — never bare `go build`.
- `go vet ./importarr/... ./pkg/events/...`.
- `go test -count=1 -race ./importarr/... ./pkg/events/...` without `KUBEBUILDER_ASSETS` — the pure-function tests (`match_test.go`, `progress_test.go`, `importarr_test.go`, `exclusion_test.go`) pass; every `*_envtest_test.go` in the four new packages SKIPs (that is expected and is not the gate).
- `KUBEBUILDER_ASSETS=$(go run sigs.k8s.io/controller-runtime/tools/setup-envtest use -p path 1.37.0) go test -count=1 ./importarr/...` — this is the real gate; every `*_envtest_test.go` must actually RUN (not skip) and pass, per CLAUDE.md's documented gotcha that a millisecond-fast "pass" without this env var means the suite never touched a real apiserver.
- `make lint` — forbidigo must show zero hits for `Status().Update`/`Status().Patch()` outside `pkg/k8s` (the test-fixture-seeding `c.Status().Update(ctx, ...)` calls in Steps 6–8/12 are test files seeding a starting state, not production code path; confirm golangci's forbidigo config excludes `_test.go`, and if it does not, seed status via a second `k8s.PatchStatus` call under a throwaway field manager instead — check `.golangci.yml`'s forbidigo exclude rules first before changing the test). Confirm separately that `go list -deps ./importarr/worker/rescan/...` does **not** include `pkg/mediainfo` (correction 2).

**Done when:**
- [ ] `pkg/events/schema.ScanTask` exists, round-trips, and is unique/versioned per `TestSchemaNamesAreUniqueAndVersioned`.
- [ ] `pkg/events.ExclusionKey`/`ExclusionEntry`/`BucketImportExclusions` exist, round-trip, and are registered in `Default()`'s topology.
- [ ] `rescan.MatchMovie` is a pure function with table tests covering: embedded tmdb id (existing + create-new), embedded imdb id resolved through the gateway, an imdb id the gateway cannot resolve (unmatched), exact title+year match against an existing movie, an ambiguous title shared by two existing movies (unmatched, candidates listed), and no id + no title match (unmatched).
- [ ] `rootfolderschedule.Reconciler` creates exactly one `LibraryScan` per cron tick, never duplicates within a tick, never writes any `RootFolder.status` field (asserted directly), and turns an invalid cron expression into a `Warning` Event plus `reconcile.TerminalError`.
- [ ] `libraryscan.Reconciler` is the only writer of `LibraryScan.status` (field manager `importarr`): it publishes exactly one `ScanTask` per scan, polls `clustarr-progress` and aggregates into status, caps `Unmatched` at 200 newest-first, and deletes the scan once `ttlSecondsAfterFinished` has elapsed since completion.
- [ ] `rescan.Worker.Handle` classifies files with `pkg/fsops` (part/extra/sample skipped, never reach matching), parses with `pkg/release.ParsePath`, matches with `MatchMovie`, and creates or updates the `Movie` and the **`MediaFileSpec`** (never `MediaFileStatus`, never via `pkg/mediainfo`) under field manager `importarr` — populating `mediaRef`/`path`/`sizeBytes`/`modTime`/`quality`/`revision`/`releaseType`/`releaseGroup`/`edition`/`languages` and deliberately leaving `formatScore`/`matchedFormats`/`profileHash` at zero (disagreement note 8).
- [ ] The worker never reclaims `spec.sizeBytes`/`spec.modTime`/`spec.original` on a `MediaFile` whose `Original` is already `false` (catalogarr owns those three post-transcode) — proven by `TestHandleDoesNotReclaimFieldsCatalogarrOwnsPostTranscode`.
- [ ] The combined envtest (Step 12) proves a planted file becomes a `MediaFile` (spec-only, field manager `importarr`, no status written) linked to the `Movie` it was matched/created against, and a second, unmatchable planted file lands in `LibraryScan.status.unmatched` with a non-empty reason.
- [ ] `importexclusion.Reconciler` validates that `ExternalIDs` has at least one recognized provider key, indexes every recognized pair into `clustarr-import-exclusions` (`ExclusionKey` → `ExclusionEntry`), removes stale keys on a spec edit, and removes all of its keys via a finalizer before the object is actually deleted.
- [ ] `importarr/run.go`'s `setupControllers`/`setupWorkers` register all three controllers and the rescan worker's subscription; `go build ./...` (via `go build ./importarr/...`, never bare) succeeds.
- [ ] `make lint` clean; `go test -race` clean; the envtest run (with `KUBEBUILDER_ASSETS` set) actually executes every `*_envtest_test.go` in the four owned packages rather than skipping.

---

## Wave 3 — end-to-end proof on kind (after every wave-2 task passed review)

### Task C11: the end-to-end harness on kind, and Phase C's three scenarios

**Files:**
- Create: `test/e2e/main_test.go`
- Create: `test/e2e/helpers_test.go`
- Create: `test/e2e/series_test.go`
- Create: `test/e2e/libraryscan_test.go`
- Create: `test/e2e/mediafile_test.go`
- Create: `test/e2e/.gitignore`
- Create: `test/e2e/artifacts/.gitkeep`
- Create: `test/fixtures/main.go`
- Create: `test/fixtures/root.go`
- Create: `test/fixtures/tmdbstub/server.go`
- Create: `test/fixtures/tvdbstub/server.go`
- Create: `test/fixtures/tvdbstub/testdata/episodes_121361_extended.json`
- Create: `test/fixtures/tvdbstub/testdata/series_900001.json`
- Create: `test/fixtures/tvdbstub/testdata/episodes_900001.json`
- Create: `test/fixtures/tvdbstub/testdata/series_900002.json`
- Create: `test/fixtures/tvdbstub/testdata/episodes_900002.json`
- Create: `test/fixtures/seed/seed.go`
- Create: `images/Dockerfile.e2e-fixtures`
- Create: `config/e2e/kustomization.yaml`
- Create: `config/e2e/tmdb-stub.yaml`
- Create: `config/e2e/tvdb-stub.yaml`
- Create: `config/e2e/metadata-providers.yaml`
- Create: `config/e2e/quality-profile.yaml`
- Create: `config/e2e/resources-patch.yaml`
- Create: `hack/e2e.sh`
- Test: the `test/e2e` package itself is the test (build-tagged `e2e`); no separate `_test.go` gate exists for it, since it can only be verified against a real kind cluster (see **Verification**).

**Path ownership:** `test/e2e/`, `test/fixtures/`, `config/e2e/`, `images/Dockerfile.e2e-fixtures`, `hack/e2e.sh`. Do not touch `hack/kind.sh`, `config/default/`, `config/manager/`, `config/crd/`, `Makefile`, or `README.md` — `make e2e` already exists and its contract (`go test ./test/e2e/... -tags e2e -timeout 30m`) is fixed; `hack/kind.sh` already creates the cluster, the `/data` hostPath mount, the namespace and NATS and this task builds on it without duplicating it.

**Read first:**
- `docs/superpowers/plans/2026-09-18-remaining-work.md:895-1012` — Phase H in full: the harness, the fixture roster, all 16 scenarios, and the per-phase rule at `:989-994` that pins Phase C to scenarios 5, 7, 8. Binding.
- Same file, `:842-863` — Phase C's own section: the gate, the trace-propagation note (not this task's concern), and the one-line E2E rule at `:863`.
- `docs/superpowers/specs/2026-09-18-clustarr-design.md:806-814` (§14 Testing) — the original E2E paragraph this task supersedes with real code, and `:798-800` (§12) for replica counts, `GOMEMLIMIT`, and resource shapes to mirror in `config/e2e`'s patches.
- `docs/superpowers/specs/2026-09-18-clustarr-design-amendment-1.md:79-165` (§A1.4 LibraryScan kind, §A1.5 matching, §A1.6 topology) and `:51-78` (§A1.3 ownership — this is the section that actually specifies the two-writer `MediaFile` split; the remaining-work plan's Phase C prose cites it as "§A5", which is wrong — §A5 in the amendment is "Risks this adds". Follow §A1.3, note the mislabel).
- `docs/research/k8s.md:407-421` (§8) — the envtest-vs-kind distinction that matters most here: envtest runs no kube-controller-manager, so `Deployment.status.conditions` never populates. `TestMain`'s "every Deployment is Available" gate is **structurally impossible to verify under envtest** and can only be exercised against a real cluster; do not try to unit-test it.
- `hack/kind.sh` in full — cluster name `clustarr` / context `kind-clustarr`, namespace `clustarr-system`, `/data` layout (`ensure_data_dir`: `torrents`, `usenet`, `media/{movies,tv,music,books,audiobooks,comics}`, `.recycle`), the `clustarr-data` PV/PVC binding, NATS via `config/nats` kustomize. `CLUSTARR_DATA_DIR` defaults to `$PWD/.data`; `hack/e2e.sh` must set it explicitly rather than relying on the default so `test/e2e` never has to guess a repo-relative path.
- `config/manager/kustomization.yaml` — the ten Deployments this task's readiness gate must see `Available`: `catalogarr`, `catalogarr-metadata`, `importarr`, `importarr-worker`, `indexarr`, `grabarr`, `squasharr`, `captionarr`, `captionarr-worker`, `ui`, all labelled `app.kubernetes.io/part-of=clustarr` by `config/manager/kustomization.yaml`'s top-level `labels:` block.
- `config/crd/kustomization.yaml` — 29 CRD files, labelled `app.kubernetes.io/part-of=clustarr` with `includeSelectors: false` (the label lands on the CRD objects themselves).
- `config/nats/service.yaml` — the NATS Service exposes `monitor` on port 8222 (`http://nats.clustarr-system.svc:8222/jsz`), which `hack/e2e.sh` port-forwards to dump JetStream state on failure. `hack/kind.sh`'s own `wait_for_nats` polls `rollout status statefulset/nats`; mirror that shape (`appsv1.StatefulSet` `status.readyReplicas`) in the Go readiness gate rather than dialing NATS directly — **the test process runs on the host against the kind kubeconfig context, not inside the cluster, so it can reach the API server but not ClusterIP Services** (no `extraPortMappings` for 4222/8222/80 in `hack/kind.sh`'s `create_cluster`). Every scenario touches NATS and the fixtures only indirectly, through controllers and workers that run in-cluster; the only channel the test process shares with the cluster is (a) the Kubernetes API and (b) the host directory kind mounts at `/data`.
- `images/Dockerfile.controller` and `images/Dockerfile.media` — the two-stage distroless-static / cgo-bookworm shapes `images/Dockerfile.e2e-fixtures` should read like a sibling of, including the `org.opencontainers.image.*` labels and non-root `USER`.
- `pkg/k8s/fieldmanager.go` — `ManagerCatalogarr = "catalogarr"`, `ManagerImportarr = "importarr"` (exact strings scenario 8 asserts against `managedFields[].manager`).
- `pkg/k8s/scheme.go:82-89` — `func MustNewScheme() *runtime.Scheme`, the scheme this task's client must use.
- `pkg/metadata/clients/tvdb/tvdb.go` (full) and `auth.go` (full), plus `tvdb_test.go:37-138` for the exact mock-server route shapes already exercised in Phase B: `POST /login` → `{"data":{"token":"..."}}`; `GET /series/{id}/extended` → the `seriesExtendedResponse` envelope; `GET /series/{id}/episodes/{order}` → the `episodesResponse` envelope. `test/fixtures/tvdbstub` reuses these exact field names.
- `pkg/metadata/clients/tmdb/tmdb.go:1-90` and `tmdb_test.go:38,139-145` — endpoints are `/movie/{id}` and `/find/{imdb_id}` relative to `baseURL` (golang-tmdb strips any `/3` prefix internally), verified against `movie_27205.json` and `find_imdb_tt1375666.json`.
- `testdata/metadata/tvdb/{login,series_121361,episodes_121361_default,updates_since}.json` and `testdata/metadata/tmdb/{movie_27205,find_imdb_tt1375666,find_imdb_notfound}.json` — the recorded fixtures this task's stubs re-serve verbatim for TVDB series 121361 (Game of Thrones) and TMDB movie 27205 (Fight Club).
- `api/catalog/v1alpha1/{libraryscan,rootfolder,series,episode,mediafile,metadataprovider,qualityprofile,movie}_types.go` (full) and `api/common/v1alpha1/{media_types,release_types,quality_types}.go` — every field name and enum value used below is copied from these files, not guessed.
- `pkg/events/subjects.go:29-66,254-277` — `StreamEvents`, `StreamWorkImportarr`, `StreamDLQ`, `FilterWorkImportarr`, `WorkScanSubject` (used only in the failure-dump section of `hack/e2e.sh`, via the NATS monitor HTTP endpoint, not from Go).
- `docs/research/naming.md:72` — `standard | daily | anime` series types; anime uses `{absolute:000}`, daily uses `{Air-Date}`. Confirms `SeriesType` values used below.

**Dependencies:** none new. Everything used below is already in `go.mod`, verified by reading it directly (no `go list -m -versions` lookup needed — that command is for picking a version of something new; nothing new is added here):
- `sigs.k8s.io/controller-runtime v0.25.1` — `pkg/client`, `pkg/client/config` (`GetConfigWithContext`).
- `k8s.io/client-go v0.37.0`, `k8s.io/apimachinery v0.37.0` (`pkg/util/wait`, `pkg/util/rand`, `pkg/apis/meta/v1`), `k8s.io/api v0.37.0` (`apps/v1`, `core/v1`).
- `k8s.io/apiextensions-apiserver v0.37.0` — present in `go.mod` but marked `// indirect`. Importing `k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1` directly from `test/e2e` compiles as-is against the existing `go.sum`; the `// indirect` comment is cosmetic and only `go mod tidy` would rewrite it. **Do not run `go mod tidy`** — CLAUDE.md and the shared context both ban it from a worker; the stale comment is harmless.
- `github.com/stretchr/testify v1.12.1` — assertions in every scenario file.
- `github.com/spf13/cobra v1.10.2` — `test/fixtures`'s own root command, matching `cmd/clustarr`'s convention.
- Go stdlib `net/http`, `encoding/json`, `io`, `os`, `path/filepath` for the fixture servers — no HTTP router library needed, the route counts are small enough for a `switch r.URL.Path`.

**Interfaces — Consumes:**
```go
// pkg/k8s/scheme.go:82
func MustNewScheme() *runtime.Scheme

// pkg/k8s/fieldmanager.go
const ManagerCatalogarr FieldManager = "catalogarr"
const ManagerImportarr  FieldManager = "importarr"

// sigs.k8s.io/controller-runtime/pkg/client/config
func GetConfigWithContext(context string) (*rest.Config, error)
```
Everything else this task depends on is `(reconciled by controller)` — written by sibling Phase C tasks this task does not own (the `catalogarr` Series/Episode/MediaFile/QualityProfile/MetadataProvider controllers, the `importarr` LibraryScan/RootFolder-schedule controllers and rescan worker). The assumed shapes, stated precisely because the scenarios below are written against them:
- **Series controller.** Given `Series.Spec{TvdbID, QualityProfileRef, RootFolderRef}`, calls the metadata gateway for a provider of `MetadataProvider.Spec.Type=tvdb`, creates one `Episode` per entry the provider returns (owned by the `Series`), and once synced sets `Series.Status.Phase=Ready`, `Status.Conditions[Ready,MetadataReady,EpisodesSynced]=True`, and **`Status.Path`** to the resolved on-disk folder under the `RootFolder`. This task never predicts the exact path string (dialect/token formatting is `pkg/naming`, Phase B, already built) — every scenario polls `Series.Status.Path` until non-empty and plants files under *that* value, so it is decoupled from the naming implementation.
- **importarr LibraryScan / rescan worker (amendment §A1.5).** Given a `LibraryScan`, walks `RootFolder.Spec.Path`, and for each file: parses with `pkg/release`, probes with `pkg/mediainfo`, resolves the owning catalog item by an embedded `{tmdbid-N}`/`{tvdbid-N}` token in an ancestor folder name, and **upserts** the matching item (§A1.1: "upsert...for each file found") — including creating a not-yet-known `Episode` when the season/episode number parses unambiguously against an already-identified `Series`, and creating a not-yet-known `Movie` when a `{tmdbid-N}` token parses unambiguously with no existing catalog item. It creates one `MediaFile` per matched file (field-managed on `status.file`/`status.probe` only, per §A1.3), sets `MediaFileSpec.ReleaseType=seasonPack` when the parent release parses as a season pack, skips sample/extra files without creating a `MediaFile` for them (incrementing `LibraryScanStatus.FilesSkipped` and/or recording them in `Status.Unmatched` with a reason — this task's scenario 7 assertion accepts either, see Step 22), and records anything it cannot confidently attribute in `Status.Unmatched` with a `Reason`, never inventing a speculative item. Sets `LibraryScan.Status.Phase=Completed` when done.
- **RootFolder schedule (amendment §A1.6).** When `RootFolder.Spec.ScanSchedule` fires, importarr creates a new `LibraryScan` for that `RootFolder`; this task does not assume a naming scheme for it and instead lists all `LibraryScan`s and filters by `Spec.RootFolderRef` in Go (Step 21).
- **catalogarr MediaFile controller (amendment §A1.3).** Once `importarr` creates a `MediaFile` with `status.file`/`status.probe` populated, `catalogarr` reconciles the same object and applies `status.quality`, `status.formatScore` and `status.conditions` through `ManagerCatalogarr`, scoring the file against the profile named in `Spec.QualityProfileRef`'s effective default (or the item's own resolved `QualityProfileRef`).

If any sibling task's real behaviour differs from these assumptions (a different embedded-token format, no upsert-on-unknown-episode, a different skip/unmatched split for samples), the fix is a small, local edit to the affected scenario's assertions — not a redesign of the harness.

**Interfaces — Produces** (later phases' scenario files land in the same `test/e2e` package and reuse these):
```go
package e2e // +build e2e

const Namespace   = "clustarr-system" // config/default declares exactly one namespace
const KubeContext = "kind-clustarr"   // hack/kind.sh's $CONTEXT

var k8sClient client.Client // set once in TestMain, read-only after that

func uniqueName(prefix string) string                                   // "<prefix>-<5 lowercase chars>"
func dataDir() string                                                    // reads $CLUSTARR_DATA_DIR, fatal if unset
func hostPath(clusterPath string) string                                 // "/data/x" -> "<dataDir>/x"
func plantFile(t *testing.T, hostAbsPath string, content []byte)         // MkdirAll + WriteFile, t.Cleanup removes it
func sampleClipBytes(t *testing.T) []byte                                // reads the seeded tiny clip once, cached
func waitFor(t *testing.T, ctx context.Context, timeout time.Duration, desc string, check func(context.Context) (bool, error))
func managedFieldsTouch(entry metav1.ManagedFieldsEntry, dottedPaths ...string) (bool, error)
```
`test/fixtures`'s binary contract for later phases: `clustarr-e2e-fixtures <name>` where `<name>` is one of `tmdb-stub`, `tvdb-stub`, `seed` today; Phase D adds `torznab-stub`, `seeder`, `nntp-stub` as new subcommands under `test/fixtures/<name>/` plus a new `RUN` stage in `images/Dockerfile.e2e-fixtures`, without touching `main.go`/`root.go`'s dispatch shape. `config/e2e/`'s one-fixture-per-file layout (`tmdb-stub.yaml`, `tvdb-stub.yaml`, ...) is deliberate so Phase D can add `config/e2e/torznab-stub.yaml` and one line in `kustomization.yaml`'s `resources:` list without editing this task's files.

---

## Part A — the fixture binary (`test/fixtures/`)

- [ ] **Step 1: scaffold `clustarr-e2e-fixtures` with a failing dispatch.**
  Write `test/fixtures/main.go`:
  ```go
  // Command clustarr-e2e-fixtures serves the in-cluster stubs the Phase C e2e
  // suite runs against: no fixture and no test reaches the real Internet
  // (docs/superpowers/plans/2026-09-18-remaining-work.md Phase H).
  package main

  import (
      "fmt"
      "os"
  )

  func main() {
      if err := NewRootCommand().Execute(); err != nil {
          fmt.Fprintln(os.Stderr, "error:", err)
          os.Exit(1)
      }
  }
  ```
  Write `test/fixtures/root.go`:
  ```go
  package main

  import "github.com/spf13/cobra"

  // NewRootCommand wires one subcommand per fixture Deployment. Phase D adds
  // torznab-stub/seeder/nntp-stub here; Phase F adds opensubtitles-stub and
  // gestdown-stub. Each subcommand owns its own flags and never imports
  // another fixture's package.
  func NewRootCommand() *cobra.Command {
      root := &cobra.Command{
          Use:   "clustarr-e2e-fixtures",
          Short: "In-cluster stub services for the Phase C+ e2e suite",
      }
      root.AddCommand(newTMDBStubCommand())
      root.AddCommand(newTVDBStubCommand())
      root.AddCommand(newSeedCommand())
      return root
  }
  ```
  Both boilerplate-header the GPL block from `hack/boilerplate.go.txt` at the top (as every task's Go file must).
  Run: `go build -o /tmp/c11-check ./test/fixtures/` — **expected failure**: `undefined: newTMDBStubCommand` (and the other two), since only `root.go`/`main.go` exist. This is the "red" for an infra binary with no unit-test surface of its own — subsequent steps turn it green one subcommand at a time.
  Commit (path-scoped): `git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "e2e: scaffold clustarr-e2e-fixtures cobra root" -- test/fixtures/main.go test/fixtures/root.go`

- [ ] **Step 2: TMDB stub, serving the recorded fixtures verbatim.**
  Write `test/fixtures/tmdbstub/server.go`:
  ```go
  // Package tmdbstub serves TMDB's /movie/{id} and /find/{imdb_id} shapes
  // (pkg/metadata/clients/tmdb/tmdb.go, verified against tmdb_test.go) from
  // the recorded JSON under testdata/metadata/tmdb/. It never talks to the
  // real TMDB API -- Phase H's harness runs with no Internet access at all.
  package tmdbstub

  import (
      "log/slog"
      "net/http"
      "os"
      "path/filepath"
  )

  // NewHandler builds the stub. recordedDir holds tmdb's login/movie/find
  // JSON copied verbatim from testdata/metadata/tmdb (images/Dockerfile.e2e-fixtures
  // COPYs it there at image-build time; local `go run` callers point
  // --recorded-dir at ../../testdata/metadata/tmdb instead).
  func NewHandler(recordedDir string, logger *slog.Logger) http.Handler {
      mux := http.NewServeMux()
      mux.HandleFunc("GET /movie/27205", serveFile(filepath.Join(recordedDir, "movie_27205.json"), logger))
      mux.HandleFunc("GET /find/tt1375666", serveFile(filepath.Join(recordedDir, "find_imdb_tt1375666.json"), logger))
      mux.HandleFunc("GET /find/{imdb}", serveFile(filepath.Join(recordedDir, "find_imdb_notfound.json"), logger))
      return mux
  }

  func serveFile(path string, logger *slog.Logger) http.HandlerFunc {
      return func(w http.ResponseWriter, r *http.Request) {
          b, err := os.ReadFile(path)
          if err != nil {
              logger.Error("tmdbstub: recorded fixture missing", "path", path, "error", err)
              http.Error(w, "fixture not found", http.StatusNotFound)
              return
          }
          w.Header().Set("Content-Type", "application/json")
          _, _ = w.Write(b)
      }
  }
  ```
  Write `test/fixtures/tmdbstub_cmd.go` (root package, wires the flag+listener):
  ```go
  package main

  import (
      "fmt"
      "log/slog"
      "net/http"
      "os"

      "github.com/spf13/cobra"

      "github.com/mediactl/clustarr/test/fixtures/tmdbstub"
  )

  func newTMDBStubCommand() *cobra.Command {
      var (
          addr        string
          recordedDir string
      )
      cmd := &cobra.Command{
          Use:   "tmdb-stub",
          Short: "Serve the recorded TMDB fixtures on --addr",
          RunE: func(cmd *cobra.Command, args []string) error {
              logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
              logger.Info("tmdb-stub: listening", "addr", addr, "recorded_dir", recordedDir)
              return http.ListenAndServe(addr, tmdbstub.NewHandler(recordedDir, logger))
          },
      }
      cmd.Flags().StringVar(&addr, "addr", ":8080", "listen address")
      cmd.Flags().StringVar(&recordedDir, "recorded-dir", "/fixtures/testdata/metadata/tmdb", "directory holding the recorded TMDB JSON")
      return cmd
  }

  var _ = fmt.Sprintf // silence unused import if RunE grows; remove once genuinely used elsewhere
  ```
  (Drop the trailing `var _ = fmt.Sprintf` line — it is a placeholder to keep the snippet self-contained; the real file has no unused imports. Remove the `"fmt"` import too.)
  Run: `go build -o /tmp/c11-check ./test/fixtures/` — expected failure until Steps 3-4 add the other two subcommands (`undefined: newTVDBStubCommand`, `undefined: newSeedCommand`).
  Commit: `git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "e2e: TMDB stub serving recorded fixtures" -- test/fixtures/tmdbstub test/fixtures/tmdbstub_cmd.go`

- [ ] **Step 3: TVDB stub — recorded series 121361, plus two fixture-owned series for the daily and anime cases.**
  First write the three fixture-owned JSON files this stub needs beyond what's recorded in `testdata/metadata/`. These live under `test/fixtures/tvdbstub/testdata/` (this task's own path, embedded via `go:embed`, not a modification of `testdata/metadata/` which Phase B owns) and use the **exact wire shape** verified from `tvdb.go`'s `seriesExtendedResponse`/`episodesResponse` structs — only the content is invented, and it is invented from real, publicly known facts about the shows named, not guessed at the API level.

  `test/fixtures/tvdbstub/testdata/episodes_121361_extended.json` — the real recorded S01E01 plus a second, real S01E02 (TVDB doesn't change ids we don't already reference; content is public fact, format matches `episodes_121361_default.json` verbatim):
  ```json
  {
    "data": {
      "episodes": [
        {"name": "Winter Is Coming", "aired": "2011-04-17", "runtime": 62, "overview": "Lord Eddard Stark is torn between his family and an old friend when asked to serve at the side of King Robert Baratheon.", "seasonNumber": 1, "number": 1, "absoluteNumber": 1},
        {"name": "The Kingsroad", "aired": "2011-04-24", "runtime": 56, "overview": "While Bran recovers from his fall, Ned takes only his daughters to Kings Landing.", "seasonNumber": 1, "number": 2, "absoluteNumber": 2}
      ]
    }
  }
  ```

  `test/fixtures/tvdbstub/testdata/series_900001.json` — a fixture-only daily series, id `900001` (outside any real TVDB range used elsewhere in this repo):
  ```json
  {
    "data": {
      "id": 900001,
      "name": "The Fixture Nightly Wire",
      "slug": "the-fixture-nightly-wire",
      "overview": "A nightly news-desk fixture used only by the Clustarr e2e suite.",
      "firstAired": "2024-01-01",
      "lastAired": "",
      "status": {"id": 1, "name": "Continuing", "recordType": "series", "keepUpdated": false},
      "originalCountry": "us",
      "originalLanguage": "eng",
      "averageRuntime": 30,
      "remoteIds": [],
      "genres": [{"id": 1, "name": "Talk Show"}],
      "airsTime": "23:00"
    }
  }
  ```

  `test/fixtures/tvdbstub/testdata/episodes_900001.json`:
  ```json
  {
    "data": {
      "episodes": [
        {"name": "Episode for 2024-01-15", "aired": "2024-01-15", "runtime": 30, "overview": "Fixture daily episode.", "seasonNumber": 1, "number": 1, "absoluteNumber": 1}
      ]
    }
  }
  ```

  `test/fixtures/tvdbstub/testdata/series_900002.json` — a fixture-only anime series, id `900002`:
  ```json
  {
    "data": {
      "id": 900002,
      "name": "Fixture Anime",
      "slug": "fixture-anime",
      "overview": "An absolute-numbered anime fixture used only by the Clustarr e2e suite.",
      "firstAired": "2023-04-01",
      "lastAired": "",
      "status": {"id": 1, "name": "Continuing", "recordType": "series", "keepUpdated": false},
      "originalCountry": "jp",
      "originalLanguage": "jpn",
      "averageRuntime": 24,
      "remoteIds": [],
      "genres": [{"id": 2, "name": "Fantasy"}],
      "airsTime": "00:00"
    }
  }
  ```

  `test/fixtures/tvdbstub/testdata/episodes_900002.json`:
  ```json
  {
    "data": {
      "episodes": [
        {"name": "Fixture Episode Five", "aired": "2023-05-06", "runtime": 24, "overview": "Fixture anime episode, absolute-numbered.", "seasonNumber": 1, "number": 5, "absoluteNumber": 5}
      ]
    }
  }
  ```

  Now `test/fixtures/tvdbstub/server.go`:
  ```go
  // Package tvdbstub serves TheTVDB v4's login/series/episodes shapes
  // (pkg/metadata/clients/tvdb/{tvdb,auth}.go) for one real, recorded series
  // (121361, Game of Thrones) and two fixture-owned series this task adds to
  // exercise daily and anime absolute numbering, which testdata/metadata/tvdb
  // does not record. No credential is ever checked -- the fixture answers
  // any /login body, matching "no Internet, closed network" (Phase H).
  package tvdbstub

  import (
      _ "embed"
      "log/slog"
      "net/http"
      "os"
      "path/filepath"
  )

  //go:embed testdata/episodes_121361_extended.json
  var episodes121361 []byte

  //go:embed testdata/series_900001.json
  var series900001 []byte

  //go:embed testdata/episodes_900001.json
  var episodes900001 []byte

  //go:embed testdata/series_900002.json
  var series900002 []byte

  //go:embed testdata/episodes_900002.json
  var episodes900002 []byte

  // NewHandler builds the stub. recordedDir holds the real recorded
  // login.json and series_121361.json, copied verbatim from
  // testdata/metadata/tvdb by images/Dockerfile.e2e-fixtures.
  func NewHandler(recordedDir string, logger *slog.Logger) http.Handler {
      mux := http.NewServeMux()
      mux.HandleFunc("POST /login", serveFile(filepath.Join(recordedDir, "login.json"), logger))
      mux.HandleFunc("GET /series/121361/extended", serveFile(filepath.Join(recordedDir, "series_121361.json"), logger))
      // The order segment (default|official|dvd|absolute|...) is ignored: the
      // Series controller's choice of order isn't this task's to predict, so
      // the stub answers the same, richer episode list for any of them.
      mux.HandleFunc("GET /series/121361/episodes/{order}", serveBytes(episodes121361, logger))
      mux.HandleFunc("GET /series/900001/extended", serveBytes(series900001, logger))
      mux.HandleFunc("GET /series/900001/episodes/{order}", serveBytes(episodes900001, logger))
      mux.HandleFunc("GET /series/900002/extended", serveBytes(series900002, logger))
      mux.HandleFunc("GET /series/900002/episodes/{order}", serveBytes(episodes900002, logger))
      return mux
  }

  func serveBytes(b []byte, logger *slog.Logger) http.HandlerFunc {
      return func(w http.ResponseWriter, r *http.Request) {
          w.Header().Set("Content-Type", "application/json")
          _, _ = w.Write(b)
      }
  }

  func serveFile(path string, logger *slog.Logger) http.HandlerFunc {
      return func(w http.ResponseWriter, r *http.Request) {
          b, err := os.ReadFile(path)
          if err != nil {
              logger.Error("tvdbstub: recorded fixture missing", "path", path, "error", err)
              http.Error(w, "fixture not found", http.StatusNotFound)
              return
          }
          w.Header().Set("Content-Type", "application/json")
          _, _ = w.Write(b)
      }
  }
  ```
  Write `test/fixtures/tvdbstub_cmd.go` mirroring Step 2's `tmdbstub_cmd.go` shape exactly, with `--recorded-dir` defaulting to `/fixtures/testdata/metadata/tvdb` and `Use: "tvdb-stub"`.
  Run: `go build -o /tmp/c11-check ./test/fixtures/` — expected failure: `undefined: newSeedCommand`.
  Commit: `git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "e2e: TVDB stub, recorded GoT plus fixture-owned daily/anime series" -- test/fixtures/tvdbstub test/fixtures/tvdbstub_cmd.go`

- [ ] **Step 4: the `seed` subcommand and the baked-in tiny clip.**
  `test/fixtures/seed/seed.go`:
  ```go
  // Package seed copies the tiny lavfi-generated clip baked into the fixture
  // image (by images/Dockerfile.e2e-fixtures's ffmpeg build stage) onto the
  // shared /data volume, so test/e2e can "plant" ffprobe-able media by a
  // plain filesystem copy -- no ffmpeg needed on the machine running `go
  // test`, and importarr's rescan worker probes real bytes with real
  // ffprobe, never a mock.
  package seed

  import (
      "fmt"
      "io"
      "os"
      "path/filepath"
  )

  // BakedClipPath is where images/Dockerfile.e2e-fixtures places the
  // generated clip in the final image.
  const BakedClipPath = "/fixtures/media/tiny.mkv"

  // Run copies BakedClipPath to <dir>/tiny.mkv, creating dir if needed.
  func Run(dir string) error {
      if err := os.MkdirAll(dir, 0o775); err != nil {
          return fmt.Errorf("seed: mkdir %s: %w", dir, err)
      }
      src, err := os.Open(BakedClipPath)
      if err != nil {
          return fmt.Errorf("seed: open baked clip: %w", err)
      }
      defer func() { _ = src.Close() }()

      dst := filepath.Join(dir, "tiny.mkv")
      out, err := os.Create(dst)
      if err != nil {
          return fmt.Errorf("seed: create %s: %w", dst, err)
      }
      defer func() { _ = out.Close() }()

      if _, err := io.Copy(out, src); err != nil {
          return fmt.Errorf("seed: copy: %w", err)
      }
      return out.Close()
  }
  ```
  `test/fixtures/seed_cmd.go`:
  ```go
  package main

  import (
      "github.com/spf13/cobra"

      "github.com/mediactl/clustarr/test/fixtures/seed"
  )

  func newSeedCommand() *cobra.Command {
      var dir string
      cmd := &cobra.Command{
          Use:   "seed",
          Short: "Copy the baked-in tiny clip into --dir as tiny.mkv",
          RunE: func(cmd *cobra.Command, args []string) error {
              return seed.Run(dir)
          },
      }
      cmd.Flags().StringVar(&dir, "dir", "/data/.e2e-fixtures", "destination directory")
      return cmd
  }
  ```
  Run: `go build -o /tmp/c11-check ./test/fixtures/` — expected to **pass now** (all three subcommands defined). Run `/tmp/c11-check --help` and confirm `tmdb-stub`, `tvdb-stub`, `seed` are listed.
  Commit: `git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "e2e: seed subcommand copying the baked-in clip onto /data" -- test/fixtures/seed test/fixtures/seed_cmd.go`

- [ ] **Step 5: `images/Dockerfile.e2e-fixtures`.**
  ```dockerfile
  # syntax=docker/dockerfile:1.7
  #
  # ghcr.io/mediactl/clustarr-e2e-fixtures -- in-cluster stub services for the
  # kind-based e2e suite (docs/superpowers/plans/2026-09-18-remaining-work.md
  # Phase H). No fixture ever calls out to the real Internet; images/
  # Dockerfile.controller and Dockerfile.media are the shape this mirrors.
  #
  #   docker build -f images/Dockerfile.e2e-fixtures -t ghcr.io/mediactl/clustarr-e2e-fixtures:dev .

  ARG GO_VERSION=1.27

  # ---------------------------------------------------------------------------
  # clipgen: one tiny lavfi-generated H.264 clip, baked in at build time so no
  # machine running `go test` (or this image, at runtime) needs ffmpeg itself.
  # A plain apt ffmpeg is enough -- this is a structural probe target for
  # pkg/mediainfo, not a codec/HDR fixture (those come with Phase E's clips).
  # ---------------------------------------------------------------------------
  FROM debian:bookworm-slim AS clipgen
  RUN apt-get update && apt-get install -y --no-install-recommends ffmpeg \
      && rm -rf /var/lib/apt/lists/*
  RUN mkdir -p /out && \
      ffmpeg -f lavfi -i "testsrc=duration=2:size=320x240:rate=15" \
             -f lavfi -i "sine=frequency=1000:duration=2" \
             -c:v libx264 -pix_fmt yuv420p -c:a aac -shortest \
             /out/tiny.mkv

  # ---------------------------------------------------------------------------
  # build: compile ./test/fixtures
  # ---------------------------------------------------------------------------
  FROM --platform=$BUILDPLATFORM golang:${GO_VERSION} AS build
  ARG TARGETOS
  ARG TARGETARCH

  WORKDIR /src
  ENV CGO_ENABLED=0 \
      GOFLAGS=-trimpath

  COPY go.mod go.sum ./
  RUN --mount=type=cache,target=/go/pkg/mod \
      go mod download

  COPY . .
  RUN --mount=type=cache,target=/go/pkg/mod \
      --mount=type=cache,target=/root/.cache/go-build \
      GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
      go build -ldflags "-s -w" -o /out/clustarr-e2e-fixtures ./test/fixtures

  # ---------------------------------------------------------------------------
  # final: distroless static, non-root (uid 65532); recorded JSON + baked clip
  # ---------------------------------------------------------------------------
  FROM gcr.io/distroless/static:nonroot
  LABEL org.opencontainers.image.source="https://github.com/mediactl/clustarr" \
        org.opencontainers.image.licenses="GPL-3.0-only" \
        org.opencontainers.image.title="clustarr-e2e-fixtures"

  COPY --from=build /out/clustarr-e2e-fixtures /clustarr-e2e-fixtures
  COPY --from=clipgen /out/tiny.mkv /fixtures/media/tiny.mkv
  COPY testdata/metadata /fixtures/testdata/metadata

  USER 65532:65532
  ENTRYPOINT ["/clustarr-e2e-fixtures"]
  ```
  Verify: `docker build -f images/Dockerfile.e2e-fixtures -t ghcr.io/mediactl/clustarr-e2e-fixtures:dev .` from the repo root succeeds; `docker run --rm ghcr.io/mediactl/clustarr-e2e-fixtures:dev --help` lists the three subcommands; `docker run --rm --entrypoint /clustarr-e2e-fixtures ghcr.io/mediactl/clustarr-e2e-fixtures:dev tmdb-stub --addr :8080 &` then from the host `curl -s http://localhost:8080/movie/27205 | head -c 80` (only works if `docker run -p 8080:8080` is added; a `-P`-free smoke check is fine via `docker exec ... wget -qO- localhost:8080/movie/27205` instead) returns the recorded Fight Club JSON.
  Commit: `git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "e2e: Dockerfile.e2e-fixtures, bakes recorded JSON and a tiny clip" -- images/Dockerfile.e2e-fixtures`

---

## Part B — the `config/e2e` overlay

- [ ] **Step 6: QualityProfile and MetadataProvider sample resources.**
  `config/e2e/quality-profile.yaml` — one minimal video profile every scenario's `Movie`/`Series` references (`ProfileMediaKind`, `Tier`, `Cutoff` fields from `api/catalog/v1alpha1/qualityprofile_types.go:145-224`):
  ```yaml
  apiVersion: catalog.clustarr.io/v1alpha1
  kind: QualityProfile
  metadata:
    name: e2e-any
    namespace: clustarr-system
  spec:
    mediaKind: video
    tiers:
    - name: HD
      qualities: ["HDTV-1080p", "WEBDL-1080p", "Bluray-1080p"]
    cutoff: HD
  ```
  `config/e2e/metadata-providers.yaml` — two `Secret`s with harmless placeholder credentials (the stubs never check them; TMDB's `golang-tmdb.Init` needs a non-empty string, `pkg/metadata/clients/tmdb/tmdb.go:66`) and two `MetadataProvider`s pointed at the in-cluster stub Services this overlay creates in Step 7:
  ```yaml
  apiVersion: v1
  kind: Secret
  metadata:
    name: tmdb-fixture-credentials
    namespace: clustarr-system
  stringData:
    apiKey: e2e-fixture-key
  ---
  apiVersion: v1
  kind: Secret
  metadata:
    name: tvdb-fixture-credentials
    namespace: clustarr-system
  stringData:
    apiKey: e2e-fixture-key
    pin: e2e-fixture-pin
  ---
  apiVersion: catalog.clustarr.io/v1alpha1
  kind: MetadataProvider
  metadata:
    name: tmdb
    namespace: clustarr-system
  spec:
    type: tmdb
    secretRef:
      name: tmdb-fixture-credentials
    baseURL: http://tmdb-stub.clustarr-system.svc:80
  ---
  apiVersion: catalog.clustarr.io/v1alpha1
  kind: MetadataProvider
  metadata:
    name: tvdb
    namespace: clustarr-system
  spec:
    type: tvdb
    secretRef:
      name: tvdb-fixture-credentials
    baseURL: http://tvdb-stub.clustarr-system.svc:80
  ```
  Commit: `git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "e2e: sample QualityProfile and stub-pointed MetadataProviders" -- config/e2e/quality-profile.yaml config/e2e/metadata-providers.yaml`

- [ ] **Step 7: fixture Deployments and Services.**
  `config/e2e/tmdb-stub.yaml`:
  ```yaml
  apiVersion: apps/v1
  kind: Deployment
  metadata:
    name: tmdb-stub
    namespace: clustarr-system
    labels: {app.kubernetes.io/component: tmdb-stub}
  spec:
    replicas: 1
    selector: {matchLabels: {app.kubernetes.io/component: tmdb-stub}}
    template:
      metadata: {labels: {app.kubernetes.io/component: tmdb-stub}}
      spec:
        containers:
        - name: tmdb-stub
          image: ghcr.io/mediactl/clustarr-e2e-fixtures:dev
          args: ["tmdb-stub", "--addr=:8080"]
          ports: [{name: http, containerPort: 8080}]
          readinessProbe: {httpGet: {path: /movie/27205, port: http}, initialDelaySeconds: 1, periodSeconds: 5}
          resources: {requests: {cpu: 10m, memory: 32Mi}, limits: {cpu: 100m, memory: 64Mi}}
  ---
  apiVersion: v1
  kind: Service
  metadata:
    name: tmdb-stub
    namespace: clustarr-system
  spec:
    selector: {app.kubernetes.io/component: tmdb-stub}
    ports: [{name: http, port: 80, targetPort: http}]
  ```
  `config/e2e/tvdb-stub.yaml` — identical shape, `name: tvdb-stub`, `args: ["tvdb-stub", "--addr=:8080"]`, `readinessProbe.httpGet.path: /series/121361/extended`.
  Commit: `git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "e2e: tmdb-stub and tvdb-stub Deployments/Services" -- config/e2e/tmdb-stub.yaml config/e2e/tvdb-stub.yaml`

- [ ] **Step 8: resource-request and replica patch, and the overlay's `kustomization.yaml`.**
  `config/e2e/resources-patch.yaml` — applied to every Clustarr-managed Deployment so ten services plus two stubs fit a small kind node within the 30-minute budget; every Deployment read in Step 7/earlier research has exactly one container at index 0:
  ```yaml
  - op: replace
    path: /spec/replicas
    value: 1
  - op: replace
    path: /spec/template/spec/containers/0/resources/requests/cpu
    value: "25m"
  - op: replace
    path: /spec/template/spec/containers/0/resources/requests/memory
    value: "64Mi"
  ```
  `config/e2e/kustomization.yaml`:
  ```yaml
  # config/e2e -- the kustomize overlay hack/e2e.sh deploys instead of
  # `make deploy`'s plain config/default: everything config/default has, plus
  # the fixture services, sample QualityProfile/MetadataProviders, and smaller
  # resource requests so the suite fits kind's node and the 30-minute
  # `make e2e` timeout (Makefile; Phase H).
  apiVersion: kustomize.config.k8s.io/v1beta1
  kind: Kustomization

  resources:
  - ../default
  - tmdb-stub.yaml
  - tvdb-stub.yaml
  - quality-profile.yaml
  - metadata-providers.yaml

  patches:
  - target:
      kind: Deployment
    path: resources-patch.yaml
  ```
  Verify: `kustomize build config/e2e | kubectl apply --server-side --dry-run=server -f -` (needs a reachable apiserver — run this after Step 20's `hack/kind.sh up`; until then, `kustomize build config/e2e >/tmp/e2e.yaml && kubectl apply --dry-run=client -f /tmp/e2e.yaml` at minimum validates the YAML shape without a live cluster).
  Commit: `git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "e2e: config/e2e overlay assembling default + fixtures + resource patch" -- config/e2e/resources-patch.yaml config/e2e/kustomization.yaml`

---

## Part C — the harness (`test/e2e/`)

- [ ] **Step 9: `TestMain` and the readiness gate — write it failing first.**
  `test/e2e/main_test.go`:
  ```go
  //go:build e2e

  package e2e

  import (
      "context"
      "fmt"
      "os"
      "testing"
      "time"

      apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
      appsv1 "k8s.io/api/apps/v1"
      corev1 "k8s.io/api/core/v1"
      metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
      "k8s.io/apimachinery/pkg/util/wait"
      "sigs.k8s.io/controller-runtime/pkg/client"
      "sigs.k8s.io/controller-runtime/pkg/client/config"

      "github.com/mediactl/clustarr/pkg/k8s"
  )

  const (
      // Namespace is where every Clustarr Deployment and every CR this suite
      // creates lives; config/default declares exactly one namespace.
      Namespace = "clustarr-system"
      // KubeContext is the kind context hack/kind.sh creates.
      KubeContext = "kind-clustarr"
      // expectedCRDCount is config/crd/kustomization.yaml's 29 entries.
      expectedCRDCount = 29
      readinessTimeout = 3 * time.Minute
  )

  var k8sClient client.Client

  func TestMain(m *testing.M) {
      ctx, cancel := context.WithTimeout(context.Background(), readinessTimeout)
      defer cancel()

      cfg, err := config.GetConfigWithContext(KubeContext)
      if err != nil {
          fmt.Fprintf(os.Stderr, "e2e: kubeconfig context %q not reachable: %v\n", KubeContext, err)
          fmt.Fprintln(os.Stderr, "e2e: run hack/e2e.sh, not `go test` directly, unless a kind cluster named clustarr is already up")
          os.Exit(1)
      }

      c, err := client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
      if err != nil {
          fmt.Fprintf(os.Stderr, "e2e: build client: %v\n", err)
          os.Exit(1)
      }
      k8sClient = c

      if _, err := os.Stat(dataDirOrEmpty()); err != nil {
          fmt.Fprintf(os.Stderr, "e2e: CLUSTARR_DATA_DIR %q not usable: %v\n", dataDirOrEmpty(), err)
          fmt.Fprintln(os.Stderr, "e2e: hack/e2e.sh must export CLUSTARR_DATA_DIR before running `make e2e`")
          os.Exit(1)
      }

      if err := waitReady(ctx); err != nil {
          fmt.Fprintf(os.Stderr, "e2e: cluster not ready: %v\n", err)
          fmt.Fprintln(os.Stderr, "e2e: a scenario never installs anything itself -- hack/e2e.sh must finish CRDs, NATS and every Deployment before `make e2e` runs")
          os.Exit(1)
      }

      os.Exit(m.Run())
  }

  // waitReady is the "TestMain refuses to run" gate Phase H specifies: 29
  // CRDs installed, NATS's StatefulSet has a ready replica, and every
  // Clustarr Deployment reports condition Available=True. It cannot be
  // exercised under envtest (docs/research/k8s.md §8: no
  // kube-controller-manager, so Deployment.status.conditions never
  // populates) -- only against a real cluster.
  func waitReady(ctx context.Context) error {
      if err := wait.PollUntilContextTimeout(ctx, 2*time.Second, readinessTimeout, true, crdsInstalled); err != nil {
          return fmt.Errorf("CRDs: %w", err)
      }
      if err := wait.PollUntilContextTimeout(ctx, 2*time.Second, readinessTimeout, true, natsReady); err != nil {
          return fmt.Errorf("NATS: %w", err)
      }
      if err := wait.PollUntilContextTimeout(ctx, 2*time.Second, readinessTimeout, true, deploymentsAvailable); err != nil {
          return fmt.Errorf("Deployments: %w", err)
      }
      return nil
  }

  func crdsInstalled(ctx context.Context) (bool, error) {
      var list apiextensionsv1.CustomResourceDefinitionList
      if err := k8sClient.List(ctx, &list, client.MatchingLabels{"app.kubernetes.io/part-of": "clustarr"}); err != nil {
          return false, nil //nolint:nilerr // keep polling; the CRD for CRDs itself always exists once any cluster is up
      }
      return len(list.Items) == expectedCRDCount, nil
  }

  func natsReady(ctx context.Context) (bool, error) {
      var sts appsv1.StatefulSet
      if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: Namespace, Name: "nats"}, &sts); err != nil {
          return false, nil //nolint:nilerr
      }
      return sts.Status.ReadyReplicas >= 1, nil
  }

  func deploymentsAvailable(ctx context.Context) (bool, error) {
      var list appsv1.DeploymentList
      if err := k8sClient.List(ctx, &list, client.InNamespace(Namespace), client.MatchingLabels{"app.kubernetes.io/part-of": "clustarr"}); err != nil {
          return false, nil //nolint:nilerr
      }
      if len(list.Items) == 0 {
          return false, nil
      }
      for _, d := range list.Items {
          if !deploymentAvailable(d) {
              return false, nil
          }
      }
      return true, nil
  }

  func deploymentAvailable(d appsv1.Deployment) bool {
      for _, c := range d.Status.Conditions {
          if c.Type == appsv1.DeploymentAvailable && c.Status == corev1.ConditionTrue {
              return true
          }
      }
      return false
  }

  var _ = metav1.Now // keep metav1 imported for scenario files in this package; removed if unused after Steps 10-13
  ```
  (Drop the trailing `var _ = metav1.Now` and the `metav1` import once `helpers_test.go` uses `metav1` for real — it is there only so this file alone still compiles in isolation while drafting; the finished package must have zero unused imports, checked by `go vet` in the next step.)
  Run: `go vet -tags e2e ./test/e2e/...` — **expected failure**: `dataDirOrEmpty` and every scenario are undefined (only `main_test.go` exists so far). This is the "red": the package doesn't compile until Step 10 adds the helpers.
  Commit only after Step 10 (same logical change); do not commit a non-compiling package.

- [ ] **Step 10: shared helpers.**
  `test/e2e/helpers_test.go`:
  ```go
  //go:build e2e

  package e2e

  import (
      "context"
      "fmt"
      "os"
      "path/filepath"
      "strings"
      "sync"
      "testing"
      "time"

      "encoding/json"

      metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
      "k8s.io/apimachinery/pkg/util/rand"
      "k8s.io/apimachinery/pkg/util/wait"
  )

  // dataDirOrEmpty returns $CLUSTARR_DATA_DIR without failing the test binary,
  // so TestMain can print a clear message instead of a panic when it's unset.
  func dataDirOrEmpty() string {
      return os.Getenv("CLUSTARR_DATA_DIR")
  }

  // dataDir is dataDirOrEmpty for use after TestMain has already verified it
  // is set and usable.
  func dataDir() string {
      d := dataDirOrEmpty()
      if d == "" {
          panic("e2e: CLUSTARR_DATA_DIR unset after TestMain's gate passed -- this is a harness bug, not a test failure")
      }
      return d
  }

  // hostPath converts a path as the cluster sees it under /data into the path
  // on the machine running `go test`, via the same hostPath mount
  // hack/kind.sh's create_cluster wires up.
  func hostPath(clusterPath string) string {
      rel := strings.TrimPrefix(clusterPath, "/data")
      return filepath.Join(dataDir(), rel)
  }

  // uniqueName returns "<prefix>-<5 lowercase alnum chars>", short enough to
  // stay under Kubernetes' 63-char name limit for every prefix this task
  // uses, and collision-safe enough for a rerunnable suite (Phase H: "every
  // scenario creates uniquely-prefixed resources").
  func uniqueName(prefix string) string {
      return fmt.Sprintf("%s-%s", prefix, rand.String(5))
  }

  // plantFile writes content at hostAbsPath, creating parent directories, and
  // registers a t.Cleanup to remove it -- even though every scenario also
  // deletes its RootFolder's whole directory, this keeps a failed setup from
  // leaking files into the next run.
  func plantFile(t *testing.T, hostAbsPath string, content []byte) {
      t.Helper()
      if err := os.MkdirAll(filepath.Dir(hostAbsPath), 0o775); err != nil {
          t.Fatalf("plantFile: mkdir %s: %v", filepath.Dir(hostAbsPath), err)
      }
      if err := os.WriteFile(hostAbsPath, content, 0o664); err != nil {
          t.Fatalf("plantFile: write %s: %v", hostAbsPath, err)
      }
      t.Cleanup(func() { _ = os.Remove(hostAbsPath) })
  }

  var (
      sampleClipOnce  sync.Once
      sampleClipBytes []byte
      sampleClipErr   error
  )

  // sampleClip returns the tiny clip hack/e2e.sh seeds to
  // <dataDir>/.e2e-fixtures/tiny.mkv via `clustarr-e2e-fixtures seed`
  // (test/fixtures/seed). Every planted "real" media file in this suite is a
  // copy of these bytes -- ffprobe reads real container/stream data, never a
  // mock, matching importarr's real pkg/mediainfo.Probe call.
  func sampleClip(t *testing.T) []byte {
      t.Helper()
      sampleClipOnce.Do(func() {
          sampleClipBytes, sampleClipErr = os.ReadFile(filepath.Join(dataDir(), ".e2e-fixtures", "tiny.mkv"))
      })
      if sampleClipErr != nil {
          t.Fatalf("sampleClip: %v (did hack/e2e.sh run `clustarr-e2e-fixtures seed`?)", sampleClipErr)
      }
      return sampleClipBytes
  }

  // waitFor polls check every 2s until it returns true, or fails the test
  // with desc after timeout. Controllers here use RequeueAfter, never sleep
  // (CLAUDE.md); this is the test-side mirror of that patience.
  func waitFor(t *testing.T, ctx context.Context, timeout time.Duration, desc string, check func(context.Context) (bool, error)) {
      t.Helper()
      err := wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, check)
      if err != nil {
          t.Fatalf("timed out waiting for %s: %v", desc, err)
      }
  }

  // managedFieldsTouch reports whether entry's FieldsV1 touches every one of
  // dottedPaths (e.g. "status.file", "status.quality"), by walking the
  // "f:"-prefixed structured-merge-diff field set
  // (sigs.k8s.io/structured-merge-diff, already a transitive dependency via
  // controller-runtime/client-go SSA support). Used by scenario 8 to prove
  // the two-writer split on MediaFile (amendment §A1.3) without guessing at
  // an un-exported comparison helper.
  func managedFieldsTouch(entry metav1.ManagedFieldsEntry, dottedPaths ...string) (bool, error) {
      if entry.FieldsV1 == nil {
          return false, nil
      }
      var raw map[string]any
      if err := json.Unmarshal(entry.FieldsV1.Raw, &raw); err != nil {
          return false, fmt.Errorf("managedFieldsTouch: decode FieldsV1: %w", err)
      }
      for _, dotted := range dottedPaths {
          cur := raw
          ok := true
          for _, seg := range strings.Split(dotted, ".") {
              next, has := cur["f:"+seg]
              if !has {
                  ok = false
                  break
              }
              nm, isMap := next.(map[string]any)
              if !isMap {
                  break // leaf field ("f:x": {}) -- present, nothing deeper to walk
              }
              cur = nm
          }
          if !ok {
              return false, nil
          }
      }
      return true, nil
  }
  ```
  Also delete the placeholder `var _ = metav1.Now` line from `main_test.go` now that `helpers_test.go` uses `metav1` for real.
  Run: `go vet -tags e2e ./test/e2e/...` — **expected failure now**: undefined `TestSeriesAndEpisodes` etc. are not referenced yet (nothing calls these helpers), so this actually **passes** once Steps 9-10 alone are in place (an unused-but-defined helper is not a vet error; only unused imports/variables are). Confirm with `go build -tags e2e ./test/e2e/...`.
  Run: `go vet -tags e2e ./test/e2e/... && echo OK` — expect `OK`.
  Commit: `git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "e2e: TestMain readiness gate and shared scenario helpers" -- test/e2e/main_test.go test/e2e/helpers_test.go`

- [ ] **Step 11: `.gitignore` and the artifacts directory.**
  `test/e2e/.gitignore`:
  ```
  artifacts/*
  !artifacts/.gitkeep
  ```
  `test/e2e/artifacts/.gitkeep` — empty file, so `hack/e2e.sh`'s failure dump (Step 23) always has somewhere to write without a first-run `mkdir` race.
  Commit: `git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "e2e: reserve test/e2e/artifacts for failure dumps" -- test/e2e/.gitignore test/e2e/artifacts/.gitkeep`

---

## Part D — Scenario 5: Series and Episodes

- [ ] **Step 12: RootFolder + Series(GoT) setup, poll for `Status.Path`.**
  `test/e2e/series_test.go`, first half:
  ```go
  //go:build e2e

  package e2e

  import (
      "context"
      "os"
      "path/filepath"
      "testing"
      "time"

      "github.com/stretchr/testify/require"
      metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
      "sigs.k8s.io/controller-runtime/pkg/client"

      catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
      commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
  )

  // TestSeriesAndEpisodes is Phase H scenario 5. Three Series exercise three
  // numbering schemes catalogarr/importarr must all handle: standard (real
  // TVDB data, a two-file season pack), daily and anime absolute (fixture-
  // owned TVDB data, amendment §A1.5's file-to-episode matching).
  func TestSeriesAndEpisodes(t *testing.T) {
      ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
      defer cancel()

      rf := &catalogv1.RootFolder{
          ObjectMeta: metav1.ObjectMeta{Name: uniqueName("e2e-series-rf"), Namespace: Namespace},
          Spec: catalogv1.RootFolderSpec{
              Path: "/data/media/tv/" + uniqueName("e2e-series"),
              Kind: catalogv1.RootFolderKindSeries,
          },
      }
      require.NoError(t, k8sClient.Create(ctx, rf))
      t.Cleanup(func() {
          _ = os.RemoveAll(hostPath(rf.Spec.Path))
          _ = k8sClient.Delete(context.Background(), rf)
      })

      standard := createSeries(ctx, t, rf.Name, 121361, catalogv1.SeriesTypeStandard)
      daily := createSeries(ctx, t, rf.Name, 900001, catalogv1.SeriesTypeDaily)
      anime := createSeries(ctx, t, rf.Name, 900002, catalogv1.SeriesTypeAnime)

      standardPath := waitForSeriesPath(ctx, t, standard)
      dailyPath := waitForSeriesPath(ctx, t, daily)
      animePath := waitForSeriesPath(ctx, t, anime)

      // Season pack: two files, one per episode, under a season-pack-styled
      // subfolder -- a real season pack unpacks into per-episode files
      // (MediaFile is "one file on disk", api/catalog/v1alpha1/mediafile_types.go:237-239).
      packDir := filepath.Join(hostPath(standardPath), "Season 01")
      plantFile(t, filepath.Join(packDir, "Game.of.Thrones.S01E01.720p.BluRay.x264-DEMAND.mkv"), sampleClip(t))
      plantFile(t, filepath.Join(packDir, "Game.of.Thrones.S01E02.720p.BluRay.x264-DEMAND.mkv"), sampleClip(t))

      plantFile(t, filepath.Join(hostPath(dailyPath), "The.Fixture.Nightly.Wire.2024.01.15.720p.WEB.x264-GROUP.mkv"), sampleClip(t))
      plantFile(t, filepath.Join(hostPath(animePath), "[GROUP] Fixture Anime - 05 [1080p][DEADBEEF].mkv"), sampleClip(t))

      scan := &catalogv1.LibraryScan{
          ObjectMeta: metav1.ObjectMeta{Name: uniqueName("e2e-series-scan"), Namespace: Namespace},
          Spec:       catalogv1.LibraryScanSpec{RootFolderRef: rf.Name, Mode: catalogv1.ScanModeFull},
      }
      require.NoError(t, k8sClient.Create(ctx, scan))
      t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), scan) })
      waitForScanCompleted(ctx, t, scan)

      assertEpisodeHasFile(ctx, t, standard.Name, 1, 1, commonv1.ReleaseTypeSeasonPack)
      assertEpisodeHasFile(ctx, t, standard.Name, 1, 2, commonv1.ReleaseTypeSeasonPack)
      assertEpisodeHasFile(ctx, t, daily.Name, 1, 1, commonv1.ReleaseTypeSingle)
      assertEpisodeHasFile(ctx, t, anime.Name, 1, 5, commonv1.ReleaseTypeSingle)
  }

  func createSeries(ctx context.Context, t *testing.T, rootFolder string, tvdbID int64, seriesType catalogv1.SeriesType) *catalogv1.Series {
      t.Helper()
      s := &catalogv1.Series{
          ObjectMeta: metav1.ObjectMeta{Name: uniqueName("e2e-series"), Namespace: Namespace},
          Spec: catalogv1.SeriesSpec{
              TvdbID:             tvdbID,
              SeriesType:         seriesType,
              QualityProfileRef:  "e2e-any",
              RootFolderRef:      rootFolder,
          },
      }
      require.NoError(t, k8sClient.Create(ctx, s))
      t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), s) })
      return s
  }

  func waitForSeriesPath(ctx context.Context, t *testing.T, s *catalogv1.Series) string {
      t.Helper()
      var path string
      waitFor(t, ctx, 3*time.Minute, "Series "+s.Name+" status.path", func(ctx context.Context) (bool, error) {
          var live catalogv1.Series
          if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(s), &live); err != nil {
              return false, nil //nolint:nilerr
          }
          path = live.Status.Path
          return path != "", nil
      })
      require.NotEmpty(t, path, "Series %s never got a status.path", s.Name)
      return path
  }
  ```
  Run: `go vet -tags e2e ./test/e2e/...` — **expected failure**: `waitForScanCompleted` and `assertEpisodeHasFile` are undefined (Step 13 adds them). This is the compile-time "red."
  Do not commit yet — Step 13 completes the file.

- [ ] **Step 13: scan-completion and episode-file assertions; make Step 12 green.**
  Append to `test/e2e/series_test.go`:
  ```go
  func waitForScanCompleted(ctx context.Context, t *testing.T, scan *catalogv1.LibraryScan) {
      t.Helper()
      waitFor(t, ctx, 3*time.Minute, "LibraryScan "+scan.Name+" Completed", func(ctx context.Context) (bool, error) {
          var live catalogv1.LibraryScan
          if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(scan), &live); err != nil {
              return false, nil //nolint:nilerr
          }
          if live.Status.Phase == catalogv1.ScanPhaseFailed {
              t.Fatalf("LibraryScan %s reached Failed; unmatched=%+v", scan.Name, live.Status.Unmatched)
          }
          return live.Status.Phase == catalogv1.ScanPhaseCompleted, nil
      })
  }

  // assertEpisodeHasFile finds the Episode for (seriesName, season, episode),
  // requires status.hasFile, and requires the MediaFile it points at has the
  // expected spec.releaseType and non-empty spec.path/status.probe -- a real
  // ffprobe result, not just a database row.
  func assertEpisodeHasFile(ctx context.Context, t *testing.T, seriesName string, season, episode int32, wantReleaseType commonv1.ReleaseType) {
      t.Helper()
      var ep catalogv1.Episode
      waitFor(t, ctx, 3*time.Minute, "Episode file for series="+seriesName, func(ctx context.Context) (bool, error) {
          var list catalogv1.EpisodeList
          if err := k8sClient.List(ctx, &list, client.InNamespace(Namespace)); err != nil {
              return false, nil //nolint:nilerr
          }
          for _, e := range list.Items {
              if e.Spec.SeriesRef == seriesName && e.Spec.SeasonNumber == season && e.Spec.EpisodeNumber == episode {
                  ep = e
                  return e.Status.HasFile, nil
              }
          }
          return false, nil
      })

      require.NotNil(t, ep.Status.FileRef, "Episode %s has no fileRef despite hasFile", ep.Name)
      var mf catalogv1.MediaFile
      require.NoError(t, k8sClient.Get(ctx, client.ObjectKey{Namespace: Namespace, Name: *ep.Status.FileRef}, &mf))
      require.Equal(t, wantReleaseType, mf.Spec.ReleaseType, "MediaFile %s releaseType", mf.Name)
      require.NotEmpty(t, mf.Spec.Path)
      require.NotNil(t, mf.Status.MediaInfo, "MediaFile %s never got probed", mf.Name)
  }
  ```
  Run: `go vet -tags e2e ./test/e2e/... && go build -tags e2e ./test/e2e/...` — expected **pass** (compiles). `go test -tags e2e -run TestSeriesAndEpisodes -list '.*' ./test/e2e/...` should list `TestSeriesAndEpisodes` — confirms it's a real, discoverable test (still can't *run* to green without a live cluster; that's Step 24's job).
  Commit: `git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "e2e: scenario 5, Series/Episode standard+daily+anime numbering" -- test/e2e/series_test.go`

---

## Part E — Scenario 7: library rescan

- [ ] **Step 14: matchable + unmatchable planted files, first scan.**
  `test/e2e/libraryscan_test.go`, first half:
  ```go
  //go:build e2e

  package e2e

  import (
      "context"
      "os"
      "path/filepath"
      "testing"
      "time"

      "github.com/stretchr/testify/require"
      metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
      "sigs.k8s.io/controller-runtime/pkg/client"

      catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
      commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
  )

  // TestLibraryRescan is Phase H scenario 7: matchable, unmatchable, sample
  // and extra files planted directly (no pre-existing Movie -- a rescan's
  // whole point is discovering a library that has none yet, amendment
  // §A1.1), then the RootFolder's schedule firing a second scan.
  func TestLibraryRescan(t *testing.T) {
      ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
      defer cancel()

      rf := &catalogv1.RootFolder{
          ObjectMeta: metav1.ObjectMeta{Name: uniqueName("e2e-scan-rf"), Namespace: Namespace},
          Spec: catalogv1.RootFolderSpec{
              Path: "/data/media/movies/" + uniqueName("e2e-scan"),
              Kind: catalogv1.RootFolderKindMovie,
          },
      }
      require.NoError(t, k8sClient.Create(ctx, rf))
      t.Cleanup(func() {
          _ = os.RemoveAll(hostPath(rf.Spec.Path))
          _ = k8sClient.Delete(context.Background(), rf)
      })
      root := hostPath(rf.Spec.Path)

      matchableDir := filepath.Join(root, "Fight Club (1999) {tmdbid-27205}")
      plantFile(t, filepath.Join(matchableDir, "Fight.Club.1999.1080p.BluRay.x264-GROUP.mkv"), sampleClip(t))
      plantFile(t, filepath.Join(matchableDir, "Sample", "Fight.Club.1999.sample.mkv"), sampleClip(t))
      plantFile(t, filepath.Join(matchableDir, "Extras", "Behind.The.Scenes.mkv"), sampleClip(t))
      plantFile(t, filepath.Join(root, "Unsorted", "IMG_0001.mkv"), sampleClip(t))

      scan := &catalogv1.LibraryScan{
          ObjectMeta: metav1.ObjectMeta{Name: uniqueName("e2e-scan"), Namespace: Namespace},
          Spec:       catalogv1.LibraryScanSpec{RootFolderRef: rf.Name, Mode: catalogv1.ScanModeFull},
      }
      require.NoError(t, k8sClient.Create(ctx, scan))
      t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), scan) })
      waitForScanCompleted(ctx, t, scan)

      var movie catalogv1.Movie
      require.NoError(t, k8sClient.Get(ctx, client.ObjectKey{Namespace: Namespace, Name: movieNameForTmdbID(ctx, t, 27205)}, &movie))
      require.True(t, movie.Status.Metadata != nil || movie.Status.Phase != "", "Movie for tmdbid 27205 was not upserted by the scan")

      var mfList catalogv1.MediaFileList
      require.NoError(t, k8sClient.List(ctx, &mfList, client.InNamespace(Namespace)))
      var mainFiles int
      for _, mf := range mfList.Items {
          if mf.Spec.MediaRef.Kind == commonv1.MediaKindMovie && mf.Spec.MediaRef.Name == movie.Name {
              mainFiles++
              require.NotContains(t, mf.Spec.Path, "Sample", "a sample file must never become a MediaFile")
              require.NotContains(t, mf.Spec.Path, "Extras", "an extras file must never become the movie's MediaFile")
          }
      }
      require.Equal(t, 1, mainFiles, "exactly one MediaFile for the matchable feature, not the sample/extra")
  }
  ```
  Run: `go vet -tags e2e ./test/e2e/...` — **expected failure**: `movieNameForTmdbID` undefined (Step 15 adds it, plus the unmatched/sample/schedule assertions).
  Do not commit yet.

- [ ] **Step 15: unmatched + sample/extra outcome assertions, and `movieNameForTmdbID`.**
  Append to `test/e2e/libraryscan_test.go`:
  ```go
  // movieNameForTmdbID finds the Movie the scan should have upserted for
  // tmdbID -- its object name isn't predictable (catalogarr generates it),
  // so list and match on spec.tmdbID instead of guessing.
  func movieNameForTmdbID(ctx context.Context, t *testing.T, tmdbID int64) string {
      t.Helper()
      var list catalogv1.MovieList
      require.NoError(t, k8sClient.List(ctx, &list, client.InNamespace(Namespace)))
      for _, m := range list.Items {
          if m.Spec.TmdbID == tmdbID {
              return m.Name
          }
      }
      t.Fatalf("no Movie with spec.tmdbID=%d was created by the scan", tmdbID)
      return ""
  }

  // TestLibraryRescanUnmatchedAndSchedule continues scenario 7: the
  // unmatchable file's fate, the sample/extra skip, and the RootFolder
  // schedule firing a second scan on its own. Split from
  // TestLibraryRescan so a failure in one half doesn't hide the other's
  // planted-file state in the same t.Cleanup stack.
  func TestLibraryRescanUnmatchedAndSchedule(t *testing.T) {
      ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
      defer cancel()

      rf := &catalogv1.RootFolder{
          ObjectMeta: metav1.ObjectMeta{Name: uniqueName("e2e-scan2-rf"), Namespace: Namespace},
          Spec: catalogv1.RootFolderSpec{
              Path:         "/data/media/movies/" + uniqueName("e2e-scan2"),
              Kind:         catalogv1.RootFolderKindMovie,
              ScanSchedule: "*/1 * * * *",
          },
      }
      require.NoError(t, k8sClient.Create(ctx, rf))
      t.Cleanup(func() {
          _ = os.RemoveAll(hostPath(rf.Spec.Path))
          _ = k8sClient.Delete(context.Background(), rf)
      })
      root := hostPath(rf.Spec.Path)

      plantFile(t, filepath.Join(root, "Unsorted", "IMG_0002.mkv"), sampleClip(t))
      first := &catalogv1.LibraryScan{
          ObjectMeta: metav1.ObjectMeta{Name: uniqueName("e2e-scan2"), Namespace: Namespace},
          Spec:       catalogv1.LibraryScanSpec{RootFolderRef: rf.Name, Mode: catalogv1.ScanModeFull},
      }
      require.NoError(t, k8sClient.Create(ctx, first))
      t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), first) })
      waitForScanCompleted(ctx, t, first)

      var completed catalogv1.LibraryScan
      require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(first), &completed))
      found := false
      for _, u := range completed.Status.Unmatched {
          if filepath.Base(u.Path) == "IMG_0002.mkv" {
              found = true
              require.NotEmpty(t, u.Reason)
          }
      }
      require.True(t, found, "an unattributable file must be recorded in status.unmatched, never a speculative item")

      // A schedule tick creates a second LibraryScan on its own -- list and
      // filter by spec.rootFolderRef rather than guess the generated name.
      plantFile(t, filepath.Join(root, "Unsorted", "IMG_0003.mkv"), sampleClip(t))
      var second catalogv1.LibraryScan
      waitFor(t, ctx, 90*time.Second, "a second, schedule-triggered LibraryScan", func(ctx context.Context) (bool, error) {
          var list catalogv1.LibraryScanList
          if err := k8sClient.List(ctx, &list, client.InNamespace(Namespace)); err != nil {
              return false, nil //nolint:nilerr
          }
          for _, s := range list.Items {
              if s.Spec.RootFolderRef == rf.Name && s.Name != first.Name {
                  second = s
                  return true, nil
              }
          }
          return false, nil
      })
      t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), &second) })
      waitForScanCompleted(ctx, t, &second)
      require.GreaterOrEqual(t, second.Status.FilesSeen, int64(1), "the schedule-triggered scan must have walked the tree again")
  }
  ```
  Run: `go vet -tags e2e ./test/e2e/... && go build -tags e2e ./test/e2e/...` — expected **pass**.
  Commit: `git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "e2e: scenario 7, library rescan matchable/unmatchable/sample/extra + schedule" -- test/e2e/libraryscan_test.go`

---

## Part F — Scenario 8: two-writer `MediaFile`

- [ ] **Step 16: matchable file + rescan, poll for both writers to have landed.**
  `test/e2e/mediafile_test.go`:
  ```go
  //go:build e2e

  package e2e

  import (
      "context"
      "os"
      "path/filepath"
      "testing"
      "time"

      "github.com/stretchr/testify/require"
      metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
      "sigs.k8s.io/controller-runtime/pkg/client"

      catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"

      "github.com/mediactl/clustarr/pkg/k8s"
  )

  // TestMediaFileTwoWriter is Phase H scenario 8 / amendment §A1.3: after a
  // probe refresh, managedFields shows importarr on status.file/status.probe
  // and catalogarr on status.quality/status.formatScore/conditions, and
  // neither clobbered the other.
  func TestMediaFileTwoWriter(t *testing.T) {
      ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
      defer cancel()

      rf := &catalogv1.RootFolder{
          ObjectMeta: metav1.ObjectMeta{Name: uniqueName("e2e-mf-rf"), Namespace: Namespace},
          Spec: catalogv1.RootFolderSpec{
              Path: "/data/media/movies/" + uniqueName("e2e-mf"),
              Kind: catalogv1.RootFolderKindMovie,
          },
      }
      require.NoError(t, k8sClient.Create(ctx, rf))
      t.Cleanup(func() {
          _ = os.RemoveAll(hostPath(rf.Spec.Path))
          _ = k8sClient.Delete(context.Background(), rf)
      })
      dir := filepath.Join(hostPath(rf.Spec.Path), "Fight Club (1999) {tmdbid-27205}")
      plantFile(t, filepath.Join(dir, "Fight.Club.1999.1080p.BluRay.x264-GROUP.mkv"), sampleClip(t))

      firstScan := &catalogv1.LibraryScan{
          ObjectMeta: metav1.ObjectMeta{Name: uniqueName("e2e-mf-scan"), Namespace: Namespace},
          Spec:       catalogv1.LibraryScanSpec{RootFolderRef: rf.Name, Mode: catalogv1.ScanModeFull},
      }
      require.NoError(t, k8sClient.Create(ctx, firstScan))
      t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), firstScan) })
      waitForScanCompleted(ctx, t, firstScan)

      movieName := movieNameForTmdbID(ctx, t, 27205)
      mf := waitForMediaFileWithBothWriters(ctx, t, movieName)

      // "After a probe refresh": force importarr to re-probe the same file
      // (ScanModeFull re-probes every file, api/catalog/.../libraryscan_types.go:31-32)
      // and catalogarr to re-score it, then re-check the split still holds.
      secondScan := &catalogv1.LibraryScan{
          ObjectMeta: metav1.ObjectMeta{Name: uniqueName("e2e-mf-scan"), Namespace: Namespace},
          Spec:       catalogv1.LibraryScanSpec{RootFolderRef: rf.Name, Mode: catalogv1.ScanModeFull},
      }
      require.NoError(t, k8sClient.Create(ctx, secondScan))
      t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), secondScan) })
      waitForScanCompleted(ctx, t, secondScan)

      refreshed := waitForMediaFileWithBothWriters(ctx, t, movieName)
      require.Equal(t, mf.Name, refreshed.Name, "the probe refresh must update the existing MediaFile, not create a second one")
  }
  ```
  Run: `go vet -tags e2e ./test/e2e/...` — expected failure: `waitForMediaFileWithBothWriters` undefined.
  Do not commit yet.

- [ ] **Step 17: `waitForMediaFileWithBothWriters` and the disjointness assertion.**
  Append to `test/e2e/mediafile_test.go`:
  ```go
  // waitForMediaFileWithBothWriters polls the Movie's MediaFile until both
  // field managers have applied, then asserts the split is exactly what
  // amendment §A1.3 specifies: importarr owns status.file/status.probe only,
  // catalogarr owns status.quality/status.formatScore/status.conditions
  // only -- proven by inspecting managedFields, not by re-deriving it from
  // which fields happen to be non-zero.
  func waitForMediaFileWithBothWriters(ctx context.Context, t *testing.T, movieName string) catalogv1.MediaFile {
      t.Helper()
      var mf catalogv1.MediaFile
      waitFor(t, ctx, 3*time.Minute, "MediaFile with both importarr and catalogarr managedFields entries", func(ctx context.Context) (bool, error) {
          var list catalogv1.MediaFileList
          if err := k8sClient.List(ctx, &list, client.InNamespace(Namespace)); err != nil {
              return false, nil //nolint:nilerr
          }
          for _, cand := range list.Items {
              if cand.Spec.MediaRef.Name != movieName {
                  continue
              }
              importarrOK, catalogarrOK := false, false
              for _, e := range cand.GetManagedFields() {
                  switch e.Manager {
                  case string(k8s.ManagerImportarr):
                      touches, err := managedFieldsTouch(e, "status.file", "status.probe")
                      if err == nil && touches {
                          importarrOK = true
                      }
                  case string(k8s.ManagerCatalogarr):
                      touches, err := managedFieldsTouch(e, "status.quality", "status.formatScore", "status.conditions")
                      if err == nil && touches {
                          catalogarrOK = true
                      }
                  }
              }
              if importarrOK && catalogarrOK {
                  mf = cand
                  return true, nil
              }
          }
          return false, nil
      })

      for _, e := range mf.GetManagedFields() {
          switch e.Manager {
          case string(k8s.ManagerImportarr):
              clobbers, _ := managedFieldsTouch(e, "status.quality")
              require.False(t, clobbers, "importarr's managedFields entry must not touch status.quality")
          case string(k8s.ManagerCatalogarr):
              clobbers, _ := managedFieldsTouch(e, "status.file")
              require.False(t, clobbers, "catalogarr's managedFields entry must not touch status.file")
          }
      }
      require.NotEmpty(t, mf.Spec.Path)
      require.NotNil(t, mf.Status.MediaInfo, "importarr's leg (status.probe) never landed")
      require.NotEmpty(t, mf.Status.Conditions, "catalogarr's leg (status.conditions) never landed")
      return mf
  }
  ```
  Run: `go vet -tags e2e ./test/e2e/... && go build -tags e2e ./test/e2e/...` — expected **pass**.
  Commit: `git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "e2e: scenario 8, two-writer MediaFile managedFields split" -- test/e2e/mediafile_test.go`

- [ ] **Step 18: whole-package compile and static-analysis check.**
  Run: `go build -tags e2e ./test/e2e/... ./test/fixtures/...` — expect success, zero output.
  Run: `gofmt -l $(git -C /home/appkins/src/mediactl/clustarr diff --name-only --cached -- test/e2e test/fixtures | grep '\.go$')` — expect empty output (nothing to reformat); `goimports`-format anything it lists before continuing.
  Run: `go test -tags e2e -list '.*' ./test/e2e/...` — expect exactly `TestSeriesAndEpisodes`, `TestLibraryRescan`, `TestLibraryRescanUnmatchedAndSchedule`, `TestMediaFileTwoWriter`.
  No commit (verification only).

---

## Part G — `hack/e2e.sh` and the first live run

- [ ] **Step 19: write `hack/e2e.sh`.**
  ```bash
  #!/usr/bin/env bash
  #
  # hack/e2e.sh -- the one command Phase H's gate asks for: bring up kind,
  # build and load every image (including the fixtures), install the CRDs,
  # apply config/e2e, wait for readiness, seed the tiny clip onto /data, run
  # `make e2e`, and on any failure dump diagnostics into test/e2e/artifacts/.
  #
  # A scenario never installs anything itself (test/e2e/main_test.go's
  # TestMain refuses to run otherwise) -- every step below is what makes that
  # true.

  set -uo pipefail  # not -e: the make e2e step's exit code must be captured, not abort the script

  REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
  cd "${REPO_ROOT}"

  CLUSTER_NAME="${KIND_CLUSTER_NAME:-clustarr}"
  NAMESPACE="clustarr-system"
  CONTEXT="kind-${CLUSTER_NAME}"
  export CLUSTARR_DATA_DIR="${CLUSTARR_DATA_DIR:-${REPO_ROOT}/.data}"
  FIXTURES_IMG="${FIXTURES_IMG:-ghcr.io/mediactl/clustarr-e2e-fixtures:dev}"
  ARTIFACTS_DIR="${REPO_ROOT}/test/e2e/artifacts"

  log()  { printf '==> %s\n' "$*" >&2; }
  die()  { printf 'error: %s\n' "$*" >&2; exit 1; }
  need() { command -v "$1" >/dev/null 2>&1 || die "missing required tool: $1"; }

  need kind; need kubectl; need docker; need go

  log "cluster up (hack/kind.sh)"
  make kind-up || die "hack/kind.sh up failed"

  log "building controller, media and fixtures images"
  make docker-build || die "make docker-build failed"
  docker build -f images/Dockerfile.e2e-fixtures -t "${FIXTURES_IMG}" . || die "fixtures image build failed"

  log "loading images into kind"
  hack/kind.sh load || die "hack/kind.sh load failed"
  kind load docker-image --name "${CLUSTER_NAME}" "${FIXTURES_IMG}" || die "loading fixtures image failed"

  log "installing CRDs"
  make install || die "make install failed"

  log "applying config/e2e"
  kustomize build config/e2e | kubectl --context "${CONTEXT}" apply --server-side -f - || die "config/e2e apply failed"

  log "waiting for every Deployment/StatefulSet in ${NAMESPACE}"
  for kind_name in deployment/catalogarr deployment/catalogarr-metadata deployment/importarr \
      deployment/importarr-worker deployment/indexarr deployment/grabarr deployment/squasharr \
      deployment/captionarr deployment/captionarr-worker deployment/ui \
      deployment/tmdb-stub deployment/tvdb-stub statefulset/nats; do
    kubectl --context "${CONTEXT}" -n "${NAMESPACE}" rollout status "${kind_name}" --timeout=180s \
      || die "${kind_name} never became ready"
  done

  log "seeding the tiny clip onto ${CLUSTARR_DATA_DIR}"
  mkdir -p "${CLUSTARR_DATA_DIR}"
  docker run --rm -v "${CLUSTARR_DATA_DIR}:/data" --entrypoint /clustarr-e2e-fixtures \
    "${FIXTURES_IMG}" seed --dir=/data/.e2e-fixtures \
    || die "seeding the fixture clip failed"

  log "running make e2e"
  make e2e
  status=$?

  if [[ "${status}" -ne 0 ]]; then
    log "make e2e failed (exit ${status}); dumping diagnostics into ${ARTIFACTS_DIR}"
    mkdir -p "${ARTIFACTS_DIR}"

    for dep in catalogarr catalogarr-metadata importarr importarr-worker indexarr grabarr \
        squasharr captionarr captionarr-worker ui tmdb-stub tvdb-stub; do
      kubectl --context "${CONTEXT}" -n "${NAMESPACE}" logs "deployment/${dep}" --all-containers --tail=-1 \
        > "${ARTIFACTS_DIR}/${dep}.log" 2>&1
    done
    kubectl --context "${CONTEXT}" -n "${NAMESPACE}" logs statefulset/nats --tail=-1 \
      > "${ARTIFACTS_DIR}/nats.log" 2>&1

    kubectl --context "${CONTEXT}" -n "${NAMESPACE}" get events --sort-by=.lastTimestamp \
      > "${ARTIFACTS_DIR}/events.txt" 2>&1

    : > "${ARTIFACTS_DIR}/resources.yaml"
    for group in catalog.clustarr.io index.clustarr.io download.clustarr.io transcode.clustarr.io subtitle.clustarr.io; do
      kinds="$(kubectl --context "${CONTEXT}" api-resources --api-group="${group}" -o name | paste -sd, -)"
      [[ -n "${kinds}" ]] && kubectl --context "${CONTEXT}" -n "${NAMESPACE}" get "${kinds}" -o yaml \
        >> "${ARTIFACTS_DIR}/resources.yaml" 2>&1
    done

    kubectl --context "${CONTEXT}" -n "${NAMESPACE}" port-forward svc/nats 18222:8222 >/dev/null 2>&1 &
    pf_pid=$!
    sleep 2
    curl -s "http://127.0.0.1:18222/jsz?streams=true&consumers=true" > "${ARTIFACTS_DIR}/nats-jsz.json" 2>&1 || true
    kill "${pf_pid}" 2>/dev/null || true

    log "diagnostics written to ${ARTIFACTS_DIR}"
  fi

  exit "${status}"
  ```
  Run: `chmod +x hack/e2e.sh`.
  Verify: `bash -n hack/e2e.sh` (syntax check only, no cluster needed) — expect no output.
  Commit: `git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "e2e: hack/e2e.sh, the one-command harness" -- hack/e2e.sh`

- [ ] **Step 20: the first live run.**
  This step cannot be simulated; it is the actual proof the harness works, and it depends on every catalogarr/importarr controller sibling Phase C tasks are landing in parallel. Run it only once those tasks report their own gates green (their envtest suites passing with `KUBEBUILDER_ASSETS` set), and expect to iterate on this step as their real behaviour surfaces gaps in the `(reconciled by controller)` assumptions above.
  Run: `hack/e2e.sh`
  Expected on first success: exit 0, `PASS` for all four `Test*` functions in the `make e2e` output, and no files written under `test/e2e/artifacts/` (a clean run leaves it holding only `.gitkeep`).
  If it fails: read the dumped logs under `test/e2e/artifacts/`, fix the affected scenario file or (if the gap is in the harness itself) this task's own code, and re-run — do not weaken an assertion to make it pass without understanding why the real behaviour differs from the documented assumption.
  No commit for this step alone; fold any fix into the step whose file it touches, as a new path-scoped commit.

---

**Verification:**
- `go build -tags e2e ./test/e2e/... ./test/fixtures/...` — must succeed with no `KUBEBUILDER_ASSETS` (this task adds no envtest suite; see the note below on why).
- `go vet -tags e2e ./test/e2e/...` and `go vet ./test/fixtures/...` — clean.
- `docker build -f images/Dockerfile.e2e-fixtures -t ghcr.io/mediactl/clustarr-e2e-fixtures:dev .` — succeeds from the repo root.
- `kustomize build config/e2e >/dev/null` — succeeds without a live cluster (pure rendering check).
- `hack/e2e.sh` — the real gate. Requires `kind`, `docker`, `kubectl`, `go`, `kustomize` on PATH and nothing else; no `KUBEBUILDER_ASSETS` needed (this task never touches envtest — see the note below) but the machine needs internet access for the one-time `docker build` layers (ffmpeg via apt, Go module downloads), never for anything that runs *inside* the cluster once built.
- **Why no envtest suite here:** every other Phase C task's gate is "an envtest suite... `KUBEBUILDER_ASSETS` set" (shared context, Global Constraints). This task is the deliberate exception: its entire subject is behaviour (`Deployment.status.conditions`, a real `StatefulSet` rollout, real files on a real hostPath) that envtest cannot produce (`docs/research/k8s.md:411`: "No kube-controller-manager/scheduler... you simulate child status yourself"). A `test/e2e` suite that only ever ran under envtest would be lying about what it proves; it must run against `hack/e2e.sh`'s real kind cluster to mean anything, per Phase H's own framing ("Unit, envtest and build-tagged integration suites do not satisfy this").

**Done when:**
- [ ] `test/e2e/main_test.go`'s `TestMain` refuses to run (clear stderr message, `os.Exit(1)`, no test executed) against a cluster missing any of: the 29 CRDs, a ready NATS `StatefulSet`, or any Clustarr `Deployment` not yet `Available`.
- [ ] `TestSeriesAndEpisodes` drives three real `Series` (standard/TVDB-recorded, daily/fixture, anime/fixture) through the real `catalogarr` Series controller and a real `LibraryScan`, and ends with `Episode.Status.HasFile=true` and a matching `MediaFile` for all four planted files, including the two-file season pack.
- [ ] `TestLibraryRescan` and `TestLibraryRescanUnmatchedAndSchedule` prove: a matchable file with an embedded `{tmdbid-N}` token is upserted into a new `Movie` plus one `MediaFile`; a sample and an extras file never become that `Movie`'s `MediaFile`; an unattributable file lands in `LibraryScan.status.unmatched` with a non-empty reason, never a speculative item; and a `RootFolder.spec.scanSchedule` tick creates a second `LibraryScan` on its own.
- [ ] `TestMediaFileTwoWriter` proves, from `managedFields` alone (not from which fields happen to be non-zero), that `importarr` and `catalogarr` each own their disjoint half of one `MediaFile`, that a probe refresh keeps updating the same object rather than creating a second one, and that neither manager's entry touches the other's fields.
- [ ] `hack/e2e.sh` runs start-to-finish from a clean checkout with only `kind`, `docker`, `kubectl`, `go` and `kustomize` installed, exits 0 on success, and on failure leaves logs for all twelve Deployments/StatefulSets, `kubectl get events`, every `catalog/index/download/transcode/subtitle.clustarr.io` resource as YAML, and the NATS `/jsz` dump under `test/e2e/artifacts/`.
- [ ] Re-running `hack/e2e.sh` against the same still-up cluster succeeds again without manual cleanup (every object and every planted file this task creates is uniquely prefixed and deleted in `t.Cleanup`, and `RootFolder.spec.path` itself is unique per run so even a failed cleanup can't collide with the next run).
- [ ] `test/fixtures/` builds one binary with three subcommands (`tmdb-stub`, `tvdb-stub`, `seed`) and `images/Dockerfile.e2e-fixtures` bakes in the recorded TMDB/TVDB JSON plus one tiny ffprobe-able clip, with no fixture reaching the real Internet at runtime.
- [ ] `make e2e`'s existing contract (`go test ./test/e2e/... -tags e2e -timeout 30m`) is unchanged; `Makefile`, `hack/kind.sh`, `config/default/`, `config/manager/` and `config/crd/` are untouched by this task.

---


---

### Task C12: Wiring, RBAC, readiness and the phase gate (serial, after C1–C11 are merged)

**Files:**
- Modify: `catalogarr/run.go`, `importarr/run.go` (the `setupControllers`/`setupWorkers` registration points, empty since Phase A)
- Modify: `config/rbac/role.yaml` (regenerated from markers), `charts/clustarr/templates/rbac.yaml` (stop the copy drifting)
- Modify: `CLAUDE.md` Status
- Modify: `docs/superpowers/plans/2026-09-18-remaining-work.md` (carried items)

**Path ownership:** controller only. No worker is running.

**Interfaces — Consumes:** every controller and worker from C1–C11.
**Interfaces — Produces:** a branch on which the services actually reconcile, `make lint` and `make test` are green, and `make e2e` passes this milestone's scenarios.

- [ ] **Step 1: Register every controller and worker**

Each task's section lists the registration lines it needs. Wire them into `catalogarr/run.go` and `importarr/run.go` behind their existing role flags, so `--role controller`, `--role worker` and `--role all` each start the right set, and `clustarr all` still stands every service up in one process.

- [ ] **Step 2: RBAC from markers (a carried defect, now unblocked)**

`config/rbac/role.yaml` was hand-written in M0 because no controller existed to carry markers. Now they do: ensure every controller has its `+kubebuilder:rbac` markers, run `make manifests`, and commit the generated role. Then make the Helm chart's copy derive from the same source rather than drifting — either template it from the generated file or add a test that fails when the two disagree, and say which you chose.

- [ ] **Step 2b: Install the bus trace hooks at the real construction site**

Task C1 put publish and receive hooks inside `pkg/events` so no caller can forget them, and
`pkg/obs` exposes the pair that satisfies them. But the one real bus-construction call site,
`pkg/k8s.ConnectBus` (called by `catalogarr`, `grabarr`, `indexarr` and `squasharr`), does
not pass them — so as things stand the propagation exists and never runs in production, and
amendment §A4's "first end-to-end trace at M1" cannot be true. C1 could not fix this itself:
`pkg/k8s` and the service `run.go` files are this task's paths, not C1's.

Give `ConnectBus` a variadic option parameter, pass `obs.BusHooks()` from each service's
`Run`, and prove it end to end: a test asserting a published envelope carries the trace when
the bus was built the way a service builds it. Decide deliberately whether `pkg/k8s` may
import `pkg/obs` or whether the hooks should be passed in by each caller, and say which you
chose and why — the dependency direction is the whole reason C1 used function fields.

- [ ] **Step 2c: Fix the shared tracing shutdown hazard in `clustarr all`**

`pkg/obs/tracing.Setup` hands every caller the same process-wide shutdown, guarded by a
`sync.Once`. In `clustarr all`, which runs seven services in one process, the first service
whose `Run` returns early therefore tears down tracing for the other six. Task C1 found this
while fixing the same pattern in a test (where one subtest's deferred shutdown killed
tracing for every later test in the binary) and correctly left the production code alone as
out of scope. Fix it here: reference-count the setup, or make the shutdown a no-op for
callers that did not perform the setup, and add a test that two callers each deferring their
shutdown leave tracing alive until the last one returns.

- [ ] **Step 3: Per-service readiness (a carried defect)**

Readiness is a JetStream ping today. Add what each service actually needs before it can serve: `catalogarr`'s informer caches synced, `importarr`'s `/data` mount present and writable. The spec's §13 readiness list is the contract.

- [ ] **Step 4: Full gate**

```bash
export KUBEBUILDER_ASSETS=$(/home/appkins/go/bin/setup-envtest use 1.37.0 -p path)
go build ./... && go vet ./...
make generate && make manifests && git status --short   # must be clean
make lint
go test -count=1 -race ./...
go mod tidy -diff
```
Every envtest suite must genuinely run — `pkg/k8s` takes ~27s and `pkg/crdcheck` ~6s when they do; single-digit milliseconds means the suite skipped.

- [ ] **Step 5: The end-to-end gate on a real cluster**

```bash
hack/e2e.sh
```
from a clean machine with only docker and kind. Scenarios 5, 7 and 8 must pass, and the artifacts directory must be empty of failures. This is the standing requirement: nothing in this phase is done until it is proven end to end on kind.

- [ ] **Step 6: Update the Status section and the carries**

Rewrite `CLAUDE.md`'s Status for Phase C — keep the whole section under about 40 lines — and move anything this phase deferred into the remaining-work plan's carried list, each with the phase that owns it.

- [ ] **Step 7: Commit**

```bash
git -c user.name=appkins -c user.email=nbatkins@gmail.com commit -m "feat: wire the catalog controllers and workers; RBAC from markers; record Phase C as done" -- catalogarr/run.go importarr/run.go config CLAUDE.md docs charts
```

**Done when:** the services reconcile with every controller and worker registered; RBAC is generated and the chart cannot drift from it; readiness reflects each service's real dependencies; the full gate is green with envtest genuinely running; `hack/e2e.sh` passes scenarios 5, 7 and 8 on kind; the Status section and the carried list are current.

---

## Self-review notes

Checked by the controller on 2026-09-18 after assembly.

- **Spec coverage.** §8.1 (want) → C6 with C5's gateway; §8.2 first half (search, decide) → C8 with C2's engine; §8.2 second half (delay, lease, grab) → C9; §8.7 (RSS, wanted cron) → C9; amendment §A1 (LibraryScan, the scan schedule, the never-guess rule, ImportExclusion) → C10; §8.4's MediaFile tail and §8.5/§8.6's watches → C7; §4's five configuration kinds → C4 (four of them; ImportExclusion is importarr's per §A1.3); §A2.2's propagation leg → C1; §14's E2E paragraph and Phase H's scenarios 5, 7 and 8 → C11. **Deliberately not in Phase C**, each with its owner: the import worker and the Download lifecycle (Phase D), transcode (E), subtitles (F), non-video kinds and the remaining metadata providers (G).
- **Cross-task interfaces.** Every `(reconciled by controller)` assumption is resolved in the table below. The two that mattered: C6's episode-listing RPC, which C5 adopted symbol for symbol; and `pkg/decision`'s shape, which C8 and C9 both consume behind a single named seam so a signature change is a one-line fix rather than a rewrite.
- **The search RPC is a seam, not a dependency.** C8 owns the interface and a fake; Phase D's indexarr satisfies the already-shipped `schema.SearchRequest`/`SearchResponse` contract.
- **Shared paths are serialized into C0**, not raced: every `api/` change, the new `pkg/events` KV names, the one new metric series, and the documentation corrections. No wave task touches `api/`, `pkg/obs/metrics`, `go.mod` or a service's `run.go`.
- **Placeholder scan.** The only `TODO` strings in the plan are quotations of existing `TODO(M1)`/`TODO(M6)` comments in `catalogarr/run.go` and `importarr/run.go` that tasks are told to update. No step is left unspecified.
- **Known gap, stated rather than hidden.** The library scanner sets `Quality` from the parsed release but leaves `FormatScore` at zero, so a scanned file reads as score 0 until Phase D scores it from the parsed release name. Tier comparisons stay correct; only within-tier tie-breaks are affected.

### Cross-task reconciliation (controller, 2026-09-18)

| Item | Finding | Resolution |
| --- | --- | --- |
| ImportExclusion ownership | My C4 brief assigned it to catalogarr. Amendment §A1.3, `pkg/k8s.ManagerImportarr`'s doc and `catalogarr/run.go`'s own comment all assign it to **importarr**. | **Ruling: the amendment wins — my brief was wrong.** C4 builds four controllers. ImportExclusion moves to C10 (importarr). Cost if wrong: one controller relocated. |
| RootFolder "built-in protection" | My brief asked for it; `RootFolderSpec` has no such field (only QualityProfile does). | Ruling: implement only what the generated type has (path/kind immutability CEL, /data/media prefix CEL). Brief mixup, correctly ignored. |
| Built-in profile count | §6.1 says 12, §9 lists 13, merged `pkg/quality.BuiltinProfiles` has 13. | Ruling: 13 — the merged code and §9 agree against §6.1's prose. |
| QualityProfile re-seeding | `builtIn:true` CEL makes the spec immutable, so §9's "re-seeded on version bump" cannot be an in-place apply. | Ruling: delete-then-recreate keyed off an annotation hash, not `k8s.Apply`. Accepted as the only shape the CEL allows. |
| Metadata provider registry | C4 and C5 run in separate processes (`catalogarr-metadata` is its own Deployment), so a shared in-memory registry is impossible. | Ruling: C4 exports a pure `BuildRegistry(ctx, client, namespace, httpClient)`; C5 calls it with its own client. The wiring task must not collapse the two call sites. |
| MetadataProvider enum coverage | 8 of 14 enum values have no Phase B client. | Ruling: `ErrProviderNotImplemented` → `Ready=Unknown`, never a crash or a false success. CARRY the missing clients to Phase G. |
| C6 gap 6: item phase rollups | No task watched Download/MediaFile from Movie/Series/Episode, so every phase past `Wanted` was unreachable. Single-writer-per-resource forces those watches into C6's own reconcilers, so no other task could have added them. | **Ruling: C6 adds both watches now.** MediaFile rollup is required by Phase C's own gate (C10's rescan creates MediaFiles; scenario 7 asserts the item reflects them). The Download watch is built and unit-tested here but only exercised end to end in Phase D, since nothing in Phase C progresses a Download past creation. Cost if wrong: an idle watch. |
| C6 ↔ C5 episode lookup | No "list a series' episodes" verb exists in `pkg/events/schema`; C6 defined one on the existing `…metadata.lookup` subject (`Kind: MediaKindEpisode`, `IDs: {tvdb, order}`, `Results [][]byte` of JSON `metadata.Episode`) and marked it an assumed contract. | Relayed to C5's writer to implement exactly, with licence to counter-propose if the real schema says otherwise. Reconciled at assembly. |
| Spec §7's `pkg/k8s` pseudocode | Lists `MapRef`, `ProbeHashChanged`, `GenerationOrFinalizer` — none exist in the merged package. | Ruling: the merged code wins; use `StatusFieldChanged[T]`, `GenerationChanged`, `Or`/`And`/`Not`. The spec's §7 sketch predates the real implementation. |
| Spec §7's `k8sbridge` | Described but never built. | Ruling: publish directly through `events.Bus.Publish` with the real helpers (`MsgIDForObject`, the work subjects, `schema.Encode`). CARRY: decide whether `k8sbridge` is still wanted, or strike it from §7, in Phase D. |
| C6 availability algorithm | My brief pointed at `metadata.md`; the real `IsAvailable(minimumAvailability, delay)` is in `quality.md` §9 (only the TTL half is in metadata.md). | Ruling: `quality.md` §9 wins; my brief was wrong. The delay applies to the `InCinemas`-known branch too. |
| C6 monitor-specials semantics | Research note only says "toggle season 0 only". | Ruling: exactly that — set the monitored flag on season-0 episodes, leave every other season untouched, with a table case proving it. |
| C6 TTL recency cutoffs | 30 days is not a ported value; nothing in the repo verifies it. | Ruling: keep 30 days as a NAMED constant documented as a chosen default, not an *arr port, so a later phase can tune it without archaeology. |
| C2 `Evaluate` signature | My brief said it takes `QualityProfileSpec`; spec §7's literal signature and the merged `pkg/quality` both say `quality.Profile`. | Ruling: the real signature wins, my brief was wrong again. `pkg/decision` therefore never imports `api/catalog/v1alpha1` — a cleaner boundary than I specified. |
| C2 vs spec §7's `Upgradable` | §7 declares `decision.Upgradable`; Task B2 already shipped that exact table as `quality.Profile.UpgradeDecision`. | Ruling: call through, never duplicate. Strike `Upgradable` from §7's `pkg/decision` list in a later spec pass. |
| C2 `Current` fields | §8.2 needs "already imported by hash/name" but §7's `Current` has no field for either. | Ruling: add `SourceTitle` (maps to the real `MediaFileSpec.ImportedFrom.ReleaseTitle`) and `SourceHash` (no MediaFile source — the caller resolves it from the originating Download, and the task says so). |
| C2 `Queued` type | §7 names the field, never defines the type. | Ruling: alias `quality.Candidate` rather than introduce a near-duplicate struct. |
| C2 `Target.FreeBytes` | §7 declares it; §8.2's checklist has no free-space check, and §8.4 puts that check in the import path. | **Ruling: drop the field.** Phase B's reviews repeatedly flagged dead exported surface, and an unread field is exactly that. CARRY: if Phase D wants a pre-grab disk check, add it to `decision` with its own rejection reason rather than reviving a silent field. |
| C2 rejection permanence | Verified against the vendored Radarr/Sonarr source, not assumed: every reason this package emits is Permanent. | Accepted — this is what a Search CR's `Override` requirement hangs on, so it had to be checked rather than guessed. |
| C2 `Rank` comparator | `docs/research/naming.md` §A6 flags its own summary "unverified order" and omits two real steps (episode count, indexer flags). | Ruling: the writer's re-verification against `DownloadDecisionComparer.cs`/`ScoreFlags` wins over the note. Fix the note's §A6 in a later docs pass. |
| C2 fixture loading | `//go:embed` cannot contain `..`, so a repo-root `testdata/` fixture cannot be embedded from a package. | Accepted: `os.ReadFile(filepath.Join("..","..","testdata",…))`, matching `pkg/quality/trash_corpus_test.go`'s existing convention. |
| C11: my "§A5" citation | §A5 is "Risks this adds"; the ownership spec is §A1.3. | Ruling: my error. Fixed in `remaining-work.md` (1 reference). |
| C11: scenario 5's grab leg | Phase H's scenario 5 says "a season pack is grabbed once", but nothing in Phase C can grab (grabarr is Phase D). | **Ruling: split the scenario.** Phase C covers Series/Episode coverage via the rescan path importarr actually owns; the grab leg moves to Phase D. Amend Phase H's scenario 5 text accordingly. |
| C11: TVDB fixture coverage | `testdata/metadata/tvdb/` records one standard-numbered series; scenario 5 needs daily and anime cases. | Ruling: serve the real recorded JSON verbatim for the real series, plus two fixture-owned synthetic series in the wire shape verified from the TVDB client's response structs. Synthetic data the fixture owns is fine; silently reshaping Phase B's recorded fixtures would not be. |
| C8: `Search` has `ac:generate=false` | controller-tools v0.22.0 panics on the embedded `commonv1.ReleaseInfo`, so no apply configuration is generated and `pkg/k8s.PatchStatus` has nothing to take. | **Ruling: hand-write the apply configuration**, mirroring controller-gen's output for a comparable kind, and keep the status write going through `PatchStatus` — the invariant is SSA through `pkg/k8s` with a named manager, which this preserves. Require a test that FAILS if controller-gen ever starts generating one, so we never carry two. CARRY to Phase D: restructure the `ReleaseInfo` embedding so the generator works, then delete the hand-written file. |
| C8: `SearchRequest` has no `Absolute` | Spec §8.2 says "tvdb plus season/episode/absolute"; the already-shipped `pkg/events/schema` type has `Season`/`Episode` only. | Ruling: generated type wins — anime folds the absolute number into `Episode` and omits `Season`. |
| C8: the RPC contract already exists | Phase A's `pkg/events/schema` already implements `SearchTask`/`SearchRequest`/`SearchResponse`/`GrabTask`/`WantedScan`. | Ruling: reuse them; do not re-design. This is the exact contract Phase D's indexarr must satisfy. |
| C9: who writes `Phase` | §8.2 says the grab path sets `Phase=Delayed`, but `ManagerCatalogarr` owns phase on every catalog kind and one writer per resource is the binding invariant. | **Ruling: the invariant wins.** The grab worker patches only `activeDownloadRef`, `pendingGrab`, `lastSearchedAt` and `searchAttempts`; C6's controller derives `Phase`. The spec's prose is describing the outcome, not the writer. |
| C9: wantedcron's shape | §6.1's prose describes per-item backoff; §5's shipped routing sends `wantedscan.>` to the search consumer, and `run.go` already calls wantedcron a `Runnable`. | Ruling: follow the merged routing — the cron fires `WantedScan` per eligible namespace; the `Backoff`/`NextEligible` pure functions live with it for the search worker to import. |
| C9: `SeedCriteria.Ratio` | Spec pseudocode says `*float64`; the generated type uses `*resource.Quantity`. | Ruling: generated type wins (the no-float invariant). |
| C9: invented values | `firstSeen` Msg-Id as Unix seconds; `chooseSource` (magnet direct; direct URL only when the Indexer has no secret; else indexerDownload); `Backoff` base case. | Accepted as flagged-and-documented inventions. Each must carry a comment saying it is a chosen default, not a ported value. |
| C5: `pkg/obs/metrics` is a shared path | New domain metric series must be declared in `pkg/obs/metrics/domain.go` because `newCounterVec` is unexported, but that file belongs to no wave task — a collision waiting to happen with six parallel agents. | **Ruling: C0 declares every Phase C metric series up front**, exactly as it pre-adds dependencies. At assembly I scan all eleven sections for the series they need and add them to C0's step list; no wave task touches `pkg/obs/metrics`. Cost if wrong: one unused collector. |
| C5: metadata cache key | The spec's KV table shows `<provider>.<kind>.<id>`, but `Registry.Lookup` is first-provider-wins, so the provider is unknown before the fetch. | Ruling: key on `<kind>:<sorted ids>`. The spec's table assumed a provider-pinned lookup the real registry does not do. |
| C5: tmdb as a Series provider | `docs/research/metadata.md` lists it; the merged `pkg/metadata/clients/tmdb` implements `MovieProvider` only. | Ruling: merged code wins. |
| C5: `ImageType` breadth | `pkg/metadata.ImageType` has 9 values; the generated CRD enum allows `poster;fanart;logo`. | Ruling: generated type wins — drop the other six rather than mislabel them. CARRY to Phase G: widen the CRD enum if the extra image types are actually wanted. |
| C5: which kinds the work queue serves | Scoped to Movie and Series, matching `catalogarr/run.go`'s own `TODO(M6)` for the other seven kinds; the RPC side serves every kind the registry knows, since it touches no CR. | Accepted — the split is the existing code's, not an invention. |
| C5 ↔ C6 episode lookup | C5 adopted C6's proposed shape as-is after reading the real `Registry.Lookup` (whose switch has no Episode case), routing to `SeriesProvider.Episodes` directly. | **Reconciled: the two sections now agree symbol for symbol.** Non-blocking wrinkle recorded: `MetadataResponse.Results`' doc comment says "hits of a search request", which a listing slightly repurposes — fix the comment in C5's step. |
| **C7: the two-writer MediaFile split as documented does not exist** | CLAUDE.md's invariant, amendment §A1.3 and `pkg/k8s/fieldmanager.go`'s doc comments all describe `importarr` owning `status.file`/`status.probe` and `catalogarr` owning `status.quality`/`status.formatScore`. **Controller verified directly:** `MediaFileStatus` is flat (`ObservedGeneration`, `Conditions`, `ProbeHash`, `ProbedAt`, `MediaInfo`, `Sidecars`, `Transcode`) — no `file`, no `probe` — and all six "decided" fields live in `MediaFileSpec`, frozen at import, exactly as spec §8.4 says. The documented split described an API we never shipped; it survived three phases because nothing reconciled yet, so no code ever tried to write those fields. | **Ruling: the generated type wins, and §8.4 agrees with it.** Real ownership: importarr creates the MediaFile and owns `MediaFileSpec` (observed facts + the frozen decided fields); catalogarr is sole writer of all `MediaFileStatus`, and additionally takes over `sizeBytes`/`modTime`/`original` after it incorporates a transcode swap (those fields' own doc comments and §8.5). The two-writer discipline and its envtest REMAIN — the split is spec-vs-status plus that three-field handover, not status-vs-status. Actions: correct CLAUDE.md's invariant; add an erratum to amendment §A1.3 rather than silently rewriting a design of record; fix `pkg/k8s/fieldmanager.go`'s doc comments in C0; C10's brief corrected mid-flight. |
| C7: rollup scope | The status rollup to the owning item was unscoped by kind in my brief. | Ruling: Movie and Episode only — CLAUDE.md puts non-video inventory in M6. Stated, not silently narrowed. |
| C7: "rescans sidecars" (§8.6) | `pkg/subtitles`' own doc says sidecar naming belongs to captionarr (Phase F). | Ruling: mirror `SubtitleRequest.status.items` into `MediaFile.status.sidecars` rather than scanning the filesystem from catalogarr. |
| C6: duplicated rollup helpers | `FileState`/`DownloadOverlay` duplicated between `movie/` and `episode/` because the task owned no shared directory. | **Ruling: give it one.** C6 also owns `catalogarr/controller/rollup/`; both pure functions live there with their tests. Verbatim duplication is a named review defect here and was raised twice in Phase B. |
| C6: Series watch shape | `SeriesStatus` has no `HasFile`/`ActiveDownloadRef`, so a direct MediaFile/Download watch has nothing to write. | Accepted: `Owns(&Episode{})` guarded by `StatusFieldChanged` on `HasFile`, reusing the existing self-loop-avoidance shape. |
| C6: `CutoffMet` | Uses the merged `quality.Profile.CutoffMet` rather than a hand-rolled comparison. | Accepted — reuse over reimplementation. |
| **C10: force-ownership hazard created by my own MediaFile ruling** | `k8s.Apply` always forces ownership, so a rescan re-applying `MediaFileSpec` would silently reclaim `sizeBytes`/`modTime`/`original` from catalogarr after the post-transcode handover. | **Accepted, and a genuinely good catch.** The worker checks `Spec.Original` first and skips such files, with a dedicated test. CARRY to Phase E: define what a rescan should do when a transcoded file's bytes legitimately changed on disk — skipping is right for Phase C but is not the final answer. |
| C10: new `pkg/events` symbols | The ImportExclusion lookup needs `ExclusionKey`/`ExclusionEntry`/`BucketImportExclusions`, but `pkg/events` is owned by Task C1 (the trace hooks) — two wave tasks would collide on it. | **Ruling: C0 declares them** (it is serial and runs first), mirroring the existing `LeaseKey`/`PendingKey` pattern. C1 adds the hooks, C10 consumes the names. Same reasoning as the metrics ruling. |
| C10: `MovieAddMethod` has no "found by scan" value | The writer used `manual` as the closest fit. | **Ruling: add a `scan` value to the enum in C0** rather than ship a small lie. Labelling a scanner-discovered item "manual" is exactly the kind of inaccuracy that costs somebody an hour a year from now. One enum value plus a regenerate. |
| C7 vs C0: who changes `api/` | I told C7 to add `commonv1.AudioStream.ChannelLayout`, but the header gives `api/common/v1alpha1` to C0. | **Ruling: C0 makes every `api/` change** (`ChannelLayout`, the `scan` enum value) and regenerates once; C7 consumes. One serial regeneration beats two parallel agents racing on generated files. Amend C7 accordingly. |
| C10: no per-directory scan chunking | §A1.6's wording implies chunking a scan into many `ScanTask` messages, but `LibraryScanStatus.Unmatched` has no merge key and `LibraryScan` has no multi-writer exception. | Ruling: one `ScanTask` per scan with heartbeats and KV progress checkpoints. The writer's reasoning is sound and the alternative would need a second writer on a status list. |
| C10: id-less file matching | Matched only against EXISTING catalog items by exact title and year — never a blind gateway search that could fabricate an item. | **Accepted, and this is the never-guess rule applied exactly right.** |
| C10: scanner leaves `FormatScore` zero | Scoring a scanned file needs `pkg/quality/catalogue` integration the task bounded out. | Ruling: accept for Phase C — `Quality` IS set from the parsed release, so tier comparisons stay correct and only within-tier tie-breaks are affected. CARRY to Phase D, prominently: until the scanner scores what it can from the parsed release name, every scanned file reads as score 0. |
| C10: `CLUSTARR_WORK_IMPORTARR` | Implemented in `pkg/events` (Phase A) but absent from spec §5's stream table. | Ruling: the implemented topology wins. CARRY: bring §5's table up to date in a docs pass. |
| C10: scan scoped to `RootFolderKind: movie` | Non-video kinds are M6 per §16. | Accepted, stated rather than silently narrowed. |
