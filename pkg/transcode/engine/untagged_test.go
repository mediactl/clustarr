/*
Copyright (C) 2026 The Clustarr Authors

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

package engine

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/obinnaokechukwu/ffgo"

	"github.com/mediactl/clustarr/pkg/transcode/standard"
)

// The engine wrote every output track the source's own language, so a
// track the standard named (standard.nameUntagged) stayed "und". The
// plan's language is written when it differs, and the source's "language"
// tag does not follow it into the output's metadata.
func TestAudioOptionsWriteTheLanguageThePlanNamed(t *testing.T) {
	src := func(language string) *ffgo.StreamInfo {
		return &ffgo.StreamInfo{Language: language, Title: "Surround", Metadata: ffgo.Metadata{"language": language, "title": "Surround", "handler_name": "x"}}
	}
	for _, copied := range []bool{true, false} {
		o := audioOptions(src("und"), standard.AudioPlan{Language: "eng"}, copied)
		assert.Equal(t, "eng", o.Language)
		assert.NotContains(t, o.Metadata, "language")
		assert.Equal(t, "x", o.Metadata["handler_name"], "the source's other tags stay")

		o = audioOptions(src(""), standard.AudioPlan{Language: "eng"}, copied)
		assert.Equal(t, "eng", o.Language)

		o = audioOptions(src("jpn"), standard.AudioPlan{Language: "jpn"}, copied)
		assert.Equal(t, "jpn", o.Language)
		assert.Equal(t, "jpn", o.Metadata["language"], "a track the plan did not rename is written as it was")

		o = audioOptions(src("und"), standard.AudioPlan{}, copied)
		assert.Equal(t, "und", o.Language, "a plan naming no language leaves the source's")
	}
}
