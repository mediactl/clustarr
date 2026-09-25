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
	"net/http"
	"net/http/httptest"
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
	"github.com/mediactl/clustarr/ui/projection"
)

// The library toolbar (design 2026-09-24, after Radarr's): the rescan
// buttons on the left, and on the right a Sort menu and a Filter menu,
// each a shadcn-templ dropdown menu of links that swap the page through
// htmx the way the tabs do. The view -- ?sort, ?dir and ?filter -- rides
// every link on the page (pager, stream, jump bar, the menus themselves),
// defaults are left off the URL so two links to one view are identical,
// and the A-Z bar shows only under the title sort, where it means
// something.

var activeAttr = regexp.MustCompile(`\sdata-active(\s|>)`)

func mixedLibrary() []projection.LibraryItem {
	mk := func(name string, monitored, hasFile bool, phase string, year int32, profile string) projection.LibraryItem {
		return projection.LibraryItem{
			Ref: types.NamespacedName{Namespace: "default", Name: strings.ToLower(name)}, Kind: commonv1.MediaKindMovie,
			Tab: projection.TabMovies, Title: name, Monitored: monitored, HasFile: hasFile, Phase: phase, Year: year, QualityProfileRef: profile,
		}
	}
	return []projection.LibraryItem{
		mk("Alien", false, false, "Unmonitored", 1979, "sd"),
		mk("Arrival", true, true, "Imported", 2016, "uhd"),
		mk("Brazil", true, false, "Wanted", 1985, "hd"),
		mk("Dune", true, false, "Downloading", 2021, "uhd"),
		mk("Heat", true, true, "CutoffUnmet", 1995, "hd"),
	}
}

func refsInOrder(body string) []string {
	var out []string
	for _, m := range regexp.MustCompile(`data-ref="default/([a-z-]+)"`).FindAllStringSubmatch(body, -1) {
		out = append(out, m[1])
	}
	return out
}

func TestLibraryToolbarHasRescanSortAndFilter(t *testing.T) {
	rf := &catalogv1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Name: "movies", Namespace: "default"},
		Spec:       catalogv1.RootFolderSpec{Path: "/data/media/movies", Kind: catalogv1.RootFolderKindMovie},
	}
	rf4k := &catalogv1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Name: "movies-4k", Namespace: "default"},
		Spec:       catalogv1.RootFolderSpec{Path: "/data/media/movies-4k", Kind: catalogv1.RootFolderKindMovie},
	}
	srv := ui.NewServer(t.Context(), ui.Options{
		Reader:  fake.NewClientBuilder().WithScheme(libraryTestScheme(t)).WithObjects(rf, rf4k).Build(),
		Library: func(context.Context) []projection.LibraryItem { return letteredLibrary(120) },
	})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/library/movies?per=25", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	toolbar := tagWith(t, body, `data-toolbar`)
	for _, attr := range []string{`data-current-sort="title"`, `data-current-dir="asc"`, `data-current-filter="all"`} {
		require.Contains(t, toolbar, attr, "the toolbar states the default view")
	}
	toolbarAt := strings.Index(body, `data-toolbar`)
	require.GreaterOrEqual(t, toolbarAt, 0)
	at := strings.LastIndex(body[:toolbarAt], "<")
	rows := strings.Index(body, `id="library-rows"`)
	require.Less(t, at, rows, "the toolbar sits above the rows")
	bar := body[at:rows]

	// One "Rescan" for the page's library (2026-09-24): a single form
	// carrying the tab, not one per RootFolder -- two movie folders, one
	// button -- and the handler fans out to every RootFolder of the tab's
	// kinds.
	require.Equal(t, 1, strings.Count(bar, `data-action="rescan"`), "one rescan button for the page")
	require.NotContains(t, bar, `data-root-folder=`)
	requireTag(t, bar, `data-tab="movies"`, `action="/library/rescan"`, `method="post"`)
	requireTag(t, bar, `name="tab"`, `value="movies"`)
	requireTag(t, bar, `name="return"`, `value="/library/movies"`)
	rescan := requireTag(t, bar, `data-action="rescan"`, `data-slot="button"`, `type="submit"`)
	require.NotRegexp(t, regexp.MustCompile(`\sdisabled(\s|>)`), rescan, "a RootFolder of the tab's kind exists")
	require.Regexp(t, regexp.MustCompile(`(?s)data-action="rescan"[^>]*>.*?<span>Rescan</span>`), bar, "the label is plain Rescan")

	require.Equal(t, 2, strings.Count(bar, `data-tui-dropdownmenu-trigger`), "a Sort trigger and a Filter trigger")
	require.Regexp(t, regexp.MustCompile(`data-tui-dropdownmenu-trigger[^>]*>[^<]*(<[^>]*>[^<]*)*Sort`), bar)
	require.Regexp(t, regexp.MustCompile(`data-tui-dropdownmenu-trigger[^>]*>[^<]*(<[^>]*>[^<]*)*Filter`), bar)

	for _, s := range projection.LibrarySorts() {
		requireTag(t, bar, `data-sort="`+string(s)+`"`, `data-slot="dropdown-menu-item"`, `hx-get="`,
			`hx-target="#library-page"`, `hx-select="#library-page"`, `hx-swap="outerHTML"`, `hx-push-url="true"`)
		require.Contains(t, bar, ">"+s.Label()+"<", "the menu prints the sort's label")
	}
	title := tagWith(t, bar, `data-sort="title"`)
	require.Regexp(t, activeAttr, title, "the current sort is marked")
	require.Contains(t, title, `href="/library/movies?dir=desc&amp;per=25"`, "the current sort's link flips its direction")
	year := tagWith(t, bar, `data-sort="year"`)
	require.NotRegexp(t, activeAttr, year)
	require.Contains(t, year, `href="/library/movies?per=25&amp;sort=year"`)
	require.Contains(t, year, `hx-get="/library/movies?per=25&amp;sort=year"`)

	for _, f := range projection.LibraryFilters() {
		requireTag(t, bar, `data-filter="`+string(f)+`"`, `data-slot="dropdown-menu-item"`, `hx-get="`,
			`hx-target="#library-page"`, `hx-select="#library-page"`, `hx-swap="outerHTML"`, `hx-push-url="true"`)
		require.Contains(t, bar, ">"+f.Label()+"<", "the menu prints the filter's label")
	}
	require.Regexp(t, activeAttr, tagWith(t, bar, `data-filter="all"`))
	missing := tagWith(t, bar, `data-filter="missing"`)
	require.NotRegexp(t, activeAttr, missing)
	require.Contains(t, missing, `href="/library/movies?filter=missing&amp;per=25"`)
}

func TestLibraryViewRidesEveryLink(t *testing.T) {
	srv := ui.NewServer(t.Context(), ui.Options{
		Library: func(context.Context) []projection.LibraryItem { return letteredLibrary(120) },
	})
	get := func(path string) string {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, http.StatusOK, rec.Code, path)
		return rec.Body.String()
	}

	// letteredLibrary is unmonitored throughout, so this filter keeps all
	// 120 titles and the pages stay where they were.
	body := get("/library/movies?sort=year&dir=desc&filter=unmonitored&per=25")
	toolbar := tagWith(t, body, `data-toolbar`)
	for _, attr := range []string{`data-current-sort="year"`, `data-current-dir="desc"`, `data-current-filter="unmonitored"`} {
		require.Contains(t, toolbar, attr)
	}
	require.Contains(t, body, `sse-connect="/events/library/movies?dir=desc&amp;filter=unmonitored&amp;page=1&amp;per=25&amp;sort=year"`, "the stream carries the view")
	require.Contains(t, body, `hx-get="/library/movies?dir=desc&amp;filter=unmonitored&amp;page=1&amp;pages=2&amp;per=25&amp;sort=year"`, "the load-more sentinel carries the view")
	require.Contains(t, tagWith(t, body, `data-sort="year"`), `href="/library/movies?filter=unmonitored&amp;per=25&amp;sort=year"`, "the active sort flips back to ascending")
	require.Contains(t, tagWith(t, body, `data-sort="title"`), `href="/library/movies?filter=unmonitored&amp;per=25"`, "the default sort leaves the URL")
	require.Contains(t, tagWith(t, body, `data-filter="all"`), `href="/library/movies?dir=desc&amp;per=25&amp;sort=year"`, "the default filter leaves the URL, the sort stays")
	require.Contains(t, tagWith(t, body, `data-filter="missing"`), `href="/library/movies?dir=desc&amp;filter=missing&amp;per=25&amp;sort=year"`)
	require.NotContains(t, body, `data-jump-bar`, "the A-Z bar means nothing under a year sort")

	body = get("/library/movies?filter=unmonitored&per=25")
	require.Contains(t, body, `data-jump-bar`, "the title sort has the A-Z bar")
	requireTag(t, body, `data-jump="M"`, `href="/library/movies?filter=unmonitored&amp;jump=M&amp;per=25"`)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/library/movies?jump=M&filter=unmonitored&per=25", nil))
	require.Equal(t, http.StatusFound, rec.Code)
	require.Equal(t, "/library/movies?filter=unmonitored&page=3&per=25", rec.Header().Get("Location"), "the jump keeps the filter")

	body = get("/library/movies?sort=bogus&dir=sideways&filter=bogus")
	toolbar = tagWith(t, body, `data-toolbar`)
	for _, attr := range []string{`data-current-sort="title"`, `data-current-dir="asc"`, `data-current-filter="all"`} {
		require.Contains(t, toolbar, attr, "unknown values fall back to the default view")
	}
	require.Contains(t, body, `sse-connect="/events/library/movies?page=1&amp;per=50"`)
}

func TestLibraryPageAndStreamApplyTheView(t *testing.T) {
	items := make(chan []projection.LibraryItem, 1)
	items <- mixedLibrary()
	srv := ui.NewServer(t.Context(), ui.Options{
		Library:          func(context.Context) []projection.LibraryItem { return mixedLibrary() },
		SubscribeLibrary: func() (<-chan []projection.LibraryItem, func()) { return items, func() {} },
	})
	get := func(path string) string {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, http.StatusOK, rec.Code, path)
		return rec.Body.String()
	}

	body := get("/library/movies?filter=missing")
	require.Equal(t, []string{"brazil", "dune"}, refsInOrder(body), "Missing keeps the monitored titles with nothing on disk")

	require.Equal(t, []string{"dune", "arrival", "heat", "brazil", "alien"}, refsInOrder(get("/library/movies?sort=year&dir=desc")))
	require.Equal(t, []string{"brazil", "dune", "heat", "arrival", "alien"}, refsInOrder(get("/library/movies?sort=status")))
	require.Equal(t, []string{"alien", "arrival", "brazil", "dune", "heat"}, refsInOrder(get("/library/movies")))

	httpSrv := httptest.NewServer(srv.Handler())
	defer httpSrv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, httpSrv.URL+"/events/library/movies?filter=missing&sort=year&dir=desc", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	frame := readSSEEvent(t, bufio.NewReader(resp.Body))
	require.Equal(t, []string{"dune", "brazil"}, refsInOrder(frame), "the stream filters and orders every frame as the page does")
}
