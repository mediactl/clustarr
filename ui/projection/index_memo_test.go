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

package projection_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/ui/projection"
)

// memoClock is a settable clock for IndexMemo.
type memoClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *memoClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *memoClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newCountingMemo(t *testing.T) (*projection.IndexMemo, *atomic.Int32, *memoClock) {
	t.Helper()
	var builds atomic.Int32
	clock := &memoClock{now: time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)}
	memo := projection.NewIndexMemoFunc(func(ctx context.Context) (*projection.Index, error) {
		builds.Add(1)
		return projection.BuildIndex(ctx, nil)
	}, projection.IndexTTL, clock.Now)
	return memo, &builds, clock
}

// TestIndexMemoBuildsOncePerTTL is the review's per-request rebuild: two
// requests inside the TTL share one build and one Index; the first after it
// rebuilds.
func TestIndexMemoBuildsOncePerTTL(t *testing.T) {
	ctx := context.Background()
	memo, builds, clock := newCountingMemo(t)

	first, err := memo.Get(ctx)
	require.NoError(t, err)
	clock.Advance(projection.IndexTTL - time.Millisecond)
	second, err := memo.Get(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, builds.Load(), "two requests within the TTL build once")
	require.Same(t, first, second)

	clock.Advance(time.Millisecond)
	third, err := memo.Get(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 2, builds.Load(), "a request after the TTL rebuilds")
	require.NotSame(t, first, third)
}

// TestIndexMemoSharesOneBuildBetweenConcurrentCallers: a burst arriving on
// an expired memo builds once, not once per request.
func TestIndexMemoSharesOneBuildBetweenConcurrentCallers(t *testing.T) {
	var builds atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	memo := projection.NewIndexMemoFunc(func(ctx context.Context) (*projection.Index, error) {
		builds.Add(1)
		once.Do(func() { close(started) })
		<-release
		return projection.BuildIndex(ctx, nil)
	}, projection.IndexTTL, time.Now)

	const callers = 16
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := memo.Get(context.Background())
			errs <- err
		}()
	}
	<-started
	time.Sleep(50 * time.Millisecond) // let the rest join the flight
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.EqualValues(t, 1, builds.Load())
}

// A failed build is not memoised, and a caller whose request ends while
// waiting gets its own context's error without failing the flight.
func TestIndexMemoFailuresAndCancellation(t *testing.T) {
	ctx := context.Background()
	var calls atomic.Int32
	memo := projection.NewIndexMemoFunc(func(ctx context.Context) (*projection.Index, error) {
		if calls.Add(1) == 1 {
			return nil, errors.New("cache not synced")
		}
		return projection.BuildIndex(ctx, nil)
	}, projection.IndexTTL, time.Now)

	_, err := memo.Get(ctx)
	require.Error(t, err)
	_, err = memo.Get(ctx)
	require.NoError(t, err, "the failure was not memoised")
	require.EqualValues(t, 2, calls.Load())

	release := make(chan struct{})
	slow := projection.NewIndexMemoFunc(func(ctx context.Context) (*projection.Index, error) {
		<-release
		return projection.BuildIndex(ctx, nil)
	}, projection.IndexTTL, time.Now)
	cctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { _, err := slow.Get(cctx); done <- err }()
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	close(release)
	_, err = slow.Get(ctx)
	require.NoError(t, err, "the flight the cancelled caller left still completed")
}
