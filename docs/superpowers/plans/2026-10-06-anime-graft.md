# Audio Donors and Grafting Implementation Plan (anime dual-audio phase 4)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** When an anime file lacks a wanted dub, clustarr finds a donor
release with it, keeps only the donor's audio, aligns that audio to the
library file with `pkg/audioalign`, and muxes the dub into the file in
place. The file stays an original, not a transcode, and remains upgradable.

**Architecture:**
- **Search and grab:** the wanted sweep queues a donor search
  (`SearchTask.Purpose: audioDonor`) for a monitored item whose
  `status.audio.graft` is `searching` or `failed`, on its own backoff.
  `pkg/decision` judges donors on languages, not quality.
- **Import:** the grab writes `Download.spec.purpose: audioDonor`. importarr
  places the donor under `<RootFolder>/.clustarr/donors/<item-uid>/` and
  applies an `AudioGraft`.
- **The graft:** squasharr runs each AudioGraft as a short `batch/v1` Job.
  The Job is rendered from the pool template and runs `squasharr-worker
  --graft-task`. The worker reduces the donor to an `.mka`, decodes both
  anchors through ffgo, aligns them, muxes (every target stream copied, the
  dub added as AAC stereo) and verifies the muxed track against the target's
  anchor. It then swaps the result in, and returns its result in the pod's
  termination message.
- **Incorporation:** catalogarr re-probes the swapped file without marking
  it a transcode. The item reconcilers derive `status.audio.graft` from the
  AudioGraft and donor Downloads.

**Tech Stack:** Go, controller-runtime, ffgo (FFmpeg 9 libraries,
squasharr-worker only), `pkg/audioalign`.

**Spec:** `docs/superpowers/specs/2026-10-06-anime-dual-audio-design.md` §6, §7.2, §8, §9.

**Execution:** Native, approved 2026-10-06 ("Approve all recommended - for
all phases - implement with Native").

## Global Constraints

- `cmd/clustarr` never links ffgo or purego (`TestClustarrNeverLinksADynamicLoader`).
  ffgo stays in `pkg/transcode/engine` and in what `cmd/squasharr-worker`
  imports.
- No new module dependency. No `go get` or `go mod tidy`.
- Every status write goes through `k8s.PatchStatus` with a complete
  declaration of what the manager owns. Every status list is capped.
- No floats in `api/`: rates in micro-units, percentages as whole numbers,
  times in milliseconds.
- A graft is not a transcode. `spec.original` is never set false for one,
  no `CLUSTARR_PROFILE` tag is written, and `CLUSTARR_GRAFT=<donor sha256[:12]>`
  is written instead.
- Never a graft and a transcode on one MediaFile at once.
- Donor searches never burst: at most 20 live donor searches per namespace
  per sweep, then index-only searches.

## Design rulings (made before writing; costs if wrong)

1. **A graft runs as one Job per AudioGraft, not through the pool's NATS
   queue.** Pools are keyed by TranscodeProfile, and a graft has none. The
   Job's pod is `pool.Template` for class cpu, so it gets the pool's
   securityContext, data volume, UMASK and image. Its result travels in the
   termination message, so it needs no new streams or consumers.
   Concurrency is capped by `--graft-concurrency` (default 2).
   [cost if wrong: about 10 s of pod start per graft]
2. **importarr places the whole donor file, and the worker reduces it to an
   `.mka`.** importarr has no ffgo, and the binary-split decision rules out
   new ffmpeg execs. [cost if wrong: the donor's video sits on disk until
   its first graft]
3. **Verify the muxed output, not only the transform.** The grafted track is
   decoded back out of the `.part` file and aligned against the target's
   anchor at rate 1 and offset 0 (median ≤ 40 ms, ≥ 90% of windows within
   80 ms). This catches timestamp and timebase mistakes that a check of the
   transform alone cannot. [cost if wrong: about 25 s more CPU per graft]
4. **The rate is applied in Go, not by swr.** The donor track is resampled
   at its own speed to 48 kHz stereo. Each output sample is then read from
   the donor time `Result.DonorAt` maps it to, by linear interpolation from
   a sliding buffer. This needs segments in increasing donor order;
   `Accept` now refuses any others. [cost if wrong: slight
   high-frequency smearing on a dub track]
5. **Donor search runs on the wanted sweep with its own backoff**
   (`status.donorSearchAttempts`, under the grab manager), not from the
   item reconciler. An item's video search and donor search are judged
   separately, so a CutoffUnmet 480p file still gets its donor search.
   [cost if wrong: up to one sweep (12 h) of latency]
6. **A donor title must name the missing languages.** It qualifies if it
   names them, or if, under an anime score set, it carries a dual or multi
   marker: TRaSH's Dual Audio pattern, `MULTi`, or a bare `DL` that is not
   part of `WEB-DL`. BoB's `...AAC.DL-BoB` is such a title. The donor's
   probe at import is the real test. [cost if wrong: a download that is
   rejected at import and blocklisted]
7. **Per-item donor rejection** lives in `AudioGraft.status.rejectedReleases`
   (≤ 16), which the donor decision reads. The namespace blocklist is
   untouched. A donor rejected at import, because its files lack the
   language, is a release fault, blocklisted and re-searched as a donor.
   [cost if wrong: one more grab]
8. **`spec.itemRef` is a `commonv1.MediaRef`.** The spec's
   `commonv1.ItemRef` does not exist. [cost if wrong: none]
9. **A grafted original MediaFile is taken over like a transcoded one, except
   `spec.original`.** catalogarr records `status.graftTag` and from then on
   sends `spec.path`, `sizeBytes` and `modTime` (spec §8). [cost if wrong: an
   importarr rescan re-applies a stale size]
10. **Live run:** monitor only Monster S01E02 and run the flow end to end on
    that episode. [cost if wrong: one video grab and one donor grab]

## Review Focus

1. **A donor whose audio starts later than its container** (a positive
   audio start_time), or a target whose anchor does. Leading silence must
   keep sample 0 at container time 0 for both, or every offset is wrong by
   the start time.
2. **A donor shorter than the target.** The mux pads with silence to the
   target's duration and never stops early. A longer donor's surplus is
   dropped, and the donor demux drains without blocking.
3. **A transcode and a graft racing for one file.** The TranscodeProfile
   skips a MediaFile with a non-terminal AudioGraft, and the graft waits
   for open TranscodeJobs. A graft whose target changed under it (a new
   probe hash) refuses to swap.
4. **SSA ownership.** Graft state is written only by the item reconciler.
   AudioGraft status is written only by squasharr. Donor attempts are
   written by the grab manager's complete set. catalogarr's spec takeover
   of a grafted original must not release `spec.original`.
5. **Grab guards.** A donor in flight must not block a video upgrade, nor a
   video Download a donor. `activeDownloadRef` and Phase must ignore donor
   Downloads.

---

### Task 1: API

**Files:**
- Create `api/transcode/v1alpha1/audiograft_types.go`.
- Modify `api/download/v1alpha1/download_types.go` (`spec.purpose`).
- Modify `api/catalog/v1alpha1/{episode,movie}_types.go` (`status.donorSearchAttempts`).
- Modify `api/catalog/v1alpha1/mediafile_types.go` (`status.graftTag`, `status.graftedAt`).
- Modify `pkg/events/schema/catalog.go` (`SearchTask.Purpose`).
- Run `make generate manifests`. Add a CEL test in `pkg/crdcheck`.

**Produces:**

```go
// transcode.clustarr.io/v1alpha1
type AudioGraftSpec struct {
	ItemRef   commonv1.MediaRef `json:"itemRef"`             // Episode or Movie
	DonorPath string            `json:"donorPath"`           // as importarr placed it
	Languages []string          `json:"languages"`           // MaxItems=4, BCP-47
	Anchor    string            `json:"anchor"`              // BCP-47
	Default   string            `json:"default,omitempty"`   // the profile's default language
	Release   string            `json:"release"`             // donor release title
}
type AudioGraftPhase string // Waiting, Pending, Running, Succeeded, Failed
type AudioGraftSegment struct{ DonorStartMillis, TargetStartMillis, LengthMillis int64 }
type AudioGraftStatus struct {
	ObservedGeneration int64; Phase AudioGraftPhase; Reason, Message string
	MediaFileRef string; TargetProbeHash string; JobName string
	RateName string; RateMicros int64; RateMarginMilli int32; CoveragePercent int32
	Segments []AudioGraftSegment // MaxItems=16
	ResidualMillis int32; Within80Percent int32; GraftTag string
	StartedAt, CompletedAt *metav1.Time
	RejectedReleases []string // MaxItems=16
	Conditions []metav1.Condition // MaxItems=8
}
// download: DownloadPurpose "", "audioDonor"; spec.purpose immutable (has()-guarded CEL)
// schema: SearchTask.Purpose string; schema.SearchPurposeAudioDonor = "audioDonor"
// catalog: EpisodeStatus/MovieStatus.DonorSearchAttempts commonv1.Attempts
// catalog: MediaFileStatus.GraftTag string; GraftedAt *metav1.Time
```

- [ ] A crdcheck test: `spec.purpose` is immutable, a Download without
  `purpose` still takes status writes, and AudioGraft's lists reject
  overflow. Run it and watch it fail (no field yet).
- [ ] Add the types. Run `make generate manifests` and the RBAC regeneration
  the markers need. Run `go test ./pkg/crdcheck/ ./api/...` with
  `KUBEBUILDER_ASSETS`. Expected: PASS. Commit.

### Task 2: `pkg/audioalign` mapping

**Produces:** `func (r Result) DonorAt(t time.Duration) (time.Duration, bool)`
(target time to donor time on the donor's own clock; false outside every
segment). `Transform` is rebuilt on it. `Accept` refuses segments whose donor
start decreases as the target start increases (`ErrSegmentOrder`).

- [ ] Tests: `DonorAt` on case 3's result; across a gap it reads false. A
  hand-built Result whose segments go backwards fails Accept with
  ErrSegmentOrder. Transform still round-trips through Verify (the existing
  test). Watch the new tests fail.
- [ ] Implement. Run `go test ./pkg/audioalign/`. Expected: PASS. Commit.

### Task 3: engine, decode and graft mux (`pkg/transcode/engine`)

**Produces:**

```go
func DecodePCM(ctx context.Context, path string, audioIndex, rate int) ([]float32, error) // mono float, sample 0 = the container's start
func ExtractAudio(ctx context.Context, input, output string, audioIndexes []int) error   // stream copy into Matroska audio
type GraftAudio struct {
	Donor string; Stream int                       // the donor's audio index (type-relative)
	Map func(time.Duration) (time.Duration, bool)  // target time -> donor time
	Language, Title string; Default bool           // Default: the new track is default, the copied audio is not
}
// Options.Graft *GraftAudio: Run adds the grafted track after the copied audio.
func CopyPlan(d ProbeLike) standard.Result // every stream copied: the base plan of a graft
```

- [ ] Tests over clips generated in the test with ffgo's encoders, as
  engine_test does:
  - DecodePCM returns the clip's duration at 8 kHz, and leading silence for
    an audio stream that starts 0.5 s late.
  - ExtractAudio keeps exactly the named tracks, with their languages.
  - A graft with an identity map adds one AAC stereo track whose PCM aligns
    to the donor's at offset 0. Every target stream is copied, the
    container tags are kept, `CLUSTARR_GRAFT` is added, and the dispositions
    follow `Default`.
  - A map with an offset of +2 s and a gap leaves silence where the map
    reads false.
  - A donor shorter than the target pads to the target's duration.
- [ ] Watch them fail. Implement. Run `go test ./pkg/transcode/engine/`.
  Expected: PASS. Commit.

### Task 4: the graft worker (`app/squash/worker/graft.go`, `cmd/squasharr-worker`)

**Produces:** `graft.Task` JSON (`target`, `targetProbeHash`, `donor`,
`languages`, `anchor`, `default`, `recycleBin`), `graft.Result` JSON (phase,
reason, message, rate, margin, coverage, segments, residual, within80,
graftTag, outputSizeBytes). The flow is `RunGraft(ctx, Task, dataRoot)
Result`:
1. Reduce the donor to `.mka` and remove the source.
2. Pick the tracks. The anchor is the target's track in the anchor
   language, or its only untagged track; the donor's must be tagged.
3. Decode both anchors at 8 kHz, then `Align` and `Accept`.
4. Mux to `<stem>.part.<ext>`, then verify the grafted track decoded back
   out.
5. Re-check the target's probe hash, then `RecycleLink` and `MoveAtomic`.

`squasharr-worker --graft-task <json>` writes the Result to
`/dev/termination-log` (`--termination-log`) and exits 0, or 1 on failure.

- [ ] Tests, on generated clips:
  - A target with Japanese audio and a donor with Japanese and English,
    delayed 1.5 s, swaps in a file with three audio tracks. Its new English
    track aligns at offset 0, and `CLUSTARR_GRAFT` is set.
  - Unrelated audio fails with reason `AlignmentRejected` and leaves the
    target byte-identical.
  - A donor lacking English fails with `DonorLacksLanguage`.
  - A target changed after the alignment fails with `TargetChanged`.
  - The main runs in `--graft-task` mode and writes the termination file.
- [ ] Watch them fail. Implement. Run
  `go test ./app/squash/worker/ ./cmd/squasharr-worker/`. Expected: PASS.
  Commit.

### Task 5: squasharr AudioGraft controller (`app/squash/controller/audiograft`)

**Produces:** `Reconciler{Client, APIReader, Pool pool.Config, Concurrency int, Clock}`, registered in `app/squash/run.go` behind the controller role; `--graft-concurrency`.

Reconcile:
1. Read the item, its `status.fileRef` and the MediaFile.
   - No probe: `Waiting`.
   - The wanted languages already present: `Succeeded`, reason `Present`.
2. If a non-terminal TranscodeJob is open for the MediaFile: `Waiting`.
3. A `Failed` for the same generation and target probe hash stays failed.
4. When the slots are full: `Waiting`, retried a minute later.
5. Otherwise create the Job, owned by the AudioGraft, and go `Pending`, then
   `Running`.
6. When the Job finishes, read its pod's termination message into status.
   - A failure appends `spec.release` to `rejectedReleases`.
   - Delete the finished Job.

The TranscodeProfile controller skips a MediaFile whose AudioGraft is
Pending or Running (a field index on `status.mediaFileRef`).

- [ ] Envtest:
  - A probed MediaFile missing English makes a Job whose args carry the task.
  - A finished pod's termination message lands in status.
  - A failure records the release.
  - An open TranscodeJob holds the graft at Waiting.
  - The TranscodeProfile skips a grafting file.
  - A second AudioGraft past the concurrency limit waits.
- [ ] Watch them fail. Implement. Run
  `go test ./app/squash/controller/audiograft/ ./app/squash/controller/transcodeprofile/`
  with assets. Expected: PASS. Commit.

### Task 6: catalogarr incorporation and graft state

- **MediaFile reconciler:**
  - It watches AudioGraft, mapping `status.mediaFileRef` to the MediaFile.
  - A Succeeded graft whose `completedAt` is after `probedAt` is a graft
    swap. It re-probes, sets `graftTag` and `graftedAt`, and from then on
    sends `spec.path`, `sizeBytes` and `modTime`.
  - It sends `spec.original` only when the file is already transcoded.
  - It never runs `staleTranscodeState` for a graft swap.
- **`rollup.AudioStateFor(p, tag, mf, g GraftObservation)`:**
  - `searching` when languages are missing and nothing is under way.
  - `grabbed` while a donor Download is open.
  - `pending` for a Pending or Running AudioGraft, `aligned` once it carries
    segments.
  - `failed` for a Failed one still missing languages.
  - `done` when nothing is missing after a graft.
  - Episode and Movie watch AudioGraft through `spec.itemRef`.
- **`rollup.ActiveDownload`** skips `spec.purpose: audioDonor`.

- [ ] Tests:
  - A rollup table for every graft state.
  - The MediaFile envtest: a graft swap keeps `original` true, sets
    `graftTag`, and takes over `sizeBytes`. A later transcode swap still
    sets `original` false. `managedFields` shows catalogarr never claims
    `spec.original` on a grafted original.
  - The Episode envtest: a donor Download leaves `Phase` and
    `activeDownloadRef` alone and reads `grabbed`.
- [ ] Watch them fail. Implement. Run the catalog controller packages.
  Expected: PASS. Commit.

### Task 7: donor search, decision and grab

- **`decision.Target.Donor *Donor{Languages []string; Anchor string; Rejected []string; Source string}`:**
  - It skips the quality, cutoff, upgrade, transcoded and video-queue
    checks.
  - `donorRejection` applies ruling 6.
  - The size table still applies.
  - It rejects releases on `Rejected`, and any release while another donor
    is queued.
  - `RankKey.Donor` ranks by lineage (same source, then a matching edition)
    and then by smaller size.
- **The search worker:**
  - `SearchTask.Purpose: audioDonor` builds the Donor from `status.audio`
    and the AudioGraft's `rejectedReleases`.
  - It records `donorSearchAttempts`.
  - It passes the purpose through the Sink, `Approved`, the pending grab and
    `createDownload`, which writes `spec.purpose`.
  - Donor leases are keyed `<mediaKey>/audioDonor`.
  - `guardExistingDownloads` and `downloads.Covers` compare purposes.
- **wantedcron:**
  - `Candidate.DonorDue` is due when `graft` is `searching` or `failed` and
    the donor backoff has elapsed.
  - The sweep emits donor searches, live for at most 20 per namespace per
    sweep, with Msg-Id kind `donor-search`.
- **The redownload handler** keeps the purpose.

- [ ] Tests:
  - Decision table:
    - BoB's title is approved as a donor for `[en]` with anchor `ja`.
    - A Japanese-only 1080p WEB title is rejected.
    - A `WEB-DL` title alone is rejected.
    - A title on `Rejected` is rejected.
    - A donor of a quality the profile lacks still passes.
    - Ranking: a same-source, smaller donor wins.
  - Grab envtest: a donor and a video Download coexist for one Episode, and
    a second donor is refused.
  - Sweep tests: a donor candidate yields a `donor-search`, and the cap
    holds.
  - The redownload test keeps the purpose.
- [ ] Watch them fail. Implement. Run the decision, search, grab, wantedcron
  and redownload packages. Expected: PASS. Commit.

### Task 8: importarr donor import

- **Fork:** after the target is resolved, `spec.purpose: audioDonor` takes
  `importDonor`.
- **Pick the file:** the largest video or `.mka` file in the content.
- **Probe:** `mediainfo.Probe`. It requires every language of the item's
  `status.audio.missing`, plus the anchor, among its tagged audio tracks.
  Otherwise the import is blocked with `ImportMessageEveryFileRejected`,
  which is a release fault, then blocklisted and re-searched as a donor.
- **Place:** hardlink or move it to
  `<RootFolder>/.clustarr/donors/<item-uid>/<item-name><ext>`.
- **Apply the AudioGraft** (name `k8s.ChildName(item, "audiograft")`,
  owner the item), under `k8s.ManagerImportarrWorker`. Then call
  `finishImported` with a `DestPath` and no MediaFile.
- **librarydelete** with `files` also removes the item's donor directory.
- **The rescan** never attributes anything under `.clustarr/`.

- [ ] Tests, built with real ffmpeg-made files as fileimport's tests are:
  - A donor with eng and jpn tracks is placed, and the AudioGraft carries
    the languages, the anchor and the release.
  - A donor with only jpn is blocked with the rejected-file message.
  - A rescan of a root folder holding `.clustarr/donors` reports nothing
    unmatched from it.
  - librarydelete removes the donor directory.
- [ ] Watch them fail. Implement. Run
  `go test ./app/import/worker/fileimport/ ./app/import/controller/librarydelete/ ./app/import/worker/rescan/`
  with assets. Expected: PASS. Commit.

### Task 9: UI, docs and the live run

- The episode rows and the movie page show the audio state: wanted, present
  and missing, and the graft state, with the AudioGraft's reason when it
  failed.
- Update CLAUDE.md (the anime paragraph) and the spec's "As built: phase 4"
  section.
- Gate, push and deploy, with images controller, media and transcoder.
- Live run: monitor Monster S01E02, then watch the video search, the donor
  search, the donor grab, the import, the AudioGraft, the Job, the swap and
  the re-probe, ending at `status.audio.graft: done` with `present [ja, en]`.

- [ ] A UI test that the episode row renders the graft state. Watch it
  fail, implement, run `go test ./ui/...`. Commit with the docs.

---

## Addendum (2026-10-06, owner request): reduce at once; transcode and graft in one pass

**Part 1.** An AudioGraft whose donor is not yet reduced gets a reduce-only Job
(`grafttask.Task.Mode: reduce`) at once, whatever the target's state: the
donor's video leaves the disk as soon as it lands. The result records
`AudioGraft.status.donorAudioPath`; a donor lacking its languages fails there,
a donor fault. Grafts read the `.mka`.

**Part 2.** A graft rides along with a transcode of its file:

- The TranscodeJob dispatcher attaches the item's AudioGraft to the task
  (`task.Task.Graft`, a `grafttask.Task`) when its donor is reduced, the dub is
  still missing, and no standalone graft is running or failed for the donor
  and file. It records `TranscodeJob.status.graft` (phase `Joined`).
- The pool worker aligns the anchors before encoding (the in-process engine,
  through an optional `worker.GraftEngine`). On success one `engine.Run` writes
  the transcode and the dub; verify expects one more audio track, and the mux
  check runs on the output. A failed alignment leaves the transcode alone; a
  failed mux check fails the attempt, and the retry carries no graft.
- The worker's result carries the graft's (`task.StatusEvent.Graft`), which
  the results consumer writes into `TranscodeJob.status.graft`.
- The AudioGraft controller, the AudioGraft's only status writer, mirrors it:
  Running while joined, then the result. When its file is one a
  TranscodeProfile will transcode (`graftstate.WouldTranscode`), it waits for
  that transcode (reason `WaitingForTranscode`, at most `joinWait`, 6 h) rather
  than graft first. A transcode that ran without it, or a wait that ran out,
  falls back to a standalone graft.
- The TranscodeProfile leaves alone only a file under a standalone graft Job:
  a reduce, or a graft waiting to join, does not hold the transcode back.
- `Grafting` and `WouldTranscode` live in `app/squash/graftstate`, which both
  controllers import.

Rulings: the joined result is a transcode (`CLUSTARR_PROFILE` and
`CLUSTARR_GRAFT`, `spec.original` false), incorporated as a swap; the 6 h wait
bounds how long a dub waits on a full transcode window; a mux check failure
re-encodes without the dub on the next attempt rather than keep a bad dub.
