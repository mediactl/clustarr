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
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/grab/lifecycle"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/names"
)

// stageOf is the record stage a live phase reads from.
func stageOf(p commonv1.DownloadPhase) commonv1.DownloadStage {
	switch p {
	case commonv1.DownloadPhaseAssigned:
		return ""
	case commonv1.DownloadPhaseQueued:
		return commonv1.DownloadStageFetchingMetadata
	case commonv1.DownloadPhaseCompleted:
		return commonv1.DownloadStageDone
	case commonv1.DownloadPhaseSeeding:
		return commonv1.DownloadStageSeeding
	}
	return commonv1.DownloadStageTransferring
}

// TestNothingRemovesOnAbsence is Review Focus 1: for every combination of
// an absent transfer record, an absent or stale engine record, a recreated
// (absent) transfers bucket and a lost command book, over every live phase
// and an hour of passes, Decide issues no absent command, no data removal
// and drops no entry. Absence is never evidence.
func TestNothingRemovesOnAbsence(t *testing.T) {
	phases := []commonv1.DownloadPhase{
		commonv1.DownloadPhaseAssigned, commonv1.DownloadPhaseQueued, commonv1.DownloadPhaseDownloading,
		commonv1.DownloadPhasePaused, commonv1.DownloadPhaseCompleted, commonv1.DownloadPhaseSeeding,
	}
	for _, phase := range phases {
		for _, recordKept := range []bool{true, false} {
			for _, engineState := range []string{"present", "absent", "stale"} {
				for _, bucketRecreated := range []bool{false, true} {
					for _, bookKept := range []bool{true, false} {
						name := fmt.Sprintf("%s/record=%t/engine=%s/recreated=%t/book=%t", phase, recordKept, engineState, bucketRecreated, bookKept)
						t.Run(name, func(t *testing.T) {
							e := entry(phase)
							e.Dispatch.DispatchedAt = metav1.NewTime(now.Add(-3 * time.Hour))
							if phase == commonv1.DownloadPhaseSeeding {
								e.Import = importedSummary(1)
							}
							var transfers []lifecycle.Transfer
							if recordKept {
								transfers = append(transfers, record(e, seq0, stageOf(phase)))
							}
							v := view([]catalogv1alpha1.DownloadEntry{e}, transfers...)
							v.Files[entryID] = 1
							switch engineState {
							case "absent":
								delete(v.Engines, "qbt-0")
							case "stale":
								eng := v.Engines["qbt-0"]
								eng.At = now.Add(-time.Hour)
								v.Engines["qbt-0"] = eng
							}
							if bucketRecreated {
								// The DownloadClient asked a resync its engine
								// has not reported yet.
								v.Transfers = nil
								v.Clients[0].ResyncSeq = 42
							}
							if !bookKept {
								v.Issued = nil
							}
							for pass := range 6 {
								p := lifecycle.Decide(v)
								require.Lenf(t, p.Entries, 1, "pass %d dropped the entry", pass)
								got := p.Entries[0].Phase
								assert.NotContainsf(t, []commonv1.DownloadPhase{
									commonv1.DownloadPhaseRemoving, commonv1.DownloadPhaseFailed, commonv1.DownloadPhaseBlocklisted,
								}, got, "pass %d", pass)
								assert.Emptyf(t, absents(p), "pass %d commanded a removal", pass)
								assert.Emptyf(t, p.Removals, "pass %d removed data", pass)
								assert.NotContainsf(t, reasons(p), lifecycle.ReasonEngineGone, "pass %d", pass)
								assert.NotContainsf(t, reasons(p), lifecycle.ReasonTransferOwnerGone, "pass %d", pass)
								require.NotNil(t, p.Finalizer)
								assert.True(t, *p.Finalizer)
								v = next(v, p)
								v.Now = v.Now.Add(13 * time.Minute)
							}
						})
					}
				}
			}
		}
	}
}

// TestAnEntryIsNotJudgedMissingBeforeResync: a live entry whose record is
// absent is re-added only once its engine re-attached at its boot, reached
// the resync its client asked for, and ResyncSettle passed (§6.7); before
// that its command is only republished at the seq it has.
func TestAnEntryIsNotJudgedMissingBeforeResync(t *testing.T) {
	// The entry was last commanded 25 minutes ago (inside the republish
	// window); its engine restarted 20 minutes ago and holds no record of it.
	base := func() lifecycle.View {
		e := entry(commonv1.DownloadPhaseDownloading)
		e.Dispatch.DispatchedAt = metav1.NewTime(now.Add(-25 * time.Minute))
		v := view([]catalogv1alpha1.DownloadEntry{e})
		eng := v.Engines["qbt-0"]
		eng.ReattachedAt = now.Add(-20 * time.Minute)
		v.Engines["qbt-0"] = eng
		return v
	}
	for name, mutate := range map[string]func(v *lifecycle.View){
		"not re-attached": func(v *lifecycle.View) {
			eng := v.Engines["qbt-0"]
			eng.Reattached, eng.ReattachedAt = false, time.Time{}
			v.Engines["qbt-0"] = eng
		},
		"re-attached inside ResyncSettle": func(v *lifecycle.View) {
			eng := v.Engines["qbt-0"]
			eng.ReattachedAt = now.Add(-lifecycle.ResyncSettle + 30*time.Second)
			v.Engines["qbt-0"] = eng
		},
		"behind the client's resync": func(v *lifecycle.View) {
			v.Clients[0].ResyncSeq = 9
			eng := v.Engines["qbt-0"]
			eng.ResyncSeq = 8
			v.Engines["qbt-0"] = eng
		},
		"no engine record": func(v *lifecycle.View) { delete(v.Engines, "qbt-0") },
	} {
		t.Run(name, func(t *testing.T) {
			v := base()
			mutate(&v)
			p := lifecycle.Decide(v)
			e := only(p)
			require.NotNil(t, e)
			assert.Equal(t, seq0, e.Dispatch.Seq, "no new seq: the entry is not judged missing")
			assert.NotContains(t, e.Message, "holds no transfer")
			for _, c := range p.Commands {
				assert.Equal(t, seq0, c.Cmd.Seq, "only the owed seq is republished")
				assert.Equal(t, schema.EngineDesiredPresent, c.Cmd.Desired)
			}
		})
	}
	t.Run("settled: re-added at a new seq", func(t *testing.T) {
		v := base()
		v.Clients[0].ResyncSeq = 9
		eng := v.Engines["qbt-0"]
		eng.ResyncSeq = 9
		v.Engines["qbt-0"] = eng
		steady(&v)
		p := lifecycle.Decide(v)
		e := only(p)
		require.NotNil(t, e)
		assert.Greater(t, e.Dispatch.Seq, seq0)
		assert.Contains(t, e.Message, "holds no transfer")
		require.Len(t, p.Commands, 1)
		assert.Equal(t, schema.EngineDesiredPresent, p.Commands[0].Cmd.Desired)
		assert.Equal(t, bootID, p.Commands[0].BootID)
	})
}

// unclaimed is a present transfer record whose claim names claimOwner and
// which no command named since its engine's boot.
func unclaimed(claimOwner schema.ItemRef, providerID string, imported, seedGoal bool) lifecycle.Transfer {
	e := entry(commonv1.DownloadPhaseDownloading)
	tr := record(e, seq0, commonv1.DownloadStageSeeding)
	tr.Record.Claimed = false
	tr.Record.Claim.Owner = claimOwner
	tr.Record.Claim.ProviderID = providerID
	tr.Record.Claim.Imported = imported
	tr.Record.SeedGoalReached = seedGoal
	return tr
}

// TestTheClaimEvidenceOrder is §6.7's evidence table, in order (Review
// Focus 3), for a transfer no entry claims.
func TestTheClaimEvidenceOrder(t *testing.T) {
	restored := owner()
	restored.UID = "owner-uid-restored"

	for _, tc := range []struct {
		name     string
		claim    lifecycle.Transfer
		mutate   func(v *lifecycle.View)
		recreate bool
		remove   bool
		data     bool
	}{{
		name:     "same UID, still wanted: re-created",
		claim:    unclaimed(owner(), "tmdb:949", false, false),
		recreate: true,
	}, {
		name:   "same UID, no longer wanted (the owner has a file): absent with the claim's data rule",
		claim:  unclaimed(owner(), "tmdb:949", false, false),
		mutate: func(v *lifecycle.View) { v.HasFile = true },
		remove: true, data: true,
	}, {
		name:   "same UID, no longer wanted (unmonitored): absent",
		claim:  unclaimed(owner(), "tmdb:949", false, false),
		mutate: func(v *lifecycle.View) { v.Monitored = false },
		remove: true, data: true,
	}, {
		name:  "same UID, the grab planner says no: absent",
		claim: unclaimed(owner(), "tmdb:949", false, false),
		mutate: func(v *lifecycle.View) {
			v.StillWants = func(schema.TransferClaim) (bool, string) { return false, "replaced" }
		},
		remove: true, data: true,
	}, {
		name:  "same UID, imported and its seed goal not met: wanted though the owner has a file (the seeding obligation)",
		claim: unclaimed(owner(), "tmdb:949", true, false),
		mutate: func(v *lifecycle.View) {
			v.HasFile = true
			v.StillWants = func(schema.TransferClaim) (bool, string) { return false, "a restored status reads no upgrade" }
		},
		recreate: true,
	}, {
		name:     "a restored UID, the same name and provider id: the same owner, re-created",
		claim:    unclaimed(restored, "tmdb:949", false, false),
		recreate: true,
	}, {
		name:     "a restored UID, the same name, the claim recorded no provider id: the same owner",
		claim:    unclaimed(restored, "", false, false),
		recreate: true,
	}, {
		name:   "a restored UID, the same name, no longer wanted: absent",
		claim:  unclaimed(restored, "tmdb:949", false, false),
		mutate: func(v *lifecycle.View) { v.HasFile = true },
		remove: true, data: true,
	}, {
		name:   "the same name, another provider id, verified through the APIReader: another item, the owner is gone",
		claim:  unclaimed(restored, "tmdb:1", false, false),
		remove: true, data: true,
	}, {
		name:   "the same name, another provider id, read only from the cache: no evidence, nothing",
		claim:  unclaimed(restored, "tmdb:1", false, false),
		mutate: func(v *lifecycle.View) { v.ProviderVerified = false },
	}, {
		name: "a record that a command named since the boot is no orphan: nothing",
		claim: func() lifecycle.Transfer {
			tr := unclaimed(owner(), "tmdb:949", false, false)
			tr.Record.Claimed = true
			return tr
		}(),
	}, {
		name: "a pre-journal record without a claim never reaches an owner: nothing",
		claim: func() lifecycle.Transfer {
			tr := unclaimed(owner(), "tmdb:949", false, false)
			tr.Record.Claim = nil
			return tr
		}(),
	}} {
		t.Run(tc.name, func(t *testing.T) {
			v := view(nil)
			v.Unclaimed = []lifecycle.Transfer{tc.claim}
			if tc.mutate != nil {
				tc.mutate(&v)
			}
			p := lifecycle.Decide(v)
			switch {
			case tc.recreate:
				e := only(p)
				require.NotNil(t, e, "re-created")
				assert.Equal(t, entryID, e.ID)
				assert.Equal(t, entryUID, e.UID, "under the claim's uid: its records and progress keys continue")
				require.Len(t, p.Commands, 1)
				c := p.Commands[0].Cmd
				assert.Equal(t, schema.EngineDesiredPresent, c.Desired)
				assert.Greater(t, c.Seq, seq0)
				assert.Equal(t, owner().UID, c.Owner.UID, "the command writes the owner's current UID into the journal")
				assert.Equal(t, owner().UID, c.Claim.Owner.UID)
				assert.Empty(t, absents(p))
			case tc.remove:
				assert.Empty(t, p.Entries)
				require.Len(t, absents(p), 1)
				assert.Equal(t, tc.data, absents(p)[0].Cmd.RemoveData)
				assert.Equal(t, seq0+1, absents(p)[0].Cmd.Seq, "above the journal's seq")
				assert.Contains(t, reasons(p), lifecycle.ReasonTransferOwnerGone)
			default:
				assert.Empty(t, p.Entries)
				assert.Empty(t, p.Commands, "no evidence, no action")
			}
		})
	}
}

// TestARecreatedImportedEntryCarriesAnAdmissibleImportDispatch: an imported
// transfer re-created from its claim -- kind's seeding torrents, resumed by
// talos's engines from the shared scratch (2026-10-15) -- gets an import
// summary whose dispatch the CRD admits: seq at least 1 and a dispatchedAt,
// and closed (answeredSeq == seq), since no import task is in flight for a
// file already imported. The zero dispatch it had made the apiserver reject
// every status apply of the owner, so none of its status ever landed.
func TestARecreatedImportedEntryCarriesAnAdmissibleImportDispatch(t *testing.T) {
	v := view(nil)
	v.Unclaimed = []lifecycle.Transfer{unclaimed(owner(), "tmdb:949", true, false)}
	v.HasFile = true
	p := lifecycle.Decide(v)
	e := only(p)
	require.NotNil(t, e, "re-created: its seed goal is not met")
	require.NotNil(t, e.Import, "the claim says imported")
	assert.Equal(t, catalogv1alpha1.ImportPhaseImported, e.Import.Phase)
	d := e.Import.Dispatch
	assert.GreaterOrEqual(t, d.Seq, int64(1), "the CRD's minimum for import.dispatch.seq")
	assert.False(t, d.DispatchedAt.IsZero(), "the CRD requires import.dispatch.dispatchedAt")
	assert.Equal(t, d.Seq, d.AnsweredSeq, "closed: no import task is in flight for an imported file")
	assert.False(t, e.Dispatch.DispatchedAt.IsZero(), "the entry's own dispatch is admissible too")
}

// blocklisted is a Blocklisted entry whose transfer is removed at its seq.
func blocklisted(confirmedAt *time.Time) (catalogv1alpha1.DownloadEntry, lifecycle.Transfer) {
	e := entry(commonv1.DownloadPhaseBlocklisted)
	e.FailureReason = commonv1.DownloadFailureMissingArticles
	e.RemoveDataOnDelete = false
	e.Dispatch.DispatchedAt = metav1.NewTime(now.Add(-48 * time.Hour)) // past R25's window
	e.Block = &catalogv1alpha1.EntryBlock{Scope: catalogv1alpha1.BlockScopeGlobal, Reason: e.FailureReason, Seq: 77}
	if confirmedAt != nil {
		t := metav1.NewTime(*confirmedAt)
		e.Block.ConfirmedAt = &t
	}
	return e, removedRecord(e)
}

// TestABlocklistedEntryIsDroppedOnlyAfterConfirmationAndQuarantine: a
// Blocklisted entry stays as a tombstone until the release index confirmed
// its block at its seq, and then BlockQuarantine more (§6.14).
func TestABlocklistedEntryIsDroppedOnlyAfterConfirmationAndQuarantine(t *testing.T) {
	t.Run("unconfirmed: kept, the block call owed", func(t *testing.T) {
		e, tr := blocklisted(nil)
		p := lifecycle.Decide(view([]catalogv1alpha1.DownloadEntry{e}, tr))
		require.NotNil(t, only(p))
		require.Len(t, p.Blocks, 1)
		assert.Equal(t, schema.BlocklistOpBlock, p.Blocks[0].Req.Op)
		assert.Equal(t, int64(77), p.Blocks[0].Req.Seq)
		assert.Equal(t, now.Add(-48*time.Hour).Add(lifecycle.BlocklistTTL), p.Blocks[0].Req.Until)
	})
	t.Run("a reply for an older seq confirms nothing", func(t *testing.T) {
		e, tr := blocklisted(nil)
		v := view([]catalogv1alpha1.DownloadEntry{e}, tr)
		v.Blocks = map[types.UID]lifecycle.BlockReply{entryUID: {Seq: 76, Applied: true, At: now.Add(-2 * time.Hour)}}
		p := lifecycle.Decide(v)
		require.NotNil(t, only(p))
		assert.Nil(t, only(p).Block.ConfirmedAt)
		assert.Len(t, p.Blocks, 1)
	})
	t.Run("confirmed inside the quarantine: a tombstone", func(t *testing.T) {
		e, tr := blocklisted(nil)
		v := view([]catalogv1alpha1.DownloadEntry{e}, tr)
		v.Blocks = map[types.UID]lifecycle.BlockReply{entryUID: {Seq: 77, Applied: true, At: now.Add(-10 * time.Minute)}}
		p := lifecycle.Decide(v)
		require.NotNil(t, only(p))
		require.NotNil(t, only(p).Block.ConfirmedAt)
		assert.Equal(t, now.Add(-10*time.Minute), only(p).Block.ConfirmedAt.UTC())
		assert.Empty(t, p.Blocks)
		assert.Equal(t, now.Add(-10*time.Minute).Add(lifecycle.BlockQuarantine), p.Due, "the next pass is the quarantine's end")
	})
	t.Run("a stale reply (a newer row stands) confirms too", func(t *testing.T) {
		e, tr := blocklisted(nil)
		v := view([]catalogv1alpha1.DownloadEntry{e}, tr)
		v.Blocks = map[types.UID]lifecycle.BlockReply{entryUID: {Seq: 77, Stale: true, At: now.Add(-time.Minute)}}
		require.NotNil(t, only(lifecycle.Decide(v)).Block.ConfirmedAt)
	})
	t.Run("confirmed past the quarantine: dropped", func(t *testing.T) {
		at := now.Add(-lifecycle.BlockQuarantine - time.Minute)
		e, tr := blocklisted(&at)
		p := lifecycle.Decide(view([]catalogv1alpha1.DownloadEntry{e}, tr))
		assert.Empty(t, p.Entries)
		assert.Contains(t, actions(p), lifecycle.ActionRemoved)
	})
	t.Run("confirmed past the quarantine but the transfer not yet removed: kept, absent owed", func(t *testing.T) {
		at := now.Add(-2 * time.Hour)
		e, _ := blocklisted(&at)
		tr := record(e, seq0-1, commonv1.DownloadStageTransferring)
		p := lifecycle.Decide(view([]catalogv1alpha1.DownloadEntry{e}, tr))
		require.NotNil(t, only(p))
		require.Len(t, absents(p), 1)
	})
}

// TestAnOwedCommandIsRepublishedWithTheSameMsgIDInsideTheWindow: a command
// the record has not reached is republished at its seq (so under its Msg-Id)
// while younger than RepublishWindow, and at a new seq past it.
func TestAnOwedCommandIsRepublishedWithTheSameMsgIDInsideTheWindow(t *testing.T) {
	owed := func(age time.Duration) lifecycle.View {
		e := entry(commonv1.DownloadPhaseDownloading)
		e.Dispatch.Seq = seq0 + 500
		e.Dispatch.DispatchedAt = metav1.NewTime(now.Add(-age))
		return view([]catalogv1alpha1.DownloadEntry{e}, record(e, seq0, commonv1.DownloadStageTransferring))
	}
	t.Run("inside the window: the same seq and boot", func(t *testing.T) {
		p := lifecycle.Decide(owed(5 * time.Minute))
		require.Len(t, p.Commands, 1)
		c := p.Commands[0]
		assert.Equal(t, seq0+500, c.Cmd.Seq)
		assert.Empty(t, c.BootID, "the record is at the engine's boot: the plain Msg-Id")
		assert.False(t, c.Retry)
		assert.NotContains(t, reasons(p), lifecycle.ReasonCommandRetrying)
		assert.Equal(t, seq0+500, only(p).Dispatch.Seq, "status keeps the seq")
		again := lifecycle.Decide(next(owed(5*time.Minute), p))
		require.Len(t, again.Commands, 1)
		assert.Equal(t, c.Cmd.Seq, again.Commands[0].Cmd.Seq, "a second pass republishes the same seq")
	})
	t.Run("past the window: a new seq, CommandRetrying", func(t *testing.T) {
		p := lifecycle.Decide(owed(lifecycle.RepublishWindow + time.Minute))
		require.Len(t, p.Commands, 1)
		c := p.Commands[0]
		assert.Greater(t, c.Cmd.Seq, seq0+500)
		assert.True(t, c.Retry)
		assert.Contains(t, reasons(p), lifecycle.ReasonCommandRetrying)
		assert.Equal(t, c.Cmd.Seq, only(p).Dispatch.Seq)
	})
	t.Run("answered: nothing", func(t *testing.T) {
		e := entry(commonv1.DownloadPhaseDownloading)
		p := lifecycle.Decide(view([]catalogv1alpha1.DownloadEntry{e}, record(e, seq0, commonv1.DownloadStageTransferring)))
		assert.Empty(t, p.Commands)
	})
	t.Run("a changed desired state is a new seq at once", func(t *testing.T) {
		v := owed(5 * time.Minute)
		v.Intents = lifecycle.ParseIntents(map[string]string{catalogv1alpha1.AnnotationDownloadPriority: entryID + "=high"})
		p := lifecycle.Decide(v)
		require.Len(t, p.Commands, 1)
		assert.Greater(t, p.Commands[0].Cmd.Seq, seq0+500)
		assert.Equal(t, commonv1.DownloadPriorityHigh, p.Commands[0].Cmd.Priority)
	})
}

// TestEngineGoneDropsAfterTenMinutes is R-6 (§6.8): a Removing entry whose
// engine is gone is dropped EngineTeardownTimeout after its transition, its
// data removed by the manager when asked, with EngineGone.
func TestEngineGoneDropsAfterTenMinutes(t *testing.T) {
	gone := map[string]func(v *lifecycle.View){
		"DownloadClient deleted": func(v *lifecycle.View) { v.Clients = v.Clients[1:] },
		"engine not ready":       func(v *lifecycle.View) { delete(v.Engines, "qbt-0") },
		"stale engine record": func(v *lifecycle.View) {
			eng := v.Engines["qbt-0"]
			eng.At = now.Add(-time.Hour)
			v.Engines["qbt-0"] = eng
		},
		"ordinal past the replicas": func(v *lifecycle.View) {
			v.Stored[0].Engine = "qbt-1"
			v.Engines["qbt-1"] = engine("qbt", 1)
		},
	}
	for name, mutate := range gone {
		t.Run(name, func(t *testing.T) {
			e := entry(commonv1.DownloadPhaseRemoving)
			e.OutputPath = outPath
			e.Dispatch.Seq = seq0 + 1 // the absent command, unanswered
			e.Dispatch.DispatchedAt = metav1.NewTime(now.Add(-5 * time.Minute))
			v := view([]catalogv1alpha1.DownloadEntry{e}, record(e, seq0, commonv1.DownloadStageSeeding))
			mutate(&v)
			steady(&v)

			p := lifecycle.Decide(v)
			require.NotNil(t, only(p), "kept inside the teardown timeout")
			assert.Empty(t, p.Removals)
			assert.NotContains(t, reasons(p), lifecycle.ReasonEngineGone)
			assert.False(t, p.Due.After(now.Add(5*time.Minute)), "woken by the timeout's end")

			v.Now = now.Add(5*time.Minute + time.Second)
			p = lifecycle.Decide(v)
			require.NotNil(t, only(p), "kept until its data is removed")
			assert.Equal(t, []string{outPath}, p.Removals, "the manager removes the data itself")
			assert.Contains(t, reasons(p), lifecycle.ReasonEngineGone)

			v.Removed[outPath] = true
			p = lifecycle.Decide(v)
			assert.Empty(t, p.Entries)
			assert.False(t, *p.Finalizer)
		})
	}
	t.Run("a live engine is waited for, however long", func(t *testing.T) {
		e := entry(commonv1.DownloadPhaseRemoving)
		e.Dispatch.Seq = seq0 + 1
		e.Dispatch.DispatchedAt = metav1.NewTime(now.Add(-3 * time.Hour))
		p := lifecycle.Decide(view([]catalogv1alpha1.DownloadEntry{e}, record(e, seq0, commonv1.DownloadStageSeeding)))
		require.NotNil(t, only(p))
		assert.NotContains(t, reasons(p), lifecycle.ReasonEngineGone)
		require.Len(t, absents(p), 1, "the absent command is owed again")
	})
}

// TestIntentsParseAndRecordEveryNonce is §6.10's grammar, invalid values
// included, and the nonce bookkeeping Decide does for every one-shot.
func TestIntentsParseAndRecordEveryNonce(t *testing.T) {
	t.Run("parse", func(t *testing.T) {
		in := lifecycle.ParseIntents(map[string]string{
			catalogv1alpha1.AnnotationDownloadPaused:   "a, b c",
			catalogv1alpha1.AnnotationDownloadPriority: "a=high,b=low c=urgent",
			catalogv1alpha1.AnnotationDownloadRemove:   "n1 a data blocklist",
			catalogv1alpha1.AnnotationDownloadResume:   "n2 b",
			catalogv1alpha1.AnnotationDownloadImport:   "n3 a target=episode/the-wire-s01e02/s01e02 override",
			catalogv1alpha1.AnnotationDownloadUnblock:  "n4 " + strings.ToUpper(infoHash) + " global",
		})
		assert.Equal(t, map[string]bool{"a": true, "b": true, "c": true}, in.Paused)
		assert.Equal(t, map[string]string{"a": "high", "b": "low"}, in.Priority)
		assert.Equal(t, &lifecycle.RemoveIntent{Nonce: "n1", ID: "a", Data: true, Blocklist: true}, in.Remove)
		assert.Equal(t, &lifecycle.ResumeIntent{Nonce: "n2", ID: "b"}, in.Resume)
		require.NotNil(t, in.Import)
		assert.Equal(t, "Episode", in.Import.Target.Kind)
		assert.Equal(t, "the-wire-s01e02", in.Import.Target.Name)
		assert.Equal(t, "s01e02", in.Import.TargetKey)
		assert.True(t, in.Import.Override)
		assert.Equal(t, &lifecycle.UnblockIntent{Nonce: "n4", InfoHash: infoHash, Global: true}, in.Unblock)
		require.Len(t, in.Invalid, 1, "c=urgent")
		assert.Equal(t, catalogv1alpha1.AnnotationDownloadPriority, in.Invalid[0].Annotation)

		u := lifecycle.ParseIntents(map[string]string{catalogv1alpha1.AnnotationDownloadUnblock: "n5 tracker/guid-1"})
		assert.Equal(t, &lifecycle.UnblockIntent{Nonce: "n5", Indexer: "tracker", GUID: "guid-1"}, u.Unblock)
	})
	for name, ann := range map[string]map[string]string{
		"remove with a bad nonce":     {catalogv1alpha1.AnnotationDownloadRemove: "-bad a"},
		"remove with an unknown flag": {catalogv1alpha1.AnnotationDownloadRemove: "n1 a purge"},
		"resume with extras":          {catalogv1alpha1.AnnotationDownloadResume: "n1 a now"},
		"import with a bare target":   {catalogv1alpha1.AnnotationDownloadImport: "n1 a target=movie"},
		"unblock of an empty indexer": {catalogv1alpha1.AnnotationDownloadUnblock: "n1 /guid"},
		"a nonce with no id":          {catalogv1alpha1.AnnotationDownloadResume: "n1"},
	} {
		t.Run("invalid: "+name, func(t *testing.T) {
			in := lifecycle.ParseIntents(ann)
			require.Len(t, in.Invalid, 1)
			assert.Nil(t, in.Remove)
			assert.Nil(t, in.Resume)
			assert.Nil(t, in.Import)
			assert.Nil(t, in.Unblock)

			e := entry(commonv1.DownloadPhaseDownloading)
			v := view([]catalogv1alpha1.DownloadEntry{e}, record(e, seq0, commonv1.DownloadStageTransferring))
			v.Intents = in
			p := lifecycle.Decide(v)
			var value string
			for _, x := range ann {
				value = x
			}
			assert.Equal(t, catalogv1alpha1.HandledNonce(value), nonceOf(p.Nonces, in.Invalid[0].Annotation), "recorded through HandledNonce")
			assert.Equal(t, []string{lifecycle.ReasonInvalidAnnotation}, reasons(p))
			assert.Empty(t, reasons(lifecycle.Decide(next(v, p))), "one Warning, once")
		})
	}
	t.Run("every valid one-shot is recorded on the pass that handles it, once", func(t *testing.T) {
		e := entry(commonv1.DownloadPhaseDownloading)
		v := view([]catalogv1alpha1.DownloadEntry{e}, record(e, seq0, commonv1.DownloadStageTransferring))
		v.Intents = lifecycle.ParseIntents(map[string]string{
			catalogv1alpha1.AnnotationDownloadResume: "r1 " + entryID,
			catalogv1alpha1.AnnotationDownloadImport: "i1 " + entryID + " override",
		})
		p := lifecycle.Decide(v)
		assert.Equal(t, "r1", p.Nonces.Resume)
		assert.Equal(t, "i1", p.Nonces.Import)
		require.Len(t, p.Commands, 1)
		assert.Equal(t, "r1", p.Commands[0].Cmd.HealthOverride, "the resume releases a health hold")
		again := lifecycle.Decide(next(v, p))
		for _, c := range again.Commands {
			assert.Equal(t, p.Commands[0].Cmd.Seq, c.Cmd.Seq, "handled once: only the owed seq is republished")
		}
		assert.Empty(t, again.Events)
	})
	t.Run("a remove naming no entry: NothingToRemove, recorded", func(t *testing.T) {
		v := view(nil)
		v.Intents = lifecycle.ParseIntents(map[string]string{catalogv1alpha1.AnnotationDownloadRemove: "x1 nope"})
		p := lifecycle.Decide(v)
		assert.Equal(t, "x1", p.Nonces.Remove)
		assert.Equal(t, []string{lifecycle.ReasonNothingToRemove}, reasons(p))
	})
	t.Run("a resume naming no live entry: NothingToResume, recorded", func(t *testing.T) {
		e := entry(commonv1.DownloadPhaseFailed)
		e.FailureReason = commonv1.DownloadFailureDiskFull
		v := view([]catalogv1alpha1.DownloadEntry{e}, record(e, seq0, commonv1.DownloadStageTransferring))
		v.Intents = lifecycle.ParseIntents(map[string]string{catalogv1alpha1.AnnotationDownloadResume: "x2 " + entryID})
		p := lifecycle.Decide(v)
		assert.Equal(t, "x2", p.Nonces.Resume)
		assert.Contains(t, reasons(p), lifecycle.ReasonNothingToResume)
	})
	t.Run("an unblock is recorded only once the index confirmed it, and releases the tombstone", func(t *testing.T) {
		at := now.Add(-10 * time.Minute)
		e, tr := blocklisted(&at)
		v := view([]catalogv1alpha1.DownloadEntry{e}, tr)
		v.Intents = lifecycle.ParseIntents(map[string]string{catalogv1alpha1.AnnotationDownloadUnblock: "u1 " + infoHash})
		p := lifecycle.Decide(v)
		assert.Empty(t, p.Nonces.Unblock, "not yet confirmed")
		require.Len(t, p.Blocks, 1)
		assert.Equal(t, schema.BlocklistOpUnblock, p.Blocks[0].Req.Op)
		assert.Equal(t, "u1", p.Blocks[0].Nonce)
		assert.Equal(t, schema.BlockScopeOf(owner()), p.Blocks[0].Req.Scope)
		require.NotNil(t, only(p), "the tombstone stands inside its quarantine")

		v = next(v, p)
		v.Unblocked = map[string]bool{"u1": true}
		p = lifecycle.Decide(v)
		assert.Equal(t, "u1", p.Nonces.Unblock)
		assert.Empty(t, p.Entries, "the confirmed unblock releases the tombstone at once")
	})
}

func nonceOf(n catalogv1alpha1.DownloadNonces, annotation string) string {
	switch annotation {
	case catalogv1alpha1.AnnotationDownloadRemove:
		return n.Remove
	case catalogv1alpha1.AnnotationDownloadResume:
		return n.Resume
	case catalogv1alpha1.AnnotationDownloadImport:
		return n.Import
	case catalogv1alpha1.AnnotationDownloadUnblock:
		return n.Unblock
	}
	return ""
}

func TestChooseClientPrefersTheIndexersClient(t *testing.T) {
	clients := []lifecycle.Client{
		{Name: "zeta", Protocol: commonv1.ProtocolTorrent, Enabled: true, Priority: 1},
		{Name: "beta", Protocol: commonv1.ProtocolTorrent, Enabled: true, Priority: 0},
		{Name: "alpha", Protocol: commonv1.ProtocolTorrent, Enabled: true, Priority: 0},
		{Name: "off", Protocol: commonv1.ProtocolTorrent, Enabled: false, Priority: -5},
		{Name: "sab", Protocol: commonv1.ProtocolUsenet, Enabled: true, Priority: -5},
	}
	for ref, want := range map[string]string{
		"":        "alpha", // the lowest priority, then the name
		"zeta":    "zeta",  // the indexer's client wins
		"off":     "alpha", // ... only when enabled
		"sab":     "alpha", // ... and of the protocol
		"missing": "alpha",
	} {
		c, ok := lifecycle.ChooseClient(clients, commonv1.ProtocolTorrent, ref)
		require.True(t, ok, ref)
		assert.Equal(t, want, c.Name, "indexer client %q", ref)
	}
	c, ok := lifecycle.ChooseClient(clients, commonv1.ProtocolUsenet, "zeta")
	require.True(t, ok)
	assert.Equal(t, "sab", c.Name)
	_, ok = lifecycle.ChooseClient(clients[3:4], commonv1.ProtocolTorrent, "")
	assert.False(t, ok, "no enabled client of the protocol")
}

func TestChooseOrdinalBalancesThenHashes(t *testing.T) {
	c := lifecycle.Client{Name: "qbt", Protocol: commonv1.ProtocolTorrent, Enabled: true, Replicas: 4}
	engines := func(loads ...int32) map[string]lifecycle.Engine {
		m := map[string]lifecycle.Engine{}
		for o, l := range loads {
			e := engine("qbt", int32(o))
			e.Active = l
			m[e.Name] = e
		}
		return m
	}
	// A guid whose hashed ordinal is 2.
	guid := ""
	for i := range 1000 {
		g := fmt.Sprintf("guid-%d", i)
		if names.HashOrdinal(4, infoHash, g) == 2 {
			guid = g
			break
		}
	}
	require.NotEmpty(t, guid)

	o, ok := lifecycle.ChooseOrdinal(c, engines(3, 1, 1, 2), infoHash, guid, now)
	require.True(t, ok)
	assert.Equal(t, int32(2), o, "a tie among the least loaded goes to the hashed ordinal")

	o, _ = lifecycle.ChooseOrdinal(c, engines(3, 0, 1, 2), infoHash, guid, now)
	assert.Equal(t, int32(1), o, "the least loaded wins over the hash")

	es := engines(0, 5, 5, 5)
	stale := es["qbt-0"]
	stale.At = now.Add(-lifecycle.EngineRecordFresh - time.Second)
	es["qbt-0"] = stale
	o, _ = lifecycle.ChooseOrdinal(c, es, infoHash, guid, now)
	assert.Equal(t, int32(2), o, "a stale record is not Ready")

	notReady := engines(0, 0, 0, 0)
	for k, e := range notReady {
		e.Ready = false
		notReady[k] = e
	}
	_, ok = lifecycle.ChooseOrdinal(c, notReady, infoHash, guid, now)
	assert.False(t, ok)

	u := lifecycle.Client{Name: "sab", Protocol: commonv1.ProtocolUsenet, Enabled: true, Replicas: 3}
	ue := map[string]lifecycle.Engine{"sab-0": engine("sab", 0), "sab-1": engine("sab", 1)}
	o, ok = lifecycle.ChooseOrdinal(u, ue, "", "g", now)
	require.True(t, ok)
	assert.Equal(t, int32(0), o, "a usenet client has ordinal 0")
}

// TestScopeByReason is §6.14's table plus timeout (R21).
func TestScopeByReason(t *testing.T) {
	for reason, want := range map[commonv1.DownloadFailureReason]catalogv1alpha1.BlockScope{
		commonv1.DownloadFailureEncrypted:          catalogv1alpha1.BlockScopeGlobal,
		commonv1.DownloadFailurePayloadMismatch:    catalogv1alpha1.BlockScopeGlobal,
		commonv1.DownloadFailureMissingArticles:    catalogv1alpha1.BlockScopeGlobal,
		commonv1.DownloadFailureImportRejected:     catalogv1alpha1.BlockScopeItem,
		commonv1.DownloadFailureStalled:            catalogv1alpha1.BlockScopeItem,
		commonv1.DownloadFailurePayloadUnavailable: catalogv1alpha1.BlockScopeItem,
		commonv1.DownloadFailureTimeout:            catalogv1alpha1.BlockScopeItem,
		commonv1.DownloadFailureManual:             catalogv1alpha1.BlockScopeItem,
	} {
		assert.Equal(t, want, lifecycle.ScopeFor(reason), string(reason))
	}
}

// TestAFailedEntryWaitsForItsRedownloadSearch is R25: a Blocklisted entry
// (or a Failed one that is not a local fault) younger than RedownloadWindow
// stays until each covered item's search was dispatched after the failure.
func TestAFailedEntryWaitsForItsRedownloadSearch(t *testing.T) {
	failedAt := now.Add(-3 * time.Hour)
	tombstone := func() lifecycle.View {
		confirmed := now.Add(-2 * time.Hour) // past the quarantine
		e, tr := blocklisted(&confirmed)
		e.Dispatch.DispatchedAt = metav1.NewTime(failedAt)
		return view([]catalogv1alpha1.DownloadEntry{e}, tr)
	}
	t.Run("not searched since the failure: kept, the search owed", func(t *testing.T) {
		v := tombstone()
		v.Searched[owner()] = failedAt.Add(-time.Hour)
		p := lifecycle.Decide(v)
		require.NotNil(t, only(p))
		assert.Equal(t, []schema.ItemRef{owner()}, p.Redownloads)
	})
	t.Run("searched since: dropped", func(t *testing.T) {
		v := tombstone()
		v.Searched[owner()] = failedAt.Add(time.Minute)
		p := lifecycle.Decide(v)
		assert.Empty(t, p.Entries)
		assert.Empty(t, p.Redownloads)
	})
	t.Run("older than the window: no search, dropped", func(t *testing.T) {
		v := tombstone()
		v.Stored[0].Dispatch.DispatchedAt = metav1.NewTime(now.Add(-lifecycle.RedownloadWindow - time.Minute))
		steady(&v)
		p := lifecycle.Decide(v)
		assert.Empty(t, p.Entries)
		assert.Empty(t, p.Redownloads)
	})
	t.Run("a local fault searches nothing", func(t *testing.T) {
		e := entry(commonv1.DownloadPhaseFailed)
		e.FailureReason = commonv1.DownloadFailureWriteError
		e.RemoveDataOnDelete = false
		e.Dispatch.DispatchedAt = metav1.NewTime(failedAt)
		p := lifecycle.Decide(view([]catalogv1alpha1.DownloadEntry{e}, removedRecord(e)))
		assert.Empty(t, p.Entries)
		assert.Empty(t, p.Redownloads)
	})
	t.Run("a pack: each covered episode not yet searched", func(t *testing.T) {
		v := tombstone()
		ep1 := schema.ItemRef{Kind: "Episode", Ref: schema.Ref{Namespace: ns, Name: "wire-s01e01", UID: "ep-1"}}
		ep2 := schema.ItemRef{Kind: "Episode", Ref: schema.Ref{Namespace: ns, Name: "wire-s01e02", UID: "ep-2"}}
		v.Covered = map[types.UID][]schema.ItemRef{entryUID: {ep1, ep2}}
		v.Searched[ep1] = failedAt.Add(time.Minute)
		p := lifecycle.Decide(v)
		require.NotNil(t, only(p))
		assert.Equal(t, []schema.ItemRef{ep2}, p.Redownloads)
	})
}

// A terminal entry keeps the message its failure set: the transfer
// record's message is the transfer's progress, and the usenet engine's
// last one read "transferring on easynews (15 connections)" after the
// download had completed, overwriting a Blocklisted entry's "no importable
// files found" on every pass (talos, 2026-10-27). A live entry still takes
// it.
func TestATerminalEntryKeepsItsOwnMessage(t *testing.T) {
	confirmed := now.Add(-10 * time.Minute)
	e, tr := blocklisted(&confirmed)
	e.Message = "no importable files found"
	tr.Record.State = schema.TransferStatePresent // not yet removed: the entry stays
	tr.Record.Message = "transferring on easynews (15 connections)"
	p := lifecycle.Decide(view([]catalogv1alpha1.DownloadEntry{e}, tr))
	require.NotNil(t, only(p))
	assert.Equal(t, "no importable files found", only(p).Message)

	l := entry(commonv1.DownloadPhaseDownloading)
	lt := record(l, seq0, commonv1.DownloadStageTransferring)
	lt.Record.Message = "transferring on easynews (15 connections)"
	p = lifecycle.Decide(view([]catalogv1alpha1.DownloadEntry{l}, lt))
	require.NotNil(t, only(p))
	assert.Equal(t, "transferring on easynews (15 connections)", only(p).Message, "a live entry shows the transfer's progress")
}

// R25's window runs from the failure (FailedAt), never from the entry's
// latest dispatch -- a Remove re-issues the command and had restarted the
// window -- and an unmonitored item, which is never searched, owes no
// search and holds no tombstone (talos, 2026-10-27: a Blocklisted entry
// for an unmonitored episode stood for good).
func TestARedownloadRunsFromTheFailureAndIsNeverOwedForAnUnmonitoredItem(t *testing.T) {
	confirmed := now.Add(-2 * time.Hour)
	tombstone := func(failedAt time.Time) (catalogv1alpha1.DownloadEntry, lifecycle.Transfer) {
		e, tr := blocklisted(&confirmed)
		e.FailedAt = ptr.To(metav1.NewTime(failedAt))
		e.Dispatch.DispatchedAt = metav1.NewTime(now.Add(-time.Minute)) // a Remove re-issued the command just now
		return e, tr
	}
	t.Run("the window is the failure's: an old failure re-dispatched is dropped", func(t *testing.T) {
		e, tr := tombstone(now.Add(-lifecycle.RedownloadWindow - time.Minute))
		v := view([]catalogv1alpha1.DownloadEntry{e}, tr)
		v.Searched[owner()] = now.Add(-48 * time.Hour)
		p := lifecycle.Decide(v)
		assert.Empty(t, p.Entries)
		assert.Empty(t, p.Redownloads)
	})
	t.Run("a recent failure re-dispatched still owes its search", func(t *testing.T) {
		e, tr := tombstone(now.Add(-3 * time.Hour))
		v := view([]catalogv1alpha1.DownloadEntry{e}, tr)
		v.Searched[owner()] = now.Add(-48 * time.Hour)
		p := lifecycle.Decide(v)
		require.NotNil(t, only(p))
		assert.Equal(t, []schema.ItemRef{owner()}, p.Redownloads)
	})
	t.Run("an unmonitored owner owes none: dropped", func(t *testing.T) {
		e, tr := tombstone(now.Add(-3 * time.Hour))
		v := view([]catalogv1alpha1.DownloadEntry{e}, tr)
		v.Monitored = false
		v.Searched[owner()] = now.Add(-48 * time.Hour)
		p := lifecycle.Decide(v)
		assert.Empty(t, p.Entries)
		assert.Empty(t, p.Redownloads)
	})
	t.Run("a pack: the unmonitored episode owes none, the monitored one does", func(t *testing.T) {
		e, tr := tombstone(now.Add(-3 * time.Hour))
		v := view([]catalogv1alpha1.DownloadEntry{e}, tr)
		ep1 := schema.ItemRef{Kind: "Episode", Ref: schema.Ref{Namespace: ns, Name: "wire-s01e01", UID: "ep-1"}}
		ep2 := schema.ItemRef{Kind: "Episode", Ref: schema.Ref{Namespace: ns, Name: "wire-s01e02", UID: "ep-2"}}
		v.Covered = map[types.UID][]schema.ItemRef{entryUID: {ep1, ep2}}
		v.Unmonitored = map[schema.ItemRef]bool{ep1: true}
		p := lifecycle.Decide(v)
		require.NotNil(t, only(p))
		assert.Equal(t, []schema.ItemRef{ep2}, p.Redownloads)
	})
}

// A failure stamps FailedAt once, at the first terminal transition.
func TestAFailureStampsFailedAtOnce(t *testing.T) {
	e := usenetEntry(commonv1.DownloadPhaseDownloading)
	p := lifecycle.Decide(view([]catalogv1alpha1.DownloadEntry{e}, failedRecord(e, commonv1.DownloadFailureMissingArticles)))
	got := only(p)
	require.NotNil(t, got)
	require.NotNil(t, got.FailedAt)
	assert.Equal(t, now, got.FailedAt.Time)
}
