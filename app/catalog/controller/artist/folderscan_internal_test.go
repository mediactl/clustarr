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

// Internal (package artist) so it can call the unexported albumRollupChanged.
package artist

import (
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/event"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
)

// An unmonitored Album reads Unmonitored from its first pass on, so its
// track list arriving moves none of the rollup's leaves; the artist's folder
// scan waits for that list (design D18), so the list's arrival alone wakes
// the artist. A later edit of a listed Album's tracks does not.
func TestAnAlbumsTrackListWakesItsArtist(t *testing.T) {
	timedOut := catalogv1alpha1.Album{
		ObjectMeta: metav1.ObjectMeta{Namespace: "music", Name: "muse-origin", Generation: 1},
		Spec:       catalogv1alpha1.AlbumSpec{ArtistRef: "muse", Monitored: ptr.To(true)},
		Status: catalogv1alpha1.AlbumStatus{Phase: commonv1.LeafPhaseUnmonitored, Conditions: []metav1.Condition{
			{Type: "TracksSynced", Status: metav1.ConditionFalse, Reason: events.RequestTimeout.Reason()},
		}},
	}
	listed := *timedOut.DeepCopy()
	listed.Status.Tracks = []catalogv1alpha1.Track{{RecordingID: "r1", Medium: 1, Number: 1, Title: "New Born"}}
	listed.Status.Conditions = []metav1.Condition{{Type: "TracksSynced", Status: metav1.ConditionTrue, Reason: "Best"}}
	assert.True(t, albumRollupChanged().Update(event.UpdateEvent{ObjectOld: &timedOut, ObjectNew: &listed}),
		"the track list's arrival wakes the artist")

	retitled := *listed.DeepCopy()
	retitled.Status.Tracks[0].Title = "New Born (Remastered)"
	assert.False(t, albumRollupChanged().Update(event.UpdateEvent{ObjectOld: &listed, ObjectNew: &retitled}),
		"a listed Album's later track edits do not")
}
