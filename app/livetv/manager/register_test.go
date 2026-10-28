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

	"github.com/mediactl/clustarr/app/livetv/manager"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// TestTheServiceAddressIsItsClusterFQDN: Plex is given each block's URI on
// this address, and the .svc short form would not resolve from a PMS on
// another cluster's DNS search path.
func TestTheServiceAddressIsItsClusterFQDN(t *testing.T) {
	assert.Equal(t, "clustarr-livetv.media.svc.cluster.local:80", manager.ServiceAddress("clustarr-livetv", "media"))
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

// TestNoPublisherWhenTheTunerIsDisabled: --livetv-bind-address=0 registers
// neither the tuner nor the endpoint publisher, so the livetv Service gets
// no endpoint to a port nobody listens on (the placement plan's Review
// Focus 3). A nil manager proves nothing is added: any Add would panic.
func TestNoPublisherWhenTheTunerIsDisabled(t *testing.T) {
	require.NoError(t, manager.Register(nil, nil, manager.Options{BindAddress: k8s.DisabledBindAddress}))
}
