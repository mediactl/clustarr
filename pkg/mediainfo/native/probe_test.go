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

package native

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mediactl/clustarr/pkg/mediainfo"
)

// FFmpeg 9's AVDOVIDecoderConfigurationRecord is nine bytes: the eight
// ISO/IEC flags and dv_md_compression, which ffprobe prints by name.
func TestDoviRecordReadsEveryField(t *testing.T) {
	got := doviRecord([]byte{1, 0, 8, 3, 1, 0, 1, 1, 1})
	assert.Equal(t, &mediainfo.DoviRecord{
		VersionMajor: 1, Profile: 8, Level: 3, RPUPresent: true, BLPresent: true,
		BLSignalCompatibilityID: 1, MDCompression: "limited",
	}, got)
	assert.Equal(t, "none", doviRecord([]byte{1, 0, 7, 6, 1, 1, 1, 6, 0}).MDCompression)
	assert.Equal(t, "", doviRecord([]byte{1, 0, 5, 3, 1, 0, 1, 0}).MDCompression, "an eight-byte record names none")
}
