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

package throttle_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/app/caption/throttle"
)

func TestAcquireGrantsUpToBurstImmediately(t *testing.T) {
	kv := testKV(t)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()

	start := time.Now()
	for range 3 { // rateMilli=3000 -> 3 req/s -> burst of 3 tokens
		require.NoError(t, throttle.Acquire(ctx, kv, "uid-burst", 3000))
	}
	elapsed := time.Since(start)
	assert.Less(t, elapsed, 200*time.Millisecond,
		"the burst must be spent without waiting -- it is exactly what capacity is for")
}

func TestAcquireBlocksOnceTheBurstIsExhausted(t *testing.T) {
	kv := testKV(t)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()

	const rateMilli = 4000 // 4 req/s -> 250ms per token once the burst is spent
	for range 4 {
		require.NoError(t, throttle.Acquire(ctx, kv, "uid-exhaust", rateMilli))
	}

	start := time.Now()
	require.NoError(t, throttle.Acquire(ctx, kv, "uid-exhaust", rateMilli))
	elapsed := time.Since(start)
	assert.GreaterOrEqual(t, elapsed, 100*time.Millisecond,
		"the 5th call must wait for a refill, not spend a token that was not there")
	assert.Less(t, elapsed, 1*time.Second, "and must not wait dramatically longer than one token period")
}

func TestAcquireReturnsTheContextErrorWhenCancelledWhileWaiting(t *testing.T) {
	kv := testKV(t)
	const rateMilli = 1000 // 1 req/s -> the 2nd call needs a full second

	require.NoError(t, throttle.Acquire(t.Context(), kv, "uid-cancel", rateMilli))

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	err := throttle.Acquire(ctx, kv, "uid-cancel", rateMilli)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestAcquireDefaultsANonPositiveRateRatherThanDividingByZero(t *testing.T) {
	kv := testKV(t)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()

	var err1, err2 error
	require.NotPanics(t, func() { err1 = throttle.Acquire(ctx, kv, "uid-zero-rate", 0) })
	require.NoError(t, err1)
	require.NotPanics(t, func() { err2 = throttle.Acquire(ctx, kv, "uid-negative-rate", -5) })
	require.NoError(t, err2)
}

// TestTwoWorkersRacingForTheLastTokenBothEventuallySucceed is the scenario
// the task description calls out by name: two workers reach for the same,
// single remaining token. Capacity is exactly one token (rateMilli=1000, so
// burst = 1 request/second's worth), so at most one of the two concurrent
// calls can be satisfied by the starting bucket; the other must observe the
// winner's consumption via CAS and wait roughly one token period, not
// receive a duplicate grant and not hang forever.
//
// A start gate (both goroutines park on <-start; the test only closes it
// once both have reported ready) is not decoration: without it, two
// goroutines launched back-to-back against a fast in-memory bus tend to run
// their whole Get-compute-write sequence one after the other rather than
// truly overlapping, so a version of Acquire with NO compare-and-swap at all
// (an outright bug -- verified by deliberately breaking it) can still pass
// this test by accident, because the second goroutine's Get happens to
// already observe the first's write. The gate forces the actually-contended
// case: both Gets racing against the SAME starting revision.
func TestTwoWorkersRacingForTheLastTokenBothEventuallySucceed(t *testing.T) {
	kv := testKV(t)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()

	const rateMilli = 1000 // capacity = 1 token

	var ready sync.WaitGroup
	ready.Add(2)
	start := make(chan struct{})

	var wg sync.WaitGroup
	elapsed := make([]time.Duration, 2)
	errs := make([]error, 2)
	var startTime time.Time
	for i := range 2 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ready.Done()
			<-start
			errs[i] = throttle.Acquire(ctx, kv, "uid-race", rateMilli)
			elapsed[i] = time.Since(startTime)
		}(i)
	}
	ready.Wait() // both goroutines are parked at <-start
	startTime = time.Now()
	close(start) // release both at the same instant
	wg.Wait()

	require.NoError(t, errs[0])
	require.NoError(t, errs[1])

	fast, slow := elapsed[0], elapsed[1]
	if slow < fast {
		fast, slow = slow, fast
	}
	assert.Less(t, fast, 150*time.Millisecond, "one of the two must win the starting token almost immediately")
	assert.GreaterOrEqual(t, slow, 700*time.Millisecond,
		"the loser must wait for a fresh token rather than being granted a second one that was never there")
}
