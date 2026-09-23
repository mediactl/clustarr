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

package audiobook_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/controller/audiobook"
)

func TestPhase(t *testing.T) {
	cases := []struct {
		name               string
		monitored          bool
		hasFile, cutoffMet bool
		pendingGrab        bool
		want               catalogv1alpha1.AudiobookPhase
	}{
		{"unmonitored wins over everything", false, false, false, false, catalogv1alpha1.AudiobookPhaseUnmonitored},
		{"monitored, no file yet", true, false, false, false, catalogv1alpha1.AudiobookPhaseWanted},
		{"a file imported and meeting cutoff", true, true, true, false, catalogv1alpha1.AudiobookPhaseImported},
		{"an imported file below cutoff", true, true, false, false, catalogv1alpha1.AudiobookPhaseCutoffUnmet},

		// A pending grab is the delay-profile window. It is the only way
		// Delayed is reachable before a Download exists.
		{"a wanted audiobook with a pending grab is Delayed", true, false, false, true, catalogv1alpha1.AudiobookPhaseDelayed},
		{"a cutoff-unmet upgrade outranks CutoffUnmet: the pending grab IS the upgrade story", true, true, false, true, catalogv1alpha1.AudiobookPhaseDelayed},
		{"but Imported outranks a delayed upgrade: a pending grab is an internal timer, not something the user can cancel", true, true, true, true, catalogv1alpha1.AudiobookPhaseImported},
		{"unmonitored still wins: a leftover pending grab is not the user's intent", false, false, false, true, catalogv1alpha1.AudiobookPhaseUnmonitored},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, audiobook.Phase(c.monitored, c.hasFile, c.cutoffMet, c.pendingGrab))
		})
	}
}
