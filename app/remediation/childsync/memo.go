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

// Package childsync is the in-process memo a container item key -- an
// Artist, an Author, a Comic -- keeps of its last complete child fan-out
// (2026-11-10 design D2, D14; the Series' episodeSyncMemo pattern, owner
// 2026-10-28: every Series pass asked the metadata gateway for its whole
// list). A pass that moved none of the fan-out's inputs lists the children
// it already has instead of fetching the provider's list again.
//
// It is a leaf package the item packages import (app/catalog/controller/
// artist, author, comic); app/remediation itself imports those packages
// for their indexes, so the memo cannot live there without a cycle.
package childsync

import (
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/types"
)

// Key is what decides a fan-out's outcome besides the provider's list: the
// spec (generation), the metadata refresh it read and whether the add
// options were applied (the first fan-out's monitoring).
type Key struct {
	Generation        int64
	RefreshedAt       time.Time
	AddOptionsApplied bool
}

// KeyAt is a Key over the inputs, the refresh time normalised to UTC and to
// the apiserver's precision: metav1.Time round-trips through time.Local and
// is stored to the second (RFC 3339), so a refresh carried in memory at
// nanoseconds -- a record just incorporated -- must compare equal to the
// same refresh read back, else the pass after the incorporation refetches
// once more. Truncate also drops a monotonic reading, which == would see.
func KeyAt(generation int64, refreshedAt time.Time, addOptionsApplied bool) Key {
	return Key{Generation: generation, RefreshedAt: refreshedAt.UTC().Truncate(time.Second), AddOptionsApplied: addOptionsApplied}
}

// Memo remembers, per item UID, the Key of its last complete fan-out in
// this process. The zero value is ready to use; a restart forgets it, so
// each container fans out once per process.
type Memo struct {
	mu   sync.Mutex
	last map[types.UID]Key
}

// Due reports whether uid's children must be fetched: never fanned out in
// this process, or an input moved since. A container with no metadata yet
// has no Key to compare and is always due; callers pass hasMetadata false
// for it -- or, as the Artist and Author do, list no children until the
// metadata is cached, since D2 keys the fan-out on its refresh and the
// first fan-out's monitor rules read it.
func (m *Memo) Due(uid types.UID, k Key, hasMetadata bool) bool {
	if !hasMetadata {
		return true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	last, seen := m.last[uid]
	return !seen || last != k
}

// Synced records a complete fan-out of uid under k. A container with no
// metadata records nothing: its next pass is due regardless.
func (m *Memo) Synced(uid types.UID, k Key, hasMetadata bool) {
	if !hasMetadata {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.last == nil {
		m.last = map[types.UID]Key{}
	}
	m.last[uid] = k
}

// Forget drops uid's record (a deleted item), so the memo does not grow
// with the library's churn.
func (m *Memo) Forget(uid types.UID) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.last, uid)
}
