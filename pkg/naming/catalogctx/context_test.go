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

package catalogctx_test

import (
	"os/exec"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/naming/catalogctx"
)

// TestImportAndRenameRenderTheSamePath is Task 6's failing-test-first case:
// the import worker and the (later) rename pass both build a naming.Context
// through Movie and File and render it through MovieFilePath, so both must
// land on the exact same destination path for the exact same inputs.
func TestImportAndRenameRenderTheSamePath(t *testing.T) {
	movie := &catalogv1alpha1.Movie{
		Spec: catalogv1alpha1.MovieSpec{TmdbID: 149},
		Status: catalogv1alpha1.MovieStatus{
			Metadata: &catalogv1alpha1.MovieMetadata{Title: "Akira", Year: 1988},
		},
	}
	root := &catalogv1alpha1.RootFolder{
		Spec: catalogv1alpha1.RootFolderSpec{
			Path: "/data/media/movies",
			Kind: catalogv1alpha1.RootFolderKindMovie,
			Naming: catalogv1alpha1.NamingSpec{
				Dialect: catalogv1alpha1.NamingDialectPlex,
			},
		},
	}
	spec := &catalogv1alpha1.MediaFileSpec{
		Quality:      commonv1.Quality{Source: commonv1.SourceBluray, Resolution: 1080},
		ReleaseGroup: "GRP",
		ImportedFrom: &catalogv1alpha1.ImportSource{
			ReleaseTitle: "Akira.1988.1080p.BluRay.x265-GRP",
		},
	}
	mi := &commonv1.MediaInfo{
		Container:  "matroska",
		VideoCodec: "hevc",
		Hdr:        commonv1.HdrFormatHDR10,
	}

	base, ok := catalogctx.Movie(movie)
	require.True(t, ok, "Movie must be renderable once status.metadata.title is set")

	c := catalogctx.File(base, spec, mi)
	ext := catalogctx.ContainerExt(mi, "source.mp4")
	require.Equal(t, ".mkv", ext)

	dest, err := catalogctx.MovieFilePath(root, movie, c, ext)
	require.NoError(t, err)
	require.Equal(t,
		"/data/media/movies/Akira (1988) {tmdb-149}/Akira (1988) - [Bluray-1080p] [HDR10] [x265]-GRP.mkv",
		dest)
}

func TestContainerExt(t *testing.T) {
	require.Equal(t, ".mkv", catalogctx.ContainerExt(&commonv1.MediaInfo{Container: "mkv"}, "x.mp4"))
	require.Equal(t, ".mp4", catalogctx.ContainerExt(&commonv1.MediaInfo{Container: "mp4"}, "x.mkv"))
	require.Equal(t, ".mkv", catalogctx.ContainerExt(&commonv1.MediaInfo{Container: "matroska,webm"}, "x.mp4"))
	require.Equal(t, ".mkv", catalogctx.ContainerExt(&commonv1.MediaInfo{Container: "matroska"}, "x.mp4"))
	require.Equal(t, ".mkv", catalogctx.ContainerExt(&commonv1.MediaInfo{Container: "webm"}, "x.mp4"))
	require.Equal(t, ".mp4", catalogctx.ContainerExt(&commonv1.MediaInfo{Container: "mov,mp4,m4a,3gp,3g2,mj2"}, "x.mkv"))
	require.Equal(t, ".avi", catalogctx.ContainerExt(&commonv1.MediaInfo{Container: "avi"}, "x.mkv"))
	require.Equal(t, ".mp4", catalogctx.ContainerExt(nil, "x.mp4"))
	require.Equal(t, ".ts", catalogctx.ContainerExt(&commonv1.MediaInfo{Container: "mpegts"}, "x.ts"),
		"an unrecognised container falls back to the source path's own extension")
}

// TestContainerExtReadsWhatTheProbeRecords runs the real producer: a
// literal MediaInfo{Container: "matroska"} is what ffprobe calls the
// format, but not what pkg/mediainfo.Probe writes into MediaInfo.Container,
// so only a real probe shows the mapping is reachable.
func TestContainerExtReadsWhatTheProbeRecords(t *testing.T) {
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not on PATH")
	}
	mi, _, err := mediainfo.Probe(t.Context(), "../../../test/data/mediainfo/sample_hevc_10bit.mkv")
	require.NoError(t, err)
	require.Equal(t, ".mkv", catalogctx.ContainerExt(mi, "x.mp4"), "container %q", mi.Container)
}

func TestMovieIsFalseWithoutMetadata(t *testing.T) {
	_, ok := catalogctx.Movie(&catalogv1alpha1.Movie{})
	require.False(t, ok)

	_, ok = catalogctx.Movie(&catalogv1alpha1.Movie{
		Status: catalogv1alpha1.MovieStatus{Metadata: &catalogv1alpha1.MovieMetadata{}},
	})
	require.False(t, ok, "empty title is still not renderable")
}

func TestEpisodeBuildsIdentityAndAggregatesNumbers(t *testing.T) {
	series := &catalogv1alpha1.Series{
		Spec: catalogv1alpha1.SeriesSpec{TvdbID: 81189, SeriesType: catalogv1alpha1.SeriesTypeStandard},
		Status: catalogv1alpha1.SeriesStatus{
			Metadata: &catalogv1alpha1.SeriesMetadata{Title: "Breaking Bad", Year: 2008},
		},
	}
	title1 := "Pilot"
	title2 := "Cat's in the Bag..."
	eps := []catalogv1alpha1.Episode{
		{
			Spec:   catalogv1alpha1.EpisodeSpec{SeasonNumber: 1, EpisodeNumber: 1},
			Status: catalogv1alpha1.EpisodeStatus{Title: title1},
		},
		{
			Spec:   catalogv1alpha1.EpisodeSpec{SeasonNumber: 1, EpisodeNumber: 2},
			Status: catalogv1alpha1.EpisodeStatus{Title: title2},
		},
	}

	c, ok := catalogctx.Episode(series, eps)
	require.True(t, ok)
	require.Equal(t, "Breaking Bad", c.SeriesTitle)
	require.Equal(t, 2008, c.SeriesYear)
	require.Equal(t, "81189", c.TvdbID)
	require.Equal(t, 1, c.Season)
	require.Equal(t, title1, c.EpisodeTitle, "the file name reads the first (lowest) episode's title")
	require.False(t, c.Special)
	require.Equal(t, []int{1, 2}, c.Episodes)
	require.Empty(t, c.Absolute, "a non-anime series never carries an absolute segment")

	_, ok = catalogctx.Episode(series, nil)
	require.False(t, ok, "no episodes means nothing to render")
}

func TestEpisodeAbsoluteOnlyWhenEveryEpisodeHasOne(t *testing.T) {
	series := &catalogv1alpha1.Series{
		Spec: catalogv1alpha1.SeriesSpec{TvdbID: 1, SeriesType: catalogv1alpha1.SeriesTypeAnime},
		Status: catalogv1alpha1.SeriesStatus{
			Metadata: &catalogv1alpha1.SeriesMetadata{Title: "Anime", Year: 2020},
		},
	}
	a1, a2 := int32(1), int32(2)
	eps := []catalogv1alpha1.Episode{
		{Spec: catalogv1alpha1.EpisodeSpec{SeasonNumber: 1, EpisodeNumber: 1}, Status: catalogv1alpha1.EpisodeStatus{AbsoluteNumber: &a1}},
		{Spec: catalogv1alpha1.EpisodeSpec{SeasonNumber: 1, EpisodeNumber: 2}, Status: catalogv1alpha1.EpisodeStatus{AbsoluteNumber: &a2}},
	}
	c, ok := catalogctx.Episode(series, eps)
	require.True(t, ok)
	require.Equal(t, []int{1, 2}, c.Absolute)

	// One episode of the file with no absolute number at all drops the
	// segment for the whole file, matching Sonarr.
	epsMissingOne := []catalogv1alpha1.Episode{
		eps[0],
		{Spec: catalogv1alpha1.EpisodeSpec{SeasonNumber: 1, EpisodeNumber: 2}},
	}
	c, ok = catalogctx.Episode(series, epsMissingOne)
	require.True(t, ok)
	require.Empty(t, c.Absolute)
}

func TestEpisodeFilePathUsesSeriesStatusPathWhenResolved(t *testing.T) {
	series := &catalogv1alpha1.Series{
		Spec: catalogv1alpha1.SeriesSpec{TvdbID: 81189, SeriesType: catalogv1alpha1.SeriesTypeStandard},
		Status: catalogv1alpha1.SeriesStatus{
			Metadata: &catalogv1alpha1.SeriesMetadata{Title: "Breaking Bad", Year: 2008},
			Path:     "/data/media/series/Breaking Bad",
		},
	}
	root := &catalogv1alpha1.RootFolder{
		Spec: catalogv1alpha1.RootFolderSpec{
			Path: "/data/media/series",
			Kind: catalogv1alpha1.RootFolderKindSeries,
			Naming: catalogv1alpha1.NamingSpec{
				Dialect: catalogv1alpha1.NamingDialectJellyfin,
			},
		},
	}
	eps := []catalogv1alpha1.Episode{
		{Spec: catalogv1alpha1.EpisodeSpec{SeasonNumber: 1, EpisodeNumber: 1}, Status: catalogv1alpha1.EpisodeStatus{Title: "Pilot"}},
	}
	c, ok := catalogctx.Episode(series, eps)
	require.True(t, ok)

	dest, err := catalogctx.EpisodeFilePath(root, series, c, ".mkv")
	require.NoError(t, err)
	require.Equal(t, "/data/media/series/Breaking Bad/Season 01/Breaking Bad (2008) - S01E01 - Pilot.mkv", dest)
}

func TestFileIsNilSafeOnSpecAndMediaInfo(t *testing.T) {
	movie := &catalogv1alpha1.Movie{
		Spec:   catalogv1alpha1.MovieSpec{TmdbID: 1},
		Status: catalogv1alpha1.MovieStatus{Metadata: &catalogv1alpha1.MovieMetadata{Title: "X", Year: 2000}},
	}
	base, ok := catalogctx.Movie(movie)
	require.True(t, ok)

	c := catalogctx.File(base, nil, nil)
	require.Equal(t, base, c, "a nil spec and a nil probe leave c unchanged")
}

// TestFileRendersTheNamesRadarrGaveTheOwnersLibrary: two files from the
// owner's movie library (2026-09-29), their frozen spec and probe as the
// cluster holds them, under the owner's movieFile override with TRaSH's
// audio and codec tokens. Each must render to the name Radarr gave it:
// before, the matched formats rendered as slugs ("[amzn anime-amzn]") and a
// rescanned file, which has no release title, lost "x264" for "h264".
// Radarr reads the codec's encoder off the scene name or, failing that, the
// file's own name (GetSceneOrFileName), and so does File.
func TestFileRendersTheNamesRadarrGaveTheOwnersLibrary(t *testing.T) {
	root := &catalogv1alpha1.RootFolder{Spec: catalogv1alpha1.RootFolderSpec{
		Path: "/data/media/movies", Kind: catalogv1alpha1.RootFolderKindMovie,
		Naming: catalogv1alpha1.NamingSpec{
			Dialect: catalogv1alpha1.NamingDialectPlex, ColonReplacement: catalogv1alpha1.ColonReplacementDelete,
			Overrides: map[string]string{"movieFile": "{Movie CleanTitle}{ (Release Year)} {tmdb-{TmdbId}} - " +
				"{[Custom Formats]}{[Quality Full]}{[MediaInfo AudioCodec}{ MediaInfo AudioChannels]}" +
				"{[MediaInfo VideoDynamicRangeType]}{[MediaInfo VideoCodec]}{-Release Group}"},
		},
	}}
	for _, tc := range []struct {
		title   string
		year    int32
		tmdb    int64
		spec    catalogv1alpha1.MediaFileSpec
		mi      commonv1.MediaInfo
		current string
	}{
		{
			title: "102 Minutes That Changed America", year: 2008, tmdb: 36130,
			spec: catalogv1alpha1.MediaFileSpec{
				Quality:        commonv1.Quality{Name: "WEBRip-1080p", Source: commonv1.SourceWebRip, Resolution: 1080, Modifier: commonv1.ModifierNone},
				ReleaseGroup:   "CasStudio",
				MatchedFormats: []string{"amzn", "anime-amzn"},
			},
			mi: commonv1.MediaInfo{
				Container: "mkv", VideoCodec: "h264", VideoProfile: "High",
				Audio: []commonv1.AudioStream{{Codec: "eac3", Channels: 2, Default: true}},
			},
			current: "102 Minutes That Changed America (2008) {tmdb-36130} - [AMZN][WEBRip-1080p][EAC3 2.0][x264]-CasStudio.mkv",
		},
		{
			title: "Twelve Monkeys", year: 1995, tmdb: 63,
			spec: catalogv1alpha1.MediaFileSpec{
				Quality:      commonv1.Quality{Name: "Bluray-1080p", Source: commonv1.SourceBluray, Resolution: 1080, Modifier: commonv1.ModifierNone},
				ReleaseGroup: "Skazhutin",
			},
			mi: commonv1.MediaInfo{
				Container: "mkv", VideoCodec: "h264", VideoProfile: "High",
				Audio: []commonv1.AudioStream{{Codec: "flac", Channels: 6, Default: true}},
			},
			current: "Twelve Monkeys (1995) {tmdb-63} - [Bluray-1080p][FLAC 5.1][x264]-Skazhutin.mkv",
		},
	} {
		movie := &catalogv1alpha1.Movie{
			Spec:   catalogv1alpha1.MovieSpec{TmdbID: tc.tmdb},
			Status: catalogv1alpha1.MovieStatus{Metadata: &catalogv1alpha1.MovieMetadata{Title: tc.title, Year: tc.year}},
		}
		base, ok := catalogctx.Movie(movie)
		require.True(t, ok)
		folder := "/data/media/movies/" + tc.title + " (" + strconv.Itoa(int(tc.year)) + ") {tmdb-" + strconv.FormatInt(tc.tmdb, 10) + "}/"
		spec := tc.spec
		spec.Path = folder + tc.current
		dest, err := catalogctx.MovieFilePath(root, movie, catalogctx.File(base, &spec, &tc.mi), catalogctx.ContainerExt(&tc.mi, spec.Path))
		require.NoError(t, err)
		require.Equal(t, spec.Path, dest, "a file Radarr named is already current")
	}
}
