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

package album_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/controller/album"
)

func TestPhase(t *testing.T) {
	cases := []struct {
		name                                string
		monitored, hasFile, cutoffMet, grab bool
		want                                catalogv1alpha1.AlbumPhase
	}{
		{"unmonitored outranks everything", false, true, true, true, catalogv1alpha1.AlbumPhaseUnmonitored},
		{"imported", true, true, true, false, catalogv1alpha1.AlbumPhaseImported},
		{"imported outranks a pending grab", true, true, true, true, catalogv1alpha1.AlbumPhaseImported},
		{"pending grab reads delayed", true, false, false, true, catalogv1alpha1.AlbumPhaseDelayed},
		{"file below cutoff", true, true, false, false, catalogv1alpha1.AlbumPhaseCutoffUnmet},
		{"pending grab outranks a merely cutoff-unmet file", true, true, false, true, catalogv1alpha1.AlbumPhaseDelayed},
		{"no file, no grab: wanted", true, false, false, false, catalogv1alpha1.AlbumPhaseWanted},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := album.Phase(c.monitored, c.hasFile, c.cutoffMet, c.grab)
			assert.Equal(t, c.want, got)
		})
	}
}
