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

package engine

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/mediactl/clustarr/pkg/download"
)

var none = map[string]struct{}{}

// TestOrphanClockUsesTheTransfersOwnAge is the carried item's fix: an orphan
// whose age the client knows is due the first pass it is seen, once that age
// exceeds the grace -- a restarted engine (a fresh clock) no longer grants it
// a whole new grace period.
func TestOrphanClockUsesTheTransfersOwnAge(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	var fresh OrphanClock // a restarted engine: no memory of any sighting
	old := download.Item{ID: "old", AddedAt: now.Add(-time.Hour)}
	young := download.Item{ID: "young", AddedAt: now.Add(-time.Minute)}

	assert.Equal(t, []string{"old"}, fresh.Due([]download.Item{old, young}, none, now, 10*time.Minute),
		"an orphan older than the grace is due on the first pass after a restart")

	// The young one stays protected by its own age however often it is seen.
	for i := range 5 {
		assert.Empty(t, fresh.Due([]download.Item{young}, none, now.Add(time.Duration(i)*time.Minute), 10*time.Minute))
	}
	assert.Equal(t, []string{"young"}, fresh.Due([]download.Item{young}, none, now.Add(9*time.Minute), 10*time.Minute))
}

// TestOrphanClockNeverReapsAMatchedTransfer holds however old it is.
func TestOrphanClockNeverReapsAMatchedTransfer(t *testing.T) {
	now := time.Now()
	var c OrphanClock
	item := download.Item{ID: "live", AddedAt: now.Add(-24 * time.Hour)}
	assert.Empty(t, c.Due([]download.Item{item}, map[string]struct{}{"live": {}}, now, time.Minute))
}

// TestOrphanClockFallsBackToFirstSightingWithoutAnAge keeps the old,
// conservative behaviour for a transfer whose age is unknown.
func TestOrphanClockFallsBackToFirstSightingWithoutAnAge(t *testing.T) {
	now := time.Now()
	var c OrphanClock
	items := []download.Item{{ID: "abc"}}

	assert.Empty(t, c.Due(items, none, now, time.Minute), "never on the first sighting")
	assert.Empty(t, c.Due(items, none, now.Add(30*time.Second), time.Minute))
	assert.Equal(t, []string{"abc"}, c.Due(items, none, now.Add(61*time.Second), time.Minute))

	// A matched pass resets the fallback clock.
	assert.Empty(t, c.Due(items, map[string]struct{}{"abc": {}}, now.Add(2*time.Minute), time.Minute))
	assert.Empty(t, c.Due(items, none, now.Add(2*time.Minute), time.Minute), "must restart from the next sighting")
}

// TestFinalizerIsALegalDistinctFinalizer pins the name: a qualified name the
// apiserver accepts, and not the controller's own
// "download.clustarr.io/download", which guards something else.
func TestFinalizerIsALegalDistinctFinalizer(t *testing.T) {
	assert.Empty(t, validation.IsQualifiedName(Finalizer))
	assert.NotEqual(t, "download.clustarr.io/download", Finalizer)
}
