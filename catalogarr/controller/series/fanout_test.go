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

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/controller/series"
	"github.com/mediactl/clustarr/pkg/metadata"
)

func TestInitialEpisodeMonitored(t *testing.T) {
	now := time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)
	aired := now.AddDate(0, 0, -10)
	old := now.AddDate(0, 0, -200)
	unaired := now.AddDate(0, 0, 10)
	all := []series.EpisodeCandidate{
		{SeasonNumber: 0, EpisodeNumber: 1, AirDate: &old},
		{SeasonNumber: 1, EpisodeNumber: 1, AirDate: &old},
		{SeasonNumber: 1, EpisodeNumber: 2, AirDate: &aired},
		{SeasonNumber: 2, EpisodeNumber: 1, AirDate: &unaired},
	}
	cases := []struct {
		name string
		mode catalogv1alpha1.SeriesMonitorMode
		ep   series.EpisodeCandidate
		run  catalogv1alpha1.SeriesRunStatus
		want bool
	}{
		{"all monitors everything incl specials", catalogv1alpha1.SeriesMonitorAll, all[0], catalogv1alpha1.SeriesRunStatusContinuing, true},
		{"future only unaired, continuing series", catalogv1alpha1.SeriesMonitorFuture, all[3], catalogv1alpha1.SeriesRunStatusContinuing, true},
		{"future skips aired", catalogv1alpha1.SeriesMonitorFuture, all[1], catalogv1alpha1.SeriesRunStatusContinuing, false},
		{"future is false for an ended series even if unaired", catalogv1alpha1.SeriesMonitorFuture, all[3], catalogv1alpha1.SeriesRunStatusEnded, false},
		{"missing monitors aired episodes", catalogv1alpha1.SeriesMonitorMissing, all[1], catalogv1alpha1.SeriesRunStatusContinuing, true},
		{"missing skips unaired", catalogv1alpha1.SeriesMonitorMissing, all[3], catalogv1alpha1.SeriesRunStatusContinuing, false},
		{"existing is always false at add time", catalogv1alpha1.SeriesMonitorExisting, all[1], catalogv1alpha1.SeriesRunStatusContinuing, false},
		{"firstSeason matches the lowest positive season", catalogv1alpha1.SeriesMonitorFirstSeason, all[1], catalogv1alpha1.SeriesRunStatusContinuing, true},
		{"firstSeason excludes specials", catalogv1alpha1.SeriesMonitorFirstSeason, all[0], catalogv1alpha1.SeriesRunStatusContinuing, false},
		{"firstSeason excludes later seasons", catalogv1alpha1.SeriesMonitorFirstSeason, all[3], catalogv1alpha1.SeriesRunStatusContinuing, false},
		{"lastSeason matches the highest season", catalogv1alpha1.SeriesMonitorLastSeason, all[3], catalogv1alpha1.SeriesRunStatusContinuing, true},
		{"pilot is s01e01 only", catalogv1alpha1.SeriesMonitorPilot, all[1], catalogv1alpha1.SeriesRunStatusContinuing, true},
		{"pilot excludes s01e02", catalogv1alpha1.SeriesMonitorPilot, all[2], catalogv1alpha1.SeriesRunStatusContinuing, false},
		// all[1]'s AirDate is `old` (200 days ago) -- the brief's own test
		// snippet named this case "recent within 90 days" but pointed it at
		// all[1], which is self-contradictory (its wantAvail=true could not
		// be reconciled with a 200-day-old air date and a 90-day window).
		// all[2] (`aired`, 10 days ago) is the fixture that actually makes
		// this case true and is consistent with the adjacent
		// "recent excludes a 200-day-old episode"/all[0] case right below.
		{"recent within 90 days", catalogv1alpha1.SeriesMonitorRecent, all[2], catalogv1alpha1.SeriesRunStatusContinuing, true},
		{"recent excludes a 200-day-old episode", catalogv1alpha1.SeriesMonitorRecent, all[0], catalogv1alpha1.SeriesRunStatusContinuing, false},
		{"monitorSpecials sets season 0 true", catalogv1alpha1.SeriesMonitorMonitorSpecials, all[0], catalogv1alpha1.SeriesRunStatusContinuing, true},
		{"monitorSpecials leaves season 1 unmodified (same as the default/All)", catalogv1alpha1.SeriesMonitorMonitorSpecials, all[1], catalogv1alpha1.SeriesRunStatusContinuing, true},
		{"unmonitorSpecials sets season 0 false", catalogv1alpha1.SeriesMonitorUnmonitorSpecials, all[0], catalogv1alpha1.SeriesRunStatusContinuing, false},
		{"unmonitorSpecials leaves season 1 unmodified (same as the default/All)", catalogv1alpha1.SeriesMonitorUnmonitorSpecials, all[1], catalogv1alpha1.SeriesRunStatusContinuing, true},
		{"none is always false", catalogv1alpha1.SeriesMonitorNone, all[1], catalogv1alpha1.SeriesRunStatusContinuing, false},
		{"skip is always false at add time", catalogv1alpha1.SeriesMonitorSkip, all[1], catalogv1alpha1.SeriesRunStatusContinuing, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, series.InitialEpisodeMonitored(c.mode, c.ep, all, c.run, now))
		})
	}
}

// TestDesiredEpisodesAnimeAbsoluteOrdering exercises the corrected
// signature: DesiredEpisodes takes existingNames explicitly (see fanout.go's
// doc comment) rather than trying to infer it, so a first-fan-out call
// passes an empty set.
func TestDesiredEpisodesAnimeAbsoluteOrdering(t *testing.T) {
	now := time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)
	s := &catalogv1alpha1.Series{
		ObjectMeta: metav1.ObjectMeta{Name: "one-piece"},
		Spec: catalogv1alpha1.SeriesSpec{
			SeriesType: catalogv1alpha1.SeriesTypeAnime,
			AddOptions: catalogv1alpha1.SeriesAddOptions{Monitor: catalogv1alpha1.SeriesMonitorAll},
		},
	}
	abs1, abs2 := int32(1091), int32(1092)
	provided := []metadata.Episode{
		{SeasonNumber: 1, EpisodeNumber: 1091, AbsoluteNumber: &abs1, Title: "Episode 1091"},
		{SeasonNumber: 1, EpisodeNumber: 1092, AbsoluteNumber: &abs2, Title: "Episode 1092"},
	}
	got := series.DesiredEpisodes(s, false, map[string]bool{}, provided, now)
	require.Len(t, got, 2)
	assert.Equal(t, "one-piece-s01e1091", got[0].Name)
	require.NotNil(t, got[0].AbsoluteNumber)
	assert.EqualValues(t, 1091, *got[0].AbsoluteNumber)
	assert.True(t, got[0].Monitored != nil && *got[0].Monitored) // AddOptions.Monitor=All, first fan-out
	assert.Equal(t, "one-piece-s01e1092", got[1].Name)
}

func TestDesiredEpisodesAfterAddOptionsAppliedUsesMonitorNewItems(t *testing.T) {
	now := time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)
	s := &catalogv1alpha1.Series{
		ObjectMeta: metav1.ObjectMeta{Name: "the-expanse"},
		Spec: catalogv1alpha1.SeriesSpec{
			SeriesType:      catalogv1alpha1.SeriesTypeStandard,
			MonitorNewItems: catalogv1alpha1.MonitorNewChildrenNone,
		},
	}
	provided := []metadata.Episode{{SeasonNumber: 7, EpisodeNumber: 1, Title: "New Season"}}
	got := series.DesiredEpisodes(s, true, map[string]bool{}, provided, now) // addOptionsApplied already true: a later refresh
	require.Len(t, got, 1)
	assert.True(t, got[0].Monitored != nil && !*got[0].Monitored) // MonitorNewItems=none, and not an existing name
}

// TestDesiredEpisodesExistingEpisodeMonitoredStaysNil proves the third,
// brief-mandated case: once addOptionsApplied and the episode already exists
// (its name is in existingNames), Monitored must come back nil so the
// reconciler knows not to touch spec.monitored -- it belongs to the user
// after creation (EpisodeSpec.Monitored's own doc comment).
func TestDesiredEpisodesExistingEpisodeMonitoredStaysNil(t *testing.T) {
	now := time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)
	s := &catalogv1alpha1.Series{
		ObjectMeta: metav1.ObjectMeta{Name: "the-expanse"},
		Spec: catalogv1alpha1.SeriesSpec{
			SeriesType:      catalogv1alpha1.SeriesTypeStandard,
			MonitorNewItems: catalogv1alpha1.MonitorNewChildrenAll,
		},
	}
	provided := []metadata.Episode{{SeasonNumber: 1, EpisodeNumber: 1, Title: "Dulcinea"}}
	existing := map[string]bool{"the-expanse-s01e01": true}
	got := series.DesiredEpisodes(s, true, existing, provided, now)
	require.Len(t, got, 1)
	assert.Nil(t, got[0].Monitored, "an already-existing episode's monitored flag must not be touched")
}

func TestDesiredEpisodesDedupesByProvidedSeasonEpisode(t *testing.T) {
	now := time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)
	s := &catalogv1alpha1.Series{
		ObjectMeta: metav1.ObjectMeta{Name: "the-expanse"},
		Spec:       catalogv1alpha1.SeriesSpec{SeriesType: catalogv1alpha1.SeriesTypeStandard, AddOptions: catalogv1alpha1.SeriesAddOptions{Monitor: catalogv1alpha1.SeriesMonitorAll}},
	}
	provided := []metadata.Episode{
		{SeasonNumber: 1, EpisodeNumber: 1, Title: "first"},
		{SeasonNumber: 1, EpisodeNumber: 1, Title: "duplicate, first occurrence wins"},
	}
	got := series.DesiredEpisodes(s, false, map[string]bool{}, provided, now)
	require.Len(t, got, 1)
	assert.Equal(t, "first", got[0].Title)
}
