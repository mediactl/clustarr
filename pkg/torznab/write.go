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
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Canonical Torznab/Newznab namespace URIs. WriteResults declares only
// these -- never the non-canonical cardigann-go variant ParseResults
// tolerates on the way in (see release_test.go's namespace_variant.xml) --
// because we control what we write; tolerance is only ever needed for what
// we read.
const (
	xmlnsAtom    = "http://www.w3.org/2005/Atom"
	xmlnsTorznab = "http://torznab.com/schemas/2015/feed"
	xmlnsNewznab = "http://www.newznab.com/DTD/2010/feeds/attributes/"
)

// WriteCaps writes c as a t=caps document. It is the exact inverse of
// ParseCaps: ParseCaps(WriteCaps(c)) reproduces c.
func WriteCaps(w io.Writer, c Caps) error {
	if _, err := io.WriteString(w, xml.Header); err != nil {
		return err
	}

	var wc wireCaps
	wc.Server.Title = c.ServerTitle
	wc.Limits.Default = c.LimitsDefault
	wc.Limits.Max = c.LimitsMax

	searchingFor := func(mode SearchMode) wireSearching {
		s := c.Modes[mode]
		avail := "no"
		if s.Available {
			avail = "yes"
		}
		return wireSearching{
			Available:       avail,
			SupportedParams: strings.Join(s.SupportedParams, ","),
			SearchEngine:    s.SearchEngine,
		}
	}
	wc.Searching.Search = searchingFor(ModeSearch)
	wc.Searching.TVSearch = searchingFor(ModeTVSearch)
	wc.Searching.MovieSearch = searchingFor(ModeMovieSearch)
	wc.Searching.MusicSearch = searchingFor(ModeMusicSearch)
	wc.Searching.AudioSearch = searchingFor(ModeAudioSearch)
	wc.Searching.BookSearch = searchingFor(ModeBookSearch)

	for _, cat := range c.Categories {
		wcat := wireCategory{ID: int32(cat.ID), Name: cat.Name}
		for _, sub := range cat.Sub {
			wcat.Subcat = append(wcat.Subcat, wireSubcat{ID: int32(sub.ID), Name: sub.Name})
		}
		wc.Categories.Category = append(wc.Categories.Category, wcat)
	}

	tagNames := make([]string, 0, len(c.Tags))
	for name := range c.Tags {
		tagNames = append(tagNames, name)
	}
	sort.Strings(tagNames)
	for _, name := range tagNames {
		wc.Tags.Tag = append(wc.Tags.Tag, wireTag{Name: name, Description: c.Tags[name]})
	}

	return xml.NewEncoder(w).Encode(wc)
}

// wireRSSOut is the write-side counterpart of wireFeed: encoding/xml has no
// notion of namespace-prefixed attribute declarations, so the
// xmlns:torznab/xmlns:newznab attributes are written as ordinary attrs
// whose literal name happens to contain a colon.
type wireRSSOut struct {
	XMLName      xml.Name       `xml:"rss"`
	Version      string         `xml:"version,attr"`
	XMLNSAtom    string         `xml:"xmlns:atom,attr"`
	XMLNSTorznab string         `xml:"xmlns:torznab,attr"`
	XMLNSNewznab string         `xml:"xmlns:newznab,attr"`
	Channel      wireChannelOut `xml:"channel"`
}

type wireChannelOut struct {
	Item []wireItemOut `xml:"item"`
}

type wireItemOut struct {
	Title       string        `xml:"title"`
	GUID        string        `xml:"guid,omitempty"`
	Link        string        `xml:"link,omitempty"`
	Comments    string        `xml:"comments,omitempty"`
	PubDate     string        `xml:"pubDate,omitempty"`
	Size        int64         `xml:"size,omitempty"`
	Description string        `xml:"description,omitempty"`
	Categories  []int32       `xml:"category"`
	Attrs       []wireAttrOut `xml:"torznab:attr"`
}

type wireAttrOut struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr"`
}

// WriteResults writes rels as an <rss><channel> feed. It is the exact
// inverse of ParseResults: ParseResults(WriteResults(rels)) reproduces
// rels, including its raw Attrs, because every typed field is written from
// the original wire string in Release.Attrs whenever one is available and
// only falls back to reformatting the typed value when it is not (a
// Release built without going through ParseItem, e.g. by pkg/cardigann).
func WriteResults(w io.Writer, rels []Release) error {
	if _, err := io.WriteString(w, xml.Header); err != nil {
		return err
	}

	out := wireRSSOut{
		Version:      "2.0",
		XMLNSAtom:    xmlnsAtom,
		XMLNSTorznab: xmlnsTorznab,
		XMLNSNewznab: xmlnsNewznab,
	}
	for _, rel := range rels {
		out.Channel.Item = append(out.Channel.Item, writeItem(rel))
	}

	return xml.NewEncoder(w).Encode(out)
}

func writeItem(rel Release) wireItemOut {
	wi := wireItemOut{
		Title: rel.Title, GUID: rel.GUID, Link: rel.Link, Comments: rel.CommentURL,
		Size: rel.Size, Description: rel.Description,
	}
	if !rel.PubDate.IsZero() {
		wi.PubDate = rel.PubDate.Format(time.RFC1123Z)
	}
	for _, c := range rel.Categories {
		wi.Categories = append(wi.Categories, int32(c))
	}

	skip := make(map[string]bool, len(rel.Attrs))

	// emitTyped writes name from rel.Attrs verbatim when present -- exact
	// byte fidelity for a Release that came from ParseItem/ParseResults --
	// else synthesizes it from the typed field via synth. Either way name
	// is marked skipped so the catch-all loop below never doubles it.
	emitTyped := func(name string, synth func() (string, bool)) {
		if vals, ok := rel.Attrs[name]; ok {
			for _, v := range vals {
				wi.Attrs = append(wi.Attrs, wireAttrOut{Name: name, Value: v})
			}
			skip[name] = true
			return
		}
		if v, ok := synth(); ok {
			wi.Attrs = append(wi.Attrs, wireAttrOut{Name: name, Value: v})
			skip[name] = true
		}
	}

	emitTyped("seeders", int32Synth(rel.Seeders))
	emitTyped("leechers", int32Synth(rel.Leechers))
	emitTyped("peers", int32Synth(rel.Peers))
	emitTyped("grabs", int32Synth(rel.Grabs))
	emitTyped("files", int32Synth(rel.Files))
	emitTyped("infohash", stringSynth(rel.InfoHash))
	emitTyped("magneturl", stringSynth(rel.MagnetURL))
	emitTyped("downloadvolumefactor", float64Synth(rel.DownloadVolumeFactor))
	emitTyped("uploadvolumefactor", float64Synth(rel.UploadVolumeFactor))
	emitTyped("minimumratio", float64Synth(rel.MinimumRatio))
	emitTyped("minimumseedtime", int64Synth(rel.MinimumSeedTime))
	emitTyped("imdbid", mapSynth(rel.IDs, "imdb"))
	emitTyped("tmdbid", mapSynth(rel.IDs, "tmdb"))
	emitTyped("tvdbid", mapSynth(rel.IDs, "tvdb"))
	emitTyped("tvmazeid", mapSynth(rel.IDs, "tvmazeid"))
	emitTyped("group", stringSynth(rel.Group))
	emitTyped("poster", stringSynth(rel.Poster))
	emitTyped("info", stringSynth(rel.Info))
	emitTyped("usenetdate", timeSynth(rel.UsenetDate))
	emitTyped("password", int32Synth(rel.Password))
	emitTyped("nfo", int32Synth(rel.NFO))

	// Every remaining Attrs name -- e.g. genre, plus "imdb" alongside
	// "imdbid" once that's already been claimed above -- has no typed
	// field of its own and is written back verbatim.
	names := make([]string, 0, len(rel.Attrs))
	for name := range rel.Attrs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if skip[name] {
			continue
		}
		for _, v := range rel.Attrs[name] {
			wi.Attrs = append(wi.Attrs, wireAttrOut{Name: name, Value: v})
		}
	}

	return wi
}

func int32Synth(p *int32) func() (string, bool) {
	return func() (string, bool) {
		if p == nil {
			return "", false
		}
		return strconv.FormatInt(int64(*p), 10), true
	}
}

func int64Synth(p *int64) func() (string, bool) {
	return func() (string, bool) {
		if p == nil {
			return "", false
		}
		return strconv.FormatInt(*p, 10), true
	}
}

func float64Synth(p *float64) func() (string, bool) {
	return func() (string, bool) {
		if p == nil {
			return "", false
		}
		return strconv.FormatFloat(*p, 'f', -1, 64), true
	}
}

func stringSynth(s string) func() (string, bool) {
	return func() (string, bool) {
		if s == "" {
			return "", false
		}
		return s, true
	}
}

func mapSynth(m map[string]string, key string) func() (string, bool) {
	return func() (string, bool) {
		v, ok := m[key]
		return v, ok
	}
}

func timeSynth(p *time.Time) func() (string, bool) {
	return func() (string, bool) {
		if p == nil {
			return "", false
		}
		return p.Format(time.RFC1123Z), true
	}
}

// WriteError writes err as a standalone <error code=".." description=".."/>
// document, the exact inverse of ParseError.
func WriteError(w io.Writer, err *Error) error {
	if _, ioErr := io.WriteString(w, xml.Header); ioErr != nil {
		return ioErr
	}
	we := wireError{Code: int(err.Code), Description: err.Description}
	return xml.NewEncoder(w).Encode(we)
}
