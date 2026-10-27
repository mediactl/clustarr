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
	"testing"

	"github.com/stretchr/testify/require"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// An Episode reads its season's PackFirst test from its Series'
// status.seasons, with no season search state at all, beside the pending
// and answered inputs it read before (owner, 2026-10-27).
func TestSeasonOfReadsItsSeasonsPackFirstTest(t *testing.T) {
	ser := &catalogv1alpha1.Series{Status: catalogv1alpha1.SeriesStatus{Seasons: []catalogv1alpha1.SeasonStatus{
		{Number: 1, Monitored: true, EpisodeCount: 7},
		{Number: 2, Monitored: true, EpisodeCount: 7, EpisodeFileCount: 6},
	}}}
	ep := func(season int32) *catalogv1alpha1.Episode {
		return &catalogv1alpha1.Episode{Spec: catalogv1alpha1.EpisodeSpec{SeasonNumber: season, EpisodeNumber: 3}}
	}
	pending, answered, packFirst := seasonOf(ser, ep(1))
	require.False(t, pending)
	require.Nil(t, answered)
	require.True(t, packFirst)
	_, _, packFirst = seasonOf(ser, ep(2))
	require.False(t, packFirst, "one missing episode is searched as an episode")

	ser.Status.SeasonSearch = &catalogv1alpha1.SeasonSearchState{Seasons: []catalogv1alpha1.SeasonSearchSeason{
		{Number: 1, Phase: catalogv1alpha1.SeasonSearchQueued},
	}}
	pending, _, packFirst = seasonOf(ser, ep(1))
	require.True(t, pending)
	require.True(t, packFirst)
}
