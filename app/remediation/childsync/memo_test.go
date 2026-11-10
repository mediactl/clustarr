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

package childsync_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/types"

	"github.com/mediactl/clustarr/app/remediation/childsync"
)

// The memo table (2026-11-10 design D2): a container fans out once per
// process, and again only when its spec generation, its metadata refresh
// or its add options moved since the last complete fan-out.
func TestTheMemoIsDueOnlyWhenAFanOutInputMoved(t *testing.T) {
	at := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	base := childsync.KeyAt(3, at, true)
	for name, tc := range map[string]struct {
		synced  *childsync.Key // nil: never synced
		now     childsync.Key
		wantDue bool
	}{
		"never fanned out":           {nil, base, true},
		"nothing moved":              {&base, base, false},
		"the spec moved":             {&base, childsync.KeyAt(4, at, true), true},
		"the metadata was refreshed": {&base, childsync.KeyAt(3, at.Add(time.Hour), true), true},
		"the add options were applied": {
			ptr(childsync.KeyAt(3, at, false)), base, true,
		},
		"the refresh read in another zone": {
			&base, childsync.KeyAt(3, at.In(time.FixedZone("west", -5*3600)), true), false,
		},
		// metav1.Time is stored to the second: the refresh a pass carries
		// at nanoseconds (a record just incorporated) is the one it reads
		// back from the apiserver.
		"the refresh carried at nanoseconds before its round trip": {
			&base, childsync.KeyAt(3, at.Add(123*time.Millisecond), true), false,
		},
		"the refresh moved by a whole second": {
			&base, childsync.KeyAt(3, at.Add(time.Second), true), true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			var m childsync.Memo
			uid := types.UID("u-1")
			if tc.synced != nil {
				m.Synced(uid, *tc.synced, true)
			}
			assert.Equal(t, tc.wantDue, m.Due(uid, tc.now, true))
		})
	}
}

// A container without metadata has no Key: it is always due and records
// nothing, so the first pass after its metadata lands fans out.
func TestAContainerWithoutMetadataIsAlwaysDue(t *testing.T) {
	var m childsync.Memo
	uid := types.UID("u-2")
	k := childsync.KeyAt(1, time.Time{}, false)
	assert.True(t, m.Due(uid, k, false))
	m.Synced(uid, k, false)
	assert.True(t, m.Due(uid, k, false), "a sync without metadata records nothing")
	assert.True(t, m.Due(uid, k, true), "and the first pass with metadata is due")
}

// Each UID is remembered on its own, and Forget drops one.
func TestTheMemoIsPerUIDAndForgets(t *testing.T) {
	var m childsync.Memo
	k := childsync.KeyAt(1, time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC), true)
	m.Synced("a", k, true)
	assert.False(t, m.Due("a", k, true))
	assert.True(t, m.Due("b", k, true), "another UID never synced")
	m.Forget("a")
	assert.True(t, m.Due("a", k, true), "forgotten: due again")
	m.Forget("never-seen")
}

func ptr[T any](v T) *T { return &v }
