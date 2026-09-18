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
	"sync"
	"time"

	"github.com/jonboulle/clockwork"
)

// BreakerState is one of a CircuitBreaker's three states.
type BreakerState int

const (
	BreakerClosed BreakerState = iota
	BreakerOpen
	BreakerHalfOpen
)

func (s BreakerState) String() string {
	switch s {
	case BreakerClosed:
		return "closed"
	case BreakerOpen:
		return "open"
	case BreakerHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

// BreakerConfig configures a CircuitBreaker.
type BreakerConfig struct {
	FailureThreshold  int           // consecutive Failure() calls in Closed before opening; <= 0 defaults to 1
	Cooldown          time.Duration // how long Open lasts before one probe is let through as Half-Open
	HalfOpenSuccesses int           // consecutive Success() calls in Half-Open before closing; <= 0 defaults to 1
}

// CircuitBreaker is a closed/open/half-open breaker for a single failing
// dependency (one per indexer or provider; callers key their own map by
// name -- this type itself is not keyed). Safe for concurrent use.
type CircuitBreaker struct {
	mu        sync.Mutex
	cfg       BreakerConfig
	clock     clockwork.Clock
	state     BreakerState
	fails     int
	successes int
	openedAt  time.Time
	probing   bool
}

// NewCircuitBreaker builds a breaker in the Closed state. clock is
// clockwork.Clock (already a project dependency via pkg/events/membus),
// so tests drive Cooldown expiry deterministically with
// clockwork.NewFakeClock() instead of sleeping; nil defaults to
// clockwork.NewRealClock().
func NewCircuitBreaker(cfg BreakerConfig, clock clockwork.Clock) *CircuitBreaker {
	if cfg.FailureThreshold <= 0 {
		cfg.FailureThreshold = 1
	}
	if cfg.HalfOpenSuccesses <= 0 {
		cfg.HalfOpenSuccesses = 1
	}
	if clock == nil {
		clock = clockwork.NewRealClock()
	}
	return &CircuitBreaker{cfg: cfg, clock: clock, state: BreakerClosed}
}

// Allow reports whether a call should proceed. Always true in Closed.
// False in Open until Cooldown has elapsed since the breaker opened, at
// which point Allow transitions to Half-Open and returns true for exactly
// one caller; a second concurrent Allow while that probe is outstanding
// returns false, so a recovering dependency is probed at a controlled
// rate rather than stampeded the instant Cooldown expires.
func (b *CircuitBreaker) Allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.state {
	case BreakerClosed:
		return true
	case BreakerOpen:
		if b.clock.Now().Sub(b.openedAt) < b.cfg.Cooldown {
			return false
		}
		b.state = BreakerHalfOpen
		b.probing = true
		return true
	case BreakerHalfOpen:
		if b.probing {
			return false
		}
		b.probing = true
		return true
	default:
		return false
	}
}

// Success records a successful call. In Half-Open it clears the
// outstanding-probe flag and counts toward HalfOpenSuccesses, closing the
// breaker once reached. In Closed it resets the consecutive-failure
// count. No-op in Open.
func (b *CircuitBreaker) Success() {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.state {
	case BreakerClosed:
		b.fails = 0
	case BreakerHalfOpen:
		b.successes++
		b.probing = false
		if b.successes >= b.cfg.HalfOpenSuccesses {
			b.state = BreakerClosed
			b.fails = 0
			b.successes = 0
		}
	}
}

// Failure records a failed call. In Closed it increments the
// consecutive-failure count, opening the breaker (recording clock.Now())
// on reaching FailureThreshold. In Half-Open a single failure reopens the
// breaker immediately and resets the cooldown clock. No-op in Open.
func (b *CircuitBreaker) Failure() {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.state {
	case BreakerClosed:
		b.fails++
		if b.fails >= b.cfg.FailureThreshold {
			b.state = BreakerOpen
			b.openedAt = b.clock.Now()
			b.fails = 0
		}
	case BreakerHalfOpen:
		b.state = BreakerOpen
		b.openedAt = b.clock.Now()
		b.probing = false
		b.successes = 0
	}
}

// State returns the breaker's current state.
func (b *CircuitBreaker) State() BreakerState {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state
}
