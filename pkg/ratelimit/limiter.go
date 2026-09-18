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
	"context"
	"fmt"
	"sync"

	"golang.org/x/time/rate"
)

// Config is one key's token-bucket parameters.
type Config struct {
	RPS   float64 // steady-state tokens/sec; <= 0 means unlimited (Wait/Allow always succeed immediately)
	Burst int     // bucket capacity; <= 0 is treated as 1 (a real Burst of 0 makes x/time/rate refuse every request, never what a caller who only set RPS wants)
}

// Limiter is a keyed token-bucket limiter, one golang.org/x/time/rate.Limiter
// per key, created lazily on first use from the key's own Config (set via
// SetConfig) or, absent one, the Limiter's default Config. Safe for
// concurrent use.
type Limiter struct {
	mu       sync.Mutex
	defaults Config
	buckets  map[string]*rate.Limiter
	configs  map[string]Config
}

// New returns a Limiter that falls back to defaults for any key without
// its own SetConfig call.
func New(defaults Config) *Limiter {
	return &Limiter{
		defaults: defaults,
		buckets:  make(map[string]*rate.Limiter),
		configs:  make(map[string]Config),
	}
}

func newBucket(cfg Config) *rate.Limiter {
	limit := rate.Limit(cfg.RPS)
	if cfg.RPS <= 0 {
		limit = rate.Inf
	}
	return rate.NewLimiter(limit, burstOrOne(cfg.Burst))
}

func burstOrOne(b int) int {
	if b <= 0 {
		return 1
	}
	return b
}

func (l *Limiter) bucket(key string) *rate.Limiter {
	l.mu.Lock()
	defer l.mu.Unlock()
	if b, ok := l.buckets[key]; ok {
		return b
	}
	cfg, ok := l.configs[key]
	if !ok {
		cfg = l.defaults
	}
	b := newBucket(cfg)
	l.buckets[key] = b
	return b
}

// Wait blocks until key's bucket has a token or ctx is done, whichever
// comes first, returning ctx's error in the latter case.
func (l *Limiter) Wait(ctx context.Context, key string) error {
	err := l.bucket(key).Wait(ctx)
	if err == nil {
		return nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if _, hasDeadline := ctx.Deadline(); hasDeadline {
		// golang.org/x/time/rate.Limiter.Wait precomputes, before ever
		// blocking, whether a token could arrive before ctx's deadline;
		// when it cannot it returns its own "would exceed context
		// deadline" error rather than waiting for the deadline to
		// actually elapse and surfacing ctx.Err(). That is functionally
		// the same outcome -- the call could never have succeeded within
		// ctx's remaining time -- so it is normalised to
		// context.DeadlineExceeded here rather than leaking rate's
		// internal, unwrapped error text to callers that reasonably
		// expect errors.Is(err, context.DeadlineExceeded) to hold.
		return fmt.Errorf("ratelimit: wait for %w: %w", context.DeadlineExceeded, err)
	}
	return err
}

// Allow reports whether key currently has a token available, consuming
// one if so, without blocking.
func (l *Limiter) Allow(key string) bool {
	return l.bucket(key).Allow()
}

// SetConfig installs cfg for key immediately: golang.org/x/time/rate's
// SetLimit/SetBurst apply to the bucket in place if it already exists, so
// tokens already accumulated are preserved rather than the bucket
// resetting.
func (l *Limiter) SetConfig(key string, cfg Config) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.configs[key] = cfg
	delete(l.buckets, key) // recreated lazily on next use with the new Config
}
