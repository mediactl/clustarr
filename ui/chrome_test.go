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
			`hx-push-url="true"`, `hx-select="#library-page"`, `hx-target="#library-page"`)
	}
	require.Regexp(t, regexp.MustCompile(`\sdata-active(\s|>)`), tagWith(t, body, `hx-get="/library/tv"`), "the TV trigger is active")
	require.NotRegexp(t, regexp.MustCompile(`\sdata-active(\s|>)`), tagWith(t, body, `hx-get="/library/movies"`))

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/library/movies?per=25", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	body = rec.Body.String()
	barAt := strings.Index(body, `data-jump-bar`)
	require.GreaterOrEqual(t, barAt, 0, "the library page has a jump bar")
	group := tagWith(t, body[barAt:], `data-slot="button-group"`)
	require.Contains(t, group, `data-orientation="vertical"`, "the bar is a vertical button group")
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
