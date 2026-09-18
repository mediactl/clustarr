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

package rssmatcher_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/worker/rssmatcher"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// testMessage is the minimal events.Message Handle uses: it only reads
// Envelope(). Delivery mechanics are membus's and pkg/events' own contract
// tests.
type testMessage struct{ env *events.Envelope }

func (m testMessage) Envelope() *events.Envelope               { return m.env }
func (m testMessage) Subject() string                          { return "" }
func (m testMessage) Attempt() uint64                          { return 1 }
func (m testMessage) Ack(context.Context) error                { return nil }
func (m testMessage) Nak(context.Context, time.Duration) error { return nil }
func (m testMessage) Term(context.Context, string) error       { return nil }
func (m testMessage) InProgress(context.Context) error         { return nil }

func releaseMessage(t *testing.T, ns, indexer string, rel schema.Release) events.Message {
	t.Helper()
	schemaName, data, err := schema.Encode(rel)
	require.NoError(t, err)
	return testMessage{env: &events.Envelope{
		Type: "index.Release", Schema: schemaName, Key: ns + "/" + indexer, Time: relNow, Data: data,
	}}
}

// blurayRelease is a top-tier hd-bluray-web candidate for the seeded movie.
func blurayRelease(tmdbID string) schema.Release {
	rel := movieRelease("The Thing", 1982, map[string]string{commonv1.IDKeyTMDB: tmdbID})
	rel.Info.Quality = commonv1.Quality{Name: "Bluray-1080p", Source: commonv1.SourceBluray, Resolution: 1080}
	return rel
}

// TestHandler_ApprovedReleaseTakesTheGrabPath is §8.7's clause end to end: a
// matched, approved release goes through grab.Decide -- the same entry point a
// search's sink uses -- and comes out as a Download plus
// status.activeDownloadRef.
func TestHandler_ApprovedReleaseTakesTheGrabPath(t *testing.T) {
	ctx := context.Background()
	mgr := newTestManager(t)
	c := mgr.GetClient()
	ns := newNamespace(t, ctx, c)

	movie := createMovie(t, ctx, c, ns, "the-thing-1982", 1091, "The Thing", 1982)
	createQualityProfile(t, ctx, c)
	createIndexer(t, ctx, c, ns, "my-indexer")
	createDelayProfile(t, ctx, c, ns, 0, true) // no delay at all: grab now

	bus := newTestBus(t)
	h := rssmatcher.NewHandler(rssmatcher.Deps{Client: c, Bus: bus, Now: func() time.Time { return relNow }})

	// The manager's cache is eventually consistent, so poll until the write
	// is visible to the index rather than racing it.
	eventually(t, 15*time.Second, "the release to be matched and grabbed", func() bool {
		if err := h.Handle(ctx, releaseMessage(t, ns, "my-indexer", blurayRelease("1091"))); err != nil {
			t.Fatalf("Handle: %v", err)
		}
		var downloads downloadv1alpha1.DownloadList
		return c.List(ctx, &downloads, client.InNamespace(ns)) == nil && len(downloads.Items) == 1
	})

	var downloads downloadv1alpha1.DownloadList
	require.NoError(t, c.List(ctx, &downloads, client.InNamespace(ns)))
	require.Len(t, downloads.Items, 1)
	dl := downloads.Items[0]
	assert.Equal(t, downloadv1alpha1.GrabSourceRSS, dl.Spec.GrabbedBy, "an RSS grab must be attributed to rss")
	assert.Equal(t, "guid-1", dl.Spec.Release.GUID)
	assert.Equal(t, "hd-bluray-web", dl.Spec.QualityProfileRef)
	require.Len(t, dl.OwnerReferences, 1)
	assert.Equal(t, movie.Name, dl.OwnerReferences[0].Name)

	// The client behind a manager is a cache, so the status write this grab
	// just made is only eventually visible through it.
	var got catalogv1alpha1.Movie
	eventually(t, 10*time.Second, "the movie's activeDownloadRef to appear", func() bool {
		return c.Get(ctx, client.ObjectKeyFromObject(movie), &got) == nil && got.Status.ActiveDownloadRef != nil
	})
	assert.Equal(t, dl.Name, *got.Status.ActiveDownloadRef)
	assert.Empty(t, got.Status.Phase, "the RSS path must never write Phase")
	require.NotNil(t, got.Status.Metadata, "the grab must not release the gateway's status.metadata")
}

// TestHandler_DelayProfileHoldsTheGrab: the same release under a profile with
// a torrent delay and no top-tier bypass records pendingGrab instead of
// creating a Download -- proving the RSS path really does go through
// grab.Decide and not around it.
func TestHandler_DelayProfileHoldsTheGrab(t *testing.T) {
	ctx := context.Background()
	mgr := newTestManager(t)
	c := mgr.GetClient()
	ns := newNamespace(t, ctx, c)

	movie := createMovie(t, ctx, c, ns, "the-thing-1982", 1091, "The Thing", 1982)
	createQualityProfile(t, ctx, c)
	createIndexer(t, ctx, c, ns, "my-indexer")
	createDelayProfile(t, ctx, c, ns, 45, false)

	bus := newTestBus(t)
	h := rssmatcher.NewHandler(rssmatcher.Deps{Client: c, Bus: bus, Now: func() time.Time { return relNow }})

	eventually(t, 15*time.Second, "the release to be held by the delay profile", func() bool {
		if err := h.Handle(ctx, releaseMessage(t, ns, "my-indexer", blurayRelease("1091"))); err != nil {
			t.Fatalf("Handle: %v", err)
		}
		var got catalogv1alpha1.Movie
		return c.Get(ctx, client.ObjectKeyFromObject(movie), &got) == nil && got.Status.PendingGrab != nil
	})

	var got catalogv1alpha1.Movie
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(movie), &got))
	require.NotNil(t, got.Status.PendingGrab)
	assert.Equal(t, commonv1.ProtocolTorrent, got.Status.PendingGrab.Protocol)
	assert.True(t, got.Status.PendingGrab.GrabAt.Time.Equal(relNow.Add(45*time.Minute)),
		"grabAt = %v, want %v", got.Status.PendingGrab.GrabAt.Time, relNow.Add(45*time.Minute))
	assert.Empty(t, got.Status.Phase, "the RSS path must never write Phase")

	var downloads downloadv1alpha1.DownloadList
	require.NoError(t, c.List(ctx, &downloads, client.InNamespace(ns)))
	assert.Empty(t, downloads.Items, "a delayed grab creates no Download yet")

	// And the candidate landed in clustarr-pending, where the scheduled
	// GrabTask will re-read it.
	_, err := bus.KV(events.BucketPending).Get(ctx,
		events.PendingKey(events.MediaKey(string(commonv1.MediaKindMovie), ns, movie.Name)))
	assert.NoError(t, err)
}

// TestHandler_BlocklistedReleaseIsRejected proves the decision engine really
// sees the live blocklist through the search worker's exported indexes: a
// labelled Download for the same info hash stops the grab.
func TestHandler_BlocklistedReleaseIsRejected(t *testing.T) {
	ctx := context.Background()
	mgr := newTestManager(t)
	c := mgr.GetClient()
	ns := newNamespace(t, ctx, c)

	movie := createMovie(t, ctx, c, ns, "the-thing-1982", 1091, "The Thing", 1982)
	createQualityProfile(t, ctx, c)
	createIndexer(t, ctx, c, ns, "my-indexer")
	createDelayProfile(t, ctx, c, ns, 0, true)

	rel := blurayRelease("1091")
	rel.Info.InfoHash = "0123456789abcdef0123456789abcdef01234567"

	blocked := &downloadv1alpha1.Download{
		ObjectMeta: metav1.ObjectMeta{
			Name: "blocked-one", Namespace: ns,
			Labels: map[string]string{downloadv1alpha1.LabelBlocklisted: downloadv1alpha1.LabelBlocklistedValue},
		},
		Spec: downloadv1alpha1.DownloadSpec{
			Protocol: commonv1.ProtocolTorrent,
			Source:   downloadv1alpha1.DownloadSource{MagnetURL: ptrTo(rel.Info.MagnetURL)},
			Release:  rel.Info,
			Target:   commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: movie.Name},
		},
	}
	require.NoError(t, c.Create(ctx, blocked))

	bus := newTestBus(t)
	h := rssmatcher.NewHandler(rssmatcher.Deps{Client: c, Bus: bus, Now: func() time.Time { return relNow }})

	eventually(t, 15*time.Second, "the blocklist index to see the blocked Download", func() bool {
		var list downloadv1alpha1.DownloadList
		return c.List(ctx, &list, client.InNamespace(ns),
			client.MatchingFields{"search.clustarr.io/blocklist-infohash": rel.Info.InfoHash}) == nil && len(list.Items) == 1
	})

	require.NoError(t, h.Handle(ctx, releaseMessage(t, ns, "my-indexer", rel)))

	var downloads downloadv1alpha1.DownloadList
	require.NoError(t, c.List(ctx, &downloads, client.InNamespace(ns)))
	assert.Len(t, downloads.Items, 1, "only the pre-existing blocked Download may be there")

	var got catalogv1alpha1.Movie
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(movie), &got))
	assert.Nil(t, got.Status.PendingGrab)
	assert.Nil(t, got.Status.ActiveDownloadRef)
}

// TestHandler_UnmatchedReleaseIsAcknowledged: most of the firehose matches
// nothing, and that path must ack without a single write.
func TestHandler_UnmatchedReleaseIsAcknowledged(t *testing.T) {
	ctx := context.Background()
	mgr := newTestManager(t)
	c := mgr.GetClient()
	ns := newNamespace(t, ctx, c)

	bus := newTestBus(t)
	h := rssmatcher.NewHandler(rssmatcher.Deps{Client: c, Bus: bus, Now: func() time.Time { return relNow }})
	require.NoError(t, h.Handle(ctx, releaseMessage(t, ns, "my-indexer", blurayRelease("999999"))))

	var downloads downloadv1alpha1.DownloadList
	require.NoError(t, c.List(ctx, &downloads, client.InNamespace(ns)))
	assert.Empty(t, downloads.Items)
}

func TestHandler_MalformedMessagesAreDiscarded(t *testing.T) {
	ctx := context.Background()
	mgr := newTestManager(t)
	c := mgr.GetClient()
	h := rssmatcher.NewHandler(rssmatcher.Deps{Client: c, Bus: newTestBus(t), Now: func() time.Time { return relNow }})

	t.Run("wrong schema", func(t *testing.T) {
		err := h.Handle(ctx, testMessage{env: &events.Envelope{Schema: "catalog.GrabTask.v1", Key: "media/x", Data: []byte(`{}`)}})
		var discard *events.DiscardError
		require.ErrorAs(t, err, &discard)
	})
	t.Run("key without a namespace", func(t *testing.T) {
		msg := releaseMessage(t, "media", "my-indexer", blurayRelease("1"))
		msg.Envelope().Key = "no-slash"
		var discard *events.DiscardError
		require.ErrorAs(t, h.Handle(ctx, msg), &discard)
	})
}

// TestHandler_SubscriptionMatchesTheSpecTable pins §5's
// catalogarr-rss-matcher row so a change to the shared topology cannot
// silently retune this consumer.
func TestHandler_SubscriptionMatchesTheSpecTable(t *testing.T) {
	sub := rssmatcher.NewHandler(rssmatcher.Deps{}).Subscription()
	require.NoError(t, sub.Validate())
	assert.Equal(t, events.ConsumerCatalogRSSMatcher, sub.Durable)
	assert.Equal(t, []string{events.FilterAllReleases}, sub.Filters)
	assert.Equal(t, 30*time.Second, sub.AckWait)
	assert.Equal(t, 6, sub.MaxDeliver)
	assert.Equal(t, []time.Duration{time.Second, 5 * time.Second, 30 * time.Second, 2 * time.Minute, 10 * time.Minute}, sub.Backoff)
	assert.Equal(t, 256, sub.MaxInFlight)
}

func ptrTo[T any](v T) *T { return &v }
