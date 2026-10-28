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

package ui_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/iptv/livetvstore"
	"github.com/mediactl/clustarr/ui"
	"github.com/mediactl/clustarr/ui/projection"
)

// liveTVStore is a membus clustarr-livetv bucket.
func liveTVStore(t *testing.T) events.ObjectStore {
	t.Helper()
	bus := membus.New(nil)
	require.NoError(t, bus.Ensure(context.Background(), events.Default().ForSingleNode()))
	return bus.ObjectStore(events.BucketLiveTV)
}

func TestLogoRouteServesTheStoredCopyAndRefusesABadHash(t *testing.T) {
	store := liveTVStore(t)
	key := types.NamespacedName{Namespace: "media", Name: "argon-tv"}
	src := "https://pl.example/espn.png"
	png := []byte("\x89PNG\r\n\x1a\nlogo")
	_, err := store.Put(context.Background(), livetvstore.LogoObject(key, src), bytes.NewReader(png), events.ObjectMeta{Headers: map[string]string{"Content-Type": "image/png"}})
	require.NoError(t, err)
	srv := ui.NewServer(t.Context(), ui.Options{LiveTV: store})

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, projection.LogoPath(key, src), nil))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "image/png", rec.Header().Get("Content-Type"))
	assert.Equal(t, png, rec.Body.Bytes())
	etag := rec.Header().Get("ETag")
	require.NotEmpty(t, etag)

	req := httptest.NewRequest(http.MethodGet, projection.LogoPath(key, src), nil)
	req.Header.Set("If-None-Match", etag)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	assert.Equal(t, http.StatusNotModified, rec.Code)

	for _, path := range []string{
		"/livetv/logo/media/argon-tv/zz",
		"/livetv/logo/media/argon-tv/" + "0123456789abcdef0123456789abcdef",
		projection.LogoPath(types.NamespacedName{Namespace: "media", Name: "other"}, src),
	} {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		assert.Equal(t, http.StatusNotFound, rec.Code, path)
	}
	rec = httptest.NewRecorder()
	ui.NewServer(t.Context(), ui.Options{}).Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, projection.LogoPath(key, src), nil))
	assert.Equal(t, http.StatusNotFound, rec.Code, "no store, no logo")
}
