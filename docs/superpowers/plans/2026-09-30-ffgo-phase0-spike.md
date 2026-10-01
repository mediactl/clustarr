# ffgo Phase 0 (go/no-go spike and fork foundation) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Prove, or disprove, that ffgo can run squasharr's standard in-process on FFmpeg 9.0 with NVDEC → `scale_cuda` → `hevc_nvenc` on the GPU, and leave a `mediactl/ffgo` fork whose loader, offsets and test suite are correct on FFmpeg 9.

**Architecture:** All code in this plan lives in the fork (`github.com/mediactl/ffgo`, cloned at `/home/appkins/src/mediactl/ffgo`), not in clustarr; clustarr gets only the spike's decision record. The fork keeps every ffgo feature (spec §2: strict superset). FFmpeg 9 correctness comes from the C shim, compiled against the headers of the FFmpeg it runs with, which reports the real struct offsets and enum values; Go checks its constants against it.

**Tech Stack:** Go 1.27 (`CGO_ENABLED=0`), purego, the ffgo C shim (gcc), FFmpeg 9.0.1 shared libraries and headers (`/usr/lib`, `/usr/include` on the owner's workstation, which also carries FFmpeg 4.4's libraries), an RTX 2070 Max-Q with NVIDIA driver 610.57.04.

**Spec:** `docs/superpowers/specs/2026-09-30-ffgo-transcoding-design.md` (clustarr). Phase 0 is its "Go/no-go spike" section plus §2 items 1-3.

## Roadmap (one plan per phase; later plans are written after this one passes)

| Phase | Plan | Depends on |
| --- | --- | --- |
| 0 | This plan: spike, version loader, shim-verified offsets, FFmpeg 9 suite, encoder by name, GPU frames | -- |
| 1 | Fork completion: multi-stream `Muxer` (copy/encode, language, disposition, attachments, chapters, metadata), side data (mastering display, light level, HDR10+ and DV RPU removal), AAC encoding and 7.1→5.1 resampling; first fork tag | 0 |
| 2 | Images: melange packages (`ffmpeg-clustarr`, Intel media stack, `squasharr-worker`), apko images, `--self-check` in CI | 0 |
| 3 | squasharr: `Plan` rewrite (§1 rules, plan JSON, `planHash`), `Engine` on the fork, `--engine` flag, API shrink | 1, 2 |
| 4 | Hardware selection: node-label classes, pod reports, class fallback (§4) | 3 |
| 5 | Parity harness on the owner's files, default switch, removal of the argv engine | 3, 4 |

If Task 6 or Task 7 below fails the go/no-go, Phases 1-5 are replaced by the spec's staged fallback (keep the `ffmpeg` executable) and planned from there.

## Global Constraints

- The fork is a strict superset of ffgo: nothing upstream is removed or narrowed; ffgo's whole test suite must pass on FFmpeg 9.0 (spec §2).
- FFmpeg 9.0 is the target: libavutil 61, libavcodec 63, libavformat 63, libavfilter 12, libswscale 10, libswresample 7 (read from `/usr/lib` on 2026-09-30).
- Pure Go builds: `CGO_ENABLED=0` for every Go binary; the only C is the shim, loaded at run time.
- Licence: the fork stays Apache-2.0; clustarr (GPL-3.0) consumes it as a module.
- Fork changes land with a test and are offered upstream as pull requests; clustarr pins the fork by tag.
- Every fix is falsified: reverted, and its test seen to fail by name (clustarr CLAUDE.md).
- No `git push` to clustarr from this plan; pushes to `mediactl/ffgo` are the fork's own branch and tags only.

## Review Focus

- **Two FFmpeg versions installed side by side** (the workstation has 4.4 and 9.0): ffgo must load one consistent set, never libavutil from one and libavcodec from another, and must say which. Pinned by Task 2's mixed-versions test.
- **The decoder's last frames at end of file**: `HWDecoder.ReadHWFrame` returns `avformat.ReadFrame`'s EOF without draining the decoder, so the final frames (up to the decoder's delay) vanish. A 960-frame source must give 960 encoded frames. Pinned by Task 6's frame-count assertion.
- **Pixel-format numbers that move between FFmpeg majors** (`AV_PIX_FMT_P010LE`, `AV_PIX_FMT_CUDA`): a hard-coded value names a different format on FFmpeg 9. Pinned by Task 3's enum check against `av_get_pix_fmt`.
- **An encoder name the FFmpeg build does not have** (`hevc_nvenc` on a build without NVENC): a clear error naming the encoder, never a nil codec reaching `avcodec_alloc_context3`. Pinned by Task 5.
- **No GPU or no driver** (`CUDA_VISIBLE_DEVICES=` empty, or no `libcuda`): creating the CUDA device returns an error; nothing hangs or crashes. Pinned by Task 6's no-device test.

---

### Task 1: Fork, local build, and the FFmpeg 9 baseline

**Files:**
- Create (fork): `docs/ffmpeg9-baseline.md`
- Modify (fork): `shim/build.sh` only if it cannot find `/usr/include` headers (record why in the commit)

**Interfaces:**
- Produces: the fork at `/home/appkins/src/mediactl/ffgo` on branch `clustarr/ffmpeg9`; `shim/prebuilt/linux-amd64/libffshim.so` built against FFmpeg 9.0.1; the list of tests failing on FFmpeg 9, which Task 4 works through.

- [ ] **Step 1: Fork and clone**

```bash
gh repo fork obinnaokechukwu/ffgo --org mediactl --clone=false
git clone git@github.com:mediactl/ffgo.git /home/appkins/src/mediactl/ffgo
cd /home/appkins/src/mediactl/ffgo
git remote add upstream https://github.com/obinnaokechukwu/ffgo.git
git switch -c clustarr/ffmpeg9
```

Expected: `git log --oneline -1` shows upstream's `8e9c9c1` (or newer).

- [ ] **Step 2: Build the shim against FFmpeg 9.0.1**

```bash
cd shim && ./build.sh && cd ..
nm -D shim/prebuilt/linux-amd64/libffshim.so | grep -c ' T ffshim_'
```

Expected: a positive count. Confirm it was built against 9.0.1:

```bash
cat > /tmp/ffshim-ver.go <<'EOF'
package main

import (
	"fmt"

	"github.com/ebitengine/purego"
)

func main() {
	lib, err := purego.Dlopen("./shim/prebuilt/linux-amd64/libffshim.so", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		panic(err)
	}
	var v func() uint32
	purego.RegisterLibFunc(&v, lib, "ffshim_avcodec_version")
	fmt.Printf("avcodec %d.%d.%d\n", v()>>16, (v()>>8)&0xff, v()&0xff)
}
EOF
CGO_ENABLED=0 go run /tmp/ffshim-ver.go
```

Expected: `avcodec 63.1.101`.

- [ ] **Step 3: Run upstream's suite and record the baseline**

```bash
CGO_ENABLED=0 go test ./... -count=1 2>&1 | tee /tmp/ffgo-baseline.txt | grep -E '^(ok|FAIL|---)' 
```

Write `docs/ffmpeg9-baseline.md` with: the FFmpeg versions the run loaded (Task 2 makes this explicit; for now note that the loader's lists stop at libavcodec 61, so on this host it falls back to FFmpeg 4.4's `.so.58` libraries or the unversioned 9.0 links), and every failing test with its first error line.

- [ ] **Step 4: Commit**

```bash
git add docs/ffmpeg9-baseline.md shim/prebuilt/linux-amd64/libffshim.so
git commit -m "docs: record ffgo's test suite on a host with FFmpeg 9.0.1 and 4.4 installed"
```

---

### Task 2: Load one consistent FFmpeg version, newest first

**Files:**
- Modify (fork): `internal/bindings/bindings.go` (`doLoad`, `loadLibrary`)
- Create (fork): `internal/bindings/versions.go`
- Test (fork): `internal/bindings/versions_test.go`

**Interfaces:**
- Produces:
  - `type VersionSet struct { FFmpeg int; AVUtil, AVCodec, AVFormat, AVFilter, SWScale, SWResample int }`
  - `var KnownVersionSets []VersionSet` (newest first)
  - `func LoadedVersionSet() (VersionSet, bool)`
  - env `FFGO_FFMPEG_MAJOR` (e.g. `9`) pins the set; unset tries newest first.
  - `doLoad` loads every library of one set or none of them.

- [ ] **Step 1: Write the failing tests**

```go
package bindings

import (
	"os"
	"testing"
)

// The owner's workstation has FFmpeg 9.0 and 4.4 installed side by side.
// ffgo must load one whole set -- never libavutil 56 beside libavcodec 63 --
// and report which.
func TestLoadPicksTheNewestCompleteSet(t *testing.T) {
	if os.Getenv("FFGO_FFMPEG_MAJOR") != "" {
		t.Skip("FFGO_FFMPEG_MAJOR pins the set")
	}
	if err := Load(); err != nil {
		t.Skipf("no FFmpeg on this host: %v", err)
	}
	set, ok := LoadedVersionSet()
	if !ok {
		t.Fatal("loaded, but no version set recorded")
	}
	got := []int{int(avutilVersion() >> 16), int(avcodecVersion() >> 16), int(avformatVersion() >> 16)}
	want := []int{set.AVUtil, set.AVCodec, set.AVFormat}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("loaded majors %v, but set FFmpeg %d is %v: libraries from two versions", got, set.FFmpeg, want)
		}
	}
	t.Logf("loaded FFmpeg %d (%v)", set.FFmpeg, got)
}

func TestKnownVersionSetsAreNewestFirstAndDistinct(t *testing.T) {
	for i := 1; i < len(KnownVersionSets); i++ {
		if KnownVersionSets[i-1].FFmpeg <= KnownVersionSets[i].FFmpeg {
			t.Fatalf("sets not newest first at %d", i)
		}
	}
	if KnownVersionSets[0] != (VersionSet{FFmpeg: 9, AVUtil: 61, AVCodec: 63, AVFormat: 63, AVFilter: 12, SWScale: 10, SWResample: 7}) {
		t.Fatalf("newest set is %+v", KnownVersionSets[0])
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `CGO_ENABLED=0 go test ./internal/bindings/ -run 'TestLoadPicks|TestKnownVersionSets' -count=1`
Expected: FAIL to build -- `undefined: LoadedVersionSet`, `undefined: KnownVersionSets`.

- [ ] **Step 3: Implement**

`internal/bindings/versions.go`:

```go
package bindings

import (
	"fmt"
	"os"
	"strconv"
)

// VersionSet is one FFmpeg release's library majors. Libraries of different
// releases must never be mixed in one process: their structs differ.
type VersionSet struct {
	FFmpeg                                              int
	AVUtil, AVCodec, AVFormat, AVFilter, SWScale, SWResample int
}

// KnownVersionSets are the releases ffgo can load, newest first. 9 was read
// from FFmpeg 9.0.1's sonames; 4-7 are upstream ffgo's lists.
var KnownVersionSets = []VersionSet{
	{FFmpeg: 9, AVUtil: 61, AVCodec: 63, AVFormat: 63, AVFilter: 12, SWScale: 10, SWResample: 7},
	{FFmpeg: 8, AVUtil: 60, AVCodec: 62, AVFormat: 62, AVFilter: 11, SWScale: 9, SWResample: 6},
	{FFmpeg: 7, AVUtil: 59, AVCodec: 61, AVFormat: 61, AVFilter: 10, SWScale: 8, SWResample: 5},
	{FFmpeg: 6, AVUtil: 58, AVCodec: 60, AVFormat: 60, AVFilter: 9, SWScale: 7, SWResample: 4},
	{FFmpeg: 5, AVUtil: 57, AVCodec: 59, AVFormat: 59, AVFilter: 8, SWScale: 6, SWResample: 4},
	{FFmpeg: 4, AVUtil: 56, AVCodec: 58, AVFormat: 58, AVFilter: 7, SWScale: 5, SWResample: 3},
}

var loadedSet *VersionSet

// LoadedVersionSet is the set Load chose.
func LoadedVersionSet() (VersionSet, bool) {
	if loadedSet == nil {
		return VersionSet{}, false
	}
	return *loadedSet, true
}

// candidateSets is KnownVersionSets, or the one FFGO_FFMPEG_MAJOR names.
func candidateSets() ([]VersionSet, error) {
	pin := os.Getenv("FFGO_FFMPEG_MAJOR")
	if pin == "" {
		return KnownVersionSets, nil
	}
	major, err := strconv.Atoi(pin)
	if err != nil {
		return nil, fmt.Errorf("ffgo: FFGO_FFMPEG_MAJOR=%q is not a number", pin)
	}
	for _, s := range KnownVersionSets {
		if s.FFmpeg == major {
			return []VersionSet{s}, nil
		}
	}
	return nil, fmt.Errorf("ffgo: FFGO_FFMPEG_MAJOR=%d is not a known FFmpeg release", major)
}
```

In `bindings.go`, replace the four `loadLibrary` calls in `doLoad` with a loop over `candidateSets()`: for each set, open `avutil`, `avcodec`, `avformat` with **only that set's major** (`loadLibrary(name, []int{set.AVCodec})` and no unversioned fallback), and `swscale` with `set.SWScale` (still optional). If any required one fails, `purego.Dlclose` what was opened and try the next set. On success, set `loadedSet = &set`. Remove the unversioned fallback from `loadLibrary` for required libraries (it is what lets a 9.0 `libavcodec.so` link sit beside a 4.4 `libavutil.so.56`). The error when nothing loads lists every set tried.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `CGO_ENABLED=0 go test ./internal/bindings/ -count=1 -v -run 'TestLoadPicks|TestKnownVersionSets'`
Expected: PASS, logging `loaded FFmpeg 9 ([61 63 63])`. Then: `FFGO_FFMPEG_MAJOR=4 CGO_ENABLED=0 go test ./internal/bindings/ -count=1 -v -run TestLoadPicks` skips by name, and a one-off `FFGO_FFMPEG_MAJOR=4 go run ./examples/<any probe example>` loads 4.4.

- [ ] **Step 5: Falsify**

Restore the unversioned fallback for `avutil` only; `TestLoadPicksTheNewestCompleteSet` must still pass here (9.0 complete), so also run with `LD_LIBRARY_PATH` pointing at a directory holding only 4.4's `libavutil.so.56` link and 9.0's `libavcodec.so.63`: the test must fail with "libraries from two versions". Revert.

- [ ] **Step 6: Commit**

```bash
git add internal/bindings/
git commit -m "fix(bindings): load one FFmpeg release's libraries, newest first, never a mix -- FFmpeg 9 (avcodec 63) is known, and FFGO_FFMPEG_MAJOR pins a release"
```

---

### Task 3: Struct offsets and enum values from the shim

**Files:**
- Modify (fork): `shim/ffshim.c`, `shim/ffshim.h`
- Create (fork): `internal/layout/layout.go`, `internal/layout/layout_test.go`
- Modify (fork): every Go file that declares a struct offset constant (`grep -rn -E 'offset[A-Za-z]+ *= *[0-9]+' --include='*.go' .` lists them; avcodec/avcodec.go alone has the AVPacket, AVCodecContext and AVCodecParameters sets)
- Modify (fork): `avutil/pixfmt.go` (pixel-format constants)

**Interfaces:**
- Produces:
  - shim: `int ffshim_offsetof(const char *field)` -- `offsetof` for `"AVCodecContext.width"` style names, `-1` for an unknown name.
  - shim: `int ffshim_pix_fmt(const char *name)` -- `av_get_pix_fmt(name)`.
  - Go: `layout.Offset(field string, fallback uintptr) uintptr` -- the shim's value when the shim is loaded, else `fallback`.
  - Go: `layout.Mismatches() []string` -- every registered field whose fallback differs from the shim's value.
  - Go: `avutil.PixelFormatByName(name string) PixelFormat`, and `avutil.PixelFormatCUDA`, re-exported as `ffgo.PixelFormatCUDA` beside ffgo's other pixel-format aliases.
  - The offset constants become package `var`s initialised through `layout.Offset` at load.

- [ ] **Step 1: Write the failing test**

```go
package layout_test

import (
	"testing"

	"github.com/obinnaokechukwu/ffgo"
	"github.com/obinnaokechukwu/ffgo/avutil"
	"github.com/obinnaokechukwu/ffgo/internal/layout"
)

// Every struct offset ffgo's Go code uses must be the offset the shim,
// compiled against the running FFmpeg's headers, reports. On FFmpeg 9 a
// hard-coded offset that drifted reads another field silently.
func TestEveryOffsetMatchesTheHeaders(t *testing.T) {
	if err := ffgo.Init(); err != nil {
		t.Skipf("no FFmpeg: %v", err)
	}
	if !layout.ShimLoaded() {
		t.Skip("no shim on this host: offsets cannot be checked")
	}
	if m := layout.Mismatches(); len(m) > 0 {
		t.Fatalf("offsets that differ from this FFmpeg's headers:\n%v", m)
	}
}

// Pixel formats are an enum whose values move between FFmpeg majors; the
// formats the GPU path names must come from the library, not a constant.
func TestPixelFormatsComeFromTheLibrary(t *testing.T) {
	if err := ffgo.Init(); err != nil {
		t.Skipf("no FFmpeg: %v", err)
	}
	for name, want := range map[string]avutil.PixelFormat{
		"yuv420p":     avutil.PixelFormatYUV420P,
		"p010le":      avutil.PixelFormatP010LE,
		"yuv420p10le": avutil.PixelFormatYUV420P10LE,
		"cuda":        avutil.PixelFormatCUDA,
	} {
		if got := avutil.PixelFormatByName(name); got != want {
			t.Errorf("%s: library says %d, ffgo's constant is %d", name, got, want)
		}
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `CGO_ENABLED=0 go test ./internal/layout/ -count=1`
Expected: FAIL to build -- package `internal/layout` and `avutil.PixelFormatByName` do not exist.

- [ ] **Step 3: Add the shim functions**

In `shim/ffshim.c` (and declare in `ffshim.h`):

```c
#include <stddef.h>
#include <string.h>
#include <libavutil/pixdesc.h>

struct ffshim_field { const char *name; int offset; };

#define FIELD(type, member) { #type "." #member, (int)offsetof(type, member) }

static const struct ffshim_field ffshim_fields[] = {
    FIELD(AVPacket, pts), FIELD(AVPacket, dts), FIELD(AVPacket, data),
    FIELD(AVPacket, size), FIELD(AVPacket, stream_index), FIELD(AVPacket, flags),
    FIELD(AVPacket, duration), FIELD(AVPacket, pos),
    FIELD(AVCodecContext, codec_type), FIELD(AVCodecContext, codec_id),
    FIELD(AVCodecContext, bit_rate), FIELD(AVCodecContext, flags),
    FIELD(AVCodecContext, time_base), FIELD(AVCodecContext, width),
    FIELD(AVCodecContext, height), FIELD(AVCodecContext, gop_size),
    FIELD(AVCodecContext, pix_fmt), FIELD(AVCodecContext, max_b_frames),
    FIELD(AVCodecParameters, codec_type), FIELD(AVCodecParameters, codec_id),
    FIELD(AVCodecParameters, codec_tag),
    /* One FIELD per offset constant in the Go code: the list is complete
       when TestEveryOffsetMatchesTheHeaders passes. */
};

int ffshim_offsetof(const char *field) {
    for (size_t i = 0; i < sizeof(ffshim_fields) / sizeof(ffshim_fields[0]); i++) {
        if (strcmp(ffshim_fields[i].name, field) == 0) {
            return ffshim_fields[i].offset;
        }
    }
    return -1;
}

int ffshim_pix_fmt(const char *name) { return (int)av_get_pix_fmt(name); }
```

Rebuild: `cd shim && ./build.sh && cd ..`.

- [ ] **Step 4: Add `internal/layout` and route the offsets through it**

```go
// Package layout resolves FFmpeg struct offsets: the shim, compiled against
// the running FFmpeg's headers, is the authority; the hard-coded values are
// what ffgo used before and the fallback when no shim is present.
package layout

import (
	"fmt"
	"sort"
	"sync"
)

var (
	mu         sync.Mutex
	registered = map[string]uintptr{} // field -> fallback
	shimOffset func(field string) int // nil without a shim
)

// SetShim installs the shim's ffshim_offsetof; bindings.Load calls it.
func SetShim(f func(string) int) { mu.Lock(); shimOffset = f; mu.Unlock() }

// ShimLoaded reports whether offsets come from the shim.
func ShimLoaded() bool { mu.Lock(); defer mu.Unlock(); return shimOffset != nil }

// Offset is field's offset: the shim's, else fallback. Every call registers
// the field so Mismatches can audit it.
func Offset(field string, fallback uintptr) uintptr {
	mu.Lock()
	defer mu.Unlock()
	registered[field] = fallback
	if shimOffset != nil {
		if off := shimOffset(field); off >= 0 {
			return uintptr(off)
		}
	}
	return fallback
}

// Mismatches lists every registered field the shim does not know or whose
// fallback differs from the shim's value.
func Mismatches() []string {
	mu.Lock()
	defer mu.Unlock()
	var out []string
	for field, fallback := range registered {
		off := shimOffset(field)
		switch {
		case off < 0:
			out = append(out, fmt.Sprintf("%s: unknown to the shim", field))
		case uintptr(off) != fallback:
			out = append(out, fmt.Sprintf("%s: hard-coded %d, headers %d", field, fallback, off))
		}
	}
	sort.Strings(out)
	return out
}
```

Then, per offset constant: `const offsetCtxWidth = 116` becomes `var offsetCtxWidth uintptr` assigned in the package's existing init-after-load hook as `offsetCtxWidth = layout.Offset("AVCodecContext.width", 116)`. Register the shim in `bindings.doLoad` after loading it: `purego.RegisterLibFunc(&offsetof, libFFShim, "ffshim_offsetof"); layout.SetShim(func(f string) int { return int(offsetof(f)) })` (the binding takes a `string`; purego converts it to a C string). Add `avutil.PixelFormatByName` on `ffshim_pix_fmt` (or `av_get_pix_fmt` bound directly from libavutil, which needs no shim), and turn `PixelFormatP010LE`, `PixelFormatYUV420P10LE` and `PixelFormatCUDA` into values resolved by name at load.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `CGO_ENABLED=0 go test ./internal/layout/ -count=1 -v`
Expected: PASS. Before the shim list is complete the first test prints the unknown fields; add a `FIELD` for each until it passes.

- [ ] **Step 6: Falsify**

Change `offsetCtxWidth`'s fallback to 112 and run with the shim: PASS must turn into "AVCodecContext.width: hard-coded 112, headers 116"; set `PixelFormatCUDA` to a constant 117 and the enum test must fail naming `cuda`. Revert both.

- [ ] **Step 7: Commit**

```bash
git add shim/ internal/layout/ internal/bindings/ avcodec/ avutil/ avformat/
git commit -m "fix: struct offsets and pixel-format values come from the shim built against the running FFmpeg, with the hard-coded values as fallback and an audit of every one -- FFmpeg 9 moved fields and formats"
```

---

### Task 4: ffgo's whole suite green on FFmpeg 9.0

**Files:**
- Modify (fork): whatever the failing tests in `docs/ffmpeg9-baseline.md` lead to
- Modify (fork): `docs/ffmpeg9-baseline.md` (closed out)
- Create (fork): `.github/workflows/ffmpeg9.yml`

**Interfaces:**
- Consumes: Task 2's loader, Task 3's offsets.
- Produces: `CGO_ENABLED=0 go test ./...` passing with FFmpeg 9.0 and with `FFGO_FFMPEG_MAJOR=4`; CI that runs both.

- [ ] **Step 1: Re-run the suite on FFmpeg 9 and on 4.4**

```bash
CGO_ENABLED=0 go test ./... -count=1 2>&1 | grep -E '^(ok|FAIL|---)'
FFGO_FFMPEG_MAJOR=4 CGO_ENABLED=0 go test ./... -count=1 2>&1 | grep -E '^(ok|FAIL|---)'
```

Expected: fewer failures than the baseline (Tasks 2-3 fix the version mixing and offset drift).

- [ ] **Step 2: For each remaining failure, in order: reproduce, find the cause, fix it with a test**

For each failing test: run it alone with `-v`; read FFmpeg 9's header for the API it calls (`/usr/include/libav*/`); fix in the binding, the shim, or the offset table. If the fix changes behaviour that no existing test pins, add the test first and watch it fail. Each fix is its own commit naming the FFmpeg 9 change it answers (`fix(avcodec): ... -- FFmpeg 9 removed X`). Falsify each.

- [ ] **Step 3: Add CI**

`.github/workflows/ffmpeg9.yml` runs `go test ./...` on `ubuntu-latest` in two jobs: one with FFmpeg 9.0 shared libraries and headers (BtbN `ffmpeg-n9.0-latest-linux64-gpl-shared-9.0.tar.xz` unpacked to `/opt/ffmpeg`, `LD_LIBRARY_PATH=/opt/ffmpeg/lib`, the shim built against `/opt/ffmpeg/include`) and one with the distribution's FFmpeg (Ubuntu's 6.x) and `FFGO_FFMPEG_MAJOR` unset.

- [ ] **Step 4: Close out the baseline and commit**

Update `docs/ffmpeg9-baseline.md` with "all passing on 9.0.1 and 4.4" and the list of fixes.

```bash
git add docs/ffmpeg9-baseline.md .github/workflows/ffmpeg9.yml
git commit -m "ci: run the whole suite on FFmpeg 9.0 shared libraries and on the distribution's FFmpeg"
```

---

### Task 5: Choose the encoder by name

**Files:**
- Modify (fork): `encoder.go` (`VideoEncoderConfig`, the video stream setup that calls `avcodec.FindEncoder`)
- Test (fork): `encoder_name_test.go`

**Interfaces:**
- Consumes: `avcodec.FindEncoderByName(name string) avcodec.Codec` (exists).
- Produces: `VideoEncoderConfig.EncoderName string` -- when set, the encoder is looked up by this name and `Codec` must be zero or match; `ErrEncoderNotFound` (wraps the name).

- [ ] **Step 1: Write the failing tests**

```go
package ffgo_test

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/obinnaokechukwu/ffgo"
)

func TestEncoderNameSelectsTheEncoder(t *testing.T) {
	if err := ffgo.Init(); err != nil {
		t.Skipf("no FFmpeg: %v", err)
	}
	out := filepath.Join(t.TempDir(), "x265.mkv")
	enc, err := ffgo.NewEncoderWithOptions(out, &ffgo.EncoderOptions{Video: &ffgo.VideoEncoderConfig{
		EncoderName: "libx265", Width: 256, Height: 256,
		FrameRate: ffgo.NewRational(25, 1), PixelFormat: ffgo.PixelFormatYUV420P,
	}})
	if err != nil {
		t.Fatalf("libx265 by name: %v", err)
	}
	writeTestFrames(t, enc, 256, 256, 10) // the suite's existing synthetic-frame helper
	if err := enc.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := ffgo.Probe(out)
	if err != nil {
		t.Fatal(err)
	}
	if info.Video().CodecName != "hevc" {
		t.Fatalf("encoded %s, want hevc", info.Video().CodecName)
	}
}

func TestAnUnknownEncoderNameIsAnErrorNamingIt(t *testing.T) {
	if err := ffgo.Init(); err != nil {
		t.Skipf("no FFmpeg: %v", err)
	}
	_, err := ffgo.NewEncoderWithOptions(filepath.Join(t.TempDir(), "x.mkv"), &ffgo.EncoderOptions{
		Video: &ffgo.VideoEncoderConfig{EncoderName: "hevc_nosuch", Width: 64, Height: 64, FrameRate: ffgo.NewRational(25, 1)},
	})
	if !errors.Is(err, ffgo.ErrEncoderNotFound) {
		t.Fatalf("got %v, want ErrEncoderNotFound", err)
	}
	if err == nil || !contains(err.Error(), "hevc_nosuch") {
		t.Fatalf("error %v does not name the encoder", err)
	}
}
```

(`writeTestFrames`, `contains` and the exact `Probe` accessor names are whatever the suite already provides; use them, do not add new helpers if equivalents exist.)

- [ ] **Step 2: Run them to verify they fail**

Run: `CGO_ENABLED=0 go test . -run 'TestEncoderName|TestAnUnknownEncoderName' -count=1`
Expected: FAIL to build -- `unknown field EncoderName`, `undefined: ffgo.ErrEncoderNotFound`.

- [ ] **Step 3: Implement**

In `encoder.go`:

```go
// ErrEncoderNotFound is returned when VideoEncoderConfig.EncoderName names an
// encoder this FFmpeg build does not have.
var ErrEncoderNotFound = errors.New("ffgo: encoder not found")
```

Add to `VideoEncoderConfig`:

```go
	// EncoderName selects the encoder by name ("libx265", "hevc_nvenc",
	// "hevc_qsv", "hevc_vaapi"), overriding the default encoder for Codec.
	EncoderName string
```

Where the video setup does `codec := avcodec.FindEncoder(codecID)`:

```go
	var codec avcodec.Codec
	if cfg.EncoderName != "" {
		codec = avcodec.FindEncoderByName(cfg.EncoderName)
		if codec == nil {
			return fmt.Errorf("%w: %s", ErrEncoderNotFound, cfg.EncoderName)
		}
	} else {
		codec = avcodec.FindEncoder(codecID)
	}
```

- [ ] **Step 4: Run them to verify they pass**

Run: `CGO_ENABLED=0 go test . -run 'TestEncoderName|TestAnUnknownEncoderName' -count=1 -v`
Expected: PASS.

- [ ] **Step 5: Falsify, then commit**

Ignore `EncoderName` (always `FindEncoder`): the unknown-name test must fail. Revert.

```bash
git add encoder.go encoder_name_test.go
git commit -m "feat(encoder): VideoEncoderConfig.EncoderName selects the encoder by name (hevc_nvenc, hevc_qsv, hevc_vaapi), and an unknown name is ErrEncoderNotFound naming it"
```

---

### Task 6: GPU frames from NVDEC through scale_cuda into NVENC (the go/no-go)

**Files:**
- Modify (fork): `shim/ffshim.c`, `shim/ffshim.h`
- Modify (fork): `avfilter/avfilter.go` (bind `av_buffersink_get_hw_frames_ctx`)
- Modify (fork): `filter_graph.go` (`FilterGraphConfig.HWFramesCtx`)
- Modify (fork): `encoder.go` (`VideoEncoderConfig.HWFramesCtx`)
- Modify (fork): `hwaccel.go` (`HWDecoder` drains at EOF; `Frame.HWFramesCtx`)
- Test (fork): `gpu_pipeline_test.go`

**Interfaces:**
- Consumes: Task 3's `avutil.PixelFormatCUDA`, Task 5's `EncoderName`; existing `NewHWDevice`, `NewHWDecoder`, `HWDecoder.ReadHWFrame`.
- Produces:
  - shim: `void *ffshim_frame_hw_frames_ctx(void *frame)`; `int ffshim_buffersrc_set_hw_frames(void *src_ctx, void *frames_ref)`.
  - `func (f Frame) HWFramesCtx() avutil.AVBufferRef`.
  - `FilterGraphConfig.HWFramesCtx avutil.AVBufferRef` -- the buffersrc takes GPU frames from this pool.
  - `func (g *FilterGraph) OutputHWFramesCtx() avutil.AVBufferRef` -- the pool `scale_cuda` writes into.
  - `VideoEncoderConfig.HWFramesCtx avutil.AVBufferRef` -- the encoder takes GPU frames from this pool (its `pix_fmt` becomes the pool's format).
  - `HWDecoder.ReadHWFrame` returns every frame, draining the decoder at EOF, then `io.EOF`.

- [ ] **Step 1: Write the failing tests**

```go
package ffgo_test

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/obinnaokechukwu/ffgo"
)

// The spike's question: NVDEC -> scale_cuda -> hevc_nvenc in-process, every
// frame staying in GPU memory. The ffmpeg CLI used 1.0 s of CPU for this
// clip (40 s of 1080p H.264, 960 frames) against 14.4 s decoding in
// software; in-process must be in the same range and lose no frame.
func TestNVDECScaleCUDANVENCKeepsFramesOnTheGPU(t *testing.T) {
	if err := ffgo.Init(); err != nil {
		t.Skipf("no FFmpeg: %v", err)
	}
	dev, err := ffgo.NewHWDevice(ffgo.HWDeviceTypeCUDA, "")
	if err != nil {
		t.Skipf("no CUDA device: %v", err)
	}
	defer dev.Close()

	src := filepath.Join(t.TempDir(), "src.mkv")
	makeClip(t, src) // ffmpeg CLI: testsrc2 1920x1080 24 fps, 40 s, libx264 yuv420p

	before := cpuTime(t)
	dec, err := ffgo.NewHWDecoder(src, &ffgo.HWDecoderConfig{HWDevice: dev, OutputSoftwareFrames: false})
	if err != nil {
		t.Fatal(err)
	}
	defer dec.Close()

	first, err := dec.ReadHWFrame()
	if err != nil {
		t.Fatal(err)
	}
	vs := dec.VideoStream()
	graph, err := ffgo.NewFilterGraph(ffgo.FilterGraphConfig{
		Width: vs.Width, Height: vs.Height, PixelFmt: ffgo.PixelFormatCUDA,
		TimeBase: vs.TimeBase, FrameRate: vs.FrameRate,
		HWFramesCtx: first.HWFramesCtx(), Filters: "scale_cuda=format=p010le",
	})
	if err != nil {
		t.Fatalf("filter graph on GPU frames: %v", err)
	}
	defer graph.Close()

	out := filepath.Join(t.TempDir(), "out.mkv")
	var enc *ffgo.Encoder
	encoded := 0
	push := func(frames []*ffgo.Frame) {
		for _, f := range frames {
			if enc == nil {
				enc, err = ffgo.NewEncoderWithOptions(out, &ffgo.EncoderOptions{Video: &ffgo.VideoEncoderConfig{
					EncoderName: "hevc_nvenc", Width: vs.Width, Height: vs.Height, FrameRate: vs.FrameRate,
					Profile: "main10", HWFramesCtx: graph.OutputHWFramesCtx(),
					CodecOptions: map[string]string{"preset": "p6", "tune": "hq", "rc": "vbr", "cq": "24"},
				}})
				if err != nil {
					t.Fatalf("hevc_nvenc on GPU frames: %v", err)
				}
			}
			if err := enc.WriteVideoFrame(*f); err != nil {
				t.Fatal(err)
			}
			encoded++
		}
	}
	frames, err := graph.Filter(&first)
	if err != nil {
		t.Fatal(err)
	}
	push(frames)
	for {
		f, err := dec.ReadHWFrame()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		frames, err := graph.Filter(&f)
		if err != nil {
			t.Fatal(err)
		}
		push(frames)
	}
	rest, err := graph.Flush()
	if err != nil {
		t.Fatal(err)
	}
	push(rest)
	if err := enc.Close(); err != nil {
		t.Fatal(err)
	}
	cpu := cpuTime(t) - before

	if encoded != 960 {
		t.Fatalf("encoded %d frames of 960: the decoder was not drained at EOF", encoded)
	}
	probe := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=codec_name,profile,pix_fmt", "-of", "csv=p=0", out)
	got, err := probe.Output()
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hevc,Main 10,yuv420p10le\n" {
		t.Fatalf("output %q", got)
	}
	t.Logf("in-process CPU time %v for 960 frames (ffmpeg CLI: 1.0s on NVDEC, 14.4s in software)", cpu)
	if cpu > 4*time.Second {
		t.Fatalf("CPU time %v: frames are not staying on the GPU", cpu)
	}
}

// With no visible GPU, creating the CUDA device is an error, not a hang.
func TestNoCUDADeviceIsAnError(t *testing.T) {
	if os.Getenv("FFGO_CHILD_NO_GPU") == "" {
		cmd := exec.Command(os.Args[0], "-test.run=TestNoCUDADeviceIsAnError", "-test.count=1")
		cmd.Env = append(os.Environ(), "FFGO_CHILD_NO_GPU=1", "CUDA_VISIBLE_DEVICES=")
		done := make(chan error, 1)
		go func() { done <- cmd.Run() }()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("child: %v", err)
			}
		case <-time.After(30 * time.Second):
			_ = cmd.Process.Kill()
			t.Fatal("creating a CUDA device with no GPU hung")
		}
		return
	}
	if err := ffgo.Init(); err != nil {
		t.Skipf("no FFmpeg: %v", err)
	}
	if _, err := ffgo.NewHWDevice(ffgo.HWDeviceTypeCUDA, ""); err == nil {
		t.Fatal("a CUDA device opened with CUDA_VISIBLE_DEVICES empty")
	}
}

func cpuTime(t *testing.T) time.Duration {
	t.Helper()
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		t.Fatal(err)
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}

func makeClip(t *testing.T, path string) {
	t.Helper()
	out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=1920x1080:rate=24", "-t", "40",
		"-pix_fmt", "yuv420p", "-c:v", "libx264", "-preset", "veryfast", "-crf", "20", path).CombinedOutput()
	if err != nil {
		t.Skipf("cannot make the clip (no ffmpeg CLI): %v %s", err, out)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `CGO_ENABLED=0 go test . -run 'TestNVDECScaleCUDANVENC|TestNoCUDADevice' -count=1 -v`
Expected: FAIL to build -- `unknown field HWFramesCtx`, `undefined: ffgo.PixelFormatCUDA` (if not re-exported), `first.HWFramesCtx undefined`, `graph.OutputHWFramesCtx undefined`.

- [ ] **Step 3: Shim functions**

```c
#include <libavfilter/buffersrc.h>

void *ffshim_frame_hw_frames_ctx(void *frame) {
    return ((AVFrame *)frame)->hw_frames_ctx;
}

/* Gives a buffersrc the GPU frame pool its input frames come from: without
   it, a filter graph fed GPU frames negotiates nothing. */
int ffshim_buffersrc_set_hw_frames(void *src_ctx, void *frames_ref) {
    AVBufferSrcParameters *p = av_buffersrc_parameters_alloc();
    if (!p) return AVERROR(ENOMEM);
    p->hw_frames_ctx = (AVBufferRef *)frames_ref;
    int ret = av_buffersrc_parameters_set((AVFilterContext *)src_ctx, p);
    av_free(p);
    return ret;
}
```

Rebuild the shim.

- [ ] **Step 4: Go wiring**

- `avfilter/avfilter.go`: bind `av_buffersink_get_hw_frames_ctx(ctx uintptr) uintptr` as `BuffersinkGetHWFramesCtx`.
- `hwaccel.go`: `func (f Frame) HWFramesCtx() avutil.AVBufferRef` on `ffshim_frame_hw_frames_ctx`. In `ReadHWFrame`, when `avformat.ReadFrame` returns EOF the first time, send a nil packet (`avcodec.SendPacket(ctx, nil)`) and keep receiving until `ReceiveFrame` returns `AVERROR_EOF`; only then return `io.EOF`. Track a `draining bool` on the decoder.
- `filter_graph.go`: after creating the buffersrc (`filter_graph.go:141-155`), if `cfg.HWFramesCtx != nil`, call `ffshim_buffersrc_set_hw_frames(src, cfg.HWFramesCtx)` before `avfilter_graph_config`; add `OutputHWFramesCtx()` returning `BuffersinkGetHWFramesCtx(sink)`.
- `encoder.go`: in the video setup, if `cfg.HWFramesCtx != nil`, set the codec context's `pix_fmt` to `avutil.PixelFormatCUDA` and call the existing `ffshim_codecctx_set_hw_frames_ctx(ctx, av_buffer_ref(cfg.HWFramesCtx))` before `avcodec_open2`; skip the software frame allocation and scaler for that stream (frames arrive ready).

- [ ] **Step 5: Run the tests to verify they pass**

Run: `CGO_ENABLED=0 go test . -run 'TestNVDECScaleCUDANVENC|TestNoCUDADevice' -count=1 -v`
Expected: PASS on the RTX 2070 Max-Q, logging the in-process CPU time. Record it.

- [ ] **Step 6: Falsify**

(a) Remove the drain: the frame-count assertion fails with fewer than 960. (b) Drop `HWFramesCtx` from the encoder config so frames are transferred to system memory: the encoder fails or the CPU time exceeds the bound. Revert both.

- [ ] **Step 7: Commit**

```bash
git add shim/ avfilter/ filter_graph.go encoder.go hwaccel.go gpu_pipeline_test.go
git commit -m "feat: GPU frames from NVDEC through a CUDA filter graph into hevc_nvenc in-process -- buffersrc and encoder take a hardware frame pool, HWDecoder drains at EOF instead of dropping its last frames"
```

**Go/no-go criterion:** the test passes with 960 frames, HEVC Main 10, and in-process CPU time within 4 s (the CLI's 1.0 s plus Go overhead). A failure that cannot be fixed within this task is a no-go.

---

### Task 7: The NVIDIA container toolkit in a distroless glibc image

**Files:**
- Create (fork): `examples/cudaprobe/main.go`
- Create (clustarr, throwaway, scratchpad only): `apko-cudaprobe.yaml`

**Interfaces:**
- Produces: the answer to spec spike item 3 -- whether the toolkit's library setup works in an apko image, and which of the two fallbacks (Wolfi's `ldconfig`, or `LD_LIBRARY_PATH` to the toolkit's mount path) is needed.

- [ ] **Step 1: A probe that loads libcuda the way FFmpeg will**

```go
// cudaprobe loads libcuda.so.1 through purego and calls cuInit and
// cuDeviceGetCount: what FFmpeg's CUDA hwcontext needs first. It prints the
// device count or the step that failed, and exits non-zero on failure.
package main

import (
	"fmt"
	"os"

	"github.com/ebitengine/purego"
)

func main() {
	lib, err := purego.Dlopen("libcuda.so.1", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		fmt.Println("dlopen libcuda.so.1:", err)
		os.Exit(1)
	}
	var cuInit func(uint32) int32
	var cuDeviceGetCount func(*int32) int32
	purego.RegisterLibFunc(&cuInit, lib, "cuInit")
	purego.RegisterLibFunc(&cuDeviceGetCount, lib, "cuDeviceGetCount")
	if rc := cuInit(0); rc != 0 {
		fmt.Println("cuInit:", rc)
		os.Exit(1)
	}
	var n int32
	if rc := cuDeviceGetCount(&n); rc != 0 {
		fmt.Println("cuDeviceGetCount:", rc)
		os.Exit(1)
	}
	fmt.Println("CUDA devices:", n)
}
```

Build: `CGO_ENABLED=0 go build -o /tmp/cudaprobe ./examples/cudaprobe` and run it on the host: expect `CUDA devices: 1`.

- [ ] **Step 2: A throwaway apko image with only glibc and the probe**

```yaml
# apko-cudaprobe.yaml (scratchpad)
contents:
  repositories: [https://packages.wolfi.dev/os]
  keyring: [https://packages.wolfi.dev/os/wolfi-signing.rsa.pub]
  packages: [glibc, ld-linux, wolfi-baselayout]
accounts:
  run-as: 65532
  users: [{username: nonroot, uid: 65532}]
entrypoint:
  command: /cudaprobe
archs: [x86_64]
```

```bash
docker run --rm -v "$PWD":/work -w /work cgr.dev/chainguard/apko build apko-cudaprobe.yaml cudaprobe:spike cudaprobe.tar
docker load < cudaprobe.tar
# add the probe as a layer:
printf 'FROM cudaprobe:spike-amd64\nCOPY cudaprobe /cudaprobe\n' | docker build -t cudaprobe:run -f - /tmp
docker run --rm --gpus all cudaprobe:run
```

Expected: `CUDA devices: 1`. Record whether the toolkit's hook ran `ldconfig` (`docker run --rm --gpus all --entrypoint /usr/sbin/ldconfig cudaprobe:run -p | grep cuda` when Wolfi's glibc ships it).

- [ ] **Step 3: If Step 2 fails, try each fallback and record which works**

1. Add Wolfi's package that provides `ldconfig` (`apk search -o cmd:ldconfig` in `wolfi-base`), rebuild, rerun.
2. Run with `-e LD_LIBRARY_PATH=/usr/lib64:/usr/lib/x86_64-linux-gnu` (the toolkit's mount paths), rerun.

- [ ] **Step 4: Commit the probe**

```bash
git add examples/cudaprobe/
git commit -m "examples: cudaprobe loads libcuda through purego, for checking a distroless image under the NVIDIA container toolkit"
```

---

### Task 8: Record the decision

**Files:**
- Modify (clustarr): `docs/superpowers/specs/2026-09-30-ffgo-transcoding-design.md` (append "Spike result")
- Modify (fork): `README.md` (a "mediactl fork" section listing what the fork adds)

- [ ] **Step 1: Append the result to the spec**

Add a `## Spike result (YYYY-MM-DD)` section: Task 6's CPU time and frame count, Task 7's outcome and the fallback chosen, the list of FFmpeg 9 fixes from Task 4, and the decision -- **go** (Phases 1-5 planned next) or **no-go** (the staged fallback is planned instead), with the reason.

- [ ] **Step 2: Fork README and tag**

Add the fork section to `README.md`, then:

```bash
git add README.md && git commit -m "docs: what the mediactl fork adds"
git push -u origin clustarr/ffmpeg9
git tag -a v0.0.0-clustarr.1 -m "FFmpeg 9 loader and offsets, encoder by name, GPU frames (Phase 0)"
git push origin v0.0.0-clustarr.1
```

- [ ] **Step 3: Commit the spec in clustarr by pathspec**

```bash
cd /home/appkins/src/mediactl/clustarr
git commit -m "docs: the ffgo spike's result" -- docs/superpowers/specs/2026-09-30-ffgo-transcoding-design.md
```

- [ ] **Step 4: Offer the fork's fixes upstream**

Open one pull request per independent fix against `obinnaokechukwu/ffgo` (version sets, shim offsets, encoder by name, GPU frames), each with its test. Only after the owner confirms the pull requests should be opened.
