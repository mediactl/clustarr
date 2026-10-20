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
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// A movie's interactive search is Radarr's: an "Interactive Search" button
// beside "Search Movie" in the movie page's toolbar opens the
// "Interactive Search - <title>" modal, the results panel an episode's or
// a season's interactive search shows, polling until the results are in,
// each release with its download button.
func TestMovieToolbarOpensRadarrsInteractiveSearch(t *testing.T) {
	srv, c := movieFixture(t, true)
	page := detailPage(t, srv, "/library/default/movie/nerve")
	bar := section(t, page, `data-toolbar`)
	bar = bar[:strings.Index(bar, `data-hero`)]

	requireTag(t, bar, `data-action="movie-interactive-search"`, `data-slot="button"`, `data-toolbar-button`, `type="submit"`)
	require.Contains(t, bar, "Interactive Search")
	require.Contains(t, bar, `hx-post="/library/default/movie/nerve/search/interactive"`)
	require.Contains(t, bar, `hx-target="#movie-modal"`)
	require.Contains(t, bar, `action="/library/default/movie/nerve/search/interactive"`, "a form post without JavaScript lands on the panel's page")
	require.Less(t, strings.Index(bar, `data-action="search-now"`), strings.Index(bar, `data-action="movie-interactive-search"`),
		"Interactive Search follows Search Movie, as in Radarr")
	require.Contains(t, page, `id="movie-modal"`)

	rec := postForm(t, srv, "/library/default/movie/nerve/search/interactive", url.Values{}, true)
	require.Equal(t, http.StatusOK, rec.Code)
	modal := rec.Body.String()
	var list catalogv1.SearchList
	require.NoError(t, c.List(t.Context(), &list, client.InNamespace("default")))
	require.Len(t, list.Items, 1)
	created := list.Items[0]
	require.Equal(t, &commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "nerve"}, created.Spec.MediaRef)
	require.False(t, created.Spec.GrabBest, "an interactive search grabs only what a person picks")
	require.Contains(t, modal, `data-movie-search-modal="nerve"`)
	require.Contains(t, modal, "Interactive Search - Nerve")
	requireTag(t, modal, `data-search-panel="`+created.Name+`"`,
		`hx-get="/searches/default/`+created.Name+`"`, `hx-trigger="load delay:2s"`)
	require.Contains(t, modal, "Searching indexers")

	rec = postForm(t, srv, "/library/default/movie/nerve/search/interactive", url.Values{}, false)
	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.True(t, strings.HasPrefix(rec.Header().Get("Location"), "/searches/default/nerve-"), rec.Header().Get("Location"))

	rec = postForm(t, srv, "/library/default/movie/nope/search/interactive", url.Values{}, true)
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.NoError(t, c.List(t.Context(), &list, client.InNamespace("default")))
	require.Len(t, list.Items, 2, "only the form post's Search; none for an unknown movie")
}

// The button is a movie's alone: a series searches interactively by
// season and episode, never as a whole.
func TestOnlyAMovieHasTheToolbarsInteractiveSearch(t *testing.T) {
	srv, _ := seasonSearchFixture(t)
	page := getPath(t, srv, "/library/default/series/andor", false).Body.String()
	require.NotContains(t, page, `data-action="movie-interactive-search"`)
	require.NotContains(t, page, `id="movie-modal"`)
}
