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

package metadata_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	metav1ac "k8s.io/client-go/applyconfigurations/meta/v1"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/metadata"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/k8s"
)

func TestSetupBuildsAndStartsTheGatewayWithNoProvidersConfigured(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	bus := membus.New(clockwork.NewRealClock())
	t.Cleanup(func() { _ = bus.Close() })
	require.NoError(t, bus.Ensure(ctx, events.Default().ForSingleNode()))

	stop, err := metadata.Setup(ctx, metadata.Options{Client: c, Bus: bus, HTTPClient: http.DefaultClient})
	require.NoError(t, err)
	require.NotNil(t, stop)
	stop()
}

func TestSetupRejectsMissingRequiredOptions(t *testing.T) {
	_, err := metadata.Setup(context.Background(), metadata.Options{})
	require.Error(t, err)
}

// TestTwoManagerSSASplitDoesNotClobberEitherSide mirrors
// pkg/k8s/patch_envtest_test.go's TestPatchStatusTwoManagersDoNotClobber for
// Movie: catalogarr (a different task's controller) owns status.phase and
// status.conditions; this task's catalogarr-worker owns status.metadata.
// Applying one must never erase the other -- this is the specific envtest
// proof the task brief calls for.
func TestTwoManagerSSASplitDoesNotClobberEitherSide(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	const ns, name = "noclobber", "inception"
	newMovie(t, ctx, c, ns, name, 27205)

	// The item controller (a different task) sets phase and conditions.
	controllerAC := catalogac.Movie(name, ns).WithStatus(
		catalogac.MovieStatus().
			WithPhase(catalogv1alpha1.MoviePhaseUnavailable).
			WithConditions(metav1ac.Condition().
				WithType(catalogv1alpha1.MovieConditionMetadataReady).
				WithStatus(metav1.ConditionFalse).
				WithReason("Pending").
				WithMessage("metadata not yet fetched").
				WithLastTransitionTime(metav1.Now())),
	)
	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarr, controllerAC)
	require.NoError(t, err)

	// The gateway patches status.metadata only, under its own field manager.
	workerAC := catalogac.Movie(name, ns).WithStatus(
		catalogac.MovieStatus().WithMetadata(
			catalogac.MovieMetadata().WithTitle("Inception").WithRuntimeMinutes(148)),
	)
	_, err = k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrMetadata, workerAC)
	require.NoError(t, err)

	var got catalogv1alpha1.Movie
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got))
	require.Equal(t, catalogv1alpha1.MoviePhaseUnavailable, got.Status.Phase, "the worker's apply erased the controller's phase")
	require.Len(t, got.Status.Conditions, 1, "the worker's apply erased the controller's conditions")
	require.NotNil(t, got.Status.Metadata)
	require.Equal(t, "Inception", got.Status.Metadata.Title)

	// The controller re-applies (e.g. it observed status.metadata changed
	// and flips MetadataReady) -- the worker's metadata must survive.
	controllerAC2 := catalogac.Movie(name, ns).WithStatus(
		catalogac.MovieStatus().
			WithPhase(catalogv1alpha1.MoviePhaseWanted).
			WithConditions(metav1ac.Condition().
				WithType(catalogv1alpha1.MovieConditionMetadataReady).
				WithStatus(metav1.ConditionTrue).
				WithReason("Fetched").
				WithMessage("metadata fetched").
				WithLastTransitionTime(metav1.Now())),
	)
	_, err = k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarr, controllerAC2)
	require.NoError(t, err)

	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got))
	require.Equal(t, catalogv1alpha1.MoviePhaseWanted, got.Status.Phase)
	require.NotNil(t, got.Status.Metadata, "the controller's re-apply erased the worker's metadata")
	require.Equal(t, "Inception", got.Status.Metadata.Title)

	var sawCatalogarr, sawWorker bool
	for _, e := range got.ManagedFields {
		if e.Subresource != "status" {
			continue
		}
		sawCatalogarr = sawCatalogarr || e.Manager == string(k8s.ManagerCatalogarr)
		sawWorker = sawWorker || e.Manager == string(k8s.ManagerCatalogarrMetadata)
	}
	require.True(t, sawCatalogarr && sawWorker, "both field managers must own a status entry")
}
