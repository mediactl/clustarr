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
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/grab/lifecycle"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// TestAnOverridePickIsSentPastABlocklistedTombstone is the smoke test's T2
// finding at the Search: the owner keeps a release's Blocklisted grab as a
// tombstone for lifecycle.BlockQuarantine, and the controller read that
// tombstone as the pick's grab -- "Grabbed", naming the blocklisted entry --
// so a spec.override pick of a quarantined release was never sent. A
// tombstone is no grab of the pick: the override is sent to the owner (whose
// planner admits it as a new entry), and the Search names that entry once
// the owner holds it.
func TestAnOverridePickIsSentPastABlocklistedTombstone(t *testing.T) {
	ctx := context.Background()
	f := newRecFixture(t, "search-override-tombstone")
	m := f.heat(t, "", false)
	f.createSearch(t, "srch", catalogv1alpha1.SearchSpec{MediaRef: movieRef("heat"), Override: true, TTL: metav1.Duration{Duration: time.Hour}})
	f.reconcile(t, "srch")
	cands := agentAnswer(t, heatTarget(), "Heat.1995.1080p.BluRay.x264-GRP")
	cands[0].Blocked = &schema.ReleaseBlock{Scope: schema.BlockScopeGlobal, Reason: "importRejected"}
	f.answer(t, f.get(t, "srch"), f.clock.Now(), cands)
	f.reconcile(t, "srch") // incorporate
	f.reconcile(t, "srch") // complete
	got := f.get(t, "srch")
	require.Equal(t, catalogv1alpha1.SearchPhaseCompleted, got.Status.Phase)
	require.True(t, hasReason(got.Status.Results[0], decision.ReasonBlocklisted), "%+v", got.Status.Results[0].Rejections)
	guid := got.Status.Results[0].GUID

	entry := func(id, uid string, phase commonv1.DownloadPhase) catalogv1alpha1.DownloadEntry {
		at := metav1.NewTime(f.clock.Now().Add(-time.Hour))
		e := catalogv1alpha1.DownloadEntry{
			ID: id, UID: uid,
			Release: catalogv1alpha1.DownloadRelease{Title: cands[0].Decision.Title, GUID: guid, IndexerRef: "nzbgeek", Protocol: commonv1.ProtocolTorrent},
			Source:  commonv1.DownloadSource{IndexerDownload: &commonv1.IndexerDownload{IndexerRef: "nzbgeek", GUID: guid}},
			Client:  "torrent", GrabbedBy: commonv1.GrabSourceSearch, GrabbedAt: at,
			Phase: phase, RemoveOnImport: true, RemoveDataOnDelete: true,
			Dispatch: catalogv1alpha1.Dispatch{Seq: 1, DispatchedAt: at},
		}
		if phase == commonv1.DownloadPhaseBlocklisted {
			confirmed := metav1.NewTime(f.clock.Now().Add(-10 * time.Minute))
			e.FailureReason = commonv1.DownloadFailureImportRejected
			e.Block = &catalogv1alpha1.EntryBlock{
				Scope: catalogv1alpha1.BlockScopeItem, Reason: commonv1.DownloadFailureImportRejected, Seq: 1, ConfirmedAt: &confirmed,
			}
		}
		return e
	}
	tomb := entry(lifecycle.EntryID("heat", "", guid), "uid-tomb", commonv1.DownloadPhaseBlocklisted)
	seedStatus(t, f.c, m, k8s.ManagerCatalogGrab, map[string]any{"downloads": []any{toMap(t, &tomb)}})

	patch := client.MergeFrom(got.DeepCopy())
	got.Spec.Grab = []string{guid}
	require.NoError(t, f.c.Patch(ctx, got, patch))
	f.reconcile(t, "srch")

	got = f.get(t, "srch")
	require.Len(t, got.Status.Grabbed, 1)
	require.NotEqual(t, tomb.ID, got.Status.Grabbed[0].DownloadRef, "a tombstone is not the pick's grab")
	sent := f.candidates(t)
	require.Len(t, sent, 1, "the override pick is sent to the owner: %+v", got.Status.Grabbed)
	require.True(t, sent[0].Override)
	require.True(t, sent[0].Manual)
	require.Equal(t, guid, sent[0].Release.Decision.GUID)

	// The owner admitted it beside the tombstone: the Search names it.
	grab := entry(lifecycle.OverrideEntryID(tomb.ID, tomb.UID), "uid-override", commonv1.DownloadPhasePending)
	seedStatus(t, f.c, m, k8s.ManagerCatalogGrab, map[string]any{"downloads": []any{toMap(t, &tomb), toMap(t, &grab)}})
	f.reconcile(t, "srch")
	got = f.get(t, "srch")
	require.Len(t, got.Status.Grabbed, 1)
	require.Equal(t, grab.ID, got.Status.Grabbed[0].DownloadRef)
	require.Len(t, f.candidates(t), 1, "sent once")
}
