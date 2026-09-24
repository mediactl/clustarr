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
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/ui"
	"github.com/mediactl/clustarr/ui/actions"
	"github.com/mediactl/clustarr/ui/projection"
)

// libraryTestScheme registers the one API group ui.NewReaderScheme would
// register that these tests need: catalog (Movie and LibraryScan/RootFolder
// live there, and actions.New's writer needs the scheme too, since it
// builds real typed objects to Create and Patch).
func libraryTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme, err := ui.NewReaderScheme()
	require.NoError(t, err)
	return scheme
}

// TestLibraryPageRendersWithoutACluster proves GET /library survives
// Options.Library being nil (no cluster configured at all), mirroring
// ui/downloads_test.go's TestDownloadsPageRendersWithoutACluster.
func TestLibraryPageRendersWithoutACluster(t *testing.T) {
	srv := ui.NewServer(t.Context(), ui.Options{})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/library/movies", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "Nothing in the library yet.")
}

// TestLibraryPageRendersItemsWithDataAttributes proves the handler wiring
// end to end: a fixture LibraryItem shows up in the rendered page with the
// data-ref, data-kind, data-monitored, data-phase and data-hasfile
// attributes ruling R8 requires tests to assert against instead of visible
// copy.
func TestLibraryPageRendersItemsWithDataAttributes(t *testing.T) {
	item := projection.LibraryItem{
		Ref:       types.NamespacedName{Namespace: "default", Name: "arrival"},
		Kind:      commonv1.MediaKindMovie,
		Tab:       projection.TabMovies,
		Title:     "Arrival",
		Monitored: true,
		Phase:     "Wanted",
		HasFile:   false,
	}

	srv := ui.NewServer(t.Context(), ui.Options{
		Library: func(context.Context) []projection.LibraryItem { return []projection.LibraryItem{item} },
	})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/library/movies", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	body := rec.Body.String()
	require.Contains(t, body, `data-ref="default/arrival"`)
	require.Contains(t, body, `data-kind="movie"`)
	require.Contains(t, body, `data-monitored="true"`)
	require.Contains(t, body, `data-phase="Wanted"`)
	require.Contains(t, body, `data-hasfile="false"`)
	require.Contains(t, body, `href="/library/default/movie/arrival"`)
}

// TestLibraryPageShowsAnUnevaluatedCutoffAsAWarning pins the badge colour
// of the CutoffUnevaluated phase (X1 added it to Movie and Episode): an item
// whose quality profile could not be resolved needs the user's attention,
// like CutoffUnmet, so it takes the amber warning badge rather than the
// neutral in-flight sky blue every phase this switch does not name gets.
func TestLibraryPageShowsAnUnevaluatedCutoffAsAWarning(t *testing.T) {
	for _, phase := range []string{"CutoffUnevaluated", "CutoffUnmet"} {
		item := projection.LibraryItem{
			Ref:  types.NamespacedName{Namespace: "default", Name: "arrival"},
			Kind: commonv1.MediaKindMovie, Tab: projection.TabMovies, Title: "Arrival", Monitored: true, Phase: phase,
		}
		srv := ui.NewServer(t.Context(), ui.Options{
			Library: func(context.Context) []projection.LibraryItem { return []projection.LibraryItem{item} },
		})
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/library/movies", nil))
		require.Equal(t, http.StatusOK, rec.Code)
		require.Contains(t, rec.Body.String(), `bg-amber-500/20 text-amber-300">`+phase+`</span>`,
			"phase %s is not shown with the amber warning badge", phase)
	}
}

// TestLibraryPageShowsTranscodedAsDone pins the badge colour of the
// Transcoded phase (gap fix T1): a transcoded file is final, as settled as
// Imported, so it takes the same emerald "done" badge rather than the
// in-flight sky blue every phase the switch does not name gets.
func TestLibraryPageShowsTranscodedAsDone(t *testing.T) {
	for _, phase := range []string{"Transcoded", "Imported"} {
		item := projection.LibraryItem{
			Ref:  types.NamespacedName{Namespace: "default", Name: "arrival"},
			Kind: commonv1.MediaKindMovie, Tab: projection.TabMovies, Title: "Arrival", Monitored: true, Phase: phase, HasFile: true,
		}
		srv := ui.NewServer(t.Context(), ui.Options{
			Library: func(context.Context) []projection.LibraryItem { return []projection.LibraryItem{item} },
		})
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/library/movies", nil))
		require.Equal(t, http.StatusOK, rec.Code)
		require.Contains(t, rec.Body.String(), `data-phase="`+phase+`"`)
		require.Contains(t, rec.Body.String(), `bg-emerald-500/20 text-emerald-300">`+phase+`</span>`,
			"phase %s is not shown with the emerald done badge", phase)
	}
}

// TestLibraryPageRendersRescanToolbarPerRootFolder proves listRootFolders'
// wiring: a RootFolder seeded into a fake, scheme-matched Reader shows up as
// its own rescan form in the toolbar.
func TestLibraryPageRendersRescanToolbarPerRootFolder(t *testing.T) {
	scheme := libraryTestScheme(t)
	rf := &catalogv1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Name: "movies", Namespace: "default"},
		Spec:       catalogv1.RootFolderSpec{Path: "/data/media/movies", Kind: catalogv1.RootFolderKindMovie},
	}
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(rf).Build()

	srv := ui.NewServer(t.Context(), ui.Options{Reader: reader})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/library/movies", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `data-root-folder="movies"`)
}

// TestLibraryDetailPageRendersActionsAndAttributes proves GET
// /library/{namespace}/{kind}/{name} finds the item in the current library
// projection and renders its own monitor/unmonitor and search-now forms
// (§A3.2's per-item actions -- rescan is RootFolder-scoped, so it does not
// appear here).
func TestLibraryDetailPageRendersActionsAndAttributes(t *testing.T) {
	item := projection.LibraryItem{
		Ref:       types.NamespacedName{Namespace: "default", Name: "arrival"},
		Kind:      commonv1.MediaKindMovie,
		Tab:       projection.TabMovies,
		Title:     "Arrival",
		Monitored: true,
		Phase:     "Wanted",
	}
	srv := ui.NewServer(t.Context(), ui.Options{
		Library: func(context.Context) []projection.LibraryItem { return []projection.LibraryItem{item} },
	})

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/library/default/movie/arrival", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	body := rec.Body.String()
	require.Contains(t, body, `data-ref="default/arrival"`)
	require.Contains(t, body, `data-monitored="true"`)
	require.Contains(t, body, `action="/library/default/movie/arrival/monitor"`)
	require.Contains(t, body, `action="/library/default/movie/arrival/search"`)
	require.Contains(t, body, `data-action="set-monitored"`)
	require.Contains(t, body, `data-action="search-now"`)
	require.Contains(t, body, "Unmonitor", "a monitored item's toggle button offers to unmonitor it")
}

// TestLibraryDetailPageNotFoundForAnUnknownItem proves a namespace/kind/name
// with no match in the current library projection 404s rather than panics
// or renders an empty page as if it were a real item.
func TestLibraryDetailPageNotFoundForAnUnknownItem(t *testing.T) {
	srv := ui.NewServer(t.Context(), ui.Options{})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/library/default/movie/nope", nil))
	require.Equal(t, http.StatusNotFound, rec.Code)
}

// TestSetMonitoredActionWithNoWriterRendersVisibleError is this task's own
// instruction, verified directly: a ui process with no cluster configured
// has a nil Options.Actions (cmd/clustarr wires it only when a kubeconfig
// resolves, since Task G3-5), and must still answer with a visible,
// machine-checkable error rather than silently doing nothing or panicking.
func TestSetMonitoredActionWithNoWriterRendersVisibleError(t *testing.T) {
	srv := ui.NewServer(t.Context(), ui.Options{})
	rec := httptest.NewRecorder()
	form := url.Values{"monitored": {"false"}}
	req := httptest.NewRequest(http.MethodPost, "/library/default/movie/arrival/monitor", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Contains(t, rec.Body.String(), `data-action-error="no-writer"`)
	require.Contains(t, rec.Body.String(), "Action failed")
}

// TestSearchNowActionWithNoWriterRendersVisibleError is
// TestSetMonitoredActionWithNoWriterRendersVisibleError's "search now"
// counterpart.
func TestSearchNowActionWithNoWriterRendersVisibleError(t *testing.T) {
	srv := ui.NewServer(t.Context(), ui.Options{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/library/default/movie/arrival/search", nil)
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Contains(t, rec.Body.String(), `data-action-error="no-writer"`)
}

// TestRescanActionWithNoWriterRendersVisibleError is
// TestSetMonitoredActionWithNoWriterRendersVisibleError's "rescan"
// counterpart.
func TestRescanActionWithNoWriterRendersVisibleError(t *testing.T) {
	srv := ui.NewServer(t.Context(), ui.Options{})
	rec := httptest.NewRecorder()
	form := url.Values{"namespace": {"default"}, "rootFolder": {"movies"}}
	req := httptest.NewRequest(http.MethodPost, "/library/rescan", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Contains(t, rec.Body.String(), `data-action-error="no-writer"`)
}

// TestSetMonitoredActionSucceedsAndRedirects proves the success path end to
// end against a real (fake) writer: the Movie's spec.monitored is patched,
// and the handler redirects (303) to the form's "return" field.
func TestSetMonitoredActionSucceedsAndRedirects(t *testing.T) {
	scheme := libraryTestScheme(t)
	movie := &catalogv1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: "arrival", Namespace: "default"},
		Spec:       catalogv1.MovieSpec{Monitored: boolPtr(true), QualityProfileRef: "hd", RootFolderRef: "movies", TmdbID: 1},
	}
	writer := fake.NewClientBuilder().WithScheme(scheme).WithObjects(movie).Build()

	srv := ui.NewServer(t.Context(), ui.Options{Actions: actions.New(writer)})
	rec := httptest.NewRecorder()
	form := url.Values{"monitored": {"false"}, "return": {"/library/default/movie/arrival"}}
	req := httptest.NewRequest(http.MethodPost, "/library/default/movie/arrival/monitor", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.Equal(t, "/library/default/movie/arrival", rec.Header().Get("Location"))

	var got catalogv1.Movie
	require.NoError(t, writer.Get(t.Context(), types.NamespacedName{Namespace: "default", Name: "arrival"}, &got))
	require.NotNil(t, got.Spec.Monitored)
	require.False(t, *got.Spec.Monitored)
}

// TestRescanActionSucceedsAndRedirects proves the "rescan" action creates a
// LibraryScan of the named RootFolder and redirects to /library by default
// when the form supplies no "return" field.
func TestRescanActionSucceedsAndRedirects(t *testing.T) {
	scheme := libraryTestScheme(t)
	writer := fake.NewClientBuilder().WithScheme(scheme).Build()

	srv := ui.NewServer(t.Context(), ui.Options{Actions: actions.New(writer)})
	rec := httptest.NewRecorder()
	form := url.Values{"namespace": {"default"}, "rootFolder": {"movies"}}
	req := httptest.NewRequest(http.MethodPost, "/library/rescan", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.Equal(t, "/library", rec.Header().Get("Location"))

	var scans catalogv1.LibraryScanList
	require.NoError(t, writer.List(t.Context(), &scans))
	require.Len(t, scans.Items, 1)
	require.Equal(t, "movies", scans.Items[0].Spec.RootFolderRef)
}

// TestLibraryNavLinksOnEveryPage covers the same nav-consistency bullet
// ui/downloads_test.go's TestDownloadsNavLinkIsOnEveryPage does, extended to
// the two pages this task adds.
func TestLibraryNavLinksOnEveryPage(t *testing.T) {
	srv := ui.NewServer(t.Context(), ui.Options{})

	for _, path := range []string{"/pipeline", "/downloads", "/library/movies", "/unmatched"} {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		require.Contains(t, rec.Body.String(), `href="/library"`, "page %s is missing the Library nav link", path)
		require.Contains(t, rec.Body.String(), `href="/unmatched"`, "page %s is missing the Unmatched nav link", path)
	}
}

// TestLibraryEventsStreamReflectsAStatusChangeBetweenTicks mirrors
// ui/server_test.go's downloads-stream test of the same shape: pushing two
// slices on Options.SubscribeLibrary's channel, one per simulated tick,
// exercises the "next slice this channel yields" contract without waiting
// on a real timer.
func TestLibraryEventsStreamReflectsAStatusChangeBetweenTicks(t *testing.T) {
	wanted := projection.LibraryItem{
		Ref: types.NamespacedName{Namespace: "default", Name: "arrival"}, Kind: commonv1.MediaKindMovie,
		Tab: projection.TabMovies, Title: "Arrival", Monitored: true, Phase: "Wanted",
	}
	imported := wanted
	imported.Phase = "Imported"
	imported.HasFile = true

	ch := make(chan []projection.LibraryItem, 2)
	ch <- []projection.LibraryItem{wanted}

	srv := ui.NewServer(t.Context(), ui.Options{
		SubscribeLibrary: func() (<-chan []projection.LibraryItem, func()) { return ch, func() {} },
	})

	httpSrv := httptest.NewServer(srv.Handler())
	defer httpSrv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, httpSrv.URL+"/events/library/movies", nil)
	require.NoError(t, err)
	resp, err := httpSrv.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	reader := bufio.NewReader(resp.Body)
	first := readSSEEvent(t, reader)
	require.Contains(t, first, `data-phase="Wanted"`)

	ch <- []projection.LibraryItem{imported}
	second := readSSEEvent(t, reader)
	require.Contains(t, second, `data-phase="Imported"`)
	require.Contains(t, second, `data-hasfile="true"`)
	require.NotContains(t, second, `data-phase="Wanted"`,
		"the second frame must reflect the status change, not repeat the first frame's phase")
}

func boolPtr(b bool) *bool { return &b }

// The library is one tab per media type (spec 2026-09-23-library-page-design):
// /library lands on Movies, and a tab that is not one of the four is not
// found rather than an empty grid.
func TestLibraryRedirectsToTheMoviesTabAndRefusesOtherTabs(t *testing.T) {
	srv := ui.NewServer(t.Context(), ui.Options{})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/library", nil))
	require.Equal(t, http.StatusFound, rec.Code)
	require.Equal(t, "/library/movies", rec.Header().Get("Location"))

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/library/series", nil))
	require.Equal(t, http.StatusNotFound, rec.Code)
}

func tabFixtures() (movie, series projection.LibraryItem) {
	movie = projection.LibraryItem{
		Ref: types.NamespacedName{Namespace: "default", Name: "arrival"}, Kind: commonv1.MediaKindMovie,
		Tab: projection.TabMovies, Title: "Arrival", Year: 2016, Poster: "https://img.example/arrival.jpg",
		QualityProfileRef: "hd-bluray-web", Monitored: true, Phase: "Imported", HasFile: true,
	}
	series = projection.LibraryItem{
		Ref: types.NamespacedName{Namespace: "default", Name: "andor"}, Kind: commonv1.MediaKindSeries,
		Tab: projection.TabTV, Title: "Andor", Year: 2022, QualityProfileRef: "web-1080p", Monitored: false,
	}
	return movie, series
}

// A tab renders only its own kind's cards, each with the poster (hotlinked,
// lazily, without a referrer), the year, the monitored badge and the quality
// profile; a card with no poster yet renders a placeholder, never a broken
// image. The tab strip links every tab and marks the current one.
func TestLibraryTabRendersItsOwnCardsWithArtYearAndProfile(t *testing.T) {
	movie, series := tabFixtures()
	srv := ui.NewServer(t.Context(), ui.Options{
		Library: func(context.Context) []projection.LibraryItem { return []projection.LibraryItem{movie, series} },
	})

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/library/tv", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.Contains(t, body, `data-ref="default/andor"`)
	require.NotContains(t, body, `data-ref="default/arrival"`, "a movie is not on the TV tab")
	require.Contains(t, body, `data-tab="tv"`)
	require.Contains(t, body, `data-profile="web-1080p"`)
	require.Contains(t, body, `data-year="2022"`)
	require.Contains(t, body, `data-monitored="false"`)
	require.Contains(t, body, `data-poster="none"`, "no poster yet renders a placeholder")
	require.NotContains(t, body, `<img`, "no poster means no image tag")
	require.Contains(t, body, `sse-connect="/events/library/tv?page=1&amp;per=50"`, "the stream carries the page's own window")
	for _, tab := range projection.Tabs() {
		require.Contains(t, body, fmt.Sprintf(`href="/library/%s"`, tab))
	}

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/library/movies", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	body = rec.Body.String()
	require.Contains(t, body, `data-ref="default/arrival"`)
	require.NotContains(t, body, `data-ref="default/andor"`)
	require.Contains(t, body, `src="https://img.example/arrival.jpg"`)
	require.Contains(t, body, `loading="lazy"`)
	require.Contains(t, body, `referrerpolicy="no-referrer"`)
	require.Contains(t, body, `data-profile="hd-bluray-web"`)
}

// A tab's stream carries only that tab's rows, so a movie landing does not
// redraw the TV grid; a stream for a tab that does not exist is not found.
func TestLibraryTabStreamCarriesOnlyItsRows(t *testing.T) {
	movie, series := tabFixtures()
	ch := make(chan []projection.LibraryItem, 1)
	ch <- []projection.LibraryItem{movie, series}
	srv := ui.NewServer(t.Context(), ui.Options{
		SubscribeLibrary: func() (<-chan []projection.LibraryItem, func()) { return ch, func() {} },
	})
	httpSrv := httptest.NewServer(srv.Handler())
	defer httpSrv.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, httpSrv.URL+"/events/library/tv", nil)
	require.NoError(t, err)
	resp, err := httpSrv.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	first := readSSEEvent(t, bufio.NewReader(resp.Body))
	require.Contains(t, first, `data-ref="default/andor"`)
	require.NotContains(t, first, `data-ref="default/arrival"`)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/events/library/nope", nil))
	require.Equal(t, http.StatusNotFound, rec.Code)
}
