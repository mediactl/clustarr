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
	"os/exec"
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
	"github.com/mediactl/clustarr/importarr/worker/fileimport"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// albumFixture is a music root with its own recycle bin, and an album with
// metadata under an artist, whose selected release lists tracks (recording
// MBIDs). qualities are the music profile's one tier.
type albumFixture struct {
	*fixture
	root  *catalogv1alpha1.RootFolder
	bin   string
	album *catalogv1alpha1.Album
}

func newAlbumFixture(t *testing.T, ns string, tracks []string, qualities ...string) *albumFixture {
	t.Helper()
	ctx := context.Background()
	f := newFixture(t, ns)
	bin := dataDir(t, "recycle")
	rf := &catalogv1alpha1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Name: "music", Namespace: f.ns},
		Spec: catalogv1alpha1.RootFolderSpec{
			Path: dataDir(t, "media"), Kind: catalogv1alpha1.RootFolderKindMusic,
			RecycleBin: catalogv1alpha1.RecycleBin{Path: bin},
		},
	}
	require.NoError(t, f.c.Create(ctx, rf))
	profile := f.nonVideoProfile(t, catalogv1alpha1.ProfileMediaKindMusic, qualities...)

	artist := &catalogv1alpha1.Artist{
		ObjectMeta: metav1.ObjectMeta{Name: "radiohead", Namespace: f.ns},
		Spec:       catalogv1alpha1.ArtistSpec{MusicBrainzID: "a74b1b7f", QualityProfileRef: profile, RootFolderRef: rf.Name},
	}
	require.NoError(t, f.c.Create(ctx, artist))
	_, err := k8s.PatchStatus(ctx, f.c, k8s.ManagerCatalogarrMetadata, catalogac.Artist(artist.Name, f.ns).
		WithStatus(catalogac.ArtistStatus().WithMetadata(catalogac.ArtistMetadata().WithName("Radiohead"))))
	require.NoError(t, err)
	album := &catalogv1alpha1.Album{
		ObjectMeta: metav1.ObjectMeta{Name: "ok-computer", Namespace: f.ns},
		Spec:       catalogv1alpha1.AlbumSpec{ArtistRef: artist.Name, ReleaseGroupID: "b1392450"},
	}
	require.NoError(t, f.c.Create(ctx, album))
	released := metav1.NewTime(time.Date(1997, 5, 21, 0, 0, 0, 0, time.UTC))
	status := catalogac.AlbumStatus().WithMetadata(catalogac.AlbumMetadata().WithTitle("OK Computer").WithReleaseDate(released))
	for i, id := range tracks {
		status = status.WithTracks(catalogac.Track().WithRecordingID(id).WithMedium(1).WithNumber(int32(i + 1)))
	}
	_, err = k8s.PatchStatus(ctx, f.c, k8s.ManagerCatalogarr, catalogac.Album(album.Name, f.ns).WithStatus(status))
	require.NoError(t, err)
	waitFor(t, 5*time.Second, func() bool {
		var a catalogv1alpha1.Album
		var r catalogv1alpha1.RootFolder
		return f.c.Get(ctx, client.ObjectKeyFromObject(album), &a) == nil && a.Status.Metadata != nil &&
			len(a.Status.Tracks) == len(tracks) && f.c.Get(ctx, client.ObjectKeyFromObject(rf), &r) == nil
	})
	return &albumFixture{fixture: f, root: rf, bin: bin, album: album}
}

// importAlbum imports contentRoot to the album, manually or not, and returns
// the import state.
func (a *albumFixture) importAlbum(t *testing.T, name, contentRoot string, manual bool) *downloadv1alpha1.ImportState {
	t.Helper()
	var annotations map[string]string
	if manual {
		annotations = map[string]string{fileimport.AnnotationImportOverride: "true"}
	}
	dl := a.createDownloadWith(t, name, contentRoot,
		commonv1.MediaRef{Kind: commonv1.MediaKindAlbum, Name: a.album.Name}, "", annotations)
	require.NoError(t, a.worker.Handle(context.Background(), newImportTaskMessage(t, a.ns, dl.Name, "")))
	return a.importState(t, dl).Status.Import
}

func (a *albumFixture) mediaFiles(t *testing.T) map[string]catalogv1alpha1.MediaFile {
	t.Helper()
	var list catalogv1alpha1.MediaFileList
	require.NoError(t, a.api.List(context.Background(), &list, client.InNamespace(a.ns)))
	out := map[string]catalogv1alpha1.MediaFile{}
	for _, mf := range list.Items {
		out[filepath.Base(mf.Spec.Path)] = mf
	}
	return out
}

// binFiles lists the base names of everything in the recycle bin's dated
// folders.
func (a *albumFixture) binFiles(t *testing.T) []string {
	t.Helper()
	var out []string
	require.NoError(t, filepath.WalkDir(a.bin, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			out = append(out, filepath.Base(p))
		}
		return err
	}))
	return out
}

// The carried "lossy music has no frozen quality": a lossy track's tier is
// its bitrate, which only the file says, so the import probes it. A CBR
// 320 MP3 is Lidarr's MP3-320, which sits on the High tier -- frozen, and
// imported without a manual override. And the lone file of an album whose
// release has one track is that track (MediaRef.Track).
func TestHandleFreezesAProbedLossyTrackAndNamesTheSingleTrack(t *testing.T) {
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not on PATH")
	}
	a := newAlbumFixture(t, "fi-probe", []string{"rec-airbag"}, "High", "FLAC")
	fixture, err := os.ReadFile("../../../testdata/mediainfo/audio_mp3_cbr320.mp3")
	require.NoError(t, err)
	contentRoot := dataDir(t, "scratch")
	require.NoError(t, os.WriteFile(filepath.Join(contentRoot, "01 - Airbag.mp3"), fixture, 0o644))

	got := a.importAlbum(t, "probe-dl", contentRoot, false)
	require.Equal(t, downloadv1alpha1.ImportPhaseImported, got.State, "message %q, rejections %v", got.Message, got.Rejections)
	mf := a.mediaFiles(t)["01 - Airbag.mp3"]
	assert.Equal(t, "High", mf.Spec.Quality.Name, "a 320 kbps MP3 freezes on Lidarr's High tier")
	assert.Equal(t, commonv1.MediaRef{Kind: commonv1.MediaKindAlbum, Name: a.album.Name, Track: "rec-airbag"}, mf.Spec.MediaRef)
}

// Two files for a one-track album are not both that track, and picking one
// would be a guess: neither is narrowed to it.
func TestHandleTwoFilesForASingleTrackAlbumNameNoTrack(t *testing.T) {
	a := newAlbumFixture(t, "fi-two-files", []string{"rec-airbag"}, "FLAC")
	contentRoot := dataDir(t, "scratch")
	mustWriteSparseFile(t, filepath.Join(contentRoot, "01 - Airbag.flac"), 1<<20)
	mustWriteSparseFile(t, filepath.Join(contentRoot, "01 - Airbag (demo).flac"), 1<<20)

	got := a.importAlbum(t, "two-dl", contentRoot, false)
	require.Equal(t, downloadv1alpha1.ImportPhaseImported, got.State, "message %q, rejections %v", got.Message, got.Rejections)
	for name, mf := range a.mediaFiles(t) {
		assert.Empty(t, mf.Spec.MediaRef.Track, name)
	}
}

// The carried "an album's or audiobook's manual import adds files and never
// replaces old ones": a manual import that brings a whole new release of the
// album supersedes the album's earlier files -- recycled, their MediaFiles
// deleted -- and a file of the new release landing on an old one's path
// links the old bytes into the bin before it is overwritten. A release with
// a file rejected does not wholly replace the old one, so it keeps them.
func TestHandleManualAlbumImportReplacesTheEarlierFiles(t *testing.T) {
	a := newAlbumFixture(t, "fi-replace", []string{"r1", "r2", "r3"}, "FLAC")

	first := dataDir(t, "scratch")
	mustWriteSparseFile(t, filepath.Join(first, "01 - Airbag.flac"), 1<<20)
	mustWriteSparseFile(t, filepath.Join(first, "02 - Paranoid Android.flac"), 1<<20)
	got := a.importAlbum(t, "first-dl", first, false)
	require.Equal(t, downloadv1alpha1.ImportPhaseImported, got.State, "message %q, rejections %v", got.Message, got.Rejections)
	before := a.mediaFiles(t)
	require.Len(t, before, 2)
	waitFor(t, 5*time.Second, func() bool {
		var list catalogv1alpha1.MediaFileList
		return a.c.List(context.Background(), &list, client.InNamespace(a.ns)) == nil && len(list.Items) == 2
	})

	// A rejected file: the new release is not wholly imported, so nothing
	// old goes.
	partial := dataDir(t, "scratch")
	mustWriteSparseFile(t, filepath.Join(partial, "03 - Subterranean Homesick Alien.flac"), 1<<20)
	mustWriteSparseFile(t, filepath.Join(partial, "04 - Exit Music.wav"), 1<<20)
	got = a.importAlbum(t, "partial-dl", partial, true)
	require.Equal(t, downloadv1alpha1.ImportPhaseImported, got.State, "message %q, rejections %v", got.Message, got.Rejections)
	assert.Len(t, a.mediaFiles(t), 3, "the two earlier files are kept beside the one imported")
	require.NotEmpty(t, got.Rejections)
	assert.Contains(t, got.Rejections[len(got.Rejections)-1], "kept its 2 earlier file(s)")
	assert.Empty(t, a.binFiles(t))
	waitFor(t, 5*time.Second, func() bool {
		var list catalogv1alpha1.MediaFileList
		return a.c.List(context.Background(), &list, client.InNamespace(a.ns)) == nil && len(list.Items) == 3
	})

	// The whole new release: one file on an old path, one new.
	whole := dataDir(t, "scratch")
	mustWriteSparseFile(t, filepath.Join(whole, "01 - Airbag.flac"), 2<<20)
	mustWriteSparseFile(t, filepath.Join(whole, "05 - Let Down.flac"), 2<<20)
	got = a.importAlbum(t, "whole-dl", whole, true)
	require.Equal(t, downloadv1alpha1.ImportPhaseImported, got.State, "message %q, rejections %v", got.Message, got.Rejections)
	assert.Empty(t, got.Rejections)

	after := a.mediaFiles(t)
	assert.Len(t, after, 2, "only the new release's files back the album")
	require.Contains(t, after, "01 - Airbag.flac")
	require.Contains(t, after, "05 - Let Down.flac")
	assert.Equal(t, int64(2<<20), after["01 - Airbag.flac"].Spec.SizeBytes, "the file on the old path is the new one")
	for _, gone := range []string{"02 - Paranoid Android.flac", "03 - Subterranean Homesick Alien.flac"} {
		_, err := os.Stat(before["01 - Airbag.flac"].Spec.Path[:len(before["01 - Airbag.flac"].Spec.Path)-len("01 - Airbag.flac")] + gone)
		assert.ErrorIs(t, err, os.ErrNotExist, "%s left the library", gone)
	}
	assert.ElementsMatch(t, []string{"01 - Airbag.flac", "02 - Paranoid Android.flac", "03 - Subterranean Homesick Alien.flac"},
		a.binFiles(t), "every replaced file is in the recycle bin, the overwritten one included")
}

// The carried "a manual folder import takes promo clips along": a person
// importing a release imports the release, and the small video beside the
// real one is its promo clip. It is left behind with a rejection saying so.
// (A download whose only video is small is still imported by a manual
// import: TestHandleImportsASuspectedSampleWhenManualOrTheRuleIsOff.)
func TestHandleManualImportLeavesThePromoClipBehind(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "fi-promo")
	contentRoot := dataDir(t, "scratch")
	mustWriteSparseFile(t, filepath.Join(contentRoot, "The.Matrix.1999.1080p.BluRay.x264-SPARKS.mkv"), sampleFloor)
	mustWriteSparseFile(t, filepath.Join(contentRoot, "Promo", "trailer-cut.mkv"), 10<<20)
	dl := f.createDownloadWith(t, "promo-dl", contentRoot,
		commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: f.movieName}, f.profile.Name,
		map[string]string{fileimport.AnnotationImportOverride: "true"})

	require.NoError(t, f.worker.Handle(ctx, newImportTaskMessage(t, f.ns, dl.Name, "")))

	got := f.importState(t, dl).Status.Import
	require.Equal(t, downloadv1alpha1.ImportPhaseImported, got.State, "message %q, rejections %v", got.Message, got.Rejections)
	require.Len(t, got.Imported, 1, "the film, not the clip")
	assert.Equal(t, "The.Matrix.1999.1080p.BluRay.x264-SPARKS.mkv", got.Imported[0].SourcePath)
	require.Len(t, got.Rejections, 1)
	assert.Contains(t, got.Rejections[0], "left behind by this manual import")
}
