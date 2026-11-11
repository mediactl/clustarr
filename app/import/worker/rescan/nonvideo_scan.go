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
	"slices"
	"strings"
	"time"

	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/import/mediafilespec"
	"github.com/mediactl/clustarr/app/import/scanprogress"
	"github.com/mediactl/clustarr/app/import/worker/fileimport"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/fsops"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/names"
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

	// artists and authors are every Artist and Author of the namespace,
	// another root's marked (OtherRootFolder): the containers a music or
	// book root's first segments name (MatchArtist, MatchAuthor).
	artists []ArtistCandidate
	authors []AuthorCandidate

	// known is every artist's, author's and audiobook's provider id in the
	// namespace, by kind: an identity identify.go resolves that an item
	// already has is that item's, never proposed again.
	known map[commonv1.MediaKind][]knownItem

	// folders memoises each container folder's verdict for the walk, by
	// kind and folder (relative to the root), and proposed each identity
	// proposed for creation, by kind and id: every file of a folder shares
	// its verdict, and no identity is proposed twice.
	folders  map[string]*folderVerdict
	proposed map[string]*folderVerdict
}

// knownItem is an existing item's provider id, and the root folder it is
// stored under when that is not the one being scanned.
type knownItem struct {
	name, id, otherRoot string
}

// folderVerdict is the walk's verdict on one container folder: the item of
// this root it is (existing), the item proposed from it (create), or
// neither (unmatched, listed once). Every file of the folder shares it.
type folderVerdict struct {
	existing   string
	create     *schema.ItemCreate
	providerID string

	// proposed is set once the create has been reported (an artist's or
	// author's one observation) or counted in a dry run.
	proposed bool
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
// only the items stored under root -- but every Artist, Author and
// Audiobook of the namespace for their ids (known), so a folder is never
// proposed as an item another root already has.
func (w *Worker) loadNonVideo(ctx context.Context, ns string, root *catalogv1alpha1.RootFolder) (*nonVideoIndex, error) {
	idx := &nonVideoIndex{
		known:    map[commonv1.MediaKind][]knownItem{},
		folders:  map[string]*folderVerdict{},
		proposed: map[string]*folderVerdict{},
	}
	in := client.InNamespace(ns)
	other := func(ref string) string {
		if ref == root.Name {
			return ""
		}
		return ref
	}
	switch root.Spec.Kind {
	case catalogv1alpha1.RootFolderKindMusic:
		var artists catalogv1alpha1.ArtistList
		if err := w.Client.List(ctx, &artists, in); err != nil {
			return nil, fmt.Errorf("rescan: list artists in %s: %w", ns, err)
		}
		byName := make(map[string]*catalogv1alpha1.Artist, len(artists.Items))
		for i := range artists.Items {
			ar := &artists.Items[i]
			byName[ar.Name] = ar
			c := ArtistCandidate{
				Name: ar.Name, MusicBrainzID: ar.Spec.MusicBrainzID, Path: underRoot(root.Spec.Path, ar.Status.Path),
				Folder: ptr.Deref(ar.Spec.Folder, ""), OtherRootFolder: other(ar.Spec.RootFolderRef),
			}
			if m := ar.Status.Metadata; m != nil {
				c.Names = append(append(c.Names, m.Name), m.Aliases...)
			}
			idx.artists = append(idx.artists, c)
			idx.known[commonv1.MediaKindArtist] = append(idx.known[commonv1.MediaKindArtist],
				knownItem{name: ar.Name, id: ar.Spec.MusicBrainzID, otherRoot: c.OtherRootFolder})
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
			c := AlbumCandidate{
				Name: al.Name, Path: al.Status.Path, Tracks: al.Status.Tracks,
				Artist: al.Spec.ArtistRef, ReleaseGroupID: al.Spec.ReleaseGroupID,
			}
			if m := al.Status.Metadata; m != nil {
				c.Title = m.Title
				c.Year = fileimport.ReleaseYear(m.ReleaseDate)
				for _, rel := range m.Releases {
					c.ReleaseIDs = append(c.ReleaseIDs, rel.ID)
					if rel.ID == m.SelectedReleaseID {
						c.Media = int32(len(rel.Media)) //nolint:gosec // a release's discs, far under int32
					}
				}
				if m.SelectedReleaseID != "" && !slices.Contains(c.ReleaseIDs, m.SelectedReleaseID) {
					// The gateway keeps a group's first 50 releases; the
					// selection may lie past them.
					c.ReleaseIDs = append(c.ReleaseIDs, m.SelectedReleaseID)
				}
			}
			if m := ar.Status.Metadata; m != nil {
				c.ArtistNames = append(append(c.ArtistNames, m.Name), m.Aliases...)
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
			au := &authors.Items[i]
			byName[au.Name] = au
			c := AuthorCandidate{
				Name: au.Name, OpenLibraryID: au.Spec.OpenLibraryID, Path: underRoot(root.Spec.Path, au.Status.Path),
				Folder: ptr.Deref(au.Spec.Folder, ""), OtherRootFolder: other(au.Spec.RootFolderRef),
			}
			if m := au.Status.Metadata; m != nil {
				c.Names = append(c.Names, m.Name)
			}
			idx.authors = append(idx.authors, c)
			idx.known[commonv1.MediaKindAuthor] = append(idx.known[commonv1.MediaKindAuthor],
				knownItem{name: au.Name, id: au.Spec.OpenLibraryID, otherRoot: c.OtherRootFolder})
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
			c := BookCandidate{
				Name: b.Name, Author: ptr.Deref(b.Spec.AuthorRef, ""), Standalone: ptr.Deref(b.Spec.AuthorRef, "") == "",
				Path: underRoot(root.Spec.Path, b.Status.Path),
			}
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
			idx.known[commonv1.MediaKindAudiobook] = append(idx.known[commonv1.MediaKindAudiobook],
				knownItem{name: ab.Name, id: ab.Spec.ASIN, otherRoot: other(ab.Spec.RootFolderRef)})
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

// underRoot is path relative to root, with forward slashes, or "" when path
// is empty or does not lie strictly beneath root.
func underRoot(root, path string) string {
	if path == "" || !underFolder(path, root) {
		return ""
	}
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil {
		return ""
	}
	return filepath.ToSlash(rel)
}

// handleNonVideoFile attributes one file under a non-video root folder and
// records it: to an existing item, or -- a music, book or audiobook root's
// container folder no item has -- to the Artist, Author or Audiobook the
// walk identifies from its folder (identify.go) and proposes.
func (w *Worker) handleNonVideoFile(ctx context.Context, st *scanState, path, rel string, info os.FileInfo) error {
	existing, skip, err := w.existingForRescan(ctx, st, path, info)
	if err != nil || skip {
		return err
	}
	switch st.root.Spec.Kind {
	case catalogv1alpha1.RootFolderKindMusic:
		return w.handleMusicFile(ctx, st, path, rel, info, existing)
	case catalogv1alpha1.RootFolderKindBook:
		return w.handleBookFile(ctx, st, path, rel, info, existing)
	case catalogv1alpha1.RootFolderKindAudiobook:
		return w.handleAudiobookFile(ctx, st, path, rel, info, existing)
	case catalogv1alpha1.RootFolderKindComic:
		m := MatchIssue(path, rel, st.nonVideo.comics, st.nonVideo.issues)
		if m.Unmatched {
			st.unmatched(rel, m.Code, m.Reason, m.Candidates, w.now())
			return nil
		}
		fresh, _ := w.freshNonVideoSpec(ctx, m.Ref.Kind, path, rel)
		return w.recordAttribution(ctx, st, path, rel, info, existing, m.Ref, fresh)
	}
	st.unmatched(rel, CodeUnsupportedKind, fmt.Sprintf(
		"root folder kind %q is not supported by library rescan yet", st.root.Spec.Kind), nil, w.now())
	return nil
}

// handleMusicFile attributes one file under a music root (2026-11-10 design
// D16, D17). The first segment is the artist's folder: an existing Artist of
// the root (MatchArtist), else the artist identify.go resolves, which is
// proposed once and its files counted as awaiting its albums, else an
// unmatched folder whose files are counted too. A file of an existing
// artist is probed before it is matched -- its tags name its album
// (MatchAlbum) and its track (fileimport.AttributeTrack) -- and a file
// whose artist's albums, or whose album's tracks, have not arrived awaits
// them rather than reading as unmatched; the folder scan the Artist owes
// once they are settled attributes it (D18).
func (w *Worker) handleMusicFile(
	ctx context.Context, st *scanState, path, rel string, info os.FileInfo, existing *catalogv1alpha1.MediaFile,
) error {
	const awaiting = string(commonv1.MediaKindArtist)
	segs := strings.Split(filepath.ToSlash(rel), "/")
	if len(segs) < 2 {
		st.unmatched(rel, CodeUnrecognisedLayout, "a music file must sit in its artist's folder under the root folder "+
			"(<artist>/[<album>/]<track>)", nil, w.now())
		return nil
	}
	v, err := w.containerFolder(ctx, st, commonv1.MediaKindArtist, segs[0])
	if err != nil {
		return err
	}
	if v.existing == "" {
		if err := w.proposeContainer(ctx, st, path, info, v); err != nil {
			return err
		}
		st.progress.Await(awaiting)
		return nil
	}

	albums := st.nonVideo.albumsOf(v.existing, segs[0])
	if len(albums) == 0 {
		// The Artist's fan-out has listed none of its albums yet.
		st.progress.Await(awaiting)
		return nil
	}
	// The probe runs before the match: a track's tags name its album
	// whatever the folder depth (D17). The file is probed once.
	if err := st.beatNow(ctx, false); err != nil {
		return err
	}
	fresh, ap := w.freshNonVideoSpec(ctx, commonv1.MediaKindAlbum, path, rel)
	var tags *commonv1.MediaTags
	if ap != nil {
		tags = ap.Tags
	}
	m := MatchAlbum(path, rel, tags, albums)
	if m.Unmatched {
		if (m.Code == CodeNoMatch || m.Code == CodeNoChild) && slices.ContainsFunc(albums, func(c AlbumCandidate) bool {
			return c.Title == ""
		}) {
			// An album whose metadata has not arrived has no title to
			// match yet: the file may be its.
			st.progress.Await(awaiting)
			return nil
		}
		st.unmatched(rel, m.Code, m.Reason, m.Candidates, w.now())
		return nil
	}
	// The track the file is (2026-11-10 design D6): by its tags, its
	// name, its recording id; a file naming none stays unmatched, with
	// the reason, rather than backing the album as no track.
	cand := &albums[slices.IndexFunc(albums, func(c AlbumCandidate) bool { return c.Name == m.Ref.Name })]
	id, reason := fileimport.AttributeTrack(cand.Tracks, cand.Media, ap, rel, len(cand.Tracks) == 1)
	switch {
	case reason == fileimport.ErrTracksPending:
		st.progress.Await(awaiting)
		return nil
	case id == "":
		st.unmatched(rel, CodeNoMatch, reason, []string{m.Ref.Name}, w.now())
		return nil
	}
	m.Ref.Track = id
	return w.recordAttribution(ctx, st, path, rel, info, existing, m.Ref, fresh)
}

// albumsOf is the root's albums of the Artist named artist, each copy
// carrying folder (the artist folder the walk resolved to that Artist) among
// its ArtistNames, so MatchAlbum's artist rule agrees with the walk's
// whatever layer resolved the folder (a token, an alias, the gateway).
func (idx *nonVideoIndex) albumsOf(artist, folder string) []AlbumCandidate {
	var out []AlbumCandidate
	for _, c := range idx.albums {
		if c.Artist != artist {
			continue
		}
		c.ArtistNames = append(slices.Clip(c.ArtistNames), folder)
		out = append(out, c)
	}
	return out
}

// handleBookFile attributes one file under a book root: to the existing
// Book MatchBook names; else, in an author folder no Author of the root
// names, to the Author identify.go resolves and proposes (its files await
// its books), or to an unmatched folder; a file of an existing Author whose
// books have not arrived awaits them. A standalone book's own folder is
// never taken for an author's.
func (w *Worker) handleBookFile(
	ctx context.Context, st *scanState, path, rel string, info os.FileInfo, existing *catalogv1alpha1.MediaFile,
) error {
	const awaiting = string(commonv1.MediaKindAuthor)
	m := MatchBook(rel, st.nonVideo.books)
	if !m.Unmatched {
		fresh, _ := w.freshNonVideoSpec(ctx, m.Ref.Kind, path, rel)
		return w.recordAttribution(ctx, st, path, rel, info, existing, m.Ref, fresh)
	}
	segs := strings.Split(filepath.ToSlash(rel), "/")
	if len(segs) < 2 || slices.ContainsFunc(st.nonVideo.books, func(c BookCandidate) bool {
		return c.Standalone && c.Path == segs[0]
	}) {
		st.unmatched(rel, m.Code, m.Reason, m.Candidates, w.now())
		return nil
	}
	v, err := w.containerFolder(ctx, st, commonv1.MediaKindAuthor, segs[0])
	if err != nil {
		return err
	}
	if v.existing == "" {
		if err := w.proposeContainer(ctx, st, path, info, v); err != nil {
			return err
		}
		st.progress.Await(awaiting)
		return nil
	}
	if !slices.ContainsFunc(st.nonVideo.books, func(c BookCandidate) bool { return c.Author == v.existing && c.Title != "" }) {
		// The Author's fan-out has listed no book with its metadata yet.
		st.progress.Await(awaiting)
		return nil
	}
	st.unmatched(rel, m.Code, m.Reason, m.Candidates, w.now())
	return nil
}

// handleAudiobookFile attributes one file under an audiobook root: to the
// existing Audiobook MatchAudiobook names; else, when no audiobook carries
// the path's ASIN or matches its folder, to the Audiobook identify.go
// resolves from its "<author>/<title>" folder, proposed with every one of
// its files, each frozen with its part's order; else to an unmatched folder.
func (w *Worker) handleAudiobookFile(
	ctx context.Context, st *scanState, path, rel string, info os.FileInfo, existing *catalogv1alpha1.MediaFile,
) error {
	m := MatchAudiobook(path, rel, st.nonVideo.audiobooks)
	ref := m.Ref
	var v *folderVerdict
	if m.Unmatched {
		dirs := bookDirs(rel)
		folder := strings.Join(dirs, "/")
		if (m.Code != CodeUnknownID && m.Code != CodeNoMatch) || len(dirs) == 0 || (len(dirs) < 2 && embeddedASIN(folder) == "") {
			st.unmatched(rel, m.Code, m.Reason, m.Candidates, w.now())
			return nil
		}
		var err error
		if v, err = w.containerFolder(ctx, st, commonv1.MediaKindAudiobook, folder); err != nil {
			return err
		}
		switch {
		case v.existing != "":
			ref = commonv1.MediaRef{Kind: commonv1.MediaKindAudiobook, Name: v.existing}
			v = nil
		case v.create == nil:
			st.progress.Await(string(commonv1.MediaKindAudiobook))
			return nil
		default:
			ref = commonv1.MediaRef{Kind: commonv1.MediaKindAudiobook, Name: v.create.Name}
		}
	}
	if err := st.beatNow(ctx, false); err != nil {
		return err
	}
	fresh, ap := w.freshNonVideoSpec(ctx, commonv1.MediaKindAudiobook, path, rel)
	fresh.PartIndex = fileimport.PartIndexOf(ap, rel)
	if v == nil {
		return w.recordAttribution(ctx, st, path, rel, info, existing, ref, fresh)
	}
	// Every file of a proposed audiobook carries the proposal, so the
	// applier creates the item with the first and records each part.
	if st.task.DryRun {
		st.predict(!v.proposed)
		v.proposed = true
		return nil
	}
	v.proposed = true
	return w.proposeFile(ctx, st, path, info, existing, ref, &fresh, v.create, "folder")
}

// containerFolder is the walk's verdict on the container folder of kind
// (an artist's or author's first segment, an audiobook's "<author>/<title>"),
// resolved at its first file and memoised for the rest: an existing item of
// the root by MatchArtist or MatchAuthor (MatchAudiobook already ran for an
// audiobook), else identify.go's identity -- an item the namespace already
// has, or one to propose -- else an unmatched folder, listed once.
func (w *Worker) containerFolder(ctx context.Context, st *scanState, kind commonv1.MediaKind, folder string) (*folderVerdict, error) {
	idx := st.nonVideo
	key := string(kind) + "/" + folder
	if v, ok := idx.folders[key]; ok {
		return v, nil
	}
	var m ItemMatch
	switch kind {
	case commonv1.MediaKindArtist:
		m = MatchArtist(folder, idx.artists)
	case commonv1.MediaKindAuthor:
		m = MatchAuthor(folder, idx.authors)
	default:
		m = ItemMatch{Unmatched: true, Code: CodeNoMatch}
	}
	v := &folderVerdict{}
	switch {
	case !m.Unmatched:
		v.existing = m.Ref.Name
	case m.Code == CodeAmbiguousTitle:
		st.unmatchedFolder(folder, m.Code, m.Reason, m.Candidates, w.now())
	default:
		id, err := w.identify(ctx, st, kind, folder)
		if err != nil {
			return nil, err
		}
		v = w.settle(st, kind, folder, id)
	}
	idx.folders[key] = v
	return v, nil
}

// identify asks identify.go what the folder of kind is, through the
// gateway's calls; for an artist, after probing up to maxTaggedFiles of the
// folder's audio files for their tags. The error is a failed heartbeat's.
func (w *Worker) identify(ctx context.Context, st *scanState, kind commonv1.MediaKind, folder string) (FolderIdentity, error) {
	g := &gateway{w: w, st: st}
	var id FolderIdentity
	switch kind {
	case commonv1.MediaKindArtist:
		var tags []*commonv1.MediaTags
		if !mbidFolderRE.MatchString(folder) {
			// A token decides alone, so its folder's files are not probed.
			var err error
			if tags, err = w.folderTags(ctx, st, folder); err != nil {
				return id, err
			}
		}
		id = IdentifyArtist(ctx, folder, tags, g.lookup, g.search)
	case commonv1.MediaKindAuthor:
		id = IdentifyAuthor(ctx, folder, g.search)
	case commonv1.MediaKindAudiobook:
		dirs := strings.Split(folder, "/")
		author := ""
		if len(dirs) > 1 {
			author = dirs[0]
		}
		id = IdentifyAudiobook(ctx, author, dirs[len(dirs)-1], st.region(), g.search)
	}
	if g.err != nil {
		return id, g.err
	}
	logging.FromContext(ctx).Debug("identified a container folder", "kind", kind, "folder", folder,
		"id", id.ID, "evidence", id.Evidence, "code", id.Code)
	return id, nil
}

// settle turns a folder's identity into the walk's verdict: an unmatched
// folder, listed once; the existing item of this root the identity is; an
// unmatched folder when the item is another root's; else the item to
// propose, once per identity in the walk -- a second folder of an identity
// already proposed awaits the first's item, and a later scan attributes it
// by its name or token.
func (w *Worker) settle(st *scanState, kind commonv1.MediaKind, folder string, id FolderIdentity) *folderVerdict {
	if id.Unmatched {
		st.unmatchedFolder(folder, id.Code, id.Reason, id.Candidates, w.now())
		return &folderVerdict{}
	}
	idx := st.nonVideo
	for _, k := range idx.known[kind] {
		if k.id == "" || !strings.EqualFold(k.id, id.ID) {
			continue
		}
		if k.otherRoot == "" {
			return &folderVerdict{existing: k.name}
		}
		st.unmatchedFolder(folder, CodeOtherRootFolder, fmt.Sprintf(
			"the folder is %s %s (id %s, by its %s), which belongs to root folder %q, not to this one: a scan "+
				"attributes a file only to an item of the root folder it scans", kind, k.name, id.ID, id.Evidence, k.otherRoot),
			[]string{k.name}, w.now())
		return &folderVerdict{}
	}
	if v := idx.proposed[string(kind)+"/"+id.ID]; v != nil {
		return &folderVerdict{create: v.create, providerID: v.providerID, proposed: true}
	}

	title := id.Title
	if strings.TrimSpace(title) == "" {
		title = folder
	}
	c := &schema.ItemCreate{
		Kind: string(kind), QualityProfileRef: st.root.Spec.Defaults.QualityProfileRef, RootFolderRef: st.root.Name,
		Title: title, Folder: folder,
	}
	v := &folderVerdict{create: c}
	switch kind {
	case commonv1.MediaKindArtist:
		c.Name, c.MusicBrainzID = names.Artist(title, id.ID), id.ID
		v.providerID = catalogv1alpha1.ExclusionIDKeyMusicBrainz + ":" + id.ID
	case commonv1.MediaKindAuthor:
		c.Name, c.OpenLibraryID = names.Author(title, id.ID), id.ID
		v.providerID = catalogv1alpha1.ExclusionIDKeyOpenLibrary + ":" + id.ID
	case commonv1.MediaKindAudiobook:
		c.Name, c.ASIN, c.Region = names.Audiobook(title, id.ID), id.ID, st.region()
		c.Year = int32(id.Year) //nolint:gosec // a release year
		v.providerID = catalogv1alpha1.ExclusionIDKeyASIN + ":" + id.ID
	}
	idx.proposed[string(kind)+"/"+id.ID] = v
	return v
}

// proposeContainer reports an artist's or author's folder's proposal once,
// with the file that found it, in the Series' shape (createSeries): a
// present observation whose attribution creates the item and freezes
// nothing -- the applier creates it, pinned to the folder, and records no
// file, since the folder's files await the item's children. A dry run
// counts the item instead. A verdict with nothing to propose, or already
// proposed, reports nothing.
func (w *Worker) proposeContainer(ctx context.Context, st *scanState, path string, info os.FileInfo, v *folderVerdict) error {
	if v.create == nil || v.proposed {
		return nil
	}
	v.proposed = true
	if st.task.DryRun {
		st.progress.ItemsCreated++
		return nil
	}
	obs := schema.ScanObservation{
		Kind: schema.ScanObservationPresent, Path: path,
		SizeBytes: info.Size(), ModTime: info.ModTime().UTC(),
		Attribution: &schema.ScanAttribution{
			Kind: v.create.Kind, Name: v.create.Name, ProviderID: v.providerID, Evidence: "folder", Create: v.create,
		},
	}
	if err := w.observe(ctx, st, obs); err != nil {
		return err
	}
	logging.FromContext(ctx).Info("proposed a container from its folder",
		"kind", v.create.Kind, "name", v.create.Name, "providerID", v.providerID, "folder", v.create.Folder)
	return nil
}

// folderTags probes up to maxTaggedFiles of an artist folder's audio files,
// in walk order, and answers their tags (nil for one that carries none or
// could not be probed), heartbeating before each probe. A worker that
// probes nothing answers none. The error is a failed heartbeat's or the
// walk's cancellation.
func (w *Worker) folderTags(ctx context.Context, st *scanState, folder string) ([]*commonv1.MediaTags, error) {
	if w.ProbeAudio == nil {
		return nil, nil
	}
	var files []string
	classifier := fileimport.ClassifierFor(commonv1.MediaKindAlbum, st.root.Spec.Path, w.SampleMaxBytes)
	err := classifier.Walk(ctx, filepath.Join(st.root.Spec.Path, folder), func(p string, _ os.FileInfo, class fsops.FileClass) error {
		if class != fsops.ClassMedia {
			return nil
		}
		if files = append(files, p); len(files) == maxTaggedFiles {
			return filepath.SkipAll
		}
		return nil
	}, func(string, error) error { return nil })
	if err != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	tags := make([]*commonv1.MediaTags, 0, len(files))
	for _, p := range files {
		if err := st.beatNow(ctx, true); err != nil {
			return nil, err
		}
		pctx, cancel := context.WithTimeout(ctx, fileimport.AudioProbeTimeout)
		ap, perr := w.ProbeAudio(pctx, p)
		cancel()
		if perr != nil {
			logging.FromContext(ctx).Debug("could not probe a file for its folder's artist tags",
				"path", relPath(st.root.Spec.Path, p), "err", perr)
			tags = append(tags, nil)
			continue
		}
		tags = append(tags, ap.Tags)
	}
	return tags, nil
}

// region is the Audible marketplace the root's audiobooks are read in: its
// defaults.region, us when unset (the field's own default).
func (s *scanState) region() string {
	if r := s.root.Spec.Defaults.Region; r != "" {
		return string(r)
	}
	return string(catalogv1alpha1.AudiobookRegionUS)
}

// unmatchedFolder lists a container folder the walk could not identify
// once, Folder set (UnmatchedFile.folder; design D16): its candidates are
// the provider's hits, and its files are counted as awaiting, not listed. A
// resumed walk that resolves the folder again lists it no second time.
func (s *scanState) unmatchedFolder(folder, code, reason string, candidates []string, at time.Time) {
	if slices.ContainsFunc(s.progress.Unmatched, func(u scanprogress.UnmatchedFile) bool { return u.Folder && u.Path == folder }) {
		return
	}
	s.unmatched(folder, code, reason, candidates, at)
	s.progress.Unmatched[len(s.progress.Unmatched)-1].Folder = true
}

// freshNonVideoSpec is what a first sighting of a non-video file freezes:
// the quality the file settles (fileimport.FrozenFileQuality: a music file
// is probed for its codec and bitrate, anything else goes by its extension,
// and a 24-bit marker in rel counts where the probe cannot say) and the
// kind's release type. There is no revision, group, edition or language to
// parse from a track, part, ebook or issue file name, and no custom-format
// score, which is TRaSH video data.
//
// The audio probe it ran comes back too (nil when none ran): an album
// file's album and track and an audiobook part's order read its tags.
func (w *Worker) freshNonVideoSpec(ctx context.Context, kind commonv1.MediaKind, path, rel string) (mediafilespec.Frozen, *mediainfo.AudioProbe) {
	f := mediafilespec.Frozen{ReleaseType: fileimport.ReleaseTypeFor(kind)}
	q, ok, ap := fileimport.FrozenFileQualityProbe(ctx, w.ProbeAudio, kind, path, rel)
	if ok {
		f.Quality = &q
	}
	return f, ap
}

// recordAttribution reports an attributed file to the manager's scan
// applier, with the MediaFile the walk read for its path (existing, nil when
// none). fresh is what a new MediaFile freezes; the applier re-asserts an
// existing one's frozen fields verbatim, and refuses -- never re-points --
// a path already recorded against another item (P41: unmatched,
// recorded_elsewhere).
func (w *Worker) recordAttribution(
	ctx context.Context, st *scanState, path, rel string, info os.FileInfo,
	existing *catalogv1alpha1.MediaFile, ref commonv1.MediaRef, fresh mediafilespec.Frozen,
) error {
	if err := w.proposeFile(ctx, st, path, info, existing, ref, &fresh, nil, "match"); err != nil {
		return err
	}
	logging.FromContext(ctx).Debug("reported a scanned file", "kind", ref.Kind, "item", ref.Name, "path", rel)
	return nil
}
