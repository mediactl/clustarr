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

package fileimport_test

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
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// seriesFixture is a series root, a Series with metadata and three
// episodes of its first season.
type seriesFixture struct {
	*fixture
	root   *catalogv1alpha1.RootFolder
	series string
}

func newSeriesFixture(t *testing.T, ns string) *seriesFixture {
	t.Helper()
	ctx := context.Background()
	f := newFixture(t, ns)
	rf := &catalogv1alpha1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Name: "shows", Namespace: f.ns},
		Spec: catalogv1alpha1.RootFolderSpec{
			Path: dataDir(t, "media"), Kind: catalogv1alpha1.RootFolderKindSeries,
			RecycleBin: catalogv1alpha1.RecycleBin{Path: dataDir(t, "recycle")},
		},
	}
	require.NoError(t, f.c.Create(ctx, rf))
	series := &catalogv1alpha1.Series{
		ObjectMeta: metav1.ObjectMeta{Name: "breaking-bad", Namespace: f.ns},
		Spec:       catalogv1alpha1.SeriesSpec{TvdbID: 81189, QualityProfileRef: f.profile.Name, RootFolderRef: rf.Name},
	}
	require.NoError(t, f.c.Create(ctx, series))
	_, err := k8s.PatchStatus(ctx, f.c, k8s.ManagerCatalogarrMetadata, catalogac.Series(series.Name, f.ns).WithStatus(
		catalogac.SeriesStatus().WithMetadata(catalogac.SeriesMetadata().WithTitle("Breaking Bad").WithYear(2008))))
	require.NoError(t, err)
	for i, title := range []string{"Pilot", "Cat's in the Bag...", "...And the Bag's in the River"} {
		name := "breaking-bad-s01e0" + string(rune('1'+i))
		require.NoError(t, f.c.Create(ctx, &catalogv1alpha1.Episode{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: f.ns},
			Spec:       catalogv1alpha1.EpisodeSpec{SeriesRef: series.Name, SeasonNumber: 1, EpisodeNumber: int32(i + 1)},
		}))
		_, err := k8s.PatchStatus(ctx, f.c, k8s.ManagerCatalogarr, catalogac.Episode(name, f.ns).
			WithStatus(catalogac.EpisodeStatus().WithTitle(title)))
		require.NoError(t, err)
	}
	waitFor(t, 5*time.Second, func() bool {
		var s catalogv1alpha1.Series
		var eps catalogv1alpha1.EpisodeList
		return f.c.Get(ctx, client.ObjectKeyFromObject(series), &s) == nil && s.Status.Metadata != nil &&
			f.c.List(ctx, &eps, client.InNamespace(f.ns)) == nil && len(eps.Items) == 3 &&
			eps.Items[2].Status.Title != "" &&
			f.c.Get(ctx, client.ObjectKeyFromObject(rf), &catalogv1alpha1.RootFolder{}) == nil
	})
	return &seriesFixture{fixture: f, root: rf, series: series.Name}
}

func (s *seriesFixture) importTo(t *testing.T, name, contentRoot string, target commonv1.MediaRef) *downloadv1alpha1.ImportState {
	t.Helper()
	dl := s.createDownloadWith(t, name, contentRoot, target, "", nil)
	require.NoError(t, s.worker.Handle(context.Background(), newImportTaskMessage(t, s.ns, dl.Name, "")))
	return s.importState(t, dl).Status.Import
}

func (s *seriesFixture) mediaFile(t *testing.T, name string) catalogv1alpha1.MediaFile {
	t.Helper()
	var mf catalogv1alpha1.MediaFile
	require.NoError(t, s.api.Get(context.Background(), client.ObjectKey{Namespace: s.ns, Name: name}, &mf))
	return mf
}

// The carried "Episode import is Ignored": a single episode's download is
// imported into the series' folder under its season folder, named by
// pkg/naming's episode preset, and recorded against that Episode.
func TestHandleImportsAnEpisode(t *testing.T) {
	s := newSeriesFixture(t, "fi-episode")
	contentRoot := dataDir(t, "scratch")
	mustWriteSparseFile(t, filepath.Join(contentRoot, "Breaking.Bad.S01E03.1080p.WEB-DL.DD5.1.H.264-GRP.mkv"), sampleFloor)

	got := s.importTo(t, "ep-dl", contentRoot, commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: "breaking-bad-s01e03"})
	require.Equal(t, downloadv1alpha1.ImportPhaseImported, got.State, "message %q, rejections %v", got.Message, got.Rejections)
	require.Len(t, got.Imported, 1)

	mf := s.mediaFile(t, got.Imported[0].MediaFileRef)
	assert.Equal(t, commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: "breaking-bad-s01e03"}, mf.Spec.MediaRef)
	// pkg/naming's Jellyfin series preset carries the TheTVDB id, and its
	// clean title drops the apostrophe.
	assert.Equal(t, filepath.Join(s.root.Spec.Path, "Breaking Bad (2008) [tvdbid-81189]", "Season 01",
		"Breaking Bad (2008) - S01E03 - ...And the Bags in the River [WEBDL-1080p]-GRP.mkv"), mf.Spec.Path)
	assert.Equal(t, "WEBDL-1080p", mf.Spec.Quality.Name)
	assert.NotEmpty(t, mf.Spec.ProfileHash)
	_, err := os.Stat(mf.Spec.Path)
	require.NoError(t, err)
}

// A season pack's download names the Series in spec.target and the
// episodes in keys. Each file goes to the episode its name numbers; a file
// holding two episodes is ONE MediaFile naming both in keys; a file naming
// an episode the series does not have is a rejection. A later, better file
// for an episode the multi-episode file held replaces it.
func TestHandleImportsASeasonPack(t *testing.T) {
	s := newSeriesFixture(t, "fi-pack")
	pack := dataDir(t, "scratch")
	dir := filepath.Join(pack, "Breaking.Bad.S01.720p.BluRay.x264-GRP")
	mustWriteSparseFile(t, filepath.Join(dir, "Breaking.Bad.S01E01E02.1080p.BluRay.x264-GRP.mkv"), sampleFloor)
	mustWriteSparseFile(t, filepath.Join(dir, "Breaking.Bad.S01E03.1080p.BluRay.x264-GRP.mkv"), sampleFloor)
	mustWriteSparseFile(t, filepath.Join(dir, "Breaking.Bad.S01E09.1080p.BluRay.x264-GRP.mkv"), sampleFloor)

	target := commonv1.MediaRef{
		Kind: commonv1.MediaKindSeries, Name: s.series,
		Keys: []string{"breaking-bad-s01e01", "breaking-bad-s01e02", "breaking-bad-s01e03"},
	}
	got := s.importTo(t, "pack-dl", pack, target)
	require.Equal(t, downloadv1alpha1.ImportPhaseImported, got.State, "message %q, rejections %v", got.Message, got.Rejections)
	require.Len(t, got.Imported, 2)
	require.Len(t, got.Rejections, 1)
	assert.Contains(t, got.Rejections[0], "the series has no S01E09")

	multi := s.mediaFile(t, got.Imported[0].MediaFileRef)
	assert.Equal(t, commonv1.MediaRef{
		Kind: commonv1.MediaKindEpisode, Name: "breaking-bad-s01e01",
		Keys: []string{"breaking-bad-s01e01", "breaking-bad-s01e02"},
	}, multi.Spec.MediaRef)
	// pkg/naming names every episode the file holds, in the root folder's
	// multi-episode style (prefixedRange by default).
	assert.Contains(t, multi.Spec.Path, "Breaking Bad (2008) - S01E01-E02 - Pilot")
	waitFor(t, 5*time.Second, func() bool {
		var list catalogv1alpha1.MediaFileList
		return s.c.List(context.Background(), &list, client.InNamespace(s.ns)) == nil && len(list.Items) == 2
	})

	// A REMUX would not be allowed; a WEB-DL of the same 1080p tier with a
	// PROPER is an upgrade by revision over the BluRay's episode 2 -- and
	// it replaces the multi-episode file that held episode 2.
	better := dataDir(t, "scratch")
	mustWriteSparseFile(t, filepath.Join(better, "Breaking.Bad.S01E02.PROPER.1080p.BluRay.x264-GRP.mkv"), sampleFloor)
	got = s.importTo(t, "better-dl", better, commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: "breaking-bad-s01e02"})
	require.Equal(t, downloadv1alpha1.ImportPhaseImported, got.State, "message %q, rejections %v", got.Message, got.Rejections)
	var gone catalogv1alpha1.MediaFile
	err := s.api.Get(context.Background(), client.ObjectKeyFromObject(&multi), &gone)
	assert.True(t, err != nil, "the replaced multi-episode file's MediaFile is deleted")
	_, statErr := os.Stat(multi.Spec.Path)
	assert.ErrorIs(t, statErr, os.ErrNotExist, "and its file recycled")
}

// Never guess: an episode target that does not exist is Blocked with the
// reason, and no MediaFile is made.
func TestHandleBlocksAnEpisodeImportWithNoEpisode(t *testing.T) {
	s := newSeriesFixture(t, "fi-no-episode")
	contentRoot := dataDir(t, "scratch")
	mustWriteSparseFile(t, filepath.Join(contentRoot, "Breaking.Bad.S01E01.1080p.WEB-DL-GRP.mkv"), sampleFloor)
	got := s.importTo(t, "nope-dl", contentRoot, commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: "does-not-exist"})
	assert.Equal(t, downloadv1alpha1.ImportPhaseBlocked, got.State)
	assert.Contains(t, got.Message, `episode "does-not-exist" does not exist`)
	var list catalogv1alpha1.MediaFileList
	require.NoError(t, s.api.List(context.Background(), &list, client.InNamespace(s.ns)))
	assert.Empty(t, list.Items)
}
