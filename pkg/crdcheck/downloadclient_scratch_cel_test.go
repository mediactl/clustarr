/*
Copyright 2026 The clustarr Authors.

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

// TestScratchPlacementsAreMutuallyExclusive compiles ScratchSpec's CEL rules
// against a real apiserver: a scratch path on the data mount excludes every
// volume placement, and an existing claim excludes the two that make the
// controller create one.
func TestScratchPlacementsAreMutuallyExclusive(t *testing.T) {
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
	gvr := schema.GroupVersionResource{Group: "download.clustarr.io", Version: "v1alpha1", Resource: "downloadclients"}
	ctx := context.Background()

	client := func(name string, scratch map[string]any) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "download.clustarr.io/v1alpha1",
			"kind":       "DownloadClient",
			"metadata":   map[string]any{"name": name, "namespace": "default"},
			"spec": map[string]any{
				"protocol": "usenet",
				"usenet": map[string]any{
					"providers": []any{map[string]any{
						"name": "p", "host": "news.example.invalid", "connections": int64(4),
						"secretRef": map[string]any{"name": "p"},
					}},
					"scratch": scratch,
				},
			},
		}}
	}

	for _, ok := range []struct {
		name    string
		scratch map[string]any
	}{
		{"path-only", map[string]any{"path": "/data/usenet/incomplete"}},
		{"existing-claim", map[string]any{"existingClaim": "nas-scratch"}},
		{"volume-name-rwx", map[string]any{"volumeName": "pv", "accessModes": []any{"ReadWriteMany"}}},
		{"class-and-volume", map[string]any{"storageClassName": "nfs", "volumeName": "pv"}},
	} {
		_, err := dyn.Resource(gvr).Namespace("default").Create(ctx, client(ok.name, ok.scratch), metav1.CreateOptions{})
		require.NoError(t, err, ok.name)
	}

	for _, bad := range []struct {
		name    string
		scratch map[string]any
	}{
		{"path-and-class", map[string]any{"path": "/data/usenet/incomplete", "storageClassName": "nfs"}},
		{"path-and-claim", map[string]any{"path": "/data/usenet/incomplete", "existingClaim": "x"}},
		{"path-and-volume", map[string]any{"path": "/data/usenet/incomplete", "volumeName": "pv"}},
		{"claim-and-class", map[string]any{"existingClaim": "x", "storageClassName": "nfs"}},
		{"claim-and-volume", map[string]any{"existingClaim": "x", "volumeName": "pv"}},
		{"relative-path", map[string]any{"path": "usenet/incomplete"}},
	} {
		_, err := dyn.Resource(gvr).Namespace("default").Create(ctx, client(bad.name, bad.scratch), metav1.CreateOptions{})
		require.Error(t, err, bad.name)
	}
}
