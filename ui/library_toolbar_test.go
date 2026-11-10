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
	"compress/gzip"
	"context"
	"fmt"
	"io"
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
	"github.com/mediactl/clustarr/ui/views"
)

// The library toolbar (design 2026-09-24, after Radarr's): the rescan
// buttons on the left, and on the right a Sort menu and a Filter menu,
// each a shadcn-templ dropdown menu of links that swap the page through
// htmx the way the tabs do. The view -- ?sort, ?dir, ?filter and, since
// 2026-11-10, the custom filter's repeated ?f -- rides every link on the
// page (stream, the menus, the filter's chips), defaults are left off the
// URL so two links to one view are identical, and the A-Z bar shows only
// under the title sort, A to Z, where it means something. The page holds
// every card the view keeps (2026-11-10, no pages).

var activeAttr = regexp.MustCompile(`\sdata-active(\s|>)`)

// checkedAttr is a checkbox's checked attribute, not aria-checked.
var checkedAttr = regexp.MustCompile(`\schecked(\s|>|=|/)`)

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

// manifestRefs are the movie names a library-manifest event lists, in
// order (views.CardID is card-<kind>-<namespace>-<name>).
func manifestRefs(frame string) []string {
	var out []string
	for _, m := range regexp.MustCompile(`(?m)^data: card-movie-default-([a-z0-9-]+) \S+$`).FindAllStringSubmatch(frame, -1) {
		out = append(out, m[1])
	}
	return out
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
	require.Equal(t, 120, strings.Count(body, `data-ref="default/`), "every card on one page; ?per means nothing any more")

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

	// Add New (2026-09-29): the page's own search-and-add, a link.
	requireTag(t, bar, `data-action="add-new"`, `href="/library/movies/add"`, `data-slot="button"`)
	require.Regexp(t, regexp.MustCompile(`(?s)data-action="add-new"[^>]*>.*?<span>Add New</span>`), bar)

	// The toolbar is shadcn's menubar (2026-10-06), its view menus the
	// menubar's own: Options, Sort and Filter, in Sonarr's order.
	require.Contains(t, toolbar, `data-slot="menubar"`)
	require.Contains(t, toolbar, `role="menubar"`)
	require.Equal(t, 3, strings.Count(bar, `data-slot="menubar-trigger"`), "an Options, a Sort and a Filter trigger")
	options, sortAt, filter := strings.Index(bar, `data-toolbar-menu="Options"`), strings.Index(bar, `data-toolbar-menu="Sort"`), strings.Index(bar, `data-toolbar-menu="Filter"`)
	require.True(t, options >= 0 && options < sortAt && sortAt < filter, "Options, Sort, Filter: %d %d %d", options, sortAt, filter)
	requireTag(t, bar, `data-toolbar-menu="Sort"`, `data-slot="menubar-trigger"`)
	// Options are the reader's own: the poster size, medium by default,
	// and the details under each poster, on.
	for _, sz := range views.PosterSizes {
		requireTag(t, bar, `data-poster-size="`+sz.Key+`"`, `data-slot="menubar-radio-item"`, `data-width="`+sz.Width+`"`)
	}
	requireTag(t, bar, `data-poster-size="medium"`, `aria-checked="true"`)
	requireTag(t, bar, `data-library-option="details"`, `data-slot="menubar-checkbox-item"`, `aria-checked="true"`)

	// The mass editor (2026-10-06, after Sonarr's): Select, and Select All
	// shown only while selecting.
	requireTag(t, bar, `data-action="select-toggle"`, `data-slot="toggle"`, `aria-pressed="false"`)
	require.Regexp(t, regexp.MustCompile(`(?s)data-action="select-toggle".*?>Select</span>.*?>Stop Selecting</span>`), bar)
	requireTag(t, bar, `data-action="select-all"`, `data-slot="button"`)
	require.Regexp(t, regexp.MustCompile(`class="hidden group-data-selecting/library:flex"[^>]*>\s*<button[^>]*data-action="select-all"`), bar, "Select All only while selecting")

	for _, s := range projection.LibrarySorts(projection.TabMovies) {
		requireTag(t, bar, `data-sort="`+string(s)+`"`, `data-slot="menubar-item"`, `hx-get="`,
			`hx-target="#library-page"`, `hx-select="#library-page"`, `hx-swap="outerHTML"`, `hx-push-url="true"`)
		require.Contains(t, bar, ">"+s.Label()+"<", "the menu prints the sort's label")
	}
	title := tagWith(t, bar, `data-sort="title"`)
	require.Regexp(t, activeAttr, title, "the current sort is marked")
	require.Contains(t, title, `href="/library/movies?dir=desc"`, "the current sort's link flips its direction")
	year := tagWith(t, bar, `data-sort="year"`)
	require.NotRegexp(t, activeAttr, year)
	require.Contains(t, year, `href="/library/movies?sort=year"`)
	require.Contains(t, year, `hx-get="/library/movies?sort=year"`)

	for _, f := range projection.LibraryFilters(projection.TabMovies) {
		requireTag(t, bar, `data-filter="`+string(f)+`"`, `data-slot="menubar-item"`, `hx-get="`,
			`hx-target="#library-page"`, `hx-select="#library-page"`, `hx-swap="outerHTML"`, `hx-push-url="true"`)
		require.Contains(t, bar, ">"+f.Label()+"<", "the menu prints the filter's label")
	}
	require.Regexp(t, activeAttr, tagWith(t, bar, `data-filter="all"`))
	missing := tagWith(t, bar, `data-filter="missing"`)
	require.NotRegexp(t, activeAttr, missing)
	require.Contains(t, missing, `href="/library/movies?filter=missing"`)

	// The custom filters (2026-11-10): the builder, saving the view (off
	// while nothing is filtered) and this browser's saved ones, which
	// ui/static/library.js fills from the template.
	requireTag(t, bar, `data-open-dialog="library-filter"`, `data-slot="menubar-item"`)
	requireTag(t, bar, `data-open-dialog="library-filter-save"`, `data-slot="menubar-item"`, `data-disabled`)
	requireTag(t, bar, `data-saved-filters`, `data-tab="movies"`)
	require.Contains(t, bar, `<template data-saved-filter-template>`)
	requireTag(t, body, `id="library-filter"`)
	requireTag(t, body, `data-filter-form`, `method="get"`, `action="/library/movies/filter"`)
	require.NotContains(t, bar, `data-filter-chips`, "no chips while nothing is filtered")
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
	// 120 titles.
	body := get("/library/movies?sort=year&dir=desc&filter=unmonitored")
	toolbar := tagWith(t, body, `data-toolbar`)
	for _, attr := range []string{`data-current-sort="year"`, `data-current-dir="desc"`, `data-current-filter="unmonitored"`} {
		require.Contains(t, toolbar, attr)
	}
	require.Equal(t, 120, strings.Count(body, `data-ref="default/`))
	require.NotContains(t, body, `data-load-more`)
	require.NotContains(t, body, `data-load-prev`)
	require.Contains(t, body, `sse-connect="/events/library/movies?dir=desc&amp;filter=unmonitored&amp;sort=year"`, "the stream carries the view")
	requireTag(t, body, `id="library-page"`, `data-query="dir=desc&amp;filter=unmonitored&amp;sort=year"`)
	require.Contains(t, tagWith(t, body, `data-sort="year"`), `href="/library/movies?filter=unmonitored&amp;sort=year"`, "the active sort flips back to ascending")
	require.Contains(t, tagWith(t, body, `data-sort="title"`), `href="/library/movies?filter=unmonitored"`, "the default sort leaves the URL")
	require.Contains(t, tagWith(t, body, `data-filter="all"`), `href="/library/movies?dir=desc&amp;sort=year"`, "the default filter leaves the URL, the sort stays")
	require.Contains(t, tagWith(t, body, `data-filter="missing"`), `href="/library/movies?dir=desc&amp;filter=missing&amp;sort=year"`)
	require.NotContains(t, body, `data-jump-bar`, "the A-Z bar means nothing under a year sort")
	// The preset is a chip, which removes it.
	requireTag(t, body, `data-filter-chip="Unmonitored Only"`)
	require.Contains(t, body, `aria-label="Remove Unmonitored Only"`)
	requireTag(t, body, `data-filter-clear`, `href="/library/movies?dir=desc&amp;sort=year"`)

	body = get("/library/movies?filter=unmonitored")
	require.Contains(t, body, `data-jump-bar`, "the title sort has the A-Z bar")
	// The letter's first title is M012, after A to L's five each.
	requireTag(t, body, `data-jump="M"`, `href="#card-movie-default-m-012"`, `data-index="60"`)
	requireTag(t, body, `id="card-movie-default-m-012"`)
	require.NotContains(t, get("/library/movies?dir=desc"), `data-jump-bar`, "nor Z to A")

	body = get("/library/movies?sort=bogus&dir=sideways&filter=bogus&f=nope:eq:1&f=year:near:2000")
	toolbar = tagWith(t, body, `data-toolbar`)
	for _, attr := range []string{`data-current-sort="title"`, `data-current-dir="asc"`, `data-current-filter="all"`} {
		require.Contains(t, toolbar, attr, "unknown values fall back to the default view")
	}
	require.Contains(t, body, `sse-connect="/events/library/movies"`, "an unknown field or predicate is dropped")
}

func TestLibraryCustomFilterRidesTheURL(t *testing.T) {
	srv := ui.NewServer(t.Context(), ui.Options{
		Library: func(context.Context) []projection.LibraryItem { return mixedLibrary() },
	})
	get := func(path string) string {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, http.StatusOK, rec.Code, path)
		return rec.Body.String()
	}

	// Every row is kept; a row's values are any of them.
	body := get("/library/movies?f=year:gte:1990&f=profile:in:uhd%7Chd")
	require.Equal(t, []string{"arrival", "dune", "heat"}, refsInOrder(body))
	requireTag(t, body, `data-filter-chips`)
	require.Contains(t, body, `data-filter-count>3 of 5<`)
	requireTag(t, body, `data-filter-chip="Year greater than or equal 1990"`)
	requireTag(t, body, `data-filter-chip="Quality Profile is uhd, hd"`)
	// A chip's x removes that row and keeps the other.
	require.Contains(t, body, `href="/library/movies?f=profile%3Ain%3Auhd%7Chd"`)
	require.Contains(t, body, `href="/library/movies?f=year%3Agte%3A1990"`)
	requireTag(t, body, `data-filter-clear`, `href="/library/movies"`)
	requireTag(t, body, `data-open-dialog="library-filter-save"`, `data-slot="menubar-item"`)
	requireTag(t, body, `data-filter-save-form`, `data-tab="movies"`, `data-query="f=year%3Agte%3A1990&amp;f=profile%3Ain%3Auhd%7Chd"`)

	// The dialog holds the rows, ready to edit.
	requireTag(t, body, `data-filter-row="0"`, `data-field-type="number"`)
	requireTag(t, body, `data-filter-row="1"`, `data-field-type="enum"`)
	requireTag(t, body, `name="value.0"`, `type="number"`, `value="1990"`)
	// The profiles the tab's items carry, sorted: hd, sd, uhd.
	requireTag(t, body, `id="filter-value-1-0"`, `name="value.1"`, `value="hd"`, ` checked`)
	require.NotRegexp(t, checkedAttr, tagWith(t, body, `id="filter-value-1-1"`), "sd is offered, not chosen")
	requireTag(t, body, `id="filter-value-1-2"`, `value="uhd"`, ` checked`)

	// The preset and the conditions together.
	require.Equal(t, []string{"heat"}, refsInOrder(get("/library/movies?filter=cutoff-unmet&f=year:lt:2000")))
	// Nothing left: the filter's own placeholder, shown.
	body = get("/library/movies?f=title:contains:zzz")
	require.Empty(t, refsInOrder(body))
	hiddenAttr := regexp.MustCompile(`\shidden(\s|>|=)`)
	require.NotRegexp(t, hiddenAttr, tagWith(t, body, `data-empty="library"`))
	require.Contains(t, body, "Nothing matches this filter.")
	require.Regexp(t, hiddenAttr, tagWith(t, get("/library/movies"), `data-empty="library"`), "the placeholder waits hidden while there are cards")
}

func TestTheFilterDialogAppliesItsRowsAsTheURL(t *testing.T) {
	srv := ui.NewServer(t.Context(), ui.Options{
		Library: func(context.Context) []projection.LibraryItem { return mixedLibrary() },
	})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/library/movies/filter?sort=year&filter=monitored"+
		"&row=a&field.a=year&op.a=gte&value.a=1990"+
		"&row=b&field.b=profile&op.b=in&value.b=uhd&value.b=hd"+
		"&row=c&field.c=bogus&op.c=eq&value.c=1"+
		"&row=d&field.d=title&op.d=contains&value.d=", nil))
	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.Equal(t, "/library/movies?f=year%3Agte%3A1990&f=profile%3Ain%3Auhd%7Chd&filter=monitored&sort=year", rec.Header().Get("Location"),
		"rows that say something become ?f, in order; an unknown field and an empty value are dropped; the sort and preset stay")

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/library/movies/filter", nil))
	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.Equal(t, "/library/movies", rec.Header().Get("Location"), "no rows clears the filter")
}

func TestAFilterRowRedrawsForItsField(t *testing.T) {
	items := mixedLibrary()
	items[0].Genres = []string{"Horror", "Science Fiction"}
	items[1].Genres = []string{"Drama", "Science Fiction"}
	srv := ui.NewServer(t.Context(), ui.Options{
		Library: func(context.Context) []projection.LibraryItem { return items },
	})
	get := func(path string) string {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, http.StatusOK, rec.Code, path)
		return rec.Body.String()
	}

	// A new field: its own predicates, its values to tick, nothing kept.
	row := get("/library/movies/filter/row?row=a&field.a=genres&prev.a=year&op.a=gte&value.a=1990")
	requireTag(t, row, `data-filter-row="a"`, `data-field-type="list"`, `hx-get="/library/movies/filter/row"`, `hx-include="this"`, `hx-swap="outerHTML"`)
	require.Contains(t, row, `hx-trigger="change[target.name===&#39;field.a&#39;||target.name===&#39;op.a&#39;]"`)
	requireTag(t, row, `name="prev.a"`, `value="genres"`)
	requireTag(t, row, `name="op.a"`, `value="in"`)
	for i, g := range []string{"Drama", "Horror", "Science Fiction"} {
		requireTag(t, row, fmt.Sprintf(`id="filter-value-a-%d"`, i), `name="value.a"`, `value="`+g+`"`)
	}
	require.NotRegexp(t, checkedAttr, row)

	// The same field: the predicate and the values stay.
	row = get("/library/movies/filter/row?row=a&field.a=genres&prev.a=genres&op.a=notIn&value.a=Horror")
	requireTag(t, row, `name="op.a"`, `value="notIn"`)
	requireTag(t, row, `id="filter-value-a-1"`, `value="Horror"`, ` checked`)

	// A date: a count of days, or a date before or after.
	row = get("/library/movies/filter/row?row=b&field.b=added&prev.b=added&op.b=inLast&value.b=7")
	requireTag(t, row, `name="value.b"`, `type="number"`, `value="7"`)
	row = get("/library/movies/filter/row?row=b&field.b=added&prev.b=added&op.b=before&value.b=2026-01-01")
	requireTag(t, row, `name="value.b"`, `type="date"`, `value="2026-01-01"`)

	// Add condition: a fresh index and the tab's first field.
	row = get("/library/movies/filter/row")
	m := regexp.MustCompile(`data-filter-row="([a-z0-9]+)"`).FindStringSubmatch(row)
	require.Len(t, m, 2)
	requireTag(t, row, `name="field.`+m[1]+`"`, `value="monitored"`)
	// An index that is not letters and digits is never echoed.
	require.NotContains(t, get("/library/movies/filter/row?row=a%27%29%3B"), `a');`)
}

func TestLibraryCardsAnswerTheNamedCards(t *testing.T) {
	srv := ui.NewServer(t.Context(), ui.Options{
		Library: func(context.Context) []projection.LibraryItem { return mixedLibrary() },
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/library/movies/cards",
		strings.NewReader("id=card-movie-default-heat&id=card-movie-default-gone&id=card-movie-default-alien"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	srv.Handler().ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, []string{"heat", "alien"}, refsInOrder(rec.Body.String()), "the named cards, in order; one the tab lacks is left out")
	requireTag(t, rec.Body.String(), `id="card-movie-default-heat"`, `data-fp=`)
	require.NotContains(t, rec.Body.String(), `data-library-grid`, "the cards alone")
}

func TestTheLibraryPageIsGzippedForABrowserThatTakesIt(t *testing.T) {
	srv := ui.NewServer(t.Context(), ui.Options{
		Library: func(context.Context) []projection.LibraryItem { return letteredLibrary(120) },
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/library/movies", nil)
	req.Header.Set("Accept-Encoding", "gzip, deflate, br")
	srv.Handler().ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "gzip", rec.Header().Get("Content-Encoding"))
	require.Contains(t, rec.Header().Values("Vary"), "Accept-Encoding")
	zr, err := gzip.NewReader(rec.Body)
	require.NoError(t, err)
	plain, err := io.ReadAll(zr)
	require.NoError(t, err)
	require.Equal(t, 120, strings.Count(string(plain), `data-ref="default/`))

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/library/movies", nil))
	require.Empty(t, rec.Header().Get("Content-Encoding"), "plain for a client that does not ask")
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
	r := bufio.NewReader(resp.Body)
	frame := readSSEEvent(t, r)
	require.True(t, strings.HasPrefix(frame, "event: library-manifest\n"), frame)
	require.Contains(t, frame, "data: total 5\n", "the tab's count, for the filter's \"n of m\"")
	require.Equal(t, []string{"dune", "brazil"}, manifestRefs(frame), "the stream filters and orders every frame as the page does")
	require.NotContains(t, frame, "data-ref", "a manifest carries no markup")

	// The same cards again send nothing; a change sends the manifest anew,
	// the changed card's fingerprint moved.
	before := frame
	items <- mixedLibrary()
	changed := mixedLibrary()
	changed[2].QualityProfileRef = "uhd" // Brazil
	items <- changed
	frame = readSSEEvent(t, r)
	require.Equal(t, []string{"dune", "brazil"}, manifestRefs(frame))
	fp := func(frame, name string) string {
		return regexp.MustCompile(`card-movie-default-` + name + ` (\S+)`).FindStringSubmatch(frame)[1]
	}
	require.Equal(t, fp(before, "dune"), fp(frame, "dune"))
	require.NotEqual(t, fp(before, "brazil"), fp(frame, "brazil"))
}
