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
package rss

import (
	"testing"

	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/newznab"
)

// TestKindFromCategories pins when a release's Newznab categories may
// overrule the classifier's video default: only when every standard category
// names one and the same non-video kind.
func TestKindFromCategories(t *testing.T) {
	for _, c := range []struct {
		name string
		cats []newznab.CategoryID
		want commonv1.MediaKind // "" means no refinement
	}{
		{"comics, with the Books root Prowlarr emits beside it", []newznab.CategoryID{7000, 7030}, commonv1.MediaKindComic},
		{"an ebook", []newznab.CategoryID{7020}, commonv1.MediaKindBook},
		{"the Books root alone is a book", []newznab.CategoryID{7000}, commonv1.MediaKindBook},
		{"lossless audio is an album", []newznab.CategoryID{3000, 3040}, commonv1.MediaKindAlbum},
		{"the Audio root alone is an album", []newznab.CategoryID{3000}, commonv1.MediaKindAlbum},
		{"an audiobook, root and all", []newznab.CategoryID{3000, 3030}, commonv1.MediaKindAudiobook},
		{"a tracker's custom category says nothing", []newznab.CategoryID{100042, 7030}, commonv1.MediaKindComic},
		{"only custom categories", []newznab.CategoryID{100042}, ""},
		{"no categories", nil, ""},
		{"a video category among them keeps the classifier's guess", []newznab.CategoryID{2000, 7030}, ""},
		{"a TV category keeps it too", []newznab.CategoryID{5070}, ""},
		{"a music video has no kind", []newznab.CategoryID{3000, 3020}, ""},
		{"an ebook and an audiobook disagree", []newznab.CategoryID{7020, 3030}, ""},
		{"a comic and an ebook disagree", []newznab.CategoryID{7020, 7030}, ""},
		{"two roots disagree", []newznab.CategoryID{3000, 7000}, ""},
		{"Other/Misc is no kind", []newznab.CategoryID{8010}, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, ok := kindFromCategories(c.cats)
			require.Equal(t, c.want != "", ok)
			require.Equal(t, c.want, got)
		})
	}
}
