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

package rootfolder_test

import (
	"context"
	"os"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/rootfolder"
	"github.com/mediactl/clustarr/pkg/k8s"
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

func TestReconcileAccessiblePathIsReady(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	rf := &catalogv1alpha1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Name: "movies", Namespace: "default"},
		Spec: catalogv1alpha1.RootFolderSpec{
			Path: "/data/media/movies",
			Kind: catalogv1alpha1.RootFolderKindMovie,
		},
	}
	if err := c.Create(ctx, rf); err != nil {
		t.Fatalf("create RootFolder: %v", err)
	}

	r := rootfolder.NewReconciler(c, events.NewFakeRecorder(10))
	r.CheckPath = func(path string) (bool, int64, int64, error) {
		if path != "/data/media/movies" {
			t.Errorf("checkPath called with %q, want /data/media/movies", path)
		}
		return true, 500_000_000_000, 1_000_000_000_000, nil
	}

	if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "movies"}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	var got catalogv1alpha1.RootFolder
	if err := c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "movies"}, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if !got.Status.Accessible {
		t.Error("status.accessible = false, want true")
	}
	if got.Status.FreeBytes != 500_000_000_000 || got.Status.TotalBytes != 1_000_000_000_000 {
		t.Errorf("freeBytes=%d totalBytes=%d, want 500e9/1e12", got.Status.FreeBytes, got.Status.TotalBytes)
	}
	if !k8s.IsConditionTrue(got.Status.Conditions, catalogv1alpha1.RootFolderConditionReady) {
		t.Error("Ready is not True")
	}
	if !k8s.IsConditionTrue(got.Status.Conditions, catalogv1alpha1.RootFolderConditionDiskSpaceOK) {
		t.Error("DiskSpaceOK is not True")
	}
}

func TestReconcileMissingPathIsNotReady(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	rf := &catalogv1alpha1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Name: "tv", Namespace: "default"},
		Spec: catalogv1alpha1.RootFolderSpec{
			Path: "/data/media/tv",
			Kind: catalogv1alpha1.RootFolderKindSeries,
		},
	}
	if err := c.Create(ctx, rf); err != nil {
		t.Fatalf("create RootFolder: %v", err)
	}

	r := rootfolder.NewReconciler(c, events.NewFakeRecorder(10))
	r.CheckPath = func(string) (bool, int64, int64, error) {
		return false, 0, 0, os.ErrNotExist
	}
	if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "tv"}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	var got catalogv1alpha1.RootFolder
	if err := c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "tv"}, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status.Accessible {
		t.Error("status.accessible = true, want false")
	}
	if k8s.IsConditionTrue(got.Status.Conditions, catalogv1alpha1.RootFolderConditionReady) {
		t.Error("Ready is True for a missing path")
	}
	cond := k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.RootFolderConditionReady)
	if cond == nil || cond.Reason != "PathNotFound" {
		t.Errorf("Ready reason = %+v, want PathNotFound", cond)
	}
}

func TestReconcileBelowMinFreeBytesFailsDiskSpaceOK(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	rf := &catalogv1alpha1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Name: "music", Namespace: "default"},
		Spec: catalogv1alpha1.RootFolderSpec{
			Path:         "/data/media/music",
			Kind:         catalogv1alpha1.RootFolderKindMusic,
			MinFreeBytes: 100_000_000_000,
		},
	}
	if err := c.Create(ctx, rf); err != nil {
		t.Fatalf("create RootFolder: %v", err)
	}

	r := rootfolder.NewReconciler(c, events.NewFakeRecorder(10))
	r.CheckPath = func(string) (bool, int64, int64, error) {
		return true, 10_000_000_000, 1_000_000_000_000, nil // 10GB free, need 100GB
	}
	if _, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "music"}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	var got catalogv1alpha1.RootFolder
	if err := c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "music"}, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if k8s.IsConditionTrue(got.Status.Conditions, catalogv1alpha1.RootFolderConditionDiskSpaceOK) {
		t.Error("DiskSpaceOK is True with free below minFreeBytes")
	}
	// Accessible is still true -- DiskSpaceOK is a separate condition from
	// Ready per the CRD's own condition pair; Ready follows DiskSpaceOK too
	// (an inaccessible-but-technically-writable folder that is about to fill
	// up is not "ready" either).
	if k8s.IsConditionTrue(got.Status.Conditions, catalogv1alpha1.RootFolderConditionReady) {
		t.Error("Ready is True while DiskSpaceOK is False")
	}
}
