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

package album

import (
	"bytes"
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/klauspost/compress/zstd"
	"k8s.io/apimachinery/pkg/types"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/lookuphold"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
)

// DefaultReleaseCacheSize and DefaultReleaseCacheBytes bound a ReleaseCache
// (loop spec §3.12): how many Albums it holds, and how many bytes their
// compressed listings take together.
const (
	DefaultReleaseCacheSize  = 16384
	DefaultReleaseCacheBytes = 48 << 20
)

// ReleaseCache holds each Album's fetched releases, keyed by the Album's
// UID, spec.releaseGroupID and status.metadata.refreshedAt, so the
// track-listing RPC runs only when one of them changed: an Album reconcile
// no longer waits on the metadata gateway on the loop's shared queue (§3.12,
// §3.17). Everything else the selection reads -- a pinned release, the
// Artist's metadata profile, the files -- is applied to the cached releases
// on every reconcile. A failed fetch is never cached: it is held instead
// (LookupHold), so the next lookup waits.
//
// A listing is held as its JSON in one zstd frame and decoded at each Get
// (owner, 2026-11-17): a release group's every release with every track,
// decoded, is a map of ids per track and per release, about 27 KB an Album
// and 113 MB of the manager's live heap for the 4,096 Albums the cache then
// held of talos's 6,876. The releases of one group repeat one another's
// tracks, so the frame is a small part of that, and the bounds now cover
// the library. The releases a Get hands back are the caller's own.
type ReleaseCache struct {
	mu       sync.Mutex // Put: the replace, the add and the trim as one
	lru      *lru.Cache[releaseKey, []byte]
	bytes    atomic.Int64
	maxBytes int64
	holds    lookuphold.Holds[releaseKey]
}

type releaseKey struct {
	uid          types.UID
	releaseGroup string
	refreshedAt  int64 // status.metadata.refreshedAt in Unix nanoseconds; 0 without metadata
}

// NewReleaseCache is a ReleaseCache of at most size Albums and
// DefaultReleaseCacheBytes of listings, least recently used out first; a
// size below 1 is DefaultReleaseCacheSize.
func NewReleaseCache(size int) *ReleaseCache {
	return NewReleaseCacheOfBytes(size, DefaultReleaseCacheBytes)
}

// NewReleaseCacheOfBytes is NewReleaseCache with its own byte bound; one
// below 1 is DefaultReleaseCacheBytes. A listing larger than the bound is
// not kept.
func NewReleaseCacheOfBytes(size int, maxBytes int64) *ReleaseCache {
	if size < 1 {
		size = DefaultReleaseCacheSize
	}
	if maxBytes < 1 {
		maxBytes = DefaultReleaseCacheBytes
	}
	c := &ReleaseCache{maxBytes: maxBytes}
	l, err := lru.NewWithEvict(size, func(_ releaseKey, frame []byte) { c.bytes.Add(-int64(len(frame))) })
	if err != nil { // lru.NewWithEvict fails only for a size below 1, ruled out above
		panic(err)
	}
	c.lru = l
	return c
}

func keyOf(alb *catalogv1alpha1.Album) releaseKey {
	k := releaseKey{uid: alb.UID, releaseGroup: alb.Spec.ReleaseGroupID}
	if alb.Status.Metadata != nil {
		k.refreshedAt = alb.Status.Metadata.RefreshedAt.UnixNano()
	}
	return k
}

// releaseFrames is the one zstd encoder and decoder every ReleaseCache
// shares; EncodeAll and DecodeAll are safe for concurrent use.
var releaseFrames = sync.OnceValues(func() (*zstd.Encoder, *zstd.Decoder) {
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault), zstd.WithEncoderConcurrency(1),
		zstd.WithWindowSize(1<<20), zstd.WithLowerEncoderMem(true), zstd.WithEncoderCRC(false))
	if err != nil { // constant, valid options
		panic(err)
	}
	dec, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(2), zstd.WithDecoderLowmem(true))
	if err != nil {
		panic(err)
	}
	return enc, dec
})

// Get is alb's cached releases, decoded for the caller. An entry that does
// not decode is dropped and reads as a miss, so the listing is fetched again.
func (c *ReleaseCache) Get(alb *catalogv1alpha1.Album) ([]pkgmetadata.AlbumRelease, bool) {
	k := keyOf(alb)
	frame, ok := c.lru.Get(k)
	if !ok {
		return nil, false
	}
	_, dec := releaseFrames()
	raw, err := dec.DecodeAll(frame, nil)
	var releases []pkgmetadata.AlbumRelease
	if err == nil {
		err = json.Unmarshal(raw, &releases)
	}
	if err != nil {
		c.lru.Remove(k)
		return nil, false
	}
	return releases, true
}

// Put caches a successful fetch of alb's releases, which ends its hold. A
// listing that cannot be kept (larger than the byte bound) is only not
// cached: the next reconcile asks the gateway again, whose own cache answers.
func (c *ReleaseCache) Put(alb *catalogv1alpha1.Album, releases []pkgmetadata.AlbumRelease) {
	k := keyOf(alb)
	c.holds.Clear(k)
	raw, err := json.Marshal(releases)
	if err != nil {
		return
	}
	enc, _ := releaseFrames()
	frame := bytes.Clone(enc.EncodeAll(raw, nil)) // no spare capacity kept

	c.mu.Lock()
	defer c.mu.Unlock()
	c.lru.Remove(k) // through the eviction callback, so the count stays exact
	if int64(len(frame)) > c.maxBytes {
		return
	}
	c.bytes.Add(int64(len(frame)))
	c.lru.Add(k, frame)
	for c.bytes.Load() > c.maxBytes {
		if _, _, ok := c.lru.RemoveOldest(); !ok {
			break
		}
	}
}

// Len is how many Albums the cache holds.
func (c *ReleaseCache) Len() int { return c.lru.Len() }

// Bytes is how many bytes the cached listings take.
func (c *ReleaseCache) Bytes() int64 { return c.bytes.Load() }

// LookupRetryMin and LookupRetryMax bound the wait a failed track-listing
// lookup holds an Album to (ReleaseCache.Failed).
const (
	LookupRetryMin = lookuphold.Min
	LookupRetryMax = lookuphold.Max
)

// LookupHold is a failed track-listing lookup an Album waits out before it
// asks the metadata gateway again.
type LookupHold = lookuphold.Hold

// Held is alb's hold while it lasts at now: no lookup may run before
// Until. A hold is keyed as the releases are, so fresh metadata (a moved
// status.metadata.refreshedAt) is not held: the gateway's refresh has just
// cached the document the lookup is answered from.
func (c *ReleaseCache) Held(alb *catalogv1alpha1.Album, now time.Time) (LookupHold, bool) {
	return c.holds.Held(keyOf(alb), now)
}

// Failed holds alb's next lookup back after one that failed at now with
// err, and returns the hold (lookuphold.Holds.Failed): the lookup has been
// failing since alb's TracksSynced condition went false over a failed
// lookup, a release selection's verdict being none.
func (c *ReleaseCache) Failed(alb *catalogv1alpha1.Album, now time.Time, err error) LookupHold {
	since := lookuphold.Since(alb.Status.Conditions, conditionTracksSynced, now,
		string(SelectionPinnedReleaseMissing), string(SelectionNoAcceptedRelease))
	return c.holds.Failed(keyOf(alb), now, since, err)
}
