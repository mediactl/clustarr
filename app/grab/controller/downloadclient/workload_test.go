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

package downloadclient

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	appsv1ac "k8s.io/client-go/applyconfigurations/apps/v1"
	corev1ac "k8s.io/client-go/applyconfigurations/core/v1"
	metav1ac "k8s.io/client-go/applyconfigurations/meta/v1"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
)

func fakeOwnerRef() *metav1ac.OwnerReferenceApplyConfiguration {
	return metav1ac.OwnerReference().
		WithAPIVersion("download.clustarr.io/v1alpha1").
		WithKind("DownloadClient").
		WithName("sab").
		WithUID("abc-123").
		WithController(true).
		WithBlockOwnerDeletion(true)
}

func torrentClient(name string, replicas int32) *downloadv1alpha1.DownloadClient {
	return &downloadv1alpha1.DownloadClient{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: downloadv1alpha1.DownloadClientSpec{
			Protocol: commonv1alpha1.ProtocolTorrent,
			Replicas: replicas,
			Torrent:  &downloadv1alpha1.TorrentSpec{},
		},
	}
}

func usenetClient(name string) *downloadv1alpha1.DownloadClient {
	return &downloadv1alpha1.DownloadClient{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: downloadv1alpha1.DownloadClientSpec{
			Protocol: commonv1alpha1.ProtocolUsenet,
			Replicas: 1,
			Usenet:   &downloadv1alpha1.UsenetSpec{},
		},
	}
}

func TestEngineWorkloadName(t *testing.T) {
	assert.Equal(t, "sab-engine", engineWorkloadName("sab"))
}

func TestTorrentReplicasFloorsAtOne(t *testing.T) {
	dc := torrentClient("sab", 0)
	assert.Equal(t, int32(1), torrentReplicas(dc))
	dc.Spec.Replicas = 4
	assert.Equal(t, int32(4), torrentReplicas(dc))
}

func TestBuildStatefulSetShape(t *testing.T) {
	dc := torrentClient("sab", 3)
	dc.Spec.Torrent.ListenPort = 51413
	owner := fakeOwnerRef()

	sts := buildStatefulSet(dc, "sab-engine", "ghcr.io/x/engine:dev", "/data", "/scratch", "clustarr-data", EngineRuntime{}, nil, owner)

	require.NotNil(t, sts.Name)
	assert.Equal(t, "sab-engine", *sts.Name)
	require.NotNil(t, sts.Namespace)
	assert.Equal(t, "default", *sts.Namespace)
	require.Len(t, sts.OwnerReferences, 1)
	assert.Equal(t, "sab", *sts.OwnerReferences[0].Name)

	require.NotNil(t, sts.Spec)
	require.NotNil(t, sts.Spec.Replicas)
	assert.Equal(t, int32(3), *sts.Spec.Replicas)

	require.NotNil(t, sts.Spec.Selector)
	assert.Equal(t, map[string]string{labelComponent: componentEngine, labelClient: "sab"}, sts.Spec.Selector.MatchLabels)

	require.NotNil(t, sts.Spec.Template)
	assert.Equal(t, sts.Spec.Selector.MatchLabels, sts.Spec.Template.Labels)

	require.NotNil(t, sts.Spec.Template.Spec)
	require.Len(t, sts.Spec.Template.Spec.Containers, 1)
	c := sts.Spec.Template.Spec.Containers[0]
	assert.Equal(t, "engine", *c.Name)
	assert.Equal(t, "ghcr.io/x/engine:dev", *c.Image)
	require.Equal(t, []string{"/bin/sh", "-c"}, c.Command)
	require.Len(t, c.Args, 1)
	assert.Contains(t, c.Args[0], "ordinal=${HOSTNAME##*-}")
	assert.Contains(t, c.Args[0], "--role torrent-engine")
	assert.Contains(t, c.Args[0], "--data-dir /data")
	assert.Contains(t, c.Args[0], "--engine sab-${ordinal}")

	require.Len(t, c.Ports, 1)
	assert.Equal(t, int32(51413), *c.Ports[0].ContainerPort)

	require.Len(t, c.VolumeMounts, 2)
	assert.Equal(t, dataVolumeName, *c.VolumeMounts[0].Name)
	assert.Equal(t, "/data", *c.VolumeMounts[0].MountPath)
	assert.Equal(t, tmpVolumeName, *c.VolumeMounts[1].Name)

	require.Len(t, sts.Spec.Template.Spec.Volumes, 2)
	require.NotNil(t, sts.Spec.Template.Spec.Volumes[0].PersistentVolumeClaim)
	assert.Equal(t, "clustarr-data", *sts.Spec.Template.Spec.Volumes[0].PersistentVolumeClaim.ClaimName)
}

func TestBuildStatefulSetDefaultsListenPort(t *testing.T) {
	dc := torrentClient("sab", 1)
	sts := buildStatefulSet(dc, "sab-engine", "img", "/data", "/scratch", "clustarr-data", EngineRuntime{}, nil, fakeOwnerRef())
	c := sts.Spec.Template.Spec.Containers[0]
	require.Len(t, c.Ports, 1)
	assert.Equal(t, int32(defaultListenPort), *c.Ports[0].ContainerPort)
}

func TestBuildDeploymentShape(t *testing.T) {
	dc := usenetClient("nzb")
	dep := buildDeployment(dc, "nzb-engine", "img", "/data", "/scratch", "clustarr-data", EngineRuntime{}, nil, fakeOwnerRef())

	require.NotNil(t, dep.Spec.Replicas)
	assert.Equal(t, int32(1), *dep.Spec.Replicas)

	require.Len(t, dep.Spec.Template.Spec.Containers, 1)
	c := dep.Spec.Template.Spec.Containers[0]
	assert.Nil(t, c.Command, "usenet container should let the image entrypoint run")
	assert.Equal(t, []string{
		"grabarr", "--role", "usenet-engine",
		"--data-dir", "/data", "--scratch-dir", "/scratch",
		"--engine", "nzb-0",
	}, c.Args)

	require.Len(t, c.VolumeMounts, 3)
	require.Len(t, dep.Spec.Template.Spec.Volumes, 3)
	assert.Equal(t, scratchVolumeName, *dep.Spec.Template.Spec.Volumes[1].Name)
	require.NotNil(t, dep.Spec.Template.Spec.Volumes[1].EmptyDir)
}

func TestScratchVolumeUsesPVCWhenStorageClassSet(t *testing.T) {
	sc := "fast"
	dc := usenetClient("nzb")
	dc.Spec.Usenet.Scratch = &downloadv1alpha1.ScratchSpec{StorageClassName: &sc}

	vol := scratchVolume(dc)
	require.NotNil(t, vol.PersistentVolumeClaim)
	assert.Equal(t, "nzb-scratch", *vol.PersistentVolumeClaim.ClaimName)
	assert.Nil(t, vol.EmptyDir)
}

func TestScratchVolumeDefaultsToEmptyDir(t *testing.T) {
	dc := usenetClient("nzb")
	vol := scratchVolume(dc)
	require.NotNil(t, vol.EmptyDir)
	require.NotNil(t, vol.EmptyDir.SizeLimit)
	want := resource.MustParse(defaultScratchSize)
	assert.Equal(t, want.String(), vol.EmptyDir.SizeLimit.String())
}

// The owner's cluster keeps /data on NFS and wants the usenet engine to work
// under /data/usenet/incomplete and publish to /data/usenet/complete, so a
// transfer survives a pod restart and the publish is one rename; the
// emptyDir it had before went with the pod, together with a finished 9.4 GB
// transfer (2026-09-24).
func TestScratchPathOnTheDataMountMountsNoScratchVolume(t *testing.T) {
	dc := usenetClient("nzb")
	dc.Spec.Usenet.Scratch = &downloadv1alpha1.ScratchSpec{Path: "/data/usenet/incomplete"}
	dc.Spec.Usenet.PublishDir = "/data/usenet/complete"

	assert.Nil(t, scratchVolume(dc))
	assert.False(t, needsScratchClaim(dc))

	dep := buildDeployment(dc, "nzb-engine", "img", "/data", "/scratch", "clustarr-data", EngineRuntime{}, nil, fakeOwnerRef())
	c := dep.Spec.Template.Spec.Containers[0]
	assert.Equal(t, []string{
		"grabarr", "--role", "usenet-engine",
		"--data-dir", "/data", "--scratch-dir", "/data/usenet/incomplete",
		"--publish-dir", "/data/usenet/complete",
		"--engine", "nzb-0",
	}, c.Args)
	require.Len(t, c.VolumeMounts, 2, "data and /tmp only")
	require.Len(t, dep.Spec.Template.Spec.Volumes, 2)
	for _, v := range dep.Spec.Template.Spec.Volumes {
		assert.NotEqual(t, scratchVolumeName, *v.Name)
	}
}

func TestScratchExistingClaimIsMountedAsIs(t *testing.T) {
	dc := usenetClient("nzb")
	dc.Spec.Usenet.Scratch = &downloadv1alpha1.ScratchSpec{ExistingClaim: "nas-scratch"}
	vol := scratchVolume(dc)
	require.NotNil(t, vol.PersistentVolumeClaim)
	assert.Equal(t, "nas-scratch", *vol.PersistentVolumeClaim.ClaimName)
	assert.False(t, needsScratchClaim(dc), "the controller creates nothing for an existing claim")
}

func TestBuildScratchPVCBindsAVolumeNameWithTheAccessModesAsked(t *testing.T) {
	dc := usenetClient("nzb")
	dc.Spec.Usenet.Scratch = &downloadv1alpha1.ScratchSpec{
		VolumeName:  "synology-scratch-pv",
		AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
		SizeLimit:   resource.MustParse("100Gi"),
	}
	require.True(t, needsScratchClaim(dc))
	pvc := buildScratchPVC(dc, fakeOwnerRef())
	require.NotNil(t, pvc.Spec.VolumeName)
	assert.Equal(t, "synology-scratch-pv", *pvc.Spec.VolumeName)
	assert.Equal(t, []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany}, pvc.Spec.AccessModes)
	require.NotNil(t, pvc.Spec.StorageClassName)
	assert.Empty(t, *pvc.Spec.StorageClassName, "static binding asks for no class, so no provisioner competes")

	vol := scratchVolume(dc)
	require.NotNil(t, vol.PersistentVolumeClaim)
	assert.Equal(t, "nzb-scratch", *vol.PersistentVolumeClaim.ClaimName)
}

func TestValidateEngineDirsRefusesDirectoriesOffTheDataMount(t *testing.T) {
	dc := usenetClient("nzb")
	require.NoError(t, validateEngineDirs(dc, "/data"), "no directories set")

	dc.Spec.Usenet.Scratch = &downloadv1alpha1.ScratchSpec{Path: "/data/usenet/incomplete"}
	dc.Spec.Usenet.PublishDir = "/data/usenet/complete"
	require.NoError(t, validateEngineDirs(dc, "/data"))
	require.NoError(t, validateEngineDirs(dc, "/data/"), "a trailing slash on the mount is not a different mount")

	dc.Spec.Usenet.Scratch.Path = "/data2/usenet/incomplete"
	err := validateEngineDirs(dc, "/data")
	require.Error(t, err, "a sibling that merely shares the prefix is not under the mount")
	assert.Contains(t, err.Error(), "spec.usenet.scratch.path")

	dc.Spec.Usenet.Scratch.Path = "/data/usenet/incomplete"
	dc.Spec.Usenet.PublishDir = "/scratch/out"
	err = validateEngineDirs(dc, "/data")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "spec.usenet.publishDir")
}

func TestBuildScratchPVCUsesStorageClass(t *testing.T) {
	sc := "fast"
	dc := usenetClient("nzb")
	dc.Spec.Usenet.Scratch = &downloadv1alpha1.ScratchSpec{
		StorageClassName: &sc,
		SizeLimit:        resource.MustParse("20Gi"),
	}
	pvc := buildScratchPVC(dc, fakeOwnerRef())
	assert.Equal(t, "nzb-scratch", *pvc.Name)
	require.NotNil(t, pvc.Spec.StorageClassName)
	assert.Equal(t, "fast", *pvc.Spec.StorageClassName)
	require.NotNil(t, pvc.Spec.Resources)
	require.NotNil(t, pvc.Spec.Resources.Requests)
	qty := (*pvc.Spec.Resources.Requests)[corev1.ResourceStorage]
	assert.Equal(t, "20Gi", qty.String())
}

func TestResourceRequirementsACOmitsEmptySides(t *testing.T) {
	ac := resourceRequirementsAC(corev1.ResourceRequirements{})
	assert.Nil(t, ac.Requests)
	assert.Nil(t, ac.Limits)

	rr := corev1.ResourceRequirements{
		Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")},
	}
	ac = resourceRequirementsAC(rr)
	require.NotNil(t, ac.Requests)
	assert.Nil(t, ac.Limits)
}

func TestTolerationsAC(t *testing.T) {
	secs := int64(30)
	in := []corev1.Toleration{{
		Key: "gpu", Operator: corev1.TolerationOpEqual, Value: "true",
		Effect: corev1.TaintEffectNoSchedule, TolerationSeconds: &secs,
	}}
	out := tolerationsAC(in)
	require.Len(t, out, 1)
	assert.Equal(t, "gpu", *out[0].Key)
	assert.Equal(t, corev1.TolerationOpEqual, *out[0].Operator)
	assert.Equal(t, "true", *out[0].Value)
	assert.Equal(t, corev1.TaintEffectNoSchedule, *out[0].Effect)
	require.NotNil(t, out[0].TolerationSeconds)
	assert.Equal(t, int64(30), *out[0].TolerationSeconds)
}

// envOf flattens a container's env into name -> value, with a downward-API
// entry as "fieldRef:<path>".
func envOf(env []corev1ac.EnvVarApplyConfiguration) map[string]string {
	out := map[string]string{}
	for _, e := range env {
		switch {
		case e.ValueFrom != nil && e.ValueFrom.FieldRef != nil:
			out[*e.Name] = "fieldRef:" + *e.ValueFrom.FieldRef.FieldPath
		case e.Value != nil:
			out[*e.Name] = *e.Value
		}
	}
	return out
}

// TestEnginePodsGetTheRuntimeTheyNeed is X14's fix for engine pods that
// could not run on a real cluster (x9-report): no serviceAccountName, so
// they ran as the namespace's unbound "default" account and were denied
// their own DownloadClient; no POD_NAMESPACE, so they could not even name
// it; no NATS_URL, so they dialled a Service no installer creates; and no
// GOMEMLIMIT, design §12's 80% of the memory limit. envtest schedules no
// pods, so only the rendered pod spec can show any of it.
func TestEnginePodsGetTheRuntimeTheyNeed(t *testing.T) {
	rt := EngineRuntime{
		ServiceAccountName: "media-clustarr-grabarr-engine",
		NATSURL:            "nats://nats.clustarr-system.svc:4222",
		BusSingleNode:      true,
		Umask:              "002",
	}
	torrent := torrentClient("sab", 1)
	torrent.Spec.Resources.Limits = corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("1Gi")}
	usenet := usenetClient("nzb")
	usenet.Spec.Resources.Limits = corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("512Mi")}

	sts := buildStatefulSet(torrent, "sab-engine", "img", "/data", "/scratch", "clustarr-data", rt, nil, fakeOwnerRef())
	dep := buildDeployment(usenet, "nzb-engine", "img", "/data", "/scratch", "clustarr-data", rt, nil, fakeOwnerRef())

	for name, tc := range map[string]struct {
		spec     *corev1ac.PodSpecApplyConfiguration
		memLimit string
		args     string
	}{
		// The chart's clustarr.gomemlimit renders these same two byte counts
		// for a 1Gi and a 512Mi Deployment (charts/clustarr/README.md).
		"torrent": {spec: sts.Spec.Template.Spec, memLimit: "858993459"},
		"usenet":  {spec: dep.Spec.Template.Spec, memLimit: "429496730"},
	} {
		t.Run(name, func(t *testing.T) {
			require.NotNil(t, tc.spec.ServiceAccountName, "the engine pod names no ServiceAccount")
			assert.Equal(t, rt.ServiceAccountName, *tc.spec.ServiceAccountName)
			c := tc.spec.Containers[0]
			assert.Equal(t, map[string]string{
				"POD_NAMESPACE": "fieldRef:metadata.namespace",
				"NATS_URL":      rt.NATSURL,
				"UMASK":         "002",
				"GOMEMLIMIT":    tc.memLimit,
			}, envOf(c.Env))
			assert.Contains(t, strings.Join(append(c.Command, c.Args...), " "), "--nats-single-node")
		})
	}

	t.Run("no memory limit, no bus settings", func(t *testing.T) {
		bare := buildStatefulSet(torrentClient("sab", 1), "sab-engine", "img", "/data", "/scratch", "clustarr-data",
			EngineRuntime{ServiceAccountName: DefaultEngineServiceAccount}, nil, fakeOwnerRef())
		c := bare.Spec.Template.Spec.Containers[0]
		assert.Equal(t, map[string]string{"POD_NAMESPACE": "fieldRef:metadata.namespace"}, envOf(c.Env),
			"GOMEMLIMIT, NATS_URL and UMASK are set only when there is something to set them to")
		assert.NotContains(t, c.Args[0], "--nats-single-node")
	})

	t.Run("NewReconciler defaults the engine ServiceAccount", func(t *testing.T) {
		r := NewReconciler(nil, nil, "/data", "/scratch", "img")
		assert.Equal(t, DefaultEngineServiceAccount, r.Engine.ServiceAccountName)
	})
}

// TestTorrentEngineHashCoversTheProxyAndItsSecret: the torrent engine reads
// spec.torrent.proxy and its Secret at start, so both roll the engine; a
// torrent client without a proxy keeps the hash it had.
func TestTorrentEngineHashCoversTheProxyAndItsSecret(t *testing.T) {
	plain := torrentClient("frugal", 1)
	before := engineConfigHash(plain, nil)
	require.Equal(t, before, engineConfigHash(plain, map[string]string{}), "no Secret digests, no change")

	proxied := plain.DeepCopy()
	proxied.Spec.Torrent.Proxy = &downloadv1alpha1.TorrentProxy{
		Host: "10.64.0.1", Port: 1080, SecretRef: &corev1.LocalObjectReference{Name: "mullvad"},
	}
	withProxy := engineConfigHash(proxied, nil)
	require.NotEqual(t, before, withProxy)
	require.NotEqual(t, withProxy, engineConfigHash(proxied, map[string]string{"mullvad": "digest-a"}))
	require.NotEqual(t,
		engineConfigHash(proxied, map[string]string{"mullvad": "digest-a"}),
		engineConfigHash(proxied, map[string]string{"mullvad": "digest-b"}), "a rotated proxy password rolls the engine")

	sc := fake.NewClientBuilder().WithScheme(clientgoscheme.Scheme).WithObjects(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "mullvad", Namespace: "default"},
		Data:       map[string][]byte{"username": []byte("u")},
	}).Build()
	r := &Reconciler{Client: sc, SecretReader: sc}
	got, err := r.secretDigests(context.Background(), proxied)
	require.NoError(t, err)
	require.Contains(t, got, "mullvad", "the proxy's Secret is digested like a usenet provider's")
}

// torrentScratch renders a torrent client with scratch set, 3 replicas unless
// the placement needs one.
func torrentScratch(replicas int32, sc downloadv1alpha1.ScratchSpec) (*downloadv1alpha1.DownloadClient, *appsv1ac.StatefulSetApplyConfiguration) {
	dc := torrentClient("qb", replicas)
	dc.Spec.Torrent.Scratch = &sc
	return dc, buildStatefulSet(dc, "qb-engine", "img", "/data", "/scratch", "clustarr-data", EngineRuntime{}, nil, fakeOwnerRef())
}

func volumeNames(sts *appsv1ac.StatefulSetApplyConfiguration) []string {
	var out []string
	for _, v := range sts.Spec.Template.Spec.Volumes {
		out = append(out, *v.Name)
	}
	return out
}

func mountAt(c corev1ac.ContainerApplyConfiguration, name string) string {
	for _, m := range c.VolumeMounts {
		if *m.Name == name {
			return *m.MountPath
		}
	}
	return ""
}

// Without scratch a torrent engine is exactly what it was: no scratch
// volume, no claim template, no --scratch-dir.
func TestTorrentWithoutScratchIsUnchanged(t *testing.T) {
	dc := torrentClient("qb", 2)
	sts := buildStatefulSet(dc, "qb-engine", "img", "/data", "/scratch", "clustarr-data", EngineRuntime{}, nil, fakeOwnerRef())
	assert.Empty(t, sts.Spec.VolumeClaimTemplates)
	assert.NotContains(t, volumeNames(sts), scratchVolumeName)
	assert.NotContains(t, sts.Spec.Template.Spec.Containers[0].Args[0], "--scratch-dir")
	assert.False(t, needsScratchClaim(dc))
}

// A storage class gives every replica a claim of its own: one RWO claim
// shared by three pods would leave two unschedulable.
func TestTorrentScratchStorageClassIsAClaimPerReplica(t *testing.T) {
	class := "local-path"
	dc, sts := torrentScratch(3, downloadv1alpha1.ScratchSpec{StorageClassName: &class, SizeLimit: resource.MustParse("200Gi")})

	require.Len(t, sts.Spec.VolumeClaimTemplates, 1)
	tpl := sts.Spec.VolumeClaimTemplates[0]
	assert.Equal(t, scratchVolumeName, *tpl.Name)
	assert.Equal(t, "local-path", *tpl.Spec.StorageClassName)
	assert.Equal(t, []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}, tpl.Spec.AccessModes)
	assert.Equal(t, resource.MustParse("200Gi"), (*tpl.Spec.Resources.Requests)[corev1.ResourceStorage])
	assert.NotContains(t, volumeNames(sts), scratchVolumeName, "the claim template supplies the volume")
	assert.False(t, needsScratchClaim(dc), "no single claim for a per-replica placement")

	c := sts.Spec.Template.Spec.Containers[0]
	assert.Equal(t, "/scratch", mountAt(c, scratchVolumeName))
	assert.Contains(t, c.Args[0], "--scratch-dir /scratch")
}

func TestTorrentScratchPathIsADirectoryOnTheDataMount(t *testing.T) {
	dc, sts := torrentScratch(3, downloadv1alpha1.ScratchSpec{Path: "/data/torrents/incomplete"})
	assert.Empty(t, sts.Spec.VolumeClaimTemplates)
	assert.NotContains(t, volumeNames(sts), scratchVolumeName)
	assert.Contains(t, sts.Spec.Template.Spec.Containers[0].Args[0], "--scratch-dir /data/torrents/incomplete")
	assert.False(t, needsScratchClaim(dc))
}

func TestTorrentScratchVolumeNameIsOneBoundClaim(t *testing.T) {
	dc, sts := torrentScratch(1, downloadv1alpha1.ScratchSpec{VolumeName: "nas-pv"})
	require.True(t, needsScratchClaim(dc))
	pvc := buildScratchPVC(dc, fakeOwnerRef())
	assert.Equal(t, "qb-scratch", *pvc.Name)
	assert.Equal(t, "nas-pv", *pvc.Spec.VolumeName)
	assert.Empty(t, sts.Spec.VolumeClaimTemplates)
	assert.Contains(t, volumeNames(sts), scratchVolumeName)
	assert.Equal(t, "/scratch", mountAt(sts.Spec.Template.Spec.Containers[0], scratchVolumeName))
}

func TestTorrentScratchExistingClaimIsMountedByEveryReplica(t *testing.T) {
	dc, sts := torrentScratch(2, downloadv1alpha1.ScratchSpec{ExistingClaim: "nas-rwx"})
	assert.False(t, needsScratchClaim(dc))
	assert.Empty(t, sts.Spec.VolumeClaimTemplates)
	var claim string
	for _, v := range sts.Spec.Template.Spec.Volumes {
		if *v.Name == scratchVolumeName {
			claim = *v.PersistentVolumeClaim.ClaimName
		}
	}
	assert.Equal(t, "nas-rwx", claim)
}

func TestTorrentScratchWithNoPlacementIsAnEmptyDir(t *testing.T) {
	_, sts := torrentScratch(1, downloadv1alpha1.ScratchSpec{})
	var sized bool
	for _, v := range sts.Spec.Template.Spec.Volumes {
		if *v.Name == scratchVolumeName {
			require.NotNil(t, v.EmptyDir)
			sized = v.EmptyDir.SizeLimit != nil && v.EmptyDir.SizeLimit.Equal(resource.MustParse(defaultScratchSize))
		}
	}
	assert.True(t, sized)
}

func TestValidateEngineDirsCoversTorrent(t *testing.T) {
	dc := torrentClient("qb", 1)
	require.NoError(t, validateEngineDirs(dc, "/data"))
	dc.Spec.Torrent.Scratch = &downloadv1alpha1.ScratchSpec{Path: "/data/torrents/incomplete"}
	dc.Spec.Torrent.PublishDir = "/data/torrents/complete"
	require.NoError(t, validateEngineDirs(dc, "/data"))

	dc.Spec.Torrent.Scratch.Path = "/srv/incomplete"
	require.ErrorContains(t, validateEngineDirs(dc, "/data"), "spec.torrent.scratch.path")
	dc.Spec.Torrent.Scratch.Path = "/data/torrents/incomplete"
	dc.Spec.Torrent.PublishDir = "/srv/complete"
	require.ErrorContains(t, validateEngineDirs(dc, "/data"), "spec.torrent.publishDir")
}

// A StatefulSet's volumeClaimTemplates cannot change in place, so a change
// to them means the StatefulSet is replaced (orphaning its pods, which the
// new one adopts and rolls).
func TestClaimTemplatesChanged(t *testing.T) {
	class := "local-path"
	_, with := torrentScratch(2, downloadv1alpha1.ScratchSpec{StorageClassName: &class})
	_, without := torrentScratch(2, downloadv1alpha1.ScratchSpec{Path: "/data/x"})

	live := func(sts *appsv1ac.StatefulSetApplyConfiguration) *appsv1.StatefulSet {
		out := &appsv1.StatefulSet{}
		for _, tpl := range sts.Spec.VolumeClaimTemplates {
			pvc := corev1.PersistentVolumeClaim{}
			pvc.Name = *tpl.Name
			pvc.Spec.StorageClassName = tpl.Spec.StorageClassName
			pvc.Spec.AccessModes = tpl.Spec.AccessModes
			pvc.Spec.Resources.Requests = *tpl.Spec.Resources.Requests
			out.Spec.VolumeClaimTemplates = append(out.Spec.VolumeClaimTemplates, pvc)
		}
		return out
	}
	assert.False(t, claimTemplatesChanged(live(with), with))
	assert.False(t, claimTemplatesChanged(live(without), without))
	assert.True(t, claimTemplatesChanged(live(without), with))
	assert.True(t, claimTemplatesChanged(live(with), without))
}
