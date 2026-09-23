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

package download_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	downloadac "github.com/mediactl/clustarr/api/applyconfiguration/download/download/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	downloadctl "github.com/mediactl/clustarr/grabarr/controller/download"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// seedOutputPath patches status.outputPath directly under
// k8s.ManagerGrabarrEngine, standing in for what a real torrent or usenet
// engine (D2-5/D2-6, not yet built) would have reported. It also writes a
// real file under dataDir so the finalizer's fsops.SafeRemove has something
// to prove it did or did not touch.
func seedOutputPath(t *testing.T, ctx context.Context, c client.Client, ns, name, outputPath string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(outputPath), 0o755))
	require.NoError(t, os.WriteFile(outputPath, []byte("fixture content"), 0o644))

	obj := downloadac.Download(name, ns).WithStatus(downloadac.DownloadStatus().WithOutputPath(outputPath))
	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerGrabarrEngine, obj)
	require.NoError(t, err)
}

func TestFinalizerRemovesDataWhenRemoveDataOnDeleteIsTrue(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	dataDir := t.TempDir()

	dl := newTorrentDownload(t, ctx, c, "default", "finalize-remove-dl", "guid-finalize-remove")
	// RemoveDataOnDelete left unset: kubebuilder:default=true applies through
	// envtest's real apiserver defaulting.

	outputPath := filepath.Join(dataDir, "torrents", "movies", dl.Name)
	seedOutputPath(t, ctx, c, "default", dl.Name, outputPath)

	r := downloadctl.NewReconciler(c, events.NewFakeRecorder(10), dataDir)
	reconcileOK(t, r, "default", dl.Name) // adds the finalizer

	live := getDownload(t, ctx, c, "default", dl.Name)
	require.True(t, ptr.Deref(live.Spec.RemoveDataOnDelete, false), "CRD default must have applied")

	require.NoError(t, c.Delete(ctx, live))
	reconcileOK(t, r, "default", dl.Name)

	_, statErr := os.Stat(outputPath)
	assert.True(t, os.IsNotExist(statErr), "the downloaded data must be removed")

	var gone downloadv1alpha1.Download
	err := c.Get(ctx, types.NamespacedName{Namespace: "default", Name: dl.Name}, &gone)
	assert.True(t, apierrors.IsNotFound(err), "the Download must be fully deleted once the finalizer clears")
}

func TestFinalizerKeepsDataWhenRemoveDataOnDeleteIsFalse(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	dataDir := t.TempDir()

	dl := newTorrentDownload(t, ctx, c, "default", "finalize-keep-dl", "guid-finalize-keep")

	// Flip RemoveDataOnDelete to false via a spec patch (Source/Release/etc
	// are immutable, but RemoveDataOnDelete carries no such CEL rule).
	patch := client.MergeFrom(dl.DeepCopy())
	dl.Spec.RemoveDataOnDelete = ptr.To(false)
	require.NoError(t, c.Patch(ctx, dl, patch))

	outputPath := filepath.Join(dataDir, "torrents", "movies", dl.Name)
	seedOutputPath(t, ctx, c, "default", dl.Name, outputPath)

	r := downloadctl.NewReconciler(c, events.NewFakeRecorder(10), dataDir)
	reconcileOK(t, r, "default", dl.Name)

	live := getDownload(t, ctx, c, "default", dl.Name)
	require.False(t, ptr.Deref(live.Spec.RemoveDataOnDelete, true))

	require.NoError(t, c.Delete(ctx, live))
	reconcileOK(t, r, "default", dl.Name)

	_, statErr := os.Stat(outputPath)
	assert.NoError(t, statErr, "data must survive deletion when removeDataOnDelete is false")

	var gone downloadv1alpha1.Download
	err := c.Get(ctx, types.NamespacedName{Namespace: "default", Name: dl.Name}, &gone)
	assert.True(t, apierrors.IsNotFound(err), "the Download must still be fully deleted")
}

func TestFinalizerWithNoOutputPathYetIsANoOp(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	dataDir := t.TempDir()

	dl := newTorrentDownload(t, ctx, c, "default", "finalize-empty-dl", "guid-finalize-empty")

	r := downloadctl.NewReconciler(c, events.NewFakeRecorder(10), dataDir)
	reconcileOK(t, r, "default", dl.Name) // adds the finalizer; never assigned or downloaded

	live := getDownload(t, ctx, c, "default", dl.Name)
	require.NoError(t, c.Delete(ctx, live))
	reconcileOK(t, r, "default", dl.Name)

	var gone downloadv1alpha1.Download
	err := c.Get(ctx, types.NamespacedName{Namespace: "default", Name: dl.Name}, &gone)
	assert.True(t, apierrors.IsNotFound(err))
}
