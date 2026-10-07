# MP4 Standard Phase 1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Every file squasharr writes is one `.mp4`: HEVC `hvc1`; per language a Dolby surround track (E-AC-3 or AC-3 copied, else AC-3 5.1 encoded) plus an AAC 2.0 companion, or AAC alone; text subtitles as `mov_text`, ASS and forced text as sidecars; files with image subtitles held for phase 2's OCR.

**Architecture:** `pkg/transcode/standard.Plan` decides the new layout. It always plans MP4, plans audio per language and plans each subtitle as embedded, a sidecar or dropped. `pkg/transcode/engine` runs the plan:
- one demux can feed a stream to a copy slot and to an encode stage at once;
- one audio stage can encode several outputs (AC-3 and AAC) from one decode;
- a converter rewrites text subtitle packets as `mov_text` samples;
- sidecar sinks write `.ass` (ffgo's `ass` muxer) and `.srt` (Go) files beside the part;
- the graft stage can emit a surround dub as AC-3 5.1 plus AAC 2.0.

The squasharr worker places the sidecars before the video swap, and every caller of the profile's `container` now uses MP4.

**Tech Stack:** Go, FFmpeg 9 through the ffgo fork (`github.com/mediactl/ffgo`, a new tag `v0.0.0-clustarr.12`), testify, envtest, the ffmpeg/ffprobe CLIs in tests only (to make and inspect clips).

**Spec:** `docs/superpowers/specs/2026-10-06-mp4-standard-design.md` (approved 2026-10-06, with the owner's E-AC-3 correction the same day).

## Global Constraints

- GPL-3.0 header from `hack/boilerplate.go.txt` on every new Go file.
- `cmd/clustarr` must never link ffgo or purego (`TestClustarrNeverLinksADynamicLoader`). Everything here lives under `pkg/transcode/engine`, `app/squash/worker/...` and `cmd/squasharr-worker`, which already link them. `pkg/transcode/standard` and `pkg/fsops` must stay ffgo-free, since the controller imports them.
- The worker never execs ffmpeg (`TestTheWorkerNeverExecsFFmpeg`). The CLIs appear in `_test.go` files only.
- No `go get` and no `go mod tidy`. The ffgo bump edits the `replace` line and runs `go mod download github.com/mediactl/ffgo@v0.0.0-clustarr.12`, as `b3b077a2` did.
- Pathspec commits: `git commit -m '...' -- <paths>`. `git add` every new file first (memory `pathspec-commit-skips-untracked`). Never `git stash`.
- `standard.Version` becomes `2`. The controller's plan (`transcode.FromSummary`) and the worker's (`transcode.FromProbe`) must still hash alike.
- Sidecar names follow `pkg/subtitles.ParseSidecar`'s grammar `<stem>[.<lang>][.forced|.sdh].<ext>`, with `ext` in `{ass, srt}`.
- Audio, from the spec's §3 as amended:
  - **E-AC-3** is copied at any channel count.
  - **AC-3** is copied.
  - **Any other surround** source becomes AC-3 5.1 at 640 kbps.
  - **Each surround primary** also gets an AAC 2.0 companion at 160 kbps.
  - **A mono or stereo primary** is AAC, copied when it is already AAC.
  - **Commentary** becomes AAC of at most 2 channels.
  - **Other tracks in the same language** are dropped.
- **Image subtitles** (any `Bitmap` stream) hold the file in phase 1: the plan is a skip naming phase 2.
- `make test` runs from a clean worktree with `helm dependency build charts/clustarr` done (CLAUDE.md gotcha).

## Rulings this plan makes against the spec (record each in the ledger)

- **R1, §10: a held file is Skipped, not Planned.** A Planned hold counts toward the 32-job window (`transcodeprofile.isOpen`), and 32 PGS files in a row by name would stop every transcode. A Skipped job is terminal, stays as the record that stops the file being planned again (`retireSucceeded` keeps Skipped), and phase 2's Version raise gives the file a new job name. *Cost if wrong:* none; phase 2 still finds the files through the new hash.
- **R2, §4: forced text subtitles become `<stem>.<lang>.forced.srt` sidecars.** They are not embedded `mov_text` with a forced disposition. FFmpeg's MP4 muxer writes no forced flag that Plex reads, while `.forced.` in a sidecar name is Plex's documented forced marker. A track titled with "sign" or "song" counts as forced, as anime "Signs & Songs" tracks are. *Cost if wrong:* a forced track Plex could have read embedded sits beside the file instead, which plays the same.
- **R3, §4: one sidecar per name.** A second track that would take a name already planned is dropped and listed in `Result.Dropped`. An existing file at a sidecar's final name is kept, and the planned sidecar is not written. *Cost if wrong:* a second English full-subtitle ASS track, rare, is lost.
- **R4, §3 rule 6: the default audio language is the source's.** The language of the source's default-flagged, non-commentary kept track carries the default, else the first kept language. A dual-audio release that lists English first but flags Japanese default keeps Japanese. *Cost if wrong:* the default flips back to the spec's literal "first kept language".
- **R5, §3: encoded tracks get generated titles**, "Dolby Digital 5.1", "Stereo" or "Mono": a copied source title such as "DTS-HD MA 5.1" would misname them. Copied tracks and commentary keep their titles. *Cost if wrong:* cosmetic.
- **R6, §6: `mov_text` is written by the engine, not FFmpeg's encoder.** ffgo binds no subtitle encoder, and the spec drops styling anyway. Each SubRip or WebVTT packet's text, without tags, becomes a `mov_text` sample: a 16-bit length, then the text, with a fixed default text sample entry as extradata. *Cost if wrong:* a later need for styled `mov_text` means binding `avcodec_encode_subtitle` in the fork.

## Review Focus

1. **A language with TrueHD 7.1 and an AC-3 5.1 "compatibility" track:** the AC-3 is copied, the TrueHD dropped, and the AAC companion is decoded from the AC-3. Task 3 tests it.
2. **An anime file with English "Full" and "Signs & Songs" ASS tracks beside a captionarr `.en.srt`:** the two `.ass` sidecars get distinct names, and the `.srt` is untouched. Tasks 4 and 9 test it.
3. **A crash between placing the sidecars and the video swap:** the retry writes no duplicate and overwrites nothing. Task 9 tests it.
4. **A verify failure, or a lease lost before the swap, with sidecars planned:** no sidecar part is left behind, and no sidecar reaches its final name. Task 9 tests it.
5. **The controller's plan and the worker's, now that subtitle suffixes read the language, the forced and SDH flags and the title,** must hash alike for the same file. Task 4 extends the parity test.

---

### Task 1: ffgo gains packets from bytes and codec-parameter setters; the MP4 capabilities are proven

**Files:**
- Modify: `/home/appkins/src/mediactl/ffgo/avcodec/avcodec.go` (near `SetCodecParTag`, line ~379, and the packet helpers, ~474)
- Modify: `/home/appkins/src/mediactl/ffgo/ffgo.go` (Packet methods, ~219-340)
- Modify: `/home/appkins/src/mediactl/ffgo/shim/ffshim.c` (layout table, ~681), only if the layout table lists AVCodecParameters fields by name; the Go fallback offsets are correct for FFmpeg 4-9
- Test: `/home/appkins/src/mediactl/ffgo/packet_data_test.go`
- Modify: `go.mod` (the `replace github.com/obinnaokechukwu/ffgo => github.com/mediactl/ffgo v0.0.0-clustarr.12` line), `go.sum`
- Test: `pkg/transcode/engine/mp4_capabilities_test.go`

**Interfaces:**
- Produces (ffgo):
  - `func NewPacketFromData(data []byte) (*Packet, error)` and `func (p *Packet) Data() []byte`;
  - `func avcodec.NewPacket(pkt Packet, size int) error`;
  - `func avcodec.SetCodecParCodecID(par Parameters, id CodecID)` and `func avcodec.GetCodecParCodecID(par Parameters) CodecID`;
  - `func avcodec.SetCodecParExtradata(par Parameters, b []byte) error` and `func avcodec.GetCodecParExtradata(par Parameters) []byte`.
- Produces (clustarr): the capability tests later tasks lean on (an `ac3` encoder at 5.1, E-AC-3 copied into MP4).

- [ ] **Step 1: Write the failing ffgo tests**

```go
// packet_data_test.go (ffgo, package ffgo)
func TestNewPacketFromDataRoundTrips(t *testing.T) {
	if err := Init(); err != nil {
		t.Skipf("no FFmpeg: %v", err)
	}
	p, err := NewPacketFromData([]byte("\x00\x05Hello"))
	if err != nil {
		t.Fatal(err)
	}
	defer p.Free()
	if got := string(p.Data()); got != "\x00\x05Hello" {
		t.Fatalf("Data() = %q", got)
	}
	if p.Size() != 7 {
		t.Fatalf("Size() = %d", p.Size())
	}
}

func TestCodecParametersTakeACodecIDAndExtradata(t *testing.T) {
	if err := Init(); err != nil {
		t.Skipf("no FFmpeg: %v", err)
	}
	par := avcodec.ParametersAlloc()
	defer avcodec.ParametersFree(&par)
	id := avcodec.CodecIDByName("mov_text")
	avcodec.SetCodecParCodecID(par, id)
	if err := avcodec.SetCodecParExtradata(par, []byte{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	if got := avcodec.GetCodecParCodecID(par); got != id {
		t.Fatalf("codec id %v, want %v", got, id)
	}
	if got := avcodec.GetCodecParExtradata(par); !bytes.Equal(got, []byte{1, 2, 3}) {
		t.Fatalf("extradata %v", got)
	}
	// Replacing frees the old buffer and takes the new one.
	if err := avcodec.SetCodecParExtradata(par, []byte{9}); err != nil {
		t.Fatal(err)
	}
	if got := avcodec.GetCodecParExtradata(par); !bytes.Equal(got, []byte{9}) {
		t.Fatalf("extradata after replace %v", got)
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `cd /home/appkins/src/mediactl/ffgo && go test -run 'TestNewPacketFromData|TestCodecParametersTake' .`
Expected: FAIL to compile with `undefined: NewPacketFromData`, `undefined: avcodec.SetCodecParCodecID`.

- [ ] **Step 3: Implement them in the fork**

In `avcodec/avcodec.go`, register `av_new_packet` beside `av_packet_alloc`, plus `av_mallocz` and `av_free`, unless `avutil` already binds them; use `avutil`'s bindings if it does. Then:

```go
var avNewPacket func(pkt uintptr, size int32) int32 // registered: "av_new_packet"

// NewPacket gives pkt a payload of size bytes (av_new_packet), zero-padded.
func NewPacket(pkt Packet, size int) error {
	if avNewPacket == nil {
		return errors.New("avcodec: av_new_packet not loaded")
	}
	if ret := avNewPacket(uintptr(pkt), int32(size)); ret < 0 {
		return avutil.NewError(ret, "av_new_packet")
	}
	return nil
}

var (
	offsetCodecParCodecID       = layout.Offset("AVCodecParameters.codec_id", 4)
	offsetCodecParExtradata     = layout.Offset("AVCodecParameters.extradata", 16)
	offsetCodecParExtradataSize = layout.Offset("AVCodecParameters.extradata_size", 24)
)

// SetCodecParCodecID sets par's codec_id.
func SetCodecParCodecID(par Parameters, id CodecID) {
	if par == nil {
		return
	}
	*(*int32)(unsafe.Pointer(uintptr(par) + offsetCodecParCodecID)) = int32(id)
}

// GetCodecParCodecID is par's codec_id.
func GetCodecParCodecID(par Parameters) CodecID {
	if par == nil {
		return 0
	}
	return CodecID(*(*int32)(unsafe.Pointer(uintptr(par) + offsetCodecParCodecID)))
}

// inputBufferPadding is AV_INPUT_BUFFER_PADDING_SIZE.
const inputBufferPadding = 64

// SetCodecParExtradata replaces par's extradata with a zero-padded copy of b
// in av_mallocz'd memory, which avcodec_parameters_free releases with par.
func SetCodecParExtradata(par Parameters, b []byte) error {
	if par == nil {
		return errors.New("avcodec: nil parameters")
	}
	ptr := (*unsafe.Pointer)(unsafe.Pointer(uintptr(par) + offsetCodecParExtradata))
	size := (*int32)(unsafe.Pointer(uintptr(par) + offsetCodecParExtradataSize))
	if *ptr != nil {
		avutil.Free(*ptr)
		*ptr, *size = nil, 0
	}
	buf := avutil.Mallocz(len(b) + inputBufferPadding)
	if buf == nil {
		return errors.New("avcodec: allocate extradata")
	}
	copy(unsafe.Slice((*byte)(buf), len(b)), b)
	*ptr, *size = buf, int32(len(b))
	return nil
}

// GetCodecParExtradata is a copy of par's extradata.
func GetCodecParExtradata(par Parameters) []byte {
	if par == nil {
		return nil
	}
	ptr := *(*unsafe.Pointer)(unsafe.Pointer(uintptr(par) + offsetCodecParExtradata))
	n := *(*int32)(unsafe.Pointer(uintptr(par) + offsetCodecParExtradataSize))
	if ptr == nil || n <= 0 {
		return nil
	}
	return append([]byte(nil), unsafe.Slice((*byte)(ptr), n)...)
}
```

In `ffgo.go`:

```go
// NewPacketFromData is a packet holding a copy of data, with no timestamps
// (the caller sets them through avcodec.SetPacketPTS and friends).
func NewPacketFromData(data []byte) (*Packet, error) {
	pkt := avcodec.PacketAlloc()
	if pkt == nil {
		return nil, errors.New("ffgo: allocate packet")
	}
	if err := avcodec.NewPacket(pkt, len(data)); err != nil {
		avcodec.PacketFree(&pkt)
		return nil, err
	}
	if len(data) > 0 {
		copy(unsafe.Slice((*byte)(avcodec.GetPacketData(pkt)), len(data)), data)
	}
	return &Packet{ptr: pkt}, nil
}

// Data is a copy of the packet's payload.
func (p *Packet) Data() []byte {
	if p.IsNil() {
		return nil
	}
	n := avcodec.GetPacketSize(p.ptr)
	d := avcodec.GetPacketData(p.ptr)
	if n <= 0 || d == nil {
		return nil
	}
	return append([]byte(nil), unsafe.Slice((*byte)(d), n)...)
}
```

Use the names `Packet`'s existing fields and `PacketFree` actually have (read `ffgo.go:200-340` first). If the shim's layout table (`shim/ffshim.c` ~681) names AVCodecParameters fields, add `codec_id`, `extradata` and `extradata_size` there as `codec_tag` is.

- [ ] **Step 4: Run the ffgo tests and the fork's whole suite**

Run: `cd /home/appkins/src/mediactl/ffgo && go test -run 'TestNewPacketFromData|TestCodecParametersTake' . && go test ./... 2>&1 | tail -5`
Expected: PASS; the suite's existing tests stay green.

- [ ] **Step 5: Commit, tag and push the fork**

```bash
cd /home/appkins/src/mediactl/ffgo
git add packet_data_test.go
git commit -m "feat: NewPacketFromData, Packet.Data, and codec_id/extradata setters on AVCodecParameters -- clustarr's mov_text and sidecar writing" -- avcodec/avcodec.go ffgo.go packet_data_test.go shim/ffshim.c
git tag v0.0.0-clustarr.12
git push origin HEAD v0.0.0-clustarr.12
```

- [ ] **Step 6: Bump clustarr to the tag**

Edit `go.mod`'s replace line to `v0.0.0-clustarr.12`, then:

Run: `cd /home/appkins/src/mediactl/clustarr && go mod download github.com/mediactl/ffgo@v0.0.0-clustarr.12 && go build ./... && echo OK`
Expected: `OK`. `git diff go.sum` shows only the ffgo lines changed.

- [ ] **Step 7: Write the capability tests in clustarr**

`pkg/transcode/engine/mp4_capabilities_test.go`, package `engine`. Each test makes its clip with the ffmpeg CLI through the helpers in `helpers_test.go` (`ffmpeg9OrSkip`, `run`, `ffprobeJSON`):

```go
// The AC-3 encoder takes 5.1 FLTP at 640 kbps: what planAudio's ac3 action
// asks for (spec §3).
func TestTheAC3EncoderOpensAtFivePointOne(t *testing.T) {
	ffmpeg9OrSkip(t)
	enc, err := ffgo.NewAudioEncoder(ffgo.AudioEncoderConfig2{
		EncoderName: "ac3", SampleRate: 48000, Layout: "5.1", BitRate: 640000,
		GlobalHeader: true, InputTimeBase: ffgo.NewRational(1, 48000),
	})
	require.NoError(t, err)
	require.NoError(t, enc.Close())
}

// E-AC-3 is copied into MP4 (ec-3): the owner's correction to the spec.
func TestEAC3IsCopiedIntoMP4(t *testing.T) {
	ffmpeg9OrSkip(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "eac3.mkv")
	run(t, "ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=320x180:rate=24:duration=2",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=2,aformat=channel_layouts=5.1",
		"-c:v", "libx265", "-preset", "ultrafast", "-pix_fmt", "yuv420p10le", "-c:a", "eac3", src)
	out := filepath.Join(dir, "out.mp4")
	plan := standard.Result{
		Decision: standard.DecisionCopyVideo, Container: transcode.ContainerMP4,
		Video: standard.VideoPlan{Action: "copy"},
		Audio: []standard.AudioPlan{{SourceIndex: 0, Action: "copy"}},
	}
	_, err := Run(context.Background(), plan, src, out, Options{})
	require.NoError(t, err)
	p := ffprobeJSON(t, out)
	require.Len(t, p.Streams, 2)
	assert.Equal(t, "eac3", p.Streams[1].CodecName)
	assert.Equal(t, 6, p.Streams[1].Channels)
}
```

The ASS copy-mux capability is proven in Task 7, whose sink it needs.

- [ ] **Step 8: Run them**

Run: `go test -count=1 -run 'TestTheAC3EncoderOpens|TestEAC3IsCopied' -v ./pkg/transcode/engine/`
Expected: PASS for both, with `eac3` and 6 channels read back. These are capability checks of code that exists, so passing first is their point: the test that must fail first is Task 3's. If the AC-3 test fails, `EncoderName` does not reach `avcodec_find_encoder_by_name`, and that is a fork fix in this task.

- [ ] **Step 9: Commit**

```bash
git add pkg/transcode/engine/mp4_capabilities_test.go
git commit -m "build: ffgo v0.0.0-clustarr.12 (packets from bytes, codec_id and extradata setters); the MP4 standard's capabilities proven: an ac3 encoder at 5.1, E-AC-3 copied into MP4" -- go.mod go.sum pkg/transcode/engine/mp4_capabilities_test.go
```

---

### Task 2: `fsops.SidecarPath` names a sidecar beside a video or a transcode part

**Files:**
- Create: `pkg/fsops/sidecar.go`
- Test: `pkg/fsops/sidecar_test.go`

**Interfaces:**
- Produces: `func SidecarPath(video, suffix string) string`, where `suffix` is everything after the stem (`"en.ass"`, `"en.forced.srt"`, `"ass"`), and `func SubtitleExt(ext string) bool` (`.ass`, `.srt`).

- [ ] **Step 1: Write the failing test**

```go
func TestSidecarPath(t *testing.T) {
	for _, tc := range []struct{ video, suffix, want string }{
		{"/m/Show - S01E01.mp4", "en.ass", "/m/Show - S01E01.en.ass"},
		{"/m/Show - S01E01.mp4", "en.forced.srt", "/m/Show - S01E01.en.forced.srt"},
		{"/m/Show - S01E01.mp4", "ass", "/m/Show - S01E01.ass"},
		// A part's sidecar is itself a part: <stem>.<lang...>.part-<uid8>-<n>.<ext>.
		{"/m/Show - S01E01.part-ab12cd34-2.mp4", "en.ass", "/m/Show - S01E01.en.part-ab12cd34-2.ass"},
		{"/m/Show - S01E01.part-ab12cd34-2.mp4", "en.forced.srt", "/m/Show - S01E01.en.forced.part-ab12cd34-2.srt"},
		{"/m/Show - S01E01.part-ab12cd34-2.mp4", "ass", "/m/Show - S01E01.part-ab12cd34-2.ass"},
	} {
		assert.Equal(t, tc.want, SidecarPath(tc.video, tc.suffix), "%s + %s", tc.video, tc.suffix)
	}
}

// A sidecar part is a part to every sweep (IsPart, ParseTranscodePart) and
// no sidecar to the subtitle scanner.
func TestASidecarPartIsAPartAndNoSidecar(t *testing.T) {
	p := SidecarPath("/m/Show.part-ab12cd34-2.mp4", "en.ass")
	assert.True(t, IsPart(p))
	tp, ok := ParseTranscodePart(p)
	require.True(t, ok)
	assert.Equal(t, "/m/Show.en", tp.Stem)
	assert.Equal(t, "ab12cd34", tp.JobUID8)
	assert.Equal(t, 2, tp.Attempt)
	assert.True(t, SubtitleExt(tp.Ext))
	_, isSidecar := subtitles.ParseSidecar("Show", filepath.Base(p))
	assert.False(t, isSidecar)
}
```

Check first that `pkg/fsops` importing `pkg/subtitles` in a test creates no cycle (`go list -deps ./pkg/subtitles | grep fsops`). If it does, assert the scanner half in `pkg/subtitles`' own tests.

- [ ] **Step 2: Run it to see it fail**

Run: `go test -run 'TestSidecarPath|TestASidecarPart' ./pkg/fsops/`
Expected: FAIL to compile, `undefined: SidecarPath`.

- [ ] **Step 3: Implement**

```go
// SidecarPath is the sidecar named suffix -- everything after the stem:
// "en.ass", "en.forced.srt", "ass" -- of the video at video. Beside a
// library file it is <stem>.<suffix>. Beside a transcode attempt's part,
// <stem>.part-<uid8>-<n>.<ext>, it is that attempt's part of the sidecar,
// <stem>.<suffix less its ext>.part-<uid8>-<n>.<suffix ext>, which
// ParseTranscodePart and IsPart read as a part (both sweeps and the
// rescan's orphan sweep remove it with its attempt) and
// subtitles.ParseSidecar reads as no sidecar of anything.
func SidecarPath(video, suffix string) string {
	ext := filepath.Ext(suffix)
	name := strings.TrimSuffix(suffix, ext)
	if p, ok := ParseTranscodePart(video); ok {
		stem := p.Stem
		if name != "" {
			stem += "." + name
		}
		return fmt.Sprintf("%s.part-%s-%d%s", stem, p.JobUID8, p.Attempt, ext)
	}
	return strings.TrimSuffix(video, filepath.Ext(video)) + "." + suffix
}

// SubtitleExt reports whether ext is a sidecar the transcode standard writes.
func SubtitleExt(ext string) bool { return ext == ".ass" || ext == ".srt" }
```

- [ ] **Step 4: Run it**

Run: `go test ./pkg/fsops/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/fsops/sidecar.go pkg/fsops/sidecar_test.go
git commit -m "feat(fsops): SidecarPath -- a sidecar beside a video, or beside a transcode part as a part of its own" -- pkg/fsops/sidecar.go pkg/fsops/sidecar_test.go
```

---

### Task 3: the standard plans MP4 and the new audio layout

**Files:**
- Modify: `pkg/transcode/standard/plan.go` (`Version`, `AudioPlan`, `Plan`, `planAudio`, `aacLayouts`, `directPlay`, the package doc)
- Test: `pkg/transcode/standard/plan_test.go`; goldens under `test/data/transcode/standard/` (regenerate through `golden_test.go`'s update flag, after reading what it is called)

**Interfaces:**
- Produces:
  - `const Version = 2`;
  - the audio actions `AudioCopy = "copy"`, `AudioAAC = "aac"` and `AudioAC3 = "ac3"`;
  - `const AC3BitRate = 640000`, `AC3Layout = "5.1"` and `AACBitRatePerChannel = 80000`;
  - `AudioPlan.Default bool` (`json:",omitempty"`). `AudioPlan.Action` is one of the three actions.
  - Plan's `Result.Container` is always `transcode.ContainerMP4`, and `Result.Attachments` always false.
- Consumes: nothing new.

- [ ] **Step 1: Write the failing tests**

Add a helper and table to `plan_test.go`, in the style of its existing tests (read `plan_test.go:1-80` for the `info`/profile builders it already has, and use them):

```go
func aud(codec string, ch int32, lang, title string) transcode.AudioStream {
	return transcode.AudioStream{Codec: codec, Channels: ch, Language: lang, Title: title}
}

func TestPlanAudioLayout(t *testing.T) {
	type out struct {
		src     int32
		action  string
		ch      int32
		def     bool
		title   string
		comment bool
	}
	for _, tc := range []struct {
		name string
		in   []transcode.AudioStream
		want []out
	}{
		{"E-AC-3 5.1 is copied and gains an AAC 2.0 companion",
			[]transcode.AudioStream{aud("eac3", 6, "en", "English")},
			[]out{{0, "copy", 0, true, "English", false}, {0, "aac", 2, false, "Stereo", false}}},
		{"E-AC-3 7.1 Atmos is copied as is",
			[]transcode.AudioStream{aud("eac3", 8, "en", "Atmos")},
			[]out{{0, "copy", 0, true, "Atmos", false}, {0, "aac", 2, false, "Stereo", false}}},
		{"DTS-HD 5.1 becomes AC-3 5.1 plus AAC 2.0",
			[]transcode.AudioStream{aud("dts", 6, "en", "DTS-HD MA 5.1")},
			[]out{{0, "ac3", 6, true, "Dolby Digital 5.1", false}, {0, "aac", 2, false, "Stereo", false}}},
		{"TrueHD 7.1 beside an AC-3 5.1 core: the AC-3 is copied, the TrueHD dropped",
			[]transcode.AudioStream{aud("truehd", 8, "en", "TrueHD Atmos"), aud("ac3", 6, "en", "AC-3")},
			[]out{{1, "copy", 0, true, "AC-3", false}, {1, "aac", 2, false, "Stereo", false}}},
		{"E-AC-3 wins over AC-3 in the same language",
			[]transcode.AudioStream{aud("ac3", 6, "en", ""), aud("eac3", 6, "en", "")},
			[]out{{1, "copy", 0, true, "", false}, {1, "aac", 2, false, "Stereo", false}}},
		{"multichannel AAC becomes AC-3 plus AAC 2.0",
			[]transcode.AudioStream{aud("aac", 6, "ja", "")},
			[]out{{0, "ac3", 6, true, "Dolby Digital 5.1", false}, {0, "aac", 2, false, "Stereo", false}}},
		{"stereo AAC is copied alone",
			[]transcode.AudioStream{aud("aac", 2, "en", "Stereo")},
			[]out{{0, "copy", 0, true, "Stereo", false}}},
		{"stereo AC-3 becomes AAC alone",
			[]transcode.AudioStream{aud("ac3", 2, "en", "")},
			[]out{{0, "aac", 2, true, "Stereo", false}}},
		{"mono FLAC becomes AAC mono",
			[]transcode.AudioStream{aud("flac", 1, "en", "")},
			[]out{{0, "aac", 1, true, "Mono", false}}},
		{"a second English mix is dropped; commentary is kept as AAC 2.0",
			[]transcode.AudioStream{aud("eac3", 6, "en", ""), aud("aac", 2, "en", "Stereo"), aud("ac3", 6, "en", "Director's Commentary")},
			[]out{{0, "copy", 0, true, "", false}, {0, "aac", 2, false, "Stereo", false}, {2, "aac", 2, false, "Director's Commentary", true}}},
		{"languages keep their order; each gets its own layout",
			[]transcode.AudioStream{aud("flac", 2, "ja", ""), aud("eac3", 6, "en", "")},
			[]out{{0, "aac", 2, true, "Stereo", false}, {1, "copy", 0, false, "", false}, {1, "aac", 2, false, "Stereo", false}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := planAudio(tc.in, nil)
			require.Len(t, got, len(tc.want))
			for i, w := range tc.want {
				g := got[i]
				assert.Equal(t, w.src, g.SourceIndex, "track %d source", i)
				assert.Equal(t, w.action, g.Action, "track %d action", i)
				if w.ch != 0 {
					assert.Equal(t, w.ch, g.Channels, "track %d channels", i)
				}
				assert.Equal(t, w.def, g.Default, "track %d default", i)
				assert.Equal(t, w.title, g.Title, "track %d title", i)
				assert.Equal(t, w.comment, g.Comment, "track %d comment", i)
			}
		})
	}
}

// R4: the source's default-flagged language keeps the default.
func TestPlanAudioKeepsTheSourcesDefaultLanguage(t *testing.T) {
	en, ja := aud("aac", 2, "en", ""), aud("aac", 2, "ja", "")
	ja.Disposition.Default = true
	got := planAudio([]transcode.AudioStream{en, ja}, nil)
	require.Len(t, got, 2)
	assert.False(t, got[0].Default)
	assert.True(t, got[1].Default)
}

func TestTheBitRatesAndLayouts(t *testing.T) {
	got := planAudio([]transcode.AudioStream{aud("dts", 8, "en", "")}, nil)
	require.Len(t, got, 2)
	assert.Equal(t, int64(640000), got[0].BitRate)
	assert.Equal(t, "5.1", got[0].Layout)
	assert.Equal(t, int64(160000), got[1].BitRate)
	assert.Equal(t, "stereo", got[1].Layout)
}

func TestPlanAlwaysWritesMP4(t *testing.T) {
	p := Plan(h264Info(), Profile{Name: "p", Hash: "h", Container: transcode.ContainerMKV}, Hardware{Tier: transcode.TierCPUx265})
	assert.Equal(t, transcode.ContainerMP4, p.Container)
	assert.False(t, p.Attachments)
}

func TestVersionIsTwo(t *testing.T) { assert.Equal(t, 2, Version) }
```

`h264Info()` stands for whichever builder `plan_test.go` already uses for an H.264 1080p source. Use that name, or add `h264Info` returning one if none exists.

- [ ] **Step 2: Run them to see them fail**

Run: `go test -run 'TestPlanAudioLayout|TestPlanAudioKeeps|TestTheBitRates|TestPlanAlwaysWritesMP4|TestVersionIsTwo' ./pkg/transcode/standard/`
Expected: FAIL. `g.Default` is undefined, then the old copy-everything results come back.

- [ ] **Step 3: Implement**

In `plan.go`: set `const Version = 2` with a comment line "2: the MP4 layout (2026-10-06 MP4 standard design)", and add `Default bool \`json:",omitempty"\`` to `AudioPlan`. Then replace `directPlay`, `aacLayouts` and `planAudio`:

```go
// Audio actions (AudioPlan.Action).
const (
	AudioCopy = "copy"
	AudioAAC  = "aac"
	AudioAC3  = "ac3"
)

// AC3BitRate and AC3Layout are a surround track encoded to AC-3 (spec §3);
// AACBitRatePerChannel makes AAC stereo 160 kbps and mono 80 kbps.
const (
	AC3BitRate           = 640000
	AC3Layout            = "5.1"
	AACBitRatePerChannel = 80000
)

// aacLayouts are the layouts the standard encodes AAC in.
var aacLayouts = map[int32]string{1: "mono", 2: "stereo"}

// planAudio is spec §3: after the languages filter, per language in the
// order the source first has it, the primary track -- E-AC-3 or AC-3
// copied, else AC-3 5.1, plus an AAC 2.0 companion when it is surround;
// AAC alone when it is mono or stereo -- then each commentary track as AAC
// of at most 2 channels. Every other track is dropped. The source's
// default language keeps the default (ruling R4).
func planAudio(as []transcode.AudioStream, languages []string) []AudioPlan {
	type group struct {
		main, comments []int
	}
	var order []string
	groups := map[string]*group{}
	for _, i := range keptAudio(as, languages) {
		lang := as[i].Language
		g := groups[lang]
		if g == nil {
			g = &group{}
			groups[lang] = g
			order = append(order, lang)
		}
		if commentary(as[i]) {
			g.comments = append(g.comments, i)
		} else {
			g.main = append(g.main, i)
		}
	}
	var out []AudioPlan
	for _, lang := range order {
		g := groups[lang]
		if p := primaryAudio(as, g.main); p >= 0 {
			out = append(out, primaryPlans(as[p], int32(p))...)
		}
		for _, i := range g.comments {
			out = append(out, commentaryPlan(as[i], int32(i)))
		}
	}
	markDefault(out, as)
	return out
}

// keptAudio is the languages filter as before: only tracks in languages
// (commentary always), unless none matches, then every track.
func keptAudio(as []transcode.AudioStream, languages []string) []int {
	matched := false
	for _, a := range as {
		if len(languages) > 0 && !commentary(a) && slices.Contains(languages, a.Language) {
			matched = true
		}
	}
	var keep []int
	for i, a := range as {
		if !matched || commentary(a) || slices.Contains(languages, a.Language) {
			keep = append(keep, i)
		}
	}
	return keep
}

func channels(a transcode.AudioStream) int32 {
	if a.Channels <= 0 {
		return 2
	}
	return a.Channels
}

// primaryAudio is the language's primary track among idx: the first
// surround E-AC-3, else the first surround AC-3, else the track with the
// most channels (the first at a tie); -1 for none.
func primaryAudio(as []transcode.AudioStream, idx []int) int {
	rank := func(a transcode.AudioStream) (tier int, ch int32) {
		if channels(a) > 2 {
			switch a.Codec {
			case "eac3":
				return 3, 0
			case "ac3":
				return 2, 0
			}
			return 1, channels(a)
		}
		return 0, channels(a)
	}
	best := -1
	for _, i := range idx {
		if best < 0 {
			best = i
			continue
		}
		t, c := rank(as[i])
		bt, bc := rank(as[best])
		if t > bt || (t == bt && c > bc) {
			best = i
		}
	}
	return best
}

func aacPlan(i int32, lang string, ch int32, title string) AudioPlan {
	return AudioPlan{SourceIndex: i, Action: AudioAAC, Channels: ch, Layout: aacLayouts[ch],
		BitRate: AACBitRatePerChannel * int64(ch), Language: lang, Title: title}
}

func aacTitle(ch int32) string {
	if ch == 1 {
		return "Mono"
	}
	return "Stereo"
}

// primaryPlans is a primary track's one or two outputs (ruling R5 titles).
func primaryPlans(a transcode.AudioStream, i int32) []AudioPlan {
	ch := channels(a)
	if ch <= 2 {
		if a.Codec == "aac" {
			return []AudioPlan{{SourceIndex: i, Action: AudioCopy, Language: a.Language, Title: a.Title}}
		}
		return []AudioPlan{aacPlan(i, a.Language, ch, aacTitle(ch))}
	}
	surround := AudioPlan{SourceIndex: i, Action: AudioCopy, Language: a.Language, Title: a.Title}
	if a.Codec != "eac3" && a.Codec != "ac3" {
		surround = AudioPlan{SourceIndex: i, Action: AudioAC3, Channels: 6, Layout: AC3Layout, BitRate: AC3BitRate,
			Language: a.Language, Title: "Dolby Digital 5.1"}
	}
	return []AudioPlan{surround, aacPlan(i, a.Language, 2, "Stereo")}
}

// commentaryPlan is a commentary track as AAC of at most 2 channels:
// copied when it already is.
func commentaryPlan(a transcode.AudioStream, i int32) AudioPlan {
	ch := min(channels(a), 2)
	p := AudioPlan{SourceIndex: i, Action: AudioCopy, Language: a.Language, Title: a.Title, Comment: true}
	if a.Codec != "aac" || channels(a) > 2 {
		p = aacPlan(i, a.Language, ch, a.Title)
		p.Comment = true
	}
	return p
}

// markDefault flags the first non-commentary output of the language whose
// source track carries the default flag, else the first output (R4).
func markDefault(out []AudioPlan, as []transcode.AudioStream) {
	if len(out) == 0 {
		return
	}
	lang, found := "", false
	for _, p := range out {
		if !p.Comment && as[p.SourceIndex].Disposition.Default {
			lang, found = p.Language, true
			break
		}
	}
	for i := range out {
		if !out[i].Comment && (!found || out[i].Language == lang) {
			out[i].Default = true
			return
		}
	}
	out[0].Default = true
}
```

In `Plan`, set the container unconditionally: replace the `Container: profile.Container` and `if p.Container == ""` block with `Container: transcode.ContainerMP4`, and set `p.Attachments = false`. Keep `Profile.Container` and document it: "ignored since Version 2: the standard writes MP4". Keep the `audioAsIs` computation, which still means every source track copied with the count unchanged.

Rewrite the package doc's audio and subtitle sentences to the new layout ("one MP4: ...").

- [ ] **Step 4: Run the package**

Run: `go test ./pkg/transcode/standard/ 2>&1 | tail -30`
Expected: the new tests PASS. Existing tests that asserted the old layout fail: MKV output, `directPlay` copies, attachments, golden hashes. Read each failure. A test of the old audio rule or the MKV container is updated to the new expectation; a golden is regenerated with the package's update flag. Any other failure is a bug in Step 3: fix it, don't loosen the test. Re-run until green.

- [ ] **Step 5: Commit**

```bash
git commit -m "feat(standard): Version 2 plans one MP4 -- per language E-AC-3 or AC-3 copied, else AC-3 5.1, with an AAC 2.0 companion; AAC alone for mono or stereo; commentary AAC; other mixes dropped" -- pkg/transcode/standard test/data/transcode/standard
```

---

### Task 4: the standard plans subtitles, sidecars and the image-subtitle hold

**Files:**
- Modify: `pkg/transcode/standard/plan.go` (`Result.Subtitles` type, the new `SubtitlePlan`, `SidecarPlan`, `Result.Sidecars`, `Result.Dropped`, `HoldImageSubtitles`, `planSubtitles`)
- Modify: `pkg/transcode/engine/graft.go` `CopyPlan` (builds `[]SubtitlePlan`) and `pkg/transcode/engine/engine.go` `planSlots` (reads `[]SubtitlePlan`), only enough to compile and copy as before; Task 6 adds conversion
- Modify: `app/squash/controller/transcodejob/plan.go` `standardStatusPlan` (`SubtitleTracks` from the plans' source indexes)
- Test: `pkg/transcode/standard/plan_test.go`, `pkg/transcode/standard/parity_test.go`

**Interfaces:**
- Produces:

```go
type SubtitlePlan struct {
	SourceIndex     int32  // type-relative
	Action          string // SubtitleCopy (a mov_text source) | SubtitleMovText (SubRip, WebVTT, plain text converted)
	Codec           string // the source codec, which the converter reads
	Language, Title string `json:",omitempty"`
	HearingImpaired bool   `json:",omitempty"`
}
type SidecarPlan struct {
	SourceIndex int32  // type-relative subtitle
	Format      string // SidecarASS (the stream copied out) | SidecarSRT (text converted)
	Codec       string // the source codec
	Suffix      string // after the video's stem: "en.ass", "en.forced.srt"
}
const (
	SubtitleCopy, SubtitleMovText = "copy", "movtext"
	SidecarASS, SidecarSRT        = "ass", "srt"
)
const HoldImageSubtitles = "image subtitles (PGS, DVD) wait for OCR: MP4 standard phase 2"
// Result.Subtitles []SubtitlePlan; Result.Sidecars []SidecarPlan; Result.Dropped []string
```

- Consumes: Task 3's Plan.

- [ ] **Step 1: Write the failing tests**

```go
func sub(codec, lang, title string, forced, hi bool) transcode.SubtitleStream {
	return transcode.SubtitleStream{Codec: codec, Language: lang, Title: title,
		Disposition: transcode.Disposition{Forced: forced, HearingImpaired: hi}}
}

func TestPlanSubtitles(t *testing.T) {
	subs, sidecars, dropped := planSubtitles([]transcode.SubtitleStream{
		sub("subrip", "en", "", false, false),                 // 0: mov_text
		sub("subrip", "en", "SDH", false, true),               // 1: mov_text, HI kept
		sub("webvtt", "es", "", false, false),                 // 2: mov_text
		sub("mov_text", "fr", "", false, false),               // 3: copied
		sub("ass", "en", "Full Subs", false, false),           // 4: en.ass
		sub("ass", "en", "Signs & Songs", false, false),       // 5: en.forced.ass (title)
		sub("subrip", "de", "Forced", true, false),            // 6: de.forced.srt
		sub("ass", "en", "Honorifics", false, false),          // 7: dropped, en.ass taken (R3)
		sub("ass", "", "", false, false),                      // 8: ass (untagged)
		sub("eia_608", "en", "", false, false),                // 9: dropped, unknown text codec
	})
	require.Len(t, subs, 4)
	assert.Equal(t, []int32{0, 1, 2, 3}, []int32{subs[0].SourceIndex, subs[1].SourceIndex, subs[2].SourceIndex, subs[3].SourceIndex})
	assert.Equal(t, SubtitleMovText, subs[0].Action)
	assert.True(t, subs[1].HearingImpaired)
	assert.Equal(t, SubtitleMovText, subs[2].Action)
	assert.Equal(t, SubtitleCopy, subs[3].Action)
	assert.Equal(t, []SidecarPlan{
		{SourceIndex: 4, Format: SidecarASS, Codec: "ass", Suffix: "en.ass"},
		{SourceIndex: 5, Format: SidecarASS, Codec: "ass", Suffix: "en.forced.ass"},
		{SourceIndex: 6, Format: SidecarSRT, Codec: "subrip", Suffix: "de.forced.srt"},
		{SourceIndex: 8, Format: SidecarASS, Codec: "ass", Suffix: "ass"},
	}, sidecars)
	require.Len(t, dropped, 2)
	assert.Contains(t, dropped[0], "subtitle 7")
	assert.Contains(t, dropped[1], "subtitle 9")
}

func TestAnImageSubtitleHoldsTheFile(t *testing.T) {
	info := h264Info()
	info.Subtitles = []transcode.SubtitleStream{sub("subrip", "en", "", false, false),
		{Codec: "hdmv_pgs_subtitle", Bitmap: true, Language: "en"}}
	p := Plan(info, Profile{Name: "p", Hash: "h"}, Hardware{Tier: transcode.TierCPUx265})
	assert.Equal(t, DecisionSkip, p.Decision)
	assert.Equal(t, HoldImageSubtitles, p.Reason)
}

func TestTheExpectationCountsOnlyEmbeddedSubtitles(t *testing.T) {
	info := h264Info()
	info.Subtitles = []transcode.SubtitleStream{sub("subrip", "en", "", false, false), sub("ass", "en", "", false, false)}
	p := Plan(info, Profile{Name: "p", Hash: "h"}, Hardware{Tier: transcode.TierCPUx265})
	assert.Equal(t, int32(1), p.Expect.SubtitleStreams)
	assert.Len(t, p.Sidecars, 1)
}
```

Extend `parity_test.go`: its fixture is probed by `FromProbe` and planned from `FromSummary`. Give it an ASS track titled "Signs" and a forced SRT, in its clip or its recorded probe (read the test first; it states which), and assert the two plans' `Hash()` are equal. That is Review Focus 5.

- [ ] **Step 2: Run them to see them fail**

Run: `go test -run 'TestPlanSubtitles|TestAnImageSubtitle|TestTheExpectationCounts|Parity' ./pkg/transcode/standard/`
Expected: FAIL to compile, `undefined: planSubtitles`.

- [ ] **Step 3: Implement**

```go
// textSubtitles become mov_text; assSubtitles go beside the file.
var (
	textSubtitles = []string{"subrip", "srt", "text", "webvtt", "mov_text"}
	assSubtitles  = []string{"ass", "ssa"}
)

// forcedSubtitle: the forced flag, or a "Signs & Songs" style title (R2).
func forcedSubtitle(s transcode.SubtitleStream) bool {
	t := strings.ToLower(s.Title)
	return s.Disposition.Forced || strings.Contains(t, "sign") || strings.Contains(t, "song")
}

// sidecarSuffix is <lang>[.forced|.sdh].<ext>, or [forced.|sdh.]<ext>
// with no language (pkg/subtitles.ParseSidecar's grammar).
func sidecarSuffix(s transcode.SubtitleStream, forced bool, ext string) string {
	var parts []string
	if s.Language != "" {
		parts = append(parts, s.Language)
	}
	switch {
	case forced:
		parts = append(parts, "forced")
	case s.Disposition.HearingImpaired:
		parts = append(parts, "sdh")
	}
	return strings.Join(append(parts, ext), ".")
}

// planSubtitles is spec §4 with rulings R2 and R3: text that is not forced
// embedded as mov_text; ASS, and forced text, as sidecars, one per name;
// anything else dropped and named. Bitmap streams never reach it (Plan
// holds the file first).
func planSubtitles(ss []transcode.SubtitleStream) (subs []SubtitlePlan, sidecars []SidecarPlan, dropped []string) {
	subs = []SubtitlePlan{}
	taken := map[string]bool{}
	side := func(i int, s transcode.SubtitleStream, format, ext string) {
		suffix := sidecarSuffix(s, forcedSubtitle(s), ext)
		if taken[suffix] {
			dropped = append(dropped, fmt.Sprintf("subtitle %d (%s %q): a second %s sidecar", i, s.Codec, s.Title, suffix))
			return
		}
		taken[suffix] = true
		sidecars = append(sidecars, SidecarPlan{SourceIndex: int32(i), Format: format, Codec: s.Codec, Suffix: suffix})
	}
	for i, s := range ss {
		switch {
		case slices.Contains(assSubtitles, s.Codec):
			side(i, s, SidecarASS, "ass")
		case slices.Contains(textSubtitles, s.Codec) && forcedSubtitle(s):
			side(i, s, SidecarSRT, "srt")
		case slices.Contains(textSubtitles, s.Codec):
			action := SubtitleMovText
			if s.Codec == "mov_text" {
				action = SubtitleCopy
			}
			subs = append(subs, SubtitlePlan{SourceIndex: int32(i), Action: action, Codec: s.Codec,
				Language: s.Language, Title: s.Title, HearingImpaired: s.Disposition.HearingImpaired})
		default:
			dropped = append(dropped, fmt.Sprintf("subtitle %d (%s): a codec the MP4 standard does not carry", i, s.Codec))
		}
	}
	return subs, sidecars, dropped
}
```

In `Plan`:
1. After the Dolby Vision profile 5 skip, add the hold: `for _, s := range info.Subtitles { if s.Bitmap { return skip(p, HoldImageSubtitles) } }`.
2. Replace the `p.Subtitles` loop with `p.Subtitles, p.Sidecars, p.Dropped = planSubtitles(info.Subtitles)`.
3. Set `Expect.SubtitleStreams` to `len(p.Subtitles)`.
4. Extend the as-is condition: `subsAsIs := len(p.Sidecars) == 0 && len(p.Dropped) == 0 && len(p.Subtitles) == len(info.Subtitles)` with every subtitle `SubtitleCopy`, and `len(info.Attachments) == 0`. The skip case becomes `compliant && audioAsIs && subsAsIs && sameContainer`, with the reason "already HEVC in the MP4 standard's layout".

In `engine.planSlots`, iterate `plan.Subtitles` as `SubtitlePlan`s by `.SourceIndex`, copy slots as before. In `engine.CopyPlan`, append `standard.SubtitlePlan{SourceIndex: ns, Action: standard.SubtitleCopy, Codec: s.Codec}`. In `standardStatusPlan`, build `SubtitleTracks` from the source indexes. Run `go build ./...`.

- [ ] **Step 4: Run the packages**

Run: `go test ./pkg/transcode/... ./app/squash/controller/transcodejob/ 2>&1 | tail -30`
Expected: the new tests PASS. Old subtitle expectations, such as every subtitle copied into MKV, are updated to the new layout after reading each one. The engine's existing tests may fail on the MP4 container, where the copied SRT is now a `mov_text` slot that Task 6 converts. Leave those failing only if their names show it is the conversion. Otherwise fix them now.

- [ ] **Step 5: Commit**

```bash
git commit -m "feat(standard): subtitles in the MP4 layout -- text as mov_text, ASS and forced text as sidecars (one per name), other codecs dropped and named, and image subtitles hold the file for phase 2's OCR" -- pkg/transcode app/squash/controller/transcodejob/plan.go
```

---

### Task 5: the engine encodes several audio outputs from one source

**Files:**
- Modify: `pkg/transcode/engine/engine.go` (`stageContext`, Run's stage loop, `demuxPaced`, `planSlots`, `addStreams`)
- Modify: `pkg/transcode/engine/audio.go` (`audioStage` takes every encode output of its source)
- Modify: `pkg/transcode/engine/video.go` and `graft.go` only where they read `sc.emit`/`sc.setup`; the variadic `setup` keeps their calls compiling
- Test: `pkg/transcode/engine/audio_test.go`

**Interfaces:**
- Produces:
  - `stageContext.setup func(...ffgo.EncodedStreamSource) error`, which registers each output's encoder in order and then waits for the header;
  - `stageContext.emits []func(*ffgo.Packet) error`, one per output, with `emit` equal to `emits[0]`;
  - `slot.fed bool`: an output another slot's stage encodes;
  - `audioStage(outs []standard.AudioPlan) stageFunc`;
  - `audioOptions(src *ffgo.StreamInfo, a standard.AudioPlan, copied bool) ffgo.StreamOptions`.
- Consumes: Task 3's `AudioPlan` (`Action`, `Default`, `Title`, `Comment`, `Layout`, `BitRate`).

- [ ] **Step 1: Write the failing test**

```go
// A DTS-like 5.1 source (FLAC 5.1 in the clip) becomes AC-3 5.1 and AAC
// 2.0 from one decode; an E-AC-3 5.1 source is copied and gains AAC 2.0;
// titles and the default flag are the plan's (spec §3).
func TestTheEngineWritesTheMP4AudioLayout(t *testing.T) {
	ffmpeg9OrSkip(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "src.mkv")
	run(t, "ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=320x180:rate=24:duration=3",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=3,aformat=channel_layouts=5.1",
		"-f", "lavfi", "-i", "sine=frequency=660:sample_rate=48000:duration=3,aformat=channel_layouts=5.1",
		"-map", "0", "-map", "1", "-map", "2",
		"-c:v", "libx265", "-preset", "ultrafast", "-pix_fmt", "yuv420p10le",
		"-c:a:0", "flac", "-c:a:1", "eac3",
		"-metadata:s:a:0", "language=jpn", "-metadata:s:a:0", "title=FLAC 5.1",
		"-metadata:s:a:1", "language=eng", "-disposition:a:0", "default", src)
	info := probeInfo(t, src) // the transcode.MediaInfo FromProbe gives, as the worker builds it
	plan := standard.Plan(info, standard.Profile{Name: "p", Hash: "h"}, standard.Hardware{Tier: transcode.TierCPUx265})
	require.Equal(t, standard.DecisionCopyVideo, plan.Decision)
	out := filepath.Join(dir, "out.mp4")
	_, err := Run(context.Background(), plan, src, out, Options{})
	require.NoError(t, err)
	p := ffprobeJSON(t, out)
	var auds []string
	for _, s := range p.Streams {
		if s.CodecType == "audio" {
			auds = append(auds, fmt.Sprintf("%s/%d/%s/%d", s.CodecName, s.Channels, s.Tags["title"], s.Disposition["default"]))
		}
	}
	assert.Equal(t, []string{
		"ac3/6/Dolby Digital 5.1/1", "aac/2/Stereo/0", // jpn: FLAC 5.1 encoded twice from one decode
		"eac3/6//0", "aac/2/Stereo/0", // eng: E-AC-3 copied, plus its companion
	}, auds)
	rep, err := Verify(context.Background(), src, out, plan.Expect)
	require.NoError(t, err)
	assert.True(t, rep.OK, "%v", rep.Problems)
}
```

`probeInfo` builds `transcode.MediaInfo` through `mediainfo.Probe` and `transcode.FromProbe`, as the worker does. Look for an existing helper in the engine tests first (`grep -n FromProbe pkg/transcode/engine/*_test.go`) and reuse it; otherwise add it to `helpers_test.go`.

- [ ] **Step 2: Run it to see it fail**

Run: `go test -count=1 -run TestTheEngineWritesTheMP4AudioLayout -v ./pkg/transcode/engine/`
Expected: FAIL. `planSlots` gives the ac3 plan an AAC stage, and the second output of source 0 gets its own stage, which never receives packets: `routes` holds one channel per source, so the run fails or the track list differs.

- [ ] **Step 3: Implement**

1. **The `stageContext` in `engine.go`:**

```go
type stageContext struct {
	in    <-chan *ffgo.Packet
	dec   *ffgo.Decoder
	src   *ffgo.StreamInfo
	opts  Options
	setup func(...ffgo.EncodedStreamSource) error // one per output, in order; then waits for the header
	emits []func(*ffgo.Packet) error              // one per output
	emit  func(*ffgo.Packet) error                // emits[0]
}
```

2. **Run's stage loop:**
   - Iterate the slots. Skip a slot that is `copy` or `fed`.
   - Collect the stage's outputs: the slot itself, plus every later slot with `fed`, the same `src.Index` and the same `donor` flag, in slot order (`outs []int`). The donor's stream 0 and the target's stream 0 share an index, so the flag is part of the key (Task 8 relies on it).
   - `setup` sends one `setupItem{slot: outs[k], src: srcs[k]}` per output, then waits on `headerDone`.
   - `emits[k]` clones and sends `muxItem{slot: outs[k]}`, as `emit` does today.
   - `mux`'s `waiting` counts every slot that is not `copy`. Fed slots are counted, since each sends a setup.

3. **`demuxPaced`:** a source may be both encoded and copied. Replace the `if encoded { ...; continue }` with:

```go
if encoded {
	sent := c
	if copied {
		if sent, err = c.Clone(); err != nil {
			_ = c.Free()
			return &Error{Stage: "demux", Err: err}
		}
	}
	select {
	case in <- sent:
	case <-ctx.Done():
		_ = sent.Free()
		if sent != c {
			_ = c.Free()
		}
		return ctx.Err()
	}
	if !copied {
		continue
	}
}
// the copied path, unchanged, sends c to muxCh
```

4. **`planSlots`:** for each `plan.Audio` entry:
   - `copy` is a copy slot, `opts: ptr(audioOptions(s, a, true))`.
   - An encode is a slot with `opts: ptr(audioOptions(s, a, false))`. Its `stage` is `audioStage(encodes of the same SourceIndex, in plan order)` when it is the first encode of that source, else `fed: true`.

```go
// audioOptions are an output audio track's tags: the source's language and
// metadata, the plan's title when it names one (R5), and the plan's default
// and comment flags in place of the source's.
func audioOptions(src *ffgo.StreamInfo, a standard.AudioPlan, copied bool) ffgo.StreamOptions {
	o := streamOptions(src, copied)
	if a.Title != "" || !copied {
		o.Title = a.Title
	}
	o.Disposition &^= ffgo.DispositionDefault | ffgo.DispositionComment
	if a.Default {
		o.Disposition |= ffgo.DispositionDefault
	}
	if a.Comment {
		o.Disposition |= ffgo.DispositionComment
	}
	return o
}
```

5. **`audio.go`:** `audioStage(outs []standard.AudioPlan)` builds one encoder per output. Each has `EncoderName` `"ac3"` for `AudioAC3`, else `"aac"`. Rates come from `outputRate(action, src)`: AAC uses the existing `maxAACRate` rule; AC-3 keeps a source rate of 32000, 44100 or 48000, else 48000. It calls `sc.setup(encs...)` once, and builds one lazily-created resampler per output from the first frame. `encode(f)` loops the outputs: resample into `outs[k].Layout` at `rate[k]` as FLTP, set the PTS, `encs[k].Encode(r, sc.emits[k])`. Flush each resampler tail and each encoder at the end, in output order.

6. **`addStreams`:** the existing `if s.opts != nil { opts = *s.opts }` already applies the options. Nothing to change there.

7. **`graftSlots`:** start each copied audio slot's override from `s.opts` when set, not `streamOptions(...)`, so the plan's flags survive: `o := streamOptions(s.src, s.copy); if s.opts != nil { o = *s.opts }`.

- [ ] **Step 4: Run the engine suite**

Run: `go test -count=1 ./pkg/transcode/engine/ 2>&1 | tail -30`
Expected: the new test PASS. The existing audio, graft and container tests PASS. A test that asserted the old copy-everything audio is updated after reading it; any other failure is a Step 3 bug.

- [ ] **Step 5: Commit**

```bash
git commit -m "feat(engine): one audio decode feeds several encoders (AC-3 5.1 and AAC 2.0), a stream can be copied and encoded at once, and each track's title and default flag are the plan's" -- pkg/transcode/engine
```

---

### Task 6: the engine converts text subtitles to `mov_text`

**Files:**
- Create: `pkg/transcode/engine/movtext.go`
- Modify: `pkg/transcode/engine/engine.go` (`slot.convert`, `planSlots`, `addStreams`, `mux`'s `write`)
- Test: `pkg/transcode/engine/movtext_test.go`

**Interfaces:**
- Produces:
  - `plainText(codec string, b []byte) string`: a cue's text without markup;
  - `movTextSample(text string) []byte`;
  - `movTextParameters(src avcodec.Parameters) (avcodec.Parameters, error)`;
  - `slot.convert func([]byte) []byte`, which the muxer applies to each packet's payload.
- Consumes: Task 1's `ffgo.NewPacketFromData`, `(*Packet).Data`, `avcodec.SetCodecParCodecID` and `SetCodecParExtradata`, and Task 4's `SubtitlePlan`.

- [ ] **Step 1: Write the failing tests**

```go
func TestPlainText(t *testing.T) {
	for _, tc := range []struct{ codec, in, want string }{
		{"subrip", "<i>Hello</i>\r\nthere", "Hello\nthere"},
		{"subrip", "{\\an8}<font color=\"#ff0\">Top</font>", "Top"},
		{"webvtt", "<v Bob>Hi &amp; bye</v>", "Hi & bye"},
		{"webvtt", "<c.yellow>Warn</c>", "Warn"},
		{"mov_text", "\x00\x05Hello", "Hello"},
		{"mov_text", "\x00\x02Hi\x00\x00\x00\x0cstyl\x00\x00\x00\x00", "Hi"},
		{"subrip", "AT&T", "AT&T"},
	} {
		assert.Equal(t, tc.want, plainText(tc.codec, []byte(tc.in)), "%s %q", tc.codec, tc.in)
	}
}

func TestMovTextSample(t *testing.T) {
	assert.Equal(t, []byte("\x00\x05Hello"), movTextSample("Hello"))
	assert.Equal(t, []byte{0, 0}, movTextSample(""))
	long := strings.Repeat("é", 40000) // 80000 bytes: cut on a rune boundary at 65535
	s := movTextSample(long)
	n := int(binary.BigEndian.Uint16(s))
	assert.Equal(t, n, len(s)-2)
	assert.True(t, utf8.Valid(s[2:]))
}

// An MKV's SubRip track lands in the MP4 as mov_text (tx3g), cue text and
// timing intact, the language kept.
func TestTheEngineWritesSubRipAsMovText(t *testing.T) {
	ffmpeg9OrSkip(t)
	dir := t.TempDir()
	srt := filepath.Join(dir, "en.srt")
	require.NoError(t, os.WriteFile(srt, []byte("1\n00:00:00,500 --> 00:00:01,500\n<i>Hello</i>\n"), 0o644))
	in := filepath.Join(dir, "in.mkv")
	run(t, "ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=320x180:rate=24:duration=3", "-i", srt,
		"-map", "0", "-map", "1", "-c:v", "libx265", "-preset", "ultrafast", "-pix_fmt", "yuv420p10le",
		"-c:s", "srt", "-metadata:s:s:0", "language=eng", in)
	info := probeInfo(t, in)
	plan := standard.Plan(info, standard.Profile{Name: "p", Hash: "h"}, standard.Hardware{Tier: transcode.TierCPUx265})
	out := filepath.Join(dir, "out.mp4")
	_, err := Run(context.Background(), plan, in, out, Options{})
	require.NoError(t, err)
	p := ffprobeJSON(t, out)
	var subs []string
	for _, s := range p.Streams {
		if s.CodecType == "subtitle" {
			subs = append(subs, s.CodecName+"/"+s.Tags["language"])
		}
	}
	assert.Equal(t, []string{"mov_text/eng"}, subs)
	cues := run(t, "ffmpeg", "-hide_banner", "-loglevel", "error", "-i", out, "-map", "0:s:0", "-f", "srt", "-")
	assert.Contains(t, cues, "00:00:00,500 --> 00:00:01,500")
	assert.Contains(t, cues, "Hello")
	assert.NotContains(t, cues, "<i>")
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test -count=1 -run 'TestPlainText|TestMovTextSample|TestTheEngineWritesSubRipAsMovText' ./pkg/transcode/engine/`
Expected: FAIL to compile, `undefined: plainText`.

- [ ] **Step 3: Implement `movtext.go`**

```go
// movTextSampleEntry is the 3GPP TextSampleEntry (TS 26.245) every
// converted track carries as extradata (ruling R6): no display flags,
// centred at the bottom, a transparent background, an empty default box,
// one style record (font 1, 18 px, opaque white) and a font table naming
// "Serif" -- what FFmpeg's mov_text encoder writes for a style-less track.
var movTextSampleEntry = []byte{
	0x00, 0x00, 0x00, 0x00, // displayFlags
	0x01, 0xFF, // horizontal centre, vertical bottom
	0x00, 0x00, 0x00, 0x00, // background RGBA
	0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, // default text box
	0x00, 0x00, 0x00, 0x00, 0x00, 0x01, 0x00, 0x12, 0xFF, 0xFF, 0xFF, 0xFF, // style record
	0x00, 0x00, 0x00, 0x12, 'f', 't', 'a', 'b', 0x00, 0x01, 0x00, 0x01, 0x05, 'S', 'e', 'r', 'i', 'f', // font table
}

var (
	markupTag   = regexp.MustCompile(`</?[^<>]*>`)
	assOverride = regexp.MustCompile(`\{[^{}]*\}`)
)

// plainText is one cue's text as Plex shows it, from a packet of codec:
// HTML-like tags (SubRip's <i> and <font>, WebVTT's <c.x> and <v Name>)
// and ASS override blocks ({\an8}) removed, WebVTT's entities decoded,
// CRLF made LF and the ends trimmed. A mov_text sample's text is its
// length-prefixed run, its style boxes dropped.
func plainText(codec string, b []byte) string {
	if codec == "mov_text" {
		if len(b) < 2 {
			return ""
		}
		n := min(int(binary.BigEndian.Uint16(b)), len(b)-2)
		return strings.TrimSpace(string(b[2 : 2+n]))
	}
	s := strings.ReplaceAll(string(b), "\r\n", "\n")
	s = assOverride.ReplaceAllString(s, "")
	s = markupTag.ReplaceAllString(s, "")
	if codec == "webvtt" {
		s = html.UnescapeString(s)
	}
	return strings.TrimSpace(s)
}

// movTextSample is text as one mov_text sample: its UTF-8 length, 16-bit
// big-endian, then the text, cut on a rune boundary to fit.
func movTextSample(text string) []byte {
	for len(text) > 0xFFFF {
		_, size := utf8.DecodeLastRuneInString(text[:0xFFFF+1])
		text = text[:0xFFFF+1-size]
	}
	b := make([]byte, 2+len(text))
	binary.BigEndian.PutUint16(b, uint16(len(text)))
	copy(b[2:], text)
	return b
}

// movTextParameters are src's parameters as a mov_text stream: codec id
// mov_text, the sample entry as extradata, no codec tag (the MP4 muxer
// writes tx3g). The caller frees them.
func movTextParameters(src avcodec.Parameters) (avcodec.Parameters, error) {
	par, err := outputParameters(src, 0)
	if err != nil {
		return nil, err
	}
	avcodec.SetCodecParCodecID(par, avcodec.CodecIDByName("mov_text"))
	if err := avcodec.SetCodecParExtradata(par, movTextSampleEntry); err != nil {
		avcodec.ParametersFree(&par)
		return nil, err
	}
	return par, nil
}
```

Check the `movTextSample` loop's arithmetic against the test. It must leave at most 65535 bytes, ending on a rune boundary. Simplify it if a clearer loop does the same.

Then the engine:
- **`planSlots`:** a `SubtitleMovText` plan becomes a copy slot with `convert: func(b []byte) []byte { return movTextSample(plainText(codec, b)) }`. `SubtitleCopy` stays a plain copy slot.
- **`addStreams`:** for a copy slot with `convert != nil`, build `par` with `movTextParameters(s.src.CodecParameters())` instead of `outputParameters`.
- **`mux`'s `write`:** before `m.WritePacket`, when `s.convert != nil`:

```go
np, err := ffgo.NewPacketFromData(s.convert(it.pkt.Data()))
if err != nil {
	return err
}
raw, old := np.Raw(), it.pkt.Raw()
avcodec.SetPacketPTS(raw, it.pkt.PTS())
avcodec.SetPacketDTS(raw, it.pkt.DTS())
avcodec.SetPacketDuration(raw, avcodec.GetPacketDuration(old))
avcodec.SetPacketFlags(raw, avcodec.GetPacketFlags(old))
_ = it.pkt.Free()
it.pkt = np
```

`write`'s deferred free must free the packet `it.pkt` holds after the swap. It does if the defer reads `it.pkt` at return: make it `defer func() { _ = it.pkt.Free() }()`, which it already is. Confirm it is not bound earlier.

- [ ] **Step 4: Run the engine suite**

Run: `go test -count=1 ./pkg/transcode/engine/ 2>&1 | tail -30`
Expected: PASS, the subtitle tests left from Task 4 included.

- [ ] **Step 5: Commit**

```bash
git add pkg/transcode/engine/movtext.go pkg/transcode/engine/movtext_test.go
git commit -m "feat(engine): SubRip, WebVTT and text subtitles become mov_text in the MP4 -- their text without markup, one sample per cue (ruling R6)" -- pkg/transcode/engine
```

---

### Task 7: the engine writes ASS and SRT sidecars beside its output

**Files:**
- Create: `pkg/transcode/engine/sidecar.go`
- Modify: `pkg/transcode/engine/engine.go` (`slot.sidecar`, `planSlots`, `addStreams`, `mux`, the failure cleanup in `Run`'s deferred removal)
- Test: `pkg/transcode/engine/sidecar_test.go`

**Interfaces:**
- Produces:
  - `type sidecarSink interface { write(p *ffgo.Packet, tb ffgo.Rational) error; close() error }`;
  - `newASSSidecar(path string, src *ffgo.StreamInfo) (*assSidecar, error)`;
  - `newSRTSidecar(path, codec string) *srtSidecar`.
  - Each `standard.SidecarPlan` is written at `fsops.SidecarPath(output, plan.Suffix)`, and removed with the output when the run fails.
- Consumes: Task 2's `fsops.SidecarPath` and Task 4's `SidecarPlan`.

- [ ] **Step 1: Write the failing tests**

Write `sidecar_test.go` (package `engine`):

```go
// An ASS stream copy-muxes into FFmpeg's ass muxer: the sidecar path (§4).
func TestAnASSStreamCopyMuxesToAnASSFile(t *testing.T) {
	ffmpeg9OrSkip(t)
	dir := t.TempDir()
	ass := filepath.Join(dir, "in.ass")
	require.NoError(t, os.WriteFile(ass, []byte(assFixture), 0o644))
	src := filepath.Join(dir, "ass.mkv")
	run(t, "ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=320x180:rate=24:duration=2", "-i", ass,
		"-c:v", "libx264", "-preset", "veryfast", "-c:s", "copy", src)
	d, err := ffgo.NewDecoder(src)
	require.NoError(t, err)
	defer d.Close()
	var sub *ffgo.StreamInfo
	for _, s := range d.Streams() {
		if s.Type == ffgo.MediaTypeSubtitle {
			sub = s
		}
	}
	require.NotNil(t, sub)
	out := filepath.Join(dir, "out.ass")
	sink, err := newASSSidecar(out, sub)
	require.NoError(t, err)
	for {
		p, err := d.ReadPacket()
		require.NoError(t, err)
		if p == nil {
			break
		}
		if p.StreamIndex() == sub.Index {
			require.NoError(t, sink.write(p, sub.TimeBase))
		}
	}
	require.NoError(t, sink.close())
	b, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Contains(t, string(b), "[Script Info]")
	assert.Contains(t, string(b), "Dialogue: 0,0:00:00.50,0:00:01.50,Default,,0,0,0,,Hello")
}

const assFixture = "[Script Info]\nScriptType: v4.00+\n\n[V4+ Styles]\n" +
	"Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding\n" +
	"Style: Default,Arial,20,&H00FFFFFF,&H000000FF,&H00000000,&H00000000,0,0,0,0,100,100,0,0,1,1,0,2,10,10,10,1\n\n" +
	"[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n" +
	"Dialogue: 0,0:00:00.50,0:00:01.50,Default,,0,0,0,,Hello\n"

func TestSRTSidecarFormatsCues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.en.forced.srt")
	s := newSRTSidecar(path, "subrip")
	tb := ffgo.NewRational(1, 1000)
	s.add(500, 1000, tb, []byte("<i>Sign</i>"))
	s.add(3_723_004, 1500, tb, []byte("Two\r\nlines"))
	require.NoError(t, s.close())
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "1\n00:00:00,500 --> 00:00:01,500\nSign\n\n2\n01:02:03,004 --> 01:02:04,504\nTwo\nlines\n\n", string(b))
}

// A run writes the plan's sidecars beside its output, and a failed run
// leaves none.
func TestTheEngineWritesTheSidecarsBesideItsOutput(t *testing.T) {
	ffmpeg9OrSkip(t)
	dir := t.TempDir()
	ass := filepath.Join(dir, "in.ass")
	require.NoError(t, os.WriteFile(ass, []byte(assFixture), 0o644))
	srt := filepath.Join(dir, "forced.srt")
	require.NoError(t, os.WriteFile(srt, []byte("1\n00:00:00,500 --> 00:00:01,500\nSign\n"), 0o644))
	in := filepath.Join(dir, "in.mkv")
	run(t, "ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=320x180:rate=24:duration=3", "-i", ass, "-i", srt,
		"-map", "0", "-map", "1", "-map", "2", "-c:v", "libx265", "-preset", "ultrafast", "-pix_fmt", "yuv420p10le",
		"-c:s:0", "copy", "-c:s:1", "srt", "-metadata:s:s:0", "language=eng", "-metadata:s:s:1", "language=ger",
		"-disposition:s:1", "forced", in)
	plan := standard.Plan(probeInfo(t, in), standard.Profile{Name: "p", Hash: "h"}, standard.Hardware{Tier: transcode.TierCPUx265})
	require.Len(t, plan.Sidecars, 2)
	out := filepath.Join(dir, "Film.part-ab12cd34-1.mp4")
	_, err := Run(context.Background(), plan, in, out, Options{})
	require.NoError(t, err)
	for _, s := range plan.Sidecars {
		st, err := os.Stat(fsops.SidecarPath(out, s.Suffix))
		require.NoError(t, err, s.Suffix)
		assert.Positive(t, st.Size(), s.Suffix)
	}
	b, err := os.ReadFile(fsops.SidecarPath(out, plan.Sidecars[1].Suffix))
	require.NoError(t, err)
	assert.Contains(t, string(b), "Sign")

	// A cancelled run removes its sidecars with its output.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out2 := filepath.Join(dir, "Film.part-ab12cd34-2.mp4")
	_, err = Run(ctx, plan, in, out2, Options{})
	require.Error(t, err)
	for _, s := range plan.Sidecars {
		_, err := os.Stat(fsops.SidecarPath(out2, s.Suffix))
		assert.ErrorIs(t, err, os.ErrNotExist, s.Suffix)
	}
}
```

The cancelled run may fail before any sidecar is created, which still satisfies the test. A sharper check is a test-only hook that fails the muxer after the header. Use `closeMuxer`'s pattern if the plain cancel proves nothing: the assertion must cover sidecars that were created.

- [ ] **Step 2: Run them to see them fail**

Run: `go test -count=1 -run 'TestAnASSStream|TestSRTSidecar|TestTheEngineWritesTheSidecars' ./pkg/transcode/engine/`
Expected: FAIL to compile, `undefined: newSRTSidecar`.

- [ ] **Step 3: Implement**

`sidecar.go`:

```go
// sidecarSink is one sidecar file a run writes beside its output: fed the
// stream's packets on the muxer's goroutine, closed after the trailer.
type sidecarSink interface {
	write(p *ffgo.Packet, tb ffgo.Rational) error
	close() error
}

// assSidecar copies an ASS/SSA stream into FFmpeg's ass muxer: the
// script's header (the stream's extradata) and one Dialogue line per
// packet, timed from it. Styling stays; attached fonts are lost (spec §4).
type assSidecar struct {
	m  *ffgo.Muxer
	st *ffgo.MuxerStream
}

func newASSSidecar(path string, src *ffgo.StreamInfo) (*assSidecar, error) {
	m, err := ffgo.NewMuxer(path, "ass")
	if err != nil {
		return nil, err
	}
	par, err := outputParameters(src.CodecParameters(), 0)
	if err != nil {
		_ = m.Close()
		return nil, err
	}
	st, err := m.AddCopyStream(&ffgo.CopyStreamConfig{CodecParameters: par, TimeBase: src.TimeBase})
	avcodec.ParametersFree(&par)
	if err == nil {
		err = m.WriteHeader()
	}
	if err != nil {
		_ = m.Close()
		return nil, err
	}
	return &assSidecar{m: m, st: st}, nil
}

func (a *assSidecar) write(p *ffgo.Packet, _ ffgo.Rational) error { return a.m.WritePacket(a.st, p) }

func (a *assSidecar) close() error {
	err := a.m.WriteTrailer()
	if cerr := a.m.Close(); err == nil {
		err = cerr
	}
	return err
}

// srtSidecar collects a text stream's cues as SubRip, written at close
// through fsops.AtomicWrite.
type srtSidecar struct {
	path, codec string
	b           strings.Builder
	n           int
}

func newSRTSidecar(path, codec string) *srtSidecar { return &srtSidecar{path: path, codec: codec} }

func (s *srtSidecar) write(p *ffgo.Packet, tb ffgo.Rational) error {
	s.add(p.PTS(), avcodec.GetPacketDuration(p.Raw()), tb, p.Data())
	return nil
}

// add appends one cue starting at pts and lasting dur, both in tb.
func (s *srtSidecar) add(pts, dur int64, tb ffgo.Rational, payload []byte) {
	text := plainText(s.codec, payload)
	if text == "" || pts == avutil.AV_NOPTS_VALUE || tb.Den == 0 {
		return
	}
	ms := func(v int64) int64 { return v * 1000 * int64(tb.Num) / int64(tb.Den) }
	s.n++
	fmt.Fprintf(&s.b, "%d\n%s --> %s\n%s\n\n", s.n, srtTime(ms(pts)), srtTime(ms(pts+dur)), text)
}

func srtTime(ms int64) string {
	return fmt.Sprintf("%02d:%02d:%02d,%03d", ms/3_600_000, ms/60_000%60, ms/1000%60, ms%1000)
}

func (s *srtSidecar) close() error {
	return fsops.AtomicWrite(s.path, strings.NewReader(s.b.String()), 0o644)
}
```

In `engine.go`:
- **The slot:** add `sidecar *standard.SidecarPlan` and `sink sidecarSink`.
- **`planSlots`:** appends one copy slot per `plan.Sidecars` entry (`name: "sidecar:"+Suffix`), so the demuxer routes the stream's packets to the muxer.
- **`addStreams`:** for a sidecar slot, create its sink at `fsops.SidecarPath(output, Suffix)`: `newASSSidecar` for `SidecarASS`, `newSRTSidecar(path, sp.Codec)` for `SidecarSRT`. Pass `output` into `addStreams`. Add no muxer stream for it.
- **`mux`'s `write`:** a slot with a sink calls `sink.write(it.pkt, s.src.TimeBase)` and returns.
- **After the trailer:** close every sink in slot order, and return the first error as a `mux` error.
- **Run's deferred removal on failure:** also remove `fsops.SidecarPath(output, sp.Suffix)` for every planned sidecar, and close any sink still open first.

- [ ] **Step 4: Run the engine suite**

Run: `go test -count=1 ./pkg/transcode/engine/ 2>&1 | tail -30`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/transcode/engine/sidecar.go pkg/transcode/engine/sidecar_test.go
git commit -m "feat(engine): ASS tracks are copied out to .ass sidecars and forced text to .forced.srt beside the output, removed with it when a run fails" -- pkg/transcode/engine
```

---

### Task 8: a surround dub is grafted as AC-3 5.1 plus AAC 2.0

**Files:**
- Modify: `pkg/transcode/engine/graft.go` (`GraftAudio.Surround`, `GraftAudio.Tracks`, `donorBuffer`, `graftStage`, `encodeBlock`, `graftSlots`)
- Modify: `app/squash/worker/graft/graft.go` (Prepare sets `Surround`; the standalone check passes `len(plan.Audio)+p.audio.Tracks()`)
- Modify: `app/squash/worker/ffgo.go` `prepareGraft` (`Expect.AudioStreams += n`) and the joined check (`graft.index+n`)
- Test: `pkg/transcode/engine/graft_test.go`

**Interfaces:**
- Produces: `GraftAudio.Surround bool`, and `func (g GraftAudio) Tracks() int` (2 when `Surround`, else 1). The grafted tracks come after the plan's audio: AC-3 5.1 (`g.Title`, default when `g.Default`) then AAC 2.0 (`g.Title + " (Stereo)"`), both flagged dub.
- Consumes: Task 5's multi-output `stageContext`.

- [ ] **Step 1: Write the failing test**

In `graft_test.go`, copy the existing interleaving test's fixture builder (read `TestAGraftedTrackIsInterleavedWithTheVideo` for how it makes the target, the donor and the `Map`). Make the donor 5.1:

```go
func TestASurroundDubIsGraftedAsAC3AndAAC(t *testing.T) {
	ffmpeg9OrSkip(t)
	target, donor := graftFixture(t, "5.1") // the existing builder, its donor in the given layout
	g := GraftAudio{Donor: donor, Stream: 0, Map: func(s float64) (float64, bool) { return s, true },
		Language: "eng", Title: "English", Default: true, Surround: true}
	require.Equal(t, 2, g.Tracks())
	plan, err := CopyPlan(target)
	require.NoError(t, err)
	out := filepath.Join(t.TempDir(), "out"+filepath.Ext(target))
	_, err = Run(context.Background(), plan, target, out, Options{Graft: &g})
	require.NoError(t, err)
	var auds []string
	for _, s := range ffprobeJSON(t, out).Streams {
		if s.CodecType == "audio" {
			auds = append(auds, fmt.Sprintf("%s/%d/%s/%d/%d", s.CodecName, s.Channels, s.Tags["title"], s.Disposition["default"], s.Disposition["dub"]))
		}
	}
	n := len(auds)
	require.GreaterOrEqual(t, n, 3)
	assert.Equal(t, "ac3/6/English/1/1", auds[n-2])
	assert.Equal(t, "aac/2/English (Stereo)/0/1", auds[n-1])
}
```

If the existing builder has another name or signature, adapt the call, and give it a layout parameter if it lacks one.

- [ ] **Step 2: Run it to see it fail**

Run: `go test -count=1 -run TestASurroundDubIsGrafted -v ./pkg/transcode/engine/`
Expected: FAIL to compile, `unknown field Surround`.

- [ ] **Step 3: Implement**

1. **`GraftAudio`:** add `Surround bool` with the comment "the dub is surround: grafted as AC-3 5.1 plus an AAC 2.0 companion (MP4 standard §3)". Add `func (g GraftAudio) Tracks() int { if g.Surround { return 2 }; return 1 }`.

2. **`donorBuffer`:** gains `ch int`, the channels per sample. Replace the hard-coded 2 everywhere with `b.ch`:

```go
func (b *donorBuffer) end() int64 { return b.base + int64(len(b.s)/b.ch) }

// at writes the donor's sample at fractional index k into dst (b.ch
// values), linearly interpolated; zeros outside what the buffer holds.
func (b *donorBuffer) at(k float64, dst []float32) {
	j := int64(k)
	if k < 0 || j < b.base || j+1 >= b.end() {
		clear(dst)
		return
	}
	f := float32(k - float64(j))
	i := b.ch * int(j-b.base)
	for c := range b.ch {
		dst[c] = b.s[i+c]*(1-f) + b.s[i+b.ch+c]*f
	}
}

func (b *donorBuffer) trim(k int64) {
	if k <= b.base {
		return
	}
	n := min(k-b.base, int64(len(b.s)/b.ch))
	b.s = append(b.s[:0], b.s[int(n)*b.ch:]...)
	b.base += n
}
```

3. **`graftStage`:**
   - `layout, ch := GraftLayout, 2`, or `"5.1", 6` when `g.Surround`.
   - The donor resampler outputs packed FLT in `layout`. Use `packedSamples(r, ch)` and `buf := donorBuffer{ch: ch}`.
   - The PCM block is `make([]float32, ch*block)`, filled per sample with `buf.at(k, pcm[ch*i:ch*i+ch])`.
   - The outputs: `type graftOut struct { enc *ffgo.AudioEncoder; conv *ffgo.Resampler }`. With `Surround`:
     - an `ac3` encoder at `standard.AC3Layout`/`standard.AC3BitRate`, its `conv` packed 5.1 FLT to 5.1 FLTP;
     - an `aac` encoder at stereo/`graftBitRate`, its `conv` packed 5.1 FLT to stereo FLTP (swr downmixes).
   - Without it, the one AAC output as today.
   - Call `sc.setup(enc1, enc2...)` once.
   - `encodeBlock(o graftOut, layout string, ch int, pcm []float32, n int64, emit)` builds the frame in `layout` with `len(pcm)/ch` samples and resamples through `o.conv`. It is called per output with `sc.emits[k]`.
   - Flushes each `conv` tail and each encoder in output order.

4. **`graftSlots`:** the first grafted slot (`stage: graftStage(...)`) with `opts` as today. With `Surround`, a second slot `{src: same, name: "graft:stereo", donor: true, fed: true, opts: {Title: g.Title + " (Stereo)", Disposition: ffgo.DispositionDub}}` inserted right after it. Task 5's loop gathers it by `(donor, src.Index)`.

5. **The worker:**
   - `app/squash/worker/graft/graft.go` Prepare reads the donor track's channels (`engine.AudioTracks(mka)`, the `dLang` entry) and sets `Surround: tracks[dLang].Channels > 2`. The standalone check becomes `Check(ctx, p, r.part, len(plan.Audio), len(plan.Audio)+p.audio.Tracks())`, and the standalone plan's expectation adds `p.audio.Tracks()`.
   - `app/squash/worker/ffgo.go` `prepareGraft`: `plan.Expect.AudioStreams += n`, with `n` from the prepared graft. Add `Tracks() int` to `grafttask.Prepared`'s interface and implement it on the graft package's `Prepared` as `p.audio.Tracks()`.
   - The joined check: `CheckGraft(ctx, graft.prepared, part, graft.index, graft.index+graft.prepared.Tracks())`. `Check` verifies the first grafted track, the AC-3 when surround; DecodePCM downmixes it as it does any track.

- [ ] **Step 4: Run the packages**

Run: `go test -count=1 ./pkg/transcode/engine/ ./app/squash/worker/... 2>&1 | tail -30`
Expected: PASS. The existing stereo graft tests and the interleaving test stay green.

- [ ] **Step 5: Commit**

```bash
git commit -m "feat(engine,squash): a surround dub is grafted as AC-3 5.1 plus an AAC 2.0 companion, interpolated across all its channels" -- pkg/transcode/engine app/squash/worker app/squash/grafttask
```

---

### Task 9: squasharr writes MP4 and places the sidecars before the swap

**Files:**
- Modify: `app/squash/worker/profile.go` (`OutputContainer`, `ProfileHashAt`)
- Modify: `app/squash/worker/ffgo.go` (`StandardProfile`, the `verify` closure, `encodeJob.sidecars`, `sweepEarlierAttempts`)
- Modify: `app/squash/worker/run.go` (place the sidecars after `beforeSwap`, in both swap branches; `removePart` removes sidecar parts)
- Modify: `app/squash/worker/buildtask.go:60`, `app/squash/controller/transcodejob/controller.go:541,577`, `dispatch.go:266,275,323` (pass `worker.OutputContainer`)
- Modify: `api/transcode/v1alpha1/transcodeprofile_types.go:200-203` (document `container` as ignored; default `mp4`), then `make generate manifests`
- Test: `app/squash/worker/worker_envtest_test.go`, `app/squash/worker/unit_test.go`

**Interfaces:**
- Produces: `const OutputContainer = transcodev1alpha1.ContainerMP4`, and `placeSidecars(ctx, plan standard.Result, part, localOut string) error`.
- Consumes: Task 2's `fsops.SidecarPath` and `SubtitleExt`, and Task 4's `Result.Sidecars`.

- [ ] **Step 1: Write the failing tests**

In `worker_envtest_test.go`:
- Change `newFixtureWith`'s default `fileName` to `Film.2020.1080p.mp4`, so the in-place tests keep testing in place.
- Invert `TestRunChangesTheContainerAndRetiresTheSource`: the source is `Film.2020.1080p.mkv`, the output `Film.2020.1080p.mp4`, and the assertion reads `mi.Container == "mp4"`.
- Add `sidecarEngine` (a `fakeEngine` whose `Encode` also writes `fsops.SidecarPath(output, s.Suffix)` for every `plan.Sidecars` entry), and a `fixtureOptions.writeSource` that makes a clip with an English ASS track.

```go
// The plan's sidecars reach their final names before the video swap; an
// existing file at one of them is kept (R3); none is left as a part.
func TestRunPlacesTheSidecarsAndKeepsAnExistingOne(t *testing.T) {
	c := requireCluster(t)
	requireFFmpeg(t)
	f := newFixtureWith(t, c, fixtureOptions{fileName: "Film.2020.1080p.mkv", writeSource: withASSAndForcedSRT})
	dir := filepath.Dir(f.local)
	existing := filepath.Join(dir, "Film.2020.1080p.ger.forced.srt")
	require.NoError(t, os.WriteFile(existing, []byte("captionarr's"), 0o644))
	out := f.processWith(t, c, sidecarEngine{report: &transcode.Report{OK: true}})
	require.NoError(t, out.Err)
	b, err := os.ReadFile(filepath.Join(dir, "Film.2020.1080p.eng.ass"))
	require.NoError(t, err)
	assert.Equal(t, "sidecar", string(b))
	b, err = os.ReadFile(existing)
	require.NoError(t, err)
	assert.Equal(t, "captionarr's", string(b), "an existing sidecar is never overwritten")
	assert.Empty(t, f.partFiles(t), "no video or sidecar part is left")
}

// A verify failure leaves no sidecar, at its final name or as a part.
func TestAFailedVerifyLeavesNoSidecar(t *testing.T) {
	c := requireCluster(t)
	requireFFmpeg(t)
	f := newFixtureWith(t, c, fixtureOptions{fileName: "Film.2020.1080p.mkv", writeSource: withASSAndForcedSRT})
	out := f.processWith(t, c, sidecarEngine{report: &transcode.Report{OK: false, Problems: []string{"bad"}}})
	require.Error(t, out.Err)
	_, err := os.Stat(filepath.Join(filepath.Dir(f.local), "Film.2020.1080p.eng.ass"))
	assert.ErrorIs(t, err, os.ErrNotExist)
	assert.Empty(t, f.partFiles(t))
}

// A crash after the sidecars were placed but before the swap: the retry
// places nothing twice and overwrites nothing.
func TestARetryAfterPlacingTheSidecarsDoesNotDuplicateThem(t *testing.T) {
	c := requireCluster(t)
	requireFFmpeg(t)
	f := newFixtureWith(t, c, fixtureOptions{fileName: "Film.2020.1080p.mkv", writeSource: withASSAndForcedSRT})
	final := filepath.Join(filepath.Dir(f.local), "Film.2020.1080p.eng.ass")
	require.NoError(t, os.WriteFile(final, []byte("first attempt"), 0o644))
	out := f.processWith(t, c, sidecarEngine{report: &transcode.Report{OK: true}})
	require.NoError(t, out.Err)
	b, err := os.ReadFile(final)
	require.NoError(t, err)
	assert.Equal(t, "first attempt", string(b))
	assert.Empty(t, f.partFiles(t))
}
```

`f.processWith(t, c, engine)` is `f.process` with the engine chosen. Add it if `process` hard-codes `fakeEngine{}`. `f.partFiles` must list sidecar parts too: extend it to every name `fsops.ParseTranscodePart` accepts. `withASSAndForcedSRT` writes the clip with the ffmpeg CLI, as Task 7's test does.

In `unit_test.go`:

```go
func TestOutputPathIsAlwaysMP4(t *testing.T) {
	out, err := OutputPath(transcodev1alpha1.TranscodeJobSpec{SourcePath: "/data/media/m/F.mkv"}, "p", OutputContainer, true)
	require.NoError(t, err)
	assert.Equal(t, "/data/media/m/F.mp4", out)
}

func TestStandardProfileIgnoresTheContainer(t *testing.T) {
	sp := StandardProfile("p", "h", transcodev1alpha1.TranscodeProfileSpec{Container: transcodev1alpha1.ContainerMKV})
	assert.Equal(t, transcode.ContainerMP4, sp.Container)
}

func TestTheV2HashIgnoresTheContainer(t *testing.T) {
	mkv := ProfileHashAt(transcodev1alpha1.TranscodeProfileSpec{Container: transcodev1alpha1.ContainerMKV}, 2)
	mp4 := ProfileHashAt(transcodev1alpha1.TranscodeProfileSpec{Container: transcodev1alpha1.ContainerMP4}, 2)
	assert.Equal(t, mkv, mp4)
	assert.NotEqual(t, ProfileHashAt(transcodev1alpha1.TranscodeProfileSpec{Container: transcodev1alpha1.ContainerMKV}, 1), mkv)
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `KUBEBUILDER_ASSETS="$(setup-envtest use 1.37.0 -p path)" go test -count=1 -run 'TestRunPlacesTheSidecars|TestAFailedVerifyLeavesNoSidecar|TestARetryAfterPlacing|TestOutputPathIsAlwaysMP4|TestStandardProfileIgnores|TestTheV2Hash|TestRunChangesTheContainer' ./app/squash/worker/`
Expected: FAIL to compile, `undefined: OutputContainer`.

- [ ] **Step 3: Implement**

1. **`profile.go`:**
   - Add `const OutputContainer = transcodev1alpha1.ContainerMP4`, with the comment "the MP4 standard (standard.Version 2) writes MP4 whatever the profile's container says".
   - In `ProfileHashAt`: `if version >= 2 { container = OutputContainer }`. Keep the struct, so v1 hashes reproduce.

2. **`ffgo.go`:**
   - `StandardProfile` sets `Container: transcode.ContainerMP4`.
   - `encodeJob` gains `plan standard.Result`, set in `ffgoJob`.
   - The `verify` closure adds a problem per sidecar part that is missing or empty: `st, err := os.Stat(fsops.SidecarPath(part, s.Suffix)); err != nil || st.Size() == 0`. It appends to `rep.Problems` and sets `rep.OK = false` before returning.
   - `sweepEarlierAttempts`' skip condition takes sidecar parts of the same stem: `stemOK := p.Stem == cur.Stem || (strings.HasPrefix(p.Stem, cur.Stem+".") && fsops.SubtitleExt(p.Ext))`, used in place of `p.Stem != cur.Stem`.

3. **`run.go`:**

```go
// placeSidecars moves each planned sidecar's part to its final name beside
// out, before the video swap: a crash between the two leaves sidecars
// beside the old file, which the retry finds in place and keeps. A file
// already at a final name is kept and the part removed (ruling R3).
func placeSidecars(ctx context.Context, plan standard.Result, part, out string) error {
	for _, s := range plan.Sidecars {
		from, to := fsops.SidecarPath(part, s.Suffix), fsops.SidecarPath(out, s.Suffix)
		if _, err := os.Lstat(to); err == nil {
			logging.FromContext(ctx).InfoContext(ctx, "squasharr worker: a sidecar already exists; keeping it", "sidecar", to)
			_ = os.Remove(from)
			continue
		}
		if err := fsops.MoveAtomic(from, to); err != nil {
			return retriable("squasharr worker: place sidecar %s: %w", to, err)
		}
	}
	return nil
}
```

   - Call it right after each successful `r.beforeSwap(ctx, job.part)`, in both branches: `if err := placeSidecars(ctx, job.plan, job.part, sw.localOut); err != nil { removePart(ctx, job.part); return err }`. The in-place branch passes `local`.
   - `removePart(ctx, part)` also removes every sidecar part of this attempt: list `filepath.Dir(part)`, and remove each regular file whose `ParseTranscodePart` has the same `JobUID8` and `Attempt`, a `Stem` with prefix `cur.Stem+"."`, and a subtitle `Ext`.

4. **The controllers:**
   - `buildtask.go:60` and `controller.go:577` pass `OutputContainer` (as `worker.OutputContainer`) to `OutputPath`.
   - `controller.go:541` and `dispatch.go:266,275,323` pass `worker.OutputContainer` to `recordPlan`/`markPlanned`, so `containerChange` reports `.mkv becomes mp4`.

5. **The API:** on `Container` write the comment "Container is ignored since the MP4 standard (2026-10-06): every transcode writes MP4. Kept so existing profiles still apply.", and change the marker to `+kubebuilder:default="mp4"`. Run `make generate manifests` and `git add` any new generated file.

- [ ] **Step 4: Run the squash suites**

Run: `KUBEBUILDER_ASSETS="$(setup-envtest use 1.37.0 -p path)" go test -count=1 ./app/squash/... ./pkg/fsops/ ./pkg/crdcheck/ 2>&1 | tail -40`
Expected: the new tests PASS. Existing tests that asserted an `.mkv` output, an in-place swap of an `.mkv`, the v1 hash or an MKV plan are updated after reading each, and only where the assertion was the old layout. A failure elsewhere is a Step 3 bug.

- [ ] **Step 5: Commit**

```bash
git status --short | grep '^??'   # git add each new file this task made
git commit -m "feat(squash): every transcode writes MP4, and the plan's sidecars are placed beside the output before the swap, never over an existing file, removed with a failed attempt" -- app/squash api/transcode config charts/clustarr/crds pkg/fsops
```

Use the CRD paths `make manifests` actually touched, from `git status`.

---

### Task 10: docs, CLAUDE.md and the gate

**Files:**
- Modify: `docs/superpowers/specs/2026-10-06-mp4-standard-design.md` (§4 and §10 for rulings R1-R3, and an "As built (phase 1)" section listing R1-R6 and the commits)
- Modify: `CLAUDE.md` (the Transcoding section's standard paragraph: "Apple TV direct-play audio ... every subtitle, attachment and chapter kept" becomes the MP4 layout; `standard.Version` 2; image subtitles held until phase 2)

- [ ] **Step 1: Write the docs**

1. **CLAUDE.md:** replace the parenthetical after "`pkg/transcode/standard.Plan` decides" with the new standard:
   - HEVC `hvc1` in MP4;
   - per language E-AC-3 or AC-3 copied, else AC-3 5.1 640k, plus AAC 2.0 160k for surround; AAC alone for mono or stereo; commentary AAC;
   - text subtitles as `mov_text`; ASS, and forced text, as sidecars placed before the swap;
   - attachments dropped, chapters kept;
   - files with image subtitles Skipped (`standard.HoldImageSubtitles`) until phase 2;
   - Dolby Vision 7/8.1 as HDR10.

   Name the spec. Keep the rest of the paragraph.

2. **The spec:** amend §4's last bullet and §10's "held back, as Planned" sentence to R1 and R2 as built, and append "As built (phase 1, 2026-10-06)" listing R1-R6.

- [ ] **Step 2: Commit**

```bash
git commit -m "docs: the MP4 standard's phase 1 as built -- CLAUDE.md's transcoding standard, the spec's rulings R1-R6" -- CLAUDE.md docs/superpowers/specs/2026-10-06-mp4-standard-design.md
```

- [ ] **Step 3: Gate in a clean worktree**

```bash
SP=/tmp/claude-1000/-home-appkins-src-mediactl-clustarr/87578cad-3c1c-45f5-a98d-ff3f9a98ce41/scratchpad
git worktree add "$SP/gate-mp4p1" HEAD
cp charts/clustarr/charts/*.tgz "$SP/gate-mp4p1/charts/clustarr/charts/"
cd "$SP/gate-mp4p1" && make test > "$SP/gate-mp4p1.log" 2>&1; echo "exit $?" >> "$SP/gate-mp4p1.log"
```

Run it in the background, and do not build images meanwhile.
Expected: `exit 0`, and `grep -c '^ok'` at least 197 packages. A failure is diagnosed in the main checkout and fixed with a test that failed first. Then re-gate.

- [ ] **Step 4: Final review**

Package the branch (`superpowers:executing-plans`' final review) from the commit before Task 1 to HEAD. Review it on the most capable model with this plan, the spec, the Review Focus list and the ledger's rulings.
