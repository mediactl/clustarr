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
	"github.com/mediactl/clustarr/app/squash/task"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/obs/logging"
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

	// PullRetryBackoffCap and PullRetryWindow bound how Serve tolerates a
	// transient pull error -- a JetStream consumer-leader move (409) or a
	// 503 during a node restart, either of which every idle worker's
	// pending pull can see (including the very first one, at startup: R29
	// fix 2). Each retry backs off starting at 1s (clamped to
	// PullRetryBackoffCap when that is smaller), doubling up to
	// PullRetryBackoffCap (default 30s) each time. A failure streak resets
	// -- so PullRetryWindow starts counting fresh -- once a pull has been
	// healthy (Next delivering a task, or a Pull/re-Pull merely acquiring a
	// live puller with nothing yet to deliver) for longer than
	// PullRetryBackoffCap: a re-Pull that only briefly succeeds before the
	// very next Next fails again does not each time look like a fresh
	// problem, but an isolated error after a long healthy idle period does
	// (R29 fix 1 -- it must not inherit an unrelated failure's clock and
	// give up on the spot). Serve gives up and returns the error -- which
	// the binary maps to a process exit, spending one attempt of the pool
	// Job's lifetime backoffLimit -- only once the current streak has
	// lasted PullRetryWindow (default 5m). Zero means the default; a test
	// shrinks both to keep cases fast.
	PullRetryBackoffCap, PullRetryWindow time.Duration

	// Clock overrides time for tests; nil means the real clock.
	Clock clockwork.Clock

	// Process overrides the transcode itself for tests; nil means [Process].
	Process func(context.Context, task.Task, Options) Outcome
}

// defaultPullRetryBackoffCap and defaultPullRetryWindow are
// ServeOptions.PullRetryBackoffCap/PullRetryWindow's zero-value defaults.
// pullRetryBackoffStart is Serve's fixed starting backoff (clamped to
// PullRetryBackoffCap when that is configured smaller, so a test can shrink
// both together and stay fast).
const (
	defaultPullRetryBackoffCap = 30 * time.Second
	defaultPullRetryWindow     = 5 * time.Minute
	pullRetryBackoffStart      = time.Second
)

// pullRetry is Serve's transient-pull-error backoff/give-up state (final
// review I1; R29 fix 1 for the streak-reset defect the re-review reproduced,
// falsified as `next task: nats: leadership change #2`). One pullRetry is
// shared by every pull attempt within one Serve call, so a failure that
// spans a Next call and the re-Pull(s) that follow it accrues against the
// same streak rather than resetting on each individual retry.
//
// The naive fix -- reset failSince on any successful pull, whether that is
// Next delivering a task or merely a Pull/re-Pull recreating a dead
// subscription -- breaks give-up entirely: a subscription that can always
// be recreated but whose every Next then fails at once (a real, reproduced
// shape: JetStream accepts CreateOrUpdateConsumer but Fetch keeps erroring)
// resets the streak every single cycle and never reaches PullRetryWindow
// (TestServeGivesUpOnAPullThatNeverRecovers, added for R29, caught this
// directly -- it hung instead of returning). So ok() only records lastOK,
// the moment of the last success, of either kind; wait decides whether the
// streak in front of it is a continuation or something fresh by asking how
// long ago that was: less than backoffCap ago (one retry cycle) is still
// the same ongoing trouble, so failSince is left alone and keeps
// accumulating toward the window; longer ago -- the long healthy idle gap
// in the reviewer's reproduction, where Next blocked without error for
// minutes with nothing to pull -- starts a fresh streak instead of
// inheriting one that was already resolved.
type pullRetry struct {
	clock              clockwork.Clock
	backoffCap, window time.Duration
	lastOK             time.Time // the last successful Next or Pull/re-Pull
	failSince          time.Time
	backoff            time.Duration
}

func newPullRetry(clock clockwork.Clock, backoffCap, window time.Duration) *pullRetry {
	return &pullRetry{
		clock: clock, backoffCap: backoffCap, window: window,
		lastOK:  clock.Now(),
		backoff: min(pullRetryBackoffStart, backoffCap),
	}
}

// ok records a successful pull (Next delivering a task, or a Pull/re-Pull
// acquiring a live puller). See the type doc for why this alone does not
// end a failure streak already in progress -- wait's gap check decides that.
func (r *pullRetry) ok() {
	r.lastOK = r.clock.Now()
}

// wait waits out one retry's backoff for a transient pull error op names,
// logging it, and reports whether the failure streak has now lasted
// r.window -- in which case the caller gives up. A cancelled ctx during the
// wait returns its own error instead.
func (r *pullRetry) wait(ctx context.Context, op string, cause error) (giveUp bool, cancelled error) {
	now := r.clock.Now()
	if r.failSince.IsZero() || now.Sub(r.lastOK) > r.backoffCap {
		r.failSince = now
		r.backoff = min(pullRetryBackoffStart, r.backoffCap)
	}
	if now.Sub(r.failSince) >= r.window {
		return true, nil
	}
	logging.FromContext(ctx).WarnContext(ctx, "squasharr worker: pull failed; retrying",
		"op", op, "error", cause, "backoff", r.backoff, "failingSince", r.failSince)
	select {
	case <-r.clock.After(r.backoff):
	case <-ctx.Done():
		return false, ctx.Err()
	}
	r.backoff = min(r.backoff*2, r.backoffCap)
	return false, nil
}

// Serve pulls this pool's tasks one at a time and runs each to a settled
// message (spec §9, §18.1). It never returns on its own except for a
// worker-level failure; a cancelled ctx (SIGTERM) returns ctx.Err() once
// in-flight work is drained, and the binary maps that to WorkerExitDrained.
//
// A pull error that is not ctx cancellation -- the initial Pull, p.Next
// failing, or Serve having to recreate p because the pull subscription
// itself died -- is retried with backoff rather than returned at once
// (final review I1): a JetStream consumer-leader move or a 503 during a
// node restart, including one that lands before Serve ever gets its first
// puller (R29 fix 2), must not spend an attempt of the pool Job's lifetime
// backoffLimit and kill every other healthy encode sharing it. Serve gives
// up only once the failure streak lasts o.PullRetryWindow with no
// successful pull in between.
func Serve(ctx context.Context, bus events.Bus, o ServeOptions) error {
	s := &server{o: o.withDefaults()}
	s.clock = s.o.Clock
	ps, ok := bus.(events.PullSubscriber)
	if !ok {
		return fmt.Errorf("squasharr worker: %T cannot pull one message at a time", bus)
	}
	s.bus, s.sub = bus, events.TranscodeTaskConsumer(o.ProfileUID, o.Class).Subscription()

	retry := newPullRetry(s.clock, s.o.PullRetryBackoffCap, s.o.PullRetryWindow)
	p, err := s.pull(ctx, ps, retry)
	if err != nil {
		return err
	}
	defer func() { p.Stop() }()

	for {
		mctx, m, nerr := p.Next(ctx)
		if nerr == nil {
			retry.ok()
			s.handle(mctx, m)
			continue
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if giveUp, cerr := retry.wait(ctx, "next task", nerr); cerr != nil {
			return cerr
		} else if giveUp {
			return fmt.Errorf("squasharr worker: next task: %w", nerr)
		}

		// The failed Next may mean the pull subscription itself is dead
		// (its consumer gone from under it); recreate it so the next
		// iteration has a live one.
		p.Stop()
		p, err = s.pull(ctx, ps, retry)
		if err != nil {
			return err
		}
	}
}

// pull acquires a Puller from ps, retrying a transient error with retry's
// backoff (R29 fix 2: the very first Pull gets exactly the treatment every
// later re-Pull already did, not an immediate return). A success calls
// retry.ok(), ending whatever failure streak was in progress.
func (s *server) pull(ctx context.Context, ps events.PullSubscriber, retry *pullRetry) (events.Puller, error) {
	for {
		p, err := ps.Pull(ctx, s.sub)
		if err == nil {
			retry.ok()
			return p, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if giveUp, cerr := retry.wait(ctx, "pull "+s.sub.Durable, err); cerr != nil {
			return nil, cerr
		} else if giveUp {
			return nil, fmt.Errorf("squasharr worker: pull %s: %w", s.sub.Durable, err)
		}
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
	if o.PullRetryBackoffCap == 0 {
		o.PullRetryBackoffCap = defaultPullRetryBackoffCap
	}
	if o.PullRetryWindow == 0 {
		o.PullRetryWindow = defaultPullRetryWindow
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
	cur, rev0, ok, err := s.claim(ctx, t)
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
	rev := &leaseRev{v: rev0}

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
	renewed := s.renew(work, stop, m, t, rev)
	opts.BeforeSwap = func(bctx context.Context) error {
		return s.reassertBeforeSwap(bctx, stop, t, rev)
	}
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
		_ = s.o.Leases.DeleteRevision(sctx, events.TranscodeLeaseKey(t.Job.UID), rev.get())
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
		_ = s.o.Leases.DeleteRevision(sctx, events.TranscodeLeaseKey(t.Job.UID), rev.get())
		_ = m.Nak(sctx, 0)
		return
	}
	_ = s.o.Leases.DeleteRevision(sctx, events.TranscodeLeaseKey(t.Job.UID), rev.get())
	_ = m.Ack(sctx)
}
