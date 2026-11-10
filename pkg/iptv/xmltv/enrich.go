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
	"fmt"
	"hash/fnv"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ArtKind is what an artwork lookup found a programme to be.
type ArtKind string

// Art kinds.
const (
	ArtMovie  ArtKind = "movie"
	ArtSeries ArtKind = "series"
)

// Art is a programme's poster and what the programme is.
type Art struct {
	URL  string
	Kind ArtKind
}

// ArtQuery is what an ArtResolver gets: the programme's title (a "Live:"
// prefix stripped), the year its title carried, its channel and length.
type ArtQuery struct {
	Title    string
	Year     int
	Channel  LineupChannel
	Duration time.Duration
}

// ArtResolver answers a programme's artwork, from a cache: a guide build
// never waits on a lookup. ok is false when it has none.
type ArtResolver func(ArtQuery) (Art, bool)

// Enricher adds to a programme what Plex's XMLTV parser needs to classify
// it (owner, 2026-10-27; the provider's guide carries a title and a
// description and nothing else):
//
//   - a sports channel's programmes are category Sports; a live event (a
//     "Live:" title, or a matchup in its title or description) is also
//     "Sports event" and its sport, carries <live/> and <new/>, and gets
//     the matchup as its sub-title;
//   - every programme without an episode number gets a Gracenote-style one
//     (dd_progid: SP for an event, unique per channel and start, so ten
//     channels' "NFL Football" stay ten games; EP for anything else, the
//     same for the same title, sub-title and description, so a repeat is
//     the same episode, unless the built guide airs it more often than a
//     repeat or in its slot day after day (repeats), when each airing is an
//     episode of its own under the one show; MV for a movie the art
//     resolver knows, unless the title airs more often than a film), which
//     keeps a one-off broadcast out of Plex's Movies;
//   - a news channel's programmes are News, a kids channel's Children;
//   - a title the art resolver knows gets its poster as <icon> and the
//     category Movie or Series;
//   - an episode iptv-epg.org numbers in its own way -- "S26 E32" in an
//     <episode-num> naming no system, and "S26 E32 — Justice for Nikki" as
//     the description's first line -- gets XMLTV's own fields: the episode
//     title as <sub-title>, the numbers as xmltv_ns and onscreen, and the
//     description without that line (talos, 2026-11-10: 2,817 of 9,950
//     programmes on the off-Plex channels carried their episode only so).
//
// What the source already says is kept: an element present is never
// duplicated or removed, and the result keeps XMLTV's element order.
type Enricher struct {
	// Art resolves a programme's artwork; nil adds none.
	Art ArtResolver
}

// programmeFacts is one programme as the Enricher and repeats see it.
type programmeFacts struct {
	title, subtitle, desc    string
	categories               map[string]bool
	hasEpisodeNum, hasIcon   bool
	hasLive, hasNew, hasPrev bool
	// The episode's numbers as the source gives them (episodeNumbers), 0
	// for none, and what the Enricher rewrites to say them in XMLTV's own
	// fields: the sub-title lifted from the description, the description
	// without its numbered first line, the episode-num naming no system.
	season, episode    int
	hasXMLTVNs         bool
	liftedSubtitle     bool
	descNumbered       bool
	unnamedEpisodeNum  bool
	onscreenEpisodeNum bool
}

// element is one child of <programme>, kept verbatim.
type element struct {
	name  string
	attrs []xml.Attr
	text  string
	raw   []byte
}

// attr is the element's attribute name, "" when it has none.
func (el element) attr(name string) string { return attr(xml.StartElement{Attr: el.attrs}, name) }

// dtdOrder is XMLTV's order of a programme's children; live is Plex's and
// xTeVe's extension, placed with new.
var dtdOrder = []string{
	"title", "sub-title", "desc", "credits", "date", "category", "keyword", "language",
	"orig-language", "length", "icon", "url", "country", "episode-num", "video", "audio",
	"previously-shown", "premiere", "last-chance", "new", "live", "subtitles", "rating",
	"star-rating", "review", "image",
}

// Enrich returns inner, a programme's inner XML on channel ch from start to
// stop, with the additions above. Inner that does not parse comes back as
// it is.
func (e *Enricher) Enrich(inner []byte, ch LineupChannel, start, stop time.Time) []byte {
	return e.enrich(inner, ch, start, stop, nil)
}

// enrich is Enrich with the built guide's repeats; nil keeps every listing
// one episode and every resolved film one movie.
func (e *Enricher) enrich(inner []byte, ch LineupChannel, start, stop time.Time, rep *repeats) []byte {
	elems, ok := splitElements(inner)
	if !ok || len(elems) == 0 {
		return inner
	}
	f := facts(elems)
	title, live := stripLive(f.title)
	title, year := stripYear(title)
	class := channelClass(ch.Group)
	sport := sportOf(title + " " + f.desc)
	matchup, ok := matchupOf(title, f.desc)
	// A live broadcast is one the provider titles "Live:" or one with a
	// matchup; a sports event is a live broadcast that names a matchup or a
	// sport, so a live studio show (the provider's "Live: SportsCenter") is
	// flagged live without landing in Plex's sports-event hubs.
	event := (class == classSports || sport != "") && (live || ok)
	sportsEvent := event && (ok || sport != "")

	// A programme whose source already gives its image and says it is a
	// movie or a series is not looked up: iptv-epg.org's guide carries both
	// for most of its 1.1 million programmes, and every lookup queued is a
	// gateway search and, once answered, a guide rebuild PMS reloads.
	described := f.hasIcon && (f.categories["movie"] || f.categories["series"])
	var art Art
	var haveArt bool
	if e != nil && e.Art != nil && !described && !event && class != classSports && class != classNews && lookupWorthy(title) {
		art, haveArt = e.Art(ArtQuery{Title: title, Year: year, Channel: ch, Duration: stop.Sub(start)})
		if haveArt && art.Kind == ArtMovie && rep.tooOftenForAFilm(title, year) {
			// A block that airs all week named like a film: neither its
			// poster nor its kind is this programme's.
			art, haveArt = Art{}, false
		}
	}

	add := map[string][][]byte{}
	addElem := func(name string, b []byte) { add[name] = append(add[name], b) }
	addText := func(name, text string) {
		var b bytes.Buffer
		b.WriteString("<" + name + ` lang="en">`)
		escape(&b, text)
		b.WriteString("</" + name + ">")
		addElem(name, b.Bytes())
	}
	addCategory := func(c string) {
		if !f.categories[strings.ToLower(c)] {
			f.categories[strings.ToLower(c)] = true
			addText("category", c)
		}
	}

	changed := false
	if live && title != f.title {
		// The title loses its "Live:" prefix; the flag says it instead.
		elems = replaceText(elems, "title", title)
		changed = true
	}
	if f.descNumbered {
		// The description loses its numbered first line, which the
		// sub-title and the episode numbers say instead.
		if f.desc == "" {
			elems = dropElements(elems, "desc")
		} else {
			elems = replaceText(elems, "desc", f.desc)
		}
		changed = true
	}
	if f.liftedSubtitle {
		addText("sub-title", f.subtitle)
	}
	if f.season > 0 || f.episode > 0 {
		if f.unnamedEpisodeNum {
			elems = dropElements(elems, "episode-num", func(el element) bool { return el.attr("system") == "" })
			changed = true
		}
		if !f.hasXMLTVNs {
			addElem("episode-num", []byte(`<episode-num system="xmltv_ns">`+xmltvNS(f.season, f.episode)+`</episode-num>`))
		}
		if !f.onscreenEpisodeNum {
			addElem("episode-num", []byte(`<episode-num system="onscreen">`+onscreen(f.season, f.episode)+`</episode-num>`))
		}
	}
	if event {
		addCategory("Sports")
		if sportsEvent {
			if f.subtitle == "" && matchup != "" {
				addText("sub-title", matchup)
			}
			addCategory("Sports event")
			if sport != "" {
				addCategory(sport)
			}
		}
		if !f.hasLive {
			addElem("live", []byte("<live/>"))
		}
		if !f.hasNew && !f.hasPrev {
			addElem("new", []byte("<new/>"))
		}
	}
	switch class {
	case classSports:
		addCategory("Sports")
	case classNews:
		addCategory("News")
	case classKids:
		addCategory("Children")
	}
	if haveArt {
		switch art.Kind {
		case ArtMovie:
			addCategory("Movie")
		case ArtSeries:
			addCategory("Series")
		}
		if !f.hasIcon && art.URL != "" {
			var b bytes.Buffer
			b.WriteString(`<icon src="`)
			escape(&b, art.URL)
			b.WriteString(`"/>`)
			addElem("icon", b.Bytes())
		}
	}
	if !f.hasEpisodeNum {
		switch {
		case sportsEvent:
			addElem("episode-num", progID("SP", ch.Number, start.UTC().Format(time.RFC3339)))
		case haveArt && art.Kind == ArtMovie:
			addElem("episode-num", progID("MV", rep.titleNorm(title), fmt.Sprint(year)))
		case class == classMovies && !haveArt:
			// Plex's movie fallback is right for a movie channel's unknown film.
		case rep.oneEpisode(rep.episodeKey(title, f.subtitle, f.desc)):
			addElem("episode-num", progID("EP", rep.titleNorm(title), f.subtitle, f.desc))
		default:
			// An airing of its own, an episode of the listing's show.
			series := progSum(rep.titleNorm(title), f.subtitle, f.desc)
			episode := progSum(ch.Number, start.UTC().Format(time.RFC3339))
			addElem("episode-num", formatProgID("EP", series, episode/100000000))
		}
	}
	if len(add) == 0 && !changed {
		return inner
	}

	byName := map[string][]element{}
	for _, el := range elems {
		byName[el.name] = append(byName[el.name], el)
	}
	var out bytes.Buffer
	for _, name := range dtdOrder {
		for _, el := range byName[name] {
			out.Write(el.raw)
		}
		for _, b := range add[name] {
			out.Write(b)
		}
		delete(byName, name)
	}
	for _, el := range elems { // anything XMLTV does not name, in its order
		if _, left := byName[el.name]; left {
			out.Write(el.raw)
		}
	}
	return out.Bytes()
}

// splitElements is inner's top-level elements, each verbatim with its
// name, attributes and text. ok is false when inner is not well-formed.
func splitElements(inner []byte) ([]element, bool) {
	d := xml.NewDecoder(bytes.NewReader(inner))
	d.Strict = false
	var (
		out   []element
		cur   *element
		start int64
		depth int
		text  strings.Builder
	)
	for {
		before := d.InputOffset()
		tok, err := d.Token()
		if err != nil {
			if cur != nil {
				return nil, false
			}
			return out, true
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if depth == 0 {
				cur, start = &element{name: t.Name.Local, attrs: t.Attr}, before
				text.Reset()
			}
			depth++
		case xml.CharData:
			if depth == 1 {
				text.Write(t)
			}
		case xml.EndElement:
			depth--
			if depth == 0 && cur != nil {
				cur.text = strings.TrimSpace(text.String())
				cur.raw = bytes.Clone(inner[start:d.InputOffset()])
				out = append(out, *cur)
				cur = nil
			}
		}
	}
}

func facts(elems []element) programmeFacts {
	f := programmeFacts{categories: map[string]bool{}}
	for _, el := range elems {
		switch el.name {
		case "title":
			if f.title == "" {
				f.title = el.text
			}
		case "sub-title":
			if f.subtitle == "" {
				f.subtitle = el.text
			}
		case "desc":
			if f.desc == "" {
				f.desc = el.text
			}
		case "category":
			f.categories[strings.ToLower(el.text)] = true
		case "episode-num":
			f.hasEpisodeNum = true
			switch el.attr("system") {
			case "xmltv_ns":
				f.hasXMLTVNs = true
			case "", "onscreen":
				// XMLTV's DTD reads an episode-num naming no system as
				// onscreen; iptv-epg.org writes "S26 E32" so.
				if s, e, ok := episodeNumbers(el.text); ok {
					if f.season == 0 && f.episode == 0 {
						f.season, f.episode = s, e
					}
					if el.attr("system") == "" {
						f.unnamedEpisodeNum = true
					} else {
						f.onscreenEpisodeNum = true
					}
				}
			}
		case "icon":
			f.hasIcon = true
		case "live":
			f.hasLive = true
		case "new":
			f.hasNew = true
		case "previously-shown":
			f.hasPrev = true
		}
	}
	// "S26 E32 — Justice for Nikki", or "S2026 E41" alone, on the
	// description's first line.
	first, rest, _ := strings.Cut(f.desc, "\n")
	if m := numberedLineRE.FindStringSubmatch(strings.TrimSpace(first)); m != nil {
		s, _ := strconv.Atoi(m[1])
		e, _ := strconv.Atoi(m[2])
		if f.season == 0 && f.episode == 0 {
			f.season, f.episode = s, e
		}
		if f.subtitle == "" && m[3] != "" {
			f.subtitle, f.liftedSubtitle = strings.TrimSpace(m[3]), true
		}
		f.desc, f.descNumbered = strings.TrimSpace(rest), true
	}
	return f
}

var (
	// episodeNumRE is an onscreen episode number: "S26 E32", "S01E39",
	// "s1 e2", "E12".
	episodeNumRE = regexp.MustCompile(`(?i)^\s*(?:S(\d{1,4})\s*)?E(\d{1,7})\s*$`)
	// numberedLineRE is iptv-epg.org's first line of an episode's
	// description: "S26 E32 — Justice for Nikki", or the numbers alone.
	numberedLineRE = regexp.MustCompile(`^S(\d{1,4}) E(\d{1,7})(?: — (\S.*))?$`)
)

// episodeNumbers reads an onscreen episode number; season is 0 when it
// names none.
func episodeNumbers(text string) (season, episode int, ok bool) {
	m := episodeNumRE.FindStringSubmatch(text)
	if m == nil {
		return 0, 0, false
	}
	season, _ = strconv.Atoi(m[1])
	episode, _ = strconv.Atoi(m[2])
	return season, episode, episode > 0
}

// xmltvNS is season and episode as xmltv_ns, counted from zero: S26 E32 is
// "25.31."; an unknown season is left empty.
func xmltvNS(season, episode int) string {
	var b strings.Builder
	if season > 0 {
		b.WriteString(strconv.Itoa(season - 1))
	}
	b.WriteString(".")
	if episode > 0 {
		b.WriteString(strconv.Itoa(episode - 1))
	}
	b.WriteString(".")
	return b.String()
}

// onscreen is season and episode as Plex shows them: "S26E32", "E12".
func onscreen(season, episode int) string {
	if season > 0 {
		return fmt.Sprintf("S%02dE%02d", season, episode)
	}
	return fmt.Sprintf("E%02d", episode)
}

// dropElements is elems without the elements named name that every keep
// test (none: all of them) picks.
func dropElements(elems []element, name string, pick ...func(element) bool) []element {
	out := make([]element, 0, len(elems))
	for _, el := range elems {
		drop := el.name == name
		for _, p := range pick {
			drop = drop && p(el)
		}
		if !drop {
			out = append(out, el)
		}
	}
	return out
}

// replaceText rewrites the first element named name, keeping its
// attributes.
func replaceText(elems []element, name, text string) []element {
	out := make([]element, len(elems))
	copy(out, elems)
	for i, el := range out {
		if el.name != name {
			continue
		}
		var b bytes.Buffer
		b.WriteString("<" + name)
		for _, a := range el.attrs {
			b.WriteString(" " + a.Name.Local + `="`)
			escape(&b, a.Value)
			b.WriteString(`"`)
		}
		b.WriteString(">")
		escape(&b, text)
		b.WriteString("</" + name + ">")
		out[i].raw, out[i].text = b.Bytes(), text
		return out
	}
	return out
}

type channelClassKind int

const (
	classOther channelClassKind = iota
	classSports
	classNews
	classKids
	classMovies
)

// channelClass reads an IPTVChannel's group.
func channelClass(group string) channelClassKind {
	g := strings.ToLower(group)
	switch {
	case strings.Contains(g, "sport"):
		return classSports
	case strings.Contains(g, "news"):
		return classNews
	case strings.Contains(g, "kid"), strings.Contains(g, "child"):
		return classKids
	case strings.Contains(g, "movie"), strings.Contains(g, "cinema"), strings.Contains(g, "film"):
		return classMovies
	}
	return classOther
}

var (
	liveRE = regexp.MustCompile(`(?i)^\s*live\s*[:\-–]\s*`)
	yearRE = regexp.MustCompile(`\s*\((19|20)\d\d\)\s*$`)
	// team is a side's name: up to four capitalized words ("Boston Celtics",
	// "St. Louis Cardinals", "Ohio State").
	team    = `([A-Z][\w.'&-]*(?: [A-Z][\w.'&-]*){0,3})`
	hostRE  = regexp.MustCompile(`(?:^|[^\w])(?:The |the )?` + team + ` (host|hosts|visit|visits|take on|takes on|face|faces|meet|meets|play|plays|battle|battles|welcome|welcomes|travel to|travels to) (?:the )?` + team + `(?:[ ,.;]|$)`)
	vsRE    = regexp.MustCompile(`(?:^|[^\w])` + team + ` (vs\.?|v\.?|versus|at|@) ` + team + `(?:[ ,.;:(]|$)`)
	generic = regexp.MustCompile(`(?i)^(paid programming|local programming|programming|to be announced|tba|movie|news|sign[- ]off|off air|no information)$`)
)

// sports maps a league or sport named in a title or description to the
// category Plex files it under.
var sports = []struct {
	re   *regexp.Regexp
	name string
}{
	{regexp.MustCompile(`(?i)\b(NFL|NCAA football|college football|football|super bowl)\b`), "Football"},
	{regexp.MustCompile(`(?i)\b(NBA|WNBA|basketball|final four)\b`), "Basketball"},
	{regexp.MustCompile(`(?i)\b(MLB|baseball|world series)\b`), "Baseball"},
	{regexp.MustCompile(`(?i)\b(NHL|hockey|stanley cup)\b`), "Hockey"},
	{regexp.MustCompile(`(?i)\b(MLS|soccer|premier league|la liga|bundesliga|serie a|ligue 1|champions league|europa league|liga mx|f[úu]tbol|world cup|copa)\b`), "Soccer"},
	{regexp.MustCompile(`(?i)\b(golf|PGA|LPGA|ryder cup|masters tournament)\b`), "Golf"},
	{regexp.MustCompile(`(?i)\b(tennis|ATP|WTA|wimbledon|US open tennis)\b`), "Tennis"},
	{regexp.MustCompile(`(?i)\b(boxing|fight night|title fight)\b`), "Boxing"},
	{regexp.MustCompile(`(?i)\b(UFC|MMA|mixed martial arts|bellator|PFL)\b`), "Mixed martial arts"},
	{regexp.MustCompile(`(?i)\b(NASCAR|IndyCar|Formula 1|Formula One|F1|MotoGP|motorsports?|grand prix|racing)\b`), "Motor racing"},
	{regexp.MustCompile(`(?i)\b(cricket|IPL|test match)\b`), "Cricket"},
	{regexp.MustCompile(`(?i)\b(rugby)\b`), "Rugby"},
	{regexp.MustCompile(`(?i)\b(WWE|AEW|wrestling|smackdown|wrestlemania)\b`), "Wrestling"},
	{regexp.MustCompile(`(?i)\b(volleyball)\b`), "Volleyball"},
	{regexp.MustCompile(`(?i)\b(lacrosse|PLL|NLL)\b`), "Lacrosse"},
	{regexp.MustCompile(`(?i)\b(horse racing|thoroughbred|kentucky derby|breeders' cup)\b`), "Horse racing"},
	{regexp.MustCompile(`(?i)\b(poker)\b`), "Poker"},
	{regexp.MustCompile(`(?i)\b(fishing|bassmaster)\b`), "Fishing"},
	{regexp.MustCompile(`(?i)\b(rodeo|bull riding|PBR)\b`), "Rodeo"},
	{regexp.MustCompile(`(?i)\b(cycling|tour de france)\b`), "Cycling"},
	{regexp.MustCompile(`(?i)\b(swimming|athletics|track and field|marathon|olympics)\b`), "Athletics"},
}

// stripLive takes a "Live:" prefix off a title, reporting whether it had
// one.
func stripLive(title string) (string, bool) {
	if m := liveRE.FindStringIndex(title); m != nil && m[1] < len(title) {
		return strings.TrimSpace(title[m[1]:]), true
	}
	return title, false
}

// stripYear takes a trailing "(2019)" off a title.
func stripYear(title string) (string, int) {
	m := yearRE.FindStringSubmatchIndex(title)
	if m == nil {
		return title, 0
	}
	var y int
	_, _ = fmt.Sscanf(strings.Trim(title[m[0]:m[1]], " ()"), "%d", &y)
	return strings.TrimSpace(title[:m[0]]), y
}

// sportOf names the sport a title or description names, or "".
func sportOf(text string) string {
	for _, s := range sports {
		if s.re.MatchString(text) {
			return s.name
		}
	}
	return ""
}

// matchupOf reads the two sides of a game out of a title or description:
// "the Eagles host the Cowboys" is "Dallas Cowboys at Philadelphia Eagles",
// "Celtics vs. Heat" is "Celtics vs. Heat". ok is false when neither names
// one.
func matchupOf(title, desc string) (string, bool) {
	trim := func(s string) string { return strings.Trim(s, " .,;:") }
	for _, text := range []string{title, desc} {
		if m := hostRE.FindStringSubmatch(text); m != nil {
			a, verb, b := trim(m[1]), m[2], trim(m[3])
			if len(a) < 3 || len(b) < 3 {
				continue
			}
			switch strings.Fields(verb)[0] {
			case "host", "hosts", "welcome", "welcomes":
				return b + " at " + a, true
			case "visit", "visits", "travel", "travels":
				return a + " at " + b, true
			}
			return a + " vs. " + b, true
		}
		if m := vsRE.FindStringSubmatch(text); m != nil {
			a, verb, b := trim(m[1]), m[2], trim(m[3])
			if len(a) < 3 || len(b) < 3 {
				continue
			}
			if verb == "at" || verb == "@" {
				return a + " at " + b, true
			}
			return a + " vs. " + b, true
		}
	}
	return "", false
}

// lookupWorthy reports whether a title is worth an artwork lookup.
func lookupWorthy(title string) bool {
	return len(title) >= 2 && !generic.MatchString(title)
}

// progID is a Gracenote-shaped programme id (dd_progid: two letters, eight
// digits, a dot, four digits) hashed from parts, as an episode-num element.
func progID(prefix string, parts ...string) []byte {
	sum := progSum(parts...)
	return formatProgID(prefix, sum, sum/100000000)
}

// progSum hashes parts for a programme id.
func progSum(parts ...string) uint64 {
	h := fnv.New64a()
	for _, p := range parts {
		_, _ = h.Write([]byte(p))
		_, _ = h.Write([]byte{0})
	}
	return h.Sum64()
}

// formatProgID is the episode-num element of a programme id whose eight
// digits (the show) come from series and four (the episode) from episode.
func formatProgID(prefix string, series, episode uint64) []byte {
	return fmt.Appendf(nil, `<episode-num system="dd_progid">%s%08d.%04d</episode-num>`, prefix, series%100000000, episode%10000)
}
