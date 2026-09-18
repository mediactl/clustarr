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

package stevenlu_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/importlist/stevenlu"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFetchParsesPopularMovies(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := os.ReadFile("../../../testdata/importlist/stevenlu/movies.json")
		require.NoError(t, err)
		_, _ = w.Write(b)
	}))
	defer srv.Close()

	l := stevenlu.New("stevenlu-popular", stevenlu.WithBaseURL(srv.URL))
	assert.Equal(t, commonv1.MediaKindMovie, l.Kind())

	items, err := l.Fetch(t.Context())
	require.NoError(t, err)
	require.Len(t, items, 2)
	assert.Equal(t, "Dune", items[0].Title)
	assert.Equal(t, "tt1160419", items[0].ExternalIDs.IMDb)
}
