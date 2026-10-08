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

package dispatch_test

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/dispatch"
)

// TestRenderFollowsTheDeliveryTable holds Render to every row of §8.3's table
// a planner renders: due and refused (waiting, with the refusal's reason),
// published, claimed, naked (retrying on the first nak and the last before
// the dead letter), answered (no delivery), terminated or dead-lettered --
// and the book's state is read at exactly the block's seq, so an advisory for
// an older dispatch never colours a newer one.
func TestRenderFollowsTheDeliveryTable(t *testing.T) {
	k := movieKey("heat")
	nakAt := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	termAt := nakAt.Add(time.Minute)
	inFlight := func(seq int64) *catalogv1alpha1.Dispatch {
		return &catalogv1alpha1.Dispatch{Seq: seq, AnsweredSeq: seq - 1, DispatchedAt: metav1.NewTime(nakAt)}
	}
	long := strings.Repeat("é", 200) // 400 bytes of two-byte runes

	for _, tc := range []struct {
		name  string
		d     *catalogv1alpha1.Dispatch
		dec   *dispatch.Decision
		book  func(*dispatch.DeliveryBook)
		check func(t *testing.T, d *catalogv1alpha1.Dispatch)
	}{
		{
			name: "due, not admitted: waiting with the refusal's reason and message",
			d:    &catalogv1alpha1.Dispatch{Seq: 4, AnsweredSeq: 4},
			dec: &dispatch.Decision{
				Reason: catalogv1alpha1.DeliveryReasonBudget, Message: "waiting for catalog-search-normal: 16 in flight",
			},
			check: func(t *testing.T, d *catalogv1alpha1.Dispatch) {
				assert.Equal(t, &catalogv1alpha1.DeliveryState{
					State: catalogv1alpha1.DeliveryWaiting, Reason: catalogv1alpha1.DeliveryReasonBudget,
					Message: "waiting for catalog-search-normal: 16 in flight",
				}, d.Delivery)
			},
		},
		{
			name: "a refusal's reason and message are clamped on rune boundaries",
			d:    inFlight(5),
			dec:  &dispatch.Decision{Reason: long, Message: long},
			check: func(t *testing.T, d *catalogv1alpha1.Dispatch) {
				assert.Equal(t, catalogv1alpha1.DeliveryWaiting, d.Delivery.State)
				assert.Equal(t, strings.Repeat("é", 32), d.Delivery.Reason)
				assert.Equal(t, strings.Repeat("é", 128), d.Delivery.Message)
			},
		},
		{
			name: "published: in flight and the book knows nothing of it",
			d:    inFlight(5),
			dec:  &dispatch.Decision{Admitted: true},
			check: func(t *testing.T, d *catalogv1alpha1.Dispatch) {
				assert.Equal(t, &catalogv1alpha1.DeliveryState{State: catalogv1alpha1.DeliveryPublished}, d.Delivery)
			},
		},
		{
			name: "claimed: the planner's own rendering from its record stands",
			d: func() *catalogv1alpha1.Dispatch {
				d := inFlight(5)
				d.Delivery = &catalogv1alpha1.DeliveryState{State: catalogv1alpha1.DeliveryClaimed}
				return d
			}(),
			check: func(t *testing.T, d *catalogv1alpha1.Dispatch) {
				assert.Equal(t, &catalogv1alpha1.DeliveryState{State: catalogv1alpha1.DeliveryClaimed}, d.Delivery)
			},
		},
		{
			name: "naked: retrying with the attempts and the nak's time",
			d:    inFlight(5),
			book: func(b *dispatch.DeliveryBook) { b.Retrying(k, 5, 1, nakAt, false) },
			check: func(t *testing.T, d *catalogv1alpha1.Dispatch) {
				nak := metav1.NewTime(nakAt)
				assert.Equal(t, &catalogv1alpha1.DeliveryState{
					State: catalogv1alpha1.DeliveryRetrying, Reason: catalogv1alpha1.DeliveryReasonNak,
					Attempts: 1, LastNakAt: &nak,
				}, d.Delivery)
			},
		},
		{
			name: "naked for the last time before the dead letter: retrying says so",
			d:    inFlight(5),
			book: func(b *dispatch.DeliveryBook) {
				b.Retrying(k, 5, 1, nakAt, false)
				b.Retrying(k, 5, 4, nakAt.Add(time.Minute), true)
			},
			check: func(t *testing.T, d *catalogv1alpha1.Dispatch) {
				assert.Equal(t, catalogv1alpha1.DeliveryRetrying, d.Delivery.State)
				assert.Equal(t, int32(4), d.Delivery.Attempts)
				assert.True(t, d.Delivery.LastNakAt.Time.Equal(nakAt.Add(time.Minute)))
				assert.Equal(t, "the next delivery is the last before the task is dead-lettered", d.Delivery.Message)
			},
		},
		{
			name: "terminated or dead-lettered: deadLettered, keeping the naks' count",
			d:    inFlight(5),
			book: func(b *dispatch.DeliveryBook) {
				b.Retrying(k, 5, 2, nakAt, false)
				b.DeadLettered(k, 5, termAt)
				b.Retrying(k, 5, 3, termAt.Add(time.Second), false) // a late nak cannot revive it
			},
			check: func(t *testing.T, d *catalogv1alpha1.Dispatch) {
				nak, term := metav1.NewTime(nakAt), metav1.NewTime(termAt)
				assert.Equal(t, &catalogv1alpha1.DeliveryState{
					State: catalogv1alpha1.DeliveryDeadLettered, Attempts: 2, LastNakAt: &nak, DeadLetteredAt: &term,
				}, d.Delivery)
			},
		},
		{
			name: "an advisory for an older dispatch does not colour the newer one",
			d:    inFlight(6),
			book: func(b *dispatch.DeliveryBook) { b.DeadLettered(k, 5, termAt) },
			check: func(t *testing.T, d *catalogv1alpha1.Dispatch) {
				assert.Equal(t, &catalogv1alpha1.DeliveryState{State: catalogv1alpha1.DeliveryPublished}, d.Delivery)
			},
		},
		{
			name: "answered: the delivery is cleared",
			d: &catalogv1alpha1.Dispatch{
				Seq: 5, AnsweredSeq: 5,
				Delivery: &catalogv1alpha1.DeliveryState{State: catalogv1alpha1.DeliveryRetrying, Attempts: 2},
			},
			book: func(b *dispatch.DeliveryBook) { b.Retrying(k, 5, 2, nakAt, false) },
			check: func(t *testing.T, d *catalogv1alpha1.Dispatch) {
				assert.Nil(t, d.Delivery)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			book := dispatch.NewDeliveryBook()
			if tc.book != nil {
				tc.book(book)
			}
			dispatch.Render(tc.d, searchNormal, tc.dec, book, k)
			assert.Equal(t, searchNormal, tc.d.Destination)
			tc.check(t, tc.d)
		})
	}

	t.Run("the destination is clamped to its MaxLength and a nil block is left alone", func(t *testing.T) {
		d := inFlight(1)
		dispatch.Render(d, strings.Repeat("d", 300), nil, nil, k)
		assert.Len(t, d.Destination, 253)
		assert.True(t, utf8.ValidString(d.Destination))
		assert.Equal(t, catalogv1alpha1.DeliveryPublished, d.Delivery.State, "no book reads as published")
		assert.NotPanics(t, func() { dispatch.Render(nil, searchNormal, nil, nil, k) })
	})

	t.Run("the book's stale and forgotten entries", func(t *testing.T) {
		book := dispatch.NewDeliveryBook()
		book.Retrying(k, 6, 1, nakAt, false)
		book.Retrying(k, 5, 3, nakAt, false) // stale: the book holds seq 6
		assert.True(t, book.Newer(k, 5))
		assert.True(t, book.Seen(k, 6))
		_, ok := book.Lookup(k, 5)
		assert.False(t, ok)
		st, ok := book.Lookup(k, 6)
		assert.True(t, ok)
		assert.Equal(t, int32(1), st.Attempts)
		book.Forget(k, 5)
		assert.True(t, book.Seen(k, 6), "Forget below the entry's seq keeps it")
		book.Forget(k, 6)
		assert.False(t, book.Seen(k, 6))
	})
}
