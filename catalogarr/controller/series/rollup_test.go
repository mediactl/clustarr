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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
	seasons, count, fileCount := series.Rollup(eps)
	require.Len(t, seasons, 2)
	assert.EqualValues(t, 3, count)
	assert.EqualValues(t, 2, fileCount)
	// seasons sorted ascending by number, per +listMapKey=number's implied order
	assert.EqualValues(t, 1, seasons[0].Number)
	assert.EqualValues(t, 2, seasons[0].EpisodeCount)
	assert.EqualValues(t, 1, seasons[0].EpisodeFileCount)
	assert.EqualValues(t, 2, seasons[1].Number)
	assert.EqualValues(t, 1, seasons[1].EpisodeCount)
	assert.EqualValues(t, 1, seasons[1].EpisodeFileCount)
}

func TestRollupEmpty(t *testing.T) {
	seasons, count, fileCount := series.Rollup(nil)
	assert.Empty(t, seasons)
	assert.Zero(t, count)
	assert.Zero(t, fileCount)
}
