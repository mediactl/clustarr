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

package metadata

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/tmdb"
)

// recordedYourName fetches Your Name (2016) through the real TMDB client
// from the fixtures recorded from the live API: English, then Japanese, its
// original language.
func recordedYourName(t *testing.T) *pkgmetadata.Movie {
	t.Helper()
	files := map[string]string{"en-US": "movie-372058.json", "ja": "movie-372058-ja.json"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name, ok := files[r.URL.Query().Get("language")]
		if r.URL.Path != "/movie/372058" || !ok {
			http.NotFound(w, r)
			return
		}
		body, err := os.ReadFile("../../../test/data/metadata/tmdb/" + name)
		require.NoError(t, err)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	c, err := tmdb.New("test-key", srv.Client(), srv.URL, pkgmetadata.NewLimiter(rate.Inf, 1))
	require.NoError(t, err)
	m, err := c.Movie(context.Background(), "372058", "")
	require.NoError(t, err)
	return m
}

// The image cap keeps every language's and every provider's images, not the
// first fifty TMDB lists: Your Name's English and untagged logos and posters
// alone fill fifty, which cut all of its Japanese images (so Plex's
// OriginalImage was always empty) and every image an artwork provider
// appended after TMDB's (so fanart.tv's banner, clearart, disc and thumb
// vanished at the next refresh).
func TestMovieImagesKeepEveryLanguageAndEveryProvider(t *testing.T) {
	m := recordedYourName(t)
	require.Greater(t, len(m.Images), 50, "premise: the recorded film lists more images than the CRD holds")
	m.Images = append(m.Images, pkgmetadata.Image{
		Type: pkgmetadata.ImageTypeBanner, URL: "https://assets.fanart.tv/fanart/movies/372058/moviebanner/your-name.jpg", Language: "en",
	})

	ac := buildMovieMetadataAC(m, nil, time.Now())

	require.LessOrEqual(t, len(ac.Images), 50, "MovieMetadata.Images: +kubebuilder:validation:MaxItems=50")
	byLang := map[string]int{}
	var banner bool
	for _, img := range ac.Images {
		if img.Language != nil {
			byLang[*img.Language]++
		}
		if *img.Type == catalogv1alpha1.ImageTypeBanner {
			banner = true
		}
	}
	assert.Positive(t, byLang["ja"], "the original language's images survive the cap")
	assert.Positive(t, byLang["en"], "the configured language's images survive the cap")
	assert.True(t, banner, "an artwork provider's image appended after TMDB's survives the cap")
	assert.Equal(t, *ac.Images[0].Type, catalogv1alpha1.ImageTypePoster, "the lead poster stays first")
}
