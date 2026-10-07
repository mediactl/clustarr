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
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/ui"
	"github.com/mediactl/clustarr/ui/actions"
	"github.com/mediactl/clustarr/ui/projection"
)

// A library card's hover actions (2026-10-06, after Sonarr's poster
// buttons): Refresh & Scan, Search, Monitor or Unmonitor, and Delete on
// every card, shown while the pointer is over it or focus is in it, hidden
// while the page is selecting. Each posts the item page's own action
// through htmx and reads its outcome into the page's #action-status.

func TestEveryCardHasItsHoverActions(t *testing.T) {
	h, _ := bulkServer(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/library/movies", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	require.Equal(t, 2, strings.Count(body, `data-card-actions`), "one action bar per card")
	at := strings.Index(body, `data-ref="media/arrival"`)
	require.GreaterOrEqual(t, at, 0)
	card := body[at:]
	card = card[:strings.Index(card[1:], `data-ref=`)+1]

	bar := requireTag(t, card, `data-card-actions`, "opacity-0", "group-hover/card:opacity-100", "group-focus-within/card:opacity-100", "group-data-selecting/library:hidden")
	require.NotEmpty(t, bar)
	for action, attrs := range map[string][]string{
		"refresh": {`hx-post="/library/media/movie/arrival/refresh"`, `hx-vals="{&#34;scan&#34;:&#34;true&#34;}"`, `aria-label="Refresh &amp; Scan"`},
		"search":  {`hx-post="/library/media/movie/arrival/search"`, `aria-label="Search"`},
		// bulkLibrary's items are unmonitored: the toggle monitors.
		"monitor": {`hx-post="/library/media/movie/arrival/monitor"`, `hx-vals="{&#34;monitored&#34;:&#34;true&#34;}"`, `aria-label="Monitor"`},
	} {
		b := requireTag(t, card, `data-card-action="`+action+`"`, append([]string{`data-slot="button"`, `type="button"`, `hx-target="#action-status"`, `hx-swap="innerHTML"`}, attrs...)...)
		require.NotEmpty(t, b, action)
	}
	// Delete asks first in the page's delete dialog, for this item alone.
	requireTag(t, card, `data-card-action="delete"`, `data-card-delete="media/movie/arrival"`, `data-templ-controls="library-bulk-delete"`, `aria-label="Delete"`)

	requireTag(t, body, `id="action-status"`, `aria-live="polite"`)
	requireTag(t, body, `data-delete-subject`, `data-default="the selected movies"`)

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/static/library.js", nil))
	for _, hook := range []string{"data-card-delete", "data-delete-subject", "data-delete-count", "data-card-link", "action-status"} {
		require.Contains(t, rec.Body.String(), hook)
	}
}

func TestTheMonitorToggleReadsTheItemsState(t *testing.T) {
	items := bulkLibrary()
	items[0].Monitored = true
	srv := ui.NewServer(t.Context(), ui.Options{Library: func(context.Context) []projection.LibraryItem { return items }})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/library/movies", nil))
	requireTag(t, rec.Body.String(), `data-card-action="monitor"`, `hx-post="/library/media/movie/arrival/monitor"`,
		`hx-vals="{&#34;monitored&#34;:&#34;false&#34;}"`, `aria-label="Unmonitor"`)
}

func cardPost(t *testing.T, h http.Handler, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	req.Header.Set("HX-Target", "action-status")
	h.ServeHTTP(rec, req)
	return rec
}

// A card's action is answered in place: 200 with the outcome for
// #action-status, never a redirect that would fetch the whole page, and a
// failure is said there too, since htmx swaps no error status.
func TestACardActionIsAnsweredIntoTheStatusRegion(t *testing.T) {
	h, writer := bulkServer(t, bulkMovie("arrival", false))
	rec := cardPost(t, h, "/library/media/movie/arrival/monitor", url.Values{"monitored": {"true"}})
	require.Equal(t, http.StatusOK, rec.Code)
	requireTag(t, rec.Body.String(), `data-action-status="ok"`)
	require.Contains(t, rec.Body.String(), "Monitored")
	var got catalogv1.Movie
	require.NoError(t, writer.Get(t.Context(), types.NamespacedName{Namespace: "media", Name: "arrival"}, &got))
	require.True(t, *got.Spec.Monitored)

	rec = cardPost(t, h, "/library/media/movie/arrival/search", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	requireTag(t, rec.Body.String(), `data-action-status="ok"`)

	rec = cardPost(t, h, "/library/media/movie/brazil/monitor", url.Values{"monitored": {"true"}})
	require.Equal(t, http.StatusOK, rec.Code, "htmx swaps only a 2xx")
	requireTag(t, rec.Body.String(), `data-action-error="not-found"`)

	// Without a writer the card says so too.
	noWriter := ui.NewServer(t.Context(), ui.Options{Library: func(context.Context) []projection.LibraryItem { return bulkLibrary() }}).Handler()
	rec = cardPost(t, noWriter, "/library/media/movie/arrival/refresh", url.Values{"scan": {"true"}})
	require.Equal(t, http.StatusOK, rec.Code)
	requireTag(t, rec.Body.String(), `data-action-error="no-writer"`)

	// The item page's own forms still redirect.
	plain := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/library/media/movie/arrival/search", strings.NewReader(url.Values{"return": {"/library/movies"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	h.ServeHTTP(plain, req)
	require.Equal(t, http.StatusSeeOther, plain.Code)
}

// A Book's monitor toggle on its author's page swaps its row; from a card
// on the Books tab it answers the status region like any card.
func TestACardsMonitorToggleNeverAnswersWithARow(t *testing.T) {
	book := &catalogv1.Book{
		ObjectMeta: metav1.ObjectMeta{Name: "dune", Namespace: "media"},
		Spec:       catalogv1.BookSpec{Monitored: boolPtr(false)},
	}
	writer := fake.NewClientBuilder().WithScheme(libraryTestScheme(t)).WithObjects(book).Build()
	h := ui.NewServer(t.Context(), ui.Options{
		Actions: actions.New(writer),
		Library: func(context.Context) []projection.LibraryItem {
			return []projection.LibraryItem{{Ref: types.NamespacedName{Namespace: "media", Name: "dune"}, Kind: commonv1.MediaKindBook, Tab: projection.TabBooks, Title: "Dune"}}
		},
	}).Handler()
	rec := cardPost(t, h, "/library/media/book/dune/monitor", url.Values{"monitored": {"true"}})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	requireTag(t, rec.Body.String(), `data-action-status="ok"`)
	require.NotRegexp(t, regexp.MustCompile(`data-child=`), rec.Body.String())
}
