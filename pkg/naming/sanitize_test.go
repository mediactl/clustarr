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
