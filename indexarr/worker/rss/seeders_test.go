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

package rss_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"

	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/indexarr/worker/rss"
)

// Sonarr's TorrentSeedingSpecification: only a torrent with a REPORTED seeder
// count below the threshold is refused; unset reads as the CRD default of 1,
// and an explicit 0 admits a seederless torrent.
func TestBelowMinimumSeeders(t *testing.T) {
	withMin := func(n *int32) *indexv1alpha1.Indexer {
		return testIndexer("media", "idx", func(i *indexv1alpha1.Indexer) { i.Spec.MinimumSeeders = n })
	}
	tests := []struct {
		name     string
		idx      *indexv1alpha1.Indexer
		protocol string
		seeders  *int32
		want     bool
	}{
		{"unset minimum, zero seeders", withMin(nil), "torrent", ptr.To[int32](0), true},
		{"unset minimum, one seeder", withMin(nil), "torrent", ptr.To[int32](1), false},
		{"explicit zero admits zero", withMin(ptr.To[int32](0)), "torrent", ptr.To[int32](0), false},
		{"below a raised minimum", withMin(ptr.To[int32](10)), "torrent", ptr.To[int32](9), true},
		{"at a raised minimum", withMin(ptr.To[int32](10)), "torrent", ptr.To[int32](10), false},
		{"no seeders reported passes", withMin(ptr.To[int32](10)), "torrent", nil, false},
		{"usenet is never judged", withMin(ptr.To[int32](10)), "usenet", ptr.To[int32](0), false},
		{"no indexer, no judgement", nil, "torrent", ptr.To[int32](0), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, rss.BelowMinimumSeeders(tt.idx, tt.protocol, tt.seeders))
		})
	}
}

// A poll neither indexes nor publishes a torrent below spec.minimumSeeders;
// one whose indexer reported no seeder count at all still goes through.
func TestPollDropsTorrentsBelowMinimumSeeders(t *testing.T) {
	clock := newFakeClock(t0)
	store := &fakeStore{inserted: 2}
	feed := pageOf(3)
	feed[0].Seeders = ptr.To[int32](0) // below the default of 1
	feed[1].Seeders = ptr.To[int32](4) // fine
	feed[2].Seeders = nil              // not reported: Sonarr accepts it
	w := newTestWorker(t, clock, &fakeSearcher{releases: feed}, testIndexer("media", "idx"), store)

	require.NoError(t, w.Handle(t.Context(), rssTaskMessage(t, "media", "idx")))

	var guids []string
	for _, r := range store.rows() {
		guids = append(guids, r.GUID)
	}
	require.ElementsMatch(t, []string{"guid-1", "guid-2"}, guids)
}
