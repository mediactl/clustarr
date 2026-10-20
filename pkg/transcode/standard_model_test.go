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

package transcode_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mediactl/clustarr/pkg/transcode"
)

// A source with a hundred subtitle streams -- an Apple TV+ WEB-DL's track
// per language and kind -- is planned; one at the cap of 128 is refused,
// since the summary the planner reads cannot say whether any were cut.
func TestTooManyStreamsRefusesOnlyAtTheCap(t *testing.T) {
	subs := func(n int) transcode.MediaInfo {
		info := transcode.MediaInfo{}
		for i := range n {
			info.Subtitles = append(info.Subtitles, transcode.SubtitleStream{Index: int32(1 + i), Codec: "mov_text"})
		}
		return info
	}
	assert.Empty(t, transcode.TooManyStreams(subs(100)))
	assert.Contains(t, transcode.TooManyStreams(subs(128)), "128 or more subtitle streams")
}
