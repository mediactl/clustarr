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

package dispatch

import (
	"sync"
	"time"
	"unicode/utf8"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// Field caps of a Dispatch block (§8.3; the CRD's MaxLength).
const (
	maxDestination = 253
	maxReason      = 64
	maxMessage     = 256
)

// deliveryEntry is the book's state for one key: the newest dispatch seq an
// advisory named, and what it said.
type deliveryEntry struct {
	seq   int64
	state catalogv1alpha1.DeliveryState
}

// DeliveryBook is the leader-local record of nak and term advisories
// (ruling R8): the advisory intake (app/intake/advisory) writes it and wakes
// the owning key; planners render Dispatch.delivery from it. It is history,
// not state: lost with the leader, and a missed advisory leaves delivery
// stale only until the next transition (§8.4).
type DeliveryBook struct {
	mu sync.Mutex
	m  map[Key]deliveryEntry
}

// NewDeliveryBook is an empty book.
func NewDeliveryBook() *DeliveryBook { return &DeliveryBook{m: map[Key]deliveryEntry{}} }

// Newer reports whether the book holds a dispatch of k later than seq: an
// advisory for seq is then stale.
func (b *DeliveryBook) Newer(k Key, seq int64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	e, ok := b.m[k]
	return ok && e.seq > seq
}

// Retrying records a nak of k's dispatch at seq (§8.3): the intake calls it
// for the first nak it sees of a dispatch and for the nak at MaxDeliver - 1
// (last), never for the others. deliveries is the advisory's delivery
// count. A dead-lettered dispatch stays dead-lettered.
func (b *DeliveryBook) Retrying(k Key, seq int64, deliveries int32, at time.Time, last bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	e, ok := b.m[k]
	if ok && e.seq > seq {
		return
	}
	if ok && e.seq == seq && e.state.State == catalogv1alpha1.DeliveryDeadLettered {
		return
	}
	t := metav1.NewTime(at)
	st := catalogv1alpha1.DeliveryState{
		State:     catalogv1alpha1.DeliveryRetrying,
		Reason:    catalogv1alpha1.DeliveryReasonNak,
		Attempts:  deliveries,
		LastNakAt: &t,
	}
	if last {
		st.Message = "the next delivery is the last before the task is dead-lettered"
	}
	b.m[k] = deliveryEntry{seq: seq, state: st}
}

// Seen reports whether the book already recorded a nak or a term of k at
// seq: the intake's "first nak" test.
func (b *DeliveryBook) Seen(k Key, seq int64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	e, ok := b.m[k]
	return ok && e.seq == seq
}

// DeadLettered records a term (or a dead-letter copy) of k's dispatch at seq.
func (b *DeliveryBook) DeadLettered(k Key, seq int64, at time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if e, ok := b.m[k]; ok && e.seq > seq {
		return
	}
	t := metav1.NewTime(at)
	prev := b.m[k]
	st := catalogv1alpha1.DeliveryState{State: catalogv1alpha1.DeliveryDeadLettered, DeadLetteredAt: &t}
	if prev.seq == seq {
		st.Attempts, st.LastNakAt = prev.state.Attempts, prev.state.LastNakAt
	}
	b.m[k] = deliveryEntry{seq: seq, state: st}
}

// Lookup is the book's delivery state of k's dispatch at exactly seq.
func (b *DeliveryBook) Lookup(k Key, seq int64) (catalogv1alpha1.DeliveryState, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	e, ok := b.m[k]
	if !ok || e.seq != seq {
		return catalogv1alpha1.DeliveryState{}, false
	}
	return *e.state.DeepCopy(), true
}

// Forget drops k's entry at or below seq: the dispatch was answered, or the
// CR is gone (seq math.MaxInt64).
func (b *DeliveryBook) Forget(k Key, seq int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if e, ok := b.m[k]; ok && e.seq <= seq {
		delete(b.m, k)
	}
}

// Render sets d's destination and delivery after §8.3's table, from the
// planner's admission decision for this pass (nil when it asked none) and
// the book:
//
//   - a refused admission: waiting, with the decision's reason and message;
//   - not in flight (seq == answeredSeq), no refusal: no delivery (answered);
//   - in flight: the book's retrying or deadLettered at seq, else a claimed
//     state the planner already rendered from its record, else published.
//
// Reason is clamped to 64 bytes and Message to 256, on rune boundaries;
// destination to 253.
func Render(d *catalogv1alpha1.Dispatch, destination string, dec *Decision, book *DeliveryBook, k Key) {
	if d == nil {
		return
	}
	if destination != "" {
		d.Destination = clamp(destination, maxDestination)
	}
	switch {
	case dec != nil && !dec.Admitted:
		d.Delivery = &catalogv1alpha1.DeliveryState{
			State:   catalogv1alpha1.DeliveryWaiting,
			Reason:  clamp(dec.Reason, maxReason),
			Message: clamp(dec.Message, maxMessage),
		}
	case !d.InFlight():
		d.Delivery = nil
	default:
		if book != nil {
			if st, ok := book.Lookup(k, d.Seq); ok {
				st.Reason = clamp(st.Reason, maxReason)
				st.Message = clamp(st.Message, maxMessage)
				d.Delivery = &st
				return
			}
		}
		if d.Delivery != nil && d.Delivery.State == catalogv1alpha1.DeliveryClaimed {
			return
		}
		d.Delivery = &catalogv1alpha1.DeliveryState{State: catalogv1alpha1.DeliveryPublished}
	}
}

// clamp cuts s to at most n bytes on a rune boundary.
func clamp(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
