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

package status_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	indexac "github.com/mediactl/clustarr/api/applyconfiguration/index/index/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/indexarr/status"
	"github.com/mediactl/clustarr/pkg/k8s"
)

func newTestClient(t *testing.T) client.Client {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{"../../config/crd/bases"},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() { _ = env.Stop() })

	c, err := client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
	require.NoError(t, err)
	return c
}

// This is the test the package exists for.
//
// Indexer.status has three writer paths. Server-side apply replaces a field
// manager's ownership set on every apply, so if the reconciler and the two
// worker paths did not have disjoint, completely-declared sets, each apply
// would release the others' fields -- and nothing would log it. Phase C hit
// that eight times; twice the fix was exactly this split.
//
// The object is driven to a populated steady state FIRST, by both managers,
// before either applies again. A test that starts from a blank object cannot
// observe a release, because there is nothing to release -- which is why this
// class of defect survived three reviews in the previous phase.
func TestTheTwoManagersDoNotReleaseEachOthersFields(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	const ns = "status-split"
	require.NoError(t, client.IgnoreAlreadyExists(
		c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})))

	idx := &indexv1alpha1.Indexer{
		ObjectMeta: metav1.ObjectMeta{Name: "split", Namespace: ns},
		Spec: indexv1alpha1.IndexerSpec{
			BaseURL: "http://fixture.invalid",
			Generic: &indexv1alpha1.GenericNewznab{Protocol: commonv1.ProtocolTorrent},
		},
	}
	require.NoError(t, c.Create(ctx, idx))

	// Steady state, half written by each manager.
	require.NoError(t, status.Patch(ctx, c, k8s.ManagerIndexarr, idx,
		func(ac *indexac.IndexerStatusApplyConfiguration) {
			ac.WithObservedGeneration(1).
				WithPrivacy("private").
				WithProtocol(commonv1.ProtocolTorrent).
				WithSessionSecretRef("split-session").
				WithCaps(indexac.Caps().WithModes(map[string][]string{"search": {"q"}}))
		}))

	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(idx), idx))
	now := metav1.NewTime(time.Now().Truncate(time.Second))
	require.NoError(t, status.Patch(ctx, c, k8s.ManagerIndexarrWorker, idx,
		func(ac *indexac.IndexerStatusApplyConfiguration) {
			ac.WithEscalationLevel(3).
				WithQueriesInWindow(7).
				WithGrabsInWindow(2).
				WithIndexedReleases(41).
				WithLastRssNewCount(5).
				WithLastRssAt(now).
				WithInitialFailureAt(now).
				WithLastFailureAt(now).
				WithLastFailure("boom")
		}))

	var seeded indexv1alpha1.Indexer
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(idx), &seeded))
	require.Equal(t, "private", seeded.Status.Privacy, "setup: controller half did not land")
	require.EqualValues(t, 41, seeded.Status.IndexedReleases, "setup: worker half did not land")

	// Now each manager applies again, changing only its own field. Neither
	// may disturb the other's half.
	require.NoError(t, status.Patch(ctx, c, k8s.ManagerIndexarr, &seeded,
		func(ac *indexac.IndexerStatusApplyConfiguration) { ac.WithObservedGeneration(2) }))

	var afterController indexv1alpha1.Indexer
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(idx), &afterController))
	assert.EqualValues(t, 3, afterController.Status.EscalationLevel, "the controller apply released escalationLevel")
	assert.EqualValues(t, 41, afterController.Status.IndexedReleases, "the controller apply released indexedReleases")
	assert.NotNil(t, afterController.Status.LastRssAt, "the controller apply released lastRssAt")
	assert.Equal(t, "boom", afterController.Status.LastFailure, "the controller apply released lastFailure")

	require.NoError(t, status.Patch(ctx, c, k8s.ManagerIndexarrWorker, &afterController,
		func(ac *indexac.IndexerStatusApplyConfiguration) { ac.WithQueriesInWindow(9) }))

	var afterWorker indexv1alpha1.Indexer
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(idx), &afterWorker))
	assert.Equal(t, "private", afterWorker.Status.Privacy, "the worker apply released privacy")
	assert.Equal(t, "split-session", afterWorker.Status.SessionSecretRef, "the worker apply released sessionSecretRef")
	assert.NotNil(t, afterWorker.Status.Caps, "the worker apply released caps")
	assert.EqualValues(t, 2, afterWorker.Status.ObservedGeneration, "the worker apply released observedGeneration")
	assert.EqualValues(t, 9, afterWorker.Status.QueriesInWindow, "the worker's own field did not update")

	// And the half a manager owns must survive ITS OWN re-apply. This is the
	// assertion the first version of this test lacked: it checked only that
	// each manager left the other's half alone, so a WorkerFields that
	// forgot one of its own ten fields passed cleanly. That is the same
	// shape as the Phase C test which drove a real steady state, triggered
	// the path, and still passed while watching the object be gutted --
	// because it asserted only that its own field had arrived.
	assert.EqualValues(t, 3, afterWorker.Status.EscalationLevel, "the worker apply released its own escalationLevel")
	assert.EqualValues(t, 41, afterWorker.Status.IndexedReleases, "the worker apply released its own indexedReleases")
	assert.EqualValues(t, 5, afterWorker.Status.LastRssNewCount, "the worker apply released its own lastRssNewCount")
	assert.EqualValues(t, 2, afterWorker.Status.GrabsInWindow, "the worker apply released its own grabsInWindow")
	assert.NotNil(t, afterWorker.Status.LastRssAt, "the worker apply released its own lastRssAt")
	assert.NotNil(t, afterWorker.Status.InitialFailureAt, "the worker apply released its own initialFailureAt")
	assert.NotNil(t, afterWorker.Status.LastFailureAt, "the worker apply released its own lastFailureAt")
	assert.Equal(t, "boom", afterWorker.Status.LastFailure, "the worker apply released its own lastFailure")

	// Same obligation on the controller half.
	assert.Equal(t, "private", afterController.Status.Privacy, "the controller apply released its own privacy")
	assert.Equal(t, "split-session", afterController.Status.SessionSecretRef, "the controller apply released its own sessionSecretRef")
	assert.NotNil(t, afterController.Status.Caps, "the controller apply released its own caps")
}

// A manager outside the split must be refused rather than allowed to claim
// fields that neither half accounts for.
func TestPatchRefusesAManagerOutsideTheSplit(t *testing.T) {
	err := status.Patch(context.Background(), nil, k8s.ManagerCatalogarr,
		&indexv1alpha1.Indexer{ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "y"}}, nil)
	require.ErrorContains(t, err, "owns no part of Indexer.status")
}
