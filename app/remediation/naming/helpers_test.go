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
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	k8sevents "k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/import/mediafilespec"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/app/remediation/naming"
	"github.com/mediactl/clustarr/app/remediation/remediationtest"
	"github.com/mediactl/clustarr/app/remediation/rename"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/naming/catalogctx"
	"github.com/mediactl/clustarr/pkg/quality"
)

// The shared fixtures, under the old package's names.
var (
	mustNamespace      = remediationtest.MustNamespace
	mustQualityProfile = remediationtest.MustQualityProfile
	mustMovie          = remediationtest.MustMovie
	mustSeries         = remediationtest.MustSeries
	mustEpisode        = remediationtest.MustEpisode
	writeFile          = remediationtest.WriteFile
	fakeMediaInfo      = remediationtest.FakeMediaInfo
	managedFieldPaths  = remediationtest.ManagedFieldPaths
	reconcileOnce      = remediationtest.ReconcileOnce
	catalogManager     = k8s.ManagerCatalog.String()
	transcodeTag       = "hevc-main10@profile-hash-def"
)

// webdl720 is the name-derived quality the naming fixtures import under.
// The fake probe reports 1920x1080, so the probe-corrected quality is
// WEBDL-1080p: a fixture whose spec quality already matched the probe could
// not tell a render that used the corrected quality from one that used the
// frozen one.
var webdl720 = remediationtest.WebDL720

// harness is the probe harness with the naming planner and the rename
// actuator beside it.
type harness struct {
	*remediationtest.ProbeHarness
	Naming    *naming.Adapter
	Rename    *rename.Actuator
	renameRec *k8sevents.FakeRecorder
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	rec := k8sevents.NewFakeRecorder(64)
	return &harness{
		ProbeHarness: remediationtest.NewProbeHarness(t), Naming: naming.New(naming.Options{}),
		Rename: rename.New(rename.Options{Recorder: rec}), renameRec: rec,
	}
}

func (h *harness) planners() []remediation.Bound {
	return []remediation.Bound{h.Probe(), remediation.Bind[naming.Input](h.Naming)}
}

// loop is f's loop over the probe and naming planners, with the rename
// actuator when withRename.
func (h *harness) loop(t *testing.T, f namedFile, withRename bool) *remediation.Reconciler {
	t.Helper()
	f.e.Loop.Bus = h.Pub
	return h.loopOn(t, f, f.e.Loop, withRename)
}

// loopReading is loop with reader as the loop's cache.
func (h *harness) loopReading(t *testing.T, f namedFile, reader client.Reader) *remediation.Reconciler {
	t.Helper()
	env := *f.e.Loop
	env.Reader, env.Bus = reader, h.Pub
	return h.loopOn(t, f, &env, false)
}

func (h *harness) loopOn(t *testing.T, f namedFile, env *remediation.Env, withRename bool) *remediation.Reconciler {
	t.Helper()
	var acts []remediation.Actuator
	if withRename {
		acts = append(acts, h.Rename)
	}
	r, err := remediation.NewReconciler(f.e.Client(), env, f.e.Recorder, remediation.Config{}, h.planners(), acts...)
	require.NoError(t, err)
	return r
}

// namedFile is a movie MediaFile, "inception" with metadata "Inception"
// 2010, under RootFolder "movies", on its own envtest apiserver.
type namedFile struct {
	e    *remediationtest.Env
	c    client.Client
	key  types.NamespacedName
	path string
	root string
}

type fileOpts struct {
	renameFiles *bool
	underRoot   bool // the file lives in its canonical folder under a writable root
}

type fileOpt func(*fileOpts)

// withRenameFiles sets the RootFolder's spec.naming.renameFiles and puts
// the file in its canonical folder under a writable root, as a rename needs.
func withRenameFiles(on bool) fileOpt {
	return func(o *fileOpts) { o.renameFiles, o.underRoot = &on, true }
}

const movieFolder = "Inception (2010) [tmdbid-27205]"

func newNamedFile(t *testing.T, ns string, opts ...fileOpt) namedFile {
	t.Helper()
	var o fileOpts
	for _, opt := range opts {
		opt(&o)
	}
	e := remediationtest.Start(t)
	ctx := context.Background()
	mustNamespace(t, ctx, e.Admin, ns)
	mustQualityProfile(t, ctx, e.Admin, "qp-video")
	root, dir := "/data/media/movies", t.TempDir()
	if o.underRoot {
		root = mediaTempDir(t)
		dir = filepath.Join(root, movieFolder)
		require.NoError(t, os.MkdirAll(dir, 0o755))
	}
	mustRootFolder(t, e.Admin, ns, "movies", root, catalogv1alpha1.RootFolderKindMovie, o.renameFiles)
	mustMovie(t, ctx, e.Admin, ns, "inception", "qp-video")
	setMovieMetadata(t, e.Admin, ns, "inception", "Inception", 2010)
	path := writeFile(t, dir, "Inception.2010.720p.WEB-DL.mkv", []byte("stand-in bytes"))
	const name = "inception-abc1234567"
	importCreates(t, e.Admin, ns, name, commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "inception"}, path, webdl720)
	return namedFile{e: e, c: e.Admin, key: types.NamespacedName{Namespace: ns, Name: name}, path: path, root: root}
}

func (f namedFile) get(t *testing.T) catalogv1alpha1.MediaFile {
	t.Helper()
	return *f.e.Get(t, f.key)
}

func (f namedFile) uid(t *testing.T) string { return string(f.get(t).UID) }

// mediaTempDir is a fresh directory under /data/media, which
// RootFolder.spec.path's CEL rule requires.
func mediaTempDir(t *testing.T) string {
	t.Helper()
	prefix := os.Getenv("CLUSTARR_TEST_MEDIA_ROOT")
	if prefix == "" {
		prefix = "/data/media"
	}
	if err := os.MkdirAll(prefix, 0o755); err != nil {
		t.Skipf("%s is not creatable (%v); run `make test` or set CLUSTARR_TEST_MEDIA_ROOT under /data/media/", prefix, err)
	}
	dir, err := os.MkdirTemp(prefix, "clustarr-naming-")
	if err != nil {
		t.Skipf("%s is not writable (%v); run `make test` or set CLUSTARR_TEST_MEDIA_ROOT under /data/media/", prefix, err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// mustRootFolder creates a RootFolder with every other naming setting left
// to its CRD default, as a user's kubectl apply of a bare RootFolder would.
func mustRootFolder(t *testing.T, c client.Client, ns, name, path string, kind catalogv1alpha1.RootFolderKind, renameFiles *bool) {
	t.Helper()
	rf := &catalogv1alpha1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       catalogv1alpha1.RootFolderSpec{Path: path, Kind: kind, Naming: catalogv1alpha1.NamingSpec{RenameFiles: renameFiles}},
	}
	require.NoError(t, client.IgnoreAlreadyExists(c.Create(context.Background(), rf)))
}

// setRenameFiles merge-patches the RootFolder's spec.naming.renameFiles.
func setRenameFiles(t *testing.T, f namedFile, on bool) {
	t.Helper()
	patch, err := json.Marshal(map[string]any{"spec": map[string]any{"naming": map[string]any{"renameFiles": on}}})
	require.NoError(t, err)
	rf := &catalogv1alpha1.RootFolder{ObjectMeta: metav1.ObjectMeta{Name: "movies", Namespace: f.key.Namespace}}
	require.NoError(t, f.c.Patch(context.Background(), rf, client.RawPatch(types.MergePatchType, patch)))
}

// setMovieMetadata stands in for the metadata gateway, the sole writer of
// Movie.status.metadata, under its own field manager.
func setMovieMetadata(t *testing.T, c client.Client, ns, name, title string, year int32) {
	t.Helper()
	_, err := k8s.PatchStatus(context.Background(), c, k8s.ManagerCatalogMetadata,
		catalogac.Movie(name, ns).WithStatus(catalogac.MovieStatus().WithMetadata(
			catalogac.MovieMetadata().WithTitle(title).WithYear(year).
				WithExternalIDs(map[string]string{"imdb": "tt1375666"}))))
	require.NoError(t, err)
}

// importCreates stands in for the import domain's materialisation of a
// placed file: the whole MediaFileSpec under its own field manager.
func importCreates(t *testing.T, c client.Client, ns, name string, ref commonv1.MediaRef, path string, q commonv1.Quality) {
	t.Helper()
	stat, err := os.Stat(path)
	require.NoError(t, err)
	ac := catalogac.MediaFile(name, ns).WithSpec(catalogac.MediaFileSpec().
		WithMediaRef(ref).WithPath(path).WithSizeBytes(stat.Size()).WithModTime(metav1.NewTime(stat.ModTime())).
		WithQuality(q).WithRevision(commonv1.Revision{Version: 1}).WithReleaseType(commonv1.ReleaseTypeSingle).
		WithFormatScore(40).WithProfileHash("profile-hash-abc").WithOriginal(true))
	_, err = k8s.Apply(context.Background(), c, mediafilespec.FieldManager, ac)
	require.NoError(t, err)
}

// probeAndName runs the passes that land a probe answered with mi and then
// render the file's name: the first asks, the second incorporates (naming's
// Gather is gated while the stored probe is pending, so it asks Again), the
// third names.
func probeAndName(t *testing.T, h *harness, r *remediation.Reconciler, f namedFile, mi *commonv1.MediaInfo) {
	t.Helper()
	reconcileOnce(t, r, f.key)
	h.Answer(t, f.uid(t), mi)
	reconcileOnce(t, r, f.key)
	reconcileOnce(t, r, f.key)
}

// wantMoviePath renders what an import of mf would have produced, through
// the same catalogctx calls the import makes, from the objects as the
// apiserver now holds them -- with the probe-corrected quality, which is the
// value the rename re-applies into spec.quality.
func wantMoviePath(t *testing.T, c client.Client, mf *catalogv1alpha1.MediaFile) string {
	t.Helper()
	ctx := context.Background()
	var movie catalogv1alpha1.Movie
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: mf.Namespace, Name: mf.Spec.MediaRef.Name}, &movie))
	var root catalogv1alpha1.RootFolder
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: mf.Namespace, Name: movie.Spec.RootFolderRef}, &root))
	base, ok := catalogctx.Movie(&movie)
	require.True(t, ok, "setup: the movie has no metadata title")
	spec := mf.Spec
	spec.Quality, _ = quality.AugmentFromMediaInfo(spec.Quality, mf.Status.MediaInfo)
	want, err := catalogctx.MovieFilePath(&root, &movie, catalogctx.File(ctx, base, &spec, mf.Status.MediaInfo),
		catalogctx.ContainerExt(mf.Status.MediaInfo, mf.Spec.Path))
	require.NoError(t, err)
	return want
}

// catalogNamingFields is catalog's FieldsV1 set under f:status.f:naming, nil
// when it claims none: SSA tracks ownership per leaf, so a release test
// asserts the leaves, not the parent.
func catalogNamingFields(entries []metav1.ManagedFieldsEntry) map[string]any {
	status, ok := managedFieldPaths(entries, catalogManager, "status")["f:status"].(map[string]any)
	if !ok {
		return nil
	}
	n, _ := status["f:naming"].(map[string]any)
	return n
}

// setStatus changes f's stored status as another planner of the loop would:
// the whole status, mutated, applied under catalog.
func setStatus(t *testing.T, f namedFile, mutate func(*catalogv1alpha1.MediaFileStatus)) {
	t.Helper()
	mf := f.get(t)
	st := mf.Status.DeepCopy()
	mutate(st)
	raw, err := json.Marshal(st)
	require.NoError(t, err)
	var ac catalogac.MediaFileStatusApplyConfiguration
	require.NoError(t, json.Unmarshal(raw, &ac))
	_, err = k8s.PatchStatus(context.Background(), f.c, k8s.ManagerCatalog, catalogac.MediaFile(f.key.Name, f.key.Namespace).WithStatus(&ac))
	require.NoError(t, err)
}

// filterPrefix keeps the events that start with prefix.
func filterPrefix(events []string, prefix string) []string {
	var out []string
	for _, e := range events {
		if strings.HasPrefix(e, prefix) {
			out = append(out, e)
		}
	}
	return out
}

// clock is a pass clock a test moves by hand.
type clock struct{ now time.Time }

func newClock() *clock { return &clock{now: time.Now().UTC().Truncate(time.Second)} }

func (c *clock) Now() time.Time      { return c.now }
func (c *clock) Add(d time.Duration) { c.now = c.now.Add(d) }
