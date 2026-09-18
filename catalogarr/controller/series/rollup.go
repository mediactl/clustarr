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
	"sort"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// Rollup folds a Series' owned Episodes into the per-season status Sonarr
// keeps: seasons sorted ascending by number, the total episode count and
// the total episode-with-file count. It is honest-but-currently-inert in
// this task's own production wiring -- nothing in Task C6 ever sets
// Episode.Status.HasFile=true except the Episode controller's own MediaFile
// watch, which this same task lands -- but the arithmetic here does not
// depend on that being true yet.
func Rollup(episodes []catalogv1alpha1.Episode) (seasons []catalogv1alpha1.SeasonStatus, episodeCount, episodeFileCount int32) {
	bySeason := map[int32]*catalogv1alpha1.SeasonStatus{}
	var order []int32
	for _, ep := range episodes {
		n := ep.Spec.SeasonNumber
		s, ok := bySeason[n]
		if !ok {
			s = &catalogv1alpha1.SeasonStatus{Number: n}
			bySeason[n] = s
			order = append(order, n)
		}
		s.EpisodeCount++
		episodeCount++
		if ep.Status.HasFile {
			s.EpisodeFileCount++
			episodeFileCount++
		}
	}

	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })
	seasons = make([]catalogv1alpha1.SeasonStatus, 0, len(order))
	for _, n := range order {
		seasons = append(seasons, *bySeason[n])
	}
	return seasons, episodeCount, episodeFileCount
}
