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

package naming_test

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
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/import/worker/probe/probetest"
	"github.com/mediactl/clustarr/app/remediation/remediationtest"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/naming/catalogctx"
	"github.com/mediactl/clustarr/pkg/quality"
)

// TestNamingProposesTheCanonicalPath: a probed MediaFile of a Movie with
// metadata, under a RootFolder, gets the path an import would have given it
// in status.naming -- codec block, probe-corrected quality and all -- and a
// NamingCurrent condition saying its spec.path is not it.
func TestNamingProposesTheCanonicalPath(t *testing.T) {
	f := newNamedFile(t, "naming-render")
	h := newHarness(t)
	r := h.loop(t, f, false)
	probeAndName(t, h, r, f, fakeMediaInfo())

	got := f.get(t)
	require.NotNil(t, got.Status.MediaInfo, "setup: the probe never landed")
	require.NotNil(t, got.Status.Naming, "status.naming was never rendered")
	want := wantMoviePath(t, f.c, &got)
	assert.Equal(t, want, got.Status.Naming.ExpectedPath)
	assert.True(t, strings.HasPrefix(want, "/data/media/movies/Inception (2010)"), "rendered under the RootFolder's own path: %s", want)
	assert.Contains(t, want, "[h264]", "the probed codec is stated in the name")
	assert.Contains(t, want, "WEBDL-1080p", "the name carries the probe-corrected quality, not the frozen 720p")
	assert.True(t, strings.HasSuffix(want, ".mkv"))
	assert.False(t, got.Status.Naming.Current, "spec.path is a release name, not the canonical one")
	assert.Empty(t, got.Status.Naming.Reason)
	require.NotNil(t, got.Status.Naming.Quality)
	assert.Equal(t, "WEBDL-1080p", got.Status.Naming.Quality.Name)
	assert.Equal(t, "WEBDL-720p", got.Spec.Quality.Name, "the frozen spec quality is the import's; catalog only proposes")

	cond := k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.ConditionNamingCurrent)
	require.NotNil(t, cond, "no NamingCurrent condition")
	assert.Equal(t, metav1.ConditionFalse, cond.Status)
	assert.Equal(t, "Stale", cond.Reason)
	for _, leaf := range []string{"f:expectedPath", "f:quality"} {
		assert.Contains(t, catalogNamingFields(got.ManagedFields), leaf, "status.naming is not claimed by catalog")
	}
}

// TestNamingEpisodeRendersThroughItsSeries is the Episode half: an episode
// file is named from its Series' metadata and its Episode's numbering and
// title, under the Series' RootFolder.
func TestNamingEpisodeRendersThroughItsSeries(t *testing.T) {
	e := remediationtest.Start(t)
	ctx := context.Background()
	const ns, name = "naming-episode", "firefly-s01e01-abc1234567"
	key := types.NamespacedName{Namespace: ns, Name: name}
	mustNamespace(t, ctx, e.Admin, ns)
	mustQualityProfile(t, ctx, e.Admin, "qp-video")
	mustRootFolder(t, e.Admin, ns, "tv", "/data/media/tv", catalogv1alpha1.RootFolderKindSeries, nil)
	mustSeries(t, ctx, e.Admin, ns, "firefly", "qp-video")
	_, err := k8s.PatchStatus(ctx, e.Admin, k8s.ManagerCatalogMetadata,
		catalogac.Series("firefly", ns).WithStatus(catalogac.SeriesStatus().WithMetadata(
			catalogac.SeriesMetadata().WithTitle("Firefly").WithYear(2002))))
	require.NoError(t, err)
	mustEpisode(t, ctx, e.Admin, ns, "firefly-s01e01", "firefly", 1, 1)
	_, err = k8s.PatchStatus(ctx, e.Admin, k8s.ManagerCatalogSeries,
		catalogac.Episode("firefly-s01e01", ns).WithStatus(catalogac.EpisodeStatus().WithTitle("Serenity")))
	require.NoError(t, err)
	path := writeFile(t, t.TempDir(), "Firefly.S01E01.720p.WEB-DL.mkv", []byte("stand-in bytes"))
	importCreates(t, e.Admin, ns, name, commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: "firefly-s01e01"}, path, webdl720)
	f := namedFile{e: e, c: e.Admin, key: key, path: path}

	h := newHarness(t)
	probeAndName(t, h, h.loop(t, f, false), f, fakeMediaInfo())

	got := f.get(t)
	require.NotNil(t, got.Status.Naming, "status.naming was never rendered")
	var series catalogv1alpha1.Series
	require.NoError(t, e.Admin.Get(ctx, types.NamespacedName{Namespace: ns, Name: "firefly"}, &series))
	var ep catalogv1alpha1.Episode
	require.NoError(t, e.Admin.Get(ctx, types.NamespacedName{Namespace: ns, Name: "firefly-s01e01"}, &ep))
	var root catalogv1alpha1.RootFolder
	require.NoError(t, e.Admin.Get(ctx, types.NamespacedName{Namespace: ns, Name: "tv"}, &root))
	base, ok := catalogctx.Episode(&series, []catalogv1alpha1.Episode{ep})
	require.True(t, ok)
	spec := got.Spec
	spec.Quality, _ = quality.AugmentFromMediaInfo(spec.Quality, got.Status.MediaInfo)
	want, err := catalogctx.EpisodeFilePath(&root, &series, catalogctx.File(ctx, base, &spec, got.Status.MediaInfo),
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

// TestNamingReportsWhyItCannotRender: a file the probe has not described
// keeps no naming at all (D-F3-5: naming plans only from a current probe),
// and an item with no metadata yet gets a reason and no expectedPath -- and
// NamingCurrent says Unknown, because nothing has been rendered to compare
// spec.path with.
func TestNamingReportsWhyItCannotRender(t *testing.T) {
	e := remediationtest.Start(t)
	ctx := context.Background()
	const ns = "naming-pending"
	mustNamespace(t, ctx, e.Admin, ns)
	mustQualityProfile(t, ctx, e.Admin, "qp-video")
	mustRootFolder(t, e.Admin, ns, "movies", "/data/media/movies", catalogv1alpha1.RootFolderKindMovie, nil)
	h := newHarness(t)

	for _, tc := range []struct {
		name     string
		movie    string
		metadata bool
		answer   schema.ProbeRecord
		want     catalogv1alpha1.NamingReason
	}{
		{name: "a file never probed", movie: "inception", metadata: true,
			answer: schema.ProbeRecord{RecordHeader: schema.RecordHeader{State: schema.ProbeFailed, Failure: "Invalid data found when processing input"}}},
		{name: "an item without metadata", movie: "memento",
			answer: schema.ProbeRecord{RecordHeader: schema.RecordHeader{State: schema.ProbeProbed}, MediaInfo: fakeMediaInfo()},
			want:   catalogv1alpha1.NamingReasonMetadataPending},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mfName := tc.movie + "-abc1234567"
			mustMovie(t, ctx, e.Admin, ns, tc.movie, "qp-video")
			if tc.metadata {
				setMovieMetadata(t, e.Admin, ns, tc.movie, "Inception", 2010)
			}
			path := writeFile(t, t.TempDir(), tc.movie+".mkv", []byte("stand-in bytes"))
			importCreates(t, e.Admin, ns, mfName, commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: tc.movie}, path, webdl720)
			f := namedFile{e: e, c: e.Admin, key: types.NamespacedName{Namespace: ns, Name: mfName}, path: path}
			r := h.loop(t, f, false)
			reconcileOnce(t, r, f.key)
			h.AnswerWith(t, f.uid(t), tc.answer)
			reconcileOnce(t, r, f.key)
			reconcileOnce(t, r, f.key)

			got := f.get(t)
			cond := k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.ConditionNamingCurrent)
			if tc.want == "" {
				assert.Nil(t, got.Status.Naming, "a file the probe has not described keeps no naming")
				assert.Nil(t, cond)
				return
			}
			require.NotNil(t, got.Status.Naming, "status.naming was never rendered")
			assert.Equal(t, tc.want, got.Status.Naming.Reason)
			assert.Empty(t, got.Status.Naming.ExpectedPath)
			assert.False(t, got.Status.Naming.Current)
			require.NotNil(t, cond, "no NamingCurrent condition")
			assert.Equal(t, metav1.ConditionUnknown, cond.Status)
			assert.Equal(t, string(tc.want), cond.Reason)
		})
	}
}

// TestNamingFollowsTheMoviesTitle, against a running manager: the
// MediaFile's own watch never fires for a change to its Movie (the
// MediaFile's generation does not move), so only S2' -- the naming planner's
// Movie source -- can carry a new title into expectedPath.
func TestNamingFollowsTheMoviesTitle(t *testing.T) {
	h := newHarness(t)
	h.WithAgent(t, remediationtest.FakeProber)
	e := remediationtest.StartControllerOn(t, h.Pub, h.planners()...)
	ctx := context.Background()
	const ns, name = "naming-watch", "inception-abc1234567"
	key := types.NamespacedName{Namespace: ns, Name: name}
	mustNamespace(t, ctx, e.Admin, ns)
	mustQualityProfile(t, ctx, e.Admin, "qp-video")
	mustRootFolder(t, e.Admin, ns, "movies", "/data/media/movies", catalogv1alpha1.RootFolderKindMovie, nil)
	mustMovie(t, ctx, e.Admin, ns, "inception", "qp-video")
	setMovieMetadata(t, e.Admin, ns, "inception", "Inception", 2010)
	path := writeFile(t, t.TempDir(), "Inception.2010.720p.WEB-DL.mkv", []byte("stand-in bytes"))
	importCreates(t, e.Admin, ns, name, commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "inception"}, path, webdl720)

	var got catalogv1alpha1.MediaFile
	require.Eventually(t, func() bool {
		got = *e.Get(t, key)
		return got.Status.Naming != nil && strings.Contains(got.Status.Naming.ExpectedPath, "/Inception (2010) [tmdbid-27205]/")
	}, 20*time.Second, 100*time.Millisecond, "the initial render never landed")

	setMovieMetadata(t, e.Admin, ns, "inception", "Inception Redux", 2010)
	require.Eventually(t, func() bool {
		got = *e.Get(t, key)
		return got.Status.Naming != nil && strings.Contains(got.Status.Naming.ExpectedPath, "/Inception Redux (2010) [tmdbid-27205]/")
	}, 20*time.Second, 100*time.Millisecond, "the Movie source did not carry the new title into status.naming")
	assert.Equal(t, wantMoviePath(t, e.Admin, &got), got.Status.Naming.ExpectedPath)
	require.NotNil(t, got.Status.MediaInfo, "the re-render released the probe result")
}

// TestNamingSurvivesAMissingFile is the release test: the MediaFile reaches
// a real steady state first -- probed and named -- and only then is the file
// removed, so the FileMissing pass has something to release. The movie's
// title changes before that pass too: a missing file keeps the proposal it
// had rather than rendering one for a file that is not there.
func TestNamingSurvivesAMissingFile(t *testing.T) {
	f := newNamedFile(t, "naming-release")
	h := newHarness(t)
	r := h.loop(t, f, false)
	probeAndName(t, h, r, f, fakeMediaInfo())
	steady := f.get(t)
	require.NotEmpty(t, steady.Status.ProbeHash, "setup: never reached a probed steady state")
	require.NotNil(t, steady.Status.Naming, "setup: status.naming never landed")
	require.NotEmpty(t, steady.Status.Naming.ExpectedPath, "setup: status.naming has no path")

	setMovieMetadata(t, f.c, f.key.Namespace, "inception", "Inception Redux", 2010)
	require.NoError(t, os.Remove(f.path))
	res := reconcileOnce(t, r, f.key)
	assert.Equal(t, time.Minute, res.RequeueAfter, "setup: this was not the FileMissing path")

	got := f.get(t) // through the uncached client, so managedFields are the apiserver's own
	ready := k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.MediaFileConditionReady)
	require.NotNil(t, ready)
	assert.Equal(t, "FileMissing", ready.Reason)
	leaves := catalogNamingFields(got.ManagedFields)
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

// containerProbe is a probe with the container and codec the file's own
// extension implies: an .avi is an XviD AVI, an .mkv the transcode's HEVC
// Matroska carrying its CLUSTARR_PROFILE tag -- so a render shows a
// transcode's container change.
func containerProbe(ctx context.Context, path string) (*commonv1.MediaInfo, *mediainfo.Raw, error) {
	mi, raw, err := remediationtest.FakeProbe(ctx, path)
	if err != nil {
		return nil, nil, err
	}
	mi.Container = strings.TrimPrefix(filepath.Ext(path), ".")
	mi.VideoCodec = "mpeg4"
	if mi.Container == "mkv" {
		mi.VideoCodec, mi.TranscodeProfile = "hevc", transcodeTag
	}
	return mi, raw, nil
}

// TestNamingHoldsWhileATranscodeRuns is ruling R18 (spec D6: a rename never
// touches a file another job holds): the transcode planner's block reading
// Running turns the proposal into TranscodePending in the same loop, and
// once the swap -- a container change, .avi to .mkv, the source retired --
// is incorporated and the block ends Succeeded, the proposal renders again
// with the new extension and codec.
func TestNamingHoldsWhileATranscodeRuns(t *testing.T) {
	e := remediationtest.Start(t)
	ctx := context.Background()
	const ns, name = "naming-transcode", "inception-abc1234567"
	mustNamespace(t, ctx, e.Admin, ns)
	mustQualityProfile(t, ctx, e.Admin, "qp-video")
	mustRootFolder(t, e.Admin, ns, "movies", "/data/media/movies", catalogv1alpha1.RootFolderKindMovie, nil)
	mustMovie(t, ctx, e.Admin, ns, "inception", "qp-video")
	setMovieMetadata(t, e.Admin, ns, "inception", "Inception", 2010)
	source := writeFile(t, t.TempDir(), "Inception.2010.720p.WEB-DL.avi", []byte("an avi as imported"))
	importCreates(t, e.Admin, ns, name, commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "inception"}, source, webdl720)
	f := namedFile{e: e, c: e.Admin, key: types.NamespacedName{Namespace: ns, Name: name}, path: source}

	h := newHarness(t)
	h.WithAgent(t, probetest.ProberFunc(containerProbe))
	clk := newClock()
	r := h.loop(t, f, false)
	r.Clock = clk.Now
	remediationtest.Settle(t, h.ProbeHarness, r, f.c, f.key)
	reconcileOnce(t, r, f.key)
	got := f.get(t)
	require.NotNil(t, got.Status.Naming)
	require.True(t, strings.HasSuffix(got.Status.Naming.ExpectedPath, "[XviD].avi"), "setup: %s", got.Status.Naming.ExpectedPath)

	at := metav1.NewTime(clk.Now())
	setStatus(t, f, func(s *catalogv1alpha1.MediaFileStatus) {
		s.Transcode = &catalogv1alpha1.TranscodeState{Phase: catalogv1alpha1.TranscodePhaseRunning, Profile: "hevc-main10",
			ProfileHash: "profile-hash-def", StartedAt: &at}
	})
	clk.Add(31 * time.Second)
	reconcileOnce(t, r, f.key)
	got = f.get(t)
	require.NotNil(t, got.Status.Naming)
	assert.Equal(t, catalogv1alpha1.NamingReasonTranscodePending, got.Status.Naming.Reason, "a running transcode did not hold the rename")
	assert.Empty(t, got.Status.Naming.ExpectedPath, "a held file proposes no path")
	assert.False(t, got.Status.Naming.Current)
	cond := k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.ConditionNamingCurrent)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionUnknown, cond.Status)
	assert.Equal(t, "TranscodePending", cond.Reason)

	// The worker places the output under its new name and retires the
	// source; the block reads Swapping, its result the output.
	output := strings.TrimSuffix(source, ".avi") + ".mkv"
	require.NoError(t, os.WriteFile(output, []byte("a matroska encode, not the same size"), 0o644))
	require.NoError(t, os.Remove(source))
	finished := metav1.NewTime(clk.Now().Add(time.Second))
	setStatus(t, f, func(s *catalogv1alpha1.MediaFileStatus) {
		s.Transcode.Phase, s.Transcode.FinishedAt = catalogv1alpha1.TranscodePhaseSwapping, &finished
		s.Transcode.Result = &catalogv1alpha1.TranscodeOutput{OutputPath: output, OutputSizeBytes: int64(len("a matroska encode, not the same size"))}
	})
	clk.Add(31 * time.Second)
	remediationtest.Settle(t, h.ProbeHarness, r, f.c, f.key)
	require.Equal(t, output, f.get(t).Spec.Path, "setup: the swap was incorporated")
	assert.Equal(t, catalogv1alpha1.NamingReasonTranscodePending, f.get(t).Status.Naming.Reason, "Swapping still holds the rename")
	// The transcode planner ends the block (its planSwap row).
	setStatus(t, f, func(s *catalogv1alpha1.MediaFileStatus) {
		s.Transcode.Phase, s.Transcode.Compliant, s.Transcode.ProfileTag, s.Transcode.Result = catalogv1alpha1.TranscodePhaseSucceeded, true, transcodeTag, nil
	})
	clk.Add(31 * time.Second)
	reconcileOnce(t, r, f.key)
	got = f.get(t)
	require.NotNil(t, got.Status.Naming)
	assert.Empty(t, got.Status.Naming.Reason)
	assert.True(t, strings.HasSuffix(got.Status.Naming.ExpectedPath, "[h265].mkv"),
		"the proposal follows the encode's container and codec: %s", got.Status.Naming.ExpectedPath)
	assert.Equal(t, wantMoviePath(t, f.c, &got), got.Status.Naming.ExpectedPath)
}

// TestNamingHoldsAStaleFileWhoseProbeFailed is ruling R20: once a file's
// bytes have changed, the last good probe no longer describes it, so a
// failed re-probe holds the proposal as ProbePending rather than rendering
// the name the old bytes earned.
func TestNamingHoldsAStaleFileWhoseProbeFailed(t *testing.T) {
	f := newNamedFile(t, "naming-stale")
	h := newHarness(t)
	r := h.loop(t, f, false)
	probeAndName(t, h, r, f, fakeMediaInfo())
	steady := f.get(t)
	require.NotNil(t, steady.Status.Naming)
	require.NotEmpty(t, steady.Status.Naming.ExpectedPath, "setup: never rendered")

	require.NoError(t, os.WriteFile(f.path, []byte("different bytes, unreadable by any prober"), 0o644))
	reconcileOnce(t, r, f.key)
	h.AnswerWith(t, f.uid(t), schema.ProbeRecord{RecordHeader: schema.RecordHeader{State: schema.ProbeFailed, Failure: "Invalid data found when processing input"}})
	reconcileOnce(t, r, f.key) // the failure lands; naming plans on the next pass
	reconcileOnce(t, r, f.key)

	got := f.get(t)
	require.Equal(t, "ProbeFailed", k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.MediaFileConditionProbed).Reason, "setup")
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
	f := newNamedFile(t, "naming-unrenderable")
	h := newHarness(t)
	r := h.loop(t, f, false)
	probeAndName(t, h, r, f, fakeMediaInfo())
	steady := f.get(t)
	require.NotNil(t, steady.Status.Naming)
	require.NotEmpty(t, steady.Status.Naming.ExpectedPath, "setup: never rendered")

	require.NoError(t, f.c.Delete(context.Background(), &catalogv1alpha1.RootFolder{ObjectMeta: metav1.ObjectMeta{Name: "movies", Namespace: f.key.Namespace}}))
	reconcileOnce(t, r, f.key)
	got := f.get(t)
	require.NotNil(t, got.Status.Naming)
	assert.Equal(t, catalogv1alpha1.NamingReasonUnrenderable, got.Status.Naming.Reason)
	assert.Empty(t, got.Status.Naming.ExpectedPath, "the previous expectedPath was not cleared")
	assert.False(t, got.Status.Naming.Current)
	assert.NotContains(t, catalogNamingFields(got.ManagedFields), "f:expectedPath", "catalog still claims a cleared expectedPath")
	cond := k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.ConditionNamingCurrent)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionUnknown, cond.Status)
	assert.Equal(t, "Unrenderable", cond.Reason)
}

// blipReader fails every RootFolder read with an internal error: a cache
// blip, not a NotFound.
type blipReader struct{ client.Reader }

func (b blipReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if _, ok := obj.(*catalogv1alpha1.RootFolder); ok {
		return apierrors.NewInternalError(errors.New("cache blip"))
	}
	return b.Reader.Get(ctx, key, obj, opts...)
}

// TestNamingKeepsItsProposalOverALookupFailure: a lookup that fails for any
// reason but NotFound -- a cache blip -- keeps the proposal already on the
// object rather than withdrawing or releasing it, and asks for a pass in
// 30 s so the render is retried: nothing else would wake the file for it.
func TestNamingKeepsItsProposalOverALookupFailure(t *testing.T) {
	f := newNamedFile(t, "naming-blip")
	h := newHarness(t)
	r := h.loop(t, f, false)
	probeAndName(t, h, r, f, fakeMediaInfo())
	res := reconcileOnce(t, r, f.key)
	require.Zero(t, res.RequeueAfter, "setup: a healthy render does not requeue")
	steady := f.get(t)
	require.NotNil(t, steady.Status.Naming)
	require.NotEmpty(t, steady.Status.Naming.ExpectedPath, "setup: never rendered")

	// The movie's title moves, so a render that went ahead would change the
	// proposal; the RootFolder read fails, so none can.
	setMovieMetadata(t, f.c, f.key.Namespace, "inception", "Inception Redux", 2010)
	res = reconcileOnce(t, h.loopReading(t, f, blipReader{f.e.Loop.Reader}), f.key)
	assert.Equal(t, 30*time.Second, res.RequeueAfter, "a render kept over a failed lookup must be retried")

	got := f.get(t)
	assert.Equal(t, steady.Status.Naming, got.Status.Naming, "the proposal was not kept over the failed lookup")
	assert.Contains(t, catalogNamingFields(got.ManagedFields), "f:expectedPath")
	assert.Nil(t, k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.MediaFileConditionNamingPlannerError),
		"a failed lookup is no planner error")
}
