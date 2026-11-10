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

package naming_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/naming"
)

func TestSanitizePathReplacesIllegalCharacters(t *testing.T) {
	got := naming.SanitizePath(`Colon: Test / Slash \ Back <x> "quote" | pipe ? * end`, naming.SanitizeOptions{ReplaceIllegal: true, IsPath: false})
	require.Equal(t, "Colon- Test - Slash - Back x 'quote' - pipe   end", got)
}

func TestSanitizePathDeletesIllegalCharactersWhenNotReplacing(t *testing.T) {
	got := naming.SanitizePath(`A?B*C`, naming.SanitizeOptions{ReplaceIllegal: false, IsPath: false})
	require.Equal(t, "ABC", got)
}

func TestSanitizePathTrimsTrailingDotsAndSpaces(t *testing.T) {
	got := naming.SanitizePath("Se7en... ", naming.DefaultSanitizeOptions())
	require.Equal(t, "Se7en", got)
}

func TestSanitizePathPreservesUnicodeTitles(t *testing.T) {
	got := naming.SanitizePath("東京物語 (1953)", naming.DefaultSanitizeOptions())
	require.Equal(t, "東京物語 (1953)", got)
}

func TestSanitizePathKeepsSeparatorsWhenIsPath(t *testing.T) {
	got := naming.SanitizePath("movies/The Matrix (1999)/file.mkv", naming.SanitizeOptions{ReplaceIllegal: true, IsPath: true, MaxComponentBytes: 255, MaxTotalBytes: 4096})
	require.Equal(t, "movies/The Matrix (1999)/file.mkv", got)
}

func TestSanitizePathCapsComponentLength(t *testing.T) {
	long := strings.Repeat("A", 300)
	got := naming.SanitizePath(long, naming.SanitizeOptions{MaxComponentBytes: 255, MaxTotalBytes: 4096})
	require.Len(t, got, 255)
}

func TestSanitizePathCapsTotalLengthByShrinkingLastSegment(t *testing.T) {
	longLeaf := strings.Repeat("B", 4090)
	got := naming.SanitizePath("movies/"+longLeaf, naming.SanitizeOptions{IsPath: true, MaxComponentBytes: 4096, MaxTotalBytes: 4096})
	require.LessOrEqual(t, len(got), 4096)
	require.True(t, strings.HasPrefix(got, "movies/"))
}

// TestSanitizePathIsIdempotent: a sanitized path sanitizes to itself, cut
// on a space or a dot included (the component cap and the total cap both
// trim after they cut), since every path the catalogue renders -- a
// controller's status.path, the import's file path -- is sanitized once
// from the same title and the two must agree byte for byte (2026-11-10,
// catalogctx.ItemFolder; the F1 review's finding).
func TestSanitizePathIsIdempotent(t *testing.T) {
	opts := naming.DefaultSanitizeOptions()
	for name, s := range map[string]string{
		"a cut on a space":            strings.Repeat("Long ", 78),
		"a cut on a dot":              strings.Repeat("Long.", 78),
		"a path cut on a space":       "/data/movies/" + strings.Repeat("Long ", 78) + "/file.mkv",
		"a total cap cut on a dot":    "movies/" + strings.Repeat("B.", 2100),
		"illegal characters and dots": "/data/movies/What If...?/What If...?.mkv",
	} {
		once := naming.SanitizePath(s, opts)
		require.Equal(t, once, naming.SanitizePath(once, opts), name)
		require.Equal(t, strings.TrimRight(once, ". "), once, "%s: no trailing dot or space survives", name)
	}
	total := naming.SanitizeOptions{IsPath: true, MaxComponentBytes: 4096, MaxTotalBytes: 4096}
	s := "movies/" + strings.Repeat("B ", 2100)
	once := naming.SanitizePath(s, total)
	require.Equal(t, once, naming.SanitizePath(once, total), "the total cap's cut")
}
