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

package records_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/mediactl/clustarr/pkg/records"
)

// TestThePacerReservesOneSlotPerKey (loop spec §4.7): a key holds one slot
// however often its pass requeues, a consumed reservation frees the key, a
// reservation unconsumed 10 minutes past its slot lapses, and an unpaced
// class is always now. 1,000 due files at 600/min take 100 s of slots, not one
// reservation per pass.
func TestThePacerReservesOneSlotPerKey(t *testing.T) {
	c := newClock()
	t0 := c.Now()
	p := records.NewPacer(records.DefaultRates(), c.Now)
	const lane = records.PaceProbeLow
	slot := func(n int) time.Time { return t0.Add(time.Duration(n) * 100 * time.Millisecond) }

	a := p.Reserve(lane, "a")
	assert.Equal(t, slot(0), a)
	assert.Equal(t, a, p.Reserve(lane, "a"), "a requeued pass finds its own slot")
	b := p.Reserve(lane, "b")
	assert.Equal(t, slot(1), b)
	for i := range 1000 {
		k := fmt.Sprint(i)
		first := p.Reserve(lane, k)
		assert.Equal(t, slot(2+i), first)
		assert.Equal(t, first, p.Reserve(lane, k))
	}
	assert.Equal(t, slot(1002), p.Reserve(lane, "last"))

	p.Consume(lane, "a")
	assert.Equal(t, slot(1003), p.Reserve(lane, "a"), "a consumed key reserves anew")

	c.Add(b.Sub(t0) + records.ReservationLapse + time.Second)
	assert.Equal(t, c.Now(), p.Reserve(lane, "b"), "an unconsumed reservation lapses 10 minutes after its slot")

	assert.Equal(t, c.Now(), p.Reserve("transcode", "x"), "transcode and graft are bounded by the ledger, not paced")
	assert.Equal(t, map[string]int{
		records.PaceProbeLow: 600, records.PaceSubtitle: 120, records.PaceMarkers: 120, records.PaceSearch: 20,
	}, records.DefaultRates())
}

// TestTheSearchClassPacesPerNamespace (ADR-0019 §5.4): a live automatic
// search reserves in "search/<namespace>", which paces at the "search" rate
// (20/min, a slot every 3 s) with its own slots per namespace; a class with a
// rate of its own keeps it, and a class whose prefix has no rate is unpaced.
func TestTheSearchClassPacesPerNamespace(t *testing.T) {
	c := newClock()
	t0 := c.Now()
	p := records.NewPacer(records.DefaultRates(), c.Now)
	a, b := records.SearchClass("a"), records.SearchClass("b")
	assert.Equal(t, "search/a", a)

	assert.Equal(t, t0, p.Reserve(a, "x"))
	assert.Equal(t, t0.Add(3*time.Second), p.Reserve(a, "y"), "search/a paces at 20/min")
	assert.Equal(t, t0.Add(6*time.Second), p.Reserve(a, "z"))
	assert.Equal(t, t0, p.Reserve(b, "x"), "search/b has slots of its own, and its key x is not search/a's")
	assert.Equal(t, t0.Add(3*time.Second), p.Reserve(b, "y"))
	assert.Equal(t, t0.Add(3*time.Second), p.Reserve(a, "y"), "a reservation stays per full class and key")

	assert.Equal(t, t0, p.Reserve(records.PaceProbeLow, "x"))
	assert.Equal(t, t0.Add(100*time.Millisecond), p.Reserve(records.PaceProbeLow, "y"), "probe/low keeps its exact 600/min")
	assert.Equal(t, t0, p.Reserve("transcode/media", "x"), "a prefix with no rate is unpaced")
	assert.Equal(t, t0, p.Reserve("transcode/media", "y"))

	// The exact name wins over the prefix: with a "probe" rate beside
	// "probe/low", probe/low still paces at its own.
	q := records.NewPacer(map[string]int{"probe": 6, records.PaceProbeLow: 600}, c.Now)
	assert.Equal(t, t0, q.Reserve(records.PaceProbeLow, "x"))
	assert.Equal(t, t0.Add(100*time.Millisecond), q.Reserve(records.PaceProbeLow, "y"))
	assert.Equal(t, t0, q.Reserve("probe/high", "x"))
	assert.Equal(t, t0.Add(10*time.Second), q.Reserve("probe/high", "y"), "probe/high falls back to the probe rate")
}
