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

package views_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/ui/projection"
	"github.com/mediactl/clustarr/ui/views"
)

// TestEveryProviderHasItsLogo: providerLinks shows a link naming a
// provider as /static/providers/<provider>.svg (owner, 2026-11-04; the
// non-video providers since 2026-11-10, design §9.6), so every Provider
// constant has its file -- a small, square-ish SVG with its role and label
// for a screen reader -- and every file is a provider the views name, so a
// renamed constant and an orphaned file are both caught.
func TestEveryProviderHasItsLogo(t *testing.T) {
	dir := filepath.Join("..", "static", "providers")
	providers := views.Providers()
	require.Len(t, providers, 16)
	for _, p := range providers {
		b, err := os.ReadFile(filepath.Join(dir, p+".svg"))
		require.NoError(t, err, "provider %q has no logo under ui/static/providers", p)
		svg := string(b)
		require.True(t, strings.HasPrefix(svg, "<svg"), "%s starts with <svg", p)
		require.Contains(t, svg, `role="img"`, p)
		require.Contains(t, svg, `aria-label="`, p)
		require.Contains(t, svg, `viewBox="`, p)
		require.Less(t, len(b), 3<<10, "%s is under 3 KiB", p)
		require.NotContains(t, svg, "<script", p)
		require.NotContains(t, svg, "<image", "%s loads nothing remote", p)
		require.NotContains(t, svg, "href=", "%s loads nothing remote", p)
	}
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".svg")
		require.Contains(t, providers, name, "ui/static/providers/%s belongs to no Provider constant", e.Name())
	}
	for _, want := range []string{"musicbrainz", "discogs", "wikipedia", "openlibrary", "hardcover", "comicvine", "metron", "mangadex", "anilist", "myanimelist", "audible", "audnexus", "goodreads"} {
		require.Contains(t, providers, want, "design §9.6 names %s", want)
	}
}

// TestProviderLinksRenderTheNewLogos: a link naming a non-video provider
// renders in the hero as its logo with the label as alt text, like IMDb's.
func TestProviderLinksRenderTheNewLogos(t *testing.T) {
	var b bytes.Buffer
	d := views.Detail{Item: projection.LibraryItem{Ref: types.NamespacedName{Namespace: "media", Name: "rh"}, Kind: commonv1.MediaKindArtist, Tab: projection.TabMusic, Title: "Radiohead"}}
	d.Links = []views.Link{
		{Label: "MusicBrainz", Href: "https://musicbrainz.org/artist/a74b1b7f", Provider: views.ProviderMusicBrainz},
		{Label: "ComicVine", Href: "https://comicvine.gamespot.com/volume/4050-18166/", Provider: views.ProviderComicVine},
		{Label: "Elsewhere", Href: "https://example.org/"},
	}
	require.NoError(t, views.LibraryDetail(d).Render(context.Background(), &b))
	html := b.String()
	require.Contains(t, html, `<img src="/static/providers/musicbrainz.svg" alt="MusicBrainz"`)
	require.Contains(t, html, `data-provider-link="musicbrainz"`)
	require.Contains(t, html, `<img src="/static/providers/comicvine.svg" alt="ComicVine"`)
	require.Contains(t, html, `lucide-external-link`, "a link naming no provider keeps the generic icon")
}
