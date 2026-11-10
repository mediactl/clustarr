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

package librarydelete

import (
	"context"
	"fmt"
	"strconv"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// The field index this controller registers, under a name of its own (a
// second registration under one name is an "indexer conflict" at start). It
// finds MediaFiles through the remediation loop's one item index,
// mfindex.Item (loop spec §3.16), which the loop registers.
const (
	childByParent = "librarydelete.parent"
)

// RegisterIndexes registers every index Reconcile reads, once, on idx.
func RegisterIndexes(ctx context.Context, idx client.FieldIndexer) error {
	parents := []struct {
		obj    client.Object
		parent func(client.Object) string
	}{
		{&catalogv1alpha1.Episode{}, func(o client.Object) string { return o.(*catalogv1alpha1.Episode).Spec.SeriesRef }},
		{&catalogv1alpha1.Album{}, func(o client.Object) string { return o.(*catalogv1alpha1.Album).Spec.ArtistRef }},
		{&catalogv1alpha1.Book{}, func(o client.Object) string {
			if ref := o.(*catalogv1alpha1.Book).Spec.AuthorRef; ref != nil {
				return *ref
			}
			return ""
		}},
		{&catalogv1alpha1.Issue{}, func(o client.Object) string { return o.(*catalogv1alpha1.Issue).Spec.ComicRef }},
	}
	for _, p := range parents {
		if err := idx.IndexField(ctx, p.obj, childByParent, func(o client.Object) []string {
			if name := p.parent(o); name != "" {
				return []string{name}
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

// kindSpec is what the delete needs to know of one deletable kind.
type kindSpec struct {
	kind commonv1.MediaKind
	// newObject is an empty object of the kind, newList an empty list.
	newObject func() client.Object
	newList   func() client.ObjectList
	// rootFolderRef and path read the item's RootFolder and status.path.
	// rootFolderRef is nil for a kind with no RootFolder of its own (an
	// Album, whose parent names it).
	rootFolderRef func(client.Object) string
	path          func(client.Object) string
	// parent is the container an item of this kind lives inside -- an
	// Album's Artist -- whose RootFolder is the item's and whose folder
	// holds the item's (nil for a kind with a folder of its own).
	parent *parentSpec
	// childKind is the kind of its children and newChildren a list of them
	// ("" and nil for Movie, Audiobook, Album and Book).
	childKind   commonv1.MediaKind
	newChildren func() client.ObjectList
	// oneFolder is a kind whose files all sit in one folder (Movie,
	// Audiobook, a standalone Book): when status.path is not on disk, that
	// folder is the item's (see Reconciler.target).
	oneFolder bool
	// rootIsNoFolder is a kind whose status.path may read the RootFolder's
	// own path and mean no folder at all: a standalone Book written before
	// 2026-11-10, when app/catalog/controller/book.Path rendered "" for a
	// book without an author and the root stood in. Such a path is blanked
	// (Reconciler.target), so the file's folder stands in as for a scanned
	// movie. For every other kind a path at the root is a resolution gone
	// wrong, and Check refuses it as outside the item.
	rootIsNoFolder bool
	// guard refuses, wrapping ErrRefused, an item the kind does not delete
	// on its own: an author's book, which goes with its author (nil for a
	// kind that deletes every item).
	guard func(client.Object) error
	// exclusion is the ImportExclusion spec that keeps it out; ok false for
	// a kind with none.
	exclusion func(client.Object) (catalogv1alpha1.ImportExclusionSpec, bool)
}

// parentSpec is how a kind without a folder of its own reaches the
// container it lives in (2026-11-10 non-video design §9.2: an album's
// files live in its artist's folder, and its own subfolder is the one part
// of that folder a delete may remove).
type parentSpec struct {
	kind commonv1.MediaKind
	// newObject is an empty parent; ref reads the item's reference to it.
	newObject func() client.Object
	ref       func(client.Object) string
	// rootFolderRef and path read the parent's RootFolder and status.path.
	rootFolderRef func(client.Object) string
	path          func(client.Object) string
}

const exclusionReason = "deleted from the library"

// noExclusion is the exclusion of a kind with none (Artist, Author):
// nothing reads one for them (catalogv1alpha1.ExclusionKindFor).
func noExclusion(client.Object) (catalogv1alpha1.ImportExclusionSpec, bool) {
	return catalogv1alpha1.ImportExclusionSpec{}, false
}

// yearOf is the year of a metadata release date for an exclusion's display
// field, 0 without one. The apiserver hands a metav1.Time back in the
// replica's local zone, so the calendar field is read in UTC (CLAUDE.md's
// gotcha: 1 January 00:30Z read as the previous year west of UTC).
func yearOf(t *metav1.Time) int32 {
	if t == nil || t.IsZero() {
		return 0
	}
	return int32(t.UTC().Year())
}

func kindFor(kind commonv1.MediaKind) kindSpec {
	for _, k := range kinds() {
		if k.kind == kind {
			return k
		}
	}
	panic("librarydelete: no kind " + string(kind))
}

func kinds() []kindSpec {
	return []kindSpec{
		{
			kind:          commonv1.MediaKindMovie,
			newObject:     func() client.Object { return &catalogv1alpha1.Movie{} },
			newList:       func() client.ObjectList { return &catalogv1alpha1.MovieList{} },
			rootFolderRef: func(o client.Object) string { return o.(*catalogv1alpha1.Movie).Spec.RootFolderRef },
			path:          func(o client.Object) string { return o.(*catalogv1alpha1.Movie).Status.Path },
			oneFolder:     true,
			exclusion: func(o client.Object) (catalogv1alpha1.ImportExclusionSpec, bool) {
				m := o.(*catalogv1alpha1.Movie)
				spec := catalogv1alpha1.ImportExclusionSpec{
					Kind: catalogv1alpha1.ExclusionKindMovie, Reason: exclusionReason,
					ExternalIDs: map[string]string{catalogv1alpha1.ExclusionIDKeyTMDB: strconv.FormatInt(m.Spec.TmdbID, 10)},
				}
				if md := m.Status.Metadata; md != nil {
					spec.Title, spec.Year = md.Title, md.Year
				}
				return spec, true
			},
		},
		{
			kind:          commonv1.MediaKindSeries,
			newObject:     func() client.Object { return &catalogv1alpha1.Series{} },
			newList:       func() client.ObjectList { return &catalogv1alpha1.SeriesList{} },
			rootFolderRef: func(o client.Object) string { return o.(*catalogv1alpha1.Series).Spec.RootFolderRef },
			path:          func(o client.Object) string { return o.(*catalogv1alpha1.Series).Status.Path },
			childKind:     commonv1.MediaKindEpisode,
			newChildren:   func() client.ObjectList { return &catalogv1alpha1.EpisodeList{} },
			exclusion: func(o client.Object) (catalogv1alpha1.ImportExclusionSpec, bool) {
				s := o.(*catalogv1alpha1.Series)
				spec := catalogv1alpha1.ImportExclusionSpec{
					Kind: catalogv1alpha1.ExclusionKindSeries, Reason: exclusionReason,
					ExternalIDs: map[string]string{catalogv1alpha1.ExclusionIDKeyTVDB: strconv.FormatInt(s.Spec.TvdbID, 10)},
				}
				if md := s.Status.Metadata; md != nil {
					spec.Title, spec.Year = md.Title, md.Year
				}
				return spec, true
			},
		},
		{
			kind:          commonv1.MediaKindArtist,
			newObject:     func() client.Object { return &catalogv1alpha1.Artist{} },
			newList:       func() client.ObjectList { return &catalogv1alpha1.ArtistList{} },
			rootFolderRef: func(o client.Object) string { return o.(*catalogv1alpha1.Artist).Spec.RootFolderRef },
			path:          func(o client.Object) string { return o.(*catalogv1alpha1.Artist).Status.Path },
			childKind:     commonv1.MediaKindAlbum,
			newChildren:   func() client.ObjectList { return &catalogv1alpha1.AlbumList{} },
			exclusion:     noExclusion,
		},
		{
			kind:          commonv1.MediaKindAuthor,
			newObject:     func() client.Object { return &catalogv1alpha1.Author{} },
			newList:       func() client.ObjectList { return &catalogv1alpha1.AuthorList{} },
			rootFolderRef: func(o client.Object) string { return o.(*catalogv1alpha1.Author).Spec.RootFolderRef },
			path:          func(o client.Object) string { return o.(*catalogv1alpha1.Author).Status.Path },
			childKind:     commonv1.MediaKindBook,
			newChildren:   func() client.ObjectList { return &catalogv1alpha1.BookList{} },
			exclusion:     noExclusion,
		},
		{
			kind:          commonv1.MediaKindAudiobook,
			newObject:     func() client.Object { return &catalogv1alpha1.Audiobook{} },
			newList:       func() client.ObjectList { return &catalogv1alpha1.AudiobookList{} },
			rootFolderRef: func(o client.Object) string { return o.(*catalogv1alpha1.Audiobook).Spec.RootFolderRef },
			path:          func(o client.Object) string { return o.(*catalogv1alpha1.Audiobook).Status.Path },
			oneFolder:     true,
			exclusion: func(o client.Object) (catalogv1alpha1.ImportExclusionSpec, bool) {
				ab := o.(*catalogv1alpha1.Audiobook)
				return catalogv1alpha1.ImportExclusionSpec{
					Kind: catalogv1alpha1.ExclusionKindAudiobook, Reason: exclusionReason,
					ExternalIDs: map[string]string{catalogv1alpha1.ExclusionIDKeyASIN: ab.Spec.ASIN},
				}, true
			},
		},
		{
			kind:          commonv1.MediaKindComic,
			newObject:     func() client.Object { return &catalogv1alpha1.Comic{} },
			newList:       func() client.ObjectList { return &catalogv1alpha1.ComicList{} },
			rootFolderRef: func(o client.Object) string { return o.(*catalogv1alpha1.Comic).Spec.RootFolderRef },
			path:          func(o client.Object) string { return o.(*catalogv1alpha1.Comic).Status.Path },
			childKind:     commonv1.MediaKindIssue,
			newChildren:   func() client.ObjectList { return &catalogv1alpha1.IssueList{} },
			exclusion: func(o client.Object) (catalogv1alpha1.ImportExclusionSpec, bool) {
				cm := o.(*catalogv1alpha1.Comic)
				// Source is comicvine or mangadex, the same words as the
				// exclusion's id keys.
				return catalogv1alpha1.ImportExclusionSpec{
					Kind: catalogv1alpha1.ExclusionKindComic, Reason: exclusionReason,
					ExternalIDs: map[string]string{string(cm.Spec.Source): cm.Spec.SourceID},
				}, true
			},
		},
		{
			// An Album (2026-11-10 non-video design §9.2) lives in its
			// Artist's folder, which names its RootFolder: a files delete
			// removes the album's own track files and its own subfolder,
			// never the artist's folder (Reconciler.target), and is refused
			// while the artist's folder is unknown (Target.Shared); a records
			// delete its MediaFiles and the Album, the Artist's rollup
			// following on its next pass. The Artist is not told: its fan-out
			// (app/catalog/controller/artist's ensureAlbum, at the next
			// childsync -- a spec edit or the metadata refresh) creates every
			// album the provider lists that it does not find, monitored by
			// the artist's monitor mode, so a deleted album of a listed
			// release group comes back, as Lidarr's does, and is searched for
			// again under monitor mode all. Deleting its files for good means
			// unmonitoring the album, or the artist, first.
			kind:      commonv1.MediaKindAlbum,
			newObject: func() client.Object { return &catalogv1alpha1.Album{} },
			newList:   func() client.ObjectList { return &catalogv1alpha1.AlbumList{} },
			path:      func(o client.Object) string { return o.(*catalogv1alpha1.Album).Status.Path },
			parent: &parentSpec{
				kind:          commonv1.MediaKindArtist,
				newObject:     func() client.Object { return &catalogv1alpha1.Artist{} },
				ref:           func(o client.Object) string { return o.(*catalogv1alpha1.Album).Spec.ArtistRef },
				rootFolderRef: func(o client.Object) string { return o.(*catalogv1alpha1.Artist).Spec.RootFolderRef },
				path:          func(o client.Object) string { return o.(*catalogv1alpha1.Artist).Status.Path },
			},
			// The exclusion is keyed by the album's MusicBrainz release-group
			// id under the musicbrainz key: the one MBID a list entry for an
			// album carries (app/import/importliststate's
			// ToMetadataExternalIDs, metadata.KeyMBReleaseGroup), which the
			// import-list sync's isExcluded looks up. It governs import
			// lists alone: the Artist's fan-out above reads no exclusion, so
			// the ui offers no checkbox for an album
			// (catalogv1alpha1.ExclusionKindFor answers false) until the
			// fan-out honours one, and meanwhile only a delete asked for by
			// hand with the delete-add-exclusion annotation writes it -- for
			// the list sync, once a list can yield albums, and for the
			// fan-out once it reads the bucket.
			exclusion: func(o client.Object) (catalogv1alpha1.ImportExclusionSpec, bool) {
				a := o.(*catalogv1alpha1.Album)
				spec := catalogv1alpha1.ImportExclusionSpec{
					Kind: catalogv1alpha1.ExclusionKindAlbum, Reason: exclusionReason,
					ExternalIDs: map[string]string{catalogv1alpha1.ExclusionIDKeyMusicBrainz: a.Spec.ReleaseGroupID},
				}
				if md := a.Status.Metadata; md != nil {
					spec.Title, spec.Year = md.Title, yearOf(md.ReleaseDate)
				}
				return spec, true
			},
		},
		{
			// A Book (design §9.3) is deletable only standalone: its own
			// folder under the root it names (spec.rootFolderRef, which
			// the CEL rule requires of a book without an author;
			// app/catalog/controller/book.Path), its one file's folder when
			// status.path is not that (oneFolder). An author's book lives
			// in its author's folder and goes with its author (guard).
			kind:      commonv1.MediaKindBook,
			newObject: func() client.Object { return &catalogv1alpha1.Book{} },
			newList:   func() client.ObjectList { return &catalogv1alpha1.BookList{} },
			rootFolderRef: func(o client.Object) string {
				if ref := o.(*catalogv1alpha1.Book).Spec.RootFolderRef; ref != nil {
					return *ref
				}
				return ""
			},
			path:           func(o client.Object) string { return o.(*catalogv1alpha1.Book).Status.Path },
			oneFolder:      true,
			rootIsNoFolder: true,
			guard: func(o client.Object) error {
				if ref := o.(*catalogv1alpha1.Book).Spec.AuthorRef; ref != nil && *ref != "" {
					return fmt.Errorf("%w: an author's book is deleted with its author (%s)", ErrRefused, *ref)
				}
				return nil
			},
			// The exclusion is keyed by the book's Open Library work id
			// under the openlibrary key (metadata.KeyOpenLibraryWork on a
			// list entry, pkg/importlist.ExternalIDs.OpenLibrary), which the
			// import-list sync's isExcluded looks up.
			exclusion: func(o client.Object) (catalogv1alpha1.ImportExclusionSpec, bool) {
				b := o.(*catalogv1alpha1.Book)
				spec := catalogv1alpha1.ImportExclusionSpec{
					Kind: catalogv1alpha1.ExclusionKindBook, Reason: exclusionReason,
					ExternalIDs: map[string]string{catalogv1alpha1.ExclusionIDKeyOpenLibrary: b.Spec.WorkID},
				}
				if md := b.Status.Metadata; md != nil {
					spec.Title, spec.Year = md.Title, yearOf(md.ReleaseDate)
				}
				return spec, true
			},
		},
	}
}
