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

package series

import (
	"fmt"
	"time"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// EffectiveEpisodeOrder returns the numbering scheme actually used: anime
// series are forced to absolute ordering regardless of spec (§4.2), every
// other series type keeps the requested order, falling back to official
// when order is the Go zero value. SeriesSpec.EpisodeOrder carries
// +kubebuilder:default=official, so a real object is never "" in practice;
// this function still handles it defensively since it is tested and used in
// isolation.
func EffectiveEpisodeOrder(seriesType catalogv1alpha1.SeriesType, order catalogv1alpha1.EpisodeOrder) catalogv1alpha1.EpisodeOrder {
	if seriesType == catalogv1alpha1.SeriesTypeAnime {
		return catalogv1alpha1.EpisodeOrderAbsolute
	}
	if order == "" {
		return catalogv1alpha1.EpisodeOrderOfficial
	}
	return order
}

// EpisodeName renders an Episode object's name per spec §4.2:
// "<series>-s<NN>e<NN>" for standard and anime series (the object name
// always numbers by season/episode; only the metadata fan-out reads
// absolute numbering, into status.absoluteNumber), and
// "<series>-<yyyy-mm-dd>" for daily series. %02d does not truncate a
// 3-or-4-digit episode number (Go's fmt prints at least the given width,
// never less), so a long-running anime's episode 1092 renders as "e1092",
// not a wrapped or truncated value.
func EpisodeName(seriesName string, seriesType catalogv1alpha1.SeriesType, season, episode int32, airDate *time.Time) string {
	if seriesType == catalogv1alpha1.SeriesTypeDaily && airDate != nil {
		return fmt.Sprintf("%s-%s", seriesName, airDate.Format("2006-01-02"))
	}
	return fmt.Sprintf("%s-s%02de%02d", seriesName, season, episode)
}
