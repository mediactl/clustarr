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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"

	"github.com/mediactl/clustarr/ui"
	"github.com/mediactl/clustarr/ui/projection"
)

// activityDashboard is a projection with rows in every Activity list.
func activityDashboard() projection.Dashboard {
	file := func(name, state, detail, message string) projection.FileRow {
		return projection.FileRow{
			File: types.NamespacedName{Namespace: "media", Name: name}, Name: name + ".mkv",
			ItemURL: "/library/media/episode/" + name, State: state, Detail: detail, Message: message,
			Since: time.Now().Add(-2 * time.Hour),
		}
	}
	var d projection.Dashboard
	d.Subtitles.Searching = projection.FileList{Rows: []projection.FileRow{file("a", "Searching", "english · missing en searching", "")}, Total: 1}
	d.Subtitles.Failed = projection.FileList{Rows: []projection.FileRow{file("b", "Blocked", "", "NoProfile: no SubtitleProfile")}, Total: 1}
	d.Markers.Found = projection.FileList{Rows: []projection.FileRow{file("c", "Found", "intro 0:42-2:12 (theintrodb)", "")}, Total: 1}
	rows := make([]projection.FileRow, 30)
	for i := range rows {
		rows[i] = file(fmt.Sprintf("p-%02d", i), "Pending", "not looked at yet", "")
	}
	d.Markers.Pending = projection.FileList{Rows: rows, Total: 30}
	return d
}

func activityServer(t *testing.T) *ui.Server {
	t.Helper()
	d := activityDashboard()
	return ui.NewServer(t.Context(), ui.Options{Dashboard: func(context.Context) projection.Dashboard { return d }})
}

// TestEveryActivityPageIsATabbedCard (owner, 2026-11-10; 2026-11-27: "the
// tabs should be simplified to use the shadcn-templ tabs component"): each
// Activity page is its section's lists under the tabs component as it
// comes, a tab a state with its count, under the Activity entry with its
// own sub-entry active, and a poller that fetches the lists again.
func TestEveryActivityPageIsATabbedCard(t *testing.T) {
	srv := activityServer(t)
	activeAttr := regexp.MustCompile(`\sdata-active(\s|>)`)
	for section, tabs := range map[string][]string{
		"downloads":  {"Running", "Queued", "Completed", "Failed"},
		"transcodes": {"Running", "Pending", "Failed", "Skipped"},
		"subtitles":  {"Searching", "Wanted", "Failed", "Done"},
		"markers":    {"Pending", "Found", "None found", "Error"},
	} {
		body := getPage(t, srv, "/activity/"+section)
		for _, label := range tabs {
			requireTag(t, body, `data-stat="`+label+`"`, `data-slot="tabs-trigger"`)
		}
		requireTag(t, body, `data-activity-tabs="`+section+`"`, `data-slot="tabs"`)
		require.NotContains(t, body, `data-dashboard-tabs`, "%s: the dashboard's frosted tabs are keyed on that mark", section)
		require.NotContains(t, body, `data-stripe`, "%s: no coloured line under a tab", section)
		require.Regexp(t, `data-stat="[^"]+"[^>]*>[^<]+<span[^>]*data-slot="badge"`, body, "%s: a tab is its name and its count", section)
		requireTag(t, body, `id="activity-poller"`, `data-list-poller`, `hx-get="/activity/`+section+`/list"`)
		sidebar := body[strings.Index(body, `data-slot="sidebar-wrapper"`):strings.Index(body, `data-slot="sidebar-inset"`)]
		require.Regexp(t, activeAttr, requireTag(t, sidebar, `href="/activity/`+section+`"`, `data-slot="sidebar-menu-button"`), "%s: its row is active", section)
		require.Regexp(t, activeAttr, tagWith(t, body[strings.Index(body, `id="app-tabbar"`):], `href="/activity"`), "%s: the tab bar lights Activity", section)
	}
}

// TestActivityFileRowsShowTheirStateDetailAndFailure: a subtitles or
// markers row names its file, links its item, and shows its state, its
// detail and a failure's text.
func TestActivityFileRowsShowTheirStateDetailAndFailure(t *testing.T) {
	srv := activityServer(t)

	body := getPage(t, srv, "/activity/subtitles")
	requireTag(t, body, `data-file="a"`, `data-state="Searching"`)
	require.Contains(t, body, `href="/library/media/episode/a"`)
	require.Contains(t, body, "english · missing en searching · 2h ago")
	require.Contains(t, body, "NoProfile: no SubtitleProfile")
	requireTag(t, body, `data-stat="Searching"`, `data-count="1"`)

	body = getPage(t, srv, "/activity/markers?tab-markers=found")
	require.Contains(t, body, "intro 0:42-2:12 (theintrodb)")
	require.Regexp(t, `data-slot="tabs-trigger"[^>]*data-active[^>]*data-stat="Found"|data-stat="Found"[^>]*data-active`, body, "the asked-for tab is open")
}

// TestActivityTabAndPageAreTheURLs (owner, 2026-11-27: "selected tab should
// be both saved on page refresh and reflected in the url"): ?tab= opens a
// tab and ?page= its page, a pager links both, the script that keeps the
// URL is served, and the poller's own word (tab-<section>) is the newer.
func TestActivityTabAndPageAreTheURLs(t *testing.T) {
	srv := activityServer(t)
	open := func(body, label string) {
		t.Helper()
		require.Regexp(t, `data-slot="tabs-trigger"[^>]*data-active[^>]*data-stat="`+label+`"|data-stat="`+label+`"[^>]*data-active`, body, "%s is open", label)
	}

	open(getPage(t, srv, "/activity/markers?tab=found"), "Found")
	open(getPage(t, srv, "/activity/markers?tab=nonsense"), "Pending")
	open(getPage(t, srv, "/activity/markers/list?tab=found&tab-markers=error"), "Error")

	body := getPage(t, srv, "/activity/markers?tab=pending&page=2")
	open(body, "Pending")
	pending := body[strings.Index(body, `data-list="markers-pending"`):strings.Index(body, `data-list="markers-found"`)]
	requireTag(t, pending, `data-dashboard-page-list="markers-pending"`, `data-dashboard-page="2"`)
	require.Equal(t, 5, strings.Count(pending, `data-file="p-`), "page 2 of 30 at 25 a page")
	require.Contains(t, pending, `href="/activity/markers?tab=pending&amp;page=1"`, "the pager links its tab and its page")

	// A page named for one tab is not another's.
	body = getPage(t, srv, "/activity/markers?tab=found&page=2")
	pending = body[strings.Index(body, `data-list="markers-pending"`):strings.Index(body, `data-list="markers-found"`)]
	requireTag(t, pending, `data-dashboard-page-list="markers-pending"`, `data-dashboard-page="1"`)

	js := get(t, srv, "/static/dashboard.js")
	require.Equal(t, http.StatusOK, js.Code)
	require.Contains(t, js.Body.String(), `url.searchParams.set("tab", value)`)
	require.Contains(t, js.Body.String(), "history.replaceState")
}

// TestActivityRowsAreOneRowWithAnInfoPopover (owner, 2026-11-27: "similar to
// a table where each entry is concise", "moved into an info button that
// presents a shadcn-templ popover"): every list is a grid its rows share
// the columns of, a row its name, its line and a toned state, and what a
// row has more to say is in its popover.
func TestActivityRowsAreOneRowWithAnInfoPopover(t *testing.T) {
	srv := activityServer(t)

	body := getPage(t, srv, "/activity/subtitles")
	_, row, ok := strings.Cut(body, `data-file="a"`)
	require.True(t, ok)
	row, _, ok = strings.Cut(row, `data-file="b"`)
	require.True(t, ok, "the row ends where the next one begins")
	require.Contains(t, tagWith(t, body, `data-file="a"`), "grid-cols-subgrid", "a row takes its list's columns")
	requireTag(t, row, `data-row-info`, `data-slot="popover-trigger"`, `aria-label="Details"`, `data-templ-controls="info-file-a"`)
	requireTag(t, row, `id="info-file-a"`, `data-slot="popover-content"`)
	require.Regexp(t, `data-slot="badge"[^>]*bg-sky-500/20[^>]*>Searching<`, row, "the state badge is toned")
	require.NotContains(t, row, `data-row-actions`, "a file row has no actions")

	// The poll waits while a row's menu or popover is open, and a redraw
	// takes the old rows' away first.
	requireTag(t, body, `id="activity-poller"`, `activityFloatOpen()`)
	js := get(t, srv, "/static/dashboard.js")
	require.Contains(t, js.Body.String(), "window.activityFloatOpen")
	require.Contains(t, js.Body.String(), "htmx:oobBeforeSwap")
}

// TestActivityListIsTheCardOutOfBandOnItsPage: the poller's answer is the
// card alone, swapped out of band, on the page each list's page-<list>
// names, 25 rows a page.
func TestActivityListIsTheCardOutOfBandOnItsPage(t *testing.T) {
	srv := activityServer(t)

	body := getPage(t, srv, "/activity/markers/list?tab-markers=pending&page-markers-pending=2")
	require.NotContains(t, body, "<html", "the card alone")
	requireTag(t, body, `id="activity-card"`, `hx-swap-oob="morph"`)
	pending := body[strings.Index(body, `data-list="markers-pending"`):strings.Index(body, `data-list="markers-found"`)]
	require.Equal(t, 5, strings.Count(pending, `data-file="p-`), "page 2 of 30 at 25 a page")
	require.Contains(t, pending, `data-file="p-29"`)
	require.NotContains(t, pending, `data-file="p-00"`)
	requireTag(t, pending, `data-dashboard-page-list="markers-pending"`, `data-dashboard-page="2"`)
	require.Contains(t, pending, `href="/activity/markers?tab=pending&amp;page=1"`, "the pager links its own page")

	full := getPage(t, srv, "/activity/markers")
	require.Equal(t, 25, strings.Count(full, `data-file="p-`), "the first page")
}

// TestAnUnknownActivitySectionIsNotFound.
func TestAnUnknownActivitySectionIsNotFound(t *testing.T) {
	srv := activityServer(t)
	for _, path := range []string{"/activity/pipeline", "/activity/pipeline/list"} {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, http.StatusNotFound, rec.Code, path)
	}
}
