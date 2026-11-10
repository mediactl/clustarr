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

package ui

import (
	"context"
	"net/http"
	"strconv"

	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/ui/projection"
	"github.com/mediactl/clustarr/ui/views"
)

// activityPerPage is how many rows an Activity page's list shows a page.
const activityPerPage = 25

// activityTitles are the Activity pages (owner, 2026-11-10), by section.
var activityTitles = map[string]string{
	"downloads":  "Downloads",
	"transcodes": "Transcodes",
	"subtitles":  "Subtitles",
	"markers":    "Markers",
}

// handleActivityIndex sends the Activity entry to its first page.
func (s *Server) handleActivityIndex(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/activity/downloads", http.StatusFound)
}

// redirectTo answers with a redirect to target: the pages the Activity
// group replaced (GET /pipeline, GET /downloads) send old links there.
func redirectTo(target string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target, http.StatusFound)
	}
}

// handleActivity is an Activity page, on the tab and the pages the URL
// names (tab-<section>, page-<list>).
func (s *Server) handleActivity(w http.ResponseWriter, r *http.Request) {
	v, ok := s.activityView(r.Context(), r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if v.Section == "downloads" {
		v.Clients = s.downloadClients(r.Context())
	}
	// The sub-menu's entries preload on hover, as the Library's do.
	preloadable(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.Activity(v).Render(r.Context(), w); err != nil {
		logging.FromContext(r.Context()).Error("render activity page", "section", v.Section, "error", err)
	}
}

// handleActivityList answers an Activity page's poller and pagers: its
// card, out of band, on the tab and pages ui/static/dashboard.js reports.
func (s *Server) handleActivityList(w http.ResponseWriter, r *http.Request) {
	v, ok := s.activityView(r.Context(), r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.ActivityList(v).Render(r.Context(), w); err != nil {
		logging.FromContext(r.Context()).Error("render activity list", "section", v.Section, "error", err)
	}
}

// activityView is the section's lists from the projection, each cut to the
// page r asks for, and the running downloads' page joined with their
// transfer telemetry; false for no such section.
func (s *Server) activityView(ctx context.Context, r *http.Request) (views.ActivityView, bool) {
	section := r.PathValue("section")
	title, ok := activityTitles[section]
	if !ok {
		return views.ActivityView{}, false
	}
	var d projection.Dashboard
	if s.opts.Dashboard != nil {
		d = s.opts.Dashboard(ctx)
	}
	q := r.URL.Query()
	v := views.ActivityView{Section: section, Title: title, Summary: d, Tab: q.Get("tab-" + section), Pages: map[string]views.ListPage{}}
	page := func(list string) int {
		n, _ := strconv.Atoi(q.Get("page-" + list))
		return n
	}
	switch section {
	case "downloads":
		dl := &v.Summary.Downloads
		var active []projection.DownloadRow
		active, v.Pages["downloads-running"] = pageOfSize(d.Downloads.Active.Rows, page("downloads-running"), activityPerPage)
		v.Active = s.downloadViews(ctx, active)
		dl.Pending.Rows, v.Pages["downloads-queued"] = pageOfSize(d.Downloads.Pending.Rows, page("downloads-queued"), activityPerPage)
		dl.Completed.Rows, v.Pages["downloads-completed"] = pageOfSize(d.Downloads.Completed.Rows, page("downloads-completed"), activityPerPage)
		dl.Failed.Rows, v.Pages["downloads-failed"] = pageOfSize(d.Downloads.Failed.Rows, page("downloads-failed"), activityPerPage)
	case "transcodes":
		tc := &v.Summary.Transcodes
		tc.Running.Rows, v.Pages["transcodes-running"] = pageOfSize(d.Transcodes.Running.Rows, page("transcodes-running"), activityPerPage)
		tc.Pending.Rows, v.Pages["transcodes-pending"] = pageOfSize(d.Transcodes.Pending.Rows, page("transcodes-pending"), activityPerPage)
		tc.Failed.Rows, v.Pages["transcodes-failed"] = pageOfSize(d.Transcodes.Failed.Rows, page("transcodes-failed"), activityPerPage)
		tc.Skipped.Rows, v.Pages["transcodes-skipped"] = pageOfSize(d.Transcodes.Skipped.Rows, page("transcodes-skipped"), activityPerPage)
	case "subtitles":
		st := &v.Summary.Subtitles
		st.Searching.Rows, v.Pages["subtitles-searching"] = pageOfSize(d.Subtitles.Searching.Rows, page("subtitles-searching"), activityPerPage)
		st.Wanted.Rows, v.Pages["subtitles-wanted"] = pageOfSize(d.Subtitles.Wanted.Rows, page("subtitles-wanted"), activityPerPage)
		st.Failed.Rows, v.Pages["subtitles-failed"] = pageOfSize(d.Subtitles.Failed.Rows, page("subtitles-failed"), activityPerPage)
		st.Satisfied.Rows, v.Pages["subtitles-done"] = pageOfSize(d.Subtitles.Satisfied.Rows, page("subtitles-done"), activityPerPage)
	case "markers":
		mk := &v.Summary.Markers
		mk.Pending.Rows, v.Pages["markers-pending"] = pageOfSize(d.Markers.Pending.Rows, page("markers-pending"), activityPerPage)
		mk.Found.Rows, v.Pages["markers-found"] = pageOfSize(d.Markers.Found.Rows, page("markers-found"), activityPerPage)
		mk.NotFound.Rows, v.Pages["markers-none"] = pageOfSize(d.Markers.NotFound.Rows, page("markers-none"), activityPerPage)
		mk.Error.Rows, v.Pages["markers-error"] = pageOfSize(d.Markers.Error.Rows, page("markers-error"), activityPerPage)
	}
	return v, true
}
