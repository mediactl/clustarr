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

package plex_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/ui/plex"
	"github.com/mediactl/clustarr/ui/projection"
)

type compactPair struct {
	URL     string `json:"url"`
	Compact string `json:"compact"`
}

// compactHandler is a provider over objs whose compact renditions are
// compact (by "<kind>/<uid>") and whose stored posters are of the content
// types webp names.
func compactHandler(t *testing.T, compact map[string]string, webp map[types.UID]bool, objs ...client.Object) http.Handler {
	t.Helper()
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(objs...).Build()
	return plex.Handler(plex.Options{
		ExternalURL: "https://plex.example:443",
		Index: func(ctx context.Context) (*projection.Index, error) {
			return projection.BuildIndex(ctx, c)
		},
		Compact: func(kind commonv1.MediaKind, uid types.UID, overlaid bool) (string, bool) {
			key := string(kind) + "/" + string(uid)
			if !overlaid {
				key += "/plain"
			}
			d, ok := compact[key]
			return d, ok
		},
		ArtContentType: func(_ commonv1.MediaKind, uid types.UID, _ catalogv1.ImageType) string {
			if webp[uid] {
				return "image/webp"
			}
			return "image/jpeg"
		},
	})
}

func compactPosters(t *testing.T, h http.Handler, header ...string) (*httptest.ResponseRecorder, []compactPair) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/plex/compact-posters", nil)
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	rec := doRequest(h, req)
	var out struct {
		Posters []compactPair `json:"posters"`
	}
	if rec.Code == http.StatusOK {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	}
	return rec, out.Posters
}

// coverPoster is the poster URL the provider hands Plex in an item's
// metadata: the URL whose SHA-1 names the poster Plex stores.
func coverPoster(t *testing.T, h http.Handler, path string) string {
	t.Helper()
	rec := doRequest(h, httptest.NewRequest(http.MethodGet, path, nil))
	require.Equal(t, http.StatusOK, rec.Code)
	m := regexp.MustCompile(`"type":"coverPoster","url":"([^"]+)"`).FindSubmatch(rec.Body.Bytes())
	require.NotNil(t, m, "no coverPoster in %s", rec.Body.String())
	var u string
	require.NoError(t, json.Unmarshal([]byte(`"`+string(m[1])+`"`), &u))
	return u
}

// The pairing list (the compact-posters design, 2026-11-27) is what the
// proxy in the Plex pod maps a poster Plex holds to its compact twin by:
// for each movie and series with a compact rendition, the poster URL the
// provider hands Plex for it, exactly as handed (Plex names its stored
// poster by that URL's SHA-1), and the path of the twin under /art. It
// names no title and nothing but a movie or a series.
func TestCompactPostersPairsThePosterPlexWasHandedWithItsTwin(t *testing.T) {
	movie := fixtureMovie() // has status.overlay
	series, episodes := fixtureSeriesAndEpisodes()
	series.Status.Overlay = nil
	webp := fixtureMovie()
	webp.Name, webp.UID = "stored-as-webp", "66666666-6666-6666-6666-666666666666"
	bare := fixtureMovie()
	bare.Name, bare.UID = "no-twin", "55555555-5555-5555-5555-555555555555"
	objs := []client.Object{movie, series, webp, bare}
	for _, e := range episodes {
		objs = append(objs, e)
	}
	compact := map[string]string{
		"movie/" + string(movie.UID):              "cmp-movie",
		"series/" + string(series.UID) + "/plain": "cmp-series",
		"movie/" + string(webp.UID):               "cmp-webp",
	}
	if len(episodes) > 0 {
		compact["episode/"+string(episodes[0].UID)] = "stray"
	}
	h := compactHandler(t, compact, map[types.UID]bool{webp.UID: true}, objs...)

	rec, pairs := compactPosters(t, h)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.Equal(t, "no-cache", rec.Header().Get("Cache-Control"))
	require.Len(t, pairs, 3, "%s", rec.Body.String())
	byCompact := map[string]string{}
	for i, p := range pairs {
		byCompact[p.Compact] = p.URL
		if i > 0 {
			assert.Less(t, pairs[i-1].URL, p.URL, "sorted by url")
		}
	}

	movieURL := coverPoster(t, h, "/plex/movies/library/metadata/"+string(movie.UID))
	assert.Equal(t, movieURL, byCompact["/art/movie/"+string(movie.UID)+"/poster?size=compact&v=cmp-movie"],
		"the URL is the one the metadata response carries")
	assert.Contains(t, movieURL, "v=overlaydigest1", "an overlaid poster is named by its overlay")

	seriesURL := coverPoster(t, h, "/plex/tv/library/metadata/"+string(series.UID))
	assert.Equal(t, seriesURL, byCompact["/art/series/"+string(series.UID)+"/poster?size=compact&v=cmp-series"])
	assert.NotContains(t, seriesURL, "overlaydigest", "a titled poster no profile selects is named by its original")

	webpURL := coverPoster(t, h, "/plex/movies/library/metadata/"+string(webp.UID))
	assert.Equal(t, webpURL, byCompact["/art/movie/"+string(webp.UID)+"/poster?size=compact&v=cmp-webp"])
	assert.Regexp(t, `&nowebp=1$`, webpURL, "a WebP poster's URL asks for it converted, and the list carries that URL")

	assert.NotContains(t, rec.Body.String(), "Skyfall", "no title")
	assert.NotContains(t, rec.Body.String(), string(bare.UID), "an item with no compact rendition is not listed")

	// The same list is the same bytes and the same ETag; a matching
	// If-None-Match answers 304 with no body.
	etag := rec.Header().Get("ETag")
	require.Regexp(t, `^"[0-9a-f]{64}"$`, etag)
	again, _ := compactPosters(t, h)
	assert.Equal(t, rec.Body.String(), again.Body.String())
	assert.Equal(t, etag, again.Header().Get("ETag"))
	cond, _ := compactPosters(t, h, "If-None-Match", etag)
	assert.Equal(t, http.StatusNotModified, cond.Code)
	assert.Empty(t, cond.Body.String())
	assert.Equal(t, etag, cond.Header().Get("ETag"))
}

// A poster is paired with the twin of its own kind: the overlay Plex holds
// with the compact overlay, an original with the compact poster. An
// overlaid poster whose compact overlay is not drawn yet (the first roll,
// or a base that cannot be drawn on) is not listed, since its compact
// poster alone would show a phone the poster without its rating badge.
func TestCompactPostersNeverPairsABadgedPosterWithABadgelessTwin(t *testing.T) {
	movie := fixtureMovie() // has status.overlay
	h := compactHandler(t, map[string]string{"movie/" + string(movie.UID) + "/plain": "titled-only"}, nil, movie)
	rec, pairs := compactPosters(t, h)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, pairs, "%s", rec.Body.String())
}

// With no compact lookup (a ui with no artwork store) or no external URL
// the list is empty, not an error: the proxy then pairs nothing.
func TestCompactPostersIsEmptyWithoutALookupOrAnExternalURL(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(fixtureMovie()).Build()
	index := func(ctx context.Context) (*projection.Index, error) { return projection.BuildIndex(ctx, c) }
	always := func(commonv1.MediaKind, types.UID, bool) (string, bool) { return "d", true }
	for name, opts := range map[string]plex.Options{
		"no lookup":       {ExternalURL: "https://plex.example", Index: index},
		"no external URL": {Index: index, Compact: always},
	} {
		t.Run(name, func(t *testing.T) {
			rec, pairs := compactPosters(t, plex.Handler(opts))
			require.Equal(t, http.StatusOK, rec.Code)
			assert.Empty(t, pairs)
			assert.JSONEq(t, `{"posters":[]}`, rec.Body.String())
		})
	}
}
