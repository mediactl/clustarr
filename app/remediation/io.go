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

package remediation

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"sync"
	"time"

	"github.com/mediactl/clustarr/pkg/obs/metrics"
)

// The I/O executor's transient errors.
var (
	ErrIOSaturated   = errors.New("remediation: every /data I/O worker is busy")
	ErrIOBreakerOpen = errors.New("remediation: /data I/O breaker is open")
	ErrIOTimeout     = errors.New("remediation: /data I/O call timed out")
)

const (
	breakerTrips  = 5
	breakerWindow = 30 * time.Second
	breakerOpen   = 60 * time.Second
)

// IOExecutor runs every /data call a Gather makes on a fixed pool with a
// per-call timeout and a mount-health breaker, so a hung NFS mount pins at
// most the pool, never the loop's workers (§3.17).
type IOExecutor struct {
	StatTimeout    time.Duration // default 5 s
	ReadDirTimeout time.Duration // default 20 s

	jobs     chan func()
	workers  int
	mu       sync.Mutex
	busy     int // calls handed to the pool and not yet returned, at most workers
	timeouts []time.Time
	openedAt time.Time
	trial    bool
	clock    func() time.Time
	stat     func(string) (fs.FileInfo, error)
	readDir  func(string) ([]fs.DirEntry, error)
}

// NewIOExecutor starts workers goroutines (--remediation-io-workers). They
// live for the process.
func NewIOExecutor(workers int) *IOExecutor {
	workers = max(workers, 1)
	x := &IOExecutor{
		StatTimeout: 5 * time.Second, ReadDirTimeout: 20 * time.Second,
		jobs: make(chan func(), workers), workers: workers, clock: time.Now, stat: os.Stat, readDir: os.ReadDir,
	}
	for range workers {
		go func() {
			for job := range x.jobs {
				job()
			}
		}()
	}
	return x
}

// Stat is os.Stat on the pool. A missing file is the fs error (a fact); a
// timeout, a busy pool or an open breaker is Transient.
func (x *IOExecutor) Stat(ctx context.Context, path string) (fs.FileInfo, error) {
	var info fs.FileInfo
	err := x.run(ctx, "stat", x.StatTimeout, func() error {
		var err error
		info, err = x.stat(path)
		return err
	})
	return info, err
}

// ReadDir is os.ReadDir on the pool, with Stat's error rules.
func (x *IOExecutor) ReadDir(ctx context.Context, dir string) ([]fs.DirEntry, error) {
	var out []fs.DirEntry
	err := x.run(ctx, "readdir", x.ReadDirTimeout, func() error {
		var err error
		out, err = x.readDir(dir)
		return err
	})
	return out, err
}

// run hands call to an idle worker, or fails at once. A worker is busy from
// the hand-off until its call returns -- a hung call keeps it -- so a pool of
// N admits N calls and the N+1st is saturated; a worker that finished a call
// and has not yet looped back to the queue counts as idle, as does one not
// yet scheduled after NewIOExecutor. The result variables call writes are
// read only after done delivers, so a call that times out and later
// finishes writes into variables nobody reads any more.
func (x *IOExecutor) run(ctx context.Context, op string, timeout time.Duration, call func() error) error {
	if !x.admit() {
		metrics.RemediationIOCallsTotal.WithLabelValues(op, "breaker_open").Inc()
		return Transient(ErrIOBreakerOpen)
	}
	if !x.take() {
		x.abandonTrial()
		metrics.RemediationIOCallsTotal.WithLabelValues(op, "saturated").Inc()
		return Transient(ErrIOSaturated)
	}
	done := make(chan error, 1)
	x.jobs <- func() { // never blocks: at most workers calls are handed off
		defer x.give()
		done <- call()
	}
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case err := <-done:
		x.closeTrial()
		metrics.RemediationIOCallsTotal.WithLabelValues(op, "ok").Inc()
		return err
	case <-t.C:
		x.recordTimeout()
		metrics.RemediationIOCallsTotal.WithLabelValues(op, "timeout").Inc()
		return Transient(ErrIOTimeout)
	case <-ctx.Done():
		x.abandonTrial()
		return Transient(ctx.Err())
	}
}

// take claims a worker for one call; false when every worker is busy.
func (x *IOExecutor) take() bool {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.busy >= x.workers {
		return false
	}
	x.busy++
	return true
}

// give returns a worker take claimed, once its call returned.
func (x *IOExecutor) give() {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.busy--
}

// admit is the breaker: closed, open for breakerOpen after breakerTrips
// timeouts inside breakerWindow, then half-open for one trial call.
func (x *IOExecutor) admit() bool {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.openedAt.IsZero() {
		return true
	}
	if x.clock().Sub(x.openedAt) < breakerOpen || x.trial {
		return false
	}
	x.trial = true
	return true
}

func (x *IOExecutor) recordTimeout() {
	x.mu.Lock()
	defer x.mu.Unlock()
	now := x.clock()
	if x.trial {
		x.openedAt, x.trial = now, false
		return
	}
	kept := x.timeouts[:0]
	for _, at := range x.timeouts {
		if now.Sub(at) < breakerWindow {
			kept = append(kept, at)
		}
	}
	kept = append(kept, now)
	x.timeouts = kept
	if len(x.timeouts) >= breakerTrips {
		x.openedAt, x.timeouts = now, nil
	}
}

// abandonTrial gives a half-open breaker's trial back without a verdict (the
// pool was busy, or the caller left): the breaker stays open, and the next
// call is the trial.
func (x *IOExecutor) abandonTrial() {
	x.mu.Lock()
	defer x.mu.Unlock()
	x.trial = false
}

func (x *IOExecutor) closeTrial() {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.trial {
		x.openedAt, x.trial = time.Time{}, false
	}
}
