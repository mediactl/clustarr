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
	"sigs.k8s.io/controller-runtime/pkg/client"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/worker/grab"
	"github.com/mediactl/clustarr/catalogarr/worker/grab/downloads"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// testMessage is the minimal events.Message Handle uses: it only reads
// Envelope(). Delivery mechanics (ack, nak, redelivery) are membus's and
// pkg/events' own contract tests; this suite is about what Handle decides.
type testMessage struct{ env *events.Envelope }

func (m testMessage) Envelope() *events.Envelope               { return m.env }
func (m testMessage) Subject() string                          { return "" }
func (m testMessage) Attempt() uint64                          { return 1 }
func (m testMessage) Ack(context.Context) error                { return nil }
func (m testMessage) Nak(context.Context, time.Duration) error { return nil }
func (m testMessage) Term(context.Context, string) error       { return nil }
func (m testMessage) InProgress(context.Context) error         { return nil }

func grabTaskMessage(t *testing.T, ns string, target commonv1.MediaRef, keys []string) events.Message {
	t.Helper()
	schemaName, data, err := schema.Encode(schema.GrabTask{MediaRef: target, Keys: keys})
	require.NoError(t, err)
	return testMessage{env: &events.Envelope{
		Type: "catalog.GrabTask", Schema: schemaName, Key: ns + "/" + target.Name, Time: testNow, Data: data,
	}}
}

// seedPending writes a pending candidate the way Decide would, using the same
// JSON shape Handle reads back. It goes through the KV directly rather than
// through Decide so this suite isolates the consumer half.
func seedPending(t *testing.T, ctx context.Context, bus events.Bus, ns string, target commonv1.MediaRef, keys []string, release commonv1.ReleaseInfo, grabbedBy downloadv1alpha1.GrabSource) string {
	t.Helper()
	key := events.PendingKey(grab.MediaKey(ns, target))
	data, err := json.Marshal(map[string]any{
		"target":    target,
		"keys":      keys,
		"release":   release,
		"grabbedBy": grabbedBy,
		"firstSeen": testNow,
	})
	require.NoError(t, err)
	_, err = bus.KV(events.BucketPending).Create(ctx, key, data)
	require.NoError(t, err)
	return key
}

// TestHandler_GrabsThePendingCandidateAndConsumesIt is the scheduled half of
// §8.2: the delivered GrabTask names only the media key, and the candidate to
// grab is re-read from clustarr-pending so a better release that arrived
// mid-window is the one grabbed.
func TestHandler_GrabsThePendingCandidateAndConsumesIt(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ns := newNamespace(t, ctx, c)

	movie := newMovie(t, ctx, c, ns, "the-thing-1982")
	newIndexer(t, ctx, c, ns, "my-indexer", nil)

	profile := hdBlurayWeb(t)
	bus := newTestBus(t, nil)
	target := commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: movie.Name}
	release := torrentRelease("guid-1", "my-indexer", profile.Tiers[0][0].Quality, 0)
	pendingKey := seedPending(t, ctx, bus, ns, target, nil, release, downloadv1alpha1.GrabSourceRSS)

	h := grab.NewHandler(grab.Deps{Client: c, Bus: bus, Now: fixedNow(testNow)})
	require.NoError(t, h.Handle(ctx, grabTaskMessage(t, ns, target, nil)))

	var downloadList downloadv1alpha1.DownloadList
	require.NoError(t, c.List(ctx, &downloadList, client.InNamespace(ns)))
	require.Len(t, downloadList.Items, 1)
	assert.Equal(t, downloadv1alpha1.GrabSourceRSS, downloadList.Items[0].Spec.GrabbedBy,
		"grabbedBy comes off the pending entry, not a hardcoded default")

	var got catalogv1alpha1.Movie
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(movie), &got))
	assert.Nil(t, got.Status.ActiveDownloadRef, "the reconciler derives activeDownloadRef from the Download (R-5), not the grab")
	assert.Nil(t, got.Status.PendingGrab, "a completed grab clears pendingGrab, or the item sits at Delayed forever")
	assert.Empty(t, got.Status.Phase)

	_, err := bus.KV(events.BucketPending).Get(ctx, pendingKey)
	assert.ErrorIs(t, err, events.ErrKeyNotFound, "the consumed pending entry is deleted")
}

// TestHandler_RedeliveryAfterTheEntryIsConsumedIsAnAck is at-least-once
// delivery's normal shape: the second delivery finds no pending entry and must
// acknowledge, not retry and not grab twice.
func TestHandler_RedeliveryAfterTheEntryIsConsumedIsAnAck(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ns := newNamespace(t, ctx, c)

	movie := newMovie(t, ctx, c, ns, "the-thing-1982")
	newIndexer(t, ctx, c, ns, "my-indexer", nil)

	profile := hdBlurayWeb(t)
	bus := newTestBus(t, nil)
	target := commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: movie.Name}
	seedPending(t, ctx, bus, ns, target, nil, torrentRelease("guid-1", "my-indexer", profile.Tiers[0][0].Quality, 0), downloadv1alpha1.GrabSourceSearch)

	h := grab.NewHandler(grab.Deps{Client: c, Bus: bus, Now: fixedNow(testNow)})
	msg := grabTaskMessage(t, ns, target, nil)
	require.NoError(t, h.Handle(ctx, msg))
	require.NoError(t, h.Handle(ctx, msg), "a redelivery must ack, not retry")

	var downloadList downloadv1alpha1.DownloadList
	require.NoError(t, c.List(ctx, &downloadList, client.InNamespace(ns)))
	assert.Len(t, downloadList.Items, 1, "a redelivery must not create a second Download")
}

// TestHandler_DuplicateGrabAcksAndClearsThePendingEntry: the lease is already
// held by someone else, so this delivery acks (no error, no retry) and drops
// the pending entry rather than leaving the item stuck at Phase=Delayed.
func TestHandler_DuplicateGrabAcksAndClearsThePendingEntry(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ns := newNamespace(t, ctx, c)

	movie := newMovie(t, ctx, c, ns, "the-thing-1982")
	profile := hdBlurayWeb(t)
	bus := newTestBus(t, nil)
	target := commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: movie.Name}
	pendingKey := seedPending(t, ctx, bus, ns, target, nil, torrentRelease("guid-1", "my-indexer", profile.Tiers[0][0].Quality, 0), downloadv1alpha1.GrabSourceSearch)

	_, err := bus.KV(events.BucketLeases).Create(ctx, events.LeaseKey(grab.MediaKey(ns, target)), []byte("someone-elses-download"))
	require.NoError(t, err)

	h := grab.NewHandler(grab.Deps{Client: c, Bus: bus, Now: fixedNow(testNow)})
	require.NoError(t, h.Handle(ctx, grabTaskMessage(t, ns, target, nil)), "a duplicate grab acks")

	var downloadList downloadv1alpha1.DownloadList
	require.NoError(t, c.List(ctx, &downloadList, client.InNamespace(ns)))
	assert.Empty(t, downloadList.Items)

	_, err = bus.KV(events.BucketPending).Get(ctx, pendingKey)
	assert.ErrorIs(t, err, events.ErrKeyNotFound)
}

func TestHandler_MalformedMessagesAreDiscarded(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	bus := newTestBus(t, nil)
	h := grab.NewHandler(grab.Deps{Client: c, Bus: bus, Now: fixedNow(testNow)})

	t.Run("wrong schema", func(t *testing.T) {
		err := h.Handle(ctx, testMessage{env: &events.Envelope{Schema: "catalog.SearchTask.v1", Key: "media/x", Data: []byte(`{}`)}})
		var discard *events.DiscardError
		require.ErrorAs(t, err, &discard)
	})
	t.Run("key without a namespace", func(t *testing.T) {
		msg := grabTaskMessage(t, "", commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "x"}, nil)
		msg.Envelope().Key = "no-slash"
		var discard *events.DiscardError
		require.ErrorAs(t, h.Handle(ctx, msg), &discard)
	})
	t.Run("corrupt pending entry", func(t *testing.T) {
		target := commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "corrupt"}
		_, err := bus.KV(events.BucketPending).Create(ctx, events.PendingKey(grab.MediaKey("media", target)), []byte("{not json"))
		require.NoError(t, err)
		var discard *events.DiscardError
		require.ErrorAs(t, h.Handle(ctx, grabTaskMessage(t, "media", target, nil)), &discard)
	})
}

// TestHandler_SubscriptionMatchesTheSpecTable pins §5's catalogarr-grab row so
// a change to the shared topology cannot silently retune this consumer.
func TestHandler_SubscriptionMatchesTheSpecTable(t *testing.T) {
	sub := grab.NewHandler(grab.Deps{}).Subscription()
	require.NoError(t, sub.Validate())
	assert.Equal(t, events.ConsumerCatalogGrab, sub.Durable)
	assert.Equal(t, []string{events.FilterCatalogGrab}, sub.Filters)
	assert.Equal(t, 60*time.Second, sub.AckWait)
	assert.Equal(t, 5, sub.MaxDeliver)
	assert.Equal(t, []time.Duration{10 * time.Second, time.Minute, 5 * time.Minute}, sub.Backoff)
	assert.Equal(t, 16, sub.MaxInFlight)
}

// TestHandler_DuplicateGrabClearsPendingGrab is the item that turned a
// duplicate ack into a permanent exit from automation.
//
// Acknowledging a duplicate used to delete the clustarr-pending entry and
// nothing else. status.pendingGrab stayed set, the Movie reconciler kept
// recomputing Phase=Delayed from it, and wantedcron's sweep only wakes Wanted
// and CutoffUnmet items -- so the movie stopped being searched for, forever,
// with no error anywhere.
//
// The movie is driven to the real delayed steady state first, because a blank
// object has no pendingGrab to strand.
func TestHandler_DuplicateGrabClearsPendingGrab(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ns := newNamespace(t, ctx, c)

	movie := newMovie(t, ctx, c, ns, "the-thing-1982")
	profile := hdBlurayWeb(t)
	bus := newTestBus(t, nil)
	target := commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: movie.Name}
	release := torrentRelease("guid-1", "my-indexer", profile.Tiers[0][0].Quality, 0)

	// The steady state a delayed grab leaves behind.
	seedWorkerStatus(t, ctx, c, movie, "", &catalogv1alpha1.PendingGrab{
		ReleaseTitle: release.Title,
		Protocol:     commonv1.ProtocolTorrent,
		GrabAt:       metav1.NewTime(testNow.Add(45 * time.Minute)),
	})
	pendingKey := seedPending(t, ctx, bus, ns, target, nil, release, downloadv1alpha1.GrabSourceSearch)

	// Something else grabbed it while the delay was running.
	_, err := bus.KV(events.BucketLeases).Create(ctx, events.LeaseKey(grab.MediaKey(ns, target)), []byte("someone-elses-download"))
	require.NoError(t, err)

	h := grab.NewHandler(grab.Deps{Client: c, Bus: bus, Now: fixedNow(testNow)})
	require.NoError(t, h.Handle(ctx, grabTaskMessage(t, ns, target, nil)), "a duplicate grab acks")

	var got catalogv1alpha1.Movie
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(movie), &got))
	assert.Nilf(t, got.Status.PendingGrab,
		"the duplicate ack left status.pendingGrab set: the movie reports Delayed forever and wantedcron never sweeps it again")

	_, err = bus.KV(events.BucketPending).Get(ctx, pendingKey)
	assert.ErrorIs(t, err, events.ErrKeyNotFound)
}

// TestPerformGrab_ExistingDownloadDuplicateClearsPendingGrab is the same
// guarantee on the OTHER duplicate exit: the lease was free, but a
// non-lease-mediated path already put a Download on the item.
func TestPerformGrab_ExistingDownloadDuplicateClearsPendingGrab(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ns := newNamespace(t, ctx, c)

	movie := newMovie(t, ctx, c, ns, "the-thing-1982")
	profile := hdBlurayWeb(t)
	target := commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: movie.Name}
	picked := torrentRelease("guid-user-picked", "my-indexer", profile.Tiers[1][0].Quality, 0)
	src, err := downloads.ResolveSource(picked)
	require.NoError(t, err)
	existing := interactiveDownload(t, ctx, c, movie, target, picked, src)
	seedWorkerStatus(t, ctx, c, movie, existing.Name, &catalogv1alpha1.PendingGrab{
		ReleaseTitle: "The.Thing.1982.1080p.BluRay.x264-GROUP",
		Protocol:     commonv1.ProtocolTorrent,
		GrabAt:       metav1.NewTime(testNow.Add(45 * time.Minute)),
	})

	deps := grab.Deps{Client: c, Bus: newTestBus(t, nil), Now: fixedNow(testNow)}
	err = grab.PerformGrabForTest(ctx, deps, ns, target, nil,
		torrentRelease("guid-1", "my-indexer", profile.Tiers[0][0].Quality, 0), downloadv1alpha1.GrabSourceSearch)
	require.ErrorIs(t, err, grab.ErrDuplicateGrab)

	var got catalogv1alpha1.Movie
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(movie), &got))
	assert.Nil(t, got.Status.PendingGrab, "the duplicate exit must clear pendingGrab")
	// The reconciler's field is untouched: the clear re-declares only the
	// grab manager's own set.
	require.NotNil(t, got.Status.ActiveDownloadRef)
	assert.Equal(t, existing.Name, *got.Status.ActiveDownloadRef)
}

// TestHandler_InteractiveGrabOfTheSameReleaseDoesNotStrandDelayed is the
// carried failure end to end. A delayed grab is waiting; meanwhile the user
// grabs the very same release from a Search CR's status.results. Both paths
// name the Download k8s.ChildName(target, guid), so when the scheduled grab
// fires the Download already exists -- made by the other path, with that
// path's source.
//
// The grab used to re-apply over it. spec.source is `self == oldSelf`, the
// two paths mapped a release differently, so the apiserver rejected the apply
// ("source is immutable"); performGrab returned before clearPendingGrab, the
// task retried into a dead letter, and the movie sat at Phase=Delayed with
// nothing left to move it. The pre-existing Download here deliberately
// carries a source the grab path would NOT produce for this release (the
// Search controller's old mapping, with no expectedInfoHash), so the test
// proves the guard alone is enough, whatever built the other Download.
func TestHandler_InteractiveGrabOfTheSameReleaseDoesNotStrandDelayed(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ns := newNamespace(t, ctx, c)

	movie := newMovie(t, ctx, c, ns, "the-thing-1982")
	newIndexer(t, ctx, c, ns, "my-indexer", nil)
	profile := hdBlurayWeb(t)
	bus := newTestBus(t, nil)
	target := commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: movie.Name}
	release := torrentRelease("guid-1", "my-indexer", profile.Tiers[0][0].Quality, 0)
	release.InfoHash = "0123456789abcdef0123456789abcdef01234567"

	// The delayed steady state.
	seedWorkerStatus(t, ctx, c, movie, "", &catalogv1alpha1.PendingGrab{
		ReleaseTitle: release.Title,
		Protocol:     commonv1.ProtocolTorrent,
		GrabAt:       metav1.NewTime(testNow.Add(45 * time.Minute)),
	})
	pendingKey := seedPending(t, ctx, bus, ns, target, nil, release, downloadv1alpha1.GrabSourceRSS)

	// The user's interactive grab of the same release, as the Search
	// controller built it before the two mappings were one.
	magnet := release.MagnetURL
	existing := interactiveDownload(t, ctx, c, movie, target, release, downloadv1alpha1.DownloadSource{MagnetURL: &magnet})
	resolved, err := downloads.ResolveSource(release)
	require.NoError(t, err)
	require.NotEqual(t, resolved, existing.Spec.Source, "the fixture must reproduce a source the grab path would not produce")

	h := grab.NewHandler(grab.Deps{Client: c, Bus: bus, Now: fixedNow(testNow)})
	require.NoError(t, h.Handle(ctx, grabTaskMessage(t, ns, target, nil)),
		"the scheduled grab of a release the user already grabbed is a duplicate to ack, not an apply to retry")

	var got catalogv1alpha1.Movie
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(movie), &got))
	assert.Nilf(t, got.Status.PendingGrab, "status.pendingGrab survived: the movie is stranded at Phase=Delayed")
	_, err = bus.KV(events.BucketPending).Get(ctx, pendingKey)
	assert.ErrorIs(t, err, events.ErrKeyNotFound, "the consumed pending entry is deleted")

	// The user's Download is exactly as the user's grab left it.
	var dl downloadv1alpha1.Download
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(existing), &dl))
	assert.Equal(t, downloadv1alpha1.GrabSourceInteractive, dl.Spec.GrabbedBy, "the grab path overwrote the user's provenance")
	assert.True(t, dl.Spec.Manual)
	assert.Equal(t, existing.Spec.Source, dl.Spec.Source)
	for _, mf := range dl.ManagedFields {
		assert.NotEqual(t, k8s.ManagerCatalogarrGrab.String(), mf.Manager, "the grab path applied over the user's Download")
	}
	var list downloadv1alpha1.DownloadList
	require.NoError(t, c.List(ctx, &list, client.InNamespace(ns)))
	assert.Len(t, list.Items, 1)
}
