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
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/ui"
	"github.com/mediactl/clustarr/ui/actions"
	"github.com/mediactl/clustarr/ui/projection"
)

// The library's mass editor (2026-10-06, after Sonarr's): Select on the
// toolbar marks cards, and the bulk bar posts the selection to
// /library/{tab}/bulk, which runs each item page's own action.

func bulkMovie(name string, monitored bool) *catalogv1.Movie {
	return &catalogv1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "media"},
		Spec:       catalogv1.MovieSpec{Monitored: boolPtr(monitored), QualityProfileRef: "hd", RootFolderRef: "movies", TmdbID: 1},
	}
}

func bulkLibrary() []projection.LibraryItem {
	mk := func(name string, kind commonv1.MediaKind, tab projection.Tab) projection.LibraryItem {
		return projection.LibraryItem{Ref: types.NamespacedName{Namespace: "media", Name: name}, Kind: kind, Tab: tab, Title: name}
	}
	return []projection.LibraryItem{
		mk("arrival", commonv1.MediaKindMovie, projection.TabMovies),
		mk("heat", commonv1.MediaKindMovie, projection.TabMovies),
		mk("andor", commonv1.MediaKindSeries, projection.TabTV),
	}
}

func bulkServer(t *testing.T, objs ...client.Object) (http.Handler, client.Client) {
	t.Helper()
	writer := fake.NewClientBuilder().WithScheme(libraryTestScheme(t)).WithObjects(objs...).Build()
	srv := ui.NewServer(t.Context(), ui.Options{
		Actions: actions.New(writer),
		Library: func(context.Context) []projection.LibraryItem { return bulkLibrary() },
	})
	return srv.Handler(), writer
}

func bulkPost(t *testing.T, h http.Handler, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	h.ServeHTTP(rec, req)
	return rec
}

func TestBulkUnmonitorPatchesEverySelectedItemAndReturnsToTheTab(t *testing.T) {
	h, writer := bulkServer(t, bulkMovie("arrival", true), bulkMovie("heat", true))
	rec := bulkPost(t, h, "/library/movies/bulk", url.Values{
		"action": {"unmonitor"}, "item": {"media/movie/arrival", "media/movie/heat"}, "return": {"/library/movies"},
	})
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	require.Equal(t, "/library/movies", rec.Header().Get("Location"))
	for _, name := range []string{"arrival", "heat"} {
		var got catalogv1.Movie
		require.NoError(t, writer.Get(t.Context(), types.NamespacedName{Namespace: "media", Name: name}, &got))
		require.False(t, *got.Spec.Monitored, name)
	}

	rec = bulkPost(t, h, "/library/movies/bulk", url.Values{"action": {"monitor"}, "item": {"media/movie/heat"}})
	require.Equal(t, http.StatusSeeOther, rec.Code)
	var heat catalogv1.Movie
	require.NoError(t, writer.Get(t.Context(), types.NamespacedName{Namespace: "media", Name: "heat"}, &heat))
	require.True(t, *heat.Spec.Monitored)
}

// The form reaches only what the tab shows: an item of another tab, one
// not in the library, a malformed one, no selection and an unknown action
// are refused before anything is written.
func TestBulkRefusesWhatTheTabDoesNotShow(t *testing.T) {
	h, writer := bulkServer(t, bulkMovie("arrival", true))
	for name, form := range map[string]url.Values{
		"another tab":    {"action": {"unmonitor"}, "item": {"media/movie/arrival", "media/series/andor"}},
		"not in library": {"action": {"unmonitor"}, "item": {"media/movie/arrival", "media/movie/brazil"}},
		"malformed":      {"action": {"unmonitor"}, "item": {"media/arrival"}},
		"nothing":        {"action": {"unmonitor"}},
		"unknown action": {"action": {"explode"}, "item": {"media/movie/arrival"}},
	} {
		rec := bulkPost(t, h, "/library/movies/bulk", form)
		require.Equal(t, http.StatusBadRequest, rec.Code, name)
		require.Contains(t, rec.Body.String(), `data-action-error="invalid"`, name)
	}
	var got catalogv1.Movie
	require.NoError(t, writer.Get(t.Context(), types.NamespacedName{Namespace: "media", Name: "arrival"}, &got))
	require.True(t, *got.Spec.Monitored, "a refused form writes nothing, not even its valid items")
	require.Equal(t, http.StatusNotFound, bulkPost(t, h, "/library/podcasts/bulk", url.Values{"action": {"monitor"}}).Code)
}

func TestBulkDeleteRequestsEachItemsDelete(t *testing.T) {
	h, writer := bulkServer(t, bulkMovie("arrival", true), bulkMovie("heat", true))
	rec := bulkPost(t, h, "/library/movies/bulk", url.Values{
		"action": {"delete"}, "item": {"media/movie/arrival", "media/movie/heat"}, "files": {"true"}, "exclude": {"true"},
	})
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	for _, name := range []string{"arrival", "heat"} {
		var got catalogv1.Movie
		require.NoError(t, writer.Get(t.Context(), types.NamespacedName{Namespace: "media", Name: name}, &got))
		require.Equal(t, string(catalogv1.DeleteFiles), got.Annotations[catalogv1.AnnotationDelete], name)
		require.Equal(t, "true", got.Annotations[catalogv1.AnnotationDeleteAddExclusion], name)
	}
}

// One item's failure does not stop the others; the failures are reported.
func TestBulkAttemptsEveryItemAndReportsTheFailures(t *testing.T) {
	h, writer := bulkServer(t, bulkMovie("heat", true)) // arrival is in the library view but not the cluster
	rec := bulkPost(t, h, "/library/movies/bulk", url.Values{"action": {"unmonitor"}, "item": {"media/movie/arrival", "media/movie/heat"}})
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Contains(t, rec.Body.String(), "arrival")
	var heat catalogv1.Movie
	require.NoError(t, writer.Get(t.Context(), types.NamespacedName{Namespace: "media", Name: "heat"}, &heat))
	require.False(t, *heat.Spec.Monitored, "the item after the failure was still patched")
}

func TestLibraryPageHasTheMassEditor(t *testing.T) {
	h, _ := bulkServer(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/library/movies", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	requireTag(t, body, `id="library-page"`, `group/library`, `data-tab="movies"`)
	bar := requireTag(t, body, `data-bulk-bar`, "sticky", "bottom-0", "hidden", "group-data-selecting/library:flex")
	require.NotEmpty(t, bar)
	requireTag(t, body, `id="library-bulk"`, `data-bulk-form`, `action="/library/movies/bulk"`, `method="post"`)
	for _, action := range []string{"monitor", "unmonitor", "search", "refresh"} {
		b := requireTag(t, body, `data-bulk-action="`+action+`"`, `name="action"`, `value="`+action+`"`, `type="submit"`)
		require.Regexp(t, regexp.MustCompile(`\sdisabled(\s|>)`), b, "%s waits for a selection", action)
	}
	del := requireTag(t, body, `data-bulk-action="delete"`, `data-slot="dialog-trigger"`)
	require.Regexp(t, regexp.MustCompile(`\sdisabled(\s|>)`), del)
	// Delete asks first, in its own form: the dialog moves to <body>.
	require.Regexp(t, regexp.MustCompile(`<input type="hidden" name="action" value="delete">`), body)
	require.Equal(t, 2, strings.Count(body, `data-bulk-form`), "the bar's form and the delete dialog's")
	requireTag(t, body, `id="bulk-delete-files"`, `name="files"`)
	require.Contains(t, body, `data-selected-count`)

	// Each card has its selection mark, shown only while selecting.
	card := requireTag(t, body, `data-ref="media/arrival"`, "group/card", "data-selected:ring-2")
	require.NotEmpty(t, card)
	require.Equal(t, 2, strings.Count(body, `data-select-mark`), "one mark per card")
	require.Contains(t, body, `data-card-details`)
	require.Contains(t, body, `var(--library-poster-width,160px)`, "the grid's column is the Options menu's poster size")

	headEnd := strings.Index(body, "</head>")
	require.GreaterOrEqual(t, headEnd, 0)
	head := body[:headEnd]
	require.Regexp(t, regexp.MustCompile(`<script[^>]*src="/static/library.js"`), head)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/static/library.js", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	for _, hook := range []string{
		"data-selecting", "select-toggle", "select-all", "data-bulk-form", "data-bulk-action",
		"data-selected-count", `name = "item"`, "data-poster-size", "--library-poster-width", "data-library-details",
		"menubar-value-change", "menubar-checked-change", "htmx:sseMessage",
	} {
		require.Contains(t, rec.Body.String(), hook)
	}
}
