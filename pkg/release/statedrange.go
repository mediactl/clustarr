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

package release

import (
	"regexp"
	"strconv"
)

// statedRangeRE is the episode range a season pack's title states in
// words: "[EP: 14-26]", "(Episodes 01-13)", "Eps 1~12". The word is what
// tells it from a season range, a year range or a resolution.
var statedRangeRE = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:eps?|episodes?)[ .:_]*0*(\d{1,4})\s*[-~]\s*0*(\d{1,4})(?:$|[^0-9])`)

// statedEpisodes is the range of episodes a season pack's title says it
// holds, when it says so: a season released in parts is posted as one pack
// per part, each named for the season and its own episodes ("(Season 1 |
// Part 02 [EP: 14-26])").
//
// Until 2026-11-27 such a pack read as the whole season. On talos EMBER's
// second part of How a Realist Hero Rebuilt the Kingdom, the one release
// that held episodes 14 to 26, overlapped every whole-season pack ranked
// above it and was refused with them; read as the episodes it names, a
// part overlaps no other part, and the grab planner takes each for its
// own.
func statedEpisodes(title string) (first, last int, ok bool) {
	m := statedRangeRE.FindStringSubmatch(title)
	if m == nil {
		return 0, 0, false
	}
	first, err1 := strconv.Atoi(m[1])
	last, err2 := strconv.Atoi(m[2])
	if err1 != nil || err2 != nil || first < 1 || last <= first || !validEpisodeRange(first, last) {
		return 0, 0, false
	}
	return first, last, true
}

// stateEpisodes makes a one-season pack whose title states its episodes
// (statedEpisodes) a pack of those episodes: no longer the whole season,
// and Partial, as a "Part 2" pack already was.
func stateEpisodes(p *ParsedRelease, title string) {
	if len(p.Seasons) != 1 || len(p.Episodes) > 0 || len(p.Absolute) > 0 {
		return
	}
	first, last, ok := statedEpisodes(title)
	if !ok {
		return
	}
	p.Episodes, p.FullSeason, p.Partial = intRange(first, last), false, true
}
