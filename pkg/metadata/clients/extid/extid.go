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

// Package extid names the pkg/metadata.ExternalIDs keys the eight clients
// added for the previously unimplemented MetadataProvider types hand out
// and consume, for ids pkg/metadata/ids.go does not name yet.
//
// They belong beside KeyTMDB and friends in pkg/metadata/ids.go, and the
// values here are chosen to be exactly what they will be there: the spec's
// own crosswalk vocabulary (design §4.2 SeriesMetadata.ExternalIDs "tvdb
// tmdb imdb tvmaze anidb anilist mal kitsu"; ComicMetadata.ExternalIDs
// "comicvine metron mangadex anilist mal"; ImportExclusion's "mangadex";
// AuthorSpec.HardcoverID). They live in a package of their own only
// because pkg/metadata was another task's to edit when these clients
// landed; moving them is a rename with no change of value.
package extid

// ExternalIDs keys, in addition to pkg/metadata's own.
const (
	// KeyHardcover is a Hardcover id. It is a book id on a Book, an
	// author id on an Author and an edition id on an Edition -- ExternalIDs
	// is per entity, so one key serves all three, exactly as KeyTMDB
	// serves both movies and series.
	KeyHardcover = "hardcover"
	// KeyMetron is a Metron series id on a comic volume, or a Metron issue
	// id on an issue.
	KeyMetron = "metron"
	// KeyGCD is a Grand Comics Database id, which Metron crosswalks.
	KeyGCD = "gcd"
	// KeyMangaDex is a MangaDex manga UUID.
	KeyMangaDex = "mangadex"
	// KeyMAL is a MyAnimeList id. MAL numbers anime and manga separately,
	// so it is an anime id on a series or movie and a manga id on a comic.
	KeyMAL = "mal"
	// KeyKitsu is a Kitsu id, anime or manga by the same rule as KeyMAL.
	KeyKitsu = "kitsu"
	// KeyAniDB is an AniDB anime id.
	KeyAniDB = "anidb"
	// KeyMangaUpdates is a MangaUpdates series id.
	KeyMangaUpdates = "mangaupdates"
)
