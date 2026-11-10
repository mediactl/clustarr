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

package xmltv

import (
	"bytes"
	"encoding/xml"
	"strconv"
	"strings"
)

// Kind is what a Listing is to Plex: a movie or an episode of a show.
type Kind string

// Listing kinds.
const (
	KindMovie   Kind = "movie"
	KindEpisode Kind = "episode"
)

// Credit is one actor and the role played ("" when the source names none).
type Credit struct {
	Name, Role string
}

// Listing is one programme in the unified model every guide source is
// reduced to, written as XMLTV in the one profile three readers share
// (the custom-guides design's §5.4): PMS's XMLTV reader, cluster-plex's
// guide builder and the ui's index. A field left empty writes nothing.
type Listing struct {
	// Kind is KindMovie or KindEpisode; "" is an episode when Season or
	// Episode is set, else a movie.
	Kind     Kind
	Title    string // the show's title for an episode, the film's for a movie
	SubTitle string // the episode's title
	Desc     string
	Actors   []Credit
	// Directors, Writers and Producers are the credits PMS keeps (§2.3);
	// presenters, guests and the rest are not carried.
	Directors, Writers, Producers []string
	// Date is a year ("2021") or a date ("20210315"): a movie's year, an
	// episode's air year.
	Date       string
	Categories []string // the source's genres; Inner adds Movie or Series
	// Poster is the show's or the film's poster, the programme's icon;
	// Backdrop its background; Still the episode's own image.
	Poster, Backdrop, Still string
	// Season and Episode count from 1; 0 is unknown.
	Season, Episode int
	ProgID          string // a Gracenote-shaped dd_progid, when the source has one
	// PlexGUID is the programme's plex://episode/… or plex://movie/…;
	// PlexShowGUID and PlexSeasonGUID its show's and season's.
	PlexGUID, PlexShowGUID, PlexSeasonGUID string
	PreviouslyShown, Premiere, New, Live   bool
	// Rating is the content rating and RatingSystem its system: "TV-14"
	// under "VCHIP", "PG-13" under "MPAA".
	Rating, RatingSystem string
	LengthMinutes        int
	// OriginallyAvailableAt is an episode's first air date, YYYY-MM-DD.
	OriginallyAvailableAt string
}

// Episode-num systems the profile writes beside XMLTV's own: the dd_progid
// Gracenote ids, the first air date, and the programme's, show's and
// season's plex guids, which PMS's reader ignores (§2.3) and cluster-plex's
// builder keys items by.
const (
	systemXMLTVNS         = "xmltv_ns"
	systemOnscreen        = "onscreen"
	systemProgID          = "dd_progid"
	systemOriginalAirDate = "original-air-date"
	systemPlex            = "plex"
	systemPlexShow        = "plex.show"
	systemPlexSeason      = "plex.season"
)

// The classifier categories Inner adds, and the plex guid forms that say
// a programme's kind.
const (
	categoryMovie   = "Movie"
	categorySeries  = "Series"
	plexMovieGUID   = "plex://movie/"
	plexEpisodeGUID = "plex://episode/"
)

// kind is the Listing's kind; "" (or any value but the two) is resolved by
// its numbers.
func (l Listing) kind() Kind {
	if l.Kind == KindMovie || l.Kind == KindEpisode {
		return l.Kind
	}
	if l.Season > 0 || l.Episode > 0 {
		return KindEpisode
	}
	return KindMovie
}

// Inner is the programme's inner XML in XMLTV's element order (dtdOrder),
// every text and attribute escaped: title, sub-title, desc, credits
// (director, actor with its role, writer, producer, in the DTD's order),
// date, the categories (the source's, then Movie or Series unless the
// source already says it), length in minutes, the poster as the one icon,
// the episode numbers, the flags, the rating and the three images.
//
// An episode's numbers are xmltv_ns ("1.10.", ".11." without a season)
// and onscreen ("S02E11", "E12"), written only when its episode is known
// (PMS reads a season alone as a movie, §2.3), and with them its first air
// date as original-air-date (beside xmltv_ns PMS keys the episode by its
// numbers and takes the date as its oad, the rig's 2026-11-10 probe). A
// movie writes none of the three, which would file it as an episode. An
// episode without numbers or a ProgID writes no episode number: the
// Enricher gives it the dd_progid every source's gets, so PMS files it
// alike whatever the source.
func (l Listing) Inner() []byte {
	var b bytes.Buffer
	kind := l.kind()
	textElem(&b, "title", l.Title)
	if l.SubTitle != "" {
		textElem(&b, "sub-title", l.SubTitle)
	}
	if l.Desc != "" {
		textElem(&b, "desc", l.Desc)
	}
	if len(l.Actors)+len(l.Directors)+len(l.Writers)+len(l.Producers) > 0 {
		b.WriteString("<credits>")
		for _, d := range l.Directors {
			textElem(&b, "director", d)
		}
		for _, a := range l.Actors {
			if a.Role != "" {
				textElem(&b, "actor", a.Name, "role", a.Role)
			} else {
				textElem(&b, "actor", a.Name)
			}
		}
		for _, w := range l.Writers {
			textElem(&b, "writer", w)
		}
		for _, p := range l.Producers {
			textElem(&b, "producer", p)
		}
		b.WriteString("</credits>")
	}
	if l.Date != "" {
		textElem(&b, "date", l.Date)
	}
	classifier := categorySeries
	if kind == KindMovie {
		classifier = categoryMovie
	}
	said := false
	for _, c := range l.Categories {
		textElem(&b, "category", c)
		said = said || strings.EqualFold(c, classifier)
	}
	if !said {
		textElem(&b, "category", classifier)
	}
	if l.LengthMinutes > 0 {
		textElem(&b, "length", strconv.Itoa(l.LengthMinutes), "units", "minutes")
	}
	if l.Poster != "" {
		emptyElem(&b, "icon", "src", l.Poster)
	}
	numbered := kind == KindEpisode && l.Episode > 0
	if numbered {
		textElem(&b, "episode-num", xmltvNS(l.Season, l.Episode), "system", systemXMLTVNS)
		textElem(&b, "episode-num", onscreen(l.Season, l.Episode), "system", systemOnscreen)
	}
	if l.ProgID != "" {
		textElem(&b, "episode-num", l.ProgID, "system", systemProgID)
	}
	if numbered && l.OriginallyAvailableAt != "" {
		textElem(&b, "episode-num", l.OriginallyAvailableAt, "system", systemOriginalAirDate)
	}
	for _, g := range [][2]string{{systemPlex, l.PlexGUID}, {systemPlexShow, l.PlexShowGUID}, {systemPlexSeason, l.PlexSeasonGUID}} {
		if g[1] != "" {
			textElem(&b, "episode-num", g[1], "system", g[0])
		}
	}
	for _, f := range []struct {
		name string
		set  bool
	}{{"previously-shown", l.PreviouslyShown}, {"premiere", l.Premiere}, {"new", l.New}, {"live", l.Live}} {
		if f.set {
			emptyElem(&b, f.name)
		}
	}
	if l.Rating != "" {
		b.WriteString("<rating")
		if l.RatingSystem != "" {
			writeAttr(&b, "system", l.RatingSystem)
		}
		b.WriteString(">")
		textElem(&b, "value", l.Rating)
		b.WriteString("</rating>")
	}
	for _, im := range []struct{ kind, orient, url string }{
		{"poster", "P", l.Poster},
		{"backdrop", "L", l.Backdrop},
		{"still", "L", l.Still},
	} {
		if im.url != "" {
			textElem(&b, "image", im.url, "type", im.kind, "size", "3", "orient", im.orient)
		}
	}
	return b.Bytes()
}

// textElem writes <name attrs...>text</name>; attrs are name, value pairs.
func textElem(b *bytes.Buffer, name, text string, attrs ...string) {
	b.WriteString("<" + name)
	for i := 0; i+1 < len(attrs); i += 2 {
		writeAttr(b, attrs[i], attrs[i+1])
	}
	b.WriteString(">")
	escape(b, text)
	b.WriteString("</" + name + ">")
}

// emptyElem writes <name attrs.../>.
func emptyElem(b *bytes.Buffer, name string, attrs ...string) {
	b.WriteString("<" + name)
	for i := 0; i+1 < len(attrs); i += 2 {
		writeAttr(b, attrs[i], attrs[i+1])
	}
	b.WriteString("/>")
}

func writeAttr(b *bytes.Buffer, name, value string) {
	b.WriteString(" " + name + `="`)
	escape(b, value)
	b.WriteString(`"`)
}

// Listing reads the programme back into the unified model: the inverse of
// Listing.Inner for a built guide's programme, and a best reading of any
// source's (an element the profile does not carry is passed over). The
// classifier category Inner adds (the last Movie or Series) becomes the
// kind; without one, a plex guid or the numbers say it. ok is false when
// the inner XML does not parse.
func (p Programme) Listing() (Listing, bool) {
	elems, ok := splitElements(p.Inner)
	if !ok {
		return Listing{}, false
	}
	var (
		l           Listing
		haveNS      bool
		posterImage bool
	)
	for _, el := range elems {
		switch el.name {
		case "title":
			if l.Title == "" {
				l.Title = el.text
			}
		case "sub-title":
			if l.SubTitle == "" {
				l.SubTitle = el.text
			}
		case "desc":
			if l.Desc == "" {
				l.Desc = el.text
			}
		case "credits":
			var c struct {
				Directors []string `xml:"director"`
				Actors    []struct {
					Role string `xml:"role,attr"`
					Name string `xml:",chardata"`
				} `xml:"actor"`
				Writers   []string `xml:"writer"`
				Producers []string `xml:"producer"`
			}
			if err := decodeElement(el.raw, &c); err != nil {
				return Listing{}, false
			}
			for _, a := range c.Actors {
				if name := strings.TrimSpace(a.Name); name != "" {
					l.Actors = append(l.Actors, Credit{Name: name, Role: strings.TrimSpace(a.Role)})
				}
			}
			l.Directors = appendNames(l.Directors, c.Directors)
			l.Writers = appendNames(l.Writers, c.Writers)
			l.Producers = appendNames(l.Producers, c.Producers)
		case "date":
			if l.Date == "" {
				l.Date = el.text
			}
		case "category":
			if el.text != "" {
				l.Categories = append(l.Categories, el.text)
			}
		case "length":
			if l.LengthMinutes == 0 {
				l.LengthMinutes = lengthMinutes(el.attr("units"), el.text)
			}
		case "icon":
			if l.Poster == "" {
				l.Poster = strings.TrimSpace(el.attr("src"))
			}
		case "episode-num":
			switch el.attr("system") {
			case systemXMLTVNS:
				if s, e, ok := parseXMLTVNS(el.text); ok && !haveNS {
					l.Season, l.Episode, haveNS = s, e, true
				}
			case "", systemOnscreen:
				if s, e, ok := episodeNumbers(el.text); ok && !haveNS && l.Episode == 0 {
					l.Season, l.Episode = s, e
				}
			case systemProgID:
				setOnce(&l.ProgID, el.text)
			case systemOriginalAirDate:
				setOnce(&l.OriginallyAvailableAt, el.text)
			case systemPlex:
				setOnce(&l.PlexGUID, el.text)
			case systemPlexShow:
				setOnce(&l.PlexShowGUID, el.text)
			case systemPlexSeason:
				setOnce(&l.PlexSeasonGUID, el.text)
			}
		case "previously-shown":
			l.PreviouslyShown = true
		case "premiere":
			l.Premiere = true
		case "new":
			l.New = true
		case "live":
			l.Live = true
		case "rating":
			if l.Rating != "" {
				continue
			}
			var r struct {
				Value string `xml:"value"`
			}
			if err := decodeElement(el.raw, &r); err != nil {
				return Listing{}, false
			}
			if v := strings.TrimSpace(r.Value); v != "" {
				l.Rating, l.RatingSystem = v, strings.TrimSpace(el.attr("system"))
			}
		case "image":
			switch el.attr("type") {
			case "poster":
				if !posterImage && el.text != "" {
					l.Poster, posterImage = el.text, true
				}
			case "backdrop":
				setOnce(&l.Backdrop, el.text)
			case "still":
				setOnce(&l.Still, el.text)
			}
		}
	}
	l.Kind, l.Categories = classify(l.Categories)
	if l.Kind == "" {
		switch {
		case strings.HasPrefix(l.PlexGUID, plexMovieGUID):
			l.Kind = KindMovie
		case strings.HasPrefix(l.PlexGUID, plexEpisodeGUID), l.Episode > 0:
			l.Kind = KindEpisode
		}
	}
	return l, true
}

// classify takes the classifier Inner adds after the source's categories
// -- the last Movie or Series -- out of categories as the kind; "" when
// none is there. The rest keep their order; none left is nil.
func classify(categories []string) (Kind, []string) {
	for i := len(categories) - 1; i >= 0; i-- {
		var kind Kind
		switch {
		case strings.EqualFold(categories[i], categoryMovie):
			kind = KindMovie
		case strings.EqualFold(categories[i], categorySeries):
			kind = KindEpisode
		default:
			continue
		}
		rest := append(append([]string(nil), categories[:i]...), categories[i+1:]...)
		if len(rest) == 0 {
			rest = nil
		}
		return kind, rest
	}
	return "", categories
}

// parseXMLTVNS reads xmltv_ns "season.episode.part", counted from zero,
// each part's "/total" dropped: "1.10." is S02E11, ".4." E05. ok is false
// when it names no episode.
func parseXMLTVNS(value string) (season, episode int, ok bool) {
	parts := strings.Split(value, ".")
	if len(parts) < 2 {
		return 0, 0, false
	}
	n := func(s string) (int, bool) {
		s, _, _ = strings.Cut(strings.TrimSpace(s), "/")
		v, err := strconv.Atoi(strings.TrimSpace(s))
		return v, err == nil && v >= 0
	}
	e, eok := n(parts[1])
	if !eok {
		return 0, 0, false
	}
	if s, sok := n(parts[0]); sok {
		season = s + 1
	}
	return season, e + 1, true
}

// lengthMinutes is an XMLTV <length> in minutes; units default to minutes.
func lengthMinutes(units, text string) int {
	v, err := strconv.Atoi(strings.TrimSpace(text))
	if err != nil || v <= 0 {
		return 0
	}
	switch strings.ToLower(strings.TrimSpace(units)) {
	case "hours":
		return v * 60
	case "seconds":
		return v / 60
	}
	return v
}

// decodeElement decodes one element's raw XML into v, as leniently as the
// guide is parsed.
func decodeElement(raw []byte, v any) error {
	d := xml.NewDecoder(bytes.NewReader(raw))
	d.Strict = false
	return d.Decode(v)
}

func appendNames(dst, names []string) []string {
	for _, n := range names {
		if n = strings.TrimSpace(n); n != "" {
			dst = append(dst, n)
		}
	}
	return dst
}

func setOnce(dst *string, v string) {
	if *dst == "" {
		*dst = strings.TrimSpace(v)
	}
}
