# The transcoder image

`images/Dockerfile.transcoder` builds squasharr's transcoder images
`FROM scratch` (spec `docs/superpowers/specs/2026-09-30-ffgo-transcoding-design.md`
§5 and its 2026-10-01 amendment). Until ffgo Phase 5 (2026-10-01) this file
was `Dockerfile.transcoder-distroless` and its image `transcoder-distroless`;
the Wolfi image then named `transcoder` (755 MB, with the `ffmpeg`
executable the argv engine ran) was deleted with that engine.

| Target | Platforms | Class | Size (2026-10-01) |
| --- | --- | --- | --- |
| `transcoder` | amd64, arm64 | `cpu`, `cuda`, `intel` (amd64) | about 118 MB gzipped on amd64 (with the Intel runtime; arm64 without it) |
| `transcoder-debug` | as `transcoder` | | +1 MB (static busybox) |

There is no CUDA image ([ADR-0015](../../docs/adr/0015-no-cuda-image.md)).
An nvidia pool runs `transcoder`: the NVIDIA container runtime injects the
driver's libraries (`libcuda`, `libnvidia-encode`, `libnvcuvid`), which
FFmpeg `dlopen`s, into a pod that sets `runtimeClassName: nvidia`, requests
`nvidia.com/gpu` and sets `NVIDIA_DRIVER_CAPABILITIES=video,compute,utility`,
as squasharr's pool template does. The `transcoder-cuda` target this table
listed until 2026-10-01 was `transcoder` plus that variable and
`NVIDIA_VISIBLE_DEVICES=all`; the second handed every GPU to a pod that ran
the image without requesting one. Do not add a CUDA target back without
reading the ADR.

What is in them, and why:

- `stage.sh` runs `lddtree --copy-to-tree` on the worker (a purego binary,
  so it brings `ld-linux` and libc), the ffgo shim and every FFmpeg
  library. There is no `ffmpeg` or `ffprobe` executable: the worker runs
  every transcode and probe in-process, and `stage.sh` fails the build if
  either is staged.
- `lddtree` cannot see what is `dlopen`ed, so that is named: glibc's NSS
  plugins (`libnss_dns`, `libnss_files`, `libresolv`) and, for Intel, the
  VAAPI driver and the QSV runtimes. The worker's own DNS uses Go's
  resolver and FFmpeg reads only local files, so the NSS plugins are there
  for correctness, not because anything uses them today.
- The CA bundle (the OTLP exporter's TLS), `nsswitch.conf`, `passwd` and
  `group` for uid 1000, and `/data`, `/scratch` and `/tmp`.
- FFmpeg's libraries and the shim live in `/usr/lib`, which the dynamic
  linker searches without an `ld.so.cache` (there is none in `scratch`).

```sh
make docker-build-transcoder       # transcoder and transcoder-debug
make docker-selfcheck-transcoder   # --self-check=<class> for each class, as the pool pods run it
```

Every pool, Intel's included, runs `transcoder` (ADR-0015). The Intel
runtime -- the iHD VAAPI driver and both QSV runtimes -- is in the image
on amd64, because nothing injects it at run time the way the NVIDIA
runtime injects NVIDIA's driver; the `transcoder-intel` target was folded
into `transcoder` on 2026-10-01 (ffgo Phase 4). `--self-check=intel
--trial` encodes on QSV and VAAPI with the image on a Comet Lake iGPU.
