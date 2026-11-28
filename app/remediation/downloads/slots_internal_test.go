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

package downloads

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/grab/lifecycle"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/app/remediation/dlindex"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

func movieKey(name string) remediation.Key {
	return remediation.Key{Kind: remediation.KindMovie, Namespace: "media", Name: name}
}

func waiter(uid, movie string, rank int, since time.Time) slotWaiter {
	return slotWaiter{uid: types.UID(uid), owner: movieKey(movie), namespace: "media", client: "sab", rank: rank, since: since}
}

// The queue for a transfer slot spans owners: the higher priority class
// first, then the longest waiting. A pass never moves a waiter back, and a
// waiter its owner no longer has, or that nobody has named for too long,
// leaves the queue.
func TestTheSlotBookOrdersWaitersAcrossOwners(t *testing.T) {
	t0 := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	b := newSlotBook()
	old := waiter("old", "a", 1, t0)
	newer := waiter("newer", "b", 1, t0.Add(time.Minute))
	high := waiter("high", "c", 2, t0.Add(time.Hour))
	other := slotWaiter{uid: "elsewhere", owner: movieKey("d"), namespace: "media", client: "other", rank: 2, since: t0}
	b.sync(movieKey("a"), []slotWaiter{old}, t0)
	b.sync(movieKey("b"), []slotWaiter{newer}, t0)
	b.sync(movieKey("c"), []slotWaiter{high}, t0)
	b.sync(movieKey("d"), []slotWaiter{other}, t0)

	assert.Equal(t, 0, b.ahead(high), "the higher class is first, however late it came")
	assert.Equal(t, 1, b.ahead(old))
	assert.Equal(t, 2, b.ahead(newer))
	assert.Equal(t, 3, b.ahead(waiter("unfiled", "e", 1, t0.Add(2*time.Minute))), "a grab not yet filed stands behind those before it")
	assert.Equal(t, []remediation.Key{movieKey("c"), movieKey("a")}, b.head("media", "sab", 2))
	assert.Equal(t, []remediation.Key{movieKey("d")}, b.head("media", "other", 4), "each client has its own queue")

	// A later pass that names a later time does not move the waiter back.
	moved := old
	moved.since = t0.Add(3 * time.Hour)
	b.sync(movieKey("a"), []slotWaiter{moved}, t0.Add(time.Minute))
	assert.Equal(t, 1, b.ahead(old))

	// Assigned: out of the queue, and counted towards its engine for a
	// while, until the cache shows it.
	b.assign("sab-0", "high", t0)
	assert.Equal(t, 0, b.ahead(old))
	assert.Equal(t, []types.UID{"high"}, b.recent("sab-0", t0.Add(30*time.Second)))
	assert.Empty(t, b.recent("sab-0", t0.Add(slotAssignedFor+time.Second)))

	// The owner's pass no longer has it; another's waiter went unnamed.
	b.sync(movieKey("a"), nil, t0.Add(2*time.Minute))
	assert.Equal(t, 0, b.ahead(newer))
	b.sync(movieKey("z"), nil, t0.Add(slotWaiterFor+time.Hour))
	assert.Empty(t, b.head("media", "sab", 4), "a waiter nobody names any more is dropped")
}

func slotEntry(uid, client, engine string, phase commonv1.DownloadPhase) catalogv1alpha1.DownloadEntry {
	e := catalogv1alpha1.DownloadEntry{ID: "id-" + uid, UID: uid, Client: client, Engine: engine, Phase: phase}
	if phase == commonv1.DownloadPhasePending && client != "" {
		e.Dispatch.Delivery = &catalogv1alpha1.DeliveryState{State: catalogv1alpha1.DeliveryWaiting, Reason: catalogv1alpha1.DeliveryReasonNoSlot}
	}
	return e
}

// What a pass does to the slots: its waiters are filed, the grabs it
// assigned count towards their engines at once, and a grab that stopped
// holding a slot wakes the head of the queue.
func TestSlotsAfterFilesWaitersAssignmentsAndWakes(t *testing.T) {
	now := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	s := &Stage{slotBook: newSlotBook(), slotWake: &wakeSource{}}
	late := lifecycle.Client{Name: "sab", Protocol: commonv1.ProtocolUsenet, Enabled: true, Replicas: 1, LateBinding: true, MaxActive: 2}
	ow := owner{ref: schema.ItemRef{Kind: "Movie", Ref: schema.Ref{Namespace: "media", Name: "heat"}}}

	// Another owner's grab is already waiting.
	s.slotBook.sync(movieKey("other"), []slotWaiter{waiter("w0", "other", 1, now.Add(-time.Hour))}, now)

	v := &lifecycle.View{Now: now, Clients: []lifecycle.Client{late}, Intents: lifecycle.ParseIntents(nil),
		Stored: []catalogv1alpha1.DownloadEntry{
			slotEntry("held", "sab", "sab-0", commonv1.DownloadPhaseDownloading),
			slotEntry("pend", "", "", commonv1.DownloadPhasePending),
		}}
	done := slotEntry("held", "sab", "sab-0", commonv1.DownloadPhaseCompleted)
	plan := lifecycle.Plan{Entries: []catalogv1alpha1.DownloadEntry{
		done,
		slotEntry("pend", "sab", "", commonv1.DownloadPhasePending),
		slotEntry("new", "sab", "sab-0", commonv1.DownloadPhaseAssigned),
	}}
	q := &recordingQueue{}
	require.NoError(t, s.slotWake.Start(t.Context(), q))
	s.slotsAfter(ow, v, plan)

	assert.Equal(t, 1, s.slotBook.ahead(waiterOf(ow, v, &plan.Entries[1], "sab")), "filed behind the grab that waited longer")
	assert.Equal(t, []types.UID{"new"}, s.slotBook.recent("sab-0", now), "an assignment counts before the cache shows it")
	assert.Equal(t, []remediation.Key{movieKey("other"), movieKey("heat")}, q.added,
		"the finished transfer's slot wakes the head of the queue")

	// A pass that frees nothing wakes nobody.
	q.added = nil
	v.Stored = plan.Entries
	s.slotsAfter(ow, v, plan)
	assert.Empty(t, q.added)

	// The predicates the index and the controller share.
	assert.True(t, dlindex.WaitsForSlot(&plan.Entries[1]))
	assert.False(t, dlindex.WaitsForSlot(&plan.Entries[2]))
	assert.True(t, dlindex.HoldsSlot(&plan.Entries[2]))
	assert.False(t, dlindex.HoldsSlot(&done), "a finished transfer holds no slot")
	post := slotEntry("post", "sab", "sab-0", commonv1.DownloadPhaseDownloading)
	post.Stage = commonv1.DownloadStageExtracting
	assert.False(t, dlindex.HoldsSlot(&post), "nor does one unpacking")
	paused := slotEntry("paused", "sab", "sab-0", commonv1.DownloadPhasePaused)
	assert.False(t, dlindex.HoldsSlot(&paused), "nor a paused one")
}

// recordingQueue keeps the keys added after a delay; nothing else of the
// queue is used by the wake source.
type recordingQueue struct {
	workqueue.TypedRateLimitingInterface[remediation.Key]
	added []remediation.Key
}

func (q *recordingQueue) AddAfter(k remediation.Key, _ time.Duration) { q.added = append(q.added, k) }
