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

package subtitlerequest

import (
	"testing"

	"github.com/stretchr/testify/assert"

	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
)

// MayWant is the SubtitleProfile controller's pre-check: false only when
// the request controller's own planner, given the file's tagged audio and no
// existing subtitle at all, wants nothing -- then no sidecar or embedded
// track could change the answer, and no request is needed. 11,600 of the
// owner's 13,308 requests were exactly that: Satisfied with no items.
func TestMayWant(t *testing.T) {
	english := subtitlev1alpha1.SubtitleProfileSpec{
		Languages: []subtitlev1alpha1.LanguageItem{{Key: "en", Language: "en", AudioExclude: true}},
	}
	probe := func(langs ...string) *commonv1alpha1.MediaInfo {
		mi := &commonv1alpha1.MediaInfo{VideoCodec: "h264"}
		for _, l := range langs {
			mi.Audio = append(mi.Audio, commonv1alpha1.AudioStream{Codec: "aac", Language: l})
		}
		return mi
	}

	assert.False(t, MayWant(english, probe("eng")), "English audio under audioExclude: nothing to want")
	assert.False(t, MayWant(english, probe("en", "spa")), "any English track excludes it")
	assert.True(t, MayWant(english, probe("jpn")), "Japanese audio wants English subtitles")
	assert.True(t, MayWant(english, probe("")), "untagged audio falls back to the item's language, which only the request knows")
	assert.True(t, MayWant(english, probe()), "no audio streams at all: unknown")
	assert.True(t, MayWant(english, nil), "no probe: unknown")

	always := subtitlev1alpha1.SubtitleProfileSpec{Languages: []subtitlev1alpha1.LanguageItem{{Key: "en", Language: "en"}}}
	assert.True(t, MayWant(always, probe("eng")), "without audioExclude English is wanted whatever the audio")

	forced := subtitlev1alpha1.SubtitleProfileSpec{Languages: []subtitlev1alpha1.LanguageItem{{Key: "en:forced", Language: "en", Forced: true}}}
	assert.True(t, MayWant(forced, probe("eng")), "forced English is wanted on English audio")
}
