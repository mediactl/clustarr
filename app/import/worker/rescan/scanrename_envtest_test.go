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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/import/controller/libraryscan"
	"github.com/mediactl/clustarr/app/import/scanprogress"
	"github.com/mediactl/clustarr/app/import/worker/rescan"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// plantCandidate plants a movie file at rel beneath the root folder,
// records it under rescan.FieldManager as an import would, and seeds
// catalogarr's status proposing expectedBase in the same folder: a rename
// candidate (rescan.Renameable). It waits until the manager's cache -- the
// one the rename pass lists MediaFiles from -- holds the seeded status.
func (f *fixture) plantCandidate(t *testing.T, ctx context.Context, rel, expectedBase string) staleFile {
	t.Helper()
	path := filepath.Join(f.root, rel)
	mustWriteFile(t, path, sampleFloor)
	info, err := os.Stat(path)
	require.NoError(t, err)

	movie := strings.ToLower(strings.Fields(filepath.Base(filepath.Dir(path)))[0])
	name := k8s.ChildName(movie, "mediafile", path)
	_, err = k8s.Apply(ctx, f.c, rescan.FieldManager, catalogac.MediaFile(name, f.ns).WithSpec(
		catalogac.MediaFileSpec().
			WithMediaRef(commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: movie}).
			WithPath(path).
			WithSizeBytes(info.Size()).
			WithModTime(metav1.NewTime(info.ModTime())).
			WithQuality(commonv1.Quality{Name: "Bluray-1080p", Source: commonv1.SourceBluray, Resolution: 1080}).
			WithOriginal(true)))
	require.NoError(t, err)

	expected := filepath.Join(filepath.Dir(path), expectedBase)
	f.seedNamingStatus(t, ctx, name, catalogac.NamingStatus().
		WithExpectedPath(expected).WithCurrent(false).WithQuality(correctedQuality))
	waitFor(t, 10*time.Second, func() bool {
		var mf catalogv1alpha1.MediaFile
		return f.c.Get(ctx, types.NamespacedName{Namespace: f.ns, Name: name}, &mf) == nil && rescan.Renameable(&mf)
	})
	return staleFile{name: name, path: path, expected: expected}
}

// renameScan creates a LibraryScan of the fixture's root folder narrowed to
// subpath, with the given rename pass and dry-run flag, and returns it with
// the task the LibraryScan controller would publish for it. The scan is put
// in the Running phase, as the controller's start step would, so that
// settle can poll it.
func (f *fixture) renameScan(
	t *testing.T, ctx context.Context, name, subpath string, rename catalogv1alpha1.ScanRename, dryRun bool,
) (*catalogv1alpha1.LibraryScan, *fakeMessage) {
	t.Helper()
	scan := &catalogv1alpha1.LibraryScan{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: f.ns},
		Spec: catalogv1alpha1.LibraryScanSpec{
			RootFolderRef: f.rf.Name, Mode: catalogv1alpha1.ScanModeIncremental,
			Subpath: subpath, Rename: rename, DryRun: dryRun,
		},
	}
	require.NoError(t, f.c.Create(ctx, scan))
	started := metav1.Now()
	scan.Status = catalogv1alpha1.LibraryScanStatus{Phase: catalogv1alpha1.ScanPhaseRunning, StartedAt: &started}
	//nolint:forbidigo // test fixture standing in for the controller's start step, not a production status write
	require.NoError(t, f.c.Status().Update(ctx, scan))
	waitFor(t, 10*time.Second, func() bool {
		var got catalogv1alpha1.LibraryScan
		return f.c.Get(ctx, types.NamespacedName{Namespace: f.ns, Name: name}, &got) == nil &&
			got.Status.Phase == catalogv1alpha1.ScanPhaseRunning
	})
	task := f.taskForSubpath(subpath, dryRun)
	task.LibraryScanRef.Name, task.LibraryScanRef.UID = scan.Name, string(scan.UID)
	return scan, newFakeMessage(t, task)
}

// renameWorker is the worker as importarr's run.go builds it, with the
// uncached reader the rename pass re-reads each MediaFile through.
func (f *fixture) renameWorker(t *testing.T) *rescan.Worker {
	t.Helper()
	w := rescan.NewWorker(f.c, f.bus)
	w.APIReader = f.api(t)
	return w
}

// settle runs the LibraryScan controller over a scan whose worker has
// reported its final tally, and returns the status it renders -- the only
// writer of LibraryScan.status, so the only way to see what a scan reports.
func (f *fixture) settle(t *testing.T, ctx context.Context, scan *catalogv1alpha1.LibraryScan) catalogv1alpha1.LibraryScanStatus {
	t.Helper()
	r := &libraryscan.Reconciler{Client: f.c, Bus: f.bus, Clock: time.Now}
	_, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: f.ns, Name: scan.Name}})
	require.NoError(t, err)
	var got catalogv1alpha1.LibraryScan
	require.NoError(t, f.api(t).Get(ctx, types.NamespacedName{Namespace: f.ns, Name: scan.Name}, &got))
	require.Equal(t, catalogv1alpha1.ScanPhaseCompleted, got.Status.Phase, "conditions: %+v", got.Status.Conditions)
	return got.Status
}

// A scan with rename: dryRun lists every rename candidate beneath its
// subpath with the DryRun reason and touches nothing; rename: apply moves
// them and counts the moves. A candidate outside the subpath is neither
// listed nor moved by either. The walk's own tally lands first.
func TestLibraryScanRenamePassDryRunThenApply(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx, "rn-scan", catalogv1alpha1.RootFolderKindMovie, "hd-bluray-web", catalogv1alpha1.ScanModeIncremental)
	heat := f.plantCandidate(t, ctx, "inside/Heat (1995)/heat.1995.1080p.bluray.x264-grp.mkv", "Heat (1995) [Bluray-2160p].mkv")
	ronin := f.plantCandidate(t, ctx, "inside/Ronin (1998)/ronin.1998.1080p.bluray.x264-grp.mkv", "Ronin (1998) [Bluray-2160p].mkv")
	alien := f.plantCandidate(t, ctx, "outside/Alien (1979)/alien.1979.1080p.bluray.x264-grp.mkv", "Alien (1979) [Bluray-2160p].mkv")

	requireUntouched := func(t *testing.T, sf staleFile) {
		t.Helper()
		requireExists(t, sf.path)
		requireAbsent(t, sf.expected)
		assert.Equal(t, sf.path, f.read(t, ctx, sf.name).Spec.Path)
	}

	// A dry run.
	scan, msg := f.renameScan(t, ctx, "rename-dry", "inside", catalogv1alpha1.ScanRenameDryRun, false)
	require.NoError(t, f.renameWorker(t).Handle(ctx, msg))
	progress := readProgress(t, ctx, f.bus, string(scan.UID))
	assert.Equal(t, int64(2), progress.FilesSeen, "the walk ran over the subpath first")
	assert.Equal(t, int64(2), progress.Unchanged)
	status := f.settle(t, ctx, scan)
	assert.Equal(t, []catalogv1alpha1.RenamedFile{
		{From: heat.path, To: heat.expected, Reason: rescan.RenameDryRun},
		{From: ronin.path, To: ronin.expected, Reason: rescan.RenameDryRun},
	}, status.Renamed)
	assert.Zero(t, status.FilesRenamed)
	assert.Equal(t, int64(2), status.FilesSeen)
	for _, sf := range []staleFile{heat, ronin, alien} {
		requireUntouched(t, sf)
	}

	// A scan that is itself a dry run never moves a file, whatever its
	// rename pass says.
	scan, msg = f.renameScan(t, ctx, "rename-dry-scan", "inside", catalogv1alpha1.ScanRenameApply, true)
	require.NoError(t, f.renameWorker(t).Handle(ctx, msg))
	status = f.settle(t, ctx, scan)
	require.Len(t, status.Renamed, 2)
	for _, r := range status.Renamed {
		assert.Equal(t, rescan.RenameDryRun, r.Reason, r.From)
	}
	assert.Zero(t, status.FilesRenamed)
	for _, sf := range []staleFile{heat, ronin, alien} {
		requireUntouched(t, sf)
	}

	// Apply.
	scan, msg = f.renameScan(t, ctx, "rename-apply", "inside", catalogv1alpha1.ScanRenameApply, false)
	require.NoError(t, f.renameWorker(t).Handle(ctx, msg))
	status = f.settle(t, ctx, scan)
	assert.Equal(t, []catalogv1alpha1.RenamedFile{
		{From: heat.path, To: heat.expected},
		{From: ronin.path, To: ronin.expected},
	}, status.Renamed)
	assert.Equal(t, int64(2), status.FilesRenamed)
	assert.Equal(t, int64(2), status.FilesSeen, "the walk saw the files at their old names, before the pass moved them")
	for _, sf := range []staleFile{heat, ronin} {
		requireAbsent(t, sf.path)
		requireExists(t, sf.expected)
		assert.Equal(t, sf.expected, f.read(t, ctx, sf.name).Spec.Path)
	}
	requireUntouched(t, alien)
}

// One candidate the pass cannot rename -- its file is gone, so RenameFile
// reports an error -- is recorded with the error as its reason, and the
// pass carries on to the next.
func TestLibraryScanRenamePassRecordsAFailureAndCarriesOn(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx, "rn-scan-err", catalogv1alpha1.RootFolderKindMovie, "hd-bluray-web", catalogv1alpha1.ScanModeIncremental)
	gone := f.plantCandidate(t, ctx, "Alien (1979)/alien.1979.1080p.bluray.x264-grp.mkv", "Alien (1979) [Bluray-2160p].mkv")
	heat := f.plantCandidate(t, ctx, "Heat (1995)/heat.1995.1080p.bluray.x264-grp.mkv", "Heat (1995) [Bluray-2160p].mkv")
	require.NoError(t, os.Remove(gone.path))

	scan, msg := f.renameScan(t, ctx, "rename-apply", "", catalogv1alpha1.ScanRenameApply, false)
	require.NoError(t, f.renameWorker(t).Handle(ctx, msg))
	status := f.settle(t, ctx, scan)

	require.Len(t, status.Renamed, 2)
	failed := status.Renamed[0]
	assert.Equal(t, gone.path, failed.From)
	assert.Equal(t, gone.expected, failed.To)
	assert.True(t, strings.HasPrefix(failed.Reason, rescan.RenameFailed+": "), failed.Reason)
	assert.Contains(t, failed.Reason, "no such file")
	assert.Equal(t, catalogv1alpha1.RenamedFile{From: heat.path, To: heat.expected}, status.Renamed[1])
	assert.Equal(t, int64(1), status.FilesRenamed)
	requireExists(t, heat.expected)
}

// A scan whose spec.rename is unset -- the CRD defaults it to off -- runs no
// rename pass: the walk's tally is what it always was, nothing is listed,
// and a rename candidate stays where it is.
func TestLibraryScanWithoutRenameRunsNoRenamePass(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx, "rn-scan-off", catalogv1alpha1.RootFolderKindMovie, "hd-bluray-web", catalogv1alpha1.ScanModeIncremental)
	heat := f.plantCandidate(t, ctx, "Heat (1995)/heat.1995.1080p.bluray.x264-grp.mkv", "Heat (1995) [Bluray-2160p].mkv")

	scan, msg := f.renameScan(t, ctx, "no-rename", "", "", false)
	require.NoError(t, f.renameWorker(t).Handle(ctx, msg))
	progress := readProgress(t, ctx, f.bus, string(scan.UID))
	assert.Equal(t, scanprogress.Progress{
		Done: true, FilesSeen: 1, FilesSkipped: 1, Unchanged: 1,
	}, progress)
	status := f.settle(t, ctx, scan)
	assert.Empty(t, status.Renamed)
	assert.Zero(t, status.FilesRenamed)
	requireExists(t, heat.path)
	requireAbsent(t, heat.expected)
}
