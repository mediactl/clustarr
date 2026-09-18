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
	"testing"

	"k8s.io/utils/ptr"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/controller/search"
	"github.com/mediactl/clustarr/pkg/k8s"
)

func TestSearchApplyConfigurationRoundTripsThroughPatchStatus(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	const ns, name = "search-ac", "srch-1"
	newNamespace(t, ctx, c, ns)

	s := &catalogv1alpha1.Search{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: catalogv1alpha1.SearchSpec{
			MediaRef: &commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "the-matrix"},
		},
	}
	require.NoError(t, c.Create(ctx, s))

	seeders := int32(42)
	ac := search.Search(name, ns).WithStatus(
		search.SearchStatus().
			WithPhase(catalogv1alpha1.SearchPhaseCompleted).
			WithObservedGeneration(1).
			WithFinishedAt(metav1.Now()).
			WithIndexerOutcomes(catalogv1alpha1.IndexerOutcome{
				Name: "idx", State: catalogv1alpha1.IndexerOutcomeOK, Count: 1,
			}).
			WithResults(catalogv1alpha1.ReleaseDecision{
				ReleaseInfo: commonv1.ReleaseInfo{
					GUID:        "g1",
					Title:       "The.Matrix.1999.1080p.BluRay.x264-GROUP",
					Seeders:     &seeders,
					PublishedAt: ptr.To(metav1.Now()),
				},
				Approved: true,
				Rank:     1,
			}),
	)
	applied, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarr, ac)
	require.NoError(t, err)
	require.NotNil(t, applied.Status)
	require.NotNil(t, applied.Status.Phase)
	require.Equal(t, catalogv1alpha1.SearchPhaseCompleted, *applied.Status.Phase)

	got := &catalogv1alpha1.Search{}
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, got))
	require.Equal(t, catalogv1alpha1.SearchPhaseCompleted, got.Status.Phase)
	require.Len(t, got.Status.Results, 1)
	require.Equal(t, "g1", got.Status.Results[0].GUID)
	require.Equal(t, int32(1), got.Status.Results[0].Rank)
	require.NotNil(t, got.Status.Results[0].Seeders)
	require.Equal(t, int32(42), *got.Status.Results[0].Seeders)
	require.Len(t, got.Status.IndexerOutcomes, 1)
	require.NotNil(t, got.Spec.MediaRef, "spec was touched")
	require.Equal(t, "the-matrix", got.Spec.MediaRef.Name)
}

// TestSearchApplyConfigurationReleasesOmittedFields pins the server-side-apply
// behaviour every status write in this package has to respect: a manager's
// ownership set is REPLACED on each apply, so a field it sent last time and
// omits this time is released, which reads as "reset to zero" on the object.
// The object is driven to a full steady state first; a blank object could not
// observe a release at all.
func TestSearchApplyConfigurationReleasesOmittedFields(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	const ns, name = "search-ac-release", "srch-2"
	newNamespace(t, ctx, c, ns)

	require.NoError(t, c.Create(ctx, &catalogv1alpha1.Search{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: catalogv1alpha1.SearchSpec{
			MediaRef: &commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "the-matrix"},
		},
	}))

	full := search.Search(name, ns).WithStatus(
		search.SearchStatus().
			WithPhase(catalogv1alpha1.SearchPhaseCompleted).
			WithStartedAt(metav1.Now()).
			WithResults(catalogv1alpha1.ReleaseDecision{
				ReleaseInfo: commonv1.ReleaseInfo{GUID: "g1", PublishedAt: ptr.To(metav1.Now())},
				Approved:    true,
				Rank:        1,
			}),
	)
	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarr, full)
	require.NoError(t, err)

	// A partial apply that keeps only the phase.
	partial := search.Search(name, ns).WithStatus(
		search.SearchStatus().WithPhase(catalogv1alpha1.SearchPhaseFailed),
	)
	_, err = k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarr, partial)
	require.NoError(t, err)

	got := &catalogv1alpha1.Search{}
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, got))
	require.Equal(t, catalogv1alpha1.SearchPhaseFailed, got.Status.Phase)
	require.Empty(t, got.Status.Results, "omitting results released them: every apply must be a complete declaration")
	require.Nil(t, got.Status.StartedAt, "omitting startedAt released it")
}
