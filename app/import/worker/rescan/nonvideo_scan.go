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

package rescan

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/import/worker/fileimport"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// nonVideoIndex is one walk's snapshot of the items a non-video root folder
// may hold, reduced to candidates. Like loadMovies it is read once per walk.
type nonVideoIndex struct {
	albums     []AlbumCandidate
	books      []BookCandidate
	audiobooks []AudiobookCandidate
	comics     []ComicCandidate
	issues     []IssueCandidate
}

// fileKindForRoot is the kind of item a file under a root folder of kind k
// backs, or "" for a kind library rescan does not attribute.
func fileKindForRoot(k catalogv1alpha1.RootFolderKind) commonv1.MediaKind {
	switch k {
	case catalogv1alpha1.RootFolderKindMovie:
		return commonv1.MediaKindMovie
	case catalogv1alpha1.RootFolderKindSeries:
		return commonv1.MediaKindEpisode
	case catalogv1alpha1.RootFolderKindMusic:
		return commonv1.MediaKindAlbum
	case catalogv1alpha1.RootFolderKindBook:
		return commonv1.MediaKindBook
	case catalogv1alpha1.RootFolderKindAudiobook:
		return commonv1.MediaKindAudiobook
	case catalogv1alpha1.RootFolderKindComic:
		return commonv1.MediaKindIssue
	default:
		return ""
	}
}

// loadNonVideo lists the kinds a root folder of this kind holds, keeping
// only the items stored under root.
func (w *Worker) loadNonVideo(ctx context.Context, ns string, root *catalogv1alpha1.RootFolder) (*nonVideoIndex, error) {
	idx := &nonVideoIndex{}
	in := client.InNamespace(ns)
	switch root.Spec.Kind {
	case catalogv1alpha1.RootFolderKindMusic:
		var artists catalogv1alpha1.ArtistList
		if err := w.Client.List(ctx, &artists, in); err != nil {
			return nil, fmt.Errorf("rescan: list artists in %s: %w", ns, err)
		}
		byName := make(map[string]*catalogv1alpha1.Artist, len(artists.Items))
		for i := range artists.Items {
			byName[artists.Items[i].Name] = &artists.Items[i]
		}
		var albums catalogv1alpha1.AlbumList
		if err := w.Client.List(ctx, &albums, in); err != nil {
			return nil, fmt.Errorf("rescan: list albums in %s: %w", ns, err)
		}
		for i := range albums.Items {
			al := &albums.Items[i]
			ar := byName[al.Spec.ArtistRef]
			if ar == nil || ar.Spec.RootFolderRef != root.Name {
				continue
			}
			c := AlbumCandidate{Name: al.Name, Path: al.Status.Path}
			if m := al.Status.Metadata; m != nil {
				c.Title = m.Title
				c.Year = fileimport.ReleaseYear(m.ReleaseDate)
			}
			if ar.Status.Metadata != nil {
				c.ArtistNames = append(c.ArtistNames, ar.Status.Metadata.Name)
			}
			c.ArtistNames = append(c.ArtistNames, folderNames(ar.Spec.Folder, ar.Status.Path)...)
			idx.albums = append(idx.albums, c)
		}

	case catalogv1alpha1.RootFolderKindBook:
		var authors catalogv1alpha1.AuthorList
		if err := w.Client.List(ctx, &authors, in); err != nil {
			return nil, fmt.Errorf("rescan: list authors in %s: %w", ns, err)
		}
		byName := make(map[string]*catalogv1alpha1.Author, len(authors.Items))
		for i := range authors.Items {
			byName[authors.Items[i].Name] = &authors.Items[i]
		}
		var books catalogv1alpha1.BookList
		if err := w.Client.List(ctx, &books, in); err != nil {
			return nil, fmt.Errorf("rescan: list books in %s: %w", ns, err)
		}
		for i := range books.Items {
			b := &books.Items[i]
			rootRef := ptr.Deref(b.Spec.RootFolderRef, "")
			au := byName[ptr.Deref(b.Spec.AuthorRef, "")]
			if rootRef == "" && au != nil {
				rootRef = au.Spec.RootFolderRef
			}
			if rootRef != root.Name {
				continue
			}
			c := BookCandidate{Name: b.Name, Standalone: ptr.Deref(b.Spec.AuthorRef, "") == ""}
			if m := b.Status.Metadata; m != nil {
				c.Title = m.Title
				c.Year = fileimport.ReleaseYear(m.ReleaseDate)
			}
			if au != nil {
				if au.Status.Metadata != nil {
					c.AuthorNames = append(c.AuthorNames, au.Status.Metadata.Name)
				}
				c.AuthorNames = append(c.AuthorNames, folderNames(au.Spec.Folder, au.Status.Path)...)
			}
			idx.books = append(idx.books, c)
		}

	case catalogv1alpha1.RootFolderKindAudiobook:
		var abs catalogv1alpha1.AudiobookList
		if err := w.Client.List(ctx, &abs, in); err != nil {
			return nil, fmt.Errorf("rescan: list audiobooks in %s: %w", ns, err)
		}
		for i := range abs.Items {
			ab := &abs.Items[i]
			if ab.Spec.RootFolderRef != root.Name {
				continue
			}
			c := AudiobookCandidate{Name: ab.Name, ASIN: ab.Spec.ASIN, Path: ab.Status.Path}
			if f := ptr.Deref(ab.Spec.Folder, ""); f != "" && c.Path == "" {
				c.Path = filepath.Join(root.Spec.Path, f)
			}
			if m := ab.Status.Metadata; m != nil {
				c.Year = fileimport.ReleaseYear(m.ReleaseDate)
				narrators := strings.Join(m.Narrators, ", ")
				c.Titles = append(c.Titles, m.Title)
				if m.Subtitle != "" {
					c.Titles = append(c.Titles, m.Title+" "+m.Subtitle)
				}
				if narrators != "" {
					c.Titles = append(c.Titles, m.Title+" "+narrators)
				}
				names := make([]string, 0, len(m.Authors))
				for _, a := range m.Authors {
					names = append(names, a.Name)
				}
				c.Authors = append(c.Authors, names...)
				c.Authors = append(c.Authors, strings.Join(names, ", "))
			}
			idx.audiobooks = append(idx.audiobooks, c)
		}

	case catalogv1alpha1.RootFolderKindComic:
		var comics catalogv1alpha1.ComicList
		if err := w.Client.List(ctx, &comics, in); err != nil {
			return nil, fmt.Errorf("rescan: list comics in %s: %w", ns, err)
		}
		kept := map[string]bool{}
		for i := range comics.Items {
			co := &comics.Items[i]
			if co.Spec.RootFolderRef != root.Name {
				continue
			}
			kept[co.Name] = true
			c := ComicCandidate{Name: co.Name, Path: co.Status.Path}
			if f := ptr.Deref(co.Spec.Folder, ""); f != "" && c.Path == "" {
				c.Path = filepath.Join(root.Spec.Path, f)
			}
			if m := co.Status.Metadata; m != nil {
				c.Titles = append(c.Titles, m.Title)
				c.Year = int(m.Year)
			}
			c.Titles = append(c.Titles, folderNames(co.Spec.Folder, co.Status.Path)...)
			idx.comics = append(idx.comics, c)
		}
		var issues catalogv1alpha1.IssueList
		if err := w.Client.List(ctx, &issues, in); err != nil {
			return nil, fmt.Errorf("rescan: list issues in %s: %w", ns, err)
		}
		for i := range issues.Items {
			is := &issues.Items[i]
			if !kept[is.Spec.ComicRef] {
				continue
			}
			idx.issues = append(idx.issues, IssueCandidate{
				Name: is.Name, ComicRef: is.Spec.ComicRef, Number: is.Spec.Number, Centis: is.Spec.CalculatedNumberCentis,
			})
		}
	}
	return idx, nil
}

// folderNames is the folder names an item's own folder may carry:
// spec.folder and status.path's last segment.
func folderNames(specFolder *string, statusPath string) []string {
	var out []string
	if f := ptr.Deref(specFolder, ""); f != "" {
		out = append(out, filepath.Base(f))
	}
	if statusPath != "" {
		out = append(out, filepath.Base(statusPath))
	}
	return out
}

// matchNonVideo runs the matcher for the walk's root kind.
func (st *scanState) matchNonVideo(path, rel string) ItemMatch {
	switch st.root.Spec.Kind {
	case catalogv1alpha1.RootFolderKindMusic:
		return MatchAlbum(path, rel, st.nonVideo.albums)
	case catalogv1alpha1.RootFolderKindBook:
		return MatchBook(rel, st.nonVideo.books)
	case catalogv1alpha1.RootFolderKindAudiobook:
		return MatchAudiobook(path, rel, st.nonVideo.audiobooks)
	case catalogv1alpha1.RootFolderKindComic:
		return MatchIssue(path, rel, st.nonVideo.comics, st.nonVideo.issues)
	}
	return ItemMatch{
		Unmatched: true, Code: CodeUnsupportedKind,
		Reason: fmt.Sprintf("root folder kind %q is not supported by library rescan yet", st.root.Spec.Kind),
	}
}

// handleNonVideoFile attributes one file under a non-video root folder to an
// existing item and records it. It never creates a catalog item.
func (w *Worker) handleNonVideoFile(ctx context.Context, st *scanState, path, rel string, info os.FileInfo) error {
	now := w.now()
	existing, skip, err := w.existingForRescan(ctx, st, path, info)
	if err != nil || skip {
		return err
	}

	m := st.matchNonVideo(path, rel)
	if m.Unmatched {
		st.unmatched(rel, m.Code, m.Reason, m.Candidates, now)
		return nil
	}
	return w.recordAttribution(ctx, st, path, rel, info, existing, m.Ref, w.freshNonVideoSpec(ctx, m.Ref.Kind, path, rel))
}

// freshNonVideoSpec is what a first sighting of a non-video file freezes:
// the quality the file settles (fileimport.FrozenFileQuality: a music file
// is probed for its codec and bitrate, anything else goes by its extension,
// and a 24-bit marker in rel counts where the probe cannot say) and the
// kind's release type. There is no revision, group, edition or language to
// parse from a track, part, ebook or issue file name, and no custom-format
// score, which is TRaSH video data.
func (w *Worker) freshNonVideoSpec(ctx context.Context, kind commonv1.MediaKind, path, rel string) frozenFields {
	f := frozenFields{releaseType: fileimport.ReleaseTypeFor(kind)}
	if q, ok := fileimport.FrozenFileQuality(ctx, w.ProbeAudio, kind, path, rel); ok {
		f.quality = &q
	}
	return f
}

// recordAttribution applies the MediaFile for an attributed file, unless a
// MediaFile for this path already points elsewhere, and updates the tally.
// fresh is only used when there is no existing MediaFile: an existing one's
// frozen fields are re-asserted verbatim (observedSpec).
func (w *Worker) recordAttribution(
	ctx context.Context, st *scanState, path, rel string, info os.FileInfo,
	existing *catalogv1alpha1.MediaFile, ref commonv1.MediaRef, fresh frozenFields,
) error {
	if existing != nil && (existing.Spec.MediaRef.Kind != ref.Kind || existing.Spec.MediaRef.Name != ref.Name) {
		st.unmatched(rel, CodeRecordedElsewhere, fmt.Sprintf(
			"attributed to %s/%s, but MediaFile %s already records this path against %s/%s; spec.mediaRef is "+
				"immutable, so delete that MediaFile to re-attribute the file",
			ref.Kind, ref.Name, existing.Name, existing.Spec.MediaRef.Kind, existing.Spec.MediaRef.Name),
			[]string{existing.Spec.MediaRef.Name}, w.now())
		return nil
	}
	if !st.task.DryRun {
		if err := w.applyObserved(ctx, st.scan.Namespace, existing, ref, path, info, fresh); err != nil {
			return err
		}
	}
	st.progress.ItemsUpdated++
	st.progress.FilesMatched++
	logging.FromContext(ctx).Debug("attributed a scanned file", "kind", ref.Kind, "item", ref.Name)
	return nil
}
