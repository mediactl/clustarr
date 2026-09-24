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
	t.Run("PurgeSubjectWildcardMatchesAcrossTheWildcardTokenOnly", func(t *testing.T) { testPurgeSubjectWildcard(t, newBus) })
	t.Run("DeleteSubscriptionIsIdempotentAndKeepsQueuedWork", func(t *testing.T) { testDeleteSubscription(t, newBus) })
	t.Run("SubscriptionsListsEachDurableUntilItIsDeleted", func(t *testing.T) { testSubscriptions(t, newBus) })
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

// testPurgeSubjectWildcard is ruling R23: withdraw purges a TranscodeJob's
// task with a wildcard in place of the profile token
// (events.WorkTranscodeTaskSubjectAnyProfile), since it does not resolve
// the TranscodeProfile any more. The purge must match every subject that
// differs only in the wildcarded token -- across profiles here -- and leave
// a sibling that differs in a token the filter does NOT wildcard (the job
// here) alone.
func testPurgeSubjectWildcard(t *testing.T, newBus func() events.Bus) {
	ctx, bus := setup(t, newBus)
	ps, sa := pullBus(t, bus)
	publishTask(ctx, t, bus, "profA", "job1") // matches: only the profile token differs from the filter
	publishTask(ctx, t, bus, "profB", "job1") // matches, under an entirely different profile
	publishTask(ctx, t, bus, "profA", "job2") // sibling: the job token differs -- must survive

	if err := sa.PurgeSubject(ctx, events.StreamWorkSquasharr,
		events.WorkTranscodeTaskSubjectAnyProfile("cpu", "job1")); err != nil {
		t.Fatalf("PurgeSubject (wildcard): %v", err)
	}

	subjects, err := sa.Subjects(ctx, events.StreamWorkSquasharr, "clustarr.work.transcode.task.>")
	if err != nil {
		t.Fatalf("Subjects: %v", err)
	}
	want := events.WorkTranscodeTaskSubject("profA", "cpu", "job2")
	if len(subjects) != 1 || subjects[0] != want {
		t.Fatalf("Subjects after wildcard purge = %v, want only [%s]", subjects, want)
	}

	// The survivor is still deliverable, not just still named in Subjects.
	kept, err := ps.Pull(ctx, events.TranscodeTaskConsumer("profA", "cpu").Subscription())
	if err != nil {
		t.Fatalf("Pull profA: %v", err)
	}
	defer kept.Stop()
	if m := next(ctx, t, kept, 5*time.Second); m.Envelope().ID != "job2" {
		t.Fatalf("got %s from profA after wildcard purge, want job2", m.Envelope().ID)
	}
	nothingWithin(ctx, t, kept, 500*time.Millisecond)

	// The other profile's matching subject is gone, not merely re-homed.
	gone, err := ps.Pull(ctx, events.TranscodeTaskConsumer("profB", "cpu").Subscription())
	if err != nil {
		t.Fatalf("Pull profB: %v", err)
	}
	defer gone.Stop()
	nothingWithin(ctx, t, gone, 500*time.Millisecond)
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

// testSubscriptions is what squasharr's pool sweep stands on (final-review
// M1): Subscriptions lists every durable a Pull or a Subscribe created on a
// stream -- after its puller or subscription stopped, since a durable
// outlives both -- and a durable DeleteSubscription removed is gone from
// the list, while a sibling on the same stream is not.
func testSubscriptions(t *testing.T, newBus func() events.Bus) {
	ctx, bus := setup(t, newBus)
	ps, sa := pullBus(t, bus)
	gone := events.TranscodeTaskConsumer("profGone", "cpu").Subscription()
	kept := events.TranscodeTaskConsumer("profKept", "nvidia").Subscription()

	p, err := ps.Pull(ctx, gone)
	if err != nil {
		t.Fatalf("Pull %s: %v", gone.Durable, err)
	}
	p.Stop()
	stop, err := bus.Subscribe(ctx, kept, func(context.Context, events.Message) error { return nil })
	if err != nil {
		t.Fatalf("Subscribe %s: %v", kept.Durable, err)
	}
	stop()

	has := func(names []string, want string) bool {
		for _, n := range names {
			if n == want {
				return true
			}
		}
		return false
	}
	names, err := sa.Subscriptions(ctx, events.StreamWorkSquasharr)
	if err != nil {
		t.Fatalf("Subscriptions: %v", err)
	}
	if !has(names, gone.Durable) || !has(names, kept.Durable) {
		t.Fatalf("Subscriptions = %v, want both %s and %s: a durable outlives its puller and its subscription",
			names, gone.Durable, kept.Durable)
	}

	if err := sa.DeleteSubscription(ctx, gone.Stream, gone.Durable); err != nil {
		t.Fatalf("DeleteSubscription: %v", err)
	}
	names, err = sa.Subscriptions(ctx, events.StreamWorkSquasharr)
	if err != nil {
		t.Fatalf("Subscriptions after delete: %v", err)
	}
	if has(names, gone.Durable) {
		t.Fatalf("Subscriptions = %v after deleting %s, want it gone", names, gone.Durable)
	}
	if !has(names, kept.Durable) {
		t.Fatalf("Subscriptions = %v after deleting %s, want %s kept", names, gone.Durable, kept.Durable)
	}
}

// testStreamAdminMissingStream holds every StreamAdmin method to one error
// contract for a stream nothing ensured: PurgeSubject, Subjects and
// Subscriptions must fail with an error satisfying errors.Is(err,
// events.ErrStreamNotFound), the sentinel Subscribe and Pull already report
// for the same condition, so a caller can switch on one error whichever
// StreamAdmin call it made. natsbus
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
	if _, err := sa.Subscriptions(ctx, missing); !errors.Is(err, events.ErrStreamNotFound) {
		t.Fatalf("Subscriptions on a missing stream = %v, want errors.Is ErrStreamNotFound", err)
	}
	if err := sa.DeleteSubscription(ctx, missing, "some-durable"); err != nil {
		t.Fatalf("DeleteSubscription on a missing stream = %v, want nil", err)
	}
}
