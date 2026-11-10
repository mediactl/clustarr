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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// The issue sync memo: due until a complete fan-out, then only when the
// spec, the metadata refresh or the add options moved (2026-11-10 design
// D2, D14); always due without metadata; one Comic's record never answers
// for another.
func TestIssueSyncMemo(t *testing.T) {
	refreshed := metav1.NewTime(time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC))
	c := &catalogv1alpha1.Comic{
		ObjectMeta: metav1.ObjectMeta{UID: "uid-1", Generation: 3},
		Status:     catalogv1alpha1.ComicStatus{Metadata: &catalogv1alpha1.ComicMetadata{RefreshedAt: refreshed}},
	}
	var m issueSyncMemo

	assert.True(t, m.due(c), "never fanned out in this process")
	m.synced(c)
	assert.True(t, m.due(c), "the complete fan-out records the add options as applied, which the stored flag does not say yet")
	c.Status.AddOptionsApplied = true
	assert.False(t, m.due(c), "the apply after the fan-out records the flag, and nothing else moved")

	gen := c.DeepCopy()
	gen.Generation = 4
	assert.True(t, m.due(gen), "a spec change")

	refetched := c.DeepCopy()
	refetched.Status.Metadata.RefreshedAt = metav1.NewTime(refreshed.Add(time.Hour))
	assert.True(t, m.due(refetched), "a metadata refresh")

	noMeta := c.DeepCopy()
	noMeta.Status.Metadata = nil
	assert.True(t, m.due(noMeta), "no metadata is always due")
	m.synced(noMeta)
	assert.False(t, m.due(c), "a fan-out without metadata records nothing, so the earlier record stands")

	other := c.DeepCopy()
	other.UID = "uid-2"
	assert.True(t, m.due(other), "another Comic")

	m.synced(gen)
	assert.False(t, m.due(gen))
	assert.True(t, m.due(c), "the record is the latest fan-out's inputs")
}
