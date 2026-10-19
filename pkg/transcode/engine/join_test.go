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

package engine

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A movie released in parts -- 28 Up as Part 1 and Part 2 of one Blu-ray
// encode (2026-10-16) -- joins into one file, every stream copied: both
// parts' frames, their length end to end, decode times moving forward
// across the seam.
func TestThePartsOfOneEncodeJoinIntoOneFile(t *testing.T) {
	dir := t.TempDir()
	parts := []string{filepath.Join(dir, "Film.Part1.mkv"), filepath.Join(dir, "Film's Part2.mkv")}
	for _, p := range parts {
		src := hevcClip(t, "mkv")
		b, err := os.ReadFile(src)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(p, b, 0o644))
	}
	out := filepath.Join(dir, "Film (2020).mkv")
	_, err := Join(context.Background(), parts, out)
	require.NoError(t, err)

	assert.Equal(t, 2*frames(t, parts[0]), frames(t, out), "both parts' frames")
	d, err := strconv.ParseFloat(strings.TrimSpace(run(t, "ffprobe", "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", out)), 64)
	require.NoError(t, err)
	assert.InDelta(t, 4.0, d, 0.2, "the parts' length end to end")
	assert.True(t, increasing(packetDTS(t, out)), "decode times move forward across the seam")
	assert.Equal(t, "hevc\naac", strings.TrimSpace(run(t, "ffprobe", "-v", "error", "-show_entries", "stream=codec_name", "-of", "csv=p=0", out)), "every stream copied")
}

func TestJoinWantsTwoParts(t *testing.T) {
	_, err := Join(context.Background(), []string{"one.mkv"}, filepath.Join(t.TempDir(), "o.mkv"))
	assert.Error(t, err)
}

// A join that cannot read a part to its end fails rather than write the
// parts it could: on 2026-10-17 a second execute of 49 Up's import joined
// part 1, found part 2 deleted under it, and placed the half film as done
// (FFmpeg's concat demuxer ends quietly at a part it cannot open).
func TestAJoinThatStopsShortOfAPartFails(t *testing.T) {
	dir := t.TempDir()
	parts := []string{filepath.Join(dir, "Film.Part1.mkv"), filepath.Join(dir, "Film.Part2.mkv")}
	for _, p := range parts {
		b, err := os.ReadFile(hevcClip(t, "mkv"))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(p, b, 0o644))
	}
	st, err := os.Stat(parts[1])
	require.NoError(t, err)
	require.NoError(t, os.Truncate(parts[1], st.Size()*3/10), "part 2 ends early; its header still says its length")
	_, err = Join(context.Background(), parts, filepath.Join(dir, "Film (2020).mkv"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parts")

	_, err = Join(context.Background(), []string{parts[0], filepath.Join(dir, "gone.mkv")}, filepath.Join(dir, "o.mkv"))
	assert.Error(t, err, "a part that is not there")
}
