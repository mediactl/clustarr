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

package tuner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/iptv/livetvstore"
	"github.com/mediactl/clustarr/pkg/iptv/plexgrid"
	"github.com/mediactl/clustarr/pkg/iptv/plexguide"
	"github.com/mediactl/clustarr/pkg/iptv/xmltv"
)

var day = time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)

func gridProgramme(start time.Time, title string) plexgrid.Programme {
	return plexgrid.Programme{Start: start, Stop: start.Add(time.Hour), Listing: xmltv.Listing{Kind: xmltv.KindEpisode, Title: title, PlexGUID: "plex://episode/x"}}
}

func TestStationLineupsAreTheCloudThenTheSourcesEachOnce(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  Config
		want []string
	}{
		{"none", Config{}, nil},
		{"the cloud alone, enabled or a dry run", Config{Plex: &PlexConfig{LineupID: "a"}}, []string{"a"}},
		{"the sources", Config{GuideSources: &GuideSourcesConfig{Lineups: []string{"b", "c", "b"}}}, []string{"b", "c"}},
		{"the enabled cloud first", Config{Plex: &PlexConfig{LineupID: "a", Enabled: true}, GuideSources: &GuideSourcesConfig{Lineups: []string{"b", "a"}}}, []string{"a", "b"}},
		{"a dry-run cloud beside sources is not a station lineup", Config{Plex: &PlexConfig{LineupID: "a"}, GuideSources: &GuideSourcesConfig{Lineups: []string{"b"}}}, []string{"b"}},
	} {
		t.Run(tc.name, func(t *testing.T) { assert.Equal(t, tc.want, stationLineups(tc.cfg)) })
	}
}

func TestSourceLineupsLeaveTheCloudToItsOwnFetch(t *testing.T) {
	cfg := Config{Plex: &PlexConfig{LineupID: "a", Token: "t"}, GuideSources: &GuideSourcesConfig{Lineups: []string{"a", "b"}, Token: "g"}}
	assert.Equal(t, []string{"b"}, sourceLineups(cfg))
	cfg.Plex.Token = ""
	assert.Equal(t, []string{"a", "b"}, sourceLineups(cfg), "a cloud lineup without its token is fetched with the sources'")
	cfg.GuideSources.Token = ""
	assert.Empty(t, sourceLineups(cfg), "no token, no fetch")
}

// A station kept from a later lineup is never kept as missing from an
// earlier one: the earlier lineup pairs the channel by its own rules.
func TestALastPairingIsShownOnlyToItsLineup(t *testing.T) {
	a := plexguide.Lineup{ID: "a", Stations: []plexguide.Station{{ID: "a-1", GridKey: "k1", CallSign: "CNNHD", Title: "CNN HD", VCN: "1", HD: true}}}
	b := plexguide.Lineup{ID: "b", Stations: []plexguide.Station{{ID: "b-2", GridKey: "k2", CallSign: "WABCDT", Title: "WABC-DT", VCN: "2", HD: true}}}
	p := &provider{lineups: map[string]plexguide.Lineup{"a": a, "b": b}}
	cfg := Config{GuideSources: &GuideSourcesConfig{Lineups: []string{"a", "b"}}}
	chans := []plexguide.Channel{{
		Object: "abc", Number: "1001", Name: "ABC", ChannelID: "ABCEast_WABC.us",
		Prev: plexguide.Prev{Match: plexguide.Automatic, Station: b.Stations[0]},
	}}
	rec, cloud := p.pairStations(cfg, chans, day)
	assert.Nil(t, cloud)
	got := rec["abc"]
	assert.Equal(t, plexguide.Automatic, got.Match)
	assert.Equal(t, "b", got.Lineup)
	assert.True(t, got.MissingSince.IsZero(), "not missing from a")

	// Before any lineup is held, each keeps what its status records, from
	// the lineup it came from.
	p.lineups = nil
	rec, _ = p.pairStations(cfg, chans, day)
	assert.Equal(t, plexguide.Pairing{Match: plexguide.Automatic, Station: b.Stations[0], Lineup: "b"}, rec["abc"])
}

func TestTheCloudPairingIsAPlexBlocksAlone(t *testing.T) {
	cloudL := plexguide.Lineup{ID: "a", Stations: []plexguide.Station{{ID: "a-1", GridKey: "k1", CallSign: "CNNHD", Title: "CNN HD", VCN: "1", HD: true}}}
	other := plexguide.Lineup{ID: "b", Stations: []plexguide.Station{{ID: "b-2", GridKey: "k2", CallSign: "WABCDT", Title: "WABC-DT", VCN: "2", HD: true}}}
	p := &provider{plex: plexState{lineup: cloudL, loaded: true}, lineups: map[string]plexguide.Lineup{"b": other}}
	cfg := Config{Plex: &PlexConfig{LineupID: "a", Enabled: true}, GuideSources: &GuideSourcesConfig{Lineups: []string{"b"}}}
	chans := []plexguide.Channel{
		{Object: "cnn", Number: "1", Name: "CNN", ChannelID: "CNN.us"},
		{Object: "abc", Number: "2", Name: "ABC", ChannelID: "ABCEast_WABC.us"},
	}
	rec, cloud := p.pairStations(cfg, chans, day)
	assert.Equal(t, "a", rec["cnn"].Lineup)
	assert.Equal(t, "b", rec["abc"].Lineup)
	assert.True(t, cloud["cnn"].Match.OnPlex())
	assert.False(t, cloud["abc"].Match.OnPlex(), "the Plex block's map holds the cloud lineup's stations only")
}

func TestWindowedKeepsTheGuidesWindow(t *testing.T) {
	to := day.AddDate(0, 0, guideDays)
	progs := []plexgrid.Programme{
		gridProgramme(day.Add(-time.Hour), "Ended at the window's start"),
		gridProgramme(day.Add(-30*time.Minute), "Running at the window's start"),
		gridProgramme(day.Add(20*time.Hour), "Today"),
		gridProgramme(to, "Past the week"),
	}
	got := windowed(progs, day, to)
	require.Len(t, got, 2)
	assert.Contains(t, string(got[0].Inner), "Running at the window")
	assert.Contains(t, string(got[1].Inner), "<title>Today</title>")
	assert.Equal(t, day.Add(20*time.Hour), got[1].Start)
}

func TestMergeGridsTakesAGridWithProgrammesOnly(t *testing.T) {
	to := day.AddDate(0, 0, guideDays)
	grids := map[string][]xmltv.Programme{
		"k1": windowed([]plexgrid.Programme{gridProgramme(day.Add(time.Hour), "Grid show")}, day, to),
		"k2": windowed([]plexgrid.Programme{gridProgramme(day.AddDate(0, 0, 10), "Next week")}, day, to), // none in the window
	}
	chans := []xmltv.LineupChannel{
		{Number: "1", Map: &xmltv.Mapping{Guide: GuideName, ChannelID: "one.us"}},
		{Number: "2", Map: &xmltv.Mapping{Guide: GuideName, ChannelID: "two.us"}},
		{Number: "3", DummyMinutes: 60},
		{Number: "4", TimeshiftMinutes: 60, Map: &xmltv.Mapping{Guide: GuideName, ChannelID: "four.us"}},
		{Number: "5", Map: &xmltv.Mapping{Guide: GuideName, ChannelID: "five.us"}},
	}
	objOf := map[string]string{"1": "one", "2": "two", "3": "three", "4": "four", "5": "five"}
	gridOf := map[string]string{"1": "k1", "2": "k2", "3": "k1", "4": "k1"}
	states := map[string]ChannelState{"one": {Guide: "xmltv/one.us"}, "two": {Guide: "xmltv/two.us"}, "three": {Guide: "dummy/60"}, "four": {Guide: "xmltv/four.us"}, "five": {Guide: "xmltv/five.us"}}

	g, byNumber, mapped := mergeGrids(chans, objOf, gridOf, grids, states)
	assert.Equal(t, map[string]string{"1": "k1", "3": "k1"}, byNumber)
	assert.Equal(t, 1, mapped, "the dummy channel is mapped now")
	assert.Equal(t, &xmltv.Mapping{Guide: plexGuideName, ChannelID: "1"}, chans[0].Map)
	assert.Equal(t, &xmltv.Mapping{Guide: plexGuideName, ChannelID: "3"}, chans[2].Map)
	assert.Zero(t, chans[2].DummyMinutes)
	assert.Equal(t, GuideName, chans[1].Map.Guide, "a grid with nothing in the window leaves the next source")
	assert.Equal(t, GuideName, chans[3].Map.Guide, "a timeshifted channel takes no grid")
	assert.Equal(t, "plex/k1", states["one"].Guide)
	assert.Equal(t, "plex/k1", states["three"].Guide)
	assert.Equal(t, "xmltv/two.us", states["two"].Guide)
	require.Len(t, g.Programmes["1"], 1)
	assert.Equal(t, "1", g.Programmes["1"][0].Channel)
	assert.Equal(t, "3", g.Programmes["3"][0].Channel, "two channels of one station each own their copy")
}

func TestGridsDue(t *testing.T) {
	gs := &GuideSourcesConfig{Token: "t", Refresh: 24 * time.Hour}
	for _, tc := range []struct {
		name string
		edit func(p *provider)
		want bool
	}{
		{"a grid never seen", func(p *provider) {}, true},
		{"fresh", func(p *provider) { p.grids.fetched = map[string]gridObject{"k": {at: day.Add(-time.Hour)}} }, false},
		{"a day old", func(p *provider) { p.grids.fetched = map[string]gridObject{"k": {at: day.Add(-24 * time.Hour)}} }, true},
		{"held", func(p *provider) { p.grids.holdUntil = day.Add(time.Minute) }, false},
		{"a hold ended", func(p *provider) { p.grids.holdUntil = day }, true},
		{"failed, retried later", func(p *provider) { p.grids.retry = map[string]time.Time{"k": day.Add(time.Minute)} }, false},
		{"fresh but stored before a force", func(p *provider) {
			p.grids.fetched = map[string]gridObject{"k": {at: day.Add(-time.Hour)}}
			p.grids.forceAt = day.Add(-time.Minute)
		}, true},
		{"stored since the force", func(p *provider) {
			p.grids.fetched = map[string]gridObject{"k": {at: day}}
			p.grids.forceAt = day.Add(-time.Minute)
		}, false},
		{"running", func(p *provider) { p.fetching["grids"] = true }, false},
		{"no token", func(p *provider) { p.cfg.GuideSources = &GuideSourcesConfig{} }, false},
		{"no guide sources", func(p *provider) { p.cfg.GuideSources = nil }, false},
		{"nothing wanted", func(p *provider) { p.grids.want = nil }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &provider{cfg: Config{GuideSources: gs}, fetching: map[string]bool{}, grids: gridState{want: []string{"k"}}}
			tc.edit(p)
			assert.Equal(t, tc.want, p.gridsDue(day))
		})
	}
}

func TestNoGridErrorCarriesTheToken(t *testing.T) {
	cfg := Config{GuideSources: &GuideSourcesConfig{Token: "s3cr3t/tok en"}}
	err := fmt.Errorf("plexgrid: grid k: %w", errors.New(`Get "https://epg.provider.plex.tv/grid/bulk?X-Plex-Token=s3cr3t%2Ftok+en": refused (s3cr3t/tok en, s3cr3t%2Ftok%20en)`))
	out := cfg.scrubGrid(err.Error())
	assert.NotContains(t, out, "s3cr3t")
	assert.Contains(t, out, "{plexToken}")
}

// fixtureGrid is test/data/plexgrid's recorded answer, reduced.
func fixtureGrid(tb testing.TB) []plexgrid.Programme {
	tb.Helper()
	b, err := os.ReadFile("../../../test/data/plexgrid/grid-sample.json")
	require.NoError(tb, err)
	var a struct {
		MediaContainer struct {
			Metadata []plexgrid.Item `json:"Metadata"`
		} `json:"MediaContainer"`
	}
	require.NoError(tb, json.Unmarshal(b, &a))
	return plexgrid.Reduce(a.MediaContainer.Metadata)
}

// stationGrid is a station's 14 days at about 30 airings a day, built from
// the fixture's airings with their descriptions made distinct (so gzip
// finds as little to share as in a real grid) until it stores as about
// 100 KB of gzip JSON, the design's estimate per station (§5.3).
func stationGrid(tb testing.TB) []plexgrid.Programme {
	tb.Helper()
	base := fixtureGrid(tb)
	rng := rand.New(rand.NewPCG(1, 2))
	word := func() string {
		const letters = "abcdefghijklmnopqrstuvwxyz"
		n := 3 + rng.IntN(7)
		b := make([]byte, n)
		for i := range b {
			b[i] = letters[rng.IntN(len(letters))]
		}
		return string(b)
	}
	var out []plexgrid.Programme
	for i := range 14 * 30 {
		pr := base[i%len(base)]
		pr.Start = day.Add(time.Duration(i) * 48 * time.Minute)
		pr.Stop = pr.Start.Add(48 * time.Minute)
		words := make([]string, 50)
		for j := range words {
			words[j] = word()
		}
		pr.Desc = strings.Join(words, " ")
		pr.SubTitle = word() + " " + word()
		pr.Categories = append([]string(nil), pr.Categories...)
		out = append(out, pr)
	}
	return out
}

// BenchmarkLoadGrids is a render's read of one station's grid from the
// store (a Get, the gzip JSON decode, the window cut and each programme's
// inner XML), which every render and range pays per paired station now
// that no grid is kept between renders. Run with -bench LoadGrids.
func BenchmarkLoadGrids(b *testing.B) {
	for _, tc := range []struct {
		name  string
		progs func(testing.TB) []plexgrid.Programme
	}{
		{"fixture", fixtureGrid},
		{"station-14d", stationGrid},
	} {
		b.Run(tc.name, func(b *testing.B) {
			bus := membus.New(nil)
			require.NoError(b, bus.Ensure(context.Background(), events.Default().ForSingleNode()))
			tn := New(Options{Store: bus.ObjectStore(events.BucketLiveTV)})
			key := types.NamespacedName{Namespace: "media", Name: "news"}
			p := &provider{key: key}
			progs := tc.progs(b)
			var enc bytes.Buffer
			require.NoError(b, livetvstore.Encode(&enc, progs))
			_, err := tn.putGrid(context.Background(), key, "k", progs)
			require.NoError(b, err)
			for b.Loop() {
				if got := tn.loadGrids(p, []string{"k"}, day); len(got["k"]) == 0 {
					b.Fatal("no programmes read")
				}
			}
			b.ReportMetric(float64(enc.Len())/1024, "gzipKB")
			b.ReportMetric(float64(len(progs)), "airings")
		})
	}
}

// gridTransport answers the guide service's /grid/bulk by station.
type gridTransport func(gridKey string) *http.Response

func (f gridTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r.URL.Query().Get("channelGridKey")), nil
}

func gridAnswer(code int, body string, header ...string) *http.Response {
	h := http.Header{}
	for i := 0; i+1 < len(header); i += 2 {
		h.Set(header[i], header[i+1])
	}
	return &http.Response{StatusCode: code, Status: http.StatusText(code), Header: h, Body: io.NopCloser(strings.NewReader(body))}
}

// gridTuner is a tuner on an in-memory store whose guide service is
// answer, its clock at day, and a provider applied with guide sources but
// no render pending (Debounce is an hour, and the one Apply asked for is
// stopped).
func gridTuner(t *testing.T, answer gridTransport, notified *atomic.Int32) (*Tuner, *provider, Config) {
	t.Helper()
	bus := membus.New(nil)
	require.NoError(t, bus.Ensure(context.Background(), events.Default().ForSingleNode()))
	tn := New(Options{
		Store: bus.ObjectStore(events.BucketLiveTV), Debounce: time.Hour, Now: func() time.Time { return day },
		Fetch:  &http.Client{Transport: answer},
		Notify: func(types.NamespacedName) { notified.Add(1) },
	})
	cfg := Config{
		Key: types.NamespacedName{Namespace: "media", Name: "grids"}, Tuners: 1, Every: time.Hour,
		Blocks:       []BlockConfig{{DeviceID: "dev1", Start: "1"}},
		GuideSources: &GuideSourcesConfig{Token: "tok", Refresh: 24 * time.Hour, Rate: 1000},
	}
	tn.Apply(cfg)
	p := tn.providers[cfg.Key]
	p.mu.Lock()
	if p.renderTimer != nil {
		p.renderTimer.Stop()
		p.renderTimer = nil
	}
	p.mu.Unlock()
	t.Cleanup(func() { tn.Remove(cfg.Key) })
	return tn, p, p.cfg
}

// pass runs one grid pass over want, as dispatch would.
func pass(tn *Tuner, p *provider, cfg Config, want ...string) {
	p.mu.Lock()
	p.grids.want = want
	p.fetching["grids"] = true
	p.mu.Unlock()
	tn.fetchGrids(context.Background(), p, cfg)
}

func renderPending(p *provider) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.renderTimer != nil
}

// A pass that stores no grid leaves the guide as it is: a start-up pass
// that finds every grid fresh, and a pass of failures every retryMax while
// the service is down, render nothing; only a stored grid asks for a
// render.
func TestAPassThatStoresNothingRendersNothing(t *testing.T) {
	grid, err := os.ReadFile("../../../test/data/plexgrid/grid-sample.json")
	require.NoError(t, err)
	var asked atomic.Int32
	var notified atomic.Int32
	tn, p, cfg := gridTuner(t, func(k string) *http.Response {
		asked.Add(1)
		if k == "down" {
			return gridAnswer(http.StatusBadGateway, "down")
		}
		return gridAnswer(http.StatusOK, string(grid))
	}, &notified)

	// A grid the store holds fresh, unseen since the start.
	_, err = tn.putGrid(context.Background(), cfg.Key, "fresh", fixtureGrid(t))
	require.NoError(t, err)
	pass(tn, p, cfg, "fresh")
	assert.Zero(t, asked.Load(), "a fresh stored grid is not asked for")
	assert.False(t, renderPending(p), "nothing stored: no render")

	// The service down: an error, no render; the snapshot says it in place.
	p.mu.Lock()
	p.snap = Snapshot{Channels: map[string]ChannelState{}} // as a render left it
	n := cloneSnapshot(p.snap)
	p.notified = &n
	p.mu.Unlock()
	pass(tn, p, cfg, "down")
	assert.Equal(t, int32(1), asked.Load())
	assert.False(t, renderPending(p), "a failure stores nothing: no render")
	p.mu.Lock()
	assert.Contains(t, p.snap.GuideSources.Error, "Bad Gateway")
	assert.Equal(t, day, p.snap.GuideSources.FetchedAt)
	p.mu.Unlock()
	assert.Equal(t, int32(1), notified.Load(), "the controller is told the state moved")
	pass(tn, p, cfg, "down")
	assert.Equal(t, int32(1), asked.Load(), "retried only after retryMax")
	assert.Equal(t, int32(1), notified.Load(), "a pass that asked nothing changes nothing")
	p.mu.Lock()
	assert.Contains(t, p.snap.GuideSources.Error, "Bad Gateway", "the failure stands until a pass asks again")
	p.mu.Unlock()

	pass(tn, p, cfg, "new")
	assert.True(t, renderPending(p), "a stored grid asks for a render")
}

// A pass whose configuration is replaced while it runs keeps nothing: the
// guide sources gone (or the provider removed) mid-pass, its 429 writes no
// hold, no grid, and no series back.
func TestAPassOfAReplacedConfigurationKeepsNothing(t *testing.T) {
	for _, tc := range []struct {
		name    string
		replace func(tn *Tuner, cfg Config)
	}{
		{"the guide sources gone", func(tn *Tuner, cfg Config) {
			cfg.GuideSources = nil
			tn.Apply(cfg)
		}},
		{"another token", func(tn *Tuner, cfg Config) {
			gs := *cfg.GuideSources
			gs.Token = "another"
			cfg.GuideSources = &gs
			tn.Apply(cfg)
		}},
		{"the provider removed", func(tn *Tuner, cfg Config) { tn.Remove(cfg.Key) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			arrived, gate := make(chan struct{}, 1), make(chan struct{})
			var notified atomic.Int32
			tn, p, cfg := gridTuner(t, func(string) *http.Response {
				arrived <- struct{}{}
				<-gate
				return gridAnswer(http.StatusTooManyRequests, "slow down", "Retry-After", "120")
			}, &notified)
			done := make(chan struct{})
			go func() { pass(tn, p, cfg, "k1", "k2"); close(done) }()
			<-arrived
			tc.replace(tn, cfg)
			close(gate)
			<-done

			p.mu.Lock()
			defer p.mu.Unlock()
			assert.True(t, p.grids.holdUntil.IsZero(), "no hold written back")
			assert.Empty(t, p.grids.fetched)
			assert.Empty(t, p.grids.retry)
			assert.False(t, gridHold.DeleteLabelValues(label(cfg.Key)), "no hold series made again")
			if tc.name == "the provider removed" {
				assert.Zero(t, gridCalls.DeletePartialMatch(map[string]string{"provider": label(cfg.Key)}), "no call series made again")
			}
		})
	}
}

// A forced pass a hold cuts short leaves the grids it did not reach forced:
// once the hold ends they are due, and the ones it stored are not.
func TestAForceOutlivesAHeldPass(t *testing.T) {
	grid, err := os.ReadFile("../../../test/data/plexgrid/grid-sample.json")
	require.NoError(t, err)
	var notified atomic.Int32
	tn, p, cfg := gridTuner(t, func(k string) *http.Response {
		if k == "k2" {
			return gridAnswer(http.StatusTooManyRequests, "slow down", "Retry-After", "0")
		}
		return gridAnswer(http.StatusOK, string(grid))
	}, &notified)
	fresh := gridObject{at: day.Add(-time.Hour)}
	p.mu.Lock()
	p.grids.fetched = map[string]gridObject{"k1": fresh, "k2": fresh, "k3": fresh}
	p.grids.forceAt = day.Add(-time.Minute) // a refresh value since they were stored
	p.mu.Unlock()
	pass(tn, p, cfg, "k1", "k2", "k3")

	p.mu.Lock()
	defer p.mu.Unlock()
	assert.Equal(t, day.Add(minGridHold), p.grids.holdUntil, "a Retry-After of 0 holds a minute")
	assert.False(t, gridStale(p.grids.fetched["k1"], day, time.Hour*24, p.grids.forceAt), "stored by the pass")
	assert.True(t, gridStale(p.grids.fetched["k2"], day, time.Hour*24, p.grids.forceAt), "held: still forced")
	assert.True(t, gridStale(p.grids.fetched["k3"], day, time.Hour*24, p.grids.forceAt), "never reached: still forced")
	assert.False(t, p.gridsDue(day), "not during the hold")
	assert.True(t, p.gridsDue(p.grids.holdUntil), "once the hold ends")
}

func TestPrevForKeepsOnlyThePairingsOfTheLineup(t *testing.T) {
	l := plexguide.Lineup{ID: "lineupaaaa", Stations: []plexguide.Station{{GridKey: "listedgrid"}}}
	for _, c := range []struct {
		name, lineup, id, grid string
		keep                   bool
	}{
		{"recorded lineup is l's, id without prefix", "lineupaaaa", "other-g1", "g1", true},
		{"recorded lineup is another's, id carries l's prefix", "lineupbbbb", "lineupaaaa-g1", "g1", false},
		{"no recorded lineup, prefix matches", "", "lineupaaaa-g1", "g1", true},
		{"no recorded lineup, prefix differs", "", "lineupbbbb-g1", "g1", false},
		{"listed station, recorded lineup another's", "lineupbbbb", "lineupbbbb-listedgrid", "listedgrid", true},
		{"listed station, no recorded lineup, prefix differs", "", "lineupbbbb-listedgrid", "listedgrid", true},
		{"listed station, recorded lineup is l's", "lineupaaaa", "x-listedgrid", "listedgrid", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			ch := plexguide.Channel{Object: "a", Prev: plexguide.Prev{
				Match: plexguide.Automatic, Lineup: c.lineup,
				Station: plexguide.Station{ID: c.id, GridKey: c.grid},
			}}
			got := prevFor(l, []plexguide.Channel{ch})[0].Prev
			if c.keep {
				assert.Equal(t, ch.Prev, got)
			} else {
				assert.Equal(t, plexguide.Prev{}, got)
			}
		})
	}
}
