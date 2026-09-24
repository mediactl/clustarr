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

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/naming"
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

func TestDestinationPath(t *testing.T) {
	eng := naming.NewEngine(naming.Config{Dialect: naming.DialectJellyfin, ColonReplacement: naming.ColonSmart})
	nctx := naming.Context{Kind: commonv1.MediaKindMovie, Title: "The Matrix", Year: 1999, TmdbID: "603"}

	dest, err := destinationPath("/data/media/movies", nil, "/data/torrents/x/The.Matrix.1999.mkv", eng, nctx)
	require.NoError(t, err)
	require.Contains(t, dest, "/data/media/movies/")
	require.Contains(t, dest, "The Matrix (1999)")
	require.True(t, len(dest) > 4 && dest[len(dest)-4:] == ".mkv", "the source extension must be preserved: %s", dest)

	override := "Custom Folder"
	dest2, err := destinationPath("/data/media/movies", &override, "/data/torrents/x/f.mkv", eng, nctx)
	require.NoError(t, err)
	require.Contains(t, dest2, "/data/media/movies/Custom Folder/")
}
