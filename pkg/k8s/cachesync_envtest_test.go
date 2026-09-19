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
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/mediactl/clustarr/pkg/k8s"
)

// startEnvtest brings up a control plane for one test.
func startEnvtest(t *testing.T) *rest.Config {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{"../../config/crd/bases"},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := env.Stop(); err != nil {
			t.Errorf("stop envtest: %v", err)
		}
	})
	return cfg
}

// TestCacheSyncCheckerBecomesReadyOnANonLeaderReplica is the regression test
// for the rollout deadlock Task C12a's review caught.
//
// [k8s.CacheSyncChecker] originally added its Runnable as a bare
// manager.RunnableFunc. That type has no NeedLeaderElection method, so
// controller-runtime's runnables.Add falls through its type switch to
// `default: r.LeaderElection.Add(...)` and puts it behind the lease. Both
// catalogarr and importarr run with --leader-elect, so on a non-leader replica
// the runnable never started, `synced` never flipped and /readyz failed
// FOREVER. With the chart's default RollingUpdate at replicas 1 (maxSurge 1,
// maxUnavailable 0) the surge pod can never go Ready while the outgoing pod
// holds the lease, and the outgoing pod is never terminated -- so every
// catalogarr and importarr rollout deadlocks.
//
// The existing coverage could not see it: cmd/clustarr's start-up envtest sets
// LeaderElect = false, which puts every runnable in the same group and makes
// the test pass vacuously.
//
// This test therefore turns leader election ON and makes sure the manager can
// NEVER win it: it pre-creates the Lease, held by another identity, with a
// renew deadline far in the future. Under the old code the readiness check
// stays failing until the test times out; with an EveryReplica runnable it
// passes within a second or two.
func TestCacheSyncCheckerBecomesReadyOnANonLeaderReplica(t *testing.T) {
	cfg := startEnvtest(t)

	const (
		leaseNamespace = "default"
		leaseID        = "cachesync-test.clustarr.io"
	)

	// Hold the lease under someone else's name, with a renew time far enough
	// ahead that this manager can never take it over.
	c, err := client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
	require.NoError(t, err)
	renew := metav1.NewMicroTime(time.Now().Add(24 * time.Hour))
	require.NoError(t, c.Create(context.Background(), &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{Name: leaseID, Namespace: leaseNamespace},
		Spec: coordinationv1.LeaseSpec{
			HolderIdentity:       ptr.To("someone-else"),
			LeaseDurationSeconds: ptr.To(int32(86400)),
			AcquireTime:          &renew,
			RenewTime:            &renew,
		},
	}))

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                  k8s.MustNewScheme(),
		Metrics:                 metricsserver.Options{BindAddress: k8s.DisabledBindAddress},
		HealthProbeBindAddress:  k8s.DisabledBindAddress,
		LeaderElection:          true,
		LeaderElectionID:        leaseID,
		LeaderElectionNamespace: leaseNamespace,
	})
	require.NoError(t, err)

	ready, err := k8s.CacheSyncChecker(mgr)
	require.NoError(t, err)

	// Something has to make the cache non-empty of informers, or
	// WaitForCacheSync is trivially true and the test proves less than it
	// looks like it does.
	_, err = mgr.GetCache().GetInformer(context.Background(), &corev1.ConfigMap{})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		_ = mgr.Start(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-stopped:
		case <-time.After(30 * time.Second):
			t.Error("the manager did not stop within 30s")
		}
	})

	require.Never(t, func() bool {
		// Sanity: this replica must genuinely not be the leader for the
		// assertion below to mean anything.
		var lease coordinationv1.Lease
		if err := c.Get(ctx, client.ObjectKey{Namespace: leaseNamespace, Name: leaseID}, &lease); err != nil {
			return false
		}
		return lease.Spec.HolderIdentity != nil && *lease.Spec.HolderIdentity != "someone-else"
	}, 2*time.Second, 200*time.Millisecond, "the manager acquired a lease it was supposed to be locked out of")

	require.Eventually(t, func() bool {
		return ready(&http.Request{}) == nil
	}, 30*time.Second, 100*time.Millisecond,
		"readiness never passed on a replica that does not hold the leader lease: the cache-sync "+
			"runnable is leader-gated, so every rollout of this service deadlocks")
}
