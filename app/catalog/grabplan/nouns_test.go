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

package grabplan_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/grabplan"
)

// TestCoverageRejectionsNameTheCoveredKind is 2026-11-10 design D10 and
// §9.4: a release-level refusal names the covered items' kind and their
// container -- an issue of the comic for a volume search's Issues, where
// the Search controller's volume modal and the planner's grab check both
// show it -- and the series wording stays byte for byte what it was.
func TestCoverageRejectionsNameTheCoveredKind(t *testing.T) {
	for _, tc := range []struct {
		name           string
		items          []grabplan.ItemCheck
		episode, issue string
	}{
		{
			"no covered item", nil,
			"WrongItem: the release covers no episode of the series",
			"WrongItem: the release covers no issue of the comic",
		},
		{
			"a live pack holds one", []grabplan.ItemCheck{{Live: true, PackLive: true}},
			"PackDownloading: the release covers an episode a pack still downloading holds; it is judged again once the pack has imported or failed",
			"PackDownloading: the release covers an issue a pack still downloading holds; it is judged again once the pack has imported or failed",
		},
		{
			"a pack over a single carrying every wanted language", []grabplan.ItemCheck{{Queued: true}, {}},
			"EpisodeDownloading: the pack covers an episode a grab carrying every wanted language is still downloading, and the pack does not carry them",
			"EpisodeDownloading: the pack covers an issue a grab carrying every wanted language is still downloading, and the pack does not carry them",
		},
		{
			"a live grab of equal preference", []grabplan.ItemCheck{{Live: true}},
			"QueueHigherPreference: the release covers an episode a grab of equal or higher preference is already downloading",
			"QueueHigherPreference: the release covers an issue a grab of equal or higher preference is already downloading",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for kind, want := range map[commonv1.MediaKind]string{commonv1.MediaKindEpisode: tc.episode, commonv1.MediaKindIssue: tc.issue} {
				got := grabplan.CoverageRejections(kind, tc.items, 1, false, false, false)
				require.Len(t, got, 1, kind)
				require.Equal(t, want, got[0].Reason, kind)
				require.Equal(t, commonv1.RejectionPermanent, got[0].Type, kind)
			}
		})
	}
	require.Nil(t, grabplan.CoverageRejections(commonv1.MediaKindIssue, []grabplan.ItemCheck{{Live: true}}, 1, false, false, true),
		"override is exempt from the queue rules whatever the kind")
}
