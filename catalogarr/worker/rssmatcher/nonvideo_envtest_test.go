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
package rssmatcher_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/worker/rssmatcher"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// nonVideoLibrary is one of each non-video kind, with the metadata the
// gateway would have written: Radiohead's Kid A, Frank Herbert's Dune (by
// its sort name "Herbert, Frank"), Stephen King and Peter Straub's The
// Talisman as an audiobook, and issue 50 of Batman.
type nonVideoLibrary struct {
	artist, album, author, book, audiobook, comic, issue string
}

func createNonVideoLibrary(t *testing.T, ctx context.Context, c client.Client, ns, profile string) nonVideoLibrary {
	t.Helper()
	lib := nonVideoLibrary{
		artist: "radiohead", album: "radiohead-kid-a", author: "frank-herbert", book: "dune",
		audiobook: "the-talisman", comic: "batman-2016", issue: "batman-2016-50",
	}
	status := func(obj any, err error) {
		t.Helper()
		require.NoError(t, err)
	}

	require.NoError(t, c.Create(ctx, &catalogv1alpha1.Artist{
		ObjectMeta: metav1.ObjectMeta{Name: lib.artist, Namespace: ns},
		Spec: catalogv1alpha1.ArtistSpec{
			MusicBrainzID: "a74b1b7f-71a5-4011-9441-d0b5e4122711", QualityProfileRef: profile, RootFolderRef: "music",
		},
	}))
	status(k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrMetadata, catalogac.Artist(lib.artist, ns).WithStatus(
		catalogac.ArtistStatus().WithMetadata(catalogac.ArtistMetadata().WithName("Radiohead").WithSortName("Radiohead")))))
	require.NoError(t, c.Create(ctx, &catalogv1alpha1.Album{
		ObjectMeta: metav1.ObjectMeta{Name: lib.album, Namespace: ns},
		Spec:       catalogv1alpha1.AlbumSpec{ArtistRef: lib.artist, ReleaseGroupID: "b8048f24-c026-3398-b23a-b5e30716ea6f"},
	}))
	status(k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrMetadata, catalogac.Album(lib.album, ns).WithStatus(
		catalogac.AlbumStatus().WithMetadata(catalogac.AlbumMetadata().WithTitle("Kid A").
			WithReleaseDate(metav1.NewTime(time.Date(2000, 10, 2, 0, 0, 0, 0, time.UTC)))))))

	require.NoError(t, c.Create(ctx, &catalogv1alpha1.Author{
		ObjectMeta: metav1.ObjectMeta{Name: lib.author, Namespace: ns},
		Spec:       catalogv1alpha1.AuthorSpec{OpenLibraryID: "OL79034A", QualityProfileRef: profile, RootFolderRef: "books"},
	}))
	status(k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrMetadata, catalogac.Author(lib.author, ns).WithStatus(
		catalogac.AuthorStatus().WithMetadata(catalogac.AuthorMetadata().WithName("Frank Herbert").WithSortName("Herbert, Frank")))))
	require.NoError(t, c.Create(ctx, &catalogv1alpha1.Book{
		ObjectMeta: metav1.ObjectMeta{Name: lib.book, Namespace: ns},
		Spec:       catalogv1alpha1.BookSpec{AuthorRef: ptr.To(lib.author), WorkID: "OL893415W"},
	}))
	status(k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrMetadata, catalogac.Book(lib.book, ns).WithStatus(
		catalogac.BookStatus().WithMetadata(catalogac.BookMetadata().WithTitle("Dune")))))

	require.NoError(t, c.Create(ctx, &catalogv1alpha1.Audiobook{
		ObjectMeta: metav1.ObjectMeta{Name: lib.audiobook, Namespace: ns},
		Spec:       catalogv1alpha1.AudiobookSpec{ASIN: "B00BATUAHO", QualityProfileRef: profile, RootFolderRef: "audiobooks"},
	}))
	status(k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrMetadata, catalogac.Audiobook(lib.audiobook, ns).WithStatus(
		catalogac.AudiobookStatus().WithMetadata(catalogac.AudiobookMetadata().WithTitle("The Talisman").WithAuthors(
			catalogac.NamedRef().WithName("Stephen King"), catalogac.NamedRef().WithName("Peter Straub"))))))

	require.NoError(t, c.Create(ctx, &catalogv1alpha1.Comic{
		ObjectMeta: metav1.ObjectMeta{Name: lib.comic, Namespace: ns},
		Spec: catalogv1alpha1.ComicSpec{
			Source: catalogv1alpha1.ComicSourceComicVine, SourceID: "91273", QualityProfileRef: profile, RootFolderRef: "comics",
		},
	}))
	status(k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrMetadata, catalogac.Comic(lib.comic, ns).WithStatus(
		catalogac.ComicStatus().WithMetadata(catalogac.ComicMetadata().WithTitle("Batman")))))
	require.NoError(t, c.Create(ctx, &catalogv1alpha1.Issue{
		ObjectMeta: metav1.ObjectMeta{Name: lib.issue, Namespace: ns},
		Spec:       catalogv1alpha1.IssueSpec{ComicRef: lib.comic, Number: "50", CalculatedNumberCentis: 5000},
	}))
	status(k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarr, catalogac.Issue(lib.issue, ns).WithStatus(
		catalogac.IssueStatus().WithDate(metav1.NewTime(time.Date(2018, 7, 4, 0, 0, 0, 0, time.UTC))))))

	eventually(t, 15*time.Second, "every non-video item's metadata to reach the cache", func() bool {
		var ar catalogv1alpha1.Artist
		var al catalogv1alpha1.Album
		var au catalogv1alpha1.Author
		var b catalogv1alpha1.Book
		var ab catalogv1alpha1.Audiobook
		var co catalogv1alpha1.Comic
		var is catalogv1alpha1.Issue
		get := func(name string, o client.Object) bool {
			return c.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, o) == nil
		}
		return get(lib.artist, &ar) && ar.Status.Metadata != nil &&
			get(lib.album, &al) && al.Status.Metadata != nil &&
			get(lib.author, &au) && au.Status.Metadata != nil &&
			get(lib.book, &b) && b.Status.Metadata != nil &&
			get(lib.audiobook, &ab) && ab.Status.Metadata != nil &&
			get(lib.comic, &co) && co.Status.Metadata != nil &&
			get(lib.issue, &is) && is.Status.Date != nil
	})
	return lib
}

// nonVideoRSSRelease is a firehose release of kind with the names indexarr's
// projection puts on it.
func nonVideoRSSRelease(kind commonv1.MediaKind, title, parsedTitle string) schema.Release {
	return schema.Release{
		Info: commonv1.ReleaseInfo{
			GUID: "guid-" + string(kind), IndexerRef: "my-indexer", IndexerName: "my-indexer",
			Protocol: commonv1.ProtocolTorrent, Title: title, SizeBytes: 400 << 20,
			MagnetURL:   "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567",
			PublishedAt: metaTime(relNow.Add(-time.Hour)),
		},
		ParsedTitle: parsedTitle,
		Kind:        kind,
		FetchedAt:   relNow,
	}
}

// TestMatch_NonVideoKinds: an album, a book, an audiobook and a comic issue
// are matched from the firehose by the names on the release -- the way
// Lidarr, Readarr and Mylar find them -- where before every non-video
// release matched nothing and those items could only be searched for.
func TestMatch_NonVideoKinds(t *testing.T) {
	ctx := context.Background()
	mgr := newTestManager(t)
	c := mgr.GetClient()
	ns := newNamespace(t, ctx, c)
	lib := createNonVideoLibrary(t, ctx, c, ns, "anything")

	album := nonVideoRSSRelease(commonv1.MediaKindAlbum, "Radiohead - Kid A (2000) [FLAC]", "Radiohead - Kid A")
	album.Artist, album.Album = "Radiohead", "Kid A"
	book := nonVideoRSSRelease(commonv1.MediaKindBook, "Herbert, Frank - Dune (1965) [EPUB]", "Dune")
	book.Author = "Herbert, Frank"
	audiobook := nonVideoRSSRelease(commonv1.MediaKindAudiobook, "Stephen King & Peter Straub - The Talisman (Unabridged) [M4B]", "The Talisman")
	audiobook.Author = "Stephen King & Peter Straub"
	comic := nonVideoRSSRelease(commonv1.MediaKindComic, "Batman 050 (2018) (Digital) (Zone-Empire)", "Batman")
	comic.Issue = "050"

	for _, tc := range []struct {
		name string
		rel  schema.Release
		want commonv1.MediaRef
	}{
		{"an album by its artist and title", album, commonv1.MediaRef{Kind: commonv1.MediaKindAlbum, Name: lib.album}},
		{"a book by its author, credited 'Last, First', and title", book, commonv1.MediaRef{Kind: commonv1.MediaKindBook, Name: lib.book}},
		{"an audiobook by one of its co-credited authors and title", audiobook, commonv1.MediaRef{Kind: commonv1.MediaKindAudiobook, Name: lib.audiobook}},
		{"an issue by its comic and number, padding and all", comic, commonv1.MediaRef{Kind: commonv1.MediaKindIssue, Name: lib.issue}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			refs, err := rssmatcher.Match(ctx, c, ns, tc.rel)
			require.NoError(t, err)
			assert.Equal(t, []commonv1.MediaRef{tc.want}, refs)
		})
	}

	misses := map[string]schema.Release{}
	wrongArtist := album
	wrongArtist.Artist = "Muse"
	misses["another artist's album of the same title"] = wrongArtist
	wrongAlbum := album
	wrongAlbum.Album = "Amnesiac"
	misses["another album by the same artist"] = wrongAlbum
	wrongIssue := comic
	wrongIssue.Issue = "051"
	misses["another issue of the comic"] = wrongIssue
	ebookAsAudiobook := book
	ebookAsAudiobook.Kind = commonv1.MediaKindAudiobook
	misses["an ebook is never an audiobook's"] = ebookAsAudiobook
	unclassified := album
	unclassified.Kind = ""
	misses["an unclassified release is not guessed at"] = unclassified
	for name, rel := range misses {
		t.Run(name, func(t *testing.T) {
			refs, err := rssmatcher.Match(ctx, c, ns, rel)
			require.NoError(t, err)
			assert.Empty(t, refs)
		})
	}

	// An unmonitored container hides its items, as Lidarr's and Readarr's
	// RSS "Artist/Author is not monitored" rules do.
	var artist catalogv1alpha1.Artist
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: ns, Name: lib.artist}, &artist))
	artist.Spec.Monitored = ptr.To(false)
	require.NoError(t, c.Update(ctx, &artist))
	eventually(t, 10*time.Second, "an unmonitored artist to hide its album", func() bool {
		refs, err := rssmatcher.Match(ctx, c, ns, album)
		return err == nil && len(refs) == 0
	})
}

// TestMatch_NonVideoAmbiguousNamesMatchNothing: two albums one release could
// be for -- an artist with two albums of one title -- match neither, as
// Sonarr refuses a title that names two series.
func TestMatch_NonVideoAmbiguousNamesMatchNothing(t *testing.T) {
	ctx := context.Background()
	mgr := newTestManager(t)
	c := mgr.GetClient()
	ns := newNamespace(t, ctx, c)
	lib := createNonVideoLibrary(t, ctx, c, ns, "anything")

	require.NoError(t, c.Create(ctx, &catalogv1alpha1.Album{
		ObjectMeta: metav1.ObjectMeta{Name: "radiohead-kid-a-live", Namespace: ns},
		Spec:       catalogv1alpha1.AlbumSpec{ArtistRef: lib.artist, ReleaseGroupID: "00000000-0000-0000-0000-000000000001"},
	}))
	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrMetadata, catalogac.Album("radiohead-kid-a-live", ns).WithStatus(
		catalogac.AlbumStatus().WithMetadata(catalogac.AlbumMetadata().WithTitle("Kid A"))))
	require.NoError(t, err)

	album := nonVideoRSSRelease(commonv1.MediaKindAlbum, "Radiohead - Kid A (2000) [FLAC]", "Radiohead - Kid A")
	album.Artist, album.Album = "Radiohead", "Kid A"
	eventually(t, 10*time.Second, "the second Kid A to make the title ambiguous", func() bool {
		var al catalogv1alpha1.Album
		if c.Get(ctx, client.ObjectKey{Namespace: ns, Name: "radiohead-kid-a-live"}, &al) != nil || al.Status.Metadata == nil {
			return false
		}
		refs, err := rssmatcher.Match(ctx, c, ns, album)
		return err == nil && len(refs) == 0
	})
}

// TestHandler_NonVideoReleaseTakesTheGrabPath is the non-video half of
// §8.7's clause end to end, through the REAL decision engine: a matched
// album release is decided against the album's identity and grabbed for it.
func TestHandler_NonVideoReleaseTakesTheGrabPath(t *testing.T) {
	ctx := context.Background()
	mgr := newTestManager(t)
	c := mgr.GetClient()
	ns := newNamespace(t, ctx, c)

	qp := &catalogv1alpha1.QualityProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "rss-music-lossless"},
		Spec: catalogv1alpha1.QualityProfileSpec{
			MediaKind: catalogv1alpha1.ProfileMediaKindMusic,
			Cutoff:    "FLAC",
			Tiers: []catalogv1alpha1.Tier{
				{Name: "FLAC", Qualities: []string{"FLAC"}},
				{Name: "High", Qualities: []string{"High"}},
			},
		},
	}
	if err := c.Create(ctx, qp); err != nil {
		require.True(t, client.IgnoreAlreadyExists(err) == nil, "create profile: %v", err)
	}
	lib := createNonVideoLibrary(t, ctx, c, ns, qp.Name)
	createIndexer(t, ctx, c, ns, "my-indexer")
	createDelayProfile(t, ctx, c, ns, 0, true)

	album := nonVideoRSSRelease(commonv1.MediaKindAlbum, "Radiohead - Kid A (2000) [FLAC]", "Radiohead - Kid A")
	album.Artist, album.Album = "Radiohead", "Kid A"

	h := rssmatcher.NewHandler(rssmatcher.Deps{Client: c, Reader: mgr.GetAPIReader(), Bus: newTestBus(t), Now: func() time.Time { return relNow }})
	eventually(t, 15*time.Second, "the album release to be matched, approved and grabbed", func() bool {
		var profile catalogv1alpha1.QualityProfile
		if c.Get(ctx, client.ObjectKey{Name: qp.Name}, &profile) != nil {
			return false
		}
		if err := h.Handle(ctx, releaseMessage(t, ns, "my-indexer", album)); err != nil {
			t.Fatalf("Handle: %v", err)
		}
		var downloads downloadv1alpha1.DownloadList
		return c.List(ctx, &downloads, client.InNamespace(ns)) == nil && len(downloads.Items) == 1
	})

	var downloads downloadv1alpha1.DownloadList
	require.NoError(t, mgr.GetAPIReader().List(ctx, &downloads, client.InNamespace(ns)))
	require.Len(t, downloads.Items, 1)
	dl := downloads.Items[0]
	assert.Equal(t, commonv1.MediaRef{Kind: commonv1.MediaKindAlbum, Name: lib.album}, dl.Spec.Target)
	assert.Equal(t, downloadv1alpha1.GrabSourceRSS, dl.Spec.GrabbedBy)
	assert.Equal(t, qp.Name, dl.Spec.QualityProfileRef, "the album ranks under its artist's profile")
	assert.Equal(t, "FLAC", dl.Spec.Release.Quality.Name)
}
