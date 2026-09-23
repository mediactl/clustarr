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

package subsource_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/release"
	"github.com/mediactl/clustarr/pkg/subtitles"
	"github.com/mediactl/clustarr/pkg/subtitles/providers/internal/subarchive/subarchivetest"
	"github.com/mediactl/clustarr/pkg/subtitles/providers/subsource"
)

const apiKey = "sekrit-key-456"

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("../../../../testdata/subtitles/subsource/" + name)
	require.NoError(t, err)
	return b
}

type request struct {
	path  string
	query url.Values
	key   string
}

// fakeSubSource routes by path; each handler returns a status, headers and
// a body. Every request is recorded with the X-API-Key it carried.
type fakeSubSource struct {
	mu     sync.Mutex
	reqs   []request
	routes map[string]func(url.Values) (int, map[string]string, []byte)
}

func (f *fakeSubSource) start(t *testing.T) *subsource.Provider {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.reqs = append(f.reqs, request{r.URL.Path, r.URL.Query(), r.Header.Get("X-API-Key")})
		f.mu.Unlock()
		h, ok := f.routes[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		code, headers, body := h(r.URL.Query())
		for k, v := range headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(code)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return subsource.New(subsource.Config{APIKey: apiKey, Endpoint: srv.URL + "/api/v1"})
}

func (f *fakeSubSource) requests() []request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]request(nil), f.reqs...)
}

func ok(body []byte) func(url.Values) (int, map[string]string, []byte) {
	return func(url.Values) (int, map[string]string, []byte) { return http.StatusOK, nil, body }
}

func inception() subtitles.Query {
	rel, _ := release.Parse("Inception.2010.720p.BluRay.x264-REWARD", release.Options{Kind: "movie"})
	return subtitles.Query{
		Kind: "movie", Title: "Inception", Year: 2010,
		IDs: map[string]string{"imdb": "1375666"}, Languages: []subtitles.LangKey{"en"}, Release: rel,
	}
}

func breakingBadS01E05() subtitles.Query {
	return subtitles.Query{
		Kind: "episode", Title: "Breaking Bad", Year: 2008, Season: 1, Episode: 5,
		IDs: map[string]string{"parent_imdb": "903747"}, Languages: []subtitles.LangKey{"en"},
	}
}

func movieServer(t *testing.T) (*fakeSubSource, *subsource.Provider) {
	f := &fakeSubSource{routes: map[string]func(url.Values) (int, map[string]string, []byte){
		"/api/v1/movies/search": ok(fixture(t, "titles_movie.json")),
		"/api/v1/subtitles":     ok(fixture(t, "movie_list.json")),
	}}
	return f, f.start(t)
}

// Bazarr's query(): the title id from movies/search by IMDb id (the one
// whose title and year match -- not "Inception 2"), then one language's
// listing for it. The key rides in X-API-Key, never the URL.
func TestMovieSearch(t *testing.T) {
	f, p := movieServer(t)

	cands, err := p.Search(context.Background(), inception())
	require.NoError(t, err)

	reqs := f.requests()
	require.Len(t, reqs, 2)
	assert.Equal(t, "/api/v1/movies/search", reqs[0].path)
	assert.Equal(t, "imdb", reqs[0].query.Get("searchType"))
	assert.Equal(t, "tt1375666", reqs[0].query.Get("imdb"))
	assert.Equal(t, "/api/v1/subtitles", reqs[1].path)
	assert.Equal(t, "100", reqs[1].query.Get("movieId"))
	assert.Equal(t, "english", reqs[1].query.Get("language"))
	assert.Equal(t, "100", reqs[1].query.Get("limit"))
	for _, r := range reqs {
		assert.Equal(t, apiKey, r.key)
		assert.False(t, r.query.Has("api_key"), "the key never goes in a URL")
	}

	require.Len(t, cands, 2, "the foreign-parts subtitle is not offered for a plain key")
	c := cands[0]
	assert.Equal(t, "subsource", c.Provider)
	assert.Equal(t, "9001", c.ID)
	assert.Equal(t, "en", c.Language)
	assert.Equal(t, "uploader-seven", c.Uploader, "the contributor whose id is the uploader's")
	assert.False(t, c.HI)
	assert.True(t, c.Matches[subtitles.MatchTitle])
	assert.True(t, c.Matches[subtitles.MatchYear])
	assert.True(t, c.Matches[subtitles.MatchReleaseGroup])
	assert.True(t, cands[1].HI, `"SDH" in the commentary`)
}

func TestAForcedKeyTakesOnlyForcedSubtitles(t *testing.T) {
	_, p := movieServer(t)
	q := inception()
	q.Languages = []subtitles.LangKey{"en:forced"}

	cands, err := p.Search(context.Background(), q)
	require.NoError(t, err)
	require.Len(t, cands, 1)
	assert.Equal(t, "9003", cands[0].ID)
	assert.True(t, cands[0].Forced)
}

// The title id is cached, as Bazarr caches search_titles for six hours.
func TestTheTitleIDIsCached(t *testing.T) {
	f, p := movieServer(t)
	for range 3 {
		_, err := p.Search(context.Background(), inception())
		require.NoError(t, err)
	}
	lookups := 0
	for _, r := range f.requests() {
		if r.path == "/api/v1/movies/search" {
			lookups++
		}
	}
	assert.Equal(t, 1, lookups)
}

// No IMDb search hit: the text search is the fallback, a match there needs
// the year, and it is not an id match -- but the checked year still is one.
func TestTheTextSearchFallback(t *testing.T) {
	f := &fakeSubSource{routes: map[string]func(url.Values) (int, map[string]string, []byte){
		"/api/v1/movies/search": func(q url.Values) (int, map[string]string, []byte) {
			if q.Get("searchType") == "imdb" {
				return http.StatusOK, nil, []byte(`{"success":true,"data":[]}`)
			}
			return http.StatusOK, nil, fixture(t, "titles_movie.json")
		},
		"/api/v1/subtitles": ok(fixture(t, "movie_list.json")),
	}}
	p := f.start(t)

	cands, err := p.Search(context.Background(), inception())
	require.NoError(t, err)
	reqs := f.requests()
	require.Len(t, reqs, 3)
	assert.Equal(t, "text", reqs[1].query.Get("searchType"))
	assert.Equal(t, "inception", reqs[1].query.Get("q"))
	require.NotEmpty(t, cands)
	assert.True(t, cands[0].Matches[subtitles.MatchYear], "the lookup checked the year")

	q := inception()
	q.Year = 0
	q.IDs = map[string]string{"imdb": "1"} // a different cache key
	cands, err = p.Search(context.Background(), q)
	require.NoError(t, err)
	require.NotEmpty(t, cands)
	assert.False(t, cands[0].Matches[subtitles.MatchYear], "neither an id match nor a checked year")
}

// Bazarr looks a title up only when it has an IMDb id.
func TestNoIMDbIDMeansNoSearch(t *testing.T) {
	f, p := movieServer(t)
	q := inception()
	q.IDs = nil
	cands, err := p.Search(context.Background(), q)
	require.NoError(t, err)
	assert.Empty(t, cands)
	assert.Empty(t, f.requests())
}

// Episodes: the season's own title entry, then the listing by season and
// episode, kept only where the release names spell this season and this
// episode or none (a season pack).
func TestEpisodeSearch(t *testing.T) {
	f := &fakeSubSource{routes: map[string]func(url.Values) (int, map[string]string, []byte){
		"/api/v1/movies/search": ok(fixture(t, "titles_season.json")),
		"/api/v1/subtitles":     ok(fixture(t, "episode_list.json")),
	}}
	p := f.start(t)

	cands, err := p.Search(context.Background(), breakingBadS01E05())
	require.NoError(t, err)

	reqs := f.requests()
	require.Len(t, reqs, 2)
	assert.Equal(t, "tt0903747", reqs[0].query.Get("imdb"))
	assert.Equal(t, "1", reqs[0].query.Get("season"))
	assert.Equal(t, "200", reqs[1].query.Get("movieId"))
	assert.Equal(t, "1", reqs[1].query.Get("seasonNumber"))
	assert.Equal(t, "5", reqs[1].query.Get("episodeNumber"))

	ids := []string{}
	for _, c := range cands {
		ids = append(ids, c.ID)
		assert.True(t, c.Matches[subtitles.MatchSeries])
		assert.True(t, c.Matches[subtitles.MatchSeason])
		assert.True(t, c.Matches[subtitles.MatchEpisode])
		assert.True(t, c.Matches[subtitles.MatchYear])
	}
	assert.Equal(t, []string{"9101", "9102"}, ids, "E06 and S02E05 are dropped; the season pack is kept")
}

func TestDownloadTakesTheEpisodeFromASeasonPack(t *testing.T) {
	f := &fakeSubSource{routes: map[string]func(url.Values) (int, map[string]string, []byte){
		"/api/v1/movies/search": ok(fixture(t, "titles_season.json")),
		"/api/v1/subtitles":     ok(fixture(t, "episode_list.json")),
		"/api/v1/subtitles/9102/download": ok(subarchivetest.Zip(t,
			"Breaking.Bad.S01E04.srt", "four", "Breaking.Bad.S01E05.srt", "five")),
		"/api/v1/subtitles/9101/download": ok(subarchivetest.Zip(t, "whatever.srt", "the one")),
	}}
	p := f.start(t)
	cands, err := p.Search(context.Background(), breakingBadS01E05())
	require.NoError(t, err)
	require.Len(t, cands, 2)

	raw, name, err := p.Download(context.Background(), cands[1])
	require.NoError(t, err)
	assert.Equal(t, "five", string(raw))
	assert.Equal(t, "Breaking.Bad.S01E05.srt", name)

	raw, _, err = p.Download(context.Background(), cands[0])
	require.NoError(t, err)
	assert.Equal(t, "the one", string(raw), "a single member is taken whatever it is named")

	for _, r := range f.requests() {
		assert.Equal(t, apiKey, r.key)
	}
}

// A pack without the episode is this candidate's failure, not a guess and
// not a ProviderError; so is a gone subtitle and a download that is not an
// archive.
func TestPerCandidateFailuresAreNotProviderErrors(t *testing.T) {
	f := &fakeSubSource{routes: map[string]func(url.Values) (int, map[string]string, []byte){
		"/api/v1/subtitles/1/download": ok(subarchivetest.Zip(t, "a.S01E04.srt", "four", "b.S01E06.srt", "six")),
		"/api/v1/subtitles/2/download": ok([]byte("not an archive")),
	}}
	p := f.start(t)
	var pe *subtitles.ProviderError

	_, _, err := p.Download(context.Background(), subtitles.Candidate{FetchID: `{"id":"1","p":true,"s":1,"e":5}`})
	require.ErrorIs(t, err, subsource.ErrNoSubtitle)
	assert.False(t, errors.As(err, &pe))

	_, _, err = p.Download(context.Background(), subtitles.Candidate{FetchID: `{"id":"2"}`})
	require.ErrorIs(t, err, subsource.ErrNoSubtitle)
	assert.False(t, errors.As(err, &pe))

	_, _, err = p.Download(context.Background(), subtitles.Candidate{FetchID: `{"id":"3"}`})
	require.ErrorIs(t, err, subsource.ErrRejected)
	assert.False(t, errors.As(err, &pe))
}

// Bazarr's _status_raiser and _retry_after, less the inline sleep.
func TestStatusMapping(t *testing.T) {
	reset := time.Now().Add(26 * time.Second).UTC().Format(time.RFC3339)
	for _, tc := range []struct {
		name     string
		code     int
		headers  map[string]string
		body     string
		kind     string
		throttle time.Duration
	}{
		{"400", http.StatusBadRequest, nil, `{"message":"Invalid request parameters"}`, subtitles.KindAPIThrottled, 10 * time.Minute},
		// Verified live 2026-09-23: what a keyless request gets.
		{"401", http.StatusUnauthorized, nil, `{"error":"API key required","message":"Please provide an API key in the X-API-Key header or api_key query parameter"}`, subtitles.KindAuth, time.Hour},
		{"403", http.StatusForbidden, nil, `{"message":"Access denied"}`, subtitles.KindAuth, 15 * time.Minute},
		{"429 with X-RateLimit-Reset", http.StatusTooManyRequests, map[string]string{"X-RateLimit-Reset": reset}, `{"retryAfter":3}`, subtitles.KindTooManyRequests, 26 * time.Second},
		{"429 with only retryAfter", http.StatusTooManyRequests, nil, `{"retryAfter":3}`, subtitles.KindTooManyRequests, 3 * time.Second},
		{"429 from the CDN", http.StatusTooManyRequests, nil, `<html>too many requests</html>`, subtitles.KindTooManyRequests, time.Hour},
		{"429 reset already past", http.StatusTooManyRequests, map[string]string{"X-RateLimit-Reset": time.Now().Add(-5 * time.Second).UTC().Format(time.RFC3339)}, ``, subtitles.KindTooManyRequests, time.Second},
		{"502", http.StatusBadGateway, nil, ``, subtitles.KindServiceUnavailable, 20 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeSubSource{routes: map[string]func(url.Values) (int, map[string]string, []byte){
				"/api/v1/movies/search": func(url.Values) (int, map[string]string, []byte) { return tc.code, tc.headers, []byte(tc.body) },
			}}
			p := f.start(t)

			_, err := p.Search(context.Background(), inception())
			var pe *subtitles.ProviderError
			require.ErrorAs(t, err, &pe)
			assert.Equal(t, tc.kind, pe.Kind)
			_, d := subtitles.ThrottleFor("subsource", err)
			assert.InDelta(t, tc.throttle.Seconds(), d.Seconds(), 2)
		})
	}
}

func TestAMissingAPIKeyIsAConfigError(t *testing.T) {
	_, err := subsource.New(subsource.Config{}).Search(context.Background(), inception())
	var pe *subtitles.ProviderError
	require.ErrorAs(t, err, &pe)
	assert.Equal(t, subtitles.KindConfig, pe.Kind)
}

func TestOversizedResponsesAreRefused(t *testing.T) {
	huge := []byte(`{"padding":"` + strings.Repeat("a", 5<<20) + `"}`)
	f := &fakeSubSource{routes: map[string]func(url.Values) (int, map[string]string, []byte){
		"/api/v1/movies/search":        ok(huge),
		"/api/v1/subtitles/1/download": ok(make([]byte, 33<<20)),
	}}
	p := f.start(t)
	_, err := p.Search(context.Background(), inception())
	require.ErrorIs(t, err, subsource.ErrResponseTooLarge)
	_, _, err = p.Download(context.Background(), subtitles.Candidate{FetchID: `{"id":"1"}`})
	require.ErrorIs(t, err, subsource.ErrResponseTooLarge)
}

// Bazarr's _is_hi and _is_forced over the commentary.
func TestHearingImpairedAndForcedHeuristics(t *testing.T) {
	for _, tc := range []struct {
		commentary string
		hi, forced bool
	}{
		{"Full SDH version", true, false},
		{"non-SDH, HI removed", false, false},
		{"closed captions", true, false},
		{"Forced subs only", false, true},
		{"foreign dialogue", false, true},
		{"plain", false, false},
	} {
		t.Run(tc.commentary, func(t *testing.T) {
			body := []byte(`{"success":true,"data":[{"subtitleId":1,"language":"english","releaseInfo":["Inception.2010.720p.BluRay.x264-REWARD"],"commentary":"` + tc.commentary + `"}]}`)
			f := &fakeSubSource{routes: map[string]func(url.Values) (int, map[string]string, []byte){
				"/api/v1/movies/search": ok(fixture(t, "titles_movie.json")),
				"/api/v1/subtitles":     ok(body),
			}}
			p := f.start(t)
			q := inception()
			if tc.forced {
				q.Languages = []subtitles.LangKey{"en:forced"}
			}
			cands, err := p.Search(context.Background(), q)
			require.NoError(t, err)
			require.Len(t, cands, 1)
			assert.Equal(t, tc.hi, cands[0].HI)
			assert.Equal(t, tc.forced, cands[0].Forced)
		})
	}
}

// The converter tries the region first, then the base language (#3481):
// pt-BR is Brazilian Portuguese, es-MX falls back to Spanish.
func TestLanguageNames(t *testing.T) {
	f, p := movieServer(t)
	for tag, want := range map[string]string{"pt-BR": "brazilian_portuguese", "pt": "portuguese", "es-MX": "spanish", "fa": "farsi_persian", "zh-TW": "chinese_bg_code"} {
		q := inception()
		q.Languages = []subtitles.LangKey{subtitles.LangKey(tag)}
		_, err := p.Search(context.Background(), q)
		require.NoError(t, err)
		reqs := f.requests()
		assert.Equal(t, want, reqs[len(reqs)-1].query.Get("language"), tag)
	}
	assert.False(t, p.Capabilities().Languages("xx"))
}
