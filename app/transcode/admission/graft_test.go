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

package admission

import (
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/transcode/graftplan"
	"github.com/mediactl/clustarr/app/transcode/transcodeplan"
)

// TestGraftSlotsCountJobsStatusAndGrants (§5.13 "Graft slots"): the union,
// by Job name, of unfinished graft Jobs and blocks that hold a slot, plus
// outstanding graft-slot grants; a joined graft holds none.
func TestGraftSlotsCountJobsStatusAndGrants(t *testing.T) {
	ns := "media"
	files := []GraftFile{
		{UID: "a", Key: types.NamespacedName{Namespace: ns, Name: "a"}, Phase: catalogv1alpha1.GraftPhaseQueued, JobName: "a-graft-1"},
		{UID: "b", Key: types.NamespacedName{Namespace: ns, Name: "b"}, Phase: catalogv1alpha1.GraftPhaseRunning, Reason: graftplan.ReasonJoinedTranscode},
		{UID: "c", Key: types.NamespacedName{Namespace: ns, Name: "c"}, Phase: catalogv1alpha1.GraftPhaseRunning, JobName: "c-graft-1"},
		{UID: "d", Key: types.NamespacedName{Namespace: "other", Name: "d"}, Phase: catalogv1alpha1.GraftPhaseQueued, JobName: "d-graft-1"},
		{UID: "e", Key: types.NamespacedName{Namespace: ns, Name: "e"}, Phase: catalogv1alpha1.GraftPhaseWaiting, Reason: graftplan.ReasonWaitingForSlot},
		{UID: "f", Key: types.NamespacedName{Namespace: ns, Name: "f"}, Phase: catalogv1alpha1.GraftPhaseSwapping},
	}
	jobs := map[string][]string{ns: {"a-graft-1", "z-orphan"}}
	assert.Equal(t, 4, GraftInUse(ns, files, jobs, map[string]int{ns: 1}), "a (block and Job once), c, z-orphan, one grant")
	assert.Equal(t, 1, GraftInUse("other", files, jobs, nil))
	assert.Zero(t, GraftInUse("empty", files, jobs, nil))

	noName := []GraftFile{
		{UID: "x", Key: types.NamespacedName{Namespace: ns, Name: "x"}, Phase: catalogv1alpha1.GraftPhaseQueued},
		{UID: "y", Key: types.NamespacedName{Namespace: ns, Name: "y"}, Phase: catalogv1alpha1.GraftPhaseQueued},
	}
	assert.Equal(t, 2, GraftInUse(ns, noName, nil, nil), "a block whose Job name is not yet written still holds its slot")
}

func TestGraftGrantsFillFreeSlotsByDonorImportedAtThenKey(t *testing.T) {
	w := func(name string, at time.Time) GraftFile {
		return GraftFile{UID: types.UID(name), Key: types.NamespacedName{Namespace: "media", Name: name},
			Phase: catalogv1alpha1.GraftPhaseWaiting, Reason: graftplan.ReasonWaitingForSlot, DonorImportedAt: at}
	}
	files := []GraftFile{w("x", t0.Add(2*time.Hour)), w("y", t0.Add(time.Hour)), w("a", t0.Add(time.Hour))}
	assert.Equal(t, []types.UID{"a", "y"}, GraftGrants(files, nil, nil, 2))
	assert.Equal(t, []types.UID{"a"}, GraftGrants(files, map[string][]string{"media": {"busy"}}, nil, 2))
	assert.Empty(t, GraftGrants(files, nil, map[string]int{"media": 2}, 2))
	assert.Len(t, GraftGrants(files, nil, nil, 0), graftplan.DefaultConcurrency, "0 is the default")

	other := w("b", t0)
	other.Key.Namespace = "anime"
	assert.Equal(t, []types.UID{"b", "a"}, GraftGrants(append(files, other), map[string][]string{"media": {"busy"}}, nil, 2),
		"each namespace has its own slots")
}

// TestAJoinedGraftIsNeverAGraftSlotCandidate (§5.13): a file whose
// transcode block carries an in-flight join, a file in backoff (its
// block's or an expired grant's) and a file already granted are not
// candidates.
func TestAJoinedGraftIsNeverAGraftSlotCandidate(t *testing.T) {
	base := GraftFile{UID: "f", Key: types.NamespacedName{Namespace: "media", Name: "f"},
		Phase: catalogv1alpha1.GraftPhaseWaiting, Reason: graftplan.ReasonWaitingForSlot}
	joined, backoff, expired, granted := base, base, base, base
	joined.JoinInFlight = true
	backoff.Reason = graftplan.ReasonBackoff
	expired.OnBackoff = true
	granted.Granted = true
	for name, f := range map[string]GraftFile{"joined": joined, "backoff": backoff, "expired grant": expired, "granted": granted} {
		assert.Empty(t, GraftGrants([]GraftFile{f}, nil, nil, 2), name)
	}
	assert.Equal(t, []types.UID{"f"}, GraftGrants([]GraftFile{base}, nil, nil, 2))
}

// TestTheLedgerGrantsAGraftSlotAndTheLoopTakesIt: the pass's step 9 issues
// a GrantGraftSlot the loop's Snapshot shows, TakeGrant marks it in use,
// and Confirm drops it; while it is outstanding it holds the namespace's
// one slot, and a joined transcode never gets one.
func TestTheLedgerGrantsAGraftSlotAndTheLoopTakesIt(t *testing.T) {
	l := NewLedger(Options{Slots: map[string]int32{"cpu": 1}, GraftConcurrency: 1, Clock: clockwork.NewFakeClockAt(t0)})
	l.markRebuilt()
	var woken []types.NamespacedName
	l.setWaker(func(nn types.NamespacedName, _ int) { woken = append(woken, nn) })
	waiting := func(name string, at time.Time) (*catalogv1alpha1.MediaFile, *catalogv1alpha1.MediaFileStatus, *transcodeplan.Eligibility) {
		return &catalogv1alpha1.MediaFile{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: name, UID: types.UID(name)}},
			&catalogv1alpha1.MediaFileStatus{LastSeq: 1, Graft: &catalogv1alpha1.GraftState{Phase: catalogv1alpha1.GraftPhaseWaiting,
				Reason: graftplan.ReasonWaitingForSlot, DonorImportedAt: &metav1.Time{Time: at}}}, nil
	}
	l.Observe(FileViewOf(waiting("f", t0)))
	l.Observe(FileViewOf(waiting("g", t0.Add(time.Hour))))
	mf, st, _ := waiting("j", t0.Add(-time.Hour))
	st.Transcode = &catalogv1alpha1.TranscodeState{Phase: catalogv1alpha1.TranscodePhaseQueued,
		Dispatch:    &catalogv1alpha1.Dispatch{Seq: 1, DispatchedAt: metav1.NewTime(t0)},
		JoinedGraft: &catalogv1alpha1.TranscodeGraftJoin{ProbeHash: "h1"}}
	l.Observe(FileViewOf(mf, st, nil))

	l.Pass(PassInputs{})
	assert.Equal(t, transcodeplan.GrantGraftSlot, l.Snapshot("f").Grant, "the oldest donor first")
	assert.Equal(t, transcodeplan.GrantNone, l.Snapshot("g").Grant, "the one slot is f's grant's")
	assert.Equal(t, transcodeplan.GrantNone, l.Snapshot("j").Grant, "a joined graft rides its transcode")
	assert.Equal(t, []types.NamespacedName{{Namespace: "media", Name: "f"}}, woken)

	snap, ok := l.TakeGrant("f")
	require.True(t, ok)
	assert.Equal(t, transcodeplan.GrantGraftSlot, snap.Grant)
	l.Confirm("f", 2)
	assert.Equal(t, transcodeplan.GrantNone, l.Snapshot("f").Grant)
	mf, st, _ = waiting("f", t0)
	st.LastSeq = 2
	st.Graft.Phase, st.Graft.Reason, st.Graft.JobName = catalogv1alpha1.GraftPhaseQueued, "", "f-graft-2"
	l.Observe(FileViewOf(mf, st, nil))
	l.Pass(PassInputs{})
	assert.Equal(t, transcodeplan.GrantNone, l.Snapshot("g").Grant, "f's Queued block holds the slot now")
	l.Pass(PassInputs{GraftJobs: map[string][]string{"media": {"f-graft-2"}}})
	assert.Equal(t, transcodeplan.GrantNone, l.Snapshot("g").Grant, "its Job and its block are one slot, still taken")
}
