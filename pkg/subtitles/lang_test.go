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

package subtitles_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/subtitles"
)

func TestParseLangKeyTableFromTheCRDPattern(t *testing.T) {
	tests := []struct {
		key                subtitles.LangKey
		wantLang           string
		wantForced, wantHI bool
	}{
		{"en", "en", false, false},
		{"en:forced", "en", true, false},
		{"en:hi", "en", false, true},
		{"pt-BR", "pt-BR", false, false},
		{"pt-BR:forced", "pt-BR", true, false},
		{"zh-Hant:hi", "zh-Hant", false, true},
	}
	for _, tt := range tests {
		t.Run(string(tt.key), func(t *testing.T) {
			lang, forced, hi, err := subtitles.ParseLangKey(tt.key)
			require.NoError(t, err)
			assert.Equal(t, tt.wantLang, lang)
			assert.Equal(t, tt.wantForced, forced)
			assert.Equal(t, tt.wantHI, hi)
			assert.Equal(t, tt.key, subtitles.FormatLangKey(lang, forced, hi), "FormatLangKey must invert ParseLangKey")
		})
	}
}

func TestParseLangKeyRejectsForcedAndHITogether(t *testing.T) {
	_, _, _, err := subtitles.ParseLangKey("en:forced:hi")
	assert.Error(t, err, "a langKey may not carry both flags — forced always beats HI (research note §3.2)")
}

func TestParseLangKeyRejectsEmpty(t *testing.T) {
	_, _, _, err := subtitles.ParseLangKey("")
	assert.Error(t, err)
}
