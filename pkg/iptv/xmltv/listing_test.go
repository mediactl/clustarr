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
	"bytes"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/iptv/xmltv"
)

// fullEpisode is a Listing with every field set, text that needs escaping
// included.
var fullEpisode = xmltv.Listing{
	Kind:     xmltv.KindEpisode,
	Title:    "Grimm & Co.",
	SubTitle: `To Protect "and" Serve <Man>`,
	Desc:     "Hank remembers an arrest he made.\nMonroe gets involved.",
	Actors: []xmltv.Credit{
		{Name: "David Giuntoli", Role: "Nick Burkhardt"},
		{Name: "Russell Hornsby"},
	},
	Directors:             []string{"Norberto Barba"},
	Writers:               []string{"David Greenwalt", "Jim Kouf"},
	Producers:             []string{"Sean Hayes"},
	Date:                  "2013",
	Categories:            []string{"Drama", "Fantasy"},
	Poster:                "https://img.example/show.jpg?w=680&h=1000",
	Backdrop:              "https://img.example/art.jpg",
	Still:                 "https://img.example/still.jpg",
	Season:                2,
	Episode:               11,
	ProgID:                "EP01419237.0034",
	PlexGUID:              "plex://episode/5d9c0e3b46115600200e2e3a",
	PlexShowGUID:          "plex://show/5d9c086c46115600200aa2fe",
	PlexSeasonGUID:        "plex://season/5d9c086c46115600200aa2fe/2",
	PreviouslyShown:       true,
	Premiere:              true,
	New:                   true,
	Live:                  true,
	Rating:                "TV-14",
	RatingSystem:          "VCHIP",
	LengthMinutes:         43,
	OriginallyAvailableAt: "2013-02-12",
}

// inDTDOrder reports whether the top-level element names of inner, read
// as tags reads them (nested names such as actor and value are not
// XMLTV programme children and are passed over), never go back in
// XMLTV's order.
func inDTDOrder(t *testing.T, inner []byte) {
	t.Helper()
	last := -1
	for _, name := range tags(inner) {
		i := slices.Index(xmltv.DTDOrder, name)
		if i < 0 {
			continue
		}
		require.GreaterOrEqual(t, i, last, "%s comes after an element XMLTV places later: %s", name, inner)
		last = i
	}
}

// Every field of a Listing is written in the one profile, in XMLTV's
// element order, escaped, and read back by Programme.Listing as it went in.
func TestListingInnerIsInDTDOrderAndRoundTrips(t *testing.T) {
	inner := fullEpisode.Inner()
	assert.Equal(t, []string{
		"title", "sub-title", "desc",
		"credits", "director", "actor", "actor", "writer", "writer", "producer",
		"date", "category", "category", "category", "length", "icon",
		"episode-num", "episode-num", "episode-num", "episode-num", "episode-num", "episode-num", "episode-num",
		"previously-shown", "premiere", "new", "live", "rating", "value",
		"image", "image", "image",
	}, tags(inner))
	inDTDOrder(t, inner)
	s := string(inner)
	for _, want := range []string{
		`<title>Grimm &amp; Co.</title>`,
		`<sub-title>To Protect &#34;and&#34; Serve &lt;Man&gt;</sub-title>`,
		`<actor role="Nick Burkhardt">David Giuntoli</actor><actor>Russell Hornsby</actor>`,
		`<category>Drama</category><category>Fantasy</category><category>Series</category>`,
		`<length units="minutes">43</length>`,
		`<icon src="https://img.example/show.jpg?w=680&amp;h=1000"/>`,
		`<episode-num system="xmltv_ns">1.10.</episode-num>`,
		`<episode-num system="onscreen">S02E11</episode-num>`,
		`<episode-num system="dd_progid">EP01419237.0034</episode-num>`,
		`<episode-num system="original-air-date">2013-02-12</episode-num>`,
		`<episode-num system="plex">plex://episode/5d9c0e3b46115600200e2e3a</episode-num>`,
		`<episode-num system="plex.show">plex://show/5d9c086c46115600200aa2fe</episode-num>`,
		`<episode-num system="plex.season">plex://season/5d9c086c46115600200aa2fe/2</episode-num>`,
		`<rating system="VCHIP"><value>TV-14</value></rating>`,
		`<image type="poster" size="3" orient="P">https://img.example/show.jpg?w=680&amp;h=1000</image>`,
		`<image type="backdrop" size="3" orient="L">https://img.example/art.jpg</image>`,
		`<image type="still" size="3" orient="L">https://img.example/still.jpg</image>`,
	} {
		assert.Contains(t, s, want)
	}

	got, ok := xmltv.Programme{Inner: inner}.Listing()
	require.True(t, ok)
	assert.Equal(t, fullEpisode, got)

	// The same programme through a built guide and back.
	var out bytes.Buffer
	guides := map[string]xmltv.Guide{"plex": {Programmes: map[string][]xmltv.Programme{"1001": {{Start: t0, Stop: t0.Add(time.Hour), Inner: inner}}}}}
	require.NoError(t, xmltv.BuildGuide(&out, []xmltv.LineupChannel{{Number: "1001", Name: "NBC", Map: &xmltv.Mapping{Guide: "plex", ChannelID: "1001"}}}, guides, t0, 1))
	built, err := xmltv.Parse(&out, map[string]bool{"1001": true}, t0, t0.Add(24*time.Hour))
	require.NoError(t, err)
	require.Len(t, built.Programmes["1001"], 1)
	got, ok = built.Programmes["1001"][0].Listing()
	require.True(t, ok)
	assert.Equal(t, fullEpisode, got)
}

// A Listing with no kind is an episode when it is numbered, else a movie;
// an episode numbered with the episode alone is ".e." and "E12".
func TestListingKindAndPartialNumbers(t *testing.T) {
	ep := string(xmltv.Listing{Title: "Jeopardy!", Episode: 12}.Inner())
	assert.Contains(t, ep, `<category>Series</category>`)
	assert.Contains(t, ep, `<episode-num system="xmltv_ns">.11.</episode-num>`)
	assert.Contains(t, ep, `<episode-num system="onscreen">E12</episode-num>`)

	mv := string(xmltv.Listing{Title: "Heat"}.Inner())
	assert.Equal(t, `<title>Heat</title><category>Movie</category>`, mv)

	// A season without an episode is no number PMS reads (spec §2.3:
	// xmltv_ns "3.." is a movie), so none is written.
	season := string(xmltv.Listing{Kind: xmltv.KindEpisode, Title: "Show", Season: 3}.Inner())
	assert.NotContains(t, season, "<episode-num")
}

// A movie carries no episode number of any system PMS reads (an
// xmltv_ns, onscreen or original-air-date one would file it as an
// episode, spec §2.3), the classifier Movie once whatever case the
// source wrote it in, and its plex guid; enriched on a channel that is not
// a movie channel it gets no generated id either, so PMS files it as a
// movie.
func TestAMovieCarriesNoEpisodeNumAndCategoryMovie(t *testing.T) {
	heat := xmltv.Listing{
		Kind: xmltv.KindMovie, Title: "Heat", Desc: "A crew of thieves.", Date: "1995",
		Categories: []string{"Crime", "movie"}, Poster: "https://img.example/heat.jpg", Backdrop: "https://img.example/heat-art.jpg",
		Season: 1, Episode: 2, OriginallyAvailableAt: "1995-12-15",
		PlexGUID: "plex://movie/5d776826e6d55c002040ac72", Rating: "R", RatingSystem: "MPAA", LengthMinutes: 170,
	}
	inner := string(heat.Inner())
	for _, system := range []string{"xmltv_ns", "onscreen", "dd_progid", "original-air-date"} {
		assert.NotContains(t, inner, `system="`+system+`"`)
	}
	assert.Equal(t, 1, strings.Count(strings.ToLower(inner), ">movie</category>"), inner)
	assert.NotContains(t, inner, "Series")
	assert.Contains(t, inner, `<episode-num system="plex">plex://movie/5d776826e6d55c002040ac72</episode-num>`)
	assert.Contains(t, inner, `<date>1995</date>`)
	assert.Contains(t, inner, `<rating system="MPAA"><value>R</value></rating>`)
	assert.NotContains(t, inner, `type="still"`)
	inDTDOrder(t, []byte(inner))

	got, ok := xmltv.Programme{Inner: []byte(inner)}.Listing()
	require.True(t, ok)
	assert.Equal(t, xmltv.KindMovie, got.Kind)
	assert.Equal(t, []string{"Crime"}, got.Categories, "the source's own Movie is the classifier")

	enriched := string((&xmltv.Enricher{}).Enrich([]byte(inner), plain, t0, t0.Add(3*time.Hour)))
	assert.NotContains(t, enriched, "dd_progid", "a movie the profile names is filed as a movie")
	assert.Contains(t, enriched, `<episode-num system="plex">plex://movie/5d776826e6d55c002040ac72</episode-num>`)
}

// An episode without numbers carries no episode number of its own: the
// enricher gives it a dd_progid as it does every source's, and keeps its
// plex guids and images as they were, in XMLTV's order.
func TestAnEpisodeWithoutNumbersCarriesNone(t *testing.T) {
	news := xmltv.Listing{
		Kind: xmltv.KindEpisode, Title: "NBC Nightly News", SubTitle: "November 10, 2026", Desc: "The day's news.",
		Poster: "https://img.example/nn.jpg", Still: "https://img.example/nn-still.jpg",
		PlexGUID: "plex://episode/6720a1b2c3d4e5f6a7b8c9d0", PlexShowGUID: "plex://show/5d9c08a0e9d5a1001f4f3c1b", Live: true,
	}
	inner := news.Inner()
	assert.NotContains(t, string(inner), `system="xmltv_ns"`)
	assert.NotContains(t, string(inner), `system="onscreen"`)
	assert.NotContains(t, string(inner), `system="dd_progid"`)

	out := (&xmltv.Enricher{}).Enrich(inner, plain, t0, t0.Add(30*time.Minute))
	s := string(out)
	assert.Regexp(t, `<episode-num system="dd_progid">EP\d{8}\.\d{4}</episode-num>`, s)
	assert.Contains(t, s, `<episode-num system="plex">plex://episode/6720a1b2c3d4e5f6a7b8c9d0</episode-num>`)
	assert.Contains(t, s, `<episode-num system="plex.show">plex://show/5d9c08a0e9d5a1001f4f3c1b</episode-num>`)
	assert.Contains(t, s, `<image type="still" size="3" orient="L">https://img.example/nn-still.jpg</image>`)
	assert.Equal(t, 1, strings.Count(s, "<live/>"))
	inDTDOrder(t, out)

	got, ok := xmltv.Programme{Inner: out}.Listing()
	require.True(t, ok)
	assert.Equal(t, xmltv.KindEpisode, got.Kind)
	assert.Regexp(t, `^EP\d{8}\.\d{4}$`, got.ProgID)
	assert.Equal(t, news.PlexGUID, got.PlexGUID)
	assert.Equal(t, news.Still, got.Still)
}

// Programme.Listing reads any source's programme, passing over what the
// profile does not carry, and refuses inner XML that does not parse.
func TestProgrammeListingIsTolerant(t *testing.T) {
	inner := []byte(`
    <category lang="en">Drama</category>
    <credits><actor role="Lead">Act Orone<image type="person">https://img.example/p.jpg</image></actor><presenter>Pre Senter</presenter></credits>
    <desc lang="en">S02 E11 — To Protect and Serve Man</desc>
    <episode-num>S02 E11</episode-num>
    <keyword lang="en">heist</keyword>
    <length units="hours">1</length>
    <icon src="https://iptv-epg.org/images/0sJ"/>
    <star-rating><value>7/10</value></star-rating>
    <title lang="en">Grimm</title>
  `)
	got, ok := xmltv.Programme{Inner: inner}.Listing()
	require.True(t, ok)
	assert.Equal(t, xmltv.Listing{
		Title: "Grimm", Desc: "S02 E11 — To Protect and Serve Man",
		Actors: []xmltv.Credit{{Name: "Act Orone", Role: "Lead"}}, Categories: []string{"Drama"},
		Poster: "https://iptv-epg.org/images/0sJ", Kind: xmltv.KindEpisode, Season: 2, Episode: 11, LengthMinutes: 60,
	}, got)

	_, ok = xmltv.Programme{Inner: []byte(`<title>Unclosed`)}.Listing()
	assert.False(t, ok)
}
