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

package search_test

import (
	"context"
	"os"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/mediactl/clustarr/catalogarr/worker/search"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// testCfg is the shared envtest control plane; see the same comment in
// catalogarr/controller/search. It is nil when KUBEBUILDER_ASSETS is unset,
// in which case every envtest here skips -- a skip is NOT a pass.
var testCfg *rest.Config

func TestMain(m *testing.M) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		os.Exit(m.Run())
	}
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{"../../../config/crd/bases"},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	if err != nil {
		panic("start envtest: " + err.Error())
	}
	testCfg = cfg
	code := m.Run()
	if err := env.Stop(); err != nil {
		panic("stop envtest: " + err.Error())
	}
	os.Exit(code)
}

func requireEnvtest(t *testing.T) {
	t.Helper()
	if testCfg == nil {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
}

// newTestManager starts a manager against the shared control plane with the
// search worker's Download field indexes registered, and returns it once its
// cache has synced. A real manager is required rather than a bare client:
// field indexes are a controller-runtime cache feature and client.List with
// MatchingFields fails outright against an unindexed client.
func newTestManager(t *testing.T) ctrl.Manager {
	t.Helper()
	requireEnvtest(t)

	mgr, err := ctrl.NewManager(testCfg, ctrl.Options{
		Scheme:                 k8s.MustNewScheme(),
		Metrics:                metricsserver.Options{BindAddress: k8s.DisabledBindAddress},
		HealthProbeBindAddress: k8s.DisabledBindAddress,
	})
	if err != nil {
		t.Fatalf("build manager: %v", err)
	}
	if err := search.RegisterDownloadIndexes(context.Background(), mgr.GetFieldIndexer()); err != nil {
		t.Fatalf("RegisterDownloadIndexes: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := mgr.Start(ctx); err != nil {
			t.Errorf("manager: %v", err)
		}
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	if !mgr.GetCache().WaitForCacheSync(ctx) {
		t.Fatal("cache did not sync")
	}
	return mgr
}

func newNamespace(t *testing.T, ctx context.Context, c client.Client, ns string) {
	t.Helper()
	err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create namespace %s: %v", ns, err)
	}
}

// eventually polls cond until it holds or the deadline passes. The cached
// client behind a manager is eventually consistent with a write made through
// it, so every assertion about what the cache can see has to poll.
func eventually(t *testing.T, timeout time.Duration, msg string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", timeout, msg)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func strPtr(s string) *string { return &s }

// newManagerWithoutIndexes builds a manager with no field indexes registered,
// for the test that proves Worker.SetupWithManager registers them itself. It
// is not started; the caller does that.
func newManagerWithoutIndexes(t *testing.T) ctrl.Manager {
	t.Helper()
	requireEnvtest(t)
	mgr, err := ctrl.NewManager(testCfg, ctrl.Options{
		Scheme:                 k8s.MustNewScheme(),
		Metrics:                metricsserver.Options{BindAddress: k8s.DisabledBindAddress},
		HealthProbeBindAddress: k8s.DisabledBindAddress,
	})
	if err != nil {
		t.Fatalf("build manager: %v", err)
	}
	return mgr
}
