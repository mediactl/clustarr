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

package audible_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/audible"
)

const fixtures = "../../../../test/data/metadata/audible/"

func serve(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.RequestURI())
		if r.URL.Path != "/1.0/catalog/products" {
			http.NotFound(w, r)
			return
		}
		body, err := os.ReadFile(fixtures + "search_left_hand.json")
		require.NoError(t, err)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

// The recorded search (2026-11-10, "The Left Hand of Darkness" by Le Guin,
// api.audible.com): every product's fields reach the model.
func TestProductsMapTheCatalogsFields(t *testing.T) {
	srv, seen := serve(t)
	c := audible.New(audible.Config{HTTPClient: srv.Client(), BaseURL: srv.URL})
	products, err := c.Products(context.Background(), "The Left Hand of Darkness", "Le Guin", "us")
	require.NoError(t, err)
	require.Len(t, *seen, 1)
	assert.Contains(t, (*seen)[0], "title=The+Left+Hand+of+Darkness")
	assert.Contains(t, (*seen)[0], "author=Le+Guin")
	assert.Contains(t, (*seen)[0], "response_groups=product_desc%2Ccontributors%2Cmedia%2Cproduct_attrs%2Cseries%2Crating")
	require.GreaterOrEqual(t, len(products), 3)
	p := products[0]
	assert.Equal(t, "B01NH0D987", p.ASIN)
	assert.Equal(t, "The Left Hand of Darkness", p.Title)
	assert.Equal(t, []metadata.NamedRef{{Name: "Ursula K. Le Guin", ASIN: "B0GZZ57KFW"}}, p.Authors)
	assert.Equal(t, []string{"George Guidall"}, p.Narrators)
	assert.Equal(t, "Recorded Books", p.Publisher)
	require.NotNil(t, p.ReleaseDate)
	assert.Equal(t, time.Date(2016, 12, 28, 0, 0, 0, 0, time.UTC), *p.ReleaseDate)
	assert.Equal(t, 579*time.Minute, p.Runtime)
	assert.Equal(t, "english", p.Language)
	assert.False(t, p.Abridged)
	assert.Equal(t, "https://m.media-amazon.com/images/I/41Y2-V4+aTL._SL500_.jpg", p.Image)
	require.NotNil(t, p.Series)
	assert.Equal(t, metadata.SeriesLink{Series: "The Hainish Cycle", Position: "4", Primary: true}, *p.Series)
	require.NotNil(t, p.Rating)
	assert.EqualValues(t, 407, p.Rating.ValueCentis)
	assert.EqualValues(t, 5968, p.Rating.Votes)
	assert.True(t, len(p.Summary) > 50 && p.Summary[:11] == "Winner of t", p.Summary)
	assert.NotContains(t, p.Summary, "<")
}

func TestSearchHitsAreKeyedByASIN(t *testing.T) {
	srv, _ := serve(t)
	c := audible.New(audible.Config{HTTPClient: srv.Client(), BaseURL: srv.URL})
	hits, err := c.SearchAudiobooks(context.Background(), "The Left Hand of Darkness", "")
	require.NoError(t, err)
	require.NotEmpty(t, hits)
	assert.Equal(t, "B01NH0D987", hits[0].IDs[metadata.KeyASIN])
	assert.EqualValues(t, 2016, hits[0].Year)
	assert.NotEmpty(t, hits[0].Poster)
}

func TestRegionsPickTheirHost(t *testing.T) {
	c := audible.New(audible.Config{})
	_, err := c.Products(context.Background(), "x", "", "mars")
	assert.ErrorContains(t, err, `unknown region "mars"`)
	_, err = c.Products(context.Background(), "   ", "", "us")
	assert.ErrorIs(t, err, metadata.ErrNotFound)
	for region, host := range audible.Hosts {
		assert.True(t, len(host) > 0, region)
	}
}
