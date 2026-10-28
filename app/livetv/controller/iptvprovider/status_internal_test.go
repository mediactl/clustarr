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
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	clustarrac "github.com/mediactl/clustarr/api/applyconfiguration/clustarr/v1alpha1"
	clustarrv1 "github.com/mediactl/clustarr/api/clustarr/v1alpha1"
	"github.com/mediactl/clustarr/app/livetv/tuner"
)

// TestAServerIsHTTPSUnlessItSaysOtherwise: spec.server takes a host, a
// host:port or a URL; with no scheme it is https, and anything that would
// carry a credential or a query is refused.
func TestAServerIsHTTPSUnlessItSaysOtherwise(t *testing.T) {
	for in, want := range map[string]string{
		"tvload.win":                   "https://tvload.win",
		" tvload.win/ ":                "https://tvload.win",
		"tvload.win:8080":              "https://tvload.win:8080",
		"http://tvload.win":            "http://tvload.win",
		"http://tvload.win:80/":        "http://tvload.win:80",
		"https://panel.example/xtream": "https://panel.example/xtream",
	} {
		got, err := NormalizeServer(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got, in)
	}
	for _, bad := range []string{"", "ftp://tvload.win", "https://u:p@tvload.win", "tvload.win/get.php?username=u", "https://"} {
		_, err := NormalizeServer(bad)
		assert.Error(t, err, bad)
	}
}

// TestAStatusTheTunerCannotAnswerIsKept: before the tuner reports (a
// manager just started), the status re-sends what it says, the account and
// the lineup's span included, so the apply releases none of it.
func TestAStatusTheTunerCannotAnswerIsKept(t *testing.T) {
	exp := metav1.NewTime(time.Date(2026, 10, 21, 6, 19, 39, 0, time.UTC))
	p := &clustarrv1.IPTVProvider{Status: clustarrv1.IPTVProviderStatus{
		Account: &clustarrv1.AccountStatus{Status: "Active", ExpiresAt: &exp, MaxConnections: 1},
		Guide:   &clustarrv1.GuideStatus{Channels: 11165, Programmes: 9000},
		Lineup:  &clustarrv1.LineupStatus{Active: 243, First: "1001", Last: "1300"},
	}}
	st := clustarrac.IPTVProviderStatus()
	reassertKnownStatus(st, p)
	require.NotNil(t, st.Account)
	assert.Equal(t, "Active", *st.Account.Status)
	assert.Equal(t, exp, *st.Account.ExpiresAt)
	assert.Equal(t, int32(1), *st.Account.MaxConnections)
	require.NotNil(t, st.Guide)
	assert.Equal(t, int32(11165), *st.Guide.Channels)
	require.NotNil(t, st.Lineup)
	assert.Equal(t, "1001", *st.Lineup.First)
	assert.Equal(t, "1300", *st.Lineup.Last)
}

func TestATunerThatHasNotFetchedKeepsTheBlocksHashes(t *testing.T) {
	// After a manager restart the tuner renders before its first fetch,
	// with no hashes. Releasing them would make cluster-plex save every
	// block's map and reload its guide again.
	p := &clustarrv1.IPTVProvider{Status: clustarrv1.IPTVProviderStatus{Blocks: []clustarrv1.BlockStatus{
		{Start: "1001", LineupHash: "lineup-1001", GuideHash: "guide-1001"},
	}}}
	pl := plan{blocks: []clustarrv1.BlockStatus{{Start: "1001", Channels: 1, First: "1001", Last: "1001"}}}
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

// TestABlockCarriesItsPathAndURI: the status names each block's device path
// and the URI Plex registers.
func TestABlockCarriesItsPathAndURI(t *testing.T) {
	pl := plan{blocks: []clustarrv1.BlockStatus{{
		Start: "1001", DeviceID: "CAFE0001", Path: "/hdhr/CAFE0001",
		URI: "http://clustarr-livetv.media.svc.cluster.local:80/hdhr/CAFE0001",
	}}}
	st := clustarrac.IPTVProviderStatus()
	withBlocks(st, &clustarrv1.IPTVProvider{}, pl, tuner.Snapshot{}, false)
	require.Len(t, st.Blocks, 1)
	require.NotNil(t, st.Blocks[0].Path)
	require.NotNil(t, st.Blocks[0].URI)
	assert.Equal(t, "/hdhr/CAFE0001", *st.Blocks[0].Path)
	assert.Equal(t, "http://clustarr-livetv.media.svc.cluster.local:80/hdhr/CAFE0001", *st.Blocks[0].URI)
}
