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
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

const edgerunnersSet = `384541:
  url_poster: https://api.mediux.pro/assets/3cec73e6-4f43-4b49-b7b4-2ca132b8e49a
  seasons:
    1:
      url_poster: https://api.mediux.pro/assets/4aa6df5a-66a7-411b-83fb-960a449f44e0
      episodes:
        1:
          url_poster: https://api.mediux.pro/assets/a0e12102-0983-4eb9-993f-5f8bf49ee7b8
`

// The item page's Image Set dialog takes a MediUX or ThePosterDB set and
// writes the item's overrides: a movie's poster and background, a series'
// those plus its seasons' posters and episodes' thumbnails. An override of
// a type the set leaves alone is kept.
func TestImageSetDialogAppliesASetToASeries(t *testing.T) {
	srv, c := movieFixture(t, true)
	page := detailPage(t, srv, "/library/default/movie/nerve")
	bar := section(t, page, `data-toolbar`)
	requireTag(t, bar, `data-action="image-set"`, `data-toolbar-button`)
	require.Contains(t, bar, "Image Set")
	dlg := section(t, page, `data-imageset-dialog="nerve"`)
	require.Contains(t, dlg, "Image Set - Nerve")
	require.Contains(t, dlg, `name="yaml"`)
	require.Contains(t, dlg, `action="/library/default/movie/nerve/imageset"`)

	// A movie: the set's poster, keyed by another id but the only entry.
	var m catalogv1.Movie
	require.NoError(t, c.Get(t.Context(), types.NamespacedName{Namespace: "default", Name: "nerve"}, &m))
	m.Spec.Artwork = []catalogv1.ArtworkOverride{{Type: catalogv1.ImageTypeLogo, URL: "https://example.org/logo.png"}}
	require.NoError(t, c.Update(t.Context(), &m))
	rec := postForm(t, srv, "/library/default/movie/nerve/imageset", url.Values{"return": {"/library/default/movie/nerve"}, "yaml": {edgerunnersSet}}, false)
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	require.NoError(t, c.Get(t.Context(), types.NamespacedName{Namespace: "default", Name: "nerve"}, &m))
	require.Equal(t, []catalogv1.ArtworkOverride{
		{Type: catalogv1.ImageTypePoster, URL: "https://api.mediux.pro/assets/3cec73e6-4f43-4b49-b7b4-2ca132b8e49a"},
		{Type: catalogv1.ImageTypeLogo, URL: "https://example.org/logo.png"},
	}, m.Spec.Artwork, "the set's poster, the logo kept")

	// A bad set, and an unknown item.
	rec = postForm(t, srv, "/library/default/movie/nerve/imageset", url.Values{"yaml": {"- nope"}}, false)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	rec = postForm(t, srv, "/library/default/movie/nope/imageset", url.Values{"yaml": {edgerunnersSet}}, false)
	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestImageSetDialogWritesASeriesSeasonsAndEpisodes(t *testing.T) {
	srv, c := seriesFixtureWithActions(t)
	page := detailPage(t, srv, "/library/default/series/andor")
	require.Contains(t, page, `data-imageset-dialog="andor"`)
	var sr catalogv1.Series
	require.NoError(t, c.Get(t.Context(), types.NamespacedName{Namespace: "default", Name: "andor"}, &sr))
	two := int32(2)
	sr.Spec.SeasonArtwork = []catalogv1.SeasonArtworkOverride{
		{Season: 1, Type: catalogv1.ImageTypePoster, URL: "https://theposterdb.com/api/assets/old"},
		{Season: 1, Episode: &two, Type: catalogv1.ImageTypeScreenshot, URL: "https://theposterdb.com/api/assets/e2"},
	}
	require.NoError(t, c.Update(t.Context(), &sr))

	rec := postForm(t, srv, "/library/default/series/andor/imageset", url.Values{"return": {"/library/default/series/andor"}, "yaml": {edgerunnersSet}}, false)
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	require.NoError(t, c.Get(t.Context(), types.NamespacedName{Namespace: "default", Name: "andor"}, &sr))
	one := int32(1)
	require.Equal(t, []catalogv1.ArtworkOverride{{Type: catalogv1.ImageTypePoster, URL: "https://api.mediux.pro/assets/3cec73e6-4f43-4b49-b7b4-2ca132b8e49a"}}, sr.Spec.Artwork)
	require.Equal(t, []catalogv1.SeasonArtworkOverride{
		{Season: 1, Type: catalogv1.ImageTypePoster, URL: "https://api.mediux.pro/assets/4aa6df5a-66a7-411b-83fb-960a449f44e0"},
		{Season: 1, Episode: &one, Type: catalogv1.ImageTypeScreenshot, URL: "https://api.mediux.pro/assets/a0e12102-0983-4eb9-993f-5f8bf49ee7b8"},
		{Season: 1, Episode: &two, Type: catalogv1.ImageTypeScreenshot, URL: "https://theposterdb.com/api/assets/e2"},
	}, sr.Spec.SeasonArtwork, "the set's season poster replaces the old one; episode 2's thumbnail, which the set leaves alone, is kept")
}
