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

// An unmonitored Album reads Unmonitored from its first pass on, so neither
// its document nor its track list arriving moves any of the rollup's leaves
// (a document without a release date); the artist's folder scan waits for
// both (design D18), so whichever arrives last wakes the artist, in either
// order: the track list from the Album's own lookup, often first, or the
// document from the queued metadata task. A later edit of a listed Album's
// tracks does not.
func TestAnAlbumsTrackListWakesItsArtist(t *testing.T) {
	timedOut := catalogv1alpha1.Album{
		ObjectMeta: metav1.ObjectMeta{Namespace: "music", Name: "muse-origin", Generation: 1},
		Spec:       catalogv1alpha1.AlbumSpec{ArtistRef: "muse", Monitored: ptr.To(true)},
		Status: catalogv1alpha1.AlbumStatus{Phase: commonv1.LeafPhaseUnmonitored, Conditions: []metav1.Condition{
			{Type: "TracksSynced", Status: metav1.ConditionFalse, Reason: events.RequestTimeout.Reason()},
		}},
	}
	tracked := func(alb catalogv1alpha1.Album) catalogv1alpha1.Album {
		out := *alb.DeepCopy()
		out.Status.Tracks = []catalogv1alpha1.Track{{RecordingID: "r1", Medium: 1, Number: 1, Title: "New Born"}}
		out.Status.Conditions = []metav1.Condition{{Type: "TracksSynced", Status: metav1.ConditionTrue, Reason: "Best"}}
		return out
	}
	documented := func(alb catalogv1alpha1.Album) catalogv1alpha1.Album {
		out := *alb.DeepCopy()
		out.Status.Metadata = &catalogv1alpha1.AlbumMetadata{Title: "Origin of Symmetry", RefreshedAt: metav1.Now()}
		return out
	}
	changed := func(old, cur catalogv1alpha1.Album) bool {
		return albumRollupChanged().Update(event.UpdateEvent{ObjectOld: &old, ObjectNew: &cur})
	}

	// The document first, then the track list.
	withDoc := documented(timedOut)
	listed := tracked(withDoc)
	assert.True(t, changed(withDoc, listed), "the track list's arrival after the document wakes the artist")

	// The track list first, then the document.
	withTracks := tracked(timedOut)
	assert.False(t, changed(timedOut, withTracks), "a track list without its document leaves the album to come")
	assert.True(t, changed(withTracks, documented(withTracks)), "the document's arrival after the track list wakes the artist")

	retitled := *listed.DeepCopy()
	retitled.Status.Tracks[0].Title = "New Born (Remastered)"
	assert.False(t, changed(listed, retitled), "a listed Album's later track edits do not")
}
