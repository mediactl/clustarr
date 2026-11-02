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

package downloads

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// satisfied is searchplan.RedownloadDue's exception read the same way for
// lifecycle: an item that holds a file and wants nothing owes no redownload.
func TestSatisfiedIsAFileThatWantsNothing(t *testing.T) {
	now := time.Date(2026, 11, 2, 21, 0, 0, 0, time.UTC)
	ep := func(hasFile bool, phase catalogv1alpha1.EpisodePhase) *catalogv1alpha1.Episode {
		e := &catalogv1alpha1.Episode{}
		e.Status.HasFile, e.Status.Phase = hasFile, phase
		return e
	}
	assert.True(t, satisfied(ep(true, catalogv1alpha1.EpisodePhaseTranscoded), now))
	assert.False(t, satisfied(ep(true, catalogv1alpha1.EpisodePhaseCutoffUnmet), now), "below its cutoff it wants one")
	assert.False(t, satisfied(ep(false, catalogv1alpha1.EpisodePhaseWanted), now))
	assert.False(t, satisfied(ep(false, catalogv1alpha1.EpisodePhaseDownloading), now), "no file, whatever the phase")
	assert.False(t, satisfied(&catalogv1alpha1.Album{}, now), "an album holds no file of its own")
}
