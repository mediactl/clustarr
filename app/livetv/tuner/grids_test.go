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

package tuner_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/app/livetv/tuner"
	"github.com/mediactl/clustarr/pkg/iptv/filter"
	"github.com/mediactl/clustarr/pkg/iptv/plexguide"
)

const (
	// wabcGridKey is WABCDT's station in DIRECTV Mobile, the HD feed argon's
	// ABC (ABCEast_WABC.us) pairs with.
	wabcGridKey = "5fc705faa62668002dcefe0d"
	// newsLineupID is a second lineup, which lists CNN alone.
	newsLineupID = "657cb97c00fec317e3e246e1"
	// threeLineupID lists three stations, gridKeys threeKey(1..3) in order.
	threeLineupID = "657cb97c00fec317e3e24333"
	gridToken     = "grid-token-secret"
)

// gridNow is the day the grid fixture (test/data/plexgrid) airs from.
var gridNow = time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)

// newArgonPanel is an Xtream panel listing argon's ABC and CNN as its
// playlist and guide name them: ABCEast_WABC.us carries WABC's call sign.
func newArgonPanel(t *testing.T) *httptest.Server {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/get.php":
			_, _ = fmt.Fprintf(w, "#EXTM3U\n"+
				"#EXTINF:-1 tvg-id=\"ABCEast_WABC.us\" tvg-name=\"ABC\" group-title=\"USA: NEWS NETWORK\",ABC\n%[1]s/live/user/pass/1.ts\n"+
				"#EXTINF:-1 tvg-id=\"CNN.us\" tvg-name=\"CNN\" group-title=\"USA: NEWS NETWORK\",CNN\n%[1]s/live/user/pass/2.ts\n"+
				"#EXTINF:-1 tvg-id=\"FoxEast_WNYW.us\" tvg-name=\"FOX\" group-title=\"USA: NEWS NETWORK\",FOX\n%[1]s/live/user/pass/3.ts\n", srv.URL)
		case "/xmltv.php":
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><tv>
<channel id="ABCEast_WABC.us"><display-name>ABC</display-name></channel>
<channel id="CNN.us"><display-name>CNN</display-name></channel>
<programme start="20261110180000 +0000" stop="20261110190000 +0000" channel="ABCEast_WABC.us"><title>World News Tonight (xmltv)</title></programme>
<programme start="20261110180000 +0000" stop="20261110190000 +0000" channel="CNN.us"><title>Newsroom (xmltv)</title></programme>
</tv>`))
		case "/player_api.php":
			_, _ = w.Write([]byte(`{"user_info":{"auth":1,"status":"Active","max_connections":"1"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// guideService is Plex's guide service: the lineups and the grid, which
// answers the fixture for WABCDT and nothing for any other station.
type guideService struct {
	srv *httptest.Server
	// mode is the grid's answer for WABCDT: 0 the fixture, 1 a 429 with
	// Retry-After 120, 2 an empty grid.
	mode  atomic.Int32
	token atomic.Value // the last grid request's X-Plex-Token
	// hold, once holdGrids made it, keeps the answers of the stations the
	// service has a grid for (WABCDT, the three-station lineup's) until
	// release; a station it has none for is answered at once.
	hold     chan struct{}
	holdOnce sync.Once
	// answer, when set, answers a grid request first; false leaves it to
	// the rules above.
	answer atomic.Pointer[func(w http.ResponseWriter, gridKey string) bool]
	gets   sync.Map // gridKey -> *atomic.Int32
}

// threeKey is the three-station lineup's i-th gridKey.
func threeKey(i int) string { return fmt.Sprintf(threeKeyPrefix+"%d", i) }

const threeKeyPrefix = "bbbbbbbbbbbbbbbbbbbbbbb"

// holdGrids holds the grid's answers until release, which the test's
// cleanup also calls (before the server's Close, which would otherwise wait
// on a held answer for good).
func (s *guideService) holdGrids(t *testing.T) {
	s.hold = make(chan struct{})
	t.Cleanup(s.release)
}

func (s *guideService) release() { s.holdOnce.Do(func() { close(s.hold) }) }

func newGuideService(t *testing.T) *guideService {
	directv, err := os.ReadFile("../../../test/data/plexguide/directv-mobile-channels.json")
	require.NoError(t, err)
	grid, err := os.ReadFile("../../../test/data/plexgrid/grid-sample.json")
	require.NoError(t, err)
	s := &guideService{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/lineups/" + plexLineupID:
			_, _ = w.Write([]byte(`{"MediaContainer":{"Lineup":[{"id":"` + plexLineupID + `","title":"DIRECTV Mobile"}]}}`))
		case "/lineups/" + plexLineupID + "/channels":
			_, _ = w.Write(directv)
		case "/lineups/" + newsLineupID:
			_, _ = w.Write([]byte(`{"MediaContainer":{"Lineup":[{"id":"` + newsLineupID + `","title":"News Only"}]}}`))
		case "/lineups/" + threeLineupID:
			_, _ = w.Write([]byte(`{"MediaContainer":{"Lineup":[{"id":"` + threeLineupID + `","title":"Three"}]}}`))
		case "/lineups/" + threeLineupID + "/channels":
			var chans []string
			for i := 1; i <= 3; i++ {
				chans = append(chans, fmt.Sprintf(`{"id":"%s-%s","gridKey":"%s","callSign":"STN%d","title":"Station %d","vcn":"%d"}`, threeLineupID, threeKey(i), threeKey(i), i, i, i))
			}
			_, _ = w.Write([]byte(`{"MediaContainer":{"Channel":[` + strings.Join(chans, ",") + `]}}`))
		case "/lineups/" + newsLineupID + "/channels":
			_, _ = w.Write([]byte(`{"MediaContainer":{"Channel":[{"id":"` + newsLineupID + `-cccccccccccccccccccccccc","gridKey":"cccccccccccccccccccccccc","callSign":"CNNHD","title":"CNN HD","vcn":"202","isHd":true}]}}`))
		case "/grid/bulk":
			k := r.URL.Query().Get("channelGridKey")
			c, _ := s.gets.LoadOrStore(k, new(atomic.Int32))
			c.(*atomic.Int32).Add(1)
			s.token.Store(r.Header.Get("X-Plex-Token"))
			if s.hold != nil && (k == wabcGridKey || strings.HasPrefix(k, threeKeyPrefix)) {
				<-s.hold
			}
			if f := s.answer.Load(); f != nil && (*f)(w, k) {
				return
			}
			if k != wabcGridKey {
				http.NotFound(w, r)
				return
			}
			switch s.mode.Load() {
			case 1:
				w.Header().Set("Retry-After", "120")
				http.Error(w, "slow down", http.StatusTooManyRequests)
			case 2:
				_, _ = w.Write([]byte(`{"MediaContainer":{"size":0,"Metadata":[]}}`))
			default:
				_, _ = w.Write(grid)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.srv.Close)
	return s
}

// gridGets is how many times the grid was asked for gridKey.
func (s *guideService) gridGets(gridKey string) int32 {
	c, ok := s.gets.Load(gridKey)
	if !ok {
		return 0
	}
	return c.(*atomic.Int32).Load()
}

// gridConfig is the argon panel's ABC and CNN on one block, their guide
// sources DIRECTV Mobile.
func gridConfig(panel string) tuner.Config {
	return tuner.Config{
		Key: key, Server: panel, Username: "user", Password: "pass",
		Blocks:    []tuner.BlockConfig{{Start: "1001", DeviceID: deviceA, FriendlyName: "Clustarr news 1001+", Numbers: []string{"1001", "1002"}}},
		UserAgent: "clustarr", Every: time.Hour,
		Filters: []filter.Rule{{Type: "groupTitle", Match: "USA: NEWS NETWORK", Enabled: true}},
		Channels: []tuner.ChannelConfig{
			{Object: "abc", Key: "t:ABCEast_WABC.us", Number: "1001", Name: "ABC", Active: true},
			{Object: "cnn", Key: "t:CNN.us", Number: "1002", Name: "CNN", Active: true},
		},
		GuideSources: &tuner.GuideSourcesConfig{Lineups: []string{plexLineupID}, Token: gridToken, Refresh: 24 * time.Hour, Rate: 100},
		Buffer:       1 << 20, Linger: time.Second,
	}
}

func startGrids(t *testing.T, svc *guideService, cfg tuner.Config) *harness {
	u, err := url.Parse(svc.srv.URL)
	require.NoError(t, err)
	return startWith(t, newStore(t), cfg, gridNow, func(o *tuner.Options) {
		o.Fetch = &http.Client{Transport: redirect{to: u}, Timeout: 10 * time.Second}
	})
}

// guideOf waits for object's channel to read guide.
func (h *harness) guideOf(t *testing.T, object, guide string) tuner.ChannelView {
	t.Helper()
	var v tuner.ChannelView
	require.Eventually(t, func() bool {
		v, _ = h.tn.Channel(key, object)
		return v.State.Guide == guide
	}, 5*time.Second, 10*time.Millisecond, "%s reads %s", object, guide)
	return v
}

// programmesOf is the programmes of channel number in an XMLTV body.
func programmesOf(body, number string) []string {
	var out []string
	for _, p := range strings.Split(body, "<programme ")[1:] {
		if strings.Contains(p, `channel="`+number+`"`) {
			out = append(out, p)
		}
	}
	return out
}

func TestAPairedStationsGridFeedsItsChannel(t *testing.T) {
	svc := newGuideService(t)
	h := startGrids(t, svc, gridConfig(newArgonPanel(t).URL))

	v := h.guideOf(t, "abc", "plex/"+wabcGridKey)
	require.NotNil(t, v.State.Plex)
	assert.Equal(t, plexguide.Automatic, v.State.Plex.Match)
	assert.Equal(t, "WABCDT", v.State.Plex.Station.CallSign)
	assert.Equal(t, plexLineupID, v.State.Plex.Lineup)
	assert.Equal(t, gridToken, svc.token.Load())

	code, body := h.get(t, "tuner.example", device(deviceA, "xmltv.xml"))
	require.Equal(t, http.StatusOK, code)
	abc := strings.Join(programmesOf(body, "1001"), "\n")
	assert.Contains(t, abc, `<episode-num system="plex">plex://episode/5fc6bf9abcd67c002d0836c8</episode-num>`)
	assert.Contains(t, abc, `<image type="still"`)
	assert.Contains(t, abc, "<title>Alien Predator</title>")
	assert.NotContains(t, abc, "World News Tonight", "a channel's programmes are one source's")
	assert.NotContains(t, abc, "Sharknado 2", "past the guide's week")

	// A range's guide reads the same grid back from the store.
	code, ranged := h.get(t, "tuner.example", "/livetv/media/news/xmltv.xml?from=1001&to=1001")
	require.Equal(t, http.StatusOK, code)
	assert.Contains(t, strings.Join(programmesOf(ranged, "1001"), "\n"), `<episode-num system="plex">plex://episode/5fc6bf9abcd67c002d0836c8</episode-num>`)

	// CNN's station has no grid: it keeps its XMLTV guide.
	h.guideOf(t, "cnn", "xmltv/CNN.us")
	assert.Contains(t, strings.Join(programmesOf(body, "1002"), "\n"), "Newsroom (xmltv)")

	snap, _ := h.tn.Snapshot(key)
	assert.Equal(t, 1, snap.GuideSources.Lineups)
	assert.Equal(t, 1, snap.GuideSources.Grids)
	assert.GreaterOrEqual(t, snap.GuideSources.Stations, 1)
	assert.Equal(t, gridNow, snap.GuideSources.FetchedAt)
	assert.Empty(t, snap.GuideSources.Error)
	assert.True(t, snap.GuideSources.HoldUntil.IsZero())
}

func TestAHeldGridFetchNeverHoldsARender(t *testing.T) {
	svc := newGuideService(t)
	svc.mode.Store(1)
	svc.holdGrids(t)
	h := startGrids(t, svc, gridConfig(newArgonPanel(t).URL))

	// The grid is asked for and has not answered: the render is done.
	require.Eventually(t, func() bool { return svc.gridGets(wabcGridKey) == 1 }, 5*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool {
		s, _ := h.tn.Snapshot(key)
		return len(s.Blocks) == 1 && s.Blocks[0].GuideHash != ""
	}, time.Second, 10*time.Millisecond)
	v := h.guideOf(t, "abc", "xmltv/ABCEast_WABC.us")
	assert.Equal(t, "WABCDT", v.State.Plex.Station.CallSign)

	svc.release()
	var snap tuner.Snapshot
	require.Eventually(t, func() bool {
		snap, _ = h.tn.Snapshot(key)
		return !snap.GuideSources.HoldUntil.IsZero()
	}, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, gridNow.Add(2*time.Minute), snap.GuideSources.HoldUntil, "Retry-After's 120 s")
	h.guideOf(t, "abc", "xmltv/ABCEast_WABC.us")
	time.Sleep(1200 * time.Millisecond) // a dispatch tick or more
	assert.Equal(t, int32(1), svc.gridGets(wabcGridKey), "nothing is asked during the hold")
}

func TestAnEmptyGridFallsToTheNextSource(t *testing.T) {
	svc := newGuideService(t)
	svc.mode.Store(2)
	h := startGrids(t, svc, gridConfig(newArgonPanel(t).URL))

	require.Eventually(t, func() bool {
		s, _ := h.tn.Snapshot(key)
		return !s.GuideSources.FetchedAt.IsZero()
	}, 5*time.Second, 10*time.Millisecond)
	h.guideOf(t, "abc", "xmltv/ABCEast_WABC.us")
	_, body := h.get(t, "tuner.example", device(deviceA, "xmltv.xml"))
	assert.Contains(t, strings.Join(programmesOf(body, "1001"), "\n"), "World News Tonight (xmltv)")
	snap, _ := h.tn.Snapshot(key)
	assert.Zero(t, snap.GuideSources.Grids)
	assert.Empty(t, snap.GuideSources.Error, "an empty grid is no failure")
}

func TestGridsRefreshDailyNotPerRender(t *testing.T) {
	svc := newGuideService(t)
	cfg := gridConfig(newArgonPanel(t).URL)
	h := startGrids(t, svc, cfg)
	h.guideOf(t, "abc", "plex/"+wabcGridKey)

	// Another render: CNN renamed.
	cfg.Channels[1].Name = "CNN International"
	h.tn.Apply(cfg)
	require.Eventually(t, func() bool {
		_, body := h.get(t, "tuner.example", device(deviceA, "xmltv.xml"))
		return strings.Contains(body, "CNN International")
	}, 5*time.Second, 10*time.Millisecond)
	time.Sleep(1200 * time.Millisecond) // a dispatch tick or more
	assert.Equal(t, int32(1), svc.gridGets(wabcGridKey), "a render asks for no grid")

	// A day on, the grid is asked for again.
	h.clock.Store(gridNow.Add(25 * time.Hour).UnixNano())
	require.Eventually(t, func() bool { return svc.gridGets(wabcGridKey) == 2 }, 5*time.Second, 10*time.Millisecond)
	h.guideOf(t, "abc", "plex/"+wabcGridKey)
}

func TestStationsPairOverLineupsInOrder(t *testing.T) {
	svc := newGuideService(t)
	cfg := gridConfig(newArgonPanel(t).URL)
	cfg.GuideSources.Lineups = []string{newsLineupID, plexLineupID}
	h := startGrids(t, svc, cfg)

	abc := h.guideOf(t, "abc", "plex/"+wabcGridKey)
	require.NotNil(t, abc.State.Plex)
	assert.Equal(t, plexLineupID, abc.State.Plex.Lineup, "WABC is only in the second lineup")
	assert.Equal(t, "WABCDT", abc.State.Plex.Station.CallSign)
	var cnn tuner.ChannelView
	require.Eventually(t, func() bool {
		cnn, _ = h.tn.Channel(key, "cnn")
		return cnn.State.Plex != nil && cnn.State.Plex.Match.OnPlex()
	}, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, newsLineupID, cnn.State.Plex.Lineup, "CNN is in both: the first lineup's")
	assert.Equal(t, "cccccccccccccccccccccccc", cnn.State.Plex.Station.GridKey)
	snap, _ := h.tn.Snapshot(key)
	assert.Equal(t, 2, snap.GuideSources.Lineups)
}

// A 429's hold runs from its answer, not from its pass's start: a pass of
// a few hundred stations runs for minutes, and a hold timed from its start
// could end before the 429 came. The pass ends at the 429: the station
// after it is never asked.
func TestAHoldIsTimedFromThe429(t *testing.T) {
	grid, err := os.ReadFile("../../../test/data/plexgrid/grid-sample.json")
	require.NoError(t, err)
	svc := newGuideService(t)
	svc.holdGrids(t)
	cfg := gridConfig(newArgonPanel(t).URL)
	cfg.Blocks[0].Numbers = append(cfg.Blocks[0].Numbers, "1003")
	cfg.Channels = append(cfg.Channels, tuner.ChannelConfig{Object: "fox", Key: "t:FoxEast_WNYW.us", Number: "1003", Name: "FOX", Active: true})
	for i := range cfg.Channels {
		cfg.Channels[i].PlexStation = threeKey(i + 1) // abc, cnn, fox: one, two, three
	}
	cfg.GuideSources.Lineups = []string{threeLineupID}
	h := startGrids(t, svc, cfg)
	answer := func(w http.ResponseWriter, k string) bool {
		switch k {
		case threeKey(1):
			_, _ = w.Write(grid)
		case threeKey(2):
			h.clock.Add(int64(5 * time.Minute)) // the pass has run five minutes by now
			w.Header().Set("Retry-After", "120")
			http.Error(w, "slow down", http.StatusTooManyRequests)
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
		return true
	}
	svc.answer.Store(&answer)
	svc.release()

	var snap tuner.Snapshot
	require.Eventually(t, func() bool {
		snap, _ = h.tn.Snapshot(key)
		return !snap.GuideSources.HoldUntil.IsZero()
	}, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, gridNow.Add(5*time.Minute+2*time.Minute), snap.GuideSources.HoldUntil, "the 429's own time plus its Retry-After")
	h.guideOf(t, "abc", "plex/"+threeKey(1))
	time.Sleep(1200 * time.Millisecond) // a dispatch tick or more
	assert.Equal(t, int32(1), svc.gridGets(threeKey(1)))
	assert.Equal(t, int32(1), svc.gridGets(threeKey(2)), "nothing asked during the hold")
	assert.Zero(t, svc.gridGets(threeKey(3)), "the pass ends at the 429")
}
