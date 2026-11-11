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

package catalogctx

import (
	"cmp"
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/naming"
)

// The non-video kinds' naming contexts and file paths (2026-11-10 design
// D7, §5.5): what the remediation loop's naming planner renders for a
// MediaFile whose owner is an Album track, a Book, an Audiobook part or an
// Issue, mirroring the folder rules the import agent places such a file by
// (app/import/worker/fileimport's resolveAlbum, resolveBook,
// resolveAudiobook and resolveIssue) so a file the import placed is
// Current, never renamed on its first pass. The folder part of each path
// is the container's resolved status.path when it has one, else the rule
// its controller writes status.path by -- ItemFolder (its spec.folder under
// the RootFolder, else the kind's folder preset), BookFolder or AlbumFolder
// -- so the import, the loop and the controllers share one rule.

// Album is the naming context of one track of album by artist: ok is false
// until the Album's title and the Artist's name have arrived, since the
// track preset names both. track is the Album's status.tracks entry the
// file's spec.mediaRef.track addresses; its medium and the medium's format
// in the album's selected release ({medium:00}, {Medium Format}) are the
// multi-disc preset's folder, which TrackFilePath renders when that
// release lists two or more media (design D19).
func Album(artist *catalogv1alpha1.Artist, album *catalogv1alpha1.Album, track *catalogv1alpha1.Track) (naming.Context, bool) {
	if album.Status.Metadata == nil || album.Status.Metadata.Title == "" || track == nil ||
		artist.Status.Metadata == nil || artist.Status.Metadata.Name == "" {
		return naming.Context{}, false
	}
	return naming.Context{
		Kind:         commonv1.MediaKindAlbum,
		ArtistName:   artist.Status.Metadata.Name,
		ArtistMbID:   artist.Spec.MusicBrainzID,
		AlbumTitle:   album.Status.Metadata.Title,
		AlbumMbID:    album.Spec.ReleaseGroupID,
		Year:         ReleaseYear(album.Status.Metadata.ReleaseDate),
		Track:        int(track.Number),
		Medium:       track.Medium,
		MediumFormat: mediumFormat(SelectedRelease(album), track.Medium),
		TrackTitle:   track.Title,
	}, true
}

// SelectedRelease is the release of album's release group its tracks were
// taken from (status.metadata.selectedReleaseID among
// status.metadata.releases), nil while none is selected or the album's
// metadata has not arrived.
func SelectedRelease(album *catalogv1alpha1.Album) *catalogv1alpha1.ReleaseSummary {
	m := album.Status.Metadata
	if m == nil || m.SelectedReleaseID == "" {
		return nil
	}
	for i := range m.Releases {
		if m.Releases[i].ID == m.SelectedReleaseID {
			return &m.Releases[i]
		}
	}
	return nil
}

// MultiDisc is whether album's selected release lists two or more media,
// which is when its tracks are named by the multi-disc preset, a folder
// per medium below the album folder (design D19: the one-disc preset
// renders every medium's track 1 as one name).
func MultiDisc(album *catalogv1alpha1.Album) bool {
	rel := SelectedRelease(album)
	return rel != nil && len(rel.Media) >= 2
}

// mediumFormat is the format of medium number n in rel ("CD", "Vinyl"),
// empty when rel is nil or lists no such medium or no format for it.
func mediumFormat(rel *catalogv1alpha1.ReleaseSummary, n int32) string {
	if rel == nil {
		return ""
	}
	for _, m := range rel.Media {
		if m.Number == n {
			return m.Format
		}
	}
	return ""
}

// Book is the naming context of book, by author when the Book is one of
// an Author's (nil for a standalone Book, which has no author folder and no
// author name): ok is false until the Book's title, and the Author's name
// when there is one, have arrived.
func Book(author *catalogv1alpha1.Author, book *catalogv1alpha1.Book) (naming.Context, bool) {
	if book.Status.Metadata == nil || book.Status.Metadata.Title == "" {
		return naming.Context{}, false
	}
	c := naming.Context{
		Kind:      commonv1.MediaKindBook,
		BookTitle: book.Status.Metadata.Title,
		Year:      ReleaseYear(book.Status.Metadata.ReleaseDate),
	}
	if author != nil {
		if author.Status.Metadata == nil || author.Status.Metadata.Name == "" {
			return naming.Context{}, false
		}
		c.AuthorName = author.Status.Metadata.Name
	}
	return c, true
}

// Part is one of an audiobook's files as PartNumbers orders them: Index is
// its spec.partIndex, the ordering key the import records -- the tags' disc
// and track as disc*1000+track, else the name's part number, else 0
// (app/import/worker/fileimport.PartIndexOf) -- and Path the tie-break.
type Part struct {
	Index int32
	Path  string
}

// PartNumbers numbers parts in play order -- the Audiobook controller's
// OrderParts rule: the numbered parts by Index, then the unnumbered, ties by
// Path -- and returns each part's number aligned with parts, 1..n: what
// {PartNumber} renders (2026-11-10 design D7, D11). A lone part is numbered
// 0, so the preset's optional token drops and a one-file audiobook carries
// no part. The import numbers its own files (fileimport.placeParts) and
// mediafile.LoadNaming (audiobookPartNumber) the Audiobook's MediaFiles,
// listed through the item index, so both render one path; a part's
// spec.partIndex itself is never rendered (disc 1 track 3 is the third
// part, not part 1003).
func PartNumbers(parts []Part) []int32 {
	numbers := make([]int32, len(parts))
	if len(parts) < 2 {
		return numbers
	}
	order := make([]int, len(parts))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int {
		pa, pb := parts[a], parts[b]
		switch {
		case pa.Index > 0 && pb.Index > 0 && pa.Index != pb.Index:
			return cmp.Compare(pa.Index, pb.Index)
		case (pa.Index > 0) != (pb.Index > 0):
			if pa.Index > 0 {
				return -1
			}
			return 1
		}
		return cmp.Compare(pa.Path, pb.Path)
	})
	for n, i := range order {
		numbers[i] = int32(n + 1) //nolint:gosec // bounded by an audiobook's files, far under int32
	}
	return numbers
}

// Audiobook is the naming context of one part of ab: number is the part's
// place among the Audiobook's parts in play order, 1..n, and 0 for a
// one-file audiobook (PartNumbers) -- what {PartNumber} renders, never the
// part's spec.partIndex, which is the ordering key. PartCount is seeded
// from the Audiobook's status.partCount and overridden by both callers
// with their own count -- the import's placed files, the MediaFiles
// mediafile.audiobookPartNumber listed -- so number and count come from
// one read, never from a count the Audiobook's next pass writes. ok is
// false until the title has arrived. Authors and narrators each collapse
// onto one token, comma-joined, as the Audiobook controller's own folder
// context does.
func Audiobook(ab *catalogv1alpha1.Audiobook, number int32) (naming.Context, bool) {
	meta := ab.Status.Metadata
	if meta == nil || meta.Title == "" {
		return naming.Context{}, false
	}
	names := make([]string, 0, len(meta.Authors))
	for _, a := range meta.Authors {
		names = append(names, a.Name)
	}
	c := naming.Context{
		Kind:       commonv1.MediaKindAudiobook,
		BookTitle:  meta.Title,
		AuthorName: strings.Join(names, ", "),
		Narrator:   strings.Join(meta.Narrators, ", "),
		Year:       ReleaseYear(meta.ReleaseDate),
		PartNumber: int(number),
		PartCount:  int(ab.Status.PartCount),
	}
	if meta.Series != nil {
		c.BookSeries, c.BookSeriesPosition = meta.Series.Series, meta.Series.Position
	}
	return c, true
}

// Issue is the naming context of issue of comic: ok is false until the
// Comic's title has arrived. The Comic's spec.specialVersion is the
// {Special Version} token (SpecialVersionLabel).
func Issue(comic *catalogv1alpha1.Comic, issue *catalogv1alpha1.Issue) (naming.Context, bool) {
	if comic.Status.Metadata == nil || comic.Status.Metadata.Title == "" {
		return naming.Context{}, false
	}
	return naming.Context{
		Kind:             commonv1.MediaKindIssue,
		ComicSeriesTitle: comic.Status.Metadata.Title,
		Year:             int(comic.Status.Metadata.Year),
		IssueNumber:      issue.Spec.Number,
		SpecialVersion:   SpecialVersionLabel(comic.Spec.SpecialVersion),
	}, true
}

// SpecialVersionLabel is what {Special Version} renders for a Comic's
// spec.specialVersion: Kapowarr's own labels for a collected edition, and
// nothing for a normal run or one whose issues are volumes (design D10:
// volumeAsIssue numbers the issues by volume and nothing more).
func SpecialVersionLabel(v catalogv1alpha1.ComicSpecialVersion) string {
	switch v {
	case catalogv1alpha1.ComicSpecialVersionTPB:
		return "TPB"
	case catalogv1alpha1.ComicSpecialVersionOneShot:
		return "One-Shot"
	case catalogv1alpha1.ComicSpecialVersionHardCover:
		return "Hard Cover"
	case catalogv1alpha1.ComicSpecialVersionOmnibus:
		return "Omnibus"
	}
	return ""
}

// ReleaseYear is the calendar year of a metadata release date, 0 for none.
// .UTC() before .Year(): metav1.Time comes back from the apiserver in the
// replica's local zone, so a midnight-UTC date west of UTC would read the
// previous day, and on 1 January the previous year (CLAUDE.md, "Gotchas").
func ReleaseYear(t *metav1.Time) int {
	if t == nil || t.IsZero() {
		return 0
	}
	return t.UTC().Year()
}

// FileExt is the extension a file of kind takes at its canonical path: a
// video file's from its probed container (ContainerExt, so a remux into
// another container is renamed with it), any other file's own, lowercased
// -- an .m4b audiobook or .m4a track probes as the MP4 container, and
// renaming it .mp4 would lose what the name says it is.
func FileExt(kind commonv1.MediaKind, mi *commonv1.MediaInfo, filePath string) string {
	switch kind {
	case commonv1.MediaKindMovie, commonv1.MediaKindEpisode:
		return ContainerExt(mi, filePath)
	}
	return strings.ToLower(filepath.Ext(filePath))
}

// AlbumFolder is the folder an Album's tracks are placed in under its
// Artist's folder (resolvedFolder for the Artist: its spec.folder as
// written when it pins one, else its status.path, else ItemFolder's answer
// for it), as the import places them and as the Album controller writes
// status.path (app/catalog/controller/album.Path): the album segment of
// the album preset -- BuildFolder renders "<artist>/<album>" and only its
// last segment is taken, so the Artist's spec.folder, which the preset
// cannot see, still decides the folder above it -- sanitized and held
// under root as ItemFolder's is. An album whose title has not arrived
// renders no segment of its own, which the preset joins away, so it is the
// artist's folder while the title is unknown, as ItemFolder degrades.
func AlbumFolder(root *catalogv1alpha1.RootFolder, artist *catalogv1alpha1.Artist, c naming.Context) (string, error) {
	artistFolder, err := resolvedFolder(root, artist.Status.Path, commonv1.MediaKindArtist, artist.Spec.Folder, c)
	if err != nil {
		return "", err
	}
	eng := EngineFor(root)
	rendered, err := eng.BuildFolder(commonv1.MediaKindAlbum, c)
	if err != nil {
		return "", fmt.Errorf("catalogctx: render album folder: %w", err)
	}
	artistOnly, err := eng.BuildFolder(commonv1.MediaKindArtist, c)
	if err != nil {
		return "", fmt.Errorf("catalogctx: render artist folder: %w", err)
	}
	if rendered == artistOnly {
		// The album segment rendered nothing: the preset's path.Join
		// dropped it, and what is left is the artist segment alone.
		return folderUnder(root.Spec.Path, artistFolder, "")
	}
	return folderUnder(root.Spec.Path, artistFolder, path.Base(rendered))
}

// standaloneBookFolderTemplate is the folder a standalone Book takes under
// its root: the title alone, the first segment of the two the import places
// its file at, "{Book Title}/{Book Title}.<ext>". It is no preset token of
// pkg/naming's, so a RootFolder's naming overrides cannot move it.
const standaloneBookFolderTemplate = "{Book Title}"

// BookFolder is the folder a Book's file is placed in, as the import places
// it and as the Book controller writes status.path
// (app/catalog/controller/book.Path; 2026-11-10 design §5.5, §9.3): one of
// an Author's (author non-nil) lives directly under its author's folder,
// ItemFolder's rule for the Author (its spec.folder, else "{Author Name}"
// from c.AuthorName -- Readarr's convention, no separate book folder), the
// root while the Author's name is not yet known; a standalone one (author
// nil, as the import's nil author) has a folder of its own under the root,
// "{Book Title}" rendered through the root's engine and sanitized as every
// component is, the root while the title is not yet known. The controller
// renders the Author's folder afresh rather than reading its status.path, as
// every controller does its own; the import (BookFilePath) takes the
// Author's status.path first, the same rule as of the Author's last pass.
func BookFolder(root *catalogv1alpha1.RootFolder, author *catalogv1alpha1.Author, c naming.Context) (string, error) {
	if author != nil {
		return ItemFolder(root, commonv1.MediaKindAuthor, author.Spec.Folder, c)
	}
	folder, _, err := standaloneBook(root, c)
	return folder, err
}

// standaloneBook renders a standalone Book's folder and its file's stem,
// both standaloneBookFolderTemplate.
func standaloneBook(root *catalogv1alpha1.RootFolder, c naming.Context) (folder, title string, err error) {
	title, err = EngineFor(root).Render(standaloneBookFolderTemplate, c)
	if err != nil {
		return "", "", fmt.Errorf("catalogctx: render book title: %w", err)
	}
	folder, err = folderUnder(root.Spec.Path, root.Spec.Path, title)
	return folder, title, err
}

// TrackFilePath is the canonical path of the track c describes, under
// root: the album folder -- the Album's status.path, sanitized below the
// root as a rendered folder is (renderedBase), unless the Artist pins its
// folder, when it is AlbumFolder's join of the album segment onto the pin
// as written (the Album's status.path is the same join, and a sanitize of
// it would rewrite the pin); else AlbumFolder under the artist folder --
// and the track preset's path below its own album segment plus ext: the
// file alone under the one-disc preset, the medium folder and the file
// ("CD 02/...") under the multi-disc one, which a release of two or more
// media renders by (MultiDisc, design D19; the album's status.path stays
// the album folder, and the rename pass moves the file into the medium
// folder only under the root's organizeFolders). The import places an
// album's files under the album folder with their own names (the TrackFile
// preset is the rename's).
func TrackFilePath(root *catalogv1alpha1.RootFolder, artist *catalogv1alpha1.Artist, album *catalogv1alpha1.Album, c naming.Context, ext string) (string, error) {
	eng := EngineFor(root)
	var file string
	var err error
	if MultiDisc(album) {
		file, err = eng.MultiDiscTrackFile(c)
	} else {
		file, err = eng.TrackFile(c)
	}
	if err != nil {
		return "", fmt.Errorf("catalogctx: render track file: %w", err)
	}
	file = belowAlbumFolder(file) + ext
	if _, pinned := pinnedFolder(artist.Spec.Folder); album.Status.Path != "" && !pinned {
		return joinUnder(root.Spec.Path, renderedBase(root.Spec.Path, album.Status.Path), file)
	}
	albumFolder, err := AlbumFolder(root, artist, c)
	if err != nil {
		return "", err
	}
	return joinUnder(root.Spec.Path, albumFolder, file)
}

// belowAlbumFolder is a rendered track path below the album segment the
// track presets carry first (Lidarr's convention: the track format is
// album-relative, so its first segment is the album folder, which
// TrackFilePath replaces by the resolved one): the file alone for the
// one-disc preset, "<medium>/<file>" for the multi-disc one, and the whole
// for an override of one segment.
func belowAlbumFolder(rendered string) string {
	if i := strings.Index(rendered, "/"); i >= 0 {
		return rendered[i+1:]
	}
	return rendered
}

// BookFilePath is the canonical path of the book c describes, under root:
// for one of an Author's, the author folder (the Author's status.path, else
// BookFolder's rule for it) and the book preset's two segments, "{Book
// Title}/{Author Name}" plus ext (Readarr's convention); for a standalone
// Book (author nil), "{Book Title}/{Book Title}" plus ext under the root
// itself (BookFolder), as the import places it.
func BookFilePath(root *catalogv1alpha1.RootFolder, author *catalogv1alpha1.Author, c naming.Context, ext string) (string, error) {
	if author == nil {
		folder, title, err := standaloneBook(root, c)
		if err != nil {
			return "", err
		}
		return joinUnder(root.Spec.Path, folder, title+ext)
	}
	file, err := EngineFor(root).BookFile(c)
	if err != nil {
		return "", fmt.Errorf("catalogctx: render book file: %w", err)
	}
	base, err := resolvedFolder(root, author.Status.Path, commonv1.MediaKindAuthor, author.Spec.Folder, c)
	if err != nil {
		return "", err
	}
	return joinUnder(root.Spec.Path, base, path.Dir(file), path.Base(file)+ext)
}

// AudiobookFilePath is the canonical path of the part c describes, under
// root: the audiobook folder (the Audiobook's status.path, else ItemFolder's
// rule for it -- its spec.folder, else the audiobook preset) and the
// audiobook file preset's last segment -- the folder's name with the part
// -- plus ext.
func AudiobookFilePath(root *catalogv1alpha1.RootFolder, ab *catalogv1alpha1.Audiobook, c naming.Context, ext string) (string, error) {
	file, err := EngineFor(root).AudiobookFile(c)
	if err != nil {
		return "", fmt.Errorf("catalogctx: render audiobook file: %w", err)
	}
	base, err := resolvedFolder(root, ab.Status.Path, commonv1.MediaKindAudiobook, ab.Spec.Folder, c)
	if err != nil {
		return "", err
	}
	return joinUnder(root.Spec.Path, base, path.Base(file)+ext)
}

// IssueFilePath is the canonical path of the issue c describes, under
// root: the comic folder (the Comic's status.path, else ItemFolder's rule
// for it -- its spec.folder, else the comic preset) and the issue preset's
// last segment plus ext.
func IssueFilePath(root *catalogv1alpha1.RootFolder, comic *catalogv1alpha1.Comic, c naming.Context, ext string) (string, error) {
	file, err := EngineFor(root).IssueFile(c)
	if err != nil {
		return "", fmt.Errorf("catalogctx: render issue file: %w", err)
	}
	base, err := resolvedFolder(root, comic.Status.Path, commonv1.MediaKindComic, comic.Spec.Folder, c)
	if err != nil {
		return "", err
	}
	return joinUnder(root.Spec.Path, base, path.Base(file)+ext)
}
