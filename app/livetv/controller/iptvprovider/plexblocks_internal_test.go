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
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	clustarrv1 "github.com/mediactl/clustarr/api/clustarr/v1alpha1"
	"github.com/mediactl/clustarr/app/livetv/tuner"
	"github.com/mediactl/clustarr/pkg/iptv/hdhr"
	"github.com/mediactl/clustarr/pkg/iptv/plexguide"
)

const plexLineupID = "657c8183afd0f02cd353633f"

// snapTuner answers one snapshot.
type snapTuner struct {
	snap tuner.Snapshot
	ok   bool
}

func (*snapTuner) Apply(tuner.Config)                                     {}
func (*snapTuner) Remove(types.NamespacedName)                            {}
func (s *snapTuner) Snapshot(types.NamespacedName) (tuner.Snapshot, bool) { return s.snap, s.ok }

func plexProvider(enabled bool) *clustarrv1.IPTVProvider {
	p := &clustarrv1.IPTVProvider{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "news"}}
	p.Spec.PlexGuide = &clustarrv1.PlexGuide{Lineup: plexLineupID, Enabled: enabled}
	return p
}

// namedChannels are active channels ch-<number>.
func namedChannels(numbers ...string) []clustarrv1.IPTVChannel {
	chans := activeChannels(numbers...)
	for i := range chans {
		chans[i].Name = "ch-" + chans[i].Spec.Number
	}
	return chans
}

// pairedSnapshot pairs the numbers given with stations of the lineup, the
// rest not in it.
func pairedSnapshot(chans []clustarrv1.IPTVChannel, paired ...string) tuner.Snapshot {
	s := tuner.Snapshot{
		PlaylistFetched: true, Channels: map[string]tuner.ChannelState{},
		Plex: tuner.PlexState{LineupID: plexLineupID, Title: "DIRECTV Mobile (1009 channels)", Stations: 1000, Loaded: true},
	}
	on := map[string]bool{}
	for _, n := range paired {
		on[n] = true
	}
	for _, ch := range chans {
		p := plexguide.Pairing{Match: plexguide.NotInLineup}
		if on[ch.Spec.Number] {
			grid := "aaaaaaaaaaaaaaaaaaaa" + ch.Spec.Number
			p = plexguide.Pairing{Match: plexguide.Automatic, Station: plexguide.Station{ID: plexLineupID + "-" + grid, GridKey: grid, CallSign: "X" + ch.Spec.Number, VCN: "2" + ch.Spec.Number[1:]}}
		}
		s.Channels[ch.Name] = tuner.ChannelState{Plex: &p}
	}
	return s
}

func TestPlexBlocksHoldOnlyPairedChannelsUnderNewDeviceIDs(t *testing.T) {
	p := plexProvider(true)
	chans := namedChannels("1001", "1002", "1003", "1004", "1005")
	r := fakeReconciler(t, testAddress, p)
	r.Tuner = &snapTuner{snap: pairedSnapshot(chans, "1001", "1003", "1005"), ok: true}
	pl, err := r.planBlocks(context.Background(), p, chans)
	require.NoError(t, err)
	require.Empty(t, pl.reason)
	require.Len(t, pl.blocks, 1)
	b := pl.blocks[0]
	assert.Equal(t, hdhr.BlockDeviceID("media", "news", "plex/"+plexLineupID+"/1001"), b.DeviceID)
	assert.NotEqual(t, hdhr.BlockDeviceID("media", "news", "1001"), b.DeviceID, "never the XMLTV block's device")
	assert.Equal(t, plexguide.LineupURI(plexLineupID, "DIRECTV Mobile (1009 channels)"), b.Lineup)
	assert.Empty(t, b.GuidePath, "Plex's guide, not clustarr's")
	assert.Equal(t, []string{"1001", "1003", "1005"}, pl.numbers[b.Start], "the channels off Plex are in no block")
	assert.Equal(t, int32(3), b.Channels)
	assert.Equal(t, "1001", b.First)
	assert.Equal(t, "1005", b.Last)
	cfg := tunerBlocks(pl)
	assert.Equal(t, b.Lineup, cfg[0].Lineup, "the tuner learns the block is Plex's")
}

func TestPlexBlocksWaitForTheLineup(t *testing.T) {
	p := plexProvider(true)
	p.Status.Blocks = []clustarrv1.BlockStatus{{Start: "1001", First: "1001", Last: "1003", DeviceID: "8246E54A", GuidePath: BlockGuidePath}}
	chans := namedChannels("1001", "1002", "1003")
	r := fakeReconciler(t, testAddress, p)
	r.Tuner = &snapTuner{snap: tuner.Snapshot{PlaylistFetched: true}, ok: true}
	pl, err := r.planBlocks(context.Background(), p, chans)
	require.NoError(t, err)
	assert.Equal(t, clustarrv1.ReasonLineupPending, pl.reason)
	require.Len(t, pl.blocks, 1)
	assert.Equal(t, "8246E54A", pl.blocks[0].DeviceID, "the published block is kept: no DVR goes")

	r.Tuner = &snapTuner{ok: false}
	pl, err = r.planBlocks(context.Background(), p, chans)
	require.NoError(t, err)
	assert.Equal(t, clustarrv1.ReasonLineupPending, pl.reason, "a tuner that does not answer is no lineup either")
}

func TestSwitchingKindsPacksAfresh(t *testing.T) {
	chans := namedChannels("1001", "1002", "1200", "1201")
	p := plexProvider(true)
	p.Status.Blocks = []clustarrv1.BlockStatus{
		{Start: "1001", First: "1001", Last: "1002", DeviceID: "8246E54A", GuidePath: BlockGuidePath},
		{Start: "1200", First: "1200", Last: "1201", DeviceID: "8246E54B", GuidePath: BlockGuidePath},
	}
	r := fakeReconciler(t, testAddress, p)
	r.Tuner = &snapTuner{snap: pairedSnapshot(chans, "1002", "1201"), ok: true}
	pl, err := r.planBlocks(context.Background(), p, chans)
	require.NoError(t, err)
	require.Len(t, pl.blocks, 1, "a first packing of the two paired numbers, not the XMLTV blocks' starts")
	assert.Equal(t, "1002", pl.blocks[0].Start)
	assert.Equal(t, []string{"1002", "1201"}, pl.numbers["1002"])

	// And back: the Plex block's start is no boundary of the XMLTV blocks.
	p.Spec.PlexGuide.Enabled = false
	p.Status.Blocks = []clustarrv1.BlockStatus{{Start: "1002", First: "1002", Last: "1201", DeviceID: pl.blocks[0].DeviceID, Lineup: pl.blocks[0].Lineup}}
	pl, err = r.planBlocks(context.Background(), p, chans)
	require.NoError(t, err)
	require.Len(t, pl.blocks, 1)
	assert.Equal(t, "1001", pl.blocks[0].Start)
	assert.Equal(t, hdhr.BlockDeviceID("media", "news", "1001"), pl.blocks[0].DeviceID)
	assert.Empty(t, pl.blocks[0].Lineup)
	assert.Equal(t, BlockGuidePath, pl.blocks[0].GuidePath)
}

func TestADryRunPairsButKeepsXMLTVBlocks(t *testing.T) {
	p := plexProvider(false)
	chans := namedChannels("1001", "1002", "1003")
	r := fakeReconciler(t, testAddress, p)
	r.Tuner = &snapTuner{snap: pairedSnapshot(chans, "1001"), ok: true}
	pl, err := r.planBlocks(context.Background(), p, chans)
	require.NoError(t, err)
	require.Len(t, pl.blocks, 1)
	assert.Empty(t, pl.blocks[0].Lineup)
	assert.Equal(t, []string{"1001", "1002", "1003"}, pl.numbers[pl.blocks[0].Start], "every active channel, on the XMLTV guide")
}

func TestAPlexBlocksMapBudgetCountsItsStations(t *testing.T) {
	p := plexProvider(true)
	chans := namedChannels("1001")
	r := fakeReconciler(t, testAddress, p)
	r.Tuner = &snapTuner{snap: pairedSnapshot(chans, "1001"), ok: true}
	pl, err := r.planBlocks(context.Background(), p, chans)
	require.NoError(t, err)
	require.Len(t, pl.blocks, 1)
	assert.Greater(t, pl.blocks[0].MapBytes, int32(100), "the lineup channel's 49-byte key counted, not the number twice")
}

// TestPlexBlocksWaitForThePlaylist: a lineup read from its cache before the
// playlist (a restart whose playlist cache is gone or another account's)
// pairs no channel, since a render places none before the first playlist;
// the published blocks are kept, or every Plex DVR would go.
func TestPlexBlocksWaitForThePlaylist(t *testing.T) {
	p := plexProvider(true)
	plexDev := hdhr.BlockDeviceID("media", "news", "plex/"+plexLineupID+"/1001")
	p.Status.Blocks = []clustarrv1.BlockStatus{{Start: "1001", First: "1001", Last: "1003", DeviceID: plexDev, Lineup: plexguide.LineupURI(plexLineupID, "x")}}
	r := fakeReconciler(t, testAddress, p)
	r.Tuner = &snapTuner{snap: tuner.Snapshot{PlaylistFetched: false, Plex: tuner.PlexState{LineupID: plexLineupID, Loaded: true}}, ok: true}
	pl, err := r.planBlocks(context.Background(), p, namedChannels("1001", "1002", "1003"))
	require.NoError(t, err)
	assert.Equal(t, clustarrv1.ReasonLineupPending, pl.reason)
	require.Len(t, pl.blocks, 1, "the published Plex block is kept")
	assert.Equal(t, plexDev, pl.blocks[0].DeviceID)
}

// TestALineupChangeGivesNewDevices: a DVR's lineup cannot change in place,
// so another lineup is other devices, which cluster-plex registers beside
// the old ones' removal, as for a switch of kind.
func TestALineupChangeGivesNewDevices(t *testing.T) {
	const otherLineup = "657cb9b8a698374a7a2dbca0"
	p := plexProvider(true)
	oldDev := hdhr.BlockDeviceID("media", "news", "plex/"+otherLineup+"/1001")
	p.Status.Blocks = []clustarrv1.BlockStatus{{Start: "1001", First: "1001", Last: "1001", DeviceID: oldDev, Lineup: plexguide.LineupURI(otherLineup, "DIRECTV New York")}}
	chans := namedChannels("1001", "1002")
	r := fakeReconciler(t, testAddress, p)
	r.Tuner = &snapTuner{snap: pairedSnapshot(chans, "1001", "1002"), ok: true}
	pl, err := r.planBlocks(context.Background(), p, chans)
	require.NoError(t, err)
	require.Len(t, pl.blocks, 1)
	assert.NotEqual(t, oldDev, pl.blocks[0].DeviceID, "the old lineup's device is not this lineup's")
	assert.Equal(t, hdhr.BlockDeviceID("media", "news", "plex/"+plexLineupID+"/1001"), pl.blocks[0].DeviceID)
	assert.Equal(t, plexguide.LineupURI(plexLineupID, "DIRECTV Mobile (1009 channels)"), pl.blocks[0].Lineup)
}
