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

package v1alpha1_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// TestHdrFormatDisplayName pins the {MediaInfo VideoDynamicRangeType}
// token's vocabulary for every HdrFormat. "DV HDR10", "HDR10+" and "HLG"
// are pinned by docs/research/naming.md (lines 55, 81, 443); the rest
// follow Radarr's MediaInfoFormatter.FormatVideoDynamicRangeType.
func TestHdrFormatDisplayName(t *testing.T) {
	tests := []struct {
		hdr  commonv1.HdrFormat
		want string
	}{
		{commonv1.HdrFormatNone, ""},
		{commonv1.HdrFormatPQ10, "PQ"},
		{commonv1.HdrFormatHDR10, "HDR10"},
		{commonv1.HdrFormatHDR10Plus, "HDR10+"},
		{commonv1.HdrFormatHLG10, "HLG"},
		{commonv1.HdrFormatDolbyVision, "DV"},
		{commonv1.HdrFormatDolbyVisionHDR10, "DV HDR10"},
		{commonv1.HdrFormatDolbyVisionSDR, "DV SDR"},
		{commonv1.HdrFormatDolbyVisionHLG, "DV HLG"},
		{commonv1.HdrFormatDolbyVisionHDR10Plus, "DV HDR10+"},
	}
	assert.Len(t, tests, 10, "every HdrFormat constant must be covered")
	for _, tt := range tests {
		t.Run(string(tt.hdr), func(t *testing.T) {
			assert.Equal(t, tt.want, tt.hdr.DisplayName())
		})
	}
}

// TestHdrFormatDisplayNameUnknown proves an unrecognised value renders
// empty rather than panicking.
func TestHdrFormatDisplayNameUnknown(t *testing.T) {
	assert.Equal(t, "", commonv1.HdrFormat("nonsense").DisplayName())
}
