# Segment detection in clustarr: design

Status: proposed, 2026-10-01. It builds on
`2026-09-30-plex-analyze-bypass-design.md`, which added TheIntroDB markers in
`MediaFile.status.markers` and cluster-plex's seeding of them into Plex.

## 1. Goal

clustarr detects the skip segments of a movie or episode file itself:

- intro;
- recap;
- credits;
- preview.

Plex's pods then never run their own intro or credits detection. On a
library of about 12,000 files that work, run inside the Plex pods, competes
with playback.

TheIntroDB, when it has a segment, stays first. In practice TheIntroDB has
about 20% of the owner's library:

- about 500 of 2,500 files asked on 2026-10-01 were Found;
- no movies at all.

Local analysis fills the rest.

**Success:**

- Every probed movie and episode file has credits when its end has
  detectable credits.
- Every episode in a season of two or more files has an intro when the
  season shares one.
- Plex's intro and credits detection is off for the Clustarr libraries.
- Analysis runs in a worker whose CPU the owner caps, outside Plex.

## 2. Owner's decisions (2026-10-01)

1. **Fingerprint.** An in-tree Go port of Chromaprint. It is tested bit for
   bit against ffmpeg's `chromaprint` muxer. ffmpeg only decodes.
2. **Placement.** A new credential-less worker binary on the media image, in
   its own Deployment. Results reach `MediaFile.status` through catalogarr,
   as `squasharr-worker`'s reach `TranscodeJob.status`.
3. **Precedence, per segment kind:** TheIntroDB, then chapters, then
   analysis; for credits, a chapter wholly titled as credits comes first
   (amended 2026-10-01, §6.6). cluster-plex turns off Plex's intro and credits detection.
4. **Scope of v1.**
   - **Intros:** by season-wide audio fingerprint.
   - **Credits:** by chapters, an end-of-file fingerprint, frame statistics
     and a gated DNN stage. Movies get credits only.
   - **Recap and preview:** from chapter names only.
5. **Structure.** One merged `status.markers.segments` list, each segment
   tagged with its source. The raw analysis is kept clustarr-internal
   (§5.2).
6. **DNN stage.** PaddleOCR's text-detection model on ONNX Runtime, through
   `onnxruntime-purego`, so clustarr's build has no cgo. It runs only when
   the cheaper credit signals are missing or weak.

## 3. Prior art

Jellyfin's Intro Skipper (`intro-skipper/intro-skipper`, read through
DeepWiki on 2026-10-01) is the reference. Its defaults, which this design
adopts where it says so:

**Intro.**

- Chromaprint fingerprints of the first `AnalysisPercent` (25%) of each
  episode, at most `AnalysisLengthLimit` (10 min).
- Episodes are compared pairwise within a season through an inverted index.
- Two points match when at most `MaximumFingerprintPointDifferences` (6 of
  32) bits differ.
- `MaximumTimeSkip` 3.5 s, `InvertedIndexShift` 2.
- The longest shared region within `MinimumIntroDuration` 15 s and
  `MaximumIntroDuration` 120 s.

**Credits.** Candidates from three analyzers:

- chapters (`ChapterAnalyzerEndCreditsPattern`);
- an end-of-file Chromaprint;
- black keyframes: `-skip_frame nokey`, `blackframe=amount=0:threshold=28`,
  `BlackFrameMinimumPercentage` 85%.

There is an entropy and saturation fallback for credits on a non-black
background. Duration bounds:

- `MinimumCreditsDuration` 15 s;
- `MaximumCreditsDuration` 450 s;
- `MaximumMovieCreditsDuration` 900 s.

**Recap and preview.**

- Chapter-name patterns: `ChapterAnalyzerRecapPattern`,
  `ChapterAnalyzerPreviewPattern`.
- For anime, a preview runs from the end of the credits.

**Text detection.** A text detector, finding boxes and never reading text,
over sampled end frames, then a run of text-dense frames, is the pattern
attributed to Plex's credit detection. That attribution is unverified. The
pipeline stands on its own.

**Libraries checked (2026-10-01):**

- **Pure-Go Chromaprint ports.** `alexgorbatchev/gochromaprint` and
  `richardwooding/fingerprint` v0.5.0 are MIT, weeks old, with no importers.
  They are reference reading only. Chromaprint is LGPL-2.1+, which GPL-3
  absorbs.
- **ONNX Runtime.** `shota3506/onnxruntime-purego` binds the ONNX Runtime
  shared library through purego, with no cgo. `Allod-Solutions/go-ocr` runs
  PaddleOCR's PP-OCRv5 detection on it.
- **Pure-Go ONNX interpreters.** gonnx and onnx-go are about 8× slower than
  ONNX Runtime.
- **The media image's ffmpeg** (BtbN n9.0.2) has:
  - the `chromaprint` muxer;
  - `blackframe`, `blackdetect`;
  - `silencedetect`, `scdet`, `freezedetect`.

## 4. Architecture

```text
MediaFile reconciler ──segments.Due──▶ work.catalogarr.segments-plan.<season|file>   (scheduled +5 min)
                                              │
                              catalogarr planner (lists the season's files)
                                              ▼
                                   work.segmentarr.analyze  ──▶  segmentarr-worker (ffmpeg decode + Go)
                                                                     │   fingerprints ⇄ clustarr-fingerprints (object store)
                                                                     ▼
                              work.catalogarr.segments-result (one message per file)
                                              │
            catalogarr result consumer: raw → clustarr-segments (KV); Merge(TheIntroDB, analysis) → status.markers
                                              ▼
                     cluster-plex seeder (unchanged input: status.markers.segments) → Plex taggings
```

### 4.1 Components

**`pkg/segments`.** Pure, testable without a cluster:

- `chromaprint`: the port;
- `align`: intro and ending-theme alignment;
- `frames`: frame statistics and scroll;
- `chapters`: name rules;
- `Merge`, `Due`, `AnalyzerVersion`.

**`pkg/segments/textdet`.** The DNN stage behind an interface:

```go
Detector{ Density(img) (int32, error) }
```

The production detector is ONNX Runtime through purego. Tests use a fake.

**`cmd/segmentarr-worker`.** A credential-less binary, its name open until
the plan.

- It needs NATS, and `/data` read-only.
- It holds no ServiceAccount token.
- Its exit codes follow `squasharr-worker`'s conventions.

**catalogarr:**

- **Planner:** a consumer of `work.catalogarr.segments-plan`.
- **Result consumer:** a consumer of `work.catalogarr.segments-result`.
- **Merge-and-apply helper:** both TheIntroDB's handler and the result
  consumer write through it. It re-reads, then applies under
  `catalogarr-markers` with a `resourceVersion` precondition. A Conflict is
  redone from a fresh read. This is CLAUDE.md's rule for two write paths
  under one manager.
- They run in the metadata gateway's Deployment, beside the TheIntroDB marker
  worker.

**cluster-plex:**

- source-aware marker ownership;
- Plex's intro and credits detection turned off (§8).

### 4.2 Work units

**Episodes.** A season is one task: a Series and a season number. The
reconciler of any due episode file publishes a season plan signal.

- It is scheduled 5 minutes ahead with `events.WithScheduleAt`.
- Its MsgID is the season plus a 5-minute bucket.
- A season being imported is planned once.

**Movies.** One task per file, with no delay.

**The planner.** It reads the season's Episodes and their MediaFiles through
the Episode index. It publishes one `AnalyzeTask`. For each file:

- path, probe hash, duration;
- the main audio stream index (the probe's default audio);
- chapters (title, start, end);
- whether start and end fingerprints are already cached;
- whether the file itself is due. A file that is not due is still
  fingerprinted, to give its siblings a comparison.

## 5. Data model

### 5.1 API (`api/catalog/v1alpha1`)

`MarkerSegment` gains two fields:

- `source`: enum `theintrodb`, `chapters`, `analysis`;
- `confidence`: `int32`, 0–100. This is the `Percent` convention: no floats
  in `api/`.

`FileMarkers.segments` becomes the merged list, still at most 20.

The existing fields remain TheIntroDB's fetch bookkeeping:

- `result`, `fetchedAt`, `forProbeHash`;
- `durationMs`, `message`, `notFoundSince`.

New field `FileMarkers.analysis *SegmentAnalysis`:

- `result`: enum `Found`, `NotFound`, `Error`;
- `analyzedAt`: `metav1.Time`;
- `forProbeHash`: string, MaxLength 128;
- `version`: `int32`;
- `message`: string, MaxLength 512.

The cluster-plex seeder already reads `segments` and needs no change of
input.

### 5.2 Internal storage (NATS, both file-backed)

These are file-backed through `BucketSpec.Durable`, which keeps them on file
storage even on single-node NATS. That matters because they are rebuilt only
by re-analysis.

**`clustarr-segments` (key-value).**

- One entry per MediaFile UID, through `events.KVKeyToken`:
  `{probeHash, version, segments[] with source and confidence, result,
  message}`.
- The merge needs it, because analysis segments that lose to TheIntroDB are
  not in status.
- TheIntroDB's raw result is the `theintrodb`-tagged subset of `segments`,
  because it always wins its kinds. So no second copy is kept.

**`clustarr-fingerprints` (object store, 1 GiB cap).**

- Keys: `<probeHash>.start` and `<probeHash>.end`, raw `uint32` little-endian.
- About 20 KB each.
- A new probe hash means a new key. The bucket's max age (90 days) expires
  old keys. An object store has no expiry per object, so a fingerprint
  still in use that expires is simply recomputed: seconds of decoding.
- `TestEnsureSingleNodeTopologyFitsTheKindServersLimits` must cover it.

### 5.3 The probe

`mediainfo` records chapter titles and times. Today it records only a count.

`mediainfo.ProbeVersion` is raised once. Every file is then re-probed exactly
once, through `status.probeVersion`. The probe hash never changes for this
(CLAUDE.md).

## 6. Detection

ffmpeg is invoked only to decode. It writes raw data to a pipe, which Go
reads.

| Purpose | Command shape |
| --- | --- |
| Audio window | `ffmpeg -nostdin -ss S -t D -i F -map 0:a:N -ac 1 -ar 11025 -f s16le -` |
| End frames | `ffmpeg -nostdin -ss S -i F -an -sn -dn -vf fps=1,scale=128:72:flags=area,format=gray -f rawvideo -` |
| DNN frame | `ffmpeg -nostdin -ss T -i F -frames:v 1 -vf scale=320:180:flags=area -f rawvideo -pix_fmt rgb24 -` |

The subprocess rule from CLAUDE.md applies: `Setpgid`, a process-group kill
as `Cancel`, and `WaitDelay`.

### 6.1 Fingerprinter (`pkg/segments/chromaprint`)

A port of Chromaprint's default algorithm (algorithm 2):

- 11,025 Hz mono input;
- 4,096-sample frames with 2/3 overlap;
- chroma over 28–3,520 Hz in 12 bands, with Chromaprint's chroma filter and
  normalization;
- 16 fixed classifiers with gray-code quantization;
- one `uint32` per about 0.1238 s.

GPL-3 header, with attribution to Chromaprint (LGPL-2.1+). Test: bit-exact
against `ffmpeg -f chromaprint -fp_format raw` on recorded clips (§9).

### 6.2 Intros (episodes, per season)

**Window.** The first `min(25% × duration, 10 min)`.

**Comparison.** Each episode is compared with up to 4 neighbours by episode
number, two before and two after. That bounds the work at about 4n
comparisons rather than n²/2.

**One comparison (Intro Skipper's procedure):**

1. Build an inverted index of each stream's points, shifted by 2.
2. Collect candidate offsets.
3. For each offset, scan the aligned points with Hamming distance ≤ 6.
4. Bridge gaps ≤ 3.5 s.
5. Keep the longest region of 15–120 s.

**Per episode.** The intro is the median start and median end of its
comparisons' regions. At least 2 comparisons must agree within 3 s;
otherwise it gets no intro.

**Bounds.** A season with a single file gets no intro. An episode whose
region starts after the window's end, which cannot happen by construction,
is guarded anyway.

### 6.3 Credits (episodes and movies)

**Window.** The last 15% of the file, at most 450 s for an episode and 900 s
for a movie.

**Signals:**

1. **Chapters.** A chapter matching the credits pattern (§6.5). Confidence
   100.
2. **Ending theme** (episodes only). The same alignment as §6.2, over the end
   window across the same neighbours. A shared region that runs to within
   5 s of the end, or to a preview chapter, is a candidate.
3. **Frame statistics,** per sampled second:
   - **Black:** at least 85% of pixels with luma < 28.
   - **Entropy:** the 64-bin luma histogram entropy, in thousandths, an
     integer.
   - **Edge density:** the share of Sobel-magnitude pixels over a threshold.
   - **Scroll:** the best vertical shift between consecutive frames'
     row-brightness profiles, found by normalized cross-correlation. "Scroll"
     means the same nonzero shift (±1 px) holding for at least 5 seconds.

   A credit-like second is black, or low-entropy with high edge density, or
   scrolling. A run of at least 15 s of credit-like seconds, allowing 2 s
   gaps, is a candidate. Its confidence depends on its length and
   uniformity.
4. **DNN text density** (§6.4). Only when 1–3 give no candidate, or only
   candidates below 70.

**Combining.**

- When two signals agree within 5 s, the start is the earlier: confidence 90.
- A single signal at 70 or above stands at its own confidence.
- Below 60, nothing is written.

The credits must end within 5 s of the end of the file, or where a preview
starts. Bounds: 15–450 s for an episode, 15–900 s for a movie.

### 6.4 DNN stage (`pkg/segments/textdet`)

**Model.** PaddleOCR's text-detection model (DB, MobileNetV3 backbone,
PP-OCRv5 mobile detection, or the newest such model the plan verifies), as
ONNX.

- Apache-2.0.
- About 4 MB.
- Embedded in the worker with `go:embed`.
- Its SHA-256 is recorded in the source.

**Runtime.** ONNX Runtime through `onnxruntime-purego`.

- `libonnxruntime.so` is pinned by version and checksum in
  `images/Dockerfile.media`.
- It is loaded once at worker start.
- When it fails to load, the stage is reported unavailable and the cheaper
  signals carry on (§7).

**Output.** Text-box density only: the summed box area over the frame area,
in thousandths. No recognition.

**Search.** Credits are almost always one block running to the end of the
file.

1. Sample every 5 s across the window.
2. Take the first sample from which density stays over the threshold to the
   end, or to a preview.
3. Binary-search between it and the sample before, to 1 s.

That is about 15 to 25 inferences per file.

**Confidence.** 80 when the dense run is at least 15 s.

### 6.5 Chapters, recaps and previews

**Name patterns.** Intro Skipper's defaults for intro, recap, credits and
preview, copied verbatim once the plan verifies them, under clustarr's own
tests. Matching is case-insensitive.

| Segment | From a chapter |
| --- | --- |
| Intro | the chapter; confidence 100 |
| Recap | the chapter; confidence 100 |
| Credits | the chapter; confidence 100 (§6.3) |
| Preview | the chapter; confidence 100 |

**Anime preview.** A series whose effective type is anime, and whose credits
end more than 5 s before the end of the file, gets a preview from the end of
the credits to the end of the file. Confidence 70.

### 6.6 Precedence (`segments.Merge`)

Per kind: TheIntroDB, then chapters, then analysis.

**Amended 2026-10-01 (owner's ruling):** for credits, a chapter whose whole
title is a credits name ("Credits", "End Credits", "Closing Credits",
"Ending", "Outro"; confidence 100) comes before TheIntroDB. Any other
chapter match ("ED", "Credits Song") is confidence 90 and keeps its place
after TheIntroDB. A chapter is authored for the file's own timeline;
TheIntroDB is matched to it by duration. On the owner's library, 36 of
107 files with both disagreed by more than 5 s, and the chapter was right
in the worst cases (Game of Thrones S02E09, TheIntroDB 8:44 early, mid-
battle; Arcane S01E07, 2:03 late).

- **A kind TheIntroDB has segments for** takes only TheIntroDB's.
- **Otherwise,** chapters' segments of that kind.
- **Otherwise,** analysis's segments of at least 60 confidence.

Segments are ordered by start and capped at 20, as today.

## 7. Errors and limits

**Decoding errors.** An ffmpeg failure, a truncated file, or no audio stream
gives `analysis.result: Error` with the reason. The file is due again after
1 day; its siblings proceed. A season with too few decodable files gets no
intros. Its credits are still analyzed per file.

**A changed or missing file.** The result consumer re-reads the MediaFile.
If the probe hash differs from the result's, the result is dropped. The new
probe makes the file due again.

**Not found.** No segment ≥ 60 gives `NotFound`. It is analyzed again only
on a new probe hash or analyzer version, never on a timer. The same bytes
give the same answer.

**The DNN cannot load.** One error log at start. The metric
`clustarr_segments_dnn_available` is 0. Readiness is unaffected.

**Redelivery.** A killed worker's task is redelivered. Cached fingerprints
make the redo skip decoding already done.

**Deadlines.** About 5 min per file and 30 min per season task. AckWait and
in-progress heartbeats are set as `squasharr-worker` sets them.

**Concurrency and CPU.**

- One task at a time by default; `--concurrency` raises it.
- ffmpeg uses `-threads` equal to `GOMAXPROCS`.
- Scaling out is more replicas.
- The chart defaults to a CPU limit of 2 and 1 replica, with an optional
  KEDA ScaledObject on the analyze consumer for scale to zero.

**Metrics.** No title, path or release labels.

- `clustarr_segments_analyzed_total{result,source}`;
- `clustarr_segments_stage_seconds{stage}`;
- `clustarr_segments_dnn_available`.

## 8. cluster-plex

**Ownership.** The seeder writes `pv:source` as the segment's source:
`theintrodb`, `chapters` or `analysis`. Every such row counts as its own. An
existing `theintrodb` row stays recognized. Otherwise ADR 0006's reconcile
per kind is unchanged.

**Plex's detection is turned off.**

- The server preferences for intro and credits marker generation are set to
  never.
- The `deep-analyze` maintenance is dropped for the Clustarr libraries.

The plan first confirms the preference names and values against a live
Plex, then records them in ADR 0006. Plex's existing markers remain only on
kinds clustarr has no segment for, as today.

## 9. Testing

The owner's standing instruction applies: unit tests. Envtest and e2e are
not run during implementation.

**Fingerprinter.** Bit-exact against ffmpeg's muxer on 2–3 short recorded
clips. The clip and its expected `uint32` stream live under
`test/data/segments/`; the expected values are produced once with ffmpeg and
checked in.

**Alignment.** Synthetic fingerprint streams with a planted shared region,
plus noise. Cases:

- two episodes;
- one episode without the intro;
- a cold open before the intro;
- an intro at different offsets;
- a season of one.

**Frames.** Sequences built from real frames sampled once and checked in,
downscaled: black, rolling text over picture, static cards, normal scenes.
They cover black, entropy, scroll and the 15 s run rule.

**DNN.** The real model on two stored frames, credits text and none. It
skips by name without `libonnxruntime.so`, as the Postgres and envtest
suites skip without their assets.

**Merge and Due.** Table-driven:

- TheIntroDB winning per kind;
- chapters over analysis;
- the confidence floor;
- a preview after credits;
- version bumps.

**Result consumer.** A fake client, including a concurrent TheIntroDB write
between read and apply.

**Worker pod.** Held to the installer's securityContext and NATS wiring by
test. This is the runtime-built-pod gotcha in CLAUDE.md.

**cluster-plex.** PostgreSQL tests for rows from several sources and the
legacy `theintrodb` tag.

**Live proof on kind-cluster-plex.**

1. Analyze a season TheIntroDB has (Dexter). Local intros and credits should
   fall within a few seconds of TheIntroDB's.
2. Analyze one it lacks (Andor). Its intros and credits should reach Plex
   through `includeMarkers`.
3. Turn Plex's detection off. Confirm the Plex pods run no intro or credits
   analysis.

## 10. Out of scope (v1)

- Recaps and previews detected from audio.
- GPU inference.
- Movie intros.
- Sharing fingerprints or segments with TheIntroDB or anyone else.
- Character recognition of the credits text.
