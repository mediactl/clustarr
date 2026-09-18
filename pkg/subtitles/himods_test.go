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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/subtitles"
)

func TestRemoveHIStripsBracketedSoundCueLines(t *testing.T) {
	tests := []struct{ in, want string }{
		{"[door slams]", ""},
		{"(phone ringing)", ""},
		{"Hello, how are you?", "Hello, how are you?"},
		{"I heard [a noise] outside.", "I heard outside."},
	}
	for _, tt := range tests {
		got, err := subtitles.RemoveHI(tt.in)
		require.NoError(t, err)
		assert.Equal(t, tt.want, collapseSpaces(got))
	}
}

func TestRemoveHIStripsSpeakerLabels(t *testing.T) {
	got, err := subtitles.RemoveHI("JOHN: Get down!")
	require.NoError(t, err)
	assert.Equal(t, "Get down!", collapseSpaces(got))
}

func TestRemoveHIStripsAllCapsSoundCueLines(t *testing.T) {
	got, err := subtitles.RemoveHI("LAUGHTER AND APPLAUSE")
	require.NoError(t, err)
	assert.Empty(t, collapseSpaces(got))
}

func TestRemoveHIKeepsAllCapsLinesWithoutASoundCueKeyword(t *testing.T) {
	// research note §6.3: HI_all_caps only fires when a sound-cue keyword
	// (LAUGH, MUSIC, DOOR, ...) is present — plain shouted dialogue in caps
	// must survive.
	got, err := subtitles.RemoveHI("GET DOWN NOW")
	require.NoError(t, err)
	assert.Equal(t, "GET DOWN NOW", collapseSpaces(got))
}

func collapseSpaces(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
