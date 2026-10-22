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

package transcodeplan

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/transcode/task"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/records"
)

// exclude sets transcode.clustarr.io/exclude on in's file and re-reads its
// eligibility, as the adapter does on the next pass.
func exclude(in *Inputs) {
	in.File = in.File.DeepCopy()
	in.File.Annotations = map[string]string{catalogv1alpha1.AnnotationTranscodeExclude: "true"}
	relook(in)
}

// An excluded file is ineligible, whether the annotation is on the
// MediaFile or on its item (the owner's Mister Rogers' Neighborhood,
// 2026-10-22), and it is not merely pending.
func TestAnExcludedFileIsNeverEligible(t *testing.T) {
	onFile := eligibleFile(probed("ph-1"))
	onFile.MF.Annotations = map[string]string{catalogv1alpha1.AnnotationTranscodeExclude: "true"}
	onItem := eligibleFile(probed("ph-1"))
	onItem.ItemExcluded = true
	for name, f := range map[string]File{"on the file": onFile, "on its item": onItem} {
		t.Run(name, func(t *testing.T) {
			e := Eligible(f, hevcProfiles())
			assert.False(t, e.Eligible)
			assert.True(t, e.Excluded)
			assert.Empty(t, e.Pending, "an exclusion never clears by itself")
			assert.Contains(t, e.Why, catalogv1alpha1.AnnotationTranscodeExclude)
		})
	}
	other := eligibleFile(probed("ph-1"))
	other.MF.Annotations = map[string]string{catalogv1alpha1.AnnotationTranscodeExclude: "false"}
	assert.True(t, Eligible(other, hevcProfiles()).Eligible, "only \"true\" excludes")
}

// A Planned file that becomes excluded gives up its window place, and a
// window grant for one is released: suspend would hold the place, which is
// why the owner's 881 files could not be suspended without stalling the
// backlog.
func TestAnExcludedFileLeavesTheWindow(t *testing.T) {
	in := inputs(nil, probed("ph-1"), GrantWindow)
	planned := pass(in).Block
	require.NotNil(t, planned)
	require.Equal(t, catalogv1alpha1.TranscodePhasePlanned, planned.Phase)

	in.Prev, in.Ledger = planned, Ledger{}
	exclude(&in)
	left := pass(in)
	if left.Block != nil {
		assert.False(t, inWindow(left.Block.Phase), "the excluded file still holds a window place: %+v", left.Block)
	}

	granted := inputs(nil, probed("ph-1"), GrantWindow)
	exclude(&granted)
	d := pass(granted)
	assert.Equal(t, GrantRelease, d.Grant)
	if d.Block != nil {
		assert.False(t, inWindow(d.Block.Phase))
	}
}

// A running attempt is withdrawn once the file is excluded, and closes as
// Skipped, reason Excluded, rather than going back to Planned.
func TestAnExcludedRunningAttemptIsWithdrawnAsSkipped(t *testing.T) {
	in := queuedIn(t)
	in.Prev.Phase = catalogv1alpha1.TranscodePhaseRunning
	claimed := t0.Add(time.Minute)
	in.Record = &task.Record{RecordHeader: schema.RecordHeader{Seq: 42, State: records.StateClaimed, ClaimedAt: &claimed}, Inputs: in.Record.Inputs}
	exclude(&in)
	first := pass(in)
	require.Equal(t, int64(42), first.Withdraw)
	require.NotNil(t, first.Block)
	assert.Equal(t, ReasonExcluded, first.Block.Reason)

	in.Prev = first.Block
	in.Record = withdrawnAt42()
	closed := pass(in)
	require.NotNil(t, closed.Block)
	assert.Equal(t, catalogv1alpha1.TranscodePhaseSkipped, closed.Block.Phase)
	assert.Equal(t, ReasonExcluded, closed.Block.Reason)
	assert.True(t, closed.Block.Dispatch.Withdrawn)
	assert.Zero(t, closed.Withdraw)
}
