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

// A .sup the MP4 standard writes beside a transcode (an uncovered PGS
// track, standard.Version 8) is a sidecar of its file -- renamed, recycled
// and deleted with it -- and an image one, which no player reads.
func TestASupSidecarBelongsToItsFileAndIsAnImage(t *testing.T) {
	key, ok := subtitles.ParseSidecar("Movie (1999)", "Movie (1999).en.sup")
	require.True(t, ok)
	assert.Equal(t, subtitles.LangKey("en"), key)
	key, ok = subtitles.ParseSidecar("Movie (1999)", "Movie (1999).en.forced.sup")
	require.True(t, ok)
	assert.Equal(t, subtitles.FormatLangKey("en", true, false), key)
	assert.True(t, subtitles.ImageSidecar("Movie (1999).en.sup"))
	assert.True(t, subtitles.ImageSidecar("Movie (1999).en.SUP"))
	assert.False(t, subtitles.ImageSidecar("Movie (1999).en.srt"))

	dir := t.TempDir()
	for _, n := range []string{"Movie (1999).mp4", "Movie (1999).en.sup", "Movie (1999).en.srt", "Other.en.sup"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, n), nil, 0o644))
	}
	found, err := subtitles.SidecarsOf(filepath.Join(dir, "Movie (1999).mp4"))
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{filepath.Join(dir, "Movie (1999).en.sup"), filepath.Join(dir, "Movie (1999).en.srt")}, found,
		"the .sup moves with its file, as the .srt does")
}
