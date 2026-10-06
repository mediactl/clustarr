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
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// TestDownloadPurposeIsImmutableAndOptional holds spec.purpose (anime
// dual-audio spec §8: set at creation, immutable) to its has()-guarded
// rule: a Download without one still takes status writes -- the class
// TestAUsenetDownloadSurvivesAStatusWrite guards -- and the purpose can be
// neither added, removed nor changed afterwards. And AudioGraft's status
// lists are capped.
func TestDownloadPurposeIsImmutableAndOptional(t *testing.T) {
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
	downloads := dyn.Resource(schema.GroupVersionResource{Group: "download.clustarr.io", Version: "v1alpha1", Resource: "downloads"}).Namespace("default")

	download := func(name string, purpose string) *unstructured.Unstructured {
		spec := map[string]any{
			"protocol": "usenet",
			"source":   map[string]any{"nzbURL": "http://fixture.invalid/a.nzb"},
			"release": map[string]any{
				"guid": "g-" + name, "indexerRef": "idx", "protocol": "usenet",
				"title": "Monster.s01e02.Downfall.DVDRip.480p.x264.AAC.DL-BoB",
			},
			"target": map[string]any{"kind": "episode", "name": "monster-s01e02"},
		}
		if purpose != "" {
			spec["purpose"] = purpose
		}
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "download.clustarr.io/v1alpha1", "kind": "Download",
			"metadata": map[string]any{"name": name, "namespace": "default"},
			"spec":     spec,
		}}
	}

	plain, err := downloads.Create(ctx, download("video", ""), metav1.CreateOptions{})
	require.NoError(t, err)
	require.NoError(t, unstructured.SetNestedField(plain.Object, "Downloading", "status", "phase"))
	plain, err = downloads.UpdateStatus(ctx, plain, metav1.UpdateOptions{})
	require.NoError(t, err, "a Download without a purpose must take status writes")
	require.NoError(t, unstructured.SetNestedField(plain.Object, "audioDonor", "spec", "purpose"))
	_, err = downloads.Update(ctx, plain, metav1.UpdateOptions{})
	require.ErrorContains(t, err, "purpose is immutable", "a purpose cannot be added after creation")

	donor, err := downloads.Create(ctx, download("donor", "audioDonor"), metav1.CreateOptions{})
	require.NoError(t, err)
	require.NoError(t, unstructured.SetNestedField(donor.Object, "Downloading", "status", "phase"))
	donor, err = downloads.UpdateStatus(ctx, donor, metav1.UpdateOptions{})
	require.NoError(t, err, "a donor Download must take status writes")
	unstructured.RemoveNestedField(donor.Object, "spec", "purpose")
	_, err = downloads.Update(ctx, donor, metav1.UpdateOptions{})
	require.ErrorContains(t, err, "purpose is immutable", "a purpose cannot be removed")

	_, err = downloads.Create(ctx, download("bogus", "subtitles"), metav1.CreateOptions{})
	require.Error(t, err, "purpose is an enum")

	grafts := dyn.Resource(schema.GroupVersionResource{Group: "transcode.clustarr.io", Version: "v1alpha1", Resource: "audiografts"}).Namespace("default")
	g, err := grafts.Create(ctx, &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "transcode.clustarr.io/v1alpha1", "kind": "AudioGraft",
		"metadata": map[string]any{"name": "monster-s01e02-audiograft", "namespace": "default"},
		"spec": map[string]any{
			"itemRef":   map[string]any{"kind": "episode", "name": "monster-s01e02"},
			"donorPath": "/data/tv/.clustarr/donors/uid/monster-s01e02.mkv",
			"languages": []any{"en"}, "anchor": "ja", "default": "en",
			"release": "Monster.s01e02.Downfall.DVDRip.480p.x264.AAC.DL-BoB",
		},
	}}, metav1.CreateOptions{})
	require.NoError(t, err)
	var segs []any
	for i := range 17 {
		segs = append(segs, map[string]any{"donorStartMillis": int64(i), "targetStartMillis": int64(i), "lengthMillis": int64(1)})
	}
	require.NoError(t, unstructured.SetNestedSlice(g.Object, segs, "status", "segments"))
	_, err = grafts.UpdateStatus(ctx, g, metav1.UpdateOptions{})
	require.ErrorContains(t, err, fmt.Sprint(16), "status.segments is capped at 16")
}
