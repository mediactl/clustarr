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
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// Match maps one release onto the monitored catalog items it could satisfy.
//
// The order is §6.1's: external ids first, normalized title+year as the
// fallback. Ids are exact and cheap; a title match is a guess, so it is only
// consulted when the indexer gave no usable id. An unmonitored item is never
// returned -- §8.7 says "monitored items", and grabbing for an item the user
// switched off would be a surprise with a download attached.
//
// A returned MediaRef is shaped for grab.StatusTargets: a movie or a single
// episode is itself, and a pack is the Series with the Episode names in Keys.
func Match(ctx context.Context, c client.Client, namespace string, rel schema.Release) ([]commonv1.MediaRef, error) {
	switch rel.Kind {
	case commonv1.MediaKindMovie:
		return matchMovie(ctx, c, namespace, rel)
	case commonv1.MediaKindSeries, commonv1.MediaKindEpisode:
		return matchSeries(ctx, c, namespace, rel)
	default:
		// §16 scopes catalogarr's non-video kinds to M6, and a release the
		// classifier could not type at all is not something to guess at.
		return nil, nil
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

func matchSeries(ctx context.Context, c client.Client, namespace string, rel schema.Release) ([]commonv1.MediaRef, error) {
	var list catalogv1alpha1.SeriesList
	if id := rel.Info.IDs[commonv1.IDKeyTVDB]; id != "" {
		if err := c.List(ctx, &list, client.InNamespace(namespace), client.MatchingFields{IndexSeriesTvdbID: id}); err != nil {
			return nil, fmt.Errorf("rssmatcher: list series by tvdb id: %w", err)
		}
	}
	if len(list.Items) == 0 {
		key := TitleYearKey(rel.ParsedTitle, rel.Year)
		if key == "" {
			return nil, nil
		}
		if err := c.List(ctx, &list, client.InNamespace(namespace), client.MatchingFields{IndexSeriesTitleYear: key}); err != nil {
			return nil, fmt.Errorf("rssmatcher: list series by title and year: %w", err)
		}
	}

	var out []commonv1.MediaRef
	for i := range list.Items {
		s := &list.Items[i]
		if !ptr.Deref(s.Spec.Monitored, true) {
			continue
		}
		episodes, err := episodesFor(ctx, c, namespace, s.Name, rel)
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
// A multi-season pack is deliberately NOT matched: §8.2's grab takes one lease
// per episode and creates one Download for the lot, and a pack spanning
// seasons is a large, rarely-wanted grab that the interactive search path
// should own. Sonarr treats them the same way.
func episodesFor(ctx context.Context, c client.Client, namespace, seriesName string, rel schema.Release) ([]string, error) {
	if rel.MultiSeason || len(rel.Seasons) != 1 {
		return nil, nil
	}
	season := rel.Seasons[0]

	var list catalogv1alpha1.EpisodeList
	if err := c.List(ctx, &list,
		client.InNamespace(namespace),
		client.MatchingFields{IndexEpisodeSeriesSeason: seasonKey(seriesName, season)},
	); err != nil {
		return nil, fmt.Errorf("rssmatcher: list episodes of %s season %d: %w", seriesName, season, err)
	}

	wanted := map[int32]struct{}{}
	for _, n := range rel.Episodes {
		wanted[n] = struct{}{}
	}

	names := make([]string, 0, len(list.Items))
	for i := range list.Items {
		ep := &list.Items[i]
		if !ptr.Deref(ep.Spec.Monitored, true) {
			continue
		}
		switch {
		case rel.FullSeason:
			// A season pack covers every episode of the season the catalog
			// knows about.
		case len(wanted) > 0:
			if _, ok := wanted[ep.Spec.EpisodeNumber]; !ok {
				continue
			}
		case rel.AirDate != nil:
			// A daily series names its episodes by date, not number.
			if ep.Status.AirDate == nil || !sameDay(ep.Status.AirDate.Time, *rel.AirDate) {
				continue
			}
		default:
			// A release that names a season but neither episodes nor a full
			// season is not something to guess at.
			return nil, nil
		}
		names = append(names, ep.Name)
	}
	sort.Strings(names)
	return names, nil
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
