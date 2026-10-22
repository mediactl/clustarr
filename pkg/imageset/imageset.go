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

// Package imageset parses Kometa's image-files YAML -- what MediUX and
// ThePosterDB export for a show or a film -- into artwork overrides
// (docs/superpowers/specs/2026-10-21-image-sets-design.md):
//
//	384541:
//	  url_poster: https://api.mediux.pro/assets/<uuid>
//	  url_background: https://api.mediux.pro/assets/<uuid>
//	  seasons:
//	    1:
//	      url_poster: https://api.mediux.pro/assets/<uuid>
//	      episodes:
//	        1:
//	          url_poster: https://api.mediux.pro/assets/<uuid>
//
// The top-level keys are the items' provider ids (TheTVDB for a show, TMDB
// for a film), as numbers or strings. tpdb_poster and tpdb_background name
// a ThePosterDB asset by id, which is https://theposterdb.com/api/assets/<id>.
package imageset

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"sigs.k8s.io/yaml"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// ErrNoEntries is a set that names no item.
var ErrNoEntries = errors.New("imageset: no entries")

// Entry is one item's images in a set.
type Entry struct {
	// ID is the top-level key: the item's provider id as written.
	ID string
	// Artwork is the item's own poster and background.
	Artwork []catalogv1.ArtworkOverride
	// SeasonArtwork is the seasons' posters and backgrounds and the
	// episodes' thumbnails, in season then episode order.
	SeasonArtwork []catalogv1.SeasonArtworkOverride
}

// text is a YAML scalar read as text: a ThePosterDB id is written as a
// number as often as a string.
type text string

func (t *text) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*t = text(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	*t = text(n.String())
	return nil
}

type rawImages struct {
	URLPoster      text `json:"url_poster"`
	URLBackground  text `json:"url_background"`
	TPDBPoster     text `json:"tpdb_poster"`
	TPDBBackground text `json:"tpdb_background"`
}

type rawEpisode struct {
	rawImages
}

type rawSeason struct {
	rawImages
	Episodes map[string]rawEpisode `json:"episodes"`
}

type rawEntry struct {
	rawImages
	Seasons map[string]rawSeason `json:"seasons"`
}

// Parse reads a set's YAML. An entry with no image at all is left out; a
// set with none is ErrNoEntries. Entries come back sorted by id.
func Parse(text string) ([]Entry, error) {
	var raw map[string]rawEntry
	if err := yaml.Unmarshal([]byte(text), &raw); err != nil {
		return nil, fmt.Errorf("imageset: %w", err)
	}
	var out []Entry
	for id, re := range raw {
		e := Entry{ID: strings.TrimSpace(id)}
		if u := re.poster(); u != "" {
			e.Artwork = append(e.Artwork, catalogv1.ArtworkOverride{Type: catalogv1.ImageTypePoster, URL: u})
		}
		if u := re.background(); u != "" {
			e.Artwork = append(e.Artwork, catalogv1.ArtworkOverride{Type: catalogv1.ImageTypeFanart, URL: u})
		}
		for _, sn := range sortedKeys(re.Seasons) {
			season, err := number(sn)
			if err != nil {
				return nil, fmt.Errorf("imageset: %s: season %q: %w", id, sn, err)
			}
			rs := re.Seasons[sn]
			if u := rs.poster(); u != "" {
				e.SeasonArtwork = append(e.SeasonArtwork, catalogv1.SeasonArtworkOverride{Season: season, Type: catalogv1.ImageTypePoster, URL: u})
			}
			if u := rs.background(); u != "" {
				e.SeasonArtwork = append(e.SeasonArtwork, catalogv1.SeasonArtworkOverride{Season: season, Type: catalogv1.ImageTypeFanart, URL: u})
			}
			for _, en := range sortedKeys(rs.Episodes) {
				episode, err := number(en)
				if err != nil || episode < 1 {
					return nil, fmt.Errorf("imageset: %s: season %d: episode %q is not a number from 1", id, season, en)
				}
				if u := rs.Episodes[en].poster(); u != "" {
					n := episode
					e.SeasonArtwork = append(e.SeasonArtwork, catalogv1.SeasonArtworkOverride{Season: season, Episode: &n, Type: catalogv1.ImageTypeScreenshot, URL: u})
				}
			}
		}
		if len(e.Artwork)+len(e.SeasonArtwork) > 0 {
			out = append(out, e)
		}
	}
	if len(out) == 0 {
		return nil, ErrNoEntries
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// For is the entry for id, else the set's only entry (a set exported for
// one item names it however its exporter keyed it), else false.
func For(entries []Entry, id string) (Entry, bool) {
	for _, e := range entries {
		if e.ID == id {
			return e, true
		}
	}
	if len(entries) == 1 {
		return entries[0], true
	}
	return Entry{}, false
}

func (r rawImages) poster() string { return pick(string(r.URLPoster), string(r.TPDBPoster)) }
func (r rawImages) background() string {
	return pick(string(r.URLBackground), string(r.TPDBBackground))
}

// pick is url when given, else ThePosterDB's asset URL for id, else "".
func pick(url, tpdbID string) string {
	if u := strings.TrimSpace(url); u != "" {
		return u
	}
	if id := strings.TrimSpace(tpdbID); id != "" {
		return "https://theposterdb.com/api/assets/" + id
	}
	return ""
}

func number(s string) (int32, error) {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 32)
	if err != nil || n < 0 {
		return 0, errors.New("not a number from 0")
	}
	return int32(n), nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, ea := strconv.Atoi(strings.TrimSpace(keys[i]))
		b, eb := strconv.Atoi(strings.TrimSpace(keys[j]))
		if ea == nil && eb == nil {
			return a < b
		}
		return keys[i] < keys[j]
	})
	return keys
}
