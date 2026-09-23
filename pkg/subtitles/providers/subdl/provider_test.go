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

package subdl_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	"github.com/mediactl/clustarr/pkg/subtitles/providers/subdl"
)

const apiKey = "sekrit-key-123"

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("../../../../testdata/subtitles/subdl/" + name)
	require.NoError(t, err)
	return b
}

// fakeSubDL answers /api/v1/subtitles with search(query) and every other
// path with files[path]; it records each search query in order.
type fakeSubDL struct {
	mu      sync.Mutex
	queries []url.Values
	search  func(q url.Values) (int, []byte)
	files   map[string][]byte
}

func (f *fakeSubDL) start(t *testing.T) (*httptest.Server, *subdl.Provider) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/subtitles" {
			f.mu.Lock()
			f.queries = append(f.queries, r.URL.Query())
			f.mu.Unlock()
			code, body := f.search(r.URL.Query())
			w.WriteHeader(code)
			_, _ = w.Write(body)
			return
		}
		if b, ok := f.files[r.URL.Path]; ok {
			_, _ = w.Write(b)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, subdl.New(subdl.Config{APIKey: apiKey, Endpoint: srv.URL + "/api/v1"})
}

func (f *fakeSubDL) recorded() []url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]url.Values(nil), f.queries...)
}

func always(code int, body []byte) func(url.Values) (int, []byte) {
	return func(url.Values) (int, []byte) { return code, body }
}

func inception() subtitles.Query {
	rel, _ := release.Parse("Inception.2010.720p.BluRay.x264-REWARD", release.Options{Kind: "movie"})
	return subtitles.Query{
		Kind: "movie", Title: "Inception", Year: 2010,
		IDs:       map[string]string{"imdb": "1375666", "tmdb": "27205"},
		Languages: []subtitles.LangKey{"en"}, Release: rel,
	}
}

func breakingBadS01E05() subtitles.Query {
	return subtitles.Query{
		Kind: "episode", Title: "Breaking Bad", Year: 2008, Season: 1, Episode: 5,
		IDs:       map[string]string{"tvdb": "81189", "parent_imdb": "903747", "parent_tmdb": "1396"},
		Languages: []subtitles.LangKey{"en"},
	}
}

// Bazarr's query() minus its bazarr=1 integration flag: the documented
// comment/releases/hi/unpack flags, the SubDL language codes, the IMDb id in
// its tt-and-seven-digits form, 30 per page.
func TestMovieSearchSendsBazarrsParameters(t *testing.T) {
	f := &fakeSubDL{search: always(http.StatusOK, fixture(t, "movie_search.json"))}
	_, p := f.start(t)

	q := inception()
	q.Languages = []subtitles.LangKey{"en", "en:hi", "pt-BR"}
	_, err := p.Search(context.Background(), q)
	require.NoError(t, err)

	qs := f.recorded()
	require.Len(t, qs, 1)
	got := qs[0]
	assert.Equal(t, apiKey, got.Get("api_key"))
	assert.Equal(t, "tt1375666", got.Get("imdb_id"))
	assert.False(t, got.Has("film_name"), "an id search sends no film_name")
	assert.Equal(t, "movie", got.Get("type"))
	assert.Equal(t, "BR_PT,EN", got.Get("languages"), "deduplicated, sorted SubDL codes")
	assert.Equal(t, "30", got.Get("subs_per_page"))
	for _, flag := range []string{"comment", "releases", "hi", "unpack"} {
		assert.Equal(t, "1", got.Get(flag), flag)
	}
	assert.False(t, got.Has("bazarr"), "Clustarr must not present itself as Bazarr: bazarr=1 is not a documented filter")
	assert.False(t, got.Has("client"), "no integration is claimed at all")
}

func TestMovieCandidates(t *testing.T) {
	f := &fakeSubDL{search: always(http.StatusOK, fixture(t, "movie_search.json"))}
	_, p := f.start(t)

	cands, err := p.Search(context.Background(), inception())
	require.NoError(t, err)
	byID := map[string]subtitles.Candidate{}
	for _, c := range cands {
		byID[c.ID] = c
	}
	require.Len(t, byID, 3, "the forced subtitle is not offered for a plain key, and the unknown language code is skipped")

	plain := byID["SUBDL::inception-english-12345.zip"]
	assert.Equal(t, "subdl", plain.Provider)
	assert.Equal(t, "en", plain.Language)
	assert.False(t, plain.HI)
	assert.False(t, plain.Forced)
	assert.Equal(t, "subber", plain.Uploader)
	assert.Equal(t, "Inception.2010.720p.BluRay.x264-REWARD\nInception.2010.1080p.BluRay.x264-SPARKS", plain.ReleaseInfo)
	assert.True(t, plain.Matches[subtitles.MatchTitle], "an IMDb-id search claims the title")
	assert.True(t, plain.Matches[subtitles.MatchYear], "and the year")
	assert.True(t, plain.Matches[subtitles.MatchReleaseGroup], "the REWARD release name corroborates the target's group")
	assert.True(t, plain.Matches[subtitles.MatchSource])

	assert.True(t, byID["SUBDL::inception-english-12346.zip"].HI, `"SDH version" marks a hearing-impaired subtitle`)
	assert.False(t, byID["SUBDL::inception-english-12346.zip"].Matches[subtitles.MatchReleaseGroup])

	ai := byID["SUBDL::inception-english-12347.zip"]
	assert.True(t, ai.AITranslated, "AI translations are flagged for the caller's filter, not dropped here")
}

// A forced key asks for forced subtitles only.
func TestAForcedKeyTakesOnlyForcedSubtitles(t *testing.T) {
	f := &fakeSubDL{search: always(http.StatusOK, fixture(t, "movie_search.json"))}
	_, p := f.start(t)
	q := inception()
	q.Languages = []subtitles.LangKey{"en:forced"}

	cands, err := p.Search(context.Background(), q)
	require.NoError(t, err)
	require.Len(t, cands, 1)
	assert.True(t, cands[0].Forced)
	assert.Equal(t, "SUBDL::inception-english-12348.zip", cands[0].ID)
}

// Without an IMDb id the film name is searched -- sanitised the way Bazarr
// does, every unsafe character a space -- and only the title is claimed.
func TestAFilmNameSearchIsSanitisedAndClaimsOnlyTheTitle(t *testing.T) {
	f := &fakeSubDL{search: always(http.StatusOK, fixture(t, "movie_search.json"))}
	_, p := f.start(t)
	q := inception()
	q.IDs = nil
	q.Title = "Grey's Anatomy: [The] \"Movie\""

	cands, err := p.Search(context.Background(), q)
	require.NoError(t, err)
	assert.Equal(t, "Grey s Anatomy: The Movie", f.recorded()[0].Get("film_name"))
	require.NotEmpty(t, cands)
	assert.True(t, cands[0].Matches[subtitles.MatchTitle])
	assert.False(t, cands[0].Matches[subtitles.MatchYear], "a fuzzy film-name search does not establish the year")
}

// Movies: when the IMDb search finds nothing, TMDB is tried, alone, and a
// hit there is an id match.
func TestMovieFallsBackToTMDB(t *testing.T) {
	f := &fakeSubDL{search: func(q url.Values) (int, []byte) {
		if q.Has("tmdb_id") {
			return http.StatusOK, fixture(t, "movie_search.json")
		}
		return http.StatusOK, []byte(`{"status":true,"subtitles":[],"totalPages":1}`)
	}}
	_, p := f.start(t)

	cands, err := p.Search(context.Background(), inception())
	require.NoError(t, err)
	qs := f.recorded()
	require.Len(t, qs, 2)
	assert.Equal(t, "27205", qs[1].Get("tmdb_id"))
	assert.False(t, qs[1].Has("imdb_id"))
	assert.False(t, qs[1].Has("film_name"))
	require.NotEmpty(t, cands)
	assert.True(t, cands[0].Matches[subtitles.MatchYear], "a TMDB hit is an id match")
}

// Nothing identifies the item: no request is made at all, since a search on
// languages alone returns other titles' subtitles.
func TestNothingToSearchByMakesNoRequest(t *testing.T) {
	f := &fakeSubDL{search: always(http.StatusOK, fixture(t, "movie_search.json"))}
	_, p := f.start(t)

	cands, err := p.Search(context.Background(), subtitles.Query{Kind: "episode", Season: 1, Episode: 1, Languages: []subtitles.LangKey{"en"}})
	require.NoError(t, err)
	assert.Empty(t, cands)
	assert.Empty(t, f.recorded())
}

func TestEpisodeSearch(t *testing.T) {
	f := &fakeSubDL{search: always(http.StatusOK, fixture(t, "episode_search.json"))}
	_, p := f.start(t)

	cands, err := p.Search(context.Background(), breakingBadS01E05())
	require.NoError(t, err)

	qs := f.recorded()
	require.Len(t, qs, 2, "the episode search, then the season-only fallback")
	assert.Equal(t, "tt0903747", qs[0].Get("imdb_id"), "the show's IMDb id, padded back to seven digits")
	assert.Equal(t, "tv", qs[0].Get("type"))
	assert.Equal(t, "1", qs[0].Get("season_number"))
	assert.Equal(t, "5", qs[0].Get("episode_number"))
	assert.Equal(t, "1", qs[1].Get("season_number"))
	assert.False(t, qs[1].Has("episode_number"))

	byID := map[string]subtitles.Candidate{}
	for _, c := range cands {
		byID[c.ID] = c
	}
	require.Len(t, byID, 3, "the E01-E04 pack cannot hold E05 and is dropped; the duplicates of the fallback merge away")

	single := byID["SUBDL::breaking-bad-s01e05.zip"]
	assert.Equal(t, map[string]bool{
		subtitles.MatchSeries: true, subtitles.MatchYear: true,
		subtitles.MatchSeason: true, subtitles.MatchEpisode: true,
	}, single.Matches)

	// Bazarr's test_unpack_selection_uses_effective_hi_class: the pack is
	// HI by its comment, so the server-unpacked SDH member is chosen.
	unpacked := byID["SUBDL::breaking-bad-season-1.zip/sdh5"]
	assert.True(t, unpacked.HI)
	assert.True(t, unpacked.Matches[subtitles.MatchEpisode])
	assert.True(t, unpacked.Matches[subtitles.MatchSeason])
	assert.Contains(t, unpacked.FetchID, "/subtitle/555-2-e05-sdh.srt")

	pack := byID["SUBDL::breaking-bad-e05-e07.zip"]
	assert.True(t, pack.Matches[subtitles.MatchEpisode], "a pack validated to hold the episode matches it")
}

// The title-only fallback runs only when every season-filtered search came
// back empty.
func TestEpisodeTitleOnlyFallback(t *testing.T) {
	f := &fakeSubDL{search: func(q url.Values) (int, []byte) {
		if q.Has("season_number") {
			return http.StatusOK, []byte(`{"status":true,"subtitles":[],"totalPages":1}`)
		}
		return http.StatusOK, fixture(t, "episode_search.json")
	}}
	_, p := f.start(t)

	cands, err := p.Search(context.Background(), breakingBadS01E05())
	require.NoError(t, err)
	qs := f.recorded()
	require.Len(t, qs, 3)
	assert.Equal(t, "tv", qs[2].Get("type"))
	assert.False(t, qs[2].Has("season_number"))
	assert.NotEmpty(t, cands)
}

// Pagination is shallow: a second page only when the first came back full,
// and never past two.
func TestPaginationStopsAtTwoPages(t *testing.T) {
	var items []map[string]any
	for i := range 30 {
		items = append(items, map[string]any{
			"name": fmt.Sprintf("sub-%d.zip", i), "language": "EN", "url": fmt.Sprintf("/subtitle/%d.zip", i),
			"subtitlePage": fmt.Sprintf("/s/info/%d", i), "releases": []string{"Inception.2010.720p.BluRay.x264-REWARD"},
		})
	}
	page := func(n int) []byte {
		for i := range items {
			items[i]["subtitlePage"] = fmt.Sprintf("/s/info/%d/%d", n, i)
		}
		b, _ := json.Marshal(map[string]any{"status": true, "subtitles": items, "totalPages": 5})
		return b
	}
	f := &fakeSubDL{search: func(q url.Values) (int, []byte) {
		n := 1
		if q.Get("page") != "" {
			_, _ = fmt.Sscan(q.Get("page"), &n)
		}
		return http.StatusOK, page(n)
	}}
	_, p := f.start(t)

	cands, err := p.Search(context.Background(), inception())
	require.NoError(t, err)
	assert.Len(t, f.recorded(), 2)
	assert.Len(t, cands, 60)
}

// The server's bazarr_policy steers the search: max_pages 1 stops the
// second page, and enabled=false returns nothing.
func TestTheServersPolicyIsHonoured(t *testing.T) {
	f := &fakeSubDL{search: always(http.StatusOK, []byte(`{"status":true,"subtitles":[{"name":"x.zip","language":"EN","url":"/subtitle/x.zip"}],"totalPages":1,"bazarr_policy":{"enabled":false}}`))}
	_, p := f.start(t)

	cands, err := p.Search(context.Background(), breakingBadS01E05())
	require.NoError(t, err)
	assert.Empty(t, cands)
	assert.Len(t, f.recorded(), 1, "no fallback searches once the server disables the integration")
}

func TestDownloadFromAnArchive(t *testing.T) {
	f := &fakeSubDL{
		search: always(http.StatusOK, fixture(t, "movie_search.json")),
		files: map[string][]byte{
			"/subtitle/12345-67890.zip": subarchivetest.Zip(t,
				"__MACOSX/._Inception.srt", "junk",
				"Inception.2010.720p.BluRay.x264-REWARD.srt", "1\n00:00:01,000 --> 00:00:02,000\nDream.\n"),
		},
	}
	_, p := f.start(t)
	cands, err := p.Search(context.Background(), inception())
	require.NoError(t, err)

	var c subtitles.Candidate
	for _, x := range cands {
		if x.ID == "SUBDL::inception-english-12345.zip" {
			c = x
		}
	}
	raw, name, err := p.Download(context.Background(), c)
	require.NoError(t, err)
	assert.Contains(t, string(raw), "Dream.")
	assert.Equal(t, "Inception.2010.720p.BluRay.x264-REWARD.srt", name)
}

// A server-unpacked member is a bare subtitle file, not an archive.
func TestDownloadAnUnpackedMember(t *testing.T) {
	f := &fakeSubDL{
		search: always(http.StatusOK, fixture(t, "episode_search.json")),
		files:  map[string][]byte{"/subtitle/555-2-e05-sdh.srt": []byte("1\n00:00:01,000 --> 00:00:02,000\n[SIREN]\n")},
	}
	_, p := f.start(t)
	c := candidateByID(t, p, breakingBadS01E05(), "SUBDL::breaking-bad-season-1.zip/sdh5")

	raw, name, err := p.Download(context.Background(), c)
	require.NoError(t, err)
	assert.Contains(t, string(raw), "[SIREN]")
	assert.Equal(t, "555-2-e05-sdh.srt", name)
}

// A pack without unpacked members is downloaded whole and the episode is
// taken from it by name (a RAR here: SubDL serves some under .zip names).
func TestDownloadTakesTheEpisodeFromAPack(t *testing.T) {
	f := &fakeSubDL{
		search: always(http.StatusOK, fixture(t, "episode_search.json")),
		files: map[string][]byte{"/subtitle/555-4.zip": subarchivetest.Rar(t,
			"Breaking.Bad.S01E07.srt", "seven",
			"Breaking.Bad.S01E05.srt", "five",
			"Breaking.Bad.S01E06.srt", "six")},
	}
	_, p := f.start(t)
	c := candidateByID(t, p, breakingBadS01E05(), "SUBDL::breaking-bad-e05-e07.zip")

	raw, name, err := p.Download(context.Background(), c)
	require.NoError(t, err)
	assert.Equal(t, "five", string(raw))
	assert.Equal(t, "Breaking.Bad.S01E05.srt", name)
}

// One candidate's defect is not the provider's: a pack without the episode
// and a dead link are plain errors, never a ProviderError the caller would
// throttle the whole provider on.
func TestPerCandidateFailuresAreNotProviderErrors(t *testing.T) {
	f := &fakeSubDL{
		search: always(http.StatusOK, fixture(t, "episode_search.json")),
		files:  map[string][]byte{"/subtitle/555-4.zip": subarchivetest.Zip(t, "a.S01E06.srt", "six", "b.S01E07.srt", "seven")},
	}
	_, p := f.start(t)
	pack := candidateByID(t, p, breakingBadS01E05(), "SUBDL::breaking-bad-e05-e07.zip")
	single := candidateByID(t, p, breakingBadS01E05(), "SUBDL::breaking-bad-s01e05.zip") // its link 404s

	var pe *subtitles.ProviderError
	_, _, err := p.Download(context.Background(), pack)
	require.ErrorIs(t, err, subdl.ErrNoSubtitle)
	assert.False(t, errors.As(err, &pe))

	_, _, err = p.Download(context.Background(), single)
	require.ErrorIs(t, err, subdl.ErrRejected)
	assert.False(t, errors.As(err, &pe))
}

// A download link on another host is refused rather than followed.
func TestDownloadRefusesALinkOffTheDownloadHost(t *testing.T) {
	_, p := (&fakeSubDL{search: always(http.StatusOK, nil)}).start(t)
	_, _, err := p.Download(context.Background(), subtitles.Candidate{FetchID: `{"l":"https://evil.example/x.zip"}`})
	require.ErrorIs(t, err, subdl.ErrRejected)
}

func candidateByID(t *testing.T, p *subdl.Provider, q subtitles.Query, id string) subtitles.Candidate {
	t.Helper()
	cands, err := p.Search(context.Background(), q)
	require.NoError(t, err)
	for _, c := range cands {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("no candidate %q", id)
	return subtitles.Candidate{}
}

// Bazarr's checked() status mapping, less the inline sleeps.
func TestStatusMapping(t *testing.T) {
	for _, tc := range []struct {
		name       string
		code       int
		header     map[string]string
		body       string
		kind       string
		throttle   time.Duration // what subtitles.ThrottleFor makes of it; 0 = do not check
		retryAfter time.Duration
	}{
		{"402 paid subscription", http.StatusPaymentRequired, nil, `{"message":"Active paid SubDL subscription required"}`, subtitles.KindConfig, time.Hour, 0},
		// Verified live 2026-09-23: this is what a missing or bad key gets.
		{"403 key verdict", http.StatusForbidden, nil, `{"status":false,"statusCode":403,"error":"not_authorized","message":"Not Authorized"}`, subtitles.KindAuth, 12 * time.Hour, 0},
		{"403 from the edge", http.StatusForbidden, nil, `<html>Just a moment...</html>`, subtitles.KindAPIThrottled, 15 * time.Minute, 0},
		{"403 with an empty verdict", http.StatusForbidden, nil, `{}`, subtitles.KindAPIThrottled, 15 * time.Minute, 0},
		{"429 rate limit with Retry-After", http.StatusTooManyRequests, map[string]string{"Retry-After": "7"}, `{"error":"rate_limit"}`, subtitles.KindAPIThrottled, 7 * time.Second, 7 * time.Second},
		{"429 rate limit without", http.StatusTooManyRequests, nil, `{"error":"rate_limit"}`, subtitles.KindAPIThrottled, 15 * time.Minute, 0},
		{"429 service busy", http.StatusTooManyRequests, nil, `{"error":"service_busy"}`, subtitles.KindServiceUnavailable, 5 * time.Second, 5 * time.Second},
		{"429 unknown", http.StatusTooManyRequests, nil, `<html/>`, subtitles.KindAPIThrottled, 15 * time.Minute, 0},
		{"500", http.StatusInternalServerError, nil, `{}`, subtitles.KindServiceUnavailable, 20 * time.Minute, 0},
		{"search route missing", http.StatusNotFound, nil, `Not found`, subtitles.KindServiceUnavailable, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeSubDL{search: func(url.Values) (int, []byte) { return tc.code, []byte(tc.body) }}
			srv, _ := f.start(t)
			srv.Config.Handler = withHeaders(srv.Config.Handler, tc.header)
			p := subdl.New(subdl.Config{APIKey: apiKey, Endpoint: srv.URL + "/api/v1"})

			_, err := p.Search(context.Background(), inception())
			var pe *subtitles.ProviderError
			require.ErrorAs(t, err, &pe)
			assert.Equal(t, tc.kind, pe.Kind)
			assert.Equal(t, tc.retryAfter, pe.RetryAfter)
			if tc.throttle != 0 {
				_, d := subtitles.ThrottleFor("subdl", err)
				assert.Equal(t, tc.throttle, d)
			}
		})
	}
}

// The daily quota resets at midnight GMT; Bazarr benches subdl until an
// hour past it.
func TestTheDailyLimitLastsUntilAnHourPastMidnightUTC(t *testing.T) {
	for _, errCode := range []string{"daily_limit", "api_download_limit_exceeded"} {
		f := &fakeSubDL{search: always(http.StatusTooManyRequests, []byte(`{"error":"`+errCode+`"}`))}
		_, p := f.start(t)

		before := time.Now().UTC()
		_, err := p.Search(context.Background(), inception())
		var pe *subtitles.ProviderError
		require.ErrorAs(t, err, &pe)
		assert.True(t, subtitles.IsQuotaExceeded(err))
		midnight := time.Date(before.Year(), before.Month(), before.Day()+1, 0, 0, 0, 0, time.UTC)
		assert.True(t, pe.ResetAt.Equal(midnight), "reset at %s", pe.ResetAt)
		assert.WithinDuration(t, midnight.Add(time.Hour), before.Add(pe.RetryAfter), 5*time.Second)
	}
}

// A 4xx that is about this one search is absorbed, as Bazarr's
// SubdlRequestRejected is: it must not bench the provider.
func TestARejectedSearchIsAbsorbed(t *testing.T) {
	f := &fakeSubDL{search: always(http.StatusBadRequest, []byte(`{"error":"Film name contains potentially unsafe characters"}`))}
	_, p := f.start(t)

	cands, err := p.Search(context.Background(), inception())
	require.NoError(t, err)
	assert.Empty(t, cands)
}

// SubDL takes the key in the query string, so it is in every request URL,
// and Go's *url.Error quotes the URL -- into an error captionarr writes
// into a SubtitleRequest's status. It must be redacted, and the error must
// stay a *url.Error so the caller still sees a transport failure.
func TestTheAPIKeyNeverReachesAnError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	endpoint := srv.URL + "/api/v1"
	srv.Close() // connection refused from here on

	p := subdl.New(subdl.Config{APIKey: apiKey, Endpoint: endpoint})
	_, err := p.Search(context.Background(), inception())
	require.Error(t, err)
	assert.NotContains(t, err.Error(), apiKey)
	assert.Contains(t, err.Error(), "api_key=<redacted>")
	var ue *url.Error
	assert.True(t, errors.As(err, &ue))
}

func TestAMissingAPIKeyIsAConfigError(t *testing.T) {
	_, err := subdl.New(subdl.Config{}).Search(context.Background(), inception())
	var pe *subtitles.ProviderError
	require.ErrorAs(t, err, &pe)
	assert.Equal(t, subtitles.KindConfig, pe.Kind)
}

func TestOversizedResponsesAreRefused(t *testing.T) {
	huge := []byte(`{"padding":"` + strings.Repeat("a", 5<<20) + `"}`)
	f := &fakeSubDL{search: always(http.StatusOK, huge)}
	_, p := f.start(t)
	_, err := p.Search(context.Background(), inception())
	require.ErrorIs(t, err, subdl.ErrResponseTooLarge)

	f = &fakeSubDL{search: always(http.StatusOK, nil), files: map[string][]byte{"/x.srt": make([]byte, 33<<20)}}
	_, p = f.start(t)
	_, _, err = p.Download(context.Background(), subtitles.Candidate{FetchID: `{"l":"/x.srt","d":true}`})
	require.ErrorIs(t, err, subdl.ErrResponseTooLarge)
}

// Bazarr's HI and forced heuristics, including the false positives its
// word-boundary rewrite fixed.
func TestHearingImpairedAndForcedHeuristics(t *testing.T) {
	for _, tc := range []struct {
		name, comment string
		hiFlag        bool
		hi, forced    bool
	}{
		{"release group Hive is not HI", "Hive-CM8 release", false, false, false},
		{"Hi10P is not HI", "Hi10P encode", false, false, false},
		{"Hindi is not HI", "Hindi dub", false, false, false},
		{"SDH is HI", "Show.S01E01.SDH", false, true, false},
		{"closed captions are HI", "closed-captions", false, true, false},
		{"an explicit non-HI marker beats the API flag", "HI removed", true, false, false},
		{"the API flag alone", "", true, true, false},
		{"foreign parts are forced", "Foreign.Parts.Only", false, false, true},
		{"foreign alone is not forced", "foreign film", false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(map[string]any{"status": true, "totalPages": 1, "subtitles": []map[string]any{{
				"name": "x.zip", "language": "EN", "url": "/subtitle/x.zip", "comment": tc.comment, "hi": tc.hiFlag,
			}}})
			f := &fakeSubDL{search: always(http.StatusOK, body)}
			_, p := f.start(t)
			q := inception()
			if tc.forced {
				q.Languages = []subtitles.LangKey{"en:forced"}
			}
			cands, err := p.Search(context.Background(), q)
			require.NoError(t, err)
			require.Len(t, cands, 1)
			assert.Equal(t, tc.hi, cands[0].HI, "hi")
			assert.Equal(t, tc.forced, cands[0].Forced, "forced")
		})
	}
}

func TestCapabilitiesLanguages(t *testing.T) {
	langs := subdl.New(subdl.Config{}).Capabilities().Languages
	for tag, want := range map[string]bool{
		"en": true, "pt-BR": true, "pt": true, "zh-TW": true, "zh-Hant": true, "zh": true, "tl": true,
		"es-MX": false, "xx": false, "": false,
	} {
		assert.Equal(t, want, langs(tag), tag)
	}
}

func withHeaders(h http.Handler, headers map[string]string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for k, v := range headers {
			w.Header().Set(k, v)
		}
		h.ServeHTTP(w, r)
	})
}
