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
	"context"
	"sync"
	"time"
)

// A late handler's replay of the store is shared by the handlers that
// register together. The controllers start within seconds of the lease,
// each watch adding its handler, and every handler decoded the whole store
// on its own: on talos (2026-11-03) four handlers replayed every Episode,
// two every MediaFile, six every Movie and eight every Series, 155,000
// decodes at 1,200 a second for two and a half minutes at the manager's CPU
// limit, and the remediation loop's workers started 2 min 46 s after the
// lease. Now the first late handler opens a replay, the handlers that
// register within replayQuiet of the last (replayWindow at most) join it,
// delivery starts once the window closes, and each row is decoded once for
// all of them: every member reads at its own cursor, no more than
// replayLead rows ahead of the slowest, and a row's object is dropped once
// every member has dispatched it, so the shared objects alive are never
// more than the lead. A member that leaves -- its handler removed, the
// informer stopped -- frees the others. A handler that registers after the
// window closed, or after the store changed (the snapshot would be stale
// for it), opens a replay of its own.
const (
	replayQuiet  = 500 * time.Millisecond
	replayWindow = 5 * time.Second
	replayLead   = 128
)

// replay is one shared delivery of the store to its members.
type replay struct {
	inf    *Informer
	rows   []*Row
	synced bool   // the informer had synced when the replay opened
	gen    uint64 // the store's generation when it opened

	mu      sync.Mutex
	notify  chan struct{} // closed and replaced on every change
	started bool
	opened  time.Time
	timer   *time.Timer
	members map[*listener]int // each member's cursor into rows
	cells   []*decoded        // the rows' objects, nil once every member is past
	freed   int               // cells before it are nil
}

// newReplay opens a replay of the store for l; s.mu is held.
func (s *Informer) newReplay(l *listener, synced bool) *replay {
	items := s.indexer.List()
	rows := make([]*Row, 0, len(items))
	for _, item := range items {
		rows = append(rows, item.(*Row))
	}
	r := &replay{
		inf: s, rows: rows, synced: synced, gen: s.gen,
		notify:  make(chan struct{}),
		opened:  time.Now(),
		members: map[*listener]int{l: 0},
		cells:   make([]*decoded, len(rows)),
	}
	r.timer = time.AfterFunc(s.replayQuiet, r.start)
	return r
}

// join adds l to a replay that has not started and whose snapshot is the
// store still (gen); it reports false otherwise.
func (r *replay) join(l *listener, gen uint64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started || gen != r.gen {
		return false
	}
	r.members[l] = 0
	wait := min(r.inf.replayQuiet, time.Until(r.opened.Add(r.inf.replayWindow)))
	if wait <= 0 {
		r.startLocked()
	} else {
		r.timer.Reset(wait)
	}
	return true
}

func (r *replay) start() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.startLocked()
}

func (r *replay) startLocked() {
	if !r.started {
		r.started = true
		r.wakeLocked()
	}
}

func (r *replay) wakeLocked() {
	close(r.notify)
	r.notify = make(chan struct{})
}

type replayStatus uint8

const (
	replayRow     replayStatus = iota // a row to dispatch
	replayDone                        // every row dispatched
	replayStopped                     // the listener or the informer stopped
)

// next is l's next row and its shared object, once the replay has started
// and l is within the lead of the slowest member.
func (r *replay) next(ctx context.Context, l *listener) (*Row, *decoded, replayStatus) {
	for {
		r.mu.Lock()
		pos, member := r.members[l]
		if !member || pos >= len(r.rows) {
			r.mu.Unlock()
			return nil, nil, replayDone
		}
		if r.started && pos-r.minLocked() <= r.inf.replayLead {
			cell := r.cells[pos]
			if cell == nil {
				cell = &decoded{}
				r.cells[pos] = cell
			}
			row := r.rows[pos]
			r.mu.Unlock()
			return row, cell, replayRow
		}
		wake := r.notify
		r.mu.Unlock()
		select {
		case <-wake:
		case <-l.stop:
			return nil, nil, replayStopped
		case <-ctx.Done():
			return nil, nil, replayStopped
		}
	}
}

// advance moves l past the row it dispatched.
func (r *replay) advance(l *listener) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.members[l]; !ok {
		return
	}
	r.members[l]++
	r.freeLocked()
	r.wakeLocked()
}

// leave takes l out: the others no longer wait for it, and what only it
// held is dropped.
func (r *replay) leave(l *listener) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.members[l]; !ok {
		return
	}
	delete(r.members, l)
	if len(r.members) == 0 {
		r.rows, r.cells = nil, nil
		r.freed = 0
	} else {
		r.freeLocked()
	}
	r.wakeLocked()
}

// remaining is how many rows l has yet to dispatch.
func (r *replay) remaining(l *listener) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	pos, ok := r.members[l]
	if !ok {
		return 0
	}
	return len(r.rows) - pos
}

// minLocked is the slowest member's cursor; r.mu is held.
func (r *replay) minLocked() int {
	m := len(r.rows)
	for _, pos := range r.members {
		m = min(m, pos)
	}
	return m
}

// freeLocked drops the objects every member is past; r.mu is held.
func (r *replay) freeLocked() {
	m := r.minLocked()
	for i := r.freed; i < m && i < len(r.cells); i++ {
		r.cells[i] = nil
	}
	r.freed = max(r.freed, m)
}
