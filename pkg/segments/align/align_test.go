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

package align_test

import (
	"math/rand/v2"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/segments/align"
	"github.com/mediactl/clustarr/pkg/segments/chromaprint"
)

// pts converts seconds to fingerprint points.
func pts(s float64) int { return int(s / chromaprint.ItemSeconds) }

func noise(r *rand.Rand, n int) []uint32 {
	out := make([]uint32, n)
	for i := range out {
		out[i] = r.Uint32()
	}
	return out
}

// plant copies intro into stream at atS seconds, flipping up to flips random
// bits per point, as a re-encode of the same audio does.
func plant(r *rand.Rand, stream, intro []uint32, atS float64, flips int) {
	at := pts(atS)
	for i, v := range intro {
		for range r.IntN(flips + 1) {
			v ^= 1 << r.IntN(32)
		}
		stream[at+i] = v
	}
}

// episode is a 10-minute window with the intro at atS (or none when atS<0).
func episode(r *rand.Rand, intro []uint32, atS float64) []uint32 {
	s := noise(r, pts(600))
	if atS >= 0 {
		plant(r, s, intro, atS, 3)
	}
	return s
}

func near(t *testing.T, want, got float64, msg string) {
	t.Helper()
	assert.InDelta(t, want, got, 2*chromaprint.ItemSeconds, msg)
}

func TestSharedFindsAPlantedIntro(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	intro := noise(r, pts(60))
	a, b := episode(r, intro, 30), episode(r, intro, 90)
	ra, rb, ok := align.Shared(a, b, align.IntroParams)
	require.True(t, ok)
	near(t, 30, ra.StartS, "start in a")
	near(t, 90, ra.EndS, "end in a")
	near(t, 90, rb.StartS, "start in b")
	near(t, 150, rb.EndS, "end in b")
}

// A stretch of the shared audio that does not match (a sound effect over
// the theme) shorter than MaxGapS does not split the intro.
func TestSharedBridgesShortGaps(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	intro := noise(r, pts(60))
	a, b := episode(r, intro, 20), episode(r, intro, 20)
	copy(b[pts(40):pts(42)], noise(r, pts(2)))
	ra, _, ok := align.Shared(a, b, align.IntroParams)
	require.True(t, ok)
	near(t, 20, ra.StartS, "start")
	near(t, 80, ra.EndS, "one region across the gap")
}

func TestSharedRejectsTooShortAndTooLong(t *testing.T) {
	r := rand.New(rand.NewPCG(5, 6))
	short := noise(r, pts(10))
	_, _, ok := align.Shared(episode(r, short, 30), episode(r, short, 50), align.IntroParams)
	assert.False(t, ok, "10 s is under the 15 s minimum")
	long := noise(r, pts(200))
	_, _, ok = align.Shared(episode(r, long, 30), episode(r, long, 50), align.IntroParams)
	assert.False(t, ok, "200 s is over the 120 s maximum")
}

func TestSeasonNeedsTwoFiles(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 8))
	intro := noise(r, pts(45))
	assert.Equal(t, []*align.Region{nil}, align.Season([][]uint32{episode(r, intro, 10)}, align.IntroParams))
}

// Two episodes have one comparison each, so one comparison decides there:
// a season of two files has intros (spec: only a single file has none).
func TestSeasonOfTwoHasIntros(t *testing.T) {
	r := rand.New(rand.NewPCG(9, 10))
	intro := noise(r, pts(45))
	got := align.Season([][]uint32{episode(r, intro, 10), episode(r, intro, 70)}, align.IntroParams)
	require.NotNil(t, got[0])
	require.NotNil(t, got[1])
	near(t, 10, got[0].StartS, "first")
	near(t, 70, got[1].StartS, "second")
}

func TestSeasonSurvivesAnEpisodeWithoutTheIntro(t *testing.T) {
	r := rand.New(rand.NewPCG(11, 12))
	intro := noise(r, pts(50))
	fps := [][]uint32{episode(r, intro, 5), episode(r, intro, 5), episode(r, intro, -1), episode(r, intro, 5), episode(r, intro, 5)}
	got := align.Season(fps, align.IntroParams)
	assert.Nil(t, got[2], "the episode without the intro")
	for _, i := range []int{0, 1, 3, 4} {
		require.NotNil(t, got[i], "episode %d", i)
		near(t, 5, got[i].StartS, "start")
		near(t, 55, got[i].EndS, "end")
	}
}

// A cold open moves the intro: each episode's own offset is found.
func TestSeasonColdOpen(t *testing.T) {
	r := rand.New(rand.NewPCG(13, 14))
	intro := noise(r, pts(40))
	at := []float64{0, 95, 140, 30}
	fps := make([][]uint32, len(at))
	for i, s := range at {
		fps[i] = episode(r, intro, s)
	}
	got := align.Season(fps, align.IntroParams)
	for i, s := range at {
		require.NotNil(t, got[i], "episode %d", i)
		near(t, s, got[i].StartS, "start")
	}
}

func TestSeasonToleratesAMissingFingerprint(t *testing.T) {
	r := rand.New(rand.NewPCG(15, 16))
	intro := noise(r, pts(40))
	got := align.Season([][]uint32{episode(r, intro, 10), nil, episode(r, intro, 10), episode(r, intro, 10)}, align.IntroParams)
	assert.Nil(t, got[1])
	assert.NotNil(t, got[0])
}
