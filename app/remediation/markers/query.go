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

package markers

import (
	"context"
	"strconv"

	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/episodeorder"
	catalogmarkers "github.com/mediactl/clustarr/app/catalog/markers"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// The provider id keys, metadata.KeyTMDB and metadata.KeyTVDB's values,
// spelled here so the manager links no pkg/metadata.
const (
	keyTMDB = "tmdb"
	keyTVDB = "tvdb"
)

// query names mf to TheIntroDB from the cache: a movie by its TMDB id; an
// episode by its series' TVDB id, season and episode, only in the aired
// order; a file holding several episodes, or an episode in another order, is
// a NotFound the loop writes itself (§4.12). It also returns the episode,
// for the segment plan. An item the cache does not hold yet is an error the
// caller makes Transient.
func query(ctx context.Context, c client.Reader, mf *catalogv1alpha1.MediaFile) (catalogmarkers.Query, *catalogv1alpha1.Episode, error) {
	key := client.ObjectKey{Namespace: mf.Namespace, Name: mf.Spec.MediaRef.Name}
	switch mf.Spec.MediaRef.Kind {
	case commonv1.MediaKindMovie:
		var mv catalogv1alpha1.Movie
		if err := c.Get(ctx, key, &mv); err != nil {
			return catalogmarkers.Query{}, nil, err
		}
		return catalogmarkers.Query{Ask: &schema.MarkersQuery{IDs: map[string]string{keyTMDB: strconv.FormatInt(mv.Spec.TmdbID, 10)}}}, nil, nil
	default:
		var ep catalogv1alpha1.Episode
		if err := c.Get(ctx, key, &ep); err != nil {
			return catalogmarkers.Query{}, nil, err
		}
		if others := otherEpisodes(mf.Spec.MediaRef); others > 0 {
			return catalogmarkers.Query{NotAsked: catalogmarkers.NotAskedMultiEpisode(others)}, &ep, nil
		}
		var sr catalogv1alpha1.Series
		if err := c.Get(ctx, client.ObjectKey{Namespace: mf.Namespace, Name: ep.Spec.SeriesRef}, &sr); err != nil {
			return catalogmarkers.Query{}, nil, err
		}
		if order := episodeorder.EffectiveEpisodeOrder(sr.Spec.SeriesType, sr.Spec.EpisodeOrder); order != catalogv1alpha1.EpisodeOrderOfficial {
			return catalogmarkers.Query{NotAsked: catalogmarkers.NotAskedOrder(order)}, &ep, nil
		}
		tvdb := strconv.FormatInt(sr.Spec.TvdbID, 10)
		return catalogmarkers.Query{
			Ask:       &schema.MarkersQuery{IDs: map[string]string{keyTVDB: tvdb}, Season: ep.Spec.SeasonNumber, Episode: ep.Spec.EpisodeNumber},
			SeriesKey: keyTVDB + ":" + tvdb,
		}, &ep, nil
	}
}

// otherEpisodes counts the episodes a file's ref names besides its own.
func otherEpisodes(ref commonv1.MediaRef) int {
	n := 0
	for _, k := range ref.Keys {
		if k != ref.Name {
			n++
		}
	}
	return n
}
