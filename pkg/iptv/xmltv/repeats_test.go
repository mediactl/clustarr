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
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/iptv/xmltv"
)

// airing is one programme of a built guide's channel.
type airing struct {
	channel string
	start   time.Time
	inner   string
}

// built is the guide BuildGuideWith writes for airings, a channel per
// distinct channel number, over the week from t0.
func built(t *testing.T, e *xmltv.Enricher, airings []airing) string {
	t.Helper()
	g := xmltv.Guide{Programmes: map[string][]xmltv.Programme{}}
	seen := map[string]bool{}
	var lineup []xmltv.LineupChannel
	for _, a := range airings {
		g.Programmes[a.channel] = append(g.Programmes[a.channel], xmltv.Programme{
			Channel: a.channel, Start: a.start, Stop: a.start.Add(30 * time.Minute), Inner: []byte(a.inner),
		})
		if !seen[a.channel] {
			seen[a.channel] = true
			lineup = append(lineup, xmltv.LineupChannel{
				Number: a.channel, Name: a.channel, Group: "Entertainment",
				Map: &xmltv.Mapping{Guide: "main", ChannelID: a.channel},
			})
		}
	}
	var out strings.Builder
	require.NoError(t, xmltv.BuildGuideWith(&out, lineup, map[string]xmltv.Guide{"main": g}, t0.Add(-time.Hour), 8, e))
	return out.String()
}

var progIDRE = regexp.MustCompile(`<episode-num system="dd_progid">((EP|MV|SP)(\d{8})\.\d{4})</episode-num>`)

// idsOf is every dd_progid of guide in order, and each id's count.
func idsOf(guide string) ([]string, map[string]int) {
	var ids []string
	n := map[string]int{}
	for _, m := range progIDRE.FindAllStringSubmatch(guide, -1) {
		ids = append(ids, m[1])
		n[m[1]]++
	}
	return ids, n
}

// A listing that reads the same on every channel all week -- the provider's
// "Paid Programming", "Paid programming." -- is a run of airings, not one
// episode: on talos (2026-10-28) its one id carried 1,077 airings on 70
// channels, every Live TV hub showing it listed all of them, and Plex's
// What's On took 20 to 46 s a hub until the app gave up. Each airing is its
// own episode of the one show.
func TestAListingOnEveryChannelAllWeekIsAiringsNotOneEpisode(t *testing.T) {
	var airings []airing
	for c := range 10 {
		for d := range 4 {
			airings = append(airings, airing{
				channel: fmt.Sprint(1001 + c), start: t0.AddDate(0, 0, d).Add(time.Duration(c) * time.Hour),
				inner: `<title>Paid Programming</title><desc>Paid programming.</desc>`,
			})
		}
	}
	ids, n := idsOf(built(t, &xmltv.Enricher{}, airings))
	require.Len(t, ids, 40)
	for id, k := range n {
		assert.LessOrEqual(t, k, 2, "%s carries %d airings", id, k)
	}
	assert.Greater(t, len(n), 30, "nearly every airing is an episode of its own")
	series := map[string]bool{}
	for _, id := range ids {
		series[id[2:10]] = true
	}
	assert.Len(t, series, 1, "all of them episodes of one show")
}

// A repeat the guide describes alike -- an episode at 8 pm and again at
// 1 am on another channel -- is still one episode.
func TestARepeatIsStillOneEpisode(t *testing.T) {
	inner := `<title>Forensic Files</title><desc>A woman's body is found in a burning car.</desc>`
	ids, n := idsOf(built(t, &xmltv.Enricher{}, []airing{
		{channel: "1040", start: t0, inner: inner},
		{channel: "1041", start: t0.Add(5 * time.Hour), inner: inner},
	}))
	require.Len(t, ids, 2)
	assert.Len(t, n, 1)
}

// A daily show whose every listing reads alike -- a newscast at noon -- is
// a new episode each day: its slot on its channel comes back the next day,
// which a repeat's does not.
func TestADailyShowIsANewEpisodeEachDay(t *testing.T) {
	inner := `<title>Eyewitness News at Noon</title><desc>The latest local news, weather and sports.</desc>`
	var airings []airing
	for d := range 3 {
		airings = append(airings, airing{channel: "1002", start: t0.AddDate(0, 0, d), inner: inner})
	}
	ids, n := idsOf(built(t, &xmltv.Enricher{}, airings))
	require.Len(t, ids, 3)
	assert.Len(t, n, 3)
}

// A title the art resolver takes for a film but that airs all day -- BET
// Centric's "Lifted" block, found as the 2010 film -- is no movie: on talos
// its MV id carried 98 airings in Plex's Upcoming Movies. A film aired a few
// times is still one movie.
func TestATitleThatAirsAllWeekIsNoMovie(t *testing.T) {
	e := &xmltv.Enricher{Art: func(q xmltv.ArtQuery) (xmltv.Art, bool) {
		return xmltv.Art{URL: "https://image.tmdb.org/t/p/w500/" + q.Title + ".jpg", Kind: xmltv.ArtMovie}, true
	}}
	var airings []airing
	for h := range 20 {
		airings = append(airings, airing{
			channel: "1085", start: t0.Add(time.Duration(h) * 2 * time.Hour),
			inner: `<title>Lifted</title><desc>Gospel and inspirational music.</desc>`,
		})
	}
	for d := range 3 {
		airings = append(airings, airing{
			channel: "1073", start: t0.AddDate(0, 0, d).Add(3 * time.Hour),
			inner: `<title>Heat</title><desc>A thief and the detective who hunts him.</desc>`,
		})
	}
	byChannel := channelProgrammes(built(t, e, airings))
	assert.NotContains(t, byChannel["1085"], ">MV", "Lifted is no movie")
	assert.NotContains(t, byChannel["1085"], `<category lang="en">Movie</category>`)
	ids, n := idsOf(byChannel["1073"])
	require.Len(t, ids, 3)
	assert.Len(t, n, 1, "Heat's three airings are one movie")
	assert.True(t, strings.HasPrefix(ids[0], "MV"))
}

// channelProgrammes is guide's programmes joined per channel.
func channelProgrammes(guide string) map[string]string {
	out := map[string]string{}
	for _, m := range regexp.MustCompile(`<programme [^>]*channel="([^"]+)">(.*?)</programme>`).FindAllStringSubmatch(guide, -1) {
		out[m[1]] += m[2]
	}
	return out
}
