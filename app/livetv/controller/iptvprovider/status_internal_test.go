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

package iptvprovider

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	clustarrac "github.com/mediactl/clustarr/api/applyconfiguration/clustarr/v1alpha1"
	clustarrv1 "github.com/mediactl/clustarr/api/clustarr/v1alpha1"
	"github.com/mediactl/clustarr/app/livetv/tuner"
)

func TestATunerThatHasNotFetchedKeepsTheBlocksHashes(t *testing.T) {
	// After a manager restart the tuner renders before its first fetch,
	// with no hashes. Releasing them would make cluster-plex save every
	// block's map and reload its guide again.
	p := &clustarrv1.IPTVProvider{Status: clustarrv1.IPTVProviderStatus{Blocks: []clustarrv1.BlockStatus{
		{Start: "1001", LineupHash: "lineup-1001", GuideHash: "guide-1001"},
	}}}
	pl := plan{blocks: []clustarrv1.BlockStatus{{Start: "1001", Channels: 1, First: "1001", Last: "1001", Port: 47000}}}
	snap := tuner.Snapshot{Blocks: []tuner.BlockState{{Start: "1001"}}}

	st := clustarrac.IPTVProviderStatus()
	withBlocks(st, p, pl, snap, true)
	require.Len(t, st.Blocks, 1)
	require.NotNil(t, st.Blocks[0].LineupHash, "the lineup hash is released")
	require.NotNil(t, st.Blocks[0].GuideHash, "the guide hash is released")
	assert.Equal(t, "lineup-1001", *st.Blocks[0].LineupHash)
	assert.Equal(t, "guide-1001", *st.Blocks[0].GuideHash)

	snap.PlaylistFetched, snap.Blocks[0].LineupHash, snap.Blocks[0].GuideHash = true, "lineup-new", "guide-new"
	st = clustarrac.IPTVProviderStatus()
	withBlocks(st, p, pl, snap, true)
	assert.Equal(t, "lineup-new", *st.Blocks[0].LineupHash, "once fetched, the render's")
	assert.Equal(t, "guide-new", *st.Blocks[0].GuideHash)
}
