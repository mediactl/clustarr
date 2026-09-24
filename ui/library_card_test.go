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
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/ui"
	"github.com/mediactl/clustarr/ui/projection"
)

// tagWith returns the whole opening tag in body that carries attr, so a
// test can assert on that one element's attributes without depending on
// the order templ writes them in.
func tagWith(t *testing.T, body, attr string) string {
	t.Helper()
	i := strings.Index(body, attr)
	require.GreaterOrEqual(t, i, 0, "no element carries %s", attr)
	start := strings.LastIndex(body[:i], "<")
	end := strings.Index(body[i:], ">")
	require.GreaterOrEqual(t, end, 0)
	return body[start : i+end+1]
}

// TestLibraryCardsAreItemComponents: a library card is shadcn-templ's item
// (design 2026-09-23, "Use shadcn-templ item components for the library
// elements"): the whole tile is a link to the detail page, the poster
// sits in the item's media slot at poster ratio, the title and year are
// the item's title, kind and profile its description, and the monitored,
// phase and on-disk marks are badges. Every data attribute the earlier
// tests and the e2e suite assert on stays on the card itself.
func TestLibraryCardsAreItemComponents(t *testing.T) {
	arrival := projection.LibraryItem{
		Ref: types.NamespacedName{Namespace: "default", Name: "arrival"}, Kind: commonv1.MediaKindMovie,
		Tab: projection.TabMovies, Title: "Arrival", Year: 2016, Monitored: true, Phase: "Imported", HasFile: true,
		QualityProfileRef: "hd-bluray-web", Poster: "https://image.tmdb.org/t/p/w500/arrival.jpg",
	}
	heat := projection.LibraryItem{
		Ref: types.NamespacedName{Namespace: "default", Name: "heat"}, Kind: commonv1.MediaKindMovie,
		Tab: projection.TabMovies, Title: "Heat", Year: 1995, Phase: "Wanted",
	}
	srv := ui.NewServer(t.Context(), ui.Options{
		Library: func(context.Context) []projection.LibraryItem { return []projection.LibraryItem{arrival, heat} },
	})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/library/movies", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	require.Equal(t, 1, strings.Count(body, `data-slot="item-group"`), "the grid is one item group")
	require.Equal(t, 2, strings.Count(body, `data-slot="item"`), "one item per card")
	require.Equal(t, 2, strings.Count(body, `data-slot="item-media"`), "every card has a media slot, art or not")

	card := tagWith(t, body, `data-ref="default/arrival"`)
	require.True(t, strings.HasPrefix(card, "<a "), "the whole card is the link, not only its title: %s", card)
	require.Contains(t, card, `href="/library/default/movie/arrival"`)
	require.Contains(t, card, `data-slot="item"`)
	for _, attr := range []string{
		`data-kind="movie"`, `data-monitored="true"`, `data-phase="Imported"`, `data-hasfile="true"`,
		`data-year="2016"`, `data-profile="hd-bluray-web"`, `data-poster="provider"`,
	} {
		require.Contains(t, card, attr, "the card keeps the attribute the tests key on")
	}

	img := tagWith(t, body, `alt="Arrival"`)
	require.True(t, strings.HasPrefix(img, "<img"), "%s", img)
	require.Contains(t, img, `src="https://image.tmdb.org/t/p/w500/arrival.jpg"`)
	require.Contains(t, img, `loading="lazy"`)
	require.Contains(t, img, `referrerpolicy="no-referrer"`)
	require.Contains(t, img, `aspect-[2/3]`, "posters keep their ratio; a square crop takes the faces off")
	require.Regexp(t, regexp.MustCompile(`data-slot="item-media"[^>]*>\s*<img[^>]*alt="Arrival"`), body, "the poster is the item's media")

	require.Regexp(t, regexp.MustCompile(`data-slot="item-title"[^>]*>[^<]*Arrival`), body)
	require.Regexp(t, regexp.MustCompile(`data-slot="item-title"[^>]*>.*?2016.*?</`), body, "the year rides the title")
	require.Regexp(t, regexp.MustCompile(`data-slot="item-description"[^>]*>[^<]*movie[^<]*hd-bluray-web`), body, "kind and profile are the description")
	require.GreaterOrEqual(t, strings.Count(body, `data-slot="badge"`), 4, "monitored, Imported, on disk, and Heat's unmonitored and Wanted are badges")
	require.Regexp(t, regexp.MustCompile(`data-slot="badge"[^>]*>monitored<`), body)
	require.Regexp(t, regexp.MustCompile(`data-slot="badge"[^>]*>Imported<`), body)

	heatCard := tagWith(t, body, `data-ref="default/heat"`)
	require.Contains(t, heatCard, `data-poster="none"`)
	require.NotContains(t, body, `alt="Heat"`, "no art means no image tag, a placeholder box instead")
	require.Contains(t, body, "no art")
}
