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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/ui/plex"
	"github.com/mediactl/clustarr/ui/projection"
)

// TestImagesMovie is GET .../{ratingKey}/images: the artwork gallery
// (research §7), poster and fanart only -- the fixture movie has no logo
// fetched.
func TestImagesMovie(t *testing.T) {
	h := newTestHandler(t, externalURLFixture, fixtureMovie())
	rec := getJSON(t, h, "/plex/movies/library/metadata/"+string(movieUID)+"/images")
	require.Equal(t, http.StatusOK, rec.Code)
	requireGolden(t, rec, "images")
}

// TestImagesEpisodeEmpty proves an episode -- which has no artwork field
// (api/catalog/v1alpha1/episode_types.go) -- answers 200 with an empty
// gallery rather than 404: the ratingKey resolves to a real object, it
// simply has nothing to report yet.
func TestImagesEpisodeEmpty(t *testing.T) {
	series, episodes := fixtureSeriesAndEpisodes()
	h := newTestHandlerWithEpisodes(t, externalURLFixture, series, episodes)

	rec := getJSON(t, h, "/plex/tv/library/metadata/"+string(episodes[0].UID)+"/images")
	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t,
		`{"MediaContainer":{"offset":0,"totalSize":0,"identifier":"tv.plex.agents.custom.clustarr.tv","size":0,"Image":[]}}`,
		rec.Body.String())
}

// An image whose stored object is a WebP is offered to PMS through /art's
// nowebp, which serves it as JPEG or PNG: PMS keeps the bytes it downloads,
// and its UltraBlur processor decodes no WebP (talos, 2026-11-03). Every
// other image keeps the URL PMS already holds, so nothing else is
// downloaded again.
func TestAWebPImageIsOfferedAsJPEG(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(fixtureMovie()).Build()
	h := plex.Handler(plex.Options{
		ExternalURL: externalURLFixture,
		Index: func(ctx context.Context) (*projection.Index, error) {
			return projection.BuildIndex(ctx, c)
		},
		ArtContentType: func(_ commonv1.MediaKind, _ types.UID, it catalogv1.ImageType) string {
			if it == catalogv1.ImageTypeFanart {
				return "image/webp"
			}
			return "image/jpeg"
		},
	})

	var md struct {
		MediaContainer struct {
			Metadata []struct {
				Thumb, Art string
				Image      []struct{ Type, URL string }
			}
		}
	}
	rec := getJSON(t, h, "/plex/movies/library/metadata/"+string(movieUID))
	require.Equal(t, http.StatusOK, rec.Code)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &md))
	require.Len(t, md.MediaContainer.Metadata, 1)
	m := md.MediaContainer.Metadata[0]
	assert.True(t, strings.HasSuffix(m.Art, "&nowebp=1"), m.Art)
	assert.NotContains(t, m.Thumb, "nowebp", "a JPEG keeps its URL")
	for _, img := range m.Image {
		assert.Equal(t, img.Type == "background", strings.Contains(img.URL, "nowebp=1"), img.Type+": "+img.URL)
	}

	var gallery struct {
		MediaContainer struct{ Image []struct{ Type, URL string } }
	}
	rec = getJSON(t, h, "/plex/movies/library/metadata/"+string(movieUID)+"/images")
	require.Equal(t, http.StatusOK, rec.Code)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &gallery))
	require.NotEmpty(t, gallery.MediaContainer.Image)
	for _, img := range gallery.MediaContainer.Image {
		assert.Equal(t, img.Type == "background", strings.Contains(img.URL, "nowebp=1"), img.Type+": "+img.URL)
	}
}
