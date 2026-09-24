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

package mediainfo

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	ffprobe "gopkg.in/vansante/go-ffprobe.v2"
)

// TestToMediaInfoReadsTheTranscodeProfileTag pins how the CLUSTARR_PROFILE
// container tag reaches MediaInfo.TranscodeProfile, the half of "is this file
// transcoded" a rescan can see (app/catalog/controller/rollup.Transcoded).
func TestToMediaInfoReadsTheTranscodeProfileTag(t *testing.T) {
	const tag = "default@0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	cases := []struct {
		name string
		tags ffprobe.Tags
		want string
	}{
		{name: "no tags at all", tags: nil, want: ""},
		{name: "other tags only", tags: ffprobe.Tags{"ENCODER": "Lavf63.1.101"}, want: ""},
		{name: "Matroska keeps the key as written", tags: ffprobe.Tags{"CLUSTARR_PROFILE": tag, "ENCODER": "Lavf"}, want: tag},
		{name: "a muxer that lower-cases the key", tags: ffprobe.Tags{"clustarr_profile": tag}, want: tag},
		{name: "mixed case", tags: ffprobe.Tags{"Clustarr_Profile": tag}, want: tag},
		{name: "the exact key wins over another spelling", tags: ffprobe.Tags{"CLUSTARR_PROFILE": tag, "clustarr_profile": "other@1"}, want: tag},
		{name: "two other spellings resolve the same way every time", tags: ffprobe.Tags{"clustarr_profile": "b@2", "Clustarr_Profile": "a@1"}, want: "a@1"},
		{name: "surrounding whitespace is trimmed", tags: ffprobe.Tags{"CLUSTARR_PROFILE": "  " + tag + "\n"}, want: tag},
		{name: "a non-string value is no tag", tags: ffprobe.Tags{"CLUSTARR_PROFILE": 42}, want: ""},
		{name: "an empty value is no tag", tags: ffprobe.Tags{"CLUSTARR_PROFILE": ""}, want: ""},
		{name: "exactly the CRD's MaxLength is kept", tags: ffprobe.Tags{"CLUSTARR_PROFILE": strings.Repeat("a", MaxTranscodeProfileLength)}, want: strings.Repeat("a", MaxTranscodeProfileLength)},
		// The apiserver would reject the whole status apply, so the file
		// would never be probed; no squasharr wrote a value this long.
		{name: "one past the CRD's MaxLength is dropped", tags: ffprobe.Tags{"CLUSTARR_PROFILE": strings.Repeat("a", MaxTranscodeProfileLength+1)}, want: ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			raw := &Raw{Format: &ffprobe.Format{Filename: "movie.mkv", TagList: c.tags}}
			for range 20 { // map order is random; the answer must not be
				assert.Equal(t, c.want, toMediaInfo(raw).TranscodeProfile)
			}
		})
	}
}

func TestFormatTagWithoutAFormatIsEmpty(t *testing.T) {
	assert.Empty(t, FormatTag(nil, ProfileTagKey))
	assert.Empty(t, FormatTag(&Raw{}, ProfileTagKey))
}
