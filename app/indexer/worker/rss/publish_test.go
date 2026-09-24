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

package rss_test

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/worker/rssmatcher"
	"github.com/mediactl/clustarr/app/indexer/worker/rss"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/natsbus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/newznab"
	"github.com/mediactl/clustarr/pkg/torznab"
)

// startServer boots an embedded JetStream server with its store under the
// test's temporary directory, so the suite needs no external broker. It is
// pkg/events/natsbus/natsbus_test.go's helper, verbatim.
func startServer(t *testing.T) *natsserver.Server {
	t.Helper()
	dir, err := os.MkdirTemp(t.TempDir(), "jetstream")
	require.NoError(t, err)
	srv, err := natsserver.NewServer(&natsserver.Options{
		ServerName: "clustarr-rss",
		Host:       "127.0.0.1",
		Port:       -1,
		JetStream:  true,
		StoreDir:   dir,
		NoLog:      true,
		NoSigs:     true,
	})
	require.NoError(t, err)
	go srv.Start()
	if !srv.ReadyForConnections(20 * time.Second) {
		t.Fatal("embedded NATS server did not become ready")
	}
	t.Cleanup(srv.Shutdown)
	return srv
}

// newJetStreamBus returns a bus over an embedded JetStream server with the
// production topology applied. membus has no deduplication window and no
// subject-to-stream routing, so only this proves the dedup accounting and
// only this proves a published release is deliverable to the SHIPPED
// consumer definition.
func newJetStreamBus(t *testing.T) events.Bus {
	t.Helper()
	bus, _ := newJetStreamBusAndConn(t)
	return bus
}

// newJetStreamBusAndConn also hands back the connection, for the few
// assertions that read the stream's own state rather than consuming it.
func newJetStreamBusAndConn(t *testing.T) (events.Bus, *nats.Conn) {
	t.Helper()
	srv := startServer(t)
	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)

	bus, err := natsbus.New(nc)
	require.NoError(t, err)
	t.Cleanup(func() { _ = bus.Close() })

	require.NoError(t, bus.Ensure(t.Context(), events.Default().ForSingleNode()))
	return bus, nc
}

func TestPublishReleasesEnvelopeKeyIsNamespaceSlashIndexerName(t *testing.T) {
	bus := membus.New(nil)
	require.NoError(t, bus.Ensure(t.Context(), events.Default().ForSingleNode()))
	t.Cleanup(func() { _ = bus.Close() })

	var mu sync.Mutex
	var got []*events.Envelope
	stop, err := bus.Subscribe(t.Context(), events.Subscription{
		Stream: events.StreamReleases, Durable: "test", Filters: []string{events.FilterAllReleases},
	}, func(_ context.Context, m events.Message) error {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, m.Envelope().Clone())
		return nil
	})
	require.NoError(t, err)
	t.Cleanup(stop)

	rels := []schema.Release{rss.ProjectRelease(torznab.Release{
		Title: "The.Matrix.1999.1080p-G", GUID: "g1", Categories: []newznab.CategoryID{2040},
	}, "my-indexer", "torrent")}

	n, err := rss.PublishReleases(t.Context(), bus, "media", "my-indexer", rels)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(got) == 1
	}, 5*time.Second, 10*time.Millisecond)

	// Exactly what catalogarr/worker/rssmatcher/handler.go does.
	ns, indexer, ok := strings.Cut(got[0].Key, "/")
	require.True(t, ok,
		"key %q has no slash: the matcher Discards it STRAIGHT TO THE DLQ, bypassing MaxDeliver", got[0].Key)
	require.Equal(t, "media", ns)
	require.Equal(t, "my-indexer", indexer)
	require.Equal(t, "media/my-indexer", got[0].Key)

	// And the negative: a media key is not an envelope key. If someone ever
	// swaps one in, this is the assertion that catches it.
	require.NotEqual(t, events.MediaKey("movie", "media", "my-indexer"), got[0].Key)
	require.NotContains(t, events.MediaKey("movie", "media", "my-indexer"), "/")
}

// TestPublishReleasesRefusesAnUncuttableKey is the same requirement from the
// producer's side: rather than emit a key the matcher would dead-letter
// without a retry, refuse before anything is published.
func TestPublishReleasesRefusesAnUncuttableKey(t *testing.T) {
	bus := membus.New(nil)
	require.NoError(t, bus.Ensure(t.Context(), events.Default().ForSingleNode()))
	t.Cleanup(func() { _ = bus.Close() })

	rels := []schema.Release{rss.ProjectRelease(torznab.Release{Title: "A.2026-G", GUID: "g"}, "idx", "torrent")}

	for _, tt := range []struct{ name, ns, idx string }{
		{"no namespace", "", "idx"},
		{"no indexer", "media", ""},
		{"neither", "", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			n, err := rss.PublishReleases(t.Context(), bus, tt.ns, tt.idx, rels)
			require.Error(t, err)
			require.Zero(t, n, "nothing may be published under a key the matcher cannot Cut")
		})
	}
}

func TestSubjectUsesTheAlignedParentCategory(t *testing.T) {
	tests := []struct {
		name string
		cats []newznab.CategoryID
		want string
	}{
		{"2040 aligns to 2000", []newznab.CategoryID{2040}, events.ReleaseSubject("torrent", "idx", 2000)},
		{"first category wins", []newznab.CategoryID{5030, 2040}, events.ReleaseSubject("torrent", "idx", 5000)},
		{"a custom id is its own parent", []newznab.CategoryID{100042}, events.ReleaseSubject("torrent", "idx", 100042)},
		{"no category at all", nil, events.ReleaseSubject("torrent", "idx", 0)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bus := membus.New(nil)
			require.NoError(t, bus.Ensure(t.Context(), events.Default().ForSingleNode()))
			t.Cleanup(func() { _ = bus.Close() })

			var mu sync.Mutex
			var subjects []string
			stop, err := bus.Subscribe(t.Context(), events.Subscription{
				Stream: events.StreamReleases, Durable: "test", Filters: []string{events.FilterAllReleases},
			}, func(_ context.Context, m events.Message) error {
				mu.Lock()
				defer mu.Unlock()
				subjects = append(subjects, m.Subject())
				return nil
			})
			require.NoError(t, err)
			t.Cleanup(stop)

			rel := rss.ProjectRelease(torznab.Release{
				Title: "A.2026.1080p-G", GUID: "g", Categories: tt.cats,
			}, "idx", "torrent")
			_, err = rss.PublishReleases(t.Context(), bus, "media", "idx", []schema.Release{rel})
			require.NoError(t, err)

			require.Eventually(t, func() bool {
				mu.Lock()
				defer mu.Unlock()
				return len(subjects) == 1
			}, 5*time.Second, 10*time.Millisecond)
			require.Equal(t, tt.want, subjects[0])
			require.True(t, events.SubjectMatches(events.FilterAllReleases, subjects[0]),
				"the matcher subscribes to clustarr.rel.> and must still see it")
		})
	}
}

func TestPublishReleasesDedupsOnIndexerAndGUID(t *testing.T) {
	bus := newJetStreamBus(t)
	rels := []schema.Release{rss.ProjectRelease(
		torznab.Release{Title: "A.2026.1080p-G", GUID: "same-guid", Categories: []newznab.CategoryID{2040}},
		"idx", "torrent")}

	first, err := rss.PublishReleases(t.Context(), bus, "media", "idx", rels)
	require.NoError(t, err)
	require.Equal(t, 1, first)

	// Re-reading the same RSS page is the normal case at a 15m interval on a
	// slow-moving feed. CLUSTARR_RELEASES has Duplicates: 2h, so the second
	// publish stores nothing and the matcher never sees it twice.
	second, err := rss.PublishReleases(t.Context(), bus, "media", "idx", rels)
	require.NoError(t, err)
	require.Equal(t, 0, second, "a duplicate is a stored-nothing, not an error")

	// The same guid from a DIFFERENT indexer is a different release.
	other, err := rss.PublishReleases(t.Context(), bus, "media", "idx2",
		[]schema.Release{rss.ProjectRelease(torznab.Release{
			Title: "A.2026.1080p-G", GUID: "same-guid", Categories: []newznab.CategoryID{2040},
		}, "idx2", "torrent")})
	require.NoError(t, err)
	require.Equal(t, 1, other)
}

func TestPublishedReleaseReachesTheShippedMatcherSubscription(t *testing.T) {
	bus := newJetStreamBus(t)

	// Not a subscription written for this test: the one the shipped handler
	// asks for. If the tuning or the filter ever moves, this moves with it.
	sub := (&rssmatcher.Handler{}).Subscription()
	require.Equal(t, events.StreamReleases, sub.Stream)

	var mu sync.Mutex
	var seen []*events.Envelope
	stop, err := bus.Subscribe(t.Context(), sub, func(_ context.Context, m events.Message) error {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, m.Envelope().Clone())
		return nil
	})
	require.NoError(t, err)
	t.Cleanup(stop)

	// A span must be live for tracing.Inject to have anything to carry;
	// TestMain installs the provider.
	ctx, span := startSpan(t)
	defer span.End()

	rel := rss.ProjectRelease(torznab.Release{
		Title: "The.Matrix.1999.1080p.BluRay.x264-GROUP", GUID: "g1",
		Categories: []newznab.CategoryID{2040}, IDs: map[string]string{"tmdb": "603"},
	}, "my-indexer", "torrent")
	_, err = rss.PublishReleases(ctx, bus, "media", "my-indexer", []schema.Release{rel})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(seen) == 1
	}, 20*time.Second, 20*time.Millisecond)

	env := seen[0]
	ns, _, ok := strings.Cut(env.Key, "/")
	require.True(t, ok)
	require.Equal(t, "media", ns)

	var out schema.Release
	require.NoError(t, schema.Decode(env.Schema, env.Data, &out))
	require.Equal(t, commonv1.MediaKindMovie, out.Kind, "the matcher dispatches on this first")
	require.Equal(t, "603", out.Info.IDs["tmdb"], "the matcher keys movies on this")
	require.Equal(t, "my-indexer", out.Info.IndexerRef, "the matcher looks up priority by this")
	require.NotEmpty(t, out.ParsedTitle)
	require.NotZero(t, out.Info.Title)
	require.NotEmpty(t, env.Trace, "tracing.Inject must run or the matcher's span starts a new trace")
}

// TestPublishedSeriesReleaseCarriesTheEpisodeFieldsTheMatcherReads covers the
// other half of the matcher's dispatch: MultiSeason+Seasons, Episodes,
// FullSeason and AirDate all steer Match, and all come from the projection.
func TestPublishedSeriesReleaseCarriesTheEpisodeFieldsTheMatcherReads(t *testing.T) {
	bus := newJetStreamBus(t)

	var mu sync.Mutex
	var seen []*events.Envelope
	stop, err := bus.Subscribe(t.Context(), (&rssmatcher.Handler{}).Subscription(),
		func(_ context.Context, m events.Message) error {
			mu.Lock()
			defer mu.Unlock()
			seen = append(seen, m.Envelope().Clone())
			return nil
		})
	require.NoError(t, err)
	t.Cleanup(stop)

	rel := rss.ProjectRelease(torznab.Release{
		Title: "Some.Show.S02E05.1080p.WEB-DL.x265-GRP", GUID: "s1",
		Categories: []newznab.CategoryID{5030}, IDs: map[string]string{"tvdb": "121361"},
	}, "my-indexer", "torrent")
	_, err = rss.PublishReleases(t.Context(), bus, "media", "my-indexer", []schema.Release{rel})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(seen) == 1
	}, 20*time.Second, 20*time.Millisecond)

	var out schema.Release
	require.NoError(t, schema.Decode(seen[0].Schema, seen[0].Data, &out))
	require.Equal(t, commonv1.MediaKindEpisode, out.Kind)
	require.Equal(t, "121361", out.Info.IDs["tvdb"])
	require.Equal(t, []int32{2}, out.Seasons)
	require.Equal(t, []int32{5}, out.Episodes)
	require.False(t, out.FullSeason)
	require.False(t, out.MultiSeason)
}
