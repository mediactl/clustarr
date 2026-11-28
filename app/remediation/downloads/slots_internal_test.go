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
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/grab/lifecycle"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/app/remediation/dlindex"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
)

func movieKey(name string) remediation.Key {
	return remediation.Key{Kind: remediation.KindMovie, Namespace: "media", Name: name}
}

// indexed collects dlindex's field index for a fake client: the real
// indexer function, as the manager's cache carries it.
type indexed struct{ b *fake.ClientBuilder }

func (i *indexed) IndexField(_ context.Context, obj client.Object, field string, fn client.IndexerFunc) error {
	i.b = i.b.WithIndex(obj, field, fn)
	return nil
}

// cacheOf is a reader over movies with the download index, as the stage's
// Reader is.
func cacheOf(t *testing.T, movies ...*catalogv1alpha1.Movie) client.Client {
	t.Helper()
	ix := &indexed{b: fake.NewClientBuilder().WithScheme(k8s.MustNewScheme())}
	require.NoError(t, dlindex.Register(t.Context(), ix))
	for _, m := range movies {
		ix.b = ix.b.WithObjects(m)
	}
	return ix.b.Build()
}

func slotEntry(uid, clientName, engine string, phase commonv1.DownloadPhase, grabbed time.Time) catalogv1alpha1.DownloadEntry {
	e := catalogv1alpha1.DownloadEntry{ID: "id-" + uid, UID: uid, Client: clientName, Engine: engine, Phase: phase}
	if !grabbed.IsZero() {
		e.GrabbedAt = metav1.NewTime(grabbed)
	}
	if phase == commonv1.DownloadPhasePending && clientName != "" {
		e.Dispatch.Delivery = &catalogv1alpha1.DeliveryState{State: catalogv1alpha1.DeliveryWaiting, Reason: catalogv1alpha1.DeliveryReasonNoSlot}
	}
	return e
}

func movie(name string, annotations map[string]string, entries ...catalogv1alpha1.DownloadEntry) *catalogv1alpha1.Movie {
	return &catalogv1alpha1.Movie{
		ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: name, Annotations: annotations},
		Status:     catalogv1alpha1.MovieStatus{Downloads: entries},
	}
}

// The queue for a transfer slot is read from status, across owners: the
// higher priority class first, then the earliest chosen. No book is kept,
// so a leader that has passed no owner yet reads the same queue.
func TestTheSlotQueueIsReadFromStatusAcrossOwners(t *testing.T) {
	t0 := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	pending := commonv1.DownloadPhasePending
	r := cacheOf(t,
		movie("a", nil, slotEntry("old", "sab", "", pending, t0)),
		movie("b", nil, slotEntry("newer", "sab", "", pending, t0.Add(time.Minute)),
			slotEntry("held", "sab", "sab-0", commonv1.DownloadPhaseDownloading, t0)),
		movie("c", map[string]string{catalogv1alpha1.AnnotationDownloadPriority: "id-high=high"},
			slotEntry("high", "sab", "", pending, t0.Add(time.Hour))),
		movie("d", nil, slotEntry("elsewhere", "other", "", pending, t0)),
	)
	q, err := queue(t.Context(), r, "media", "sab")
	require.NoError(t, err)
	var order []types.UID
	for _, w := range q {
		order = append(order, w.uid)
	}
	assert.Equal(t, []types.UID{"high", "old", "newer"}, order, "the higher class first, however late it was chosen; a holder is no waiter")
	assert.Equal(t, []remediation.Key{movieKey("c"), movieKey("a")}, head(q, 2))

	other, err := queue(t.Context(), r, "media", "other")
	require.NoError(t, err)
	assert.Equal(t, []remediation.Key{movieKey("d")}, head(other, 4), "each client has its own queue")

	// What lifecycle is told: how many wait ahead of each of an owner's
	// Pending entries, and each engine's holders, the cache's and the
	// assignments this leader made a moment ago.
	s := &Stage{o: Options{Reader: r}, slotBook: newSlotBook(), slotWake: &wakeSource{}}
	s.slotBook.assign("sab-0", "just-assigned", t0.Add(2*time.Hour-time.Second))
	late := lifecycle.Client{Name: "sab", Protocol: commonv1.ProtocolUsenet, Enabled: true, Replicas: 1, LateBinding: true, MaxActive: 2}
	fresh := slotEntry("fresh", "", "", pending, time.Time{})
	fresh.Release.Protocol = commonv1.ProtocolUsenet
	v := &lifecycle.View{
		Now: t0.Add(2 * time.Hour), Clients: []lifecycle.Client{late}, Intents: lifecycle.ParseIntents(nil),
		Engines:    map[string]lifecycle.Engine{"sab-0": {Name: "sab-0", Client: "sab"}},
		Stored:     []catalogv1alpha1.DownloadEntry{slotEntry("newer", "sab", "", pending, t0.Add(time.Minute))},
		NewEntries: []catalogv1alpha1.DownloadEntry{fresh},
	}
	ow := owner{ref: schema.ItemRef{Kind: "Movie", Ref: schema.Ref{Namespace: "media", Name: "b"}}}
	require.NoError(t, s.slots(t.Context(), ow, v))
	assert.Equal(t, map[types.UID]int{"newer": 2, "fresh": 3}, v.Ahead, "a grab just made stands behind every waiter")
	assert.Equal(t, int32(2), v.Engines["sab-0"].Pinned, "the cache's holder and the one assigned a moment ago")
	assert.Empty(t, s.slotBook.recent("sab-0", t0.Add(2*time.Hour+slotAssignedFor)), "an assignment counts only until the cache shows it")
}

// What a pass does to the slots: the grabs it assigned count towards
// their engines at once; a grab that stopped holding a slot wakes the next
// waiters in line; and any change to a client's waiters or holders wakes
// its DownloadClient. A pass that changes nothing wakes nobody.
func TestSlotsAfterWakesTheNextWaitersAndTheClient(t *testing.T) {
	now := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	pending := commonv1.DownloadPhasePending
	r := cacheOf(t,
		movie("other", nil, slotEntry("w0", "sab", "", pending, now.Add(-time.Hour))),
		movie("heat", nil, slotEntry("pend", "sab", "", pending, now.Add(-time.Minute))),
	)
	var woken []string
	s := &Stage{
		o:        Options{Reader: r, ClientWake: func(ns, name string) { woken = append(woken, ns+"/"+name) }},
		slotBook: newSlotBook(), slotWake: &wakeSource{},
	}
	q := &recordingQueue{}
	require.NoError(t, s.slotWake.Start(t.Context(), q))
	late := lifecycle.Client{Name: "sab", Protocol: commonv1.ProtocolUsenet, Enabled: true, Replicas: 1, LateBinding: true, MaxActive: 2}
	ow := owner{ref: schema.ItemRef{Kind: "Movie", Ref: schema.Ref{Namespace: "media", Name: "heat"}}}

	v := &lifecycle.View{Now: now, Clients: []lifecycle.Client{late}, Intents: lifecycle.ParseIntents(nil),
		Stored: []catalogv1alpha1.DownloadEntry{
			slotEntry("held", "sab", "sab-0", commonv1.DownloadPhaseDownloading, now),
			slotEntry("pend", "sab", "", pending, now.Add(-time.Minute)),
		}}
	done := slotEntry("held", "sab", "sab-0", commonv1.DownloadPhaseCompleted, now)
	plan := lifecycle.Plan{Entries: []catalogv1alpha1.DownloadEntry{
		done,
		slotEntry("pend", "sab", "", pending, now.Add(-time.Minute)),
		slotEntry("new", "sab", "sab-0", commonv1.DownloadPhaseAssigned, now),
	}}
	s.slotsAfter(t.Context(), ow, v, plan)

	assert.Equal(t, []types.UID{"new"}, s.slotBook.recent("sab-0", now), "an assignment counts before the cache shows it")
	assert.Equal(t, []remediation.Key{movieKey("other"), movieKey("heat")}, q.added,
		"the finished transfer's slot wakes the head of the queue, in order")
	assert.Equal(t, []string{"media/sab"}, woken, "the client's controller is told once")

	// The same entries again: nothing changed, nobody is woken.
	q.added, woken = nil, nil
	v.Stored = plan.Entries
	s.slotsAfter(t.Context(), ow, v, plan)
	assert.Empty(t, q.added)
	assert.Empty(t, woken)

	// A waiter that left its owner changes the queue: the controller is
	// told, and no waiter is woken for a slot nobody freed.
	v.Stored = []catalogv1alpha1.DownloadEntry{slotEntry("pend", "sab", "", pending, now)}
	s.slotsAfter(t.Context(), ow, v, lifecycle.Plan{})
	assert.Empty(t, q.added)
	assert.Equal(t, []string{"media/sab"}, woken)

	// The predicates the index and the controller share.
	assert.True(t, dlindex.WaitsForSlot(&plan.Entries[1]))
	assert.False(t, dlindex.WaitsForSlot(&plan.Entries[2]))
	assert.True(t, dlindex.HoldsSlot(&plan.Entries[2]))
	assert.False(t, dlindex.HoldsSlot(&done), "a finished transfer holds no slot")
	post := slotEntry("post", "sab", "sab-0", commonv1.DownloadPhaseDownloading, now)
	post.Stage = commonv1.DownloadStageExtracting
	assert.False(t, dlindex.HoldsSlot(&post), "nor does one unpacking")
	paused := slotEntry("paused", "sab", "sab-0", commonv1.DownloadPhasePaused, now)
	assert.False(t, dlindex.HoldsSlot(&paused), "nor a paused one")
}

// An engine's record wakes the waiters only when it shows room that was
// not there: the first this leader sees, an engine that became ready, a new
// budget, fewer transfers. The record it writes every minute, saying the
// same, wakes nobody; nor does one that shows less room.
func TestOnlyAnEngineRecordWithNewRoomWakes(t *testing.T) {
	b := newSlotBook()
	busy := engineReport{ready: true, budgetSeq: 7, load: 8}
	assert.True(t, b.roomChanged("sab-0", busy), "the first report a leader sees")
	assert.False(t, b.roomChanged("sab-0", busy), "the same again: a heartbeat")
	assert.False(t, b.roomChanged("sab-0", engineReport{ready: true, budgetSeq: 7, load: 9}), "more load is no room")
	assert.True(t, b.roomChanged("sab-0", engineReport{ready: true, budgetSeq: 7, load: 6}), "fewer transfers")
	assert.True(t, b.roomChanged("sab-0", engineReport{ready: true, budgetSeq: 8, load: 6}), "a new budget")
	assert.False(t, b.roomChanged("sab-0", engineReport{ready: false, budgetSeq: 8, load: 6}), "an engine that stopped being ready")
	assert.True(t, b.roomChanged("sab-0", engineReport{ready: true, budgetSeq: 8, load: 6}), "and is ready again")
	assert.True(t, b.roomChanged("sab-1", busy), "each engine has its own last report")
}

// recordingQueue keeps the keys added after a delay; nothing else of the
// queue is used by the wake source.
type recordingQueue struct {
	workqueue.TypedRateLimitingInterface[remediation.Key]
	added []remediation.Key
}

func (q *recordingQueue) AddAfter(k remediation.Key, _ time.Duration) { q.added = append(q.added, k) }
