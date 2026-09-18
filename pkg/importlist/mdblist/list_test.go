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

package mdblist_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/importlist"
	"github.com/mediactl/clustarr/pkg/importlist/mdblist"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFetchFiltersByMediaType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "key123", r.URL.Query().Get("apikey"))
		b, err := os.ReadFile("../../../testdata/importlist/mdblist/items.json")
		require.NoError(t, err)
		_, _ = w.Write(b)
	}))
	defer srv.Close()

	l, err := mdblist.New("mdblist-top", commonv1.MediaKindMovie,
		importlist.MdblistConfig{URL: srv.URL + "/lists/1/items"}, "key123")
	require.NoError(t, err)

	items, err := l.Fetch(t.Context())
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, "The Matrix", items[0].Title)
	assert.Equal(t, "tt0133093", items[0].ExternalIDs.IMDb)
	assert.Equal(t, "603", items[0].ExternalIDs.TMDB)
}
