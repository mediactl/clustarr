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

package worker

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/squasharr/task"
)

var (
	errCancelled = errors.New("task withdrawn by squasharr")
	errFenced    = errors.New("lease could not be renewed; stopped before it could lapse")
	errDeadline  = errors.New("task deadline exceeded")
)

// claim takes t's lease. It reports the lease it found when it could not.
func (s *server) claim(ctx context.Context, t task.Task) (cur task.Lease, rev uint64, ok bool, err error) {
	key := events.TranscodeLeaseKey(t.Job.UID)
	val, _ := json.Marshal(s.held(t))
	for try := 0; try < 2; try++ {
		rev, err = s.o.Leases.Create(ctx, key, val)
		if err == nil {
			return task.Lease{}, rev, true, nil
		}
		if !errors.Is(err, events.ErrKeyExists) {
			return task.Lease{}, 0, false, err
		}
		e, err := s.o.Leases.Get(ctx, key)
		if errors.Is(err, events.ErrKeyNotFound) {
			continue // lapsed between Create and Get: try again once
		}
		if err != nil {
			return task.Lease{}, 0, false, err
		}
		if err := json.Unmarshal(e.Value, &cur); err != nil {
			return task.Lease{}, 0, false, err
		}
		if cur.State == task.LeaseCancelled && cur.Attempt < t.Attempt {
			rev, err = s.o.Leases.Update(ctx, key, val, e.Revision)
			return cur, rev, err == nil, nil
		}
		return cur, 0, false, nil
	}
	return task.Lease{}, 0, false, events.ErrKeyExists
}

func (s *server) held(t task.Task) task.Lease {
	return task.Lease{
		Job: t.Job, Attempt: t.Attempt, State: task.LeaseHeld,
		Pod: s.o.PodName, Node: s.o.Node, Since: s.clock.Now().UTC(),
	}
}

// renew keeps the lease and the ack window alive until ctx ends. A revision
// it did not write means squasharr cancelled the task, or the lease lapsed
// and someone else took it: stop at once. Plain errors are tolerated until
// FenceAfter since the last good renewal, then the work is stopped: FenceAfter
// is 30s short of the lease TTL, so the work ends before anyone can claim it.
func (s *server) renew(ctx context.Context, stop context.CancelCauseFunc, m events.Message, t task.Task, rev *uint64) <-chan struct{} {
	done := make(chan struct{})
	key := events.TranscodeLeaseKey(t.Job.UID)
	val, _ := json.Marshal(s.held(t))
	// The ticker, and the FenceAfter baseline lastOK, are both captured here
	// in the caller's goroutine, rather than inside the one below: handle
	// calls Process immediately after renew returns, and a fake clock's
	// Advance only wakes a ticker already registered as a waiter and only
	// measures Since against a lastOK already recorded. Capturing either one
	// inside the spawned goroutine raced Process's start (and so a test's
	// Advance) against that goroutine actually getting scheduled -- observed
	// directly: under a fake clock a 20s Advance can complete before the new
	// goroutine runs its first statement, so a lastOK read there is already
	// stale by a full tick.
	tick := s.clock.NewTicker(s.o.Renew)
	lastOK := s.clock.Now()
	go func() {
		defer close(done)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.Chan():
			}
			_ = m.InProgress(ctx)
			next, err := s.o.Leases.Update(ctx, key, val, *rev)
			switch {
			case err == nil:
				*rev, lastOK = next, s.clock.Now()
			case errors.Is(err, events.ErrRevisionMismatch) || errors.Is(err, events.ErrKeyNotFound):
				if e, gerr := s.o.Leases.Get(ctx, key); gerr == nil {
					var cur task.Lease
					if json.Unmarshal(e.Value, &cur) == nil && cur.State == task.LeaseCancelled && cur.Attempt >= t.Attempt {
						stop(errCancelled)
						return
					}
				}
				stop(errFenced)
				return
			default:
				if s.clock.Since(lastOK) >= s.o.FenceAfter {
					stop(errFenced)
					return
				}
			}
		}
	}()
	return done
}
