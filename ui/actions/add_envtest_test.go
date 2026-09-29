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
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/ui/actions"
)

// TestAddItemAgainstARealAPIServer is Add New's apiserver proof
// (docs/superpowers/specs/2026-09-29-add-new-design.md, Testing): each of
// the four kinds is accepted by its CRD's schema and CEL rules, owned by
// clustarr-ui with no status field; a second add lands on the first; a
// value the CRD enum refuses is returned, not swallowed; and every write
// is a (group, resource, verb) actions.Grants() declares, which
// cmd/clustarr/ui_rbac_test.go holds the role to (envtest enforces no RBAC).
func TestAddItemAgainstARealAPIServer(t *testing.T) {
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

	reqs := []actions.AddRequest{
		{Kind: commonv1.MediaKindMovie, Title: "Heat", ProviderID: "949", Monitor: "movieOnly", MinimumAvailability: "released"},
		{Kind: commonv1.MediaKindSeries, Title: "Breaking Bad", ProviderID: "81189", Monitor: "future", SeriesType: "standard", MonitorNewItems: "all", SeasonFolder: true},
		{Kind: commonv1.MediaKindArtist, Title: "Radiohead", ProviderID: "a74b1b7f-71a5-4011-9441-d0b5e4122711", Monitor: "all", MonitorNewItems: "new"},
		{Kind: commonv1.MediaKindAuthor, Title: "Jane Austen", ProviderID: "OL21594A", Monitor: "all", MonitorNewItems: "all"},
	}
	grants := map[actions.Grant]bool{}
	for _, g := range actions.Grants() {
		grants[g] = true
	}
	for _, req := range reqs {
		req.Namespace, req.RootFolderRef, req.QualityProfileRef, req.Monitored, req.SearchOnAdd = ns, "root", "profile", true, true
		t.Run(string(req.Kind), func(t *testing.T) {
			name, existed, err := actions.AddItem(ctx, rec, req)
			require.NoError(t, err)
			require.False(t, existed)

			obj := itemFixtures[req.Kind]("", ns)
			gvk := mustGVK(t, obj, scheme)
			got := getUnstructured(ctx, t, c, gvk, name, ns)
			requireNeverOnStatus(t, got)
			require.Nil(t, got.Object["status"], "nothing but spec was written")

			_, existed, err = actions.AddItem(ctx, rec, req)
			require.NoError(t, err)
			require.True(t, existed, "the second add lands on the first")
		})
	}

	bad := reqs[0]
	bad.Namespace, bad.RootFolderRef, bad.QualityProfileRef, bad.ProviderID, bad.Monitor = ns, "root", "profile", "550", "bogus"
	_, _, err = actions.AddItem(ctx, rec, bad)
	require.Error(t, err, "the CRD enum refuses monitor=bogus, and the error is returned")

	for _, call := range rec.calls() {
		gvk := mustGVK(t, call.obj, scheme)
		resource := map[string]string{"Movie": "movies", "Series": "series", "Artist": "artists", "Author": "authors"}[gvk.Kind]
		require.True(t, grants[actions.Grant{Group: gvk.Group, Resource: resource, Verb: call.verb}], "%s %s is not in actions.Grants()", call.verb, gvk.Kind)
	}
}
