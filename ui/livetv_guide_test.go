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

package ui_test

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mediactl/clustarr/pkg/iptv/livetvstore"
	"github.com/mediactl/clustarr/ui"
	"github.com/mediactl/clustarr/ui/projection"
)

// guideFrom is the guide index's first day; tvNow (19:10) puts now - 30
// min in window 3 (18:00-24:00).
var guideFrom = time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)

// guideFixture is argon-tv with ESPN (1001, sports) and CNN (1002, news)
// and a guide index: 1001's Pregame 17:30-18:30 and NFL Football
// 19:00-20:00, 1002's News 18:00-19:00.
func guideFixture(t *testing.T, withIndex bool) (*ui.Server, client.Client) {
	t.Helper()
	store := liveTVStore(t)
	putObject(t, store, livetvstore.PlaylistObject(tvKey), livetvstore.Playlist{Entries: []livetvstore.PlaylistEntry{
		{Key: "n:espn", Name: "ESPN HD", Group: "USA: SPORTS"}, {Key: "n:cnn", Name: "CNN", Group: "USA: NEWS"},
	}})
	if withIndex {
		at := func(h, m int) time.Time {
			return guideFrom.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute)
		}
		putObject(t, store, livetvstore.GuideIndexObject(tvKey), livetvstore.GuideIndex{From: guideFrom,
			Channels: []livetvstore.IndexChannel{{Number: "1001", Name: "ESPN HD", Logo: "https://epg.example/espn.png"}, {Number: "1002", Name: "CNN"}},
			Programmes: []livetvstore.IndexProgramme{
				{Channel: "1001", Start: at(17, 30), Stop: at(18, 30), Title: "Pregame"},
				{Channel: "1001", Start: at(19, 0), Stop: at(20, 0), Title: "NFL Football", SubTitle: "Cowboys at Eagles", Desc: "Week 9.",
					Episode: "S01E02", Image: "https://image.tmdb.org/t/p/w500/nfl.jpg", Live: true, New: true},
				{Channel: "1002", Start: at(18, 0), Stop: at(19, 0), Title: "News", Image: "https://iptv-epg.example/news.jpg"},
			}})
	}
	espn := tvChannel("argon-tv-espn", "n:espn", "1001", true, "Active", "xmltv/US-ESPN")
	cnn := tvChannel("argon-tv-cnn", "n:cnn", "1002", true, "Active", "dummy/60")
	return tvServerWith(t, store, tvProvider(), espn, cnn)
}

// windowOf is window k's layer in body.
func windowOf(t *testing.T, body string, k int) string {
	t.Helper()
	i := strings.Index(body, fmt.Sprintf(`data-window="%d"`, k))
	require.GreaterOrEqual(t, i, 0, "window %d", k)
	end := strings.Index(body[i:], `data-window-end`)
	require.Greater(t, end, 0)
	return body[i : i+end]
}

// block is the programme titled title in s: its opening tag.
func block(t *testing.T, s, title string) string {
	t.Helper()
	i := strings.Index(s, ">"+title+"<")
	require.GreaterOrEqual(t, i, 0, title)
	start := strings.LastIndex(s[:i], "<button")
	require.GreaterOrEqual(t, start, 0)
	return s[start : start+strings.Index(s[start:], ">")+1]
}

func TestGuidePageRendersTheWindowAroundNow(t *testing.T) {
	srv, _ := guideFixture(t, true)
	rec := get(t, srv, "/livetv/guide")
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Equal(t, "private, max-age=10", rec.Header().Get("Cache-Control"))
	w3 := windowOf(t, body, 3)
	_ = windowOf(t, body, 2)
	_ = windowOf(t, body, 4)
	assert.NotContains(t, body, `data-window="1"`, "the window around now, the one before it and the next")
	assert.NotContains(t, body, `data-window="5"`)
	nfl := block(t, w3, "NFL Football")
	assert.Contains(t, nfl, "left:450px", "19:00 is 60 minutes into 18:00-24:00, at 7.5 px a minute")
	assert.Contains(t, nfl, "width:448px")
	assert.Contains(t, nfl, "top:40px", "the first row, under the time header")
	news := block(t, w3, "News")
	assert.Contains(t, news, "left:0px")
	assert.Contains(t, news, "top:130px", "the second row")
	for _, s := range []string{"S01E02", "LIVE", "NEW"} {
		assert.Contains(t, w3, s)
	}
	for _, k := range []string{"1", "5"} {
		requireTag(t, body, `k=`+k+`&amp;`, `hx-get="/livetv/guide/window?`, `hx-trigger="intersect once root:#guide-scroll"`)
	}
	rows := regexp.MustCompile(`data-guide-channel="([0-9.]+)"`).FindAllStringSubmatch(body, -1)
	require.Len(t, rows, 2)
	assert.Equal(t, []string{"1001", "1002"}, []string{rows[0][1], rows[1][1]})
	requireTag(t, body, `src="`+projection.LogoPath(tvKey, "https://epg.example/espn.png")+`"`)
	requireTag(t, body, `id="guide-now"`)
}

func TestTheGuideOpensWithWhatIsAiringThoughItStartedBeforeTheWindow(t *testing.T) {
	store := liveTVStore(t)
	putObject(t, store, livetvstore.GuideIndexObject(tvKey), livetvstore.GuideIndex{From: guideFrom,
		Channels: []livetvstore.IndexChannel{{Number: "1001", Name: "ESPN HD"}},
		Programmes: []livetvstore.IndexProgramme{
			// 17:00-21:00 starts in window 2 (12:00-18:00) and airs at
			// 19:10, when the page opens on window 3.
			{Channel: "1001", Start: guideFrom.Add(17 * time.Hour), Stop: guideFrom.Add(21 * time.Hour), Title: "Marathon"},
		}})
	srv, _ := tvServerWith(t, store, tvProvider(), tvChannel("argon-tv-espn", "n:espn", "1001", true, "Active", "xmltv/US-ESPN"))
	body := get(t, srv, "/livetv/guide").Body.String()
	assert.Equal(t, 1, strings.Count(body, ">Marathon<"), "drawn at the now line on load, once")
}

func TestAProgrammeBelongsToTheWindowItStartsIn(t *testing.T) {
	srv, _ := guideFixture(t, true)
	w2 := get(t, srv, "/livetv/guide/window?k=2&dir=-1").Body.String()
	pre := block(t, w2, "Pregame")
	assert.Contains(t, pre, "left:2475px", "17:30 is 330 minutes into 12:00-18:00")
	assert.Contains(t, pre, "width:448px", "drawn whole, across the window's edge")
	w3 := get(t, srv, "/livetv/guide/window?k=3&dir=1").Body.String()
	assert.NotContains(t, w3, ">Pregame<", "once, in the window it starts in")
}

func TestGuideWindowClampsItsInputs(t *testing.T) {
	srv, _ := guideFixture(t, true)
	for _, k := range []string{"-1", "28"} {
		rec := get(t, srv, "/livetv/guide/window?k="+k+"&dir=1")
		require.Equal(t, http.StatusOK, rec.Code, k)
		assert.NotContains(t, rec.Body.String(), "data-programme", k)
		assert.NotContains(t, rec.Body.String(), "intersect once", k)
	}
	assert.Equal(t, http.StatusBadRequest, get(t, srv, "/livetv/guide/window?k=x").Code)
	w0 := get(t, srv, "/livetv/guide/window?k=0&dir=-1").Body.String()
	assert.NotContains(t, w0, "intersect once", "nothing before the guide's first window")
	w27 := get(t, srv, "/livetv/guide/window?k=27&dir=1").Body.String()
	assert.NotContains(t, w27, "intersect once", "nothing after its last")
	w2 := get(t, srv, "/livetv/guide/window?k=2&dir=-1").Body.String()
	requireTag(t, w2, `k=1&amp;`, `hx-trigger="intersect once root:#guide-scroll"`, `dir=-1`)
}

func TestGuideWithNoIndexSaysTheGuideIsNotBuiltYet(t *testing.T) {
	srv, _ := guideFixture(t, false)
	rec := get(t, srv, "/livetv/guide")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "not built yet")
	assert.NotContains(t, rec.Body.String(), "data-programme")
}

func TestGuideFiltersByGroupAndSearch(t *testing.T) {
	srv, _ := guideFixture(t, true)
	body := get(t, srv, "/livetv/guide?q=espn").Body.String()
	assert.Equal(t, 1, strings.Count(body, "data-guide-channel="))
	assert.NotContains(t, body, ">News<")
	body = get(t, srv, "/livetv/guide?group=USA%3A+NEWS").Body.String()
	assert.Equal(t, 1, strings.Count(body, "data-guide-channel="))
	assert.Contains(t, body, `data-guide-channel="1002"`)
	body = get(t, srv, "/livetv/guide?q=1001").Body.String()
	assert.Contains(t, body, `data-guide-channel="1001"`)
}

func TestProgrammeDialogShowsDetailsAndTheStreamURL(t *testing.T) {
	srv, _ := guideFixture(t, true)
	rec := get(t, srv, "/livetv/guide/programme?provider=media%2Fargon-tv&channel=1001&start=2026-10-27T19:00:00Z")
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	for _, s := range []string{"NFL Football", "Cowboys at Eagles", "Week 9.", "S01E02"} {
		assert.Contains(t, body, s)
	}
	requireTag(t, body, `data-copy="http://`+tvAddr+`/hdhr/AAAA0001/stream/1001"`)
	assert.Contains(t, body, "/art/search?src=", "a TMDB image through the signed proxy")
	assert.NotContains(t, body, `src="https://image.tmdb.org`, "never hot-linked")
	news := get(t, srv, "/livetv/guide/programme?provider=media%2Fargon-tv&channel=1002&start=2026-10-27T18:00:00Z").Body.String()
	assert.NotContains(t, news, "iptv-epg.example", "an image on a host the proxy does not fetch is left out")
	assert.Equal(t, http.StatusNotFound, get(t, srv, "/livetv/guide/programme?provider=media%2Fargon-tv&channel=1001&start=2026-10-27T05:00:00Z").Code)
}

func TestGuideTimesAreLabelledInTheBrowsersZone(t *testing.T) {
	srv, _ := guideFixture(t, true)
	body := get(t, srv, "/livetv/guide").Body.String()
	labels := regexp.MustCompile(`<time data-local="hour" datetime="([^"]+)"`).FindAllStringSubmatch(body, -1)
	require.NotEmpty(t, labels)
	for _, l := range labels {
		assert.True(t, strings.HasSuffix(l[1], "Z"), l[1])
	}
}
