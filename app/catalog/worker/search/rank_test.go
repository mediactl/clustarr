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

package search

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/decision"
)

func seedersPtr(n int32) *int32 { return &n }

func approved(guid string, key decision.RankKey, rel commonv1.ReleaseInfo) decision.Decision {
	rel.GUID = guid
	return decision.Decision{Release: rel, Approved: true, Rank: key}
}

func TestRankAndCapOrdersApprovedAheadOfRejectedAndStampsRank(t *testing.T) {
	tests := []struct {
		name string
		in   []decision.Decision
		want []string
	}{
		{
			name: "better quality tier wins",
			in: []decision.Decision{
				approved("worse-tier", decision.RankKey{QualityIndex: 1}, commonv1.ReleaseInfo{}),
				approved("best-tier", decision.RankKey{QualityIndex: 0}, commonv1.ReleaseInfo{}),
			},
			want: []string{"best-tier", "worse-tier"},
		},
		{
			name: "higher format score wins inside a tier",
			in: []decision.Decision{
				approved("low-score", decision.RankKey{FormatScore: 10}, commonv1.ReleaseInfo{}),
				approved("high-score", decision.RankKey{FormatScore: 100}, commonv1.ReleaseInfo{}),
			},
			want: []string{"high-score", "low-score"},
		},
		{
			name: "seeders break a tie between equal torrents",
			in: []decision.Decision{
				approved("low-seeders", decision.RankKey{}, commonv1.ReleaseInfo{
					Protocol: commonv1.ProtocolTorrent, Seeders: seedersPtr(5),
				}),
				approved("high-seeders", decision.RankKey{}, commonv1.ReleaseInfo{
					Protocol: commonv1.ProtocolTorrent, Seeders: seedersPtr(500),
				}),
			},
			want: []string{"high-seeders", "low-seeders"},
		},
		{
			name: "rejected releases follow every approved one, in arrival order",
			in: []decision.Decision{
				{
					Release:    commonv1.ReleaseInfo{GUID: "rejected-a"},
					Rejections: []commonv1.Rejection{{Reason: "quality below cutoff", Type: commonv1.RejectionPermanent}},
				},
				approved("ok", decision.RankKey{}, commonv1.ReleaseInfo{}),
				{
					Release:             commonv1.ReleaseInfo{GUID: "rejected-b"},
					TemporarilyRejected: true,
					Rejections:          []commonv1.Rejection{{Reason: "queue has an equal candidate", Type: commonv1.RejectionTemporary}},
				},
			},
			want: []string{"ok", "rejected-a", "rejected-b"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := RankAndCap(tc.in, decision.Options{}, 100)
			require.Len(t, got, len(tc.want))
			for i, guid := range tc.want {
				require.Equalf(t, guid, got[i].GUID, "position %d", i)
				require.Equalf(t, int32(i+1), got[i].Rank, "position %d rank", i)
			}
		})
	}
}

func TestRankAndCapCarriesTheVerdictOntoTheAPIType(t *testing.T) {
	rejections := []commonv1.Rejection{{Reason: "release group is unwanted", Type: commonv1.RejectionPermanent}}
	got := RankAndCap([]decision.Decision{{
		Release:             commonv1.ReleaseInfo{GUID: "g1", FormatScore: 42, MatchedFormats: []string{"x265"}},
		TemporarilyRejected: true,
		Rejections:          rejections,
	}}, decision.Options{}, 100)

	require.Len(t, got, 1)
	require.False(t, got[0].Approved)
	require.True(t, got[0].TemporarilyRejected)
	require.Equal(t, rejections, got[0].Rejections)
	require.Equal(t, int32(42), got[0].FormatScore)
	require.Equal(t, []string{"x265"}, got[0].MatchedFormats)
}

func TestRankAndCapLimits(t *testing.T) {
	mk := func(n int) []decision.Decision {
		ds := make([]decision.Decision, n)
		for i := range ds {
			ds[i] = approved(fmt.Sprintf("r%d", i), decision.RankKey{}, commonv1.ReleaseInfo{})
		}
		return ds
	}

	require.Len(t, RankAndCap(mk(5), decision.Options{}, 2), 2)
	require.Len(t, RankAndCap(mk(3), decision.Options{}, 10_000), 3,
		"a limit above MaxResults still cannot invent results")
	require.Len(t, RankAndCap(mk(3), decision.Options{}, 0), 3,
		"limit 0 means MaxResults, not 0")
	require.Len(t, RankAndCap(mk(MaxResults+50), decision.Options{}, 0), MaxResults,
		"status.results has MaxItems=200; the apiserver rejects more")
}
