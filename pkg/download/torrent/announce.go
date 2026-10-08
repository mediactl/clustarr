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

package torrent

import (
	"net/url"
	"strconv"
)

// Re-adding an info hash the client dropped earlier (smoke test, 2026-10-08).
//
// anacrolix v1.61.0 announces to regular (http, https, udp) trackers from
// one client-level dispatcher keyed by (tracker URL, info hash), and never
// forgets a key: Drop leaves its announceData, announceStates and the
// torrentForAnnounceRequests binding behind (nothing in the package deletes
// them). A Torrent added later for the same info hash registers the same
// keys, and regularTrackerAnnounceDispatcher.addKey returns at once for a
// key it already holds (client-tracker-announcer.go:379), so
// torrentForAnnounceRequests[infohash] keeps a weak pointer to the dropped
// Torrent. Every announce for that key then builds its request from the
// dropped Torrent and hands the tracker's peers to it
// (singleAnnounceAttempter, getTorrentForAnnounceRequest at :533 and :883;
// singleAnnounce's addPeers at :586, which a closed Torrent ignores) -- or,
// once it is collected, skips the announce ("Torrent GCed"). The re-added
// Torrent never hears from the tracker: with the DHT off it reads "no peers
// connected" until the engine restarts with a new client. Upstream reports
// it as anacrolix/torrent#1048; neither fix offered (#1049, #1051) was
// merged, #1081 is open, and master still returns early.
//
// So a re-added Torrent announces under keys the dispatcher has never seen:
// each regular tracker URL carries a fragment naming how many times this
// client has dropped the info hash. A new key runs addKey in full -- a
// fresh announce state that sends Started at once, and the binding moved to
// the new Torrent, which the old keys (still announcing, since they are
// never forgotten) then use too. A fragment never leaves the process: Go's
// HTTP client sends the request URI without it, and anacrolix's UDP client
// sends RequestURI (BEP 41), which has none either. Each tagged URL is one
// more anacrolix tracker client, made through the same ClientConfig, so the
// proxy's rules (HTTPProxy, TrackerListenPacket) hold for it; the tag is the
// drop count, not a unique id, so every info hash on its n-th re-add shares
// one client per tracker, and their number is bounded by the most re-adds
// any one release has had in this process.
//
// What it costs: the dropped Torrent's keys stay registered for the
// client's life (as they already did) and now announce for the re-added
// Torrent alongside its own, so a release re-added n times is announced up
// to n+1 times per tracker interval. And v1.61.0 registers a new URL's
// tracker client by writing a map (initTrackerClient,
// client-tracker-announcer.go:877) that an announce in flight reads after
// releasing the client lock (singleAnnounce, :555-559): a data race the
// library has for every tracker URL a client first sees, which each new
// (tracker, drop count) pair now meets once. Restarting the anacrolix client
// per re-add was the alternative, and would interrupt every other transfer.

// reannounceFragment prefixes the fragment of a re-added Torrent's tracker
// URL; the drop count follows it.
const reannounceFragment = "clustarr-reannounce-"

// trackersForDrops is tiers as a Torrent of an info hash this client has
// dropped drops times announces them: unchanged for none, each regular
// tracker's URL tagged with the count otherwise (see above).
func trackersForDrops(tiers [][]string, drops int) [][]string {
	if drops == 0 {
		return tiers
	}
	out := make([][]string, len(tiers))
	for i, tier := range tiers {
		out[i] = make([]string, len(tier))
		for j, u := range tier {
			out[i][j] = announceURLForDrops(u, drops)
		}
	}
	return out
}

// announceURLForDrops is raw tagged for drops, replacing any tag it carries
// already (a publish re-adds from the Torrent's own, possibly tagged, list).
// A URL anacrolix does not announce to through the dispatcher -- WebTorrent,
// an unparsable one, one with userinfo (which anacrolix refuses) -- or one
// that would not parse back to itself, which anacrolix requires of a tracker
// key, is returned as it is.
func announceURLForDrops(raw string, drops int) string {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil {
		return raw
	}
	switch u.Scheme {
	case "http", "https", "udp", "udp4", "udp6":
	default:
		return raw
	}
	u.Fragment, u.RawFragment = reannounceFragment+strconv.Itoa(drops), ""
	tagged := u.String()
	if back, err := url.Parse(tagged); err != nil || back.String() != tagged {
		return raw
	}
	return tagged
}
