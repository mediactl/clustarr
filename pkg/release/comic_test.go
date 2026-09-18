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
	"github.com/stretchr/testify/require"
)

func TestParseComicAndManga(t *testing.T) {
	tests := []struct {
		name   string
		title  string
		series string
		issue  string
		volume int
		year   int
		format string
		manga  bool
	}{
		// quality.md §7.3: rls parses title as "Saga 001", group as "Empire" —
		// both wrong; series and issue number must be split ourselves.
		{
			"western comic", "Saga 001 (2012) (Digital) (Zone-Empire).cbz",
			"Saga", "001", 0, 2012, "CBZ", false,
		},
		{
			"manga volume+chapter", "One Piece v107 c1088 (2023) (Digital) (LuffyGroup).cbz",
			"One Piece", "1088", 107, 2023, "CBZ", true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := parseComic(tt.title)
			require.NoError(t, err)
			require.NotNil(t, p.Comic)
			assert.Equal(t, tt.series, p.Comic.Series)
			assert.Equal(t, tt.issue, p.Comic.Issue)
			assert.Equal(t, tt.volume, p.Comic.Volume)
			assert.Equal(t, tt.year, p.Comic.Year)
			assert.Equal(t, tt.format, p.Comic.Format)
			assert.Equal(t, tt.manga, p.Comic.Manga)
		})
	}
}
