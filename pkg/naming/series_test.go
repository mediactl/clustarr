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
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/naming"
)

func TestSeasonFolderPadsAndNamesSpecialsPerDialect(t *testing.T) {
	tests := []struct {
		dialect naming.Dialect
		season  int
		want    string
	}{
		{naming.DialectJellyfin, 3, "Season 03"},
		{naming.DialectJellyfin, 0, "Season 00"},
		{naming.DialectPlex, 0, "Season 00"},
		{naming.DialectKodi, 0, "Specials"},
	}
	for _, tt := range tests {
		e := naming.NewEngine(naming.Config{Dialect: tt.dialect})
		got, err := e.SeasonFolder(naming.Context{Season: tt.season})
		require.NoError(t, err)
		require.Equal(t, tt.want, got)
	}
}

func TestSeriesFolderJellyfinIDTag(t *testing.T) {
	e := naming.NewEngine(naming.Config{Dialect: naming.DialectJellyfin})
	c := naming.Context{SeriesTitle: "The Series Title!", SeriesYear: 2010, TvdbID: "153021"}
	got, err := e.SeriesFolder(c)
	require.NoError(t, err)
	require.Equal(t, "The Series Title! (2010) [tvdbid-153021]", got)
}

func TestEpisodeFileStandard(t *testing.T) {
	e := naming.NewEngine(naming.Config{Dialect: naming.DialectJellyfin})
	c := naming.Context{
		SeriesTitle: "The Series Title!", SeriesYear: 2010,
		Season: 1, Episodes: []int{1}, EpisodeTitle: "Episode Title 1",
		Quality:  commonv1.Quality{Source: commonv1.SourceWebDL, Resolution: 1080},
		Revision: commonv1.Revision{Version: 2}, ReleaseGroup: "RlsGrp",
	}
	got, err := e.EpisodeFile(c)
	require.NoError(t, err)
	require.Equal(t, "The Series Title! (2010) - S01E01 - Episode Title 1 [WEBDL-1080p Proper]-RlsGrp", got)
}

func TestMultiEpisodeJoinStyles(t *testing.T) {
	tests := []struct {
		style naming.MultiEpisodeStyle
		want  string
	}{
		{naming.MultiEpisodeExtend, "S01E01-02-03"},
		{naming.MultiEpisodeRepeat, "S01E01E02E03"},
		{naming.MultiEpisodeScene, "S01E01-E02-E03"},
		{naming.MultiEpisodeRange, "S01E01-03"},
		{naming.MultiEpisodePrefixedRange, "S01E01-E03"},
	}
	for _, tt := range tests {
		t.Run(string(tt.style), func(t *testing.T) {
			e := naming.NewEngine(naming.Config{MultiEpisodeStyle: tt.style})
			got, err := e.Render("{episodeRange}", naming.Context{Season: 1, Episodes: []int{1, 2, 3}})
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestMultiEpisodeDuplicateStyleTwoEpisodes(t *testing.T) {
	e := naming.NewEngine(naming.Config{MultiEpisodeStyle: naming.MultiEpisodeDuplicate})
	got, err := e.Render("{episodeRange}", naming.Context{Season: 1, Episodes: []int{1, 2}})
	require.NoError(t, err)
	require.Equal(t, "S01E01.S01E02", got)
}

func TestEpisodeFileAnimeAbsoluteNumbering(t *testing.T) {
	e := naming.NewEngine(naming.Config{})
	c := naming.Context{
		SeriesTitle: "The Series Title!", SeriesYear: 2010,
		Season: 1, Episodes: []int{1}, Absolute: []int{1}, EpisodeTitle: "Episode Title 1",
		Quality: commonv1.Quality{Source: commonv1.SourceTV, Resolution: 720},
	}
	got, err := e.EpisodeFile(c)
	require.NoError(t, err)
	require.Equal(t, "The Series Title! (2010) - S01E01 - 001 - Episode Title 1 [HDTV-720p]", got)
}

func TestEpisodeFileAnimeAbsoluteRange(t *testing.T) {
	e := naming.NewEngine(naming.Config{MultiEpisodeStyle: naming.MultiEpisodeRange})
	c := naming.Context{Season: 1, Episodes: []int{13, 14}, Absolute: []int{13, 14}}
	got, err := e.Render("{absoluteRange}", c)
	require.NoError(t, err)
	require.Equal(t, "013-014", got)
}

func TestEpisodeFileDaily(t *testing.T) {
	airDate := time.Date(2013, 10, 30, 0, 0, 0, 0, time.UTC)
	e := naming.NewEngine(naming.Config{})
	c := naming.Context{
		SeriesTitle: "The Series Title!", SeriesYear: 2010,
		AirDate: &airDate, EpisodeTitle: "Episode Title 1",
		Quality: commonv1.Quality{Source: commonv1.SourceWebDL, Resolution: 1080},
	}
	got, err := e.EpisodeFile(c)
	require.NoError(t, err)
	require.Equal(t, "The Series Title! (2010) - 2013-10-30 - Episode Title 1 [WEBDL-1080p]", got)
}
