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
// Indexer and only indexarr holds the Indexer; the one function serves the
// RSS poll and the search fan-out alike (app/indexer/search imports this
// package for ProjectRelease already), so the two paths cannot disagree.
func BelowMinimumSeeders(idx *indexv1alpha1.Indexer, protocol string, seeders *int32) bool {
	if idx == nil || seeders == nil || protocol != string(commonv1.ProtocolTorrent) {
		return false
	}
	return *seeders < idx.Spec.MinimumSeedersOrDefault()
}
