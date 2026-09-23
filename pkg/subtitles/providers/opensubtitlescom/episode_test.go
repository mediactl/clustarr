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

package opensubtitlescom_test

import (
	"context"
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/subtitles"
	"github.com/mediactl/clustarr/pkg/subtitles/providers/opensubtitlescom"
)

// searchServer serves login.json and the named search fixture, recording
// every /subtitles query.
func searchServer(t *testing.T, fixture string) (*opensubtitlescom.Provider, *[]url.Values) {
	t.Helper()
	var queries []url.Values
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			_, _ = w.Write(readFixture(t, "login.json"))
		case "/subtitles":
			queries = append(queries, r.URL.Query())
			_, _ = w.Write(readFixture(t, fixture))
		default:
			http.NotFound(w, r)
		}
	})
	return opensubtitlescom.New(opensubtitlescom.Config{APIKey: "k", Username: "u", Password: "p", Endpoint: srv.URL}), &queries
}

// breakingBadS01E01 is what captionarr's fetch worker builds for an
// episode: the show's ids under parent_imdb/parent_tmdb (the IMDb one with
// its leading zero, as TMDB reports it), and no ids of the episode's own.
func breakingBadS01E01() subtitles.Query {
	return subtitles.Query{
		Kind: "episode", Title: "Breaking Bad", Year: 2008, Season: 1, Episode: 1,
		IDs:       map[string]string{"tvdb": "81189", "parent_imdb": "0903747", "parent_tmdb": "1396"},
		Languages: []subtitles.LangKey{"en"},
	}
}

// The carried item: "OpenSubtitles.com ignores parent_imdb/parent_tmdb, so
// an episode is searched by moviehash only". The show's ids now reach the
// API as parent_imdb_id/parent_tmdb_id, normalised the way Bazarr's
// sanitize_external_ids does (no leading zero), with season and episode.
func TestEpisodeSearchSendsTheShowsIDsAsParentIDs(t *testing.T) {
	p, queries := searchServer(t, "search_episode.json")

	_, err := p.Search(context.Background(), breakingBadS01E01())
	require.NoError(t, err)

	require.Len(t, *queries, 1)
	q := (*queries)[0]
	assert.Equal(t, "903747", q.Get("parent_imdb_id"))
	assert.Equal(t, "1396", q.Get("parent_tmdb_id"))
	assert.Equal(t, "1", q.Get("season_number"))
	assert.Equal(t, "1", q.Get("episode_number"))
	assert.Equal(t, "episode", q.Get("type"))
	assert.False(t, q.Has("imdb_id"), "the show's id must not be sent as the episode's own imdb_id")
	assert.False(t, q.Has("tmdb_id"))
}

// A movie has no parent: a stray parent id on a movie query is not sent.
func TestMovieSearchNeverSendsParentIDs(t *testing.T) {
	p, queries := searchServer(t, "search.json")

	_, err := p.Search(context.Background(), subtitles.Query{
		Kind: "movie", IDs: map[string]string{"imdb": "tt1375666", "parent_imdb": "903747"},
		Languages: []subtitles.LangKey{"en"},
	})
	require.NoError(t, err)

	require.Len(t, *queries, 1)
	assert.Equal(t, "1375666", (*queries)[0].Get("imdb_id"), "the tt prefix is stripped")
	assert.False(t, (*queries)[0].Has("parent_imdb_id"))
}

// Bazarr's get_matches for an episode result: series always, season and
// episode when feature_details agrees with the query, year when the show's
// id matches (score.py's series_imdb_id expansion) or the year does.
func TestEpisodeCandidatesCarryTheirIdentityMatches(t *testing.T) {
	p, _ := searchServer(t, "search_episode.json")

	cands, err := p.Search(context.Background(), breakingBadS01E01())
	require.NoError(t, err)
	require.Len(t, cands, 2)

	assert.Equal(t, map[string]bool{
		subtitles.MatchSeries: true, subtitles.MatchSeason: true,
		subtitles.MatchEpisode: true, subtitles.MatchYear: true,
	}, cands[0].Matches, "S01E01 under the right show")
	assert.Equal(t, map[string]bool{
		subtitles.MatchSeries: true, subtitles.MatchSeason: true, subtitles.MatchYear: true,
	}, cands[1].Matches, "S01E02 is the wrong episode: no episode match")
	assert.Equal(t, "112233", cands[0].FetchID)
	assert.True(t, cands[1].HI)
}

// When the show's id does not match and no year was asked for, only what
// feature_details itself confirms is claimed.
func TestEpisodeYearNeedsAnIDOrYearMatch(t *testing.T) {
	p, _ := searchServer(t, "search_episode.json")
	q := breakingBadS01E01()
	q.IDs = map[string]string{"parent_imdb": "1"}
	q.Year = 0

	cands, err := p.Search(context.Background(), q)
	require.NoError(t, err)
	require.NotEmpty(t, cands)
	assert.False(t, cands[0].Matches[subtitles.MatchYear])
	assert.True(t, cands[0].Matches[subtitles.MatchEpisode])
}

// A movie result: title always; year from the matching id.
func TestMovieCandidatesCarryTitleAndYear(t *testing.T) {
	p, _ := searchServer(t, "search.json")

	cands, err := p.Search(context.Background(), subtitles.Query{
		Kind: "movie", IDs: map[string]string{"tmdb": "27205"}, Languages: []subtitles.LangKey{"en"},
	})
	require.NoError(t, err)
	require.Len(t, cands, 1)
	assert.Equal(t, map[string]bool{
		subtitles.MatchTitle: true, subtitles.MatchYear: true, subtitles.MatchHash: true,
	}, cands[0].Matches)
}
