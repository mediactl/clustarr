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
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/config"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	transcodeac "github.com/mediactl/clustarr/api/applyconfiguration/transcode/transcode/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/mediafile"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/naming/catalogctx"
	"github.com/mediactl/clustarr/pkg/quality"
)

// webdl720 is the name-derived quality the naming fixtures import under. The
// fake probe reports 1920x1080, so the probe-corrected quality is
// WEBDL-1080p: a fixture whose spec quality already matched the probe could
// not tell a render that used the corrected quality from one that used the
// frozen one, and status.naming.quality exists precisely so the rename can
// re-apply the corrected value and land on a name that stays current.
var webdl720 = commonv1.Quality{Name: "WEBDL-720p", Source: commonv1.SourceWebDL, Resolution: commonv1.Resolution720p, Modifier: commonv1.ModifierNone}

// mustRootFolder creates a RootFolder with every naming setting left to its
// CRD default, as a user's kubectl apply of a bare RootFolder would.
func mustRootFolder(t *testing.T, ctx context.Context, c client.Client, ns, name, path string, kind catalogv1alpha1.RootFolderKind) {
	t.Helper()
	rf := &catalogv1alpha1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       catalogv1alpha1.RootFolderSpec{Path: path, Kind: kind},
	}
	require.NoError(t, client.IgnoreAlreadyExists(c.Create(ctx, rf)))
}

// setMovieMetadata stands in for the metadata gateway, the sole writer of
// Movie.status.metadata, under its own field manager.
func setMovieMetadata(t *testing.T, ctx context.Context, c client.Client, ns, name, title string, year int32) {
	t.Helper()
	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrMetadata,
		catalogac.Movie(name, ns).WithStatus(catalogac.MovieStatus().WithMetadata(
			catalogac.MovieMetadata().WithTitle(title).WithYear(year).
				WithExternalIDs(map[string]string{"imdb": "tt1375666"}))))
	require.NoError(t, err)
}

// wantMoviePath renders what an import of mf would have produced, through
// the same catalogctx calls fileimport makes, from the objects as the
// apiserver now holds them -- with the probe-corrected quality, which is the
// value the rename re-applies into spec.quality.
func wantMoviePath(t *testing.T, ctx context.Context, c client.Client, mf *catalogv1alpha1.MediaFile) string {
	t.Helper()
	var movie catalogv1alpha1.Movie
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: mf.Namespace, Name: mf.Spec.MediaRef.Name}, &movie))
	var root catalogv1alpha1.RootFolder
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: mf.Namespace, Name: movie.Spec.RootFolderRef}, &root))
	base, ok := catalogctx.Movie(&movie)
	require.True(t, ok, "setup: the movie has no metadata title")
	spec := mf.Spec
	spec.Quality, _ = quality.AugmentFromMediaInfo(spec.Quality, mf.Status.MediaInfo)
	want, err := catalogctx.MovieFilePath(&root, &movie, catalogctx.File(t.Context(), base, &spec, mf.Status.MediaInfo),
		catalogctx.ContainerExt(mf.Status.MediaInfo, mf.Spec.Path))
	require.NoError(t, err)
	return want
}

// namingOwnedByCatalogarr reports whether k8s.ManagerCatalogarr's status
// entry in managedFields claims status.naming -- the one place an SSA
// release is visible (CLAUDE.md, "A double-claim is silent").
func namingOwnedByCatalogarr(entries []metav1.ManagedFieldsEntry) bool {
	return catalogarrNamingFields(entries) != nil
}

// catalogarrNamingFields is k8s.ManagerCatalogarr's FieldsV1 set under
// f:status.f:naming, nil when it claims none: SSA tracks ownership per leaf,
// so a release test asserts the leaves, not the parent (CLAUDE.md).
func catalogarrNamingFields(entries []metav1.ManagedFieldsEntry) map[string]any {
	fields := managedFieldPaths(entries, k8s.ManagerCatalogarr.String(), "status")
	status, ok := fields["f:status"].(map[string]any)
	if !ok {
		return nil
	}
	naming, _ := status["f:naming"].(map[string]any)
	return naming
}

// TestNamingProposesTheCanonicalPath is Task 9's (a): a probed MediaFile of
// a Movie with metadata, under a RootFolder, gets the path an import would
// have given it in status.naming -- codec block, probe-corrected quality and
// all -- and a NamingCurrent condition saying its spec.path is not it.
func TestNamingProposesTheCanonicalPath(t *testing.T) {
	c, _ := startEnv(t)
	ctx := t.Context()
	const ns, name = "naming-render", "inception-abc1234567"
	key := types.NamespacedName{Namespace: ns, Name: name}
	mustNamespace(t, ctx, c, ns)
	mustQualityProfile(t, ctx, c, "qp-video")
	mustRootFolder(t, ctx, c, ns, "movies", "/data/media/movies", catalogv1alpha1.RootFolderKindMovie)
	mustMovie(t, ctx, c, ns, "inception", "qp-video")
	setMovieMetadata(t, ctx, c, ns, "inception", "Inception", 2010)

	path := writeFile(t, t.TempDir(), "Inception.2010.720p.WEB-DL.mkv", []byte("stand-in bytes"))
	stat, err := os.Stat(path)
	require.NoError(t, err)
	importarrCreatesMediaFile(t, ctx, c, ns, name, path, stat.Size(), stat.ModTime(), webdl720)

	r := &mediafile.Reconciler{Client: c, Probe: fakeProbe, Clock: time.Now}
	_, err = r.Reconcile(ctx, ctrl.Request{NamespacedName: key})
	require.NoError(t, err)

	var got catalogv1alpha1.MediaFile
	require.NoError(t, c.Get(ctx, key, &got))
	require.NotNil(t, got.Status.MediaInfo, "setup: the probe never landed")
	require.NotNil(t, got.Status.Naming, "status.naming was never rendered")

	want := wantMoviePath(t, ctx, c, &got)
	assert.Equal(t, want, got.Status.Naming.ExpectedPath)
	assert.True(t, strings.HasPrefix(want, "/data/media/movies/Inception (2010)"), "rendered under the RootFolder's own path: %s", want)
	assert.Contains(t, want, "[h264]", "the probed codec is stated in the name")
	assert.Contains(t, want, "WEBDL-1080p", "the name carries the probe-corrected quality, not the frozen 720p")
	assert.Equal(t, ".mkv", want[len(want)-len(".mkv"):])
	assert.False(t, got.Status.Naming.Current, "spec.path is a release name, not the canonical one")
	assert.Empty(t, got.Status.Naming.Reason)
	require.NotNil(t, got.Status.Naming.Quality)
	assert.Equal(t, "WEBDL-1080p", got.Status.Naming.Quality.Name)
	assert.Equal(t, "WEBDL-720p", got.Spec.Quality.Name, "the frozen spec quality is importarr's; catalogarr only proposes")

	cond := k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.ConditionNamingCurrent)
	require.NotNil(t, cond, "no NamingCurrent condition")
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, "Stale", cond.Reason)
	assert.True(t, namingOwnedByCatalogarr(got.ManagedFields), "status.naming is not claimed by %s", k8s.ManagerCatalogarr)
}

// TestNamingEpisodeRendersThroughItsSeries is the Episode half of (a): an
// episode file is named from its Series' metadata and its Episode's
// numbering and title, under the Series' RootFolder.
func TestNamingEpisodeRendersThroughItsSeries(t *testing.T) {
	c, _ := startEnv(t)
	ctx := t.Context()
	const ns, name = "naming-episode", "firefly-s01e01-abc1234567"
	key := types.NamespacedName{Namespace: ns, Name: name}
	mustNamespace(t, ctx, c, ns)
	mustQualityProfile(t, ctx, c, "qp-video")
	mustRootFolder(t, ctx, c, ns, "tv", "/data/media/tv", catalogv1alpha1.RootFolderKindSeries)
	mustSeries(t, ctx, c, ns, "firefly", "qp-video")
	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrMetadata,
		catalogac.Series("firefly", ns).WithStatus(catalogac.SeriesStatus().WithMetadata(
			catalogac.SeriesMetadata().WithTitle("Firefly").WithYear(2002))))
	require.NoError(t, err)
	mustEpisode(t, ctx, c, ns, "firefly-s01e01", "firefly", 1, 1)
	_, err = k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrSeries,
		catalogac.Episode("firefly-s01e01", ns).WithStatus(catalogac.EpisodeStatus().WithTitle("Serenity")))
	require.NoError(t, err)

	path := writeFile(t, t.TempDir(), "Firefly.S01E01.720p.WEB-DL.mkv", []byte("stand-in bytes"))
	stat, err := os.Stat(path)
	require.NoError(t, err)
	importarrCreatesMediaFileFor(t, ctx, c, ns, name,
		commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: "firefly-s01e01"},
		path, stat.Size(), stat.ModTime(), webdl720)

	r := &mediafile.Reconciler{Client: c, Probe: fakeProbe, Clock: time.Now}
	_, err = r.Reconcile(ctx, ctrl.Request{NamespacedName: key})
	require.NoError(t, err)

	var got catalogv1alpha1.MediaFile
	require.NoError(t, c.Get(ctx, key, &got))
	require.NotNil(t, got.Status.Naming, "status.naming was never rendered")

	var series catalogv1alpha1.Series
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "firefly"}, &series))
	var ep catalogv1alpha1.Episode
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "firefly-s01e01"}, &ep))
	var root catalogv1alpha1.RootFolder
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "tv"}, &root))
	base, ok := catalogctx.Episode(&series, []catalogv1alpha1.Episode{ep})
	require.True(t, ok)
	spec := got.Spec
	spec.Quality, _ = quality.AugmentFromMediaInfo(spec.Quality, got.Status.MediaInfo)
	want, err := catalogctx.EpisodeFilePath(&root, &series, catalogctx.File(t.Context(), base, &spec, got.Status.MediaInfo),
		catalogctx.ContainerExt(got.Status.MediaInfo, got.Spec.Path))
	require.NoError(t, err)

	assert.Equal(t, want, got.Status.Naming.ExpectedPath)
	assert.True(t, strings.HasPrefix(want, "/data/media/tv/Firefly (2002)"), "rendered under the Series' RootFolder: %s", want)
	assert.Contains(t, want, "S01E01")
	assert.Contains(t, want, "Serenity")
	assert.Contains(t, want, "[h264]")
	assert.False(t, got.Status.Naming.Current)
	assert.Empty(t, got.Status.Naming.Reason)
}

// TestNamingReportsWhyItCannotRender is Task 9's (b), plus the metadata gate
// beside it: a file with no probe yet, and an item with no metadata yet, get
// a reason and no expectedPath -- and NamingCurrent says Unknown, because
// nothing has been rendered to compare spec.path with.
func TestNamingReportsWhyItCannotRender(t *testing.T) {
	c, _ := startEnv(t)
	ctx := t.Context()
	const ns = "naming-pending"
	mustNamespace(t, ctx, c, ns)
	mustQualityProfile(t, ctx, c, "qp-video")
	mustRootFolder(t, ctx, c, ns, "movies", "/data/media/movies", catalogv1alpha1.RootFolderKindMovie)

	failingProbe := func(context.Context, string) (*commonv1.MediaInfo, *mediainfo.Raw, error) {
		return nil, nil, errors.New("ffprobe: Invalid data found when processing input")
	}

	for _, tc := range []struct {
		name     string
		movie    string
		metadata bool
		probe    mediafile.ProbeFunc
		want     catalogv1alpha1.NamingReason
	}{
		{name: "a file never probed", movie: "inception", metadata: true, probe: failingProbe, want: catalogv1alpha1.NamingReasonProbePending},
		{name: "an item without metadata", movie: "memento", metadata: false, probe: fakeProbe, want: catalogv1alpha1.NamingReasonMetadataPending},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mfName := tc.movie + "-abc1234567"
			key := types.NamespacedName{Namespace: ns, Name: mfName}
			mustMovie(t, ctx, c, ns, tc.movie, "qp-video")
			if tc.metadata {
				setMovieMetadata(t, ctx, c, ns, tc.movie, "Inception", 2010)
			}
			path := writeFile(t, t.TempDir(), tc.movie+".mkv", []byte("stand-in bytes"))
			stat, err := os.Stat(path)
			require.NoError(t, err)
			importarrCreatesMediaFileFor(t, ctx, c, ns, mfName,
				commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: tc.movie},
				path, stat.Size(), stat.ModTime(), webdl720)

			r := &mediafile.Reconciler{Client: c, Probe: tc.probe, Clock: time.Now}
			_, err = r.Reconcile(ctx, ctrl.Request{NamespacedName: key})
			require.NoError(t, err)

			var got catalogv1alpha1.MediaFile
			require.NoError(t, c.Get(ctx, key, &got))
			require.NotNil(t, got.Status.Naming, "status.naming was never rendered")
			assert.Equal(t, tc.want, got.Status.Naming.Reason)
			assert.Empty(t, got.Status.Naming.ExpectedPath)
			assert.False(t, got.Status.Naming.Current)

			cond := k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.ConditionNamingCurrent)
			require.NotNil(t, cond, "no NamingCurrent condition")
			assert.Equal(t, metav1.ConditionUnknown, cond.Status)
			assert.Equal(t, string(tc.want), cond.Reason)
		})
	}
}

// TestNamingFollowsTheMoviesTitle is Task 9's (c), against a running
// manager rather than a direct Reconcile call: the MediaFile's own watch
// never fires for a change to its Movie (the MediaFile's generation does not
// move), so only the Movie watch can carry a new title into expectedPath.
func TestNamingFollowsTheMoviesTitle(t *testing.T) {
	_, cfg := startEnv(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 k8s.MustNewScheme(),
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
		Controller:             config.Controller{SkipNameValidation: ptr.To(true)},
	})
	require.NoError(t, err)
	r := mediafile.NewReconciler(mgr.GetClient(), mgr.GetScheme(), events.NewFakeRecorder(32))
	r.Probe = fakeProbe
	require.NoError(t, r.SetupWithManager(mgr))
	go func() { _ = mgr.Start(ctx) }()
	require.True(t, mgr.GetCache().WaitForCacheSync(ctx))
	c := mgr.GetClient()

	const ns, name = "naming-watch", "inception-abc1234567"
	key := types.NamespacedName{Namespace: ns, Name: name}
	mustNamespace(t, ctx, c, ns)
	mustQualityProfile(t, ctx, c, "qp-video")
	mustRootFolder(t, ctx, c, ns, "movies", "/data/media/movies", catalogv1alpha1.RootFolderKindMovie)
	mustMovie(t, ctx, c, ns, "inception", "qp-video")
	setMovieMetadata(t, ctx, c, ns, "inception", "Inception", 2010)

	path := writeFile(t, t.TempDir(), "Inception.2010.720p.WEB-DL.mkv", []byte("stand-in bytes"))
	stat, err := os.Stat(path)
	require.NoError(t, err)
	importarrCreatesMediaFile(t, ctx, c, ns, name, path, stat.Size(), stat.ModTime(), webdl720)

	var got catalogv1alpha1.MediaFile
	require.Eventually(t, func() bool {
		if err := c.Get(ctx, key, &got); err != nil {
			return false
		}
		return got.Status.Naming != nil && strings.Contains(got.Status.Naming.ExpectedPath, "/Inception (2010) [tmdbid-27205]/")
	}, 10*time.Second, 100*time.Millisecond, "the initial render never landed")

	setMovieMetadata(t, ctx, c, ns, "inception", "Inception Redux", 2010)

	require.Eventually(t, func() bool {
		if err := c.Get(ctx, key, &got); err != nil {
			return false
		}
		return got.Status.Naming != nil && strings.Contains(got.Status.Naming.ExpectedPath, "/Inception Redux (2010) [tmdbid-27205]/")
	}, 10*time.Second, 100*time.Millisecond, "the Movie watch did not carry the new title into status.naming")
	assert.Equal(t, wantMoviePath(t, ctx, c, &got), got.Status.Naming.ExpectedPath)
	require.NotNil(t, got.Status.MediaInfo, "the re-render released the probe result")
}

// TestNamingSurvivesAMissingFile is Task 9's (d), the release test. It
// drives the MediaFile to a real steady state first -- probed, and named --
// and only then removes the file, so the FileMissing early return has
// something to release (CLAUDE.md: a test that triggers a failure path on
// a blank object cannot observe a release). The movie's title changes
// before the missing-file reconcile too: FileMissing re-sends the proposal
// it already had rather than rendering one for a file that is not there,
// so the value must be exactly the steady one, not a fresh render that
// happens to match it.
func TestNamingSurvivesAMissingFile(t *testing.T) {
	c, _ := startEnv(t)
	ctx := t.Context()
	const ns, name = "naming-release", "inception-abc1234567"
	key := types.NamespacedName{Namespace: ns, Name: name}
	mustNamespace(t, ctx, c, ns)
	mustQualityProfile(t, ctx, c, "qp-video")
	mustRootFolder(t, ctx, c, ns, "movies", "/data/media/movies", catalogv1alpha1.RootFolderKindMovie)
	mustMovie(t, ctx, c, ns, "inception", "qp-video")
	setMovieMetadata(t, ctx, c, ns, "inception", "Inception", 2010)

	path := writeFile(t, t.TempDir(), "Inception.2010.720p.WEB-DL.mkv", []byte("stand-in bytes"))
	stat, err := os.Stat(path)
	require.NoError(t, err)
	importarrCreatesMediaFile(t, ctx, c, ns, name, path, stat.Size(), stat.ModTime(), webdl720)

	r := &mediafile.Reconciler{Client: c, Probe: fakeProbe, Clock: time.Now}
	_, err = r.Reconcile(ctx, ctrl.Request{NamespacedName: key})
	require.NoError(t, err)

	var steady catalogv1alpha1.MediaFile
	require.NoError(t, c.Get(ctx, key, &steady))
	require.NotEmpty(t, steady.Status.ProbeHash, "setup: never reached a probed steady state")
	require.NotNil(t, steady.Status.Naming, "setup: status.naming never landed")
	require.NotEmpty(t, steady.Status.Naming.ExpectedPath, "setup: status.naming has no path")
	require.True(t, namingOwnedByCatalogarr(steady.ManagedFields), "setup: catalogarr does not own status.naming")

	setMovieMetadata(t, ctx, c, ns, "inception", "Inception Redux", 2010)
	require.NoError(t, os.Remove(path))

	res, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key})
	require.NoError(t, err)
	assert.Equal(t, time.Minute, res.RequeueAfter, "setup: this was not the FileMissing path")

	// c is a direct client (client.New, no cache), so managedFields are the
	// apiserver's own and not a cache's stripped copy.
	var got catalogv1alpha1.MediaFile
	require.NoError(t, c.Get(ctx, key, &got))
	ready := k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.MediaFileConditionReady)
	require.NotNil(t, ready)
	assert.Equal(t, "FileMissing", ready.Reason)

	assert.True(t, namingOwnedByCatalogarr(got.ManagedFields),
		"the FileMissing apply released status.naming from %s: %+v", k8s.ManagerCatalogarr, fieldManagerNames(got.ManagedFields))
	leaves := catalogarrNamingFields(got.ManagedFields)
	for _, leaf := range []string{"f:expectedPath", "f:current", "f:quality"} {
		assert.Contains(t, leaves, leaf, "the FileMissing apply released status.naming.%s", strings.TrimPrefix(leaf, "f:"))
	}
	qualityLeaves, _ := leaves["f:quality"].(map[string]any)
	assert.Contains(t, qualityLeaves, "f:name", "the FileMissing apply released status.naming.quality.name")
	assert.Equal(t, steady.Status.Naming, got.Status.Naming, "status.naming changed on a missing file")
	assert.Equal(t, steady.Status.ProbeHash, got.Status.ProbeHash, "status.probeHash was released")
	assert.Equal(t, steady.Status.MediaInfo, got.Status.MediaInfo, "status.mediaInfo was released")

	cond := k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.ConditionNamingCurrent)
	require.NotNil(t, cond, "the NamingCurrent condition was dropped")
	assert.Equal(t, "Stale", cond.Reason)
}

// containerProbe is fakeProbe with the container and codec the file's own
// extension implies, as the real ffprobeexec.Probe records the container: an
// .avi is an XviD AVI, an .mkv an HEVC Matroska -- so a render can show a
// transcode's container change.
func containerProbe(ctx context.Context, path string) (*commonv1.MediaInfo, *mediainfo.Raw, error) {
	mi, raw, err := fakeProbe(ctx, path)
	if err != nil {
		return nil, nil, err
	}
	mi.Container = strings.TrimPrefix(filepath.Ext(path), ".")
	mi.VideoCodec = "mpeg4"
	if mi.Container == "mkv" {
		mi.VideoCodec = "hevc"
	}
	return mi, raw, nil
}

// patchTranscodeJobStatus stands in for squasharr, the sole writer of
// TranscodeJob.status, declaring its whole status on every apply.
func patchTranscodeJobStatus(t *testing.T, ctx context.Context, c client.Client, ns, name string, st *transcodeac.TranscodeJobStatusApplyConfiguration) {
	t.Helper()
	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerSquasharr, transcodeac.TranscodeJob(name, ns).WithStatus(st))
	require.NoError(t, err)
}

// TestNamingHoldsWhileATranscodeRuns is ruling R18 (spec D6: a rename never
// touches a file another job holds), against a running manager: a
// TranscodeJob that is merely Running must turn the proposal into
// TranscodePending -- which only the TranscodeJob watch firing on a
// non-Succeeded phase can bring about, since the MediaFile itself does not
// change -- and once it succeeds with a container change and the swap is
// incorporated, the proposal renders again with the new extension and codec.
// The fixture is TestContainerChangeMovesSpecPath's: an .avi encoded to
// .mkv, the source retired.
func TestNamingHoldsWhileATranscodeRuns(t *testing.T) {
	_, cfg := startEnv(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 k8s.MustNewScheme(),
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
		Controller:             config.Controller{SkipNameValidation: ptr.To(true)},
	})
	require.NoError(t, err)
	r := mediafile.NewReconciler(mgr.GetClient(), mgr.GetScheme(), events.NewFakeRecorder(64))
	r.Probe = containerProbe
	require.NoError(t, r.SetupWithManager(mgr))
	go func() { _ = mgr.Start(ctx) }()
	require.True(t, mgr.GetCache().WaitForCacheSync(ctx))
	c := mgr.GetClient()

	const ns, name = "naming-transcode", "inception-abc1234567"
	key := types.NamespacedName{Namespace: ns, Name: name}
	mustNamespace(t, ctx, c, ns)
	mustQualityProfile(t, ctx, c, "qp-video")
	mustTranscodeProfile(t, ctx, c, "hevc-main10", "profile-hash-def")
	mustRootFolder(t, ctx, c, ns, "movies", "/data/media/movies", catalogv1alpha1.RootFolderKindMovie)
	mustMovie(t, ctx, c, ns, "inception", "qp-video")
	setMovieMetadata(t, ctx, c, ns, "inception", "Inception", 2010)

	source := writeFile(t, t.TempDir(), "Inception.2010.720p.WEB-DL.avi", []byte("an avi as imported"))
	stat, err := os.Stat(source)
	require.NoError(t, err)
	importarrCreatesMediaFile(t, ctx, c, ns, name, source, stat.Size(), stat.ModTime(), webdl720)

	var got catalogv1alpha1.MediaFile
	require.Eventually(t, func() bool {
		return c.Get(ctx, key, &got) == nil && got.Status.Naming != nil && got.Status.Naming.ExpectedPath != ""
	}, 10*time.Second, 100*time.Millisecond, "the initial render never landed")
	require.True(t, strings.HasSuffix(got.Status.Naming.ExpectedPath, "[XviD].avi"), "setup: %s", got.Status.Naming.ExpectedPath)

	tj := &transcodev1alpha1.TranscodeJob{
		ObjectMeta: metav1.ObjectMeta{Name: name + "-enc01", Namespace: ns},
		Spec:       transcodev1alpha1.TranscodeJobSpec{MediaFileRef: name, ProfileRef: "hevc-main10", SourcePath: source},
	}
	require.NoError(t, c.Create(ctx, tj))
	patchTranscodeJobStatus(t, ctx, c, ns, tj.Name, transcodeac.TranscodeJobStatus().WithPhase(transcodev1alpha1.TranscodeJobPhaseRunning))

	require.Eventually(t, func() bool {
		return c.Get(ctx, key, &got) == nil && got.Status.Naming != nil &&
			got.Status.Naming.Reason == catalogv1alpha1.NamingReasonTranscodePending
	}, 10*time.Second, 100*time.Millisecond, "a running transcode did not hold the rename")
	assert.Empty(t, got.Status.Naming.ExpectedPath, "a held file proposes no path")
	assert.False(t, got.Status.Naming.Current)
	cond := k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.ConditionNamingCurrent)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionUnknown, cond.Status)
	assert.Equal(t, "TranscodePending", cond.Reason)

	// squasharr's worker: place the output under its new name, retire the
	// source, then report Succeeded -- finished after the probe's second.
	output := strings.TrimSuffix(source, ".avi") + ".mkv"
	require.NoError(t, os.WriteFile(output, []byte("a matroska encode, not the same size"), 0o644))
	require.NoError(t, os.Remove(source))
	time.Sleep(1100 * time.Millisecond)
	patchTranscodeJobStatus(t, ctx, c, ns, tj.Name, transcodeac.TranscodeJobStatus().
		WithPhase(transcodev1alpha1.TranscodeJobPhaseSucceeded).
		WithFinishedAt(metav1.NewTime(time.Now())).
		WithResult(transcodeac.Result().WithOutputPath(output).WithOutputSizeBytes(int64(len("a matroska encode, not the same size")))))

	require.Eventually(t, func() bool {
		return c.Get(ctx, key, &got) == nil && got.Spec.Path == output &&
			got.Status.Naming != nil && got.Status.Naming.ExpectedPath != ""
	}, 10*time.Second, 100*time.Millisecond, "the incorporated swap never rendered a proposal")
	assert.Empty(t, got.Status.Naming.Reason)
	assert.True(t, strings.HasSuffix(got.Status.Naming.ExpectedPath, "[h265].mkv"),
		"the proposal follows the encode's container and codec: %s", got.Status.Naming.ExpectedPath)
	assert.Equal(t, wantMoviePath(t, ctx, c, &got), got.Status.Naming.ExpectedPath)
}

// TestNamingHoldsAStaleFileWhoseProbeFailed is ruling R20: once a file's
// bytes have changed, the last good probe no longer describes it, so a
// failed re-probe holds the proposal as ProbePending rather than rendering
// the name the old bytes earned.
func TestNamingHoldsAStaleFileWhoseProbeFailed(t *testing.T) {
	c, _ := startEnv(t)
	ctx := t.Context()
	const ns, name = "naming-stale", "inception-abc1234567"
	key := types.NamespacedName{Namespace: ns, Name: name}
	mustNamespace(t, ctx, c, ns)
	mustQualityProfile(t, ctx, c, "qp-video")
	mustRootFolder(t, ctx, c, ns, "movies", "/data/media/movies", catalogv1alpha1.RootFolderKindMovie)
	mustMovie(t, ctx, c, ns, "inception", "qp-video")
	setMovieMetadata(t, ctx, c, ns, "inception", "Inception", 2010)

	path := writeFile(t, t.TempDir(), "Inception.2010.720p.WEB-DL.mkv", []byte("stand-in bytes"))
	stat, err := os.Stat(path)
	require.NoError(t, err)
	importarrCreatesMediaFile(t, ctx, c, ns, name, path, stat.Size(), stat.ModTime(), webdl720)
	_, err = (&mediafile.Reconciler{Client: c, Probe: fakeProbe, Clock: time.Now}).Reconcile(ctx, ctrl.Request{NamespacedName: key})
	require.NoError(t, err)
	var steady catalogv1alpha1.MediaFile
	require.NoError(t, c.Get(ctx, key, &steady))
	require.NotNil(t, steady.Status.Naming)
	require.NotEmpty(t, steady.Status.Naming.ExpectedPath, "setup: never rendered")

	require.NoError(t, os.WriteFile(path, []byte("different bytes, unreadable by ffprobe"), 0o644))
	failing := &mediafile.Reconciler{
		Client: c, Clock: time.Now,
		Probe: func(context.Context, string) (*commonv1.MediaInfo, *mediainfo.Raw, error) {
			return nil, nil, errors.New("ffprobe: Invalid data found when processing input")
		},
	}
	res, err := failing.Reconcile(ctx, ctrl.Request{NamespacedName: key})
	require.NoError(t, err)
	assert.Equal(t, 30*time.Second, res.RequeueAfter, "setup: this was not the ProbeFailed path")

	var got catalogv1alpha1.MediaFile
	require.NoError(t, c.Get(ctx, key, &got))
	require.NotNil(t, got.Status.Naming)
	assert.Equal(t, catalogv1alpha1.NamingReasonProbePending, got.Status.Naming.Reason)
	assert.Empty(t, got.Status.Naming.ExpectedPath, "the old bytes' name is withdrawn")
	assert.Nil(t, got.Status.Naming.Quality, "the old probe's quality does not describe these bytes")
	assert.Equal(t, steady.Status.MediaInfo, got.Status.MediaInfo, "the last good probe itself is kept")
	cond := k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.ConditionNamingCurrent)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionUnknown, cond.Status)
	assert.Equal(t, "ProbePending", cond.Reason)
}

// TestNamingUnrenderableClearsThePath: a proposal already rendered is
// withdrawn when it can no longer be rendered -- here the RootFolder is
// deleted -- so no rename acts on a path nothing can vouch for.
func TestNamingUnrenderableClearsThePath(t *testing.T) {
	c, _ := startEnv(t)
	ctx := t.Context()
	const ns, name = "naming-unrenderable", "inception-abc1234567"
	key := types.NamespacedName{Namespace: ns, Name: name}
	mustNamespace(t, ctx, c, ns)
	mustQualityProfile(t, ctx, c, "qp-video")
	mustRootFolder(t, ctx, c, ns, "movies", "/data/media/movies", catalogv1alpha1.RootFolderKindMovie)
	mustMovie(t, ctx, c, ns, "inception", "qp-video")
	setMovieMetadata(t, ctx, c, ns, "inception", "Inception", 2010)

	path := writeFile(t, t.TempDir(), "Inception.2010.720p.WEB-DL.mkv", []byte("stand-in bytes"))
	stat, err := os.Stat(path)
	require.NoError(t, err)
	importarrCreatesMediaFile(t, ctx, c, ns, name, path, stat.Size(), stat.ModTime(), webdl720)
	r := &mediafile.Reconciler{Client: c, Probe: fakeProbe, Clock: time.Now}
	_, err = r.Reconcile(ctx, ctrl.Request{NamespacedName: key})
	require.NoError(t, err)
	var steady catalogv1alpha1.MediaFile
	require.NoError(t, c.Get(ctx, key, &steady))
	require.NotNil(t, steady.Status.Naming)
	require.NotEmpty(t, steady.Status.Naming.ExpectedPath, "setup: never rendered")

	require.NoError(t, c.Delete(ctx, &catalogv1alpha1.RootFolder{ObjectMeta: metav1.ObjectMeta{Name: "movies", Namespace: ns}}))
	_, err = r.Reconcile(ctx, ctrl.Request{NamespacedName: key})
	require.NoError(t, err)

	var got catalogv1alpha1.MediaFile
	require.NoError(t, c.Get(ctx, key, &got))
	require.NotNil(t, got.Status.Naming)
	assert.Equal(t, catalogv1alpha1.NamingReasonUnrenderable, got.Status.Naming.Reason)
	assert.Empty(t, got.Status.Naming.ExpectedPath, "the previous expectedPath was not cleared")
	assert.False(t, got.Status.Naming.Current)
	assert.NotContains(t, catalogarrNamingFields(got.ManagedFields), "f:expectedPath", "catalogarr still claims a cleared expectedPath")
	cond := k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.ConditionNamingCurrent)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionUnknown, cond.Status)
	assert.Equal(t, "Unrenderable", cond.Reason)
}

// TestNamingKeepsItsProposalOverALookupFailure: a lookup that fails for any
// reason but NotFound -- a cache blip -- keeps the proposal already on the
// object rather than withdrawing or releasing it, and requeues so the render
// is retried: nothing else would wake the reconcile for it.
func TestNamingKeepsItsProposalOverALookupFailure(t *testing.T) {
	c, cfg := startEnv(t)
	ctx := t.Context()
	const ns, name = "naming-blip", "inception-abc1234567"
	key := types.NamespacedName{Namespace: ns, Name: name}
	mustNamespace(t, ctx, c, ns)
	mustQualityProfile(t, ctx, c, "qp-video")
	mustRootFolder(t, ctx, c, ns, "movies", "/data/media/movies", catalogv1alpha1.RootFolderKindMovie)
	mustMovie(t, ctx, c, ns, "inception", "qp-video")
	setMovieMetadata(t, ctx, c, ns, "inception", "Inception", 2010)

	path := writeFile(t, t.TempDir(), "Inception.2010.720p.WEB-DL.mkv", []byte("stand-in bytes"))
	stat, err := os.Stat(path)
	require.NoError(t, err)
	importarrCreatesMediaFile(t, ctx, c, ns, name, path, stat.Size(), stat.ModTime(), webdl720)
	res, err := (&mediafile.Reconciler{Client: c, Probe: fakeProbe, Clock: time.Now}).Reconcile(ctx, ctrl.Request{NamespacedName: key})
	require.NoError(t, err)
	require.Zero(t, res.RequeueAfter, "setup: a healthy render does not requeue")
	var steady catalogv1alpha1.MediaFile
	require.NoError(t, c.Get(ctx, key, &steady))
	require.NotNil(t, steady.Status.Naming)
	require.NotEmpty(t, steady.Status.Naming.ExpectedPath, "setup: never rendered")

	// The movie's title moves, so a render that went ahead would change the
	// proposal; the RootFolder read fails, so none can.
	setMovieMetadata(t, ctx, c, ns, "inception", "Inception Redux", 2010)
	wc, err := client.NewWithWatch(cfg, client.Options{Scheme: k8s.MustNewScheme()})
	require.NoError(t, err)
	blip := interceptor.NewClient(rawClient{wc}, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*catalogv1alpha1.RootFolder); ok {
				return apierrors.NewInternalError(errors.New("cache blip"))
			}
			return c.Get(ctx, key, obj, opts...)
		},
	})
	res, err = (&mediafile.Reconciler{Client: blip, Probe: fakeProbe, Clock: time.Now}).Reconcile(ctx, ctrl.Request{NamespacedName: key})
	require.NoError(t, err, "a failed naming lookup does not fail the reconcile")
	assert.Equal(t, 30*time.Second, res.RequeueAfter, "a render kept over a failed lookup must be retried")

	var got catalogv1alpha1.MediaFile
	require.NoError(t, c.Get(ctx, key, &got))
	assert.Equal(t, steady.Status.Naming, got.Status.Naming, "the proposal was not kept over the failed lookup")
	assert.True(t, namingOwnedByCatalogarr(got.ManagedFields))
}
