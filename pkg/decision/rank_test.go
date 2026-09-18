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

package decision_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/decision"
)

func TestRankPrimaryKey(t *testing.T) {
	t.Run("lower QualityIndex (better quality) ranks first", func(t *testing.T) {
		worse := decision.Decision{Release: common.ReleaseInfo{GUID: "worse"}, Rank: decision.RankKey{QualityIndex: 1}}
		better := decision.Decision{Release: common.ReleaseInfo{GUID: "better"}, Rank: decision.RankKey{QualityIndex: 0}}
		ranked := decision.Rank([]decision.Decision{worse, better}, decision.Options{})
		require.Equal(t, "better", ranked[0].Release.GUID)
	})

	t.Run("same quality: higher revision (proper/repack) ranks first when PreferRevision", func(t *testing.T) {
		v1 := decision.Decision{Release: common.ReleaseInfo{GUID: "v1"}, Rank: decision.RankKey{PreferRevision: true, Revision: common.Revision{Version: 1}}}
		v2 := decision.Decision{Release: common.ReleaseInfo{GUID: "v2-proper"}, Rank: decision.RankKey{PreferRevision: true, Revision: common.Revision{Version: 2}}}
		ranked := decision.Rank([]decision.Decision{v1, v2}, decision.Options{})
		require.Equal(t, "v2-proper", ranked[0].Release.GUID)
	})

	t.Run("PreferRevision false: revision is not a tiebreak (doNotPrefer)", func(t *testing.T) {
		v1 := decision.Decision{Release: common.ReleaseInfo{GUID: "v1"}, Rank: decision.RankKey{PreferRevision: false, Revision: common.Revision{Version: 1}}}
		v2 := decision.Decision{Release: common.ReleaseInfo{GUID: "v2"}, Rank: decision.RankKey{PreferRevision: false, Revision: common.Revision{Version: 2}}}
		ranked := decision.Rank([]decision.Decision{v2, v1}, decision.Options{}) // stable: original order preserved
		require.Equal(t, "v2", ranked[0].Release.GUID)
	})

	t.Run("same quality and revision: higher custom-format score ranks first", func(t *testing.T) {
		low := decision.Decision{Release: common.ReleaseInfo{GUID: "low"}, Rank: decision.RankKey{FormatScore: 10}}
		high := decision.Decision{Release: common.ReleaseInfo{GUID: "high"}, Rank: decision.RankKey{FormatScore: 50}}
		ranked := decision.Rank([]decision.Decision{low, high}, decision.Options{})
		require.Equal(t, "high", ranked[0].Release.GUID)
	})
}
