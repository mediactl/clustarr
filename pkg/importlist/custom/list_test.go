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

package custom_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/importlist"
	"github.com/mediactl/clustarr/pkg/importlist/custom"
)

// mdblistFeed is three items of the owner's "MAL Anime Series" list as
// mdblist.com's RSS served them (2026-10-21), the feed's token redacted.
const mdblistFeed = "../../../test/data/importlist/custom/mdblist-mal-anime-series.rss"

func serveFile(t *testing.T, path string) *httptest.Server {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
		_, _ = w.Write(b)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func rssList(kind commonv1.MediaKind, url string) *custom.List {
	return custom.New("mal-anime-series", kind, importlist.CustomConfig{URL: url, Format: importlist.CustomListFormatRSS})
}

// An mdblist item names its show by TMDB id in its guid (show:62564) and
// links IMDb, Trakt, TMDB and MAL in its description; its title ends in
// the year.
func TestAnMdblistFeedListsEachShowWithItsIDsAndYear(t *testing.T) {
	srv := serveFile(t, mdblistFeed)
	l := rssList(commonv1.MediaKindSeries, srv.URL+"/lists/appkins-so3yo0/mal-anime-series?rss=REDACTED")
	assert.Equal(t, "mal-anime-series", l.Name())
	assert.Equal(t, commonv1.MediaKindSeries, l.Kind())

	items, err := l.Fetch(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []importlist.Item{
		{Title: "Sound! Euphonium", Year: 2015, ExternalIDs: importlist.ExternalIDs{TMDB: "62564", IMDb: "tt4384306"}},
		{Title: "KONOSUBA - God's blessing on this wonderful world!", Year: 2016, ExternalIDs: importlist.ExternalIDs{TMDB: "65844", IMDb: "tt5370118"}},
		{Title: "Mobile Suit Gundam: Iron-Blooded Orphans", Year: 2015, ExternalIDs: importlist.ExternalIDs{TMDB: "64375", IMDb: "tt5344382"}},
	}, items)
}

// A show's TMDB id is not a movie's: a movie list keeps none of them.
func TestAMovieListKeepsNoShowFromTheFeed(t *testing.T) {
	srv := serveFile(t, mdblistFeed)
	items, err := rssList(commonv1.MediaKindMovie, srv.URL).Fetch(t.Context())
	require.NoError(t, err)
	assert.Empty(t, items)
}

func TestAFeedThatIsNotRSSFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html><body>Sign in</body></html>`))
	}))
	defer srv.Close()
	_, err := rssList(commonv1.MediaKindSeries, srv.URL).Fetch(t.Context())
	require.Error(t, err)
}

func TestAFeedAnsweringAnErrorStatusFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	_, err := rssList(commonv1.MediaKindSeries, srv.URL).Fetch(t.Context())
	require.ErrorContains(t, err, "403")
}

func TestAFeedPastTheCapIsRefused(t *testing.T) {
	big := bytes.Repeat([]byte(" "), custom.MaxFeedBytes+1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<rss version="2.0"><channel>`))
		_, _ = w.Write(big)
		_, _ = w.Write([]byte(`</channel></rss>`))
	}))
	defer srv.Close()
	_, err := rssList(commonv1.MediaKindSeries, srv.URL).Fetch(t.Context())
	require.ErrorIs(t, err, custom.ErrResponseTooLarge)
}

// plexFeed is a Plex friends' watchlist feed (https://rss.plex.tv/<uuid>).
// Its envelope is the owner's feed as Plex served it on 2026-11-17, the
// uuid and name changed; that feed held no item then, so the items are
// written to the shape Radarr's and Sonarr's PlexRssImportParser read: a
// category of movie or show, and one guid per id (imdb://, tmdb://,
// tvdb://).
const plexFeed = "../../../test/data/importlist/custom/plex-friends-watchlist.rss"

// A Plex item names its ids in its guids and its kind in its category, so
// one feed of movies and shows serves a list of each.
func TestAPlexWatchlistFeedListsEachKindWithItsIDs(t *testing.T) {
	srv := serveFile(t, plexFeed)

	movies, err := rssList(commonv1.MediaKindMovie, srv.URL).Fetch(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []importlist.Item{
		{Title: "Dune", Year: 2021, ExternalIDs: importlist.ExternalIDs{IMDb: "tt1160419"}},
		{Title: "Arrival", Year: 2016, ExternalIDs: importlist.ExternalIDs{IMDb: "tt2543164", TMDB: "329865"}},
	}, movies)

	series, err := rssList(commonv1.MediaKindSeries, srv.URL).Fetch(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []importlist.Item{
		{Title: "Severance", Year: 2022, ExternalIDs: importlist.ExternalIDs{TVDB: "371980"}},
		{Title: "Slow Horses", ExternalIDs: importlist.ExternalIDs{TVDB: "371572", IMDb: "tt5875444"}},
	}, series)
}

// A category is a kind only beside a Plex guid: another feed's category is
// a genre, and an item named "movie" there is still of no known kind.
func TestACategoryIsAKindOnlyOnAPlexItem(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<rss version="2.0"><channel><item>
			<title>Sound! Euphonium (2015)</title>
			<link>https://www.imdb.com/title/tt4384306/</link>
			<category>movie</category>
		</item></channel></rss>`))
	}))
	t.Cleanup(srv.Close)
	items, err := rssList(commonv1.MediaKindSeries, srv.URL).Fetch(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []importlist.Item{
		{Title: "Sound! Euphonium", Year: 2015, ExternalIDs: importlist.ExternalIDs{IMDb: "tt4384306"}},
	}, items)
}

// mdblist's feed URL carries the list's token as ?rss=, and http.Client.Do's
// *url.Error quotes the URL whole.
func TestAnUnreachableFeedsErrorCarriesNoToken(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL + "/lists/x/y?rss=s3cr3ttoken"
	srv.Close()
	_, err := rssList(commonv1.MediaKindSeries, url).Fetch(t.Context())
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "s3cr3ttoken")
}

// mdblistJSON is seven items of the owner's public "The 700+ Club:
// Deep-Cut Anime" list as https://mdblist.com/lists/<user>/<slug>/json
// served them (2026-10-28): its first six shows and the one with no TheTVDB
// or IMDb id. A public list needs no token.
const mdblistJSON = "../../../test/data/importlist/custom/mdblist-the-700-club-deep-cut-anime.json"

func jsonList(kind commonv1.MediaKind, url string) *custom.List {
	return custom.New("the-700-club-deep-cut-anime", kind, importlist.CustomConfig{URL: url, Format: importlist.CustomListFormatJSON})
}

// An mdblist list's JSON names each show's TMDB id (id), TheTVDB id
// (tvdbid), IMDb id and first year, so no resolver is asked for a TheTVDB
// id it already has; a show with none keeps its TMDB id for the resolver.
func TestAnMdblistJSONListsEachShowWithItsIDs(t *testing.T) {
	srv := serveFile(t, mdblistJSON)
	items, err := jsonList(commonv1.MediaKindSeries, srv.URL+"/lists/appkins-so3yo0/the-700-club-deep-cut-anime/json").Fetch(t.Context())
	require.NoError(t, err)
	require.Len(t, items, 7)
	assert.Equal(t, importlist.Item{Title: "Great Pretender", Year: 2020, ExternalIDs: importlist.ExternalIDs{TMDB: "93816", TVDB: "376144", IMDb: "tt11680468"}}, items[0])
	assert.Equal(t, importlist.Item{Title: "Magi", Year: 2012, ExternalIDs: importlist.ExternalIDs{TMDB: "60654", TVDB: "262094", IMDb: "tt2425098"}}, items[5])
	assert.Equal(t, importlist.Item{Title: "New PANTY & STOCKING with GARTERBELT", Year: 2025, ExternalIDs: importlist.ExternalIDs{TMDB: "246862"}}, items[6])
}

// A show's TMDB id is not a movie's: a movie list keeps none of them.
func TestAMovieListKeepsNoShowFromTheJSON(t *testing.T) {
	srv := serveFile(t, mdblistJSON)
	items, err := jsonList(commonv1.MediaKindMovie, srv.URL).Fetch(t.Context())
	require.NoError(t, err)
	assert.Empty(t, items)
}

// A private list answers 200 with {"error": "This List is Private"}, as the
// owner's did until they made it public: that is a failure naming the
// reason, never an empty list (which would read as every item removed).
func TestAPrivateMdblistJSONFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"error": "This List is Private"}`))
	}))
	t.Cleanup(srv.Close)
	items, err := jsonList(commonv1.MediaKindSeries, srv.URL).Fetch(t.Context())
	assert.Nil(t, items)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "This List is Private")

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`<html>`)) }))
	t.Cleanup(bad.Close)
	_, err = jsonList(commonv1.MediaKindSeries, bad.URL).Fetch(t.Context())
	require.Error(t, err, "a body that is not JSON")
}
