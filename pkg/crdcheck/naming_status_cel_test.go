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

// TestNamingStatusRoundTripsAndScanRenameIsAnEnum compiles two of Task 1's
// additions against a real apiserver: MediaFile's new status.naming field
// (NamingStatus, catalogarr's proposal for the file's canonical path) round
// trips a status write, and LibraryScan's new spec.rename field accepts only
// off, dryRun and apply.
func TestNamingStatusRoundTripsAndScanRenameIsAnEnum(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test` to install the CRDs")
	}

	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{"../../config/crd/bases"},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	require.NoError(t, err, "start envtest")
	t.Cleanup(func() { require.NoError(t, env.Stop()) })

	dyn, err := dynamic.NewForConfig(cfg)
	require.NoError(t, err)
	ctx := context.Background()

	gvrMediaFiles := schema.GroupVersionResource{Group: "catalog.clustarr.io", Version: "v1alpha1", Resource: "mediafiles"}
	mediaFile := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "catalog.clustarr.io/v1alpha1",
		"kind":       "MediaFile",
		"metadata":   map[string]any{"name": "inception-abc1234567", "namespace": "default"},
		"spec": map[string]any{
			"mediaRef": map[string]any{"kind": "movie", "name": "inception"},
			"path":     "/data/media/movies/Inception (2010)/Inception (2010).mkv",
		},
	}}
	created, err := dyn.Resource(gvrMediaFiles).Namespace("default").Create(ctx, mediaFile, metav1.CreateOptions{})
	require.NoError(t, err, "creating the MediaFile")

	require.NoError(t, unstructured.SetNestedMap(created.Object, map[string]any{
		"expectedPath": "/data/media/movies/Inception (2010)/Inception (2010) - [Bluray-1080p][x265].mkv",
		"current":      false,
		"reason":       "ProbePending",
	}, "status", "naming"))
	updated, err := dyn.Resource(gvrMediaFiles).Namespace("default").UpdateStatus(ctx, created, metav1.UpdateOptions{})
	require.NoError(t, err, "status.naming with reason ProbePending must round-trip")

	got, err := dyn.Resource(gvrMediaFiles).Namespace("default").Get(ctx, updated.GetName(), metav1.GetOptions{})
	require.NoError(t, err)
	naming, found, err := unstructured.NestedMap(got.Object, "status", "naming")
	require.NoError(t, err)
	require.True(t, found, "status.naming must be present after the write")
	require.Equal(t, "ProbePending", naming["reason"])

	gvrLibraryScans := schema.GroupVersionResource{Group: "catalog.clustarr.io", Version: "v1alpha1", Resource: "libraryscans"}
	scan := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "catalog.clustarr.io/v1alpha1",
		"kind":       "LibraryScan",
		"metadata":   map[string]any{"name": "movies-abc", "namespace": "default"},
		"spec": map[string]any{
			"rootFolderRef": "movies",
			"rename":        "sideways",
		},
	}}
	_, err = dyn.Resource(gvrLibraryScans).Namespace("default").Create(ctx, scan, metav1.CreateOptions{})
	require.Error(t, err, "rename accepts only off, dryRun and apply")
}
