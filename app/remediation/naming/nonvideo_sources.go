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
	"context"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/remediation/mfindex"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/k8s/rowcache"
	"github.com/mediactl/clustarr/pkg/naming/catalogctx"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// The non-video kinds' naming watches (2026-11-10 design D7, §5.5): the
// inputs catalogctx.Album, Book, Audiobook and Issue and the four file
// paths read, on the item and on its container, mapped to the files whose
// proposal they move, and the RootFolder fan-out for the music, book,
// audiobook and comic roots that mediafile.FilesForRootFolder (movies and
// series) does not cover.

// The field indexes other controllers register, by the field path each is
// registered under (as app/catalog/controller/search/container.go spells
// them): an Album by spec.artistRef (the Artist controller), a Book by
// spec.authorRef (the Author controller) and an Issue by spec.comicRef
// (comic.RegisterIndexes).
const (
	albumByArtistRefIndex = ".spec.artistRef"
	bookByAuthorRefIndex  = ".spec.authorRef"
	issueByComicRefIndex  = ".spec.comicRef"
)

// albumNaming, artistNaming, bookNaming, authorNaming, audiobookNaming and
// comicNaming are the status fields a file's naming reads of each kind:
// what the contexts and the folder rules read (title or name, release
// year, the resolved path, an Album's tracks, an Audiobook's part count)
// and nothing of the phases, counts and rollups a pass must not wake for. A
// spec change (a folder override, a profile, a Comic's special version, an
// Issue's number) reaches the watch through GenerationChanged.
type albumNaming struct {
	title  string
	year   int
	path   string
	tracks string
}

func albumNamingInputs(o client.Object) albumNaming {
	a, ok := o.(*catalogv1alpha1.Album)
	if !ok {
		return albumNaming{}
	}
	out := albumNaming{path: a.Status.Path}
	if md := a.Status.Metadata; md != nil {
		out.title, out.year = md.Title, catalogctx.ReleaseYear(md.ReleaseDate)
	}
	var b strings.Builder
	for _, t := range a.Status.Tracks {
		b.WriteString(t.RecordingID)
		b.WriteByte('/')
		b.WriteString(strconv.Itoa(int(t.Medium)))
		b.WriteByte('/')
		b.WriteString(strconv.Itoa(int(t.Number)))
		b.WriteByte('/')
		b.WriteString(t.Title)
		b.WriteByte('\n')
	}
	out.tracks = b.String()
	return out
}

type namePath struct {
	name string
	path string
}

func artistNamingInputs(o client.Object) namePath {
	a, ok := o.(*catalogv1alpha1.Artist)
	if !ok {
		return namePath{}
	}
	out := namePath{path: a.Status.Path}
	if md := a.Status.Metadata; md != nil {
		out.name = md.Name
	}
	return out
}

func authorNamingInputs(o client.Object) namePath {
	a, ok := o.(*catalogv1alpha1.Author)
	if !ok {
		return namePath{}
	}
	out := namePath{path: a.Status.Path}
	if md := a.Status.Metadata; md != nil {
		out.name = md.Name
	}
	return out
}

type titleYearPath struct {
	title string
	year  int
	path  string
}

func bookNamingInputs(o client.Object) titleYearPath {
	b, ok := o.(*catalogv1alpha1.Book)
	if !ok {
		return titleYearPath{}
	}
	out := titleYearPath{path: b.Status.Path}
	if md := b.Status.Metadata; md != nil {
		out.title, out.year = md.Title, catalogctx.ReleaseYear(md.ReleaseDate)
	}
	return out
}

func comicNamingInputs(o client.Object) titleYearPath {
	c, ok := o.(*catalogv1alpha1.Comic)
	if !ok {
		return titleYearPath{}
	}
	out := titleYearPath{path: c.Status.Path}
	if md := c.Status.Metadata; md != nil {
		out.title, out.year = md.Title, int(md.Year)
	}
	return out
}

type audiobookNaming struct {
	title, authors, narrators string
	series, position          string
	year                      int
	path                      string
	parts                     int32
}

func audiobookNamingInputs(o client.Object) audiobookNaming {
	ab, ok := o.(*catalogv1alpha1.Audiobook)
	if !ok {
		return audiobookNaming{}
	}
	out := audiobookNaming{path: ab.Status.Path, parts: ab.Status.PartCount}
	if md := ab.Status.Metadata; md != nil {
		names := make([]string, 0, len(md.Authors))
		for _, a := range md.Authors {
			names = append(names, a.Name)
		}
		out.title, out.authors, out.narrators = md.Title, strings.Join(names, "\n"), strings.Join(md.Narrators, "\n")
		out.year = catalogctx.ReleaseYear(md.ReleaseDate)
		if md.Series != nil {
			out.series, out.position = md.Series.Series, md.Series.Position
		}
	}
	return out
}

// The watches' predicates: a spec change, or the status fields a render
// reads. An Issue's render reads only its spec.
func albumNamingChanged() predicate.Predicate {
	return k8s.Or(k8s.GenerationChanged(), k8s.StatusFieldChanged(albumNamingInputs))
}

func artistNamingChanged() predicate.Predicate {
	return k8s.Or(k8s.GenerationChanged(), k8s.StatusFieldChanged(artistNamingInputs))
}

func bookNamingChanged() predicate.Predicate {
	return k8s.Or(k8s.GenerationChanged(), k8s.StatusFieldChanged(bookNamingInputs))
}

func authorNamingChanged() predicate.Predicate {
	return k8s.Or(k8s.GenerationChanged(), k8s.StatusFieldChanged(authorNamingInputs))
}

func audiobookNamingChanged() predicate.Predicate {
	return k8s.Or(k8s.GenerationChanged(), k8s.StatusFieldChanged(audiobookNamingInputs))
}

func comicNamingChanged() predicate.Predicate {
	return k8s.Or(k8s.GenerationChanged(), k8s.StatusFieldChanged(comicNamingInputs))
}

func issueNamingChanged() predicate.Predicate { return k8s.GenerationChanged() }

// The map functions. Every list is served from c (the manager's cache)
// through an index and reads keys alone (rowcache.Keys: no decode of a
// row-cached kind, no deep copy), since only names are needed; a failed
// List is logged and skipped, as mediafile's are, since a map function
// cannot return an error and the file is re-rendered on its next pass.

// filesOfAlbum is an Album's files.
func filesOfAlbum(ctx context.Context, c client.Reader, o client.Object) []types.NamespacedName {
	a, ok := o.(*catalogv1alpha1.Album)
	if !ok {
		return nil
	}
	set := fileSet{}
	set.addFilesOf(ctx, c, a.Namespace, commonv1.MediaKindAlbum, a.Name)
	return set.names()
}

// filesOfArtist is an Artist's albums' files.
func filesOfArtist(ctx context.Context, c client.Reader, o client.Object) []types.NamespacedName {
	a, ok := o.(*catalogv1alpha1.Artist)
	if !ok {
		return nil
	}
	set := fileSet{}
	set.addArtistFiles(ctx, c, a.Namespace, a.Name)
	return set.names()
}

// filesOfBook is a Book's files.
func filesOfBook(ctx context.Context, c client.Reader, o client.Object) []types.NamespacedName {
	b, ok := o.(*catalogv1alpha1.Book)
	if !ok {
		return nil
	}
	set := fileSet{}
	set.addFilesOf(ctx, c, b.Namespace, commonv1.MediaKindBook, b.Name)
	return set.names()
}

// filesOfAuthor is an Author's books' files.
func filesOfAuthor(ctx context.Context, c client.Reader, o client.Object) []types.NamespacedName {
	a, ok := o.(*catalogv1alpha1.Author)
	if !ok {
		return nil
	}
	set := fileSet{}
	set.addAuthorFiles(ctx, c, a.Namespace, a.Name)
	return set.names()
}

// filesOfAudiobook is an Audiobook's files, its parts.
func filesOfAudiobook(ctx context.Context, c client.Reader, o client.Object) []types.NamespacedName {
	ab, ok := o.(*catalogv1alpha1.Audiobook)
	if !ok {
		return nil
	}
	set := fileSet{}
	set.addFilesOf(ctx, c, ab.Namespace, commonv1.MediaKindAudiobook, ab.Name)
	return set.names()
}

// filesOfIssue is an Issue's files (a pack's file once, through every
// issue it covers, since the item index lists it under each).
func filesOfIssue(ctx context.Context, c client.Reader, o client.Object) []types.NamespacedName {
	iss, ok := o.(*catalogv1alpha1.Issue)
	if !ok {
		return nil
	}
	set := fileSet{}
	set.addFilesOf(ctx, c, iss.Namespace, commonv1.MediaKindIssue, iss.Name)
	return set.names()
}

// filesOfComic is a Comic's issues' files.
func filesOfComic(ctx context.Context, c client.Reader, o client.Object) []types.NamespacedName {
	cm, ok := o.(*catalogv1alpha1.Comic)
	if !ok {
		return nil
	}
	set := fileSet{}
	set.addComicFiles(ctx, c, cm.Namespace, cm.Name)
	return set.names()
}

// nonVideoFilesOfRootFolder is a RootFolder's every artist's, author's,
// book's, audiobook's and comic's files -- a preset change renames the
// whole folder by design. The containers are listed whole from the cache
// (none is a row-cached kind, and a library holds hundreds of them where it
// holds tens of thousands of files) and filtered on spec.rootFolderRef; a
// Book is reached through its own rootFolderRef and through its Author's,
// so one overriding its Author's root gets a pass under either, which
// renders the same path.
func nonVideoFilesOfRootFolder(ctx context.Context, c client.Reader, o client.Object) []types.NamespacedName {
	rf, ok := o.(*catalogv1alpha1.RootFolder)
	if !ok {
		return nil
	}
	ns, set := rf.Namespace, fileSet{}
	warn := func(what string, err error) {
		logging.FromContext(ctx).Warn("naming: list a RootFolder's "+what+" to re-name their files", "rootFolder", rf.Name, "err", err)
	}
	var artists catalogv1alpha1.ArtistList
	if err := c.List(ctx, &artists, client.InNamespace(ns), client.UnsafeDisableDeepCopy); err != nil {
		warn("artists", err)
	}
	for i := range artists.Items {
		if artists.Items[i].Spec.RootFolderRef == rf.Name {
			set.addArtistFiles(ctx, c, ns, artists.Items[i].Name)
		}
	}
	var authors catalogv1alpha1.AuthorList
	if err := c.List(ctx, &authors, client.InNamespace(ns), client.UnsafeDisableDeepCopy); err != nil {
		warn("authors", err)
	}
	for i := range authors.Items {
		if authors.Items[i].Spec.RootFolderRef == rf.Name {
			set.addAuthorFiles(ctx, c, ns, authors.Items[i].Name)
		}
	}
	var books catalogv1alpha1.BookList
	if err := c.List(ctx, &books, client.InNamespace(ns), client.UnsafeDisableDeepCopy); err != nil {
		warn("books", err)
	}
	for i := range books.Items {
		if ptr.Deref(books.Items[i].Spec.RootFolderRef, "") == rf.Name {
			set.addFilesOf(ctx, c, ns, commonv1.MediaKindBook, books.Items[i].Name)
		}
	}
	var audiobooks catalogv1alpha1.AudiobookList
	if err := c.List(ctx, &audiobooks, client.InNamespace(ns), client.UnsafeDisableDeepCopy); err != nil {
		warn("audiobooks", err)
	}
	for i := range audiobooks.Items {
		if audiobooks.Items[i].Spec.RootFolderRef == rf.Name {
			set.addFilesOf(ctx, c, ns, commonv1.MediaKindAudiobook, audiobooks.Items[i].Name)
		}
	}
	var comics catalogv1alpha1.ComicList
	if err := c.List(ctx, &comics, client.InNamespace(ns), client.UnsafeDisableDeepCopy); err != nil {
		warn("comics", err)
	}
	for i := range comics.Items {
		if comics.Items[i].Spec.RootFolderRef == rf.Name {
			set.addComicFiles(ctx, c, ns, comics.Items[i].Name)
		}
	}
	return set.names()
}

// fileSet deduplicates the files a fan-out collects: a pack's file is
// reached once through each issue it covers.
type fileSet map[types.NamespacedName]struct{}

// addFilesOf adds the files backing the named item, through the loop's
// item index.
func (s fileSet) addFilesOf(ctx context.Context, c client.Reader, ns string, kind commonv1.MediaKind, name string) {
	files, err := rowcache.Keys(ctx, c, &catalogv1alpha1.MediaFileList{}, client.InNamespace(ns), client.MatchingFields{mfindex.Item: mfindex.ItemKey(kind, name)})
	if err != nil {
		logging.FromContext(ctx).Warn("naming: list an item's files to re-name them", "kind", kind, "name", name, "err", err)
		return
	}
	for _, f := range files {
		s[f] = struct{}{}
	}
}

// addArtistFiles adds the files of every Album of artist.
func (s fileSet) addArtistFiles(ctx context.Context, c client.Reader, ns, artist string) {
	albums, err := rowcache.Keys(ctx, c, &catalogv1alpha1.AlbumList{}, client.InNamespace(ns), client.MatchingFields{albumByArtistRefIndex: artist})
	if err != nil {
		logging.FromContext(ctx).Warn("naming: list an artist's albums to re-name their files", "artist", artist, "err", err)
		return
	}
	for _, a := range albums {
		s.addFilesOf(ctx, c, ns, commonv1.MediaKindAlbum, a.Name)
	}
}

// addAuthorFiles adds the files of every Book of author.
func (s fileSet) addAuthorFiles(ctx context.Context, c client.Reader, ns, author string) {
	books, err := rowcache.Keys(ctx, c, &catalogv1alpha1.BookList{}, client.InNamespace(ns), client.MatchingFields{bookByAuthorRefIndex: author})
	if err != nil {
		logging.FromContext(ctx).Warn("naming: list an author's books to re-name their files", "author", author, "err", err)
		return
	}
	for _, b := range books {
		s.addFilesOf(ctx, c, ns, commonv1.MediaKindBook, b.Name)
	}
}

// addComicFiles adds the files of every Issue of comic.
func (s fileSet) addComicFiles(ctx context.Context, c client.Reader, ns, comic string) {
	issues, err := rowcache.Keys(ctx, c, &catalogv1alpha1.IssueList{}, client.InNamespace(ns), client.MatchingFields{issueByComicRefIndex: comic})
	if err != nil {
		logging.FromContext(ctx).Warn("naming: list a comic's issues to re-name their files", "comic", comic, "err", err)
		return
	}
	for _, iss := range issues {
		s.addFilesOf(ctx, c, ns, commonv1.MediaKindIssue, iss.Name)
	}
}

func (s fileSet) names() []types.NamespacedName {
	if len(s) == 0 {
		return nil
	}
	out := make([]types.NamespacedName, 0, len(s))
	for k := range s {
		out = append(out, k)
	}
	return out
}
