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
	"fmt"
	"net/http"
	"strconv"
	"time"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/ui/views"
)

// A season's two searches (Wave S, Sonarr's season searches): the
// magnifier creates a Search with grabBest and spec.season, which the
// Series plans -- one paced query for the season, the best release for
// each wanted episode grabbed, packs preferred on a tie -- and the person
// an interactive season search, whose results open in the season's modal
// with season packs listed first. Each season header shows the Series'
// season search state as a badge.

// handleSeasonSearch serves POST
// /library/{namespace}/series/{name}/seasons/{n}/search: the season's
// magnifier, answered into #action-status as an episode's search is.
func (s *Server) handleSeasonSearch(w http.ResponseWriter, r *http.Request) {
	ns, name := r.PathValue("namespace"), r.PathValue("name")
	n, err := strconv.ParseInt(r.PathValue("n"), 10, 32)
	if err != nil || n < 0 {
		http.NotFound(w, r)
		return
	}
	_, aerr := s.opts.Actions.SearchSeason(r.Context(), ns, name, int32(n))
	s.finishActionWith(w, r, aerr, seasonSearchDone(int32(n)))
}

// seasonSearchDone is the magnifier's status line.
func seasonSearchDone(season int32) string {
	if season == 0 {
		return "Searching the specials"
	}
	return fmt.Sprintf("Searching season %d", season)
}

// handleSeasonInteractiveSearch serves POST
// /library/{namespace}/series/{name}/seasons/{n}/search/interactive: it
// starts an interactive season search and answers htmx with the season's
// modal, its results panel polling, season packs listed by default; a form
// post is sent to the panel's page. A series the projection or the cache
// does not hold is not found, and nothing is created for it.
func (s *Server) handleSeasonInteractiveSearch(w http.ResponseWriter, r *http.Request) {
	ns, name := r.PathValue("namespace"), r.PathValue("name")
	n, err := strconv.ParseInt(r.PathValue("n"), 10, 32)
	if err != nil || n < 0 {
		http.NotFound(w, r)
		return
	}
	item, ok := findLibraryItem(s.opts.Library(r.Context()), ns, string(commonv1.MediaKindSeries), name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	series, ok := s.getSeries(r.Context(), item.Ref)
	if !ok {
		http.NotFound(w, r)
		return
	}
	search, aerr := s.opts.Actions.InteractiveSeasonSearch(r.Context(), ns, name, int32(n))
	if !isHTMX(r) {
		if aerr != nil {
			s.finishAction(w, r, aerr)
			return
		}
		http.Redirect(w, r, searchPanelURL(search)+"?filter=packs", http.StatusSeeOther)
		return
	}
	var p views.SearchPanel
	if aerr != nil {
		p = views.SearchPanel{Phase: string(catalogv1.SearchPhaseFailed), Failed: "the search was not started", Error: failureOf(r, aerr), Season: true}
	} else {
		p = searchPanel(search, time.Now())
		p = withFilter(p, "")
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if rerr := views.SeasonSearchModal(s.itemDetail(r.Context(), item), seasonRow(series, int32(n)), p).Render(r.Context(), w); rerr != nil {
		logging.FromContext(r.Context()).Error("render season search modal", "error", rerr)
	}
}

// withFilter is p under a season search's filter: "packs" or "all", the
// default packs. Any other Search lists all.
func withFilter(p views.SearchPanel, filter string) views.SearchPanel {
	if !p.Season {
		p.Filter = ""
		return p
	}
	switch filter {
	case "all", "packs":
		p.Filter = filter
	default:
		p.Filter = "packs"
	}
	return p
}

// seasonSearchBadge is a season's search badge from the Series' season
// search: Queued, Searching, or "Searched: N approved" with its age; zero
// for a season the state does not name, or one skipped, failed or
// canceled.
func seasonSearchBadge(series *catalogv1.Series, number int32, now time.Time) views.SeasonSearchBadge {
	st := series.Status.SeasonSearch
	if st == nil {
		return views.SeasonSearchBadge{}
	}
	for _, se := range st.Seasons {
		if se.Number != number {
			continue
		}
		switch se.Phase {
		case catalogv1.SeasonSearchQueued:
			return views.SeasonSearchBadge{Phase: string(se.Phase), Label: "Queued", Title: "The season waits its turn in the series' search"}
		case catalogv1.SeasonSearchSearching:
			return views.SeasonSearchBadge{Phase: string(se.Phase), Label: "Searching", Title: "The season is being searched"}
		case catalogv1.SeasonSearchAnswered:
			b := views.SeasonSearchBadge{Phase: string(se.Phase), Label: fmt.Sprintf("Searched: %d approved", se.Approved)}
			if se.AnsweredAt != nil {
				b.Title = fmt.Sprintf("%d releases, %d approved, %s ago", se.Candidates, se.Approved, ageLabel(now.Sub(se.AnsweredAt.Time)))
			}
			return b
		}
	}
	return views.SeasonSearchBadge{}
}
