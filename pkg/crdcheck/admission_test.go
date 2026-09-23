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

// admissionCase is one object sent to a real apiserver as a server-side
// dry-run create. wantErr is a substring of the rejection, or "" when the
// object must be admitted.
type admissionCase struct {
	name    string
	gvr     schema.GroupVersionResource
	obj     map[string]any
	wantErr string
}

var (
	gvrIndexerProxies = schema.GroupVersionResource{Group: "index.clustarr.io", Version: "v1alpha1", Resource: "indexerproxies"}
	gvrArtists        = schema.GroupVersionResource{Group: "catalog.clustarr.io", Version: "v1alpha1", Resource: "artists"}
	gvrMediaFiles     = schema.GroupVersionResource{Group: "catalog.clustarr.io", Version: "v1alpha1", Resource: "mediafiles"}
)

// TestAdmission pins the API shape decisions of the gap-fix wave (X1) at the
// one place a schema decision is actually enforced: a real apiserver
// compiling the generated CRDs in config/crd/bases. Every case is an
// unstructured object through a dynamic client, so an absent key stays absent
// on the wire -- the typed client would marshal a zero value and hide the
// difference between "omitted" and "zero" that several of these cases turn on.
// Creates are server-side dry runs, so cases never collide on names and no
// state survives between them.
func TestAdmission(t *testing.T) {
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

	cases := []admissionCase{}
	cases = append(cases, indexerProxyPortCases()...)
	cases = append(cases, artistSecondaryTypeCases()...)
	cases = append(cases, mediaRefTrackCases()...)

	ctx := context.Background()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			obj := &unstructured.Unstructured{Object: c.obj}
			_, err := dyn.Resource(c.gvr).Namespace("default").Create(ctx, obj,
				metav1.CreateOptions{DryRun: []string{metav1.DryRunAll}})
			if c.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err, "the apiserver admitted an object the schema must refuse")
			require.ErrorContains(t, err, c.wantErr)
		})
	}
}

// indexerProxyPortCases: spec.port is +required in 1..65535 (gap-fix ruling
// R-9). Before, the CRD admitted a portless proxy that the controller then
// refused as an invalid spec on every reconcile.
func indexerProxyPortCases() []admissionCase {
	proxy := func(port any) map[string]any {
		spec := map[string]any{"type": "http", "host": "proxy.local"}
		if port != nil {
			spec["port"] = port
		}
		return map[string]any{
			"apiVersion": "index.clustarr.io/v1alpha1",
			"kind":       "IndexerProxy",
			"metadata":   map[string]any{"name": "p", "namespace": "default"},
			"spec":       spec,
		}
	}
	return []admissionCase{
		{"IndexerProxy with a port is admitted", gvrIndexerProxies, proxy(int64(3128)), ""},
		{"IndexerProxy without a port is refused", gvrIndexerProxies, proxy(nil), "spec.port: Required value"},
		{"IndexerProxy with port 0 is refused", gvrIndexerProxies, proxy(int64(0)), "spec.port"},
		{"IndexerProxy with port 65536 is refused", gvrIndexerProxies, proxy(int64(65536)), "spec.port"},
	}
}

// artistSecondaryTypeCases: MusicBrainz's twelfth secondary release-group
// type, "Field recording", has a CRD token (fieldRecording) like the other
// eleven; before, a profile could not name it at all.
func artistSecondaryTypeCases() []admissionCase {
	artist := func(secondary ...any) map[string]any {
		return map[string]any{
			"apiVersion": "catalog.clustarr.io/v1alpha1",
			"kind":       "Artist",
			"metadata":   map[string]any{"name": "a", "namespace": "default"},
			"spec": map[string]any{
				"musicBrainzID":     "b10bbbfc-cf9e-42e0-be17-e2c3e1d2600d",
				"qualityProfileRef": "q",
				"rootFolderRef":     "r",
				"metadataProfile":   map[string]any{"secondaryTypes": secondary},
			},
		}
	}
	return []admissionCase{
		{"Artist profile accepting fieldRecording is admitted", gvrArtists, artist("studio", "fieldRecording"), ""},
		{"Artist profile with an unknown secondary type is refused", gvrArtists, artist("Field recording"), "secondaryTypes"},
	}
}

// mediaRefTrackCases: MediaRef.track addresses one Track of an Album by its
// recording MBID, so a single-track MediaFile can be attributed per track.
// It is meaningless on any other kind and the CEL rule on MediaRef says so.
func mediaRefTrackCases() []admissionCase {
	mediaFile := func(ref map[string]any) map[string]any {
		return map[string]any{
			"apiVersion": "catalog.clustarr.io/v1alpha1",
			"kind":       "MediaFile",
			"metadata":   map[string]any{"name": "mf", "namespace": "default"},
			"spec": map[string]any{
				"mediaRef": ref,
				"path":     "/data/media/music/Artist/Album (2001)/01 - Track.flac",
			},
		}
	}
	const recording = "5b11f4ce-a62d-471e-81fc-a69a8278c7da"
	return []admissionCase{
		{
			"MediaFile addressing an album track is admitted", gvrMediaFiles,
			mediaFile(map[string]any{"kind": "album", "name": "nevermind", "track": recording}), "",
		},
		{
			"MediaFile addressing a whole album is still admitted", gvrMediaFiles,
			mediaFile(map[string]any{"kind": "album", "name": "nevermind"}), "",
		},
		{
			"MediaFile with a track on a non-album kind is refused", gvrMediaFiles,
			mediaFile(map[string]any{"kind": "movie", "name": "heat", "track": recording}), "needs kind album",
		},
		{
			"MediaFile with an over-long track id is refused", gvrMediaFiles,
			mediaFile(map[string]any{"kind": "album", "name": "nevermind", "track": recording + "-x"}), "spec.mediaRef.track",
		},
	}
}
