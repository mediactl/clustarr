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

// A double-claim against a field another manager owns does NOT surface as an
// apiserver conflict: pkg/k8s.PatchStatus and pkg/k8s.Apply both force
// ownership unconditionally, so an over-claim is silent everywhere except
// metadata.managedFields -- an object-VALUE assertion cannot see it (CLAUDE.md,
// "A double-claim will NOT surface as an apiserver conflict"). This file reads
// managedFields directly, which is the only place this class of bug is
// visible at all.
package downloadclient_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/app/grab/controller/downloadclient"
	"github.com/mediactl/clustarr/pkg/fsops"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// managersOf returns the set of field-manager names that appear anywhere in
// obj's managedFields, restricted to entries touching the named subresource
// ("" for the main resource, "status" for status).
func managersOf(t *testing.T, entries []metav1.ManagedFieldsEntry, subresource string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, e := range entries {
		if e.Subresource != subresource {
			continue
		}
		out[e.Manager] = true
	}
	return out
}

// TestDownloadClientStatusIsOwnedOnlyByManagerGrabarr guards D2-3's own claim:
// DownloadClientStatus has exactly one legitimate writer (k8s.ManagerGrabarr;
// see app/grab/status.go's doc comment, which covers Download.status, not
// DownloadClient.status -- DownloadClient has no split at all). A future
// change that routed some DownloadClientStatus field through a different
// manager -- k8s.ManagerGrabarrEngine, say, by copy-pasting a pattern from
// Download.status -- would not fail any value assertion, because
// ForceOwnership always wins the value. It would only show up here.
func TestDownloadClientStatusIsOwnedOnlyByManagerGrabarr(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)

	dc := &downloadv1alpha1.DownloadClient{
		ObjectMeta: metav1.ObjectMeta{Name: "managed", Namespace: "default"},
		Spec: downloadv1alpha1.DownloadClientSpec{
			Protocol: commonv1alpha1.ProtocolTorrent,
			Replicas: 1,
			Torrent:  &downloadv1alpha1.TorrentSpec{},
		},
	}
	require.NoError(t, c.Create(ctx, dc))

	r := downloadclient.NewReconciler(c, events.NewFakeRecorder(10), "/data", "/scratch", "img")
	r.DiskUsage = func(string) (fsops.Usage, error) {
		return fsops.Usage{Total: 100 << 30, Free: 50 << 30}, nil
	}
	reconcileOK(t, r, "default", "managed")

	var got downloadv1alpha1.DownloadClient
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "managed"}, &got))

	statusManagers := managersOf(t, got.ManagedFields, "status")
	assert.Equal(t, map[string]bool{k8s.ManagerGrabarr.String(): true}, statusManagers,
		"DownloadClient.status must be owned by k8s.ManagerGrabarr alone")
}

// TestBlocklistSweeperClaimsNoDownloadStatusField guards the opposite
// direction: the sweep must never become a status writer on Download at all.
// It deletes the object outright (see blocklist.go's doc comment for why), so
// this asserts there is no "status" managedFields entry for k8s.ManagerGrabarr
// or k8s.ManagerGrabarrEngine produced by the sweeper's own reconcile -- the
// Patch calls in this test's helpers are the fixture simulating a real
// controller/engine writer, not the sweeper under test.
func TestBlocklistSweeperClaimsNoDownloadStatusField(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)

	d := mkBlocklistedDownload(t, ctx, c, "managed-sweep")
	before := len(d.ManagedFields)

	s := downloadclient.NewBlocklistSweeper(c, events.NewFakeRecorder(10))
	// blocklistedUntil is unset, so the sweeper reconciles and does nothing --
	// exactly the path that would be tempted to "helpfully" patch status if
	// it were going to at all.
	_, err := s.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: "default", Name: "managed-sweep"}})
	require.NoError(t, err)

	var got downloadv1alpha1.Download
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "managed-sweep"}, &got))
	assert.Len(t, got.ManagedFields, before, "the sweeper must not add any managedFields entry when it takes no action")

	// Even the fixture's own writes (the create, plus nothing else here --
	// blocklistedUntil was deliberately left unset) never attribute a status
	// entry to a manager named "downloadclient" or "downloadclient-blocklist":
	// the sweeper has no field-manager identity at all, because it never
	// calls k8s.PatchStatus or k8s.Apply.
	statusManagers := managersOf(t, got.ManagedFields, "status")
	assert.NotContains(t, statusManagers, "downloadclient")
	assert.NotContains(t, statusManagers, "downloadclient-blocklist")
}
