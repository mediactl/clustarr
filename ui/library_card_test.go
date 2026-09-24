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

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
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

// TestLibraryCardsFollowRadarrsPosterGrid: a library card is Radarr's
// poster tile (design 2026-09-24) built from shadcn-templ's item,
// aspect-ratio and tooltip: the whole tile is the link, the poster fills
// it at 2:3 inside the aspect-ratio slot with the title as a tooltip
// rather than printed text, a status stripe under the poster carries
// data-status, and the footer is two centred lines -- the monitored state
// and the quality profile. Every data attribute the earlier tests key on
// stays on the card element.
func TestLibraryCardsFollowRadarrsPosterGrid(t *testing.T) {
	arrivalPoster := projection.ArtURL(commonv1.MediaKindMovie, types.UID("arrival-uid"), catalogv1.ImageTypePoster, "arrival-digest")
	arrival := projection.LibraryItem{
		Ref: types.NamespacedName{Namespace: "default", Name: "arrival"}, Kind: commonv1.MediaKindMovie,
		Tab: projection.TabMovies, Title: "Arrival", Year: 2016, Monitored: true, Phase: "Imported", HasFile: true,
		QualityProfileRef: "hd-bluray-web", Poster: arrivalPoster,
	}
	heat := projection.LibraryItem{
		Ref: types.NamespacedName{Namespace: "default", Name: "heat"}, Kind: commonv1.MediaKindMovie,
		Tab: projection.TabMovies, Title: "Heat", Year: 1995, Monitored: true, Phase: "Wanted",
	}
	alien := projection.LibraryItem{
		Ref: types.NamespacedName{Namespace: "default", Name: "alien"}, Kind: commonv1.MediaKindMovie,
		Tab: projection.TabMovies, Title: "Alien", Year: 1979, Phase: "Unmonitored",
	}
	srv := ui.NewServer(t.Context(), ui.Options{
		Library: func(context.Context) []projection.LibraryItem { return []projection.LibraryItem{arrival, heat, alien} },
	})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/library/movies", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	require.Equal(t, 1, strings.Count(body, `data-slot="item-group"`), "the grid is one item group")
	require.Equal(t, 3, strings.Count(body, `data-slot="aspect-ratio"`), "every card has a poster box, art or not")
	require.NotContains(t, body, `data-slot="item-title"`, "the poster carries the title; the card prints none")

	card := tagWith(t, body, `data-ref="default/arrival"`)
	require.True(t, strings.HasPrefix(card, "<a "), "the whole tile is the link: %s", card)
	require.Contains(t, card, `href="/library/default/movie/arrival"`)
	require.Contains(t, card, `data-slot="item"`)
	for _, attr := range []string{
		`data-kind="movie"`, `data-monitored="true"`, `data-phase="Imported"`, `data-hasfile="true"`,
		`data-year="2016"`, `data-profile="hd-bluray-web"`, `data-poster="provider"`,
	} {
		require.Contains(t, card, attr, "the card keeps the attribute the tests key on")
	}

	require.Regexp(t, regexp.MustCompile(`data-slot="aspect-ratio"[^>]*--ratio: 2/3[^>]*>[^<]*<img[^>]*alt="Arrival"`), body, "the poster fills a 2:3 box")
	img := tagWith(t, body, `alt="Arrival"`)
	require.Contains(t, img, `src="`+arrivalPoster+`"`, "the poster is this ui's own /art route, never a provider's URL")
	require.Contains(t, img, `loading="lazy"`)

	// ADR-0011: every image a card renders is this ui's own /art route, so
	// no rendered card ever hotlinks a provider's URL directly.
	require.NotRegexp(t, regexp.MustCompile(`<img[^>]*src="https?://`), body,
		"no <img> in the library grid hotlinks a provider's own URL")
	require.Regexp(t, regexp.MustCompile(`data-slot="tooltip-content"[^>]*>[^<]*Arrival`), body, "the title is a tooltip on the poster")

	requireTag(t, body, `data-status="downloaded"`, `bg-emerald-500`)
	at := strings.Index(body, `data-status="downloaded"`)
	require.GreaterOrEqual(t, at, 0)
	footer := body[at:]
	end := strings.Index(footer, "</a>")
	require.GreaterOrEqual(t, end, 0)
	footer = footer[:end]
	require.Contains(t, footer, ">Monitored<")
	require.Contains(t, footer, ">hd-bluray-web<")

	heatCard := tagWith(t, body, `data-ref="default/heat"`)
	require.Contains(t, heatCard, `data-poster="none"`)
	require.NotContains(t, body, `alt="Heat"`, "no art means no image tag, a placeholder with the title instead")
	require.Regexp(t, regexp.MustCompile(`data-slot="aspect-ratio"[^>]*>\s*<div[^>]*>[^<]*Heat`), body, "the placeholder names the film")
	requireTag(t, body, `data-status="missing"`, `bg-red-500`)
	requireTag(t, body, `data-status="unmonitored"`, `bg-slate-500`)
	require.Contains(t, body, ">Unmonitored<")
}

// TestLibraryStripeColourFollowsState pins Radarr's stripe semantics on
// the card: on disk is green, on disk below the cutoff is amber, in
// flight is blue, missing and monitored is red, and unmonitored with
// nothing on disk is gray.
func TestLibraryStripeColourFollowsState(t *testing.T) {
	for name, tc := range map[string]struct {
		item   projection.LibraryItem
		status string
		class  string
	}{
		"on disk":            {projection.LibraryItem{HasFile: true, Monitored: true, Phase: "Imported"}, "downloaded", "bg-emerald-500"},
		"transcoded":         {projection.LibraryItem{HasFile: true, Monitored: true, Phase: "Transcoded"}, "downloaded", "bg-emerald-500"},
		"cutoff unmet":       {projection.LibraryItem{HasFile: true, Monitored: true, Phase: "CutoffUnmet"}, "cutoff-unmet", "bg-amber-500"},
		"cutoff unevaluated": {projection.LibraryItem{HasFile: true, Monitored: true, Phase: "CutoffUnevaluated"}, "cutoff-unmet", "bg-amber-500"},
		"downloading":        {projection.LibraryItem{Monitored: true, Phase: "Downloading"}, "downloading", "bg-sky-500"},
		"delayed":            {projection.LibraryItem{Monitored: true, Phase: "Delayed"}, "downloading", "bg-sky-500"},
		"missing":            {projection.LibraryItem{Monitored: true, Phase: "Wanted"}, "missing", "bg-red-500"},
		"unmonitored":        {projection.LibraryItem{Monitored: false, Phase: "Unmonitored"}, "unmonitored", "bg-slate-500"},
	} {
		t.Run(name, func(t *testing.T) {
			it := tc.item
			it.Ref = types.NamespacedName{Namespace: "default", Name: "x"}
			it.Kind, it.Tab, it.Title = commonv1.MediaKindMovie, projection.TabMovies, "X"
			srv := ui.NewServer(t.Context(), ui.Options{
				Library: func(context.Context) []projection.LibraryItem { return []projection.LibraryItem{it} },
			})
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/library/movies", nil))
			require.Equal(t, http.StatusOK, rec.Code)
			requireTag(t, rec.Body.String(), `data-status="`+tc.status+`"`, tc.class)
		})
	}
}
