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

// Item is what Drift, the gateway's Fetcher.Sync and its Pass read off any
// of the eight kinds.
type Item struct {
	Kind      commonv1.MediaKind
	Overrides []catalogv1alpha1.ArtworkOverride // spec.artwork
	Images    []catalogv1alpha1.Image           // status.metadata.images, nil before the first metadata fetch
	Entries   []catalogv1alpha1.ArtworkEntry    // status.artwork

	// HasOverlay: status.overlay is set (Movie and Series only), so the
	// renderer has an overlay to clear if the poster goes.
	HasOverlay bool

	// Ratings are status.metadata.ratings (Movie and Series only): an input
	// of the overlay, so part of the render task's Msg-Id (RenderToken).
	Ratings []catalogv1alpha1.Rating
}

// ItemOf reads obj's artwork inputs: its kind, spec.artwork,
// status.metadata.images (nil before the first metadata fetch) and
// status.artwork.
func ItemOf(obj client.Object) (Item, error) {
	switch o := obj.(type) {
	case *catalogv1alpha1.Movie:
		it := Item{Kind: commonv1.MediaKindMovie, Overrides: o.Spec.Artwork, Entries: o.Status.Artwork, HasOverlay: o.Status.Overlay != nil}
		if o.Status.Metadata != nil {
			it.Images, it.Ratings = o.Status.Metadata.Images, o.Status.Metadata.Ratings
		}
		return it, nil
	case *catalogv1alpha1.Series:
		it := Item{Kind: commonv1.MediaKindSeries, Overrides: o.Spec.Artwork, Entries: o.Status.Artwork, HasOverlay: o.Status.Overlay != nil}
		if o.Status.Metadata != nil {
			it.Images, it.Ratings = o.Status.Metadata.Images, o.Status.Metadata.Ratings
		}
		return it, nil
	case *catalogv1alpha1.Artist:
		it := Item{Kind: commonv1.MediaKindArtist, Overrides: o.Spec.Artwork, Entries: o.Status.Artwork}
		if o.Status.Metadata != nil {
			it.Images = o.Status.Metadata.Images
		}
		return it, nil
	case *catalogv1alpha1.Album:
		it := Item{Kind: commonv1.MediaKindAlbum, Overrides: o.Spec.Artwork, Entries: o.Status.Artwork}
		if o.Status.Metadata != nil {
			it.Images = o.Status.Metadata.Images
		}
		return it, nil
	case *catalogv1alpha1.Author:
		it := Item{Kind: commonv1.MediaKindAuthor, Overrides: o.Spec.Artwork, Entries: o.Status.Artwork}
		if o.Status.Metadata != nil {
			it.Images = o.Status.Metadata.Images
		}
		return it, nil
	case *catalogv1alpha1.Book:
		it := Item{Kind: commonv1.MediaKindBook, Overrides: o.Spec.Artwork, Entries: o.Status.Artwork}
		if o.Status.Metadata != nil {
			it.Images = o.Status.Metadata.Images
		}
		return it, nil
	case *catalogv1alpha1.Audiobook:
		it := Item{Kind: commonv1.MediaKindAudiobook, Overrides: o.Spec.Artwork, Entries: o.Status.Artwork}
		if o.Status.Metadata != nil {
			it.Images = o.Status.Metadata.Images
		}
		return it, nil
	case *catalogv1alpha1.Comic:
		it := Item{Kind: commonv1.MediaKindComic, Overrides: o.Spec.Artwork, Entries: o.Status.Artwork}
		if o.Status.Metadata != nil {
			it.Images = o.Status.Metadata.Images
		}
		return it, nil
	default:
		return Item{}, fmt.Errorf("%w: %T", ErrNoArtwork, obj)
	}
}

// NewObject returns an empty object of kind, ready for a Get.
func NewObject(kind commonv1.MediaKind) (client.Object, error) {
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

// NewList returns an empty list of kind's items, ready for a List: the
// reaper's audit reads the eight kinds' status.artwork through it.
func NewList(kind commonv1.MediaKind) (client.ObjectList, error) {
	switch kind {
	case commonv1.MediaKindMovie:
		return &catalogv1alpha1.MovieList{}, nil
	case commonv1.MediaKindSeries:
		return &catalogv1alpha1.SeriesList{}, nil
	case commonv1.MediaKindArtist:
		return &catalogv1alpha1.ArtistList{}, nil
	case commonv1.MediaKindAlbum:
		return &catalogv1alpha1.AlbumList{}, nil
	case commonv1.MediaKindAuthor:
		return &catalogv1alpha1.AuthorList{}, nil
	case commonv1.MediaKindBook:
		return &catalogv1alpha1.BookList{}, nil
	case commonv1.MediaKindAudiobook:
		return &catalogv1alpha1.AudiobookList{}, nil
	case commonv1.MediaKindComic:
		return &catalogv1alpha1.ComicList{}, nil
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
