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

package subtitleprofile

import (
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/event"

	"github.com/mediactl/clustarr/pkg/k8s"
)

// TestMediaFileWatchWakesOnProbeHashWithoutGenerationBump is the test the F-3
// brief asks for by name: catalogarr's mediafile controller writes
// status.probeHash under ITS OWN field manager (k8s.ManagerCatalogarr), which
// -- per CLAUDE.md's "status writes by other managers do not bump
// metadata.generation" -- never changes generation. A watch built on
// k8s.GenerationChanged() alone would therefore never fire when a file goes
// from unprobed to probed, and this controller would never learn a
// previously-ineligible (unprobed) file became eligible. This is the whole
// reason the MediaFile Watches call in controller.go's SetupWithManager uses
// k8s.StatusFieldChanged(extractProbeHash) instead.
func TestMediaFileWatchWakesOnProbeHashWithoutGenerationBump(t *testing.T) {
	p := k8s.StatusFieldChanged(extractProbeHash)

	before := movieFile("arrival-2016", nil)
	before.Generation = 1
	before.Status.ProbeHash = ""

	after := movieFile("arrival-2016", nil)
	after.Generation = 1 // unchanged -- a status-only write from catalogarr's own manager
	after.Status.ProbeHash = "sha1-of-the-probe"

	if !p.Update(event.UpdateEvent{ObjectOld: before, ObjectNew: after}) {
		t.Fatal("a probeHash landing (generation unchanged) must wake the watch")
	}

	// A generation-only predicate, by contrast, must NOT fire for the exact
	// same event -- proving StatusFieldChanged is doing real work here, not
	// just being equivalent to GenerationChanged by coincidence.
	gen := k8s.GenerationChanged()
	if gen.Update(event.UpdateEvent{ObjectOld: before, ObjectNew: after}) {
		t.Fatal("GenerationChanged fired on a status-only write; the test fixture no longer demonstrates the gap StatusFieldChanged closes")
	}
}

// TestMediaFileWatchIgnoresAnUnrelatedStatusWrite proves the predicate does
// not simply fire on every update to a probed file -- only a change to
// probeHash itself wakes a reconcile, matching transcodeprofile's identical
// proof for the same predicate function shape
// (pkg/k8s/predicates_test.go's TestStatusFieldChangedIgnoresMetadataChurn).
func TestMediaFileWatchIgnoresAnUnrelatedStatusWrite(t *testing.T) {
	p := k8s.StatusFieldChanged(extractProbeHash)

	before := movieFile("arrival-2016", nil)
	before.Status.ProbeHash = "same-hash"
	after := movieFile("arrival-2016", map[string]string{"catalog.clustarr.io/movie": "arrival"})
	after.Status.ProbeHash = "same-hash"
	after.ResourceVersion = "999"

	if p.Update(event.UpdateEvent{ObjectOld: before, ObjectNew: after}) {
		t.Fatal("an unrelated metadata/status write woke the probeHash watcher")
	}
}
