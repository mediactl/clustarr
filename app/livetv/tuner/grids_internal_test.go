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
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
		{"forced", func(p *provider) {
			p.grids.fetched = map[string]gridObject{"k": {at: day}}
			p.grids.force = true
		}, true},
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
