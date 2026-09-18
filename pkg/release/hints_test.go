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

package release

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseHintsUsesRlsForCodecHdrAudioOnly(t *testing.T) {
	title := "Dune.Part.Two.2024.2160p.UHD.BluRay.REMUX.HDR.HEVC.TrueHD.7.1.Atmos-FraMeSToR"
	h := parseHints(title)
	assert.Contains(t, h.HDR, "HDR")
	assert.Contains(t, h.Audio, "TrueHD")
	assert.Contains(t, h.Audio, "Atmos")
	assert.Equal(t, "7.1", h.Channels)
}

// TestParseHintsStreamingNeverContainsRevisionOrEditionTags is the fix for
// hints.go sourcing Hints.Streaming from rls's catch-all Other field:
// REMUX/REPACK/PROPER land in Other (they're revision/edition tags, not
// streaming services), so a title carrying one of those but no actual
// streaming-service token must report an empty Hints.Streaming.
func TestParseHintsStreamingNeverContainsRevisionOrEditionTags(t *testing.T) {
	titles := []string{
		"Dune.Part.Two.2024.2160p.UHD.BluRay.REMUX.HDR.HEVC.TrueHD.7.1.Atmos-FraMeSToR",
		"Poor.Things.2023.REPACK.2160p.WEB-DL.DDP5.1.Atmos.H.265-GROUP",
	}
	for _, title := range titles {
		h := parseHints(title)
		assert.NotContains(t, h.Streaming, "REMUX")
		assert.NotContains(t, h.Streaming, "REPACK")
		assert.Empty(t, h.Streaming, "%q has no real streaming-service token", title)
	}
}

// TestParseHintsStreamingRecognizesKnownServiceVocabulary verifies the
// positive side: a title that does carry a real streaming-service token
// reports it in Hints.Streaming.
func TestParseHintsStreamingRecognizesKnownServiceVocabulary(t *testing.T) {
	tests := []struct {
		title string
		want  string
	}{
		{"Oppenheimer.2023.1080p.AMZN.WEB-DL.DDP5.1.H.264-FLUX", "AMZN"},
		{"Severance.S02E03.Chikhai.Bardo.1080p.ATVP.WEB-DL.DDP5.1.Atmos.H.264-NTb", "ATVP"},
		{"Stranger.Things.S04E09.The.Piggyback.720p.NF.WEBRip.x264-GROUP", "NF"},
		{"The.Mandalorian.S01E01.1080p.DSNP.WEB-DL.DDP5.1.H.264-NTb", "DSNP"},
		{"The.Bear.S03E01E02.720p.HULU.WEB-DL.DDP5.1.H.264-NTb", "HULU"},
		{"House.of.the.Dragon.S01E01.2160p.HMAX.WEB-DL.DDP5.1.Atmos.H.265-NTb", "HMAX"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			h := parseHints(tt.title)
			assert.Equal(t, []string{tt.want}, h.Streaming)
		})
	}
}
