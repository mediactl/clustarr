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
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	transcodeac "github.com/mediactl/clustarr/api/applyconfiguration/transcode/transcode/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/controller/mediafile"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
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

// importarrCreatesMediaFile simulates the file-import worker task this task
// does not own: it Applies the full MediaFileSpec under
// k8s.ManagerImportarrWorker, exactly the fields "Resolving the
// field-manager split" assigns importarr, none of the three catalogarr may
// later take over.
func importarrCreatesMediaFile(t *testing.T, ctx context.Context, c client.Client, ns, name, path string, size int64, modTime time.Time, q commonv1.Quality) {
	t.Helper()
	ac := catalogac.MediaFile(name, ns).WithSpec(
		catalogac.MediaFileSpec().
			WithMediaRef(commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "inception"}).
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
	if _, err := k8s.Apply(ctx, c, k8s.ManagerImportarrWorker, ac); err != nil {
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

	r := &mediafile.Reconciler{Client: c, Probe: fakeProbe, Catalogue: catalogue.LoadedCatalogue(), Clock: time.Now}
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

	for _, want := range []struct{ manager, subresource string }{
		{"importarr-worker", ""},
		{"catalogarr", "status"},
	} {
		if !managesField(got.ManagedFields, want.manager, want.subresource) {
			t.Errorf("no %q/%q entry in managedFields: %+v", want.manager, want.subresource, fieldManagerNames(got.ManagedFields))
		}
	}
	if managesField(got.ManagedFields, "catalogarr", "") {
		t.Errorf("catalogarr claimed a main-resource field before any transcode: %+v", fieldManagerNames(got.ManagedFields))
	}

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
		{"importarr-worker", ""}, // still owns path/quality/... (untouched fields)
		{"catalogarr", ""},       // now owns sizeBytes/modTime/original
		{"catalogarr", "status"},
	} {
		if !managesField(got.ManagedFields, want.manager, want.subresource) {
			t.Errorf("no %q/%q entry in managedFields after transcode: %+v", want.manager, want.subresource, fieldManagerNames(got.ManagedFields))
		}
	}
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
