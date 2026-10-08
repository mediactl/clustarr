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

package ui_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/natsbus"
	"github.com/mediactl/clustarr/ui"
)

// newArtStore builds an in-memory artwork bucket exactly like the metadata
// gateway's own fixture (app/catalog/metadata/artwork/fetcher_test.go's
// newFixture): a membus, Ensure'd with the default topology so
// events.BucketArtwork exists, then bound.
func newArtStore(t *testing.T) events.ObjectStore {
	t.Helper()
	bus := membus.New(nil)
	require.NoError(t, bus.Ensure(t.Context(), events.Default()))
	return bus.ObjectStore(events.BucketArtwork)
}

// putArt writes body to store under key with contentType and returns the
// resulting ObjectInfo, so a test can assert the digest it later expects
// handleArt to serve and set as the response's ETag.
func putArt(t *testing.T, store events.ObjectStore, key, contentType, body string) events.ObjectInfo {
	t.Helper()
	info, err := store.Put(t.Context(), key, strings.NewReader(body),
		events.ObjectMeta{Headers: map[string]string{events.HeaderContentType: contentType}})
	require.NoError(t, err)
	return info
}

func artPath(uid types.UID) string {
	return "/art/movie/" + string(uid) + "/poster"
}

// TestHandleArtPrefersOverlayOverOriginal: with both variants stored,
// handleArt serves the rendered overlay, not the metadata gateway's
// original -- the renderer's badge is meant to be what a viewer actually
// sees.
func TestHandleArtPrefersOverlayOverOriginal(t *testing.T) {
	store := newArtStore(t)
	uid := types.UID("11111111-1111-1111-1111-111111111111")
	putArt(t, store, events.ArtworkKey(commonv1.MediaKindMovie, uid, string(catalogv1.ImageTypePoster), events.ArtworkVariantOriginal),
		"image/jpeg", "original-bytes")
	overlay := putArt(t, store, events.ArtworkKey(commonv1.MediaKindMovie, uid, string(catalogv1.ImageTypePoster), events.ArtworkVariantOverlay),
		"image/png", "overlay-bytes")

	srv := ui.NewServer(t.Context(), ui.Options{Artwork: store})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, artPath(uid), nil))

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "overlay-bytes", rec.Body.String())
	require.Equal(t, `"`+overlay.Digest+`"`, rec.Header().Get("ETag"))
	require.Equal(t, "image/png", rec.Header().Get("Content-Type"))
	require.Equal(t, strconv.Itoa(len("overlay-bytes")), rec.Header().Get("Content-Length"))
}

// TestHandleArtFallsBackToOriginal: with only the original stored (no
// overlay rendered yet), handleArt serves it.
func TestHandleArtFallsBackToOriginal(t *testing.T) {
	store := newArtStore(t)
	uid := types.UID("22222222-2222-2222-2222-222222222222")
	original := putArt(t, store, events.ArtworkKey(commonv1.MediaKindMovie, uid, string(catalogv1.ImageTypePoster), events.ArtworkVariantOriginal),
		"image/jpeg", "original-bytes")

	srv := ui.NewServer(t.Context(), ui.Options{Artwork: store})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, artPath(uid), nil))

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "original-bytes", rec.Body.String())
	require.Equal(t, `"`+original.Digest+`"`, rec.Header().Get("ETag"))
	require.Equal(t, "image/jpeg", rec.Header().Get("Content-Type"))
	require.Equal(t, strconv.Itoa(len("original-bytes")), rec.Header().Get("Content-Length"))
}

// TestHandleArt404WhenBothVariantsMissing: neither object written -> 404,
// not a broken image and not a 500.
func TestHandleArt404WhenBothVariantsMissing(t *testing.T) {
	store := newArtStore(t)
	uid := types.UID("33333333-3333-3333-3333-333333333333")

	srv := ui.NewServer(t.Context(), ui.Options{Artwork: store})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, artPath(uid), nil))

	require.Equal(t, http.StatusNotFound, rec.Code)
}

// TestHandleArtNilOptionsArtworkIs404: a ui process with no NATS endpoint
// reachable (Options.Artwork left nil, exactly like a nil Reader) answers
// 404 for every /art request rather than panicking.
func TestHandleArtNilOptionsArtworkIs404(t *testing.T) {
	srv := ui.NewServer(t.Context(), ui.Options{})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, artPath("abc"), nil))
	require.Equal(t, http.StatusNotFound, rec.Code)
}

// TestHandleArtUnknownKindIs404NotServerError: a {kind} the MediaKind enum
// does not carry must not reach events.ArtworkKey, which panics on an
// unrecognised part -- 404, never a 500.
func TestHandleArtUnknownKindIs404NotServerError(t *testing.T) {
	store := newArtStore(t)
	srv := ui.NewServer(t.Context(), ui.Options{Artwork: store})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/art/spaceship/abc/poster", nil))
	require.Equal(t, http.StatusNotFound, rec.Code)
}

// TestHandleArtUnknownTypeIs404: a {type} the ImageType enum does not carry
// is 404 too, for the same reason.
func TestHandleArtUnknownTypeIs404(t *testing.T) {
	store := newArtStore(t)
	srv := ui.NewServer(t.Context(), ui.Options{Artwork: store})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/art/movie/abc/backdrop", nil))
	require.Equal(t, http.StatusNotFound, rec.Code)
}

// TestHandleArtIfNoneMatchIs304WithNoBody: a conditional request carrying
// the object's own ETag gets 304 and no body, sparing the transfer entirely.
func TestHandleArtIfNoneMatchIs304WithNoBody(t *testing.T) {
	store := newArtStore(t)
	uid := types.UID("44444444-4444-4444-4444-444444444444")
	info := putArt(t, store, events.ArtworkKey(commonv1.MediaKindMovie, uid, string(catalogv1.ImageTypePoster), events.ArtworkVariantOriginal),
		"image/jpeg", "original-bytes")

	srv := ui.NewServer(t.Context(), ui.Options{Artwork: store})
	req := httptest.NewRequest(http.MethodGet, artPath(uid), nil)
	req.Header.Set("If-None-Match", `"`+info.Digest+`"`)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusNotModified, rec.Code)
	require.Empty(t, rec.Body.String())
	require.Empty(t, rec.Header().Get("Content-Length"), "a 304 carries no body, so no Content-Length either")
}

// TestHandleArtCacheControlIsImmutableOnlyWhenVMatchesServedDigest: the
// route's whole point is a URL that is safe to cache forever once its ?v
// names the digest actually served, and never otherwise -- no ?v, and a
// stale ?v naming a digest this object no longer carries, both revalidate.
func TestHandleArtCacheControlIsImmutableOnlyWhenVMatchesServedDigest(t *testing.T) {
	store := newArtStore(t)
	uid := types.UID("55555555-5555-5555-5555-555555555555")
	info := putArt(t, store, events.ArtworkKey(commonv1.MediaKindMovie, uid, string(catalogv1.ImageTypePoster), events.ArtworkVariantOriginal),
		"image/jpeg", "original-bytes")

	srv := ui.NewServer(t.Context(), ui.Options{Artwork: store})

	matching := httptest.NewRecorder()
	srv.Handler().ServeHTTP(matching, httptest.NewRequest(http.MethodGet, artPath(uid)+"?v="+info.Digest, nil))
	require.Equal(t, "public, max-age=31536000, immutable", matching.Header().Get("Cache-Control"))

	noV := httptest.NewRecorder()
	srv.Handler().ServeHTTP(noV, httptest.NewRequest(http.MethodGet, artPath(uid), nil))
	require.Equal(t, "no-cache", noV.Header().Get("Cache-Control"))

	stale := httptest.NewRecorder()
	srv.Handler().ServeHTTP(stale, httptest.NewRequest(http.MethodGet, artPath(uid)+"?v=stale-digest", nil))
	require.Equal(t, "no-cache", stale.Header().Get("Cache-Control"))
}

// countingStore counts the reads that cost a NATS round trip, and the names
// Info was asked for.
type countingStore struct {
	events.ObjectStore
	gets, infos atomic.Int32

	mu        sync.Mutex
	infoNames []string
}

func (c *countingStore) Get(ctx context.Context, n string) (events.ObjectInfo, io.ReadCloser, error) {
	c.gets.Add(1)
	return c.ObjectStore.Get(ctx, n)
}

func (c *countingStore) Info(ctx context.Context, n string) (events.ObjectInfo, error) {
	c.infos.Add(1)
	c.mu.Lock()
	c.infoNames = append(c.infoNames, n)
	c.mu.Unlock()
	return c.ObjectStore.Info(ctx, n)
}

func (c *countingStore) asked() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.infoNames)
}

// unsyncedStore is a store whose Watch never replays, so the ui's artwork
// index never syncs and /art must ask the store.
type unsyncedStore struct{ *countingStore }

func (unsyncedStore) Watch(ctx context.Context, _ ...events.WatchOption) (<-chan events.ObjectEvent, error) {
	return make(chan events.ObjectEvent), nil
}

// newSyncedArtServer is NewServer over store, once its artwork index has
// replayed the bucket.
func newSyncedArtServer(t *testing.T, opts ui.Options) *ui.Server {
	t.Helper()
	s := ui.NewServer(t.Context(), opts)
	require.Eventually(t, func() bool { return ui.ArtIndexSyncedForTest(s) }, 10*time.Second, 10*time.Millisecond,
		"the artwork index never synced")
	return s
}

func posterKey(kind commonv1.MediaKind, uid types.UID, variant string) string {
	return events.ArtworkKey(kind, uid, string(catalogv1.ImageTypePoster), variant)
}

func getArt(s *ui.Server, path string, header ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// TestArt304MakesNoStoreCalls: with the index synced, a matching
// If-None-Match answers 304 from the index alone. /art used to Get the whole
// object -- a direct get plus an ordered consumer created and deleted -- before
// it checked the header (artwork design §B.8 as amended 2026-10-07).
func TestArt304MakesNoStoreCalls(t *testing.T) {
	store := &countingStore{ObjectStore: newArtStore(t)}
	info := putArt(t, store.ObjectStore, posterKey(commonv1.MediaKindMovie, "u1", events.ArtworkVariantOriginal), "image/png", "png")
	s := newSyncedArtServer(t, ui.Options{Artwork: store})

	rec := getArt(s, "/art/movie/u1/poster", "If-None-Match", `"`+info.Digest+`"`)
	require.Equal(t, http.StatusNotModified, rec.Code)
	require.Equal(t, `"`+info.Digest+`"`, rec.Header().Get("ETag"))
	require.Zero(t, store.gets.Load()+store.infos.Load(), "a 304 must cost no store call")
}

// TestArtSkipsOverlayForKindsWithoutOverlays: only movie and series posters
// can have an overlay (artwork design §B.3); with no synced index, an artist
// poster asks the store for its original alone, while a movie poster asks
// for the overlay first.
func TestArtSkipsOverlayForKindsWithoutOverlays(t *testing.T) {
	inner := &countingStore{ObjectStore: newArtStore(t)}
	putArt(t, inner.ObjectStore, posterKey(commonv1.MediaKindArtist, "u2", events.ArtworkVariantOriginal), "image/jpeg", "artist")
	putArt(t, inner.ObjectStore, posterKey(commonv1.MediaKindMovie, "u3", events.ArtworkVariantOriginal), "image/jpeg", "movie")
	s := ui.NewServer(t.Context(), ui.Options{Artwork: unsyncedStore{inner}})

	rec := getArt(s, "/art/artist/u2/poster")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "artist", rec.Body.String())
	require.Equal(t, []string{posterKey(commonv1.MediaKindArtist, "u2", events.ArtworkVariantOriginal)}, inner.asked(),
		"an artist poster has no overlay to look for")

	rec = getArt(s, "/art/movie/u3/poster")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, []string{
		posterKey(commonv1.MediaKindArtist, "u2", events.ArtworkVariantOriginal),
		posterKey(commonv1.MediaKindMovie, "u3", events.ArtworkVariantOverlay),
		posterKey(commonv1.MediaKindMovie, "u3", events.ArtworkVariantOriginal),
	}, inner.asked(), "a movie poster looks for its overlay first")
}

// TestArtMissWithUnsyncedIndexAsksTheStore: an unsynced index never makes a
// miss authoritative -- an object the index has not seen is served from the
// store -- while a synced index's miss is a 404 with no store call.
func TestArtMissWithUnsyncedIndexAsksTheStore(t *testing.T) {
	inner := &countingStore{ObjectStore: newArtStore(t)}
	putArt(t, inner.ObjectStore, posterKey(commonv1.MediaKindMovie, "u4", events.ArtworkVariantOriginal), "image/jpeg", "unseen")
	s := ui.NewServer(t.Context(), ui.Options{Artwork: unsyncedStore{inner}})
	require.False(t, ui.ArtIndexSyncedForTest(s))

	rec := getArt(s, "/art/movie/u4/poster")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "unseen", rec.Body.String())
	require.Positive(t, inner.infos.Load(), "the unsynced index's miss went to the store")

	synced := &countingStore{ObjectStore: newArtStore(t)}
	ss := newSyncedArtServer(t, ui.Options{Artwork: synced})
	require.Equal(t, http.StatusNotFound, getArt(ss, "/art/movie/u5/poster").Code)
	require.Zero(t, synced.gets.Load()+synced.infos.Load(), "a synced index's miss is authoritative")
}

// TestArtServesFromTheDigestCache: the second request for one digest makes
// no Get; an object larger than a quarter of the cache is served but not kept.
func TestArtServesFromTheDigestCache(t *testing.T) {
	store := &countingStore{ObjectStore: newArtStore(t)}
	small := putArt(t, store.ObjectStore, posterKey(commonv1.MediaKindMovie, "u6", events.ArtworkVariantOriginal), "image/png",
		strings.Repeat("s", 100))
	putArt(t, store.ObjectStore, posterKey(commonv1.MediaKindMovie, "u7", events.ArtworkVariantOriginal), "image/png",
		strings.Repeat("l", 300))
	s := newSyncedArtServer(t, ui.Options{Artwork: store, ArtCacheBytes: 1024})

	for range 2 {
		rec := getArt(s, "/art/movie/u6/poster")
		require.Equal(t, http.StatusOK, rec.Code)
		require.Equal(t, strings.Repeat("s", 100), rec.Body.String())
		require.Equal(t, `"`+small.Digest+`"`, rec.Header().Get("ETag"))
	}
	require.Equal(t, int32(1), store.gets.Load(), "the second request for one digest is served from the cache")

	for range 2 {
		rec := getArt(s, "/art/movie/u7/poster")
		require.Equal(t, http.StatusOK, rec.Code)
		require.Len(t, rec.Body.String(), 300)
	}
	require.Equal(t, int32(3), store.gets.Load(), "an object over a quarter of the cache is read every time")
}

// overwritingStore's first Get returns a reader that, after 128 KiB, Puts
// replacement under the same name before it reads on.
type overwritingStore struct {
	events.ObjectStore
	t           *testing.T
	replacement []byte
	once        sync.Once
}

func (o *overwritingStore) Get(ctx context.Context, name string) (events.ObjectInfo, io.ReadCloser, error) {
	info, rc, err := o.ObjectStore.Get(ctx, name)
	if err != nil {
		return info, rc, err
	}
	first := false
	o.once.Do(func() { first = true })
	if !first {
		return info, rc, nil
	}
	return info, &overwriteAfter{ReadCloser: rc, after: 128 << 10, overwrite: func() {
		_, err := o.ObjectStore.Put(context.Background(), name, bytes.NewReader(o.replacement),
			events.ObjectMeta{Headers: map[string]string{events.HeaderContentType: "image/png"}})
		assert.NoError(o.t, err, "overwrite the object mid-read")
	}}, nil
}

type overwriteAfter struct {
	io.ReadCloser
	after     int
	read      int
	done      bool
	overwrite func()
}

func (r *overwriteAfter) Read(p []byte) (int, error) {
	if !r.done && r.read >= r.after {
		r.done = true
		r.overwrite()
	}
	n, err := r.ReadCloser.Read(p)
	r.read += n
	return n, err
}

// TestArtRetriesAnObjectOverwrittenMidRead is research E14: an object
// overwritten while /art reads it stalls the reader until its deadline,
// then fails with "i/o timeout", never a digest error. /art reads whole
// objects under a deadline and retries once with a fresh lookup, so the
// client gets a 200 with the new bytes, never a truncated 200. Real server.
func TestArtRetriesAnObjectOverwrittenMidRead(t *testing.T) {
	srv, err := natsserver.NewServer(&natsserver.Options{
		ServerName: "clustarr-ui-art-test", Host: "127.0.0.1", Port: -1,
		JetStream: true, StoreDir: t.TempDir(), NoLog: true, NoSigs: true,
	})
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(20*time.Second))
	t.Cleanup(srv.Shutdown)
	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	bus, err := natsbus.New(nc)
	require.NoError(t, err)
	require.NoError(t, bus.Ensure(t.Context(), events.Default().ForSingleNode()))
	store := bus.ObjectStore(events.BucketArtwork)

	key := posterKey(commonv1.MediaKindMovie, "u8", events.ArtworkVariantOriginal)
	big := bytes.Repeat([]byte{0xAB}, 8<<20)
	_, err = store.Put(t.Context(), key, bytes.NewReader(big),
		events.ObjectMeta{Headers: map[string]string{events.HeaderContentType: "image/png"}})
	require.NoError(t, err)
	replacement := bytes.Repeat([]byte{0xCD}, 1<<10)
	sum := sha256.Sum256(replacement)

	ui.SetArtReadTimeoutForTest(t, 2*time.Second)
	s := newSyncedArtServer(t, ui.Options{Artwork: &overwritingStore{ObjectStore: store, t: t, replacement: replacement}})

	rec := getArt(s, "/art/movie/u8/poster")
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, replacement, rec.Body.Bytes(), "the retry serves the new object whole")
	require.Equal(t, `"`+hex.EncodeToString(sum[:])+`"`, rec.Header().Get("ETag"), "the ETag names the bytes sent")
}
