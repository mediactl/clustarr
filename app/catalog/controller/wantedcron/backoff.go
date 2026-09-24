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

package wantedcron

import (
	"time"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// The backoff constants from spec §6.1: "per-item >=6h gap and Attempts
// backoff 6h*2^n capped 7d".
const (
	// MinimumGap is the floor: an item is never searched twice inside six
	// hours, however few attempts it has behind it.
	MinimumGap = 6 * time.Hour

	// MaximumGap caps the doubling. Seven days is the point past which a
	// never-found item costs more in indexer queries than it is worth.
	MaximumGap = 7 * 24 * time.Hour
)

// Backoff is how long to wait after the most recent search attempt before
// searching again.
//
// The spec gives the formula (6h*2^n capped 7d) but not its base case, so this
// package pins one: n = Count-1, with Count floored at 1. That makes the FIRST
// retry wait the documented minimum six-hour gap rather than twelve, which is
// the reading that keeps §6.1's two clauses -- the ">=6h gap" and the
// "6h*2^n" -- describing the same schedule instead of two different ones. The
// resulting ladder is 6h, 6h, 12h, 24h, 48h, 96h, 168h, 168h, ...
//
// Task C8's search worker calls this per item when it expands a WantedScan.
func Backoff(a commonv1.Attempts) time.Duration {
	n := a.Count
	if n < 1 {
		n = 1
	}
	// Cap the exponent before shifting: 1 << 62 overflows a time.Duration,
	// and an item with hundreds of failed attempts is not hypothetical.
	if n > 32 {
		return MaximumGap
	}
	d := MinimumGap * time.Duration(1) << (n - 1)
	if d > MaximumGap || d <= 0 {
		return MaximumGap
	}
	return d
}

// NextEligible is the earliest instant an item may be searched again.
//
// A zero time means "eligible now", which is what an item with no recorded
// attempt gets: a never-searched item must never be held back, or an item
// added between two sweeps would wait a full backoff window for its first
// search.
func NextEligible(a commonv1.Attempts) time.Time {
	if a.Latest == nil {
		return time.Time{}
	}
	return a.Latest.Add(Backoff(a))
}

// Eligible reports whether an item whose search Attempts are a may be searched
// at now.
func Eligible(a commonv1.Attempts, now time.Time) bool {
	next := NextEligible(a)
	return next.IsZero() || !next.After(now)
}
