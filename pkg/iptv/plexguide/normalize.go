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

package plexguide

import (
	"regexp"
	"strings"
	"unicode"
)

// suffixes are cut from a normalized name in this order, each at most once
// and only while something is left, so "Cartoon Network HD" and "CARTOON
// NETWORK" both read "cartoon", and "NewsNation SD" "newsnation".
var suffixes = []string{"hdtv", "hd", "sd", "east", "usa", "network", "channel", "tv"}

// Normalize is a channel or station name as the pairing compares it: lower
// case, "&" as "and", "+" as "plus", a trailing .us, .ca or .uk (an XMLTV
// id's country) cut, nothing but letters and digits, then the suffixes cut.
func Normalize(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	for _, tld := range []string{".us", ".ca", ".uk"} {
		s = strings.TrimSuffix(s, tld)
	}
	s = strings.NewReplacer("&", "and", "+", "plus").Replace(s)
	s = strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r
		}
		return -1
	}, s)
	for _, suf := range suffixes {
		if len(s) > len(suf)+1 && strings.HasSuffix(s, suf) {
			s = strings.TrimSuffix(s, suf)
		}
	}
	return s
}

// baseCallSign is a call sign without its trailing DT or HD: WEAR and
// WEARDT, AETV and AETVHD are one station each.
func baseCallSign(cs string) string {
	cs = strings.ToUpper(strings.TrimSpace(cs))
	for _, suf := range []string{"DT", "HD"} {
		if len(cs) > len(suf)+2 && strings.HasSuffix(cs, suf) {
			return strings.TrimSuffix(cs, suf)
		}
	}
	return cs
}

// broadcastCallSign is a US broadcast station's call sign: K or W and three
// letters.
var broadcastCallSign = regexp.MustCompile(`^[KW][A-Z]{3}$`)

// callSignsIn are the broadcast call signs an XMLTV channel id names: each
// token between non-alphanumerics, or its last four letters
// ("ABCEast_WABC.us" names WABC, "CBSKEYE.us" KEYE).
func callSignsIn(channelID string) []string {
	var out []string
	seen := map[string]bool{}
	for _, tok := range strings.FieldsFunc(channelID, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		tok = strings.ToUpper(tok)
		for _, cand := range []string{tok, tok[max(0, len(tok)-4):]} {
			if broadcastCallSign.MatchString(cand) && !seen[cand] {
				seen[cand] = true
				out = append(out, cand)
			}
		}
	}
	return out
}

// aliases are names Gracenote spells otherwise, as Normalize reads both
// sides: a channel name or guide id on the left, a station's normalized
// title on the right (a title, so a network's SD and HD feeds both answer).
// Only a pairing certain from the owner's lineup (test/data/plexguide)
// goes in.
var aliases = map[string]string{
	"foxsports1":    "fs1",                       // FOX SPORTS 1: FS1
	"foxsports2":    "fs2",                       // FOX SPORTS 2: FS2
	"travel":        "thetravel",                 // TRAVEL CHANNEL: The Travel Channel
	"smithsonian":   "smithsonianhd",             // SMITHSONIAN CHANNEL: Smithsonian HD Network
	"lifetimemovie": "lmn",                       // LIFETIME MOVIE NETWORK: LMN
	"showtime":      "paramountpluswithshowtime", // SHOWTIME: Paramount+ with Showtime
	"lawandcrime":   "lawandcrimestream",         // LAW AND CRIME: Law & Crime Stream
}
