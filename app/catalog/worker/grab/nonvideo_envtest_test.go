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

package grab_test

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/worker/grab"
	"github.com/mediactl/clustarr/app/catalog/worker/grab/downloads"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/quality"
)

// nonVideoStatus is the part of a non-video item's status these tests read,
// whatever its kind.
type nonVideoStatus struct {
	obj            client.Object
	phase          string
	pendingGrab    *catalogv1alpha1.PendingGrab
	lastSearchedAt *metav1.Time
	searchAttempts commonv1.Attempts
}

// nonVideoKind builds one non-video item, its container, and a realistic
// steady state: the reconciler's phase (or, for an Issue, state) under
// k8s.ManagerCatalogarr, and a delayed grab's pendingGrab under
// k8s.ManagerCatalogarrGrab. A blank object could not show a server-side-apply
// release.
type nonVideoKind struct {
	name string
	// setup creates the item and its container and returns the grab target.
	setup func(t *testing.T, ctx context.Context, c client.Client, ns string) commonv1.MediaRef
	// read fetches the item's status.
	read func(t *testing.T, ctx context.Context, c client.Client, ns, name string) nonVideoStatus
	// wantProfile is the quality profile the Download must record: the one
	// the item inherits, exactly as the search worker ranked it.
	wantProfile string
	// wantPhase is the reconciler's phase the grab must leave alone.
	wantPhase string
}

func delayedPendingGrab() *catalogac.PendingGrabApplyConfiguration {
	return catalogac.PendingGrab().
		WithReleaseTitle("Some.Release").
		WithProtocol(commonv1.ProtocolUsenet).
		WithGrabAt(metav1.NewTime(testNow.Add(time.Hour)))
}

func nonVideoKinds() []nonVideoKind {
	return []nonVideoKind{
		{
			// An album with no override inherits its Artist's profile.
			name: "album", wantProfile: "music-artist", wantPhase: string(catalogv1alpha1.AlbumPhaseDelayed),
			setup: func(t *testing.T, ctx context.Context, c client.Client, ns string) commonv1.MediaRef {
				require.NoError(t, c.Create(ctx, &catalogv1alpha1.Artist{
					ObjectMeta: metav1.ObjectMeta{Name: "radiohead", Namespace: ns},
					Spec: catalogv1alpha1.ArtistSpec{
						MusicBrainzID: "a74b1b7f-71a5-4011-9441-d0b5e4122711", QualityProfileRef: "music-artist", RootFolderRef: "music",
						DelayProfileRef: ptr.To("slow-music"), Tags: []string{"lossless"},
					},
				}))
				require.NoError(t, c.Create(ctx, &catalogv1alpha1.Album{
					ObjectMeta: metav1.ObjectMeta{Name: "radiohead-kid-a", Namespace: ns},
					Spec:       catalogv1alpha1.AlbumSpec{ArtistRef: "radiohead", ReleaseGroupID: "b8048f24-c026-3398-b23a-b5e30716ea6f"},
				}))
				_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarr, catalogac.Album("radiohead-kid-a", ns).WithStatus(
					catalogac.AlbumStatus().WithPhase(catalogv1alpha1.AlbumPhaseDelayed)))
				require.NoError(t, err)
				_, err = k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrGrab, catalogac.Album("radiohead-kid-a", ns).WithStatus(
					catalogac.AlbumStatus().WithPendingGrab(delayedPendingGrab())))
				require.NoError(t, err)
				return commonv1.MediaRef{Kind: commonv1.MediaKindAlbum, Name: "radiohead-kid-a"}
			},
			read: func(t *testing.T, ctx context.Context, c client.Client, ns, name string) nonVideoStatus {
				var a catalogv1alpha1.Album
				require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &a))
				return nonVideoStatus{&a, string(a.Status.Phase), a.Status.PendingGrab, a.Status.LastSearchedAt, a.Status.SearchAttempts}
			},
		},
		{
			// A book's own override wins over its Author's profile.
			name: "book", wantProfile: "book-override", wantPhase: string(catalogv1alpha1.BookPhaseDelayed),
			setup: func(t *testing.T, ctx context.Context, c client.Client, ns string) commonv1.MediaRef {
				require.NoError(t, c.Create(ctx, &catalogv1alpha1.Author{
					ObjectMeta: metav1.ObjectMeta{Name: "frank-herbert", Namespace: ns},
					Spec: catalogv1alpha1.AuthorSpec{
						OpenLibraryID: "OL79034A", QualityProfileRef: "book-author", RootFolderRef: "books",
					},
				}))
				require.NoError(t, c.Create(ctx, &catalogv1alpha1.Book{
					ObjectMeta: metav1.ObjectMeta{Name: "dune", Namespace: ns},
					Spec: catalogv1alpha1.BookSpec{
						AuthorRef: ptr.To("frank-herbert"), WorkID: "OL893415W", QualityProfileRef: ptr.To("book-override"),
					},
				}))
				_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarr, catalogac.Book("dune", ns).WithStatus(
					catalogac.BookStatus().WithPhase(catalogv1alpha1.BookPhaseDelayed)))
				require.NoError(t, err)
				_, err = k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrGrab, catalogac.Book("dune", ns).WithStatus(
					catalogac.BookStatus().WithPendingGrab(delayedPendingGrab())))
				require.NoError(t, err)
				return commonv1.MediaRef{Kind: commonv1.MediaKindBook, Name: "dune"}
			},
			read: func(t *testing.T, ctx context.Context, c client.Client, ns, name string) nonVideoStatus {
				var b catalogv1alpha1.Book
				require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &b))
				return nonVideoStatus{&b, string(b.Status.Phase), b.Status.PendingGrab, b.Status.LastSearchedAt, b.Status.SearchAttempts}
			},
		},
		{
			// An audiobook is standalone: everything is its own.
			name: "audiobook", wantProfile: "audiobook", wantPhase: string(catalogv1alpha1.AudiobookPhaseDelayed),
			setup: func(t *testing.T, ctx context.Context, c client.Client, ns string) commonv1.MediaRef {
				require.NoError(t, c.Create(ctx, &catalogv1alpha1.Audiobook{
					ObjectMeta: metav1.ObjectMeta{Name: "guards-guards", Namespace: ns},
					Spec:       catalogv1alpha1.AudiobookSpec{ASIN: "B002V1A0WE", QualityProfileRef: "audiobook", RootFolderRef: "audiobooks"},
				}))
				_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarr, catalogac.Audiobook("guards-guards", ns).WithStatus(
					catalogac.AudiobookStatus().WithPhase(catalogv1alpha1.AudiobookPhaseDelayed)))
				require.NoError(t, err)
				_, err = k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrGrab, catalogac.Audiobook("guards-guards", ns).WithStatus(
					catalogac.AudiobookStatus().WithPendingGrab(delayedPendingGrab())))
				require.NoError(t, err)
				return commonv1.MediaRef{Kind: commonv1.MediaKindAudiobook, Name: "guards-guards"}
			},
			read: func(t *testing.T, ctx context.Context, c client.Client, ns, name string) nonVideoStatus {
				var a catalogv1alpha1.Audiobook
				require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &a))
				return nonVideoStatus{&a, string(a.Status.Phase), a.Status.PendingGrab, a.Status.LastSearchedAt, a.Status.SearchAttempts}
			},
		},
		{
			// An issue is ranked and grabbed under its Comic's profile, and
			// since X15 shows its wait in pendingGrab like the rest.
			name: "issue", wantProfile: "comic", wantPhase: string(catalogv1alpha1.IssueStateDelayed),
			setup: func(t *testing.T, ctx context.Context, c client.Client, ns string) commonv1.MediaRef {
				require.NoError(t, c.Create(ctx, &catalogv1alpha1.Comic{
					ObjectMeta: metav1.ObjectMeta{Name: "saga", Namespace: ns},
					Spec: catalogv1alpha1.ComicSpec{
						Source: catalogv1alpha1.ComicSourceComicVine, SourceID: "4050-46644",
						QualityProfileRef: "comic", RootFolderRef: "comics",
					},
				}))
				require.NoError(t, c.Create(ctx, &catalogv1alpha1.Issue{
					ObjectMeta: metav1.ObjectMeta{Name: "saga-00001.0", Namespace: ns},
					Spec:       catalogv1alpha1.IssueSpec{ComicRef: "saga", Number: "1", CalculatedNumberCentis: 100},
				}))
				_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarr, catalogac.Issue("saga-00001.0", ns).WithStatus(
					catalogac.IssueStatus().WithState(catalogv1alpha1.IssueStateDelayed)))
				require.NoError(t, err)
				_, err = k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrGrab, catalogac.Issue("saga-00001.0", ns).WithStatus(
					catalogac.IssueStatus().WithPendingGrab(delayedPendingGrab())))
				require.NoError(t, err)
				return commonv1.MediaRef{Kind: commonv1.MediaKindIssue, Name: "saga-00001.0"}
			},
			read: func(t *testing.T, ctx context.Context, c client.Client, ns, name string) nonVideoStatus {
				var iss catalogv1alpha1.Issue
				require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &iss))
				return nonVideoStatus{&iss, string(iss.Status.State), iss.Status.PendingGrab, iss.Status.LastSearchedAt, iss.Status.SearchAttempts}
			},
		},
	}
}

func usenetRelease(guid, title string) commonv1.ReleaseInfo {
	return commonv1.ReleaseInfo{
		GUID: guid, IndexerRef: "my-indexer", IndexerName: "my-indexer",
		Protocol:    commonv1.ProtocolUsenet,
		DownloadURL: "https://example.invalid/nzb/" + guid,
		Title:       title,
		PublishedAt: ptr.To(metav1.NewTime(testNow.Add(-time.Hour))),
	}
}

// TestGrab_NonVideoKindsTakeTheWholeGrabPath is the grab half of automatic
// non-video search. Until it, grab.StatusTargets refused album, book,
// audiobook and issue, so a delayed grab of one could never fire and the
// search worker's sink dropped their approved releases; the search worker
// gated those kinds' automatic searches off rather than spend indexer
// queries on results nothing could grab.
//
// Each kind goes through the real scheduled grab (Handler.Handle over a
// pending entry) from its steady state, and must come out with:
//   - one Download whose spec.target is exactly the item (no keys), owned by
//     it, recording the profile the item inherits, with ResolveSource's
//     source;
//   - the item's lease held by that Download;
//   - the consumed pendingGrab cleared and nothing
//     else released -- the reconciler's phase stays, and the grab manager
//     owns no activeDownloadRef (R-5);
//   - the double-grab guard refusing a second release while it is live;
//   - the search backoff advancing on it (RecordSearchAttempt).
func TestGrab_NonVideoKindsTakeTheWholeGrabPath(t *testing.T) {
	for _, k := range nonVideoKinds() {
		t.Run(k.name, func(t *testing.T) {
			ctx := context.Background()
			c := newTestClient(t)
			ns := newNamespace(t, ctx, c)
			target := k.setup(t, ctx, c, ns)
			bus := newTestBus(t, nil)
			release := usenetRelease("guid-"+k.name, "Some.Release")
			pendingKey := seedPending(t, ctx, bus, ns, target, nil, release, downloadv1alpha1.GrabSourceRSS)

			h := grab.NewHandler(grab.Deps{Client: c, Reader: c, Bus: bus, Now: fixedNow(testNow)})
			require.NoError(t, h.Handle(ctx, grabTaskMessage(t, ns, target, nil)), "the scheduled grab of a %s", k.name)

			var list downloadv1alpha1.DownloadList
			require.NoError(t, c.List(ctx, &list, client.InNamespace(ns)))
			require.Len(t, list.Items, 1)
			dl := list.Items[0]
			item := k.read(t, ctx, c, ns, target.Name)
			assert.Equal(t, k8s.ChildName(target.Name, release.GUID), dl.Name)
			assert.Equal(t, target, dl.Spec.Target, "spec.target is the item itself, with no keys")
			assert.Equal(t, k.wantProfile, dl.Spec.QualityProfileRef, "the Download records the profile the item inherits")
			assert.Equal(t, downloadv1alpha1.GrabSourceRSS, dl.Spec.GrabbedBy)
			wantSource, err := downloads.ResolveSource(release)
			require.NoError(t, err)
			assert.Equal(t, wantSource, dl.Spec.Source)
			require.Len(t, dl.OwnerReferences, 1)
			assert.Equal(t, item.obj.GetUID(), dl.OwnerReferences[0].UID, "the item owns its Download")

			entry, err := bus.KV(events.BucketLeases).Get(ctx, events.LeaseKey(grab.MediaKey(ns, target)))
			require.NoError(t, err)
			assert.Equal(t, dl.Name, string(entry.Value))
			_, err = bus.KV(events.BucketPending).Get(ctx, pendingKey)
			assert.ErrorIs(t, err, events.ErrKeyNotFound)

			assert.Nil(t, item.pendingGrab, "the consumed pendingGrab must be cleared")
			assert.Equal(t, k.wantPhase, item.phase, "the reconciler's field is not the grab path's to touch")
			assert.NotContains(t, managerStatusFields(item.obj, k8s.ManagerCatalogarrGrab), "activeDownloadRef")

			// The guard: while that Download is live, another release for
			// the same item is a duplicate.
			err = grab.PerformGrabForTest(ctx, grab.Deps{Client: c, Reader: c, Bus: bus, Now: fixedNow(testNow)},
				ns, target, nil, usenetRelease("guid-"+k.name+"-2", "Another.Release"), downloadv1alpha1.GrabSourceSearch)
			require.ErrorIs(t, err, grab.ErrDuplicateGrab)
			require.NoError(t, c.List(ctx, &list, client.InNamespace(ns)))
			assert.Len(t, list.Items, 1)

			// The backoff ladder advances for the kind as well.
			require.NoError(t, grab.RecordSearchAttempt(ctx, c, ns, target, testNow))
			item = k.read(t, ctx, c, ns, target.Name)
			require.NotNil(t, item.lastSearchedAt)
			assert.EqualValues(t, 1, item.searchAttempts.Count)
			assert.Equal(t, k.wantPhase, item.phase)
		})
	}
}

// TestDecide_NonVideoKindsHonourTheirDelayProfile: a non-video grab under a
// delay is held, not grabbed at once and not dropped, and every kind records
// it in pendingGrab -- an Issue too since X15, which until then was scheduled
// with nothing written to it and so could never read delayed.
func TestDecide_NonVideoKindsHonourTheirDelayProfile(t *testing.T) {
	profile := hdBlurayWeb(t)
	bottom := profile.Tiers[len(profile.Tiers)-1][0].Quality
	for _, k := range nonVideoKinds() {
		t.Run(k.name, func(t *testing.T) {
			ctx := context.Background()
			c := newTestClient(t)
			ns := newNamespace(t, ctx, c)
			target := k.setup(t, ctx, c, ns)
			// Start from a wanted item with nothing pending.
			seedClear(t, ctx, c, ns, target)
			bus := newTestBus(t, nil)

			release := usenetRelease("guid-"+k.name, "Some.Release")
			release.Quality = bottom
			require.NoError(t, grab.Decide(ctx, grab.Deps{Client: c, Bus: bus, Now: fixedNow(testNow)}, profile,
				catalogv1alpha1.DelayProfileSpec{UsenetDelayMinutes: 30, BypassIfHighestQuality: boolPtr(false)},
				grab.Approved{Namespace: ns, Target: target, Release: release, GrabbedBy: downloadv1alpha1.GrabSourceSearch}))

			var list downloadv1alpha1.DownloadList
			require.NoError(t, c.List(ctx, &list, client.InNamespace(ns)))
			assert.Empty(t, list.Items, "a delayed grab creates no Download yet")
			_, err := bus.KV(events.BucketPending).Get(ctx, events.PendingKey(grab.MediaKey(ns, target)))
			require.NoError(t, err, "the delayed grab is scheduled for every kind")

			after := k.read(t, ctx, c, ns, target.Name)
			require.NotNil(t, after.pendingGrab, "the delayed grab is recorded on the item")
			assert.True(t, after.pendingGrab.GrabAt.Time.Equal(testNow.Add(30*time.Minute)))
		})
	}
}

// seedClear releases the fixture's pendingGrab (an empty declaration by its
// manager), so a Decide test starts from a wanted item.
func seedClear(t *testing.T, ctx context.Context, c client.Client, ns string, target commonv1.MediaRef) {
	t.Helper()
	var err error
	switch target.Kind {
	case commonv1.MediaKindAlbum:
		_, err = k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrGrab, catalogac.Album(target.Name, ns).WithStatus(catalogac.AlbumStatus()))
	case commonv1.MediaKindBook:
		_, err = k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrGrab, catalogac.Book(target.Name, ns).WithStatus(catalogac.BookStatus()))
	case commonv1.MediaKindAudiobook:
		_, err = k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrGrab, catalogac.Audiobook(target.Name, ns).WithStatus(catalogac.AudiobookStatus()))
	case commonv1.MediaKindIssue:
		_, err = k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrGrab, catalogac.Issue(target.Name, ns).WithStatus(catalogac.IssueStatus()))
	default:
		t.Fatalf("seedClear: %s has no pendingGrab", target.Kind)
	}
	require.NoError(t, err)
}

// TestSink_DeliversANonVideoReleaseAndDropsAContainerLoudly: the search
// worker's sink now grabs an album -- the path whose silence gated automatic
// non-video search off -- and an approved release for a kind the grab path
// still cannot grab (a container, such as an Artist) is dropped with a log
// line saying so rather than with nothing at all.
func TestSink_DeliversANonVideoReleaseAndDropsAContainerLoudly(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ns := newNamespace(t, ctx, c)
	album := nonVideoKinds()[0]
	target := album.setup(t, ctx, c, ns)

	var logs bytes.Buffer
	ctx = logging.NewContext(ctx, slog.New(slog.NewTextHandler(&logs, nil)))
	sink := grab.Sink{
		Deps:           grab.Deps{Client: c, Reader: c, Bus: newTestBus(t, nil), Now: fixedNow(testNow)},
		ResolveProfile: func(context.Context, string) (quality.Profile, error) { return hdBlurayWeb(t), nil },
		ResolveDelay: func(context.Context, string, *string, []string) (catalogv1alpha1.DelayProfileSpec, error) {
			return catalogv1alpha1.DelayProfileSpec{}, nil
		},
	}
	approved := []commonv1.ReleaseDecision{{ReleaseInfo: usenetRelease("guid-sink", "Radiohead - Kid A (2000) [FLAC]"), Approved: true}}

	require.NoError(t, sink.Deliver(ctx, ns, target, approved, downloadv1alpha1.GrabSourceSearch))
	var list downloadv1alpha1.DownloadList
	require.NoError(t, c.List(ctx, &list, client.InNamespace(ns)))
	require.Len(t, list.Items, 1, "the sink grabs an approved album release")
	assert.Equal(t, target, list.Items[0].Spec.Target)
	assert.Equal(t, downloadv1alpha1.GrabSourceSearch, list.Items[0].Spec.GrabbedBy)

	require.NoError(t, sink.Deliver(ctx, ns, commonv1.MediaRef{Kind: commonv1.MediaKindArtist, Name: "radiohead"}, approved,
		downloadv1alpha1.GrabSourceSearch),
		"an ungrabbable kind is not worth a redelivery")
	require.NoError(t, c.List(ctx, &list, client.InNamespace(ns)))
	assert.Len(t, list.Items, 1)
	assert.Contains(t, logs.String(), "dropping an approved release", "the drop must be visible")
	assert.Contains(t, logs.String(), "kind=artist")
}
