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

package delayprofile_test

import (
	"context"
	"os"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/delayprofile"
	"github.com/mediactl/clustarr/pkg/k8s"
)

func newTestClient(t *testing.T) client.Client {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	env := &envtest.Environment{CRDDirectoryPaths: []string{"../../../../config/crd/bases"}, ErrorIfCRDPathMissing: true}
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

// corev1Namespace builds a Namespace object to create in envtest, which
// starts clean (no "default" namespace exists yet, unlike most real
// clusters). It returns a pointer -- the brief this test followed sketched
// c.Create(ctx, &corev1Namespace(name)), but Go does not allow taking the
// address of a function call's result, so this returns *corev1.Namespace
// directly instead.
func corev1Namespace(name string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
}

func TestReconcileMarksReady(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	// A stock kube-apiserver bootstraps "default" itself on startup (it is
	// not something an empty etcd needs seeded), so envtest already has it;
	// tolerate AlreadyExists rather than assuming a clean slate.
	if err := client.IgnoreAlreadyExists(c.Create(ctx, corev1Namespace("default"))); err != nil {
		t.Fatalf("create namespace: %v", err)
	}
	dp := &catalogv1alpha1.DelayProfile{ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: "default"}}
	if err := c.Create(ctx, dp); err != nil {
		t.Fatalf("create DelayProfile: %v", err)
	}

	r := delayprofile.NewReconciler(c, events.NewFakeRecorder(10))
	if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "default"}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	var got catalogv1alpha1.DelayProfile
	if err := c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "default"}, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if !k8s.IsConditionTrue(got.Status.Conditions, catalogv1alpha1.DelayProfileConditionReady) {
		t.Error("Ready is not True")
	}
	if got.Status.ObservedGeneration != got.Generation {
		t.Errorf("observedGeneration = %d, want %d", got.Status.ObservedGeneration, got.Generation)
	}
}
