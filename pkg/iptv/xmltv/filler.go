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

package xmltv

import (
	"regexp"
	"strings"
)

// Filler reports whether a programme's inner XML is a placeholder a guide
// source lists where it has nothing to say: "Paid Programming", "SIGN OFF",
// "Channel No Longer Available", "No EVENT Today", "No Data", "To Be
// Announced" and their like. BuildGuideWith leaves such a programme out of
// the guide (owner, 2026-11-16: Plex's What's On hubs list whatever is on,
// and a sixth of what was on at any moment on talos was one of these --
// "SIGN OFF" on fifteen sports channels, "Paid Programming" on eighty-nine).
// Nothing but the airing itself puts a listing in those hubs, so no tag
// could be taken from it instead: the channel's row in the guide is empty
// for that time, as it is wherever a source lists nothing.
//
// The rule reads the title alone, compared without case or punctuation. A
// word that is also a real title ("Blackout", "Movie") counts only on a
// listing that says nothing else: no description and no episode title.
func Filler(inner []byte) bool {
	elems, ok := splitElements(inner)
	if !ok || len(elems) == 0 {
		return false
	}
	f := facts(elems)
	title := fillerNorm.ReplaceAllString(strings.ToLower(f.title), " ")
	title = strings.TrimSpace(title)
	if title == "" {
		return false
	}
	if fillerTitles[title] || fillerPattern.MatchString(title) {
		return true
	}
	// A source that has only the title to give repeats it as the episode's
	// ("Blackout", "Blackout"): that says nothing else either.
	subtitle := strings.TrimSpace(fillerNorm.ReplaceAllString(strings.ToLower(f.subtitle), " "))
	return fillerBare[title] && f.desc == "" && (subtitle == "" || subtitle == title)
}

var (
	// fillerNorm is what a title is compared without: anything but letters
	// and digits, each run read as one space.
	fillerNorm = regexp.MustCompile(`[^\p{L}\p{N}]+`)

	// fillerTitles are whole titles, normalised, that are never a programme.
	fillerTitles = set(
		"paid programming", "paid program", "paid programme", "paid programs", "paid programmes",
		"infomercial", "infomercials",
		"sign off", "signoff", "off air", "off the air", "channel off air",
		"no data", "no data available", "no information", "no information available", "no info", "no info available",
		"no programming", "no programming available", "no program", "no programme", "no schedule", "no schedule available",
		"no guide data", "no guide data available", "no listing", "no listings", "no listings available",
		"to be announced", "programming to be announced", "tba", "tbd", "to be determined",
		"channel no longer available", "no longer available", "channel not available", "not available",
		"program unavailable", "programme unavailable", "programming unavailable",
	)

	// fillerPattern are the shapes a source varies: "No EVENT Today",
	// "Marlins Baseball Sign Off", "Cricket Filler", "Upcoming - MLB Network
	// Strike Zone" (an event channel between events), "Paid Programming 1
	// Inspiration Ministries".
	fillerPattern = regexp.MustCompile(`^no (event|events|game|games|match|matches|race|races|fight|fights)( scheduled)?( today| tonight| available)?$` +
		`| sign off$| no longer available$| filler$|^upcoming |^paid program(ming|me|mes|s)? `)

	// fillerBare are placeholders that are also real titles.
	fillerBare = set("blackout", "black out", "programming", "movie", "film", "event", "live event", "program", "programme")
)

func set(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}
