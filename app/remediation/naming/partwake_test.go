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

package naming

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/priorityqueue"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/remediation"
)

// wakeQueue records what a handler enqueues and at which priority.
type wakeQueue struct {
	priorityqueue.PriorityQueue[remediation.Key]
	added map[remediation.Key]int
}

func newWakeQueue() *wakeQueue { return &wakeQueue{added: map[remediation.Key]int{}} }

func (q *wakeQueue) Add(k remediation.Key) { q.added[k] = 0 }

func (q *wakeQueue) AddWithOpts(o priorityqueue.AddOpts, ks ...remediation.Key) {
	for _, k := range ks {
		p := 0
		if o.Priority != nil {
			p = *o.Priority
		}
		q.added[k] = p
	}
}

// TestAnAudiobookPartWakesItsSiblingsWhenTheOrderMoves is S12/MediaFile
// (2026-11-10 design D7, D11; the D6 review's wake finding): once a part
// is planned, a sibling appearing or going, a part re-keyed or moved to
// another audiobook, or an unnumbered part's path moving renumbers the
// others, so each other part of the audiobook (and of the one left) is
// enqueued at low priority, through the item index -- never the changed
// file itself, nothing on the initial list, nothing for a status-only
// update, a numbered part's path, or a movie's file.
func TestAnAudiobookPartWakesItsSiblingsWhenTheOrderMoves(t *testing.T) {
	c := nonVideoFixture(t) // guards-audio-01 and -02 are the Audiobook's planned parts
	h := audiobookPartWakes(c)
	ctx := t.Context()
	key := func(name string) remediation.Key {
		return remediation.Key{Kind: remediation.KindMediaFile, Namespace: "media", Name: name}
	}
	part := func(name, audiobook, path string, index int32) *catalogv1alpha1.MediaFile {
		return &catalogv1alpha1.MediaFile{ObjectMeta: nvMeta(name), Spec: catalogv1alpha1.MediaFileSpec{
			MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindAudiobook, Name: audiobook}, Path: path, PartIndex: index,
		}}
	}
	siblings := map[remediation.Key]int{key("guards-audio-01"): handler.LowPriority, key("guards-audio-02"): handler.LowPriority}
	third := part("guards-audio-03", "guards-audio", "/data/guards-audio-03", 1003)

	q := newWakeQueue()
	h.Create(ctx, event.TypedCreateEvent[client.Object]{Object: third}, q)
	assert.Equal(t, siblings, q.added, "a third part appears: the two planned parts re-plan, not the new one (S1's)")

	q = newWakeQueue()
	h.Create(ctx, event.TypedCreateEvent[client.Object]{Object: third, IsInInitialList: true}, q)
	assert.Empty(t, q.added, "the initial list: every file wakes itself")

	q = newWakeQueue()
	h.Delete(ctx, event.TypedDeleteEvent[client.Object]{Object: part("guards-audio-02", "guards-audio", "/data/guards-audio-02", 0)}, q)
	assert.Equal(t, map[remediation.Key]int{key("guards-audio-01"): handler.LowPriority}, q.added, "a part goes: the rest renumber")

	q = newWakeQueue()
	same := part("guards-audio-01", "guards-audio", "/data/guards-audio-01", 1001)
	h.Update(ctx, event.TypedUpdateEvent[client.Object]{ObjectOld: same, ObjectNew: same.DeepCopy()}, q)
	assert.Empty(t, q.added, "a status-only update moves no order")

	q = newWakeQueue()
	rekeyed := same.DeepCopy()
	rekeyed.Spec.PartIndex = 2001
	h.Update(ctx, event.TypedUpdateEvent[client.Object]{ObjectOld: same, ObjectNew: rekeyed}, q)
	assert.Equal(t, map[remediation.Key]int{key("guards-audio-02"): handler.LowPriority}, q.added, "a part re-keyed: its siblings, not itself")

	q = newWakeQueue()
	renamed := same.DeepCopy()
	renamed.Spec.Path = "/data/guards-audio-01 (01).mp3"
	h.Update(ctx, event.TypedUpdateEvent[client.Object]{ObjectOld: same, ObjectNew: renamed}, q)
	assert.Empty(t, q.added, "a numbered part's path breaks only a tie")

	q = newWakeQueue()
	loose := part("guards-audio-01", "guards-audio", "/data/guards-audio-01", 0)
	looseRenamed := loose.DeepCopy()
	looseRenamed.Spec.Path = "/data/zz.mp3"
	h.Update(ctx, event.TypedUpdateEvent[client.Object]{ObjectOld: loose, ObjectNew: looseRenamed}, q)
	assert.Equal(t, map[remediation.Key]int{key("guards-audio-02"): handler.LowPriority}, q.added, "an unnumbered part orders by its path")

	q = newWakeQueue()
	moved := part("guards-audio-03", "other-audio", "/data/guards-audio-03", 1003)
	h.Update(ctx, event.TypedUpdateEvent[client.Object]{ObjectOld: third, ObjectNew: moved}, q)
	assert.Equal(t, siblings, q.added, "a part moved to another audiobook: the parts it left (the other has none listed)")

	q = newWakeQueue()
	movie := &catalogv1alpha1.MediaFile{ObjectMeta: nvMeta("heat-mkv"), Spec: catalogv1alpha1.MediaFileSpec{MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "heat"}}}
	h.Create(ctx, event.TypedCreateEvent[client.Object]{Object: movie}, q)
	h.Delete(ctx, event.TypedDeleteEvent[client.Object]{Object: movie}, q)
	assert.Empty(t, q.added, "a movie's file is no part")
}
