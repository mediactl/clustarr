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

// TestAnM3UProviderNeedsAPlaylistURLAndAnXtreamOneAServer holds
// IPTVProvider's type rule (the direct-tuners and ui design §5.7): an
// Xtream provider (the default) names a server and a credentials Secret;
// an M3U one a playlist URL, and no Secret unless its URLs need one.
func TestAnM3UProviderNeedsAPlaylistURLAndAnXtreamOneAServer(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test` to install the CRDs")
	}
	env := &envtest.Environment{CRDDirectoryPaths: []string{"../../config/crd/bases"}, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, env.Stop()) })
	dyn, err := dynamic.NewForConfig(cfg)
	require.NoError(t, err)
	providers := dyn.Resource(schema.GroupVersionResource{Group: "clustarr.io", Version: "v1alpha1", Resource: "iptvproviders"}).Namespace("default")
	create := func(name string, spec map[string]any) error {
		_, err := providers.Create(context.Background(), &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "clustarr.io/v1alpha1", "kind": "IPTVProvider", "metadata": map[string]any{"name": name}, "spec": spec,
		}}, metav1.CreateOptions{})
		return err
	}
	const msg = "an Xtream provider needs server and credentialsRef; an M3U provider needs playlistURL"
	require.NoError(t, create("xtream", map[string]any{"server": "tvload.win", "credentialsRef": map[string]any{"name": "s"}}), "type defaults to Xtream")
	require.ErrorContains(t, create("xtream-no-server", map[string]any{"credentialsRef": map[string]any{"name": "s"}}), msg)
	require.ErrorContains(t, create("xtream-no-secret", map[string]any{"server": "tvload.win"}), msg)
	require.NoError(t, create("m3u", map[string]any{"type": "M3U", "playlistURL": "https://lists.example/all.m3u"}), "no server, no Secret")
	require.ErrorContains(t, create("m3u-no-url", map[string]any{"type": "M3U"}), msg)
	require.Error(t, create("other", map[string]any{"type": "Stalker", "playlistURL": "https://x"}), "the type is an enum")
}
