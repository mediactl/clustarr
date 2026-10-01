# ffgo Phase 4 (choosing the hardware) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** a pool pod measures its own device in-process through ffgo at start -- does the class's encoder open and encode, which formats NVDEC decodes -- and publishes the result with its health; the controller sends no work to a GPU class whose pods all report unhealthy; and every class runs the one transcoder image, Intel's included.

**Architecture:** The cluster layer of spec §4 already exists (`transcodejob.ChooseClass`: NVIDIA, then Intel, then CPU, by GPU node label, free slot and schedulability; `rerouteUnschedulable`; fallback to CPU). Phase 4 adds the pod layer: `inprocess.Engine.Measure` runs the real pipeline (`engine.Run`) on tiny samples it makes in-process, `worker.Serve` measures once at start and before pulling, re-measures an unhealthy device every two minutes without pulling, and publishes `Healthy`/`Error` beside the limits under the existing `clustarr-progress` `encoder-limits.<class>` key; the controller reads health with the limits, treats a class whose fresh reports are all unhealthy as unavailable (like an unschedulable pool) and shows health in `TranscodeProfile.status.encoderLimits`. The argv engine keeps its binary-based measurements until Phase 5 deletes it.

**Tech Stack:** Go (`CGO_ENABLED=0`), the ffgo fork (v0.0.0-clustarr.6), controller-runtime, envtest, the distroless images, FFmpeg 9.0.1 on the workstation (RTX 2070 Max-Q, Comet Lake iGPU).

**Spec:** `docs/superpowers/specs/2026-09-30-ffgo-transcoding-design.md` §4 (choosing the hardware), §5 Images; ADR-0015 (one image for every class). Phase 3 plan: `docs/superpowers/plans/2026-10-01-ffgo-phase3-engine.md`.

## Global Constraints

- `cmd/clustarr` stays statically linked: nothing it imports may import ffgo or purego (`TestClustarrNeverLinksADynamicLoader`). In-process code lives in `app/squash/worker/inprocess`, reached through `worker.Engine`.
- The argv engine (`--worker-engine=ffmpeg`, still the default) and its binary-based measurements (`transcode.ProbeLimits`, `ProbeDecoders`, `ProbeCapabilities`) keep working unchanged; Phase 4 changes only the ffgo path and what every pod publishes.
- One image for every class (ADR-0015): no per-class worker image setting.
- Status writes only through `pkg/k8s.PatchStatus`; every status list capped; API changes additive (pre-alpha).
- Transcoding on kind-cluster-plex stays paused (all TranscodeJobs `spec.suspend: true`) until Phase 5 is complete (owner, 2026-10-01). No deploy in this phase.
- Commits on `main` with a pathspec; never `git stash`; the controlling session pushes.

## Review Focus

- **A device that opens but cannot encode** (driver library missing, NVENC session limit): measured as unhealthy by the trial encode, not reported healthy because the device opened. Pinned by Task 1's trial being a real `engine.Run`.
- **A pod that starts unhealthy and recovers** (the GPU was briefly held by another pod): it re-measures and starts pulling without a restart. Pinned by Task 2.
- **Health that goes stale**: a report older than the limits' freshness window counts as absent, not unhealthy, so a class whose pods are gone is not starved forever. Pinned by Task 3.
- **No report at all** (a class with no pool pod yet): the class stays eligible -- the pool must be created before any pod can report. Pinned by Task 3.
- **QSV absent, VAAPI working** on an Intel node: the pod is healthy on VAAPI and the ffgo job encodes on VAAPI. Pinned by Task 1/2 (the measured tier is the one the job uses).

---

### Task 1: In-process measurement (`inprocess.Engine.Measure`)

**Files:** `app/squash/worker/inprocess/measure.go`, `app/squash/worker/inprocess/measure_test.go`.

**Interfaces:**
- Produces: `func (Engine) Measure(ctx context.Context, class transcode.Hardware) (Measurement, error)` in package `inprocess`, with `type Measurement struct { Tier transcode.Tier; Limits transcode.Limits }` declared in package `worker` (so the worker's interface can name it without importing ffgo) -- concretely `worker.Measurement`, and `inprocess` returns it through the interface method below. To avoid a cycle (`worker`'s tests import `inprocess`), the type lives in `pkg/transcode`: `type Measurement struct { Tier Tier; Limits Limits }`.
- Semantics: for `cpu`, a libx265 trial; for `nvidia`, open CUDA, an NVENC trial, then one NVDEC trial per `transcode.NVDECSampleFormats()` key (`h264:8` ... `mpeg2video:8`), recording each in `Limits.NVDEC.Formats`; for `intel`, QSV then VAAPI, the first whose trial succeeds is `Tier`. A trial is `engine.Run` of the standard's plan for that tier on a 5-frame 256x256 sample made in-process (`ffgo` encoder + muxer) in `os.TempDir()`. The device not opening, or every trial for the class failing, is `transcode.ErrDeviceUnavailable` wrapped with the cause.

- [ ] Step 1: `pkg/transcode`: add `type Measurement struct { Tier Tier; Limits Limits }` and export the NVDEC sample list as `NVDECSampleFormats() []NVDECSample` (key, encoder, pixel format), unchanged content.
- [ ] Step 2: write `measure_test.go`: `TestMeasureTheCPUClass` (FFmpeg 9 present → Tier cpu-x265, no error); `TestMeasureNVIDIAOnARealDevice` (skips without CUDA; Tier nvenc, `Limits.NVDEC.Formats["h264:8"]` true, `["h264:10"]` false on Turing); `TestADeviceThatWillNotOpenIsUnavailable` (`openDevice` replaced by a failing func through a package variable → `errors.Is(err, transcode.ErrDeviceUnavailable)`). Run: FAIL (undefined).
- [ ] Step 3: implement `makeSample(path, encoder, pixFmt string) error` (ffgo `NewVideoStreamEncoder` + `NewMuxer`/`AddEncoderStream`, 5 black frames), `trial(ctx, plan, sample, dev)` (`engine.Run` into a temp output, removed after), and `Measure`. Run: PASS (NVIDIA on the workstation).
- [ ] Step 4: commit `feat(squash): the in-process engine measures its pod's device`.

### Task 2: The worker measures at start and gates pulling on health

**Files:** `app/squash/worker/ffgo.go` (interface), `app/squash/worker/serve.go`, `app/squash/worker/limits.go`, `app/squash/task/limits.go`, tests in `serve_test.go`/`limits_test.go`, `app/squash/worker/inprocess/inprocess.go` (method set).

**Interfaces:**
- Consumes: `transcode.Measurement`, `transcode.ErrDeviceUnavailable`.
- Produces: `worker.Engine` gains `Measure(ctx, class transcode.Hardware) (transcode.Measurement, error)`; `task.PublishEncoderLimits` gains a health argument: `PublishEncoderHealth(ctx, kv, class, node string, l transcode.Limits, healthy bool, msg string, now time.Time) error`, with `PublishEncoderLimits` kept as the healthy case; node entries gain `Healthy *bool` and `Error string` (absent = healthy, for entries written by older pods).
- Semantics: when `Options.Engine != nil`, `Serve` measures the pod's class before its first pull, publishes the result, and caches the `Measurement` for ffgo jobs (their tier and NVDEC formats come from it, not from `ProbeCapabilities`/`ProbeDecoders`); an unhealthy result is published with its error, nothing is pulled, and the measurement is retried every `ServeOptions.RemeasureEvery` (default 2m) until healthy. With `Engine == nil` nothing changes.

- [ ] Step 1: tests -- `TestAPodWhoseDeviceWillNotOpenPullsNothingAndReportsIt` (fake engine `Measure` returns ErrDeviceUnavailable: no Pull within 200 ms, KV entry for the node has `healthy: false` and the error); `TestAPodRecoversWhenItsDeviceOpens` (fails first, succeeds on the second call with `RemeasureEvery` 50 ms: pulling starts, entry healthy); `TestTheFFgoJobUsesTheMeasuredTier` (measurement says VAAPI for intel: the job's plan encodes `hevc_vaapi`). Run: FAIL.
- [ ] Step 2: implement in `serve.go`/`limits.go`/`task/limits.go`/`ffgo.go`; `inprocess.Engine` satisfies the larger interface. Run: PASS; whole worker and task packages green.
- [ ] Step 3: commit `feat(squash): a pool pod measures its device at start and takes no work until it is healthy`.

### Task 3: The controller reads health

**Files:** `api/transcode/v1alpha1/transcodeprofile_types.go` (`EncoderLimit` gains `healthy *bool`, `message string` MaxLength 256), generated code and CRDs, `app/squash/task/limits.go` (`ReadEncoderHealth(ctx, kv, class, now) (healthy, unhealthy int, err error)` over fresh entries), `app/squash/controller/transcodejob/class.go`/`pools.go` (unavailable classes), `app/squash/controller/transcodeprofile` (status), tests.

- [ ] Step 1: tests -- `TestAClassWhosePodsAllReportUnhealthyGetsNoWork` (unit, `ChooseClass` with `unavailable[nvidia]` → cpu); envtest `TestAnAutoJobAvoidsAnUnhealthyGPUClass` (KV has only an unhealthy nvidia report → the job dispatches to cpu); `TestAStaleOrMissingReportLeavesTheClassEligible`; transcodeprofile envtest asserts `status.encoderLimits[].healthy` and `message`. Run: FAIL.
- [ ] Step 2: implement: `ChooseClass` takes `unavailable` (unschedulable or all-unhealthy); a pinned job of an unavailable class is held with the reported error as its message. Run: PASS; `make manifests generate`; squash suites green.
- [ ] Step 3: commit `feat(squash): no work goes to a GPU class whose pods report its device unhealthy`.

### Task 4: One transcoder image for every class

**Files:** `images/Dockerfile.transcoder-distroless`, `images/distroless/README.md`, `Makefile` (`DISTROLESS_TARGETS`), `.github/workflows/{ci,release}.yml`, `pkg/transcode/selfcheck` (class intel runs against `transcoder`), `docs/adr/0015-no-cuda-image.md` (Intel note), `cmd/clustarr/chart_images_test.go` if it lists targets.

- [ ] Step 1: guard test -- `TestTheOneTranscoderImageCarriesTheIntelStack` (Dockerfile text: the `transcoder` target copies the Intel staging tree; no `transcoder-intel` target remains). Run: FAIL.
- [ ] Step 2: fold `staging-intel` into `transcoder`; delete `transcoder-intel`/`-debug`; update the Makefile, CI and release matrices; build `transcoder` and run `--self-check=cpu`, `=cuda`, `=intel` on it locally. Run: guard PASS, three self-checks pass.
- [ ] Step 3: commit `build: the one transcoder image carries the Intel media stack`.

### Task 5: Docs and ledger

- [ ] Spec §4 "as built" note; CLAUDE.md Transcoding paragraph (pod health, one image); ledger lines; final whole-phase review.
