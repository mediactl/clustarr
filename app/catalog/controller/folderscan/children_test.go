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
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"
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
// which pending are not listed yet, as the Artist's call site fills them.
func albumsOf(req folderscan.Request, pending, count int) folderscan.Request {
	req.Children, req.Awaits = "albums", "metadata and track lists"
	req.ChildrenSettled, req.Pending, req.Count = pending == 0, pending, count
	return req
}

// TestPassWaitsForTheChildrensLists is design D18: on talos a scan created
// 390 artists whose albums are thousands of MusicBrainz lookups at one a
// second, so a folder scan asked 30 s after the albums were listed would
// find no Album with a track list and attribute nothing -- and it is the
// artist's only scan. The scan waits, Unknown and Settling, naming how many
// albums are still to come, with one timer, its deadline (each album's
// arrival wakes the artist); the pass that finds the last one in turns it
// False, and the 30 s settle counts from that pass, however long the wait
// was.
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
	assert.Equal(t, folderscan.Outcome{Retry: folderscan.ChildrenWait}, out,
		"one timer, the wait's deadline: each album's arrival wakes the artist")
	cond := k8s.FindCondition(conds, catalogv1alpha1.SeriesConditionFolderScanned)
	require.NotNil(t, cond, "the first albums record the scan they owe")
	assert.Equal(t, metav1.ConditionUnknown, cond.Status)
	assert.Equal(t, folderscan.ReasonSettling, cond.Reason)
	assert.Equal(t, "waiting for the metadata and track lists of 23 of its 23 albums; its folder is scanned for their files once they are in", cond.Message)

	// Hours of lookups later, 7 still to come: still waiting, the count
	// moving, nothing read and nothing scanned, the deadline where it was.
	cond.LastTransitionTime = metav1.NewTime(time.Now().Add(-3 * time.Hour))
	waitingSince := cond.LastTransitionTime
	base.HadChildren = true // the status records them now
	base.Now = waitingSince.Add(3 * time.Hour)
	out = folderscan.Pass(ctx, c, folders, albumsOf(base, 7, 23), &conds)
	assert.Equal(t, folderscan.Outcome{Retry: folderscan.ChildrenWait - 3*time.Hour}, out)
	cond = k8s.FindCondition(conds, catalogv1alpha1.SeriesConditionFolderScanned)
	assert.Equal(t, metav1.ConditionUnknown, cond.Status)
	assert.Equal(t, "waiting for the metadata and track lists of 7 of its 23 albums; its folder is scanned for their files once they are in", cond.Message)
	assert.Equal(t, waitingSince, cond.LastTransitionTime, "the wait is one state; only its count moves")
	assert.Zero(t, folders.calls, "nothing is read while the albums list")
	assert.Empty(t, musicScans(t, c))

	// The last album is in: the settle starts now, not when the albums were
	// first listed three hours ago.
	out = folderscan.Pass(ctx, c, folders, albumsOf(base, 0, 23), &conds)
	assert.Equal(t, folderscan.Outcome{Retry: folderscan.SettleAfter}, out)
	cond = k8s.FindCondition(conds, catalogv1alpha1.SeriesConditionFolderScanned)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, folderscan.ReasonSettling, cond.Reason)
	assert.Equal(t, "the metadata and track lists of all 23 of its albums are in; its folder is scanned for their files once they settle", cond.Message)
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
// settle has run, its deadline begun again; so does one that falls back
// while the scan is pending (the folder's read failed, or the LibraryScan's
// create, each retried a minute later), which must not reach the scan
// before the child is back in; and a scan already asked for never waits
// again.
func TestPassWaitsAgainForAChildThatFallsBack(t *testing.T) {
	ctx := context.Background()
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).Build()
	folders := &fakeFolders{holds: map[string]bool{artistFolder: true}}
	base := folderscan.Request{
		Item: scannedArtist(), Root: musicRoot, Path: artistFolder,
		HadChildren: true, HasChildren: true, FieldManager: k8s.ManagerCatalog, Now: time.Now(),
	}

	pending := metav1.Condition{
		Type: catalogv1alpha1.SeriesConditionFolderScanned, Status: metav1.ConditionFalse,
		Reason: folderscan.ReasonPending, Message: "could not read its folder " + artistFolder + ": remediation: /data I/O call timed out",
		LastTransitionTime: metav1.NewTime(time.Now().Add(-time.Hour)),
	}
	for name, from := range map[string]metav1.Condition{"settling": settled(), "pending": pending} {
		conds := []metav1.Condition{from}
		out := folderscan.Pass(ctx, c, folders, albumsOf(base, 1, 24), &conds)
		assert.Equal(t, folderscan.Outcome{Retry: folderscan.ChildrenWait}, out, name)
		cond := k8s.FindCondition(conds, catalogv1alpha1.SeriesConditionFolderScanned)
		assert.Equal(t, metav1.ConditionUnknown, cond.Status, name)
		assert.Equal(t, folderscan.ReasonSettling, cond.Reason, name)
		assert.Contains(t, cond.Message, "1 of its 24 albums", name)
		assert.True(t, cond.LastTransitionTime.After(from.LastTransitionTime.Add(30*time.Minute)), "%s: the wait begins again", name)
	}
	assert.Zero(t, folders.calls, "nothing is read while an album is to come")
	assert.Empty(t, musicScans(t, c))

	done := []metav1.Condition{{
		Type: catalogv1alpha1.SeriesConditionFolderScanned, Status: metav1.ConditionTrue,
		Reason: folderscan.ReasonScanRequested, LastTransitionTime: metav1.NewTime(time.Now().Add(-time.Hour)),
	}}
	out := folderscan.Pass(ctx, c, folders, albumsOf(base, 1, 24), &done)
	assert.Equal(t, folderscan.Outcome{}, out)
	assert.Equal(t, metav1.ConditionTrue, k8s.FindCondition(done, catalogv1alpha1.SeriesConditionFolderScanned).Status,
		"the scan is the item's for life")
}

// booksOf is a Request's children fields for an author with count books of
// which pending have no document yet, as the Author's call site fills them.
func booksOf(req folderscan.Request, pending, count int) folderscan.Request {
	req.Children, req.Awaits = "books", "metadata"
	req.ChildrenSettled, req.Pending, req.Count = pending == 0, pending, count
	return req
}

// A child whose lookup fails for good -- a work Open Library answers 404
// for, whose Book the metadata worker's failure record leaves without a
// document, asked again on every backoff -- would hold its container's only
// scan for good. The wait ends at its deadline, ChildrenWait after it began:
// the pass past it asks for the scan with what is in, and says how many were
// not. A step of that scan that cannot be made yet (the folder's read fails)
// keeps the condition Unknown, so the deadline does not move and the next
// pass tries again at once rather than waiting another day.
func TestPassStopsWaitingAtTheDeadline(t *testing.T) {
	ctx := context.Background()
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).Build()
	const authorFolder = "/data/media/books/Frank Herbert"
	bookRoot := &catalogv1alpha1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Namespace: "books", Name: "books"},
		Spec:       catalogv1alpha1.RootFolderSpec{Path: "/data/media/books", Kind: catalogv1alpha1.RootFolderKindBook},
	}
	author := &catalogv1alpha1.Author{ObjectMeta: metav1.ObjectMeta{
		Namespace: "books", Name: "frank-herbert", UID: "author-uid", Generation: 1,
	}}
	bookScans := func() []catalogv1alpha1.LibraryScan {
		var list catalogv1alpha1.LibraryScanList
		require.NoError(t, c.List(ctx, &list, client.InNamespace("books")))
		return list.Items
	}
	folders := &fakeFolders{holds: map[string]bool{authorFolder: true}, err: errors.New("remediation: /data I/O call timed out")}
	began := time.Now().Add(-folderscan.ChildrenWait)
	conds := []metav1.Condition{{
		Type: catalogv1alpha1.SeriesConditionFolderScanned, Status: metav1.ConditionUnknown, Reason: folderscan.ReasonSettling,
		Message:            "waiting for the metadata of 1 of its 300 books; its folder is scanned for their files once they are in",
		LastTransitionTime: metav1.NewTime(began),
	}}
	base := folderscan.Request{
		Item: author, Root: bookRoot, Path: authorFolder, HadChildren: true, HasChildren: true,
		FieldManager: k8s.ManagerCatalog,
	}

	// A second before the deadline: still waiting, for that second.
	base.Now = began.Add(folderscan.ChildrenWait - time.Second)
	out := folderscan.Pass(ctx, c, folders, booksOf(base, 1, 300), &conds)
	assert.Equal(t, folderscan.Outcome{Retry: time.Second}, out)
	assert.Zero(t, folders.calls)

	// At the deadline the scan is asked for; the folder's read fails, and
	// the condition stays Unknown, its deadline where it was.
	base.Now = began.Add(folderscan.ChildrenWait)
	out = folderscan.Pass(ctx, c, folders, booksOf(base, 1, 300), &conds)
	assert.Equal(t, folderscan.Outcome{Retry: folderscan.RetryAfter}, out)
	assert.Equal(t, 1, folders.calls)
	cond := k8s.FindCondition(conds, catalogv1alpha1.SeriesConditionFolderScanned)
	assert.Equal(t, metav1.ConditionUnknown, cond.Status)
	assert.Equal(t, folderscan.ReasonPending, cond.Reason)
	assert.Contains(t, cond.Message, "could not read its folder")
	assert.True(t, cond.LastTransitionTime.Time.Equal(began), "the deadline does not move")
	assert.Empty(t, bookScans())

	// The retry a minute later, the book still without its document: the
	// scan, at once, naming the book it did not wait for.
	folders.err = nil
	base.Now = base.Now.Add(folderscan.RetryAfter)
	out = folderscan.Pass(ctx, c, folders, booksOf(base, 1, 300), &conds)
	assert.Equal(t, folderscan.ScanName(author), out.Scan)
	assert.Zero(t, out.Retry)
	got := bookScans()
	require.Len(t, got, 1)
	assert.Equal(t, catalogv1alpha1.LibraryScanSpec{
		RootFolderRef: "books", Subpath: "Frank Herbert", Mode: catalogv1alpha1.ScanModeIncremental,
	}, got[0].Spec)
	cond = k8s.FindCondition(conds, catalogv1alpha1.SeriesConditionFolderScanned)
	assert.Equal(t, metav1.ConditionTrue, cond.Status)
	assert.Equal(t, folderscan.ReasonScanRequested, cond.Reason)
	assert.Equal(t, "LibraryScan "+got[0].Name+" scans Frank Herbert for the files that awaited its books, "+
		"though the metadata of 1 of its 300 books were not in after 24h0m0s", cond.Message)

	// Done: the book's document arriving later asks for nothing more.
	out = folderscan.Pass(ctx, c, folders, booksOf(base, 0, 300), &conds)
	assert.Equal(t, folderscan.Outcome{}, out)
	assert.Len(t, bookScans(), 1)

	// A wait the last child ends after its deadline released a scan that
	// could not be made yet settles as one that ended in time.
	late := []metav1.Condition{{
		Type: catalogv1alpha1.SeriesConditionFolderScanned, Status: metav1.ConditionUnknown, Reason: folderscan.ReasonPending,
		LastTransitionTime: metav1.NewTime(began),
	}}
	out = folderscan.Pass(ctx, c, folders, booksOf(base, 0, 300), &late)
	assert.Equal(t, folderscan.Outcome{Retry: folderscan.SettleAfter}, out)
	cond = k8s.FindCondition(late, catalogv1alpha1.SeriesConditionFolderScanned)
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, folderscan.ReasonSettling, cond.Reason)
}

// tracksSynced is the Album reconciler's TracksSynced condition (its
// conditionTracksSynced), with a reason its writers produce.
func tracksSynced(status metav1.ConditionStatus, reason string) metav1.Condition {
	return metav1.Condition{Type: "TracksSynced", Status: status, Reason: reason}
}

// gatewayError is the TracksSynced reason the Album reconciler writes when
// the metadata gateway answers its track listing with an error of its own
// (album.syncTracks: resp.Error wrapped as "metadata gateway: %s", then
// events.ClassifyRequestError), here the error the gateway's one-a-second
// MusicBrainz limiter returns when the caller's deadline would pass first,
// which on talos is most first asks, as rate.Limiter.Wait makes it.
func gatewayError(t *testing.T) string {
	t.Helper()
	lim := rate.NewLimiter(1, 1)
	require.True(t, lim.Allow(), "the burst is spent by the asks ahead")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := lim.Wait(ctx)
	require.Error(t, err)
	assert.NotErrorIs(t, err, context.DeadlineExceeded, "the limiter refuses before the deadline, with an error of its own")
	return events.ClassifyRequestError(fmt.Errorf("metadata gateway: %s", err.Error())).Reason()
}

// An Album is in once it carries what a scan attributes a track file by:
// its document (the title, year and release ids MatchAlbum reads) and its
// track list. The list is in when it lists tracks, or when its listing was
// answered with none; never on a failed ask, whatever the failure's class:
// the gateway's own errors (a MusicBrainz limiter or 503) read RPCError and
// are asked again like a timeout. Tracks the Album's first pass took from
// its direct lookup, before the queued metadata task brought its document,
// are not enough, and neither is MetadataReady False alone, which every
// Album reads before its document lands.
func TestAlbumListed(t *testing.T) {
	refreshing := metav1.Condition{Type: catalogv1alpha1.AlbumConditionMetadataReady, Status: metav1.ConditionFalse, Reason: "Refreshing"}
	fresh := metav1.Condition{Type: catalogv1alpha1.AlbumConditionMetadataReady, Status: metav1.ConditionTrue, Reason: k8s.ReasonReconciled}
	tracks := []catalogv1alpha1.Track{{RecordingID: "r1", Medium: 1, Number: 1, Title: "Uprising"}}
	doc := &catalogv1alpha1.AlbumMetadata{Title: "The Resistance", RefreshedAt: metav1.Now()}
	rpcError := gatewayError(t)
	require.Equal(t, events.RequestOther.Reason(), rpcError)
	for name, tc := range map[string]struct {
		status catalogv1alpha1.AlbumStatus
		listed bool
	}{
		"just created by the fan-out":          {},
		"its document is still to come":        {status: catalogv1alpha1.AlbumStatus{Conditions: []metav1.Condition{refreshing}}},
		"its tracks are listed":                {status: catalogv1alpha1.AlbumStatus{Metadata: doc, Tracks: tracks, Conditions: []metav1.Condition{fresh, tracksSynced(metav1.ConditionTrue, string(album.SelectionBest))}}, listed: true},
		"tracks listed before its document":    {status: catalogv1alpha1.AlbumStatus{Tracks: tracks, Conditions: []metav1.Condition{refreshing, tracksSynced(metav1.ConditionTrue, string(album.SelectionBest))}}},
		"a selection leaf alone, no document":  {status: catalogv1alpha1.AlbumStatus{Metadata: &catalogv1alpha1.AlbumMetadata{SelectedReleaseID: "rel"}, Tracks: tracks}},
		"kept tracks through a failed ask":     {status: catalogv1alpha1.AlbumStatus{Metadata: doc, Tracks: tracks, Conditions: []metav1.Condition{tracksSynced(metav1.ConditionFalse, events.RequestTimeout.Reason())}}, listed: true},
		"the group lists no releases":          {status: catalogv1alpha1.AlbumStatus{Metadata: doc, Conditions: []metav1.Condition{fresh, tracksSynced(metav1.ConditionTrue, string(album.SelectionNoReleases))}}, listed: true},
		"no release the profile takes":         {status: catalogv1alpha1.AlbumStatus{Metadata: doc, Conditions: []metav1.Condition{fresh, tracksSynced(metav1.ConditionFalse, string(album.SelectionNoAcceptedRelease))}}, listed: true},
		"its pinned release is missing":        {status: catalogv1alpha1.AlbumStatus{Metadata: doc, Conditions: []metav1.Condition{tracksSynced(metav1.ConditionFalse, string(album.SelectionPinnedReleaseMissing))}}, listed: true},
		"no release the profile takes, no doc": {status: catalogv1alpha1.AlbumStatus{Conditions: []metav1.Condition{refreshing, tracksSynced(metav1.ConditionFalse, string(album.SelectionNoAcceptedRelease))}}},
		"the gateway answered with an error":   {status: catalogv1alpha1.AlbumStatus{Metadata: doc, Conditions: []metav1.Condition{fresh, tracksSynced(metav1.ConditionFalse, rpcError)}}},
		"its ask timed out":                    {status: catalogv1alpha1.AlbumStatus{Metadata: doc, Conditions: []metav1.Condition{fresh, tracksSynced(metav1.ConditionFalse, events.RequestTimeout.Reason())}}},
		"no metadata agent answered":           {status: catalogv1alpha1.AlbumStatus{Metadata: doc, Conditions: []metav1.Condition{fresh, tracksSynced(metav1.ConditionFalse, events.RequestNoResponders.Reason())}}},
		"the metadata agent was busy":          {status: catalogv1alpha1.AlbumStatus{Metadata: doc, Conditions: []metav1.Condition{fresh, tracksSynced(metav1.ConditionFalse, events.RequestBusy.Reason())}}},
	} {
		alb := catalogv1alpha1.Album{Status: tc.status}
		assert.Equal(t, tc.listed, folderscan.AlbumListed(&alb), name)
	}

	albums := []catalogv1alpha1.Album{
		{},
		{Status: catalogv1alpha1.AlbumStatus{Metadata: doc, Tracks: tracks}},
		{Status: catalogv1alpha1.AlbumStatus{Tracks: tracks}},
		{Status: catalogv1alpha1.AlbumStatus{Metadata: doc, Conditions: []metav1.Condition{tracksSynced(metav1.ConditionFalse, rpcError)}}},
		{Status: catalogv1alpha1.AlbumStatus{Metadata: doc, Conditions: []metav1.Condition{tracksSynced(metav1.ConditionTrue, string(album.SelectionNoReleases))}}},
	}
	assert.Equal(t, 3, folderscan.AlbumsPending(albums))
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
