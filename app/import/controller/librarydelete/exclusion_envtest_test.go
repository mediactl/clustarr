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

package librarydelete

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// envtestClient is a client against a real apiserver with the CRDs
// installed, nil without KUBEBUILDER_ASSETS. The package's other tests run
// the reconciler against controller-runtime's fake client (newClient),
// which validates nothing, so a write the CRD would refuse -- a kind
// outside the ImportExclusion's enum, an id map the CEL rule rejects, a
// Book's workID off its pattern -- is proven only here.
var envtestClient client.Client

func TestMain(m *testing.M) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		os.Exit(m.Run()) // the envtests skip themselves
	}
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{"../../../../config/crd/bases"},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	if err != nil {
		fmt.Fprintf(os.Stderr, "start envtest: %v\n", err)
		os.Exit(1)
	}
	code := func() int {
		envtestClient, err = client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
		if err != nil {
			fmt.Fprintf(os.Stderr, "build client: %v\n", err)
			return 1
		}
		return m.Run()
	}()
	if err := env.Stop(); err != nil {
		fmt.Fprintf(os.Stderr, "stop envtest: %v\n", err)
	}
	os.Exit(code)
}

func requireEnvtest(t *testing.T) client.Client {
	t.Helper()
	if envtestClient == nil {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	return envtestClient
}

// The Album and Book kindSpecs render an ImportExclusion the apiserver takes
// (2026-11-10, task F2's review): each fixture item is one its CRD accepts
// (the Book's workID pattern and its authorless CEL rule, the Album's
// required refs), and the exclusion rendered from it as the reconciler's
// exclude renders it from its cached read -- kind album or book, the ids
// map under the musicbrainz or openlibrary key, the metadata's title and
// year, the reason -- is created against the installed ImportExclusion CRD
// and read back whole.
func TestAlbumAndBookExclusionsAreAcceptedByTheAPIServer(t *testing.T) {
	c := requireEnvtest(t)
	ctx := context.Background()
	ns := "librarydelete-exclusions"
	require.NoError(t, c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}))

	a := album("dsotm", "/data/music/Pink Floyd/The Dark Side of the Moon (1973)", catalogv1alpha1.DeleteFiles)
	a.Annotations[catalogv1alpha1.AnnotationDeleteAddExclusion] = "true"
	a.Status.Metadata = &catalogv1alpha1.AlbumMetadata{
		Title:       "The Dark Side of the Moon",
		ReleaseDate: &metav1.Time{Time: time.Date(1973, 3, 1, 0, 0, 0, 0, time.UTC)},
	}
	b := standaloneBook("/data/books/The Dispossessed", catalogv1alpha1.DeleteRecords)
	b.Annotations[catalogv1alpha1.AnnotationDeleteAddExclusion] = "true"
	b.Status.Metadata = &catalogv1alpha1.BookMetadata{
		Title:       "The Dispossessed",
		ReleaseDate: &metav1.Time{Time: time.Date(1974, 5, 1, 0, 0, 0, 0, time.UTC)},
	}

	for _, tc := range []struct {
		kind      commonv1.MediaKind
		item      client.Object
		wantKind  catalogv1alpha1.ExclusionKind
		wantIDs   map[string]string
		wantTitle string
		wantYear  int32
	}{
		{
			kind: commonv1.MediaKindAlbum, item: a, wantKind: catalogv1alpha1.ExclusionKindAlbum,
			wantIDs:   map[string]string{catalogv1alpha1.ExclusionIDKeyMusicBrainz: "rg-dsotm"},
			wantTitle: "The Dark Side of the Moon", wantYear: 1973,
		},
		{
			kind: commonv1.MediaKindBook, item: b, wantKind: catalogv1alpha1.ExclusionKindBook,
			wantIDs:   map[string]string{catalogv1alpha1.ExclusionIDKeyOpenLibrary: "OL1W"},
			wantTitle: "The Dispossessed", wantYear: 1974,
		},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			// Rendered first: the reconciler renders from its cached read,
			// status and all, and a create's answer carries no status.
			spec, ok := kindFor(tc.kind).exclusion(tc.item)
			require.True(t, ok, "the kind has an exclusion")

			// The fixture itself is an object its CRD takes; the apiserver
			// assigns the uid.
			tc.item.SetNamespace(ns)
			tc.item.SetUID("")
			require.NoError(t, c.Create(ctx, tc.item), "the fixture item is one its CRD accepts")

			ex := &catalogv1alpha1.ImportExclusion{
				ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: tc.item.GetName()},
				Spec:       spec,
			}
			require.NoError(t, c.Create(ctx, ex), "the rendered exclusion is one the CRD accepts")

			var got catalogv1alpha1.ImportExclusion
			require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: tc.item.GetName()}, &got))
			assert.Equal(t, tc.wantKind, got.Spec.Kind)
			assert.Equal(t, tc.wantIDs, got.Spec.ExternalIDs)
			assert.Equal(t, tc.wantTitle, got.Spec.Title)
			assert.Equal(t, tc.wantYear, got.Spec.Year)
			assert.Equal(t, exclusionReason, got.Spec.Reason)
		})
	}
}
