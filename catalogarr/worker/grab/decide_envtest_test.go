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

package grab_test

import (
	"context"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/worker/grab"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// pumpClock advances a fake clock from another goroutine until the test is
// done with it. membus's subscription loop polls through clock.After, so a
// fake clock that nobody advances never delivers anything -- membus.New's own
// doc comment says as much.
func pumpClock(t *testing.T, clock *clockwork.FakeClock, step time.Duration) {
	t.Helper()
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	go func() {
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
}

// TestDecide_BypassGrabsImmediately: a top-tier release under a profile with
// BypassIfHighestQuality (the CRD default) skips the delay entirely -- a
// Download now, and nothing written to clustarr-pending.
func TestDecide_BypassGrabsImmediately(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ns := newNamespace(t, ctx, c)

	movie := newMovie(t, ctx, c, ns, "the-thing-1982")
	newIndexer(t, ctx, c, ns, "my-indexer", nil)

	profile := hdBlurayWeb(t)
	bus := newTestBus(t, nil)
	target := commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: movie.Name}

	err := grab.Decide(ctx,
		grab.Deps{Client: c, Bus: bus, Now: fixedNow(testNow)},
		profile,
		catalogv1alpha1.DelayProfileSpec{TorrentDelayMinutes: 45},
		grab.Approved{
			Namespace: ns,
			Target:    target,
			Release:   torrentRelease("guid-top", "my-indexer", profile.Tiers[0][0].Quality, 0),
			GrabbedBy: downloadv1alpha1.GrabSourceSearch,
		})
	require.NoError(t, err)

	var downloads downloadv1alpha1.DownloadList
	require.NoError(t, c.List(ctx, &downloads, client.InNamespace(ns)))
	require.Len(t, downloads.Items, 1, "a bypassed grab happens immediately")

	_, err = bus.KV(events.BucketPending).Get(ctx, events.PendingKey(grab.MediaKey(ns, target)))
	assert.ErrorIs(t, err, events.ErrKeyNotFound, "a bypassed grab never touches clustarr-pending")

	var got catalogv1alpha1.Movie
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(movie), &got))
	assert.Nil(t, got.Status.PendingGrab)
	assert.Empty(t, got.Status.Phase, "Decide must never set Phase")
}

// TestDecide_DelayedGrabSchedulesAndSetsPendingGrab is the delay path end to
// end: no Download yet, status.pendingGrab recorded with GrabAt =
// firstSeen+delay, and a GrabTask that the catalogarr-grab consumer only
// receives once the fake clock has passed the delay.
func TestDecide_DelayedGrabSchedulesAndSetsPendingGrab(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := newTestClient(t)
	ns := newNamespace(t, ctx, c)

	movie := newMovie(t, ctx, c, ns, "the-thing-1982")
	newIndexer(t, ctx, c, ns, "my-indexer", nil)

	profile := hdBlurayWeb(t)
	clock := clockwork.NewFakeClockAt(testNow)
	bus := newTestBus(t, clock)
	target := commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: movie.Name}

	// Subscribe on exactly the subscription the real Handler uses, so the
	// test proves the scheduled task lands on the subject and filter the grab
	// consumer actually watches.
	received := make(chan schema.GrabTask, 4)
	stop, err := bus.Subscribe(ctx, grab.NewHandler(grab.Deps{Client: c, Bus: bus}).Subscription(),
		func(_ context.Context, m events.Message) error {
			var task schema.GrabTask
			if err := schema.Decode(m.Envelope().Schema, m.Envelope().Data, &task); err != nil {
				return err
			}
			received <- task
			return nil
		})
	require.NoError(t, err)
	defer stop()

	bottom := profile.Tiers[len(profile.Tiers)-1][0].Quality
	err = grab.Decide(ctx,
		grab.Deps{Client: c, Bus: bus, Now: clock.Now},
		profile,
		catalogv1alpha1.DelayProfileSpec{TorrentDelayMinutes: 45},
		grab.Approved{
			Namespace: ns,
			Target:    target,
			Release:   torrentRelease("guid-bottom", "my-indexer", bottom, 0),
			GrabbedBy: downloadv1alpha1.GrabSourceSearch,
		})
	require.NoError(t, err)

	var downloads downloadv1alpha1.DownloadList
	require.NoError(t, c.List(ctx, &downloads, client.InNamespace(ns)))
	assert.Empty(t, downloads.Items, "a delayed grab creates no Download yet")

	var got catalogv1alpha1.Movie
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(movie), &got))
	require.NotNil(t, got.Status.PendingGrab)
	assert.Equal(t, "The.Thing.1982.1080p.BluRay.x264-GROUP", got.Status.PendingGrab.ReleaseTitle)
	assert.Equal(t, commonv1.ProtocolTorrent, got.Status.PendingGrab.Protocol)
	assert.True(t, got.Status.PendingGrab.GrabAt.Time.Equal(testNow.Add(45*time.Minute)),
		"grabAt = %v, want %v", got.Status.PendingGrab.GrabAt.Time, testNow.Add(45*time.Minute))
	assert.Empty(t, got.Status.Phase, "Decide must never set Phase -- that is the Movie reconciler's job")

	// Nothing is delivered before the window elapses.
	select {
	case task := <-received:
		t.Fatalf("a GrabTask was delivered before the delay elapsed: %+v", task)
	case <-time.After(50 * time.Millisecond):
	}

	// Advance past the window. The clock must be driven from another
	// goroutine because the subscription loop polls on it.
	pumpClock(t, clock, 5*time.Minute)
	select {
	case task := <-received:
		assert.Equal(t, target, task.MediaRef)
	case <-time.After(10 * time.Second):
		t.Fatal("no GrabTask delivered after the delay elapsed")
	}
}

// TestDecide_SecondBetterCandidateKeepsTheWindow is §8.7's "pending CAS
// keep-best updates a delayed grab": a better release replaces the candidate
// and the recorded title, but grabAt does not move.
func TestDecide_SecondBetterCandidateKeepsTheWindow(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ns := newNamespace(t, ctx, c)

	movie := newMovie(t, ctx, c, ns, "the-thing-1982")
	profile := hdBlurayWeb(t)
	clock := clockwork.NewFakeClockAt(testNow)
	bus := newTestBus(t, clock)
	target := commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: movie.Name}
	delay := catalogv1alpha1.DelayProfileSpec{
		TorrentDelayMinutes: 45,
		// Both bypasses off, or the second (top-tier) candidate would be
		// grabbed on the spot instead of replacing the pending one.
		BypassIfHighestQuality: boolPtr(false),
	}

	deps := grab.Deps{Client: c, Bus: bus, Now: clock.Now}
	bottom := profile.Tiers[len(profile.Tiers)-1][0].Quality
	require.NoError(t, grab.Decide(ctx, deps, profile, delay, grab.Approved{
		Namespace: ns, Target: target,
		Release:   torrentRelease("guid-worse", "my-indexer", bottom, 0),
		GrabbedBy: downloadv1alpha1.GrabSourceSearch,
	}))

	clock.Advance(10 * time.Minute)
	better := torrentRelease("guid-better", "my-indexer", profile.Tiers[0][0].Quality, 50)
	better.Title = "The.Thing.1982.1080p.BluRay.REMUX-BETTER"
	require.NoError(t, grab.Decide(ctx, deps, profile, delay, grab.Approved{
		Namespace: ns, Target: target, Release: better, GrabbedBy: downloadv1alpha1.GrabSourceRSS,
	}))

	var got catalogv1alpha1.Movie
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(movie), &got))
	require.NotNil(t, got.Status.PendingGrab)
	assert.Equal(t, better.Title, got.Status.PendingGrab.ReleaseTitle, "the better candidate replaces the pending one")
	assert.True(t, got.Status.PendingGrab.GrabAt.Time.Equal(testNow.Add(45*time.Minute)),
		"grabAt = %v, want the original %v: a replacement must not restart the delay",
		got.Status.PendingGrab.GrabAt.Time, testNow.Add(45*time.Minute))

	var downloads downloadv1alpha1.DownloadList
	require.NoError(t, c.List(ctx, &downloads, client.InNamespace(ns)))
	assert.Empty(t, downloads.Items)
}

func boolPtr(b bool) *bool { return &b }
