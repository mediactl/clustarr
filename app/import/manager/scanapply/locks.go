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

package scanapply

import (
	"context"
	"sync"
)

// itemLocks serialises the create decision per item (ensureItem): the
// intake's scan durable runs several observations at once, and every part
// of a proposed audiobook carries its create, so without it two parts could
// both read the item absent, both apply it and both count it created --
// and the second apply, computing its own authorRef, could release the
// first's. Held from the fresh read to the apply, so the second observation
// reads what the first wrote. One process is enough: the consumer is the
// manager leader's alone. The zero value is ready; an entry lives while
// anyone holds or waits for its key.
type itemLocks struct {
	mu    sync.Mutex
	locks map[string]*itemLock
}

type itemLock struct {
	held chan struct{}
	refs int
}

// lock waits for key, or for ctx to end; unlock releases it.
func (l *itemLocks) lock(ctx context.Context, key string) (unlock func(), err error) {
	l.mu.Lock()
	if l.locks == nil {
		l.locks = map[string]*itemLock{}
	}
	e := l.locks[key]
	if e == nil {
		e = &itemLock{held: make(chan struct{}, 1)}
		l.locks[key] = e
	}
	e.refs++
	l.mu.Unlock()
	release := func() {
		l.mu.Lock()
		if e.refs--; e.refs == 0 {
			delete(l.locks, key)
		}
		l.mu.Unlock()
	}
	select {
	case e.held <- struct{}{}:
		return func() { <-e.held; release() }, nil
	case <-ctx.Done():
		release()
		return nil, ctx.Err()
	}
}
