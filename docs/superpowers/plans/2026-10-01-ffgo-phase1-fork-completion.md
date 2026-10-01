# ffgo Phase 1 (fork completion) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give the `mediactl/ffgo` fork every primitive squasharr's Engine (Phase 3) needs to transcode one file in-process: one demuxer feeding a decoder per stream, GPU or CPU video into HEVC Main 10 with HDR10 metadata kept and HDR10+/Dolby Vision RPU removed, AAC encoding with 7.1→5.1 downmix, and one muxer that takes copied and encoded streams with their language, disposition, attachments, chapters and container metadata. Tag `v0.0.0-clustarr.3`.

**Architecture:** All code is in the fork (`/home/appkins/src/mediactl/ffgo`, module path `github.com/obinnaokechukwu/ffgo`), on `master`, which is what clustarr pins by tag. New FFmpeg struct access goes through the C shim, compiled against the loaded release's headers (Phase 0's rule): new fields are shim-only (no Go fallback offsets), and a call made without a matching shim returns `ErrShimRequired`. Upstream's `Encoder`/`Decoder` stay as they are (strict superset); the Engine gets new packet-level types next to them — `StreamDecoder`, `VideoStreamEncoder`, `AudioEncoder` — and an extended `Muxer`, so it can run each stage on its own goroutine.

**Tech Stack:** Go 1.27 (`CGO_ENABLED=0`), purego, the ffgo C shim (gcc), FFmpeg 9.0.1 shared libraries and headers on the owner's workstation (RTX 2070 Max-Q, driver 610.57.04), the `ffmpeg`/`ffprobe` executables to generate and inspect test clips.

**Spec:** `docs/superpowers/specs/2026-09-30-ffgo-transcoding-design.md` (clustarr): §2 items 3-6, §1 (what the primitives must make possible), and the Spike result. Roadmap: `docs/superpowers/plans/2026-09-30-ffgo-phase0-spike.md` (Phase 1 row).

## Global Constraints

- The fork is a strict superset of ffgo: nothing upstream is removed or narrowed; ffgo's whole suite stays green on FFmpeg 9.0 (CI: `ffmpeg9.yml` BtbN 9.0, Debian 12's 5.1, Ubuntu 24.04's 6.1).
- New FFmpeg struct fields are read through the shim only. A primitive whose FFmpeg API is missing from the loaded release (stream `coded_side_data` before FFmpeg 6.1, `decoded_side_data` before 7.0) returns `ErrNotSupported`, never a wrong value.
- `CGO_ENABLED=0` for every Go binary; the only C is the shim, loaded at run time.
- Licence: the fork stays Apache-2.0.
- Every fix and feature lands with a test seen to fail first; every fix is falsified (reverted, its test fails by name).
- Test clips are generated at test time with the `ffmpeg` executable (skip by name when it is absent), never committed, except the existing `testdata/test.mp4`.
- GPU tests skip by name without a CUDA device (`TestNoCUDADeviceIsAnError`'s pattern), and run for real on the owner's workstation.
- Pushes go to `mediactl/ffgo` only (`master` and tags). No upstream pull request is opened without the owner's confirmation.
- Phase 0's deferred items this plan takes over: §2.3's `av_hwframe_ctx_alloc`/`_init` and a device on a filter graph (Task 5); `EncoderName` not checking `Codec` (Task 5, for the new encoder only); a clear error when the shim is missing (`ErrShimRequired`, every task).

## Review Focus

- **Packets reused by the demuxer:** `Decoder.ReadPacket` returns a packet it overwrites on the next read. Handed to another goroutine, it is overwritten mid-decode. Pinned by Task 3's concurrent read test (packets cloned; frame counts equal `ffprobe -count_frames`).
- **AAC frame size:** the AAC encoder takes exactly 1024 samples per frame except the last; a decoder or resampler hands over other sizes (FLAC 4608, a resampler's variable output). Pinned by Task 4's odd-size input test (no `EINVAL` from `avcodec_send_frame`, duration within one frame).
- **A channel layout that is not the default for its count:** six channels that are `5.1(side)` or `6.0`, eight that are `7.1(wide)`. Resampling by count alone remaps them. Pinned by Task 4's tone-on-one-channel downmix test.
- **HDR10 metadata lost on re-encode:** mastering display and light level must reach the output at stream level (Matroska `Colour`) and in-band for libx265; HDR10+ and DV RPU must be gone from every frame. Pinned by Task 6.
- **Timestamps:** the encoder must keep the frame PTS it is given (upstream's `Encoder` overwrites it with a counter, wrong after a seek and for 24000/1001), and must not drop a frame on `EAGAIN`. Pinned by Task 5's PTS and back-pressure tests.

---

### Task 1: Stream disposition, channel layouts, shim enum values

**Files:**
- Modify: `shim/ffshim.h`, `shim/ffshim.c`
- Modify: `internal/shim/shim.go`
- Create: `streaminfo.go` (ffgo), `streaminfo_test.go`
- Modify: `avutil/avutil.go` (bind `av_channel_layout_from_string`, `av_channel_layout_uninit`)
- Modify: `errors.go` (`ErrShimRequired`, `ErrNotSupported`)

**Interfaces:**
- Produces:
  - `var ErrShimRequired = errors.New("ffgo: this needs the C shim built against the loaded FFmpeg")`, `var ErrNotSupported = errors.New("ffgo: not supported by the loaded FFmpeg release")`
  - `type Disposition int32` with `DispositionDefault=0x1, DispositionDub=0x2, DispositionOriginal=0x4, DispositionComment=0x8, DispositionLyrics=0x10, DispositionKaraoke=0x20, DispositionForced=0x40, DispositionHearingImpaired=0x80, DispositionVisualImpaired=0x100, DispositionCleanEffects=0x200, DispositionAttachedPic=0x400, DispositionCaptions=0x10000, DispositionDescriptions=0x20000, DispositionMetadata=0x40000` (FFmpeg's `AV_DISPOSITION_*`, checked against the shim's enum table in the test)
  - `func (d *Decoder) Streams() []*StreamInfo` — every stream, in index order
  - `StreamInfo` gains `Disposition Disposition`, `Language string`, `Title string`, `ChannelLayout string` (audio: `av_channel_layout_describe`, e.g. `"5.1(side)"`), `Metadata Metadata`
  - `func shim.EnumValue(name string) (int, bool)` — enum values compiled from the loaded headers, for `"AV_DISPOSITION_*"`, `"AV_FRAME_DATA_*"`, `"AV_PKT_DATA_*"` names this plan uses
  - `func shim.StreamDisposition(st unsafe.Pointer) int32`, `func shim.SetStreamDisposition(st unsafe.Pointer, d int32)`
  - `func shim.CodecParChLayoutDescribe(par unsafe.Pointer) string`, `func shim.FrameChLayoutDescribe(frame unsafe.Pointer) string`

- [ ] **Step 1: Write the failing tests**

```go
// streaminfo_test.go
func TestStreamsReportDispositionLanguageAndLayout(t *testing.T) {
	ffmpegOrSkip(t)
	path := filepath.Join(t.TempDir(), "in.mkv")
	run(t, "ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=128x72:rate=25:duration=1",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=1,aformat=channel_layouts=5.1(side)",
		"-f", "lavfi", "-i", "sine=frequency=880:duration=1,aformat=channel_layouts=7.1",
		"-map", "0", "-map", "1", "-map", "2", "-c:v", "libx264", "-c:a", "flac",
		"-metadata:s:a:0", "language=eng", "-metadata:s:a:1", "language=fre",
		"-metadata:s:a:1", "title=Commentary",
		"-disposition:a:0", "default", "-disposition:a:1", "comment", path)
	d, err := NewDecoder(path)
	require.NoError(t, err)
	defer d.Close()
	s := d.Streams()
	require.Len(t, s, 3)
	assert.Equal(t, "eng", s[1].Language)
	assert.Equal(t, "5.1(side)", s[1].ChannelLayout)
	assert.Equal(t, DispositionDefault, s[1].Disposition&DispositionDefault)
	assert.Equal(t, "fre", s[2].Language)
	assert.Equal(t, "Commentary", s[2].Title)
	assert.Equal(t, "7.1", s[2].ChannelLayout)
	assert.Equal(t, DispositionComment, s[2].Disposition&DispositionComment)
}

func TestDispositionConstantsMatchTheHeaders(t *testing.T) {
	requireShim(t)
	for name, v := range map[string]Disposition{
		"AV_DISPOSITION_DEFAULT": DispositionDefault, "AV_DISPOSITION_COMMENT": DispositionComment,
		"AV_DISPOSITION_FORCED": DispositionForced, "AV_DISPOSITION_HEARING_IMPAIRED": DispositionHearingImpaired,
		"AV_DISPOSITION_VISUAL_IMPAIRED": DispositionVisualImpaired, "AV_DISPOSITION_ORIGINAL": DispositionOriginal,
		"AV_DISPOSITION_DESCRIPTIONS": DispositionDescriptions, "AV_DISPOSITION_CAPTIONS": DispositionCaptions,
	} {
		got, ok := shim.EnumValue(name)
		require.True(t, ok, name)
		assert.Equal(t, int(v), got, name)
	}
}
```

`ffmpegOrSkip`, `run` and `requireShim` go in a new `helpers_test.go` (package ffgo): `ffmpegOrSkip` skips naming the missing executable; `run` fails the test with the command's combined output; `requireShim` skips unless `layout.ShimLoaded()`.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test -run 'TestStreamsReport|TestDispositionConstants' -count=1 .`
Expected: FAIL to compile — `d.Streams undefined`, `DispositionDefault undefined`, `shim.EnumValue undefined`.

- [ ] **Step 3: Implement**

`shim/ffshim.c` (declarations added to `ffshim.h`):

```c
#include <libavutil/channel_layout.h>

struct ffshim_enum { const char *name; int value; };
#define FFSHIM_ENUM(e) { #e, (int)(e) }
static const struct ffshim_enum ffshim_enums[] = {
    FFSHIM_ENUM(AV_DISPOSITION_DEFAULT), FFSHIM_ENUM(AV_DISPOSITION_DUB),
    FFSHIM_ENUM(AV_DISPOSITION_ORIGINAL), FFSHIM_ENUM(AV_DISPOSITION_COMMENT),
    FFSHIM_ENUM(AV_DISPOSITION_LYRICS), FFSHIM_ENUM(AV_DISPOSITION_KARAOKE),
    FFSHIM_ENUM(AV_DISPOSITION_FORCED), FFSHIM_ENUM(AV_DISPOSITION_HEARING_IMPAIRED),
    FFSHIM_ENUM(AV_DISPOSITION_VISUAL_IMPAIRED), FFSHIM_ENUM(AV_DISPOSITION_CLEAN_EFFECTS),
    FFSHIM_ENUM(AV_DISPOSITION_ATTACHED_PIC), FFSHIM_ENUM(AV_DISPOSITION_CAPTIONS),
    FFSHIM_ENUM(AV_DISPOSITION_DESCRIPTIONS), FFSHIM_ENUM(AV_DISPOSITION_METADATA),
    FFSHIM_ENUM(AV_FRAME_DATA_MASTERING_DISPLAY_METADATA),
    FFSHIM_ENUM(AV_FRAME_DATA_CONTENT_LIGHT_LEVEL),
    FFSHIM_ENUM(AV_FRAME_DATA_DYNAMIC_HDR_PLUS),
    FFSHIM_ENUM(AV_FRAME_DATA_DOVI_RPU_BUFFER),
    FFSHIM_ENUM(AV_FRAME_DATA_DOVI_METADATA),
    FFSHIM_ENUM(AV_PKT_DATA_MASTERING_DISPLAY_METADATA),
    FFSHIM_ENUM(AV_PKT_DATA_CONTENT_LIGHT_LEVEL),
    FFSHIM_ENUM(AV_PKT_DATA_DOVI_CONF),
    FFSHIM_ENUM(AV_PKT_DATA_DYNAMIC_HDR10_PLUS),
};
int ffshim_enum_value(const char *name, int *out) {
    for (size_t i = 0; i < sizeof ffshim_enums / sizeof ffshim_enums[0]; i++)
        if (strcmp(ffshim_enums[i].name, name) == 0) { *out = ffshim_enums[i].value; return 0; }
    return -1;
}

int  ffshim_stream_disposition(void *st) { return ((AVStream *)st)->disposition; }
void ffshim_stream_set_disposition(void *st, int d) { ((AVStream *)st)->disposition = d; }

int ffshim_codecpar_ch_layout_describe(void *par, char *buf, size_t size) {
    return av_channel_layout_describe(&((AVCodecParameters *)par)->ch_layout, buf, size);
}
int ffshim_frame_ch_layout_describe(void *frame, char *buf, size_t size) {
    return av_channel_layout_describe(&((AVFrame *)frame)->ch_layout, buf, size);
}
```

(`AV_PKT_DATA_DYNAMIC_HDR10_PLUS` exists from FFmpeg 7.0; guard its line with `#if LIBAVCODEC_VERSION_MAJOR >= 61`, and `AV_FRAME_DATA_DOVI_*` with `#if LIBAVUTIL_VERSION_MAJOR >= 57`.)

`internal/shim/shim.go`: register the five functions with `registerOptionalLibFunc` and wrap them; the describe wrappers allocate a 64-byte buffer, return `""` on a negative result. `EnumValue` returns `(0, false)` when the shim is not loaded or the symbol is missing.

`streaminfo.go`: `Decoder.Streams()` builds every stream with `getStreamInfo`, then fills the new fields: `Disposition` from `shim.StreamDisposition` (0 without a shim), `Metadata` from `getMetadataFromDict(avformat.GetStreamMetadata(stream))`, `Language = Metadata["language"]`, `Title = Metadata["title"]`, `ChannelLayout` from `shim.CodecParChLayoutDescribe` for audio. `getStreamInfo` itself is unchanged so upstream callers see the same values.

- [ ] **Step 4: Run them to verify they pass, then falsify**

Run: `go test -run 'TestStreamsReport|TestDispositionConstants' -count=1 -v .`
Expected: PASS. Then make `ffshim_stream_disposition` return 0, rebuild the shim (`cd shim && ./build.sh`), and see `TestStreamsReportDispositionLanguageAndLayout` fail on the disposition asserts; restore.

- [ ] **Step 5: Commit**

```bash
git add shim/ffshim.h shim/ffshim.c internal/shim/shim.go streaminfo.go streaminfo_test.go helpers_test.go avutil/avutil.go errors.go
git commit -m "feat: stream disposition, language, title and channel layout for every stream; header enum values through the shim"
```

---

### Task 2: Side data on frames, streams and encoders

**Files:**
- Modify: `shim/ffshim.h`, `shim/ffshim.c`, `internal/shim/shim.go`
- Create: `sidedata.go`, `sidedata_test.go`
- Modify: `avutil/avutil.go` (bind `av_frame_get_side_data`, `av_frame_remove_side_data`, `av_frame_new_side_data`)

**Interfaces:**
- Produces:
  - `type FrameSideDataType int32`: `func FrameSideMasteringDisplay() FrameSideDataType`, `FrameSideContentLightLevel()`, `FrameSideHDRPlus()`, `FrameSideDOVIRPU()`, `FrameSideDOVIMetadata()` — functions resolving through `shim.EnumValue` (enum values are read from the loaded headers, as pixel formats are by name)
  - `type PacketSideDataType int32`: `PacketSideMasteringDisplay()`, `PacketSideContentLightLevel()`, `PacketSideDOVIConf()`, `PacketSideHDR10Plus()`
  - `func (f Frame) SideData(t FrameSideDataType) ([]byte, bool)` (a copy)
  - `func (f Frame) RemoveSideData(t ...FrameSideDataType)`
  - `func (f Frame) AddSideData(t FrameSideDataType, data []byte) error`
  - `func StreamSideData(par avcodec.Parameters, t PacketSideDataType) ([]byte, bool)` and `func SetStreamSideData(par avcodec.Parameters, t PacketSideDataType, data []byte) error` (`ErrNotSupported` before FFmpeg 6.1)
  - `func addDecodedSideData(ctx avcodec.Context, t FrameSideDataType, data []byte) error` (package-private; Task 5's encoder uses it; `ErrNotSupported` before FFmpeg 7.0)
  - The mastering-display and light-level payloads are FFmpeg's own structs (`AVMasteringDisplayMetadata`, `AVContentLightMetadata`), passed through as bytes; the frame and packet side data of each carry the same struct, so a frame's bytes are valid stream side data.

- [ ] **Step 1: Write the failing tests**

```go
// sidedata_test.go
// hdr10Clip makes a 10-bit HEVC clip whose SEI carries mastering display and
// light level (libx265 x265-params), in Matroska.
func hdr10Clip(t *testing.T) string {
	ffmpegOrSkip(t)
	path := filepath.Join(t.TempDir(), "hdr10.mkv")
	run(t, "ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=256x144:rate=24:duration=1",
		"-pix_fmt", "yuv420p10le", "-c:v", "libx265",
		"-x265-params", "log-level=error:hdr10=1:repeat-headers=1:colorprim=bt2020:transfer=smpte2084:colormatrix=bt2020nc:"+
			"master-display=G(13250,34500)B(7500,3000)R(34000,16000)WP(15635,16450)L(10000000,1):max-cll=1000,400",
		path)
	return path
}

func TestDecodedFramesCarryMasteringDisplayAndLightLevel(t *testing.T) {
	requireShim(t)
	d, err := NewDecoder(hdr10Clip(t))
	require.NoError(t, err)
	defer d.Close()
	f, err := d.DecodeVideo()
	require.NoError(t, err)
	md, ok := f.SideData(FrameSideMasteringDisplay())
	require.True(t, ok, "mastering display side data")
	assert.NotEmpty(t, md)
	cll, ok := f.SideData(FrameSideContentLightLevel())
	require.True(t, ok, "light level side data")
	// AVContentLightMetadata is two unsigned ints: MaxCLL, MaxFALL.
	require.Len(t, cll, 8)
	assert.Equal(t, uint32(1000), binary.LittleEndian.Uint32(cll[0:4]))
	assert.Equal(t, uint32(400), binary.LittleEndian.Uint32(cll[4:8]))
}

func TestRemoveSideDataDropsOnlyTheNamedTypes(t *testing.T) {
	requireShim(t)
	f, err := NewVideoFrame(64, 64, PixelFormatYUV420P)
	require.NoError(t, err)
	defer f.Free()
	require.NoError(t, f.AddSideData(FrameSideHDRPlus(), []byte{1, 2, 3}))
	require.NoError(t, f.AddSideData(FrameSideContentLightLevel(), make([]byte, 8)))
	f.RemoveSideData(FrameSideHDRPlus(), FrameSideDOVIRPU(), FrameSideDOVIMetadata())
	_, ok := f.SideData(FrameSideHDRPlus())
	assert.False(t, ok)
	_, ok = f.SideData(FrameSideContentLightLevel())
	assert.True(t, ok)
}

func TestStreamSideDataRoundTrip(t *testing.T) {
	requireShim(t)
	par := avcodec.ParametersAlloc()
	defer avcodec.ParametersFree(&par)
	want := []byte{0xe8, 3, 0, 0, 0x90, 1, 0, 0}
	require.NoError(t, SetStreamSideData(par, PacketSideContentLightLevel(), want))
	got, ok := StreamSideData(par, PacketSideContentLightLevel())
	require.True(t, ok)
	assert.Equal(t, want, got)
	require.NoError(t, SetStreamSideData(par, PacketSideContentLightLevel(), want), "replacing keeps one entry")
}
```

(`NewVideoFrame` is upstream's frame allocator if present under that name; otherwise use the allocator `pool.go` uses — check with `grep -n 'func New.*Frame' *.go` before writing the test and use the real name.)

- [ ] **Step 2: Run them to verify they fail**

Run: `go test -run 'TestDecodedFramesCarry|TestRemoveSideData|TestStreamSideData' -count=1 .`
Expected: FAIL to compile — `FrameSideMasteringDisplay undefined`, `f.SideData undefined`, `SetStreamSideData undefined`.

- [ ] **Step 3: Implement**

Shim additions (declared in `ffshim.h`):

```c
#include <libavutil/frame.h>
#include <libavcodec/packet.h>

void *ffshim_frame_side_data(void *frame, int type, size_t *size) {
    AVFrameSideData *sd = av_frame_get_side_data((AVFrame *)frame, (enum AVFrameSideDataType)type);
    if (!sd) return NULL;
    *size = sd->size;
    return sd->data;
}

int ffshim_codecpar_side_data_get(void *par, int type, void **data, size_t *size) {
#if LIBAVCODEC_VERSION_INT >= AV_VERSION_INT(60, 31, 100)
    AVCodecParameters *p = par;
    const AVPacketSideData *sd = av_packet_side_data_get(p->coded_side_data, p->nb_coded_side_data,
                                                         (enum AVPacketSideDataType)type);
    if (!sd) return -1;
    *data = sd->data; *size = sd->size;
    return 0;
#else
    return AVERROR(ENOSYS);
#endif
}

int ffshim_codecpar_side_data_set(void *par, int type, const void *data, size_t size) {
#if LIBAVCODEC_VERSION_INT >= AV_VERSION_INT(60, 31, 100)
    AVCodecParameters *p = par;
    AVPacketSideData *sd = av_packet_side_data_new(&p->coded_side_data, &p->nb_coded_side_data,
                                                   (enum AVPacketSideDataType)type, size, 0);
    if (!sd) return AVERROR(ENOMEM);
    memcpy(sd->data, data, size);
    return 0;
#else
    return AVERROR(ENOSYS);
#endif
}

int ffshim_codecctx_add_decoded_side_data(void *ctx, int type, const void *data, size_t size) {
#if LIBAVCODEC_VERSION_MAJOR >= 61
    AVCodecContext *c = ctx;
    AVFrameSideData *sd = av_frame_side_data_new(&c->decoded_side_data, &c->nb_decoded_side_data,
                                                 (enum AVFrameSideDataType)type, size,
                                                 AV_FRAME_SIDE_DATA_FLAG_REPLACE);
    if (!sd) return AVERROR(ENOMEM);
    memcpy(sd->data, data, size);
    return 0;
#else
    return AVERROR(ENOSYS);
#endif
}
```

(`AV_VERSION_INT(60, 31, 100)` is the libavcodec that added `coded_side_data` to `AVCodecParameters`, FFmpeg 6.1. `av_packet_side_data_new` replaces an existing entry of the same type, so `SetStreamSideData` keeps one entry; the round-trip test's second call checks it compiles to one entry by counting with `ffshim_codecpar_side_data_get` — the test asserts the value, and Step 4's falsification checks a duplicate would be caught by reading `nb_coded_side_data` through a one-line test helper `ffshim_codecpar_nb_side_data`.)

`sidedata.go`: the type functions resolve once with `sync.OnceValue` and panic with a clear message if the shim does not know the name (a shim from this commit on always does). `Frame.SideData` copies `size` bytes from the shim pointer into a new slice. `RemoveSideData` calls the bound `av_frame_remove_side_data` per type. `AddSideData` calls `av_frame_new_side_data` (returns `AVFrameSideData*`) then the shim's `ffshim_frame_side_data` to get the data pointer and copies in. `StreamSideData`/`SetStreamSideData` return `ErrShimRequired` without a shim and map `AVERROR(ENOSYS)` to `ErrNotSupported`.

- [ ] **Step 4: Run them to verify they pass, then falsify**

Run: `go test -run 'TestDecodedFramesCarry|TestRemoveSideData|TestStreamSideData' -count=1 -v .`
Expected: PASS. Falsify: make `RemoveSideData` a no-op → `TestRemoveSideDataDropsOnlyTheNamedTypes` fails; restore.

- [ ] **Step 5: Commit**

```bash
git add shim/ffshim.h shim/ffshim.c internal/shim/shim.go sidedata.go sidedata_test.go avutil/avutil.go
git commit -m "feat: frame, stream and encoder side data -- mastering display and light level read and written, HDR10+ and Dolby Vision RPU removable per frame"
```

---

### Task 3: One demuxer, a decoder per stream

**Files:**
- Create: `streamdecoder.go`, `streamdecoder_test.go`
- Modify: `ffgo.go` (`Packet.Clone`), `avcodec/avcodec.go` (bind `av_packet_clone` if not bound)

**Interfaces:**
- Consumes: `Decoder.Streams()` (Task 1); `HWDevice` (upstream); `avutil.OptSetInt` for `extra_hw_frames` (as `NewHWDecoder` does).
- Produces:
  - `func (p *Packet) Clone() (*Packet, error)` — an owned reference (`av_packet_clone`) the caller frees; safe to send to another goroutine
  - `type StreamDecoderConfig struct { HWDevice *HWDevice; ExtraHWFrames int; Threads int }`
  - `func (d *Decoder) NewStreamDecoder(streamIndex int, cfg *StreamDecoderConfig) (*StreamDecoder, error)` — any audio or video stream of the demuxer; with `HWDevice`, frames stay on the GPU (no transfer)
  - `func (s *StreamDecoder) Send(p *Packet) error` (nil sends end of stream)
  - `func (s *StreamDecoder) Receive() (Frame, error)` — `ErrAgain` when it needs input, `io.EOF` once drained; the returned frame is the decoder's own and valid until the next `Receive`; `Frame.Clone()` (upstream `av_frame_clone` wrapper; add it if absent) to keep it
  - `func (s *StreamDecoder) TimeBase() Rational`, `func (s *StreamDecoder) HWFramesCtx() avutil.HWFramesContext` (nil before the first GPU frame), `func (s *StreamDecoder) Close() error`
  - `var ErrAgain = errors.New("ffgo: needs more input")`; `errors.Is(err, ErrAgain)` also holds for an `*avutil.Error` with `AVERROR(EAGAIN)` (extend `Error.Is` as Phase 0 did for EOF)

- [ ] **Step 1: Write the failing tests**

```go
// streamdecoder_test.go
func TestOneDemuxerDecodesEveryStreamOnItsOwnGoroutine(t *testing.T) {
	ffmpegOrSkip(t)
	path := filepath.Join(t.TempDir(), "multi.mkv")
	run(t, "ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=160x90:rate=25:duration=3",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=3",
		"-f", "lavfi", "-i", "sine=frequency=880:duration=3,aformat=channel_layouts=5.1",
		"-map", "0", "-map", "1", "-map", "2", "-c:v", "libx264", "-c:a:0", "aac", "-c:a:1", "flac", path)
	d, err := NewDecoder(path)
	require.NoError(t, err)
	defer d.Close()

	chans := map[int]chan *Packet{}
	counts := make([]atomic.Int64, len(d.Streams()))
	var wg sync.WaitGroup
	for _, s := range d.Streams() {
		sd, err := d.NewStreamDecoder(s.Index, nil)
		require.NoError(t, err)
		ch := make(chan *Packet, 4)
		chans[s.Index] = ch
		wg.Add(1)
		go func(idx int, sd *StreamDecoder, ch chan *Packet) {
			defer wg.Done()
			defer sd.Close()
			drain := func() {
				for {
					f, err := sd.Receive()
					if errors.Is(err, ErrAgain) || errors.Is(err, io.EOF) {
						return
					}
					require.NoError(t, err)
					_ = f
					counts[idx].Add(1)
				}
			}
			for p := range ch {
				require.NoError(t, sd.Send(p))
				p.Free()
				drain()
			}
			require.NoError(t, sd.Send(nil))
			drain()
		}(s.Index, sd, ch)
	}
	for {
		p, err := d.ReadPacket()
		require.NoError(t, err)
		if p == nil {
			break
		}
		c, err := p.Clone()
		require.NoError(t, err)
		chans[p.StreamIndex()] <- c
	}
	for _, ch := range chans {
		close(ch)
	}
	wg.Wait()
	for i := range counts {
		assert.Equal(t, ffprobeFrameCount(t, path, i), counts[i].Load(), "stream %d", i)
	}
}

func TestStreamDecoderKeepsNVDECFramesOnTheGPU(t *testing.T) {
	dev := cudaOrSkip(t) // NewHWDevice(HWDeviceTypeCUDA, ""); skips naming the error
	d, err := NewDecoder(h264Clip(t, 48)) // 2 s at 24 fps, from gpu_pipeline_test.go's generator
	require.NoError(t, err)
	defer d.Close()
	sd, err := d.NewStreamDecoder(d.VideoStream().Index, &StreamDecoderConfig{HWDevice: dev, ExtraHWFrames: 8})
	require.NoError(t, err)
	defer sd.Close()
	n := 0
	for {
		p, err := d.ReadPacket()
		require.NoError(t, err)
		if p == nil {
			require.NoError(t, sd.Send(nil))
		} else if p.StreamIndex() == d.VideoStream().Index {
			require.NoError(t, sd.Send(p))
		} else {
			continue
		}
		for {
			f, err := sd.Receive()
			if errors.Is(err, ErrAgain) || errors.Is(err, io.EOF) {
				break
			}
			require.NoError(t, err)
			assert.Equal(t, PixelFormatCUDA(), PixelFormat(f.Format()))
			n++
		}
		if p == nil {
			break
		}
	}
	assert.Equal(t, 48, n)
	assert.NotNil(t, sd.HWFramesCtx())
}
```

`ffprobeFrameCount(t, path, index) int64` (helpers_test.go) runs `ffprobe -v error -count_frames -select_streams <index> -show_entries stream=nb_read_frames -of csv=p=0`. `cudaOrSkip` and `h264Clip` move into `helpers_test.go` from `gpu_pipeline_test.go` if they exist there under other names; reuse, do not duplicate.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test -run 'TestOneDemuxer|TestStreamDecoderKeeps' -count=1 .`
Expected: FAIL to compile — `d.NewStreamDecoder undefined`, `p.Clone undefined`, `ErrAgain undefined`.

- [ ] **Step 3: Implement**

`streamdecoder.go`: `NewStreamDecoder` locks `d.mu`, finds the stream's codec parameters, `avcodec.FindDecoder`, `AllocContext3`, `ParametersToContext`, sets `pkt_timebase` to the stream's time base (`avutil.OptSetQ(ctx, "pkt_timebase", tb)` — bind `av_opt_set_q` if absent, or use the shim's time-base setter), `threads` when `cfg.Threads > 0`, and for `cfg.HWDevice`: `avcodec.SetCtxHWDeviceCtx` and `extra_hw_frames` exactly as `NewHWDecoder` does; `Open2`; allocate one frame. `Send(nil)` sends a nil packet (`avcodec.SendPacket(ctx, nil)`). `Receive` calls `avutil.FrameUnref` then `avcodec.ReceiveFrame`; maps `AVERROR(EAGAIN)` to `ErrAgain` and `AVERROR_EOF` to `io.EOF`. `HWFramesCtx` reads `shim.FrameHWFramesCtx` of the last frame (nil without one). `Close` frees frame and context; it does not touch the demuxer.

`Packet.Clone`: `av_packet_clone(p.ptr)`; `&Packet{ptr: c, owned: true}`; `ErrNoMemory`-style error on NULL.

- [ ] **Step 4: Run them to verify they pass, then falsify**

Run: `go test -run 'TestOneDemuxer|TestStreamDecoderKeeps' -count=1 -race -v .`
Expected: PASS (GPU test runs on the workstation; `-race` clean). Falsify: send `p` itself instead of `c` in the test's read loop → frame counts diverge or the race detector fires; restore. (This pins the Review Focus "packets reused by the demuxer" in the API's documentation: `ReadPacket`'s doc comment gains "valid until the next ReadPacket; Clone it to keep it".)

- [ ] **Step 5: Commit**

```bash
git add streamdecoder.go streamdecoder_test.go ffgo.go avcodec/avcodec.go errors.go helpers_test.go gpu_pipeline_test.go
git commit -m "feat: StreamDecoder -- a decoder per stream of one demuxer, GPU frames kept on the device; Packet.Clone for handing packets across goroutines"
```

---

### Task 4: AAC encoding and channel-layout-correct resampling

**Files:**
- Modify: `avutil/avutil.go` (bind `av_audio_fifo_alloc`, `av_audio_fifo_write`, `av_audio_fifo_read`, `av_audio_fifo_size`, `av_audio_fifo_free`)
- Modify: `resampler.go` (`AudioFormat.Layout`)
- Create: `audioencoder.go`, `audioencoder_test.go`

**Interfaces:**
- Consumes: `shim.FrameChLayoutDescribe` (Task 1); `avutil.ChannelLayoutFromString` (Task 1).
- Produces:
  - `AudioFormat` gains `Layout string` — an FFmpeg layout name (`"7.1"`, `"5.1(side)"`, `"stereo"`); when set it is used through `av_channel_layout_from_string` instead of the default layout for `Channels`, on both sides of the `Resampler`
  - `func (f Frame) ChannelLayout() string` (shim; `""` without one)
  - `type AudioEncoderConfig2 struct { EncoderName string /* default "aac" */; SampleRate int; Layout string; BitRate int64; GlobalHeader bool }` — named `AudioEncoderConfig2` because upstream's `AudioEncoderConfig` is taken; doc comment says it is the packet-level encoder's config
  - `func NewAudioEncoder(cfg AudioEncoderConfig2) (*AudioEncoder, error)`
  - `func (e *AudioEncoder) Encode(f Frame, emit func(*Packet) error) error` — any number of samples per call; buffered through an audio FIFO into the encoder's frame size
  - `func (e *AudioEncoder) Flush(emit func(*Packet) error) error` — the FIFO's remainder as a short last frame, then the encoder drained
  - `func (e *AudioEncoder) Parameters() (avcodec.Parameters, error)` — a fresh `AVCodecParameters` from the context (caller frees), `func (e *AudioEncoder) TimeBase() Rational` (1/sample rate), `func (e *AudioEncoder) FrameSize() int`, `func (e *AudioEncoder) Close() error`
  - PTS: the first input frame's PTS, rescaled from its time base (`Encode` takes it from the frame's `time_base` field when set, else from `cfg`-less 1/sample-rate) to 1/sample rate, then advanced by samples; packets carry PTS/DTS in `TimeBase()`

- [ ] **Step 1: Write the failing tests**

```go
// audioencoder_test.go
// toneOn makes a 1 s 48 kHz 7.1 FLAC whose only non-silent channel is `ch`.
func toneOn(t *testing.T, ch string) string {
	ffmpegOrSkip(t)
	path := filepath.Join(t.TempDir(), "tone71.mkv")
	run(t, "ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "sine=frequency=1000:sample_rate=48000:duration=1",
		"-af", "pan=7.1|"+ch+"=c0", "-c:a", "flac", path)
	return path
}

func TestSevenOneSideChannelsFoldIntoFiveOneSurrounds(t *testing.T) {
	out := transcodeAudio(t, toneOn(t, "SL"), "5.1", 384000) // decode → Resampler(Layout 5.1) → AudioEncoder → Muxer.AddEncoderStream
	levels := channelRMSdB(t, out)                           // ffmpeg astats per channel, by layout name
	assert.Equal(t, "5.1", ffprobeLayout(t, out))
	assert.Greater(t, levels["BL"], -20.0, "SL folds into BL")
	assert.Less(t, levels["FL"], -60.0, "nothing leaks to FL")
	assert.Less(t, levels["FC"], -60.0)
}

func TestSixChannelSideLayoutIsNotRemappedByCount(t *testing.T) {
	ffmpegOrSkip(t)
	src := filepath.Join(t.TempDir(), "side.mkv")
	run(t, "ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "sine=frequency=1000:sample_rate=48000:duration=1",
		"-af", "pan=5.1(side)|SR=c0", "-c:a", "flac", src)
	out := transcodeAudio(t, src, "5.1", 384000)
	levels := channelRMSdB(t, out)
	assert.Greater(t, levels["BR"], -20.0, "SR lands in BR, the 5.1 surround right")
	assert.Less(t, levels["FR"], -60.0)
}

func TestAACEncoderTakesAnyFrameSize(t *testing.T) {
	enc, err := NewAudioEncoder(AudioEncoderConfig2{SampleRate: 48000, Layout: "stereo", BitRate: 128000})
	require.NoError(t, err)
	defer enc.Close()
	require.Equal(t, 1024, enc.FrameSize())
	var samples int64
	emit := func(p *Packet) error { samples += 1024; return nil }
	for _, n := range []int{4608, 333, 1, 2047, 4608} { // FLAC block, odd, tiny
		f := silentFrame(t, 48000, "stereo", n)
		require.NoError(t, enc.Encode(f, emit))
		f.Free()
	}
	require.NoError(t, enc.Flush(emit))
	assert.InDelta(t, 4608+333+1+2047+4608, samples, 2*1024)
}
```

`transcodeAudio`, `channelRMSdB` (runs `ffmpeg -i out -af astats=metadata=1:reset=0 -f null -` and parses `Channel: N` / `RMS level dB` per channel, mapping N to the layout's channel names), `ffprobeLayout` and `silentFrame` go in `audioencoder_test.go`. `transcodeAudio` uses `StreamDecoder` (Task 3) and `Muxer.AddEncoderStream` — which Task 6 adds; so in this task it writes through a minimal local muxing helper built on upstream's `Muxer.AddCopyStream` with the encoder's parameters, replaced by `AddEncoderStream` in Task 6 (one-line change in the helper).

- [ ] **Step 2: Run them to verify they fail**

Run: `go test -run 'TestSevenOne|TestSixChannel|TestAACEncoder' -count=1 .`
Expected: FAIL to compile — `NewAudioEncoder undefined`, `AudioFormat.Layout undefined`.

- [ ] **Step 3: Implement**

`resampler.go`: when `src.Layout`/`dst.Layout` is set, fill the 64-byte layout buffers with `avutil.ChannelLayoutFromString(buf, name)` instead of `ChannelLayoutDefault`, and set `Channels` from the parsed layout (`shim` helper `ffshim_ch_layout_nb_channels(buf)`). The legacy bitmask path is unchanged (it predates `Layout` and is never taken on FFmpeg 5.1+).

`audioencoder.go`: `NewAudioEncoder` finds the encoder by name (`aac` default), sets sample rate, `sample_fmt` to the codec's first supported format (`fltp` for `aac`), channel layout from `Layout` through a shim setter `ffshim_codecctx_set_ch_layout_from_string(ctx, name)` (added here), bit rate, time base 1/sample rate, `AV_CODEC_FLAG_GLOBAL_HEADER` when asked; opens; reads `frame_size`; allocates an `AVAudioFifo` for the format and channel count, and one output frame of `frame_size` samples (`av_frame_get_buffer`). `Encode` writes the frame's samples into the FIFO, then while `fifo_size >= frame_size` reads a frame, stamps `pts = next; next += frame_size`, sends, and drains packets to `emit` (receive loop until `EAGAIN`). A send that returns `EAGAIN` drains first and resends (never drops). `Flush` sends the remainder as a short frame (`nb_samples` = remainder), then `SendFrame(nil)` and drains to `EOF`.

- [ ] **Step 4: Run them to verify they pass, then falsify**

Run: `go test -run 'TestSevenOne|TestSixChannel|TestAACEncoder' -count=1 -v .`
Expected: PASS. Falsify: drop the `Layout` branch in `NewResampler` (default-by-count) → `TestSixChannelSideLayoutIsNotRemappedByCount` fails (SR mapped by position, not by name); restore. Bypass the FIFO (send input frames directly) → `TestAACEncoderTakesAnyFrameSize` fails with `EINVAL`; restore.

- [ ] **Step 5: Commit**

```bash
git add avutil/avutil.go resampler.go audioencoder.go audioencoder_test.go shim/ffshim.h shim/ffshim.c internal/shim/shim.go
git commit -m "feat: AudioEncoder (AAC through an audio FIFO, PTS kept) and resampling by channel-layout name, so 7.1 and 5.1(side) downmix by position name, not count"
```

---

### Task 5: Packet-level video encoder, GPU frame pools and filter-graph devices

**Files:**
- Create: `videoencoder.go`, `videoencoder_test.go`, `hwframes.go`, `hwframes_test.go`
- Modify: `filter_graph.go` (`FilterGraphConfig.HWDevice`), `avutil/avutil.go` (bind `av_hwframe_ctx_alloc`, `av_hwframe_ctx_init`), `shim/ffshim.{h,c}` (frames-context field setters; filter `hw_device_ctx`), `internal/shim/shim.go`

**Interfaces:**
- Consumes: `applyVideoOptions(ctx, *VideoEncoderConfig)` and the hardware-frames setup in `encoder.go` (Phase 0); `addDecodedSideData` (Task 2).
- Produces:
  - `type HWFramesConfig struct { Device *HWDevice; Format PixelFormat /* e.g. PixelFormatCUDA() */; SWFormat PixelFormat /* e.g. PixelFormatP010LE() */; Width, Height, PoolSize int }`
  - `func NewHWFrames(cfg HWFramesConfig) (avutil.HWFramesContext, error)` — `av_hwframe_ctx_alloc` + fields through the shim + `av_hwframe_ctx_init`; the caller unrefs
  - `FilterGraphConfig` gains `HWDevice *HWDevice` — set on every filter of the graph before `avfilter_graph_config` (`filter->hw_device_ctx`, through the shim), so `hwupload`, `scale_cuda`, `vpp_qsv` and `scale_vaapi` can be fed software frames or create their own pools
  - `type VideoStreamEncoderConfig struct { VideoEncoderConfig; TimeBase Rational; SideData map[FrameSideDataType][]byte; GlobalHeader bool }`
  - `func NewVideoStreamEncoder(cfg VideoStreamEncoderConfig) (*VideoStreamEncoder, error)` — `EncoderName` must encode `Codec` when both are set (else `ErrEncoderCodecMismatch`); `SideData` goes to `decoded_side_data` before open; `TimeBase` is the encoder's time base (the filter graph's output time base)
  - `func (e *VideoStreamEncoder) Encode(f Frame, emit func(*Packet) error) error` — keeps `f`'s PTS; `EAGAIN` on send drains then resends
  - `func (e *VideoStreamEncoder) Flush(emit func(*Packet) error) error`, `Parameters() (avcodec.Parameters, error)`, `TimeBase() Rational`, `Close() error`

- [ ] **Step 1: Write the failing tests**

```go
// videoencoder_test.go
func TestVideoStreamEncoderKeepsFramePTS(t *testing.T) {
	enc := x265Encoder(t, NewRational(1001, 24000)) // libx265, yuv420p10le, 64x64, time base 1001/24000
	var pts []int64
	for i := int64(0); i < 10; i++ {
		f := grayFrame(t, 64, 64, PixelFormatYUV420P10LE())
		f.SetPTS(1000 + i) // starts after a seek
		require.NoError(t, enc.Encode(f, func(p *Packet) error { pts = append(pts, p.PTS()); return nil }))
		f.Free()
	}
	require.NoError(t, enc.Flush(func(p *Packet) error { pts = append(pts, p.PTS()); return nil }))
	slices.Sort(pts)
	assert.Equal(t, []int64{1000, 1001, 1002, 1003, 1004, 1005, 1006, 1007, 1008, 1009}, pts)
	assert.Equal(t, NewRational(1001, 24000), enc.TimeBase())
}

func TestVideoStreamEncoderNeverDropsAFrameUnderBackPressure(t *testing.T) {
	// libx265 with a 40-frame lookahead answers EAGAIN-free sends but holds
	// frames; with frame-threads the send can return EAGAIN. Count packets.
	enc := x265Encoder(t, NewRational(1, 25), "rc-lookahead=40:frame-threads=4")
	n := 0
	for i := int64(0); i < 120; i++ {
		f := grayFrame(t, 64, 64, PixelFormatYUV420P10LE())
		f.SetPTS(i)
		require.NoError(t, enc.Encode(f, func(*Packet) error { n++; return nil }))
		f.Free()
	}
	require.NoError(t, enc.Flush(func(*Packet) error { n++; return nil }))
	assert.Equal(t, 120, n)
}

func TestEncoderNameMustEncodeTheCodec(t *testing.T) {
	_, err := NewVideoStreamEncoder(VideoStreamEncoderConfig{VideoEncoderConfig: VideoEncoderConfig{
		Codec: CodecIDH264, EncoderName: "libx265", Width: 64, Height: 64}, TimeBase: NewRational(1, 25)})
	assert.ErrorIs(t, err, ErrEncoderCodecMismatch)
}

// hwframes_test.go
func TestSoftwareDecodeUploadsThroughAFilterGraphDevice(t *testing.T) {
	// The NVIDIA path for a source NVDEC cannot decode: CPU decode → hwupload
	// (device on the graph) → scale_cuda=format=p010le → hevc_nvenc.
	dev := cudaOrSkip(t)
	frames := encodeThrough(t, dev, h264Clip(t, 48), "hwupload,scale_cuda=format=p010le", "hevc_nvenc")
	assert.Equal(t, 48, frames)
}

func TestNewHWFramesMakesAPoolOfTheAskedFormat(t *testing.T) {
	dev := cudaOrSkip(t)
	ref, err := NewHWFrames(HWFramesConfig{Device: dev, Format: PixelFormatCUDA(), SWFormat: PixelFormatP010LE(),
		Width: 256, Height: 144, PoolSize: 4})
	require.NoError(t, err)
	defer avutil.FreeBufferRef(&ref)
	assert.Equal(t, int(PixelFormatCUDA()), shim.HWFramesFormat(unsafe.Pointer(ref)))
}
```

`x265Encoder(t, tb, extraX265Params...)`, `grayFrame`, `encodeThrough` (StreamDecoder on CPU → `NewFilterGraph` with `HWDevice: dev` → `NewVideoStreamEncoder` with `EncoderName` and the graph's `OutputHWFramesCtx()` → counts packets) live in the test files. `SetPTS` is upstream's frame PTS setter or `avutil.SetFramePTS` wrapped — use what exists.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test -run 'TestVideoStreamEncoder|TestEncoderNameMust|TestSoftwareDecodeUploads|TestNewHWFrames' -count=1 .`
Expected: FAIL to compile — `NewVideoStreamEncoder`, `NewHWFrames`, `FilterGraphConfig.HWDevice`, `ErrEncoderCodecMismatch` undefined.

- [ ] **Step 3: Implement**

Shim:

```c
int ffshim_hwframes_configure(void *frames_ref, int format, int sw_format, int width, int height, int pool) {
    AVHWFramesContext *fc = (AVHWFramesContext *)((AVBufferRef *)frames_ref)->data;
    fc->format = format; fc->sw_format = sw_format;
    fc->width = width; fc->height = height; fc->initial_pool_size = pool;
    return 0;
}
int ffshim_filter_set_hw_device(void *filter_ctx, void *device_ref) {
    AVFilterContext *f = filter_ctx;
    av_buffer_unref(&f->hw_device_ctx);
    f->hw_device_ctx = av_buffer_ref((AVBufferRef *)device_ref);
    return f->hw_device_ctx ? 0 : AVERROR(ENOMEM);
}
```

(`ffshim_filter_set_hw_device` sits under `FFSHIM_HAVE_AVFILTER`, as Phase 0's `ffshim_buffersrc_set_hw_frames` does.)

`filter_graph.go`: after parsing the graph (`avfilter_graph_parse_ptr`) and before `avfilter_graph_config`, iterate the graph's filters (`nb_filters`/`filters[i]` through two shim accessors, `ffshim_graph_nb_filters`, `ffshim_graph_filter`) and call `ffshim_filter_set_hw_device` when `cfg.HWDevice != nil`.

`videoencoder.go`: `NewVideoStreamEncoder` resolves the codec (`FindEncoderByName` when named; `avcodec.GetCodecID(codec) != Codec` → `ErrEncoderCodecMismatch` when both given), allocates the context, sets width/height/time base/framerate, applies `applyVideoOptions(ctx, &cfg.VideoEncoderConfig)`, sets `HWFramesCtx` and pix_fmt from `shim.HWFramesFormat` exactly as `encoder.go` does, adds `cfg.SideData` with `addDecodedSideData`, sets the global-header flag when asked, opens. `Encode`: `SendFrame`; on `EAGAIN` receive-and-emit until `EAGAIN`, then resend; then receive-and-emit until `EAGAIN`. Packets keep the encoder's time base. `Parameters` allocates `AVCodecParameters` and fills it with `ParametersFromContext`.

`hwframes.go`: `NewHWFrames` = `av_hwframe_ctx_alloc(device.Context())`, `ffshim_hwframes_configure`, `av_hwframe_ctx_init`; on error unref and return it.

- [ ] **Step 4: Run them to verify they pass, then falsify**

Run: `go test -run 'TestVideoStreamEncoder|TestEncoderNameMust|TestSoftwareDecodeUploads|TestNewHWFrames' -count=1 -v .`
Expected: PASS on the workstation (GPU tests run). Falsify: overwrite PTS with a counter in `Encode` → `TestVideoStreamEncoderKeepsFramePTS` fails; return on `EAGAIN` without resending → `TestVideoStreamEncoderNeverDropsAFrame...` fails if libx265 ever returns `EAGAIN` there — if it never does on this host, ledger a ruling that the back-pressure test covers the drain-and-resend path only by inspection, and keep the test as a frame-count guard. Skip the graph-device loop → `TestSoftwareDecodeUploads...` fails at `hwupload` ("A hardware device reference is required"); restore.

- [ ] **Step 5: Commit**

```bash
git add videoencoder.go videoencoder_test.go hwframes.go hwframes_test.go filter_graph.go avutil/avutil.go shim/ffshim.h shim/ffshim.c internal/shim/shim.go
git commit -m "feat: VideoStreamEncoder (frame PTS kept, EAGAIN drained not dropped, HDR side data in), GPU frame pools, and a hardware device on filter graphs for hwupload, vpp_qsv and scale_vaapi"
```

---

### Task 6: One muxer, many streams

**Files:**
- Modify: `muxer.go`, `attachments.go` (share the attachment-stream builder)
- Create: `muxer_streams_test.go`

**Interfaces:**
- Consumes: `VideoStreamEncoder`, `AudioEncoder` (`Parameters()`, `TimeBase()`), `StreamInfo` (Task 1), `SetStreamSideData` (Task 2).
- Produces:
  - `type StreamOptions struct { Language, Title string; Disposition Disposition; Metadata Metadata; SideData map[PacketSideDataType][]byte }` — applied when the stream is added; `Metadata` first, then `Language`/`Title` override it
  - `CopyStreamConfig` gains `Options StreamOptions`
  - `type EncodedStreamSource interface { Parameters() (avcodec.Parameters, error); TimeBase() Rational }`
  - `func (m *Muxer) AddEncoderStream(src EncodedStreamSource, opts StreamOptions) (*MuxerStream, error)` — packets written with `WritePacket` are rescaled from `src.TimeBase()` to the stream's
  - `func (m *Muxer) AddAttachment(att Attachment) error`, `func (m *Muxer) SetChapters(chapters []Chapter) error`, `func (m *Muxer) SetMetadata(md Metadata) error` — before `WriteHeader`
  - `func (m *Muxer) NeedsGlobalHeader() bool` — so the Engine sets `GlobalHeader` on encoders before creating them
  - `WritePacket` is safe from several goroutines (it is already mutex-guarded; the doc comment says so)

- [ ] **Step 1: Write the failing test**

```go
// muxer_streams_test.go
func TestMuxerRebuildsAFileStreamByStream(t *testing.T) {
	src := everythingClip(t) // h264 video; eng AC-3 5.1 default; fre FLAC stereo "Commentary" comment;
	// eng SRT forced; a font attachment; two chapters; container title "Clip".
	out := filepath.Join(t.TempDir(), "out.mkv")

	d, err := NewDecoder(src)
	require.NoError(t, err)
	defer d.Close()
	m, err := NewMuxer(out, "matroska")
	require.NoError(t, err)
	defer m.Close()

	streams := map[int]*MuxerStream{}
	var aac *AudioEncoder
	var aacDec *StreamDecoder
	var res *Resampler
	for _, s := range d.Streams() {
		opts := StreamOptions{Language: s.Language, Title: s.Title, Disposition: s.Disposition, Metadata: s.Metadata}
		if s.Type == MediaTypeAudio && s.CodecName == "flac" {
			aacDec, err = d.NewStreamDecoder(s.Index, nil)
			require.NoError(t, err)
			aac, err = NewAudioEncoder(AudioEncoderConfig2{SampleRate: 48000, Layout: "stereo", BitRate: 128000, GlobalHeader: m.NeedsGlobalHeader()})
			require.NoError(t, err)
			res, err = NewResampler(AudioFormat{SampleRate: s.SampleRate, Layout: s.ChannelLayout, SampleFormat: SampleFormatS32},
				AudioFormat{SampleRate: 48000, Layout: "stereo", SampleFormat: SampleFormatFltP})
			require.NoError(t, err)
			streams[s.Index], err = m.AddEncoderStream(aac, opts)
		} else {
			streams[s.Index], err = m.AddCopyStream(&CopyStreamConfig{CodecParameters: s.CodecParameters(), TimeBase: s.TimeBase, Options: opts})
		}
		require.NoError(t, err)
	}
	for _, a := range d.GetAttachments() {
		require.NoError(t, m.AddAttachment(a))
	}
	require.NoError(t, m.SetChapters(d.GetChapters()))
	require.NoError(t, m.SetMetadata(d.GetMetadata()))
	require.NoError(t, m.WriteHeader())
	pumpCopyAndAAC(t, d, m, streams, aacDec, res, aac) // copies packets, decodes→resamples→encodes the FLAC track
	require.NoError(t, m.WriteTrailer())

	p := ffprobeJSON(t, out) // ffprobe -show_streams -show_chapters -show_format -of json
	require.Len(t, p.Streams, 5)
	assert.Equal(t, "h264", p.Streams[0].CodecName)
	assert.Equal(t, "ac3", p.Streams[1].CodecName)
	assert.Equal(t, "eng", p.Streams[1].Tags["language"])
	assert.Equal(t, 1, p.Streams[1].Disposition["default"])
	assert.Equal(t, "aac", p.Streams[2].CodecName)
	assert.Equal(t, "fre", p.Streams[2].Tags["language"])
	assert.Equal(t, "Commentary", p.Streams[2].Tags["title"])
	assert.Equal(t, 1, p.Streams[2].Disposition["comment"])
	assert.Equal(t, "subrip", p.Streams[3].CodecName)
	assert.Equal(t, 1, p.Streams[3].Disposition["forced"])
	assert.Equal(t, "attachment", p.Streams[4].CodecType)
	assert.Equal(t, "font.ttf", p.Streams[4].Tags["filename"])
	assert.Len(t, p.Chapters, 2)
	assert.Equal(t, "Clip", p.Format.Tags["title"])
	assert.InDelta(t, ffprobeDuration(t, src), p.Format.DurationSeconds(), 0.1)
}
```

`everythingClip` builds the source with one `ffmpeg` call: lavfi video and two sine sources, an SRT written to the temp dir, `-attach font.ttf -metadata:s:t mimetype=application/x-truetype-font` (the file's bytes need not be a real font), `-i chapters.ffmeta -map_chapters 2`, `-metadata title=Clip`, and the per-stream `-metadata:s:*`/`-disposition:*` flags. `pumpCopyAndAAC` and `ffprobeJSON` (a struct of what the asserts read) live in the test file.

- [ ] **Step 2: Run it to verify it fails**

Run: `go test -run TestMuxerRebuildsAFileStreamByStream -count=1 .`
Expected: FAIL to compile — `StreamOptions`, `AddEncoderStream`, `Muxer.AddAttachment`, `Muxer.SetChapters`, `Muxer.SetMetadata`, `NeedsGlobalHeader` undefined.

- [ ] **Step 3: Implement**

`muxer.go`: `applyStreamOptions(stream, opts)` sets metadata (`avformat.SetStreamMetadata` per key, then `language`/`title`), `shim.SetStreamDisposition` (`ErrShimRequired` if a non-zero disposition is asked without a shim), and `SetStreamSideData` on the stream's codec parameters per entry. `AddCopyStream` calls it after copying parameters. `AddEncoderStream` = new stream, `ParametersCopy` from `src.Parameters()` (then frees them), time base set to `src.TimeBase()`, `ms.timeBase = src.TimeBase()`, a new `packetMode bool` (rescale in `WritePacket`, like copy mode), then `applyStreamOptions`. `AddAttachment` calls `addAttachmentStream(formatCtx, att)` — the body of `Encoder.AddAttachment` moved into a shared function, `Encoder.AddAttachment` calling it too. `SetChapters` likewise shares `Encoder.SetChapters`' loop (`addChapters(formatCtx, chapters)`). `SetMetadata` = `avformat.SetMetadata` per key. `NeedsGlobalHeader` = `avformat.NeedsGlobalHeader(m.formatCtx)`.

- [ ] **Step 4: Run it to verify it passes, then falsify**

Run: `go test -run TestMuxerRebuildsAFileStreamByStream -count=1 -v .`
Expected: PASS. Falsify: skip `shim.SetStreamDisposition` in `applyStreamOptions` → the default/comment/forced asserts fail; skip `addChapters` → `Chapters` length 0; restore each.

- [ ] **Step 5: Commit**

```bash
git add muxer.go attachments.go chapters.go muxer_streams_test.go
git commit -m "feat: Muxer takes encoded and copied streams with language, title, disposition and side data, plus attachments, chapters and container metadata"
```

---

### Task 7: HDR10 end to end, the whole suite, CI, tag

**Files:**
- Create: `hdr_pipeline_test.go`
- Modify: `README.md` (fork section: the Phase 1 primitives), `docs/ffmpeg9-baseline.md` (test count)

**Interfaces:**
- Consumes: everything above.
- Produces: tag `v0.0.0-clustarr.3` on `master`.

- [ ] **Step 1: Write the failing tests**

```go
// hdr_pipeline_test.go
// hdr10Transcode decodes hdr10Clip, removes HDR10+ and DV side data from
// every frame, encodes HEVC Main 10 with encoderName (through filter when
// set), passing the source's mastering display and light level as encoder
// side data and as output stream side data, and muxes to Matroska.
func TestHDR10SurvivesALibx265ReEncode(t *testing.T) {
	out := hdr10Transcode(t, hdr10Clip(t), "libx265", nil, "")
	s := ffprobeStreamSideData(t, out) // ffprobe -show_streams side_data_list
	assert.Contains(t, s, "Mastering display metadata")
	assert.Contains(t, s, "Content light level metadata")
	f := ffprobeFirstFrameSideData(t, out) // -show_frames -read_intervals %+#1
	assert.Contains(t, f, "Mastering display metadata", "in-band SEI from libx265")
	assert.Equal(t, "smpte2084", ffprobeColorTransfer(t, out))
	assert.Equal(t, "yuv420p10le", ffprobePixFmt(t, out))
	assert.Equal(t, "Main 10", ffprobeProfile(t, out))
}

func TestHDR10SurvivesAnNVENCReEncode(t *testing.T) {
	dev := cudaOrSkip(t)
	out := hdr10Transcode(t, hdr10Clip(t), "hevc_nvenc", dev, "scale_cuda=format=p010le")
	s := ffprobeStreamSideData(t, out)
	assert.Contains(t, s, "Mastering display metadata")
	assert.Contains(t, s, "Content light level metadata")
	assert.Equal(t, "smpte2084", ffprobeColorTransfer(t, out))
	assert.Equal(t, "Main 10", ffprobeProfile(t, out))
	t.Logf("NVENC in-band SEI: %v", ffprobeFirstFrameSideData(t, out)) // recorded, not asserted (see Step 4)
}

func TestHDRPlusIsRemovedFromEveryFrame(t *testing.T) {
	// No encoder writes HDR10+ from a synthetic clip, so the test adds it to
	// each decoded frame, runs the removal hdr10Transcode uses, and checks
	// the frames handed to the encoder carry none.
	frames := hdr10TranscodeFrames(t, hdr10Clip(t), func(f Frame) { _ = f.AddSideData(FrameSideHDRPlus(), []byte{1}) })
	for i, sd := range frames {
		assert.NotContains(t, sd, FrameSideHDRPlus(), "frame %d", i)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test -run 'TestHDR10Survives|TestHDRPlusIsRemoved' -count=1 .`
Expected: FAIL to compile (`hdr10Transcode` not yet written), then — once the helper is written without the side-data hand-off — FAIL on the "Mastering display metadata" stream assertion. Write the helper in two steps so the second RED is seen: first without passing `SideData`/`SetStreamSideData`, watch the assertion fail, then add them.

- [ ] **Step 3: Implement**

`hdr10Transcode` (test helper; it is the Engine's video leg in miniature and Phase 3 copies its order): read the source stream's mastering display and light level with `StreamSideData`; if absent, from the first decoded frame's `SideData`; create the encoder with `SideData` set and `ColorSpec` (BT.2020, PQ, BT.2020nc, limited) through `VideoEncoderConfig.CodecOptions` (`color_primaries`, `color_trc`, `colorspace`) or the context setters, whichever upstream exposes; add the output stream with `StreamOptions.SideData` carrying both; on every frame call `RemoveSideData(FrameSideHDRPlus(), FrameSideDOVIRPU(), FrameSideDOVIMetadata())` before `Encode`. No production code is expected to change in this task; if a test fails for a reason in a primitive, fix the primitive with its own failing test first.

- [ ] **Step 4: Run them to verify they pass**

Run: `go test -run 'TestHDR10Survives|TestHDRPlusIsRemoved' -count=1 -v .`
Expected: PASS; on the workstation the NVENC test runs. Ledger what the NVENC test logs for in-band SEI: FFmpeg 9's `hevc_nvenc` either writes mastering display/light level SEI from `decoded_side_data` or not; if not, stream-level metadata is what players read from Matroska and Phase 3 records the gap in the spec — rule, don't stall.

- [ ] **Step 5: The whole suite, CI, README, tag**

Run: `go test -count=1 ./... > /tmp/ffgo-phase1-suite.txt 2>&1; tail -20 /tmp/ffgo-phase1-suite.txt`
Expected: every package `ok`, no `FAIL`. Also run the Debian 12 simulation used in Phase 0 (FFmpeg 5.1 container, `go test ./...`): Phase 1's new tests skip by name there where `ErrNotSupported` applies (coded side data, decoded side data) — add `t.Skip` on `errors.Is(err, ErrNotSupported)` where a helper hits it — and nothing else changes.

Update `README.md`'s fork section with one line per primitive, then:

```bash
git add hdr_pipeline_test.go README.md docs/ffmpeg9-baseline.md
git commit -m "test: HDR10 survives libx265 and NVENC re-encodes with HDR10+ and DV RPU removed; README: the Phase 1 primitives"
git push origin master
```

Wait for `ffmpeg9.yml` on the pushed commit (`gh run watch` on its run id; all three jobs green). Then:

```bash
git tag -a v0.0.0-clustarr.3 -m "Phase 1: StreamDecoder, VideoStreamEncoder, AudioEncoder, multi-stream Muxer, side data, GPU frame pools and filter-graph devices"
git push origin v0.0.0-clustarr.3
```

Upstream pull requests for these primitives are listed in the final report and opened only on the owner's confirmation.
