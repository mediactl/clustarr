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

package mediafile_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	k8sevents "k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	downloadac "github.com/mediactl/clustarr/api/applyconfiguration/download/download/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/controller/episode"
	"github.com/mediactl/clustarr/catalogarr/controller/mediafile"
	"github.com/mediactl/clustarr/catalogarr/controller/movie"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// nopPublisher satisfies movie.Reconciler's Bus dependency. Every Movie in
// this file carries fresh status.metadata, so the staleness branch that
// would publish a MetadataTask is never taken and nothing is ever sent.
type nopPublisher struct{}

func (nopPublisher) Publish(_ context.Context, _ string, _ *events.Envelope, _ ...events.PublishOption) (events.Receipt, error) {
	return events.Receipt{}, nil
}

// startOwnerCache starts a real ctrl.Manager's cache (WITHOUT wiring any
// controller to it, so nothing auto-reconciles and no process-global
// controller name is claimed) with the field indexes the Movie and Episode
// reconcilers' List calls read -- the MediaFiles backing an item and the
// Downloads covering it. Only a manager cache's FieldIndexer serves a
// List(client.MatchingFields{...}); against a bare client the same option is
// sent to the apiserver as a fieldSelector, which no CRD declares as
// selectable (the same trap mediafile_controller.go's
// latestUnincorporatedTranscode documents from the other direction). The
// indexes come from each package's RegisterIndexes, the call its
// SetupWithManager makes, so they cannot drift from production.
func startOwnerCache(t *testing.T, ctx context.Context, cfg *rest.Config) client.Client {
	t.Helper()
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 k8s.MustNewScheme(),
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
	})
	require.NoError(t, err)

	require.NoError(t, movie.RegisterIndexes(ctx, mgr.GetFieldIndexer()))
	require.NoError(t, episode.RegisterIndexes(ctx, mgr.GetFieldIndexer()))

	go func() { _ = mgr.Start(ctx) }()
	require.True(t, mgr.GetCache().WaitForCacheSync(ctx))
	return mgr.GetClient()
}

// mustDownload creates a Download owned by owner in the given phase, the way
// the grab path and grabarr would between them.
func mustDownload(t *testing.T, ctx context.Context, c client.Client, owner client.Object, name string, target commonv1.MediaRef, phase downloadv1alpha1.DownloadPhase) {
	t.Helper()
	ns := owner.GetNamespace()
	ref, err := k8s.OwnerReference(owner, k8s.MustNewScheme())
	require.NoError(t, err)
	dl := &downloadv1alpha1.Download{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, OwnerReferences: []metav1.OwnerReference{ref}},
		Spec: downloadv1alpha1.DownloadSpec{
			Protocol: commonv1.ProtocolTorrent,
			Source:   downloadv1alpha1.DownloadSource{MagnetURL: ptrTo("magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567")},
			Release: commonv1.ReleaseInfo{
				GUID: "https://indexer.example/1", IndexerRef: "example", IndexerName: "Example",
				Title: "Fixture.2010.1080p", Protocol: commonv1.ProtocolTorrent,
				InfoHash: "0123456789abcdef0123456789abcdef01234567",
			},
			Target: target,
		},
	}
	require.NoError(t, c.Create(ctx, dl))
	_, err = k8s.PatchStatus(ctx, c, k8s.ManagerGrabarr,
		downloadac.Download(name, ns).WithStatus(downloadac.DownloadStatus().WithPhase(phase)))
	require.NoError(t, err)
}

func ptrTo[T any](v T) *T { return &v }

// TestMediaFileReconcileDoesNotReleaseOwnerStatus is the C13 regression
// gate. It does NOT create a blank Movie/Episode and assert -- a blank
// object has nothing to release, which is precisely why this class of bug
// survived three reviews on this branch. Instead it drives each item to a
// genuine steady state through its OWN reconciler (the only writer of
// k8s.ManagerCatalogarr on that object), so path, available/availableAt,
// addOptionsApplied, observedGeneration and activeDownloadRef are all
// really owned by that field manager (the ref, since ruling R-5, by that
// manager alone), and only then runs the MediaFile
// reconciler over the file backing it.
//
// Before task C13, mediafile.Reconciler.rollupToOwner applied a
// SEVEN-field Movie/Episode status (hasFile, fileRef, fileQuality,
// fileFormatScore, cutoffMet, phase, conditions) under that same
// k8s.ManagerCatalogarr. Server-side apply replaces a manager's ownership
// set rather than merging it, so every field above that the rollup omitted
// was released, and the object read back with path="", available=false,
// addOptionsApplied=false, observedGeneration=0 and
// activeDownloadRef=<nil>. Losing addOptionsApplied lets a Movie's
// addOptions be applied a second time; losing activeDownloadRef orphans the
// in-flight Download and returns the item to the search rotation.
func TestMediaFileReconcileDoesNotReleaseOwnerStatus(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	bare, cfg := startEnv(t)
	c := startOwnerCache(t, ctx, cfg)

	const ns = "ownerstatus-ns"
	mustNamespace(t, ctx, c, ns)
	mustQualityProfile(t, ctx, c, "cutoff-met-at-1080p")
	require.NoError(t, c.Create(ctx, &catalogv1alpha1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Name: "movies", Namespace: ns},
		Spec:       catalogv1alpha1.RootFolderSpec{Path: "/data/media/movies", Kind: catalogv1alpha1.RootFolderKindMovie},
	}))

	dir := t.TempDir()
	bluray := commonv1.Quality{Name: "Bluray-1080p", Resolution: 1080, Source: commonv1.SourceBluray, Modifier: commonv1.ModifierNone}
	scheme := k8s.MustNewScheme()

	newMediaFileReconciler := func() *mediafile.Reconciler {
		r := mediafile.NewReconciler(c, scheme, k8sevents.NewFakeRecorder(20))
		r.Probe = fakeProbe
		r.Clock = time.Now
		return r
	}

	t.Run("a MediaFile reconcile does not release the Movie fields catalogarr owns", func(t *testing.T) {
		const name = "inception"
		m := &catalogv1alpha1.Movie{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
			Spec: catalogv1alpha1.MovieSpec{
				TmdbID: 27205, QualityProfileRef: "cutoff-met-at-1080p", RootFolderRef: "movies",
				MinimumAvailability: catalogv1alpha1.MinimumAvailabilityReleased,
			},
		}
		require.NoError(t, c.Create(ctx, m))

		// status.metadata belongs to the metadata gateway, under its own
		// field manager -- without it the Movie reconciler never computes
		// path or availability at all, and there would be nothing for the
		// rollup to release.
		yesterday := metav1.NewTime(time.Now().Add(-24 * time.Hour))
		_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrMetadata,
			catalogac.Movie(name, ns).WithStatus(catalogac.MovieStatus().WithMetadata(
				catalogac.MovieMetadata().WithTitle("Inception").WithYear(2010).
					WithStatus(catalogv1alpha1.MovieReleaseStatusReleased).
					WithDigitalRelease(yesterday).WithRefreshedAt(metav1.Now()))))
		require.NoError(t, err)

		filePath := writeFile(t, dir, "inception.mkv", []byte("inception"))
		importarrCreatesMediaFileFor(t, ctx, c, ns, name+"-abc1234567",
			commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: name}, filePath, 9, time.Now(), bluray)

		// An in-flight Download the Movie owns. Since ruling R-5 the Movie
		// reconciler is status.activeDownloadRef's only writer, deriving it
		// from this Download, so catalogarr is the only thing standing
		// between the ref and deletion -- which is what puts the field in
		// the blast radius of a rollup apply.
		var owner catalogv1alpha1.Movie
		require.NoError(t, bare.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &owner))
		mustDownload(t, ctx, c, &owner, name+"-dl", commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: name}, downloadv1alpha1.DownloadPhaseAssigned)

		// The Movie reconciler reads through the manager cache, so wait for
		// it to observe the metadata, the file and the Download before
		// driving the steady state.
		require.Eventually(t, func() bool {
			var got catalogv1alpha1.Movie
			if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got); err != nil {
				return false
			}
			var gotMF catalogv1alpha1.MediaFile
			if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name + "-abc1234567"}, &gotMF); err != nil {
				return false
			}
			var gotDL downloadv1alpha1.Download
			if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name + "-dl"}, &gotDL); err != nil {
				return false
			}
			return got.Status.Metadata != nil && gotDL.Status.Phase == downloadv1alpha1.DownloadPhaseAssigned
		}, 10*time.Second, 20*time.Millisecond, "setup: cache never observed metadata, the file and the Download")

		mr := &movie.Reconciler{Client: c, Scheme: scheme, Recorder: k8sevents.NewFakeRecorder(20), Bus: nopPublisher{}}
		req := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: name}}
		_, err = mr.Reconcile(ctx, req)
		require.NoError(t, err)

		var before catalogv1alpha1.Movie
		require.NoError(t, bare.Get(ctx, req.NamespacedName, &before))
		require.NotEmpty(t, before.Status.Path, "setup: the Movie reconciler must have set status.path")
		require.True(t, before.Status.Available, "setup: the Movie reconciler must have set status.available")
		require.NotNil(t, before.Status.AvailableAt, "setup: the Movie reconciler must have set status.availableAt")
		require.True(t, before.Status.AddOptionsApplied, "setup: the Movie reconciler must have set status.addOptionsApplied")
		require.NotZero(t, before.Status.ObservedGeneration, "setup: the Movie reconciler must have set status.observedGeneration")
		require.NotNil(t, before.Status.ActiveDownloadRef, "setup: the Movie reconciler must still hold status.activeDownloadRef")
		require.True(t, before.Status.HasFile, "setup: the Movie reconciler must have rolled up the MediaFile itself")

		// The MediaFile reconcile under test. Its own object's status is
		// none of this test's business; what matters is that it leaves the
		// owning Movie alone.
		_, err = newMediaFileReconciler().Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Namespace: ns, Name: name + "-abc1234567"},
		})
		require.NoError(t, err)

		var after catalogv1alpha1.Movie
		require.NoError(t, bare.Get(ctx, req.NamespacedName, &after))
		assert.Equal(t, before.Status.Path, after.Status.Path, "a MediaFile reconcile must not release Movie status.path")
		assert.True(t, after.Status.Available, "a MediaFile reconcile must not release Movie status.available")
		assert.NotNil(t, after.Status.AvailableAt, "a MediaFile reconcile must not release Movie status.availableAt")
		assert.True(t, after.Status.AddOptionsApplied, "a MediaFile reconcile must not release Movie status.addOptionsApplied -- releasing it lets addOptions be applied twice")
		assert.Equal(t, before.Status.ObservedGeneration, after.Status.ObservedGeneration, "a MediaFile reconcile must not release Movie status.observedGeneration")
		if assert.NotNil(t, after.Status.ActiveDownloadRef, "a MediaFile reconcile must not release Movie status.activeDownloadRef -- releasing it orphans the in-flight Download") {
			assert.Equal(t, *before.Status.ActiveDownloadRef, *after.Status.ActiveDownloadRef)
		}
	})

	t.Run("a MediaFile reconcile does not release the Episode fields catalogarr owns", func(t *testing.T) {
		const (
			series = "the-wire"
			name   = "the-wire-s01e01"
		)
		mustSeries(t, ctx, c, ns, series, "cutoff-met-at-1080p")
		mustEpisode(t, ctx, c, ns, name, series, 1, 1)

		// status.airDate belongs to the Series reconciler, under
		// k8s.ManagerCatalogarrSeries -- a genuinely aired episode, not a
		// blank one.
		_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrSeries,
			catalogac.Episode(name, ns).WithStatus(catalogac.EpisodeStatus().
				WithAirDate(metav1.NewTime(time.Now().Add(-48*time.Hour)))))
		require.NoError(t, err)

		filePath := writeFile(t, dir, "the-wire-s01e01.mkv", []byte("wire"))
		importarrCreatesMediaFileFor(t, ctx, c, ns, name+"-abc1234567",
			commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: name}, filePath, 4, time.Now(), bluray)

		var owner catalogv1alpha1.Episode
		require.NoError(t, bare.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &owner))
		mustDownload(t, ctx, c, &owner, name+"-dl", commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: name}, downloadv1alpha1.DownloadPhaseAssigned)

		require.Eventually(t, func() bool {
			var got catalogv1alpha1.Episode
			if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got); err != nil {
				return false
			}
			var gotMF catalogv1alpha1.MediaFile
			if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name + "-abc1234567"}, &gotMF); err != nil {
				return false
			}
			var gotDL downloadv1alpha1.Download
			if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name + "-dl"}, &gotDL); err != nil {
				return false
			}
			return got.Status.AirDate != nil && gotDL.Status.Phase == downloadv1alpha1.DownloadPhaseAssigned
		}, 10*time.Second, 20*time.Millisecond, "setup: cache never observed airDate, the file and the Download")

		er := &episode.Reconciler{Client: c, Scheme: scheme, Recorder: k8sevents.NewFakeRecorder(20)}
		req := reconcile.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: name}}
		_, err = er.Reconcile(ctx, req)
		require.NoError(t, err)

		var before catalogv1alpha1.Episode
		require.NoError(t, bare.Get(ctx, req.NamespacedName, &before))
		require.NotZero(t, before.Status.ObservedGeneration, "setup: the Episode reconciler must have set status.observedGeneration")
		require.NotNil(t, before.Status.ActiveDownloadRef, "setup: the Episode reconciler must still hold status.activeDownloadRef")
		require.True(t, before.Status.HasFile, "setup: the Episode reconciler must have rolled up the MediaFile itself")

		_, err = newMediaFileReconciler().Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Namespace: ns, Name: name + "-abc1234567"},
		})
		require.NoError(t, err)

		var after catalogv1alpha1.Episode
		require.NoError(t, bare.Get(ctx, req.NamespacedName, &after))
		assert.Equal(t, before.Status.ObservedGeneration, after.Status.ObservedGeneration, "a MediaFile reconcile must not release Episode status.observedGeneration")
		if assert.NotNil(t, after.Status.ActiveDownloadRef, "a MediaFile reconcile must not release Episode status.activeDownloadRef -- releasing it orphans the in-flight Download") {
			assert.Equal(t, *before.Status.ActiveDownloadRef, *after.Status.ActiveDownloadRef)
		}
		// status.airDate is the Series reconciler's, under a different field
		// manager, so it can never be released by a catalogarr apply -- but
		// asserting it pins the manager split the fix relies on.
		assert.NotNil(t, after.Status.AirDate, "status.airDate belongs to catalogarr-series and must be untouched")
	})
}
