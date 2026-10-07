# The native image

`images/Dockerfile.native` builds `ghcr.io/mediactl/clustarr/native` FROM
scratch (spec `docs/superpowers/specs/2026-10-06-manager-agent-split-design.md`
§10.1.3): the `agent`, `markers` and `transcode` binaries on FFmpeg 9's
libraries. It replaced the `transcoder` image (this file's
`Dockerfile.transcoder` until then) and the debian-based `media` image, which
carried `ffmpeg`, `ffprobe` and `par2` executables; nothing runs an external
media program any more (R2).

| Target | Platforms | Runs | Size |
| --- | --- | --- | --- |
| `native` | amd64, arm64 | every agent domain, the download engines, markers, the transcode pools (`cpu`, `cuda`, `intel` on amd64) and the graft Jobs | the transcoder's 118 MB gzipped on amd64 plus the agent, markers and ONNX Runtime |
| `native-debug` | as `native` | | +1 MB (static busybox) |

Both are per-architecture: `native` is `native-amd64` or `native-arm64` over a
shared `native-base`, and only amd64, which stages libva and Intel's iHD
driver, sets `LIBVA_*`. There is no ENTRYPOINT: every container names its
binary (`/usr/bin/agent`, `/usr/bin/markers`, `/usr/bin/transcode`).

There is no CUDA image ([ADR-0015](../../docs/adr/0015-no-cuda-image.md)).
An nvidia pool runs `native`: the NVIDIA container runtime injects the
driver's libraries (`libcuda`, `libnvidia-encode`, `libnvcuvid`), which
FFmpeg `dlopen`s, into a pod that sets `runtimeClassName: nvidia`, requests
`nvidia.com/gpu` and sets `NVIDIA_DRIVER_CAPABILITIES=video,compute,utility`,
as the manager's pool template does. Do not add a CUDA target back without
reading the ADR.

What is in it, and why:

- `stage.sh` runs `lddtree --copy-to-tree` on the three binaries (the agent
  is a cgo build, R13, so it brings glibc and libstdc++), the ffgo shim,
  ONNX Runtime and every FFmpeg library. It fails the build if `ffmpeg`,
  `ffprobe` or `par2` is staged.
- Only the seven FFmpeg libraries ffgo and its shim load (`libavutil`,
  `libavcodec`, `libavformat`, `libavfilter`, `libavdevice`, `libswscale`,
  `libswresample`) are copied from BtbN's build, and `stage.sh` refuses any
  other `libav*`, `libsw*` or `libpostproc*` (upgrade guide U3).
- `lddtree` cannot see what is `dlopen`ed, so that is named: glibc's NSS
  plugins and, on amd64, the VAAPI driver and the QSV runtimes.
- The CA bundle, `nsswitch.conf`, `passwd` and `group` for uid 1000, and
  `/data`, `/scratch` and `/tmp`.
- FFmpeg's libraries, the shim and ONNX Runtime live in `/usr/lib`, which the
  dynamic linker searches without an `ld.so.cache`; `FFGO_SHIM_DIR` and
  `ORT_LIB_PATH` point there.

```sh
make docker-build-native   # native and native-debug, with the pinned contexts (make contexts)
make docker-selfcheck      # hack/image-checks.sh, as CI and the release run it
```
