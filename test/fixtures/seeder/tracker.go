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
//
// Every answer carries ReannounceInterval, not the tracker server's default
// five minutes. With no DHT and no PEX the tracker is the client's only way
// to find the seeder, and anacrolix/torrent re-announces only when the last
// answer's interval has elapsed: after a dropped connection a download sat
// idle for up to five minutes before it found the seeder again (seen by gap
// fix Z1), which on e2e's timeouts reads as a stalled download. The interval
// is a constant so every run announces on the same schedule.
type singlePeerTracker struct {
	infoHash metainfo.Hash
	peer     trackerServer.PeerInfo
}

// ReannounceInterval is the "interval", in seconds, every announce answer
// carries: how long a client waits before it announces again.
const ReannounceInterval = 2

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
	interval := generics.Some(int32(ReannounceInterval))
	if infoHash != t.infoHash {
		return trackerServer.ServerAnnounceResult{Seeders: generics.Some(int32(0)), Interval: interval}
	}
	return trackerServer.ServerAnnounceResult{
		Peers:    []trackerServer.PeerInfo{t.peer},
		Seeders:  generics.Some(int32(1)),
		Interval: interval,
	}
}
