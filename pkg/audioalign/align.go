/*
Copyright 2026 The Clustarr Authors.

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU General Public License for more details.

You should have received a copy of the GNU General Public License
along with this program.  If not, see <https://www.gnu.org/licenses/>.
*/

package audioalign

import (
	"errors"
	"fmt"
	"math"
	"runtime"
	"sort"
	"sync"
	"time"
)

// Segment places a stretch of the donor on the target: the donor from
// DonorStart (on its timeline stretched by Result.Rate) plays from
// TargetStart for Length. TargetStart-DonorStart is the segment's offset.
type Segment struct {
	DonorStart, TargetStart, Length time.Duration
}

// Result is an alignment of a donor onto a target.
type Result struct {
	// Rate is how much faster the donor plays than the target; the donor
	// is stretched by Rate onto the target's timeline (25/23.976 for a PAL
	// speed-up of a film-rate target).
	Rate float64
	// RateName is the standard ratio Rate is, or Rate printed.
	RateName string
	// RateMargin is the chosen rate's median window peak over the best
	// candidate's more than 1% away (another family of ratios).
	RateMargin float64
	Segments   []Segment
	// Coverage is the share of the target the confident windows of the
	// segments cover, 0 to 1.
	Coverage  float64
	Windows   int
	Confident int
}

type rateCandidate struct {
	name string
	r    float64
}

// candidates are the speed ratios real releases differ by.
var candidates = []rateCandidate{
	{"1", 1}, {"25/23.976", 25 / 23.976}, {"23.976/25", 23.976 / 25},
	{"25/24", 25.0 / 24}, {"24/25", 24.0 / 25}, {"1.001", 1.001}, {"1/1.001", 1 / 1.001},
}

const (
	winFrames    = 1000 // 20 s
	stepFrames   = 1500 // 30 s
	guardFrames  = 50   // 1 s around a peak
	minRatio     = 1.5
	minPeak      = 0.05
	agreeFrames  = 2 // 40 ms
	coarseStride = 3 // rate selection reads every 3rd window
)

type window struct {
	start     int // donor frame (stretched timeline)
	lag       int // target frame - donor frame
	peak      float64
	ratio     float64
	confident bool
}

// target holds the target's per-band spectra for one FFT size.
type target struct {
	n    int
	size int
	spec [][]complex128
}

func newTarget(t features) *target {
	n := t.frames()
	size := nextPow2(n + winFrames)
	tg := &target{n: n, size: size, spec: make([][]complex128, len(t))}
	for b, band := range t {
		s := make([]complex128, size)
		for i, v := range band {
			s[i] = complex(v, 0)
		}
		fft(s, false)
		tg.spec[b] = s
	}
	return tg
}

// correlate scores the donor window d[start:start+winFrames] at every
// target position.
func (tg *target) correlate(d features, start int) []float64 {
	acc := make([]complex128, tg.size)
	buf := make([]complex128, tg.size)
	for b := range d {
		for i := range buf {
			buf[i] = 0
		}
		for i := 0; i < winFrames; i++ {
			buf[i] = complex(d[b][start+i], 0)
		}
		fft(buf, false)
		ts := tg.spec[b]
		for i := range acc {
			w := buf[i]
			acc[i] += ts[i] * complex(real(w), -imag(w))
		}
	}
	fft(acc, true)
	valid := tg.n - winFrames + 1
	if valid < 1 {
		return nil
	}
	out := make([]float64, valid)
	scale := float64(tg.size) * float64(winFrames) * float64(len(d))
	for i := range out {
		out[i] = real(acc[i]) / scale
	}
	return out
}

func (tg *target) windows(d features, stride int) []window {
	var starts []int
	for st := 0; st+winFrames <= d.frames(); st += stepFrames * stride {
		starts = append(starts, st)
	}
	out := make([]window, len(starts))
	ok := make([]bool, len(starts))
	// Windows are independent, so they correlate in parallel.
	sem := make(chan struct{}, runtime.GOMAXPROCS(0))
	var wg sync.WaitGroup
	for i, st := range starts {
		wg.Add(1)
		sem <- struct{}{}
		go func(i, st int) {
			defer func() { <-sem; wg.Done() }()
			out[i], ok[i] = tg.window(d, st)
		}(i, st)
	}
	wg.Wait()
	var res []window
	for i := range out {
		if ok[i] {
			res = append(res, out[i])
		}
	}
	return res
}

func (tg *target) window(d features, st int) (window, bool) {
	s := tg.correlate(d, st)
	if s == nil {
		return window{}, false
	}
	k := 0
	for i, v := range s {
		if v > s[k] {
			k = i
		}
	}
	second := math.Inf(-1)
	for i, v := range s {
		if (i < k-guardFrames || i > k+guardFrames) && v > second {
			second = v
		}
	}
	ratio := math.Inf(1)
	if second > 0 {
		ratio = s[k] / second
	}
	return window{start: st, lag: k - st, peak: s[k], ratio: ratio,
		confident: ratio >= minRatio && s[k] >= minPeak}, true
}

func medianPeak(ws []window) float64 {
	if len(ws) == 0 {
		return 0
	}
	p := make([]float64, len(ws))
	for i, w := range ws {
		p[i] = w.peak
	}
	sort.Float64s(p)
	return p[len(p)/2]
}

// Align aligns donor onto target; both are the anchor track at SampleRate.
func Align(donor, target []float32) (Result, error) {
	d, t := extract(donor), extract(target)
	if d.frames() < winFrames || t.frames() < winFrames {
		return Result{}, errors.New("audioalign: both recordings must be longer than one 20 s window")
	}
	tg := newTarget(t)

	// Rate: the candidate whose coarse windows peak highest.
	type scored struct {
		c rateCandidate
		p float64
	}
	var sc []scored
	for _, c := range candidates {
		sc = append(sc, scored{c, medianPeak(tg.windows(d.stretch(c.r), coarseStride))})
	}
	sort.Slice(sc, func(i, j int) bool { return sc[i].p > sc[j].p })
	best := sc[0].c
	// The margin is over the best candidate of another family: 25/24 and
	// 25/23.976 are 0.1% apart and nearly tie on peaks, and the drift
	// refinement below, not the margin, tells them apart.
	margin := math.Inf(1)
	for _, o := range sc[1:] {
		if math.Abs(o.c.r/best.r-1) > 0.01 {
			if o.p > 0 {
				margin = sc[0].p / o.p
			}
			break
		}
	}

	ws := tg.windows(d.stretch(best.r), 1)
	// Refinement: a lag that drifts across the longest run means the rate
	// is a little off; a neighbouring candidate the drift points at wins.
	if slope, ok := driftSlope(ws); ok {
		refined := best.r * (1 + slope)
		if c := nearest(refined); c.name != best.name {
			best = c
			ws = tg.windows(d.stretch(best.r), 1)
		}
	}

	r := Result{Rate: best.r, RateName: best.name, RateMargin: margin, Windows: len(ws)}
	runs := segmentRuns(ws)
	ds := d.stretch(best.r)
	r.Segments = placeSegments(runs, ds, t)
	covered := 0
	for _, run := range runs {
		covered += len(run)
	}
	for _, w := range ws {
		if w.confident {
			r.Confident++
		}
	}
	r.Coverage = math.Min(1, float64(covered*stepFrames)/float64(t.frames()))
	return r, nil
}

func nearest(rate float64) rateCandidate {
	best := candidates[0]
	for _, c := range candidates {
		if math.Abs(c.r-rate) < math.Abs(best.r-rate) {
			best = c
		}
	}
	return best
}

// driftSlope fits lag against position over the longest run of confident
// windows whose lags step smoothly (each within 0.5 s of the previous).
func driftSlope(ws []window) (float64, bool) {
	var bestRun, run []window
	for _, w := range ws {
		if !w.confident {
			continue
		}
		if len(run) > 0 && abs(w.lag-run[len(run)-1].lag) > 25 {
			if len(run) > len(bestRun) {
				bestRun = run
			}
			run = nil
		}
		run = append(run, w)
	}
	if len(run) > len(bestRun) {
		bestRun = run
	}
	if len(bestRun) < 4 {
		return 0, false
	}
	var sx, sy, sxx, sxy float64
	n := float64(len(bestRun))
	for _, w := range bestRun {
		x, y := float64(w.start), float64(w.lag)
		sx, sy, sxx, sxy = sx+x, sy+y, sxx+x*x, sxy+x*y
	}
	den := n*sxx - sx*sx
	if den == 0 {
		return 0, false
	}
	return (n*sxy - sx*sy) / den, true
}

// segmentRuns groups confident windows into runs of one offset. A run of a
// single window is noise and dropped; non-confident windows between two
// windows of one offset do not split a run.
func segmentRuns(ws []window) [][]window {
	var runs [][]window
	var cur []window
	for _, w := range ws {
		if !w.confident {
			continue
		}
		if len(cur) > 0 && abs(w.lag-cur[len(cur)-1].lag) > agreeFrames {
			runs = append(runs, cur)
			cur = nil
		}
		cur = append(cur, w)
	}
	if len(cur) > 0 {
		runs = append(runs, cur)
	}
	var out [][]window
	for _, r := range runs {
		if len(r) >= 2 {
			// merge into the previous run when it has the same offset
			if len(out) > 0 && abs(out[len(out)-1][0].lag-r[0].lag) <= agreeFrames {
				out[len(out)-1] = append(out[len(out)-1], r...)
				continue
			}
			out = append(out, r)
		}
	}
	return out
}

// placeSegments turns runs into segments, finding each boundary between
// two runs with 5 s probes every second scored at both offsets.
func placeSegments(runs [][]window, d, t features) []Segment {
	if len(runs) == 0 {
		return nil
	}
	lagOf := func(run []window) int {
		lags := make([]int, len(run))
		for i, w := range run {
			lags[i] = w.lag
		}
		sort.Ints(lags)
		return lags[len(lags)/2]
	}
	bounds := []int{0}
	for i := 1; i < len(runs); i++ {
		a, b := runs[i-1], runs[i]
		lo, hi := a[len(a)-1].start+winFrames/2, b[0].start+winFrames/2
		bounds = append(bounds, boundary(d, t, lo, hi, lagOf(a), lagOf(b)))
	}
	bounds = append(bounds, d.frames())
	var segs []Segment
	for i, run := range runs {
		lag := lagOf(run)
		ds, de := bounds[i], bounds[i+1]
		if ds+lag < 0 {
			ds = -lag
		}
		if de+lag > t.frames() {
			de = t.frames() - lag
		}
		if de <= ds {
			continue
		}
		segs = append(segs, Segment{
			DonorStart:  frames(ds),
			TargetStart: frames(ds + lag),
			Length:      frames(de - ds),
		})
	}
	return segs
}

// boundary is the donor frame in [lo, hi) where offset lagB starts scoring
// better than lagA.
func boundary(d, t features, lo, hi, lagA, lagB int) int {
	const probe, step = 250, 50
	score := func(start, lag int) float64 {
		var s float64
		for b := range d {
			for i := 0; i < probe; i++ {
				di, ti := start+i, start+i+lag
				if di < 0 || ti < 0 || di >= len(d[b]) || ti >= len(t[b]) {
					continue
				}
				s += d[b][di] * t[b][ti]
			}
		}
		return s
	}
	best, bestGain := (lo+hi)/2, math.Inf(-1)
	// The boundary maximises how much better A fits before it and B after.
	var diffs []float64
	var starts []int
	for st := lo; st+probe <= hi; st += step {
		diffs = append(diffs, score(st, lagB)-score(st, lagA))
		starts = append(starts, st)
	}
	for i := range starts {
		var gain float64
		for j := range diffs {
			if j < i {
				gain -= diffs[j]
			} else {
				gain += diffs[j]
			}
		}
		if gain > bestGain {
			best, bestGain = starts[i]+probe/2, gain
		}
	}
	return best
}

func frames(n int) time.Duration {
	return time.Duration(float64(n) * frameSeconds * float64(time.Second))
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func (r Result) String() string {
	return fmt.Sprintf("rate %s margin %.2f coverage %.2f segments %d", r.RateName, r.RateMargin, r.Coverage, len(r.Segments))
}
