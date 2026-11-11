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
	"path/filepath"
	"sort"
	"time"

	"k8s.io/apimachinery/pkg/types"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/ui/views"
)

// An episode's page (owner, 2026-11-03: "pages for TV episodes ... the same
// variety of info as the movie page") is filled as the movie page is, from
// what the cluster already holds: the Episode, its Series' library card
// and metadata, and its MediaFile. Nothing here fetches from a provider,
// and nothing here writes.

// handleEpisodePage serves GET /library/{namespace}/episode/{name}, the
// page an episode's title opens (and preloads).
func (s *Server) handleEpisodePage(w http.ResponseWriter, r *http.Request) {
	s.renderEpisodePage(w, r, true)
}

// renderEpisodePage answers the episode's page, preloadable when the route
// is one a link preloads; not found when the episode, its series' card or
// the reader is missing.
func (s *Server) renderEpisodePage(w http.ResponseWriter, r *http.Request, preload bool) {
	v, ok := s.episodePage(r.Context(), r.PathValue("namespace"), r.PathValue("name"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	if preload {
		preloadable(w)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.EpisodePage(v).Render(r.Context(), w); err != nil {
		logging.FromContext(r.Context()).Error("render episode page", "err", err)
	}
}

// episodePage builds an episode's page: its own title, number, air date,
// runtime, overview, still, audio, status, file, extra files and
// subtitles, its series' poster, fanart, quality profile, certification,
// language, network and genres, and the episodes either side of it.
func (s *Server) episodePage(ctx context.Context, ns, name string) (views.EpisodeView, bool) {
	var ep catalogv1.Episode
	if !s.getObject(ctx, types.NamespacedName{Namespace: ns, Name: name}, &ep) {
		return views.EpisodeView{}, false
	}
	item, ok := findLibraryItem(s.opts.Library(ctx), ns, string(commonv1.MediaKindSeries), ep.Spec.SeriesRef)
	if !ok {
		return views.EpisodeView{}, false
	}
	f := s.episodeFile(ctx, &ep)
	v := views.EpisodeView{
		Detail:       views.Detail{Item: item},
		Row:          episodeRow(&ep, f, time.Now()),
		SeasonLabel:  views.SeasonRow{Number: ep.Spec.SeasonNumber}.Label(),
		CutoffReason: ep.Status.CutoffReason,
	}
	if a := ep.Status.AbsoluteNumber; a != nil {
		v.Absolute = *a
	}
	folder := ""
	if series, ok := s.getSeries(ctx, item.Ref); ok {
		fillSeries(&v.Detail, series)
		folder = series.Status.Path
	}
	// The episode's own over its series': the series' overview and
	// alternative titles are its page's; its runtime stands in for an
	// episode without one.
	seriesRuntime := v.Runtime
	v.Overview, v.Runtime, v.AltTitles = ep.Status.Overview, runtimeLabel(ep.Status.RuntimeMinutes), nil
	if v.Runtime == "" {
		v.Runtime = seriesRuntime
	}
	v.Links, v.SeriesLinks = episodeLinks(&ep), seriesLinks(v.Links)
	v.Audio = audioNote(ep.Status.Audio)
	v.Still = s.episodeStill(&ep)
	if f != nil {
		v.Files, v.Extras = fileRows(folder, f)
		v.SubtitlesPhase, v.Subtitles = subtitleRows(f)
		v.Path = filepath.Dir(f.Spec.Path)
	}
	v.Prev, v.Next = s.episodeNeighbours(ctx, &ep)
	return v, true
}

// episodeLinks are the episode's own provider pages: its TVDB page.
func episodeLinks(ep *catalogv1.Episode) []views.Link {
	if ep.Status.TvdbID <= 0 {
		return nil
	}
	return []views.Link{{Label: "TVDB", Href: fmt.Sprintf("https://thetvdb.com/dereferrer/episode/%d", ep.Status.TvdbID), Provider: views.ProviderTVDB}}
}

// seriesLinks are the series' provider pages as the episode's page names
// them, "TVDB (series)", so a link's title says whose page it opens.
func seriesLinks(series []views.Link) []views.Link {
	links := make([]views.Link, 0, len(series))
	for _, l := range series {
		links = append(links, views.Link{Label: l.Label + " (series)", Href: l.Href, Provider: l.Provider})
	}
	return links
}

// episodeStill is the episode's still (TVDB's screenshot) through
// /art/search, since the browser never fetches a provider's image itself
// (ADR-0011); "" when it has none on a host the proxy fetches from.
func (s *Server) episodeStill(ep *catalogv1.Episode) string {
	for _, img := range ep.Status.Images {
		if img.Type != catalogv1.ImageTypeScreenshot {
			continue
		}
		if u := s.searchArt.URL(img.URL); u != "" {
			return u
		}
	}
	return ""
}

// episodeNeighbours are the pages of the episodes either side of ep in its
// series, in season and episode order as the series' page lists them.
func (s *Server) episodeNeighbours(ctx context.Context, ep *catalogv1.Episode) (prev, next string) {
	episodes, err := s.listEpisodes(ctx, ep.Namespace, ep.Spec.SeriesRef)
	if err != nil {
		logging.FromContext(ctx).Error("list episodes for an episode's neighbours", "err", err)
		return "", ""
	}
	sort.Slice(episodes, func(i, j int) bool {
		a, b := episodes[i].Spec, episodes[j].Spec
		if a.SeasonNumber != b.SeasonNumber {
			return a.SeasonNumber < b.SeasonNumber
		}
		return a.EpisodeNumber < b.EpisodeNumber
	})
	url := func(e *catalogv1.Episode) string { return views.EpisodeRow{Namespace: e.Namespace, Name: e.Name}.URL() }
	for i := range episodes {
		if episodes[i].Name != ep.Name {
			continue
		}
		if i > 0 {
			prev = url(&episodes[i-1])
		}
		if i+1 < len(episodes) {
			next = url(&episodes[i+1])
		}
		break
	}
	return prev, next
}
