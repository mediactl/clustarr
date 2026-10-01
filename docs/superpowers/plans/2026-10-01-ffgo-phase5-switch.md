# ffgo Phase 5 (parity, the switch, and deleting the argv engine) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** the in-process engine becomes squasharr's only engine: the worker probes through ffgo (no `ffprobe`), a parity harness proves the standard on real files of every class, `--worker-engine` defaults to ffgo and then goes, `TranscodeProfile` drops its encoding knobs, and the argv engine, its binary measurements, `ffmpeg`/`ffprobe` in the transcoder image and the Debian transcoder image are deleted.

**Architecture:** Spec §5 Rollout, in order. (1) The fork exposes the stream facts `ffprobe` reports and ffgo does not yet (codec profile name, colour properties, bits per raw sample, field order). (2) `inprocess.Probe` fills a `mediainfo.Raw` from ffgo and maps it with `pkg/mediainfo`'s own mapping (exported as `mediainfo.FromRaw`), so the worker's re-probe and output probe keep every classification rule; a test holds it equal to `mediainfo.Probe` on generated fixtures. (3) A build-tagged parity harness runs both engines over short clips cut from the owner's library and compares the probed outputs. (4) The default flips to ffgo. (5) The API cut the owner chose (2026-10-01): encoding knobs go, operational fields stay; `status.hash` covers the standard's inputs plus a version constant. (6) The argv engine and everything only it uses are deleted.

**Tech Stack:** Go (`CGO_ENABLED=0`), the ffgo fork (new tag), controller-runtime, envtest, the distroless transcoder image, FFmpeg 9.0.1 and the RTX 2070 Max-Q on the workstation for the parity harness.

**Spec:** `docs/superpowers/specs/2026-09-30-ffgo-transcoding-design.md` §5 (API, Images, Rollout, Testing). Phase 4 plan: `docs/superpowers/plans/2026-10-01-ffgo-phase4-hardware.md`.

## Global Constraints

- The owner's API cut (2026-10-01): **remove** `video.*`, `hdr.*`, `subtitles.*`, `verify.*`, `audio.*` except `audio.languages`, and the compliance policy (`policy.skipIfCompliant`, `policy.remuxOnlyWhenVideoCompliant`); **keep** `selector`, `default`, `priority`, `hardware`, `gpu`, `resources`, `scratch`, `activeDeadline`, `ttlSecondsAfterFinished`, `maxConcurrent`, `container`, `quality`, `audio.languages`, and `policy.replaceSource`, `policy.recycleBin`, `policy.maxOutputToSourcePercent`, `policy.minDuration`, `policy.neverTranscodeModifiers`.
- Re-transcoding is never an upgrade side effect (spec §5): a file squasharr transcoded stays skipped whatever the new hash (`alreadyTranscoded`: its tag, or its HEVC encoder tag), and the owner's selector excludes HEVC.
- `cmd/clustarr` stays statically linked (`TestClustarrNeverLinksADynamicLoader`).
- Transcoding on kind-cluster-plex stays paused until this phase is complete and deployed; the parity harness runs on the workstation's GPU, never on the cluster. CPU-heavy work is kept short (clips, NVENC).
- Status writes through `pkg/k8s.PatchStatus`; capped lists; commits on `main` with a pathspec; never `git stash`; the controlling session pushes.

## Review Focus

- **The worker's probe and catalogarr's disagree** on a field the standard reads (bit depth, HDR class, a DV profile, an audio layout, a language): the plan hashes differ (a warning) or, worse, the worker decides differently. Pinned by Task 2's equality test over generated fixtures of each class.
- **A stored profile written before the cut** still carries the removed fields: the apiserver prunes them at the next write, the controller never reads them, and the new hash does not requeue anything. Pinned by Task 5.
- **A TranscodeJob recorded with an argv plan** when the argv engine is gone: it is re-planned at dispatch, never run as argv and never failed. Pinned by Task 6.
- **The image without `ffmpeg`/`ffprobe`**: nothing in the worker execs either (a guard over its import graph and an image self-check). Pinned by Task 6.
- **Parity's tolerances hide a real regression** (a dropped track, lost HDR metadata): the harness compares structure exactly and only timing within tolerance. Pinned by Task 3.

---

### Task 1: The fork exposes what ffprobe reports

**Files (fork `/home/appkins/src/mediactl/ffgo`):** `streaminfo.go`, `internal/shim` or `avcodec` accessors, a test; tag `v0.0.0-clustarr.7`; clustarr `go.mod`.

- [ ] Test `TestStreamsReportProfileColourDepthAndFieldOrder` (a libx265 Main 10 clip tagged bt2020/smpte2084/tv, an interlaced mpeg2 clip): `StreamInfo.Profile == "Main 10"`, `ColorPrimaries "bt2020"`, `ColorTransfer "smpte2084"`, `ColorSpace "bt2020nc"`, `ColorRange "tv"`, `BitsPerRawSample 10`, `FieldOrder "tt"` for the interlaced clip. FAIL, then implement from `AVCodecParameters` (profile through `avcodec_profile_name`; colour through `av_color_*_name`; `bits_per_raw_sample`; `field_order`), PASS; FFmpeg 5.1 in the fork's CI.
- [ ] Commit, tag, push the fork; bump clustarr's `replace`.

### Task 2: `inprocess.Probe` (no ffprobe in the worker)

**Files:** `pkg/mediainfo/mediainfo.go` (export `FromRaw(raw *Raw) *commonv1.MediaInfo` over the existing mapping), `app/squash/worker/inprocess/probe.go`, `probe_test.go`, `app/squash/worker` (an `Engine.Probe` used for the re-probe and the output probe when the engine is present).

- [ ] Test `TestTheInProcessProbeAgreesWithFFprobe`: for generated fixtures -- SDR H.264 + AC-3 5.1 + SRT + font + chapters; HEVC Main 10 HDR10 with mastering display and light level; HLG; Hi10P; multi-audio (E-AC-3 eng default, AAC fre commentary) -- `mediainfo.FromRaw(inprocess.Probe(f))` equals `mediainfo.Probe(f)` field by field, and `transcode.FromProbe` of both plans the same standard hash. FAIL, implement, PASS.
- [ ] The worker's ffgo path re-probes and probes its output through the engine; `TestRunWithTheInProcessEngine...` runs with `ffprobe` removed from `PATH`.
- [ ] Commit.

### Task 3: The parity harness

**Files:** `test/parity/parity_test.go` (`//go:build parity`), `hack/parity-clips.sh`, results in the spec's Rollout section.

- [ ] `hack/parity-clips.sh` cuts 60 s stream-copied clips (`ffmpeg -ss <mid> -t 60 -c copy`) of one library file per class into `$CLUSTARR_PARITY_DIR`, chosen from the cluster's MediaFile probe summaries: SDR H.264, HEVC 8-bit, HDR10, HLG, DV 7, DV 8.1, Hi10P, TrueHD + E-AC-3 multi-audio, PGS, fonts.
- [ ] The harness runs each clip through the argv engine (NVENC tier, the live profile) and the standard (NVENC), probes both outputs and asserts: same video codec family (HEVC), the standard's bit depth by its rule, the same audio track count and languages, each kept track direct-play or AAC, every subtitle and attachment kept, chapters kept, HDR10 mastering metadata kept, DV 7/8.1 out as HDR10, duration within 1 s. A mismatch prints both probes.
- [ ] Run it on the workstation; record per-class results and speeds in the spec. Commit.

### Task 4: The default becomes ffgo

**Files:** `cmd/clustarr/services.go` (`--worker-engine` default `ffgo`), chart `values.yaml`/schema, `config/manager/squasharr.yaml`, tests.

- [ ] Test the default; flip it; commit.

### Task 5: The API cut and the new hash

**Files:** `api/transcode/v1alpha1/transcodeprofile_types.go` (fields removed per the Global Constraints), generated code, CRDs, `app/squash/worker/profile.go` (`ProfileSpec`, `StandardProfile`), `app/squash/controller/transcodeprofile` (hash: quality, languages, modifiers, container and `standard.Version`), `pkg/transcode/standard` (`Version` constant), samples and docs, tests.

- [ ] Tests: `TestTheHashCoversOnlyTheStandardsInputs` (changing a kept operational field leaves the hash; changing quality, languages, modifiers or container moves it; raising `standard.Version` moves it); `TestAStoredProfileWithRemovedFieldsStillReconciles` (an unstructured profile carrying `video.crf` is accepted and pruned); `TestANewHashRequeuesNothingTranscoded` (a MediaFile tagged with the old hash, or with an HEVC encoder tag, gets no job). FAIL, implement, PASS; `make generate manifests`; suites green.
- [ ] Commit.

### Task 6: Delete the argv engine

**Files:** `pkg/transcode` (argv `Plan`, `Args`, runner, CLI verify, `ProbeCapabilities`/`ProbeLimits`/`ProbeDecoders`, NVENC/QSV argv builders; keep `MediaInfo`, `FromSummary`/`FromProbe`, `NVDECDecodes`/`NVDECKey`, tiers, `Limits.NVDEC`), `app/squash/worker` (argvJob, `CheckFFmpeg`, the binary limits cache), `app/squash/controller/transcodejob` (argv planning, `--worker-engine`, argv status fields), `api/transcode` (`status.plan` argv fields; `encoderLimits` B-frame/lookahead), `images/Dockerfile.transcoder-distroless` (no `ffmpeg`/`ffprobe`), `images/Dockerfile.transcoder` (deleted), image names (`transcoder-distroless` becomes `transcoder`), CI/release/Makefile/chart, tests.

- [ ] Tests: `TestAJobRecordedWithAnArgvPlanIsReplanned`; `TestTheWorkerNeverExecsFFmpeg` (AST guard: no `exec.Command*` naming ffmpeg/ffprobe under `app/squash` and `pkg/transcode`); the image self-check asserts `/usr/bin/ffmpeg` and `/usr/bin/ffprobe` are absent. FAIL, delete, PASS; every suite green.
- [ ] Commit in its own commit (spec: "deleted in their own commit").

### Task 7: e2e scenario 12, docs

- [ ] Rewrite `test/e2e/transcode_test.go` scenario 12 for the standard (written; e2e runs are Phase H's, by the owner's standing instruction). Spec "as built", CLAUDE.md, ADR index if an ADR is superseded. Final whole-phase review.
