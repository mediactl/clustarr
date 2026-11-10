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
	"fmt"
	"net/http"
	"sort"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/ui/actions"
	"github.com/mediactl/clustarr/ui/views"
)

// A comic's issues are Mylar's issues table (2026-11-10 design §9.4; plan
// W4b.3): each row has the monitor bookmark, the number, the title opening
// the issue's page, the cover and store dates, the status cell and the
// automatic and interactive searches, the episode's own routes and actions
// for kind issue. The interactive search is the episode's
// (startInteractiveSearch), its results drawn in comic columns by the
// issue's own panel routes (handleIssueSearchPanel, handleIssueGrabRelease)
// rather than the shared /searches ones, so a poll keeps the columns.

// handleIssueDetails serves GET /library/{namespace}/issue/{name}/details:
// the modal on its Details tab, or the Search tab with ?tab=search, to
// htmx, and the issue's page to anyone else.
func (s *Server) handleIssueDetails(w http.ResponseWriter, r *http.Request) {
	if !isHTMX(r) {
		s.renderIssuePage(w, r, false)
		return
	}
	d, ok := s.issueDetail(r.Context(), r.PathValue("namespace"), r.PathValue("name"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	if r.URL.Query().Get("tab") == "search" {
		d.Tab = "search"
	}
	s.renderIssueModal(w, r, d)
}

// handleIssueInteractiveSearch serves POST
// /library/{namespace}/issue/{name}/search/interactive: it starts an
// interactive search for the issue (startInteractiveSearch) and answers
// htmx with the issue's modal on its Search tab, the results panel polling
// the issue's own panel route; a form post is sent to the shared panel's
// page. An issue the cache does not hold is not found, and nothing is
// created for it.
func (s *Server) handleIssueInteractiveSearch(w http.ResponseWriter, r *http.Request) {
	ns, name := r.PathValue("namespace"), r.PathValue("name")
	d, ok := s.issueDetail(r.Context(), ns, name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	panel, ok := s.startInteractiveSearch(w, r, ns, commonv1.MediaKindIssue, name)
	if !ok {
		return
	}
	d.Tab, d.Search = "search", &views.IssueSearchPanel{SearchPanel: *panel, Issue: name}
	s.renderIssueModal(w, r, d)
}

// handleIssueSearchPanel serves GET
// /library/{namespace}/issue/{name}/search/{search}: an issue's
// interactive search results in comic columns, which the panel polls until
// it is settled -- the shared /searches route draws Sonarr's columns. A
// Search that is gone answers a panel that says so and stops polling.
func (s *Server) handleIssueSearchPanel(w http.ResponseWriter, r *http.Request) {
	ns, issue, name := r.PathValue("namespace"), r.PathValue("name"), r.PathValue("search")
	var search catalogv1.Search
	p := views.IssueSearchPanel{SearchPanel: views.SearchPanel{Namespace: ns, Name: name, Gone: true}, Issue: issue}
	if s.getObject(r.Context(), types.NamespacedName{Namespace: ns, Name: name}, &search) {
		p.SearchPanel = searchPanel(&search, time.Now())
	}
	s.renderIssueSearchPanel(w, r, p, search.Spec.MediaRef)
}

// handleIssueGrabRelease serves POST
// /library/{namespace}/issue/{name}/search/{search}/grab with a "guid"
// field, handleGrabRelease over the issue's panel: the download button in
// comic columns. htmx gets the panel back with the grab requested; a
// failure is rendered in the panel.
func (s *Server) handleIssueGrabRelease(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	ns, issue, name, guid := r.PathValue("namespace"), r.PathValue("name"), r.PathValue("search"), r.FormValue("guid")
	var search catalogv1.Search
	if !s.getObject(r.Context(), types.NamespacedName{Namespace: ns, Name: name}, &search) {
		if !isHTMX(r) {
			http.NotFound(w, r)
			return
		}
		s.renderIssueSearchPanel(w, r, views.IssueSearchPanel{SearchPanel: views.SearchPanel{Namespace: ns, Name: name, Gone: true}, Issue: issue}, nil)
		return
	}

	var err error
	release, found := resultByGUID(search.Status.Results, guid)
	if !found {
		err = fmt.Errorf("%w: release %q is not in the search's results", actions.ErrInvalid, guid)
	} else {
		var patched *catalogv1.Search
		if patched, err = s.opts.Actions.GrabRelease(r.Context(), &search, guid, permanentlyRejected(release)); err == nil {
			search = *patched
		}
	}
	p := views.IssueSearchPanel{SearchPanel: searchPanel(&search, time.Now()), Issue: issue}
	if !isHTMX(r) {
		if err != nil {
			s.finishAction(w, r, err)
			return
		}
		http.Redirect(w, r, p.PanelURL(), http.StatusSeeOther)
		return
	}
	if err != nil {
		p.Error = failureOf(r, err)
	}
	s.renderIssueSearchPanel(w, r, p, search.Spec.MediaRef)
}

// renderIssueSearchPanel answers an issue's results panel: the panel to
// htmx, a page titled by what was searched for otherwise.
func (s *Server) renderIssueSearchPanel(w http.ResponseWriter, r *http.Request, p views.IssueSearchPanel, ref *commonv1.MediaRef) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	view := views.IssueSearchPanelView(p)
	if !isHTMX(r) {
		title := "Interactive search"
		if ref != nil {
			title += ": " + ref.Name
		}
		view = views.IssueSearchPage(p, title)
	}
	if err := view.Render(r.Context(), w); err != nil {
		logging.FromContext(r.Context()).Error("render issue search panel", "error", err)
	}
}

// renderIssueModal answers htmx with the issue's details modal.
func (s *Server) renderIssueModal(w http.ResponseWriter, r *http.Request, d views.IssueDetail) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.IssueModal(d).Render(r.Context(), w); err != nil {
		logging.FromContext(r.Context()).Error("render issue details", "error", err)
	}
}

// issueDetail reads an issue, its comic and its file and builds the
// modal's view on its Details tab. False when the issue, its comic's card
// or the reader is missing.
func (s *Server) issueDetail(ctx context.Context, ns, name string) (views.IssueDetail, bool) {
	var iss catalogv1.Issue
	if !s.getObject(ctx, types.NamespacedName{Namespace: ns, Name: name}, &iss) {
		return views.IssueDetail{}, false
	}
	item, ok := findLibraryItem(s.opts.Library(ctx), ns, string(commonv1.MediaKindComic), iss.Spec.ComicRef)
	if !ok {
		return views.IssueDetail{}, false
	}
	d := views.IssueDetail{
		Row:        issueRow(&iss, time.Now()),
		ComicTitle: item.Title,
		ComicURL:   views.DetailPath(item),
		Overview:   iss.Status.Overview,
		Credits:    issueCredits(iss.Status.Credits),
		Tab:        "details",
	}
	if f := s.issueFile(ctx, &iss); f != nil {
		folder := ""
		var co catalogv1.Comic
		if s.getObject(ctx, item.Ref, &co) {
			folder = co.Status.Path
		}
		rows, _ := fileRows(folder, f)
		d.File = &rows[0]
	}
	return d, true
}

// issueFile reads the MediaFile behind iss by name (status.fileRef), one
// cached Get, never a List: nil when the issue has no file, names none, or
// the reader cannot find it.
func (s *Server) issueFile(ctx context.Context, iss *catalogv1.Issue) *catalogv1.MediaFile {
	ref := iss.Status.FileRef
	if !iss.Status.HasFile || ref == nil || *ref == "" {
		return nil
	}
	var f catalogv1.MediaFile
	if !s.getObject(ctx, types.NamespacedName{Namespace: iss.Namespace, Name: *ref}, &f) {
		return nil
	}
	return &f
}

// listIssues lists one comic's Issues through the reader, selected by
// spec.comicRef: the ui cache's index (IssueComicRefField), or Issue's
// selectable field on a raw client; never every Issue in the namespace.
func (s *Server) listIssues(ctx context.Context, namespace, comic string) ([]catalogv1.Issue, error) {
	if s.opts.Reader == nil {
		return nil, nil
	}
	var list catalogv1.IssueList
	if err := s.opts.Reader.List(ctx, &list, client.InNamespace(namespace),
		client.MatchingFields{IssueComicRefField: comic}); err != nil {
		return nil, err
	}
	return list.Items, nil
}

// sortIssues orders issues as Mylar's table does: by their calculated
// number, then by the number as printed, so "12" comes before "12.HU" and
// annuals sort where the provider numbers them.
func sortIssues(issues []catalogv1.Issue) {
	sort.SliceStable(issues, func(i, j int) bool {
		a, b := issues[i].Spec, issues[j].Spec
		if a.CalculatedNumberCentis != b.CalculatedNumberCentis {
			return a.CalculatedNumberCentis < b.CalculatedNumberCentis
		}
		return a.Number < b.Number
	})
}

// issueRows is a comic's issues table, in number order.
func (s *Server) issueRows(ctx context.Context, namespace, comic string) ([]views.IssueRow, error) {
	issues, err := s.listIssues(ctx, namespace, comic)
	if err != nil {
		return nil, err
	}
	sortIssues(issues)
	now := time.Now()
	rows := make([]views.IssueRow, 0, len(issues))
	for i := range issues {
		rows = append(rows, issueRow(&issues[i], now))
	}
	return rows, nil
}

// issueRow is one issue's row in Mylar's issues table.
func issueRow(iss *catalogv1.Issue, now time.Time) views.IssueRow {
	row := views.IssueRow{
		Namespace: iss.Namespace, Name: iss.Name, Comic: iss.Spec.ComicRef,
		Number: iss.Spec.Number, NumberCentis: iss.Spec.CalculatedNumberCentis,
		Title: iss.Status.Title, Monitored: monitoredOrDefault(iss.Spec.Monitored),
		HasFile: iss.Status.HasFile, Format: issueFormat(iss), Phase: string(iss.Status.Phase),
	}
	if iss.Status.Date != nil {
		row.CoverDate = iss.Status.Date.UTC().Format(time.DateOnly)
		row.CoverDateLabel = relativeDate(iss.Status.Date.UTC(), now)
	}
	if iss.Status.StoreDate != nil {
		row.StoreDate = iss.Status.StoreDate.UTC().Format(time.DateOnly)
		row.StoreDateLabel = relativeDate(iss.Status.StoreDate.UTC(), now)
	}
	row.Status = issueStatus(iss, row.Monitored, now)
	return row
}

// issueFormat is the file's format as the status cell shows it: the comic
// ladder's name (status.fileFormat, CBZ), else the frozen quality's name,
// else "" without a file.
func issueFormat(iss *catalogv1.Issue) string {
	switch {
	case !iss.Status.HasFile:
		return ""
	case iss.Status.FileFormat != "":
		return iss.Status.FileFormat
	case iss.Status.FileQuality != nil:
		return iss.Status.FileQuality.Name
	}
	return ""
}

// issueReleaseDate is the day an issue is out: its store date when the
// provider has one, else its cover date; nil for an undated issue.
func issueReleaseDate(iss *catalogv1.Issue) *time.Time {
	switch {
	case iss.Status.StoreDate != nil:
		t := iss.Status.StoreDate.UTC()
		return &t
	case iss.Status.Date != nil:
		t := iss.Status.Date.UTC()
		return &t
	}
	return nil
}

// issueStatus is the episode's status cell for an issue (design §9.4,
// "the episode's cases"), the first that applies: a grab in flight, a grab
// waiting out its delay, the file's format (warned below the cutoff or
// with no profile to judge it), no date yet (TBA), not out yet (the phase
// Unavailable, or a release date still to come), unmonitored, else
// missing. Every input is the Issue's own status (2026-11-10 design D4),
// which the catalog derives from its Comic's grab entries and its
// MediaFile.
func issueStatus(iss *catalogv1.Issue, monitored bool, now time.Time) views.RowStatus {
	st := iss.Status
	released := issueReleaseDate(iss)
	switch {
	case st.Phase == catalogv1.IssuePhaseDownloading || st.ActiveDownloadRef != nil:
		return views.RowStatus{Kind: "downloading", Label: "Downloading", Title: "Issue is downloading"}
	case st.Phase == catalogv1.IssuePhaseDelayed || st.PendingGrab != nil:
		return views.RowStatus{Kind: "pending", Label: "Pending", Title: "A release waits out its delay profile"}
	case st.HasFile:
		s := views.RowStatus{Kind: "file", Label: "Unknown", Title: "Issue on disk"}
		if f := issueFormat(iss); f != "" {
			s.Label = f
		}
		switch st.Phase {
		case catalogv1.IssuePhaseCutoffUnmet:
			s.Warn, s.Title = true, "Quality cutoff has not been met"
		case catalogv1.IssuePhaseCutoffUnevaluated:
			s.Warn, s.Title = true, "Quality profile could not be resolved"
		}
		return s
	case released == nil:
		return views.RowStatus{Kind: "tba", Label: "TBA", Title: "Release date to be announced"}
	case st.Phase == catalogv1.IssuePhaseUnavailable || released.After(now):
		return views.RowStatus{Kind: "unreleased", Label: "Unreleased", Title: "Issue is not out yet"}
	case !monitored:
		return views.RowStatus{Kind: "unmonitored", Label: "Unmonitored", Title: "Issue is not monitored"}
	default:
		return views.RowStatus{Kind: "missing", Label: "Missing", Title: "Issue missing from disk"}
	}
}

// issueCredits are the issue's status.credits as the page prints them.
func issueCredits(credits []catalogv1.Credit) []views.IssueCredit {
	if len(credits) == 0 {
		return nil
	}
	out := make([]views.IssueCredit, 0, len(credits))
	for _, c := range credits {
		out = append(out, views.IssueCredit{Name: c.Name, Role: c.Role})
	}
	return out
}

// replyIssueRow answers an htmx issue toggle with the row re-rendered,
// replyEpisodeRow's counterpart (design §9.6: handleSetMonitored answers
// row replies for Issue as for Episode and Book; the dispatch is
// routes.go's): from the Issue the patch returned on success, or from the
// Issue as it still stands with the failure inside it.
func (s *Server) replyIssueRow(w http.ResponseWriter, r *http.Request, ns, name string, patched client.Object, err error) {
	var iss *catalogv1.Issue
	if got, ok := patched.(*catalogv1.Issue); ok && err == nil {
		iss = got
	} else {
		var current catalogv1.Issue
		if s.getObject(r.Context(), types.NamespacedName{Namespace: ns, Name: name}, &current) {
			iss = &current
		}
	}
	if iss == nil {
		iss = &catalogv1.Issue{}
		iss.Namespace, iss.Name = ns, name
	}
	row := issueRow(iss, time.Now())
	if err != nil {
		row.Error = failureOf(r, err)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if rerr := views.IssueRowView(row).Render(r.Context(), w); rerr != nil {
		logging.FromContext(r.Context()).Error("render issue row", "error", rerr)
	}
}
