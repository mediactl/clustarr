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

package cardigann

import (
	"github.com/mediactl/clustarr/pkg/newznab"
)

// canonicalCategories maps every one of the 71 names in schema-v11.json's
// IndexerCategories enum to a newznab.CategoryID. Only the 37 names in the
// five families pkg/newznab actually defines (Movies x10, Audio x7, TV x10,
// Books x7, Other x3) get their own constant; the remaining 34 (Console
// x15, PC x8, XXX x11 — media kinds Clustarr's catalog never has) fold to
// newznab.CatOther.
var canonicalCategories = map[string]newznab.CategoryID{
	"Movies":         newznab.CatMovies,
	"Movies/Foreign": newznab.CatMoviesForeign,
	"Movies/Other":   newznab.CatMoviesOther,
	"Movies/SD":      newznab.CatMoviesSD,
	"Movies/HD":      newznab.CatMoviesHD,
	"Movies/UHD":     newznab.CatMoviesUHD,
	"Movies/BluRay":  newznab.CatMoviesBluRay,
	"Movies/3D":      newznab.CatMovies3D,
	"Movies/DVD":     newznab.CatMoviesDVD,
	"Movies/WEB-DL":  newznab.CatMoviesWEBDL,

	"Audio":           newznab.CatAudio,
	"Audio/MP3":       newznab.CatAudioMP3,
	"Audio/Video":     newznab.CatAudioVideo,
	"Audio/Audiobook": newznab.CatAudioAudiobook,
	"Audio/Lossless":  newznab.CatAudioLossless,
	"Audio/Other":     newznab.CatAudioOther,
	"Audio/Foreign":   newznab.CatAudioForeign,

	"TV":             newznab.CatTV,
	"TV/WEB-DL":      newznab.CatTVWEBDL,
	"TV/Foreign":     newznab.CatTVForeign,
	"TV/SD":          newznab.CatTVSD,
	"TV/HD":          newznab.CatTVHD,
	"TV/UHD":         newznab.CatTVUHD,
	"TV/Other":       newznab.CatTVOther,
	"TV/Sport":       newznab.CatTVSport,
	"TV/Anime":       newznab.CatTVAnime,
	"TV/Documentary": newznab.CatTVDocumentary,

	"Books":           newznab.CatBooks,
	"Books/Mags":      newznab.CatBooksMags,
	"Books/EBook":     newznab.CatBooksEBook,
	"Books/Comics":    newznab.CatBooksComics,
	"Books/Technical": newznab.CatBooksTechnical,
	"Books/Other":     newznab.CatBooksOther,
	"Books/Foreign":   newznab.CatBooksForeign,

	"Other":        newznab.CatOther,
	"Other/Misc":   newznab.CatOtherMisc,
	"Other/Hashed": newznab.CatOtherHashed,

	// Console (15), PC (8) and XXX (11): Clustarr's catalog has no
	// software/console/adult media kind, so every name in these three
	// families folds to the generic "other" bucket.
	"Console": newznab.CatOther, "Console/NDS": newznab.CatOther, "Console/PSP": newznab.CatOther,
	"Console/Wii": newznab.CatOther, "Console/XBox": newznab.CatOther, "Console/XBox 360": newznab.CatOther,
	"Console/Wiiware": newznab.CatOther, "Console/XBox 360 DLC": newznab.CatOther, "Console/PS3": newznab.CatOther,
	"Console/Other": newznab.CatOther, "Console/3DS": newznab.CatOther, "Console/PS Vita": newznab.CatOther,
	"Console/WiiU": newznab.CatOther, "Console/XBox One": newznab.CatOther, "Console/PS4": newznab.CatOther,

	"PC": newznab.CatOther, "PC/0day": newznab.CatOther, "PC/ISO": newznab.CatOther,
	"PC/Mac": newznab.CatOther, "PC/Mobile-Other": newznab.CatOther, "PC/Games": newznab.CatOther,
	"PC/Mobile-iOS": newznab.CatOther, "PC/Mobile-Android": newznab.CatOther,

	"XXX": newznab.CatOther, "XXX/DVD": newznab.CatOther, "XXX/WMV": newznab.CatOther,
	"XXX/XviD": newznab.CatOther, "XXX/x264": newznab.CatOther, "XXX/UHD": newznab.CatOther,
	"XXX/Pack": newznab.CatOther, "XXX/ImageSet": newznab.CatOther, "XXX/Other": newznab.CatOther,
	"XXX/SD": newznab.CatOther, "XXX/WEB-DL": newznab.CatOther,
}

// Capabilities is the resolved, indexer-agnostic summary of what a
// Definition can search for — the shape IndexerDefinitionStatus.Caps and
// Indexer.status.caps (spec §4.3) are populated from.
type Capabilities struct {
	Modes             map[string][]string
	Categories        []newznab.CategoryID // resolved, deduped; Console/PC/XXX-family Cardigann categories fold to newznab.CatOther
	AllowRawSearch    bool
	AllowTVSearchIMDB bool
}

// Capabilities derives Capabilities from d.Caps; pure, no I/O.
func (d *Definition) Capabilities() Capabilities {
	seen := make(map[newznab.CategoryID]struct{})
	var cats []newznab.CategoryID
	add := func(name string) {
		id, ok := canonicalCategories[name]
		if !ok {
			id = newznab.CatOther
		}
		if _, dup := seen[id]; dup {
			return
		}
		seen[id] = struct{}{}
		cats = append(cats, id)
	}
	for _, m := range d.Caps.CategoryMappings {
		add(m.Cat)
	}
	for _, name := range d.Caps.Categories {
		add(name)
	}
	return Capabilities{
		Modes:             d.Caps.Modes,
		Categories:        cats,
		AllowRawSearch:    d.Caps.AllowRawSearch,
		AllowTVSearchIMDB: d.Caps.AllowTVSearchIMDB,
	}
}

// CategoryMapper resolves between a Definition's own tracker category ids
// and the standard Newznab ids Clustarr's search/decision layer speaks.
type CategoryMapper struct{ def *Definition }

// NewCategoryMapper builds a CategoryMapper over def's category mappings.
func NewCategoryMapper(def *Definition) CategoryMapper {
	return CategoryMapper{def: def}
}

// ToTracker returns every CategoryMapping.ID whose Cat resolves to one of
// categories (or to newznab.CatOther, for categories with no exact
// mapping), for building a search request's category filter.
func (m CategoryMapper) ToTracker(categories []newznab.CategoryID) []string {
	expanded := newznab.Expand(categories)
	want := make(map[newznab.CategoryID]struct{}, len(expanded))
	for _, id := range expanded {
		want[id] = struct{}{}
	}
	var out []string
	for _, cm := range m.def.Caps.CategoryMappings {
		id, ok := canonicalCategories[cm.Cat]
		if !ok {
			id = newznab.CatOther
		}
		if _, ok := want[id]; ok {
			out = append(out, string(cm.ID))
		}
	}
	return out
}

// FromTracker resolves one tracker category id (as it appears in a search
// result row) back to Newznab ids — usually one, occasionally more if the
// definition maps the same id to several Cat entries.
func (m CategoryMapper) FromTracker(trackerID string) []newznab.CategoryID {
	var out []newznab.CategoryID
	for _, cm := range m.def.Caps.CategoryMappings {
		if string(cm.ID) != trackerID {
			continue
		}
		id, ok := canonicalCategories[cm.Cat]
		if !ok {
			id = newznab.CatOther
		}
		out = append(out, id)
	}
	return out
}

// FromTrackerDesc is FromTracker's fallback for definitions whose rows only
// expose a description string (categorydesc), not an id.
func (m CategoryMapper) FromTrackerDesc(desc string) []newznab.CategoryID {
	var out []newznab.CategoryID
	for _, cm := range m.def.Caps.CategoryMappings {
		if cm.Desc != desc {
			continue
		}
		id, ok := canonicalCategories[cm.Cat]
		if !ok {
			id = newznab.CatOther
		}
		out = append(out, id)
	}
	return out
}
