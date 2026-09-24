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

package naming_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/naming"
)

type dialectGolden struct {
	Dialect      string `json:"dialect"`
	MovieFolder  string `json:"movieFolder"`
	MovieFile    string `json:"movieFile"`
	SeriesFolder string `json:"seriesFolder"`
}

func TestDialectGoldens(t *testing.T) {
	raw, err := os.ReadFile("../../test/data/naming/dialects.json")
	require.NoError(t, err)
	var goldens []dialectGolden
	require.NoError(t, json.Unmarshal(raw, &goldens))
	require.NotEmpty(t, goldens)

	movie := naming.Context{
		Title: "The Matrix", Year: 1999, TmdbID: "603",
		Quality:  commonv1.Quality{Source: commonv1.SourceBluray, Resolution: 2160},
		Revision: commonv1.Revision{Version: 1}, ReleaseGroup: "RlsGrp",
	}
	series := naming.Context{SeriesTitle: "The Series Title!", SeriesYear: 2010, TvdbID: "153021"}

	for _, g := range goldens {
		t.Run(g.Dialect, func(t *testing.T) {
			e := naming.NewEngine(naming.Config{Dialect: naming.Dialect(g.Dialect)})
			mf, err := e.MovieFolder(movie)
			require.NoError(t, err)
			require.Equal(t, g.MovieFolder, mf)

			mF, err := e.MovieFile(movie)
			require.NoError(t, err)
			require.Equal(t, g.MovieFile, mF)

			sf, err := e.SeriesFolder(series)
			require.NoError(t, err)
			require.Equal(t, g.SeriesFolder, sf)
		})
	}
}

func TestOverridesTakePrecedenceOverDialectPreset(t *testing.T) {
	e := naming.NewEngine(naming.Config{
		Dialect:   naming.DialectJellyfin,
		Overrides: map[string]string{naming.TokenMovieFolder: "{Movie Title} only"},
	})
	got, err := e.MovieFolder(naming.Context{Title: "Heat"})
	require.NoError(t, err)
	require.Equal(t, "Heat only", got)
}

func TestBuildFolderAndBuildFileDispatchByKind(t *testing.T) {
	e := naming.NewEngine(naming.Config{Dialect: naming.DialectJellyfin})

	folder, err := e.BuildFolder(commonv1.MediaKindMovie, naming.Context{Title: "Heat", Year: 1995, TmdbID: "949"})
	require.NoError(t, err)
	require.Equal(t, "Heat (1995) [tmdbid-949]", folder)

	_, err = e.BuildFile(commonv1.MediaKindSeries, naming.Context{})
	require.ErrorIs(t, err, naming.ErrNoFile)
}

func TestRenderAppliesSeparatorAndCase(t *testing.T) {
	e := naming.NewEngine(naming.Config{ReplaceSpaces: true, Separator: "_", Case: "lower"})
	got, err := e.Render("{Movie Title} ({Release Year})", naming.Context{Title: "The Matrix", Year: 1999})
	require.NoError(t, err)
	require.Equal(t, "the_matrix_(1999)", got)
}
