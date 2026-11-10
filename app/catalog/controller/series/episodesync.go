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

package series

import (
	"time"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/remediation/childsync"
)

// syncKey is the child-sync memo's key for s (app/remediation/childsync,
// the one memo every container keeps; until 2026-11-10 this package kept
// an episodeSyncMemo of its own, owner 2026-10-28: every Series pass asked
// the metadata gateway for its whole list and patched every Episode, and
// 11 of the remediation loop's 16 workers waited on that fetch): its spec
// generation and its metadata refresh. status.addOptionsApplied is no
// input, though DesiredEpisodes reads it: the memo records only a complete
// fan-out, the first of which applies the add-time mode and flips the flag
// in the same pass (reconcileNormal sends stored || fan.synced), so the key
// carries no flag (childsync.Key, which carried one until 2026-11-10)
// whatever the pass read -- a pass reading false after that
// fan-out (an Episode's own Create wakes the Series before the flip's apply
// reaches the cache; or the apply conflicted) skips the fetch, which would
// create nothing, and sends the flip again (2026-11-10: keyed on the read
// value, TestArtistItemPath's and TestAuthorItemPath's fan-outs fetched
// twice). A series without metadata has no key: its fan-out waits for the
// document (syncEpisodes).
func syncKey(s *catalogv1alpha1.Series) (childsync.Key, bool) {
	if s.Status.Metadata == nil {
		return childsync.Key{}, false
	}
	return childsync.KeyAt(s.Generation, s.Status.Metadata.RefreshedAt.Time), true
}

// providerStatusMatches reports whether ep already holds every provider field
// ensureEpisode would apply for d, plexID being the one it would send ("" for
// none), so the apply would change nothing: a value d omits must be absent
// too, since applying would release it.
func providerStatusMatches(ep *catalogv1alpha1.Episode, d DesiredEpisode, plexID string) bool {
	st := ep.Status
	if st.Title != d.Title || st.Overview != d.Overview || st.RuntimeMinutes != d.RuntimeMinutes ||
		st.TvdbID != d.TvdbID || st.PlexID != plexID {
		return false
	}
	switch {
	case d.AirDate == nil && st.AirDate != nil, d.AirDate != nil && st.AirDate == nil:
		return false
	case d.AirDate != nil && !st.AirDate.UTC().Equal(d.AirDate.UTC().Truncate(time.Second)):
		return false
	}
	switch {
	case d.AbsoluteNumber == nil && st.AbsoluteNumber != nil, d.AbsoluteNumber != nil && st.AbsoluteNumber == nil:
		return false
	case d.AbsoluteNumber != nil && *st.AbsoluteNumber != *d.AbsoluteNumber:
		return false
	}
	if d.Still == "" {
		return len(st.Images) == 0
	}
	return len(st.Images) == 1 && st.Images[0].Type == catalogv1alpha1.ImageTypeScreenshot && st.Images[0].URL == d.Still
}
