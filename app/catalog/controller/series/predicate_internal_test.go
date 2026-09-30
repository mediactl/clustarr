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
	"testing"

	"github.com/stretchr/testify/assert"
	"sigs.k8s.io/controller-runtime/pkg/event"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// An owned Episode wakes its Series when what the rollup counts changes:
// its file, or its phase (Wanted, Downloading, Unmonitored...), which the
// missing and downloading counts read. Nothing else -- the Series' own
// writes to its Episodes (title, air date) must not loop it.
func TestEpisodeRollupChanged(t *testing.T) {
	p := episodeRollupChanged()
	ep := func(phase catalogv1alpha1.EpisodePhase, hasFile bool, title string) *catalogv1alpha1.Episode {
		return &catalogv1alpha1.Episode{Status: catalogv1alpha1.EpisodeStatus{Phase: phase, HasFile: hasFile, Title: title}}
	}
	upd := func(o, n *catalogv1alpha1.Episode) bool {
		return p.Update(event.UpdateEvent{ObjectOld: o, ObjectNew: n})
	}
	assert.True(t, upd(ep("Wanted", false, "a"), ep("Unmonitored", false, "a")))
	assert.True(t, upd(ep("Wanted", false, "a"), ep("Wanted", true, "a")))
	assert.False(t, upd(ep("Wanted", false, "a"), ep("Wanted", false, "b")))
}
