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

package metadata

import (
	"context"
	"encoding/json"
)

// PlexSeason is one season's id in Plex's own metadata service.
type PlexSeason struct {
	Number int32  `json:"number"`
	ID     string `json:"id"`
}

// PlexEpisode is one episode's id in Plex's own metadata service, with the
// numbering and TVDB episode id Plex files it under (Plex numbers specials
// its own way, so TVDB is the join key and the pair only a fallback).
type PlexEpisode struct {
	Season  int32  `json:"season"`
	Episode int32  `json:"episode"`
	TVDB    string `json:"tvdb,omitempty"`
	ID      string `json:"id"`
}

// PlexChildren are a show's Plex id and its seasons' and episodes'.
type PlexChildren struct {
	ShowID   string        `json:"showID"`
	Seasons  []PlexSeason  `json:"seasons,omitempty"`
	Episodes []PlexEpisode `json:"episodes,omitempty"`
}

// PlexProvider supplies a show's Plex ids, which the ui's Plex provider
// answers seasons and episodes with as plex:// GUIDs
// (docs/superpowers/specs/2026-10-06-plex-native-guids-design.md).
type PlexProvider interface {
	Provider
	ShowChildren(ctx context.Context, ids ExternalIDs) (*PlexChildren, error)
}

// PlexExtrasProvider supplies an item's extras -- trailers and clips -- as
// Plex's metadata service sends them, for the ui's extras route. PMS asks
// every provider for an item's extras on a refresh and deletes the ones it
// holds when the answer is empty, so a failed fetch is an error and never
// an empty list.
type PlexExtrasProvider interface {
	Provider
	Extras(ctx context.Context, plexID string) ([]json.RawMessage, error)
}

// PlexCollectionProvider names the collection a movie belongs to in Plex's
// own metadata service. Plex cannot look a collection up by its TMDB id,
// but a member movie's Plex metadata carries the collection's plex://
// GUID, which the ui's Plex provider answers the collection with.
type PlexCollectionProvider interface {
	Provider
	// MovieCollection is the 24-hex id of the collection the movie with
	// Plex id moviePlexID belongs to, "" when it belongs to none.
	MovieCollection(ctx context.Context, moviePlexID string) (string, error)
}
