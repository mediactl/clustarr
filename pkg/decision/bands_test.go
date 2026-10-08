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

// TestBandOfPutsIdentityRejectionsLast is series search design §8.2: the
// identity rejections (WrongItem, UnknownItem, UnableToParse) band a release
// with the other titles, whatever else it carries; FullSeason, quality and
// item-state rejections band it as rejected. Each verdict is a real
// release's, from Evaluate.
func TestBandOfPutsIdentityRejectionsLast(t *testing.T) {
	single := decision.Target{Kind: common.MediaKindEpisode, Available: true, Identity: decision.Identity{
		Titles: []string{"Breaking Bad"}, Season: 1, Episodes: []int{5}, SingleEpisodeSearch: true,
	}}
	band := func(title string) decision.ResultBand {
		d := evaluateOne(t, single, title, textIndexer, nil)
		return decision.BandOf(d.Approved, d.Rejections)
	}
	require.Equal(t, decision.BandApproved, band("Breaking.Bad.S01E05.720p.HDTV.x264-GRP"))
	require.Equal(t, decision.BandOtherTitle, band("Better.Call.Saul.S01E05.720p.HDTV.x264-GRP"), "WrongItem")
	require.Equal(t, decision.BandOtherTitle, band("Breaking.Bad.S02E05.720p.HDTV.x264-GRP"), "WrongItem by numbering")
	require.Equal(t, decision.BandRejected, band("Breaking.Bad.S01.720p.BluRay.x264-GRP"), "FullSeason is the right series")

	unknown := single
	unknown.Identity.Titles = nil
	d := evaluateOne(t, unknown, "Breaking.Bad.S01E05.720p.HDTV.x264-GRP", textIndexer, nil)
	require.Equal(t, decision.BandOtherTitle, decision.BandOf(d.Approved, d.Rejections), "UnknownItem: %+v", d.Rejections)

	unparsed := common.Rejection{Reason: decision.ReasonUnableToParse.Code + ": no", Type: common.RejectionPermanent}
	quality := common.Rejection{Reason: decision.ReasonQualityNotWanted.Code + ": WEBRip-480p", Type: common.RejectionPermanent}
	wrong := common.Rejection{Reason: decision.ReasonWrongItem.Code + ": other", Type: common.RejectionPermanent}
	require.Equal(t, decision.BandOtherTitle, decision.BandOf(false, []common.Rejection{unparsed}))
	require.Equal(t, decision.BandRejected, decision.BandOf(false, []common.Rejection{quality}))
	require.Equal(t, decision.BandOtherTitle, decision.BandOf(false, []common.Rejection{quality, wrong}), "an identity rejection wins over any other")
	require.Equal(t, decision.BandRejected, decision.BandOf(true, []common.Rejection{decision.FullSeasonNotAired(1)}),
		"an item-state rejection added after approval moves the release to rejected, never to other titles")

	require.Equal(t, []string{"approved", "rejected", "otherTitles"},
		[]string{decision.BandApproved.String(), decision.BandRejected.String(), decision.BandOtherTitle.String()})
}
