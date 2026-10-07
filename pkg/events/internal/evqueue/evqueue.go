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

// Package evqueue is the unbounded watch relay both buses share: a producer
// pushes without ever blocking, and Relay hands the items on, in order, at
// the reader's pace. A watch built on it never drops a change and never
// stalls the writer or the broker subscription that produced it (artwork
// design §B.1 as amended 2026-10-07: nats.go's object watch blocks its
// ordered consumer on a 32-slot channel).
package evqueue

import (
	"context"
	"sync"
)

// Queue is an unbounded FIFO. The zero value is not usable; call New.
type Queue[T any] struct {
	mu     sync.Mutex
	items  []T
	closed bool
	// signal wakes Relay; one slot is enough, since Relay drains everything
	// queued before it waits again.
	signal chan struct{}
}

// New returns an empty, open queue.
func New[T any]() *Queue[T] {
	return &Queue[T]{signal: make(chan struct{}, 1)}
}

// Push appends v. It never blocks; after Close it is a no-op.
func (q *Queue[T]) Push(v T) {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return
	}
	q.items = append(q.items, v)
	q.mu.Unlock()
	q.wake()
}

// Close marks the end of the queue: Relay closes its channel once it has
// handed on everything pushed before.
func (q *Queue[T]) Close() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	q.wake()
}

func (q *Queue[T]) wake() {
	select {
	case q.signal <- struct{}{}:
	default:
	}
}

// Relay sends every item to out, in push order, and closes out once the
// queue is closed and drained, or when ctx ends (dropping what is left).
// Run it in its own goroutine, once per queue.
func (q *Queue[T]) Relay(ctx context.Context, out chan<- T) {
	defer close(out)
	for {
		q.mu.Lock()
		if len(q.items) > 0 {
			v := q.items[0]
			var zero T
			q.items[0] = zero
			q.items = q.items[1:]
			if len(q.items) == 0 {
				q.items = nil // release the drained backing array
			}
			q.mu.Unlock()
			select {
			case out <- v:
			case <-ctx.Done():
				return
			}
			continue
		}
		closed := q.closed
		q.mu.Unlock()
		if closed {
			return
		}
		select {
		case <-q.signal:
		case <-ctx.Done():
			return
		}
	}
}
