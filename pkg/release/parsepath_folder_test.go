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

package release

import (
	"testing"

	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

func TestParsePathFallsBackToTheItemFolder(t *testing.T) {
	p, err := ParsePath("/data/media/movies/A Scanner Darkly (2006) {tmdb-3509}/2ef6f194995e4a11b055d0f2354ef0ba.mp4", Options{Kind: commonv1.MediaKindMovie, FolderFallback: true})
	require.NoError(t, err)
	require.True(t, p.FromFolder)
	require.Equal(t, "A Scanner Darkly", p.Title)
	require.Equal(t, 2006, p.Year)
	require.Equal(t, "3509", p.IDs["tmdb"])
	require.Equal(t, commonv1.SourceUnknown, p.Quality.Source)
	require.Empty(t, p.Group)

	p, err = ParsePath("/data/media/movies/A Scanner Darkly (2006) {tmdb-3509}/A.Scanner.Darkly.2006.1080p.BluRay.x264-GRP.mkv", Options{Kind: commonv1.MediaKindMovie, FolderFallback: true})
	require.NoError(t, err)
	require.False(t, p.FromFolder, "a basename that parses is never overridden by its folder")
	require.Equal(t, "GRP", p.Group)

	_, err = ParsePath("/data/media/movies/junk/2ef6f194995e4a11b055d0f2354ef0ba.mp4", Options{Kind: commonv1.MediaKindMovie, FolderFallback: true})
	require.Error(t, err, "neither the name nor a folder names an item")

	e, err := ParsePath("/data/media/tv/Breaking Bad (2008) [tvdbid-81189]/Season 01/2ef6f194995e4a11b055d0f2354ef0ba.mkv", Options{Kind: commonv1.MediaKindEpisode, FolderFallback: true})
	require.Error(t, err, "an episode file needs its numbering from its own name; the folder cannot say which episode it is")
	_ = e

	_, err = ParsePath("/data/media/movies/A Scanner Darkly (2006) {tmdb-3509}/Featurettes/2ef6f194995e4a11b055d0f2354ef0ba.mp4", Options{Kind: commonv1.MediaKindMovie, FolderFallback: true})
	require.Error(t, err, "a file deeper than the immediate parent must not attribute to a further ancestor (R8: the scanner never guesses)")

	_, err = ParsePath("/data/media/movies/A Scanner Darkly (2006) {tmdb-3509}/2ef6f194995e4a11b055d0f2354ef0ba.mp4", Options{Kind: commonv1.MediaKindMovie})
	require.Error(t, err, "the fallback is opt-in: with FolderFallback unset, the same path that succeeds above still errors")
}
