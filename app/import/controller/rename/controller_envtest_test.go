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

package rename_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/event"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/import/controller/rename"
	"github.com/mediactl/clustarr/app/import/worker/rescan"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// testClient talks straight to the envtest apiserver. RenameFile's re-read
// is the uncached one anyway, and the controller needs no field index, so
// there is no manager or cache here.
var testClient client.Client

func TestMain(m *testing.M) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		os.Exit(m.Run()) // every envtest below skips itself
	}
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{"../../../../config/crd/bases"},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	if err != nil {
		fmt.Fprintf(os.Stderr, "start envtest: %v\n", err)
		os.Exit(1)
	}
	testClient, err = client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
	if err != nil {
		fmt.Fprintf(os.Stderr, "build client: %v\n", err)
		_ = env.Stop()
		os.Exit(1)
	}
	code := m.Run()
	if err := env.Stop(); err != nil {
		fmt.Fprintf(os.Stderr, "stop envtest: %v\n", err)
	}
	os.Exit(code)
}

func requireEnvtest(t *testing.T) client.Client {
	t.Helper()
	if testClient == nil {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	return testClient
}

// mediaTempDir is a fresh directory under /data/media, which
// RootFolder.spec.path's CEL rule requires; see the rescan suite's helper of
// the same name.
func mediaTempDir(t *testing.T) string {
	t.Helper()
	prefix := os.Getenv("CLUSTARR_TEST_MEDIA_ROOT")
	if prefix == "" {
		prefix = "/data/media"
	}
	if err := os.MkdirAll(prefix, 0o755); err != nil {
		t.Skipf("%s is not creatable (%v); run `make test` or set CLUSTARR_TEST_MEDIA_ROOT under /data/media/", prefix, err)
	}
	dir, err := os.MkdirTemp(prefix, "clustarr-rename-")
	if err != nil {
		t.Skipf("%s is not writable (%v); run `make test` or set CLUSTARR_TEST_MEDIA_ROOT under /data/media/", prefix, err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func writeFile(t *testing.T, path string, size int64) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	f, err := os.Create(path) //nolint:gosec // a test directory
	require.NoError(t, err)
	require.NoError(t, f.Truncate(size))
	require.NoError(t, f.Close())
}

var correctedQuality = commonv1.Quality{Name: "Bluray-2160p", Source: commonv1.SourceBluray, Resolution: 2160}

// fixture is one namespace with a movie RootFolder and a steady MediaFile
// catalogarr proposes to rename within its folder.
type fixture struct {
	c        client.Client
	ns       string
	root     string
	name     string
	path     string
	expected string
	sidecar  string
	rec      *events.FakeRecorder
	r        *rename.Reconciler
}

type fixtureOpts struct {
	renameFiles *bool
	ready       metav1.ConditionStatus
	// sizeSkew makes spec.sizeBytes disagree with the file on disk.
	sizeSkew int64
	// episode records the file against an Episode of a Series rather than
	// a Movie.
	episode bool
}

func newFixture(t *testing.T, ctx context.Context, ns string, o fixtureOpts) *fixture {
	t.Helper()
	c := requireEnvtest(t)
	require.NoError(t, c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}))
	root := mediaTempDir(t)
	require.NoError(t, c.Create(ctx, &catalogv1alpha1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Name: "library", Namespace: ns},
		Spec: catalogv1alpha1.RootFolderSpec{
			Path: root, Kind: catalogv1alpha1.RootFolderKindMovie,
			Naming: catalogv1alpha1.NamingSpec{RenameFiles: o.renameFiles},
		},
	}))
	// A second RootFolder with the switch on, so a test with it off on the
	// file's own RootFolder exercises the lookup rather than the shortcut.
	require.NoError(t, c.Create(ctx, &catalogv1alpha1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Name: "elsewhere", Namespace: ns},
		Spec: catalogv1alpha1.RootFolderSpec{
			Path: filepath.Join(root, "elsewhere"), Kind: catalogv1alpha1.RootFolderKindMovie,
			Naming: catalogv1alpha1.NamingSpec{RenameFiles: new(true)},
		},
	}))

	ref := commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "heat-949"}
	dir := filepath.Join(root, "Heat (1995) {tmdb-949}")
	if o.episode {
		require.NoError(t, c.Create(ctx, &catalogv1alpha1.Series{
			ObjectMeta: metav1.ObjectMeta{Name: "fargo", Namespace: ns},
			Spec:       catalogv1alpha1.SeriesSpec{TvdbID: 269613, QualityProfileRef: "hd", RootFolderRef: "library"},
		}))
		require.NoError(t, c.Create(ctx, &catalogv1alpha1.Episode{
			ObjectMeta: metav1.ObjectMeta{Name: "fargo-s01e01", Namespace: ns},
			Spec:       catalogv1alpha1.EpisodeSpec{SeriesRef: "fargo", SeasonNumber: 1, EpisodeNumber: 1},
		}))
		ref = commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: "fargo-s01e01"}
		dir = filepath.Join(root, "Fargo (2014) {tvdb-269613}", "Season 01")
	} else {
		require.NoError(t, c.Create(ctx, &catalogv1alpha1.Movie{
			ObjectMeta: metav1.ObjectMeta{Name: "heat-949", Namespace: ns},
			Spec:       catalogv1alpha1.MovieSpec{TmdbID: 949, QualityProfileRef: "hd", RootFolderRef: "library"},
		}))
	}

	path := filepath.Join(dir, "release.1080p.bluray.x264-grp.mkv")
	writeFile(t, path, 4096)
	sidecar := filepath.Join(dir, "release.1080p.bluray.x264-grp.en.srt")
	writeFile(t, sidecar, 64)
	info, err := os.Stat(path)
	require.NoError(t, err)

	name := k8s.ChildName(ref.Name, "mediafile", path)
	_, err = k8s.Apply(ctx, c, rescan.FieldManager, catalogac.MediaFile(name, ns).WithSpec(
		catalogac.MediaFileSpec().
			WithMediaRef(ref).
			WithPath(path).
			WithSizeBytes(info.Size()+o.sizeSkew).
			WithModTime(metav1.NewTime(info.ModTime())).
			WithQuality(commonv1.Quality{Name: "Bluray-1080p", Source: commonv1.SourceBluray, Resolution: 1080}).
			WithRevision(commonv1.Revision{Version: 1}).
			WithReleaseType(commonv1.ReleaseTypeSingle).
			WithReleaseGroup("GRP").
			WithLanguages("en").
			WithImportedFrom(catalogac.ImportSource().
				WithDownloadRef("dl").
				WithReleaseTitle("Release.1080p.BluRay.x264-GRP").
				WithProtocol(commonv1.ProtocolTorrent).
				WithImportedAt(metav1.NewTime(time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)))).
			WithFormatScore(100).
			WithMatchedFormats("x264").
			WithProfileHash("hash").
			WithOriginal(true)))
	require.NoError(t, err)

	ready := o.ready
	if ready == "" {
		ready = metav1.ConditionTrue
	}
	now := metav1.Now()
	expected := filepath.Join(dir, "Canonical Name [Bluray-2160p][x264].mkv")
	_, err = k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarr, catalogac.MediaFile(name, ns).WithStatus(
		catalogac.MediaFileStatus().
			WithConditions(k8s.ConditionACs([]metav1.Condition{
				{Type: catalogv1alpha1.MediaFileConditionReady, Status: ready, Reason: "Ready", Message: "m", LastTransitionTime: now},
				{Type: catalogv1alpha1.MediaFileConditionProbed, Status: metav1.ConditionTrue, Reason: "Probed", Message: "m", LastTransitionTime: now},
				{Type: catalogv1alpha1.ConditionNamingCurrent, Status: metav1.ConditionFalse, Reason: "Stale", Message: "m", LastTransitionTime: now},
			})...).
			WithNaming(catalogac.NamingStatus().WithExpectedPath(expected).WithCurrent(false).WithQuality(correctedQuality)).
			WithSidecars(catalogac.Sidecar().WithPath(sidecar).WithLanguage("en"))))
	require.NoError(t, err)

	rec := events.NewFakeRecorder(10)
	return &fixture{
		c: c, ns: ns, root: root, name: name, path: path, expected: expected, sidecar: sidecar, rec: rec,
		r: &rename.Reconciler{Client: c, APIReader: c, Recorder: rec},
	}
}

func (f *fixture) read(t *testing.T, ctx context.Context) *catalogv1alpha1.MediaFile {
	t.Helper()
	var mf catalogv1alpha1.MediaFile
	require.NoError(t, f.c.Get(ctx, types.NamespacedName{Namespace: f.ns, Name: f.name}, &mf))
	return &mf
}

func (f *fixture) reconcile(t *testing.T, ctx context.Context) ctrl.Result {
	t.Helper()
	res, err := f.r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: f.ns, Name: f.name}})
	require.NoError(t, err)
	return res
}

func (f *fixture) events() []string {
	var out []string
	for {
		select {
		case e := <-f.rec.Events:
			out = append(out, e)
		default:
			return out
		}
	}
}

// managersOf lists every manager owning jsonPath on the main resource, so
// a co-owner is seen too.
func managersOf(t *testing.T, entries []metav1.ManagedFieldsEntry, jsonPath string) []string {
	t.Helper()
	var out []string
	for _, e := range entries {
		if e.Subresource != "" || e.FieldsV1 == nil {
			continue
		}
		var fields map[string]any
		require.NoError(t, json.Unmarshal(e.FieldsV1.GetRawBytes(), &fields))
		owned := true
		for _, part := range strings.Split(jsonPath, ".") {
			next, ok := fields["f:"+part].(map[string]any)
			if !ok {
				owned = false
				break
			}
			fields = next
		}
		if owned {
			out = append(out, e.Manager)
		}
	}
	return out
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// (a) The RootFolder allows it: the file and its sidecar move, the spec is
// importarr's complete set with the new path and the corrected quality, and
// an Event says so.
func TestReconcileRenamesAFileItsRootFolderAllows(t *testing.T) {
	ctx := context.Background()
	for _, episode := range []bool{false, true} {
		t.Run(fmt.Sprintf("episode=%v", episode), func(t *testing.T) {
			f := newFixture(t, ctx, fmt.Sprintf("rename-ok-%v", episode), fixtureOpts{renameFiles: new(true), episode: episode})
			before := f.read(t, ctx)

			assert.Equal(t, ctrl.Result{}, f.reconcile(t, ctx))

			assert.False(t, exists(f.path), "the file left its old path")
			assert.True(t, exists(f.expected), "the file is at the proposed path")
			assert.False(t, exists(f.sidecar))
			assert.True(t, exists(filepath.Join(filepath.Dir(f.expected), "Canonical Name [Bluray-2160p][x264].en.srt")),
				"the sidecar followed, keeping its language suffix")

			after := f.read(t, ctx)
			want := before.Spec
			want.Path = f.expected
			want.Quality = correctedQuality
			assert.Equal(t, want, after.Spec)
			for _, leaf := range []string{
				"spec.mediaRef", "spec.path", "spec.sizeBytes", "spec.modTime", "spec.quality", "spec.revision",
				"spec.releaseType", "spec.releaseGroup", "spec.languages", "spec.importedFrom", "spec.formatScore",
				"spec.matchedFormats", "spec.profileHash", "spec.original",
			} {
				assert.Equal(t, []string{string(rescan.FieldManager)}, managersOf(t, after.ManagedFields, leaf), leaf)
			}
			assert.Equal(t, []string{"Normal Renamed renamed " + f.path + " to " + f.expected}, f.events())
		})
	}
}

// (b) A file already at the proposed path: nothing moves, and an Event says
// why.
func TestReconcileReportsACollision(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx, "rename-collision", fixtureOpts{renameFiles: new(true)})
	writeFile(t, f.expected, 1234)
	before := f.read(t, ctx)

	assert.Equal(t, ctrl.Result{}, f.reconcile(t, ctx))

	assert.True(t, exists(f.path))
	st, err := os.Stat(f.expected)
	require.NoError(t, err)
	assert.Equal(t, int64(1234), st.Size(), "the file already there is untouched")
	assert.Equal(t, before.ResourceVersion, f.read(t, ctx).ResourceVersion)
	evs := f.events()
	require.Len(t, evs, 1)
	assert.True(t, strings.HasPrefix(evs[0], "Warning Collision "), evs[0])
}

// (c) The file on disk is not what spec records: nothing moves, and the
// controller looks again in a minute, once the rescan has re-observed it.
func TestReconcileRequeuesAChangedFile(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx, "rename-changed", fixtureOpts{renameFiles: new(true), sizeSkew: 1})
	before := f.read(t, ctx)

	assert.Equal(t, ctrl.Result{RequeueAfter: time.Minute}, f.reconcile(t, ctx))

	assert.True(t, exists(f.path))
	assert.False(t, exists(f.expected))
	assert.Equal(t, before.ResourceVersion, f.read(t, ctx).ResourceVersion)
	evs := f.events()
	require.Len(t, evs, 1)
	assert.True(t, strings.HasPrefix(evs[0], "Warning Changed "), evs[0])
}

// (d) The file's own RootFolder leaves renameFiles unset -- another one
// sets it -- so nothing happens; (f) a file catalogarr does not report
// Ready is not touched either (ruling R20).
func TestReconcileLeavesAFileAlone(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		opts fixtureOpts
	}{
		{name: "renamefiles-unset", opts: fixtureOpts{}},
		{name: "renamefiles-false", opts: fixtureOpts{renameFiles: new(false)}},
		{name: "not-ready", opts: fixtureOpts{renameFiles: new(true), ready: metav1.ConditionFalse}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, ctx, "rename-alone-"+tc.name, tc.opts)
			before := f.read(t, ctx)

			assert.Equal(t, ctrl.Result{}, f.reconcile(t, ctx))

			assert.True(t, exists(f.path))
			assert.True(t, exists(f.sidecar))
			assert.False(t, exists(f.expected))
			assert.Equal(t, before.ResourceVersion, f.read(t, ctx).ResourceVersion, "nothing was applied")
			assert.Empty(t, f.events())
		})
	}
}

// A LibraryScan still walking the file's folder holds the rename (spec D6)
// -- one over another RootFolder does not -- and once it is Completed the
// rename proceeds.
func TestReconcileHoldsWhileAScanWalksTheFolder(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx, "rename-scan", fixtureOpts{renameFiles: new(true)})
	scan := func(name, root string, phase catalogv1alpha1.ScanPhase) {
		require.NoError(t, f.c.Create(ctx, &catalogv1alpha1.LibraryScan{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: f.ns},
			Spec:       catalogv1alpha1.LibraryScanSpec{RootFolderRef: root},
		}))
		f.setScanPhase(t, ctx, name, phase)
	}
	scan("library-tick", "library", catalogv1alpha1.ScanPhaseRunning)
	scan("elsewhere-tick", "elsewhere", catalogv1alpha1.ScanPhaseRunning)
	scan("library-done", "library", catalogv1alpha1.ScanPhaseFailed)
	before := f.read(t, ctx)

	assert.Equal(t, ctrl.Result{RequeueAfter: time.Minute}, f.reconcile(t, ctx))
	assert.True(t, exists(f.path))
	assert.False(t, exists(f.expected))
	assert.Equal(t, before.ResourceVersion, f.read(t, ctx).ResourceVersion, "nothing was applied")
	assert.Equal(t, []string{"Normal ScanInProgress not renamed yet: LibraryScan library-tick is walking the file's folder"},
		f.events())

	f.setScanPhase(t, ctx, "library-tick", catalogv1alpha1.ScanPhaseCompleted)
	assert.Equal(t, ctrl.Result{}, f.reconcile(t, ctx))
	assert.False(t, exists(f.path))
	assert.True(t, exists(f.expected))
	assert.Equal(t, f.expected, f.read(t, ctx).Spec.Path)
	assert.Equal(t, []string{"Normal Renamed renamed " + f.path + " to " + f.expected}, f.events())
}

// A scan's walk is its RootFolder's path joined with spec.subpath: a
// subpath naming the file's folder, or only the file itself, holds the
// rename; one naming a sibling whose name is a prefix of the folder's
// ("Heat" beside "Heat (1995) {tmdb-949}") walks nothing of it and does not.
func TestReconcileHoldsOnlyForAScanWhoseSubpathCoversTheFolder(t *testing.T) {
	folder := "Heat (1995) {tmdb-949}"
	cases := []struct {
		name, ns, subpath string
		holds             bool
	}{
		{name: "the file's folder", ns: "rename-scan-folder", subpath: folder, holds: true},
		{name: "only the file", ns: "rename-scan-entry", subpath: filepath.Join(folder, "release.1080p.bluray.x264-grp.mkv"), holds: true},
		{name: "a sibling-prefix folder", ns: "rename-scan-sibling", subpath: "Heat", holds: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			f := newFixture(t, ctx, tc.ns, fixtureOpts{renameFiles: new(true)})
			require.NoError(t, os.MkdirAll(filepath.Join(f.root, "Heat"), 0o755))
			require.Equal(t, filepath.Join(f.root, folder), filepath.Dir(f.path), "the fixture's folder")
			require.NoError(t, f.c.Create(ctx, &catalogv1alpha1.LibraryScan{
				ObjectMeta: metav1.ObjectMeta{Name: "narrowed", Namespace: f.ns},
				Spec:       catalogv1alpha1.LibraryScanSpec{RootFolderRef: "library", Subpath: tc.subpath},
			}))
			f.setScanPhase(t, ctx, "narrowed", catalogv1alpha1.ScanPhaseRunning)

			if tc.holds {
				assert.Equal(t, ctrl.Result{RequeueAfter: time.Minute}, f.reconcile(t, ctx))
				assert.True(t, exists(f.path))
				assert.False(t, exists(f.expected))
				assert.Equal(t, []string{"Normal ScanInProgress not renamed yet: LibraryScan narrowed is walking the file's folder"},
					f.events())
				return
			}
			assert.Equal(t, ctrl.Result{}, f.reconcile(t, ctx))
			assert.False(t, exists(f.path))
			assert.True(t, exists(f.expected))
			assert.Equal(t, []string{"Normal Renamed renamed " + f.path + " to " + f.expected}, f.events())
		})
	}
}

func (f *fixture) setScanPhase(t *testing.T, ctx context.Context, name string, phase catalogv1alpha1.ScanPhase) {
	t.Helper()
	_, err := k8s.PatchStatus(ctx, f.c, k8s.ManagerImportarr, catalogac.LibraryScan(name, f.ns).
		WithStatus(catalogac.LibraryScanStatus().WithPhase(phase)))
	require.NoError(t, err)
}

// The predicate admits a renameable MediaFile when first seen, and an
// update only when its conditions or its proposed path changed.
func TestPredicateAdmitsOnlyARenameableFileWhoseConditionsChanged(t *testing.T) {
	mf := func(naming, ready, probed metav1.ConditionStatus, msg string) *catalogv1alpha1.MediaFile {
		return &catalogv1alpha1.MediaFile{Status: catalogv1alpha1.MediaFileStatus{Conditions: []metav1.Condition{
			{Type: catalogv1alpha1.ConditionNamingCurrent, Status: naming, Reason: "r", Message: msg},
			{Type: catalogv1alpha1.MediaFileConditionReady, Status: ready, Reason: "r"},
			{Type: catalogv1alpha1.MediaFileConditionProbed, Status: probed, Reason: "r"},
		}}}
	}
	const (
		cT, cF, cU = metav1.ConditionTrue, metav1.ConditionFalse, metav1.ConditionUnknown
	)
	stale := mf(cF, cT, cT, "the file's canonical path is /a")
	p := rename.Predicate()

	assert.True(t, p.Create(event.CreateEvent{Object: stale}))
	for _, notYet := range []*catalogv1alpha1.MediaFile{
		mf(cT, cT, cT, ""), mf(cU, cT, cT, ""), mf(cF, cF, cT, ""), mf(cF, cT, cF, ""), {},
	} {
		assert.False(t, p.Create(event.CreateEvent{Object: notYet}))
		assert.False(t, p.Update(event.UpdateEvent{ObjectOld: stale, ObjectNew: notYet}))
	}
	assert.True(t, p.Update(event.UpdateEvent{ObjectOld: mf(cU, cT, cT, ""), ObjectNew: stale}), "flipped stale")
	assert.True(t, p.Update(event.UpdateEvent{ObjectOld: mf(cF, cT, cT, "the file's canonical path is /b"), ObjectNew: stale}),
		"a new proposal")
	assert.False(t, p.Update(event.UpdateEvent{ObjectOld: stale.DeepCopy(), ObjectNew: stale}), "conditions unchanged")
	moved := stale.DeepCopy()
	moved.Status.Naming = &catalogv1alpha1.NamingStatus{ExpectedPath: "/b"}
	assert.True(t, p.Update(event.UpdateEvent{ObjectOld: stale, ObjectNew: moved}),
		"a new proposal whose condition reads the same")
	assert.False(t, p.Delete(event.DeleteEvent{Object: stale}))
}
