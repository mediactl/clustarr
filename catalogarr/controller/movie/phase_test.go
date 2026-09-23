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
		cutoffEvaluated                 bool
		pendingGrab                     bool
		want                            catalogv1alpha1.MoviePhase
	}{
		{"unmonitored wins over everything", false, true, true, false, false, true, false, catalogv1alpha1.MoviePhaseUnmonitored},
		{"monitored, no metadata yet", true, false, false, false, false, true, false, catalogv1alpha1.MoviePhasePending},
		{"monitored, metadata ready, not available", true, true, false, false, false, true, false, catalogv1alpha1.MoviePhaseUnavailable},
		{"monitored, metadata ready, available", true, true, true, false, false, true, false, catalogv1alpha1.MoviePhaseWanted},
		{"a file already imported outranks availability", true, true, false, true, true, true, false, catalogv1alpha1.MoviePhaseImported},
		{"an imported file below cutoff", true, true, true, true, false, true, false, catalogv1alpha1.MoviePhaseCutoffUnmet},
		{"hasFile with stale metadata still reports pending -- a file cannot exist before the item is monitored/known", true, false, false, true, true, true, false, catalogv1alpha1.MoviePhasePending},

		// A pending grab is the delay-profile window. It is the only way
		// Delayed is reachable before a Download exists.
		{"a wanted movie with a pending grab is Delayed", true, true, true, false, false, true, true, catalogv1alpha1.MoviePhaseDelayed},
		{"an unavailable movie with a pending grab is still Delayed", true, true, false, false, false, true, true, catalogv1alpha1.MoviePhaseDelayed},
		{"a delayed upgrade outranks CutoffUnmet: the pending grab IS the upgrade story", true, true, true, true, false, true, true, catalogv1alpha1.MoviePhaseDelayed},
		{"but Imported outranks a delayed upgrade: a pending grab is an internal timer, not something the user can cancel", true, true, true, true, true, true, true, catalogv1alpha1.MoviePhaseImported},
		{"unmonitored still wins: a leftover pending grab is not the user's intent", false, true, true, false, false, true, true, catalogv1alpha1.MoviePhaseUnmonitored},

		// An unresolved QualityProfile means cutoffMet is no verdict at all.
		// The file must not read as an upgrade candidate (CutoffUnmet).
		{"a file whose cutoff was never evaluated is CutoffUnevaluated, not CutoffUnmet", true, true, true, true, false, false, false, catalogv1alpha1.MoviePhaseCutoffUnevaluated},
		{"a pending grab still outranks CutoffUnevaluated, as it does CutoffUnmet", true, true, true, true, false, false, true, catalogv1alpha1.MoviePhaseDelayed},
		{"no file and no profile is still Wanted: there is no cutoff question to answer", true, true, true, false, false, false, false, catalogv1alpha1.MoviePhaseWanted},
		{"Pending still wins: metadata readiness gates everything below it", true, false, true, false, false, true, true, catalogv1alpha1.MoviePhasePending},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, movie.Phase(c.monitored, c.metaReady, c.available, c.hasFile, false, c.cutoffMet, c.cutoffEvaluated, c.pendingGrab))
		})
	}
}

// TestPhaseTranscoded pins the owner's rule (CLAUDE.md, "Transcoding"): a
// transcoded file reads Transcoded wherever it would otherwise read Imported,
// CutoffUnmet or CutoffUnevaluated, and never CutoffUnmet. Unmonitored and
// Pending keep their precedence; the Download overlay, applied by the
// reconciler over this, keeps its own.
func TestPhaseTranscoded(t *testing.T) {
	cases := []struct {
		name                                 string
		monitored, metaReady, available      bool
		cutoffMet, cutoffEvaluated, pendingG bool
		want                                 catalogv1alpha1.MoviePhase
	}{
		{"where it would read Imported", true, true, true, true, true, false, catalogv1alpha1.MoviePhaseTranscoded},
		{"where it would read CutoffUnmet", true, true, true, false, true, false, catalogv1alpha1.MoviePhaseTranscoded},
		{"where it would read CutoffUnevaluated", true, true, true, false, false, false, catalogv1alpha1.MoviePhaseTranscoded},
		{"a leftover pending grab does not displace it, as it does not displace Imported", true, true, true, false, true, true, catalogv1alpha1.MoviePhaseTranscoded},
		{"availability is moot once there is a file", true, true, false, false, true, false, catalogv1alpha1.MoviePhaseTranscoded},
		{"Unmonitored keeps its precedence", false, true, true, true, true, false, catalogv1alpha1.MoviePhaseUnmonitored},
		{"Pending keeps its precedence", true, false, true, true, true, false, catalogv1alpha1.MoviePhasePending},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, movie.Phase(c.monitored, c.metaReady, c.available, true, true, c.cutoffMet, c.cutoffEvaluated, c.pendingG))
		})
	}
	assert.Equal(t, catalogv1alpha1.MoviePhaseWanted, movie.Phase(true, true, true, false, true, false, true, false),
		"with no file there is nothing transcoded, whatever the flag says")
}
