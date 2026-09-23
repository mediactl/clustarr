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

// TestEpisodeAndAbsoluteRangeTokensAreEmptyForEveryStyleWhenNoEpisodes is a
// direct test of formatEpisodeRange's and formatAbsoluteRange's own empty
// guard (through the internal-only {episodeRange}/{absoluteRange} tokens,
// since EpisodeFile's real templates never emit them -- see
// TestEpisodeFileWithNoEpisodesDoesNotPanic in errors_test.go for the
// end-to-end case). Every MultiEpisodeStyle must produce "" for a nil
// Episodes/Absolute slice, not just the zero value, and must not panic.
func TestEpisodeAndAbsoluteRangeTokensAreEmptyForEveryStyleWhenNoEpisodes(t *testing.T) {
	styles := []naming.MultiEpisodeStyle{
		naming.MultiEpisodeExtend,
		naming.MultiEpisodeDuplicate,
		naming.MultiEpisodeRepeat,
		naming.MultiEpisodeScene,
		naming.MultiEpisodeRange,
		naming.MultiEpisodePrefixedRange,
	}
	for _, style := range styles {
		t.Run(string(style), func(t *testing.T) {
			e := naming.NewEngine(naming.Config{MultiEpisodeStyle: style})

			var got string
			var err error
			require.NotPanics(t, func() {
				got, err = e.Render("{episodeRange}", naming.Context{Season: 1, Episodes: nil})
			})
			require.NoError(t, err)
			require.Empty(t, got)

			require.NotPanics(t, func() {
				got, err = e.Render("{absoluteRange}", naming.Context{Absolute: nil})
			})
			require.NoError(t, err)
			require.Empty(t, got)
		})
	}
}

// TestEpisodeFileNamesEveryEpisodeOfAMultiEpisodeFile is the X7a-found
// defect: the episode presets' "S{season:00}E{episode:00}" rendered only the
// first episode, so a file holding S01E01-E03 was named as S01E01 alone.
// Sonarr expands the whole season-episode pattern per MultiEpisodeStyle
// (FileNameBuilder.AddSeasonEpisodeNumberingTokens); every expected value
// below is what Sonarr's FormatNumberTokens/FormatRangeNumberTokens produce
// for the same pattern, including Duplicate's use of the separator the
// template puts before the pattern (" - ").
func TestEpisodeFileNamesEveryEpisodeOfAMultiEpisodeFile(t *testing.T) {
	c := naming.Context{
		SeriesTitle: "The Series Title!", SeriesYear: 2010,
		Season: 1, Episodes: []int{1, 2, 3}, EpisodeTitle: "Episode Title",
		Quality: commonv1.Quality{Source: commonv1.SourceWebDL, Resolution: 1080},
	}
	for _, tc := range []struct {
		style naming.MultiEpisodeStyle
		want  string
	}{
		{"", "S01E01-E03"}, // the zero value is Sonarr's and the CRD's default, prefixedRange
		{naming.MultiEpisodePrefixedRange, "S01E01-E03"},
		{naming.MultiEpisodeExtend, "S01E01-02-03"},
		{naming.MultiEpisodeDuplicate, "S01E01 - S01E02 - S01E03"},
		{naming.MultiEpisodeRepeat, "S01E01E02E03"},
		{naming.MultiEpisodeScene, "S01E01-E02-E03"},
		{naming.MultiEpisodeRange, "S01E01-03"},
	} {
		t.Run("style "+string(tc.style), func(t *testing.T) {
			got, err := naming.NewEngine(naming.Config{MultiEpisodeStyle: tc.style}).EpisodeFile(c)
			require.NoError(t, err)
			require.Equal(t, "The Series Title! (2010) - "+tc.want+" - Episode Title [WEBDL-1080p]", got)
		})
	}

	t.Run("anime: the absolute numbers are expanded too", func(t *testing.T) {
		anime := c
		anime.Absolute = []int{13, 14, 15}
		for _, tc := range []struct {
			style naming.MultiEpisodeStyle
			want  string
		}{
			{naming.MultiEpisodePrefixedRange, "S01E01-E03 - 013-015"},
			{naming.MultiEpisodeExtend, "S01E01-02-03 - 013-014-015"},
			// Duplicate repeats each pattern with the separator around it:
			// " - " on both sides, since the pattern and the absolute token
			// sit between "}" and "{" in the preset.
			{naming.MultiEpisodeDuplicate, "S01E01 - S01E02 - S01E03 - 013 - 014 - 015"},
			{naming.MultiEpisodeRepeat, "S01E01E02E03 - 013-014-015"},
		} {
			got, err := naming.NewEngine(naming.Config{MultiEpisodeStyle: tc.style}).EpisodeFile(anime)
			require.NoError(t, err)
			require.Equal(t, "The Series Title! (2010) - "+tc.want+" - Episode Title [WEBDL-1080p]", got, string(tc.style))
		}
	})

	t.Run("a Sonarr-syntax override template is expanded the same way", func(t *testing.T) {
		e := naming.NewEngine(naming.Config{
			MultiEpisodeStyle: naming.MultiEpisodeDuplicate,
			Overrides:         map[string]string{naming.TokenEpisodeFile: "{Series CleanTitleWithoutYear}.S{season:00}E{episode:00}.{Quality Full}"},
		})
		got, err := e.EpisodeFile(naming.Context{SeriesTitle: "Show", Season: 2, Episodes: []int{4, 5}, Quality: commonv1.Quality{Source: commonv1.SourceTV, Resolution: 720}})
		require.NoError(t, err)
		require.Equal(t, "Show.S02E04.S02E05.HDTV-720p", got)

		e = naming.NewEngine(naming.Config{Overrides: map[string]string{naming.TokenEpisodeFile: "{Series CleanTitleWithoutYear} {season}x{episode:00}"}})
		got, err = e.EpisodeFile(naming.Context{SeriesTitle: "Show", Season: 2, Episodes: []int{4, 5}})
		require.NoError(t, err)
		require.Equal(t, "Show 2x04-x05", got)
	})

	t.Run("a lone {episode} token is the first and last episode", func(t *testing.T) {
		got, err := naming.NewEngine(naming.Config{}).Render("Episode {episode:00}", naming.Context{Season: 1, Episodes: []int{1, 2, 3}})
		require.NoError(t, err)
		require.Equal(t, "Episode 01-03", got)
	})
}

// TestSeasonFolderHonoursTheOverride holds SeasonFolder to the RootFolder's
// naming.overrides.seasonFolder, which the CRD documents as overridable but
// the engine used to ignore (it always rendered "Season %02d"). Kodi's
// literal "Specials" for season 0 survives the override: Kodi's scrapers
// require that exact name.
func TestSeasonFolderHonoursTheOverride(t *testing.T) {
	tests := []struct {
		dialect  naming.Dialect
		override string
		season   int
		want     string
	}{
		{naming.DialectJellyfin, "S{season:00}", 3, "S03"},
		{naming.DialectJellyfin, "Season {season}", 3, "Season 3"},
		{naming.DialectPlex, "S{season:00}", 0, "S00"},
		{naming.DialectKodi, "S{season:00}", 2, "S02"},
		{naming.DialectKodi, "S{season:00}", 0, "Specials"},
		{naming.DialectJellyfin, "", 4, "Season 04"}, // an empty override is no override
	}
	for _, tt := range tests {
		e := naming.NewEngine(naming.Config{
			Dialect:   tt.dialect,
			Overrides: map[string]string{naming.TokenSeasonFolder: tt.override},
		})
		got, err := e.SeasonFolder(naming.Context{Season: tt.season})
		require.NoError(t, err)
		require.Equal(t, tt.want, got, "dialect %s, override %q, season %d", tt.dialect, tt.override, tt.season)
	}
}

// TestSeriesFolderKeepsApostrophes holds every dialect's series folder
// preset to the title as the provider spells it: a new show's folder used to
// be rendered from CleanTitle, which strips apostrophes ("Bobs Burgers").
// CleanTitle itself still strips them, for anyone who asks for it by name.
func TestSeriesFolderKeepsApostrophes(t *testing.T) {
	c := naming.Context{SeriesTitle: "Bob's Burgers", SeriesYear: 2011, TvdbID: "194031"}
	for _, d := range []naming.Dialect{naming.DialectJellyfin, naming.DialectPlex, naming.DialectEmby, naming.DialectKodi} {
		got, err := naming.NewEngine(naming.Config{Dialect: d}).SeriesFolder(c)
		require.NoError(t, err)
		require.Contains(t, got, "Bob's Burgers (2011)", "dialect %s", d)
	}

	clean, err := naming.NewEngine(naming.Config{
		Overrides: map[string]string{naming.TokenSeriesFolder: "{Series CleanTitleWithoutYear}"},
	}).SeriesFolder(c)
	require.NoError(t, err)
	require.Equal(t, "Bobs Burgers", clean)
}
