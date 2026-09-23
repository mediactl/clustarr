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
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/importarr/worker/rescan"
	"github.com/mediactl/clustarr/pkg/fsops"
)

// shortFilmBytes is a real short film's size: under fsops'
// DefaultSampleMaxBytes, so the size floor alone suspects it is a sample.
const shortFilmBytes = 45 << 20

// plantShortFilm plants a 45 MiB film carrying its own tmdb id -- a file the
// walk would attribute with full confidence, and create a Movie for, if the
// size floor did not intervene -- beside the release's own name-marked
// sample, and returns both paths relative to the root folder.
func plantShortFilm(t *testing.T, f *fixture) (film, sample string) {
	t.Helper()
	dir := "World of Tomorrow (2015) [tmdbid-326215]"
	film = filepath.Join(dir, "World of Tomorrow (2015) [tmdbid-326215].mkv")
	sample = filepath.Join(dir, "world.of.tomorrow.2015.sample.mkv")
	mustWriteFile(t, filepath.Join(f.root, film), shortFilmBytes)
	mustWriteFile(t, filepath.Join(f.root, sample), 10<<20)
	return film, sample
}

// The scanner never guesses, and a size is a guess: a video file only the
// size floor flags is recorded as unmatched with the reason -- naming its
// size and the threshold -- and never silently skipped, and never
// attributed either. A file whose NAME marks it a sample is still skipped
// without a listing.
func TestHandleRecordsASuspectedSampleAsUnmatched(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx, "rw-suspect", catalogv1alpha1.RootFolderKindMovie, "hd-bluray-web", catalogv1alpha1.ScanModeFull)
	film, sample := plantShortFilm(t, f)

	w := rescan.NewWorker(f.c, f.bus)
	require.Equal(t, fsops.DefaultSampleMaxBytes, w.SampleMaxBytes, "NewWorker keeps the 50 MiB default")
	require.NoError(t, w.Handle(ctx, newFakeMessage(t, f.task(false))))

	got := readProgress(t, ctx, f.bus, string(f.scan.UID))
	require.True(t, got.Done)
	require.Empty(t, got.Error)
	unmatched := unmatchedByPath(got)
	require.Contains(t, unmatched, film, "a small video must be surfaced, never silently skipped")
	reason := unmatched[film].Reason
	assert.Contains(t, reason, "suspected sample")
	assert.Contains(t, reason, "45.0 MiB (47185920 bytes)", "the reason names the file's size")
	assert.Contains(t, reason, "50.0 MiB (52428800 bytes)", "and the threshold it fell under")
	assert.Contains(t, reason, "assign it by hand", "and what a person can do about it")
	assert.Empty(t, unmatched[film].Candidates)
	assert.NotContains(t, unmatched, sample, "a name-marked sample is skipped, not listed")
	assert.Len(t, got.Unmatched, 1)

	assert.Equal(t, int64(1), got.FilesSeen, "the suspected sample was considered, as every unmatched file is")
	assert.Equal(t, int64(1), got.FilesSkipped, "only the name-marked sample was skipped")
	assert.Zero(t, got.FilesMatched, "a size-suspected file is never attributed")
	assert.Zero(t, got.ItemsCreated, "not even with a tmdb id in its name")
}

// SampleMaxBytes 0 turns the size floor off: the same film is attributed --
// its Movie created from the tmdb id it carries -- and the name-marked
// sample is still skipped, because that is a different rule.
func TestHandleSampleMaxBytesZeroDisablesTheSizeRule(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx, "rw-suspect-off", catalogv1alpha1.RootFolderKindMovie, "hd-bluray-web", catalogv1alpha1.ScanModeFull)
	film, _ := plantShortFilm(t, f)

	w := rescan.NewWorker(f.c, f.bus)
	w.SampleMaxBytes = 0
	require.NoError(t, w.Handle(ctx, newFakeMessage(t, f.task(false))))

	got := readProgress(t, ctx, f.bus, string(f.scan.UID))
	require.Empty(t, got.Error)
	assert.Empty(t, got.Unmatched)
	assert.Equal(t, int64(1), got.FilesSeen)
	assert.Equal(t, int64(1), got.FilesMatched)
	assert.Equal(t, int64(1), got.ItemsCreated)
	assert.Equal(t, int64(1), got.FilesSkipped, "the name rule is not the size rule")

	mf := mediaFilesIn(t, ctx, f.c, f.ns, 1)[0]
	assert.Equal(t, filepath.Join(f.root, film), mf.Spec.Path)
	assert.Equal(t, int64(shortFilmBytes), mf.Spec.SizeBytes)
}

// The unmatched listing is only honest if its remedy works: assigning the
// suspected sample by hand records it, because a size does not overrule a
// person. Once a MediaFile records it, a later scan refreshes it like any
// other attributed file instead of listing it as unmatched forever.
func TestHandleSuspectedSampleCanBeAssignedByHand(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx, "rw-suspect-assign", catalogv1alpha1.RootFolderKindMovie, "hd-bluray-web", catalogv1alpha1.ScanModeFull)
	film, _ := plantShortFilm(t, f)

	movie := &catalogv1alpha1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: "world-of-tomorrow", Namespace: f.ns},
		Spec:       catalogv1alpha1.MovieSpec{TmdbID: 326215, QualityProfileRef: "hd-bluray-web", RootFolderRef: f.rf.Name},
	}
	require.NoError(t, f.c.Create(ctx, movie))
	waitCached(t, ctx, f.c, movie, func() bool { return true })

	require.NoError(t, rescan.NewWorker(f.c, f.bus).Handle(ctx, newFakeMessage(t, f.task(false))))
	first := readProgress(t, ctx, f.bus, string(f.scan.UID))
	require.Contains(t, unmatchedByPath(first), film, "setup: the film is listed as a suspected sample")
	assert.Zero(t, first.FilesMatched, "an existing Movie with the file's tmdb id does not overrule the size floor either")

	scan, msg := f.assignScan(t, ctx, "assign-short", "movie/"+movie.Name, film)
	require.NoError(t, rescan.NewWorker(f.c, f.bus).Handle(ctx, msg))

	assigned := readProgress(t, ctx, f.bus, string(scan.UID))
	require.Empty(t, assigned.Error)
	assert.Empty(t, assigned.Unmatched, "a manual assignment takes a suspected sample")
	assert.Equal(t, int64(1), assigned.FilesMatched)

	mf := mediaFilesIn(t, ctx, f.c, f.ns, 1)[0]
	assert.Equal(t, commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: movie.Name}, mf.Spec.MediaRef)
	assert.Equal(t, filepath.Join(f.root, film), mf.Spec.Path)
	require.NotNil(t, mf.Spec.ImportedFrom)
	assert.True(t, mf.Spec.ImportedFrom.Manual)

	// The next scheduled scan: the MediaFile is the file's attribution.
	require.NoError(t, rescan.NewWorker(f.c, f.bus).Handle(ctx, newFakeMessage(t, f.task(false))))
	again := readProgress(t, ctx, f.bus, string(f.scan.UID))
	require.Empty(t, again.Error)
	assert.Empty(t, again.Unmatched, "an attributed file is not a suspected sample")
	assert.Equal(t, int64(1), again.FilesMatched)
	assert.Equal(t, int64(1), again.ItemsUpdated)
}
