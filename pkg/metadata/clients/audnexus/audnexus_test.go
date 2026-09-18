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

package audnexus_test

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
	"github.com/mediactl/clustarr/pkg/metadata/clients/audnexus"
)

func TestAudiobookMapsAudnexusFieldsIntoTheNormalizedModel(t *testing.T) {
	body, err := os.ReadFile("../../../../testdata/metadata/audnexus/book_B0036I54I6.json")
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/books/B0036I54I6", r.URL.Path)
		require.Equal(t, "us", r.URL.Query().Get("region"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	c := audnexus.New(srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))

	a, err := c.Audiobook(context.Background(), "B0036I54I6", "us")

	require.NoError(t, err)
	require.Equal(t, "The Hobbit", a.Title)
	require.Equal(t, "Recorded Books", a.Publisher)
	require.Equal(t, 630*time.Minute, a.Runtime)
	require.Equal(t, []string{"Rob Inglis"}, a.Narrators)
	require.Len(t, a.Authors, 1)
	require.Equal(t, "J.R.R. Tolkien", a.Authors[0].Name)
	require.Equal(t, "unabridged", a.Format)
	require.NotNil(t, a.Rating)
	require.EqualValues(t, 960, a.Rating.ValueCentis, "4.8/5 normalized to /10, ×100")
	require.Equal(t, "9780618968633", a.IDs[metadata.KeyISBN13])
}

func TestAudiobookMapsA404ToErrNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	c := audnexus.New(srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))

	_, err := c.Audiobook(context.Background(), "B0000000000", "us")

	require.ErrorIs(t, err, metadata.ErrNotFound)
}
