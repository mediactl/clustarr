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

package lifecycle_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/app/grab/lifecycle"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// importedSummary is an entry's summary once lifecycle recorded the import
// (toImported stamps importedAt).
func importedSummary(files int32) *catalogv1alpha1.DownloadImportSummary {
	t := metav1.NewTime(now.Add(-time.Hour))
	return &catalogv1alpha1.DownloadImportSummary{Phase: catalogv1alpha1.ImportPhaseImported, ImportedAt: &t, Files: files}
}

// placedDecision is importplan's decision once the execute placed files:
// imported, with no importedAt until every MediaFile names the entry.
func placedDecision(files int32) lifecycle.ImportDecision {
	return lifecycle.ImportDecision{
		Summary: catalogv1alpha1.DownloadImportSummary{Phase: catalogv1alpha1.ImportPhaseImported, Files: files},
		Verdict: lifecycle.ImportImported,
	}
}

// next is the pass after p: its entries stored, its book kept, a second on.
func next(v lifecycle.View, p lifecycle.Plan) lifecycle.View {
	v.Stored = p.Entries
	v.NewEntries = nil
	v.Issued = p.Issued
	v.Nonces = p.Nonces
	v.Now = v.Now.Add(time.Second)
	return v
}

func failedRecord(e catalogv1alpha1.DownloadEntry, reason commonv1.DownloadFailureReason) lifecycle.Transfer {
	t := record(e, seq0, commonv1.DownloadStageTransferring)
	t.Record.State = schema.TransferStateFailed
	t.Record.EngineFailureReason = reason
	return t
}

func removedRecord(e catalogv1alpha1.DownloadEntry) lifecycle.Transfer {
	t := record(e, e.Dispatch.Seq, e.Stage)
	t.Record.State = schema.TransferStateRemoved
	return t
}

// TestEveryRowOfTheStateMachine walks §6.4's table one row per case, each
// from an owner already in its steady state.
func TestEveryRowOfTheStateMachine(t *testing.T) {
	for _, tc := range []struct {
		name  string
		view  func() lifecycle.View
		check func(t *testing.T, p lifecycle.Plan)
		// nextCheck, when set, checks the pass after: the effects a
		// transition owes once its status is applied.
		nextCheck func(t *testing.T, p lifecycle.Plan)
	}{{
		name: "a chosen candidate waits Pending while no engine is ready",
		view: func() lifecycle.View {
			v := view(nil)
			v.Engines = map[string]lifecycle.Engine{}
			e := entry("")
			e.Client, e.Engine, e.Dispatch = "", "", catalogv1alpha1.Dispatch{}
			v.NewEntries = []catalogv1alpha1.DownloadEntry{e}
			return v
		},
		check: func(t *testing.T, p lifecycle.Plan) {
			e := only(p)
			require.NotNil(t, e)
			assert.Equal(t, commonv1.DownloadPhasePending, e.Phase)
			assert.Empty(t, e.Engine, "nothing is pinned before an engine is ready")
			require.NotNil(t, e.Dispatch.Delivery)
			assert.Equal(t, catalogv1alpha1.DeliveryReasonEngineNotReady, e.Dispatch.Delivery.Reason)
			assert.Contains(t, e.Message, "qbt")
			assert.Empty(t, p.Commands)
			require.NotNil(t, p.Finalizer)
			assert.True(t, *p.Finalizer, "the finalizer holds while an entry exists")
		},
	}, {
		name: "Pending -> Assigned: client and engine pinned, present at a new seq, grabbed, a direct grab counted",
		view: func() lifecycle.View {
			v := view(nil)
			e := entry("")
			e.Client, e.Engine, e.Dispatch = "", "", catalogv1alpha1.Dispatch{}
			m := "magnet:?xt=urn:btih:" + infoHash
			e.Source = commonv1.DownloadSource{MagnetURL: &m}
			v.NewEntries = []catalogv1alpha1.DownloadEntry{e}
			return v
		},
		check: func(t *testing.T, p lifecycle.Plan) {
			e := only(p)
			require.NotNil(t, e)
			assert.Equal(t, commonv1.DownloadPhaseAssigned, e.Phase)
			assert.Equal(t, "qbt", e.Client)
			assert.Equal(t, "qbt-0", e.Engine)
			require.Len(t, p.Commands, 1)
			c := p.Commands[0]
			assert.Equal(t, schema.EngineDesiredPresent, c.Cmd.Desired)
			assert.GreaterOrEqual(t, c.Cmd.Seq, now.UnixMilli(), "a seq never below the clock's millisecond")
			assert.Equal(t, e.Dispatch.Seq, c.Cmd.Seq, "status carries the seq the command does")
			assert.Equal(t, entryUID, c.Cmd.Claim.Entry.UID)
			assert.Contains(t, actions(p), lifecycle.ActionGrabbed)
			require.Len(t, p.DirectGrabs, 1, "a magnet from an indexer is fetched without rpc.index.download: counted here")
		},
	}, {
		name: "Pending -> Assigned: an indexer download is counted by the index agent, not here",
		view: func() lifecycle.View {
			v := view(nil)
			e := entry("")
			e.Client, e.Engine, e.Dispatch = "", "", catalogv1alpha1.Dispatch{}
			v.NewEntries = []catalogv1alpha1.DownloadEntry{e}
			return v
		},
		check: func(t *testing.T, p lifecycle.Plan) {
			assert.Equal(t, commonv1.DownloadPhaseAssigned, only(p).Phase)
			assert.Empty(t, p.DirectGrabs)
		},
	}, {
		name: "Assigned -> Queued on fetchingMetadata",
		view: func() lifecycle.View {
			e := entry(commonv1.DownloadPhaseAssigned)
			return view([]catalogv1alpha1.DownloadEntry{e}, record(e, seq0, commonv1.DownloadStageFetchingMetadata))
		},
		check: func(t *testing.T, p lifecycle.Plan) {
			assert.Equal(t, commonv1.DownloadPhaseQueued, only(p).Phase)
			assert.Empty(t, p.Commands, "a steady entry owes nothing")
		},
	}, {
		name: "Queued -> Downloading on transferring",
		view: func() lifecycle.View {
			e := entry(commonv1.DownloadPhaseQueued)
			return view([]catalogv1alpha1.DownloadEntry{e}, record(e, seq0, commonv1.DownloadStageTransferring))
		},
		check: func(t *testing.T, p lifecycle.Plan) {
			assert.Equal(t, commonv1.DownloadPhaseDownloading, only(p).Phase)
			assert.Equal(t, commonv1.DownloadPhaseDownloading, p.Phase, "the owner's downloadPhase is the active entry's")
			assert.Empty(t, p.Commands)
		},
	}, {
		name: "any live -> Paused on the user's pause: present,paused at a new seq",
		view: func() lifecycle.View {
			e := entry(commonv1.DownloadPhaseDownloading)
			v := view([]catalogv1alpha1.DownloadEntry{e}, record(e, seq0, commonv1.DownloadStageTransferring))
			v.Intents = lifecycle.ParseIntents(map[string]string{catalogv1alpha1.AnnotationDownloadPaused: entryID})
			return v
		},
		check: func(t *testing.T, p lifecycle.Plan) {
			assert.Equal(t, commonv1.DownloadPhasePaused, only(p).Phase)
			require.Len(t, p.Commands, 1)
			assert.True(t, p.Commands[0].Cmd.Paused)
			assert.Greater(t, p.Commands[0].Cmd.Seq, seq0)
		},
	}, {
		name: "Downloading -> Paused on healthPaused, the message naming the floor",
		view: func() lifecycle.View {
			e := usenetEntry(commonv1.DownloadPhaseDownloading)
			tr := record(e, seq0, commonv1.DownloadStageTransferring)
			tr.Record.HealthPaused = true
			tr.Record.Health = &schema.TransferHealth{HealthPercent: 88, CriticalHealthPercent: 95}
			return view([]catalogv1alpha1.DownloadEntry{e}, tr)
		},
		check: func(t *testing.T, p lifecycle.Plan) {
			e := only(p)
			assert.Equal(t, commonv1.DownloadPhasePaused, e.Phase)
			assert.Contains(t, e.Message, "88%")
			assert.Contains(t, e.Message, "95%")
			assert.Empty(t, p.Commands, "nothing until the user resumes")
		},
	}, {
		name: "Downloading -> Completed on done: the import inspect is dispatched",
		view: func() lifecycle.View {
			e := entry(commonv1.DownloadPhaseDownloading)
			v := view([]catalogv1alpha1.DownloadEntry{e}, record(e, seq0, commonv1.DownloadStageDone))
			v.Import = map[types.UID]lifecycle.ImportDecision{entryUID: {
				Summary:  catalogv1alpha1.DownloadImportSummary{Phase: catalogv1alpha1.ImportPhasePending},
				Dispatch: &lifecycle.ImportDispatch{Phase: schema.ImportSubInspect, Seq: 5},
				Verdict:  lifecycle.ImportNone,
			}}
			return v
		},
		check: func(t *testing.T, p lifecycle.Plan) {
			e := only(p)
			assert.Equal(t, commonv1.DownloadPhaseCompleted, e.Phase)
			assert.NotNil(t, e.CompletedAt)
			assert.Contains(t, actions(p), lifecycle.ActionCompleted)
			require.Len(t, p.Imports, 1)
			assert.Equal(t, schema.ImportSubInspect, p.Imports[0].Phase)
			assert.Equal(t, types.UID(entryUID), p.Imports[0].EntryUID)
		},
	}, {
		name: "Completed stays Completed until every placed file has its MediaFile",
		view: func() lifecycle.View {
			e := entry(commonv1.DownloadPhaseCompleted)
			v := view([]catalogv1alpha1.DownloadEntry{e}, record(e, seq0, commonv1.DownloadStageSeeding))
			v.Import = map[types.UID]lifecycle.ImportDecision{entryUID: placedDecision(2)}
			v.Files[entryID] = 1
			return v
		},
		check: func(t *testing.T, p lifecycle.Plan) {
			assert.Equal(t, commonv1.DownloadPhaseCompleted, only(p).Phase)
			for _, c := range p.Commands {
				assert.False(t, c.Cmd.Imported, "no imported: true before the MediaFiles exist")
			}
		},
	}, {
		name: "Completed -> Seeding once the MediaFiles name the id: the command says imported",
		view: func() lifecycle.View {
			e := entry(commonv1.DownloadPhaseCompleted)
			v := view([]catalogv1alpha1.DownloadEntry{e}, record(e, seq0, commonv1.DownloadStageSeeding))
			v.Import = map[types.UID]lifecycle.ImportDecision{entryUID: placedDecision(1)}
			v.Files[entryID] = 1
			return v
		},
		check: func(t *testing.T, p lifecycle.Plan) {
			assert.Equal(t, commonv1.DownloadPhaseSeeding, only(p).Phase)
			require.Len(t, p.Commands, 1)
			assert.True(t, p.Commands[0].Cmd.Imported)
			assert.Equal(t, schema.EngineDesiredPresent, p.Commands[0].Cmd.Desired)
			assert.Contains(t, actions(p), lifecycle.ActionImported)
		},
	}, {
		name: "Seeding -> Removing on the seed goal and removeCompleted: absent with removeDataOnDelete",
		view: func() lifecycle.View {
			e := entry(commonv1.DownloadPhaseSeeding)
			e.Import = importedSummary(1)
			tr := record(e, seq0, commonv1.DownloadStageSeeding)
			tr.Record.SeedGoalReached = true
			return view([]catalogv1alpha1.DownloadEntry{e}, tr)
		},
		check: func(t *testing.T, p lifecycle.Plan) {
			assert.Equal(t, commonv1.DownloadPhaseRemoving, only(p).Phase)
			require.Len(t, absents(p), 1)
			assert.True(t, absents(p)[0].Cmd.RemoveData)
		},
	}, {
		name: "Seeding stays Seeding when the client keeps completed transfers",
		view: func() lifecycle.View {
			e := entry(commonv1.DownloadPhaseSeeding)
			e.Import = importedSummary(1)
			tr := record(e, seq0, commonv1.DownloadStageSeeding)
			tr.Record.SeedGoalReached = true
			v := view([]catalogv1alpha1.DownloadEntry{e}, tr)
			v.Clients[0].RemoveCompleted = false
			steady(&v)
			return v
		},
		check: func(t *testing.T, p lifecycle.Plan) {
			assert.Equal(t, commonv1.DownloadPhaseSeeding, only(p).Phase)
			assert.Empty(t, absents(p))
		},
	}, {
		name: "Imported (usenet) -> Removing once the import is done",
		view: func() lifecycle.View {
			e := usenetEntry(commonv1.DownloadPhaseImported)
			e.Import = importedSummary(1)
			return view([]catalogv1alpha1.DownloadEntry{e}, record(e, seq0, commonv1.DownloadStageDone))
		},
		check: func(t *testing.T, p lifecycle.Plan) {
			assert.Equal(t, commonv1.DownloadPhaseRemoving, only(p).Phase)
			require.Len(t, absents(p), 1)
		},
	}, {
		name: "Completed -> Blocklisted on the import's release fault: item scope, absent, failed, a redownload",
		view: func() lifecycle.View {
			e := entry(commonv1.DownloadPhaseCompleted)
			v := view([]catalogv1alpha1.DownloadEntry{e}, record(e, seq0, commonv1.DownloadStageSeeding))
			v.Import = map[types.UID]lifecycle.ImportDecision{entryUID: {
				Summary: catalogv1alpha1.DownloadImportSummary{Phase: catalogv1alpha1.ImportPhaseBlocked, Message: "every candidate file was rejected"},
				Verdict: lifecycle.ImportReleaseFault,
			}}
			return v
		},
		check: func(t *testing.T, p lifecycle.Plan) {
			e := only(p)
			assert.Equal(t, commonv1.DownloadPhaseBlocklisted, e.Phase)
			assert.Equal(t, commonv1.DownloadFailureImportRejected, e.FailureReason)
			require.NotNil(t, e.Block)
			assert.Equal(t, catalogv1alpha1.BlockScopeItem, e.Block.Scope)
			require.Len(t, absents(p), 1)
			assert.True(t, absents(p)[0].Cmd.RemoveData)
			assert.Contains(t, actions(p), lifecycle.ActionFailed)
		},
		nextCheck: func(t *testing.T, p lifecycle.Plan) {
			assert.Equal(t, []schema.ItemRef{owner()}, p.Redownloads, "R25: a redownload search while the tombstone stands")
			require.Len(t, p.Blocks, 1, "the block owed to the release index")
			assert.Equal(t, schema.BlockScopeOf(owner()), p.Blocks[0].Req.Scope)
			assert.Equal(t, only(p).Block.Seq, p.Blocks[0].Req.Seq)
		},
	}, {
		name: "Completed -> Failed importExpired: never blocklisted or searched again",
		view: func() lifecycle.View {
			e := entry(commonv1.DownloadPhaseCompleted)
			v := view([]catalogv1alpha1.DownloadEntry{e}, record(e, seq0, commonv1.DownloadStageSeeding))
			v.Import = map[types.UID]lifecycle.ImportDecision{entryUID: {
				Summary: catalogv1alpha1.DownloadImportSummary{Phase: catalogv1alpha1.ImportPhaseExpired},
				Verdict: lifecycle.ImportExpired,
			}}
			return v
		},
		check: func(t *testing.T, p lifecycle.Plan) {
			e := only(p)
			assert.Equal(t, commonv1.DownloadPhaseFailed, e.Phase)
			assert.Equal(t, commonv1.DownloadFailureImportExpired, e.FailureReason)
			assert.Nil(t, e.Block)
			require.Len(t, absents(p), 1)
		},
		nextCheck: func(t *testing.T, p lifecycle.Plan) {
			assert.Empty(t, p.Blocks)
			assert.Empty(t, p.Redownloads, "an expired hold is never searched again")
		},
	}, {
		name: "Downloading -> Blocklisted on missingArticles: a global block",
		view: func() lifecycle.View {
			e := usenetEntry(commonv1.DownloadPhaseDownloading)
			return view([]catalogv1alpha1.DownloadEntry{e}, failedRecord(e, commonv1.DownloadFailureMissingArticles))
		},
		check: func(t *testing.T, p lifecycle.Plan) {
			e := only(p)
			assert.Equal(t, commonv1.DownloadPhaseBlocklisted, e.Phase)
			assert.Equal(t, catalogv1alpha1.BlockScopeGlobal, e.Block.Scope)
			require.Len(t, absents(p), 1)
		},
		nextCheck: func(t *testing.T, p lifecycle.Plan) {
			require.Len(t, p.Blocks, 1)
			assert.Equal(t, schema.BlockScopeGlobal, p.Blocks[0].Req.Scope)
		},
	}, {
		name: "Queued -> Blocklisted on payloadMismatch: a global block",
		view: func() lifecycle.View {
			e := entry(commonv1.DownloadPhaseQueued)
			return view([]catalogv1alpha1.DownloadEntry{e}, failedRecord(e, commonv1.DownloadFailurePayloadMismatch))
		},
		check: func(t *testing.T, p lifecycle.Plan) {
			assert.Equal(t, commonv1.DownloadPhaseBlocklisted, only(p).Phase)
			assert.Equal(t, catalogv1alpha1.BlockScopeGlobal, only(p).Block.Scope)
		},
	}, {
		name: "Downloading -> Failed on a local fault: absent, no block, no redownload",
		view: func() lifecycle.View {
			e := entry(commonv1.DownloadPhaseDownloading)
			return view([]catalogv1alpha1.DownloadEntry{e}, failedRecord(e, commonv1.DownloadFailureDiskFull))
		},
		check: func(t *testing.T, p lifecycle.Plan) {
			e := only(p)
			assert.Equal(t, commonv1.DownloadPhaseFailed, e.Phase)
			assert.Equal(t, commonv1.DownloadFailureDiskFull, e.FailureReason)
			assert.Nil(t, e.Block)
			require.Len(t, absents(p), 1)
		},
		nextCheck: func(t *testing.T, p lifecycle.Plan) {
			assert.Empty(t, p.Blocks)
			assert.Empty(t, p.Redownloads, "a local fault is no evidence against the release (Radarr)")
		},
	}, {
		name: "Downloading -> Blocklisted stalled after the client's stallTimeout without progress",
		view: func() lifecycle.View {
			e := entry(commonv1.DownloadPhaseDownloading)
			tr := record(e, seq0, commonv1.DownloadStageTransferring)
			last := now.Add(-lifecycle.StallTimeout - time.Minute)
			tr.Record.LastProgressAt = &last
			return view([]catalogv1alpha1.DownloadEntry{e}, tr)
		},
		check: func(t *testing.T, p lifecycle.Plan) {
			e := only(p)
			assert.Equal(t, commonv1.DownloadPhaseBlocklisted, e.Phase)
			assert.Equal(t, commonv1.DownloadFailureStalled, e.FailureReason)
			assert.Equal(t, catalogv1alpha1.BlockScopeItem, e.Block.Scope)
		},
	}, {
		name: "usenet Downloading -> Blocklisted timeout past the opt-in downloadTimeout (R21)",
		view: func() lifecycle.View {
			e := usenetEntry(commonv1.DownloadPhaseDownloading)
			tr := record(e, seq0, commonv1.DownloadStageTransferring)
			started := now.Add(-7 * time.Hour)
			tr.Record.StartedAt = &started
			v := view([]catalogv1alpha1.DownloadEntry{e}, tr)
			v.Clients[1].DownloadTimeout = 6 * time.Hour
			steady(&v)
			return v
		},
		check: func(t *testing.T, p lifecycle.Plan) {
			assert.Equal(t, commonv1.DownloadPhaseBlocklisted, only(p).Phase)
			assert.Equal(t, commonv1.DownloadFailureTimeout, only(p).FailureReason)
			assert.Equal(t, catalogv1alpha1.BlockScopeItem, only(p).Block.Scope)
		},
	}, {
		name: "usenet Downloading -> Blocklisted stalled past the client's opt-in stallTimeout",
		view: func() lifecycle.View {
			return usenetStalled(false)
		},
		check: func(t *testing.T, p lifecycle.Plan) {
			assert.Equal(t, commonv1.DownloadPhaseBlocklisted, only(p).Phase)
			assert.Equal(t, commonv1.DownloadFailureStalled, only(p).FailureReason)
		},
	}, {
		name: "usenet Downloading waiting its turn is never stalled, however old its progress",
		view: func() lifecycle.View {
			return usenetStalled(true)
		},
		check: func(t *testing.T, p lifecycle.Plan) {
			for _, e := range p.Entries {
				assert.NotEqual(t, commonv1.DownloadPhaseBlocklisted, e.Phase)
			}
			assert.Empty(t, p.Blocks, "a transfer queued for a slot is the engine's wait, not the release's")
		},
	}, {
		// The engine holds most of its jobs for a transfer slot: on talos
		// (2026-10-28) 590 of them read Downloading beside the five that
		// moved bytes, and the dashboard's "Running downloads" listed queued
		// ones with no progress.
		name: "usenet transferring and waiting its turn -> Queued",
		view: func() lifecycle.View {
			e := usenetEntry(commonv1.DownloadPhaseDownloading)
			tr := record(e, seq0, commonv1.DownloadStageTransferring)
			tr.Record.WaitingTurn = true
			v := view([]catalogv1alpha1.DownloadEntry{e}, tr)
			steady(&v)
			return v
		},
		check: func(t *testing.T, p lifecycle.Plan) {
			require.Len(t, p.Entries, 1)
			assert.Equal(t, commonv1.DownloadPhaseQueued, p.Entries[0].Phase)
		},
	}, {
		name: "usenet Queued whose turn came -> Downloading",
		view: func() lifecycle.View {
			e := usenetEntry(commonv1.DownloadPhaseQueued)
			tr := record(e, seq0, commonv1.DownloadStageTransferring)
			v := view([]catalogv1alpha1.DownloadEntry{e}, tr)
			steady(&v)
			return v
		},
		check: func(t *testing.T, p lifecycle.Plan) {
			require.Len(t, p.Entries, 1)
			assert.Equal(t, commonv1.DownloadPhaseDownloading, p.Entries[0].Phase)
		},
	}, {
		name: "healthPaused with healthAction delete -> Blocklisted missingArticles",
		view: func() lifecycle.View {
			e := usenetEntry(commonv1.DownloadPhaseDownloading)
			tr := record(e, seq0, commonv1.DownloadStageTransferring)
			tr.Record.HealthPaused = true
			v := view([]catalogv1alpha1.DownloadEntry{e}, tr)
			v.Clients[1].HealthAction = downloadv1alpha1.HealthActionDelete
			steady(&v)
			return v
		},
		check: func(t *testing.T, p lifecycle.Plan) {
			assert.Equal(t, commonv1.DownloadPhaseBlocklisted, only(p).Phase)
			assert.Equal(t, commonv1.DownloadFailureMissingArticles, only(p).FailureReason)
		},
	}, {
		name: "any -> Removing on the user's remove, data as asked",
		view: func() lifecycle.View {
			e := entry(commonv1.DownloadPhaseDownloading)
			e.RemoveDataOnDelete = false
			v := view([]catalogv1alpha1.DownloadEntry{e}, record(e, seq0, commonv1.DownloadStageTransferring))
			v.Intents = lifecycle.ParseIntents(map[string]string{catalogv1alpha1.AnnotationDownloadRemove: "n1 " + entryID + " data"})
			return v
		},
		check: func(t *testing.T, p lifecycle.Plan) {
			assert.Equal(t, commonv1.DownloadPhaseRemoving, only(p).Phase)
			require.Len(t, absents(p), 1)
			assert.True(t, absents(p)[0].Cmd.RemoveData, "the intent's data flag")
			assert.Equal(t, "n1", p.Nonces.Remove)
		},
	}, {
		name: "any -> Blocklisted on the user's remove with blocklist: item scope, manual",
		view: func() lifecycle.View {
			e := entry(commonv1.DownloadPhaseSeeding)
			e.Import = importedSummary(1)
			v := view([]catalogv1alpha1.DownloadEntry{e}, record(e, seq0, commonv1.DownloadStageSeeding))
			v.Intents = lifecycle.ParseIntents(map[string]string{catalogv1alpha1.AnnotationDownloadRemove: "n2 " + entryID + " blocklist"})
			return v
		},
		check: func(t *testing.T, p lifecycle.Plan) {
			e := only(p)
			assert.Equal(t, commonv1.DownloadPhaseBlocklisted, e.Phase)
			assert.Equal(t, commonv1.DownloadFailureManual, e.FailureReason)
			assert.Equal(t, catalogv1alpha1.BlockScopeItem, e.Block.Scope)
			require.Len(t, absents(p), 1)
			assert.False(t, absents(p)[0].Cmd.RemoveData, "no data flag: the files stay")
		},
	}, {
		name: "any live -> Removing on the owner being deleted",
		view: func() lifecycle.View {
			e := entry(commonv1.DownloadPhaseDownloading)
			v := view([]catalogv1alpha1.DownloadEntry{e}, record(e, seq0, commonv1.DownloadStageTransferring))
			v.Deleting = true
			return v
		},
		check: func(t *testing.T, p lifecycle.Plan) {
			assert.Equal(t, commonv1.DownloadPhaseRemoving, only(p).Phase)
			require.Len(t, absents(p), 1)
			assert.True(t, absents(p)[0].Cmd.RemoveData)
			assert.True(t, *p.Finalizer, "the finalizer holds until the entry is dropped")
		},
	}, {
		name: "Removing: record removed at the seq -> the data removed first",
		view: func() lifecycle.View {
			e := entry(commonv1.DownloadPhaseRemoving)
			e.OutputPath = outPath
			return view([]catalogv1alpha1.DownloadEntry{e}, removedRecord(e))
		},
		check: func(t *testing.T, p lifecycle.Plan) {
			require.NotNil(t, only(p), "kept until its data is removed")
			assert.Equal(t, []string{outPath}, p.Removals)
		},
	}, {
		name: "Removing: record removed and the data gone -> dropped, removed, finalizer off",
		view: func() lifecycle.View {
			e := entry(commonv1.DownloadPhaseRemoving)
			e.OutputPath = outPath
			v := view([]catalogv1alpha1.DownloadEntry{e}, removedRecord(e))
			v.Removed[outPath] = true
			return v
		},
		check: func(t *testing.T, p lifecycle.Plan) {
			assert.Empty(t, p.Entries)
			assert.Empty(t, p.Removals)
			assert.Contains(t, actions(p), lifecycle.ActionRemoved)
			require.NotNil(t, p.Finalizer)
			assert.False(t, *p.Finalizer)
		},
	}, {
		name: "Failed: record removed -> dropped",
		view: func() lifecycle.View {
			e := entry(commonv1.DownloadPhaseFailed)
			e.FailureReason = commonv1.DownloadFailureDiskFull
			e.RemoveDataOnDelete = false
			return view([]catalogv1alpha1.DownloadEntry{e}, removedRecord(e))
		},
		check: func(t *testing.T, p lifecycle.Plan) {
			assert.Empty(t, p.Entries)
			assert.Empty(t, p.Removals, "no data asked")
		},
	}, {
		name: "Seeding: record failed payloadUnavailable -> dropped with SeedingLost, nothing blocked or searched",
		view: func() lifecycle.View {
			e := entry(commonv1.DownloadPhaseSeeding)
			e.Import = importedSummary(1)
			return view([]catalogv1alpha1.DownloadEntry{e}, failedRecord(e, commonv1.DownloadFailurePayloadUnavailable))
		},
		check: func(t *testing.T, p lifecycle.Plan) {
			assert.Empty(t, p.Entries)
			assert.Contains(t, reasons(p), lifecycle.ReasonSeedingLost)
			assert.Empty(t, p.Blocks)
			assert.Empty(t, p.Redownloads)
			assert.Empty(t, absents(p), "the engine already let it go")
		},
	}, {
		name: "Downloading: record failed payloadUnavailable before import -> Blocklisted item scope with a redownload",
		view: func() lifecycle.View {
			e := entry(commonv1.DownloadPhaseDownloading)
			return view([]catalogv1alpha1.DownloadEntry{e}, failedRecord(e, commonv1.DownloadFailurePayloadUnavailable))
		},
		check: func(t *testing.T, p lifecycle.Plan) {
			e := only(p)
			assert.Equal(t, commonv1.DownloadPhaseBlocklisted, e.Phase)
			assert.Equal(t, catalogv1alpha1.BlockScopeItem, e.Block.Scope)
		},
		nextCheck: func(t *testing.T, p lifecycle.Plan) {
			assert.Equal(t, []schema.ItemRef{owner()}, p.Redownloads)
		},
	}, {
		name: "any live before Removing: a settled engine holds no transfer -> present again at a new seq, after its boot",
		view: func() lifecycle.View {
			e := entry(commonv1.DownloadPhaseDownloading)
			// Last commanded before the engine's restart an hour ago.
			e.Dispatch.DispatchedAt = metav1.NewTime(now.Add(-3 * time.Hour))
			return view([]catalogv1alpha1.DownloadEntry{e})
		},
		check: func(t *testing.T, p lifecycle.Plan) {
			assert.Equal(t, commonv1.DownloadPhaseDownloading, only(p).Phase, "the phase is unchanged")
			require.Len(t, p.Commands, 1)
			c := p.Commands[0]
			assert.Equal(t, schema.EngineDesiredPresent, c.Cmd.Desired)
			assert.Greater(t, c.Cmd.Seq, seq0)
			assert.Equal(t, bootID, c.BootID, "the Msg-Id names the boot, so the dedupe window does not swallow it")
		},
	}, {
		name: "an unclaimed transfer the owner still wants -> re-created under the claim, present at a new seq",
		view: func() lifecycle.View {
			v := view(nil)
			e := entry(commonv1.DownloadPhaseDownloading)
			tr := record(e, seq0, commonv1.DownloadStageTransferring)
			tr.Record.Claimed = false
			v.Unclaimed = []lifecycle.Transfer{tr}
			return v
		},
		check: func(t *testing.T, p lifecycle.Plan) {
			e := only(p)
			require.NotNil(t, e)
			assert.Equal(t, entryID, e.ID)
			assert.Equal(t, entryUID, e.UID)
			assert.Equal(t, commonv1.DownloadPhaseDownloading, e.Phase)
			require.Len(t, p.Commands, 1)
			assert.Equal(t, schema.EngineDesiredPresent, p.Commands[0].Cmd.Desired)
			assert.Greater(t, p.Commands[0].Cmd.Seq, seq0)
		},
	}, {
		name: "an unclaimed transfer the owner no longer wants -> absent with the claim's data rule",
		view: func() lifecycle.View {
			v := view(nil)
			v.HasFile = true
			e := entry(commonv1.DownloadPhaseDownloading)
			e.RemoveDataOnDelete = false
			tr := record(e, seq0, commonv1.DownloadStageTransferring)
			tr.Record.Claimed = false
			v.Unclaimed = []lifecycle.Transfer{tr}
			return v
		},
		check: func(t *testing.T, p lifecycle.Plan) {
			assert.Empty(t, p.Entries)
			require.Len(t, absents(p), 1)
			assert.False(t, absents(p)[0].Cmd.RemoveData, "the claim's removeDataOnDelete")
			assert.Equal(t, seq0+1, absents(p)[0].Cmd.Seq)
		},
	}} {
		t.Run(tc.name, func(t *testing.T) {
			v := tc.view()
			p := lifecycle.Decide(v)
			tc.check(t, p)
			if tc.nextCheck != nil {
				tc.nextCheck(t, lifecycle.Decide(next(v, p)))
			}
		})
	}
}
