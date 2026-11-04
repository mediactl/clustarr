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
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/iptv/plexguide"
)

var now = time.Date(2026, 11, 3, 20, 0, 0, 0, time.UTC)

func station(grid, callSign, title, vcn string, hd bool) plexguide.Station {
	return plexguide.Station{ID: "lineup-" + grid, GridKey: grid, CallSign: callSign, Title: title, VCN: vcn, HD: hd}
}

// small is a lineup of a few stations, two of them one network's feeds.
func small() plexguide.Lineup {
	return plexguide.Lineup{ID: "lineup", Stations: []plexguide.Station{
		station("aaaaaaaaaaaaaaaaaaaaaaa1", "WEAR", "ABC", "003", false),
		station("aaaaaaaaaaaaaaaaaaaaaaa2", "WEARDT", "ABC", "003", true),
		station("aaaaaaaaaaaaaaaaaaaaaaa3", "WABCDT", "ABC", "396", true),
		station("aaaaaaaaaaaaaaaaaaaaaaa4", "FX", "FX", "248", false),
		station("aaaaaaaaaaaaaaaaaaaaaaa5", "FXHD", "FX HD", "248", true),
		station("aaaaaaaaaaaaaaaaaaaaaaa6", "ESPNHD", "ESPN HD", "206", true),
	}}
}

func TestACallSignInTheGuideIDDecides(t *testing.T) {
	p := plexguide.Pair(directvMobile(t), []plexguide.Channel{{Object: "abc", Number: "1001", Name: "ABC", ChannelID: "ABCEast_WABC.us"}}, now)["abc"]
	assert.Equal(t, plexguide.Automatic, p.Match)
	assert.Equal(t, "WABCDT", p.Station.CallSign)
	assert.Equal(t, "396", p.Station.VCN)
}

func TestANetworkNameAloneIsAmbiguous(t *testing.T) {
	p := plexguide.Pair(small(), []plexguide.Channel{{Object: "abc", Number: "1001", Name: "ABC"}}, now)["abc"]
	assert.Equal(t, plexguide.Ambiguous, p.Match, "WEAR and WABC are both titled ABC")
	require.Len(t, p.Candidates, 2)
	assert.Equal(t, "WEARDT", p.Candidates[0].CallSign, "by number")
	assert.Equal(t, "WABCDT", p.Candidates[1].CallSign)
}

func TestANetworkNameIsNeverGuessedFromTheCloudLineup(t *testing.T) {
	// The cloud titles a station by its call sign ("WEAR-DT") and names its
	// network apart (affiliateCallSign), which pairs nothing: matching it
	// would put New York's WNET on Alabama Public Television's schedule.
	p := plexguide.Pair(directvMobile(t), []plexguide.Channel{
		{Object: "abc", Number: "1001", Name: "ABC"},
		{Object: "pbs", Number: "1005", Name: "PBS", ChannelID: "PBSWNET.us"},
	}, now)
	assert.False(t, p["abc"].Match.OnPlex())
	assert.Equal(t, plexguide.NotInLineup, p["pbs"].Match)
}

func TestNamesMatchAfterNormalising(t *testing.T) {
	out := plexguide.Pair(directvMobile(t), []plexguide.Channel{
		{Object: "ae", Number: "1070", Name: "A&E", ChannelID: "AandENetwork.us"},
		{Object: "apl", Number: "1076", Name: "ANIMAL PLANET", ChannelID: "AnimalPlanet.us"},
		{Object: "fs1", Number: "1291", Name: "FOX SPORTS 1", ChannelID: "FoxSports1.us"},
	}, now)
	assert.Equal(t, "AETVHD", out["ae"].Station.CallSign)
	assert.Equal(t, "APLHD", out["apl"].Station.CallSign)
	assert.Equal(t, "FS1HD", out["fs1"].Station.CallSign, "through the aliases")
}

func TestOneNetworksFeedsAreOneStationServedInHD(t *testing.T) {
	out := plexguide.Pair(small(), []plexguide.Channel{{Object: "fx", Number: "1", Name: "FX"}, {Object: "wear", Number: "2", Name: "x", ChannelID: "WEAR.us"}}, now)
	assert.Equal(t, plexguide.Automatic, out["fx"].Match, "FX and FXHD are one network, not two stations")
	assert.Equal(t, "FXHD", out["fx"].Station.CallSign)
	assert.Equal(t, "WEARDT", out["wear"].Station.CallSign, "WEAR and WEARDT are one station: its HD feed")
}

func TestAManualStationTakesItFromAnAutomaticOne(t *testing.T) {
	out := plexguide.Pair(small(), []plexguide.Channel{
		{Object: "auto", Number: "1", Name: "ESPN"},
		{Object: "hand", Number: "2", Name: "Sports", Station: "aaaaaaaaaaaaaaaaaaaaaaa6"},
	}, now)
	assert.Equal(t, plexguide.Manual, out["hand"].Match)
	assert.Equal(t, plexguide.Taken, out["auto"].Match)
	assert.Equal(t, "hand", out["auto"].TakenBy)
}

func TestTwoAutomaticClaimsGoToTheLowerNumber(t *testing.T) {
	out := plexguide.Pair(small(), []plexguide.Channel{
		{Object: "b", Number: "1010", Name: "ESPN"},
		{Object: "a", Number: "1002", Name: "ESPN HD"},
	}, now)
	assert.Equal(t, plexguide.Automatic, out["a"].Match)
	assert.Equal(t, plexguide.Taken, out["b"].Match)
	assert.Equal(t, "a", out["b"].TakenBy)
}

func TestATimeshiftedChannelIsNeverPaired(t *testing.T) {
	out := plexguide.Pair(small(), []plexguide.Channel{
		{Object: "auto", Number: "1", Name: "ESPN", Timeshifted: true},
		{Object: "hand", Number: "2", Name: "x", Station: "aaaaaaaaaaaaaaaaaaaaaaa6", Timeshifted: true},
	}, now)
	assert.Equal(t, plexguide.Timeshifted, out["auto"].Match)
	assert.Equal(t, plexguide.Timeshifted, out["hand"].Match, "a station chosen by hand too")
}

func TestOffIsOff(t *testing.T) {
	p := plexguide.Pair(small(), []plexguide.Channel{{Object: "a", Number: "1", Name: "ESPN", Off: true}}, now)["a"]
	assert.Equal(t, plexguide.Off, p.Match)
	assert.False(t, p.Match.OnPlex())
}

func TestAManualStationNotInTheLineup(t *testing.T) {
	p := plexguide.Pair(small(), []plexguide.Channel{{Object: "a", Number: "1", Name: "x", Station: "ffffffffffffffffffffffff"}}, now)["a"]
	assert.Equal(t, plexguide.NotInLineup, p.Match)
}

func TestAnEmptyLineupIsNoLineup(t *testing.T) {
	p := plexguide.Pair(plexguide.Lineup{ID: "x"}, []plexguide.Channel{{Object: "a", Number: "1", Name: "ESPN"}}, now)["a"]
	assert.Equal(t, plexguide.NoLineup, p.Match)
}

func TestAPairSticksWhileItsStationIsListed(t *testing.T) {
	l := small()
	prev := plexguide.Prev{Match: plexguide.Automatic, Station: l.Stations[3]} // FX, SD
	p := plexguide.Pair(l, []plexguide.Channel{{Object: "a", Number: "1", Name: "ESPN", Prev: prev}}, now)["a"]
	assert.Equal(t, plexguide.Automatic, p.Match)
	assert.Equal(t, "FX", p.Station.CallSign, "the name now matching ESPN moves nothing")
}

func TestAMissingStationKeepsItsChannelForSevenDays(t *testing.T) {
	gone := station("dddddddddddddddddddddddd", "GONE", "Gone", "999", true)
	ch := func(since time.Time) []plexguide.Channel {
		return []plexguide.Channel{{Object: "a", Number: "1", Name: "nothing", Prev: plexguide.Prev{Match: plexguide.Automatic, Station: gone, MissingSince: since}}}
	}
	p := plexguide.Pair(small(), ch(now.Add(-6*24*time.Hour)), now)["a"]
	assert.Equal(t, plexguide.Automatic, p.Match)
	assert.Equal(t, "GONE", p.Station.CallSign, "still served: its id and number come from the status")
	assert.Equal(t, now.Add(-6*24*time.Hour), p.MissingSince)

	p = plexguide.Pair(small(), ch(time.Time{}), now)["a"]
	assert.Equal(t, now, p.MissingSince, "absent for the first time: since now")

	p = plexguide.Pair(small(), ch(now.Add(-8*24*time.Hour)), now)["a"]
	assert.Equal(t, plexguide.NotInLineup, p.Match, "past the grace the rules decide")
}

func TestTheOwnersChannelsPair(t *testing.T) {
	b, err := os.ReadFile("../../../test/data/plexguide/argon-channels.json")
	require.NoError(t, err)
	var raw []struct{ Number, Name, ChannelID string }
	require.NoError(t, json.Unmarshal(b, &raw))
	var chans []plexguide.Channel
	for _, r := range raw {
		chans = append(chans, plexguide.Channel{Object: r.Number, Number: r.Number, Name: r.Name, ChannelID: r.ChannelID})
	}
	out := plexguide.Pair(directvMobile(t), chans, now)
	held := map[string]string{}
	paired := 0
	for obj, p := range out {
		if !p.Match.OnPlex() {
			continue
		}
		paired++
		other, dup := held[p.Station.GridKey]
		assert.False(t, dup, "station %s held by %s and %s", p.Station.CallSign, obj, other)
		held[p.Station.GridKey] = obj
	}
	assert.GreaterOrEqual(t, paired, 165)
	for number, cs := range map[string]string{"1001": "WABCDT", "1002": "WCBSDT", "1003": "WNBCDT", "1004": "WNYWDT"} {
		assert.Equal(t, cs, out[number].Station.CallSign, number)
	}
}

// TestUndoingAHandPickRunsTheRules: a hand-picked station set back to
// automatic is paired by the rules, not kept as the last pairing was.
func TestUndoingAHandPickRunsTheRules(t *testing.T) {
	l := small()
	prev := plexguide.Prev{Match: plexguide.Manual, Station: l.Stations[3]} // FX, picked by hand
	p := plexguide.Pair(l, []plexguide.Channel{{Object: "a", Number: "1", Name: "ESPN", Prev: prev}}, now)["a"]
	assert.Equal(t, plexguide.Automatic, p.Match)
	assert.Equal(t, "ESPNHD", p.Station.CallSign)
}
