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
	"unicode/utf8"

	"github.com/jonboulle/clockwork"

	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/transcode/task"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/records"
	"github.com/mediactl/clustarr/pkg/transcode"
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

	// Records is the clustarr-transcodes bucket: the worker claims each
	// dispatch's record and answers in it by compare-and-swap (loop spec
	// §4.12); it publishes no status event and writes no Kubernetes object.
	Records events.KV

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

	// RemeasureEvery is how often a pod whose device could not be used
	// measures it again before it takes any work (spec §4), and how often a
	// healthy pod republishes its report, which the controller stops
	// counting after task.EncoderLimitsFresh. Zero means two minutes.
	RemeasureEvery time.Duration

	// MeasureTimeout bounds one measurement: a wedged device that never
	// answers is reported unusable rather than left with no report. Zero
	// means three minutes.
	MeasureTimeout time.Duration

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
	logging.FromContext(ctx).WarnContext(ctx, "transcode worker: pull failed; retrying",
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
	if s.o.limits == nil {
		s.o.limits = newLimitsCache(s.o.Telemetry, o.Class, o.Node, o.PodName)
	}
	s.clock = s.o.Clock
	ps, ok := bus.(events.PullSubscriber)
	if !ok {
		return fmt.Errorf("transcode worker: %T cannot pull one message at a time", bus)
	}
	s.bus, s.sub = bus, events.TranscodeTaskConsumer(o.ProfileUID, o.Class).Subscription()
	if o.Records == nil {
		return errors.New("transcode worker: no clustarr-transcodes bucket to answer in")
	}
	s.answers = task.Answers(o.Records, o.PodName)

	if s.o.Engine != nil && s.o.Measurement == nil {
		m, err := s.measureUntilHealthy(ctx)
		if err != nil {
			return err
		}
		s.o.Measurement = &m
		go s.keepReportFresh(ctx, m)
	}

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
			return fmt.Errorf("transcode worker: next task: %w", nerr)
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

// measureUntilHealthy measures this pod's device for its class and
// publishes the result with its health (spec §4: "a pod whose class's
// device will not open reports so and takes no jobs"), measuring again
// every RemeasureEvery until the device can be used; only then does Serve
// pull. It returns only the healthy measurement, or ctx's error.
func (s *server) measureUntilHealthy(ctx context.Context) (transcode.Measurement, error) {
	log := logging.FromContext(ctx)
	class := transcode.Hardware(s.o.Class)
	for {
		mctx, cancel := context.WithTimeout(ctx, s.o.MeasureTimeout)
		m, err := s.o.Engine.Measure(mctx, class)
		cancel()
		if ctx.Err() != nil {
			return transcode.Measurement{}, ctx.Err()
		}
		if err != nil && errors.Is(mctx.Err(), context.DeadlineExceeded) {
			err = fmt.Errorf("%w: measuring the %s device took longer than %s", transcode.ErrDeviceUnavailable, class, s.o.MeasureTimeout)
		}
		s.o.limits.publishHealth(ctx, m, err)
		if err == nil {
			log.InfoContext(ctx, "transcode worker: measured the device", "class", class, "tier", m.Tier)
			return m, nil
		}
		log.WarnContext(ctx, "transcode worker: the device cannot be used; taking no work until it can",
			"class", class, "error", err, "retryIn", s.o.RemeasureEvery)
		select {
		case <-ctx.Done():
			return transcode.Measurement{}, ctx.Err()
		case <-s.clock.After(s.o.RemeasureEvery):
		}
	}
}

// keepReportFresh republishes a healthy pod's measurement every
// RemeasureEvery until ctx ends: the controller counts a report for
// task.EncoderLimitsFresh only, and a healthy pod that went quiet would
// leave an unhealthy node's fresh reports to speak for the whole class.
func (s *server) keepReportFresh(ctx context.Context, m transcode.Measurement) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.clock.After(s.o.RemeasureEvery):
			s.o.limits.publishHealth(ctx, m, nil)
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
			return nil, fmt.Errorf("transcode worker: pull %s: %w", s.sub.Durable, err)
		}
	}
}

type server struct {
	o       ServeOptions
	clock   clockwork.Clock
	bus     events.Bus
	sub     events.Subscription
	answers *records.Answerer[*task.Record]
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
	if o.RemeasureEvery == 0 {
		o.RemeasureEvery = 2 * time.Minute
	}
	if o.MeasureTimeout == 0 {
		o.MeasureTimeout = 3 * time.Minute
	}
	if o.Clock == nil {
		o.Clock = clockwork.NewRealClock()
	}
	if o.Process == nil {
		o.Process = Process
	}
	return o
}

// handle runs one delivery of one dispatch (loop spec §4.12): a v1 task is
// acked (the loop requests the file again as v2); a superseded one -- a
// record at a higher Seq, or answered or withdrawn at its own -- is acked
// unrun; then the lease, then the record's claim (an absent or older record
// naks and waits for the loop's owed write, §4.8), the work, the answer by
// compare-and-swap, the lease's release and the ack. It publishes no status
// event and writes no Kubernetes object.
func (s *server) handle(ctx context.Context, m events.Message) {
	settle := func() (context.Context, context.CancelFunc) { // survives a drain
		return context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	}
	log := logging.FromContext(ctx)
	if m.Envelope().Schema == (task.TaskV1{}).Schema() {
		log.InfoContext(ctx, "transcode worker: a v1 task; the loop requests this file again as v2")
		sctx, cancel := settle()
		defer cancel()
		_ = m.Ack(sctx)
		return
	}
	var t task.Task
	if err := schema.Decode(m.Envelope().Schema, m.Envelope().Data, &t); err != nil {
		sctx, cancel := settle()
		defer cancel()
		subj, dl := events.DeadLetter(m, s.sub.Durable, "undecodable task: "+err.Error())
		if _, perr := s.bus.Publish(sctx, subj, dl); perr != nil {
			log.WarnContext(ctx, "transcode worker: dead-letter publish failed", "error", perr)
		}
		_ = m.Term(sctx, "undecodable task")
		return
	}
	ctx = logging.With(ctx, "mediaFile", t.File.Key(), "seq", t.Seq)
	log = logging.FromContext(ctx)
	key := events.RecordKey(t.File.UID)
	if out, ok := FactAlready(ctx, t, s.o.Options); ok {
		// This dispatch's swap already happened: answer the fact, even over
		// a withdrawal, before any other check (§4.12).
		log.InfoContext(ctx, "transcode worker: the swap already happened; answering it")
		started := s.clock.Now().UTC()
		ans, _ := s.answerOf(ctx, t, out, nil, started)
		s.settleFact(ctx, m, t, key, ans, nil)
		return
	}
	if sup, err := s.answers.Superseded(ctx, key, t.Seq); err != nil || sup {
		sctx, cancel := settle()
		defer cancel()
		if err != nil {
			log.WarnContext(ctx, "transcode worker: cannot read the record; retrying", "error", err)
			_ = m.Nak(sctx, s.o.HeldRetry)
			return
		}
		_ = m.Ack(sctx)
		return
	}
	cur, rev0, ok, err := s.claim(ctx, t)
	switch {
	case err != nil || (!ok && cur.State == task.LeaseHeld):
		sctx, cancel := settle()
		defer cancel()
		_ = m.Nak(sctx, s.o.HeldRetry)
		return
	case !ok: // cancelled for this dispatch or a later one
		sctx, cancel := settle()
		defer cancel()
		_ = m.Ack(sctx)
		return
	}
	rev := &leaseRev{v: rev0}
	release := func(sctx context.Context) {
		_ = s.o.Leases.DeleteRevision(sctx, events.TranscodeLeaseKey(t.File.UID), rev.get())
	}
	started := s.clock.Now().UTC()
	// A redelivery may reclaim a record claimed at its Seq: s.claim just
	// took the lease, so no worker holds it live.
	verdict, err := s.answers.Claim(ctx, key, t.Seq, m.Attempt() > 1)
	switch {
	case err != nil:
		sctx, cancel := settle()
		defer cancel()
		release(sctx)
		_ = m.Nak(sctx, s.o.HeldRetry)
		return
	case verdict == records.NotYet: // absent or below: wait for the loop's owed write (§4.8)
		sctx, cancel := settle()
		defer cancel()
		release(sctx)
		if m.Attempt() >= uint64(max(s.sub.MaxDeliver, 1)) { //nolint:gosec // MaxDeliver is small and positive
			_ = m.Ack(sctx) // the last delivery acks; the loop republishes
			return
		}
		_ = m.Nak(sctx, records.NotYetDelay)
		return
	case verdict != records.Wrote: // withdrawn, answered or superseded at this seq
		sctx, cancel := settle()
		defer cancel()
		release(sctx)
		_ = m.Ack(sctx)
		return
	}

	opts := s.o.Options
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

	sctx, cancel := settle()
	defer cancel()
	ans, answer := s.answerOf(ctx, t, out, cause, started)
	if !answer { // fenced, cancelled or drained: no answer
		switch {
		case errors.Is(cause, errFenced):
			// A fenced worker must not answer at all: it no longer knows
			// whether it still holds the lease. The lease is left to lapse.
			_ = m.Nak(sctx, 0)
		case errors.Is(cause, errCancelled):
			release(sctx)
			_ = m.Ack(sctx) // withdrawn: the loop already closed the dispatch
		default: // drained: the next pod takes it
			release(sctx)
			_ = m.Nak(sctx, 0)
		}
		return
	}
	rec := &task.Record{
		RecordHeader: schema.RecordHeader{Schema: task.RecordSchema, MediaFile: t.File, Seq: t.Seq, State: records.StateAnswered, AnsweredAt: ans.FinishedAt},
		Inputs:       t.Inputs, Answer: &ans,
	}
	if ans.Reason == task.ReasonRetriable && m.Attempt() >= uint64(max(s.sub.MaxDeliver, 1)) { //nolint:gosec // as above
		// The last delivery writes failed with the cause rather than leave
		// it to the dead-letter subject (§4.8 "Last delivery").
		rec.State, rec.Failure, rec.Transient = records.StateFailed, clamp(ans.Message, records.MaxFailure), true
	}
	if ans.Outcome == task.OutcomeSucceeded {
		s.settleFact(ctx, m, t, key, ans, release)
		return
	}
	if _, err := s.answers.Answer(sctx, key, rec); err != nil {
		log.WarnContext(ctx, "transcode worker: the answer was not recorded; redelivery redoes it", "error", err)
		release(sctx)
		_ = m.Nak(sctx, 0)
		return
	}
	release(sctx)
	_ = m.Ack(sctx)
}

// answerOf is the answer to t from Process's outcome, or false for a run
// that answers nothing: fenced (it may no longer hold the lease), cancelled
// by a withdrawal, or drained short of success. A success Process returned
// outranks a cancel or deadline cause that fired only after it was done.
func (s *server) answerOf(ctx context.Context, t task.Task, out Outcome, cause error, started time.Time) (task.Answer, bool) {
	log := logging.FromContext(ctx)
	finished := s.clock.Now().UTC()
	a := task.Answer{
		Class: string(taskClass(t)), Pod: s.o.PodName, Node: s.o.Node, StartedAt: &started, FinishedAt: &finished,
		StderrTail: tail(out.StderrTail, task.MaxAnswerStderr), Graft: out.Graft,
	}
	if out.Err != nil {
		a.Message = clamp(out.Err.Error(), task.MaxAnswerMessage)
	}
	switch {
	case errors.Is(cause, errFenced):
		log.WarnContext(ctx, "transcode worker: lease could not be renewed; stopping before it lapses")
		return a, false
	case out.Code == ExitOK:
		a.Outcome = task.OutcomeSucceeded
		if out.Result != nil {
			res := *out.Result
			res.MediaInfo = nil // the loop's probe of the output is the record of it
			a.Result = &res
		}
	case errors.Is(cause, errCancelled):
		log.InfoContext(ctx, "transcode worker: dispatch withdrawn by the loop")
		return a, false
	case errors.Is(cause, errDeadline):
		a.Outcome, a.Reason = task.OutcomeFailed, task.ReasonDeadlineExceeded
	case ctx.Err() != nil:
		log.InfoContext(ctx, "transcode worker: drained; releasing the lease for the next pod")
		return a, false
	default:
		a.Outcome, a.Reason = task.OutcomeFailed, reasonFor(out)
	}
	return a, true
}

// taskClass is the class t was dispatched to: BuildTask sets both Class and
// Profile.Hardware to it.
func taskClass(t task.Task) transcodev1alpha1.Hardware {
	if t.Class != "" {
		return transcodev1alpha1.Hardware(t.Class)
	}
	if t.Profile.Hardware != nil {
		return *t.Profile.Hardware
	}
	return ""
}

// reasonFor names a Process outcome: Process's own reason when it has one
// (SourceChanged, GPUUnavailable, GPUEncodeFailed), else the code's.
func reasonFor(out Outcome) task.Reason {
	if out.Reason != "" {
		return out.Reason
	}
	switch out.Code {
	case ExitInvalidSource:
		return task.ReasonInvalidSource
	case ExitVerifyFailed:
		return task.ReasonVerifyFailed
	default:
		return task.ReasonRetriable
	}
}

// clamp cuts s to at most n bytes on a rune boundary.
func clamp(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// tail keeps the last n bytes of s, on a rune boundary: the end of an
// encoder's stderr is where its error is.
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	i := len(s) - n
	for i < len(s) && !utf8.RuneStart(s[i]) {
		i++
	}
	return s[i:]
}

// factAnswerTimeout is how long a fact's answer is retried before the task
// is nak'd (§4.12): a withdrawal has usually purged the subject by then, so
// a redelivery cannot be relied on to carry the fact.
const factAnswerTimeout = 10 * time.Minute

// settleFact answers a fact -- a succeeded dispatch, whose file already
// changed on disk -- retrying a failed Answer with backoff (1 s doubling to
// 30 s) for factAnswerTimeout, whatever the lease now says, then releases
// the lease (release, when given) and acks; it naks only once the timeout
// passes.
func (s *server) settleFact(ctx context.Context, m events.Message, t task.Task, key string, ans task.Answer, release func(context.Context)) {
	log := logging.FromContext(ctx)
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), factAnswerTimeout+10*time.Second)
	defer cancel()
	rec := &task.Record{
		RecordHeader: schema.RecordHeader{Schema: task.RecordSchema, MediaFile: t.File, Seq: t.Seq, State: records.StateAnswered, AnsweredAt: ans.FinishedAt},
		Inputs:       t.Inputs, Answer: &ans,
	}
	deadline := s.clock.Now().Add(factAnswerTimeout)
	backoff := time.Second
	for {
		v, err := s.answers.Answer(fctx, key, rec)
		if err == nil {
			if v == records.DroppedFact {
				log.WarnContext(ctx, "transcode worker: a newer dispatch owns the record; the loop recovers this swap from disk",
					"outputPath", t.Inputs.OutputPath)
			}
			if release != nil {
				release(fctx)
			}
			_ = m.Ack(fctx)
			return
		}
		if !s.clock.Now().Before(deadline) {
			log.WarnContext(ctx, "transcode worker: the fact's answer was not recorded; nak'd", "error", err)
			if release != nil {
				release(fctx)
			}
			_ = m.Nak(fctx, 0)
			return
		}
		log.WarnContext(ctx, "transcode worker: the fact's answer was not recorded; retrying", "error", err, "backoff", backoff)
		select {
		case <-fctx.Done():
			_ = m.Nak(context.WithoutCancel(ctx), 0)
			return
		case <-s.clock.After(backoff):
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}
