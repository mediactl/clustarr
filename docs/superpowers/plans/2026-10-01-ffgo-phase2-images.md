# ffgo Phase 2 (distroless transcoder images) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Revised 2026-10-01** at the owner's direction, mid-execution: no Wolfi, apko or melange. Each image is a multi-stage Docker build whose last stage is `FROM scratch`, holding exactly the files `lddtree` traces from what the worker loads, plus the few files glibc and the pod need. The first version of this plan (Wolfi packages and apko images) was approved and then replaced before any of it was committed.

**Goal:** Build squasharr's transcoder images from `scratch` -- `transcoder` (CPU, amd64 and arm64), `transcoder-intel` and `transcoder-cuda` (amd64), each with a `-debug` twin -- carrying FFmpeg 9.0's shared libraries with NVENC/NVDEC/CUDA filters, VAAPI and QSV, the ffgo shim built against the same headers, the Intel media runtime for the Intel image, and the worker; every image proves itself with `squasharr-worker --self-check` in CI, and on real NVIDIA and Intel hardware with `--self-check --trial`.

**Architecture:** One file, `images/Dockerfile.transcoder-distroless`, beside today's `images/Dockerfile.transcoder` (which keeps serving the live pools until Phase 3/4 switch them, and is deleted in Phase 5). Stages:
1. `build` -- `golang:1.27-bookworm`, `squasharr-worker` with `CGO_ENABLED=0`. purego's binaries are dynamically linked to glibc (purego's fake cgo), which is what lets them `dlopen`; `lddtree` on the worker therefore brings the dynamic linker and libc.
2. `ffmpeg` -- BtbN's FFmpeg 9.0 **shared** GPL build (the source today's image already uses; it carries NVENC/NVDEC, CUDA filters built with clang, VAAPI through libva stubs, the libvpl dispatcher, and its headers), with its libraries moved into the multiarch library directory, and the ffgo shim compiled against its headers.
3. `staging` (and `staging-intel`) -- `debian:bookworm-slim` with `pax-utils`; `lddtree --copy-to-tree /staging` on the worker, the shim, every FFmpeg library and the `ffmpeg`/`ffprobe` executables; then what nothing's `DT_NEEDED` names because it is `dlopen`ed: glibc's NSS plugins (`libnss_dns`, `libnss_files`, `libresolv`) and, for Intel, libva, libva-drm, the iHD driver and both QSV runtimes (and their own trees); plus the CA bundle, `nsswitch.conf`, `passwd`/`group` for uid 1000, and `/data`, `/scratch`, `/tmp`.
4. `transcoder`, `transcoder-intel`, `transcoder-cuda` -- `FROM scratch`, `COPY --from=staging /staging /`; the `-debug` targets add a static busybox.

**Tech Stack:** Docker 29.7 with BuildKit, `pax-utils` (`lddtree`), BtbN FFmpeg-Builds `n9.0` shared GPL (linux64 and linuxarm64), Debian 12's Intel media runtime (`intel-media-va-driver-non-free`, `libmfx-gen1.2`, `libmfx1`, `libva2`, `libva-drm2` -- the set today's image ships, verified on a Comet Lake iGPU), Go 1.27, the fork at `v0.0.0-clustarr.3` (Phase 1).

**Spec:** `docs/superpowers/specs/2026-09-30-ffgo-transcoding-design.md` (clustarr) §5 "Images" as amended 2026-10-01 (this method replaces apko/Wolfi), the Spike result item 3, §4 "The pod". Roadmap: `docs/superpowers/plans/2026-09-30-ffgo-phase0-spike.md` (Phase 2 row).

## Global Constraints

- Final stages are `FROM scratch`: no shell, no package manager; `-debug` adds a static busybox only.
- Contents are what `lddtree` traces plus the listed `dlopen`ed libraries and files; nothing is copied by hand that `lddtree` can trace.
- Until Phase 5 the images also carry `ffmpeg`/`ffprobe` (the argv engine runs on them); they are traced by `lddtree` like the rest.
- Runtime identity as today: uid/gid 1000 (`clustarr`), `/data` and `/scratch` owned by 1000, `USER 1000:1000`, entrypoint `/usr/bin/squasharr-worker`; `transcoder-cuda` sets `NVIDIA_VISIBLE_DEVICES=all` and `NVIDIA_DRIVER_CAPABILITIES=video,compute,utility`; every image sets `FFGO_SHIM_DIR` to the shim's directory and `LIBVA_MESSAGING_LEVEL=1`; Intel sets `LIBVA_DRIVERS_PATH` and `LIBVA_DRIVER_NAME=iHD`.
- FFmpeg 9.0 shared, sonames avutil 61 / avcodec 63 / avformat 63 / avfilter 12 / swscale 10 / swresample 7 / avdevice 63; the shim is compiled against the same tarball's headers in the same build. `FFMPEG_URL`/`FFMPEG_SHA256` build args pin a build, as today's Dockerfile does.
- The worker is `CGO_ENABLED=0`; no C in clustarr.
- Commits on clustarr `main` use a pathspec; never `git stash`; the controlling session pushes.
- `go.mod` changes are made once, serially, in Task 1.

## Review Focus

- **A library loaded at run time that `lddtree` cannot see** (the iHD driver, the QSV runtimes, libva-drm, NSS plugins): the self-check must open each class's devices' encoders by name and CI must fail naming the library. Pinned by Task 2/3's self-check on every image and Task 4's trial.
- **The dynamic linker cannot find a library** (no `ld.so.cache` in `scratch`): libraries must sit in a directory the linker searches by default (the multiarch directory) or be named by `LD_LIBRARY_PATH`; NVIDIA's injected driver libraries must be found too. Pinned by Task 4's trial on the cluster.
- **The shim built against other headers than the libraries shipped**: the self-check asserts the shim matched the release. Pinned by Task 1's test and every image's self-check.
- **Non-root, read-only root filesystem, every capability dropped** (the pool pods' securityContext): the trial writes only to `/scratch` or `$TMPDIR`. Pinned by Task 4's runs with `--read-only --cap-drop=ALL --user 1000:1000`.
- **arm64**: `transcoder` builds for arm64 with BtbN's linuxarm64 build and its self-check passes. Pinned by Task 5's matrix.

---

### Task 1: The fork in clustarr, and `--self-check`

(Unchanged from the first version.)

**Files:**
- Modify: `go.mod`, `go.sum` (require `github.com/obinnaokechukwu/ffgo`, replaced by `github.com/mediactl/ffgo v0.0.0-clustarr.3`)
- Create: `pkg/transcode/selfcheck/selfcheck.go`, `pkg/transcode/selfcheck/selfcheck_test.go`
- Modify: `cmd/squasharr-worker/main.go`, `cmd/squasharr-worker/main_test.go`

**Interfaces:**
- Produces:
  - `type Class string` (`ClassCPU = "cpu"`, `ClassCUDA = "cuda"`, `ClassIntel = "intel"`)
  - `type Report struct { FFmpegMajor int; ShimMatched bool; Encoders map[string]bool; Filters map[string]bool; Trials map[string]error }`
  - `func Check(ctx context.Context, class Class) (Report, error)` — ffgo `Init`, the loaded release must be 9, the shim must have matched, every encoder and filter the class needs must exist by name, and libx265 plus aac must open and encode a few frames on the CPU; the error names the first missing piece
  - `func Trial(ctx context.Context, class Class, dir string) (Report, error)` — Check, then a real GPU encode for the class (Task 4 fills the GPU legs)
  - `var Needs = map[Class]struct{ Encoders, Filters []string }{ ClassCPU: {{"libx265", "aac"}, nil}, ClassCUDA: {{"libx265", "aac", "hevc_nvenc"}, {"scale_cuda", "hwupload"}}, ClassIntel: {{"libx265", "aac", "hevc_qsv", "hevc_vaapi"}, {"vpp_qsv", "scale_vaapi", "hwupload"}} }`
  - worker flags `--self-check=<class>` (prints the report as JSON on stdout, exits 0 on success, `worker.WorkerExitMisconfigured` otherwise) and `--trial` (with `--self-check`, runs `Trial` in `--scratch-dir`, default `$TMPDIR`); neither needs `NATS_URL` or the pool env

- [ ] **Step 1: Pin the fork** — `go mod edit -require=github.com/obinnaokechukwu/ffgo@v0.0.0 -replace=github.com/obinnaokechukwu/ffgo=github.com/mediactl/ffgo@v0.0.0-clustarr.3`, `go mod tidy`; Expected: one require and one replace line; `make build` succeeds.

- [ ] **Step 2: Write the failing tests** — `TestCheckPassesOnThisHostsFFmpeg9ForCPU` (FFmpegMajor 9, ShimMatched, libx265 and aac true; skips without FFmpeg 9 libraries), `TestCheckNamesTheFirstMissingEncoder` (a `Needs` entry naming `no_such_encoder` → the error names it), `TestCheckFailsWhenTheShimDoesNotMatch` (re-exec with `FFGO_SHIM_DIR` at an empty dir → an error naming the shim), and in `cmd/squasharr-worker` `TestSelfCheckNeedsNoClusterEnvironment` (`run([]string{"--self-check=cpu"}, empty env) == 0`).

- [ ] **Step 3: Run them to verify they fail** — `go test -count=1 ./pkg/transcode/selfcheck/ ./cmd/squasharr-worker/`; Expected: FAIL to compile (`Check` undefined; `--self-check` unknown flag).

- [ ] **Step 4: Implement** — `Check` as specified; `main.go` handles `--self-check` before any environment check and prints `json.MarshalIndent(report)`.

- [ ] **Step 5: Run, falsify, commit** — PASS; falsify the shim check; `git commit -m "feat(squasharr): the worker links the ffgo fork and checks itself ..." -- go.mod go.sum pkg/transcode/selfcheck cmd/squasharr-worker`.

---

### Task 2: `Dockerfile.transcoder-distroless`: `transcoder` and `transcoder-cuda`

**Files:**
- Create: `images/Dockerfile.transcoder-distroless`
- Modify: `Makefile` (`docker-build-distroless`, `docker-selfcheck-distroless`), `images/Dockerfile.transcoder` (header: its successor and its end in Phase 5)

**Interfaces:**
- Produces: targets `transcoder`, `transcoder-debug`, `transcoder-cuda`, `transcoder-cuda-debug`; `make docker-build-distroless` builds them as `ghcr.io/mediactl/clustarr/<target>:dev`; `make docker-selfcheck-distroless` runs `--self-check=<class>` in each under `--read-only --cap-drop=ALL --user 1000:1000` and fails on a non-zero exit.

- [ ] **Step 1: Write the Dockerfile**

```dockerfile
# syntax=docker/dockerfile:1.7
ARG GO_VERSION=1.27
ARG FFMPEG_BRANCH=9.0

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-bookworm AS build
ARG TARGETOS TARGETARCH VERSION=dev
WORKDIR /src
ENV CGO_ENABLED=0 GOFLAGS=-trimpath
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
      -ldflags "-s -w -X github.com/mediactl/clustarr/pkg/version.Version=${VERSION}" \
      -o /out/squasharr-worker ./cmd/squasharr-worker \
 && mkdir -p /out/ffgo && cp -r "$(go list -m -f '{{.Dir}}' github.com/obinnaokechukwu/ffgo)/shim" /out/ffgo/

FROM debian:bookworm-slim AS ffmpeg
ARG TARGETARCH FFMPEG_BRANCH FFMPEG_URL="" FFMPEG_SHA256=""
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl xz-utils gcc libc6-dev \
 && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/ffgo/shim /src/shim
RUN set -eux; \
    case "${TARGETARCH}" in amd64) a=linux64 ;; arm64) a=linuxarm64 ;; *) exit 1 ;; esac; \
    url="${FFMPEG_URL:-https://github.com/BtbN/FFmpeg-Builds/releases/download/latest/ffmpeg-n${FFMPEG_BRANCH}-latest-${a}-gpl-shared-${FFMPEG_BRANCH}.tar.xz}"; \
    curl -fsSL -o /tmp/ff.tar.xz "$url"; \
    if [ -n "${FFMPEG_SHA256}" ]; then echo "${FFMPEG_SHA256}  /tmp/ff.tar.xz" | sha256sum -c -; fi; \
    mkdir -p /opt/ffmpeg && tar -xJf /tmp/ff.tar.xz --strip-components=1 -C /opt/ffmpeg; \
    lib=/usr/lib/$(gcc -print-multiarch); \
    mkdir -p /out$lib /out/usr/bin; \
    cp -a /opt/ffmpeg/lib/lib*.so* /out$lib/; \
    install -m0755 /opt/ffmpeg/bin/ffmpeg /opt/ffmpeg/bin/ffprobe /out/usr/bin/; \
    gcc -shared -fPIC -O2 -DFFSHIM_HAVE_AVDEVICE=1 -DFFSHIM_HAVE_AVFILTER=1 -I/opt/ffmpeg/include \
      -o /out$lib/libffshim.so /src/shim/ffshim.c \
      -L/opt/ffmpeg/lib -lavutil -lavcodec -lavformat -lavdevice -lavfilter

FROM debian:bookworm-slim AS staging
RUN apt-get update && apt-get install -y --no-install-recommends pax-utils ca-certificates \
 && rm -rf /var/lib/apt/lists/*
COPY --from=ffmpeg /out/ /
COPY --from=build /out/squasharr-worker /usr/bin/squasharr-worker
COPY images/distroless/stage.sh /stage.sh
RUN /stage.sh /staging

FROM scratch AS transcoder
COPY --from=staging /staging/ /
ENV FFGO_SHIM_DIR=/usr/lib/x86_64-linux-gnu LIBVA_MESSAGING_LEVEL=1
USER 1000:1000
VOLUME ["/data"]
ENTRYPOINT ["/usr/bin/squasharr-worker"]
```

(`FFGO_SHIM_DIR` uses the multiarch directory the stage script reports; the arm64 build sets it to `/usr/lib/aarch64-linux-gnu` through an `ARG`-derived `ENV`.) `transcoder-cuda` is `FROM transcoder` plus the two NVIDIA variables. Each `-debug` target is `FROM <base>` plus `COPY --from=busybox:1.37-musl /bin/ /bin/`.

`images/distroless/stage.sh` (POSIX sh, run in the staging stage):

```sh
#!/bin/sh
# Copies into $1 exactly what the worker loads: lddtree traces each
# DT_NEEDED tree (the worker itself brings ld-linux and libc), and the
# files glibc dlopens or reads at run time are listed explicitly.
set -eu
dst=$1; lib=/usr/lib/$(dpkg-architecture -qDEB_HOST_MULTIARCH 2>/dev/null || uname -m)-linux-gnu
trace() { for f in "$@"; do lddtree --copy-to-tree "$dst" "$f" >/dev/null; done; }
trace /usr/bin/squasharr-worker /usr/bin/ffmpeg /usr/bin/ffprobe "$lib"/libffshim.so "$lib"/libav*.so.* "$lib"/libsw*.so.* "$lib"/libpostproc*.so.* 2>/dev/null || true
trace "$lib"/libnss_dns.so.2 "$lib"/libnss_files.so.2 "$lib"/libresolv.so.2
for x in ${EXTRA_DLOPEN:-}; do trace $x; done
mkdir -p "$dst/etc/ssl/certs" "$dst/data" "$dst/scratch" "$dst/tmp"
cp /etc/ssl/certs/ca-certificates.crt "$dst/etc/ssl/certs/"
echo 'hosts: files dns' > "$dst/etc/nsswitch.conf"
printf 'root:x:0:0:root:/root:/sbin/nologin\nclustarr:x:1000:1000::/nonexistent:/sbin/nologin\n' > "$dst/etc/passwd"
printf 'root:x:0:\nclustarr:x:1000:\n' > "$dst/etc/group"
chown 1000:1000 "$dst/data" "$dst/scratch"; chmod 1777 "$dst/tmp"
```

(The `|| true` is not kept: Step 2 builds without it once the exact library globs are known from the tarball, so a missing library fails the build.)

- [ ] **Step 2: Build and self-check**

Run: `make docker-build-distroless && make docker-selfcheck-distroless`
Expected: four images build; each self-check prints `"FFmpegMajor": 9`, `"ShimMatched": true`, every encoder and filter of its class `true`, exits 0; `docker run --rm --entrypoint /bin/sh <non-debug image>` fails (no shell), the `-debug` one works. Record sizes in `images/distroless/README.md` beside today's image sizes.

- [ ] **Step 3: Commit** — `git commit -m "build(images): distroless transcoder and transcoder-cuda from scratch -- lddtree traces what the worker loads ..." -- images/Dockerfile.transcoder-distroless images/distroless Makefile images/Dockerfile.transcoder`

---

### Task 3: `transcoder-intel`

**Files:**
- Modify: `images/Dockerfile.transcoder-distroless` (stages `staging-intel`, targets `transcoder-intel`, `transcoder-intel-debug`), `Makefile`

**Interfaces:**
- Produces: `transcoder-intel` (amd64): `staging-intel` is `staging` with Debian 12's Intel runtime installed (`intel-media-va-driver-non-free` from non-free, `libmfx-gen1.2`, `libmfx1`, `libva2`, `libva-drm2`), traced with `EXTRA_DLOPEN` = the iHD driver, `libva.so.2`, `libva-drm.so.2`, `libmfx-gen.so.1.2`, `libmfxhw64.so.1` and the `mfx` plugin directory's libraries; `LIBVA_DRIVERS_PATH=/usr/lib/x86_64-linux-gnu/dri`, `LIBVA_DRIVER_NAME=iHD`.

- [ ] **Step 1: Write the stage and target** (the non-free source line and its `grep` guard as today's Dockerfile does).
- [ ] **Step 2: Build and self-check** — `make docker-build-distroless docker-selfcheck-distroless`; Expected: `transcoder-intel` reports `hevc_qsv`, `hevc_vaapi`, `vpp_qsv`, `scale_vaapi` true; `ls` in its `-debug` twin shows `iHD_drv_video.so` and both runtimes under the multiarch directory.
- [ ] **Step 3: Commit.**

---

### Task 4: Real hardware -- `--trial` on NVIDIA and Intel

(As the first version's Task 5.)

**Files:**
- Modify: `pkg/transcode/selfcheck/selfcheck.go`; Create: `pkg/transcode/selfcheck/trial_test.go`

**Interfaces:**
- Produces: `Trial` legs — `cuda`: a 2 s 256x144 H.264 clip made in-process (libx264, `VideoStreamEncoder`, into `dir`), decoded on NVDEC (`StreamDecoder` with a CUDA device), `scale_cuda=format=p010le`, `hevc_nvenc` with the owner's archival settings (preset p7, rc constqp, qp 23, spatial and temporal AQ); and CPU decode → `hwupload,scale_cuda=format=p010le` → `hevc_nvenc`. `intel`: CPU decode → `hwupload,scale_vaapi=format=p010` → `hevc_vaapi` on `/dev/dri/renderD128`, and → `hwupload=extra_hw_frames=64,vpp_qsv=format=p010` → `hevc_qsv` on a QSV device. Each leg records `Trials["<leg>"]`; `Trial` fails if every leg of the class failed. Packets counted equal the input frames.

- [ ] **Step 1: Failing tests** — `TestTrialEncodesOnNVIDIA` (skips without CUDA; both CUDA legs nil), `TestTrialEncodesOnIntel` (skips without a render node or a VAAPI driver on the host).
- [ ] **Step 2: Run** — FAIL on the workstation (CUDA legs missing).
- [ ] **Step 3: Implement the legs** from the fork's primitives only, writing nothing outside `dir`.
- [ ] **Step 4: Real hardware** — workstation `go test` PASS for NVIDIA; Intel in the image: `docker run --rm --read-only --cap-drop=ALL --user 1000:$(stat -c %g /dev/dri/renderD128) --device /dev/dri/renderD128 --tmpfs /scratch:uid=1000 ghcr.io/mediactl/clustarr/transcoder-intel:dev --self-check=intel --trial --scratch-dir=/scratch` (a failing leg is ledgered with its reason, not stalled on); NVIDIA on kind-cluster-plex: `kind load docker-image` of only this image, a one-shot pod with `runtimeClassName: nvidia`, `NVIDIA_VISIBLE_DEVICES=all`, no `nvidia.com/gpu` request, the pool's securityContext, an emptyDir at `/scratch`, args `--self-check=cuda --trial --scratch-dir=/scratch`; Expected: both CUDA legs nil; the pod deleted; nothing else on the cluster changes; downloads stay paused.
- [ ] **Step 5: Commit.**

---

### Task 5: CI and publishing

**Files:**
- Modify: `.github/workflows/release.yml` (matrix entries for the six new targets: `transcoder-distroless` amd64+arm64 → `ghcr.io/<repo>/transcoder-distroless`, `transcoder-intel` amd64, `transcoder-cuda-distroless` amd64, and their `-debug` twins), `.github/workflows/ci.yml` (a job building each target with `load: true` and running its self-check under `--read-only --cap-drop=ALL --user 1000:1000`), `images/distroless/README.md`

**Interfaces:**
- Produces: on pull requests the images build and self-check; on `main` and `v*` tags they publish with `release.yml`'s version scheme. The names carry `-distroless` until Phase 3/4 switch the pools, so today's `transcoder`/`transcoder-cuda` tags are untouched.

- [ ] **Step 1: Edit the workflows** (actions pinned by commit SHA as `release.yml` pins them).
- [ ] **Step 2: Push and watch** — `gh run watch` on the CI and release runs; every job green; `docker manifest inspect` on the multi-arch `transcoder-distroless` lists amd64 and arm64. A red job is fixed here and the run repeated.
- [ ] **Step 3: Commit and push.**
