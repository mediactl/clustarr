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

package k8s_test

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv1 "k8s.io/api/autoscaling/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	corev1ac "k8s.io/client-go/applyconfigurations/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
)

// counterValue reads one resolved counter series. It reads the series
// through client_model rather than prometheus/testutil, which needs a module
// go.mod does not declare (app/catalog/metadata's tieredcache_test.go says
// why), so the test builds under -mod=readonly without touching go.mod.
func counterValue(t *testing.T, c prometheus.Counter) float64 {
	t.Helper()
	var m dto.Metric
	require.NoError(t, c.Write(&m))
	return m.GetCounter().GetValue()
}

// TestReadOnlyRefusesEveryWriterAndCounts: an agent's client (ADR-0019
// §9.2) refuses every write -- the main resource's, Status()'s and any
// SubResource's -- with ErrAgentWrite, counts each refusal once under its
// verb in clustarr_agent_write_refused_total, and never reaches the store
// behind it; reads pass through.
func TestReadOnlyRefusesEveryWriterAndCounts(t *testing.T) {
	ctx := context.Background()
	const ns = "default"
	seeded := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "seeded", Namespace: ns}, Data: map[string]string{"k": "v"}}
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "agent-import", Namespace: ns},
		Spec: appsv1.DeploymentSpec{
			Replicas: new(int32(2)),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "agent-import"}},
		},
	}
	backing := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(seeded, dep).Build()
	c := k8s.ReadOnly(backing)

	fresh := func() *corev1.ConfigMap {
		return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "fresh", Namespace: ns}, Data: map[string]string{"k": "new"}}
	}
	changed := func() *corev1.ConfigMap {
		cm := seeded.DeepCopy()
		cm.Data = map[string]string{"k": "changed"}
		return cm
	}
	patch := client.RawPatch(types.MergePatchType, []byte(`{"data":{"k":"patched"}}`))
	scale := &autoscalingv1.Scale{Spec: autoscalingv1.ScaleSpec{Replicas: 5}}

	for _, tc := range []struct {
		verb  string
		write func() error
	}{
		{"create", func() error { return c.Create(ctx, fresh()) }},
		{"update", func() error { return c.Update(ctx, changed()) }},
		{"patch", func() error { return c.Patch(ctx, seeded.DeepCopy(), patch) }},
		{"apply", func() error {
			return c.Apply(ctx, corev1ac.ConfigMap("seeded", ns).WithData(map[string]string{"k": "applied"}),
				client.FieldOwner(k8s.ManagerImport.String()))
		}},
		{"delete", func() error { return c.Delete(ctx, seeded.DeepCopy()) }},
		{"deleteAllOf", func() error { return c.DeleteAllOf(ctx, &corev1.ConfigMap{}, client.InNamespace(ns)) }},
		{"status/create", func() error { return c.Status().Create(ctx, dep.DeepCopy(), &appsv1.Deployment{}) }},
		{"status/update", func() error { return c.Status().Update(ctx, dep.DeepCopy()) }},
		{"status/patch", func() error { return c.Status().Patch(ctx, dep.DeepCopy(), patch) }},
		{"status/apply", func() error {
			return c.Status().Apply(ctx, corev1ac.ConfigMap("seeded", ns), client.FieldOwner(k8s.ManagerImport.String()))
		}},
		{"scale/create", func() error { return c.SubResource("scale").Create(ctx, dep.DeepCopy(), scale.DeepCopy()) }},
		{"scale/update", func() error {
			return c.SubResource("scale").Update(ctx, dep.DeepCopy(), client.WithSubResourceBody(scale.DeepCopy()))
		}},
		{"scale/patch", func() error { return c.SubResource("scale").Patch(ctx, dep.DeepCopy(), patch) }},
		{"scale/apply", func() error {
			return c.SubResource("scale").Apply(ctx, corev1ac.ConfigMap("seeded", ns), client.FieldOwner(k8s.ManagerAutoscale.String()))
		}},
	} {
		t.Run(tc.verb, func(t *testing.T) {
			counter := metrics.AgentWriteRefusedTotal.WithLabelValues(tc.verb)
			before := counterValue(t, counter)
			require.ErrorIs(t, tc.write(), k8s.ErrAgentWrite)
			assert.InDelta(t, before+1, counterValue(t, counter), 0, "one refusal counts once, under %q", tc.verb)
		})
	}

	// Nothing reached the store: reads through the wrapper see it unchanged.
	var cm corev1.ConfigMap
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(seeded), &cm))
	assert.Equal(t, map[string]string{"k": "v"}, cm.Data)
	assert.True(t, apierrors.IsNotFound(c.Get(ctx, client.ObjectKeyFromObject(fresh()), &corev1.ConfigMap{})),
		"a refused create left no object behind")
	var list corev1.ConfigMapList
	require.NoError(t, c.List(ctx, &list, client.InNamespace(ns)))
	require.Len(t, list.Items, 1)
	assert.Equal(t, "seeded", list.Items[0].Name)
	var got appsv1.Deployment
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(dep), &got))
	assert.Equal(t, int32(2), *got.Spec.Replicas, "a refused scale write left the replicas alone")

	// A subresource read passes through, and counts nothing.
	readCounter := metrics.AgentWriteRefusedTotal.WithLabelValues("scale/get")
	before := counterValue(t, readCounter)
	var read autoscalingv1.Scale
	require.NoError(t, c.SubResource("scale").Get(ctx, dep.DeepCopy(), &read))
	assert.Equal(t, int32(2), read.Spec.Replicas)
	assert.InDelta(t, before, counterValue(t, readCounter), 0)
}
