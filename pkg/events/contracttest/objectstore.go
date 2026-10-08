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
	"fmt"
	"io"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/mediactl/clustarr/pkg/events"
)

// mustPut writes body under name with meta, failing the test on an error.
func mustPut(ctx context.Context, t *testing.T, store events.ObjectStore, name, body string, meta events.ObjectMeta) events.ObjectInfo {
	t.Helper()
	info, err := store.Put(ctx, name, strings.NewReader(body), meta)
	if err != nil {
		t.Fatalf("Put %s: %v", name, err)
	}
	return info
}

// mustInfo reads name's info, failing the test on an error.
func mustInfo(ctx context.Context, t *testing.T, store events.ObjectStore, name string) events.ObjectInfo {
	t.Helper()
	info, err := store.Info(ctx, name)
	if err != nil {
		t.Fatalf("Info %s: %v", name, err)
	}
	return info
}

// testObjectMetadataRoundTrip: the metadata and headers a Put sends come back
// from the Put and from Info (artwork design §B.1, §B.2 as amended).
func testObjectMetadataRoundTrip(t *testing.T, newBus func() events.Bus) {
	ctx, bus := setup(t, newBus)
	store := bus.ObjectStore(events.BucketArtwork)
	meta := events.ObjectMeta{
		Headers:  map[string]string{"Content-Type": "image/png"},
		Metadata: map[string]string{events.ArtworkMetaKeyUID: "uid-1", events.ArtworkMetaKeyVersion: events.ArtworkMetaVersion},
	}
	put := mustPut(ctx, t, store, "movie/uid-1/poster/original", "png", meta)
	for _, info := range []events.ObjectInfo{put, mustInfo(ctx, t, store, "movie/uid-1/poster/original")} {
		if !maps.Equal(info.Metadata, meta.Metadata) || info.Headers["Content-Type"] != "image/png" {
			t.Errorf("info %+v, want the metadata and headers Put sent", info)
		}
	}
	listed, err := store.List(ctx, "movie/uid-1/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listed) != 1 || !maps.Equal(listed[0].Metadata, meta.Metadata) {
		t.Errorf("List = %+v, want the one object with its metadata", listed)
	}
}

// testObjectPutReplacesMetadata: E3 -- a key SetMeta added is gone after a
// Put that omits it. Every Put is a complete declaration.
func testObjectPutReplacesMetadata(t *testing.T, newBus func() events.Bus) {
	ctx, bus := setup(t, newBus)
	store := bus.ObjectStore(events.BucketArtwork)
	name := "movie/uid-2/poster/original"
	mustPut(ctx, t, store, name, "v1", events.ObjectMeta{Metadata: map[string]string{"a": "1"}})
	if err := store.SetMeta(ctx, name, events.ObjectMeta{Metadata: map[string]string{"a": "1", "b": "2"}}); err != nil {
		t.Fatalf("SetMeta: %v", err)
	}
	if got := mustInfo(ctx, t, store, name).Metadata; got["b"] != "2" {
		t.Fatalf("metadata %v after SetMeta added b, want b=2", got)
	}
	mustPut(ctx, t, store, name, "v2", events.ObjectMeta{Metadata: map[string]string{"a": "1"}})
	if got := mustInfo(ctx, t, store, name).Metadata; got["b"] != "" || got["a"] != "1" {
		t.Errorf("metadata %v after a Put that omitted b, want only a=1", got)
	}
}

// testObjectSetMetaKeepsContent: E2 -- SetMeta rewrites no chunk: the digest
// and the bytes are what Put stored.
func testObjectSetMetaKeepsContent(t *testing.T, newBus func() events.Bus) {
	ctx, bus := setup(t, newBus)
	store := bus.ObjectStore(events.BucketArtwork)
	name := "movie/uid-3/poster/original"
	before := mustPut(ctx, t, store, name, "bytes", events.ObjectMeta{Headers: map[string]string{"Content-Type": "image/jpeg"}})
	if err := store.SetMeta(ctx, name, events.ObjectMeta{
		Headers:  map[string]string{"Content-Type": "image/jpeg"},
		Metadata: map[string]string{"k": "v"},
	}); err != nil {
		t.Fatalf("SetMeta: %v", err)
	}
	info, r, err := store.Get(ctx, name)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer func() { _ = r.Close() }()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(b) != "bytes" || info.Digest != before.Digest || info.Size != before.Size || info.Metadata["k"] != "v" {
		t.Errorf("after SetMeta: %q %+v, want the same bytes, size and digest with the new metadata", b, info)
	}
	if info.Headers["Content-Type"] != "image/jpeg" {
		t.Errorf("after SetMeta: headers %v, want the Content-Type it sent", info.Headers)
	}
}

// testObjectSetMetaOnDeleted: SetMeta on a deleted object, or one never
// written, is ErrObjectNotFound -- never a resurrection.
func testObjectSetMetaOnDeleted(t *testing.T, newBus func() events.Bus) {
	ctx, bus := setup(t, newBus)
	store := bus.ObjectStore(events.BucketArtwork)
	name := "movie/uid-4/poster/original"
	mustPut(ctx, t, store, name, "x", events.ObjectMeta{})
	if err := store.Delete(ctx, name); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	for _, n := range []string{name, "movie/never/poster/original"} {
		if err := store.SetMeta(ctx, n, events.ObjectMeta{}); !errors.Is(err, events.ErrObjectNotFound) {
			t.Errorf("SetMeta(%s) = %v, want ErrObjectNotFound", n, err)
		}
	}
	if _, err := store.Info(ctx, name); !errors.Is(err, events.ErrObjectNotFound) {
		t.Errorf("Info after a refused SetMeta = %v, want the object still gone", err)
	}
}

// testSetMetaSendsTheCompleteMap: SetMeta replaces headers and metadata
// wholesale, like Put, so a partial map loses every key it omits on both
// buses (ADR-0021 decision 3: a write sends the complete metadata). A caller
// that meant to change one key must send them all, through its store's
// builder.
func testSetMetaSendsTheCompleteMap(t *testing.T, newBus func() events.Bus) {
	ctx, bus := setup(t, newBus)
	store := bus.ObjectStore(events.BucketArtwork)
	name := "movie/uid-complete/poster/original"
	mustPut(ctx, t, store, name, "x", events.ObjectMeta{
		Headers: map[string]string{"Content-Type": "image/png", "X-Kept": "only-if-sent"},
		Metadata: map[string]string{
			events.ArtworkMetaKeyVersion: events.ArtworkMetaVersion,
			events.ArtworkMetaKeyUID:     "uid-complete",
			events.ArtworkMetaKeyKind:    "Movie",
		},
	})
	partial := events.ObjectMeta{
		Headers:  map[string]string{"Content-Type": "image/png"},
		Metadata: map[string]string{events.ArtworkMetaKeyUID: "uid-complete"},
	}
	if err := store.SetMeta(ctx, name, partial); err != nil {
		t.Fatalf("SetMeta: %v", err)
	}
	got := mustInfo(ctx, t, store, name)
	if !maps.Equal(got.Metadata, partial.Metadata) {
		t.Errorf("metadata after a partial SetMeta = %v, want exactly %v: the omitted keys are released", got.Metadata, partial.Metadata)
	}
	if _, kept := got.Headers["X-Kept"]; kept || got.Headers["Content-Type"] != "image/png" {
		t.Errorf("headers after a partial SetMeta = %v, want only the Content-Type it sent", got.Headers)
	}
}

// nextObjectEvent waits for one watch delivery.
func nextObjectEvent(t *testing.T, ch <-chan events.ObjectEvent) events.ObjectEvent {
	t.Helper()
	select {
	case e, ok := <-ch:
		if !ok {
			t.Fatal("the object watch closed")
		}
		return e
	case <-time.After(Timeout):
		t.Fatal("timed out waiting for an object watch delivery")
	}
	return events.ObjectEvent{}
}

// noObjectEvent fails if ch delivers anything within d.
func noObjectEvent(t *testing.T, ch <-chan events.ObjectEvent, d time.Duration) {
	t.Helper()
	select {
	case e, ok := <-ch:
		if ok {
			t.Errorf("unexpected object watch delivery %+v", e)
		}
	case <-time.After(d):
	}
}

// watchObjects starts a watch on store that ends with the test.
func watchObjects(ctx context.Context, t *testing.T, store events.ObjectStore, opts ...events.WatchOption) <-chan events.ObjectEvent {
	t.Helper()
	wctx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	ch, err := store.Watch(wctx, opts...)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	return ch
}

// testObjectWatchReplays: Watch replays every object's latest info, then one
// Synced event, then every change.
func testObjectWatchReplays(t *testing.T, newBus func() events.Bus) {
	ctx, bus := setup(t, newBus)
	store := bus.ObjectStore(events.BucketArtwork)
	for _, n := range []string{"movie/a/poster/original", "movie/b/poster/original"} {
		mustPut(ctx, t, store, n, n, events.ObjectMeta{})
	}
	ch := watchObjects(ctx, t, store)
	seen := map[string]bool{}
	for {
		e := nextObjectEvent(t, ch)
		if e.Synced {
			break
		}
		if !e.Deleted {
			seen[e.Info.Name] = true
		}
	}
	if len(seen) != 2 || !seen["movie/a/poster/original"] || !seen["movie/b/poster/original"] {
		t.Fatalf("replay before Synced: %v, want both objects", seen)
	}
	mustPut(ctx, t, store, "movie/c/poster/original", "c", events.ObjectMeta{})
	if e := nextObjectEvent(t, ch); e.Info.Name != "movie/c/poster/original" || e.Synced || e.Deleted {
		t.Errorf("after Synced: %+v, want the new Put", e)
	}
}

// testObjectWatchSeesSetMeta: E2 -- a metadata-only change is a watch event,
// carrying the new metadata and the bytes' unchanged digest.
func testObjectWatchSeesSetMeta(t *testing.T, newBus func() events.Bus) {
	ctx, bus := setup(t, newBus)
	store := bus.ObjectStore(events.BucketArtwork)
	name := "movie/setmeta/poster/original"
	put := mustPut(ctx, t, store, name, "bytes", events.ObjectMeta{Metadata: map[string]string{"k": "old"}})
	ch := watchObjects(ctx, t, store, events.WatchUpdatesOnly())
	if err := store.SetMeta(ctx, name, events.ObjectMeta{Metadata: map[string]string{"k": "new"}}); err != nil {
		t.Fatalf("SetMeta: %v", err)
	}
	e := nextObjectEvent(t, ch)
	if e.Synced || e.Deleted || e.Info.Name != name {
		t.Fatalf("event %+v, want the SetMeta of %s", e, name)
	}
	if e.Info.Metadata["k"] != "new" || e.Info.Digest != put.Digest {
		t.Errorf("event %+v, want metadata k=new and the Put's digest %s", e.Info, put.Digest)
	}
	noObjectEvent(t, ch, 300*time.Millisecond)
}

// testObjectWatchSeesDelete: a Delete is one event, Deleted, naming the object.
func testObjectWatchSeesDelete(t *testing.T, newBus func() events.Bus) {
	ctx, bus := setup(t, newBus)
	store := bus.ObjectStore(events.BucketArtwork)
	name := "movie/deleted/poster/original"
	mustPut(ctx, t, store, name, "bytes", events.ObjectMeta{})
	ch := watchObjects(ctx, t, store, events.WatchUpdatesOnly())
	if err := store.Delete(ctx, name); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	e := nextObjectEvent(t, ch)
	if !e.Deleted || e.Synced || e.Info.Name != name {
		t.Errorf("event %+v, want a Deleted event for %s", e, name)
	}
	noObjectEvent(t, ch, 300*time.Millisecond)
}

// testObjectWatchUpdatesOnly: WatchUpdatesOnly skips the replay and the
// Synced event: the first event is the first change.
func testObjectWatchUpdatesOnly(t *testing.T, newBus func() events.Bus) {
	ctx, bus := setup(t, newBus)
	store := bus.ObjectStore(events.BucketArtwork)
	for _, n := range []string{"movie/old-1/poster/original", "movie/old-2/poster/original"} {
		mustPut(ctx, t, store, n, n, events.ObjectMeta{})
	}
	ch := watchObjects(ctx, t, store, events.WatchUpdatesOnly())
	noObjectEvent(t, ch, 300*time.Millisecond)
	mustPut(ctx, t, store, "movie/new/poster/original", "new", events.ObjectMeta{})
	if e := nextObjectEvent(t, ch); e.Synced || e.Deleted || e.Info.Name != "movie/new/poster/original" {
		t.Errorf("first event %+v, want the Put made after the watch began, never Synced", e)
	}
	noObjectEvent(t, ch, 300*time.Millisecond)
}

// testObjectWatchRefusesFromRevision: nats.go would silently replay from the
// start (E6), so the bus refuses rather than pretend.
func testObjectWatchRefusesFromRevision(t *testing.T, newBus func() events.Bus) {
	ctx, bus := setup(t, newBus)
	if _, err := bus.ObjectStore(events.BucketArtwork).Watch(ctx, events.WatchFromRevision(1000)); !errors.Is(err, events.ErrWatchOptionUnsupported) {
		t.Fatalf("Watch(FromRevision) = %v, want ErrWatchOptionUnsupported", err)
	}
}

// testObjectWatchPastTheBuffer: a reader that falls 1,000 changes behind
// loses none, in order (nats.go's callback blocks on 32 slots; the bus relays
// through an unbounded queue), and no writer waits on it.
func testObjectWatchPastTheBuffer(t *testing.T, newBus func() events.Bus) {
	ctx, bus := setupFor(t, newBus, 2*Timeout)
	store := bus.ObjectStore(events.BucketArtwork)
	ch := watchObjects(ctx, t, store, events.WatchUpdatesOnly())
	const n = 1000
	names := make([]string, n)
	for i := range n {
		names[i] = fmt.Sprintf("movie/buf-%04d/poster/original", i)
		putCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		if _, err := store.Put(putCtx, names[i], strings.NewReader("x"), events.ObjectMeta{}); err != nil {
			cancel()
			t.Fatalf("Put %d with nobody reading the watch: %v", i, err)
		}
		cancel()
	}
	for i, want := range names {
		e := nextObjectEvent(t, ch)
		if e.Synced || e.Deleted || e.Info.Name != want {
			t.Fatalf("event %d = %+v, want the Put of %s", i, e, want)
		}
	}
}

// testObjectStoreStatus: Status names the bucket and its creation time, which
// a Put does not move (a reader compares it to notice a bucket re-created
// under a running watch, research E9).
func testObjectStoreStatus(t *testing.T, newBus func() events.Bus) {
	ctx, bus := setup(t, newBus)
	store := bus.ObjectStore(events.BucketArtwork)
	a, err := store.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	mustPut(ctx, t, store, "movie/s/poster/original", "s", events.ObjectMeta{})
	b, err := store.Status(ctx)
	if err != nil {
		t.Fatalf("Status after a Put: %v", err)
	}
	if a.Created.IsZero() || !a.Created.Equal(b.Created) || a.Bucket != events.BucketArtwork || b.Bucket != events.BucketArtwork {
		t.Errorf("Status %+v then %+v, want bucket %s and one non-zero creation time", a, b, events.BucketArtwork)
	}
	if b.Bytes == 0 {
		t.Errorf("Status after a Put reports %d bytes, want the stored size", b.Bytes)
	}
}
