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

package subtitleprofile_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	subtitleac "github.com/mediactl/clustarr/api/applyconfiguration/subtitle/subtitle/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	"github.com/mediactl/clustarr/app/caption/controller/subtitleprofile"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// requestWrites counts every write a client makes to a SubtitleRequest.
type requestWrites struct {
	client.Client
	n atomic.Int32
}

func (c *requestWrites) count(o any) {
	switch o.(type) {
	case *subtitlev1alpha1.SubtitleRequest, *subtitleac.SubtitleRequestApplyConfiguration:
		c.n.Add(1)
	}
}

func (c *requestWrites) Apply(ctx context.Context, ac runtime.ApplyConfiguration, opts ...client.ApplyOption) error {
	c.count(ac)
	return c.Client.Apply(ctx, ac, opts...)
}

func (c *requestWrites) Create(ctx context.Context, o client.Object, opts ...client.CreateOption) error {
	c.count(o)
	return c.Client.Create(ctx, o, opts...)
}

func (c *requestWrites) Update(ctx context.Context, o client.Object, opts ...client.UpdateOption) error {
	c.count(o)
	return c.Client.Update(ctx, o, opts...)
}

func (c *requestWrites) Patch(ctx context.Context, o client.Object, p client.Patch, opts ...client.PatchOption) error {
	c.count(o)
	return c.Client.Patch(ctx, o, p, opts...)
}

func (c *requestWrites) Delete(ctx context.Context, o client.Object, opts ...client.DeleteOption) error {
	c.count(o)
	return c.Client.Delete(ctx, o, opts...)
}

// TestReconcileOverAnUnchangedLibraryWritesNoRequest: a request that
// already carries exactly this manager's declaration -- its MediaFile's
// owner reference, mediaFileRef and the winning profileRef -- is not
// applied again. Every profile reconcile used to apply one per probed file
// (about ten thousand on the owner's library), each a no-op.
func TestReconcileOverAnUnchangedLibraryWritesNoRequest(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	const ns = "subtitleprofile-unchanged"
	require.NoError(t, client.IgnoreAlreadyExists(c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})))

	movieFile(t, ctx, c, ns, "arrival-2016", nil)
	movieFile(t, ctx, c, ns, "sicario-2015", nil)
	sp := defaultProfile(t, ctx, c, "default")

	wc := &requestWrites{Client: c}
	r := subtitleprofile.NewReconciler(wc, k8s.MustNewScheme(), events.NewFakeRecorder(10))
	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: sp.Name}}

	_, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	require.Equal(t, int32(2), wc.n.Load(), "setup: the first reconcile ensures both requests")
	before := listRequests(t, ctx, c)
	require.Len(t, before, 2)

	wc.n.Store(0)
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Zero(t, wc.n.Load(), "a reconcile over an unchanged library wrote a SubtitleRequest")
	after := listRequests(t, ctx, c)
	require.Len(t, after, 2)
	for i := range before {
		assert.Equal(t, before[i].ResourceVersion, after[i].ResourceVersion, "%s changed", before[i].Name)
	}
}

// TestReconcileReappliesARequestThatDrifted: the skip compares against the
// object, so a request whose profileRef was changed under it, or whose
// MediaFile was replaced, is applied again, as every reconcile used to.
func TestReconcileReappliesARequestThatDrifted(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	const ns = "subtitleprofile-drift"
	require.NoError(t, client.IgnoreAlreadyExists(c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})))

	mf := movieFile(t, ctx, c, ns, "arrival-2016", nil)
	other := movieFile(t, ctx, c, ns, "sicario-2015", nil)
	sp := defaultProfile(t, ctx, c, "default")
	r := subtitleprofile.NewReconciler(c, k8s.MustNewScheme(), events.NewFakeRecorder(10))
	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: sp.Name}}
	_, err := r.Reconcile(ctx, req)
	require.NoError(t, err)

	// Another writer moves the one request's profileRef, and the other's
	// MediaFile is deleted and imported again under the same name, so its
	// request names an owner UID that is gone (envtest runs no garbage
	// collector to delete the request with it).
	var sr subtitlev1alpha1.SubtitleRequest
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(mf), &sr))
	require.NoError(t, c.Patch(ctx, &sr, client.RawPatch(types.MergePatchType, []byte(`{"spec":{"profileRef":"elsewhere"}}`)),
		client.FieldOwner("someone-else")))
	require.NoError(t, c.Delete(ctx, other))
	again := &catalogv1alpha1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Name: other.Name, Namespace: ns},
		Spec:       other.Spec,
	}
	require.NoError(t, c.Create(ctx, again))
	_, err = k8s.PatchStatus(ctx, c, k8s.ManagerCatalog, catalogac.MediaFile(again.Name, ns).WithStatus(
		catalogac.MediaFileStatus().WithProbeHash("p-again").WithMediaInfo(*other.Status.MediaInfo)))
	require.NoError(t, err)
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(again), again))
	require.NotEqual(t, other.UID, again.UID)

	wc := &requestWrites{Client: c}
	r.Client = wc
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, int32(2), wc.n.Load(), "both drifted requests are applied again")
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(mf), &sr))
	assert.Equal(t, sp.Name, sr.Spec.ProfileRef, "the drifted profileRef was not put back")
	var sr2 subtitlev1alpha1.SubtitleRequest
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(again), &sr2))
	require.Len(t, sr2.OwnerReferences, 1, "the gone owner was not released")
	assert.Equal(t, again.UID, sr2.OwnerReferences[0].UID, "the request was not handed to the new MediaFile")
}
