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

package graftplan_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/transcode/graftplan"
)

// TestJoinCarriesTheGraftOfAJoinableFile (§5.8 step 4, §5.13): the graft a
// transcode dispatch carries, and the join the transcode block records.
func TestJoinCarriesTheGraftOfAJoinableFile(t *testing.T) {
	st := with(probed("h1", "jpn"), &catalogv1alpha1.GraftState{Phase: catalogv1alpha1.GraftPhaseWaiting, Reason: graftplan.ReasonWaitingForSlot})
	v := graftplan.View{File: theFile(), Prev: &st, Draft: &st, Now: now, ProbeCurrent: true}
	j := graftplan.Join(v, graftplan.JoinInput{Item: theItem(), Root: root, RecycleBin: "/data/media/.recycle"})
	require.NotNil(t, j)
	assert.Equal(t, "en", j.Task.Language)
	assert.Equal(t, donorPath, j.Task.Donor)
	assert.Equal(t, target, j.Task.Target)
	assert.Equal(t, "h1", j.Task.TargetProbeHash)
	assert.True(t, j.Task.Default)
	assert.Zero(t, j.Task.Seq, "a joined task is fenced by the transcode's dispatch")
	assert.Equal(t, catalogv1alpha1.TranscodeGraftJoin{DonorRelease: release, DonorImportedAt: &theDonor().ImportedAt,
		Languages: []string{"en"}, ProbeHash: "h1"}, j.Join)
}

func TestJoinRefusesWhatCannotRideAlong(t *testing.T) {
	cases := map[string]catalogv1alpha1.MediaFileStatus{
		"a standalone graft holds the file": with(probed("h1", "jpn"), flying(catalogv1alpha1.GraftPhaseRunning, 100)),
		"the donor failed these bytes": with(probed("h1", "jpn"), &catalogv1alpha1.GraftState{Phase: catalogv1alpha1.GraftPhaseFailed,
			DonorFault: true, DonorRelease: release, DonorImportedAt: &theDonor().ImportedAt, ProbeHash: "h1"}),
		"the file carries the language": probed("h1", "jpn", "eng"),
	}
	for name, st := range cases {
		v := graftplan.View{File: theFile(), Prev: &st, Draft: &st, Now: now, ProbeCurrent: true}
		assert.Nil(t, graftplan.Join(v, graftplan.JoinInput{Item: theItem(), Root: root}), name)
	}
	st := probed("h1", "jpn")
	v := graftplan.View{File: theFile(), Prev: &st, Draft: &st, Now: now, ProbeCurrent: true}
	assert.Nil(t, graftplan.Join(v, graftplan.JoinInput{Item: theItem()}), "under no RootFolder")
}

// TestJoinPendingHoldsTheNextDispatchUntilTheGraftRecordsItsResult (§5.13).
func TestJoinPendingHoldsTheNextDispatchUntilTheGraftRecordsItsResult(t *testing.T) {
	tc := &catalogv1alpha1.TranscodeState{Dispatch: &catalogv1alpha1.Dispatch{Seq: 200, AnsweredSeq: 200},
		JoinedGraft: &catalogv1alpha1.TranscodeGraftJoin{ProbeHash: "h1"}}
	s, pending := graftplan.JoinPending(tc, &catalogv1alpha1.GraftState{Phase: catalogv1alpha1.GraftPhaseRunning, Reason: graftplan.ReasonJoinedTranscode, JoinedTranscodeSeq: 200})
	assert.Equal(t, int64(200), s)
	assert.True(t, pending)
	_, pending = graftplan.JoinPending(tc, &catalogv1alpha1.GraftState{Phase: catalogv1alpha1.GraftPhaseSwapping, JoinedTranscodeSeq: 200})
	assert.False(t, pending)
	_, pending = graftplan.JoinPending(tc, &catalogv1alpha1.GraftState{Phase: catalogv1alpha1.GraftPhaseWaiting,
		Reason: graftplan.ReasonWaitingForSlot, JoinedTranscodeSeq: 150})
	assert.True(t, pending, "a graft block that never derived the join still owes it")
	_, pending = graftplan.JoinPending(tc, &catalogv1alpha1.GraftState{Phase: catalogv1alpha1.GraftPhaseWaiting,
		Reason: graftplan.ReasonWaitingForSlot, JoinedTranscodeSeq: 200})
	assert.False(t, pending, "the join ended without its graft, and the block recorded that")
	_, pending = graftplan.JoinPending(tc, nil)
	assert.False(t, pending, "no graft block is left to read the record: a hold would keep the transcode from ever dispatching")
	_, pending = graftplan.JoinPending(&catalogv1alpha1.TranscodeState{Dispatch: &catalogv1alpha1.Dispatch{Seq: 200}},
		&catalogv1alpha1.GraftState{Phase: catalogv1alpha1.GraftPhaseWaiting})
	assert.False(t, pending, "no join, nothing to wait for")
}

// TestAPanickingGraftPlannerNeverLeavesAJoinedTaskBesideAWaitingBlock
// (§5.13): the transcode planner joined in pass 1 and the graft planner
// failed there, so its Waiting block stayed (the loop's isolation keeps the
// last block, F3.1). Admission never hands that file a graft slot (see
// admission's TestAJoinedGraftIsNeverAGraftSlotCandidate), the transcode
// holds its next dispatch, and pass 2 derives the joined Running.
func TestAPanickingGraftPlannerNeverLeavesAJoinedTaskBesideAWaitingBlock(t *testing.T) {
	waiting := &catalogv1alpha1.GraftState{Phase: catalogv1alpha1.GraftPhaseWaiting, Reason: graftplan.ReasonWaitingForSlot, ProbeHash: "h1"}
	afterPass1 := with(probed("h1", "jpn"), waiting)
	afterPass1.Transcode = &catalogv1alpha1.TranscodeState{Phase: catalogv1alpha1.TranscodePhaseQueued,
		Dispatch:    &catalogv1alpha1.Dispatch{Seq: 200, DispatchedAt: now},
		JoinedGraft: &catalogv1alpha1.TranscodeGraftJoin{DonorRelease: release, Languages: []string{"en"}, ProbeHash: "h1"}}
	assert.True(t, graftplan.JoinInFlight(afterPass1.Transcode))
	_, held := graftplan.JoinPending(afterPass1.Transcode, afterPass1.Graft)
	assert.True(t, held, "the transcode's next dispatch waits for the graft to record the join")
	granted := ready()
	granted.Granted = true
	pass2, d := pass(afterPass1, granted)
	assert.Equal(t, catalogv1alpha1.GraftPhaseRunning, pass2.Graft.Phase)
	assert.Equal(t, graftplan.ReasonJoinedTranscode, pass2.Graft.Reason)
	assert.Empty(t, d.Effects, "a stray graft grant is not used beside the joined task")
}
