# In-process transcoding on ffgo, with a fixed standard

Date: 2026-09-30. Designed in conversation with the owner, section by
section; each section below was approved as written.

## Why

squasharr transcodes by building an `ffmpeg` command line
(`pkg/transcode.Plan` → `Args`) and running the static BtbN executable in
a pool pod. TranscodeProfile exposes the whole tuning surface of four
encoders, and the profile names its hardware class. The owner wants:

- **A fixed standard with minimal customization.** HEVC video always; audio
  Apple TV plays directly is kept, everything else becomes AAC.
- **Automatic hardware choice.** No profile names a hardware class.
- **FFmpeg in-process.** Decode, filter, encode and mux from Go through
  [ffgo](https://github.com/obinnaokechukwu/ffgo) (purego bindings, no
  cgo), with no `ffmpeg` subprocess.
- **Distroless images.** Only the libraries the worker needs, the worker,
  and CA certificates.

Decisions taken while designing, in order:

| Question | Decision |
| --- | --- |
| Why ffgo | All of: in-process FFmpeg, automatic GPU choice, fixed profiles |
| Approach | In-process on ffgo now, not staged behind today's pipeline |
| Profile surface | Selector, one quality value, audio languages, `policy.neverTranscodeModifiers` |
| Dolby Vision | Strip to HDR10 (profile 5 skipped; see §1) |
| The ffgo fork | Includes all of ffgo's existing functionality |
| Images | Distroless glibc, assembled with apko from Wolfi packages |

## What ffgo does and does not do today

Read from the repository on 2026-09-30 (master, last push 2026-08-01,
Apache-2.0, which GPL-3.0 code may use):

- **It cannot drive the GPU encoders.** `Encoder` resolves its encoder with
  `avcodec.FindEncoder(codecID)`, so HEVC is FFmpeg's default encoder
  (libx265); there is no way to ask for `hevc_nvenc`, `hevc_qsv` or
  `hevc_vaapi`. The low-level `avcodec.FindEncoderByName` exists, but the
  encoder takes no hardware frames context, and there are no bindings to
  allocate one (`av_hwframe_ctx_alloc`/`_init`) or to give a filter graph
  a device. NVDEC to NVENC without a copy through the CPU is not possible.
- **The pasted GPU-detection snippet is illustrative.** It comes from
  `examples/streaming-server/.nuggets/performance/encoding-performance.md`.
  `VideoEncoderConfig` has no `HWDevice` field, its helpers
  (`canLoadCUDA`, `getCUDADeviceCount`, `canLoadVAAPI`) do not exist, and
  the source files the note cites are not in the repository. ffgo's real
  equivalent, `AvailableHWDeviceTypes()`, opens a device context of each
  type, so it reports working devices; it does not count CUDA devices.
- **Audio is incomplete.** `Encoder` takes one audio stream, and its own
  comment says audio encoding "is not yet fully implemented". `Muxer`
  mixes copied and encoded streams (`AddCopyStream`), at a lower level.
- **FFmpeg 4.x-7.x only.** The images pin 9.0. ffgo has had struct-offset
  bugs on 7.x (its TODO, resolved for macOS by moving field access into
  its C shim).
- **What it has:** hardware decoding (`HWDecoder`), filter graphs,
  container and stream metadata, stream copy, subtitles, bitstream
  filters, resampling, and a C shim it loads at run time (prebuilt, or
  built from `shim/`).

## 1. The standard

Every file gets the same treatment; only the four settings in §5 vary.

**Video**

- Output is HEVC Main 10 (`yuv420p10le`) for HDR at any size and for
  anything above 1080p, and HEVC Main (`yuv420p`; NV12 surfaces on a GPU)
  for SDR at 1080p or less -- the owner's call, 2026-10-01
  (`transcode.EightBitTarget`, shared by both engines). Resolution and
  frame rate are unchanged.
- A file whose video is already HEVC Main 10, or HEVC Main within the
  8-bit target, and whose audio is all kept (below) is skipped; a 10-bit
  file is never encoded down to 8 bits. A file whose video is compliant but whose audio is
  not has its video copied and only its audio processed.
- HDR10 and HLG keep their colour tags and their mastering-display and
  light-level metadata. HDR10+ dynamic metadata is dropped.
- Dolby Vision:
  - profiles 7 and 8.1 are re-encoded as HDR10 without the DV layer (their
    base layer is HDR10);
  - profile 8.4 is re-encoded as HLG (its base layer is HLG);
  - profile 5 is skipped with a reason. Its base layer is in Dolby's own
    colour space, and without the DV layer it plays with wrong colours.
- Quality is the profile's one `quality` value, mapped to each encoder's
  own control: CRF (libx265), CQ (NVENC), `global_quality` (QSV), QP
  (VAAPI). The calibration table is code. Each encoder's other settings
  are fixed at today's defaults, clamped to the device's measured limits
  (encoder limits, as now).

**Audio (Apple TV direct play)**

- AAC, AC-3 and E-AC-3 tracks are copied, E-AC-3 carrying Atmos included.
- Every other codec (TrueHD, DTS and DTS-HD, FLAC, PCM, Opus, MP3, and
  the rest) is encoded to AAC at 64 kbps per channel. Sources above 5.1
  are downmixed to 5.1. The original is not kept beside it, so a TrueHD
  Atmos track becomes 5.1 AAC.
- `audio.languages` filters the tracks kept. Commentary tracks are always
  kept. Dispositions and language tags are preserved.

**Everything else**

- All subtitle tracks, attachments (fonts), chapters and container
  metadata are copied, and the `CLUSTARR_PROFILE` tag is written.
- A file whose modifier is in `policy.neverTranscodeModifiers` is skipped.
- Unchanged from today: the re-probe before encoding (exit 3 when the
  source changed), verification of the output (exit 4), the recycle-bin
  hard link and rename over the source, and the output-size check (at
  most 100% of the source).

## 2. The ffgo fork

`mediactl/ffgo`, pinned by tag in `go.mod`. Changes land in the fork with
a test and come back here as a tag, and each is offered upstream as a pull
request. The plex-postgresql fork works the same way.

The fork is a **strict superset** of ffgo: nothing upstream is removed or
narrowed, and upstream changes are merged in regularly. Its CI runs ffgo's
whole test suite on FFmpeg 9.0 and on the 4-7 versions upstream supports.

Additions, in order:

1. **FFmpeg 9 support** across all of ffgo, not only the paths squasharr
   uses. The shim is built against FFmpeg 9's headers; field access whose
   offset moves between versions goes through the shim; the libraries'
   versions are asserted at load.
2. **Encoder by name.** `VideoEncoderConfig.EncoderName` (`hevc_nvenc`,
   `hevc_qsv`, `hevc_vaapi`, `libx265`), on `FindEncoderByName`.
3. **GPU frames from decoder to encoder.**
   - hardware frame pool bindings (`av_hwframe_ctx_alloc`/`_init`);
   - a device and frame pool on an encoder;
   - the same on a filter graph's input, so `scale_cuda`, `vpp_qsv` and
     `scale_vaapi` run on GPU frames.
4. **One muxer, many streams.** Each stream copied or encoded, with
   per-stream language and disposition, attachments, chapters and
   container metadata.
5. **Frame side data.** Mastering-display and light-level metadata reach
   the encoder; HDR10+ and Dolby Vision RPU data can be removed per frame.
6. **Audio encoding.** AAC finished, and 7.1 resampled to 5.1 (on the
   existing `Resampler`).

## 3. The pipeline

`pkg/transcode` becomes two parts.

**`Plan`** stays pure: no I/O, table-tested. From the probe, the profile
and the pod's hardware report it decides:

- skip, video copy or video encode, with the reason;
- the hardware path: NVDEC or CPU decode, the encoder by name, the GPU
  filter;
- each audio track: copy, or AAC and its channel count;
- the subtitles, attachments and chapters copied;
- the output it expects.

The controller records the plan in `TranscodeJob.status.plan`, as now.
Its hash is the hash of the plan's JSON, replacing today's argv hash, and
the worker compares its own plan with the recorded one, as now.

**`Engine`** runs a plan on ffgo as goroutines:

- one demuxer reads packets;
- copied streams go straight to the muxer;
- video goes decoder → filter graph (GPU or CPU) → encoder → muxer;
- each converted audio track goes decoder → resampler → AAC encoder →
  muxer.

Channels between stages are bounded, so a slow encoder slows the demuxer
instead of filling memory. One context cancels every stage; the first
error from any stage is returned, and the rest clean up.

Unchanged around it: the re-probe (exit 3), the `.part` output, the
verification against the plan's expected output (exit 4, now probed
through ffgo rather than `ffprobe`), the recycle-bin swap, and the exit
codes the Job's `podFailurePolicy` reads.

- **Progress** comes from the muxer's timestamps instead of `-progress`
  output. The `clustarr-progress` telemetry (1 Hz) and `status.progress`
  keep their shape.
- **Errors** carry FFmpeg's error code and message.
  `status.stderrTail` holds the tail of FFmpeg's log, captured through
  ffgo's log callback (one of the shim's jobs).

## 4. Choosing the hardware

Profiles name no hardware class. Two layers decide it.

**The cluster (the controller places the job)**

- The GPU classes come from node labels the vendor plugins already set:
  `nvidia.com/gpu.present=true` (NVIDIA GPU feature discovery) and
  `gpu.intel.com/device-id.*` (Intel's device plugin). squasharr keeps one
  pool per class that has nodes, plus a CPU pool, on the existing pool
  machinery.
- Preference is NVIDIA, then Intel, then CPU: a job goes to the first
  class whose nodes reported they can do it. A 10-bit source NVDEC cannot
  decode still encodes on NVIDIA, decoding on the CPU; a Dolby Vision 7 or
  8.1 source stripped to HDR10 can run on a GPU.
- With no GPU nodes, everything runs on the CPU pool. A GPU pool whose
  pods keep failing to start (today's `gpuUnavailable` path) sends its
  jobs to the next class rather than holding them.

**The pod (the worker checks what it was given)**

- At start a pool pod uses ffgo to open the devices it can see
  (`AvailableHWDeviceTypes()`, then `NewHWDevice` for CUDA, QSV and
  VAAPI), runs a trial encode with each GPU encoder by name (the encoder
  limits trial, in-process) and a trial decode of each format (as
  `transcode.ProbeDecoders` does today).
- It publishes the result under the `clustarr-progress` key the
  controller already reads; `status.encoderLimits` shows it per node.
- A pod whose class's device will not open reports so and takes no jobs;
  the controller sends that class no work until a pod reports healthy.

**As built (2026-10-01, Phase 4).** The cluster layer was already there
(`transcodejob.ChooseClass`, `rerouteUnschedulable`). A pool pod with the
in-process engine measures its class at start
(`app/squash/worker/inprocess.Engine.Measure`): the class's first tier whose
device opens and whose trial encode succeeds -- each trial the real
pipeline on a five-frame sample made in-process -- and, for NVENC, which
formats NVDEC decodes. It publishes the result with `healthy` and the
reason under its node in `encoder-limits.<class>`, pulls nothing while the
device is unusable, and measures again every two minutes. The controller
sends no work to a GPU class every fresh report of which is unhealthy
(auto jobs choose another class; pinned jobs wait, naming the reason); a
class with no fresh report stays eligible. `status.encoderLimits[]`
shows `healthy` and `message`. B-frame and lookahead limits stay the argv
engine's binary measurement: the standard sets neither. Every class runs
the one `transcoder` image, which carries the Intel runtime on amd64.

Not carried over from the snippet: round-robin over GPUs within a process.
The device plugin gives each pod its own GPU, so a pool pod uses `cuda:0`,
its own. VideoToolbox is macOS-only and not needed in the cluster; the fork
keeps it as upstream has it.

## 5. API, images, rollout, testing

### API

Pre-alpha, so `TranscodeProfile` shrinks in place. Its spec becomes:

- `selector`;
- `quality`: integer 0-51, default 24, a pointer so 0 can be sent from Go;
- `audio.languages`, capped;
- `policy.neverTranscodeModifiers`, capped.

Every other field goes, with the `Hardware` and per-encoder spec types.
Removed fields left in stored profiles are pruned by the apiserver the
next time each is written; the release notes give the edit for the
owner's profiles.

`TranscodeJob.status.plan` holds the new plan (decision, hardware path,
per-track actions, expected output) instead of an argv; `argsHash`
becomes `planHash`.

`status.hash` covers only `quality`, `audio.languages` and the modifier
policy, plus a version constant for the standard. A file already tagged
with a profile's hash is never transcoded again because the standard's
code changed; re-transcoding the library is a deliberate action, never a
side effect of an upgrade.

### Images

Built from [Wolfi](https://github.com/wolfi-dev/os) packages (glibc,
apk) and assembled with apko, in this repository under `images/wolfi/`.
Chainguard's own `cgr.dev` images are not used: their free tier is
`:latest` only. Wolfi's packages and the apko and melange tools are free
(Apache-2.0).

Wolfi's `ffmpeg-9.0` (9.0.2) is a shared GPL build with libx265, x264,
libvpx and SVT-AV1, which covers the CPU tier. Its build has no NVIDIA
codec headers and no libva headers, so it has no NVENC, NVDEC, CUDA
filters, VAAPI or QSV, and Wolfi packages no Intel media driver or libvpl
GPU runtime. So squasharr builds its own packages with melange:

- **`ffmpeg-clustarr`** 9.0.x: Wolfi's `ffmpeg-9.0` recipe plus
  `nv-codec-headers`, the CUDA filters built with clang (no CUDA toolkit),
  libva and libvpl; built shared, with the fork's C shim against the same
  headers.
- **The Intel media stack:** `gmmlib`, `intel-media-driver` (iHD) and the
  libvpl GPU runtime.
- **`squasharr-worker`:** the static Go binary (`CGO_ENABLED=0`).

apko images, each with no shell unless listed:

| Image | Packages |
| --- | --- |
| `transcoder` | glibc, `ca-certificates-bundle`, `ffmpeg-clustarr` libraries, `squasharr-worker` |
| `transcoder-intel` | `transcoder`'s, plus the Intel media stack |
| ~~`transcoder-cuda`~~ | removed 2026-10-01 ([ADR-0015](../../adr/0015-no-cuda-image.md)): nvidia pools run `transcoder`, the container toolkit injecting NVIDIA's driver libraries from the host |
| each `-debug` | the same plus `busybox`, for `kubectl exec` and the e2e suite |

The CA bundle is there because the OTLP trace exporter uses TLS unless it
is set insecure (`pkg/obs/tracing`); nothing else the worker does uses
TLS. apko produces an SBOM for each image. CI runs the worker's
`--self-check` on every built image: it loads every library through ffgo
and opens each encoder by name, so a missing library fails CI, not a job.

### Rollout

- The worker gets `--engine=ffmpeg|ffgo`. `ffmpeg` keeps today's argv
  engine and the full profile surface until the switch, so running pools
  see no change.
- A parity harness runs both engines over a fixed set of real files from
  the owner's library and compares the probed outputs: stream layout,
  codecs, colour tags, mastering metadata, chapters, attachments and
  duration, within tolerance.
- When it passes on every file class -- SDR H.264, HEVC 8-bit, HDR10,
  HLG, Dolby Vision 7 and 8.1, Hi10P, multi-audio with TrueHD and E-AC-3,
  PGS subtitles, fonts -- the default becomes `ffgo`. The argv engine and
  the removed profile fields are then deleted in their own commit.

### Testing

- **`Plan`:** table tests for every rule in §1; plan goldens replace the
  argv goldens.
- **`Engine`:** tests against the real FFmpeg 9 libraries on small
  generated clips, one per file class, on the CPU in CI.
- **GPU paths:** on any host with the hardware (the owner's workstation
  has the same RTX 2070 Max-Q as the cluster), skipping by name elsewhere,
  as `TestProbeDecodersOnRealNVDEC` does.
- **The fork:** ffgo's own suite, FFmpeg 9 and 4-7, in the fork's CI.
- **End to end:** e2e scenario 12 rewritten for the standard.
- Every fix falsified: reverted, and its test seen to fail by name.

## Go/no-go spike (first, before anything else)

The unknowns are §2 items 1 and 3 and one image question. The spike
answers them in the fork and a throwaway apko image, on the owner's RTX
2070 Max-Q:

1. ffgo decodes and encodes a clip on FFmpeg 9.0 shared libraries.
2. NVDEC → `scale_cuda` → `hevc_nvenc` runs in-process with frames kept
   on the GPU, and the CPU time matches what the NVDEC branch measured
   for the ffmpeg CLI (1.0 s for the 40 s 1080p clip, against 14.4 s).
3. In the apko image, the NVIDIA container toolkit's library setup works:
   it updates the library cache by running `ldconfig` in the container.
   Wolfi's glibc packages ship `ldconfig`, which may settle this outright;
   otherwise the worker finds the driver libraries through the toolkit's
   mount path and `LD_LIBRARY_PATH`.

If 1 or 2 cannot be made reliable in reasonable time, the work stops
there and falls back to the staged design (keep the `ffmpeg` executable,
apply §1, §4 and the images on it). Nothing else is lost by trying.

## Relation to other work

- `feat/nvenc-gpu-decode` (`8b74559e`): NVDEC decoding and
  `audio.copyCodecs` on today's argv engine. It ships independently and
  holds until the switch; its measured decoder capabilities and trial
  decodes carry over to §4, and its opt-in `copyCodecs` becomes §1's fixed
  audio rule.
- `docs/superpowers/specs/2026-09-30-backlog-windowing-and-encoder-limits-design.md`:
  the job window, encoder limits and their publication are unchanged.

## Out of scope

- New work on ffgo features the standard does not use (two-pass,
  streaming, capture); the fork only keeps them working on FFmpeg 9.
- Video codecs other than HEVC, or resolution changes.
- VideoToolbox, and non-Linux workers.
- Re-transcoding already-transcoded files to the new standard.

## Spike result (2026-09-30): go

Phase 0 ran on the owner's workstation (FFmpeg 9.0.1, RTX 2070 Max-Q,
driver 610.57.04) in the fork `mediactl/ffgo`, branch `clustarr/ffmpeg9`,
tag `v0.0.0-clustarr.1`
(`docs/superpowers/plans/2026-09-30-ffgo-phase0-spike.md`).

1. **ffgo on FFmpeg 9.0: yes, after five fixes.** Upstream could not load
   FFmpeg 9 on that host at all (its loader stopped at FFmpeg 7, picked
   4.4's libavutil and panicked). Now: one release's libraries loaded,
   newest first; struct offsets read from the shim built against the
   loaded headers (35 of 111 Go offsets differ on FFmpeg 9, among them
   `AVCodecContext.hw_frames_ctx`); key frames read from `AVFrame.flags`
   (FFmpeg 9 removed `key_frame`, and every frame read as a keyframe);
   pixel formats looked up by name; a shim used only with its own
   release. ffgo's whole suite passes on 9.0.1 (238 tests) and on BtbN's
   9.0.2 shared build; `ffmpeg9.yml` runs it in the fork's CI.
2. **NVDEC → `scale_cuda` → `hevc_nvenc` in-process: yes.** 40 s of 1080p
   H.264 (960 frames) became HEVC Main 10 with every frame, using 1.17 s
   of CPU (the ffmpeg CLI: 1.0 s on NVDEC, 14.4 s decoding in software).
   It took GPU frame pools on the filter graph's input and on the encoder,
   and `HWDecoder` draining at end of file (without it, 958 of the 960
   frames came out).
3. **The NVIDIA container toolkit in a distroless glibc image: works, no
   fallback needed.** On kind-cluster-plex's `nvidia` RuntimeClass, a
   3.5 MB apko image of Wolfi's glibc and loader alone (no shell), running
   as uid 65532 with every capability dropped, reached the GPU
   (`cuDeviceGetCount` = 1), with and without `ldconfig` in the image.
   libcuda printed `driverInitFileInfo ... result=11` to stderr there and
   still initialised; Phase 2's image self-check runs NVDEC and NVENC for
   real.

**The owner's NVENC settings (2026-09-30), replacing §1's "today's
defaults" for NVIDIA:** the archival encode
`-hwaccel cuda -hwaccel_output_format cuda`, `scale_cuda=format=p010le`,
`hevc_nvenc -preset p7 -rc constqp` with spatial and temporal AQ. Measured
on the RTX 2070: under `constqp` NVENC ignores `-cq` (cq 23 and cq 30 were
byte-identical), so the profile's `quality` maps to `-qp` (default 23);
AQ does change the output under constqp; and `-pix_fmt p010le` on GPU
frames fails (exit 218), so it is left off -- `scale_cuda` already makes
10-bit surfaces. Audio stays §1's Apple TV rule rather than the command's
`-c:a copy`. (An earlier message named preset p4 at CQ 24; the archival
command, sent after, supersedes it.)

**Decision: go.** Phases 1-5 of the plan's roadmap are planned next.

## Amendment (2026-10-01): images from `scratch`, not Wolfi

The owner replaced §5 "Images" during Phase 2: no Wolfi, apko or melange.
Each image is a multi-stage Docker build (`images/Dockerfile.transcoder-distroless`):

- FFmpeg 9.0 comes from BtbN's shared GPL build (the source today's image
  already uses: NVENC/NVDEC, CUDA filters, VAAPI and QSV), and the ffgo shim
  is compiled against its headers in the same build.
- `lddtree --copy-to-tree` stages exactly what the worker loads. The worker
  is a purego binary, so its dynamic linker and libc come with it; then the
  shim, the FFmpeg libraries and, until Phase 5, `ffmpeg`/`ffprobe`.
- What nothing's `DT_NEEDED` names, because it is `dlopen`ed, is traced
  explicitly: glibc's NSS plugins, and for Intel libva, libva-drm, the iHD
  driver and both QSV runtimes (Debian 12's packages, as today's image).
- The final stage is `FROM scratch`, with the CA bundle, `nsswitch.conf`,
  `passwd`/`group` for uid 1000, and `/data`, `/scratch`, `/tmp`. The
  `-debug` twins add a static busybox.

The image names, classes, self-check and CI gate of §5 are unchanged.
