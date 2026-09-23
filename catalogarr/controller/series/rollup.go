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
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// SeriesRollup is everything Rollup folds a Series' Episodes into.
type SeriesRollup struct {
	// Seasons is the per-season rollup, sorted ascending by number, each
	// with its own NextAiring.
	Seasons []catalogv1alpha1.SeasonStatus

	// EpisodeCount and EpisodeFileCount are the series totals.
	EpisodeCount, EpisodeFileCount int32

	// NextAiring and PreviousAiring are the series' next and most recent
	// airings; nil when there is none.
	NextAiring, PreviousAiring *metav1.Time
}

// Rollup folds a Series' owned Episodes into the per-season status Sonarr
// keeps: seasons sorted ascending by number, the total episode count, the
// total episode-with-file count, and the airing dates.
//
// The airings follow Sonarr's SeriesStatisticsRepository (and its per-season
// twin) exactly: NextAiring is the earliest air date at or after now,
// PreviousAiring the latest one before now, and both consider MONITORED
// episodes only -- Sonarr's query nulls out every row with Monitored = false
// before its MIN/MAX
// (https://github.com/Sonarr/Sonarr/blob/develop/src/NzbDrone.Core/SeriesStats/SeriesStatisticsRepository.cs).
// An episode with no air date counts toward neither. seriesRefreshState
// reads PreviousAiring for the "recently ended" refresh bucket; until this
// had a writer, every ended series fell through to the slow EndedOld TTL.
func Rollup(episodes []catalogv1alpha1.Episode, now time.Time) SeriesRollup {
	var out SeriesRollup
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
		out.EpisodeCount++
		if ep.Status.HasFile {
			s.EpisodeFileCount++
			out.EpisodeFileCount++
		}

		if ep.Status.AirDate == nil || !ptr.Deref(ep.Spec.Monitored, true) {
			continue
		}
		at := ep.Status.AirDate.Time
		if at.Before(now) {
			out.PreviousAiring = later(out.PreviousAiring, at)
			continue
		}
		out.NextAiring = earlier(out.NextAiring, at)
		s.NextAiring = earlier(s.NextAiring, at)
	}

	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })
	out.Seasons = make([]catalogv1alpha1.SeasonStatus, 0, len(order))
	for _, n := range order {
		out.Seasons = append(out.Seasons, *bySeason[n])
	}
	return out
}

func earlier(cur *metav1.Time, at time.Time) *metav1.Time {
	if cur == nil || at.Before(cur.Time) {
		t := metav1.NewTime(at)
		return &t
	}
	return cur
}

func later(cur *metav1.Time, at time.Time) *metav1.Time {
	if cur == nil || at.After(cur.Time) {
		t := metav1.NewTime(at)
		return &t
	}
	return cur
}
