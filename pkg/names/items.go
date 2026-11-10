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

package names

import "strconv"

// Movie is the name a Movie for tmdbID gets. It hashes "movie" and the id,
// not the title, so the rescan, an import list and the UI's Add New agree
// on one object for one film.
func Movie(title string, tmdbID int64) string {
	return ChildName(title, "movie", strconv.FormatInt(tmdbID, 10))
}

// Series is Movie's counterpart for a TVDB id.
func Series(title string, tvdbID int64) string {
	return ChildName(title, "series", strconv.FormatInt(tvdbID, 10))
}

// Artist is the name an Artist for a MusicBrainz artist id gets.
func Artist(name, mbid string) string { return ChildName(name, "artist", mbid) }

// Author is the name an Author for an Open Library author key gets.
func Author(name, olid string) string { return ChildName(name, "author", olid) }

// Comic is the name a Comic for a metadata source's volume id gets
// (2026-11-10 design §9.1, Add New): the source is part of the identity,
// since a ComicVine volume id and a MangaDex manga UUID are different
// namespaces, so "comicvine"/"123" and "mangadex"/"123" never collide.
func Comic(title, source, sourceID string) string {
	return ChildName(title, "comic", source, sourceID)
}

// Audiobook is the name an Audiobook for an Audible ASIN gets.
func Audiobook(title, asin string) string { return ChildName(title, "audiobook", asin) }

// Book is the name a standalone Book for an Open Library work key gets: a
// book Add New adds with no author. An author's own books are named by
// the Author controller after the author (app/catalog/controller/author's
// BookName), so a work added by hand and one an author fans out are two
// objects.
func Book(title, workID string) string { return ChildName(title, "book", workID) }
