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

package remediation

import (
	"sync"
	"time"

	"golang.org/x/time/rate"
	"k8s.io/apimachinery/pkg/types"
)

// slotLapse is when an unused limiter slot is given back (§4.7's pacer rule).
const slotLapse = 10 * time.Minute

// WriteLimiter is the status-write token bucket (§3.7) with one reserved slot
// per key, so a paced pass requeued to its slot applies then without taking
// a second token. Status applies that no grant, answer, intent or first
// probe caused pass it (--remediation-bulk-writes-per-second, D39).
type WriteLimiter struct {
	mu    sync.Mutex
	lim   *rate.Limiter // nil: disabled
	slots map[types.UID]time.Time
}

// NewWriteLimiter allows perSecond status writes with burst; perSecond 0
// disables it.
func NewWriteLimiter(perSecond, burst int) *WriteLimiter {
	l := &WriteLimiter{slots: map[types.UID]time.Time{}}
	if perSecond > 0 {
		l.lim = rate.NewLimiter(rate.Limit(perSecond), max(burst, 1))
	}
	return l
}

// Admit returns how long key's pass must wait to apply: 0 when it may apply
// now (its slot is due, or a token is free), else the time to its slot.
func (l *WriteLimiter) Admit(key types.UID, now time.Time) time.Duration {
	if l == nil || l.lim == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if at, ok := l.slots[key]; ok {
		switch {
		case now.Sub(at) >= slotLapse:
			delete(l.slots, key)
		case !now.Before(at):
			delete(l.slots, key)
			return 0
		default:
			return at.Sub(now)
		}
	}
	res := l.lim.ReserveN(now, 1)
	d := res.DelayFrom(now)
	if d == 0 {
		return 0
	}
	if len(l.slots) >= sweepAt {
		for k, at := range l.slots {
			if now.Sub(at) >= slotLapse {
				delete(l.slots, k) // a deleted file's slot is never claimed
			}
		}
	}
	l.slots[key] = now.Add(d)
	return d
}

// sweepAt is the slot count past which Admit drops lapsed slots.
const sweepAt = 1024
