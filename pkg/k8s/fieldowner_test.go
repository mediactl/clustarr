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

package k8s_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mediactl/clustarr/pkg/k8s"
)

// In binaries named manager and agent an untagged write would record
// "manager" or "agent"; the default owner keeps the audit continuous
// across the split (§5.3.5, OD4).
func TestManagerOptionsPinTheDefaultFieldOwner(t *testing.T) {
	assert.Equal(t, "clustarr", k8s.DefaultFieldOwner)
	for _, elect := range []bool{false, true} {
		opts := k8s.DefaultOptions().ManagerOptions("catalog.clustarr.io", elect)
		assert.Equal(t, "clustarr", opts.Client.FieldOwner, "leader election %v", elect)
	}
}

// The default owner is never a server-side-apply manager: every PatchStatus,
// Apply and FieldOwner site names one of §2's managers, and Validate refuses
// anything else.
func TestTheDefaultFieldOwnerIsNoFieldManager(t *testing.T) {
	assert.Error(t, k8s.FieldManager(k8s.DefaultFieldOwner).Validate())
	assert.NotContains(t, k8s.FieldManagers(), k8s.FieldManager(k8s.DefaultFieldOwner))
	assert.NotContains(t, k8s.RetiredFieldManagers(), k8s.FieldManager(k8s.DefaultFieldOwner))
}
