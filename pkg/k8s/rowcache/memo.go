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

package rowcache

import (
	"container/list"
	"reflect"
	"sync"

	"k8s.io/apimachinery/pkg/runtime"
)

// memoBudget is how much JSON one kind's memo stands for: the objects read
// most recently, kept decoded, up to this many bytes of their rows' JSON.
// A decoded object is about twice its JSON, so five kinds hold some 40 MB
// at the most, where their decoded whole was 230 MB and more.
const memoBudget = 4 << 20

// memoListLimit is the longest List whose misses are remembered: a list of
// a whole kind would turn the memo over once and leave nothing of use.
const memoListLimit = 64

// memo keeps the objects of one kind read most recently as they were
// decoded, so that reading one again is a copy and not an inflate and a
// JSON decode.
//
// Rows made a read cost what the informer cache made free. On talos
// (2026-11-28) the manager spent 63% of its CPU in codec.decodeInto, held
// at its 750m limit: an item's pass reads its Series for its monitoring,
// its owner, its settings and its profile, in every stage, and decoded it
// each time -- One Piece's is 168 KB of JSON, and a thousand of its
// episodes were in flight. Thirty searches the agents had answered in
// seconds sat unfolded for half an hour, and titles timed out being read.
// The same few objects are read again and again, so a small memo takes
// most of that without giving back the memory the rows saved.
//
// An entry is the row it was decoded from: a changed object is a new row,
// which the memo does not know, so nothing stale is ever returned, and the
// entry of the old row is replaced by the key's next read.
type memo struct {
	mu     sync.Mutex
	budget int
	used   int
	order  *list.List // most recently read first
	byKey  map[string]*list.Element
}

type memoEntry struct {
	row *Row
	obj runtime.Object
}

func newMemo(budget int) *memo {
	return &memo{budget: budget, order: list.New(), byKey: map[string]*list.Element{}}
}

// lookup returns the object decoded from r, or nil. What it returns is the
// memo's own: copy it, or hand it only to a reader that will not write.
func (m *memo) lookup(r *Row) runtime.Object {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	el, ok := m.byKey[r.k]
	if !ok {
		return nil
	}
	e := el.Value.(*memoEntry)
	if e.row != r {
		return nil
	}
	m.order.MoveToFront(el)
	return e.obj
}

// store remembers obj as r decoded; obj must be the memo's alone from here.
// A row larger than the whole budget is not kept.
func (m *memo) store(r *Row, obj runtime.Object) {
	if m == nil || r.size > m.budget {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if el, ok := m.byKey[r.k]; ok {
		e := el.Value.(*memoEntry)
		m.used += r.size - e.row.size
		e.row, e.obj = r, obj
		m.order.MoveToFront(el)
	} else {
		m.byKey[r.k] = m.order.PushFront(&memoEntry{row: r, obj: obj})
		m.used += r.size
	}
	for m.used > m.budget {
		last := m.order.Back()
		e := last.Value.(*memoEntry)
		m.order.Remove(last)
		delete(m.byKey, e.row.k)
		m.used -= e.row.size
	}
}

// read fills out from r for a Get or a List: a copy of the memo's object
// when it has r's, else a decode, remembered when keep is set. shared is
// the reader's promise not to write (client.UnsafeDisableDeepCopy): out
// then shares the memo's maps and slices, as it would the informer
// cache's.
func (c *codec) read(r *Row, out runtime.Object, shared, keep bool) error {
	if cached := c.memo.lookup(r); cached != nil {
		v := reflect.ValueOf(out)
		if v.Kind() != reflect.Pointer || v.Elem().Type() != c.typ {
			return c.decodeInto(r, out, reasonRead) // answers with the type error
		}
		if !shared {
			cached = cached.DeepCopyObject()
		}
		v.Elem().Set(reflect.ValueOf(cached).Elem())
		return nil
	}
	if err := c.decodeInto(r, out, reasonRead); err != nil {
		return err
	}
	if keep {
		c.memo.store(r, out.DeepCopyObject())
	}
	return nil
}
