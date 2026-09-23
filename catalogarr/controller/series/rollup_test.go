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

package series_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/controller/series"
)

// TestRollup is honest-but-currently-inert in production: nothing in this
// task's own reconcilers ever sets Episode.Status.HasFile = true (that
// happens once the Episode controller's own MediaFile watch, added later in
// this same task, lands) -- but the arithmetic is exercised here with
// synthetic data regardless.
func TestRollup(t *testing.T) {
	eps := []catalogv1alpha1.Episode{
		{Spec: catalogv1alpha1.EpisodeSpec{SeasonNumber: 1, EpisodeNumber: 1}, Status: catalogv1alpha1.EpisodeStatus{HasFile: true}},
		{Spec: catalogv1alpha1.EpisodeSpec{SeasonNumber: 1, EpisodeNumber: 2}, Status: catalogv1alpha1.EpisodeStatus{HasFile: false}},
		{Spec: catalogv1alpha1.EpisodeSpec{SeasonNumber: 2, EpisodeNumber: 1}, Status: catalogv1alpha1.EpisodeStatus{HasFile: true}},
	}
	r := series.Rollup(eps, time.Now())
	seasons := r.Seasons
	require.Len(t, seasons, 2)
	assert.EqualValues(t, 3, r.EpisodeCount)
	assert.EqualValues(t, 2, r.EpisodeFileCount)
	// seasons sorted ascending by number, per +listMapKey=number's implied order
	assert.EqualValues(t, 1, seasons[0].Number)
	assert.EqualValues(t, 2, seasons[0].EpisodeCount)
	assert.EqualValues(t, 1, seasons[0].EpisodeFileCount)
	assert.EqualValues(t, 2, seasons[1].Number)
	assert.EqualValues(t, 1, seasons[1].EpisodeCount)
	assert.EqualValues(t, 1, seasons[1].EpisodeFileCount)
}

// TestRollupCountsATranscodedEpisode: a Transcoded episode has its file and
// meets its cutoff (the episode reconciler writes hasFile and cutoffMet true
// for it), so it counts toward the file totals exactly as an Imported one
// does -- the Series never reads the episode phase, only hasFile.
func TestRollupCountsATranscodedEpisode(t *testing.T) {
	eps := []catalogv1alpha1.Episode{
		{Spec: catalogv1alpha1.EpisodeSpec{SeasonNumber: 1, EpisodeNumber: 1}, Status: catalogv1alpha1.EpisodeStatus{
			Phase: catalogv1alpha1.EpisodePhaseTranscoded, HasFile: true, CutoffMet: true,
		}},
		{Spec: catalogv1alpha1.EpisodeSpec{SeasonNumber: 1, EpisodeNumber: 2}, Status: catalogv1alpha1.EpisodeStatus{
			Phase: catalogv1alpha1.EpisodePhaseImported, HasFile: true, CutoffMet: true,
		}},
		{Spec: catalogv1alpha1.EpisodeSpec{SeasonNumber: 1, EpisodeNumber: 3}, Status: catalogv1alpha1.EpisodeStatus{
			Phase: catalogv1alpha1.EpisodePhaseWanted,
		}},
	}
	r := series.Rollup(eps, time.Now())
	require.Len(t, r.Seasons, 1)
	assert.EqualValues(t, 3, r.EpisodeCount)
	assert.EqualValues(t, 2, r.EpisodeFileCount, "the Transcoded episode has its file")
	assert.EqualValues(t, 2, r.Seasons[0].EpisodeFileCount)
}

func TestRollupEmpty(t *testing.T) {
	r := series.Rollup(nil, time.Now())
	assert.Empty(t, r.Seasons)
	assert.Zero(t, r.EpisodeCount)
	assert.Zero(t, r.EpisodeFileCount)
	assert.Nil(t, r.NextAiring)
	assert.Nil(t, r.PreviousAiring)
}

// TestRollupAirings pins Sonarr's SeriesStatisticsRepository semantics:
// next is the earliest monitored air date at or after now, previous the
// latest monitored one before now, per season as well as per series, and an
// unmonitored or undated episode counts toward neither.
func TestRollupAirings(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *metav1.Time { v := metav1.NewTime(now.Add(d)); return &v }
	ep := func(season, number int32, air *metav1.Time, monitored bool) catalogv1alpha1.Episode {
		return catalogv1alpha1.Episode{
			Spec:   catalogv1alpha1.EpisodeSpec{SeasonNumber: season, EpisodeNumber: number, Monitored: ptr.To(monitored)},
			Status: catalogv1alpha1.EpisodeStatus{AirDate: air},
		}
	}
	day := 24 * time.Hour
	eps := []catalogv1alpha1.Episode{
		ep(1, 1, at(-30*day), true),
		ep(1, 2, at(-7*day), true),  // the previous airing
		ep(1, 3, at(-1*day), false), // later, but unmonitored
		ep(2, 1, at(0), true),       // airs exactly now: that is "next", not "previous"
		ep(2, 2, at(7*day), true),
		ep(3, 1, at(2*day), false), // unmonitored: season 3 has no next airing
		ep(3, 2, nil, true),        // no air date at all
	}

	r := series.Rollup(eps, now)
	require.NotNil(t, r.PreviousAiring)
	assert.True(t, r.PreviousAiring.Time.Equal(now.Add(-7*day)), "previous is the latest MONITORED airing before now")
	require.NotNil(t, r.NextAiring)
	assert.True(t, r.NextAiring.Time.Equal(now), "an airing at now is the next one (Sonarr: AirDateUtc >= now)")

	require.Len(t, r.Seasons, 3)
	assert.Nil(t, r.Seasons[0].NextAiring, "season 1 has aired")
	require.NotNil(t, r.Seasons[1].NextAiring)
	assert.True(t, r.Seasons[1].NextAiring.Time.Equal(now))
	assert.Nil(t, r.Seasons[2].NextAiring, "an unmonitored or undated episode is nobody's next airing")
	assert.EqualValues(t, 7, r.EpisodeCount, "counts are not filtered by monitoring")
}
