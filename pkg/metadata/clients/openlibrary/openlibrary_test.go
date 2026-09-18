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

package openlibrary_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"

	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/openlibrary"
)

func TestEditionMapsAnISBNLookupIntoTheNormalizedModel(t *testing.T) {
	body, err := os.ReadFile("../../../../testdata/metadata/openlibrary/isbn_9780141439518.json")
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/isbn/9780141439518.json", r.URL.Path)
		require.Contains(t, r.Header.Get("User-Agent"), "Clustarr/")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	c := openlibrary.New("Clustarr/0.1 (contact@example.invalid)", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))

	e, err := c.Edition(context.Background(), metadata.ExternalIDs{metadata.KeyISBN13: "9780141439518"})

	require.NoError(t, err)
	require.Equal(t, "Pride and Prejudice", e.Title)
	require.Equal(t, "Penguin Classics", e.Publisher)
	require.Equal(t, "9780141439518", e.IDs[metadata.KeyISBN13])
	require.Equal(t, "OL3355576M", e.IDs[metadata.KeyOpenLibraryEdition])
	require.Len(t, e.Images, 1)
	require.Equal(t, "https://covers.openlibrary.org/b/id/8739161-L.jpg", e.Images[0].URL)
}

func TestEditionMapsAMissingISBNToErrNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	c := openlibrary.New("Clustarr/0.1 (contact@example.invalid)", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))

	_, err := c.Edition(context.Background(), metadata.ExternalIDs{metadata.KeyISBN13: "0000000000000"})

	require.ErrorIs(t, err, metadata.ErrNotFound)
}

func TestEditionRequiresAnISBN13(t *testing.T) {
	c := openlibrary.New("Clustarr/0.1 (contact@example.invalid)", http.DefaultClient, "", metadata.NewLimiter(rate.Inf, 1))

	_, err := c.Edition(context.Background(), metadata.ExternalIDs{})

	require.Error(t, err)
}
