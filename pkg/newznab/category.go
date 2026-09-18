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

// Package newznab is the Newznab category table: the fixed, five-family
// standard tree Clustarr uses (movies/audio/tv/books/other), plus the
// conversions between a category id and a Clustarr MediaKind. It does no
// I/O and parses no XML -- that is pkg/torznab's job.
package newznab

import (
	"crypto/sha1" //nolint:gosec // not a security use: a stable, low-collision id derivation, not a cryptographic guarantee.
	"encoding/binary"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// CategoryID is a Newznab category id, such as 2000 (Movies) or 2040
// (Movies/HD).
type CategoryID int32

// SubCategory is a leaf Newznab category, one level under a Category.
type SubCategory struct {
	ID   CategoryID
	Name string
}

// Category is a x000-aligned parent Newznab category and its leaves.
type Category struct {
	ID   CategoryID
	Name string
	Sub  []SubCategory
}

// Standard Newznab categories, the five families Clustarr uses (§4.4 of
// docs/research/indexers.md). Values are transcribed verbatim from the
// standard table.
const (
	CatMovies        CategoryID = 2000
	CatMoviesForeign CategoryID = 2010
	CatMoviesOther   CategoryID = 2020
	CatMoviesSD      CategoryID = 2030
	CatMoviesHD      CategoryID = 2040
	CatMoviesUHD     CategoryID = 2045
	CatMoviesBluRay  CategoryID = 2050
	CatMovies3D      CategoryID = 2060
	CatMoviesDVD     CategoryID = 2070
	CatMoviesWEBDL   CategoryID = 2080
	CatMoviesX265    CategoryID = 2090

	CatAudio          CategoryID = 3000
	CatAudioMP3       CategoryID = 3010
	CatAudioVideo     CategoryID = 3020
	CatAudioAudiobook CategoryID = 3030
	CatAudioLossless  CategoryID = 3040
	CatAudioOther     CategoryID = 3050
	CatAudioForeign   CategoryID = 3060

	CatTV            CategoryID = 5000
	CatTVWEBDL       CategoryID = 5010
	CatTVForeign     CategoryID = 5020
	CatTVSD          CategoryID = 5030
	CatTVHD          CategoryID = 5040
	CatTVUHD         CategoryID = 5045
	CatTVOther       CategoryID = 5050
	CatTVSport       CategoryID = 5060
	CatTVAnime       CategoryID = 5070
	CatTVDocumentary CategoryID = 5080
	CatTVX265        CategoryID = 5090

	CatBooks          CategoryID = 7000
	CatBooksMags      CategoryID = 7010
	CatBooksEBook     CategoryID = 7020
	CatBooksComics    CategoryID = 7030
	CatBooksTechnical CategoryID = 7040
	CatBooksOther     CategoryID = 7050
	CatBooksForeign   CategoryID = 7060

	CatOther       CategoryID = 8000
	CatOtherMisc   CategoryID = 8010
	CatOtherHashed CategoryID = 8020

	// CustomCategoryOffset marks indexer-specific categories: 100000 + a
	// hash of the tracker's own category id/name. Ids at or above this are
	// never expanded and their Parent() is themselves.
	CustomCategoryOffset CategoryID = 100000
)

// tree is the source of truth Tree, Expand and pkg/torznab.WriteCaps read
// from.
var tree = []Category{
	{
		ID: CatMovies, Name: "Movies",
		Sub: []SubCategory{
			{ID: CatMoviesForeign, Name: "Movies/Foreign"},
			{ID: CatMoviesOther, Name: "Movies/Other"},
			{ID: CatMoviesSD, Name: "Movies/SD"},
			{ID: CatMoviesHD, Name: "Movies/HD"},
			{ID: CatMoviesUHD, Name: "Movies/UHD"},
			{ID: CatMoviesBluRay, Name: "Movies/BluRay"},
			{ID: CatMovies3D, Name: "Movies/3D"},
			{ID: CatMoviesDVD, Name: "Movies/DVD"},
			{ID: CatMoviesWEBDL, Name: "Movies/WEB-DL"},
			{ID: CatMoviesX265, Name: "Movies/x265"},
		},
	},
	{
		ID: CatAudio, Name: "Audio",
		Sub: []SubCategory{
			{ID: CatAudioMP3, Name: "Audio/MP3"},
			{ID: CatAudioVideo, Name: "Audio/Video"},
			{ID: CatAudioAudiobook, Name: "Audio/Audiobook"},
			{ID: CatAudioLossless, Name: "Audio/Lossless"},
			{ID: CatAudioOther, Name: "Audio/Other"},
			{ID: CatAudioForeign, Name: "Audio/Foreign"},
		},
	},
	{
		ID: CatTV, Name: "TV",
		Sub: []SubCategory{
			{ID: CatTVWEBDL, Name: "TV/WEB-DL"},
			{ID: CatTVForeign, Name: "TV/Foreign"},
			{ID: CatTVSD, Name: "TV/SD"},
			{ID: CatTVHD, Name: "TV/HD"},
			{ID: CatTVUHD, Name: "TV/UHD"},
			{ID: CatTVOther, Name: "TV/Other"},
			{ID: CatTVSport, Name: "TV/Sport"},
			{ID: CatTVAnime, Name: "TV/Anime"},
			{ID: CatTVDocumentary, Name: "TV/Documentary"},
			{ID: CatTVX265, Name: "TV/x265"},
		},
	},
	{
		ID: CatBooks, Name: "Books",
		Sub: []SubCategory{
			{ID: CatBooksMags, Name: "Books/Mags"},
			{ID: CatBooksEBook, Name: "Books/EBook"},
			{ID: CatBooksComics, Name: "Books/Comics"},
			{ID: CatBooksTechnical, Name: "Books/Technical"},
			{ID: CatBooksOther, Name: "Books/Other"},
			{ID: CatBooksForeign, Name: "Books/Foreign"},
		},
	},
	{
		ID: CatOther, Name: "Other",
		Sub: []SubCategory{
			{ID: CatOtherMisc, Name: "Other/Misc"},
			{ID: CatOtherHashed, Name: "Other/Hashed"},
		},
	},
}

// Tree returns the full standard tree for the five families Clustarr uses
// (2000/3000/5000/7000/8000; §4.4). It is the source of truth Expand and
// pkg/torznab.WriteCaps read from.
func Tree() []Category {
	out := make([]Category, len(tree))
	copy(out, tree)
	return out
}

// Parent returns the x000-aligned parent of a standard category id, or the
// id itself when it is custom (>= CustomCategoryOffset).
func (c CategoryID) Parent() CategoryID {
	if c >= CustomCategoryOffset {
		return c
	}
	return (c / 1000) * 1000
}

// Expand replaces every standard parent id in ids with itself plus all of
// its Tree() sub-category ids; ids >= CustomCategoryOffset pass through
// untouched. Order is preserved, duplicates are dropped.
func Expand(ids []CategoryID) []CategoryID {
	byID := make(map[CategoryID]Category, len(tree))
	for _, c := range tree {
		byID[c.ID] = c
	}

	seen := make(map[CategoryID]bool, len(ids))
	var out []CategoryID
	add := func(id CategoryID) {
		if seen[id] {
			return
		}
		seen[id] = true
		out = append(out, id)
	}

	for _, id := range ids {
		if id >= CustomCategoryOffset {
			add(id)
			continue
		}
		add(id)
		if c, ok := byID[id]; ok {
			for _, sub := range c.Sub {
				add(sub.ID)
			}
		}
	}
	return out
}

// Custom derives a stable indexer-specific category id from the tracker's
// own (possibly non-numeric) category identifier: 100000 + the first two
// bytes of sha1(trackerID) as a big-endian uint16.
func Custom(trackerID string) CategoryID {
	sum := sha1.Sum([]byte(trackerID)) //nolint:gosec // stable id derivation only, not a security digest.
	return CustomCategoryOffset + CategoryID(binary.BigEndian.Uint16(sum[:2]))
}

// ByKind returns Clustarr's default category set for a media kind:
// movie->2000, series/episode->5000, artist/album->3000,
// audiobook->3030, author/book->7020, comic/issue->7030. Anime (5070) is
// not reachable from a MediaKind -- it is a search-time overlay
// (Indexer.spec.animeCategories), not a catalog kind.
func ByKind(kind commonv1.MediaKind) []CategoryID {
	switch kind {
	case commonv1.MediaKindMovie:
		return []CategoryID{CatMovies}
	case commonv1.MediaKindSeries, commonv1.MediaKindEpisode:
		return []CategoryID{CatTV}
	case commonv1.MediaKindArtist, commonv1.MediaKindAlbum:
		return []CategoryID{CatAudio}
	case commonv1.MediaKindAudiobook:
		return []CategoryID{CatAudioAudiobook}
	case commonv1.MediaKindAuthor, commonv1.MediaKindBook:
		return []CategoryID{CatBooksEBook}
	case commonv1.MediaKindComic, commonv1.MediaKindIssue:
		return []CategoryID{CatBooksComics}
	default:
		return nil
	}
}
