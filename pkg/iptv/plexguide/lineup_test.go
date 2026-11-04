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

package plexguide_test

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/iptv/plexguide"
)

// directvMobile is Plex's DIRECTV Mobile lineup as epg.provider.plex.tv
// answered it on 2026-11-03 (the first 1,000 of its 1,009 stations).
func directvMobile(t *testing.T) plexguide.Lineup {
	t.Helper()
	f, err := os.Open("../../../test/data/plexguide/directv-mobile-channels.json")
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	st, err := plexguide.DecodeChannels(f)
	require.NoError(t, err)
	return plexguide.Lineup{ID: "657c8183afd0f02cd353633f", Title: "DIRECTV Mobile (1009 channels)", Stations: st}
}

func TestDecodeChannelsReadsTheCloudShape(t *testing.T) {
	l := directvMobile(t)
	require.Len(t, l.Stations, 1000)
	var wkrg plexguide.Station
	for _, s := range l.Stations {
		if s.CallSign == "WKRGDT" {
			wkrg = s
		}
	}
	assert.Equal(t, "5fc70603336b43002e137ec6", wkrg.GridKey)
	assert.Equal(t, "657c8183afd0f02cd353633f-5fc70603336b43002e137ec6", wkrg.ID)
	assert.Equal(t, "005", wkrg.VCN)
	assert.True(t, wkrg.HD)
	assert.Equal(t, "CBS", wkrg.AffiliateCallSign)
}

func TestDecodeChannelsLeavesHiddenStationsOut(t *testing.T) {
	st, err := plexguide.DecodeChannels(strings.NewReader(`{"MediaContainer":{"Channel":[
		{"id":"l-a","gridKey":"a","callSign":"AAA","vcn":"1","hidden":false},
		{"id":"l-b","gridKey":"b","callSign":"BBB","vcn":"2","hidden":true}]}}`))
	require.NoError(t, err)
	require.Len(t, st, 1)
	assert.Equal(t, "AAA", st[0].CallSign)
}

func TestDecodeTitleReadsTheLineup(t *testing.T) {
	title, err := plexguide.DecodeTitle(strings.NewReader(`{"MediaContainer":{"Lineup":[{"id":"657c8183afd0f02cd353633f","title":"DIRECTV Mobile (1009 channels)","type":"satellite"}]}}`))
	require.NoError(t, err)
	assert.Equal(t, "DIRECTV Mobile (1009 channels)", title)
	_, err = plexguide.DecodeTitle(strings.NewReader(`{"MediaContainer":{"size":0}}`))
	assert.ErrorIs(t, err, plexguide.ErrNoLineup)
}

func TestLineupURIEscapesAsPMSDoes(t *testing.T) {
	// PMS's own lineup list (GET /livetv/epg/countries/usa/
	// tv.plex.providers.epg.cloud/lineups?postalCode=32548), recorded.
	assert.Equal(t, "lineup://tv.plex.providers.epg.cloud/657c8183afd0f02cd353633f#DIRECTV%20Mobile%20%281009%20channels%29",
		plexguide.LineupURI("657c8183afd0f02cd353633f", "DIRECTV Mobile (1009 channels)"))
	uri := plexguide.LineupURI("657c8183afd0f02cd353633f", "A&B #1 + C")
	_, title, _ := strings.Cut(uri, "#")
	assert.Equal(t, "A%26B%20%231%20%2B%20C", title, "a title's &, # and + are escaped, so the fragment is one")
}

func TestURLsNameTheCloudGuide(t *testing.T) {
	assert.Equal(t, "https://epg.provider.plex.tv/lineups/657c8183afd0f02cd353633f", plexguide.LineupURL("657c8183afd0f02cd353633f"))
	assert.Equal(t, "https://epg.provider.plex.tv/lineups/657c8183afd0f02cd353633f/channels", plexguide.ChannelsURL("657c8183afd0f02cd353633f"))
}

func TestNormalize(t *testing.T) {
	for in, want := range map[string]string{
		"A&E":                   "aande",
		"Cartoon Network HD":    "cartoon",
		"CARTOON NETWORK":       "cartoon",
		"NewsNation SD":         "newsnation",
		"ABCEast_WABC.us":       "abceastwabc",
		"MeTV+":                 "metvplus",
		"The Travel Channel HD": "thetravel",
		"TV":                    "tv",
	} {
		assert.Equal(t, want, plexguide.Normalize(in), in)
	}
}
