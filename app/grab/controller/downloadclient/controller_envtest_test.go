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

package downloadclient_test

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	appsv1ac "k8s.io/client-go/applyconfigurations/apps/v1"
	corev1ac "k8s.io/client-go/applyconfigurations/core/v1"
	metav1ac "k8s.io/client-go/applyconfigurations/meta/v1"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/app/grab/controller/downloadclient"
	cevents "github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/records"
	"github.com/mediactl/clustarr/pkg/records/agentrecords"
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
	if err != nil {
		t.Fatalf("start envtest: %v", err)
	}
	t.Cleanup(func() {
		if err := env.Stop(); err != nil {
			t.Errorf("stop envtest: %v", err)
		}
	})
	c, err := client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
	if err != nil {
		t.Fatalf("build client: %v", err)
	}
	return c
}

// engineFreeBytes points r at a clustarr-engines bucket holding a fresh,
// ready record for each of dc's ordinals reporting free bytes: DiskSpaceOK
// reads the engines' own free bytes from their records (ADR-0019 §6.7), not
// a filesystem probe in the controller pod.
func engineFreeBytes(t *testing.T, r *downloadclient.Reconciler, dc *downloadv1alpha1.DownloadClient, free int64) {
	t.Helper()
	ctx := context.Background()
	bus := membus.New(clockwork.NewRealClock())
	require.NoError(t, bus.Ensure(ctx, cevents.Default()))
	kv := bus.KV(cevents.BucketEngines)
	w := records.NewWriter(kv, agentrecords.Engines(), "engine-test", "test")
	item := schema.ItemRef{Kind: "DownloadClient", Ref: schema.Ref{Namespace: dc.Namespace, Name: dc.Name, UID: string(dc.UID)}}
	for o := range max(dc.Spec.Replicas, 1) {
		_, err := w.Write(ctx, agentrecords.EngineKey(string(dc.UID), o), &schema.EngineRecord{
			RecordHeader: schema.RecordHeader{Item: &item, Sub: strconv.Itoa(int(o)), Seq: 1, State: schema.TransferStatePresent},
			Client:       dc.Name, Ordinal: o, Ready: true, Reattached: true,
			PublishFreeBytes: free, At: time.Now().UTC(),
		})
		require.NoError(t, err)
	}
	r.Engines = records.NewReader(kv, agentrecords.Engines())
}

func reconcileOK(t *testing.T, r *downloadclient.Reconciler, ns, name string) {
	t.Helper()
	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: name}})
	require.NoError(t, err)
}

func TestReconcileTorrentClientCreatesStatefulSet(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)

	dc := &downloadv1alpha1.DownloadClient{
		ObjectMeta: metav1.ObjectMeta{Name: "qbit", Namespace: "default"},
		Spec: downloadv1alpha1.DownloadClientSpec{
			Protocol: commonv1alpha1.ProtocolTorrent,
			Replicas: 2,
			Torrent:  &downloadv1alpha1.TorrentSpec{},
		},
	}
	require.NoError(t, c.Create(ctx, dc))

	r := downloadclient.NewReconciler(c, events.NewFakeRecorder(10), "/data", "/scratch", "ghcr.io/x/engine:dev")
	engineFreeBytes(t, r, dc, 50<<30)
	reconcileOK(t, r, "default", "qbit")

	var sts appsv1.StatefulSet
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "qbit-engine"}, &sts))
	require.Len(t, sts.OwnerReferences, 1)
	require.Equal(t, "qbit", sts.OwnerReferences[0].Name)
	require.Equal(t, int32(2), *sts.Spec.Replicas)
	require.Len(t, sts.Spec.Template.Spec.Containers, 1)
	require.Equal(t, "ghcr.io/x/engine:dev", sts.Spec.Template.Spec.Containers[0].Image)

	var got downloadv1alpha1.DownloadClient
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "qbit"}, &got))
	require.NotNil(t, got.Status.Engine)
	require.Equal(t, "qbit-engine", got.Status.Engine.WorkloadRef)
	require.True(t, k8s.IsConditionTrue(got.Status.Conditions, downloadv1alpha1.DownloadClientConditionDiskSpaceOK))
	// The StatefulSet controller does not run under envtest (no kubelet, no
	// StatefulSet controller-manager), so ReadyReplicas never advances past 0
	// -- EngineReady is therefore False here, which is the correct, honest
	// reading of "the workload exists but nothing has reported ready".
	require.False(t, k8s.IsConditionTrue(got.Status.Conditions, downloadv1alpha1.DownloadClientConditionEngineReady))
	require.False(t, k8s.IsConditionTrue(got.Status.Conditions, k8s.ConditionReady))
}

func TestReconcileUsenetClientCreatesDeployment(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)

	dc := &downloadv1alpha1.DownloadClient{
		ObjectMeta: metav1.ObjectMeta{Name: "sab", Namespace: "default"},
		Spec: downloadv1alpha1.DownloadClientSpec{
			Protocol: commonv1alpha1.ProtocolUsenet,
			Replicas: 1,
			Usenet: &downloadv1alpha1.UsenetSpec{
				Providers: []downloadv1alpha1.NNTPProvider{{
					Name: "primary", Host: "news.example.com",
					SecretRef: corev1.LocalObjectReference{Name: "nntp-creds"},
				}},
			},
		},
	}
	require.NoError(t, c.Create(ctx, dc))

	r := downloadclient.NewReconciler(c, events.NewFakeRecorder(10), "/data", "/scratch", "ghcr.io/x/engine:dev")
	engineFreeBytes(t, r, dc, 50<<30)
	reconcileOK(t, r, "default", "sab")

	var dep appsv1.Deployment
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "sab-engine"}, &dep))
	require.Equal(t, int32(1), *dep.Spec.Replicas)
	var volumes []string
	for _, v := range dep.Spec.Template.Spec.Volumes {
		volumes = append(volumes, v.Name)
	}
	require.Equal(t, []string{"data", "scratch", "tmp"}, volumes)

	var got downloadv1alpha1.DownloadClient
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "sab"}, &got))
	require.NotNil(t, got.Status.Engine)
	require.Equal(t, "sab-engine", got.Status.Engine.WorkloadRef)
}

// TestAnExistingEngineWorkloadGainsThePodSecurity starts from an engine
// StatefulSet already running in the pre-X16 shape -- applied by the same
// field manager, with no security context and no /tmp -- because that is
// what every cluster upgrading into X16 has. The next reconcile must bring it
// to the secured shape (a manager's apply replaces its own set, so nothing
// of the old template survives), and the apiserver must accept that shape.
func TestAnExistingEngineWorkloadGainsThePodSecurity(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)

	dc := &downloadv1alpha1.DownloadClient{
		ObjectMeta: metav1.ObjectMeta{Name: "legacy", Namespace: "default"},
		Spec: downloadv1alpha1.DownloadClientSpec{
			Protocol: commonv1alpha1.ProtocolTorrent,
			Replicas: 1,
			Torrent:  &downloadv1alpha1.TorrentSpec{},
		},
	}
	require.NoError(t, c.Create(ctx, dc))

	labels := map[string]string{"app.kubernetes.io/component": "grab-engine", "download.clustarr.io/client": "legacy"}
	old := appsv1ac.StatefulSet("legacy-engine", "default").
		WithLabels(labels).
		WithSpec(appsv1ac.StatefulSetSpec().
			WithReplicas(1).
			WithSelector(metav1ac.LabelSelector().WithMatchLabels(labels)).
			WithTemplate(corev1ac.PodTemplateSpec().WithLabels(labels).WithSpec(corev1ac.PodSpec().
				WithContainers(corev1ac.Container().WithName("engine").WithImage("img").
					WithVolumeMounts(corev1ac.VolumeMount().WithName("data").WithMountPath("/data"))).
				WithVolumes(corev1ac.Volume().WithName("data").WithPersistentVolumeClaim(
					corev1ac.PersistentVolumeClaimVolumeSource().WithClaimName("clustarr-data"))))))
	_, err := k8s.Apply(ctx, c, k8s.ManagerGrab, old)
	require.NoError(t, err)

	r := downloadclient.NewReconciler(c, events.NewFakeRecorder(10), "/data", "/scratch", "img")
	engineFreeBytes(t, r, dc, 50<<30)
	reconcileOK(t, r, "default", "legacy")

	var sts appsv1.StatefulSet
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "legacy-engine"}, &sts))
	pod := sts.Spec.Template.Spec
	psc := pod.SecurityContext
	require.NotNil(t, psc)
	require.NotNil(t, psc.RunAsNonRoot)
	require.True(t, *psc.RunAsNonRoot)
	require.NotNil(t, psc.RunAsUser)
	require.Equal(t, int64(1000), *psc.RunAsUser)
	require.NotNil(t, psc.RunAsGroup)
	require.Equal(t, int64(1000), *psc.RunAsGroup)
	require.NotNil(t, psc.FSGroup)
	require.Equal(t, int64(1000), *psc.FSGroup)
	require.NotNil(t, psc.FSGroupChangePolicy)
	require.Equal(t, corev1.FSGroupChangeOnRootMismatch, *psc.FSGroupChangePolicy)
	require.NotNil(t, psc.SeccompProfile)
	require.Equal(t, corev1.SeccompProfileTypeRuntimeDefault, psc.SeccompProfile.Type)

	require.Len(t, pod.Containers, 1)
	csc := pod.Containers[0].SecurityContext
	require.NotNil(t, csc)
	require.NotNil(t, csc.ReadOnlyRootFilesystem)
	require.True(t, *csc.ReadOnlyRootFilesystem)
	require.NotNil(t, csc.AllowPrivilegeEscalation)
	require.False(t, *csc.AllowPrivilegeEscalation)
	require.NotNil(t, csc.Capabilities)
	require.Equal(t, []corev1.Capability{"ALL"}, csc.Capabilities.Drop)

	var tmp bool
	for _, m := range pod.Containers[0].VolumeMounts {
		tmp = tmp || m.MountPath == "/tmp"
	}
	require.True(t, tmp, "the read-only root filesystem needs /tmp as a volume")
}

func TestReconcileBelowMinFreeIsNotReady(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)

	dc := &downloadv1alpha1.DownloadClient{
		ObjectMeta: metav1.ObjectMeta{Name: "full", Namespace: "default"},
		Spec: downloadv1alpha1.DownloadClientSpec{
			Protocol: commonv1alpha1.ProtocolTorrent,
			Replicas: 1,
			Torrent:  &downloadv1alpha1.TorrentSpec{},
		},
	}
	require.NoError(t, c.Create(ctx, dc))

	r := downloadclient.NewReconciler(c, events.NewFakeRecorder(10), "/data", "/scratch", "img")
	r.MinFreeBytes = 10 << 30
	engineFreeBytes(t, r, dc, 1<<30)
	reconcileOK(t, r, "default", "full")

	var got downloadv1alpha1.DownloadClient
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "full"}, &got))
	require.False(t, k8s.IsConditionTrue(got.Status.Conditions, downloadv1alpha1.DownloadClientConditionDiskSpaceOK))
	require.False(t, k8s.IsConditionTrue(got.Status.Conditions, k8s.ConditionReady))
	require.Equal(t, int64(1<<30), got.Status.FreeBytes)
}

func strPtr(s string) *string { return &s }
