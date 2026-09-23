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

package tvdb_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"

	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/tvdb"
)

func TestSeriesLogsInOnceAndReusesTheToken(t *testing.T) {
	login, _ := os.ReadFile("../../../../testdata/metadata/tvdb/login.json")
	series, _ := os.ReadFile("../../../../testdata/metadata/tvdb/series_121361.json")
	var logins int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/login":
			atomic.AddInt32(&logins, 1)
			_, _ = w.Write(login)
		case r.URL.Path == "/series/121361/extended":
			require.Equal(t, "Bearer eyJhbGciOiJIUzI1NiJ9.test-payload.test-signature", r.Header.Get("Authorization"))
			_, _ = w.Write(series)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	c := tvdb.New("test-key", "test-pin", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))

	s1, err := c.Series(context.Background(), "121361")
	require.NoError(t, err)
	require.Equal(t, "Game of Thrones", s1.Title)
	require.Equal(t, "tt0944947", s1.IDs[metadata.KeyIMDb])
	require.Equal(t, metadata.SeriesStatusEnded, s1.Status)

	_, err = c.Series(context.Background(), "121361")
	require.NoError(t, err)
	require.EqualValues(t, 1, atomic.LoadInt32(&logins), "the second call must reuse the cached token, not log in again")
}

func TestEpisodesUsesTheRequestedSeasonOrder(t *testing.T) {
	login, _ := os.ReadFile("../../../../testdata/metadata/tvdb/login.json")
	episodes, _ := os.ReadFile("../../../../testdata/metadata/tvdb/episodes_121361_default.json")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			_, _ = w.Write(login)
		case "/series/121361/episodes/default":
			_, _ = w.Write(episodes)
		default:
			t.Fatalf("unexpected request: %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	c := tvdb.New("test-key", "test-pin", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))

	eps, err := c.Episodes(context.Background(), "121361", "default")

	require.NoError(t, err)
	require.Len(t, eps, 1)
	require.Equal(t, "Winter Is Coming", eps[0].Title)
	require.EqualValues(t, 1, eps[0].SeasonNumber)
	require.EqualValues(t, 1, *eps[0].AbsoluteNumber)
}

// episodesPage renders one page of TheTVDB v4's episode-list envelope: the
// episodes named, in season n+1, and links.next set when more follows.
func episodesPage(t *testing.T, n int, next bool, names ...string) []byte {
	t.Helper()
	eps := make([]map[string]any, 0, len(names))
	for i, name := range names {
		eps = append(eps, map[string]any{"name": name, "seasonNumber": n + 1, "number": i + 1, "aired": "1990-01-01"})
	}
	links := map[string]any{"self": fmt.Sprintf("/series/71663/episodes/official?page=%d", n), "next": nil, "page_size": 2}
	if next {
		links["next"] = fmt.Sprintf("/series/71663/episodes/official?page=%d", n+1)
	}
	body, err := json.Marshal(map[string]any{"data": map[string]any{"episodes": eps}, "links": links})
	require.NoError(t, err)
	return body
}

// TheTVDB v4 pages an episode list (links.page_size, 500 in production) and
// names the next page in links.next; a series with more episodes than one
// page -- The Simpsons, Mister Rogers' Neighborhood -- was cut off at the
// first. Every page is fetched, in order, by asking for ?page=N until next
// is null.
func TestEpisodesFollowsEveryPage(t *testing.T) {
	login, _ := os.ReadFile("../../../../testdata/metadata/tvdb/login.json")
	var pages []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			_, _ = w.Write(login)
		case "/series/71663/episodes/official":
			page := r.URL.Query().Get("page")
			pages = append(pages, page)
			switch page {
			case "0":
				_, _ = w.Write(episodesPage(t, 0, true, "Simpsons Roasting on an Open Fire", "Bart the Genius"))
			case "1":
				_, _ = w.Write(episodesPage(t, 1, true, "Homer's Odyssey", "There's No Disgrace Like Home"))
			case "2":
				_, _ = w.Write(episodesPage(t, 2, false, "Bart the General"))
			default:
				t.Errorf("unexpected page %q", page)
			}
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	c := tvdb.New("test-key", "test-pin", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))

	eps, err := c.Episodes(context.Background(), "71663", "official")

	require.NoError(t, err)
	titles := make([]string, 0, len(eps))
	for _, ep := range eps {
		titles = append(titles, ep.Title)
	}
	require.Equal(t, []string{
		"Simpsons Roasting on an Open Fire", "Bart the Genius", "Homer's Odyssey",
		"There's No Disgrace Like Home", "Bart the General",
	}, titles)
	require.EqualValues(t, 3, eps[4].SeasonNumber)
	require.Equal(t, []string{"0", "1", "2"}, pages, "each page fetched once, in order")
}

// A page that names a next page yet carries no episodes ends the walk:
// nothing more is coming, and following it would spend the limiter's budget
// against TheTVDB on empty pages.
func TestEpisodesStopsAtAnEmptyPage(t *testing.T) {
	login, _ := os.ReadFile("../../../../testdata/metadata/tvdb/login.json")
	var pages []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			_, _ = w.Write(login)
		case "/series/71663/episodes/official":
			page := r.URL.Query().Get("page")
			pages = append(pages, page)
			switch page {
			case "0":
				_, _ = w.Write(episodesPage(t, 0, true, "Simpsons Roasting on an Open Fire"))
			case "1":
				_, _ = w.Write(episodesPage(t, 1, true))
			default:
				t.Errorf("walked past the empty page to %q", page)
			}
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	c := tvdb.New("test-key", "test-pin", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))

	eps, err := c.Episodes(context.Background(), "71663", "official")

	require.NoError(t, err)
	require.Len(t, eps, 1)
	require.Equal(t, []string{"0", "1"}, pages)
}

func TestUpdatesReturnsRecordIDsSinceTheGivenTime(t *testing.T) {
	login, _ := os.ReadFile("../../../../testdata/metadata/tvdb/login.json")
	updates, _ := os.ReadFile("../../../../testdata/metadata/tvdb/updates_since.json")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			_, _ = w.Write(login)
		case "/updates":
			require.Equal(t, "1700000000", r.URL.Query().Get("since"))
			_, _ = w.Write(updates)
		default:
			t.Fatalf("unexpected request: %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	c := tvdb.New("test-key", "test-pin", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))

	ids, err := c.Updates(context.Background(), time.Unix(1700000000, 0))

	require.NoError(t, err)
	require.ElementsMatch(t, []string{"121361", "3254641"}, ids)
}

func TestSeriesRejectsMalformedResponseBodies(t *testing.T) {
	login, err := os.ReadFile("../../../../testdata/metadata/tvdb/login.json")
	require.NoError(t, err)

	tests := []struct {
		name string
		body string
	}{
		{"empty body", ""},
		{"truncated JSON", `{"id": 1, "title": "Hea`},
		{"garbage bytes", "not json at all {{{"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/login":
					_, _ = w.Write(login)
				case "/series/121361/extended":
					_, _ = w.Write([]byte(tt.body))
				default:
					t.Fatalf("unexpected request: %s", r.URL.Path)
				}
			}))
			defer srv.Close()
			c := tvdb.New("test-key", "test-pin", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))

			var s *metadata.Series
			require.NotPanics(t, func() {
				s, err = c.Series(context.Background(), "121361")
			})

			require.Nil(t, s)
			require.Error(t, err)
			require.ErrorIs(t, err, metadata.ErrDecode)
		})
	}
}

// TVDB reports a series' original language as ISO 639-3 -- the real fixture
// says "eng" -- and every consumer of Series.OriginalLanguage reads BCP-47. The
// client used to pass "eng" straight through, so pkg/decision could not match
// it against a release's language and failed open with a warning on every
// evaluation: language conditions were silently inert for every TVDB series.
func TestSeriesOriginalLanguageIsBCP47(t *testing.T) {
	login, _ := os.ReadFile("../../../../testdata/metadata/tvdb/login.json")
	series, _ := os.ReadFile("../../../../testdata/metadata/tvdb/series_121361.json")
	require.Contains(t, string(series), `"originalLanguage": "eng"`,
		"the fixture must carry TVDB's real ISO 639-3 form, or this test proves nothing")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/login":
			_, _ = w.Write(login)
		case r.URL.Path == "/series/121361/extended":
			_, _ = w.Write(series)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	c := tvdb.New("test-key", "test-pin", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))
	s, err := c.Series(context.Background(), "121361")
	require.NoError(t, err)
	require.Equal(t, "en", s.OriginalLanguage)
}

func oversized(w http.ResponseWriter, prefix string) {
	_, _ = w.Write([]byte(prefix))
	_, _ = w.Write(bytes.Repeat([]byte("x"), int(metadata.MaxResponseBytes)))
	_, _ = w.Write([]byte(`"}}`))
}

func TestSeriesRejectsAnOversizedBody(t *testing.T) {
	login, err := os.ReadFile("../../../../testdata/metadata/tvdb/login.json")
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" {
			_, _ = w.Write(login)
			return
		}
		oversized(w, `{"data":{"name":"`)
	}))
	defer srv.Close()
	c := tvdb.New("test-key", "", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))

	_, err = c.Series(context.Background(), "121361")

	require.ErrorIs(t, err, metadata.ErrResponseTooLarge)
	require.NotErrorIs(t, err, metadata.ErrDecode)
}

func TestLoginRejectsAnOversizedBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/login", r.URL.Path, "no request may follow a failed login")
		oversized(w, `{"data":{"token":"`)
	}))
	defer srv.Close()
	c := tvdb.New("test-key", "", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))

	_, err := c.Series(context.Background(), "121361")

	require.ErrorIs(t, err, metadata.ErrResponseTooLarge)
}

// A series' image and artworks reach the normalized model as Images -- the
// series' own image first, as its poster, then each artwork by TheTVDB's
// type (2 poster, 3 background, 1 banner) -- so status.metadata.images has
// a poster for the library page to show. The recording carries none.
func TestSeriesMapsItsImageAndArtworksIntoImages(t *testing.T) {
	login, _ := os.ReadFile("../../../../testdata/metadata/tvdb/login.json")
	recorded, err := os.ReadFile("../../../../testdata/metadata/tvdb/series_121361.json")
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(recorded, &doc))
	data, ok := doc["data"].(map[string]any)
	require.True(t, ok)
	data["image"] = "https://artworks.thetvdb.com/banners/posters/121361-1.jpg"
	data["artworks"] = []map[string]any{
		{"type": 3, "image": "https://artworks.thetvdb.com/banners/fanart/original/121361-2.jpg"},
		{"type": 2, "image": "https://artworks.thetvdb.com/banners/posters/121361-3.jpg"},
		{"type": 1, "image": "https://artworks.thetvdb.com/banners/graphical/121361-g.jpg"},
		{"type": 99, "image": "https://artworks.thetvdb.com/banners/unknown.jpg"},
	}
	body, err := json.Marshal(doc)
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			_, _ = w.Write(login)
		case "/series/121361/extended":
			_, _ = w.Write(body)
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	c := tvdb.New("test-key", "test-pin", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))

	s, err := c.Series(context.Background(), "121361")
	require.NoError(t, err)
	require.Equal(t, []metadata.Image{
		{Type: metadata.ImageTypePoster, URL: "https://artworks.thetvdb.com/banners/posters/121361-1.jpg"},
		{Type: metadata.ImageTypeFanart, URL: "https://artworks.thetvdb.com/banners/fanart/original/121361-2.jpg"},
		{Type: metadata.ImageTypePoster, URL: "https://artworks.thetvdb.com/banners/posters/121361-3.jpg"},
		{Type: metadata.ImageTypeBanner, URL: "https://artworks.thetvdb.com/banners/graphical/121361-g.jpg"},
	}, s.Images, "an artwork of an unknown type is left out")
}

// TestSeriesMapsItsAliasesIntoAlternateTitles: TheTVDB's aliases become the
// series' AlternateTitles, each with its language normalised to BCP 47 as
// OriginalLanguage is, the series' own name and a case-only repeat dropped
// (metadata.DistinctAltTitles), and no scene season. The fixture's aliases
// follow SeriesExtendedRecord.aliases' documented shape
// (docs/research/metadata.md §2.2); they are not a recorded live response.
func TestSeriesMapsItsAliasesIntoAlternateTitles(t *testing.T) {
	login, _ := os.ReadFile("../../../../testdata/metadata/tvdb/login.json")
	series, err := os.ReadFile("../../../../testdata/metadata/tvdb/series_121361.json")
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			_, _ = w.Write(login)
		case "/series/121361/extended":
			_, _ = w.Write(series)
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	c := tvdb.New("test-key", "test-pin", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))

	s, err := c.Series(context.Background(), "121361")
	require.NoError(t, err)
	require.Equal(t, []metadata.AltTitle{
		{Title: "GoT", Language: "en"},
		{Title: "Juego de tronos", Language: "es"},
		{Title: "Le Trône de fer", Language: "fr"},
	}, s.AlternateTitles)
}
