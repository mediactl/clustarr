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

package fileimport

import (
	"context"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"
	"unicode"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/import/importtarget"
	"github.com/mediactl/clustarr/pkg/naming"
	"github.com/mediactl/clustarr/pkg/naming/catalogctx"
	"github.com/mediactl/clustarr/pkg/quality"
)

// errBlocked marks a resolution failure that no redelivery can fix -- the
// target does not exist, or names no root folder or profile -- so the
// caller reports status.import=blocked with the message instead of retrying.
// Anything else resolveNonVideo returns (an apiserver read failing, metadata
// not fetched yet) is retried like the movie path's equivalents.
var errBlocked = errors.New("blocked")

func blocked(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errBlocked, fmt.Sprintf(format, args...))
}

// blockedMessage strips the sentinel prefix for the status message.
func blockedMessage(err error) string {
	return strings.TrimPrefix(err.Error(), errBlocked.Error()+": ")
}

// nonVideoPlan is everything a non-video import needs, resolved from the
// target item and its parents before any file is touched.
type nonVideoPlan struct {
	// ref is the MediaFile's spec.mediaRef: the album, book, audiobook or
	// issue the files back.
	ref commonv1.MediaRef

	namespace  string
	rootFolder *catalogv1alpha1.RootFolder
	profile    quality.Profile

	// folder is the item's absolute folder in the library.
	folder string

	// fileName maps a source file (its path relative to the content root)
	// onto its name under folder. Multi-file items keep the source's own
	// relative path, disc subfolders included: renaming a track or a part
	// needs its number and title, which only a probe of its tags could
	// supply.
	fileName func(srcRel string) string

	// singleTrack is the recording MBID of an album whose selected release
	// has exactly one track; empty otherwise. A lone audio file imported
	// to such an album is that track (commonv1.MediaRef.Track).
	singleTrack string
}

// resolveNonVideo reads the target item and its parents and works out where
// its files go. Errors wrapping errBlocked are terminal for this Download;
// anything else is worth a redelivery.
func (w *Worker) resolveNonVideo(ctx context.Context, ns string, target importtarget.ImportTarget, downloadProfileRef string) (nonVideoPlan, error) {
	ref := target.FileRef()
	plan := nonVideoPlan{ref: ref, namespace: ns}

	var (
		rootRef, profileRef, folder string
		fileName                    func(string) string
		err                         error
	)
	switch ref.Kind {
	case commonv1.MediaKindAlbum:
		rootRef, profileRef, folder, plan.singleTrack, err = w.resolveAlbum(ctx, ns, ref.Name)
		fileName = keepRelative
	case commonv1.MediaKindBook:
		rootRef, profileRef, folder, fileName, err = w.resolveBook(ctx, ns, ref.Name)
	case commonv1.MediaKindAudiobook:
		rootRef, profileRef, folder, err = w.resolveAudiobook(ctx, ns, ref.Name)
		fileName = keepRelative
	case commonv1.MediaKindIssue:
		rootRef, profileRef, folder, fileName, err = w.resolveIssue(ctx, ns, target)
	default:
		return nonVideoPlan{}, blocked("target kind %q is not a non-video file kind", ref.Kind)
	}
	if err != nil {
		return nonVideoPlan{}, err
	}

	if downloadProfileRef != "" {
		profileRef = downloadProfileRef
	}
	if rootRef == "" {
		return nonVideoPlan{}, blocked("%s %s names no root folder", ref.Kind, ref.Name)
	}
	if profileRef == "" {
		return nonVideoPlan{}, blocked("neither the download nor %s %s names a quality profile", ref.Kind, ref.Name)
	}

	var root catalogv1alpha1.RootFolder
	if err := w.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: rootRef}, &root); err != nil {
		if apierrors.IsNotFound(err) {
			return nonVideoPlan{}, blocked("root folder %q does not exist", rootRef)
		}
		return nonVideoPlan{}, fmt.Errorf("fileimport: get root folder %s/%s: %w", ns, rootRef, err)
	}
	if !importtarget.FileRefFitsRoot(ref, root.Spec.Kind) {
		return nonVideoPlan{}, blocked("root folder %q is a %s root; a %s file does not belong there",
			root.Name, root.Spec.Kind, ref.Kind)
	}
	plan.rootFolder = &root

	profile, err := w.resolveProfile(ctx, profileRef, "")
	if err != nil {
		return nonVideoPlan{}, err
	}
	plan.profile = profile

	if !filepath.IsAbs(folder) {
		folder = filepath.Join(root.Spec.Path, folder)
	}
	plan.folder = folder
	plan.fileName = fileName

	return plan, nil
}

// keepRelative is the file-name rule for a multi-file item.
func keepRelative(srcRel string) string { return srcRel }

// getRoot reads a root folder by name for folder rendering. A missing one is
// terminal.
func (w *Worker) getRoot(ctx context.Context, ns, name string) (*catalogv1alpha1.RootFolder, error) {
	var root catalogv1alpha1.RootFolder
	if err := w.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &root); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, blocked("root folder %q does not exist", name)
		}
		return nil, fmt.Errorf("fileimport: get root folder %s/%s: %w", ns, name, err)
	}
	return &root, nil
}

// getItem reads one catalog object, turning NotFound into a terminal error:
// a Download whose target is gone, or an import-target naming nothing, is
// not something a redelivery fixes.
func (w *Worker) getItem(ctx context.Context, ns, kind, name string, obj client.Object) error {
	if err := w.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, obj); err != nil {
		if apierrors.IsNotFound(err) {
			return blocked("%s %q does not exist", kind, name)
		}
		return fmt.Errorf("fileimport: get %s %s/%s: %w", kind, ns, name, err)
	}
	return nil
}

// resolveAlbum: the artist's folder, then the album's own folder segment
// from pkg/naming's album preset. Files keep their relative paths.
// singleTrack is the recording MBID when the album's selected release has
// exactly one track (status.tracks).
func (w *Worker) resolveAlbum(ctx context.Context, ns, name string) (rootRef, profileRef, folder, singleTrack string, err error) {
	var album catalogv1alpha1.Album
	if err := w.getItem(ctx, ns, "album", name, &album); err != nil {
		return "", "", "", "", err
	}
	if len(album.Status.Tracks) == 1 {
		singleTrack = album.Status.Tracks[0].RecordingID
	}
	var artist catalogv1alpha1.Artist
	if err := w.getItem(ctx, ns, "artist", album.Spec.ArtistRef, &artist); err != nil {
		return "", "", "", "", err
	}
	rootRef = artist.Spec.RootFolderRef
	profileRef = ptr.Deref(album.Spec.QualityProfileRef, artist.Spec.QualityProfileRef)
	if album.Status.Path != "" {
		return rootRef, profileRef, album.Status.Path, singleTrack, nil
	}
	if album.Status.Metadata == nil || album.Status.Metadata.Title == "" {
		return "", "", "", "", fmt.Errorf("fileimport: album %s/%s has no metadata yet", ns, name)
	}
	root, err := w.getRoot(ctx, ns, rootRef)
	if err != nil {
		return "", "", "", "", err
	}
	eng := catalogctx.EngineFor(root)
	artistFolder, err := w.artistFolder(root, &artist, eng)
	if err != nil {
		return "", "", "", "", err
	}
	nctx := naming.Context{
		Kind:       commonv1.MediaKindAlbum,
		ArtistName: artistName(&artist),
		AlbumTitle: album.Status.Metadata.Title,
	}
	nctx.Year = ReleaseYear(album.Status.Metadata.ReleaseDate)
	rendered, err := eng.BuildFolder(commonv1.MediaKindAlbum, nctx)
	if err != nil {
		return "", "", "", "", blocked("render album folder: %v", err)
	}
	return rootRef, profileRef, filepath.Join(artistFolder, path.Base(rendered)), singleTrack, nil
}

// artistFolder mirrors the Artist controller's path rule: status.path when
// it has resolved one, else spec.folder, else the artist preset.
func (w *Worker) artistFolder(root *catalogv1alpha1.RootFolder, artist *catalogv1alpha1.Artist, eng naming.Engine) (string, error) {
	if artist.Status.Path != "" {
		return artist.Status.Path, nil
	}
	if f := ptr.Deref(artist.Spec.Folder, ""); f != "" {
		return filepath.Join(root.Spec.Path, f), nil
	}
	name := artistName(artist)
	if name == "" {
		return "", fmt.Errorf("fileimport: artist %s/%s has no metadata yet", artist.Namespace, artist.Name)
	}
	f, err := eng.BuildFolder(commonv1.MediaKindArtist, naming.Context{Kind: commonv1.MediaKindArtist, ArtistName: name})
	if err != nil {
		return "", blocked("render artist folder: %v", err)
	}
	return filepath.Join(root.Spec.Path, f), nil
}

func artistName(a *catalogv1alpha1.Artist) string {
	if a.Status.Metadata == nil {
		return ""
	}
	return a.Status.Metadata.Name
}

// resolveBook: pkg/naming's book-file preset is "{Book Title}/{Author Name}"
// under the author's folder (Readarr's convention: a folder per book, the
// file named after the author). A standalone book has no author folder and
// no author name, so it gets "<root>/{Book Title}/{Book Title}.<ext>".
func (w *Worker) resolveBook(ctx context.Context, ns, name string) (
	rootRef, profileRef, folder string, fileName func(string) string, err error,
) {
	var book catalogv1alpha1.Book
	if err := w.getItem(ctx, ns, "book", name, &book); err != nil {
		return "", "", "", nil, err
	}
	var author *catalogv1alpha1.Author
	if ref := ptr.Deref(book.Spec.AuthorRef, ""); ref != "" {
		author = &catalogv1alpha1.Author{}
		if err := w.getItem(ctx, ns, "author", ref, author); err != nil {
			return "", "", "", nil, err
		}
	}
	rootRef = ptr.Deref(book.Spec.RootFolderRef, "")
	profileRef = ptr.Deref(book.Spec.QualityProfileRef, "")
	if author != nil {
		if rootRef == "" {
			rootRef = author.Spec.RootFolderRef
		}
		if profileRef == "" {
			profileRef = author.Spec.QualityProfileRef
		}
	}
	if rootRef == "" {
		return "", "", "", nil, blocked("book %s has no rootFolderRef and no author to inherit one from", name)
	}
	if book.Status.Metadata == nil || book.Status.Metadata.Title == "" {
		return "", "", "", nil, fmt.Errorf("fileimport: book %s/%s has no metadata yet", ns, name)
	}
	root, err := w.getRoot(ctx, ns, rootRef)
	if err != nil {
		return "", "", "", nil, err
	}
	eng := catalogctx.EngineFor(root)
	nctx := naming.Context{Kind: commonv1.MediaKindBook, BookTitle: book.Status.Metadata.Title}
	nctx.Year = ReleaseYear(book.Status.Metadata.ReleaseDate)

	if author == nil {
		title, rerr := eng.Render("{Book Title}", nctx)
		if rerr != nil {
			return "", "", "", nil, blocked("render book title: %v", rerr)
		}
		return rootRef, profileRef, filepath.Join(root.Spec.Path, title), withExt(title), nil
	}

	if author.Status.Metadata == nil || author.Status.Metadata.Name == "" {
		return "", "", "", nil, fmt.Errorf("fileimport: author %s/%s has no metadata yet", ns, author.Name)
	}
	nctx.AuthorName = author.Status.Metadata.Name
	authorFolder := author.Status.Path
	if authorFolder == "" {
		if f := ptr.Deref(author.Spec.Folder, ""); f != "" {
			authorFolder = filepath.Join(root.Spec.Path, f)
		} else {
			f, ferr := eng.BuildFolder(commonv1.MediaKindAuthor, nctx)
			if ferr != nil {
				return "", "", "", nil, blocked("render author folder: %v", ferr)
			}
			authorFolder = filepath.Join(root.Spec.Path, f)
		}
	}
	file, err := eng.BuildFile(commonv1.MediaKindBook, nctx)
	if err != nil {
		return "", "", "", nil, blocked("render book file: %v", err)
	}
	return rootRef, profileRef, filepath.Join(authorFolder, path.Dir(file)), withExt(path.Base(file)), nil
}

// withExt names a single-file item's file base plus the source's extension.
func withExt(base string) func(string) string {
	return func(srcRel string) string { return base + strings.ToLower(filepath.Ext(srcRel)) }
}

// resolveAudiobook mirrors the Audiobook controller's path rule
// (app/catalog/controller/audiobook.Path): status.path when resolved, else
// spec.folder, else the audiobook preset over the same naming context.
func (w *Worker) resolveAudiobook(ctx context.Context, ns, name string) (rootRef, profileRef, folder string, err error) {
	var ab catalogv1alpha1.Audiobook
	if err := w.getItem(ctx, ns, "audiobook", name, &ab); err != nil {
		return "", "", "", err
	}
	rootRef, profileRef = ab.Spec.RootFolderRef, ab.Spec.QualityProfileRef
	if ab.Status.Path != "" {
		return rootRef, profileRef, ab.Status.Path, nil
	}
	if f := ptr.Deref(ab.Spec.Folder, ""); f != "" {
		return rootRef, profileRef, f, nil
	}
	meta := ab.Status.Metadata
	if meta == nil || meta.Title == "" {
		return "", "", "", fmt.Errorf("fileimport: audiobook %s/%s has no metadata yet", ns, name)
	}
	root, err := w.getRoot(ctx, ns, rootRef)
	if err != nil {
		return "", "", "", err
	}
	names := make([]string, 0, len(meta.Authors))
	for _, a := range meta.Authors {
		names = append(names, a.Name)
	}
	nctx := naming.Context{
		Kind:       commonv1.MediaKindAudiobook,
		BookTitle:  meta.Title,
		AuthorName: strings.Join(names, ", "),
		Narrator:   strings.Join(meta.Narrators, ", "),
	}
	nctx.Year = ReleaseYear(meta.ReleaseDate)
	if meta.Series != nil {
		nctx.BookSeries, nctx.BookSeriesPosition = meta.Series.Series, meta.Series.Position
	}
	rendered, err := catalogctx.EngineFor(root).BuildFolder(commonv1.MediaKindAudiobook, nctx)
	if err != nil {
		return "", "", "", blocked("render audiobook folder: %v", err)
	}
	if !wellFormedFolder(rendered) {
		return "", "", "", blocked("the audiobook preset renders %q for audiobook %s -- its metadata lacks the series, "+
			"position or year the preset needs; set spec.folder", rendered, name)
	}
	return rootRef, profileRef, rendered, nil
}

// wellFormedFolder reports whether every segment of a rendered relative
// folder names something. pkg/naming's audiobook preset renders
// "Author// -  - Title" for a book with no series, position or year, and
// creating that on disk would be worse than refusing.
func wellFormedFolder(rel string) bool {
	for _, seg := range strings.Split(rel, "/") {
		if !strings.ContainsFunc(seg, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) {
			return false
		}
	}
	return rel != ""
}

// resolveIssue: the comic's folder (Comic controller's rule) and the
// issue-file preset's last segment, "{Comic Series Title} c{issue}".
// target is checked against the Issue's own comicRef when the annotation
// named the comic, so "comic/a/<an issue of b>" is refused, not followed.
func (w *Worker) resolveIssue(ctx context.Context, ns string, target importtarget.ImportTarget) (
	rootRef, profileRef, folder string, fileName func(string) string, err error,
) {
	ref := target.FileRef()
	var issue catalogv1alpha1.Issue
	if err := w.getItem(ctx, ns, "issue", ref.Name, &issue); err != nil {
		return "", "", "", nil, err
	}
	if target.Kind == commonv1.MediaKindComic && issue.Spec.ComicRef != target.Name {
		return "", "", "", nil, blocked("issue %s belongs to comic %q, not %q", issue.Name, issue.Spec.ComicRef, target.Name)
	}
	var comic catalogv1alpha1.Comic
	if err := w.getItem(ctx, ns, "comic", issue.Spec.ComicRef, &comic); err != nil {
		return "", "", "", nil, err
	}
	rootRef, profileRef = comic.Spec.RootFolderRef, comic.Spec.QualityProfileRef
	if comic.Status.Metadata == nil || comic.Status.Metadata.Title == "" {
		return "", "", "", nil, fmt.Errorf("fileimport: comic %s/%s has no metadata yet", ns, comic.Name)
	}
	root, err := w.getRoot(ctx, ns, rootRef)
	if err != nil {
		return "", "", "", nil, err
	}
	eng := catalogctx.EngineFor(root)
	nctx := naming.Context{
		Kind:             commonv1.MediaKindIssue,
		ComicSeriesTitle: comic.Status.Metadata.Title,
		IssueNumber:      issue.Spec.Number,
	}
	folder = comic.Status.Path
	if folder == "" {
		if f := ptr.Deref(comic.Spec.Folder, ""); f != "" {
			folder = filepath.Join(root.Spec.Path, f)
		} else {
			f, ferr := eng.BuildFolder(commonv1.MediaKindComic, nctx)
			if ferr != nil {
				return "", "", "", nil, blocked("render comic folder: %v", ferr)
			}
			folder = filepath.Join(root.Spec.Path, f)
		}
	}
	file, err := eng.BuildFile(commonv1.MediaKindIssue, nctx)
	if err != nil {
		return "", "", "", nil, blocked("render issue file: %v", err)
	}
	return rootRef, profileRef, folder, withExt(path.Base(file)), nil
}
