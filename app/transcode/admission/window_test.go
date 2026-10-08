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

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/types"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/transcode/transcodeplan"
)

// TestWindowCountsWindowPhasesAndGrants is §5.11 "The window holds files":
// Pending, Planned, Queued, Running and Swapping hold a place, a verdict or
// a finished transcode does not, and an outstanding window grant does.
func TestWindowCountsWindowPhasesAndGrants(t *testing.T) {
	l, _ := newTestLedger(t, map[string]int32{"cpu": 0}, 3)
	for i, p := range []catalogv1alpha1.TranscodePhase{pending, planned, queued, running, swapping, skipped,
		catalogv1alpha1.TranscodePhaseSucceeded, catalogv1alpha1.TranscodePhaseFailed} {
		v := fv(string(rune('a'+i)), int64(i+1), p)
		v.Eligible = windowPhase(p) // a Succeeded file is Transcoded()
		v.VerdictHolds = p == skipped || p == catalogv1alpha1.TranscodePhaseFailed
		l.Observe(v)
	}
	assert.Equal(t, 5, l.Open("hevc"), "Pending, Planned, Queued, Running, Swapping")
	backlog := fv("z", 1, "")
	l.Observe(backlog)
	l.Pass(passIn())
	assert.Equal(t, transcodeplan.GrantNone, l.Snapshot("z").Grant, "the window of 3 is full")
	assert.Equal(t, 1, l.Backlog("hevc"), "z waits in the backlog")
	l.SetWindow(0) // --job-window 0 is unlimited
	l.Pass(passIn())
	assert.Equal(t, transcodeplan.GrantWindow, l.Snapshot("z").Grant)
	assert.Equal(t, 6, l.Open("hevc"), "an outstanding window grant counts")
	assert.Zero(t, l.Backlog("hevc"), "a granted file left the backlog")

	l.Observe(fv("z", 2, pending))
	assert.Equal(t, transcodeplan.GrantNone, l.Snapshot("z").Grant, "the Pending block holds the place now, not the grant")
	assert.Equal(t, 6, l.Open("hevc"), "counted once")
}

// TestFillWindowTakesPriorityThenName is §5.11 "Filling it": effective
// priority descending, then namespace/name.
func TestFillWindowTakesPriorityThenName(t *testing.T) {
	l, _ := newTestLedger(t, map[string]int32{"cpu": 0}, 2)
	for _, name := range []string{"d", "b", "c", "a"} {
		v := fv(name, 1, "")
		if name == "d" {
			v.Priority = 10
		}
		l.Observe(v)
	}
	l.Pass(passIn())
	var granted []string
	for _, name := range []string{"a", "b", "c", "d"} {
		if l.Snapshot(types.UID(name)).Grant == transcodeplan.GrantWindow {
			granted = append(granted, name)
		}
	}
	assert.Equal(t, []string{"a", "d"}, granted)
	assert.Equal(t, 2, l.Backlog("hevc"), "waitingFiles: c and b")
}
