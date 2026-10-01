# Segment detection: verified facts

Everything clustarr's own intro, credits, recap and preview detection
(`pkg/segments`, `cmd/segmentarr-worker`) was built against, as checked on
2026-10-01.

## Prior art: Jellyfin's Intro Skipper

Read from `intro-skipper/intro-skipper`, master, through DeepWiki and the
source. These are its defaults.

**Intro.** Each episode's first 25% (`AnalysisPercent`), at most 10 minutes
(`AnalysisLengthLimit`), is fingerprinted with Chromaprint. Episodes in a
season are compared pairwise through an inverted index:

- two points match when at most 6 of 32 bits differ
  (`MaximumFingerprintPointDifferences`);
- gaps up to 3.5 s are bridged (`MaximumTimeSkip`);
- `InvertedIndexShift` is 2;
- the longest shared region of 15–120 s is the intro.

**Credits.** Candidates come from three sources:

- chapters;
- an end-of-file fingerprint;
- black keyframes: `-skip_frame nokey`, `blackframe=amount=0:threshold=28`,
  with 85% of pixels black.

An entropy and saturation fallback catches credits that are not on black.
Credits run 15–450 s, or up to 900 s in a movie.

**Chapter-name patterns.** Taken verbatim from
`IntroSkipper/Configuration/PluginConfiguration.cs`. Each uses
`(?![\s:]+End)`, so `pkg/segments` compiles them with regexp2.

| Segment | Pattern |
| --- | --- |
| intro | `(^\|\s)(Intro\|Introduction\|OP\|Opening)(?![\s:]+End)(\s\|:\|$)` |
| credits | `(^\|\s)(Credits?\|ED\|Ending\|Outro)(?![\s:]+End)(\s\|:\|$)` |
| preview | `(^\|\s)(Preview\|PV\|Sneak\s?Peek\|Coming\s?(Up\|Soon)\|Next\s+(time\|on\|episode)\|Extra\|Teaser\|Trailer)(?![\s:]+End)(\s\|:\|$)` |
| recap | `(^\|\s)(Re?cap\|Sum{1,2}ary\|Prev(ious(ly)?)?\|(Last\|Earlier)(\s\w+)?\|Catch[ -]up)(?![\s:]+End)(\s\|:\|$)` |

## Chromaprint

`pkg/segments/chromaprint` ports Chromaprint's default algorithm (test2):

- 4,096-sample frames, hopping 1,365 samples at 11,025 Hz;
- a Hamming window scaled by 1/32767, then the power spectrum;
- 12 chroma bands over 28–3,520 Hz, with no interpolation;
- the chroma filter `{0.25, 0.75, 1, 0.75, 0.25}`;
- Euclidean normalization with a 0.01 floor;
- the 16 `kClassifiersTest2` classifiers and gray code.

It matches the media image's ffmpeg (`-f chromaprint -fp_format raw`, BtbN
n9.0.2) bit for bit on `test/data/segments/*.s16le`. It fingerprints ten
minutes of audio in 0.93 s on one core.

**Test audio.** White noise is useless as test audio. Its chroma is flat, so
every clip fingerprints alike and matches everywhere. Tests use melodies
instead: half-second tones at random pitches.

## Frame statistics (1 fps, 128×72 grey)

Measured on the owner's library (`test/data/segments/frames`):

| Clip | Black | Entropy (bits) | Edges (‰) | Scroll |
| --- | --- | --- | --- | --- |
| Blade Runner rolling credits | 100% | — | — | 6 rows/s, 10 frames |
| Her, dense rolling credits | 100% | — | — | ~20 rows/s; aliases at 1 fps |
| Dexter name cards over grey | none | 4.00–4.06 | 73–95 | — |
| Normal scenes | none | ≥ 4.51 | ≥ 207 | — |

Text over a flat picture is therefore entropy under 4.3 bits with edges of
20–200 ‰. The margin is thin, so a run carried only by that rule scores
confidence 60. That is not enough to stand alone, and it asks the text
detector to confirm.

## Text detection

**Model.** PaddleOCR PP-OCRv5 mobile detection (DB), as converted by
`ilaylow/PP_OCRv5_mobile_onnx` (Apache-2.0, paddle2onnx). Details:

- SHA-256 `1eb7b4f7ab657ebd1c66d5f79bca7497f29768a2e3c15e52daecbba1a8e4a039`;
- 4.8 MB, embedded with `go:embed`;
- PaddlePaddle's own repositories ship no ONNX, and the other mirrors
  returned 404 or served the 84 MB server model.

**Runtime.** ONNX Runtime 1.23.2 through `shota3506/onnxruntime-purego`,
which supports only C API 23 (ORT 1.23.x). The media image pins its
linux-x64 and linux-aarch64 archives by SHA-256.

**Density.** The share of pixels with a text probability over 0.3, in
thousandths. Dexter's name card reads 9 ‰ and a scene with a name tag
reads 0 ‰, so the threshold is 5 ‰.

**Search.** Samples every 30 s across the credits window, then a binary
search to 1 s: about 20 inferences per file.

## Plex

These preferences come from a live server's `/:/prefs` (2026-10-01).

| Preference | Default | Options |
| --- | --- | --- |
| `GenerateIntroMarkerBehavior` | `asap` | `never`, `scheduled`, `asap` |
| `GenerateCreditsMarkerBehavior` | `asap` | `never`, `scheduled`, `asap` |
| `GenerateAdMarkerBehavior` | `scheduled` | `never`, `scheduled`, `asap` |
| `MarkerSource` | `any` | `any` (both, online first), `cloud` (only online, no local detection), `local` |

cluster-plex sets `MarkerSource=cloud` and `GenerateAdMarkerBehavior=never`.
Plex then does no local detection, but still fetches its own online markers
for the kinds clustarr has nothing for.
