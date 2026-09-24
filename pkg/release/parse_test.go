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

package release_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/release"
)

func TestParseAutoDetectsKindWhenOptionsKindIsEmpty(t *testing.T) {
	tests := []struct {
		name  string
		title string
		kind  commonv1.MediaKind
	}{
		{"movie", "The.Matrix.1999.1080p.BluRay.x264-GROUP", commonv1.MediaKindMovie},
		{"episode", "Severance.S02E03.1080p.ATVP.WEB-DL.DDP5.1.Atmos.H.264-NTb", commonv1.MediaKindEpisode},
		{"music", "Pink Floyd - The Dark Side of the Moon (1973) [FLAC]", commonv1.MediaKindAlbum},
		{"comic", "Saga 001 (2012) (Digital) (Zone-Empire).cbz", commonv1.MediaKindComic},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := release.Parse(tt.title, release.Options{})
			require.NoError(t, err)
			require.NotNil(t, p)
			switch tt.kind {
			case commonv1.MediaKindAlbum:
				assert.NotNil(t, p.Music)
			case commonv1.MediaKindComic:
				assert.NotNil(t, p.Comic)
			}
		})
	}
}

func TestParsePathStripsDirectoryAndExtension(t *testing.T) {
	p, err := release.ParsePath("/data/media/movies/The.Matrix.1999.1080p.BluRay.x264-GROUP/The.Matrix.1999.1080p.BluRay.x264-GROUP.mkv", release.Options{})
	require.NoError(t, err)
	assert.Equal(t, "The Matrix", p.Title)
	assert.Equal(t, 1999, p.Year)
}

// TestParsePathKeepsComicFormat pins that a comic file's format survives
// ParsePath: the extension is the format, and stripping it (as every other
// kind wants) left Quality "Unknown" and ComicInfo.Format empty for any file
// whose name did not also carry a "[CBZ]" token.
func TestParsePathKeepsComicFormat(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		kind   commonv1.MediaKind
		format string
	}{
		{"issue cbz", "/data/comics/Saga (2012)/Saga 001 (2012) (Digital) (Zone-Empire).cbz", commonv1.MediaKindIssue, "CBZ"},
		{"comic cbr", "/data/comics/Saga (2012)/Saga 002 (2012) (Digital) (Zone-Empire).cbr", commonv1.MediaKindComic, "CBR"},
		{"manga pdf", "/data/comics/One Piece/One Piece v107 c1088 (2023).pdf", commonv1.MediaKindComic, "PDF"},
		{"classified cbz", "/data/comics/Saga (2012)/Saga 001 (2012) (Digital) (Zone-Empire).cbz", "", "CBZ"},
		{"classified manga pdf", "/data/comics/One Piece/One Piece v107c1088 (2023).pdf", "", "PDF"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := release.ParsePath(tt.path, release.Options{Kind: tt.kind})
			require.NoError(t, err)
			require.NotNil(t, p.Comic)
			assert.Equal(t, tt.format, p.Comic.Format)
			assert.Equal(t, tt.format, p.Quality.Name)
		})
	}
}

// TestParsePathStripsANonComicExtension guards the other side: a movie or a
// book file still loses its extension, so ".mkv" never reaches the group
// parser and an ebook's ".pdf" is not read as a comic.
func TestParsePathStripsANonComicExtension(t *testing.T) {
	p, err := release.ParsePath("/data/movies/The.Matrix.1999.1080p.BluRay.x264-GROUP.mkv", release.Options{Kind: commonv1.MediaKindMovie})
	require.NoError(t, err)
	assert.Equal(t, "GROUP", p.Group)

	b, err := release.ParsePath("/data/books/Andy Weir - Project Hail Mary (2021) [EPUB].pdf", release.Options{Kind: commonv1.MediaKindBook})
	require.NoError(t, err)
	assert.Nil(t, b.Comic)
	assert.Equal(t, "EPUB", b.Quality.Name)
}

func TestParseRejectsEmptyTitle(t *testing.T) {
	_, err := release.Parse("", release.Options{})
	assert.Error(t, err)
}

// TestParsePathExtractsEmbeddedProviderIDs covers what the library scanner
// actually reads from a Jellyfin/Plex/*arr-style folder name: an embedded
// [tmdbid-N] token in both the parent folder and the file name.
func TestParsePathExtractsEmbeddedProviderIDs(t *testing.T) {
	p, err := release.ParsePath(
		"/data/media/movies/Heat (1995) [tmdbid-949]/Heat (1995) [tmdbid-949] - 1080p.mkv",
		release.Options{},
	)
	require.NoError(t, err)
	assert.Equal(t, "Heat", p.Title)
	assert.Equal(t, 1995, p.Year)
	assert.Equal(t, map[string]string{"tmdb": "949"}, p.IDs)
}

// TestParseKindExtractsIDsAndTrimsThemForNonMovieKinds verifies the
// controller ruling that extractIDs is a shared pre-dispatch step in
// Parse, not something only parseMovie benefits from: a series title
// carrying an embedded tvdbid token must have it both recorded in IDs and
// stripped from the parsed Title/Seasons/Episodes, the same as a movie.
func TestParseKindExtractsIDsAndTrimsThemForNonMovieKinds(t *testing.T) {
	p, err := release.ParseKind(
		"Breaking Bad (2008) [tvdbid-81189] - S01E01 - Pilot [1080p]",
		commonv1.MediaKindEpisode,
	)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"tvdb": "81189"}, p.IDs)
	assert.Equal(t, "Breaking Bad", p.Title)
	assert.Equal(t, []int{1}, p.Seasons)
	assert.Equal(t, []int{1}, p.Episodes)
}

// TestParseKindSplitsSlashAlternateTitleForAnimeKind verifies the " / "
// alternate-title split (buildTitles, titles.go) also applies to a kind
// other than movie, now that it runs in Parse's shared pre-dispatch step.
func TestParseKindSplitsSlashAlternateTitleForAnimeKind(t *testing.T) {
	p, err := release.ParseKind(
		"[SubsPlease] Kimetsu no Yaiba / Demon Slayer - 12 (1080p) [HASH].mkv",
		commonv1.MediaKindEpisode,
	)
	require.NoError(t, err)
	assert.Equal(t, "Kimetsu no Yaiba", p.Title)
	assert.Equal(t, []string{"Kimetsu no Yaiba", "Demon Slayer"}, p.Titles)
}

// TestParsePathExtractsProviderIDFromParentFolderForSeries is the exact
// scenario the controller ruling names: a library scanner reading a
// Jellyfin/Plex/*arr-style TV layout, where the provider id lives in the
// show folder (two levels above the episode file), not the episode
// filename itself.
func TestParsePathExtractsProviderIDFromParentFolderForSeries(t *testing.T) {
	p, err := release.ParsePath(
		"/data/media/tv/Breaking Bad (2008) [tvdbid-81189]/Season 01/Breaking Bad (2008) - S01E01 - Pilot [1080p].mkv",
		release.Options{Kind: commonv1.MediaKindSeries},
	)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"tvdb": "81189"}, p.IDs)
	assert.Equal(t, "Breaking Bad", p.Title)
	assert.Equal(t, []int{1}, p.Seasons)
	assert.Equal(t, []int{1}, p.Episodes)
}

// TestParseTitlesAlwaysHasTitleFirstAcrossEveryKind verifies the package-
// wide invariant that ParsedRelease.Titles always starts with Title, even
// for kinds (series, album, comic, ...) whose own parser doesn't build a
// multi-entry Titles list itself — Parse's dispatch (parse.go) backfills
// Titles = [Title] for any kind that left it empty.
func TestParseTitlesAlwaysHasTitleFirstAcrossEveryKind(t *testing.T) {
	tests := []struct {
		name  string
		title string
		kind  commonv1.MediaKind
	}{
		{"episode", "Severance.S02E03.1080p.ATVP.WEB-DL.DDP5.1.Atmos.H.264-NTb", commonv1.MediaKindEpisode},
		{"album", "Pink Floyd - The Dark Side of the Moon (1973) [FLAC]", commonv1.MediaKindAlbum},
		{"comic", "Saga 001 (2012) (Digital) (Zone-Empire).cbz", commonv1.MediaKindComic},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := release.ParseKind(tt.title, tt.kind)
			require.NoError(t, err)
			require.NotEmpty(t, p.Titles)
			assert.Equal(t, p.Title, p.Titles[0])
		})
	}
}

func TestParseNeverPanicsOnMalformedInput(t *testing.T) {
	inputs := []string{
		"", " ", ".", "-", "[", "]", "S01E", "1999", strings.Repeat("a", 5000),
		"€™š™š™š.1999.1080p", "\x00\x01\x02", "S99E99999999999999999999999999",
	}
	for _, in := range inputs {
		in := in
		t.Run(in, func(t *testing.T) {
			assert.NotPanics(t, func() {
				_, _ = release.Parse(in, release.Options{})
			})
		})
	}
}

func TestParseKindWithUnknownKindReturnsError(t *testing.T) {
	_, err := release.ParseKind("Some.Title.2020.1080p.BluRay.x264-GROUP", commonv1.MediaKind("bogus"))
	assert.Error(t, err)
}

// TestParseBracedIDTokensDoNotHijackClassification is the carried defect
// "any {...} token classifies a title as an audiobook": both titles used to
// classify as audiobook, fail the book patterns, and ship with no parsed
// fields at all. The bracket form ([tmdbid-603]) never had the problem, so
// the brace form must now parse identically to it.
func TestParseBracedIDTokensDoNotHijackClassification(t *testing.T) {
	t.Run("episode", func(t *testing.T) {
		p, err := release.Parse("Some Show S01E01 {tvdbid-121361}", release.Options{})
		require.NoError(t, err)
		assert.Equal(t, "Some Show", p.Title)
		assert.Equal(t, []int{1}, p.Seasons)
		assert.Equal(t, []int{1}, p.Episodes)
		assert.Equal(t, map[string]string{"tvdb": "121361"}, p.IDs)
	})
	t.Run("movie", func(t *testing.T) {
		p, err := release.Parse("{imdbid-tt0133093} The Matrix 1999 1080p", release.Options{})
		require.NoError(t, err)
		assert.Equal(t, "The Matrix", p.Title)
		assert.Equal(t, 1999, p.Year)
		assert.Equal(t, map[string]string{"imdb": "tt0133093"}, p.IDs)
	})
	t.Run("a real narrator brace is still an audiobook", func(t *testing.T) {
		p, err := release.Parse("Project Hail Mary - Andy Weir {Ray Porter} [ASIN B08G9PRS1K] [M4B]", release.Options{})
		require.NoError(t, err)
		require.NotNil(t, p.Book)
		assert.Equal(t, "Ray Porter", p.Book.Narrator)
	})
}

// TestParsePathReleaseGroupOnLibraryLayouts is the corpus importarr's
// rescan guard (app/import/worker/rescan/releasegroup.go, deleted with this
// fix) carried while pkg/release mis-read the group on *arr's own renamed
// files. MediaFileSpec.ReleaseGroup is frozen at import, so these are the
// values that become permanent.
func TestParsePathReleaseGroupOnLibraryLayouts(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"/data/media/movies/Heat (1995) [tmdbid-949]/Heat (1995) [tmdbid-949] - Bluray-1080p.mkv", ""},
		{"/data/media/movies/Heat (1995)/Heat.1995.Bluray-1080p-RlsGrp.mkv", "RlsGrp"},
		{"/data/media/movies/Heat (1995)/Heat (1995) WEBDL-720p.mkv", ""},
		{"/data/media/movies/Heat (1995)/Heat.1995.1080p.BluRay.x264-SPARKS.mkv", "SPARKS"},
		{"/data/media/movies/Heat (1995)/Heat.1995.2160p.UHD.BluRay.x265-TERMiNAL.mkv", "TERMiNAL"},
		{"/data/media/movies/Heat (1995)/Heat.1995.1080p.WEB-DL.DDP5.1-NTb.mkv", "NTb"},
		{"/data/media/movies/Heat (1995)/Heat.1995.DVDRip.XviD-FraMeSToR.mkv", "FraMeSToR"},
		{"/data/media/movies/Heat (1995) [tmdbid-949]/Heat (1995) [tmdbid-949].mkv", ""},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			p, err := release.ParsePath(tt.path, release.Options{Kind: commonv1.MediaKindMovie})
			require.NoError(t, err)
			assert.Equal(t, tt.want, p.Group)
		})
	}
}
