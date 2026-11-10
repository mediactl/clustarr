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
	"k8s.io/utils/ptr"

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
// is the container's resolved status.path when it has one, else its
// spec.folder under the RootFolder, else the kind's folder preset -- the
// rule the item controllers' Path functions and the import share.

// Album is the naming context of one track of album by artist: ok is false
// until the Album's title and the Artist's name have arrived, since the
// track preset names both. track is the Album's status.tracks entry the
// file's spec.mediaRef.track addresses.
func Album(artist *catalogv1alpha1.Artist, album *catalogv1alpha1.Album, track *catalogv1alpha1.Track) (naming.Context, bool) {
	if album.Status.Metadata == nil || album.Status.Metadata.Title == "" || track == nil ||
		artist.Status.Metadata == nil || artist.Status.Metadata.Name == "" {
		return naming.Context{}, false
	}
	return naming.Context{
		Kind:       commonv1.MediaKindAlbum,
		ArtistName: artist.Status.Metadata.Name,
		ArtistMbID: artist.Spec.MusicBrainzID,
		AlbumTitle: album.Status.Metadata.Title,
		AlbumMbID:  album.Spec.ReleaseGroupID,
		Year:       ReleaseYear(album.Status.Metadata.ReleaseDate),
		Track:      int(track.Number),
		Disc:       int(track.Medium),
		TrackTitle: track.Title,
	}, true
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

// TrackFilePath is the canonical path of the track c describes, under
// root: the album folder -- the Album's status.path, else the artist folder
// (the Artist's status.path, else its spec.folder, else the artist preset)
// joined with the album preset's own segment -- and the track preset's
// file segment plus ext. The import places an album's files under the same
// folder with their own names (the TrackFile preset is the rename's).
func TrackFilePath(root *catalogv1alpha1.RootFolder, artist *catalogv1alpha1.Artist, album *catalogv1alpha1.Album, c naming.Context, ext string) (string, error) {
	eng := EngineFor(root)
	file, err := eng.TrackFile(c)
	if err != nil {
		return "", fmt.Errorf("catalogctx: render track file: %w", err)
	}
	file = path.Base(file) + ext
	if album.Status.Path != "" {
		return joinUnder(root.Spec.Path, album.Status.Path, file)
	}
	albumFolder, err := eng.BuildFolder(commonv1.MediaKindAlbum, c)
	if err != nil {
		return "", fmt.Errorf("catalogctx: render album folder: %w", err)
	}
	base, rels, err := containerFolder(root, artist.Status.Path, artist.Spec.Folder, func() (string, error) {
		return eng.BuildFolder(commonv1.MediaKindArtist, c)
	})
	if err != nil {
		return "", fmt.Errorf("catalogctx: render artist folder: %w", err)
	}
	return joinUnder(root.Spec.Path, base, append(rels, path.Base(albumFolder), file)...)
}

// BookFilePath is the canonical path of the book c describes, under root:
// for one of an Author's, the author folder (the Author's status.path, else
// its spec.folder, else the author preset) and the book preset's two
// segments, "{Book Title}/{Author Name}" plus ext (Readarr's convention);
// for a standalone Book (author nil), "{Book Title}/{Book Title}" plus ext
// under the root itself, as the import places it.
func BookFilePath(root *catalogv1alpha1.RootFolder, author *catalogv1alpha1.Author, c naming.Context, ext string) (string, error) {
	eng := EngineFor(root)
	if author == nil {
		title, err := eng.Render("{Book Title}", c)
		if err != nil {
			return "", fmt.Errorf("catalogctx: render book title: %w", err)
		}
		return joinUnder(root.Spec.Path, root.Spec.Path, title, title+ext)
	}
	file, err := eng.BookFile(c)
	if err != nil {
		return "", fmt.Errorf("catalogctx: render book file: %w", err)
	}
	base, rels, err := containerFolder(root, author.Status.Path, author.Spec.Folder, func() (string, error) {
		return eng.BuildFolder(commonv1.MediaKindAuthor, c)
	})
	if err != nil {
		return "", fmt.Errorf("catalogctx: render author folder: %w", err)
	}
	return joinUnder(root.Spec.Path, base, append(rels, path.Dir(file), path.Base(file)+ext)...)
}

// AudiobookFilePath is the canonical path of the part c describes, under
// root: the audiobook folder (the Audiobook's status.path, else its
// spec.folder, else the audiobook preset) and the audiobook file preset's
// last segment -- the folder's name with the part -- plus ext.
func AudiobookFilePath(root *catalogv1alpha1.RootFolder, ab *catalogv1alpha1.Audiobook, c naming.Context, ext string) (string, error) {
	eng := EngineFor(root)
	file, err := eng.AudiobookFile(c)
	if err != nil {
		return "", fmt.Errorf("catalogctx: render audiobook file: %w", err)
	}
	base, rels, err := containerFolder(root, ab.Status.Path, ab.Spec.Folder, func() (string, error) {
		return eng.BuildFolder(commonv1.MediaKindAudiobook, c)
	})
	if err != nil {
		return "", fmt.Errorf("catalogctx: render audiobook folder: %w", err)
	}
	return joinUnder(root.Spec.Path, base, append(rels, path.Base(file)+ext)...)
}

// IssueFilePath is the canonical path of the issue c describes, under
// root: the comic folder (the Comic's status.path, else its spec.folder,
// else the comic preset) and the issue preset's last segment plus ext.
func IssueFilePath(root *catalogv1alpha1.RootFolder, comic *catalogv1alpha1.Comic, c naming.Context, ext string) (string, error) {
	eng := EngineFor(root)
	file, err := eng.IssueFile(c)
	if err != nil {
		return "", fmt.Errorf("catalogctx: render issue file: %w", err)
	}
	base, rels, err := containerFolder(root, comic.Status.Path, comic.Spec.Folder, func() (string, error) {
		return eng.BuildFolder(commonv1.MediaKindComic, c)
	})
	if err != nil {
		return "", fmt.Errorf("catalogctx: render comic folder: %w", err)
	}
	return joinUnder(root.Spec.Path, base, append(rels, path.Base(file)+ext)...)
}

// containerFolder is the folder rule every non-video container shares, as
// seriesFolder is the Series': its resolved status.path as the base and
// nothing relative; else the root as the base and its spec.folder; else the
// root and the kind's rendered preset.
func containerFolder(root *catalogv1alpha1.RootFolder, statusPath string, folder *string, preset func() (string, error)) (base string, rels []string, err error) {
	if statusPath != "" {
		return statusPath, nil, nil
	}
	if f := ptr.Deref(folder, ""); f != "" {
		return root.Spec.Path, []string{f}, nil
	}
	rendered, err := preset()
	if err != nil {
		return "", nil, err
	}
	return root.Spec.Path, []string{rendered}, nil
}
