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

// TestAGrabEntryKeepsTheIndexersAudioLanguages: the entry's release is
// what the grab's item-state check reads (ReleaseInfo), so it keeps the
// languages the indexer reported, as the search read them.
func TestAGrabEntryKeepsTheIndexersAudioLanguages(t *testing.T) {
	rel := commonv1.ReleaseInfo{
		Title: "Akira.1988.BluRay.1080p.DDP.Atmos.5.1.x264-hallowed", Languages: []string{"English", "Japanese"},
		IndexerLanguages: []string{"English", "Japanese"},
	}
	got := grabplan.ReleaseInfo(grabplan.DownloadRelease(rel, 0))
	require.Equal(t, []string{"English", "Japanese"}, got.IndexerLanguages)
}
