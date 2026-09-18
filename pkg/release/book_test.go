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

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

func TestParseBookAndAudiobook(t *testing.T) {
	tests := []struct {
		name       string
		title      string
		kind       commonv1.MediaKind
		author     string
		bookTitle  string
		format     string
		narrator   string
		asin       string
		unabridged bool
	}{
		{
			"ebook", "Andy Weir - Project Hail Mary (2021) [EPUB]",
			commonv1.MediaKindBook, "Andy Weir", "Project Hail Mary", "EPUB", "", "", false,
		},
		// quality.md §7.3: rls returns type unknown, title unparsed, for this one.
		{
			"audiobook unabridged", "Andy Weir - Project Hail Mary (Unabridged) [M4B 64kbps]",
			commonv1.MediaKindAudiobook, "Andy Weir", "Project Hail Mary", "M4B", "", "", true,
		},
		{
			"audiobook with narrator and asin",
			"Project Hail Mary - Andy Weir {Ray Porter} [ASIN B08GB59RY8] [M4B]",
			commonv1.MediaKindAudiobook, "Andy Weir", "Project Hail Mary", "M4B", "Ray Porter", "B08GB59RY8", false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := parseBook(tt.title, tt.kind)
			require.NoError(t, err)
			require.NotNil(t, p.Book)
			assert.Equal(t, tt.author, p.Book.Author)
			assert.Equal(t, tt.bookTitle, p.Book.Title)
			assert.Equal(t, tt.format, p.Book.Format)
			assert.Equal(t, tt.narrator, p.Book.Narrator)
			assert.Equal(t, tt.asin, p.Book.ASIN)
			assert.Equal(t, tt.unabridged, p.Book.Unabridged)
		})
	}
}
