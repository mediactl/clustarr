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
	"strconv"

	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// The field indexes this controller reads, under names of its own: the
// Series and fileimport packages register similar ones, and a second
// registration under one name is an "indexer conflict" at start.
const (
	mediaFileByTarget = "librarydelete.spec.mediaRef.target"
	childByParent     = "librarydelete.parent"
)

// RegisterIndexes registers every index Reconcile reads, once, on idx.
func RegisterIndexes(ctx context.Context, idx client.FieldIndexer) error {
	if err := idx.IndexField(ctx, &catalogv1alpha1.MediaFile{}, mediaFileByTarget, func(o client.Object) []string {
		mf, ok := o.(*catalogv1alpha1.MediaFile)
		if !ok || mf.Spec.MediaRef.Name == "" {
			return nil
		}
		ref := mf.Spec.MediaRef
		out := []string{TargetKey(ref.Kind, ref.Name)}
		for _, k := range ref.Keys {
			if k != ref.Name {
				out = append(out, TargetKey(ref.Kind, k))
			}
		}
		return out
	}); err != nil {
		return err
	}
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
	rootFolderRef func(client.Object) string
	path          func(client.Object) string
	// childKind is the kind of its children and newChildren a list of them
	// ("" and nil for Movie and Audiobook).
	childKind   commonv1.MediaKind
	newChildren func() client.ObjectList
	// oneFolder is a kind whose files all sit in one folder (Movie,
	// Audiobook): when status.path is not on disk, that folder is the
	// item's (see Reconciler.target).
	oneFolder bool
	// exclusion is the ImportExclusion spec that keeps it out; ok false for
	// a kind with none.
	exclusion func(client.Object) (catalogv1alpha1.ImportExclusionSpec, bool)
}

const exclusionReason = "deleted from the library"

// bookKind is a Book's path, for the occupant check only: a Book has its
// own folder (under its Author's) but no Delete of its own.
func bookKind() kindSpec {
	return kindSpec{
		kind:    commonv1.MediaKindBook,
		newList: func() client.ObjectList { return &catalogv1alpha1.BookList{} },
		path:    func(o client.Object) string { return o.(*catalogv1alpha1.Book).Status.Path },
	}
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
			exclusion: func(client.Object) (catalogv1alpha1.ImportExclusionSpec, bool) {
				return catalogv1alpha1.ImportExclusionSpec{}, false
			},
		},
		{
			kind:          commonv1.MediaKindAuthor,
			newObject:     func() client.Object { return &catalogv1alpha1.Author{} },
			newList:       func() client.ObjectList { return &catalogv1alpha1.AuthorList{} },
			rootFolderRef: func(o client.Object) string { return o.(*catalogv1alpha1.Author).Spec.RootFolderRef },
			path:          func(o client.Object) string { return o.(*catalogv1alpha1.Author).Status.Path },
			childKind:     commonv1.MediaKindBook,
			newChildren:   func() client.ObjectList { return &catalogv1alpha1.BookList{} },
			exclusion: func(client.Object) (catalogv1alpha1.ImportExclusionSpec, bool) {
				return catalogv1alpha1.ImportExclusionSpec{}, false
			},
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
	}
}
