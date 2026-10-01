# ADR-0015: There is no CUDA image; nvidia pools run the transcoder image and the NVIDIA runtime injects the driver

**Status:** Accepted, 2026-10-01

## Context

squasharr's pools ran NVIDIA transcodes in a dedicated image, chosen by
`--worker-image-cuda` (`CLUSTARR_WORKER_IMAGE_CUDA`, chart
`image.transcoderCuda`). Two generations of it existed:

- `transcoder-cuda` (`images/Dockerfile.transcoder`), the `transcoder`
  image rebuilt `FROM nvidia/cuda:12.8.1-base-ubuntu24.04`: 941 MB against
  `transcoder`'s 755 MB.
- `transcoder-cuda-distroless` (`images/Dockerfile.transcoder-distroless`),
  `FROM transcoder` plus two `ENV` lines: `NVIDIA_VISIBLE_DEVICES=all` and
  `NVIDIA_DRIVER_CAPABILITIES=video,compute,utility`.

Neither carried anything the encode used:

- **ffmpeg loads the driver, not the toolkit.** BtbN's FFmpeg builds reach
  NVENC, NVDEC and CUDA through `nv-codec-headers`' dynamic loader: they
  `dlopen` `libcuda.so.1`, `libnvidia-encode.so.1` and `libnvcuvid.so.1` at
  run time, and those are driver libraries. The CUDA filters
  (`scale_cuda`) are compiled with clang to PTX, so no `nvrtc`, `libnpp` or
  CUDA runtime is linked either. Nothing from the `nvidia/cuda` base was
  ever loaded.
- **The NVIDIA container runtime injects the driver.** On a pod with
  `runtimeClassName: nvidia`, the runtime bind-mounts the host driver's
  libraries for the capabilities `NVIDIA_DRIVER_CAPABILITIES` names
  (`compute` and `utility` by default; `video` adds NVENC and NVDEC). The
  pool's pod template already sets all three things it needs: the runtime
  class, an `nvidia.com/gpu` request, and
  `NVIDIA_DRIVER_CAPABILITIES=video,compute,utility`
  (`app/squash/controller/pool.applyHardware`). The image's own `ENV`
  duplicated the last.
- **It was proven on the owner's cluster.** The ffgo spike (2026-09-30,
  `docs/superpowers/specs/2026-09-30-ffgo-transcoding-design.md`) reached
  the GPU from a 3.5 MB image holding only glibc and its loader, and the
  live NVIDIA pool has encoded with NVDEC, `scale_cuda` and `hevc_nvenc` in
  `transcoder-cuda-distroless`, which holds no NVIDIA file (RTX 2070,
  driver 610.57.04, device plugin v0.20.1).

And the image's `NVIDIA_VISIBLE_DEVICES=all` was a hazard. The device
plugin sets that variable to the allocated GPU's id for a pod that requests
one, but a pod that runs the image under the `nvidia` runtime *without* a
GPU request would keep `all`, and see every GPU on the node behind the
scheduler's back.

The `nvidia/cuda` base also sets `NVIDIA_REQUIRE_CUDA`, which makes the
runtime refuse to start the container on a host whose driver is older than
the base's CUDA version, a failure that has nothing to do with what ffmpeg
needs.

## Decision

There is no CUDA image. Every pool, cpu, intel and nvidia, runs the one
image `--worker-image` names (`CLUSTARR_WORKER_IMAGE`, chart
`image.transcoder`). An nvidia pool gets the GPU and its libraries from its
pod spec alone: the runtime class, the GPU request and
`NVIDIA_DRIVER_CAPABILITIES=video,compute,utility`. No image sets
`NVIDIA_VISIBLE_DEVICES`.

Removed on 2026-10-01:

- the `transcoder-cuda` target (Debian) and the `transcoder-cuda` /
  `transcoder-cuda-debug` targets (distroless), and their published images;
- `--worker-image-cuda`, `CLUSTARR_WORKER_IMAGE_CUDA`, `pool.Config.ImageCUDA`
  and `squasharr.Options.WorkerImageCUDA`;
- the chart's `image.transcoderCuda`, which the values schema now rejects:
  a values file, or a release upgraded with `--reuse-values`, that still
  sets it fails with "at '/image': additional properties 'transcoderCuda'
  not allowed", loudly rather than silently ignoring a CUDA image choice.
  `--set image.transcoderCuda=null` does not remove a key from supplied
  values, so a release carrying it is upgraded once from its exported
  values with the key deleted (`helm get values <release> -o yaml`, delete
  `image.transcoderCuda`, `helm upgrade -f` that file, not
  `--reuse-values`);
- `make docker-build-cuda` and `TRANSCODER_CUDA_IMG`.

The image's `--self-check=cuda` still runs, now against `transcoder`
(`ci.yml`, `release.yml`, `make docker-selfcheck-distroless`), since that is
the image nvidia pools run.

`cmd/clustarr`'s `TestTranscoderImagesAreWhatSquasharrStampsOntoPools`
holds this: no Dockerfile under `images/` builds a `*cuda*` target or sets
`NVIDIA_VISIBLE_DEVICES`, and neither the chart nor `config/manager` names a
CUDA image. The pool render test holds the nvidia pod to the one image, the
`video` capability and no `NVIDIA_VISIBLE_DEVICES`.

## Consequences

- One image to build, publish and check for every class. A GPU node that
  runs cpu and nvidia pools no longer pulls a second, 941 MB Debian image
  (the distroless pair shared their layers, so they cost only the build).
- An nvidia pool's GPU access lives entirely in its pod spec, where the
  device plugin and the runtime class already govern it.
- **A CUDA library becomes necessary only with an ffmpeg linked against
  `libnpp` or `nvrtc`** (`scale_npp`, runtime-compiled filters), or another
  program that links the CUDA runtime. Reopen this ADR first; such a library
  belongs in the image as a library, still without
  `NVIDIA_VISIBLE_DEVICES`.
- A cluster without the NVIDIA runtime class, the device plugin, or the
  `video` capability fails at the pool's start, in its `--self-check`, not
  at image build. That was true of the CUDA image too.
- Plans written before this date (`docs/superpowers/plans/2026-09-23-*`,
  `2026-10-01-ffgo-phase2-images.md`, `2026-10-01-ffgo-phase3-engine.md`)
  name `transcoder-cuda` images; they are historical. A step that would
  build or deploy one now builds and deploys `transcoder` instead.
