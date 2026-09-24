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

package search_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/search"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// statusFieldsOwnedBy returns the top-level status field keys the named
// manager owns, straight out of the object's managedFields.
//
// This is the only way to tell "declared empty" from "released" apart: both
// leave an empty list on the object, and only the ownership record says
// whether the manager is still speaking for the field. Everything else is a
// proxy.
func statusFieldsOwnedBy(t *testing.T, obj *catalogv1alpha1.Search, manager string) map[string]any {
	t.Helper()
	for _, mf := range obj.ManagedFields {
		if mf.Manager != manager || mf.Subresource != "status" || mf.FieldsV1 == nil {
			continue
		}
		var parsed map[string]any
		require.NoError(t, json.Unmarshal(mf.FieldsV1.GetRawBytes(), &parsed))
		owned, _ := parsed["f:status"].(map[string]any)
		return owned
	}
	return nil
}

// TestAnEmptyResultsListIsReleasedAndReadsEmpty pins what an empty
// status.results means on the wire now that Search's apply configuration is
// generated (gap-fix X1 item 3), because it changed.
//
// The hand-written apply configuration this package used to carry held its
// lists as pointers to slices, so WithResults() with nothing to append sent
// `results: []` and kept the field owned. Every generated apply-configuration
// field is omitempty, so an empty list now goes out omitted and the field is
// RELEASED. For an atomic list with exactly one owner -- status.results is the
// worker's alone on a mediaRef Search and the reconciler's alone on a query
// Search -- the object reads the same either way: no results. Only the
// ownership record differs, and this test pins that difference so no comment
// goes back to claiming the field stays owned. What preserves real results
// across a write was never the wire form: it is the caller re-declaring them
// (Worker.writeFailure, Reconciler.newStatusUpdate).
func TestAnEmptyResultsListIsReleasedAndReadsEmpty(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	const ns, name = "search-ownership", "srch-own"
	newNamespace(t, ctx, c, ns)

	require.NoError(t, c.Create(ctx, &catalogv1alpha1.Search{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: catalogv1alpha1.SearchSpec{
			MediaRef: &commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "the-matrix"},
		},
	}))

	// Steady state first: a blank object cannot observe a release.
	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrWorker,
		catalogac.Search(name, ns).WithStatus(
			catalogac.SearchStatus().
				WithFinishedAt(metav1.Now()).
				WithIndexerOutcomes(catalogac.IndexerOutcome().WithName("idx").WithState(catalogv1alpha1.IndexerOutcomeOK).WithCount(1)).
				WithResults(approvedResult("g1"))))
	require.NoError(t, err)

	got := &catalogv1alpha1.Search{}
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, got))
	require.Len(t, got.Status.Results, 1)
	require.Contains(t, statusFieldsOwnedBy(t, got, string(k8s.ManagerCatalogarrWorker)), "f:results")

	// The same manager applies again with nothing to put in the list, which is
	// what a terminal failure with no earlier results carries.
	_, err = k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrWorker,
		catalogac.Search(name, ns).WithStatus(
			catalogac.SearchStatus().
				WithFinishedAt(metav1.Now()).
				WithIndexerOutcomes().
				WithResults()))
	require.NoError(t, err)

	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, got))
	require.Empty(t, got.Status.Results, "an empty list must read as no results")
	owned := statusFieldsOwnedBy(t, got, string(k8s.ManagerCatalogarrWorker))
	require.NotContains(t, owned, "f:results",
		"a generated apply configuration omits an empty list, so the field is released, not declared empty")
	require.Contains(t, owned, "f:finishedAt", "the fields this apply did send stay owned")
}

// TestAssociativeListOwnershipIsPerEntry records the limit of the fix above,
// because it is surprising and because the rest of this package's design
// depends on knowing it.
//
// status.indexerOutcomes and status.grabbed are listType=map. Server-side
// apply tracks an associative list per ENTRY -- ownership is recorded as
// k:{"name":"idx"} under the field -- so an empty list owns nothing, and
// declaring `[]` is indistinguishable from omitting the field in both the
// resulting object and the ownership record. (The generated apply
// configuration omits an empty list anyway; see the test above.)
//
// Which is why Worker.writeFailure re-declares the existing outcomes rather
// than trusting the wire form, and why Reconciler.newStatusUpdate copies the
// live status.grabbed into every update.
func TestAssociativeListOwnershipIsPerEntry(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	const ns, name = "search-assoc", "srch-assoc"
	newNamespace(t, ctx, c, ns)

	require.NoError(t, c.Create(ctx, &catalogv1alpha1.Search{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: catalogv1alpha1.SearchSpec{
			MediaRef: &commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "the-matrix"},
		},
	}))
	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrWorker,
		catalogac.Search(name, ns).WithStatus(
			catalogac.SearchStatus().
				WithFinishedAt(metav1.Now()).
				WithIndexerOutcomes(catalogac.IndexerOutcome().WithName("idx").WithState(catalogv1alpha1.IndexerOutcomeOK)).
				WithResults(approvedResult("g1"))))
	require.NoError(t, err)

	got := &catalogv1alpha1.Search{}
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, got))
	owned := statusFieldsOwnedBy(t, got, string(k8s.ManagerCatalogarrWorker))
	entries, ok := owned["f:indexerOutcomes"].(map[string]any)
	require.True(t, ok, "a non-empty associative list is owned by entry")
	require.Contains(t, entries, `k:{"name":"idx"}`)

	// Declaring the list empty removes the entry AND the ownership record --
	// exactly as omitting the field would have done.
	_, err = k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrWorker,
		catalogac.Search(name, ns).WithStatus(
			catalogac.SearchStatus().
				WithFinishedAt(metav1.Now()).
				WithIndexerOutcomes().
				WithResults(approvedResult("g1"))))
	require.NoError(t, err)

	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, got))
	require.Empty(t, got.Status.IndexerOutcomes)
	require.NotContains(t, statusFieldsOwnedBy(t, got, string(k8s.ManagerCatalogarrWorker)), "f:indexerOutcomes",
		"an empty associative list owns nothing; only re-declaring its contents preserves them")
}

// TestSearchApplyConfigurationClaimsOnlyWhatItSets: a builder that never
// mentions a field has to leave it alone. Without omitempty an untouched field
// would go out as `"results": null` and claim ownership of something this
// caller does not own.
func TestSearchApplyConfigurationClaimsOnlyWhatItSets(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	const ns, name = "search-ownership-scope", "srch-scope"
	newNamespace(t, ctx, c, ns)

	require.NoError(t, c.Create(ctx, &catalogv1alpha1.Search{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: catalogv1alpha1.SearchSpec{
			MediaRef: &commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "the-matrix"},
		},
	}))

	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrWorker,
		catalogac.Search(name, ns).WithStatus(
			catalogac.SearchStatus().WithFinishedAt(metav1.Now())))
	require.NoError(t, err)

	got := &catalogv1alpha1.Search{}
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, got))
	owned := statusFieldsOwnedBy(t, got, string(k8s.ManagerCatalogarrWorker))
	require.Contains(t, owned, "f:finishedAt")
	require.NotContains(t, owned, "f:results",
		"a builder that never calls WithResults must not claim the field")
	require.NotContains(t, owned, "f:indexerOutcomes")
	require.NotContains(t, owned, "f:grabbed")
	require.Nil(t, got.Status.Results, "an unclaimed list stays absent, not []")
}

// TestReconcilerDoesNotClaimWorkerFieldsOnAMediaRefSearch guards the split
// runQuery introduced: Reconciler.apply only declares
// results/indexerOutcomes/finishedAt when s.Spec.Query != nil (see
// newStatusUpdate and apply). A mediaRef-mode Search must never see
// k8s.ManagerCatalogarr -- the reconciler's own manager -- claim any of the
// three; they stay k8s.ManagerCatalogarrWorker's alone. Only
// metadata.managedFields can show an over-claim; every value assertion
// elsewhere in this package would still pass even if this one regressed
// (CLAUDE.md's "an over-claim is silent").
func TestReconcilerDoesNotClaimWorkerFieldsOnAMediaRefSearch(t *testing.T) {
	f := newFixture(t, "search-own-mediaref")
	f.createSearch(t, "srch", catalogv1alpha1.SearchSpec{
		MediaRef: movieRef("the-matrix"), TTL: metav1.Duration{Duration: time.Hour},
	})

	f.reconcile(t, "srch")

	got := f.get(t, "srch")
	owned := statusFieldsOwnedBy(t, got, string(k8s.ManagerCatalogarr))
	require.Contains(t, owned, "f:phase", "the reconciler does own phase")
	require.NotContains(t, owned, "f:results")
	require.NotContains(t, owned, "f:indexerOutcomes")
	require.NotContains(t, owned, "f:finishedAt")
}

// TestReconcilerClaimsResultFieldsOnAQueryModeSearch is the other half: a
// query-mode Search has no worker, so k8s.ManagerCatalogarr must own all
// three once runQuery lands Completed.
func TestReconcilerClaimsResultFieldsOnAQueryModeSearch(t *testing.T) {
	f := newFixture(t, "search-own-query")
	f.r.Query = &search.FakeQueryRPC{Response: schema.QueryResponse{
		Releases: []schema.Release{{Info: commonv1.ReleaseInfo{GUID: "g1"}}},
	}}
	q := "the matrix"
	f.createSearch(t, "srch", catalogv1alpha1.SearchSpec{Query: &q, TTL: metav1.Duration{Duration: time.Hour}})

	f.reconcile(t, "srch")

	got := f.get(t, "srch")
	owned := statusFieldsOwnedBy(t, got, string(k8s.ManagerCatalogarr))
	require.Contains(t, owned, "f:results")
	require.Contains(t, owned, "f:indexerOutcomes")
	require.Contains(t, owned, "f:finishedAt")
}
