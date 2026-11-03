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

package decision

import (
	"slices"

	"github.com/mediactl/clustarr/pkg/dubs"
	"github.com/mediactl/clustarr/pkg/lang"
	"github.com/mediactl/clustarr/pkg/quality"
)

// ForDub is p's audio policy for an item whose English dub the MAL-Dubs
// list knows as s (pkg/dubs; owner, 2026-11-03: English dubs and dual audio
// "*only* for series who's status.externalIDs.mal exists within" the list).
// NotDubbed drops English from the languages the policy wants, so nothing
// searches for a dub, a donor or dual audio that does not exist; Incomplete
// (a simuldub) moves it to Profile.AudioOptional, so a file without it meets
// the cutoff while a release with it still ranks first and a donor search
// keeps looking; Dubbed and Ungoverned leave p as it is. An item whose
// original language is English still wants it, through "original", and a
// policy of English alone is left alone: it is not a dub's.
func ForDub(p quality.Profile, s dubs.State) quality.Profile {
	if s != dubs.NotDubbed && s != dubs.Incomplete {
		return p
	}
	var keep, english []string
	for _, l := range p.AudioLanguages {
		if t, ok := lang.Normalize(l); ok && l != "original" && baseLanguage(string(t)) == "en" {
			english = append(english, l)
			continue
		}
		keep = append(keep, l)
	}
	if len(english) == 0 || len(keep) == 0 {
		return p
	}
	p.AudioLanguages = keep
	p.AudioOptional = nil
	if s == dubs.Incomplete {
		p.AudioOptional = slices.Clone(english)
	}
	return p
}
