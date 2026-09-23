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

package indexerproxy_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/indexarr/controller/indexerproxy"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// testCfg is the shared control plane. It is nil when KUBEBUILDER_ASSETS is
// unset, in which case every envtest in this package SKIPS -- and a suite that
// finishes in milliseconds skipped rather than passed.
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

func newTestClient(t *testing.T) client.Client {
	t.Helper()
	if testCfg == nil {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	c, err := client.New(testCfg, client.Options{Scheme: k8s.MustNewScheme()})
	require.NoError(t, err)
	return c
}

func newNamespace(t *testing.T, ctx context.Context, c client.Client, ns string) {
	t.Helper()
	err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create namespace %s: %v", ns, err)
	}
}

// flareSolverrServer answers GET / the way FlareSolverr's index endpoint does.
func flareSolverrServer(t *testing.T, version string) (host string, port int32, srv *httptest.Server) {
	t.Helper()
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"msg":"FlareSolverr is ready!","version":"` + version + `"}`))
	}))
	t.Cleanup(srv.Close)
	h, p, err := net.SplitHostPort(srv.Listener.Addr().String())
	require.NoError(t, err)
	n, err := strconv.Atoi(p)
	require.NoError(t, err)
	return h, int32(n), srv
}

func reconcileOnce(t *testing.T, r *indexerproxy.Reconciler, ns, name string) (ctrl.Result, error) {
	t.Helper()
	return r.Reconcile(context.Background(),
		reconcile.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: name}})
}

func TestIndexerProxyReportsReadyAndVersion(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	const ns = "proxy-ready"
	newNamespace(t, ctx, c, ns)

	host, port, srv := flareSolverrServer(t, "v3.3.21")
	proxy := &indexv1alpha1.IndexerProxy{
		ObjectMeta: metav1.ObjectMeta{Name: "flare", Namespace: ns},
		Spec: indexv1alpha1.IndexerProxySpec{
			Type: indexv1alpha1.IndexerProxyTypeFlareSolverr, Host: host, Port: port,
		},
	}
	require.NoError(t, c.Create(ctx, proxy))

	r := indexerproxy.NewReconciler(c, events.NewFakeRecorder(10), srv.Client())
	res, err := reconcileOnce(t, r, ns, "flare")
	require.NoError(t, err)
	assert.Positive(t, res.RequeueAfter, "a reachability probe must be repeated")

	var got indexv1alpha1.IndexerProxy
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "flare"}, &got))

	assert.Equal(t, got.Generation, got.Status.ObservedGeneration)
	ready := k8s.FindCondition(got.Status.Conditions, indexv1alpha1.IndexerProxyConditionReady)
	require.NotNil(t, ready, "no Ready condition: %+v", got.Status.Conditions)
	assert.Equal(t, metav1.ConditionTrue, ready.Status)
	assert.Equal(t, got.Generation, ready.ObservedGeneration,
		"the Ready condition does not carry the generation it was decided from")
	assert.Equal(t, "v3.3.21", got.Status.Version)
	require.NotNil(t, got.Status.LastCheckedAt)
	assert.False(t, got.Status.LastCheckedAt.IsZero())
}

// TestMissingSecretDoesNotReleaseTheProbeResult is the release-on-early-return
// regression test, driven against the TRANSIENT path: a spec.secretRef whose
// Secret is not there (yet). That is the shape that gutted healthy objects in
// Phase C -- a blip, not a mistake.
//
// A blank object cannot observe a release, so the proxy is driven to a real
// steady state by a successful probe FIRST. It then asserts the whole
// previously-written status survives, not merely the field this path sets: a
// Phase C test that checked only its own field passed cleanly while watching
// the object be gutted.
func TestMissingSecretDoesNotReleaseTheProbeResult(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	const ns = "proxy-secret-vanishes"
	newNamespace(t, ctx, c, ns)

	host, port, srv := flareSolverrServer(t, "v3.3.21")
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "proxy-creds", Namespace: ns}}
	require.NoError(t, c.Create(ctx, secret))

	proxy := &indexv1alpha1.IndexerProxy{
		ObjectMeta: metav1.ObjectMeta{Name: "flare", Namespace: ns},
		Spec: indexv1alpha1.IndexerProxySpec{
			Type: indexv1alpha1.IndexerProxyTypeFlareSolverr, Host: host, Port: port,
			SecretRef: &corev1.LocalObjectReference{Name: "proxy-creds"},
		},
	}
	require.NoError(t, c.Create(ctx, proxy))

	r := indexerproxy.NewReconciler(c, events.NewFakeRecorder(10), srv.Client())
	_, err := reconcileOnce(t, r, ns, "flare")
	require.NoError(t, err)

	var steady indexv1alpha1.IndexerProxy
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "flare"}, &steady))
	require.Equal(t, "v3.3.21", steady.Status.Version, "setup: the first reconcile did not populate status.version")
	require.NotNil(t, steady.Status.LastCheckedAt, "setup: the first reconcile did not stamp lastCheckedAt")
	steadyChecked := steady.Status.LastCheckedAt.DeepCopy()

	// The blip: the Secret goes away.
	require.NoError(t, c.Delete(ctx, secret))

	res, err := reconcileOnce(t, r, ns, "flare")
	require.NoError(t, err, "a missing Secret is transient, not terminal")
	assert.Positive(t, res.RequeueAfter, "a missing dependency must be retried")

	var after indexv1alpha1.IndexerProxy
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "flare"}, &after))

	assert.Equal(t, "v3.3.21", after.Status.Version, "a transient failure released status.version")
	require.NotNil(t, after.Status.LastCheckedAt, "a transient failure released status.lastCheckedAt")
	assert.Equal(t, steadyChecked.UTC(), after.Status.LastCheckedAt.UTC(),
		"the un-probed path moved lastCheckedAt; it means 'when it was last probed'")
	assert.Equal(t, after.Generation, after.Status.ObservedGeneration)

	ready := k8s.FindCondition(after.Status.Conditions, indexv1alpha1.IndexerProxyConditionReady)
	require.NotNil(t, ready)
	assert.Equal(t, metav1.ConditionFalse, ready.Status)
	assert.Equal(t, k8s.ReasonDependencyNotReady, ready.Reason)
}

// TestUnaddressableSpecIsTerminalAndKeepsStatus covers the other early return:
// a spec with no port. It is terminal -- only an edit can fix it -- and it,
// too, must declare the complete owned set rather than gutting the object.
func TestUnaddressableSpecIsTerminalAndKeepsStatus(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	const ns = "proxy-unaddressable"
	newNamespace(t, ctx, c, ns)

	host, port, srv := flareSolverrServer(t, "v3.3.21")
	proxy := &indexv1alpha1.IndexerProxy{
		ObjectMeta: metav1.ObjectMeta{Name: "flare", Namespace: ns},
		Spec: indexv1alpha1.IndexerProxySpec{
			Type: indexv1alpha1.IndexerProxyTypeFlareSolverr, Host: host, Port: port,
		},
	}
	require.NoError(t, c.Create(ctx, proxy))

	r := indexerproxy.NewReconciler(c, events.NewFakeRecorder(10), srv.Client())
	_, err := reconcileOnce(t, r, ns, "flare")
	require.NoError(t, err)

	var steady indexv1alpha1.IndexerProxy
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "flare"}, &steady))
	require.Equal(t, "v3.3.21", steady.Status.Version, "setup: the first reconcile did not populate status")
	require.NotNil(t, steady.Status.LastCheckedAt)

	steady.Spec.Port = 0
	require.NoError(t, c.Update(ctx, &steady))

	_, err = reconcileOnce(t, r, ns, "flare")
	require.Error(t, err)
	require.ErrorIs(t, err, reconcile.TerminalError(nil),
		"an unaddressable spec must not be requeued forever: %v", err)

	var after indexv1alpha1.IndexerProxy
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "flare"}, &after))
	assert.Equal(t, "v3.3.21", after.Status.Version, "an invalid spec released status.version")
	assert.NotNil(t, after.Status.LastCheckedAt, "an invalid spec released status.lastCheckedAt")
	assert.Equal(t, after.Generation, after.Status.ObservedGeneration)

	ready := k8s.FindCondition(after.Status.Conditions, indexv1alpha1.IndexerProxyConditionReady)
	require.NotNil(t, ready)
	assert.Equal(t, metav1.ConditionFalse, ready.Status)
	assert.Equal(t, k8s.ReasonInvalidSpec, ready.Reason)
	assert.Equal(t, after.Generation, ready.ObservedGeneration)
}

// TestUnreachableProxyIsNotReadyButKeepsVersion: a probe that fails is the
// third path through the same apply, and it also stamps lastCheckedAt --
// "when it was last probed", not "when it last worked".
func TestUnreachableProxyIsNotReadyButKeepsVersion(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	const ns = "proxy-unreachable"
	newNamespace(t, ctx, c, ns)

	host, port, srv := flareSolverrServer(t, "v3.3.21")
	proxy := &indexv1alpha1.IndexerProxy{
		ObjectMeta: metav1.ObjectMeta{Name: "flare", Namespace: ns},
		Spec: indexv1alpha1.IndexerProxySpec{
			Type: indexv1alpha1.IndexerProxyTypeFlareSolverr, Host: host, Port: port,
		},
	}
	require.NoError(t, c.Create(ctx, proxy))

	r := indexerproxy.NewReconciler(c, events.NewFakeRecorder(10), srv.Client())
	_, err := reconcileOnce(t, r, ns, "flare")
	require.NoError(t, err)

	var steady indexv1alpha1.IndexerProxy
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "flare"}, &steady))
	require.Equal(t, "v3.3.21", steady.Status.Version)
	steadyChecked := steady.Status.LastCheckedAt.DeepCopy()

	srv.Close() // the proxy goes down; the port is now refusing connections

	_, err = reconcileOnce(t, r, ns, "flare")
	require.NoError(t, err, "an unreachable proxy is reported, not returned as an error")

	var after indexv1alpha1.IndexerProxy
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "flare"}, &after))
	assert.Equal(t, "v3.3.21", after.Status.Version,
		"a failed probe released the last known version instead of keeping it beside Ready=False")
	require.NotNil(t, after.Status.LastCheckedAt)
	assert.False(t, after.Status.LastCheckedAt.Before(steadyChecked),
		"a failed probe did not stamp lastCheckedAt")

	ready := k8s.FindCondition(after.Status.Conditions, indexv1alpha1.IndexerProxyConditionReady)
	require.NotNil(t, ready)
	assert.Equal(t, metav1.ConditionFalse, ready.Status)
	assert.Equal(t, indexerproxy.ReasonUnreachable, ready.Reason)
}

func TestReconcileIsANoOpForAMissingProxy(t *testing.T) {
	c := newTestClient(t)
	r := indexerproxy.NewReconciler(c, events.NewFakeRecorder(10), http.DefaultClient)
	res, err := reconcileOnce(t, r, "default", "definitely-not-there")
	require.NoError(t, err)
	assert.Zero(t, res)
}

// TestSetupWithManagerRegisters is the wiring smoke test for Task D1-8.
func TestSetupWithManagerRegisters(t *testing.T) {
	if testCfg == nil {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	mgr, err := ctrl.NewManager(testCfg, ctrl.Options{
		Scheme:  k8s.MustNewScheme(),
		Metrics: server.Options{BindAddress: "0"},
	})
	require.NoError(t, err)
	require.NoError(t, indexerproxy.NewReconciler(
		mgr.GetClient(), events.NewFakeRecorder(10), http.DefaultClient).SetupWithManager(mgr))
}
