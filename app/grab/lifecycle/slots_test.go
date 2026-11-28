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
	"github.com/mediactl/clustarr/app/grab/lifecycle"
)

// lateClient is a usenet client of n engines that binds late.
func lateClient(n int32) lifecycle.Client {
	c := usenetClient()
	c.Replicas, c.LateBinding, c.MaxActive = n, true, 8
	return c
}

// slotEngine is a Ready engine of sab running transfers at once and
// holding pinned grabs.
func slotEngine(ordinal, transfers, pinned int32) lifecycle.Engine {
	e := engine("sab", ordinal)
	e.Transfers, e.Pinned = transfers, pinned
	return e
}

// Engines fill by the share of their transfers in use: the small one is
// full after its two (and the look-ahead), the rest go to the large one,
// as the owner describes the 1G and 2.5G nodes (2026-11-27).
func TestChooseSlotFillsEnginesByFraction(t *testing.T) {
	c := lateClient(2)
	es := map[string]lifecycle.Engine{"sab-0": slotEngine(0, 8, 0), "sab-1": slotEngine(1, 2, 0)}
	var got []int32
	for range 14 {
		o, ok, _ := lifecycle.ChooseSlot(c, es, 0, now)
		require.True(t, ok)
		got = append(got, o)
		name := lifecycle.EngineName("sab", o)
		e := es[name]
		e.Pinned++
		es[name] = e
	}
	assert.Equal(t, int32(10), es["sab-0"].Pinned, "eight transfers and the look-ahead of two")
	assert.Equal(t, int32(4), es["sab-1"].Pinned, "two transfers and the look-ahead of two")
	assert.Equal(t, int32(0), got[0], "ties go to the lowest ordinal")
	assert.Equal(t, int32(1), got[1], "then to the engine with the lower share in use")
	_, ok, ready := lifecycle.ChooseSlot(c, es, 0, now)
	assert.False(t, ok, "every engine is full")
	assert.True(t, ready)
}

func TestChooseSlotRules(t *testing.T) {
	c := lateClient(2)
	full := func() map[string]lifecycle.Engine {
		return map[string]lifecycle.Engine{"sab-0": slotEngine(0, 8, 10), "sab-1": slotEngine(1, 2, 1)}
	}

	es := full()
	o, ok, _ := lifecycle.ChooseSlot(c, es, 0, now)
	require.True(t, ok)
	assert.Equal(t, int32(1), o, "the one engine with a slot")

	_, ok, _ = lifecycle.ChooseSlot(c, es, 3, now)
	assert.False(t, ok, "three slots are free and three grabs wait ahead: not this one's turn")
	_, ok, _ = lifecycle.ChooseSlot(c, es, 2, now)
	assert.True(t, ok, "two wait ahead of three free slots")

	es = full()
	e := es["sab-1"]
	e.Draining = true
	es["sab-1"] = e
	_, ok, ready := lifecycle.ChooseSlot(c, es, 0, now)
	assert.False(t, ok, "a draining engine is given nothing new")
	assert.True(t, ready)

	es = full()
	e = es["sab-1"]
	e.Ready = false
	es["sab-1"] = e
	_, ok, _ = lifecycle.ChooseSlot(c, es, 0, now)
	assert.False(t, ok, "an engine that is not ready has no slot")

	_, ok, ready = lifecycle.ChooseSlot(lateClient(0), map[string]lifecycle.Engine{}, 0, now)
	assert.False(t, ok)
	assert.False(t, ready, "a client scaled to no engines has none ready")

	// An engine that states no transfers has its client's maxActive.
	es = map[string]lifecycle.Engine{"sab-0": slotEngine(0, 0, 9)}
	_, ok, _ = lifecycle.ChooseSlot(lateClient(1), es, 0, now)
	assert.True(t, ok, "eight and two: the tenth slot")
	es["sab-0"] = slotEngine(0, 0, 10)
	_, ok, _ = lifecycle.ChooseSlot(lateClient(1), es, 0, now)
	assert.False(t, ok)
}

// pendingUsenet is a grab not yet assigned.
func pendingUsenet(uid string) catalogv1alpha1.DownloadEntry {
	e := usenetEntry(commonv1.DownloadPhasePending)
	e.ID, e.UID = "id-"+uid, uid
	e.Client, e.Engine = "", ""
	e.Dispatch = catalogv1alpha1.Dispatch{}
	return e
}

func entryByUID(t *testing.T, p lifecycle.Plan, uid string) catalogv1alpha1.DownloadEntry {
	t.Helper()
	for _, e := range p.Entries {
		if e.UID == uid {
			return e
		}
	}
	t.Fatalf("no entry %s in the plan", uid)
	return catalogv1alpha1.DownloadEntry{}
}

// A late-binding client's grab is assigned only while an engine has a
// slot: the next stays Pending on the client, with the reason the fleet
// controller and the stage count waiters by, and no command.
func TestALateBindingGrabWaitsForASlot(t *testing.T) {
	v := view(nil)
	v.Clients = []lifecycle.Client{lateClient(1)}
	v.Engines = map[string]lifecycle.Engine{"sab-0": slotEngine(0, 1, 2)} // one transfer + look-ahead of two
	v.NewEntries = []catalogv1alpha1.DownloadEntry{pendingUsenet("u1"), pendingUsenet("u2")}
	p := lifecycle.Decide(v)

	first, second := entryByUID(t, p, "u1"), entryByUID(t, p, "u2")
	assert.Equal(t, commonv1.DownloadPhaseAssigned, first.Phase)
	assert.Equal(t, "sab-0", first.Engine)
	assert.Equal(t, commonv1.DownloadPhasePending, second.Phase, "the engine's last slot went to the first")
	assert.Empty(t, second.Engine)
	assert.Equal(t, "sab", second.Client, "the waiter names its client")
	require.NotNil(t, second.Dispatch.Delivery)
	assert.Equal(t, catalogv1alpha1.DeliveryReasonNoSlot, second.Dispatch.Delivery.Reason)
	assert.Contains(t, second.Message, "waiting for a free transfer slot")
	require.Len(t, p.Commands, 1, "only the assigned grab is commanded")

	// The same view without late binding assigns both at once, as a single
	// engine always has.
	v.Clients = []lifecycle.Client{usenetClient()}
	p = lifecycle.Decide(v)
	assert.Equal(t, commonv1.DownloadPhaseAssigned, entryByUID(t, p, "u2").Phase)
	require.Len(t, p.Commands, 2)
}

// A grab with others waiting ahead of it across owners does not take a
// slot out of turn. It sets no timer of its own -- a freed slot and the
// engines' records wake the head of the queue -- and its place is the time
// its release was chosen, kept on the entry.
func TestALateBindingGrabKeepsItsPlaceInTheQueue(t *testing.T) {
	v := view(nil)
	v.Clients = []lifecycle.Client{lateClient(1)}
	v.Engines = map[string]lifecycle.Engine{"sab-0": slotEngine(0, 8, 9)} // one slot free
	fresh := pendingUsenet("u1")
	fresh.GrabbedAt = metav1.Time{}
	v.NewEntries = []catalogv1alpha1.DownloadEntry{fresh}
	v.Ahead = map[types.UID]int{"u1": 40}
	p := lifecycle.Decide(v)
	waiting := entryByUID(t, p, "u1")
	assert.Equal(t, commonv1.DownloadPhasePending, waiting.Phase)
	assert.True(t, p.Due.IsZero(), "a waiter's passes do not grow with the queue: it is woken, it does not poll")
	assert.Equal(t, now, waiting.GrabbedAt.Time, "its place in the queue is when its release was chosen")

	// The next pass keeps that place.
	v.NewEntries, v.Stored = nil, []catalogv1alpha1.DownloadEntry{waiting}
	v.Now = now.Add(time.Hour)
	again := entryByUID(t, lifecycle.Decide(v), "u1")
	assert.Equal(t, now, again.GrabbedAt.Time)
	v.Now, v.Stored, v.NewEntries = now, nil, []catalogv1alpha1.DownloadEntry{fresh}

	v.Ahead = map[types.UID]int{"u1": 0}
	p = lifecycle.Decide(v)
	assert.Equal(t, commonv1.DownloadPhaseAssigned, entryByUID(t, p, "u1").Phase)
}
