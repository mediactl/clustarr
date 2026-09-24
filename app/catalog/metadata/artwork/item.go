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

package artwork

import (
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// ErrNoArtwork marks an object or kind that carries no spec.artwork and
// status.artwork: anything but the eight kinds spec §B.6 names.
var ErrNoArtwork = errors.New("artwork: kind has no artwork")

// item is what Sync and Drift read off any of the eight kinds.
type item struct {
	kind      commonv1.MediaKind
	overrides []catalogv1alpha1.ArtworkOverride
	images    []catalogv1alpha1.Image
	entries   []catalogv1alpha1.ArtworkEntry
}

// itemOf reads obj's artwork inputs: its kind, spec.artwork,
// status.metadata.images (nil before the first metadata fetch) and
// status.artwork.
func itemOf(obj client.Object) (item, error) {
	switch o := obj.(type) {
	case *catalogv1alpha1.Movie:
		var imgs []catalogv1alpha1.Image
		if o.Status.Metadata != nil {
			imgs = o.Status.Metadata.Images
		}
		return item{commonv1.MediaKindMovie, o.Spec.Artwork, imgs, o.Status.Artwork}, nil
	case *catalogv1alpha1.Series:
		var imgs []catalogv1alpha1.Image
		if o.Status.Metadata != nil {
			imgs = o.Status.Metadata.Images
		}
		return item{commonv1.MediaKindSeries, o.Spec.Artwork, imgs, o.Status.Artwork}, nil
	case *catalogv1alpha1.Artist:
		var imgs []catalogv1alpha1.Image
		if o.Status.Metadata != nil {
			imgs = o.Status.Metadata.Images
		}
		return item{commonv1.MediaKindArtist, o.Spec.Artwork, imgs, o.Status.Artwork}, nil
	case *catalogv1alpha1.Album:
		var imgs []catalogv1alpha1.Image
		if o.Status.Metadata != nil {
			imgs = o.Status.Metadata.Images
		}
		return item{commonv1.MediaKindAlbum, o.Spec.Artwork, imgs, o.Status.Artwork}, nil
	case *catalogv1alpha1.Author:
		var imgs []catalogv1alpha1.Image
		if o.Status.Metadata != nil {
			imgs = o.Status.Metadata.Images
		}
		return item{commonv1.MediaKindAuthor, o.Spec.Artwork, imgs, o.Status.Artwork}, nil
	case *catalogv1alpha1.Book:
		var imgs []catalogv1alpha1.Image
		if o.Status.Metadata != nil {
			imgs = o.Status.Metadata.Images
		}
		return item{commonv1.MediaKindBook, o.Spec.Artwork, imgs, o.Status.Artwork}, nil
	case *catalogv1alpha1.Audiobook:
		var imgs []catalogv1alpha1.Image
		if o.Status.Metadata != nil {
			imgs = o.Status.Metadata.Images
		}
		return item{commonv1.MediaKindAudiobook, o.Spec.Artwork, imgs, o.Status.Artwork}, nil
	case *catalogv1alpha1.Comic:
		var imgs []catalogv1alpha1.Image
		if o.Status.Metadata != nil {
			imgs = o.Status.Metadata.Images
		}
		return item{commonv1.MediaKindComic, o.Spec.Artwork, imgs, o.Status.Artwork}, nil
	default:
		return item{}, fmt.Errorf("%w: %T", ErrNoArtwork, obj)
	}
}

// newObject returns an empty object of kind, ready for a Get.
func newObject(kind commonv1.MediaKind) (client.Object, error) {
	switch kind {
	case commonv1.MediaKindMovie:
		return &catalogv1alpha1.Movie{}, nil
	case commonv1.MediaKindSeries:
		return &catalogv1alpha1.Series{}, nil
	case commonv1.MediaKindArtist:
		return &catalogv1alpha1.Artist{}, nil
	case commonv1.MediaKindAlbum:
		return &catalogv1alpha1.Album{}, nil
	case commonv1.MediaKindAuthor:
		return &catalogv1alpha1.Author{}, nil
	case commonv1.MediaKindBook:
		return &catalogv1alpha1.Book{}, nil
	case commonv1.MediaKindAudiobook:
		return &catalogv1alpha1.Audiobook{}, nil
	case commonv1.MediaKindComic:
		return &catalogv1alpha1.Comic{}, nil
	default:
		return nil, fmt.Errorf("%w: %q", ErrNoArtwork, kind)
	}
}

// gvkFor is the GroupVersionKind of kind's list, for the reaper's
// metadata-only List.
func gvkFor(kind commonv1.MediaKind) (schema.GroupVersionKind, bool) {
	gv := catalogv1alpha1.GroupVersion
	switch kind {
	case commonv1.MediaKindMovie:
		return gv.WithKind("MovieList"), true
	case commonv1.MediaKindSeries:
		return gv.WithKind("SeriesList"), true
	case commonv1.MediaKindArtist:
		return gv.WithKind("ArtistList"), true
	case commonv1.MediaKindAlbum:
		return gv.WithKind("AlbumList"), true
	case commonv1.MediaKindAuthor:
		return gv.WithKind("AuthorList"), true
	case commonv1.MediaKindBook:
		return gv.WithKind("BookList"), true
	case commonv1.MediaKindAudiobook:
		return gv.WithKind("AudiobookList"), true
	case commonv1.MediaKindComic:
		return gv.WithKind("ComicList"), true
	default:
		return schema.GroupVersionKind{}, false
	}
}
