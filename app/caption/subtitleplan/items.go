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

package subtitleplan

import (
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/subtitles"
)

// planItems decides which items the block keeps (§6.4.4): an item for a
// profile key that is wanted or has a subtitle on disk (downloadedAt or
// name), re-adopting one a profile edit dropped so its history survives a
// language dropped and restored; one appended for every wanted key with
// none; the rest returned as dropped, which the caller withdraws when a
// dispatch is in flight. A profile language the plan no longer wants and
// that never downloaded a subtitle is dropped too: audioExclude now matching
// the file's audio or its item's original language leaves nothing to search
// for. Order is preserved and new items are appended.
func planItems(items []catalogv1alpha1.SubtitleItemStatus, profileKeys map[string]bool,
	wanted []subtitles.LangKey,
) (kept, dropped []catalogv1alpha1.SubtitleItemStatus) {
	want := make(map[string]bool, len(wanted))
	for _, k := range wanted {
		want[string(k)] = true
	}
	have := make(map[string]bool, len(items))
	for _, it := range items {
		neverDownloaded := it.DownloadedAt == nil && it.Name == ""
		if !profileKeys[it.LangKey] || (!want[it.LangKey] && neverDownloaded) {
			dropped = append(dropped, it)
			continue
		}
		kept = append(kept, *it.DeepCopy())
		have[it.LangKey] = true
	}
	for _, k := range wanted {
		if !have[string(k)] {
			kept = append(kept, catalogv1alpha1.SubtitleItemStatus{LangKey: string(k), State: catalogv1alpha1.SubtitleStatePending})
			have[string(k)] = true
		}
	}
	return kept, dropped
}
