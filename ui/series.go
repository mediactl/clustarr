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
	"sort"
	"strconv"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/ui/projection"
	"github.com/mediactl/clustarr/ui/views"
)

// The series page and the season route (spec 2026-09-23-library-page-design,
// decision 3) read at request time through Options.Reader -- the same cache
// the projection lists from -- rather than from the projection's snapshot:
// a season's episodes are fetched when it is first expanded, and after a
// toggle the component must show the value just written, not the one the
// last tick saw. Nothing here writes.

// isHTMX reports whether htmx made the request, in which case a route that
// also serves a full page serves the component alone.
func isHTMX(r *http.Request) bool { return r.Header.Get("HX-Request") == "true" }

// renderSeriesPage renders a Series' page for handleLibraryItem: the
// library card's header and one season component per season. With no
// reader (no cluster) it renders the header alone.
func (s *Server) renderSeriesPage(w http.ResponseWriter, r *http.Request, item projection.LibraryItem) {
	var seasons []views.SeasonRow
	if series, ok := s.getSeries(r.Context(), item.Ref); ok {
		seasons = seasonRows(series)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.SeriesDetail(item, seasons).Render(r.Context(), w); err != nil {
		logging.FromContext(r.Context()).Error("render series page", "error", err)
	}
}

// handleSeason serves GET /library/{namespace}/series/{name}/seasons/{n}:
// the episodes component to htmx, a full page to anyone else. A series the
// projection does not list, a series the cache does not hold or a season
// that is not a number is not found.
func (s *Server) handleSeason(w http.ResponseWriter, r *http.Request) {
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
	episodes, err := s.listEpisodes(r.Context(), ns)
	if err != nil {
		logging.FromContext(r.Context()).Error("list episodes for season page", "error", err)
		http.Error(w, "could not list episodes", http.StatusInternalServerError)
		return
	}
	rows := episodeRows(episodes, name, int32(n))

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if isHTMX(r) {
		if err := views.EpisodeRows(rows).Render(r.Context(), w); err != nil {
			logging.FromContext(r.Context()).Error("render episodes component", "error", err)
		}
		return
	}
	if err := views.SeasonPage(item, seasonRow(series, int32(n)), rows).Render(r.Context(), w); err != nil {
		logging.FromContext(r.Context()).Error("render season page", "error", err)
	}
}

// getSeries reads one Series through the reader; false when there is no
// reader or no such series.
func (s *Server) getSeries(ctx context.Context, ref types.NamespacedName) (*catalogv1.Series, bool) {
	if s.opts.Reader == nil {
		return nil, false
	}
	var series catalogv1.Series
	if err := s.opts.Reader.Get(ctx, ref, &series); err != nil {
		if !apierrors.IsNotFound(err) {
			logging.FromContext(ctx).Error("get series", "series", ref.String(), "error", err)
		}
		return nil, false
	}
	return &series, true
}

// listEpisodes lists a namespace's Episodes through the reader. Filtering
// by series and season happens in memory (episodeRows): the list is served
// from the cache, and a field index is worth adding only if this shows up.
func (s *Server) listEpisodes(ctx context.Context, namespace string) ([]catalogv1.Episode, error) {
	if s.opts.Reader == nil {
		return nil, nil
	}
	var list catalogv1.EpisodeList
	if err := s.opts.Reader.List(ctx, &list, client.InNamespace(namespace)); err != nil {
		return nil, err
	}
	return list.Items, nil
}

// seasonRows projects a Series' seasons, from the status rollup, in season
// order. The effective monitored flag is the spec override when there is
// one: it is what a toggle just wrote, and the rollup catches up on the
// next reconcile.
func seasonRows(series *catalogv1.Series) []views.SeasonRow {
	rows := make([]views.SeasonRow, 0, len(series.Status.Seasons))
	for _, st := range series.Status.Seasons {
		rows = append(rows, seasonRow(series, st.Number))
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Number < rows[j].Number })
	return rows
}

// seasonRow is one season of series, by number; a season the rollup does
// not know yet still renders, with no counts.
func seasonRow(series *catalogv1.Series, number int32) views.SeasonRow {
	row := views.SeasonRow{Series: series.Name, Namespace: series.Namespace, Number: number, Monitored: true}
	for _, st := range series.Status.Seasons {
		if st.Number == number {
			row.Monitored, row.Episodes, row.Files = st.Monitored, st.EpisodeCount, st.EpisodeFileCount
			if st.NextAiring != nil {
				row.NextAiring = st.NextAiring.UTC().Format("2006-01-02")
			}
		}
	}
	for _, sp := range series.Spec.Seasons {
		if sp.Number == number && sp.Monitored != nil {
			row.Monitored = *sp.Monitored
		}
	}
	return row
}

// episodeRows keeps the episodes of one season of one series, in episode
// order.
func episodeRows(episodes []catalogv1.Episode, series string, season int32) []views.EpisodeRow {
	var rows []views.EpisodeRow
	for i := range episodes {
		ep := &episodes[i]
		if ep.Spec.SeriesRef != series || ep.Spec.SeasonNumber != season {
			continue
		}
		row := views.EpisodeRow{
			Namespace: ep.Namespace, Name: ep.Name, Number: ep.Spec.EpisodeNumber,
			Title: ep.Status.Title, Monitored: monitoredOrDefault(ep.Spec.Monitored),
			HasFile: ep.Status.HasFile, Phase: string(ep.Status.Phase),
		}
		if ep.Status.AirDate != nil {
			row.AirDate = ep.Status.AirDate.UTC().Format("2006-01-02")
		}
		if ep.Status.FileQuality != nil {
			row.Quality = ep.Status.FileQuality.Name
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Number < rows[j].Number })
	return rows
}

// monitoredOrDefault reads a spec.monitored pointer as the CRD default
// would: nil is true.
func monitoredOrDefault(m *bool) bool { return m == nil || *m }
