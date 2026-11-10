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

package ui

import (
	"testing"

	"github.com/stretchr/testify/require"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/ui/views"
)

// The comic page's Metron logo is the metadata's links entry of type
// metron (design §9.4; the metron client records the series page by its
// slug, which the ids do not carry), placed after the source's own page:
// a Metron id in the externalIDs alone makes no link, and so does a links
// entry of that type without a URL.
func TestComicLinksTakeTheMetronPageFromTheMetadatasLinksAlone(t *testing.T) {
	comic := func(md catalogv1.ComicMetadata) *catalogv1.Comic {
		return &catalogv1.Comic{
			Spec:   catalogv1.ComicSpec{Source: catalogv1.ComicSourceComicVine, SourceID: "4050-56582"},
			Status: catalogv1.ComicStatus{Metadata: &md},
		}
	}
	comicVine := views.Link{Label: "ComicVine", Href: "https://comicvine.gamespot.com/volume/4050-56582/", Provider: views.ProviderComicVine}
	metron := views.Link{Label: "Metron", Href: "https://metron.cloud/series/saga-2012/", Provider: views.ProviderMetron}

	withLink := comic(catalogv1.ComicMetadata{
		ExternalIDs: map[string]string{"metron": "1", "anilist": "98765"},
		Links:       []catalogv1.Link{{Type: "metron", URL: "https://metron.cloud/series/saga-2012/"}},
	})
	require.Equal(t, []views.Link{
		comicVine, metron,
		{Label: "AniList", Href: "https://anilist.co/manga/98765", Provider: views.ProviderAniList},
	}, comicLinks(withLink), "the links entry's page, after the source's and before the ids' pages")

	idAlone := comic(catalogv1.ComicMetadata{ExternalIDs: map[string]string{"metron": "1"}})
	require.Equal(t, []views.Link{comicVine}, comicLinks(idAlone), "a Metron id alone addresses no page on its site")

	noURL := comic(catalogv1.ComicMetadata{Links: []catalogv1.Link{{Type: "metron"}}})
	require.Equal(t, []views.Link{comicVine}, comicLinks(noURL), "a links entry without a URL makes no link")

	require.Equal(t, []views.Link{comicVine}, comicLinks(comic(catalogv1.ComicMetadata{})), "no links, no Metron")
}
