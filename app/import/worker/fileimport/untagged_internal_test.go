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

package fileimport

import (
	"testing"

	"github.com/stretchr/testify/assert"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// Panty & Stocking's Blu-ray remux (talos, 2026-11-26): Japanese tagged,
// the English dub untagged, the grab's release listed as English and
// Japanese. The probe named no languages (one track is unknown), the
// title named none, and the file froze as Japanese alone.
func TestAFileWithAnUntaggedTrackTakesTheGrabsLanguages(t *testing.T) {
	probe := func(languages ...string) *commonv1.MediaInfo {
		mi := &commonv1.MediaInfo{}
		for _, l := range languages {
			mi.Audio = append(mi.Audio, commonv1.AudioStream{Language: l})
		}
		return mi
	}
	grabbed := []string{"English", "Japanese"}

	assert.Equal(t, []string{"Japanese", "English"}, untaggedLanguageNames(probe("jpn", "und"), grabbed), "the tagged track's first, then the grab's")
	assert.Equal(t, []string{"Japanese", "English"}, untaggedLanguageNames(probe("jpn", ""), grabbed))
	assert.Equal(t, []string{"French", "English", "Japanese"}, untaggedLanguageNames(probe("fre", "und"), grabbed), "a tagged track the grab did not list is the file's too")
	assert.Nil(t, probedLanguageNames(probe("jpn", "und")), "the probe alone names none")

	assert.Nil(t, untaggedLanguageNames(probe("jpn", "eng"), grabbed), "every track is tagged: the probe names them")
	assert.Nil(t, untaggedLanguageNames(probe("und", ""), grabbed), "no track is tagged: the names' reading stands")
	assert.Nil(t, untaggedLanguageNames(probe("jpn", "und"), nil), "the grab read none")
	assert.Nil(t, untaggedLanguageNames(probe(), grabbed))
	assert.Nil(t, untaggedLanguageNames(nil, grabbed))
}
