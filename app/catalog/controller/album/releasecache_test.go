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

package album_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/album"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
)

var errLookup = errors.New("metadata gateway: rate: Wait(n=1) would exceed context deadline")

// lookupAlbum is an Album whose track listing has not been fetched, its
// metadata refreshed at refreshed, and its TracksSynced condition, when
// tracksSynced is set, in that state since since.
func lookupAlbum(uid string, refreshed time.Time, tracksSynced *metav1.Condition) *catalogv1alpha1.Album {
	alb := &catalogv1alpha1.Album{
		ObjectMeta: metav1.ObjectMeta{Name: uid, Namespace: "music", UID: types.UID(uid)},
		Spec:       catalogv1alpha1.AlbumSpec{ArtistRef: "berner", ReleaseGroupID: "rg-" + uid},
		Status: catalogv1alpha1.AlbumStatus{Metadata: &catalogv1alpha1.AlbumMetadata{
			Title: "Border Wars", RefreshedAt: metav1.NewTime(refreshed),
		}},
	}
	if tracksSynced != nil {
		alb.Status.Conditions = []metav1.Condition{*tracksSynced}
	}
	return alb
}

func tracksSyncedFalse(reason string, since time.Time) *metav1.Condition {
	return &metav1.Condition{Type: "TracksSynced", Status: metav1.ConditionFalse, Reason: reason, LastTransitionTime: metav1.NewTime(since)}
}

func assertWithin(t *testing.T, got, lo, hi time.Duration, msg string) {
	t.Helper()
	assert.GreaterOrEqual(t, got, lo, msg)
	assert.LessOrEqual(t, got, hi, msg)
}

// A failed track-listing lookup holds the Album's next one back, longer the
// longer the lookup has been failing -- the failure's own age, so each
// wait about doubles -- up to an hour, each wait jittered to between half
// and all of it (talos, 2026-11-15: 1,082 Albums retrying every 30 s
// asked the gateway for about 70 MusicBrainz requests a second against
// one, and almost none were answered).
func TestAFailedLookupHoldsTheNextOneBackLongerTheLongerItFails(t *testing.T) {
	c := album.NewReleaseCache(0)
	t0 := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	alb := lookupAlbum("border-wars", t0.Add(-time.Hour), nil)

	_, held := c.Held(alb, t0)
	require.False(t, held, "nothing has failed yet")

	hold := c.Failed(alb, t0, errLookup)
	assertWithin(t, hold.Until.Sub(t0), album.LookupRetryMin/2, album.LookupRetryMin, "the first wait")
	require.ErrorIs(t, hold.Err, errLookup)

	got, held := c.Held(alb, hold.Until.Add(-time.Second))
	require.True(t, held, "the lookup waits until the hold ends")
	assert.Equal(t, hold.Until, got.Until)
	require.ErrorIs(t, got.Err, errLookup, "the failure is reported again while it waits")
	_, held = c.Held(alb, hold.Until)
	require.False(t, held, "the hold ends on time")

	// Fail at every hold's end for a day: each wait is between half and all
	// of the time it has been failing, within the bounds.
	now := hold.Until
	var last time.Duration
	for now.Sub(t0) < 24*time.Hour {
		h := c.Failed(alb, now, errLookup)
		wait, failingFor := h.Until.Sub(now), now.Sub(t0)
		d := min(max(failingFor, album.LookupRetryMin), album.LookupRetryMax)
		assertWithin(t, wait, d/2, d, "a wait after failing for "+failingFor.String())
		last, now = wait, h.Until
	}
	assertWithin(t, last, album.LookupRetryMax/2, album.LookupRetryMax, "a day of failures waits at the cap")
}

// An Album whose TracksSynced condition has said a lookup failed for days
// waits at the cap after its first failure in this process, so a restarted
// manager asks once for each such Album rather than every 30 s again. A
// condition false for a release selection (no failed lookup) seeds nothing.
func TestALookupFailingSinceBeforeThisProcessStartsAtTheCap(t *testing.T) {
	c := album.NewReleaseCache(0)
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)

	failing := lookupAlbum("failing", now.Add(-time.Hour), tracksSyncedFalse("RPCError", now.Add(-96*time.Hour)))
	assertWithin(t, c.Failed(failing, now, errLookup).Until.Sub(now), album.LookupRetryMax/2, album.LookupRetryMax,
		"four days of failed lookups")

	selected := lookupAlbum("selected", now.Add(-time.Hour), tracksSyncedFalse(string(album.SelectionNoAcceptedRelease), now.Add(-96*time.Hour)))
	assertWithin(t, c.Failed(selected, now, errLookup).Until.Sub(now), album.LookupRetryMin/2, album.LookupRetryMin,
		"a release selection's false condition is no failed lookup")
}

// Albums that fail at one instant do not come back at one instant.
func TestAlbumsFailingTogetherAreSpreadOut(t *testing.T) {
	c := album.NewReleaseCache(0)
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	var first, last time.Time
	for i := range 200 {
		alb := lookupAlbum(fmt.Sprintf("album-%d", i), now.Add(-time.Hour),
			tracksSyncedFalse("RPCTimeout", now.Add(-96*time.Hour)))
		u := c.Failed(alb, now, errLookup).Until
		require.True(t, u.After(now), "every failure is held")
		if first.IsZero() {
			first, last = u, u
		}
		if u.Before(first) {
			first = u
		}
		if u.After(last) {
			last = u
		}
	}
	assert.Greater(t, last.Sub(first), 10*time.Minute, "200 holds spread across the jitter")
}

// A fetched listing clears the hold, and so does fresh metadata: the
// gateway's refresh caches the document the next lookup is answered from.
func TestAHoldEndsWithAFetchOrFreshMetadata(t *testing.T) {
	c := album.NewReleaseCache(0)
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)

	fetched := lookupAlbum("fetched", now.Add(-time.Hour), nil)
	c.Failed(fetched, now, errLookup)
	_, held := c.Held(fetched, now.Add(time.Second))
	require.True(t, held)
	c.Put(fetched, []pkgmetadata.AlbumRelease{{Title: "Border Wars"}})
	_, held = c.Held(fetched, now.Add(time.Second))
	assert.False(t, held, "a fetch clears the hold")

	refreshed := lookupAlbum("refreshed", now.Add(-time.Hour), nil)
	c.Failed(refreshed, now, errLookup)
	_, held = c.Held(refreshed, now.Add(2*time.Second))
	require.True(t, held)
	refreshed.Status.Metadata.RefreshedAt = metav1.NewTime(now.Add(time.Second))
	_, held = c.Held(refreshed, now.Add(2*time.Second))
	assert.False(t, held, "fresh metadata is not held by the stale document's failure")
}
