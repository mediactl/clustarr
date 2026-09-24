//go:build e2e

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

// Scenario 11 (docs/superpowers/plans/2026-09-18-remaining-work.md):
// "Non-video inventory (M6). Artist/Album, Author/Book, Audiobook and
// Comic/Issue through manual import (Download with Manual=true and the
// import-target annotation) -> files stored under the naming presets with
// metadata from the stubs."
//
// Every parent/child pair here is a real reconciler as of G2-5 (9caea89):
// the seven non-video reconcilers (album, artist, audiobook, author, book,
// comic, issue) are registered and proven to run in
// cmd/clustarr/start_envtest_test.go. Metadata comes from
// test/fixtures/nonvideostub, real HTTP, real JSON, never mocked at the
// gateway boundary -- the same MusicBrainz/Open Library/Audnexus/ComicVine
// fixtures test/fixtures/nonvideostub's own unit tests drive with the real
// pkg/metadata/clients.
//
// # Why manual import here is a LibraryScan, not a Download
//
// The plan text names "Download with Manual=true and the import-target
// annotation". Even after X12c (docs/superpowers/plans/2026-09-23-gap-
// fixes.md) fixed test/fixtures/seeder's and test/fixtures/nntpstub's
// former "clustarr-fixture.bin" extension gap (test/e2e/download_test.go's
// package doc comment), a REAL grabbed-and-completed Download still cannot
// reach fileimport's import step for a NON-VIDEO kind: both fixtures serve
// one fixed movie-release file (seeder.ContentName), real VIDEO bytes
// under a real MOVIE-shaped release name, and app/import/worker/fileimport/
// process.go's release.ParsePath call is hard-coded
// Options{Kind: commonv1.MediaKindMovie} (processFile's own source) for
// the movie walk this Download route drives -- there is no download-path
// route to an Artist/Author/Audiobook/Comic import in this fixture set
// regardless of extension, and building one (a second fixture content
// kind, or a configurable one) is a materially different task than X12c's
// brief. Driving a Download here would still produce four more
// waitForImportOutcome failures proving nothing about non-video import
// specifically.
//
// app/import/worker/fileimport/annotation.go's own doc comment states the
// import-target grammar is honoured on BOTH a Download and a LibraryScan --
// "The same import-target grammar is also honoured on a LibraryScan, where
// it is how a file the scanner left unmatched is assigned by hand" -- and
// this is exactly the mechanism ui/actions.ManualAssign (G3-4) already
// builds: a LibraryScan whose spec.subpath names one file and whose
// catalog.clustarr.io/import-target annotation redirects it at one catalog
// item, mode: full. This file drives that mechanism directly against
// k8sClient rather than through ui/actions (which needs Options.Actions
// wired -- see ui_test.go's new page tests for that gap), planting one file
// under each kind's RootFolder and creating the redirecting LibraryScan by
// hand, in ui/actions.ManualAssign's own exact shape.
// Until G2-4 (6e1b97b), library rescan refused every non-movie root folder
// as "unsupported_root_kind", so every scan below would have failed
// outright; that code is now reported only for a root folder kind rescan
// does not attribute at all (app/import/worker/rescan's fileKindForRoot).
//
// Build-tagged e2e. Per the standing instruction, this suite is written and
// has never been run against a kind cluster.
package e2e

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/import/worker/fileimport"
)

// nonVideoConditionTimeout bounds the wait for a parent's metadata-ready and
// fan-out conditions: one real HTTP round trip to nonvideo-stub plus one
// reconcile, generous margin over indexerReadyTimeout's own reasoning for
// the same shape of wait.
const nonVideoConditionTimeout = 2 * time.Minute

// nonVideoImportTimeout bounds the wait for the manual-assign LibraryScan's
// resulting MediaFile to appear.
const nonVideoImportTimeout = 2 * time.Minute

// fixtureNonVideoStubService is config/e2e/nonvideo-stub.yaml's Service
// name, matching helpers_test.go's fixtureSeederService/fixtureNNTPStub*
// naming.
const fixtureNonVideoStubService = "nonvideo-stub"

// manualAssignScan creates the exact LibraryScan ui/actions.ManualAssign
// builds (G3-4) -- GenerateName "assign-", the import-target annotation,
// spec.mode Full -- directly against k8sClient, so this file proves the
// WORKER half of manual assignment on its own. The ui half (Options.Actions,
// wired into both ui commands by G3-5) is scenario 14's POST to
// /unmatched/assign in ui_test.go.
func manualAssignScan(ctx context.Context, t *testing.T, rootFolder, subpath string, target fileimport.ImportTarget) *catalogv1alpha1.LibraryScan {
	t.Helper()
	scan := &catalogv1alpha1.LibraryScan{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "e2e-assign-",
			Namespace:    Namespace,
			Annotations:  map[string]string{fileimport.AnnotationImportTarget: target.String()},
		},
		Spec: catalogv1alpha1.LibraryScanSpec{
			RootFolderRef: rootFolder,
			Subpath:       subpath,
			Mode:          catalogv1alpha1.ScanModeFull,
		},
	}
	require.NoError(t, k8sClient.Create(ctx, scan))
	cleanupUnlessFailed(t, func() { _ = k8sClient.Delete(context.Background(), scan) })
	return scan
}

// waitForMediaFileAt polls for exactly one MediaFile whose spec.path equals
// fullPath, the manual-assign import's own success signal -- more direct
// than waiting on the LibraryScan's own phase, which the never-guess rule
// (CLAUDE.md) can legitimately leave at Completed with the file still
// recorded in status.unmatched if the target was refused.
func waitForMediaFileAt(ctx context.Context, t *testing.T, fullPath string) catalogv1alpha1.MediaFile {
	t.Helper()
	var found catalogv1alpha1.MediaFile
	err := wait.PollUntilContextTimeout(ctx, pollInterval, nonVideoImportTimeout, true, func(ctx context.Context) (bool, error) {
		files := mediaFilesUnder(ctx, t, filepath.Dir(fullPath))
		for _, mf := range files {
			if mf.Spec.Path == fullPath {
				found = mf
				return true, nil
			}
		}
		return false, nil
	})
	require.NoError(t, err, "no MediaFile at %s within %s -- manual-assign import did not land", fullPath, nonVideoImportTimeout)
	return found
}

// TestNonVideoArtistAlbumManualImport is scenario 11's Artist/Album leg.
func TestNonVideoArtistAlbumManualImport(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), scenarioTimeout)
	defer cancel()
	requireFixtureService(ctx, t, fixtureNonVideoStubService)

	rf := newRootFolder(ctx, t, "e2e11-music-rf", catalogv1alpha1.RootFolderKindMusic, "music")

	artist := &catalogv1alpha1.Artist{
		ObjectMeta: metav1.ObjectMeta{Name: uniqueName("e2e11-artist"), Namespace: Namespace},
		Spec: catalogv1alpha1.ArtistSpec{
			MusicBrainzID:     "a74b1b7f-71a5-4011-9441-d0b5e4122711", // Radiohead, test/data/metadata/musicbrainz
			QualityProfileRef: "music-standard",                       // built-in, seeded by qualityprofile.Bootstrap
			RootFolderRef:     rf.Name,
		},
	}
	require.NoError(t, k8sClient.Create(ctx, artist))
	cleanupUnlessFailed(t, func() { _ = k8sClient.Delete(context.Background(), artist) })

	waitFor(t, ctx, nonVideoConditionTimeout, "Artist "+artist.Name+" AlbumsSynced", func(ctx context.Context) (bool, error) {
		var live catalogv1alpha1.Artist
		if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(artist), &live); err != nil {
			//nolint:nilerr // keep polling
			return false, nil
		}
		return isConditionTrue(live.Status.Conditions, catalogv1alpha1.ArtistConditionMetadataReady) &&
			isConditionTrue(live.Status.Conditions, catalogv1alpha1.ArtistConditionAlbumsSynced), nil
	})

	var albums catalogv1alpha1.AlbumList
	require.NoError(t, k8sClient.List(ctx, &albums, client.InNamespace(Namespace)))
	var album *catalogv1alpha1.Album
	for i := range albums.Items {
		if albums.Items[i].Spec.ArtistRef == artist.Name {
			album = &albums.Items[i]
			break
		}
	}
	require.NotNil(t, album, "Artist %s fanned out no Album (test/data/metadata/musicbrainz/browse_releasegroups_radiohead.json names one, Kid A)", artist.Name)
	require.Equal(t, "0b56cf2b-8e64-39e0-b6d5-9a89e46be9f6", album.Spec.ReleaseGroupID, "fanned-out Album must be Kid A, the one release group the fixture browse answers with")

	relSubpath := filepath.Join(artist.Name, "track.mp3")
	fullPath := filepath.Join(rf.Spec.Path, relSubpath)
	plantBytes(t, hostPath(fullPath), []byte("id3-fixture-e2e11-music"))

	manualAssignScan(ctx, t, rf.Name, relSubpath, fileimport.ImportTarget{Kind: commonv1.MediaKindAlbum, Name: album.Name})

	mf := waitForMediaFileAt(ctx, t, fullPath)
	require.Equal(t, commonv1.MediaKindAlbum, mf.Spec.MediaRef.Kind)
	require.Equal(t, album.Name, mf.Spec.MediaRef.Name)
}

// TestNonVideoAuthorBookManualImport is scenario 11's Author/Book leg.
func TestNonVideoAuthorBookManualImport(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), scenarioTimeout)
	defer cancel()
	requireFixtureService(ctx, t, fixtureNonVideoStubService)

	rf := newRootFolder(ctx, t, "e2e11-book-rf", catalogv1alpha1.RootFolderKindBook, "books")

	author := &catalogv1alpha1.Author{
		ObjectMeta: metav1.ObjectMeta{Name: uniqueName("e2e11-author"), Namespace: Namespace},
		Spec: catalogv1alpha1.AuthorSpec{
			OpenLibraryID:     "OL21594A", // test/data/metadata/openlibrary/author_OL21594A.json
			QualityProfileRef: "ebook",    // built-in
			RootFolderRef:     rf.Name,
		},
	}
	require.NoError(t, k8sClient.Create(ctx, author))
	cleanupUnlessFailed(t, func() { _ = k8sClient.Delete(context.Background(), author) })

	waitFor(t, ctx, nonVideoConditionTimeout, "Author "+author.Name+" BooksSynced", func(ctx context.Context) (bool, error) {
		var live catalogv1alpha1.Author
		if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(author), &live); err != nil {
			//nolint:nilerr // keep polling
			return false, nil
		}
		return isConditionTrue(live.Status.Conditions, catalogv1alpha1.AuthorConditionMetadataReady) &&
			isConditionTrue(live.Status.Conditions, catalogv1alpha1.AuthorConditionBooksSynced), nil
	})

	var books catalogv1alpha1.BookList
	require.NoError(t, k8sClient.List(ctx, &books, client.InNamespace(Namespace)))
	var book *catalogv1alpha1.Book
	for i := range books.Items {
		if books.Items[i].Spec.AuthorRef != nil && *books.Items[i].Spec.AuthorRef == author.Name {
			book = &books.Items[i]
			break
		}
	}
	require.NotNil(t, book, "Author %s fanned out no Book (test/data/metadata/openlibrary/works_OL21594A.json)", author.Name)

	relSubpath := filepath.Join(author.Name, "book.epub")
	fullPath := filepath.Join(rf.Spec.Path, relSubpath)
	plantBytes(t, hostPath(fullPath), []byte("epub-fixture-e2e11-book"))

	manualAssignScan(ctx, t, rf.Name, relSubpath, fileimport.ImportTarget{Kind: commonv1.MediaKindBook, Name: book.Name})

	mf := waitForMediaFileAt(ctx, t, fullPath)
	require.Equal(t, commonv1.MediaKindBook, mf.Spec.MediaRef.Kind)
	require.Equal(t, book.Name, mf.Spec.MediaRef.Name)
}

// TestNonVideoAudiobookManualImport is scenario 11's Audiobook leg. Unlike
// the other three, Audiobook has no child kind (G2-3's own scope: "standalone
// Book" is the parentless case for books; Audiobook is always the leaf
// itself, one file per Audiobook, the same shape as Movie).
func TestNonVideoAudiobookManualImport(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), scenarioTimeout)
	defer cancel()
	requireFixtureService(ctx, t, fixtureNonVideoStubService)

	rf := newRootFolder(ctx, t, "e2e11-audiobook-rf", catalogv1alpha1.RootFolderKindAudiobook, "audiobooks")

	audiobook := &catalogv1alpha1.Audiobook{
		ObjectMeta: metav1.ObjectMeta{Name: uniqueName("e2e11-audiobook"), Namespace: Namespace},
		Spec: catalogv1alpha1.AudiobookSpec{
			ASIN:              "B0036I54I6", // test/data/metadata/audnexus/book_B0036I54I6.json
			QualityProfileRef: "audiobook",  // built-in
			RootFolderRef:     rf.Name,
		},
	}
	require.NoError(t, k8sClient.Create(ctx, audiobook))
	cleanupUnlessFailed(t, func() { _ = k8sClient.Delete(context.Background(), audiobook) })

	waitFor(t, ctx, nonVideoConditionTimeout, "Audiobook "+audiobook.Name+" MetadataReady", func(ctx context.Context) (bool, error) {
		var live catalogv1alpha1.Audiobook
		if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(audiobook), &live); err != nil {
			//nolint:nilerr // keep polling
			return false, nil
		}
		return isConditionTrue(live.Status.Conditions, catalogv1alpha1.AudiobookConditionMetadataReady), nil
	})

	relSubpath := filepath.Join(audiobook.Name, "audiobook.m4b")
	fullPath := filepath.Join(rf.Spec.Path, relSubpath)
	plantBytes(t, hostPath(fullPath), []byte("m4b-fixture-e2e11-audiobook"))

	manualAssignScan(ctx, t, rf.Name, relSubpath, fileimport.ImportTarget{Kind: commonv1.MediaKindAudiobook, Name: audiobook.Name})

	mf := waitForMediaFileAt(ctx, t, fullPath)
	require.Equal(t, commonv1.MediaKindAudiobook, mf.Spec.MediaRef.Kind)
	require.Equal(t, audiobook.Name, mf.Spec.MediaRef.Name)
}

// TestNonVideoComicIssueManualImport is scenario 11's Comic/Issue leg. It
// uses the plain "issue/<name>" target form (fileimport.ParseImportTarget's
// two-segment grammar), not the "comic/<c>/<key>" three-segment alternative
// the design note also allows: both resolve to the identical
// FileRef{Kind: Issue, Name: ...} (annotation.go's FileRef, the Key branch
// for MediaKindComic), and the two-segment form needs only the Issue's own
// object name -- which this test already has from listing Issues -- not a
// second lookup of the issue's printed number.
func TestNonVideoComicIssueManualImport(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), scenarioTimeout)
	defer cancel()
	requireFixtureService(ctx, t, fixtureNonVideoStubService)

	rf := newRootFolder(ctx, t, "e2e11-comic-rf", catalogv1alpha1.RootFolderKindComic, "comics")

	comic := &catalogv1alpha1.Comic{
		ObjectMeta: metav1.ObjectMeta{Name: uniqueName("e2e11-comic"), Namespace: Namespace},
		Spec: catalogv1alpha1.ComicSpec{
			Source:            catalogv1alpha1.ComicSourceComicVine,
			SourceID:          "4050-18257", // test/data/metadata/comicvine/volume_18257.json
			QualityProfileRef: "comic",      // built-in
			RootFolderRef:     rf.Name,
			Monitored:         ptr.To(true),
		},
	}
	require.NoError(t, k8sClient.Create(ctx, comic))
	cleanupUnlessFailed(t, func() { _ = k8sClient.Delete(context.Background(), comic) })

	waitFor(t, ctx, nonVideoConditionTimeout, "Comic "+comic.Name+" IssuesSynced", func(ctx context.Context) (bool, error) {
		var live catalogv1alpha1.Comic
		if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(comic), &live); err != nil {
			//nolint:nilerr // keep polling
			return false, nil
		}
		return isConditionTrue(live.Status.Conditions, catalogv1alpha1.ComicConditionMetadataReady) &&
			isConditionTrue(live.Status.Conditions, catalogv1alpha1.ComicConditionIssuesSynced), nil
	})

	var issues catalogv1alpha1.IssueList
	require.NoError(t, k8sClient.List(ctx, &issues, client.InNamespace(Namespace)))
	var issue *catalogv1alpha1.Issue
	for i := range issues.Items {
		if issues.Items[i].Spec.ComicRef == comic.Name {
			issue = &issues.Items[i]
			break
		}
	}
	require.NotNil(t, issue, "Comic %s fanned out no Issue (test/data/metadata/comicvine/issues_volume_18257.json)", comic.Name)

	relSubpath := filepath.Join(comic.Name, fmt.Sprintf("issue-%s.cbz", issue.Spec.Number))
	fullPath := filepath.Join(rf.Spec.Path, relSubpath)
	plantBytes(t, hostPath(fullPath), []byte("cbz-fixture-e2e11-comic"))

	manualAssignScan(ctx, t, rf.Name, relSubpath, fileimport.ImportTarget{Kind: commonv1.MediaKindIssue, Name: issue.Name})

	mf := waitForMediaFileAt(ctx, t, fullPath)
	require.Equal(t, commonv1.MediaKindIssue, mf.Spec.MediaRef.Kind)
	require.Equal(t, issue.Name, mf.Spec.MediaRef.Name)
}
