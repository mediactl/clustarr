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
