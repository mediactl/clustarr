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

package xmltv_test

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/iptv/xmltv"
)

var (
	t0     = time.Date(2026, 10, 27, 19, 0, 0, 0, time.UTC)
	sports = xmltv.LineupChannel{Number: "1286", Name: "ESPN", Group: "Sports"}
	plain  = xmltv.LineupChannel{Number: "1073", Name: "AMC", Group: "Entertainment"}
)

// tags is the order of element names in inner.
func tags(inner []byte) []string {
	var out []string
	for _, m := range regexp.MustCompile(`<([a-z-]+)[ />]`).FindAllSubmatch(inner, -1) {
		out = append(out, string(m[1]))
	}
	return out
}

// The provider's live game (tvload.win's own words for Monday Night
// Football): the title loses its Live prefix, the matchup becomes the
// sub-title, the categories put it under Sports as a live event of its
// sport, it is flagged live and new, and an SP programme id keeps it apart
// from every other channel's "NFL Football" -- all in XMLTV's element order.
func TestALiveGameIsASportsEventWithItsMatchup(t *testing.T) {
	inner := []byte(`<title>Live: NFL Football</title><desc>The Philadelphia Eagles host the Dallas Cowboys in NFL regular-season play at Lincoln Financial Field.</desc>`)
	out := string((&xmltv.Enricher{}).Enrich(inner, sports, t0, t0.Add(3*time.Hour)))
	assert.Contains(t, out, `<title>NFL Football</title>`)
	assert.Contains(t, out, `<sub-title lang="en">Dallas Cowboys at Philadelphia Eagles</sub-title>`)
	assert.Contains(t, out, `<category lang="en">Sports</category>`)
	assert.Contains(t, out, `<category lang="en">Sports event</category>`)
	assert.Contains(t, out, `<category lang="en">Football</category>`)
	assert.Regexp(t, `<episode-num system="dd_progid">SP\d{8}\.\d{4}</episode-num>`, out)
	assert.Contains(t, out, `<live/>`)
	assert.Contains(t, out, `<new/>`)
	assert.Equal(t, []string{"title", "sub-title", "desc", "category", "category", "category", "episode-num", "new", "live"}, tags([]byte(out)))
}

// Two channels airing "NFL Football" at the same hour are two games; the
// same game on the same channel is the same id at every build.
func TestEventIDsAreUniquePerChannelAndStable(t *testing.T) {
	inner := []byte(`<title>Live: NFL Football</title><desc>Teams play.</desc>`)
	e := &xmltv.Enricher{}
	a := string(e.Enrich(inner, sports, t0, t0.Add(time.Hour)))
	b := string(e.Enrich(inner, xmltv.LineupChannel{Number: "1291", Group: "Sports"}, t0, t0.Add(time.Hour)))
	id := regexp.MustCompile(`SP\d{8}\.\d{4}`)
	assert.NotEqual(t, id.FindString(a), id.FindString(b))
	assert.Equal(t, a, string(e.Enrich(inner, sports, t0, t0.Add(time.Hour))))
}

// A studio show on a sports channel is Sports but no event: no live flag,
// no matchup, an EP id that repeats carry too.
func TestASportsChannelsStudioShowIsNotAnEvent(t *testing.T) {
	inner := []byte(`<title>SportsCenter</title><desc>The day's news, highlights and analysis.</desc>`)
	out := string((&xmltv.Enricher{}).Enrich(inner, sports, t0, t0.Add(time.Hour)))
	assert.Contains(t, out, `<category lang="en">Sports</category>`)
	assert.NotContains(t, out, "Sports event")
	assert.NotContains(t, out, "<live/>")
	assert.NotContains(t, out, "<sub-title")
	assert.Regexp(t, `EP\d{8}\.\d{4}`, out)
	repeat := string((&xmltv.Enricher{}).Enrich(inner, sports, t0.Add(24*time.Hour), t0.Add(25*time.Hour)))
	assert.Equal(t, out, repeat, "the same episode is the same id a day later")
}

// The provider titles a live studio show "Live:" too (tvload.win: "Live:
// SportsCenter", "Live: ACC PM"). It is flagged live and new and filed under
// Sports, but with no matchup or sport named it is no sports event: no
// "Sports event" category, an EP id rather than an SP one, so Plex's
// Upcoming Sports hub lists games, not desks.
func TestALiveStudioShowIsLiveButNoSportsEvent(t *testing.T) {
	inner := []byte(`<title>Live: SportsCenter</title><desc>The latest news, highlights and inside information from every corner of the sports world.</desc>`)
	out := string((&xmltv.Enricher{}).Enrich(inner, sports, t0, t0.Add(time.Hour)))
	assert.Contains(t, out, `<title>SportsCenter</title>`)
	assert.Contains(t, out, `<live/>`)
	assert.Contains(t, out, `<new/>`)
	assert.Contains(t, out, `<category lang="en">Sports</category>`)
	assert.NotContains(t, out, "Sports event")
	assert.NotContains(t, out, "<sub-title")
	assert.Regexp(t, `EP\d{8}\.\d{4}`, out)
	assert.NotRegexp(t, `SP\d{8}`, out)
}

// A game on a broadcast network is an event when its title names the sport
// and its description a matchup; "Celtics at Heat" reads as a matchup too.
func TestAGameOnAnyChannelIsAnEventWhenItNamesASportAndAMatchup(t *testing.T) {
	inner := []byte(`<title>NBA Basketball</title><desc>Boston Celtics at Miami Heat.</desc>`)
	out := string((&xmltv.Enricher{}).Enrich(inner, plain, t0, t0.Add(3*time.Hour)))
	assert.Contains(t, out, `<sub-title lang="en">Boston Celtics at Miami Heat</sub-title>`)
	assert.Contains(t, out, `<category lang="en">Basketball</category>`)
	assert.Contains(t, out, `<live/>`)
}

// A series the art resolver knows gets its poster and the Series category
// and a stable EP id; a movie gets Movie and an MV id; a programme the
// resolver has nothing for keeps the EP id only.
func TestArtworkAddsAnIconAndTheKind(t *testing.T) {
	art := func(q xmltv.ArtQuery) (xmltv.Art, bool) {
		switch q.Title {
		case "Friends":
			return xmltv.Art{URL: "https://img.example/friends.jpg", Kind: xmltv.ArtSeries}, true
		case "Heat":
			assert.Equal(t, 1995, q.Year)
			return xmltv.Art{URL: "https://img.example/heat.jpg", Kind: xmltv.ArtMovie}, true
		}
		return xmltv.Art{}, false
	}
	e := &xmltv.Enricher{Art: art}
	out := string(e.Enrich([]byte(`<title lang="en">Friends</title><desc>The One Where Ross Finds Out</desc>`), plain, t0, t0.Add(30*time.Minute)))
	assert.Contains(t, out, `<icon src="https://img.example/friends.jpg"/>`)
	assert.Contains(t, out, `<category lang="en">Series</category>`)
	assert.Regexp(t, `EP\d{8}\.\d{4}`, out)
	assert.Equal(t, []string{"title", "desc", "category", "icon", "episode-num"}, tags([]byte(out)))

	out = string(e.Enrich([]byte(`<title>Heat (1995)</title><desc>A crew of thieves.</desc>`), plain, t0, t0.Add(3*time.Hour)))
	assert.Contains(t, out, `<category lang="en">Movie</category>`)
	assert.Regexp(t, `MV\d{8}\.\d{4}`, out)
	assert.Contains(t, out, `<title>Heat (1995)</title>`, "the title is kept as the source wrote it")

	out = string(e.Enrich([]byte(`<title>Paid Programming</title>`), plain, t0, t0.Add(30*time.Minute)))
	assert.NotContains(t, out, "<icon")
	assert.Regexp(t, `EP\d{8}\.\d{4}`, out)
}

// A programme its source already gives an image and a kind is not looked
// up (iptv-epg.org's guide); one with an image but no kind still is, for
// its Movie or Series category.
func TestADescribedProgrammeIsNotLookedUp(t *testing.T) {
	var asked []string
	e := &xmltv.Enricher{Art: func(q xmltv.ArtQuery) (xmltv.Art, bool) {
		asked = append(asked, q.Title)
		return xmltv.Art{URL: "y.png", Kind: xmltv.ArtSeries}, true
	}}
	out := string(e.Enrich([]byte(`<title lang="en">Storage Wars</title><category lang="en">Reality</category><category lang="en">Series</category><icon src="https://iptv-epg.org/images/x"/>`), plain, t0, t0.Add(30*time.Minute)))
	assert.Empty(t, asked)
	assert.Equal(t, 1, strings.Count(out, "<icon"), out)
	assert.Equal(t, 2, strings.Count(out, "<category"), out)

	_ = e.Enrich([]byte(`<title>Friends</title><icon src="https://iptv-epg.org/images/y"/>`), plain, t0, t0.Add(30*time.Minute))
	assert.Equal(t, []string{"Friends"}, asked)
}

// What the source says stands: a category, an episode number and an icon
// already there are never doubled, and a movie channel's unknown film keeps
// Plex's movie fallback by getting no episode number.
func TestExistingElementsAreKeptAndNotDoubled(t *testing.T) {
	inner := []byte(`<title>NFL Football</title><sub-title>Eagles vs. Cowboys</sub-title><category>Sports</category><episode-num system="xmltv_ns">0.3.</episode-num><icon src="x.png"/>`)
	e := &xmltv.Enricher{Art: func(xmltv.ArtQuery) (xmltv.Art, bool) { return xmltv.Art{URL: "y.png", Kind: xmltv.ArtSeries}, true }}
	out := string(e.Enrich(inner, sports, t0, t0.Add(3*time.Hour)))
	assert.Equal(t, 1, strings.Count(out, "<category"), out)
	assert.Equal(t, 1, strings.Count(out, "<episode-num"), out)
	assert.Equal(t, 1, strings.Count(out, "<icon"), out)
	assert.Equal(t, 1, strings.Count(out, "<sub-title"), out)

	movies := xmltv.LineupChannel{Number: "1211", Group: "Movies"}
	out = string((&xmltv.Enricher{}).Enrich([]byte(`<title>An Unknown Film</title>`), movies, t0, t0.Add(2*time.Hour)))
	assert.NotContains(t, out, "<episode-num")
}

// News and kids channels are filed by their group; a dummy guide's or a
// malformed programme's inner XML comes back untouched.
func TestGroupsAndPassThrough(t *testing.T) {
	e := &xmltv.Enricher{}
	out := string(e.Enrich([]byte(`<title>CNN Newsroom</title>`), xmltv.LineupChannel{Number: "1030", Group: "News"}, t0, t0.Add(time.Hour)))
	assert.Contains(t, out, `<category lang="en">News</category>`)
	out = string(e.Enrich([]byte(`<title>Bluey</title>`), xmltv.LineupChannel{Number: "1264", Group: "Kids"}, t0, t0.Add(time.Hour)))
	assert.Contains(t, out, `<category lang="en">Children</category>`)
	broken := []byte(`<title>Unclosed`)
	assert.Equal(t, broken, e.Enrich(broken, plain, t0, t0.Add(time.Hour)))
}

// BuildGuideWith passes every mapped programme through the enricher, and
// BuildGuide is the same guide with nothing added.
func TestBuildGuideWithEnrichesProgrammes(t *testing.T) {
	g := xmltv.Guide{Programmes: map[string][]xmltv.Programme{
		"espn.us": {{Channel: "espn.us", Start: t0, Stop: t0.Add(time.Hour), Inner: []byte(`<title>Live: NFL Football</title><desc>The Eagles host the Cowboys.</desc>`)}},
	}}
	lineup := []xmltv.LineupChannel{{Number: "1286", Name: "ESPN", Group: "Sports", Map: &xmltv.Mapping{Guide: "main", ChannelID: "espn.us"}}}
	var plainOut, enriched strings.Builder
	require.NoError(t, xmltv.BuildGuide(&plainOut, lineup, map[string]xmltv.Guide{"main": g}, t0, 1))
	require.NoError(t, xmltv.BuildGuideWith(&enriched, lineup, map[string]xmltv.Guide{"main": g}, t0, 1, &xmltv.Enricher{}))
	assert.Contains(t, plainOut.String(), "Live: NFL Football")
	assert.Contains(t, enriched.String(), `<sub-title lang="en">Cowboys at Eagles</sub-title>`)
	assert.Contains(t, enriched.String(), "<live/>")
}
