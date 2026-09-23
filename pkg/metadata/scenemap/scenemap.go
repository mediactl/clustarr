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

// Package scenemap supplies scene numbering: the table that says which
// TVDB episode a scene release's "S02E05" or "- 18" means for a series
// whose releases are numbered differently from TVDB -- most anime. Without
// it a scene-numbered release with no absolute number is rejected as the
// wrong item.
//
// The source is TheXEM (https://thexem.info), exactly as Sonarr uses it
// (Sonarr src/NzbDrone.Core/DataAugmentation/Xem: XemProxy.cs,
// XemService.cs, Model/XemValues.cs): /map/havemap lists the TVDB series
// TheXEM holds a table for, /map/all returns one series' rows, and
// /map/allNames returns every series' scene titles with the scene season
// each names. XEM is the client; Cached puts a metadata.Cache in front of
// it and skips the per-series request for a series havemap does not list,
// as Sonarr does.
//
// Map carries the rows as plain data. Numbering has the same fields, in
// the same order and of the same types, as pkg/decision's
// EpisodeNumbering, so a consumer converts one to the other with a plain
// type conversion and neither package imports the other.
package scenemap

import "context"

// Numbering is one episode's place in one numbering scheme. Absolute is 0
// when the scheme gives the episode none.
type Numbering struct {
	Season   int `json:"season"`
	Episode  int `json:"episode"`
	Absolute int `json:"absolute"`
}

// Mapping is one row of a series' table: the numbering scene releases use
// for an episode, and the TVDB numbering of the same episode. Several rows
// can share a scene number (a double-length scene episode that TVDB splits
// in two) or a TVDB number.
type Mapping struct {
	Scene Numbering `json:"scene"`
	TVDB  Numbering `json:"tvdb"`
}

// SceneName is a title scene releases use for a series, and the scene
// season that title means: "Shinryaku!? Ika Musume" is season 2 of TVDB
// 195721. Season is nil for a title that names the whole series (TheXEM's
// -1).
type SceneName struct {
	Title  string `json:"title"`
	Season *int   `json:"season,omitempty"`
}

// Map is everything TheXEM holds for one TVDB series. A series TheXEM
// does not map has an empty Map, which is not an error: most series are
// numbered the same way on TVDB and in the scene.
type Map struct {
	TVDBID   int64       `json:"tvdbId"`
	Mappings []Mapping   `json:"mappings,omitempty"`
	Names    []SceneName `json:"names,omitempty"`
}

// Source is what a consumer of scene numbering depends on. Cached is the
// production implementation; a test hands in a literal Map.
type Source interface {
	SceneMap(ctx context.Context, tvdbID int64) (*Map, error)
}
