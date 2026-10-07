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
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/naming/catalogctx"
)

func rootFolder(path string, kind catalogv1alpha1.RootFolderKind) *catalogv1alpha1.RootFolder {
	return &catalogv1alpha1.RootFolder{Spec: catalogv1alpha1.RootFolderSpec{
		Path: path, Kind: kind,
		Naming: catalogv1alpha1.NamingSpec{Dialect: catalogv1alpha1.NamingDialectJellyfin},
	}}
}

func seriesTitled(title string) *catalogv1alpha1.Series {
	return &catalogv1alpha1.Series{
		Spec:   catalogv1alpha1.SeriesSpec{TvdbID: 266189, SeriesType: catalogv1alpha1.SeriesTypeStandard},
		Status: catalogv1alpha1.SeriesStatus{Metadata: &catalogv1alpha1.SeriesMetadata{Title: title, Year: 2010}},
	}
}

func episodePath(t *testing.T, s *catalogv1alpha1.Series) (string, error) {
	t.Helper()
	eps := []catalogv1alpha1.Episode{{
		Spec:   catalogv1alpha1.EpisodeSpec{SeasonNumber: 1, EpisodeNumber: 1},
		Status: catalogv1alpha1.EpisodeStatus{Title: "Episode 1"},
	}}
	c, ok := catalogctx.Episode(s, eps)
	require.True(t, ok)
	return catalogctx.EpisodeFilePath(rootFolder("/data/media/series", catalogv1alpha1.RootFolderKindSeries), s, c, ".mkv")
}

// TestEpisodeFilePathKeepsASlashedTitleInOneFolder: TVDB titles the Irish
// drama "Love/Hate"; it is one series folder, not "Love" holding "Hate".
func TestEpisodeFilePathKeepsASlashedTitleInOneFolder(t *testing.T) {
	got, err := episodePath(t, seriesTitled("Love/Hate"))
	require.NoError(t, err)
	require.Equal(t, "/data/media/series/Love+Hate (2010) [tvdbid-266189]/Season 01/Love+Hate (2010) - S01E01 - Episode 1.mkv", got)
}

// TestEpisodeFilePathCannotLeaveTheRootFolder: a crowd-edited title, a
// Windows separator, a dot-only title, a spec.folder and a stale
// status.path pointing outside the root folder each either stay strictly
// under root.spec.path or fail -- never a path outside it.
func TestEpisodeFilePathCannotLeaveTheRootFolder(t *testing.T) {
	got, err := episodePath(t, seriesTitled("x/../../../../etc"))
	require.NoError(t, err)
	require.Equal(t, "/data/media/series/x+..+..+..+..+etc (2010) [tvdbid-266189]/Season 01/x+..+..+..+..+etc (2010) - S01E01 - Episode 1.mkv", got)

	got, err = episodePath(t, seriesTitled(`a\b`))
	require.NoError(t, err)
	require.Equal(t, "/data/media/series/a+b (2010) [tvdbid-266189]/Season 01/a+b (2010) - S01E01 - Episode 1.mkv", got)

	kodi := seriesTitled("..")
	eps := []catalogv1alpha1.Episode{{Spec: catalogv1alpha1.EpisodeSpec{SeasonNumber: 1, EpisodeNumber: 1}}}
	c, ok := catalogctx.Episode(kodi, eps)
	require.True(t, ok)
	root := rootFolder("/data/media/series", catalogv1alpha1.RootFolderKindSeries)
	root.Spec.Naming.Overrides = map[string]string{"seriesFolder": "{Series TitleWithoutYear}"}
	_, err = catalogctx.EpisodeFilePath(root, kodi, c, ".mkv")
	require.Error(t, err, "a series folder of only dots is the root's parent")

	escaping := seriesTitled("Heat")
	escaping.Spec.Folder = new("../../etc")
	_, err = episodePath(t, escaping)
	require.Error(t, err, "spec.folder outside the root folder")

	stale := seriesTitled("Heat")
	stale.Status.Path = "/etc"
	_, err = episodePath(t, stale)
	require.Error(t, err, "status.path outside the root folder")

	sub := seriesTitled("Heat")
	sub.Spec.Folder = new("Anime/Heat")
	got, err = episodePath(t, sub)
	require.NoError(t, err, "a spec.folder naming a subfolder of the root stays legal")
	require.True(t, strings.HasPrefix(got, "/data/media/series/Anime/Heat/Season 01/"), got)
}

func moviePath(m *catalogv1alpha1.Movie) (string, error) {
	c, ok := catalogctx.Movie(m)
	if !ok {
		panic("movie has no metadata")
	}
	return catalogctx.MovieFilePath(rootFolder("/data/media/movies", catalogv1alpha1.RootFolderKindMovie), m, c, ".mkv")
}

func movieTitled(title string) *catalogv1alpha1.Movie {
	return &catalogv1alpha1.Movie{
		Spec:   catalogv1alpha1.MovieSpec{TmdbID: 754},
		Status: catalogv1alpha1.MovieStatus{Metadata: &catalogv1alpha1.MovieMetadata{Title: title, Year: 1997}},
	}
}

// TestMovieFilePathCannotLeaveTheRootFolder: Radarr's CleanTitle already
// spaces a "/" ("Face/Off" is "Face Off"), but a backslash survives it and
// is CleanFileName's "+", and a spec.folder is taken as written -- so the
// root check is what holds a movie inside its root folder.
func TestMovieFilePathCannotLeaveTheRootFolder(t *testing.T) {
	got, err := moviePath(movieTitled("Face/Off"))
	require.NoError(t, err)
	require.Equal(t, "/data/media/movies/Face Off (1997) [tmdbid-754]/Face Off (1997).mkv", got)

	got, err = moviePath(movieTitled(`a\b`))
	require.NoError(t, err)
	require.Equal(t, "/data/media/movies/a+b (1997) [tmdbid-754]/a+b (1997).mkv", got)

	m := movieTitled("Heat")
	m.Spec.Folder = new("x/../../../../etc")
	_, err = moviePath(m)
	require.Error(t, err, "spec.folder outside the root folder")

	m.Spec.Folder = new("..")
	_, err = moviePath(m)
	require.Error(t, err, "spec.folder naming the root's parent")
}
