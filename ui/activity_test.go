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

// TestEveryActivityPageIsATabbedCard (owner, 2026-11-10): each Activity
// page is the dashboard's tabbed card for its section, under the Activity
// entry with its own sub-entry active, and a poller that fetches the card
// again.
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
		requireTag(t, body, `data-dashboard-tabs="`+section+`"`)
		requireTag(t, body, `id="activity-poller"`, `data-list-poller`, `hx-get="/activity/`+section+`/list"`)
		sidebar := body[strings.Index(body, `data-slot="sidebar-wrapper"`):strings.Index(body, `data-slot="sidebar-inset"`)]
		require.Regexp(t, activeAttr, requireTag(t, sidebar, `href="/activity"`, `data-slot="sidebar-menu-button"`), "%s: the Activity entry is active", section)
		require.Regexp(t, activeAttr, requireTag(t, sidebar, `href="/activity/`+section+`"`, `data-slot="sidebar-menu-sub-button"`), "%s: its sub-entry is active", section)
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

// TestActivityListIsTheCardOutOfBandOnItsPage: the poller's answer is the
// card alone, swapped out of band, on the page each list's page-<list>
// names, 25 rows a page.
func TestActivityListIsTheCardOutOfBandOnItsPage(t *testing.T) {
	srv := activityServer(t)

	body := getPage(t, srv, "/activity/markers/list?tab-markers=pending&page-markers-pending=2")
	require.NotContains(t, body, "<html", "the card alone")
	requireTag(t, body, `id="activity-card"`, `hx-swap-oob="true"`)
	pending := body[strings.Index(body, `data-list="markers-pending"`):strings.Index(body, `data-list="markers-found"`)]
	require.Equal(t, 5, strings.Count(pending, `data-file="p-`), "page 2 of 30 at 25 a page")
	require.Contains(t, pending, `data-file="p-29"`)
	require.NotContains(t, pending, `data-file="p-00"`)
	requireTag(t, pending, `data-dashboard-page-list="markers-pending"`, `data-dashboard-page="2"`)
	require.Contains(t, pending, `href="/activity/markers?page-markers-pending=1"`, "the pager links its own page")

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
