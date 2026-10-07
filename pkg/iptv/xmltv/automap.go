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

// Index answers AutoMap for many channels at once: built once per set of
// guides, each lookup is a map read rather than a scan of every channel.
type Index struct {
	byID   map[string]Mapping // the first guide (in name order) holding the id
	byNorm map[string]Mapping // the first channel whose display name normalises to the key
}

// NewIndex indexes guides for AutoMap's rules: an id match first, then a
// display name equal under release.TitleNorm, guides taken in name order.
func NewIndex(guides map[string]Guide) *Index {
	names := make([]string, 0, len(guides))
	for n := range guides {
		names = append(names, n)
	}
	slices.Sort(names)
	ix := &Index{byID: map[string]Mapping{}, byNorm: map[string]Mapping{}}
	for _, gn := range names {
		for _, c := range guides[gn].Channels {
			if _, ok := ix.byID[c.ID]; !ok {
				ix.byID[c.ID] = Mapping{Guide: gn, ChannelID: c.ID}
			}
			for _, dn := range c.DisplayNames {
				n := release.TitleNorm(dn)
				if _, ok := ix.byNorm[n]; !ok && n != "" {
					ix.byNorm[n] = Mapping{Guide: gn, ChannelID: c.ID}
				}
			}
		}
	}
	return ix
}

// AutoMap is the package AutoMap over the indexed guides.
func (ix *Index) AutoMap(tvgID, name string) (Mapping, bool) {
	if tvgID != "" {
		if m, ok := ix.byID[tvgID]; ok {
			return m, true
		}
	}
	if n := release.TitleNorm(name); n != "" {
		if m, ok := ix.byNorm[n]; ok {
			return m, true
		}
	}
	return Mapping{}, false
}

// AutoMap finds a guide channel for a playlist entry: first a channel whose
// id is the entry's tvg-id, then one with a display name equal to the
// entry's name under release.TitleNorm. Guides are searched in name order,
// so the answer is stable.
func AutoMap(tvgID, name string, guides map[string]Guide) (Mapping, bool) {
	return NewIndex(guides).AutoMap(tvgID, name)
}
