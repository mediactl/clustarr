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

package subtitlerequest

import (
	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	"github.com/mediactl/clustarr/app/caption/status"
	"github.com/mediactl/clustarr/pkg/subtitles"
)

// planItems decides which items this apply declares, under the item-liveness
// protocol (app/caption/status.IsLive): every item returned is one this
// controller wants, and the caller gives each a non-empty nextSearchAt before
// the apply. It keeps every item for
// a language the (override-filtered) profile still has -- re-adopting one an
// earlier apply released, so its worker-written history survives a profile
// edit that drops a language and restores it -- creates one for every wanted
// language that has none, and returns the langKeys of the live items it
// leaves out, which this apply withdraws by not sending them. Creating is
// legal because state is optional: an item with no state is "planned, never
// searched".
//
// Order is preserved and new items are appended, so an unchanged plan
// renders an unchanged list.
func planItems(items []subtitlev1alpha1.SubtitleItem, profileKeys map[string]bool,
	wanted []subtitles.LangKey,
) (kept []subtitlev1alpha1.SubtitleItem, dropped []string) {
	have := make(map[string]bool, len(items))
	for _, it := range items {
		if !profileKeys[it.LangKey] {
			if status.IsLive(it) {
				dropped = append(dropped, it.LangKey) // withdrawn by this apply
			}
			continue
		}
		kept = append(kept, it)
		have[it.LangKey] = true
	}
	for _, k := range wanted {
		if !have[string(k)] {
			kept = append(kept, subtitlev1alpha1.SubtitleItem{LangKey: string(k)})
			have[string(k)] = true
		}
	}
	return kept, dropped
}
