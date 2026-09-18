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

package movie_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/controller/movie"
)

func TestPhase(t *testing.T) {
	cases := []struct {
		name                            string
		monitored, metaReady, available bool
		hasFile, cutoffMet              bool
		pendingGrab                     bool
		want                            catalogv1alpha1.MoviePhase
	}{
		{"unmonitored wins over everything", false, true, true, false, false, false, catalogv1alpha1.MoviePhaseUnmonitored},
		{"monitored, no metadata yet", true, false, false, false, false, false, catalogv1alpha1.MoviePhasePending},
		{"monitored, metadata ready, not available", true, true, false, false, false, false, catalogv1alpha1.MoviePhaseUnavailable},
		{"monitored, metadata ready, available", true, true, true, false, false, false, catalogv1alpha1.MoviePhaseWanted},
		{"a file already imported outranks availability", true, true, false, true, true, false, catalogv1alpha1.MoviePhaseImported},
		{"an imported file below cutoff", true, true, true, true, false, false, catalogv1alpha1.MoviePhaseCutoffUnmet},
		{"hasFile with stale metadata still reports pending -- a file cannot exist before the item is monitored/known", true, false, false, true, true, false, catalogv1alpha1.MoviePhasePending},

		// A pending grab is the delay-profile window. It is the only way
		// Delayed is reachable before a Download exists.
		{"a wanted movie with a pending grab is Delayed", true, true, true, false, false, true, catalogv1alpha1.MoviePhaseDelayed},
		{"an unavailable movie with a pending grab is still Delayed", true, true, false, false, false, true, catalogv1alpha1.MoviePhaseDelayed},
		{"a delayed upgrade outranks CutoffUnmet: the in-flight action is what the phase should show", true, true, true, true, false, true, catalogv1alpha1.MoviePhaseDelayed},
		{"a delayed upgrade outranks Imported for the same reason", true, true, true, true, true, true, catalogv1alpha1.MoviePhaseDelayed},
		{"unmonitored still wins: a leftover pending grab is not the user's intent", false, true, true, false, false, true, catalogv1alpha1.MoviePhaseUnmonitored},
		{"Pending still wins: metadata readiness gates everything below it", true, false, true, false, false, true, catalogv1alpha1.MoviePhasePending},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, movie.Phase(c.monitored, c.metaReady, c.available, c.hasFile, c.cutoffMet, c.pendingGrab))
		})
	}
}
