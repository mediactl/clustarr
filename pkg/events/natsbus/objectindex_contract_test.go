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

package natsbus_test

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/contracttest"
	"github.com/mediactl/clustarr/pkg/events/natsbus"
)

// indexRig is a bus with the contract topology ensured, and a raw JetStream
// handle on the same server to see which stream a watch reads.
type indexRig struct {
	bus   *natsbus.Bus
	js    jetstream.JetStream
	store events.ObjectStore
}

func newIndexRig(ctx context.Context, t *testing.T) indexRig {
	t.Helper()
	srv := startServer(t)
	dial := func() *nats.Conn {
		nc, err := nats.Connect(srv.ClientURL())
		require.NoError(t, err)
		t.Cleanup(nc.Close)
		return nc
	}
	bus, err := natsbus.New(dial())
	require.NoError(t, err)
	t.Cleanup(func() { _ = bus.Close() })
	require.NoError(t, bus.Ensure(ctx, contracttest.Topology()))
	js, err := jetstream.New(dial())
	require.NoError(t, err)
	return indexRig{bus: bus, js: js, store: bus.ObjectStore(events.BucketArtwork)}
}

func (r indexRig) put(ctx context.Context, t *testing.T, name string, size int) events.ObjectInfo {
	t.Helper()
	info, err := r.store.Put(ctx, name, bytes.NewReader(bytes.Repeat([]byte{byte(len(name))}, size)),
		events.ObjectMeta{Headers: map[string]string{events.HeaderContentType: "image/jpeg"}, Metadata: map[string]string{"k": name}})
	require.NoError(t, err)
	return info
}

// consumers is how many consumers stream has.
func (r indexRig) consumers(ctx context.Context, t *testing.T, stream string) int {
	t.Helper()
	s, err := r.js.Stream(ctx, stream)
	require.NoError(t, err)
	info, err := s.Info(ctx)
	require.NoError(t, err)
	return info.State.Consumers
}

// replay reads ch to its Synced marker: the live objects by name, and the
// names delivered as tombstones.
func replay(t *testing.T, ch <-chan events.ObjectEvent) (live map[string]events.ObjectInfo, deleted map[string]bool) {
	t.Helper()
	live, deleted = map[string]events.ObjectInfo{}, map[string]bool{}
	for {
		select {
		case e, ok := <-ch:
			require.True(t, ok, "the watch closed before Synced")
			switch {
			case e.Synced:
				return live, deleted
			case e.Deleted:
				deleted[e.Info.Name] = true
				delete(live, e.Info.Name)
			default:
				live[e.Info.Name] = e.Info
			}
		case <-time.After(30 * time.Second):
			t.Fatal("no Synced marker within 30s")
		}
	}
}

func next(t *testing.T, ch <-chan events.ObjectEvent) events.ObjectEvent {
	t.Helper()
	select {
	case e, ok := <-ch:
		require.True(t, ok, "the watch closed")
		return e
	case <-time.After(15 * time.Second):
		t.Fatal("no event within 15s")
		return events.ObjectEvent{}
	}
}

// A bucket with a metadata index is replayed from the index stream: every
// live object with the digest, size, headers and metadata Info reports, a
// deleted one as a tombstone, then Synced, then live changes -- and no
// consumer is made on the bucket's own stream, whose replay costs the server
// a read of every chunk (talos, 2026-11-17).
func TestObjectWatchReplaysTheMetadataIndex(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	r := newIndexRig(ctx, t)

	const n = 40
	for i := range n {
		r.put(ctx, t, fmt.Sprintf("movie/uid-%02d/poster/original", i), 96*1024)
	}
	rewritten := r.put(ctx, t, "movie/uid-03/poster/original", 2048) // a second Put of one name
	require.NoError(t, r.store.SetMeta(ctx, "movie/uid-05/poster/original", events.ObjectMeta{
		Headers:  map[string]string{events.HeaderContentType: "image/png"},
		Metadata: map[string]string{"k": "set"},
	}))
	require.NoError(t, r.store.Delete(ctx, "movie/uid-07/poster/original"))

	wctx, stop := context.WithCancel(ctx)
	defer stop()
	ch, err := r.store.Watch(wctx)
	require.NoError(t, err)
	live, deleted := replay(t, ch)

	assert.Len(t, live, n-1)
	assert.True(t, deleted["movie/uid-07/poster/original"], "the deleted object's tombstone")
	for name, got := range live {
		want, err := r.store.Info(ctx, name)
		require.NoError(t, err, name)
		assert.Equal(t, want.Digest, got.Digest, name)
		assert.Equal(t, want.Size, got.Size, name)
		assert.Equal(t, want.Headers, got.Headers, name)
		assert.Equal(t, want.Metadata, got.Metadata, name)
	}
	assert.Equal(t, rewritten.Digest, live["movie/uid-03/poster/original"].Digest)
	assert.Equal(t, "set", live["movie/uid-05/poster/original"].Metadata["k"])

	assert.Equal(t, 1, r.consumers(ctx, t, events.ObjectMetaIndexStream(events.BucketArtwork)))
	assert.Zero(t, r.consumers(ctx, t, events.ObjectStoreStream(events.BucketArtwork)),
		"the watch must not read the bucket's own stream")

	added := r.put(ctx, t, "series/uid-new/fanart/original", 4096)
	e := next(t, ch)
	assert.Equal(t, "series/uid-new/fanart/original", e.Info.Name)
	assert.Equal(t, added.Digest, e.Info.Digest)

	require.NoError(t, r.store.Delete(ctx, "movie/uid-09/poster/original"))
	e = next(t, ch)
	assert.True(t, e.Deleted)
	assert.Equal(t, "movie/uid-09/poster/original", e.Info.Name)
}

// The index stream holds metadata records alone: a replay of it costs the
// records' size, not the bucket's.
func TestObjectMetadataIndexHoldsNoChunk(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	r := newIndexRig(ctx, t)
	for i := range 8 {
		r.put(ctx, t, fmt.Sprintf("movie/uid-%d/fanart/original", i), 512*1024)
	}
	wctx, stop := context.WithCancel(ctx)
	defer stop()
	ch, err := r.store.Watch(wctx) // returns once the index holds the bucket's latest record
	require.NoError(t, err)
	live, _ := replay(t, ch)
	require.Len(t, live, 8)

	s, err := r.js.Stream(ctx, events.ObjectMetaIndexStream(events.BucketArtwork))
	require.NoError(t, err)
	info, err := s.Info(ctx, jetstream.WithSubjectFilter(">"))
	require.NoError(t, err)
	assert.EqualValues(t, 8, info.State.Msgs)
	assert.Less(t, info.State.Bytes, uint64(64*1024), "8 records, none of the 4 MiB of chunks")
	for subject := range info.State.Subjects {
		assert.Contains(t, subject, ".M.", "only metadata subjects")
	}
}

// An empty bucket's watch is Synced at once, from the index.
func TestObjectWatchOfAnEmptyIndexedBucketSyncs(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	r := newIndexRig(ctx, t)
	wctx, stop := context.WithCancel(ctx)
	defer stop()
	ch, err := r.store.Watch(wctx)
	require.NoError(t, err)
	live, deleted := replay(t, ch)
	assert.Empty(t, live)
	assert.Empty(t, deleted)
	assert.Equal(t, 1, r.consumers(ctx, t, events.ObjectMetaIndexStream(events.BucketArtwork)))

	r.put(ctx, t, "movie/uid-1/poster/original", 1024)
	assert.Equal(t, "movie/uid-1/poster/original", next(t, ch).Info.Name)
}

// A bus whose manager has not made the index (an older manager, an index an
// operator deleted) watches the bucket itself, and answers the same.
func TestObjectWatchWithoutAnIndexReadsTheBucket(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	r := newIndexRig(ctx, t)
	for i := range 5 {
		r.put(ctx, t, fmt.Sprintf("movie/uid-%d/poster/original", i), 8192)
	}
	require.NoError(t, r.js.DeleteStream(ctx, events.ObjectMetaIndexStream(events.BucketArtwork)))

	wctx, stop := context.WithCancel(ctx)
	defer stop()
	ch, err := r.store.Watch(wctx)
	require.NoError(t, err)
	live, _ := replay(t, ch)
	assert.Len(t, live, 5)
	assert.Equal(t, 1, r.consumers(ctx, t, events.ObjectStoreStream(events.BucketArtwork)))

	// The watch outlives the deadline its creation ran under.
	r.put(ctx, t, "movie/uid-late/poster/original", 1024)
	assert.Equal(t, "movie/uid-late/poster/original", next(t, ch).Info.Name)
}

// An index older than its bucket indexes a bucket that was deleted and
// created again. A watch does not replay it, and the next Ensure builds it
// again from the new bucket.
func TestAStaleObjectMetadataIndexIsNotReplayedAndIsRebuilt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	r := newIndexRig(ctx, t)
	for i := range 6 {
		r.put(ctx, t, fmt.Sprintf("movie/old-%d/poster/original", i), 4096)
	}
	// Wait for the index to hold the six, so its source is past the new
	// bucket's first sequences.
	first, stopFirst := context.WithCancel(ctx)
	ch, err := r.store.Watch(first)
	require.NoError(t, err)
	live, _ := replay(t, ch)
	require.Len(t, live, 6)
	stopFirst()

	// The bucket is deleted and created again, by a bus of its own as an
	// operator's tool would; the index is left as it was.
	time.Sleep(10 * time.Millisecond) // creation times a clock tick apart
	require.NoError(t, r.js.DeleteObjectStore(ctx, events.BucketArtwork))
	top := contracttest.Topology()
	spec, ok := top.ObjectStore(events.BucketArtwork)
	require.True(t, ok)
	_, err = r.js.CreateObjectStore(ctx, events.ObjectStoreConfig(spec))
	require.NoError(t, err)

	other, err := natsbus.New(reconnect(t, r))
	require.NoError(t, err)
	t.Cleanup(func() { _ = other.Close() })
	store := other.ObjectStore(events.BucketArtwork)
	_, err = store.Put(ctx, "movie/new-1/poster/original", bytes.NewReader([]byte("new")), events.ObjectMeta{})
	require.NoError(t, err)

	stale, stopStale := context.WithCancel(ctx)
	ch, err = store.Watch(stale)
	require.NoError(t, err)
	live, _ = replay(t, ch)
	stopStale()
	require.Len(t, live, 1, "the old bucket's six records must not be replayed")
	assert.Contains(t, live, "movie/new-1/poster/original")

	require.NoError(t, other.Ensure(ctx, top))
	rebuilt, stopRebuilt := context.WithCancel(ctx)
	defer stopRebuilt()
	ch, err = store.Watch(rebuilt)
	require.NoError(t, err)
	live, _ = replay(t, ch)
	require.Len(t, live, 1)
	assert.Contains(t, live, "movie/new-1/poster/original")
	assert.Equal(t, 1, r.consumers(ctx, t, events.ObjectMetaIndexStream(events.BucketArtwork)),
		"after Ensure the watch reads the rebuilt index")
}

// reconnect dials the rig's server again.
func reconnect(t *testing.T, r indexRig) *nats.Conn {
	t.Helper()
	nc, err := nats.Connect(r.js.Conn().ConnectedUrl())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	return nc
}

// On a three-server cluster the index is an R3 stream the servers fill
// between themselves, and a watch replays it from whichever server it lands
// on.
func TestObjectWatchReplaysTheMetadataIndexOnACluster(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	bus, nc := clusterBus(t)
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	// The bucket and its index as Ensure makes them, without the rest of the
	// topology: the bus reads the bucket's spec from the default topology.
	spec, ok := events.Default().ObjectStore(events.BucketArtwork)
	require.True(t, ok)
	spec.MaxBytes = 256 * events.MiB
	require.Eventually(t, func() bool {
		_, err := js.CreateOrUpdateObjectStore(callCtx(ctx), events.ObjectStoreConfig(spec))
		return err == nil
	}, 60*time.Second, 500*time.Millisecond, "create the bucket")
	require.Eventually(t, func() bool {
		_, err := js.CreateOrUpdateStream(callCtx(ctx), events.ObjectMetaIndexConfig(spec))
		return err == nil
	}, 60*time.Second, 500*time.Millisecond, "create the index")
	index, err := js.Stream(ctx, events.ObjectMetaIndexStream(events.BucketArtwork))
	require.NoError(t, err)
	assert.Equal(t, 3, index.CachedInfo().Config.Replicas)

	store := bus.ObjectStore(events.BucketArtwork)
	const n = 25
	for i := range n {
		_, err := store.Put(ctx, fmt.Sprintf("series/uid-%02d/poster/original", i),
			bytes.NewReader(bytes.Repeat([]byte{byte(i)}, 200*1024)), events.ObjectMeta{})
		require.NoError(t, err)
	}
	for range 3 { // three watches, as three ui replicas open at once
		wctx, stop := context.WithCancel(ctx)
		ch, err := store.Watch(wctx)
		require.NoError(t, err)
		live, _ := replay(t, ch)
		stop()
		assert.Len(t, live, n)
	}
	bucket, err := js.Stream(ctx, events.ObjectStoreStream(events.BucketArtwork))
	require.NoError(t, err)
	info, err := bucket.Info(ctx)
	require.NoError(t, err)
	assert.Zero(t, info.State.Consumers, "no watch read the bucket's own stream")
}
