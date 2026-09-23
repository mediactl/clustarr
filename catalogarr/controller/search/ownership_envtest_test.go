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

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/controller/search"
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

// TestResultsAreDeclaredEmptyRatherThanReleased pins the mechanism the
// pointer-to-slice list fields exist to provide, on status.results -- the one
// of the three that is an atomic list, and so the only one where the
// distinction is expressible at all.
//
// Under server-side apply a field a manager omits is RELEASED, so "this list
// is now empty" and "I have nothing to say about this list" must be different
// things on the wire. A plain []T with `omitempty` cannot express the first,
// because encoding/json omits on length rather than nil-ness, so an
// explicitly-emptied list silently degrades into the second.
func TestResultsAreDeclaredEmptyRatherThanReleased(t *testing.T) {
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
		search.Search(name, ns).WithStatus(
			search.SearchStatus().
				WithFinishedAt(metav1.Now()).
				WithIndexerOutcomes(catalogv1alpha1.IndexerOutcome{
					Name: "idx", State: catalogv1alpha1.IndexerOutcomeOK, Count: 1,
				}).
				WithResults(approvedResult("g1"))))
	require.NoError(t, err)

	got := &catalogv1alpha1.Search{}
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, got))
	require.Len(t, got.Status.Results, 1)
	require.Contains(t, statusFieldsOwnedBy(t, got, string(k8s.ManagerCatalogarrWorker)), "f:results")

	// The same manager applies again with nothing to put in the list, which is
	// what a terminal failure report carries.
	_, err = k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrWorker,
		search.Search(name, ns).WithStatus(
			search.SearchStatus().
				WithFinishedAt(metav1.Now()).
				WithIndexerOutcomes().
				WithResults()))
	require.NoError(t, err)

	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, got))
	require.Contains(t, statusFieldsOwnedBy(t, got, string(k8s.ManagerCatalogarrWorker)), "f:results",
		"an empty atomic list must still be DECLARED; omitting it releases the field instead")
	require.NotNil(t, got.Status.Results, "a declared-empty list is [], not absent")
	require.Empty(t, got.Status.Results)
}

// TestAssociativeListOwnershipIsPerEntry records the limit of the fix above,
// because it is surprising and because the rest of this package's design
// depends on knowing it.
//
// status.indexerOutcomes and status.grabbed are listType=map. Server-side
// apply tracks an associative list per ENTRY -- ownership is recorded as
// k:{"name":"idx"} under the field -- so an empty list owns nothing, and
// declaring `[]` is indistinguishable from omitting the field in both the
// resulting object and the ownership record. The pointer shape buys these two
// fields nothing.
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
		search.Search(name, ns).WithStatus(
			search.SearchStatus().
				WithFinishedAt(metav1.Now()).
				WithIndexerOutcomes(catalogv1alpha1.IndexerOutcome{
					Name: "idx", State: catalogv1alpha1.IndexerOutcomeOK,
				}).
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
		search.Search(name, ns).WithStatus(
			search.SearchStatus().
				WithFinishedAt(metav1.Now()).
				WithIndexerOutcomes().
				WithResults(approvedResult("g1"))))
	require.NoError(t, err)

	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, got))
	require.Empty(t, got.Status.IndexerOutcomes)
	require.NotContains(t, statusFieldsOwnedBy(t, got, string(k8s.ManagerCatalogarrWorker)), "f:indexerOutcomes",
		"an empty associative list owns nothing; only re-declaring its contents preserves them")
}

// TestSearchApplyConfigurationClaimsOnlyWhatItSets is the other half of the
// pointer change: making an empty list declarable must not make every builder
// claim every list. A builder that never mentions a field has to leave it
// alone, which is why `omitempty` stays on the pointer rather than being
// dropped -- without it, an untouched field would go out as
// `"results": null` and claim ownership of something this caller does not own.
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
		search.Search(name, ns).WithStatus(
			search.SearchStatus().WithFinishedAt(metav1.Now())))
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
