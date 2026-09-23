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

package rescan_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/importarr/worker/rescan"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// fixture is one namespace with a RootFolder, a LibraryScan and a planted
// tree, ready for a Handle call.
type fixture struct {
	ns   string
	root string
	scan *catalogv1alpha1.LibraryScan
	rf   *catalogv1alpha1.RootFolder
	bus  events.Bus
	c    client.Client
}

func newFixture(t *testing.T, ctx context.Context, name string, kind catalogv1alpha1.RootFolderKind, profile string, mode catalogv1alpha1.ScanMode) *fixture {
	t.Helper()
	c := requireEnvtest(t)
	ns := createNamespace(t, ctx, c, name)
	root := mediaTempDir(t)

	rf := &catalogv1alpha1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Name: "movies", Namespace: ns},
		Spec: catalogv1alpha1.RootFolderSpec{
			Path:     root,
			Kind:     kind,
			Defaults: catalogv1alpha1.RootDefaults{QualityProfileRef: profile},
		},
	}
	require.NoError(t, c.Create(ctx, rf))

	scan := &catalogv1alpha1.LibraryScan{
		ObjectMeta: metav1.ObjectMeta{Name: "tick", Namespace: ns},
		Spec:       catalogv1alpha1.LibraryScanSpec{RootFolderRef: "movies", Mode: mode},
	}
	require.NoError(t, c.Create(ctx, scan))

	// The worker reads through the manager's cache; wait for both objects
	// to land there before it looks for them.
	waitFor(t, 10*time.Second, func() bool {
		var got catalogv1alpha1.LibraryScan
		return c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "tick"}, &got) == nil
	})
	waitFor(t, 10*time.Second, func() bool {
		var got catalogv1alpha1.RootFolder
		return c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "movies"}, &got) == nil
	})

	return &fixture{ns: ns, root: root, scan: scan, rf: rf, bus: newBus(t, ctx), c: c}
}

func (f *fixture) task(dryRun bool) schema.ScanTask {
	return f.taskForSubpath("", dryRun)
}

// taskForSubpath builds the task the LibraryScan controller would publish for
// a scan narrowed to one directory: Path is the root folder joined with the
// subpath, while RootFolderRef still names the root folder itself.
func (f *fixture) taskForSubpath(subpath string, dryRun bool) schema.ScanTask {
	return schema.ScanTask{
		LibraryScanRef: schema.Ref{Namespace: f.ns, Name: f.scan.Name, UID: string(f.scan.UID)},
		RootFolderRef:  schema.Ref{Namespace: f.ns, Name: f.rf.Name},
		Path:           filepath.Join(f.root, subpath),
		Mode:           string(f.scan.Spec.Mode),
		DryRun:         dryRun,
	}
}

// Classification happens before matching, so a part file, an extras folder
// and a sample never reach the catalog at all.
func TestHandleSkipsPartExtraAndSampleFiles(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx, "rw-skip", catalogv1alpha1.RootFolderKindMovie, "hd-bluray-web", catalogv1alpha1.ScanModeFull)

	dir := filepath.Join(f.root, "Movie (2020) [tmdbid-1]")
	mustWriteFile(t, filepath.Join(dir, "Movie (2020) [tmdbid-1].mkv.part"), sampleFloor)
	mustWriteFile(t, filepath.Join(dir, "featurettes", "behind-the-scenes.mkv"), sampleFloor)
	mustWriteFile(t, filepath.Join(dir, "Movie.Sample.mkv"), sampleFloor)
	mustWriteFile(t, filepath.Join(dir, "notes.txt"), 16)

	msg := newFakeMessage(t, f.task(false))
	require.NoError(t, rescan.NewWorker(f.c, f.bus).Handle(ctx, msg))

	got := readProgress(t, ctx, f.bus, string(f.scan.UID))
	assert.True(t, got.Done)
	assert.Empty(t, got.Error)
	// Each is counted by what it is, and none of them as a skipped media
	// file: FilesSkipped is the CRD's "an unchanged fingerprint", and these
	// are not media at all.
	assert.Equal(t, int64(1), got.Parts)
	assert.Equal(t, int64(1), got.Extras)
	assert.Equal(t, int64(1), got.Samples)
	assert.Equal(t, int64(1), got.NotMedia)
	assert.Zero(t, got.FilesSkipped, "no media file was skipped")
	assert.Zero(t, got.FilesSeen, "nothing was classified as media")
	assert.Empty(t, got.Unmatched, "a file not considered is not an unmatched file")
	assert.Contains(t, got.Summary(), "4 other files not considered (1 not media, 1 samples, 1 extras, 1 partial downloads)")

	var movies catalogv1alpha1.MovieList
	require.NoError(t, f.c.List(ctx, &movies, client.InNamespace(f.ns)))
	assert.Empty(t, movies.Items)
}

// A confident match creates the Movie and the MediaFile, writes MediaFileSpec
// only, and leaves every byte of MediaFileStatus to catalogarr.
func TestHandleCreatesMovieAndMediaFileSpecForAConfidentMatch(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx, "rw-create", catalogv1alpha1.RootFolderKindMovie, "hd-bluray-web", catalogv1alpha1.ScanModeFull)

	path := filepath.Join(f.root, "Heat (1995) [tmdbid-949]", "Heat (1995) [tmdbid-949] - Bluray-1080p.mkv")
	mustWriteFile(t, path, sampleFloor)
	info, err := os.Stat(path)
	require.NoError(t, err)

	msg := newFakeMessage(t, f.task(false))
	require.NoError(t, rescan.NewWorker(f.c, f.bus).Handle(ctx, msg))

	got := readProgress(t, ctx, f.bus, string(f.scan.UID))
	assert.Equal(t, int64(1), got.FilesSeen)
	assert.Equal(t, int64(1), got.FilesMatched)
	assert.Equal(t, int64(1), got.ItemsCreated)
	assert.Empty(t, got.Unmatched)
	assert.Positive(t, msg.heartbeats.Load(), "a walk must send in-progress acks")

	var movies catalogv1alpha1.MovieList
	waitFor(t, 10*time.Second, func() bool {
		return f.c.List(ctx, &movies, client.InNamespace(f.ns)) == nil && len(movies.Items) == 1
	})
	movie := movies.Items[0]
	assert.Equal(t, int64(949), movie.Spec.TmdbID)
	assert.Equal(t, "hd-bluray-web", movie.Spec.QualityProfileRef)
	assert.Equal(t, "movies", movie.Spec.RootFolderRef)
	assert.Equal(t, catalogv1alpha1.MovieAddMethodScan, movie.Spec.AddOptions.AddMethod)
	require.NotNil(t, movie.Spec.AddOptions.SearchForMovie)
	assert.False(t, *movie.Spec.AddOptions.SearchForMovie, "a file already on disk must not trigger a grab for itself")
	assert.Equal(t, string(rescan.FieldManager), managerFor(t, movie.ManagedFields, "", "spec.tmdbID"))

	var files catalogv1alpha1.MediaFileList
	waitFor(t, 10*time.Second, func() bool {
		return f.c.List(ctx, &files, client.InNamespace(f.ns)) == nil && len(files.Items) == 1
	})
	mf := files.Items[0]

	assert.Equal(t, commonv1.MediaKindMovie, mf.Spec.MediaRef.Kind)
	assert.Equal(t, movie.Name, mf.Spec.MediaRef.Name)
	assert.Equal(t, path, mf.Spec.Path)
	assert.Equal(t, info.Size(), mf.Spec.SizeBytes)
	assert.False(t, mf.Spec.ModTime.IsZero())
	assert.Equal(t, "Bluray-1080p", mf.Spec.Quality.Name, "the quality frozen at import comes from the parsed release")
	assert.NotEmpty(t, mf.Spec.Languages)
	// Radarr's own renamed-file layout carries no group, and pkg/release must
	// not read the quality's "-1080p" as one: spec.releaseGroup is frozen at
	// import and custom formats score on it (pkg/release's
	// TestParsePathReleaseGroupOnLibraryLayouts pins the parser side).
	assert.Empty(t, mf.Spec.ReleaseGroup,
		"a group that is really a quality token must not be frozen into spec")

	// The two-writer split: importarr owns MediaFileSpec, catalogarr owns
	// all of MediaFileStatus. Nothing here may have touched status.
	assert.Equal(t, string(rescan.FieldManager), managerFor(t, mf.ManagedFields, "", "spec.path"))
	assert.Equal(t, string(rescan.FieldManager), managerFor(t, mf.ManagedFields, "", "spec.sizeBytes"))
	assert.Empty(t, managerFor(t, mf.ManagedFields, "status", "status"),
		"importarr must never write any MediaFile status field")
	assert.Empty(t, mf.Status.ProbeHash)
	assert.Nil(t, mf.Status.MediaInfo)

	// This fixture's "hd-bluray-web" names no QualityProfile that exists
	// in the cluster, so the file is recorded unscored: no formatScore,
	// matchedFormats or profileHash, and the empty profileHash says so.
	// TestHandleScoresAScannedMovieFileAgainstItsProfile covers scoring.
	assert.Zero(t, mf.Spec.FormatScore)
	assert.Empty(t, mf.Spec.MatchedFormats)
	assert.Empty(t, mf.Spec.ProfileHash)
	assert.Empty(t, managerFor(t, mf.ManagedFields, "", "spec.formatScore"))
}

// An incremental scan skips a file whose size and mtime still match what the
// MediaFile records; a full scan re-applies it.
func TestHandleIncrementalSkipsAnUnchangedFingerprint(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx, "rw-incremental", catalogv1alpha1.RootFolderKindMovie, "hd-bluray-web", catalogv1alpha1.ScanModeIncremental)

	path := filepath.Join(f.root, "Heat (1995) [tmdbid-949]", "Heat (1995) [tmdbid-949] - Bluray-1080p.mkv")
	mustWriteFile(t, path, sampleFloor)

	w := rescan.NewWorker(f.c, f.bus)
	require.NoError(t, w.Handle(ctx, newFakeMessage(t, f.task(false))))
	first := readProgress(t, ctx, f.bus, string(f.scan.UID))
	require.Equal(t, int64(1), first.FilesMatched)

	waitFor(t, 10*time.Second, func() bool {
		var list catalogv1alpha1.MediaFileList
		return f.c.List(ctx, &list, client.InNamespace(f.ns),
			client.MatchingFields{rescan.MediaFilePathIndexKey: path}) == nil && len(list.Items) == 1
	})

	next, msg := f.nextScan(t, ctx, "tick-2")
	require.NoError(t, w.Handle(ctx, msg))
	second := readProgress(t, ctx, f.bus, string(next.UID))
	assert.Equal(t, int64(1), second.FilesSkipped, "the unchanged fingerprint is skipped")
	assert.Equal(t, int64(1), second.Unchanged, "and counted as unchanged")
	assert.Zero(t, second.FilesMatched)
}

// nextScan creates another LibraryScan of the fixture's root folder, as the
// schedule's next tick would, and returns it with the task the controller
// would publish for it. A second walk is a second scan: a second delivery of
// the FIRST scan's task is a redelivery, which resumes that scan's tally
// rather than walking again.
func (f *fixture) nextScan(t *testing.T, ctx context.Context, name string) (*catalogv1alpha1.LibraryScan, *fakeMessage) {
	t.Helper()
	scan := &catalogv1alpha1.LibraryScan{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: f.ns},
		Spec:       catalogv1alpha1.LibraryScanSpec{RootFolderRef: f.rf.Name, Mode: f.scan.Spec.Mode},
	}
	require.NoError(t, f.c.Create(ctx, scan))
	waitFor(t, 10*time.Second, func() bool {
		var got catalogv1alpha1.LibraryScan
		return f.c.Get(ctx, types.NamespacedName{Namespace: f.ns, Name: name}, &got) == nil
	})
	task := f.task(false)
	task.LibraryScanRef.Name, task.LibraryScanRef.UID = scan.Name, string(scan.UID)
	return scan, newFakeMessage(t, task)
}

// The never-guess rule: a file with no provider id and no title match becomes
// an unmatched entry with a reason, never a speculative Movie.
func TestHandleRecordsAnUnattributableFileWithAReason(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx, "rw-unmatched", catalogv1alpha1.RootFolderKindMovie, "hd-bluray-web", catalogv1alpha1.ScanModeFull)

	rel := filepath.Join("Some Unknown Film (2024)", "Some Unknown Film (2024).mkv")
	mustWriteFile(t, filepath.Join(f.root, rel), sampleFloor)

	require.NoError(t, rescan.NewWorker(f.c, f.bus).Handle(ctx, newFakeMessage(t, f.task(false))))

	got := readProgress(t, ctx, f.bus, string(f.scan.UID))
	assert.Equal(t, int64(1), got.FilesSeen)
	assert.Zero(t, got.FilesMatched)
	require.Len(t, got.Unmatched, 1)
	assert.Equal(t, rel, got.Unmatched[0].Path, "the path is relative to the root folder, per the CRD field")
	assert.NotEmpty(t, got.Unmatched[0].Reason)
	assert.False(t, got.Unmatched[0].SeenAt.IsZero())

	var movies catalogv1alpha1.MovieList
	require.NoError(t, f.c.List(ctx, &movies, client.InNamespace(f.ns)))
	assert.Empty(t, movies.Items, "the scanner never guesses an item into existence")
}

// An unmatched path is relative to the ROOT FOLDER, per the CRD field. When
// spec.subpath narrows the walk, the subpath is part of that relative path
// and must not be swallowed: two files with the same basename under different
// subpaths would otherwise be indistinguishable in status.
func TestHandleRecordsUnmatchedPathsRelativeToTheRootFolderNotTheSubpath(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx, "rw-subpath", catalogv1alpha1.RootFolderKindMovie, "hd-bluray-web", catalogv1alpha1.ScanModeFull)

	subpath := "Unsorted"
	rel := filepath.Join(subpath, "Some Unknown Film (2024)", "Some Unknown Film (2024).mkv")
	mustWriteFile(t, filepath.Join(f.root, rel), sampleFloor)
	// A second, identically named file outside the subpath, to prove the
	// recorded paths could actually be told apart.
	mustWriteFile(t, filepath.Join(f.root, "Some Unknown Film (2024)", "Some Unknown Film (2024).mkv"), sampleFloor)

	require.NoError(t, rescan.NewWorker(f.c, f.bus).Handle(ctx, newFakeMessage(t, f.taskForSubpath(subpath, false))))

	got := readProgress(t, ctx, f.bus, string(f.scan.UID))
	assert.Equal(t, int64(1), got.FilesSeen, "only the subpath is walked")
	require.Len(t, got.Unmatched, 1)
	assert.Equal(t, rel, got.Unmatched[0].Path,
		"the path keeps its subpath prefix, because it is relative to the root folder")
}

// Library rescan handles movie root folders. Anything else is reported as an
// honest scope limit rather than guessed at.
func TestHandleReportsUnsupportedRootFolderKinds(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx, "rw-series", catalogv1alpha1.RootFolderKindSeries, "hd-bluray-web", catalogv1alpha1.ScanModeFull)

	mustWriteFile(t, filepath.Join(f.root, "Breaking Bad (2008)", "Breaking Bad - S01E01.mkv"), sampleFloor)

	require.NoError(t, rescan.NewWorker(f.c, f.bus).Handle(ctx, newFakeMessage(t, f.task(false))))

	got := readProgress(t, ctx, f.bus, string(f.scan.UID))
	require.Len(t, got.Unmatched, 1)
	assert.Contains(t, got.Unmatched[0].Reason, "not supported by library rescan yet")
}

// A root folder with no default quality profile cannot have a Movie created
// under it -- Movie.spec.qualityProfileRef is required -- so the file is
// reported instead of applied and rejected on every pass.
func TestHandleReportsAMissingDefaultQualityProfile(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx, "rw-no-profile", catalogv1alpha1.RootFolderKindMovie, "", catalogv1alpha1.ScanModeFull)

	mustWriteFile(t, filepath.Join(f.root, "Heat (1995) [tmdbid-949]", "Heat (1995) [tmdbid-949].mkv"), sampleFloor)

	require.NoError(t, rescan.NewWorker(f.c, f.bus).Handle(ctx, newFakeMessage(t, f.task(false))))

	got := readProgress(t, ctx, f.bus, string(f.scan.UID))
	require.Len(t, got.Unmatched, 1)
	assert.Contains(t, got.Unmatched[0].Reason, "qualityProfileRef")

	var movies catalogv1alpha1.MovieList
	require.NoError(t, f.c.List(ctx, &movies, client.InNamespace(f.ns)))
	assert.Empty(t, movies.Items)
}

// A dry run reports the same tally without creating anything.
func TestHandleDryRunChangesNothing(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx, "rw-dry-run", catalogv1alpha1.RootFolderKindMovie, "hd-bluray-web", catalogv1alpha1.ScanModeFull)

	mustWriteFile(t, filepath.Join(f.root, "Heat (1995) [tmdbid-949]", "Heat (1995) [tmdbid-949].mkv"), sampleFloor)

	require.NoError(t, rescan.NewWorker(f.c, f.bus).Handle(ctx, newFakeMessage(t, f.task(true))))

	got := readProgress(t, ctx, f.bus, string(f.scan.UID))
	assert.Equal(t, int64(1), got.FilesMatched)
	assert.Equal(t, int64(1), got.ItemsCreated)

	var movies catalogv1alpha1.MovieList
	require.NoError(t, f.c.List(ctx, &movies, client.InNamespace(f.ns)))
	assert.Empty(t, movies.Items, "a dry run creates nothing")
	var files catalogv1alpha1.MediaFileList
	require.NoError(t, f.c.List(ctx, &files, client.InNamespace(f.ns)))
	assert.Empty(t, files.Items)
}

// A task whose scan has been deleted, or replaced under the same name, is
// finished rather than retried forever: there is nobody left to report to.
func TestHandleDiscardsATaskWithNoLiveScan(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx, "rw-discard", catalogv1alpha1.RootFolderKindMovie, "hd-bluray-web", catalogv1alpha1.ScanModeFull)
	w := rescan.NewWorker(f.c, f.bus)

	gone := f.task(false)
	gone.LibraryScanRef.Name = "no-such-scan"
	assertDiscarded(t, w.Handle(ctx, newFakeMessage(t, gone)))

	replaced := f.task(false)
	replaced.LibraryScanRef.UID = "a-different-uid"
	assertDiscarded(t, w.Handle(ctx, newFakeMessage(t, replaced)))

	garbage := newFakeMessage(t, f.task(false))
	garbage.env.Data = []byte("{")
	assertDiscarded(t, w.Handle(ctx, garbage))
}

// A walk that cannot run at all reports Failed through the checkpoint once
// the topology's last delivery has been used, so the controller stops
// polling a scan that will never finish.
func TestHandleReportsAFailedWalkOnTheFinalDelivery(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx, "rw-failed", catalogv1alpha1.RootFolderKindMovie, "hd-bluray-web", catalogv1alpha1.ScanModeFull)

	task := f.task(false)
	task.Path = filepath.Join(f.root, "does-not-exist")
	w := rescan.NewWorker(f.c, f.bus)

	// An early delivery is simply retried; nothing final is reported.
	early := newFakeMessage(t, task)
	require.Error(t, w.Handle(ctx, early))
	_, err := f.bus.KV(events.BucketProgress).Get(ctx, rescan.ProgressKey(string(f.scan.UID)))
	assert.ErrorIs(t, err, events.ErrKeyNotFound)

	spec, ok := events.Default().Consumer(events.ConsumerImportScan)
	require.True(t, ok)
	final := newFakeMessage(t, task)
	final.attempt = uint64(spec.MaxDeliver)
	require.Error(t, w.Handle(ctx, final))

	got := readProgress(t, ctx, f.bus, string(f.scan.UID))
	assert.True(t, got.Done)
	assert.NotEmpty(t, got.Error)
}
