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
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/ui"
)

// /art/<kind>/<uid>/poster?size=compact is the compact rendition of a
// movie's or a series' poster (the compact-posters design, 2026-11-27): the
// compact overlay when there is one, else the compact poster, and 404 when
// the item has neither -- never the regular poster, since the proxy in the
// Plex pod that asks takes a 404 as "serve what Plex holds". It carries the
// regular image's ETag and caching rules, by its own digest.
func TestArtServesTheCompactRendition(t *testing.T) {
	store := newArtStore(t)
	key := func(kind commonv1.MediaKind, uid, variant string) string {
		return posterKey(kind, types.UID(uid), variant)
	}
	put := func(k, body string) events.ObjectInfo { return putArt(t, store, k, "image/jpeg", body) }

	// both: a titled poster with an overlay.
	put(key(commonv1.MediaKindMovie, "both", events.ArtworkVariantOriginal), "regular poster")
	put(key(commonv1.MediaKindMovie, "both", events.ArtworkVariantOverlay), "regular overlay")
	put(key(commonv1.MediaKindMovie, "both", events.ArtworkVariantCompact), "compact poster")
	bothCompact := put(key(commonv1.MediaKindMovie, "both", events.ArtworkVariantOverlayCompact), "compact overlay")
	// titled: a titled poster no profile selects.
	put(key(commonv1.MediaKindSeries, "titled", events.ArtworkVariantOriginal), "regular poster")
	titledCompact := put(key(commonv1.MediaKindSeries, "titled", events.ArtworkVariantCompact), "compact poster only")
	// plain: an untitled poster with an overlay drawn before the compact one.
	put(key(commonv1.MediaKindMovie, "plain", events.ArtworkVariantOriginal), "regular poster")
	plainOverlay := put(key(commonv1.MediaKindMovie, "plain", events.ArtworkVariantOverlay), "regular overlay")
	// An episode's poster and a movie's fanart have no compact rendition,
	// whatever is stored under the name.
	put(key(commonv1.MediaKindEpisode, "ep", events.ArtworkVariantOriginal), "episode poster")
	put(key(commonv1.MediaKindEpisode, "ep", events.ArtworkVariantCompact), "stray")
	put(events.ArtworkKey(commonv1.MediaKindMovie, "both", "fanart", events.ArtworkVariantOriginal), "fanart")
	put(events.ArtworkKey(commonv1.MediaKindMovie, "both", "fanart", events.ArtworkVariantCompact), "stray")

	for _, synced := range []bool{true, false} {
		name := "from the index"
		s := newSyncedArtServer(t, ui.Options{Artwork: store, ArtCacheBytes: 1 << 20})
		if !synced {
			name = "asking the store"
			s = ui.NewServer(t.Context(), ui.Options{Artwork: unsyncedStore{&countingStore{ObjectStore: store}}})
			require.False(t, ui.ArtIndexSyncedForTest(s))
		}
		t.Run(name, func(t *testing.T) {
			rec := getArt(s, "/art/movie/both/poster?size=compact")
			require.Equal(t, http.StatusOK, rec.Code)
			assert.Equal(t, "compact overlay", rec.Body.String())
			assert.Equal(t, "image/jpeg", rec.Header().Get("Content-Type"))
			assert.Equal(t, `"`+bothCompact.Digest+`"`, rec.Header().Get("ETag"))
			assert.Equal(t, "no-cache", rec.Header().Get("Cache-Control"))

			rec = getArt(s, "/art/movie/both/poster?size=compact&v="+bothCompact.Digest)
			require.Equal(t, http.StatusOK, rec.Code)
			assert.Equal(t, "public, max-age=31536000, immutable", rec.Header().Get("Cache-Control"), "?v names the compact digest")

			req := httptest.NewRequest(http.MethodGet, "/art/movie/both/poster?size=compact", nil)
			req.Header.Set("If-None-Match", `"`+bothCompact.Digest+`"`)
			cond := httptest.NewRecorder()
			s.Handler().ServeHTTP(cond, req)
			assert.Equal(t, http.StatusNotModified, cond.Code)
			assert.Empty(t, cond.Body.String())

			rec = getArt(s, "/art/series/titled/poster?size=compact")
			require.Equal(t, http.StatusOK, rec.Code)
			assert.Equal(t, "compact poster only", rec.Body.String())
			assert.Equal(t, `"`+titledCompact.Digest+`"`, rec.Header().Get("ETag"))

			for _, path := range []string{
				"/art/movie/plain/poster?size=compact", // no compact rendition: never the regular one
				"/art/movie/none/poster?size=compact",  // no such item
				"/art/movie/both/poster?size=large",    // a size /art does not know
				"/art/movie/both/poster?size=Compact",  // exactly "compact"
				"/art/movie/both/fanart?size=compact",  // only a poster has one
				"/art/episode/ep/poster?size=compact",  // only a movie's and a series'
			} {
				assert.Equal(t, http.StatusNotFound, getArt(s, path).Code, path)
			}

			// No size is the regular choice, as before.
			rec = getArt(s, "/art/movie/both/poster")
			require.Equal(t, http.StatusOK, rec.Code)
			assert.Equal(t, "regular overlay", rec.Body.String())
			rec = getArt(s, "/art/movie/plain/poster?size=")
			require.Equal(t, http.StatusOK, rec.Code)
			assert.Equal(t, "regular overlay", rec.Body.String())
			assert.Equal(t, `"`+plainOverlay.Digest+`"`, rec.Header().Get("ETag"))
		})
	}
}
