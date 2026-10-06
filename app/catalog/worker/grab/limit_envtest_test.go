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
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/worker/grab"
	"github.com/mediactl/clustarr/app/indexer/limits"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/quality"
)

// fullIndexer creates an Indexer with spec.limits.grabLimit 1 and fills its
// grab window with one grab of another release made at `at`, so the window
// next has room at at + 24h + 1s.
func fullIndexer(t *testing.T, ctx context.Context, c client.Client, bus events.Bus, ns, name string, at time.Time) *indexv1alpha1.Indexer {
	t.Helper()
	idx := newIndexer(t, ctx, c, ns, name, nil)
	patch := client.MergeFrom(idx.DeepCopy())
	idx.Spec.Limits = &indexv1alpha1.Limits{GrabLimit: ptr.To[int32](1)}
	require.NoError(t, c.Patch(ctx, idx, patch))
	r, err := limits.ReserveGrab(ctx, bus.KV(events.BucketIndexerLimits), idx, "someone-elses-grab", at)
	require.NoError(t, err)
	require.True(t, r.Allowed)
	return idx
}

func grabsOn(t *testing.T, ctx context.Context, bus events.Bus, idx *indexv1alpha1.Indexer) int32 {
	t.Helper()
	u, err := limits.Grabs(ctx, bus.KV(events.BucketIndexerLimits), idx, testNow)
	require.NoError(t, err)
	return u.Count
}

func immediateSink(c client.Client, bus events.Bus, profile quality.Profile) grab.Sink {
	return grab.Sink{
		Deps:           grab.Deps{Client: c, Bus: bus, Now: fixedNow(testNow)},
		ResolveProfile: func(context.Context, string) (quality.Profile, error) { return profile, nil },
		ResolveDelay: func(context.Context, string, *string, []string) (catalogv1alpha1.DelayProfileSpec, error) {
			return catalogv1alpha1.DelayProfileSpec{}, nil
		},
	}
}

// spec.limits.grabLimit was never enforced. A grab now reserves on its
// Indexer's grab ring before the Download exists, and the best release on an
// indexer that is full is passed over for the next one on ANOTHER indexer --
// the full indexer's second release would be refused alike.
func TestSink_PassesOverAnIndexerAtItsGrabLimit(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ns := newNamespace(t, ctx, c)
	bus := newTestBus(t, nil)

	movie := newMovie(t, ctx, c, ns, "the-thing-1982")
	seedWorkerStatus(t, ctx, c, movie, "", nil)
	full := fullIndexer(t, ctx, c, bus, ns, "full", testNow.Add(-time.Hour))
	open := newIndexer(t, ctx, c, ns, "open", nil)
	profile := hdBlurayWeb(t)
	q := profile.Tiers[0][0].Quality

	err := immediateSink(c, bus, profile).Deliver(ctx, ns, commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: movie.Name},
		[]commonv1.ReleaseDecision{
			{ReleaseInfo: torrentRelease("guid-full-1", "full", q, 0), Approved: true, Rank: 0},
			{ReleaseInfo: torrentRelease("guid-full-2", "full", q, 0), Approved: true, Rank: 1},
			{ReleaseInfo: torrentRelease("guid-open", "open", q, 0), Approved: true, Rank: 2},
		}, "", "")
	require.NoError(t, err)

	var dls downloadv1alpha1.DownloadList
	require.NoError(t, c.List(ctx, &dls, client.InNamespace(ns)))
	require.Len(t, dls.Items, 1)
	assert.Equal(t, "guid-open", dls.Items[0].Spec.Release.GUID, "the full indexer's releases must be passed over")
	assert.Equal(t, int32(1), grabsOn(t, ctx, bus, full), "a refused grab is not counted")
	assert.Equal(t, int32(1), grabsOn(t, ctx, bus, open), "the grab is reserved on the indexer it came from")

	var got catalogv1alpha1.Movie
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(movie), &got))
	assert.Nil(t, got.Status.PendingGrab)
}

// When every approved release's indexer is full the grab is not dropped: it
// is held like a delayed grab, until the soonest of those windows has room,
// with that indexer's release -- and the scheduled delivery then grabs it.
func TestSink_HoldsTheGrabUntilAWindowHasRoomWhenEveryIndexerIsFull(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ns := newNamespace(t, ctx, c)
	bus := newTestBus(t, nil)

	movie := newMovie(t, ctx, c, ns, "the-thing-1982")
	seedWorkerStatus(t, ctx, c, movie, "", nil)
	fullIndexer(t, ctx, c, bus, ns, "later", testNow.Add(-time.Hour))    // room at testNow+23h+1s
	fullIndexer(t, ctx, c, bus, ns, "sooner", testNow.Add(-3*time.Hour)) // room at testNow+21h+1s
	profile := hdBlurayWeb(t)
	q := profile.Tiers[0][0].Quality
	target := commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: movie.Name}

	err := immediateSink(c, bus, profile).Deliver(ctx, ns, target,
		[]commonv1.ReleaseDecision{
			{ReleaseInfo: torrentRelease("guid-later", "later", q, 0), Approved: true, Rank: 0},
			{ReleaseInfo: torrentRelease("guid-sooner", "sooner", q, 0), Approved: true, Rank: 1},
		}, "", "")
	require.NoError(t, err)

	var dls downloadv1alpha1.DownloadList
	require.NoError(t, c.List(ctx, &dls, client.InNamespace(ns)))
	require.Empty(t, dls.Items, "no indexer had room")

	retryAt := testNow.Add(-3 * time.Hour).Add(24*time.Hour + time.Second)
	var got catalogv1alpha1.Movie
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(movie), &got))
	require.NotNil(t, got.Status.PendingGrab, "the grab must be held, not dropped")
	assert.True(t, got.Status.PendingGrab.GrabAt.Time.Equal(retryAt),
		"grabAt = %v, want the soonest window's room at %v", got.Status.PendingGrab.GrabAt.Time, retryAt)

	e, err := bus.KV(events.BucketPending).Get(ctx, events.PendingKey(grab.MediaKey(ns, target)))
	require.NoError(t, err)
	var pending struct {
		Release commonv1.ReleaseInfo `json:"release"`
	}
	require.NoError(t, json.Unmarshal(e.Value, &pending))
	assert.Equal(t, "guid-sooner", pending.Release.GUID, "the held release is the one whose indexer has room first")

	// The scheduled task fires once that window has room.
	h := grab.NewHandler(grab.Deps{Client: c, Bus: bus, Now: fixedNow(retryAt)})
	require.NoError(t, h.Handle(ctx, grabTaskMessage(t, ns, target, nil)))
	require.NoError(t, c.List(ctx, &dls, client.InNamespace(ns)))
	require.Len(t, dls.Items, 1)
	assert.Equal(t, "guid-sooner", dls.Items[0].Spec.Release.GUID)
}

// The scheduled path holds one candidate, so a full window there holds the
// grab again for the instant it has room, keeps the pending entry, and
// acknowledges: a nak would spend the consumer's deliveries on a window that
// may be a day long, then dead-letter a release nothing is wrong with.
func TestHandler_HoldsAGrabItsIndexersLimitRefuses(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ns := newNamespace(t, ctx, c)
	bus := newTestBus(t, nil)

	movie := newMovie(t, ctx, c, ns, "the-thing-1982")
	seedWorkerStatus(t, ctx, c, movie, "", nil)
	fullIndexer(t, ctx, c, bus, ns, "full", testNow.Add(-time.Hour))
	profile := hdBlurayWeb(t)
	target := commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: movie.Name}
	pendingKey := seedPending(t, ctx, bus, ns, target, nil,
		torrentRelease("guid-1", "full", profile.Tiers[0][0].Quality, 0), downloadv1alpha1.GrabSourceRSS)

	h := grab.NewHandler(grab.Deps{Client: c, Bus: bus, Now: fixedNow(testNow)})
	require.NoError(t, h.Handle(ctx, grabTaskMessage(t, ns, target, nil)), "a full window is held and acked, not retried")

	var dls downloadv1alpha1.DownloadList
	require.NoError(t, c.List(ctx, &dls, client.InNamespace(ns)))
	require.Empty(t, dls.Items)

	_, err := bus.KV(events.BucketPending).Get(ctx, pendingKey)
	require.NoError(t, err, "the held candidate must stay pending")

	var got catalogv1alpha1.Movie
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(movie), &got))
	require.NotNil(t, got.Status.PendingGrab)
	assert.True(t, got.Status.PendingGrab.GrabAt.Time.Equal(testNow.Add(23*time.Hour+time.Second)),
		"grabAt = %v", got.Status.PendingGrab.GrabAt.Time)
}

// The grab path's reservation is keyed by GUID, so the same release reserved
// by an earlier, failed attempt passes again rather than being refused by
// its own slot.
func TestPerformGrab_ARetriedGrabIsNotRefusedByItsOwnReservation(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ns := newNamespace(t, ctx, c)
	bus := newTestBus(t, nil)

	movie := newMovie(t, ctx, c, ns, "the-thing-1982")
	seedWorkerStatus(t, ctx, c, movie, "", nil)
	idx := newIndexer(t, ctx, c, ns, "one", nil)
	patch := client.MergeFrom(idx.DeepCopy())
	idx.Spec.Limits = &indexv1alpha1.Limits{GrabLimit: ptr.To[int32](1)}
	require.NoError(t, c.Patch(ctx, idx, patch))
	_, err := limits.ReserveGrab(ctx, bus.KV(events.BucketIndexerLimits), idx, "guid-1", testNow.Add(-time.Minute))
	require.NoError(t, err)

	profile := hdBlurayWeb(t)
	err = immediateSink(c, bus, profile).Deliver(ctx, ns, commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: movie.Name},
		[]commonv1.ReleaseDecision{{ReleaseInfo: torrentRelease("guid-1", "one", profile.Tiers[0][0].Quality, 0), Approved: true}}, "", "")
	require.NoError(t, err)

	var dls downloadv1alpha1.DownloadList
	require.NoError(t, c.List(ctx, &dls, client.InNamespace(ns)))
	require.Len(t, dls.Items, 1)
	assert.Equal(t, int32(1), grabsOn(t, ctx, bus, idx))
}
