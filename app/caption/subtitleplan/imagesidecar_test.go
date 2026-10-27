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

package subtitleplan

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// A .sup beside the file -- the PGS track the MP4 standard kept for the
// OCR to come (standard.Version 8) -- is no subtitle a player reads: the
// language still wants a text one, so it is not an existing subtitle,
// where the .srt beside it is.
func TestAnImageSidecarIsNoExistingSubtitle(t *testing.T) {
	got := sidecarExisting([]string{"Movie (1999).en.sup", "Movie (1999).fr.srt", "Movie (1999).mp4"}, "Movie (1999).mp4")
	var keys []string
	for _, e := range got {
		keys = append(keys, e.langKey)
	}
	assert.Equal(t, []string{"fr"}, keys, "%+v", got)
}
