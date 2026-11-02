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
	// catalog-search-normal: 16 in flight".
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
	return l.admit(durable, k, seq, false)
}

// AdmitFirst is Admit for a dispatch that goes ahead of the durable's
// routine ones -- a redownload (R25), whose 24-hour window a queue of
// routine searches must not run out: while it waits for a slot, a routine
// Admit is refused one slot sooner per such waiter, so the next slot that
// frees is its, and it is woken when one does (Wakes). On talos
// (2026-11-02) about 307 redownloads competed equally with ~2,256 routine
// searches, so about one admission in eight went to one of them.
func (l *Ledger) AdmitFirst(durable string, k Key, seq int64) Decision {
	return l.admit(durable, k, seq, true)
}

func (l *Ledger) admit(durable string, k Key, seq int64, first bool) Decision {
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
	l.lapseLocked(durable, book, now)
	if cur, ok := book[k]; ok && cur.seq >= seq {
		l.clearWaitingLocked(durable, k)
		return Decision{Admitted: true}
	}
	prev, superseding := book[k]
	n := len(book)
	if superseding {
		n--
	}
	limit := budgetOf(c)
	if !first {
		limit -= l.firstWaitingLocked(durable, k, now)
	}
	if n >= limit {
		if first {
			l.markFirstLocked(durable, k, now)
		}
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
			RetryAfter: l.budgetRetryLocked(durable, k, first),
		}
	}
	book[k] = &slot{seq: seq, state: reserved, expires: now.Add(ReservationTTL), prev: prev, at: now}
	l.clearWaitingLocked(durable, k)
	delete(l.first[durable], k)
	return Decision{Admitted: true}
}
