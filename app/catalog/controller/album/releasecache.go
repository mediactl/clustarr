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
	"math/rand/v2"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
// on every reconcile. A failed fetch is never cached: it is held instead
// (LookupHold), so the next lookup waits. The releases it hands back are
// shared: SelectRelease and BuildTracks only read them.
type ReleaseCache struct {
	lru   *lru.Cache[releaseKey, []pkgmetadata.AlbumRelease]
	holds *lru.Cache[releaseKey, LookupHold]
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
	h, err := lru.New[releaseKey, LookupHold](size)
	if err != nil {
		panic(err)
	}
	return &ReleaseCache{lru: c, holds: h}
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

// Put caches a successful fetch of alb's releases, which ends its hold.
func (c *ReleaseCache) Put(alb *catalogv1alpha1.Album, releases []pkgmetadata.AlbumRelease) {
	k := keyOf(alb)
	c.lru.Add(k, releases)
	c.holds.Remove(k)
}

// Len is how many Albums the cache holds.
func (c *ReleaseCache) Len() int { return c.lru.Len() }

// LookupRetryMin and LookupRetryMax bound the wait a failed track-listing
// lookup holds an Album to (ReleaseCache.Failed).
const (
	LookupRetryMin = 30 * time.Second
	LookupRetryMax = time.Hour
)

// LookupHold is a failed track-listing lookup an Album waits out before it
// asks the metadata gateway again.
type LookupHold struct {
	// Until is when the next lookup may run.
	Until time.Time
	// Err is the failure, reported on TracksSynced again while the Album
	// waits, so the condition holds still.
	Err error
	// since is when the lookup started failing.
	since time.Time
}

// Held is alb's hold while it lasts at now: no lookup may run before
// Until. A hold is keyed as the releases are, so fresh metadata (a moved
// status.metadata.refreshedAt) is not held: the gateway's refresh has just
// cached the document the lookup is answered from.
func (c *ReleaseCache) Held(alb *catalogv1alpha1.Album, now time.Time) (LookupHold, bool) {
	h, ok := c.holds.Get(keyOf(alb))
	if !ok || !now.Before(h.Until) {
		return LookupHold{}, false
	}
	return h, true
}

// Failed holds alb's next lookup back after one that failed at now with
// err, and returns the hold. The wait is how long the lookup has been
// failing, between LookupRetryMin and LookupRetryMax, so each wait about
// doubles the last, jittered to between half and all of it so Albums that
// failed together do not ask again together. Every Album's reconcile
// asked again 30 s after any failure until 2026-11-15, and on talos 1,082
// of them asked the gateway for about 70 MusicBrainz requests a second
// against the one it may make, so almost none were answered.
//
// The lookup has been failing since its first failure this process saw,
// or since alb's TracksSynced condition went false over a failed lookup,
// whichever is earlier: a restarted manager asks once for an Album failing
// for days and then waits the longest, rather than every 30 s again.
func (c *ReleaseCache) Failed(alb *catalogv1alpha1.Album, now time.Time, err error) LookupHold {
	k := keyOf(alb)
	since := failingSince(alb, now)
	if prev, ok := c.holds.Peek(k); ok && prev.since.Before(since) {
		since = prev.since
	}
	d := min(max(now.Sub(since), LookupRetryMin), LookupRetryMax)
	h := LookupHold{Until: now.Add(d/2 + rand.N(d/2+1)), Err: err, since: since}
	c.holds.Add(k, h)
	return h
}

// failingSince is when alb's TracksSynced condition went false over a
// failed lookup, or now. A false condition over a release selection
// (SelectionPinnedReleaseMissing, SelectionNoAcceptedRelease) is no failed
// lookup.
func failingSince(alb *catalogv1alpha1.Album, now time.Time) time.Time {
	cond := meta.FindStatusCondition(alb.Status.Conditions, conditionTracksSynced)
	if cond == nil || cond.Status != metav1.ConditionFalse ||
		cond.Reason == string(SelectionPinnedReleaseMissing) || cond.Reason == string(SelectionNoAcceptedRelease) ||
		!cond.LastTransitionTime.Time.Before(now) {
		return now
	}
	return cond.LastTransitionTime.Time
}
