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

package manager_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/app/livetv/controller/iptvprovider"
	"github.com/mediactl/clustarr/app/livetv/manager"
)

func TestParsePortRange(t *testing.T) {
	r, err := manager.ParsePortRange(manager.DefaultPortRange)
	require.NoError(t, err)
	assert.Equal(t, iptvprovider.PortRange{Low: 47000, High: 47999}, r)
	r, err = manager.ParsePortRange(" 50000 - 50010 ")
	require.NoError(t, err)
	assert.Equal(t, iptvprovider.PortRange{Low: 50000, High: 50010}, r)
	for _, bad := range []string{"", "47000", "a-b", "1000-2000", "47000-47000", "47999-47000", "47000-70000"} {
		_, err := manager.ParsePortRange(bad)
		assert.Error(t, err, bad)
	}
}

func TestTargetPortIsTheBindAddressesPort(t *testing.T) {
	p, err := manager.TargetPort(":5004")
	require.NoError(t, err)
	assert.Equal(t, int32(5004), p)
	for _, bad := range []string{"5004", ":0", ":x", "0.0.0.0"} {
		_, err := manager.TargetPort(bad)
		assert.Error(t, err, bad)
	}
}
