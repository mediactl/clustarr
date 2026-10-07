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
	"strings"
	"sync"
	"time"
)

// Pacer classes (§4.7). The probe high lane, transcode and graft are bounded
// by the import rate and the admission ledger, and are not paced.
const (
	PaceProbeLow = "probe/low"
	PaceSubtitle = "subtitle"
	PaceMarkers  = "markers"
	// PaceSearch paces live automatic searches per namespace (ADR-0019
	// §5.4): a planner reserves in class "search/<namespace>", which paces
	// at PaceSearch's rate. Index-only searches are not paced.
	PaceSearch = "search"
	// ReservationLapse frees a reservation not consumed this long after its slot.
	ReservationLapse = 10 * time.Minute
)

// DefaultRates are §4.7's admission rates, per minute, and ADR-0019 §5.4's
// live-search rate (--search-live-per-minute).
func DefaultRates() map[string]int {
	return map[string]int{PaceProbeLow: 600, PaceSubtitle: 120, PaceMarkers: 120, PaceSearch: 20}
}

// SearchClass is the pacing class of a live automatic search in namespace.
func SearchClass(namespace string) string { return PaceSearch + "/" + namespace }

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
// unpaced. A class's rate is looked up by its exact name first, then by the
// part before its first '/', so "search/media" paces at the "search" rate;
// reservations stay per full class name and key. A nil now is time.Now.
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

// rate is class's slot interval: its own, else its prefix's.
func (p *Pacer) rate(class string) (time.Duration, bool) {
	if every, ok := p.every[class]; ok {
		return every, true
	}
	if prefix, _, ok := strings.Cut(class, "/"); ok {
		every, ok := p.every[prefix]
		return every, ok
	}
	return 0, false
}

// Reserve returns key's slot in class: the slot it holds, or else the next
// free one, which it then holds. A planner whose slot is in the future returns
// Due = the slot plus up to 5 s of jitter, and checks its reservation before
// any I/O that serves only the dispatch. An unpaced class is always now.
func (p *Pacer) Reserve(class, key string) time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	every, paced := p.rate(class)
	if !paced {
		return now
	}
	p.sweepLocked(class, now)
	held := p.held[class]
	if held == nil {
		held = map[string]time.Time{}
		p.held[class] = held
	}
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
