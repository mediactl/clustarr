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

// Package recyclesweep queues the recycle-bin sweep: one RecycleSweepTask
// per 6-hour UTC slot on importarr-recycle, published by the manager's
// leader (spec 2026-10-06 §3.5.3, OD36). The import agent's
// fileimport.RecycleSweeper handles it. Before this the sweep was an hourly
// timer on every importarr-worker replica, which never fires in a domain
// the HPA has scaled to zero.
package recyclesweep

import (
	"context"
	"fmt"
	"time"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/version"
)

// Interval is the sweep cadence (OD36). Retention is in whole days, so a
// file outlives it by at most Interval, and the import domain wakes four
// times a day for it instead of 24.
const Interval = 6 * time.Hour

// DefaultRetryDelay is how soon a failed publish is tried again.
const DefaultRetryDelay = time.Minute

// Scheduler publishes the sweep task at start and at every slot boundary.
// It is leader-only: a second manager replica would only publish
// duplicates the stream drops.
type Scheduler struct {
	// Bus publishes the task.
	Bus events.Publisher
	// Now is a seam for tests; nil means time.Now.
	Now func() time.Time
	// RetryDelay is the wait after a failed publish; 0 means DefaultRetryDelay.
	RetryDelay time.Duration
	// OnTick is a test hook called after every publish attempt.
	OnTick func(err error)
}

// NeedLeaderElection makes the scheduler a cluster singleton.
func (s *Scheduler) NeedLeaderElection() bool { return true }

// Slot is the 6-hour UTC window t falls in.
func Slot(t time.Time) time.Time { return t.UTC().Truncate(Interval) }

// NextSlot is the start of the window after t's.
func NextSlot(t time.Time) time.Time { return Slot(t).Add(Interval) }

// MsgID is the task's deduplication key: one per slot. The stream's 1 h
// dedupe window is shorter than a slot, so a manager restart later in the
// slot publishes one extra sweep, which SweepOnce makes harmless: it
// removes only what is already past retention.
func MsgID(t time.Time) string { return "recycle-sweep/" + Slot(t).Format(time.RFC3339) }

// Start publishes at once, then at each slot boundary; after a failed
// publish it waits RetryDelay instead. It returns nil when ctx ends.
func (s *Scheduler) Start(ctx context.Context) error {
	log := logging.FromContext(ctx).With("runnable", "recyclesweep")
	for {
		_, err := s.PublishOnce(ctx, s.now())
		if err != nil && ctx.Err() == nil {
			log.Warn("recyclesweep: could not queue the recycle-bin sweep; retrying", "error", err, "retryIn", s.retryDelay())
		}
		if s.OnTick != nil {
			s.OnTick(err)
		}
		wait := s.retryDelay()
		if err == nil {
			now := s.now()
			wait = NextSlot(now).Sub(now)
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

// PublishOnce queues the sweep for the slot at falls in.
func (s *Scheduler) PublishOnce(ctx context.Context, at time.Time) (events.Receipt, error) {
	name, data, err := schema.Encode(schema.RecycleSweepTask{Period: Slot(at)})
	if err != nil {
		return events.Receipt{}, err
	}
	env := &events.Envelope{
		ID:     MsgID(at),
		Type:   "importarr.RecycleSweepTask",
		Schema: name,
		Source: "importarr@" + version.String(),
		Time:   at,
		Data:   data,
	}
	r, err := s.Bus.Publish(ctx, events.SubjectImportRecycleSweep, env)
	if err != nil {
		return events.Receipt{}, fmt.Errorf("recyclesweep: publish %s: %w", events.SubjectImportRecycleSweep, err)
	}
	return r, nil
}

func (s *Scheduler) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Scheduler) retryDelay() time.Duration {
	if s.RetryDelay > 0 {
		return s.RetryDelay
	}
	return DefaultRetryDelay
}
