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
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/pipeline"
	"github.com/mediactl/clustarr/ui"
	"github.com/mediactl/clustarr/ui/projection"
)

// The page chrome follows Radarr's (design 2026-09-24): a sidebar with
// every page, breadcrumbs above the content, the library's tabs as a tabs
// component, an A-Z jump bar as a vertical button group, and the component
// scripts loaded once from the embedded bundle.

// letteredLibrary is n movies whose titles start with A, B, ... Z in turn,
// sorted by title as the projection sorts its output, so they fall into
// letter groups of four or five.
func letteredLibrary(n int) []projection.LibraryItem {
	out := make([]projection.LibraryItem, n)
	for i := range out {
		out[i] = projection.LibraryItem{
			Ref:   types.NamespacedName{Namespace: "default", Name: fmt.Sprintf("m-%03d", i)},
			Kind:  commonv1.MediaKindMovie,
			Tab:   projection.TabMovies,
			Title: fmt.Sprintf("%c%03d", 'A'+i%26, i),
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Title < out[j].Title })
	return out
}

func TestLayoutHasASidebarAndTheComponentScripts(t *testing.T) {
	srv := ui.NewServer(t.Context(), ui.Options{Entries: func(context.Context) []pipeline.Entry { return nil }})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/pipeline", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	require.Contains(t, body, `data-slot="sidebar"`)
	require.Contains(t, body, `data-slot="sidebar-inset"`, "the page renders inside the sidebar's inset")
	for _, href := range []string{"/pipeline", "/library", "/downloads", "/unmatched", "/import-lists", "/settings"} {
		requireTag(t, body, `href="`+href+`"`, `data-slot="sidebar-menu-button"`)
	}
	// The bare data-active attribute, not the data-active: variants in the
	// class list.
	activeAttr := regexp.MustCompile(`\sdata-active(\s|>)`)
	require.Regexp(t, activeAttr, tagWith(t, body, `href="/pipeline"`), "the current page's entry is active")
	require.NotRegexp(t, activeAttr, tagWith(t, body, `href="/downloads"`))

	// The library's tabs live in the top bar of every page (design
	// 2026-09-24, after Radarr's top nav), the breadcrumbs beneath it inside
	// the swapped page body; on a page that is no library tab none is active.
	headerEnd := strings.Index(body, "</header>")
	require.GreaterOrEqual(t, headerEnd, 0)
	header := body[:headerEnd]
	require.Equal(t, 4, strings.Count(header, `data-tui-tabs-trigger`), "the four library tabs sit in the top bar")
	for _, tab := range projection.Tabs() {
		trigger := requireTag(t, header, `hx-get="/library/`+string(tab)+`"`, `data-tui-tabs-trigger`, `hx-push-url="true"`,
			`hx-select="#page-body"`, `hx-target="#page-body"`, `hx-swap="outerHTML"`)
		require.NotRegexp(t, regexp.MustCompile(`\sdata-active(\s|>)`), trigger, "no tab is active on the pipeline page")
	}
	pageBody := strings.Index(body, `id="page-body"`)
	require.Greater(t, pageBody, headerEnd, "the page body follows the top bar")
	require.Greater(t, strings.Index(body, `data-slot="breadcrumb"`), pageBody, "the breadcrumbs sit beneath the top bar, in the page body")
	require.NotContains(t, header, `data-slot="breadcrumb"`)

	headEnd := strings.Index(body, "</head>")
	require.GreaterOrEqual(t, headEnd, 0)
	head := body[:headEnd]
	require.Regexp(t, regexp.MustCompile(`<script[^>]*src="/static/js/shadcn-templ-[0-9a-f]+\.js"`), head, "the component bundle loads from the embedded static files")
	src := regexp.MustCompile(`/static/js/shadcn-templ-[0-9a-f]+\.js`).FindString(head)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, src, nil))
	require.Equal(t, http.StatusOK, rec.Code, "GET %s", src)
	require.Contains(t, rec.Header().Get("Content-Type"), "javascript")
}

func TestLibraryPageHasBreadcrumbsTabsAndAJumpBar(t *testing.T) {
	srv := ui.NewServer(t.Context(), ui.Options{
		Library: func(context.Context) []projection.LibraryItem { return letteredLibrary(120) },
	})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/library/tv", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	require.Contains(t, body, `data-slot="breadcrumb"`)
	require.Contains(t, tagWith(t, body, `data-slot="breadcrumb-link"`), `href="/library"`)
	require.Regexp(t, regexp.MustCompile(`data-slot="breadcrumb-link"[^>]*>[^<]*Library`), body)
	require.Regexp(t, regexp.MustCompile(`data-slot="breadcrumb-page"[^>]*>[^<]*TV`), body, "the current tab is the breadcrumb's page")

	require.Contains(t, body, `data-tui-tabs-value="tv"`, "the tabs component marks the current tab")
	for _, tab := range projection.Tabs() {
		requireTag(t, body, `hx-get="/library/`+string(tab)+`"`, `data-tui-tabs-trigger`, `data-tui-tabs-value="`+string(tab)+`"`,
			`hx-push-url="true"`, `hx-select="#page-body"`, `hx-target="#page-body"`)
	}
	require.Less(t, strings.Index(body, `data-tui-tabs-trigger`), strings.Index(body, "</header>"), "the tabs are in the top bar")
	require.Regexp(t, regexp.MustCompile(`\sdata-active(\s|>)`), tagWith(t, body, `hx-get="/library/tv"`), "the TV trigger is active")
	require.NotRegexp(t, regexp.MustCompile(`\sdata-active(\s|>)`), tagWith(t, body, `hx-get="/library/movies"`))

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/library/movies?per=25", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	body = rec.Body.String()
	barAt := strings.Index(body, `data-jump-bar`)
	require.GreaterOrEqual(t, barAt, 0, "the library page has a jump bar")
	// The bar is fixed to the screen's right edge beneath the toolbar, not
	// a flex sibling of the grid (design 2026-09-24); the grid keeps clear
	// of it, every card files under its letter for the scroll tracker, and
	// the tracker script loads from the static files.
	bar := tagWith(t, body, `data-jump-bar`)
	for _, class := range []string{"fixed", "right-0", "top-[8.75rem]", "bottom-0"} {
		require.Contains(t, bar, class, "the bar is fixed to the right of the screen")
	}
	require.NotContains(t, bar, "sticky")
	require.Contains(t, tagWith(t, body, `id="library-rows"`), "lg:pr-10", "the grid keeps clear of the bar")
	requireTag(t, body, `data-ref="default/m-000"`, `data-letter="A"`)
	requireTag(t, body, `data-ref="default/m-001"`, `data-letter="B"`)
	headEnd := strings.Index(body, "</head>")
	require.GreaterOrEqual(t, headEnd, 0)
	require.Regexp(t, regexp.MustCompile(`<script[^>]*src="/static/jump.js"`), body[:headEnd], "the scroll tracker loads in the head")
	group := tagWith(t, body[barAt:], `data-slot="button-group"`)
	require.Contains(t, group, `data-orientation="vertical"`, "the bar is a vertical button group")
	require.Contains(t, group, "h-full")
	require.Contains(t, tagWith(t, body, `data-jump="M"`), "flex-1", "the letters share the height evenly")
	require.Equal(t, 27, strings.Count(body, `data-jump="`), "# and A-Z")
	requireTag(t, body, `data-jump="M"`, `href="/library/movies?jump=M&amp;per=25"`)
	hash := tagWith(t, body, `data-jump="#"`)
	require.NotContains(t, hash, `href=`, "a letter no title starts with is not a link")
	require.Contains(t, hash, `disabled`)
}

func TestLibraryJumpRedirectsToTheLetterPage(t *testing.T) {
	srv := ui.NewServer(t.Context(), ui.Options{
		Library: func(context.Context) []projection.LibraryItem { return letteredLibrary(120) },
	})
	for name, tc := range map[string]struct {
		path string
		want string
	}{
		// A-P have five titles each, so M begins at index 60: page 3 of 25.
		"middle letter": {"/library/movies?jump=M&per=25", "/library/movies?page=3&per=25"},
		// Q-Z have four each: R begins at 80 + 4 = 84, page 4.
		"late letter":  {"/library/movies?jump=R&per=25", "/library/movies?page=4&per=25"},
		"first letter": {"/library/movies?jump=A&per=25", "/library/movies?page=1&per=25"},
		// No title starts with a digit, so # lands on the first page.
		"absent letter":     {"/library/movies?jump=%23&per=25", "/library/movies?page=1&per=25"},
		"default page size": {"/library/movies?jump=M", "/library/movies?page=2&per=50"},
	} {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
			require.Equal(t, http.StatusFound, rec.Code)
			require.Equal(t, tc.want, rec.Header().Get("Location"))
		})
	}
}

// TestTheItemsTabIsActiveOnItsPage: the top bar marks the tab an item's
// page belongs to.
func TestTheItemsTabIsActiveOnItsPage(t *testing.T) {
	items := letteredLibrary(3)
	items[1].Kind, items[1].Tab = commonv1.MediaKindSeries, projection.TabTV
	srv := ui.NewServer(t.Context(), ui.Options{
		Library: func(context.Context) []projection.LibraryItem { return items },
	})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/library/default/series/"+items[1].Ref.Name, nil))
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.Regexp(t, regexp.MustCompile(`\sdata-active(\s|>)`), tagWith(t, body, `hx-get="/library/tv"`))
	require.NotRegexp(t, regexp.MustCompile(`\sdata-active(\s|>)`), tagWith(t, body, `hx-get="/library/movies"`))
}

// TestLibraryScrollsInsteadOfPaging: the library tabs load more as the
// reader scrolls (design 2026-09-24): no pager, a sentinel after the grid
// that fetches the window one page wider through htmx and swaps the rows
// (stream included, so it reconnects for the wider window), none once
// everything is on screen; the view rides the sentinel too.
func TestLibraryScrollsInsteadOfPaging(t *testing.T) {
	srv := ui.NewServer(t.Context(), ui.Options{
		Library: func(context.Context) []projection.LibraryItem { return letteredLibrary(120) },
	})
	get := func(path string) string {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, http.StatusOK, rec.Code, path)
		return rec.Body.String()
	}

	body := get("/library/movies?per=25")
	require.NotContains(t, body, `data-pager`)
	require.Equal(t, 25, strings.Count(body, `data-ref="`))
	requireTag(t, body, `data-load-more`, `hx-get="/library/movies?page=1&amp;pages=2&amp;per=25"`, `hx-trigger="revealed"`,
		`hx-target="#library-rows"`, `hx-select="#library-rows"`, `hx-swap="outerHTML"`)
	require.Greater(t, strings.Index(body, `data-load-more`), strings.LastIndex(body, `data-ref="`), "the sentinel follows the grid")
	require.Contains(t, body, `sse-connect="/events/library/movies?page=1&amp;per=25"`)

	body = get("/library/movies?per=25&pages=2")
	require.Equal(t, 50, strings.Count(body, `data-ref="`))
	requireTag(t, body, `data-load-more`, `hx-get="/library/movies?page=1&amp;pages=3&amp;per=25"`)
	require.Contains(t, body, `sse-connect="/events/library/movies?page=1&amp;pages=2&amp;per=25"`, "the stream carries the whole window")

	body = get("/library/movies?per=25&pages=5")
	require.Equal(t, 120, strings.Count(body, `data-ref="`))
	require.NotContains(t, body, `data-load-more`, "everything is on screen")

	body = get("/library/movies?per=25&filter=unmonitored&sort=year")
	requireTag(t, body, `data-load-more`, `hx-get="/library/movies?filter=unmonitored&amp;page=1&amp;pages=2&amp;per=25&amp;sort=year"`)

	// A jump lands on the letter's page and scrolls on from there.
	body = get("/library/movies?page=3&per=25")
	require.Equal(t, 25, strings.Count(body, `data-ref="`))
	require.Contains(t, body, `data-ref="default/m-010"`, "the third page of 25 opens on K, the eleventh letter of five titles each")
	requireTag(t, body, `data-load-more`, `hx-get="/library/movies?page=3&amp;pages=2&amp;per=25"`)
}
