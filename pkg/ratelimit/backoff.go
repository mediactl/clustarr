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

package ratelimit

import (
	"math"
	"math/rand/v2"
	"time"
)

// Backoff computes exponential retry delays with jitter and an optional
// server-supplied override.
type Backoff struct {
	Base       time.Duration // attempt 0's delay before jitter; must be > 0
	Max        time.Duration // ceiling after exponentiation, before jitter; <= 0 means no ceiling
	Multiplier float64       // growth per attempt; <= 0 defaults to 2
	Jitter     float64       // randomised fraction of the delay, clamped to [0,1]; 0 disables jitter
}

// Next returns the delay before attempt (0-based) should retry. When
// retryAfter > 0 it is returned completely unchanged -- a server's
// Retry-After is authoritative in both directions (never lowered by Max,
// never raised by Base), matching docs/research/indexers.md §6 ("429 ->
// Retry-After honoured"). Otherwise Next returns
// min(Base * Multiplier^attempt, Max) widened or narrowed by up to
// +/-Jitter/2 of itself, using math/rand/v2 so concurrent callers
// computing the same attempt do not retry in lockstep.
func (b Backoff) Next(attempt int, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		return retryAfter
	}

	mult := b.Multiplier
	if mult <= 0 {
		mult = 2
	}
	delay := float64(b.Base) * math.Pow(mult, float64(attempt))
	if b.Max > 0 && delay > float64(b.Max) {
		delay = float64(b.Max)
	}

	jitter := b.Jitter
	if jitter < 0 {
		jitter = 0
	} else if jitter > 1 {
		jitter = 1
	}
	if jitter > 0 {
		spread := delay * jitter
		delay += (rand.Float64()*2 - 1) * spread / 2
		if delay < 0 {
			delay = 0
		}
	}
	return time.Duration(delay)
}
