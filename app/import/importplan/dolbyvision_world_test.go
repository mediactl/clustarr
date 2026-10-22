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

package importplan_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// TestASeasonPackOfDolbyVisionEpisodesImportsNothing: talos, 2026-10-22, a
// Netflix Dolby Vision profile 5 season pack whose title named no Dolby
// Vision replaced ten BluRay episodes; only movies were held to the rule.
func TestASeasonPackOfDolbyVisionEpisodesImportsNothing(t *testing.T) {
	wd := newWorld(t)
	root := wd.payload(map[string]int64{
		"The.Wire.S01.1080p.NF.WEB-DL.x265-GRP/The.Wire.S01E01.1080p.NF.WEB-DL.x265-GRP.mkv": big,
		"The.Wire.S01.1080p.NF.WEB-DL.x265-GRP/The.Wire.S01E02.1080p.NF.WEB-DL.x265-GRP.mkv": big,
	})
	for _, f := range []string{"The.Wire.S01E01.1080p.NF.WEB-DL.x265-GRP.mkv", "The.Wire.S01E02.1080p.NF.WEB-DL.x265-GRP.mkv"} {
		wd.probes[f] = &commonv1.MediaInfo{VideoCodec: "hevc", Width: 1920, Height: 1080, Hdr: commonv1.HdrFormatDolbyVision}
	}
	g := wd.grab(commonv1.MediaKindSeries, "the-wire", "The.Wire.S01.1080p.NF.WEB-DL.x265-GRP", root)

	dec := g.inspected()

	assert.Equal(t, commonv1.ImportClassReleaseFault, dec.Class, dec.Summary.Message)
	assert.Empty(t, dec.Materialise, "no Dolby Vision episode is imported")
	assert.Contains(t, dec.Summary.Message, "Dolby Vision")
}

// TestAnHDR10EpisodeStillImports: the rule refuses Dolby Vision, not HDR.
func TestAnHDR10EpisodeStillImports(t *testing.T) {
	wd := newWorld(t)
	root := wd.payload(map[string]int64{
		"The.Wire.S01E01.1080p.BluRay.x265-GRP/The.Wire.S01E01.1080p.BluRay.x265-GRP.mkv": big,
	})
	wd.probes["The.Wire.S01E01.1080p.BluRay.x265-GRP.mkv"] = &commonv1.MediaInfo{VideoCodec: "hevc", Width: 1920, Height: 1080, Hdr: commonv1.HdrFormatHDR10}
	g := wd.grab(commonv1.MediaKindSeries, "the-wire", "The.Wire.S01E01.1080p.BluRay.x265-GRP", root)

	g.inspected()

	mv := g.plan().Plan.Moves
	if assert.Len(t, mv, 1) {
		assert.Equal(t, "the-wire-s01e01", mv[0].Target.Name)
	}
}
