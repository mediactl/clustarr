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
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	downloadac "github.com/mediactl/clustarr/api/applyconfiguration/download/download/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// donorClip is a short Matroska file with video and an audio track per
// language.
func donorClip(t *testing.T, langs ...string) string {
	t.Helper()
	requireFFmpeg(t)
	args := []string{"-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "testsrc2=duration=1:size=160x90:rate=25"}
	maps := []string{"-map", "0"}
	var meta []string
	for i, l := range langs {
		args = append(args, "-f", "lavfi", "-i", "sine=frequency=440:duration=1")
		maps = append(maps, "-map", string(rune('1'+i)))
		meta = append(meta, "-metadata:s:a:"+string(rune('0'+i)), "language="+l)
	}
	args = append(append(append(args, maps...), "-c:v", "libx264", "-preset", "ultrafast", "-c:a", "aac"), meta...)
	return encodeClip(t, "donor.mkv", args...)
}

// donorFor makes the fixture's movie a Japanese film whose file lacks the
// English dub its profile grafts, and a completed donor Download for it.
func (f *fixture) donorFor(t *testing.T, name, contentRoot string) *downloadv1alpha1.Download {
	t.Helper()
	ctx := context.Background()
	_, err := k8s.PatchStatus(ctx, f.c, k8s.ManagerCatalog, catalogac.Movie(f.movieName, f.ns).WithStatus(
		catalogac.MovieStatus().
			WithMetadata(catalogac.MovieMetadata().WithTitle("The Matrix").WithYear(1999).WithOriginalLanguage("ja")).
			WithAudio(catalogac.AudioState().WithWanted("en", "ja").WithPresent("ja").WithMissing("en").WithGraft("grabbed"))))
	require.NoError(t, err)
	waitFor(t, 5*time.Second, func() bool {
		var m catalogv1alpha1.Movie
		return f.c.Get(ctx, client.ObjectKey{Namespace: f.ns, Name: f.movieName}, &m) == nil && m.Status.Audio != nil
	})
	dl := &downloadv1alpha1.Download{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: f.ns},
		Spec: downloadv1alpha1.DownloadSpec{
			Protocol: commonv1.ProtocolUsenet, Source: downloadv1alpha1.DownloadSource{NZBURL: ptrTo("http://idx/donor.nzb")},
			Release: commonv1.ReleaseInfo{
				GUID: "g-" + name, IndexerRef: "idx", Title: "The.Matrix.1999.DVDRip.x264.AAC.DL-BoB",
				Protocol: commonv1.ProtocolUsenet,
			},
			Target:  commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: f.movieName},
			Purpose: downloadv1alpha1.DownloadPurposeAudioDonor, QualityProfileRef: f.profile.Name,
		},
	}
	require.NoError(t, f.c.Create(ctx, dl))
	_, err = k8s.PatchStatus(ctx, f.c, k8s.ManagerGrab, downloadac.Download(name, f.ns).WithStatus(
		downloadac.DownloadStatus().WithPhase(downloadv1alpha1.DownloadPhaseCompleted).WithContentRoot(contentRoot).WithCanMoveFiles(false)))
	require.NoError(t, err)
	waitFor(t, 5*time.Second, func() bool {
		var got downloadv1alpha1.Download
		return f.c.Get(ctx, client.ObjectKeyFromObject(dl), &got) == nil && got.Status.Phase == downloadv1alpha1.DownloadPhaseCompleted
	})
	return dl
}

func ptrTo[T any](v T) *T { return &v }

// TestADonorIsPlacedBesideTheLibraryAndNamedInTheAudioGraft (spec §6.2):
// a donor makes no MediaFile; its file goes under the item's donor folder
// and its AudioGraft names it, with the languages, anchor and release.
func TestADonorIsPlacedBesideTheLibraryAndNamedInTheAudioGraft(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "fi-donor")
	clip := donorClip(t, "eng", "jpn")
	contentRoot := dataDir(t, "scratch")
	copyOf(t, clip)(filepath.Join(contentRoot, "The.Matrix.1999.DVDRip.x264.AAC.DL-BoB.mkv"))
	dl := f.donorFor(t, "donor-dl", contentRoot)

	require.NoError(t, f.worker.Handle(ctx, newImportTaskMessage(t, f.ns, dl.Name, "")))

	var got downloadv1alpha1.Download
	require.NoError(t, f.api.Get(ctx, client.ObjectKeyFromObject(dl), &got))
	require.NotNil(t, got.Status.Import)
	require.Equal(t, downloadv1alpha1.ImportPhaseImported, got.Status.Import.State, got.Status.Import.Message)
	require.Len(t, got.Status.Import.Imported, 1)
	imp := got.Status.Import.Imported[0]
	assert.Empty(t, imp.MediaFileRef, "a donor is no library file")
	var movie catalogv1alpha1.Movie
	require.NoError(t, f.api.Get(ctx, client.ObjectKey{Namespace: f.ns, Name: f.movieName}, &movie))
	want := filepath.Join(f.mediaRoot, ".clustarr", "donors", string(movie.UID), f.movieName+".mkv")
	assert.Equal(t, want, imp.DestPath)
	assert.FileExists(t, want)

	var files catalogv1alpha1.MediaFileList
	require.NoError(t, f.api.List(ctx, &files, client.InNamespace(f.ns)))
	assert.Empty(t, files.Items)

	var g transcodev1alpha1.AudioGraft
	require.NoError(t, f.api.Get(ctx, client.ObjectKey{Namespace: f.ns, Name: k8s.AudioGraftName(f.movieName)}, &g))
	assert.Equal(t, want, g.Spec.DonorPath)
	assert.Equal(t, []string{"en"}, g.Spec.Languages)
	assert.Equal(t, "ja", g.Spec.Anchor)
	assert.Equal(t, "The.Matrix.1999.DVDRip.x264.AAC.DL-BoB", g.Spec.Release)
	require.Len(t, g.OwnerReferences, 1)
	assert.Equal(t, movie.UID, g.OwnerReferences[0].UID, "the AudioGraft goes with its item")
}

// TestADonorWithoutTheDubIsTheReleasesFault: its files lack the language
// the title promised, so the import is the release's fault: walked once
// more to confirm, then every file rejected -- grabarr blocklists the
// release and the donor is searched for again.
func TestADonorWithoutTheDubIsTheReleasesFault(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "fi-donor-lacks")
	contentRoot := dataDir(t, "scratch")
	copyOf(t, donorClip(t, "jpn"))(filepath.Join(contentRoot, "The.Matrix.1999.DVDRip.x264.AAC.DL-BoB.mkv"))
	dl := f.donorFor(t, "donor-lacks-dl", contentRoot)

	var retry *events.RetryError
	require.ErrorAs(t, f.worker.Handle(ctx, newImportTaskMessage(t, f.ns, dl.Name, "")), &retry,
		"a release fault is walked once more before grabarr blocklists it")
	var got downloadv1alpha1.Download
	require.NoError(t, f.api.Get(ctx, client.ObjectKeyFromObject(dl), &got))
	require.NotNil(t, got.Status.Import)
	assert.Equal(t, downloadv1alpha1.ImportPhasePending, got.Status.Import.State)
	assert.Equal(t, downloadv1alpha1.ImportClassReleaseFault, got.Status.Import.Class)
	require.NotNil(t, got.Status.Import.NextAttemptAt)

	second := newImportTaskMessage(t, f.ns, dl.Name, "")
	second.attempt = 2
	require.NoError(t, f.worker.Handle(ctx, second))
	require.NoError(t, f.api.Get(ctx, client.ObjectKeyFromObject(dl), &got))
	require.NotNil(t, got.Status.Import)
	assert.Equal(t, downloadv1alpha1.ImportPhaseBlocked, got.Status.Import.State)
	assert.Equal(t, downloadv1alpha1.ImportClassReleaseFault, got.Status.Import.Class)
	assert.Nil(t, got.Status.Import.HeldSince, "a release fault is not held: grabarr blocklists it")
	assert.Equal(t, downloadv1alpha1.ImportMessageEveryFileRejected, got.Status.Import.Message)
	require.NotEmpty(t, got.Status.Import.Rejections)
	assert.True(t, strings.Contains(got.Status.Import.Rejections[0], "en"), got.Status.Import.Rejections[0])
	var g transcodev1alpha1.AudioGraft
	assert.Error(t, f.api.Get(ctx, client.ObjectKey{Namespace: f.ns, Name: k8s.AudioGraftName(f.movieName)}, &g), "no AudioGraft")
}
