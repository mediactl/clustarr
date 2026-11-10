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

package grabplan

import (
	"testing"

	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// TestACoveredItemsOwnRejectionsNameItsKind (2026-11-10 design D10): the
// rejections an automatic candidate's covered item adds name the item's
// kind too -- an issue not monitored or not released, an episode not
// monitored or not aired, the latter byte for byte as before -- and an
// owner covered by itself keeps the series wording.
func TestACoveredItemsOwnRejectionsNameItsKind(t *testing.T) {
	require.Equal(t, "NotMonitored: the episode is not monitored", rejectionNotMonitored(nounsOf(commonv1.MediaKindEpisode)).Reason)
	require.Equal(t, "Availability: the episode has not aired", rejectionNotOut(nounsOf(commonv1.MediaKindEpisode)).Reason)
	require.Equal(t, "NotMonitored: the issue is not monitored", rejectionNotMonitored(nounsOf(commonv1.MediaKindIssue)).Reason)
	require.Equal(t, "Availability: the issue has not been released", rejectionNotOut(nounsOf(commonv1.MediaKindIssue)).Reason)
	require.Equal(t, commonv1.MediaKindIssue, coveredKind(Covered{Ref: schema.ItemRef{Kind: "Issue"}}))
	require.Equal(t, commonv1.MediaKindEpisode, coveredKind(Covered{Ref: schema.ItemRef{Kind: "Episode"}}))
	require.Equal(t, episodeNouns, nounsOf(commonv1.MediaKindMovie), "an owner covered by itself keeps the series wording")
}
