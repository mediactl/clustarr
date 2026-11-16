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

package crdcheck

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// A MediaFile's track is a correction a scan may make -- which recording of
// its Album the file is -- while the item it backs stays what it was: kind,
// name and keys immutable (2026-11-16: 3,664 of talos's album files were
// attributed by track number against the wrong edition, and spec.mediaRef
// whole was immutable, so no scan could correct one).
func TestAMediaFilesTrackMayBeCorrectedButNotItsItem(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test` to install the CRDs")
	}
	env := &envtest.Environment{CRDDirectoryPaths: []string{"../../config/crd/bases"}, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, env.Stop()) })
	dyn, err := dynamic.NewForConfig(cfg)
	require.NoError(t, err)
	ctx := context.Background()
	files := dyn.Resource(schema.GroupVersionResource{Group: "catalog.clustarr.io", Version: "v1alpha1", Resource: "mediafiles"}).Namespace("default")

	mf := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "catalog.clustarr.io/v1alpha1", "kind": "MediaFile",
		"metadata": map[string]any{"name": "kush-coma"},
		"spec": map[string]any{
			"mediaRef": map[string]any{"kind": "album", "name": "old", "track": "41984f98-0000-0000-0000-000000000000"},
			"path":     "/data/media/music/Danny Brown/18_danny_brown-kush_coma-fih.mp3",
		},
	}}
	_, err = files.Create(ctx, mf, metav1.CreateOptions{})
	require.NoError(t, err)

	update := func(field string, value any) error {
		t.Helper()
		got, err := files.Get(ctx, "kush-coma", metav1.GetOptions{})
		require.NoError(t, err)
		require.NoError(t, unstructured.SetNestedField(got.Object, value, "spec", "mediaRef", field))
		_, err = files.Update(ctx, got, metav1.UpdateOptions{})
		return err
	}
	require.NoError(t, update("track", "2c0e7363-0000-0000-0000-000000000000"), "the track is corrected")
	require.ErrorContains(t, update("name", "new"), "immutable", "the album it backs is not")
	require.ErrorContains(t, update("kind", "movie"), "immutable")
	require.ErrorContains(t, update("keys", []any{"a"}), "immutable")

	// The manager writes the rename guard's reason (mediafile.RenderNaming)
	// into status.naming; an enum that lacks it refuses the file's whole
	// status apply.
	got, err := files.Get(ctx, "kush-coma", metav1.GetOptions{})
	require.NoError(t, err)
	require.NoError(t, unstructured.SetNestedField(got.Object, map[string]any{"current": false, "reason": "TrackTitleMismatch"}, "status", "naming"))
	_, err = files.UpdateStatus(ctx, got, metav1.UpdateOptions{})
	require.NoError(t, err, "status.naming.reason admits TrackTitleMismatch")
}
