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
	"fmt"
	"time"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// Decision is Admit's answer.
type Decision struct {
	Admitted bool
	// Reason is a catalogv1alpha1.DeliveryReason* value when !Admitted.
	Reason Reason
	// Message is what the CR's delivery.message shows, e.g. "waiting for
	// catalogarr-search-normal: 16 in flight".
	Message string
	// RetryAfter is when the planner should ask again.
	RetryAfter time.Duration
}

// Admit asks whether k may publish its dispatch at seq to durable, and
// reserves the slot when it may (§5.4):
//
//   - Rebuilding until Start has rebuilt the ledger from status;
//   - admitted at once when k already holds a slot at seq or later (a pass
//     that asks again);
//   - admitted while the durable's outstanding dispatches, k's older one
//     aside (a new seq supersedes it), are below Budget;
//   - else NoAgent when the durable is unattended (§5.5), Budget otherwise.
//
// NoCapableAgent and EngineNotReady are the caller's own refusals, reported
// through Waiting. A reservation lapses after ReservationTTL unless
// Published confirms it.
func (l *Ledger) Admit(durable string, k Key, seq int64) Decision {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if !l.rebuilt {
		return Decision{
			Reason:     catalogv1alpha1.DeliveryReasonRebuilding,
			Message:    "waiting for the dispatch ledger to rebuild from status after a leader change",
			RetryAfter: TickInterval,
		}
	}
	c, ok := l.spec(durable)
	if !ok {
		return Decision{
			Reason:     catalogv1alpha1.DeliveryReasonBudget,
			Message:    fmt.Sprintf("%s is not a durable of the topology", durable),
			RetryAfter: TickInterval,
		}
	}
	book := l.bookLocked(durable)
	l.lapseLocked(book, now)
	if cur, ok := book[k]; ok && cur.seq >= seq {
		l.clearWaitingLocked(durable, k)
		return Decision{Admitted: true}
	}
	prev, superseding := book[k]
	n := len(book)
	if superseding {
		n--
	}
	if n >= budgetOf(c) {
		if l.unattendedLocked(durable) {
			return Decision{
				Reason:     catalogv1alpha1.DeliveryReasonNoAgent,
				Message:    fmt.Sprintf("no agent is taking %s: %d in flight", durable, n),
				RetryAfter: TickInterval,
			}
		}
		return Decision{
			Reason:     catalogv1alpha1.DeliveryReasonBudget,
			Message:    fmt.Sprintf("waiting for %s: %d in flight", durable, n),
			RetryAfter: TickInterval,
		}
	}
	book[k] = &slot{seq: seq, state: reserved, expires: now.Add(ReservationTTL), prev: prev}
	l.clearWaitingLocked(durable, k)
	return Decision{Admitted: true}
}
