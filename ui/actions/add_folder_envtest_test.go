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

package actions_test

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/ui/actions"
)

// TestAddItemPinsTheFolderAgainstARealAPIServer: the Unmatched page's
// confirmation of a folder (the 2026-11-10 non-video libraries design's
// D20) creates an Artist, an Author and an Audiobook whose spec.folder the
// CRDs keep as written -- a name the sanitizer would rewrite, an
// audiobook's "<author>/<title>" -- with no status written, every write a
// grant actions.Grants() declares.
func TestAddItemPinsTheFolderAgainstARealAPIServer(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	env := &envtest.Environment{CRDDirectoryPaths: []string{"../../config/crd/bases"}, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, env.Stop()) })

	scheme := runtime.NewScheme()
	require.NoError(t, catalogv1alpha1.AddToScheme(scheme))
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	require.NoError(t, err)
	ctx := t.Context()
	rec := &recordingWriter{c: c}
	const ns = "default"

	grants := map[actions.Grant]bool{}
	for _, g := range actions.Grants() {
		grants[g] = true
	}
	for _, tc := range []struct {
		req      actions.AddRequest
		resource string
		want     string
	}{
		{actions.AddRequest{Kind: commonv1.MediaKindArtist, Title: "Sunn O)))", ProviderID: "5b11f4ce-a62d-471e-81fc-a69a8278c7da", Folder: "Sunn O)))"}, "artists", "Sunn O)))"},
		{actions.AddRequest{Kind: commonv1.MediaKindAuthor, Title: "Frank Herbert", ProviderID: "OL79034A", Folder: "Frank Herbert"}, "authors", "Frank Herbert"},
		{actions.AddRequest{Kind: commonv1.MediaKindAudiobook, Title: "Dune", ProviderID: "B002V1OF70", Folder: "Frank Herbert/Dune?"}, "audiobooks", "Frank Herbert/Dune?"},
	} {
		t.Run(string(tc.req.Kind), func(t *testing.T) {
			req := tc.req
			req.Namespace, req.RootFolderRef, req.QualityProfileRef, req.Monitored = ns, "root", "profile", true
			name, existed, err := actions.AddItem(ctx, rec, req)
			require.NoError(t, err)
			require.False(t, existed)

			gvk := mustGVK(t, itemFixtures[req.Kind]("", ns), scheme)
			got := getUnstructured(ctx, t, c, gvk, name, ns)
			folder, found, err := unstructured.NestedString(got.Object, "spec", "folder")
			require.NoError(t, err)
			require.True(t, found, "spec.folder is set")
			require.Equal(t, tc.want, folder, "the folder as written")
			require.Nil(t, got.Object["status"], "nothing but spec was written")
			for _, call := range rec.calls() {
				if mustGVK(t, call.obj, scheme).Kind == gvk.Kind {
					require.True(t, grants[actions.Grant{Group: gvk.Group, Resource: tc.resource, Verb: call.verb}],
						"%s %s is not in actions.Grants()", call.verb, gvk.Kind)
				}
			}
		})
	}
}
