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

package contracttest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mediactl/clustarr/pkg/events"
)

// RunPullContract holds a bus's PullSubscriber and StreamAdmin to the
// behaviour squasharr's worker pools depend on.
func RunPullContract(t *testing.T, newBus func() events.Bus) {
	t.Run("PullHandsOutOneMessagePerNext", func(t *testing.T) { testPullOnePerNext(t, newBus) })
	t.Run("PullRedeliversANakedMessage", func(t *testing.T) { testPullRedelivers(t, newBus) })
	t.Run("InProgressHoldsAPulledMessage", func(t *testing.T) { testPullInProgress(t, newBus) })
	t.Run("PurgeSubjectRemovesOnlyThatSubject", func(t *testing.T) { testPurgeSubject(t, newBus) })
	t.Run("DeleteSubscriptionIsIdempotentAndKeepsQueuedWork", func(t *testing.T) { testDeleteSubscription(t, newBus) })
	t.Run("StreamAdminReportsAMissingStream", func(t *testing.T) { testStreamAdminMissingStream(t, newBus) })
}

func pullBus(t *testing.T, bus events.Bus) (events.PullSubscriber, events.StreamAdmin) {
	t.Helper()
	ps, ok := bus.(events.PullSubscriber)
	if !ok {
		t.Fatalf("%T does not implement events.PullSubscriber", bus)
	}
	sa, ok := bus.(events.StreamAdmin)
	if !ok {
		t.Fatalf("%T does not implement events.StreamAdmin", bus)
	}
	return ps, sa
}

func publishTask(ctx context.Context, t *testing.T, bus events.Bus, profile, job string) {
	t.Helper()
	subj := events.WorkTranscodeTaskSubject(profile, "cpu", job)
	if _, err := bus.Publish(ctx, subj, envelope(job, "transcode.Task.v1", job)); err != nil {
		t.Fatalf("Publish %s: %v", subj, err)
	}
}

func next(ctx context.Context, t *testing.T, p events.Puller, within time.Duration) events.Message {
	t.Helper()
	c, cancel := context.WithTimeout(ctx, within)
	defer cancel()
	_, m, err := p.Next(c)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	return m
}

func nothingWithin(ctx context.Context, t *testing.T, p events.Puller, within time.Duration) {
	t.Helper()
	c, cancel := context.WithTimeout(ctx, within)
	defer cancel()
	if _, m, err := p.Next(c); err == nil {
		t.Fatalf("Next returned %s when nothing was deliverable", m.Envelope().ID)
	}
}

func testPullOnePerNext(t *testing.T, newBus func() events.Bus) {
	ctx, bus := setup(t, newBus)
	ps, _ := pullBus(t, bus)
	sub := events.TranscodeTaskConsumer("prof", "cpu").Subscription()
	publishTask(ctx, t, bus, "prof", "j1")
	publishTask(ctx, t, bus, "prof", "j2")

	a, err := ps.Pull(ctx, sub)
	if err != nil {
		t.Fatalf("Pull a: %v", err)
	}
	defer a.Stop()
	b, err := ps.Pull(ctx, sub)
	if err != nil {
		t.Fatalf("Pull b: %v", err)
	}
	defer b.Stop()

	m1, m2 := next(ctx, t, a, 5*time.Second), next(ctx, t, b, 5*time.Second)
	if m1.Envelope().ID == m2.Envelope().ID {
		t.Fatalf("two pullers on one durable both got %s", m1.Envelope().ID)
	}
	nothingWithin(ctx, t, a, 500*time.Millisecond)
	_ = m1.Ack(ctx)
	_ = m2.Ack(ctx)
}

func testPullRedelivers(t *testing.T, newBus func() events.Bus) {
	ctx, bus := setup(t, newBus)
	ps, _ := pullBus(t, bus)
	p, err := ps.Pull(ctx, events.TranscodeTaskConsumer("prof", "cpu").Subscription())
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	defer p.Stop()
	publishTask(ctx, t, bus, "prof", "j1")

	m := next(ctx, t, p, 5*time.Second)
	if err := m.Nak(ctx, 0); err != nil {
		t.Fatalf("Nak: %v", err)
	}
	again := next(ctx, t, p, 5*time.Second)
	if again.Envelope().ID != "j1" || again.Attempt() != 2 {
		t.Fatalf("redelivery = %s attempt %d, want j1 attempt 2", again.Envelope().ID, again.Attempt())
	}
	_ = again.Ack(ctx)
}

func testPullInProgress(t *testing.T, newBus func() events.Bus) {
	ctx, bus := setup(t, newBus)
	ps, _ := pullBus(t, bus)
	sub := events.TranscodeTaskConsumer("prof", "cpu").Subscription()
	sub.AckWait = 2 * time.Second
	p, err := ps.Pull(ctx, sub)
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	defer p.Stop()
	publishTask(ctx, t, bus, "prof", "j1")

	m := next(ctx, t, p, 5*time.Second)
	for i := 0; i < 8; i++ { // 4s, twice the ack window
		time.Sleep(500 * time.Millisecond)
		if err := m.InProgress(ctx); err != nil {
			t.Fatalf("InProgress: %v", err)
		}
	}
	nothingWithin(ctx, t, p, 500*time.Millisecond)
	// Stop renewing: the message must come back after one ack window.
	again := next(ctx, t, p, 6*time.Second)
	if again.Envelope().ID != "j1" {
		t.Fatalf("redelivered %s, want j1", again.Envelope().ID)
	}
	_ = again.Ack(ctx)
}

func testPurgeSubject(t *testing.T, newBus func() events.Bus) {
	ctx, bus := setup(t, newBus)
	ps, sa := pullBus(t, bus)
	publishTask(ctx, t, bus, "prof", "gone")
	publishTask(ctx, t, bus, "prof", "kept")

	subjects, err := sa.Subjects(ctx, events.StreamWorkSquasharr, events.FilterTranscodeTasks("prof", "cpu"))
	if err != nil || len(subjects) != 2 {
		t.Fatalf("Subjects = %v, %v; want two", subjects, err)
	}
	if err := sa.PurgeSubject(ctx, events.StreamWorkSquasharr,
		events.WorkTranscodeTaskSubject("prof", "cpu", "gone")); err != nil {
		t.Fatalf("PurgeSubject: %v", err)
	}
	p, err := ps.Pull(ctx, events.TranscodeTaskConsumer("prof", "cpu").Subscription())
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	defer p.Stop()
	if m := next(ctx, t, p, 5*time.Second); m.Envelope().ID != "kept" {
		t.Fatalf("got %s after purging gone, want kept", m.Envelope().ID)
	}
	nothingWithin(ctx, t, p, 500*time.Millisecond)
}

func testDeleteSubscription(t *testing.T, newBus func() events.Bus) {
	ctx, bus := setup(t, newBus)
	ps, sa := pullBus(t, bus)
	sub := events.TranscodeTaskConsumer("prof", "cpu").Subscription()
	if err := sa.DeleteSubscription(ctx, sub.Stream, sub.Durable); err != nil {
		t.Fatalf("deleting a durable that never existed: %v", err)
	}
	p, err := ps.Pull(ctx, sub)
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	p.Stop()
	publishTask(ctx, t, bus, "prof", "queued")
	if err := sa.DeleteSubscription(ctx, sub.Stream, sub.Durable); err != nil {
		t.Fatalf("DeleteSubscription: %v", err)
	}
	p, err = ps.Pull(ctx, sub) // a new durable on a work queue still sees the stored task
	if err != nil {
		t.Fatalf("Pull after delete: %v", err)
	}
	defer p.Stop()
	if m := next(ctx, t, p, 5*time.Second); m.Envelope().ID != "queued" {
		t.Fatalf("got %s, want the task published before the delete", m.Envelope().ID)
	}
}

// testStreamAdminMissingStream holds every StreamAdmin method to one error
// contract for a stream nothing ensured: PurgeSubject and Subjects must fail
// with an error satisfying errors.Is(err, events.ErrStreamNotFound), the
// sentinel Subscribe and Pull already report for the same condition, so a
// caller can switch on one error whichever StreamAdmin call it made. natsbus
// wrapping the raw jetstream.ErrStreamNotFound instead of remapping it, while
// membus already used the sentinel, is exactly the drift this guards.
// DeleteSubscription is different by design (see its doc comment: a missing
// durable, and so a missing stream, is not an error) and must stay nil.
func testStreamAdminMissingStream(t *testing.T, newBus func() events.Bus) {
	ctx, bus := setup(t, newBus)
	_, sa := pullBus(t, bus)
	const missing = "CLUSTARR_NO_SUCH_STREAM"

	if err := sa.PurgeSubject(ctx, missing, "clustarr.no.such.subject"); !errors.Is(err, events.ErrStreamNotFound) {
		t.Fatalf("PurgeSubject on a missing stream = %v, want errors.Is ErrStreamNotFound", err)
	}
	if _, err := sa.Subjects(ctx, missing, "clustarr.>"); !errors.Is(err, events.ErrStreamNotFound) {
		t.Fatalf("Subjects on a missing stream = %v, want errors.Is ErrStreamNotFound", err)
	}
	if err := sa.DeleteSubscription(ctx, missing, "some-durable"); err != nil {
		t.Fatalf("DeleteSubscription on a missing stream = %v, want nil", err)
	}
}
