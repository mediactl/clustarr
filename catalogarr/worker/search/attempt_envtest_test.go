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

package search_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/controller/wantedcron"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// TestWorkerRecordsASearchAttempt closes the round-1 gap: nothing was stamping
// status.lastSearchedAt / status.searchAttempts, so wantedcron.Backoff had
// nothing to count and stayed flat at MinimumGap forever -- every
// twelve-hourly sweep re-searching every still-wanted item.
func TestWorkerRecordsASearchAttempt(t *testing.T) {
	ctx := context.Background()
	f := newWorkerFixture(t, "worker-attempt")

	require.NoError(t, f.worker.Handle(ctx, testMessage{env: f.envelope(t, schema.SearchTask{
		MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "the-matrix"},
		Reason:   schema.SearchReasonMissing,
	})}))

	got := &catalogv1alpha1.Movie{}
	require.NoError(t, f.api.Get(ctx, client.ObjectKey{Namespace: f.ns, Name: "the-matrix"}, got))
	require.NotNil(t, got.Status.LastSearchedAt)
	require.NotNil(t, got.Status.SearchAttempts.Initial)
	require.NotNil(t, got.Status.SearchAttempts.Latest)
	require.Equal(t, int32(1), got.Status.SearchAttempts.Count)

	// And the point of recording it: the item is now held back by the ladder.
	require.False(t, wantedcron.Eligible(got.Status.SearchAttempts, f.worker.Clock.Now()),
		"a just-searched item must not be swept again inside the six-hour floor")

	firstInitial := got.Status.SearchAttempts.Initial.DeepCopy()

	require.NoError(t, f.worker.Handle(ctx, testMessage{env: f.envelope(t, schema.SearchTask{
		MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "the-matrix"},
		Reason:   schema.SearchReasonMissing,
	})}))

	require.NoError(t, f.api.Get(ctx, client.ObjectKey{Namespace: f.ns, Name: "the-matrix"}, got))
	require.Equal(t, int32(2), got.Status.SearchAttempts.Count)
	require.Equal(t, firstInitial.UTC(), got.Status.SearchAttempts.Initial.UTC(),
		"Initial is stamped once and never moved")
}

// TestWorkerRecordingAnAttemptLeavesTheGrabPathsFieldsIntact is the assertion
// that belongs on this side of the boundary.
//
// grab.RecordSearchAttempt writes under k8s.ManagerCatalogarrGrab, which also
// owns status.activeDownloadRef and status.pendingGrab. Server-side apply
// replaces a manager's whole ownership set, so a helper that declared only the
// two timestamp fields would delete a delayed item's pendingGrab and take it
// out of Phase=Delayed back to Wanted -- where the wanted cron would re-search
// an item that already had a grab scheduled. That is the exact failure that
// split catalogarr-worker into per-consumer field managers, and it is why this
// package calls into grab instead of reimplementing the cycle. C9 guarantees
// it; this pins the guarantee from the caller's side, where a regression in
// grab would otherwise surface as a mystery in search.
func TestWorkerRecordingAnAttemptLeavesTheGrabPathsFieldsIntact(t *testing.T) {
	ctx := context.Background()
	f := newWorkerFixture(t, "worker-attempt-coexist")

	// Drive the item to the steady state the guarantee is about: a delayed
	// grab already pending, under the same field manager. A blank object
	// could not observe a release.
	grabAt := metav1.NewTime(time.Now().Add(time.Hour).Truncate(time.Second))
	_, err := k8s.PatchStatus(ctx, f.mgr, k8s.ManagerCatalogarrGrab,
		catalogac.Movie("the-matrix", f.ns).WithStatus(
			catalogac.MovieStatus().
				WithActiveDownloadRef("the-matrix-abc1234567").
				WithPendingGrab(catalogac.PendingGrab().
					WithGrabAt(grabAt).
					WithProtocol(commonv1.ProtocolTorrent).
					WithReleaseTitle("The.Matrix.1999.2160p.UHD.BluRay.x265-BEST"))))
	require.NoError(t, err)

	before := &catalogv1alpha1.Movie{}
	require.NoError(t, f.api.Get(ctx, client.ObjectKey{Namespace: f.ns, Name: "the-matrix"}, before))
	require.NotNil(t, before.Status.ActiveDownloadRef)
	require.NotNil(t, before.Status.PendingGrab)

	eventually(t, 10*time.Second, "the cache to see the pending grab", func() bool {
		var m catalogv1alpha1.Movie
		if err := f.mgr.Get(ctx, client.ObjectKey{Namespace: f.ns, Name: "the-matrix"}, &m); err != nil {
			return false
		}
		return m.Status.PendingGrab != nil
	})

	require.NoError(t, f.worker.Handle(ctx, testMessage{env: f.envelope(t, schema.SearchTask{
		MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "the-matrix"},
		Reason:   schema.SearchReasonCutoffUnmet,
	})}))

	after := &catalogv1alpha1.Movie{}
	require.NoError(t, f.api.Get(ctx, client.ObjectKey{Namespace: f.ns, Name: "the-matrix"}, after))

	require.NotNil(t, after.Status.ActiveDownloadRef,
		"recording a search attempt must not release the grab path's activeDownloadRef")
	require.Equal(t, "the-matrix-abc1234567", *after.Status.ActiveDownloadRef)
	require.NotNil(t, after.Status.PendingGrab,
		"recording a search attempt must not release a delayed item's pendingGrab")
	require.Equal(t, grabAt.UTC(), after.Status.PendingGrab.GrabAt.UTC())
	require.Equal(t, commonv1.ProtocolTorrent, after.Status.PendingGrab.Protocol)
	require.Equal(t, "The.Matrix.1999.2160p.UHD.BluRay.x265-BEST", after.Status.PendingGrab.ReleaseTitle)

	// And the attempt really was recorded, so this is not passing by doing
	// nothing at all.
	require.NotNil(t, after.Status.LastSearchedAt)
	require.Equal(t, int32(1), after.Status.SearchAttempts.Count)
}

// TestWorkerRecordingAnAttemptIsNotFatal: a search that succeeded must not be
// re-run because a timestamp could not be written. The target is deleted
// between the snapshot and the stamp, which grab.RecordSearchAttempt treats as
// a no-op -- and even if it did not, Handle must still ack.
func TestWorkerRecordingAnAttemptIsNotFatal(t *testing.T) {
	ctx := context.Background()
	f := newWorkerFixture(t, "worker-attempt-nonfatal")

	env := f.envelope(t, schema.SearchTask{
		MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "the-matrix"},
		Reason:   schema.SearchReasonMissing,
	})
	require.NoError(t, f.mgr.Delete(ctx, &catalogv1alpha1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: "the-matrix", Namespace: f.ns},
	}))
	// The snapshot still reads the cached copy, so the search itself runs; the
	// stamp lands on an object that is already gone.
	err := f.worker.Handle(ctx, testMessage{env: env, attempt: 1})
	if err != nil {
		// If the cache already caught up the task is retried instead, which is
		// the documented cache-warm path and equally acceptable here. What
		// must never happen is a Discard, which would throw away a search that
		// actually ran.
		var de *events.DiscardError
		require.NotErrorAs(t, err, &de)
	}
	_, _, _, deliveries := f.sink.last()
	require.LessOrEqual(t, deliveries, 1)
}
