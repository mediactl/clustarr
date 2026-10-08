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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// The status-write limiter (§3.7) holds one slot per key: a requeued pass
// finds its own slot and never reserves a second; the slot is consumed when
// its pass applies; a slot never used lapses after ten minutes.
func TestTheWriteLimiterReservesOneSlotPerKey(t *testing.T) {
	t0 := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	l := NewWriteLimiter(10, 1)
	assert.Zero(t, l.Admit("a", t0), "the burst admits the first key at once")
	wb := l.Admit("b", t0)
	assert.InDelta(t, float64(100*time.Millisecond), float64(wb), float64(time.Millisecond))
	assert.Equal(t, wb, l.Admit("b", t0), "b's requeue finds its own slot, not a new one")
	assert.Equal(t, wb-40*time.Millisecond, l.Admit("b", t0.Add(40*time.Millisecond)), "and waits only the rest of it")
	wc := l.Admit("c", t0)
	assert.InDelta(t, float64(200*time.Millisecond), float64(wc), float64(time.Millisecond), "b's slot is one reservation, not two")
	t1 := t0.Add(wb)
	assert.Zero(t, l.Admit("b", t1), "b's slot is due: its pass applies")
	assert.Positive(t, l.Admit("b", t1), "and the slot is spent: b's next change waits again")
	t2 := t1.Add(time.Second)
	assert.Positive(t, l.Admit("d", t2.Add(-900*time.Millisecond)))
	assert.Zero(t, l.Admit("d", t2.Add(11*time.Minute)), "an unused slot lapses after ten minutes")
}

func TestAZeroRateDisablesTheLimiter(t *testing.T) {
	l := NewWriteLimiter(0, 0)
	for range 100 {
		assert.Zero(t, l.Admit("k", time.Now()))
	}
	var nilLimiter *WriteLimiter
	assert.Zero(t, nilLimiter.Admit("k", time.Now()), "a nil limiter admits")
}
