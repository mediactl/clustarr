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
	"errors"
	"fmt"
	"time"

	"github.com/jonboulle/clockwork"

	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/squasharr/task"
)

// ServeOptions configures [Serve].
type ServeOptions struct {
	Options // handed to Process for every task; Options.PodName names this worker

	// ProfileUID, Class and Node identify this pool: ProfileUID and Class
	// pick the durable consumer (events.TranscodeTaskConsumer), and Node is
	// reported on every status event.
	ProfileUID, Class, Node string

	// Leases is the clustarr-transcode-leases bucket.
	Leases events.KV

	// Renew, FenceAfter and HeldRetry default to 20s, 60s and 30s.
	Renew, FenceAfter, HeldRetry time.Duration

	// Clock overrides time for tests; nil means the real clock.
	Clock clockwork.Clock

	// Process overrides the transcode itself for tests; nil means [Process].
	Process func(context.Context, task.Task, Options) Outcome
}

// Serve pulls this pool's tasks one at a time and runs each to a settled
// message (spec §9, §18.1). It never returns on its own except for a
// worker-level failure; a cancelled ctx (SIGTERM) returns ctx.Err() once
// in-flight work is drained, and the binary maps that to WorkerExitDrained.
func Serve(ctx context.Context, bus events.Bus, o ServeOptions) error {
	s := &server{o: o.withDefaults()}
	s.clock = s.o.Clock
	ps, ok := bus.(events.PullSubscriber)
	if !ok {
		return fmt.Errorf("squasharr worker: %T cannot pull one message at a time", bus)
	}
	s.bus, s.sub = bus, events.TranscodeTaskConsumer(o.ProfileUID, o.Class).Subscription()
	p, err := ps.Pull(ctx, s.sub)
	if err != nil {
		return fmt.Errorf("squasharr worker: pull %s: %w", s.sub.Durable, err)
	}
	defer p.Stop()
	for {
		mctx, m, err := p.Next(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("squasharr worker: next task: %w", err)
		}
		s.handle(mctx, m)
	}
}

type server struct {
	o     ServeOptions
	clock clockwork.Clock
	bus   events.Bus
	sub   events.Subscription
}

func (o ServeOptions) withDefaults() ServeOptions {
	if o.Renew == 0 {
		o.Renew = 20 * time.Second
	}
	if o.FenceAfter == 0 {
		o.FenceAfter = 60 * time.Second
	}
	if o.HeldRetry == 0 {
		o.HeldRetry = 30 * time.Second
	}
	if o.Clock == nil {
		o.Clock = clockwork.NewRealClock()
	}
	if o.Process == nil {
		o.Process = Process
	}
	return o
}

func (s *server) handle(ctx context.Context, m events.Message) {
	settle := func() (context.Context, context.CancelFunc) { // survives a drain
		return context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	}
	log := logging.FromContext(ctx)
	var t task.Task
	if err := schema.Decode(m.Envelope().Schema, m.Envelope().Data, &t); err != nil {
		sctx, cancel := settle()
		defer cancel()
		subj, dl := events.DeadLetter(m, s.sub.Durable, "undecodable task: "+err.Error())
		if _, perr := s.bus.Publish(sctx, subj, dl); perr != nil {
			log.WarnContext(ctx, "squasharr worker: dead-letter publish failed", "error", perr)
		}
		_ = m.Term(sctx, "undecodable task")
		return
	}
	cur, rev, ok, err := s.claim(ctx, t)
	switch {
	case err != nil || (!ok && cur.State == task.LeaseHeld):
		sctx, cancel := settle()
		defer cancel()
		_ = m.Nak(sctx, s.o.HeldRetry)
		return
	case !ok: // cancelled for this attempt or a later one
		sctx, cancel := settle()
		defer cancel()
		_ = m.Ack(sctx)
		return
	}

	rep := &reporter{s: s, t: t, delivery: m.Attempt()}
	if err := rep.publish(ctx, task.StatusEvent{Kind: task.EventClaimed}); err != nil {
		log.WarnContext(ctx, "squasharr worker: claimed event not published", "error", err)
	}
	opts := s.o.Options
	opts.OnProgress = func(pctx context.Context, p transcodev1alpha1.Progress) error {
		return rep.publish(pctx, task.StatusEvent{Kind: task.EventProgress, Progress: &p})
	}

	work, stop := context.WithCancelCause(ctx)
	defer stop(nil)
	if d := t.Deadline.Duration; d > 0 {
		timer := s.clock.AfterFunc(d, func() { stop(errDeadline) })
		defer timer.Stop()
	}
	renewed := s.renew(work, stop, m, t, &rev)
	out := s.o.Process(work, t, opts)
	cause := context.Cause(work)
	stop(nil)
	<-renewed

	fin := task.StatusEvent{Kind: task.EventFinished, StderrTail: out.StderrTail}
	if out.Err != nil {
		fin.Message = out.Err.Error()
	}
	switch {
	case errors.Is(cause, errFenced):
		// A fenced worker must not report at all: it no longer knows whether
		// it still holds the lease, so even a Failed report could race a
		// second worker's claim and clobber its status (fix round 1, item 4:
		// this is the one outcome that beats a successful out.Code, since
		// Process may have returned before noticing the fence).
		log.WarnContext(ctx, "squasharr worker: lease could not be renewed; stopping before it lapses",
			"job", t.Job.Key(), "attempt", t.Attempt)
		sctx, cancel := settle()
		defer cancel()
		_ = m.Nak(sctx, 0) // the lease is left to lapse; nothing else is safe
		return
	case out.Code == ExitOK:
		// A success Process returned outranks a cancel or deadline cause
		// that only fired after it was already done (fix round 1, item 4):
		// squasharr is told what actually happened to the file, not that a
		// signal it can no longer act on arrived microseconds later.
		fin.Outcome, fin.Result = task.OutcomeSucceeded, out.Result
	case errors.Is(cause, errCancelled):
		log.InfoContext(ctx, "squasharr worker: task cancelled by squasharr",
			"job", t.Job.Key(), "attempt", t.Attempt)
		fin.Outcome, fin.Reason = task.OutcomeCancelled, task.ReasonCancelled
	case errors.Is(cause, errDeadline):
		fin.Outcome, fin.Reason = task.OutcomeFailed, task.ReasonDeadlineExceeded
	case ctx.Err() != nil && out.Code != ExitOK:
		// Drained: the top-level ctx (not just work) was cancelled while
		// Process was still short of success, whatever it returned --
		// ExitRetriable mid-encode, but just as much a classified failure
		// from a probe or stat call that was interrupted by the same
		// cancellation (fix round 1, item 1: the old ExitRetriable-only
		// guard reported that as Failed and acked it, blocking the job for
		// good instead of letting redelivery retry a healthy task).
		log.InfoContext(ctx, "squasharr worker: drained; releasing the lease for the next pod",
			"job", t.Job.Key(), "attempt", t.Attempt)
		sctx, cancel := settle()
		defer cancel()
		_ = s.o.Leases.DeleteRevision(sctx, events.TranscodeLeaseKey(t.Job.UID), rev)
		_ = m.Nak(sctx, 0)
		return
	default:
		fin.Outcome, fin.Reason = task.OutcomeFailed, reasonFor(out)
	}

	// finished is stored before the task can disappear.
	sctx, cancel := settle()
	defer cancel()
	if err := rep.publish(sctx, fin); err != nil {
		log.WarnContext(ctx, "squasharr worker: finished event not published; redelivery will redo it", "error", err)
		// Release the lease now rather than leave a redelivery waiting out
		// the full lease TTL for it to lapse (fix round 1, item 8).
		_ = s.o.Leases.DeleteRevision(sctx, events.TranscodeLeaseKey(t.Job.UID), rev)
		_ = m.Nak(sctx, 0)
		return
	}
	_ = s.o.Leases.DeleteRevision(sctx, events.TranscodeLeaseKey(t.Job.UID), rev)
	_ = m.Ack(sctx)
}
