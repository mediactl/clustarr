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
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/types"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// episodeSyncMemo remembers, per Series UID, the inputs of its last complete
// episode fan-out in this process, so a pass that moved none of them does not
// fetch the episode list again (owner, 2026-10-28: every Series pass asked
// the metadata gateway for its whole list and patched every Episode; 11 of
// the remediation loop's 16 workers waited on that fetch). A restart forgets
// it, so each Series fans out once per process.
type episodeSyncMemo struct {
	mu   sync.Mutex
	last map[types.UID]episodeSyncKey
}

// episodeSyncKey is what decides a fan-out's outcome besides the provider's
// list: the spec (generation), the metadata refresh it read and whether the
// add options were applied (the first fan-out's monitoring).
type episodeSyncKey struct {
	generation        int64
	refreshedAt       time.Time
	addOptionsApplied bool
}

func episodeSyncKeyOf(s *catalogv1alpha1.Series) (episodeSyncKey, bool) {
	if s.Status.Metadata == nil {
		return episodeSyncKey{}, false
	}
	return episodeSyncKey{
		generation: s.Generation, refreshedAt: s.Status.Metadata.RefreshedAt.UTC(), addOptionsApplied: s.Status.AddOptionsApplied,
	}, true
}

// due reports whether s's episodes must be fetched: never fanned out in this
// process, or an input moved since; always while s has no metadata.
func (m *episodeSyncMemo) due(s *catalogv1alpha1.Series) bool {
	k, ok := episodeSyncKeyOf(s)
	if !ok {
		return true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	last, seen := m.last[s.UID]
	return !seen || last != k
}

// synced records a complete fan-out of s.
func (m *episodeSyncMemo) synced(s *catalogv1alpha1.Series) {
	k, ok := episodeSyncKeyOf(s)
	if !ok {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.last == nil {
		m.last = map[types.UID]episodeSyncKey{}
	}
	m.last[s.UID] = k
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
