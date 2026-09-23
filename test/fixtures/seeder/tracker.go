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

package seeder

import (
	"context"
	"net/netip"

	"github.com/anacrolix/generics"
	"github.com/anacrolix/torrent/metainfo"
	trackerServer "github.com/anacrolix/torrent/tracker/server"
	"github.com/anacrolix/torrent/tracker/udp"
)

// singlePeerTracker is a [trackerServer.AnnounceTracker] that advertises
// exactly one peer -- this seeder -- for exactly one torrent -- the fixture
// it seeds. It tracks no announce history and no other torrents: a BEP3
// answer here is a fixed fact ("I am seeding this one file"), not a lookup
// into a live swarm table, which is all a seed-only fixture needs.
//
// This is what lets a stock BitTorrent client -- production configuration,
// trackers enabled, DHT disabled because there is no Internet to bootstrap
// it from -- discover and connect to the seeder using nothing but the
// .torrent's own announce URL, exactly as it would against a real tracker.
type singlePeerTracker struct {
	infoHash metainfo.Hash
	peer     trackerServer.PeerInfo
}

func newSinglePeerTracker(infoHash metainfo.Hash, peer netip.AddrPort) *singlePeerTracker {
	return &singlePeerTracker{infoHash: infoHash, peer: trackerServer.PeerInfo{AnnounceAddr: peer}}
}

// TrackAnnounce records nothing. Every announce this fixture answers is
// answered identically regardless of who asked or what event they reported.
func (t *singlePeerTracker) TrackAnnounce(context.Context, udp.AnnounceRequest, trackerServer.AnnounceAddr) error {
	return nil
}

// Scrape reports one seeder for the fixture's own info hash and nothing for
// any other. httpTrackerServer's HTTP handler never calls this (BEP3's HTTP
// scrape convention is a separate, optional endpoint this fixture does not
// serve), but a real [trackerServer.AnnounceTracker] must implement it.
func (t *singlePeerTracker) Scrape(_ context.Context, infoHashes []trackerServer.InfoHash) ([]udp.ScrapeInfohashResult, error) {
	out := make([]udp.ScrapeInfohashResult, len(infoHashes))
	for i, ih := range infoHashes {
		if ih == t.infoHash {
			out[i] = udp.ScrapeInfohashResult{Seeders: 1}
		}
	}
	return out, nil
}

// GetPeers answers with the seeder itself for the fixture's info hash, and
// with nobody for any other -- this fixture seeds exactly one file, so an
// announce for a different torrent finds nothing here, the same as a real
// tracker that was never told about it.
func (t *singlePeerTracker) GetPeers(
	_ context.Context, infoHash trackerServer.InfoHash, _ trackerServer.GetPeersOpts, _ trackerServer.AnnounceAddr,
) trackerServer.ServerAnnounceResult {
	if infoHash != t.infoHash {
		return trackerServer.ServerAnnounceResult{Seeders: generics.Some(int32(0))}
	}
	return trackerServer.ServerAnnounceResult{
		Peers:   []trackerServer.PeerInfo{t.peer},
		Seeders: generics.Some(int32(1)),
	}
}
