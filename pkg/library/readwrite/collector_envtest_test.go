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

package readwrite_test

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/library/readwrite"
	"github.com/mediactl/clustarr/test/librarytest"
)

// TestTheCollectorDeletesWhatItsOwnerLeftBehind is design §3.4: a Comic is
// a custom resource and its Issues are rows, so the apiserver's garbage
// collector no longer deletes an Issue with its Comic. The collector does,
// by that collector's rule. An Issue whose Comic is there is kept; with the
// Comic deleted it is deleted, held by its finalizer as a delete of it is;
// one whose Comic was made again under the same name belongs to the Comic
// that is gone and goes too; one with no owner is nobody's to collect.
func TestTheCollectorDeletesWhatItsOwnerLeftBehind(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		if os.Getenv("CLUSTARR_REQUIRE_TOOLS") == "1" {
			t.Fatal("KUBEBUILDER_ASSETS is unset and CLUSTARR_REQUIRE_TOOLS=1")
		}
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	st := librarytest.Start(t).Store(t, nil)
	env := &envtest.Environment{CRDDirectoryPaths: []string{"../../../config/crd/bases"}, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, env.Stop()) })
	scheme := runtime.NewScheme()
	require.NoError(t, catalogv1.AddToScheme(scheme))
	api, err := client.New(cfg, client.Options{Scheme: scheme})
	require.NoError(t, err)
	c := readwrite.NewClient(api, st, false, "clustarr")
	ctx := t.Context()

	comic := func(name string) *catalogv1.Comic {
		return &catalogv1.Comic{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec: catalogv1.ComicSpec{
				Source: catalogv1.ComicSourceComicVine, SourceID: "4050-" + name,
				QualityProfileRef: "any", RootFolderRef: "comics",
			},
		}
	}
	issueOf := func(owner *catalogv1.Comic, name string, finalizer bool) *catalogv1.Issue {
		iss := &catalogv1.Issue{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec:       catalogv1.IssueSpec{ComicRef: "x", Number: "1", CalculatedNumberCentis: 100},
		}
		if owner != nil {
			require.NoError(t, controllerutil.SetControllerReference(owner, iss, scheme))
		}
		if finalizer {
			controllerutil.AddFinalizer(iss, "clustarr.io/item")
		}
		require.NoError(t, c.Create(ctx, iss))
		return iss
	}
	exists := func(name string) (*catalogv1.Issue, bool) {
		var iss catalogv1.Issue
		err := c.Get(ctx, client.ObjectKey{Namespace: "default", Name: name}, &iss)
		if apierrors.IsNotFound(err) {
			return nil, false
		}
		require.NoError(t, err)
		return &iss, true
	}

	saga, girls := comic("saga"), comic("paper-girls")
	require.NoError(t, c.Create(ctx, saga))
	require.NoError(t, c.Create(ctx, girls))
	issueOf(saga, "saga-1", false)
	issueOf(saga, "saga-2", true)
	issueOf(girls, "paper-girls-1", false)
	issueOf(nil, "loose-1", false)

	collector := &readwrite.Collector{Store: st, Owners: api}
	n, err := collector.Sweep(ctx)
	require.NoError(t, err)
	assert.Zero(t, n, "every owner is there")

	t.Log("saga is deleted, and made again under its name")
	require.NoError(t, c.Delete(ctx, saga))
	again := comic("saga")
	require.NoError(t, c.Create(ctx, again))
	require.NotEqual(t, saga.UID, again.UID)
	issueOf(again, "saga-3", false)

	n, err = collector.Sweep(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, n, "the two Issues of the Comic that is gone")
	_, ok := exists("saga-1")
	assert.False(t, ok, "an Issue with no finalizer is gone")
	held, ok := exists("saga-2")
	require.True(t, ok, "an Issue with a finalizer is marked, and waits for it")
	assert.NotNil(t, held.DeletionTimestamp)
	for _, name := range []string{"saga-3", "paper-girls-1", "loose-1"} {
		iss, ok := exists(name)
		require.True(t, ok, name)
		assert.Nil(t, iss.DeletionTimestamp, name)
	}

	t.Log("a second sweep finds the marked Issue already going and nothing else")
	n, err = collector.Sweep(ctx)
	require.NoError(t, err)
	assert.Zero(t, n, "the marked Issue is already going")
	held, ok = exists("saga-2")
	require.True(t, ok)
	controllerutil.RemoveFinalizer(held, "clustarr.io/item")
	require.NoError(t, c.Update(ctx, held))
	_, ok = exists("saga-2")
	assert.False(t, ok, "its finalizer removed, it is gone")
}
