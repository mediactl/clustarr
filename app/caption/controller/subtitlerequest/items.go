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
// A profile language the plan no longer wants and that never downloaded a
// subtitle (no downloadedAt, no path) is withdrawn too: audioExclude now
// matching the file's audio or its item's original language leaves nothing
// to search for, and kept, the item read as a search in flight forever --
// every request phase Searching (1,433 of the owner's, 2026-09-30). Only
// its search backoff is lost if the language is wanted again. An item that
// did download is kept: its subtitle is on disk and upgrades still judge it.
//
// Order is preserved and new items are appended, so an unchanged plan
// renders an unchanged list.
func planItems(items []subtitlev1alpha1.SubtitleItem, profileKeys map[string]bool,
	wanted []subtitles.LangKey,
) (kept []subtitlev1alpha1.SubtitleItem, dropped []string) {
	want := make(map[string]bool, len(wanted))
	for _, k := range wanted {
		want[string(k)] = true
	}
	have := make(map[string]bool, len(items))
	for _, it := range items {
		neverDownloaded := it.DownloadedAt == nil && it.Path == ""
		if !profileKeys[it.LangKey] || (!want[it.LangKey] && neverDownloaded) {
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
