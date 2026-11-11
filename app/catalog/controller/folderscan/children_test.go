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

package folderscan_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/album"
	"github.com/mediactl/clustarr/app/catalog/controller/folderscan"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
)

var musicRoot = &catalogv1alpha1.RootFolder{
	ObjectMeta: metav1.ObjectMeta{Namespace: "music", Name: "music"},
	Spec:       catalogv1alpha1.RootFolderSpec{Path: "/data/media/music", Kind: catalogv1alpha1.RootFolderKindMusic},
}

const artistFolder = "/data/media/music/Muse"

func scannedArtist() *catalogv1alpha1.Artist {
	return &catalogv1alpha1.Artist{ObjectMeta: metav1.ObjectMeta{
		Namespace: "music", Name: "muse-9c9f1380", UID: "artist-uid", Generation: 1,
	}}
}

func musicScans(t *testing.T, c client.Client) []catalogv1alpha1.LibraryScan {
	t.Helper()
	var list catalogv1alpha1.LibraryScanList
	require.NoError(t, c.List(context.Background(), &list, client.InNamespace("music")))
	return list.Items
}

// albumsOf is a Request's children fields for an artist with count albums of
// which pending have no track list yet, as the Artist's call site fills them.
func albumsOf(req folderscan.Request, pending, count int) folderscan.Request {
	req.Children, req.Awaits = "albums", "track lists"
	req.ChildrenSettled, req.Pending, req.Count = pending == 0, pending, count
	return req
}

// TestPassWaitsForTheChildrensLists is design D18: on talos a scan created
// 390 artists whose albums are thousands of MusicBrainz lookups at one a
// second, so a folder scan asked 30 s after the albums were listed would
// find no Album with a track list and attribute nothing -- and it is the
// artist's only scan. The scan waits, Unknown and Settling, naming how many
// albums are still to list their tracks, with no timer (each album's listing
// wakes the artist); the pass that finds the last one in turns it False, and
// the 30 s settle counts from that pass, however long the wait was.
func TestPassWaitsForTheChildrensLists(t *testing.T) {
	ctx := context.Background()
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).Build()
	folders := &fakeFolders{holds: map[string]bool{artistFolder: true}}
	a := scannedArtist()
	base := folderscan.Request{
		Item: a, Root: musicRoot, Path: artistFolder, HasChildren: true,
		FieldManager: k8s.ManagerCatalog, Now: time.Now(),
	}
	var conds []metav1.Condition

	// The fan-out lists 23 albums, none with tracks: the scan is owed and
	// waits for them.
	out := folderscan.Pass(ctx, c, folders, albumsOf(base, 23, 23), &conds)
	assert.Equal(t, folderscan.Outcome{}, out, "no timer: each album's listing wakes the artist")
	cond := k8s.FindCondition(conds, catalogv1alpha1.SeriesConditionFolderScanned)
	require.NotNil(t, cond, "the first albums record the scan they owe")
	assert.Equal(t, metav1.ConditionUnknown, cond.Status)
	assert.Equal(t, folderscan.ReasonSettling, cond.Reason)
	assert.Equal(t, "waiting for the track lists of 23 of its 23 albums; its folder is scanned for their files once they are in", cond.Message)

	// Hours of lookups later, 7 still to come: still waiting, the count
	// moving, nothing read and nothing scanned however long it has been.
	cond.LastTransitionTime = metav1.NewTime(time.Now().Add(-3 * time.Hour))
	waitingSince := cond.LastTransitionTime
	base.HadChildren = true // the status records them now
	base.Now = time.Now()
	out = folderscan.Pass(ctx, c, folders, albumsOf(base, 7, 23), &conds)
	assert.Equal(t, folderscan.Outcome{}, out)
	cond = k8s.FindCondition(conds, catalogv1alpha1.SeriesConditionFolderScanned)
	assert.Equal(t, metav1.ConditionUnknown, cond.Status)
	assert.Equal(t, "waiting for the track lists of 7 of its 23 albums; its folder is scanned for their files once they are in", cond.Message)
	assert.Equal(t, waitingSince, cond.LastTransitionTime, "the wait is one state; only its count moves")
	assert.Zero(t, folders.calls, "nothing is read while the albums list")
	assert.Empty(t, musicScans(t, c))

	// The last album lists its tracks: the settle starts now, not when the
	// albums were first listed three hours ago.
	out = folderscan.Pass(ctx, c, folders, albumsOf(base, 0, 23), &conds)
	assert.Equal(t, folderscan.Outcome{Retry: folderscan.SettleAfter}, out)
	cond = k8s.FindCondition(conds, catalogv1alpha1.SeriesConditionFolderScanned)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, folderscan.ReasonSettling, cond.Reason)
	assert.Equal(t, "the track lists of all 23 of its albums are in; its folder is scanned for their files once they settle", cond.Message)
	assert.True(t, cond.LastTransitionTime.After(waitingSince.Add(time.Hour)), "the settle counts from the pass that found the last album in")
	assert.Zero(t, folders.calls)
	assert.Empty(t, musicScans(t, c), "no scan before the last album's track list settles into the import agent's cache")

	// Within the settle: the rest of it.
	base.Now = cond.LastTransitionTime.Add(10 * time.Second)
	out = folderscan.Pass(ctx, c, folders, albumsOf(base, 0, 23), &conds)
	assert.Equal(t, folderscan.Outcome{Retry: 20 * time.Second}, out)
	assert.Empty(t, musicScans(t, c))

	// Once it is up: the one scan of the artist's folder.
	base.Now = cond.LastTransitionTime.Add(folderscan.SettleAfter)
	out = folderscan.Pass(ctx, c, folders, albumsOf(base, 0, 23), &conds)
	assert.Equal(t, folderscan.ScanName(a), out.Scan)
	got := musicScans(t, c)
	require.Len(t, got, 1)
	assert.Equal(t, catalogv1alpha1.LibraryScanSpec{
		RootFolderRef: "music", Subpath: "Muse", Mode: catalogv1alpha1.ScanModeIncremental,
	}, got[0].Spec)
	cond = k8s.FindCondition(conds, catalogv1alpha1.SeriesConditionFolderScanned)
	assert.Equal(t, metav1.ConditionTrue, cond.Status)
	assert.Equal(t, folderscan.ReasonScanRequested, cond.Reason)
}

// A child that falls back to waiting while the scan settles -- an artist's
// new album from a later fan-out, or a condition left False by a manager
// that did not yet wait -- returns the scan to waiting, however long the
// settle has run; and a scan already asked for never waits again.
func TestPassWaitsAgainForAChildThatFallsBack(t *testing.T) {
	ctx := context.Background()
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).Build()
	folders := &fakeFolders{holds: map[string]bool{artistFolder: true}}
	base := folderscan.Request{
		Item: scannedArtist(), Root: musicRoot, Path: artistFolder,
		HadChildren: true, HasChildren: true, FieldManager: k8s.ManagerCatalog, Now: time.Now(),
	}

	conds := []metav1.Condition{settled()}
	out := folderscan.Pass(ctx, c, folders, albumsOf(base, 1, 24), &conds)
	assert.Equal(t, folderscan.Outcome{}, out)
	cond := k8s.FindCondition(conds, catalogv1alpha1.SeriesConditionFolderScanned)
	assert.Equal(t, metav1.ConditionUnknown, cond.Status)
	assert.Equal(t, folderscan.ReasonSettling, cond.Reason)
	assert.Contains(t, cond.Message, "1 of its 24 albums")
	assert.Zero(t, folders.calls)
	assert.Empty(t, musicScans(t, c))

	done := []metav1.Condition{{
		Type: catalogv1alpha1.SeriesConditionFolderScanned, Status: metav1.ConditionTrue,
		Reason: folderscan.ReasonScanRequested, LastTransitionTime: metav1.NewTime(time.Now().Add(-time.Hour)),
	}}
	out = folderscan.Pass(ctx, c, folders, albumsOf(base, 1, 24), &done)
	assert.Equal(t, folderscan.Outcome{}, out)
	assert.Equal(t, metav1.ConditionTrue, k8s.FindCondition(done, catalogv1alpha1.SeriesConditionFolderScanned).Status,
		"the scan is the item's for life")
}

// tracksSynced is the Album reconciler's TracksSynced condition (its
// conditionTracksSynced), with a reason its writers produce.
func tracksSynced(status metav1.ConditionStatus, reason string) metav1.Condition {
	return metav1.Condition{Type: "TracksSynced", Status: status, Reason: reason}
}

// An Album's track list is in when it lists tracks, or when its listing was
// answered with none; it is still to come before its first pass lands and
// while its ask fails for a reason the Album retries. MetadataReady False
// alone, which every Album reads before its document lands -- its first pass
// included, whatever the listing did -- says nothing either way.
func TestAlbumListed(t *testing.T) {
	refreshing := metav1.Condition{Type: catalogv1alpha1.AlbumConditionMetadataReady, Status: metav1.ConditionFalse, Reason: "Refreshing"}
	fresh := metav1.Condition{Type: catalogv1alpha1.AlbumConditionMetadataReady, Status: metav1.ConditionTrue, Reason: k8s.ReasonReconciled}
	tracks := []catalogv1alpha1.Track{{RecordingID: "r1", Medium: 1, Number: 1, Title: "Uprising"}}
	for name, tc := range map[string]struct {
		status catalogv1alpha1.AlbumStatus
		listed bool
	}{
		"just created by the fan-out":          {},
		"its document is still to come":        {status: catalogv1alpha1.AlbumStatus{Conditions: []metav1.Condition{refreshing}}},
		"its tracks are listed":                {status: catalogv1alpha1.AlbumStatus{Tracks: tracks, Conditions: []metav1.Condition{refreshing, tracksSynced(metav1.ConditionTrue, string(album.SelectionBest))}}, listed: true},
		"kept tracks through a failed ask":     {status: catalogv1alpha1.AlbumStatus{Tracks: tracks, Conditions: []metav1.Condition{tracksSynced(metav1.ConditionFalse, events.RequestTimeout.Reason())}}, listed: true},
		"the group lists no releases":          {status: catalogv1alpha1.AlbumStatus{Conditions: []metav1.Condition{fresh, tracksSynced(metav1.ConditionTrue, string(album.SelectionNoReleases))}}, listed: true},
		"no release the profile takes":         {status: catalogv1alpha1.AlbumStatus{Conditions: []metav1.Condition{fresh, tracksSynced(metav1.ConditionFalse, string(album.SelectionNoAcceptedRelease))}}, listed: true},
		"its pinned release is missing":        {status: catalogv1alpha1.AlbumStatus{Conditions: []metav1.Condition{tracksSynced(metav1.ConditionFalse, string(album.SelectionPinnedReleaseMissing))}}, listed: true},
		"the gateway answered with an error":   {status: catalogv1alpha1.AlbumStatus{Conditions: []metav1.Condition{refreshing, tracksSynced(metav1.ConditionFalse, events.RequestOther.Reason())}}, listed: true},
		"its ask timed out":                    {status: catalogv1alpha1.AlbumStatus{Conditions: []metav1.Condition{refreshing, tracksSynced(metav1.ConditionFalse, events.RequestTimeout.Reason())}}},
		"no metadata agent answered":           {status: catalogv1alpha1.AlbumStatus{Conditions: []metav1.Condition{refreshing, tracksSynced(metav1.ConditionFalse, events.RequestNoResponders.Reason())}}},
		"the metadata agent was busy":          {status: catalogv1alpha1.AlbumStatus{Conditions: []metav1.Condition{refreshing, tracksSynced(metav1.ConditionFalse, events.RequestBusy.Reason())}}},
		"a timed-out ask with its document in": {status: catalogv1alpha1.AlbumStatus{Conditions: []metav1.Condition{fresh, tracksSynced(metav1.ConditionFalse, events.RequestTimeout.Reason())}}},
	} {
		alb := catalogv1alpha1.Album{Status: tc.status}
		assert.Equal(t, tc.listed, folderscan.AlbumListed(&alb), name)
	}

	albums := []catalogv1alpha1.Album{
		{},
		{Status: catalogv1alpha1.AlbumStatus{Tracks: tracks}},
		{Status: catalogv1alpha1.AlbumStatus{Conditions: []metav1.Condition{tracksSynced(metav1.ConditionFalse, events.RequestTimeout.Reason())}}},
		{Status: catalogv1alpha1.AlbumStatus{Conditions: []metav1.Condition{tracksSynced(metav1.ConditionTrue, string(album.SelectionNoReleases))}}},
	}
	assert.Equal(t, 2, folderscan.AlbumsPending(albums))
	assert.Zero(t, folderscan.AlbumsPending(nil))
}

// A Book is in once its metadata is: a scan attributes a book file by the
// Book's title, which its document brings.
func TestBookListed(t *testing.T) {
	withDoc := catalogv1alpha1.Book{Status: catalogv1alpha1.BookStatus{Metadata: &catalogv1alpha1.BookMetadata{Title: "Dune"}}}
	assert.True(t, folderscan.BookListed(&withDoc))
	assert.False(t, folderscan.BookListed(&catalogv1alpha1.Book{}))
	assert.Equal(t, 1, folderscan.BooksPending([]catalogv1alpha1.Book{withDoc, {}, withDoc}))
}
