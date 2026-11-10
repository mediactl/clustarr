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

package backlog

import (
	"context"
	"sync"
	"time"

	"github.com/mediactl/clustarr/pkg/events/schema"
)

// WakeBuffer is the book's wake channel buffer: more than a cycle's grants
// at the largest perCycle, so a cycle's wakes seldom wait.
const WakeBuffer = 256

// Grant is one item's backlog search: it may search live once, for
// Reason, until Until.
type Grant struct {
	Reason schema.SearchReason
	At     time.Time
	Until  time.Time
}

// Book is the leader-local grant book, as the donor budget is: the
// BacklogSearch controller grants, the search stage reads and spends. A new
// leader starts empty, and the next cycle grants again: an item whose grant
// was lost was not searched, so it is still due.
type Book struct {
	mu     sync.Mutex
	grants map[string]Grant // by item uid
	wakes  chan schema.ItemRef
}

// NewBook is an empty book.
func NewBook() *Book {
	return &Book{grants: map[string]Grant{}, wakes: make(chan schema.ItemRef, WakeBuffer)}
}

// Grant records g for the item uid.
func (b *Book) Grant(uid string, g Grant) {
	if b == nil || uid == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.grants[uid] = g
	b.prune(g.At)
}

// Granted is the item's grant, false when it has none or it lapsed by now.
func (b *Book) Granted(uid string, now time.Time) (Grant, bool) {
	if b == nil {
		return Grant{}, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	g, ok := b.grants[uid]
	if !ok || !now.Before(g.Until) {
		return Grant{}, false
	}
	return g, true
}

// Spend drops the item's grant: its live search was dispatched, or its
// season's answer folded in.
func (b *Book) Spend(uid string) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.grants, uid)
}

// Len is how many grants the book holds, lapsed ones included until the
// next Grant prunes them.
func (b *Book) Len() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.grants)
}

// prune drops the grants lapsed by now. Under mu.
func (b *Book) prune(now time.Time) {
	for uid, g := range b.grants {
		if !now.Before(g.Until) {
			delete(b.grants, uid)
		}
	}
}

// Wake asks the remediation loop to plan ref's key, waiting for room until
// ctx ends.
func (b *Book) Wake(ctx context.Context, ref schema.ItemRef) error {
	if b == nil {
		return nil
	}
	select {
	case b.wakes <- ref:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Wakes is the channel the remediation loop reads the book's wakes from;
// nil for a nil book.
func (b *Book) Wakes() <-chan schema.ItemRef {
	if b == nil {
		return nil
	}
	return b.wakes
}
