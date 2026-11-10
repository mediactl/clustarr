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
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"

	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/audnexus"
)

// GET /authors/{asin}?region= (api.audnex.us, recorded 2026-11-10) maps
// into an Author for Add New's author link (2026-11-10 design D11, §5.4;
// plan W2b.3 leftover): the ASIN as its id, the biography, the photo as its
// poster and the genres; Audnexus's similar authors have no field and are
// left out.
func TestAudiobookAuthorMapsTheAuthorRecord(t *testing.T) {
	body, err := os.ReadFile("../../../../test/data/metadata/audnexus/author_B000AP9A6K.json")
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/authors/B000AP9A6K", r.URL.Path)
		require.Equal(t, "uk", r.URL.Query().Get("region"), "the region is part of the request, as an audiobook's is")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	c := audnexus.New(srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))

	var provider metadata.AudiobookAuthorProvider = c
	a, err := provider.AudiobookAuthor(context.Background(), "B000AP9A6K", "uk")

	require.NoError(t, err)
	require.Equal(t, "J.K. Rowling", a.Name)
	require.Equal(t, metadata.ExternalIDs{metadata.KeyASIN: "B000AP9A6K"}, a.IDs)
	require.True(t, strings.HasPrefix(a.Overview, "Rowling is the author of the enduringly popular"), a.Overview)
	require.Equal(t, []string{"Children's Audiobooks"}, a.Genres)
	require.Equal(t, []metadata.Image{{
		Type: metadata.ImageTypePoster,
		URL:  "https://images-na.ssl-images-amazon.com/images/S/amzn-author-media-prod/8cigckin175jtpsk3gs361r4ss.jpg",
	}}, a.Images)
	require.Nil(t, a.Born)
	require.Empty(t, a.Links)
}

// Audnexus answers an author whose Audible page is gone with a 404 (its
// UPSTREAM_GONE body), which is ErrNotFound as every other 404 is.
func TestAudiobookAuthorMapsA404ToErrNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":"UPSTREAM_GONE","message":"Upstream page gone (redirected to search)"}}`))
	}))
	defer srv.Close()
	c := audnexus.New(srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))

	_, err := c.AudiobookAuthor(context.Background(), "B000AQ1S22", "us")

	require.ErrorIs(t, err, metadata.ErrNotFound)
}

func TestAudiobookAuthorRejectsAMalformedBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"asin":"B000AP9A6K","name":`))
	}))
	defer srv.Close()
	c := audnexus.New(srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))

	a, err := c.AudiobookAuthor(context.Background(), "B000AP9A6K", "us")

	require.Nil(t, a)
	require.ErrorIs(t, err, metadata.ErrDecode)
}
