# Segment detection implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task by task. Steps use checkbox (`- [ ]`) syntax for tracking. Every code step is test-first: write the test, watch it fail for the stated reason, implement, watch it pass, commit path-scoped (`git commit -m … -- <paths>`).

**Goal:** clustarr detects intros, credits, recaps and previews itself, merges them under TheIntroDB into `MediaFile.status.markers`, and cluster-plex turns off Plex's local detection.

**Architecture.**

- **ffmpeg** only decodes, to pipes.
- **Analysis is Go, in `pkg/segments`:** a Chromaprint port, season alignment, frame statistics, chapter rules, and a gated text detector on ONNX Runtime.
- **`cmd/segmentarr-worker`,** a credential-less binary on the media image, runs the analysis from NATS tasks.
- **catalogarr:**
  - plans tasks per season or per movie;
  - stores raw results in KV;
  - writes the merged segments under `catalogarr-markers` through one compare-and-swap helper, which TheIntroDB's handler also uses.

**Tech stack:**

- Go and controller-runtime;
- NATS JetStream, including KV and an object store;
- ffmpeg n9 (BtbN, in the media image);
- `github.com/shota3506/onnxruntime-purego` with `libonnxruntime.so`;
- PaddleOCR's text-detection model as ONNX;
- pgx v5 in cluster-plex.

**Spec:** `docs/superpowers/specs/2026-10-01-segment-detection-design.md`.

## Global constraints

- **Header:** the GPL-3.0 header on every new Go file.
- **Status writes:**
  - every one goes through `pkg/k8s.PatchStatus`;
  - the markers writer is `k8s.ManagerCatalogarrMarkers` only;
  - both write paths go through one helper that re-reads and applies with a `resourceVersion` precondition (CLAUDE.md: two write paths under one manager).
- **API:** no floats; confidence is an `int32` percent. Every status list is capped: segments 20, chapters 64.
- **Bus:**
  - every KV key goes through `events.KVKeyToken`;
  - new buckets and stores are `Durable`;
  - the single-node topology test must still pass.
- **Subprocesses:** `Setpgid`, a `Cancel` that kills the process group, and `WaitDelay`.
- **Metrics:** prefix `clustarr_`; never labelled by title, path or release.
- **Credentials:** the worker pod has no ServiceAccount token, no RBAC, `/data` read-only, and NATS only.
- **Dependencies:** `go get` runs serially, once, in Task 7 only.
- **Tests:** unit tests only, per the owner's standing instruction. Envtest and e2e are not run. Suites that need assets skip by name.
- **Commits:** on main, path-scoped, never `git stash`, never pushed by a subagent.
- **Thresholds (spec §3 and §6):**
  - **intro:** window min(25%, 10 min); ≤ 6 differing bits per point; 3.5 s gap; 15–120 s; up to 4 neighbours; agreement within 3 s;
  - **credits:** window the last 15%, at most 450 s for an episode and 900 s for a movie; black pixel luma < 28; black frame ≥ 85%; runs ≥ 15 s; scroll for ≥ 5 s at ±1 px;
  - **confidence:** chapter 100, two signals agreeing within 5 s 90, DNN 80, a single strong signal ≥ 70, nothing written below 60.

## Review focus

1. **A season of one file, or with only one decodable file:** no intro, no error; credits still analyzed per file. Tests in Tasks 4 and 10.
2. **A file replaced or re-probed while a season task is in flight:** the stale result is dropped and the new probe re-queues the file. Test in Task 11.
3. **TheIntroDB refreshing a file that analysis already covers:** TheIntroDB wins its kinds, and analysis keeps the kinds TheIntroDB lacks after the re-merge. Test in Task 11.
4. **The DNN unavailable** (library or model missing): the worker serves, the stage is skipped, and the metric reads 0. Test in Task 7.
5. **Credits followed by an anime preview, or a file whose last frame isn't credits:** credits end where the preview starts; no credits are invented over a normal last scene. Tests in Tasks 5 and 6.

## Rulings carried in from the spec

**Ruling P-1, Plex preferences** (read from a live Plex on 2026-10-01):

- `MarkerSource` is `any`; its options are `any`, `cloud` ("only online (no local detection)") and `local`. `GenerateIntroMarkerBehavior` and `GenerateCreditsMarkerBehavior` are `asap`, and `GenerateAdMarkerBehavior` is `scheduled`; their options are `never`, `scheduled` and `asap`.
- cluster-plex writes `MarkerSource=cloud` and `GenerateAdMarkerBehavior=never` as required preferences. That turns off local intro, credits and ad detection and keeps Plex's online markers.
- Cost if wrong: Plex's online markers keep arriving for kinds clustarr has none of, which is wanted.

**Ruling P-2, the worker binary:** a separate `cmd/segmentarr-worker`, like `cmd/squasharr-worker`, so it links no controller-runtime. It ships in the media image with its own chart template, as the ui has.

---

## clustarr

### Task 1: The probe records chapters

**Files:**

- Modify: `api/common/v1alpha1/media_types.go`: `MediaInfo`.
- Modify: `pkg/mediainfo/map.go` and `pkg/mediainfo/mediainfo.go`: `ProbeVersion`.
- Test: `pkg/mediainfo/map_test.go`.
- Fixture: an ffprobe JSON with chapters under `test/data/mediainfo/`, recorded with `ffprobe -show_chapters` from a real file.

**Interfaces.** Produces:

```go
// commonv1
type Chapter struct {
	// +kubebuilder:validation:MaxLength=128
	Title       string `json:"title,omitempty"`
	StartMillis int64  `json:"startMillis"`
	EndMillis   int64  `json:"endMillis"`
}

// MediaInfo gains:
// +optional
// +kubebuilder:validation:MaxItems=64
ChapterList []Chapter `json:"chapterList,omitempty"`
```

`mediainfo.ProbeVersion` goes up by one.

- [ ] **Failing test.** `TestMapRecordsChapterTitlesAndTimes` maps the fixture. It asserts the titles and millisecond times of the first and last chapters, and that more than 64 chapters are capped at 64. A title over 128 bytes is cut on a rune boundary.
- [ ] **Implement.** Map `raw.Chapters`. ffprobe gives `start_time` and `end_time` as decimal-second strings; parse them with `strconv.ParseFloat` inside `pkg/mediainfo`, never in `api/`. Raise `ProbeVersion`.
- [ ] `make generate manifests`, then `go test ./pkg/mediainfo/... ./pkg/crdcheck/...`. Expected: PASS.
- [ ] Commit: `feat(mediainfo): the probe records chapter titles and times`.

### Task 2: API for sources, confidence and analysis

**Files:**

- Modify: `api/catalog/v1alpha1/mediafile_types.go`.
- Generated: deepcopy, applyconfigurations, the CRD.
- Test: `pkg/crdcheck` (cap guard).

**Interfaces.** Produces:

```go
type SegmentSource string // +kubebuilder:validation:Enum=theintrodb;chapters;analysis
const (SegmentSourceTheIntroDB SegmentSource = "theintrodb"; SegmentSourceChapters = "chapters"; SegmentSourceAnalysis = "analysis")

// MarkerSegment gains:
// +optional
Source SegmentSource `json:"source,omitempty"`
// +optional +kubebuilder:validation:Minimum=0 +kubebuilder:validation:Maximum=100
Confidence int32 `json:"confidence,omitempty"`

type SegmentAnalysis struct {
	Result       MarkersResult `json:"result"`          // Found|NotFound|Error
	AnalyzedAt   metav1.Time   `json:"analyzedAt"`
	// +kubebuilder:validation:MaxLength=128
	ForProbeHash string        `json:"forProbeHash,omitempty"`
	Version      int32         `json:"version,omitempty"`
	// +kubebuilder:validation:MaxLength=512
	Message      string        `json:"message,omitempty"`
}

// FileMarkers gains:
// +optional
Analysis *SegmentAnalysis `json:"analysis,omitempty"`
```

- [ ] **Failing tests.** `TestEveryStatusListIsCapped` and `TestNoCRDDefaultIsUnreachableFromGo` run over the new types. Generation must succeed, and the guards stay green.
- [ ] `make generate manifests`, then `go test ./pkg/crdcheck/... ./api/...`. Expected: PASS.
- [ ] Commit: `feat(api): marker segments carry their source and confidence; status.markers.analysis records local analysis`.

### Task 3: Chromaprint port (`pkg/segments/chromaprint`)

**Files:**

- Create: `pkg/segments/chromaprint/{chromaprint.go,fft.go,chroma.go,classifier.go,resample.go,chromaprint_test.go}`.
- Fixtures: `test/data/segments/{tone-sweep.s16le, speech.s16le, music.s16le}`. These are 11,025 Hz mono clips of 10–20 s, cut from royalty-free or self-generated audio; record the source in a README beside them. Plus their `.fp` files, produced once with:

```
ffmpeg -f s16le -ar 11025 -ac 1 -i X.s16le -f chromaprint -fp_format raw X.fp
```

**Interfaces.** Produces:

```go
// Fingerprint computes Chromaprint's default (algorithm 2) fingerprint of
// mono 16-bit PCM at 11025 Hz: one uint32 per 1365 samples (~0.1238 s).
func Fingerprint(pcm []int16) []uint32
const ItemSeconds = 1365.0 / 11025.0 // seconds per point; float is fine outside api/
```

**Algorithm.** A port of Chromaprint's `fingerprinter_configuration.cpp`, configuration test2:

- frame size 4,096, overlap 4,096 − 1,365;
- Hamming window, then FFT;
- chroma over 28–3,520 Hz in 12 bands, with interpolation;
- chroma filter coefficients `{0.25, 0.75, 1.0, 0.75, 0.25}`;
- chroma normalizer: Euclidean norm;
- the 16 classifiers and quantizer thresholds, copied verbatim with a `// from chromaprint (LGPL-2.1+)` attribution;
- the integral image and gray code.

The resampler is unnecessary because ffmpeg supplies 11,025 Hz; leave `resample.go` out unless a test needs it.

- [ ] **Failing test.** `TestFingerprintMatchesFFmpegBitForBit`: for each fixture, `Fingerprint(pcm)` equals the `.fp` stream exactly, in length and every value.
- [ ] **Implement** until bit-exact. On a mismatch, compare intermediate chroma vectors against Chromaprint's source rather than tuning numbers.
- [ ] **Benchmark.** `BenchmarkFingerprintTenMinutes` must take under 1 s on one core; record the figure in the commit message.
- [ ] `go test ./pkg/segments/chromaprint/`. Expected: PASS.
- [ ] Commit: `feat(segments): a Go port of Chromaprint's default fingerprint, bit-exact against ffmpeg's muxer`.

### Task 4: Alignment (`pkg/segments/align`)

**Files:**

- Create: `pkg/segments/align/{align.go,season.go,align_test.go}`.

**Interfaces.** Produces:

```go
type Region struct{ StartS, EndS float64 } // seconds within the window
// Shared finds the longest region of a and b (points of two windows) whose
// aligned points differ in at most MaxBitDiff bits, bridging gaps up to
// MaxGapS, between MinS and MaxS long. ok is false when none qualifies.
func Shared(a, b []uint32, p Params) (ra, rb Region, ok bool)
type Params struct{ MaxBitDiff int; MaxGapS, MinS, MaxS float64; IndexShift uint }
var IntroParams = Params{MaxBitDiff: 6, MaxGapS: 3.5, MinS: 15, MaxS: 120, IndexShift: 2}
// Season returns each episode's agreed region: the median of its
// comparisons with up to 4 neighbours (by index), when at least 2 agree
// within 3 s. Episodes are ordered by episode number; fps[i] may be nil.
func Season(fps [][]uint32, p Params) []*Region
```

**Steps:**

- [ ] **Failing tests.** All with synthetic streams: a planted shared run in random noise, at a fixed seed.
  - `TestSharedFindsAPlantedIntro`: a 60 s run at 30 s in a and at 90 s in b, recovered within ±1 point.
  - `TestSharedBridgesShortGaps`: corrupt 2 s inside; still one region.
  - `TestSharedRejectsTooShortAndTooLong`.
  - `TestSeasonNeedsTwoAgreeing`: for a 1-episode season, all nil.
  - `TestSeasonSurvivesAnEpisodeWithoutTheIntro`: one episode without the run gets nil; the others get their regions. This pins Review Focus 1.
  - `TestSeasonColdOpen`: the intro at different offsets per episode.
- [ ] **Implement.**
  - **Index:** an inverted index of `point >> IndexShift` to its positions.
  - **Offsets:** candidate offsets come from index hits, tallied.
  - **Scan:** for each of the top 10 offsets, scan the aligned points with `bits.OnesCount32(a^b) <= MaxBitDiff`, bridging gaps.
  - **Season:** neighbours are i−2, i−1, i+1, i+2, clipped.
- [ ] `go test ./pkg/segments/align/`. Expected: PASS.
- [ ] Commit: `feat(segments): season-wide shared-region alignment of fingerprints`.

### Task 5: Frame statistics (`pkg/segments/frames`)

**Files:**

- Create: `pkg/segments/frames/{stats.go,scroll.go,runs.go,frames_test.go}`.
- Fixtures: `test/data/segments/frames/*.gray`. These are 128×72 raw grayscale sequences cut once from real files: a black end card, rolling credits over picture, static title cards on black, a normal scene. Plus a README naming their sources.

**Interfaces.** Produces:

```go
const W, H = 128, 72
type Stat struct{ Black bool; EntropyMilli, EdgePermille int32; ScrollPx int8; Scrolling bool }
func Stats(frames [][]byte) []Stat // one per frame; Scrolling needs >=5 consecutive same shift ±1
type Run struct{ StartS, EndS int; Confidence int32 }
// CreditRuns returns runs of credit-like seconds >= 15 s (2 s gaps bridged).
// The window starts at offsetS seconds into the file.
func CreditRuns(st []Stat, offsetS int) []Run
```

**Steps:**

- [ ] **Failing tests:**
  - `TestBlackCardIsBlack`;
  - `TestRollingCreditsScroll`: the shift is constant and nonzero over 5 frames;
  - `TestNormalSceneIsNotCredits`: no run is returned over the normal scene, pinning Review Focus 5;
  - `TestRunsBridgeShortGapsAndNeedFifteenSeconds`.
- [ ] **Implement.**
  - **Black:** a black pixel is `< 28`, a black frame `>= 85%` black pixels.
  - **Entropy:** over a 64-bin histogram, in thousandths.
  - **Edges:** Sobel magnitude over 64.
  - **Scroll:** normalized cross-correlation of row-mean profiles over shifts −12…12.
  - **A credit-like second** is `Black || Scrolling || (EntropyMilli < 3000 && EdgePermille > 60)`. Tune the last two thresholds only against the fixtures and record them as constants with a comment.
  - **Run confidence:** 70, plus 10 if at least 50% of seconds are scrolling, plus 10 if the run touches the window's end. Capped at 90.
- [ ] `go test ./pkg/segments/frames/`. Expected: PASS.
- [ ] Commit: `feat(segments): black, entropy, edge and scroll statistics over end-of-file frames`.

### Task 6: Chapters, combination, merge and due (`pkg/segments`)

**Files:**

- Create: `pkg/segments/{chapters.go,credits.go,merge.go,due.go,segments_test.go}`.

**Interfaces.** Produces:

```go
const AnalyzerVersion int32 = 1
type Kind string // intro|recap|credits|preview (the catalog MarkerKind values)
type Segment struct{ Kind Kind; StartMs, EndMs int64; Source catalogv1alpha1.SegmentSource; Confidence int32 }
// FromChapters applies the chapter-name patterns (verbatim from Intro
// Skipper's defaults, verified in this task) and returns segments at 100.
func FromChapters(ch []commonv1.Chapter) []Segment
// Credits combines candidates per spec §6.3 for a file of durationMs.
func Credits(durationMs int64, cands []Segment, previewStartMs int64) (Segment, bool)
// AnimePreview is §6.5's preview after credits for an anime series.
func AnimePreview(credits Segment, durationMs int64) (Segment, bool)
// Merge applies precedence per kind: TheIntroDB, chapters, analysis (>=60).
func Merge(theintrodb, analysis []Segment) []Segment // ordered by start, capped 20
// Due reports whether mf needs analysis now (spec §7).
func Due(mf *catalogv1alpha1.MediaFile, now time.Time) bool
```

**Steps:**

- [ ] **Verify the patterns.** Read Intro Skipper's `PluginConfiguration` defaults for `ChapterAnalyzerIntroductionPattern`, `…EndCreditsPattern`, `…RecapPattern` and `…PreviewPattern` from its repository, through DeepWiki or GitHub. Copy them as Go `regexp` constants. If a pattern uses syntax RE2 lacks, use `regexp2` per CLAUDE.md. Cite the source and its commit in a comment.
- [ ] **Failing tests,** table-driven:
  - `TestChapterNames`: "Opening", "OP", "Intro", "Recap", "Previously on", "Ending", "ED", "End Credits", "Preview", "Next Episode", and "Chapter 3", which gives none.
  - `TestCreditsCombination`:
    - two signals within 5 s give the earlier start at 90;
    - a single 70 stands;
    - 55 gives nothing;
    - credits not reaching within 5 s of the end are rejected unless they end at a preview start;
    - a 20-minute run in a movie is rejected (over 900 s).
  - `TestMergePrecedence`:
    - TheIntroDB's intro beats analysis's intro;
    - analysis credits survive beside a TheIntroDB intro, pinning Review Focus 3;
    - chapters beat analysis;
    - the cap is 20.
  - `TestDue`:
    - unprobed: false;
    - never analyzed: true;
    - a new probe hash: true;
    - an old `version`: true;
    - Error less than a day old: false; more than a day old: true;
    - NotFound at the current version and hash: false.
- [ ] **Implement.**
- [ ] `go test ./pkg/segments/`. Expected: PASS.
- [ ] Commit: `feat(segments): chapter rules, credit combination, precedence merge and due`.

### Task 7: Text detection (`pkg/segments/textdet`) and the one dependency

**Files:**

- `go.mod`, run serially: `go get github.com/shota3506/onnxruntime-purego@<latest tag>`.
- Create: `pkg/segments/textdet/{textdet.go,onnx.go,search.go,model/det.onnx,textdet_test.go}`.
- Fixtures: `test/data/segments/textdet/{credits.rgb,scene.rgb}`, at 320×180.

**Interfaces.** Produces:

```go
type Detector interface{ Density(rgb []byte, w, h int) (permille int32, err error) }
// NewONNX loads libonnxruntime from libPath (env ORT_LIB_PATH, default
// /usr/lib/libonnxruntime.so) and the embedded model; ErrUnavailable when
// either fails.
func NewONNX(libPath string) (Detector, error)
var ErrUnavailable = errors.New("textdet: unavailable")
// FindStart binary-searches the credits start in [fromS, toS] given a frame
// source; ok false when no dense run reaches the end.
func FindStart(ctx context.Context, d Detector, frame func(ctx context.Context, atS float64) ([]byte, error), fromS, toS float64, threshold int32) (startS float64, ok bool, err error)
```

**Steps:**

- [ ] **Model.** Download PaddleOCR's newest mobile detection model in ONNX form, PP-OCRv5 mobile detection, from PaddleOCR's or OnnxOCR's release.
  - Record its URL, SHA-256 and licence (Apache-2.0) in `model/README.md`.
  - Embed it with `go:embed`. The size must be under 10 MB.
- [ ] **Failing tests:**
  - `TestFindStartBinarySearch`, with a fake Detector that answers by time: it finds a start of 1,200 s to within 1 s in at most 25 calls;
  - `TestONNXSeesCreditsText`: on the fixtures, credits density is greater than scene density, and greater than the threshold. It skips by name when `ORT_LIB_PATH` is unset;
  - `TestUnavailableWithoutTheLibrary`: a bogus path gives `ErrUnavailable`. This is Review Focus 4's first half.
- [ ] **Implement.**
  - **Preprocessing:** NCHW float32, normalized with the model's mean and std, which are documented in the model README.
  - **Output:** the probability map. Density is the share of pixels over 0.3, in thousandths. No box extraction is needed.
- [ ] `go test ./pkg/segments/textdet/` once with `ORT_LIB_PATH` pointing at a locally downloaded `libonnxruntime.so`, and once without. Expected: PASS both, the second skipping by name.
- [ ] Commit: `feat(segments): PaddleOCR text density on ONNX Runtime through purego, and the credits binary search`.

### Task 8: Decoding (`pkg/segments/decode`)

**Files:**

- Create: `pkg/segments/decode/{decode.go,proc.go,decode_test.go}`.

**Interfaces.** Produces:

```go
type Decoder struct{ FFmpeg string; Threads int }
func (d Decoder) Audio(ctx context.Context, path string, stream int, fromS, durS float64) ([]int16, error) // mono 11025 s16le
func (d Decoder) Frames(ctx context.Context, path string, fromS float64) ([][]byte, error)                // 1 fps, 128x72 gray
func (d Decoder) Frame(ctx context.Context, path string, atS float64) ([]byte, error)                     // 320x180 rgb24
```

**Process discipline:** `exec.CommandContext` with `SysProcAttr{Setpgid: true}`, a `Cancel` that kills `-pgid`, `WaitDelay` 5 s, and stderr kept to its last 2 KB for errors. Commands are as in spec §6.

**Steps:**

- [ ] **Failing tests,** skipping by name without `ffmpeg` on `PATH`:
  - `TestAudioDecodesTheWindow`: a generated 30 s sine `.mkv` made with ffmpeg's `lavfi` in the test; the window from 10 s to 20 s gives 110,250 samples;
  - `TestFramesOnePerSecond`: a `testsrc` clip;
  - `TestAHungFFmpegIsKilledWithItsGroup`: a fake `FFmpeg` script that sleeps; the context deadline returns within 6 s.
- [ ] **Implement.**
- [ ] `go test ./pkg/segments/decode/`. Expected: PASS.
- [ ] Commit: `feat(segments): ffmpeg decoding to raw audio and frames, killed with its process group`.

### Task 9: Topology and task schema

**Files:**

- Modify: `pkg/events/subjects.go`, `pkg/events/topology.go`.
- Create: `pkg/events/schema/segments.go`.
- Test: `pkg/events/topology_test.go`, `pkg/events/schema/*_test.go`.

**Interfaces.** Produces subjects, all on stream `CLUSTARR_WORK_CATALOGARR`:

- `WorkSegmentsPlanSubject(seasonKey)` → `clustarr.work.catalogarr.segments-plan.normal.<key>`;
- `WorkSegmentsAnalyzeSubject(key)` → `…segments-analyze.normal.<key>`;
- `WorkSegmentsResultSubject(key)` → `…segments-result.normal.<key>`.

Consumers:

| Consumer | AckWait | MaxDeliver | MaxAckPending | BackOff |
| --- | --- | --- | --- | --- |
| `catalogarr-segments-plan` | 60 s | 5 | 8 | — |
| `segmentarr-analyze` | 30 min, with in-progress heartbeats | 3 | 4 | 1 m, 10 m |
| `catalogarr-segments-result` | 60 s | 8 | 32 | — |

Storage:

- bucket `BucketSegments = "clustarr-segments"`: durable, no TTL;
- object store `ObjectStoreFingerprints = "clustarr-fingerprints"`: file storage, `MaxBytes` 1 GiB, MaxAge 90 days. Add a MaxAge field to `ObjectStoreSpec` if it lacks one, with Ensure support and a natsbus contract test.

Schema:

```go
type SegmentsPlanTask struct{ Namespace, Series string; Season int32; Movie string } // Movie set => one file
type AnalyzeFile struct{ MediaFile, UID, Path, ProbeHash string; DurationMs int64; AudioStream int32; Chapters []commonv1.Chapter; Due bool; Anime bool }
type AnalyzeTask struct{ Namespace, Key string; Kind string /*episode|movie*/; Files []AnalyzeFile }
type SegmentJSON struct{ Kind string; StartMs, EndMs int64; Source string; Confidence int32 }
type SegmentsResult struct{ MediaFile, ProbeHash string; Version int32; Result string; Message string; Segments []SegmentJSON }
```

**Steps:**

- [ ] **Failing tests:**
  - the topology `Validate`;
  - `TestEnsureSingleNodeTopologyFitsTheKindServersLimits`, with the new store and bucket;
  - schema round trips;
  - `ScheduleSubject` accepts the plan subject.
- [ ] **Implement.**
- [ ] `go test ./pkg/events/...`, including the natsbus contract tests on the embedded server. Expected: PASS.
- [ ] Commit: `feat(events): segment planning, analysis and result subjects, the segments bucket and the fingerprint store`.

### Task 10: The worker (`app/segments/worker`, `cmd/segmentarr-worker`)

**Files:**

- Create: `app/segments/worker/{worker.go,season.go,cache.go,worker_test.go}`.
- Create: `cmd/segmentarr-worker/{main.go,main_test.go}`.

**Interfaces.**

- Consumes: Tasks 3–9.
- Produces: `worker.Handler{Decoder, Detector textdet.Detector /*may be nil*/, Fingerprints events.ObjectStore, Bus events.Publisher, Clock}` with `Handle(ctx, events.Message) error`.

**Per task:**

1. **Fingerprints.** For each file, load its start and end fingerprints from the store, or decode and fingerprint them and store them. Episodes need both windows; movies need only the end window, and only for the frames path.
2. **Intros, for episodes.** `align.Season` over the start windows with `IntroParams`, then the region converted to file milliseconds.
3. **Ending theme.** `align.Season` over the end windows with credits parameters, `MinS` 15 and `MaxS` 450.
4. **Per due file:**
   - chapters (`FromChapters`);
   - frames (`decode.Frames` from the window start, then `frames.CreditRuns`);
   - the DNN, if candidates are none or below 70 and the Detector is non-nil (`textdet.FindStart`);
   - `segments.Credits`;
   - `AnimePreview` when the file is anime.
5. **Publish** one `SegmentsResult` per due file. A file whose decoding failed publishes `Result: Error` with the message; its siblings continue. This pins Review Focus 1.
6. **Heartbeat** with `m.InProgress` every 30 s.
7. **Metrics,** with no title or path labels:
   - `clustarr_segments_analyzed_total{result,source}`;
   - `clustarr_segments_stage_seconds{stage}`, with stages fingerprint, align, frames, dnn;
   - `clustarr_segments_dnn_available`.

   They are served on `--metrics-bind-address :8080`, as squasharr-worker serves its own.

**`main.go`** follows `cmd/squasharr-worker`:

- **Environment:** `NATS_URL` and `POD_NAME` are required; `ORT_LIB_PATH` is optional.
- **Flags:** `--concurrency 1`, `--data-dir`.
- **Exit codes:** squasharr-worker's.
- **DNN:** `textdet.NewONNX` failing logs once and sets `clustarr_segments_dnn_available 0`.
- **Subscription:** `bus.Subscribe` on `segmentarr-analyze`.

**Steps:**

- [ ] **Failing tests,** with a fake Decoder returning synthetic PCM with a planted shared intro, a fake store and a recording publisher:
  - `TestASeasonGetsIntrosAndCredits`;
  - `TestASingleFileSeasonGetsCreditsOnly`;
  - `TestADecodeFailureIsThatFilesError`;
  - `TestCachedFingerprintsAreNotDecodedAgain`;
  - `TestTheDNNRunsOnlyWhenSignalsAreWeak`;
  - `TestWithoutADetectorTheStageIsSkipped`: Review Focus 4's second half;
  - `cmd`: `TestMissingNATSURLExitsMisconfigured`.
- [ ] **Implement.**
- [ ] `go test ./app/segments/... ./cmd/segmentarr-worker/`. Expected: PASS.
- [ ] Commit: `feat(segments): segmentarr-worker analyzes a season or a movie and publishes per-file results`.

### Task 11: catalogarr: due, planner, results, compare-and-swap merge

**Files:**

- Create: `app/catalog/segments/{publish.go,planner.go,results.go,apply.go,setup.go,*_test.go}`.
- Modify: `app/catalog/controller/mediafile/mediafile_controller.go`: `followUpMarkers` also calls `segments.Due`, then publishes a plan.
- Modify: `app/catalog/markers/handler.go`: applies through `segments.ApplyMerged`.
- Modify: `app/catalog/run.go`: wires `catsegments.Setup` beside the markers worker.

**Interfaces.** Produces:

```go
// PublishPlan schedules the season's (or movie's) plan 5 min ahead, MsgID season+5-min bucket.
func PublishPlan(ctx context.Context, bus events.Publisher, mf *catalogv1alpha1.MediaFile, ep *catalogv1alpha1.Episode, now time.Time) error
// TheIntroDBUpdate is the marker handler's fetch outcome: bookkeeping plus its
// segments (all tagged theintrodb). AnalysisUpdate is a worker result's.
type TheIntroDBUpdate struct{ Result catalogv1alpha1.MarkersResult; FetchedAt metav1.Time; ForProbeHash string; DurationMs int64; Message string; NotFoundSince *metav1.Time; Segments []segments.Segment }
type AnalysisUpdate struct{ Result catalogv1alpha1.MarkersResult; AnalyzedAt metav1.Time; ForProbeHash string; Version int32; Message string }
// ApplyMerged re-reads mf, sets TheIntroDB bookkeeping (when tid != nil) and/or analysis
// bookkeeping (when an != nil), merges segments from status (theintrodb-tagged) and the
// clustarr-segments KV (analysis), and applies under catalogarr-markers with a
// resourceVersion precondition; a Conflict is redone from a fresh read (max 5).
func ApplyMerged(ctx context.Context, c client.Client, kv events.KV, key client.ObjectKey, tid *TheIntroDBUpdate, an *AnalysisUpdate) error
```

**Planner (`catalogarr-segments-plan`):**

- **Episodes:** it lists the season's Episodes through the uncached reader with the existing series-ref field index, if it exists, or a label selector; check `app/catalog/controller/episode` for which. Then their MediaFiles.
- **For each file it builds an `AnalyzeFile`:**
  - `Due` from `segments.Due`;
  - `Anime` from the Series' `EffectiveEpisodeOrder` or series type;
  - the audio stream is the probe's default audio index.
- It publishes one `AnalyzeTask`. Movies are one-file tasks.

**Results (`catalogarr-segments-result`):**

- A result whose probe hash differs from the file's current one is dropped. This pins Review Focus 2.
- Otherwise it writes the raw result to `clustarr-segments`, then `ApplyMerged` with the analysis update.

**Steps:**

- [ ] **Failing tests,** on the fake client and membus:
  - `TestDueEpisodePublishesOneSeasonPlan`: two episodes of a season give one message within the bucket;
  - `TestPlannerBuildsTheSeasonTask`;
  - `TestStaleResultIsDropped`;
  - `TestMergeKeepsAnalysisCreditsWhenTheIntroDBAddsAnIntro`: Review Focus 3, through the TheIntroDB handler path;
  - `TestApplyMergedRedoesOnConflict`: a write interleaved between read and apply, using an interceptor client that bumps the resourceVersion once, and the final object has both writers' fields;
  - `TestTheSegmentsManagerIsOnePatchStatusAccepts`: `catalogarr-markers` still, so there's no new manager.
- [ ] **Implement.** Refactor the markers handler's apply into `ApplyMerged` without changing its tests' expectations.
- [ ] `go test ./app/catalog/... ./cmd/clustarr/`, which includes the registration guard. Expected: PASS.
- [ ] **Version bump.** A raised `segments.AnalyzerVersion` makes every file due. Its reconcile publishes the plan, because `followUpMarkers` runs on every MediaFile reconcile. That holds once the 10 h resync reaches the file, or at catalogarr's start.
- [ ] Commit: `feat(catalog): plan segment analysis per season, record worker results and merge them under TheIntroDB in status.markers`.

### Task 12: Installers and image

**Files:**

- Modify: `images/Dockerfile.media`:
  - builds `./cmd/segmentarr-worker`;
  - adds a pinned `libonnxruntime.so`, the latest release from GitHub `microsoft/onnxruntime`, linux-x64, with its SHA-256 checked;
  - sets `ENV ORT_LIB_PATH`.
- Create: `charts/clustarr/templates/segmentarr-worker.yaml`. A Deployment:
  - `automountServiceAccountToken: false`;
  - no ServiceAccount binding;
  - the shared securityContext and UMASK;
  - `NATS_URL`, `POD_NAME`;
  - `/data` read-only;
  - grace equal to the consumer AckWait;
  - an optional KEDA ScaledObject on `segmentarr-analyze`.
- Modify: `charts/clustarr/values.yaml`, adding `segmentarrWorker: {enabled: true, replicas: 1, concurrency: 1, resources: {limits: {cpu: "2", memory: 2Gi}, requests: {cpu: 250m, memory: 512Mi}}, keda: {enabled: false}}`.
- Create: `config/manager/segmentarr-worker.yaml`, and add it to the kustomization.
- Test:
  - `cmd/clustarr`'s chart and kustomize parity test;
  - the NATS URL guard;
  - a new `TestSegmentarrWorkerHasNoCredentials`: the rendered pod has no token and no RBAC subject.

**Steps:**

- [ ] **Failing test.** `TestSegmentarrWorkerHasNoCredentials`.
- [ ] **Implement.** Then `helm dependency build charts/clustarr` (CLAUDE.md gotcha), and `go test ./cmd/clustarr/`.
- [ ] Commit: `feat(deploy): segmentarr-worker in the media image with ONNX Runtime, its chart template and kustomize manifest`.

### Task 13: UI and docs

**Files:**

- Modify: `ui/detail.go`. `markersLabel` shows each segment's source as a short tag, plus `(analysis 0:00–0:00)`. Test in `ui/detail_test.go`.
- Create: `docs/research/segment-detection.md`:
  - Intro Skipper's defaults with their source;
  - the Plex preferences of Ruling P-1;
  - the ONNX Runtime and model versions and checksums.
- Modify: `CLAUDE.md`: one paragraph under the skip-segments paragraph naming:
  - the worker;
  - the precedence;
  - `segments.AnalyzerVersion`;
  - the buckets.

**Steps:**

- [ ] **Failing test.** `TestMarkersLabelNamesTheSource`.
- [ ] **Implement.**
- [ ] `go test ./ui/...`.
- [ ] Commit: `feat(ui): the files table names each segment's source` and `docs: segment detection research and CLAUDE.md`.

## cluster-plex

### Task 14: Source-aware ownership and Plex's detection off

**Files:**

- Modify: `pkg/plexseed/input.go`: `Segment.Source`, `Confidence`.
- Modify: `pkg/plexseed/rows.go`. `pv:source` is the segment's source, `theintrodb` when it is empty.
- Modify: `pkg/plexseed/seeder.go`. `ours()` accepts `pv:source` ∈ {theintrodb, chapters, analysis}.
- Modify: `pkg/plex/prefs/required.go`. Add `MarkerSource: cloud` and `GenerateAdMarkerBehavior: never`, each with a comment citing Ruling P-1.
- Modify: `docs/adr/0006-…`, recording both.
- Test: `pkg/plexseed/rows_test.go`, `seeder_test.go` (PostgreSQL), `pkg/plex/prefs/required_test.go`.

**Steps:**

- [ ] **Failing tests:**
  - `TestMarkerRowsTagTheirSource`;
  - `TestRowsOfEverySourceAreOurs`, on PostgreSQL: an `analysis` credits row is replaced when TheIntroDB credits arrive, and a legacy `theintrodb` row stays ours;
  - `TestRequiredTurnsOffLocalMarkerDetection`.
- [ ] **Implement.** Then `make test`, `make lint`, and the PostgreSQL suite, with `CLUSTERPLEX_TEST_POSTGRES_DSN` against a throwaway `postgres:15-alpine`.
- [ ] Commit: `feat(plexseed): markers from every clustarr source are ours` and `feat(prefs): Plex detects no intro, credits or ad markers locally`.

## Deploy and live proof

### Task 15: kind-cluster-plex

- [ ] **clustarr.** Deploy chain N with the scratchpad toolkit (`newdeploy.sh N HEAD`, then build, then deploy; no gate, no push). Confirm:
  - `segmentarr-worker` Ready;
  - `clustarr_segments_dnn_available 1`.
- [ ] **cluster-plex.** Build the image at HEAD, `kind load`, bump `newTag` in `k8s/overlays/kind-cluster-plex`, then `kubectl apply -k`. Confirm on a pod, through `/:/prefs`, that `MarkerSource=cloud`.
- [ ] **Proof 1: Dexter.** Plan its season 1, by waiting for the reconcile or deleting a stale plan message. Compare analysis intros and credits with TheIntroDB's for the same files. Record:
  - the median absolute difference of start and end;
  - how many files are within 5 s.
- [ ] **Proof 2: Andor season 1, which TheIntroDB lacks.** Confirm intros and credits in `status.markers` with `source: analysis`, and in Plex through `includeMarkers`.
- [ ] **Proof 3: Plex does no local detection.** Watch a Plex pod for marker-generation activity after a library refresh; there should be none. Use the Plex log and process list through a debug container.
- [ ] **Pushing.** Ask the owner before pushing either repository.
