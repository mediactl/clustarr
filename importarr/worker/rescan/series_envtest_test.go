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
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/importarr/worker/rescan"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// createSeries creates a Series under the fixture's root folder with
// metadata and episodes 1..n of season 1, named "<name>-s01eNN".
func createSeries(t *testing.T, ctx context.Context, f *fixture, name, title string, year int32, tvdb int64, n int) {
	t.Helper()
	s := &catalogv1alpha1.Series{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: f.ns},
		Spec:       catalogv1alpha1.SeriesSpec{TvdbID: tvdb, QualityProfileRef: "hd-bluray-web", RootFolderRef: f.rf.Name},
	}
	require.NoError(t, f.c.Create(ctx, s))
	_, err := k8s.PatchStatus(ctx, f.c, k8s.ManagerCatalogarrMetadata, catalogac.Series(name, f.ns).WithStatus(
		catalogac.SeriesStatus().WithMetadata(catalogac.SeriesMetadata().WithTitle(title).WithYear(year))))
	require.NoError(t, err)
	for i := 1; i <= n; i++ {
		require.NoError(t, f.c.Create(ctx, &catalogv1alpha1.Episode{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-s01e%02d", name, i), Namespace: f.ns},
			Spec:       catalogv1alpha1.EpisodeSpec{SeriesRef: name, SeasonNumber: 1, EpisodeNumber: int32(i)},
		}))
	}
	waitFor(t, 10*time.Second, func() bool {
		var got catalogv1alpha1.Series
		var eps catalogv1alpha1.EpisodeList
		if f.c.Get(ctx, client.ObjectKeyFromObject(s), &got) != nil || got.Status.Metadata == nil ||
			f.c.List(ctx, &eps, client.InNamespace(f.ns)) != nil {
			return false
		}
		count := 0
		for _, e := range eps.Items {
			if e.Spec.SeriesRef == name {
				count++
			}
		}
		return count == n
	})
}

// The carried "a Series root folder rescan reports unsupported_root_kind":
// a file under a series root is attributed to an EXISTING series by its
// folder -- the series' title and year, or a TheTVDB id in the folder name
// -- and to its episode by the numbering its name carries; a file holding
// two episodes is one MediaFile naming both. What the layout or the
// numbering does not settle is reported, never guessed.
func TestHandleAttributesEpisodeFilesUnderASeriesRoot(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx, "rw-series", catalogv1alpha1.RootFolderKindSeries, "", catalogv1alpha1.ScanModeFull)
	createSeries(t, ctx, f, "breaking-bad", "Breaking Bad", 2008, 81189, 2)
	createSeries(t, ctx, f, "the-wire", "The Wire", 2002, 79126, 2)

	pilot := filepath.Join("Breaking Bad (2008)", "Season 01", "Breaking Bad (2008) - S01E01 - Pilot [WEBDL-1080p].mkv")
	wire := filepath.Join("Baltimore [tvdbid-79126]", "The.Wire.S01E01E02.1080p.BluRay.x264-GRP.mkv")
	missing := filepath.Join("Breaking Bad (2008)", "Season 01", "Breaking Bad - S01E07.mkv")
	unknown := filepath.Join("Unknown Show (2020)", "Unknown.Show.S01E01.720p.HDTV.x264-GRP.mkv")
	unknownID := filepath.Join("Somewhere [tvdbid-1]", "Show.S01E01.720p.HDTV.x264-GRP.mkv")
	for _, rel := range []string{pilot, wire, missing, unknown, unknownID} {
		mustWriteFile(t, filepath.Join(f.root, rel), sampleFloor)
	}

	require.NoError(t, rescan.NewWorker(f.c, f.bus).Handle(ctx, newFakeMessage(t, f.task(false))))
	got := readProgress(t, ctx, f.bus, string(f.scan.UID))
	require.Empty(t, got.Error)
	assert.Equal(t, int64(5), got.FilesSeen)
	assert.Equal(t, int64(2), got.FilesMatched)
	assert.Zero(t, got.ItemsCreated, "a rescan never creates a series")

	unmatched := unmatchedByPath(got)
	require.Len(t, unmatched, 3)
	assert.Contains(t, unmatched[missing].Reason, "the series has no S01E07")
	assert.Contains(t, unmatched[unknown].Reason, "no existing series titled")
	assert.Contains(t, unmatched[unknownID].Reason, "TheTVDB id 1")

	files := mediaFilesIn(t, ctx, f.c, f.ns, 2)
	byPath := map[string]catalogv1alpha1.MediaFile{}
	for _, mf := range files {
		byPath[mf.Spec.Path] = mf
	}
	assert.Equal(t, commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: "breaking-bad-s01e01"},
		byPath[filepath.Join(f.root, pilot)].Spec.MediaRef)
	assert.Equal(t, "WEBDL-1080p", byPath[filepath.Join(f.root, pilot)].Spec.Quality.Name)
	assert.Equal(t, commonv1.MediaRef{
		Kind: commonv1.MediaKindEpisode, Name: "the-wire-s01e01",
		Keys: []string{"the-wire-s01e01", "the-wire-s01e02"},
	}, byPath[filepath.Join(f.root, wire)].Spec.MediaRef)
}

// A file whose name carries no numbering is assigned to an episode by hand,
// like any unmatched file: the import-target names the episode.
func TestHandleManualAssignmentToAnEpisode(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx, "rw-series-assign", catalogv1alpha1.RootFolderKindSeries, "", catalogv1alpha1.ScanModeFull)
	createSeries(t, ctx, f, "breaking-bad", "Breaking Bad", 2008, 81189, 2)
	rel := filepath.Join("Breaking Bad (2008)", "bonus", "the lost episode.mkv")
	mustWriteFile(t, filepath.Join(f.root, rel), sampleFloor)

	scan, msg := f.assignScan(t, ctx, "assign-ep", "series/breaking-bad/breaking-bad-s01e02", rel)
	require.NoError(t, rescan.NewWorker(f.c, f.bus).Handle(ctx, msg))
	got := readProgress(t, ctx, f.bus, string(scan.UID))
	require.Empty(t, got.Error)
	assert.Equal(t, int64(1), got.FilesMatched)
	mf := mediaFilesIn(t, ctx, f.c, f.ns, 1)[0]
	assert.Equal(t, commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: "breaking-bad-s01e02"}, mf.Spec.MediaRef)
	require.NotNil(t, mf.Spec.ImportedFrom)
	assert.True(t, mf.Spec.ImportedFrom.Manual)

	// An episode of another series is refused, not followed.
	createSeries(t, ctx, f, "the-wire", "The Wire", 2002, 79126, 1)
	scan2, msg2 := f.assignScan(t, ctx, "assign-wrong", "series/breaking-bad/the-wire-s01e01", rel)
	require.NoError(t, rescan.NewWorker(f.c, f.bus).Handle(ctx, msg2))
	assert.Contains(t, readProgress(t, ctx, f.bus, string(scan2.UID)).Error, "belongs to series \"the-wire\"")
}
