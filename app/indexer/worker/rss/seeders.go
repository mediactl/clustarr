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

package rss

import (
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/pkg/torznab"
)

// BelowMinimumSeeders reports whether a release from idx carries fewer
// seeders than the Indexer's spec.minimumSeeders allows, which drops it
// before it is indexed, published to the firehose or returned by a search.
//
// It is Sonarr's and Radarr's TorrentSeedingSpecification
// (src/NzbDrone.Core/DecisionEngine/Specifications/TorrentSeedingSpecification.cs):
// only a torrent is judged, and only when the indexer REPORTED a seeder
// count -- `Seeders.HasValue && Seeders.Value < minimumSeeders`. A torrent
// with no seeders attr passes, because "the indexer did not say" is not
// "nobody is seeding"; a usenet release has no seeders at all.
//
// The threshold is read through MinimumSeedersOrDefault, so an unset field is
// the CRD's default of 1 and an explicit 0 admits a seederless torrent. It
// lives here rather than in pkg/decision because the threshold is per
// Indexer and only index holds the Indexer; the one function serves the
// RSS poll and the search fan-out alike (app/indexer/search imports this
// package for ProjectRelease already), so the two paths cannot disagree.
func BelowMinimumSeeders(idx *indexv1alpha1.Indexer, protocol string, seeders *int32) bool {
	if idx == nil || seeders == nil || protocol != string(commonv1.ProtocolTorrent) {
		return false
	}
	return *seeders < idx.Spec.MinimumSeedersOrDefault()
}

// payloadType is the media type an item's enclosure names protocol's
// payload by: an NZB for usenet, a .torrent for anything else.
func payloadType(protocol string) string {
	if protocol == string(commonv1.ProtocolUsenet) {
		return torznab.MediaTypeNZB
	}
	return torznab.MediaTypeTorrent
}

// NoPayload reports a release its indexer does not carry for protocol: its
// enclosures name media types and none is protocol's
// (torznab.Release.PayloadLink). An indexer that mirrors torrents to
// Usenet lists every release in one feed, each with a torrent enclosure
// and, once mirrored, an NZB one; read as a usenet indexer, a release with
// no NZB is not one of its releases. A torrent is still a torrent by its
// magnet or info hash alone. Like BelowMinimumSeeders, the one function
// serves the RSS poll and the search fan-out.
func NoPayload(r torznab.Release, protocol string) bool {
	if _, ok := r.PayloadLink(payloadType(protocol)); ok || len(r.Enclosures) == 0 {
		return false
	}
	return protocol == string(commonv1.ProtocolUsenet) || (r.MagnetURL == "" && r.InfoHash == "")
}
