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

package scenemap_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/scenemap"
)

func newCache(t *testing.T, clock clockwork.Clock) *metadata.LRUCache {
	t.Helper()
	c, err := metadata.NewLRUCache(64, clock)
	require.NoError(t, err)
	return c
}

func TestSceneMapCombinesRowsAndNames(t *testing.T) {
	s := serveXEM(t)
	src := scenemap.NewCached(s.xem(), newCache(t, clockwork.NewRealClock()), scenemap.Options{})

	m, err := src.SceneMap(context.Background(), 72454)

	require.NoError(t, err)
	require.EqualValues(t, 72454, m.TVDBID)
	require.Len(t, m.Mappings, 40)
	require.Contains(t, m.Names, scenemap.SceneName{Title: "Detektiv Conan"})
}

func TestASeriesHavemapDoesNotListCostsNoRequestOfItsOwn(t *testing.T) {
	s := serveXEM(t)
	src := scenemap.NewCached(s.xem(), newCache(t, clockwork.NewRealClock()), scenemap.Options{})

	m, err := src.SceneMap(context.Background(), 81797)

	require.NoError(t, err)
	require.Empty(t, m.Mappings)
	require.Zero(t, s.count("/map/all?81797"), "Sonarr asks for rows only for a series havemap lists")
}

func TestEverythingIsCachedUntilItsTTL(t *testing.T) {
	s := serveXEM(t)
	clock := clockwork.NewFakeClock()
	src := scenemap.NewCached(s.xem(), newCache(t, clock), scenemap.Options{})
	ctx := context.Background()

	for range 3 {
		_, err := src.SceneMap(ctx, 72454)
		require.NoError(t, err)
		_, err = src.SceneMap(ctx, 80644)
		require.NoError(t, err)
	}
	require.Equal(t, 1, s.count("/map/havemap"))
	require.Equal(t, 1, s.count("/map/allNames"))
	require.Equal(t, 1, s.count("/map/all?72454"))
	require.Equal(t, 1, s.count("/map/all?80644"))

	clock.Advance(scenemap.DefaultHaveMapTTL)
	_, err := src.SceneMap(ctx, 72454)
	require.NoError(t, err)
	require.Equal(t, 2, s.count("/map/havemap"), "the series list and names expire at three hours")
	require.Equal(t, 2, s.count("/map/allNames"))
	require.Equal(t, 1, s.count("/map/all?72454"), "a series' rows last twelve")

	clock.Advance(scenemap.DefaultMappingTTL)
	_, err = src.SceneMap(ctx, 72454)
	require.NoError(t, err)
	require.Equal(t, 2, s.count("/map/all?72454"))
}

// fakeFetcher counts calls and can block or fail them.
type fakeFetcher struct {
	haveMap  atomic.Int32
	mappings atomic.Int32
	names    atomic.Int32
	release  chan struct{}
	err      error
	rows     []scenemap.Mapping
}

func (f *fakeFetcher) HaveMap(ctx context.Context) ([]int64, error) {
	f.haveMap.Add(1)
	return []int64{7}, f.err
}

func (f *fakeFetcher) Names(ctx context.Context) (map[int64][]scenemap.SceneName, error) {
	f.names.Add(1)
	return map[int64][]scenemap.SceneName{}, nil
}

func (f *fakeFetcher) Mappings(ctx context.Context, id int64) ([]scenemap.Mapping, error) {
	f.mappings.Add(1)
	if f.release != nil {
		select {
		case <-f.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return f.rows, nil
}

func TestConcurrentCallersShareOneRequest(t *testing.T) {
	f := &fakeFetcher{release: make(chan struct{}), rows: []scenemap.Mapping{{Scene: scenemap.Numbering{Season: 1, Episode: 1}, TVDB: scenemap.Numbering{Season: 1, Episode: 1}}}}
	src := scenemap.NewCached(f, newCache(t, clockwork.NewRealClock()), scenemap.Options{})

	var wg sync.WaitGroup
	results := make([]*scenemap.Map, 8)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m, err := src.SceneMap(context.Background(), 7)
			assert.NoError(t, err)
			results[i] = m
		}()
	}
	require.Eventually(t, func() bool { return f.mappings.Load() == 1 }, 5*time.Second, time.Millisecond)
	time.Sleep(20 * time.Millisecond) // let the rest queue behind the leader
	close(f.release)
	wg.Wait()

	require.EqualValues(t, 1, f.mappings.Load())
	for _, m := range results {
		require.Len(t, m.Mappings, 1)
	}
}

func TestAFollowerDoesNotInheritTheLeadersCancellation(t *testing.T) {
	f := &fakeFetcher{release: make(chan struct{})}
	src := scenemap.NewCached(f, newCache(t, clockwork.NewRealClock()), scenemap.Options{})

	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	leaderDone := make(chan error, 1)
	go func() { _, err := src.SceneMap(leaderCtx, 7); leaderDone <- err }()
	require.Eventually(t, func() bool { return f.mappings.Load() == 1 }, 5*time.Second, time.Millisecond)

	followerDone := make(chan error, 1)
	go func() { _, err := src.SceneMap(context.Background(), 7); followerDone <- err }()
	time.Sleep(20 * time.Millisecond)
	cancelLeader()
	require.ErrorIs(t, <-leaderDone, context.Canceled)

	close(f.release)
	require.NoError(t, <-followerDone, "the follower fetches for itself")
	require.EqualValues(t, 2, f.mappings.Load())
}

func TestAFailureIsNotCached(t *testing.T) {
	boom := errors.New("thexem down")
	f := &fakeFetcher{err: boom}
	src := scenemap.NewCached(f, newCache(t, clockwork.NewRealClock()), scenemap.Options{})

	_, err := src.SceneMap(context.Background(), 7)
	require.ErrorIs(t, err, boom)

	f.err = nil
	m, err := src.SceneMap(context.Background(), 7)
	require.NoError(t, err)
	require.NotNil(t, m)
	require.EqualValues(t, 2, f.haveMap.Load())
}

func TestAnEmptyTableIsCachedAsAnAnswer(t *testing.T) {
	f := &fakeFetcher{}
	src := scenemap.NewCached(f, newCache(t, clockwork.NewRealClock()), scenemap.Options{})

	for range 3 {
		m, err := src.SceneMap(context.Background(), 7)
		require.NoError(t, err)
		require.Empty(t, m.Mappings)
	}
	require.EqualValues(t, 1, f.mappings.Load())
}
