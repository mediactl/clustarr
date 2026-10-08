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
	"k8s.io/apimachinery/pkg/types"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/transcode/transcodeplan"
)

var t0 = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

const (
	pending    = catalogv1alpha1.TranscodePhasePending
	planned    = catalogv1alpha1.TranscodePhasePlanned
	queued     = catalogv1alpha1.TranscodePhaseQueued
	running    = catalogv1alpha1.TranscodePhaseRunning
	swapping   = catalogv1alpha1.TranscodePhaseSwapping
	skipped    = catalogv1alpha1.TranscodePhaseSkipped
	cpu        = transcodev1alpha1.HardwareCPU
	nvidia     = transcodev1alpha1.HardwareNVIDIA
	profileUID = "uid-hevc"
)

// fv is one auto file of profile hevc that encodes, eligible, planned at t0.
func fv(uid string, seq int64, phase catalogv1alpha1.TranscodePhase) FileView {
	return FileView{
		UID: types.UID(uid), Key: types.NamespacedName{Namespace: "media", Name: uid}, Seq: seq, Phase: phase,
		Profile: "hevc", ProfileUID: profileUID, Class: cpu, Hardware: transcodev1alpha1.HardwareAuto, Encodes: true,
		PlannedAt: t0, Eligible: true, EligibleKnown: true,
	}
}

func newTestLedger(t *testing.T, slots map[string]int32, window int) (*Ledger, *clockwork.FakeClock) {
	t.Helper()
	clk := clockwork.NewFakeClockAt(t0)
	l := NewLedger(Options{Slots: slots, Window: window, Clock: clk})
	l.markRebuilt() // these tests do not exercise Rebuild
	return l, clk
}

func passIn() PassInputs {
	return PassInputs{Profiles: map[string]ProfileView{"hevc": {Name: "hevc", UID: profileUID}}}
}

// phaseOf is the phase the ledger holds for uid.
func (l *Ledger) phaseOf(uid types.UID) catalogv1alpha1.TranscodePhase {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e := l.files[uid]; e != nil {
		return e.view.Phase
	}
	return ""
}

// inFlight is the files of class whose block holds a slot (Queued or
// Running), the way candidatesLocked counts them.
func (l *Ledger) inFlight(class transcodev1alpha1.Hardware) int {
	return l.countFiles(func(v FileView) bool { return v.Class == class })
}

// inFlightProfile is the same count for one profile, every class.
func (l *Ledger) inFlightProfile(profile string) int {
	return l.countFiles(func(v FileView) bool { return v.Profile == profile })
}

func (l *Ledger) countFiles(match func(FileView) bool) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, e := range l.files {
		if slotPhase(e.view.Phase) && !l.orphanedLocked(e.view) && match(e.view) {
			n++
		}
	}
	return n
}

// dispatchGrants is class's live dispatch grants (in use, or unexpired)
// of files whose block does not already hold the slot.
func (l *Ledger) dispatchGrants(class transcodev1alpha1.Hardware) int {
	return l.countGrants(func(g *grant) bool { return g.class == class })
}

// dispatchGrantsProfile is the same count for one profile.
func (l *Ledger) dispatchGrantsProfile(profile string) int {
	return l.countGrants(func(g *grant) bool { return g.profile == profile })
}

func (l *Ledger) countGrants(match func(*grant) bool) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.clock.Now()
	n := 0
	for uid, g := range l.grants {
		if g.kind != transcodeplan.GrantDispatch || (!g.inUse && !now.Before(g.expires)) || !match(g) {
			continue
		}
		if e := l.files[uid]; e != nil && slotPhase(e.view.Phase) {
			continue
		}
		n++
	}
	return n
}

// TestObserveIgnoresAStaleObservation is §5.5's (seq, rank) merge, the
// absent block included: rank(Queued)=1, rank(Running)=2, anything else 3.
func TestObserveIgnoresAStaleObservation(t *testing.T) {
	for _, tc := range []struct {
		name        string
		first, then FileView
		want        catalogv1alpha1.TranscodePhase
	}{
		{"Queued(5) after Running(5) is ignored", fv("a", 5, running), fv("a", 5, queued), running},
		{"Planned(4) after Queued(5) is ignored", fv("a", 5, queued), fv("a", 4, planned), queued},
		{"no block(4) after Queued(5) is ignored", fv("a", 5, queued), fv("a", 4, ""), queued},
		{"Queued(5) then Running(5)", fv("a", 5, queued), fv("a", 5, running), running},
		{"Running(5) then Planned(5), a requeue", fv("a", 5, running), fv("a", 5, planned), planned},
		{"Running(5) then Swapping(5)", fv("a", 5, running), fv("a", 5, swapping), swapping},
		{"Skipped(5) then Planned(5), a retry", fv("a", 5, skipped), fv("a", 5, planned), planned},
		{"Queued(5) then no block(5)", fv("a", 5, queued), fv("a", 5, ""), ""},
		{"Running(5) then Queued(6), a new dispatch", fv("a", 5, running), fv("a", 6, queued), queued},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l, _ := newTestLedger(t, map[string]int32{"cpu": 1}, 0)
			l.Observe(tc.first)
			l.Observe(tc.then)
			assert.Equal(t, tc.want, l.phaseOf("a"))
		})
	}
}

// TestAnAbsentBlockFreesItsSlot is the fuzz invariant's "never under-admits
// after a clear": once an applied status has no block, the file holds no
// slot after the next Observe, and another file is granted it.
func TestAnAbsentBlockFreesItsSlot(t *testing.T) {
	l, _ := newTestLedger(t, map[string]int32{"cpu": 1}, 0)
	l.Observe(fv("a", 5, queued))
	l.Observe(fv("b", 6, planned))
	l.Pass(passIn())
	assert.Equal(t, transcodeplan.GrantNone, l.Snapshot("b").Grant, "a holds the one cpu slot")
	assert.Equal(t, "waiting for a free cpu slot", l.Snapshot("b").WaitReason)
	l.Observe(fv("a", 5, ""))
	l.Pass(passIn())
	assert.Equal(t, transcodeplan.GrantDispatch, l.Snapshot("b").Grant)
	assert.Equal(t, cpu, l.Snapshot("b").Class)
	assert.Empty(t, l.Snapshot("b").WaitReason, "a granted file waits for nothing")
}

// TestGrantInUseNeverExpires and TestExpiredGrantBacksOff are §5.5 "Grants".
func TestGrantInUseNeverExpires(t *testing.T) {
	l, clk := newTestLedger(t, map[string]int32{"cpu": 1}, 0)
	l.Observe(fv("a", 1, planned))
	l.Observe(fv("b", 1, planned))
	l.Pass(passIn())
	require.Equal(t, transcodeplan.GrantDispatch, l.Snapshot("a").Grant, "a is first by name")
	_, ok := l.TakeGrant("a")
	require.True(t, ok)
	clk.Advance(grantTTL * 3)
	l.Pass(passIn())
	assert.Equal(t, transcodeplan.GrantDispatch, l.Snapshot("a").Grant, "a grant in use outlives its TTL")
	assert.Equal(t, transcodeplan.GrantNone, l.Snapshot("b").Grant, "and keeps holding the one slot")
	l.Confirm("a", 2)
	l.Observe(fv("a", 2, queued))
	assert.Equal(t, 1, l.inFlight(cpu))
	assert.Zero(t, l.dispatchGrants(cpu), "the Queued block holds the slot now, not the grant")
}

func TestExpiredGrantBacksOff(t *testing.T) {
	l, clk := newTestLedger(t, map[string]int32{"cpu": 1}, 0)
	l.Observe(fv("a", 1, planned))
	l.Pass(passIn())
	require.Equal(t, transcodeplan.GrantDispatch, l.Snapshot("a").Grant)
	clk.Advance(grantTTL + time.Second) // never taken
	assert.Equal(t, transcodeplan.GrantNone, l.Snapshot("a").Grant, "an expired grant is not shown")
	_, ok := l.TakeGrant("a")
	assert.False(t, ok, "nor taken")
	l.Pass(passIn())
	assert.Equal(t, transcodeplan.GrantNone, l.Snapshot("a").Grant)
	clk.Advance(30 * time.Second)
	l.Pass(passIn())
	assert.Equal(t, transcodeplan.GrantNone, l.Snapshot("a").Grant, "a 1-minute backoff")
	clk.Advance(31 * time.Second)
	l.Pass(passIn())
	assert.Equal(t, transcodeplan.GrantDispatch, l.Snapshot("a").Grant)
	clk.Advance(grantTTL + time.Second)
	l.Pass(passIn())
	clk.Advance(61 * time.Second)
	l.Pass(passIn())
	assert.Equal(t, transcodeplan.GrantNone, l.Snapshot("a").Grant, "the backoff doubled to 2 minutes")
	clk.Advance(60 * time.Second)
	l.Pass(passIn())
	assert.Equal(t, transcodeplan.GrantDispatch, l.Snapshot("a").Grant, "and ended")
}

// TestWaitReasonEnqueuesOnlyOnChange: a held candidate is enqueued (and its
// Planned message written) only when its reason changes (§5.7 step 7).
func TestWaitReasonEnqueuesOnlyOnChange(t *testing.T) {
	l, _ := newTestLedger(t, map[string]int32{"cpu": 0}, 0)
	var woken []types.NamespacedName
	var prios []int
	l.setWaker(func(nn types.NamespacedName, prio int) { woken, prios = append(woken, nn), append(prios, prio) })
	l.Observe(fv("a", 1, planned))
	for range 5 {
		l.Pass(passIn())
	}
	assert.Equal(t, []types.NamespacedName{{Namespace: "media", Name: "a"}}, woken)
	assert.Equal(t, []int{GrantPriority}, prios)
	assert.Equal(t, "waiting for a free cpu slot", l.Snapshot("a").WaitReason)
	assert.Equal(t, transcodeplan.ReasonWaitingForSlot, l.Snapshot("a").WaitCause)

	v := fv("a", 1, planned)
	v.Hardware = transcodev1alpha1.HardwareGPU
	l.Observe(v)
	in := passIn()
	in.GPUNodes = map[transcodev1alpha1.Hardware]bool{nvidia: true}
	for range 3 {
		l.Pass(in)
	}
	assert.Len(t, woken, 2, "the reason changed once: a gpu file waits for a GPU slot")
	assert.Equal(t, WaitingForGPU, l.Snapshot("a").WaitReason)
	assert.Equal(t, transcodeplan.ReasonWaitingForGPU, l.Snapshot("a").WaitCause)
}
