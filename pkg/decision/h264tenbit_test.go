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
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/decision"
)

// H.264 10-bit is the one codec the transcoder cannot take (owner,
// 2026-11-02: "the only real *off-limits* codec is encoding or decoding
// H.264 10-bit"): a 10bit or Hi10P title naming no other codec is
// clustarr's h264-10bit, refused under every profile as AV1 is, where an
// x265 10-bit release, which QSV decodes, and 8-bit H.264 are not.
func TestAnH264TenBitReleaseIsRefusedAndX265TenBitIsNot(t *testing.T) {
	for _, profile := range []string{"hd-bluray-web", "anime-remux-1080p"} {
		p, cat := builtin(t, profile)
		for title, refused := range map[string]bool{
			"Rushmore.1998.1080p.BluRay.10bit.x264.DTS-GROUP": true,
			"Rushmore.1998.1080p.BluRay.Hi10P.FLAC-GROUP":     true,
			"[Group] Rushmore (1998) [BD 1080p 10-bit FLAC]":  true,
			"Rushmore.1998.1080p.BluRay.10bit.x265.DTS-GROUP": false,
			"Rushmore.1998.1080p.BluRay.HEVC.10bit.DTS-GROUP": false,
			"Rushmore.1998.1080p.BluRay.AV1.10bit.Opus-GROUP": false, // AV1's own format refuses it
			"Rushmore.1998.1080p.BluRay.x264.DTS-GROUP":       false,
		} {
			got := decide(t, p, cat, rushmoreTarget(nil), title)
			matched := slices.Contains(got.Release.MatchedFormats, "h264-10bit")
			assert.Equal(t, refused, matched, "%s under %s: %v", title, profile, got.Release.MatchedFormats)
			if refused {
				assert.False(t, got.Approved, "%s under %s", title, profile)
				assert.True(t, hasRejection(got, decision.ReasonCustomFormatMinimumScore), "%s under %s: %v", title, profile, reasons(got))
			}
		}
	}
}

// An H.264 10-bit file does not meet the cutoff (owner, 2026-11-03: "These
// files shouldn't meet the cutoff - or should be upgraded"): a release the
// profile takes replaces it, a lower tier included, though it is no upgrade
// over the file's quality, where the same file in 8-bit stays.
func TestAnH264TenBitFileIsReplacedByAnyReleaseTheProfileTakes(t *testing.T) {
	p, cat := builtin(t, "hd-bluray-web")
	const bluray = "Rushmore.1998.1080p.BluRay.DD5.1.x264-GRP"
	const web = "Rushmore.1998.1080p.AMZN.WEB-DL.DDP5.1.H.264-GRP"
	cur := imported(t, p, cat, bluray, "h264", common.HdrFormatNone)

	same := decide(t, p, cat, rushmoreTarget(cur), bluray)
	assert.False(t, same.Approved, "an 8-bit file at the cutoff stays: %v", reasons(same))

	cur.H264TenBit = true
	same = decide(t, p, cat, rushmoreTarget(cur), bluray)
	assert.True(t, same.Approved, "%v", reasons(same))
	lower := decide(t, p, cat, rushmoreTarget(cur), web)
	assert.True(t, lower.Approved, "a lower tier the profile takes replaces it: %v", reasons(lower))
	hi10p := decide(t, p, cat, rushmoreTarget(cur), "Rushmore.1998.1080p.BluRay.10bit.x264.DTS-GROUP")
	assert.False(t, hi10p.Approved, "never by another H.264 10-bit release")
}
