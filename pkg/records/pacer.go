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

package records

import (
	"sync"
	"time"
)

// Pacer classes (§4.7). The probe high lane, transcode and graft are bounded
// by the import rate and the admission ledger, and are not paced.
const (
	PaceProbeLow = "probe/low"
	PaceSubtitle = "subtitle"
	PaceMarkers  = "markers"
	// ReservationLapse frees a reservation not consumed this long after its slot.
	ReservationLapse = 10 * time.Minute
)

// DefaultRates are §4.7's admission rates, per minute.
func DefaultRates() map[string]int {
	return map[string]int{PaceProbeLow: 600, PaceSubtitle: 120, PaceMarkers: 120}
}

// Pacer is the leader-local admission queue over new requests (not
// republishes), one per class: a key reserves one slot and keeps it until
// Consume or until it lapses, so a requeued pass never reserves again (§4.7;
// TestThePacerReservesOneSlotPerKey).
type Pacer struct {
	mu    sync.Mutex
	now   func() time.Time
	every map[string]time.Duration
	next  map[string]time.Time
	held  map[string]map[string]time.Time
	swept map[string]time.Time
}

// NewPacer paces each class at perMinute; a class absent or at zero is
// unpaced. A nil now is time.Now.
func NewPacer(perMinute map[string]int, now func() time.Time) *Pacer {
	if now == nil {
		now = time.Now
	}
	p := &Pacer{
		now: now, every: map[string]time.Duration{}, next: map[string]time.Time{},
		held: map[string]map[string]time.Time{}, swept: map[string]time.Time{},
	}
	for class, n := range perMinute {
		if n > 0 {
			p.every[class] = time.Minute / time.Duration(n)
			p.held[class] = map[string]time.Time{}
		}
	}
	return p
}

// Reserve returns key's slot in class: the slot it holds, or else the next
// free one, which it then holds. A planner whose slot is in the future returns
// Due = the slot plus up to 5 s of jitter, and checks its reservation before
// any I/O that serves only the dispatch. An unpaced class is always now.
func (p *Pacer) Reserve(class, key string) time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	every, paced := p.every[class]
	if !paced {
		return now
	}
	p.sweepLocked(class, now)
	held := p.held[class]
	if slot, ok := held[key]; ok {
		if now.Before(slot.Add(ReservationLapse)) {
			return slot
		}
		delete(held, key)
	}
	slot := p.next[class]
	if slot.Before(now) {
		slot = now
	}
	p.next[class] = slot.Add(every)
	held[key] = slot
	return slot
}

// Consume releases key's reservation once its request's apply landed.
func (p *Pacer) Consume(class, key string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.held[class], key)
}

// sweepLocked drops lapsed reservations at most once a minute per class.
func (p *Pacer) sweepLocked(class string, now time.Time) {
	if now.Sub(p.swept[class]) < time.Minute {
		return
	}
	p.swept[class] = now
	for k, slot := range p.held[class] {
		if !now.Before(slot.Add(ReservationLapse)) {
			delete(p.held[class], k)
		}
	}
}
