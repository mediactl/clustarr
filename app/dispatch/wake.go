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
	"hash/fnv"
	"slices"
	"time"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// Waiting on a rebuildable durable's budget (the search lanes). On talos
// (2026-11-02) ~2,263 Budget-refused searches asked again every
// TickInterval, ~226 passes a second, which kept the item loop's 16 workers
// busy. A key refused for budget there is woken when a slot frees (Wakes),
// so it asks again rarely on its own.
const (
	// BudgetRetry is how soon a routine key refused for a rebuildable
	// durable's budget asks again unwoken, spread by its key over
	// 0.75-1.25 times this so the waiters do not ask in step.
	BudgetRetry = 2 * time.Minute
	// FirstRetry is how soon a key refused by AdmitFirst asks again
	// unwoken; it must ask within FirstTTL to keep its claim.
	FirstRetry = time.Minute
	// FirstTTL is how long a refused AdmitFirst holds its claim on the next
	// slot without asking again.
	FirstTTL = 2 * time.Minute
	// wakeBuffer bounds the wakes not yet taken; a wake that does not fit
	// is dropped, and its key asks again after its retry.
	wakeBuffer = 512
)

// firstWait is one key waiting through AdmitFirst.
type firstWait struct {
	since, seen time.Time
}

// Wakes are the keys to plan again because a slot of the durable they wait
// on freed: the waiters AdmitFirst refused, oldest first, then the keys
// refused for budget (Waiting), the longest unrefused first -- twice as many
// as slots freed, since a woken key may no longer be due. Only rebuildable
// durables wake; the item loop takes the channel (one reader).
func (l *Ledger) Wakes() <-chan Key { return l.wakes }

// firstWaitingLocked counts durable's live AdmitFirst waiters other than k,
// forgetting those past FirstTTL.
func (l *Ledger) firstWaitingLocked(durable string, k Key, now time.Time) int {
	n := 0
	for w, fw := range l.first[durable] {
		if now.Sub(fw.seen) >= FirstTTL {
			delete(l.first[durable], w)
			continue
		}
		if w != k {
			n++
		}
	}
	return n
}

// markFirstLocked records k waiting through AdmitFirst: its place from its
// first refusal, its claim renewed by each.
func (l *Ledger) markFirstLocked(durable string, k Key, now time.Time) {
	m := l.first[durable]
	if m == nil {
		m = map[Key]firstWait{}
		l.first[durable] = m
	}
	fw, ok := m[k]
	if !ok {
		fw.since = now
	}
	fw.seen = now
	m[k] = fw
}

// budgetRetryLocked is a Budget refusal's RetryAfter: TickInterval on a
// durable that does not wake, else FirstRetry for AdmitFirst and BudgetRetry
// spread by k.
func (l *Ledger) budgetRetryLocked(durable string, k Key, first bool) time.Duration {
	if _, rebuildable := l.sources[durable]; !rebuildable {
		return TickInterval
	}
	if first {
		return FirstRetry
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(k.String()))
	spread := time.Duration(h.Sum32()%1000) * BudgetRetry / 2000 // 0 to BudgetRetry/2
	return BudgetRetry*3/4 + spread
}

// freedLocked wakes waiters of durable, which freed n slots (Wakes).
func (l *Ledger) freedLocked(durable string, n int) {
	if n <= 0 || l.wakes == nil {
		return
	}
	if _, rebuildable := l.sources[durable]; !rebuildable {
		return
	}
	want := 2 * n
	type waiter struct {
		k  Key
		at time.Time
	}
	var firsts, routine []waiter
	for k, fw := range l.first[durable] {
		firsts = append(firsts, waiter{k: k, at: fw.since})
	}
	for k, w := range l.waiting[durable] {
		if _, ok := l.first[durable][k]; !ok && w.reason == catalogv1alpha1.DeliveryReasonBudget {
			routine = append(routine, waiter{k: k, at: w.at})
		}
	}
	byAt := func(a, b waiter) int { return a.at.Compare(b.at) }
	slices.SortFunc(firsts, byAt)
	slices.SortFunc(routine, byAt)
	for _, w := range append(firsts, routine...) {
		if want == 0 {
			return
		}
		select {
		case l.wakes <- w.k:
			want--
		default:
			return
		}
	}
}
