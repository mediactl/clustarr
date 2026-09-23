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

// TestAUsenetDownloadSurvivesAStatusWrite is the regression test for
// DownloadSpec.Release's "release identity is immutable" CEL rule.
//
// The rule compares five commonv1alpha1.ReleaseInfo fields, every one of them
// +optional with omitempty. It originally compared them unguarded. An absent
// optional field is not present in the object map at all, so CEL does not
// report it unequal -- it raises "no such key" and the apiserver rejects the
// write outright.
//
// That made the rule fatal for usenet specifically: a usenet release has no
// infoHash, by protocol. Every usenet Download therefore failed EVERY write
// after creation, including a status-only apply that never touches spec --
// because a status-subresource write re-evaluates the spec's CEL rules against
// the stored object. Status applies are grabarr's only means of reporting
// progress, so the effect was that usenet downloads could be created and then
// never updated again.
//
// It stayed invisible because every fixture in the tree was torrent-shaped,
// and a torrent release happens to carry all five fields. Two separate tasks
// hit it independently, pinned all five fields in their own fixtures to get
// past it, and moved on. So this test deliberately uses the shape nothing else
// does: infoHash absent entirely.
//
// The test is written against the generated CRD in config/crd/bases through a
// dynamic client, not through the typed client, for a reason that matters: the
// typed Go client always marshals a struct field, so a typed create would send
// `"infoHash": ""` and put the key in the object -- which satisfies the broken
// rule and makes this test pass against the bug it exists to catch.
func TestAUsenetDownloadSurvivesAStatusWrite(t *testing.T) {
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

	gvr := schema.GroupVersionResource{
		Group:    "download.clustarr.io",
		Version:  "v1alpha1",
		Resource: "downloads",
	}
	ctx := context.Background()

	// A usenet release: no infoHash, because usenet has none. Built as
	// unstructured so the key is genuinely absent rather than present-and-empty.
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "download.clustarr.io/v1alpha1",
		"kind":       "Download",
		"metadata":   map[string]any{"name": "usenet-no-infohash", "namespace": "default"},
		"spec": map[string]any{
			"protocol": "usenet",
			"source":   map[string]any{"nzbURL": "http://fixture.invalid/a.nzb"},
			"release": map[string]any{
				"guid":       "g-1",
				"indexerRef": "idx",
				"title":      "Example.Release.2019.1080p.WEB-DL.x264-CLUSTARR",
				"protocol":   "usenet",
			},
			"target": map[string]any{"kind": "movie", "name": "example"},
		},
	}}

	created, err := dyn.Resource(gvr).Namespace("default").Create(ctx, obj, metav1.CreateOptions{})
	require.NoError(t, err, "creating a usenet Download with no infoHash must be accepted")
	require.NotContains(t, created.Object["spec"].(map[string]any)["release"], "infoHash",
		"the fixture must leave infoHash genuinely absent, or this test cannot detect the bug it guards")

	// The write that the unguarded rule rejected. It touches status only and
	// never mentions spec, but the apiserver re-runs spec's CEL rules against
	// the stored object regardless -- which is what made this fatal.
	require.NoError(t, unstructured.SetNestedField(created.Object, "Downloading", "status", "phase"))
	_, err = dyn.Resource(gvr).Namespace("default").UpdateStatus(ctx, created, metav1.UpdateOptions{})
	require.NoError(t, err,
		"a status write to a usenet Download must be accepted; an unguarded "+
			"self.infoHash == oldSelf.infoHash fails here with \"no such key: infoHash\"")
}
