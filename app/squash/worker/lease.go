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
	"sync"

	"github.com/mediactl/clustarr/app/squash/task"
	"github.com/mediactl/clustarr/pkg/events"
)

var (
	errCancelled = errors.New("task withdrawn by squasharr")
	errFenced    = errors.New("lease could not be renewed; stopped before it could lapse")
	errDeadline  = errors.New("task deadline exceeded")
)

// claim takes t's lease. It reports the lease it found when it could not:
// ok=false with err=nil covers three outcomes handle tells apart by cur.State
// and t.Attempt --
//   - cur.State==Held and cur.Attempt<=t.Attempt: someone else actively
//     holds an equal-or-older attempt; handle naks with HeldRetry.
//   - cur.Attempt>t.Attempt, in any state: a later attempt already has (or
//     had) this job's lease, so this delivery was superseded before it ever
//     ran; handle acks it (fix round 1, item 6).
//   - a cancel marker names this attempt or a later one: withdrawn; handle
//     acks it.
//
// Replacing an earlier attempt's cancel marker (the fourth case, ok=true) can
// itself race a concurrent writer: if the Update fails with
// ErrRevisionMismatch or ErrKeyNotFound the marker moved or lapsed between
// the Get and the Update, not that this attempt lost -- returning that error
// as ok=false would silently ack a task that is still runnable (fix round 1,
// item 2). Re-read and decide again instead, bounded so a genuinely stuck
// bus still returns.
func (s *server) claim(ctx context.Context, t task.Task) (cur task.Lease, rev uint64, ok bool, err error) {
	key := events.TranscodeLeaseKey(t.Job.UID)
	val, _ := json.Marshal(s.held(t))
	for try := 0; try < 3; try++ {
		rev, err = s.o.Leases.Create(ctx, key, val)
		if err == nil {
			return task.Lease{}, rev, true, nil
		}
		if !errors.Is(err, events.ErrKeyExists) {
			return task.Lease{}, 0, false, err
		}
		e, gerr := s.o.Leases.Get(ctx, key)
		if errors.Is(gerr, events.ErrKeyNotFound) {
			continue // lapsed between Create and Get: try again
		}
		if gerr != nil {
			return task.Lease{}, 0, false, gerr
		}
		cur = task.Lease{}
		if uerr := json.Unmarshal(e.Value, &cur); uerr != nil {
			return task.Lease{}, 0, false, uerr
		}
		switch {
		case cur.Attempt > t.Attempt:
			return task.Lease{}, 0, false, nil
		case cur.State != task.LeaseCancelled || cur.Attempt >= t.Attempt:
			return cur, 0, false, nil
		}
		var next uint64
		var uerr error
		next, uerr = s.o.Leases.Update(ctx, key, val, e.Revision)
		switch {
		case uerr == nil:
			return task.Lease{}, next, true, nil
		case errors.Is(uerr, events.ErrRevisionMismatch) || errors.Is(uerr, events.ErrKeyNotFound):
			continue // the marker moved or lapsed since Get: decide again
		default:
			return task.Lease{}, 0, false, uerr
		}
	}
	return task.Lease{}, 0, false, events.ErrKeyExists
}

func (s *server) held(t task.Task) task.Lease {
	return task.Lease{
		Job: t.Job, Attempt: t.Attempt, State: task.LeaseHeld,
		Pod: s.o.PodName, Node: s.o.Node, Since: s.clock.Now().UTC(),
	}
}

// leaseRev is the current revision handle knows this task's lease is at,
// shared between the renewal goroutine below and Options.BeforeSwap's
// pre-swap reassert (final review I2c): Process runs synchronously on
// handle's own goroutine while renew's ticks fire concurrently on its own,
// so both readers and writers of the revision need this mutex -- the KV
// store itself already serializes the actual Update calls by revision, this
// only protects the local copy from a data race.
type leaseRev struct {
	mu sync.Mutex
	v  uint64
}

func (r *leaseRev) get() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.v
}

func (r *leaseRev) set(v uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.v = v
}

// renew keeps the lease and the ack window alive until ctx ends. A revision
// it did not write means squasharr cancelled the task, or the lease lapsed
// and someone else took it: stop at once. Plain errors are tolerated until
// FenceAfter since the last good renewal, then the work is stopped: FenceAfter
// is 30s short of the lease TTL, so the work ends before anyone can claim it.
func (s *server) renew(ctx context.Context, stop context.CancelCauseFunc, m events.Message, t task.Task, rev *leaseRev) <-chan struct{} {
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
			next, err := s.o.Leases.Update(ctx, key, val, rev.get())
			switch {
			case err == nil:
				rev.set(next)
				lastOK = s.clock.Now()
			case errors.Is(err, events.ErrRevisionMismatch) || errors.Is(err, events.ErrKeyNotFound):
				e, gerr := s.o.Leases.Get(ctx, key)
				if gerr != nil {
					stop(errFenced)
					return
				}
				var cur task.Lease
				if json.Unmarshal(e.Value, &cur) != nil {
					stop(errFenced)
					return
				}
				switch {
				case cur.State == task.LeaseCancelled && cur.Attempt >= t.Attempt:
					stop(errCancelled)
					return
				case cur.State == task.LeaseHeld && cur.Pod == s.o.PodName && cur.Attempt == t.Attempt:
					// A lost Update reply (fix round 1, item 5): our own
					// write landed on the server and this is it -- we just
					// never saw the new revision come back. Adopt it and
					// keep renewing instead of fencing ourselves off a lease
					// we still hold.
					rev.set(e.Revision)
					lastOK = s.clock.Now()
				default:
					stop(errFenced)
					return
				}
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

// errBeforeSwapAborted is what run() gets back from Options.BeforeSwap when
// reassertBeforeSwap could not confirm this attempt still holds the lease;
// the message adds nothing handle doesn't already know from cause
// (context.Cause(work), set by the stop(...) call below before this
// returns), so it stays terse.
var errBeforeSwapAborted = errors.New("squasharr worker: lease could not be reasserted before the swap")

// reassertBeforeSwap is Options.BeforeSwap: immediately before Process
// renames the verified output over the source, it re-asserts this attempt's
// lease with a revision-checked Update instead of trusting the last
// periodic renewal, which can be up to Renew (20s) stale -- long enough for
// squasharr to have withdrawn the attempt, or for another worker to have
// fenced it out, without renew's own tick having noticed yet (final review
// I2c). A failure here is handled exactly like renew's own: a confirmed
// cancel marker stops work with errCancelled; our own write landing but its
// reply getting lost adopts the new revision and lets the swap proceed
// (renew's fix round 1, item 5 case, reached here too); anything else --
// someone else's lease, an unreadable lease, a plain transport error even
// after a Get to disambiguate -- stops work with errFenced, since a fenced
// worker cannot safely report anything either way (serve.go's handle
// already treats it that way for the renewal path). Either stop makes
// context.Cause(work) report the cause before this returns, so handle's
// switch settles the task the same way it would have if renew's own tick
// had caught it first.
func (s *server) reassertBeforeSwap(ctx context.Context, stop context.CancelCauseFunc, t task.Task, rev *leaseRev) error {
	key := events.TranscodeLeaseKey(t.Job.UID)
	val, _ := json.Marshal(s.held(t))
	next, err := s.o.Leases.Update(ctx, key, val, rev.get())
	if err == nil {
		rev.set(next)
		return nil
	}
	e, gerr := s.o.Leases.Get(ctx, key)
	if gerr != nil {
		stop(errFenced)
		return errBeforeSwapAborted
	}
	var cur task.Lease
	if json.Unmarshal(e.Value, &cur) != nil {
		stop(errFenced)
		return errBeforeSwapAborted
	}
	switch {
	case cur.State == task.LeaseCancelled && cur.Attempt >= t.Attempt:
		stop(errCancelled)
	case cur.State == task.LeaseHeld && cur.Pod == s.o.PodName && cur.Attempt == t.Attempt:
		rev.set(e.Revision) // our own write landed; only the reply was lost
		return nil
	default:
		stop(errFenced)
	}
	return errBeforeSwapAborted
}
