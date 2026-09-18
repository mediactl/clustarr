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
