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
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/resource"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

func smallFloor(qualities ...string) []catalogv1alpha1.SizeLimit {
	out := make([]catalogv1alpha1.SizeLimit, 0, len(qualities))
	for _, q := range qualities {
		floor := resource.MustParse("5")
		out = append(out, catalogv1alpha1.SizeLimit{Quality: q, MinMBPerMinute: &floor})
	}
	return out
}

// The owner's transcode-first profile (2026-10-28): any 1080p source, the
// smallest first within a codec class. HEVC is copied, not encoded, so it
// ranks first (x265-hd +300; a 10-bit HEVC keeps +150 after the 10bit
// format's -150); an 8-bit H.264 is the fallback (0); the two codecs this
// cluster's GPUs cannot decode, so every one falls back to a CPU transcode,
// are rejected: a 10-bit H.264 (-150) and AV1 (the default -10000).
func TestATranscodeFirstProfileRanksByCodecThenSize(t *testing.T) {
	cat := catalogue.LoadedCatalogue()
	p, errs := quality.FromCRD(&catalogv1alpha1.QualityProfile{Spec: catalogv1alpha1.QualityProfileSpec{
		MediaKind: catalogv1alpha1.ProfileMediaKindVideo,
		Tiers: []catalogv1alpha1.Tier{{Name: "1080p", Qualities: []string{
			"WEBDL-1080p", "WEBRip-1080p", "HDTV-1080p", "Bluray-1080p",
		}}},
		Cutoff:         "1080p",
		FormatScores:   []catalogv1alpha1.FormatScore{{Format: "x265-hd", Score: 300}, {Format: "10bit", Score: -150}},
		MinFormatScore: 0,
		SizeTable:      catalogv1alpha1.SizeTableMovie,
		// TRaSH's minimums are x264's (Bluray-1080p ~53 MB a minute), which a
		// small HEVC encode falls below; 5 MB a minute still keeps out fakes
		// and samples.
		SizeLimits:     smallFloor("WEBDL-1080p", "WEBRip-1080p", "HDTV-1080p", "Bluray-1080p"),
		PreferSmallest: true,
	}}, cat)
	require.Empty(t, errs)

	rel := func(title string, size int64) common.ReleaseInfo {
		return common.ReleaseInfo{GUID: "idx:" + title, IndexerRef: "idx", Protocol: common.ProtocolUsenet, Title: title, SizeBytes: size}
	}
	ds := decision.Evaluate(context.Background(), rushmoreTarget(nil), p, cat, []common.ReleaseInfo{
		rel("Rushmore.1998.1080p.BluRay.x265-BIG", 12<<30),
		rel("Rushmore.1998.1080p.WEB-DL.DDP5.1.H.265-SMALL", 3<<30),
		rel("Rushmore.1998.1080p.BluRay.10bit.x265-TENBIT", 2<<30),
		rel("Rushmore.1998.1080p.WEB-DL.DDP5.1.H.264-AVC", 1<<30),
		rel("Rushmore.1998.1080p.BluRay.Hi10P.x264-HITEN", 1<<30),
		rel("Rushmore.1998.1080p.WEB-DL.AV1-AVONE", 1<<30),
		rel("Rushmore.1998.1080p.WEB-DL.x265-FAKE", 300<<20),
	}, usenetOnly)
	require.Len(t, ds, 7)

	var approved []decision.Decision
	for _, d := range ds {
		switch d.Release.Title {
		case "Rushmore.1998.1080p.BluRay.Hi10P.x264-HITEN", "Rushmore.1998.1080p.WEB-DL.AV1-AVONE":
			require.False(t, d.Approved, "%s falls back to a CPU transcode", d.Release.Title)
			continue
		case "Rushmore.1998.1080p.WEB-DL.x265-FAKE":
			require.False(t, d.Approved, "300 MB for a 110-minute film is under the floor")
			continue
		}
		require.True(t, d.Approved, "%s: %v", d.Release.Title, d.Rejections)
		approved = append(approved, d)
	}
	var order []string
	for _, d := range decision.Rank(approved, usenetOnly) {
		order = append(order, d.Release.Title)
	}
	require.Equal(t, []string{
		"Rushmore.1998.1080p.WEB-DL.DDP5.1.H.265-SMALL", // 8-bit HEVC, the smaller
		"Rushmore.1998.1080p.BluRay.x265-BIG",
		"Rushmore.1998.1080p.BluRay.10bit.x265-TENBIT", // 10-bit HEVC: copied too, but below 8-bit
		"Rushmore.1998.1080p.WEB-DL.DDP5.1.H.264-AVC",  // 8-bit H.264: the GPU encodes it
	}, order)
}
