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

package comicvine_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"

	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/comicvine"
)

func TestVolumeMapsComicVineFieldsIntoTheNormalizedModel(t *testing.T) {
	body, err := os.ReadFile("../../../../testdata/metadata/comicvine/volume_18257.json")
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/volume/4050-18257", r.URL.Path)
		require.NotEmpty(t, r.Header.Get("User-Agent"), "ComicVine blocks the Go default User-Agent")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	c := comicvine.New("test-key", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 3))

	v, err := c.Volume(context.Background(), metadata.ExternalIDs{metadata.KeyComicVine: "4050-18257"})

	require.NoError(t, err)
	require.Equal(t, "Batman", v.Title)
	require.Equal(t, "DC Comics", v.Publisher)
	require.EqualValues(t, 716, v.IssueCount)
	require.EqualValues(t, 1940, *v.StartYear)
	require.Equal(t, "4050-18257", v.IDs[metadata.KeyComicVine])
}

func TestVolumeMapsA404ToErrNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	c := comicvine.New("test-key", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 3))

	_, err := c.Volume(context.Background(), metadata.ExternalIDs{metadata.KeyComicVine: "4050-99999999"})

	require.ErrorIs(t, err, metadata.ErrNotFound)
}

func TestVolumeMapsA429WithRetryAfterToRateLimitedError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	c := comicvine.New("test-key", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 3))

	_, err := c.Volume(context.Background(), metadata.ExternalIDs{metadata.KeyComicVine: "4050-18257"})

	var rl *metadata.RateLimitedError
	require.ErrorAs(t, err, &rl)
	require.Equal(t, 2*time.Minute, rl.RetryAfter)
}

func TestVolumeRequiresAComicVineID(t *testing.T) {
	c := comicvine.New("test-key", http.DefaultClient, "", metadata.NewLimiter(rate.Inf, 3))

	_, err := c.Volume(context.Background(), metadata.ExternalIDs{})

	require.Error(t, err)
}

// TestSearchVolumesMapsComicVineFieldsIntoSearchHits exercises
// search?resources=volume, documented in docs/research/metadata.md §2.5.
func TestSearchVolumesMapsComicVineFieldsIntoSearchHits(t *testing.T) {
	body, err := os.ReadFile("../../../../testdata/metadata/comicvine/search_batman.json")
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/search/", r.URL.Path)
		require.Equal(t, "volume", r.URL.Query().Get("resources"))
		require.Equal(t, "batman", r.URL.Query().Get("query"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	c := comicvine.New("test-key", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 3))

	hits, err := c.SearchVolumes(context.Background(), "batman")

	require.NoError(t, err)
	require.Len(t, hits, 1)
	require.Equal(t, "Batman", hits[0].Title)
	require.EqualValues(t, 1940, hits[0].Year)
	require.Equal(t, "18257", hits[0].IDs[metadata.KeyComicVine])
	require.Equal(t, "https://comicvine.gamespot.com/a/uploads/original/batman.jpg", hits[0].Poster)
}

// TestIssuesMapsAVolumesIssues exercises /issues/?filter=volume:{id},
// documented in docs/research/metadata.md §2.5.
func TestIssuesMapsAVolumesIssues(t *testing.T) {
	body, err := os.ReadFile("../../../../testdata/metadata/comicvine/issues_volume_18257.json")
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/issues/", r.URL.Path)
		require.Equal(t, "volume:18257", r.URL.Query().Get("filter"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	c := comicvine.New("test-key", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 3))

	issues, err := c.Issues(context.Background(), "18257")

	require.NoError(t, err)
	require.Len(t, issues, 1)
	require.Equal(t, "1", issues[0].Number)
	require.Equal(t, "Batman #1", issues[0].Title)
	require.NotNil(t, issues[0].CoverDate)
	require.True(t, issues[0].CoverDate.Equal(time.Date(1940, 4, 25, 0, 0, 0, 0, time.UTC)))
	require.NotNil(t, issues[0].StoreDate)
	require.True(t, issues[0].StoreDate.Equal(time.Date(1940, 3, 1, 0, 0, 0, 0, time.UTC)))
	require.NotNil(t, issues[0].Image)
	require.Equal(t, "https://comicvine.gamespot.com/a/uploads/original/batman-1.jpg", issues[0].Image.URL)
}

func TestVolumeRejectsMalformedResponseBodies(t *testing.T) {
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
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()
			c := comicvine.New("test-key", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 3))

			var v *metadata.ComicVolume
			var err error
			require.NotPanics(t, func() {
				v, err = c.Volume(context.Background(), metadata.ExternalIDs{metadata.KeyComicVine: "4050-18257"})
			})

			require.Nil(t, v)
			require.Error(t, err)
			require.ErrorIs(t, err, metadata.ErrDecode)
		})
	}
}
