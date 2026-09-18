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

package decision

import (
	"testing"

	"github.com/stretchr/testify/require"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/quality"
)

func TestProtocolRejection(t *testing.T) {
	t.Run("enabled protocol passes", func(t *testing.T) {
		o := Options{ProtocolsEnabled: map[string]bool{"torrent": true}}
		require.Nil(t, protocolRejection(common.ReleaseInfo{Protocol: common.ProtocolTorrent}, o))
	})
	t.Run("disabled protocol rejects", func(t *testing.T) {
		o := Options{ProtocolsEnabled: map[string]bool{"torrent": false, "usenet": true}}
		got := protocolRejection(common.ReleaseInfo{Protocol: common.ProtocolTorrent}, o)
		require.NotNil(t, got)
		require.Equal(t, common.RejectionPermanent, got.Type)
	})
	t.Run("a protocol missing from the map is disabled (fail closed)", func(t *testing.T) {
		o := Options{ProtocolsEnabled: map[string]bool{}}
		require.NotNil(t, protocolRejection(common.ReleaseInfo{Protocol: common.ProtocolUsenet}, o))
	})
}

func TestAvailabilityRejection(t *testing.T) {
	t.Run("unavailable and not user-invoked rejects", func(t *testing.T) {
		got := availabilityRejection(Target{Available: false}, Options{UserInvoked: false})
		require.NotNil(t, got)
	})
	t.Run("unavailable but user-invoked is skipped -- naming.md §A6: interactive search skips monitored-type checks", func(t *testing.T) {
		require.Nil(t, availabilityRejection(Target{Available: false}, Options{UserInvoked: true}))
	})
	t.Run("available passes regardless", func(t *testing.T) {
		require.Nil(t, availabilityRejection(Target{Available: true}, Options{UserInvoked: false}))
	})
}

func TestQualityRejections(t *testing.T) {
	bluray1080, _ := quality.Lookup("video", "Bluray-1080p")
	webdl720, _ := quality.Lookup("video", "WEBDL-720p")
	p := quality.Profile{Tiers: [][]quality.Definition{{bluray1080}}, MinFormatScore: 10}

	t.Run("allowed quality, score above minimum", func(t *testing.T) {
		require.Empty(t, qualityRejections(p, common.ReleaseInfo{Quality: bluray1080.Quality}, 10))
	})
	t.Run("quality not in any tier", func(t *testing.T) {
		got := qualityRejections(p, common.ReleaseInfo{Quality: webdl720.Quality}, 10)
		require.Len(t, got, 1)
		require.Contains(t, got[0].Reason, ReasonQualityNotWanted.Code)
	})
	t.Run("score below MinFormatScore", func(t *testing.T) {
		got := qualityRejections(p, common.ReleaseInfo{Quality: bluray1080.Quality}, 9)
		require.Len(t, got, 1)
		require.Contains(t, got[0].Reason, ReasonCustomFormatMinimumScore.Code)
	})
	t.Run("both fail at once", func(t *testing.T) {
		got := qualityRejections(p, common.ReleaseInfo{Quality: webdl720.Quality}, 0)
		require.Len(t, got, 2)
	})
}
