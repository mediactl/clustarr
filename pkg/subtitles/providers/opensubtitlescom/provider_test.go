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
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/subtitles"
	"github.com/mediactl/clustarr/pkg/subtitles/providers/opensubtitlescom"
)

func newTestServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("../../../../testdata/subtitles/opensubtitles/" + name)
	require.NoError(t, err)
	return b
}

func TestSearchSendsMoviehashIDsLanguagesAndHIParams(t *testing.T) {
	var gotQuery url.Values
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			_, _ = w.Write(readFixture(t, "login.json"))
		case "/subtitles":
			gotQuery = r.URL.Query()
			_, _ = w.Write(readFixture(t, "search.json"))
		default:
			http.NotFound(w, r)
		}
	})

	p := opensubtitlescom.New(opensubtitlescom.Config{APIKey: "k", Username: "u", Password: "p", Endpoint: srv.URL})

	cands, err := p.Search(context.Background(), subtitles.Query{
		Kind: "movie", IDs: map[string]string{"imdb": "1375666", "tmdb": "27205"},
		Hash: "1606fd38140b6f23", Languages: []subtitles.LangKey{"en", "fr:forced", "de:hi"},
	})
	require.NoError(t, err)
	require.Len(t, cands, 1)

	c := cands[0]
	assert.Equal(t, "opensubtitlescom", c.Provider)
	assert.Equal(t, "998877", c.FetchID)
	assert.Equal(t, "en", c.Language)
	assert.False(t, c.HI)
	assert.Equal(t, "Inception.2010.720p.BluRay.x264-REWARD", c.ReleaseInfo)
	assert.True(t, c.Matches[subtitles.MatchHash], "moviehash_match:true must set the hash match")
	assert.True(t, c.Trusted)

	assert.Equal(t, "1375666", gotQuery.Get("imdb_id"))
	assert.Equal(t, "27205", gotQuery.Get("tmdb_id"))
	assert.Equal(t, "1606fd38140b6f23", gotQuery.Get("moviehash"))
	assert.Equal(t, "en,fr,de", gotQuery.Get("languages"))
	assert.Equal(t, "exclude", gotQuery.Get("ai_translated"))
}

func TestSearchMapsForcedAndHILangKeysToHearingImpairedAndForeignPartsOnly(t *testing.T) {
	var queries []url.Values
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			_, _ = w.Write(readFixture(t, "login.json"))
		case "/subtitles":
			queries = append(queries, r.URL.Query())
			_, _ = w.Write(readFixture(t, "search.json"))
		}
	})
	p := opensubtitlescom.New(opensubtitlescom.Config{APIKey: "k", Username: "u", Password: "p", Endpoint: srv.URL})

	_, err := p.Search(context.Background(), subtitles.Query{Kind: "movie", Languages: []subtitles.LangKey{"en:hi"}})
	require.NoError(t, err)
	_, err = p.Search(context.Background(), subtitles.Query{Kind: "movie", Languages: []subtitles.LangKey{"en:forced"}})
	require.NoError(t, err)

	require.Len(t, queries, 2)
	assert.Equal(t, "only", queries[0].Get("hearing_impaired"))
	assert.Equal(t, "exclude", queries[0].Get("foreign_parts_only"))
	assert.Equal(t, "only", queries[1].Get("foreign_parts_only"))
	assert.Equal(t, "exclude", queries[1].Get("hearing_impaired"))
}

func TestDownloadFetchesTheLinkedFileAndReturnsItsName(t *testing.T) {
	const srtBody = "1\n00:00:01,000 --> 00:00:02,000\nHello.\n"
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/login":
			_, _ = w.Write(readFixture(t, "login.json"))
		case r.URL.Path == "/download" && r.Method == http.MethodPost:
			fixture := strings.Replace(string(readFixture(t, "download.json")), "REPLACED_AT_TEST_TIME", "http://"+r.Host+"/dl/abcdefgh", 1)
			_, _ = w.Write([]byte(fixture))
		case r.URL.Path == "/dl/abcdefgh":
			_, _ = w.Write([]byte(srtBody))
		default:
			http.NotFound(w, r)
		}
	})
	p := opensubtitlescom.New(opensubtitlescom.Config{APIKey: "k", Username: "u", Password: "p", Endpoint: srv.URL})

	raw, name, err := p.Download(context.Background(), subtitles.Candidate{FetchID: "998877"})
	require.NoError(t, err)
	assert.Equal(t, srtBody, string(raw))
	assert.Equal(t, "Inception.2010.720p.BluRay.x264-REWARD.srt", name)
}

func TestDownloadReturnsATypedQuotaExceededErrorOn406(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			_, _ = w.Write(readFixture(t, "login.json"))
		case "/download":
			w.WriteHeader(http.StatusNotAcceptable)
			_, _ = w.Write(readFixture(t, "quota_exceeded_406.json"))
		}
	})
	p := opensubtitlescom.New(opensubtitlescom.Config{APIKey: "k", Username: "u", Password: "p", Endpoint: srv.URL})

	_, _, err := p.Download(context.Background(), subtitles.Candidate{FetchID: "998877"})
	require.Error(t, err)
	assert.True(t, subtitles.IsQuotaExceeded(err))
	reason, d := subtitles.ThrottleFor("opensubtitlescom", err)
	assert.Equal(t, subtitles.KindDownloadLimitExceeded, reason)
	assert.Equal(t, 6*time.Hour, d)
}

func TestDownloadReturnsATypedRateLimitedErrorOn429WithRetryAfter(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			_, _ = w.Write(readFixture(t, "login.json"))
		case "/download":
			w.Header().Set("Retry-After", "5")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"message":"Too many requests"}`))
		}
	})
	p := opensubtitlescom.New(opensubtitlescom.Config{APIKey: "k", Username: "u", Password: "p", Endpoint: srv.URL})

	_, _, err := p.Download(context.Background(), subtitles.Candidate{FetchID: "998877"})
	require.Error(t, err)
	assert.True(t, subtitles.IsRateLimited(err))
	_, d := subtitles.ThrottleFor("opensubtitlescom", err)
	assert.Equal(t, 5*time.Second, d, "an explicit Retry-After header must win over the 1-minute static override")
}
