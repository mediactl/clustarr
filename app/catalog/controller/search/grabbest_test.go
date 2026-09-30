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

package search

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

func release(guid string, approved bool, rejections ...commonv1.Rejection) commonv1.ReleaseDecision {
	return commonv1.ReleaseDecision{
		ReleaseInfo: commonv1.ReleaseInfo{
			Protocol: commonv1.ProtocolTorrent, GUID: guid, Title: guid,
			DownloadURL: "https://tracker.example/" + guid + ".torrent",
		},
		Approved:   approved,
		Rejections: rejections,
	}
}

func fakeReconciler(t *testing.T, objs ...client.Object) (*Reconciler, client.Client) {
	t.Helper()
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(objs...).
		WithStatusSubresource(&catalogv1alpha1.Search{}).Build()
	return &Reconciler{Client: c, Scheme: c.Scheme()}, c
}

func completedBookSearch(results ...commonv1.ReleaseDecision) *catalogv1alpha1.Search {
	finished := metav1.NewTime(time.Now())
	return &catalogv1alpha1.Search{
		ObjectMeta: metav1.ObjectMeta{Name: "the-idiot-x", Namespace: "media"},
		Spec: catalogv1alpha1.SearchSpec{
			MediaRef: &commonv1.MediaRef{Kind: commonv1.MediaKindBook, Name: "the-idiot"},
			GrabBest: true,
		},
		Status: catalogv1alpha1.SearchStatus{
			Phase: catalogv1alpha1.SearchPhaseCompleted, StartedAt: &finished, FinishedAt: &finished, Results: results,
		},
	}
}

func reconcileSearch(t *testing.T, r *Reconciler, name string) {
	t.Helper()
	_, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "media", Name: name}})
	require.NoError(t, err)
}

// TestGrabBestGrabsTheTopApprovedReleaseOnce: with grabBest a completed
// Search grabs its best approved release as the automatic search would --
// grabbedBy search, not manual -- and a second reconcile grabs nothing more.
func TestGrabBestGrabsTheTopApprovedReleaseOnce(t *testing.T) {
	r, c := fakeReconciler(t, completedBookSearch(
		release("rejected-first", false, commonv1.Rejection{Reason: "WrongItem", Type: commonv1.RejectionPermanent}),
		release("best", true), release("second", true)))

	reconcileSearch(t, r, "the-idiot-x")
	reconcileSearch(t, r, "the-idiot-x")

	var dls downloadv1alpha1.DownloadList
	require.NoError(t, c.List(context.Background(), &dls))
	require.Len(t, dls.Items, 1)
	dl := dls.Items[0]
	require.Equal(t, "best", dl.Spec.Release.GUID)
	require.Equal(t, downloadv1alpha1.GrabSourceSearch, dl.Spec.GrabbedBy)
	require.False(t, dl.Spec.Manual, "an automatic pick is not a person's: the importer keeps its upgrade rules")

	var s catalogv1alpha1.Search
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: "media", Name: "the-idiot-x"}, &s))
	require.Len(t, s.Status.Grabbed, 1)
	require.Equal(t, dl.Name, s.Status.Grabbed[0].DownloadRef)
}

// TestGrabBestNeverGrabsARejectedRelease: a temporary rejection ("already
// in the queue") is a human's call on an interactive Search, never an
// automatic grab's.
func TestGrabBestNeverGrabsARejectedRelease(t *testing.T) {
	r, c := fakeReconciler(t, completedBookSearch(
		release("queued", false, commonv1.Rejection{Reason: "AlreadyQueued", Type: commonv1.RejectionTemporary})))
	reconcileSearch(t, r, "the-idiot-x")
	var dls downloadv1alpha1.DownloadList
	require.NoError(t, c.List(context.Background(), &dls))
	require.Empty(t, dls.Items)
}
