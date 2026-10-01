# ffgo Phase 3 (the standard plan and the in-process engine) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** squasharr transcodes in-process on the ffgo fork: a pure `standard.Plan` decides spec §1's fixed standard (HEVC Main 10, Apple TV audio, HDR10/HLG kept, Dolby Vision stripped or skipped), a goroutine `engine.Run` executes it on FFmpeg 9 with no `ffmpeg` subprocess, and the worker's `--engine=ffgo` runs it beside today's argv engine, selected per installation by the controller's `--worker-engine`. Proven on kind-cluster-plex's NVIDIA pool against the argv engine's speeds.

**Architecture:** Two new packages beside today's `pkg/transcode` (which stays, unchanged, for the argv engine until Phase 5):
- `pkg/transcode/standard` — `Plan(info transcode.MediaInfo, p Profile, hw Hardware) Plan`, pure and table-tested; `Plan` is a JSON-serialisable description (decision; video copy or encode with the encoder name, its options, the decode path and GPU filter; each audio track's copy or AAC with channels; subtitles, attachments, chapters copied; tags; the expected output) whose SHA-256 is the plan hash.
- `pkg/transcode/engine` — `Run(ctx, plan, input, output, Options) (Result, error)`: one demuxer goroutine, a goroutine per encoded stream (video: decoder → filter graph → encoder; audio: decoder → resampler → AAC), copied streams straight to the muxer, bounded channels, one context cancelling every stage, the first error returned; progress from the muxer's timestamps; FFmpeg's log tail captured through ffgo's log callback. `Verify(output, plan.Expect)` probes the output through ffgo.
The controller plans with `standard.Plan` and records it when `--worker-engine=ffgo`; the worker re-plans from its re-probe, compares hashes as today, and runs the engine. Exit codes, the `.part` output, the re-probe, verification failure (exit 4), the recycle-bin swap and telemetry keep their shape.

**Tech Stack:** Go 1.27 (`CGO_ENABLED=0`), the ffgo fork (`github.com/mediactl/ffgo`, Phase 1 primitives; new tags as the engine needs fork fixes), controller-runtime, envtest, the distroless images (Phase 2), FFmpeg 9.0.1 on the workstation (RTX 2070 Max-Q, Comet Lake iGPU), kind-cluster-plex.

**Spec:** `docs/superpowers/specs/2026-09-30-ffgo-transcoding-design.md` §1 (the standard), §3 (the pipeline), §5 (API, rollout, testing), the Spike result (the owner's archival NVENC settings), the 2026-10-01 amendment. Roadmap: `docs/superpowers/plans/2026-09-30-ffgo-phase0-spike.md` (Phase 3 row).

## Global Constraints

- The argv engine and the full profile surface stay working and the default (`--worker-engine=ffmpeg`) until Phase 5's switch; Phase 3 adds, it does not remove.
- API changes are additive and pre-alpha: `TranscodeProfile.spec.quality` (`*int32`, 0-51, default 24 through an accessor, since a typed client cannot send 0 otherwise); `TranscodeJob.status.plan` gains `engine`, `planHash`, `decode`, `videoAction`. Every status list stays capped.
- §1 exactly: HEVC Main 10 (`yuv420p10le`/`p010`), resolution and frame rate unchanged; skip when video is HEVC Main 10 and every kept audio track is copyable; video copy when only audio needs work; HDR10/HLG keep colour tags, mastering display and light level; HDR10+ dropped; DV 7/8.1 → HDR10, 8.4 → HLG, 5 skipped with a reason; audio AAC/AC-3/E-AC-3 copied, everything else AAC at 64 kbps per channel, above 5.1 downmixed to 5.1, the original not kept; `audio.languages` filters, commentary always kept, dispositions and language tags preserved; all subtitles, attachments, chapters, container metadata copied; `CLUSTARR_PROFILE` written; `policy.neverTranscodeModifiers` skips.
- Quality: the profile's one `quality` maps per encoder by a code table: libx265 `crf` = quality; hevc_nvenc `qp` = quality − 1 (the owner's archival default qp 23 at the spec's default quality 24), preset p7, rc constqp, spatial and temporal AQ, no `-cq` (ignored under constqp, measured); hevc_qsv `global_quality` = quality; hevc_vaapi `qp` = quality. Device limits clamp as today (`transcode.Limits.Apply`).
- Pure functions where the logic is tricky (`standard.Plan`); table tests for every §1 rule; plan goldens (JSON) replace argv goldens for the new engine.
- Every fix falsified (reverted, its test seen to fail by name). Engine tests run against real FFmpeg 9 libraries on generated clips, one per file class, on the CPU; GPU paths skip by name without hardware and run on the workstation.
- Status writes only through `pkg/k8s.PatchStatus`; one field manager per writer; the CAS path for `TranscodeJob.status` (CLAUDE.md).
- Commits on clustarr `main` with a pathspec; never `git stash`; the controlling session pushes; deploys to kind-cluster-plex only through the Helm release `clustarr` in `clustarr-system` with `--reuse-values`, images side-loaded with `kind load`; downloads stay paused.

## Review Focus

- **Stream timestamps and interleaving:** copied streams and encoded streams in one muxer, with B-frames, a non-zero start time, and an audio track that starts later than the video; the muxer must not starve or buffer the whole file. Pinned by Task 4's multi-stream test (duration and A/V start within one frame of the source).
- **A source whose decoder output differs from its container's claims** (a 6-channel track whose frames are 5.1(side); a frame-rate change mid-stream): the engine takes layouts and parameters from decoded frames, not the probe. Pinned by Task 5.
- **Cancellation and errors mid-stream:** a cancelled context or a failing stage stops every goroutine, removes the `.part`, and returns the first error with FFmpeg's message; no goroutine leaks. Pinned by Task 4's cancel test (goroutine count back to baseline).
- **Back-pressure:** a slow encoder slows the demuxer (bounded channels); memory stays bounded on a long file. Pinned by Task 4's slow-consumer test.
- **Dolby Vision profile 7 dual-layer:** the enhancement layer is a second video stream; the plan copies none of it and strips the RPU from every frame. Pinned by Task 2 (plan) and Task 6 (side-data removal, as Phase 1 tested it).

---

### Task 1: API additions

**Files:** `api/transcode/v1alpha1/transcodeprofile_types.go`, `api/transcode/v1alpha1/transcodejob_types.go`, generated deepcopy/applyconfigurations, `config/crd/bases/*`, `pkg/crdcheck` (if a guard needs the new default), `app/squash/worker/profile.go` (the accessor).

**Interfaces — Produces:**
- `TranscodeProfileSpec.Quality *int32` (`+kubebuilder:validation:Minimum=0`, `Maximum=51`, `+optional`), `func (s *TranscodeProfileSpec) QualityOrDefault() int32` (24 for nil)
- `Plan.Engine string` (`ffmpeg`|`ffgo`, `+optional`, `+kubebuilder:validation:Enum`), `Plan.PlanHash string`, `Plan.Decode string` (`cpu`|`nvdec`|`upload`), `Plan.VideoAction string` (`copy`|`encode`)

- [ ] Write `TestQualityOrDefault` (nil → 24, explicit 0 → 0) and an envtest that creates a profile with `quality: 0` from a typed client and reads 0 back (`pkg/crdcheck`'s typed-client trap); run → compile failure; add the fields and markers; `make generate manifests`; run → PASS; `TestNoCRDDefaultIsUnreachableFromGo` and `TestEveryStatusListIsCapped` still pass; commit.

### Task 2: `standard.Plan` — §1 as a pure function

**Files:** create `pkg/transcode/standard/{doc,plan,profile,quality,hash}.go`, `plan_test.go`, goldens under `test/data/transcode/standard/`.

**Interfaces — Produces:**

```go
type Profile struct {
    Name, Hash string          // CLUSTARR_PROFILE = Name@Hash
    Quality    int32
    Languages  []string        // BCP-47 / ISO 639-2 as the probe gives them; empty keeps all
    NeverTranscodeModifiers []string
    Container  transcode.Container // mkv or mp4, as today
    MaxOutputToSourcePercent int32
}
type Hardware struct {
    Tier   transcode.Tier      // from the pool class: nvenc, qsv, vaapi, cpu
    Limits transcode.Limits    // device limits incl. NVDEC (ProbeDecoders)
}
type Decision string // "skip", "copyVideo", "encode"
type Plan struct {
    Decision  Decision; Reason string
    Container transcode.Container
    Video     VideoPlan
    Audio     []AudioPlan   // one per kept source audio track, in source order
    Subtitles []int32       // every subtitle stream
    Attachments, Chapters bool
    Tags      map[string]string
    Expect    Expectation
}
type VideoPlan struct {
    SourceIndex int32; Action string          // "copy" | "encode"
    Encoder string; Options map[string]string // e.g. hevc_nvenc {preset:p7 rc:constqp qp:23 spatial-aq:1 temporal-aq:1 bf:… rc-lookahead:…}
    Decode string                             // "cpu" | "nvdec" | "upload" | "vaapi" | "qsv"
    Filter string                             // "scale_cuda=format=p010le", "hwupload,scale_cuda=format=p010le", "format=yuv420p10le", "hwupload,scale_vaapi=format=p010", "hwupload=extra_hw_frames=64,vpp_qsv=format=p010"
    HDR string                                // "sdr" | "hdr10" | "hlg"
    StripSideData []string                    // "hdr10plus", "dovi"
    Color ColorTags                           // primaries, transfer, matrix, range
}
type AudioPlan struct {
    SourceIndex int32; Action string // "copy" | "aac"
    Channels int32; Layout string     // aac only: source's, capped at 5.1 ("5.1" for >6)
    BitRate int64                     // 64000 * channels
    Language, Title string; Disposition int32
}
func Plan(info transcode.MediaInfo, p Profile, hw Hardware) Plan
func (p Plan) Hash() string // sha256 of the canonical JSON
```

- [ ] Write the table test first, one row per §1 rule: H.264 SDR → encode (cpu: libx265 crf 24 `format=yuv420p10le`; nvenc with NVDEC-decodable source: decode nvdec, filter `scale_cuda=format=p010le`, options as the archival set with qp 23; nvenc with Hi10P: decode upload); HEVC Main 10 + AAC → skip; HEVC Main 10 + TrueHD → copyVideo with AAC 5.1 384k (TrueHD 7.1 downmixed); HEVC 8-bit → encode; HDR10 → encode, HDR hdr10, colour tags bt2020/smpte2084, strip hdr10plus; HLG → hlg tags; DV 8.1 → HDR10 strip dovi; DV 8.4 → HLG strip dovi; DV 7 → HDR10, enhancement stream not mapped; DV 5 → skip "Dolby Vision profile 5"; audio E-AC-3 Atmos → copy; FLAC stereo → AAC 128k; DTS-HD 7.1 → AAC 5.1 384k; languages [eng] drops fre unless it is commentary; commentary kept; subtitles/attachments/chapters all copied; modifier in neverTranscodeModifiers → skip; quality 30 → crf 30 / qp 29 / global_quality 30; device limit bFrames 5 clamps the nvenc option. Run → compile failure.
- [ ] Implement `Plan` from those rules (reusing `transcode.NVDECDecodes`, `transcode.Limits.Apply`, `transcode.MediaInfo`'s HDR classification); run → PASS; write JSON goldens for six representative plans (`-update` flag as the argv goldens do); falsify two rules (the 5.1 cap, the commentary keep); commit.

### Task 3: Fork prerequisites the engine needs

**Files:** in `mediactl/ffgo`: whatever Tasks 4-6 find missing, each with its own failing test; carried minors that the engine hits: `avcodec.SetCtxFlags` sign extension (Phase 2 review minor 5), `WritePacket` documented as taking the packet (Phase 1 review minor 2), a `StreamDecoderConfig.DecoderName` (Phase 1 recommendation, AV1 on NVDEC), a log-callback capture helper if `log.go` lacks one usable per run.

- [ ] For each: failing test in the fork, fix, falsify, suite green (FFmpeg 9 local, Debian 12 container), push, CI green, tag `v0.0.0-clustarr.N`, bump clustarr's `replace`. This task is open-ended by design: it runs whenever Tasks 4-6 hit a fork gap, and its ledger lines list each.

### Task 4: `engine.Run` — demux, copy, mux, progress, errors

**Files:** create `pkg/transcode/engine/{doc,engine,stages,progress,log}.go`, `engine_test.go`, `helpers_test.go` (clip generators, like the fork's).

**Interfaces — Produces:**

```go
type Options struct {
    Progress   func(transcode.Progress) // muxer-time based, at most 1 Hz
    HWDevice   *ffgo.HWDevice           // opened by the caller for GPU decodes/filters (nil: CPU)
    LogTail    int                      // bytes of FFmpeg's log kept for status.stderrTail (default 4096)
    QueueDepth int                      // packets/frames per stage channel (default 16)
}
type Result struct{ Frames int64; DurationMillis int64; LogTail string }
type Error struct{ Stage string; Err error; LogTail string } // Unwrap; Stage "demux"|"video"|"audio:N"|"mux"
func Run(ctx context.Context, p standard.Plan, input, output string, o Options) (Result, error)
```

- [ ] Tests first: a remux-only plan (copyVideo with every audio copied) of a generated MKV with H.264, two audio tracks (AC-3 default, AAC commentary), SRT forced, a font attachment, two chapters → ffprobe JSON matches stream by stream (codec, language, title, dispositions), chapters, attachment, `CLUSTARR_PROFILE` tag, duration within 0.1 s; a cancel test (cancel after the first progress callback → `ctx.Err()` returned, `.part` gone, `runtime.NumGoroutine()` back to its baseline within 1 s); a slow-consumer test (a progress callback that sleeps; peak RSS growth bounded — measured with `runtime.ReadMemStats` HeapInuse below 64 MiB for a 30 s clip); a corrupt-input test (a truncated file → `*Error` with Stage "demux" or the decoder's and a non-empty LogTail). Run → compile failure.
- [ ] Implement: the demuxer goroutine reads packets and routes clones to per-stream channels (copied streams → the mux channel with their time base; encoded streams → their stage); the mux goroutine owns the `ffgo.Muxer` (`AddCopyStream` with `StreamOptions` from `Decoder.Streams`; `AddEncoderStream` for encoders created by the stages before `WriteHeader` — the stages report their encoder's parameters on a setup channel, and the muxer writes the header once every stream is known); `errgroup` with a shared context; progress from the highest muxed DTS in seconds against the input duration; ffgo log callback into a ring buffer. Run → PASS; falsify the clone (send the demuxer's packet) and the cancel cleanup; commit.

### Task 5: The audio stage

- [ ] Tests first: TrueHD 7.1 (`-c:a truehd -strict -2`) → AAC 5.1 384k, levels by channel as the fork's downmix test; FLAC stereo → AAC 128k; E-AC-3 copied; an audio track starting 0.5 s after the video → output start within one AAC frame; a 6-channel 5.1(side) track → layout from decoded frames. Implement the stage (StreamDecoder → Resampler built from the first frame's `ChannelLayout()` → AudioEncoder with `InputTimeBase` the stream's) and commit.

### Task 6: The video stage — CPU and GPU

- [ ] Tests first (CPU, libx265 at `preset=ultrafast` in tests through an `Options` override): H.264 SDR → HEVC Main 10 `yuv420p10le`, colour tags passed; HDR10 → mastering display and light level at stream level and in-band, smpte2084; HLG → arib-std-b67 tags; frames with HDR10+ side data → none reach the encoder; Hi10P (H.264 10-bit) → encodes; a GOP-12 source → the encoder's own GOP (Phase 1 review #4). GPU tests (skip by name): NVDEC path and upload path on hevc_nvenc with the archival options, 48 frames in → 48 packets; VAAPI and QSV on the Intel node when present. Implement the stage (StreamDecoder with the plan's decode, FilterGraph built from the first frame with the plan's filter and the device, VideoStreamEncoder by name with options and side data, `RemoveSideData` per frame) and commit.

### Task 7: `engine.Verify` and the worker's `--engine=ffgo`

**Files:** `pkg/transcode/engine/verify.go`; `app/squash/worker/{run,options,engine}.go`; `cmd/squasharr-worker/main.go`; `app/squash/task/task.go` (`Task.Engine`, `Task.PlanHash`).

- [ ] Tests first: `Verify` passes a good output and fails (with the reason) one whose duration is short by 5 %, whose video is not HEVC Main 10, or whose stream count differs; the worker, run on a generated source with `--engine=ffgo` through the existing `Process` test harness, produces a verified swap with the tag and the same exit codes as the argv engine (0, 3 on a changed source, 4 on a forced verify failure), progress in telemetry, and `stderrTail` from the engine's log tail. Implement: `Options.Engine`; in `runner.run`, when the task's engine is ffgo, plan with `standard.Plan` from the re-probe, compare `PlanHash` with the recorded one (a mismatch is logged and the local plan used, as today's argsHash), open the device for the tier, `engine.Run` into the `.part`, `engine.Verify`, then the existing swap. Commit.

### Task 8: The controller's `--worker-engine`

**Files:** `app/squash/controller/transcodejob/{plan,dispatch}.go`, `app/squash/controller/pool/render.go`, `cmd/clustarr` flags, chart (`squasharr.workerEngine`, default `ffmpeg`) and kustomize, chart/kustomize agreement tests.

- [ ] Tests first: with `--worker-engine=ffgo`, a planned TranscodeJob records `status.plan.engine: ffgo`, `planHash`, `videoAction`, `decode`, per-track audio and the subtitles from `standard.Plan`; the pool pod template carries `--engine=ffgo`; changing the flag re-renders the pool (drift → recreate after draining, as an image change does) and re-plans Planned jobs whose `plan.engine` differs (dispatch re-plans a plan whose engine is not the pool's, as it re-plans one whose class differs). With `ffmpeg`, everything is as today (existing tests unchanged). Implement and commit.

### Task 9: On kind-cluster-plex

- [ ] Build the media and `transcoder-cuda-distroless` images at the merged commit, `kind load` them, `helm upgrade --reuse-values --set image.media.tag=<sha> --set image.transcoderCuda.tag=<sha> --set squasharr.workerEngine=ffgo`; watch the NVIDIA pool drain and recreate; the first ten ffgo jobs must succeed and verify; compare their speed and the pool pod's CPU with the argv engine's NVDEC jobs and the CPU-decode baseline (1080p H.264: 4.06x, 102 fps median over 99 jobs). If any job fails, roll back with `--set squasharr.workerEngine=ffmpeg` (one helm upgrade) and fix with a test. Record the numbers in the spec's rollout section.
