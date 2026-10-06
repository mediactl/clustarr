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

package plex

import (
	"testing"

	"github.com/stretchr/testify/require"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// TestEffectiveOrderAnimeKeepsTheOfficialOrder holds the mirror of
// series.EffectiveEpisodeOrder to the same rule: series type anime no longer
// forces absolute order; only spec.episodeOrder changes it.
func TestEffectiveOrderAnimeKeepsTheOfficialOrder(t *testing.T) {
	s := &catalogv1.Series{Spec: catalogv1.SeriesSpec{SeriesType: catalogv1.SeriesTypeAnime}}
	require.Equal(t, catalogv1.EpisodeOrderOfficial, effectiveOrder(s))
	s.Spec.EpisodeOrder = catalogv1.EpisodeOrderAbsolute
	require.Equal(t, catalogv1.EpisodeOrderAbsolute, effectiveOrder(s))
}
