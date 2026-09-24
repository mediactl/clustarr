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

package download_test

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	downloadac "github.com/mediactl/clustarr/api/applyconfiguration/download/download/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	downloadctl "github.com/mediactl/clustarr/app/grab/controller/download"
	"github.com/mediactl/clustarr/pkg/k8s"
)

func newTestClient(t *testing.T) client.Client {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{"../../../../config/crd/bases"},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, env.Stop()) })

	c, err := client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
	require.NoError(t, err)
	return c
}

func reconcileOK(t *testing.T, r *downloadctl.Reconciler, ns, name string) reconcile.Result {
	t.Helper()
	res, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: name}})
	require.NoError(t, err)
	return res
}

// newTorrentDownload builds a torrent-shaped Download: every field the
// "release identity is immutable" CEL rule compares is present, matching
// every other torrent fixture in the tree.
func newTorrentDownload(t *testing.T, ctx context.Context, c client.Client, ns, name, guid string) *downloadv1alpha1.Download {
	t.Helper()
	magnet := "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567"
	dl := &downloadv1alpha1.Download{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: downloadv1alpha1.DownloadSpec{
			Protocol: commonv1.ProtocolTorrent,
			Source:   downloadv1alpha1.DownloadSource{MagnetURL: &magnet},
			Release: commonv1.ReleaseInfo{
				GUID: guid, IndexerRef: "example", IndexerName: "Example",
				Title: "Arrival.2016.1080p", Protocol: commonv1.ProtocolTorrent,
				InfoHash: "0123456789abcdef0123456789abcdef01234567",
			},
			Target: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "arrival"},
		},
	}
	require.NoError(t, c.Create(ctx, dl))
	return dl
}

// newUsenetDownload builds a usenet-shaped Download with NO infoHash --
// context note 2: nothing else in this package's fixtures covers that shape,
// and it is the one the now-fixed "release identity is immutable" CEL rule
// used to reject on every write after creation.
func newUsenetDownload(t *testing.T, ctx context.Context, c client.Client, ns, name, guid string) *downloadv1alpha1.Download {
	t.Helper()
	nzb := "https://fixture.invalid/" + name + ".nzb"
	dl := &downloadv1alpha1.Download{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: downloadv1alpha1.DownloadSpec{
			Protocol: commonv1.ProtocolUsenet,
			Source:   downloadv1alpha1.DownloadSource{NZBURL: &nzb},
			Release: commonv1.ReleaseInfo{
				GUID: guid, IndexerRef: "example", IndexerName: "Example",
				Title: "Arrival.2016.1080p", Protocol: commonv1.ProtocolUsenet,
				// InfoHash deliberately left unset: usenet releases have none.
			},
			Target: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "arrival"},
		},
	}
	require.NoError(t, c.Create(ctx, dl))
	return dl
}

func newTorrentClient(t *testing.T, ctx context.Context, c client.Client, ns, name string, priority, replicas int32) *downloadv1alpha1.DownloadClient {
	t.Helper()
	dc := &downloadv1alpha1.DownloadClient{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: downloadv1alpha1.DownloadClientSpec{
			Protocol: commonv1.ProtocolTorrent,
			Enabled:  ptr.To(true),
			Priority: priority,
			Replicas: replicas,
			Torrent:  &downloadv1alpha1.TorrentSpec{},
		},
	}
	require.NoError(t, c.Create(ctx, dc))
	return dc
}

func newUsenetClient(t *testing.T, ctx context.Context, c client.Client, ns, name string) *downloadv1alpha1.DownloadClient {
	t.Helper()
	dc := &downloadv1alpha1.DownloadClient{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: downloadv1alpha1.DownloadClientSpec{
			Protocol: commonv1.ProtocolUsenet,
			Enabled:  ptr.To(true),
			Priority: 1,
			Replicas: 1,
			Usenet: &downloadv1alpha1.UsenetSpec{
				Providers: []downloadv1alpha1.NNTPProvider{{
					Name: "primary", Host: "news.fixture.invalid",
					SecretRef: corev1.LocalObjectReference{Name: "nntp-creds"},
				}},
			},
		},
	}
	require.NoError(t, c.Create(ctx, dc))
	return dc
}

// markEngineReady patches dc's EngineReady condition directly under
// k8s.ManagerGrabarr, standing in for what grabarr/controller/downloadclient's
// own reconciler (D2-3, already landed) would have written.
func markEngineReady(t *testing.T, ctx context.Context, c client.Client, ns, name string, ready bool) {
	t.Helper()
	status := metav1.ConditionFalse
	if ready {
		status = metav1.ConditionTrue
	}
	cond := k8s.ConditionAC(metav1.Condition{
		Type: downloadv1alpha1.DownloadClientConditionEngineReady, Status: status,
		Reason: "Test", Message: "test fixture",
	})
	obj := downloadac.DownloadClient(name, ns).WithStatus(downloadac.DownloadClientStatus().WithConditions(cond))
	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerGrabarr, obj)
	require.NoError(t, err)
}

func getDownload(t *testing.T, ctx context.Context, c client.Client, ns, name string) *downloadv1alpha1.Download {
	t.Helper()
	var dl downloadv1alpha1.Download
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &dl))
	return &dl
}

func TestReconcileAssignsTorrentDownloadToLowestPriorityEnabledClient(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)

	newTorrentClient(t, ctx, c, "default", "qbit-high", 5, 1)
	low := newTorrentClient(t, ctx, c, "default", "qbit-low", 1, 3)
	markEngineReady(t, ctx, c, "default", "qbit-high", true)
	markEngineReady(t, ctx, c, "default", "qbit-low", true)

	dl := newTorrentDownload(t, ctx, c, "default", "torrent-dl", "guid-torrent-1")

	r := downloadctl.NewReconciler(c, events.NewFakeRecorder(10), t.TempDir())
	reconcileOK(t, r, "default", dl.Name)

	got := getDownload(t, ctx, c, "default", dl.Name)
	assert.Equal(t, low.Name, got.Spec.ClientRef, "the lower priority number must win")
	assert.NotEmpty(t, got.Status.Engine)
	assert.Equal(t, downloadv1alpha1.DownloadPhaseAssigned, got.Status.Phase)
	assert.True(t, k8s.IsConditionTrue(got.Status.Conditions, downloadv1alpha1.DownloadConditionAssigned))
	assert.Equal(t, low.Name, got.Labels[downloadv1alpha1.LabelClient])
	assert.Equal(t, got.Status.Engine, got.Labels[downloadv1alpha1.LabelEngine])
}

func TestReconcileAssignsUsenetDownloadWithNoInfoHash(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)

	dc := newUsenetClient(t, ctx, c, "default", "sabnzbd")
	markEngineReady(t, ctx, c, "default", dc.Name, true)

	dl := newUsenetDownload(t, ctx, c, "default", "usenet-dl", "guid-usenet-1")
	require.Empty(t, dl.Spec.Release.InfoHash, "fixture must leave infoHash unset -- this is the shape the now-fixed CEL rule used to reject")

	r := downloadctl.NewReconciler(c, events.NewFakeRecorder(10), t.TempDir())
	reconcileOK(t, r, "default", dl.Name)

	got := getDownload(t, ctx, c, "default", dl.Name)
	assert.Equal(t, dc.Name, got.Spec.ClientRef)
	assert.Equal(t, dc.Name+"-0", got.Status.Engine, "usenet clients are pinned to spec.replicas==1, so the only ordinal is 0")
	assert.Equal(t, downloadv1alpha1.DownloadPhaseAssigned, got.Status.Phase)
}

func TestEnginePinSurvivesAReconcileThatWouldOtherwisePickDifferently(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)

	newTorrentClient(t, ctx, c, "default", "qbit-a", 1, 1)
	markEngineReady(t, ctx, c, "default", "qbit-a", true)

	dl := newTorrentDownload(t, ctx, c, "default", "pin-dl", "guid-pin-1")
	r := downloadctl.NewReconciler(c, events.NewFakeRecorder(10), t.TempDir())
	reconcileOK(t, r, "default", dl.Name)

	before := getDownload(t, ctx, c, "default", dl.Name)
	require.Equal(t, "qbit-a", before.Spec.ClientRef)
	require.NotEmpty(t, before.Status.Engine)
	pinnedEngine := before.Status.Engine

	// A client that would win pickClient outright if it ran again: priority 0
	// beats qbit-a's priority 1.
	newTorrentClient(t, ctx, c, "default", "qbit-would-win", 0, 1)
	markEngineReady(t, ctx, c, "default", "qbit-would-win", true)

	reconcileOK(t, r, "default", dl.Name)

	after := getDownload(t, ctx, c, "default", dl.Name)
	assert.Equal(t, "qbit-a", after.Spec.ClientRef, "clientRef must not silently migrate once set")
	assert.Equal(t, pinnedEngine, after.Status.Engine, "status.engine must not silently migrate once pinned")
}

func TestEnginePinSurvivesItsDownloadClientDisappearing(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)

	newTorrentClient(t, ctx, c, "default", "qbit-gone", 1, 1)
	markEngineReady(t, ctx, c, "default", "qbit-gone", true)

	dl := newTorrentDownload(t, ctx, c, "default", "vanish-dl", "guid-vanish-1")
	r := downloadctl.NewReconciler(c, events.NewFakeRecorder(10), t.TempDir())
	reconcileOK(t, r, "default", dl.Name)

	before := getDownload(t, ctx, c, "default", dl.Name)
	require.NotEmpty(t, before.Status.Engine)
	pinnedEngine := before.Status.Engine

	require.NoError(t, c.Delete(ctx, &downloadv1alpha1.DownloadClient{
		ObjectMeta: metav1.ObjectMeta{Name: "qbit-gone", Namespace: "default"},
	}))

	reconcileOK(t, r, "default", dl.Name)

	after := getDownload(t, ctx, c, "default", dl.Name)
	assert.Equal(t, pinnedEngine, after.Status.Engine, "status.engine must survive its DownloadClient being deleted")
	assert.Equal(t, downloadv1alpha1.DownloadPhaseAssigned, after.Status.Phase)
}

func TestReconcileWithNoEnabledClientStaysPending(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)

	dl := newTorrentDownload(t, ctx, c, "default", "no-client-dl", "guid-none-1")
	r := downloadctl.NewReconciler(c, events.NewFakeRecorder(10), t.TempDir())
	res := reconcileOK(t, r, "default", dl.Name)

	assert.Positive(t, res.RequeueAfter)
	got := getDownload(t, ctx, c, "default", dl.Name)
	assert.Equal(t, downloadv1alpha1.DownloadPhasePending, got.Status.Phase)
	assert.Empty(t, got.Status.Engine)
	assert.Empty(t, got.Spec.ClientRef)
	cond := k8s.FindCondition(got.Status.Conditions, downloadv1alpha1.DownloadConditionAssigned)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, downloadctl.ReasonNoEnabledClient, cond.Reason)
}

func TestReconcileWaitsForEngineReady(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)

	newTorrentClient(t, ctx, c, "default", "qbit-notready", 1, 1)
	// EngineReady deliberately left unset (not True).

	dl := newTorrentDownload(t, ctx, c, "default", "wait-dl", "guid-wait-1")
	r := downloadctl.NewReconciler(c, events.NewFakeRecorder(10), t.TempDir())
	res := reconcileOK(t, r, "default", dl.Name)

	assert.Positive(t, res.RequeueAfter)
	got := getDownload(t, ctx, c, "default", dl.Name)
	assert.Equal(t, "qbit-notready", got.Spec.ClientRef, "clientRef is picked even while waiting for the engine")
	assert.Empty(t, got.Status.Engine, "R4: an engine must not be handed work before it reports ready")
	assert.Equal(t, downloadv1alpha1.DownloadPhasePending, got.Status.Phase)
	cond := k8s.FindCondition(got.Status.Conditions, downloadv1alpha1.DownloadConditionAssigned)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, downloadctl.ReasonEngineNotReady, cond.Reason)

	// Now flip EngineReady and reconcile again: the pin must complete.
	markEngineReady(t, ctx, c, "default", "qbit-notready", true)
	reconcileOK(t, r, "default", dl.Name)
	got = getDownload(t, ctx, c, "default", dl.Name)
	assert.Equal(t, "qbit-notready-0", got.Status.Engine)
	assert.Equal(t, downloadv1alpha1.DownloadPhaseAssigned, got.Status.Phase)
}

func TestReconcileWithMissingClientRefKeepsWaiting(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)

	dc := newTorrentClient(t, ctx, c, "default", "qbit-temp", 1, 1)
	// EngineReady deliberately left unset: the client is picked (clientRef
	// gets set) but the pin must wait, so the "before" snapshot below is
	// genuinely pre-pin rather than already assigned.

	dl := newTorrentDownload(t, ctx, c, "default", "clientref-missing-dl", "guid-missing-1")
	r := downloadctl.NewReconciler(c, events.NewFakeRecorder(10), t.TempDir())
	reconcileOK(t, r, "default", dl.Name)

	before := getDownload(t, ctx, c, "default", dl.Name)
	require.Equal(t, dc.Name, before.Spec.ClientRef)
	require.Empty(t, before.Status.Engine, "not pinned yet in this scenario")

	require.NoError(t, c.Delete(ctx, dc))

	res := reconcileOK(t, r, "default", dl.Name)
	assert.Positive(t, res.RequeueAfter)
	after := getDownload(t, ctx, c, "default", dl.Name)
	assert.Empty(t, after.Status.Engine)
	assert.Equal(t, downloadv1alpha1.DownloadPhasePending, after.Status.Phase)
	cond := k8s.FindCondition(after.Status.Conditions, downloadv1alpha1.DownloadConditionAssigned)
	require.NotNil(t, cond)
	assert.Equal(t, k8s.ReasonDependencyNotReady, cond.Reason)
}
