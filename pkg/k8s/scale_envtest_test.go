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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mediactl/clustarr/pkg/k8s"
)

// The autoscale reconciler wakes a Deployment at zero through its scale
// subresource (spec 2026-10-06 §9.5). The write is recorded under
// clustarr-autoscale on the scale subresource, and a Deployment already at
// the target is not written again.
func TestScaleToWritesTheScaleSubresource(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	const ns = "scale-to"
	require.NoError(t, c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}))
	labels := map[string]string{"app": "agent-import"}
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "agent-import", Namespace: ns},
		Spec: appsv1.DeploymentSpec{
			Replicas: new(int32(0)),
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "agent", Image: "example.invalid/native:test"}}},
			},
		},
	}
	require.NoError(t, c.Create(ctx, dep))

	require.NoError(t, k8s.ScaleTo(ctx, c, k8s.ManagerAutoscale, dep, 1))
	var got appsv1.Deployment
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(dep), &got))
	assert.Equal(t, int32(1), *got.Spec.Replicas)
	recorded := false
	for _, mf := range got.ManagedFields {
		if mf.Manager == string(k8s.ManagerAutoscale) && mf.Subresource == "scale" {
			recorded = true
		}
	}
	assert.True(t, recorded, "the scale write is recorded under clustarr-autoscale on the scale subresource: %v",
		fieldManagerNames(got.ManagedFields))

	// Already at 1: no write. The apiserver drops a no-op update without a
	// new resourceVersion, so the request itself is what is counted.
	var scaleWrites int
	counted := subResourceUpdateCounter{Client: c, updates: &scaleWrites}
	require.NoError(t, k8s.ScaleTo(ctx, counted, k8s.ManagerAutoscale, dep, 1))
	assert.Zero(t, scaleWrites, "already at 1: no write")
	require.NoError(t, k8s.ScaleTo(ctx, counted, k8s.ManagerAutoscale, dep, 0))
	assert.Equal(t, 1, scaleWrites, "a change is one scale write")
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(dep), &got))
	assert.Equal(t, int32(0), *got.Spec.Replicas)

	assert.Error(t, k8s.ScaleTo(ctx, nil, k8s.FieldManager("nobody"), dep, 1), "an unknown manager is refused before any request")
	assert.Error(t, k8s.ScaleTo(ctx, nil, k8s.ManagerAutoscale, dep, 1), "a nil client is refused, not dereferenced")
}

// subResourceUpdateCounter counts the subresource Updates it forwards.
type subResourceUpdateCounter struct {
	client.Client
	updates *int
}

func (c subResourceUpdateCounter) SubResource(sub string) client.SubResourceClient {
	return countedSubResource{SubResourceClient: c.Client.SubResource(sub), updates: c.updates}
}

type countedSubResource struct {
	client.SubResourceClient
	updates *int
}

func (s countedSubResource) Update(ctx context.Context, obj client.Object, opts ...client.SubResourceUpdateOption) error {
	*s.updates++
	return s.SubResourceClient.Update(ctx, obj, opts...)
}
