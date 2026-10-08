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
	"slices"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/ui/actions"
	"github.com/mediactl/clustarr/ui/projection"
	"github.com/mediactl/clustarr/ui/views"
)

// An episode's details and its searches, Sonarr's episode details modal
// (2026-10-07): the episode's title opens the modal on its Details tab,
// the row's person icon on its Search tab with an interactive search
// running. An interactive search is a Search without grabBest
// (actions.InteractiveSearch) whose status.results the panel polls; its
// download button adds the release to spec.grab (actions.GrabRelease) and
// the Search controller grabs it. Every route serves htmx its component
// and anyone else a page.

// handleEpisodeDetails serves GET /library/{namespace}/episode/{name}/details,
// on the Details tab, or the Search tab with ?tab=search.
func (s *Server) handleEpisodeDetails(w http.ResponseWriter, r *http.Request) {
	d, series, ok := s.episodeDetail(r.Context(), r.PathValue("namespace"), r.PathValue("name"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	if r.URL.Query().Get("tab") == "search" {
		d.Tab = "search"
	}
	s.renderEpisode(w, r, d, series)
}

// handleInteractiveSearch serves POST
// /library/{namespace}/episode/{name}/search/interactive: it starts an
// interactive search for the episode and answers htmx with the episode's
// modal on its Search tab, the results panel polling; a form post is sent
// to the panel's page. An episode the cache does not hold is not found,
// and nothing is created for it.
func (s *Server) handleInteractiveSearch(w http.ResponseWriter, r *http.Request) {
	ns, name := r.PathValue("namespace"), r.PathValue("name")
	d, series, ok := s.episodeDetail(r.Context(), ns, name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	panel, ok := s.startInteractiveSearch(w, r, ns, commonv1.MediaKindEpisode, name)
	if !ok {
		return
	}
	d.Tab, d.Search = "search", panel
	s.renderEpisode(w, r, d, series)
}

// startInteractiveSearch starts an interactive search for the item
// kind/name, an episode's or a book's. For htmx it returns the results
// panel the item's modal shows on its Search tab, a failure to start
// rendered inside it. A form post it answers itself -- sent to the
// panel's page, or the failure -- and it returns false.
func (s *Server) startInteractiveSearch(
	w http.ResponseWriter, r *http.Request, ns string, kind commonv1.MediaKind, name string,
) (*views.SearchPanel, bool) {
	search, err := s.opts.Actions.InteractiveSearch(r.Context(), ns, kind, name)
	if !isHTMX(r) {
		if err != nil {
			s.finishAction(w, r, err)
			return nil, false
		}
		http.Redirect(w, r, searchPanelURL(search), http.StatusSeeOther)
		return nil, false
	}
	if err != nil {
		return &views.SearchPanel{Phase: string(catalogv1.SearchPhaseFailed), Failed: "the search was not started", Error: failureOf(r, err)}, true
	}
	p := searchPanel(search, time.Now())
	return &p, true
}

// handleSearchPanel serves GET /searches/{namespace}/{name}: an
// interactive search's results panel, which polls this route until it
// is settled. A Search that is gone (its TTL expired) answers a panel
// that says so and stops polling.
func (s *Server) handleSearchPanel(w http.ResponseWriter, r *http.Request) {
	ns, name := r.PathValue("namespace"), r.PathValue("name")
	var p views.SearchPanel
	var search catalogv1.Search
	if s.getObject(r.Context(), types.NamespacedName{Namespace: ns, Name: name}, &search) {
		p = searchPanel(&search, time.Now())
	} else {
		p = views.SearchPanel{Namespace: ns, Name: name, Gone: true}
	}
	s.renderSearchPanel(w, r, p, search.Spec.MediaRef)
}

// handleGrabRelease serves POST /searches/{namespace}/{name}/grab with a
// "guid" field: the interactive search's download button. A release with
// a permanent rejection is grabbed with spec.override -- the button asked
// the person first. htmx gets the panel back, from the Search the patch
// returned, with the grab requested; a failure is rendered in the panel.
func (s *Server) handleGrabRelease(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	ns, name, guid := r.PathValue("namespace"), r.PathValue("name"), r.FormValue("guid")
	var search catalogv1.Search
	if !s.getObject(r.Context(), types.NamespacedName{Namespace: ns, Name: name}, &search) {
		if !isHTMX(r) {
			http.NotFound(w, r)
			return
		}
		s.renderSearchPanel(w, r, views.SearchPanel{Namespace: ns, Name: name, Gone: true}, nil)
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
	if !isHTMX(r) {
		if err != nil {
			s.finishAction(w, r, err)
			return
		}
		http.Redirect(w, r, searchPanelURL(&search), http.StatusSeeOther)
		return
	}
	p := searchPanel(&search, time.Now())
	if err != nil {
		p.Error = failureOf(r, err)
	}
	s.renderSearchPanel(w, r, p, search.Spec.MediaRef)
}

// renderEpisode answers the episode's details: the modal to htmx, a page
// otherwise.
func (s *Server) renderEpisode(w http.ResponseWriter, r *http.Request, d views.EpisodeDetail, series projection.LibraryItem) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	view := views.EpisodePage(d, series)
	if isHTMX(r) {
		view = views.EpisodeModal(d)
	}
	if err := view.Render(r.Context(), w); err != nil {
		logging.FromContext(r.Context()).Error("render episode details", "error", err)
	}
}

// renderSearchPanel answers a results panel: the panel to htmx, a page
// titled by what was searched for otherwise.
func (s *Server) renderSearchPanel(w http.ResponseWriter, r *http.Request, p views.SearchPanel, ref *commonv1.MediaRef) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	view := views.SearchPanelView(p)
	if !isHTMX(r) {
		title := "Interactive search"
		if ref != nil {
			title += ": " + ref.Name
		}
		view = views.SearchPage(p, title)
	}
	if err := view.Render(r.Context(), w); err != nil {
		logging.FromContext(r.Context()).Error("render search panel", "error", err)
	}
}

// episodeDetail reads an episode and its series and builds the modal's
// view on its Details tab, plus the series' library card for a page's
// crumbs. False when the episode, its series' card or the reader is
// missing.
func (s *Server) episodeDetail(ctx context.Context, ns, name string) (views.EpisodeDetail, projection.LibraryItem, bool) {
	var ep catalogv1.Episode
	if !s.getObject(ctx, types.NamespacedName{Namespace: ns, Name: name}, &ep) {
		return views.EpisodeDetail{}, projection.LibraryItem{}, false
	}
	item, ok := findLibraryItem(s.opts.Library(ctx), ns, string(commonv1.MediaKindSeries), ep.Spec.SeriesRef)
	if !ok {
		return views.EpisodeDetail{}, projection.LibraryItem{}, false
	}
	d := views.EpisodeDetail{
		Row:         episodeRow(&ep, time.Now()),
		SeriesTitle: item.Title,
		SeriesURL:   views.DetailPath(item),
		Overview:    ep.Status.Overview,
		Runtime:     runtimeLabel(ep.Status.RuntimeMinutes),
		Tab:         "details",
	}
	if ep.Status.HasFile && ep.Status.FileRef != nil {
		var mf catalogv1.MediaFile
		if s.getObject(ctx, types.NamespacedName{Namespace: ns, Name: *ep.Status.FileRef}, &mf) {
			folder := ""
			if series, ok := s.getSeries(ctx, item.Ref); ok {
				folder = series.Status.Path
			}
			rows, _ := fileRows(folder, &mf)
			d.File = &rows[0]
		}
	}
	return d, item, true
}

// searchPanel is a Search's results panel: its phase, the indexers' tally,
// the results in their ranked order (approved first) and each one's grab.
// It polls while the Search has not answered, or a requested grab has no
// outcome yet.
func searchPanel(search *catalogv1.Search, now time.Time) views.SearchPanel {
	st := search.Status
	p := views.SearchPanel{
		Namespace: search.Namespace, Name: search.Name,
		Phase: string(st.Phase), Indexers: len(st.IndexerOutcomes),
	}
	if st.Phase == catalogv1.SearchPhaseFailed {
		p.Failed = "no reason given"
		for _, t := range []string{catalogv1.SearchConditionFailed, catalogv1.SearchConditionReady} {
			if c := meta.FindStatusCondition(st.Conditions, t); c != nil && c.Message != "" {
				p.Failed = c.Message
				break
			}
		}
	}
	for _, o := range st.IndexerOutcomes {
		if o.State == catalogv1.IndexerOutcomeTimeout || o.State == catalogv1.IndexerOutcomeError {
			p.IndexerErrors++
		}
	}
	results := slices.Clone(st.Results)
	slices.SortStableFunc(results, func(a, b commonv1.ReleaseDecision) int { return int(a.Rank) - int(b.Rank) })
	outstanding := false
	for _, rd := range results {
		row := releaseRow(rd, now)
		row.Grab, row.GrabError = grabState(search, rd.GUID)
		outstanding = outstanding || row.Grab == "requested"
		p.Results = append(p.Results, row)
	}
	p.Poll = p.Pending() || outstanding
	return p
}

// grabState is how a release's grab stands: "" when none was asked for,
// "requested" while the controller has not reported it, then "grabbed" or
// "failed" from status.grabbed.
func grabState(search *catalogv1.Search, guid string) (state, reason string) {
	for _, g := range search.Status.Grabbed {
		if g.GUID != guid {
			continue
		}
		if g.Error != "" {
			return "failed", g.Error
		}
		return "grabbed", ""
	}
	if slices.Contains(search.Spec.Grab, guid) {
		return "requested", ""
	}
	return "", ""
}

// releaseRow is one result in Sonarr's columns.
func releaseRow(rd commonv1.ReleaseDecision, now time.Time) views.ReleaseRow {
	row := views.ReleaseRow{
		GUID: rd.GUID, Protocol: string(rd.Protocol), Title: rd.Title, InfoURL: rd.InfoURL,
		Indexer: rd.IndexerName, Size: rd.SizeBytes, Languages: rd.Languages,
		Quality: qualityLabel(rd.Quality, rd.Revision), Score: rd.FormatScore, Formats: rd.MatchedFormats,
		Approved: rd.Approved, Override: permanentlyRejected(rd),
	}
	if row.Indexer == "" {
		row.Indexer = rd.IndexerRef
	}
	if rd.PublishedAt != nil {
		row.Age = ageLabel(now.Sub(rd.PublishedAt.Time))
	}
	if rd.Protocol == commonv1.ProtocolTorrent && (rd.Seeders != nil || rd.Leechers != nil) {
		row.Peers = fmt.Sprintf("%d/%d", deref(rd.Seeders), deref(rd.Leechers))
	}
	for _, rej := range rd.Rejections {
		reason := rej.Reason
		if rej.Type == commonv1.RejectionTemporary {
			reason += " (temporary)"
		}
		row.Rejections = append(row.Rejections, reason)
	}
	return row
}

// permanentlyRejected is whether grabbing rd needs spec.override: the
// Search controller grabs an approved or temporarily rejected release
// freely and asks for the override on any permanent rejection
// (search.resolveGrab).
func permanentlyRejected(rd commonv1.ReleaseDecision) bool {
	if rd.Approved {
		return false
	}
	return slices.ContainsFunc(rd.Rejections, func(r commonv1.Rejection) bool {
		return r.Type == commonv1.RejectionPermanent
	})
}

// resultByGUID finds a release in a Search's results.
func resultByGUID(results []commonv1.ReleaseDecision, guid string) (commonv1.ReleaseDecision, bool) {
	i := slices.IndexFunc(results, func(r commonv1.ReleaseDecision) bool { return r.GUID == guid })
	if guid == "" || i < 0 {
		return commonv1.ReleaseDecision{}, false
	}
	return results[i], true
}

// qualityLabel is the quality as Sonarr shows it, with a repack or a
// later version named: "WEBDL-1080p Proper", "Bluray-1080p v2".
func qualityLabel(q commonv1.Quality, rev commonv1.Revision) string {
	switch {
	case q.Name == "":
		return ""
	case rev.Version > 1 && rev.Repack:
		return q.Name + " Repack"
	case rev.Version > 1:
		return q.Name + " Proper"
	}
	return q.Name
}

// ageLabel is a release's age as Sonarr gives it: minutes under an hour,
// hours under a day, else days.
func ageLabel(d time.Duration) string {
	switch {
	case d < 0:
		return ""
	case d < time.Hour:
		return plural(int(d.Minutes()), "minute")
	case d < 24*time.Hour:
		return plural(int(d.Hours()), "hour")
	default:
		return plural(int(d.Hours()/24), "day")
	}
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

func deref(n *int32) int32 {
	if n == nil {
		return 0
	}
	return *n
}

// searchPanelURL is the Search's results page.
func searchPanelURL(search *catalogv1.Search) string {
	return views.SearchPanel{Namespace: search.Namespace, Name: search.Name}.URL()
}
