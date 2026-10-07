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

package fileimport

import (
	"testing"

	"github.com/stretchr/testify/require"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/naming/catalogctx"
	"github.com/mediactl/clustarr/pkg/quality"
)

func TestRelPath(t *testing.T) {
	require.Equal(t, "movie.mkv", relPath("/data/torrents/x", "/data/torrents/x/movie.mkv"))
	require.Equal(t, "sub/movie.mkv", relPath("/data/torrents/x", "/data/torrents/x/sub/movie.mkv"))
	// Unrelated: falls back to the absolute path rather than a misleading "..".
	require.Equal(t, "/elsewhere/movie.mkv", relPath("/data/torrents/x", "/elsewhere/movie.mkv"))
	require.Equal(t, "/a/b", relPath("", "/a/b"), "an empty root is passed through verbatim")
}

func TestCapMatchedFormats(t *testing.T) {
	short := []string{"a", "b"}
	require.Equal(t, short, capMatchedFormats(short))

	long := make([]string, 250)
	for i := range long {
		long[i] = "f"
	}
	got := capMatchedFormats(long)
	require.Len(t, got, 200)
}

func TestVerdictMessage(t *testing.T) {
	// Every non-Upgrade verdict must render a non-empty, distinct message; a
	// blank or identical rejection string across verdicts would leave an
	// operator unable to tell two different rejections apart.
	verdicts := []quality.Verdict{
		quality.ExistingBetterQuality, quality.UpgradesNotAllowed, quality.ExistingBetterRevision,
		quality.QualityCutoffMet, quality.FormatScoreNotHigher, quality.FormatCutoffMet,
		quality.FormatIncrementTooSmall,
	}
	seen := map[string]bool{}
	for _, v := range verdicts {
		msg := verdictMessage(v)
		require.NotEmpty(t, msg)
		require.False(t, seen[msg], "verdict %d reused message %q", v, msg)
		seen[msg] = true
	}
}

func TestDedupKey(t *testing.T) {
	require.Equal(t, "import.abc123", DedupKey("abc123"))
	// Distinct inputs must never collapse onto the same key -- KVKeyToken's
	// own injectivity contract, exercised through this package's use of it.
	require.NotEqual(t, DedupKey("a:b"), DedupKey("a,b"))
}

func TestTargetKey(t *testing.T) {
	require.Equal(t, "movie/the-matrix", targetKey("movie", "the-matrix"))
	require.NotEqual(t, targetKey("movie", "x"), targetKey("episode", "x"),
		"the same name under a different kind must not collide")
}

// TestMovieFilePathHonoursTheFolderOverride is a thin integration check
// that this package wires catalogv1alpha1's RootFolder and Movie into
// catalogctx.MovieFilePath correctly; catalogctx's own tests cover the
// rendering itself.
func TestMovieFilePathHonoursTheFolderOverride(t *testing.T) {
	root := &catalogv1alpha1.RootFolder{Spec: catalogv1alpha1.RootFolderSpec{
		Path: "/data/media/movies",
		Naming: catalogv1alpha1.NamingSpec{
			Dialect: catalogv1alpha1.NamingDialectJellyfin, ColonReplacement: catalogv1alpha1.ColonReplacementSmart,
		},
	}}
	movie := &catalogv1alpha1.Movie{
		Spec:   catalogv1alpha1.MovieSpec{TmdbID: 603},
		Status: catalogv1alpha1.MovieStatus{Metadata: &catalogv1alpha1.MovieMetadata{Title: "The Matrix", Year: 1999}},
	}
	c, ok := catalogctx.Movie(movie)
	require.True(t, ok)

	ext := catalogctx.ContainerExt(nil, "/data/torrents/x/The.Matrix.1999.mkv")
	dest, err := catalogctx.MovieFilePath(root, movie, c, ext)
	require.NoError(t, err)
	require.Contains(t, dest, "/data/media/movies/")
	require.Contains(t, dest, "The Matrix (1999)")
	require.True(t, len(dest) > 4 && dest[len(dest)-4:] == ".mkv", "the source extension must be preserved: %s", dest)

	override := "Custom Folder"
	movie.Spec.Folder = &override
	dest2, err := catalogctx.MovieFilePath(root, movie, c, ext)
	require.NoError(t, err)
	require.Contains(t, dest2, "/data/media/movies/Custom Folder/")
}
