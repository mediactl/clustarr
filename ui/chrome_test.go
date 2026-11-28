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

// TestLayoutHasASidebarAndTheComponentScripts: the Liquid Glass shell
// (2026-11-28, after the Music and TV apps): a full-height floating glass
// sidebar -- the brand, the search, sections of flat rows, Settings at its
// foot -- a sticky toolbar in each page with no breadcrumb trail, and the
// phone's tab bar; there is no full-width top bar.
func TestLayoutHasASidebarAndTheComponentScripts(t *testing.T) {
	srv := ui.NewServer(t.Context(), ui.Options{})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/unmatched", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	require.Contains(t, body, `data-slot="sidebar"`)
	require.Contains(t, body, `data-slot="sidebar-inset"`, "the page renders inside the sidebar's inset")
	inset := strings.Index(body, `data-slot="sidebar-inset"`)
	sidebarHTML := body[strings.Index(body, `data-slot="sidebar-wrapper"`):inset]
	rows := []string{"/dashboard", "/library/collections", "/library/hubs", "/livetv/channels", "/livetv/guide", "/livetv/sources",
		"/activity/downloads", "/activity/transcodes", "/activity/subtitles", "/activity/markers", "/unmatched", "/import-lists", "/settings"}
	for _, tab := range projection.Tabs() {
		rows = append(rows, "/library/"+string(tab))
	}
	for _, href := range rows {
		requireTag(t, sidebarHTML, `href="`+href+`"`, `data-slot="sidebar-menu-button"`)
	}
	require.Equal(t, len(rows), strings.Count(sidebarHTML, `data-slot="sidebar-menu-button"`), "one row per page, no sub-menus")
	require.NotContains(t, sidebarHTML, `data-slot="sidebar-menu-sub"`, "the rows are flat")
	for _, label := range []string{"Library", "Plex", "Live TV", "Activity", "Library Health"} {
		require.Regexp(t, regexp.MustCompile(`data-slot="sidebar-group-label"[^>]*>\s*`+label+`\s*<`), sidebarHTML, "the %s section's header", label)
	}
	require.Less(t, strings.Index(sidebarHTML, `data-slot="sidebar-content"`), strings.Index(sidebarHTML, `href="/settings"`), "Settings is at the panel's foot")
	require.Greater(t, strings.Index(sidebarHTML, `href="/settings"`), strings.Index(sidebarHTML, `data-slot="sidebar-footer"`))
	// The bare data-active attribute, not the data-active: variants in the
	// class list.
	activeAttr := regexp.MustCompile(`\sdata-active(\s|>)`)
	require.Regexp(t, activeAttr, tagWith(t, sidebarHTML, `href="/unmatched"`), "the current page's row is active")
	require.NotRegexp(t, activeAttr, tagWith(t, sidebarHTML, `href="/activity/downloads"`))
	require.NotContains(t, body, `href="/pipeline"`, "the pipeline page is gone (owner, 2026-11-10)")
	for _, tab := range projection.Tabs() {
		// A section page's row swaps the page body and carries the shell's
		// live parts out of band, so the lit row follows the swap.
		entry := requireTag(t, sidebarHTML, `href="/library/`+string(tab)+`"`, `data-slot="sidebar-menu-button"`,
			`hx-get="/library/`+string(tab)+`"`, `hx-push-url="true"`, `hx-select="#page-body"`, `hx-select-oob="#app-nav,#app-tabbar,#header-find"`,
			`hx-target="#page-body"`, `hx-swap="outerHTML"`)
		require.NotRegexp(t, activeAttr, entry, "no library row is active on the Unmatched page")
	}

	// The search is the sidebar's: shadcn's input group, a search icon and
	// the result count; off a library tab it searches the whole library.
	find := strings.Index(sidebarHTML, `id="header-find"`)
	require.Greater(t, find, 0, "the search is in the sidebar")
	group := sidebarHTML[find:]
	require.Contains(t, group, `data-slot="input-group"`)
	requireTag(t, group, `data-find-input`, `data-slot="input-group-control"`, `hx-get="/library/find"`, `placeholder="Search library…"`)
	require.Contains(t, group, `lucide-search`)
	requireTag(t, group, `id="library-find-count"`)

	// No top bar, no breadcrumbs: the page's toolbar is inside its body.
	require.NotContains(t, body, `data-slot="breadcrumb"`)
	require.NotContains(t, body, `page-crumbs`)
	pageBody := strings.Index(body, `id="page-body"`)
	require.Greater(t, pageBody, inset)
	require.Greater(t, strings.Index(body, `data-topbar`), pageBody, "the toolbar is the page's own")
	require.NotContains(t, body, `data-nav="add-new"`, "Add is offered only where it applies")

	// The phone's tab bar: the four sections and the search.
	tabbar := body[strings.Index(body, `id="app-tabbar"`):]
	for _, href := range []string{"/dashboard", "/library", "/livetv", "/activity"} {
		requireTag(t, tabbar, `href="`+href+`"`)
	}
	requireTag(t, tabbar, `data-find-open-button`, `aria-label="Search"`, `data-slot="button"`)

	headEnd := strings.Index(body, "</head>")
	require.GreaterOrEqual(t, headEnd, 0)
	head := body[:headEnd]
	require.Regexp(t, regexp.MustCompile(`<script[^>]*src="/static/js/shadcn-templ-[0-9a-f]+\.js"`), head, "the component bundle loads from the embedded static files")
	src := regexp.MustCompile(`/static/js/shadcn-templ-[0-9a-f]+\.js`).FindString(head)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, src, nil))
	require.Equal(t, http.StatusOK, rec.Code, "GET %s", src)
	require.Contains(t, rec.Header().Get("Content-Type"), "javascript")
	// The navigation menu, the menubar and the scroll area (shadcn-templ's
	// own since 2026-10-06) ship their scripts in the same bundle:
	// `shadcn-templ bundle` packs every ui/components/*/*.js.
	for _, slot := range []string{`"navigation-menu-link"`, `"menubar"`, `scroll-area-viewport`} {
		require.Contains(t, rec.Body.String(), slot, "the bundle lacks %s; run `shadcn-templ bundle`", slot)
	}

	// The Dashboard offers Add: a menu of every library's Add New page.
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	dash := rec.Body.String()
	requireTag(t, dash, `data-nav="add-new"`, `group/button`)
	for _, tab := range projection.Tabs() {
		requireTag(t, dash, `data-nav-add="`+string(tab)+`"`, `href="/library/`+string(tab)+`/add"`, `data-slot="dropdown-menu-item"`)
	}
}

func TestLibraryPageHasSidebarRowsASectionStripAndAJumpBar(t *testing.T) {
	srv := ui.NewServer(t.Context(), ui.Options{
		Library: func(context.Context) []projection.LibraryItem { return letteredLibrary(120) },
	})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/library/tv", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	require.NotContains(t, body, `data-slot="breadcrumb"`)
	// On a tab the sidebar's search is the tab's.
	requireTag(t, body, `data-find-input`, `hx-get="/library/tv/find"`, `placeholder="Search TV…"`)
	// The tab offers its own Add among its actions, not the every-library menu.
	requireTag(t, body, `data-action="add-new"`, `href="/library/tv/add"`, `group/button`)
	require.NotContains(t, body, `data-nav="add-new"`)

	activeAttr := regexp.MustCompile(`\sdata-active(\s|>)`)
	sidebarHTML := body[strings.Index(body, `data-slot="sidebar-wrapper"`):strings.Index(body, `data-slot="sidebar-inset"`)]
	require.Regexp(t, activeAttr, tagWith(t, sidebarHTML, `href="/library/tv"`), "the TV row is active")
	require.NotRegexp(t, activeAttr, tagWith(t, sidebarHTML, `href="/library/movies"`))
	// On a phone and a tablet the library's pages are a segmented strip
	// under the toolbar; the TV segment is lit.
	strip := body[strings.Index(body, `data-section-strip`):]
	strip = strip[:strings.Index(strip, "</nav>")]
	for _, tab := range projection.Tabs() {
		requireTag(t, strip, `href="/library/`+string(tab)+`"`, `data-slot="tabs-trigger"`, `hx-get="/library/`+string(tab)+`"`,
			`hx-select-oob="#app-nav,#app-tabbar,#header-find"`, `hx-target="#page-body"`)
	}
	require.Regexp(t, activeAttr, tagWith(t, strip, `href="/library/tv"`))
	require.NotRegexp(t, activeAttr, tagWith(t, strip, `href="/library/movies"`))
	// The phone's tab bar lights Library.
	tabbar := body[strings.Index(body, `id="app-tabbar"`):]
	require.Regexp(t, activeAttr, tagWith(t, tabbar, `href="/library"`))

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
	for _, class := range []string{"fixed", "right-0", "top-[7.5rem]", "bottom-0"} {
		require.Contains(t, bar, class, "the bar is fixed to the right of the screen")
	}
	require.NotContains(t, bar, "sticky")
	requireTag(t, body, `id="library-rows"`, `data-total="120"`)
	requireTag(t, body, `id="library-page"`, "lg:pr-12") // the page, its toolbar too, keeps clear of the bar
	// The position thumb (design 2026-09-24, after Radarr's): a thin mark
	// the tracker script places along the strip in proportion to the item
	// range on screen over the whole list, not snapped to a letter.
	require.NotContains(t, bar, "relative", "a second position utility would override fixed and drop the strip to the page's end")
	// The thumb drags like a native scrollbar's (2026-09-24): a grab strip
	// wider than the slim rounded bar it draws (Plex's, 2026-11-04),
	// pointer events on (it is the drag handle), the browser's touch
	// panning off so a touch drag moves the thumb and not the page.
	// ui/static/jump.js does the dragging.
	// The strip is 44px (owner, 2026-11-17: "the current scroll bar is too
	// close to the letters"), Plex Web's spacing: the thumb down its left
	// edge, the letters in its right 24px, air between them.
	require.Contains(t, bar, "w-11")
	requireTag(t, body[barAt:], `data-jump-letters`, `ml-auto`, `w-6`)
	thumb := requireTag(t, body[barAt:], `data-jump-thumb`, `absolute`, `left-0`, `w-4`, `before:w-1.5`, `before:rounded-full`, `cursor-grab`, `touch-none`, `select-none`)
	require.NotContains(t, thumb, "pointer-events-none", "the thumb takes the pointer; it is the drag handle")
	require.NotContains(t, thumb, "w-0.5", "the grab strip is wider than the line it draws")
	require.NotContains(t, body, `data-letter=`, "cards carry no letter; the thumb is positional")
	headEnd := strings.Index(body, "</head>")
	require.GreaterOrEqual(t, headEnd, 0)
	require.Regexp(t, regexp.MustCompile(`<script[^>]*src="/static/jump.js"`), body[:headEnd], "the scroll tracker loads in the head")
	// The letters are plain text in a column, as the Plex apps' A-Z bar
	// is since their redesign (owner, 2026-11-04), not Radarr's button
	// group.
	letters := tagWith(t, body[barAt:], `data-jump-letters`)
	require.Contains(t, letters, "flex-col", "the letters stand in a column")
	require.Contains(t, letters, "h-full")
	require.Contains(t, letters, "ml-auto", "the column stands at the strip's right, clear of the thumb at its left")
	require.NotContains(t, body[barAt:strings.Index(body[barAt:], "</nav>")+barAt], `data-slot="button`, "no buttons")
	// The native scrollbar is hidden on the library (design 2026-09-24): the
	// A-Z strip is the only thing on the right edge, as in Radarr.
	require.Contains(t, tagWith(t, body, `<html`), `data-scrollbar="hidden"`)
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/unmatched", nil))
	require.NotContains(t, tagWith(t, rec.Body.String(), `<html`), `data-scrollbar`, "other pages keep their scrollbar")
	letter := tagWith(t, body, `data-jump="M"`)
	require.Contains(t, letter, "flex-1", "the letters share the height evenly")
	require.Contains(t, letter, "w-full", "and the strip's width")
	require.Equal(t, 27, strings.Count(body, `data-jump="`), "# and A-Z")
	// Each letter links to its first title's card, on the one page
	// (2026-11-10), and carries that card's index, which ui/static/jump.js
	// falls back to once the stream has replaced the card. A-P have five
	// titles: M is the 13th letter, index 60.
	requireTag(t, body, `data-jump="M"`, `href="#card-movie-default-m-012"`, `data-index="60"`)
	requireTag(t, body, `data-jump="A"`, `href="#card-movie-default-m-000"`, `data-index="0"`)
	requireTag(t, body, `data-jump="Z"`, `href="#card-movie-default-m-025"`, `data-index="116"`)
	require.NotContains(t, body, `data-page=`, "nothing pages")
	hash := tagWith(t, body, `data-jump="#"`)
	require.NotContains(t, hash, `href=`, "a letter no title starts with is not a link")
	require.Contains(t, hash, `aria-disabled="true"`)
	require.Contains(t, hash, "pointer-events-none", "the wheel over a letter without titles scrolls the page")
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
	// A child page's toolbar has a back button naming its parent.
	requireTag(t, body, `data-back`, `href="/library/tv"`, `aria-label="Back to TV"`)
	require.NotContains(t, body, `data-section-strip`, "an item's page carries no section strip")
}

// TestTheLibraryIsOnePage: a tab holds every card its view keeps (owner,
// 2026-11-10: "We should probably not paginate the libraries so we can
// mass select and scroll more smoothly"): no pager, no sentinels, and the
// old paging parameters, and ?jump, mean nothing.
func TestTheLibraryIsOnePage(t *testing.T) {
	srv := ui.NewServer(t.Context(), ui.Options{
		Library: func(context.Context) []projection.LibraryItem { return letteredLibrary(120) },
	})
	for _, path := range []string{"/library/movies", "/library/movies?per=25", "/library/movies?page=3&pages=2&per=25", "/library/movies?jump=M"} {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, http.StatusOK, rec.Code, path)
		body := rec.Body.String()
		require.Equal(t, 120, strings.Count(body, `data-ref="`), path)
		for _, gone := range []string{`data-pager`, `data-load-more`, `data-load-prev`, `pages=`} {
			require.NotContains(t, body, gone, path)
		}
		require.Contains(t, body, `sse-connect="/events/library/movies"`, path)
	}
}

// TestLayoutCarriesTheClustarrMark: the mark (ui/static/logo.svg, derived
// from the Servarr family mark in the theme's own palette, 2026-09-29) is
// the page's favicon and touch icon and heads the sidebar beside the
// wordmark (the Liquid Glass shell, 2026-11-28: there is no top bar). The
// toolbar's toggle shows the sidebar again once it is hidden.
func TestLayoutCarriesTheClustarrMark(t *testing.T) {
	srv := ui.NewServer(t.Context(), ui.Options{})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/unmatched", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	headEnd := strings.Index(body, "</head>")
	require.Greater(t, headEnd, 0)
	head := body[:headEnd]
	requireTag(t, head, `href="/static/favicon.svg"`, `rel="icon"`)
	requireTag(t, head, `href="/static/favicon-32.png"`, `rel="icon"`)
	requireTag(t, head, `href="/static/apple-touch-icon.png"`, `rel="apple-touch-icon"`)

	wrapper := strings.Index(body, `data-slot="sidebar-wrapper"`)
	inset := strings.Index(body, `data-slot="sidebar-inset"`)
	require.Greater(t, inset, wrapper)
	require.Less(t, strings.Index(body[headEnd:], `src="/static/logo.svg"`)+headEnd, inset)
	require.Greater(t, strings.Index(body[headEnd:], `src="/static/logo.svg"`)+headEnd, wrapper, "the brand heads the sidebar")
	brand := tagWith(t, body[wrapper:], `src="/static/logo.svg"`)
	require.Contains(t, brand, `alt="Clustarr"`)
	require.Contains(t, body[wrapper:inset], `<span>Clustarr</span>`)
	require.NotContains(t, body[inset:], `src="/static/logo.svg"`, "the page carries no brand of its own")
	requireTag(t, body[inset:], `data-sidebar-show`, `data-templ-sidebar-trigger="app-sidebar"`)
	requireTag(t, body[wrapper:], `data-templ-sidebar-id="app-sidebar"`)
}
