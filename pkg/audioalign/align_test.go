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

package audioalign_test

import (
	"math"
	"math/rand/v2"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/audioalign"
)

const sr = audioalign.SampleRate

// soundtrack is a synthetic score: random decaying tone bursts, about four
// a second between 200 and 3,000 Hz, over low noise, from a fixed seed.
func soundtrack(seed uint64, seconds float64) []float32 {
	r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	n := int(seconds * sr)
	x := make([]float32, n)
	for i := range x {
		x[i] = float32(r.NormFloat64() * 0.002)
	}
	for t := 0.0; t < seconds; t += r.ExpFloat64() / 4 {
		f := 200 + r.Float64()*2800
		d := 0.05 + r.Float64()*0.25
		a := 0.2 + r.Float64()*0.6
		start := int(t * sr)
		for k := 0; k < int(d*sr) && start+k < n; k++ {
			tt := float64(k) / sr
			x[start+k] += float32(a * math.Exp(-tt*12) * math.Sin(2*math.Pi*f*tt))
		}
	}
	return x
}

// resample plays x faster by rate (rate > 1 shortens it), as a PAL speed-up does.
func resample(x []float32, rate float64) []float32 {
	n := int(float64(len(x)) / rate)
	out := make([]float32, n)
	for i := range out {
		src := float64(i) * rate
		j := int(src)
		if j+1 >= len(x) {
			break
		}
		f := float32(src - float64(j))
		out[i] = x[j]*(1-f) + x[j+1]*f
	}
	return out
}

func delay(x []float32, d time.Duration) []float32 {
	return append(make([]float32, int(d.Seconds()*sr)), x...)
}

func cut(x []float32, at, length time.Duration) []float32 {
	a, b := int(at.Seconds()*sr), int((at+length).Seconds()*sr)
	return append(append([]float32(nil), x[:a]...), x[b:]...)
}

func insert(x []float32, at time.Duration, extra []float32) []float32 {
	a := int(at.Seconds() * sr)
	return append(append(append([]float32(nil), x[:a]...), extra...), x[a:]...)
}

func offsetOf(s audioalign.Segment) time.Duration { return s.TargetStart - s.DonorStart }

func near(t *testing.T, want, got time.Duration, tol time.Duration, msg string) {
	t.Helper()
	require.InDelta(t, want.Seconds(), got.Seconds(), tol.Seconds(), msg)
}

func TestAlignIdentity(t *testing.T) {
	x := soundtrack(1, 600)
	r, err := audioalign.Align(x, x)
	require.NoError(t, err)
	require.Equal(t, "1", r.RateName)
	require.Len(t, r.Segments, 1)
	near(t, 0, offsetOf(r.Segments[0]), 40*time.Millisecond, "offset")
	require.GreaterOrEqual(t, r.Coverage, 0.9)
	require.NoError(t, r.Accept(audioalign.DefaultThresholds))
}

func TestAlignPALSpeedupAndDelay(t *testing.T) {
	target := soundtrack(2, 600)
	donor := delay(resample(target, 25/23.976), 3217*time.Millisecond)
	r, err := audioalign.Align(donor, target)
	require.NoError(t, err)
	require.Equal(t, "25/23.976", r.RateName)
	require.Len(t, r.Segments, 1)
	// The donor's 3.217 s of lead-in, stretched back onto the target's timeline.
	near(t, -3217*time.Millisecond*25/24, offsetOf(r.Segments[0]), 60*time.Millisecond, "offset on the stretched timeline")
	require.NoError(t, r.Accept(audioalign.DefaultThresholds))

	med, within := audioalign.Verify(donor, target, r)
	require.LessOrEqual(t, med, 40*time.Millisecond)
	require.GreaterOrEqual(t, within, 0.9)

	// What the mux would apply, aligned again, is the target's own timing.
	moved := audioalign.Transform(donor, r, len(target))
	again, err := audioalign.Align(moved, target)
	require.NoError(t, err)
	require.Equal(t, "1", again.RateName)
	require.Len(t, again.Segments, 1)
	near(t, 0, offsetOf(again.Segments[0]), 60*time.Millisecond, "transformed donor offset")
}

func TestAlignACutInTheDonor(t *testing.T) {
	target := soundtrack(3, 600)
	donor := cut(resample(target, 25/23.976), 300*time.Second, 10*time.Second)
	r, err := audioalign.Align(donor, target)
	require.NoError(t, err)
	require.Equal(t, "25/23.976", r.RateName)
	require.Len(t, r.Segments, 2)
	jump := offsetOf(r.Segments[1]) - offsetOf(r.Segments[0])
	near(t, 10*time.Second*25/24, jump, 200*time.Millisecond, "the cut, on the stretched timeline")
	require.NoError(t, r.Accept(audioalign.DefaultThresholds))
}

func TestAlignExtraDonorMaterial(t *testing.T) {
	target := soundtrack(4, 600)
	donor := insert(target, 200*time.Second, soundtrack(99, 8))
	r, err := audioalign.Align(donor, target)
	require.NoError(t, err)
	require.Equal(t, "1", r.RateName)
	require.Len(t, r.Segments, 2)
	near(t, -8*time.Second, offsetOf(r.Segments[1])-offsetOf(r.Segments[0]), 100*time.Millisecond, "extra donor material")
}

func TestAlignTellsTwentyFiveOverTwentyFourFromPAL(t *testing.T) {
	target := soundtrack(5, 600)
	r, err := audioalign.Align(resample(target, 25.0/24), target)
	require.NoError(t, err)
	require.Equal(t, "25/24", r.RateName)
	require.Len(t, r.Segments, 1)
}

func TestAlignRefusesUnrelatedAudio(t *testing.T) {
	r, err := audioalign.Align(soundtrack(6, 600), soundtrack(7, 600))
	require.NoError(t, err)
	require.Error(t, r.Accept(audioalign.DefaultThresholds))
}

func TestAlignIgnoresSilence(t *testing.T) {
	target := soundtrack(8, 600)
	for i := 250 * sr; i < 310*sr; i++ {
		target[i] = 0
	}
	donor := delay(target, 2*time.Second)
	r, err := audioalign.Align(donor, target)
	require.NoError(t, err)
	require.Len(t, r.Segments, 1, "silence must not invent a second segment")
	require.NoError(t, r.Accept(audioalign.DefaultThresholds))
}

func TestAlignRuntime(t *testing.T) {
	if testing.Short() {
		t.Skip("runtime guard")
	}
	target := soundtrack(9, 24*60)
	donor := delay(resample(target, 25/23.976), time.Second)
	cpu0, start := cpuTime(t), time.Now()
	_, err := audioalign.Align(donor, target)
	require.NoError(t, err)
	cpu, wall := cpuTime(t)-cpu0, time.Since(start)
	t.Logf("align: wall %s, cpu %s", wall, cpu)
	// CPU time, not wall time: under make test every package runs at once
	// and the wall clock measures the machine's load (32 s against 14 s
	// alone, 2026-10-06). About 100 s of CPU alone; a graft Job's 4 cores
	// take some 25 s of it.
	require.Less(t, cpu, 240*time.Second, "a 24-minute episode must align well inside a worker's budget")
}

// TestRealPair is gate G1 (spec §10): two real releases of one episode,
// decoded to 8 kHz mono s16le. It skips unless both env vars name files.
func TestRealPair(t *testing.T) {
	dp, tp := os.Getenv("CLUSTARR_AUDIOALIGN_DONOR"), os.Getenv("CLUSTARR_AUDIOALIGN_TARGET")
	if dp == "" || tp == "" {
		t.Skip("set CLUSTARR_AUDIOALIGN_DONOR and CLUSTARR_AUDIOALIGN_TARGET to 8 kHz mono s16le files")
	}
	load := func(p string) []float32 {
		b, err := os.ReadFile(p)
		require.NoError(t, err)
		out := make([]float32, len(b)/2)
		for i := range out {
			out[i] = float32(int16(uint16(b[2*i])|uint16(b[2*i+1])<<8)) / 32768
		}
		return out
	}
	donor, target := load(dp), load(tp)
	start := time.Now()
	r, err := audioalign.Align(donor, target)
	require.NoError(t, err)
	t.Logf("align took %s", time.Since(start))
	t.Logf("rate %s (%.6f) margin %.2f coverage %.2f windows %d confident %d", r.RateName, r.Rate, r.RateMargin, r.Coverage, r.Windows, r.Confident)
	for i, s := range r.Segments {
		t.Logf("segment %d: donor %s target %s length %s offset %s", i, s.DonorStart, s.TargetStart, s.Length, offsetOf(s))
	}
	med, within := audioalign.Verify(donor, target, r)
	t.Logf("verify: median residual %s, within 80 ms %.2f", med, within)
	if os.Getenv("CLUSTARR_AUDIOALIGN_INFO_ONLY") != "" {
		return
	}
	require.NoError(t, r.Accept(audioalign.DefaultThresholds))
}

// cpuTime is the process's user plus system CPU time so far.
func cpuTime(t *testing.T) time.Duration {
	var ru syscall.Rusage
	require.NoError(t, syscall.Getrusage(syscall.RUSAGE_SELF, &ru))
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}
