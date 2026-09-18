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

package fsops_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/fsops"
)

func TestIsPartMatchesTheAnacrolixPartSuffix(t *testing.T) {
	require.True(t, fsops.IsPart("../../testdata/fsops/classify/Movie.Title.2024.1080p.WEB-DL.mkv.part"))
	require.False(t, fsops.IsPart("../../testdata/fsops/classify/Movie.Title.2024.1080p.WEB-DL.mkv"))
}

func TestIsExtraMatchesAKnownExtrasFolder(t *testing.T) {
	require.True(t, fsops.IsExtra("../../testdata/fsops/classify/behind the scenes/short-clip.mkv"))
	require.True(t, fsops.IsExtra("../../testdata/fsops/classify/samples/Movie.Sample.mkv"),
		"a folder literally named samples is Jellyfin extras content")
	require.False(t, fsops.IsExtra("../../testdata/fsops/classify/Movie.Title.2024.1080p.WEB-DL.mkv"))
}

func TestIsSampleMatchesTheFilenameSignatureRegardlessOfSize(t *testing.T) {
	require.True(t, fsops.IsSample("../../testdata/fsops/classify/Movie.Title.2024.Sample.mkv", 5*1024*1024*1024))
}

func TestIsSampleFlagsASmallMediaFileWithoutTheSignature(t *testing.T) {
	require.True(t, fsops.IsSample("Movie.Title.2024.1080p.mkv", 10*1024*1024), "under the 50 MiB heuristic")
	require.False(t, fsops.IsSample("Movie.Title.2024.1080p.mkv", 2*1024*1024*1024), "a real-sized file is never a sample by size alone")
	require.False(t, fsops.IsSample("readme.txt", 10), "size alone never flags a non-media extension")
}
