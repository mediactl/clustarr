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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/worker/grab"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
)

var testNow = time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)

// TestPerformGrab_CreatesDownloadAndPatchesStatus is §8.2's happy path: one
// deterministically named Download, owned by the target, and
// status.activeDownloadRef written under k8s.ManagerCatalogarrWorker only --
// never status.phase, which is the Movie reconciler's under
// k8s.ManagerCatalogarr.
func TestPerformGrab_CreatesDownloadAndPatchesStatus(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ns := newNamespace(t, ctx, c)

	movie := newMovie(t, ctx, c, ns, "the-thing-1982")
	newIndexer(t, ctx, c, ns, "my-indexer", nil)

	profile := hdBlurayWeb(t)
	release := torrentRelease("guid-1", "my-indexer", profile.Tiers[0][0].Quality, 0)
	target := commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: movie.Name}

	bus := newTestBus(t, nil)
	deps := grab.Deps{Client: c, Bus: bus, Now: fixedNow(testNow)}
	require.NoError(t, grab.PerformGrabForTest(ctx, deps, ns, target, nil, release, downloadv1alpha1.GrabSourceSearch))

	var downloads downloadv1alpha1.DownloadList
	require.NoError(t, c.List(ctx, &downloads, client.InNamespace(ns)))
	require.Len(t, downloads.Items, 1)
	dl := downloads.Items[0]

	assert.Equal(t, k8s.ChildName(movie.Name, "guid-1"), dl.Name, "the Download name must be <target>-<sha1(guid)[:10]>")
	assert.Equal(t, "guid-1", dl.Spec.Release.GUID)
	assert.Equal(t, "hd-bluray-web", dl.Spec.QualityProfileRef)
	assert.Equal(t, commonv1.ProtocolTorrent, dl.Spec.Protocol)
	assert.Equal(t, downloadv1alpha1.GrabSourceSearch, dl.Spec.GrabbedBy)
	assert.Equal(t, target, dl.Spec.Target)
	require.NotNil(t, dl.Spec.Source.MagnetURL, "a magnet needs no indexer auth and wins over indexerDownload")
	require.Len(t, dl.OwnerReferences, 1)
	assert.Equal(t, movie.Name, dl.OwnerReferences[0].Name)

	var got catalogv1alpha1.Movie
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(movie), &got))
	require.NotNil(t, got.Status.ActiveDownloadRef)
	assert.Equal(t, dl.Name, *got.Status.ActiveDownloadRef)
	assert.Empty(t, got.Status.Phase, "performGrab must never set Phase: that is the Movie reconciler's field")

	// The lease is held, not released, so grabarr can match it to the
	// Download and release it on a terminal phase.
	entry, err := bus.KV(events.BucketLeases).Get(ctx, events.LeaseKey(grab.MediaKey(ns, target)))
	require.NoError(t, err)
	assert.Equal(t, dl.Name, string(entry.Value))
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

// TestPerformGrab_OptimisticReReadStopsANonLeaseGrab covers the second guard:
// the lease was free, but something that does not take leases (a Search CR's
// manual grab) already claimed the item.
//
// The Movie is driven to its real steady state FIRST -- activeDownloadRef and
// pendingGrab both set, under the same field manager performGrab writes with
// -- so the assertion can observe a server-side-apply release if the failure
// path builds a partial status. A blank object could not.
func TestPerformGrab_OptimisticReReadStopsANonLeaseGrab(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ns := newNamespace(t, ctx, c)

	movie := newMovie(t, ctx, c, ns, "the-thing-1982")
	seedWorkerStatus(t, ctx, c, movie, "someone-elses-download", &catalogv1alpha1.PendingGrab{
		ReleaseTitle: "Other.Release",
		Protocol:     commonv1.ProtocolUsenet,
		GrabAt:       metav1.NewTime(testNow.Add(time.Hour)),
	})

	profile := hdBlurayWeb(t)
	release := torrentRelease("guid-1", "my-indexer", profile.Tiers[0][0].Quality, 0)
	target := commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: movie.Name}

	bus := newTestBus(t, nil)
	deps := grab.Deps{Client: c, Bus: bus, Now: fixedNow(testNow)}
	err := grab.PerformGrabForTest(ctx, deps, ns, target, nil, release, downloadv1alpha1.GrabSourceSearch)
	require.ErrorIs(t, err, grab.ErrDuplicateGrab)

	var downloads downloadv1alpha1.DownloadList
	require.NoError(t, c.List(ctx, &downloads, client.InNamespace(ns)))
	assert.Empty(t, downloads.Items)

	// The lease taken before the re-read must be rolled back: leaving it
	// would block the item for the ten minutes the sweeper takes.
	_, err = bus.KV(events.BucketLeases).Get(ctx, events.LeaseKey(grab.MediaKey(ns, target)))
	assert.ErrorIs(t, err, events.ErrKeyNotFound, "the failed grab must release the lease it took")

	// And the steady state it never meant to touch is intact.
	var got catalogv1alpha1.Movie
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(movie), &got))
	require.NotNil(t, got.Status.ActiveDownloadRef)
	assert.Equal(t, "someone-elses-download", *got.Status.ActiveDownloadRef)
	require.NotNil(t, got.Status.PendingGrab, "a failed grab must not release the pendingGrab it did not write")
	assert.Equal(t, "Other.Release", got.Status.PendingGrab.ReleaseTitle)
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

// TestPerformGrab_UnauthenticatedIndexerUsesTheDirectURL is the other half of
// the same clause.
func TestPerformGrab_UnauthenticatedIndexerUsesTheDirectURL(t *testing.T) {
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

	var downloads downloadv1alpha1.DownloadList
	require.NoError(t, c.List(ctx, &downloads, client.InNamespace(ns)))
	require.Len(t, downloads.Items, 1)
	dl := downloads.Items[0]
	require.NotNil(t, dl.Spec.Source.TorrentURL)
	assert.Equal(t, release.DownloadURL, *dl.Spec.Source.TorrentURL)
	assert.Nil(t, dl.Spec.Source.IndexerDownload)
	require.NotNil(t, dl.Spec.SeedCriteria, "the Indexer's seedCriteria overrides the client's limits")
	require.NotNil(t, dl.Spec.SeedCriteria.SeedTime)
	assert.Equal(t, 48*time.Hour, dl.Spec.SeedCriteria.SeedTime.Duration)
}

// TestPerformGrab_SeasonPackLeasesAndPatchesEveryEpisode is the pack shape:
// the Series is the target and owner, every episode is a status target.
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
		require.NotNilf(t, ep.Status.ActiveDownloadRef, "%s has no activeDownloadRef", n)
		assert.Equal(t, dl.Name, *ep.Status.ActiveDownloadRef)
		assert.Empty(t, ep.Status.Phase, "performGrab must never set Phase")

		entry, err := bus.KV(events.BucketLeases).Get(ctx,
			events.LeaseKey(grab.MediaKey(ns, commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: n})))
		require.NoErrorf(t, err, "%s holds no lease", n)
		assert.Equal(t, dl.Name, string(entry.Value))
	}
}

// TestPerformGrab_PreservesTheGatewaysMetadata is the regression test for the
// nastiest thing found in this task: the metadata gateway writes
// status.metadata under the SAME field manager this package writes
// status.activeDownloadRef with, so an apply that omitted it would RELEASE
// it. The Movie would lose its cached metadata, its reconciler would drop back
// to Phase=Pending and the gateway would refetch from the provider -- on every
// single grab.
//
// It only fails against an object that already HAS metadata, which is why the
// movie is driven to that steady state first.
func TestPerformGrab_PreservesTheGatewaysMetadata(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ns := newNamespace(t, ctx, c)

	movie := newMovie(t, ctx, c, ns, "the-thing-1982")
	newIndexer(t, ctx, c, ns, "my-indexer", nil)

	// Exactly what catalogarr/metadata's Handler writes, field manager and all.
	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrWorker,
		catalogac.Movie(movie.Name, ns).WithStatus(catalogac.MovieStatus().WithMetadata(
			catalogac.MovieMetadata().
				WithTitle("The Thing").
				WithYear(1982).
				WithRuntimeMinutes(109).
				WithStatus(catalogv1alpha1.MovieReleaseStatusReleased).
				WithRefreshedAt(metav1.NewTime(testNow)),
		)))
	require.NoError(t, err)

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
	require.NotNil(t, got.Status.ActiveDownloadRef)
}
