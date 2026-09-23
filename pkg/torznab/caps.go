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

// Package torznab is the Torznab/Newznab wire client: caps, search and
// error parsing, an HTTP client and the server-side encoders that are
// exact inverses of the parsers.
//
// The caller owns pacing. Client does not rate-limit unless built with
// WithRateLimit: the controller that drives it holds one *ratelimit.Limiter
// per indexer host, derived from Indexer.spec.requestDelay and
// Indexer.spec.limits (api/index/v1alpha1), and a library-side default
// would sit in series underneath it and silently halve the configured
// rate.
package torznab

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/mediactl/clustarr/pkg/newznab"
)

// SearchMode is a Torznab search function name, as it appears in caps XML
// and in the `t=` query parameter.
type SearchMode string

// Torznab search modes (docs/research/indexers.md §4.1).
const (
	ModeSearch      SearchMode = "search"
	ModeTVSearch    SearchMode = "tvsearch"
	ModeMovieSearch SearchMode = "movie"
	ModeMusicSearch SearchMode = "music"
	ModeAudioSearch SearchMode = "audio" // spec alias of music
	ModeBookSearch  SearchMode = "book"
)

// capsElement is the <X-search> tag name for a SearchMode; distinct from
// the `t=` value above (e.g. t=movie but <movie-search .../> in caps).
func (m SearchMode) capsElement() string {
	switch m {
	case ModeSearch:
		return "search"
	case ModeTVSearch:
		return "tv-search"
	case ModeMovieSearch:
		return "movie-search"
	case ModeMusicSearch:
		return "music-search"
	case ModeAudioSearch:
		return "audio-search"
	case ModeBookSearch:
		return "book-search"
	default:
		return string(m) + "-search"
	}
}

// allSearchModes lists every SearchMode caps parsing/writing recognises, in
// caps XML document order.
var allSearchModes = []SearchMode{ModeSearch, ModeTVSearch, ModeMovieSearch, ModeMusicSearch, ModeAudioSearch, ModeBookSearch}

// Searching is one <X-search available=".." supportedParams=".."/> entry.
type Searching struct {
	Available       bool
	SupportedParams []string
	SearchEngine    string // "raw" when the indexer accepts free-text q
}

// Caps is the parsed response of t=caps.
type Caps struct {
	ServerTitle   string
	LimitsDefault int
	LimitsMax     int
	Modes         map[SearchMode]Searching
	Categories    []newznab.Category
	Tags          map[string]string // flag name -> description
}

// Supports reports whether mode is available and accepts param.
func (c Caps) Supports(mode SearchMode, param string) bool {
	s, ok := c.Modes[mode]
	if !ok || !s.Available {
		return false
	}
	for _, p := range s.SupportedParams {
		if p == param {
			return true
		}
	}
	return false
}

// ErrMalformedCaps is what every t=caps document ParseCaps cannot read
// matches: not XML, truncated, or carrying a value its typed field refuses.
//
// The last case is the one this sentinel exists for. wireCaps.Limits stays
// TYPED (ints, not strings, unlike wireItem's lenient Size): a caps document
// is one small, authoritative answer about what the indexer accepts, so a
// <limits max="lots"> is not something to degrade past silently -- a guessed
// limit would page the indexer at a size it never offered. encoding/xml
// reports that as a bare *strconv.NumError, which reads to a caller as an
// arbitrary parse failure; wrapping it lets the Indexer reconciler report
// "the indexer's caps document is malformed" as its own reason rather than a
// generic probe failure.
var ErrMalformedCaps = errors.New("torznab: malformed caps document")

// wireCaps mirrors the <caps> document 1:1 (docs/research/indexers.md
// §2.3).
type wireCaps struct {
	XMLName xml.Name `xml:"caps"`
	Server  struct {
		Title string `xml:"title,attr"`
	} `xml:"server"`
	Limits struct {
		Default int `xml:"default,attr"`
		Max     int `xml:"max,attr"`
	} `xml:"limits"`
	Searching struct {
		Search      wireSearching `xml:"search"`
		TVSearch    wireSearching `xml:"tv-search"`
		MovieSearch wireSearching `xml:"movie-search"`
		MusicSearch wireSearching `xml:"music-search"`
		AudioSearch wireSearching `xml:"audio-search"`
		BookSearch  wireSearching `xml:"book-search"`
	} `xml:"searching"`
	Categories struct {
		Category []wireCategory `xml:"category"`
	} `xml:"categories"`
	Tags struct {
		Tag []wireTag `xml:"tag"`
	} `xml:"tags"`
}

type wireSearching struct {
	Available       string `xml:"available,attr"`
	SupportedParams string `xml:"supportedParams,attr"`
	SearchEngine    string `xml:"searchEngine,attr"`
}

type wireCategory struct {
	ID     int32        `xml:"id,attr"`
	Name   string       `xml:"name,attr"`
	Subcat []wireSubcat `xml:"subcat"`
}

type wireSubcat struct {
	ID   int32  `xml:"id,attr"`
	Name string `xml:"name,attr"`
}

type wireTag struct {
	Name        string `xml:"name,attr"`
	Description string `xml:"description,attr"`
}

// ParseCaps parses r as a t=caps response document. Any document it cannot
// read -- including a <limits> attribute that is not an integer -- is an
// error matching [ErrMalformedCaps].
func ParseCaps(r io.Reader) (Caps, error) {
	var wc wireCaps
	if err := xml.NewDecoder(r).Decode(&wc); err != nil {
		return Caps{}, fmt.Errorf("%w: %w", ErrMalformedCaps, err)
	}

	byElement := map[string]wireSearching{
		ModeSearch.capsElement():      wc.Searching.Search,
		ModeTVSearch.capsElement():    wc.Searching.TVSearch,
		ModeMovieSearch.capsElement(): wc.Searching.MovieSearch,
		ModeMusicSearch.capsElement(): wc.Searching.MusicSearch,
		ModeAudioSearch.capsElement(): wc.Searching.AudioSearch,
		ModeBookSearch.capsElement():  wc.Searching.BookSearch,
	}

	modes := make(map[SearchMode]Searching, len(allSearchModes))
	for _, m := range allSearchModes {
		ws := byElement[m.capsElement()]
		modes[m] = Searching{
			Available:       ws.Available == "yes",
			SupportedParams: splitParams(ws.SupportedParams),
			SearchEngine:    ws.SearchEngine,
		}
	}

	categories := make([]newznab.Category, 0, len(wc.Categories.Category))
	for _, wcat := range wc.Categories.Category {
		sub := make([]newznab.SubCategory, 0, len(wcat.Subcat))
		for _, ws := range wcat.Subcat {
			sub = append(sub, newznab.SubCategory{ID: newznab.CategoryID(ws.ID), Name: ws.Name})
		}
		categories = append(categories, newznab.Category{
			ID:   newznab.CategoryID(wcat.ID),
			Name: wcat.Name,
			Sub:  sub,
		})
	}

	var tags map[string]string
	if len(wc.Tags.Tag) > 0 {
		tags = make(map[string]string, len(wc.Tags.Tag))
		for _, wt := range wc.Tags.Tag {
			tags[wt.Name] = wt.Description
		}
	}

	return Caps{
		ServerTitle:   wc.Server.Title,
		LimitsDefault: wc.Limits.Default,
		LimitsMax:     wc.Limits.Max,
		Modes:         modes,
		Categories:    categories,
		Tags:          tags,
	}, nil
}

// splitParams splits a comma-separated supportedParams attribute value,
// returning nil (not [""]) for an empty string.
func splitParams(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}
