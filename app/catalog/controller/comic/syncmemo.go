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

package comic

import (
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/types"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// issueSyncMemo remembers, per Comic UID, the inputs of its last complete
// issue fan-out in this process, so a pass that moved none of them does not
// fetch the issue list again (2026-11-10 design D2, D14: the Comic
// re-fetched its issues by RPC on every pass, and every Issue's hasFile or
// downloadPhase flip woke it). A restart forgets it, so each Comic fans
// out once per process.
//
// It is the Series' episodeSyncMemo (series/episodesync.go) in shape, and
// the shape W1.1 of the design's plan gives the shared childSyncMemo in
// app/remediation/childsync.go (keyed by UID; due when the spec generation,
// metadata.refreshedAt or the add options moved since the last complete
// fan-out). W1.1 had not landed it on main when this was written, so this
// local copy stands in; W6 replaces it with the shared one.
type issueSyncMemo struct {
	mu   sync.Mutex
	last map[types.UID]issueSyncKey
}

// issueSyncKey is what decides a fan-out's outcome besides the provider's
// list: the spec (generation), the metadata refresh it read and whether the
// add options were applied (the first fan-out's monitoring).
type issueSyncKey struct {
	generation        int64
	refreshedAt       time.Time
	addOptionsApplied bool
}

// issueSyncKeyOf is c's key with addOptionsApplied as given: the stored
// flag when asking whether a fan-out is due, and true when recording a
// complete one, since the apply that follows it records the flag true --
// recording the stored (still false) flag would make the next pass due
// again for the flag's flip alone, one fetch for nothing at every add.
func issueSyncKeyOf(c *catalogv1alpha1.Comic, addOptionsApplied bool) (issueSyncKey, bool) {
	if c.Status.Metadata == nil {
		return issueSyncKey{}, false
	}
	return issueSyncKey{
		generation: c.Generation, refreshedAt: c.Status.Metadata.RefreshedAt.UTC(), addOptionsApplied: addOptionsApplied,
	}, true
}

// due reports whether c's issues must be fetched: never fanned out in this
// process, or an input moved since; always while c has no metadata.
func (m *issueSyncMemo) due(c *catalogv1alpha1.Comic) bool {
	k, ok := issueSyncKeyOf(c, c.Status.AddOptionsApplied)
	if !ok {
		return true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	last, seen := m.last[c.UID]
	return !seen || last != k
}

// synced records a complete fan-out of c, after which the add options read
// as applied.
func (m *issueSyncMemo) synced(c *catalogv1alpha1.Comic) {
	k, ok := issueSyncKeyOf(c, true)
	if !ok {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.last == nil {
		m.last = map[types.UID]issueSyncKey{}
	}
	m.last[c.UID] = k
}
