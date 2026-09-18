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

package torznab

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/mediactl/clustarr/pkg/newznab"
)

// Query is the normalized, protocol-agnostic search request. Values
// builds the wire query string; Validate checks it against a Caps.
type Query struct {
	Type                             SearchMode
	Q                                string
	Categories                       []newznab.CategoryID
	IMDBID, TMDBID, TVDBID, TVMazeID string
	Season                           *int
	Episode                          string
	Artist, Album                    string
	Author, Title                    string
	Limit, Offset                    int
}

// Values builds the wire query string for q. apikey is omitted when empty.
// extended=1 is always set, so servers return every attr (§4.1).
func (q Query) Values(apikey string) url.Values {
	v := url.Values{}
	v.Set("t", string(q.Type))
	v.Set("extended", "1")
	if apikey != "" {
		v.Set("apikey", apikey)
	}
	if q.Q != "" {
		v.Set("q", q.Q)
	}
	if len(q.Categories) > 0 {
		cats := make([]string, len(q.Categories))
		for i, c := range q.Categories {
			cats[i] = strconv.Itoa(int(c))
		}
		v.Set("cat", strings.Join(cats, ","))
	}
	if q.IMDBID != "" {
		v.Set("imdbid", strings.TrimPrefix(q.IMDBID, "tt"))
	}
	if q.TMDBID != "" {
		v.Set("tmdbid", q.TMDBID)
	}
	if q.TVDBID != "" {
		v.Set("tvdbid", q.TVDBID)
	}
	if q.TVMazeID != "" {
		v.Set("tvmazeid", q.TVMazeID)
	}
	if q.Season != nil {
		v.Set("season", strconv.Itoa(*q.Season))
	}
	if q.Episode != "" {
		v.Set("ep", q.Episode)
	}
	if q.Artist != "" {
		v.Set("artist", q.Artist)
	}
	if q.Album != "" {
		v.Set("album", q.Album)
	}
	if q.Author != "" {
		v.Set("author", q.Author)
	}
	if q.Title != "" {
		v.Set("title", q.Title)
	}
	if q.Limit != 0 {
		v.Set("limit", strconv.Itoa(q.Limit))
	}
	if q.Offset != 0 {
		v.Set("offset", strconv.Itoa(q.Offset))
	}
	return v
}

// Validate checks q against caps, returning an error when q.Type is not
// advertised as available.
func (q Query) Validate(caps Caps) error {
	s, ok := caps.Modes[q.Type]
	if !ok || !s.Available {
		return fmt.Errorf("torznab: search mode %q is not available on this indexer", q.Type)
	}
	return nil
}
