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
	"slices"
	"strings"
	"unicode"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/import/importtarget"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/naming"
	"github.com/mediactl/clustarr/pkg/naming/catalogctx"
	"github.com/mediactl/clustarr/pkg/obs/logging"
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

	// folder is the item's absolute folder in the library: the container
	// folder rule the import and the loop share (status.path, else
	// spec.folder under the root, else the kind's folder preset). It is
	// the base of a file's fallback name (dest).
	folder string

	// fileName maps a source file (its path relative to the content root)
	// onto its fallback name under folder, the name the import gave every
	// file before the 2026-11-10 design: a multi-file item's file keeps
	// its own relative path, disc subfolders included, a single-file
	// item's takes the item's rendered base plus the source's extension.
	fileName func(srcRel string) string

	// tracks is an album's selected release's listing and media its disc
	// count (2026-11-10 design D6): every audio file is attributed to one
	// track (AttributeTrack), a lone file to a one-track album's one.
	tracks []catalogv1alpha1.Track
	media  int32

	// naming renders a file's canonical path, the one the remediation
	// loop's naming planner proposes (design D7, §5.5), so a fresh import
	// reads NamingCurrent True and the loop renames nothing.
	naming nonVideoNaming
}

// nonVideoNaming is the loop's own rendering of a non-video file's path
// (app/catalog/controller/mediafile.namingOwnerOf, through
// pkg/naming/catalogctx), held as two closures over the resolved objects:
// identity is the item's naming context for one file -- an Album's for the
// attributed track, an Audiobook's for the part's place among the import's
// parts, a Book's or an Issue's for the item alone -- false while the
// metadata the preset names has not arrived; render is the kind's *FilePath
// under root.
type nonVideoNaming struct {
	identity func(track *catalogv1alpha1.Track, part audiobookPart) (naming.Context, bool)
	render   func(root *catalogv1alpha1.RootFolder, c naming.Context, ext string) (string, error)
}

// audiobookPart is an audiobook file's place among the import's parts
// (catalogctx.PartNumbers over the files' frozen partIndex): its number in
// play order, 0 for a lone file, and their count -- what {PartNumber} and
// {PartCount} render. The zero value for every other kind.
type audiobookPart struct {
	number int32
	count  int
}

// resolved is what one kind's resolver read: the refs the import inherits,
// the container folder, the fallback name rule, an album's tracks and the
// loop's naming closures.
type resolved struct {
	rootRef, profileRef, folder string
	fileName                    func(string) string
	tracks                      []catalogv1alpha1.Track
	media                       int32
	naming                      nonVideoNaming
}

// resolveNonVideo reads the target item and its parents and works out where
// its files go. Errors wrapping errBlocked are terminal for this Download;
// anything else is worth a redelivery.
func (w *Worker) resolveNonVideo(ctx context.Context, ns string, target importtarget.ImportTarget, downloadProfileRef string) (nonVideoPlan, error) {
	ref := target.FileRef()
	plan := nonVideoPlan{ref: ref, namespace: ns}

	var (
		r   resolved
		err error
	)
	switch ref.Kind {
	case commonv1.MediaKindAlbum:
		r, err = w.resolveAlbum(ctx, ns, ref.Name)
	case commonv1.MediaKindBook:
		r, err = w.resolveBook(ctx, ns, ref.Name)
	case commonv1.MediaKindAudiobook:
		r, err = w.resolveAudiobook(ctx, ns, ref.Name)
	case commonv1.MediaKindIssue:
		r, err = w.resolveIssue(ctx, ns, target)
	default:
		return nonVideoPlan{}, blocked("target kind %q is not a non-video file kind", ref.Kind)
	}
	if err != nil {
		return nonVideoPlan{}, err
	}
	plan.tracks, plan.media, plan.naming = r.tracks, r.media, r.naming

	profileRef := r.profileRef
	if downloadProfileRef != "" {
		profileRef = downloadProfileRef
	}
	if r.rootRef == "" {
		return nonVideoPlan{}, blocked("%s %s names no root folder", ref.Kind, ref.Name)
	}
	if profileRef == "" {
		return nonVideoPlan{}, blocked("neither the download nor %s %s names a quality profile", ref.Kind, ref.Name)
	}

	var root catalogv1alpha1.RootFolder
	if err := w.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: r.rootRef}, &root); err != nil {
		if apierrors.IsNotFound(err) {
			return nonVideoPlan{}, blocked("root folder %q does not exist", r.rootRef)
		}
		return nonVideoPlan{}, fmt.Errorf("fileimport: get root folder %s/%s: %w", ns, r.rootRef, err)
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

	folder := r.folder
	if !filepath.IsAbs(folder) {
		folder = filepath.Join(root.Spec.Path, folder)
	}
	plan.folder = folder
	plan.fileName = r.fileName

	return plan, nil
}

// dest is where f, at rel under the content root, goes in the library: its
// canonical path, rendered as the remediation loop's naming planner renders
// it (the loop's own closures over the same objects, through catalogctx;
// 2026-11-10 design D7, §5.5), so a fresh import reads NamingCurrent True
// and the loop renames nothing. The loop merges the file's frozen release
// fields (catalogctx.File) and its profile's configured language before it
// renders, so the import does the same; no non-video preset names either,
// but a template override may.
//
// Where the import lacks an input the rename has -- an album file
// attributed to no track (its rejection names why), a track its Album's
// release does not list, metadata the preset needs that has not arrived --
// or the render fails, the file keeps the name the import gave it before
// the design: its own under the item's folder (fileName), which the loop
// reads as Stale and proposes the canonical path for on its first pass, as
// it did for every such file until now. The fallback's sanitising is the
// one the import always applied to that name.
//
// part is an audiobook file's place among the import's parts (placeParts),
// the zero value for every other kind.
func (p nonVideoPlan) dest(ctx context.Context, f *schema.InspectedFile, rel, releaseTitle string, part audiobookPart) string {
	fallback := naming.SanitizePath(filepath.Join(p.folder, p.fileName(rel)), naming.DefaultSanitizeOptions())
	if p.naming.identity == nil || p.naming.render == nil {
		return fallback
	}
	var track *catalogv1alpha1.Track
	if p.ref.Kind == commonv1.MediaKindAlbum {
		i := slices.IndexFunc(p.tracks, func(t catalogv1alpha1.Track) bool { return t.RecordingID == f.Frozen.Track })
		if f.Frozen.Track == "" || i < 0 {
			return fallback
		}
		track = &p.tracks[i]
	}
	c, ok := p.naming.identity(track, part)
	if !ok {
		return fallback
	}
	c.Language = catalogctx.ConfiguredLanguage(&p.profile)
	c = catalogctx.File(ctx, c, frozenSpec(f.Frozen, releaseTitle), nil)
	dest, err := p.naming.render(p.rootFolder, c, catalogctx.FileExt(p.ref.Kind, nil, rel))
	if err != nil {
		logging.FromContext(ctx).Warn("fileimport: the file's canonical path could not be rendered; it keeps its own name",
			"source", rel, "kind", p.ref.Kind, "item", p.ref.Name, "err", err)
		return fallback
	}
	return dest
}

// placeParts renders the destination of each of an audiobook import's
// parts once the walk has found them all (2026-11-10 design D7, D11; C5):
// {PartNumber} is a file's place among the import's parts in play order --
// catalogctx.PartNumbers over their frozen partIndex (the ordering key the
// execute carries onto spec.partIndex) and their source paths -- and
// {PartCount} their number, as the remediation loop's naming planner numbers
// the Audiobook's MediaFiles, so a two-disc set imports as parts 01..n, not
// as "(1003)", and a lone part carries no number. files is the inspect's own
// slice; a file the walk could not read, or a sample, is no part and keeps
// no destination.
func (p nonVideoPlan) placeParts(ctx context.Context, files []schema.InspectedFile, root, releaseTitle string) {
	var (
		parts []catalogctx.Part
		at    []int
	)
	for i := range files {
		if files[i].Proposed == nil || len(files[i].Rejections) > 0 {
			continue
		}
		parts = append(parts, catalogctx.Part{Index: files[i].Frozen.PartIndex, Path: files[i].Path})
		at = append(at, i)
	}
	numbers := catalogctx.PartNumbers(parts)
	for n, i := range at {
		f := &files[i]
		f.Dest = p.dest(ctx, f, relPath(root, f.Path), releaseTitle, audiobookPart{number: numbers[n], count: len(parts)})
	}
}

// keepOwnNamesOnCollision gives back their fallback names to the files of
// one inspect whose canonical paths collide: the track preset has no medium
// token (the 2026-11-10 plan's W1.4 leftover), so two discs' same-numbered,
// same-titled tracks render alike, and importplan refuses two files at one
// path, which would hold the whole album. Named their own way, both import
// as they did before the design; the loop proposes the canonical path for
// each and overwrites nothing. files is the inspect's own slice; fallback is
// dest's fallback for one of them.
func keepOwnNamesOnCollision(files []schema.InspectedFile, fallback func(f *schema.InspectedFile) string) {
	at := map[string]int{}
	for i := range files {
		if files[i].Dest != "" {
			at[files[i].Dest]++
		}
	}
	for i := range files {
		if files[i].Dest != "" && at[files[i].Dest] > 1 {
			files[i].Dest = fallback(&files[i])
		}
	}
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
// from pkg/naming's album preset. A file's canonical name is the track
// preset's for the track it is attributed to (catalogctx.TrackFilePath);
// its fallback keeps its relative path. tracks is the selected release's
// listing (status.tracks), media its disc count.
func (w *Worker) resolveAlbum(ctx context.Context, ns, name string) (resolved, error) {
	var album catalogv1alpha1.Album
	if err := w.getItem(ctx, ns, "album", name, &album); err != nil {
		return resolved{}, err
	}
	r := resolved{fileName: keepRelative, tracks: album.Status.Tracks}
	if m := album.Status.Metadata; m != nil {
		for _, rel := range m.Releases {
			if rel.ID == m.SelectedReleaseID {
				r.media = int32(len(rel.Media))
			}
		}
	}
	var artist catalogv1alpha1.Artist
	if err := w.getItem(ctx, ns, "artist", album.Spec.ArtistRef, &artist); err != nil {
		return resolved{}, err
	}
	r.rootRef = artist.Spec.RootFolderRef
	r.profileRef = ptr.Deref(album.Spec.QualityProfileRef, artist.Spec.QualityProfileRef)
	r.naming = nonVideoNaming{
		identity: func(track *catalogv1alpha1.Track, _ audiobookPart) (naming.Context, bool) {
			return catalogctx.Album(&artist, &album, track)
		},
		render: func(root *catalogv1alpha1.RootFolder, c naming.Context, ext string) (string, error) {
			return catalogctx.TrackFilePath(root, &artist, &album, c, ext)
		},
	}
	if album.Status.Path != "" {
		r.folder = album.Status.Path
		return r, nil
	}
	if album.Status.Metadata == nil || album.Status.Metadata.Title == "" {
		return resolved{}, fmt.Errorf("fileimport: album %s/%s has no metadata yet", ns, name)
	}
	root, err := w.getRoot(ctx, ns, r.rootRef)
	if err != nil {
		return resolved{}, err
	}
	eng := catalogctx.EngineFor(root)
	artistFolder, err := w.artistFolder(root, &artist, eng)
	if err != nil {
		return resolved{}, err
	}
	nctx := naming.Context{
		Kind:       commonv1.MediaKindAlbum,
		ArtistName: artistName(&artist),
		AlbumTitle: album.Status.Metadata.Title,
	}
	nctx.Year = ReleaseYear(album.Status.Metadata.ReleaseDate)
	rendered, err := eng.BuildFolder(commonv1.MediaKindAlbum, nctx)
	if err != nil {
		return resolved{}, blocked("render album folder: %v", err)
	}
	r.folder = filepath.Join(artistFolder, path.Base(rendered))
	return r, nil
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
// no author name, so it gets "<root>/{Book Title}/{Book Title}.<ext>". The
// canonical path is catalogctx.BookFilePath's, the same two rules.
func (w *Worker) resolveBook(ctx context.Context, ns, name string) (resolved, error) {
	var book catalogv1alpha1.Book
	if err := w.getItem(ctx, ns, "book", name, &book); err != nil {
		return resolved{}, err
	}
	var author *catalogv1alpha1.Author
	if ref := ptr.Deref(book.Spec.AuthorRef, ""); ref != "" {
		author = &catalogv1alpha1.Author{}
		if err := w.getItem(ctx, ns, "author", ref, author); err != nil {
			return resolved{}, err
		}
	}
	r := resolved{
		rootRef:    ptr.Deref(book.Spec.RootFolderRef, ""),
		profileRef: ptr.Deref(book.Spec.QualityProfileRef, ""),
		naming: nonVideoNaming{
			identity: func(*catalogv1alpha1.Track, audiobookPart) (naming.Context, bool) {
				return catalogctx.Book(author, &book)
			},
			render: func(root *catalogv1alpha1.RootFolder, c naming.Context, ext string) (string, error) {
				return catalogctx.BookFilePath(root, author, c, ext)
			},
		},
	}
	if author != nil {
		if r.rootRef == "" {
			r.rootRef = author.Spec.RootFolderRef
		}
		if r.profileRef == "" {
			r.profileRef = author.Spec.QualityProfileRef
		}
	}
	if r.rootRef == "" {
		return resolved{}, blocked("book %s has no rootFolderRef and no author to inherit one from", name)
	}
	if book.Status.Metadata == nil || book.Status.Metadata.Title == "" {
		return resolved{}, fmt.Errorf("fileimport: book %s/%s has no metadata yet", ns, name)
	}
	root, err := w.getRoot(ctx, ns, r.rootRef)
	if err != nil {
		return resolved{}, err
	}
	eng := catalogctx.EngineFor(root)
	nctx := naming.Context{Kind: commonv1.MediaKindBook, BookTitle: book.Status.Metadata.Title}
	nctx.Year = ReleaseYear(book.Status.Metadata.ReleaseDate)

	if author == nil {
		title, rerr := eng.Render("{Book Title}", nctx)
		if rerr != nil {
			return resolved{}, blocked("render book title: %v", rerr)
		}
		r.folder, r.fileName = filepath.Join(root.Spec.Path, title), withExt(title)
		return r, nil
	}

	if author.Status.Metadata == nil || author.Status.Metadata.Name == "" {
		return resolved{}, fmt.Errorf("fileimport: author %s/%s has no metadata yet", ns, author.Name)
	}
	nctx.AuthorName = author.Status.Metadata.Name
	authorFolder := author.Status.Path
	if authorFolder == "" {
		if f := ptr.Deref(author.Spec.Folder, ""); f != "" {
			authorFolder = filepath.Join(root.Spec.Path, f)
		} else {
			f, ferr := eng.BuildFolder(commonv1.MediaKindAuthor, nctx)
			if ferr != nil {
				return resolved{}, blocked("render author folder: %v", ferr)
			}
			authorFolder = filepath.Join(root.Spec.Path, f)
		}
	}
	file, err := eng.BuildFile(commonv1.MediaKindBook, nctx)
	if err != nil {
		return resolved{}, blocked("render book file: %v", err)
	}
	r.folder, r.fileName = filepath.Join(authorFolder, path.Dir(file)), withExt(path.Base(file))
	return r, nil
}

// withExt names a single-file item's file base plus the source's extension.
func withExt(base string) func(string) string {
	return func(srcRel string) string { return base + strings.ToLower(filepath.Ext(srcRel)) }
}

// resolveAudiobook mirrors the Audiobook controller's path rule
// (app/catalog/controller/audiobook.Path): status.path when resolved, else
// spec.folder, else the audiobook preset over the same naming context. A
// part's canonical name is the audiobook file preset's with the part's
// number among the import's parts (placeParts, catalogctx.AudiobookFilePath);
// its fallback keeps its relative path.
func (w *Worker) resolveAudiobook(ctx context.Context, ns, name string) (resolved, error) {
	var ab catalogv1alpha1.Audiobook
	if err := w.getItem(ctx, ns, "audiobook", name, &ab); err != nil {
		return resolved{}, err
	}
	r := resolved{
		rootRef: ab.Spec.RootFolderRef, profileRef: ab.Spec.QualityProfileRef, fileName: keepRelative,
		naming: nonVideoNaming{
			// The loop's context counts the Audiobook's status.partCount,
			// which a fresh import's files have yet to make; the import
			// counts its own parts (placeParts).
			identity: func(_ *catalogv1alpha1.Track, part audiobookPart) (naming.Context, bool) {
				c, ok := catalogctx.Audiobook(&ab, part.number)
				c.PartCount = part.count
				return c, ok
			},
			render: func(root *catalogv1alpha1.RootFolder, c naming.Context, ext string) (string, error) {
				return catalogctx.AudiobookFilePath(root, &ab, c, ext)
			},
		},
	}
	if ab.Status.Path != "" {
		r.folder = ab.Status.Path
		return r, nil
	}
	if f := ptr.Deref(ab.Spec.Folder, ""); f != "" {
		r.folder = f
		return r, nil
	}
	meta := ab.Status.Metadata
	if meta == nil || meta.Title == "" {
		return resolved{}, fmt.Errorf("fileimport: audiobook %s/%s has no metadata yet", ns, name)
	}
	root, err := w.getRoot(ctx, ns, r.rootRef)
	if err != nil {
		return resolved{}, err
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
		return resolved{}, blocked("render audiobook folder: %v", err)
	}
	if !wellFormedFolder(rendered) {
		return resolved{}, blocked("the audiobook preset renders %q for audiobook %s -- its metadata lacks the series, "+
			"position or year the preset needs; set spec.folder", rendered, name)
	}
	r.folder = rendered
	return r, nil
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
// issue-file preset's last segment, "{Comic Series Title} c{issue}". The
// canonical path is catalogctx.IssueFilePath's, which also names the
// Comic's specialVersion ("Saga c001 TPB"), as the loop does; the fallback
// is the preset's segment without it, the import's name before the design.
// target is checked against the Issue's own comicRef when the annotation
// named the comic, so "comic/a/<an issue of b>" is refused, not followed.
func (w *Worker) resolveIssue(ctx context.Context, ns string, target importtarget.ImportTarget) (resolved, error) {
	ref := target.FileRef()
	var issue catalogv1alpha1.Issue
	if err := w.getItem(ctx, ns, "issue", ref.Name, &issue); err != nil {
		return resolved{}, err
	}
	if target.Kind == commonv1.MediaKindComic && issue.Spec.ComicRef != target.Name {
		return resolved{}, blocked("issue %s belongs to comic %q, not %q", issue.Name, issue.Spec.ComicRef, target.Name)
	}
	var comic catalogv1alpha1.Comic
	if err := w.getItem(ctx, ns, "comic", issue.Spec.ComicRef, &comic); err != nil {
		return resolved{}, err
	}
	r := resolved{
		rootRef: comic.Spec.RootFolderRef, profileRef: comic.Spec.QualityProfileRef,
		naming: nonVideoNaming{
			identity: func(*catalogv1alpha1.Track, audiobookPart) (naming.Context, bool) {
				return catalogctx.Issue(&comic, &issue)
			},
			render: func(root *catalogv1alpha1.RootFolder, c naming.Context, ext string) (string, error) {
				return catalogctx.IssueFilePath(root, &comic, c, ext)
			},
		},
	}
	if comic.Status.Metadata == nil || comic.Status.Metadata.Title == "" {
		return resolved{}, fmt.Errorf("fileimport: comic %s/%s has no metadata yet", ns, comic.Name)
	}
	root, err := w.getRoot(ctx, ns, r.rootRef)
	if err != nil {
		return resolved{}, err
	}
	eng := catalogctx.EngineFor(root)
	nctx := naming.Context{
		Kind:             commonv1.MediaKindIssue,
		ComicSeriesTitle: comic.Status.Metadata.Title,
		IssueNumber:      issue.Spec.Number,
	}
	r.folder = comic.Status.Path
	if r.folder == "" {
		if f := ptr.Deref(comic.Spec.Folder, ""); f != "" {
			r.folder = filepath.Join(root.Spec.Path, f)
		} else {
			f, ferr := eng.BuildFolder(commonv1.MediaKindComic, nctx)
			if ferr != nil {
				return resolved{}, blocked("render comic folder: %v", ferr)
			}
			r.folder = filepath.Join(root.Spec.Path, f)
		}
	}
	file, err := eng.BuildFile(commonv1.MediaKindIssue, nctx)
	if err != nil {
		return resolved{}, blocked("render issue file: %v", err)
	}
	r.fileName = withExt(path.Base(file))
	return r, nil
}
