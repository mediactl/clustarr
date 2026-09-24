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

package rssmatcher

import (
	"context"
	"fmt"
	"sort"
	"time"

	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// Match maps one release onto the monitored catalog items it could satisfy.
//
// The order is §6.1's: external ids first, normalized title (and year) as
// the fallback. A non-video release carries no id its item has, so it is
// matched by the names on it alone (nonvideo.go). Ids are exact and cheap; a title match is a guess, so it is
// only consulted when the indexer gave no usable id. An unmonitored item is
// never returned -- §8.7 says "monitored items", and grabbing for an item the
// user switched off would be a surprise with a download attached.
//
// A returned MediaRef is shaped for grab.StatusTargets: a movie, a single
// episode, an album, a book, an audiobook or a comic issue is itself, and a
// pack is the Series with the Episode names in Keys.
//
// Match reads every episode number literally. The handler matches through
// matchWith, handing it the series' scene-numbering table.
func Match(ctx context.Context, c client.Client, namespace string, rel schema.Release) ([]commonv1.MediaRef, error) {
	return matchWith(ctx, c, namespace, rel, nil)
}

// sceneLookup returns a series' scene-numbering table by tvdb id (see
// search.SceneMappings). Nil reads every number literally.
type sceneLookup func(ctx context.Context, tvdbID int64) []decision.SceneMapping

func (l sceneLookup) table(ctx context.Context, tvdbID int64) []decision.SceneMapping {
	if l == nil {
		return nil
	}
	return l(ctx, tvdbID)
}

func matchWith(ctx context.Context, c client.Client, namespace string, rel schema.Release, scenes sceneLookup) ([]commonv1.MediaRef, error) {
	switch rel.Kind {
	case commonv1.MediaKindMovie:
		return matchMovie(ctx, c, namespace, rel)
	case commonv1.MediaKindSeries, commonv1.MediaKindEpisode:
		return matchSeries(ctx, c, namespace, rel, scenes)
	default:
		// An album, a book, an audiobook or a comic issue, by the names
		// indexarr put on the release (nonvideo.go). An unclassified
		// release is not something to guess at: it matches nothing.
		return matchNonVideo(ctx, c, namespace, rel)
	}
}

func matchMovie(ctx context.Context, c client.Client, namespace string, rel schema.Release) ([]commonv1.MediaRef, error) {
	var list catalogv1alpha1.MovieList
	if id := rel.Info.IDs[commonv1.IDKeyTMDB]; id != "" {
		if err := c.List(ctx, &list, client.InNamespace(namespace), client.MatchingFields{IndexMovieTmdbID: id}); err != nil {
			return nil, fmt.Errorf("rssmatcher: list movies by tmdb id: %w", err)
		}
	}
	if len(list.Items) == 0 {
		key := TitleYearKey(rel.ParsedTitle, rel.Year)
		if key == "" {
			return nil, nil
		}
		if err := c.List(ctx, &list, client.InNamespace(namespace), client.MatchingFields{IndexMovieTitleYear: key}); err != nil {
			return nil, fmt.Errorf("rssmatcher: list movies by title and year: %w", err)
		}
	}

	out := make([]commonv1.MediaRef, 0, len(list.Items))
	for i := range list.Items {
		m := &list.Items[i]
		if !ptr.Deref(m.Spec.Monitored, true) {
			continue
		}
		out = append(out, commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: m.Name})
	}
	return out, nil
}

func matchSeries(ctx context.Context, c client.Client, namespace string, rel schema.Release, scenes sceneLookup) ([]commonv1.MediaRef, error) {
	var list catalogv1alpha1.SeriesList
	if id := rel.Info.IDs[commonv1.IDKeyTVDB]; id != "" {
		if err := c.List(ctx, &list, client.InNamespace(namespace), client.MatchingFields{IndexSeriesTvdbID: id}); err != nil {
			return nil, fmt.Errorf("rssmatcher: list series by tvdb id: %w", err)
		}
	}
	if len(list.Items) == 0 {
		key := SeriesTitleKey(rel.ParsedTitle, rel.Year)
		if key == "" {
			return nil, nil
		}
		if err := c.List(ctx, &list, client.InNamespace(namespace), client.MatchingFields{IndexSeriesTitleYear: key}); err != nil {
			return nil, fmt.Errorf("rssmatcher: list series by title: %w", err)
		}
		if len(list.Items) > 1 {
			// Two series answer to one title ("Doctor Who" 1963 and 2005,
			// both titled plainly): a release naming neither year cannot say
			// which it is, and grabbing it for both would be wrong for one.
			// Sonarr refuses the same way (SeriesRepository.FindByTitle ->
			// ReturnSingleSeriesOrThrow). Counted before the monitored filter,
			// as Sonarr counts every series: an unmonitored namesake still
			// makes the title ambiguous.
			logging.FromContext(ctx).Debug("rssmatcher: release title names several series; not guessing",
				"series", len(list.Items))
			return nil, nil
		}
	}

	var out []commonv1.MediaRef
	for i := range list.Items {
		s := &list.Items[i]
		if !ptr.Deref(s.Spec.Monitored, true) {
			continue
		}
		episodes, err := episodesFor(ctx, c, namespace, s.Name, rel, scenes.table(ctx, s.Spec.TvdbID))
		if err != nil {
			return nil, err
		}
		if ref, ok := packRef(s.Name, episodes); ok {
			out = append(out, ref)
		}
	}
	return out, nil
}

// episodesFor resolves which of a Series' Episode objects a release covers.
//
// It resolves through the objects rather than by rendering an Episode name:
// the name scheme differs by series type (daily series are named by air date),
// and an episode the provider has not fanned out yet must not be invented. A
// release naming an episode that does not exist simply matches nothing.
//
// The release's numbering is read through the series' scene table first, the
// way pkg/decision's identity check reads it (decision.Identity.SceneMappings;
// Sonarr's ParsingService for a release from an indexer): a scene season pack
// is the TVDB episodes of that scene season, a scene SxxEyy is the TVDB
// episode its row names, an absolute number is the one TVDB episode its row
// names (read literally when no row, or more than one, has it). A mapping
// replaces the literal reading. The matcher and the identity check must
// agree: a release matched to the literal episode would be refused by the
// check as the wrong item, and one matched to nothing would never reach it.
//
// A release numbered only absolutely -- most anime -- is matched through the
// episodes' absolute numbers.
//
// A multi-season pack is deliberately NOT matched: §8.2's grab takes one lease
// per episode and creates one Download for the lot, and a pack spanning
// seasons is a large, rarely-wanted grab that the interactive search path
// should own. Sonarr treats them the same way.
func episodesFor(ctx context.Context, c client.Client, namespace, seriesName string, rel schema.Release, table []decision.SceneMapping) ([]string, error) {
	if rel.MultiSeason || len(rel.Seasons) > 1 {
		return nil, nil
	}
	scene := newSceneIndex(table)

	var eps []*catalogv1alpha1.Episode
	var err error
	switch {
	case len(rel.Seasons) == 1:
		eps, err = seasonEpisodes(ctx, c, namespace, seriesName, rel, scene)
	case len(rel.Absolute) > 0:
		eps, err = absoluteEpisodes(ctx, c, namespace, seriesName, rel.Absolute, scene)
	}
	if err != nil || len(eps) == 0 {
		return nil, err
	}

	seen := map[string]struct{}{}
	names := make([]string, 0, len(eps))
	for _, ep := range eps {
		if !ptr.Deref(ep.Spec.Monitored, true) {
			continue
		}
		if _, dup := seen[ep.Name]; dup {
			continue
		}
		seen[ep.Name] = struct{}{}
		names = append(names, ep.Name)
	}
	sort.Strings(names)
	return names, nil
}

// seasonEpisodes resolves a release that names one season: a full-season
// pack, SxxEyy episodes, or a daily air date.
func seasonEpisodes(ctx context.Context, c client.Client, namespace, seriesName string, rel schema.Release, scene sceneIndex) ([]*catalogv1alpha1.Episode, error) {
	season := rel.Seasons[0]
	switch {
	case rel.FullSeason:
		if mapped := scene.bySeason[season]; len(mapped) > 0 {
			return pairEpisodes(ctx, c, namespace, seriesName, mapped)
		}
		// A season pack covers every episode of the season the catalog
		// knows about.
		return listSeason(ctx, c, namespace, seriesName, season)
	case len(rel.Episodes) > 0:
		var pairs []seasonEpisode
		for _, e := range rel.Episodes {
			if mapped := scene.byEpisode[seasonEpisode{season, e}]; len(mapped) > 0 {
				pairs = append(pairs, mapped...)
				continue
			}
			pairs = append(pairs, seasonEpisode{season, e})
		}
		return pairEpisodes(ctx, c, namespace, seriesName, pairs)
	case rel.AirDate != nil:
		// A daily series names its episodes by date, not number.
		all, err := listSeason(ctx, c, namespace, seriesName, season)
		if err != nil {
			return nil, err
		}
		var out []*catalogv1alpha1.Episode
		for _, ep := range all {
			if ep.Status.AirDate != nil && sameDay(ep.Status.AirDate.Time, *rel.AirDate) {
				out = append(out, ep)
			}
		}
		return out, nil
	default:
		// A release that names a season but neither episodes nor a full
		// season is not something to guess at.
		return nil, nil
	}
}

// absoluteEpisodes resolves a release numbered only absolutely.
func absoluteEpisodes(ctx context.Context, c client.Client, namespace, seriesName string, absolutes []int32, scene sceneIndex) ([]*catalogv1alpha1.Episode, error) {
	var out []*catalogv1alpha1.Episode
	for _, a := range absolutes {
		if mapped := scene.byAbsolute[a]; len(mapped) == 1 {
			eps, err := pairEpisodes(ctx, c, namespace, seriesName, mapped)
			if err != nil {
				return nil, err
			}
			out = append(out, eps...)
			continue
		}
		var list catalogv1alpha1.EpisodeList
		if err := c.List(ctx, &list,
			client.InNamespace(namespace),
			client.MatchingFields{IndexEpisodeSeriesAbsolute: absoluteKey(seriesName, a)},
		); err != nil {
			return nil, fmt.Errorf("rssmatcher: list episodes of %s by absolute number %d: %w", seriesName, a, err)
		}
		for i := range list.Items {
			out = append(out, &list.Items[i])
		}
	}
	return out, nil
}

// pairEpisodes resolves (season, episode) pairs to Episode objects, one List
// per season.
func pairEpisodes(ctx context.Context, c client.Client, namespace, seriesName string, pairs []seasonEpisode) ([]*catalogv1alpha1.Episode, error) {
	bySeason := map[int32]map[int32]struct{}{}
	for _, p := range pairs {
		if bySeason[p.season] == nil {
			bySeason[p.season] = map[int32]struct{}{}
		}
		bySeason[p.season][p.episode] = struct{}{}
	}
	var out []*catalogv1alpha1.Episode
	for season, wanted := range bySeason {
		eps, err := listSeason(ctx, c, namespace, seriesName, season)
		if err != nil {
			return nil, err
		}
		for _, ep := range eps {
			if _, ok := wanted[ep.Spec.EpisodeNumber]; ok {
				out = append(out, ep)
			}
		}
	}
	return out, nil
}

func listSeason(ctx context.Context, c client.Client, namespace, seriesName string, season int32) ([]*catalogv1alpha1.Episode, error) {
	var list catalogv1alpha1.EpisodeList
	if err := c.List(ctx, &list,
		client.InNamespace(namespace),
		client.MatchingFields{IndexEpisodeSeriesSeason: seasonKey(seriesName, season)},
	); err != nil {
		return nil, fmt.Errorf("rssmatcher: list episodes of %s season %d: %w", seriesName, season, err)
	}
	out := make([]*catalogv1alpha1.Episode, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, &list.Items[i])
	}
	return out, nil
}

// seasonEpisode is one TVDB (season, episode) pair.
type seasonEpisode struct{ season, episode int32 }

// sceneIndex is a series' scene table keyed for the matcher's three
// lookups, with the same readings as pkg/decision's (identity_scene.go): a
// row naming no TVDB episode maps nothing and is skipped.
type sceneIndex struct {
	byEpisode  map[seasonEpisode][]seasonEpisode
	bySeason   map[int32][]seasonEpisode
	byAbsolute map[int32][]seasonEpisode
}

func newSceneIndex(rows []decision.SceneMapping) sceneIndex {
	idx := sceneIndex{
		byEpisode:  map[seasonEpisode][]seasonEpisode{},
		bySeason:   map[int32][]seasonEpisode{},
		byAbsolute: map[int32][]seasonEpisode{},
	}
	for _, r := range rows {
		if r.TVDB.Episode <= 0 {
			continue
		}
		tvdb := seasonEpisode{int32(r.TVDB.Season), int32(r.TVDB.Episode)}
		if r.Scene.Episode > 0 {
			k := seasonEpisode{int32(r.Scene.Season), int32(r.Scene.Episode)}
			idx.byEpisode[k] = append(idx.byEpisode[k], tvdb)
			idx.bySeason[k.season] = append(idx.bySeason[k.season], tvdb)
		}
		if r.Scene.Absolute > 0 {
			a := int32(r.Scene.Absolute)
			idx.byAbsolute[a] = append(idx.byAbsolute[a], tvdb)
		}
	}
	return idx
}

// packRef shapes the matched episodes the way grab.StatusTargets expects: one
// episode is itself, several are the Series narrowed by Keys, none is no
// match at all.
func packRef(seriesName string, episodes []string) (commonv1.MediaRef, bool) {
	switch len(episodes) {
	case 0:
		return commonv1.MediaRef{}, false
	case 1:
		return commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: episodes[0]}, true
	default:
		return commonv1.MediaRef{Kind: commonv1.MediaKindSeries, Name: seriesName, Keys: episodes}, true
	}
}

// sameDay compares two instants by calendar day in UTC. A daily episode is
// identified by its air date, and an indexer and a metadata provider rarely
// agree on the time of day.
func sameDay(a, b time.Time) bool {
	a, b = a.UTC(), b.UTC()
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}
