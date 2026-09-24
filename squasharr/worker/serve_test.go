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
	"sync/atomic"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/squasharr/task"
)

type harness struct {
	t       *testing.T
	ctx     context.Context
	cancel  context.CancelFunc
	clock   *clockwork.FakeClock
	bus     events.Bus
	leases  events.KV
	calls   atomic.Int32
	release chan Outcome // unbuffered: a send means the stub took it
	started chan struct{}
	ended   chan struct{}
	events  chan task.StatusEvent
	done    chan error
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{
		t: t, clock: clockwork.NewFakeClock(), release: make(chan Outcome),
		started: make(chan struct{}, 8), ended: make(chan struct{}, 8),
		events: make(chan task.StatusEvent, 64), done: make(chan error, 1),
	}
	h.ctx, h.cancel = context.WithCancel(context.Background())
	t.Cleanup(h.cancel)
	h.bus = membus.New(h.clock)
	require.NoError(t, h.bus.Ensure(h.ctx, events.Default().ForSingleNode()))
	h.leases = h.bus.KV(events.BucketTranscodeLeases)
	rc, ok := events.Default().Consumer(events.ConsumerSquasharrResults)
	require.True(t, ok)
	stop, err := h.bus.Subscribe(context.Background(), rc.Subscription(), func(_ context.Context, m events.Message) error {
		var ev task.StatusEvent
		require.NoError(t, schema.Decode(m.Envelope().Schema, m.Envelope().Data, &ev))
		h.events <- ev
		return nil
	})
	require.NoError(t, err)
	t.Cleanup(stop)
	t.Cleanup(pumpClock(t, h.clock, 2*time.Millisecond))
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
// catalogarr/worker/grab/decide_envtest_test.go's pumpClock.
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
	go func() {
		h.done <- Serve(h.ctx, h.bus, ServeOptions{
			ProfileUID: "puid", Class: "cpu", Node: "n1",
			Options: Options{PodName: "pool-abc"},
			Leases:  h.leases, Clock: h.clock,
			Process: func(ctx context.Context, _ task.Task, o Options) Outcome {
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
					return Outcome{Code: ExitRetriable, Err: ctx.Err()}
				}
			},
		})
	}()
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
	assert.False(t, h.leaseGone(), "the lease is held while Process runs")
	progress := h.next(task.EventProgress)
	assert.Equal(t, uint64(2), progress.Seq)
	assert.Equal(t, int32(40), progress.Progress.Percent)

	h.release <- Outcome{Code: ExitOK, Result: &transcodev1alpha1.Result{OutputPath: "/data/x.mkv"}}
	fin := h.next(task.EventFinished)
	assert.Equal(t, task.OutcomeSucceeded, fin.Outcome)
	assert.Equal(t, "/data/x.mkv", fin.Result.OutputPath)
	assert.Equal(t, uint64(3), fin.Seq)
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
}

func TestServeDropsATaskCancelledForItsOwnAttempt(t *testing.T) {
	h := newHarness(t)
	h.putLease(task.Lease{Job: schema.Ref{UID: "juid"}, Attempt: 2, State: task.LeaseCancelled})
	h.serve()
	h.publish(2, 0)
	h.noEvent(task.EventClaimed, 300*time.Millisecond)
	assert.Zero(t, h.calls.Load())
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
	assert.ErrorIs(t, <-h.done, context.Canceled)
}

func TestServeDrainNaksUnfinishedWork(t *testing.T) {
	h := newHarness(t)
	h.serve()
	h.publish(1, 0)
	<-h.started
	h.cancel()
	assert.ErrorIs(t, <-h.done, context.Canceled)
	h.noEvent(task.EventFinished, 300*time.Millisecond)
	assert.True(t, h.leaseGone(), "drained work releases its lease so the next pod can start at once")
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
