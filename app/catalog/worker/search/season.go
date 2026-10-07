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

package search

import (
	"context"
	"fmt"
	"slices"

	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/worker/titleindex"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// SeasonSearchable reports a task the worker answers as a season search
// (series search design §5.2): MediaRef kind series with Season set. A
// series task without a season is refused: Sonarr has no interactive
// series search, and a series is searched one season at a time.
func SeasonSearchable(task schema.SearchTask) bool {
	return task.MediaRef.Kind == commonv1.MediaKindSeries && task.Season != nil
}

// snapshotSeason reads the Series and the season's Episodes (through
// titleindex.IndexEpisodeSeriesSeason) and builds the season snapshot
// (series search design §5.2, §6.1): the request's ids -- the series' tvdb
// id, the season's scene season, no episode, anime, title and year -- and
// the decision Target: kind series, monitored, available, anime, each
// episode's runtime, and the identity of every episode of the season
// (monitored or not: identity asks "is this release for this season", not
// "is it wanted") in season-search mode, with the series' TheXEM table and
// its library namesakes. Its owner is the Series.
func (w *Worker) snapshotSeason(ctx context.Context, ns string, ref commonv1.MediaRef, season int32) (itemSnapshot, error) {
	snap := itemSnapshot{
		Target: decision.Target{
			Kind: commonv1.MediaKindSeries,
			Key:  events.MediaKey(string(ref.Kind), ns, ref.Name),
		},
	}
	var s catalogv1alpha1.Series
	if err := w.Client.Get(ctx, client.ObjectKey{Namespace: ns, Name: ref.Name}, &s); err != nil {
		return snap, fmt.Errorf("get Series %s/%s: %w", ns, ref.Name, err)
	}
	var list catalogv1alpha1.EpisodeList
	if err := w.Client.List(ctx, &list, client.InNamespace(ns),
		client.MatchingFields{titleindex.IndexEpisodeSeriesSeason: titleindex.SeasonKey(s.Name, season)}); err != nil {
		return snap, fmt.Errorf("list the Episodes of %s season %d: %w", s.Name, season, err)
	}
	eps := make([]*catalogv1alpha1.Episode, 0, len(list.Items))
	for i := range list.Items {
		eps = append(eps, &list.Items[i])
	}
	slices.SortFunc(eps, func(a, b *catalogv1alpha1.Episode) int {
		return int(a.Spec.EpisodeNumber) - int(b.Spec.EpisodeNumber)
	})

	snap.QualityProfileRef = s.Spec.QualityProfileRef
	scene := SceneMappings(ctx, w.SceneMaps, s.Spec.TvdbID)
	anime := s.Spec.SeriesType == catalogv1alpha1.SeriesTypeAnime
	snap.IDs.TvdbID = s.Spec.TvdbID
	snap.IDs.Anime = anime
	snap.IDs.Season = new(SceneSeason(scene, season, eps))
	var seriesRuntime int32
	if md := s.Status.Metadata; md != nil {
		snap.IDs.Year = md.Year
		snap.IDs.OriginalLanguageTag = md.OriginalLanguage
		// The title alone on the wire: a season query names no number.
		snap.IDs.Title = md.Title
		snap.Target.OriginalLanguageTag = md.OriginalLanguage
		seriesRuntime = md.RuntimeMinutes
	}

	seriesMonitored := ptr.Deref(s.Spec.Monitored, true)
	now := w.now()
	runtimes := make(map[int]int, len(eps))
	anyMonitored, anyAvailable := false, false
	for _, e := range eps {
		anyMonitored = anyMonitored || ptr.Deref(e.Spec.Monitored, true)
		anyAvailable = anyAvailable || episodeAvailable(e, now)
		rt := e.Status.RuntimeMinutes
		if rt == 0 {
			rt = seriesRuntime
		}
		// Zero stays zero: pkg/decision applies its own 45-minute
		// fallback per episode.
		runtimes[int(e.Spec.EpisodeNumber)] = int(rt)
	}
	snap.Target.Monitored = seriesMonitored && anyMonitored
	snap.Target.Available = anyAvailable
	snap.Target.Anime = anime
	snap.Target.RuntimeByEpisode = runtimes

	snap.Target.Identity = EpisodeIdentity(&s, eps...)
	snap.Target.Identity.Season = int(season)
	snap.Target.Identity.SceneMappings = scene
	// A season search takes any release of the season: singles,
	// multi-episode releases and packs (decision.Identity.SeasonSearch).
	// Set here, at the season search's own call site, never in
	// EpisodeIdentity, which the RSS matcher shares.
	snap.Target.Identity.SeasonSearch = true
	snap.Target.Identity.Namesakes = w.namesakes(ctx, ns, &s)
	snap.Owner = ownerRef(string(commonv1.MediaKindSeries), &s)
	return snap, nil
}

// SceneSeason is the scene season most of eps map to in scene (TheXEM),
// the stored season when the series is unmapped. Each episode counts under
// its scene season, or its stored one where scene has no row for it; a tie
// keeps the stored season, else the lowest. A season the scene splits
// across two scene seasons is searched by the larger part; the other
// part's episodes are left to the sweep (series search design §12).
func SceneSeason(scene []decision.SceneMapping, season int32, eps []*catalogv1alpha1.Episode) int32 {
	if len(scene) == 0 || len(eps) == 0 {
		return season
	}
	counts := map[int32]int{}
	for _, e := range eps {
		s, _, _ := searchNumbering(scene, e)
		counts[s]++
	}
	best, bestN := season, counts[season]
	keys := make([]int32, 0, len(counts))
	for s := range counts {
		keys = append(keys, s)
	}
	slices.Sort(keys)
	for _, s := range keys {
		if counts[s] > bestN {
			best, bestN = s, counts[s]
		}
	}
	return best
}
