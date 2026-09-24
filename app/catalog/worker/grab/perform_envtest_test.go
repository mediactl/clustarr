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
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/worker/grab"
	"github.com/mediactl/clustarr/app/catalog/worker/grab/downloads"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
)

var testNow = time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)

// TestPerformGrab_CreatesDownloadAndPatchesStatus is §8.2's happy path: one
// deterministically named Download, owned by the target, whose spec.source is
// exactly downloads.ResolveSource's -- the mapping the Search controller's
// interactive grabs use too. The grab writes no status.phase, which is the
// Movie reconciler's under k8s.ManagerCatalogarr, and no
// status.activeDownloadRef, which ruling R-5 gives to that reconciler alone.
func TestPerformGrab_CreatesDownloadAndPatchesStatus(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ns := newNamespace(t, ctx, c)

	movie := newMovie(t, ctx, c, ns, "the-thing-1982")
	newIndexer(t, ctx, c, ns, "my-indexer", nil)

	profile := hdBlurayWeb(t)
	release := torrentRelease("guid-1", "my-indexer", profile.Tiers[0][0].Quality, 0)
	release.InfoHash = "0123456789abcdef0123456789abcdef01234567"
	target := commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: movie.Name}

	bus := newTestBus(t, nil)
	deps := grab.Deps{Client: c, Bus: bus, Now: fixedNow(testNow)}
	require.NoError(t, grab.PerformGrabForTest(ctx, deps, ns, target, nil, release, downloadv1alpha1.GrabSourceSearch))

	var downloadList downloadv1alpha1.DownloadList
	require.NoError(t, c.List(ctx, &downloadList, client.InNamespace(ns)))
	require.Len(t, downloadList.Items, 1)
	dl := downloadList.Items[0]

	assert.Equal(t, k8s.ChildName(movie.Name, "guid-1"), dl.Name, "the Download name must be <target>-<sha1(guid)[:10]>")
	assert.Equal(t, "guid-1", dl.Spec.Release.GUID)
	assert.Equal(t, "hd-bluray-web", dl.Spec.QualityProfileRef)
	assert.Equal(t, commonv1.ProtocolTorrent, dl.Spec.Protocol)
	assert.Equal(t, downloadv1alpha1.GrabSourceSearch, dl.Spec.GrabbedBy)
	assert.Equal(t, target, dl.Spec.Target)
	wantSource, err := downloads.ResolveSource(release)
	require.NoError(t, err)
	assert.Equal(t, wantSource, dl.Spec.Source, "both grab paths map a release through downloads.ResolveSource")
	require.NotNil(t, dl.Spec.Source.MagnetURL, "a magnet needs no indexer round-trip and wins")
	require.NotNil(t, dl.Spec.Source.ExpectedInfoHash, "the info-hash guard rides along with the magnet")
	require.Len(t, dl.OwnerReferences, 1)
	assert.Equal(t, movie.Name, dl.OwnerReferences[0].Name)

	var got catalogv1alpha1.Movie
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(movie), &got))
	assert.Nil(t, got.Status.ActiveDownloadRef, "performGrab must not write activeDownloadRef: the reconciler derives it (R-5)")
	assert.Empty(t, got.Status.Phase, "performGrab must never set Phase: that is the Movie reconciler's field")

	// The lease is held, not released: the next grab of this item asks
	// this Download whether it still occupies the item.
	entry, err := bus.KV(events.BucketLeases).Get(ctx, events.LeaseKey(grab.MediaKey(ns, target)))
	require.NoError(t, err)
	assert.Equal(t, dl.Name, string(entry.Value))
}

// TestPerformGrab_NeverWritesActiveDownloadRef is ruling R-5 at the level
// where an over-claim is visible at all. pkg/k8s forces ownership, so two
// managers writing one field never conflict and every value assertion passes
// either way; only metadata.managedFields shows who owns what.
//
// The movie starts in the pre-R-5 steady state -- a delayed grab under the
// grab manager, and an activeDownloadRef the grab manager also owned -- so
// the assertion also proves the migration: the grab path's next write
// releases the ref rather than re-declaring it, leaving the reconciler as its
// only writer.
func TestPerformGrab_NeverWritesActiveDownloadRef(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ns := newNamespace(t, ctx, c)

	movie := newMovie(t, ctx, c, ns, "the-thing-1982")
	newIndexer(t, ctx, c, ns, "my-indexer", nil)
	seedWorkerStatus(t, ctx, c, movie, "", nil)
	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrGrab, catalogac.Movie(movie.Name, ns).WithStatus(
		catalogac.MovieStatus().
			WithActiveDownloadRef("the-thing-1982-0000000000").
			WithPendingGrab(catalogac.PendingGrab().
				WithReleaseTitle("The.Thing.1982.1080p.BluRay.x264-GROUP").
				WithProtocol(commonv1.ProtocolTorrent).
				WithGrabAt(metav1.NewTime(testNow.Add(time.Hour))))))
	require.NoError(t, err)

	profile := hdBlurayWeb(t)
	target := commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: movie.Name}
	deps := grab.Deps{Client: c, Bus: newTestBus(t, nil), Now: fixedNow(testNow)}
	require.NoError(t, grab.PerformGrabForTest(ctx, deps, ns, target, nil,
		torrentRelease("guid-1", "my-indexer", profile.Tiers[0][0].Quality, 0), downloadv1alpha1.GrabSourceSearch))

	var got catalogv1alpha1.Movie
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(movie), &got))
	assert.Nil(t, got.Status.PendingGrab, "the grab consumed the pending candidate")
	assert.NotContains(t, managerStatusFields(&got, k8s.ManagerCatalogarrGrab), "activeDownloadRef",
		"k8s.ManagerCatalogarrGrab still owns status.activeDownloadRef after a grab; R-5 gives it to the reconciler alone")
}

// TestPerformGrab_DuplicateIsAckAndStop proves §8.2's "on exists -> ack and
// stop": a lease already held means no second Download and no retry.
func TestPerformGrab_DuplicateIsAckAndStop(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ns := newNamespace(t, ctx, c)

	movie := newMovie(t, ctx, c, ns, "the-thing-1982")
	profile := hdBlurayWeb(t)
	release := torrentRelease("guid-1", "my-indexer", profile.Tiers[0][0].Quality, 0)
	target := commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: movie.Name}

	bus := newTestBus(t, nil)
	_, err := bus.KV(events.BucketLeases).Create(ctx, events.LeaseKey(grab.MediaKey(ns, target)), []byte("the-thing-1982-preexisting"))
	require.NoError(t, err)

	deps := grab.Deps{Client: c, Bus: bus, Now: fixedNow(testNow)}
	err = grab.PerformGrabForTest(ctx, deps, ns, target, nil, release, downloadv1alpha1.GrabSourceSearch)
	require.ErrorIs(t, err, grab.ErrDuplicateGrab)

	var downloads downloadv1alpha1.DownloadList
	require.NoError(t, c.List(ctx, &downloads, client.InNamespace(ns)))
	assert.Empty(t, downloads.Items, "a duplicate grab must create no Download")
}

// TestPerformGrab_ExistingDownloadStopsANonLeaseGrab covers the second guard:
// the lease was free, but something that takes no lease -- a Search CR's
// interactive grab -- already put a Download on the item.
//
// The guard used to re-read status.activeDownloadRef, which nothing on the
// interactive path ever set, so it could not fire. It now asks the apiserver
// for the Downloads covering the item. The Movie is driven to its real steady
// state FIRST -- a pendingGrab under the grab manager and the reconciler's
// activeDownloadRef -- so the assertions can observe a server-side-apply
// release if the failure path builds a partial status. A blank object could
// not.
func TestPerformGrab_ExistingDownloadStopsANonLeaseGrab(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ns := newNamespace(t, ctx, c)

	movie := newMovie(t, ctx, c, ns, "the-thing-1982")
	profile := hdBlurayWeb(t)
	target := commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: movie.Name}

	// A user grabbed a different release interactively.
	picked := torrentRelease("guid-user-picked", "my-indexer", profile.Tiers[1][0].Quality, 0)
	pickedSource, err := downloads.ResolveSource(picked)
	require.NoError(t, err)
	existing := interactiveDownload(t, ctx, c, movie, target, picked, pickedSource)
	seedWorkerStatus(t, ctx, c, movie, existing.Name, &catalogv1alpha1.PendingGrab{
		ReleaseTitle: "Other.Release",
		Protocol:     commonv1.ProtocolUsenet,
		GrabAt:       metav1.NewTime(testNow.Add(time.Hour)),
	})

	release := torrentRelease("guid-1", "my-indexer", profile.Tiers[0][0].Quality, 0)
	bus := newTestBus(t, nil)
	deps := grab.Deps{Client: c, Bus: bus, Now: fixedNow(testNow)}
	err = grab.PerformGrabForTest(ctx, deps, ns, target, nil, release, downloadv1alpha1.GrabSourceSearch)
	require.ErrorIs(t, err, grab.ErrDuplicateGrab)

	var downloadList downloadv1alpha1.DownloadList
	require.NoError(t, c.List(ctx, &downloadList, client.InNamespace(ns)))
	require.Len(t, downloadList.Items, 1, "the guard must stop a second Download for an item that has one")
	assert.Equal(t, existing.Name, downloadList.Items[0].Name)

	// The lease taken before the guard must be rolled back: leaving it
	// would block the item until the orphan grace passed.
	_, err = bus.KV(events.BucketLeases).Get(ctx, events.LeaseKey(grab.MediaKey(ns, target)))
	assert.ErrorIs(t, err, events.ErrKeyNotFound, "the failed grab must release the lease it took")

	// The steady state this exit never meant to touch is intact: the
	// reconciler's activeDownloadRef survives.
	var got catalogv1alpha1.Movie
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(movie), &got))
	require.NotNil(t, got.Status.ActiveDownloadRef)
	assert.Equal(t, existing.Name, *got.Status.ActiveDownloadRef)

	// pendingGrab IS cleared here, deliberately: this grab lost, so the
	// pending candidate will never be grabbed, and leaving it set would hold
	// the item at Phase=Delayed where wantedcron's Wanted/CutoffUnmet sweep
	// never reaches it again. See clearPendingGrab.
	assert.Nil(t, got.Status.PendingGrab)
}

// TestPerformGrab_FinishedDownloadAndItsLeaseDoNotBlockTheNextGrab is what
// lets an item be grabbed more than once. Nothing deletes a grab lease when
// its Download finishes -- spec §5 has the Download controller and a sweeper
// do it, and neither exists -- so before the grab path reclaimed stale leases
// itself, the first grab's lease answered every later grab of the item with
// ErrDuplicateGrab: no retry after a failed download, no upgrade, ever.
func TestPerformGrab_FinishedDownloadAndItsLeaseDoNotBlockTheNextGrab(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ns := newNamespace(t, ctx, c)

	movie := newMovie(t, ctx, c, ns, "the-thing-1982")
	newIndexer(t, ctx, c, ns, "my-indexer", nil)
	profile := hdBlurayWeb(t)
	target := commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: movie.Name}
	bus := newTestBus(t, nil)
	deps := grab.Deps{Client: c, Bus: bus, Now: fixedNow(testNow)}

	first := torrentRelease("guid-first", "my-indexer", profile.Tiers[1][0].Quality, 0)
	require.NoError(t, grab.PerformGrabForTest(ctx, deps, ns, target, nil, first, downloadv1alpha1.GrabSourceSearch))
	firstName := k8s.ChildName(movie.Name, first.GUID)

	// While the first Download is live, a second grab is a duplicate.
	second := torrentRelease("guid-second", "my-indexer", profile.Tiers[0][0].Quality, 0)
	err := grab.PerformGrabForTest(ctx, deps, ns, target, nil, second, downloadv1alpha1.GrabSourceSearch)
	require.ErrorIs(t, err, grab.ErrDuplicateGrab)

	// grabarr fails it. Its lease is still in the bucket.
	setDownloadPhase(t, ctx, c, ns, firstName, downloadv1alpha1.DownloadPhaseFailed)
	require.NoError(t, grab.PerformGrabForTest(ctx, deps, ns, target, nil, second, downloadv1alpha1.GrabSourceSearch),
		"a failed Download's lease must not block the next grab of the item")

	secondName := k8s.ChildName(movie.Name, second.GUID)
	var dl downloadv1alpha1.Download
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: ns, Name: secondName}, &dl))
	entry, err := bus.KV(events.BucketLeases).Get(ctx, events.LeaseKey(grab.MediaKey(ns, target)))
	require.NoError(t, err)
	assert.Equal(t, secondName, string(entry.Value), "the lease now names the Download that holds the item")
}

// TestPerformGrab_RedeliveryAfterThePublishFailedFinishesTheGrab is the
// idempotent retry the handler documents. A delivery that created its
// Download and then failed -- to clear pendingGrab, or to publish
// release.grabbed -- is redelivered; the lease it took names its own
// Download. That lease used to be refused like anyone else's, so the retry
// was acknowledged as a duplicate of itself and release.grabbed (indexarr's
// grab accounting, the history) was never published.
func TestPerformGrab_RedeliveryAfterThePublishFailedFinishesTheGrab(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ns := newNamespace(t, ctx, c)

	movie := newMovie(t, ctx, c, ns, "the-thing-1982")
	newIndexer(t, ctx, c, ns, "my-indexer", nil)
	profile := hdBlurayWeb(t)
	target := commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: movie.Name}
	release := torrentRelease("guid-1", "my-indexer", profile.Tiers[0][0].Quality, 0)
	bus := newTestBus(t, nil)
	deps := grab.Deps{Client: c, Bus: bus, Now: fixedNow(testNow)}

	require.NoError(t, grab.PerformGrabForTest(ctx, deps, ns, target, nil, release, downloadv1alpha1.GrabSourceRSS))
	// The first delivery's tail "failed": the item still reads as delayed.
	seedWorkerStatus(t, ctx, c, movie, "", &catalogv1alpha1.PendingGrab{
		ReleaseTitle: release.Title, Protocol: commonv1.ProtocolTorrent, GrabAt: metav1.NewTime(testNow),
	})

	require.NoError(t, grab.PerformGrabForTest(ctx, deps, ns, target, nil, release, downloadv1alpha1.GrabSourceRSS),
		"the redelivery must finish its own grab, not ack it as a duplicate of itself")

	var downloadList downloadv1alpha1.DownloadList
	require.NoError(t, c.List(ctx, &downloadList, client.InNamespace(ns)))
	assert.Len(t, downloadList.Items, 1, "resuming must not create a second Download")
	var got catalogv1alpha1.Movie
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(movie), &got))
	assert.Nil(t, got.Status.PendingGrab, "the resumed grab clears the consumed pendingGrab")
}

// TestPerformGrab_AuthenticatedIndexerRoutesThroughIndexarr covers §8.2's
// "source (indexerDownload when the indexer is authenticated)": with no magnet
// and an Indexer carrying a SecretRef, the engine must be told to resolve the
// link through indexarr rather than handed a URL that will 401.
func TestPerformGrab_AuthenticatedIndexerRoutesThroughIndexarr(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ns := newNamespace(t, ctx, c)

	movie := newMovie(t, ctx, c, ns, "the-thing-1982")
	newIndexer(t, ctx, c, ns, "private-tracker", &corev1.LocalObjectReference{Name: "tracker-creds"})

	profile := hdBlurayWeb(t)
	release := torrentRelease("guid-private", "private-tracker", profile.Tiers[0][0].Quality, 0)
	release.MagnetURL = ""
	release.DownloadURL = "https://example.invalid/dl/guid-private"
	release.InfoHash = "0123456789abcdef0123456789abcdef01234567"
	target := commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: movie.Name}

	deps := grab.Deps{Client: c, Bus: newTestBus(t, nil), Now: fixedNow(testNow)}
	require.NoError(t, grab.PerformGrabForTest(ctx, deps, ns, target, nil, release, downloadv1alpha1.GrabSourceRSS))

	var downloads downloadv1alpha1.DownloadList
	require.NoError(t, c.List(ctx, &downloads, client.InNamespace(ns)))
	require.Len(t, downloads.Items, 1)
	src := downloads.Items[0].Spec.Source
	require.NotNil(t, src.IndexerDownload, "an authenticated indexer must route through indexarr")
	assert.Equal(t, "private-tracker", src.IndexerDownload.IndexerRef)
	assert.Equal(t, "guid-private", src.IndexerDownload.GUID)
	assert.Nil(t, src.TorrentURL, "exactly one source variant may be set")
	require.NotNil(t, src.ExpectedInfoHash, "the info-hash guard is set on every branch")
	assert.Equal(t, release.InfoHash, *src.ExpectedInfoHash)
}

// TestPerformGrab_UpperCaseInfoHashStillGrabs: expectedInfoHash's CRD
// pattern is lower-case hex, and indexers report the hash in upper case often
// enough. Passed through verbatim, as the grab path used to, the apiserver
// rejected the whole Download and the grab retried into a dead letter.
func TestPerformGrab_UpperCaseInfoHashStillGrabs(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ns := newNamespace(t, ctx, c)

	movie := newMovie(t, ctx, c, ns, "the-thing-1982")
	newIndexer(t, ctx, c, ns, "my-indexer", nil)
	profile := hdBlurayWeb(t)
	release := torrentRelease("guid-1", "my-indexer", profile.Tiers[0][0].Quality, 0)
	release.InfoHash = "0123456789ABCDEF0123456789ABCDEF01234567"
	target := commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: movie.Name}

	deps := grab.Deps{Client: c, Bus: newTestBus(t, nil), Now: fixedNow(testNow)}
	require.NoError(t, grab.PerformGrabForTest(ctx, deps, ns, target, nil, release, downloadv1alpha1.GrabSourceSearch))

	var dl downloadv1alpha1.Download
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: ns, Name: k8s.ChildName(movie.Name, release.GUID)}, &dl))
	require.NotNil(t, dl.Spec.Source.ExpectedInfoHash)
	assert.Equal(t, "0123456789abcdef0123456789abcdef01234567", *dl.Spec.Source.ExpectedInfoHash)
}

// TestPerformGrab_UnauthenticatedIndexerAlsoRoutesThroughIndexarr: whether
// an Indexer has a Secret is no longer an input to the source. It used to
// pick a direct torrentURL here, while the Search controller picked
// indexerDownload for the same release -- and since both name the Download
// identically and spec.source is immutable, whichever grabbed second was
// rejected. Routing every indexer release through indexarr also keeps the
// Indexer's proxy and grab accounting in the path.
func TestPerformGrab_UnauthenticatedIndexerAlsoRoutesThroughIndexarr(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ns := newNamespace(t, ctx, c)

	movie := newMovie(t, ctx, c, ns, "the-thing-1982")
	idx := newIndexer(t, ctx, c, ns, "open-tracker", nil)
	idx.Spec.SeedCriteria = &commonv1.SeedCriteria{SeedTime: &metav1.Duration{Duration: 48 * time.Hour}}
	require.NoError(t, c.Update(ctx, idx))

	profile := hdBlurayWeb(t)
	release := torrentRelease("guid-open", "open-tracker", profile.Tiers[0][0].Quality, 0)
	release.MagnetURL = ""
	release.DownloadURL = "https://example.invalid/dl/guid-open"
	target := commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: movie.Name}

	deps := grab.Deps{Client: c, Bus: newTestBus(t, nil), Now: fixedNow(testNow)}
	require.NoError(t, grab.PerformGrabForTest(ctx, deps, ns, target, nil, release, downloadv1alpha1.GrabSourceSearch))

	var downloadList downloadv1alpha1.DownloadList
	require.NoError(t, c.List(ctx, &downloadList, client.InNamespace(ns)))
	require.Len(t, downloadList.Items, 1)
	dl := downloadList.Items[0]
	require.NotNil(t, dl.Spec.Source.IndexerDownload)
	assert.Equal(t, release.DownloadURL, dl.Spec.Source.IndexerDownload.URL)
	assert.Nil(t, dl.Spec.Source.TorrentURL)
	require.NotNil(t, dl.Spec.SeedCriteria, "the Indexer's seedCriteria overrides the client's limits")
	require.NotNil(t, dl.Spec.SeedCriteria.SeedTime)
	assert.Equal(t, 48*time.Hour, dl.Spec.SeedCriteria.SeedTime.Duration)
}

// TestPerformGrab_SeasonPackLeasesAndPatchesEveryEpisode is the pack shape:
// the Series is the target and owner, every episode is a status target with
// its own lease.
func TestPerformGrab_SeasonPackLeasesAndPatchesEveryEpisode(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ns := newNamespace(t, ctx, c)

	series := &catalogv1alpha1.Series{
		ObjectMeta: metav1.ObjectMeta{Name: "the-wire", Namespace: ns},
		Spec: catalogv1alpha1.SeriesSpec{
			TvdbID: 79126, QualityProfileRef: "hd-bluray-web", RootFolderRef: "tv",
		},
	}
	require.NoError(t, c.Create(ctx, series))
	names := []string{"the-wire-s01e01", "the-wire-s01e02"}
	for i, n := range names {
		ep := &catalogv1alpha1.Episode{
			ObjectMeta: metav1.ObjectMeta{Name: n, Namespace: ns},
			Spec: catalogv1alpha1.EpisodeSpec{
				SeriesRef: series.Name, SeasonNumber: 1, EpisodeNumber: int32(i + 1), Monitored: ptr.To(true),
			},
		}
		require.NoError(t, c.Create(ctx, ep))
	}

	profile := hdBlurayWeb(t)
	release := torrentRelease("guid-pack", "", profile.Tiers[0][0].Quality, 0)
	target := commonv1.MediaRef{Kind: commonv1.MediaKindSeries, Name: series.Name}

	bus := newTestBus(t, nil)
	deps := grab.Deps{Client: c, Bus: bus, Now: fixedNow(testNow)}
	require.NoError(t, grab.PerformGrabForTest(ctx, deps, ns, target, names, release, downloadv1alpha1.GrabSourceSearch))

	var downloads downloadv1alpha1.DownloadList
	require.NoError(t, c.List(ctx, &downloads, client.InNamespace(ns)))
	require.Len(t, downloads.Items, 1, "one pack is one Download")
	dl := downloads.Items[0]
	assert.Equal(t, names, dl.Spec.Target.Keys)
	assert.Equal(t, "hd-bluray-web", dl.Spec.QualityProfileRef, "an episode's profile comes from its Series")
	require.Len(t, dl.OwnerReferences, 1)
	assert.Equal(t, series.Name, dl.OwnerReferences[0].Name)

	for _, n := range names {
		var ep catalogv1alpha1.Episode
		require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: ns, Name: n}, &ep))
		assert.Nilf(t, ep.Status.ActiveDownloadRef, "%s: performGrab must not write activeDownloadRef (R-5)", n)
		assert.Empty(t, ep.Status.Phase, "performGrab must never set Phase")

		entry, err := bus.KV(events.BucketLeases).Get(ctx,
			events.LeaseKey(grab.MediaKey(ns, commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: n})))
		require.NoErrorf(t, err, "%s holds no lease", n)
		assert.Equal(t, dl.Name, string(entry.Value))
	}
}

// TestPerformGrab_PreservesTheGatewaysMetadata is one half of the
// field-manager split's regression cover: a grab must not disturb
// status.metadata.
//
// While the grab path and the metadata gateway shared k8s.ManagerCatalogarrWorker
// this failed -- server-side apply replaces a manager's whole ownership set,
// so an apply carrying only the grab's fields RELEASED the metadata the
// gateway had written under the same name. The Movie lost its cached
// metadata, its reconciler recomputed Phase=Pending and the gateway refetched
// from the provider, on every grab.
//
// It only fails against an object that already HAS metadata, which is why the
// movie is driven to that steady state first.
func TestPerformGrab_PreservesTheGatewaysMetadata(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ns := newNamespace(t, ctx, c)

	movie := newMovie(t, ctx, c, ns, "the-thing-1982")
	newIndexer(t, ctx, c, ns, "my-indexer", nil)
	seedGatewayMetadata(t, ctx, c, movie)

	profile := hdBlurayWeb(t)
	release := torrentRelease("guid-1", "my-indexer", profile.Tiers[0][0].Quality, 0)
	target := commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: movie.Name}
	deps := grab.Deps{Client: c, Bus: newTestBus(t, nil), Now: fixedNow(testNow)}
	require.NoError(t, grab.PerformGrabForTest(ctx, deps, ns, target, nil, release, downloadv1alpha1.GrabSourceSearch))

	var got catalogv1alpha1.Movie
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(movie), &got))
	require.NotNil(t, got.Status.Metadata, "the grab released the gateway's status.metadata")
	assert.Equal(t, "The Thing", got.Status.Metadata.Title)
	assert.EqualValues(t, 1982, got.Status.Metadata.Year)
	assert.EqualValues(t, 109, got.Status.Metadata.RuntimeMinutes)
	assert.Equal(t, catalogv1alpha1.MovieReleaseStatusReleased, got.Status.Metadata.Status)
}

// TestGatewayRefreshPreservesTheGrabsFields is the OTHER half, and the more
// damaging direction: a metadata refresh must not disturb the grab path's
// fields.
//
// While both wrote as k8s.ManagerCatalogarrWorker, one gateway refresh after a
// grab deleted status.activeDownloadRef (then still a grab-path field), and a
// refresh inside a delay window
// deleted status.pendingGrab -- taking the item out of Phase=Delayed back to
// Wanted, where the wanted cron re-searched an item that already had a grab
// scheduled. Nothing errored; the item simply lost its place in the pipeline.
//
// The apply below is byte-for-byte what app/catalog/metadata's handler builds:
// MovieStatus().WithMetadata(...) and nothing else.
func TestGatewayRefreshPreservesTheGrabsFields(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ns := newNamespace(t, ctx, c)

	movie := newMovie(t, ctx, c, ns, "the-thing-1982")
	newIndexer(t, ctx, c, ns, "my-indexer", nil)
	seedGatewayMetadata(t, ctx, c, movie)

	profile := hdBlurayWeb(t)
	target := commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: movie.Name}
	deps := grab.Deps{Client: c, Bus: newTestBus(t, nil), Now: fixedNow(testNow)}
	require.NoError(t, grab.PerformGrabForTest(ctx, deps, ns,
		target, nil, torrentRelease("guid-1", "my-indexer", profile.Tiers[0][0].Quality, 0),
		downloadv1alpha1.GrabSourceSearch))

	// A pendingGrab as well, so the refresh has a grab-owned field to
	// release, and the reconciler's activeDownloadRef beside it.
	seedWorkerStatus(t, ctx, c, movie, "the-thing-1982-abcdef0123", &catalogv1alpha1.PendingGrab{
		ReleaseTitle: "The.Thing.1982.2160p.REMUX-BETTER",
		Protocol:     commonv1.ProtocolUsenet,
		GrabAt:       metav1.NewTime(testNow.Add(45 * time.Minute)),
	})

	// Now the gateway refreshes. This is the write that used to gut the item.
	seedGatewayMetadata(t, ctx, c, movie)

	var got catalogv1alpha1.Movie
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(movie), &got))
	require.NotNilf(t, got.Status.ActiveDownloadRef,
		"the gateway refresh released status.activeDownloadRef: the Download is now orphaned and the item is back in the search rotation")
	assert.Equal(t, "the-thing-1982-abcdef0123", *got.Status.ActiveDownloadRef)
	require.NotNilf(t, got.Status.PendingGrab,
		"the gateway refresh released status.pendingGrab: the item drops out of Phase=Delayed and the wanted cron re-searches it")
	assert.Equal(t, "The.Thing.1982.2160p.REMUX-BETTER", got.Status.PendingGrab.ReleaseTitle)
	assert.True(t, got.Status.PendingGrab.GrabAt.Time.Equal(testNow.Add(45*time.Minute)))
	require.NotNil(t, got.Status.Metadata, "and the refresh itself must have landed")

	// The two managers own disjoint status entries, which is what makes the
	// guarantee structural rather than a pass-through nobody may forget.
	var sawGrab, sawMetadata bool
	for _, e := range got.ManagedFields {
		if e.Subresource != "status" {
			continue
		}
		sawGrab = sawGrab || e.Manager == string(k8s.ManagerCatalogarrGrab)
		sawMetadata = sawMetadata || e.Manager == string(k8s.ManagerCatalogarrMetadata)
	}
	assert.True(t, sawGrab && sawMetadata,
		"the grab path and the gateway must own separate status field-manager entries, got %+v", got.ManagedFields)
}

// TestRecordSearchAttempt_AdvancesTheLadderWithoutReleasingAnything is the
// helper C8 calls, tested the way every write in this package is: against an
// object already in its steady state, so a partial apply would be visible.
//
// The ladder itself matters -- without a writer for these two fields
// wantedcron.Backoff never leaves its six-hour floor -- but the release is
// what the helper exists to prevent: a second package sending only
// lastSearchedAt and searchAttempts under this manager would delete
// pendingGrab.
func TestRecordSearchAttempt_AdvancesTheLadderWithoutReleasingAnything(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ns := newNamespace(t, ctx, c)

	movie := newMovie(t, ctx, c, ns, "the-thing-1982")
	seedGatewayMetadata(t, ctx, c, movie)
	seedWorkerStatus(t, ctx, c, movie, "the-thing-1982-abcdef0123", &catalogv1alpha1.PendingGrab{
		ReleaseTitle: "The.Thing.1982.2160p.REMUX-BETTER",
		Protocol:     commonv1.ProtocolUsenet,
		GrabAt:       metav1.NewTime(testNow.Add(45 * time.Minute)),
	})

	ref := commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: movie.Name}
	require.NoError(t, grab.RecordSearchAttempt(ctx, c, ns, ref, testNow))

	var got catalogv1alpha1.Movie
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(movie), &got))
	require.NotNil(t, got.Status.LastSearchedAt)
	assert.True(t, got.Status.LastSearchedAt.Time.Equal(testNow))
	assert.EqualValues(t, 1, got.Status.SearchAttempts.Count)
	require.NotNil(t, got.Status.SearchAttempts.Initial)
	require.NotNil(t, got.Status.SearchAttempts.Latest)

	// Nothing else this manager owns was released, and the gateway's field
	// (a different manager) is untouched either way.
	require.NotNilf(t, got.Status.ActiveDownloadRef,
		"recording a search attempt touched the reconciler's status.activeDownloadRef")
	require.NotNilf(t, got.Status.PendingGrab,
		"recording a search attempt released status.pendingGrab")
	require.NotNil(t, got.Status.Metadata)
	assert.Equal(t, catalogv1alpha1.MoviePhaseDelayed, got.Status.Phase, "the reconciler's phase is untouched")
	assert.NotContains(t, managerStatusFields(&got, k8s.ManagerCatalogarrGrab), `"f:phase"`, "this helper must never write Phase")

	// A second attempt advances Latest and Count but never moves Initial --
	// the ladder is measured from the first attempt.
	later := testNow.Add(7 * time.Hour)
	require.NoError(t, grab.RecordSearchAttempt(ctx, c, ns, ref, later))
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(movie), &got))
	assert.EqualValues(t, 2, got.Status.SearchAttempts.Count)
	assert.True(t, got.Status.SearchAttempts.Initial.Time.Equal(testNow), "Initial must not move")
	assert.True(t, got.Status.SearchAttempts.Latest.Time.Equal(later))
	require.NotNil(t, got.Status.PendingGrab, "the second write must not release either")
}

// TestRecordSearchAttempt_PackStampsEveryEpisode: one interactive pack search
// records an attempt against every episode it covered, so the backoff ladder
// applies per item rather than to a Series that has no such status.
func TestRecordSearchAttempt_PackStampsEveryEpisode(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ns := newNamespace(t, ctx, c)

	series := &catalogv1alpha1.Series{
		ObjectMeta: metav1.ObjectMeta{Name: "the-wire", Namespace: ns},
		Spec:       catalogv1alpha1.SeriesSpec{TvdbID: 79126, QualityProfileRef: "hd-bluray-web", RootFolderRef: "tv"},
	}
	require.NoError(t, c.Create(ctx, series))
	names := []string{"the-wire-s01e01", "the-wire-s01e02"}
	for i, n := range names {
		require.NoError(t, c.Create(ctx, &catalogv1alpha1.Episode{
			ObjectMeta: metav1.ObjectMeta{Name: n, Namespace: ns},
			Spec: catalogv1alpha1.EpisodeSpec{
				SeriesRef: series.Name, SeasonNumber: 1, EpisodeNumber: int32(i + 1), Monitored: ptr.To(true),
			},
		}))
	}

	require.NoError(t, grab.RecordSearchAttempt(ctx, c, ns,
		commonv1.MediaRef{Kind: commonv1.MediaKindSeries, Name: series.Name, Keys: names}, testNow))

	for _, n := range names {
		var ep catalogv1alpha1.Episode
		require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: ns, Name: n}, &ep))
		require.NotNilf(t, ep.Status.LastSearchedAt, "%s was not stamped", n)
		assert.EqualValues(t, 1, ep.Status.SearchAttempts.Count)
	}
}

// TestRecordSearchAttempt_MissingObjectIsNotAnError: an item deleted between
// the search and this write has nothing to record against, and failing would
// dead-letter a task that did its job.
func TestRecordSearchAttempt_MissingObjectIsNotAnError(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ns := newNamespace(t, ctx, c)
	require.NoError(t, grab.RecordSearchAttempt(ctx, c, ns,
		commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "never-existed"}, testNow))
}

// staleCacheClient is a client whose reads have not yet seen any Download --
// the informer cache in the milliseconds after another path created one.
// Every other read and every write goes to the apiserver.
func staleCacheClient(t *testing.T) client.Client {
	t.Helper()
	return interceptor.NewClient(newWatchClient(t), interceptor.Funcs{
		List: func(ctx context.Context, wc client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if _, ok := list.(*downloadv1alpha1.DownloadList); ok {
				return nil
			}
			return wc.List(ctx, list, opts...)
		},
		Get: func(ctx context.Context, wc client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*downloadv1alpha1.Download); ok {
				return apierrors.NewNotFound(downloadv1alpha1.GroupVersion.WithResource("downloads").GroupResource(), key.Name)
			}
			return wc.Get(ctx, key, obj, opts...)
		},
	})
}

// TestPerformGrab_TheGuardReadsLiveNotTheCache closes the guard's cache
// window. A Search CR's interactive grab takes no lease, so the Download list
// is the only thing that can see it -- and a list through the informer cache
// cannot see a Download created a moment earlier. The automatic grab of a
// different release then put a second Download on the item beside it.
//
// Deps.Client here is a cache that has not seen the user's Download yet;
// Deps.Reader is the apiserver. The guard must read the second. The control
// half runs the same grab with no Reader, proving the stale client really
// does hide the Download -- without it the first half would pass for the
// wrong reason.
func TestPerformGrab_TheGuardReadsLiveNotTheCache(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	profile := hdBlurayWeb(t)
	picked := torrentRelease("guid-user-picked", "my-indexer", profile.Tiers[1][0].Quality, 0)
	automatic := torrentRelease("guid-automatic", "my-indexer", profile.Tiers[0][0].Quality, 0)
	pickedSource, err := downloads.ResolveSource(picked)
	require.NoError(t, err)

	setup := func() (string, *catalogv1alpha1.Movie, commonv1.MediaRef) {
		ns := newNamespace(t, ctx, c)
		movie := newMovie(t, ctx, c, ns, "the-thing-1982")
		newIndexer(t, ctx, c, ns, "my-indexer", nil)
		seedWorkerStatus(t, ctx, c, movie, "", nil)
		target := commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: movie.Name}
		interactiveDownload(t, ctx, c, movie, target, picked, pickedSource)
		return ns, movie, target
	}
	count := func(ns string) int {
		var list downloadv1alpha1.DownloadList
		require.NoError(t, c.List(ctx, &list, client.InNamespace(ns)))
		return len(list.Items)
	}

	ns, _, target := setup()
	live := grab.Deps{Client: staleCacheClient(t), Reader: c, Bus: newTestBus(t, nil), Now: fixedNow(testNow)}
	err = grab.PerformGrabForTest(ctx, live, ns, target, nil, automatic, downloadv1alpha1.GrabSourceSearch)
	require.ErrorIs(t, err, grab.ErrDuplicateGrab,
		"the guard read the cache, missed the user's Download and grabbed a second release beside it")
	assert.Equal(t, 1, count(ns))

	// Control: the same grab with only the stale client double-grabs.
	ns, _, target = setup()
	cached := grab.Deps{Client: staleCacheClient(t), Bus: newTestBus(t, nil), Now: fixedNow(testNow)}
	require.NoError(t, grab.PerformGrabForTest(ctx, cached, ns, target, nil, automatic, downloadv1alpha1.GrabSourceSearch))
	assert.Equal(t, 2, count(ns), "the stale client must actually hide the Download, or the live half proves nothing")
}
