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
	"testing"

	"github.com/stretchr/testify/assert"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/decision"
)

// A file that ends before its own chapters do holds part of the film
// (owner, 2026-11-25: "Make this self-healing"; The Drama on talos ran 28
// of 106 minutes as a finished transcode): a release the profile takes
// replaces it, though the file is a transcode, which is final under every
// other rule, and though the release is a tier below it.
func TestATruncatedFileIsReplacedByAnyReleaseTheProfileTakes(t *testing.T) {
	p, cat := builtin(t, "hd-bluray-web")
	const bluray = "Rushmore.1998.1080p.BluRay.DD5.1.x264-GRP"
	const web = "Rushmore.1998.1080p.AMZN.WEB-DL.DDP5.1.H.264-GRP"
	cur := imported(t, p, cat, bluray, "hevc", common.HdrFormatNone)
	cur.Transcoded = true

	whole := decide(t, p, cat, rushmoreTarget(cur), web)
	assert.False(t, whole.Approved, "a whole transcode is final: %v", reasons(whole))
	assert.True(t, hasRejection(whole, decision.ReasonTranscodedFinal), "%v", reasons(whole))

	cur.Truncated = true
	same := decide(t, p, cat, rushmoreTarget(cur), bluray)
	assert.True(t, same.Approved, "%v", reasons(same))
	lower := decide(t, p, cat, rushmoreTarget(cur), web)
	assert.True(t, lower.Approved, "a lower tier the profile takes replaces it: %v", reasons(lower))
	sd := decide(t, p, cat, rushmoreTarget(cur), "Rushmore.1998.DVDRip.x264-GRP")
	assert.False(t, sd.Approved, "never a quality the profile does not take")
}
