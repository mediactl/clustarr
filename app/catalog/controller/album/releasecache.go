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
	lru "github.com/hashicorp/golang-lru/v2"
	"k8s.io/apimachinery/pkg/types"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
)

// DefaultReleaseCacheSize bounds a ReleaseCache (loop spec §3.12).
const DefaultReleaseCacheSize = 4096

// ReleaseCache holds each Album's fetched releases, keyed by the Album's
// UID, spec.releaseGroupID and status.metadata.refreshedAt, so the
// track-listing RPC runs only when one of them changed: an Album reconcile
// no longer waits on the metadata gateway on the loop's shared queue (§3.12,
// §3.17). Everything else the selection reads -- a pinned release, the
// Artist's metadata profile, the files -- is applied to the cached releases
// on every reconcile. A failed fetch is never cached. The releases it hands
// back are shared: SelectRelease and BuildTracks only read them.
type ReleaseCache struct {
	lru *lru.Cache[releaseKey, []pkgmetadata.AlbumRelease]
}

type releaseKey struct {
	uid          types.UID
	releaseGroup string
	refreshedAt  int64 // status.metadata.refreshedAt in Unix nanoseconds; 0 without metadata
}

// NewReleaseCache is a ReleaseCache of at most size Albums, least recently
// used out first; a size below 1 is DefaultReleaseCacheSize.
func NewReleaseCache(size int) *ReleaseCache {
	if size < 1 {
		size = DefaultReleaseCacheSize
	}
	c, err := lru.New[releaseKey, []pkgmetadata.AlbumRelease](size)
	if err != nil { // lru.New fails only for a size below 1, ruled out above
		panic(err)
	}
	return &ReleaseCache{lru: c}
}

func keyOf(alb *catalogv1alpha1.Album) releaseKey {
	k := releaseKey{uid: alb.UID, releaseGroup: alb.Spec.ReleaseGroupID}
	if alb.Status.Metadata != nil {
		k.refreshedAt = alb.Status.Metadata.RefreshedAt.UnixNano()
	}
	return k
}

// Get is alb's cached releases.
func (c *ReleaseCache) Get(alb *catalogv1alpha1.Album) ([]pkgmetadata.AlbumRelease, bool) {
	return c.lru.Get(keyOf(alb))
}

// Put caches a successful fetch of alb's releases.
func (c *ReleaseCache) Put(alb *catalogv1alpha1.Album, releases []pkgmetadata.AlbumRelease) {
	c.lru.Add(keyOf(alb), releases)
}

// Len is how many Albums the cache holds.
func (c *ReleaseCache) Len() int { return c.lru.Len() }
