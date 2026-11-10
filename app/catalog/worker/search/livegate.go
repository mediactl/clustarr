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

package search

import (
	"context"
	"slices"
	"sync"
)

// DefaultLiveSearches is how many live searches -- the ones that ask the
// indexers, not the release index -- one catalog agent runs at once
// (--live-searches; owner, 2026-11-10). Every live search fans out to every
// indexer at once, and each indexer takes one request per its
// spec.requestDelay (index's per-host limiter), so n live searches in
// flight queue n deep on every limiter: on talos the two search lanes' 16
// slots queued 16 deep behind a 2 s delay, a 32 s wait inside a 43 s
// budget, and a search that took over 30 s lapsed and was searched a second
// time (the durables' first BackOff step). Four is what the slowest
// indexer serves: nyaasi, 2 s apart through the VPN proxy and about 2.3 s
// a request, answers four searches in about 8 s, where the usenet API
// indexers, 500 ms apart, answer them in 2 s. The rest wait here, which
// costs no indexer anything, and a person's search goes first.
const DefaultLiveSearches = 4

// liveGate admits at most n live searches at once. A waiter a person is
// waiting on (SearchTask.UserInvoked) is admitted before every automatic
// one, each kind in arrival order. A nil gate admits everything.
type liveGate struct {
	mu   sync.Mutex
	free int
	// waiting is the queue per priority: [0] user-invoked, [1] automatic.
	waiting [2][]chan struct{}
}

// newLiveGate is a gate for n searches at once; n < 1 is no gate.
func newLiveGate(n int) *liveGate {
	if n < 1 {
		return nil
	}
	return &liveGate{free: n}
}

// acquire waits for a place under ctx and returns its release, which the
// caller calls exactly once. A cancelled wait takes no place.
func (g *liveGate) acquire(ctx context.Context, user bool) (release func(), err error) {
	if g == nil {
		return func() {}, nil
	}
	g.mu.Lock()
	if g.free > 0 {
		g.free--
		g.mu.Unlock()
		return g.releaser(), nil
	}
	p := 1
	if user {
		p = 0
	}
	ch := make(chan struct{})
	g.waiting[p] = append(g.waiting[p], ch)
	g.mu.Unlock()

	select {
	case <-ch:
		return g.releaser(), nil
	case <-ctx.Done():
	}
	g.mu.Lock()
	if i := slices.Index(g.waiting[p], ch); i >= 0 {
		g.waiting[p] = slices.Delete(g.waiting[p], i, i+1)
		g.mu.Unlock()
		return nil, ctx.Err()
	}
	g.mu.Unlock()
	// release handed this waiter its place as the context ended: pass the
	// place on rather than lose it.
	<-ch
	g.releaser()()
	return nil, ctx.Err()
}

// releaser is one place's release: it hands the place to the first waiter,
// user-invoked first, or frees it.
func (g *liveGate) releaser() func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			defer g.mu.Unlock()
			for p := range g.waiting {
				if len(g.waiting[p]) > 0 {
					ch := g.waiting[p][0]
					g.waiting[p] = g.waiting[p][1:]
					close(ch)
					return
				}
			}
			g.free++
		})
	}
}

// queued is how many wait, for tests.
func (g *liveGate) queued() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.waiting[0]) + len(g.waiting[1])
}
