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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	appsv1ac "k8s.io/client-go/applyconfigurations/apps/v1"
	corev1ac "k8s.io/client-go/applyconfigurations/core/v1"
	metav1ac "k8s.io/client-go/applyconfigurations/meta/v1"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"

	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/grabarr/controller/downloadclient"
	"github.com/mediactl/clustarr/pkg/fsops"
	"github.com/mediactl/clustarr/pkg/k8s"
)

func newRolloutReconciler(c client.Client) *downloadclient.Reconciler {
	r := downloadclient.NewReconciler(c, events.NewFakeRecorder(10), "/data", "/scratch", "img")
	r.DiskUsage = func(string) (fsops.Usage, error) {
		return fsops.Usage{Total: 100 << 30, Free: 50 << 30, Available: 50 << 30}, nil
	}
	return r
}

func templateHash(t *testing.T, c client.Client, obj client.Object) string {
	t.Helper()
	require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(obj), obj))
	var ann map[string]string
	switch w := obj.(type) {
	case *appsv1.StatefulSet:
		ann = w.Spec.Template.Annotations
	case *appsv1.Deployment:
		ann = w.Spec.Template.Annotations
	}
	h := ann[downloadclient.EngineConfigHashAnnotation]
	require.NotEmpty(t, h, "the engine pod template carries no config hash")
	return h
}

// A setting the engine reads at start rolls it; one it reads on every
// reconcile does not. Without the hash, stallTimeout (and the rest) took
// effect only when an engine happened to restart.
func TestAStartTimeSettingRollsTheTorrentEngineAndALiveOneDoesNot(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	r := newRolloutReconciler(c)

	dc := &downloadv1alpha1.DownloadClient{
		ObjectMeta: metav1.ObjectMeta{Name: "roll", Namespace: "default"},
		Spec: downloadv1alpha1.DownloadClientSpec{
			Protocol: commonv1alpha1.ProtocolTorrent,
			Replicas: 1,
			Torrent:  &downloadv1alpha1.TorrentSpec{},
		},
	}
	require.NoError(t, c.Create(ctx, dc))
	reconcileOK(t, r, "default", "roll")
	sts := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "roll-engine"}}
	first := templateHash(t, c, sts)

	// seed and removeCompleted are read live: no roll.
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(dc), dc))
	ratio := resource.MustParse("3")
	dc.Spec.Torrent.Seed = &commonv1alpha1.SeedCriteria{Ratio: &ratio}
	off := false
	dc.Spec.Torrent.RemoveCompleted = &off
	require.NoError(t, c.Update(ctx, dc))
	reconcileOK(t, r, "default", "roll")
	assert.Equal(t, first, templateHash(t, c, sts), "a live-read setting restarted every engine ordinal")

	// stallTimeout is read at start: roll.
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(dc), dc))
	dc.Spec.Torrent.StallTimeout = &metav1.Duration{Duration: time.Hour}
	require.NoError(t, c.Update(ctx, dc))
	reconcileOK(t, r, "default", "roll")
	assert.NotEqual(t, first, templateHash(t, c, sts), "a stallTimeout change did not change the engine pod template")
}

func TestAStartTimeSettingRollsTheUsenetEngine(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	r := newRolloutReconciler(c)

	dc := &downloadv1alpha1.DownloadClient{
		ObjectMeta: metav1.ObjectMeta{Name: "nzb", Namespace: "default"},
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
	reconcileOK(t, r, "default", "nzb")
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "nzb-engine"}}
	hash := templateHash(t, c, dep)

	for name, mutate := range map[string]func(*downloadv1alpha1.DownloadClient){
		"downloadTimeout": func(d *downloadv1alpha1.DownloadClient) {
			d.Spec.Usenet.DownloadTimeout = &metav1.Duration{Duration: 6 * time.Hour}
		},
		"healthAction": func(d *downloadv1alpha1.DownloadClient) {
			d.Spec.Usenet.HealthAction = downloadv1alpha1.HealthActionDelete
		},
		"categories": func(d *downloadv1alpha1.DownloadClient) {
			d.Spec.Categories = map[string]string{"Movie": "films"}
		},
	} {
		require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(dc), dc))
		mutate(dc)
		require.NoError(t, c.Update(ctx, dc))
		reconcileOK(t, r, "default", "nzb")
		next := templateHash(t, c, dep)
		assert.NotEqual(t, hash, next, "a %s change did not change the engine pod template", name)
		hash = next
	}
	assert.Equal(t, appsv1.RecreateDeploymentStrategyType, dep.Spec.Strategy.Type,
		"two usenet engines under one identity must never run at once")
}

// A usenet engine Deployment created before the strategy was set carries the
// apiserver's defaulted RollingUpdate, rollingUpdate block included. The next
// reconcile must still be able to move it to Recreate -- the apiserver
// refuses a Recreate Deployment that keeps a rollingUpdate block, and one
// rejected apply here would stop the DownloadClient reconciling at all.
func TestAnExistingUsenetDeploymentMovesToRecreate(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)

	dc := &downloadv1alpha1.DownloadClient{
		ObjectMeta: metav1.ObjectMeta{Name: "old-nzb", Namespace: "default"},
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

	labels := map[string]string{"app.kubernetes.io/component": "grabarr-engine", "download.clustarr.io/client": "old-nzb"}
	old := appsv1ac.Deployment("old-nzb-engine", "default").
		WithLabels(labels).
		WithSpec(appsv1ac.DeploymentSpec().
			WithReplicas(1).
			WithSelector(metav1ac.LabelSelector().WithMatchLabels(labels)).
			WithTemplate(corev1ac.PodTemplateSpec().WithLabels(labels).WithSpec(corev1ac.PodSpec().
				WithContainers(corev1ac.Container().WithName("engine").WithImage("img")))))
	_, err := k8s.Apply(ctx, c, k8s.ManagerGrabarr, old)
	require.NoError(t, err)
	var before appsv1.Deployment
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "old-nzb-engine"}, &before))
	require.Equal(t, appsv1.RollingUpdateDeploymentStrategyType, before.Spec.Strategy.Type)
	require.NotNil(t, before.Spec.Strategy.RollingUpdate, "the precondition: a defaulted rollingUpdate block")

	reconcileOK(t, newRolloutReconciler(c), "default", "old-nzb")

	var after appsv1.Deployment
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "old-nzb-engine"}, &after))
	assert.Equal(t, appsv1.RecreateDeploymentStrategyType, after.Spec.Strategy.Type)
	assert.Nil(t, after.Spec.Strategy.RollingUpdate)
}
