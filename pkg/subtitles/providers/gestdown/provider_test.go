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

package gestdown_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/subtitles"
	"github.com/mediactl/clustarr/pkg/subtitles/providers/gestdown"
)

const resolvedShowID = "31ffb6ce-c000-4079-8912-b3f72057baed" // matches testdata/subtitles/gestdown/shows.json

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("../../../../testdata/subtitles/gestdown/" + name)
	require.NoError(t, err)
	return b
}

// twoStepServer serves the real two-request flow Search now performs: a
// show-id lookup (GET /shows/external/tvdb/{id}) followed by a subtitle
// search keyed on the resolved show id (GET /subtitles/get/{showId}/...).
// gotPaths records every request path in order, for tests that care which
// endpoint was hit and how many times.
func twoStepServer(t *testing.T, showsBody, searchBody []byte) (*httptest.Server, *[]string) {
	t.Helper()
	var gotPaths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPaths = append(gotPaths, r.URL.Path)
		switch {
		case strings.HasPrefix(r.URL.Path, "/shows/external/tvdb/"):
			_, _ = w.Write(showsBody)
		case strings.HasPrefix(r.URL.Path, "/subtitles/get/"):
			_, _ = w.Write(searchBody)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &gotPaths
}

func TestSearchResolvesTheShowIDThenSearchesSubtitlesByIt(t *testing.T) {
	srv, gotPaths := twoStepServer(t, readFixture(t, "shows.json"), readFixture(t, "search.json"))

	p := gestdown.New(gestdown.Config{Endpoint: srv.URL})
	cands, err := p.Search(context.Background(), subtitles.Query{
		Kind: "episode", IDs: map[string]string{"tvdb": "81189"}, Season: 1, Episode: 1,
		Languages: []subtitles.LangKey{"en"},
	})
	require.NoError(t, err)
	require.Len(t, cands, 1, "the incomplete subtitle (completed:false) must be filtered out, per Bazarr's gestdown.py")

	c := cands[0]
	assert.Equal(t, "gestdown", c.Provider)
	assert.Equal(t, "abc123", c.ID)
	assert.Equal(t, "/subtitles/download/abc123", c.FetchID, "FetchID carries the raw downloadUri — Download must GET it directly, not reconstruct a path (verified against Bazarr's gestdown.py: page_link = _BASE_URL + data[\"downloadUri\"])")
	assert.Equal(t, "DIMENSION", c.ReleaseInfo)
	assert.False(t, c.HI)

	require.Len(t, *gotPaths, 2, "must resolve the show id (verified live against api.gestdown.info) before searching subtitles by it")
	assert.Equal(t, "/shows/external/tvdb/81189", (*gotPaths)[0])
	assert.Equal(t, "/subtitles/get/"+resolvedShowID+"/1/1/en", (*gotPaths)[1])
}

func TestSearchCachesTheResolvedShowIDAcrossCalls(t *testing.T) {
	srv, gotPaths := twoStepServer(t, readFixture(t, "shows.json"), readFixture(t, "search.json"))

	p := gestdown.New(gestdown.Config{Endpoint: srv.URL})
	q := subtitles.Query{Kind: "episode", IDs: map[string]string{"tvdb": "81189"}, Season: 1, Episode: 1, Languages: []subtitles.LangKey{"en"}}

	_, err := p.Search(context.Background(), q)
	require.NoError(t, err)
	_, err = p.Search(context.Background(), q)
	require.NoError(t, err)

	showLookups := 0
	for _, path := range *gotPaths {
		if strings.HasPrefix(path, "/shows/external/tvdb/") {
			showLookups++
		}
	}
	assert.Equal(t, 1, showLookups, "the resolved show id must be cached for the Provider's lifetime, not re-looked-up on every Search")
}

func TestSearchReturnsNotFoundProviderErrorWhenShowLookupIs404(t *testing.T) {
	// Verified live against api.gestdown.info on 2026-09-18: a TVDB id it
	// has never indexed returns HTTP 404 with a bare JSON string body
	// ("Couldn't find show: 999999999"), not a JSON object.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/shows/external/tvdb/") {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`"Couldn't find show: 999999999"`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	p := gestdown.New(gestdown.Config{Endpoint: srv.URL})

	var cands []subtitles.Candidate
	var err error
	require.NotPanics(t, func() {
		cands, err = p.Search(context.Background(), subtitles.Query{
			Kind: "episode", IDs: map[string]string{"tvdb": "999999999"}, Season: 1, Episode: 1,
			Languages: []subtitles.LangKey{"en"},
		})
	})
	require.Error(t, err)
	assert.Nil(t, cands)
	assert.True(t, subtitles.IsNotFound(err), "a 404 on the show lookup must map to an ErrNotFound-class ProviderError")
}

func TestSearchReturnsAnErrorOnMalformedShowLookupResponsesWithoutPanicking(t *testing.T) {
	tests := []struct {
		name string
		body []byte
	}{
		{"empty body", []byte{}},
		{"truncated JSON", []byte(`{"shows":[{"id":"x"`)},
		{"garbage bytes", []byte{0x00, 0x01, 0xFF, 0xFE, 0x80}},
		{"empty shows list", []byte(`{"shows":[]}`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _ := twoStepServer(t, tt.body, readFixture(t, "search.json"))
			p := gestdown.New(gestdown.Config{Endpoint: srv.URL})

			var cands []subtitles.Candidate
			var err error
			require.NotPanics(t, func() {
				cands, err = p.Search(context.Background(), subtitles.Query{
					Kind: "episode", IDs: map[string]string{"tvdb": "81189"}, Season: 1, Episode: 1,
					Languages: []subtitles.LangKey{"en"},
				})
			})
			assert.Error(t, err)
			assert.Nil(t, cands)
		})
	}
}

func TestSearchReturnsAnErrorOnMalformedSubtitleSearchResponsesWithoutPanicking(t *testing.T) {
	tests := []struct {
		name string
		body []byte
	}{
		{"empty body", []byte{}},
		{"truncated JSON", []byte(`{"matchingSubtitles":[{"subtitleId":"x"`)},
		{"garbage bytes", []byte{0x00, 0x01, 0xFF, 0xFE, 0x80}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _ := twoStepServer(t, readFixture(t, "shows.json"), tt.body)
			p := gestdown.New(gestdown.Config{Endpoint: srv.URL})

			var cands []subtitles.Candidate
			var err error
			require.NotPanics(t, func() {
				cands, err = p.Search(context.Background(), subtitles.Query{
					Kind: "episode", IDs: map[string]string{"tvdb": "81189"}, Season: 1, Episode: 1,
					Languages: []subtitles.LangKey{"en"},
				})
			})
			assert.Error(t, err)
			assert.Nil(t, cands)
		})
	}
}

func TestDownloadFetchesTheDownloadURI(t *testing.T) {
	const srtBody = "1\n00:00:01,000 --> 00:00:02,000\nHi.\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/subtitles/download/abc123" {
			_, _ = w.Write([]byte(srtBody))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	p := gestdown.New(gestdown.Config{Endpoint: srv.URL})
	raw, name, err := p.Download(context.Background(), subtitles.Candidate{FetchID: "/subtitles/download/abc123", ReleaseInfo: "DIMENSION"})
	require.NoError(t, err)
	assert.Equal(t, srtBody, string(raw))
	assert.NotEmpty(t, name)
}

func TestSearchReturns423AsAThrottledServiceUnavailableError(t *testing.T) {
	// research note §4.2: "423 = refreshing, retry in 30s". The unconditional
	// 423 below fires on whichever request Search makes first (the show
	// lookup, under the new two-step flow), so this still exercises the
	// same mapping without needing to know the internal call order.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusLocked)
	}))
	defer srv.Close()

	p := gestdown.New(gestdown.Config{Endpoint: srv.URL})
	_, err := p.Search(context.Background(), subtitles.Query{
		Kind: "episode", IDs: map[string]string{"tvdb": "12345"}, Season: 1, Episode: 1,
		Languages: []subtitles.LangKey{"en"},
	})
	require.Error(t, err)
	reason, d := subtitles.ThrottleFor("gestdown", err)
	assert.Equal(t, subtitles.KindServiceUnavailable, reason)
	assert.Equal(t, 30*time.Second, d)
}

func TestSearchSkipsNonEpisodeKinds(t *testing.T) {
	// Gestdown is TV-only (research note §4.2); a movie Query must not hit
	// the network at all.
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()

	p := gestdown.New(gestdown.Config{Endpoint: srv.URL})
	cands, err := p.Search(context.Background(), subtitles.Query{Kind: "movie"})
	require.NoError(t, err)
	assert.Empty(t, cands)
	assert.False(t, called)
}
