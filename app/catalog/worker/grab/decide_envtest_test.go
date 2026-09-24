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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/worker/grab"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/quality"
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

// TestSink_DeliversTheBestApprovedRelease is the bridge between §8.2's two
// halves: the search worker's ranked list in, a Download out, with the
// not-approved candidates ignored and the ranking respected.
func TestSink_DeliversTheBestApprovedRelease(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ns := newNamespace(t, ctx, c)

	movie := newMovie(t, ctx, c, ns, "the-thing-1982")
	newIndexer(t, ctx, c, ns, "my-indexer", nil)
	profile := hdBlurayWeb(t)

	rejected := torrentRelease("guid-rejected", "my-indexer", profile.Tiers[0][0].Quality, 0)
	approved := torrentRelease("guid-approved", "my-indexer", profile.Tiers[0][0].Quality, 0)

	sink := grab.Sink{
		Deps:           grab.Deps{Client: c, Bus: newTestBus(t, nil), Now: fixedNow(testNow)},
		ResolveProfile: func(context.Context, string) (quality.Profile, error) { return profile, nil },
		ResolveDelay: func(context.Context, string, *string, []string) (catalogv1alpha1.DelayProfileSpec, error) {
			return catalogv1alpha1.DelayProfileSpec{}, nil
		},
	}
	err := sink.Deliver(ctx, ns, commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: movie.Name},
		[]commonv1.ReleaseDecision{
			{ReleaseInfo: rejected, Approved: false, Rank: 0},
			{ReleaseInfo: approved, Approved: true, Rank: 1},
		}, "")
	require.NoError(t, err)

	var downloads downloadv1alpha1.DownloadList
	require.NoError(t, c.List(ctx, &downloads, client.InNamespace(ns)))
	require.Len(t, downloads.Items, 1)
	assert.Equal(t, "guid-approved", downloads.Items[0].Spec.Release.GUID,
		"a rejected decision must never be grabbed, however well it ranked")
	assert.Equal(t, downloadv1alpha1.GrabSourceSearch, downloads.Items[0].Spec.GrabbedBy,
		"an empty grab source is a search")
}

// TestSink_NoApprovedReleaseIsANoOp: a search that approved nothing is a
// successful search, not a failure.
func TestSink_NoApprovedReleaseIsANoOp(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ns := newNamespace(t, ctx, c)
	newMovie(t, ctx, c, ns, "the-thing-1982")

	sink := grab.Sink{Deps: grab.Deps{Client: c, Bus: newTestBus(t, nil), Now: fixedNow(testNow)}}
	require.NoError(t, sink.Deliver(ctx, ns,
		commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "the-thing-1982"},
		[]commonv1.ReleaseDecision{{Approved: false}}, downloadv1alpha1.GrabSourceSearch))

	var downloads downloadv1alpha1.DownloadList
	require.NoError(t, c.List(ctx, &downloads, client.InNamespace(ns)))
	assert.Empty(t, downloads.Items)
}

// TestResolveConfig_EpisodeReadsItsSeries pins the rule that EpisodeSpec has
// no QualityProfileRef, DelayProfileRef or Tags of its own.
func TestResolveConfig_EpisodeReadsItsSeries(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ns := newNamespace(t, ctx, c)

	series := &catalogv1alpha1.Series{
		ObjectMeta: metav1.ObjectMeta{Name: "the-wire", Namespace: ns},
		Spec: catalogv1alpha1.SeriesSpec{
			TvdbID: 79126, QualityProfileRef: "hd-bluray-web", RootFolderRef: "tv",
			DelayProfileRef: ptr.To("slow"), Tags: []string{"hd"},
		},
	}
	require.NoError(t, c.Create(ctx, series))
	ep := &catalogv1alpha1.Episode{
		ObjectMeta: metav1.ObjectMeta{Name: "the-wire-s01e01", Namespace: ns},
		Spec:       catalogv1alpha1.EpisodeSpec{SeriesRef: series.Name, SeasonNumber: 1, EpisodeNumber: 1},
	}
	require.NoError(t, c.Create(ctx, ep))

	for _, ref := range []commonv1.MediaRef{
		{Kind: commonv1.MediaKindEpisode, Name: ep.Name},
		{Kind: commonv1.MediaKindSeries, Name: series.Name, Keys: []string{ep.Name}},
	} {
		cfg, err := grab.ResolveConfig(ctx, c, ns, ref)
		require.NoErrorf(t, err, "kind %s", ref.Kind)
		assert.Equal(t, "hd-bluray-web", cfg.QualityProfileRef)
		require.NotNil(t, cfg.DelayProfileRef)
		assert.Equal(t, "slow", *cfg.DelayProfileRef)
		assert.Equal(t, []string{"hd"}, cfg.Tags)
	}

	_, err := grab.ResolveConfig(ctx, c, ns, commonv1.MediaRef{Kind: commonv1.MediaKindSeries, Name: series.Name})
	assert.ErrorIs(t, err, grab.ErrUnsupportedKind, "a series target with no keys governs nothing")
}

// TestDecide_TwoSeasonPacksKeepSeparatePendingEntries is the pack-collision
// regression, end to end.
//
// Both packs name the same Series, so before MediaKeyFor they shared one
// clustarr-pending key: the second Decide compared an S02 pack against an S01
// pack on quality alone, evicted one of them, and reused the first pack's
// Msg-Id so the evicted pack's scheduled delivery was deduplicated away --
// while its episodes kept a status.pendingGrab nothing would ever clear.
func TestDecide_TwoSeasonPacksKeepSeparatePendingEntries(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ns := newNamespace(t, ctx, c)

	series := &catalogv1alpha1.Series{
		ObjectMeta: metav1.ObjectMeta{Name: "the-wire", Namespace: ns},
		Spec: catalogv1alpha1.SeriesSpec{
			TvdbID: 79126, QualityProfileRef: "hd-bluray-web", RootFolderRef: "tv",
		},
	}
	require.NoError(t, c.Create(ctx, series))
	s1 := []string{"the-wire-s01e01", "the-wire-s01e02"}
	s2 := []string{"the-wire-s02e01", "the-wire-s02e02"}
	for season, names := range map[int32][]string{1: s1, 2: s2} {
		for i, n := range names {
			require.NoError(t, c.Create(ctx, &catalogv1alpha1.Episode{
				ObjectMeta: metav1.ObjectMeta{Name: n, Namespace: ns},
				Spec: catalogv1alpha1.EpisodeSpec{
					SeriesRef: series.Name, SeasonNumber: season, EpisodeNumber: int32(i + 1),
					Monitored: ptr.To(true),
				},
			}))
		}
	}

	profile := hdBlurayWeb(t)
	bus := newTestBus(t, nil)
	deps := grab.Deps{Client: c, Bus: bus, Now: fixedNow(testNow)}
	delay := catalogv1alpha1.DelayProfileSpec{TorrentDelayMinutes: 45, BypassIfHighestQuality: boolPtr(false)}
	target := commonv1.MediaRef{Kind: commonv1.MediaKindSeries, Name: series.Name}

	// The S01 pack is the better release. If the two share a key, the S02
	// pack loses keep-best and is evicted.
	best := torrentRelease("guid-s01", "my-indexer", profile.Tiers[0][0].Quality, 100)
	worse := torrentRelease("guid-s02", "my-indexer", profile.Tiers[len(profile.Tiers)-1][0].Quality, 0)

	require.NoError(t, grab.Decide(ctx, deps, profile, delay, grab.Approved{
		Namespace: ns, Target: target, Keys: s1, Release: best, GrabbedBy: downloadv1alpha1.GrabSourceSearch,
	}))
	require.NoError(t, grab.Decide(ctx, deps, profile, delay, grab.Approved{
		Namespace: ns, Target: target, Keys: s2, Release: worse, GrabbedBy: downloadv1alpha1.GrabSourceSearch,
	}))

	kv := bus.KV(events.BucketPending)
	e1, err := kv.Get(ctx, events.PendingKey(grab.MediaKeyFor(ns, target, s1)))
	require.NoError(t, err, "the S01 pack lost its pending entry")
	e2, err := kv.Get(ctx, events.PendingKey(grab.MediaKeyFor(ns, target, s2)))
	require.NoError(t, err, "the S02 pack was evicted by the S01 pack's keep-best")
	assert.NotEqual(t, string(e1.Value), string(e2.Value))
	assert.Contains(t, string(e1.Value), "guid-s01")
	assert.Contains(t, string(e2.Value), "guid-s02")

	// And every episode of BOTH seasons is genuinely waiting, not just the
	// ones whose entry happened to survive.
	for _, n := range append(append([]string{}, s1...), s2...) {
		var ep catalogv1alpha1.Episode
		require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: ns, Name: n}, &ep))
		require.NotNilf(t, ep.Status.PendingGrab, "%s has no pendingGrab", n)
	}
}

// TestDecide_PropagatesTheTraceToTheScheduledGrab is item 4's regression: a
// delayed grab is the longest causal gap in the system, so an orphaned span
// there is exactly the trace an operator wants and cannot get.
//
// It asserts the wire header end to end: the producer's trace ID is on the
// envelope the grab consumer receives, and grab.Handler.Handle's own span
// joins that trace rather than starting a new one.
func TestDecide_PropagatesTheTraceToTheScheduledGrab(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := newTestClient(t)
	ns := newNamespace(t, ctx, c)

	_, err := tracing.Setup(ctx, tracing.Options{Enabled: false, ServiceName: "grab-test", SampleRatio: 1})
	require.NoError(t, err)

	movie := newMovie(t, ctx, c, ns, "the-thing-1982")
	profile := hdBlurayWeb(t)
	clock := clockwork.NewFakeClockAt(testNow)
	bus := newTestBus(t, clock)
	target := commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: movie.Name}

	received := make(chan string, 4)
	stop, err := bus.Subscribe(ctx, grab.NewHandler(grab.Deps{Client: c, Bus: bus}).Subscription(),
		func(hctx context.Context, m events.Message) error {
			// Exactly what Handle does: Extract, then Start.
			hctx = tracing.Extract(hctx, m.Envelope())
			_, span := tracing.Start(hctx, "consumer")
			defer span.End()
			received <- span.SpanContext().TraceID().String()
			return nil
		})
	require.NoError(t, err)
	defer stop()

	produceCtx, produceSpan := tracing.Start(ctx, "producer")
	wantTrace := produceSpan.SpanContext().TraceID().String()
	err = grab.Decide(produceCtx,
		grab.Deps{Client: c, Bus: bus, Now: clock.Now},
		profile,
		catalogv1alpha1.DelayProfileSpec{TorrentDelayMinutes: 45, BypassIfHighestQuality: boolPtr(false)},
		grab.Approved{
			Namespace: ns, Target: target,
			Release:   torrentRelease("guid-traced", "my-indexer", profile.Tiers[len(profile.Tiers)-1][0].Quality, 0),
			GrabbedBy: downloadv1alpha1.GrabSourceSearch,
		})
	produceSpan.End()
	require.NoError(t, err)

	pumpClock(t, clock, 5*time.Minute)
	select {
	case got := <-received:
		assert.Equal(t, wantTrace, got,
			"the grab consumer's span must join the trace of the decision that scheduled it")
	case <-time.After(10 * time.Second):
		t.Fatal("no GrabTask delivered")
	}
}
