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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	downloadac "github.com/mediactl/clustarr/api/applyconfiguration/download/download/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/importarr/worker/fileimport"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// createDownloadWith is createDownload with the profile and annotations under
// the test's control, for targets other than the fixture's movie.
func (f *fixture) createDownloadWith(
	t *testing.T, name, contentRoot string, target commonv1.MediaRef, profile string, annotations map[string]string,
) *downloadv1alpha1.Download {
	t.Helper()
	ctx := context.Background()
	magnet := "magnet:?xt=urn:btih:" + strings.Repeat("b", 40)
	dl := &downloadv1alpha1.Download{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: f.ns, Annotations: annotations},
		Spec: downloadv1alpha1.DownloadSpec{
			Protocol: commonv1.ProtocolTorrent,
			Source:   downloadv1alpha1.DownloadSource{MagnetURL: &magnet},
			Release: commonv1.ReleaseInfo{
				GUID: "g-" + name, IndexerRef: "idx", IndexerName: "Example", Title: name,
				Protocol: commonv1.ProtocolTorrent, InfoHash: strings.Repeat("b", 40),
			},
			Target:            target,
			QualityProfileRef: profile,
		},
	}
	require.NoError(t, f.c.Create(ctx, dl))
	_, err := k8s.PatchStatus(ctx, f.c, k8s.ManagerGrabarr, downloadac.Download(dl.Name, f.ns).WithStatus(
		downloadac.DownloadStatus().
			WithPhase(downloadv1alpha1.DownloadPhaseCompleted).
			WithContentRoot(contentRoot)))
	require.NoError(t, err)
	waitFor(t, 5*time.Second, func() bool {
		var got downloadv1alpha1.Download
		return f.c.Get(ctx, client.ObjectKeyFromObject(dl), &got) == nil && got.Status.Phase == downloadv1alpha1.DownloadPhaseCompleted
	})
	return dl
}

// setAnnotation is a user editing the Download's metadata, as kubectl
// annotate or the UI would. It waits for the cache to see the change.
func (f *fixture) setAnnotation(t *testing.T, dl *downloadv1alpha1.Download, key, value string) {
	t.Helper()
	ctx := context.Background()
	var cur downloadv1alpha1.Download
	require.NoError(t, f.api.Get(ctx, client.ObjectKeyFromObject(dl), &cur))
	patch := client.MergeFrom(cur.DeepCopy())
	if cur.Annotations == nil {
		cur.Annotations = map[string]string{}
	}
	cur.Annotations[key] = value
	require.NoError(t, f.c.Patch(ctx, &cur, patch))
	waitFor(t, 5*time.Second, func() bool {
		var got downloadv1alpha1.Download
		return f.c.Get(ctx, client.ObjectKeyFromObject(dl), &got) == nil && got.Annotations[key] == value
	})
}

func (f *fixture) importState(t *testing.T, dl *downloadv1alpha1.Download) *downloadv1alpha1.Download {
	t.Helper()
	var got downloadv1alpha1.Download
	require.NoError(t, f.api.Get(context.Background(), client.ObjectKeyFromObject(dl), &got))
	require.NotNil(t, got.Status.Import)
	return &got
}

// nonVideoProfile is a minimal QualityProfile of a non-video kind.
func (f *fixture) nonVideoProfile(t *testing.T, kind catalogv1alpha1.ProfileMediaKind, qualities ...string) string {
	t.Helper()
	qp := &catalogv1alpha1.QualityProfile{
		ObjectMeta: metav1.ObjectMeta{Name: string(kind) + "-" + f.ns},
		Spec: catalogv1alpha1.QualityProfileSpec{
			MediaKind: kind, Cutoff: "all",
			Tiers: []catalogv1alpha1.Tier{{Name: "all", Qualities: qualities}},
		},
	}
	require.NoError(t, f.c.Create(context.Background(), qp))
	waitFor(t, 5*time.Second, func() bool {
		return f.c.Get(context.Background(), client.ObjectKeyFromObject(qp), &catalogv1alpha1.QualityProfile{}) == nil
	})
	return qp.Name
}

func (f *fixture) newRoot(t *testing.T, name string, kind catalogv1alpha1.RootFolderKind) *catalogv1alpha1.RootFolder {
	t.Helper()
	rf := &catalogv1alpha1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: f.ns},
		Spec:       catalogv1alpha1.RootFolderSpec{Path: dataDir(t, "media"), Kind: kind},
	}
	require.NoError(t, f.c.Create(context.Background(), rf))
	waitFor(t, 5*time.Second, func() bool {
		return f.c.Get(context.Background(), client.ObjectKeyFromObject(rf), &catalogv1alpha1.RootFolder{}) == nil
	})
	return rf
}

// capturePublisher records what Retrigger publishes.
type capturePublisher struct {
	subject string
	env     *events.Envelope
}

func (p *capturePublisher) Publish(_ context.Context, subject string, e *events.Envelope, _ ...events.PublishOption) (events.Receipt, error) {
	p.subject, p.env = subject, e
	return events.Receipt{}, nil
}

// import-target redirects an import to another item of a supported kind:
// the MediaFile backs the annotated movie, not spec.target.
func TestHandleImportTargetRedirectsAMovieImport(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "fi-redirect")

	heat := &catalogv1alpha1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: "heat", Namespace: f.ns},
		Spec:       catalogv1alpha1.MovieSpec{TmdbID: 949, QualityProfileRef: f.profile.Name, RootFolderRef: f.rootFolder.Name},
	}
	require.NoError(t, f.c.Create(ctx, heat))
	_, err := k8s.PatchStatus(ctx, f.c, k8s.ManagerCatalogarr, catalogac.Movie(heat.Name, f.ns).WithStatus(
		catalogac.MovieStatus().WithMetadata(catalogac.MovieMetadata().WithTitle("Heat").WithYear(1995))))
	require.NoError(t, err)
	waitFor(t, 5*time.Second, func() bool {
		var got catalogv1alpha1.Movie
		return f.c.Get(ctx, client.ObjectKeyFromObject(heat), &got) == nil && got.Status.Metadata != nil
	})

	contentRoot := dataDir(t, "scratch")
	mustWriteSparseFile(t, filepath.Join(contentRoot, "Heat.1995.1080p.BluRay.x264-SPARKS.mkv"), sampleFloor)
	dl := f.createDownloadWith(t, "redirect-dl", contentRoot,
		commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: f.movieName}, f.profile.Name,
		map[string]string{fileimport.AnnotationImportTarget: "movie/heat"})

	require.NoError(t, f.worker.Handle(ctx, newImportTaskMessage(t, f.ns, dl.Name, "")))

	got := f.importState(t, dl)
	require.Equal(t, downloadv1alpha1.ImportPhaseImported, got.Status.Import.State, got.Status.Import.Message)
	var mf catalogv1alpha1.MediaFile
	require.NoError(t, f.api.Get(ctx, client.ObjectKey{Namespace: f.ns, Name: got.Status.Import.Imported[0].MediaFileRef}, &mf))
	assert.Equal(t, commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "heat"}, mf.Spec.MediaRef)
	assert.Contains(t, mf.Spec.Path, "Heat (1995)")
}

// A malformed annotation is a user instruction this worker cannot follow:
// it is reported on status.import -- which importarr owns, on a Download
// whose other status fields grabarr owns and keeps -- and nothing is
// imported, not even to spec.target.
func TestHandleReportsAMalformedImportAnnotationOnStatus(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "fi-bad-annotation")

	contentRoot := dataDir(t, "scratch")
	mustWriteSparseFile(t, filepath.Join(contentRoot, "The.Matrix.1999.1080p.BluRay.x264-SPARKS.mkv"), sampleFloor)

	for i, bad := range []map[string]string{
		{fileimport.AnnotationImportTarget: "movie/The Matrix"},
		{fileimport.AnnotationImportOverride: "yes"},
	} {
		dl := f.createDownloadWith(t, "bad-"+string(rune('a'+i)), contentRoot,
			commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: f.movieName}, f.profile.Name, bad)
		// The Download already has an import state from an earlier attempt.
		_, err := k8s.PatchStatus(ctx, f.c, k8s.ManagerImportarr, downloadac.Download(dl.Name, f.ns).WithStatus(
			downloadac.DownloadStatus().WithImport(downloadac.ImportState().
				WithState(downloadv1alpha1.ImportPhaseBlocked).WithMessage(downloadv1alpha1.ImportMessageEveryFileRejected))))
		require.NoError(t, err)

		require.NoError(t, f.worker.Handle(ctx, newImportTaskMessage(t, f.ns, dl.Name, "")))

		got := f.importState(t, dl)
		assert.Equal(t, downloadv1alpha1.ImportPhaseBlocked, got.Status.Import.State)
		assert.Contains(t, got.Status.Import.Message, "invalid annotation")
		assert.Equal(t, downloadv1alpha1.DownloadPhaseCompleted, got.Status.Phase, "grabarr's fields survive")
		assert.Equal(t, contentRoot, got.Status.ContentRoot)
		assert.Equal(t, string(k8s.ManagerImportarr), managerFor(t, got.ManagedFields, "status", "status.import.message"))
		assert.Equal(t, string(k8s.ManagerGrabarr), managerFor(t, got.ManagedFields, "status", "status.phase"))
	}

	var mfList catalogv1alpha1.MediaFileList
	require.NoError(t, f.api.List(ctx, &mfList, client.InNamespace(f.ns)))
	assert.Empty(t, mfList.Items, "a malformed instruction must not fall back to spec.target")
}

// A non-video import whose quality cannot be determined without a probe (an
// .mp3's tier is a bitrate band) is blocked unless it is manual. A user then sets import-override=true, the
// Retrigger re-queues the blocked import, and the re-run imports the tracks
// into the album's folder under the artist's, recording importedFrom.manual
// and no quality -- MediaFileSpec only.
func TestHandleNonVideoImportNeedsOverrideAndRetriggerRequeuesIt(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "fi-album")
	rf := f.newRoot(t, "music", catalogv1alpha1.RootFolderKindMusic)
	profile := f.nonVideoProfile(t, catalogv1alpha1.ProfileMediaKindMusic, "FLAC", "WAV")

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
	_, err = k8s.PatchStatus(ctx, f.c, k8s.ManagerCatalogarrMetadata, catalogac.Album(album.Name, f.ns).
		WithStatus(catalogac.AlbumStatus().WithMetadata(catalogac.AlbumMetadata().WithTitle("OK Computer").WithReleaseDate(released))))
	require.NoError(t, err)
	waitFor(t, 5*time.Second, func() bool {
		var a catalogv1alpha1.Album
		var r catalogv1alpha1.Artist
		return f.c.Get(ctx, client.ObjectKeyFromObject(album), &a) == nil && a.Status.Metadata != nil &&
			f.c.Get(ctx, client.ObjectKeyFromObject(artist), &r) == nil && r.Status.Metadata != nil
	})

	contentRoot := dataDir(t, "scratch")
	mustWriteSparseFile(t, filepath.Join(contentRoot, "CD1", "01 - Airbag.mp3"), 1<<20)
	mustWriteSparseFile(t, filepath.Join(contentRoot, "folder.jpg"), 4096)
	dl := f.createDownloadWith(t, "okc-dl", contentRoot,
		commonv1.MediaRef{Kind: commonv1.MediaKindAlbum, Name: album.Name}, "", nil)

	require.NoError(t, f.worker.Handle(ctx, newImportTaskMessage(t, f.ns, dl.Name, "")))
	blocked := f.importState(t, dl)
	require.Equal(t, downloadv1alpha1.ImportPhaseBlocked, blocked.Status.Import.State)
	require.Len(t, blocked.Status.Import.Rejections, 1)
	assert.Contains(t, blocked.Status.Import.Rejections[0], "cannot be determined without probing")

	// The user overrides; the Retrigger sees the annotation change on a
	// Blocked Download and re-queues exactly one import task for it.
	f.setAnnotation(t, dl, fileimport.AnnotationImportOverride, "true")
	pub := &capturePublisher{}
	r := &fileimport.Retrigger{Client: f.c, Bus: pub}
	_, err = r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(dl)})
	require.NoError(t, err)
	require.NotNil(t, pub.env, "a blocked download with a new import annotation must be re-queued")
	assert.Equal(t, events.WorkFileImportSubject(string(blocked.UID)), pub.subject)

	require.NoError(t, f.worker.Handle(ctx, &fakeMessage{env: pub.env}))

	got := f.importState(t, dl)
	require.Equal(t, downloadv1alpha1.ImportPhaseImported, got.Status.Import.State, got.Status.Import.Message)
	require.Len(t, got.Status.Import.Imported, 1)
	want := filepath.Join(rf.Spec.Path, "Radiohead", "OK Computer (1997)", "CD1", "01 - Airbag.mp3")
	assert.Equal(t, want, got.Status.Import.Imported[0].DestPath)
	_, err = os.Stat(want)
	require.NoError(t, err)

	var mf catalogv1alpha1.MediaFile
	require.NoError(t, f.api.Get(ctx, client.ObjectKey{Namespace: f.ns, Name: got.Status.Import.Imported[0].MediaFileRef}, &mf))
	assert.Equal(t, commonv1.MediaRef{Kind: commonv1.MediaKindAlbum, Name: album.Name}, mf.Spec.MediaRef)
	assert.Equal(t, commonv1.ReleaseTypeAlbum, mf.Spec.ReleaseType)
	assert.Empty(t, mf.Spec.Quality.Name)
	require.NotNil(t, mf.Spec.ImportedFrom)
	assert.True(t, mf.Spec.ImportedFrom.Manual)
	assert.Equal(t, "importarr-worker", managerFor(t, mf.ManagedFields, "", "spec.importedFrom.manual"))
	assert.Empty(t, managerFor(t, mf.ManagedFields, "status", "status"), "never any MediaFile status")

	// The Download is Imported now: a further annotation change is ignored.
	f.setAnnotation(t, dl, fileimport.AnnotationImportTarget, "album/"+album.Name)
	pub.env = nil
	_, err = r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(dl)})
	require.NoError(t, err)
	assert.Nil(t, pub.env, "only a Blocked import is re-queued")
}

// A comic Download is a container: its files belong to one of its issues,
// and choosing which is a guess, so it blocks and says how to fix it. The
// keyed annotation "comic/<comic>/<issue>" then imports to that Issue under
// pkg/naming's "{Series}/{Series} c{issue}" -- and a key naming another
// comic's issue is refused rather than followed.
func TestHandleImportTargetKeyedComicIssue(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, "fi-comic")
	rf := f.newRoot(t, "comics", catalogv1alpha1.RootFolderKindComic)
	profile := f.nonVideoProfile(t, catalogv1alpha1.ProfileMediaKindComic, "CBZ")

	for _, name := range []string{"saga", "monstress"} {
		co := &catalogv1alpha1.Comic{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: f.ns},
			Spec: catalogv1alpha1.ComicSpec{
				Source: catalogv1alpha1.ComicSourceComicVine, SourceID: "4050-" + name,
				QualityProfileRef: profile, RootFolderRef: rf.Name,
			},
		}
		require.NoError(t, f.c.Create(ctx, co))
		_, err := k8s.PatchStatus(ctx, f.c, k8s.ManagerCatalogarrMetadata, catalogac.Comic(name, f.ns).
			WithStatus(catalogac.ComicStatus().WithMetadata(catalogac.ComicMetadata().WithTitle(strings.ToUpper(name[:1])+name[1:]))))
		require.NoError(t, err)
		is := &catalogv1alpha1.Issue{
			ObjectMeta: metav1.ObjectMeta{Name: name + "-00001.0", Namespace: f.ns},
			Spec:       catalogv1alpha1.IssueSpec{ComicRef: name, Number: "1", CalculatedNumberCentis: 100},
		}
		require.NoError(t, f.c.Create(ctx, is))
		waitFor(t, 5*time.Second, func() bool {
			var c catalogv1alpha1.Comic
			return f.c.Get(ctx, client.ObjectKeyFromObject(co), &c) == nil && c.Status.Metadata != nil &&
				f.c.Get(ctx, client.ObjectKeyFromObject(is), &catalogv1alpha1.Issue{}) == nil
		})
	}

	contentRoot := dataDir(t, "scratch")
	mustWriteSparseFile(t, filepath.Join(contentRoot, "Saga 001 (2012).cbz"), 1<<20)
	dl := f.createDownloadWith(t, "saga-dl", contentRoot,
		commonv1.MediaRef{Kind: commonv1.MediaKindComic, Name: "saga"}, "", nil)

	require.NoError(t, f.worker.Handle(ctx, newImportTaskMessage(t, f.ns, dl.Name, "")))
	got := f.importState(t, dl)
	require.Equal(t, downloadv1alpha1.ImportPhaseBlocked, got.Status.Import.State)
	assert.Contains(t, got.Status.Import.Message, "holds no files itself")

	f.setAnnotation(t, dl, fileimport.AnnotationImportTarget, "comic/saga/monstress-00001.0")
	require.NoError(t, f.worker.Handle(ctx, newImportTaskMessage(t, f.ns, dl.Name, "")))
	got = f.importState(t, dl)
	require.Equal(t, downloadv1alpha1.ImportPhaseBlocked, got.Status.Import.State)
	assert.Contains(t, got.Status.Import.Message, `belongs to comic "monstress"`)

	f.setAnnotation(t, dl, fileimport.AnnotationImportTarget, "comic/saga/saga-00001.0")
	require.NoError(t, f.worker.Handle(ctx, newImportTaskMessage(t, f.ns, dl.Name, "")))
	got = f.importState(t, dl)
	require.Equal(t, downloadv1alpha1.ImportPhaseImported, got.Status.Import.State, got.Status.Import.Message)
	assert.Equal(t, filepath.Join(rf.Spec.Path, "Saga", "Saga c1.cbz"), got.Status.Import.Imported[0].DestPath)

	var mf catalogv1alpha1.MediaFile
	require.NoError(t, f.api.Get(ctx, client.ObjectKey{Namespace: f.ns, Name: got.Status.Import.Imported[0].MediaFileRef}, &mf))
	assert.Equal(t, commonv1.MediaRef{Kind: commonv1.MediaKindIssue, Name: "saga-00001.0"}, mf.Spec.MediaRef)
	assert.Equal(t, "CBZ", mf.Spec.Quality.Name)
	assert.False(t, mf.Spec.ImportedFrom.Manual, "no override, no spec.manual: CBZ is known and allowed")
}
