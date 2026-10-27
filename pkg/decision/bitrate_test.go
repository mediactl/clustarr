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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

// webBitrate is web-1080p with the owner's 1080p range (2026-10-27): 100 kB/s
// to 1 MB/s.
func webBitrate(t *testing.T) (quality.Profile, *catalogue.Catalogue) {
	t.Helper()
	p, cat := builtin(t, "web-1080p")
	p.Bitrates = map[int32]quality.BitrateLimit{1080: {Min: 100_000, Max: 1_000_000}}
	return p, cat
}

func decideSized(t *testing.T, p quality.Profile, cat *catalogue.Catalogue, tg decision.Target, title string, size int64) decision.Decision {
	t.Helper()
	ds := decision.Evaluate(context.Background(), tg, p, cat,
		[]common.ReleaseInfo{{GUID: "idx:" + title, IndexerRef: "idx", Protocol: common.ProtocolUsenet, Title: title, SizeBytes: size}}, usenetOnly)
	require.Len(t, ds, 1)
	return ds[0]
}

// A release is held to the profile's range over the runtime the size check
// uses: Rushmore's 110-minute fallback here.
func TestAReleaseOutsideTheBitrateRangeIsRefused(t *testing.T) {
	p, cat := webBitrate(t)
	const title = "Rushmore.1998.1080p.AMZN.WEB-DL.DDP5.1.H.264-GRP"
	assert.True(t, hasRejection(decideSized(t, p, cat, rushmoreTarget(nil), title, 600_000_000), decision.ReasonBelowMinimumBitrate),
		"600 MB over 110 minutes is about 91 kB/s")
	assert.True(t, hasRejection(decideSized(t, p, cat, rushmoreTarget(nil), title, 7_000_000_000), decision.ReasonAboveMaximumBitrate),
		"7 GB over 110 minutes is about 1.06 MB/s")
	inside := decideSized(t, p, cat, rushmoreTarget(nil), title, 3_000_000_000)
	assert.False(t, hasRejection(inside, decision.ReasonBelowMinimumBitrate) || hasRejection(inside, decision.ReasonAboveMaximumBitrate))
	p.Bitrates = nil
	assert.False(t, hasRejection(decideSized(t, p, cat, rushmoreTarget(nil), title, 600_000_000), decision.ReasonBelowMinimumBitrate),
		"a profile without a range checks none")
}

// A file below the range does not fit the profile: a release inside it
// replaces it though the file is a transcode and the release no upgrade, a
// lower tier the profile takes included (owner, 2026-10-27: a 67 kB/s BiOMA
// encode of The Sopranos S01E03, whose WEB posts were taken down).
func TestAFileOutsideTheBitrateRangeIsReplacedByOneInside(t *testing.T) {
	p, cat := builtin(t, "hd-bluray-web")
	p.Bitrates = map[int32]quality.BitrateLimit{1080: {Min: 100_000, Max: 1_000_000}}
	const bluray = "Rushmore.1998.1080p.BluRay.DD5.1.x264-GRP"
	cur := imported(t, p, cat, bluray, "hevc", common.HdrFormatNone)
	cur.Transcoded = true

	cur.BytesPerSecond = 67_000
	d := decideSized(t, p, cat, rushmoreTarget(cur), bluray, 6_200_000_000)
	assert.True(t, d.Approved, "%v", d.Rejections)

	lower := decideSized(t, p, cat, rushmoreTarget(cur), "Rushmore.1998.1080p.AMZN.WEB-DL.DDP5.1.H.264-GRP", 3_000_000_000)
	assert.True(t, lower.Approved, "a lower tier the profile takes replaces it: %v", lower.Rejections)

	cur.BytesPerSecond = 400_000
	inRange := decideSized(t, p, cat, rushmoreTarget(cur), bluray, 6_200_000_000)
	assert.True(t, hasRejection(inRange, decision.ReasonTranscodedFinal), "a file inside the range stays final: %v", inRange.Rejections)
}
