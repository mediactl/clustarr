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

package extmetrics_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mediactl/clustarr/app/autoscale/extmetrics"
)

func apiServiceExists(t *testing.T, c client.Client) bool {
	t.Helper()
	err := c.Get(context.Background(), client.ObjectKeyFromObject(apiServiceKey()), apiServiceKey())
	if apierrors.IsNotFound(err) {
		return false
	}
	require.NoError(t, err)
	return true
}

func pointAPIServiceAt(t *testing.T, c client.Client, namespace string) {
	t.Helper()
	u := readAPIService(t, c)
	require.NoError(t, unstructured.SetNestedField(u.Object, namespace, "spec", "service", "namespace"))
	require.NoError(t, c.Update(context.Background(), u))
}

// Without the guard, deleting the release namespace leaves a cluster-scoped
// APIService pointing at a deleted Service, and every namespace deletion in
// the cluster stalls on the failed discovery (§9.8). envtest runs no
// namespace controller, so a deleted namespace stays Terminating, which is
// exactly the window the guard acts in.
func TestNamespaceGuardDeletesTheAPIServiceOnlyWhileItsNamespaceTerminates(t *testing.T) {
	_, c := startEnv(t)
	ctx := context.Background()
	namespace := createNamespace(t, c, "guard-release")
	g := &extmetrics.NamespaceGuard{Client: c, Reader: c, Namespace: namespace, ServiceName: "external-metrics"}
	assert.False(t, g.NeedLeaderElection(), "every replica: whichever is shut down last must act")
	require.NoError(t, c.Create(ctx, installerAPIService(namespace, "external-metrics")))

	deleted, err := g.Check(ctx)
	require.NoError(t, err)
	assert.False(t, deleted, "a live namespace keeps its APIService")
	assert.True(t, apiServiceExists(t, c))

	require.NoError(t, c.Delete(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}))
	require.Eventually(t, func() bool {
		var n corev1.Namespace
		return c.Get(ctx, client.ObjectKey{Name: namespace}, &n) == nil && !n.DeletionTimestamp.IsZero()
	}, 10*time.Second, 100*time.Millisecond)

	pointAPIServiceAt(t, c, "keda")
	deleted, err = g.Check(ctx)
	require.NoError(t, err)
	assert.False(t, deleted, "another provider's APIService is never deleted")
	assert.True(t, apiServiceExists(t, c))

	pointAPIServiceAt(t, c, namespace)
	stopped, cancel := context.WithCancel(ctx)
	cancel()
	require.NoError(t, g.Start(stopped), "Start acts once its context ends, as at SIGTERM")
	assert.False(t, apiServiceExists(t, c), "the terminating release namespace's APIService is deleted on the way out")

	deleted, err = g.Check(ctx)
	require.NoError(t, err)
	assert.False(t, deleted, "nothing left to delete; not an error")
}
