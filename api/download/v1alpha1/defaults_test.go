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

package v1alpha1_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"

	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
)

// TestTorrentProxyDefaults: every defaulted proxy switch is a pointer, so
// a DownloadClient built in Go can set it false, and nil reads as the CRD
// default.
func TestTorrentProxyDefaults(t *testing.T) {
	var p downloadv1alpha1.TorrentProxy
	require.True(t, p.HostnameLookupOrDefault())
	require.True(t, p.PeerConnectionsOrDefault())
	require.True(t, p.UDPOrDefault())
	require.Equal(t, "1.1.1.1:53", p.DNSServerOrDefault())
	require.Equal(t, "10.64.0.1:1080", downloadv1alpha1.TorrentProxy{Host: "10.64.0.1", Port: 1080}.Address())
	require.Equal(t, "[fd00::1]:1080", downloadv1alpha1.TorrentProxy{Host: "fd00::1", Port: 1080}.Address())

	p = downloadv1alpha1.TorrentProxy{
		HostnameLookup: ptr.To(false), PeerConnections: ptr.To(false), UDP: ptr.To(false), DNSServer: "9.9.9.9:53",
	}
	require.False(t, p.HostnameLookupOrDefault())
	require.False(t, p.PeerConnectionsOrDefault())
	require.False(t, p.UDPOrDefault())
	require.Equal(t, "9.9.9.9:53", p.DNSServerOrDefault())
}
