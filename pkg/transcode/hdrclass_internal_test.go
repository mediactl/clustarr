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

package transcode

import (
	"testing"

	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// hdrClass is unexported, so this table test lives in an internal
// (package transcode) test file rather than pkg/transcode/mediainfo_test.go,
// which is package transcode_test like the rest of this package's tests.
func TestHdrClassCollapsesAllTenCRDValues(t *testing.T) {
	cases := []struct {
		name string
		in   commonv1.HdrFormat
		want hdrBucket
	}{
		{"none", commonv1.HdrFormatNone, hdrNone},
		{"empty string treated as none", commonv1.HdrFormat(""), hdrNone},
		{"pq10 graded as hdr10", commonv1.HdrFormatPQ10, hdrHDR10},
		{"hdr10", commonv1.HdrFormatHDR10, hdrHDR10},
		{"hdr10plus", commonv1.HdrFormatHDR10Plus, hdrHDR10Plus},
		{"hlg10", commonv1.HdrFormatHLG10, hdrHLG},
		{"dolbyVision", commonv1.HdrFormatDolbyVision, hdrDolbyVision},
		{"dolbyVisionHdr10", commonv1.HdrFormatDolbyVisionHDR10, hdrDolbyVision},
		{"dolbyVisionSdr", commonv1.HdrFormatDolbyVisionSDR, hdrDolbyVision},
		{"dolbyVisionHlg", commonv1.HdrFormatDolbyVisionHLG, hdrDolbyVision},
		{"dolbyVisionHdr10Plus", commonv1.HdrFormatDolbyVisionHDR10Plus, hdrDolbyVision},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, hdrClass(tc.in))
		})
	}
}
