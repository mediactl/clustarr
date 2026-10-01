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

// The library typeahead (2026-10-01, after Sonarr's and Radarr's header
// search): a box on every tab whose dropdown lists the tab's monitored
// items matching what was typed, each a link to the item's page.

func findLibrary() []projection.LibraryItem {
	mk := func(name, title string, kind commonv1.MediaKind, tab projection.Tab, monitored bool, year int32) projection.LibraryItem {
		return projection.LibraryItem{
			Ref: types.NamespacedName{Namespace: "media", Name: name}, Kind: kind, Tab: tab,
			Title: title, Monitored: monitored, Year: year, Poster: "/art/media/" + name + "/poster",
		}
	}
	return []projection.LibraryItem{
		mk("blade-runner", "Blade Runner", commonv1.MediaKindMovie, projection.TabMovies, true, 1982),
		mk("blade", "Blade", commonv1.MediaKindMovie, projection.TabMovies, true, 1998),
		mk("blade-ii", "Blade II", commonv1.MediaKindMovie, projection.TabMovies, false, 2002),
		mk("blade-of-the-immortal", "Blade of the Immortal", commonv1.MediaKindSeries, projection.TabTV, true, 2019),
	}
}

func findServer(t *testing.T) http.Handler {
	t.Helper()
	return ui.NewServer(t.Context(), ui.Options{
		Library: func(context.Context) []projection.LibraryItem { return findLibrary() },
	}).Handler()
}

func findGet(t *testing.T, h http.Handler, path string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec.Code, rec.Body.String()
}

func TestLibraryFindListsTheTabsMonitoredMatchesBestFirst(t *testing.T) {
	code, body := findGet(t, findServer(t), "/library/movies/find?q=blade")
	require.Equal(t, http.StatusOK, code)

	var refs []string
	for _, m := range regexp.MustCompile(`data-find-ref="([^"]+)"`).FindAllStringSubmatch(body, -1) {
		refs = append(refs, m[1])
	}
	require.Equal(t, []string{"media/blade", "media/blade-runner"}, refs,
		"the exact title first; Blade II is unmonitored and Blade of the Immortal is TV")

	first := requireTag(t, body, `data-find-ref="media/blade"`)
	require.Contains(t, first, `href="/library/media/movie/blade"`, "a result links to the item's page")
	require.Contains(t, body, `src="/art/media/blade/poster"`, "a result shows the poster")
	require.Contains(t, body, "1998", "a result shows the year")
}

func TestLibraryFindSaysWhenNothingMatchesAndOffersAddNew(t *testing.T) {
	code, body := findGet(t, findServer(t), "/library/movies/find?q=zardoz")
	require.Equal(t, http.StatusOK, code)
	require.NotContains(t, body, "data-find-ref=")
	require.Contains(t, body, "data-find-empty")
	require.Contains(t, body, "No monitored movies match")
	requireTag(t, body, `data-action="find-add-new"`, `href="/library/movies/add?q=zardoz"`)
}

func TestLibraryFindRendersNothingForABlankQuery(t *testing.T) {
	code, body := findGet(t, findServer(t), "/library/movies/find?q=+")
	require.Equal(t, http.StatusOK, code)
	require.Empty(t, strings.TrimSpace(body), "the dropdown closes on an empty box")
}

func TestLibraryFindRefusesAnUnknownTab(t *testing.T) {
	code, _ := findGet(t, findServer(t), "/library/podcasts/find?q=blade")
	require.Equal(t, http.StatusNotFound, code)
}

func TestEveryLibraryTabHasTheFindBox(t *testing.T) {
	h := findServer(t)
	for _, tab := range projection.Tabs() {
		code, body := findGet(t, h, "/library/"+string(tab))
		require.Equal(t, http.StatusOK, code, tab)
		box := requireTag(t, body, `data-find-input`)
		require.Contains(t, box, `hx-get="/library/`+string(tab)+`/find"`, tab)
		require.Contains(t, box, `name="q"`, tab)
		require.Contains(t, body, `id="library-find-results"`, tab)
	}
}

func TestAddNewTakesAQueryFromTheURL(t *testing.T) {
	code, body := findGet(t, findServer(t), "/library/movies/add?q=zardoz")
	require.Equal(t, http.StatusOK, code)
	box := requireTag(t, body, `name="q"`, `type="search"`)
	require.Contains(t, box, `value="zardoz"`, "the search box is filled in")
	require.Contains(t, box, "load", "and searches as the page loads")
}

func TestTheFindKeysScriptIsServedAndLoaded(t *testing.T) {
	h := findServer(t)
	code, js := findGet(t, h, "/static/find.js")
	require.Equal(t, http.StatusOK, code)
	require.Contains(t, js, "data-find-input", "the script drives the typeahead box")
	_, page := findGet(t, h, "/library/movies")
	require.Regexp(t, regexp.MustCompile(`<script[^>]*src="/static/find.js"`), page)
}
