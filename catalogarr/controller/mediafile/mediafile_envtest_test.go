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
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	subtitleac "github.com/mediactl/clustarr/api/applyconfiguration/subtitle/subtitle/v1alpha1"
	transcodeac "github.com/mediactl/clustarr/api/applyconfiguration/transcode/transcode/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/controller/mediafile"
	"github.com/mediactl/clustarr/importarr/worker/rescan"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/mediainfo"
)

func startEnv(t *testing.T) (client.Client, *rest.Config) {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	env := &envtest.Environment{CRDDirectoryPaths: []string{"../../../config/crd/bases"}, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	if err != nil {
		t.Fatalf("start envtest: %v", err)
	}
	t.Cleanup(func() { _ = env.Stop() })
	c, err := client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
	if err != nil {
		t.Fatalf("build client: %v", err)
	}
	return c, cfg
}

func mustNamespace(t *testing.T, ctx context.Context, c client.Client, ns string) {
	t.Helper()
	if err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}); err != nil && client.IgnoreAlreadyExists(err) != nil {
		t.Fatalf("create namespace %s: %v", ns, err)
	}
}

// importarrCreatesMediaFile stands in for importarr's rescan worker, which
// this package cannot run (it needs a bus, a LibraryScan, a RootFolder and a
// real tree on disk): it Applies the full MediaFileSpec, exactly the fields
// "Resolving the field-manager split" assigns importarr, none of the three
// catalogarr may later take over.
//
// It applies under rescan.FieldManager, the constant importarr's worker
// itself writes with, rather than restating a k8s.Manager* name here. Task
// C14 found this gate proving the split for a manager name production never
// used -- production wrote as k8s.ManagerImportarr while this file, and
// k8s.ManagerImportarrWorker's own doc comment, said importarr-worker -- so
// the one thing the gate exists to prove was being proved about a fiction.
// Naming production's own constant makes that drift impossible rather than
// merely fixed.
func importarrCreatesMediaFile(t *testing.T, ctx context.Context, c client.Client, ns, name, path string, size int64, modTime time.Time, q commonv1.Quality) {
	t.Helper()
	importarrCreatesMediaFileFor(t, ctx, c, ns, name,
		commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "inception"}, path, size, modTime, q)
}

// importarrCreatesMediaFileFor is importarrCreatesMediaFile generalised to an
// arbitrary owner: Step 10's fixture-driven rollup test needs more than one
// Movie and an Episode, none of them named "inception".
func importarrCreatesMediaFileFor(t *testing.T, ctx context.Context, c client.Client, ns, name string, mediaRef commonv1.MediaRef, path string, size int64, modTime time.Time, q commonv1.Quality) {
	t.Helper()
	ac := catalogac.MediaFile(name, ns).WithSpec(
		catalogac.MediaFileSpec().
			WithMediaRef(mediaRef).
			WithPath(path).
			WithSizeBytes(size).
			WithModTime(metav1.NewTime(modTime)).
			WithQuality(q).
			WithRevision(commonv1.Revision{Version: 1}).
			WithReleaseType(commonv1.ReleaseTypeSingle).
			WithFormatScore(40).
			WithProfileHash("profile-hash-abc").
			WithOriginal(true),
	)
	if _, err := k8s.Apply(ctx, c, rescan.FieldManager, ac); err != nil {
		t.Fatalf("simulate importarr create: %v", err)
	}
}

func mustQualityProfile(t *testing.T, ctx context.Context, c client.Client, name string) {
	t.Helper()
	qp := &catalogv1alpha1.QualityProfile{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: catalogv1alpha1.QualityProfileSpec{
			MediaKind: catalogv1alpha1.ProfileMediaKindVideo,
			Tiers: []catalogv1alpha1.Tier{
				{Name: "Bluray-1080p", Qualities: []string{"Bluray-1080p"}},
				{Name: "WEB 1080p", Qualities: []string{"WEBDL-1080p"}},
			},
			Cutoff: "Bluray-1080p",
		},
	}
	if err := c.Create(ctx, qp); err != nil && client.IgnoreAlreadyExists(err) != nil {
		t.Fatalf("create QualityProfile: %v", err)
	}
}

func mustMovie(t *testing.T, ctx context.Context, c client.Client, ns, name, qualityProfile string) {
	t.Helper()
	m := &catalogv1alpha1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: catalogv1alpha1.MovieSpec{
			TmdbID:            27205,
			QualityProfileRef: qualityProfile,
			RootFolderRef:     "movies",
		},
	}
	if err := c.Create(ctx, m); err != nil && client.IgnoreAlreadyExists(err) != nil {
		t.Fatalf("create Movie: %v", err)
	}
}

func mustSeries(t *testing.T, ctx context.Context, c client.Client, ns, name, qualityProfile string) {
	t.Helper()
	s := &catalogv1alpha1.Series{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: catalogv1alpha1.SeriesSpec{
			TvdbID:            153021,
			QualityProfileRef: qualityProfile,
			RootFolderRef:     "tv",
		},
	}
	if err := c.Create(ctx, s); err != nil && client.IgnoreAlreadyExists(err) != nil {
		t.Fatalf("create Series: %v", err)
	}
}

func mustEpisode(t *testing.T, ctx context.Context, c client.Client, ns, name, seriesRef string, season, episode int32) {
	t.Helper()
	ep := &catalogv1alpha1.Episode{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: catalogv1alpha1.EpisodeSpec{
			SeriesRef:     seriesRef,
			SeasonNumber:  season,
			EpisodeNumber: episode,
		},
	}
	if err := c.Create(ctx, ep); err != nil && client.IgnoreAlreadyExists(err) != nil {
		t.Fatalf("create Episode: %v", err)
	}
}

// mustTranscodeProfile creates the cluster-scoped TranscodeProfile a
// TranscodeJob fixture's spec.profileRef names, with status.hash set the
// same way squasharr's own controller would -- transcodeProfileTag's
// "CLUSTARR_PROFILE=<name>@<hash>" render needs the object to exist and have
// a hash, the same as it would in a real cluster where squasharr always
// creates the TranscodeProfile before any TranscodeJob can reference it.
func mustTranscodeProfile(t *testing.T, ctx context.Context, c client.Client, name, hash string) {
	t.Helper()
	tp := &transcodev1alpha1.TranscodeProfile{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if err := c.Create(ctx, tp); err != nil && client.IgnoreAlreadyExists(err) != nil {
		t.Fatalf("create TranscodeProfile: %v", err)
	}
	if _, err := k8s.PatchStatus(ctx, c, k8s.ManagerSquasharr,
		transcodeac.TranscodeProfile(name).WithStatus(transcodeac.TranscodeProfileStatus().WithHash(hash)),
	); err != nil {
		t.Fatalf("set TranscodeProfile status: %v", err)
	}
}

func writeFile(t *testing.T, dir, name string, contents []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, contents, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// TestMediaFileFieldManagersStayDisjoint is the mandatory two-writer gate:
// importarr's simulated create Applies MediaFileSpec, catalogarr's Reconcile
// writes MediaFileStatus, and after a simulated transcode swap catalogarr also
// claims exactly spec.sizeBytes/modTime/original -- proven by reading
// managedFields directly, not by inference.
func TestMediaFileFieldManagersStayDisjoint(t *testing.T) {
	c, _ := startEnv(t)
	ctx := t.Context()
	const ns, name = "twowriter", "inception-abc1234567"
	dir := t.TempDir()
	mustNamespace(t, ctx, c, ns)
	mustQualityProfile(t, ctx, c, "hd-bluray-web-test")
	mustMovie(t, ctx, c, ns, "inception", "hd-bluray-web-test")

	path := writeFile(t, dir, "Inception (2010).mkv", []byte("stand-in bytes"))
	stat, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	importarrCreatesMediaFile(t, ctx, c, ns, name, path, stat.Size(), stat.ModTime(),
		commonv1.Quality{Name: "WEBDL-1080p", Source: commonv1.SourceWebDL, Resolution: commonv1.Resolution1080p, Modifier: commonv1.ModifierNone})

	r := &mediafile.Reconciler{Client: c, Probe: fakeProbe, Clock: time.Now}
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: name}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	var got catalogv1alpha1.MediaFile
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got); err != nil {
		t.Fatalf("get: %v", err)
	}

	// importarr's fields survived catalogarr's writes.
	assert.Equal(t, "WEBDL-1080p", got.Spec.Quality.Name)
	assert.Equal(t, path, got.Spec.Path)
	assert.Equal(t, "profile-hash-abc", got.Spec.ProfileHash)

	// catalogarr produced status without ever claiming a spec field
	// importarr owns (no transcode happened yet, so it claims none).
	require.NotEmpty(t, got.Status.ProbeHash)
	require.NotNil(t, got.Status.MediaInfo)

	// A plain, untranscoded MediaFile still gets the full mirrored label
	// set: spec §8.4 says the MediaFile reconciler "probes, sets labels,
	// probeHash, Probed" unconditionally, and metadata.labels is neither
	// spec nor status -- disjoint from everything importarr owns, so there
	// is no two-writer reason to withhold it pre-transcode.
	assert.Equal(t, "movie", got.Labels[catalogv1alpha1.LabelKind])
	assert.Equal(t, "1080", got.Labels[catalogv1alpha1.LabelResolution])
	assert.Equal(t, "webdl", got.Labels[catalogv1alpha1.LabelSource])
	assert.Equal(t, "none", got.Labels[catalogv1alpha1.LabelModifier])
	assert.Equal(t, "h264", got.Labels[catalogv1alpha1.LabelVideoCodec])
	assert.Equal(t, "true", got.Labels[catalogv1alpha1.LabelOriginal])

	for _, want := range []struct{ manager, subresource string }{
		{rescan.FieldManager.String(), ""},
		{"catalogarr", "status"},
	} {
		if !managesField(got.ManagedFields, want.manager, want.subresource) {
			t.Errorf("no %q/%q entry in managedFields: %+v", want.manager, want.subresource, fieldManagerNames(got.ManagedFields))
		}
	}

	// The precise check the invariant calls for: catalogarr claims
	// metadata.labels on the main resource, but none of importarr's
	// spec.* fields, before any transcode has happened.
	catalogarrMain := managedFieldPaths(got.ManagedFields, "catalogarr", "")
	require.NotNil(t, catalogarrMain, "catalogarr should own metadata.labels on the main resource: %+v", fieldManagerNames(got.ManagedFields))
	assert.True(t, claimsLabels(catalogarrMain), "catalogarr should claim metadata.labels")
	assert.False(t, claimsSpecField(catalogarrMain), "catalogarr must not claim any spec field before a transcode: %+v", specFieldNames(catalogarrMain))

	importarrMain := managedFieldPaths(got.ManagedFields, rescan.FieldManager.String(), "")
	require.NotNil(t, importarrMain)
	assert.True(t, specFieldNames(importarrMain)["quality"], "the rescan worker should still own spec.quality")
	assert.True(t, specFieldNames(importarrMain)["path"], "the rescan worker should still own spec.path")

	// Now simulate squasharr: a Succeeded TranscodeJob at the same path.
	newContents := []byte("re-encoded stand-in bytes, longer")
	if err := os.WriteFile(path, newContents, 0o644); err != nil {
		t.Fatal(err)
	}
	newStat, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	finished := metav1.NewTime(newStat.ModTime().Add(time.Second))
	mustTranscodeProfile(t, ctx, c, "hevc-main10", "profile-hash-def")
	tj := &transcodev1alpha1.TranscodeJob{
		ObjectMeta: metav1.ObjectMeta{Name: name + "-abcd1234", Namespace: ns},
		Spec:       transcodev1alpha1.TranscodeJobSpec{MediaFileRef: name, ProfileRef: "hevc-main10", SourcePath: path, SourceProbeHash: got.Status.ProbeHash},
	}
	if err := c.Create(ctx, tj); err != nil {
		t.Fatalf("create TranscodeJob: %v", err)
	}
	// A hand-built fixture standing in for squasharr's worker; PatchStatus
	// (not a direct Status().Update -- forbidigo has no test-file exemption
	// in .golangci.yml) is still the only status-write path this test uses.
	if _, err := k8s.PatchStatus(ctx, c, k8s.ManagerSquasharr,
		transcodeac.TranscodeJob(tj.Name, tj.Namespace).WithStatus(
			transcodeac.TranscodeJobStatus().
				WithPhase(transcodev1alpha1.TranscodeJobPhaseSucceeded).
				WithFinishedAt(finished).
				WithResult(transcodeac.Result().WithOutputPath(path).WithOutputSizeBytes(int64(len(newContents)))),
		)); err != nil {
		t.Fatalf("set TranscodeJob status: %v", err)
	}

	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: name}}); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}

	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	assert.Equal(t, int64(len(newContents)), got.Spec.SizeBytes)
	assert.False(t, got.Spec.Original != nil && *got.Spec.Original)
	// Quality/revision/formatScore/matchedFormats/releaseType are untouched --
	// the freeze this task's whole design exists to prove.
	assert.Equal(t, "WEBDL-1080p", got.Spec.Quality.Name)
	assert.Equal(t, int32(40), got.Spec.FormatScore)

	for _, want := range []struct{ manager, subresource string }{
		{rescan.FieldManager.String(), ""}, // still owns path/quality/... (untouched fields)
		{"catalogarr", ""},                 // now owns sizeBytes/modTime/original (and still labels)
		{"catalogarr", "status"},
	} {
		if !managesField(got.ManagedFields, want.manager, want.subresource) {
			t.Errorf("no %q/%q entry in managedFields after transcode: %+v", want.manager, want.subresource, fieldManagerNames(got.ManagedFields))
		}
	}

	// catalogarr's spec claim is now EXACTLY sizeBytes/modTime/original --
	// never path, never any of the frozen release-time fields -- and it
	// still claims labels. The rescan worker's spec claim no longer includes
	// the three fields it just transferred, but still includes quality/path
	// untouched: a clean ownership transfer, not a conflict.
	catalogarrMain = managedFieldPaths(got.ManagedFields, "catalogarr", "")
	require.NotNil(t, catalogarrMain)
	assert.True(t, claimsLabels(catalogarrMain), "catalogarr should still claim metadata.labels after a transcode")
	assert.Equal(t, map[string]bool{"sizeBytes": true, "modTime": true, "original": true}, specFieldNames(catalogarrMain))

	importarrMain = managedFieldPaths(got.ManagedFields, rescan.FieldManager.String(), "")
	require.NotNil(t, importarrMain)
	importarrSpec := specFieldNames(importarrMain)
	assert.True(t, importarrSpec["quality"], "the rescan worker should still own spec.quality")
	assert.True(t, importarrSpec["path"], "the rescan worker should still own spec.path")
	assert.False(t, importarrSpec["sizeBytes"], "the rescan worker should have released sizeBytes to catalogarr")
	assert.False(t, importarrSpec["modTime"], "the rescan worker should have released modTime to catalogarr")
	assert.False(t, importarrSpec["original"], "the rescan worker should have released original to catalogarr")

	require.NotNil(t, got.Status.Transcode)
	assert.True(t, got.Status.Transcode.Compliant)
}

func fakeProbe(_ context.Context, path string) (*commonv1.MediaInfo, *mediainfo.Raw, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, nil, err // same failure shape as the real mediainfo.Probe on a missing file
	}
	return &commonv1.MediaInfo{
		Container:     "mkv",
		VideoCodec:    "h264",
		Width:         1920,
		Height:        1080,
		RuntimeMillis: 1000,
		Audio:         []commonv1.AudioStream{{Codec: "aac", Channels: 2, ChannelLayout: "stereo"}},
	}, nil, nil
}

func managesField(entries []metav1.ManagedFieldsEntry, manager, subresource string) bool {
	for _, e := range entries {
		if e.Manager == manager && e.Subresource == subresource {
			return true
		}
	}
	return false
}

func fieldManagerNames(entries []metav1.ManagedFieldsEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Manager+"/"+e.Subresource)
	}
	return out
}

// managedFieldPaths decodes the (manager, subresource) entry's FieldsV1 --
// server-side apply's per-field-path ownership record -- into its top-level
// field-path set (e.g. "f:metadata", "f:spec"). It returns nil when no such
// entry exists, so the coordinator's ruling can be checked precisely:
// "claims nothing on the main resource" is too broad a thing to assert
// (metadata.labels is neither spec nor status, and is disjoint from
// everything importarr owns), but "claims none of importarr's spec.*
// fields" is exactly what FieldsV1's "f:spec" sub-map answers.
func managedFieldPaths(entries []metav1.ManagedFieldsEntry, manager, subresource string) map[string]any {
	for _, e := range entries {
		if e.Manager == manager && e.Subresource == subresource && e.FieldsV1 != nil {
			var m map[string]any
			if err := json.Unmarshal(e.FieldsV1.GetRawBytes(), &m); err == nil {
				return m
			}
		}
	}
	return nil
}

// claimsSpecField reports whether a managedFieldPaths result includes any
// spec.* field at all.
func claimsSpecField(fields map[string]any) bool {
	v, ok := fields["f:spec"]
	if !ok {
		return false
	}
	m, ok := v.(map[string]any)
	return ok && len(m) > 0
}

// claimsLabels reports whether a managedFieldPaths result includes
// metadata.labels.
func claimsLabels(fields map[string]any) bool {
	meta, ok := fields["f:metadata"].(map[string]any)
	if !ok {
		return false
	}
	_, ok = meta["f:labels"]
	return ok
}

// specFieldNames returns the bare (un-prefixed, non-".") field names a
// managedFieldPaths result's "f:spec" sub-map claims, e.g. {"sizeBytes",
// "modTime", "original"} -- the exact set this task's central finding says
// catalogarr may ever claim on MediaFileSpec, and no more.
func specFieldNames(fields map[string]any) map[string]bool {
	out := map[string]bool{}
	spec, ok := fields["f:spec"].(map[string]any)
	if !ok {
		return out
	}
	for k := range spec {
		if k == "." {
			continue
		}
		out[strings.TrimPrefix(k, "f:")] = true
	}
	return out
}

// The owning Movie/Episode rollup this controller used to perform lived
// here as TestReconcileFixtureDrivenMovieRollup and
// TestReconcileFixtureDrivenEpisodeRollup. Task C13 deleted the rollup
// itself (see the Reconciler doc comment), so those two tests went with it:
// the behaviour they covered is the Movie and Episode reconcilers' own, and
// is proven there by each package's "MediaFile watch rolls up HasFile and
// reaches Imported or CutoffUnmet" subtest, driven through a real manager
// and a real watch rather than a direct MediaFile reconcile. What replaces
// them here is TestMediaFileReconcileDoesNotReleaseOwnerStatus in
// ownerstatus_envtest_test.go, which asserts the opposite: that a MediaFile
// reconcile leaves the owning item's status entirely alone.

// skipIfNoFFprobe mirrors pkg/mediainfo/probe_test.go's own unexported
// helper of the same name -- that one lives in a different package and is
// not importable, so this task's real-ffprobe test carries its own copy.
func skipIfNoFFprobe(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not on PATH")
	}
}

// TestReconcileRealFFprobe is the one place this task's fake probe and the
// real mediainfo.Probe connect end to end: NewReconciler's default Probe
// (unlike every other test in this file, which overrides it with fakeProbe)
// runs the actual ffprobe binary against a Phase B fixture. It skips
// cleanly, naming ffprobe, when the binary is not on PATH; every other
// reconcile behaviour this task cares about already has an ffprobe-free
// fixture-driven test (TestMediaFileFieldManagersStayDisjoint,
// TestReconcileFixtureDrivenMovieRollup/EpisodeRollup).
func TestReconcileRealFFprobe(t *testing.T) {
	skipIfNoFFprobe(t)
	c, _ := startEnv(t)
	ctx := t.Context()
	const ns, name = "realffprobe", "sample-abc1234567"
	dir := t.TempDir()
	mustNamespace(t, ctx, c, ns)
	mustQualityProfile(t, ctx, c, "qp-video")
	mustMovie(t, ctx, c, ns, "inception", "qp-video")

	src, err := os.ReadFile("../../../testdata/mediainfo/sample_h264_8bit.mp4")
	require.NoError(t, err)
	path := writeFile(t, dir, "sample.mp4", src)
	stat, err := os.Stat(path)
	require.NoError(t, err)

	importarrCreatesMediaFileFor(t, ctx, c, ns, name,
		commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "inception"},
		path, stat.Size(), stat.ModTime(),
		commonv1.Quality{Name: "Bluray-1080p", Source: commonv1.SourceBluray, Resolution: commonv1.Resolution1080p, Modifier: commonv1.ModifierNone})

	r := mediafile.NewReconciler(c, k8s.MustNewScheme(), nil)
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: name}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	var got catalogv1alpha1.MediaFile
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got))
	require.NotNil(t, got.Status.MediaInfo)
	assert.Equal(t, "h264", got.Status.MediaInfo.VideoCodec)
	require.NotEmpty(t, got.Status.MediaInfo.Audio)
	assert.NotEmpty(t, got.Status.MediaInfo.Audio[0].ChannelLayout)
}

// TestTranscodeJobWatchTriggersReconcile drives a real ctrl.Manager, proving
// the Watches(&TranscodeJob{}, ...) wiring in SetupWithManager -- not a
// direct Reconcile call -- actually enqueues a request when a hand-created
// TranscodeJob reaches phase Succeeded.
func TestTranscodeJobWatchTriggersReconcile(t *testing.T) {
	_, cfg := startEnv(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 k8s.MustNewScheme(),
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
		// SetupWithManager's Named("mediafile") is checked against a
		// process-wide, never-cleared registry (controller-runtime's
		// checkName, pkg/controller/name.go) -- correct for one real
		// cluster process, but this file starts a fresh manager per test
		// and two manager-driven tests in the same `go test` binary would
		// otherwise collide on the second SetupWithManager call regardless
		// of the first manager having been stopped.
		Controller: config.Controller{SkipNameValidation: ptr.To(true)},
	})
	require.NoError(t, err)

	r := mediafile.NewReconciler(mgr.GetClient(), mgr.GetScheme(), record.NewFakeRecorder(32))
	r.Probe = fakeProbe
	require.NoError(t, r.SetupWithManager(mgr))

	go func() { _ = mgr.Start(ctx) }()
	require.True(t, mgr.GetCache().WaitForCacheSync(ctx))
	c := mgr.GetClient()

	const ns, name = "tjwatch", "inception-abc1234567"
	dir := t.TempDir()
	mustNamespace(t, ctx, c, ns)
	mustQualityProfile(t, ctx, c, "qp-video")
	mustMovie(t, ctx, c, ns, "inception", "qp-video")

	path := writeFile(t, dir, "Inception (2010).mkv", []byte("stand-in bytes"))
	stat, err := os.Stat(path)
	require.NoError(t, err)
	importarrCreatesMediaFileFor(t, ctx, c, ns, name,
		commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "inception"},
		path, stat.Size(), stat.ModTime(),
		commonv1.Quality{Name: "WEBDL-1080p", Source: commonv1.SourceWebDL, Resolution: commonv1.Resolution1080p, Modifier: commonv1.ModifierNone})

	var got catalogv1alpha1.MediaFile
	require.Eventually(t, func() bool {
		if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got); err != nil {
			return false
		}
		return got.Status.ProbeHash != ""
	}, 5*time.Second, 100*time.Millisecond, "initial For(MediaFile) reconcile did not land")

	newContents := []byte("re-encoded stand-in bytes, longer")
	require.NoError(t, os.WriteFile(path, newContents, 0o644))
	newStat, err := os.Stat(path)
	require.NoError(t, err)
	finished := metav1.NewTime(newStat.ModTime().Add(time.Second))

	mustTranscodeProfile(t, ctx, c, "hevc-main10", "profile-hash-def")
	tj := &transcodev1alpha1.TranscodeJob{
		ObjectMeta: metav1.ObjectMeta{Name: name + "-abcd1234", Namespace: ns},
		Spec:       transcodev1alpha1.TranscodeJobSpec{MediaFileRef: name, ProfileRef: "hevc-main10", SourcePath: path, SourceProbeHash: got.Status.ProbeHash},
	}
	require.NoError(t, c.Create(ctx, tj))
	_, err = k8s.PatchStatus(ctx, c, k8s.ManagerSquasharr,
		transcodeac.TranscodeJob(tj.Name, tj.Namespace).WithStatus(
			transcodeac.TranscodeJobStatus().
				WithPhase(transcodev1alpha1.TranscodeJobPhaseSucceeded).
				WithFinishedAt(finished).
				WithResult(transcodeac.Result().WithOutputPath(path).WithOutputSizeBytes(int64(len(newContents)))),
		))
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got); err != nil {
			return false
		}
		return got.Spec.Original != nil && !*got.Spec.Original &&
			got.Status.Transcode != nil && got.Status.Transcode.Compliant
	}, 5*time.Second, 100*time.Millisecond, "the TranscodeJob watch did not trigger the swap")
}

// TestSubtitleRequestWatchTriggersReconcile is
// TestTranscodeJobWatchTriggersReconcile's SubtitleRequest counterpart: the
// "status.items changed" predicate (k8s.StatusFieldChanged over
// extractSubtitleItemsSignature) and mediaFileForSubtitleRequest driving a
// real manager, not a direct Reconcile call -- this is §8.6's sidecar
// feedback path end to end.
//
// It also carries the lesson from task C14. This test drove a correct
// steady state and then asserted only that ITS field had arrived, and so it
// passed, for the whole of Phase C, while watching status.probeHash,
// status.probedAt, status.mediaInfo and status.transcode be released by a
// second status apply made under the same field manager in the same
// reconcile. The three lines below the Eventually are the fix, and the rule
// they stand for is: assert that the REST of the object survived, not just
// that your field arrived.
func TestSubtitleRequestWatchTriggersReconcile(t *testing.T) {
	_, cfg := startEnv(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 k8s.MustNewScheme(),
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
		// SetupWithManager's Named("mediafile") is checked against a
		// process-wide, never-cleared registry (controller-runtime's
		// checkName, pkg/controller/name.go) -- correct for one real
		// cluster process, but this file starts a fresh manager per test
		// and two manager-driven tests in the same `go test` binary would
		// otherwise collide on the second SetupWithManager call regardless
		// of the first manager having been stopped.
		Controller: config.Controller{SkipNameValidation: ptr.To(true)},
	})
	require.NoError(t, err)

	r := mediafile.NewReconciler(mgr.GetClient(), mgr.GetScheme(), record.NewFakeRecorder(32))
	r.Probe = fakeProbe
	require.NoError(t, r.SetupWithManager(mgr))

	go func() { _ = mgr.Start(ctx) }()
	require.True(t, mgr.GetCache().WaitForCacheSync(ctx))
	c := mgr.GetClient()

	const ns, name = "srwatch", "inception-abc1234567"
	dir := t.TempDir()
	mustNamespace(t, ctx, c, ns)
	mustQualityProfile(t, ctx, c, "qp-video")
	mustMovie(t, ctx, c, ns, "inception", "qp-video")

	path := writeFile(t, dir, "Inception (2010).mkv", []byte("stand-in bytes"))
	stat, err := os.Stat(path)
	require.NoError(t, err)
	importarrCreatesMediaFileFor(t, ctx, c, ns, name,
		commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "inception"},
		path, stat.Size(), stat.ModTime(),
		commonv1.Quality{Name: "WEBDL-1080p", Source: commonv1.SourceWebDL, Resolution: commonv1.Resolution1080p, Modifier: commonv1.ModifierNone})

	var got catalogv1alpha1.MediaFile
	require.Eventually(t, func() bool {
		if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got); err != nil {
			return false
		}
		return got.Status.ProbeHash != ""
	}, 5*time.Second, 100*time.Millisecond, "initial For(MediaFile) reconcile did not land")

	sr := &subtitlev1alpha1.SubtitleRequest{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       subtitlev1alpha1.SubtitleRequestSpec{MediaFileRef: name},
	}
	require.NoError(t, c.Create(ctx, sr))
	_, err = k8s.PatchStatus(ctx, c, k8s.ManagerCaptionarrWorker,
		subtitleac.SubtitleRequest(sr.Name, sr.Namespace).WithStatus(
			subtitleac.SubtitleRequestStatus().WithItems(
				subtitleac.SubtitleItem().WithLangKey("en").WithState(subtitlev1alpha1.SubtitleItemDownloaded).WithPath("Inception (2010).en.srt"),
			),
		))
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &got); err != nil {
			return false
		}
		return len(got.Status.Sidecars) == 1 && got.Status.Sidecars[0].Language == "en"
	}, 5*time.Second, 100*time.Millisecond, "the SubtitleRequest watch did not trigger the sidecar refresh")

	require.NotEmpty(t, got.Status.ProbeHash,
		"folding in a sidecar released the probe result: status.sidecars must be part of the SAME apply as the probe, not a second one under the same manager")
	require.NotNil(t, got.Status.MediaInfo, "folding in a sidecar released status.mediaInfo")
	require.NotEmpty(t, got.Status.Conditions, "folding in a sidecar released the conditions")
}

// TestTransientFileFailuresPreserveProbedStatus is the case this file did
// not have: neither the FileMissing branch nor the ProbeFailed branch had
// any test at all, and both of them applied a MediaFileStatus containing
// only observedGeneration and conditions under k8s.ManagerCatalogarr -- the
// sole owner of ALL of MediaFileStatus. Server-side apply replaces a
// manager's ownership set on every apply rather than merging it, so each of
// those applies released status.probeHash, status.probedAt,
// status.mediaInfo, status.sidecars and status.transcode. Measured before
// the fix, over one os.Remove and one reconcile:
//
//	BEFORE: probeHash="3580b9bd..." mediaInfo=true  probedAt=true
//	AFTER : probeHash=""            mediaInfo=false probedAt=false
//
// Both triggers are transient and routine -- an RWX /data blip, a user
// moving a file and moving it back, an ffprobe that times out under load --
// and the requeue is 60s, so a healthy file spends the whole outage gutted.
// status.transcode.compliant reading false in particular makes squasharr
// re-transcode an already-compliant file once Phase E lands.
//
// The shape matters as much as the assertions: this drives the MediaFile to
// a REAL steady state first -- probed, with sidecars and an incorporated
// transcode -- and only then breaks the file. A test that creates a blank
// object and triggers the failure path cannot observe a release, because
// there was nothing to release; that is exactly why the branches looked
// covered enough to ship.
func TestTransientFileFailuresPreserveProbedStatus(t *testing.T) {
	c, _ := startEnv(t)
	ctx := t.Context()
	const ns, name = "transient", "inception-abc1234567"
	key := types.NamespacedName{Namespace: ns, Name: name}
	dir := t.TempDir()
	mustNamespace(t, ctx, c, ns)
	mustQualityProfile(t, ctx, c, "qp-video")
	mustMovie(t, ctx, c, ns, "inception", "qp-video")

	path := writeFile(t, dir, "Inception (2010).mkv", []byte("stand-in bytes"))
	stat, err := os.Stat(path)
	require.NoError(t, err)
	importarrCreatesMediaFile(t, ctx, c, ns, name, path, stat.Size(), stat.ModTime(),
		commonv1.Quality{Name: "WEBDL-1080p", Source: commonv1.SourceWebDL, Resolution: commonv1.Resolution1080p, Modifier: commonv1.ModifierNone})

	r := &mediafile.Reconciler{Client: c, Probe: fakeProbe, Clock: time.Now}
	_, err = r.Reconcile(ctx, ctrl.Request{NamespacedName: key})
	require.NoError(t, err)

	// A SubtitleRequest, so status.sidecars is populated too. After
	// captionarr lands every video MediaFile has one, which is what makes
	// a released status.sidecars the common case rather than the exotic one.
	sr := &subtitlev1alpha1.SubtitleRequest{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       subtitlev1alpha1.SubtitleRequestSpec{MediaFileRef: name},
	}
	require.NoError(t, c.Create(ctx, sr))
	_, err = k8s.PatchStatus(ctx, c, k8s.ManagerCaptionarrWorker,
		subtitleac.SubtitleRequest(sr.Name, sr.Namespace).WithStatus(
			subtitleac.SubtitleRequestStatus().WithItems(
				subtitleac.SubtitleItem().WithLangKey("en").WithState(subtitlev1alpha1.SubtitleItemDownloaded).WithPath("Inception (2010).en.srt"),
			),
		))
	require.NoError(t, err)

	// ... and an incorporated transcode swap, so status.transcode is set.
	newContents := []byte("re-encoded stand-in bytes, longer")
	require.NoError(t, os.WriteFile(path, newContents, 0o644))
	newStat, err := os.Stat(path)
	require.NoError(t, err)
	mustTranscodeProfile(t, ctx, c, "hevc-main10", "profile-hash-def")
	tj := &transcodev1alpha1.TranscodeJob{
		ObjectMeta: metav1.ObjectMeta{Name: name + "-abcd1234", Namespace: ns},
		Spec:       transcodev1alpha1.TranscodeJobSpec{MediaFileRef: name, ProfileRef: "hevc-main10", SourcePath: path},
	}
	require.NoError(t, c.Create(ctx, tj))
	_, err = k8s.PatchStatus(ctx, c, k8s.ManagerSquasharr,
		transcodeac.TranscodeJob(tj.Name, tj.Namespace).WithStatus(
			transcodeac.TranscodeJobStatus().
				WithPhase(transcodev1alpha1.TranscodeJobPhaseSucceeded).
				WithFinishedAt(metav1.NewTime(newStat.ModTime().Add(time.Second))).
				WithResult(transcodeac.Result().WithOutputPath(path).WithOutputSizeBytes(int64(len(newContents)))),
		))
	require.NoError(t, err)

	_, err = r.Reconcile(ctx, ctrl.Request{NamespacedName: key})
	require.NoError(t, err)

	var steady catalogv1alpha1.MediaFile
	require.NoError(t, c.Get(ctx, key, &steady))
	require.NotEmpty(t, steady.Status.ProbeHash, "setup: never reached a probed steady state")
	require.NotNil(t, steady.Status.ProbedAt)
	require.NotNil(t, steady.Status.MediaInfo)
	require.Len(t, steady.Status.Sidecars, 1, "setup: the sidecar never landed")
	require.NotNil(t, steady.Status.Transcode, "setup: the transcode swap was never incorporated")
	require.True(t, steady.Status.Transcode.Compliant)

	// assertSteadyStateSurvived is the whole point: every field
	// ManagerCatalogarr owns and this pass did not recompute must still be
	// exactly what it was.
	assertSteadyStateSurvived := func(t *testing.T) {
		t.Helper()
		var got catalogv1alpha1.MediaFile
		require.NoError(t, c.Get(ctx, key, &got))
		assert.Equal(t, steady.Status.ProbeHash, got.Status.ProbeHash, "status.probeHash was released")
		assert.Equal(t, steady.Status.ProbedAt, got.Status.ProbedAt, "status.probedAt was released")
		assert.Equal(t, steady.Status.MediaInfo, got.Status.MediaInfo, "status.mediaInfo was released")
		assert.Equal(t, steady.Status.Sidecars, got.Status.Sidecars, "status.sidecars was released")
		require.NotNil(t, got.Status.Transcode, "status.transcode was released; squasharr would re-transcode a compliant file")
		assert.True(t, got.Status.Transcode.Compliant, "status.transcode.compliant was reset to false")
		assert.Equal(t, steady.Status.Transcode.ProfileTag, got.Status.Transcode.ProfileTag)
	}

	t.Run("a missing file does not release the probe result", func(t *testing.T) {
		require.NoError(t, os.Remove(path))
		t.Cleanup(func() { require.NoError(t, os.WriteFile(path, newContents, 0o644)) })

		res, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: key})
		require.NoError(t, err)
		assert.Equal(t, time.Minute, res.RequeueAfter, "the FileMissing branch should requeue, not give up")

		assertSteadyStateSurvived(t)

		var got catalogv1alpha1.MediaFile
		require.NoError(t, c.Get(ctx, key, &got))
		ready := k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.MediaFileConditionReady)
		require.NotNil(t, ready)
		assert.Equal(t, metav1.ConditionFalse, ready.Status)
		assert.Equal(t, "FileMissing", ready.Reason)
	})

	t.Run("a failing probe does not release the previous probe result", func(t *testing.T) {
		// Different bytes, so the probe hash is stale and the reconcile
		// really does re-probe rather than short-circuiting.
		require.NoError(t, os.WriteFile(path, []byte("truncated, unreadable by ffprobe"), 0o644))
		failing := &mediafile.Reconciler{
			Client: c,
			Clock:  time.Now,
			Probe: func(context.Context, string) (*commonv1.MediaInfo, *mediainfo.Raw, error) {
				return nil, nil, errors.New("ffprobe: Invalid data found when processing input")
			},
		}

		res, err := failing.Reconcile(ctx, ctrl.Request{NamespacedName: key})
		require.NoError(t, err)
		assert.Equal(t, 30*time.Second, res.RequeueAfter, "the ProbeFailed branch should requeue, not give up")

		assertSteadyStateSurvived(t)

		var got catalogv1alpha1.MediaFile
		require.NoError(t, c.Get(ctx, key, &got))
		probedCond := k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.MediaFileConditionProbed)
		require.NotNil(t, probedCond)
		assert.Equal(t, metav1.ConditionFalse, probedCond.Status)
		assert.Equal(t, "ProbeFailed", probedCond.Reason)
	})
}
