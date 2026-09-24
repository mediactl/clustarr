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
	"sync/atomic"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/squash/task"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// settleKind is which WorkQueue method Serve's task-consumer message saw.
type settleKind string

const (
	settleAck  settleKind = "ack"
	settleNak  settleKind = "nak"
	settleTerm settleKind = "term"
)

// settlement is one recorded Ack/Nak/Term call, captured by trackingBus so
// tests can assert what Serve told the queue to do, not only what it
// published (fix round 1, item 3).
type settlement struct {
	kind   settleKind
	delay  time.Duration
	reason string
}

// trackingBus wraps a membus.Bus so every settlement its task consumer's
// Puller sees is recorded. It embeds the concrete *membus.Bus (not the
// events.Bus interface) so every method that isn't overridden -- including
// Pull, part of events.PullSubscriber, not events.Bus -- promotes straight
// through.
type trackingBus struct {
	*membus.Bus
	settled chan settlement
}

func wrapForSettlementTracking(mb *membus.Bus) *trackingBus {
	return &trackingBus{Bus: mb, settled: make(chan settlement, 32)}
}

var _ events.Bus = (*trackingBus)(nil)

var _ events.PullSubscriber = (*trackingBus)(nil)

func (b *trackingBus) Pull(ctx context.Context, s events.Subscription) (events.Puller, error) {
	p, err := b.Bus.Pull(ctx, s)
	if err != nil {
		return nil, err
	}
	return &trackingPuller{Puller: p, bus: b}, nil
}

func (b *trackingBus) record(s settlement) {
	select {
	case b.settled <- s:
	default: // a test that doesn't drain settlements must never block Serve
	}
}

type trackingPuller struct {
	events.Puller
	bus *trackingBus
}

func (p *trackingPuller) Next(ctx context.Context) (context.Context, events.Message, error) {
	mctx, m, err := p.Puller.Next(ctx)
	if err != nil {
		return mctx, m, err
	}
	return mctx, &trackingMessage{Message: m, bus: p.bus}, nil
}

type trackingMessage struct {
	events.Message
	bus *trackingBus
}

func (m *trackingMessage) Ack(ctx context.Context) error {
	m.bus.record(settlement{kind: settleAck})
	return m.Message.Ack(ctx)
}

func (m *trackingMessage) Nak(ctx context.Context, delay time.Duration) error {
	m.bus.record(settlement{kind: settleNak, delay: delay})
	return m.Message.Nak(ctx, delay)
}

func (m *trackingMessage) Term(ctx context.Context, reason string) error {
	m.bus.record(settlement{kind: settleTerm, reason: reason})
	return m.Message.Term(ctx, reason)
}

type harness struct {
	t         *testing.T
	ctx       context.Context
	cancel    context.CancelFunc
	clock     *clockwork.FakeClock
	bus       events.Bus
	tracking  *trackingBus
	leases    events.KV
	calls     atomic.Int32
	release   chan Outcome // unbuffered: a send means the stub took it
	started   chan struct{}
	ended     chan struct{}
	events    chan task.StatusEvent
	decodeErr chan error

	// cancelOutcome is what the stub Process returns when its ctx is done
	// instead of receiving on release. Tests that need Serve's drain path to
	// see a specific, non-ExitRetriable classification (fix round 1, item 1)
	// override it before calling serve.
	cancelOutcome Outcome

	// processOverride replaces the default stub Process entirely when set.
	// It exists for a test whose Process must keep waiting on release even
	// after its ctx is done -- the default stub's select always prefers a
	// closed ctx.Done() the instant it fires, so it can never model "the
	// encode had already finished; Process reports success regardless of a
	// deadline or drain that arrived after" (fix round 1, item 4).
	processOverride func(context.Context, task.Task, Options) Outcome

	doneOnce sync.Once
	doneCh   chan struct{}
	doneErr  error
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{
		t: t, clock: clockwork.NewFakeClock(), release: make(chan Outcome),
		started: make(chan struct{}, 8), ended: make(chan struct{}, 8),
		events: make(chan task.StatusEvent, 64), decodeErr: make(chan error, 1),
		cancelOutcome: Outcome{Code: ExitRetriable},
		doneCh:        make(chan struct{}),
	}
	h.ctx, h.cancel = context.WithCancel(context.Background())
	mb := membus.New(h.clock)
	h.tracking = wrapForSettlementTracking(mb)
	h.bus = h.tracking
	require.NoError(t, h.bus.Ensure(h.ctx, events.Default().ForSingleNode()))
	h.leases = h.bus.KV(events.BucketTranscodeLeases)
	rc, ok := events.Default().Consumer(events.ConsumerSquasharrResults)
	require.True(t, ok)
	// item 9: this handler runs on membus's own goroutine, not the test's,
	// so a decode failure is reported through a channel instead of
	// require/assert.FailNow, which is unsafe to call off the test
	// goroutine.
	stop, err := h.bus.Subscribe(context.Background(), rc.Subscription(), func(_ context.Context, m events.Message) error {
		var ev task.StatusEvent
		if derr := schema.Decode(m.Envelope().Schema, m.Envelope().Data, &ev); derr != nil {
			select {
			case h.decodeErr <- derr:
			default:
			}
			return derr
		}
		h.events <- ev
		return nil
	})
	require.NoError(t, err)

	// Cleanups run LIFO. Registered in this order, they execute: join (cancel
	// ctx, wait for Serve to actually return -- item 9) first, then stop the
	// clock pump, then stop the results subscription, then check for a
	// decode failure nothing already asserted on.
	t.Cleanup(func() {
		select {
		case e := <-h.decodeErr:
			t.Errorf("a status event failed to decode: %v", e)
		default:
		}
	})
	t.Cleanup(stop)
	t.Cleanup(pumpClock(t, h.clock, 2*time.Millisecond))
	t.Cleanup(func() {
		h.cancel()
		select {
		case <-h.doneCh:
		case <-time.After(5 * time.Second):
			t.Log("Serve did not exit within 5s of test cleanup cancelling its context")
		}
	})
	return h
}

// pumpClock advances the fake clock in small steps from another goroutine,
// matching membus's own poll interval, so its poll-based delivery
// (Subscribe's delivery loop and Pull's Next, both of which wait on
// clock.After) keeps making progress for the lifetime of the harness --
// membus.New's own doc comment says a fake clock must be driven this way.
// The steps are tiny next to the thresholds these tests drive by hand
// (20s renewal, 60s FenceAfter, 60s task deadlines), so the drift they add
// over a test's few hundred milliseconds of real time is negligible; the
// same pattern is established in
// app/catalog/worker/grab/decide_envtest_test.go's pumpClock.
func pumpClock(t *testing.T, clock *clockwork.FakeClock, step time.Duration) func() {
	t.Helper()
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		for {
			select {
			case <-done:
				return
			default:
				clock.Advance(step)
				time.Sleep(time.Millisecond)
			}
		}
	}()
	return func() {
		close(done)
		<-stopped
	}
}

func (h *harness) serve() {
	h.serveOnBus(h.bus)
}

func (h *harness) serveOnBus(bus events.Bus) {
	h.serveOnBusWithOptions(bus, nil)
}

// serveOnBusWithOptions is serveOnBus, with mutate given a chance to
// override the ServeOptions Serve runs with -- I1's tests use it to shrink
// PullRetryBackoffCap/PullRetryWindow so a retry-and-give-up case stays
// fast under the fake clock.
func (h *harness) serveOnBusWithOptions(bus events.Bus, mutate func(*ServeOptions)) {
	process := h.processOverride
	if process == nil {
		process = func(ctx context.Context, _ task.Task, o Options) Outcome {
			h.calls.Add(1)
			h.started <- struct{}{}
			defer func() { h.ended <- struct{}{} }()
			if o.OnProgress != nil {
				_ = o.OnProgress(ctx, transcodev1alpha1.Progress{Percent: 40})
			}
			select {
			case out := <-h.release:
				return out
			case <-ctx.Done():
				out := h.cancelOutcome
				if out.Err == nil {
					out.Err = ctx.Err()
				}
				return out
			}
		}
	}
	o := ServeOptions{
		ProfileUID: "puid", Class: "cpu", Node: "n1",
		Options: Options{PodName: "pool-abc"},
		Leases:  h.leases, Clock: h.clock,
		Process: process,
	}
	if mutate != nil {
		mutate(&o)
	}
	go func() {
		err := Serve(h.ctx, bus, o)
		h.doneOnce.Do(func() {
			h.doneErr = err
			close(h.doneCh)
		})
	}()
}

// waitDone blocks for Serve's return value, however many times it -- or the
// test's own cleanup -- is called: doneCh is closed exactly once, so every
// caller after the first still gets the answer instead of blocking.
func (h *harness) waitDone(within time.Duration) error {
	h.t.Helper()
	select {
	case <-h.doneCh:
		return h.doneErr
	case <-time.After(within):
		h.t.Fatal("Serve did not return in time")
		return nil
	}
}

func (h *harness) publish(attempt int32, deadline time.Duration) {
	h.t.Helper()
	tk := task.Task{Job: schema.Ref{Namespace: "media", Name: "tj", UID: "juid"}, Attempt: attempt, Class: "cpu"}
	tk.Deadline.Duration = deadline
	sch, data, err := schema.Encode(tk)
	require.NoError(h.t, err)
	id := events.MsgIDForTranscodeTask("juid", attempt)
	_, err = h.bus.Publish(h.ctx, events.WorkTranscodeTaskSubject("puid", "cpu", "juid"),
		&events.Envelope{ID: id, Schema: sch, Data: data}, events.WithMsgID(id))
	require.NoError(h.t, err)
}

// next waits for the next event of kind, skipping others.
func (h *harness) next(kind task.EventKind) task.StatusEvent {
	h.t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-h.events:
			if ev.Kind == kind {
				return ev
			}
		case <-deadline:
			h.t.Fatalf("no %s event within 5s", kind)
		}
	}
}

func (h *harness) noEvent(kind task.EventKind, within time.Duration) {
	h.t.Helper()
	deadline := time.After(within)
	for {
		select {
		case ev := <-h.events:
			if ev.Kind == kind {
				h.t.Fatalf("unexpected %s event: %+v", kind, ev)
			}
		case <-deadline:
			return
		}
	}
}

// nextSettlement waits for the next Ack/Nak/Term the task consumer's message
// saw.
func (h *harness) nextSettlement() settlement {
	h.t.Helper()
	select {
	case s := <-h.tracking.settled:
		return s
	case <-time.After(5 * time.Second):
		h.t.Fatal("no settlement within 5s")
		return settlement{}
	}
}

func (h *harness) putLease(l task.Lease) {
	h.t.Helper()
	b, _ := json.Marshal(l)
	_, err := h.leases.Put(h.ctx, events.TranscodeLeaseKey("juid"), b)
	require.NoError(h.t, err)
}

func (h *harness) leaseGone() bool {
	_, err := h.leases.Get(context.Background(), events.TranscodeLeaseKey("juid"))
	return err != nil
}

func TestServeReportsClaimedProgressFinishedAndAcks(t *testing.T) {
	h := newHarness(t)
	h.serve()
	h.publish(1, 0)
	claimed := h.next(task.EventClaimed)
	assert.Equal(t, uint64(1), claimed.Seq)
	assert.Equal(t, "pool-abc", claimed.Pod)
	assert.Equal(t, "n1", claimed.Node)
	assert.Equal(t, int32(1), claimed.Attempt)
	assert.Equal(t, transcodev1alpha1.HardwareCPU, claimed.Class, "every event names the class the task was dispatched to")
	assert.False(t, h.leaseGone(), "the lease is held while Process runs")
	progress := h.next(task.EventProgress)
	assert.Equal(t, uint64(2), progress.Seq)
	assert.Equal(t, int32(40), progress.Progress.Percent)
	assert.Equal(t, transcodev1alpha1.HardwareCPU, progress.Class)

	h.release <- Outcome{Code: ExitOK, Result: &transcodev1alpha1.Result{OutputPath: "/data/x.mkv"}}
	fin := h.next(task.EventFinished)
	assert.Equal(t, task.OutcomeSucceeded, fin.Outcome)
	assert.Equal(t, "/data/x.mkv", fin.Result.OutputPath)
	assert.Equal(t, uint64(3), fin.Seq)
	assert.Equal(t, transcodev1alpha1.HardwareCPU, fin.Class)
	assert.Equal(t, settleAck, h.nextSettlement().kind)
	require.Eventually(t, h.leaseGone, 5*time.Second, 10*time.Millisecond, "the lease is released after finished")
	h.clock.Advance(2 * time.Minute) // past AckWait: an acked task never returns
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, int32(1), h.calls.Load())
}

// The queue retries nothing: every outcome is reported and acked, and
// squasharr decides what happens next (spec §18.3).
func TestServeReportsEveryOutcomeAndAcks(t *testing.T) {
	for _, tc := range []struct {
		out    Outcome
		reason task.Reason
	}{
		{Outcome{Code: ExitRetriable}, task.ReasonRetriable},
		{Outcome{Code: ExitInvalidSource}, task.ReasonInvalidSource},
		{Outcome{Code: ExitVerifyFailed}, task.ReasonVerifyFailed},
		{Outcome{Code: ExitInvalidSource, Reason: task.ReasonSourceChanged}, task.ReasonSourceChanged},
		{Outcome{Code: ExitRetriable, Reason: task.ReasonGPUUnavailable}, task.ReasonGPUUnavailable},
		{Outcome{Code: ExitRetriable, Reason: task.ReasonGPUEncodeFailed, StderrTail: "nvenc: no device"}, task.ReasonGPUEncodeFailed},
	} {
		t.Run(string(tc.reason), func(t *testing.T) {
			h := newHarness(t)
			h.serve()
			h.publish(1, 0)
			<-h.started
			h.release <- tc.out
			fin := h.next(task.EventFinished)
			assert.Equal(t, task.OutcomeFailed, fin.Outcome)
			assert.Equal(t, tc.reason, fin.Reason)
			assert.Equal(t, tc.out.StderrTail, fin.StderrTail)
			assert.Equal(t, settleAck, h.nextSettlement().kind)
			h.clock.Advance(time.Hour)
			time.Sleep(100 * time.Millisecond)
			assert.Equal(t, int32(1), h.calls.Load(), "a reported task is acked, never redelivered")
		})
	}
}

func TestServeNaksATaskAnotherWorkerHolds(t *testing.T) {
	h := newHarness(t)
	h.putLease(task.Lease{Job: schema.Ref{UID: "juid"}, Attempt: 1, State: task.LeaseHeld, Pod: "other"})
	h.serve()
	h.publish(1, 0)
	h.noEvent(task.EventClaimed, 300*time.Millisecond)
	assert.Zero(t, h.calls.Load(), "Process must not run while another worker's lease lives")
	s := h.nextSettlement()
	assert.Equal(t, settleNak, s.kind)
	assert.Equal(t, 30*time.Second, s.delay, "the default HeldRetry")
}

// Review Focus 3.
func TestServeReplacesACancelledLeaseFromAnEarlierAttempt(t *testing.T) {
	h := newHarness(t)
	h.putLease(task.Lease{Job: schema.Ref{UID: "juid"}, Attempt: 1, State: task.LeaseCancelled})
	h.serve()
	h.publish(2, 0)
	assert.Equal(t, int32(2), h.next(task.EventClaimed).Attempt, "attempt 2 was dropped by attempt 1's cancel marker")
	<-h.started
	h.release <- Outcome{Code: ExitOK, Result: &transcodev1alpha1.Result{}}
	assert.Equal(t, int32(2), h.next(task.EventFinished).Attempt)
	assert.Equal(t, settleAck, h.nextSettlement().kind)
}

func TestServeDropsATaskCancelledForItsOwnAttempt(t *testing.T) {
	h := newHarness(t)
	h.putLease(task.Lease{Job: schema.Ref{UID: "juid"}, Attempt: 2, State: task.LeaseCancelled})
	h.serve()
	h.publish(2, 0)
	h.noEvent(task.EventClaimed, 300*time.Millisecond)
	assert.Zero(t, h.calls.Load())
	assert.Equal(t, settleAck, h.nextSettlement().kind)
}

// Fix round 1, item 6: a lease already held (or cancelled) for a later
// attempt than the one just delivered means this delivery is stale --
// squasharr moved on without waiting for it -- so it settles as done, not as
// something to keep retrying.
func TestServeAcksATaskSupersededByALaterAttempt(t *testing.T) {
	h := newHarness(t)
	h.putLease(task.Lease{Job: schema.Ref{UID: "juid"}, Attempt: 2, State: task.LeaseHeld, Pod: "other"})
	h.serve()
	h.publish(1, 0) // an older, now-stale attempt
	h.noEvent(task.EventClaimed, 300*time.Millisecond)
	assert.Zero(t, h.calls.Load(), "a superseded attempt must never run")
	assert.Equal(t, settleAck, h.nextSettlement().kind)
}

func TestServeCancelsRunningWorkWhenTheLeaseIsCancelled(t *testing.T) {
	h := newHarness(t)
	h.serve()
	h.publish(1, 0)
	<-h.started
	h.putLease(task.Lease{Job: schema.Ref{UID: "juid"}, Attempt: 1, State: task.LeaseCancelled})
	h.clock.Advance(20 * time.Second) // the next renewal sees the revision change
	fin := h.next(task.EventFinished)
	assert.Equal(t, task.OutcomeCancelled, fin.Outcome)
	assert.Equal(t, task.ReasonCancelled, fin.Reason)
	assert.Equal(t, settleAck, h.nextSettlement().kind)
}

func TestServeSelfFencesWhenRenewalsFail(t *testing.T) {
	h := newHarness(t)
	failing := &failingUpdates{KV: h.leases}
	h.leases = failing
	h.serve()
	h.publish(1, 0)
	<-h.started
	failing.fail.Store(true)
	for i := 0; i < 3; i++ { // three missed renewals: 60s
		h.clock.Advance(20 * time.Second)
		time.Sleep(20 * time.Millisecond)
	}
	select {
	case <-h.ended: // Process saw its context cancelled: the encode was stopped
	case <-time.After(5 * time.Second):
		t.Fatal("a worker that cannot renew its lease kept encoding past FenceAfter")
	}
	h.noEvent(task.EventFinished, 300*time.Millisecond) // a fenced worker reports nothing
	s := h.nextSettlement()
	assert.Equal(t, settleNak, s.kind)
	assert.Zero(t, s.delay)
	assert.False(t, h.leaseGone(), "a fenced worker's lease is left to lapse, not released")
}

// Fix round 1, item 5: an Update that actually landed on the server but whose
// reply the worker never saw must not read as someone else taking the lease.
func TestServeAdoptsARevisionAfterALostUpdateReply(t *testing.T) {
	h := newHarness(t)
	lost := &lostReplyUpdates{KV: h.leases}
	h.leases = lost
	h.serve()
	h.publish(1, 0)
	<-h.started
	h.clock.Advance(20 * time.Second) // first renewal: the reply is "lost"
	time.Sleep(20 * time.Millisecond)
	assert.True(t, lost.lost.Load(), "the test setup did not exercise the lost-reply path")
	h.clock.Advance(20 * time.Second) // second renewal: must not have fenced
	time.Sleep(20 * time.Millisecond)
	select {
	case <-h.ended:
		t.Fatal("a lost Update reply for the worker's own lease must not stop the encode")
	default:
	}
	h.release <- Outcome{Code: ExitOK, Result: &transcodev1alpha1.Result{}}
	fin := h.next(task.EventFinished)
	assert.Equal(t, task.OutcomeSucceeded, fin.Outcome)
	assert.Equal(t, settleAck, h.nextSettlement().kind)
}

// Review Focus 4.
func TestServeFinishesASucceededTaskDespiteDrain(t *testing.T) {
	h := newHarness(t)
	h.serve()
	h.publish(1, 0)
	<-h.started
	h.release <- Outcome{Code: ExitOK, Result: &transcodev1alpha1.Result{OutputPath: "/data/x.mkv"}}
	h.cancel() // SIGTERM lands as Process returns
	assert.Equal(t, task.OutcomeSucceeded, h.next(task.EventFinished).Outcome)
	assert.Equal(t, settleAck, h.nextSettlement().kind)
	assert.ErrorIs(t, h.waitDone(5*time.Second), context.Canceled)
}

func TestServeDrainNaksUnfinishedWork(t *testing.T) {
	h := newHarness(t)
	h.serve()
	h.publish(1, 0)
	<-h.started
	h.cancel()
	assert.ErrorIs(t, h.waitDone(5*time.Second), context.Canceled)
	h.noEvent(task.EventFinished, 300*time.Millisecond)
	assert.True(t, h.leaseGone(), "drained work releases its lease so the next pod can start at once")
	s := h.nextSettlement()
	assert.Equal(t, settleNak, s.kind)
	assert.Zero(t, s.delay)
}

// Fix round 1, item 1: a drain that lands while Process is still probing --
// returning a classified failure code, not ExitRetriable -- must still win
// as a drain: release the lease and Nak(0) with no finished event, rather
// than report the task Failed and ack it, which would block the job for
// good (a healthy task never gets to run again).
func TestServeDrainDuringAClassifiedFailureReleasesTheLeaseWithoutReporting(t *testing.T) {
	h := newHarness(t)
	h.cancelOutcome = Outcome{Code: ExitInvalidSource}
	h.serve()
	h.publish(1, 0)
	<-h.started
	h.cancel()
	assert.ErrorIs(t, h.waitDone(5*time.Second), context.Canceled)
	h.noEvent(task.EventFinished, 300*time.Millisecond)
	assert.True(t, h.leaseGone(), "a drained probe releases its lease so the next pod can start at once")
	s := h.nextSettlement()
	assert.Equal(t, settleNak, s.kind)
	assert.Zero(t, s.delay)
}

func TestServeEnforcesTheTaskDeadline(t *testing.T) {
	h := newHarness(t)
	h.serve()
	h.publish(1, time.Minute)
	<-h.started
	h.clock.Advance(61 * time.Second)
	fin := h.next(task.EventFinished)
	assert.Equal(t, task.OutcomeFailed, fin.Outcome)
	assert.Equal(t, task.ReasonDeadlineExceeded, fin.Reason)
	assert.Equal(t, settleAck, h.nextSettlement().kind)
}

// Fix round 1, item 4: Process finishing successfully outranks a deadline
// that only fired after it was already done.
func TestServeReportsSuccessEvenAfterTheDeadlineFired(t *testing.T) {
	h := newHarness(t)
	// The default stub's select always prefers a ctx already Done, so it can
	// never reach this scenario: Process ignores the deadline (its own
	// encode/verify/swap was already past the point of checking ctx) and
	// still reports success once it is actually done.
	h.processOverride = func(ctx context.Context, _ task.Task, o Options) Outcome {
		h.calls.Add(1)
		h.started <- struct{}{}
		defer func() { h.ended <- struct{}{} }()
		if o.OnProgress != nil {
			_ = o.OnProgress(ctx, transcodev1alpha1.Progress{Percent: 40})
		}
		return <-h.release
	}
	h.serve()
	h.publish(1, time.Minute)
	<-h.started
	h.clock.Advance(61 * time.Second)
	time.Sleep(20 * time.Millisecond) // let the deadline timer fire and cancel work
	h.release <- Outcome{Code: ExitOK, Result: &transcodev1alpha1.Result{OutputPath: "/data/x.mkv"}}
	fin := h.next(task.EventFinished)
	assert.Equal(t, task.OutcomeSucceeded, fin.Outcome)
	assert.Equal(t, "/data/x.mkv", fin.Result.OutputPath)
	assert.Equal(t, settleAck, h.nextSettlement().kind)
}

// Fix round 1, item 2: a claim that races the marker replace it needs
// (another writer already moved or expired it) must retry, not silently ack
// an attempt that is still perfectly runnable.
func TestServeRetriesAClaimAfterALostMarkerReplace(t *testing.T) {
	h := newHarness(t)
	h.putLease(task.Lease{Job: schema.Ref{UID: "juid"}, Attempt: 1, State: task.LeaseCancelled})
	failFirst := &failFirstUpdate{KV: h.leases}
	h.leases = failFirst
	h.serve()
	h.publish(2, 0)
	claimed := h.next(task.EventClaimed)
	assert.Equal(t, int32(2), claimed.Attempt, "the claim retried after its first replace attempt's reply was lost")
	assert.True(t, failFirst.failed.Load())
	<-h.started
	h.release <- Outcome{Code: ExitOK, Result: &transcodev1alpha1.Result{}}
	assert.Equal(t, int32(2), h.next(task.EventFinished).Attempt)
	assert.Equal(t, settleAck, h.nextSettlement().kind)
}

// Fix round 1, item 8: a finished event that fails to publish must still
// release the lease, so a redelivery is not held to the full lease TTL.
func TestServeReleasesTheLeaseWhenTheFinishedPublishFails(t *testing.T) {
	h := newHarness(t)
	fb := &failFinishedPublish{trackingBus: h.tracking, jobUID: "juid"}
	fb.fail.Store(true)
	h.serveOnBus(fb)
	h.publish(1, 0)
	<-h.started
	h.release <- Outcome{Code: ExitOK, Result: &transcodev1alpha1.Result{}}
	s := h.nextSettlement()
	assert.Equal(t, settleNak, s.kind)
	assert.Zero(t, s.delay)
	h.noEvent(task.EventFinished, 300*time.Millisecond)

	// Nak(0) redelivers at once: the retry reclaims a fresh lease (item 8's
	// release made that possible instead of leaving the redelivery to wait
	// out the lease TTL) and this time the publish succeeds.
	<-h.started
	h.release <- Outcome{Code: ExitOK, Result: &transcodev1alpha1.Result{}}
	fin := h.next(task.EventFinished)
	assert.Equal(t, task.OutcomeSucceeded, fin.Outcome)
	assert.Equal(t, settleAck, h.nextSettlement().kind)
	assert.True(t, h.leaseGone(), "the lease is released once the retry's finished event is stored")
}

// I2c: Serve wires Options.BeforeSwap to reassertBeforeSwap, so a process
// stub that calls it right where run() would -- immediately before the
// swap -- gets exactly renew's own settlement when it finds a confirmed
// cancel marker for this attempt: cancelled, reported, acked. (run.go's own
// envtest, TestRunAbortsTheSwapAndRemovesThePartWhenBeforeSwapFails, proves
// the filesystem side with a real encode; this proves the settlement
// plumbing against the fake bus and clock, cheaply.)
func TestServeSettlesAsCancelledWhenBeforeSwapFindsAConfirmedCancelMarker(t *testing.T) {
	h := newHarness(t)
	h.processOverride = func(ctx context.Context, _ task.Task, o Options) Outcome {
		h.calls.Add(1)
		h.started <- struct{}{}
		defer func() { h.ended <- struct{}{} }()
		// squasharr withdrew this attempt between the last renewal and the
		// swap -- the window I2c closes.
		h.putLease(task.Lease{Job: schema.Ref{UID: "juid"}, Attempt: 1, State: task.LeaseCancelled})
		err := o.BeforeSwap(ctx)
		assert.Error(t, err, "the hook must abort once the marker names this attempt")
		// A real run() never reaches ExitOK once BeforeSwap aborts the swap;
		// the stub mirrors that so the switch is exercised the way handle()
		// would actually see it.
		return Outcome{Code: ExitRetriable, Err: err}
	}
	h.serve()
	h.publish(1, 0)
	<-h.started
	<-h.ended
	fin := h.next(task.EventFinished)
	assert.Equal(t, task.OutcomeCancelled, fin.Outcome)
	assert.Equal(t, task.ReasonCancelled, fin.Reason)
	assert.Equal(t, settleAck, h.nextSettlement().kind)
}

// I2c, the other branch: BeforeSwap finding a healthy lease held by
// somebody else -- a second worker adopted the job after fencing this one
// out, without this pod's own renewal noticing yet -- settles exactly as a
// renewal-time fence would: nothing reported, Nak(0), the lease left for
// its holder rather than released.
func TestServeSettlesAsFencedWhenBeforeSwapFindsAnotherWorkersLease(t *testing.T) {
	h := newHarness(t)
	h.processOverride = func(ctx context.Context, _ task.Task, o Options) Outcome {
		h.calls.Add(1)
		h.started <- struct{}{}
		defer func() { h.ended <- struct{}{} }()
		h.putLease(task.Lease{Job: schema.Ref{UID: "juid"}, Attempt: 1, State: task.LeaseHeld, Pod: "other"})
		err := o.BeforeSwap(ctx)
		assert.Error(t, err, "the hook must abort once another pod holds the lease")
		return Outcome{Code: ExitRetriable, Err: err}
	}
	h.serve()
	h.publish(1, 0)
	<-h.started
	<-h.ended
	h.noEvent(task.EventFinished, 300*time.Millisecond)
	s := h.nextSettlement()
	assert.Equal(t, settleNak, s.kind)
	assert.Zero(t, s.delay)
	assert.False(t, h.leaseGone(), "a fenced worker's lease is left to lapse, not released")
}

// Fix round 1, item 3: an undecodable task is dead-lettered, not silently
// dropped or endlessly redelivered.
func TestServeDeadLettersAnUndecodableTask(t *testing.T) {
	h := newHarness(t)
	dlqc, ok := events.Default().Consumer(events.ConsumerDLQProjector)
	require.True(t, ok)
	dlq := make(chan *events.Envelope, 4)
	stopDLQ, err := h.bus.Subscribe(context.Background(), dlqc.Subscription(), func(_ context.Context, m events.Message) error {
		dlq <- m.Envelope()
		return nil
	})
	require.NoError(t, err)
	t.Cleanup(stopDLQ)

	h.serve()
	id := "garbage-1"
	_, err = h.bus.Publish(h.ctx, events.WorkTranscodeTaskSubject("puid", "cpu", "juid"),
		&events.Envelope{ID: id, Schema: task.Task{}.Schema(), Data: []byte("not json")}, events.WithMsgID(id))
	require.NoError(t, err)

	s := h.nextSettlement()
	assert.Equal(t, settleTerm, s.kind)
	assert.Zero(t, h.calls.Load(), "an undecodable task must never reach Process")

	select {
	case env := <-dlq:
		assert.Equal(t, id, env.Headers[events.HeaderDLQMsgID])
		assert.NotEmpty(t, env.Headers[events.HeaderDLQReason])
	case <-time.After(5 * time.Second):
		t.Fatal("no DLQ envelope published for the undecodable task")
	}
}

// I1: a transient pull error -- a JetStream consumer-leader move (409) or a
// 503 during a node restart -- must not make Serve return at once: every
// idle worker sharing the pool would see one and exit, spending an attempt
// of the pool Job's lifetime backoffLimit each, and killing every other
// healthy encode sharing it. Serve retries with backoff, recreating the
// puller each time (flakyPull's Pull is itself flaky, exercising that
// path), and a task published while it is retrying is still claimed once
// the run of failures ends.
func TestServeRetriesTransientPullErrorsThenClaimsTheTask(t *testing.T) {
	h := newHarness(t)
	flaky := wrapForFlakyPull(h.tracking, errors.New("nats: leadership change"))
	flaky.nextFails.Store(3)
	h.serveOnBusWithOptions(flaky, func(o *ServeOptions) {
		o.PullRetryBackoffCap = 20 * time.Millisecond
		o.PullRetryWindow = time.Minute
	})
	h.publish(1, 0)
	<-h.started
	h.release <- Outcome{Code: ExitOK, Result: &transcodev1alpha1.Result{}}
	fin := h.next(task.EventFinished)
	assert.Equal(t, task.OutcomeSucceeded, fin.Outcome)
	assert.Equal(t, settleAck, h.nextSettlement().kind)
	assert.LessOrEqual(t, flaky.nextFails.Load(), int32(0), "the injected failures were exhausted before the task was pulled")
	assert.GreaterOrEqual(t, flaky.pullCalls.Load(), int32(2), "a failed Next recreates the puller, so Pull ran more than once")
}

// I1, the give-up case: a pull that never recovers must still end Serve --
// so the pod restarts and the pool Job's own machinery (podFailurePolicy,
// backoffLimit) takes over -- but only once the failure streak has lasted
// PullRetryWindow, never on the first error.
func TestServeGivesUpOnAPullThatNeverRecovers(t *testing.T) {
	h := newHarness(t)
	sentinel := errors.New("nats: no suitable servers")
	flaky := wrapForFlakyPull(h.tracking, sentinel)
	flaky.nextFails.Store(-1) // fails forever
	h.serveOnBusWithOptions(flaky, func(o *ServeOptions) {
		o.PullRetryBackoffCap = 10 * time.Millisecond
		o.PullRetryWindow = 100 * time.Millisecond
	})
	err := h.waitDone(5 * time.Second)
	require.Error(t, err)
	assert.ErrorIs(t, err, sentinel)
}

// wrapForFlakyPull decorates bus's Pull for I1's tests: nextFails counts
// down failures the returned Puller's Next reports before behaving
// normally (a negative count never runs out, for a "fails forever" case),
// and pullFails does the same for Pull itself, so Serve's "recreate the
// dead puller" path is exercised too, not only Next's own retry.
func wrapForFlakyPull(bus interface {
	events.Bus
	events.PullSubscriber
}, err error,
) *flakyPullBus {
	return &flakyPullBus{Bus: bus, ps: bus, nextFails: new(atomic.Int32), pullFails: new(atomic.Int32), err: err}
}

type flakyPullBus struct {
	events.Bus
	ps                   events.PullSubscriber
	nextFails, pullFails *atomic.Int32
	err                  error
	pullCalls            atomic.Int32
}

var _ events.PullSubscriber = (*flakyPullBus)(nil)

func (b *flakyPullBus) Pull(ctx context.Context, s events.Subscription) (events.Puller, error) {
	b.pullCalls.Add(1)
	if consumeFlakyFailure(b.pullFails) {
		return nil, b.err
	}
	p, err := b.ps.Pull(ctx, s)
	if err != nil {
		return nil, err
	}
	return &flakyPuller{Puller: p, fails: b.nextFails, err: b.err}, nil
}

type flakyPuller struct {
	events.Puller
	fails *atomic.Int32
	err   error
}

func (p *flakyPuller) Next(ctx context.Context) (context.Context, events.Message, error) {
	if consumeFlakyFailure(p.fails) {
		return ctx, nil, p.err
	}
	return p.Puller.Next(ctx)
}

// consumeFlakyFailure reports whether this call should fail: a positive
// counter decrements and fails, zero never fails, and a negative counter
// fails forever without ever reaching zero.
func consumeFlakyFailure(n *atomic.Int32) bool {
	for {
		cur := n.Load()
		switch {
		case cur == 0:
			return false
		case cur < 0:
			return true
		default:
			if n.CompareAndSwap(cur, cur-1) {
				return true
			}
		}
	}
}

type failingUpdates struct {
	events.KV
	fail atomic.Bool
}

func (f *failingUpdates) Update(ctx context.Context, key string, val []byte, rev uint64) (uint64, error) {
	if f.fail.Load() {
		return 0, events.ErrClosed
	}
	return f.KV.Update(ctx, key, val, rev)
}

// lostReplyUpdates performs every Update for real, but on the first call
// that actually succeeds, reports ErrRevisionMismatch instead of the true
// result -- the write landed on the server; only the reply was lost.
type lostReplyUpdates struct {
	events.KV
	lost atomic.Bool
}

func (l *lostReplyUpdates) Update(ctx context.Context, key string, val []byte, rev uint64) (uint64, error) {
	next, err := l.KV.Update(ctx, key, val, rev)
	if err == nil && l.lost.CompareAndSwap(false, true) {
		return 0, events.ErrRevisionMismatch
	}
	return next, err
}

// failFirstUpdate fails exactly the first Update call with
// ErrRevisionMismatch, then delegates normally.
type failFirstUpdate struct {
	events.KV
	failed atomic.Bool
}

func (f *failFirstUpdate) Update(ctx context.Context, key string, val []byte, rev uint64) (uint64, error) {
	if f.failed.CompareAndSwap(false, true) {
		return 0, events.ErrRevisionMismatch
	}
	return f.KV.Update(ctx, key, val, rev)
}

// failFinishedPublish makes exactly the finished status-event publish fail,
// so a test can prove handle still releases the lease before Nak(0) instead
// of leaving it to lapse.
type failFinishedPublish struct {
	*trackingBus
	jobUID string
	fail   atomic.Bool
}

func (f *failFinishedPublish) Publish(ctx context.Context, subject string, e *events.Envelope,
	opts ...events.PublishOption,
) (events.Receipt, error) {
	if subject == events.WorkTranscodeResultSubject(f.jobUID) {
		var ev task.StatusEvent
		if err := schema.Decode(e.Schema, e.Data, &ev); err == nil && ev.Kind == task.EventFinished {
			// Fail exactly the first finished publish. A Nak(0) after it
			// redelivers at once, and that retry must not fail forever, or
			// the test could never observe the ack half of the recovery.
			if f.fail.CompareAndSwap(true, false) {
				return events.Receipt{}, events.ErrClosed
			}
		}
	}
	return f.trackingBus.Publish(ctx, subject, e, opts...)
}
