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
	"errors"
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

func TestAJSONFeedIsStillNotImplemented(t *testing.T) {
	l := custom.New("custom-feed", commonv1.MediaKindMovie, importlist.CustomConfig{Format: importlist.CustomListFormatJSON})
	items, err := l.Fetch(t.Context())
	assert.Nil(t, items)
	var nie *importlist.NotImplementedError
	assert.True(t, errors.As(err, &nie))
	assert.Equal(t, "custom", nie.Provider)
	assert.ErrorIs(t, err, importlist.ErrNotImplemented)
}
