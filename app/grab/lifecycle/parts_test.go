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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/grab/lifecycle"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// setOf is a set of n usenet parts (a movie released in parts), each part
// downloading at stage, with its transfer record.
func setOf(n int, stages ...commonv1.DownloadStage) ([]catalogv1alpha1.DownloadEntry, []lifecycle.Transfer) {
	var es []catalogv1alpha1.DownloadEntry
	var ts []lifecycle.Transfer
	for i := 1; i <= n; i++ {
		e := usenetEntry(commonv1.DownloadPhaseDownloading)
		e.ID, e.UID = fmt.Sprintf("heat-part%d", i), fmt.Sprintf("6a1b2c3d-0000-4000-8000-00000000010%d", i)
		e.Release.GUID, e.Release.Title = fmt.Sprintf("guid-part%d", i), fmt.Sprintf("Heat.1995.Part%d.1080p.BluRay.x264-GRP", i)
		e.Set = &catalogv1alpha1.EntrySet{ID: "parts:heat", Part: int32(i), Of: int32(n)}
		es = append(es, e)
		tr := record(e, seq0, stages[i-1])
		tr.Record.OutputPath = "/data/usenet/complete/movie/" + e.ID
		ts = append(ts, tr)
	}
	return es, ts
}

func entryNamed(p lifecycle.Plan, id string) *catalogv1alpha1.DownloadEntry {
	for i := range p.Entries {
		if p.Entries[i].ID == id {
			return &p.Entries[i]
		}
	}
	return nil
}

// A set's first part imports the set only once every part is done; until
// then, and while it imports, the other parts wait at Completed.
func TestASetImportsOnceEveryPartIsDone(t *testing.T) {
	inspect := lifecycle.ImportDecision{
		Summary:  catalogv1alpha1.DownloadImportSummary{Phase: catalogv1alpha1.ImportPhasePending},
		Dispatch: &lifecycle.ImportDispatch{Phase: schema.ImportSubInspect, Seq: 5},
		Verdict:  lifecycle.ImportNone,
	}

	es, ts := setOf(2, commonv1.DownloadStageDone, commonv1.DownloadStageTransferring)
	v := view(es, ts...)
	v.Import = map[types.UID]lifecycle.ImportDecision{types.UID(es[0].UID): inspect}
	p := lifecycle.Decide(v)
	first := entryNamed(p, "heat-part1")
	assert.Equal(t, commonv1.DownloadPhaseCompleted, first.Phase)
	assert.Contains(t, first.Message, "1 of 2")
	assert.Empty(t, p.Imports, "no import while a part downloads")
	assert.Equal(t, commonv1.DownloadPhaseDownloading, entryNamed(p, "heat-part2").Phase)

	es, ts = setOf(2, commonv1.DownloadStageDone, commonv1.DownloadStageDone)
	es[0].Phase, es[1].Phase = commonv1.DownloadPhaseCompleted, commonv1.DownloadPhaseCompleted
	es[0].Message = first.Message // the last pass's wait
	v = view(es, ts...)
	v.Import = map[types.UID]lifecycle.ImportDecision{types.UID(es[0].UID): inspect}
	p = lifecycle.Decide(v)
	require.Len(t, p.Imports, 1, "the first part imports the set")
	assert.NotContains(t, entryNamed(p, "heat-part1").Message, "waiting", "no longer waiting once it imports")
	assert.Equal(t, types.UID(es[0].UID), p.Imports[0].EntryUID)
	second := entryNamed(p, "heat-part2")
	assert.Equal(t, commonv1.DownloadPhaseCompleted, second.Phase)
	assert.Contains(t, second.Message, "waiting for part 1")
}

// The other parts follow the first part's import: Imported with it, then
// removed with their data as an imported entry is.
func TestTheOtherPartsFollowTheFirstPartsImport(t *testing.T) {
	es, ts := setOf(3, commonv1.DownloadStageDone, commonv1.DownloadStageDone, commonv1.DownloadStageDone)
	for i := range es {
		es[i].Phase, es[i].RemoveOnImport = commonv1.DownloadPhaseCompleted, true
	}
	v := view(es, ts...)
	v.Import = map[types.UID]lifecycle.ImportDecision{types.UID(es[0].UID): placedDecision(1)}
	v.Files["heat-part1"] = 1
	p := lifecycle.Decide(v)
	for _, e := range p.Entries {
		assert.Equalf(t, commonv1.DownloadPhaseImported, e.Phase, "%s", e.ID)
		if e.Set.Part == 1 {
			continue // its summary is importplan's own
		}
		// A follower never ran an import of its own: its summary still
		// needs the dispatch the CRD admits (seq >= 1, dispatchedAt),
		// closed, or the owner's every status apply is refused.
		require.NotNilf(t, e.Import, "%s", e.ID)
		d := e.Import.Dispatch
		assert.GreaterOrEqualf(t, d.Seq, int64(1), "%s: the CRD's minimum for import.dispatch.seq", e.ID)
		assert.Falsef(t, d.DispatchedAt.IsZero(), "%s: the CRD requires import.dispatch.dispatchedAt", e.ID)
		assert.Equalf(t, d.Seq, d.AnsweredSeq, "%s: closed", e.ID)
	}
	p = lifecycle.Decide(next(v, p))
	for _, e := range p.Entries {
		assert.Equalf(t, commonv1.DownloadPhaseRemoving, e.Phase, "%s", e.ID)
	}
}

// A part that fails takes its set down: the others are removed, whether
// they still download or wait done, and a failed import of the set removes
// the parts it would have joined.
func TestAFailedPartTakesItsSetDown(t *testing.T) {
	t.Run("a part's transfer fails", func(t *testing.T) {
		es, ts := setOf(3, commonv1.DownloadStageDone, commonv1.DownloadStageTransferring, commonv1.DownloadStageTransferring)
		es[0].Phase = commonv1.DownloadPhaseCompleted
		ts[1] = failedRecord(es[1], commonv1.DownloadFailureMissingArticles)
		v := view(es, ts...)
		p := lifecycle.Decide(next(v, lifecycle.Decide(v)))
		assert.Equal(t, commonv1.DownloadPhaseBlocklisted, entryNamed(p, "heat-part2").Phase)
		assert.Equal(t, commonv1.DownloadPhaseRemoving, entryNamed(p, "heat-part1").Phase)
		assert.Equal(t, commonv1.DownloadPhaseRemoving, entryNamed(p, "heat-part3").Phase)
	})
	t.Run("the set's import is a release fault", func(t *testing.T) {
		es, ts := setOf(2, commonv1.DownloadStageDone, commonv1.DownloadStageDone)
		es[0].Phase, es[1].Phase = commonv1.DownloadPhaseCompleted, commonv1.DownloadPhaseCompleted
		v := view(es, ts...)
		v.Import = map[types.UID]lifecycle.ImportDecision{types.UID(es[0].UID): {
			Summary: catalogv1alpha1.DownloadImportSummary{Phase: catalogv1alpha1.ImportPhaseBlocked, Message: "the parts did not join"},
			Verdict: lifecycle.ImportReleaseFault,
		}}
		p := lifecycle.Decide(v)
		assert.Equal(t, commonv1.DownloadPhaseBlocklisted, entryNamed(p, "heat-part1").Phase)
		v = next(v, p)
		v.Import = nil
		p = lifecycle.Decide(v)
		assert.Equal(t, commonv1.DownloadPhaseRemoving, entryNamed(p, "heat-part2").Phase)
	})
	t.Run("a part still Pending is dropped, never added", func(t *testing.T) {
		es, ts := setOf(2, commonv1.DownloadStageDone, commonv1.DownloadStageDone)
		es[0].Phase, es[0].Client, es[0].Engine, es[0].Dispatch = commonv1.DownloadPhasePending, "", "", catalogv1alpha1.Dispatch{}
		es[1].Phase = commonv1.DownloadPhaseBlocklisted
		v := view(es, ts[1])
		p := lifecycle.Decide(v)
		for _, c := range p.Commands {
			assert.NotEqual(t, schema.EngineDesiredPresent, c.Cmd.Desired, "the dead set's Pending part is not added")
		}
		if e := entryNamed(p, "heat-part1"); e != nil {
			assert.Equal(t, commonv1.DownloadPhaseRemoving, e.Phase)
		}
	})
	t.Run("an imported set's parts are no failure", func(t *testing.T) {
		es, ts := setOf(2, commonv1.DownloadStageDone, commonv1.DownloadStageDone)
		es[0].Phase, es[0].Import = commonv1.DownloadPhaseRemoving, importedSummary(1)
		es[1].Phase, es[1].Import = commonv1.DownloadPhaseImported, importedSummary(0)
		es[1].RemoveOnImport = false
		v := view(es, ts...)
		v.Clients[1].RemoveCompleted = false
		p := lifecycle.Decide(v)
		assert.Equal(t, commonv1.DownloadPhaseImported, entryNamed(p, "heat-part2").Phase,
			"the first part's removal after the import does not remove the second")
	})
}

// SetReady is the downloads stage's gate on the first part's import:
// every part present and done.
func TestSetReadyWantsEveryPartDone(t *testing.T) {
	es, ts := setOf(2, commonv1.DownloadStageDone, commonv1.DownloadStageTransferring)
	recs := map[string]*schema.TransferRecord{}
	for i := range ts {
		recs[es[i].UID] = &ts[i].Record
	}
	rec := func(e *catalogv1alpha1.DownloadEntry) *schema.TransferRecord { return recs[e.UID] }
	assert.False(t, lifecycle.SetReady(es, &es[0], rec))
	recs[es[1].UID].Stage = commonv1.DownloadStageDone
	assert.True(t, lifecycle.SetReady(es, &es[0], rec))
	assert.False(t, lifecycle.SetReady(es[:1], &es[0], rec), "a part missing")
}

// A part re-created from its transfer's claim (its entry lost with the
// owner's status) is still a part of its set, never a movie of its own.
func TestARecreatedPartKeepsItsSet(t *testing.T) {
	es, ts := setOf(2, commonv1.DownloadStageTransferring, commonv1.DownloadStageTransferring)
	v := view(nil)
	for i := range ts {
		ts[i].Record.Claimed = false
		ts[i].Record.DownloadID = fmt.Sprintf("job-%d", i)
		ts[i].Record.Claim.Set = &schema.SetRef{ID: es[i].Set.ID, Part: es[i].Set.Part, Of: es[i].Set.Of}
		v.Unclaimed = append(v.Unclaimed, ts[i])
	}
	p := lifecycle.Decide(v)
	require.Len(t, p.Entries, 2)
	for _, e := range p.Entries {
		require.NotNilf(t, e.Set, "%s", e.ID)
		assert.Equal(t, "parts:heat", e.Set.ID)
	}
}
