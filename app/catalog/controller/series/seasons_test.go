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
	"github.com/mediactl/clustarr/app/catalog/controller/series"
	"github.com/mediactl/clustarr/pkg/metadata"
)

func episode(name string, season int32, monitored *bool) catalogv1alpha1.Episode {
	return catalogv1alpha1.Episode{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       catalogv1alpha1.EpisodeSpec{SeriesRef: "show", SeasonNumber: season, EpisodeNumber: 1, Monitored: monitored},
	}
}

// A season turned on sets every one of its episodes that is not on yet, and
// touches no other season.
func TestSeasonCascadeSetsTheSeasonsEpisodes(t *testing.T) {
	s := &catalogv1alpha1.Series{Spec: catalogv1alpha1.SeriesSpec{
		Seasons: []catalogv1alpha1.SeasonSpec{{Number: 2, Monitored: ptr.To(true)}},
	}}
	eps := []catalogv1alpha1.Episode{
		episode("s01e01", 1, ptr.To(false)),
		episode("s02e02", 2, ptr.To(false)),
		episode("s02e01", 2, ptr.To(false)),
		episode("s02e03", 2, ptr.To(true)),
	}
	assert.Equal(t, []series.MonitoredChange{
		{Name: "s02e01", Monitored: true},
		{Name: "s02e02", Monitored: true},
	}, series.SeasonCascade(s, eps))
}

// Once a season's override is recorded as applied, an episode toggled on
// its own afterwards keeps its flag: the cascade runs once per change.
func TestSeasonCascadeLeavesAnAppliedSeasonAlone(t *testing.T) {
	s := &catalogv1alpha1.Series{
		Spec: catalogv1alpha1.SeriesSpec{Seasons: []catalogv1alpha1.SeasonSpec{{Number: 1, Monitored: ptr.To(true)}}},
		Status: catalogv1alpha1.SeriesStatus{Seasons: []catalogv1alpha1.SeasonStatus{
			{Number: 1, AppliedMonitored: ptr.To(true)},
		}},
	}
	eps := []catalogv1alpha1.Episode{episode("s01e01", 1, ptr.To(false))}
	assert.Empty(t, series.SeasonCascade(s, eps))

	// Turning the season off again is a new change, and cascades.
	s.Spec.Seasons[0].Monitored = ptr.To(false)
	eps = []catalogv1alpha1.Episode{episode("s01e01", 1, ptr.To(false)), episode("s01e02", 1, nil)}
	assert.Equal(t, []series.MonitoredChange{{Name: "s01e02", Monitored: false}}, series.SeasonCascade(s, eps))
}

// A season without an override is never cascaded.
func TestSeasonCascadeIgnoresSeasonsWithoutAnOverride(t *testing.T) {
	s := &catalogv1alpha1.Series{Spec: catalogv1alpha1.SeriesSpec{
		Seasons: []catalogv1alpha1.SeasonSpec{{Number: 1}},
	}}
	assert.Empty(t, series.SeasonCascade(s, []catalogv1alpha1.Episode{episode("s01e01", 1, ptr.To(false))}))
}

// A season reads monitored when any of its episodes is, so a season whose
// episodes were all left unmonitored shows its toggle off.
func TestRollupSeasonMonitoredWhenAnyEpisodeIs(t *testing.T) {
	got := series.Rollup([]catalogv1alpha1.Episode{
		episode("s01e01", 1, ptr.To(false)),
		episode("s01e02", 1, ptr.To(true)),
		episode("s02e01", 2, ptr.To(false)),
		episode("s03e01", 3, nil),
	}, time.Now())
	require.Len(t, got.Seasons, 3)
	assert.True(t, got.Seasons[0].Monitored)
	assert.False(t, got.Seasons[1].Monitored)
	assert.True(t, got.Seasons[2].Monitored, "an unset flag is the schema's default, true")
}

// A new episode of a season with an override takes the override, before
// monitorNewItems and before the add-time monitor mode.
func TestDesiredEpisodesNewEpisodeTakesItsSeasonOverride(t *testing.T) {
	now := time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)
	s := &catalogv1alpha1.Series{
		ObjectMeta: metav1.ObjectMeta{Name: "the-expanse"},
		Spec: catalogv1alpha1.SeriesSpec{
			SeriesType:      catalogv1alpha1.SeriesTypeStandard,
			MonitorNewItems: catalogv1alpha1.MonitorNewChildrenAll,
			AddOptions:      catalogv1alpha1.SeriesAddOptions{Monitor: catalogv1alpha1.SeriesMonitorNone},
			Seasons: []catalogv1alpha1.SeasonSpec{
				{Number: 1, Monitored: ptr.To(true)},
				{Number: 7, Monitored: ptr.To(false)},
			},
		},
	}
	provided := []metadata.Episode{
		{SeasonNumber: 1, EpisodeNumber: 1},
		{SeasonNumber: 2, EpisodeNumber: 1},
		{SeasonNumber: 7, EpisodeNumber: 1},
	}
	// At add: season 1's override beats monitor=none; season 2 has none.
	got := series.DesiredEpisodes(s, false, map[string]bool{}, provided, now)
	require.Len(t, got, 3)
	assert.Equal(t, []bool{true, false, false}, []bool{*got[0].Monitored, *got[1].Monitored, *got[2].Monitored})

	// Later: season 7's override beats monitorNewItems=all.
	got = series.DesiredEpisodes(s, true, map[string]bool{}, provided, now)
	require.Len(t, got, 3)
	assert.Equal(t, []bool{true, true, false}, []bool{*got[0].Monitored, *got[1].Monitored, *got[2].Monitored})
}

// Specials do not count toward the series (Sonarr's series statistics
// leave season 0 out): the series' episode and file counts and its
// airings come from the regular seasons alone. The Specials row keeps its
// own counts and next airing.
func TestRollupLeavesSpecialsOutOfTheSeriesTotals(t *testing.T) {
	now := time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *metav1.Time { t := metav1.NewTime(now.Add(d)); return &t }
	ep := func(season int32, hasFile bool, air *metav1.Time) catalogv1alpha1.Episode {
		e := episode("e", season, ptr.To(true))
		e.Status.HasFile, e.Status.AirDate = hasFile, air
		return e
	}
	got := series.Rollup([]catalogv1alpha1.Episode{
		ep(0, true, at(-24*time.Hour)), // a special aired yesterday, on disk
		ep(0, false, at(24*time.Hour)), // a special airing tomorrow
		ep(1, true, at(-90*24*time.Hour)),
		ep(1, false, at(30*24*time.Hour)),
	}, now)
	assert.Equal(t, int32(2), got.EpisodeCount)
	assert.Equal(t, int32(1), got.EpisodeFileCount)
	require.NotNil(t, got.PreviousAiring)
	assert.Equal(t, now.Add(-90*24*time.Hour), got.PreviousAiring.UTC())
	require.NotNil(t, got.NextAiring)
	assert.Equal(t, now.Add(30*24*time.Hour), got.NextAiring.UTC())

	require.Len(t, got.Seasons, 2)
	specials := got.Seasons[0]
	assert.Equal(t, int32(0), specials.Number)
	assert.Equal(t, int32(2), specials.EpisodeCount)
	assert.Equal(t, int32(1), specials.EpisodeFileCount)
	require.NotNil(t, specials.NextAiring)
	assert.Equal(t, now.Add(24*time.Hour), specials.NextAiring.UTC())
}

// The series counts its missing episodes -- phase Wanted -- and those with a
// download in flight or a grab pending, specials left out, so the library
// can tell a complete series from one with gaps.
func TestRollupCountsMissingAndDownloadingEpisodes(t *testing.T) {
	ep := func(season int32, phase catalogv1alpha1.EpisodePhase) catalogv1alpha1.Episode {
		e := episode("e", season, ptr.To(true))
		e.Status.Phase = phase
		return e
	}
	got := series.Rollup([]catalogv1alpha1.Episode{
		ep(0, catalogv1alpha1.EpisodePhaseWanted),
		ep(0, catalogv1alpha1.EpisodePhaseDownloading),
		ep(1, catalogv1alpha1.EpisodePhaseWanted),
		ep(1, catalogv1alpha1.EpisodePhaseWanted),
		ep(1, catalogv1alpha1.EpisodePhaseDownloading),
		ep(1, catalogv1alpha1.EpisodePhaseDelayed),
		ep(1, catalogv1alpha1.EpisodePhaseImported),
		ep(2, catalogv1alpha1.EpisodePhaseUnmonitored),
		ep(2, catalogv1alpha1.EpisodePhaseUnaired),
	}, time.Now())
	assert.Equal(t, int32(2), got.MissingEpisodeCount)
	assert.Equal(t, int32(2), got.DownloadingEpisodeCount)
}
