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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/subtitles"
	"github.com/mediactl/clustarr/pkg/subtitles/providers/gestdown"
)

func TestSearchByTVDBSeasonEpisodeLanguage(t *testing.T) {
	fixture, err := os.ReadFile("../../../../testdata/subtitles/gestdown/search.json")
	require.NoError(t, err)

	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write(fixture)
	}))
	defer srv.Close()

	p := gestdown.New(gestdown.Config{Endpoint: srv.URL})
	cands, err := p.Search(context.Background(), subtitles.Query{
		Kind: "episode", IDs: map[string]string{"tvdb": "12345"}, Season: 1, Episode: 1,
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
	assert.Contains(t, gotPath, "12345")
	assert.Contains(t, gotPath, "1/1")
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
	// research note §4.2: "423 = refreshing, retry in 30s".
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
