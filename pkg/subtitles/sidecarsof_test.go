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

package subtitles_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/subtitles"
)

// SidecarsOf finds every subtitle beside a video that ParseSidecar
// attributes to it, recorded or not: squasharr's own (MP4 standard
// §4.1) as well as captionarr's.
func TestSidecarsOfListsEverySidecarOfTheVideo(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{
		"Show - S01E01.mp4", "Show - S01E01.en.srt", "Show - S01E01.en.forced.ass", "Show - S01E01.de.sdh.srt",
		"Show - S01E01.Extended.en.srt",        // another video's
		"Show - S01E01.en.part-ab12cd34-1.ass", // a transcode attempt's part
		"Show - S01E01.nfo", "Show - S01E02.en.srt",
	} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644))
	}
	got, err := subtitles.SidecarsOf(filepath.Join(dir, "Show - S01E01.mp4"))
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{
		filepath.Join(dir, "Show - S01E01.en.srt"),
		filepath.Join(dir, "Show - S01E01.en.forced.ass"),
		filepath.Join(dir, "Show - S01E01.de.sdh.srt"),
	}, got)
	none, err := subtitles.SidecarsOf(filepath.Join(t.TempDir(), "gone", "x.mp4"))
	require.NoError(t, err, "a missing folder has no sidecars")
	assert.Empty(t, none)
}
