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

package artwork

import (
	"context"
	"sync"
)

// KeyedLock serialises work per key, in process: the gateway's Fetcher.Lock
// made reusable, so the renderer holds one per item too (artwork design §B.3
// as amended 2026-10-07). Two Puts of one object name at once leak a full
// chunk set for good (research E8), and SetMeta has no compare-and-swap, so
// each variant's writer runs one item at a time. The zero value is ready to
// use; it must not be copied after first use.
type KeyedLock struct {
	mu    sync.Mutex
	locks map[string]*keyedSlot
}

// keyedSlot is one key's one-slot channel, reference-counted so the map
// keeps no entry for a key nobody holds or waits for.
type keyedSlot struct {
	ch   chan struct{}
	refs int
}

// Lock blocks until key is free or ctx ends. The returned func releases it
// and may be called more than once; a waiter whose ctx ended gets ctx's
// error and leaves no entry behind.
func (l *KeyedLock) Lock(ctx context.Context, key string) (unlock func(), err error) {
	l.mu.Lock()
	if l.locks == nil {
		l.locks = map[string]*keyedSlot{}
	}
	s := l.locks[key]
	if s == nil {
		s = &keyedSlot{ch: make(chan struct{}, 1)}
		l.locks[key] = s
	}
	s.refs++
	l.mu.Unlock()

	release := func() {
		l.mu.Lock()
		s.refs--
		if s.refs == 0 {
			delete(l.locks, key)
		}
		l.mu.Unlock()
	}
	select {
	case s.ch <- struct{}{}:
		var once sync.Once
		return func() {
			once.Do(func() {
				<-s.ch
				release()
			})
		}, nil
	case <-ctx.Done():
		release()
		return nil, ctx.Err()
	}
}
