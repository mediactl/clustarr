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
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/transcode/graftplan"
	"github.com/mediactl/clustarr/app/transcode/transcodeplan"
)

// A pass allocates nothing per file it only counts. On talos (2026-11-03)
// every pass built a GraftFile of each of 14,300 files and a key string for
// each, a third and a fifth of everything the manager allocated, at up to 39
// passes a second.
func TestAPassAllocatesNothingPerIdleFile(t *testing.T) {
	small, large := bytesPerPass(t, 20), bytesPerPass(t, 2000)
	assert.LessOrEqual(t, large-small, int64(4096),
		"a pass allocated %d bytes with 2,000 idle files and %d with 20", large, small)
}

// bytesPerPass is the bytes a pass allocates over a full window of 3 and n
// idle files (allocsPerPass counts allocations, which a slice of every file
// grows in a dozen).
func bytesPerPass(t *testing.T, n int) int64 {
	t.Helper()
	l, _ := newTestLedger(t, map[string]int32{"cpu": 0}, 3)
	for i := range 3 {
		l.Observe(fv(fmt.Sprintf("open-%d", i), 1, pending))
	}
	for i := range n {
		l.Observe(fv(fmt.Sprintf("the-sopranos-1999-a1b2c3d4e5-s01e01-%05d", i), 1, ""))
	}
	in := passIn()
	l.Pass(in)
	return testing.Benchmark(func(b *testing.B) {
		for range b.N {
			l.Pass(in)
		}
	}).AllocedBytesPerOp()
}

// The graft-slot count still sees every file with a graft in play.
func TestGraftSlotsAreGrantedAmongTheFilesWithAGraftInPlay(t *testing.T) {
	l, _ := newTestLedger(t, map[string]int32{"cpu": 2}, 0)
	l.o.GraftConcurrency = 1
	for i := range 50 {
		l.Observe(fv("idle-"+string(rune('a'+i%26))+string(rune('a'+i/26)), 1, ""))
	}
	// Neither file wants a transcode: a window grant would make the waiting
	// graft no candidate (graftCandidate).
	waiting := fv("waiting", 1, "")
	waiting.Eligible, waiting.GraftPhase, waiting.GraftReason = false, catalogv1alpha1.GraftPhaseWaiting, graftplan.ReasonWaitingForSlot
	waiting.DonorImportedAt = t0
	running := fv("running", 1, "")
	running.Eligible, running.GraftPhase, running.GraftJobName = false, catalogv1alpha1.GraftPhaseRunning, "graft-running"
	l.Observe(waiting)
	l.Observe(running)

	l.Pass(passIn())
	assert.Nil(t, l.grants[types.UID("waiting")], "the one slot is held by the running graft's Job")

	finished := running
	finished.Seq, finished.GraftPhase, finished.GraftJobName = 2, catalogv1alpha1.GraftPhaseSucceeded, ""
	l.Observe(finished)
	l.Pass(passIn())
	g := l.grants[types.UID("waiting")]
	require.NotNil(t, g, "the slot the finished graft freed goes to the waiting one")
	assert.Equal(t, transcodeplan.GrantGraftSlot, g.kind)
}

// A wake inside passMinInterval of the last pass waits for the interval's
// end rather than running a pass; one at or past it runs.
func TestAWakeInsideThePassIntervalWaitsForItsEnd(t *testing.T) {
	r := &Reconciler{}
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	_, ok := r.throttled(now)
	assert.False(t, ok, "the first pass is never throttled")

	r.nextPassAt = now.Add(passMinInterval)
	wait, ok := r.throttled(now.Add(300 * time.Millisecond))
	assert.True(t, ok)
	assert.Equal(t, 700*time.Millisecond, wait, "the wake is answered when the interval ends")

	_, ok = r.throttled(now.Add(passMinInterval))
	assert.False(t, ok, "a wake at the interval's end passes")
}
