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

package projection

import (
	"context"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// IndexTTL is how long one built [Index] answers Plex requests before the
// next request rebuilds it. Plex asks in bursts -- a library scan is a match
// per item, then a metadata, images and children call per match -- and
// rebuilding lists every Movie, Series and Episode, so five seconds turns a
// scan's thousands of full-catalogue builds into one per window, while a
// catalogue edit still reaches Plex within one. It is invalidated on TTL
// only: nothing here watches for changes.
const IndexTTL = 5 * time.Second

// IndexMemo memoises [BuildIndex] for [IndexTTL]: every caller inside the
// window shares one built Index, and concurrent callers once it lapses
// share one build (singleflight) rather than each listing the catalogue.
// The shared Index is read-only by contract -- every accessor returns a
// copy of any slice a caller could reorder -- and its objects may be the
// informer cache's own (see [NewIndexMemo]), so nothing may write through
// them.
type IndexMemo struct {
	build func(context.Context) (*Index, error)
	ttl   time.Duration
	now   func() time.Time

	flight singleflight.Group

	mu      sync.Mutex
	idx     *Index
	builtAt time.Time
}

// NewIndexMemo memoises BuildIndex over r for [IndexTTL]. It lists with
// client.UnsafeDisableDeepCopy: the Index is only ever read, so a deep copy
// of every catalogue object per build is pure garbage. An informer-backed
// reader (ui.NewClusterReader) honours it; a direct or fake client ignores
// it and copies as before.
func NewIndexMemo(r client.Reader) *IndexMemo {
	return NewIndexMemoFunc(func(ctx context.Context) (*Index, error) {
		return BuildIndex(ctx, r, client.UnsafeDisableDeepCopy)
	}, IndexTTL, time.Now)
}

// NewIndexMemoFunc memoises build for ttl against now: [NewIndexMemo]'s
// seam, for a test that counts builds and moves the clock.
func NewIndexMemoFunc(build func(context.Context) (*Index, error), ttl time.Duration, now func() time.Time) *IndexMemo {
	return &IndexMemo{build: build, ttl: ttl, now: now}
}

// Get returns the memoised Index, building it when none is younger than the
// TTL. A caller whose ctx ends while waiting on another's build gives up
// with ctx's error; the build itself runs detached from any one caller's
// cancellation (context.WithoutCancel), so the first caller hanging up does
// not fail every request sharing its flight. A failed build is not
// memoised: the next call tries again.
func (m *IndexMemo) Get(ctx context.Context) (*Index, error) {
	if idx, ok := m.fresh(); ok {
		return idx, nil
	}
	ch := m.flight.DoChan("index", func() (any, error) {
		// A flight that finished between fresh() and DoChan left a
		// current Index: take it rather than build twice.
		if idx, ok := m.fresh(); ok {
			return idx, nil
		}
		started := m.now()
		idx, err := m.build(context.WithoutCancel(ctx))
		if err != nil {
			return nil, err
		}
		m.mu.Lock()
		// Aged from when the lists began, not when they ended: the data
		// is as old as the first List.
		m.idx, m.builtAt = idx, started
		m.mu.Unlock()
		return idx, nil
	})
	select {
	case res := <-ch:
		if res.Err != nil {
			return nil, res.Err
		}
		return res.Val.(*Index), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (m *IndexMemo) fresh() (*Index, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.idx == nil || m.now().Sub(m.builtAt) >= m.ttl {
		return nil, false
	}
	return m.idx, true
}
