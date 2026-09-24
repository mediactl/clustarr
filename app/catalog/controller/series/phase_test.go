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

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/series"
)

func TestPhase(t *testing.T) {
	cases := []struct {
		name                         string
		monitored, metaReady, synced bool
		want                         catalogv1alpha1.SeriesPhase
	}{
		{"unmonitored wins", false, true, true, catalogv1alpha1.SeriesPhaseUnmonitored},
		{"pending until metadata ready", true, false, false, catalogv1alpha1.SeriesPhasePending},
		{"pending until episodes synced", true, true, false, catalogv1alpha1.SeriesPhasePending},
		{"ready once both are true", true, true, true, catalogv1alpha1.SeriesPhaseReady},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, series.Phase(c.monitored, c.metaReady, c.synced))
		})
	}
}
