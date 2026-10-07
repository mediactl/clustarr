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

// TestLiveTVCEL holds the clustarr.io kinds' CEL rules (Live TV spec §3.1):
// exactly one of url and urlFrom, exactly one of guide and dummy, channelID
// with guide, and an immutable providerRef and key. It writes through a
// dynamic client so an absent key stays absent on the wire.
func TestLiveTVCEL(t *testing.T) {
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
	providers := dyn.Resource(schema.GroupVersionResource{Group: "clustarr.io", Version: "v1alpha1", Resource: "iptvproviders"}).Namespace("default")
	channels := dyn.Resource(schema.GroupVersionResource{Group: "clustarr.io", Version: "v1alpha1", Resource: "iptvchannels"}).Namespace("default")

	provider := func(name string, playlist map[string]any) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "clustarr.io/v1alpha1", "kind": "IPTVProvider",
			"metadata": map[string]any{"name": name},
			"spec":     map[string]any{"tuners": int64(2), "playlist": playlist},
		}}
	}
	_, err = providers.Create(ctx, provider("by-url", map[string]any{"url": "http://p.example/get.php"}), metav1.CreateOptions{})
	require.NoError(t, err, "url alone is accepted")
	_, err = providers.Create(ctx, provider("by-secret", map[string]any{"urlFrom": map[string]any{"name": "s", "key": "playlist-url"}}), metav1.CreateOptions{})
	require.NoError(t, err, "urlFrom alone is accepted")
	_, err = providers.Create(ctx, provider("both", map[string]any{"url": "http://p.example", "urlFrom": map[string]any{"name": "s", "key": "k"}}), metav1.CreateOptions{})
	require.ErrorContains(t, err, "set exactly one of url and urlFrom")
	_, err = providers.Create(ctx, provider("neither", map[string]any{}), metav1.CreateOptions{})
	require.ErrorContains(t, err, "set exactly one of url and urlFrom")

	// spec.device.id is eight uppercase hex digits; spec.dvr.maxChannels
	// is 1-450 (PMS takes ~454 channels in one device's map, recorded).
	with := func(name string, path []string, v any) *unstructured.Unstructured {
		p := provider(name, map[string]any{"url": "http://p.example"})
		require.NoError(t, unstructured.SetNestedField(p.Object, v, append([]string{"spec"}, path...)...))
		return p
	}
	for _, c := range []struct {
		name string
		path []string
		v    any
		ok   bool
	}{
		{"id-ok", []string{"device", "id"}, "CAFE0001", true},
		{"id-lower", []string{"device", "id"}, "cafe0001", false},
		{"id-short", []string{"device", "id"}, "CAFE001", false},
		{"id-long", []string{"device", "id"}, "CAFE00011", false},
		{"id-not-hex", []string{"device", "id"}, "CAFEXYZ1", false},
		{"max-1", []string{"dvr", "maxChannels"}, int64(1), true},
		{"max-450", []string{"dvr", "maxChannels"}, int64(450), true},
		{"max-0", []string{"dvr", "maxChannels"}, int64(0), false},
		{"max-451", []string{"dvr", "maxChannels"}, int64(451), false},
	} {
		_, err := providers.Create(ctx, with(c.name, c.path, c.v), metav1.CreateOptions{})
		if c.ok {
			require.NoError(t, err, c.name)
		} else {
			require.Error(t, err, c.name)
		}
	}

	channel := func(name string, epg map[string]any) *unstructured.Unstructured {
		spec := map[string]any{"providerRef": "by-url", "key": "t:bbc1.uk", "number": "1001", "active": true}
		if epg != nil {
			spec["epg"] = epg
		}
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "clustarr.io/v1alpha1", "kind": "IPTVChannel",
			"metadata": map[string]any{"name": name}, "spec": spec,
		}}
	}
	_, err = channels.Create(ctx, channel("guide", map[string]any{"guide": "main", "channelID": "bbc1.uk"}), metav1.CreateOptions{})
	require.NoError(t, err)
	_, err = channels.Create(ctx, channel("dummy", map[string]any{"dummy": true}), metav1.CreateOptions{})
	require.NoError(t, err)
	_, err = channels.Create(ctx, channel("both-epg", map[string]any{"guide": "main", "channelID": "x", "dummy": true}), metav1.CreateOptions{})
	require.ErrorContains(t, err, "set exactly one of guide and dummy")
	_, err = channels.Create(ctx, channel("no-id", map[string]any{"guide": "main"}), metav1.CreateOptions{})
	require.ErrorContains(t, err, "channelID is required with guide")
	_, err = channels.Create(ctx, channel("no-epg", nil), metav1.CreateOptions{})
	require.NoError(t, err, "no epg means by tvg-id, else dummy")

	got, err := channels.Get(ctx, "guide", metav1.GetOptions{})
	require.NoError(t, err)
	require.NoError(t, unstructured.SetNestedField(got.Object, "t:other", "spec", "key"))
	_, err = channels.Update(ctx, got, metav1.UpdateOptions{})
	require.ErrorContains(t, err, "key is immutable")
	got, err = channels.Get(ctx, "guide", metav1.GetOptions{})
	require.NoError(t, err)
	require.NoError(t, unstructured.SetNestedField(got.Object, "other", "spec", "providerRef"))
	_, err = channels.Update(ctx, got, metav1.UpdateOptions{})
	require.ErrorContains(t, err, "providerRef is immutable")

	bad := channel("bad-number", nil)
	require.NoError(t, unstructured.SetNestedField(bad.Object, "10-01", "spec", "number"))
	_, err = channels.Create(ctx, bad, metav1.CreateOptions{})
	require.Error(t, err, "a number is digits with an optional .minor")
}
