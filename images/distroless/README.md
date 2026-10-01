# Distroless transcoder images

`images/Dockerfile.transcoder-distroless` builds squasharr's transcoder
images `FROM scratch` (spec `docs/superpowers/specs/2026-09-30-ffgo-transcoding-design.md`
§5 and its 2026-10-01 amendment).

| Target | Platforms | Class | Size (2026-10-01) | Today's Debian image |
| --- | --- | --- | --- | --- |
| `transcoder` | amd64, arm64 | `cpu`, `cuda` | 95 MB | `transcoder` 755 MB |
| `transcoder-intel` | amd64 | `intel` | 111 MB | (in `transcoder`, 755 MB) |
| `<target>-debug` | as its target | | +1 MB (static busybox) | |

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
  so it brings `ld-linux` and libc), the ffgo shim, every FFmpeg library,
  and `ffmpeg`/`ffprobe` (the argv engine's, until Phase 5).
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
make docker-build-distroless       # every target and its -debug twin
make docker-selfcheck-distroless   # --self-check=<class> in each, as the pool pods run it
```
