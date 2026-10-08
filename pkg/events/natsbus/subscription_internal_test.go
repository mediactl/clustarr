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

package natsbus

import (
	"testing"
	"time"

	"github.com/mediactl/clustarr/pkg/events"
)

// TestLapsedDeliveriesNeverExceedTheSlots drives the reaper's bookkeeping
// without a server: a lapse frees a slot only while fewer than slots are
// lapsed, so a pod runs at most twice its slots (slots live, slots lapsed),
// and at the lapsed cap with every slot live it fetches nothing (spec §9.3;
// W4.90 retired the Fetch(1) Wave 4c kept open there).
func TestLapsedDeliveriesNeverExceedTheSlots(t *testing.T) {
	sub := events.Subscription{Durable: "nb-internal-lapse", Backoff: []time.Duration{time.Second}, MaxDeliver: 3}
	s := &subscription{
		sub:     sub,
		slots:   2,
		timing:  sub.Timing(),
		running: map[*delivery]struct{}{},
		wake:    make(chan struct{}, 1),
	}
	now := time.Now()
	overdue := func() *delivery {
		d := &delivery{seq: uint64(len(s.running) + 1), attempt: 1, deadline: now.Add(-2 * time.Second)}
		s.running[d] = struct{}{}
		s.live++
		return d
	}
	first, _ := overdue(), overdue()
	if n := s.next(); n != 0 {
		t.Fatalf("every slot live: next() = %d, want 0", n)
	}
	s.reap(now)
	if s.live != 0 || s.lapsed != 2 {
		t.Fatalf("after the first reap: live %d lapsed %d, want 0 and 2", s.live, s.lapsed)
	}
	if n := s.next(); n != 2 {
		t.Fatalf("two lapsed, none live: next() = %d, want 2", n)
	}
	select {
	case <-s.wake:
	default:
		t.Fatal("a reap that freed slots did not wake the pull loop")
	}
	overdue()
	overdue()
	s.reap(now)
	if s.live != 2 || s.lapsed != 2 {
		t.Fatalf("at the lapsed cap: live %d lapsed %d, want 2 and 2 (four handlers, twice the slots)", s.live, s.lapsed)
	}
	if n := s.next(); n != 0 {
		t.Fatalf("at the lapsed cap with every slot held: next() = %d, want 0 (nothing fetched to park)", n)
	}
	if s.saturatedSince.IsZero() {
		t.Error("the lapsed cap is held but the subscription does not read saturated")
	}
	s.finish(first)
	s.reap(now)
	if s.live != 1 || s.lapsed != 2 {
		t.Fatalf("after a lapsed handler returned: live %d lapsed %d, want 1 and 2", s.live, s.lapsed)
	}
	if n := s.next(); n != 1 {
		t.Fatalf("a lapsed place freed, one live overdue: next() = %d, want 1", n)
	}
}

// TestNextFetchesOnlyFreeSlots: the pull loop asks for exactly the free slots
// and nothing at saturation -- no Fetch(1) kept open at the lapsed cap, since
// each redelivery of a message it parked spent an attempt (split §9.3 as
// amended 2026-10-07, M1).
func TestNextFetchesOnlyFreeSlots(t *testing.T) {
	for _, tc := range []struct {
		name                string
		slots, live, lapsed int
		want                int
	}{
		{"idle", 2, 0, 0, 2},
		{"one slot free", 2, 1, 0, 1},
		{"every slot live", 2, 2, 0, 0},
		{"lapsed cap, every slot live", 2, 2, 2, 0},
		{"lapsed cap, one slot free", 2, 1, 2, 1},
		{"one slot, wedged", 1, 1, 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &subscription{slots: tc.slots, live: tc.live, lapsed: tc.lapsed, running: map[*delivery]struct{}{}}
			if got := s.next(); got != tc.want {
				t.Errorf("next() = %d, want %d", got, tc.want)
			}
		})
	}
}
