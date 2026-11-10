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

package naming

import (
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/remediation/mfindex"
	"github.com/mediactl/clustarr/pkg/k8s"
)

func nvMeta(name string) metav1.ObjectMeta { return metav1.ObjectMeta{Namespace: "media", Name: name} }

// nonVideoFixture is a namespace with one tree of each non-video kind under
// its own RootFolder, and a movie root nothing non-video lives under: the
// Artist boc with two Albums (one file each), the Author pratchett with the
// Book guards (one file) and the standalone Book dune under books2, the
// Audiobook guards-audio (two parts) and the Comic saga with two Issues,
// one file each plus a pack file covering both.
func nonVideoFixture(t *testing.T) client.Client {
	t.Helper()
	file := func(name string, ref commonv1.MediaRef) *catalogv1alpha1.MediaFile {
		return &catalogv1alpha1.MediaFile{ObjectMeta: nvMeta(name), Spec: catalogv1alpha1.MediaFileSpec{MediaRef: ref, Path: "/data/" + name}}
	}
	objs := []client.Object{
		&catalogv1alpha1.Artist{ObjectMeta: nvMeta("boc"), Spec: catalogv1alpha1.ArtistSpec{MusicBrainzID: "mb", RootFolderRef: "music"}},
		&catalogv1alpha1.Album{ObjectMeta: nvMeta("mhtrtc"), Spec: catalogv1alpha1.AlbumSpec{ArtistRef: "boc", ReleaseGroupID: "rg1"}},
		&catalogv1alpha1.Album{ObjectMeta: nvMeta("geogaddi"), Spec: catalogv1alpha1.AlbumSpec{ArtistRef: "boc", ReleaseGroupID: "rg2"}},
		&catalogv1alpha1.Artist{ObjectMeta: nvMeta("other-artist"), Spec: catalogv1alpha1.ArtistSpec{MusicBrainzID: "mb2", RootFolderRef: "music2"}},
		&catalogv1alpha1.Album{ObjectMeta: nvMeta("other-album"), Spec: catalogv1alpha1.AlbumSpec{ArtistRef: "other-artist", ReleaseGroupID: "rg3"}},
		file("mhtrtc-03", commonv1.MediaRef{Kind: commonv1.MediaKindAlbum, Name: "mhtrtc", Track: "rec-3"}),
		file("geogaddi-01", commonv1.MediaRef{Kind: commonv1.MediaKindAlbum, Name: "geogaddi", Track: "rec-1"}),
		file("other-album-01", commonv1.MediaRef{Kind: commonv1.MediaKindAlbum, Name: "other-album", Track: "rec-9"}),

		&catalogv1alpha1.Author{ObjectMeta: nvMeta("pratchett"), Spec: catalogv1alpha1.AuthorSpec{OpenLibraryID: "OL1A", RootFolderRef: "books"}},
		&catalogv1alpha1.Book{ObjectMeta: nvMeta("guards"), Spec: catalogv1alpha1.BookSpec{AuthorRef: ptr.To("pratchett"), WorkID: "w1"}},
		&catalogv1alpha1.Book{ObjectMeta: nvMeta("dune"), Spec: catalogv1alpha1.BookSpec{WorkID: "w2", RootFolderRef: ptr.To("books2")}},
		file("guards-epub", commonv1.MediaRef{Kind: commonv1.MediaKindBook, Name: "guards"}),
		file("dune-epub", commonv1.MediaRef{Kind: commonv1.MediaKindBook, Name: "dune"}),

		&catalogv1alpha1.Audiobook{ObjectMeta: nvMeta("guards-audio"), Spec: catalogv1alpha1.AudiobookSpec{ASIN: "B1", RootFolderRef: "audiobooks"}},
		file("guards-audio-01", commonv1.MediaRef{Kind: commonv1.MediaKindAudiobook, Name: "guards-audio"}),
		file("guards-audio-02", commonv1.MediaRef{Kind: commonv1.MediaKindAudiobook, Name: "guards-audio"}),

		&catalogv1alpha1.Comic{ObjectMeta: nvMeta("saga"), Spec: catalogv1alpha1.ComicSpec{Source: catalogv1alpha1.ComicSourceComicVine, SourceID: "1", RootFolderRef: "comics"}},
		&catalogv1alpha1.Issue{ObjectMeta: nvMeta("saga-001"), Spec: catalogv1alpha1.IssueSpec{ComicRef: "saga", Number: "001"}},
		&catalogv1alpha1.Issue{ObjectMeta: nvMeta("saga-002"), Spec: catalogv1alpha1.IssueSpec{ComicRef: "saga", Number: "002"}},
		file("saga-001-cbz", commonv1.MediaRef{Kind: commonv1.MediaKindIssue, Name: "saga-001"}),
		file("saga-pack", commonv1.MediaRef{Kind: commonv1.MediaKindIssue, Name: "saga-001", Keys: []string{"saga-001", "saga-002"}}),

		&catalogv1alpha1.Movie{ObjectMeta: nvMeta("heat"), Spec: catalogv1alpha1.MovieSpec{TmdbID: 949, RootFolderRef: "movies"}},
		file("heat-mkv", commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "heat"}),
	}
	return fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(objs...).
		WithIndex(&catalogv1alpha1.MediaFile{}, mfindex.Item, mfindex.Extract).
		WithIndex(&catalogv1alpha1.Album{}, albumByArtistRefIndex, func(o client.Object) []string {
			return []string{o.(*catalogv1alpha1.Album).Spec.ArtistRef}
		}).
		WithIndex(&catalogv1alpha1.Book{}, bookByAuthorRefIndex, func(o client.Object) []string {
			return []string{ptr.Deref(o.(*catalogv1alpha1.Book).Spec.AuthorRef, "")}
		}).
		WithIndex(&catalogv1alpha1.Issue{}, issueByComicRefIndex, func(o client.Object) []string {
			return []string{o.(*catalogv1alpha1.Issue).Spec.ComicRef}
		}).
		Build()
}

func names(nns []types.NamespacedName) []string {
	out := make([]string, 0, len(nns))
	for _, nn := range nns {
		out = append(out, nn.Name)
	}
	slices.Sort(out)
	return out
}

// TestNonVideoWatchesMapToTheFilesTheyRename covers the seven map functions
// and the RootFolder fan-out, each through the index it reads: an item's
// own files, a container's children's files (a pack's file once, not once
// per issue), and a root's every tree's files, nothing of another root's.
func TestNonVideoWatchesMapToTheFilesTheyRename(t *testing.T) {
	c := nonVideoFixture(t)
	ctx := t.Context()

	assert.Equal(t, []string{"mhtrtc-03"}, names(filesOfAlbum(ctx, c, &catalogv1alpha1.Album{ObjectMeta: nvMeta("mhtrtc")})))
	assert.Equal(t, []string{"geogaddi-01", "mhtrtc-03"}, names(filesOfArtist(ctx, c, &catalogv1alpha1.Artist{ObjectMeta: nvMeta("boc")})))
	assert.Equal(t, []string{"guards-epub"}, names(filesOfBook(ctx, c, &catalogv1alpha1.Book{ObjectMeta: nvMeta("guards")})))
	assert.Equal(t, []string{"guards-epub"}, names(filesOfAuthor(ctx, c, &catalogv1alpha1.Author{ObjectMeta: nvMeta("pratchett")})))
	assert.Equal(t, []string{"guards-audio-01", "guards-audio-02"}, names(filesOfAudiobook(ctx, c, &catalogv1alpha1.Audiobook{ObjectMeta: nvMeta("guards-audio")})))
	assert.Equal(t, []string{"saga-pack"}, names(filesOfIssue(ctx, c, &catalogv1alpha1.Issue{ObjectMeta: nvMeta("saga-002")})),
		"a pack's file is reached through every issue it covers")
	assert.Equal(t, []string{"saga-001-cbz", "saga-pack"}, names(filesOfComic(ctx, c, &catalogv1alpha1.Comic{ObjectMeta: nvMeta("saga")})),
		"the pack's file once, not once per issue")

	root := func(name string) *catalogv1alpha1.RootFolder {
		return &catalogv1alpha1.RootFolder{ObjectMeta: nvMeta(name)}
	}
	assert.Equal(t, []string{"geogaddi-01", "mhtrtc-03"}, names(nonVideoFilesOfRootFolder(ctx, c, root("music"))))
	assert.Equal(t, []string{"other-album-01"}, names(nonVideoFilesOfRootFolder(ctx, c, root("music2"))))
	assert.Equal(t, []string{"guards-epub"}, names(nonVideoFilesOfRootFolder(ctx, c, root("books"))), "an author's books through the author")
	assert.Equal(t, []string{"dune-epub"}, names(nonVideoFilesOfRootFolder(ctx, c, root("books2"))), "a standalone book through its own root")
	assert.Equal(t, []string{"guards-audio-01", "guards-audio-02"}, names(nonVideoFilesOfRootFolder(ctx, c, root("audiobooks"))))
	assert.Equal(t, []string{"saga-001-cbz", "saga-pack"}, names(nonVideoFilesOfRootFolder(ctx, c, root("comics"))))
	assert.Empty(t, nonVideoFilesOfRootFolder(ctx, c, root("movies")), "a movie root's files are mediafile.FilesForRootFolder's")

	assert.Empty(t, filesOfAlbum(ctx, c, &catalogv1alpha1.Artist{ObjectMeta: nvMeta("boc")}), "wrong type maps to nothing")
	assert.Empty(t, nonVideoFilesOfRootFolder(ctx, c, &catalogv1alpha1.Album{ObjectMeta: nvMeta("mhtrtc")}))
}

// TestNonVideoNamingPredicatesIgnoreStatusChurn holds each watch to the
// fields a render reads: a phase or a count moving must not wake every
// file's pass; a title, a path, a track, a part count or a spec change
// must.
func TestNonVideoNamingPredicatesIgnoreStatusChurn(t *testing.T) {
	update := func(old, cur client.Object) event.UpdateEvent {
		return event.UpdateEvent{ObjectOld: old, ObjectNew: cur}
	}
	date := func(y int) *metav1.Time {
		d := metav1.NewTime(time.Date(y, time.June, 1, 0, 0, 0, 0, time.UTC))
		return &d
	}

	album := func(title string, year int, phase commonv1.LeafPhase, tracks ...catalogv1alpha1.Track) *catalogv1alpha1.Album {
		return &catalogv1alpha1.Album{Status: catalogv1alpha1.AlbumStatus{
			Phase: phase, Metadata: &catalogv1alpha1.AlbumMetadata{Title: title, ReleaseDate: date(year)}, Tracks: tracks,
		}}
	}
	tr := catalogv1alpha1.Track{RecordingID: "r1", Medium: 1, Number: 1, Title: "Intro"}
	p := albumNamingChanged()
	assert.False(t, p.Update(update(album("Kid A", 2000, commonv1.LeafPhaseWanted, tr), album("Kid A", 2000, commonv1.LeafPhaseImported, tr))), "phase churn")
	assert.True(t, p.Update(update(album("Kid A", 2000, "", tr), album("Kid A (Deluxe)", 2000, "", tr))), "title")
	assert.True(t, p.Update(update(album("Kid A", 2000, "", tr), album("Kid A", 2001, "", tr))), "release year")
	renamed := tr
	renamed.Title = "Everything in Its Right Place"
	assert.True(t, p.Update(update(album("Kid A", 2000, "", tr), album("Kid A", 2000, "", renamed))), "a track's title")
	assert.True(t, p.Update(update(album("Kid A", 2000, "", tr), album("Kid A", 2000, ""))), "the tracks going away")
	withPath := album("Kid A", 2000, "", tr)
	withPath.Status.Path = "/data/music/Radiohead/Kid A"
	assert.True(t, p.Update(update(album("Kid A", 2000, "", tr), withPath)), "the resolved path")
	gen := album("Kid A", 2000, "", tr)
	gen.Generation = 2
	assert.True(t, p.Update(update(album("Kid A", 2000, "", tr), gen)), "a spec change")

	artist := func(name, path string, count int32) *catalogv1alpha1.Artist {
		return &catalogv1alpha1.Artist{Status: catalogv1alpha1.ArtistStatus{Path: path, AlbumFileCount: count, Metadata: &catalogv1alpha1.ArtistMetadata{Name: name}}}
	}
	p = artistNamingChanged()
	assert.False(t, p.Update(update(artist("Radiohead", "/m/Radiohead", 1), artist("Radiohead", "/m/Radiohead", 2))), "a count")
	assert.True(t, p.Update(update(artist("Radiohead", "/m/Radiohead", 1), artist("Radiohead", "/m/Radiohead (band)", 1))), "the path")
	assert.True(t, p.Update(update(artist("Radiohead", "", 1), artist("Radio Head", "", 1))), "the name")

	audiobook := func(title string, parts int32, phase commonv1.LeafPhase) *catalogv1alpha1.Audiobook {
		return &catalogv1alpha1.Audiobook{Status: catalogv1alpha1.AudiobookStatus{
			Phase: phase, PartCount: parts, Metadata: &catalogv1alpha1.AudiobookMetadata{Title: title, Narrators: []string{"N"}},
		}}
	}
	p = audiobookNamingChanged()
	assert.False(t, p.Update(update(audiobook("Dune", 12, commonv1.LeafPhaseWanted), audiobook("Dune", 12, commonv1.LeafPhaseImported))), "phase churn")
	assert.True(t, p.Update(update(audiobook("Dune", 11, ""), audiobook("Dune", 12, ""))), "the part count names every part's {PartCount}")
	assert.True(t, p.Update(update(audiobook("Dune", 12, ""), audiobook("Dune Messiah", 12, ""))), "the title")

	comic := func(title string, year int32, path string, issues int32) *catalogv1alpha1.Comic {
		return &catalogv1alpha1.Comic{Status: catalogv1alpha1.ComicStatus{Path: path, Metadata: &catalogv1alpha1.ComicMetadata{Title: title, Year: year, IssueCount: issues}}}
	}
	p = comicNamingChanged()
	assert.False(t, p.Update(update(comic("Saga", 2012, "/c/Saga", 60), comic("Saga", 2012, "/c/Saga", 61))), "an issue count")
	assert.True(t, p.Update(update(comic("Saga", 2012, "/c/Saga", 60), comic("Saga", 2012, "/c/Saga (2012)", 60))), "the path")
	assert.True(t, p.Update(update(comic("Saga", 2012, "", 60), comic("Saga: Compendium", 2012, "", 60))), "the title")

	book := func(title string, year int) *catalogv1alpha1.Book {
		return &catalogv1alpha1.Book{Status: catalogv1alpha1.BookStatus{Metadata: &catalogv1alpha1.BookMetadata{Title: title, ReleaseDate: date(year)}}}
	}
	p = bookNamingChanged()
	assert.False(t, p.Update(update(book("Dune", 1965), book("Dune", 1965))), "nothing moved")
	assert.True(t, p.Update(update(book("Dune", 1965), book("Dune", 1966))), "the year")

	p = issueNamingChanged()
	i1 := &catalogv1alpha1.Issue{Spec: catalogv1alpha1.IssueSpec{Number: "1"}}
	i2 := &catalogv1alpha1.Issue{Spec: catalogv1alpha1.IssueSpec{Number: "1"}}
	i2.Generation = 2
	assert.True(t, p.Update(update(i1, i2)), "a spec change")
	i2.Generation = 0
	i2.Status.Phase = commonv1.LeafPhaseImported
	assert.False(t, p.Update(update(i1, i2)), "status churn")

	require.True(t, p.Create(event.CreateEvent{Object: i1}), "creates pass")
}
