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
package segmentplan_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/segmentplan"
	"github.com/mediactl/clustarr/pkg/segments"
)

// TestDue moved verbatim from pkg/segments (W7.4: Due moved here, since
// pkg/segments no longer links the API).
func TestDue(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	file := func(kind commonv1.MediaKind, probed bool, a *catalogv1alpha1.SegmentAnalysis) *catalogv1alpha1.MediaFile {
		mf := &catalogv1alpha1.MediaFile{}
		mf.Spec.MediaRef = commonv1.MediaRef{Kind: kind, Name: "x"}
		if probed {
			mf.Status.ProbeHash = "h1"
			mf.Status.MediaInfo = &commonv1.MediaInfo{RuntimeMillis: 1}
		}
		if a != nil {
			mf.Status.Markers = &catalogv1alpha1.FileMarkers{Analysis: a}
		}
		return mf
	}
	an := func(r catalogv1alpha1.MarkersResult, hash string, v int32, ago time.Duration) *catalogv1alpha1.SegmentAnalysis {
		return &catalogv1alpha1.SegmentAnalysis{Result: r, ForProbeHash: hash, Version: v, AnalyzedAt: metav1.NewTime(now.Add(-ago))}
	}
	v := segments.AnalyzerVersion
	tests := []struct {
		name string
		mf   *catalogv1alpha1.MediaFile
		due  bool
	}{
		{"unprobed", file(commonv1.MediaKindEpisode, false, nil), false},
		{"an album", file(commonv1.MediaKindAlbum, true, nil), false},
		{"never analyzed", file(commonv1.MediaKindEpisode, true, nil), true},
		{"a movie never analyzed", file(commonv1.MediaKindMovie, true, nil), true},
		{"a new probe", file(commonv1.MediaKindEpisode, true, an(catalogv1alpha1.MarkersFound, "h0", v, time.Hour)), true},
		{"an older analyzer", file(commonv1.MediaKindEpisode, true, an(catalogv1alpha1.MarkersFound, "h1", v-1, time.Hour)), true},
		{"found, current", file(commonv1.MediaKindEpisode, true, an(catalogv1alpha1.MarkersFound, "h1", v, 400*24*time.Hour)), false},
		{"not found, current: never on a timer", file(commonv1.MediaKindEpisode, true, an(catalogv1alpha1.MarkersNotFound, "h1", v, 400*24*time.Hour)), false},
		{"an error an hour ago", file(commonv1.MediaKindEpisode, true, an(catalogv1alpha1.MarkersError, "h1", v, time.Hour)), false},
		{"an error a day ago", file(commonv1.MediaKindEpisode, true, an(catalogv1alpha1.MarkersError, "h1", v, 24*time.Hour)), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { assert.Equal(t, tt.due, segmentplan.Due(tt.mf, now)) })
	}
}
