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

package rowcache

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	toolscache "k8s.io/client-go/tools/cache"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// Handlers that register together on a started informer share one replay:
// every row is decoded once for all of them, each gets the same object, and
// each is told of the sync after the last row.
func TestLateHandlersRegisteringTogetherShareOneReplay(t *testing.T) {
	h := newHarness(t, episodes("media", "a", 60)...)
	h.run()
	before := counterValue(t, h.k.codec.decodes[reasonEvent])

	r1, r2, r3 := &recorder{}, &recorder{}, &recorder{}
	var regs []toolscache.ResourceEventHandlerRegistration
	for _, r := range []*recorder{r1, r2, r3} {
		reg, err := h.inf.AddEventHandler(r)
		require.NoError(t, err)
		regs = append(regs, reg)
	}
	for _, reg := range regs {
		require.Eventually(t, reg.HasSynced, 5*time.Second, 10*time.Millisecond)
	}
	for _, r := range []*recorder{r2, r3} {
		require.Equal(t, 60, r.count("add"))
		for i, e := range r1.of("add") {
			assert.True(t, e.initial)
			assert.Same(t, e.obj, r.of("add")[i].obj, "row %d is one object for every member", i)
		}
	}
	assert.Equal(t, float64(60), counterValue(t, h.k.codec.decodes[reasonEvent])-before, "one decode per row, not one per handler")

	// A handler registering after the replay started reads the store on
	// its own.
	late := &recorder{}
	reg, err := h.inf.AddEventHandler(late)
	require.NoError(t, err)
	require.Eventually(t, reg.HasSynced, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, 60, late.count("add"))
	assert.Equal(t, float64(120), counterValue(t, h.k.codec.decodes[reasonEvent])-before)
}

// gate is a handler that holds every add until it is let through, one at a
// time.
type gate struct {
	recorder
	pass chan struct{}
}

func (g *gate) OnAdd(obj any, initial bool) {
	<-g.pass
	g.recorder.OnAdd(obj, initial)
}

// A fast member never runs more than the lead ahead of the slowest, so the
// decoded objects a replay holds are bounded, and a member that leaves frees
// the others.
func TestAReplayBoundsTheFastMemberToItsLead(t *testing.T) {
	h := newHarness(t, episodes("media", "a", 40)...)
	h.inf.replayLead = 4
	h.run()

	slow := &gate{pass: make(chan struct{})}
	fast := &recorder{}
	slowReg, err := h.inf.AddEventHandler(slow)
	require.NoError(t, err)
	fastReg, err := h.inf.AddEventHandler(fast)
	require.NoError(t, err)

	// The slow member takes three rows; the fast one may be at most the
	// lead past it.
	for range 3 {
		slow.pass <- struct{}{}
	}
	require.Eventually(t, func() bool { return fast.count("add") >= 3 }, 5*time.Second, 10*time.Millisecond)
	time.Sleep(100 * time.Millisecond)
	assert.LessOrEqual(t, fast.count("add"), slow.count("add")+h.inf.replayLead+1, "the fast member waits for the slow one")
	assert.Less(t, fast.count("add"), 40)
	assert.False(t, fastReg.HasSynced())

	// The slow handler is removed while blocked in its own callback: the
	// fast one finishes alone, before the slow one's goroutine is let go.
	require.NoError(t, h.inf.RemoveEventHandler(slowReg))
	require.Eventually(t, fastReg.HasSynced, 5*time.Second, 10*time.Millisecond)
	close(slow.pass)
	assert.Equal(t, 40, fast.count("add"))
	for i := range fast.of("add") {
		assert.True(t, fast.of("add")[i].initial)
	}
}

// A handler registering after the store changed does not join the open
// replay, whose snapshot is stale for it: it reads the store as it is.
func TestAHandlerAfterAStoreChangeReplaysTheStoreAsItIs(t *testing.T) {
	eps := episodes("media", "a", 20)
	h := newHarness(t, eps...)
	h.run()

	first := &recorder{}
	reg1, err := h.inf.AddEventHandler(first)
	require.NoError(t, err)

	changed := eps[4].DeepCopy()
	changed.Spec.SeasonNumber = 9
	h.src.Modify(changed)
	require.Eventually(t, func() bool {
		h.inf.mu.Lock()
		defer h.inf.mu.Unlock()
		item, _, _ := h.inf.indexer.GetByKey("media/" + changed.Name)
		return item != nil && item.(*Row) != nil && string(item.(*Row).body) != ""
	}, 5*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool { return first.count("update") == 1 || first.count("add") > 0 }, 5*time.Second, 10*time.Millisecond)

	second := &recorder{}
	reg2, err := h.inf.AddEventHandler(second)
	require.NoError(t, err)
	require.Eventually(t, func() bool { return reg1.HasSynced() && reg2.HasSynced() }, 5*time.Second, 10*time.Millisecond)

	var got *catalogv1alpha1.Episode
	for _, e := range second.of("add") {
		if ep := e.obj.(*catalogv1alpha1.Episode); ep.Name == changed.Name {
			got = ep
		}
	}
	require.NotNil(t, got)
	assert.Equal(t, int32(9), got.Spec.SeasonNumber, "the second handler's replay carries the change")
	assert.Equal(t, 20, second.count("add"))
	assert.Equal(t, 0, second.count("update"), "the change came before its registration")
}
