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

package frames_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/segments/frames"
)

func load(t *testing.T, name string) [][]byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "test", "data", "segments", "frames", name+".gray"))
	require.NoError(t, err)
	var out [][]byte
	for i := 0; i+frames.W*frames.H <= len(b); i += frames.W * frames.H {
		out = append(out, b[i:i+frames.W*frames.H])
	}
	return out
}

func TestCreditsOnBlackAreBlack(t *testing.T) {
	for _, n := range []string{"bladerunner-credits", "her-credits"} {
		for i, s := range frames.Stats(load(t, n)) {
			assert.True(t, s.Black, "%s frame %d", n, i)
		}
	}
}

// Blade Runner's credits roll 6 rows a second at 72 rows: ten frames in a
// row at one shift.
func TestRollingCreditsScroll(t *testing.T) {
	st := frames.Stats(load(t, "bladerunner-credits"))
	n := 0
	for _, s := range st[10:] {
		if s.Scrolling {
			n++
			assert.EqualValues(t, 6, s.ScrollPx)
		}
	}
	assert.Equal(t, 10, n)
}

// A normal scene's last 20 seconds are not credits -- with nothing else to
// go on, no credits are invented over a film's final scene.
func TestNormalScenesAreNotCredits(t *testing.T) {
	for _, n := range []string{"dexter-scene", "her-scene"} {
		st := frames.Stats(load(t, n))
		for i, s := range st {
			assert.False(t, s.Scrolling, "%s frame %d scrolls", n, i)
		}
		assert.Empty(t, frames.CreditRuns(st, 0), n)
	}
}

func TestCreditsOnBlackAreAStrongRun(t *testing.T) {
	runs := frames.CreditRuns(frames.Stats(load(t, "bladerunner-credits")), 6800)
	require.Len(t, runs, 1)
	assert.Equal(t, frames.Run{StartS: 6800, EndS: 6820, Confidence: 90}, runs[0],
		"black throughout, half of it scrolling, reaching the window's end")
}

// Name cards over a picture are only text over a flat frame: a weak run,
// left to the DNN to confirm.
func TestCardsOverPictureAreAWeakRun(t *testing.T) {
	runs := frames.CreditRuns(frames.Stats(load(t, "dexter-credits")), 3100)
	require.Len(t, runs, 1)
	assert.Equal(t, frames.Run{StartS: 3100, EndS: 3120, Confidence: 60}, runs[0])
}

func TestRunsBridgeShortGapsAndNeedFifteenSeconds(t *testing.T) {
	blk, scene := frames.Stat{Black: true}, frames.Stat{EntropyMilli: 5000, EdgePermille: 400}
	seq := func(parts ...any) []frames.Stat {
		var out []frames.Stat
		for i := 0; i < len(parts); i += 2 {
			for range parts[i+1].(int) {
				out = append(out, parts[i].(frames.Stat))
			}
		}
		return out
	}
	runs := frames.CreditRuns(seq(scene, 10, blk, 8, scene, 2, blk, 8), 0)
	require.Len(t, runs, 1, "a 2 s gap is bridged")
	assert.Equal(t, 10, runs[0].StartS)
	assert.Equal(t, 28, runs[0].EndS)

	assert.Len(t, frames.CreditRuns(seq(scene, 10, blk, 8, scene, 3, blk, 8), 0), 0, "a 3 s gap splits into two short runs")
	assert.Len(t, frames.CreditRuns(seq(blk, 14), 0), 0, "14 s is too short")
}
