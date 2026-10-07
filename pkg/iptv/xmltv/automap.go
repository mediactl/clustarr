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
	"slices"

	"github.com/mediactl/clustarr/pkg/release"
)

// AutoMap finds a guide channel for a playlist entry: first a channel whose
// id is the entry's tvg-id, then one with a display name equal to the
// entry's name under release.TitleNorm. Guides are searched in name order,
// so the answer is stable.
func AutoMap(tvgID, name string, guides map[string]Guide) (Mapping, bool) {
	names := make([]string, 0, len(guides))
	for n := range guides {
		names = append(names, n)
	}
	slices.Sort(names)
	if tvgID != "" {
		for _, gn := range names {
			for _, c := range guides[gn].Channels {
				if c.ID == tvgID {
					return Mapping{Guide: gn, ChannelID: c.ID}, true
				}
			}
		}
	}
	want := release.TitleNorm(name)
	if want == "" {
		return Mapping{}, false
	}
	for _, gn := range names {
		for _, c := range guides[gn].Channels {
			for _, dn := range c.DisplayNames {
				if release.TitleNorm(dn) == want {
					return Mapping{Guide: gn, ChannelID: c.ID}, true
				}
			}
		}
	}
	return Mapping{}, false
}
