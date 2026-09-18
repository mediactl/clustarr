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
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/mediactl/clustarr/pkg/newznab"
)

// Release is one search result item, with every wire attribute the client
// needs as a typed field, plus a verbatim catch-all for the rest -- the
// same split the research note's own §11 Release sketch uses. This is the
// struct pkg/cardigann (Task B7) returns from its own Search.
type Release struct {
	Title       string
	GUID        string
	Link        string
	CommentURL  string
	PubDate     time.Time
	Size        int64
	Description string
	Categories  []newznab.CategoryID

	// torrent. DownloadVolumeFactor/UploadVolumeFactor/MinimumRatio are
	// *float64, not a CLAUDE.md-banned type here: they are wire-protocol
	// decimals carried verbatim from the indexer, not a human-typed config
	// value, and this package never persists a Release to a CRD.
	Seeders              *int32
	Leechers             *int32
	Peers                *int32
	Grabs                *int32
	Files                *int32
	InfoHash             string
	MagnetURL            string
	DownloadVolumeFactor *float64
	UploadVolumeFactor   *float64
	MinimumRatio         *float64
	MinimumSeedTime      *int64 // seconds

	// usenet (Newznab-native)
	Group      string
	Poster     string
	UsenetDate *time.Time
	Password   *int32
	NFO        *int32
	Info       string // nfo url

	// IDs is keyed by commonv1alpha1.IDKeyIMDB/IDKeyTMDB/IDKeyTVDB, plus
	// "tvmazeid" (no common.IDKey* constant exists for it). The IMDb value
	// carries the canonical "tt" prefix.
	IDs map[string]string

	// Attrs holds every torznab:/newznab: attr verbatim (name -> all
	// values, since e.g. category/tag/genre repeat), including ones with
	// no typed field above (season, episode, genre, year, language, ...).
	Attrs map[string][]string
}

// wireEnclosure mirrors an RSS <enclosure> element.
type wireEnclosure struct {
	URL    string `xml:"url,attr"`
	Length int64  `xml:"length,attr"`
	Type   string `xml:"type,attr"`
}

// wireAttr matches BOTH <torznab:attr> and <newznab:attr> -- and any future
// namespace variant such as cardigann-go's non-canonical
// http://torznab.github.io/schemas/2015/feed -- because it keys off the
// element's local name only, never its namespace URI or prefix.
type wireAttr struct {
	XMLName xml.Name
	Name    string `xml:"name,attr"`
	Value   string `xml:"value,attr"`
}

// wireItem mirrors a single <item> 1:1 (docs/research/indexers.md §4.2).
//
// Size is a string, not an int64: encoding/xml aborts the ENTIRE Decode
// with an error the moment any element it is unmarshaling into a numeric
// Go field fails strconv parsing, which would turn one item's malformed
// <size> into a hard failure for the whole feed (see
// TestParseResultsMalformedNumericValueInOneItemDoesNotFailTheWholeFeed).
// Keeping it a string here and parsing it ourselves in newRelease lets a
// bad value degrade to Release.Size == 0, the same graceful-degradation
// contract every torznab:/newznab: attr already gets via parseInt32 etc.
type wireItem struct {
	Title       string         `xml:"title"`
	GUID        string         `xml:"guid"`
	Link        string         `xml:"link"`
	Comments    string         `xml:"comments"`
	PubDate     string         `xml:"pubDate"`
	Size        string         `xml:"size"`
	Description string         `xml:"description"`
	Categories  []int32        `xml:"category"`
	Enclosure   *wireEnclosure `xml:"enclosure"`
	Extra       []wireAttr     `xml:",any"` // every element the named fields above don't claim
}

// wireFeed mirrors an <rss><channel> document.
type wireFeed struct {
	XMLName xml.Name `xml:"rss"`
	Channel struct {
		Item []wireItem `xml:"item"`
	} `xml:"channel"`
}

// ParseItem parses a single <item> (used directly by table tests).
func ParseItem(r io.Reader) (Release, error) {
	var wi wireItem
	if err := xml.NewDecoder(r).Decode(&wi); err != nil {
		return Release{}, err
	}
	return newRelease(wi)
}

// ParseResults parses a whole <rss><channel> feed into its items.
func ParseResults(r io.Reader) ([]Release, error) {
	var wf wireFeed
	if err := xml.NewDecoder(r).Decode(&wf); err != nil {
		return nil, err
	}

	rels := make([]Release, 0, len(wf.Channel.Item))
	for _, wi := range wf.Channel.Item {
		rel, err := newRelease(wi)
		if err != nil {
			return nil, err
		}
		rels = append(rels, rel)
	}
	return rels, nil
}

func newRelease(w wireItem) (Release, error) {
	rel := Release{
		Title: w.Title, GUID: w.GUID, Link: w.Link, CommentURL: w.Comments,
		Description: w.Description,
		IDs:         map[string]string{}, Attrs: map[string][]string{},
	}
	// A malformed <size> (e.g. a humanized string like "12.5GB") degrades
	// to 0 rather than failing the parse -- see wireItem's doc comment.
	if v, err := strconv.ParseInt(w.Size, 10, 64); err == nil {
		rel.Size = v
	}
	if w.PubDate != "" {
		t, err := time.Parse(time.RFC1123Z, w.PubDate)
		if err != nil {
			return Release{}, fmt.Errorf("torznab: pubDate %q: %w", w.PubDate, err)
		}
		// Normalized to UTC (rather than kept in whatever fixed-offset
		// zone time.Parse constructed) so two independently parsed
		// occurrences of the same instant -- as WriteResults' round trip
		// produces -- are reflect.DeepEqual, not just Equal(): they share
		// the single time.UTC *Location instead of two structurally
		// separate zones that happen to both mean "+0000".
		rel.PubDate = t.UTC()
	}
	for _, c := range w.Categories {
		rel.Categories = append(rel.Categories, newznab.CategoryID(c))
	}
	if w.Enclosure != nil && rel.Link == "" {
		rel.Link = w.Enclosure.URL
	}
	for _, a := range w.Extra {
		if a.XMLName.Local != "attr" {
			continue // e.g. a foreign atom:link element; not an attr
		}
		rel.Attrs[a.Name] = append(rel.Attrs[a.Name], a.Value)
		applyAttr(&rel, a.Name, a.Value)
	}
	if len(rel.IDs) == 0 {
		rel.IDs = nil
	}
	if len(rel.Attrs) == 0 {
		rel.Attrs = nil
	}
	return rel, nil
}

// applyAttr maps one torznab:/newznab: attr onto rel's typed fields. Names
// with no typed field above only land in rel.Attrs (already handled by the
// caller before applyAttr runs). A malformed numeric value is dropped
// silently rather than failing the whole parse -- the raw string is still
// preserved verbatim in rel.Attrs.
func applyAttr(rel *Release, name, value string) {
	switch name {
	case "seeders":
		rel.Seeders = parseInt32(value)
	case "leechers":
		rel.Leechers = parseInt32(value)
	case "peers":
		rel.Peers = parseInt32(value)
	case "grabs":
		rel.Grabs = parseInt32(value)
	case "files":
		rel.Files = parseInt32(value)
	case "infohash":
		rel.InfoHash = value
	case "magneturl":
		rel.MagnetURL = value
	case "downloadvolumefactor":
		rel.DownloadVolumeFactor = parseFloat64(value)
	case "uploadvolumefactor":
		rel.UploadVolumeFactor = parseFloat64(value)
	case "minimumratio":
		rel.MinimumRatio = parseFloat64(value)
	case "minimumseedtime":
		rel.MinimumSeedTime = parseInt64(value)
	case "imdbid":
		rel.IDs["imdb"] = value // already "tt"-prefixed
	case "imdb":
		if _, ok := rel.IDs["imdb"]; !ok {
			rel.IDs["imdb"] = "tt" + zeroPad(value, 7)
		}
	case "tmdbid":
		rel.IDs["tmdb"] = value
	case "tvdbid":
		rel.IDs["tvdb"] = value
	case "tvmazeid":
		rel.IDs["tvmazeid"] = value
	case "category":
		if id, err := strconv.ParseInt(value, 10, 32); err == nil {
			rel.addCategoryIfAbsent(newznab.CategoryID(id))
		}
	case "group":
		rel.Group = value
	case "poster":
		rel.Poster = value
	case "info":
		rel.Info = value
	case "usenetdate":
		if t, err := time.Parse(time.RFC1123Z, value); err == nil {
			u := t.UTC() // see the PubDate comment in newRelease for why
			rel.UsenetDate = &u
		}
	case "password":
		rel.Password = parseInt32(value)
	case "nfo":
		rel.NFO = parseInt32(value)
	}
}

// addCategoryIfAbsent appends id to rel.Categories unless it is already
// present, so a torznab:attr category entry that duplicates the base
// <category> element (see search_with_attrs.xml) does not double up.
func (rel *Release) addCategoryIfAbsent(id newznab.CategoryID) {
	for _, existing := range rel.Categories {
		if existing == id {
			return
		}
	}
	rel.Categories = append(rel.Categories, id)
}

func parseInt32(s string) *int32 {
	v, err := strconv.ParseInt(s, 10, 32)
	if err != nil {
		return nil
	}
	v32 := int32(v)
	return &v32
}

func parseInt64(s string) *int64 {
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return nil
	}
	return &v
}

func parseFloat64(s string) *float64 {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil
	}
	return &v
}

// zeroPad left-pads s with '0' to width n.
func zeroPad(s string, n int) string {
	for len(s) < n {
		s = "0" + s
	}
	return s
}
