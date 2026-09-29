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
