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

package v1alpha1

import (
	"net"
	"strconv"
)

// The accessors below apply TorrentProxy's CRD defaults to a nil field, so
// a DownloadClient built in Go reads the same as one the apiserver
// defaulted (the typed-client defaulting trap, CLAUDE.md).

// HostnameLookupOrDefault is spec.torrent.proxy.hostnameLookup, true when unset.
func (p TorrentProxy) HostnameLookupOrDefault() bool {
	return p.HostnameLookup == nil || *p.HostnameLookup
}

// PeerConnectionsOrDefault is spec.torrent.proxy.peerConnections, true when unset.
func (p TorrentProxy) PeerConnectionsOrDefault() bool {
	return p.PeerConnections == nil || *p.PeerConnections
}

// UDPOrDefault is spec.torrent.proxy.udp, true when unset.
func (p TorrentProxy) UDPOrDefault() bool {
	return p.UDP == nil || *p.UDP
}

// DNSServerOrDefault is spec.torrent.proxy.dnsServer, DefaultProxyDNSServer
// when unset.
func (p TorrentProxy) DNSServerOrDefault() string {
	if p.DNSServer == "" {
		return DefaultProxyDNSServer
	}
	return p.DNSServer
}

// Address is the proxy's host:port.
func (p TorrentProxy) Address() string {
	return net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port)))
}

// DefaultTorrentPublishDir is where torrents have always been written.
const DefaultTorrentPublishDir = "/data/torrents"

// PublishDirOrDefault is spec.torrent.publishDir, or
// [DefaultTorrentPublishDir] when it is unset (or t is nil).
func (t *TorrentSpec) PublishDirOrDefault() string {
	if t == nil || t.PublishDir == "" {
		return DefaultTorrentPublishDir
	}
	return t.PublishDir
}
