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

package metadata_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/metadata"
)

func TestRefreshTTLPortsTheArrHeuristics(t *testing.T) {
	fresh := time.Now()
	tests := []struct {
		name  string
		kind  commonv1.MediaKind
		state string
		want  time.Duration
	}{
		{"movie announced", commonv1.MediaKindMovie, metadata.RefreshStateAnnounced, 12 * time.Hour},
		{"movie in cinemas", commonv1.MediaKindMovie, metadata.RefreshStateInCinemas, 12 * time.Hour},
		{"movie released recently", commonv1.MediaKindMovie, metadata.RefreshStateReleasedRecent, 24 * time.Hour},
		{"movie released long ago", commonv1.MediaKindMovie, metadata.RefreshStateReleasedOld, 7 * 24 * time.Hour},
		{"series continuing", commonv1.MediaKindSeries, metadata.RefreshStateContinuing, 6 * time.Hour},
		{"series ended recently", commonv1.MediaKindSeries, metadata.RefreshStateEndedRecent, 24 * time.Hour},
		{"series ended long ago", commonv1.MediaKindSeries, metadata.RefreshStateEndedOld, 30 * 24 * time.Hour},
		{"album", commonv1.MediaKindAlbum, metadata.RefreshStateActive, 7 * 24 * time.Hour},
		{"book", commonv1.MediaKindBook, metadata.RefreshStateActive, 30 * 24 * time.Hour},
		{"comic ongoing", commonv1.MediaKindComic, metadata.RefreshStateOngoing, 24 * time.Hour},
		{"comic completed", commonv1.MediaKindComic, metadata.RefreshStateCompleted, 30 * 24 * time.Hour},
		{"search results", commonv1.MediaKindMovie, metadata.RefreshStateSearch, time.Hour},
		{"id crosswalk", commonv1.MediaKindMovie, metadata.RefreshStateCrosswalk, 90 * 24 * time.Hour},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, metadata.RefreshTTL(tt.kind, tt.state, fresh))
		})
	}
}

func TestRefreshTTLForcesAnImmediateRefreshPastTheHardCap(t *testing.T) {
	stale := time.Now().Add(-200 * 24 * time.Hour) // past the 180-day hard cap

	got := metadata.RefreshTTL(commonv1.MediaKindMovie, metadata.RefreshStateReleasedOld, stale)

	require.Zero(t, got, "a record untouched for 200 days must refresh immediately regardless of its bucket")
}
