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
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/importarr/worker/rescan"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// threeFilms plants three films, each carrying a tmdb id, so a walk that
// reaches one creates its Movie. Returned in walk order.
func threeFilms(t *testing.T, f *fixture) []string {
	t.Helper()
	var paths []string
	for i, title := range []string{"Alpha (2001)", "Beta (2002)", "Gamma (2003)"} {
		dir := title + " [tmdbid-" + string(rune('1'+i)) + "]"
		p := filepath.Join(f.root, dir, dir+".mkv")
		mustWriteFile(t, p, sampleFloor)
		paths = append(paths, p)
	}
	return paths
}

func (f *fixture) putCheckpoint(t *testing.T, ctx context.Context, p rescan.Progress) {
	t.Helper()
	data, err := p.Encode()
	require.NoError(t, err)
	_, err = f.bus.KV(events.BucketProgress).Put(ctx, rescan.ProgressKey(string(f.scan.UID)), data)
	require.NoError(t, err)
}

func movieCount(t *testing.T, ctx context.Context, c client.Client, ns string) int {
	t.Helper()
	var movies catalogv1alpha1.MovieList
	require.NoError(t, c.List(ctx, &movies, client.InNamespace(ns)))
	return len(movies.Items)
}

// A redelivered scan task resumes from the last checkpoint: the files whose
// outcome that checkpoint already counts are passed over -- not walked and
// not counted a second time -- and the tally carries on from the
// checkpoint's numbers rather than from zero, which is what drove the
// LibraryScan's counters backwards on every redelivery.
func TestHandleResumesARedeliveredScanFromItsCheckpoint(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx, "rw-resume", catalogv1alpha1.RootFolderKindMovie, "hd-bluray-web", catalogv1alpha1.ScanModeFull)
	films := threeFilms(t, f)

	// The first delivery got through two films before it died.
	f.putCheckpoint(t, ctx, rescan.Progress{
		FilesSeen: 2, FilesMatched: 2, ItemsCreated: 2, Resume: films[1],
		Unmatched: []rescan.UnmatchedFile{{Path: "carried.mkv", Reason: "from the first delivery"}},
	})

	msg := newFakeMessage(t, f.task(false))
	msg.attempt = 2
	require.NoError(t, rescan.NewWorker(f.c, f.bus).Handle(ctx, msg))

	got := readProgress(t, ctx, f.bus, string(f.scan.UID))
	require.True(t, got.Done)
	assert.Equal(t, int64(3), got.FilesSeen, "two carried and one walked, not three walked again on top")
	assert.Equal(t, int64(3), got.FilesMatched)
	assert.Equal(t, int64(3), got.ItemsCreated)
	assert.Empty(t, got.Resume, "the final tally has nothing to resume from")
	assert.Contains(t, unmatchedByPath(got), "carried.mkv", "the carried unmatched list survives")
	waitFor(t, 10*time.Second, func() bool { return movieCount(t, ctx, f.c, f.ns) == 1 })
	assert.Equal(t, 1, movieCount(t, ctx, f.c, f.ns), "only the film after the checkpoint was walked")
}

// A redelivery of a walk that already reported its final tally -- the ack
// was lost -- is a no-op: walking again would overwrite that tally with a
// partial one on the way to the same result.
func TestHandleARedeliveryAfterTheFinalCheckpointIsANoOp(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx, "rw-done", catalogv1alpha1.RootFolderKindMovie, "hd-bluray-web", catalogv1alpha1.ScanModeFull)
	threeFilms(t, f)
	f.putCheckpoint(t, ctx, rescan.Progress{Done: true, FilesSeen: 7, FilesMatched: 7})

	msg := newFakeMessage(t, f.task(false))
	msg.attempt = 2
	require.NoError(t, rescan.NewWorker(f.c, f.bus).Handle(ctx, msg))

	got := readProgress(t, ctx, f.bus, string(f.scan.UID))
	assert.Equal(t, int64(7), got.FilesSeen, "the final tally stands")
	assert.Zero(t, movieCount(t, ctx, f.c, f.ns), "nothing was walked")
}

// A scan the controller has settled -- failed for want of progress, or
// because this very task was dead-lettered -- is not walked by a late
// delivery: there is nobody left to report the result to.
func TestHandleDiscardsATaskForASettledScan(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx, "rw-settled", catalogv1alpha1.RootFolderKindMovie, "hd-bluray-web", catalogv1alpha1.ScanModeFull)
	threeFilms(t, f)
	_, err := k8s.PatchStatus(ctx, f.c, k8s.ManagerImportarr, catalogac.LibraryScan(f.scan.Name, f.ns).
		WithStatus(catalogac.LibraryScanStatus().WithPhase(catalogv1alpha1.ScanPhaseFailed)))
	require.NoError(t, err)
	waitCached(t, ctx, f.c, f.scan, func() bool { return f.scan.Status.Phase == catalogv1alpha1.ScanPhaseFailed })

	assertDiscarded(t, rescan.NewWorker(f.c, f.bus).Handle(ctx, newFakeMessage(t, f.task(false))))
	assert.Zero(t, movieCount(t, ctx, f.c, f.ns))
}

// One folder the walk cannot list no longer aborts the whole scan: it is
// listed as unmatched with the filesystem's error, and the films before and
// after it are attributed.
func TestHandleCarriesOnPastAnUnreadableFolder(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode-0 directory anyway")
	}
	ctx := context.Background()
	f := newFixture(t, ctx, "rw-unreadable", catalogv1alpha1.RootFolderKindMovie, "hd-bluray-web", catalogv1alpha1.ScanModeFull)
	films := threeFilms(t, f)
	locked := filepath.Dir(films[1])
	require.NoError(t, os.Chmod(locked, 0))
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	require.NoError(t, rescan.NewWorker(f.c, f.bus).Handle(ctx, newFakeMessage(t, f.task(false))))

	got := readProgress(t, ctx, f.bus, string(f.scan.UID))
	require.True(t, got.Done)
	require.Empty(t, got.Error, "one unreadable folder is not a failed scan")
	assert.Equal(t, int64(2), got.FilesMatched, "the films on either side of it")
	assert.Equal(t, int64(1), got.Unreadable)
	entry, ok := unmatchedByPath(got)[filepath.Base(locked)]
	require.True(t, ok, "the unreadable folder is listed: %+v", got.Unmatched)
	assert.Contains(t, entry.Reason, "could not be read")
	assert.Contains(t, entry.Reason, "permission denied")
}

// A person who assigns a FOLDER assigns the release in it; a small video
// beside the real one there is its promo clip, and is left behind -- listed
// as unmatched, with the remedy -- rather than swept into the movie. A folder
// whose only video is the small one is what the person assigned, and it is
// taken.
func TestHandleManualFolderAssignmentLeavesThePromoClipBehind(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx, "rw-promo", catalogv1alpha1.RootFolderKindMovie, "hd-bluray-web", catalogv1alpha1.ScanModeFull)
	movie := &catalogv1alpha1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: "unsorted-film", Namespace: f.ns},
		Spec:       catalogv1alpha1.MovieSpec{TmdbID: 42, QualityProfileRef: "hd-bluray-web", RootFolderRef: f.rf.Name},
	}
	require.NoError(t, f.c.Create(ctx, movie))
	waitCached(t, ctx, f.c, movie, func() bool { return true })

	mustWriteFile(t, filepath.Join(f.root, "Unsorted", "feature.mkv"), sampleFloor)
	promo := filepath.Join("Unsorted", "promo.mkv")
	mustWriteFile(t, filepath.Join(f.root, promo), 10<<20)
	mustWriteFile(t, filepath.Join(f.root, "Short", "short-film.mkv"), shortFilmBytes)

	scan, msg := f.assignScan(t, ctx, "assign-folder", "movie/"+movie.Name, "Unsorted")
	require.NoError(t, rescan.NewWorker(f.c, f.bus).Handle(ctx, msg))
	got := readProgress(t, ctx, f.bus, string(scan.UID))
	require.Empty(t, got.Error)
	assert.Equal(t, int64(1), got.FilesMatched, "the feature")
	entry, ok := unmatchedByPath(got)[promo]
	require.True(t, ok, "the promo clip is left behind and listed")
	assert.Contains(t, entry.Reason, "left behind by this manual assignment")
	files := mediaFilesIn(t, ctx, f.c, f.ns, 1)
	assert.Equal(t, filepath.Join(f.root, "Unsorted", "feature.mkv"), files[0].Spec.Path)

	scan2, msg2 := f.assignScan(t, ctx, "assign-short", "movie/"+movie.Name, "Short")
	require.NoError(t, rescan.NewWorker(f.c, f.bus).Handle(ctx, msg2))
	got2 := readProgress(t, ctx, f.bus, string(scan2.UID))
	assert.Empty(t, got2.Unmatched, "a folder whose only video is small is what the person assigned")
	assert.Equal(t, int64(1), got2.FilesMatched)
}
