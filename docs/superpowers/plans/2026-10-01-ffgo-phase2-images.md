# ffgo Phase 2 (distroless transcoder images) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build squasharr's transcoder images as distroless glibc images from Wolfi packages with apko — `transcoder` (CPU, amd64 and arm64), `transcoder-intel` and `transcoder-cuda` (amd64), each with a `-debug` twin — carrying our own melange-built FFmpeg 9.0 with NVENC/NVDEC/CUDA filters, VAAPI and QSV, the Intel media stack, the ffgo shim, and the worker; every image proves itself with `squasharr-worker --self-check` in CI, and on real NVIDIA and Intel hardware with `--self-check --trial`.

**Architecture:** Recipes live in clustarr under `images/wolfi/`: melange recipes in `images/wolfi/packages/`, apko configs in `images/wolfi/images/`, a local build script `hack/wolfi/build.sh` behind `make wolfi-packages`/`make wolfi-images`, and a CI workflow `wolfi-images.yml`. Packages are built into a local signed repository (`images/wolfi/out/`, gitignored) and the images take Wolfi's own packages plus ours from it. The worker gains `--self-check[=cpu|cuda|intel]` and `--trial`, implemented in `pkg/transcode/selfcheck` on the fork's Phase 1 primitives, so Phase 4's pod report reuses the same trial. The images do not replace today's Dockerfile images yet: the controller keeps its image flags until Phase 3/4 switch the pools (the argv engine needs the `ffmpeg`/`ffprobe` executables, which the new images also carry until Phase 5).

**Tech Stack:** melange and apko (installed at `/usr/bin` on the workstation), Docker 29.7 (the melange runner and image loading), Wolfi `os` repository (glibc 2.43, clang 19-23, libva 2.24, x265 4.2), FFmpeg 9.0.2 source, nv-codec-headers, Intel gmmlib / media-driver / libvpl / vpl-gpu-rt / MediaSDK runtime, Go 1.27 (`CGO_ENABLED=0`), the fork at `v0.0.0-clustarr.3` (Phase 1).

**Spec:** `docs/superpowers/specs/2026-09-30-ffgo-transcoding-design.md` (clustarr) §5 "Images", the Spike result item 3, §4 "The pod". Roadmap: `docs/superpowers/plans/2026-09-30-ffgo-phase0-spike.md` (Phase 2 row).

## Global Constraints

- Images are assembled with apko from Wolfi packages plus our melange packages; Chainguard's `cgr.dev` images are not used (spec §5).
- No shell in the non-debug images; `-debug` adds `busybox` only.
- Contents: glibc, `ca-certificates-bundle`, `ffmpeg-clustarr` libraries (and, until Phase 5, its `ffmpeg`/`ffprobe` executables), the ffgo shim, `squasharr-worker`; `transcoder-intel` adds the Intel media stack; `transcoder-cuda` adds nothing (the NVIDIA toolkit injects the driver).
- Runtime identity as today: uid/gid 1000 (`clustarr`), `/data` and `/scratch` owned by 1000, `USER 1000:1000`, entrypoint `squasharr-worker`; `transcoder-cuda` sets `NVIDIA_VISIBLE_DEVICES=all`, `NVIDIA_DRIVER_CAPABILITIES=video,compute,utility`; every image sets `LIBVA_MESSAGING_LEVEL=1` and `FFGO_SHIM_DIR=/usr/lib`.
- The FFmpeg build is GPL (libx265, libx264) shared, version 9.0.2, sonames avutil 61 / avcodec 63 / avformat 63 / avfilter 12 / swscale 10 / swresample 7 / avdevice 63; the shim is built against the same headers in the same melange build.
- Pure Go: the worker is `CGO_ENABLED=0`; no C in clustarr.
- Intel's media driver is built with its non-free kernels (upstream's default), since the free build has no HEVC 10-bit encode on Kaby Lake through Comet Lake (today's image comment); both QSV runtimes ship (vpl-gpu-rt for Gen12+, MediaSDK's `libmfxhw64` for Broadwell..Ice Lake), as today's image does.
- Commits on clustarr `main` use a pathspec (`git commit -m ... -- <paths>`); never `git stash`; the controlling session pushes.
- `go.mod` changes are made once, serially, in Task 1.

## Review Focus

- **A library the worker loads at run time is missing from the image** (libavdevice, which the Phase 0 shim links; libdrm; libva-drm; libstdc++ for x265): `--self-check` must load every library through ffgo and fail CI naming the library. Pinned by Task 4's self-check on every image.
- **The shim built against other headers than the libraries shipped**: the self-check asserts the shim loaded and matched the release (`layout.ShimLoaded()`), else fails. Pinned by Task 1's self-check test with a mismatched `FFGO_SHIM_DIR`.
- **An encoder present by name but not openable on the device** (hevc_qsv with no runtime for the GPU generation, hevc_nvenc with a driver too old for the SDK headers): the trial encodes for real and reports which step failed. Pinned by Task 5 on the RTX 2070 (cluster) and the workstation's Intel iGPU.
- **Non-root with every capability dropped and a read-only root filesystem** (the pool pods' securityContext): the trial writes only to `/scratch` or `$TMPDIR`. Pinned by Task 5's runs with `--read-only --cap-drop=ALL --user 1000:1000`.
- **arm64**: `transcoder` builds for aarch64 without the CUDA and Intel options and its self-check passes under emulation or on an arm64 runner. Pinned by Task 6's matrix.

---

### Task 1: The fork in clustarr, and `--self-check`

**Files:**
- Modify: `go.mod`, `go.sum` (require `github.com/obinnaokechukwu/ffgo`, replaced by `github.com/mediactl/ffgo v0.0.0-clustarr.3`)
- Create: `pkg/transcode/selfcheck/selfcheck.go`, `pkg/transcode/selfcheck/selfcheck_test.go`
- Modify: `cmd/squasharr-worker/main.go`, `cmd/squasharr-worker/main_test.go`

**Interfaces:**
- Produces:
  - `type Class string` (`ClassCPU = "cpu"`, `ClassCUDA = "cuda"`, `ClassIntel = "intel"`)
  - `type Report struct { FFmpegMajor int; ShimMatched bool; Encoders map[string]bool; Filters map[string]bool; Trials map[string]error }`
  - `func Check(ctx context.Context, class Class) (Report, error)` — ffgo `Init`, the loaded release must be 9, the shim must have matched, every encoder and filter the class needs must exist by name, and libx265 plus aac must open and encode 10 frames on the CPU; the error names the first missing piece
  - `func Trial(ctx context.Context, class Class, dir string) (Report, error)` — Check, then a real GPU encode for the class (Task 5 fills the GPU legs; here it is CPU-only and returns Check's report for `cuda`/`intel`)
  - `var Needs = map[Class]struct{ Encoders, Filters []string }{ ClassCPU: {{"libx265", "aac"}, nil}, ClassCUDA: {{"libx265", "aac", "hevc_nvenc"}, {"scale_cuda", "hwupload"}}, ClassIntel: {{"libx265", "aac", "hevc_qsv", "hevc_vaapi"}, {"vpp_qsv", "scale_vaapi", "hwupload"}} }`
  - worker flags `--self-check=<class>` (prints the report as JSON on stdout, exits 0 on success, `worker.WorkerExitMisconfigured` otherwise) and `--trial` (with `--self-check`, runs `Trial` in `--scratch-dir`, default `$TMPDIR`); neither needs `NATS_URL` or the pool env

- [ ] **Step 1: Pin the fork**

```bash
go mod edit -require=github.com/obinnaokechukwu/ffgo@v0.0.0 -replace=github.com/obinnaokechukwu/ffgo=github.com/mediactl/ffgo@v0.0.0-clustarr.3
go mod tidy
grep -n ffgo go.mod
```
Expected: one `require` and one `replace` line naming `v0.0.0-clustarr.3`; `go build ./...` (through `make build`) succeeds.

- [ ] **Step 2: Write the failing tests**

```go
// pkg/transcode/selfcheck/selfcheck_test.go
func TestCheckPassesOnThisHostsFFmpeg9ForCPU(t *testing.T) {
	r, err := Check(context.Background(), ClassCPU)
	if errors.Is(err, ffgo.ErrFFmpegNotFound) { // the fork's sentinel for no libraries; use its real name
		t.Skip("no FFmpeg 9 libraries on this host")
	}
	require.NoError(t, err)
	assert.Equal(t, 9, r.FFmpegMajor)
	assert.True(t, r.ShimMatched)
	assert.True(t, r.Encoders["libx265"])
	assert.True(t, r.Encoders["aac"])
}

func TestCheckNamesTheFirstMissingEncoder(t *testing.T) {
	old := Needs[ClassCPU]
	t.Cleanup(func() { Needs[ClassCPU] = old })
	Needs[ClassCPU] = struct{ Encoders, Filters []string }{[]string{"libx265", "no_such_encoder"}, nil}
	_, err := Check(context.Background(), ClassCPU)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no_such_encoder")
}

func TestCheckFailsWhenTheShimDoesNotMatch(t *testing.T) {
	// Re-exec this test binary with FFGO_SHIM_DIR pointing at an empty dir:
	// no shim, so FFmpeg 9 must be refused by name (Phase 0's Init rule).
	if os.Getenv("SELFCHECK_CHILD") == "1" {
		_, err := Check(context.Background(), ClassCPU)
		if err == nil {
			os.Exit(0)
		}
		fmt.Println(err)
		os.Exit(3)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestCheckFailsWhenTheShimDoesNotMatch$")
	cmd.Env = append(os.Environ(), "SELFCHECK_CHILD=1", "FFGO_SHIM_DIR="+t.TempDir())
	out, err := cmd.CombinedOutput()
	require.Error(t, err, string(out))
	assert.Contains(t, string(out), "shim")
}
```

```go
// cmd/squasharr-worker/main_test.go (added)
func TestSelfCheckNeedsNoClusterEnvironment(t *testing.T) {
	code := run([]string{"--self-check=cpu"}, func(string) string { return "" })
	if code == worker.WorkerExitMisconfigured && !ffmpeg9Present() {
		t.Skip("no FFmpeg 9 libraries on this host")
	}
	assert.Equal(t, 0, code)
}
```

- [ ] **Step 3: Run them to verify they fail**

Run: `go test -count=1 ./pkg/transcode/selfcheck/ ./cmd/squasharr-worker/`
Expected: FAIL to compile — package `selfcheck` has no `Check`; `run` rejects `--self-check` ("unknown flag").

- [ ] **Step 4: Implement**

`selfcheck.go`: `Check` calls `ffgo.Init()`; reads `bindings.LoadedVersionSet()` through the fork's exported accessor (add one to the fork in Phase 1 Task 7's README pass if it is not exported; if it is missing at this point, rule and read `avutil_version() >> 16` through `ffgo.AVUtilVersion()`), requires major 61 (FFmpeg 9); `ShimMatched` from the fork's exported `layout.ShimLoaded()` equivalent; looks up each encoder with `avcodec.FindEncoderByName` and each filter with `avfilter.GetByName`; opens libx265 (`VideoStreamEncoder`, 64x64 yuv420p10le, 10 gray frames) and aac (`AudioEncoder`, stereo, 2048 silent samples), discarding packets. `Trial` = `Check` for now.

`main.go`: two flags; when `--self-check` is set, run `selfcheck.Check`/`Trial` before any env check, print `json.MarshalIndent(report)`, return 0 or `WorkerExitMisconfigured` with the error on stderr.

- [ ] **Step 5: Run them to verify they pass, falsify, commit**

Run: `go test -count=1 ./pkg/transcode/selfcheck/ ./cmd/squasharr-worker/`
Expected: PASS. Falsify: skip the shim check in `Check` → `TestCheckFailsWhenTheShimDoesNotMatch` still fails by name only if Init refuses; if Init already refuses, the test proves the image property either way — ledger which. Then:

```bash
git commit -m "feat(squasharr): the worker links the ffgo fork and checks itself -- --self-check loads FFmpeg 9 and the shim, finds each encoder and filter its class needs, and encodes libx265 and AAC" -- go.mod go.sum pkg/transcode/selfcheck cmd/squasharr-worker
```

---

### Task 2: melange packages for the GPU stacks

**Files:**
- Create: `images/wolfi/packages/nv-codec-headers.yaml`, `libvpl.yaml`, `intel-gmmlib.yaml`, `intel-media-driver.yaml`, `intel-vpl-gpu-rt.yaml`, `intel-mediasdk-runtime.yaml`
- Create: `hack/wolfi/build.sh`, `images/wolfi/README.md`
- Modify: `Makefile` (`wolfi-packages`), `.gitignore` (`images/wolfi/out/`, `images/wolfi/*.rsa`)

**Interfaces:**
- Produces: signed APKs in `images/wolfi/out/<arch>/` for `nv-codec-headers`, `libvpl`, `libvpl-dev`, `intel-gmmlib`, `intel-gmmlib-dev`, `intel-media-driver`, `intel-vpl-gpu-rt`, `intel-mediasdk-runtime`; `hack/wolfi/build.sh packages [name...]` builds them in dependency order with `melange build --runner docker --arch x86_64 --signing-key images/wolfi/melange.rsa --repository-append images/wolfi/out --keyring-append images/wolfi/melange.rsa.pub --out-dir images/wolfi/out`, generating the key with `melange keygen` if absent

- [ ] **Step 1: Pin each source**

For each upstream, take the newest release tag and its commit, and write both into the recipe's `git-checkout` (`tag:` and `expected-commit:`), as Wolfi's recipes do:

```bash
git ls-remote --tags https://github.com/FFmpeg/nv-codec-headers 'n13.*' | tail -3
git ls-remote --tags https://github.com/intel/libvpl 'v2.*' | tail -3
git ls-remote --tags https://github.com/intel/gmmlib 'intel-gmmlib-22.*' | tail -3
git ls-remote --tags https://github.com/intel/media-driver 'intel-media-25.*' | tail -3
git ls-remote --tags https://github.com/intel/vpl-gpu-rt 'intel-onevpl-25.*' | tail -3
git ls-remote --tags https://github.com/Intel-Media-SDK/MediaSDK 'intel-mediasdk-23.*' | tail -3
```
Expected: one tag and commit each; nv-codec-headers' major must be one FFmpeg 9.0.2's configure accepts (`grep -n ffnvcodec configure` in the FFmpeg tree: "ffnvcodec >= 12.0.16.1" or newer) and the driver on the RTX 2070 hosts (610.57.04) must meet its minimum (the headers' README lists the minimum driver per SDK).

- [ ] **Step 2: Write the recipes**

`nv-codec-headers.yaml` (header-only):

```yaml
package:
  name: nv-codec-headers
  version: "<tag without n>"
  epoch: 0
  description: FFmpeg's NVIDIA codec headers (ffnvcodec)
  copyright:
    - license: MIT
environment:
  contents:
    packages: [build-base, busybox, wolfi-baselayout]
pipeline:
  - uses: git-checkout
    with:
      repository: https://github.com/FFmpeg/nv-codec-headers
      tag: n${{package.version}}
      expected-commit: <commit>
  - runs: make PREFIX=/usr DESTDIR=${{targets.destdir}} install
test:
  pipeline:
    - runs: test -f /usr/include/ffnvcodec/nvEncodeAPI.h && test -f /usr/lib/pkgconfig/ffnvcodec.pc
```

`libvpl.yaml`, `intel-gmmlib.yaml`, `intel-vpl-gpu-rt.yaml`, `intel-media-driver.yaml`, `intel-mediasdk-runtime.yaml`: the same shape with `uses: cmake/configure`, `cmake/build`, `cmake/install` (Wolfi's pipelines), `-dev` subpackages via `split/dev`, and these specifics:
- `intel-media-driver`: build-deps `intel-gmmlib-dev`, `libva-dev`, `libdrm-dev`, `libpciaccess-dev`; cmake `-DINSTALL_DRIVER_SYSCONF=OFF -DENABLE_NONFREE_KERNELS=ON -DMEDIA_BUILD_FATAL_WARNINGS=OFF`; installs `/usr/lib/dri/iHD_drv_video.so`; test: `test -f /usr/lib/dri/iHD_drv_video.so`.
- `intel-vpl-gpu-rt`: build-deps `libva-dev`, `libdrm-dev`; installs `libmfx-gen.so.1.2*`.
- `intel-mediasdk-runtime`: cmake `-DBUILD_SAMPLES=OFF -DBUILD_TUTORIALS=OFF -DENABLE_OPENCL=OFF -DBUILD_DISPATCHER=OFF`; package only `libmfxhw64.so.1*` and its plugin dir (the dispatcher comes from libvpl).
- `libvpl`: dispatcher `libvpl.so.2`, `-dev` with `vpl.pc`.

`hack/wolfi/build.sh`: `set -euo pipefail`; key generation; an ordered list (`nv-codec-headers libvpl intel-gmmlib intel-vpl-gpu-rt intel-mediasdk-runtime intel-media-driver`, later `ffmpeg-clustarr squasharr-worker`); builds each name given (all by default) and skips a package whose APK for the recipe's version and epoch is already in `out/` unless `FORCE=1`.

- [ ] **Step 3: Build and test them**

Run: `make wolfi-packages PACKAGES="nv-codec-headers libvpl intel-gmmlib intel-vpl-gpu-rt intel-mediasdk-runtime intel-media-driver" 2>&1 | tail -20`
Expected: each package's melange `test:` passes; `ls images/wolfi/out/x86_64/*.apk` lists the eight APKs. A build failure is fixed in the recipe (missing build-dep, cmake option) and the rebuild must pass; every recipe change that is not a version pin gets a comment saying why.

- [ ] **Step 4: Commit**

```bash
git commit -m "build(images): melange recipes for the GPU stacks -- nv-codec-headers, libvpl, Intel gmmlib, media driver (non-free kernels), vpl-gpu-rt and the MediaSDK runtime" -- images/wolfi hack/wolfi Makefile .gitignore
```

---

### Task 3: `ffmpeg-clustarr` and the ffgo shim

**Files:**
- Create: `images/wolfi/packages/ffmpeg-clustarr.yaml`

**Interfaces:**
- Consumes: Task 2's packages; Wolfi's `ffmpeg-9.0` recipe (copied, with its configure flags kept).
- Produces: `ffmpeg-clustarr` (the `ffmpeg`/`ffprobe` executables), `ffmpeg-clustarr-libs` (the seven shared libraries), `ffmpeg-clustarr-dev`, `ffmpeg-clustarr-ffgo-shim` (`/usr/lib/libffshim.so` built from `github.com/mediactl/ffgo` at `v0.0.0-clustarr.3` against this build's headers)

- [ ] **Step 1: Write the recipe**

Start from Wolfi's `ffmpeg-9.0.yaml` (fetched from `https://raw.githubusercontent.com/wolfi-dev/os/main/ffmpeg-9.0.yaml`; keep its `git-checkout` of `n9.0.2` and `expected-commit`). Changes, each commented in the recipe:
- name `ffmpeg-clustarr`; `provides` none (it must never satisfy another package's `ffmpeg` dependency);
- build-deps added: `nv-codec-headers`, `clang-19` (or newest clang whose `llc` has the NVPTX target: `llc --version | grep nvptx` in the build), `libva-dev`, `libdrm-dev`, `libvpl-dev`;
- configure adds `--enable-ffnvcodec --enable-nvenc --enable-nvdec --enable-cuvid --enable-cuda-llvm --enable-vaapi --enable-libvpl --enable-libdrm` and drops `--enable-ffplay` and `libsdl2-dev` (no display in a pod);
- subpackages: `-libs` (all `lib*.so.*`), `-dev` (`split/dev`), `-ffgo-shim`:

```yaml
  - name: ${{package.name}}-ffgo-shim
    description: ffgo's C shim, built against this FFmpeg's headers
    pipeline:
      - uses: git-checkout
        with:
          repository: https://github.com/mediactl/ffgo
          tag: v0.0.0-clustarr.3
          expected-commit: <the tag's commit, from git ls-remote>
          destination: ffgo
      - runs: |
          cd ffgo/shim
          FFMPEG_DIR=${{targets.destdir}}/usr CC=gcc ./build.sh
          install -Dm755 libffshim.so ${{targets.subpkgdir}}/usr/lib/libffshim.so
```

- package test: `ffmpeg -hide_banner -encoders` contains `hevc_nvenc`, `hevc_qsv`, `hevc_vaapi`, `libx265`, `aac`; `-filters` contains `scale_cuda`, `vpp_qsv`, `scale_vaapi`, `hwupload`; `-decoders` contains `hevc_cuvid` (proves NVDEC/cuvid) ; the shim subpackage test checks `readelf -d /usr/lib/libffshim.so` names `libavcodec.so.63`.

- [ ] **Step 2: Build it**

Run: `make wolfi-packages PACKAGES=ffmpeg-clustarr 2>&1 | tail -30`
Expected: the package test passes. If `--enable-cuda-llvm` fails, the configure log names the reason (no NVPTX target); fix by the clang choice, never by dropping the CUDA filters (spec §5).

- [ ] **Step 3: Commit**

```bash
git commit -m "build(images): ffmpeg-clustarr -- Wolfi's FFmpeg 9.0.2 recipe plus NVENC, NVDEC, CUDA filters (clang), VAAPI and QSV, and the ffgo shim built against the same headers" -- images/wolfi/packages/ffmpeg-clustarr.yaml
```

---

### Task 4: The worker package and the apko images

**Files:**
- Create: `images/wolfi/packages/squasharr-worker.yaml`, `images/wolfi/images/transcoder.yaml`, `transcoder-intel.yaml`, `transcoder-cuda.yaml` (each with a `-debug` variant as a second file that `include`s it and adds `busybox`)
- Modify: `hack/wolfi/build.sh` (`images` verb), `Makefile` (`wolfi-images`)

**Interfaces:**
- Produces: OCI tarballs `images/wolfi/out/images/<name>.tar`, loaded as `ghcr.io/mediactl/clustarr/<name>:dev`; `make wolfi-images` builds and runs `--self-check=<class>` in each (`docker run --rm <image> --self-check=<class>`), failing on a non-zero exit

- [ ] **Step 1: Write the configs**

`squasharr-worker.yaml` builds `./cmd/squasharr-worker` from the repository (melange `--source-dir .`, `uses: go/build` with `packages: ./cmd/squasharr-worker`, `ldflags: -s -w -X github.com/mediactl/clustarr/pkg/version.Version=${{package.version}}`, `CGO_ENABLED=0`), version from `VERSION` (default `0.0.0_git<sha7>`).

`transcoder.yaml`:

```yaml
contents:
  keyring:
    - https://packages.wolfi.dev/os/wolfi-signing.rsa.pub
    - ../melange.rsa.pub
  repositories:
    - https://packages.wolfi.dev/os
    - '@local ../out'
  packages:
    - wolfi-baselayout
    - glibc
    - ld-linux
    - ca-certificates-bundle
    - libstdc++
    - ffmpeg-clustarr-libs@local
    - ffmpeg-clustarr@local        # ffmpeg/ffprobe for the argv engine, until Phase 5
    - ffmpeg-clustarr-ffgo-shim@local
    - squasharr-worker@local
accounts:
  groups: [{ groupname: clustarr, gid: 1000 }]
  users: [{ username: clustarr, uid: 1000, gid: 1000, shell: /sbin/nologin }]
  run-as: "1000"
paths:
  - { path: /data, type: directory, uid: 1000, gid: 1000, permissions: 0o755 }
  - { path: /scratch, type: directory, uid: 1000, gid: 1000, permissions: 0o755 }
environment:
  FFGO_SHIM_DIR: /usr/lib
  LIBVA_MESSAGING_LEVEL: "1"
entrypoint:
  command: /usr/bin/squasharr-worker
archs: [x86_64, aarch64]
annotations:
  org.opencontainers.image.source: https://github.com/mediactl/clustarr
  org.opencontainers.image.licenses: GPL-3.0-only
```

`transcoder-intel.yaml` = the same plus `intel-media-driver@local`, `intel-vpl-gpu-rt@local`, `intel-mediasdk-runtime@local`, `libvpl@local`, `libva`, `libdrm`, `archs: [x86_64]`, `LIBVA_DRIVERS_PATH: /usr/lib/dri`, `LIBVA_DRIVER_NAME: iHD`. `transcoder-cuda.yaml` = `transcoder.yaml`'s contents, `archs: [x86_64]`, plus `NVIDIA_VISIBLE_DEVICES: all`, `NVIDIA_DRIVER_CAPABILITIES: video,compute,utility`. (apko has no `include`; the `-debug` files repeat the base file plus `busybox` — `hack/wolfi/build.sh` generates them with `yq` from the base so they cannot drift, and the generated files are not committed.)

On arm64, `ffmpeg-clustarr` builds without the NVIDIA and Intel options (the recipe's configure adds them under `if [ "${{build.arch}}" = x86_64 ]`), so `transcoder` is multi-arch and the other two are amd64 only.

- [ ] **Step 2: Build and self-check**

Run: `make wolfi-images 2>&1 | tail -30`
Expected, for each of the six images: `apko build` succeeds; `docker load` succeeds; `docker run --rm --read-only --cap-drop=ALL --user 1000:1000 <image> --self-check=<class>` prints a report with `"FFmpegMajor": 9`, `"ShimMatched": true`, every encoder and filter `true`, and exits 0; `docker run --rm --entrypoint /bin/sh <image>` fails for non-debug images (no shell) and works for `-debug`. Record each image's size (`docker image inspect -f '{{.Size}}'`) in `images/wolfi/README.md`.

- [ ] **Step 3: Commit**

```bash
git commit -m "build(images): distroless transcoder, transcoder-intel and transcoder-cuda (and -debug) assembled with apko; each passes --self-check" -- images/wolfi hack/wolfi Makefile
```

---

### Task 5: Real hardware -- `--trial` on NVIDIA and Intel

**Files:**
- Modify: `pkg/transcode/selfcheck/selfcheck.go`, `pkg/transcode/selfcheck/trial_test.go` (create)

**Interfaces:**
- Produces: `Trial` legs:
  - `cuda`: a 2 s 256x144 H.264 clip made in-process (libx264 through `VideoStreamEncoder` into `dir`), decoded on NVDEC (`StreamDecoder` with a CUDA `HWDevice`), `scale_cuda=format=p010le`, `hevc_nvenc` (preset p7, rc constqp, qp 23, spatial and temporal AQ — the owner's archival settings); and the same clip through CPU decode → `hwupload,scale_cuda=format=p010le` → `hevc_nvenc` (Phase 1 Task 5's path, for sources NVDEC cannot decode)
  - `intel`: CPU decode → `hwupload,scale_vaapi=format=p010` → `hevc_vaapi` on a VAAPI device (`/dev/dri/renderD128`), and → `hwupload=extra_hw_frames=64,vpp_qsv=format=p010` → `hevc_qsv` on a QSV device derived from it
  - each leg records `Trials["<leg>"] = nil` or its error; `Trial` fails if every leg of the class failed, and reports the rest

- [ ] **Step 1: Write the failing tests**

```go
// trial_test.go
func TestTrialEncodesOnNVIDIA(t *testing.T) {
	if _, err := ffgo.NewHWDevice(ffgo.HWDeviceTypeCUDA, ""); err != nil {
		t.Skipf("no CUDA device: %v", err)
	}
	r, err := Trial(context.Background(), ClassCUDA, t.TempDir())
	require.NoError(t, err)
	assert.NoError(t, r.Trials["nvdec-nvenc"])
	assert.NoError(t, r.Trials["upload-nvenc"])
}

func TestTrialEncodesOnIntel(t *testing.T) {
	if _, err := os.Stat("/dev/dri/renderD128"); err != nil {
		t.Skip("no render node")
	}
	if _, err := ffgo.NewHWDevice(ffgo.HWDeviceTypeVAAPI, "/dev/dri/renderD128"); err != nil {
		t.Skipf("no VAAPI driver on this host: %v", err) // the workstation; the image brings iHD (Step 4)
	}
	r, err := Trial(context.Background(), ClassIntel, t.TempDir())
	require.NoError(t, err)
	assert.NoError(t, r.Trials["vaapi"])
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test -count=1 -run TestTrial -v ./pkg/transcode/selfcheck/`
Expected: on the workstation, `TestTrialEncodesOnNVIDIA` FAILS (`Trials["nvdec-nvenc"]` missing: the CUDA leg is not written); the Intel test skips by name.

- [ ] **Step 3: Implement the legs**

In `selfcheck.go`, one function per leg, each returning the first error with the step named (`"nvdec-nvenc: open hevc_nvenc: ..."`), built only from the fork's Phase 1 primitives, writing nothing outside `dir`; packets are counted and must equal the 48 input frames.

- [ ] **Step 4: Run them on real hardware**

Run: `go test -count=1 -run TestTrial -v ./pkg/transcode/selfcheck/` → PASS for NVIDIA on the workstation.

Then in the images:
- Intel, on the workstation: `make wolfi-images` and `docker run --rm --read-only --cap-drop=ALL --user 1000:$(stat -c %g /dev/dri/renderD128) --device /dev/dri/renderD128 --tmpfs /scratch:uid=1000 ghcr.io/mediactl/clustarr/transcoder-intel:dev --self-check=intel --trial --scratch-dir=/scratch`. Expected: `vaapi` and `qsv` legs `null`. If the workstation's iGPU generation is not supported by either runtime, the report says which leg failed and why; ledger it as a ruling (the image is verified on the next Intel node) — do not stall.
- NVIDIA, on kind-cluster-plex: `kind load docker-image ghcr.io/mediactl/clustarr/transcoder-cuda:dev --name cluster-plex` (load only this image: the gotcha about loads stalling etcd), then a one-shot pod as Phase 0 Task 7's probe ran — `runtimeClassName: nvidia`, `NVIDIA_VISIBLE_DEVICES=all`, no `nvidia.com/gpu` request (the live pool holds the GPU), `securityContext` as the pool's, args `--self-check=cuda --trial --scratch-dir=/scratch` with an emptyDir at `/scratch`. Expected: both CUDA legs `null` in the pod log; delete the pod. Nothing else on the cluster changes; downloads stay paused.

- [ ] **Step 5: Commit**

```bash
git commit -m "feat(squasharr): --trial encodes for real on the class's device -- NVDEC or upload into hevc_nvenc with the archival settings, VAAPI and QSV for Intel; verified in transcoder-cuda on the RTX 2070 and transcoder-intel on <host's iGPU or the ruling>" -- pkg/transcode/selfcheck
```

---

### Task 6: CI and publishing

**Files:**
- Create: `.github/workflows/wolfi-images.yml`
- Modify: `images/wolfi/README.md`, `images/Dockerfile.transcoder` (header: points at `images/wolfi/` as its successor; removed in Phase 5)

**Interfaces:**
- Produces: on pull requests, packages and images built and self-checked (no push); on `main` and `v*` tags, the six images pushed to `ghcr.io/<repo>/<name>` with the same version scheme as `release.yml` (`<tag without v>` or `dev-<sha7>`), multi-arch manifest for `transcoder`, and apko's SBOMs attached

- [ ] **Step 1: Write the workflow**

Jobs: `packages` (matrix x86_64 on `ubuntu-24.04`, aarch64 on `ubuntu-24.04-arm`; installs melange and apko from their GitHub releases at pinned versions with checksums; restores `images/wolfi/out/` from `actions/cache` keyed by the hash of `images/wolfi/packages/*.yaml` and the fork tag; generates an ephemeral signing key; `hack/wolfi/build.sh packages`; uploads `out/` as an artifact), `images` (needs packages; downloads both arches' `out/`; `apko build` each image; `docker load`; `--self-check=<class>` in each, `--read-only --cap-drop=ALL --user 1000:1000`; on push events `apko publish` with `--sbom-path`), each action pinned by commit SHA as `release.yml` pins them.

- [ ] **Step 2: Run it**

Push the branch state to `main` (the controlling session pushes; Tasks 1-5's commits are on `main` already), then: `gh run watch $(gh run list --workflow wolfi-images.yml --limit 1 --json databaseId -q '.[0].databaseId')`
Expected: every job green; `docker manifest inspect ghcr.io/mediactl/clustarr/transcoder:dev-<sha7>` lists amd64 and arm64.

A red job is fixed in this task, with the fix committed and the run repeated until green.

- [ ] **Step 3: Commit**

```bash
git commit -m "ci(images): build the Wolfi packages and apko images on every change, self-check each image, and publish them with SBOMs from main and tags" -- .github/workflows/wolfi-images.yml images/wolfi/README.md images/Dockerfile.transcoder
git push origin main
```
