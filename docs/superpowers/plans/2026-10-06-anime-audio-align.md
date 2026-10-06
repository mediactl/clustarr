# Audio Alignment and G1 Implementation Plan (anime dual-audio phase 3)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `pkg/audioalign`, a pure-Go port of the spike's method (spec
§7.1), proven on synthetic transforms and then on a real pair of one
Monster episode (gate G1). G1 sets the acceptance thresholds phase 4 uses.

**Architecture:** 8 kHz mono `[]float32` in. Onset features come from a
512-point STFT with a 20 ms hop over 24 log-spaced bands from 150 to
3,800 Hz. The rate is the candidate whose window peaks are strongest,
refined by a line fit. Offsets come from 20 s windows every 30 s
(cross-correlation summed over bands in the frequency domain). Segments
are runs of constant offset. `Accept` and `Verify` gate the result.
Everything is pure Go, including a radix-2 FFT, with no new module.

**Spec:** `docs/superpowers/specs/2026-10-06-anime-dual-audio-design.md` §2, §7.1, §10 (G1).

**Execution:** Native, approved 2026-10-06 (the owner approved all phases
and the one G1 download).

## Global Constraints

- No new module dependency. No `go get` or `go mod tidy`.
- Tests use synthetic signals generated in the test, with no large
  fixtures. The real-pair check is a test that skips unless
  `CLUSTARR_AUDIOALIGN_DONOR` and `CLUSTARR_AUDIOALIGN_TARGET` name 8 kHz
  mono s16le files.
- The G1 media files stay in the scratchpad and are never committed.

## Review Focus

1. **Silence or very quiet audio**, such as a title card: windows with no
   onsets must read not confident rather than produce a confident wrong
   offset.
2. **A donor shorter than the target**, or the reverse.
3. **25/24 against 25/23.976**, 0.1% apart. The refinement must tell them
   apart over a 24-minute episode.
4. **A cut in either direction**: the donor missing material, or the
   donor having extra.
5. **Runtime on a 24-minute episode** within a CPU pool worker's budget,
   well under a minute.

---

### Task 1: `pkg/audioalign`

**Files:** create `pkg/audioalign/{fft.go,features.go,align.go,accept.go,doc.go}`
and `pkg/audioalign/align_test.go`.

**Produces:**

```go
const SampleRate = 8000
type Segment struct{ DonorStart, TargetStart, Length time.Duration }
type Result struct {
	Rate      float64       // donor speed / target speed; the donor is stretched by Rate onto the target
	RateName  string        // "1", "25/23.976", ...
	RateMargin float64      // best median peak / runner-up's
	Segments  []Segment
	Coverage  float64       // confident windows' share of the target, 0-1
	Windows, Confident int
}
func Align(donor, target []float32) (Result, error)
type Thresholds struct{ MinCoverage float64; MaxSegments, MinSegmentWindows int; MinRateMargin float64 }
var DefaultThresholds Thresholds
func (r Result) Accept(t Thresholds) error
func Verify(donorAnchor, targetAnchor []float32, r Result) (medianResidual time.Duration, within80ms float64)
func Transform(donor []float32, r Result, targetLen int) []float32 // what the mux applies: stretch by Rate, then place segments, silence elsewhere
```

- [ ] Tests first, on a synthetic soundtrack: 10 minutes of random
  decaying tone bursts, about 4 a second at 200 to 3,000 Hz, plus low
  noise, from a fixed seed. Test helpers resample by a rate (linear
  interpolation), delay (prepend silence) and cut (remove a span). Cases:
  1. Identity: rate "1", offset 0, one segment, coverage ≥ 0.9.
  2. PAL speed-up plus a 3.217 s delay: RateName "25/23.976", and one
     segment whose offset is within 40 ms.
  3. As 2, plus 10 s removed from the donor at 300 s: two segments, the
     second jumping by about 10.4 s on the stretched timeline.
  4. Extra donor material, 8 s inserted at 200 s: two segments.
  5. A 25/24 donor resolves to "25/24", not "25/23.976".
  6. Unrelated signals (two seeds): Accept fails, with coverage below the
     threshold.
  7. A target with 60 s of silence in the middle: no confident window
     lands in the silence, and the result still Accepts.
  8. Verify on case 2's result gives a median residual ≤ 40 ms and ≥ 90%
     of windows within 80 ms.
  9. Transform then Verify round-trips: the transformed donor aligns at
     rate 1, offset 0.
  10. A runtime guard: 24 synthetic minutes align in under 30 s, skipped
      under `-short`.
- [ ] Run them and watch them fail.
- [ ] Implement the method as the spec describes:
  - rate selection on a subset of windows (every 3rd) per candidate, then
    every window at the chosen rate;
  - cross-correlation as `IFFT(Σ_b FFT(T_b)·conj(FFT(W_b)))`, with each
    target band's FFT computed once per rate;
  - a window is confident when its peak is ≥ 1.5× the best peak outside
    ±1 s, and its peak is ≥ 0.05 in absolute value (the silence guard);
  - segments are runs of confident windows whose offsets agree within
    40 ms;
  - rate refinement: a line fit of the lags in the longest run against
    window time, applied as a correction to the candidate rate.
- [ ] Run `go test ./pkg/audioalign/`. Expected: PASS. Commit.

### Task 2: G1, the real pair

- [ ] Decode with local ffmpeg to 8 kHz mono s16le:
  - the BoB DVD S01E02 Japanese track (the file is in the scratchpad);
  - the library's Netflix S01E02 Japanese track (already extracted as
    `scratchpad/graft/nf_e02_jpn.pcm`).
- [ ] Add `TestRealPair`, which skips without the two env vars. It runs
  `Align`, prints the Result, `Accept(DefaultThresholds)` and `Verify`,
  and fails if Accept fails. Run it on the pair.
- [ ] Record the result in the spec under "G1 result" (rate, segments,
  coverage, margin, residuals). If it fails, read why, adjust the
  thresholds or the method under superpowers:systematic-debugging, and
  record that. If no reasonable method aligns it, stop and report: spec
  §10 says phase 4 is redesigned then.
- [ ] Also run the English track of the DVD against the Netflix Japanese
  track, for information only (test B on real sources).
- [ ] Commit the test and the spec note.
